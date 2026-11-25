package llmgw

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/commtunnel"
	"github.com/colabhive/colabhive/internal/models"
)

// errNativePassThroughUnsupported signals that the remote runtime does not expose
// Ollama's native `/v1/*` HTTP endpoint (e.g., DemoMode, mock test runtime, or an
// older Ollama build < 0.14) and the caller should seamlessly fall back to Service.Chat().
var errNativePassThroughUnsupported = errors.New("native Ollama /v1 pass-through unavailable")

// prepareNativeOllamaPayload preserves every client field (tools, tool_choice, response_format,
// vision images, instructions, previous_response_id, metadata, etc.) while rewriting `model`
// to Ollama's loaded GGUF tag and mapping custom `think`/`enable_thinking` flags to each
// endpoint's native Ollama 0.40+ convention.
func prepareNativeOllamaPayload(path string, rawBody []byte, ollamaTag string) ([]byte, bool, error) {
	var bodyMap map[string]any
	if err := json.Unmarshal(rawBody, &bodyMap); err != nil || bodyMap == nil {
		return nil, false, fmt.Errorf("invalid JSON request body: %w", err)
	}

	bodyMap["model"] = ollamaTag
	wantStream, _ := bodyMap["stream"].(bool)

	var thinkFlag *bool
	if v, ok := bodyMap["think"].(bool); ok {
		b := v
		thinkFlag = &b
	} else if v, ok := bodyMap["enable_thinking"].(bool); ok {
		b := v
		thinkFlag = &b
	}
	delete(bodyMap, "think")
	delete(bodyMap, "enable_thinking")

	if thinkFlag != nil {
		switch path {
		case "/v1/chat/completions":
			if _, exists := bodyMap["reasoning_effort"]; !exists {
				if !*thinkFlag {
					bodyMap["reasoning_effort"] = "none"
				}
			}
		case "/v1/responses":
			if _, exists := bodyMap["reasoning"]; !exists {
				if !*thinkFlag {
					bodyMap["reasoning"] = map[string]any{"effort": "none"}
				}
			}
		case "/v1/messages":
			if _, exists := bodyMap["thinking"]; !exists {
				if !*thinkFlag {
					bodyMap["thinking"] = map[string]any{"type": "disabled"}
				} else {
					bodyMap["thinking"] = map[string]any{"type": "enabled"}
				}
			}
		}
	}

	out, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, false, err
	}
	return out, wantStream, nil
}

func buildOllamaHTTPProxyPython(path string, payloadB64 string, extraHeaders map[string]string, numCtx int, numParallel int) string {
	if numCtx <= 0 {
		numCtx = 8192
	}
	if numParallel <= 0 {
		numParallel = 1
	}
	hdrJSON, _ := json.Marshal(extraHeaders)
	return fmt.Sprintf(`import base64, json, os, shutil, subprocess, sys, time, urllib.error, urllib.request

__ch_path = %q
__ch_req_bytes = base64.b64decode(%q)
__ch_extra_hdrs = json.loads(%q)
__ch_num_ctx = %d
__ch_num_parallel = %d

def __ch_open_http():
    __hdrs = {"Content-Type": "application/json"}
    for __k, __v in __ch_extra_hdrs.items():
        if __v:
            __hdrs[__k] = __v
    __req = urllib.request.Request(
        "http://127.0.0.1:11434" + __ch_path,
        data=__ch_req_bytes,
        headers=__hdrs,
        method="POST",
    )
    return urllib.request.urlopen(__req, timeout=300)

def __ch_emit_meta(status, ct):
    sys.stdout.write("COLABHIVE_HTTP_META:" + json.dumps({"status": int(status), "content_type": str(ct or "")}) + "\n")
    sys.stdout.flush()

def __ch_emit_chunk(raw_bytes):
    if not raw_bytes:
        return
    sys.stdout.write("COLABHIVE_HTTP_CHUNK:" + base64.b64encode(raw_bytes).decode("ascii") + "\n")
    sys.stdout.flush()

__ch_resp = None
try:
    __ch_resp = __ch_open_http()
except urllib.error.HTTPError as __he:
    __ct = ""
    try:
        __ct = __he.headers.get("Content-Type", "application/json") if __he.headers else "application/json"
    except Exception:
        __ct = "application/json"
    __ch_emit_meta(__he.code, __ct)
    try:
        __ch_emit_chunk(__he.read(65536))
    except Exception:
        pass
    sys.stdout.write("COLABHIVE_HTTP_END:{\"ok\":true}\n")
    sys.stdout.flush()
    raise SystemExit(0)
except urllib.error.URLError:
    __ch_bin = shutil.which("ollama") or ("/usr/local/bin/ollama" if os.path.exists("/usr/local/bin/ollama") else None)
    if __ch_bin:
        __ch_env = dict(os.environ)
        __ch_env["OLLAMA_HOST"] = "127.0.0.1:11434"
        __ch_env["OLLAMA_KEEP_ALIVE"] = "30m"
        __ch_env["OLLAMA_CONTEXT_LENGTH"] = str(__ch_num_ctx)
        __ch_env["OLLAMA_FLASH_ATTENTION"] = "1"
        __ch_env["OLLAMA_KV_CACHE_TYPE"] = "q8_0"
        __ch_env["OLLAMA_NUM_PARALLEL"] = str(__ch_num_parallel)
        __ch_env["OLLAMA_MAX_LOADED_MODELS"] = "1"
        with open("/tmp/colabhive_ollama.log", "ab") as __ch_log:
            subprocess.Popen([__ch_bin, "serve"], env=__ch_env, stdin=subprocess.DEVNULL, stdout=__ch_log, stderr=__ch_log, start_new_session=True)
        for _ in range(25):
            time.sleep(0.4)
            try:
                with urllib.request.urlopen("http://127.0.0.1:11434/api/tags", timeout=2) as __r:
                    if __r.status == 200:
                        break
            except Exception:
                pass
    try:
        __ch_resp = __ch_open_http()
    except urllib.error.HTTPError as __he2:
        __ct2 = ""
        try:
            __ct2 = __he2.headers.get("Content-Type", "application/json") if __he2.headers else "application/json"
        except Exception:
            __ct2 = "application/json"
        __ch_emit_meta(__he2.code, __ct2)
        try:
            __ch_emit_chunk(__he2.read(65536))
        except Exception:
            pass
        sys.stdout.write("COLABHIVE_HTTP_END:{\"ok\":true}\n")
        sys.stdout.flush()
        raise SystemExit(0)

__ct_ok = ""
try:
    __ct_ok = __ch_resp.headers.get("Content-Type", "application/json") if __ch_resp.headers else "application/json"
except Exception:
    __ct_ok = "application/json"
__ch_emit_meta(getattr(__ch_resp, "status", 200), __ct_ok)

with __ch_resp as __stream:
    if "event-stream" in __ct_ok.lower() or "ndjson" in __ct_ok.lower():
        for __line in __stream:
            __ch_emit_chunk(__line)
    else:
        while True:
            __buf = __stream.read(16384)
            if not __buf:
                break
            __ch_emit_chunk(__buf)

sys.stdout.write("COLABHIVE_HTTP_END:{\"ok\":true}\n")
sys.stdout.flush()
`, path, payloadB64, string(hdrJSON), numCtx, numParallel)
}

