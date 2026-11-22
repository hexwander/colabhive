/**
 * ColabHive Canonical Frontend Type Definitions (`web/src/types/index.ts`)
 * Mirrors `internal/models/*.go` JSON wire tags 1:1.
 */

export type AcceleratorType =
  | 'G4'
  | 'H100'
  | 'A100'
  | 'L4'
  | 'T4'
  | 'V6E1'
  | 'V5E1'
  | 'CPU';

export const ALL_ACCELERATORS: AcceleratorType[] = [
  'G4',
  'H100',
  'A100',
  'L4',
  'T4',
  'V6E1',
  'V5E1',
  'CPU',
];

export type RuntimeVariant = 'DEFAULT' | 'GPU' | 'TPU';
export type MachineShape = 'STANDARD' | 'HIGH_RAM';
export type RateCalibrationSource = 'live' | 'empirical' | 'reference';

export interface HardwareSpec {
  accelerator: AcceleratorType;
  wire_accelerator: string;
  display_name: string;
  short_label: string;
  architecture: string;
  variant: RuntimeVariant;
  vram_gb: number;
  default_burn_rate_cu_hr: number;
  min_burn_rate_cu_hr: number;
  max_burn_rate_cu_hr: number;
  free_tier_eligible: boolean;
  supports_high_mem_flag: boolean;
  always_high_mem: boolean;
  optimal_workload: string;
  cli_flag: string;
  fallback_order: number;
}

export const DEFAULT_HARDWARE_CATALOG: HardwareSpec[] = [
  {
    accelerator: 'G4',
    wire_accelerator: 'G4',
    display_name: 'NVIDIA RTX PRO 6000 Blackwell',
    short_label: 'G4 96GB',
    architecture: 'Blackwell · SM 12.0 · GDDR7',
    variant: 'GPU',
    vram_gb: 96,
    default_burn_rate_cu_hr: 9.66,
    min_burn_rate_cu_hr: 6.7,
    max_burn_rate_cu_hr: 10.0,
    free_tier_eligible: false,
    supports_high_mem_flag: true,
    always_high_mem: false,
    optimal_workload: '>=26B BF16 Models (Qwen-27B / Gemma-26B)',
    cli_flag: '--gpu G4',
    fallback_order: 2,
  },
  {
    accelerator: 'H100',
    wire_accelerator: 'H100',
    display_name: 'NVIDIA H100 SXM5',
    short_label: 'H100 80GB',
    architecture: 'Hopper · SM 9.0 · HBM3',
    variant: 'GPU',
    vram_gb: 80,
    default_burn_rate_cu_hr: 12.5,
    min_burn_rate_cu_hr: 12.5,
    max_burn_rate_cu_hr: 18.0,
    free_tier_eligible: false,
    supports_high_mem_flag: true,
    always_high_mem: false,
    optimal_workload: 'FP8 / FlashAttention-3 / Dense Training',
    cli_flag: '--gpu H100',
    fallback_order: 1,
  },
  {
    accelerator: 'A100',
    wire_accelerator: 'A100',
    display_name: 'NVIDIA A100 SXM4',
    short_label: 'A100 40/80GB',
    architecture: 'Ampere · SM 8.0 · HBM2e',
    variant: 'GPU',
    vram_gb: 40,
    default_burn_rate_cu_hr: 8.88,
    min_burn_rate_cu_hr: 7.0,
    max_burn_rate_cu_hr: 10.6,
    free_tier_eligible: false,
    supports_high_mem_flag: true,
    always_high_mem: false,
    optimal_workload: '<=14B BF16 · Native Triton / CUDA Wheels',
    cli_flag: '--gpu A100 --high-mem',
    fallback_order: 3,
  },
  {
    accelerator: 'L4',
    wire_accelerator: 'L4',
    display_name: 'NVIDIA L4',
    short_label: 'L4 24GB',
    architecture: 'Ada Lovelace · SM 8.9 · GDDR6',
    variant: 'GPU',
    vram_gb: 24,
    default_burn_rate_cu_hr: 4.52,
    min_burn_rate_cu_hr: 1.8,
    max_burn_rate_cu_hr: 4.6,
    free_tier_eligible: false,
    supports_high_mem_flag: false,
    always_high_mem: true,
    optimal_workload: '7B-9B Inference / LoRA / Diffusion',
    cli_flag: '--gpu L4',
    fallback_order: 4,
  },
  {
    accelerator: 'T4',
    wire_accelerator: 'T4',
    display_name: 'NVIDIA Tesla T4',
    short_label: 'T4 16GB',
    architecture: 'Turing · SM 7.5 · GDDR6',
    variant: 'GPU',
    vram_gb: 16,
    default_burn_rate_cu_hr: 1.96,
    min_burn_rate_cu_hr: 0.0,
    max_burn_rate_cu_hr: 2.0,
    free_tier_eligible: true,
    supports_high_mem_flag: true,
    always_high_mem: false,
    optimal_workload: '<=4B Eval / Unit Tests / Free Dynamic Window',
    cli_flag: '--gpu T4',
    fallback_order: 5,
  },
  {
    accelerator: 'V6E1',
    wire_accelerator: 'V6E1',
    display_name: 'Cloud TPU v6e-1 Trillium',
    short_label: 'TPU v6e-1 32GB',
    architecture: 'Trillium · 1 Chip · HBM',
    variant: 'TPU',
    vram_gb: 32,
    default_burn_rate_cu_hr: 5.3,
    min_burn_rate_cu_hr: 4.0,
    max_burn_rate_cu_hr: 5.5,
    free_tier_eligible: false,
    supports_high_mem_flag: false,
    always_high_mem: true,
    optimal_workload: 'JAX / XLA / PyTorch-XLA High Perf/CU',
    cli_flag: '--tpu v6e1',
    fallback_order: 6,
  },
  {
    accelerator: 'V5E1',
    wire_accelerator: 'V5E1',
    display_name: 'Cloud TPU v5e-1',
    short_label: 'TPU v5e-1 16GB',
    architecture: 'TPU v5e · 1 Chip · HBM',
    variant: 'TPU',
    vram_gb: 16,
    default_burn_rate_cu_hr: 3.85,
    min_burn_rate_cu_hr: 2.5,
    max_burn_rate_cu_hr: 4.0,
    free_tier_eligible: false,
    supports_high_mem_flag: false,
    always_high_mem: true,
    optimal_workload: 'Cost-Efficient JAX / Embedding Extraction',
    cli_flag: '--tpu v5e1',
    fallback_order: 7,
  },
  {
    accelerator: 'CPU',
    wire_accelerator: 'NONE',
    display_name: 'Cloud x86_64 CPU VM',
    short_label: 'CPU VM',
    architecture: 'x86_64 Cloud Host',
    variant: 'DEFAULT',
    vram_gb: 0,
    default_burn_rate_cu_hr: 0.74,
    min_burn_rate_cu_hr: 0.0,
    max_burn_rate_cu_hr: 1.1,
    free_tier_eligible: true,
    supports_high_mem_flag: true,
    always_high_mem: false,
    optimal_workload: 'Data Preprocessing / Aggregation (+ Free Tier)',
    cli_flag: '',
    fallback_order: 8,
  },
];

