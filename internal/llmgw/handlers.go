package llmgw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

const maxGatewayRequestBodyBytes = 16 << 20 // 16 MiB cap on chat payloads

// RegisterRoutes mounts all ColabHive v1.1 LLM control, OpenAI, Anthropic, and Ollama routes onto mux.
func RegisterRoutes(mux *http.ServeMux, svc *Service) {
	if mux == nil {
		return
	}
	h := newHTTPHandler(svc)

	// ColabHive v1.1 Control & Built-In Chat Routes
	mux.HandleFunc("GET /api/v1/llm/catalog", h.handleGetCatalog)
	mux.HandleFunc("GET /api/v1/llm/status", h.handleGetStatus)
	mux.HandleFunc("POST /api/v1/llm/start", h.handleStart)
	mux.HandleFunc("POST /api/v1/llm/stop", h.handleStop)
	mux.HandleFunc("POST /api/v1/llm/chat/completions", h.handleOpenAIChatCompletions)

	// OpenAI Compatible Local Gateway (`/v1/*`)
	mux.HandleFunc("GET /v1/models", h.handleOpenAIModels)
	mux.HandleFunc("GET /v1/models/{model}", h.handleOpenAIGetModel)
	mux.HandleFunc("POST /v1/chat/completions", h.handleOpenAIChatCompletions)
	mux.HandleFunc("POST /v1/responses", h.handleOpenAIResponses)

	// Anthropic Messages Compatible Local Gateway (`/v1/messages`, `/v1/messages/count_tokens`)
	mux.HandleFunc("POST /v1/messages", h.handleAnthropicMessages)
	mux.HandleFunc("POST /v1/messages/count_tokens", h.handleAnthropicCountTokens)

	// Ollama Native Compatible Local Gateway (`/api/tags`, `/api/ps`, `/api/show`, `/api/chat`, `/api/generate`, `/api/version`)
	mux.HandleFunc("GET /api/tags", h.handleOllamaTags)
	mux.HandleFunc("GET /api/ps", h.handleOllamaPS)
	mux.HandleFunc("GET /api/version", h.handleOllamaVersion)
	mux.HandleFunc("POST /api/show", h.handleOllamaShow)
	mux.HandleFunc("POST /api/chat", h.handleOllamaChat)
	mux.HandleFunc("POST /api/generate", h.handleOllamaGenerate)
}

// RegisterRoutes mounts all LLM Gateway routes onto mux.
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	RegisterRoutes(mux, s)
}

type httpHandler struct {
	svc *Service
}

func newHTTPHandler(svc *Service) *httpHandler {
	return &httpHandler{svc: svc}
}

// ============================================================================
// Control Endpoints (`/api/v1/llm/*`)
// ============================================================================

func (h *httpHandler) handleGetCatalog(w http.ResponseWriter, _ *http.Request) {
	if h.svc == nil {
		writeJSON(w, http.StatusOK, DefaultCatalog())
		return
	}
	writeJSON(w, http.StatusOK, h.svc.Catalog())
}

func (h *httpHandler) handleGetStatus(w http.ResponseWriter, _ *http.Request) {
	if h.svc == nil {
		writeControlError(w, http.StatusServiceUnavailable, "LLM gateway service is not initialized")
		return
	}
	writeJSON(w, http.StatusOK, h.svc.Status())
}

