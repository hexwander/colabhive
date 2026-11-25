import {
  DEFAULT_HARDWARE_CATALOG,
  type Account,
  type AccountPatch,
  type AccountVerifyRequest,
  type AccountVerifyResult,
  type AgentAPIKey,
  type AgentClientConfigStatus,
  type AgentClientID,
  type AgentInjectRequest,
  type CellMutationRequest,
  type CreateAPIKeyRequest,
  type CredentialBundle,
  type CredentialImportRequest,
  type DaemonHealthStatus,
  type DiscoveredLocalCredential,
  type Experiment,
  type ExperimentLaunchRequest,
  type ExperimentTeardownResult,
  type KernelExecRequest,
  type KernelExecResult,
  type NotebookCell,
  type OAuthAuthMode,
  type OAuthPendingSession,
  type OAuthStartRequest,
  type ProxyConfig,
  type ProxyProbeResult,
  type QuotaSummary,
  type RemoteFileEntry,
  type RouteBinding,
  type RouterPolicy,
  type RuntimeSession,
  type SavingsReceiptStats,
  type SessionAllocRequest,
  type SystemSelfTestResult,
  type ToolTraceEvent,
  type WSEvent,
} from '../types';
import {
  buildFallbackClientConfigs,
  DEMO_API_KEYS,
  DEMO_PROXIES,
} from '../components/layout/telemetryUi';
import {
  DEMO_REMOTE_FILES,
  DEMO_RICH_OUTPUTS,
  DEMO_ROUTER_POLICY,
} from '../components/terminal/workbenchMockData';

// ============================================================================
// Zero-Backend Demo Sandbox Detection & Session Persistence
// ============================================================================

export const IS_STATIC_DEMO_BUILD: boolean = import.meta.env.VITE_DEMO_SITE === 'true';

const DEMO_SANDBOX_SESSION_KEY = 'colabhive.demo_sandbox_v1';
const DEMO_AGENTS_STORAGE_KEY = 'colabhive.demo_agents_v1';
export const DEMO_STATE_CHANGED_EVENT = 'colabhive:demo-state-changed';

export function isDemoSiteMode(): boolean {
  if (IS_STATIC_DEMO_BUILD) {
    return true;
  }
  if (typeof window === 'undefined') {
    return false;
  }
  try {
    const qs = new URLSearchParams(window.location.search);
    if (qs.get('demo') === '1' || qs.get('COLABHIVE_DEMO') === '1') {
      sessionStorage.setItem(DEMO_SANDBOX_SESSION_KEY, '1');
      return true;
    }
    if (sessionStorage.getItem(DEMO_SANDBOX_SESSION_KEY) === '1') {
      return true;
    }
  } catch {
    // Ignore storage access errors in restricted iframes/contexts
  }
  return false;
}

const futureIso = (hoursFromNow: number) =>
  new Date(Date.now() + hoursFromNow * 3600 * 1000).toISOString();

const pastIso = (secondsAgo: number) =>
  new Date(Date.now() - secondsAgo * 1000).toISOString();

function buildEquivalenceForCu(schedulableCu: number, restrictH100 = false, freeWindows = 0) {
  const out: Account['gpu_equivalence'] = {};
  for (const spec of DEFAULT_HARDWARE_CATALOG) {
    const locked = restrictH100 && spec.accelerator === 'H100';
    const rate = spec.default_burn_rate_cu_hr;
    const effectiveCu =
      schedulableCu <= 0 && spec.free_tier_eligible && freeWindows > 0 ? 15.0 : schedulableCu;
    out[spec.accelerator] = {
      accelerator: spec.accelerator,
      display_name: spec.display_name,
      short_label: spec.short_label,
      vram_gb: spec.vram_gb,
      burn_rate_cu_hr: rate,
      calibration_source:
        spec.accelerator === 'G4' || spec.accelerator === 'A100'
          ? 'live'
          : spec.accelerator === 'L4' || spec.accelerator === 'H100'
            ? 'empirical'
            : 'reference',
      available_hours: locked || rate <= 0 ? 0 : effectiveCu / rate,
      schedulable_hours: locked || rate <= 0 ? 0 : effectiveCu / rate,
      eligible_accounts: locked ? 0 : 1,
      max_parallel_slots: locked ? 0 : 2,
      free_windows_ready: spec.free_tier_eligible ? freeWindows : 0,
      locked,
    };
  }
  return out;
}

// ============================================================================
// Initial Rich Stateful Simulation Store
// ============================================================================

function createInitialAccounts(): Account[] {
  const now = new Date().toISOString();
  return [
    {
      id: 'acc_ultra_01',
      email: 'lab-ultra@research.example',
      display_name: 'Stanford AI Ultra Core',
      alias: 'AI-Ultra-01',
      notes: 'Google AI Ultra 2,500 CU · Primary 96GB Blackwell G4 multi-shard node',
      subscription_tier: 'AI_ULTRA',
      health: 'ACTIVE_WORKING',
      enabled: true,
      priority: 100,
      groups: ['research-core', 'high-vram'],
      tags: ['blackwell-96g', 'sticky'],
      proxy_id: 'prx_us_lax_01',
      proxy: {
        ...DEMO_PROXIES[0],
        latency_ms: 48,
      },
      max_concurrent_vms: 4,
      active_assignments: 2,
      sticky_agent_count: 1,
      paid_cu_balance: 2140.0,
      reference_cycle_cu: 2500,
      schedulable_cu: 2140.0,
      live_burn_rate_cu_hr: 19.32,
      cycle_resets_at: futureIso(21 * 24 + 6),
      expiring_soon_cu: 0,
      free_quota: {
        remaining_milli_ccu: 15000,
        remaining_free_cu: 15.0,
        next_refill_at: futureIso(5.5),
      },
      gpu_entitlements: {
        G4: { accelerator: 'G4', status: 'AVAILABLE', calibrated_burn_rate: 9.66, calibration_source: 'live' },
        H100: { accelerator: 'H100', status: 'AVAILABLE', calibrated_burn_rate: 12.5, calibration_source: 'empirical' },
        A100: { accelerator: 'A100', status: 'AVAILABLE', calibrated_burn_rate: 8.88, calibration_source: 'live' },
        L4: { accelerator: 'L4', status: 'AVAILABLE', calibration_source: 'empirical' },
        T4: { accelerator: 'T4', status: 'AVAILABLE', calibration_source: 'reference' },
        V6E1: { accelerator: 'V6E1', status: 'AVAILABLE', calibration_source: 'reference' },
      },
      gpu_equivalence: buildEquivalenceForCu(2140.0, false, 1),
      public_api_supported: true,
      tunnel_api_supported: true,
      is_contributor: false,
      guardrails: {
        min_reserve_cu: 0,
        donated_cu_used_total: 0,
        donated_cu_used_today: 0,
        allow_paid_cu_burn: true,
        allowed_gpus: ['G4', 'H100', 'A100', 'L4', 'T4', 'V6E1', 'V5E1', 'CPU'],
        max_concurrent_vms: 4,
        block_drive_propagation: false,
        mask_email_in_dashboard: false,
        paused: false,
      },
      active_session_summary: '2× G4 96GB (qwen3-27b-s0, s1 · Keepalive 8s ✓)',
      last_polled_at: now,
      created_at: now,
      updated_at: now,
    },
    {
      id: 'acc_proplus_02',
      email: 'alex.chen@stanford-ai.example',
      display_name: 'Alex Chen · Colab Pro+',
      alias: 'ProPlus-Alex',
      notes: 'High-RAM A100 80GB node · Pinned sticky session for Claude Code',
      subscription_tier: 'PRO_PLUS',
      health: 'ACTIVE_WORKING',
      enabled: true,
      priority: 92,
      groups: ['research-core', 'high-vram'],
      tags: ['a100-80g', 'triton'],
      proxy_id: 'prx_us_iad_02',
      proxy: {
        ...DEMO_PROXIES[1],
        latency_ms: 61,
      },
      max_concurrent_vms: 3,
      active_assignments: 1,
      sticky_agent_count: 1,
      paid_cu_balance: 412.0,
      reference_cycle_cu: 500,
      schedulable_cu: 412.0,
      live_burn_rate_cu_hr: 10.6,
      cycle_resets_at: futureIso(16 * 24 + 4),
      expiring_soon_cu: 0,
      gpu_entitlements: {
        G4: { accelerator: 'G4', status: 'AVAILABLE', calibrated_burn_rate: 9.66, calibration_source: 'live' },
        H100: { accelerator: 'H100', status: 'AVAILABLE', calibrated_burn_rate: 12.5, calibration_source: 'empirical' },
        A100: { accelerator: 'A100', status: 'AVAILABLE', calibrated_burn_rate: 10.6, calibration_source: 'live' },
        L4: { accelerator: 'L4', status: 'AVAILABLE' },
        T4: { accelerator: 'T4', status: 'AVAILABLE' },
        V6E1: { accelerator: 'V6E1', status: 'AVAILABLE' },
      },
      gpu_equivalence: buildEquivalenceForCu(412.0, false, 0),
      public_api_supported: true,
      tunnel_api_supported: true,
      is_contributor: false,
      guardrails: {
        min_reserve_cu: 0,
        donated_cu_used_total: 0,
        donated_cu_used_today: 0,
        allow_paid_cu_burn: true,
        allowed_gpus: ['G4', 'H100', 'A100', 'L4', 'T4', 'V6E1', 'V5E1', 'CPU'],
        max_concurrent_vms: 3,
        block_drive_propagation: false,
        mask_email_in_dashboard: false,
        paused: false,
      },
      active_session_summary: 'claude-a100-80g · A100 80GB (62.4/80.0 GB · Keepalive 8s ✓)',
      last_polled_at: now,
      created_at: now,
      updated_at: now,
    },
    {
      id: 'acc_contrib_03',
      email: 'maya.lin@mit-vision.example',
      display_name: 'Donated Lab GPU · Maya',
      alias: 'Donated-Maya',
      notes: 'Friend-donated expiring 90d batch · Prioritized by Waste-Zero · Drive Firewall ON',
      subscription_tier: 'PRO_PLUS',
      health: 'ACTIVE_WORKING',
      enabled: true,
      priority: 96,
      groups: ['lab-contributors'],
      tags: ['contributed', 'expiring-6d', 'firewall-on'],
      proxy_id: 'prx_us_iad_02',
      proxy: DEMO_PROXIES[1],
      max_concurrent_vms: 2,
      active_assignments: 1,
      sticky_agent_count: 1,
      paid_cu_balance: 168.0,
      reference_cycle_cu: 200,
      custom_cycle_cu: true,
      schedulable_cu: 128.0,
      live_burn_rate_cu_hr: 4.52,
      cycle_resets_at: futureIso(6 * 24 + 3),
      earliest_batch_expires_at: futureIso(6 * 24 + 3),
      expiring_soon_cu: 128.0,
      gpu_entitlements: {
        G4: { accelerator: 'G4', status: 'AVAILABLE' },
        H100: { accelerator: 'H100', status: 'INELIGIBLE', reason: 'Disabled by contributor guardrail' },
        A100: { accelerator: 'A100', status: 'AVAILABLE', calibrated_burn_rate: 8.88, calibration_source: 'live' },
        L4: { accelerator: 'L4', status: 'AVAILABLE', calibrated_burn_rate: 4.52, calibration_source: 'live' },
        T4: { accelerator: 'T4', status: 'AVAILABLE' },
      },
      gpu_equivalence: buildEquivalenceForCu(128.0, true, 0),
      public_api_supported: false,
      tunnel_api_supported: true,
      is_contributor: false,
      invite_code: 'ch_inv_demo2026',
      guardrails: {
        min_reserve_cu: 40,
        max_donated_cu_total: 160,
        max_donated_cu_daily: 45,
        donated_cu_used_total: 32.0,
        donated_cu_used_today: 9.4,
        allow_paid_cu_burn: true,
        allowed_gpus: ['G4', 'A100', 'L4', 'T4'],
        max_concurrent_vms: 2,
        block_drive_propagation: true,
        mask_email_in_dashboard: false,
        paused: false,
        receipt_token: 'ch_rcpt_demo_maya2026',
      },
      active_session_summary: 'codex-lora-l4 · L4 24GB (18.6/24.0 GB · Drive Firewall ✓)',
      last_polled_at: now,
      created_at: now,
      updated_at: now,
    },
    {
      id: 'acc_free_04',
      email: 'dev-pro@robotics.example',
      display_name: 'Free T4 Standby · Robotics',
      alias: 'Free-T4-Standby',
      notes: 'Dynamic Free Tier T4 16GB standby window · Zero paid CU burn',
      subscription_tier: 'FREE',
      health: 'HEALTHY',
      enabled: true,
      priority: 65,
      groups: ['sandbox', 'free-window'],
      tags: ['free-t4', 'standby'],
      proxy_id: 'prx_sg_03',
      proxy: DEMO_PROXIES[2],
      max_concurrent_vms: 1,
      active_assignments: 0,
      sticky_agent_count: 0,
      paid_cu_balance: 0,
      reference_cycle_cu: 100,
      schedulable_cu: 0,
      live_burn_rate_cu_hr: 0,
      expiring_soon_cu: 0,
      free_quota: {
        remaining_milli_ccu: 15000,
        remaining_free_cu: 15.0,
        next_refill_at: futureIso(4.2),
      },
      gpu_entitlements: {
        G4: { accelerator: 'G4', status: 'TIER_RESTRICTED', reason: 'Colab Pro / Ultra required' },
        H100: { accelerator: 'H100', status: 'TIER_RESTRICTED', reason: 'Colab Pro+ / Ultra required' },
        A100: { accelerator: 'A100', status: 'TIER_RESTRICTED', reason: 'Colab Pro / Ultra required' },
        L4: { accelerator: 'L4', status: 'TIER_RESTRICTED', reason: 'Colab Pro required' },
        T4: { accelerator: 'T4', status: 'AVAILABLE', calibrated_burn_rate: 0, calibration_source: 'reference' },
        CPU: { accelerator: 'CPU', status: 'AVAILABLE', calibrated_burn_rate: 0, calibration_source: 'reference' },
      },
      gpu_equivalence: buildEquivalenceForCu(0, true, 1),
      public_api_supported: false,
      tunnel_api_supported: true,
      is_contributor: false,
      guardrails: {
        min_reserve_cu: 0,
        donated_cu_used_total: 0,
        donated_cu_used_today: 0,
        allow_paid_cu_burn: false,
        allowed_gpus: ['T4', 'CPU'],
        max_concurrent_vms: 1,
        block_drive_propagation: false,
        mask_email_in_dashboard: false,
        paused: false,
      },
      last_polled_at: now,
      created_at: now,
      updated_at: now,
    },
  ];
}

