// Package mcp implements ColabHive's unified Model Context Protocol (2025-11-25, 2025-06-18,
// 2025-03-26 & 2024-11-05) JSON-RPC 2.0 server supporting concurrent `stdio`, Streamable HTTP
// (`POST /mcp`, `GET /mcp` SSE, `DELETE /mcp`), and dual-stack Legacy HTTP+SSE (`GET /mcp` /
// `GET /mcp/sse` + async `POST /mcp/messages`) with `Mcp-Session-Id` sticky kernel routing.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/colabhive/colabhive/internal/models"
)

const (
	// ProtocolVersion20251125 is accepted for modern 2025-11-25 MCP clients.
	ProtocolVersion20251125 = "2025-11-25"
	// ProtocolVersion20250618 is accepted for MCP clients supporting structuredContent.
	ProtocolVersion20250618 = "2025-06-18"
	// ProtocolVersion20250326 is the primary Streamable HTTP MCP specification version.
	ProtocolVersion20250326 = "2025-03-26"
	// ProtocolVersion20241105 is accepted for backward compatibility with legacy HTTP+SSE clients.
	ProtocolVersion20241105 = "2024-11-05"

	maxMCPPayloadBytes = 64 << 20 // 64 MiB max JSON-RPC line / HTTP payload
	mcpSessionIdleTTL  = 2 * time.Hour
	maxMCPSessions     = 8192
)

// ToolDefinition describes one statically registered MCP tool in `tools/list`.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ContentBlock represents an item inside CallToolResult.Content (`text` or `image`).
type ContentBlock struct {
	Type     string `json:"type"`               // "text" | "image"
	Text     string `json:"text,omitempty"`     // populated when Type == "text"
	Data     string `json:"data,omitempty"`     // base64 image bytes when Type == "image"
	MimeType string `json:"mimeType,omitempty"` // e.g. "image/png" when Type == "image"
}

// CallToolResult is the canonical MCP `tools/call` result envelope.
type CallToolResult struct {
	Content           []ContentBlock `json:"content"`
	StructuredContent any            `json:"structuredContent,omitempty"`
	IsError           bool           `json:"isError,omitempty"`
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type callContext struct {
	stickyKey       string
	clientName      string
	transport       models.TransportType
	apiKey          *models.AgentAPIKey
	directLoopback  bool
	defaultStrategy models.RoutingStrategy
	defaultAccount  string
}

func (c callContext) apiKeyID() string {
	if c.apiKey != nil {
		return c.apiKey.ID
	}
	return ""
}

func (c callContext) allowedGroups() []string {
	if c.apiKey != nil && len(c.apiKey.AllowedGroups) > 0 {
		return append([]string(nil), c.apiKey.AllowedGroups...)
	}
	return nil
}

func (c callContext) effectiveStrategy(argVal string) models.RoutingStrategy {
	return models.RoutingStrategy(firstStr(argVal, string(c.defaultStrategy)))
}

func (c callContext) effectiveAccount(argVal string) string {
	return firstStr(argVal, c.defaultAccount)
}

type mcpClientMeta struct {
	clientName   string
	apiKeyID     string
	createdAt    time.Time
	lastActiveAt time.Time
}

type sseSessionStream struct {
	ctx            context.Context
	cancel         context.CancelFunc
	apiKeyID       string
	directLoopback bool
	msgCh          chan []byte
	done           chan struct{}
}

// Server exposes ColabHive's statically registered MCP Suites A, B, and C over stdio and Streamable HTTP.
type Server struct {
	pool    models.AccountPoolService
	runtime models.RuntimeEngine
	router  models.RouterService
	hub     models.EventHub

	tools   []ToolDefinition
	toolMap map[string]toolHandlerFunc

	mu                    sync.RWMutex
	adminToken            string
	cloudMode             bool
	trustedWorkspaceRaw   string
	trustedWorkspaceCanon string
	sessions              map[string]mcpClientMeta
	sseChannels           map[string]*sseSessionStream

	inFlightMu      sync.Mutex
	inFlightCancels map[string]context.CancelFunc

	defaultStdioStrategy models.RoutingStrategy
	defaultStdioAccount  string
	defaultStdioAPIKey   string
}

type toolHandlerFunc func(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error)

type traceSummary struct {
	sessionID    string
	accountID    string
	accountEmail string
	reqGPU       models.AcceleratorType
	assignedGPU  models.AcceleratorType
	fallback     string
	cuDelta      float64
}

// NewServer creates an MCP server with all Official Colab MCP (Suite A), Colab CLI (Suite B),
// and Pool/Sharding (Suite C) tools statically registered at startup.
func NewServer(
	pool models.AccountPoolService,
	runtime models.RuntimeEngine,
	router models.RouterService,
	hub models.EventHub,
) *Server {
	s := &Server{
		pool:            pool,
		runtime:         runtime,
		router:          router,
		hub:             hub,
		toolMap:         make(map[string]toolHandlerFunc),
		sessions:        make(map[string]mcpClientMeta),
		sseChannels:     make(map[string]*sseSessionStream),
		inFlightCancels: make(map[string]context.CancelFunc),
	}
	s.registerAllTools()
	return s
}

// SetAdminToken configures an optional admin bearer token accepted alongside `ch_sk_...` keys.
func (s *Server) SetAdminToken(token string) {
	s.mu.Lock()
	s.adminToken = strings.TrimSpace(token)
	s.mu.Unlock()
}

// SetCloudMode enables hardened cloud deployment isolation (requiring auth and disabling host-filesystem paths).
func (s *Server) SetCloudMode(cloud bool) {
	s.mu.Lock()
	s.cloudMode = cloud
	s.mu.Unlock()
}

func (s *Server) isCloudMode() bool {
	s.mu.RLock()
	c := s.cloudMode
	s.mu.RUnlock()
	return c
}

func (s *Server) isAuthRequired() bool {
	s.mu.RLock()
	req := s.cloudMode || s.adminToken != ""
	s.mu.RUnlock()
	return req
}

// SetStdioDefaults sets optional default `--strategy` and `--account` overrides for `colabhive mcp stdio`.
func (s *Server) SetStdioDefaults(strategy models.RoutingStrategy, account string) {
	s.mu.Lock()
	s.defaultStdioStrategy = strategy
	s.defaultStdioAccount = strings.TrimSpace(account)
	s.mu.Unlock()
}

// SetStdioAPIKey configures an explicit `--api-key` passed on `colabhive mcp --api-key ...`.
func (s *Server) SetStdioAPIKey(rawKey string) {
	s.mu.Lock()
	s.defaultStdioAPIKey = strings.TrimSpace(rawKey)
	s.mu.Unlock()
}

// Tools returns a copy of all statically registered MCP tool definitions.
func (s *Server) Tools() []ToolDefinition {
	out := make([]ToolDefinition, len(s.tools))
	copy(out, s.tools)
	return out
}

func constantTimeEqualMCP(provided, expected string) bool {
	p := strings.TrimSpace(provided)
	e := strings.TrimSpace(expected)
	if p == "" || e == "" {
		return false
	}
	ph := sha256.Sum256([]byte(p))
	eh := sha256.Sum256([]byte(e))
	return subtle.ConstantTimeCompare(ph[:], eh[:]) == 1
}

type mcpAuthCtxKey int

const ctxKeyPreAuthedAPIKey mcpAuthCtxKey = 1

type scopedStickyRouter interface {
	ResolveStickySessionForKey(ctx context.Context, stickyKey string, callerAPIKeyID string) (*models.RuntimeSession, bool)
	UnbindStickySessionForKey(ctx context.Context, stickyKey string, apiKeyID string) error
}

// WithAuthenticatedAPIKey attaches an already-authenticated scoped AgentAPIKey to ctx
// so mounted `/mcp` handlers never re-parse or promote a scoped key to admin (`nil`).
func WithAuthenticatedAPIKey(ctx context.Context, key *models.AgentAPIKey) context.Context {
	if key == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyPreAuthedAPIKey, key)
}

