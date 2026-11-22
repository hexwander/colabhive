package pool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/colabhive/colabhive/internal/models"
)

func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(sum[:])
}

// CreateInvite creates a shareable Guest Contributor Portal invitation (`ch_inv_...`).
func (s *Service) CreateInvite(ctx context.Context, req models.CreateInviteRequest) (*models.ContributorInvite, error) {
	code := models.NewPrefixedID("ch_inv_")
	now := s.now()

	title := strings.TrimSpace(req.PoolTitle)
	if title == "" {
		title = "ColabHive Shared Compute Pool"
	}
	maintainer := strings.TrimSpace(req.MaintainerName)
	if maintainer == "" {
		maintainer = "Pool Maintainer"
	}

	g := req.DefaultGuardrails
	g.BlockDrivePropagation = true
	g.ReceiptToken = ""
	if g.MaxConcurrentVMs <= 0 {
		g.MaxConcurrentVMs = 1
	}
	if len(g.AllowedGPUs) == 0 {
		g.AllowedGPUs = []models.AcceleratorType{
			models.AcceleratorG4,
			models.AcceleratorA100,
			models.AcceleratorL4,
			models.AcceleratorT4,
			models.AcceleratorCPU,
		}
	}

	modes := []models.OAuthAuthMode{models.OAuthModeRemoteCopyPaste, models.OAuthModeCLIDonate}
	if s.cfg.CustomOAuthClientID != "" && s.cfg.WebCallbackRedirectURI != "" {
		modes = append(modes, models.OAuthModeWebCallback)
	}

	var expiresAt *time.Time
	if req.TTLHours > 0 {
		exp := now.Add(time.Duration(req.TTLHours) * time.Hour)
		expiresAt = &exp
	}

	invite := &models.ContributorInvite{
		Code:              code,
		PoolTitle:         title,
		MaintainerName:    maintainer,
		WelcomeNote:       strings.TrimSpace(req.WelcomeNote),
		TargetGroups:      uniqueStrings(req.TargetGroups),
		DefaultProxyID:    strings.TrimSpace(req.DefaultProxyID),
		DefaultGuardrails: g,
		MaxUses:           req.MaxUses,
		UsedCount:         0,
		SupportedModes:    modes,
		RequiredScopes:    append([]string(nil), models.ContributorOAuthScopes...),
		ShareURL:          s.cfg.PublicBaseURL + "/contribute/" + code,
		Revoked:           false,
		ExpiresAt:         expiresAt,
		CreatedAt:         now,
	}

	if err := s.st.SaveInvite(ctx, invite); err != nil {
		return nil, err
	}
	return invite, nil
}

// ListInvites lists all contributor invite links.
func (s *Service) ListInvites(ctx context.Context) ([]*models.ContributorInvite, error) {
	return s.st.ListInvites(ctx)
}

// RevokeInvite marks an invite link as revoked so no new guest contributions can redeem it.
func (s *Service) RevokeInvite(ctx context.Context, code string) error {
	s.inviteMu.Lock()
	defer s.inviteMu.Unlock()

	inv, err := s.st.GetInvite(ctx, strings.TrimSpace(code))
	if err != nil {
		return err
	}
	inv.Revoked = true
	return s.st.SaveInvite(ctx, inv)
}

// GetPublicInvite returns public Guest Portal metadata if the invite is active and unrevoked.
func (s *Service) GetPublicInvite(ctx context.Context, code string) (*models.ContributorInvite, error) {
	inv, err := s.st.GetInvite(ctx, strings.TrimSpace(code))
	if err != nil {
		return nil, err
	}
	if inv.Revoked {
		return nil, errors.New("pool: contributor invite has been revoked")
	}
	if inv.ExpiresAt != nil && !s.now().Before(*inv.ExpiresAt) {
		return nil, errors.New("pool: contributor invite has expired")
	}
	if inv.MaxUses > 0 && inv.UsedCount >= inv.MaxUses {
		return nil, errors.New("pool: contributor invite has reached maximum redemptions")
	}
	return inv, nil
}

// StartContribution validates an invite link, locks in the contributor's chosen safety guardrails,
// and starts a least-privilege (`colaboratory`-only, ZERO Google Drive) OAuth2 PKCE S256 session.
func (s *Service) StartContribution(
	ctx context.Context,
	inviteCode string,
	guardrails models.ContributorGuardrails,
	mode models.OAuthAuthMode,
) (*models.OAuthPendingSession, error) {
	inv, err := s.GetPublicInvite(ctx, inviteCode)
	if err != nil {
		return nil, err
	}

	// Enforce hard contributor security invariants.
	guardrails.BlockDrivePropagation = true
	guardrails.ReceiptToken = ""
	if guardrails.MaxConcurrentVMs <= 0 {
		if inv.DefaultGuardrails.MaxConcurrentVMs > 0 {
			guardrails.MaxConcurrentVMs = inv.DefaultGuardrails.MaxConcurrentVMs
		} else {
			guardrails.MaxConcurrentVMs = 1
		}
	}
	if len(guardrails.AllowedGPUs) == 0 && len(inv.DefaultGuardrails.AllowedGPUs) > 0 {
		guardrails.AllowedGPUs = append([]models.AcceleratorType(nil), inv.DefaultGuardrails.AllowedGPUs...)
	}

	if mode == "" {
		mode = models.OAuthModeRemoteCopyPaste
	}

	return s.StartOAuth(ctx, models.OAuthStartRequest{
		Groups:        inv.TargetGroups,
		Tags:          []string{"contributed"},
		ProxyID:       inv.DefaultProxyID,
		Mode:          mode,
		IsContributor: true,
		InviteCode:    inv.Code,
		Guardrails:    &guardrails,
	})
}

