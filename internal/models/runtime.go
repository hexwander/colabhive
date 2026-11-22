package models

import "time"

// SessionStatus represents the lifecycle state of a Colab RuntimeSession.
type SessionStatus string

const (
	SessionProvisioning SessionStatus = "PROVISIONING"
	SessionIdle         SessionStatus = "IDLE"
	SessionBusy         SessionStatus = "BUSY"
	SessionReconnecting SessionStatus = "RECONNECTING"
	SessionDraining     SessionStatus = "DRAINING"
	SessionStopped      SessionStatus = "STOPPED"
	SessionLost         SessionStatus = "LOST"
	SessionZombieAlert  SessionStatus = "ZOMBIE_ALERT"
	SessionError        SessionStatus = "ERROR"
)

// BackendAPIType identifies which Google Colab backend provisioned a runtime.
type BackendAPIType string

const (
	BackendTunnelAPI BackendAPIType = "tunnel" // colab.research.google.com/tun/m/...
	BackendPublicAPI BackendAPIType = "public" // colaboratory.googleapis.com/v1beta/runtimes/...
)

// VMGPUTelemetry captures per-GPU utilization and VRAM metrics from /api/colab/resources or nvidia-smi.
type VMGPUTelemetry struct {
	Name              string  `json:"name"`
	MemoryUsedBytes   int64   `json:"memory_used_bytes"`
	MemoryTotalBytes  int64   `json:"memory_total_bytes"`
	GPUUtilization    float64 `json:"gpu_utilization"`    // 0.0 .. 1.0
	MemoryUtilization float64 `json:"memory_utilization"` // 0.0 .. 1.0
	TemperatureC      int     `json:"temperature_c,omitempty"`
	PowerWatts        int     `json:"power_watts,omitempty"`
	EverUsed          bool    `json:"ever_used"`
}

// VMResourceTelemetry matches Colab's Tornado `/api/colab/resources` out-of-band monitor.
type VMResourceTelemetry struct {
	MemoryTotalBytes int64            `json:"memory_total_bytes"`
	MemoryFreeBytes  int64            `json:"memory_free_bytes"`
	MemoryUsedBytes  int64            `json:"memory_used_bytes"`
	DiskTotalBytes   int64            `json:"disk_total_bytes"`
	DiskUsedBytes    int64            `json:"disk_used_bytes"`
	HFCacheBytes     int64            `json:"hf_cache_bytes,omitempty"`
	GPUs             []VMGPUTelemetry `json:"gpus"`
	SampledAt        time.Time        `json:"sampled_at"`
}

// WorkerLiveness tracks the `/proc/{pid}/stat` anti-zombie probe for background jobs.
type WorkerLiveness struct {
	PID            int        `json:"pid,omitempty"`
	ProcState      string     `json:"proc_state,omitempty"` // "R", "S", "D", "Z"
	Alive          bool       `json:"alive"`
	ZombieDetected bool       `json:"zombie_detected"`
	HasFatalTrace  bool       `json:"has_fatal_trace"`
	ExitCode       *int       `json:"exit_code,omitempty"`
	LogOffset      int64      `json:"log_offset"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
}

// ExecutionStep represents one stage in the Workbench Execution Timeline & Waterfall.
type ExecutionStep struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Status      string     `json:"status"` // "pending" | "running" | "completed" | "failed"
	DurationMs  int64      `json:"duration_ms"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Detail      string     `json:"detail,omitempty"`
}

// KernelExecError matches Google's ExecutionError & Jupyter iopub `error` wire frame.
type KernelExecError struct {
	Name      string   `json:"name"`
	Value     string   `json:"value"`
	Traceback []string `json:"traceback"`
	ExitCode  *int     `json:"exit_code,omitempty"` // Populated when Name == "SystemExit"
}

// KernelRichOutput matches Google's `RichOutput` (`colab-api.json` rev 20260913)
// so large base64 plots are stored out-of-band unless InlineImages is requested.
type KernelRichOutput struct {
	Index        int      `json:"index"`
	MimeTypes    []string `json:"mimeTypes"`
	FilePath     string   `json:"filePath,omitempty"`
	ArtifactURL  string   `json:"artifactUrl,omitempty"`
	Title        string   `json:"title,omitempty"`
	SizeBytes    int64    `json:"sizeBytes"`
	InlineBase64 string   `json:"inlineBase64,omitempty"`
	TextData     string   `json:"textData,omitempty"`
}