func authenticatedAPIKeyFromContext(ctx context.Context) *models.AgentAPIKey {
	if ctx == nil {
		return nil
	}
	if v := ctx.Value(ctxKeyPreAuthedAPIKey); v != nil {
		if k, ok := v.(*models.AgentAPIKey); ok {
			return k
		}
	}
	return nil
}

func isAllowedLoopbackHostMCP(rawHost string) bool {
	h := strings.TrimSpace(rawHost)
	if h == "" {
		return true
	}
	if parsedHost, _, err := net.SplitHostPort(h); err == nil {
		h = parsedHost
	} else if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = strings.Trim(h, "[]")
	} else if strings.Count(h, ":") == 1 {
		return false
	}
	h = strings.Trim(h, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func isAllowedLoopbackOriginMCP(origin string) bool {
	trimmed := strings.TrimSpace(origin)
	if trimmed == "" {
		return true
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	h := u.Hostname()
	if strings.EqualFold(h, "localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

func trimBearerPrefix(raw string) string {
	k := strings.TrimSpace(raw)
	if strings.HasPrefix(strings.ToLower(k), "bearer ") {
		k = strings.TrimSpace(k[7:])
	}
	return k
}

func isAsyncStdioPayload(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	if raw[0] == '[' {
		return true
	}
	var peek struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return false
	}
	return strings.TrimSpace(peek.Method) == "tools/call"
}

// readJSONRPCLine reads a single newline-delimited JSON-RPC frame up to maxBytes using bufio.Reader.
// If a single line exceeds maxBytes, it drains the remainder of that line and returns tooLong=true
// without killing subsequent frames on the stream.
func readJSONRPCLine(r *bufio.Reader, maxBytes int) (line []byte, tooLong bool, err error) {
	var accum []byte
	exceeded := false
	for {
		frag, rErr := r.ReadSlice('\n')
		if !exceeded {
			if len(accum)+len(frag) > maxBytes {
				exceeded = true
				accum = nil
			} else if rErr == nil && len(accum) == 0 {
				return frag, false, nil
			} else {
				accum = append(accum, frag...)
			}
		}
		if rErr == nil {
			if exceeded {
				return nil, true, nil
			}
			return accum, false, nil
		}
		if errors.Is(rErr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(rErr, io.EOF) {
			if exceeded {
				return nil, true, nil
			}
			if len(accum) > 0 {
				return accum, false, nil
			}
			return nil, false, io.EOF
		}
		return nil, exceeded, rErr
	}
}

// ============================================================================
// `stdio` Transport (`colabhive mcp stdio`)
// ============================================================================

// ServeStdio serves newline-delimited JSON-RPC 2.0 over `in` and `out` until EOF or ctx cancellation.
// Long-running `tools/call` requests run concurrently (`writeMu`-guarded) so `ping` heartbeats and
// `notifications/cancelled` never suffer head-of-line blocking during GPU execution.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	stickyKey := "mcp-stdio-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]

	s.mu.RLock()
	defStrat := s.defaultStdioStrategy
	defAcc := s.defaultStdioAccount
	explicitStdioKey := s.defaultStdioAPIKey
	adminTok := s.adminToken
	cloud := s.cloudMode
	s.mu.RUnlock()

	var authedKey *models.AgentAPIKey
	rawSuppliedKey := trimBearerPrefix(firstStr(explicitStdioKey, getenvDefault("COLABHIVE_API_KEY", "")))
	if rawSuppliedKey != "" {
		if adminTok != "" && constantTimeEqualMCP(rawSuppliedKey, adminTok) {
			authedKey = nil
		} else {
			if s.router == nil {
				return errors.New("unauthorized: API key authentication unavailable in ServeStdio")
			}
			k, err := s.router.AuthenticateAPIKey(ctx, rawSuppliedKey)
			if err != nil || k == nil {
				if err != nil {
					return fmt.Errorf("unauthorized API key in ServeStdio: %w", err)
				}
				return errors.New("unauthorized API key in ServeStdio")
			}
			authedKey = k
		}
	} else if cloud {
		return errors.New("unauthorized: API key or admin token required in cloud mode")
	}

	baseCtx := callContext{
		stickyKey:       stickyKey,
		clientName:      "mcp-stdio-client",
		transport:       models.TransportMCPStdio,
		apiKey:          authedKey,
		defaultStrategy: defStrat,
		defaultAccount:  defAcc,
	}

	reader := bufio.NewReaderSize(in, 64*1024)

	var (
		writeMu      sync.Mutex
		clientMu     sync.RWMutex
		wg           sync.WaitGroup
		writeErrOnce sync.Once
		writeErr     error
	)

	writeLine := func(payload []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		buf := make([]byte, len(payload)+1)
		copy(buf, payload)
		buf[len(payload)] = '\n'
		if _, err := out.Write(buf); err != nil {
			writeErrOnce.Do(func() { writeErr = err })
			return err
		}
		return nil
	}

	snapshotCallCtx := func() callContext {
		clientMu.RLock()
		c := baseCtx
		clientMu.RUnlock()
		return c
	}

	updateClientName := func(name string) {
		if name == "" {
			return
		}
		clientMu.Lock()
		baseCtx.clientName = name
		clientMu.Unlock()
	}

	handleLine := func(rawLine []byte) error {
		localCtx := snapshotCallCtx()
		respBytes, updatedClient, shouldReply := s.processRawJSONRPC(ctx, &localCtx, rawLine)
		if updatedClient != "" {
			updateClientName(updatedClient)
		}
		if shouldReply && len(respBytes) > 0 {
			return writeLine(respBytes)
		}
		return nil
	}

	errCh := make(chan error, 1)
	go func() {
		for {
			rawLine, tooLong, rErr := readJSONRPCLine(reader, maxMCPPayloadBytes)
			if tooLong {
				errResp, _ := json.Marshal(jsonRPCResponse{
					JSONRPC: "2.0",
					ID:      json.RawMessage("null"),
					Error:   &jsonRPCError{Code: -32600, Message: "JSON-RPC frame exceeds 64 MiB limit"},
				})
				if wErr := writeLine(errResp); wErr != nil {
					wg.Wait()
					errCh <- wErr
					return
				}
				continue
			}
			if rErr != nil {
				wg.Wait()
				if errors.Is(rErr, io.EOF) {
					errCh <- writeErr
					return
				}
				errCh <- rErr
				return
			}

			trimmed := bytes.TrimSpace(rawLine)
			if len(trimmed) == 0 {
				continue
			}
			lineCopy := append([]byte(nil), trimmed...)
			if isAsyncStdioPayload(lineCopy) {
				wg.Add(1)
				go func(payload []byte) {
					defer wg.Done()
					_ = handleLine(payload)
				}(lineCopy)
			} else {
				if wErr := handleLine(lineCopy); wErr != nil {
					wg.Wait()
					errCh <- wErr
					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// ============================================================================
// Streamable HTTP (`2025-03-26` / `2025-06-18`) + Legacy SSE (`2024-11-05`) Handler
// ============================================================================

// Handler returns an http.Handler serving `/mcp` (POST/GET/DELETE) and `/mcp/sse` + `/mcp/messages`.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if !s.isAuthRequired() {
			if !isAllowedLoopbackHostMCP(r.Host) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden: non-loopback Host header blocked in local mode"})
				return
			}
			if origin != "" && !isAllowedLoopbackOriginMCP(origin) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden: cross-origin MCP request blocked in local mode"})
				return
			}
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set(
			"Access-Control-Allow-Headers",
			"Authorization, Content-Type, Accept, Mcp-Session-Id, MCP-Protocol-Version, Last-Event-ID, X-ColabHive-Key, X-ColabHive-Session-Id, X-ColabHive-Strategy, X-ColabHive-Account",
		)
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, MCP-Protocol-Version")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		apiKey, authErr := s.authenticateHTTPRequest(r.Context(), r)
		if authErr != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": authErr.Error()})
			return
		}

		cleanPath := strings.TrimRight(r.URL.Path, "/")
		switch {
		case strings.HasSuffix(cleanPath, "/mcp/sse") && r.Method == http.MethodGet:
			s.handleLegacySSE(w, r, apiKey)
		case strings.HasSuffix(cleanPath, "/mcp/messages") && r.Method == http.MethodPost:
			s.handleLegacySSEMessage(w, r, apiKey)
		default:
			switch r.Method {
			case http.MethodPost:
				s.handleStreamablePost(w, r, apiKey)
			case http.MethodGet:
				sessionHdr := firstStr(r.Header.Get("Mcp-Session-Id"), r.Header.Get("X-ColabHive-Session-Id"))
				protoHdr := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
				acceptLower := strings.ToLower(r.Header.Get("Accept"))
				wantsSSE := strings.Contains(acceptLower, "text/event-stream")

				// Dual-stack `GET /mcp`: when `Accept` contains `text/event-stream` and BOTH
				// `Mcp-Session-Id` and `MCP-Protocol-Version` headers are empty, serve Legacy 2024-11-05 SSE!
				if sessionHdr == "" && protoHdr == "" {
					if wantsSSE {
						s.handleLegacySSE(w, r, apiKey)
						return
					}
					http.Error(w, "method not allowed: POST JSON-RPC or GET with Accept: text/event-stream", http.StatusMethodNotAllowed)
					return
				}
				if acceptLower != "" && !wantsSSE && !strings.Contains(acceptLower, "*/*") {
					http.Error(w, "method not allowed: expected Accept: text/event-stream", http.StatusMethodNotAllowed)
					return
				}
				s.handleStreamableGetSSE(w, r, apiKey)
			case http.MethodDelete:
				s.handleStreamableDelete(w, r, apiKey)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		}
	})
}

func (s *Server) authenticateHTTPRequest(ctx context.Context, r *http.Request) (*models.AgentAPIKey, error) {
	if preAuthed := authenticatedAPIKeyFromContext(ctx); preAuthed != nil {
		return preAuthed, nil
	}

	rawTok := ""
	if authHdr := strings.TrimSpace(r.Header.Get("Authorization")); authHdr != "" {
		rawTok = trimBearerPrefix(authHdr)
	} else if xk := strings.TrimSpace(r.Header.Get("X-ColabHive-Key")); xk != "" {
		rawTok = xk
	} else if qTok := strings.TrimSpace(r.URL.Query().Get("token")); qTok != "" {
		rawTok = qTok
	} else if qKey := strings.TrimSpace(r.URL.Query().Get("key")); qKey != "" {
		rawTok = qKey
	} else if qAPIKey := strings.TrimSpace(r.URL.Query().Get("api_key")); qAPIKey != "" {
		rawTok = qAPIKey
	}

	s.mu.RLock()
	adminTok := s.adminToken
	cloud := s.cloudMode
	s.mu.RUnlock()

	if rawTok == "" {
		if adminTok != "" || cloud {
			return nil, errors.New("missing Authorization Bearer token or ?key= parameter")
		}
		return nil, nil
	}

	if adminTok != "" && constantTimeEqualMCP(rawTok, adminTok) {
		return nil, nil
	}

	if strings.HasPrefix(rawTok, "ch_sk_") {
		if s.router == nil {
			return nil, errors.New("API key authentication unavailable")
		}
		key, err := s.router.AuthenticateAPIKey(ctx, rawTok)
		if err != nil {
			return nil, err
		}
		return key, nil
	}

	// Reject any unrecognized non-`ch_sk_` bearer token even when `adminTok == ""`.
	return nil, errors.New("unauthorized token")
}

func (s *Server) handleStreamablePost(w http.ResponseWriter, r *http.Request, apiKey *models.AgentAPIKey) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPPayloadBytes+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(body) > maxMCPPayloadBytes {
		http.Error(w, "JSON-RPC request body exceeds 64 MiB limit", http.StatusRequestEntityTooLarge)
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		http.Error(w, "empty JSON-RPC body", http.StatusBadRequest)
		return
	}

	sessionID := strings.TrimSpace(r.Header.Get("Mcp-Session-Id"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(r.Header.Get("X-ColabHive-Session-Id"))
	}
	isNewSession := false
	if sessionID == "" {
		sessionID = "mcp-http-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
		isNewSession = true
	} else if !s.canCallerAccessMCPSession(r.Context(), sessionID, apiKey) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden: Mcp-Session-Id belongs to a different API key or principal"})
		return
	}

	clientName := s.lookupSessionClientName(sessionID, r.UserAgent())
	cctx := callContext{
		stickyKey:       sessionID,
		clientName:      clientName,
		transport:       models.TransportMCPHTTP,
		apiKey:          apiKey,
		directLoopback:  isDirectLoopbackHTTPRequest(r),
		defaultStrategy: models.RoutingStrategy(strings.TrimSpace(r.Header.Get("X-ColabHive-Strategy"))),
		defaultAccount:  strings.TrimSpace(r.Header.Get("X-ColabHive-Account")),
	}

	respBytes, updatedClient, shouldReply := s.processRawJSONRPC(r.Context(), &cctx, trimmed)
	if updatedClient != "" || isNewSession {
		nameToSave := firstStr(updatedClient, clientName, "mcp-http-client")
		s.saveSessionClientMeta(sessionID, nameToSave, cctx.apiKeyID())
	} else {
		s.touchSessionMeta(sessionID)
	}

	w.Header().Set("Mcp-Session-Id", sessionID)
	if !shouldReply {
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if strings.EqualFold(r.URL.Query().Get("stream"), "sse") {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(respBytes))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

func (s *Server) handleStreamableGetSSE(w http.ResponseWriter, r *http.Request, apiKey *models.AgentAPIKey) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sessionID := firstStr(r.Header.Get("Mcp-Session-Id"), r.Header.Get("X-ColabHive-Session-Id"))
	callerKeyID := ""
	if apiKey != nil {
		callerKeyID = apiKey.ID
	}
	if sessionID == "" {
		sessionID = "mcp-sse-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
		s.saveSessionClientMeta(sessionID, firstStr(r.UserAgent(), "mcp-sse-client"), callerKeyID)
	} else if !s.canCallerAccessMCPSession(r.Context(), sessionID, apiKey) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden: Mcp-Session-Id belongs to a different API key or principal"})
		return
	} else {
		s.touchSessionMeta(sessionID)
	}

	// Register the SSE stream in `s.sseChannels` BEFORE flushing HTTP 200 headers so a
	// client that immediately sends `DELETE /mcp` upon receiving the stream response can
	// never race ahead of registration and miss canceling this stream.
	streamCtx, streamCancel := context.WithCancel(r.Context())
	defer streamCancel()

	stream := &sseSessionStream{
		ctx:            streamCtx,
		cancel:         streamCancel,
		apiKeyID:       callerKeyID,
		directLoopback: isDirectLoopbackHTTPRequest(r),
		msgCh:          make(chan []byte, 256),
		done:           make(chan struct{}),
	}
	s.mu.Lock()
	if prev := s.sseChannels[sessionID]; prev != nil && prev.cancel != nil {
		prev.cancel()
	}
	s.sseChannels[sessionID] = stream
	s.mu.Unlock()
	defer func() {
		close(stream.done)
		s.mu.Lock()
		if s.sseChannels[sessionID] == stream {
			delete(s.sseChannels, sessionID)
		}
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Mcp-Session-Id", sessionID)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": colabhive-mcp-stream-ready\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-streamCtx.Done():
			return
		case payload := <-stream.msgCh:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(payload))
			flusher.Flush()
		case <-ticker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) handleStreamableDelete(w http.ResponseWriter, r *http.Request, apiKey *models.AgentAPIKey) {
	sessionID := firstStr(r.Header.Get("Mcp-Session-Id"), r.Header.Get("X-ColabHive-Session-Id"))
	if sessionID != "" {
		if !s.canCallerAccessMCPSession(r.Context(), sessionID, apiKey) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden: Mcp-Session-Id belongs to a different API key or principal"})
			return
		}
		s.mu.Lock()
		delete(s.sessions, sessionID)
		stream := s.sseChannels[sessionID]
		delete(s.sseChannels, sessionID)
		s.mu.Unlock()
		if stream != nil && stream.cancel != nil {
			stream.cancel()
		}
		if s.router != nil {
			callerKeyID := ""
			if apiKey != nil {
				callerKeyID = apiKey.ID
			}
			if sr, ok := s.router.(scopedStickyRouter); ok {
				_ = sr.UnbindStickySessionForKey(r.Context(), sessionID, callerKeyID)
			} else if callerKeyID == "" {
				_ = s.router.UnbindStickySession(r.Context(), sessionID)
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLegacySSE(w http.ResponseWriter, r *http.Request, apiKey *models.AgentAPIKey) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	sessionID := "mcp-sse-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:14]
	callerKeyID := ""
	if apiKey != nil {
		callerKeyID = apiKey.ID
	}
	s.saveSessionClientMeta(sessionID, firstStr(r.UserAgent(), "mcp-legacy-sse"), callerKeyID)

	streamCtx, streamCancel := context.WithCancel(r.Context())
	defer streamCancel()

	stream := &sseSessionStream{
		ctx:            streamCtx,
		cancel:         streamCancel,
		apiKeyID:       callerKeyID,
		directLoopback: isDirectLoopbackHTTPRequest(r),
		msgCh:          make(chan []byte, 256),
		done:           make(chan struct{}),
	}
	s.mu.Lock()
	s.sseChannels[sessionID] = stream
	s.mu.Unlock()
	defer func() {
		close(stream.done)
		s.mu.Lock()
		if s.sseChannels[sessionID] == stream {
			delete(s.sseChannels, sessionID)
		}
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	endpointPath := fmt.Sprintf("/mcp/messages?session_id=%s", url.QueryEscape(sessionID))
	q := r.URL.Query()
	if key := trimBearerPrefix(firstStr(q.Get("token"), q.Get("key"), q.Get("api_key"))); key != "" {
		endpointPath += "&key=" + url.QueryEscape(key)
	}
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", endpointPath)
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-streamCtx.Done():
			return
		case payload := <-stream.msgCh:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", string(payload))
			flusher.Flush()
		case <-ticker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

func (s *Server) handleLegacySSEMessage(w http.ResponseWriter, r *http.Request, apiKey *models.AgentAPIKey) {
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "missing session_id query parameter"})
		return
	}

	s.mu.RLock()
	stream := s.sseChannels[sessionID]
	adminTok := s.adminToken
	s.mu.RUnlock()

	if stream == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "unknown or closed MCP SSE session"})
		return
	}

	callerKeyID := ""
	if apiKey != nil {
		callerKeyID = apiKey.ID
	}
	if (callerKeyID != "" && stream.apiKeyID != callerKeyID) || (callerKeyID == "" && stream.apiKeyID != "" && adminTok == "") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "forbidden: SSE session belongs to a different principal"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxMCPPayloadBytes+1))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if len(body) > maxMCPPayloadBytes {
		http.Error(w, "JSON-RPC request body exceeds 64 MiB limit", http.StatusRequestEntityTooLarge)
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		http.Error(w, "empty JSON-RPC body", http.StatusBadRequest)
		return
	}

	s.touchSessionMeta(sessionID)
	ua := r.UserAgent()
	hdrStrat := models.RoutingStrategy(strings.TrimSpace(r.Header.Get("X-ColabHive-Strategy")))
	hdrAcc := strings.TrimSpace(r.Header.Get("X-ColabHive-Account"))
	msgDirectLoopback := stream.directLoopback && isDirectLoopbackHTTPRequest(r)

	// Return HTTP 202 Accepted immediately per MCP 2024-11-05 HTTP+SSE spec and process asynchronously!
	w.WriteHeader(http.StatusAccepted)

	go func(rawPayload []byte) {
		execCtx := stream.ctx
		if execCtx == nil {
			var cancel context.CancelFunc
			execCtx, cancel = context.WithTimeout(context.Background(), 45*time.Minute)
			defer cancel()
		}

		cctx := callContext{
			stickyKey:       sessionID,
			clientName:      s.lookupSessionClientName(sessionID, ua),
			transport:       models.TransportMCPSSE,
			apiKey:          apiKey,
			directLoopback:  msgDirectLoopback,
			defaultStrategy: hdrStrat,
			defaultAccount:  hdrAcc,
		}
		respBytes, updatedClient, shouldReply := s.processRawJSONRPC(execCtx, &cctx, rawPayload)
		if updatedClient != "" {
			s.saveSessionClientMeta(sessionID, updatedClient, callerKeyID)
		}
		if shouldReply && len(respBytes) > 0 {
			select {
			case stream.msgCh <- respBytes:
			case <-stream.done:
			}
		}
	}(append([]byte(nil), trimmed...))
}