function createInitialSessions(): RuntimeSession[] {
  const now = new Date().toISOString();
  return [
    {
      id: 'sess_g4_shard0',
      name: 'qwen3-27b-shard-0',
      notebook_name: 'qwen3-27b-shard-0.ipynb',
      account_id: 'acc_ultra_01',
      account_email: 'lab-ultra@research.example',
      backend: 'public',
      runtime_name: 'runtimes/r-blackwell-g4-96g-01',
      endpoint: 'gpu-g4-s-9f21k8a',
      notebook_hash: 'nbh_Q29sYWJIaXZlX1F3ZW4zXzI3Ql9TaGFyZDBfRzRfOTZH',
      proxy_url: 'https://8080-gpu-g4-s-9f21k8a.us-west1.colab.googleusercontent.com',
      variant: 'GPU',
      requested_accelerator: 'G4',
      accelerator: 'G4',
      fallback_applied: false,
      shape: 'HIGH_RAM',
      status: 'BUSY',
      status_message: 'Executing Shard #0 (PID 4182 · BF16 vLLM Worker · 1,142 tok/s)',
      kernel_id: '8f4c12a0-9e11-48bc-a4f1-002918471a00',
      jupyter_session_id: 'jsess_01',
      burn_rate_cu_hr: 9.66,
      accumulated_cu: 3.48,
      colab_web_url: 'https://colab.research.google.com/',
      experiment_id: 'exp_qwen3_27b_eval',
      shard_index: 0,
      sticky_key: 'mcp:antigravity:exp-main',
      api_key_id: 'key_prod_agy',
      keepalive_healthy: true,
      last_keepalive_at: pastIso(2),
      zmq_reconnect_count: 0,
      uptime_sec: 1296,
      created_at: pastIso(1296),
      updated_at: now,
      resources: {
        memory_total_bytes: 89_657_389_056,
        memory_free_bytes: 61_203_947_520,
        memory_used_bytes: 28_453_441_536,
        disk_total_bytes: 253_055_090_688,
        disk_used_bytes: 78_383_153_152,
        hf_cache_bytes: 58_196_803_584,
        sampled_at: now,
        gpus: [
          {
            name: 'NVIDIA RTX PRO 6000 Blackwell (96GB)',
            memory_used_bytes: 58_196_803_584, // 54.2 GB
            memory_total_bytes: 103_079_215_104, // 96.0 GB
            gpu_utilization: 0.94,
            memory_utilization: 0.56,
            temperature_c: 66,
            power_watts: 284,
            ever_used: true,
          },
        ],
      },
      worker: {
        pid: 4182,
        proc_state: 'R',
        alive: true,
        zombie_detected: false,
        has_fatal_trace: false,
        log_offset: 152480,
        last_checked_at: pastIso(2),
      },
      timeline: [
        {
          id: 'st-1',
          label: 'Allocate NVIDIA RTX PRO 6000 Blackwell 96GB (AI-Ultra-01)',
          status: 'completed',
          duration_ms: 3120,
          started_at: pastIso(1295),
          detail: 'colaboratory.googleapis.com/v1beta via socks5://us-lax (48ms)',
        },
        {
          id: 'st-2',
          label: 'Hydrate /root/.cache/huggingface (Qwen/Qwen3-27B BF16 · 54.2 GB)',
          status: 'completed',
          duration_ms: 34200,
          started_at: pastIso(1288),
          detail: 'Zero-disk-spill NVMe warm cache',
        },
        {
          id: 'st-3',
          label: 'Shard #0 Evaluation + /proc/4182/stat Anti-Zombie Watchdog',
          status: 'running',
          duration_ms: 1240000,
          started_at: pastIso(1250),
          detail: '98 / 125 MMLU-Pro reasoning chains verified',
        },
      ],
      cells: [
        {
          cell_id: 'cell_01',
          index: 0,
          cell_type: 'code',
          execution_count: 1,
          source:
            '!nvidia-smi --query-gpu=name,memory.used,memory.total,utilization.gpu,temperature.gpu --format=csv',
        },
        {
          cell_id: 'cell_02',
          index: 1,
          cell_type: 'code',
          execution_count: 2,
          source:
            'import torch\nprint("Device:", torch.cuda.get_device_name(0))\nprint(f"VRAM: {torch.cuda.get_device_properties(0).total_memory / 1024**3:.1f} GB | BF16: {torch.cuda.is_bf16_supported()}")',
          outputs: [DEMO_RICH_OUTPUTS[0]],
        },
      ],
    },
    {
      id: 'sess_g4_shard1',
      name: 'qwen3-27b-shard-1',
      notebook_name: 'qwen3-27b-shard-1.ipynb',
      account_id: 'acc_ultra_01',
      account_email: 'lab-ultra@research.example',
      backend: 'public',
      runtime_name: 'runtimes/r-blackwell-g4-96g-02',
      endpoint: 'gpu-g4-s-4b88c1e',
      notebook_hash: 'nbh_Q29sYWJIaXZlX1F3ZW4zXzI3Ql9TaGFyZDFfRzRfOTZH',
      proxy_url: 'https://8080-gpu-g4-s-4b88c1e.us-central1.colab.googleusercontent.com',
      variant: 'GPU',
      requested_accelerator: 'G4',
      accelerator: 'G4',
      fallback_applied: false,
      shape: 'HIGH_RAM',
      status: 'BUSY',
      status_message: 'Executing Shard #1 (PID 3910 · BF16 vLLM Worker · 1,108 tok/s)',
      kernel_id: '3a9d7712-112c-4f90-9c2a-77819200b112',
      burn_rate_cu_hr: 9.66,
      accumulated_cu: 3.41,
      colab_web_url: 'https://colab.research.google.com/',
      experiment_id: 'exp_qwen3_27b_eval',
      shard_index: 1,
      keepalive_healthy: true,
      last_keepalive_at: pastIso(3),
      zmq_reconnect_count: 0,
      uptime_sec: 1272,
      created_at: pastIso(1272),
      updated_at: now,
      resources: {
        memory_total_bytes: 89_657_389_056,
        memory_free_bytes: 63_100_000_000,
        memory_used_bytes: 26_557_389_056,
        disk_total_bytes: 253_055_090_688,
        disk_used_bytes: 76_100_000_000,
        hf_cache_bytes: 58_196_803_584,
        sampled_at: now,
        gpus: [
          {
            name: 'NVIDIA RTX PRO 6000 Blackwell (96GB)',
            memory_used_bytes: 55_619_829_760, // 51.8 GB
            memory_total_bytes: 103_079_215_104, // 96.0 GB
            gpu_utilization: 0.91,
            memory_utilization: 0.54,
            temperature_c: 64,
            power_watts: 276,
            ever_used: true,
          },
        ],
      },
      worker: {
        pid: 3910,
        proc_state: 'R',
        alive: true,
        zombie_detected: false,
        has_fatal_trace: false,
        log_offset: 141220,
        last_checked_at: pastIso(3),
      },
    },
    {
      id: 'sess_a100_80g',
      name: 'claude-triton-a100-80g',
      notebook_name: 'claude-triton-a100-80g.ipynb',
      account_id: 'acc_proplus_02',
      account_email: 'alex.chen@stanford-ai.example',
      backend: 'public',
      runtime_name: 'runtimes/r-ampere-a100-80g-04',
      endpoint: 'gpu-a100-s-77d19f',
      notebook_hash: 'nbh_Q2xhdWRlQ29kZV9Ucml0b25fQTEwMF84MEdCX0hpZ2hSQU0',
      proxy_url: 'https://8080-gpu-a100-s-77d19f.us-east1.colab.googleusercontent.com',
      variant: 'GPU',
      requested_accelerator: 'A100',
      accelerator: 'A100',
      fallback_applied: false,
      shape: 'HIGH_RAM',
      status: 'BUSY',
      status_message: 'Sticky Claude Code Kernel + Shard #2 (Triton 3.2 · 80GB HBM2e)',
      kernel_id: 'c4209e11-6a18-4209-b811-902184ab7712',
      burn_rate_cu_hr: 10.6,
      accumulated_cu: 4.86,
      colab_web_url: 'https://colab.research.google.com/',
      experiment_id: 'exp_qwen3_27b_eval',
      shard_index: 2,
      sticky_key: 'mcp:claude-code:triton-bench',
      api_key_id: 'key_dev_claude',
      keepalive_healthy: true,
      last_keepalive_at: pastIso(2),
      zmq_reconnect_count: 0,
      uptime_sec: 1650,
      created_at: pastIso(1650),
      updated_at: now,
      resources: {
        memory_total_bytes: 179_314_778_112,
        memory_free_bytes: 118_400_000_000,
        memory_used_bytes: 60_914_778_112,
        disk_total_bytes: 253_055_090_688,
        disk_used_bytes: 64_200_000_000,
        hf_cache_bytes: 48_400_000_000,
        sampled_at: now,
        gpus: [
          {
            name: 'NVIDIA A100-SXM4-80GB',
            memory_used_bytes: 67_001_489_818, // 62.4 GB
            memory_total_bytes: 85_899_345_920, // 80.0 GB
            gpu_utilization: 0.87,
            memory_utilization: 0.78,
            temperature_c: 59,
            power_watts: 312,
            ever_used: true,
          },
        ],
      },
      worker: {
        pid: 2814,
        proc_state: 'R',
        alive: true,
        zombie_detected: false,
        has_fatal_trace: false,
        log_offset: 94120,
        last_checked_at: pastIso(2),
      },
    },
    {
      id: 'sess_l4_24g',
      name: 'codex-lora-eval-l4',
      notebook_name: 'codex-lora-eval-l4.ipynb',
      account_id: 'acc_contrib_03',
      account_email: 'maya.lin@mit-vision.example',
      backend: 'tunnel',
      runtime_name: 'runtimes/r-ada-l4-24g-07',
      endpoint: 'gpu-l4-s-3c91a2',
      notebook_hash: 'nbh_Q29kZXhfTG9SQV9MNF8yNEdCX0RvbmF0ZWRfRmlyZXdhbGw',
      proxy_url: 'https://8080-gpu-l4-s-3c91a2.us-east4.colab.googleusercontent.com',
      variant: 'GPU',
      requested_accelerator: 'L4',
      accelerator: 'L4',
      fallback_applied: false,
      shape: 'HIGH_RAM',
      status: 'BUSY',
      status_message: 'Waste-Zero Expiring CU · Contributor Drive Firewall Active',
      kernel_id: '7b019e44-55c1-44e8-913a-88192014c811',
      burn_rate_cu_hr: 4.52,
      accumulated_cu: 1.54,
      colab_web_url: 'https://colab.research.google.com/',
      experiment_id: 'exp_qwen3_27b_eval',
      shard_index: 3,
      sticky_key: 'mcp:codex:lora-eval',
      keepalive_healthy: true,
      last_keepalive_at: pastIso(4),
      zmq_reconnect_count: 0,
      uptime_sec: 1228,
      created_at: pastIso(1228),
      updated_at: now,
      resources: {
        memory_total_bytes: 67_645_734_912,
        memory_free_bytes: 49_200_000_000,
        memory_used_bytes: 18_445_734_912,
        disk_total_bytes: 120_259_084_288,
        disk_used_bytes: 34_800_000_000,
        sampled_at: now,
        gpus: [
          {
            name: 'NVIDIA L4 (24GB GDDR6)',
            memory_used_bytes: 19_971_597_926, // 18.6 GB
            memory_total_bytes: 25_769_803_776, // 24.0 GB
            gpu_utilization: 0.78,
            memory_utilization: 0.775,
            temperature_c: 56,
            power_watts: 68,
            ever_used: true,
          },
        ],
      },
      worker: {
        pid: 4409,
        proc_state: 'R',
        alive: true,
        zombie_detected: false,
        has_fatal_trace: false,
        log_offset: 71840,
        last_checked_at: pastIso(3),
      },
    },
  ];
}

