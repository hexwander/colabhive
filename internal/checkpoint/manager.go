// Package checkpoint implements ColabHive v1.1 Pillar 4: automatic progress snapshots,
// cross-account preemption relay, and 100% PII-redacted compute savings receipts.
package checkpoint

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

var (
	// ErrCheckpointNotFound is returned when a requested checkpoint ID does not exist.
	ErrCheckpointNotFound = errors.New("checkpoint not found")

	// ErrCheckpointEmpty is returned in live mode when a remote checkpoint directory contains zero files.
	ErrCheckpointEmpty = errors.New("checkpoint sync failed: remote directory contains no files")

	emailRedactPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
)

const (
	defaultRemoteCheckpointDir = "/content/checkpoints"
	manifestFilename           = "checkpoint_manifest.json"
	internalMetaFilename       = ".colabhive_ckpt_meta.json"
	maxRemoteWalkDepth         = 3
	maxCheckpointFileCount     = 2000
	maxCheckpointFileBytes     = 64 << 20 // 64 MiB per file cap (matches runtime safe JSON transfer cap)
	maxCheckpointTotalBytes    = 1 << 30  // 1 GiB per snapshot safety cap
	maxMetadataJSONBytes       = 16 << 20 // 16 MiB cap for policy/records JSON
	resumeCleanupTimeout       = 15 * time.Second
)

// Config configures the Checkpoint & Preemption Relay Manager.
type Config struct {
	DataDir  string
	DemoMode bool
}

type persistedCheckpointState struct {
	Records         []*models.CheckpointRecord `json:"records"`
	SourceAccountID map[string]string          `json:"source_account_id,omitempty"`
}

type validatedSnapshotFile struct {
	relOS    string
	relSlash string
	size     int64
}

// Manager coordinates workspace checkpoint snapshots and cross-account preemption relays.
type Manager struct {
	mu               sync.RWMutex
	cfg              Config
	dataDir          string
	checkpointsDir   string
	pool             models.AccountPoolService
	rt               models.RuntimeEngine
	hub              models.EventHub
	policy           models.CheckpointPolicy
	records          map[string]*models.CheckpointRecord
	sourceAccountID  map[string]string
	activeRestores   map[string]int
	pendingPruneDirs map[string]struct{}
	loadErr          error
}

// NewManager creates a Checkpoint Manager backed by `<dataDir>/checkpoints/`.
func NewManager(
	dataDir string,
	pool models.AccountPoolService,
	rt models.RuntimeEngine,
	hub models.EventHub,
) *Manager {
	return NewManagerWithConfig(Config{DataDir: dataDir}, pool, rt, hub)
}

// NewManagerWithConfig creates a Checkpoint Manager with explicit Config options.
func NewManagerWithConfig(
	cfg Config,
	pool models.AccountPoolService,
	rt models.RuntimeEngine,
	hub models.EventHub,
) *Manager {
	baseDir := strings.TrimSpace(cfg.DataDir)
	if baseDir == "" || baseDir == ":memory:" {
		baseDir = filepath.Join(os.TempDir(), "colabhive-checkpoints")
	}
	ckptDir := filepath.Join(baseDir, "checkpoints")
	_ = os.MkdirAll(ckptDir, 0o700)

	m := &Manager{
		cfg:              cfg,
		dataDir:          baseDir,
		checkpointsDir:   ckptDir,
		pool:             pool,
		rt:               rt,
		hub:              hub,
		policy:           models.DefaultCheckpointPolicy(),
		records:          make(map[string]*models.CheckpointRecord),
		sourceAccountID:  make(map[string]string),
		activeRestores:   make(map[string]int),
		pendingPruneDirs: make(map[string]struct{}),
	}
	m.loadFromDisk()
	return m
}

// DataDir returns the resolved base data directory backing this manager.
func (m *Manager) DataDir() string {
	if m == nil {
		return ""
	}
	return m.dataDir
}

// LedgerError returns any error encountered while reading or parsing persisted checkpoint telemetry
// from disk (`records.json`), allowing callers such as Savings Receipt computation to fail closed
// rather than exporting unverified partial/zero totals.
func (m *Manager) LedgerError() error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loadErr
}

// MaxCheckpointFileBytes returns the maximum allowed individual checkpoint file size in bytes.
func MaxCheckpointFileBytes() int64 {
	return maxCheckpointFileBytes
}

// RedactEmailStrings strips any email addresses from human-visible labels or notes.
func RedactEmailStrings(s string) string {
	if s == "" {
		return ""
	}
	return emailRedactPattern.ReplaceAllString(s, "pooled-account")
}

// FormatSafeAccountLabel builds a friendly, 100% PII-free label for an Account without exposing raw emails.
func FormatSafeAccountLabel(acc *models.Account, fallbackAccountID string) string {
	shortID := shortAccountSuffix(fallbackAccountID)
	if acc != nil && shortID == "" {
		shortID = shortAccountSuffix(acc.ID)
	}
	if acc != nil {
		if alias := strings.TrimSpace(acc.Alias); alias != "" {
			cleanAlias := strings.TrimSpace(RedactEmailStrings(alias))
			if cleanAlias != "" && cleanAlias != "pooled-account" {
				return cleanAlias
			}
		}
		provLabel := providerFriendlyLabel(acc.EffectiveProvider())
		tierLabel := tierFriendlyBadge(acc)
		if shortID != "" {
			if tierLabel != "" {
				return fmt.Sprintf("%s · %s (#%s)", provLabel, tierLabel, shortID)
			}
			return fmt.Sprintf("%s (#%s)", provLabel, shortID)
		}
		if tierLabel != "" {
			return fmt.Sprintf("%s · %s", provLabel, tierLabel)
		}
		return provLabel
	}
	if shortID != "" {
		return fmt.Sprintf("算力池账号 #%s", shortID)
	}
	return "算力池健康账号"
}

func shortAccountSuffix(id string) string {
	clean := strings.TrimSpace(RedactEmailStrings(id))
	clean = strings.TrimPrefix(clean, "acc_")
	if len(clean) > 6 {
		return clean[:6]
	}
	return clean
}

func providerFriendlyLabel(p models.ProviderType) string {
	_ = p
	return "Colab 算力号"
}

func tierFriendlyBadge(acc *models.Account) string {
	if acc == nil {
		return ""
	}
	switch acc.SubscriptionTier {
	case models.TierAIUltra:
		return "AI Ultra"
	case models.TierProPlus:
		return "Pro+"
	case models.TierPro:
		return "Pro"
	case models.TierFree:
		return "免费卡"
	default:
		return string(acc.SubscriptionTier)
	}
}

// GetPolicy returns the active CheckpointPolicy.
func (m *Manager) GetPolicy() models.CheckpointPolicy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return clonePolicy(m.policy)
}