// normalizeChatCompletionsJSON rewrites `model` to clientModelID and mirrors Ollama's
// `reasoning` field onto `reasoning_content` (keeping both) so every OpenAI-compatible
// client (Cherry Studio, Open WebUI, Cursor, SillyTavern, Cline) separates thinking & answer.
func normalizeChatCompletionsJSON(raw []byte, clientModelID string) ([]byte, int) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw, 0
	}
	mutated := false
	if clientModelID != "" {
		if _, ok := obj["model"]; ok {
			obj["model"] = clientModelID
			mutated = true
		}
	}
	if choices, ok := obj["choices"].([]any); ok {
		for _, item := range choices {
			ch, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, field := range []string{"delta", "message"} {
				sub, ok := ch[field].(map[string]any)
				if !ok {
					continue
				}
				if rStr, ok := sub["reasoning"].(string); ok && rStr != "" {
					if cur, has := sub["reasoning_content"].(string); !has || cur == "" {
						sub["reasoning_content"] = rStr
						mutated = true
					}
				}
			}
		}
	}
	compToks := extractUsageCompletionTokens(obj)
	if !mutated {
		return raw, compToks
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw, compToks
	}
	return out, compToks
}

func normalizeAnthropicJSON(raw []byte, clientModelID string) ([]byte, int) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw, 0
	}
	mutated := false
	if clientModelID != "" {
		if _, ok := obj["model"]; ok {
			obj["model"] = clientModelID
			mutated = true
		}
		if msg, ok := obj["message"].(map[string]any); ok {
			if _, ok2 := msg["model"]; ok2 {
				msg["model"] = clientModelID
				mutated = true
			}
		}
	}
	compToks := extractUsageCompletionTokens(obj)
	if !mutated {
		return raw, compToks
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw, compToks
	}
	return out, compToks
}

func normalizeResponsesJSON(raw []byte, clientModelID string) ([]byte, int) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw, 0
	}
	mutated := false
	if clientModelID != "" {
		if _, ok := obj["model"]; ok {
			obj["model"] = clientModelID
			mutated = true
		}
		if respObj, ok := obj["response"].(map[string]any); ok {
			if _, ok2 := respObj["model"]; ok2 {
				respObj["model"] = clientModelID
				mutated = true
			}
		}
	}
	compToks := extractUsageCompletionTokens(obj)
	if !mutated {
		return raw, compToks
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return raw, compToks
	}
	return out, compToks
}

func extractUsageCompletionTokens(obj map[string]any) int {
	readFrom := func(u map[string]any) int {
		if u == nil {
			return 0
		}
		for _, k := range []string{"completion_tokens", "output_tokens"} {
			if f, ok := u[k].(float64); ok && f > 0 {
				return int(f)
			}
		}
		return 0
	}
	if u, ok := obj["usage"].(map[string]any); ok {
		if n := readFrom(u); n > 0 {
			return n
		}
	}
	if respObj, ok := obj["response"].(map[string]any); ok {
		if u, ok2 := respObj["usage"].(map[string]any); ok2 {
			if n := readFrom(u); n > 0 {
				return n
			}
		}
	}
	return 0
}