function createInitialExperiments(): Experiment[] {
  const now = new Date().toISOString();
  return [
    {
      id: 'exp_qwen3_27b_eval',
      title: 'Qwen3-27B MMLU-Pro Reasoning Sweep · Shards=4 Parallel',
      status: 'RUNNING',
      script_path: 'benchmarks/eval_qwen3_27b_shard.py',
      accelerator: 'G4',
      high_mem: true,
      num_shards: 4,
      strategy: 'waste-zero',
      completed_shards: 0,
      running_shards: 4,
      failed_shards: 0,
      synced_records_total: 392,
      target_records_total: 500,
      aggregate_burn_rate_cu_hr: 34.44,
      cu_burned_so_far: 13.29,
      estimated_total_cu: 16.95,
      auto_stop_on_complete: true,
      local_output_dir: './outputs/qwen3_27b_eval',
      merged_jsonl_path: './outputs/qwen3_27b_eval/merged_results.jsonl',
      artifacts: DEMO_RICH_OUTPUTS,
      created_at: pastIso(1290),
      updated_at: now,
      shards: [
        {
          shard_id: 0,
          session_id: 'sess_g4_shard0',
          session_name: 'qwen3-27b-shard-0',
          account_id: 'acc_ultra_01',
          account_email: 'lab-ultra@research.example',
          accelerator: 'G4',
          status: 'RUNNING',
          pid: 4182,
          proc_state: 'R',
          zombie_detected: false,
          progress_pct: 82,
          synced_records: 103,
          target_records: 125,
          vram_used_gb: 54.2,
          vram_total_gb: 96.0,
          sm_util_pct: 94,
          temp_c: 66,
          cu_burned: 3.48,
          startup_self_check: 'PASSED',
          log_tail: 'Step 103/125 | mmlu_pro=0.8240 | tok/s=1142.8 | synced shard_0.jsonl',
          started_at: pastIso(1290),
        },
        {
          shard_id: 1,
          session_id: 'sess_g4_shard1',
          session_name: 'qwen3-27b-shard-1',
          account_id: 'acc_ultra_01',
          account_email: 'lab-ultra@research.example',
          accelerator: 'G4',
          status: 'RUNNING',
          pid: 3910,
          proc_state: 'R',
          zombie_detected: false,
          progress_pct: 78,
          synced_records: 98,
          target_records: 125,
          vram_used_gb: 51.8,
          vram_total_gb: 96.0,
          sm_util_pct: 91,
          temp_c: 64,
          cu_burned: 3.41,
          startup_self_check: 'PASSED',
          log_tail: 'Step 98/125 | mmlu_pro=0.8184 | tok/s=1108.4 | synced shard_1.jsonl',
          started_at: pastIso(1270),
        },
        {
          shard_id: 2,
          session_id: 'sess_a100_80g',
          session_name: 'claude-triton-a100-80g',
          account_id: 'acc_proplus_02',
          account_email: 'alex.chen@stanford-ai.example',
          accelerator: 'A100',
          status: 'RUNNING',
          pid: 2814,
          proc_state: 'R',
          zombie_detected: false,
          progress_pct: 85,
          synced_records: 106,
          target_records: 125,
          vram_used_gb: 62.4,
          vram_total_gb: 80.0,
          sm_util_pct: 87,
          temp_c: 59,
          cu_burned: 4.86,
          startup_self_check: 'PASSED',
          log_tail: 'Step 106/125 | mmlu_pro=0.8296 | HBM2e=1.52 TB/s | synced shard_2.jsonl',
          started_at: pastIso(1260),
        },
        {
          shard_id: 3,
          session_id: 'sess_l4_24g',
          session_name: 'codex-lora-eval-l4',
          account_id: 'acc_contrib_03',
          account_email: 'maya.lin@mit-vision.example',
          accelerator: 'L4',
          status: 'RUNNING',
          pid: 4409,
          proc_state: 'R',
          zombie_detected: false,
          progress_pct: 68,
          synced_records: 85,
          target_records: 125,
          vram_used_gb: 18.6,
          vram_total_gb: 24.0,
          sm_util_pct: 78,
          temp_c: 56,
          cu_burned: 1.54,
          startup_self_check: 'PASSED',
          log_tail: 'Step 85/125 | INT8 KV-cache | Maya reserve floor 40.0 CU protected',
          started_at: pastIso(1225),
        },
      ],
    },
  ];
}

function loadSavedInjectedAgents(): Set<AgentClientID> {
  const defaults: AgentClientID[] = [
    'antigravity',
    'claude-code',
    'cursor',
    'codex',
    'gemini-cli',
  ];
  try {
    const raw = sessionStorage.getItem(DEMO_AGENTS_STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as unknown;
      if (Array.isArray(parsed)) {
        return new Set(parsed as AgentClientID[]);
      }
    }
  } catch {
    // Fall back to defaults
  }
  return new Set(defaults);
}

function saveInjectedAgents(injectedSet: Set<AgentClientID>): void {
  try {
    sessionStorage.setItem(DEMO_AGENTS_STORAGE_KEY, JSON.stringify(Array.from(injectedSet)));
  } catch {
    // Ignore storage errors
  }
}

function createInitialAgentClients(): AgentClientConfigStatus[] {
  const injectedSet = loadSavedInjectedAgents();
  const base = buildFallbackClientConfigs('http://127.0.0.1:7860', '', 'http');
  const now = new Date().toISOString();
  return base.map((c) => {
    const isInjected = injectedSet.has(c.client_id);
    return {
      ...c,
      detected: true,
      injected: isInjected,
      backup_exists: isInjected,
      backup_path: isInjected ? `${c.config_path}.colabhive.bak` : undefined,
      supports_auto_inject: true,
      last_synced_at: isInjected ? now : undefined,
    };
  });
}

function createInitialBindings(): RouteBinding[] {
  return [
    {
      sticky_key: 'mcp:antigravity:exp-main',
      client_name: 'Antigravity IDE',
      api_key_id: 'key_prod_agy',
      api_key_prefix: 'ch_sk_live_9f8a4c',
      strategy: 'waste-zero',
      session_id: 'sess_g4_shard0',
      session_name: 'qwen3-27b-shard-0',
      kernel_id: '8f4c12a0-9e11',
      account_id: 'acc_ultra_01',
      account_email: 'lab-ultra@research.example',
      proxy_summary: 'socks5://38.142.*.*:1080',
      accelerator: 'G4',
      active_calls: 2,
      idle_ttl_remaining_sec: 1720,
      pinned_at: pastIso(1280),
      last_active_at: pastIso(3),
    },
    {
      sticky_key: 'mcp:claude-code:triton-bench',
      client_name: 'Claude Code CLI',
      api_key_id: 'key_dev_claude',
      api_key_prefix: 'ch_sk_live_3d1b7e',
      strategy: 'sticky-session',
      session_id: 'sess_a100_80g',
      session_name: 'claude-triton-a100-80g',
      kernel_id: 'c4209e11-6a18',
      account_id: 'acc_proplus_02',
      account_email: 'alex.chen@stanford-ai.example',
      proxy_summary: 'http://198.51.*.*:8080',
      accelerator: 'A100',
      active_calls: 1,
      idle_ttl_remaining_sec: 1640,
      pinned_at: pastIso(1600),
      last_active_at: pastIso(6),
    },
    {
      sticky_key: 'mcp:codex:lora-eval',
      client_name: 'Codex CLI',
      api_key_id: 'key_prod_agy',
      api_key_prefix: 'ch_sk_live_9f8a4c',
      strategy: 'waste-zero',
      session_id: 'sess_l4_24g',
      session_name: 'codex-lora-eval-l4',
      kernel_id: '7b019e44-55c1',
      account_id: 'acc_contrib_03',
      account_email: 'maya.lin@mit-vision.example',
      proxy_summary: 'http://198.51.*.*:8080',
      accelerator: 'L4',
      active_calls: 1,
      idle_ttl_remaining_sec: 1580,
      pinned_at: pastIso(1200),
      last_active_at: pastIso(11),
    },
  ];
}