// ============================================================================
// Core JSON-RPC 2.0 Method Dispatcher & Cancellation Tracking
// ============================================================================

func canonicalRequestID(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err == nil {
		return "s:" + s
	}
	var num json.Number
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&num); err == nil {
		return "n:" + num.String()
	}
	return string(trimmed)
}

func (s *Server) registerInFlightCancel(stickyKey string, reqID json.RawMessage, cancel context.CancelFunc) string {
	cid := canonicalRequestID(reqID)
	if cid == "" {
		return ""
	}
	mapKey := stickyKey + "\x00" + cid
	s.inFlightMu.Lock()
	s.inFlightCancels[mapKey] = cancel
	s.inFlightMu.Unlock()
	return mapKey
}

func (s *Server) unregisterInFlightCancel(mapKey string) {
	if mapKey == "" {
		return
	}
	s.inFlightMu.Lock()
	delete(s.inFlightCancels, mapKey)
	s.inFlightMu.Unlock()
}

func (s *Server) cancelInFlightRequest(stickyKey string, reqID json.RawMessage) {
	cid := canonicalRequestID(reqID)
	if cid == "" {
		return
	}
	mapKey := stickyKey + "\x00" + cid
	s.inFlightMu.Lock()
	cancelFn := s.inFlightCancels[mapKey]
	delete(s.inFlightCancels, mapKey)
	s.inFlightMu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}
}

