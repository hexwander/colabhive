// Package mockcolab provides a deterministic, realistic local HTTP + WebSocket mock of
// Google OAuth2, Colab GAPI (`v1/user-info`), Colab Public API (`v1beta`), Colab Tunnel
// (`/tun/m/*` with `)]}'\n` XSSI guard + 2-step XSRF), Tornado `/api/colab/resources`,
// and Jupyter REST + ZMQ-over-WebSocket kernel channels for hermetic tests and `--demo` mode.
package mockcolab

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/google/uuid"
)

// MockAccount defines a simulated Google Colab user account inside mockcolab.Server.
type MockAccount struct {
	Email             string
	DisplayName       string
	AvatarURL         string
	SubscriptionTier  string // e.g. "SUBSCRIPTION_TIER_PRO_PLUS", "SUBSCRIPTION_TIER_PRO", "SUBSCRIPTION_TIER_NONE"
	PaidCUBalance     float64
	FreeMilliCCU      int64
	NextRefillSec     int64
	MaxVMs            int
	EligibleGPUs      []string // e.g. ["T4", "L4", "G4", "A100", "H100"]
	EligibleTPUs      []string // e.g. ["V5E1", "V6E1"]
	IneligibleGPUs    []string
	PublicAPIEnabled  bool
	AuthCode          string
	RefreshToken      string
	AccessToken       string
	Scopes            []string
}

type mockVM struct {
	Endpoint        string
	NotebookHash    string
	AccountEmail    string
	Variant         int
	MachineShape    int
	Accelerator     models.AcceleratorType
	BurnRateCUHr    float64
	ProxyToken      string
	CreatedAt       time.Time
	LastKeepaliveAt time.Time
	ZombieWorker    bool

	mu       sync.RWMutex
	kernels  map[string]*mockKernel
	sessions map[string]map[string]any
	files    map[string][]byte // normalized path e.g. "content/sample_data/README.md" -> raw bytes
}

type mockKernel struct {
	ID             string
	Name           string
	ExecutionState string
	ExecutionCount int
	LastActivity   time.Time
	Interrupted    bool
}

type recordedAuthChallenge struct {
	CodeChallenge string
	Method        string
	Scopes        string
}

// Server is an in-process mock of Google OAuth2, Colab control-plane APIs, and Jupyter VM proxies.
type Server struct {
	URL string

	ln      net.Listener
	httpSrv *http.Server

	mu                 sync.RWMutex
	accountsByEmail    map[string]*MockAccount
	codeToEmail        map[string]string
	refreshToEmail     map[string]string
	accessToEmail      map[string]string
	revokedTokens      map[string]bool
	authChallenges     map[string]recordedAuthChallenge
	xsrfTokens         map[string]string // token -> action key
	vmsByEndpoint      map[string]*mockVM
	nbhToEndpoint      map[string]string
	proxyTokenToVM     map[string]*mockVM
	nextAssignStatus   int

	revokeCalls            atomic.Int64
	tokenInfoSawAuthHeader atomic.Bool
}

// NewServer starts a new local mockcolab HTTP + WebSocket server on an ephemeral loopback port.
func NewServer() *Server {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("mockcolab: listen failed: %v", err))
	}

	s := &Server{
		URL:             "http://" + ln.Addr().String(),
		ln:              ln,
		accountsByEmail: make(map[string]*MockAccount),
		codeToEmail:     make(map[string]string),
		refreshToEmail:  make(map[string]string),
		accessToEmail:   make(map[string]string),
		revokedTokens:   make(map[string]bool),
		authChallenges:  make(map[string]recordedAuthChallenge),
		xsrfTokens:      make(map[string]string),
		vmsByEndpoint:   make(map[string]*mockVM),
		nbhToEndpoint:   make(map[string]string),
		proxyTokenToVM:  make(map[string]*mockVM),
	}

	s.seedDefaultMockAccounts()

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.httpSrv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = s.httpSrv.Serve(ln)
	}()

	return s
}

// BaseURL returns the server's HTTP base URL (`http://127.0.0.1:<port>`).
func (s *Server) BaseURL() string {
	return s.URL
}

// Close shuts down the mock server and closes its listener.
func (s *Server) Close() error {
	return s.httpSrv.Close()
}