// ============================================================================
// Accounts, Proxies, Guardrails & Quota Observatory
// ============================================================================

export type SubscriptionTier = 'FREE' | 'PAYG' | 'PRO' | 'PRO_PLUS' | 'AI_ULTRA';

export type AccountHealth =
  | 'HEALTHY'
  | 'ACTIVE_WORKING'
  | 'MAX_CONCURRENCY'
  | 'COOLDOWN_RATE_LIMIT'
  | 'QUOTA_LOW_FREE_ONLY'
  | 'PAUSED_CAP_REACHED'
  | 'PAUSED_SCHEDULE_WINDOW'
  | 'PAUSED_MANUAL'
  | 'AUTH_EXPIRED'
  | 'REVOKED_BY_USER'
  | 'QUARANTINED_DENYLIST'
  | 'PROXY_ERROR';

export type GPUEntitlementStatus =
  | 'AVAILABLE'
  | 'TIER_RESTRICTED'
  | 'INELIGIBLE'
  | 'COOLDOWN'
  | 'UNKNOWN';

export interface GPUEntitlement {
  accelerator: AcceleratorType;
  status: GPUEntitlementStatus;
  reason?: string;
  cooldown_until?: string;
  calibrated_burn_rate?: number;
  calibration_source?: RateCalibrationSource;
  verified_at?: string;
}

export type ProxyProtocol = 'direct' | 'http' | 'https' | 'socks5';

