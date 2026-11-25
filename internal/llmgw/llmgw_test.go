package llmgw_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/colabhive/colabhive/internal/events"
	"github.com/colabhive/colabhive/internal/llmgw"
	"github.com/colabhive/colabhive/internal/mockcolab"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/router"
	"github.com/colabhive/colabhive/internal/runtime"
	"github.com/colabhive/colabhive/internal/server"
	"github.com/colabhive/colabhive/internal/store"
)

type testHarness struct {
	st     models.Store
	hub    models.EventHub
	pool   models.AccountPoolService
	rtr    models.RouterService
	rt     *runtime.Engine
	svc    *llmgw.Service
	srv    *httptest.Server
	nowMu  sync.Mutex
	curNow time.Time
}

func newTestHarness(t *testing.T, idleInterval time.Duration) *testHarness {
	t.Helper()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite(:memory:): %v", err)
	}
	hub := events.NewHub()
	poolSvc := pool.NewService(st, hub, pool.Config{
		MasterSecret:  "test-master-secret-v11-llmgw",
		PublicBaseURL: "http://127.0.0.1:7860",
	})
	rtrSvc := router.NewServiceWithDirs(st, poolSvc, hub, t.TempDir(), t.TempDir(), t.TempDir(), "windows")
	rtEng := runtime.NewConcreteEngine(st, poolSvc, rtrSvc, hub, runtime.Options{
		DemoMode:               true,
		DisableBackgroundLoops: true,
		ArtifactsDir:           t.TempDir(),
	})

	h := &testHarness{
		st:     st,
		hub:    hub,
		pool:   poolSvc,
		rtr:    rtrSvc,
		rt:     rtEng,
		curNow: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC),
	}

	if idleInterval <= 0 {
		idleInterval = 5 * time.Second
	}
	mux := http.NewServeMux()
	httpSrv := httptest.NewServer(mux)
	svc := llmgw.NewService(poolSvc, rtEng, rtrSvc, hub, llmgw.Config{
		BaseURL:            httpSrv.URL,
		DemoMode:           true,
		DefaultIdleStopMin: 30,
		IdleCheckInterval:  idleInterval,
		NowFunc: func() time.Time {
			h.nowMu.Lock()
			defer h.nowMu.Unlock()
			return h.curNow
		},
	})
	server.RegisterV11LLMRoutes(mux, svc)

	h.svc = svc
	h.srv = httpSrv

	t.Cleanup(func() {
		httpSrv.Close()
		_ = svc.Close()
		_ = rtEng.Close()
		_ = st.Close()
	})
	return h
}

func (h *testHarness) advanceClock(d time.Duration) {
	h.nowMu.Lock()
	h.curNow = h.curNow.Add(d)
	h.nowMu.Unlock()
}

func seedTestAccount(
	t *testing.T,
	st models.Store,
	id string,
	email string,
	alias string,
	tier models.SubscriptionTier,
	gpus []models.AcceleratorType,
) *models.Account {
	t.Helper()
	now := time.Now().UTC()
	ent := make(map[models.AcceleratorType]models.GPUEntitlement, len(gpus))
	for _, g := range gpus {
		ent[g] = models.GPUEntitlement{
			Accelerator: g,
			Status:      models.EntitlementAvailable,
			VerifiedAt:  &now,
		}
	}
	paidCU := 0.0
	if tier != models.TierFree {
		paidCU = 300.0
	}
	acc := &models.Account{
		ID:                 id,
		Email:              email,
		Alias:              alias,
		Provider:           models.ProviderColab,
		SubscriptionTier:   tier,
		Health:             models.HealthHealthy,
		Enabled:            true,
		MaxConcurrentVMs:   2,
		PaidCUBalance:      paidCU,
		SchedulableCU:      paidCU,
		FreeQuota:          &models.FreeQuotaInfo{RemainingMilliCCU: 3500, RemainingFreeCU: 3.5},
		GPUEntitlements:    ent,
		PublicAPISupported: true,
		TunnelAPISupported: true,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := st.SaveAccount(context.Background(), acc); err != nil {
		t.Fatalf("SaveAccount(%s): %v", id, err)
	}
	return acc
}

func TestCatalogListingAndSpecMetadata(t *testing.T) {
	h := newTestHarness(t, 0)

	resp, err := http.Get(h.srv.URL + "/api/v1/llm/catalog")
	if err != nil {
		t.Fatalf("GET /api/v1/llm/catalog: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var specs []models.LLMModelSpec
	if err := json.NewDecoder(resp.Body).Decode(&specs); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	if len(specs) != 8 {
		t.Fatalf("expected 8 curated LLM specs, got %d", len(specs))
	}

	wantOrder := []struct {
		id         string
		freeReady  bool
		minVRAM    int
		gpu        models.AcceleratorType
		ollamaTag  string
		containsZh string
	}{
		{"qwen3.5-9b-heretic", true, 8, models.AcceleratorT4, "hf.co/mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF:Q4_K_M", "免费卡极速无审查"},
		{"gemma4-12b-heretic", true, 10, models.AcceleratorT4, "hf.co/culturerevolt/gemma-4-12b-heretic-abliterated-GGUF:Q4_K_M", "免费卡文笔与酒馆首选"},
		{"qwen3.8-27b-abliterated", false, 20, models.AcceleratorL4, "hf.co/huihui-ai/Huihui-Qwen3.8-27B-abliterated-GGUF:Q4_K_M", "450万下载社区口碑王"},
		{"qwen3.8-27b-heretic-coder", false, 20, models.AcceleratorL4, "hf.co/DavidAU/Qwen3.8-27B-TURBO-Fable-Cold-Fusion-735-882-Heretic-Uncensored-NEO-CODER-MAX-MTP-GGUF:Q4_K_M", "4.2K点赞编程与创作神作"},
		{"gemma4-31b-heretic", false, 24, models.AcceleratorA100, "hf.co/mradermacher/gemma-4-31B-it-ARA-heretic-GGUF:Q4_K_M", "酒馆角色扮演与小说王者"},
		{"glm5.3-flash-uncensored", false, 96, models.AcceleratorG4, "hf.co/GCSA-AiLab/GLM-5.3-Flash-Uncensored-RCO-GSQ-GGUF:latest", "智谱最新单文件 Q4 无审查旗舰"},
		{"qwen3.8-flash-next-abliterated", false, 56, models.AcceleratorG4, "hf.co/agentionai/Qwen3.8-Flash-Next-Gyro-abliterated-Huihui-GGUF:latest", "175万下载单卡旗舰"},
		{"deepseek-v4.1-flash-uncensored", false, 84, models.AcceleratorG4, "hf.co/huihui-ai/Huihui-DeepSeek-V4-Flash-abliterated-ds4-GGUF:latest", "284B MoE 单卡旗舰"},
	}

	for i, want := range wantOrder {
		got := specs[i]
		if got.ID != want.id {
			t.Errorf("spec[%d].ID = %q, want %q", i, got.ID, want.id)
		}
		if got.FreeTierReady != want.freeReady {
			t.Errorf("spec[%d].FreeTierReady = %v, want %v", i, got.FreeTierReady, want.freeReady)
		}
		if got.MinVRAMGB != want.minVRAM {
			t.Errorf("spec[%d].MinVRAMGB = %d, want %d", i, got.MinVRAMGB, want.minVRAM)
		}
		if got.RecommendedGPU != want.gpu {
			t.Errorf("spec[%d].RecommendedGPU = %q, want %q", i, got.RecommendedGPU, want.gpu)
		}
		if got.OllamaTag != want.ollamaTag {
			t.Errorf("spec[%d].OllamaTag = %q, want %q", i, got.OllamaTag, want.ollamaTag)
		}
		if !strings.Contains(got.DisplayNameZh, want.containsZh) {
			t.Errorf("spec[%d].DisplayNameZh = %q, missing %q", i, got.DisplayNameZh, want.containsZh)
		}
		// Guard against HF Ollama OCI registry invalid quantization suffixes (e.g. bare :Q4_K or :IQ1S).
		if strings.HasPrefix(got.OllamaTag, "hf.co/") {
			if idx := strings.LastIndex(got.OllamaTag, ":"); idx > 0 {
				suffix := strings.ToUpper(got.OllamaTag[idx+1:])
				for _, bad := range []string{"Q4_K", "Q3_K", "Q5_K", "Q4_K_L", "Q5_K_L", "IQ1S", "IQ1M"} {
					if suffix == bad {
						t.Errorf("spec[%d].OllamaTag %q uses invalid HF Ollama quantization suffix :%s", i, got.OllamaTag, suffix)
					}
				}
			}
		}
	}
}

func TestZeroAccountsReturnsActionableMessage(t *testing.T) {
	h := newTestHarness(t, 0)

	// 1. Direct start with 0 accounts
	startRes, err := http.Post(h.srv.URL+"/api/v1/llm/start", "application/json", strings.NewReader(`{"model_id":"qwen3-8b"}`))
	if err != nil {
		t.Fatalf("POST /api/v1/llm/start: %v", err)
	}
	defer startRes.Body.Close()
	if startRes.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when 0 accounts exist, got %d", startRes.StatusCode)
	}
	var errBody map[string]any
	_ = json.NewDecoder(startRes.Body).Decode(&errBody)
	msg, _ := errBody["error"].(string)
	if !strings.Contains(msg, "账号池") || !strings.Contains(msg, "Colab") {
		t.Fatalf("expected clear actionable bilingual error message, got %q", msg)
	}

	// 2. Cold OpenAI chat completion with 0 accounts
	chatRes, err := http.Post(
		h.srv.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"qwen3-8b","messages":[{"role":"user","content":"你好"}]}`),
	)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer chatRes.Body.Close()
	if chatRes.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on cold /v1/chat/completions with 0 accounts, got %d", chatRes.StatusCode)
	}
}

func TestOneClickStartHotSwitchStopAndPrivacyRedaction(t *testing.T) {
	h := newTestHarness(t, 0)
	rawEmail := "private-owner-secret@example.com"
	seedTestAccount(t, h.st, "acc_free_1", rawEmail, "", models.TierProPlus, []models.AcceleratorType{
		models.AcceleratorT4,
		models.AcceleratorL4,
		models.AcceleratorG4,
	})

	// 1. Start free-tier model -> should default to StrategyFreeFirst and mask raw email!
	res, err := http.Post(h.srv.URL+"/api/v1/llm/start", "application/json", strings.NewReader(`{"model_id":"qwen3-8b"}`))
	if err != nil {
		t.Fatalf("POST /api/v1/llm/start: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", res.StatusCode)
	}

	var st models.LLMGatewayStatus
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !st.Running || st.State != models.LLMStateReady {
		t.Fatalf("expected running ready state, got running=%v state=%s", st.Running, st.State)
	}
	if st.ActiveModelID != "qwen3-8b" {
		t.Errorf("ActiveModelID = %q, want qwen3-8b", st.ActiveModelID)
	}
	if st.Strategy != models.StrategyFreeFirst {
		t.Errorf("Strategy = %q, want %q", st.Strategy, models.StrategyFreeFirst)
	}
	if st.AssignedGPU != models.AcceleratorT4 {
		t.Errorf("AssignedGPU = %q, want T4", st.AssignedGPU)
	}
	if strings.Contains(st.AccountLabel, rawEmail) || strings.Contains(st.StatusStep, rawEmail) || strings.Contains(st.StatusStepZh, rawEmail) {
		t.Fatalf("PII LEAK: raw email %q appeared in status: %+v", rawEmail, st)
	}
	if !strings.Contains(st.AccountLabel, "p***@example.com") {
		t.Errorf("expected masked email label p***@example.com, got %q", st.AccountLabel)
	}
	firstSessionID := st.SessionID

	// 2. Hot-switch to pro 32B model (`qwen3-32b`) -> defaults to StrategyWasteZero, L4 GPU, stops old session
	res2, err := http.Post(h.srv.URL+"/api/v1/llm/start", "application/json", strings.NewReader(`{"model_id":"qwen3-32b"}`))
	if err != nil {
		t.Fatalf("hot-switch start: %v", err)
	}
	defer res2.Body.Close()
	var st2 models.LLMGatewayStatus
	_ = json.NewDecoder(res2.Body).Decode(&st2)
	if st2.ActiveModelID != "qwen3-32b" || st2.Strategy != models.StrategyWasteZero || st2.AssignedGPU != models.AcceleratorL4 {
		t.Fatalf("unexpected hot-switched status: %+v", st2)
	}
	if oldSess, _ := h.rt.GetSession(context.Background(), firstSessionID); oldSess != nil && oldSess.Status != models.SessionStopped {
		t.Fatalf("expected old session %s to be STOPPED after hot-switch, got %s", firstSessionID, oldSess.Status)
	}

	// 3. Stop gateway
	stopRes, err := http.Post(h.srv.URL+"/api/v1/llm/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/v1/llm/stop: %v", err)
	}
	defer stopRes.Body.Close()
	var stStop models.LLMGatewayStatus
	_ = json.NewDecoder(stopRes.Body).Decode(&stStop)
	if stStop.Running || stStop.State != models.LLMStateStopped || stStop.SessionID != "" {
		t.Fatalf("expected stopped gateway status, got %+v", stStop)
	}
}

func TestOpenAIGatewayStreamingAndNonStreaming(t *testing.T) {
	h := newTestHarness(t, 0)
	seedTestAccount(t, h.st, "acc_1", "user1@example.com", "主力免费卡", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})

	// 1. GET /v1/models when cold returns 0 deployed models (and ?catalog=1 returns all 8 catalog entries)
	modelsRes, err := http.Get(h.srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	defer modelsRes.Body.Close()
	var modelList struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(modelsRes.Body).Decode(&modelList); err != nil {
		t.Fatalf("decode /v1/models: %v", err)
	}
	if modelList.Object != "list" || len(modelList.Data) != 0 {
		t.Fatalf("expected 0 deployed models when gateway is idle, got %+v", modelList)
	}
	catRes, err := http.Get(h.srv.URL + "/v1/models?catalog=1")
	if err != nil {
		t.Fatalf("GET /v1/models?catalog=1: %v", err)
	}
	var catList struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.NewDecoder(catRes.Body).Decode(&catList)
	_ = catRes.Body.Close()
	if catList.Object != "list" || len(catList.Data) != 8 {
		t.Fatalf("unexpected /v1/models?catalog=1 response: %+v", catList)
	}

	// 2. Cold-start auto-launch via non-streaming POST /v1/chat/completions
	nonStreamBody := `{"messages":[{"role":"user","content":[{"type":"text","text":"请用Python写一个统计频次的函数"}]}],"stream":false}`
	chatRes, err := http.Post(h.srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(nonStreamBody))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions (stream=false): %v", err)
	}
	defer chatRes.Body.Close()
	if chatRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", chatRes.StatusCode)
	}
	var cmpl struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(chatRes.Body).Decode(&cmpl); err != nil {
		t.Fatalf("decode completion: %v", err)
	}
	if cmpl.Object != "chat.completion" || cmpl.Model != llmgw.DefaultModelID {
		t.Fatalf("unexpected completion header: %+v", cmpl)
	}
	if len(cmpl.Choices) != 1 || cmpl.Choices[0].Message.Content == "" || cmpl.Choices[0].FinishReason != "stop" {
		t.Fatalf("unexpected completion choices: %+v", cmpl.Choices)
	}
	if cmpl.Usage.TotalTokens <= 0 {
		t.Errorf("expected positive TotalTokens, got %d", cmpl.Usage.TotalTokens)
	}

	// 3. Streaming SSE via authenticated alias POST /api/v1/llm/chat/completions
	streamBody := `{"model":"qwen3.5-9b-heretic","messages":[{"role":"user","content":"Hello ColabHive!"}],"stream":true}`
	sRes, err := http.Post(h.srv.URL+"/api/v1/llm/chat/completions", "application/json", strings.NewReader(streamBody))
	if err != nil {
		t.Fatalf("POST /api/v1/llm/chat/completions (stream=true): %v", err)
	}
	defer sRes.Body.Close()
	if ct := sRes.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream Content-Type, got %q", ct)
	}

	var assembled strings.Builder
	sawDone := false
	scanner := bufio.NewScanner(sRes.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			sawDone = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE JSON frame %q: %v", payload, err)
		}
		if len(chunk.Choices) > 0 {
			assembled.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if !sawDone {
		t.Fatalf("missing data: [DONE] sentinel in OpenAI SSE stream")
	}
	if assembled.Len() == 0 {
		t.Fatalf("assembled OpenAI SSE content was empty")
	}
}

func TestAnthropicMessagesStreamingAndNonStreaming(t *testing.T) {
	h := newTestHarness(t, 0)
	seedTestAccount(t, h.st, "acc_1", "claude-user@example.com", "科研免费号", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})

	// 1. Non-streaming POST /v1/messages
	nonStreamReq := `{
		"model": "claude-3-7-sonnet-latest",
		"max_tokens": 512,
		"system": [{"type": "text", "text": "You are a helpful coding assistant."}],
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Explain zero-tunnel GPU pooling."}]}],
		"stream": false
	}`
	res, err := http.Post(h.srv.URL+"/v1/messages", "application/json", strings.NewReader(nonStreamReq))
	if err != nil {
		t.Fatalf("POST /v1/messages (stream=false): %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	var msgResp struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Role       string `json:"role"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&msgResp); err != nil {
		t.Fatalf("decode Anthropic response: %v", err)
	}
	if msgResp.Type != "message" || msgResp.Role != "assistant" || msgResp.StopReason != "end_turn" {
		t.Fatalf("unexpected Anthropic response envelope: %+v", msgResp)
	}
	if msgResp.Model != llmgw.DefaultModelID {
		t.Fatalf("expected actual served model %s echoed (not proprietary alias), got %q", llmgw.DefaultModelID, msgResp.Model)
	}
	if len(msgResp.Content) != 1 || msgResp.Content[0].Text == "" {
		t.Fatalf("empty Anthropic text content: %+v", msgResp.Content)
	}

	// 2. Requesting a different explicit model while warm MUST return 409 Conflict!
	streamReq := `{
		"model": "gemma4-12b-heretic",
		"max_tokens": 512,
		"messages": [{"role": "user", "content": "你好，请分析一下多账号自动切换原理"}],
		"stream": true
	}`
	mismatchRes, err := http.Post(h.srv.URL+"/v1/messages", "application/json", strings.NewReader(streamReq))
	if err != nil {
		t.Fatalf("POST /v1/messages (mismatch): %v", err)
	}
	_ = mismatchRes.Body.Close()
	if mismatchRes.StatusCode != http.StatusConflict {
		t.Fatalf("expected HTTP 409 Conflict for warm model mismatch, got %d", mismatchRes.StatusCode)
	}

	// Switch explicitly to gemma4-12b-heretic via /api/v1/llm/start, then stream cleanly!
	swRes, err := http.Post(h.srv.URL+"/api/v1/llm/start", "application/json", strings.NewReader(`{"model_id":"gemma4-12b-heretic"}`))
	if err != nil {
		t.Fatalf("POST /api/v1/llm/start switch: %v", err)
	}
	_ = swRes.Body.Close()
	if swRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 switching to gemma4-12b-heretic, got %d", swRes.StatusCode)
	}

	// 3. Streaming POST /v1/messages
	sRes, err := http.Post(h.srv.URL+"/v1/messages", "application/json", strings.NewReader(streamReq))
	if err != nil {
		t.Fatalf("POST /v1/messages (stream=true): %v", err)
	}
	defer sRes.Body.Close()

	seenEvents := make(map[string]int)
	var deltaText strings.Builder
	scanner := bufio.NewScanner(sRes.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			evName := strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			seenEvents[evName]++
		} else if strings.HasPrefix(line, "data: ") {
			raw := strings.TrimPrefix(line, "data: ")
			var frame struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(raw), &frame); err == nil && frame.Type == "content_block_delta" {
				deltaText.WriteString(frame.Delta.Text)
			}
		}
	}

	for _, requiredEvent := range []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	} {
		if seenEvents[requiredEvent] == 0 {
			t.Errorf("missing required Anthropic SSE event %q (seen: %v)", requiredEvent, seenEvents)
		}
	}
	if deltaText.Len() == 0 {
		t.Fatalf("expected non-empty Anthropic streaming text deltas")
	}
}

