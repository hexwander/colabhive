/**
 * ColabHive v1.1 Frontend Type Contracts (`web/src/types/v11.ts`)
 * Mirrors `internal/models/v11.go` JSON wire tags 1:1.
 */

import type { AcceleratorType, RoutingStrategy } from './index';

// ============================================================================
// Compute Provider Identity (Google Colab)
// ============================================================================

export type ProviderType = 'colab';

export interface ProviderAccountMeta {
  username?: string;
  preferred_region?: string;
  default_gpu_name?: string;
}

// ============================================================================
// Pillar 1: Private AI Chat & Zero-Tunnel Local `/v1` + Ollama Gateway
// ============================================================================

export type LLMEngineType = 'ollama' | 'vllm' | 'llamacpp';

export type LLMGatewayState =
  | 'stopped'
  | 'starting'
  | 'pulling'
  | 'ready'
  | 'failover'
  | 'error';

export interface LLMModelSpec {
  id: string;
  display_name: string;
  display_name_zh: string;
  provider_family: string;
  summary: string;
  summary_zh: string;
  parameter_size: string;
  quantization: string;
  context_length: number;
  min_vram_gb: number;
  recommended_gpu: AcceleratorType;
  free_tier_ready: boolean;
  default_engine: LLMEngineType;
  ollama_tag: string;
  huggingface_repo?: string;
  tags: string[];
}

export interface LLMGatewayStatus {
  running: boolean;
  state: LLMGatewayState;
  status_step?: string;
  status_step_zh?: string;
  active_model_id?: string;
  active_model_name?: string;
  engine?: LLMEngineType;
  strategy?: RoutingStrategy;
  session_id?: string;
  account_id?: string;
  account_label?: string;
  provider?: ProviderType;
  assigned_gpu?: AcceleratorType;
  free_tier_active: boolean;
  auto_failover: boolean;
  failover_count: number;
  last_failover_note?: string;
  openai_base_url: string;
  anthropic_base_url: string;
  ollama_base_url: string;
  idle_auto_stop_min: number;
  requests_served: number;
  tokens_generated: number;
  avg_tokens_per_sec: number;
  started_at?: string;
  last_active_at?: string;
  error_message?: string;
  deploy_logs?: string[];
}

export interface LLMStartRequest {
  model_id: string;
  preferred_gpu?: AcceleratorType;
  account_id?: string;
  strategy?: RoutingStrategy;
  engine?: LLMEngineType;
  auto_failover?: boolean;
  idle_auto_stop_min?: number;
}

// ============================================================================
// Pillar 2: 1-Click AI App Store & Zero-Tunnel Private Local Port Bridge
// ============================================================================

export type AppCategory = 'video' | 'image' | 'audio' | 'workspace' | 'custom';

export type AppInstanceState =
  | 'starting'
  | 'running'
  | 'failover'
  | 'stopping'
  | 'stopped'
  | 'error';

export interface AppModelOption {
  id: string;
  name: string;
  name_zh: string;
  summary?: string;
  summary_zh?: string;
  min_vram_gb: number;
  recommended_gpu: AcceleratorType;
  download_url?: string;
  target_subdir?: string;
  filename?: string;
}

export type PluginKind = 'engine' | 'ui' | 'bundle';

export interface PluginUIConfig {
  mode?: 'remote_proxy' | 'local_bundle' | 'builtin_route';
  bundle_dir?: string;
  entry?: string;
  route?: string;
}

export interface AppTemplate {
  id: string;
  name: string;
  name_zh: string;
  tagline: string;
  tagline_zh: string;
  description: string;
  description_zh: string;
  category: AppCategory;
  icon: string;
  default_remote_port: number;
  default_local_port: number;
  min_vram_gb: number;
  recommended_gpu: AcceleratorType;
  free_tier_compatible: boolean;
  est_boot_sec: number;
  highlights: string[];
  highlights_zh: string[];
  supports_port_bridge: boolean;
  plugin_integration_tip_en?: string;
  plugin_integration_tip_zh?: string;
  author?: string;
  version?: string;
  is_plugin?: boolean;
  kind?: PluginKind;
  provides?: string[];
  consumes?: string[];
  optional_capabilities?: string[];
  permissions?: string[];
  media_api_base?: string;
  ui?: PluginUIConfig;
  model_options?: AppModelOption[];
  supports_custom_model_url?: boolean;
}