// nativeSSETransformer applies minimal protocol compatibility shims while forwarding
// Ollama's native SSE frames incrementally to w:
//   - /v1/chat/completions: mirrors `delta.reasoning` -> `delta.reasoning_content` & rewrites `model`
//   - /v1/responses: auto-injects missing `response.reasoning_summary_part.added` / `.done`
//     events required by Vercel AI SDK / Cherry Studio, & rewrites `model`
//   - /v1/messages: rewrites `model` while forwarding native Claude thinking/text/tool_use blocks
type nativeSSETransformer struct {
	path          string
	clientModelID string
	writeRaw      func([]byte) error

	lineBuf     bytes.Buffer
	eventName   string
	dataLines   []string
	hasFields   bool
	outputToks  int
	chunkApprox int

	// Responses API reasoning part lifecycle tracking (fixes Ollama 0.40 omitting summary_part.added/done)
	respSeq              int
	respReasoningAdded   map[string]bool
	respReasoningDone    map[string]bool
	respReasoningOutIdx  map[string]int
	respReasoningTextBuf map[string]*strings.Builder
}

func newNativeSSETransformer(path, clientModelID string, writeRaw func([]byte) error) *nativeSSETransformer {
	return &nativeSSETransformer{
		path:                 path,
		clientModelID:        clientModelID,
		writeRaw:             writeRaw,
		respReasoningAdded:   make(map[string]bool),
		respReasoningDone:    make(map[string]bool),
		respReasoningOutIdx:  make(map[string]int),
		respReasoningTextBuf: make(map[string]*strings.Builder),
	}
}

func (t *nativeSSETransformer) Write(chunk []byte) error {
	for _, b := range chunk {
		if b == '\n' {
			line := t.lineBuf.String()
			t.lineBuf.Reset()
			line = strings.TrimSuffix(line, "\r")
			if err := t.handleLine(line); err != nil {
				return err
			}
		} else {
			t.lineBuf.WriteByte(b)
		}
	}
	return nil
}

func (t *nativeSSETransformer) Flush() error {
	if t.lineBuf.Len() > 0 {
		line := strings.TrimSuffix(t.lineBuf.String(), "\r")
		t.lineBuf.Reset()
		if err := t.handleLine(line); err != nil {
			return err
		}
	}
	if t.hasFields {
		return t.dispatchEvent()
	}
	return nil
}

func (t *nativeSSETransformer) handleLine(line string) error {
	if line == "" {
		if t.hasFields {
			return t.dispatchEvent()
		}
		return nil
	}
	if strings.HasPrefix(line, ":") {
		return t.writeRaw([]byte(line + "\n\n"))
	}
	t.hasFields = true
	if strings.HasPrefix(line, "event:") {
		t.eventName = strings.TrimSpace(line[len("event:"):])
		return nil
	}
	if strings.HasPrefix(line, "data:") {
		val := line[len("data:"):]
		if strings.HasPrefix(val, " ") {
			val = val[1:]
		}
		t.dataLines = append(t.dataLines, val)
		return nil
	}
	return nil
}

func (t *nativeSSETransformer) emitSSE(eventName string, data []byte) error {
	var buf bytes.Buffer
	if eventName != "" {
		buf.WriteString("event: ")
		buf.WriteString(eventName)
		buf.WriteByte('\n')
	}
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return t.writeRaw(buf.Bytes())
}

func (t *nativeSSETransformer) nextRespSeq() int {
	cur := t.respSeq
	t.respSeq++
	return cur
}

func (t *nativeSSETransformer) dispatchEvent() error {
	evtName := t.eventName
	dataJoined := strings.Join(t.dataLines, "\n")
	t.eventName = ""
	t.dataLines = nil
	t.hasFields = false

	trimmed := strings.TrimSpace(dataJoined)
	if trimmed == "" {
		return nil
	}
	if trimmed == "[DONE]" {
		return t.emitSSE(evtName, []byte("[DONE]"))
	}

	switch t.path {
	case "/v1/chat/completions":
		t.chunkApprox++
		norm, toks := normalizeChatCompletionsJSON([]byte(trimmed), t.clientModelID)
		if toks > t.outputToks {
			t.outputToks = toks
		}
		return t.emitSSE(evtName, norm)

	case "/v1/messages":
		t.chunkApprox++
		norm, toks := normalizeAnthropicJSON([]byte(trimmed), t.clientModelID)
		if toks > t.outputToks {
			t.outputToks = toks
		}
		return t.emitSSE(evtName, norm)

	case "/v1/responses":
		return t.dispatchResponsesEvent(evtName, []byte(trimmed))

	default:
		return t.emitSSE(evtName, []byte(trimmed))
	}
}