// UpdatePolicy validates, updates, and persists CheckpointPolicy on disk.
func (m *Manager) UpdatePolicy(next models.CheckpointPolicy) (models.CheckpointPolicy, error) {
	if next.SyncIntervalSec <= 0 {
		next.SyncIntervalSec = 60
	} else if next.SyncIntervalSec < 5 {
		next.SyncIntervalSec = 5
	} else if next.SyncIntervalSec > 86400 {
		next.SyncIntervalSec = 86400
	}

	if next.MaxSnapshotsPerJob <= 0 {
		next.MaxSnapshotsPerJob = 3
	} else if next.MaxSnapshotsPerJob > 50 {
		next.MaxSnapshotsPerJob = 50
	}

	cleanedPaths := make([]string, 0, len(next.WatchPaths))
	seen := make(map[string]struct{})
	for _, raw := range next.WatchPaths {
		cp, err := sanitizeRemotePath(raw)
		if err != nil {
			return models.CheckpointPolicy{}, err
		}
		if _, dup := seen[cp]; !dup {
			seen[cp] = struct{}{}
			cleanedPaths = append(cleanedPaths, cp)
		}
	}
	if len(cleanedPaths) == 0 {
		cleanedPaths = []string{"/content/checkpoints", "/content/outputs"}
	}
	next.WatchPaths = cleanedPaths
	next.UpdatedAt = time.Now().UTC()

	m.mu.Lock()
	prev := m.policy
	m.policy = next
	polCopy := clonePolicy(m.policy)
	err := m.writeRootJSONAtomicLocked("policy.json", polCopy)
	if err != nil {
		m.policy = prev
	}
	m.mu.Unlock()
	if err != nil {
		return models.CheckpointPolicy{}, fmt.Errorf("persist checkpoint policy: %w", err)
	}
	return polCopy, nil
}