export interface ProxyConfig {
  id: string;
  name: string;
  protocol: ProxyProtocol;
  url?: string;
  masked_url: string;
  region?: string;
  exit_ip?: string;
  latency_ms: number;
  healthy: boolean;
  bound_accounts: number;
  last_checked_at?: string;
  last_error?: string;
  created_at: string;
}

export interface ProxyProbeResult {
  healthy: boolean;
  exit_ip?: string;
  region?: string;
  latency_ms: number;
  status_code?: number;
  error?: string;
  checked_at: string;
}

export interface ScheduleWindow {
  enabled: boolean;
  timezone: string;
  days_of_week: number[];
  start_hour: string;
  end_hour: string;
}

export interface ContributorGuardrails {
  min_reserve_cu: number;
  max_donated_cu_total?: number;
  max_donated_cu_daily?: number;
  donated_cu_used_total: number;
  donated_cu_used_today: number;
  allow_paid_cu_burn: boolean;
  allowed_gpus: AcceleratorType[];
  max_concurrent_vms: number;
  schedule_window?: ScheduleWindow;
  block_drive_propagation: boolean;
  mask_email_in_dashboard: boolean;
  paused: boolean;
  expires_at?: string;
  receipt_token?: string;
}

export interface FreeQuotaInfo {
  remaining_milli_ccu: number;
  remaining_free_cu: number;
  next_refill_at?: string;
}

export interface GPUHourEquivalence {
  accelerator: AcceleratorType;
  display_name: string;
  short_label: string;
  vram_gb: number;
  burn_rate_cu_hr: number;
  calibration_source: RateCalibrationSource;
  available_hours: number;
  schedulable_hours: number;
  eligible_accounts: number;
  max_parallel_slots: number;
  free_windows_ready: number;
  locked: boolean;
}

export interface Account {
  id: string;
  email: string;
  display_name?: string;
  avatar_url?: string;
  alias?: string;
  notes?: string;
  subscription_tier: SubscriptionTier;
  health: AccountHealth;
  health_reason?: string;
  cooldown_until?: string;
  enabled: boolean;
  priority: number;
  groups: string[];
  tags: string[];
  proxy_id?: string;
  proxy?: ProxyConfig;
  max_concurrent_vms: number;
  active_assignments: number;
  sticky_agent_count: number;
  paid_cu_balance: number;
  reference_cycle_cu: number;
  custom_cycle_cu?: boolean;
  schedulable_cu: number;
  live_burn_rate_cu_hr: number;
  cycle_resets_at?: string;
  earliest_batch_expires_at?: string;
  expiring_soon_cu: number;
  free_quota?: FreeQuotaInfo;
  gpu_entitlements: Partial<Record<AcceleratorType, GPUEntitlement>>;
  gpu_equivalence?: Partial<Record<AcceleratorType, GPUHourEquivalence>>;
  public_api_supported: boolean;
  tunnel_api_supported: boolean;
  is_contributor: boolean;
  invite_code?: string;
  guardrails: ContributorGuardrails;
  scopes?: string[];
  active_session_summary?: string;
  token_expires_at?: string;
  last_polled_at?: string;
  created_at: string;
  updated_at: string;
}

export type OAuthAuthMode = 'remote_copy_paste' | 'web_callback' | 'cli_donate';

export interface OAuthStartRequest {
  alias?: string;
  notes?: string;
  groups?: string[];
  tags?: string[];
  proxy_id?: string;
  proxy_url?: string;
  mode?: OAuthAuthMode;
  is_contributor?: boolean;
  invite_code?: string;
  guardrails?: ContributorGuardrails;
}

export interface OAuthPendingSession {
  state: string;
  code_challenge: string;
  auth_url: string;
  redirect_uri: string;
  mode: OAuthAuthMode;
  scopes: string[];
  alias?: string;
  notes?: string;
  groups?: string[];
  tags?: string[];
  proxy_id?: string;
  proxy_url?: string;
  is_contributor: boolean;
  invite_code?: string;
  guardrails?: ContributorGuardrails;
  expires_at: string;
  created_at: string;
}

export interface ContributorInvite {
  code: string;
  pool_title: string;
  maintainer_name: string;
  welcome_note?: string;
  target_groups: string[];
  default_proxy_id?: string;
  default_guardrails: ContributorGuardrails;
  max_uses: number;
  used_count: number;
  supported_modes: OAuthAuthMode[];
  required_scopes: string[];
  share_url?: string;
  revoked: boolean;
  expires_at?: string;
  created_at: string;
}