func TestOllamaNativeTagsChatAndGenerate(t *testing.T) {
	h := newTestHarness(t, 0)
	seedTestAccount(t, h.st, "acc_ollama", "ollama-tester@example.com", "本地直连号", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})

	// 1. GET /api/tags when cold returns 0 deployed models (?catalog=1 lists full catalog)
	tagsRes, err := http.Get(h.srv.URL + "/api/tags")
	if err != nil {
		t.Fatalf("GET /api/tags: %v", err)
	}
	defer tagsRes.Body.Close()
	var tagsPayload struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.NewDecoder(tagsRes.Body).Decode(&tagsPayload); err != nil {
		t.Fatalf("decode /api/tags: %v", err)
	}
	if len(tagsPayload.Models) != 0 {
		t.Fatalf("expected 0 deployed models on idle /api/tags, got %+v", tagsPayload)
	}
	tagsCatRes, err := http.Get(h.srv.URL + "/api/tags?catalog=1")
	if err != nil {
		t.Fatalf("GET /api/tags?catalog=1: %v", err)
	}
	var tagsCatPayload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	_ = json.NewDecoder(tagsCatRes.Body).Decode(&tagsCatPayload)
	_ = tagsCatRes.Body.Close()
	if len(tagsCatPayload.Models) != 8 || tagsCatPayload.Models[0].Name != "hf.co/mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF:Q4_K_M" {
		t.Fatalf("unexpected /api/tags?catalog=1 payload: %+v", tagsCatPayload)
	}

	// 2. POST /api/chat (NDJSON default streaming when stream is omitted)
	chatRes, err := http.Post(
		h.srv.URL+"/api/chat",
		"application/json",
		strings.NewReader(`{"model":"qwen3.5-9b-heretic","messages":[{"role":"user","content":"测试 Ollama 流式对话"}]}`),
	)
	if err != nil {
		t.Fatalf("POST /api/chat: %v", err)
	}
	defer chatRes.Body.Close()

	var chatText strings.Builder
	sawDone := false
	scanner := bufio.NewScanner(chatRes.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Done bool `json:"done"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatalf("decode NDJSON line %q: %v", line, err)
		}
		chatText.WriteString(item.Message.Content)
		if item.Done {
			sawDone = true
		}
	}
	if !sawDone || chatText.Len() == 0 {
		t.Fatalf("Ollama NDJSON /api/chat incomplete: done=%v len=%d", sawDone, chatText.Len())
	}

	// 3. POST /api/generate (stream=false JSON)
	genRes, err := http.Post(
		h.srv.URL+"/api/generate",
		"application/json",
		strings.NewReader(`{"model":"qwen3.5-9b-heretic","prompt":"Summarize GPU pooling","stream":false}`),
	)
	if err != nil {
		t.Fatalf("POST /api/generate: %v", err)
	}
	defer genRes.Body.Close()
	var genPayload struct {
		Model    string `json:"model"`
		Response string `json:"response"`
		Done     bool   `json:"done"`
	}
	if err := json.NewDecoder(genRes.Body).Decode(&genPayload); err != nil {
		t.Fatalf("decode /api/generate: %v", err)
	}
	if !genPayload.Done || genPayload.Response == "" || genPayload.Model != "hf.co/mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF:Q4_K_M" {
		t.Fatalf("unexpected /api/generate payload: %+v", genPayload)
	}
}

func TestAutomaticCrossAccountFailoverWhenSessionDiesMidFlight(t *testing.T) {
	h := newTestHarness(t, 0)
	email1 := "alpha-preempted@example.com"
	email2 := "bravo-standby@example.com"
	seedTestAccount(t, h.st, "acc_alpha", email1, "", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})
	seedTestAccount(t, h.st, "acc_bravo", email2, "二号备用卡", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})

	wsCtx, wsCancel := context.WithCancel(context.Background())
	defer wsCancel()
	evCh, unsub := h.hub.Subscribe(wsCtx)
	defer unsub()

	// Start gateway -> lands on first account
	st1, err := h.svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"})
	if err != nil {
		t.Fatalf("initial Start: %v", err)
	}
	firstAcctID := st1.AccountID
	firstSessID := st1.SessionID
	if firstSessID == "" {
		t.Fatalf("expected non-empty initial SessionID")
	}

	// Simulate Account #1 Colab VM preemption mid-flight by stopping session 1 directly!
	if err := h.rt.StopSession(context.Background(), firstSessID); err != nil {
		t.Fatalf("StopSession(%s): %v", firstSessID, err)
	}

	// Send streaming OpenAI chat request -> must seamlessly fail over to the OTHER healthy account without dropping chat!
	reqBody := `{"model":"qwen3-8b","messages":[{"role":"user","content":"原账号掉线了还能继续回答吗？"}],"stream":true}`
	res, err := http.Post(h.srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(reqBody))
	if err != nil {
		t.Fatalf("POST /v1/chat/completions during failover: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected seamless 200 OK on failover, got %d", res.StatusCode)
	}

	var reply strings.Builder
	sawDone := false
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		ln := scanner.Text()
		if !strings.HasPrefix(ln, "data: ") {
			continue
		}
		data := strings.TrimPrefix(ln, "data: ")
		if data == "[DONE]" {
			sawDone = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err == nil && len(chunk.Choices) > 0 {
			reply.WriteString(chunk.Choices[0].Delta.Content)
		}
	}
	if !sawDone || reply.Len() == 0 {
		t.Fatalf("expected uninterrupted SSE completion after failover, got done=%v reply=%q", sawDone, reply.String())
	}

	after := h.svc.Status()
	if after.FailoverCount != 1 {
		t.Fatalf("expected FailoverCount == 1, got %d", after.FailoverCount)
	}
	if after.AccountID == firstAcctID {
		t.Fatalf("expected failover onto standby account, still on %q", after.AccountID)
	}
	if after.SessionID == firstSessID || after.SessionID == "" {
		t.Fatalf("expected fresh SessionID after failover, got %q", after.SessionID)
	}
	if after.LastFailoverNote == "" {
		t.Fatalf("expected non-empty LastFailoverNote")
	}
	if strings.Contains(after.LastFailoverNote, email1) || strings.Contains(after.LastFailoverNote, email2) {
		t.Fatalf("PII LEAK in LastFailoverNote: %q", after.LastFailoverNote)
	}

	// Verify WSEventLLMGateway events were published including failover & ready states
	sawFailoverEvent := false
	sawReadyAfterFailover := false
drainLoop:
	for {
		select {
		case ev := <-evCh:
			if ev.Type != models.WSEventLLMGateway {
				continue
			}
			if payload, ok := ev.Payload.(models.LLMGatewayStatus); ok {
				if payload.State == models.LLMStateFailover {
					sawFailoverEvent = true
				}
				if payload.State == models.LLMStateReady && payload.FailoverCount == 1 {
					sawReadyAfterFailover = true
				}
			}
		default:
			break drainLoop
		}
	}
	if !sawFailoverEvent || !sawReadyAfterFailover {
		t.Fatalf("expected WSEventLLMGateway failover+ready broadcasts, got failover=%v ready=%v", sawFailoverEvent, sawReadyAfterFailover)
	}
}

// fakeLiveOllamaRuntime verifies the real Linux Colab VM `COLABHIVE_LLM_LIVE:` + `COLABHIVE_LLM_RESP:` Python loopback bridge!
type fakeLiveOllamaRuntime struct {
	models.RuntimeEngine
	mu       sync.Mutex
	sessions map[string]*models.RuntimeSession
}

func (f *fakeLiveOllamaRuntime) CreateSession(_ context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess := &models.RuntimeSession{
		ID:          "sess_live_colab_01",
		Name:        req.Name,
		AccountID:   "acc_live_1",
		Accelerator: req.Accelerator,
		Status:      models.SessionIdle,
		ProxyURL:    "https://gpu-t4-s-realcolab.prod.colab.dev",
		Endpoint:    "gpu-t4-s-realcolab",
	}
	f.sessions[sess.ID] = sess
	return sess, nil
}

func (f *fakeLiveOllamaRuntime) GetSession(_ context.Context, id string) (*models.RuntimeSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[id]
	if !ok {
		return nil, errors.New("session not found")
	}
	cp := *sess
	return &cp, nil
}

func (f *fakeLiveOllamaRuntime) StopSession(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sess, ok := f.sessions[id]; ok {
		sess.Status = models.SessionStopped
	}
	return nil
}

func (f *fakeLiveOllamaRuntime) ExecuteCode(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	if strings.Contains(req.Code, "ollama.com/install.sh") {
		if !strings.Contains(req.Code, "127.0.0.1:11434/api/tags") || !strings.Contains(req.Code, "returncode != 0") {
			return &models.KernelExecResult{
				Status: "error",
				Stderr: "bootstrap script missing /api/tags health check or install exit-code check",
			}, nil
		}
		return &models.KernelExecResult{
			Status: "ok",
			Stdout: "COLABHIVE_LLM_LIVE:{\"ok\":true,\"tag\":\"deepseek-r1:14b\"}\n",
		}, nil
	}
	if strings.Contains(req.Code, "127.0.0.1:11434/api/chat") {
		payload, _ := json.Marshal(map[string]any{
			"content":           "Real Ollama loopback inference succeeded!",
			"chunks":            []string{"Real Ollama ", "loopback ", "inference succeeded!"},
			"prompt_tokens":     18,
			"completion_tokens": 6,
			"done":              true,
		})
		b64 := base64.StdEncoding.EncodeToString(payload)
		return &models.KernelExecResult{
			Status: "ok",
			Stdout: "COLABHIVE_LLM_RESP:" + b64 + "\n",
		}, nil
	}
	return &models.KernelExecResult{Status: "ok"}, nil
}

func TestLiveColabOllamaLoopbackBridgeAndIdleAutoStop(t *testing.T) {
	fakeRT := &fakeLiveOllamaRuntime{sessions: make(map[string]*models.RuntimeSession)}
	var nowMu sync.Mutex
	curNow := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	svc := llmgw.NewService(nil, fakeRT, nil, events.NewHub(), llmgw.Config{
		BaseURL:            "http://127.0.0.1:7860",
		DemoMode:           false,
		DefaultIdleStopMin: 15,
		IdleCheckInterval:  20 * time.Millisecond,
		NowFunc: func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return curNow
		},
	})
	defer svc.Close()

	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Start deepseek-r1-14b in live Colab mode
	st, err := svc.Start(context.Background(), models.LLMStartRequest{
		ModelID:         "deepseek-r1-14b",
		IdleAutoStopMin: 10,
	})
	if err != nil {
		t.Fatalf("Start live mode: %v", err)
	}
	if !st.Running || st.ActiveModelID != "deepseek-r1-14b" {
		t.Fatalf("unexpected live status: %+v", st)
	}

	// 2. Call OpenAI chat completion -> should receive exact bridge tokens & token counts
	body := bytes.NewBufferString(`{"model":"deepseek-r1-14b","messages":[{"role":"user","content":"Ping real Ollama"}]}`)
	res, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", body)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions: %v", err)
	}
	defer res.Body.Close()
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode live completion: %v", err)
	}
	if len(parsed.Choices) != 1 || parsed.Choices[0].Message.Content != "Real Ollama loopback inference succeeded!" {
		t.Fatalf("unexpected live bridge content: %+v", parsed.Choices)
	}
	if parsed.Usage.PromptTokens != 18 || parsed.Usage.CompletionTokens != 6 {
		t.Fatalf("unexpected live bridge usage: %+v", parsed.Usage)
	}

	// 3. Advance clock past IdleAutoStopMin (10m) and verify automatic idle shutdown!
	nowMu.Lock()
	curNow = curNow.Add(11 * time.Minute)
	nowMu.Unlock()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !svc.Status().Running {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	finalSt := svc.Status()
	if finalSt.Running || finalSt.State != models.LLMStateStopped {
		t.Fatalf("expected idle watchdog to auto-stop session after 11m, got %+v", finalSt)
	}
	if !strings.Contains(finalSt.StatusStepZh, "自动释放") {
		t.Errorf("expected idle auto-stop StatusStepZh note, got %q", finalSt.StatusStepZh)
	}
}

func TestMockColabJupyterWebSocketExecutionAndPreload(t *testing.T) {
	mockSrv := mockcolab.NewServer()
	defer mockSrv.Close()

	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	hub := events.NewHub()
	poolSvc := pool.NewService(st, hub, pool.Config{
		GoogleAuthURI:      mockSrv.URL + "/o/oauth2/auth",
		GoogleTokenURI:     mockSrv.URL + "/token",
		GoogleTokenInfoURI: mockSrv.URL + "/tokeninfo",
		GoogleRevokeURI:    mockSrv.URL + "/revoke",
		GoogleUserInfoURI:  mockSrv.URL + "/oauth2/v2/userinfo",
		ColabTunnelDomain:  mockSrv.URL,
		ColabGAPIDomain:    mockSrv.URL,
		ColabPublicDomain:  mockSrv.URL,
	})
	if _, err := mockcolab.SeedDemoCluster(context.Background(), st, poolSvc, mockSrv); err != nil {
		t.Fatalf("SeedDemoCluster: %v", err)
	}

	rtrSvc := router.NewServiceWithDirs(st, poolSvc, hub, t.TempDir(), t.TempDir(), t.TempDir(), "windows")
	rtEng := runtime.NewConcreteEngine(st, poolSvc, rtrSvc, hub, runtime.Options{
		DemoMode:               false, // Exercise real XSRF /tun/m/assign + ZMQ-over-WebSocket Jupyter v5.3!
		TunnelBaseURL:          mockSrv.URL,
		PublicAPIBaseURL:       mockSrv.URL,
		DisableBackgroundLoops: true,
		ArtifactsDir:           t.TempDir(),
	})
	defer rtEng.Close()

	// 0. Non-demo mode against mockcolab (no real Ollama daemon) MUST fail honestly instead of returning fake text!
	strictSvc := llmgw.NewService(poolSvc, rtEng, rtrSvc, hub, llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: false,
	})
	defer strictSvc.Close()
	if _, strictErr := strictSvc.Start(context.Background(), models.LLMStartRequest{ModelID: "deepseek-r1-70b"}); strictErr == nil {
		t.Fatal("SECURITY/HONESTY FAILURE: DemoMode=false silently succeeded without real COLABHIVE_LLM_LIVE confirmation!")
	}

	// 1. Explicit DemoMode=true over real mockcolab XSRF + ZMQ WebSocket succeeds cleanly
	svc := llmgw.NewService(poolSvc, rtEng, rtrSvc, hub, llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: true,
	})
	defer svc.Close()

	mux := http.NewServeMux()
	server.RegisterV11LLMRoutes(mux, svc)
	httpSrv := httptest.NewServer(mux)
	defer httpSrv.Close()

	// 1. Ollama empty-messages preload (`POST /api/chat` with messages=[])
	preloadRes, err := http.Post(httpSrv.URL+"/api/chat", "application/json", strings.NewReader(`{"model":"deepseek-v4.1-flash-uncensored","messages":[]}`))
	if err != nil {
		t.Fatalf("Ollama preload: %v", err)
	}
	defer preloadRes.Body.Close()
	if preloadRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on Ollama preload, got %d", preloadRes.StatusCode)
	}

	stSnap := svc.Status()
	if !stSnap.Running || stSnap.ActiveModelID != "deepseek-v4.1-flash-uncensored" || stSnap.AssignedGPU != models.AcceleratorG4 {
		t.Fatalf("expected deepseek-v4.1-flash-uncensored running on G4 via mockcolab WebSocket, got %+v", stSnap)
	}

	// 2. Execute reasoning chat over mockcolab ZMQ WebSocket fallback
	chatRes, err := http.Post(
		httpSrv.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"deepseek-v4.1-flash-uncensored","messages":[{"role":"user","content":"测试 96GB 推理链"}],"stream":false}`),
	)
	if err != nil {
		t.Fatalf("chat over mockcolab: %v", err)
	}
	defer chatRes.Body.Close()
	var cmpl struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(chatRes.Body).Decode(&cmpl)
	if len(cmpl.Choices) != 1 || !strings.Contains(cmpl.Choices[0].Message.Content, "DeepSeek-V4-Flash") {
		t.Fatalf("expected completion reply from deepseek-v4.1-flash-uncensored, got %+v", cmpl.Choices)
	}
}