// ListCheckpoints returns all tracked CheckpointRecords ordered newest UpdatedAt first.
func (m *Manager) ListCheckpoints() []*models.CheckpointRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*models.CheckpointRecord, 0, len(m.records))
	for _, rec := range m.records {
		if rec != nil {
			out = append(out, cloneRecord(rec))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// GetCheckpoint returns a single CheckpointRecord by ID.
func (m *Manager) GetCheckpoint(id string) (*models.CheckpointRecord, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rec, ok := m.records[strings.TrimSpace(id)]
	if !ok || rec == nil {
		return nil, ErrCheckpointNotFound
	}
	return cloneRecord(rec), nil
}

// LatestCheckpointForJob finds the most recently updated CheckpointRecord for a jobID or sessionID.
func (m *Manager) LatestCheckpointForJob(jobID string) (*models.CheckpointRecord, bool) {
	target := strings.TrimSpace(jobID)
	if target == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	var best *models.CheckpointRecord
	for _, rec := range m.records {
		if rec == nil {
			continue
		}
		if rec.JobID == target || rec.SourceSessionID == target || rec.ResumedSessionID == target {
			if best == nil || rec.UpdatedAt.After(best.UpdatedAt) {
				best = rec
			}
		}
	}
	if best == nil {
		return nil, false
	}
	return cloneRecord(best), true
}

// SyncSessionCheckpoint stages and downloads files from RemotePath (default `/content/checkpoints`)
// into an immutable unique `<dataDir>/checkpoints/<checkpointID>/` directory using root-confined I/O,
// publishing metadata ONLY after all downloads complete cleanly.
func (m *Manager) SyncSessionCheckpoint(
	ctx context.Context,
	req models.CheckpointSyncRequest,
) (*models.CheckpointRecord, error) {
	if m.rt == nil {
		return nil, errors.New("runtime engine is required for checkpoint sync")
	}
	sessID := strings.TrimSpace(req.SessionID)
	if sessID == "" {
		return nil, errors.New("invalid checkpoint sync request: session_id is required")
	}

	if strings.TrimSpace(req.ResumeCommand) != "" {
		if _, rErr := validateResumeCommand(req.ResumeCommand); rErr != nil {
			return nil, rErr
		}
	}

	sess, err := m.rt.GetSession(ctx, sessID)
	if err != nil {
		return nil, fmt.Errorf("lookup session %q: %w", sessID, err)
	}

	remotePath := strings.TrimSpace(req.RemotePath)
	if remotePath == "" {
		remotePath = defaultRemoteCheckpointDir
	}
	cleanRemote, err := sanitizeRemotePath(remotePath)
	if err != nil {
		return nil, err
	}

	rawJobID := strings.TrimSpace(req.JobID)
	if rawJobID == "" {
		if sess != nil && sess.ExperimentID != "" {
			rawJobID = sess.ExperimentID
		} else {
			rawJobID = sessID
		}
	}
	safeJobID := sanitizeSafeDirSegment(rawJobID)

	jobKind := strings.ToLower(strings.TrimSpace(req.JobKind))
	switch jobKind {
	case "app", "experiment", "session", "llm":
	default:
		jobKind = "session"
	}

	jobTitle := strings.TrimSpace(RedactEmailStrings(req.JobTitle))
	if jobTitle == "" {
		if sess != nil && strings.TrimSpace(sess.Name) != "" {
			jobTitle = RedactEmailStrings(sess.Name)
		} else {
			jobTitle = safeJobID
		}
	}

	var srcAcc *models.Account
	var srcAccountID string
	if sess != nil {
		srcAccountID = sess.AccountID
		if m.pool != nil && srcAccountID != "" {
			srcAcc, _ = m.pool.GetAccount(ctx, srcAccountID)
		}
	}
	srcLabel := FormatSafeAccountLabel(srcAcc, srcAccountID)

	if mkErr := os.MkdirAll(m.checkpointsDir, 0o700); mkErr != nil {
		return nil, fmt.Errorf("create checkpoints root dir: %w", mkErr)
	}
	ckptRoot, err := os.OpenRoot(m.checkpointsDir)
	if err != nil {
		return nil, fmt.Errorf("open checkpoints root: %w", err)
	}
	defer ckptRoot.Close()

	ckptID := models.NewPrefixedID("ckpt_")
	if err := validateSafeChildName(ckptID); err != nil {
		return nil, fmt.Errorf("invalid generated checkpoint id: %w", err)
	}

	if err := ckptRoot.Mkdir(ckptID, 0o700); err != nil {
		return nil, fmt.Errorf("create immutable staging dir: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = ckptRoot.RemoveAll(ckptID)
		}
	}()

	snapRoot, err := ckptRoot.OpenRoot(ckptID)
	if err != nil {
		return nil, fmt.Errorf("open snapshot staging root: %w", err)
	}

	downloadedFiles, totalBytes, dlErr := m.downloadRemoteTree(ctx, sessID, cleanRemote, snapRoot)
	if dlErr != nil {
		_ = snapRoot.Close()
		return nil, dlErr
	}

	now := time.Now().UTC()

	// In live mode (DemoMode == false), incomplete or empty snapshots MUST fail loudly.
	// Only DemoMode permits synthetic manifest-only snapshots when remote files are absent.
	if downloadedFiles == 0 {
		if !m.cfg.DemoMode {
			_ = snapRoot.Close()
			return nil, fmt.Errorf("%w: %q", ErrCheckpointEmpty, cleanRemote)
		}
		manifest := map[string]any{
			"checkpoint_id":     ckptID,
			"job_id":            safeJobID,
			"job_title":         jobTitle,
			"job_kind":          jobKind,
			"source_session_id": sessID,
			"remote_path":       cleanRemote,
			"progress_pct":      clampPct(req.ProgressPct),
			"resume_command":    strings.TrimSpace(req.ResumeCommand),
			"synced_at":         now.Format(time.RFC3339),
		}
		manifestRaw, _ := json.MarshalIndent(manifest, "", "  ")
		manifestRaw = append(manifestRaw, '\n')
		if wErr := writeBytesAtomicInRoot(snapRoot, manifestFilename, manifestRaw); wErr != nil {
			_ = snapRoot.Close()
			return nil, fmt.Errorf("write checkpoint manifest: %w", wErr)
		}
		downloadedFiles = 1
		totalBytes = int64(len(manifestRaw))
	}

	if cErr := snapRoot.Close(); cErr != nil {
		return nil, fmt.Errorf("close snapshot staging root: %w", cErr)
	}

	var assignedGPU models.AcceleratorType
	if sess != nil {
		assignedGPU = sess.Accelerator
	}

	localDir := filepath.Join(m.checkpointsDir, ckptID)
	rec := &models.CheckpointRecord{
		ID:                 ckptID,
		JobID:              safeJobID,
		JobTitle:           jobTitle,
		JobKind:            jobKind,
		SourceSessionID:    sessID,
		SourceAccountLabel: srcLabel,
		AssignedGPU:        assignedGPU,
		RemotePath:         cleanRemote,
		LocalSnapshotDir:   localDir,
		FileCount:          downloadedFiles,
		TotalBytes:         totalBytes,
		ProgressPct:        clampPct(req.ProgressPct),
		RelayCount:         0,
		Status:             "synced",
		ResumeCommand:      strings.TrimSpace(req.ResumeCommand),
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	m.mu.Lock()
	m.records[rec.ID] = rec
	if srcAccountID != "" {
		m.sourceAccountID[rec.ID] = srcAccountID
	}
	dirsToPrune := m.pruneJobSnapshotsLocked(safeJobID)
	persistErr := m.persistRecordsLocked()
	if persistErr != nil {
		delete(m.records, rec.ID)
		delete(m.sourceAccountID, rec.ID)
		m.mu.Unlock()
		return nil, fmt.Errorf("persist checkpoint record: %w", persistErr)
	}
	committed = true
	recOut := cloneRecord(rec)
	m.mu.Unlock()

	for _, pruneDir := range dirsToPrune {
		if validateSafeChildName(pruneDir) == nil {
			_ = ckptRoot.RemoveAll(pruneDir)
		}
	}

	return recOut, nil
}

// ResumeFromCheckpoint validates snapshot & local files BEFORE allocating a new GPU VM,
// creates remote parent directories before upload, verifies ResumeCommand exit code,
// cleans up any newly allocated VM on upload/resume failure using context.WithoutCancel(ctx)
// with a 15s bounded timeout, increments RelayCount, sets Status = "resumed", and broadcasts WSEventCheckpointRelay.
func (m *Manager) ResumeFromCheckpoint(
	ctx context.Context,
	checkpointID string,
	req models.CheckpointResumeRequest,
) (*models.CheckpointRecord, error) {
	if m.rt == nil {
		return nil, errors.New("runtime engine is required for checkpoint resume")
	}
	id := strings.TrimSpace(checkpointID)
	if id == "" {
		return nil, errors.New("invalid checkpoint id: required")
	}

	if strings.TrimSpace(req.ResumeCommand) != "" {
		if _, cmdErr := validateResumeCommand(req.ResumeCommand); cmdErr != nil {
			return nil, cmdErr
		}
	}

	m.mu.Lock()
	existing, ok := m.records[id]
	if !ok || existing == nil {
		m.mu.Unlock()
		return nil, ErrCheckpointNotFound
	}
	recSnap := cloneRecord(existing)
	srcAccID := m.sourceAccountID[id]
	m.activeRestores[id]++
	m.mu.Unlock()

	// Release active restore pin and execute deferred directory pruning if evicted while restoring.
	var snapRoot *os.Root
	var ckptRoot *os.Root
	defer func() {
		if snapRoot != nil {
			_ = snapRoot.Close()
		}
		if ckptRoot != nil {
			_ = ckptRoot.Close()
		}
		m.releaseActiveRestore(id)
	}()

	resumeCmd := strings.TrimSpace(req.ResumeCommand)
	if resumeCmd == "" {
		resumeCmd = strings.TrimSpace(recSnap.ResumeCommand)
	}
	if resumeCmd != "" {
		cleanResumeCmd, cmdErr := validateResumeCommand(resumeCmd)
		if cmdErr != nil {
			return nil, cmdErr
		}
		resumeCmd = cleanResumeCmd
	}

	cleanRemoteRoot, sErr := sanitizeRemotePath(recSnap.RemotePath)
	if sErr != nil {
		return nil, fmt.Errorf("invalid checkpoint remote_path: %w", sErr)
	}

	// Pre-validate local snapshot directory and every local file BEFORE allocating any GPU VM!
	relSnapDir, relErr := m.resolveConfinedSnapshotSubdir(recSnap)
	if relErr != nil {
		return nil, relErr
	}

	var openErr error
	ckptRoot, openErr = os.OpenRoot(m.checkpointsDir)
	if openErr != nil {
		return nil, fmt.Errorf("open checkpoints root: %w", openErr)
	}
	dirInfo, lstatErr := ckptRoot.Lstat(relSnapDir)
	if lstatErr != nil {
		return nil, fmt.Errorf("checkpoint snapshot directory missing: %w", lstatErr)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return nil, fmt.Errorf("invalid checkpoint snapshot directory %q: must be a real directory", relSnapDir)
	}
	snapRoot, openErr = ckptRoot.OpenRoot(relSnapDir)
	if openErr != nil {
		return nil, fmt.Errorf("open snapshot root %q: %w", relSnapDir, openErr)
	}

	stagedFiles, walkErr := collectValidatedSnapshotFiles(snapRoot)
	if walkErr != nil {
		return nil, fmt.Errorf("validate local checkpoint snapshot: %w", walkErr)
	}
	if len(stagedFiles) == 0 && !m.cfg.DemoMode {
		return nil, errors.New("checkpoint snapshot contains no local files to restore")
	}

	// Pre-validation passed — transition record to relaying.
	m.mu.Lock()
	if cur, exists := m.records[id]; exists && cur != nil {
		cur.Status = "relaying"
		cur.UpdatedAt = time.Now().UTC()
	}
	m.mu.Unlock()

	if srcAccID == "" && recSnap.SourceSessionID != "" {
		if oldSess, gErr := m.rt.GetSession(ctx, recSnap.SourceSessionID); gErr == nil && oldSess != nil {
			srcAccID = oldSess.AccountID
		}
	}

	targetGPU := req.PreferredGPU
	if targetGPU == "" {
		targetGPU = recSnap.AssignedGPU
	}
	if targetGPU == "" {
		targetGPU = models.AcceleratorT4
	}

	strategy := req.Strategy
	if strategy == "" {
		strategy = models.StrategyFreeFirst
	}

	baseAlloc := models.SessionAllocRequest{
		Name:         fmt.Sprintf("resume-%s", sanitizeSafeDirSegment(recSnap.JobID)),
		Accelerator:  targetGPU,
		Strategy:     strategy,
		AutoFallback: true,
		CallerName:   "checkpoint-relay",
	}

	var newSess *models.RuntimeSession
	var allocErr error
	if srcAccID != "" {
		crossAccountReq := baseAlloc
		crossAccountReq.ExcludedAccountIDs = []string{srcAccID}
		newSess, allocErr = m.rt.CreateSession(ctx, crossAccountReq)
	}
	if newSess == nil {
		// Fall back to any healthy account if pool has only 1 account or exclusion had no match.
		newSess, allocErr = m.rt.CreateSession(ctx, baseAlloc)
	}
	if allocErr != nil || newSess == nil {
		m.markRecordFailed(id)
		if allocErr != nil {
			return nil, fmt.Errorf("allocate relay GPU session: %w", allocErr)
		}
		return nil, errors.New("allocate relay GPU session failed")
	}

	cleanupNewSession := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resumeCleanupTimeout)
		defer cancel()
		_ = m.rt.StopSession(cleanupCtx, newSess.ID)
	}

	uploadedCount, uploadedBytes, upErr := m.uploadValidatedSnapshot(ctx, newSess.ID, snapRoot, stagedFiles, cleanRemoteRoot)
	if upErr != nil {
		cleanupNewSession()
		m.markRecordFailed(id)
		return nil, fmt.Errorf("restore snapshot files to %s: %w", newSess.ID, upErr)
	}

	if resumeCmd != "" {
		if runErr := m.executeAndVerifyResumeCommand(ctx, newSess.ID, cleanRemoteRoot, resumeCmd); runErr != nil {
			cleanupNewSession()
			m.markRecordFailed(id)
			return nil, fmt.Errorf("execute resume command on %s: %w", newSess.ID, runErr)
		}
	}

	var resumedAcc *models.Account
	if m.pool != nil && newSess.AccountID != "" {
		resumedAcc, _ = m.pool.GetAccount(ctx, newSess.AccountID)
	}
	resumedLabel := FormatSafeAccountLabel(resumedAcc, newSess.AccountID)

	now := time.Now().UTC()
	const noteZh = "原账号掉线/额度耗尽 → 已自动同步快照并在新账号无缝续跑"
	const noteEn = "Original account offline/quota exhausted → Snapshot synced and seamlessly resumed on next healthy account"

	m.mu.Lock()
	cur, ok := m.records[id]
	_, wasPruned := m.pendingPruneDirs[id]
	var updatedCopy *models.CheckpointRecord
	var persistErr error
	if ok && cur != nil && !wasPruned {
		cur.ResumedSessionID = newSess.ID
		cur.ResumedAccountLabel = resumedLabel
		cur.AssignedGPU = newSess.Accelerator
		if uploadedCount > 0 {
			cur.FileCount = uploadedCount
			cur.TotalBytes = uploadedBytes
		}
		cur.RelayCount++
		cur.Status = "resumed"
		cur.LastRelayNoteZh = noteZh
		cur.LastRelayNoteEn = noteEn
		if resumeCmd != "" {
			cur.ResumeCommand = resumeCmd
		}
		cur.UpdatedAt = now
		if newSess.AccountID != "" {
			m.sourceAccountID[id] = newSess.AccountID
		}
		persistErr = m.persistRecordsLocked()
		updatedCopy = cloneRecord(cur)
	} else {
		recSnap.ResumedSessionID = newSess.ID
		recSnap.ResumedAccountLabel = resumedLabel
		recSnap.AssignedGPU = newSess.Accelerator
		if uploadedCount > 0 {
			recSnap.FileCount = uploadedCount
			recSnap.TotalBytes = uploadedBytes
		}
		recSnap.RelayCount++
		recSnap.Status = "resumed"
		recSnap.LastRelayNoteZh = noteZh
		recSnap.LastRelayNoteEn = noteEn
		if resumeCmd != "" {
			recSnap.ResumeCommand = resumeCmd
		}
		recSnap.UpdatedAt = now
		updatedCopy = cloneRecord(recSnap)
	}
	m.mu.Unlock()

	if persistErr != nil {
		return nil, fmt.Errorf("persist resumed checkpoint: %w", persistErr)
	}

	if m.hub != nil {
		m.hub.Publish(models.WSEvent{
			Type:      models.WSEventCheckpointRelay,
			Timestamp: now,
			Payload:   updatedCopy,
		})
	}

	return updatedCopy, nil
}

// RestoreSnapshotIntoSession validates and uploads an existing synced checkpoint snapshot directly
// into an already-allocated target GPU VM session (used by multi-path failover and checkpoint-protected Restart).
// Pass `countAsAutoRelay=false` on manual Restart and secondary failover paths so only genuine automatic
// preemption relays increment `RelayCount` once per failover event.
func (m *Manager) RestoreSnapshotIntoSession(
	ctx context.Context,
	checkpointID string,
	targetSessionID string,
	countAsAutoRelay bool,
) (*models.CheckpointRecord, error) {
	if m.rt == nil {
		return nil, errors.New("runtime engine is required to restore checkpoint")
	}
	id := strings.TrimSpace(checkpointID)
	sessID := strings.TrimSpace(targetSessionID)
	if id == "" || sessID == "" {
		return nil, errors.New("checkpoint_id and target_session_id are required")
	}

	m.mu.Lock()
	rec, ok := m.records[id]
	if !ok || rec == nil {
		m.mu.Unlock()
		return nil, ErrCheckpointNotFound
	}
	if rec.Status != "synced" && rec.Status != "resumed" {
		m.mu.Unlock()
		return nil, fmt.Errorf("checkpoint %s is not restorable (status=%s)", id, rec.Status)
	}
	recSnap := cloneRecord(rec)
	m.activeRestores[id]++
	m.mu.Unlock()

	defer m.releaseActiveRestore(id)

	cleanRemoteRoot, err := sanitizeRemotePath(recSnap.RemotePath)
	if err != nil {
		return nil, fmt.Errorf("invalid checkpoint remote_path: %w", err)
	}

	relSnapDir, relErr := m.resolveConfinedSnapshotSubdir(recSnap)
	if relErr != nil {
		return nil, relErr
	}
	ckptRoot, openErr := os.OpenRoot(m.checkpointsDir)
	if openErr != nil {
		return nil, fmt.Errorf("open checkpoints root: %w", openErr)
	}
	defer ckptRoot.Close()
	dirInfo, lstatErr := ckptRoot.Lstat(relSnapDir)
	if lstatErr != nil {
		return nil, fmt.Errorf("checkpoint snapshot directory missing: %w", lstatErr)
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return nil, fmt.Errorf("invalid checkpoint snapshot directory %q: must be a real directory", relSnapDir)
	}
	snapRoot, openErr := ckptRoot.OpenRoot(relSnapDir)
	if openErr != nil {
		return nil, fmt.Errorf("open snapshot root %q: %w", relSnapDir, openErr)
	}
	defer snapRoot.Close()

	stagedFiles, walkErr := collectValidatedSnapshotFiles(snapRoot)
	if walkErr != nil {
		return nil, fmt.Errorf("validate local checkpoint snapshot: %w", walkErr)
	}
	if len(stagedFiles) == 0 && !m.cfg.DemoMode {
		return nil, errors.New("checkpoint snapshot contains no local files to restore")
	}

	uploadedCount, uploadedBytes, upErr := m.uploadValidatedSnapshot(ctx, sessID, snapRoot, stagedFiles, cleanRemoteRoot)
	if upErr != nil {
		return nil, fmt.Errorf("restore snapshot %s files to %s: %w", id, sessID, upErr)
	}

	var accLabel string
	var accID string
	var assignedGPU models.AcceleratorType
	if sess, gErr := m.rt.GetSession(ctx, sessID); gErr == nil && sess != nil {
		accID = sess.AccountID
		assignedGPU = sess.Accelerator
		var acc *models.Account
		if m.pool != nil && accID != "" {
			acc, _ = m.pool.GetAccount(ctx, accID)
		}
		accLabel = FormatSafeAccountLabel(acc, accID)
	}

	now := time.Now().UTC()
	m.mu.Lock()
	cur, exists := m.records[id]
	_, wasPruned := m.pendingPruneDirs[id]
	var updatedCopy *models.CheckpointRecord
	if exists && cur != nil && !wasPruned {
		cur.ResumedSessionID = sessID
		if accLabel != "" {
			cur.ResumedAccountLabel = accLabel
		}
		if assignedGPU != "" {
			cur.AssignedGPU = assignedGPU
		}
		if uploadedCount > 0 {
			cur.FileCount = uploadedCount
			cur.TotalBytes = uploadedBytes
		}
		if countAsAutoRelay {
			cur.RelayCount++
		}
		cur.Status = "resumed"
		cur.UpdatedAt = now
		if accID != "" {
			m.sourceAccountID[id] = accID
		}
		_ = m.persistRecordsLocked()
		updatedCopy = cloneRecord(cur)
	} else {
		recSnap.ResumedSessionID = sessID
		recSnap.Status = "resumed"
		recSnap.UpdatedAt = now
		updatedCopy = cloneRecord(recSnap)
	}
	m.mu.Unlock()

	if m.hub != nil {
		m.hub.Publish(models.WSEvent{
			Type:      models.WSEventCheckpointRelay,
			Timestamp: now,
			Payload:   updatedCopy,
		})
	}
	return updatedCopy, nil
}

func (m *Manager) markRecordFailed(id string) {
	m.mu.Lock()
	if cur, exists := m.records[id]; exists && cur != nil {
		cur.Status = "failed"
		cur.UpdatedAt = time.Now().UTC()
		_ = m.persistRecordsLocked()
	}
	m.mu.Unlock()
}

func (m *Manager) releaseActiveRestore(id string) {
	var shouldPrune bool
	m.mu.Lock()
	if cur := m.activeRestores[id]; cur <= 1 {
		delete(m.activeRestores, id)
		if _, pending := m.pendingPruneDirs[id]; pending {
			delete(m.pendingPruneDirs, id)
			shouldPrune = true
		}
	} else {
		m.activeRestores[id] = cur - 1
	}
	m.mu.Unlock()

	if shouldPrune && validateSafeChildName(id) == nil {
		if root, err := os.OpenRoot(m.checkpointsDir); err == nil {
			_ = root.RemoveAll(id)
			_ = root.Close()
		}
	}
}

func (m *Manager) resolveConfinedSnapshotSubdir(rec *models.CheckpointRecord) (string, error) {
	if rec == nil {
		return "", errors.New("checkpoint record is nil")
	}
	localDir := strings.TrimSpace(rec.LocalSnapshotDir)
	if localDir == "" {
		if validateSafeChildName(rec.ID) == nil {
			return rec.ID, nil
		}
		return "", errors.New("checkpoint snapshot directory is empty")
	}
	absBase, err := filepath.Abs(m.checkpointsDir)
	if err != nil {
		return "", fmt.Errorf("resolve checkpoints base dir: %w", err)
	}
	absSnap, err := filepath.Abs(localDir)
	if err != nil {
		return "", fmt.Errorf("resolve snapshot dir: %w", err)
	}
	rel, err := filepath.Rel(absBase, absSnap)
	if err != nil || rel == "." || rel == ".." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("checkpoint snapshot dir %q escapes checkpoints root", localDir)
	}
	// Ensure every segment in rel is a safe child name (no traversal or separator tricks).
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if err := validateSafeChildName(seg); err != nil {
			return "", fmt.Errorf("unsafe snapshot dir segment %q: %w", seg, err)
		}
	}
	return rel, nil
}