func (s *Server) processRawJSONRPC(
	ctx context.Context,
	cctx *callContext,
	raw []byte,
) (respBytes []byte, discoveredClientName string, shouldReply bool) {
	if len(raw) == 0 {
		return nil, "", false
	}

	// Batch JSON-RPC array
	if raw[0] == '[' {
		var batch []jsonRPCRequest
		if err := json.Unmarshal(raw, &batch); err != nil {
			errResp, _ := json.Marshal(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &jsonRPCError{Code: -32700, Message: "Parse error: " + err.Error()},
			})
			return errResp, "", true
		}
		if len(batch) == 0 {
			errResp, _ := json.Marshal(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &jsonRPCError{Code: -32600, Message: "Invalid Request: empty JSON-RPC batch"},
			})
			return errResp, "", true
		}
		responses := make([]jsonRPCResponse, 0, len(batch))
		for _, req := range batch {
			resp, cName, reply := s.dispatchOne(ctx, cctx, req)
			if cName != "" {
				discoveredClientName = cName
				cctx.clientName = cName
			}
			if reply && resp != nil {
				responses = append(responses, *resp)
			}
		}
		if len(responses) == 0 {
			return nil, discoveredClientName, false
		}
		encoded, _ := json.Marshal(responses)
		return encoded, discoveredClientName, true
	}

	var single jsonRPCRequest
	if err := json.Unmarshal(raw, &single); err != nil {
		errResp, _ := json.Marshal(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &jsonRPCError{Code: -32700, Message: "Parse error: " + err.Error()},
		})
		return errResp, "", true
	}

	resp, cName, reply := s.dispatchOne(ctx, cctx, single)
	if !reply || resp == nil {
		return nil, cName, false
	}
	encoded, _ := json.Marshal(resp)
	return encoded, cName, true
}