func (t *nativeSSETransformer) dispatchResponsesEvent(evtName string, raw []byte) error {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return t.emitSSE(evtName, raw)
	}
	evtType := evtName
	if s, ok := obj["type"].(string); ok && s != "" {
		evtType = s
	}
	if t.clientModelID != "" {
		if respObj, ok := obj["response"].(map[string]any); ok {
			if _, has := respObj["model"]; has {
				respObj["model"] = t.clientModelID
			}
		}
	}
	if toks := extractUsageCompletionTokens(obj); toks > t.outputToks {
		t.outputToks = toks
	}

	switch evtType {
	case "response.output_item.added":
		item, _ := obj["item"].(map[string]any)
		outIdx := 0
		if f, ok := obj["output_index"].(float64); ok {
			outIdx = int(f)
		}
		obj["sequence_number"] = t.nextRespSeq()
		b, _ := json.Marshal(obj)
		if err := t.emitSSE(evtType, b); err != nil {
			return err
		}
		if item != nil && item["type"] == "reasoning" {
			if rsID, _ := item["id"].(string); rsID != "" && !t.respReasoningAdded[rsID] {
				t.respReasoningAdded[rsID] = true
				t.respReasoningOutIdx[rsID] = outIdx
				t.respReasoningTextBuf[rsID] = &strings.Builder{}
				partAdded, _ := json.Marshal(map[string]any{
					"type":            "response.reasoning_summary_part.added",
					"sequence_number": t.nextRespSeq(),
					"item_id":         rsID,
					"output_index":    outIdx,
					"summary_index":   0,
					"part": map[string]any{
						"type": "summary_text",
						"text": "",
					},
				})
				return t.emitSSE("response.reasoning_summary_part.added", partAdded)
			}
		}
		return nil

	case "response.reasoning_summary_part.added":
		rsID, _ := obj["item_id"].(string)
		if rsID != "" && t.respReasoningAdded[rsID] {
			// Already emitted right after output_item.added — avoid duplicate event.
			return nil
		}
		if rsID != "" {
			t.respReasoningAdded[rsID] = true
			if f, ok := obj["output_index"].(float64); ok {
				t.respReasoningOutIdx[rsID] = int(f)
			}
			if t.respReasoningTextBuf[rsID] == nil {
				t.respReasoningTextBuf[rsID] = &strings.Builder{}
			}
		}
		obj["sequence_number"] = t.nextRespSeq()
		b, _ := json.Marshal(obj)
		return t.emitSSE(evtType, b)

	case "response.reasoning_summary_text.delta":
		t.chunkApprox++
		rsID, _ := obj["item_id"].(string)
		outIdx := 0
		if f, ok := obj["output_index"].(float64); ok {
			outIdx = int(f)
		}
		if rsID != "" && !t.respReasoningAdded[rsID] {
			t.respReasoningAdded[rsID] = true
			t.respReasoningOutIdx[rsID] = outIdx
			t.respReasoningTextBuf[rsID] = &strings.Builder{}
			partAdded, _ := json.Marshal(map[string]any{
				"type":            "response.reasoning_summary_part.added",
				"sequence_number": t.nextRespSeq(),
				"item_id":         rsID,
				"output_index":    outIdx,
				"summary_index":   0,
				"part": map[string]any{
					"type": "summary_text",
					"text": "",
				},
			})
			if err := t.emitSSE("response.reasoning_summary_part.added", partAdded); err != nil {
				return err
			}
		}
		if rsID != "" {
			if deltaStr, ok := obj["delta"].(string); ok {
				if t.respReasoningTextBuf[rsID] == nil {
					t.respReasoningTextBuf[rsID] = &strings.Builder{}
				}
				t.respReasoningTextBuf[rsID].WriteString(deltaStr)
			}
		}
		obj["sequence_number"] = t.nextRespSeq()
		b, _ := json.Marshal(obj)
		return t.emitSSE(evtType, b)

	case "response.reasoning_summary_part.done":
		if rsID, _ := obj["item_id"].(string); rsID != "" {
			t.respReasoningDone[rsID] = true
		}
		obj["sequence_number"] = t.nextRespSeq()
		b, _ := json.Marshal(obj)
		return t.emitSSE(evtType, b)

	case "response.output_item.done":
		item, _ := obj["item"].(map[string]any)
		if item != nil && item["type"] == "reasoning" {
			if rsID, _ := item["id"].(string); rsID != "" && t.respReasoningAdded[rsID] && !t.respReasoningDone[rsID] {
				t.respReasoningDone[rsID] = true
				outIdx := t.respReasoningOutIdx[rsID]
				fullThink := ""
				if sb := t.respReasoningTextBuf[rsID]; sb != nil {
					fullThink = sb.String()
				}
				partDone, _ := json.Marshal(map[string]any{
					"type":            "response.reasoning_summary_part.done",
					"sequence_number": t.nextRespSeq(),
					"item_id":         rsID,
					"output_index":    outIdx,
					"summary_index":   0,
					"part": map[string]any{
						"type": "summary_text",
						"text": fullThink,
					},
				})
				if err := t.emitSSE("response.reasoning_summary_part.done", partDone); err != nil {
					return err
				}
			}
		}
		obj["sequence_number"] = t.nextRespSeq()
		b, _ := json.Marshal(obj)
		return t.emitSSE(evtType, b)

	case "response.output_text.delta":
		t.chunkApprox++
		obj["sequence_number"] = t.nextRespSeq()
		b, _ := json.Marshal(obj)
		return t.emitSSE(evtType, b)

	default:
		obj["sequence_number"] = t.nextRespSeq()
		b, _ := json.Marshal(obj)
		return t.emitSSE(evtType, b)
	}
}