func isRemoteDirMissingErr(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such file or directory") ||
		strings.Contains(lower, "file or directory does not exist") ||
		strings.Contains(lower, "directory does not exist") ||
		(strings.Contains(lower, "http 404") && (strings.Contains(lower, "does not exist") || strings.Contains(lower, "file or directory")))
}

func (m *Manager) downloadRemoteTree(
	ctx context.Context,
	sessionID string,
	rootRemote string,
	snapRoot *os.Root,
) (fileCount int, totalBytes int64, err error) {
	type dirItem struct {
		remote string
		relOS  string
		depth  int
	}
	queue := []dirItem{{remote: rootRemote, relOS: "", depth: 0}}

	for len(queue) > 0 {
		if ctx.Err() != nil {
			return 0, 0, ctx.Err()
		}
		cur := queue[0]
		queue = queue[1:]

		entries, listErr := m.rt.ListFiles(ctx, sessionID, cur.remote)
		if listErr != nil {
			if cur.depth == 0 && (m.cfg.DemoMode || isRemoteDirMissingErr(listErr)) {
				return 0, 0, nil
			}
			return 0, 0, fmt.Errorf("list remote checkpoint directory %q: %w", cur.remote, listErr)
		}

		for _, ent := range entries {
			name := ent.Name
			if name == ".ipynb_checkpoints" || name == internalMetaFilename {
				continue
			}
			if vErr := validateSafeChildName(name); vErr != nil {
				return 0, 0, fmt.Errorf("unsafe remote checkpoint entry: %w", vErr)
			}

			// NEVER trust VM-supplied ent.Path; construct remote path strictly from validated parent + validated child name.
			entRemote := path.Join(cur.remote, name)
			entRelOS := name
			if cur.relOS != "" {
				entRelOS = filepath.Join(cur.relOS, name)
			}

			if strings.EqualFold(ent.Type, "directory") || strings.EqualFold(ent.Type, "dir") {
				if cur.depth+1 > maxRemoteWalkDepth {
					return 0, 0, fmt.Errorf("checkpoint directory %q exceeds maximum nesting depth (%d); aborting incomplete snapshot", entRemote, maxRemoteWalkDepth)
				}
				queue = append(queue, dirItem{
					remote: entRemote,
					relOS:  entRelOS,
					depth:  cur.depth + 1,
				})
				continue
			}

			if fileCount >= maxCheckpointFileCount {
				return 0, 0, fmt.Errorf("checkpoint snapshot exceeds maximum file count (%d); aborting incomplete snapshot", maxCheckpointFileCount)
			}

			if ent.SizeBytes > maxCheckpointFileBytes {
				return 0, 0, fmt.Errorf("remote file %q exceeds max checkpoint file size (%d bytes)", entRemote, maxCheckpointFileBytes)
			}
			if ent.SizeBytes > 0 && totalBytes+ent.SizeBytes > maxCheckpointTotalBytes {
				return 0, 0, fmt.Errorf("checkpoint snapshot exceeds total size cap (%d bytes)", maxCheckpointTotalBytes)
			}

			data, dlErr := m.rt.DownloadFile(ctx, sessionID, entRemote)
			if dlErr != nil {
				return 0, 0, fmt.Errorf("download remote file %q: %w", entRemote, dlErr)
			}
			if int64(len(data)) > maxCheckpointFileBytes {
				return 0, 0, fmt.Errorf("downloaded file %q exceeds max checkpoint file size (%d bytes)", entRemote, maxCheckpointFileBytes)
			}
			if totalBytes+int64(len(data)) > maxCheckpointTotalBytes {
				return 0, 0, fmt.Errorf("checkpoint snapshot exceeds total size cap (%d bytes)", maxCheckpointTotalBytes)
			}

			if wErr := writeBytesAtomicInRoot(snapRoot, entRelOS, data); wErr != nil {
				return 0, 0, fmt.Errorf("stage checkpoint file %q: %w", entRelOS, wErr)
			}
			fileCount++
			totalBytes += int64(len(data))
		}
	}
	return fileCount, totalBytes, nil
}