func (h *httpHandler) handleStart(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeControlError(w, http.StatusServiceUnavailable, "LLM gateway service is not initialized")
		return
	}
	var req models.LLMStartRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeControlError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	st, err := h.svc.Start(r.Context(), req)
	if err != nil {
		writeControlError(w, classifyGatewayHTTPStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *httpHandler) handleStop(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeControlError(w, http.StatusServiceUnavailable, "LLM gateway service is not initialized")
		return
	}
	st, err := h.svc.Stop(r.Context())
	if err != nil {
		writeControlError(w, classifyGatewayHTTPStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ============================================================================
// OpenAI Compatible Protocol (`/v1/models`, `/v1/chat/completions`, `/api/v1/llm/chat/completions`)
// ============================================================================

type openAIWireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Name    string          `json:"name,omitempty"`
}

type openAIChatRequest struct {
	Model               string              `json:"model"`
	Messages            []openAIWireMessage `json:"messages"`
	Stream              bool                `json:"stream"`
	Temperature         *float64            `json:"temperature,omitempty"`
	TopP                *float64            `json:"top_p,omitempty"`
	MaxTokens           int                 `json:"max_tokens,omitempty"`
	MaxCompletionTokens int                 `json:"max_completion_tokens,omitempty"`
	Stop                json.RawMessage     `json:"stop,omitempty"`
	Think               *bool               `json:"think,omitempty"`
	EnableThinking      *bool               `json:"enable_thinking,omitempty"`
	ReasoningEffort     string              `json:"reasoning_effort,omitempty"`
}

func resolveThinkFlag(think, enableThinking *bool, effort string) *bool {
	if think != nil {
		v := *think
		return &v
	}
	if enableThinking != nil {
		v := *enableThinking
		return &v
	}
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "off", "disabled", "false", "0":
		v := false
		return &v
	case "low", "medium", "high", "enabled", "on", "true", "1":
		v := true
		return &v
	default:
		return nil
	}
}

// thinkingStreamSplitter splits incremental chunks containing `<think>...</think>` into
// structured reasoning vs answer callbacks across arbitrary chunk boundaries.
type thinkingStreamSplitter struct {
	inThink bool
	pending string
	onThink func(string) error
	onText  func(string) error
}

func newThinkingStreamSplitter(onThink, onText func(string) error) *thinkingStreamSplitter {
	return &thinkingStreamSplitter{onThink: onThink, onText: onText}
}

func (ts *thinkingStreamSplitter) Push(chunk string) error {
	if chunk == "" {
		return nil
	}
	ts.pending += chunk
	for len(ts.pending) > 0 {
		if !ts.inThink {
			const openTag = "<think>"
			if idx := strings.Index(ts.pending, openTag); idx >= 0 {
				before := ts.pending[:idx]
				if before != "" && ts.onText != nil {
					if err := ts.onText(before); err != nil {
						return err
					}
				}
				ts.pending = strings.TrimPrefix(ts.pending[idx+len(openTag):], "\n")
				ts.inThink = true
				continue
			}
			// Keep at most len(openTag)-1 trailing bytes if they could start `<think>`.
			hold := partialTagSuffixLen(ts.pending, openTag)
			emit := ts.pending[:len(ts.pending)-hold]
			ts.pending = ts.pending[len(ts.pending)-hold:]
			if emit != "" && ts.onText != nil {
				if err := ts.onText(emit); err != nil {
					return err
				}
			}
			return nil
		}
		const closeTag = "</think>"
		if idx := strings.Index(ts.pending, closeTag); idx >= 0 {
			inside := strings.TrimSuffix(ts.pending[:idx], "\n")
			if inside != "" && ts.onThink != nil {
				if err := ts.onThink(inside); err != nil {
					return err
				}
			}
			rest := ts.pending[idx+len(closeTag):]
			rest = strings.TrimLeft(rest, "\r\n")
			ts.pending = rest
			ts.inThink = false
			continue
		}
		hold := partialTagSuffixLen(ts.pending, closeTag)
		emit := ts.pending[:len(ts.pending)-hold]
		ts.pending = ts.pending[len(ts.pending)-hold:]
		if emit != "" && ts.onThink != nil {
			if err := ts.onThink(emit); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

func (ts *thinkingStreamSplitter) Flush() error {
	if ts.pending == "" {
		return nil
	}
	rem := ts.pending
	ts.pending = ""
	if ts.inThink {
		rem = strings.TrimSuffix(rem, "\n")
		if rem != "" && ts.onThink != nil {
			return ts.onThink(rem)
		}
		return nil
	}
	if ts.onText != nil {
		return ts.onText(rem)
	}
	return nil
}

func partialTagSuffixLen(s, tag string) int {
	maxCheck := len(tag) - 1
	if maxCheck > len(s) {
		maxCheck = len(s)
	}
	for k := maxCheck; k >= 1; k-- {
		if strings.HasSuffix(s, tag[:k]) {
			return k
		}
	}
	return 0
}

func splitThinkingAndAnswer(raw string) (thinking string, answer string) {
	var tb, ab strings.Builder
	sp := newThinkingStreamSplitter(
		func(s string) error { tb.WriteString(s); return nil },
		func(s string) error { ab.WriteString(s); return nil },
	)
	_ = sp.Push(raw)
	_ = sp.Flush()
	return strings.TrimSpace(tb.String()), strings.TrimSpace(ab.String())
}

func formatOpenAIModelObject(sp models.LLMModelSpec, created int64) map[string]any {
	return map[string]any{
		"id":       sp.ID,
		"object":   "model",
		"created":  created,
		"owned_by": "colabhive-" + strings.ToLower(sp.ProviderFamily),
		"root":     sp.OllamaTag,
		"meta": map[string]any{
			"display_name":    sp.DisplayName,
			"display_name_zh": sp.DisplayNameZh,
			"min_vram_gb":     sp.MinVRAMGB,
			"free_tier_ready": sp.FreeTierReady,
			"recommended_gpu": sp.RecommendedGPU,
			"ollama_tag":      sp.OllamaTag,
		},
	}
}

func (h *httpHandler) catalogWithActiveCustom() []models.LLMModelSpec {
	specs := DefaultCatalog()
	if h.svc != nil {
		specs = h.svc.Catalog()
		if active, ok := h.svc.ActiveModelSpec(); ok {
			if _, inCatalog := LookupModelSpecInCatalog(active.ID, specs); !inCatalog {
				specs = append([]models.LLMModelSpec{active}, specs...)
			}
		}
	}
	return specs
}

// deployedModelsForWireList returns ONLY the currently deployed model(s) on `/v1/models`
// and `/api/tags` so desktop clients (Cherry Studio, SillyTavern, Cursor, Open WebUI) only
// display models that are actually deployed and ready to serve right now.
// Pass `?catalog=1` to inspect the full built-in catalog over the OpenAI/Ollama discovery routes.
func (h *httpHandler) deployedModelsForWireList(r *http.Request) []models.LLMModelSpec {
	if r != nil && (r.URL.Query().Get("catalog") == "1" || r.URL.Query().Get("all") == "1") {
		return h.catalogWithActiveCustom()
	}
	if h.svc != nil {
		st := h.svc.Status()
		if st.Running {
			if active, ok := h.svc.ActiveModelSpec(); ok {
				return []models.LLMModelSpec{active}
			}
		}
	}
	return []models.LLMModelSpec{}
}

func (h *httpHandler) resolveModel(raw string) (models.LLMModelSpec, error) {
	if h.svc != nil {
		return h.svc.ResolveModel(raw)
	}
	return ResolveModelSpec(raw)
}

func (h *httpHandler) handleOpenAIModels(w http.ResponseWriter, r *http.Request) {
	specs := h.deployedModelsForWireList(r)
	created := int64(1763800000)
	data := make([]map[string]any, 0, len(specs))
	for _, sp := range specs {
		data = append(data, formatOpenAIModelObject(sp, created))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

func (h *httpHandler) handleOpenAIGetModel(w http.ResponseWriter, r *http.Request) {
	modelID := strings.TrimSpace(r.PathValue("model"))
	sp, err := h.resolveModel(modelID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{
				"message": err.Error(),
				"type":    "invalid_request_error",
				"code":    "model_not_found",
			},
			"message": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, formatOpenAIModelObject(sp, 1763800000))
}

func (h *httpHandler) handleOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	isControlAlias := strings.HasPrefix(r.URL.Path, "/api/v1/llm/")
	writeErr := func(code int, errType, msg string) {
		if isControlAlias {
			writeControlError(w, code, msg)
			return
		}
		writeJSON(w, code, map[string]any{
			"error": map[string]any{
				"message": msg,
				"type":    errType,
				"code":    errType,
			},
			"message": msg,
		})
	}

	if h.svc == nil {
		writeErr(http.StatusServiceUnavailable, "service_unavailable", "LLM gateway service is not initialized")
		return
	}

	var wireReq openAIChatRequest
	rawBody, err := decodeRequiredJSONWithRaw(r, &wireReq)
	if err != nil {
		writeErr(http.StatusBadRequest, "invalid_request_error", "invalid chat completion JSON: "+err.Error())
		return
	}
	if len(wireReq.Messages) == 0 {
		writeErr(http.StatusBadRequest, "invalid_request_error", "messages array must not be empty")
		return
	}

	msgs := make([]ChatMessage, 0, len(wireReq.Messages))
	for _, wm := range wireReq.Messages {
		role := strings.TrimSpace(wm.Role)
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, ChatMessage{
			Role:    role,
			Content: extractFlexibleContentText(wm.Content),
		})
	}

	maxTok := wireReq.MaxTokens
	if maxTok <= 0 && wireReq.MaxCompletionTokens > 0 {
		maxTok = wireReq.MaxCompletionTokens
	}
	inferReq := ChatInferRequest{
		Model:       wireReq.Model,
		Messages:    msgs,
		Temperature: wireReq.Temperature,
		TopP:        wireReq.TopP,
		MaxTokens:   maxTok,
		Stop:        parseStopSequences(wireReq.Stop),
		Think:       resolveThinkFlag(wireReq.Think, wireReq.EnableThinking, wireReq.ReasoningEffort),
	}

	// Fast synchronous model validation & warm mismatch guard (400 / 409) BEFORE opening SSE stream.
	if err := h.svc.ValidateChatModel(inferReq.Model); err != nil {
		writeErr(classifyGatewayHTTPStatus(err), openAIErrorType(err), err.Error())
		return
	}

	// Live GPU fast-path: transparently pass through to Ollama's native `/v1/chat/completions`
	// (full native reasoning, tool calling, vision, response_format & prompt caching).
	if handled, proxyErr := h.svc.ProxyNativeOllamaV1(r.Context(), "/v1/chat/completions", wireReq.Model, rawBody, r.Header, w); handled {
		return
	} else if proxyErr != nil && !errors.Is(proxyErr, errNativePassThroughUnsupported) {
		writeErr(classifyGatewayHTTPStatus(proxyErr), openAIErrorType(proxyErr), proxyErr.Error())
		return
	}

	cmplID := models.NewPrefixedID("chatcmpl-")
	createdUnix := time.Now().UTC().Unix()

	if !wireReq.Stream {
		resp, err := h.svc.Chat(r.Context(), inferReq, nil)
		if err != nil {
			writeErr(classifyGatewayHTTPStatus(err), openAIErrorType(err), err.Error())
			return
		}
		thinkText, answerText := splitThinkingAndAnswer(resp.Content)
		msgObj := map[string]any{
			"role":    "assistant",
			"content": answerText,
		}
		if thinkText != "" {
			msgObj["reasoning_content"] = thinkText
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id":                 cmplID,
			"object":             "chat.completion",
			"created":            createdUnix,
			"model":              resp.ModelID,
			"system_fingerprint": "fp_colabhive_v11",
			"choices": []map[string]any{
				{
					"index":         0,
					"message":       msgObj,
					"finish_reason": "stop",
				},
			},
			"usage": map[string]any{
				"prompt_tokens":     resp.PromptTokens,
				"completion_tokens": resp.CompletionTokens,
				"total_tokens":      resp.TotalTokens,
			},
		})
		return
	}

	// Streaming SSE mode (`stream: true`):
	// Flush headers + `: keepalive\n\n` IMMEDIATELY before cold start / inference so reverse proxies
	// (e.g. Caddy 120s response-header timeout) never drop the connection!
	sse := startSSEStream(r.Context(), w, h.svc.sseKeepaliveInterval())
	defer sse.StopKeepalive()

	roleSent := false
	resolvedModel := ""

	ensureRoleChunk := func(fallbackModel string) error {
		if roleSent {
			return nil
		}
		roleSent = true
		if st := h.svc.Status(); st.ActiveModelID != "" {
			resolvedModel = st.ActiveModelID
		} else if fallbackModel != "" {
			resolvedModel = fallbackModel
		} else {
			resolvedModel = DefaultModelID
		}
		return sse.WriteDataJSON(map[string]any{
			"id":      cmplID,
			"object":  "chat.completion.chunk",
			"created": createdUnix,
			"model":   resolvedModel,
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         map[string]any{"role": "assistant", "content": ""},
					"finish_reason": nil,
				},
			},
		})
	}

	splitter := newThinkingStreamSplitter(
		func(thinkDelta string) error {
			if err := ensureRoleChunk(""); err != nil {
				return err
			}
			return sse.WriteDataJSON(map[string]any{
				"id":      cmplID,
				"object":  "chat.completion.chunk",
				"created": createdUnix,
				"model":   resolvedModel,
				"choices": []map[string]any{
					{
						"index":         0,
						"delta":         map[string]any{"reasoning_content": thinkDelta},
						"finish_reason": nil,
					},
				},
			})
		},
		func(textDelta string) error {
			if err := ensureRoleChunk(""); err != nil {
				return err
			}
			return sse.WriteDataJSON(map[string]any{
				"id":      cmplID,
				"object":  "chat.completion.chunk",
				"created": createdUnix,
				"model":   resolvedModel,
				"choices": []map[string]any{
					{
						"index":         0,
						"delta":         map[string]any{"content": textDelta},
						"finish_reason": nil,
					},
				},
			})
		},
	)

	resp, err := h.svc.Chat(r.Context(), inferReq, func(piece string) error {
		return splitter.Push(piece)
	})
	if err == nil {
		err = splitter.Flush()
	}
	sse.StopKeepalive()
	if err != nil {
		errType := openAIErrorType(err)
		statusCode := classifyGatewayHTTPStatus(err)
		_ = sse.WriteEventJSON("error", map[string]any{
			"error": map[string]any{
				"message": err.Error(),
				"type":    errType,
				"code":    errType,
				"status":  statusCode,
			},
			"message": err.Error(),
		})
		return
	}

	finalModel := resp.ModelID
	if finalModel == "" {
		finalModel = resolvedModel
	}
	if err := ensureRoleChunk(finalModel); err != nil {
		return
	}
	_ = sse.WriteDataJSON(map[string]any{
		"id":      cmplID,
		"object":  "chat.completion.chunk",
		"created": createdUnix,
		"model":   finalModel,
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         map[string]any{},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     resp.PromptTokens,
			"completion_tokens": resp.CompletionTokens,
			"total_tokens":      resp.TotalTokens,
		},
	})
	_ = sse.WriteRaw("data: [DONE]\n\n")
}