// ProxyNativeOllamaV1 transparently relays a standard OpenAI Chat (`/v1/chat/completions`),
// OpenAI Responses (`/v1/responses`), or Anthropic Messages (`/v1/messages`) request straight
// to the remote Colab GPU's native Ollama HTTP server, preserving full native reasoning,
// tool calling, vision, structured output, and prompt caching while applying ColabHive's
// FIFO inference queue, mid-stream kernel interrupt, idle watchdog, and compatibility shims.
//
// Returns (handled=false, err=errNativePassThroughUnsupported) BEFORE writing any bytes to w
// whenever DemoMode is active, the remote mock doesn't support `/v1/*`, or upstream returns 404.
func (s *Service) ProxyNativeOllamaV1(
	ctx context.Context,
	path string,
	requestedModel string,
	rawBody []byte,
	reqHeaders http.Header,
	w http.ResponseWriter,
) (bool, error) {
	if s == nil || s.cfg.DemoMode {
		return false, errNativePassThroughUnsupported
	}

	s.inFlight.Add(1)
	defer func() {
		s.inFlight.Add(-1)
		now := s.nowUTC()
		s.stateMu.Lock()
		if s.status.Running {
			s.status.LastActiveAt = &now
		}
		s.stateMu.Unlock()
	}()

	snap, err := s.ensureReadyForChat(ctx, requestedModel)
	if err != nil {
		return false, err
	}
	if !snap.liveOllama {
		return false, errNativePassThroughUnsupported
	}

	ollamaTag := snap.spec.OllamaTag
	if ollamaTag == "" {
		ollamaTag = "qwen3:8b"
	}
	clientModelID := snap.spec.ID
	if clientModelID == "" {
		clientModelID = DefaultModelID
	}

	rewrittenBody, _, err := prepareNativeOllamaPayload(path, rawBody, ollamaTag)
	if err != nil {
		return false, &gatewayClientError{statusCode: http.StatusBadRequest, msg: err.Error()}
	}
	payloadB64 := base64.StdEncoding.EncodeToString(rewrittenBody)

	extraHeaders := map[string]string{}
	if reqHeaders != nil {
		for _, hk := range []string{"anthropic-version", "anthropic-beta", "x-api-key"} {
			if v := strings.TrimSpace(reqHeaders.Get(hk)); v != "" {
				extraHeaders[hk] = v
			}
		}
	}
	if path == "/v1/messages" {
		if extraHeaders["anthropic-version"] == "" {
			extraHeaders["anthropic-version"] = "2023-06-01"
		}
		if extraHeaders["x-api-key"] == "" {
			extraHeaders["x-api-key"] = "ollama"
		}
	}

	now := s.nowUTC()
	s.stateMu.Lock()
	if s.lifecycleGen == snap.lifecycleGen && s.status.Running {
		s.status.LastActiveAt = &now
	}
	s.stateMu.Unlock()

	start := time.Now()

	// Fast path: full-duplex multiplexed Jupyter Comm TCP tunnel directly to Ollama `127.0.0.1:11434`.
	// Bypasses `inferSem` completely so multiple Cherry Studio / Cursor / API sessions stream in parallel
	// and abort independently on cancel without interrupting sibling turns.
	if handled, tryFallback, tErr := s.proxyNativeOllamaV1ViaTunnel(
		ctx, snap, path, clientModelID, rewrittenBody, extraHeaders, start, w,
	); handled || !tryFallback {
		return handled, tErr
	}

	// Fallback path: acquire exclusive 1-slot FIFO queue for single-kernel Jupyter execute_request bridge.
	if s.inferSem != nil {
		select {
		case s.inferSem <- struct{}{}:
			defer func() { <-s.inferSem }()
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}

	numParallel, numCtx := safeOllamaConcurrency(snap.status.AssignedGPU, snap.spec)
	proxyCode := buildOllamaHTTPProxyPython(path, payloadB64, extraHeaders, numCtx, numParallel)

	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	flusher, _ := w.(http.Flusher)

	var (
		mu              sync.Mutex
		pendingLine     strings.Builder
		sawMeta         bool
		metaStatus      int
		metaContentType string
		unsupported404  bool
		headersWritten  bool
		isStreamResp    bool
		writeErr        error
		nonStreamBuf    bytes.Buffer
		sseTransform    *nativeSSETransformer
		keepaliveStop   chan struct{}
		keepaliveOnce   sync.Once
	)

	stopKeepalive := func() {
		keepaliveOnce.Do(func() {
			if keepaliveStop != nil {
				close(keepaliveStop)
			}
		})
	}
	defer stopKeepalive()

	writeFlushed := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		if _, wErr := w.Write(b); wErr != nil {
			return wErr
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}

	handleLine := func(rawLine string) {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			return
		}
		if strings.HasPrefix(line, "COLABHIVE_HTTP_META:") {
			var meta struct {
				Status      int    `json:"status"`
				ContentType string `json:"content_type"`
			}
			if jErr := json.Unmarshal([]byte(strings.TrimPrefix(line, "COLABHIVE_HTTP_META:")), &meta); jErr != nil {
				return
			}
			mu.Lock()
			sawMeta = true
			metaStatus = meta.Status
			metaContentType = meta.ContentType
			if meta.Status == http.StatusNotFound {
				unsupported404 = true
				mu.Unlock()
				execCancel()
				return
			}
			ctLower := strings.ToLower(meta.ContentType)
			isStreamResp = meta.Status >= 200 && meta.Status < 300 && (strings.Contains(ctLower, "event-stream") || strings.Contains(ctLower, "ndjson"))
			if isStreamResp {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.Header().Set("Cache-Control", "no-cache, no-transform")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Set("X-Accel-Buffering", "no")
				w.WriteHeader(meta.Status)
				headersWritten = true
				if flusher != nil {
					flusher.Flush()
				}
				sseTransform = newNativeSSETransformer(path, clientModelID, func(out []byte) error {
					return writeFlushed(out)
				})
				keepaliveStop = make(chan struct{})
				if interval := s.sseKeepaliveInterval(); interval > 0 {
					go func(stopCh <-chan struct{}) {
						ticker := time.NewTicker(interval)
						defer ticker.Stop()
						for {
							select {
							case <-stopCh:
								return
							case <-execCtx.Done():
								return
							case <-ticker.C:
								mu.Lock()
								if headersWritten && writeErr == nil {
									_ = writeFlushed([]byte(": keepalive\n\n"))
								}
								mu.Unlock()
							}
						}
					}(keepaliveStop)
				}
			}
			mu.Unlock()
			return
		}

		if strings.HasPrefix(line, "COLABHIVE_HTTP_CHUNK:") {
			b64 := strings.TrimPrefix(line, "COLABHIVE_HTTP_CHUNK:")
			decoded, dErr := base64.StdEncoding.DecodeString(b64)
			if dErr != nil || len(decoded) == 0 {
				return
			}
			mu.Lock()
			if unsupported404 || writeErr != nil {
				mu.Unlock()
				return
			}
			if isStreamResp && sseTransform != nil {
				if err := sseTransform.Write(decoded); err != nil {
					writeErr = err
					mu.Unlock()
					execCancel()
					return
				}
			} else {
				nonStreamBuf.Write(decoded)
			}
			mu.Unlock()
			return
		}
	}

	feedStdout := func(chunk string) {
		for {
			nl := strings.IndexByte(chunk, '\n')
			if nl < 0 {
				mu.Lock()
				pendingLine.WriteString(chunk)
				mu.Unlock()
				return
			}
			mu.Lock()
			pendingLine.WriteString(chunk[:nl])
			line := pendingLine.String()
			pendingLine.Reset()
			mu.Unlock()
			handleLine(line)
			chunk = chunk[nl+1:]
		}
	}

	execRes, execErr := s.rt.ExecuteCode(execCtx, models.KernelExecRequest{
		SessionID:               snap.sessionID,
		Code:                    proxyCode,
		Silent:                  false,
		StoreHistory:            false,
		IdleTimeoutSec:          120,
		CallerName:              "llmgw-proxy",
		SuppressOutputBroadcast: true,
		OnStdout:                feedStdout,
	})

	// Flush any trailing stdout line (and handle runtimes that return stdout in execRes without OnStdout).
	mu.Lock()
	alreadySawMeta := sawMeta
	rem := pendingLine.String()
	pendingLine.Reset()
	mu.Unlock()
	if strings.TrimSpace(rem) != "" {
		handleLine(rem)
	}
	if !alreadySawMeta && execRes != nil && strings.Contains(execRes.Stdout, "COLABHIVE_HTTP_META:") {
		feedStdout(execRes.Stdout)
		mu.Lock()
		rem2 := pendingLine.String()
		pendingLine.Reset()
		mu.Unlock()
		if strings.TrimSpace(rem2) != "" {
			handleLine(rem2)
		}
	}
	stopKeepalive()

	mu.Lock()
	gotMeta := sawMeta
	got404 := unsupported404
	wroteHeaders := headersWritten
	wasStream := isStreamResp
	wErr := writeErr
	stCode := metaStatus
	stCT := metaContentType
	tr := sseTransform
	rawRespBytes := nonStreamBuf.Bytes()
	mu.Unlock()

	if ctx.Err() != nil || wErr != nil {
		s.interruptSessionSync(snap.sessionID, 3*time.Second)
		return wroteHeaders, ctx.Err()
	}

	// If remote mock/older Ollama did not understand native `/v1/*` and nothing was written to w,
	// seamlessly fall back to Service.Chat() so zero requests break!
	if !wroteHeaders && (!gotMeta || got404) {
		return false, errNativePassThroughUnsupported
	}

	if execErr != nil && !wroteHeaders {
		return false, sanitizeGatewayError(execErr)
	}

	compTokens := 0
	if wasStream && tr != nil {
		_ = tr.Flush()
		compTokens = tr.outputToks
		if compTokens <= 0 && tr.chunkApprox > 0 {
			compTokens = tr.chunkApprox
		}
	} else if !wroteHeaders {
		if stCode <= 0 {
			stCode = http.StatusOK
		}
		if stCT == "" {
			stCT = "application/json; charset=utf-8"
		}
		outBytes := rawRespBytes
		if stCode >= 200 && stCode < 300 && strings.Contains(strings.ToLower(stCT), "json") {
			switch path {
			case "/v1/chat/completions":
				outBytes, compTokens = normalizeChatCompletionsJSON(rawRespBytes, clientModelID)
			case "/v1/responses":
				outBytes, compTokens = normalizeResponsesJSON(rawRespBytes, clientModelID)
			case "/v1/messages":
				outBytes, compTokens = normalizeAnthropicJSON(rawRespBytes, clientModelID)
			}
		}
		w.Header().Set("Content-Type", stCT)
		w.WriteHeader(stCode)
		_, _ = w.Write(outBytes)
	}

	// Record live throughput & served request telemetry cleanly.
	if stCode >= 200 && stCode < 300 {
		s.recordServedTelemetry(snap, start, compTokens)
	}

	return true, nil
}