func collectValidatedSnapshotFiles(snapRoot *os.Root) ([]validatedSnapshotFile, error) {
	var files []validatedSnapshotFile
	var totalBytes int64

	type walkDir struct {
		relOS string
		depth int
	}
	stack := []walkDir{{relOS: ".", depth: 0}}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		f, err := snapRoot.Open(cur.relOS)
		if err != nil {
			return nil, err
		}
		entries, readErr := f.ReadDir(-1)
		_ = f.Close()
		if readErr != nil {
			return nil, readErr
		}

		for _, ent := range entries {
			name := ent.Name()
			if vErr := validateSafeChildName(name); vErr != nil {
				return nil, fmt.Errorf("unsafe local snapshot entry %q: %w", name, vErr)
			}
			childRel := name
			if cur.relOS != "." {
				childRel = filepath.Join(cur.relOS, name)
			}

			info, lErr := snapRoot.Lstat(childRel)
			if lErr != nil {
				return nil, lErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("symlink %q is forbidden inside checkpoint snapshot", filepath.ToSlash(childRel))
			}
			if info.IsDir() {
				if cur.depth+1 > maxRemoteWalkDepth {
					return nil, fmt.Errorf("snapshot directory %q exceeds maximum nesting depth", filepath.ToSlash(childRel))
				}
				stack = append(stack, walkDir{relOS: childRel, depth: cur.depth + 1})
				continue
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("non-regular file %q is forbidden inside checkpoint snapshot", filepath.ToSlash(childRel))
			}
			if name == internalMetaFilename || strings.HasPrefix(name, ".tmp-") {
				continue
			}
			if info.Size() > maxCheckpointFileBytes {
				return nil, fmt.Errorf("snapshot file %q exceeds max checkpoint file size (%d bytes)", filepath.ToSlash(childRel), maxCheckpointFileBytes)
			}
			if totalBytes+info.Size() > maxCheckpointTotalBytes {
				return nil, fmt.Errorf("snapshot exceeds total size limit (%d bytes)", maxCheckpointTotalBytes)
			}
			if len(files) >= maxCheckpointFileCount {
				return nil, fmt.Errorf("snapshot exceeds maximum file count (%d)", maxCheckpointFileCount)
			}
			totalBytes += info.Size()
			files = append(files, validatedSnapshotFile{
				relOS:    childRel,
				relSlash: filepath.ToSlash(childRel),
				size:     info.Size(),
			})
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].relSlash < files[j].relSlash
	})
	return files, nil
}