// CompleteContribution exchanges the contributor's auth code, saves encrypted credentials & guardrails,
// increments invite usage, and issues a private self-service ReceiptToken (`ch_rcpt_...`).
func (s *Service) CompleteContribution(
	ctx context.Context,
	inviteCode string,
	state string,
	rawCodeOrURL string,
) (*models.ContributionReceipt, error) {
	s.inviteMu.Lock()
	defer s.inviteMu.Unlock()

	inv, err := s.GetPublicInvite(ctx, inviteCode)
	if err != nil {
		return nil, err
	}

	code, stateFromURL, err := ParseAuthCodeInput(rawCodeOrURL)
	if err != nil {
		return nil, err
	}
	stateKey := strings.TrimSpace(state)
	if stateKey == "" {
		stateKey = stateFromURL
	}
	pending, err := s.consumePendingOAuth(stateKey)
	if err != nil {
		return nil, err
	}
	if !pending.IsContributor || pending.InviteCode != inv.Code {
		return nil, errors.New("pool: OAuth state does not match contributor invite")
	}

	receiptToken, err := generateRandomToken("ch_rcpt_", 24)
	if err != nil {
		return nil, fmt.Errorf("pool: generate receipt token: %w", err)
	}
	receiptHash := sha256Hex(receiptToken)

	acc, err := s.completePendingOAuthWithReceiptHash(ctx, pending, code, receiptHash)
	if err != nil {
		return nil, err
	}

	// Verify the contributor receipt is still active on the persisted account (in case owner OAuth
	// claimed the account concurrently during post-save quota refresh) without ever setting IsContributor=true!
	freshAcc, fErr := s.st.GetAccount(ctx, acc.ID)
	if fErr != nil || freshAcc == nil || !freshAcc.IsContributor || freshAcc.ReceiptTokenHash != receiptHash {
		return nil, errors.New("pool: cannot overwrite an owner account via public contribution portal")
	}

	inv.UsedCount++
	if err := s.st.SaveInvite(ctx, inv); err != nil {
		return nil, err
	}

	status, err := s.buildReceiptStatus(ctx, freshAcc)
	if err != nil {
		return nil, err
	}

	return &models.ContributionReceipt{
		ReceiptToken: receiptToken,
		ManageURL:    s.cfg.PublicBaseURL + "/contribute/manage/" + receiptToken,
		Status:       *status,
	}, nil
}

