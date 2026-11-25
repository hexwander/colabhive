package pool

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/vault"
)

const (
	// PlaintextKeyringSection is `keyrings.alt.escape("google-colab-cli")`.
	PlaintextKeyringSection = "google_2Dcolab_2Dcli"
	// PlaintextKeyringOption is lowercased `keyrings.alt.escape("oauth_token")`.
	PlaintextKeyringOption = "oauth_5ftoken"
)

// ParsedOAuthCredential represents normalized OAuth2 credentials extracted from any local/portable format.
type ParsedOAuthCredential struct {
	Email            string
	Alias            string
	SubscriptionTier models.SubscriptionTier
	Groups           []string
	Tags             []string
	ProxyURL         string
	ClientID         string
	ClientSecret     string
	TokenURI         string
	RefreshToken     string
	AccessToken      string
	Scopes           []string
	Expiry           *time.Time
	Guardrails       models.ContributorGuardrails
}

// EscapeKeyringIdentifier implements Python `keyrings.alt.escape.escape()` (`[A-Za-z0-9]` verbatim,
// every other UTF-8 byte encoded as `_%02X`, e.g. `google-colab-cli` -> `google_2Dcolab_2Dcli`).
func EscapeKeyringIdentifier(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "_%02X", ch)
		}
	}
	return b.String()
}

// UnescapeKeyringIdentifier reverses `keyrings.alt.escape` (`_%02X` / `_%02x` -> raw byte).
func UnescapeKeyringIdentifier(escaped string) string {
	out := make([]byte, 0, len(escaped))
	for i := 0; i < len(escaped); i++ {
		if escaped[i] == '_' && i+2 < len(escaped) {
			if b, err := strconv.ParseUint(escaped[i+1:i+3], 16, 8); err == nil {
				out = append(out, byte(b))
				i += 2
				continue
			}
		}
		out = append(out, escaped[i])
	}
	return string(out)
}

type colabTokenJSONWire struct {
	Token        string   `json:"token"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	IDToken      string   `json:"id_token"`
	TokenURI     string   `json:"token_uri"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	Account      string   `json:"account"`
	Email        string   `json:"email"`
	Expiry       string   `json:"expiry"`
	Type         string   `json:"type"`
}

// ParsePlaintextKeyringINI decodes a `keyrings.alt.file.PlaintextKeyring` INI file (`keyring_pass.cfg`)
// containing `[google_2Dcolab_2Dcli]` -> `oauth_5ftoken = \n\t<base64_json>`.
func ParsePlaintextKeyringINI(iniText string) (*ParsedOAuthCredential, error) {
	scanner := bufio.NewScanner(strings.NewReader(iniText))
	inTargetSection := false
	inTargetOption := false
	var b64Buf strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inTargetOption {
				break
			}
			secName := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			inTargetSection = strings.EqualFold(secName, PlaintextKeyringSection) ||
				strings.EqualFold(UnescapeKeyringIdentifier(secName), "google-colab-cli")
			inTargetOption = false
			continue
		}

		if !inTargetSection {
			continue
		}

		isContinuation := len(line) > 0 && (line[0] == '\t' || line[0] == ' ')
		if inTargetOption {
			if isContinuation {
				if trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, ";") {
					b64Buf.WriteString(trimmed)
				}
				continue
			}
			if trimmed == "" {
				continue
			}
			break
		}

		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}

		sepIdx := strings.IndexAny(line, "=:")
		if sepIdx == -1 {
			continue
		}
		optKey := strings.TrimSpace(line[:sepIdx])
		optVal := strings.TrimSpace(line[sepIdx+1:])
		if strings.EqualFold(optKey, PlaintextKeyringOption) ||
			strings.EqualFold(UnescapeKeyringIdentifier(optKey), "oauth_token") {
			inTargetOption = true
			if optVal != "" {
				b64Buf.WriteString(optVal)
			}
		}
	}

	rawB64 := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			return -1
		}
		return r
	}, b64Buf.String())

	if rawB64 == "" {
		return nil, errors.New("pool: [google_2Dcolab_2Dcli] oauth_5ftoken not found in keyring INI")
	}

	decoded, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		decoded, err = base64.RawStdEncoding.DecodeString(rawB64)
		if err != nil {
			return nil, fmt.Errorf("pool: decode keyring oauth_5ftoken base64: %w", err)
		}
	}
	return parseColabTokenWireJSON(decoded)
}

