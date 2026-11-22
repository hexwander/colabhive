import React, { useId, useMemo, useState } from 'react';
import { AlertTriangle, Cpu, Plus, Zap } from 'lucide-react';
import {
  ALL_ROUTING_STRATEGIES,
  DEFAULT_HARDWARE_CATALOG,
  type AcceleratorType,
  type Account,
  type Experiment,
  type HardwareSpec,
  type RoutingStrategy,
  type RuntimeSession,
} from '../../types';
import { api, formatCU } from '../../lib/api';
import { useI18n } from '../../i18n';
import {
  ActionButton,
  AnimatedNumber,
  DialogModal,
  InsetWell,
  SegmentedQuotaBar,
  StatusPill,
  Switch,
  cn,
} from '../ui';
import { getAcceleratorCssColor, STRATEGY_METADATA } from './workbenchMockData';

const FIELD_LABEL = 'block mb-1.5 text-[12px] font-medium text-fg-secondary';
/** Theme-safe native `<option>` (never a hardcoded dark hex). */
const OPTION_CLASS = 'bg-surface-2 text-fg';

/** zh-CN workload blurbs per tier (the catalog's `optimal_workload` is English-only). */
const ZH_WORKLOAD_BY_TIER: Record<AcceleratorType, string> = {
  G4: '26B+ 大模型微调与推理',
  H100: 'FP8 大模型高速训练',
  A100: '7B–14B 训练 · CUDA 兼容佳',
  L4: '7B–9B 推理 / LoRA 省额度',
  T4: '轻量调试 · 常有免费卡',
  V6E1: 'JAX / XLA 矩阵加速',
  V5E1: '轻量 TPU 特征提取',
  CPU: '数据清洗与预处理',
};

function FormError({ message }: { message: string }) {
  return (
    <div
      data-tone="coral"
      role="alert"
      className="flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[12.5px]"
      style={{
        backgroundColor: 'var(--tone-soft)',
        borderColor: 'var(--tone-border)',
        color: 'var(--tone-fg)',
      }}
    >
      <AlertTriangle className="w-4 h-4 shrink-0 mt-px" />
      <span className="ch-num min-w-0 break-words">{message}</span>
    </div>
  );
}

function ToggleRow({
  title,
  note,
  checked,
  onChange,
  disabled,
}: {
  title: React.ReactNode;
  note: React.ReactNode;
  checked: boolean;
  onChange: (next: boolean) => void;
  disabled?: boolean;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <div className="text-[12.5px] font-medium text-fg">{title}</div>
        <div className="mt-0.5 text-[12px] leading-relaxed text-fg-secondary">{note}</div>
      </div>
      <Switch
        checked={checked}
        onChange={onChange}
        disabled={disabled}
        label={typeof title === 'string' ? title : undefined}
        className="mt-0.5"
      />
    </div>
  );
}

// ============================================================================
// Modal 1: Provision a single interactive runtime session
// ============================================================================

export interface ProvisionSessionModalProps {
  open: boolean;
  accounts: Account[];
  hardwareCatalog?: HardwareSpec[];
  isOfflineDemo?: boolean;
  onClose: () => void;
  onCreated: (session: RuntimeSession) => void;
}