func (s *Server) dispatchOne(
	ctx context.Context,
	cctx *callContext,
	req jsonRPCRequest,
) (*jsonRPCResponse, string, bool) {
	trimmedID := bytes.TrimSpace(req.ID)
	hasValidID := len(trimmedID) > 0 && !bytes.Equal(trimmedID, []byte("null"))
	method := strings.TrimSpace(req.Method)

	if method == "notifications/cancelled" || method == "cancelled" {
		var cancelParams struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if err := json.Unmarshal(req.Params, &cancelParams); err == nil {
			s.cancelInFlightRequest(cctx.stickyKey, cancelParams.RequestID)
		}
		return nil, "", false
	}

	// Per MCP 2025-03-26 / 2025-06-18 Streamable HTTP spec:
	// Notifications (`id` omitted or `null`) AND client-to-server JSON-RPC Responses (`method == ""`)
	// MUST NOT produce a JSON-RPC reply (-> HTTP 202 Accepted with empty body).
	if !hasValidID || method == "" || strings.HasPrefix(method, "notifications/") {
		return nil, "", false
	}

	switch method {
	case "initialize":
		var initParams struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		_ = json.Unmarshal(req.Params, &initParams)

		negotiated := ProtocolVersion20250326
		switch strings.TrimSpace(initParams.ProtocolVersion) {
		case ProtocolVersion20241105:
			negotiated = ProtocolVersion20241105
		case ProtocolVersion20250326:
			negotiated = ProtocolVersion20250326
		case ProtocolVersion20250618:
			negotiated = ProtocolVersion20250618
		case ProtocolVersion20251125:
			negotiated = ProtocolVersion20251125
		}

		result := map[string]any{
			"protocolVersion": negotiated,
			"capabilities": map[string]any{
				"tools": map[string]any{
					"listChanged": false, // All tools statically registered at startup!
				},
				"resources": map[string]any{},
				"prompts":   map[string]any{},
				"logging":   map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "colabhive-mcp",
				"version": "1.0.0",
			},
			"instructions": "ColabHive Multi-Account Google Colab GPU Pool & Kernel Router. All tools are available immediately without opening a browser tab. Over HTTP or API-key MCP, prefer inline transfers: pass 'content' or 'content_base64' to colab_file_upload, omit 'local_path' on colab_file_download to receive bytes inline, pass inline 'code' to colab_exec, pass 'packages'/'requirements_txt' to colab_install_packages, and pass 'script_code' to experiment_launch_sharded (host paths are only permitted over local keyless stdio or when the daemon explicitly enables --mcp-trusted-workspace for direct 127.0.0.1 loopback callers).",
		}
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result}, initParams.ClientInfo.Name, true

	case "ping":
		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}, "", true

	case "tools/list":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": s.tools,
			},
		}, "", true

	case "resources/list":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"resources": []any{}},
		}, "", true

	case "resources/templates/list":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"resourceTemplates": []any{}},
		}, "", true

	case "prompts/list":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{"prompts": []any{}},
		}, "", true

	case "logging/setLevel":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}, "", true

	case "completion/complete":
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"completion": map[string]any{
					"values":  []any{},
					"total":   0,
					"hasMore": false,
				},
			},
		}, "", true

	case "tools/call":
		var callParams struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &callParams); err != nil {
			return &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &jsonRPCError{Code: -32602, Message: "Invalid tools/call params: " + err.Error()},
			}, "", true
		}
		if callParams.Arguments == nil {
			callParams.Arguments = make(map[string]any)
		}

		handler, exists := s.toolMap[callParams.Name]
		if !exists {
			return &jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &jsonRPCError{Code: -32601, Message: fmt.Sprintf("Unknown MCP tool %q", callParams.Name)},
			}, "", true
		}

		callCtx, cancelCall := context.WithCancel(ctx)
		cancelKey := s.registerInFlightCancel(cctx.stickyKey, req.ID, cancelCall)
		defer func() {
			s.unregisterInFlightCancel(cancelKey)
			cancelCall()
		}()

		start := time.Now()
		res, trSummary, callErr := handler(callCtx, *cctx, callParams.Arguments)
		durMs := time.Since(start).Milliseconds()

		status := "ok"
		statusCode := 200
		errMsg := ""
		if callErr != nil {
			status = "error"
			statusCode = 500
			errMsg = callErr.Error()
			if cctx.apiKey != nil {
				errMsg = redactEmailsInMCPText(errMsg, "")
				res = errorToolResult(errors.New(errMsg))
			} else {
				res = errorToolResult(callErr)
			}
		} else if res != nil && res.IsError {
			status = "error"
			statusCode = 400
			if cctx.apiKey != nil {
				for i := range res.Content {
					res.Content[i].Text = redactEmailsInMCPText(res.Content[i].Text, "")
				}
			}
			if len(res.Content) > 0 {
				errMsg = res.Content[0].Text
			}
		} else if trSummary.fallback != "" {
			status = "fallback"
		}

		if s.router != nil {
			reqPrev, _ := json.Marshal(callParams.Arguments)
			respPrev := ""
			if res != nil && len(res.Content) > 0 {
				respPrev = res.Content[0].Text
			}
			keyPrefix := ""
			if cctx.apiKey != nil {
				keyPrefix = cctx.apiKey.KeyPrefix
			}
			_ = s.router.RecordTrace(context.WithoutCancel(ctx), models.ToolTraceEvent{
				Caller:          firstStr(cctx.clientName, "mcp-client"),
				APIKeyPrefix:    keyPrefix,
				Transport:       cctx.transport,
				ToolOrEndpoint:  callParams.Name,
				SessionID:       trSummary.sessionID,
				AccountID:       trSummary.accountID,
				AccountEmail:    trSummary.accountEmail,
				RequestedGPU:    trSummary.reqGPU,
				AssignedGPU:     trSummary.assignedGPU,
				FallbackTrace:   trSummary.fallback,
				DurationMs:      durMs,
				CUDelta:         trSummary.cuDelta,
				Status:          status,
				StatusCode:      statusCode,
				RequestPreview:  string(reqPrev),
				ResponsePreview: respPrev,
				ErrorMessage:    errMsg,
			})
		}

		return &jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: res}, "", true

	default:
		return &jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", method)},
		}, "", true
	}
}