// PoolConfig returns a pool.Config pre-wired to this mock server's OAuth2 & Colab endpoints.
func (s *Server) PoolConfig(masterSecret string) pool.Config {
	return pool.Config{
		MasterSecret:        masterSecret,
		PublicBaseURL:       "http://127.0.0.1:7860",
		GoogleAuthURI:       s.URL + "/o/oauth2/auth",
		GoogleTokenURI:      s.URL + "/token",
		GoogleTokenInfoURI:  s.URL + "/tokeninfo",
		GoogleRevokeURI:     s.URL + "/revoke",
		GoogleUserInfoURI:   s.URL + "/oauth2/v2/userinfo",
		ColabTunnelDomain:   s.URL,
		ColabGAPIDomain:     s.URL,
		ColabPublicDomain:   s.URL,
		ProxyProbeTargetURL: s.URL + "/ip",
	}
}

// RegisterAccount registers or updates a simulated Google account in the mock server.
func (s *Server) RegisterAccount(acct MockAccount) *MockAccount {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registerAccountLocked(acct)
}

func (s *Server) registerAccountLocked(acct MockAccount) *MockAccount {
	email := strings.ToLower(strings.TrimSpace(acct.Email))
	if acct.MaxVMs <= 0 {
		acct.MaxVMs = 3
	}
	if acct.NextRefillSec <= 0 {
		acct.NextRefillSec = time.Now().UTC().Add(4 * time.Hour).Unix()
	}
	slug := strings.Split(email, "@")[0]
	if acct.AuthCode == "" {
		acct.AuthCode = "4/0A_mock_code_" + slug
	}
	if acct.RefreshToken == "" {
		acct.RefreshToken = "1//0mock_refresh_" + slug
	}
	if acct.AccessToken == "" {
		acct.AccessToken = "ya29.mock_access_" + slug
	}
	if len(acct.Scopes) == 0 {
		acct.Scopes = append([]string(nil), models.OwnerOAuthScopes...)
	}

	cp := acct
	s.accountsByEmail[email] = &cp
	s.codeToEmail[cp.AuthCode] = email
	s.refreshToEmail[cp.RefreshToken] = email
	s.accessToEmail[cp.AccessToken] = email
	return &cp
}

func (s *Server) seedDefaultMockAccounts() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.registerAccountLocked(MockAccount{
		Email:            "lab-ultra-01@stanford-ai.org",
		DisplayName:      "Stanford AI Lab Ultra #1",
		SubscriptionTier: "SUBSCRIPTION_TIER_PRO_PLUS",
		PaidCUBalance:    820.50,
		FreeMilliCCU:     18500,
		MaxVMs:           5,
		EligibleGPUs:     []string{"T4", "L4", "G4", "A100", "H100"},
		EligibleTPUs:     []string{"V5E1", "V6E1"},
		PublicAPIEnabled: true,
		AuthCode:         "4/0A_ultra_01",
		RefreshToken:     "1//0ultra_refresh_01",
		AccessToken:      "ya29.ultra_access_01",
	})

	s.registerAccountLocked(MockAccount{
		Email:            "research-proplus-02@berkeley.edu",
		DisplayName:      "Berkeley Systems Pro+ #2",
		SubscriptionTier: "SUBSCRIPTION_TIER_PRO_PLUS",
		PaidCUBalance:    412.80,
		FreeMilliCCU:     14000,
		MaxVMs:           4,
		EligibleGPUs:     []string{"T4", "L4", "G4", "A100"},
		IneligibleGPUs:   []string{"H100"},
		EligibleTPUs:     []string{"V5E1", "V6E1"},
		PublicAPIEnabled: true,
		AuthCode:         "4/0A_proplus_02",
		RefreshToken:     "1//0proplus_refresh_02",
		AccessToken:      "ya29.proplus_access_02",
	})

	s.registerAccountLocked(MockAccount{
		Email:            "sweeps-pro-03@gmail.com",
		DisplayName:      "Eval Sweeps Pro #3",
		SubscriptionTier: "SUBSCRIPTION_TIER_PRO",
		PaidCUBalance:    94.20,
		FreeMilliCCU:     12000,
		MaxVMs:           2,
		EligibleGPUs:     []string{"T4", "L4", "A100"},
		IneligibleGPUs:   []string{"G4", "H100"},
		EligibleTPUs:     []string{"V5E1"},
		PublicAPIEnabled: false,
		AuthCode:         "4/0A_pro_03",
		RefreshToken:     "1//0pro_refresh_03",
		AccessToken:      "ya29.pro_access_03",
	})

	s.registerAccountLocked(MockAccount{
		Email:            "alice.donated@mit.edu",
		DisplayName:      "Alice Chen (Contributed)",
		SubscriptionTier: "SUBSCRIPTION_TIER_PRO_PLUS",
		PaidCUBalance:    310.00,
		FreeMilliCCU:     15000,
		MaxVMs:           2,
		EligibleGPUs:     []string{"T4", "L4", "G4", "A100"},
		EligibleTPUs:     []string{"V5E1", "V6E1"},
		PublicAPIEnabled: false,
		AuthCode:         "4/0A_contrib_alice",
		RefreshToken:     "1//0contrib_refresh_alice",
		AccessToken:      "ya29.contrib_access_alice",
		Scopes:           append([]string(nil), models.ContributorOAuthScopes...),
	})
}

