package pluginhost

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/appstore"
)

// SandboxSession describes an active isolated loopback server hosting one local UI plugin bundle.
// Each third-party UI plugin runs on its own deterministic `127.a.b.c:<port>` origin with a random
// per-session least-privilege token (`ch_plug_...`) and NEVER shares the control-plane origin.
type SandboxSession struct {
	PluginID    string   `json:"plugin_id"`
	Name        string   `json:"name"`
	NameZh      string   `json:"name_zh"`
	Origin      string   `json:"origin"`
	URL         string   `json:"url"`
	Permissions []string `json:"permissions"`

	token  string
	ln     net.Listener
	server *http.Server
	root   *os.Root
}

// UISandbox manages isolated loopback HTTP servers for third-party local UI plugin bundles
// and persists user UI preferences (`<dataDir>/plugin_ui_prefs.json`).
type UISandbox struct {
	mu           sync.RWMutex
	dataDir      string
	hostBaseURL  string
	byPluginID   map[string]*SandboxSession
	byOrigin     map[string]*SandboxSession
	byToken      map[string]*SandboxSession
	uiPrefs      map[string]string // capability group (e.g. "media.v1") -> UI plugin ID ("builtin:media-studio" | "<plugin-id>" | "remote:native")
}

// NewUISandbox initializes the isolated plugin UI sandbox manager.
func NewUISandbox(dataDir, hostBaseURL string) *UISandbox {
	base := strings.TrimRight(strings.TrimSpace(hostBaseURL), "/")
	if base == "" {
		base = "http://127.0.0.1:7860"
	}
	s := &UISandbox{
		dataDir:     strings.TrimSpace(dataDir),
		hostBaseURL: base,
		byPluginID:  make(map[string]*SandboxSession),
		byOrigin:    make(map[string]*SandboxSession),
		byToken:     make(map[string]*SandboxSession),
		uiPrefs:     map[string]string{"media.v1": "builtin:media-studio"},
	}
	s.loadPrefs()
	return s
}

func (s *UISandbox) prefsPath() string {
	if s.dataDir == "" || s.dataDir == ":memory:" {
		return ""
	}
	return filepath.Join(s.dataDir, "plugin_ui_prefs.json")
}