// CompleteCredentialContribution validates an active contributor invite under s.inviteMu,
// verifies donated OAuth credentials without allowing owner-account overwrites, enforces
// contributor safety guardrails (`IsContributor = true`, `BlockDrivePropagation = true`),
// persists `ReceiptTokenHash`, atomically increments `inv.UsedCount`, and returns a live receipt.
func (s *Service) CompleteCredentialContribution(
	ctx context.Context,
	inviteCode string,
	guardrails models.ContributorGuardrails,
	credJSON []byte,
) (*models.ContributionReceipt, error) {
	s.inviteMu.Lock()
	defer s.inviteMu.Unlock()

	inv, err := s.GetPublicInvite(ctx, inviteCode)
	if err != nil {
		return nil, err
	}

	cred, err := parseDonatedCredentialPayload(credJSON)
	if err != nil {
		return nil, err
	}

	proxyID := strings.TrimSpace(inv.DefaultProxyID)
	proxyURL := ""
	if proxyID != "" {
		if p, pErr := s.st.GetProxy(ctx, proxyID); pErr == nil && p != nil {
			proxyURL = p.URL
		}
	}

	accessToken := cred.AccessToken
	var tokenExpiry *time.Time
	if cred.Expiry != nil {
		tokenExpiry = cred.Expiry
	}

	// Verify donated credentials strictly: when `cred.RefreshToken != ""`, refresh/validate it directly
	// against `GoogleTokenURI` so a mismatched `(OwnerRefreshToken, AttackerAccessToken)` pair cannot
	// authenticate as the attacker while persisting the owner's refresh token!
	resolvedEmail, resolvedAccess, exp, resolvedName, vErr := s.verifyDonatedCredentialIdentity(ctx, *cred, proxyURL)
	if vErr != nil {
		return nil, vErr
	}
	email := strings.TrimSpace(cred.Email)
	if resolvedEmail != "" {
		if email != "" && !strings.EqualFold(email, resolvedEmail) {
			return nil, errors.New("pool: donated credential email does not match verified Google account identity")
		}
		email = resolvedEmail
		if resolvedAccess != "" {
			accessToken = resolvedAccess
			tokenExpiry = exp
		}
		if cred.Alias == "" && resolvedName != "" {
			cred.Alias = resolvedName
		}
	}
	if email == "" {
		seed := cred.RefreshToken
		if seed == "" {
			seed = accessToken
		}
		sum := sha256.Sum256([]byte(seed))
		email = "imported-" + hex.EncodeToString(sum[:4]) + "@colab.local"
	}

	s.emailUpsertMu.Lock()
	existing, _ := s.st.GetAccountByEmail(ctx, email)
	if existing != nil {
		if !existing.IsContributor {
			s.emailUpsertMu.Unlock()
			return nil, errors.New("pool: cannot overwrite an owner account via public contribution portal")
		}
		if resolvedEmail == "" {
			existingRefresh, _ := s.cipher.DecryptString(existing.EncryptedRefreshToken)
			if cred.RefreshToken == "" || existingRefresh == "" || existingRefresh != cred.RefreshToken {
				s.emailUpsertMu.Unlock()
				return nil, errors.New("pool: cannot overwrite existing account without verified Google identity")
			}
		}
	}

	// Enforce contributor guardrail invariants & inherit unset defaults from invite.
	g := guardrails
	if g.MaxConcurrentVMs <= 0 {
		if inv.DefaultGuardrails.MaxConcurrentVMs > 0 {
			g.MaxConcurrentVMs = inv.DefaultGuardrails.MaxConcurrentVMs
		} else {
			g.MaxConcurrentVMs = 1
		}
	}
	if len(g.AllowedGPUs) == 0 && len(inv.DefaultGuardrails.AllowedGPUs) > 0 {
		g.AllowedGPUs = append([]models.AcceleratorType(nil), inv.DefaultGuardrails.AllowedGPUs...)
	}
	g.BlockDrivePropagation = true
	g.ReceiptToken = ""

	receiptToken, err := generateRandomToken("ch_rcpt_", 24)
	if err != nil {
		s.emailUpsertMu.Unlock()
		return nil, fmt.Errorf("pool: generate receipt token: %w", err)
	}

	encRef, err := s.cipher.EncryptString(cred.RefreshToken)
	if err != nil {
		s.emailUpsertMu.Unlock()
		return nil, err
	}
	encAcc, err := s.cipher.EncryptString(accessToken)
	if err != nil {
		s.emailUpsertMu.Unlock()
		return nil, err
	}

	targetID := models.NewPrefixedID("acc_")
	if existing != nil {
		targetID = existing.ID
	}

	now := s.now()
	mu := s.accountMutex(targetID)
	mu.Lock()
	if existing != nil {
		if fresh, fErr := s.st.GetAccount(ctx, targetID); fErr == nil && fresh != nil {
			existing = fresh
			if !existing.IsContributor {
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
			CreatedAt:        now,
		}
	}

	acc.Email = email
	if cred.Alias != "" {
		acc.Alias = cred.Alias
	}
	acc.Groups = uniqueStrings(append(acc.Groups, inv.TargetGroups...))
	acc.Tags = uniqueStrings(append(acc.Tags, "contributed"))
	if proxyID != "" {
		acc.ProxyID = proxyID
	}
	acc.IsContributor = true
	acc.InviteCode = inv.Code
	acc.Guardrails = g
	acc.Guardrails.BlockDrivePropagation = true
	acc.MaxConcurrentVMs = g.MaxConcurrentVMs
	if len(cred.Scopes) > 0 {
		acc.Scopes = cred.Scopes
	} else if len(acc.Scopes) == 0 {
		acc.Scopes = append([]string(nil), models.ContributorOAuthScopes...)
	}
	acc.Health = models.HealthHealthy
	acc.HealthReason = ""
	acc.CooldownUntil = nil
	acc.Enabled = true
	if encRef != "" {
		acc.EncryptedRefreshToken = encRef
	}
	if encAcc != "" {
		acc.EncryptedAccessToken = encAcc
	}
	acc.OAuthClientID = firstNonEmpty(cred.ClientID, models.OfficialColabOAuthClientID)
	acc.OAuthClientSecret = firstNonEmpty(cred.ClientSecret, models.OfficialColabOAuthClientSecret)
	acc.TokenExpiresAt = tokenExpiry
	acc.ReceiptTokenHash = sha256Hex(receiptToken)
	acc.UpdatedAt = now

	s.recomputeAccountDerivedFields(ctx, acc)
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		mu.Unlock()
		s.emailUpsertMu.Unlock()
		return nil, err
	}
	mu.Unlock()
	s.emailUpsertMu.Unlock()

	if refreshed, qErr := s.RefreshAccountQuota(ctx, acc.ID); qErr == nil && refreshed != nil {
		acc = refreshed
	}
	freshAcc, fErr := s.st.GetAccount(ctx, acc.ID)
	if fErr != nil || freshAcc == nil || !freshAcc.IsContributor || freshAcc.ReceiptTokenHash != sha256Hex(receiptToken) {
		return nil, errors.New("pool: cannot overwrite an owner account via public contribution portal")
	}
	acc = freshAcc

	inv.UsedCount++
	if err := s.st.SaveInvite(ctx, inv); err != nil {
		return nil, err
	}

	s.publish(models.WSEventAccountUpdated, acc)
	if summary, sErr := s.GetQuotaSummary(ctx); sErr == nil {
		s.publish(models.WSEventQuotaUpdated, summary)
	}

	status, err := s.buildReceiptStatus(ctx, acc)
	if err != nil {
		return nil, err
	}

	return &models.ContributionReceipt{
		ReceiptToken: receiptToken,
		ManageURL:    s.cfg.PublicBaseURL + "/contribute/manage/" + receiptToken,
		Status:       *status,
	}, nil
}