func (s *Service) recordServedTelemetry(snap activeRuntimeSnap, start time.Time, compTokens int) {
	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Millisecond
	}
	finishedAt := s.nowUTC()
	var updatedSnap models.LLMGatewayStatus
	shouldPublish := false
	s.stateMu.Lock()
	if s.lifecycleGen == snap.lifecycleGen && s.status.Running {
		s.status.RequestsServed++
		if compTokens > 0 {
			s.status.TokensGenerated += int64(compTokens)
			s.totalGenTimeNs += elapsed.Nanoseconds()
			if s.totalGenTimeNs > 0 && s.status.TokensGenerated > 0 {
				tps := float64(s.status.TokensGenerated) / (float64(s.totalGenTimeNs) / float64(time.Second))
				s.status.AvgTokensPerSec = math.Round(tps*10) / 10
			}
		}
		s.status.LastActiveAt = &finishedAt
		updatedSnap = cloneGatewayStatus(s.status)
		shouldPublish = true
	}
	s.stateMu.Unlock()
	if shouldPublish {
		s.publishStatus(updatedSnap)
	}
}

func (s *Service) effectiveOllamaPort() int {
	if s != nil && s.ollamaPort > 0 && s.ollamaPort <= 65535 {
		return s.ollamaPort
	}
	return 11434
}