func (s *UISandbox) loadPrefs() {
	p := s.prefsPath()
	if p == "" {
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil || len(raw) == 0 {
		return
	}
	var m map[string]string
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range m {
		if strings.TrimSpace(k) != "" && strings.TrimSpace(v) != "" {
			s.uiPrefs[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}
}

func (s *UISandbox) persistPrefsLocked() {
	p := s.prefsPath()
	if p == "" {
		return
	}
	raw, err := json.MarshalIndent(s.uiPrefs, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// GetUIPreferences returns a copy of capability -> selected UI plugin ID preferences.
func (s *UISandbox) GetUIPreferences() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.uiPrefs))
	for k, v := range s.uiPrefs {
		out[k] = v
	}
	return out
}

// SetUIPreference persists the user's preferred UI plugin for a capability group (e.g. "media.v1").
func (s *UISandbox) SetUIPreference(capability, uiPluginID string) error {
	capKey := strings.ToLower(strings.TrimSpace(capability))
	uiID := strings.TrimSpace(uiPluginID)
	if capKey == "" || uiID == "" {
		return errors.New("capability and ui_plugin_id are required")
	}
	s.mu.Lock()
	s.uiPrefs[capKey] = uiID
	s.persistPrefsLocked()
	s.mu.Unlock()
	return nil
}

// deterministicPluginLoopbackIP derives a per-plugin loopback IP in 127.64.0.0/10 so each
// UI plugin runs on its own browser-enforced origin separate from 127.0.0.1:7860 and app bridges.
func deterministicPluginLoopbackIP(pluginID string) string {
	sum := sha256.Sum256([]byte("colabhive-ui-plugin:" + pluginID))
	b1 := 64 + int(sum[0]%60) // 127.64.x.y .. 127.123.x.y
	b2 := int(sum[1]%250) + 1
	b3 := int(sum[2]%250) + 2
	return fmt.Sprintf("127.%d.%d.%d", b1, b2, b3)
}

func generatePluginToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return "ch_plug_" + hex.EncodeToString(b[:])
}

// EnsureOpen starts (or returns an existing) isolated loopback sandbox server for a v2 local_bundle UI plugin.
func (s *UISandbox) EnsureOpen(manifest appstore.AppPluginManifest) (*SandboxSession, error) {
	if manifest.UI == nil || manifest.UI.Mode != "local_bundle" {
		return nil, fmt.Errorf("plugin %q does not define a local_bundle UI", manifest.ID)
	}
	if strings.TrimSpace(manifest.RootDir) == "" {
		return nil, fmt.Errorf("plugin %q has no local directory bundle on disk", manifest.ID)
	}

	s.mu.RLock()
	if existing, ok := s.byPluginID[manifest.ID]; ok && existing != nil {
		cp := *existing
		s.mu.RUnlock()
		return &cp, nil
	}
	s.mu.RUnlock()

	bundleDir := manifest.UI.BundleDir
	if bundleDir == "" {
		bundleDir = "ui/dist"
	}
	entry := manifest.UI.Entry
	if entry == "" {
		entry = "index.html"
	}
	absBundle := filepath.Join(manifest.RootDir, filepath.FromSlash(bundleDir))
	info, err := os.Stat(absBundle)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("plugin %q UI bundle directory not found (%s)", manifest.ID, bundleDir)
	}
	root, err := os.OpenRoot(absBundle)
	if err != nil {
		return nil, fmt.Errorf("open plugin UI bundle root: %w", err)
	}
	if _, eErr := root.Stat(entry); eErr != nil {
		_ = root.Close()
		return nil, fmt.Errorf("plugin %q UI entry %q not found inside %s", manifest.ID, entry, bundleDir)
	}

	bindIP := deterministicPluginLoopbackIP(manifest.ID)
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:0", bindIP))
	if err != nil {
		// Fallback to 127.0.0.1 ephemeral port if OS restricts non-.1 loopback bindings.
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = root.Close()
			return nil, fmt.Errorf("bind isolated plugin UI listener: %w", err)
		}
	}
	addr := ln.Addr().(*net.TCPAddr)
	origin := fmt.Sprintf("http://%s:%d", addr.IP.String(), addr.Port)
	token := generatePluginToken()
	perms := append([]string(nil), manifest.Permissions...)
	if len(perms) == 0 {
		// Default safe read/media permissions when omitted by simple UI bundles.
		perms = []string{"assets:read", "assets:write", "media:invoke", "engines:read", "jobs:manage", "cap:llm.openai.v1"}
	}

	rootFS := root.FS()
	sdkJS := buildPluginSDKScript(s.hostBaseURL, manifest.ID, token, perms)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /__colabhive_sdk.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(sdkJS))
	})
	serveEntry := func(w http.ResponseWriter, r *http.Request) {
		f, oErr := root.Open(entry)
		if oErr != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, stErr := f.Stat()
		if stErr != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, entry, st.ModTime(), f)
	}
	fileSrv := http.FileServerFS(rootFS)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		clean := path.Clean("/" + r.URL.Path)
		if clean == "/" || clean == "/"+entry {
			serveEntry(w, r)
			return
		}
		rel := strings.TrimPrefix(clean, "/")
		if _, stErr := fs.Stat(rootFS, rel); stErr != nil {
			// SPA fallback to entry HTML for client-side routed UI bundles.
			if !strings.Contains(path.Base(rel), ".") {
				serveEntry(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + rel
		fileSrv.ServeHTTP(w, r2)
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	sess := &SandboxSession{
		PluginID:    manifest.ID,
		Name:        manifest.Name,
		NameZh:      manifest.NameZh,
		Origin:      origin,
		URL:         origin + "/",
		Permissions: perms,
		token:       token,
		ln:          ln,
		server:      srv,
		root:        root,
	}

	s.mu.Lock()
	// Handle race if another request opened the same plugin concurrently.
	if prev, ok := s.byPluginID[manifest.ID]; ok && prev != nil {
		cp := *prev
		s.mu.Unlock()
		_ = ln.Close()
		_ = root.Close()
		return &cp, nil
	}
	s.byPluginID[manifest.ID] = sess
	s.byOrigin[origin] = sess
	s.byToken[token] = sess
	s.mu.Unlock()

	go func() {
		_ = srv.Serve(ln)
	}()

	cp := *sess
	return &cp, nil
}

// IsRegisteredOrigin reports whether origin belongs to an active isolated UI plugin sandbox.
func (s *UISandbox) IsRegisteredOrigin(origin string) bool {
	if s == nil {
		return false
	}
	trimmed := strings.TrimRight(strings.TrimSpace(origin), "/")
	if trimmed == "" {
		return false
	}
	s.mu.RLock()
	_, ok := s.byOrigin[trimmed]
	s.mu.RUnlock()
	return ok
}

// AuthenticateToken validates a scoped `ch_plug_...` Bearer / header token and verifies
// that it matches the request Origin (when present) and holds requiredPerm (if non-empty).
func (s *UISandbox) AuthenticateToken(token, origin, requiredPerm string) (*SandboxSession, bool) {
	if s == nil {
		return nil, false
	}
	cleanTok := strings.TrimSpace(token)
	if !strings.HasPrefix(cleanTok, "ch_plug_") {
		return nil, false
	}
	s.mu.RLock()
	sess, ok := s.byToken[cleanTok]
	s.mu.RUnlock()
	if !ok || sess == nil {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(sess.token), []byte(cleanTok)) != 1 {
		return nil, false
	}
	cleanOrigin := strings.TrimRight(strings.TrimSpace(origin), "/")
	if cleanOrigin != "" && !strings.EqualFold(cleanOrigin, sess.Origin) {
		return nil, false
	}
	if requiredPerm != "" && !sess.HasPermission(requiredPerm) {
		return nil, false
	}
	cp := *sess
	return &cp, true
}

// HasPermission reports whether the sandboxed plugin session holds perm.
func (s *SandboxSession) HasPermission(perm string) bool {
	want := strings.ToLower(strings.TrimSpace(perm))
	for _, p := range s.Permissions {
		if strings.EqualFold(p, want) {
			return true
		}
	}
	return false
}

// Close shuts down all active isolated UI plugin sandbox servers cleanly.
func (s *UISandbox) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	sessions := make([]*SandboxSession, 0, len(s.byPluginID))
	for _, sess := range s.byPluginID {
		if sess != nil {
			sessions = append(sessions, sess)
		}
	}
	s.byPluginID = make(map[string]*SandboxSession)
	s.byOrigin = make(map[string]*SandboxSession)
	s.byToken = make(map[string]*SandboxSession)
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, sess := range sessions {
		if sess.server != nil {
			_ = sess.server.Shutdown(ctx)
		}
		if sess.ln != nil {
			_ = sess.ln.Close()
		}
		if sess.root != nil {
			_ = sess.root.Close()
		}
	}
	return nil
}

func buildPluginSDKScript(hostBaseURL, pluginID, token string, perms []string) string {
	cfgJSON, _ := json.Marshal(map[string]any{
		"hostBaseURL": hostBaseURL,
		"pluginID":    pluginID,
		"token":       token,
		"permissions": perms,
	})
	return fmt.Sprintf(`(function(){
  const cfg = %s;
  function authHeaders(extra) {
    return Object.assign({ "Authorization": "Bearer " + cfg.token, "X-ColabHive-Plugin-Token": cfg.token }, extra || {});
  }
  async function req(path, opts) {
    const o = Object.assign({}, opts || {});
    o.headers = authHeaders(o.headers);
    const res = await fetch(cfg.hostBaseURL + path, o);
    const txt = await res.text();
    let body = null;
    try { body = txt ? JSON.parse(txt) : null; } catch(_) { body = txt; }
    if (!res.ok) {
      throw new Error((body && (body.error || body.message)) || ("HTTP " + res.status));
    }
    return body;
  }
  window.ColabHiveSDK = {
    version: "2",
    pluginId: cfg.pluginID,
    hostBaseURL: cfg.hostBaseURL,
    permissions: cfg.permissions,
    assetFileURL: function(assetId) {
      return cfg.hostBaseURL + "/sdk/v1/assets/" + encodeURIComponent(assetId) + "/file?plugin_token=" + encodeURIComponent(cfg.token);
    },
    getCapabilities: function() { return req("/sdk/v1/capabilities"); },
    listAssets: function(params) {
      const qs = new URLSearchParams(params || {}).toString();
      return req("/sdk/v1/assets" + (qs ? "?" + qs : ""));
    },
    deleteAsset: function(id) { return req("/sdk/v1/assets/" + encodeURIComponent(id), { method: "DELETE" }); },
    uploadAsset: function(file, meta) {
      const fd = new FormData();
      fd.append("file", file);
      if (meta) fd.append("meta", JSON.stringify(meta));
      return req("/sdk/v1/assets", { method: "POST", body: fd });
    },
    listMediaEngines: function() { return req("/sdk/v1/media/engines"); },
    listEngineModels: function(instanceId, force) {
      return req("/sdk/v1/media/instances/" + encodeURIComponent(instanceId) + "/models" + (force ? "?refresh=1" : ""));
    },
    getModelDefaults: function(instanceId, modelId) {
      return req("/sdk/v1/media/instances/" + encodeURIComponent(instanceId) + "/models/" + encodeURIComponent(modelId) + "/defaults");
    },
    createMediaJob: function(payload) {
      return req("/sdk/v1/media/jobs", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload || {})
      });
    },
    listMediaJobs: function() { return req("/sdk/v1/media/jobs"); },
    cancelMediaJob: function(jobId) {
      return req("/sdk/v1/media/jobs/" + encodeURIComponent(jobId) + "/cancel", { method: "POST" });
    },
    subscribeJobEvents: function(jobId, onEvent) {
      const es = new EventSource(cfg.hostBaseURL + "/sdk/v1/media/jobs/" + encodeURIComponent(jobId) + "/events?plugin_token=" + encodeURIComponent(cfg.token));
      es.onmessage = function(ev) {
        try { onEvent(JSON.parse(ev.data)); } catch(_) {}
      };
      return function() { es.close(); };
    },
    enhancePrompt: function(payload) {
      return req("/sdk/v1/capabilities/llm.openai.v1/enhance-prompt", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload || {})
      });
    }
  };
})();
`, string(cfgJSON))
}