// RevokeCalls returns the total count of `POST /revoke` invocations received.
func (s *Server) RevokeCalls() int {
	return int(s.revokeCalls.Load())
}

// IsTokenRevoked reports whether token has been invalidated via `/revoke` or RevokeToken.
func (s *Server) IsTokenRevoked(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revokedTokens[token]
}

// RevokeToken simulates user-initiated revocation at myaccount.google.com/permissions.
func (s *Server) RevokeToken(token string) {
	s.mu.Lock()
	s.revokedTokens[token] = true
	s.mu.Unlock()
}

// TokenInfoSawAuthHeader reports whether `/tokeninfo` received an unwanted Authorization header.
func (s *Server) TokenInfoSawAuthHeader() bool {
	return s.tokenInfoSawAuthHeader.Load()
}

// SetNextAssignHTTPStatus injects a one-shot HTTP error status (e.g. 503) on the next POST /tun/m/assign.
func (s *Server) SetNextAssignHTTPStatus(status int) {
	s.mu.Lock()
	s.nextAssignStatus = status
	s.mu.Unlock()
}

// SetWorkerZombieMode toggles `/proc/{pid}/stat` zombie (`Z`) simulation on a mock VM.
func (s *Server) SetWorkerZombieMode(endpoint string, zombie bool) {
	s.mu.Lock()
	if vm, ok := s.vmsByEndpoint[endpoint]; ok {
		vm.ZombieWorker = zombie
	}
	s.mu.Unlock()
}

// SeedExternalAssignment creates a running VM directly inside mockcolab (simulating a task started
// externally via Colab Web UI or `google-colab-cli` prior to ColabHive discovery).
func (s *Server) SeedExternalAssignment(
	email string,
	accel models.AcceleratorType,
	highMem bool,
	notebookName string,
	executionState string,
	files map[string][]byte,
) (endpoint string, kernelID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	spec, ok := models.GetHardwareSpec(accel)
	if !ok {
		spec, _ = models.GetHardwareSpec(models.AcceleratorG4)
		accel = models.AcceleratorG4
	}
	shortID := strings.ReplaceAll(uuid.NewString()[:10], "-", "")
	prefix := "gpu"
	if spec.Variant == models.VariantTPU {
		prefix = "tpu"
	} else if spec.Variant == models.VariantDefault {
		prefix = "rn"
	}
	endpoint = fmt.Sprintf("%s-%s-s-%s", prefix, strings.ToLower(string(accel)), shortID)
	proxyTok := "ChAKDm_ext_" + shortID
	nbh := models.NewNotebookHash()
	now := time.Now().UTC()

	shapeCode := 0
	if highMem || spec.AlwaysHighMem {
		shapeCode = 1
	}

	if executionState == "" {
		executionState = "busy"
	}
	kernelID = "kern-ext-" + shortID[:8]
	kern := &mockKernel{
		ID:             kernelID,
		Name:           "python3",
		ExecutionState: executionState,
		ExecutionCount: 4,
		LastActivity:   now,
	}

	nbPath := strings.TrimSpace(notebookName)
	if nbPath == "" {
		nbPath = "external_experiment.ipynb"
	}
	sessID := "jup-ext-" + shortID[:8]

	vmFiles := map[string][]byte{
		"content/sample_data/README.md": []byte("Colab Runtime /content/sample_data\n"),
	}
	for k, v := range files {
		cleanKey := strings.TrimPrefix(strings.TrimSpace(k), "/")
		if !strings.HasPrefix(cleanKey, "content/") && cleanKey != "content" {
			cleanKey = "content/" + cleanKey
		}
		vmFiles[cleanKey] = append([]byte(nil), v...)
	}

	vm := &mockVM{
		Endpoint:        endpoint,
		NotebookHash:    nbh,
		AccountEmail:    strings.TrimSpace(email),
		Variant:         spec.Variant.WireCode(),
		MachineShape:    shapeCode,
		Accelerator:     accel,
		BurnRateCUHr:    spec.DefaultBurnRateCUHr,
		ProxyToken:      proxyTok,
		CreatedAt:       now,
		LastKeepaliveAt: now,
		kernels: map[string]*mockKernel{
			kernelID: kern,
		},
		sessions: map[string]map[string]any{
			sessID: {
				"id":     sessID,
				"path":   nbPath,
				"name":   nbPath,
				"type":   "notebook",
				"kernel": kernelToWire(kern),
			},
		},
		files: vmFiles,
	}
	s.vmsByEndpoint[endpoint] = vm
	s.nbhToEndpoint[nbh] = endpoint
	s.proxyTokenToVM[proxyTok] = vm
	return endpoint, kernelID
}