func (s *Service) getOrCreateOllamaTunnel(ctx context.Context, sessionID string) (res *commtunnel.Tunnel) {
	if s == nil || s.rt == nil || strings.TrimSpace(sessionID) == "" || s.closed.Load() {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			res = nil
		}
	}()

	s.tunnelMu.Lock()
	if existing, ok := s.ollamaTunnels[sessionID]; ok && existing != nil && !existing.IsClosed() {
		s.tunnelMu.Unlock()
		return existing
	}
	s.tunnelMu.Unlock()

	sess, err := s.rt.GetSession(ctx, sessionID)
	if err != nil || !commtunnel.SupportsJupyterCommTunnel(sess) {
		return nil
	}

	refreshSession := func(rCtx context.Context) (*models.RuntimeSession, error) {
		if s.rt == nil {
			return nil, errors.New("runtime engine unavailable")
		}
		_, _ = s.rt.ExecuteCode(rCtx, models.KernelExecRequest{
			SessionID:               sessionID,
			Code:                    "1",
			Silent:                  true,
			StoreHistory:            false,
			IdleTimeoutSec:          10,
			CallerName:              "llmgw-tunnel-refresh",
			SuppressOutputBroadcast: true,
		})
		return s.rt.GetSession(rCtx, sessionID)
	}

	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	if s.closed.Load() {
		return nil
	}
	if s.ollamaTunnels == nil {
		s.ollamaTunnels = make(map[string]*commtunnel.Tunnel)
	}
	if existing, ok := s.ollamaTunnels[sessionID]; ok && existing != nil && !existing.IsClosed() {
		return existing
	}

	t := commtunnel.New(commtunnel.Options{
		SessionID:             sessionID,
		RemotePort:            s.effectiveOllamaPort(),
		ResponseHeaderTimeout: 5 * time.Minute,
		GetSession: func(gCtx context.Context) (*models.RuntimeSession, error) {
			if s.rt == nil {
				return nil, errors.New("runtime engine unavailable")
			}
			cur, gErr := s.rt.GetSession(gCtx, sessionID)
			if gErr != nil || cur == nil {
				return cur, gErr
			}
			if cur.ProxyToken == "" || (cur.ProxyTokenExpiresAt != nil && time.Until(*cur.ProxyTokenExpiresAt) <= 5*time.Minute) {
				if fresh, rErr := refreshSession(gCtx); rErr == nil && fresh != nil {
					return fresh, nil
				}
			}
			return cur, nil
		},
		RefreshSession: refreshSession,
		GetEgressProxy: func(pCtx context.Context, cur *models.RuntimeSession) string {
			if s.pool == nil || cur == nil || strings.TrimSpace(cur.AccountID) == "" {
				return ""
			}
			if _, proxyURL, pErr := s.pool.AcquireAccessToken(pCtx, cur.AccountID); pErr == nil {
				return proxyURL
			}
			return ""
		},
	})
	s.ollamaTunnels[sessionID] = t
	return t
}

func (s *Service) closeOllamaTunnel(sessionID string) {
	if s == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	s.tunnelMu.Lock()
	t := s.ollamaTunnels[sessionID]
	delete(s.ollamaTunnels, sessionID)
	s.tunnelMu.Unlock()
	if t != nil {
		_ = t.Close()
	}
}

func (s *Service) closeAllOllamaTunnels() {
	if s == nil {
		return
	}
	s.tunnelMu.Lock()
	toClose := make([]*commtunnel.Tunnel, 0, len(s.ollamaTunnels))
	for k, t := range s.ollamaTunnels {
		delete(s.ollamaTunnels, k)
		if t != nil {
			toClose = append(toClose, t)
		}
	}
	s.tunnelMu.Unlock()
	for _, t := range toClose {
		_ = t.Close()
	}
}

func extractOllamaErrorText(raw []byte, fallbackStatus string) string {
	var parsed map[string]any
	if json.Unmarshal(bytes.TrimSpace(raw), &parsed) == nil && parsed != nil {
		if eStr, ok := parsed["error"].(string); ok && strings.TrimSpace(eStr) != "" {
			return strings.TrimSpace(eStr)
		}
		if eObj, ok := parsed["error"].(map[string]any); ok {
			if msg, ok2 := eObj["message"].(string); ok2 && strings.TrimSpace(msg) != "" {
				return strings.TrimSpace(msg)
			}
		}
	}
	if txt := strings.TrimSpace(string(raw)); txt != "" {
		if len(txt) > 300 {
			txt = txt[:300]
		}
		return txt
	}
	if fallbackStatus != "" {
		return "upstream Ollama returned " + fallbackStatus
	}
	return "upstream Ollama request failed"
}