export interface CreateInviteRequest {
  pool_title: string;
  maintainer_name: string;
  welcome_note?: string;
  target_groups?: string[];
  default_proxy_id?: string;
  default_guardrails: ContributorGuardrails;
  max_uses?: number;
  ttl_hours?: number;
}

export interface ContributorActiveVM {
  session_id: string;
  accelerator: AcceleratorType;
  burn_rate_cu_hr: number;
  uptime_sec: number;
  started_at: string;
}

export interface ContributorReceiptStatus {
  account_id: string;
  masked_email: string;
  subscription_tier: SubscriptionTier;
  health: AccountHealth;
  paid_cu_balance: number;
  schedulable_cu: number;
  live_burn_rate_cu_hr: number;
  guardrails: ContributorGuardrails;
  active_vms: ContributorActiveVM[];
  last_polled_at?: string;
}

export interface ContributionReceipt {
  receipt_token: string;
  manage_url: string;
  status: ContributorReceiptStatus;
}

export interface ContributorGuardrailsPatch {
  min_reserve_cu?: number;
  max_donated_cu_total?: number;
  max_donated_cu_daily?: number;
  allow_paid_cu_burn?: boolean;
  allowed_gpus?: AcceleratorType[];
  max_concurrent_vms?: number;
  schedule_window?: ScheduleWindow;
  mask_email_in_dashboard?: boolean;
  paused?: boolean;
}

export interface QuotaSummary {
  accounts_total: number;
  accounts_healthy: number;
  accounts_working: number;
  accounts_contributor: number;
  total_available_cu: number;
  schedulable_cu: number;
  estimated_value_usd: number;
  active_burn_rate_cu_hr: number;
  pool_depletion_eta_hours?: number;
  earliest_batch_expiry_cu: number;
  earliest_batch_expires_at?: string;
  active_vms: number;
  total_vm_slots: number;
  total_online_vram_gb: number;
  free_t4_windows_ready: number;
  saved_idle_waste_cu: number;
  auto_teardowns_count: number;
  zombie_aborts_count: number;
  gpu_equivalence: Partial<Record<AcceleratorType, GPUHourEquivalence>>;
  updated_at: string;
}

export interface DiscoveredLocalCredential {
  id: string;
  source_type: 'keyring_plaintext' | 'colab_token_json' | 'gcloud_adc';
  source_path: string;
  environment: 'host' | 'wsl2';
  email_preview?: string;
  token_preview: string;
  scopes?: string[];
}

export type ImportConflictStrategy = 'skip' | 'update' | 'merge_groups';

export interface CredentialImportRequest {
  format: 'discovered' | 'keyring_ini' | 'adc_json' | 'raw_json' | 'chvault';
  discovered_ids?: string[];
  raw_payload?: string;
  passphrase?: string;
  default_groups?: string[];
  default_proxy_id?: string;
  conflict_strategy?: ImportConflictStrategy;
}

export interface CredentialExportRequest {
  account_ids?: string[];
  include_proxies: boolean;
  encrypt: boolean;
  passphrase?: string;
}

export interface CredentialBundle {
  schema: string;
  version: string;
  exported_at: string;
  encrypted: boolean;
  accounts?: unknown[];
  proxies?: ProxyConfig[];
  kdf?: { alg: string; iterations: number; salt_b64: string };
  cipher?: { alg: string; nonce_b64: string; ciphertext_b64: string };
}

export interface AccountPatch {
  alias?: string;
  notes?: string;
  enabled?: boolean;
  priority?: number;
  groups?: string[];
  tags?: string[];
  proxy_id?: string;
  max_concurrent_vms?: number;
  subscription_tier?: SubscriptionTier;
  reference_cycle_cu?: number;
  guardrails?: ContributorGuardrails;
}

// ============================================================================
// Runtime Sessions, Jupyter Execution & Sharded Experiments
// ============================================================================

export type SessionStatus =
  | 'PROVISIONING'
  | 'IDLE'
  | 'BUSY'
  | 'RECONNECTING'
  | 'DRAINING'
  | 'STOPPED'
  | 'LOST'
  | 'ZOMBIE_ALERT'
  | 'ERROR';

export type BackendAPIType = 'tunnel' | 'public';

