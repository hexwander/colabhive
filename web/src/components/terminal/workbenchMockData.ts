import type {
  AcceleratorType,
  Account,
  Experiment,
  KernelExecResult,
  KernelRichOutput,
  RemoteFileEntry,
  RouteBinding,
  RouterPolicy,
  RoutingStrategy,
  RuntimeSession,
  SessionStreamChunk,
  ToolTraceEvent,
} from '../../types';

/**
 * Maps an AcceleratorType to its muted v2 tier hue token (small dots and labels only).
 */
export function getAcceleratorCssColor(acc?: AcceleratorType | string): string {
  switch (acc) {
    case 'G4':
      return 'var(--tier-g4)';
    case 'H100':
      return 'var(--tier-h100)';
    case 'A100':
      return 'var(--tier-a100)';
    case 'L4':
      return 'var(--tier-l4)';
    case 'V6E1':
    case 'V5E1':
      return 'var(--tier-tpu)';
    case 'T4':
    case 'CPU':
    default:
      return 'var(--tier-t4)';
  }
}

export function getAcceleratorGlow(acc?: AcceleratorType | string): string {
  switch (acc) {
    case 'G4':
      return 'var(--emerald-soft)';
    case 'H100':
      return 'var(--coral-soft)';
    case 'A100':
      return 'var(--sky-soft)';
    case 'L4':
      return 'var(--emerald-soft)';
    case 'V6E1':
    case 'V5E1':
      return 'var(--amber-soft)';
    default:
      return 'var(--neutral-soft)';
  }
}

/**
 * Normalizes GPU/SM utilization from either 0..1 (Colab `/api/colab/resources`) or 0..100.
 */
export function normalizeUtilPct(raw: number | undefined | null): number {
  if (raw === undefined || raw === null || Number.isNaN(raw) || raw <= 0) return 0;
  if (raw <= 1.0) return Math.min(100, Math.round(raw * 100));
  return Math.min(100, Math.round(raw));
}

/**
 * Formats seconds into `HH:MM:SS` or `MM:SS` tabular clock string.
 */
export function formatElapsedSec(sec: number | undefined | null): string {
  if (!sec || sec < 0 || !Number.isFinite(sec)) return '00:00:00';
  const total = Math.floor(sec);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
}

/**
 * Formats milliseconds into crisp `142 ms` or `2.48 s`.
 */
export function formatDurationMs(ms: number | undefined | null): string {
  if (ms === undefined || ms === null || Number.isNaN(ms)) return '0 ms';
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)} s`;
  const m = Math.floor(ms / 60_000);
  const s = ((ms % 60_000) / 1000).toFixed(1);
  return `${m}m ${s}s`;
}

/**
 * Generates crisp, self-contained SVG data-URIs representing scientific Matplotlib plots
 * when previewing out-of-band RichOutputs in demo mode or before PNG fetch.
 */
export function buildScientificPlotDataUri(
  variant: 'loss' | 'throughput' | 'vram',
  title: string,
): string {
  const svg =
    variant === 'loss'
      ? `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 300" width="560" height="300">
  <rect width="560" height="300" fill="#141413"/>
  <g stroke="#262624" stroke-width="1">
    <line x1="56" y1="42" x2="532" y2="42"/>
    <line x1="56" y1="96" x2="532" y2="96"/>
    <line x1="56" y1="150" x2="532" y2="150"/>
    <line x1="56" y1="204" x2="532" y2="204"/>
    <line x1="56" y1="252" x2="532" y2="252"/>
    <line x1="56" y1="32" x2="56" y2="252"/>
    <line x1="175" y1="32" x2="175" y2="252" stroke-dasharray="3 3"/>
    <line x1="294" y1="32" x2="294" y2="252" stroke-dasharray="3 3"/>
    <line x1="413" y1="32" x2="413" y2="252" stroke-dasharray="3 3"/>
  </g>
  <text x="56" y="24" fill="#f4f4f2" font-family="JetBrains Mono, monospace" font-size="12" font-weight="600">${title}</text>
  <text x="415" y="24" fill="#22c55e" font-family="JetBrains Mono, monospace" font-size="10">4x G4 Shards Merged</text>
  <path d="M56,56 C105,112 145,152 205,178 C275,208 360,226 528,238" fill="none" stroke="#22c55e" stroke-width="2.4"/>
  <path d="M56,66 C110,124 155,162 215,186 C285,214 370,231 528,242" fill="none" stroke="#f06449" stroke-width="1.8" stroke-dasharray="5 3"/>
  <circle cx="528" cy="238" r="4" fill="#22c55e"/>
  <circle cx="528" cy="242" r="3.5" fill="#f06449"/>
  <g fill="#a1a19a" font-family="JetBrains Mono, monospace" font-size="10">
    <text x="20" y="46">2.40</text>
    <text x="20" y="100">1.80</text>
    <text x="20" y="154">1.20</text>
    <text x="20" y="208">0.60</text>
    <text x="20" y="255">0.20</text>
    <text x="52" y="272">0</text>
    <text x="165" y="272">125</text>
    <text x="284" y="272">250</text>
    <text x="403" y="272">375</text>
    <text x="512" y="272">500</text>
  </g>
  <rect x="364" y="48" width="156" height="44" rx="4" fill="#1e1e1c" stroke="#333330"/>
  <line x1="374" y1="62" x2="394" y2="62" stroke="#22c55e" stroke-width="2.4"/>
  <text x="400" y="65" fill="#e7e7e4" font-family="JetBrains Mono, monospace" font-size="10">Train Loss (0.284)</text>
  <line x1="374" y1="79" x2="394" y2="79" stroke="#f06449" stroke-width="1.8" stroke-dasharray="4 2"/>
  <text x="400" y="82" fill="#e7e7e4" font-family="JetBrains Mono, monospace" font-size="10">Val MMLU (0.311)</text>