// proxyNativeOllamaV1ViaTunnel streams `/v1/chat/completions`, `/v1/responses`, or `/v1/messages`
// directly over the multiplexed Jupyter Comm TCP tunnel to `127.0.0.1:11434`.
//
// Return values:
//   - (handled=true, tryFallback=false, err): headers were committed or request completed.
//   - (handled=false, tryFallback=false, err): upstream 404 or client cancellation (do not retry ExecuteCode).
//   - (handled=false, tryFallback=true, nil): tunnel unavailable or pre-header dial error; fall back to ExecuteCode.
func (s *Service) proxyNativeOllamaV1ViaTunnel(
	ctx context.Context,
	snap activeRuntimeSnap,
	path string,
	clientModelID string,
	rewrittenBody []byte,
	extraHeaders map[string]string,
	start time.Time,
	w http.ResponseWriter,
) (handled bool, tryFallback bool, err error) {
	tunnel := s.getOrCreateOllamaTunnel(ctx, snap.sessionID)
	if tunnel == nil {
		return false, true, nil
	}

	targetURL := fmt.Sprintf("http://127.0.0.1:%d%s", s.effectiveOllamaPort(), path)
	httpReq, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(rewrittenBody))
	if reqErr != nil {
		return false, true, nil
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range extraHeaders {
		if v != "" {
			httpReq.Header.Set(k, v)
		}
	}

	hc := &http.Client{Transport: tunnel.Transport()}
	resp, doErr := hc.Do(httpReq)
	if doErr != nil {
		if ctx.Err() != nil {
			return false, false, ctx.Err()
		}
		// Pre-header transport error (e.g., Ollama daemon exited or mock server without Comm WS):
		// fall back seamlessly to the ExecuteCode bridge which also auto-restarts `ollama serve`.
		return false, true, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false, false, errNativePassThroughUnsupported
	}

	stCode := resp.StatusCode
	stCT := strings.TrimSpace(resp.Header.Get("Content-Type"))
	ctLower := strings.ToLower(stCT)
	isStreamResp := stCode >= 200 && stCode < 300 && (strings.Contains(ctLower, "event-stream") || strings.Contains(ctLower, "ndjson"))

	if isStreamResp {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(stCode)
		if flusher != nil {
			flusher.Flush()
		}

		var writeMu sync.Mutex
		var writeErr error
		writeFlushed := func(b []byte) error {
			if len(b) == 0 {
				return nil
			}
			if _, wErr := w.Write(b); wErr != nil {
				return wErr
			}
			if flusher != nil {
				flusher.Flush()
			}
			return nil
		}

		sseTransform := newNativeSSETransformer(path, clientModelID, writeFlushed)

		// Closing resp.Body on ctx.Done() severs ONLY this request's multiplexed TCP commStream,
		// aborting generation inside Ollama in milliseconds without affecting sibling sessions.
		streamDone := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				_ = resp.Body.Close()
			case <-streamDone:
			}
		}()

		keepaliveStop := make(chan struct{})
		if interval := s.sseKeepaliveInterval(); interval > 0 {
			go func() {
				ticker := time.NewTicker(interval)
				defer ticker.Stop()
				for {
					select {
					case <-keepaliveStop:
						return
					case <-ctx.Done():
						return
					case <-ticker.C:
						writeMu.Lock()
						if writeErr == nil {
							_ = writeFlushed([]byte(": keepalive\n\n"))
						}
						writeMu.Unlock()
					}
				}
			}()
		}

		buf := make([]byte, 16384)
		for {
			n, rErr := resp.Body.Read(buf)
			if n > 0 {
				writeMu.Lock()
				if writeErr == nil {
					if tErr := sseTransform.Write(buf[:n]); tErr != nil {
						writeErr = tErr
					}
				}
				wFailed := writeErr != nil
				writeMu.Unlock()
				if wFailed {
					_ = resp.Body.Close()
					break
				}
			}
			if rErr != nil {
				break
			}
		}

		close(keepaliveStop)
		close(streamDone)

		writeMu.Lock()
		wErr := writeErr
		if wErr == nil && ctx.Err() == nil {
			_ = sseTransform.Flush()
		}
		writeMu.Unlock()

		if ctx.Err() != nil || wErr != nil {
			return true, false, ctx.Err()
		}

		compTokens := sseTransform.outputToks
		if compTokens <= 0 && sseTransform.chunkApprox > 0 {
			compTokens = sseTransform.chunkApprox
		}
		s.recordServedTelemetry(snap, start, compTokens)
		return true, false, nil
	}

	// Non-streaming HTTP response (JSON / error body).
	readDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = resp.Body.Close()
		case <-readDone:
		}
	}()
	rawRespBytes, rErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	close(readDone)
	if ctx.Err() != nil {
		return false, false, ctx.Err()
	}
	if rErr != nil {
		return false, false, sanitizeGatewayError(rErr)
	}

	if stCT == "" {
		stCT = "application/json; charset=utf-8"
	}
	compTokens := 0
	outBytes := rawRespBytes
	if stCode >= 200 && stCode < 300 && strings.Contains(strings.ToLower(stCT), "json") {
		switch path {
		case "/v1/chat/completions":
			outBytes, compTokens = normalizeChatCompletionsJSON(rawRespBytes, clientModelID)
		case "/v1/responses":
			outBytes, compTokens = normalizeResponsesJSON(rawRespBytes, clientModelID)
		case "/v1/messages":
			outBytes, compTokens = normalizeAnthropicJSON(rawRespBytes, clientModelID)
		}
	}
	w.Header().Set("Content-Type", stCT)
	w.WriteHeader(stCode)
	_, _ = w.Write(outBytes)

	if stCode >= 200 && stCode < 300 {
		s.recordServedTelemetry(snap, start, compTokens)
	}
	return true, false, nil
}