export interface AppPluginManifest {
  id: string;
  name: string;
  name_zh: string;
  tagline?: string;
  tagline_zh?: string;
  description?: string;
  description_zh?: string;
  category: AppCategory;
  icon?: string;
  repo_url?: string;
  start_command?: string;
  remote_port: number;
  local_port?: number;
  min_vram_gb?: number;
  recommended_gpu?: AcceleratorType;
  free_tier_compatible?: boolean;
  est_boot_sec?: number;
  highlights?: string[];
  highlights_zh?: string[];
  supports_port_bridge?: boolean;
  plugin_integration_tip_en?: string;
  plugin_integration_tip_zh?: string;
  author?: string;
  version?: string;
}

export interface AppInstance {
  id: string;
  template_id: string;
  name: string;
  name_zh: string;
  category: AppCategory;
  state: AppInstanceState;
  status_step?: string;
  status_step_zh?: string;
  progress_pct: number;
  session_id?: string;
  account_id?: string;
  account_label?: string;
  provider?: ProviderType;
  assigned_gpu?: AcceleratorType;
  free_tier_active: boolean;
  remote_port: number;
  local_port?: number;
  local_bridge_active: boolean;
  local_url?: string;
  web_proxy_url: string;
  idle_auto_stop_min: number;
  auto_resume_enabled: boolean;
  failover_count: number;
  last_failover_note?: string;
  selected_model_id?: string;
  selected_model_name?: string;
  model_download_url?: string;
  checkpoint_dir?: string;
  custom_repo_url?: string;
  custom_command?: string;
  provides?: string[];
  media_api_base?: string;
  studio_route?: string;
  launch_mode?: 'headless' | 'native_webui';
  error_message?: string;
  deploy_logs?: string[];
  started_at: string;
  last_active_at: string;
}

export interface AppLaunchRequest {
  template_id: string;
  name?: string;
  preferred_gpu?: AcceleratorType;
  account_id?: string;
  strategy?: RoutingStrategy;
  selected_model_id?: string;
  model_download_url?: string;
  preferred_local_port?: number;
  idle_auto_stop_min?: number;
  auto_resume_enabled?: boolean;
  custom_repo_url?: string;
  custom_command?: string;
  custom_remote_port?: number;
  launch_mode?: 'headless' | 'native_webui';
  auto_fallback?: boolean;
  queue_retry_enabled?: boolean;
  queue_max_wait_sec?: number;
}

// ============================================================================
// Plugin Platform v2 Host SDK (`/sdk/v1/*`)
// ============================================================================

export type AssetKind = 'image' | 'video' | 'audio';

export interface MediaAsset {
  id: string;
  kind: AssetKind;
  mime_type: string;
  filename: string;
  size_bytes: number;
  prompt?: string;
  negative_prompt?: string;
  model_id?: string;
  model_name?: string;
  mode?: string;
  resolution?: string;
  steps?: number;
  seed?: number;
  duration_sec?: number;
  generation_ms?: number;
  engine_id?: string;
  instance_id?: string;
  job_id?: string;
  source_plugin_id?: string;
  parent_asset_id?: string;
  file_url: string;
  created_at: string;
}

export interface CapabilityProvider {
  capability: string;
  plugin_id: string;
  instance_id?: string;
  name: string;
  name_zh: string;
  state: string;
  gpu?: string;
  account_label?: string;
  media_api_base?: string;
  native_url?: string;
}

export interface UIPluginDescriptor {
  id: string;
  name: string;
  name_zh: string;
  author?: string;
  version?: string;
  description?: string;
  description_zh?: string;
  mode: 'builtin_route' | 'local_bundle' | 'remote_proxy';
  route?: string;
  consumes?: string[];
  optional_capabilities?: string[];
  permissions?: string[];
  builtin: boolean;
}

export interface CapabilitiesSnapshot {
  sdk_version: string;
  providers: CapabilityProvider[];
  ui_plugins: UIPluginDescriptor[];
  ui_preferences: Record<string, string>;
  llm_ready: boolean;
  llm_model_name?: string;
}

export interface SandboxSession {
  plugin_id: string;
  name: string;
  name_zh: string;
  origin: string;
  url: string;
  permissions: string[];
}

export interface MediaEngineSummary {
  instance_id: string;
  template_id: string;
  name: string;
  name_zh: string;
  state: string;
  launch_mode: 'headless' | 'native_webui' | string;
  media_api_ready: boolean;
  provides: string[];
  assigned_gpu: string;
  account_label: string;
  free_tier_active: boolean;
  idle_auto_stop_min: number;
  native_url?: string;
  studio_route?: string;
  powered_by?: string;
  started_at: string;
}