export interface VMGPUTelemetry {
  name: string;
  memory_used_bytes: number;
  memory_total_bytes: number;
  gpu_utilization: number;
  memory_utilization: number;
  temperature_c?: number;
  power_watts?: number;
  ever_used: boolean;
}

export interface VMResourceTelemetry {
  memory_total_bytes: number;
  memory_free_bytes: number;
  memory_used_bytes: number;
  disk_total_bytes: number;
  disk_used_bytes: number;
  hf_cache_bytes?: number;
  gpus: VMGPUTelemetry[];
  sampled_at: string;
}

export interface WorkerLiveness {
  pid?: number;
  proc_state?: string;
  alive: boolean;
  zombie_detected: boolean;
  has_fatal_trace: boolean;
  log_offset: number;
  last_checked_at?: string;
}

export interface ExecutionStep {
  id: string;
  label: string;
  status: 'pending' | 'running' | 'completed' | 'failed';
  duration_ms: number;
  started_at: string;
  completed_at?: string;
  detail?: string;
}

export interface KernelExecError {
  name: string;
  value: string;
  traceback: string[];
  exit_code?: number;
}

export interface KernelRichOutput {
  index: number;
  mimeTypes: string[];
  filePath?: string;
  artifactUrl?: string;
  title?: string;
  sizeBytes: number;
  inlineBase64?: string;
  textData?: string;
}

export interface KernelExecResult {
  session: string;
  executionId: string;
  executionCount: number;
  status: 'ok' | 'error' | 'abort';
  stdout: string;
  stderr: string;
  outputTruncated: boolean;
  result?: string;
  executionError?: KernelExecError | null;
  richOutputs?: KernelRichOutput[];
  durationMs: number;
}

export interface KernelExecRequest {
  session_id: string;
  code: string;
  local_file_path?: string;
  env?: Record<string, string>;
  idle_timeout_sec: number;
  silent?: boolean;
  store_history: boolean;
  allow_stdin?: boolean;
  inline_images?: boolean;
  auto_provision_gpu?: AcceleratorType;
  sticky_key?: string;
  caller_name?: string;
}

export interface NotebookCell {
  cell_id: string;
  index: number;
  cell_type: 'code' | 'markdown';
  source: string;
  execution_count?: number;
  outputs?: KernelRichOutput[];
}

export interface CellMutationRequest {
  operation: 'add_code' | 'add_text' | 'update' | 'run' | 'delete' | 'move';
  cell_id?: string;
  content?: string;
  index?: number;
  run_on_add?: boolean;
  timeout_sec?: number;
}

export interface RuntimeSession {
  id: string;
  name: string;
  account_id: string;
  account_email: string;
  backend: BackendAPIType;
  runtime_name?: string;
  endpoint: string;
  notebook_hash: string;
  proxy_url: string;
  proxy_token_expires_at?: string;
  variant: RuntimeVariant;
  requested_accelerator: AcceleratorType;
  accelerator: AcceleratorType;
  fallback_applied: boolean;
  shape: MachineShape;
  status: SessionStatus;
  status_message?: string;
  kernel_id?: string;
  jupyter_session_id?: string;
  burn_rate_cu_hr: number;
  accumulated_cu: number;
  colab_web_url: string;
  experiment_id?: string;
  shard_index?: number;
  sticky_key?: string;
  api_key_id?: string;
  keepalive_healthy: boolean;
  last_keepalive_at?: string;
  zmq_reconnect_count: number;
  resources?: VMResourceTelemetry;
  worker?: WorkerLiveness;
  timeline?: ExecutionStep[];
  cells?: NotebookCell[];
  adopted_external?: boolean;
  notebook_name?: string;
  recent_logs?: string[];
  last_execution_file?: string;
  last_execution_at?: string;
  uptime_sec: number;
  created_at: string;
  updated_at: string;
  stopped_at?: string;
}

export interface SessionAllocRequest {
  name?: string;
  accelerator: AcceleratorType;
  high_mem: boolean;
  strategy?: RoutingStrategy;
  account_selector?: string;
  allowed_groups?: string[];
  auto_fallback: boolean;
  fallback_chain?: AcceleratorType[];
  sticky_key?: string;
  api_key_id?: string;
  caller_name?: string;
  experiment_id?: string;
  shard_index?: number;
}

export interface RemoteFileEntry {
  name: string;
  path: string;
  type: 'file' | 'directory';
  size_bytes: number;
  last_modified?: string;
  synced?: boolean;
}