// WriteVMFile writes or updates a file inside a mock VM's `/content` tree.
func (s *Server) WriteVMFile(endpoint string, relPath string, content []byte) {
	s.mu.Lock()
	vm, ok := s.vmsByEndpoint[endpoint]
	s.mu.Unlock()
	if !ok || vm == nil {
		return
	}
	cleanKey := strings.TrimPrefix(strings.TrimSpace(relPath), "/")
	if !strings.HasPrefix(cleanKey, "content/") && cleanKey != "content" {
		cleanKey = "content/" + cleanKey
	}
	vm.mu.Lock()
	vm.files[cleanKey] = append([]byte(nil), content...)
	vm.mu.Unlock()
}

// UnassignExternalEndpoint simulates an external Colab runtime being stopped/unassigned upstream.
func (s *Server) UnassignExternalEndpoint(endpoint string) {
	s.mu.Lock()
	if vm, ok := s.vmsByEndpoint[endpoint]; ok {
		delete(s.nbhToEndpoint, vm.NotebookHash)
		delete(s.proxyTokenToVM, vm.ProxyToken)
		delete(s.vmsByEndpoint, endpoint)
	}
	s.mu.Unlock()
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Probe IP helper
	mux.HandleFunc("/ip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"ip":      "198.51.100.77",
			"country": "US",
		})
	})

	// Google OAuth2 Endpoints
	mux.HandleFunc("/o/oauth2/auth", s.handleOAuthAuthorize)
	mux.HandleFunc("/token", s.handleOAuthToken)
	mux.HandleFunc("/tokeninfo", s.handleTokenInfo)
	mux.HandleFunc("/oauth2/v2/userinfo", s.handleUserInfo)
	mux.HandleFunc("/oauth2/v3/userinfo", s.handleUserInfo)
	mux.HandleFunc("/revoke", s.handleRevoke)

	// Colab GAPI & Public API Endpoints
	mux.HandleFunc("/v1/user-info", s.handleGAPIUserInfo)
	mux.HandleFunc("/v1beta/subscription", s.handlePublicSubscription)
	mux.HandleFunc("/v1beta/runtimespecs", s.handlePublicRuntimeSpecs)

	// Colab Tunnel API (`/tun/m/*` with `)]}'\n` XSSI prefix)
	mux.HandleFunc("/tun/m/ccu-info", s.handleTunnelCCUInfo)
	mux.HandleFunc("/tun/m/assignments", s.handleTunnelAssignments)
	mux.HandleFunc("/tun/m/assign", s.handleTunnelAssign)
	mux.HandleFunc("/tun/m/unassign/", s.handleTunnelUnassign)
	mux.HandleFunc("/tun/m/", s.handleTunnelCatchAll)

	// Colab VM Runtime Proxy (`/runtime/{endpoint}/api/...` & direct `/api/...`)
	mux.HandleFunc("/runtime/", s.handleRuntimeSubtree)
	mux.HandleFunc("/api/", s.handleDirectRuntimeSubtree)
}

func writeColabXSSIJSON(w http.ResponseWriter, status int, payload any) {
	b, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(models.ColabXSSIPrefix))
	_, _ = w.Write(b)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) authenticateBearer(r *http.Request) (*MockAccount, bool) {
	hdr := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(hdr), "bearer ") {
		return nil, false
	}
	tok := strings.TrimSpace(hdr[7:])
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.revokedTokens[tok] {
		return nil, false
	}
	email, ok := s.accessToEmail[tok]
	if !ok {
		return nil, false
	}
	acct, ok := s.accountsByEmail[email]
	return acct, ok
}