function createInitialTraces(): ToolTraceEvent[] {
  return [
    {
      id: 'tr_demo_01',
      timestamp: pastIso(2),
      caller: 'Claude Code CLI',
      api_key_prefix: 'ch_sk_live_3d1b7e',
      transport: 'mcp-http',
      tool_or_endpoint: 'execute_code',
      session_id: 'sess_a100_80g',
      account_id: 'acc_proplus_02',
      account_email: 'alex.chen@stanford-ai.example',
      requested_gpu: 'A100',
      assigned_gpu: 'A100',
      duration_ms: 348,
      cu_delta: 0.001,
      status: 'ok',
      status_code: 200,
      request_preview: JSON.stringify(
        {
          jsonrpc: '2.0',
          method: 'tools/call',
          params: {
            name: 'execute_code',
            arguments: {
              session_id: 'sess_a100_80g',
              code: 'bench_flash_attn_v3(seq_len=8192, dtype="bf16")',
            },
          },
        },
        null,
        2,
      ),
      response_preview: JSON.stringify(
        {
          status: 'ok',
          stdout: 'A100-80GB FlashAttn: 1.54 TB/s HBM2e (0.138 ms)\n',
          durationMs: 348,
        },
        null,
        2,
      ),
    },
    {
      id: 'tr_demo_02',
      timestamp: pastIso(9),
      caller: 'Antigravity IDE',
      api_key_prefix: 'ch_sk_live_9f8a4c',
      transport: 'mcp-http',
      tool_or_endpoint: 'execute_code',
      session_id: 'sess_g4_shard0',
      account_id: 'acc_ultra_01',
      account_email: 'lab-ultra@research.example',
      requested_gpu: 'G4',
      assigned_gpu: 'G4',
      duration_ms: 294,
      cu_delta: 0.0008,
      status: 'ok',
      status_code: 200,
      request_preview: JSON.stringify(
        {
          jsonrpc: '2.0',
          method: 'tools/call',
          params: {
            name: 'execute_code',
            arguments: {
              session_id: 'sess_g4_shard0',
              code: 'import torch; print(torch.cuda.memory_allocated() / 1e9)',
            },
          },
        },
        null,
        2,
      ),
      response_preview: JSON.stringify(
        { status: 'ok', stdout: '54.21 GB\n', durationMs: 294 },
        null,
        2,
      ),
    },
    {
      id: 'tr_demo_03',
      timestamp: pastIso(24),
      caller: 'Codex CLI',
      api_key_prefix: 'ch_sk_live_9f8a4c',
      transport: 'mcp-stdio',
      tool_or_endpoint: 'create_runtime',
      session_id: 'sess_l4_24g',
      account_id: 'acc_contrib_03',
      account_email: 'maya.lin@mit-vision.example',
      requested_gpu: 'L4',
      assigned_gpu: 'L4',
      duration_ms: 2840,
      cu_delta: 0,
      status: 'ok',
      status_code: 200,
      request_preview: JSON.stringify(
        {
          accelerator: 'L4',
          strategy: 'waste-zero',
          reason: 'Prioritized 6-day expiring donated CU on acc_contrib_03',
        },
        null,
        2,
      ),
    },
    {
      id: 'tr_demo_04',
      timestamp: pastIso(52),
      caller: 'Cursor IDE',
      api_key_prefix: 'ch_sk_live_9f8a4c',
      transport: 'mcp-http',
      tool_or_endpoint: 'create_runtime',
      session_id: 'sess_g4_shard0',
      account_id: 'acc_ultra_01',
      account_email: 'lab-ultra@research.example',
      requested_gpu: 'H100',
      assigned_gpu: 'G4',
      fallback_trace:
        'H100 HTTP 400 regional stockout -> 41ms Auto-Fallback -> G4 96GB GDDR7 (>=80GB VRAM satisfied) -> 200 OK',
      duration_ms: 3190,
      cu_delta: 0,
      status: 'fallback',
      status_code: 200,
    },
  ];
}

interface DemoStoreState {
  accounts: Account[];
  sessions: RuntimeSession[];
  experiments: Experiment[];
  clients: AgentClientConfigStatus[];
  policy: RouterPolicy;
  bindings: RouteBinding[];
  traces: ToolTraceEvent[];
  apiKeys: AgentAPIKey[];
  proxies: ProxyConfig[];
  filesBySession: Map<string, RemoteFileEntry[]>;
  autostart: boolean;
  savedWasteCu: number;
  autoTeardownsCount: number;
  zombieAbortsCount: number;
  tickSeq: number;
}

const store: DemoStoreState = {
  accounts: createInitialAccounts(),
  sessions: createInitialSessions(),
  experiments: createInitialExperiments(),
  clients: createInitialAgentClients(),
  policy: { ...DEMO_ROUTER_POLICY },
  bindings: createInitialBindings(),
  traces: createInitialTraces(),
  apiKeys: DEMO_API_KEYS.map((k) => ({ ...k })),
  proxies: DEMO_PROXIES.map((p) => ({ ...p })),
  filesBySession: new Map<string, RemoteFileEntry[]>(),
  autostart: true,
  savedWasteCu: 348.6,
  autoTeardownsCount: 23,
  zombieAbortsCount: 5,
  tickSeq: 0,
};

type WsSubscriber = (evt: WSEvent) => void;
const wsSubscribers = new Set<WsSubscriber>();
let tickerHandle: number | null = null;

function notifyStateChanged(): void {
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent(DEMO_STATE_CHANGED_EVENT));
  }
}

function broadcastWs(evt: WSEvent): void {
  for (const sub of wsSubscribers) {
    try {
      sub(evt);
    } catch {
      // Ignore subscriber error
    }
  }
}

function recomputeAccountAssignments(): void {
  const active = store.sessions.filter((s) => s.status !== 'STOPPED' && s.status !== 'LOST');
  store.accounts = store.accounts.map((acc) => {
    const mySessions = active.filter((s) => s.account_id === acc.id);
    const burn = mySessions.reduce((sum, s) => sum + (s.burn_rate_cu_hr || 0), 0);
    const nextHealth =
      !acc.enabled || acc.guardrails?.paused
        ? 'PAUSED_MANUAL'
        : acc.health === 'COOLDOWN_RATE_LIMIT'
          ? 'COOLDOWN_RATE_LIMIT'
          : mySessions.length > 0
            ? 'ACTIVE_WORKING'
            : 'HEALTHY';
    return {
      ...acc,
      active_assignments: mySessions.length,
      live_burn_rate_cu_hr: Number(burn.toFixed(2)),
      health: nextHealth,
      updated_at: new Date().toISOString(),
    };
  });
}

export function computeDemoQuotaSummary(): QuotaSummary {
  const enabledAccounts = store.accounts.filter((a) => a.enabled && a.health !== 'REVOKED_BY_USER');
  const activeSessions = store.sessions.filter(
    (s) => s.status !== 'STOPPED' && s.status !== 'LOST',
  );

  const totalPaidCu = store.accounts.reduce((s, a) => s + Math.max(0, a.paid_cu_balance), 0);
  const totalFreeCu = store.accounts.reduce(
    (s, a) => s + (a.subscription_tier === 'FREE' ? (a.free_quota?.remaining_free_cu ?? 0) : 0),
    0,
  );
  const totalAvailableCu = Number((totalPaidCu + totalFreeCu).toFixed(2));

  const schedulableCu = Number(
    enabledAccounts
      .reduce((s, a) => {
        if (a.subscription_tier === 'FREE' && a.paid_cu_balance <= 0) {
          return s + (a.free_quota?.remaining_free_cu ?? 0);
        }
        const reserve = a.is_contributor ? (a.guardrails?.min_reserve_cu ?? 0) : 0;
        return s + Math.max(0, a.paid_cu_balance - reserve);
      }, 0)
      .toFixed(2),
  );

  const activeBurn = Number(
    activeSessions.reduce((s, sess) => s + (sess.burn_rate_cu_hr || 0), 0).toFixed(2),
  );

  const totalOnlineVramGb = Math.round(
    activeSessions.reduce((s, sess) => {
      const memBytes = sess.resources?.gpus?.[0]?.memory_total_bytes ?? 0;
      if (memBytes > 0) return s + memBytes / 1_073_741_824;
      const spec = DEFAULT_HARDWARE_CATALOG.find((h) => h.accelerator === sess.accelerator);
      return s + (spec?.vram_gb ?? 0);
    }, 0),
  );

  const totalSlots = enabledAccounts.reduce((s, a) => s + Math.max(1, a.max_concurrent_vms), 0);
  const freeWindows = enabledAccounts.filter(
    (a) => (a.free_quota?.remaining_free_cu ?? 0) > 0,
  ).length;

  const expiringAcc = store.accounts
    .filter((a) => a.expiring_soon_cu > 0 && a.earliest_batch_expires_at)
    .sort(
      (a, b) =>
        Date.parse(a.earliest_batch_expires_at!) - Date.parse(b.earliest_batch_expires_at!),
    )[0];

  const gpuEq: QuotaSummary['gpu_equivalence'] = {};
  for (const spec of DEFAULT_HARDWARE_CATALOG) {
    const rate = spec.default_burn_rate_cu_hr;
    const eligible = enabledAccounts.filter((a) => {
      const st = a.gpu_entitlements?.[spec.accelerator]?.status;
      if (st === 'TIER_RESTRICTED' || st === 'INELIGIBLE') return false;
      if (
        a.is_contributor &&
        a.guardrails.allowed_gpus.length > 0 &&
        !a.guardrails.allowed_gpus.includes(spec.accelerator)
      ) {
        return false;
      }
      if (a.subscription_tier === 'FREE' && !spec.free_tier_eligible) return false;
      return true;
    });

    const eligibleCu = eligible.reduce((s, a) => {
      if (a.subscription_tier === 'FREE' && a.paid_cu_balance <= 0) {
        return s + (a.free_quota?.remaining_free_cu ?? 0);
      }
      const reserve = a.is_contributor ? (a.guardrails?.min_reserve_cu ?? 0) : 0;
      return s + Math.max(0, a.paid_cu_balance - reserve);
    }, 0);

    gpuEq[spec.accelerator] = {
      accelerator: spec.accelerator,
      display_name: spec.display_name,
      short_label: spec.short_label,
      vram_gb: spec.vram_gb,
      burn_rate_cu_hr: rate,
      calibration_source:
        spec.accelerator === 'G4' || spec.accelerator === 'A100'
          ? 'live'
          : spec.accelerator === 'L4' || spec.accelerator === 'H100'
            ? 'empirical'
            : 'reference',
      available_hours: rate > 0 ? Number((totalAvailableCu / rate).toFixed(2)) : 0,
      schedulable_hours: rate > 0 ? Number((eligibleCu / rate).toFixed(2)) : 0,
      eligible_accounts: eligible.length,
      max_parallel_slots: eligible.reduce((s, a) => s + Math.max(1, a.max_concurrent_vms), 0),
      free_windows_ready: spec.free_tier_eligible ? freeWindows : 0,
      locked: eligible.length === 0,
    };
  }

  return {
    accounts_total: store.accounts.length,
    accounts_healthy: enabledAccounts.length,
    accounts_working: store.accounts.filter((a) => a.active_assignments > 0).length,
    accounts_contributor: store.accounts.filter((a) => a.is_contributor).length,
    total_available_cu: totalAvailableCu,
    schedulable_cu: schedulableCu,
    estimated_value_usd: Number((totalAvailableCu * 0.1).toFixed(2)),
    active_burn_rate_cu_hr: activeBurn,
    pool_depletion_eta_hours:
      activeBurn > 0 ? Number((schedulableCu / activeBurn).toFixed(2)) : undefined,
    earliest_batch_expiry_cu: expiringAcc?.expiring_soon_cu ?? 0,
    earliest_batch_expires_at: expiringAcc?.earliest_batch_expires_at,
    active_vms: activeSessions.length,
    total_vm_slots: totalSlots,
    total_online_vram_gb: totalOnlineVramGb,
    free_t4_windows_ready: freeWindows,
    saved_idle_waste_cu: store.savedWasteCu,
    auto_teardowns_count: store.autoTeardownsCount,
    zombie_aborts_count: store.zombieAbortsCount,
    gpu_equivalence: gpuEq,
    updated_at: new Date().toISOString(),
  };
}

// ============================================================================
// Synthetic 2.5s Telemetry Stream & Failover Simulator
// ============================================================================