// KernelExecResult matches Google's official `ExecuteCodeResult` contract.
type KernelExecResult struct {
	Session         string             `json:"session"`
	ExecutionID     string             `json:"executionId"`
	ExecutionCount  int                `json:"executionCount"`
	Status          string             `json:"status"` // "ok" | "error" | "abort"
	Stdout          string             `json:"stdout"`
	Stderr          string             `json:"stderr"`
	OutputTruncated bool               `json:"outputTruncated"`
	Result          string             `json:"result,omitempty"`
	ExecutionError  *KernelExecError   `json:"executionError,omitempty"`
	RichOutputs     []KernelRichOutput `json:"richOutputs,omitempty"`
	DurationMs      int64              `json:"durationMs"`
}

// KernelExecRequest describes a Python/IPython cell execution against a Colab runtime.
type KernelExecRequest struct {
	SessionID          string            `json:"session_id"`
	Code               string            `json:"code"`
	LocalFilePath      string            `json:"local_file_path,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	IdleTimeoutSec     float64           `json:"idle_timeout_sec"` // 0 = unlimited
	Silent             bool              `json:"silent,omitempty"`
	StoreHistory       bool              `json:"store_history"`
	AllowStdin         bool              `json:"allow_stdin,omitempty"`
	InlineImages       bool              `json:"inline_images,omitempty"`
	AutoProvisionGPU   AcceleratorType   `json:"auto_provision_gpu,omitempty"`
	StickyKey          string            `json:"sticky_key,omitempty"`
	CallerName         string            `json:"caller_name,omitempty"`
	APIKeyID           string            `json:"api_key_id,omitempty"`
	Strategy           RoutingStrategy   `json:"strategy,omitempty"`
	AccountSelector    string            `json:"account_selector,omitempty"`
	ExcludedAccountIDs []string          `json:"excluded_account_ids,omitempty"`
}

// NotebookCell backs ColabHive's headless virtual `.ipynb` notebook for `googlecolab/colab-mcp` tools.
type NotebookCell struct {
	CellID         string             `json:"cell_id"`
	Index          int                `json:"index"`
	CellType       string             `json:"cell_type"` // "code" | "markdown"
	Source         string             `json:"source"`
	ExecutionCount int                `json:"execution_count,omitempty"`
	Outputs        []KernelRichOutput `json:"outputs,omitempty"`
}

// CellMutationRequest encapsulates `add_code_cell`, `add_text_cell`, `update_cell`, `run_code_cell`, `delete_cell`, `move_cell`.
type CellMutationRequest struct {
	Operation  string  `json:"operation"` // "add_code" | "add_text" | "update" | "run" | "delete" | "move"
	CellID     string  `json:"cell_id,omitempty"`
	Content    string  `json:"content,omitempty"`
	Index      *int    `json:"index,omitempty"`
	RunOnAdd   bool    `json:"run_on_add,omitempty"`
	TimeoutSec float64 `json:"timeout_sec,omitempty"`
}

// RuntimeSession represents an allocated Google Colab VM and its persistent Jupyter kernel.
type RuntimeSession struct {
	ID                   string               `json:"id"`
	Name                 string               `json:"name"`
	AccountID            string               `json:"account_id"`
	AccountEmail         string               `json:"account_email"`
	Backend              BackendAPIType       `json:"backend"`
	RuntimeName          string               `json:"runtime_name,omitempty"` // runtimes/{id} on Public API
	Endpoint             string               `json:"endpoint"`               // gpu-g4-s-... on Tunnel API
	NotebookHash         string               `json:"notebook_hash"`          // 44-char nbh
	ProxyURL             string               `json:"proxy_url"`
	ProxyToken           string               `json:"-"` // Never expose capability token in UI responses
	ProxyTokenExpiresAt  *time.Time           `json:"proxy_token_expires_at,omitempty"`
	Variant              RuntimeVariant       `json:"variant"`
	RequestedAccelerator AcceleratorType      `json:"requested_accelerator"`
	Accelerator          AcceleratorType      `json:"accelerator"`
	FallbackApplied      bool                 `json:"fallback_applied"`
	Shape                MachineShape         `json:"shape"`
	Status               SessionStatus        `json:"status"`
	StatusMessage        string               `json:"status_message,omitempty"`
	KernelID             string               `json:"kernel_id,omitempty"`
	JupyterSessionID     string               `json:"jupyter_session_id,omitempty"`
	BurnRateCUHr         float64              `json:"burn_rate_cu_hr"`
	AccumulatedCU        float64              `json:"accumulated_cu"`
	ColabWebURL          string               `json:"colab_web_url"`
	ExperimentID         string               `json:"experiment_id,omitempty"`
	ShardIndex           *int                 `json:"shard_index,omitempty"`
	StickyKey            string               `json:"sticky_key,omitempty"`
	APIKeyID             string               `json:"api_key_id,omitempty"`
	KeepaliveHealthy     bool                 `json:"keepalive_healthy"`
	LastKeepaliveAt      *time.Time           `json:"last_keepalive_at,omitempty"`
	ZMQReconnectCount    int                  `json:"zmq_reconnect_count"`
	Resources            *VMResourceTelemetry `json:"resources,omitempty"`
	Worker               *WorkerLiveness      `json:"worker,omitempty"`
	Timeline             []ExecutionStep      `json:"timeline,omitempty"`
	Cells                []NotebookCell       `json:"cells,omitempty"`
	AdoptedExternal      bool                 `json:"adopted_external"`
	NotebookName         string               `json:"notebook_name,omitempty"`
	RecentLogs           []string             `json:"recent_logs,omitempty"`
	LastExecutionFile    string               `json:"last_execution_file,omitempty"`
	LastExecutionAt      *time.Time           `json:"last_execution_at,omitempty"`
	UptimeSec            int64                `json:"uptime_sec"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
	StoppedAt            *time.Time           `json:"stopped_at,omitempty"`
}