func TestConcurrentFailoverDeduplicationAndExtendedWireEndpoints(t *testing.T) {
	h := newTestHarness(t, 0)
	seedTestAccount(t, h.st, "acc_c1", "concurrent1@example.com", "节点一", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})
	seedTestAccount(t, h.st, "acc_c2", "concurrent2@example.com", "节点二", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})

	// 1. Extended OpenAI / Anthropic / Ollama discovery endpoints
	mRes, err := http.Get(h.srv.URL + "/v1/models/qwen3-8b")
	if err != nil {
		t.Fatalf("GET /v1/models/qwen3-8b: %v", err)
	}
	_ = mRes.Body.Close()
	if mRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /v1/models/qwen3-8b, got %d", mRes.StatusCode)
	}

	ctRes, err := http.Post(
		h.srv.URL+"/v1/messages/count_tokens",
		"application/json",
		strings.NewReader(`{"model":"qwen3-8b","messages":[{"role":"user","content":"Count these tokens please"}]}`),
	)
	if err != nil {
		t.Fatalf("POST /v1/messages/count_tokens: %v", err)
	}
	var ctBody struct {
		InputTokens int `json:"input_tokens"`
	}
	_ = json.NewDecoder(ctRes.Body).Decode(&ctBody)
	_ = ctRes.Body.Close()
	if ctBody.InputTokens <= 0 {
		t.Fatalf("expected positive input_tokens, got %d", ctBody.InputTokens)
	}

	showRes, err := http.Post(h.srv.URL+"/api/show", "application/json", strings.NewReader(`{"name":"deepseek-r1:14b"}`))
	if err != nil {
		t.Fatalf("POST /api/show: %v", err)
	}
	var showBody struct {
		Capabilities []string `json:"capabilities"`
	}
	_ = json.NewDecoder(showRes.Body).Decode(&showBody)
	_ = showRes.Body.Close()
	if len(showBody.Capabilities) < 2 {
		t.Fatalf("expected thinking+completion capabilities on deepseek-r1:14b, got %v", showBody.Capabilities)
	}

	// 2. Start session 1, kill it, then blast 12 concurrent requests across OpenAI/Anthropic/Ollama
	st1, err := h.svc.Start(context.Background(), models.LLMStartRequest{ModelID: llmgw.DefaultModelID})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := h.rt.StopSession(context.Background(), st1.SessionID); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			var resp *http.Response
			var reqErr error
			switch idx % 3 {
			case 0:
				resp, reqErr = http.Post(
					h.srv.URL+"/v1/chat/completions",
					"application/json",
					strings.NewReader(`{"model":"qwen3.5-9b-heretic","messages":[{"role":"developer","content":"Be concise"},{"role":"user","content":"Concurrent ping"}],"stream":false}`),
				)
			case 1:
				resp, reqErr = http.Post(
					h.srv.URL+"/v1/messages",
					"application/json",
					strings.NewReader(`{"model":"qwen3.5-9b-heretic","messages":[{"role":"user","content":"Concurrent Claude ping"}],"stream":false}`),
				)
			default:
				resp, reqErr = http.Post(
					h.srv.URL+"/api/chat",
					"application/json",
					strings.NewReader(`{"model":"qwen3.5-9b-heretic","messages":[{"role":"user","content":"Concurrent Ollama ping"}],"stream":false}`),
				)
			}
			if reqErr != nil {
				errs <- reqErr
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errs <- errors.New("non-200 status during concurrent failover")
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent request error: %v", e)
	}

	stAfter := h.svc.Status()
	if stAfter.FailoverCount != 1 {
		t.Fatalf("expected deduplicated FailoverCount == 1 across %d concurrent callers, got %d", workers, stAfter.FailoverCount)
	}

	// 3. Verify GET /api/ps reports loaded VRAM model, then Ollama keep_alive=0 unloads it cleanly
	psRes, err := http.Get(h.srv.URL + "/api/ps")
	if err != nil {
		t.Fatalf("GET /api/ps: %v", err)
	}
	var psBody struct {
		Models []map[string]any `json:"models"`
	}
	_ = json.NewDecoder(psRes.Body).Decode(&psBody)
	_ = psRes.Body.Close()
	if len(psBody.Models) != 1 {
		t.Fatalf("expected 1 active model in /api/ps, got %d", len(psBody.Models))
	}

	unloadRes, err := http.Post(h.srv.URL+"/api/generate", "application/json", strings.NewReader(`{"model":"qwen3:8b","keep_alive":0}`))
	if err != nil {
		t.Fatalf("Ollama keep_alive=0 unload: %v", err)
	}
	_ = unloadRes.Body.Close()
	if h.svc.Status().Running {
		t.Fatalf("expected keep_alive=0 to stop active GPU session cleanly")
	}
}