export interface MediaModelInfo {
  id: string;
  name: string;
  family?: string;
  family_label?: string;
  description?: string;
  main_output?: string[];
  outputs?: string[];
  inputs?: string[];
  capabilities?: Record<string, boolean>;
  cached: boolean;
  featured?: boolean;
  default_resolution?: string;
  default_steps?: number;
}

export type MediaJobState = 'queued' | 'running' | 'done' | 'error' | 'canceled';

export interface MediaJobRequest {
  instance_id: string;
  model_id: string;
  model_name?: string;
  mode?: 't2i' | 'i2i' | 't2v' | 'i2v' | 'audio';
  prompt: string;
  negative_prompt?: string;
  resolution?: string;
  steps?: number;
  seed?: number;
  guidance_scale?: number;
  video_length?: number;
  duration_sec?: number;
  fps?: number;
  input_asset_id?: string;
  source_plugin_id?: string;
  extra_settings?: Record<string, unknown>;
}

export interface MediaJob {
  id: string;
  remote_job_id?: string;
  instance_id: string;
  engine_id: string;
  model_id: string;
  model_name?: string;
  mode: string;
  prompt: string;
  negative_prompt?: string;
  resolution?: string;
  steps?: number;
  seed?: number;
  input_asset_id?: string;
  source_plugin_id?: string;
  state: MediaJobState;
  phase?: string;
  status_text?: string;
  progress: number;
  current_step?: number;
  total_steps?: number;
  preview_data_url?: string;
  logs?: string[];
  error?: string;
  asset_ids?: string[];
  assets?: MediaAsset[];
  duration_ms?: number;
  created_at: string;
  updated_at: string;
  completed_at?: string;
}

export interface MediaJobEvent {
  type: 'snapshot' | 'progress' | 'preview' | 'log' | 'done' | 'error' | 'canceled';
  job_id: string;
  state?: MediaJobState;
  phase?: string;
  status?: string;
  progress?: number;
  current_step?: number;
  total_steps?: number;
  preview_data_url?: string;
  log_line?: string;
  error?: string;
  assets?: MediaAsset[];
  timestamp: string;
}

export interface EnhancePromptResponse {
  enhanced_prompt: string;
  model_id: string;
  model_name: string;
}

// ============================================================================
// Pillar 4: Auto-Checkpoint Preemption Relay & Shareable Savings Receipt
// ============================================================================

export interface CheckpointPolicy {
  auto_resume_enabled: boolean;
  sync_interval_sec: number;
  watch_paths: string[];
  max_snapshots_per_job: number;
  updated_at: string;
}

export interface CheckpointRecord {
  id: string;
  job_id: string;
  job_title: string;
  job_kind: 'app' | 'experiment' | 'session' | 'llm';
  source_session_id: string;
  source_account_label: string;
  resumed_session_id?: string;
  resumed_account_label?: string;
  assigned_gpu?: AcceleratorType;
  remote_path: string;
  local_snapshot_dir?: string;
  file_count: number;
  total_bytes: number;
  progress_pct: number;
  relay_count: number;
  status: 'synced' | 'relaying' | 'resumed' | 'failed';
  last_relay_note_en?: string;
  last_relay_note_zh?: string;
  resume_command?: string;
  created_at: string;
  updated_at: string;
}

export interface CheckpointSyncRequest {
  session_id: string;
  job_id?: string;
  job_title?: string;
  job_kind?: string;
  remote_path?: string;
  progress_pct?: number;
  resume_command?: string;
}

export interface CheckpointResumeRequest {
  preferred_gpu?: AcceleratorType;
  strategy?: RoutingStrategy;
  resume_command?: string;
}

export interface SavingsReceiptStats {
  generated_at: string;
  pool_title: string;
  total_accounts: number;
  providers_connected: string[];
  total_gpu_hours: number;
  free_gpu_hours_harvested: number;
  g4_equivalent_hours: number;
  total_cu_burned: number;
  saved_idle_waste_cu: number;
  waste_zero_efficiency_pct: number;
  auto_resume_relays: number;
  auto_teardowns: number;
  zombie_aborts: number;
  estimated_saved_usd: number;
  estimated_saved_cny: number;
  top_gpus_used: string[];
  rank_badge_en: string;
  rank_badge_zh: string;
  share_copy_en: string;
  share_copy_zh: string;
}