// SessionAllocRequest specifies parameters for allocating a new Colab runtime via the pool router.
type SessionAllocRequest struct {
	Name                 string            `json:"name,omitempty"`
	Accelerator          AcceleratorType   `json:"accelerator"`
	HighMem              bool              `json:"high_mem"`
	RequireLargeVRAM     bool              `json:"require_large_vram,omitempty"`
	Strategy             RoutingStrategy   `json:"strategy,omitempty"`
	AccountSelector      string            `json:"account_selector,omitempty"` // ID, email, or tag:<name>
	AllowedGroups        []string          `json:"allowed_groups,omitempty"`
	ExcludedAccountIDs   []string          `json:"excluded_account_ids,omitempty"`
	ExcludedAccountTiers []string          `json:"excluded_account_tiers,omitempty"`
	AutoFallback         bool              `json:"auto_fallback"`
	FallbackChain        []AcceleratorType `json:"fallback_chain,omitempty"`
	StickyKey            string            `json:"sticky_key,omitempty"`
	APIKeyID             string            `json:"api_key_id,omitempty"`
	CallerName           string            `json:"caller_name,omitempty"`
	ExperimentID         string            `json:"experiment_id,omitempty"`
	ShardIndex           *int              `json:"shard_index,omitempty"`
}

// SessionFilter filters active/historical RuntimeSessions.
type SessionFilter struct {
	AccountID    string `json:"account_id,omitempty"`
	ExperimentID string `json:"experiment_id,omitempty"`
	ActiveOnly   bool   `json:"active_only,omitempty"`
	SyncBackend  bool   `json:"sync_backend,omitempty"`
}

// SessionSyncResult is returned by POST /api/v1/sessions/sync and SyncRemoteAssignments.
type SessionSyncResult struct {
	Sessions     []*RuntimeSession `json:"sessions"`
	AdoptedCount int               `json:"adopted_count"`
	ActiveCount  int               `json:"active_count"`
}

// RemoteFileEntry describes a file or directory under `/content` on a Colab VM.
type RemoteFileEntry struct {
	Name         string     `json:"name"`
	Path         string     `json:"path"`
	Type         string     `json:"type"` // "file" | "directory"
	SizeBytes    int64      `json:"size_bytes"`
	LastModified *time.Time `json:"last_modified,omitempty"`
	Synced       bool       `json:"synced,omitempty"`
}

// ExperimentStatus represents the aggregate state of a multi-shard parallel experiment.
type ExperimentStatus string

const (
	ExperimentPending        ExperimentStatus = "PENDING"
	ExperimentProvisioning   ExperimentStatus = "PROVISIONING"
	ExperimentRunning        ExperimentStatus = "RUNNING"
	ExperimentCompleted      ExperimentStatus = "COMPLETED"
	ExperimentPartialFailure ExperimentStatus = "PARTIAL_FAILURE"
	ExperimentFailed         ExperimentStatus = "FAILED"
	ExperimentCancelled      ExperimentStatus = "CANCELLED"
)