func TestCustomHuggingFaceGGUFNormalizationAndShellInjectionDefense(t *testing.T) {
	// 1. Unit verification of URL/tag normalization & parameter-based GPU/FreeTier inference
	validCases := []struct {
		raw       string
		wantTag   string
		wantGPU   models.AcceleratorType
		wantFree  bool
		wantMinGB int
	}{
		{
			raw:       "hf.co/unsloth/gemma-3-27b-it-GGUF:Q4_K_M",
			wantTag:   "hf.co/unsloth/gemma-3-27b-it-GGUF:Q4_K_M",
			wantGPU:   models.AcceleratorL4,
			wantFree:  false,
			wantMinGB: 22,
		},
		{
			raw:       "https://huggingface.co/bartowski/Llama-3.3-70B-Instruct-GGUF",
			wantTag:   "hf.co/bartowski/Llama-3.3-70B-Instruct-GGUF",
			wantGPU:   models.AcceleratorG4,
			wantFree:  false,
			wantMinGB: 48,
		},
		{
			raw:       "https://huggingface.co/unsloth/Qwen3-14B-GGUF/tree/main",
			wantTag:   "hf.co/unsloth/Qwen3-14B-GGUF",
			wantGPU:   models.AcceleratorT4,
			wantFree:  true,
			wantMinGB: 12,
		},
		{
			raw:       "ollama run huihui_ai/qwen2.5-abliterated:7b",
			wantTag:   "huihui_ai/qwen2.5-abliterated:7b",
			wantGPU:   models.AcceleratorT4,
			wantFree:  true,
			wantMinGB: 8,
		},
		{
			raw:       "mistral-large:123b",
			wantTag:   "mistral-large:123b",
			wantGPU:   models.AcceleratorG4,
			wantFree:  false,
			wantMinGB: 48,
		},
		{
			raw:       "codestral:34b",
			wantTag:   "codestral:34b",
			wantGPU:   models.AcceleratorL4,
			wantFree:  false,
			wantMinGB: 22,
		},
	}

	for _, tc := range validCases {
		spec, err := llmgw.ResolveModelSpec(tc.raw)
		if err != nil {
			t.Fatalf("ResolveModelSpec(%q) unexpected error: %v", tc.raw, err)
		}
		if spec.ID != tc.wantTag || spec.OllamaTag != tc.wantTag {
			t.Errorf("ResolveModelSpec(%q) tag = (%q, %q), want %q", tc.raw, spec.ID, spec.OllamaTag, tc.wantTag)
		}
		if spec.RecommendedGPU != tc.wantGPU {
			t.Errorf("ResolveModelSpec(%q).RecommendedGPU = %q, want %q", tc.raw, spec.RecommendedGPU, tc.wantGPU)
		}
		if spec.FreeTierReady != tc.wantFree {
			t.Errorf("ResolveModelSpec(%q).FreeTierReady = %v, want %v", tc.raw, spec.FreeTierReady, tc.wantFree)
		}
		if spec.MinVRAMGB != tc.wantMinGB {
			t.Errorf("ResolveModelSpec(%q).MinVRAMGB = %d, want %d", tc.raw, spec.MinVRAMGB, tc.wantMinGB)
		}
	}

	// 2. Shell-injection, path-traversal, CLI-flag-injection, non-HF URL & length cap defense
	maliciousInputs := []string{
		"hf.co/owner/repo; rm -rf /",
		"hf.co/owner/repo$(whoami)",
		"hf.co/owner/`id`",
		"hf.co/owner/repo|cat /etc/passwd",
		"hf.co/owner/repo&&curl evil.example.com",
		"hf.co/owner/repo\nid",
		"hf.co/../../etc/passwd",
		"../secret:latest",
		"-rf",
		"--help",
		"https://evil.example.com/backdoor.gguf",
		"hf.co/onlyowner",
		"hf.co/owner/repo:",
		strings.Repeat("a", 181),
	}
	for _, bad := range maliciousInputs {
		if _, err := llmgw.ResolveModelSpec(bad); err == nil {
			t.Fatalf("SECURITY FAILURE: ResolveModelSpec(%q) succeeded, expected rejection!", bad)
		}
	}

	// 3. End-to-end HTTP Start + Chat + Rejection via /api/v1/llm/start & /v1/chat/completions
	h := newTestHarness(t, 0)
	seedTestAccount(t, h.st, "acc_hf_1", "hf-pro@example.com", "HF测试号", models.TierProPlus, []models.AcceleratorType{
		models.AcceleratorT4,
		models.AcceleratorL4,
		models.AcceleratorG4,
	})

	// Reject shell injection over HTTP with 400 Bad Request
	badRes, err := http.Post(
		h.srv.URL+"/api/v1/llm/start",
		"application/json",
		strings.NewReader(`{"model_id":"hf.co/unsloth/model; curl attacker.tld"}`),
	)
	if err != nil {
		t.Fatalf("POST malicious start: %v", err)
	}
	_ = badRes.Body.Close()
	if badRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for shell-injection model_id, got %d", badRes.StatusCode)
	}

	// Start custom Hugging Face URL -> auto-normalizes to hf.co/... and boots on L4
	goodRes, err := http.Post(
		h.srv.URL+"/api/v1/llm/start",
		"application/json",
		strings.NewReader(`{"model_id":"https://huggingface.co/unsloth/gemma-3-27b-it-GGUF:Q4_K_M"}`),
	)
	if err != nil {
		t.Fatalf("POST custom HF start: %v", err)
	}
	var st models.LLMGatewayStatus
	_ = json.NewDecoder(goodRes.Body).Decode(&st)
	_ = goodRes.Body.Close()
	if goodRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for custom HF GGUF start, got %d (%+v)", goodRes.StatusCode, st)
	}
	if st.ActiveModelID != "hf.co/unsloth/gemma-3-27b-it-GGUF:Q4_K_M" || st.AssignedGPU != models.AcceleratorL4 {
		t.Fatalf("unexpected custom HF status: %+v", st)
	}

	// Verify active custom HF model appears in /v1/models & serves completions cleanly
	modelsRes, err := http.Get(h.srv.URL + "/v1/models")
	if err != nil {
		t.Fatalf("GET /v1/models: %v", err)
	}
	var mList struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.NewDecoder(modelsRes.Body).Decode(&mList)
	_ = modelsRes.Body.Close()
	if len(mList.Data) != 1 || mList.Data[0].ID != "hf.co/unsloth/gemma-3-27b-it-GGUF:Q4_K_M" {
		t.Fatalf("expected ONLY the deployed custom HF model in /v1/models, got %+v", mList.Data)
	}

	chatRes, err := http.Post(
		h.srv.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"hf.co/unsloth/gemma-3-27b-it-GGUF:Q4_K_M","messages":[{"role":"user","content":"你好自定义模型"}],"stream":false}`),
	)
	if err != nil {
		t.Fatalf("POST /v1/chat/completions custom HF: %v", err)
	}
	_ = chatRes.Body.Close()
	if chatRes.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on custom HF chat completion, got %d", chatRes.StatusCode)
	}
}

// configurableTestRuntime supports deterministic testing of non-demo honesty, NDJSON error/done checks,
// 4xx non-failover, explicit Stop() lifecycleGen lock, failed Stop() teardown retention, and slow SSE keepalives.
type configurableTestRuntime struct {
	models.RuntimeEngine
	mu           sync.Mutex
	createCount  atomic.Int32
	stopCount    atomic.Int32
	sessions     map[string]*models.RuntimeSession
	onExecute    func(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error)
	onStop       func(ctx context.Context, id string) error
	lastAllocReq models.SessionAllocRequest
	sessionPrefx string
}

func newConfigurableTestRuntime() *configurableTestRuntime {
	return &configurableTestRuntime{
		sessions:     make(map[string]*models.RuntimeSession),
		sessionPrefx: "sess_cfg_",
	}
}

func (r *configurableTestRuntime) CreateSession(_ context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error) {
	n := r.createCount.Add(1)
	id := fmt.Sprintf("%s%d", r.sessionPrefx, n)
	r.mu.Lock()
	r.lastAllocReq = req
	r.mu.Unlock()
	sess := &models.RuntimeSession{
		ID:          id,
		Name:        req.Name,
		AccountID:   fmt.Sprintf("acc_cfg_%d", n),
		Accelerator: req.Accelerator,
		Status:      models.SessionIdle,
		ProxyURL:    "demo://should-not-trigger-sim-when-demomode-false",
		Endpoint:    "demo-mock-endpoint",
	}
	r.mu.Lock()
	r.sessions[id] = sess
	r.mu.Unlock()
	return sess, nil
}

func (r *configurableTestRuntime) GetSession(_ context.Context, id string) (*models.RuntimeSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sess, ok := r.sessions[id]
	if !ok {
		return nil, errors.New("session not found")
	}
	cp := *sess
	return &cp, nil
}

func (r *configurableTestRuntime) StopSession(ctx context.Context, id string) error {
	r.stopCount.Add(1)
	if r.onStop != nil {
		if err := r.onStop(ctx, id); err != nil {
			return err
		}
	}
	r.mu.Lock()
	if sess, ok := r.sessions[id]; ok {
		sess.Status = models.SessionStopped
	}
	r.mu.Unlock()
	return nil
}

func (r *configurableTestRuntime) ExecuteCode(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	if r.onExecute != nil {
		return r.onExecute(ctx, req)
	}
	return &models.KernelExecResult{Status: "ok"}, nil
}

func TestNonDemoStrictHonestyNDJSONValidationAnd4xxNoFailover(t *testing.T) {
	rt := newConfigurableTestRuntime()
	var inferBehavior atomic.Value
	inferBehavior.Store("missing_live_bootstrap")

	rt.onExecute = func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
		mode := inferBehavior.Load().(string)
		if strings.Contains(req.Code, "ollama.com/install.sh") {
			if mode == "missing_live_bootstrap" {
				// Simulate non-demo VM where ollama did not emit COLABHIVE_LLM_LIVE:
				return &models.KernelExecResult{Status: "ok", Stdout: "ollama missing\n"}, nil
			}
			return &models.KernelExecResult{
				Status: "ok",
				Stdout: `COLABHIVE_LLM_LIVE:{"ok":true,"tag":"qwen3:8b"}` + "\n",
			}, nil
		}
		switch mode {
		case "ndjson_error_field":
			payload, _ := json.Marshal(map[string]any{
				"content": "partial",
				"error":   "CUDA out of memory",
				"done":    false,
			})
			return &models.KernelExecResult{
				Status: "ok",
				Stdout: "COLABHIVE_LLM_RESP:" + base64.StdEncoding.EncodeToString(payload) + "\n",
			}, nil
		case "ndjson_missing_done":
			payload, _ := json.Marshal(map[string]any{
				"content": "truncated without done",
				"chunks":  []string{"truncated"},
				"done":    false,
			})
			return &models.KernelExecResult{
				Status: "ok",
				Stdout: "COLABHIVE_LLM_RESP:" + base64.StdEncoding.EncodeToString(payload) + "\n",
			}, nil
		case "http_400_bad_prompt":
			return &models.KernelExecResult{
				Status: "error",
				Stderr: "RuntimeError: COLABHIVE_LLM_CLIENT_ERR:400:invalid options: temperature out of range",
			}, nil
		default:
			payload, _ := json.Marshal(map[string]any{
				"content":           "ok",
				"chunks":            []string{"ok"},
				"prompt_tokens":     5,
				"completion_tokens": 1,
				"done":              true,
			})
			return &models.KernelExecResult{
				Status: "ok",
				Stdout: "COLABHIVE_LLM_RESP:" + base64.StdEncoding.EncodeToString(payload) + "\n",
			}, nil
		}
	}

	svc := llmgw.NewService(nil, rt, nil, events.NewHub(), llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: false,
	})
	defer svc.Close()

	// 1. Even with demo:// proxy URL, DemoMode=false MUST fail when COLABHIVE_LLM_LIVE is missing!
	if _, err := svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"}); err == nil {
		t.Fatal("expected Start() with DemoMode=false to fail when COLABHIVE_LLM_LIVE is absent")
	}

	// 2. Start cleanly when COLABHIVE_LLM_LIVE is present
	inferBehavior.Store("ok")
	st, err := svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"})
	if err != nil || !st.Running {
		t.Fatalf("Start live mode: st=%+v err=%v", st, err)
	}
	createsBefore := rt.createCount.Load()

	// 3. Real Ollama NDJSON error field MUST fail (never return partial text as success)
	inferBehavior.Store("ndjson_error_field")
	if _, err := svc.Chat(context.Background(), llmgw.ChatInferRequest{
		Model:    "qwen3-8b",
		Messages: []llmgw.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil); err == nil || !strings.Contains(err.Error(), "CUDA out of memory") {
		t.Fatalf("expected error for NDJSON error field, got %v", err)
	}

	// 4. Real Ollama NDJSON missing done=true MUST fail
	inferBehavior.Store("ndjson_missing_done")
	if _, err := svc.Chat(context.Background(), llmgw.ChatInferRequest{
		Model:    "qwen3-8b",
		Messages: []llmgw.ChatMessage{{Role: "user", Content: "hi"}},
	}, nil); err == nil || !strings.Contains(err.Error(), "done=true") {
		t.Fatalf("expected error for missing done=true, got %v", err)
	}

	// 5. 4xx bad prompt MUST return HTTP 400, MUST NOT fail over, and MUST leave gateway healthy & running!
	createsBefore400 := rt.createCount.Load()
	failoversBefore400 := svc.Status().FailoverCount
	inferBehavior.Store("http_400_bad_prompt")
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	badResp, err := http.Post(
		srv.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"qwen3-8b","stream":false,"messages":[{"role":"user","content":"bad prompt"}]}`),
	)
	if err != nil {
		t.Fatalf("POST 400 test: %v", err)
	}
	_ = badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 for Ollama 4xx client error, got %d", badResp.StatusCode)
	}
	after400 := svc.Status()
	if !after400.Running || after400.FailoverCount != failoversBefore400 || rt.createCount.Load() != createsBefore400 {
		t.Fatalf("4xx bad prompt must NOT trigger failover or kill gateway: status=%+v creates=%d (was %d)",
			after400, rt.createCount.Load(), createsBefore400)
	}
	_ = createsBefore
}