func (s *Server) evictIdleSessionsLocked(now time.Time) {
	cutoff := now.Add(-mcpSessionIdleTTL)
	for id, meta := range s.sessions {
		last := meta.lastActiveAt
		if last.IsZero() {
			last = meta.createdAt
		}
		if last.Before(cutoff) {
			delete(s.sessions, id)
		}
	}
	for len(s.sessions) >= maxMCPSessions {
		var oldestKey string
		var oldestTime time.Time
		for id, meta := range s.sessions {
			last := meta.lastActiveAt
			if last.IsZero() {
				last = meta.createdAt
			}
			if oldestKey == "" || last.Before(oldestTime) {
				oldestKey = id
				oldestTime = last
			}
		}
		if oldestKey == "" {
			break
		}
		delete(s.sessions, oldestKey)
	}
}

func (s *Server) canCallerAccessMCPSession(ctx context.Context, sessionID string, apiKey *models.AgentAPIKey) bool {
	if sessionID == "" {
		return true
	}
	callerKeyID := ""
	if apiKey != nil {
		callerKeyID = apiKey.ID
	}

	s.mu.RLock()
	meta, hasMeta := s.sessions[sessionID]
	stream := s.sseChannels[sessionID]
	adminTok := s.adminToken
	s.mu.RUnlock()

	if hasMeta {
		if callerKeyID != "" && meta.apiKeyID != callerKeyID {
			return false
		}
		if callerKeyID == "" && meta.apiKeyID != "" && adminTok == "" {
			return false
		}
	}
	if stream != nil {
		if callerKeyID != "" && stream.apiKeyID != callerKeyID {
			return false
		}
		if callerKeyID == "" && stream.apiKeyID != "" && adminTok == "" {
			return false
		}
	}
	if callerKeyID != "" && s.router != nil {
		if sr, ok := s.router.(scopedStickyRouter); ok {
			if bound, okB := sr.ResolveStickySessionForKey(ctx, sessionID, callerKeyID); okB && bound != nil {
				if bound.AdoptedExternal || bound.APIKeyID != callerKeyID {
					return false
				}
			}
		} else if bound, okB := s.router.ResolveStickySession(ctx, sessionID); okB && bound != nil {
			if bound.AdoptedExternal || bound.APIKeyID != callerKeyID {
				return false
			}
		}
	}
	return true
}