export type ExperimentStatus =
  | 'PENDING'
  | 'PROVISIONING'
  | 'RUNNING'
  | 'COMPLETED'
  | 'PARTIAL_FAILURE'
  | 'FAILED'
  | 'CANCELLED';

export interface ExperimentFileSpec {
  local_path: string;
  remote_path: string;
}

export interface ExperimentShard {
  shard_id: number;
  session_id: string;
  session_name: string;
  account_id: string;
  account_email: string;
  accelerator: AcceleratorType;
  status: string;
  pid?: number;
  proc_state?: string;
  zombie_detected: boolean;
  exit_code?: number;
  progress_pct: number;
  synced_records: number;
  target_records?: number;
  vram_used_gb?: number;
  vram_total_gb?: number;
  sm_util_pct?: number;
  temp_c?: number;
  cu_burned: number;
  startup_self_check?: string;
  log_tail?: string;
  started_at?: string;
  completed_at?: string;
}

export interface ExperimentLaunchRequest {
  experiment_id: string;
  title?: string;
  script_path: string;
  script_code?: string;
  script_args?: string[];
  upload_files?: ExperimentFileSpec[];
  packages?: string[];
  accelerator: AcceleratorType;
  high_mem: boolean;
  num_shards: number;
  strategy: RoutingStrategy;
  remote_checkpoint_glob?: string;
  local_output_dir?: string;
  auto_stop_on_complete: boolean;
  env?: Record<string, string>;
}

export interface Experiment {
  id: string;
  title: string;
  status: ExperimentStatus;
  script_path: string;
  accelerator: AcceleratorType;
  high_mem: boolean;
  num_shards: number;
  strategy: RoutingStrategy;
  completed_shards: number;
  running_shards: number;
  failed_shards: number;
  synced_records_total: number;
  target_records_total?: number;
  aggregate_burn_rate_cu_hr: number;
  cu_burned_so_far: number;
  estimated_total_cu?: number;
  auto_stop_on_complete: boolean;
  local_output_dir?: string;
  merged_jsonl_path?: string;
  artifacts?: KernelRichOutput[];
  shards: ExperimentShard[];
  created_at: string;
  updated_at: string;
  completed_at?: string;
}

export interface ExperimentTeardownResult {
  experiment_id: string;
  all_vms_unassigned: boolean;
  active_vms_remaining: number;
  merged_jsonl_path?: string;
  merged_records_count: number;
  exported_notebooks?: string[];
  total_cu_consumed: number;
}

// ============================================================================
// Agent Router, API Keys, `cc-switch` Config Sync & WebSocket Events
// ============================================================================

export type RoutingStrategy =
  | 'waste-zero'
  | 'max-balance'
  | 'sticky-session'
  | 'round-robin'
  | 'free-opportunistic';

export const ALL_ROUTING_STRATEGIES: RoutingStrategy[] = [
  'waste-zero',
  'max-balance',
  'sticky-session',
  'round-robin',
  'free-opportunistic',
];

export interface RouterPolicy {
  default_strategy: RoutingStrategy;
  sticky_session_enabled: boolean;
  sticky_idle_ttl_sec: number;
  auto_fallback_enabled: boolean;
  fallback_chain: AcceleratorType[];
  large_vram_fallback_chain: AcceleratorType[];
  auto_release_on_zombie: boolean;
  keepalive_interval_sec: number;
  resource_poll_interval_sec: number;
  updated_at: string;
}

export interface AgentAPIKey {
  id: string;
  name: string;
  key_prefix: string;
  raw_key?: string;
  allowed_groups: string[];
  allowed_gpus: AcceleratorType[];
  default_strategy?: RoutingStrategy;
  max_budget_cu_total: number;
  max_budget_cu_daily: number;
  used_cu_total: number;
  used_cu_today: number;
  max_concurrent_vms: number;
  idle_auto_kill_min: number;
  enabled: boolean;
  last_used_at?: string;
  expires_at?: string;
  created_at: string;
}

export interface CreateAPIKeyRequest {
  name: string;
  allowed_groups?: string[];
  allowed_gpus?: AcceleratorType[];
  default_strategy?: RoutingStrategy;
  max_budget_cu_total?: number;
  max_budget_cu_daily?: number;
  max_concurrent_vms?: number;
  idle_auto_kill_min?: number;
  expires_in_days?: number;
}