// ============================================================================
// OpenAI Responses API Compatible Protocol (`POST /v1/responses`)
// ============================================================================

type openAIResponsesRequest struct {
	Model           string          `json:"model"`
	Instructions    string          `json:"instructions,omitempty"`
	Input           json.RawMessage `json:"input"`
	Stream          bool            `json:"stream"`
	Temperature     *float64        `json:"temperature,omitempty"`
	TopP            *float64        `json:"top_p,omitempty"`
	MaxOutputTokens int             `json:"max_output_tokens,omitempty"`
	Think           *bool           `json:"think,omitempty"`
	Reasoning       *struct {
		Effort string `json:"effort,omitempty"`
	} `json:"reasoning,omitempty"`
}

func parseResponsesInputMessages(instructions string, rawInput json.RawMessage) []ChatMessage {
	msgs := make([]ChatMessage, 0, 4)
	if sys := strings.TrimSpace(instructions); sys != "" {
		msgs = append(msgs, ChatMessage{Role: "system", Content: sys})
	}
	trimmed := strings.TrimSpace(string(rawInput))
	if trimmed == "" || trimmed == "null" {
		return msgs
	}
	if strings.HasPrefix(trimmed, "\"") {
		var text string
		if err := json.Unmarshal(rawInput, &text); err == nil && strings.TrimSpace(text) != "" {
			msgs = append(msgs, ChatMessage{Role: "user", Content: text})
		}
		return msgs
	}
	if strings.HasPrefix(trimmed, "[") {
		var items []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Text    string          `json:"text"`
		}
		if err := json.Unmarshal(rawInput, &items); err == nil {
			for _, it := range items {
				role := strings.TrimSpace(it.Role)
				if role == "" {
					role = "user"
				}
				if role == "developer" {
					role = "system"
				}
				text := strings.TrimSpace(extractFlexibleContentText(it.Content))
				if text == "" {
					text = strings.TrimSpace(it.Text)
				}
				if text != "" {
					msgs = append(msgs, ChatMessage{Role: role, Content: text})
				}
			}
			return msgs
		}
	}
	if strings.HasPrefix(trimmed, "{") {
		var item struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Text    string          `json:"text"`
		}
		if err := json.Unmarshal(rawInput, &item); err == nil {
			role := strings.TrimSpace(item.Role)
			if role == "" {
				role = "user"
			}
			if role == "developer" {
				role = "system"
			}
			text := strings.TrimSpace(extractFlexibleContentText(item.Content))
			if text == "" {
				text = strings.TrimSpace(item.Text)
			}
			if text != "" {
				msgs = append(msgs, ChatMessage{Role: role, Content: text})
			}
			return msgs
		}
	}
	if fallback := strings.TrimSpace(extractFlexibleContentText(rawInput)); fallback != "" {
		msgs = append(msgs, ChatMessage{Role: "user", Content: fallback})
	}
	return msgs
}

func buildResponsesObject(respID, itemID, modelID string, createdUnix int64, rawContent string, promptTok, completionTok, totalTok int) map[string]any {
	thinkText, answerText := splitThinkingAndAnswer(rawContent)
	outputItems := make([]map[string]any, 0, 2)
	if thinkText != "" {
		outputItems = append(outputItems, map[string]any{
			"id":     "rs_" + strings.TrimPrefix(itemID, "msg_"),
			"type":   "reasoning",
			"status": "completed",
			"summary": []map[string]any{
				{
					"type": "summary_text",
					"text": thinkText,
				},
			},
		})
	}
	outputItems = append(outputItems, map[string]any{
		"id":     itemID,
		"type":   "message",
		"status": "completed",
		"role":   "assistant",
		"content": []map[string]any{
			{
				"type":        "output_text",
				"text":        answerText,
				"annotations": []any{},
			},
		},
	})
	return map[string]any{
		"id":          respID,
		"object":      "response",
		"created_at":  createdUnix,
		"status":      "completed",
		"model":       modelID,
		"output_text": answerText,
		"output":      outputItems,
		"usage": map[string]any{
			"input_tokens":  promptTok,
			"output_tokens": completionTok,
			"total_tokens":  totalTok,
		},
	}
}