export function ProvisionSessionModal({
  open,
  accounts,
  hardwareCatalog = DEFAULT_HARDWARE_CATALOG,
  isOfflineDemo = false,
  onClose,
  onCreated,
}: ProvisionSessionModalProps) {
  const { locale, displayEmail } = useI18n();
  const isZh = locale === 'zh-CN';
  const formId = useId();

  const [name, setName] = useState('interactive-g4-dev');
  const [accelerator, setAccelerator] = useState<AcceleratorType>('G4');
  const [highMem, setHighMem] = useState(true);
  const [strategy, setStrategy] = useState<RoutingStrategy>('waste-zero');
  const [accountSelector, setAccountSelector] = useState('');
  const [autoFallback, setAutoFallback] = useState(true);
  const [stickyKey, setStickyKey] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);

  const selectedSpec = useMemo(
    () =>
      hardwareCatalog.find((h) => h.accelerator === accelerator) ||
      DEFAULT_HARDWARE_CATALOG[0],
    [hardwareCatalog, accelerator],
  );

  const effectiveHighMem = selectedSpec.always_high_mem ? true : highMem;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setErrorMsg(null);

    try {
      if (isOfflineDemo) {
        await new Promise((r) => window.setTimeout(r, 420));
        const chosenAcc =
          accounts.find((a) => a.id === accountSelector) || accounts[0];
        const sim: RuntimeSession = {
          id: `sess_${accelerator.toLowerCase()}_${Date.now().toString(36)}`,
          name: name.trim() || `${accelerator.toLowerCase()}-kernel`,
          account_id: chosenAcc?.id || 'acc_ultra_01',
          account_email: chosenAcc?.email || 'lab-ultra@research.example',
          backend: 'public',
          endpoint: `gpu-${accelerator.toLowerCase()}-s-${Math.random().toString(36).slice(2, 8)}`,
          notebook_hash: 'nbh_Q29sYWJIaXZlX0xpdmVfUHJvdmlzaW9uZWRfU2Vzc2lvbg',
          proxy_url: 'https://8080-gpu.colab.googleusercontent.com',
          variant: selectedSpec.variant,
          requested_accelerator: accelerator,
          accelerator,
          fallback_applied: false,
          shape: effectiveHighMem ? 'HIGH_RAM' : 'STANDARD',
          status: 'IDLE',
          status_message: 'Warm Jupyter Kernel Ready',
          kernel_id: crypto.randomUUID?.() || 'k-9281a0',
          burn_rate_cu_hr: selectedSpec.default_burn_rate_cu_hr,
          accumulated_cu: 0,
          colab_web_url: 'https://colab.research.google.com/notebooks/empty.ipynb?authuser=0',
          sticky_key: stickyKey.trim() || undefined,
          keepalive_healthy: true,
          last_keepalive_at: new Date().toISOString(),
          zmq_reconnect_count: 0,
          uptime_sec: 1,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
          worker: {
            pid: 3104,
            proc_state: 'S',
            alive: true,
            zombie_detected: false,
            has_fatal_trace: false,
            log_offset: 0,
          },
          resources: {
            memory_total_bytes: 89_657_389_056,
            memory_free_bytes: 81_200_000_000,
            memory_used_bytes: 8_457_389_056,
            disk_total_bytes: 253_055_090_688,
            disk_used_bytes: 28_100_000_000,
            sampled_at: new Date().toISOString(),
            gpus:
              selectedSpec.vram_gb > 0
                ? [
                    {
                      name: selectedSpec.display_name,
                      memory_used_bytes: 1_288_490_188,
                      memory_total_bytes: selectedSpec.vram_gb * 1024 * 1024 * 1024,
                      gpu_utilization: 0.04,
                      memory_utilization: 0.02,
                      temperature_c: 44,
                      power_watts: 68,
                      ever_used: true,
                    },
                  ]
                : [],
          },
        };
        onCreated(sim);
        onClose();
        return;
      }

      const created = await api.createSession({
        name: name.trim() || undefined,
        accelerator,
        high_mem: effectiveHighMem,
        strategy,
        account_selector: accountSelector || undefined,
        auto_fallback: autoFallback,
        sticky_key: stickyKey.trim() || undefined,
        caller_name: 'workbench-ui',
      });
      onCreated(created);
      onClose();
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <DialogModal
      open={open}
      onClose={onClose}
      size="2xl"
      icon={<Cpu />}
      title={isZh ? '启动新 GPU 实例' : 'New GPU Session'}
      subtitle={
        isZh
          ? '从算力池自动挑选最合适的账号分配云端显卡，全程开启防掉线保活与卡死检测。'
          : 'Automatically picks the best account in your pool and allocates a cloud GPU, with keep-alive and hung-process detection always on.'
      }
      footer={
        <div className="flex items-center justify-end gap-2">
          <ActionButton variant="ghost" size="md" onClick={onClose}>
            {isZh ? '取消' : 'Cancel'}
          </ActionButton>
          <ActionButton
            type="submit"
            form={formId}
            variant="primary"
            size="md"
            icon={<Plus />}
            loading={submitting}
          >
            {isZh ? `启动 ${accelerator} 实例` : `Start ${accelerator} Session`}
          </ActionButton>
        </div>
      }
    >
      <form id={formId} onSubmit={handleSubmit} className="space-y-5">
        {/* GPU choice */}
        <div>
          <div className={FIELD_LABEL} id={`${formId}-tier`}>
            {isZh ? '选择显卡型号' : 'Choose a GPU'}
          </div>
          <div
            role="radiogroup"
            aria-labelledby={`${formId}-tier`}
            className="grid grid-cols-2 sm:grid-cols-4 gap-2"
          >
            {hardwareCatalog.map((spec) => {
              const active = accelerator === spec.accelerator;
              const workload = isZh
                ? (ZH_WORKLOAD_BY_TIER[spec.accelerator] ?? spec.optimal_workload)
                : spec.optimal_workload;
              return (
                <button
                  key={spec.accelerator}
                  type="button"
                  role="radio"
                  aria-checked={active}
                  onClick={() => setAccelerator(spec.accelerator)}
                  title={workload}
                  className={cn(
                    'rounded-2xl border p-2.5 text-left cursor-pointer transition-all',
                    active
                      ? 'bg-surface-2 border-[var(--accent)] shadow-[var(--shadow-card)]'
                      : 'bg-surface-1/75 border-line-faint hover:bg-surface-2 hover:border-line-strong',
                  )}
                >
                  <div className="flex items-center justify-between gap-1.5">
                    <span className="ch-num inline-flex items-center gap-1.5 min-w-0 text-[12.5px] font-semibold text-fg">
                      <span
                        className="w-2 h-2 rounded-full shrink-0"
                        style={{ backgroundColor: getAcceleratorCssColor(spec.accelerator) }}
                      />
                      <span className="truncate">{spec.short_label}</span>
                    </span>
                    {spec.free_tier_eligible ? (
                      <StatusPill size="xs" tone="emerald" mono>
                        {isZh ? '免费' : 'Free'}
                      </StatusPill>
                    ) : (
                      active && (
                        <span
                          aria-hidden="true"
                          className="w-2 h-2 rounded-full shrink-0"
                          style={{ backgroundColor: 'var(--accent)' }}
                        />
                      )
                    )}
                  </div>
                  <div className="ch-num mt-1 text-[11.5px] text-fg-secondary">
                    ~{formatCU(spec.default_burn_rate_cu_hr)} CU/h
                  </div>
                  <div className="mt-0.5 truncate text-[11px] text-fg-muted">
                    {workload}
                  </div>
                </button>
              );
            })}
          </div>
        </div>

        {/* Name + sticky key */}
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
          <div>
            <label htmlFor={`${formId}-name`} className={FIELD_LABEL}>
              {isZh ? '实例名称' : 'Session name'}
            </label>
            <input
              id={`${formId}-name`}
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="qwen3-eval-interactive"
              spellCheck={false}
              className="ui-input font-mono ch-num"
            />
          </div>

          <div>
            <label
              htmlFor={`${formId}-sticky`}
              className={FIELD_LABEL}
              title={
                isZh
                  ? '填写后，使用相同标识的 AI 助手会一直复用这台实例和已下载的权重'
                  : 'AI assistants using the same key keep reusing this session and its downloaded weights'
              }
            >
              {isZh ? '固定环境标识（可选）' : 'Pinned environment key (optional)'}
            </label>
            <input
              id={`${formId}-sticky`}
              type="text"
              value={stickyKey}
              onChange={(e) => setStickyKey(e.target.value)}
              placeholder="mcp:antigravity:workspace-1"
              spellCheck={false}
              className="ui-input font-mono ch-num"
            />
          </div>
        </div>

        {/* Strategy + account */}
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3.5">
          <div>
            <label htmlFor={`${formId}-strategy`} className={FIELD_LABEL}>
              {isZh ? '选号方式' : 'Routing strategy'}
            </label>
            <select
              id={`${formId}-strategy`}
              value={strategy}
              onChange={(e) => setStrategy(e.target.value as RoutingStrategy)}
              title={isZh ? STRATEGY_METADATA[strategy].zhDesc : STRATEGY_METADATA[strategy].enDesc}
              className="ui-input cursor-pointer"
            >
              {ALL_ROUTING_STRATEGIES.map((st) => (
                <option key={st} value={st} className={OPTION_CLASS}>
                  {isZh ? STRATEGY_METADATA[st].zhTitle : STRATEGY_METADATA[st].enTitle}
                </option>
              ))}
            </select>
          </div>

          <div>
            <label htmlFor={`${formId}-account`} className={FIELD_LABEL}>
              {isZh ? '使用哪个账号' : 'Account'}
            </label>
            <select
              id={`${formId}-account`}
              value={accountSelector}
              onChange={(e) => setAccountSelector(e.target.value)}
              className="ui-input cursor-pointer"
            >
              <option value="" className={OPTION_CLASS}>
                {isZh ? '自动挑选（推荐）' : 'Pick automatically (recommended)'}
              </option>
              {accounts.map((acc) => (
                <option key={acc.id} value={acc.id} className={OPTION_CLASS}>
                  {displayEmail(acc.email)} ({formatCU(acc.schedulable_cu)} CU)
                </option>
              ))}
            </select>
          </div>
        </div>

        {/* High-RAM + auto-fallback */}
        <InsetWell padding="md" className="space-y-3.5">
          <ToggleRow
            title={isZh ? '大内存模式' : 'High-RAM mode'}
            note={
              selectedSpec.always_high_mem
                ? isZh
                  ? `${selectedSpec.accelerator} 默认就是大内存，无需额外开启。`
                  : `${selectedSpec.accelerator} is always High-RAM.`
                : isZh
                  ? '提供约 83.5 GB 系统内存，大模型权重加载更快、更不容易爆内存。'
                  : 'Allocates ~83.5 GB host RAM for faster weight loading.'
            }
            checked={effectiveHighMem}
            disabled={selectedSpec.always_high_mem}
            onChange={setHighMem}
          />
          <div className="border-t border-line-faint" />
          <ToggleRow
            title={isZh ? '缺卡时自动换卡防报错' : 'Auto-switch GPU when out of stock'}
            note={
              <>
                {isZh ? '目标显卡缺货时按顺序换用下一档：' : 'Falls back in order: '}
                <span className="ch-num">H100 → G4 → A100 → L4 → T4</span>
              </>
            }
            checked={autoFallback}
            onChange={setAutoFallback}
          />
        </InsetWell>

        {errorMsg && <FormError message={errorMsg} />}
      </form>
    </DialogModal>
  );
}