const SYNTHETIC_CALLERS = [
  { caller: 'Claude Code CLI', tool: 'execute_code', sessId: 'sess_a100_80g' },
  { caller: 'Antigravity IDE', tool: 'execute_code', sessId: 'sess_g4_shard0' },
  { caller: 'Codex CLI', tool: 'execute_code', sessId: 'sess_l4_24g' },
  { caller: 'Cursor IDE', tool: 'pool_quota_summary', sessId: 'sess_g4_shard1' },
];

function runSyntheticTick(): void {
  store.tickSeq += 1;
  const nowIso = new Date().toISOString();

  // 1. Gently jitter GPU utilization, temperature & uptime on active sessions
  for (const sess of store.sessions) {
    if (sess.status === 'STOPPED' || sess.status === 'LOST') continue;
    sess.uptime_sec += 3;
    sess.accumulated_cu = Number(
      (sess.accumulated_cu + (sess.burn_rate_cu_hr * 2.5) / 3600).toFixed(4),
    );
    sess.last_keepalive_at = nowIso;
    sess.updated_at = nowIso;

    const gpu = sess.resources?.gpus?.[0];
    if (gpu) {
      const wave = Math.sin(store.tickSeq * 0.9 + sess.uptime_sec * 0.05) * 0.035;
      const base =
        sess.accelerator === 'G4' ? 0.92 : sess.accelerator === 'A100' ? 0.87 : 0.79;
      gpu.gpu_utilization = Number(Math.max(0.62, Math.min(0.99, base + wave)).toFixed(2));
      if (gpu.temperature_c) {
        const tempBase = sess.accelerator === 'G4' ? 65 : sess.accelerator === 'A100' ? 59 : 56;
        gpu.temperature_c = Math.round(tempBase + wave * 35);
      }
      sess.resources!.sampled_at = nowIso;
    }

    broadcastWs({
      type: 'session_updated',
      timestamp: nowIso,
      payload: { ...sess },
    });
  }

  // 2. Advance Shards=4 experiment progress smoothly
  for (const exp of store.experiments) {
    if (exp.status !== 'RUNNING') continue;
    let totalSynced = 0;
    exp.shards = exp.shards.map((sh, idx) => {
      const target = sh.target_records || 125;
      let nextRecords = sh.synced_records + (store.tickSeq % 2 === idx % 2 ? 1 : 0);
      if (nextRecords >= target) {
        nextRecords = 76 + idx * 4;
      }
      const pct = Math.min(99, Math.round((nextRecords / target) * 100));
      totalSynced += nextRecords;
      const linkedSess = store.sessions.find((s) => s.id === sh.session_id);
      const utilPct = linkedSess?.resources?.gpus?.[0]?.gpu_utilization
        ? Math.round(linkedSess.resources.gpus[0].gpu_utilization * 100)
        : sh.sm_util_pct;
      return {
        ...sh,
        synced_records: nextRecords,
        progress_pct: pct,
        sm_util_pct: utilPct,
        cu_burned: Number((sh.cu_burned + 0.006).toFixed(3)),
        log_tail: `Step ${nextRecords}/${target} | mmlu_pro=${(0.812 + idx * 0.005).toFixed(4)} | synced shard_${sh.shard_id}.jsonl`,
      };
    });
    exp.synced_records_total = totalSynced;
    exp.cu_burned_so_far = Number((exp.cu_burned_so_far + 0.024).toFixed(2));
    exp.updated_at = nowIso;

    broadcastWs({
      type: 'experiment_updated',
      timestamp: nowIso,
      payload: { ...exp },
    });
  }

  // 3. Emit synthetic MCP router trace every 2nd tick (every 5s)
  if (store.tickSeq % 2 === 0) {
    const pick = SYNTHETIC_CALLERS[(store.tickSeq / 2) % SYNTHETIC_CALLERS.length];
    const sess = store.sessions.find((s) => s.id === pick.sessId) ?? store.sessions[0];
    const acc = store.accounts.find((a) => a.id === sess?.account_id) ?? store.accounts[0];
    const trace: ToolTraceEvent = {
      id: `tr_live_${Date.now().toString(36)}_${store.tickSeq}`,
      timestamp: nowIso,
      caller: pick.caller,
      api_key_prefix: 'ch_sk_live_9f8a4c',
      transport: pick.caller.includes('Codex') ? 'mcp-stdio' : 'mcp-http',
      tool_or_endpoint: pick.tool,
      session_id: sess?.id,
      account_id: acc?.id,
      account_email: acc?.email,
      requested_gpu: sess?.accelerator ?? 'G4',
      assigned_gpu: sess?.accelerator ?? 'G4',
      duration_ms: 185 + ((store.tickSeq * 37) % 240),
      cu_delta: pick.tool === 'execute_code' ? 0.0009 : 0,
      status: 'ok',
      status_code: 200,
    };
    store.traces = [trace, ...store.traces].slice(0, 80);
    broadcastWs({
      type: 'trace_event',
      timestamp: nowIso,
      payload: trace,
    });
  }

  broadcastWs({
    type: 'quota_updated',
    timestamp: nowIso,
    payload: computeDemoQuotaSummary(),
  });
  notifyStateChanged();
}

export function subscribeDemoWsEvents(listener: WsSubscriber): () => void {
  wsSubscribers.add(listener);
  if (tickerHandle === null && typeof window !== 'undefined') {
    tickerHandle = window.setInterval(runSyntheticTick, 2500);
  }
  return () => {
    wsSubscribers.delete(listener);
    if (wsSubscribers.size === 0 && tickerHandle !== null && typeof window !== 'undefined') {
      window.clearInterval(tickerHandle);
      tickerHandle = null;
    }
  };
}

/**
 * Public helper for LandingPage & Showcase interactive failover button:
 * Synthesizes an HTTP 429 rate-limit / stockout on primary account and records
 * a sub-second cross-account failover trace to ProPlus-Alex (A100 80GB).
 */
export function simulateDemoFailoverEvent(): ToolTraceEvent {
  const nowIso = new Date().toISOString();
  const trace: ToolTraceEvent = {
    id: `tr_failover_${Date.now().toString(36)}`,
    timestamp: nowIso,
    caller: 'Claude Code CLI',
    api_key_prefix: 'ch_sk_live_3d1b7e',
    transport: 'mcp-http',
    tool_or_endpoint: 'execute_code',
    session_id: 'sess_a100_80g',
    account_id: 'acc_proplus_02',
    account_email: 'alex.chen@stanford-ai.example',
    requested_gpu: 'G4',
    assigned_gpu: 'A100',
    fallback_trace:
      'Primary AI-Ultra-01 HTTP 429 RATE_LIMITED -> 38ms Smart Router Failover -> ProPlus-Alex (A100 80GB High-RAM) -> 200 OK (Zero Agent Interruption)',
    duration_ms: 38,
    cu_delta: 0.0012,
    status: 'fallback',
    status_code: 200,
  };
  store.traces = [trace, ...store.traces].slice(0, 80);
  broadcastWs({
    type: 'trace_event',
    timestamp: nowIso,
    payload: trace,
  });
  notifyStateChanged();
  return trace;
}

export function getDemoSnapshot() {
  return {
    summary: computeDemoQuotaSummary(),
    accounts: [...store.accounts],
    sessions: [...store.sessions],
    experiments: [...store.experiments],
    clients: [...store.clients],
    traces: [...store.traces],
  };
}

// ============================================================================
// Full In-Memory REST Endpoint Router (Zero Network Calls / Zero 404s!)
// ============================================================================

const delay = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

function parseJsonBody<B = Record<string, unknown>>(body: BodyInit | null | undefined): B {
  if (!body || typeof body !== 'string') return {} as B;
  try {
    return JSON.parse(body) as B;
  } catch {
    return {} as B;
  }
}