func (m *Manager) uploadValidatedSnapshot(
	ctx context.Context,
	sessionID string,
	snapRoot *os.Root,
	files []validatedSnapshotFile,
	cleanRemoteRoot string,
) (count int, totalBytes int64, err error) {
	// Collect unique remote parent directories and create them BEFORE uploading files.
	dirSet := map[string]struct{}{cleanRemoteRoot: {}}
	for _, f := range files {
		targetRemote := path.Join(cleanRemoteRoot, f.relSlash)
		parent := path.Dir(targetRemote)
		if parent != "" && parent != "." && parent != "/" {
			dirSet[parent] = struct{}{}
		}
	}
	dirs := make([]string, 0, len(dirSet))
	for d := range dirSet {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	if mkErr := m.ensureRemoteDirectories(ctx, sessionID, dirs); mkErr != nil {
		return 0, 0, fmt.Errorf("create remote checkpoint directories: %w", mkErr)
	}

	for _, f := range files {
		raw, readErr := readRegularFileLimitedInRoot(snapRoot, f.relOS, maxCheckpointFileBytes)
		if readErr != nil {
			return count, totalBytes, readErr
		}
		targetRemote := path.Join(cleanRemoteRoot, f.relSlash)
		if uErr := m.rt.UploadFile(ctx, sessionID, targetRemote, raw); uErr != nil {
			return count, totalBytes, uErr
		}
		count++
		totalBytes += int64(len(raw))
	}

	if count == 0 {
		if !m.cfg.DemoMode {
			return 0, 0, errors.New("snapshot contains zero files to upload")
		}
		fallbackPayload, _ := json.MarshalIndent(map[string]any{
			"resumed_session_id": sessionID,
			"remote_path":        cleanRemoteRoot,
			"restored_at":        time.Now().UTC().Format(time.RFC3339),
		}, "", "  ")
		fallbackPayload = append(fallbackPayload, '\n')
		targetRemote := path.Join(cleanRemoteRoot, manifestFilename)
		if uErr := m.rt.UploadFile(ctx, sessionID, targetRemote, fallbackPayload); uErr != nil {
			return 0, 0, uErr
		}
		count = 1
		totalBytes = int64(len(fallbackPayload))
	}
	return count, totalBytes, nil
}

func (m *Manager) ensureRemoteDirectories(ctx context.Context, sessionID string, dirs []string) error {
	if len(dirs) == 0 {
		return nil
	}
	dirsJSON, err := json.Marshal(dirs)
	if err != nil {
		return err
	}
	code := strings.Join([]string{
		"import json, os",
		"for p in json.loads(os.environ.get('COLABHIVE_MKDIR_PATHS_JSON', '[]')):",
		"    os.makedirs(p, exist_ok=True)",
		"print('[colabhive-checkpoint] remote dirs ready')",
	}, "\n")
	res, err := m.rt.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID: sessionID,
		Code:      code,
		Env: map[string]string{
			"COLABHIVE_MKDIR_PATHS_JSON": string(dirsJSON),
		},
		Silent:         true,
		StoreHistory:   false,
		IdleTimeoutSec: 30,
		CallerName:     "checkpoint-mkdir",
	})
	if err != nil {
		return err
	}
	if res != nil && (strings.EqualFold(res.Status, "error") || strings.EqualFold(res.Status, "abort")) {
		if res.ExecutionError != nil {
			return fmt.Errorf("%s: %s", res.ExecutionError.Name, res.ExecutionError.Value)
		}
		return fmt.Errorf("remote mkdir returned status %q", res.Status)
	}
	return nil
}