// FormatPlaintextKeyringINI encodes an OAuth credential into exact `keyrings.alt.file.PlaintextKeyring`
// wire format compatible with `google-colab-cli v0.8.0`.
func FormatPlaintextKeyringINI(cred ParsedOAuthCredential) (string, error) {
	clientID := cred.ClientID
	if clientID == "" {
		clientID = models.OfficialColabOAuthClientID
	}
	clientSecret := cred.ClientSecret
	if clientSecret == "" {
		clientSecret = models.OfficialColabOAuthClientSecret
	}
	tokenURI := cred.TokenURI
	if tokenURI == "" {
		tokenURI = models.OfficialGoogleTokenURI
	}
	scopes := cred.Scopes
	if len(scopes) == 0 {
		scopes = models.OwnerOAuthScopes
	}

	payload := map[string]any{
		"token":           cred.AccessToken,
		"refresh_token":   cred.RefreshToken,
		"token_uri":       tokenURI,
		"client_id":       clientID,
		"client_secret":   clientSecret,
		"scopes":          scopes,
		"universe_domain": "googleapis.com",
		"account":         cred.Email,
	}
	if cred.Expiry != nil {
		payload["expiry"] = cred.Expiry.UTC().Format(time.RFC3339Nano)
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	b64 := base64.StdEncoding.EncodeToString(jsonBytes)

	var out strings.Builder
	fmt.Fprintf(&out, "[%s]\n%s = \n", PlaintextKeyringSection, PlaintextKeyringOption)
	for i := 0; i < len(b64); i += 76 {
		end := i + 76
		if end > len(b64) {
			end = len(b64)
		}
		fmt.Fprintf(&out, "\t%s\n", b64[i:end])
	}
	return out.String(), nil
}

func parseColabTokenWireJSON(raw []byte) (*ParsedOAuthCredential, error) {
	var w colabTokenJSONWire
	if err := json.Unmarshal(bytes.TrimSpace(raw), &w); err != nil {
		return nil, fmt.Errorf("pool: invalid credential JSON: %w", err)
	}
	access := strings.TrimSpace(w.Token)
	if access == "" {
		access = strings.TrimSpace(w.AccessToken)
	}
	refresh := strings.TrimSpace(w.RefreshToken)
	if refresh == "" && access == "" {
		return nil, errors.New("pool: credential JSON contains neither refresh_token nor access token")
	}

	email := strings.TrimSpace(w.Email)
	if email == "" && strings.Contains(w.Account, "@") {
		email = strings.TrimSpace(w.Account)
	}
	alias := ""
	if (email == "" || alias == "") && strings.TrimSpace(w.IDToken) != "" {
		if jwtEmail, jwtName := parseOIDCClaimsOffline(w.IDToken); jwtEmail != "" || jwtName != "" {
			if email == "" {
				email = jwtEmail
			}
			if alias == "" {
				alias = jwtName
			}
		}
	}
	clientID := strings.TrimSpace(w.ClientID)
	if clientID == "" {
		clientID = models.OfficialColabOAuthClientID
	}
	clientSecret := strings.TrimSpace(w.ClientSecret)
	if clientSecret == "" {
		clientSecret = models.OfficialColabOAuthClientSecret
	}
	tokenURI := strings.TrimSpace(w.TokenURI)
	if tokenURI == "" {
		tokenURI = models.OfficialGoogleTokenURI
	}

	var expiry *time.Time
	if w.Expiry != "" {
		if t, err := time.Parse(time.RFC3339Nano, w.Expiry); err == nil {
			utc := t.UTC()
			expiry = &utc
		}
	}

	return &ParsedOAuthCredential{
		Email:        email,
		Alias:        alias,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		TokenURI:     tokenURI,
		RefreshToken: refresh,
		AccessToken:  access,
		Scopes:       w.Scopes,
		Expiry:       expiry,
	}, nil
}

func parseOIDCClaimsOffline(idToken string) (email string, name string) {
	parts := strings.Split(strings.TrimSpace(idToken), ".")
	if len(parts) < 2 {
		return "", ""
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims map[string]any
	if json.Unmarshal(payloadBytes, &claims) != nil {
		return "", ""
	}
	if v, ok := claims["email"].(string); ok {
		email = strings.TrimSpace(v)
	}
	if v, ok := claims["name"].(string); ok {
		name = strings.TrimSpace(v)
	}
	return email, name
}

func formatLocalSourceLabel(env string, sourcePath string) string {
	if !strings.EqualFold(strings.TrimSpace(env), "wsl2") {
		return ""
	}
	norm := strings.ReplaceAll(strings.TrimSpace(sourcePath), "/", `\`)
	lower := strings.ToLower(norm)
	for _, prefix := range []string{`\\wsl.localhost\`, `\\wsl$\`} {
		if strings.HasPrefix(lower, prefix) {
			rest := norm[len(prefix):]
			segs := strings.Split(rest, `\`)
			distro := ""
			if len(segs) >= 1 {
				distro = strings.TrimSpace(segs[0])
			}
			user := ""
			if len(segs) >= 3 && strings.EqualFold(segs[1], "home") {
				user = strings.TrimSpace(segs[2])
			}
			if distro != "" && user != "" {
				return fmt.Sprintf("WSL2 · %s (%s)", distro, user)
			}
			if distro != "" {
				return "WSL2 · " + distro
			}
		}
	}
	return "WSL2"
}

// ScanLocalCredentials inspects standard read-only local and WSL2 credential locations ONLY when
// explicitly invoked by the user/API, returning account identity and status without ever logging raw secrets.
func (s *Service) ScanLocalCredentials(ctx context.Context) ([]models.DiscoveredLocalCredential, error) {
	candidates := s.cfg.LocalScanRoots
	if len(candidates) == 0 {
		candidates = buildDefaultScanCandidates()
	}

	// Load existing pool accounts once for instant offline refresh-token & email matching.
	byRefreshToken := make(map[string]*models.Account)
	byEmailLower := make(map[string]*models.Account)
	if s.st != nil {
		if existingAccounts, lErr := s.st.ListAccounts(ctx); lErr == nil {
			for _, acc := range existingAccounts {
				if acc == nil {
					continue
				}
				if em := strings.ToLower(strings.TrimSpace(acc.Email)); em != "" {
					byEmailLower[em] = acc
				}
				if s.cipher != nil && acc.EncryptedRefreshToken != "" {
					if dec, dErr := s.cipher.DecryptString(acc.EncryptedRefreshToken); dErr == nil && strings.TrimSpace(dec) != "" {
						byRefreshToken[strings.TrimSpace(dec)] = acc
					}
				}
			}
		}
	}

	// Pick an optional fallback proxy URL if user configured proxies in ColabHive.
	fallbackProxyURL := ""
	if s.st != nil {
		if proxies, pErr := s.st.ListProxies(ctx); pErr == nil {
			for _, p := range proxies {
				if p != nil && strings.TrimSpace(p.URL) != "" {
					if p.Healthy || fallbackProxyURL == "" {
						fallbackProxyURL = strings.TrimSpace(p.URL)
						if p.Healthy {
							break
						}
					}
				}
			}
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}
	scanBudgetCtx, scanCancel := context.WithTimeout(ctx, 8*time.Second)
	defer scanCancel()

	results := make([]models.DiscoveredLocalCredential, 0)
	records := make(map[string]discoveredRecord)

	for _, cand := range candidates {
		if ctx.Err() != nil {
			break
		}
		if strings.TrimSpace(cand.SourcePath) == "" {
			continue
		}
		info, err := os.Stat(cand.SourcePath)
		if err != nil || info.IsDir() || info.Size() == 0 || info.Size() > 2*1024*1024 {
			continue
		}

		// If companion keyringrc.cfg is specified and present, verify it doesn't disable plaintext keyring.
		if cand.KeyringRC != "" {
			if rcBytes, rcErr := os.ReadFile(cand.KeyringRC); rcErr == nil {
				rcStr := string(rcBytes)
				if strings.Contains(rcStr, "default-keyring") && !strings.Contains(rcStr, "PlaintextKeyring") {
					// Still allow parsing keyring_pass.cfg if it actually contains [google_2Dcolab_2Dcli]
					_ = rcStr
				}
			}
		}

		rawBytes, err := os.ReadFile(cand.SourcePath)
		if err != nil {
			continue
		}

		var parsed *ParsedOAuthCredential
		switch cand.SourceType {
		case "keyring_plaintext":
			parsed, err = ParsePlaintextKeyringINI(string(rawBytes))
		case "colab_token_json", "gcloud_adc":
			parsed, err = parseColabTokenWireJSON(rawBytes)
		default:
			continue
		}
		if err != nil || parsed == nil {
			continue
		}

		email := strings.TrimSpace(parsed.Email)
		displayName := strings.TrimSpace(parsed.Alias)
		resolvedAccess := parsed.AccessToken
		resolvedExp := parsed.Expiry
		alreadyInPool := false
		existingAccountID := ""
		status := "ready"

		// Tier 1: Match against already-imported pool accounts by refresh_token (0ms, works offline).
		if parsed.RefreshToken != "" {
			if matched, ok := byRefreshToken[strings.TrimSpace(parsed.RefreshToken)]; ok && matched != nil {
				if email == "" {
					email = strings.TrimSpace(matched.Email)
				}
				if displayName == "" {
					displayName = strings.TrimSpace(matched.Alias)
				}
				alreadyInPool = true
				existingAccountID = matched.ID
				status = "in_pool"
			}
		}

		// Tier 2: Check if in-file email already exists in the pool.
		if !alreadyInPool && email != "" {
			if matched, ok := byEmailLower[strings.ToLower(email)]; ok && matched != nil {
				if displayName == "" {
					displayName = strings.TrimSpace(matched.Alias)
				}
				alreadyInPool = true
				existingAccountID = matched.ID
				status = "in_pool"
			}
		}

		// Tier 3: Resolve real Google account email via cached/live OAuth identity lookup when file omits email.
		if email == "" {
			rEmail, rName, rAcc, rExp, invalidGrant := s.resolveScanIdentityCached(scanBudgetCtx, *parsed, fallbackProxyURL)
			if rEmail != "" {
				email = rEmail
				if displayName == "" {
					displayName = rName
				}
				if rAcc != "" {
					resolvedAccess = rAcc
					resolvedExp = rExp
				}
				if matched, ok := byEmailLower[strings.ToLower(email)]; ok && matched != nil {
					alreadyInPool = true
					existingAccountID = matched.ID
					status = "in_pool"
				} else {
					status = "ready"
				}
			} else if invalidGrant {
				status = "expired"
			} else {
				status = "unverified"
			}
		}

		sum := sha256.Sum256([]byte(cand.SourceType + "|" + cand.SourcePath))
		discID := "disc_" + hex.EncodeToString(sum[:8])

		secretSample := parsed.RefreshToken
		if secretSample == "" {
			secretSample = parsed.AccessToken
		}

		meta := models.DiscoveredLocalCredential{
			ID:                discID,
			SourceType:        cand.SourceType,
			SourcePath:        cand.SourcePath,
			Environment:       cand.Environment,
			Email:             email,
			EmailPreview:      MaskEmail(email),
			DisplayName:       displayName,
			SourceLabel:       formatLocalSourceLabel(cand.Environment, cand.SourcePath),
			AlreadyInPool:     alreadyInPool,
			ExistingAccountID: existingAccountID,
			Status:            status,
			TokenPreview:      maskTokenPreview(secretSample),
			Scopes:            parsed.Scopes,
		}
		results = append(results, meta)
		records[discID] = discoveredRecord{
			meta:        meta,
			sourcePath:  cand.SourcePath,
			sourceType:  cand.SourceType,
			resolvedEm:  email,
			resolvedNm:  displayName,
			accessToken: resolvedAccess,
			expiry:      resolvedExp,
		}
	}

	s.scanMu.Lock()
	for k, v := range records {
		s.discovered[k] = v
	}
	s.scanMu.Unlock()

	return results, nil
}

const scanIdentityCacheTTL = 5 * time.Minute

func (s *Service) resolveScanIdentityCached(
	ctx context.Context,
	cred ParsedOAuthCredential,
	fallbackProxyURL string,
) (email, displayName, accessToken string, expiry *time.Time, invalidGrant bool) {
	keySeed := strings.TrimSpace(cred.RefreshToken) + "|" + strings.TrimSpace(cred.AccessToken)
	keySum := sha256.Sum256([]byte(keySeed))
	cacheKey := hex.EncodeToString(keySum[:12])

	s.scanMu.RLock()
	if s.scanIdentityCache != nil {
		if entry, ok := s.scanIdentityCache[cacheKey]; ok && s.now().Sub(entry.cachedAt) < scanIdentityCacheTTL {
			s.scanMu.RUnlock()
			return entry.email, entry.displayName, entry.accessToken, entry.expiry, entry.invalidGrant
		}
	}
	s.scanMu.RUnlock()

	if ctx.Err() != nil {
		return "", "", "", nil, false
	}

	em, tok, exp, nm, inv := s.tryResolveImportedIdentityWithStatus(ctx, cred, "")
	if em == "" && !inv && fallbackProxyURL != "" && ctx.Err() == nil {
		em, tok, exp, nm, inv = s.tryResolveImportedIdentityWithStatus(ctx, cred, fallbackProxyURL)
	}

	if em != "" || inv {
		s.scanMu.Lock()
		if s.scanIdentityCache == nil {
			s.scanIdentityCache = make(map[string]cachedScanIdentity)
		}
		s.scanIdentityCache[cacheKey] = cachedScanIdentity{
			email:        em,
			displayName:  nm,
			accessToken:  tok,
			expiry:       exp,
			invalidGrant: inv,
			cachedAt:     s.now(),
		}
		s.scanMu.Unlock()
	}
	return em, nm, tok, exp, inv
}

func buildDefaultScanCandidates() []LocalScanCandidate {
	var out []LocalScanCandidate
	home, _ := os.UserHomeDir()

	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			out = append(out, LocalScanCandidate{
				SourceType:  "keyring_plaintext",
				SourcePath:  filepath.Join(localAppData, "Python Keyring", "keyring_pass.cfg"),
				KeyringRC:   filepath.Join(localAppData, "Python Keyring", "keyringrc.cfg"),
				Environment: "host",
			})
		}
		if appData := os.Getenv("APPDATA"); appData != "" {
			out = append(out, LocalScanCandidate{
				SourceType:  "gcloud_adc",
				SourcePath:  filepath.Join(appData, "gcloud", "application_default_credentials.json"),
				Environment: "host",
			})
		}
		if home != "" {
			out = append(out, LocalScanCandidate{
				SourceType:  "colab_token_json",
				SourcePath:  filepath.Join(home, ".config", "colab-cli", "token.json"),
				Environment: "host",
			})
		}

		// WSL2 cross-boundary discovery (`\\wsl.localhost\<Distro>\home\<user>\...`)
		distros := discoverWSL2Distros()
		for _, distro := range distros {
			homeRoot := `\\wsl.localhost\` + distro + `\home`
			entries, err := os.ReadDir(homeRoot)
			if err != nil {
				continue
			}
			for _, u := range entries {
				if !u.IsDir() {
					continue
				}
				uHome := filepath.Join(homeRoot, u.Name())
				out = append(out,
					LocalScanCandidate{
						SourceType:  "keyring_plaintext",
						SourcePath:  filepath.Join(uHome, ".local", "share", "python_keyring", "keyring_pass.cfg"),
						KeyringRC:   filepath.Join(uHome, ".config", "python_keyring", "keyringrc.cfg"),
						Environment: "wsl2",
					},
					LocalScanCandidate{
						SourceType:  "colab_token_json",
						SourcePath:  filepath.Join(uHome, ".config", "colab-cli", "token.json"),
						Environment: "wsl2",
					},
					LocalScanCandidate{
						SourceType:  "gcloud_adc",
						SourcePath:  filepath.Join(uHome, ".config", "gcloud", "application_default_credentials.json"),
						Environment: "wsl2",
					},
				)
			}
		}
	} else {
		xdgData := os.Getenv("XDG_DATA_HOME")
		if xdgData == "" && home != "" {
			xdgData = filepath.Join(home, ".local", "share")
		}
		xdgCfg := os.Getenv("XDG_CONFIG_HOME")
		if xdgCfg == "" && home != "" {
			xdgCfg = filepath.Join(home, ".config")
		}
		if xdgData != "" {
			rc := ""
			if xdgCfg != "" {
				rc = filepath.Join(xdgCfg, "python_keyring", "keyringrc.cfg")
			}
			out = append(out, LocalScanCandidate{
				SourceType:  "keyring_plaintext",
				SourcePath:  filepath.Join(xdgData, "python_keyring", "keyring_pass.cfg"),
				KeyringRC:   rc,
				Environment: "host",
			})
		}
		if xdgCfg != "" {
			out = append(out,
				LocalScanCandidate{
					SourceType:  "colab_token_json",
					SourcePath:  filepath.Join(xdgCfg, "colab-cli", "token.json"),
					Environment: "host",
				},
				LocalScanCandidate{
					SourceType:  "gcloud_adc",
					SourcePath:  filepath.Join(xdgCfg, "gcloud", "application_default_credentials.json"),
					Environment: "host",
				},
			)
		}
	}

	if gac := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); gac != "" {
		out = append(out, LocalScanCandidate{
			SourceType:  "gcloud_adc",
			SourcePath:  gac,
			Environment: "host",
		})
	}
	return out
}

func discoverWSL2Distros() []string {
	known := []string{"Ubuntu", "Ubuntu-24.04", "Ubuntu-22.04", "Debian"}
	entries, err := os.ReadDir(`\\wsl.localhost`)
	if err != nil || len(entries) == 0 {
		return known
	}
	seen := make(map[string]bool)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			seen[e.Name()] = true
			out = append(out, e.Name())
		}
	}
	for _, k := range known {
		if !seen[k] {
			out = append(out, k)
		}
	}
	return out
}

// ImportCredentials imports accounts from `keyring_ini`, `adc_json`, `raw_json`, `chvault`, or `discovered` IDs.
func (s *Service) ImportCredentials(ctx context.Context, req models.CredentialImportRequest) ([]*models.Account, error) {
	format := strings.ToLower(strings.TrimSpace(req.Format))
	strategy := req.ConflictStrategy
	if strategy == "" {
		strategy = models.ConflictUpdate
	}

	var parsedList []ParsedOAuthCredential

	switch format {
	case "discovered":
		if len(req.DiscoveredIDs) == 0 {
			return nil, errors.New("pool: no discovered_ids provided for discovered import")
		}
		for _, id := range req.DiscoveredIDs {
			s.scanMu.RLock()
			rec, ok := s.discovered[id]
			s.scanMu.RUnlock()
			if !ok {
				return nil, fmt.Errorf("pool: unknown discovered credential id %q (run ScanLocalCredentials first)", id)
			}
			rawBytes, err := os.ReadFile(rec.sourcePath)
			if err != nil {
				return nil, fmt.Errorf("pool: read discovered file: %w", err)
			}
			var cred *ParsedOAuthCredential
			if rec.sourceType == "keyring_plaintext" {
				cred, err = ParsePlaintextKeyringINI(string(rawBytes))
			} else {
				cred, err = parseColabTokenWireJSON(rawBytes)
			}
			if err != nil {
				return nil, err
			}
			if cred.Email == "" && rec.resolvedEm != "" {
				cred.Email = rec.resolvedEm
			}
			if cred.Alias == "" && rec.resolvedNm != "" {
				cred.Alias = rec.resolvedNm
			}
			if rec.accessToken != "" && (cred.AccessToken == "" || cred.Expiry == nil || cred.Expiry.Before(s.now().Add(tokenRefreshMargin))) {
				cred.AccessToken = rec.accessToken
				if rec.expiry != nil {
					cred.Expiry = rec.expiry
				}
			}
			parsedList = append(parsedList, *cred)
		}

	case "keyring_ini", "keyring_plaintext":
		cred, err := ParsePlaintextKeyringINI(req.RawPayload)
		if err != nil {
			return nil, err
		}
		parsedList = append(parsedList, *cred)

	case "adc_json", "gcloud_adc":
		cred, err := parseColabTokenWireJSON([]byte(req.RawPayload))
		if err != nil {
			return nil, err
		}
		parsedList = append(parsedList, *cred)

	case "chvault":
		bundle, err := vault.UnpackBundle([]byte(req.RawPayload), req.Passphrase)
		if err != nil {
			return nil, err
		}
		for i := range bundle.Proxies {
			p := bundle.Proxies[i]
			if p.URL != "" {
				_, _ = s.SaveProxy(ctx, &p)
			}
		}
		for _, entry := range bundle.Accounts {
			parsedList = append(parsedList, portableEntryToParsed(entry))
		}

	case "raw_json", "":
		raw := bytes.TrimSpace([]byte(req.RawPayload))
		if len(raw) == 0 {
			return nil, errors.New("pool: empty raw_payload for credential import")
		}
		// Auto-detect bundle vs array vs single token JSON vs INI
		if raw[0] == '[' && !bytes.Contains(raw[:minInt(len(raw), 40)], []byte("{")) {
			cred, err := ParsePlaintextKeyringINI(req.RawPayload)
			if err != nil {
				return nil, err
			}
			parsedList = append(parsedList, *cred)
		} else if raw[0] == '[' {
			var arr []json.RawMessage
			if err := json.Unmarshal(raw, &arr); err != nil {
				return nil, fmt.Errorf("pool: invalid JSON credential array: %w", err)
			}
			for _, item := range arr {
				cred, err := parseColabTokenWireJSON(item)
				if err != nil {
					return nil, err
				}
				parsedList = append(parsedList, *cred)
			}
		} else {
			var probe struct {
				Schema    string `json:"schema"`
				Encrypted bool   `json:"encrypted"`
			}
			if json.Unmarshal(raw, &probe) == nil && (probe.Schema == vault.BundleSchema || probe.Encrypted) {
				bundle, err := vault.UnpackBundle(raw, req.Passphrase)
				if err != nil {
					return nil, err
				}
				for i := range bundle.Proxies {
					p := bundle.Proxies[i]
					if p.URL != "" {
						_, _ = s.SaveProxy(ctx, &p)
					}
				}
				for _, entry := range bundle.Accounts {
					parsedList = append(parsedList, portableEntryToParsed(entry))
				}
			} else {
				cred, err := parseColabTokenWireJSON(raw)
				if err != nil {
					return nil, err
				}
				parsedList = append(parsedList, *cred)
			}
		}

	default:
		return nil, fmt.Errorf("pool: unsupported credential import format %q", req.Format)
	}

	imported := make([]*models.Account, 0, len(parsedList))
	for _, cred := range parsedList {
		acc, skipped, err := s.upsertImportedCredential(ctx, cred, req.DefaultGroups, req.DefaultProxyID, strategy)
		if err != nil {
			return nil, err
		}
		if !skipped && acc != nil {
			imported = append(imported, acc)
		}
	}

	if summary, err := s.GetQuotaSummary(ctx); err == nil {
		s.publish(models.WSEventQuotaUpdated, summary)
	}
	return imported, nil
}

func portableEntryToParsed(entry models.PortableAccountEntry) ParsedOAuthCredential {
	return ParsedOAuthCredential{
		Email:            entry.Email,
		Alias:            entry.Alias,
		SubscriptionTier: entry.SubscriptionTier,
		Groups:           entry.Groups,
		Tags:             entry.Tags,
		ProxyURL:         entry.ProxyURL,
		ClientID:         entry.OAuth.ClientID,
		ClientSecret:     entry.OAuth.ClientSecret,
		TokenURI:         entry.OAuth.TokenURI,
		RefreshToken:     entry.OAuth.RefreshToken,
		AccessToken:      entry.OAuth.AccessToken,
		Scopes:           entry.OAuth.Scopes,
		Guardrails:       entry.Guardrails,
	}
}

func (s *Service) upsertImportedCredential(
	ctx context.Context,
	cred ParsedOAuthCredential,
	defaultGroups []string,
	defaultProxyID string,
	strategy models.ImportConflictStrategy,
) (*models.Account, bool, error) {
	proxyID := strings.TrimSpace(defaultProxyID)
	proxyURL := strings.TrimSpace(cred.ProxyURL)
	if proxyURL != "" {
		if pid, err := s.ensureProxyForURL(ctx, proxyURL, ""); err == nil {
			proxyID = pid
		}
	} else if proxyID != "" {
		if p, err := s.st.GetProxy(ctx, proxyID); err == nil && p != nil {
			proxyURL = p.URL
		}
	}

	email := strings.TrimSpace(cred.Email)
	accessToken := cred.AccessToken
	var tokenExpiry *time.Time
	if cred.Expiry != nil {
		tokenExpiry = cred.Expiry
	}

	// If email is unknown (common in colab-cli keyring & gcloud ADC), attempt live refresh + userinfo lookup.
	if email == "" {
		resolvedEmail, resolvedAccess, exp, resolvedName := s.tryResolveImportedIdentity(ctx, cred, proxyURL)
		if resolvedEmail != "" {
			email = resolvedEmail
		}
		if resolvedAccess != "" {
			accessToken = resolvedAccess
			tokenExpiry = exp
		}
		if cred.Alias == "" && resolvedName != "" {
			cred.Alias = resolvedName
		}
	}
	if email == "" {
		// Deterministic offline fallback email when Google endpoints are unreachable.
		seed := cred.RefreshToken
		if seed == "" {
			seed = cred.AccessToken
		}
		sum := sha256.Sum256([]byte(seed))
		email = "imported-" + hex.EncodeToString(sum[:4]) + "@colab.local"
	}

	existing, _ := s.st.GetAccountByEmail(ctx, email)
	if existing != nil && strategy == models.ConflictSkip {
		return existing, true, nil
	}

	encRef, err := s.cipher.EncryptString(cred.RefreshToken)
	if err != nil {
		return nil, false, err
	}
	encAcc, err := s.cipher.EncryptString(accessToken)
	if err != nil {
		return nil, false, err
	}

	now := s.now()
	var acc *models.Account
	if existing != nil {
		acc = existing
		if strategy == models.ConflictMergeGroups {
			acc.Groups = uniqueStrings(append(acc.Groups, append(cred.Groups, defaultGroups...)...))
			acc.Tags = uniqueStrings(append(acc.Tags, cred.Tags...))
		} else {
			if len(cred.Groups) > 0 || len(defaultGroups) > 0 {
				acc.Groups = uniqueStrings(append(cred.Groups, defaultGroups...))
			}
			if len(cred.Tags) > 0 {
				acc.Tags = uniqueStrings(cred.Tags)
			}
		}
	} else {
		tier := cred.SubscriptionTier
		if tier == "" {
			tier = models.TierFree
		}
		acc = &models.Account{
			ID:               models.NewPrefixedID("acc_"),
			Email:            email,
			Alias:            cred.Alias,
			SubscriptionTier: tier,
			Health:           models.HealthHealthy,
			Enabled:          true,
			Priority:         10,
			Groups:           uniqueStrings(append(cred.Groups, defaultGroups...)),
			Tags:             uniqueStrings(cred.Tags),
			Guardrails:       cred.Guardrails,
			CreatedAt:        now,
		}
	}

	if cred.Alias != "" {
		acc.Alias = cred.Alias
	}
	if proxyID != "" {
		acc.ProxyID = proxyID
	}
	if len(cred.Scopes) > 0 {
		acc.Scopes = cred.Scopes
	} else if len(acc.Scopes) == 0 {
		acc.Scopes = append([]string(nil), models.OwnerOAuthScopes...)
	}
	if encRef != "" {
		acc.EncryptedRefreshToken = encRef
	}
	if encAcc != "" {
		acc.EncryptedAccessToken = encAcc
	}
	acc.OAuthClientID = firstNonEmpty(cred.ClientID, models.OfficialColabOAuthClientID)
	acc.OAuthClientSecret = firstNonEmpty(cred.ClientSecret, models.OfficialColabOAuthClientSecret)
	acc.TokenExpiresAt = tokenExpiry
	acc.UpdatedAt = now

	if err := s.st.SaveAccount(ctx, acc); err != nil {
		return nil, false, err
	}

	if refreshed, qErr := s.RefreshAccountQuota(ctx, acc.ID); qErr == nil && refreshed != nil {
		acc = refreshed
	} else {
		s.recomputeAccountDerivedFields(ctx, acc)
		_ = s.st.SaveAccount(ctx, acc)
	}
	s.publish(models.WSEventAccountUpdated, acc)
	return acc, false, nil
}

func (s *Service) tryResolveImportedIdentity(
	ctx context.Context,
	cred ParsedOAuthCredential,
	proxyURL string,
) (email string, accessToken string, expiry *time.Time, displayName string) {
	em, tok, exp, nm, _ := s.tryResolveImportedIdentityWithStatus(ctx, cred, proxyURL)
	return em, tok, exp, nm
}

func (s *Service) tryResolveImportedIdentityWithStatus(
	ctx context.Context,
	cred ParsedOAuthCredential,
	proxyURL string,
) (email string, accessToken string, expiry *time.Time, displayName string, invalidGrant bool) {
	rCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	httpClient, err := s.transport.HTTPClient(proxyURL, 4*time.Second)
	if err != nil {
		return "", "", nil, "", false
	}

	tok := cred.AccessToken
	var exp *time.Time
	needsRefresh := tok == "" || cred.Expiry == nil || cred.Expiry.Before(s.now().Add(tokenRefreshMargin))

	if !needsRefresh && tok != "" {
		if em, nm, _, _ := s.fetchGoogleIdentity(rCtx, httpClient, tok, ""); em != "" {
			return em, tok, cred.Expiry, nm, false
		}
	}

	if cred.RefreshToken != "" {
		refreshedTok, expTime, inv := s.refreshRawTokenWithStatus(
			rCtx,
			httpClient,
			firstNonEmpty(cred.ClientID, models.OfficialColabOAuthClientID),
			firstNonEmpty(cred.ClientSecret, models.OfficialColabOAuthClientSecret),
			cred.RefreshToken,
		)
		if inv {
			return "", "", nil, "", true
		}
		if refreshedTok != "" {
			tok = refreshedTok
			exp = expTime
		}
	}
	if tok == "" {
		return "", "", nil, "", false
	}
	em, nm, _, _ := s.fetchGoogleIdentity(rCtx, httpClient, tok, "")
	return em, tok, exp, nm, false
}

func (s *Service) refreshRawTokenWithStatus(
	ctx context.Context,
	httpClient *http.Client,
	clientID string,
	clientSecret string,
	refreshToken string,
) (string, *time.Time, bool) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.GoogleTokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", nil, false
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", nil, false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		low := strings.ToLower(string(body))
		isRevoked := resp.StatusCode == http.StatusUnauthorized ||
			strings.Contains(low, "invalid_grant") ||
			strings.Contains(low, "unauthorized_client")
		return "", nil, isRevoked
	}
	var tok googleTokenResponse
	if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		return "", nil, false
	}
	dur := time.Duration(tok.ExpiresIn) * time.Second
	if dur <= 0 {
		dur = 55 * time.Minute
	}
	exp := s.now().Add(dur)
	return tok.AccessToken, &exp, false
}

// ExportVaultBundle exports owner accounts (never external contributor accounts!) into a
// portable `.chvault` CredentialBundle, optionally encrypted with PBKDF2-HMAC-SHA256 + AES-256-GCM.
func (s *Service) ExportVaultBundle(ctx context.Context, req models.CredentialExportRequest) (*models.CredentialBundle, error) {
	if req.Encrypt && strings.TrimSpace(req.Passphrase) == "" {
		return nil, vault.ErrPassphraseRequired
	}

	allAccts, err := s.st.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}

	allowIDs := make(map[string]bool, len(req.AccountIDs))
	for _, id := range req.AccountIDs {
		allowIDs[id] = true
	}

	entries := make([]models.PortableAccountEntry, 0)
	usedProxyIDs := make(map[string]bool)

	for _, acc := range allAccts {
		// SECURITY INVARIANT: Never export donated contributor tokens out of the vault.
		if acc.IsContributor {
			continue
		}
		if len(allowIDs) > 0 && !allowIDs[acc.ID] {
			continue
		}

		refreshTok, _ := s.cipher.DecryptString(acc.EncryptedRefreshToken)
		accessTok, _ := s.cipher.DecryptString(acc.EncryptedAccessToken)
		if refreshTok == "" && accessTok == "" {
			continue
		}

		proxyURL := ""
		if acc.ProxyID != "" {
			usedProxyIDs[acc.ProxyID] = true
			if p, pErr := s.st.GetProxy(ctx, acc.ProxyID); pErr == nil && p != nil {
				proxyURL = p.URL
			}
		}

		entries = append(entries, models.PortableAccountEntry{
			Email:            acc.Email,
			Alias:            acc.Alias,
			SubscriptionTier: acc.SubscriptionTier,
			Groups:           acc.Groups,
			Tags:             acc.Tags,
			ProxyURL:         proxyURL,
			OAuth: models.PortableAccountOAuth{
				ClientID:     firstNonEmpty(acc.OAuthClientID, models.OfficialColabOAuthClientID),
				ClientSecret: firstNonEmpty(acc.OAuthClientSecret, models.OfficialColabOAuthClientSecret),
				TokenURI:     models.OfficialGoogleTokenURI,
				RefreshToken: refreshTok,
				AccessToken:  accessTok,
				Scopes:       acc.Scopes,
			},
			Guardrails: acc.Guardrails,
		})
	}

	var proxies []models.ProxyConfig
	if req.IncludeProxies {
		if allProxies, pErr := s.st.ListProxies(ctx); pErr == nil {
			for _, p := range allProxies {
				if len(allowIDs) == 0 || usedProxyIDs[p.ID] {
					proxies = append(proxies, *p)
				}
			}
		}
	}

	passphrase := ""
	if req.Encrypt || strings.TrimSpace(req.Passphrase) != "" {
		passphrase = req.Passphrase
	}
	return s.cipher.PackBundle(entries, proxies, passphrase)
}

// MaskEmail masks an email address (`alice.researcher@uni.edu` -> `ali***@uni.edu`).
func MaskEmail(email string) string {
	trimmed := strings.TrimSpace(email)
	if trimmed == "" {
		return ""
	}
	at := strings.IndexByte(trimmed, '@')
	if at <= 0 {
		if len(trimmed) <= 3 {
			return "***"
		}
		return trimmed[:2] + "***"
	}
	local := trimmed[:at]
	domain := trimmed[at:]
	vis := 3
	if len(local)-1 < vis {
		vis = len(local) - 1
	}
	if vis < 1 {
		vis = 1
	}
	return local[:vis] + "***" + domain
}

func maskTokenPreview(tok string) string {
	t := strings.TrimSpace(tok)
	if len(t) == 0 {
		return "***"
	}
	if len(t) <= 8 {
		return t[:2] + "…***"
	}
	return t[:6] + "…***"
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