func parseDonatedCredentialPayload(raw []byte) (*ParsedOAuthCredential, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("pool: empty donated credential payload")
	}
	if trimmed[0] == '"' {
		var strPayload string
		if err := json.Unmarshal(trimmed, &strPayload); err == nil {
			strTrim := strings.TrimSpace(strPayload)
			if strings.HasPrefix(strTrim, "[") && !strings.Contains(strTrim[:minInt(len(strTrim), 40)], "{") {
				return ParsePlaintextKeyringINI(strTrim)
			}
			return parseColabTokenWireJSON([]byte(strTrim))
		}
	}
	if trimmed[0] == '[' && !bytes.Contains(trimmed[:minInt(len(trimmed), 40)], []byte("{")) {
		return ParsePlaintextKeyringINI(string(trimmed))
	}
	if trimmed[0] == '[' {
		return nil, errors.New("pool: public credential contribution accepts a single account credential, not an array")
	}
	cred, err := parseColabTokenWireJSON(trimmed)
	if err == nil {
		return cred, nil
	}
	var portable models.PortableAccountEntry
	if pErr := json.Unmarshal(trimmed, &portable); pErr == nil && (portable.OAuth.RefreshToken != "" || portable.OAuth.AccessToken != "") {
		parsed := portableEntryToParsed(portable)
		return &parsed, nil
	}
	return nil, err
}