func (m *Manager) executeAndVerifyResumeCommand(
	ctx context.Context,
	sessionID string,
	remoteRoot string,
	resumeCmd string,
) error {
	execCode := strings.Join([]string{
		"import os, shlex, subprocess, sys, time",
		"cmd = os.environ.get('COLABHIVE_RESUME_CMD', '').strip()",
		"ckpt_dir = os.environ.get('COLABHIVE_CHECKPOINT_DIR', '/content/checkpoints')",
		"os.makedirs(ckpt_dir, exist_ok=True)",
		"if cmd:",
		"    argv = shlex.split(cmd)",
		"    log_path = '/content/colabhive_resume.log'",
		"    with open(log_path, 'a', encoding='utf-8') as log_fh:",
		"        proc = subprocess.Popen(argv, shell=False, cwd='/content', stdout=log_fh, stderr=subprocess.STDOUT)",
		"    time.sleep(0.5)",
		"    rc = proc.poll()",
		"    if rc is not None and rc != 0:",
		"        tail = ''",
		"        try:",
		"            with open(log_path, 'r', encoding='utf-8', errors='replace') as rf:",
		"                tail = rf.read()[-1000:]",
		"        except Exception:",
		"            pass",
		"        print(f'[colabhive-checkpoint] resume command exited with code {rc}: {tail}', file=sys.stderr)",
		"        raise SystemExit(rc)",
		"    print(f'[colabhive-checkpoint] resumed pid={proc.pid} dir={ckpt_dir}')",
	}, "\n")

	res, err := m.rt.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID: sessionID,
		Code:      execCode,
		Env: map[string]string{
			"COLABHIVE_RESUME_CMD":     resumeCmd,
			"COLABHIVE_CHECKPOINT_DIR": remoteRoot,
		},
		Silent:         true,
		StoreHistory:   false,
		IdleTimeoutSec: 60,
		CallerName:     "checkpoint-relay",
	})
	if err != nil {
		return err
	}
	if res == nil {
		return nil
	}
	if res.ExecutionError != nil {
		if res.ExecutionError.ExitCode != nil {
			if *res.ExecutionError.ExitCode != 0 {
				return fmt.Errorf("resume command exited with code %d: %s", *res.ExecutionError.ExitCode, strings.TrimSpace(res.ExecutionError.Value))
			}
		} else {
			return fmt.Errorf("resume command error (%s): %s", res.ExecutionError.Name, strings.TrimSpace(res.ExecutionError.Value))
		}
	}
	if strings.EqualFold(res.Status, "error") || strings.EqualFold(res.Status, "abort") {
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = fmt.Sprintf("kernel execution status=%s", res.Status)
		}
		return fmt.Errorf("resume command failed: %s", detail)
	}
	return nil
}

// pruneJobSnapshotsLocked enforces MaxSnapshotsPerJob independently per `(JobID, RemotePath)` bucket
// so syncing a secondary watch path (e.g. `/content/outputs`) NEVER prunes the latest backup of the
// instance's primary workspace directory (e.g. `/content` or `/content/ComfyUI/output`) even when MaxSnapshotsPerJob == 1!
func (m *Manager) pruneJobSnapshotsLocked(jobID string) (dirsToRemoveNow []string) {
	maxKeep := m.policy.MaxSnapshotsPerJob
	if maxKeep <= 0 {
		maxKeep = 3
	}
	byPath := make(map[string][]*models.CheckpointRecord)
	for _, r := range m.records {
		if r != nil && r.JobID == jobID {
			rp := strings.TrimSpace(r.RemotePath)
			byPath[rp] = append(byPath[rp], r)
		}
	}
	for _, matching := range byPath {
		if len(matching) <= maxKeep {
			continue
		}
		sort.Slice(matching, func(i, j int) bool {
			if !matching[i].UpdatedAt.Equal(matching[j].UpdatedAt) {
				return matching[i].UpdatedAt.After(matching[j].UpdatedAt)
			}
			return matching[i].CreatedAt.After(matching[j].CreatedAt)
		})
		for _, old := range matching[maxKeep:] {
			delete(m.records, old.ID)
			delete(m.sourceAccountID, old.ID)
			if m.activeRestores[old.ID] > 0 {
				m.pendingPruneDirs[old.ID] = struct{}{}
			} else if validateSafeChildName(old.ID) == nil {
				dirsToRemoveNow = append(dirsToRemoveNow, old.ID)
			}
		}
	}
	return dirsToRemoveNow
}

func (m *Manager) loadFromDisk() {
	root, err := os.OpenRoot(m.checkpointsDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			m.loadErr = fmt.Errorf("open checkpoints directory: %w", err)
		}
		return
	}
	defer root.Close()

	if rawPol, err := readRegularFileLimitedInRoot(root, "policy.json", maxMetadataJSONBytes); err == nil && len(rawPol) > 0 {
		var pol models.CheckpointPolicy
		if json.Unmarshal(rawPol, &pol) == nil {
			if pol.SyncIntervalSec > 0 {
				m.policy = pol
			}
		}
	}

	rawRecs, recErr := readRegularFileLimitedInRoot(root, "records.json", maxMetadataJSONBytes)
	if recErr != nil {
		if !errors.Is(recErr, os.ErrNotExist) {
			m.loadErr = fmt.Errorf("read checkpoint ledger records.json: %w", recErr)
		}
		return
	}
	trimmed := bytes.TrimSpace(rawRecs)
	if len(trimmed) == 0 {
		m.loadErr = errors.New("corrupt checkpoint ledger records.json: empty file")
		return
	}

	if trimmed[0] == '[' {
		var plainList []*models.CheckpointRecord
		if err := json.Unmarshal(trimmed, &plainList); err != nil {
			m.loadErr = fmt.Errorf("corrupt checkpoint ledger records.json: %w", err)
			return
		}
		for _, rec := range plainList {
			if rec == nil || strings.TrimSpace(rec.ID) == "" {
				m.loadErr = errors.New("corrupt checkpoint ledger records.json: contains null or empty record entry")
				return
			}
			m.records[rec.ID] = rec
		}
		return
	}

	if trimmed[0] != '{' {
		m.loadErr = errors.New("corrupt checkpoint ledger records.json: expected JSON object or array")
		return
	}
	var rawObj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &rawObj); err != nil {
		m.loadErr = fmt.Errorf("corrupt checkpoint ledger records.json: %w", err)
		return
	}
	rawRecsField, hasRecords := rawObj["records"]
	if !hasRecords || len(bytes.TrimSpace(rawRecsField)) == 0 || bytes.Equal(bytes.TrimSpace(rawRecsField), []byte("null")) {
		m.loadErr = errors.New("corrupt checkpoint ledger records.json: missing required \"records\" array")
		return
	}

	var state persistedCheckpointState
	if err := json.Unmarshal(trimmed, &state); err != nil {
		m.loadErr = fmt.Errorf("corrupt checkpoint ledger records.json: %w", err)
		return
	}
	for _, rec := range state.Records {
		if rec == nil || strings.TrimSpace(rec.ID) == "" {
			m.loadErr = errors.New("corrupt checkpoint ledger records.json: contains null or empty record entry")
			return
		}
		m.records[rec.ID] = rec
	}
	for k, v := range state.SourceAccountID {
		m.sourceAccountID[k] = v
	}
}