func (s *Server) lookupSessionClientName(sessionID string, ua string) string {
	now := time.Now().UTC()
	s.mu.RLock()
	meta, ok := s.sessions[sessionID]
	s.mu.RUnlock()
	if ok {
		last := meta.lastActiveAt
		if last.IsZero() {
			last = meta.createdAt
		}
		if now.Sub(last) <= mcpSessionIdleTTL && meta.clientName != "" {
			return meta.clientName
		}
	}
	if strings.TrimSpace(ua) != "" {
		return strings.Split(strings.TrimSpace(ua), " ")[0]
	}
	return "mcp-client"
}

func (s *Server) saveSessionClientMeta(sessionID string, name string, apiKeyID string) {
	if sessionID == "" {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	s.evictIdleSessionsLocked(now)
	prev, had := s.sessions[sessionID]
	created := now
	if had && !prev.createdAt.IsZero() {
		created = prev.createdAt
	}
	if name == "" && had {
		name = prev.clientName
	}
	if apiKeyID == "" && had {
		apiKeyID = prev.apiKeyID
	}
	s.sessions[sessionID] = mcpClientMeta{
		clientName:   firstStr(name, "mcp-client"),
		apiKeyID:     apiKeyID,
		createdAt:    created,
		lastActiveAt: now,
	}
	s.mu.Unlock()
}

func (s *Server) touchSessionMeta(sessionID string) {
	if sessionID == "" {
		return
	}
	now := time.Now().UTC()
	s.mu.Lock()
	s.evictIdleSessionsLocked(now)
	if meta, ok := s.sessions[sessionID]; ok {
		meta.lastActiveAt = now
		s.sessions[sessionID] = meta
	}
	s.mu.Unlock()
}

func jsonToolResult(payload any) *CallToolResult {
	encoded, _ := json.MarshalIndent(payload, "", "  ")
	return &CallToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: string(encoded)},
		},
		StructuredContent: payload,
	}
}

func errorToolResult(err error) *CallToolResult {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	payload := map[string]any{"error": msg}
	encoded, _ := json.Marshal(payload)
	return &CallToolResult{
		Content: []ContentBlock{
			{Type: "text", Text: string(encoded)},
		},
		StructuredContent: payload,
		IsError:           true,
	}
}

func firstStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func getenvDefault(key, def string) string {
	if v, ok := lookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