export async function handleDemoApiRequest<T>(
  rawPath: string,
  init: RequestInit = {},
): Promise<T> {
  const method = (init.method || 'GET').toUpperCase();
  const [pathname, searchStr = ''] = rawPath.split('?');
  const qs = new URLSearchParams(searchStr);

  // 1. Health & System Diagnostics
  if (pathname === '/api/v1/health') {
    const activeSessions = store.sessions.filter(
      (s) => s.status !== 'STOPPED' && s.status !== 'LOST',
    );
    const res: DaemonHealthStatus = {
      healthy: true,
      version: '1.1.0',
      deployment_mode: 'local',
      auth_required: false,
      uptime_sec: 86420 + store.tickSeq * 3,
      keepalive_loop_sec: 8,
      resource_poll_sec: 10,
      anti_zombie_armed: true,
      contributor_firewall_on: true,
      active_sessions: activeSessions.length,
      active_experiments: store.experiments.filter((e) => e.status === 'RUNNING').length,
      connected_ws_clients: Math.max(1, wsSubscribers.size),
      timestamp: new Date().toISOString(),
    };
    return res as unknown as T;
  }

  if (pathname === '/api/v1/hardware') {
    return DEFAULT_HARDWARE_CATALOG as unknown as T;
  }

  if (pathname === '/api/v1/ws-ticket') {
    return { ticket: 'demo_ws_ticket_local' } as unknown as T;
  }

  if (pathname === '/api/v1/system/selftest' && method === 'POST') {
    await delay(180);
    const activeCount = store.sessions.filter(
      (s) => s.status !== 'STOPPED' && s.status !== 'LOST',
    ).length;
    const connectedIdes = store.clients.filter((c) => c.injected).length;
    const selfTest: SystemSelfTestResult = {
      overall_ok: true,
      mode: 'demo',
      checked_at: new Date().toISOString(),
      discovered_local_count: 2,
      steps: [
        {
          id: 'sqlite_vault',
          title_zh: '本地数据库与 AES-256-GCM 保险箱',
          title_en: 'SQLite Store & AES-256-GCM Vault',
          status: 'ok',
          duration_ms: 14,
          summary: `AES-256-GCM memory vault verified · ${store.accounts.length} pooled accounts & ${store.proxies.length} proxy profiles healthy`,
          detail: 'PBKDF2-HMAC-SHA256 · Zero plaintext refresh tokens on disk',
        },
        {
          id: 'google_network',
          title_zh: 'Google 官方 API 网络连通性',
          title_en: 'Google Official API Network Reachability',
          status: 'ok',
          duration_ms: 52,
          summary:
            'Dual-channel colaboratory.googleapis.com/v1beta + colab.research.google.com/tun/m reachable (48ms)',
          detail: 'Per-account SOCKS5/HTTP sticky egress isolation active',
        },
        {
          id: 'mcp_router',
          title_zh: 'MCP 协议服务与智能路由引擎',
          title_en: 'MCP Server & Smart Router Engine',
          status: 'ok',
          duration_ms: 9,
          summary: `Streamable HTTP /mcp + SSE + stdio active · ${connectedIdes}/8 AI coding agents connected · ${activeCount} GPUs warm`,
          detail: `Policy: ${store.policy.default_strategy} · Anti-Zombie /proc/stat watchdog armed (8s)`,
        },
        {
          id: 'local_cli_scanner',
          title_zh: '本机 / WSL2 现有凭证只读扫描',
          title_en: 'Local & WSL2 CLI Credential Read-Only Scan',
          status: 'ok',
          duration_ms: 21,
          summary:
            'Read-only scan found 2 authorized colab-cli / gcloud ADC profiles ready for 1-click import',
          detail: '~/.config/colab/credentials.json (host) · ~/.config/gcloud/application_default_credentials.json (wsl2)',
        },
      ],
    };
    return selfTest as unknown as T;
  }

  // 2. Pool Summary & Accounts
  if (pathname === '/api/v1/pool/summary') {
    if (qs.get('refresh') === 'true') {
      await delay(140);
    }
    return computeDemoQuotaSummary() as unknown as T;
  }

  if (pathname === '/api/v1/accounts' && method === 'GET') {
    return store.accounts.map((a) => ({ ...a })) as unknown as T;
  }

  const accVerifyMatch = pathname.match(/^\/api\/v1\/accounts\/([^/]+)\/verify$/);
  if (accVerifyMatch && method === 'POST') {
    const accId = decodeURIComponent(accVerifyMatch[1]);
    const reqBody = parseJsonBody<AccountVerifyRequest>(init.body);
    const acc = store.accounts.find((a) => a.id === accId) ?? store.accounts[0];
    await delay(reqBody.run_kernel_smoke ? 380 : 160);
    const smokeGpu = reqBody.accelerator || 'G4';
    const result: AccountVerifyResult = {
      account_id: acc.id,
      email: acc.email,
      overall_ok: true,
      checked_at: new Date().toISOString(),
      total_duration_ms: reqBody.run_kernel_smoke ? 384 : 158,
      account: { ...acc, last_polled_at: new Date().toISOString() },
      kernel_stdout: reqBody.run_kernel_smoke
        ? `ColabHive Smoke OK on ${smokeGpu} | Python 3.11.11 | PyTorch 2.6.0+cu126 | CUDA Available: True\n`
        : undefined,
      steps: [
        {
          id: 'egress',
          title_zh: '网络与出口连通性',
          title_en: 'Network & Egress Connectivity',
          status: 'ok',
          duration_ms: acc.proxy?.latency_ms ?? 48,
          summary: `Pinned exit ${acc.proxy?.masked_url || 'Direct'} (${acc.proxy?.region || 'US-West'}) verified in ${acc.proxy?.latency_ms ?? 48}ms`,
        },
        {
          id: 'oauth',
          title_zh: 'OAuth2 登录凭证与自动续期',
          title_en: 'OAuth2 Credentials & Auto-Refresh',
          status: 'ok',
          duration_ms: 46,
          summary: acc.is_contributor
            ? 'Scoped colaboratory token valid · Contributor Drive Firewall enforced'
            : 'OAuth2 refresh token healthy · Auto-renewal active',
        },
        {
          id: 'quota',
          title_zh: 'Colab 官方实时额度与显卡权限',
          title_en: 'Colab Live Quota & GPU Entitlements',
          status: 'ok',
          duration_ms: 64,
          summary: `${acc.subscription_tier} · ${acc.paid_cu_balance.toFixed(2)} CU paid balance · ${acc.active_assignments}/${acc.max_concurrent_vms} active VM slots`,
        },
        {
          id: 'kernel_smoke',
          title_zh: '云端内核真机代码试跑',
          title_en: 'Cloud Kernel Live Smoke Execution',
          status: reqBody.run_kernel_smoke ? 'ok' : 'skipped',
          duration_ms: reqBody.run_kernel_smoke ? 226 : 0,
          summary: reqBody.run_kernel_smoke
            ? `Executed live Python kernel check on ${smokeGpu} and auto-released cleanly`
            : 'Read-only check completed (0.00 CU spent). Click "Run Live Smoke Test" to verify kernel execution.',
        },
      ],
    };
    return result as unknown as T;
  }

  const accRefreshMatch = pathname.match(/^\/api\/v1\/accounts\/([^/]+)\/refresh$/);
  if (accRefreshMatch && method === 'POST') {
    const accId = decodeURIComponent(accRefreshMatch[1]);
    await delay(150);
    const nowIso = new Date().toISOString();
    store.accounts = store.accounts.map((a) =>
      a.id === accId ? { ...a, last_polled_at: nowIso, updated_at: nowIso } : a,
    );
    const found = store.accounts.find((a) => a.id === accId) ?? store.accounts[0];
    notifyStateChanged();
    return { ...found } as unknown as T;
  }

  if (pathname === '/api/v1/accounts/oauth/start' && method === 'POST') {
    const payload = parseJsonBody<OAuthStartRequest>(init.body);
    const stateId = `ch_oauth_demo_${Math.random().toString(36).slice(2, 10)}`;
    const pending: OAuthPendingSession = {
      state: stateId,
      code_challenge: 'S256_demo_pkce',
      auth_url: `https://accounts.google.com/o/oauth2/v2/auth?client_id=colabhive-demo&state=${stateId}&response_type=code`,
      redirect_uri: 'http://localhost:54321/callback',
      mode: payload.mode || 'remote_copy_paste',
      scopes: ['openid', 'email', 'https://www.googleapis.com/auth/colaboratory'],
      alias: payload.alias,
      groups: payload.groups,
      is_contributor: Boolean(payload.is_contributor),
      expires_at: futureIso(0.25),
      created_at: new Date().toISOString(),
    };
    return pending as unknown as T;
  }

  if (pathname === '/api/v1/accounts/oauth/exchange' && method === 'POST') {
    await delay(220);
    const nowIso = new Date().toISOString();
    const newAcc: Account = {
      ...store.accounts[1],
      id: `acc_demo_${Date.now().toString(36)}`,
      email: 'researcher.node@stanford-ai.example',
      display_name: 'Researcher Pro+ Node',
      alias: 'ProPlus-New',
      active_assignments: 0,
      live_burn_rate_cu_hr: 0,
      health: 'HEALTHY',
      paid_cu_balance: 500,
      reference_cycle_cu: 500,
      schedulable_cu: 500,
      created_at: nowIso,
      updated_at: nowIso,
    };
    store.accounts = [...store.accounts, newAcc];
    notifyStateChanged();
    return newAcc as unknown as T;
  }

  const singleAccMatch = pathname.match(/^\/api\/v1\/accounts\/([^/]+)$/);
  if (singleAccMatch) {
    const accId = decodeURIComponent(singleAccMatch[1]);
    if (method === 'GET') {
      const found = store.accounts.find((a) => a.id === accId) ?? store.accounts[0];
      return { ...found } as unknown as T;
    }
    if (method === 'PATCH') {
      const patch = parseJsonBody<AccountPatch>(init.body);
      store.accounts = store.accounts.map((a) => {
        if (a.id !== accId) return a;
        const mergedGuardrails = patch.guardrails
          ? { ...a.guardrails, ...patch.guardrails }
          : a.guardrails;
        const reserve = a.is_contributor ? (mergedGuardrails?.min_reserve_cu ?? 0) : 0;
        const nextEnabled = patch.enabled !== undefined ? patch.enabled : a.enabled;
        return {
          ...a,
          ...patch,
          enabled: nextEnabled,
          guardrails: mergedGuardrails,
          schedulable_cu: Math.max(0, a.paid_cu_balance - reserve),
          health: !nextEnabled
            ? 'PAUSED_MANUAL'
            : a.active_assignments > 0
              ? 'ACTIVE_WORKING'
              : 'HEALTHY',
          updated_at: new Date().toISOString(),
        };
      });
      const updated = store.accounts.find((a) => a.id === accId) ?? store.accounts[0];
      notifyStateChanged();
      return { ...updated } as unknown as T;
    }
    if (method === 'DELETE') {
      store.accounts = store.accounts.filter((a) => a.id !== accId);
      notifyStateChanged();
      return undefined as unknown as T;
    }
  }

  // 3. Local Credential Scanner & Vault Portability
  if (pathname === '/api/v1/import/scan-local') {
    const discovered: DiscoveredLocalCredential[] = [
      {
        id: 'disc_host_colab_01',
        source_type: 'colab_token_json',
        source_path: '~/.config/colab/credentials.json',
        environment: 'host',
        email: 'alex.chen@stanford-ai.example',
        email_preview: 'ale***@stanford-ai.example',
        display_name: 'Alex Chen',
        already_in_pool: false,
        status: 'ready',
        token_preview: '1//0gX9d...7kQa',
        scopes: ['openid', 'email', 'colaboratory'],
      },
      {
        id: 'disc_wsl2_adc_02',
        source_type: 'gcloud_adc',
        source_path: '\\\\wsl.localhost\\Ubuntu\\home\\researcher\\.config\\gcloud\\application_default_credentials.json',
        environment: 'wsl2',
        email: 'lab-ultra@research.example',
        email_preview: 'lab***@research.example',
        display_name: 'Lab Ultra',
        source_label: 'WSL2 · Ubuntu (researcher)',
        already_in_pool: true,
        status: 'in_pool',
        token_preview: '1//0eR4m...2pLw',
        scopes: ['openid', 'email', 'colaboratory'],
      },
    ];
    return discovered as unknown as T;
  }

  if (pathname === '/api/v1/pool/import' && method === 'POST') {
    const _req = parseJsonBody<CredentialImportRequest>(init.body);
    void _req;
    await delay(180);
    return store.accounts.slice(0, 2) as unknown as T;
  }

  if (pathname === '/api/v1/pool/export' && method === 'POST') {
    const bundle: CredentialBundle = {
      schema: 'colabhive.vault.v1',
      version: '1.0.0',
      exported_at: new Date().toISOString(),
      encrypted: true,
      kdf: { alg: 'PBKDF2-HMAC-SHA256', iterations: 210000, salt_b64: 'ZGVtb19zYWx0X2NvbGFiaGl2ZQ==' },
      cipher: {
        alg: 'AES-256-GCM',
        nonce_b64: 'ZGVtb19ub25jZQ==',
        ciphertext_b64: 'ZGVtb19lbmNyeXB0ZWRfY2h2YXVsdF9wYXlsb2Fk',
      },
    };
    return bundle as unknown as T;
  }

  // 4. Proxies
  if (pathname === '/api/v1/proxies') {
    if (method === 'GET') {
      return store.proxies.map((p) => ({ ...p })) as unknown as T;
    }
    if (method === 'POST') {
      const body = parseJsonBody<Partial<ProxyConfig> & { name: string; url: string }>(init.body);
      const created: ProxyConfig = {
        id: `prx_${Date.now().toString(36)}`,
        name: body.name || 'Custom Proxy',
        protocol: body.url?.startsWith('socks5') ? 'socks5' : 'http',
        url: body.url,
        masked_url: body.url || 'socks5://198.51.*.*:1080',
        region: body.region || 'US-West',
        exit_ip: '198.51.100.88',
        latency_ms: 46,
        healthy: true,
        bound_accounts: 0,
        last_checked_at: new Date().toISOString(),
        created_at: new Date().toISOString(),
      };
      store.proxies = [...store.proxies, created];
      return created as unknown as T;
    }
  }

  if (pathname === '/api/v1/proxies/test' && method === 'POST') {
    await delay(140);
    const probe: ProxyProbeResult = {
      healthy: true,
      exit_ip: '38.142.90.14',
      region: 'US-California',
      latency_ms: 49,
      status_code: 200,
      checked_at: new Date().toISOString(),
    };
    return probe as unknown as T;
  }

  const proxyDelMatch = pathname.match(/^\/api\/v1\/proxies\/([^/]+)$/);
  if (proxyDelMatch && method === 'DELETE') {
    const id = decodeURIComponent(proxyDelMatch[1]);
    store.proxies = store.proxies.filter((p) => p.id !== id);
    return undefined as unknown as T;
  }

  // 6. Sessions & Remote Execution Workbench
  if (pathname === '/api/v1/sessions/sync' && method === 'POST') {
    await delay(180);
    const active = store.sessions.filter((s) => s.status !== 'STOPPED' && s.status !== 'LOST');
    return {
      sessions: store.sessions.map((s) => ({ ...s })),
      adopted_count: 0,
      active_count: active.length,
    } as unknown as T;
  }

  if (pathname === '/api/v1/sessions') {
    if (method === 'GET') {
      let list = store.sessions.filter((s) => s.status !== 'STOPPED' && s.status !== 'LOST');
      const accFilter = qs.get('account_id');
      const expFilter = qs.get('experiment_id');
      if (accFilter) list = list.filter((s) => s.account_id === accFilter);
      if (expFilter) list = list.filter((s) => s.experiment_id === expFilter);
      return list.map((s) => ({ ...s })) as unknown as T;
    }
    if (method === 'POST') {
      await delay(260);
      const req = parseJsonBody<SessionAllocRequest>(init.body);
      const spec =
        DEFAULT_HARDWARE_CATALOG.find((h) => h.accelerator === req.accelerator) ??
        DEFAULT_HARDWARE_CATALOG[0];
      const chosenAcc =
        store.accounts.find((a) => a.id === req.account_selector) ?? store.accounts[0];
      const nowIso = new Date().toISOString();
      const created: RuntimeSession = {
        id: `sess_${spec.accelerator.toLowerCase()}_${Date.now().toString(36)}`,
        name: req.name?.trim() || `${spec.accelerator.toLowerCase()}-interactive`,
        notebook_name: `${req.name?.trim() || `${spec.accelerator.toLowerCase()}-interactive`}.ipynb`,
        account_id: chosenAcc.id,
        account_email: chosenAcc.email,
        backend: 'public',
        endpoint: `gpu-${spec.accelerator.toLowerCase()}-${Math.random().toString(36).slice(2, 7)}`,
        notebook_hash: 'nbh_Q29sYWJIaXZlX0RlbW9fU2FuZGJveF9TZXNzaW9u',
        proxy_url: 'https://8080-gpu.colab.googleusercontent.com',
        variant: spec.variant,
        requested_accelerator: spec.accelerator,
        accelerator: spec.accelerator,
        fallback_applied: false,
        shape: req.high_mem || spec.always_high_mem ? 'HIGH_RAM' : 'STANDARD',
        status: 'IDLE',
        status_message: 'Warm Jupyter Kernel Ready (Demo Sandbox)',
        kernel_id: `k-${Math.random().toString(36).slice(2, 10)}`,
        burn_rate_cu_hr: spec.default_burn_rate_cu_hr,
        accumulated_cu: 0,
        colab_web_url: 'https://colab.research.google.com/',
        sticky_key: req.sticky_key,
        keepalive_healthy: true,
        last_keepalive_at: nowIso,
        zmq_reconnect_count: 0,
        uptime_sec: 1,
        created_at: nowIso,
        updated_at: nowIso,
        resources: {
          memory_total_bytes: 89_657_389_056,
          memory_free_bytes: 79_200_000_000,
          memory_used_bytes: 10_457_389_056,
          disk_total_bytes: 253_055_090_688,
          disk_used_bytes: 26_100_000_000,
          sampled_at: nowIso,
          gpus:
            spec.vram_gb > 0
              ? [
                  {
                    name: spec.display_name,
                    memory_used_bytes: 2_147_483_648,
                    memory_total_bytes: spec.vram_gb * 1_073_741_824,
                    gpu_utilization: 0.12,
                    memory_utilization: 0.04,
                    temperature_c: 45,
                    power_watts: 72,
                    ever_used: true,
                  },
                ]
              : [],
        },
        worker: {
          pid: 3290,
          proc_state: 'S',
          alive: true,
          zombie_detected: false,
          has_fatal_trace: false,
          log_offset: 0,
        },
      };
      store.sessions = [created, ...store.sessions];
      recomputeAccountAssignments();
      notifyStateChanged();
      return created as unknown as T;
    }
  }

  const sessExecMatch = pathname.match(/^\/api\/v1\/sessions\/([^/]+)\/exec$/);
  if (sessExecMatch && method === 'POST') {
    const sessId = decodeURIComponent(sessExecMatch[1]);
    const body = parseJsonBody<KernelExecRequest>(init.body);
    await delay(210);
    const sess = store.sessions.find((s) => s.id === sessId) ?? store.sessions[0];
    const gpuName =
      sess?.resources?.gpus?.[0]?.name || 'NVIDIA RTX PRO 6000 Blackwell (96GB)';
    const codeLower = (body.code || '').toLowerCase();
    let stdout = `[ColabHive Demo Kernel · ${sess?.accelerator || 'G4'}]\nExecuted on ${gpuName} (${sess?.account_email || 'lab-ultra@research.example'})\n`;
    if (codeLower.includes('nvidia-smi')) {
      stdout =
        `+-----------------------------------------------------------------------------------------+\n` +
        `| NVIDIA-SMI 560.35.03    Driver Version: 560.35.03    CUDA Version: 12.6                 |\n` +
        `| GPU  Name                                   Persistence-M | Bus-Id          Disp.A      |\n` +
        `|   0  ${gpuName.padEnd(38, ' ')} Off |   00000000:00:04.0 Off |\n` +
        `| 65C    P0             284W / 300W |   54210MiB / 98304MiB |     94%      Default        |\n` +
        `+-----------------------------------------------------------------------------------------+\n`;
    } else if (codeLower.includes('torch')) {
      stdout = `PyTorch 2.6.0+cu126 | CUDA Device: ${gpuName} | BF16: True | Free VRAM: 41.8 GB\n`;
    }
    const execRes: KernelExecResult = {
      session: sessId,
      executionId: `exec_${Date.now().toString(36)}`,
      executionCount: Math.floor(Math.random() * 10) + 3,
      status: 'ok',
      stdout,
      stderr: '',
      outputTruncated: false,
      durationMs: 214,
    };
    return execRes as unknown as T;
  }

  const sessCellsMatch = pathname.match(/^\/api\/v1\/sessions\/([^/]+)\/cells$/);
  if (sessCellsMatch) {
    const sessId = decodeURIComponent(sessCellsMatch[1]);
    const sess = store.sessions.find((s) => s.id === sessId) ?? store.sessions[0];
    if (method === 'GET') {
      return (sess?.cells ?? []) as unknown as T;
    }
    if (method === 'POST') {
      const mut = parseJsonBody<CellMutationRequest>(init.body);
      const newCell: NotebookCell = {
        cell_id: mut.cell_id || `cell_${Date.now().toString(36)}`,
        index: (sess?.cells?.length ?? 0) + 1,
        cell_type: mut.operation === 'add_text' ? 'markdown' : 'code',
        source: mut.content || '',
        execution_count: 3,
      };
      if (sess) {
        sess.cells = [...(sess.cells ?? []), newCell];
      }
      return { cell: newCell } as unknown as T;
    }
  }

  const sessFilesMatch = pathname.match(/^\/api\/v1\/sessions\/([^/]+)\/files$/);
  if (sessFilesMatch) {
    const sessId = decodeURIComponent(sessFilesMatch[1]);
    if (!store.filesBySession.has(sessId)) {
      store.filesBySession.set(
        sessId,
        DEMO_REMOTE_FILES.map((f) => ({ ...f })),
      );
    }
    const files = store.filesBySession.get(sessId)!;
    if (method === 'GET') {
      return files.map((f) => ({ ...f })) as unknown as T;
    }
    if (method === 'POST') {
      const body = parseJsonBody<{ remote_path?: string; content_base64?: string }>(init.body);
      const remotePath = body.remote_path || '/content/uploaded_file.py';
      const name = remotePath.split('/').filter(Boolean).pop() || 'uploaded_file.py';
      const bytes = body.content_base64 ? Math.round((body.content_base64.length * 3) / 4) : 1024;
      const existingIdx = files.findIndex((f) => f.path === remotePath);
      const entry: RemoteFileEntry = {
        name,
        path: remotePath,
        type: 'file',
        size_bytes: bytes,
        last_modified: new Date().toISOString(),
        synced: true,
      };
      if (existingIdx >= 0) files[existingIdx] = entry;
      else files.push(entry);
      return { uploaded: true, remote_path: remotePath, bytes } as unknown as T;
    }
    if (method === 'DELETE') {
      const rmPath = qs.get('path');
      if (rmPath) {
        store.filesBySession.set(
          sessId,
          files.filter((f) => f.path !== rmPath),
        );
      }
      return undefined as unknown as T;
    }
  }

  const sessInstallMatch = pathname.match(/^\/api\/v1\/sessions\/([^/]+)\/install$/);
  if (sessInstallMatch && method === 'POST') {
    const sessId = decodeURIComponent(sessInstallMatch[1]);
    const body = parseJsonBody<{ packages?: string[] }>(init.body);
    await delay(240);
    const pkgs = (body.packages ?? []).join(' ');
    return {
      session: sessId,
      executionId: `pip_${Date.now().toString(36)}`,
      executionCount: 1,
      status: 'ok',
      stdout: `Resolved & installed packages via uv pip: ${pkgs || 'requirements.txt'} in 1.18s\n`,
      stderr: '',
      outputTruncated: false,
      durationMs: 240,
    } as unknown as T;
  }

  const sessActionMatch = pathname.match(
    /^\/api\/v1\/sessions\/([^/]+)\/(open-url|restart-kernel|interrupt|hot-restart)$/,
  );
  if (sessActionMatch) {
    const [, rawId, act] = sessActionMatch;
    const sess = store.sessions.find((s) => s.id === decodeURIComponent(rawId));
    if (act === 'open-url') {
      return { url: sess?.colab_web_url || 'https://colab.research.google.com/' } as unknown as T;
    }
    if (act === 'interrupt') {
      return { interrupted: true } as unknown as T;
    }
    return { restarted: true } as unknown as T;
  }

  const singleSessMatch = pathname.match(/^\/api\/v1\/sessions\/([^/]+)$/);
  if (singleSessMatch) {
    const sessId = decodeURIComponent(singleSessMatch[1]);
    if (method === 'GET') {
      const found = store.sessions.find((s) => s.id === sessId) ?? store.sessions[0];
      return { ...found } as unknown as T;
    }
    if (method === 'DELETE') {
      await delay(140);
      store.sessions = store.sessions.map((s) =>
        s.id === sessId
          ? { ...s, status: 'STOPPED', stopped_at: new Date().toISOString(), burn_rate_cu_hr: 0 }
          : s,
      );
      store.savedWasteCu = Number((store.savedWasteCu + 4.8).toFixed(1));
      store.autoTeardownsCount += 1;
      recomputeAccountAssignments();
      notifyStateChanged();
      return undefined as unknown as T;
    }
  }

  // 7. Parallel Sharded Experiments
  if (pathname === '/api/v1/experiments') {
    if (method === 'GET') {
      return store.experiments.map((e) => ({ ...e, shards: e.shards.map((s) => ({ ...s })) })) as unknown as T;
    }
    if (method === 'POST') {
      await delay(280);
      const req = parseJsonBody<ExperimentLaunchRequest>(init.body);
      const spec =
        DEFAULT_HARDWARE_CATALOG.find((h) => h.accelerator === req.accelerator) ??
        DEFAULT_HARDWARE_CATALOG[0];
      const numShards = Math.max(1, Math.min(8, req.num_shards || 4));
      const nowIso = new Date().toISOString();
      const created: Experiment = {
        id: req.experiment_id || `exp_${Date.now().toString(36)}`,
        title: req.title || `${req.script_path} · ${numShards}× ${spec.accelerator}`,
        status: 'RUNNING',
        script_path: req.script_path || 'train_eval.py',
        accelerator: spec.accelerator,
        high_mem: req.high_mem,
        num_shards: numShards,
        strategy: req.strategy || 'waste-zero',
        completed_shards: 0,
        running_shards: numShards,
        failed_shards: 0,
        synced_records_total: 0,
        target_records_total: numShards * 100,
        aggregate_burn_rate_cu_hr: Number((spec.default_burn_rate_cu_hr * numShards).toFixed(2)),
        cu_burned_so_far: 0,
        auto_stop_on_complete: req.auto_stop_on_complete ?? true,
        artifacts: DEMO_RICH_OUTPUTS,
        created_at: nowIso,
        updated_at: nowIso,
        shards: Array.from({ length: numShards }, (_, idx) => {
          const acc = store.accounts[idx % Math.max(1, store.accounts.length - 1)] ?? store.accounts[0];
          return {
            shard_id: idx,
            session_id: `sess_exp_${idx}_${Date.now().toString(36)}`,
            session_name: `shard-${idx}-${spec.accelerator.toLowerCase()}`,
            account_id: acc.id,
            account_email: acc.email,
            accelerator: spec.accelerator,
            status: 'RUNNING',
            pid: 4100 + idx * 17,
            proc_state: 'R',
            zombie_detected: false,
            progress_pct: 12 + idx * 4,
            synced_records: 12 + idx * 4,
            target_records: 100,
            vram_used_gb: Number((spec.vram_gb * 0.55).toFixed(1)),
            vram_total_gb: spec.vram_gb,
            sm_util_pct: 89 + (idx % 6),
            temp_c: 62 + idx,
            cu_burned: 0.04,
            startup_self_check: 'PASSED',
            log_tail: `Shard #${idx} initialized on ${acc.alias || acc.email} · streaming JSONL`,
            started_at: nowIso,
          };
        }),
      };
      store.experiments = [created, ...store.experiments];
      notifyStateChanged();
      return created as unknown as T;
    }
  }

  const expTeardownMatch = pathname.match(/^\/api\/v1\/experiments\/([^/]+)\/teardown$/);
  if (expTeardownMatch && method === 'POST') {
    const expId = decodeURIComponent(expTeardownMatch[1]);
    await delay(180);
    store.experiments = store.experiments.map((e) =>
      e.id === expId
        ? { ...e, status: 'COMPLETED', running_shards: 0, completed_shards: e.num_shards }
        : e,
    );
    const res: ExperimentTeardownResult = {
      experiment_id: expId,
      all_vms_unassigned: true,
      active_vms_remaining: 0,
      merged_jsonl_path: `./outputs/${expId}/merged_results.jsonl`,
      merged_records_count: 500,
      total_cu_consumed: 13.42,
    };
    notifyStateChanged();
    return res as unknown as T;
  }

  const singleExpMatch = pathname.match(/^\/api\/v1\/experiments\/([^/]+)$/);
  if (singleExpMatch && method === 'GET') {
    const expId = decodeURIComponent(singleExpMatch[1]);
    const found = store.experiments.find((e) => e.id === expId) ?? store.experiments[0];
    return { ...found } as unknown as T;
  }

  // 8. Router Policy, Sticky Bindings, Traces, Keys & 8-IDE Agent Config Switches
  if (pathname === '/api/v1/router/policy') {
    if (method === 'PUT') {
      const next = parseJsonBody<RouterPolicy>(init.body);
      store.policy = { ...store.policy, ...next, updated_at: new Date().toISOString() };
      notifyStateChanged();
    }
    return { ...store.policy } as unknown as T;
  }

  if (pathname === '/api/v1/router/sticky' && method === 'GET') {
    return store.bindings.map((b) => ({ ...b })) as unknown as T;
  }

  const unbindMatch = pathname.match(/^\/api\/v1\/router\/sticky\/([^/]+)$/);
  if (unbindMatch && method === 'DELETE') {
    const key = decodeURIComponent(unbindMatch[1]);
    store.bindings = store.bindings.filter((b) => b.sticky_key !== key);
    notifyStateChanged();
    return undefined as unknown as T;
  }

  if (pathname === '/api/v1/router/traces') {
    const limit = Number(qs.get('limit') || 100);
    return store.traces.slice(0, limit).map((t) => ({ ...t })) as unknown as T;
  }

  if (pathname === '/api/v1/keys') {
    if (method === 'GET') {
      return store.apiKeys.map((k) => ({ ...k })) as unknown as T;
    }
    if (method === 'POST') {
      const req = parseJsonBody<CreateAPIKeyRequest>(init.body);
      const suffix = Math.random().toString(36).slice(2, 10);
      const rawKey = `ch_sk_demo_${suffix}_${Math.random().toString(36).slice(2, 12)}`;
      const created: AgentAPIKey = {
        id: `key_${Date.now().toString(36)}`,
        name: req.name || 'Scoped Agent Key',
        key_prefix: `ch_sk_demo_${suffix}`,
        raw_key: rawKey,
        allowed_groups: req.allowed_groups || ['research-core'],
        allowed_gpus: req.allowed_gpus || ['G4', 'A100', 'L4', 'T4'],
        default_strategy: req.default_strategy || 'waste-zero',
        max_budget_cu_total: req.max_budget_cu_total || 200,
        max_budget_cu_daily: req.max_budget_cu_daily || 50,
        used_cu_total: 0,
        used_cu_today: 0,
        max_concurrent_vms: req.max_concurrent_vms || 3,
        idle_auto_kill_min: req.idle_auto_kill_min || 20,
        enabled: true,
        created_at: new Date().toISOString(),
      };
      store.apiKeys = [created, ...store.apiKeys];
      notifyStateChanged();
      return created as unknown as T;
    }
  }

  const revokeKeyMatch = pathname.match(/^\/api\/v1\/keys\/([^/]+)$/);
  if (revokeKeyMatch && method === 'DELETE') {
    const keyId = decodeURIComponent(revokeKeyMatch[1]);
    store.apiKeys = store.apiKeys.filter((k) => k.id !== keyId);
    notifyStateChanged();
    return undefined as unknown as T;
  }

  if (pathname === '/api/v1/agents/clients' && method === 'GET') {
    const apiKey = qs.get('api_key') || '';
    if (apiKey) {
      const withKey = buildFallbackClientConfigs('http://127.0.0.1:7860', apiKey, 'http');
      const byId = new Map(withKey.map((c) => [c.client_id, c]));
      return store.clients.map((c) => ({
        ...c,
        snippet: byId.get(c.client_id)?.snippet ?? c.snippet,
        cli_command: byId.get(c.client_id)?.cli_command ?? c.cli_command,
      })) as unknown as T;
    }
    return store.clients.map((c) => ({ ...c })) as unknown as T;
  }

  if (pathname === '/api/v1/agents/inject' && method === 'POST') {
    await delay(110);
    const req = parseJsonBody<AgentInjectRequest>(init.body);
    const nowIso = new Date().toISOString();
    store.clients = store.clients.map((c) =>
      c.client_id === req.client_id
        ? {
            ...c,
            injected: true,
            detected: true,
            backup_exists: true,
            backup_path: `${c.config_path}.colabhive.bak`,
            transport: req.transport || c.transport,
            last_synced_at: nowIso,
          }
        : c,
    );
    saveInjectedAgents(
      new Set(store.clients.filter((c) => c.injected).map((c) => c.client_id)),
    );
    const updated =
      store.clients.find((c) => c.client_id === req.client_id) ?? store.clients[0];

    const trace: ToolTraceEvent = {
      id: `tr_inject_${Date.now().toString(36)}`,
      timestamp: nowIso,
      caller: updated.display_name,
      transport: updated.transport === 'stdio' ? 'mcp-stdio' : 'mcp-http',
      tool_or_endpoint: 'mcp_handshake_ready',
      account_id: 'acc_ultra_01',
      account_email: 'lab-ultra@research.example',
      assigned_gpu: 'G4',
      duration_ms: 14,
      cu_delta: 0,
      status: 'ok',
      status_code: 200,
    };
    store.traces = [trace, ...store.traces].slice(0, 80);
    broadcastWs({ type: 'trace_event', timestamp: nowIso, payload: trace });
    notifyStateChanged();
    return { ...updated } as unknown as T;
  }

  const removeAgentMatch = pathname.match(/^\/api\/v1\/agents\/inject\/([^/]+)$/);
  if (removeAgentMatch && method === 'DELETE') {
    await delay(90);
    const clientId = decodeURIComponent(removeAgentMatch[1]) as AgentClientID;
    store.clients = store.clients.map((c) =>
      c.client_id === clientId
        ? { ...c, injected: false, last_synced_at: new Date().toISOString() }
        : c,
    );
    saveInjectedAgents(
      new Set(store.clients.filter((c) => c.injected).map((c) => c.client_id)),
    );
    const updated =
      store.clients.find((c) => c.client_id === clientId) ?? store.clients[0];
    notifyStateChanged();
    return { ...updated } as unknown as T;
  }

  // 9. Native Desktop Tray State & Action Bridge
  if (pathname === '/api/v1/desktop/state') {
    return {
      native: false,
      bridge_active: true,
      desktop_shell: false,
      autostart: store.autostart,
      autostart_enabled: store.autostart,
      tray_visible: true,
      main_window_visible: true,
    } as unknown as T;
  }

  if (pathname === '/api/v1/desktop/action' && method === 'POST') {
    const body = parseJsonBody<{ action?: string; route?: string }>(init.body);
    if (body.action === 'toggle-autostart') {
      store.autostart = !store.autostart;
      notifyStateChanged();
      return {
        ok: true,
        handled: true,
        bridge_active: true,
        autostart: store.autostart,
        autostart_enabled: store.autostart,
      } as unknown as T;
    }
    return {
      ok: true,
      handled: false,
      bridge_active: false,
      autostart: store.autostart,
      autostart_enabled: store.autostart,
    } as unknown as T;
  }

  // 10. v1.1 Savings Receipt
  if (
    (pathname === '/api/v1/stats/savings-card' || pathname === '/api/v1/savings/receipt') &&
    method === 'GET'
  ) {
    const receipt: SavingsReceiptStats = {
      generated_at: new Date().toISOString(),
      pool_title: 'ColabHive 私人聚合算力池',
      total_accounts: store.accounts.length,
      providers_connected: ['Google Colab'],
      total_gpu_hours: 186.4,
      free_gpu_hours_harvested: 94.5,
      g4_equivalent_hours: 142.8,
      total_cu_burned: 412.6,
      saved_idle_waste_cu: store.savedWasteCu,
      waste_zero_efficiency_pct: 98.4,
      auto_resume_relays: 7,
      auto_teardowns: store.autoTeardownsCount,
      zombie_aborts: store.zombieAbortsCount,
      estimated_saved_usd: 268.5,
      estimated_saved_cny: 1933.2,
      top_gpus_used: ['G4 96GB', 'A100 80GB', 'T4 16GB'],
      rank_badge_en: '🏆 Free Compute Grandmaster',
      rank_badge_zh: '🏆 算力白嫖宗师',
      share_copy_en:
        'Pooled Google Colab accounts with ColabHive — 186.4h cloud GPU runtime (94.5h free tier, ~$269 at Vast.ai/RunPod avg rates) with zero idle waste! github.com/hexwander/colabhive',
      share_copy_zh:
        '我用开源神器 ColabHive 把多个 Google Colab 账号汇成了私人算力池：累计运行 186.4 小时云端显卡（含 94.5 小时免费卡），等效云端租卡价值约 ¥1,933（按 Vast.ai/RunPod 均价）！→ github.com/hexwander/colabhive',
    };
    return receipt as unknown as T;
  }

  // Graceful fallback so no unexpected route ever throws a 404 in Demo Sandbox Mode
  return {} as unknown as T;
}

export function triggerDemoFileDownload(remotePath: string, suggestedFilename?: string): void {
  if (typeof document === 'undefined') return;
  const filename =
    suggestedFilename || remotePath.split('/').filter(Boolean).pop() || 'colabhive_artifact.jsonl';
  const sampleContent = filename.endsWith('.jsonl')
    ? JSON.stringify({
        shard: 0,
        model: 'Qwen/Qwen3-27B',
        accelerator: 'G4-96GB',
        mmlu_pro_acc: 0.824,
        tokens_per_sec: 1142.8,
      }) + '\n'
    : `# ColabHive Demo Artifact: ${remotePath}\nprint("ColabHive Zero-Backend Sandbox")\n`;
  const blob = new Blob([sampleContent], { type: 'text/plain;charset=utf-8' });
  const blobUrl = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = blobUrl;
  link.download = filename;
  link.style.display = 'none';
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  window.setTimeout(() => URL.revokeObjectURL(blobUrl), 1000);
}