func (s *Server) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	challenge := q.Get("code_challenge")
	method := q.Get("code_challenge_method")
	scopes := q.Get("scope")

	if state != "" && challenge != "" {
		s.mu.Lock()
		s.authChallenges[state] = recordedAuthChallenge{
			CodeChallenge: challenge,
			Method:        method,
			Scopes:        scopes,
		}
		s.mu.Unlock()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<html><body><h1>ColabHive Mock Google Consent</h1><code>4/0A_ultra_01</code></body></html>")
}

func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	_ = r.ParseForm()
	grantType := r.FormValue("grant_type")

	switch grantType {
	case "authorization_code":
		code := strings.TrimSpace(r.FormValue("code"))
		verifier := strings.TrimSpace(r.FormValue("code_verifier"))
		if code == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "missing code"})
			return
		}

		// Verify PKCE S256 if any recorded challenge matches this verifier; reject if verifier is empty!
		if verifier == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request", "error_description": "missing pkce code_verifier"})
			return
		}
		sum := sha256.Sum256([]byte(verifier))
		computedChallenge := base64.RawURLEncoding.EncodeToString(sum[:])

		s.mu.Lock()
		defer s.mu.Unlock()

		// Check if any recorded challenge matches, and inherit its requested scopes if dynamic.
		var matchedScopes string
		if len(s.authChallenges) > 0 {
			foundMatch := false
			for st, ch := range s.authChallenges {
				if ch.CodeChallenge == computedChallenge {
					foundMatch = true
					matchedScopes = ch.Scopes
					delete(s.authChallenges, st)
					break
				}
			}
			// If challenges were explicitly registered via GET /o/oauth2/auth, require match when code starts with "strict_".
			if !foundMatch && strings.HasPrefix(code, "strict_") {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "PKCE code_verifier mismatch"})
				return
			}
		}

		email, ok := s.codeToEmail[code]
		var acct *MockAccount
		if ok {
			acct = s.accountsByEmail[email]
		} else {
			// Dynamically create demo account for interactive paste testing
			short := strings.ReplaceAll(uuid.NewString()[:6], "-", "")
			acct = s.registerAccountLocked(MockAccount{
				Email:            fmt.Sprintf("demo-user-%s@gmail.com", short),
				DisplayName:      "ColabHive Demo Researcher",
				SubscriptionTier: "SUBSCRIPTION_TIER_PRO_PLUS",
				PaidCUBalance:    250.0,
				FreeMilliCCU:     15000,
				MaxVMs:           3,
				EligibleGPUs:     []string{"T4", "L4", "G4", "A100"},
				EligibleTPUs:     []string{"V5E1", "V6E1"},
				PublicAPIEnabled: true,
				AuthCode:         code,
			})
		}

		freshAccess := "ya29.mock_" + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
		acct.AccessToken = freshAccess
		s.accessToEmail[freshAccess] = strings.ToLower(acct.Email)

		scopeStr := strings.Join(acct.Scopes, " ")
		if matchedScopes != "" {
			scopeStr = matchedScopes
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"access_token":  freshAccess,
			"expires_in":    3599,
			"refresh_token": acct.RefreshToken,
			"scope":         scopeStr,
			"token_type":    "Bearer",
		})

	case "refresh_token":
		ref := strings.TrimSpace(r.FormValue("refresh_token"))
		s.mu.Lock()
		defer s.mu.Unlock()

		if ref == "" || s.revokedTokens[ref] {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":             "invalid_grant",
				"error_description": "Token has been expired or revoked.",
			})
			return
		}
		email, ok := s.refreshToEmail[ref]
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":             "invalid_grant",
				"error_description": "Unknown refresh token.",
			})
			return
		}
		acct := s.accountsByEmail[email]
		freshAccess := "ya29.refreshed_" + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
		acct.AccessToken = freshAccess
		s.accessToEmail[freshAccess] = email

		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": freshAccess,
			"expires_in":   3599,
			"scope":        strings.Join(acct.Scopes, " "),
			"token_type":   "Bearer",
		})

	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
	}
}

func (s *Server) handleTokenInfo(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		s.tokenInfoSawAuthHeader.Store(true)
	}
	tok := strings.TrimSpace(r.URL.Query().Get("access_token"))
	s.mu.RLock()
	email, ok := s.accessToEmail[tok]
	revoked := s.revokedTokens[tok]
	var acct *MockAccount
	if ok {
		acct = s.accountsByEmail[email]
	}
	s.mu.RUnlock()

	if !ok || revoked || acct == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error_description": "Invalid Value"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email":          acct.Email,
		"email_verified": "true",
		"scope":          strings.Join(acct.Scopes, " "),
		"expires_in":     "3590",
		"sub":            "109876543210",
	})
}