func (h *httpHandler) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	writeErr := func(code int, errType, msg string) {
		writeJSON(w, code, map[string]any{
			"error": map[string]any{
				"message": msg,
				"type":    errType,
				"code":    errType,
			},
			"message": msg,
		})
	}

	if h.svc == nil {
		writeErr(http.StatusServiceUnavailable, "service_unavailable", "LLM gateway service is not initialized")
		return
	}

	var wireReq openAIResponsesRequest
	rawBody, err := decodeRequiredJSONWithRaw(r, &wireReq)
	if err != nil {
		writeErr(http.StatusBadRequest, "invalid_request_error", "invalid responses JSON: "+err.Error())
		return
	}
	msgs := parseResponsesInputMessages(wireReq.Instructions, wireReq.Input)
	if len(msgs) == 0 {
		writeErr(http.StatusBadRequest, "invalid_request_error", "input must not be empty")
		return
	}

	respEffort := ""
	if wireReq.Reasoning != nil {
		respEffort = wireReq.Reasoning.Effort
	}
	inferReq := ChatInferRequest{
		Model:       wireReq.Model,
		Messages:    msgs,
		Temperature: wireReq.Temperature,
		TopP:        wireReq.TopP,
		MaxTokens:   wireReq.MaxOutputTokens,
		Think:       resolveThinkFlag(wireReq.Think, nil, respEffort),
	}

	if err := h.svc.ValidateChatModel(inferReq.Model); err != nil {
		writeErr(classifyGatewayHTTPStatus(err), openAIErrorType(err), err.Error())
		return
	}

	// Live GPU fast-path: transparently pass through to Ollama's native `/v1/responses`
	// with automatic `reasoning_summary_part.added` / `.done` shim for Cherry Studio & Vercel AI SDK.
	if handled, proxyErr := h.svc.ProxyNativeOllamaV1(r.Context(), "/v1/responses", wireReq.Model, rawBody, r.Header, w); handled {
		return
	} else if proxyErr != nil && !errors.Is(proxyErr, errNativePassThroughUnsupported) {
		writeErr(classifyGatewayHTTPStatus(proxyErr), openAIErrorType(proxyErr), proxyErr.Error())
		return
	}

	respID := models.NewPrefixedID("resp_")
	itemID := models.NewPrefixedID("msg_")
	rsID := "rs_" + strings.TrimPrefix(itemID, "msg_")
	createdUnix := time.Now().UTC().Unix()

	if !wireReq.Stream {
		resp, err := h.svc.Chat(r.Context(), inferReq, nil)
		if err != nil {
			writeErr(classifyGatewayHTTPStatus(err), openAIErrorType(err), err.Error())
			return
		}
		modelOut := resp.ModelID
		if modelOut == "" {
			modelOut = DefaultModelID
		}
		writeJSON(w, http.StatusOK, buildResponsesObject(
			respID,
			itemID,
			modelOut,
			createdUnix,
			resp.Content,
			resp.PromptTokens,
			resp.CompletionTokens,
			resp.TotalTokens,
		))
		return
	}

	sse := startSSEStream(r.Context(), w, h.svc.sseKeepaliveInterval())
	defer sse.StopKeepalive()

	seq := 0
	nextSeq := func() int {
		cur := seq
		seq++
		return cur
	}
	createdSent := false
	resolvedModel := ""
	nextOutputIdx := 0
	reasoningStarted := false
	reasoningClosed := false
	reasoningOutputIdx := -1
	var reasoningAccum strings.Builder
	messageStarted := false
	messageOutputIdx := -1

	ensureCreated := func(fallbackModel string) error {
		if createdSent {
			return nil
		}
		createdSent = true
		if st := h.svc.Status(); st.ActiveModelID != "" {
			resolvedModel = st.ActiveModelID
		} else if fallbackModel != "" {
			resolvedModel = fallbackModel
		} else {
			resolvedModel = DefaultModelID
		}
		return sse.WriteEventJSON("response.created", map[string]any{
			"type":            "response.created",
			"sequence_number": nextSeq(),
			"response": map[string]any{
				"id":         respID,
				"object":     "response",
				"created_at": createdUnix,
				"status":     "in_progress",
				"model":      resolvedModel,
				"output":     []any{},
			},
		})
	}

	ensureReasoningStarted := func() error {
		if reasoningStarted {
			return nil
		}
		if err := ensureCreated(""); err != nil {
			return err
		}
		reasoningStarted = true
		reasoningOutputIdx = nextOutputIdx
		nextOutputIdx++
		if err := sse.WriteEventJSON("response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": nextSeq(),
			"output_index":    reasoningOutputIdx,
			"item": map[string]any{
				"id":      rsID,
				"type":    "reasoning",
				"status":  "in_progress",
				"summary": []any{},
			},
		}); err != nil {
			return err
		}
		return sse.WriteEventJSON("response.reasoning_summary_part.added", map[string]any{
			"type":            "response.reasoning_summary_part.added",
			"sequence_number": nextSeq(),
			"item_id":         rsID,
			"output_index":    reasoningOutputIdx,
			"summary_index":   0,
			"part": map[string]any{
				"type": "summary_text",
				"text": "",
			},
		})
	}

	closeReasoningIfOpen := func() error {
		if !reasoningStarted || reasoningClosed {
			return nil
		}
		reasoningClosed = true
		fullThink := strings.TrimSpace(reasoningAccum.String())
		if err := sse.WriteEventJSON("response.reasoning_summary_text.done", map[string]any{
			"type":            "response.reasoning_summary_text.done",
			"sequence_number": nextSeq(),
			"item_id":         rsID,
			"output_index":    reasoningOutputIdx,
			"summary_index":   0,
			"text":            fullThink,
		}); err != nil {
			return err
		}
		if err := sse.WriteEventJSON("response.reasoning_summary_part.done", map[string]any{
			"type":            "response.reasoning_summary_part.done",
			"sequence_number": nextSeq(),
			"item_id":         rsID,
			"output_index":    reasoningOutputIdx,
			"summary_index":   0,
			"part": map[string]any{
				"type": "summary_text",
				"text": fullThink,
			},
		}); err != nil {
			return err
		}
		return sse.WriteEventJSON("response.output_item.done", map[string]any{
			"type":            "response.output_item.done",
			"sequence_number": nextSeq(),
			"output_index":    reasoningOutputIdx,
			"item": map[string]any{
				"id":     rsID,
				"type":   "reasoning",
				"status": "completed",
				"summary": []map[string]any{
					{
						"type": "summary_text",
						"text": fullThink,
					},
				},
			},
		})
	}

	ensureMessageStarted := func(fallbackModel string) error {
		if messageStarted {
			return nil
		}
		if err := ensureCreated(fallbackModel); err != nil {
			return err
		}
		if err := closeReasoningIfOpen(); err != nil {
			return err
		}
		messageStarted = true
		messageOutputIdx = nextOutputIdx
		nextOutputIdx++
		if err := sse.WriteEventJSON("response.output_item.added", map[string]any{
			"type":            "response.output_item.added",
			"sequence_number": nextSeq(),
			"output_index":    messageOutputIdx,
			"item": map[string]any{
				"id":      itemID,
				"type":    "message",
				"status":  "in_progress",
				"role":    "assistant",
				"content": []any{},
			},
		}); err != nil {
			return err
		}
		return sse.WriteEventJSON("response.content_part.added", map[string]any{
			"type":            "response.content_part.added",
			"sequence_number": nextSeq(),
			"item_id":         itemID,
			"output_index":    messageOutputIdx,
			"content_index":   0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})
	}

	respSplitter := newThinkingStreamSplitter(
		func(thinkDelta string) error {
			if err := ensureReasoningStarted(); err != nil {
				return err
			}
			reasoningAccum.WriteString(thinkDelta)
			return sse.WriteEventJSON("response.reasoning_summary_text.delta", map[string]any{
				"type":            "response.reasoning_summary_text.delta",
				"sequence_number": nextSeq(),
				"item_id":         rsID,
				"output_index":    reasoningOutputIdx,
				"summary_index":   0,
				"delta":           thinkDelta,
			})
		},
		func(textDelta string) error {
			if err := ensureMessageStarted(""); err != nil {
				return err
			}
			return sse.WriteEventJSON("response.output_text.delta", map[string]any{
				"type":            "response.output_text.delta",
				"sequence_number": nextSeq(),
				"item_id":         itemID,
				"output_index":    messageOutputIdx,
				"content_index":   0,
				"delta":           textDelta,
			})
		},
	)

	resp, err := h.svc.Chat(r.Context(), inferReq, func(piece string) error {
		return respSplitter.Push(piece)
	})
	if err == nil {
		err = respSplitter.Flush()
	}
	sse.StopKeepalive()
	if err != nil {
		_ = sse.WriteEventJSON("error", map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    openAIErrorType(err),
				"message": err.Error(),
			},
		})
		return
	}

	finalModel := resp.ModelID
	if finalModel == "" {
		finalModel = resolvedModel
	}
	if err := ensureMessageStarted(finalModel); err != nil {
		return
	}
	_, cleanRespText := splitThinkingAndAnswer(resp.Content)
	_ = sse.WriteEventJSON("response.output_text.done", map[string]any{
		"type":            "response.output_text.done",
		"sequence_number": nextSeq(),
		"item_id":         itemID,
		"output_index":    messageOutputIdx,
		"content_index":   0,
		"text":            cleanRespText,
	})
	_ = sse.WriteEventJSON("response.content_part.done", map[string]any{
		"type":            "response.content_part.done",
		"sequence_number": nextSeq(),
		"item_id":         itemID,
		"output_index":    messageOutputIdx,
		"content_index":   0,
		"part": map[string]any{
			"type":        "output_text",
			"text":        cleanRespText,
			"annotations": []any{},
		},
	})
	_ = sse.WriteEventJSON("response.output_item.done", map[string]any{
		"type":            "response.output_item.done",
		"sequence_number": nextSeq(),
		"output_index":    messageOutputIdx,
		"item": map[string]any{
			"id":     itemID,
			"type":   "message",
			"status": "completed",
			"role":   "assistant",
			"content": []map[string]any{
				{
					"type":        "output_text",
					"text":        cleanRespText,
					"annotations": []any{},
				},
			},
		},
	})
	_ = sse.WriteEventJSON("response.completed", map[string]any{
		"type":            "response.completed",
		"sequence_number": nextSeq(),
		"response": buildResponsesObject(
			respID,
			itemID,
			finalModel,
			createdUnix,
			resp.Content,
			resp.PromptTokens,
			resp.CompletionTokens,
			resp.TotalTokens,
		),
	})
}