func (s *Service) verifyDonatedCredentialIdentity(
	ctx context.Context,
	cred ParsedOAuthCredential,
	proxyURL string,
) (email string, accessToken string, expiry *time.Time, displayName string, err error) {
	rCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()

	httpClient, cErr := s.transport.HTTPClient(proxyURL, 6*time.Second)
	if cErr != nil {
		return "", "", nil, "", fmt.Errorf("pool: failed to build HTTP client to verify donated credential: %w", cErr)
	}

	if strings.TrimSpace(cred.RefreshToken) != "" {
		if strings.TrimSpace(s.cfg.GoogleTokenURI) == "" {
			return "", "", nil, "", errors.New("pool: OAuth token verification endpoint is not configured")
		}
		form := url.Values{}
		form.Set("client_id", firstNonEmpty(cred.ClientID, models.OfficialColabOAuthClientID))
		form.Set("client_secret", firstNonEmpty(cred.ClientSecret, models.OfficialColabOAuthClientSecret))
		form.Set("refresh_token", strings.TrimSpace(cred.RefreshToken))
		form.Set("grant_type", "refresh_token")

		req, rErr := http.NewRequestWithContext(rCtx, http.MethodPost, s.cfg.GoogleTokenURI, strings.NewReader(form.Encode()))
		if rErr != nil {
			return "", "", nil, "", fmt.Errorf("pool: build donated token verification request: %w", rErr)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, doErr := httpClient.Do(req)
		if doErr != nil {
			return "", "", nil, "", fmt.Errorf("pool: failed to verify donated refresh_token upstream: %w", doErr)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized {
			return "", "", nil, "", errors.New("pool: invalid or revoked donated refresh_token")
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", "", nil, "", fmt.Errorf("pool: upstream token verification returned HTTP %d", resp.StatusCode)
		}
		var tok googleTokenResponse
		if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
			return "", "", nil, "", errors.New("pool: invalid upstream token response for donated refresh_token")
		}
		dur := time.Duration(tok.ExpiresIn) * time.Second
		if dur <= 0 {
			dur = 55 * time.Minute
		}
		exp := s.now().Add(dur)
		refEmail, refName, _, _ := s.fetchGoogleIdentity(rCtx, httpClient, tok.AccessToken, "")
		if refEmail == "" {
			return "", "", nil, "", errors.New("pool: unable to verify Google account identity for donated refresh_token")
		}
		if strings.TrimSpace(cred.AccessToken) != "" {
			accEmail, _, _, _ := s.fetchGoogleIdentity(rCtx, httpClient, cred.AccessToken, "")
			if accEmail == "" {
				return "", "", nil, "", errors.New("pool: unable to verify Google account identity for donated access_token")
			}
			if !strings.EqualFold(accEmail, refEmail) {
				return "", "", nil, "", errors.New("pool: donated refresh_token and access_token belong to different Google accounts")
			}
		}
		return refEmail, tok.AccessToken, &exp, refName, nil
	}

	if strings.TrimSpace(cred.AccessToken) == "" {
		return "", "", nil, "", errors.New("pool: donated credential must include a valid refresh_token or access_token")
	}
	em, nm, _, _ := s.fetchGoogleIdentity(rCtx, httpClient, cred.AccessToken, "")
	if em == "" {
		return "", "", nil, "", errors.New("pool: unable to verify Google account identity for donated access_token")
	}
	return em, cred.AccessToken, cred.Expiry, nm, nil
}

// GetContributionByReceipt looks up a contributed account by its SHA-256 receipt digest
// and returns privacy-preserving live telemetry.
func (s *Service) GetContributionByReceipt(ctx context.Context, receiptToken string) (*models.ContributorReceiptStatus, error) {
	hash := sha256Hex(receiptToken)
	acc, err := s.st.GetAccountByReceiptHash(ctx, hash)
	if err != nil || acc == nil || !acc.IsContributor {
		return nil, errors.New("pool: contributor receipt not found")
	}
	s.recomputeAccountDerivedFields(ctx, acc)
	return s.buildReceiptStatus(ctx, acc)
}

// UpdateContributionGuardrails lets an external contributor adjust their safety floor, GPU caps,
// schedule window, or pause state in real time without admin intervention.
func (s *Service) UpdateContributionGuardrails(
	ctx context.Context,
	receiptToken string,
	patch models.ContributorGuardrailsPatch,
) (*models.ContributorReceiptStatus, error) {
	hash := sha256Hex(receiptToken)
	accLookup, err := s.st.GetAccountByReceiptHash(ctx, hash)
	if err != nil || accLookup == nil || !accLookup.IsContributor {
		return nil, errors.New("pool: contributor receipt not found")
	}

	mu := s.accountMutex(accLookup.ID)
	mu.Lock()
	acc, err := s.st.GetAccount(ctx, accLookup.ID)
	if err != nil || acc == nil || !acc.IsContributor || acc.ReceiptTokenHash != hash {
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("pool: contributor receipt not found")
	}
	if acc.Health == models.HealthRevokedByUser {
		mu.Unlock()
		return nil, errors.New("pool: contribution has already been revoked")
	}

	if patch.MinReserveCU != nil && *patch.MinReserveCU >= 0 {
		acc.Guardrails.MinReserveCU = *patch.MinReserveCU
	}
	if patch.MaxDonatedCUTotal != nil && *patch.MaxDonatedCUTotal >= 0 {
		acc.Guardrails.MaxDonatedCUTotal = *patch.MaxDonatedCUTotal
	}
	if patch.MaxDonatedCUDaily != nil && *patch.MaxDonatedCUDaily >= 0 {
		acc.Guardrails.MaxDonatedCUDaily = *patch.MaxDonatedCUDaily
	}
	if patch.AllowPaidCUBurn != nil {
		acc.Guardrails.AllowPaidCUBurn = *patch.AllowPaidCUBurn
	}
	if patch.AllowedGPUs != nil {
		acc.Guardrails.AllowedGPUs = append([]models.AcceleratorType(nil), patch.AllowedGPUs...)
	}
	if patch.MaxConcurrentVMs != nil && *patch.MaxConcurrentVMs > 0 {
		acc.Guardrails.MaxConcurrentVMs = *patch.MaxConcurrentVMs
		acc.MaxConcurrentVMs = *patch.MaxConcurrentVMs
	}
	if patch.ScheduleWindow != nil {
		win := *patch.ScheduleWindow
		acc.Guardrails.ScheduleWindow = &win
	}
	if patch.MaskEmailInDashboard != nil {
		acc.Guardrails.MaskEmailInDashboard = *patch.MaskEmailInDashboard
	}
	if patch.Paused != nil {
		acc.Guardrails.Paused = *patch.Paused
	}
	acc.Guardrails.BlockDrivePropagation = true
	acc.Guardrails.ReceiptToken = ""

	s.recomputeAccountDerivedFields(ctx, acc)
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		mu.Unlock()
		return nil, err
	}
	mu.Unlock()

	if acc.ActiveAssignments > 0 && (acc.Guardrails.Paused || !acc.Health.IsSchedulable()) {
		s.publish(models.WSEventGuardrailPreempt, map[string]any{
			"account_id": acc.ID,
			"reason":     acc.HealthReason,
		})
	}
	s.publish(models.WSEventAccountUpdated, acc)
	if summary, sErr := s.GetQuotaSummary(ctx); sErr == nil {
		s.publish(models.WSEventQuotaUpdated, summary)
	}
	return s.buildReceiptStatus(ctx, acc)
}