func TestExplicitStopLifecycleGenPreventsFailoverResurrection(t *testing.T) {
	rt := newConfigurableTestRuntime()
	inFlightEntered := make(chan struct{})
	releaseInFlight := make(chan struct{})

	rt.onExecute = func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
		if req.CallerName == "llmgw-infer" {
			close(inFlightEntered)
			<-releaseInFlight
			return nil, errors.New("websocket connection reset: session stopped")
		}
		return &models.KernelExecResult{Status: "ok"}, nil
	}

	svc := llmgw.NewService(nil, rt, nil, events.NewHub(), llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: true,
	})
	defer svc.Close()

	if _, err := svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if rt.createCount.Load() != 1 {
		t.Fatalf("expected 1 session created, got %d", rt.createCount.Load())
	}

	chatErrCh := make(chan error, 1)
	go func() {
		_, err := svc.Chat(context.Background(), llmgw.ChatInferRequest{
			Model:    "qwen3-8b",
			Messages: []llmgw.ChatMessage{{Role: "user", Content: "slow query"}},
		}, nil)
		chatErrCh <- err
	}()

	<-inFlightEntered

	// Explicit Stop() bumps lifecycleGen and clears Running BEFORE stopping session.
	stopSnap, stopErr := svc.Stop(context.Background())
	if stopErr != nil || stopSnap.Running || stopSnap.State != models.LLMStateStopped {
		t.Fatalf("Stop failed: snap=%+v err=%v", stopSnap, stopErr)
	}

	// Unblock the in-flight inference failure -> MUST NOT resurrect gateway via failover!
	close(releaseInFlight)
	if err := <-chatErrCh; !errors.Is(err, llmgw.ErrGatewayStopped) {
		t.Fatalf("expected ErrGatewayStopped after explicit Stop(), got %v", err)
	}

	finalSt := svc.Status()
	if finalSt.Running || finalSt.State != models.LLMStateStopped || finalSt.FailoverCount != 0 || rt.createCount.Load() != 1 {
		t.Fatalf("FAILOVER RESURRECTION BUG: gateway=%+v creates=%d", finalSt, rt.createCount.Load())
	}
}