</svg>`
      : variant === 'throughput'
        ? `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 300" width="560" height="300">
  <rect width="560" height="300" fill="#141413"/>
  <text x="48" y="26" fill="#f4f4f2" font-family="JetBrains Mono, monospace" font-size="12" font-weight="600">${title}</text>
  <text x="392" y="26" fill="#22c55e" font-family="JetBrains Mono, monospace" font-size="10">Zero Extra CU Speedup: 3.94x</text>
  <g stroke="#262624" stroke-width="1">
    <line x1="48" y1="246" x2="530" y2="246"/>
    <line x1="48" y1="186" x2="530" y2="186" stroke-dasharray="3 3"/>
    <line x1="48" y1="126" x2="530" y2="126" stroke-dasharray="3 3"/>
    <line x1="48" y1="66" x2="530" y2="66" stroke-dasharray="3 3"/>
  </g>
  <rect x="84" y="196" width="68" height="50" fill="#71716b" opacity="0.85" rx="3"/>
  <rect x="192" y="148" width="68" height="98" fill="#3b82f6" opacity="0.88" rx="3"/>
  <rect x="300" y="72" width="68" height="174" fill="#22c55e" opacity="0.92" rx="3"/>
  <rect x="408" y="54" width="68" height="192" fill="#f06449" opacity="0.92" rx="3"/>
  <g fill="#f4f4f2" font-family="JetBrains Mono, monospace" font-size="10" text-anchor="middle">
    <text x="118" y="188">1,120 tok/s</text>
    <text x="226" y="140">2,210 tok/s</text>
    <text x="334" y="64">4,418 tok/s</text>
    <text x="442" y="46">5,890 tok/s</text>
  </g>
  <g fill="#a1a19a" font-family="JetBrains Mono, monospace" font-size="10" text-anchor="middle">
    <text x="118" y="266">1x G4</text>
    <text x="226" y="266">2x G4</text>
    <text x="334" y="266">4x G4 (Active)</text>
    <text x="442" y="266">4x H100</text>
  </g>
</svg>`
        : `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 560 300" width="560" height="300">
  <rect width="560" height="300" fill="#141413"/>
  <text x="48" y="26" fill="#f4f4f2" font-family="JetBrains Mono, monospace" font-size="12" font-weight="600">${title}</text>
  <g stroke="#262624" stroke-width="1">
    <line x1="48" y1="248" x2="532" y2="248"/>
    <line x1="48" y1="52" x2="532" y2="52" stroke="#f06449" stroke-dasharray="4 4"/>
  </g>
  <text x="430" y="46" fill="#f06449" font-family="JetBrains Mono, monospace" font-size="9">96.0 GB GDDR7 Ceiling</text>
  <path d="M48,244 L88,142 L132,138 L220,136 L310,134 L420,135 L532,133 L532,248 L48,248 Z" fill="rgba(34,197,94,0.15)"/>
  <path d="M48,244 L88,142 L132,138 L220,136 L310,134 L420,135 L532,133" fill="none" stroke="#22c55e" stroke-width="2.2"/>
  <g fill="#a1a19a" font-family="JetBrains Mono, monospace" font-size="10">
    <text x="54" y="126" fill="#4ade80">Allocated: 51.2 GB / 96.0 GB (BF16 KV-Cache Stable)</text>
    <text x="54" y="268">t=00:00</text>
    <text x="272" y="268">t=00:10</text>
    <text x="486" y="268">t=00:20</text>
  </g>