export interface RouteBinding {
  sticky_key: string;
  client_name: string;
  api_key_id?: string;
  api_key_prefix?: string;
  strategy: RoutingStrategy;
  session_id: string;
  session_name: string;
  kernel_id?: string;
  account_id: string;
  account_email: string;
  proxy_summary?: string;
  accelerator: AcceleratorType;
  active_calls: number;
  idle_ttl_remaining_sec: number;
  pinned_at: string;
  last_active_at: string;
}

export type TransportType = 'mcp-http' | 'mcp-sse' | 'mcp-stdio' | 'rest' | 'cli' | 'sdk';

export interface ToolTraceEvent {
  id: string;
  timestamp: string;
  caller: string;
  api_key_prefix?: string;
  transport: TransportType;
  tool_or_endpoint: string;
  session_id?: string;
  account_id?: string;
  account_email?: string;
  requested_gpu?: AcceleratorType;
  assigned_gpu?: AcceleratorType;
  fallback_trace?: string;
  duration_ms: number;
  cu_delta: number;
  status: 'ok' | 'fallback' | 'error';
  status_code: number;
  request_preview?: string;
  response_preview?: string;
  error_message?: string;
}

export type AgentClientID =
  | 'antigravity'
  | 'claude-code'
  | 'claude-desktop'
  | 'codex'
  | 'gemini-cli'
  | 'cursor'
  | 'windsurf'
  | 'opencode';

export const ALL_AGENT_CLIENTS: AgentClientID[] = [
  'antigravity',
  'claude-code',
  'claude-desktop',
  'codex',
  'gemini-cli',
  'cursor',
  'windsurf',
  'opencode',
];

export interface AgentClientConfigStatus {
  client_id: AgentClientID;
  display_name: string;
  config_path: string;
  wsl_config_path?: string;
  format: 'json' | 'toml' | 'jsonc';
  transport: 'http' | 'stdio';
  detected: boolean;
  injected: boolean;
  backup_exists: boolean;
  backup_path?: string;
  hot_reload_note?: string;
  snippet: string;
  cli_command?: string;
  deep_link_uri?: string;
  last_synced_at?: string;
  supports_auto_inject?: boolean;
  cloud_mode?: boolean;
}

export interface AgentInjectRequest {
  client_id: AgentClientID;
  server_base_url?: string;
  api_key?: string;
  transport?: 'http' | 'stdio';
  include_wsl2?: boolean;
}

export type WSEventType =
  | 'quota_updated'
  | 'account_updated'
  | 'session_updated'
  | 'session_output'
  | 'experiment_updated'
  | 'trace_event'
  | 'guardrail_preempt'
  | 'zombie_alert'
  | 'heartbeat';

export interface SessionStreamChunk {
  session_id: string;
  experiment_id?: string;
  shard_index?: number;
  stream: 'stdout' | 'stderr' | 'colab';
  text: string;
  rich_output?: KernelRichOutput;
  timestamp: string;
}

export interface WSEvent<T = unknown> {
  type: WSEventType;
  timestamp: string;
  payload: T;
}

export interface DaemonHealthStatus {
  healthy: boolean;
  version: string;
  deployment_mode?: 'local' | 'cloud';
  auth_required?: boolean;
  public_url?: string;
  uptime_sec: number;
  keepalive_loop_sec: number;
  resource_poll_sec: number;
  anti_zombie_armed: boolean;
  contributor_firewall_on: boolean;
  active_sessions: number;
  active_experiments: number;
  connected_ws_clients: number;
  timestamp: string;
}

// ============================================================================
// Account & System Diagnostic Self-Test DTOs
// ============================================================================

export type VerifyStepStatus = 'ok' | 'error' | 'skipped';

export interface VerifyStepResult {
  id: string;
  title_zh: string;
  title_en: string;
  status: VerifyStepStatus;
  duration_ms: number;
  summary: string;
  detail?: string;
}

export interface AccountVerifyRequest {
  run_kernel_smoke?: boolean;
  accelerator?: AcceleratorType;
}

export interface AccountVerifyResult {
  account_id: string;
  email: string;
  overall_ok: boolean;
  checked_at: string;
  total_duration_ms: number;
  steps: VerifyStepResult[];
  account?: Account;
  kernel_stdout?: string;
}

export interface SystemSelfTestResult {
  overall_ok: boolean;
  mode: 'demo' | 'production';
  checked_at: string;
  steps: VerifyStepResult[];
  discovered_local_count: number;
}