// ============================================================================
// Anthropic Messages Compatible Protocol (`POST /v1/messages`)
// ============================================================================

type anthropicWireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicMessagesRequest struct {
	Model         string                 `json:"model"`
	System        json.RawMessage        `json:"system,omitempty"`
	Messages      []anthropicWireMessage `json:"messages"`
	MaxTokens     int                    `json:"max_tokens,omitempty"`
	Stream        bool                   `json:"stream"`
	Temperature   *float64               `json:"temperature,omitempty"`
	TopP          *float64               `json:"top_p,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
	Think         *bool                  `json:"think,omitempty"`
	Thinking      *struct {
		Type string `json:"type,omitempty"`
	} `json:"thinking,omitempty"`
}

func (h *httpHandler) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	writeAnthropicErr := func(code int, errType, msg string) {
		writeJSON(w, code, map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    errType,
				"message": msg,
			},
			"message": msg,
		})
	}

	if h.svc == nil {
		writeAnthropicErr(http.StatusServiceUnavailable, "api_error", "LLM gateway service is not initialized")
		return
	}

	var wireReq anthropicMessagesRequest
	rawBody, err := decodeRequiredJSONWithRaw(r, &wireReq)
	if err != nil {
		writeAnthropicErr(http.StatusBadRequest, "invalid_request_error", "invalid Anthropic messages JSON: "+err.Error())
		return
	}
	if len(wireReq.Messages) == 0 {
		writeAnthropicErr(http.StatusBadRequest, "invalid_request_error", "messages array must not be empty")
		return
	}

	msgs := make([]ChatMessage, 0, len(wireReq.Messages)+1)
	if sysText := strings.TrimSpace(extractFlexibleContentText(wireReq.System)); sysText != "" {
		msgs = append(msgs, ChatMessage{Role: "system", Content: sysText})
	}
	for _, am := range wireReq.Messages {
		role := strings.TrimSpace(am.Role)
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, ChatMessage{
			Role:    role,
			Content: extractFlexibleContentText(am.Content),
		})
	}

	maxTok := wireReq.MaxTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	anthThinkMode := ""
	if wireReq.Thinking != nil {
		anthThinkMode = wireReq.Thinking.Type
	}
	inferReq := ChatInferRequest{
		Model:       wireReq.Model,
		Messages:    msgs,
		Temperature: wireReq.Temperature,
		TopP:        wireReq.TopP,
		MaxTokens:   maxTok,
		Stop:        wireReq.StopSequences,
		Think:       resolveThinkFlag(wireReq.Think, nil, anthThinkMode),
	}

	// Fast synchronous model validation & warm mismatch guard (400 / 409) BEFORE opening SSE stream.
	if err := h.svc.ValidateChatModel(inferReq.Model); err != nil {
		writeAnthropicErr(classifyGatewayHTTPStatus(err), anthropicErrorType(err), err.Error())
		return
	}

	// Live GPU fast-path: transparently pass through to Ollama's native `/v1/messages`
	// (full native Claude `thinking_delta`, `text_delta`, `tool_use`, and prompt caching).
	if handled, proxyErr := h.svc.ProxyNativeOllamaV1(r.Context(), "/v1/messages", wireReq.Model, rawBody, r.Header, w); handled {
		return
	} else if proxyErr != nil && !errors.Is(proxyErr, errNativePassThroughUnsupported) {
		writeAnthropicErr(classifyGatewayHTTPStatus(proxyErr), anthropicErrorType(proxyErr), proxyErr.Error())
		return
	}

	msgID := models.NewPrefixedID("msg_")
	estInputTokens := estimatePromptTokens(msgs)

	if !wireReq.Stream {
		resp, err := h.svc.Chat(r.Context(), inferReq, nil)
		if err != nil {
			writeAnthropicErr(classifyGatewayHTTPStatus(err), anthropicErrorType(err), err.Error())
			return
		}
		modelOut := resp.ModelID
		if modelOut == "" {
			modelOut = DefaultModelID
		}
		thinkText, answerText := splitThinkingAndAnswer(resp.Content)
		blocks := make([]map[string]any, 0, 2)
		if thinkText != "" {
			blocks = append(blocks, map[string]any{
				"type":     "thinking",
				"thinking": thinkText,
			})
		}
		blocks = append(blocks, map[string]any{
			"type": "text",
			"text": answerText,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"model":         modelOut,
			"content":       blocks,
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  resp.PromptTokens,
				"output_tokens": resp.CompletionTokens,
			},
		})
		return
	}

	// Streaming Anthropic SSE: flush headers + `: keepalive\n\n` immediately before cold start/inference!
	sse := startSSEStream(r.Context(), w, h.svc.sseKeepaliveInterval())
	defer sse.StopKeepalive()

	streamStarted := false
	activeBlockType := "" // "", "thinking", "text"
	activeBlockIdx := -1

	ensureMessageStarted := func(fallbackModel string) error {
		if streamStarted {
			return nil
		}
		streamStarted = true
		activeModel := DefaultModelID
		if st := h.svc.Status(); st.ActiveModelID != "" {
			activeModel = st.ActiveModelID
		} else if fallbackModel != "" {
			activeModel = fallbackModel
		}

		return sse.WriteEventJSON("message_start", map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            msgID,
				"type":          "message",
				"role":          "assistant",
				"model":         activeModel,
				"content":       []any{},
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  estInputTokens,
					"output_tokens": 1,
				},
			},
		})
	}

	switchBlock := func(wantType string) error {
		if activeBlockType == wantType {
			return nil
		}
		if activeBlockType != "" {
			if err := sse.WriteEventJSON("content_block_stop", map[string]any{
				"type":  "content_block_stop",
				"index": activeBlockIdx,
			}); err != nil {
				return err
			}
		}
		activeBlockIdx++
		activeBlockType = wantType
		blockPayload := map[string]any{"type": "text", "text": ""}
		if wantType == "thinking" {
			blockPayload = map[string]any{"type": "thinking", "thinking": ""}
		}
		return sse.WriteEventJSON("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         activeBlockIdx,
			"content_block": blockPayload,
		})
	}

	anthSplitter := newThinkingStreamSplitter(
		func(thinkDelta string) error {
			if err := ensureMessageStarted(""); err != nil {
				return err
			}
			if err := switchBlock("thinking"); err != nil {
				return err
			}
			return sse.WriteEventJSON("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": activeBlockIdx,
				"delta": map[string]any{
					"type":     "thinking_delta",
					"thinking": thinkDelta,
				},
			})
		},
		func(textDelta string) error {
			if err := ensureMessageStarted(""); err != nil {
				return err
			}
			if err := switchBlock("text"); err != nil {
				return err
			}
			return sse.WriteEventJSON("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": activeBlockIdx,
				"delta": map[string]any{
					"type": "text_delta",
					"text": textDelta,
				},
			})
		},
	)

	resp, err := h.svc.Chat(r.Context(), inferReq, func(piece string) error {
		return anthSplitter.Push(piece)
	})
	if err == nil {
		err = anthSplitter.Flush()
	}
	sse.StopKeepalive()
	if err != nil {
		_ = sse.WriteEventJSON("error", map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    anthropicErrorType(err),
				"message": err.Error(),
			},
		})
		return
	}

	if err := ensureMessageStarted(resp.ModelID); err != nil {
		return
	}
	if activeBlockType == "" {
		_ = switchBlock("text")
	}
	if activeBlockType != "" {
		_ = sse.WriteEventJSON("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": activeBlockIdx,
		})
	}
	_ = sse.WriteEventJSON("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": map[string]any{
			"output_tokens": resp.CompletionTokens,
		},
	})
	_ = sse.WriteEventJSON("message_stop", map[string]any{
		"type": "message_stop",
	})
}

func (h *httpHandler) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	var wireReq anthropicMessagesRequest
	if err := decodeRequiredJSON(r, &wireReq); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "invalid_request_error", "message": err.Error()},
		})
		return
	}
	msgs := make([]ChatMessage, 0, len(wireReq.Messages)+1)
	if sysText := strings.TrimSpace(extractFlexibleContentText(wireReq.System)); sysText != "" {
		msgs = append(msgs, ChatMessage{Role: "system", Content: sysText})
	}
	for _, am := range wireReq.Messages {
		msgs = append(msgs, ChatMessage{Role: am.Role, Content: extractFlexibleContentText(am.Content)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"input_tokens": estimatePromptTokens(msgs),
	})
}

// ============================================================================
// Ollama Native Compatible Protocol (`/api/tags`, `/api/ps`, `/api/show`, `/api/chat`, `/api/generate`)
// ============================================================================

type ollamaChatRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	Stream    *bool           `json:"stream,omitempty"` // Ollama defaults to stream=true when omitted!
	KeepAlive json.RawMessage `json:"keep_alive,omitempty"`
	Options   struct {
		Temperature *float64 `json:"temperature,omitempty"`
		TopP        *float64 `json:"top_p,omitempty"`
		NumPredict  int      `json:"num_predict,omitempty"`
		Stop        []string `json:"stop,omitempty"`
	} `json:"options,omitempty"`
}

type ollamaGenerateRequest struct {
	Model     string          `json:"model"`
	Prompt    string          `json:"prompt"`
	System    string          `json:"system,omitempty"`
	Stream    *bool           `json:"stream,omitempty"` // Ollama defaults to stream=true when omitted!
	KeepAlive json.RawMessage `json:"keep_alive,omitempty"`
	Options   struct {
		Temperature *float64 `json:"temperature,omitempty"`
		TopP        *float64 `json:"top_p,omitempty"`
		NumPredict  int      `json:"num_predict,omitempty"`
		Stop        []string `json:"stop,omitempty"`
	} `json:"options,omitempty"`
}

func isZeroKeepAlive(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	switch trimmed {
	case "0", `"0"`, `"0s"`, `"0m"`:
		return true
	default:
		return false
	}
}

func (h *httpHandler) handleOllamaTags(w http.ResponseWriter, r *http.Request) {
	specs := h.deployedModelsForWireList(r)
	nowStr := time.Now().UTC().Format(time.RFC3339)

	modelsList := make([]map[string]any, 0, len(specs))
	for _, sp := range specs {
		sum := sha256.Sum256([]byte("colabhive:" + sp.OllamaTag))
		fam := strings.ToLower(sp.ProviderFamily)
		modelsList = append(modelsList, map[string]any{
			"name":        sp.OllamaTag,
			"model":       sp.OllamaTag,
			"modified_at": nowStr,
			"size":        EstimatedModelBytes(sp),
			"digest":      hex.EncodeToString(sum[:]),
			"details": map[string]any{
				"parent_model":       "",
				"format":             "gguf",
				"family":             fam,
				"families":           []string{fam},
				"parameter_size":     sp.ParameterSize,
				"quantization_level": sp.Quantization,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": modelsList,
	})
}

func (h *httpHandler) handleOllamaVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": "0.6.2",
	})
}

func (h *httpHandler) handleOllamaPS(w http.ResponseWriter, _ *http.Request) {
	running := []map[string]any{}
	if h.svc != nil {
		st := h.svc.Status()
		if sp, ok := h.svc.ActiveModelSpec(); ok && st.Running {
			sum := sha256.Sum256([]byte("colabhive:" + sp.OllamaTag))
			idleMin := st.IdleAutoStopMin
			if idleMin <= 0 {
				idleMin = 30
			}
			expires := time.Now().UTC().Add(time.Duration(idleMin) * time.Minute)
			if st.LastActiveAt != nil {
				expires = st.LastActiveAt.Add(time.Duration(idleMin) * time.Minute)
			}
			sizeBytes := EstimatedModelBytes(sp)
			fam := strings.ToLower(sp.ProviderFamily)
			running = append(running, map[string]any{
				"name":       sp.OllamaTag,
				"model":      sp.OllamaTag,
				"size":       sizeBytes,
				"size_vram":  sizeBytes,
				"digest":     hex.EncodeToString(sum[:]),
				"expires_at": expires.Format(time.RFC3339),
				"details": map[string]any{
					"format":             "gguf",
					"family":             fam,
					"families":           []string{fam},
					"parameter_size":     sp.ParameterSize,
					"quantization_level": sp.Quantization,
				},
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": running,
	})
}

func (h *httpHandler) handleOllamaShow(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Model string `json:"model"`
	}
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	target := strings.TrimSpace(req.Model)
	if target == "" {
		target = strings.TrimSpace(req.Name)
	}
	sp, err := h.resolveModel(target)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	fam := strings.ToLower(sp.ProviderFamily)
	caps := []string{"completion"}
	if strings.Contains(sp.ID, "deepseek") || strings.HasPrefix(sp.ID, "qwq") || strings.Contains(sp.ID, "qwen3") {
		caps = append(caps, "thinking")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"modelfile":    fmt.Sprintf("FROM %s\nPARAMETER num_ctx %d\n", sp.OllamaTag, sp.ContextLength),
		"parameters":   fmt.Sprintf("num_ctx %d", sp.ContextLength),
		"template":     "{{ .Prompt }}",
		"capabilities": caps,
		"details": map[string]any{
			"parent_model":       "",
			"format":             "gguf",
			"family":             fam,
			"families":           []string{fam},
			"parameter_size":     sp.ParameterSize,
			"quantization_level": sp.Quantization,
		},
		"model_info": map[string]any{
			"general.architecture": fam,
			"context_length":       sp.ContextLength,
		},
	})
}

func (h *httpHandler) handleOllamaChat(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "LLM gateway service is not initialized"})
		return
	}
	var wireReq ollamaChatRequest
	if err := decodeRequiredJSON(r, &wireReq); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid Ollama chat JSON: " + err.Error()})
		return
	}
	if len(wireReq.Messages) == 0 {
		sp, rErr := h.resolveModel(wireReq.Model)
		if rErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": rErr.Error()})
			return
		}
		targetModel := sp.ID
		tagOut := sp.OllamaTag
		if isZeroKeepAlive(wireReq.KeepAlive) {
			if _, err := h.svc.Stop(r.Context()); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"model":       tagOut,
				"created_at":  time.Now().UTC().Format(time.RFC3339Nano),
				"message":     map[string]any{"role": "assistant", "content": ""},
				"done":        true,
				"done_reason": "unload",
			})
			return
		}
		// Ollama CLI sends an empty messages list to preload a model before interactive prompt.
		if _, err := h.svc.Start(r.Context(), models.LLMStartRequest{ModelID: targetModel}); err != nil {
			writeJSON(w, classifyGatewayHTTPStatus(err), map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model":       tagOut,
			"created_at":  time.Now().UTC().Format(time.RFC3339Nano),
			"message":     map[string]any{"role": "assistant", "content": ""},
			"done":        true,
			"done_reason": "load",
		})
		return
	}

	msgs := make([]ChatMessage, 0, len(wireReq.Messages))
	for _, m := range wireReq.Messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		msgs = append(msgs, ChatMessage{Role: role, Content: m.Content})
	}

	inferReq := ChatInferRequest{
		Model:       wireReq.Model,
		Messages:    msgs,
		Temperature: wireReq.Options.Temperature,
		TopP:        wireReq.Options.TopP,
		MaxTokens:   wireReq.Options.NumPredict,
		Stop:        wireReq.Options.Stop,
	}

	if err := h.svc.ValidateChatModel(inferReq.Model); err != nil {
		writeJSON(w, classifyGatewayHTTPStatus(err), map[string]any{"error": err.Error()})
		return
	}

	stream := wireReq.Stream == nil || *wireReq.Stream
	if !stream {
		resp, err := h.svc.Chat(r.Context(), inferReq, nil)
		if err != nil {
			writeJSON(w, classifyGatewayHTTPStatus(err), map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model":      resp.OllamaTag,
			"created_at": time.Now().UTC().Format(time.RFC3339Nano),
			"message": map[string]any{
				"role":    "assistant",
				"content": resp.Content,
			},
			"done":              true,
			"done_reason":       "stop",
			"total_duration":    resp.Duration.Nanoseconds(),
			"prompt_eval_count": resp.PromptTokens,
			"eval_count":        resp.CompletionTokens,
			"eval_duration":     resp.Duration.Nanoseconds(),
		})
		return
	}

	flusher, _ := w.(http.Flusher)
	headerSent := false
	tagOut := "qwen3:8b"

	sendNDJSON := func(obj any) error {
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		b = append(b, '\n')
		if _, err := w.Write(b); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	initNDJSON := func(fallbackTag string) {
		if headerSent {
			return
		}
		headerSent = true
		if sp, ok := h.svc.ActiveModelSpec(); ok && sp.OllamaTag != "" {
			tagOut = sp.OllamaTag
		} else if fallbackTag != "" {
			tagOut = fallbackTag
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
	}

	resp, err := h.svc.Chat(r.Context(), inferReq, func(piece string) error {
		initNDJSON("")
		return sendNDJSON(map[string]any{
			"model":      tagOut,
			"created_at": time.Now().UTC().Format(time.RFC3339Nano),
			"message": map[string]any{
				"role":    "assistant",
				"content": piece,
			},
			"done": false,
		})
	})
	if err != nil {
		if !headerSent {
			writeJSON(w, classifyGatewayHTTPStatus(err), map[string]any{"error": err.Error()})
		} else {
			_ = sendNDJSON(map[string]any{"error": err.Error()})
		}
		return
	}

	initNDJSON(resp.OllamaTag)
	_ = sendNDJSON(map[string]any{
		"model":      resp.OllamaTag,
		"created_at": time.Now().UTC().Format(time.RFC3339Nano),
		"message": map[string]any{
			"role":    "assistant",
			"content": "",
		},
		"done":              true,
		"done_reason":       "stop",
		"total_duration":    resp.Duration.Nanoseconds(),
		"prompt_eval_count": resp.PromptTokens,
		"eval_count":        resp.CompletionTokens,
		"eval_duration":     resp.Duration.Nanoseconds(),
	})
}

func (h *httpHandler) handleOllamaGenerate(w http.ResponseWriter, r *http.Request) {
	if h.svc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "LLM gateway service is not initialized"})
		return
	}
	var wireReq ollamaGenerateRequest
	if err := decodeRequiredJSON(r, &wireReq); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid Ollama generate JSON: " + err.Error()})
		return
	}
	if strings.TrimSpace(wireReq.Prompt) == "" {
		sp, rErr := h.resolveModel(wireReq.Model)
		if rErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": rErr.Error()})
			return
		}
		targetModel := sp.ID
		tagOut := sp.OllamaTag
		if isZeroKeepAlive(wireReq.KeepAlive) {
			if _, err := h.svc.Stop(r.Context()); err != nil {
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"model":       tagOut,
				"created_at":  time.Now().UTC().Format(time.RFC3339Nano),
				"response":    "",
				"done":        true,
				"done_reason": "unload",
			})
			return
		}
		if _, err := h.svc.Start(r.Context(), models.LLMStartRequest{ModelID: targetModel}); err != nil {
			writeJSON(w, classifyGatewayHTTPStatus(err), map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model":       tagOut,
			"created_at":  time.Now().UTC().Format(time.RFC3339Nano),
			"response":    "",
			"done":        true,
			"done_reason": "load",
		})
		return
	}

	msgs := make([]ChatMessage, 0, 2)
	if strings.TrimSpace(wireReq.System) != "" {
		msgs = append(msgs, ChatMessage{Role: "system", Content: wireReq.System})
	}
	msgs = append(msgs, ChatMessage{Role: "user", Content: wireReq.Prompt})

	inferReq := ChatInferRequest{
		Model:       wireReq.Model,
		Messages:    msgs,
		Temperature: wireReq.Options.Temperature,
		TopP:        wireReq.Options.TopP,
		MaxTokens:   wireReq.Options.NumPredict,
		Stop:        wireReq.Options.Stop,
	}

	if err := h.svc.ValidateChatModel(inferReq.Model); err != nil {
		writeJSON(w, classifyGatewayHTTPStatus(err), map[string]any{"error": err.Error()})
		return
	}

	stream := wireReq.Stream == nil || *wireReq.Stream
	if !stream {
		resp, err := h.svc.Chat(r.Context(), inferReq, nil)
		if err != nil {
			writeJSON(w, classifyGatewayHTTPStatus(err), map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"model":             resp.OllamaTag,
			"created_at":        time.Now().UTC().Format(time.RFC3339Nano),
			"response":          resp.Content,
			"done":              true,
			"done_reason":       "stop",
			"total_duration":    resp.Duration.Nanoseconds(),
			"prompt_eval_count": resp.PromptTokens,
			"eval_count":        resp.CompletionTokens,
			"eval_duration":     resp.Duration.Nanoseconds(),
		})
		return
	}

	flusher, _ := w.(http.Flusher)
	headerSent := false
	tagOut := "qwen3:8b"

	sendNDJSON := func(obj any) error {
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		b = append(b, '\n')
		if _, err := w.Write(b); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	initNDJSON := func(fallbackTag string) {
		if headerSent {
			return
		}
		headerSent = true
		if sp, ok := h.svc.ActiveModelSpec(); ok && sp.OllamaTag != "" {
			tagOut = sp.OllamaTag
		} else if fallbackTag != "" {
			tagOut = fallbackTag
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
	}

	resp, err := h.svc.Chat(r.Context(), inferReq, func(piece string) error {
		initNDJSON("")
		return sendNDJSON(map[string]any{
			"model":      tagOut,
			"created_at": time.Now().UTC().Format(time.RFC3339Nano),
			"response":   piece,
			"done":       false,
		})
	})
	if err != nil {
		if !headerSent {
			writeJSON(w, classifyGatewayHTTPStatus(err), map[string]any{"error": err.Error()})
		} else {
			_ = sendNDJSON(map[string]any{"error": err.Error()})
		}
		return
	}

	initNDJSON(resp.OllamaTag)
	_ = sendNDJSON(map[string]any{
		"model":             resp.OllamaTag,
		"created_at":        time.Now().UTC().Format(time.RFC3339Nano),
		"response":          "",
		"done":              true,
		"done_reason":       "stop",
		"total_duration":    resp.Duration.Nanoseconds(),
		"prompt_eval_count": resp.PromptTokens,
		"eval_count":        resp.CompletionTokens,
		"eval_duration":     resp.Duration.Nanoseconds(),
	})
}

// ============================================================================
// Wire Helpers & Synchronized SSE Keepalive Stream Writer
// ============================================================================

type sseStreamWriter struct {
	mu       sync.Mutex
	w        http.ResponseWriter
	flusher  http.Flusher
	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func startSSEStream(ctx context.Context, w http.ResponseWriter, keepaliveInterval time.Duration) *sseStreamWriter {
	flusher, _ := w.(http.Flusher)
	setSSEHeaders(w)
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": keepalive\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	sw := &sseStreamWriter{
		w:       w,
		flusher: flusher,
		stopCh:  make(chan struct{}),
	}
	if keepaliveInterval > 0 {
		sw.wg.Add(1)
		go func() {
			defer sw.wg.Done()
			ticker := time.NewTicker(keepaliveInterval)
			defer ticker.Stop()
			for {
				select {
				case <-sw.stopCh:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := sw.WriteRaw(": keepalive\n\n"); err != nil {
						return
					}
				}
			}
		}()
	}
	return sw
}

func (sw *sseStreamWriter) StopKeepalive() {
	sw.stopOnce.Do(func() {
		close(sw.stopCh)
	})
	sw.wg.Wait()
}

func (sw *sseStreamWriter) WriteRaw(s string) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if _, err := io.WriteString(sw.w, s); err != nil {
		return err
	}
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
	return nil
}

func (sw *sseStreamWriter) WriteDataJSON(payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if _, err := fmt.Fprintf(sw.w, "data: %s\n\n", b); err != nil {
		return err
	}
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
	return nil
}

func (sw *sseStreamWriter) WriteEventJSON(eventName string, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if _, err := fmt.Fprintf(sw.w, "event: %s\ndata: %s\n\n", eventName, b); err != nil {
		return err
	}
	if sw.flusher != nil {
		sw.flusher.Flush()
	}
	return nil
}

func openAIErrorType(err error) string {
	switch {
	case errors.Is(err, ErrModelMismatch):
		return "model_mismatch"
	case errors.Is(err, ErrNoAccountsAvailable):
		return "insufficient_quota"
	case errors.Is(err, ErrGatewayStopped):
		return "service_unavailable"
	default:
		var clientErr *gatewayClientError
		if errors.As(err, &clientErr) {
			return "invalid_request_error"
		}
		if classifyGatewayHTTPStatus(err) == http.StatusBadRequest {
			return "invalid_request_error"
		}
		return "server_error"
	}
}

func anthropicErrorType(err error) string {
	switch {
	case errors.Is(err, ErrModelMismatch):
		return "invalid_request_error"
	case errors.Is(err, ErrNoAccountsAvailable), errors.Is(err, ErrGatewayStopped):
		return "overloaded_error"
	default:
		var clientErr *gatewayClientError
		if errors.As(err, &clientErr) {
			return "invalid_request_error"
		}
		if classifyGatewayHTTPStatus(err) == http.StatusBadRequest {
			return "invalid_request_error"
		}
		return "api_error"
	}
}

func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeControlError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error":   msg,
		"message": msg,
		"status":  status,
	})
}

func classifyGatewayHTTPStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if errors.Is(err, ErrModelMismatch) {
		return http.StatusConflict
	}
	if errors.Is(err, ErrNoAccountsAvailable) || errors.Is(err, ErrGatewayStopped) {
		return http.StatusServiceUnavailable
	}
	var clientErr *gatewayClientError
	if errors.As(err, &clientErr) && clientErr.statusCode >= 400 && clientErr.statusCode < 500 {
		return clientErr.statusCode
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "invalid"),
		strings.Contains(lower, "unsupported accelerator"),
		strings.Contains(lower, "not supported for interactive colab sessions"),
		strings.Contains(lower, "must not be empty"):
		return http.StatusBadRequest
	case strings.Contains(lower, "no eligible pooled account"),
		strings.Contains(lower, "no gpu accounts"):
		return http.StatusServiceUnavailable
	default:
		return http.StatusBadGateway
	}
}

func decodeOptionalJSON(r *http.Request, dst any) error {
	if r == nil || r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayRequestBodyBytes+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > maxGatewayRequestBodyBytes {
		return errors.New("request body exceeds 16 MiB limit")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func decodeRequiredJSON(r *http.Request, dst any) error {
	_, err := decodeRequiredJSONWithRaw(r, dst)
	return err
}

func decodeRequiredJSONWithRaw(r *http.Request, dst any) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, errors.New("empty request body")
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxGatewayRequestBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxGatewayRequestBodyBytes {
		return nil, errors.New("request body exceeds 16 MiB limit")
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil, errors.New("empty request body")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return nil, err
	}
	return raw, nil
}

// extractFlexibleContentText extracts plain text whether content is a JSON string
// or an OpenAI/Anthropic array of content blocks (`[{"type":"text","text":"..."}]`).
func extractFlexibleContentText(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if strings.HasPrefix(trimmed, "\"") {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	if strings.HasPrefix(trimmed, "[") {
		var blocks []struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &blocks); err == nil {
			var parts []string
			for _, b := range blocks {
				if b.Text != "" {
					parts = append(parts, b.Text)
				} else if b.Content != "" {
					parts = append(parts, b.Content)
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return trimmed
}

func parseStopSequences(raw json.RawMessage) []string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var single string
		if err := json.Unmarshal(raw, &single); err == nil && single != "" {
			return []string{single}
		}
	}
	if strings.HasPrefix(trimmed, "[") {
		var list []string
		if err := json.Unmarshal(raw, &list); err == nil {
			return list
		}
	}
	return nil
}