// RevokeContribution executes a 1-click self-service emergency revoke:
//  1. Immediately transitions health to REVOKED_BY_USER and disables scheduling under accountMutex.
//  2. Unassigns all ColabHive-managed active VMs on Google (`sessionDrainer`) BEFORE revoking OAuth tokens.
//  3. Calls Google's `POST https://oauth2.googleapis.com/revoke?token=...` via the account's sticky proxy,
//     verifying HTTP 200 or HTTP 400 (`invalid_token`) before wiping `EncryptedRefreshToken`.
func (s *Service) RevokeContribution(ctx context.Context, receiptToken string) error {
	hash := sha256Hex(receiptToken)
	accLookup, err := s.st.GetAccountByReceiptHash(ctx, hash)
	if err != nil || accLookup == nil || !accLookup.IsContributor {
		return errors.New("pool: contributor receipt not found")
	}

	// 1. Immediately disable scheduling and mark REVOKED_BY_USER before network calls.
	mu := s.accountMutex(accLookup.ID)
	mu.Lock()
	acc, err := s.st.GetAccount(ctx, accLookup.ID)
	if err != nil || acc == nil || !acc.IsContributor || acc.ReceiptTokenHash != hash {
		mu.Unlock()
		if err != nil {
			return err
		}
		return errors.New("pool: contributor receipt not found")
	}
	acc.Enabled = false
	acc.Guardrails.Paused = true
	acc.SchedulableCU = 0
	acc.Health = models.HealthRevokedByUser
	acc.HealthReason = "Revoked by contributor via self-service portal"
	if err := s.st.SaveAccount(ctx, acc); err != nil {
		mu.Unlock()
		return err
	}
	// Mark `revokingAccs[accLookup.ID]` BEFORE releasing `mu` so concurrent owner OAuth waits for VM drain
	// to finish before reclaiming the account (zero race window between receipt check and drain gate!).
	s.acctMu.Lock()
	if s.revokingAccs == nil {
		s.revokingAccs = make(map[string]int)
	}
	s.revokingAccs[accLookup.ID]++
	s.acctMu.Unlock()
	mu.Unlock()
	defer func() {
		s.acctMu.Lock()
		if s.revokingAccs[accLookup.ID] <= 1 {
			delete(s.revokingAccs, accLookup.ID)
		} else {
			s.revokingAccs[accLookup.ID]--
		}
		s.acctMu.Unlock()
	}()

	// 2. Drain active ColabHive-managed VMs on Google BEFORE revoking OAuth credentials!
	if drainer := s.getSessionDrainer(); drainer != nil {
		if drainErr := drainer(ctx, accLookup.ID); drainErr != nil {
			return fmt.Errorf("pool: failed to drain active contributor VMs before revoke: %w", drainErr)
		}
	}

	// 3. Revalidate contributor receipt under account lock BEFORE mutating any remaining session rows
	//    or revoking OAuth grant at Google (`oauth2.googleapis.com/revoke`).
	mu.Lock()
	acc, err = s.st.GetAccount(ctx, accLookup.ID)
	if err != nil || acc == nil || !acc.IsContributor || acc.ReceiptTokenHash != hash {
		mu.Unlock()
		if err != nil {
			return err
		}
		return errors.New("pool: contributor receipt not found")
	}

	now := s.now()
	if sessions, sErr := s.st.ListSessions(ctx); sErr == nil {
		for _, sess := range sessions {
			if sess.AccountID == accLookup.ID && !sess.AdoptedExternal && isActiveSessionStatus(sess.Status) {
				sess.Status = models.SessionStopped
				sess.StatusMessage = "Terminated immediately due to contributor self-service revocation"
				sess.StoppedAt = &now
				sess.ProxyToken = ""
				_ = s.st.SaveSession(ctx, sess)
				s.publish(models.WSEventSessionUpdated, sess)
			}
		}
	}

	tokenToRevoke, _ := s.cipher.DecryptString(acc.EncryptedRefreshToken)
	if tokenToRevoke == "" {
		tokenToRevoke, _ = s.cipher.DecryptString(acc.EncryptedAccessToken)
	}
	proxyURL := s.resolveAccountProxyURL(ctx, acc)

	if tokenToRevoke != "" {
		httpClient, cErr := s.transport.HTTPClient(proxyURL, 10*time.Second)
		if cErr != nil {
			mu.Unlock()
			return fmt.Errorf("pool: build proxy client for OAuth revoke: %w", cErr)
		}
		revURL := s.cfg.GoogleRevokeURI + "?token=" + url.QueryEscape(tokenToRevoke)
		form := url.Values{"token": {tokenToRevoke}}
		rReq, rErr := http.NewRequestWithContext(ctx, http.MethodPost, revURL, strings.NewReader(form.Encode()))
		if rErr != nil {
			mu.Unlock()
			return fmt.Errorf("pool: create OAuth revoke request: %w", rErr)
		}
		rReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, doErr := httpClient.Do(rReq)
		if doErr != nil {
			mu.Unlock()
			return fmt.Errorf("pool: Google OAuth revoke request failed: %w", doErr)
		}
		revBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		_ = resp.Body.Close()

		confirmedRevoked := (resp.StatusCode >= 200 && resp.StatusCode < 300) ||
			(resp.StatusCode == http.StatusBadRequest && strings.Contains(strings.ToLower(string(revBody)), "invalid_token"))
		if !confirmedRevoked {
			mu.Unlock()
			return fmt.Errorf("pool: Google OAuth revoke returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(revBody)))
		}
	}

	// 4. Permanently wipe stored OAuth secrets now that Google confirmed revocation (or no token remained).
	acc.EncryptedRefreshToken = ""
	acc.EncryptedAccessToken = ""
	acc.TokenExpiresAt = nil
	acc.Enabled = false
	acc.Guardrails.Paused = true
	acc.ActiveAssignments = 0
	acc.LiveBurnRateCUHr = 0
	acc.SchedulableCU = 0
	acc.Health = models.HealthRevokedByUser
	acc.HealthReason = "Revoked by contributor via self-service portal"

	if err := s.st.SaveAccount(ctx, acc); err != nil {
		mu.Unlock()
		return err
	}
	mu.Unlock()

	s.publish(models.WSEventGuardrailPreempt, map[string]any{
		"account_id": acc.ID,
		"revoked":    true,
		"reason":     acc.HealthReason,
	})
	s.publish(models.WSEventAccountUpdated, acc)
	if summary, sErr := s.GetQuotaSummary(ctx); sErr == nil {
		s.publish(models.WSEventQuotaUpdated, summary)
	}
	return nil
}

func (s *Service) buildReceiptStatus(ctx context.Context, acc *models.Account) (*models.ContributorReceiptStatus, error) {
	activeVMs := make([]models.ContributorActiveVM, 0)
	now := s.now()
	if sessions, err := s.st.ListSessions(ctx); err == nil {
		for _, sess := range sessions {
			if sess.AccountID == acc.ID && isActiveSessionStatus(sess.Status) {
				uptime := int64(now.Sub(sess.CreatedAt).Seconds())
				if uptime < 0 {
					uptime = 0
				}
				activeVMs = append(activeVMs, models.ContributorActiveVM{
					SessionID:    sess.ID,
					Accelerator:  sess.Accelerator,
					BurnRateCUHr: sess.BurnRateCUHr,
					UptimeSec:    uptime,
					StartedAt:    sess.CreatedAt,
				})
			}
		}
	}

	g := acc.Guardrails
	g.ReceiptToken = ""

	return &models.ContributorReceiptStatus{
		AccountID:        acc.ID,
		MaskedEmail:      MaskEmail(acc.Email),
		SubscriptionTier: acc.SubscriptionTier,
		Health:           acc.Health,
		PaidCUBalance:    round2(acc.PaidCUBalance),
		SchedulableCU:    round2(acc.SchedulableCU),
		LiveBurnRateCUHr: round2(acc.LiveBurnRateCUHr),
		Guardrails:       g,
		ActiveVMs:        activeVMs,
		LastPolledAt:     acc.LastPolledAt,
	}, nil
}

// ============================================================================
// Scheduler Admission Guardrail Filter (`CanScheduleAccount`)
// ============================================================================

// CanScheduleAccount enforces account health, cooldowns, concurrency caps, GPU entitlements,
// contributor `MinReserveCU` safety floors (requiring >= max(2.0 CU, 15m of burn) above floor),
// `AllowedGPUs`, and `ScheduleWindow` boundaries.
func (s *Service) CanScheduleAccount(acc *models.Account, requested models.AcceleratorType) (allowed bool, reason string) {
	if acc == nil {
		return false, "nil account"
	}
	now := s.now()

	if !acc.Enabled {
		return false, "account is disabled"
	}
	if acc.Guardrails.Paused {
		return false, "account scheduling is paused by guardrails"
	}
	if acc.CooldownUntil != nil && now.Before(*acc.CooldownUntil) {
		return false, fmt.Sprintf("account in rate-limit cooldown until %s", acc.CooldownUntil.Format(time.RFC3339))
	}

	// Reject non-schedulable terminal/error states (allowing expired cooldowns to pass).
	switch acc.Health {
	case models.HealthAuthExpired, models.HealthRevokedByUser, models.HealthQuarantinedDenylist, models.HealthProxyError, models.HealthPausedManual:
		return false, fmt.Sprintf("account health is %s", acc.Health)
	case models.HealthCooldownRateLimit, models.HealthMaxConcurrency:
		if acc.CooldownUntil != nil && now.Before(*acc.CooldownUntil) {
			return false, fmt.Sprintf("account in %s cooldown", acc.Health)
		}
		if acc.Health == models.HealthCooldownRateLimit && acc.CooldownUntil == nil {
			return false, "account in rate-limit cooldown"
		}
	}

	if acc.Guardrails.ExpiresAt != nil && !now.Before(*acc.Guardrails.ExpiresAt) {
		return false, "contributor authorization has expired"
	}

	if acc.Guardrails.ScheduleWindow != nil && acc.Guardrails.ScheduleWindow.Enabled {
		if !isWithinScheduleWindow(acc.Guardrails.ScheduleWindow, now) {
			return false, "outside contributor active schedule window"
		}
	}

	if len(acc.Guardrails.AllowedGPUs) > 0 && !containsAccelerator(acc.Guardrails.AllowedGPUs, requested) {
		return false, fmt.Sprintf("accelerator %s is not permitted by contributor guardrails", requested)
	}

	maxVMs := effectiveMaxVMs(acc)
	if acc.ActiveAssignments >= maxVMs {
		return false, fmt.Sprintf("max concurrent VMs reached (%d/%d)", acc.ActiveAssignments, maxVMs)
	}

	if !isAccountEntitledToGPU(acc, requested, now) {
		return false, fmt.Sprintf("account lacks entitlement or is in cooldown for %s", requested)
	}

	spec, ok := models.GetHardwareSpec(requested)
	if !ok {
		return false, fmt.Sprintf("unknown accelerator %s", requested)
	}

	burnRate := spec.DefaultBurnRateCUHr
	if ent, eOK := acc.GPUEntitlements[requested]; eOK && ent.CalibratedBurnRate > 0 {
		burnRate = ent.CalibratedBurnRate
	}

	hasFreeWindow := spec.FreeTierEligible &&
		((acc.FreeQuota != nil && (acc.FreeQuota.RemainingMilliCCU > 0 || acc.FreeQuota.RemainingFreeCU > 0)) ||
			(acc.SubscriptionTier == models.TierFree && acc.PaidCUBalance == 0))

	if acc.IsContributor && !acc.Guardrails.AllowPaidCUBurn {
		if spec.FreeTierEligible && (acc.PaidCUBalance == 0 || hasFreeWindow) {
			return true, "ok (free quota window)"
		}
		return false, "contributor disallows paid Compute Unit burn"
	}

	availCU := acc.PaidCUBalance - acc.Guardrails.MinReserveCU
	if acc.IsContributor {
		if acc.Guardrails.MaxDonatedCUTotal > 0 {
			availCU = math.Min(availCU, acc.Guardrails.MaxDonatedCUTotal-acc.Guardrails.DonatedCUUsedTotal)
		}
		if acc.Guardrails.MaxDonatedCUDaily > 0 {
			usedToday := effectiveGuardrailsDailyUsed(acc.Guardrails, now)
			availCU = math.Min(availCU, acc.Guardrails.MaxDonatedCUDaily-usedToday)
		}
	}

	// Enforce at least max(2.0 CU, 15 minutes of burn) runway above MinReserveCU.
	minRunway := math.Max(2.0, burnRate*0.25)
	if burnRate == 0 {
		minRunway = 0
	}

	if availCU < minRunway {
		// Allow opportunistic Free T4/CPU only when it cannot threaten a contributor's positive paid balance floor.
		if hasFreeWindow && (!acc.IsContributor || acc.PaidCUBalance == 0 || acc.Guardrails.MinReserveCU == 0) {
			return true, "ok (free quota window)"
		}
		return false, fmt.Sprintf("insufficient schedulable CU above reserve floor (available %.2f CU < required %.2f CU)", availCU, minRunway)
	}

	return true, "ok"
}

func effectiveGuardrailsDailyUsed(g models.ContributorGuardrails, now time.Time) float64 {
	today := now.UTC().Format("2006-01-02")
	if g.DailyUsageDate != "" && g.DailyUsageDate != today {
		return 0
	}
	return g.DonatedCUUsedToday
}

func normalizeGuardrailsDailyUsage(g *models.ContributorGuardrails, now time.Time) bool {
	if g == nil {
		return false
	}
	today := now.UTC().Format("2006-01-02")
	if g.DailyUsageDate == "" {
		g.DailyUsageDate = today
		return true
	}
	if g.DailyUsageDate != today {
		g.DonatedCUUsedToday = 0
		g.DailyUsageDate = today
		return true
	}
	return false
}

func isWithinScheduleWindow(win *models.ScheduleWindow, utcNow time.Time) bool {
	if win == nil || !win.Enabled {
		return true
	}
	loc := resolveLocationWithFallback(win.Timezone)
	local := utcNow.In(loc)

	startMin, okStart := parseHHMM(win.StartHour)
	endMin, okEnd := parseHHMM(win.EndHour)
	curMin := local.Hour()*60 + local.Minute()

	dayAllowed := func(wd time.Weekday) bool {
		if len(win.DaysOfWeek) == 0 {
			return true
		}
		for _, d := range win.DaysOfWeek {
			if d == int(wd) {
				return true
			}
		}
		return false
	}

	if !okStart || !okEnd || startMin == endMin {
		return dayAllowed(local.Weekday())
	}

	if startMin < endMin {
		// Daytime window (e.g. 09:00 -> 18:00)
		return dayAllowed(local.Weekday()) && curMin >= startMin && curMin < endMin
	}

	// Overnight window (e.g. 22:00 -> 09:00 spanning midnight)
	if curMin >= startMin {
		return dayAllowed(local.Weekday())
	}
	if curMin < endMin {
		prevDay := (local.Weekday() + 6) % 7
		return dayAllowed(prevDay)
	}
	return false
}

func parseHHMM(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func resolveLocationWithFallback(tz string) *time.Location {
	trimmed := strings.TrimSpace(tz)
	if trimmed == "" || strings.EqualFold(trimmed, "UTC") {
		return time.UTC
	}
	if loc, err := time.LoadLocation(trimmed); err == nil {
		return loc
	}
	// Defensive fallback for minimal Windows/container environments lacking zoneinfo.
	switch trimmed {
	case "Asia/Shanghai", "Asia/Hong_Kong", "Asia/Taipei", "Asia/Singapore":
		return time.FixedZone(trimmed, 8*3600)
	case "Asia/Tokyo", "Asia/Seoul":
		return time.FixedZone(trimmed, 9*3600)
	case "America/Los_Angeles", "US/Pacific":
		return time.FixedZone(trimmed, -8*3600)
	case "America/New_York", "US/Eastern":
		return time.FixedZone(trimmed, -5*3600)
	case "Europe/Berlin", "Europe/Paris":
		return time.FixedZone(trimmed, 1*3600)
	default:
		return time.UTC
	}
}
