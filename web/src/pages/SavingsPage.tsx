import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  Award,
  Check,
  Copy,
  Cpu,
  Download,
  Flame,
  Gauge,
  Loader2,
  Monitor,
  Moon,
  RefreshCw,
  Rocket,
  ShieldCheck,
  Smartphone,
  Sparkles,
  Sun,
  Users,
  Zap,
} from 'lucide-react';
import type { Account, QuotaSummary, SavingsReceiptStats } from '../types';
import { useI18n } from '../i18n';
import { navigate as fallbackNavigate } from '../components/layout/navigation';
import { V11_DEMO_EVENT, WS_V11_EVENT, v11Api } from '../lib/apiV11';
import {
  ActionButton,
  InsetWell,
  LayeredCard,
  PageHeader,
  SegmentedTabs,
  Skeleton,
  StatusPill,
} from '../components/ui/primitives';
import {
  exportSavingsPosterPng,
  SavingsReceiptCanvas,
  scrubSavingsPII,
  type PosterLayout,
  type PosterTheme,
} from '../components/ui/SavingsReceiptModal';

export interface SavingsPageProps {
  accounts?: Account[];
  summary?: QuotaSummary;
  onNavigate?: (to: string) => void;
}

export function SavingsPage({
  accounts: _accounts = [],
  summary: _summary,
  onNavigate,
}: SavingsPageProps = {}) {
  const { locale, theme: appTheme } = useI18n();
  const zh = locale === 'zh-CN';
  const nav = onNavigate ?? fallbackNavigate;

  const [stats, setStats] = useState<SavingsReceiptStats | null>(null);
  const [loading, setLoading] = useState<boolean>(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [exportError, setExportError] = useState<string | null>(null);

  const [posterTheme, setPosterTheme] = useState<PosterTheme>(() =>
    appTheme === 'light' ? 'warm-alabaster' : 'obsidian-gold',
  );
  const [layout, setLayout] = useState<PosterLayout>('vertical');
  const [downloading, setDownloading] = useState<boolean>(false);
  const [copiedText, setCopiedText] = useState<boolean>(false);

  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const mountedRef = useRef(true);
  const requestSeqRef = useRef(0);
  const copyTimerRef = useRef<number | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
    };
  }, []);

  const refreshReceipt = useCallback(async () => {
    const seq = ++requestSeqRef.current;
    setLoading(true);
    setLoadError(null);
    setExportError(null);
    try {
      const data = await v11Api.getSavingsReceipt();
      if (!mountedRef.current || seq !== requestSeqRef.current) return;
      setStats(data);
    } catch (err) {
      if (!mountedRef.current || seq !== requestSeqRef.current) return;
      setStats(null);
      setLoadError(
        err instanceof Error
          ? err.message
          : zh
            ? '无法从 ColabHive 服务拉取真实算力省钱账单，请确认服务在线后重试'
            : 'Failed to fetch verified savings statistics from the ColabHive server.',
      );
    } finally {
      if (mountedRef.current && seq === requestSeqRef.current) {
        setLoading(false);
      }
    }
  }, [zh]);

  useEffect(() => {
    void refreshReceipt();
  }, [refreshReceipt]);

  useEffect(() => {
    const onStateChanged = () => {
      void refreshReceipt();
    };
    window.addEventListener(V11_DEMO_EVENT, onStateChanged);
    window.addEventListener(WS_V11_EVENT, onStateChanged);
    return () => {
      window.removeEventListener(V11_DEMO_EVENT, onStateChanged);
      window.removeEventListener(WS_V11_EVENT, onStateChanged);
    };
  }, [refreshReceipt]);

  const shareCopy = useMemo(
    () => (stats ? scrubSavingsPII(zh ? stats.share_copy_zh : stats.share_copy_en) : ''),
    [stats, zh],
  );

  const canExport = Boolean(stats && !loadError && !loading);

  const handleDownloadPng = useCallback(async () => {
    const canvas = canvasRef.current;
    if (!canvas || !stats) return;
    setDownloading(true);
    setExportError(null);
    try {
      await exportSavingsPosterPng(canvas, stats, posterTheme, layout, zh);
    } catch (err) {
      if (mountedRef.current) {
        setExportError(
          err instanceof Error
            ? err.message
            : zh
              ? '保存高清 PNG 失败，请稍后重试'
              : 'Failed to export HD PNG poster',
        );
      }
    } finally {
      if (mountedRef.current) {
        setDownloading(false);
      }
    }
  }, [stats, posterTheme, layout, zh]);

  const handleCopyShareText = useCallback(async () => {
    if (!shareCopy) return;
    setExportError(null);
    try {
      await navigator.clipboard.writeText(shareCopy);
      if (!mountedRef.current) return;
      setCopiedText(true);
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
      copyTimerRef.current = window.setTimeout(() => {
        if (mountedRef.current) setCopiedText(false);
      }, 2000);
    } catch {
      if (mountedRef.current) {
        setExportError(
          zh
            ? '剪贴板写入被浏览器限制，请直接选中下方文案手动复制'
            : 'Clipboard write blocked — please select and copy the text manually.',
        );
      }
    }
  }, [shareCopy, zh]);

  const savedUsd = stats && Number.isFinite(stats.estimated_saved_usd)
    ? Math.max(0, stats.estimated_saved_usd)
    : 0;
  const savedCny = stats && Number.isFinite(stats.estimated_saved_cny)
    ? Math.max(0, stats.estimated_saved_cny)
    : savedUsd * 7.2;
  const efficiencyPct = stats && Number.isFinite(stats.waste_zero_efficiency_pct)
    ? Math.min(100, Math.max(0, stats.waste_zero_efficiency_pct))
    : 0;
  const freeGpuHours = stats && Number.isFinite(stats.free_gpu_hours_harvested)
    ? Math.max(0, stats.free_gpu_hours_harvested)
    : 0;
  const totalGpuHours = stats && Number.isFinite(stats.total_gpu_hours)
    ? Math.max(0, stats.total_gpu_hours)
    : 0;

  return (
    <div className="space-y-6 pb-10">
      <PageHeader
        title={zh ? '算力省钱账单与 2× 晒图工作台' : 'Compute Savings Dashboard & Poster Studio'}
        subtitle={
          zh
            ? '真实核算你的云端显卡运行小时、自动闲置关机省下的 CU 与跨号断点续跑成果；所有数字源自实测统计，海报 100% 脱敏零个人邮箱。'
            : 'Verified cloud GPU runtime, idle-shutdown savings, and cross-account checkpoint relays — with 100% PII-free 2× Retina poster export.'
        }
        badge={
          <StatusPill tone="emerald" icon={<ShieldCheck />}>
            {zh ? '零隐私脱敏校验' : '100% Zero-PII Verified'}
          </StatusPill>
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <ActionButton
              size="sm"
              variant="secondary"
              icon={<RefreshCw className={loading ? 'animate-spin' : undefined} />}
              onClick={() => void refreshReceipt()}
            >
              {zh ? '刷新实测统计' : 'Refresh Stats'}
            </ActionButton>
            <ActionButton
              size="sm"
              variant="secondary"
              icon={<Rocket />}
              onClick={() => nav('/apps')}
            >
              {zh ? '前往 AI 应用商店' : 'Open AI App Store'}
            </ActionButton>
          </div>
        }
      />

      {/* Live Load Error State — Never Show Fake ¥2,466 Fallback */}
      {loadError && (
        <LayeredCard
          padding="md"
          style={{
            borderColor: 'var(--coral-border)',
            backgroundColor: 'var(--coral-soft)',
          }}
        >
          <div
            role="alert"
            className="flex flex-col sm:flex-row sm:items-center justify-between gap-4"
            style={{ color: 'var(--coral-text)' }}
          >
            <div className="flex items-start gap-3">
              <AlertCircle className="w-5 h-5 shrink-0 mt-0.5" />
              <div className="space-y-1">
                <div className="text-[14px] font-bold">
                  {zh ? '暂时无法获取真实算力省钱账单' : 'Could Not Load Verified Savings Data'}
                </div>
                <p className="text-[12.5px] opacity-90">{loadError}</p>
                <p className="text-[11.5px] opacity-80">
                  {zh
                    ? '为保证账单数据真实可信，未获取服务端实测结果前已锁定 PNG 导出功能。'
                    : 'PNG export stays disabled until verified telemetry loads from the server.'}
                </p>
              </div>
            </div>
            <ActionButton
              size="sm"
              variant="secondary"
              icon={<RefreshCw />}
              onClick={() => void refreshReceipt()}
            >
              {zh ? '重试拉取' : 'Retry'}
            </ActionButton>
          </div>
        </LayeredCard>
      )}

      {/* Top 4 Honest KPI Bento Cards */}
      {loading && !stats ? (
        <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
          {[0, 1, 2, 3].map((i) => (
            <LayeredCard key={i} padding="md">
              <Skeleton className="h-3.5 w-28" />
              <Skeleton className="mt-3 h-8 w-32" />
              <Skeleton className="mt-3 h-3 w-44 max-w-full" />
            </LayeredCard>
          ))}
        </div>
      ) : stats ? (
        <div className="ui-stagger grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-4">
          <LayeredCard
            padding="md"
            staggerIndex={0}
            style={{
              borderColor: 'var(--amber-border)',
              backgroundImage:
                'linear-gradient(135deg, var(--amber-soft) 0%, var(--surface-2) 68%)',
            }}
          >
            <div className="flex items-center justify-between gap-2 text-[12px] font-medium text-fg-secondary">
              <span>{zh ? '等效云端租卡价值' : 'Equivalent Cloud GPU Value'}</span>
              <Award className="w-4 h-4 shrink-0" style={{ color: 'var(--amber-text)' }} />
            </div>
            <div
              className="mt-2 ch-num text-[28px] font-extrabold tracking-tight leading-none"
              style={{ color: 'var(--amber-text)' }}
            >
              {zh
                ? `¥${Math.round(savedCny).toLocaleString('en-US')}`
                : `$${savedUsd.toFixed(0)}`}
            </div>
            <div
              className="mt-2 ch-num text-[11.5px] leading-relaxed text-fg-muted whitespace-nowrap truncate"
              title={
                zh
                  ? `≈ $${savedUsd.toFixed(2)} USD · 按 Vast.ai / RunPod 2026 均价估算`
                  : `≈ ¥${Math.round(savedCny).toLocaleString('en-US')} · Vast.ai / RunPod 2026 avg`
              }
            >
              {zh
                ? `≈ $${Math.round(savedUsd).toLocaleString('en-US')} · Vast/RunPod 均价`
                : `≈ ¥${Math.round(savedCny).toLocaleString('en-US')} · Vast/RunPod avg`}
            </div>
          </LayeredCard>

          <LayeredCard padding="md" staggerIndex={1}>
            <div className="flex items-center justify-between gap-2 text-[12px] font-medium text-fg-secondary">
              <span>{zh ? '累计免费显卡运行时长' : 'Free GPU Hours Harvested'}</span>
              <Zap className="w-4 h-4 shrink-0" style={{ color: 'var(--emerald-text)' }} />
            </div>
            <div
              className="mt-2 ch-num text-[28px] font-extrabold tracking-tight leading-none"
              style={{ color: 'var(--emerald-text)' }}
            >
              {freeGpuHours.toFixed(1)}h
            </div>
            <div className="mt-2 ch-num text-[11.5px] leading-relaxed text-fg-muted whitespace-nowrap truncate">
              {zh
                ? `总 GPU ${totalGpuHours.toFixed(1)}h · 折合 G4 ${stats.g4_equivalent_hours.toFixed(1)}h`
                : `${totalGpuHours.toFixed(1)}h total · ${stats.g4_equivalent_hours.toFixed(1)}h G4`}
            </div>
          </LayeredCard>

          <LayeredCard padding="md" staggerIndex={2}>
            <div className="flex items-center justify-between gap-2 text-[12px] font-medium text-fg-secondary">
              <span>{zh ? '防浪费实测利用率' : 'Measured Waste-Zero Efficiency'}</span>
              <Gauge className="w-4 h-4 shrink-0 text-fg-muted" />
            </div>
            <div className="mt-2 ch-num text-[28px] font-extrabold tracking-tight leading-none text-fg">
              {efficiencyPct.toFixed(1)}%
            </div>
            <div className="mt-2 ch-num text-[11.5px] leading-relaxed text-fg-muted whitespace-nowrap truncate">
              {zh
                ? `${stats.auto_teardowns} 次闲置关机 · ${stats.zombie_aborts} 次卡死拦截`
                : `${stats.auto_teardowns} idle stops · ${stats.zombie_aborts} aborts`}
            </div>
          </LayeredCard>

          <LayeredCard padding="md" staggerIndex={3}>
            <div className="flex items-center justify-between gap-2 text-[12px] font-medium text-fg-secondary">
              <span>{zh ? '跨号断点自动续跑' : 'Auto-Resume Relays'}</span>
              <ShieldCheck className="w-4 h-4 shrink-0" style={{ color: 'var(--emerald-text)' }} />
            </div>
            <div className="mt-2 flex items-baseline gap-1 text-[28px] font-extrabold tracking-tight leading-none text-fg">
              <span className="ch-num">{stats.auto_resume_relays}</span>
              {zh && <span className="text-[18px] font-bold">次</span>}
            </div>
            <div className="mt-2 ch-num text-[11.5px] leading-relaxed text-fg-muted whitespace-nowrap truncate">
              {zh
                ? `${stats.total_accounts} 个账号 · 省下 ${stats.saved_idle_waste_cu.toFixed(1)} CU`
                : `${stats.total_accounts} accounts · ${stats.saved_idle_waste_cu.toFixed(1)} CU saved`}
            </div>
          </LayeredCard>
        </div>
      ) : null}

      {/* Main 2-Column 2× Retina Poster Studio & Breakdown */}
      <div className="grid grid-cols-1 xl:grid-cols-12 gap-5 items-start">
        {/* Left 7 cols: Live 2× Retina Poster Canvas Studio */}
        <LayeredCard
          padding="md"
          className="xl:col-span-7 space-y-4"
          title={zh ? '🎨 2× 视网膜高清省钱海报工作台' : '🎨 2× Retina Poster Studio'}
          subtitle={
            zh
              ? '支持竖版（小红书 / 朋友圈）与横版（社区 / X / README）双版式实时预览，纯前端 Canvas 2× 直出'
              : 'Switch between vertical social posters and widescreen cards — rendered locally on 2× High-DPI Canvas.'
          }
          headerRight={
            <StatusPill tone="amber" size="xs" icon={<Sparkles />}>
              {zh ? '2× 高清直出' : '2× Retina'}
            </StatusPill>
          }
        >
          <div className="space-y-4">
            <div className="flex flex-wrap items-center gap-2.5">
              <SegmentedTabs<PosterTheme>
                size="sm"
                value={posterTheme}
                onChange={setPosterTheme}
                aria-label={zh ? '海报配色主题' : 'Poster theme'}
                options={[
                  {
                    value: 'obsidian-gold',
                    icon: <Moon />,
                    label: zh ? '暗黑鎏金' : 'Obsidian Gold',
                  },
                  {
                    value: 'warm-alabaster',
                    icon: <Sun />,
                    label: zh ? '暖白雅致' : 'Warm Alabaster',
                  },
                ]}
              />
              <SegmentedTabs<PosterLayout>
                size="sm"
                value={layout}
                onChange={setLayout}
                aria-label={zh ? '海报版式' : 'Poster layout'}
                options={[
                  {
                    value: 'vertical',
                    icon: <Smartphone />,
                    label: zh ? '竖版海报 (小红书/朋友圈)' : 'Vertical Poster',
                  },
                  {
                    value: 'landscape',
                    icon: <Monitor />,
                    label: zh ? '横版宽屏 (社区/X)' : 'Landscape Card',
                  },
                ]}
              />
            </div>

            {stats ? (
              <SavingsReceiptCanvas
                stats={stats}
                posterTheme={posterTheme}
                layout={layout}
                zh={zh}
                canvasRef={canvasRef}
                maxWidthVertical={400}
                maxWidthLandscape={680}
              />
            ) : loading ? (
              <div className="rounded-2xl border border-line bg-surface-1 p-14 flex flex-col items-center justify-center gap-3 text-fg-muted">
                <Loader2 className="w-7 h-7 animate-spin" style={{ color: 'var(--amber)' }} />
                <span className="text-[13px]">
                  {zh ? '正在加载实测统计数据并绘制海报…' : 'Loading verified stats for poster…'}
                </span>
              </div>
            ) : (
              <div className="rounded-2xl border border-dashed border-line-strong bg-surface-1/60 p-12 text-center space-y-2 text-fg-muted">
                <Award className="w-8 h-8 mx-auto opacity-50" />
                <div className="text-[13.5px] font-medium text-fg-secondary">
                  {zh ? '等待连接服务获取真实统计数据' : 'Waiting for verified server telemetry'}
                </div>
                <p className="text-[12px]">
                  {zh
                    ? '恢复连接后点击上方「刷新实测统计」即可生成 2× 高清海报。'
                    : 'Click Refresh Stats once the backend daemon is reachable.'}
                </p>
              </div>
            )}
          </div>
        </LayeredCard>

        {/* Right 5 cols: Export Actions, Share Caption & Real Telemetry Breakdown */}
        <div className="xl:col-span-5 space-y-4">
          <LayeredCard
            padding="md"
            title={zh ? '📥 导出高清海报与晒单文案' : '📥 Export Poster & Share Copy'}
            subtitle={
              zh
                ? '使用异步 Blob 编码直出无损 PNG，绝不暴露个人邮箱、IP 或令牌'
                : 'Async lossless PNG blob export with zero personal emails, IPs, or tokens.'
            }
          >
            <div className="space-y-4">
              {exportError && (
                <div
                  role="alert"
                  className="flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[12.5px]"
                  style={{
                    backgroundColor: 'var(--coral-soft)',
                    borderColor: 'var(--coral-border)',
                    color: 'var(--coral-text)',
                  }}
                >
                  <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
                  <span>{exportError}</span>
                </div>
              )}

              <div className="flex flex-col sm:flex-row gap-2.5">
                <ActionButton
                  size="md"
                  variant="primary"
                  fullWidth
                  disabled={!canExport}
                  loading={downloading}
                  icon={<Download />}
                  onClick={() => void handleDownloadPng()}
                >
                  {zh ? '保存高清海报 (.png)' : 'Download HD Poster (.png)'}
                </ActionButton>
                <ActionButton
                  size="md"
                  variant="secondary"
                  fullWidth
                  disabled={!canExport || !shareCopy}
                  icon={copiedText ? <Check /> : <Copy />}
                  onClick={() => void handleCopyShareText()}
                  style={copiedText ? { color: 'var(--emerald-text)' } : undefined}
                >
                  {copiedText
                    ? zh
                      ? '✓ 已复制文案'
                      : '✓ Copied'
                    : zh
                      ? '复制晒单文案'
                      : 'Copy Share Copy'}
                </ActionButton>
              </div>

              <InsetWell
                padding="sm"
                label={zh ? '📝 配套晒单文案预览' : '📝 Share Copy Preview'}
                headerRight={
                  <StatusPill size="xs" tone="emerald">
                    0 PII
                  </StatusPill>
                }
              >
                <p className="text-[12.5px] leading-relaxed text-fg-secondary select-all">
                  {shareCopy ||
                    (zh
                      ? '加载实测统计后将自动生成脱敏晒单文案。'
                      : 'Sanitized share caption will appear once verified stats load.')}
                </p>
              </InsetWell>

              <div className="flex items-center gap-2 text-[12px] text-fg-secondary px-1">
                <ShieldCheck className="w-4 h-4 shrink-0" style={{ color: 'var(--emerald)' }} />
                <span>
                  {zh
                    ? '所有邮箱与密钥已在渲染前自动脱敏过滤，可直接发小红书 / 朋友圈 / GitHub。'
                    : 'All emails and secrets are scrubbed before canvas rendering.'}
                </span>
              </div>
            </div>
          </LayeredCard>

          {/* Verified Usage Breakdown */}
          {stats && (
            <LayeredCard
              padding="md"
              title={zh ? '📊 账单核算明细' : '📊 Verified Savings Breakdown'}
            >
              <dl className="space-y-3 text-[12.5px]">
                <div className="flex items-center justify-between gap-3 pb-2.5 border-b border-line-faint">
                  <dt className="text-fg-secondary flex items-center gap-1.5">
                    <Users className="w-3.5 h-3.5 text-fg-muted" />
                    {zh ? '已接入平台' : 'Connected Platforms'}
                  </dt>
                  <dd className="font-semibold text-fg text-right">
                    {stats.providers_connected && stats.providers_connected.length > 0
                      ? stats.providers_connected.map(scrubSavingsPII).join(' · ')
                      : '—'}
                  </dd>
                </div>

                <div className="flex items-center justify-between gap-3 pb-2.5 border-b border-line-faint">
                  <dt className="text-fg-secondary flex items-center gap-1.5">
                    <Cpu className="w-3.5 h-3.5 text-fg-muted" />
                    {zh ? '折合 96GB G4 等效时长' : 'G4 96GB Equivalent Hours'}
                  </dt>
                  <dd className="ch-num font-semibold text-fg">
                    {stats.g4_equivalent_hours.toFixed(1)} h
                  </dd>
                </div>

                <div className="flex items-center justify-between gap-3 pb-2.5 border-b border-line-faint">
                  <dt className="text-fg-secondary flex items-center gap-1.5">
                    <Flame className="w-3.5 h-3.5 text-fg-muted" />
                    {zh ? '累计消耗 / 闲置节省 CU' : 'CU Burned / Idle Waste Saved'}
                  </dt>
                  <dd className="ch-num font-semibold text-fg">
                    {stats.total_cu_burned.toFixed(1)} /{' '}
                    <span style={{ color: 'var(--emerald-text)' }}>
                      +{stats.saved_idle_waste_cu.toFixed(1)} CU
                    </span>
                  </dd>
                </div>

                <div className="flex items-center justify-between gap-3 pb-2.5 border-b border-line-faint">
                  <dt className="text-fg-secondary">
                    {zh ? '租卡单价基准' : 'Rental Rate Benchmark'}
                  </dt>
                  <dd className="ch-num text-[11.5px] font-medium text-fg-muted text-right">
                    {zh
                      ? 'Vast.ai / RunPod 均价 (大卡 $2.2 · L4 $0.6 · T4 $0.25/h)'
                      : 'Vast.ai / RunPod avg ($2.2 / $0.6 / $0.25/h)'}
                  </dd>
                </div>

                <div className="space-y-1.5">
                  <dt className="text-fg-secondary">{zh ? '常用云端显卡' : 'Top GPUs Used'}</dt>
                  <dd className="flex flex-wrap gap-1.5">
                    {stats.top_gpus_used && stats.top_gpus_used.length > 0 ? (
                      stats.top_gpus_used.map((gpu) => (
                        <StatusPill key={gpu} size="xs" tone="neutral" mono>
                          {scrubSavingsPII(gpu)}
                        </StatusPill>
                      ))
                    ) : (
                      <span className="text-[12px] text-fg-muted">
                        {zh ? '尚未产生 GPU 运行记录' : 'No GPU sessions recorded yet'}
                      </span>
                    )}
                  </dd>
                </div>
              </dl>

              <div className="mt-4 pt-3 border-t border-line-faint flex items-center justify-between gap-2">
                <span className="text-[11.5px] text-fg-muted">
                  {zh ? '想接入更多免费账号扩大池子？' : 'Want to expand your free compute pool?'}
                </span>
                <ActionButton size="xs" variant="secondary" onClick={() => nav('/pool')}>
                  {zh ? '管理账号池 →' : 'Account Pool →'}
                </ActionButton>
              </div>
            </LayeredCard>
          )}
        </div>
      </div>
    </div>
  );
}

export default SavingsPage;