func TestFailedStopPreservesSessionIDForRetryTeardown(t *testing.T) {
	rt := newConfigurableTestRuntime()
	var failFirstStop atomic.Bool
	failFirstStop.Store(true)

	var stoppedIDs []string
	var stopMu sync.Mutex
	rt.onStop = func(_ context.Context, id string) error {
		stopMu.Lock()
		stoppedIDs = append(stoppedIDs, id)
		stopMu.Unlock()
		if failFirstStop.Load() {
			return errors.New("upstream Colab teardown HTTP 503")
		}
		return nil
	}

	svc := llmgw.NewService(nil, rt, nil, events.NewHub(), llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: true,
	})
	defer svc.Close()

	started, err := svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"})
	if err != nil || started.SessionID == "" {
		t.Fatalf("Start: %+v (%v)", started, err)
	}
	origSessID := started.SessionID

	// 1. First Stop() fails -> MUST keep SessionID recorded and set State = LLMStateError!
	stErr, err := svc.Stop(context.Background())
	if err == nil {
		t.Fatal("expected error when StopSession fails")
	}
	if stErr == nil || stErr.State != models.LLMStateError || stErr.SessionID != origSessID || stErr.Running {
		t.Fatalf("expected State=error and preserved SessionID=%q after failed Stop(), got %+v", origSessID, stErr)
	}

	// 2. Retry Stop() once upstream recovers -> MUST retry exact SessionID and clean up to LLMStateStopped!
	failFirstStop.Store(false)
	stOK, err := svc.Stop(context.Background())
	if err != nil {
		t.Fatalf("retry Stop() failed: %v", err)
	}
	if stOK.State != models.LLMStateStopped || stOK.SessionID != "" || stOK.Running {
		t.Fatalf("expected clean LLMStateStopped after retry Stop(), got %+v", stOK)
	}
	stopMu.Lock()
	defer stopMu.Unlock()
	if len(stoppedIDs) != 2 || stoppedIDs[0] != origSessID || stoppedIDs[1] != origSessID {
		t.Fatalf("expected StopSession retried twice with %q, got %v", origSessID, stoppedIDs)
	}
}

func TestWarmModelMismatchReturns409AndEchoesActualActiveModel(t *testing.T) {
	h := newTestHarness(t, 0)
	seedTestAccount(t, h.st, "acc_mm_1", "mm@example.com", "匹配测试", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})

	if _, err := h.svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// 1. Explicit different model MUST return 409 Conflict across OpenAI (stream & non-stream), Anthropic, and Ollama!
	for _, tc := range []struct {
		path string
		body string
	}{
		{"/v1/chat/completions", `{"model":"deepseek-r1-14b","stream":false,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/chat/completions", `{"model":"qwen3:32b","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"/v1/messages", `{"model":"deepseek-r1-14b","max_tokens":64,"stream":false,"messages":[{"role":"user","content":"hi"}]}`},
		{"/api/chat", `{"model":"deepseek-r1:14b","stream":false,"messages":[{"role":"user","content":"hi"}]}`},
		{"/api/generate", `{"model":"gemma3:12b","prompt":"hi","stream":false}`},
	} {
		resp, err := http.Post(h.srv.URL+tc.path, "application/json", strings.NewReader(tc.body))
		if err != nil {
			t.Fatalf("POST %s: %v", tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("expected 409 Conflict for warm model mismatch on %s, got %d", tc.path, resp.StatusCode)
		}
	}

	// 2. Proprietary client alias ("gpt-4o") succeeds and echoes actual served ActiveModelID ("qwen3-8b")!
	aliasResp, err := http.Post(
		h.srv.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"gpt-4o","stream":false,"messages":[{"role":"user","content":"echo check"}]}`),
	)
	if err != nil {
		t.Fatalf("POST alias: %v", err)
	}
	var out struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(aliasResp.Body).Decode(&out)
	_ = aliasResp.Body.Close()
	if aliasResp.StatusCode != http.StatusOK || out.Model != "qwen3-8b" {
		t.Fatalf("expected 200 and echoed active model qwen3-8b, got status=%d model=%q", aliasResp.StatusCode, out.Model)
	}
}

func TestSSEImmediateKeepaliveFlushAndTerminalErrorFraming(t *testing.T) {
	rt := newConfigurableTestRuntime()
	inferStarted := make(chan struct{}, 4)
	releaseInfer := make(chan struct{})
	var failInfer atomic.Bool

	rt.onExecute = func(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
		if req.CallerName == "llmgw-infer" {
			select {
			case inferStarted <- struct{}{}:
			default:
			}
			select {
			case <-releaseInfer:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if failInfer.Load() {
				return nil, errors.New("bad request: prompt exceeds model context window")
			}
		}
		return &models.KernelExecResult{Status: "ok"}, nil
	}

	svc := llmgw.NewService(nil, rt, nil, events.NewHub(), llmgw.Config{
		BaseURL:              "http://127.0.0.1:7860",
		DemoMode:             true,
		SSEKeepaliveInterval: 25 * time.Millisecond,
	})
	defer svc.Close()

	if _, err := svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 1. Verify headers + `: keepalive\n\n` are flushed IMMEDIATELY BEFORE inference unblocks!
	failInfer.Store(true)
	resp, err := http.Post(
		srv.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"qwen3-8b","stream":true,"messages":[{"role":"user","content":"slow failing stream"}]}`),
	)
	if err != nil {
		t.Fatalf("POST slow SSE: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("expected immediate 200 text/event-stream headers, got %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// Read first line BEFORE releasing inference — proves `: keepalive` flushed immediately!
	br := bufio.NewReader(resp.Body)
	firstLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("failed to read immediate keepalive line before inference completed: %v", err)
	}
	if strings.TrimSpace(firstLine) != ": keepalive" {
		t.Fatalf("expected immediate ': keepalive' flush, got %q", firstLine)
	}

	// Wait briefly so at least one periodic heartbeat ticks, then unblock with an inference error!
	<-inferStarted
	time.Sleep(45 * time.Millisecond)
	close(releaseInfer)

	restBytes, _ := io.ReadAll(br)
	fullBody := firstLine + string(restBytes)
	if !strings.Contains(fullBody, "event: error\n") || !strings.Contains(fullBody, `"error"`) {
		t.Fatalf("expected terminal SSE 'event: error' JSON frame on post-header error, got:\n%s", fullBody)
	}
	if strings.Contains(fullBody, "data: [DONE]") {
		t.Fatalf("errored OpenAI SSE stream must NOT emit data: [DONE]:\n%s", fullBody)
	}

	// 2. Verify Anthropic SSE also emits `event: error` JSON frame when inference fails after header flush!
	anthResp, err := http.Post(
		srv.URL+"/v1/messages",
		"application/json",
		strings.NewReader(`{"model":"qwen3-8b","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"fail anthropic"}]}`),
	)
	if err != nil {
		t.Fatalf("POST Anthropic failing SSE: %v", err)
	}
	anthRaw, _ := io.ReadAll(anthResp.Body)
	_ = anthResp.Body.Close()
	if anthResp.StatusCode != http.StatusOK ||
		!strings.Contains(string(anthRaw), ": keepalive") ||
		!strings.Contains(string(anthRaw), "event: error\n") ||
		!strings.Contains(string(anthRaw), `"type":"error"`) {
		t.Fatalf("expected Anthropic SSE keepalive + event: error frame, got status=%d body:\n%s", anthResp.StatusCode, string(anthRaw))
	}
}

func TestOllamaUnloadReturns502OnStopFailure(t *testing.T) {
	rt := newConfigurableTestRuntime()
	var failStop atomic.Bool
	failStop.Store(true)
	rt.onStop = func(_ context.Context, _ string) error {
		if failStop.Load() {
			return errors.New("upstream Colab unassign HTTP 503")
		}
		return nil
	}

	svc := llmgw.NewService(nil, rt, nil, events.NewHub(), llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: true,
	})
	defer svc.Close()

	started, err := svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"})
	if err != nil || started.SessionID == "" {
		t.Fatalf("Start: %+v (%v)", started, err)
	}
	origSessID := started.SessionID

	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Both Ollama `/api/chat` and `/api/generate` with keep_alive=0 MUST return 502 Bad Gateway
	// with JSON {"error": ...} when Stop fails — NEVER falsely confirm unload with 200 OK!
	for _, tc := range []struct {
		path string
		body string
	}{
		{"/api/chat", `{"model":"qwen3:8b","messages":[],"keep_alive":0}`},
		{"/api/generate", `{"model":"qwen3:8b","prompt":"","keep_alive":0}`},
	} {
		resp, reqErr := http.Post(srv.URL+tc.path, "application/json", strings.NewReader(tc.body))
		if reqErr != nil {
			t.Fatalf("POST %s unload: %v", tc.path, reqErr)
		}
		var payload map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&payload)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("P0 FAILURE: expected 502 Bad Gateway on failed Stop via %s, got %d (%+v)", tc.path, resp.StatusCode, payload)
		}
		errStr, _ := payload["error"].(string)
		if !strings.Contains(errStr, "503") {
			t.Fatalf("expected error JSON with upstream 503 detail on %s, got %+v", tc.path, payload)
		}
		cur := svc.Status()
		if cur.State != models.LLMStateError || cur.SessionID != origSessID || cur.Running {
			t.Fatalf("expected retained SessionID=%q & State=error after failed %s unload, got %+v", origSessID, tc.path, cur)
		}
	}

	// Once upstream unassign recovers, retrying keep_alive=0 returns 200 OK and clears SessionID!
	failStop.Store(false)
	okResp, err := http.Post(srv.URL+"/api/generate", "application/json", strings.NewReader(`{"model":"qwen3:8b","prompt":"","keep_alive":0}`))
	if err != nil {
		t.Fatalf("retry POST /api/generate unload: %v", err)
	}
	_ = okResp.Body.Close()
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on recovered unload retry, got %d", okResp.StatusCode)
	}
	finalSt := svc.Status()
	if finalSt.State != models.LLMStateStopped || finalSt.SessionID != "" || finalSt.Running {
		t.Fatalf("expected clean stopped state after unload retry, got %+v", finalSt)
	}
}

func TestFailedBootstrapRetainsSessionIDWhenRollbackStopFails(t *testing.T) {
	rt := newConfigurableTestRuntime()
	rt.onExecute = func(_ context.Context, _ models.KernelExecRequest) (*models.KernelExecResult, error) {
		return &models.KernelExecResult{
			Status: "error",
			Stderr: "ollama pull failed: connection reset by peer",
		}, nil
	}

	var failStop atomic.Bool
	failStop.Store(true)
	var stopCalls []string
	var stopMu sync.Mutex
	rt.onStop = func(_ context.Context, id string) error {
		stopMu.Lock()
		stopCalls = append(stopCalls, id)
		stopMu.Unlock()
		if failStop.Load() {
			return errors.New("colab unassign timeout 504")
		}
		return nil
	}

	svc := llmgw.NewService(nil, rt, nil, events.NewHub(), llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: false,
	})
	defer svc.Close()

	// 1. Bootstrap fails AND rollback StopSession fails -> MUST retain SessionID so user can retry Stop!
	_, startErr := svc.Start(context.Background(), models.LLMStartRequest{ModelID: "qwen3-8b"})
	if startErr == nil {
		t.Fatal("expected Start() error when remote Ollama bootstrap fails")
	}
	if !strings.Contains(strings.ToLower(startErr.Error()), "retry stop") || !strings.Contains(startErr.Error(), "504") {
		t.Fatalf("expected error message explaining rollback teardown failure & Stop retry, got %q", startErr.Error())
	}

	st := svc.Status()
	if st.Running || st.State != models.LLMStateError || st.SessionID != "sess_cfg_1" {
		t.Fatalf("P0 FAILURE: failed bootstrap rollback must retain SessionID=sess_cfg_1 and set State=error, got %+v", st)
	}

	// 2. Retry Stop() once upstream unassign recovers -> tears down exact retained SessionID!
	failStop.Store(false)
	stopped, stopErr := svc.Stop(context.Background())
	if stopErr != nil {
		t.Fatalf("retry Stop() after failed bootstrap rollback: %v", stopErr)
	}
	if stopped.Running || stopped.State != models.LLMStateStopped || stopped.SessionID != "" {
		t.Fatalf("expected clean stopped status after retry Stop(), got %+v", stopped)
	}

	stopMu.Lock()
	defer stopMu.Unlock()
	if len(stopCalls) != 2 || stopCalls[0] != "sess_cfg_1" || stopCalls[1] != "sess_cfg_1" {
		t.Fatalf("expected StopSession called twice on sess_cfg_1 (rollback + retry Stop), got %v", stopCalls)
	}
}