</svg>`;
  return `data:image/svg+xml;utf8,${encodeURIComponent(svg)}`;
}

// ============================================================================
// Demo / Simulation Fallback Snapshots (Used when backend is offline or previewed)
// ============================================================================

export const DEMO_RICH_OUTPUTS: KernelRichOutput[] = [
  {
    index: 0,
    mimeTypes: ['image/png', 'image/svg+xml'],
    title: 'qwen3_27b_mmlu_loss_curve.png',
    filePath: '/content/.colabhive_outputs/exp_qwen3_27b/loss_curve.png',
    sizeBytes: 68420,
    artifactUrl: buildScientificPlotDataUri('loss', 'Qwen3-27B Eval · Loss & MMLU Trajectory'),
  },
  {
    index: 1,
    mimeTypes: ['image/png', 'image/svg+xml'],
    title: 'shard_throughput_scaling.png',
    filePath: '/content/.colabhive_outputs/exp_qwen3_27b/shard_scaling.png',
    sizeBytes: 51290,
    artifactUrl: buildScientificPlotDataUri('throughput', 'Parallel Shard Throughput (tok/s)'),
  },
  {
    index: 2,
    mimeTypes: ['image/png', 'image/svg+xml'],
    title: 'blackwell_g4_vram_profile.png',
    filePath: '/content/.colabhive_outputs/exp_qwen3_27b/vram_profile.png',
    sizeBytes: 44810,
    artifactUrl: buildScientificPlotDataUri('vram', 'RTX PRO 6000 G4 · VRAM Residency'),
  },
];

export const DEMO_EXPERIMENTS: Experiment[] = [
  {
    id: 'exp_qwen3_27b_eval',
    title: 'Qwen3-27B MMLU-Pro Eval · 4× G4 96GB Shards',
    status: 'RUNNING',
    script_path: 'benchmarks/eval_qwen3_27b_shard.py',
    accelerator: 'G4',
    high_mem: true,
    num_shards: 4,
    strategy: 'waste-zero',
    completed_shards: 1,
    running_shards: 3,
    failed_shards: 0,
    synced_records_total: 368,
    target_records_total: 500,
    aggregate_burn_rate_cu_hr: 38.64,
    cu_burned_so_far: 12.68,
    estimated_total_cu: 17.22,
    auto_stop_on_complete: true,
    local_output_dir: './outputs/qwen3_27b_eval',
    merged_jsonl_path: './outputs/qwen3_27b_eval/merged_results.jsonl',
    artifacts: DEMO_RICH_OUTPUTS,
    created_at: new Date(Date.now() - 20 * 60_000).toISOString(),
    updated_at: new Date().toISOString(),
    shards: [
      {
        shard_id: 0,
        session_id: 'sess_g4_shard0',
        session_name: 'qwen3-27b-s0',
        account_id: 'acc_ultra_01',
        account_email: 'lab-ultra@research.example',
        accelerator: 'G4',
        status: 'RUNNING',
        pid: 4182,
        proc_state: 'R',
        zombie_detected: false,
        progress_pct: 74,
        synced_records: 93,
        target_records: 125,
        vram_used_gb: 51.2,
        vram_total_gb: 96.0,
        sm_util_pct: 94,
        temp_c: 67,
        cu_burned: 3.22,
        startup_self_check: 'PASSED',
        log_tail: 'Step 93/125 | acc=0.8172 | tok/s=1128.4 | saved shard_0.jsonl',
        started_at: new Date(Date.now() - 20 * 60_000).toISOString(),
      },
      {
        shard_id: 1,
        session_id: 'sess_g4_shard1',
        session_name: 'qwen3-27b-s1',
        account_id: 'acc_proplus_02',
        account_email: 'alex.chen@stanford-ai.example',
        accelerator: 'G4',
        status: 'RUNNING',
        pid: 3910,
        proc_state: 'R',
        zombie_detected: false,
        progress_pct: 71,
        synced_records: 89,
        target_records: 125,
        vram_used_gb: 50.8,
        vram_total_gb: 96.0,
        sm_util_pct: 92,
        temp_c: 65,
        cu_burned: 3.18,
        startup_self_check: 'PASSED',
        log_tail: 'Step 89/125 | acc=0.8090 | tok/s=1104.9 | saved shard_1.jsonl',
        started_at: new Date(Date.now() - 20 * 60_000).toISOString(),
      },
      {
        shard_id: 2,
        session_id: 'sess_g4_shard2',
        session_name: 'qwen3-27b-s2',
        account_id: 'acc_proplus_03',
        account_email: 'dev-pro@robotics.example',
        accelerator: 'G4',
        status: 'COMPLETED_AND_UNASSIGNED',
        pid: 4011,
        proc_state: 'S',
        zombie_detected: false,
        exit_code: 0,
        progress_pct: 100,
        synced_records: 125,
        target_records: 125,
        vram_used_gb: 0,
        vram_total_gb: 96.0,
        sm_util_pct: 0,
        temp_c: 38,
        cu_burned: 3.11,
        startup_self_check: 'PASSED',
        log_tail: 'DONE.flag written (125/125). Auto-unassigned VM -> 0.00 CU/hr.',
        started_at: new Date(Date.now() - 20 * 60_000).toISOString(),
        completed_at: new Date(Date.now() - 90_000).toISOString(),
      },
      {
        shard_id: 3,
        session_id: 'sess_g4_shard3',
        session_name: 'qwen3-27b-s3',
        account_id: 'acc_contrib_04',
        account_email: 'maya.lin@mit-vision.example',
        accelerator: 'G4',
        status: 'RUNNING',
        pid: 4409,
        proc_state: 'R',
        zombie_detected: false,
        progress_pct: 49,
        synced_records: 61,
        target_records: 125,
        vram_used_gb: 51.6,
        vram_total_gb: 96.0,
        sm_util_pct: 95,
        temp_c: 68,
        cu_burned: 3.17,
        startup_self_check: 'PASSED',
        log_tail: 'Step 61/125 | acc=0.8241 | reserve floor 50.0 CU intact',
        started_at: new Date(Date.now() - 19 * 60_000).toISOString(),
      },
    ],
  },
];

export const DEMO_SESSIONS: RuntimeSession[] = [
  {
    id: 'sess_g4_shard0',
    name: 'qwen3-27b-s0',
    account_id: 'acc_ultra_01',
    account_email: 'lab-ultra@research.example',
    backend: 'public',
    runtime_name: 'runtimes/r-blackwell-96g-01',
    endpoint: 'gpu-g4-s-9f21k8a',
    notebook_hash: 'nbh_Qm9sYWJIaXZlX1F3ZW4zXzI3Ql9TaGFyZDBfRzRfOTZH',
    proxy_url: 'https://8080-gpu-g4-s-9f21k8a.us-west1.colab.googleusercontent.com',
    variant: 'GPU',
    requested_accelerator: 'H100',
    accelerator: 'G4',
    fallback_applied: true,
    shape: 'HIGH_RAM',
    status: 'BUSY',
    status_message: 'Executing Shard #0 (PID 4182 · BF16 vLLM Worker)',
    kernel_id: '8f4c12a0-9e11-48bc-a4f1-002918471a00',
    jupyter_session_id: 'jsess_01',
    burn_rate_cu_hr: 9.66,
    accumulated_cu: 3.22,
    colab_web_url: 'https://colab.research.google.com/notebooks/empty.ipynb?authuser=0',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 0,
    sticky_key: 'mcp:antigravity:exp-main',
    api_key_id: 'key_prod_01',
    keepalive_healthy: true,
    last_keepalive_at: new Date(Date.now() - 3_000).toISOString(),
    zmq_reconnect_count: 1,
    uptime_sec: 1214,
    created_at: new Date(Date.now() - 1214_000).toISOString(),
    updated_at: new Date().toISOString(),
    resources: {
      memory_total_bytes: 89_657_389_056,
      memory_free_bytes: 61_203_947_520,
      memory_used_bytes: 28_453_441_536,
      disk_total_bytes: 253_055_090_688,
      disk_used_bytes: 78_383_153_152,
      hf_cache_bytes: 58_196_803_584,
      sampled_at: new Date().toISOString(),
      gpus: [
        {
          name: 'NVIDIA RTX PRO 6000 Blackwell Server Edition',
          memory_used_bytes: 54_975_581_388,
          memory_total_bytes: 103_079_215_104,
          gpu_utilization: 0.94,
          memory_utilization: 0.533,
          temperature_c: 67,
          power_watts: 286,
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
      log_offset: 148290,
      last_checked_at: new Date(Date.now() - 2_000).toISOString(),
    },
    timeline: [
      {
        id: 'st-1',
        label: 'Assign Runtime (H100 HTTP 400 -> Auto-Fallback G4 96GB)',
        status: 'completed',
        duration_ms: 4210,
        started_at: new Date(Date.now() - 1210_000).toISOString(),
        detail: 'acc_ultra_01 via socks5://us-lax (54ms)',
      },
      {
        id: 'st-2',
        label: 'uv pip install vllm==0.8.4 transformers accelerate datasets',
        status: 'completed',
        duration_ms: 14820,
        started_at: new Date(Date.now() - 1205_000).toISOString(),
        detail: 'Resolved 42 wheels in 14.8s',
      },
      {
        id: 'st-3',
        label: 'Hydrate /root/.cache/huggingface (Qwen/Qwen3-27B BF16)',
        status: 'completed',
        duration_ms: 38400,
        started_at: new Date(Date.now() - 1190_000).toISOString(),
        detail: '54.2 GB cached on NVMe',
      },
      {
        id: 'st-4',
        label: 'Shard #0 Worker Loop + /proc/4182/stat Anti-Zombie Guard',
        status: 'running',
        duration_ms: 1142000,
        started_at: new Date(Date.now() - 1150_000).toISOString(),
        detail: '93 / 125 samples synced',
      },
      {
        id: 'st-5',
        label: 'Merge shard_0.jsonl & Auto-Unassign VM',
        status: 'pending',
        duration_ms: 0,
        started_at: new Date().toISOString(),
      },
    ],
    cells: [
      {
        cell_id: 'cell_01',
        index: 0,
        cell_type: 'code',
        execution_count: 1,
        source: '!nvidia-smi --query-gpu=name,memory.used,memory.total,utilization.gpu,temperature.gpu --format=csv',
      },
      {
        cell_id: 'cell_02',
        index: 1,
        cell_type: 'code',
        execution_count: 2,
        source:
          'import torch\nprint("CUDA Device:", torch.cuda.get_device_name(0))\nprint(f"BF16 Supported: {torch.cuda.is_bf16_supported()} | VRAM: {torch.cuda.get_device_properties(0).total_memory / 1e9:.1f} GB")',
        outputs: [DEMO_RICH_OUTPUTS[0]],
      },
    ],
  },
  {
    id: 'sess_g4_shard1',
    name: 'qwen3-27b-s1',
    account_id: 'acc_proplus_02',
    account_email: 'alex.chen@stanford-ai.example',
    backend: 'tunnel',
    endpoint: 'gpu-g4-s-4b88c1e',
    notebook_hash: 'nbh_Qm9sYWJIaXZlX1F3ZW4zXzI3Ql9TaGFyZDFfRzRfOTZH',
    proxy_url: 'https://8080-gpu-g4-s-4b88c1e.us-central1.colab.googleusercontent.com',
    variant: 'GPU',
    requested_accelerator: 'G4',
    accelerator: 'G4',
    fallback_applied: false,
    shape: 'HIGH_RAM',
    status: 'BUSY',
    kernel_id: '3a9d7712-112c-4f90-9c2a-77819200b112',
    burn_rate_cu_hr: 9.66,
    accumulated_cu: 3.18,
    colab_web_url: 'https://colab.research.google.com/notebooks/empty.ipynb?authuser=0',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 1,
    keepalive_healthy: true,
    last_keepalive_at: new Date(Date.now() - 4_000).toISOString(),
    zmq_reconnect_count: 0,
    uptime_sec: 1198,
    created_at: new Date(Date.now() - 1198_000).toISOString(),
    updated_at: new Date().toISOString(),
    resources: {
      memory_total_bytes: 89_657_389_056,
      memory_free_bytes: 63_100_000_000,
      memory_used_bytes: 26_557_389_056,
      disk_total_bytes: 253_055_090_688,
      disk_used_bytes: 76_100_000_000,
      hf_cache_bytes: 58_196_803_584,
      sampled_at: new Date().toISOString(),
      gpus: [
        {
          name: 'NVIDIA RTX PRO 6000 Blackwell Server Edition',
          memory_used_bytes: 54_546_084_659,
          memory_total_bytes: 103_079_215_104,
          gpu_utilization: 0.92,
          memory_utilization: 0.529,
          temperature_c: 65,
          power_watts: 279,
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
      log_offset: 139012,
      last_checked_at: new Date(Date.now() - 4_000).toISOString(),
    },
  },
  {
    id: 'sess_a100_interactive',
    name: 'claude-triton-kernel-dev',
    account_id: 'acc_proplus_03',
    account_email: 'dev-pro@robotics.example',
    backend: 'public',
    runtime_name: 'runtimes/r-ampere-a100-09',
    endpoint: 'gpu-a100-s-77d19f',
    notebook_hash: 'nbh_Q2xhdWRlQ29kZV9Ucml0b25fQTEwMF9JbnRlcmFjdGl2ZQ',
    proxy_url: 'https://8080-gpu-a100-s-77d19f.us-east1.colab.googleusercontent.com',
    variant: 'GPU',
    requested_accelerator: 'A100',
    accelerator: 'A100',
    fallback_applied: false,
    shape: 'HIGH_RAM',
    status: 'IDLE',
    status_message: 'Warm Jupyter Kernel Ready (Triton 3.2 + FlashAttn-2)',
    kernel_id: 'c4209e11-6a18-4209-b811-902184ab7712',
    burn_rate_cu_hr: 8.88,
    accumulated_cu: 4.74,
    colab_web_url: 'https://colab.research.google.com/notebooks/empty.ipynb?authuser=0',
    sticky_key: 'mcp:claude-code:triton-bench',
    api_key_id: 'key_dev_02',
    keepalive_healthy: true,
    last_keepalive_at: new Date(Date.now() - 2_000).toISOString(),
    zmq_reconnect_count: 0,
    uptime_sec: 1922,
    created_at: new Date(Date.now() - 1922_000).toISOString(),
    updated_at: new Date().toISOString(),
    resources: {
      memory_total_bytes: 89_657_389_056,
      memory_free_bytes: 71_400_000_000,
      memory_used_bytes: 18_257_389_056,
      disk_total_bytes: 253_055_090_688,
      disk_used_bytes: 44_200_000_000,
      hf_cache_bytes: 29_400_000_000,
      sampled_at: new Date().toISOString(),
      gpus: [
        {
          name: 'NVIDIA A100-SXM4-40GB',
          memory_used_bytes: 19_864_223_744,
          memory_total_bytes: 42_949_672_960,
          gpu_utilization: 0.18,
          memory_utilization: 0.462,
          temperature_c: 52,
          power_watts: 118,
          ever_used: true,
        },
      ],
    },
    worker: {
      pid: 2814,
      proc_state: 'S',
      alive: true,
      zombie_detected: false,
      has_fatal_trace: false,
      log_offset: 38120,
      last_checked_at: new Date(Date.now() - 2_000).toISOString(),
    },
    timeline: [
      {
        id: 'a-1',
        label: 'Allocate NVIDIA A100-SXM4-40GB (--high-mem)',
        status: 'completed',
        duration_ms: 3420,
        started_at: new Date(Date.now() - 1920_000).toISOString(),
      },
      {
        id: 'a-2',
        label: 'Compile custom Triton fused RMSNorm + SwiGLU kernel',
        status: 'completed',
        duration_ms: 2180,
        started_at: new Date(Date.now() - 640_000).toISOString(),
      },
    ],
  },
];

export const DEMO_STREAM_CHUNKS: SessionStreamChunk[] = [
  {
    session_id: 'sess_g4_shard0',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 0,
    stream: 'colab',
    text: '\x1b[36m[colabhive]\x1b[0m Provisioning requested=H100 -> HTTP 400 STOCKOUT on acc_ultra_01 -> \x1b[33mAuto-Fallback G4 (RTX PRO 6000 96GB)\x1b[0m OK in 4.21s',
    timestamp: new Date(Date.now() - 1200_000).toISOString(),
  },
  {
    session_id: 'sess_g4_shard0',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 0,
    stream: 'colab',
    text: '\x1b[32m[keepalive]\x1b[0m Attached ZMQ iopub/shell tunnel · 8s heartbeat armed · /proc/4182/stat watchdog = R (Healthy)',
    timestamp: new Date(Date.now() - 1190_000).toISOString(),
  },
  {
    session_id: 'sess_g4_shard0',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 0,
    stream: 'stdout',
    text: '\x1b[1m[vLLM 0.8.4]\x1b[0m Loaded Qwen/Qwen3-27B in BF16 (51.20 GB / 96.00 GB GDDR7) · KV Cache headroom: 38.4 GB',
    timestamp: new Date(Date.now() - 1140_000).toISOString(),
  },
  {
    session_id: 'sess_g4_shard1',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 1,
    stream: 'stdout',
    text: '\x1b[32m[Shard #1]\x1b[0m Step 89/125 | mmlu_pro_acc=0.8090 | latency=884ms/req | synced -> /content/shard_1.jsonl',
    timestamp: new Date(Date.now() - 42_000).toISOString(),
  },
  {
    session_id: 'sess_g4_shard2',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 2,
    stream: 'colab',
    text: '\x1b[32m[Shard #2 DONE]\x1b[0m Verified 125/125 JSONL records -> Downloaded checkpoint -> \x1b[33munassign(gpu-g4-s-2c19)\x1b[0m -> Burn rate now 0.00 CU/hr',
    timestamp: new Date(Date.now() - 30_000).toISOString(),
  },
  {
    session_id: 'sess_g4_shard3',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 3,
    stream: 'stderr',
    text: '\x1b[33m[guardrail]\x1b[0m Contributor account alice***@stanford.edu balance=118.4 CU (Personal Reserve Floor: 50.0 CU · Safe Margin: +68.4 CU)',
    timestamp: new Date(Date.now() - 18_000).toISOString(),
  },
  {
    session_id: 'sess_g4_shard0',
    experiment_id: 'exp_qwen3_27b_eval',
    shard_index: 0,
    stream: 'stdout',
    text: '\x1b[36mEpoch 1/1\x1b[0m | Step 93/125 | loss=\x1b[32m0.2841\x1b[0m | mmlu_pro=\x1b[1m81.72%\x1b[0m | throughput=1,128.4 tok/s | Saved /content/shard_0.jsonl',
    timestamp: new Date(Date.now() - 4_000).toISOString(),
  },
  {
    session_id: 'sess_a100_interactive',
    stream: 'stdout',
    text: '\x1b[32m[Triton Benchmark]\x1b[0m Fused RMSNorm+SwiGLU fwd+bwd: 1.48 TB/s HBM2e bandwidth (1.84x faster than eager PyTorch)',
    timestamp: new Date(Date.now() - 9_000).toISOString(),
  },
];

export const DEMO_EXEC_HISTORY: KernelExecResult[] = [
  {
    session: 'sess_g4_shard0',
    executionId: 'exec_demo_01',
    executionCount: 1,
    status: 'ok',
    durationMs: 184,
    outputTruncated: false,
    stdout:
      'name, memory.used [MiB], memory.total [MiB], utilization.gpu [%], temperature.gpu\nNVIDIA RTX PRO 6000 Blackwell Server Edition, 52429 MiB, 98304 MiB, 94 %, 67\n',
    stderr: '',
  },
  {
    session: 'sess_g4_shard0',
    executionId: 'exec_demo_02',
    executionCount: 2,
    status: 'ok',
    durationMs: 642,
    outputTruncated: false,
    stdout:
      'CUDA Device: NVIDIA RTX PRO 6000 Blackwell Server Edition\nBF16 Supported: True | VRAM: 96.0 GB | HF Cache Warm: 54.2 GB\n',
    stderr: '',
    result: "{'shard': 0, 'synced_records': 93, 'mmlu_pro_acc': 0.8172}",
    richOutputs: [DEMO_RICH_OUTPUTS[0], DEMO_RICH_OUTPUTS[1]],
  },
];

export const DEMO_REMOTE_FILES: RemoteFileEntry[] = [
  {
    name: '.colabhive_outputs',
    path: '/content/.colabhive_outputs',
    type: 'directory',
    size_bytes: 4096,
    last_modified: new Date(Date.now() - 120_000).toISOString(),
    synced: true,
  },
  {
    name: 'checkpoints',
    path: '/content/checkpoints',
    type: 'directory',
    size_bytes: 4096,
    last_modified: new Date(Date.now() - 300_000).toISOString(),
    synced: true,
  },
  {
    name: 'shard_0.jsonl',
    path: '/content/shard_0.jsonl',
    type: 'file',
    size_bytes: 2_481_920,
    last_modified: new Date(Date.now() - 6_000).toISOString(),
    synced: true,
  },
  {
    name: 'eval_qwen3_27b_shard.py',
    path: '/content/eval_qwen3_27b_shard.py',
    type: 'file',
    size_bytes: 14_820,
    last_modified: new Date(Date.now() - 1200_000).toISOString(),
    synced: true,
  },
  {
    name: 'worker.log',
    path: '/content/worker.log',
    type: 'file',
    size_bytes: 152_490,
    last_modified: new Date(Date.now() - 2_000).toISOString(),
    synced: false,
  },
  {
    name: 'metrics_summary.json',
    path: '/content/metrics_summary.json',
    type: 'file',
    size_bytes: 4_812,
    last_modified: new Date(Date.now() - 15_000).toISOString(),
    synced: true,
  },
];

export const DEMO_ACCOUNTS: Account[] = [
  {
    id: 'acc_ultra_01',
    email: 'lab-ultra@research.example',
    alias: 'Primary Ultra #1',
    subscription_tier: 'AI_ULTRA',
    health: 'ACTIVE_WORKING',
    enabled: true,
    priority: 100,
    groups: ['research-core', 'high-vram'],
    tags: ['flagship'],
    max_concurrent_vms: 4,
    active_assignments: 1,
    sticky_agent_count: 1,
    paid_cu_balance: 386.4,
    reference_cycle_cu: 500,
    schedulable_cu: 386.4,
    live_burn_rate_cu_hr: 9.66,
    expiring_soon_cu: 42.0,
    earliest_batch_expires_at: new Date(Date.now() + 9 * 86400_000).toISOString(),
    gpu_entitlements: {
      G4: { accelerator: 'G4', status: 'AVAILABLE', calibrated_burn_rate: 9.66, calibration_source: 'live' },
      H100: { accelerator: 'H100', status: 'TIER_RESTRICTED', reason: 'HTTP 400 Stockout' },
      A100: { accelerator: 'A100', status: 'AVAILABLE', calibrated_burn_rate: 8.88, calibration_source: 'live' },
    },
    public_api_supported: true,
    tunnel_api_supported: true,
    is_contributor: false,
    proxy: {
      id: 'prx_lax_01',
      name: 'US-LAX Residential #1',
      protocol: 'socks5',
      masked_url: 'socks5://38.104.*.*:1080',
      region: 'US-West',
      latency_ms: 54,
      healthy: true,
      bound_accounts: 1,
      created_at: new Date().toISOString(),
    },
    guardrails: {
      min_reserve_cu: 0,
      donated_cu_used_total: 0,
      donated_cu_used_today: 0,
      allow_paid_cu_burn: true,
      allowed_gpus: ['G4', 'H100', 'A100', 'L4', 'T4'],
      max_concurrent_vms: 4,
      block_drive_propagation: false,
      mask_email_in_dashboard: false,
      paused: false,
    },
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
  {
    id: 'acc_proplus_02',
    email: 'alex.chen@stanford-ai.example',
    alias: 'Lab Pro+ #2',
    subscription_tier: 'PRO_PLUS',
    health: 'ACTIVE_WORKING',
    enabled: true,
    priority: 90,
    groups: ['research-core'],
    tags: [],
    max_concurrent_vms: 3,
    active_assignments: 1,
    sticky_agent_count: 1,
    paid_cu_balance: 442.8,
    reference_cycle_cu: 500,
    schedulable_cu: 442.8,
    live_burn_rate_cu_hr: 9.66,
    expiring_soon_cu: 0,
    gpu_entitlements: {
      G4: { accelerator: 'G4', status: 'AVAILABLE' },
      A100: { accelerator: 'A100', status: 'AVAILABLE' },
    },
    public_api_supported: true,
    tunnel_api_supported: true,
    is_contributor: false,
    proxy: {
      id: 'prx_ord_02',
      name: 'US-ORD Static #2',
      protocol: 'socks5',
      masked_url: 'socks5://45.79.*.*:1080',
      region: 'US-Central',
      latency_ms: 61,
      healthy: true,
      bound_accounts: 1,
      created_at: new Date().toISOString(),
    },
    guardrails: {
      min_reserve_cu: 0,
      donated_cu_used_total: 0,
      donated_cu_used_today: 0,
      allow_paid_cu_burn: true,
      allowed_gpus: ['G4', 'A100', 'L4', 'T4'],
      max_concurrent_vms: 3,
      block_drive_propagation: false,
      mask_email_in_dashboard: false,
      paused: false,
    },
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
  {
    id: 'acc_proplus_03',
    email: 'dev-pro@robotics.example',
    alias: 'Lab Pro+ #3',
    subscription_tier: 'PRO_PLUS',
    health: 'ACTIVE_WORKING',
    enabled: true,
    priority: 85,
    groups: ['research-core', 'triton-dev'],
    tags: [],
    max_concurrent_vms: 3,
    active_assignments: 1,
    sticky_agent_count: 1,
    paid_cu_balance: 318.5,
    reference_cycle_cu: 500,
    schedulable_cu: 318.5,
    live_burn_rate_cu_hr: 8.88,
    expiring_soon_cu: 0,
    gpu_entitlements: {
      G4: { accelerator: 'G4', status: 'AVAILABLE' },
      A100: { accelerator: 'A100', status: 'AVAILABLE' },
      L4: { accelerator: 'L4', status: 'AVAILABLE' },
    },
    public_api_supported: true,
    tunnel_api_supported: true,
    is_contributor: false,
    guardrails: {
      min_reserve_cu: 0,
      donated_cu_used_total: 0,
      donated_cu_used_today: 0,
      allow_paid_cu_burn: true,
      allowed_gpus: ['G4', 'A100', 'L4', 'T4'],
      max_concurrent_vms: 3,
      block_drive_propagation: false,
      mask_email_in_dashboard: false,
      paused: false,
    },
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
  {
    id: 'acc_contrib_04',
    email: 'maya.lin@mit-vision.example',
    alias: 'Contributor · Alice',
    subscription_tier: 'PRO_PLUS',
    health: 'ACTIVE_WORKING',
    enabled: true,
    priority: 75,
    groups: ['lab-contributors'],
    tags: ['contributed'],
    max_concurrent_vms: 1,
    active_assignments: 1,
    sticky_agent_count: 1,
    paid_cu_balance: 168.4,
    reference_cycle_cu: 500,
    schedulable_cu: 118.4,
    live_burn_rate_cu_hr: 9.66,
    expiring_soon_cu: 95.0,
    gpu_entitlements: {
      G4: { accelerator: 'G4', status: 'AVAILABLE' },
      A100: { accelerator: 'A100', status: 'AVAILABLE' },
    },
    public_api_supported: false,
    tunnel_api_supported: true,
    is_contributor: true,
    guardrails: {
      min_reserve_cu: 50.0,
      max_donated_cu_total: 150.0,
      max_donated_cu_daily: 40.0,
      donated_cu_used_total: 31.6,
      donated_cu_used_today: 8.4,
      allow_paid_cu_burn: true,
      allowed_gpus: ['G4', 'A100', 'L4', 'T4'],
      max_concurrent_vms: 1,
      block_drive_propagation: true,
      mask_email_in_dashboard: false,
      paused: false,
    },
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
];

export const DEMO_ROUTER_POLICY: RouterPolicy = {
  default_strategy: 'waste-zero',
  sticky_session_enabled: true,
  sticky_idle_ttl_sec: 1800,
  auto_fallback_enabled: true,
  fallback_chain: ['H100', 'G4', 'A100', 'L4', 'T4'],
  large_vram_fallback_chain: ['H100', 'G4'],
  auto_release_on_zombie: true,
  keepalive_interval_sec: 8,
  resource_poll_interval_sec: 10,
  updated_at: new Date().toISOString(),
};

export const DEMO_STICKY_BINDINGS: RouteBinding[] = [
  {
    sticky_key: 'mcp:antigravity:exp-main',
    client_name: 'Antigravity IDE',
    api_key_id: 'key_prod_01',
    api_key_prefix: 'ch_sk_live_9f8a2c',
    strategy: 'waste-zero',
    session_id: 'sess_g4_shard0',
    session_name: 'qwen3-27b-s0',
    kernel_id: '8f4c12a0-9e11',
    account_id: 'acc_ultra_01',
    account_email: 'lab-ultra@research.example',
    proxy_summary: 'socks5://38.104.22.9:1080',
    accelerator: 'G4',
    active_calls: 2,
    idle_ttl_remaining_sec: 1684,
    pinned_at: new Date(Date.now() - 1200_000).toISOString(),
    last_active_at: new Date(Date.now() - 4_000).toISOString(),
  },
  {
    sticky_key: 'mcp:claude-code:triton-bench',
    client_name: 'Claude Code CLI',
    api_key_id: 'key_dev_02',
    api_key_prefix: 'ch_sk_live_4d11e0',
    strategy: 'sticky-session',
    session_id: 'sess_a100_interactive',
    session_name: 'claude-triton-kernel-dev',
    kernel_id: 'c4209e11-6a18',
    account_id: 'acc_proplus_03',
    account_email: 'dev-pro@robotics.example',
    proxy_summary: 'socks5://104.28.14.2:1080',
    accelerator: 'A100',
    active_calls: 1,
    idle_ttl_remaining_sec: 1420,
    pinned_at: new Date(Date.now() - 1900_000).toISOString(),
    last_active_at: new Date(Date.now() - 9_000).toISOString(),
  },
  {
    sticky_key: 'sdk:python:shard-worker-1',
    client_name: 'Python HivePool SDK',
    api_key_id: 'key_prod_01',
    api_key_prefix: 'ch_sk_live_9f8a2c',
    strategy: 'round-robin',
    session_id: 'sess_g4_shard1',
    session_name: 'qwen3-27b-s1',
    kernel_id: '3a9d7712-112c',
    account_id: 'acc_proplus_02',
    account_email: 'alex.chen@stanford-ai.example',
    proxy_summary: 'socks5://45.79.88.12:1080',
    accelerator: 'G4',
    active_calls: 1,
    idle_ttl_remaining_sec: 1742,
    pinned_at: new Date(Date.now() - 1180_000).toISOString(),
    last_active_at: new Date(Date.now() - 12_000).toISOString(),
  },
  {
    sticky_key: 'cli:colabhive:shard-worker-3',
    client_name: 'colabhive CLI',
    api_key_id: 'key_prod_01',
    api_key_prefix: 'ch_sk_live_9f8a2c',
    strategy: 'waste-zero',
    session_id: 'sess_g4_shard3',
    session_name: 'qwen3-27b-s3',
    kernel_id: '7b019e44-55c1',
    account_id: 'acc_contrib_04',
    account_email: 'maya.lin@mit-vision.example',
    proxy_summary: 'socks5://64.225.19.8:1080',
    accelerator: 'G4',
    active_calls: 1,
    idle_ttl_remaining_sec: 1590,
    pinned_at: new Date(Date.now() - 1140_000).toISOString(),
    last_active_at: new Date(Date.now() - 18_000).toISOString(),
  },
];

export const DEMO_TRACES: ToolTraceEvent[] = [
  {
    id: 'tr_01',
    timestamp: new Date(Date.now() - 2_800).toISOString(),
    caller: 'Antigravity IDE',
    api_key_prefix: 'ch_sk_live_9f8a2c',
    transport: 'mcp-http',
    tool_or_endpoint: 'execute_code',
    session_id: 'sess_g4_shard0',
    account_id: 'acc_ultra_01',
    account_email: 'lab-ultra@research.example',
    requested_gpu: 'G4',
    assigned_gpu: 'G4',
    duration_ms: 412,
    cu_delta: 0.0011,
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
            sticky_key: 'mcp:antigravity:exp-main',
            code: 'import json, pathlib; print(len(pathlib.Path("/content/shard_0.jsonl").read_text().splitlines()))',
          },
        },
      },
      null,
      2,
    ),
    response_preview: JSON.stringify(
      {
        executionCount: 4,
        status: 'ok',
        stdout: '93\n',
        durationMs: 412,
        richOutputs: [],
      },
      null,
      2,
    ),
  },
  {
    id: 'tr_02',
    timestamp: new Date(Date.now() - 11_400).toISOString(),
    caller: 'Claude Code CLI',
    api_key_prefix: 'ch_sk_live_4d11e0',
    transport: 'mcp-http',
    tool_or_endpoint: 'execute_code',
    session_id: 'sess_a100_interactive',
    account_id: 'acc_proplus_03',
    account_email: 'dev-pro@robotics.example',
    requested_gpu: 'A100',
    assigned_gpu: 'A100',
    duration_ms: 829,
    cu_delta: 0.0021,
    status: 'ok',
    status_code: 200,
    request_preview: JSON.stringify(
      {
        jsonrpc: '2.0',
        method: 'tools/call',
        params: {
          name: 'execute_code',
          arguments: {
            session_id: 'sess_a100_interactive',
            code: 'bench_rmsnorm_swiglu(M=4096, N=8192, dtype="bf16")',
          },
        },
      },
      null,
      2,
    ),
    response_preview: JSON.stringify(
      {
        executionCount: 7,
        status: 'ok',
        stdout: 'Fused RMSNorm+SwiGLU: 1.48 TB/s HBM2e (0.142 ms)\n',
        result: '{"gbps": 1482.4, "speedup": 1.84}',
      },
      null,
      2,
    ),
  },
  {
    id: 'tr_03',
    timestamp: new Date(Date.now() - 48_000).toISOString(),
    caller: 'Antigravity IDE',
    api_key_prefix: 'ch_sk_live_9f8a2c',
    transport: 'mcp-http',
    tool_or_endpoint: 'create_runtime',
    session_id: 'sess_g4_shard0',
    account_id: 'acc_ultra_01',
    account_email: 'lab-ultra@research.example',
    requested_gpu: 'H100',
    assigned_gpu: 'G4',
    fallback_trace:
      'H100 assign -> HTTP 400 FAILED_PRECONDITION (Stockout on acc_ultra_01, 6h GPU-tier soft cooldown) -> Auto-Fallback G4 (96GB GDDR7 >= 80GB VRAM requirement satisfied) -> 200 OK',
    duration_ms: 4210,
    cu_delta: 0,
    status: 'fallback',
    status_code: 200,
    request_preview: JSON.stringify(
      {
        jsonrpc: '2.0',
        method: 'tools/call',
        params: {
          name: 'create_runtime',
          arguments: {
            accelerator: 'H100',
            high_mem: true,
            strategy: 'waste-zero',
            auto_fallback: true,
            fallback_chain: ['H100', 'G4'],
          },
        },
      },
      null,
      2,
    ),
    response_preview: JSON.stringify(
      {
        session_id: 'sess_g4_shard0',
        assigned_accelerator: 'G4',
        vram_gb: 96,
        fallback_applied: true,
        burn_rate_cu_hr: 9.66,
      },
      null,
      2,
    ),
  },
  {
    id: 'tr_04',
    timestamp: new Date(Date.now() - 96_000).toISOString(),
    caller: 'Python HivePool SDK',
    api_key_prefix: 'ch_sk_live_9f8a2c',
    transport: 'sdk',
    tool_or_endpoint: 'experiment_launch_sharded',
    session_id: 'sess_g4_shard1',
    account_id: 'acc_proplus_02',
    account_email: 'alex.chen@stanford-ai.example',
    requested_gpu: 'G4',
    assigned_gpu: 'G4',
    duration_ms: 5190,
    cu_delta: 0.014,
    status: 'ok',
    status_code: 201,
    request_preview: JSON.stringify(
      {
        experiment_id: 'exp_qwen3_27b_eval',
        num_shards: 4,
        accelerator: 'G4',
        strategy: 'waste-zero',
        script_path: 'benchmarks/eval_qwen3_27b_shard.py',
        auto_stop_on_complete: true,
      },
      null,
      2,
    ),
    response_preview: JSON.stringify(
      {
        experiment_id: 'exp_qwen3_27b_eval',
        status: 'RUNNING',
        shards_allocated: 4,
        accounts_routed: ['acc_ultra_01', 'acc_proplus_02', 'acc_proplus_03', 'acc_contrib_04'],
      },
      null,
      2,
    ),
  },
  {
    id: 'tr_05',
    timestamp: new Date(Date.now() - 185_000).toISOString(),
    caller: 'Codex CLI',
    api_key_prefix: 'ch_sk_live_71b20a',
    transport: 'mcp-stdio',
    tool_or_endpoint: 'pool_quota_summary',
    duration_ms: 38,
    cu_delta: 0,
    status: 'ok',
    status_code: 200,
    request_preview: JSON.stringify(
      { jsonrpc: '2.0', method: 'tools/call', params: { name: 'pool_quota_summary', arguments: {} } },
      null,
      2,
    ),
    response_preview: JSON.stringify(
      {
        total_available_cu: 1316.1,
        schedulable_cu: 1266.1,
        active_burn_rate_cu_hr: 37.86,
        g4_equivalent_hours: 131.06,
      },
      null,
      2,
    ),
  },
  {
    id: 'tr_06',
    timestamp: new Date(Date.now() - 310_000).toISOString(),
    caller: 'colabhive CLI',
    api_key_prefix: 'ch_sk_live_9f8a2c',
    transport: 'cli',
    tool_or_endpoint: 'POST /api/v1/sessions/sess_contrib_test/exec',
    session_id: 'sess_g4_shard3',
    account_id: 'acc_contrib_04',
    account_email: 'maya.lin@mit-vision.example',
    requested_gpu: 'G4',
    assigned_gpu: 'G4',
    duration_ms: 19,
    cu_delta: 0,
    status: 'error',
    status_code: 403,
    error_message:
      'Blocked by ColabHive Contributor Firewall: google.colab.drive.mount() & GCP credential propagation are forbidden on contributed accounts.',
    request_preview: JSON.stringify(
      {
        session_id: 'sess_g4_shard3',
        code: 'from google.colab import drive; drive.mount("/content/drive")',
      },
      null,
      2,
    ),
    response_preview: JSON.stringify(
      {
        error:
          'Blocked by ColabHive Contributor Firewall: Drive/GCP credential propagation is disabled on donated accounts.',
      },
      null,
      2,
    ),
  },
];

/**
 * Plain-human routing strategy copy. `zhBadge` / `enBadge` are the locale-aware
 * badges (the zh-CN UI never shows an all-caps English badge); `badge` is kept
 * as the English alias for older call sites.
 */
export const STRATEGY_METADATA: Record<
  RoutingStrategy,
  {
    badge: string;
    zhBadge: string;
    enBadge: string;
    zhTitle: string;
    enTitle: string;
    zhDesc: string;
    enDesc: string;
    accentColor: string;
  }
> = {
  'waste-zero': {
    badge: 'Recommended',
    zhBadge: '推荐 · 防浪费',
    enBadge: 'Recommended',
    zhTitle: '防过期优先（推荐） · 优先消耗快过期的额度',
    enTitle: 'Expiring Quota First (Recommended) · Zero Waste',
    zhDesc: '优先使用即将到期或临近月度重置的额度批次与免费窗口，避免算力白白过期作废。',
    enDesc: 'Uses quota batches closest to 90-day expiry or monthly reset first so zero compute expires unused.',
    accentColor: 'var(--accent)',
  },
  'max-balance': {
    badge: 'Long Runs',
    zhBadge: '长时训练稳跑',
    enBadge: 'Long Runs',
    zhTitle: '大余额优先 · 适合长时大模型训练不切号',
    enTitle: 'Highest Balance First · Best for Long Training Runs',
    zhDesc: '优先分配可用余额最充足的账号，避免大模型微调或长时任务跑到一半因余额见底而中断。',
    enDesc: 'Routes long training and heavy evaluation jobs to accounts with the deepest available quota.',
    accentColor: 'var(--accent)',
  },
  'sticky-session': {
    badge: 'Warm Cache',
    zhBadge: '保留权重缓存',
    enBadge: 'Warm Cache',
    zhTitle: '固定环境优先 · 复用已下载的 60GB 模型权重',
    enTitle: 'Keep Same Environment · Reuse Cached 60GB Weights',
    zhDesc: '将同一个 AI 助手的连续调用固定在同一台云端实例上，直接复用显存模型与已下载的几十 GB 权重。',
    enDesc: 'Pins consecutive AI tool calls to the same warm GPU instance, preserving VRAM models and 60GB HF cache.',
    accentColor: 'var(--accent)',
  },
  'round-robin': {
    badge: 'Multi-GPU',
    zhBadge: '多卡并发提速',
    enBadge: 'Multi-GPU',
    zhTitle: '多号轮流分摊 · 适合多卡并发评测',
    enTitle: 'Spread Across Accounts · Parallel Multi-GPU Eval',
    zhDesc: '在所有可用账号之间均匀分配任务，轻松开启 4 卡或 8 卡同时并行跑评测，成倍缩短等待时间。',
    enDesc: 'Distributes concurrent GPU sessions evenly across healthy accounts to maximize parallel shard slots.',
    accentColor: 'var(--accent)',
  },
  'free-opportunistic': {
    badge: 'Free First',
    zhBadge: '免费卡优先',
    enBadge: 'Free First',
    zhTitle: '优先白嫖免费卡 · 先用免费 T4 再扣付费额度',
    enTitle: 'Free GPU First · Use Free T4 Before Paid Quota',
    zhDesc: '自动优先使用各账号动态恢复的免费 T4 / CPU 额度，只有免费卡满载时才动用付费额度。',
    enDesc: 'Schedules onto dynamically refilling free T4/CPU quota first and only spends paid CU when full.',
    accentColor: 'var(--accent)',
  },
};