// ============================================================================
// Modal 2: Parallel multi-GPU sharded experiment launcher
// ============================================================================

export interface LaunchShardedModalProps {
  open: boolean;
  hardwareCatalog?: HardwareSpec[];
  isOfflineDemo?: boolean;
  onClose: () => void;
  onLaunched: (experiment: Experiment) => void;
}

const DEFAULT_SHARD_SCRIPT = `import os, json, time
shard_id = int(os.environ.get("COLABHIVE_SHARD_ID", "0"))
num_shards = int(os.environ.get("COLABHIVE_NUM_SHARDS", "4"))
print(f"[Shard {shard_id}/{num_shards}] Starting parallel GPU evaluation...")
out_path = f"/content/shard_{shard_id}.jsonl"
with open(out_path, "w") as f:
    for idx in range(shard_id, 200, num_shards):
        f.write(json.dumps({"idx": idx, "shard": shard_id, "score": 0.84}) + "\\n")
print(f"[Shard {shard_id}] Completed & synced -> {out_path}")`;

export function LaunchShardedModal({
  open,
  hardwareCatalog = DEFAULT_HARDWARE_CATALOG,
  isOfflineDemo = false,
  onClose,
  onLaunched,
}: LaunchShardedModalProps) {
  const { locale } = useI18n();
  const isZh = locale === 'zh-CN';
  const formId = useId();

  const [title, setTitle] = useState('Qwen3-27B Eval · Parallel Sharded Sweep');
  const [experimentId, setExperimentId] = useState(
    () => `exp_sweep_${Math.random().toString(36).slice(2, 6)}`,
  );
  const [accelerator, setAccelerator] = useState<AcceleratorType>('G4');
  const [numShards, setNumShards] = useState(4);
  const [strategy, setStrategy] = useState<RoutingStrategy>('waste-zero');
  const [scriptPath, setScriptPath] = useState('benchmarks/eval_shard.py');
  const [scriptCode, setScriptCode] = useState(DEFAULT_SHARD_SCRIPT);
  const [packages, setPackages] = useState('transformers accelerate datasets');
  const [checkpointGlob, setCheckpointGlob] = useState('/content/shard_*.jsonl');
  const [autoStop, setAutoStop] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);

  const selectedSpec = useMemo(
    () =>
      hardwareCatalog.find((h) => h.accelerator === accelerator) ||
      DEFAULT_HARDWARE_CATALOG[0],
    [hardwareCatalog, accelerator],
  );

  const singleGpuHours = 4.0;
  const shardedWallHours = singleGpuHours / numShards;
  const aggregateBurnRate = selectedSpec.default_burn_rate_cu_hr * numShards;
  const totalEstimatedCu = selectedSpec.default_burn_rate_cu_hr * singleGpuHours;

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitting(true);
    setErrorMsg(null);

    const pkgList = packages
      .split(/[\s,]+/)
      .map((p) => p.trim())
      .filter(Boolean);

    try {
      if (isOfflineDemo) {
        await new Promise((r) => window.setTimeout(r, 480));
        const expId = experimentId.trim() || `exp_${Date.now().toString(36)}`;
        const simExp: Experiment = {
          id: expId,
          title: title.trim() || `${numShards}× ${accelerator} Sharded Sweep`,
          status: 'RUNNING',
          script_path: scriptPath.trim() || 'eval_shard.py',
          accelerator,
          high_mem: true,
          num_shards: numShards,
          strategy,
          completed_shards: 0,
          running_shards: numShards,
          failed_shards: 0,
          synced_records_total: 0,
          target_records_total: 200,
          aggregate_burn_rate_cu_hr: aggregateBurnRate,
          cu_burned_so_far: 0.04,
          estimated_total_cu: totalEstimatedCu,
          auto_stop_on_complete: autoStop,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
          shards: Array.from({ length: numShards }).map((_, idx) => ({
            shard_id: idx,
            session_id: `${expId}_s${idx}`,
            session_name: `${expId}-s${idx}`,
            account_id: `acc_pool_0${(idx % 4) + 1}`,
            account_email: `pool.worker0${(idx % 4) + 1}@gmail.com`,
            accelerator,
            status: 'RUNNING',
            pid: 4100 + idx,
            proc_state: 'R',
            zombie_detected: false,
            progress_pct: 12 + idx * 4,
            synced_records: 6 + idx * 2,
            target_records: Math.ceil(200 / numShards),
            vram_used_gb: Math.round(selectedSpec.vram_gb * 0.54 * 10) / 10,
            vram_total_gb: selectedSpec.vram_gb,
            sm_util_pct: 91,
            temp_c: 63 + idx,
            cu_burned: 0.01,
            startup_self_check: 'PASSED',
            log_tail: `[Shard #${idx}] Initialized ${accelerator} worker`,
            started_at: new Date().toISOString(),
          })),
        };
        onLaunched(simExp);
        onClose();
        return;
      }

      const launched = await api.launchExperiment({
        experiment_id: experimentId.trim() || `exp_${Date.now().toString(36)}`,
        title: title.trim() || undefined,
        script_path: scriptPath.trim() || 'eval_shard.py',
        script_code: scriptCode,
        packages: pkgList,
        accelerator,
        high_mem: true,
        num_shards: numShards,
        strategy,
        remote_checkpoint_glob: checkpointGlob.trim() || undefined,
        auto_stop_on_complete: autoStop,
      });
      onLaunched(launched);
      onClose();
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <DialogModal
      open={open}
      onClose={onClose}
      size="2xl"
      icon={<Zap />}
      title={isZh ? '多卡并行跑实验' : 'Run Parallel Shards'}
      subtitle={
        isZh
          ? '自动把任务切分到多张云端 GPU 同时运行，总消耗额度不变，等待时间成倍缩短！'
          : 'Automatically splits your job across several cloud GPUs at once — same total quota, a fraction of the wait.'
      }
      footer={
        <div className="flex items-center justify-end gap-2">
          <ActionButton variant="ghost" size="md" onClick={onClose}>
            {isZh ? '取消' : 'Cancel'}
          </ActionButton>
          <ActionButton
            type="submit"
            form={formId}
            variant="primary"
            size="md"
            icon={<Zap />}
            loading={submitting}
          >
            {isZh
              ? `启动 ${numShards} 张 ${accelerator} 并行运行`
              : `Launch ${numShards}× ${accelerator} in Parallel`}
          </ActionButton>
        </div>
      }
    >
      <form id={formId} onSubmit={handleSubmit} className="space-y-5">
        {/* Speedup estimate */}
        <InsetWell padding="md" className="space-y-3">
          <div className="grid grid-cols-3 gap-3 sm:divide-x sm:divide-line-faint">
            <div className="min-w-0">
              <div className="flex items-center justify-between gap-1.5">
                <span className="text-[11.5px] font-medium text-fg-muted">
                  {isZh ? '预计等待时间' : 'Estimated wait'}
                </span>
                <StatusPill size="xs" tone="emerald" mono>
                  {numShards}× {isZh ? '提速' : 'faster'}
                </StatusPill>
              </div>
              <div className="ch-num mt-1 text-[18px] leading-6 font-semibold text-fg">
                <AnimatedNumber value={shardedWallHours} decimals={2} suffix="h" />
              </div>
              <div className="ch-num mt-0.5 text-[11px] text-fg-muted">
                {isZh ? '单卡需要 4.00h' : 'vs 4.00h on one GPU'}
              </div>
            </div>
            <div className="min-w-0 sm:pl-4">
              <div className="text-[11.5px] font-medium text-fg-muted">
                {isZh ? '每小时总消耗' : 'Combined burn'}
              </div>
              <div className="ch-num mt-1 text-[18px] leading-6 font-semibold text-fg">
                <AnimatedNumber value={aggregateBurnRate} decimals={2} />
                <span className="ml-1 text-[12px] font-medium text-fg-muted">CU/h</span>
              </div>
              <div className="ch-num mt-0.5 text-[11px] text-fg-muted">
                {numShards}× {selectedSpec.short_label}
              </div>
            </div>
            <div className="min-w-0 sm:pl-4">
              <div className="text-[11.5px] font-medium text-fg-muted">
                {isZh ? '预计总消耗' : 'Estimated total'}
              </div>
              <div className="ch-num mt-1 text-[18px] leading-6 font-semibold text-fg">
                <AnimatedNumber value={totalEstimatedCu} decimals={2} />
                <span className="ml-1 text-[12px] font-medium text-fg-muted">CU</span>
              </div>
              <div className="mt-0.5 text-[11px] text-fg-muted">
                {isZh ? `快 ${numShards} 倍，总额度不变` : `${numShards}× faster, same total CU`}
              </div>
            </div>
          </div>
          <SegmentedQuotaBar
            value={Math.round((1 / numShards) * 100)}
            projectedValue={100}
            segments={20}
            tone="emerald"
          />
        </InsetWell>

        {/* Title + ID */}
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-3.5">
          <div className="sm:col-span-2">
            <label htmlFor={`${formId}-title`} className={FIELD_LABEL}>
              {isZh ? '实验名称' : 'Experiment title'}
            </label>
            <input
              id={`${formId}-title`}
              type="text"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className="ui-input"
            />
          </div>

          <div>
            <label htmlFor={`${formId}-id`} className={FIELD_LABEL}>
              {isZh ? '实验编号' : 'Experiment ID'}
            </label>
            <input
              id={`${formId}-id`}
              type="text"
              value={experimentId}
              onChange={(e) => setExperimentId(e.target.value)}
              spellCheck={false}
              className="ui-input font-mono ch-num"
            />
          </div>
        </div>

        {/* Accelerator + shards + strategy */}
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-3.5">
          <div>
            <label htmlFor={`${formId}-acc`} className={FIELD_LABEL}>
              {isZh ? '每张卡的型号' : 'GPU per shard'}
            </label>
            <select
              id={`${formId}-acc`}
              value={accelerator}
              onChange={(e) => setAccelerator(e.target.value as AcceleratorType)}
              className="ui-input ch-num cursor-pointer"
            >
              {hardwareCatalog.map((h) => (
                <option key={h.accelerator} value={h.accelerator} className={OPTION_CLASS}>
                  {h.short_label} (~{formatCU(h.default_burn_rate_cu_hr)} CU/h)
                </option>
              ))}
            </select>
          </div>

          <div>
            <label
              htmlFor={`${formId}-shards`}
              className={cn(FIELD_LABEL, 'flex items-center justify-between')}
            >
              <span>{isZh ? '并行显卡数量' : 'Parallel GPUs'}</span>
              <span className="ch-num font-semibold text-[var(--emerald-text)]">{numShards}×</span>
            </label>
            <div className="flex items-center h-8">
              <input
                id={`${formId}-shards`}
                type="range"
                min={1}
                max={8}
                step={1}
                value={numShards}
                onChange={(e) => setNumShards(parseInt(e.target.value, 10))}
                className="w-full cursor-pointer"
                style={{ accentColor: 'var(--emerald)' }}
              />
            </div>
          </div>

          <div>
            <label htmlFor={`${formId}-strategy`} className={FIELD_LABEL}>
              {isZh ? '选号方式' : 'Routing strategy'}
            </label>
            <select
              id={`${formId}-strategy`}
              value={strategy}
              onChange={(e) => setStrategy(e.target.value as RoutingStrategy)}
              title={isZh ? STRATEGY_METADATA[strategy].zhDesc : STRATEGY_METADATA[strategy].enDesc}
              className="ui-input cursor-pointer"
            >
              {ALL_ROUTING_STRATEGIES.map((st) => (
                <option key={st} value={st} className={OPTION_CLASS}>
                  {isZh ? STRATEGY_METADATA[st].zhTitle : STRATEGY_METADATA[st].enTitle}
                </option>
              ))}
            </select>
          </div>
        </div>

        {/* Script path + packages + checkpoint glob */}
        <div className="grid grid-cols-1 sm:grid-cols-3 gap-3.5">
          <div>
            <label htmlFor={`${formId}-script`} className={FIELD_LABEL}>
              {isZh ? '运行脚本路径' : 'Entry script path'}
            </label>
            <input
              id={`${formId}-script`}
              type="text"
              value={scriptPath}
              onChange={(e) => setScriptPath(e.target.value)}
              spellCheck={false}
              className="ui-input font-mono ch-num"
            />
          </div>

          <div>
            <label htmlFor={`${formId}-pkgs`} className={FIELD_LABEL}>
              {isZh ? '预装依赖包' : 'Pre-install packages'}
            </label>
            <input
              id={`${formId}-pkgs`}
              type="text"
              value={packages}
              onChange={(e) => setPackages(e.target.value)}
              spellCheck={false}
              className="ui-input font-mono ch-num"
            />
          </div>

          <div>
            <label
              htmlFor={`${formId}-glob`}
              className={FIELD_LABEL}
              title={
                isZh
                  ? '每张卡跑完后，匹配这个规则的结果文件会被自动汇总保存'
                  : 'Result files matching this pattern are collected from every shard'
              }
            >
              {isZh ? '结果文件匹配规则' : 'Result file pattern'}
            </label>
            <input
              id={`${formId}-glob`}
              type="text"
              value={checkpointGlob}
              onChange={(e) => setCheckpointGlob(e.target.value)}
              spellCheck={false}
              className="ui-input font-mono ch-num"
            />
          </div>
        </div>

        {/* Worker script */}
        <div>
          <label htmlFor={`${formId}-code`} className={FIELD_LABEL}>
            {isZh ? '每张卡运行的脚本' : 'Script run on each GPU'}
            <span className="ml-2 font-normal text-fg-muted">
              {isZh ? '可读取环境变量 ' : 'reads '}
              <span className="ch-num">COLABHIVE_SHARD_ID / COLABHIVE_NUM_SHARDS</span>
            </span>
          </label>
          <textarea
            id={`${formId}-code`}
            value={scriptCode}
            onChange={(e) => setScriptCode(e.target.value)}
            rows={6}
            spellCheck={false}
            className="block w-full rounded-xl border px-3.5 py-3 text-[12px] leading-[1.65] outline-none resize-y transition-shadow focus:shadow-[0_0_0_3px_var(--accent-soft)]"
            style={{
              backgroundColor: 'var(--code-bg)',
              borderColor: 'var(--code-border)',
              color: 'var(--code-fg)',
              fontFamily: 'var(--font-mono)',
            }}
          />
        </div>

        {/* Auto-teardown guardrail */}
        <InsetWell padding="md">
          <ToggleRow
            title={isZh ? '跑完自动释放显卡' : 'Auto-release GPUs when done'}
            note={
              isZh
                ? '每张卡跑完后立即保存结果并归还显卡，避免空转白扣额度。'
                : 'Saves results and releases each GPU the moment it finishes, so nothing idles.'
            }
            checked={autoStop}
            onChange={setAutoStop}
          />
        </InsetWell>

        {errorMsg && <FormError message={errorMsg} />}
      </form>
    </DialogModal>
  );
}