func TestTrueIncrementalStreamingOverJupyterWSAndPrivateReplyIsolation(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer st.Close()

	hub := events.NewHub()
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	evCh, unsub := hub.Subscribe(subCtx)
	defer unsub()

	var sessionOutputLeaks atomic.Int32
	go func() {
		for ev := range evCh {
			if ev.Type == models.WSEventSessionOutput {
				sessionOutputLeaks.Add(1)
			}
		}
	}()

	var (
		inferModeMu   sync.Mutex
		inferMode     = "incremental"
		releaseToken2 = make(chan struct{})
	)
	setMode := func(m string) {
		inferModeMu.Lock()
		inferMode = m
		inferModeMu.Unlock()
	}
	getMode := func() string {
		inferModeMu.Lock()
		defer inferModeMu.Unlock()
		return inferMode
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var jupSrv *httptest.Server
	jupMux := http.NewServeMux()
	jupMux.HandleFunc("/tun/m/assign", func(w http.ResponseWriter, r *http.Request) {
		acc := r.URL.Query().Get("accelerator")
		nbh := r.URL.Query().Get("nbh")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + fmt.Sprintf(`{"acc":%q,"nbh":%q,"token":"xsrf-tok","variant":"GPU"}`, acc, nbh)))
			return
		}
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + fmt.Sprintf(
			`{"accelerator":%q,"endpoint":"gpu-t4-s-stream01","variant":1,"runtimeProxyInfo":{"token":"proxy-tok","tokenExpiresInSeconds":3600,"url":%q}}`,
			acc, jupSrv.URL,
		)))
	})
	jupMux.HandleFunc("/tun/m/unassign/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{"token":"xsrf-un"}`))
			return
		}
		_, _ = w.Write([]byte(models.ColabXSSIPrefix + `{}`))
	})
	jupMux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"jup-s-1","kernel":{"id":"kern-stream-1"}}`))
	})
	jupMux.HandleFunc("/api/colab/resources", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"memory":{"totalBytes":16000000000,"freeBytes":8000000000},"disks":[],"gpus":[]}`))
	})
	jupMux.HandleFunc("/api/kernels/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/channels") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		conn, uErr := upgrader.Upgrade(w, r, nil)
		if uErr != nil {
			return
		}
		defer conn.Close()

		for {
			var wire struct {
				Header  map[string]any `json:"header"`
				Content map[string]any `json:"content"`
			}
			if rErr := conn.ReadJSON(&wire); rErr != nil {
				return
			}
			parentID, _ := wire.Header["msg_id"].(string)
			msgType, _ := wire.Header["msg_type"].(string)
			code, _ := wire.Content["code"].(string)

			sendFrame := func(channel, msgType string, content map[string]any) {
				_ = conn.WriteJSON(map[string]any{
					"channel":       channel,
					"header":        map[string]any{"msg_id": "m_" + msgType, "msg_type": msgType, "version": "5.3"},
					"parent_header": map[string]any{"msg_id": parentID},
					"metadata":      map[string]any{},
					"content":       content,
				})
			}
			if msgType == "kernel_info_request" {
				sendFrame("shell", "kernel_info_reply", map[string]any{"status": "ok"})
				continue
			}

			switch {
			case strings.Contains(code, "ollama.com/install.sh"):
				sendFrame("iopub", "stream", map[string]any{
					"name": "stdout",
					"text": `COLABHIVE_LLM_LIVE:{"ok":true,"tag":"qwen3:8b"}` + "\n",
				})
			case strings.Contains(code, "127.0.0.1:11434/api/chat"):
				if !strings.Contains(code, "sys.stdout.flush()") {
					sendFrame("iopub", "stream", map[string]any{
						"name": "stderr",
						"text": "bridge script missing incremental sys.stdout.flush()",
					})
					sendFrame("shell", "execute_reply", map[string]any{"status": "error", "ename": "AssertionError", "evalue": "missing flush"})
					sendFrame("iopub", "status", map[string]any{"execution_state": "idle"})
					continue
				}
				switch getMode() {
				case "incremental":
					// Frame 1 arrives immediately; split across two WS stream frames to verify line buffering!
					sendFrame("iopub", "stream", map[string]any{
						"name": "stdout",
						"text": `{"message":{"role":"assistant","content":"SECRET_FIRST_`,
					})
					sendFrame("iopub", "stream", map[string]any{
						"name": "stdout",
						"text": "TOKEN_\"},\"done\":false}\n",
					})
					// Block until the HTTP SSE consumer proves it ALREADY received token 1!
					select {
					case <-releaseToken2:
					case <-time.After(5 * time.Second):
					}
					sendFrame("iopub", "stream", map[string]any{
						"name": "stdout",
						"text": "{\"message\":{\"role\":\"assistant\",\"content\":\"SECOND_TOKEN\"},\"done\":false}\n",
					})
					sendFrame("iopub", "stream", map[string]any{
						"name": "stdout",
						"text": "{\"message\":{\"role\":\"assistant\",\"content\":\"\"},\"done\":true,\"prompt_eval_count\":11,\"eval_count\":22}\n",
					})

				case "long_over_512k":
					// Stream 160 chunks × 4 KiB = 640 KiB (>512 KiB single-frame cap!)
					piece := strings.Repeat("A", 4096)
					for i := 0; i < 160; i++ {
						line, _ := json.Marshal(map[string]any{
							"message": map[string]string{"role": "assistant", "content": piece},
							"done":    false,
						})
						sendFrame("iopub", "stream", map[string]any{
							"name": "stdout",
							"text": string(line) + "\n",
						})
					}
					sendFrame("iopub", "stream", map[string]any{
						"name": "stdout",
						"text": "{\"message\":{\"role\":\"assistant\",\"content\":\"\"},\"done\":true,\"prompt_eval_count\":10,\"eval_count\":640}\n",
					})

				case "stream_error":
					sendFrame("iopub", "stream", map[string]any{
						"name": "stdout",
						"text": "{\"error\":\"VRAM exhausted mid-stream\"}\n",
					})

				case "missing_done":
					sendFrame("iopub", "stream", map[string]any{
						"name": "stdout",
						"text": "{\"message\":{\"role\":\"assistant\",\"content\":\"cut short\"},\"done\":false}\n",
					})
				}
			}

			sendFrame("shell", "execute_reply", map[string]any{"status": "ok", "execution_count": 1})
			sendFrame("iopub", "status", map[string]any{"execution_state": "idle"})
		}
	})
	jupSrv = httptest.NewServer(jupMux)
	defer jupSrv.Close()

	seedTestAccount(t, st, "acc_stream_1", "stream-test@example.com", "流式测试号", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})
	rtrSvc := router.NewServiceWithDirs(st, nil, hub, t.TempDir(), t.TempDir(), t.TempDir(), "windows")
	rtEng := runtime.NewConcreteEngine(st, nil, rtrSvc, hub, runtime.Options{
		DemoMode:               false,
		TunnelBaseURL:          jupSrv.URL,
		PublicAPIBaseURL:       jupSrv.URL,
		DisableBackgroundLoops: true,
		ArtifactsDir:           t.TempDir(),
	})
	defer rtEng.Close()

	svc := llmgw.NewService(nil, rtEng, rtrSvc, hub, llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: false,
	})
	defer svc.Close()

	if _, err := svc.Start(ctx, models.LLMStartRequest{ModelID: "qwen3-8b"}); err != nil {
		t.Fatalf("Start live streaming session: %v", err)
	}

	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	gwSrv := httptest.NewServer(mux)
	defer gwSrv.Close()

	// 1. TRUE incremental streaming proof: token 1 MUST arrive over HTTP SSE BEFORE releaseToken2 is closed!
	sseResp, err := http.Post(
		gwSrv.URL+"/v1/chat/completions",
		"application/json",
		strings.NewReader(`{"model":"qwen3-8b","stream":true,"messages":[{"role":"user","content":"private secret question"}]}`),
	)
	if err != nil {
		t.Fatalf("POST incremental SSE: %v", err)
	}
	defer sseResp.Body.Close()

	br := bufio.NewReader(sseResp.Body)
	sawFirstBeforeRelease := false
	for !sawFirstBeforeRelease {
		line, rErr := br.ReadString('\n')
		if rErr != nil {
			t.Fatalf("stream ended before receiving first incremental token: %v", rErr)
		}
		if strings.Contains(line, "SECRET_FIRST_TOKEN_") {
			sawFirstBeforeRelease = true
		}
	}
	// Release token 2 only AFTER proving token 1 was already flushed incrementally to the HTTP client!
	close(releaseToken2)

	restBytes, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("ReadAll rest of SSE: %v", err)
	}
	restStr := string(restBytes)
	if !strings.Contains(restStr, "SECOND_TOKEN") || !strings.Contains(restStr, "data: [DONE]") {
		t.Fatalf("expected SECOND_TOKEN and [DONE] after release, got:\n%s", restStr)
	}

	// 2. Arbitrarily long reply (>640 KiB, exceeding 512 KiB single-frame stdout cap) MUST succeed completely!
	setMode("long_over_512k")
	var streamedBuf strings.Builder
	longResp, err := svc.Chat(ctx, llmgw.ChatInferRequest{
		Model:    "qwen3-8b",
		Messages: []llmgw.ChatMessage{{Role: "user", Content: "write a huge document"}},
	}, func(piece string) error {
		streamedBuf.WriteString(piece)
		return nil
	})
	if err != nil {
		t.Fatalf("expected >512 KiB streamed reply to succeed without cap failure, got: %v", err)
	}
	const wantLen = 160 * 4096
	if len(longResp.Content) != wantLen || streamedBuf.Len() != wantLen {
		t.Fatalf("expected full %d bytes streamed & accumulated, got resp=%d streamed=%d", wantLen, len(longResp.Content), streamedBuf.Len())
	}

	// 3. Real Ollama NDJSON error frame & missing done=true MUST fail cleanly!
	setMode("stream_error")
	if _, err := svc.Chat(ctx, llmgw.ChatInferRequest{
		Model:    "qwen3-8b",
		Messages: []llmgw.ChatMessage{{Role: "user", Content: "trigger error"}},
	}, nil); err == nil || !strings.Contains(err.Error(), "VRAM exhausted mid-stream") {
		t.Fatalf("expected NDJSON error frame failure, got %v", err)
	}

	setMode("missing_done")
	if _, err := svc.Chat(ctx, llmgw.ChatInferRequest{
		Model:    "qwen3-8b",
		Messages: []llmgw.ChatMessage{{Role: "user", Content: "trigger truncation"}},
	}, nil); err == nil || !strings.Contains(err.Error(), "done=true") {
		t.Fatalf("expected missing done=true error, got %v", err)
	}

	// 4. Private reply isolation: ZERO WSEventSessionOutput events may be broadcast over /ws/events!
	time.Sleep(30 * time.Millisecond)
	if leaks := sessionOutputLeaks.Load(); leaks != 0 {
		t.Fatalf("PRIVACY FAILURE: expected 0 WSEventSessionOutput events for private AI chat, got %d", leaks)
	}
}