func (m *Manager) writeRootJSONAtomicLocked(relName string, v any) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(m.checkpointsDir, 0o700); err != nil {
		return err
	}
	root, err := os.OpenRoot(m.checkpointsDir)
	if err != nil {
		return err
	}
	defer root.Close()
	return writeBytesAtomicInRoot(root, relName, encoded)
}

func (m *Manager) persistRecordsLocked() error {
	list := make([]*models.CheckpointRecord, 0, len(m.records))
	for _, rec := range m.records {
		if rec != nil {
			list = append(list, cloneRecord(rec))
		}
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	srcMap := make(map[string]string, len(m.sourceAccountID))
	for k, v := range m.sourceAccountID {
		srcMap[k] = v
	}
	return m.writeRootJSONAtomicLocked("records.json", persistedCheckpointState{
		Records:         list,
		SourceAccountID: srcMap,
	})
}

func readRegularFileLimitedInRoot(root *os.Root, relPath string, maxBytes int64) ([]byte, error) {
	info, err := root.Lstat(relPath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("symlink %q is forbidden", relPath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("non-regular file %q is forbidden", relPath)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file %q exceeds maximum allowed size (%d bytes)", filepath.Base(relPath), maxBytes)
	}
	f, err := root.Open(relPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("non-regular file %q is forbidden", relPath)
	}
	if fi.Size() > maxBytes {
		return nil, fmt.Errorf("file %q exceeds maximum allowed size (%d bytes)", filepath.Base(relPath), maxBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("file %q exceeds maximum allowed size (%d bytes)", filepath.Base(relPath), maxBytes)
	}
	return raw, nil
}

func writeBytesAtomicInRoot(root *os.Root, relPath string, data []byte) error {
	cleanRel := filepath.Clean(relPath)
	if cleanRel == "." || cleanRel == ".." || !filepath.IsLocal(cleanRel) {
		return fmt.Errorf("invalid confined target path %q", relPath)
	}

	parent := filepath.Dir(cleanRel)
	if parent != "." && parent != "" {
		if err := mkdirAllNoSymlinkInRoot(root, parent); err != nil {
			return err
		}
	}

	if existing, err := root.Lstat(cleanRel); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return fmt.Errorf("refusing to overwrite non-regular file or symlink %q", cleanRel)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	randBuf := make([]byte, 8)
	_, _ = rand.Read(randBuf)
	tmpBase := ".tmp-" + hex.EncodeToString(randBuf)
	tmpRel := tmpBase
	if parent != "." && parent != "" {
		tmpRel = filepath.Join(parent, tmpBase)
	}

	f, err := root.OpenFile(tmpRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, wErr := f.Write(data); wErr != nil {
		_ = f.Close()
		_ = root.Remove(tmpRel)
		return wErr
	}
	if cErr := f.Close(); cErr != nil {
		_ = root.Remove(tmpRel)
		return cErr
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*10) * time.Millisecond)
		}
		if rErr := root.Rename(tmpRel, cleanRel); rErr == nil {
			return nil
		} else {
			lastErr = rErr
		}
	}
	_ = root.Remove(tmpRel)
	return lastErr
}

func mkdirAllNoSymlinkInRoot(root *os.Root, relDir string) error {
	clean := filepath.Clean(relDir)
	if clean == "." || clean == "" {
		return nil
	}
	if !filepath.IsLocal(clean) {
		return fmt.Errorf("invalid relative directory %q", relDir)
	}
	parts := strings.Split(filepath.ToSlash(clean), "/")
	cur := ""
	for _, seg := range parts {
		if err := validateSafeChildName(seg); err != nil {
			return err
		}
		if cur == "" {
			cur = seg
		} else {
			cur = filepath.Join(cur, seg)
		}
		info, err := root.Lstat(cur)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink directory %q is forbidden", cur)
			}
			if !info.IsDir() {
				return fmt.Errorf("path %q exists and is not a directory", cur)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if mkErr := root.Mkdir(cur, 0o700); mkErr != nil && !errors.Is(mkErr, os.ErrExist) {
			return mkErr
		}
	}
	return nil
}

// validateSafeChildName enforces a strict single child path segment (no /, \, .., null bytes, or Windows drive/ADS colons).
func validateSafeChildName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("invalid child name %q", name)
	}
	if strings.ContainsAny(name, "/\\\x00:") {
		return fmt.Errorf("invalid child name %q: path separators, colons, and null bytes are forbidden", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("invalid child name %q: '..' sequence forbidden", name)
	}
	if !filepath.IsLocal(name) {
		return fmt.Errorf("invalid child name %q: not a safe local child name", name)
	}
	return nil
}

func validateResumeCommand(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > 512 {
		return "", errors.New("invalid resume_command: exceeds maximum length (512 chars)")
	}
	if strings.ContainsAny(trimmed, "\r\n\x00;&|$`()<>\\") {
		return "", errors.New("invalid resume_command: shell chaining, pipes, and command substitutions are forbidden")
	}
	return trimmed, nil
}

func sanitizeRemotePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("invalid remote_path: empty path")
	}
	if strings.ContainsRune(trimmed, '\x00') {
		return "", errors.New("invalid remote_path: null byte forbidden")
	}
	slashed := strings.ReplaceAll(trimmed, "\\", "/")
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("invalid remote_path %q: path traversal forbidden", raw)
		}
	}
	cleaned := path.Clean("/" + strings.TrimLeft(slashed, "/"))
	if cleaned == "/" {
		return "", fmt.Errorf("invalid remote_path %q: root directory not allowed", raw)
	}
	return cleaned, nil
}

func sanitizeSafeDirSegment(raw string) string {
	trimmed := strings.TrimSpace(RedactEmailStrings(raw))
	if trimmed == "" {
		return "job_default"
	}
	var b strings.Builder
	for i := 0; i < len(trimmed) && b.Len() < 96; i++ {
		c := trimmed[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if isAlphaNum || c == '_' || c == '-' || c == '.' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return "job_default"
	}
	stem := out
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		out = "job_" + out
	}
	return out
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

func clonePolicy(p models.CheckpointPolicy) models.CheckpointPolicy {
	cp := p
	if len(p.WatchPaths) > 0 {
		cp.WatchPaths = append([]string(nil), p.WatchPaths...)
	} else {
		cp.WatchPaths = []string{}
	}
	return cp
}

func cloneRecord(r *models.CheckpointRecord) *models.CheckpointRecord {
	if r == nil {
		return nil
	}
	cp := *r
	cp.JobTitle = RedactEmailStrings(cp.JobTitle)
	cp.SourceAccountLabel = RedactEmailStrings(cp.SourceAccountLabel)
	cp.ResumedAccountLabel = RedactEmailStrings(cp.ResumedAccountLabel)
	cp.LastRelayNoteEn = RedactEmailStrings(cp.LastRelayNoteEn)
	cp.LastRelayNoteZh = RedactEmailStrings(cp.LastRelayNoteZh)
	return &cp
}
