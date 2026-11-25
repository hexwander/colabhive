package pool

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

// GeneratePKCES256 generates a RFC 7636 S256 (codeVerifier, codeChallenge) pair.
func GeneratePKCES256() (verifier string, challenge string, err error) {
	raw := make([]byte, 48)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", "", fmt.Errorf("pool: generate pkce verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func generateRandomToken(prefix string, byteLen int) (string, error) {
	raw := make([]byte, byteLen)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// ParseAuthCodeInput extracts an authorization code (and optional state) from either:
//  1. A raw authorization code (`4/0AVGzR1B...`)
//  2. A full Google remote redirect URL (`https://sdk.cloud.google.com/applicationdefaultauthcode.html?state=...&code=4%2F0A...`)
//  3. A localhost callback URL (`http://localhost:54321/?code=4/0A...&state=...`)
//  4. A raw query string snippet containing `code=...`
func ParseAuthCodeInput(rawInput string) (code string, parsedState string, err error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return "", "", errors.New("pool: empty authorization code input")
	}

	extractFromQuery := func(q string) (string, string) {
		vals, err := url.ParseQuery(strings.TrimPrefix(q, "?"))
		if err != nil {
			return "", ""
		}
		return strings.TrimSpace(vals.Get("code")), strings.TrimSpace(vals.Get("state"))
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, uErr := url.Parse(trimmed)
		if uErr == nil {
			c, st := extractFromQuery(u.RawQuery)
			if c == "" && u.Fragment != "" {
				c, st = extractFromQuery(u.Fragment)
			}
			if c != "" {
				return c, st, nil
			}
		}
	}

	if strings.Contains(trimmed, "code=") {
		qPart := trimmed
		if idx := strings.Index(qPart, "?"); idx != -1 {
			qPart = qPart[idx+1:]
		}
		c, st := extractFromQuery(qPart)
		if c != "" {
			return c, st, nil
		}
	}

	// Unescape if user copied URL-encoded `4%2F0A...` directly.
	if strings.Contains(trimmed, "%2F") || strings.Contains(trimmed, "%2f") {
		if unescaped, uErr := url.QueryUnescape(trimmed); uErr == nil && strings.TrimSpace(unescaped) != "" {
			trimmed = strings.TrimSpace(unescaped)
		}
	}

	return trimmed, "", nil
}

// StartOAuth initiates a Google OAuth2 PKCE S256 session using the official Cloud SDK / colab-cli
// remote copy-paste redirect URI (`https://sdk.cloud.google.com/applicationdefaultauthcode.html` + `token_usage=remote`).
func (s *Service) StartOAuth(ctx context.Context, req models.OAuthStartRequest) (*models.OAuthPendingSession, error) {
	verifier, challenge, err := GeneratePKCES256()
	if err != nil {
		return nil, err
	}
	state, err := generateRandomToken("st_", 24)
	if err != nil {
		return nil, fmt.Errorf("pool: generate oauth state: %w", err)
	}

	mode := req.Mode
	if mode == "" {
		mode = models.OAuthModeRemoteCopyPaste
	}

	scopes := append([]string(nil), models.OwnerOAuthScopes...)

	clientID := models.OfficialColabOAuthClientID
	redirectURI := models.OfficialRemoteRedirectURI
	isRemoteCopyPaste := true

	if mode == models.OAuthModeWebCallback && s.cfg.CustomOAuthClientID != "" && s.cfg.WebCallbackRedirectURI != "" {
		clientID = s.cfg.CustomOAuthClientID
		redirectURI = s.cfg.WebCallbackRedirectURI
		isRemoteCopyPaste = false
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", state)
	q.Set("prompt", "consent")
	q.Set("access_type", "offline")
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if isRemoteCopyPaste {
		q.Set("token_usage", "remote")
	}

	authURL := s.cfg.GoogleAuthURI + "?" + q.Encode()
	now := s.now()

	var guardrailsCopy *models.ContributorGuardrails
	if req.Guardrails != nil {
		g := *req.Guardrails
		if req.IsContributor {
			g.BlockDrivePropagation = true
		}
		guardrailsCopy = &g
	}

	pending := &models.OAuthPendingSession{
		State:         state,
		CodeVerifier:  verifier,
		CodeChallenge: challenge,
		AuthURL:       authURL,
		RedirectURI:   redirectURI,
		Mode:          mode,
		Scopes:        scopes,
		Alias:         strings.TrimSpace(req.Alias),
		Notes:         strings.TrimSpace(req.Notes),
		Groups:        uniqueStrings(req.Groups),
		Tags:          uniqueStrings(req.Tags),
		ProxyID:       strings.TrimSpace(req.ProxyID),
		ProxyURL:      strings.TrimSpace(req.ProxyURL),
		IsContributor: req.IsContributor,
		InviteCode:    strings.TrimSpace(req.InviteCode),
		Guardrails:    guardrailsCopy,
		ExpiresAt:     now.Add(oauthPendingTTL),
		CreatedAt:     now,
	}

	s.oauthMu.Lock()
	for k, old := range s.pendingOAuth {
		if now.After(old.ExpiresAt) {
			delete(s.pendingOAuth, k)
		}
	}
	s.pendingOAuth[state] = pending
	s.oauthMu.Unlock()

	return pending, nil
}

func (s *Service) consumePendingOAuth(state string) (*models.OAuthPendingSession, error) {
	s.oauthMu.Lock()
	defer s.oauthMu.Unlock()

	now := s.now()
	pending, ok := s.pendingOAuth[state]
	if !ok {
		return nil, errors.New("pool: invalid or expired OAuth state token")
	}
	delete(s.pendingOAuth, state)
	if now.After(pending.ExpiresAt) {
		return nil, errors.New("pool: OAuth session expired (10m TTL exceeded)")
	}
	return pending, nil
}

type googleTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	Scope            string `json:"scope"`
	TokenType        string `json:"token_type"`
	IDToken          string `json:"id_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type googleUserInfoResponse struct {
	ID            string `json:"id"`
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	VerifiedEmail any    `json:"verified_email"`
	Name          string `json:"name"`
	Picture       string `json:"picture"`
}

type googleTokenInfoResponse struct {
	Email string `json:"email"`
	Scope string `json:"scope"`
	Sub   string `json:"sub"`
}

// CompleteOAuth exchanges a pasted authorization code or redirect URL for OAuth tokens,
// verifies identity & Colab quota via the account's sticky proxy, encrypts credentials in Vault,
// and saves the pooled Account.
func (s *Service) CompleteOAuth(ctx context.Context, state string, rawCodeOrURL string) (*models.Account, error) {
	code, stateFromURL, err := ParseAuthCodeInput(rawCodeOrURL)
	if err != nil {
		return nil, err
	}
	stateKey := strings.TrimSpace(state)
	if stateKey == "" {
		stateKey = stateFromURL
	}
	if stateKey == "" {
		return nil, errors.New("pool: missing OAuth state parameter")
	}

	pending, err := s.consumePendingOAuth(stateKey)
	if err != nil {
		return nil, err
	}
	if pending.IsContributor {
		return nil, errors.New("pool: contributor OAuth session cannot be completed via owner endpoint")
	}
	return s.completePendingOAuth(ctx, pending, code)
}

func (s *Service) completePendingOAuth(ctx context.Context, pending *models.OAuthPendingSession, code string) (*models.Account, error) {
	return s.completePendingOAuthWithReceiptHash(ctx, pending, code, "")
}

func (s *Service) completePendingOAuthWithReceiptHash(
	ctx context.Context,
	pending *models.OAuthPendingSession,
	code string,
	receiptTokenHash string,
) (*models.Account, error) {
	proxyID := pending.ProxyID
	proxyURL := pending.ProxyURL
	if proxyURL != "" && proxyID == "" {
		createdID, err := s.ensureProxyForURL(ctx, proxyURL, "")
		if err != nil {
			return nil, err
		}
		proxyID = createdID
	} else if proxyID != "" && proxyURL == "" {
		if p, err := s.st.GetProxy(ctx, proxyID); err == nil && p != nil {
			proxyURL = p.URL
		}
	}

	clientID := models.OfficialColabOAuthClientID
	clientSecret := models.OfficialColabOAuthClientSecret
	if pending.Mode == models.OAuthModeWebCallback && s.cfg.CustomOAuthClientID != "" && s.cfg.WebCallbackRedirectURI != "" {
		clientID = s.cfg.CustomOAuthClientID
		clientSecret = s.cfg.CustomOAuthClientSecret
	}

	httpClient, err := s.transport.HTTPClient(proxyURL, 25*time.Second)
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("redirect_uri", pending.RedirectURI)
	form.Set("grant_type", "authorization_code")
	form.Set("code_verifier", pending.CodeVerifier)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.GoogleTokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("pool: build token exchange request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pool: oauth token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	var tok googleTokenResponse
	_ = json.Unmarshal(body, &tok)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || tok.AccessToken == "" {
		msg := tok.ErrorDescription
		if msg == "" {
			msg = tok.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("pool: Google token exchange rejected (%s)", msg)
	}

	email, displayName, avatarURL, resolvedScopes := s.fetchGoogleIdentity(ctx, httpClient, tok.AccessToken, tok.IDToken)
	if email == "" {
		return nil, errors.New("pool: could not resolve Google account email from userinfo/tokeninfo")
	}

	scopes := pending.Scopes
	if tok.Scope != "" {
		scopes = strings.Fields(tok.Scope)
	} else if len(resolvedScopes) > 0 {
		scopes = resolvedScopes
	}

	now := s.now()
	expiresIn := time.Duration(tok.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 55 * time.Minute
	}
	tokenExpiry := now.Add(expiresIn)

	encAccess, err := s.cipher.EncryptString(tok.AccessToken)
	if err != nil {
		return nil, err
	}

	// Serialize email-keyed account lookup + creation/update, waiting if a contributor revocation VM
	// drain (`revokingAccs`) is currently in flight so the drain cannot stop newly claimed owner VMs!
	var (
		existing *models.Account
		targetID string
		mu       *sync.Mutex
	)
	for {
		s.emailUpsertMu.Lock()
		existing, _ = s.st.GetAccountByEmail(ctx, email)
		if existing != nil && pending.IsContributor && !existing.IsContributor {
			s.emailUpsertMu.Unlock()
			return nil, errors.New("pool: cannot overwrite an owner account via public contribution portal")
		}

		targetID = models.NewPrefixedID("acc_")
		if existing != nil {
			targetID = existing.ID
		}
		mu = s.accountMutex(targetID)
		mu.Lock()

		if existing != nil && !pending.IsContributor {
			s.acctMu.Lock()
			draining := s.revokingAccs[targetID] > 0
			s.acctMu.Unlock()
			if draining {
				mu.Unlock()
				s.emailUpsertMu.Unlock()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(25 * time.Millisecond):
				}
				continue
			}
		}
		break
	}

	if existing != nil {
		if fresh, fErr := s.st.GetAccount(ctx, targetID); fErr == nil && fresh != nil {
			existing = fresh
			if pending.IsContributor && !existing.IsContributor {
				mu.Unlock()
				s.emailUpsertMu.Unlock()
				return nil, errors.New("pool: cannot overwrite an owner account via public contribution portal")
			}
		}
	}

	var acc *models.Account
	if existing != nil {
		acc = existing
	} else {
		acc = &models.Account{
			ID:               targetID,
			Email:            email,
			SubscriptionTier: models.TierFree,
			Health:           models.HealthHealthy,
			Enabled:          true,
			Priority:         10,
			MaxConcurrentVMs: 2,
			CreatedAt:        now,
		}
	}

	acc.Email = email
	if displayName != "" {
		acc.DisplayName = displayName
	}
	if avatarURL != "" {
		acc.AvatarURL = avatarURL
	}
	if pending.Alias != "" {
		acc.Alias = pending.Alias
	} else if acc.Alias == "" && displayName != "" {
		acc.Alias = displayName
	}
	if pending.Notes != "" {
		acc.Notes = pending.Notes
	}
	if len(pending.Groups) > 0 {
		acc.Groups = uniqueStrings(append(acc.Groups, pending.Groups...))
	}
	if len(pending.Tags) > 0 {
		acc.Tags = uniqueStrings(append(acc.Tags, pending.Tags...))
	}
	if proxyID != "" {
		acc.ProxyID = proxyID
	}
	acc.IsContributor = pending.IsContributor
	if !acc.IsContributor {
		acc.ReceiptTokenHash = ""
		acc.InviteCode = ""
	} else {
		acc.Guardrails.BlockDrivePropagation = true
		if receiptTokenHash != "" {
			acc.ReceiptTokenHash = receiptTokenHash
		}
	}
	if pending.InviteCode != "" && acc.IsContributor {
		acc.InviteCode = pending.InviteCode
	}
	if pending.Guardrails != nil {
		g := *pending.Guardrails
		if acc.IsContributor {
			g.BlockDrivePropagation = true
			if g.MaxConcurrentVMs > 0 {
				acc.MaxConcurrentVMs = g.MaxConcurrentVMs
			} else {
				g.MaxConcurrentVMs = 1
				acc.MaxConcurrentVMs = 1
			}
		}
		acc.Guardrails = g
	}
	acc.Scopes = scopes
	acc.Health = models.HealthHealthy
	acc.HealthReason = ""
	acc.CooldownUntil = nil
	acc.Enabled = true
	acc.EncryptedAccessToken = encAccess
	if tok.RefreshToken != "" {
		encRefresh, rErr := s.cipher.EncryptString(tok.RefreshToken)
		if rErr != nil {
			mu.Unlock()
			s.emailUpsertMu.Unlock()
			return nil, rErr
		}
		acc.EncryptedRefreshToken = encRefresh
	}
	acc.OAuthClientID = clientID
	acc.OAuthClientSecret = clientSecret
	acc.TokenExpiresAt = &tokenExpiry
	acc.UpdatedAt = now
	s.recomputeAccountDerivedFields(ctx, acc)

	if err := s.st.SaveAccount(ctx, acc); err != nil {
		mu.Unlock()
		s.emailUpsertMu.Unlock()
		return nil, err
	}
	mu.Unlock()
	s.emailUpsertMu.Unlock()

	// Immediately poll live Colab quota & dual-backend capabilities (best-effort if offline).
	if refreshed, qErr := s.RefreshAccountQuota(ctx, acc.ID); qErr == nil && refreshed != nil {
		acc = refreshed
	} else {
		mu.Lock()
		if fresh, fErr := s.st.GetAccount(ctx, acc.ID); fErr == nil && fresh != nil {
			s.recomputeAccountDerivedFields(ctx, fresh)
			_ = s.st.SaveAccount(ctx, fresh)
			acc = fresh
		}
		mu.Unlock()
	}

	s.publish(models.WSEventAccountUpdated, acc)
	if summary, sErr := s.GetQuotaSummary(ctx); sErr == nil {
		s.publish(models.WSEventQuotaUpdated, summary)
	}
	return acc, nil
}

func (s *Service) fetchGoogleIdentity(
	ctx context.Context,
	httpClient *http.Client,
	accessToken string,
	idToken string,
) (email string, name string, avatar string, scopes []string) {
	// 1. Call OAuth2 UserInfo endpoint with Bearer token.
	if uReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.GoogleUserInfoURI, nil); err == nil {
		uReq.Header.Set("Authorization", "Bearer "+accessToken)
		uReq.Header.Set("Accept", "application/json")
		if resp, err := httpClient.Do(uReq); err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var ui googleUserInfoResponse
				if json.Unmarshal(body, &ui) == nil {
					email = strings.TrimSpace(ui.Email)
					name = strings.TrimSpace(ui.Name)
					avatar = strings.TrimSpace(ui.Picture)
				}
			}
		}
	}

	// 2. Call tokeninfo with QUERY PARAMETER ONLY (no Authorization header per colab-cli quirk).
	tiURL := s.cfg.GoogleTokenInfoURI + "?access_token=" + url.QueryEscape(accessToken)
	if tReq, err := http.NewRequestWithContext(ctx, http.MethodGet, tiURL, nil); err == nil {
		tReq.Header.Set("Accept", "application/json")
		if resp, err := httpClient.Do(tReq); err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var ti googleTokenInfoResponse
				if json.Unmarshal(body, &ti) == nil {
					if email == "" {
						email = strings.TrimSpace(ti.Email)
					}
					if ti.Scope != "" {
						scopes = strings.Fields(ti.Scope)
					}
				}
			}
		}
	}

	// 3. Fallback: decode OIDC id_token JWT payload if present.
	if (email == "" || name == "") && idToken != "" {
		parts := strings.Split(idToken, ".")
		if len(parts) >= 2 {
			if payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
				var claims map[string]any
				if json.Unmarshal(payloadBytes, &claims) == nil {
					if email == "" {
						if v, ok := claims["email"].(string); ok {
							email = strings.TrimSpace(v)
						}
					}
					if name == "" {
						if v, ok := claims["name"].(string); ok {
							name = strings.TrimSpace(v)
						}
					}
					if avatar == "" {
						if v, ok := claims["picture"].(string); ok {
							avatar = strings.TrimSpace(v)
						}
					}
				}
			}
		}
	}
	return email, name, avatar, scopes
}

// AcquireAccessToken returns a valid, decrypted OAuth2 access token and sticky proxy URL for accountID,
// proactively refreshing via Google's token endpoint when within 5 minutes of expiry.
func (s *Service) AcquireAccessToken(ctx context.Context, accountID string) (accessToken string, proxyURL string, err error) {
	return s.acquireAccessTokenInternal(ctx, accountID, false)
}

// AcquireAccessTokenForTeardown acquires an access token specifically to unassign live Colab VMs
// during contributor revocation or account deletion before stored OAuth tokens are wiped.
func (s *Service) AcquireAccessTokenForTeardown(ctx context.Context, accountID string) (accessToken string, proxyURL string, err error) {
	return s.acquireAccessTokenInternal(ctx, accountID, true)
}

func (s *Service) acquireAccessTokenInternal(
	ctx context.Context,
	accountID string,
	allowRevokedTeardown bool,
) (accessToken string, proxyURL string, err error) {
	mu := s.accountMutex(accountID)
	mu.Lock()
	defer mu.Unlock()

	acc, err := s.st.GetAccount(ctx, accountID)
	if err != nil {
		return "", "", err
	}
	if acc.Health == models.HealthRevokedByUser && !allowRevokedTeardown {
		return "", "", errors.New("pool: account contribution has been revoked")
	}
	if acc.EffectiveProvider() != models.ProviderColab {
		return "", "", fmt.Errorf("pool: account %s uses provider %s (skip Google Colab OAuth)", acc.ID, acc.EffectiveProvider())
	}

	proxyURL = s.resolveAccountProxyURL(ctx, acc)
	now := s.now()

	// Return cached access token if valid for more than 5 minutes.
	if acc.EncryptedAccessToken != "" && acc.TokenExpiresAt != nil && acc.TokenExpiresAt.Sub(now) > tokenRefreshMargin {
		tok, dErr := s.cipher.DecryptString(acc.EncryptedAccessToken)
		if dErr == nil && tok != "" {
			return tok, proxyURL, nil
		}
	}

	if acc.EncryptedRefreshToken == "" {
		// If only a non-expiring/demo access token was seeded without refresh token, return it if valid.
		if acc.EncryptedAccessToken != "" && (acc.TokenExpiresAt == nil || now.Before(*acc.TokenExpiresAt)) {
			tok, dErr := s.cipher.DecryptString(acc.EncryptedAccessToken)
			if dErr == nil && tok != "" {
				return tok, proxyURL, nil
			}
		}
		return "", proxyURL, errors.New("pool: account has no refresh token stored; re-authentication required")
	}

	refreshToken, err := s.cipher.DecryptString(acc.EncryptedRefreshToken)
	if err != nil {
		return "", proxyURL, fmt.Errorf("pool: decrypt refresh token: %w", err)
	}

	clientID := acc.OAuthClientID
	clientSecret := acc.OAuthClientSecret
	if clientID == "" {
		clientID = models.OfficialColabOAuthClientID
		clientSecret = models.OfficialColabOAuthClientSecret
	}

	httpClient, err := s.transport.HTTPClient(proxyURL, 20*time.Second)
	if err != nil {
		acc.Health = models.HealthProxyError
		acc.HealthReason = err.Error()
		_ = s.st.SaveAccount(ctx, acc)
		return "", proxyURL, err
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.GoogleTokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", proxyURL, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", proxyURL, fmt.Errorf("pool: refresh token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	var tok googleTokenResponse
	_ = json.Unmarshal(body, &tok)

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
		if strings.EqualFold(tok.Error, "invalid_grant") {
			if acc.IsContributor {
				acc.Health = models.HealthRevokedByUser
				acc.HealthReason = "OAuth grant revoked by contributor (invalid_grant)"
			} else {
				acc.Health = models.HealthAuthExpired
				acc.HealthReason = "OAuth refresh token expired or revoked (invalid_grant)"
			}
			_ = s.st.SaveAccount(ctx, acc)
			s.publish(models.WSEventAccountUpdated, acc)
			return "", proxyURL, fmt.Errorf("pool: %s", acc.HealthReason)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || tok.AccessToken == "" {
		return "", proxyURL, fmt.Errorf("pool: token refresh HTTP %d (%s)", resp.StatusCode, tok.Error)
	}

	encAccess, err := s.cipher.EncryptString(tok.AccessToken)
	if err != nil {
		return "", proxyURL, err
	}
	acc.EncryptedAccessToken = encAccess
	refreshToSeal := refreshToken
	if tok.RefreshToken != "" {
		refreshToSeal = tok.RefreshToken
	}
	if refreshToSeal != "" {
		encRef, rErr := s.cipher.EncryptString(refreshToSeal)
		if rErr != nil {
			return "", proxyURL, rErr
		}
		acc.EncryptedRefreshToken = encRef
	}
	expiresIn := time.Duration(tok.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 55 * time.Minute
	}
	exp := s.now().Add(expiresIn)
	acc.TokenExpiresAt = &exp
	if acc.Health == models.HealthAuthExpired || acc.Health == models.HealthProxyError {
		acc.Health = models.HealthHealthy
		acc.HealthReason = ""
	}
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		return "", proxyURL, err
	}
	return tok.AccessToken, proxyURL, nil
}