func (s *Server) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticateBearer(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":             "109876543210",
		"email":          acct.Email,
		"verified_email": true,
		"name":           acct.DisplayName,
		"picture":        acct.AvatarURL,
	})
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	tok := strings.TrimSpace(r.URL.Query().Get("token"))
	if tok == "" {
		tok = strings.TrimSpace(r.FormValue("token"))
	}
	s.revokeCalls.Add(1)
	if tok == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_token"})
		return
	}

	s.mu.Lock()
	s.revokedTokens[tok] = true
	if email, ok := s.refreshToEmail[tok]; ok {
		if acct, okA := s.accountsByEmail[email]; okA {
			s.revokedTokens[acct.AccessToken] = true
		}
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) accountLiveBurnAndCountLocked(email string) (float64, int) {
	var burn float64
	var count int
	lower := strings.ToLower(email)
	for _, vm := range s.vmsByEndpoint {
		if strings.ToLower(vm.AccountEmail) == lower {
			count++
			burn += vm.BurnRateCUHr
		}
	}
	return burn, count
}

func (s *Server) handleGAPIUserInfo(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticateBearer(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "UNAUTHENTICATED"})
		return
	}

	s.mu.RLock()
	burn, count := s.accountLiveBurnAndCountLocked(acct.Email)
	bal := acct.PaidCUBalance
	tier := acct.SubscriptionTier
	eligGPUs := append([]string(nil), acct.EligibleGPUs...)
	eligTPUs := append([]string(nil), acct.EligibleTPUs...)
	ineligGPUs := append([]string(nil), acct.IneligibleGPUs...)
	milli := acct.FreeMilliCCU
	refill := acct.NextRefillSec
	s.mu.RUnlock()

	var elig []map[string]any
	if len(eligGPUs) > 0 {
		elig = append(elig, map[string]any{"variant": "VARIANT_GPU", "models": eligGPUs})
	}
	if len(eligTPUs) > 0 {
		elig = append(elig, map[string]any{"variant": "VARIANT_TPU", "models": eligTPUs})
	}
	var inelig []map[string]any
	if len(ineligGPUs) > 0 {
		inelig = append(inelig, map[string]any{"variant": "VARIANT_GPU", "models": ineligGPUs})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"subscriptionTier":        tier,
		"paidComputeUnitsBalance": bal,
		"consumptionRateHourly":   burn,
		"assignmentsCount":        count,
		"eligibleAccelerators":    elig,
		"ineligibleAccelerators":  inelig,
		"freeCcuQuotaInfo": map[string]any{
			"remainingTokens":        fmt.Sprintf("%d", milli),
			"nextRefillTimestampSec": refill,
		},
	})
}

func (s *Server) handlePublicSubscription(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticateBearer(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "UNAUTHENTICATED"})
		return
	}
	if !acct.PublicAPIEnabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "USER_PROJECT_DENIED"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tier": acct.SubscriptionTier,
	})
}

func (s *Server) handlePublicRuntimeSpecs(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticateBearer(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "UNAUTHENTICATED"})
		return
	}
	if !acct.PublicAPIEnabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "USER_PROJECT_DENIED"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runtimeSpecs": models.HardwareCatalog,
	})
}