func TestExplicitAccountIDPinningInLLMStart(t *testing.T) {
	h := newTestHarness(t, 0)
	seedTestAccount(t, h.st, "acc_pin_a", "pin-a@example.com", "节点A", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})
	seedTestAccount(t, h.st, "acc_pin_b", "pin-b@example.com", "节点B", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})

	// Also seed a legacy non-Colab account row to verify defensive rejection.
	now := time.Now().UTC()
	if err := h.st.SaveAccount(context.Background(), &models.Account{
		ID:               "acc_pin_legacy_ext",
		Email:            "pin@legacy.ext",
		Alias:            "Legacy External",
		Provider:         models.ProviderType("legacy_external"),
		SubscriptionTier: models.TierFree,
		Health:           models.HealthHealthy,
		Enabled:          true,
		MaxConcurrentVMs: 2,
		CreatedAt:        now,
		UpdatedAt:        now,
	}); err != nil {
		t.Fatalf("SaveAccount(legacy_external): %v", err)
	}

	// 1. Explicitly pin acc_pin_b -> gateway MUST launch on acc_pin_b!
	resB, err := http.Post(h.srv.URL+"/api/v1/llm/start", "application/json", strings.NewReader(`{"model_id":"qwen3-8b","account_id":"acc_pin_b"}`))
	if err != nil {
		t.Fatalf("POST start pinned B: %v", err)
	}
	var stB models.LLMGatewayStatus
	_ = json.NewDecoder(resB.Body).Decode(&stB)
	_ = resB.Body.Close()
	if resB.StatusCode != http.StatusOK || stB.AccountID != "acc_pin_b" {
		t.Fatalf("expected pinned start on acc_pin_b, got status=%d statusObj=%+v", resB.StatusCode, stB)
	}

	// 2. Re-start same model pinned to acc_pin_a -> MUST switch from acc_pin_b to acc_pin_a (not hit wrong fast-path)!
	resA, err := http.Post(h.srv.URL+"/api/v1/llm/start", "application/json", strings.NewReader(`{"model_id":"qwen3-8b","account_id":"acc_pin_a"}`))
	if err != nil {
		t.Fatalf("POST start pinned A: %v", err)
	}
	var stA models.LLMGatewayStatus
	_ = json.NewDecoder(resA.Body).Decode(&stA)
	_ = resA.Body.Close()
	if resA.StatusCode != http.StatusOK || stA.AccountID != "acc_pin_a" {
		t.Fatalf("expected pinned switch to acc_pin_a, got status=%d statusObj=%+v", resA.StatusCode, stA)
	}

	// 3. Pinning a legacy non-Colab account MUST fail with HTTP 400 Bad Request!
	resK, err := http.Post(h.srv.URL+"/api/v1/llm/start", "application/json", strings.NewReader(`{"model_id":"qwen3-8b","account_id":"acc_pin_legacy_ext"}`))
	if err != nil {
		t.Fatalf("POST start pinned legacy_external: %v", err)
	}
	rawK, _ := io.ReadAll(resK.Body)
	_ = resK.Body.Close()
	if resK.StatusCode != http.StatusBadRequest || !strings.Contains(strings.ToLower(string(rawK)), "not supported for interactive colab sessions") {
		t.Fatalf("expected 400 Bad Request for non-Colab account_id, got status=%d body=%s", resK.StatusCode, string(rawK))
	}
}

func TestAccountNotFoundNeverClearsSessionAndExplicitT4DisablesFallback(t *testing.T) {
	rt := newConfigurableTestRuntime()
	rt.onStop = func(_ context.Context, _ string) error {
		return errors.New("failed to acquire credentials to unassign m-s-abc: runtime: account not found or deleted")
	}

	svc := llmgw.NewService(nil, rt, nil, events.NewHub(), llmgw.Config{
		BaseURL:  "http://127.0.0.1:7860",
		DemoMode: true,
	})
	defer svc.Close()

	// 1. Explicit T4 MUST disable AutoFallback so it never silently escalates to paid GPUs!
	if _, err := svc.Start(context.Background(), models.LLMStartRequest{
		ModelID:      "qwen3-8b",
		PreferredGPU: models.AcceleratorT4,
	}); err != nil {
		t.Fatalf("Start explicit T4: %v", err)
	}
	rt.mu.Lock()
	gotAlloc := rt.lastAllocReq
	rt.mu.Unlock()
	if gotAlloc.AutoFallback || len(gotAlloc.FallbackChain) != 0 {
		t.Fatalf("expected explicit T4 to set AutoFallback=false & empty FallbackChain, got %+v", gotAlloc)
	}

	// 2. Account-not-found error during Stop MUST NOT clear SessionID or return nil error!
	st, stopErr := svc.Stop(context.Background())
	if stopErr == nil {
		t.Fatalf("expected Stop() to fail on account-not-found error, got nil")
	}
	if st == nil || st.SessionID == "" || st.State != models.LLMStateError {
		t.Fatalf("expected SessionID retained in LLMStateError when account lookup fails during Stop, got %+v", st)
	}
}

func TestCatalogHotReloadFromJSONFile(t *testing.T) {
	dir := t.TempDir()
	catPath := dir + "/llm_catalog.json"
	customJSON := `{"models":[{
		"id":"qwen4-next-heretic",
		"display_name":"Qwen4 Next Heretic · Hot-Reloaded",
		"display_name_zh":"Qwen4 Next Heretic · 热更新模型",
		"provider_family":"Qwen",
		"parameter_size":"14B",
		"quantization":"Q4_K_M",
		"min_vram_gb":12,
		"recommended_gpu":"T4",
		"free_tier_ready":true,
		"ollama_tag":"hf.co/huihui-ai/Qwen4-Next-14B-Heretic-GGUF:Q4_K_M",
		"huggingface_repo":"huihui-ai/Qwen4-Next-14B-Heretic-GGUF"
	}]}`
	if err := os.WriteFile(catPath, []byte(customJSON), 0o600); err != nil {
		t.Fatalf("write llm_catalog.json: %v", err)
	}
	svc := llmgw.NewService(nil, newConfigurableTestRuntime(), nil, events.NewHub(), llmgw.Config{
		BaseURL:                "http://127.0.0.1:7860",
		DataDir:                dir,
		DisableUserHomeCatalog: true,
		DemoMode:               true,
	})
	defer svc.Close()

	got := svc.Catalog()
	if len(got) != 1 || got[0].ID != "qwen4-next-heretic" || got[0].OllamaTag != "hf.co/huihui-ai/Qwen4-Next-14B-Heretic-GGUF:Q4_K_M" {
		t.Fatalf("expected hot-reloaded JSON catalog, got %+v", got)
	}
	resolved, err := svc.ResolveModel("qwen4-next-heretic")
	if err != nil || resolved.ID != "qwen4-next-heretic" {
		t.Fatalf("expected ResolveModel to find hot-reloaded entry, got %+v err=%v", resolved, err)
	}

	// Corrupt JSON must safely fall back to built-in 8-model curated catalog!
	if err := os.WriteFile(catPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("corrupt llm_catalog.json: %v", err)
	}
	fallback := svc.Catalog()
	if len(fallback) != 8 || fallback[0].ID != llmgw.DefaultModelID {
		t.Fatalf("expected safe fallback to built-in catalog on invalid JSON, got %+v", fallback)
	}
}

func TestOpenAIResponsesEndpointNonStreamAndStream(t *testing.T) {
	h := newTestHarness(t, 0)
	seedTestAccount(t, h.st, "acc_resp_1", "resp@example.com", "RespNode", models.TierFree, []models.AcceleratorType{models.AcceleratorT4})

	// 1. Non-streaming with string input + instructions
	res1, err := http.Post(
		h.srv.URL+"/v1/responses",
		"application/json",
		strings.NewReader(`{"model":"`+llmgw.DefaultModelID+`","instructions":"Be concise","input":"Say hello"}`),
	)
	if err != nil {
		t.Fatalf("POST /v1/responses non-stream: %v", err)
	}
	raw1, _ := io.ReadAll(res1.Body)
	_ = res1.Body.Close()
	if res1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on non-stream /v1/responses, got %d: %s", res1.StatusCode, string(raw1))
	}
	var parsed1 struct {
		ID         string `json:"id"`
		Object     string `json:"object"`
		Status     string `json:"status"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw1, &parsed1); err != nil {
		t.Fatalf("decode non-stream /v1/responses: %v (%s)", err, string(raw1))
	}
	if parsed1.Object != "response" || parsed1.Status != "completed" || strings.TrimSpace(parsed1.OutputText) == "" {
		t.Fatalf("unexpected non-stream /v1/responses payload: %+v", parsed1)
	}
	if len(parsed1.Output) == 0 || len(parsed1.Output[0].Content) == 0 || parsed1.Output[0].Content[0].Type != "output_text" {
		t.Fatalf("missing output_text item in /v1/responses: %+v", parsed1)
	}

	// 2. Streaming SSE with structured message array input (`input_text`)
	res2, err := http.Post(
		h.srv.URL+"/v1/responses",
		"application/json",
		strings.NewReader(`{"model":"`+llmgw.DefaultModelID+`","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"Stream test"}]}]}`),
	)
	if err != nil {
		t.Fatalf("POST /v1/responses stream: %v", err)
	}
	raw2, _ := io.ReadAll(res2.Body)
	_ = res2.Body.Close()
	body2 := string(raw2)
	if res2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on stream /v1/responses, got %d: %s", res2.StatusCode, body2)
	}
	for _, wantEvt := range []string{
		"event: response.created",
		"event: response.output_text.delta",
		"event: response.output_text.done",
		"event: response.completed",
	} {
		if !strings.Contains(body2, wantEvt) {
			t.Fatalf("expected SSE event %q in /v1/responses stream, got:\n%s", wantEvt, body2)
		}
	}

	// 3. Empty input returns 400 invalid_request_error
	res3, err := http.Post(
		h.srv.URL+"/v1/responses",
		"application/json",
		strings.NewReader(`{"model":"`+llmgw.DefaultModelID+`","input":""}`),
	)
	if err != nil {
		t.Fatalf("POST /v1/responses empty: %v", err)
	}
	_ = res3.Body.Close()
	if res3.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on empty input, got %d", res3.StatusCode)
	}

	// 4. Reasoning model over /v1/responses MUST emit full OpenAI Responses / Vercel AI SDK lifecycle:
	//    output_item.added(reasoning) -> reasoning_summary_part.added -> reasoning_summary_text.delta ->
	//    reasoning_summary_text.done -> reasoning_summary_part.done -> output_item.done(reasoning) ->
	//    output_item.added(message, output_index=1) -> content_part.added -> output_text.delta!
	if _, err := h.svc.Start(context.Background(), models.LLMStartRequest{ModelID: "deepseek-r1:14b"}); err != nil {
		t.Fatalf("switch to reasoning model: %v", err)
	}
	res4, err := http.Post(
		h.srv.URL+"/v1/responses",
		"application/json",
		strings.NewReader(`{"model":"deepseek-r1:14b","stream":true,"input":"你好"}`),
	)
	if err != nil {
		t.Fatalf("POST /v1/responses reasoning stream: %v", err)
	}
	raw4, _ := io.ReadAll(res4.Body)
	_ = res4.Body.Close()
	body4 := string(raw4)
	if res4.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on reasoning /v1/responses, got %d: %s", res4.StatusCode, body4)
	}
	wantOrdered := []string{
		"event: response.created",
		`"type":"reasoning"`,
		"event: response.reasoning_summary_part.added",
		"event: response.reasoning_summary_text.delta",
		"event: response.reasoning_summary_text.done",
		"event: response.reasoning_summary_part.done",
		`"type":"message"`,
		"event: response.content_part.added",
		"event: response.output_text.delta",
		"event: response.completed",
	}
	prevPos := -1
	for _, token := range wantOrdered {
		pos := strings.Index(body4, token)
		if pos < 0 {
			t.Fatalf("missing required Responses reasoning lifecycle token %q in stream:\n%s", token, body4)
		}
		if pos <= prevPos {
			t.Fatalf("out-of-order Responses reasoning lifecycle token %q (pos=%d <= prev=%d):\n%s", token, pos, prevPos, body4)
		}
		prevPos = pos
	}
}