// ExperimentFileSpec maps a local file to upload to each shard VM before launch.
type ExperimentFileSpec struct {
	LocalPath     string `json:"local_path"`
	RemotePath    string `json:"remote_path"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

// ExperimentShard represents one parallel GPU worker inside a sharded Experiment.
type ExperimentShard struct {
	ShardID          int             `json:"shard_id"`
	SessionID        string          `json:"session_id"`
	SessionName      string          `json:"session_name"`
	AccountID        string          `json:"account_id"`
	AccountEmail     string          `json:"account_email"`
	Accelerator      AcceleratorType `json:"accelerator"`
	Status           string          `json:"status"` // "PROVISIONING" | "RUNNING" | "COMPLETED" | "COMPLETED_AND_UNASSIGNED" | "FAILED" | "ZOMBIE_ABORTED"
	PID              int             `json:"pid,omitempty"`
	ProcState        string          `json:"proc_state,omitempty"`
	ZombieDetected   bool            `json:"zombie_detected"`
	ExitCode         *int            `json:"exit_code,omitempty"`
	ProgressPct      float64         `json:"progress_pct"`
	SyncedRecords        int             `json:"synced_records"`
	TargetRecords        int             `json:"target_records,omitempty"`
	CheckpointExpected   bool            `json:"checkpoint_expected,omitempty"`
	FinalCheckpointSaved bool            `json:"final_checkpoint_saved,omitempty"`
	VRAMUsedGB       float64         `json:"vram_used_gb,omitempty"`
	VRAMTotalGB      float64         `json:"vram_total_gb,omitempty"`
	SMUtilPct        float64         `json:"sm_util_pct,omitempty"`
	TempC            int             `json:"temp_c,omitempty"`
	CUBurned         float64         `json:"cu_burned"`
	StartupSelfCheck string          `json:"startup_self_check,omitempty"` // "PENDING" | "PASSED" | "FAILED"
	LogTail          string          `json:"log_tail,omitempty"`
	StartedAt        *time.Time      `json:"started_at,omitempty"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
}

// ExperimentLaunchRequest launches `NumShards` parallel GPU VMs across pooled accounts.
type ExperimentLaunchRequest struct {
	ExperimentID         string               `json:"experiment_id"`
	Title                string               `json:"title,omitempty"`
	ScriptPath           string               `json:"script_path"`
	ScriptCode           string               `json:"script_code,omitempty"`
	ScriptArgs           []string             `json:"script_args,omitempty"`
	UploadFiles          []ExperimentFileSpec `json:"upload_files,omitempty"`
	Packages             []string             `json:"packages,omitempty"`
	Accelerator          AcceleratorType      `json:"accelerator"`
	HighMem              bool                 `json:"high_mem"`
	NumShards            int                  `json:"num_shards"`
	Strategy             RoutingStrategy      `json:"strategy"`
	RemoteCheckpointGlob string               `json:"remote_checkpoint_glob,omitempty"`
	LocalOutputDir       string               `json:"local_output_dir,omitempty"`
	AutoStopOnComplete   bool                 `json:"auto_stop_on_complete"`
	Env                  map[string]string    `json:"env,omitempty"`
	APIKeyID             string               `json:"api_key_id,omitempty"`
}

// Experiment aggregates multi-account sharded runs for the Live Experiment Workbench (`/exp`).
type Experiment struct {
	ID                    string             `json:"id"`
	Title                 string             `json:"title"`
	APIKeyID              string             `json:"api_key_id,omitempty"`
	Status                ExperimentStatus   `json:"status"`
	ScriptPath            string             `json:"script_path"`
	Accelerator           AcceleratorType    `json:"accelerator"`
	HighMem               bool               `json:"high_mem"`
	NumShards             int                `json:"num_shards"`
	Strategy              RoutingStrategy    `json:"strategy"`
	CompletedShards       int                `json:"completed_shards"`
	RunningShards         int                `json:"running_shards"`
	FailedShards          int                `json:"failed_shards"`
	SyncedRecordsTotal    int                `json:"synced_records_total"`
	TargetRecordsTotal    int                `json:"target_records_total,omitempty"`
	AggregateBurnRateCUHr float64            `json:"aggregate_burn_rate_cu_hr"`
	CUBurnedSoFar         float64            `json:"cu_burned_so_far"`
	EstimatedTotalCU      float64            `json:"estimated_total_cu,omitempty"`
	AutoStopOnComplete    bool               `json:"auto_stop_on_complete"`
	LocalOutputDir        string             `json:"local_output_dir,omitempty"`
	MergedJSONLPath       string             `json:"merged_jsonl_path,omitempty"`
	Artifacts             []KernelRichOutput `json:"artifacts,omitempty"`
	Shards                []ExperimentShard  `json:"shards"`
	CreatedAt             time.Time          `json:"created_at"`
	UpdatedAt             time.Time          `json:"updated_at"`
	CompletedAt           *time.Time         `json:"completed_at,omitempty"`
}

// ExperimentTeardownResult summarizes final JSONL merge and zero-leak VM unassignment.
type ExperimentTeardownResult struct {
	ExperimentID       string   `json:"experiment_id"`
	AllVMsUnassigned   bool     `json:"all_vms_unassigned"`
	ActiveVMsRemaining int      `json:"active_vms_remaining"`
	MergedJSONLPath    string   `json:"merged_jsonl_path,omitempty"`
	MergedRecordsCount int      `json:"merged_records_count"`
	ExportedNotebooks  []string `json:"exported_notebooks,omitempty"`
	TotalCUConsumed    float64  `json:"total_cu_consumed"`
}