func (s *Server) handleTunnelCCUInfo(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticateBearer(r)
	if !ok {
		writeColabXSSIJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	s.mu.RLock()
	burn, count := s.accountLiveBurnAndCountLocked(acct.Email)
	bal := acct.PaidCUBalance
	eligGPUs := append([]string(nil), acct.EligibleGPUs...)
	eligTPUs := append([]string(nil), acct.EligibleTPUs...)
	milli := acct.FreeMilliCCU
	refill := acct.NextRefillSec
	s.mu.RUnlock()

	writeColabXSSIJSON(w, http.StatusOK, map[string]any{
		"currentBalance":        bal,
		"consumptionRateHourly": burn,
		"assignmentsCount":      count,
		"eligibleGpus":          eligGPUs,
		"eligibleTpus":          eligTPUs,
		"freeCcuQuotaInfo": map[string]any{
			"remainingTokens":        milli,
			"nextRefillTimestampSec": refill,
		},
	})
}

func (s *Server) handleTunnelAssignments(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticateBearer(r)
	if !ok {
		writeColabXSSIJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	s.mu.RLock()
	list := make([]map[string]any, 0)
	for _, vm := range s.vmsByEndpoint {
		if strings.EqualFold(vm.AccountEmail, acct.Email) {
			wireAcc := "NONE"
			if spec, okS := models.GetHardwareSpec(vm.Accelerator); okS {
				wireAcc = spec.WireAccelerator
			}
			list = append(list, map[string]any{
				"accelerator":  wireAcc,
				"endpoint":     vm.Endpoint,
				"variant":      vm.Variant,
				"machineShape": vm.MachineShape,
				"runtimeProxyInfo": map[string]any{
					"token":                 vm.ProxyToken,
					"tokenExpiresInSeconds": 3600,
					"url":                   s.URL + "/runtime/" + vm.Endpoint,
				},
			})
		}
	}
	s.mu.RUnlock()

	writeColabXSSIJSON(w, http.StatusOK, map[string]any{"assignments": list})
}

func (s *Server) handleTunnelAssign(w http.ResponseWriter, r *http.Request) {
	acct, ok := s.authenticateBearer(r)
	if !ok {
		writeColabXSSIJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	q := r.URL.Query()
	nbh := q.Get("nbh")
	variantStr := q.Get("variant")
	if variantStr == "" {
		variantStr = "DEFAULT"
	}
	rawAcc := q.Get("accelerator")
	if rawAcc == "" {
		rawAcc = "NONE"
	}
	shapeParam := q.Get("shape")

	normAcc, err := models.NormalizeAccelerator(rawAcc)
	if err != nil {
		// Mirror Colab quirk where unknown strings fall back to A100 unless rejected
		normAcc = models.AcceleratorA100
		rawAcc = "A100"
	}

	switch r.Method {
	case http.MethodGet:
		s.mu.Lock()
		if ep, exists := s.nbhToEndpoint[nbh]; exists {
			if vm, okVM := s.vmsByEndpoint[ep]; okVM {
				s.mu.Unlock()
				writeColabXSSIJSON(w, http.StatusOK, map[string]any{
					"endpoint": vm.Endpoint,
					"runtimeProxyInfo": map[string]any{
						"token":                 vm.ProxyToken,
						"tokenExpiresInSeconds": 3600,
						"url":                   s.URL + "/runtime/" + vm.Endpoint,
					},
				})
				return
			}
		}
		xsrf := "xsrf_assign_" + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
		s.xsrfTokens[xsrf] = "assign:" + nbh
		s.mu.Unlock()

		writeColabXSSIJSON(w, http.StatusOK, map[string]any{
			"acc":     strings.ToUpper(rawAcc),
			"nbh":     nbh,
			"token":   xsrf,
			"variant": variantStr,
		})

	case http.MethodPost:
		xsrf := strings.TrimSpace(r.Header.Get("X-Goog-Colab-Token"))
		s.mu.Lock()
		expected, okX := s.xsrfTokens[xsrf]
		if !okX || expected != "assign:"+nbh {
			s.mu.Unlock()
			writeColabXSSIJSON(w, http.StatusForbidden, map[string]string{"error": "missing or invalid X-Goog-Colab-Token"})
			return
		}
		delete(s.xsrfTokens, xsrf)

		if s.nextAssignStatus != 0 {
			injected := s.nextAssignStatus
			s.nextAssignStatus = 0
			s.mu.Unlock()
			writeColabXSSIJSON(w, injected, map[string]string{"error": fmt.Sprintf("injected HTTP %d", injected)})
			return
		}

		// Check GPU/TPU entitlement -> HTTP 400 if account lacks tier entitlement
		if normAcc != models.AcceleratorCPU {
			allowed := false
			for _, g := range acct.EligibleGPUs {
				if strings.EqualFold(g, string(normAcc)) {
					allowed = true
					break
				}
			}
			for _, t := range acct.EligibleTPUs {
				if strings.EqualFold(t, string(normAcc)) {
					allowed = true
					break
				}
			}
			if !allowed {
				s.mu.Unlock()
				writeColabXSSIJSON(w, http.StatusBadRequest, map[string]string{
					"error": fmt.Sprintf("Accelerator %s is not available for this subscription tier", normAcc),
				})
				return
			}
		}

		// Check max concurrent assignments -> HTTP 412 Precondition Failed
		_, activeCount := s.accountLiveBurnAndCountLocked(acct.Email)
		if activeCount >= acct.MaxVMs {
			s.mu.Unlock()
			writeColabXSSIJSON(w, http.StatusPreconditionFailed, map[string]string{
				"error": "TooManyAssignmentsError: maximum concurrent runtimes reached",
			})
			return
		}

		spec, _ := models.GetHardwareSpec(normAcc)
		shortID := strings.ReplaceAll(uuid.NewString()[:10], "-", "")
		ep := fmt.Sprintf("gpu-%s-s-%s", strings.ToLower(string(normAcc)), shortID)
		proxyTok := "ChAKDm_mock_" + shortID

		machineShape := 0
		if shapeParam == "hm" || spec.AlwaysHighMem {
			machineShape = 1
		}
		now := time.Now().UTC()
		vm := &mockVM{
			Endpoint:        ep,
			NotebookHash:    nbh,
			AccountEmail:    acct.Email,
			Variant:         spec.Variant.WireCode(),
			MachineShape:    machineShape,
			Accelerator:     normAcc,
			BurnRateCUHr:    spec.DefaultBurnRateCUHr,
			ProxyToken:      proxyTok,
			CreatedAt:       now,
			LastKeepaliveAt: now,
			kernels:         make(map[string]*mockKernel),
			sessions:        make(map[string]map[string]any),
			files: map[string][]byte{
				"content/sample_data/README.md": []byte("ColabHive Mock Runtime /content/sample_data\n"),
			},
		}
		s.vmsByEndpoint[ep] = vm
		if nbh != "" {
			s.nbhToEndpoint[nbh] = ep
		}
		s.proxyTokenToVM[proxyTok] = vm
		s.mu.Unlock()

		writeColabXSSIJSON(w, http.StatusOK, map[string]any{
			"accelerator": spec.WireAccelerator,
			"endpoint":    ep,
			"variant":     vm.Variant,
			"runtimeProxyInfo": map[string]any{
				"token":                 proxyTok,
				"tokenExpiresInSeconds": 3600,
				"url":                   s.URL + "/runtime/" + ep,
			},
		})

	default:
		writeColabXSSIJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (s *Server) handleTunnelUnassign(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticateBearer(r); !ok {
		writeColabXSSIJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	ep := strings.TrimPrefix(r.URL.Path, "/tun/m/unassign/")
	ep = strings.Trim(ep, "/")

	switch r.Method {
	case http.MethodGet:
		xsrf := "xsrf_unassign_" + strings.ReplaceAll(uuid.NewString()[:12], "-", "")
		s.mu.Lock()
		s.xsrfTokens[xsrf] = "unassign:" + ep
		s.mu.Unlock()
		writeColabXSSIJSON(w, http.StatusOK, map[string]string{"token": xsrf})

	case http.MethodPost:
		xsrf := strings.TrimSpace(r.Header.Get("X-Goog-Colab-Token"))
		s.mu.Lock()
		expected, okX := s.xsrfTokens[xsrf]
		if !okX || expected != "unassign:"+ep {
			s.mu.Unlock()
			writeColabXSSIJSON(w, http.StatusForbidden, map[string]string{"error": "invalid X-Goog-Colab-Token"})
			return
		}
		delete(s.xsrfTokens, xsrf)
		if vm, okVM := s.vmsByEndpoint[ep]; okVM {
			delete(s.nbhToEndpoint, vm.NotebookHash)
			delete(s.proxyTokenToVM, vm.ProxyToken)
			delete(s.vmsByEndpoint, ep)
		}
		s.mu.Unlock()
		writeColabXSSIJSON(w, http.StatusOK, map[string]any{})

	default:
		writeColabXSSIJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (s *Server) handleTunnelCatchAll(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authenticateBearer(r); !ok {
		writeColabXSSIJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/tun/m/")
	// Match `<endpoint>/keep-alive/`
	if strings.Contains(rel, "/keep-alive") {
		ep := strings.Split(rel, "/")[0]
		if r.Header.Get("X-Colab-Tunnel") != "Google" {
			writeColabXSSIJSON(w, http.StatusBadRequest, map[string]string{"error": "missing X-Colab-Tunnel: Google header"})
			return
		}
		s.mu.Lock()
		vm, ok := s.vmsByEndpoint[ep]
		if ok {
			vm.LastKeepaliveAt = time.Now().UTC()
		}
		s.mu.Unlock()
		if !ok {
			writeColabXSSIJSON(w, http.StatusNotFound, map[string]string{"error": "endpoint not found"})
			return
		}
		writeColabXSSIJSON(w, http.StatusOK, map[string]any{"keepAlive": true})
		return
	}
	writeColabXSSIJSON(w, http.StatusNotFound, map[string]string{"error": "unknown tunnel route"})
}
