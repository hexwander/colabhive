import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Activity,
  ArrowRight,
  ArrowUpRight,
  Check,
  Cloud,
  Copy,
  Download,
  ExternalLink,
  Flame,
  Gauge,
  Gift,
  Globe,
  Laptop,
  Layers,
  Lock,
  MessageCircle,
  Moon,
  Network,
  Rocket,
  RotateCcw,
  ShieldCheck,
  Sparkles,
  Star,
  Sun,
  Zap,
} from 'lucide-react';
import {
  DEFAULT_HARDWARE_CATALOG,
  type AcceleratorType,
  type AgentClientID,
  type ToolTraceEvent,
} from '../types';
import { api, formatCU, formatHours, useColabHiveEvents } from '../lib/api';
import {
  DEMO_STATE_CHANGED_EVENT,
  getDemoSnapshot,
  simulateDemoFailoverEvent,
} from '../lib/demoSiteAdapter';
import { useI18n } from '../i18n';
import { getObservatoryHomePath, navigate } from '../components/layout/navigation';
import { formatDurationMs } from '../components/terminal/workbenchMockData';
import {
  CircularQuotaRing,
  cn,
  SegmentedQuotaBar,
  StatusPill,
  Switch,
} from '../components/ui/primitives';
import { BrandMark } from '../components/ui/BrandLogo';
import { AgentBrandIcon } from '../components/ui/AgentBrandIcon';
import { TrayPopoverPage, type TrayTab } from './TrayPopoverPage';

const GITHUB_REPO_URL = 'https://github.com/hexwander/colabhive';
const GITHUB_RELEASES_URL = 'https://github.com/hexwander/colabhive/releases/latest';
const GITHUB_ARCH_URL = 'https://github.com/hexwander/colabhive/tree/main/docs/architecture';
const GITHUB_SDK_URL = 'https://github.com/hexwander/colabhive/tree/main/sdk/python';

type InstallTabKey = 'windows' | 'unix' | 'docker' | 'sdk';
type SandboxStageKey = 'tray' | 'pool' | 'failover';
type FailoverPhase = 'normal' | 'throttled' | 'rerouted';

const IDE_BADGES: {
  id: AgentClientID;
  name: string;
  mono: string;
  proto: string;
  toneBg: string;
  toneFg: string;
}[] = [
  {
    id: 'claude-code',
    name: 'Claude Code',
    mono: 'CC',
    proto: 'Streamable HTTP',
    toneBg: 'var(--coral-soft)',
    toneFg: 'var(--coral-text)',
  },
  {
    id: 'codex',
    name: 'Codex',
    mono: 'CX',
    proto: 'MCP stdio',
    toneBg: 'var(--emerald-soft)',
    toneFg: 'var(--emerald-text)',
  },
  {
    id: 'antigravity',
    name: 'Antigravity',
    mono: 'AG',
    proto: 'Streamable HTTP',
    toneBg: 'var(--neutral-soft)',
    toneFg: 'var(--text-primary)',
  },
  {
    id: 'cursor',
    name: 'Cursor',
    mono: 'CU',
    proto: 'Streamable HTTP',
    toneBg: 'var(--sky-soft)',
    toneFg: 'var(--sky-text)',
  },
  {
    id: 'gemini-cli',
    name: 'Gemini CLI',
    mono: 'GM',
    proto: 'Streamable HTTP',
    toneBg: 'var(--sky-soft)',
    toneFg: 'var(--sky-text)',
  },
  {
    id: 'windsurf',
    name: 'Windsurf',
    mono: 'WS',
    proto: 'Streamable HTTP',
    toneBg: 'var(--emerald-soft)',
    toneFg: 'var(--emerald-text)',
  },
  {
    id: 'opencode',
    name: 'OpenCode',
    mono: 'OC',
    proto: 'Streamable HTTP',
    toneBg: 'var(--amber-soft)',
    toneFg: 'var(--amber-text)',
  },
  {
    id: 'claude-desktop',
    name: 'Claude Desktop',
    mono: 'CD',
    proto: 'MCP stdio Bridge',
    toneBg: 'var(--coral-soft)',
    toneFg: 'var(--coral-text)',
  },
];

function LandingBrandEmblem({ size = 30 }: { size?: number }) {
  return <BrandMark size={size} />;
}

export function LandingPage() {
  const { locale, toggleLocale, theme, toggleTheme } = useI18n();
  const zh = locale === 'zh-CN';
  const consolePath = getObservatoryHomePath();

  // Live reactive snapshot synchronized with demoSiteAdapter & 2.5s synthetic ticks
  const [snap, setSnap] = useState(() => getDemoSnapshot());
  const refreshSnap = useCallback(() => {
    setSnap(getDemoSnapshot());
  }, []);

  useEffect(() => {
    window.addEventListener(DEMO_STATE_CHANGED_EVENT, refreshSnap);
    return () => window.removeEventListener(DEMO_STATE_CHANGED_EVENT, refreshSnap);
  }, [refreshSnap]);

  // Subscribe to synthetic 2.5s telemetry ticks while on LandingPage so GPU% & shards animate live
  useColabHiveEvents(() => {
    refreshSnap();
  }, true);

  // Install Bar State
  const [installTab, setInstallTab] = useState<InstallTabKey>('windows');
  const [copiedInstall, setCopiedInstall] = useState(false);

  // Embedded Interactive Live Sandbox State
  const [sandboxTab, setSandboxTab] = useState<SandboxStageKey>('tray');
  const [trayTab, setTrayTab] = useState<TrayTab>('agents');
  const [togglingIde, setTogglingIde] = useState<AgentClientID | null>(null);

  // Tab 2: Shards=4 Parallel Speedup Calculator State
  const [calcGpu, setCalcGpu] = useState<AcceleratorType>('G4');
  const [calcShards, setCalcShards] = useState<number>(4);
  const [calcWorkHours, setCalcWorkHours] = useState<number>(4);

  // Tab 3: Cross-Account Failover Simulator State
  const [failoverPhase, setFailoverPhase] = useState<FailoverPhase>('normal');
  const [lastFailoverTrace, setLastFailoverTrace] = useState<ToolTraceEvent | null>(null);

  const installOptions: Record<
    InstallTabKey,
    { label: string; prompt: string; cmd: string; hint: string }
  > = useMemo(
    () => ({
      windows: {
        label: zh ? 'Windows 桌面托盘版' : 'Windows Tray App',
        prompt: 'PS>',
        cmd: 'irm https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.ps1 | iex',
        hint: zh
          ? '自动校验 SHA-256 并安装无标题栏原生托盘端 colabhive-desktop.exe + colab CLI，静默驻留任务栏'
          : 'Verifies SHA-256 & installs frameless Win32 tray app colabhive-desktop.exe + colab CLI to ~/.colabhive',
      },
      unix: {
        label: zh ? 'macOS (Apple Silicon) / Linux' : 'macOS (M-Series) / Linux',
        prompt: '$',
        cmd: 'curl -fsSL https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.sh | sh',
        hint: zh
          ? 'macOS 原生支持 Apple Silicon (darwin/arm64) 与 Intel 菜单栏 ColabHive.app 弹窗；Linux 单文件零依赖即开即用'
          : 'Installs native macOS Apple Silicon / Intel Menu Bar ColabHive.app + static Go colabhive / colab CLI',
      },
      docker: {
        label: zh ? 'Docker 云端团队版' : 'Docker Cloud Server',
        prompt: '$',
        cmd: 'docker compose -f docker-compose.cloud.yml --env-file .env.cloud --profile caddy up -d',
        hint: zh
          ? '内置 Caddy 自动 HTTPS 证书、流式 MCP 反代 (flush_interval -1)、管理员口令门禁与好友共享页'
          : 'Hardened cloud server with Caddy auto-HTTPS, zero-buffer MCP streaming, Admin Gate & Guest Share Portal',
      },
      sdk: {
        label: 'Python SDK',
        prompt: '$',
        cmd: 'pip install colabhive-sdk && colabhive status',
        hint: zh
          ? '支持 Python 分布式多卡评测池 HivePool.map_shards() 及 30 秒命令行算力捐赠 colabhive donate'
          : 'Programmatic multi-GPU shard runner HivePool.map_shards() & 1-command CLI compute donation',
      },
    }),
    [zh],
  );

  const copyTimerRef = useRef<number | null>(null);
  const failoverTimerRef = useRef<number | null>(null);

  useEffect(
    () => () => {
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
      if (failoverTimerRef.current !== null) window.clearTimeout(failoverTimerRef.current);
    },
    [],
  );

  const handleCopyInstall = useCallback(async () => {
    const text = installOptions[installTab].cmd;
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      const ta = document.createElement('textarea');
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
    }
    setCopiedInstall(true);
    if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
    copyTimerRef.current = window.setTimeout(() => setCopiedInstall(false), 1800);
  }, [installOptions, installTab]);

  const scrollToSection = useCallback((id: string) => {
    const el = document.getElementById(id);
    if (el) {
      el.scrollIntoView({ behavior: 'smooth', block: 'start' });
    }
  }, []);

  const handleToggleIdeLive = useCallback(
    async (clientId: AgentClientID) => {
      setTogglingIde(clientId);
      try {
        const current = snap.clients.find((c) => c.client_id === clientId);
        if (current?.injected) {
          await api.removeAgentConfig(clientId);
        } else {
          await api.injectAgentConfig({
            client_id: clientId,
            server_base_url: 'http://127.0.0.1:7860',
            transport: clientId === 'claude-desktop' ? 'stdio' : 'http',
          });
        }
        refreshSnap();
      } finally {
        setTogglingIde(null);
      }
    },
    [refreshSnap, snap.clients],
  );

  const handleTriggerFailover = useCallback(() => {
    setFailoverPhase('throttled');
    if (failoverTimerRef.current !== null) window.clearTimeout(failoverTimerRef.current);
    failoverTimerRef.current = window.setTimeout(() => {
      const tr = simulateDemoFailoverEvent();
      setLastFailoverTrace(tr);
      setFailoverPhase('rerouted');
      refreshSnap();
      failoverTimerRef.current = null;
    }, 520);
  }, [refreshSnap]);

  const handleResetFailover = useCallback(() => {
    setFailoverPhase('normal');
    setLastFailoverTrace(null);
  }, []);

  const connectedAgentCount = useMemo(
    () => snap.clients.filter((c) => c.injected).length,
    [snap.clients],
  );
  const claudeCodeConnected = useMemo(
    () => Boolean(snap.clients.find((c) => c.client_id === 'claude-code')?.injected),
    [snap.clients],
  );
  const activeSessions = useMemo(
    () => snap.sessions.filter((s) => s.status !== 'STOPPED' && s.status !== 'LOST'),
    [snap.sessions],
  );
  const liveExperiment = snap.experiments[0];

  // Tab 2 Speedup Maths
  const calcSpec = useMemo(
    () =>
      DEFAULT_HARDWARE_CATALOG.find((h) => h.accelerator === calcGpu) ??
      DEFAULT_HARDWARE_CATALOG[0],
    [calcGpu],
  );
  const calcTotalCu = calcSpec.default_burn_rate_cu_hr * calcWorkHours;
  const calcParallelWallHrs = calcWorkHours / Math.max(1, calcShards);

  return (
    <div
      className="min-h-dvh flex flex-col text-fg selection:bg-[var(--emerald-soft)]"
      style={{
        backgroundColor: 'var(--surface-0)',
        backgroundImage:
          theme === 'dark'
            ? 'radial-gradient(1100px 520px at 50% -80px, rgba(34, 197, 94, 0.08), transparent 65%), radial-gradient(850px 420px at 88% 120px, rgba(245, 158, 11, 0.06), transparent 60%)'
            : 'radial-gradient(1100px 540px at 50% -90px, rgba(22, 163, 74, 0.07), transparent 65%), radial-gradient(850px 420px at 88% 120px, rgba(217, 119, 6, 0.055), transparent 62%)',
      }}
    >
      {/* ====================================================================
       * 1. Top Floating Glass Header
       * ================================================================== */}
      <div className="sticky top-0 z-50 px-3 sm:px-6 pt-3 pointer-events-none">
        <header
          className="pointer-events-auto max-w-[1260px] mx-auto h-14 px-3.5 sm:px-5 rounded-2xl border flex items-center justify-between gap-3"
          style={{
            backgroundColor:
              theme === 'dark' ? 'rgba(23, 23, 22, 0.84)' : 'rgba(255, 255, 255, 0.84)',
            borderColor: 'var(--glass-card-border)',
            boxShadow: 'var(--shadow-card)',
            backdropFilter: 'blur(18px) saturate(150%)',
            WebkitBackdropFilter: 'blur(18px) saturate(150%)',
          }}
        >
          <div className="flex items-center gap-2.5 min-w-0">
            <LandingBrandEmblem size={28} />
            <span
              className="text-[16px] font-bold tracking-tight text-fg"
              style={{ fontFamily: "'Bricolage Grotesque', var(--font-sans)" }}
            >
              ColabHive
            </span>
            <span
              className="hidden sm:inline-flex ch-num px-1.5 py-0.5 rounded-md text-[10.5px] font-semibold"
              style={{
                backgroundColor: 'var(--emerald-soft)',
                color: 'var(--emerald-text)',
              }}
            >
              v1.1
            </span>
          </div>

          <nav
            aria-label="Product Sections"
            className="hidden lg:flex items-center gap-1 text-[13px] font-medium text-fg-secondary"
          >
            {[
              { id: 'v11-pillars', label: zh ? 'v1.1 四大新玩法' : 'v1.1 Highlights' },
              { id: 'sandbox', label: zh ? '交互试玩' : 'Live Sandbox' },
              { id: 'architecture', label: zh ? '核心架构' : 'Architecture' },
              { id: 'agents', label: zh ? 'Agent 接入' : 'Agent MCP' },
              { id: 'install', label: zh ? '快速安装' : 'Quick Install' },
            ].map((item) => (
              <button
                key={item.id}
                type="button"
                onClick={() => scrollToSection(item.id)}
                className="px-3 py-1.5 rounded-full hover:text-fg hover:bg-[var(--hover-fill)] transition-colors cursor-pointer bg-transparent border-0"
              >
                {item.label}
              </button>
            ))}
          </nav>

          <div className="flex items-center gap-1.5 shrink-0">
            <button
              type="button"
              onClick={toggleLocale}
              title={zh ? 'Switch to English' : '切换为中文'}
              className="ui-btn ui-btn-ghost ui-btn-xs ch-num font-semibold"
            >
              <Globe className="w-3.5 h-3.5" />
              <span>{zh ? '中 / EN' : 'EN / 中'}</span>
            </button>

            <button
              type="button"
              onClick={toggleTheme}
              aria-label={zh ? '切换深浅色主题' : 'Toggle theme'}
              title={zh ? '切换浅色 / 深色工作室主题' : 'Switch Light / Dark studio theme'}
              className="ui-icon-btn"
            >
              {theme === 'dark' ? <Moon className="w-4 h-4" /> : <Sun className="w-4 h-4" />}
            </button>

            <a
              href={GITHUB_REPO_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="ui-btn ui-btn-secondary hidden sm:inline-flex"
            >
              <Star className="w-3.5 h-3.5 text-[var(--amber)]" />
              <span>GitHub ★</span>
            </a>

            <button
              type="button"
              onClick={() => navigate(consolePath)}
              className="ui-btn ui-btn-primary ui-btn-md"
            >
              <span>{zh ? '全屏体验控制台' : 'Full Live Console'}</span>
              <ArrowRight className="w-3.5 h-3.5" />
            </button>
          </div>
        </header>
      </div>

      {/* ====================================================================
       * 2. Editorial Hero + One-Click Copy Install Bar
       * ================================================================== */}
      <main className="flex-1">
        <section className="max-w-[1260px] mx-auto px-4 sm:px-6 pt-10 sm:pt-14 pb-8">
          <div className="max-w-[940px] mx-auto text-center space-y-5">
            <div className="inline-flex flex-wrap items-center justify-center gap-2">
              <StatusPill tone="emerald" size="md" dot pulse>
                {zh
                  ? 'Apache-2.0 开源 · 纯 Go 单文件 · 在线零后端实机沙盒已就绪'
                  : 'Apache-2.0 Open Source · Single Go Binary · Live Interactive Sandbox'}
              </StatusPill>
            </div>

            <h1
              className="text-[30px] sm:text-[44px] lg:text-[52px] font-bold tracking-[-0.03em] leading-[1.13] text-fg text-balance"
              style={{
                fontFamily: "'Bricolage Grotesque', 'Plus Jakarta Sans', 'Noto Sans SC', sans-serif",
              }}
            >
              {zh ? (
                <>
                  把散落的 Google Colab 多账号<span className="whitespace-nowrap">变成你的</span>
                  <span
                    className="inline-block px-2 mx-0.5 rounded-xl"
                    style={{
                      backgroundColor: 'var(--emerald-soft)',
                      color: 'var(--emerald-text)',
                    }}
                  >
                    私人免费 AI 超级主机
                  </span>
                </>
              ) : (
                <>
                  Turn Pooled Google Colab GPUs into Your{' '}
                  <span
                    className="inline-block px-2.5 rounded-xl"
                    style={{
                      backgroundColor: 'var(--emerald-soft)',
                      color: 'var(--emerald-text)',
                    }}
                  >
                    Private Free AI Supercomputer
                  </span>
                </>
              )}
            </h1>

            <p className="max-w-[820px] mx-auto text-[15px] sm:text-[16.5px] leading-relaxed text-fg-secondary text-balance">
              {zh
                ? '把多个 Google Colab 免费与 Pro/Ultra 账号汇成一池：内置私人开源大模型网关 (/v1)、一键 ComfyUI / WanGP / JupyterLab 应用商店、掉线自动换号接着跑，同时支持 8 款 AI 编程助手 MCP 路由与 4× 多卡并发提速。'
                : 'Pool multiple Google Colab Free & Pro+/Ultra accounts into one local hub: Private Open-Weight LLM Gateway (/v1), 1-Click AI App Store, Auto-Resume Relay, Savings Poster, and 8-IDE MCP Routing.'}
            </p>

            {/* Primary Hero CTAs */}
            <div className="pt-1 flex flex-wrap items-center justify-center gap-3">
              <button
                type="button"
                onClick={() => navigate('/apps/llm')}
                className="h-11 px-6 rounded-full text-[14px] font-semibold inline-flex items-center gap-2 cursor-pointer transition-transform active:scale-[0.98]"
                style={{
                  backgroundColor: 'var(--accent)',
                  color: 'var(--accent-fg)',
                  boxShadow: 'var(--shadow-accent)',
                }}
              >
                <Zap className="w-4 h-4" />
                <span>{zh ? '一键部署无审查大模型 →' : 'Deploy Uncensored LLM →'}</span>
              </button>

              <button
                type="button"
                onClick={() => navigate('/apps')}
                className="h-11 px-5 rounded-full text-[13.5px] font-semibold border inline-flex items-center gap-2 cursor-pointer transition-colors hover:border-line-strong"
                style={{
                  backgroundColor: 'var(--surface-2)',
                  borderColor: 'var(--control-border)',
                  color: 'var(--text-primary)',
                  boxShadow: 'var(--shadow-control)',
                }}
              >
                <Rocket className="w-4 h-4 text-[var(--amber)]" />
                <span>{zh ? 'WanGP 视频 & AI 应用商店' : 'WanGP Video & AI App Store'}</span>
              </button>

              <button
                type="button"
                onClick={() => navigate(consolePath)}
                className="h-11 px-5 rounded-full text-[13.5px] font-semibold border inline-flex items-center gap-2 cursor-pointer transition-colors hover:border-line-strong"
                style={{
                  backgroundColor: 'var(--surface-2)',
                  borderColor: 'var(--control-border)',
                  color: 'var(--text-primary)',
                  boxShadow: 'var(--shadow-control)',
                }}
              >
                <Gauge className="w-4 h-4 text-[var(--emerald)]" />
                <span>{zh ? '进入全屏算力控制台' : 'Full Live Console'}</span>
              </button>

              <a
                href={GITHUB_RELEASES_URL}
                target="_blank"
                rel="noopener noreferrer"
                className="h-11 px-5 rounded-full text-[13.5px] font-semibold border inline-flex items-center gap-2 no-underline transition-colors hover:border-line-strong"
                style={{
                  backgroundColor: 'transparent',
                  borderColor: 'var(--border-strong)',
                  color: 'var(--text-secondary)',
                }}
              >
                <Download className="w-4 h-4" />
                <span>{zh ? '下载桌面托盘版 / CLI' : 'Download Release'}</span>
              </a>
            </div>
          </div>

          {/* 1-Click Tabbed Copyable Install Command Bar */}
          <div
            id="install"
            className="mt-8 max-w-[880px] mx-auto rounded-[20px] border p-2 sm:p-2.5 scroll-mt-24"
            style={{
              backgroundColor: 'var(--surface-2)',
              borderColor: 'var(--glass-card-border)',
              boxShadow: 'var(--shadow-card)',
            }}
          >
            <div className="flex flex-wrap items-center justify-between gap-1.5 pb-2 px-1">
              <div className="flex flex-wrap items-center gap-1" role="tablist" aria-label="Install platform">
                {(Object.keys(installOptions) as InstallTabKey[]).map((key) => {
                  const active = installTab === key;
                  return (
                    <button
                      key={key}
                      type="button"
                      role="tab"
                      aria-selected={active}
                      onClick={() => {
                        setInstallTab(key);
                        setCopiedInstall(false);
                      }}
                      className={cn(
                        'h-7 px-3 rounded-full text-[12px] transition-all cursor-pointer border',
                        active
                          ? 'font-semibold text-fg bg-surface-1 border-line-strong'
                          : 'font-medium text-fg-muted border-transparent hover:text-fg',
                      )}
                    >
                      {installOptions[key].label}
                    </button>
                  );
                })}
              </div>

              <span className="hidden sm:inline-flex items-center gap-1.5 text-[11px] text-fg-muted ch-num pr-1">
                <ShieldCheck className="w-3.5 h-3.5 text-[var(--emerald)]" />
                <span>SHA-256 Verified · CGO_ENABLED=0</span>
              </span>
            </div>

            <div
              className="rounded-[14px] px-3.5 sm:px-4 py-3 flex items-center justify-between gap-3 border"
              style={{
                backgroundColor: 'var(--code-bg)',
                borderColor: 'var(--code-border)',
              }}
            >
              <div className="flex items-center gap-2.5 min-w-0 overflow-x-auto">
                <span
                  className="ch-num text-[12.5px] font-bold shrink-0 select-none"
                  style={{ color: '#22C55E' }}
                >
                  {installOptions[installTab].prompt}
                </span>
                <code
                  className="ch-num text-[12.5px] sm:text-[13px] whitespace-nowrap"
                  style={{ color: 'var(--code-fg)' }}
                >
                  {installOptions[installTab].cmd}
                </code>
              </div>

              <button
                type="button"
                onClick={() => void handleCopyInstall()}
                className="shrink-0 inline-flex items-center gap-1.5 h-8 px-3 rounded-lg text-[12px] font-semibold cursor-pointer transition-colors border"
                style={{
                  backgroundColor: copiedInstall ? 'rgba(34, 197, 94, 0.18)' : 'rgba(255, 255, 255, 0.08)',
                  borderColor: copiedInstall ? 'rgba(34, 197, 94, 0.45)' : 'rgba(255, 255, 255, 0.12)',
                  color: copiedInstall ? '#4ADE80' : '#F4F4F2',
                }}
              >
                {copiedInstall ? <Check className="w-3.5 h-3.5" /> : <Copy className="w-3.5 h-3.5" />}
                <span>{copiedInstall ? (zh ? '已复制' : 'Copied') : zh ? '一键复制' : 'Copy'}</span>
              </button>
            </div>

            <div className="pt-2 px-2 flex flex-wrap items-center justify-between gap-2 text-[11.5px] text-fg-muted">
              <span>{installOptions[installTab].hint}</span>
              <a
                href={GITHUB_REPO_URL}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1 font-medium text-fg-secondary hover:text-fg no-underline shrink-0"
              >
                <span>{zh ? '查看源码与构建说明' : 'Source & Build Docs'}</span>
                <ArrowUpRight className="w-3 h-3" />
              </a>
            </div>
          </div>
        </section>

        {/* ====================================================================
         * 2b. v1.1 Four Flagship Features Bento Showcase (`#v11-pillars`)
         * ================================================================== */}
        <section
          id="v11-pillars"
          className="max-w-[1260px] mx-auto px-4 sm:px-6 py-6 space-y-5 scroll-mt-20"
        >
          <div className="flex flex-col sm:flex-row sm:items-end justify-between gap-3">
            <div className="space-y-1.5 max-w-[720px]">
              <span className="text-[11.5px] font-bold tracking-wider uppercase text-[var(--emerald-text)]">
                {zh ? 'ColabHive v1.1 重磅升级 · 不懂代码也能玩转免费显卡' : 'New in ColabHive v1.1 · Built for Everyone'}
              </span>
              <h2
                className="text-[24px] sm:text-[31px] font-bold tracking-tight text-fg"
                style={{ fontFamily: "'Bricolage Grotesque', var(--font-sans)" }}
              >
                {zh
                  ? '4 大全民向 AI 杀手级体验，让闲置云端显卡真正为你所用'
                  : 'Four Everyday AI Superpowers Powered by Your Free GPU Pool'}
              </h2>
            </div>
            <StatusPill tone="emerald" size="sm" dot>
              {zh ? '零公网穿透 · 零封号风险 · 完全免费开源' : 'Zero Public Tunnels · Zero Ban Risk · 100% Free'}
            </StatusPill>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 sm:gap-5">
            {/* Pillar 1: Private AI Chat */}
            <div className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4">
              <div className="space-y-2.5">
                <div className="flex items-center justify-between gap-2">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{ backgroundColor: 'var(--emerald-soft)', color: 'var(--emerald-text)' }}
                  >
                    <MessageCircle className="w-5 h-5" />
                  </span>
                  <StatusPill tone="emerald" size="xs" mono>
                    127.0.0.1:7860/v1 · Ollama
                  </StatusPill>
                </div>
                <h3 className="text-[18px] font-bold text-fg">
                  {zh
                    ? '无审查开源大模型一键部署 & 本机 /v1 网关'
                    : 'Uncensored Open-Weight LLM Deployer & Local /v1 Gateway'}
                </h3>
                <p className="text-[13.5px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '精选 2026 年 11 月社区最热 Heretic / Abliterated 正交去审查 GGUF 模型（DeepSeek-V4.1-Flash-1017、GLM-5.3-Flash、Qwen3.8-Flash-Next、Qwen3.8-27B、Gemma 4 及任意 Hugging Face 链接），一键部署到池内免费 T4 或 96GB G4 显卡，本机直接暴露标准 OpenAI / Anthropic / Ollama /v1 接口，直连 SillyTavern 酒馆、Cherry Studio 与 Cursor。'
                    : '1-click top Nov 2026 uncensored GGUFs (DeepSeek-V4.1-Flash-1017, GLM-5.3-Flash, Qwen3.8-Flash-Next, Qwen3.8-27B, Gemma 4, or any hf.co repo) on Free T4 to 96GB G4 GPUs, exposing private OpenAI / Anthropic / Ollama /v1 endpoints for SillyTavern, Cherry Studio & Cursor.'}
                </p>
              </div>
              <button
                type="button"
                onClick={() => navigate('/apps/llm')}
                className="ui-btn ui-btn-secondary ui-btn-md w-full justify-between"
              >
                <span>{zh ? '进入无审查大模型部署 (/apps/llm)' : 'Open Uncensored LLM Deployer (/apps/llm)'}</span>
                <ArrowRight className="w-3.5 h-3.5 text-[var(--emerald-text)]" />
              </button>
            </div>

            {/* Pillar 2: 1-Click Video / Image App Store */}
            <div className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4">
              <div className="space-y-2.5">
                <div className="flex items-center justify-between gap-2">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{ backgroundColor: 'var(--amber-soft)', color: 'var(--amber-text)' }}
                  >
                    <Rocket className="w-5 h-5" />
                  </span>
                  <StatusPill tone="amber" size="xs" mono>
                    127.0.0.1:8188 直连
                  </StatusPill>
                </div>
                <h3 className="text-[18px] font-bold text-fg">
                  {zh
                    ? '🎬 一键 AI 视频/画图商店 & 零穿透私有端口桥'
                    : '🎬 1-Click AI Video/Image Store & Localhost Bridge'}
                </h3>
                <p className="text-[13.5px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '像手机应用商店一样一键拉起 Wan 2.2 图生视频、Flux 超清画图、CosyVoice 声音克隆与 Open WebUI。独创本机私有端口桥 (127.0.0.1:8188)，Krita / PS 画图插件直接连云端大显存，无需 ngrok 公网穿透，彻底告别封号焦虑。'
                    : 'Launch Wan 2.2 Video, Flux ComfyUI, WhisperX + CosyVoice & Open WebUI in one click. Binds private 127.0.0.1 ports locally so Krita / Photoshop plugins connect directly with zero public ngrok tunnels.'}
                </p>
              </div>
              <button
                type="button"
                onClick={() => navigate('/apps')}
                className="ui-btn ui-btn-secondary ui-btn-md w-full justify-between"
              >
                <span>{zh ? '逛逛一键 AI 应用商店 (/apps)' : 'Browse 1-Click App Store (/apps)'}</span>
                <ArrowRight className="w-3.5 h-3.5 text-[var(--amber-text)]" />
              </button>
            </div>

            {/* Pillar 3: Multi-Account Colab Pool (Free-First & Waste-Zero) */}
            <div className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4">
              <div className="space-y-2.5">
                <div className="flex items-center justify-between gap-2">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{ backgroundColor: 'var(--sky-soft)', color: 'var(--sky-text)' }}
                  >
                    <Gift className="w-5 h-5" />
                  </span>
                  <StatusPill tone="sky" size="xs" mono>
                    Free-First · Waste-Zero
                  </StatusPill>
                </div>
                <h3 className="text-[18px] font-bold text-fg">
                  {zh
                    ? '🎁 多账号 Colab 聚合算力池 (Free-First)'
                    : '🎁 Multi-Account Colab Pool (Free-First)'}
                </h3>
                <p className="text-[13.5px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '把多个 Google Colab 免费与 Pro / Pro+ / Ultra 账号汇成同一个本地算力池。开启「优先烧免费额度」先用满所有 $0 T4 窗口，再按快到期批次优先（Waste-Zero）自动分配大显存 G4 / H100 / A100，彻底告别额度过期浪费。'
                    : 'Aggregate multiple Google Colab Free, Pro, Pro+, and Ultra accounts into a single pool. Enable Free-First to drain $0 T4 windows first, then automatically route expiring CU batches to G4 / H100 / A100 GPUs.'}
                </p>
              </div>
              <button
                type="button"
                onClick={() => navigate('/pool')}
                className="ui-btn ui-btn-secondary ui-btn-md w-full justify-between"
              >
                <span>{zh ? '前往账号算力池 (/pool)' : 'Manage Account Pool (/pool)'}</span>
                <ArrowRight className="w-3.5 h-3.5 text-[var(--sky-text)]" />
              </button>
            </div>

            {/* Pillar 4: Auto-Resume & Savings Receipt */}
            <div className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4">
              <div className="space-y-2.5">
                <div className="flex items-center justify-between gap-2">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{ backgroundColor: 'var(--coral-soft)', color: 'var(--coral-text)' }}
                  >
                    <Sparkles className="w-5 h-5" />
                  </span>
                  <StatusPill tone="coral" size="xs">
                    {zh ? '断线续跑 · 省钱海报' : 'Auto-Relay · Poster'}
                  </StatusPill>
                </div>
                <h3 className="text-[18px] font-bold text-fg">
                  {zh
                    ? '🛡️ 掉线自动换号接着跑 & 🏆 算力省钱晒单海报'
                    : '🛡️ Auto-Checkpoint Relay & 🏆 Savings Receipt'}
                </h3>
                <p className="text-[13.5px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '后台定时把作品与训练快照同步回本机：前一个免费账号时长耗尽被踢下线瞬间，自动换下一个空闲号从 82% 接着跑完！更支持一键生成零隐私泄露的高清「算力省钱账单」晒图卡片，直观记录累计省下多少租卡费。'
                    : 'Periodically snapshots progress locally: when a free VM hits timeout, ColabHive automatically restores onto the next account so hours of work never vanish — plus a 1-click PII-free Compute Savings Poster.'}
                </p>
              </div>
              <button
                type="button"
                onClick={() => navigate(consolePath)}
                className="ui-btn ui-btn-secondary ui-btn-md w-full justify-between"
              >
                <span>{zh ? '查看实时省钱账单与控制台 →' : 'View Savings & Live Console →'}</span>
                <ArrowRight className="w-3.5 h-3.5 text-[var(--coral-text)]" />
              </button>
            </div>
          </div>
        </section>

        {/* ====================================================================
         * 3. Embedded Interactive Live Sandbox (The Showstopper!)
         * ================================================================== */}
        <section id="sandbox" className="max-w-[1260px] mx-auto px-4 sm:px-6 py-6 scroll-mt-20">
          <div
            className="rounded-[26px] border p-4 sm:p-6 lg:p-7 space-y-6"
            style={{
              backgroundColor: 'var(--surface-2)',
              borderColor: 'var(--glass-card-border)',
              boxShadow: 'var(--shadow-card)',
            }}
          >
            {/* Sandbox Header + 3-Mode Interactive Switcher */}
            <div className="flex flex-col xl:flex-row xl:items-center justify-between gap-4 pb-4 border-b border-line-faint">
              <div className="space-y-1 min-w-0">
                <div className="flex items-center gap-2">
                  <span
                    className="w-2 h-2 rounded-full animate-pulse"
                    style={{ backgroundColor: 'var(--emerald)' }}
                  />
                  <span className="text-[11.5px] font-bold tracking-wider uppercase text-[var(--emerald-text)]">
                    {zh ? '零后端内存态实机沙盒 · 直接点击交互' : 'Zero-Backend Stateful Live Playground'}
                  </span>
                </div>
                <h2 className="ui-title text-[19px] sm:text-[21px] font-bold text-fg">
                  {zh
                    ? '无需安装，直接在下方拨动开关、试算多卡提速或触发 429 容灾漂移'
                    : 'Try the Live System Tray, 4× Shard Calculator & 429 Auto-Failover Right Here'}
                </h2>
              </div>

              <div
                role="tablist"
                aria-label="Interactive Sandbox Views"
                className="flex flex-wrap sm:flex-nowrap items-center gap-1 p-1.5 rounded-2xl border shrink-0 self-start xl:self-center"
                style={{
                  backgroundColor: 'var(--surface-1)',
                  borderColor: 'var(--well-border)',
                }}
              >
                {(
                  [
                    {
                      key: 'tray' as SandboxStageKey,
                      label: zh ? '托盘四栏实机交互' : '4-Tab Live Tray',
                    },
                    {
                      key: 'pool' as SandboxStageKey,
                      label: zh ? '🖥 算力池与多卡并发总览' : '🖥 Pool & Shards=4 Speedup',
                    },
                    {
                      key: 'failover' as SandboxStageKey,
                      label: zh ? '🔀 跨账号自动容灾与 MCP 路由' : '🔀 Auto-Failover & MCP Router',
                    },
                  ] as const
                ).map((tItem) => {
                  const active = sandboxTab === tItem.key;
                  return (
                    <button
                      key={tItem.key}
                      type="button"
                      role="tab"
                      aria-selected={active}
                      onClick={() => setSandboxTab(tItem.key)}
                      className={cn(
                        'h-9 px-3.5 rounded-xl text-[12.5px] transition-all cursor-pointer whitespace-nowrap',
                        active
                          ? 'bg-surface-2 text-fg font-bold shadow-[var(--shadow-control)] border border-line'
                          : 'text-fg-secondary font-medium hover:text-fg border border-transparent',
                      )}
                    >
                      {tItem.label}
                    </button>
                  );
                })}
              </div>
            </div>

            {/* ----------------------------------------------------------------
             * SANDBOX TAB 1: Real Clickable `<TrayPopoverPage embedded />`
             * -------------------------------------------------------------- */}
            {sandboxTab === 'tray' && (
              <div className="grid grid-cols-1 lg:grid-cols-12 gap-6 items-center">
                {/* Left Guided Interactive Callouts (7 cols) */}
                <div className="lg:col-span-7 space-y-4">
                  <div
                    className="rounded-2xl border p-4 space-y-2"
                    style={{
                      backgroundColor: 'var(--emerald-soft)',
                      borderColor: 'var(--emerald-border)',
                    }}
                  >
                    <div className="flex items-center justify-between gap-2">
                      <span className="inline-flex items-center gap-1.5 text-[12.5px] font-bold text-[var(--emerald-text)]">
                        <Sparkles className="w-4 h-4" />
                        {zh
                          ? '点我切换 Agent / 额度 / 实例 / 路由，或直接在右侧拨动 Claude Code 开关试试！'
                          : 'Click Agent / Quota / VMs / Router below — or flip Claude Code live on the right!'}
                      </span>
                    </div>
                    <p className="text-[12px] leading-relaxed text-fg-secondary">
                      {zh
                        ? '右侧并非静态截图，而是与 Windows 任务栏托盘完全同源的 396×580 真实组件。你在此处拨动的每一个 AI 助手开关都会保存在当前会话中，进入全屏控制台依然生效。'
                        : 'The 396×580 panel on the right is the exact live React surface hosted inside colabhive-desktop.exe. Every switch you toggle persists across the full console.'}
                    </p>
                  </div>

                  {/* 4 Interactive Tray Tab Switcher Cards */}
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    {(
                      [
                        {
                          id: 'agents' as TrayTab,
                          badge: `${connectedAgentCount}/8 ON`,
                          title: zh ? '1. Agent 一键接入栏' : '1. 8-Agent 1-Click Toggles',
                          desc: zh
                            ? '免翻找 JSON/TOML，一键写入 Claude Code、Codex、Antigravity、Cursor 并自动备份。'
                            : 'Surgical cc-switch MCP config sync with automatic backup & 1-click undo.',
                        },
                        {
                          id: 'quota' as TrayTab,
                          badge: `${formatCU(snap.summary.total_available_cu, 0)} CU`,
                          title: zh ? '2. 额度与双环监控栏' : '2. Pool Quota & Dual Rings',
                          desc: zh
                            ? '汇总 AI Ultra (2,140 CU)、Pro+、6 天临期共享号与免费 T4，直观换算 G4 可跑时长。'
                            : 'Tracks AI Ultra, Pro+, 6-day expiring donated CU & Free T4 in G4 GPU-hours.',
                        },
                        {
                          id: 'instances' as TrayTab,
                          badge: `${activeSessions.length} GPUs`,
                          title: zh ? '3. 云端实例负载栏' : '3. Live Cloud GPU VMs',
                          desc: zh
                            ? '每 2.5 秒实时跳动 G4 96GB / A100 80GB / L4 显存与核心利用率，支持一键释放防漏扣。'
                            : 'Live 2.5s VRAM & SM% telemetry across G4 96GB, A100 80GB & L4 with 1-click release.',
                        },
                        {
                          id: 'router' as TrayTab,
                          badge: `${snap.traces.length} Calls`,
                          title: zh ? '4. MCP 实时路由流' : '4. Live MCP Trace Stream',
                          desc: zh
                            ? '三节点流向图 [Caller ── 🐝 ── Account·GPU] 实时滚动展示每次工具调用耗时。'
                            : '3-node flow pill + scrolling JSON-RPC tool invocations with millisecond latency.',
                        },
                      ] as const
                    ).map((card) => {
                      const selected = trayTab === card.id;
                      return (
                        <button
                          key={card.id}
                          type="button"
                          onClick={() => setTrayTab(card.id)}
                          className={cn(
                            'rounded-2xl border p-3.5 text-left transition-all cursor-pointer space-y-1.5',
                            selected
                              ? 'bg-surface-2 border-[var(--accent)] shadow-[var(--shadow-card)]'
                              : 'bg-surface-1 border-line-faint hover:border-line-strong',
                          )}
                        >
                          <div className="flex items-center justify-between gap-2">
                            <span className="text-[13px] font-bold text-fg">{card.title}</span>
                            <span
                              className="ch-num px-2 py-0.5 rounded-full text-[10.5px] font-semibold"
                              style={{
                                backgroundColor: selected
                                  ? 'var(--emerald-soft)'
                                  : 'var(--neutral-soft)',
                                color: selected ? 'var(--emerald-text)' : 'var(--text-secondary)',
                              }}
                            >
                              {card.badge}
                            </span>
                          </div>
                          <p className="text-[11.5px] leading-relaxed text-fg-muted">{card.desc}</p>
                        </button>
                      );
                    })}
                  </div>

                  {/* Synchronized Quick Action Strip */}
                  <div
                    className="rounded-2xl border px-4 py-3 flex flex-wrap items-center justify-between gap-3"
                    style={{
                      backgroundColor: 'var(--surface-1)',
                      borderColor: 'var(--well-border)',
                    }}
                  >
                    <div className="flex items-center gap-3">
                      <Switch
                        checked={claudeCodeConnected}
                        disabled={togglingIde === 'claude-code'}
                        onChange={() => {
                          setTrayTab('agents');
                          void handleToggleIdeLive('claude-code');
                        }}
                        label="Claude Code MCP"
                      />
                      <div className="text-[12.5px]">
                        <span className="font-semibold text-fg">
                          {zh ? '同步拨动 Claude Code 开关' : 'Sync-Toggle Claude Code MCP'}
                        </span>
                        <span className="ml-2 text-[11.5px] text-fg-muted">
                          {claudeCodeConnected
                            ? zh
                              ? '（当前已接入 · 右侧托盘实时联动）'
                              : '(Connected · Watch tray sync live)'
                            : zh
                              ? '（已断开 · 点击重新一键接入）'
                              : '(Disconnected · Click to re-inject)'}
                        </span>
                      </div>
                    </div>

                    <div className="flex items-center gap-2">
                      <button
                        type="button"
                        onClick={() => navigate('/tray')}
                        className="ui-btn ui-btn-secondary ui-btn-xs"
                      >
                        <span>{zh ? '独立打开 /tray' : 'Open /tray'}</span>
                        <ArrowUpRight className="w-3 h-3" />
                      </button>
                      <button
                        type="button"
                        onClick={() => navigate(consolePath)}
                        className="ui-btn ui-btn-primary ui-btn-xs"
                      >
                        <span>{zh ? '进入全屏控制台' : 'Full Console'}</span>
                        <ArrowRight className="w-3 h-3" />
                      </button>
                    </div>
                  </div>
                </div>

                {/* Right Stage: Embedded Real 396×580 TrayPopoverPage (5 cols) */}
                <div className="lg:col-span-5 flex justify-center">
                  <div
                    className="p-3 sm:p-4 rounded-[26px] border flex flex-col items-center"
                    style={{
                      backgroundColor: 'var(--surface-1)',
                      borderColor: 'var(--well-border)',
                      boxShadow: 'var(--well-shadow)',
                    }}
                  >
                    <TrayPopoverPage
                      embedded
                      controlledTab={trayTab}
                      onTabChange={setTrayTab}
                    />
                  </div>
                </div>
              </div>
            )}

            {/* ----------------------------------------------------------------
             * SANDBOX TAB 2: Pool Quota Rings, GPU Matrix & Shards=4 Calculator
             * -------------------------------------------------------------- */}
            {sandboxTab === 'pool' && (
              <div className="grid grid-cols-1 lg:grid-cols-12 gap-6">
                {/* Left: 4 Pooled Accounts + GPU Hour Equivalence Matrix (6 cols) */}
                <div className="lg:col-span-6 space-y-4">
                  <div className="flex items-center justify-between">
                    <div className="text-[13.5px] font-bold text-fg">
                      {zh
                        ? `${snap.accounts.length} 个 Google Colab 账号汇聚一池 · 合计 ${formatCU(snap.summary.total_available_cu, 0)} CU`
                        : `${snap.accounts.length} Pooled Colab Accounts · ${formatCU(snap.summary.total_available_cu, 0)} CU Total`}
                    </div>
                    <button
                      type="button"
                      onClick={() => navigate('/pool')}
                      className="ui-btn ui-btn-secondary ui-btn-xs"
                    >
                      <span>{zh ? '管理账号池' : 'Open Pool'}</span>
                      <ArrowRight className="w-3 h-3" />
                    </button>
                  </div>

                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    {snap.accounts.map((acc) => {
                      const isFree = acc.subscription_tier === 'FREE';
                      const cap = isFree ? 15 : acc.reference_cycle_cu || 500;
                      const bal = isFree
                        ? (acc.free_quota?.remaining_free_cu ?? 0)
                        : acc.paid_cu_balance;
                      const pct = Math.min(100, Math.round((bal / Math.max(1, cap)) * 100));
                      const tone = acc.expiring_soon_cu > 0
                        ? 'amber'
                        : isFree
                          ? 'sky'
                          : 'emerald';

                      return (
                        <div
                          key={acc.id}
                          className="rounded-2xl border p-3.5 flex items-center justify-between gap-3"
                          style={{
                            backgroundColor: 'var(--surface-1)',
                            borderColor: 'var(--well-border)',
                          }}
                        >
                          <div className="min-w-0 space-y-1">
                            <div className="flex items-center gap-1.5">
                              <span className="text-[13px] font-bold text-fg truncate">
                                {acc.display_name || acc.alias}
                              </span>
                            </div>
                            <div className="ch-num text-[12px] font-semibold text-fg-secondary">
                              {isFree
                                ? zh
                                  ? `免费动态池 · ${bal.toFixed(0)} CU`
                                  : `Free T4 Window · ${bal.toFixed(0)} CU`
                                : `${formatCU(bal, 0)} / ${formatCU(cap, 0)} CU`}
                            </div>
                            <div className="text-[11px] text-fg-muted truncate">
                              {acc.expiring_soon_cu > 0
                                ? zh
                                  ? '⏰ 6 天后到期 · Waste-Zero 优先消耗'
                                  : '⏰ Expires in 6d · Waste-Zero Priority'
                                : acc.live_burn_rate_cu_hr > 0
                                  ? zh
                                    ? `运行中 · -${acc.live_burn_rate_cu_hr} CU/h`
                                    : `Active · -${acc.live_burn_rate_cu_hr} CU/h`
                                  : zh
                                    ? '热备待命 · 0.00 CU/h'
                                    : 'Standby · 0.00 CU/h'}
                            </div>
                          </div>

                          <CircularQuotaRing
                            value={pct}
                            size={46}
                            strokeWidth={3.8}
                            tone={tone}
                          />
                        </div>
                      );
                    })}
                  </div>

                  {/* GPU Hour Matrix (G4 96GB, H100 80GB, A100 80GB, L4 24GB) */}
                  <div
                    className="rounded-2xl border p-4 space-y-3"
                    style={{
                      backgroundColor: 'var(--surface-1)',
                      borderColor: 'var(--well-border)',
                    }}
                  >
                    <div className="flex items-center justify-between text-[12.5px]">
                      <span className="font-bold text-fg">
                        {zh ? '池内余量折合各型号显卡可跑时长' : 'Equivalent GPU Runtime Matrix'}
                      </span>
                      <span className="ch-num text-fg-muted">
                        ≈ ${snap.summary.estimated_value_usd.toFixed(0)} PAYG
                      </span>
                    </div>

                    <div className="grid grid-cols-2 sm:grid-cols-4 gap-2.5">
                      {(['G4', 'H100', 'A100', 'L4'] as AcceleratorType[]).map((gpu) => {
                        const eq = snap.summary.gpu_equivalence?.[gpu];
                        const spec = DEFAULT_HARDWARE_CATALOG.find((h) => h.accelerator === gpu)!;
                        const active = calcGpu === gpu;
                        return (
                          <button
                            key={gpu}
                            type="button"
                            onClick={() => setCalcGpu(gpu)}
                            className={cn(
                              'rounded-xl border p-2.5 text-left transition-all cursor-pointer',
                              active
                                ? 'bg-surface-2 border-[var(--accent)] shadow-[var(--shadow-control)]'
                                : 'bg-surface-2/60 border-line-faint hover:border-line-strong',
                            )}
                          >
                            <div className="flex items-center justify-between text-[11px] font-bold text-fg">
                              <span>{gpu}</span>
                              <span className="ch-num text-fg-muted">
                                {gpu === 'A100' ? '80GB' : `${spec.vram_gb}GB`}
                              </span>
                            </div>
                            <div className="mt-1 ch-num text-[16px] font-bold text-[var(--emerald-text)]">
                              {formatHours(eq?.schedulable_hours ?? 200)}
                            </div>
                            <div className="ch-num text-[10.5px] text-fg-muted">
                              {spec.default_burn_rate_cu_hr} CU/h
                            </div>
                          </button>
                        );
                      })}
                    </div>
                  </div>
                </div>

                {/* Right: Shards=4 Parallel Speedup Calculator + Live Experiment Progress (6 cols) */}
                <div className="lg:col-span-6 space-y-4">
                  <div
                    className="rounded-2xl border p-4 sm:p-5 space-y-4"
                    style={{
                      backgroundColor: 'var(--surface-1)',
                      borderColor: 'var(--well-border)',
                    }}
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div>
                        <div className="text-[13.5px] font-bold text-fg">
                          {zh
                            ? '多卡并行省时计算器（总扣费 CU 100% 不变！）'
                            : 'Parallel Shard Speedup Calculator (Zero Extra CU Cost!)'}
                        </div>
                        <div className="text-[11.5px] text-fg-muted">
                          {zh
                            ? `${calcShards} 张 ${calcGpu} 并行跑 ${calcParallelWallHrs.toFixed(1)}h = 单卡串行跑 ${calcWorkHours}h`
                            : `${calcShards}× ${calcGpu} for ${calcParallelWallHrs.toFixed(1)}h == 1× ${calcGpu} for ${calcWorkHours}h`}
                        </div>
                      </div>
                      <StatusPill tone="emerald" size="sm" mono>
                        {calcShards}× {zh ? '倍提速' : 'Faster'}
                      </StatusPill>
                    </div>

                    {/* Controls: Shards & Total Workload */}
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                      <div className="space-y-1.5">
                        <span className="text-[11.5px] font-semibold text-fg-secondary">
                          {zh ? '并行分片卡数 (Shards)' : 'Parallel GPU Shards'}
                        </span>
                        <div className="grid grid-cols-4 gap-1.5">
                          {[1, 2, 4, 8].map((k) => (
                            <button
                              key={k}
                              type="button"
                              onClick={() => setCalcShards(k)}
                              className={cn(
                                'h-8 rounded-lg ch-num text-[12px] font-bold border cursor-pointer transition-colors',
                                calcShards === k
                                  ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
                                  : 'bg-surface-2 text-fg-secondary border-line hover:text-fg',
                              )}
                            >
                              {k}×
                            </button>
                          ))}
                        </div>
                      </div>

                      <div className="space-y-1.5">
                        <span className="text-[11.5px] font-semibold text-fg-secondary">
                          {zh ? '实验总算力工作量' : 'Total GPU-Hours Workload'}
                        </span>
                        <div className="grid grid-cols-3 gap-1.5">
                          {[4, 8, 16].map((hrs) => (
                            <button
                              key={hrs}
                              type="button"
                              onClick={() => setCalcWorkHours(hrs)}
                              className={cn(
                                'h-8 rounded-lg ch-num text-[12px] font-bold border cursor-pointer transition-colors',
                                calcWorkHours === hrs
                                  ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
                                  : 'bg-surface-2 text-fg-secondary border-line hover:text-fg',
                              )}
                            >
                              {hrs} GPU·h
                            </button>
                          ))}
                        </div>
                      </div>
                    </div>

                    {/* Visual Comparison Bars */}
                    <div className="rounded-xl bg-surface-2 border border-line p-3.5 space-y-3">
                      <div className="space-y-1">
                        <div className="flex items-center justify-between text-[12px]">
                          <span className="text-fg-muted">
                            {zh ? `单账号串行 (1× ${calcGpu})` : `Single-Account Serial (1× ${calcGpu})`}
                          </span>
                          <span className="ch-num font-semibold text-fg-secondary">
                            {formatHours(calcWorkHours)} · {formatCU(calcTotalCu, 1)} CU
                          </span>
                        </div>
                        <div className="h-2 rounded-full bg-[var(--neutral-soft)] overflow-hidden">
                          <div
                            className="h-full rounded-full"
                            style={{ width: '100%', backgroundColor: 'var(--text-muted)' }}
                          />
                        </div>
                      </div>

                      <div className="space-y-1">
                        <div className="flex items-center justify-between text-[12px]">
                          <span className="font-bold text-fg">
                            {zh
                              ? `ColabHive 跨号并发 (${calcShards}× ${calcGpu})`
                              : `ColabHive Multi-Account (${calcShards}× ${calcGpu})`}
                          </span>
                          <span className="ch-num font-bold text-[var(--emerald-text)]">
                            {formatHours(calcParallelWallHrs)} · {formatCU(calcTotalCu, 1)} CU ({zh ? '零额外扣费' : 'Same CU'})
                          </span>
                        </div>
                        <div className="h-2.5 rounded-full bg-[var(--emerald-soft)] overflow-hidden">
                          <div
                            className="h-full rounded-full transition-all duration-300"
                            style={{
                              width: `${Math.max(12, Math.round(100 / calcShards))}%`,
                              backgroundColor: 'var(--emerald)',
                            }}
                          />
                        </div>
                      </div>
                    </div>

                    {/* Live Animated Shards=4 Sweep Preview */}
                    {liveExperiment && (
                      <div className="space-y-2 pt-1">
                        <div className="flex items-center justify-between text-[11.5px]">
                          <span className="font-semibold text-fg-secondary">
                            {zh
                              ? `实时分片实况：${liveExperiment.title}`
                              : `Live Sweep: ${liveExperiment.title}`}
                          </span>
                          <button
                            type="button"
                            onClick={() => navigate('/exp')}
                            className="text-[var(--emerald-text)] font-semibold hover:underline cursor-pointer bg-transparent border-0 p-0"
                          >
                            {zh ? '打开实验终端 →' : 'Open Workbench →'}
                          </button>
                        </div>

                        <div className="grid grid-cols-2 gap-2">
                          {liveExperiment.shards.map((sh) => (
                            <div
                              key={sh.shard_id}
                              className="rounded-xl bg-surface-2 border border-line-faint px-3 py-2 space-y-1"
                            >
                              <div className="flex items-center justify-between text-[11px] ch-num">
                                <span className="font-bold text-fg">
                                  Shard #{sh.shard_id} · {sh.accelerator}
                                </span>
                                <span className="text-[var(--emerald-text)] font-semibold">
                                  {sh.progress_pct}%
                                </span>
                              </div>
                              <SegmentedQuotaBar
                                value={sh.progress_pct}
                                segments={14}
                                tone="emerald"
                              />
                            </div>
                          ))}
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              </div>
            )}

            {/* ----------------------------------------------------------------
             * SANDBOX TAB 3: Interactive Cross-Account 429 Failover Simulator
             * -------------------------------------------------------------- */}
            {sandboxTab === 'failover' && (
              <div className="space-y-5">
                {/* Interactive Action Bar */}
                <div
                  className="rounded-2xl border px-4 py-3.5 flex flex-col sm:flex-row sm:items-center justify-between gap-3"
                  style={{
                    backgroundColor:
                      failoverPhase === 'throttled'
                        ? 'var(--amber-soft)'
                        : failoverPhase === 'rerouted'
                          ? 'var(--sky-soft)'
                          : 'var(--surface-1)',
                    borderColor:
                      failoverPhase === 'throttled'
                        ? 'var(--amber-border)'
                        : failoverPhase === 'rerouted'
                          ? 'var(--sky-border)'
                          : 'var(--well-border)',
                  }}
                >
                  <div className="space-y-0.5">
                    <div className="text-[13.5px] font-bold text-fg">
                      {failoverPhase === 'normal' &&
                        (zh
                          ? '当前状态：主力账号 AI-Ultra-01 (G4 96GB) 正常承载 MCP 请求'
                          : 'Status: Primary AI-Ultra-01 (G4 96GB) serving MCP calls normally')}
                      {failoverPhase === 'throttled' &&
                        (zh
                          ? '⚡ 侦测到 HTTP 429 区域限流 / 缺卡！正在触发二级健康状态机冷却与漂移…'
                          : '⚡ HTTP 429 Rate-Limit / Stockout detected! Evaluating standby pool…')}
                      {failoverPhase === 'rerouted' &&
                        (zh
                          ? '✓ 38ms 无感漂移完成！已自动切换至备用号 ProPlus-Alex · A100 80GB（Agent 零报错中断）'
                          : '✓ 38ms Failover Complete! Routed to ProPlus-Alex · A100 80GB with zero agent error')}
                    </div>
                    <div className="text-[11.5px] text-fg-secondary">
                      {zh
                        ? '告别官方 colab-mcp 单账号遇错直接退出 code 1 的痛点：ColabHive 自动跨账号、跨卡型毫秒级兜底'
                        : 'Eliminates single-account colab-mcp crashes: ColabHive transparently fails over across accounts & GPU tiers'}
                    </div>
                  </div>

                  <div className="flex flex-wrap items-center gap-2 shrink-0">
                    <button
                      type="button"
                      onClick={handleTriggerFailover}
                      className="ui-btn ui-btn-coral ui-btn-md"
                    >
                      <Zap className="w-3.5 h-3.5" />
                      <span>
                        {zh
                          ? '模拟主号 429 限流 / 断连 ➔ 观察毫秒级自动漂移至备用号'
                          : 'Simulate Primary 429 ➔ Watch 38ms Auto-Failover'}
                      </span>
                    </button>
                    {failoverPhase !== 'normal' && (
                      <button
                        type="button"
                        onClick={handleResetFailover}
                        className="ui-btn ui-btn-secondary ui-btn-md"
                      >
                        <RotateCcw className="w-3.5 h-3.5" />
                        <span>{zh ? '重置' : 'Reset'}</span>
                      </button>
                    )}
                  </div>
                </div>

                {/* SVG Animated Bezier Topology Canvas */}
                <div
                  className="rounded-2xl border p-4 sm:p-5 overflow-x-auto"
                  style={{
                    backgroundColor: 'var(--surface-1)',
                    borderColor: 'var(--well-border)',
                  }}
                >
                  <svg
                    viewBox="0 0 920 270"
                    className="w-full min-w-[680px] h-auto select-none"
                    role="img"
                    aria-label="Cross-account MCP failover topology"
                  >
                    {/* Left -> Center Bezier Paths */}
                    <path
                      d="M 220 72 C 300 72, 315 135, 385 135"
                      fill="none"
                      stroke="var(--emerald)"
                      strokeWidth="2.4"
                      strokeOpacity="0.85"
                    />
                    <path
                      d="M 220 135 C 295 135, 315 135, 385 135"
                      fill="none"
                      stroke="var(--sky)"
                      strokeWidth="2.4"
                      strokeOpacity="0.85"
                    />
                    <path
                      d="M 220 198 C 300 198, 315 135, 385 135"
                      fill="none"
                      stroke="var(--emerald)"
                      strokeWidth="2"
                      strokeOpacity="0.55"
                    />

                    {/* Center -> Right Path 1 (Primary AI Ultra: Emerald -> Amber/Coral dashed on 429) */}
                    <path
                      d="M 545 135 C 615 135, 625 62, 695 62"
                      fill="none"
                      stroke={
                        failoverPhase === 'normal'
                          ? 'var(--emerald)'
                          : failoverPhase === 'throttled'
                            ? 'var(--amber)'
                            : 'var(--coral)'
                      }
                      strokeWidth={failoverPhase === 'normal' ? '3' : '2.2'}
                      strokeDasharray={failoverPhase === 'normal' ? undefined : '6 5'}
                      strokeOpacity={failoverPhase === 'rerouted' ? '0.55' : '0.95'}
                    />

                    {/* Center -> Right Path 2 (Standby Pro+ A100: Lights up Amber -> Sky on Failover!) */}
                    <path
                      d="M 545 135 C 615 135, 625 135, 695 135"
                      fill="none"
                      stroke={
                        failoverPhase === 'rerouted'
                          ? 'var(--sky)'
                          : failoverPhase === 'throttled'
                            ? 'var(--amber)'
                            : 'var(--border-strong)'
                      }
                      strokeWidth={failoverPhase === 'rerouted' ? '3.6' : '2'}
                    />

                    {/* Center -> Right Path 3 (Donated Lab L4: Waste-Zero steady Emerald) */}
                    <path
                      d="M 545 135 C 615 135, 625 208, 695 208"
                      fill="none"
                      stroke="var(--emerald)"
                      strokeWidth="2.2"
                      strokeOpacity="0.75"
                    />

                    {/* Animated Packet Dot along Active Target */}
                    {failoverPhase === 'normal' && (
                      <circle r="5" fill="var(--emerald)">
                        <animateMotion
                          dur="1.8s"
                          repeatCount="indefinite"
                          path="M 545 135 C 615 135, 625 62, 695 62"
                        />
                      </circle>
                    )}
                    {failoverPhase === 'rerouted' && (
                      <circle r="5.5" fill="var(--sky)">
                        <animateMotion
                          dur="1.3s"
                          repeatCount="indefinite"
                          path="M 545 135 C 615 135, 625 135, 695 135"
                        />
                      </circle>
                    )}

                    {/* Left Caller Nodes */}
                    {[
                      { y: 48, title: 'Claude Code CLI', sub: 'Streamable HTTP /mcp', dot: 'var(--coral)' },
                      { y: 111, title: 'Antigravity IDE', sub: 'Sticky Kernel Session', dot: 'var(--emerald)' },
                      { y: 174, title: 'Codex / Cursor', sub: 'MCP stdio & HTTP', dot: 'var(--sky)' },
                    ].map((cNode) => (
                      <g key={cNode.title} transform={`translate(20, ${cNode.y})`}>
                        <rect
                          width="200"
                          height="48"
                          rx="12"
                          fill="var(--surface-2)"
                          stroke="var(--border-subtle)"
                        />
                        <circle cx="20" cy="24" r="5" fill={cNode.dot} />
                        <text
                          x="34"
                          y="21"
                          fill="var(--text-primary)"
                          fontSize="12.5"
                          fontWeight="700"
                        >
                          {cNode.title}
                        </text>
                        <text x="34" y="36" fill="var(--text-muted)" fontSize="10.5">
                          {cNode.sub}
                        </text>
                      </g>
                    ))}

                    {/* Center Smart Router Hub */}
                    <g transform="translate(385, 91)">
                      <rect
                        width="160"
                        height="88"
                        rx="18"
                        fill="var(--brand-bg)"
                        stroke={
                          failoverPhase === 'rerouted'
                            ? 'var(--sky)'
                            : failoverPhase === 'throttled'
                              ? 'var(--amber)'
                              : '#22C55E'
                        }
                        strokeWidth="2"
                      />
                      <text
                        x="80"
                        y="32"
                        textAnchor="middle"
                        fill="#FFFFFF"
                        fontSize="13"
                        fontWeight="700"
                      >
                        ColabHive Router
                      </text>
                      <text
                        x="80"
                        y="51"
                        textAnchor="middle"
                        fill="#A1A19A"
                        fontSize="10.5"
                        fontFamily="JetBrains Mono, monospace"
                      >
                        Waste-Zero · Sticky
                      </text>
                      <text
                        x="80"
                        y="69"
                        textAnchor="middle"
                        fill={
                          failoverPhase === 'rerouted'
                            ? '#60A5FA'
                            : failoverPhase === 'throttled'
                              ? '#FBBF24'
                              : '#4ADE80'
                        }
                        fontSize="10.5"
                        fontWeight="700"
                        fontFamily="JetBrains Mono, monospace"
                      >
                        {failoverPhase === 'normal'
                          ? '● 12ms Healthy'
                          : failoverPhase === 'throttled'
                            ? '⚡ 429 Intercepted'
                            : '✓ 38ms Auto-Drift'}
                      </text>
                    </g>

                    {/* Right Account Target Nodes */}
                    {/* Node 1: Primary AI Ultra */}
                    <g transform="translate(695, 36)">
                      <rect
                        width="205"
                        height="52"
                        rx="13"
                        fill="var(--surface-2)"
                        stroke={
                          failoverPhase === 'normal' ? 'var(--emerald)' : 'var(--coral)'
                        }
                        strokeWidth="1.8"
                      />
                      <text
                        x="14"
                        y="21"
                        fill="var(--text-primary)"
                        fontSize="12"
                        fontWeight="700"
                      >
                        AI-Ultra-01 · G4 96GB
                      </text>
                      <text
                        x="14"
                        y="38"
                        fill={
                          failoverPhase === 'normal'
                            ? 'var(--emerald-text)'
                            : 'var(--coral-text)'
                        }
                        fontSize="10.5"
                        fontWeight="600"
                        fontFamily="JetBrains Mono, monospace"
                      >
                        {failoverPhase === 'normal'
                          ? '2,140 CU · Primary Active'
                          : 'HTTP 429 · Soft Cooldown'}
                      </text>
                    </g>

                    {/* Node 2: Failover Standby Pro+ A100 80GB */}
                    <g transform="translate(695, 109)">
                      <rect
                        width="205"
                        height="52"
                        rx="13"
                        fill="var(--surface-2)"
                        stroke={
                          failoverPhase === 'rerouted'
                            ? 'var(--sky)'
                            : 'var(--border-subtle)'
                        }
                        strokeWidth={failoverPhase === 'rerouted' ? '2.2' : '1.2'}
                      />
                      <text
                        x="14"
                        y="21"
                        fill="var(--text-primary)"
                        fontSize="12"
                        fontWeight="700"
                      >
                        ProPlus-Alex · A100 80GB
                      </text>
                      <text
                        x="14"
                        y="38"
                        fill={
                          failoverPhase === 'rerouted'
                            ? 'var(--sky-text)'
                            : 'var(--text-muted)'
                        }
                        fontSize="10.5"
                        fontWeight="600"
                        fontFamily="JetBrains Mono, monospace"
                      >
                        {failoverPhase === 'rerouted'
                          ? '✓ Active Failover · 38ms OK'
                          : '412 CU · Warm Standby'}
                      </text>
                    </g>

                    {/* Node 3: Donated Lab L4 */}
                    <g transform="translate(695, 182)">
                      <rect
                        width="205"
                        height="52"
                        rx="13"
                        fill="var(--surface-2)"
                        stroke="var(--border-subtle)"
                      />
                      <text
                        x="14"
                        y="21"
                        fill="var(--text-primary)"
                        fontSize="12"
                        fontWeight="700"
                      >
                        Donated-Maya · L4 24GB
                      </text>
                      <text
                        x="14"
                        y="38"
                        fill="var(--amber-text)"
                        fontSize="10.5"
                        fontWeight="600"
                        fontFamily="JetBrains Mono, monospace"
                      >
                        168 CU · 6d Expiry Priority
                      </text>
                    </g>
                  </svg>
                </div>

                {/* Live Trace Output Strip */}
                <div className="rounded-2xl border border-line-faint divide-y divide-line-faint bg-surface-2">
                  {(lastFailoverTrace
                    ? [lastFailoverTrace, ...snap.traces.filter((t) => t.id !== lastFailoverTrace.id)]
                    : snap.traces
                  )
                    .slice(0, 3)
                    .map((tr) => (
                      <div
                        key={tr.id}
                        className="px-4 py-2.5 flex flex-wrap items-center justify-between gap-2 text-[12px]"
                      >
                        <div className="flex items-center gap-2 min-w-0">
                          <StatusPill
                            tone={tr.status === 'fallback' ? 'sky' : 'emerald'}
                            size="xs"
                            mono
                          >
                            {tr.status === 'fallback'
                              ? zh
                                ? '自动容灾漂移'
                                : 'AUTO-FAILOVER'
                              : '200 OK'}
                          </StatusPill>
                          <span className="font-semibold text-fg">{tr.caller}</span>
                          <code className="ch-num px-1.5 py-0.5 rounded bg-surface-1 text-[11px] text-fg-secondary">
                            {tr.tool_or_endpoint}
                          </code>
                          <span className="text-fg-muted ch-num truncate">
                            → {tr.account_email?.split('@')[0]} · {tr.assigned_gpu}
                          </span>
                        </div>
                        <span className="ch-num text-[11.5px] font-semibold text-fg-secondary">
                          {formatDurationMs(tr.duration_ms)}
                        </span>
                      </div>
                    ))}
                </div>
              </div>
            )}
          </div>
        </section>

        {/* ====================================================================
         * 4. 6-Card Architectural Bento Grid
         * ================================================================== */}
        <section
          id="architecture"
          className="max-w-[1260px] mx-auto px-4 sm:px-6 py-12 space-y-7 scroll-mt-20"
        >
          <div className="max-w-[740px] space-y-2">
            <span className="text-[11.5px] font-bold tracking-wider uppercase text-[var(--coral-text)]">
              {zh ? '系统级可靠性与安全边界' : 'Precision Systems Architecture'}
            </span>
            <h2
              className="text-[26px] sm:text-[34px] font-bold tracking-tight text-fg"
              style={{ fontFamily: "'Bricolage Grotesque', var(--font-sans)" }}
            >
              {zh
                ? '专为多账号科研算力池与全自动 AI 编程智能体打造'
                : 'Engineered for Multi-Account GPU Clusters & Autonomous AI Agents'}
            </h2>
            <p className="text-[14.5px] text-fg-secondary leading-relaxed">
              {zh
                ? '每一处细节都针对真实 Colab 痛点设计：临期额度防作废、僵尸进程防偷跑、好友共享防越权、多智能体并发免阻塞。'
                : 'Every subsystem eliminates a real Colab failure mode: expiring CUs, zombie GPU leaks, credential exposure, and single-client MCP locks.'}
            </p>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 sm:gap-5">
            {/* Bento Card 1: Waste-Zero Scheduler */}
            <div className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4">
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{
                      backgroundColor: 'var(--amber-soft)',
                      color: 'var(--amber-text)',
                    }}
                  >
                    <Flame className="w-5 h-5" />
                  </span>
                  <StatusPill tone="amber" size="xs" mono>
                    Waste-Zero FSM
                  </StatusPill>
                </div>
                <h3 className="text-[17px] font-bold text-fg">
                  {zh ? 'Waste-Zero 临期防浪费调度器' : 'Waste-Zero Expiring-CU Scheduler'}
                </h3>
                <p className="text-[13px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '自动识别 90 天按量到期批次与月度重置节点，优先消耗快过期算力与免费动态 T4 窗口，不让任何 1 CU 白白过期作废。'
                    : 'Automatically prioritizes 90-day expiring PAYG batches, monthly reset cliffs, and dynamically refilling Free T4 milli-CCU windows.'}
                </p>
              </div>

              <div className="rounded-xl p-3 bg-surface-1 border border-line-faint space-y-1.5 text-[11.5px] ch-num">
                <div className="flex items-center justify-between">
                  <span className="text-[var(--amber-text)] font-semibold">
                    1. Donated-Maya (6d left)
                  </span>
                  <span className="font-bold text-fg">Priority #1</span>
                </div>
                <div className="flex items-center justify-between text-fg-muted">
                  <span>2. AI-Ultra-01 (21d reset)</span>
                  <span>Deep Reserve</span>
                </div>
              </div>
            </div>

            {/* Bento Card 2: 8-IDE Zero-Config Headless MCP (`#agents`) */}
            <div
              id="agents"
              className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4 scroll-mt-24"
            >
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{
                      backgroundColor: 'var(--emerald-soft)',
                      color: 'var(--emerald-text)',
                    }}
                  >
                    <Zap className="w-5 h-5" />
                  </span>
                  <StatusPill tone="emerald" size="xs" mono>
                    HTTP + SSE + stdio
                  </StatusPill>
                </div>
                <h3 className="text-[17px] font-bold text-fg">
                  {zh ? '8 款主流 AI 助手一键零配置 MCP' : '8-IDE Zero-Config Headless MCP'}
                </h3>
                <p className="text-[13px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '100% 无头三协议并发服务，彻底解决官方工具弹浏览器窗口与 1013 单客户端锁死问题；首次写入前自动备份原配置。'
                    : '100% headless tri-protocol MCP server with zero Chrome popups and zero 1013 single-client locks. Click any badge to toggle live:'}
                </p>
              </div>

              <div className="flex flex-wrap gap-1.5">
                {IDE_BADGES.map((ide) => {
                  const isOn = Boolean(
                    snap.clients.find((c) => c.client_id === ide.id)?.injected,
                  );
                  return (
                    <button
                      key={ide.id}
                      type="button"
                      onClick={() => void handleToggleIdeLive(ide.id)}
                      className="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-full text-[11.5px] font-semibold border cursor-pointer transition-all"
                      style={{
                        backgroundColor: isOn ? 'var(--emerald-soft)' : 'var(--surface-1)',
                        borderColor: isOn ? 'var(--emerald-border)' : 'var(--border-faint)',
                        color: isOn ? 'var(--emerald-text)' : 'var(--text-secondary)',
                      }}
                    >
                      <AgentBrandIcon id={ide.id} size={14} className="text-fg" />
                      <span>{ide.name}</span>
                      {isOn && <span className="w-1.5 h-1.5 rounded-full bg-current" />}
                    </button>
                  );
                })}
              </div>
            </div>

            {/* Bento Card 3: `/proc/stat` Anti-Zombie Guard */}
            <div className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4">
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{
                      backgroundColor: 'var(--coral-soft)',
                      color: 'var(--coral-text)',
                    }}
                  >
                    <Activity className="w-5 h-5" />
                  </span>
                  <StatusPill tone="coral" size="xs" mono>
                    /proc/pid/stat
                  </StatusPill>
                </div>
                <h3 className="text-[17px] font-bold text-fg">
                  {zh ? '/proc/stat 防僵尸巡检与自动退卡' : '/proc/stat Anti-Zombie & Auto-Unassign'}
                </h3>
                <p className="text-[13px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '每 8 秒维持 ZMQ 心跳保活并巡检 Linux 内核进程态。一旦捕获僵尸态 (Z) 或致命 CUDA 崩溃，立即落盘日志并退租显卡，杜绝半夜空烧 9.66 CU/h。'
                    : '8s ZMQ keepalive paired with Linux /proc/{pid}/stat inspection. Aborts hung zombie workers and unassigns idle GPUs automatically.'}
                </p>
              </div>

              <div className="rounded-xl p-3 bg-surface-1 border border-line-faint flex items-center justify-between text-[12px] ch-num">
                <span className="text-fg-secondary">
                  {zh ? '累计自动挽回闲置算力' : 'Saved Idle Quota'}
                </span>
                <span className="font-bold text-[var(--emerald-text)]">
                  +{formatCU(snap.summary.saved_idle_waste_cu, 1)} CU
                </span>
              </div>
            </div>

            {/* Bento Card 4: Portable Encrypted Vault & Local CLI Auto-Scan */}
            <div className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4">
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{
                      backgroundColor: 'var(--emerald-soft)',
                      color: 'var(--emerald-text)',
                    }}
                  >
                    <Lock className="w-5 h-5" />
                  </span>
                  <StatusPill tone="emerald" size="xs">
                    {zh ? 'AES-256-GCM 本地加密' : 'AES-256-GCM Vault'}
                  </StatusPill>
                </div>
                <h3 className="text-[17px] font-bold text-fg">
                  {zh ? '一键导入本机凭证与加密迁移' : '1-Click Local Scan & Vault Backup'}
                </h3>
                <p className="text-[13px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '自动识别本机与 WSL2 已登录的 Colab CLI 账号邮箱，所有令牌均使用本地 AES-256-GCM 加密保存，并支持 .chvault 一键换机恢复。'
                    : 'Automatically identifies signed-in local & WSL2 Colab CLI accounts, encrypts all tokens with AES-256-GCM, and supports portable .chvault backups.'}
                </p>
              </div>

              <button
                type="button"
                onClick={() => navigate('/pool')}
                className="ui-btn ui-btn-secondary ui-btn-md w-full justify-between"
              >
                <span className="ch-num truncate">/pool</span>
                <span className="inline-flex items-center gap-1 font-semibold text-[var(--emerald-text)]">
                  {zh ? '管理账号池' : 'Open Account Pool'}
                  <ArrowRight className="w-3.5 h-3.5" />
                </span>
              </button>
            </div>

            {/* Bento Card 5: Per-Account Sticky SOCKS5 / HTTP Proxy */}
            <div className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4">
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{
                      backgroundColor: 'var(--sky-soft)',
                      color: 'var(--sky-text)',
                    }}
                  >
                    <Network className="w-5 h-5" />
                  </span>
                  <StatusPill tone="sky" size="xs" mono>
                    SOCKS5 / HTTP
                  </StatusPill>
                </div>
                <h3 className="text-[17px] font-bold text-fg">
                  {zh ? '单账号独立固定出口代理隔离' : 'Per-Account Sticky Proxy Isolation'}
                </h3>
                <p className="text-[13px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '告别全局 VPN 串号风控：每个 Google 账号可绑定专属 SOCKS5/HTTP 住宅或校园网出口，贯穿 OAuth 续期、控制面与 Jupyter 隧道。'
                    : 'Pin dedicated SOCKS5 or HTTP exit IPs per Google account across OAuth token refresh, Colab Control Plane, and Jupyter WebSocket streams.'}
                </p>
              </div>

              <div className="rounded-xl p-3 bg-surface-1 border border-line-faint space-y-1 text-[11.5px] ch-num text-fg-secondary">
                <div className="flex justify-between">
                  <span>AI-Ultra-01 ➔ socks5://38.142.*.*</span>
                  <span className="text-[var(--emerald-text)]">48ms ✓</span>
                </div>
                <div className="flex justify-between">
                  <span>ProPlus-Alex ➔ http://198.51.*.*</span>
                  <span className="text-[var(--emerald-text)]">61ms ✓</span>
                </div>
              </div>
            </div>

            {/* Bento Card 6: Native Win32 Tray vs Hardened Cloud Server (`#modes`) */}
            <div
              id="modes"
              className="ui-card p-5 sm:p-6 flex flex-col justify-between gap-4 scroll-mt-24"
            >
              <div className="space-y-3">
                <div className="flex items-center justify-between">
                  <span
                    className="w-10 h-10 rounded-2xl inline-flex items-center justify-center"
                    style={{
                      backgroundColor: 'var(--neutral-soft)',
                      color: 'var(--text-primary)',
                    }}
                  >
                    <Layers className="w-5 h-5" />
                  </span>
                  <StatusPill tone="accent" size="xs">
                    {zh ? '双栖架构' : 'Dual Mode'}
                  </StatusPill>
                </div>
                <h3 className="text-[17px] font-bold text-fg">
                  {zh
                    ? 'Win32 & macOS 原生托盘端 vs. 云端团队服'
                    : 'Frameless Win32 & macOS Menu Bar vs. Cloud'}
                </h3>
                <p className="text-[13px] leading-relaxed text-fg-secondary">
                  {zh
                    ? '个人开发支持 Windows 无边框系统托盘端与 macOS Apple Silicon (ColabHive.app) 菜单栏弹窗本机免密直连；实验室合租用 --mode cloud 开启管理员门禁、受限 ch_sk_ 密钥与自动 TLS。'
                    : 'Solo devs run frameless Windows Tray or macOS Apple Silicon Menu Bar (ColabHive.app) with keyless /mcp; teams deploy --mode cloud with Admin Gate & scoped ch_sk_ keys.'}
                </p>
              </div>

              <div className="grid grid-cols-2 gap-2 text-[11.5px]">
                <div className="rounded-xl p-2.5 bg-surface-1 border border-line-faint space-y-0.5">
                  <div className="flex items-center gap-1 font-bold text-fg">
                    <Laptop className="w-3.5 h-3.5 text-[var(--emerald)]" />
                    <span>{zh ? 'Win + macOS 桌面端' : 'Win + macOS App'}</span>
                  </div>
                  <div className="text-fg-muted">
                    {zh ? '托盘/菜单栏 · 免密 /mcp' : 'Tray / Menu Bar · Keyless'}
                  </div>
                </div>
                <div className="rounded-xl p-2.5 bg-surface-1 border border-line-faint space-y-0.5">
                  <div className="flex items-center gap-1 font-bold text-fg">
                    <Cloud className="w-3.5 h-3.5 text-[var(--sky)]" />
                    <span>{zh ? '云端团队版' : 'Cloud Server'}</span>
                  </div>
                  <div className="text-fg-muted">
                    {zh ? 'Bearer 门禁 · 配额密钥' : 'Admin Gate · Scoped Keys'}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </section>

        {/* Bottom Full-Console Launch Banner */}
        <section className="max-w-[1260px] mx-auto px-4 sm:px-6 pb-14">
          <div
            className="rounded-[24px] border p-6 sm:p-9 flex flex-col lg:flex-row lg:items-center justify-between gap-6"
            style={{
              backgroundColor: 'var(--surface-2)',
              borderColor: 'var(--glass-card-border)',
              boxShadow: 'var(--shadow-card)',
            }}
          >
            <div className="space-y-2 max-w-[680px]">
              <h2
                className="text-[22px] sm:text-[28px] font-bold tracking-tight text-fg"
                style={{ fontFamily: "'Bricolage Grotesque', var(--font-sans)" }}
              >
                {zh
                  ? '立即探索完整 8 大模块在线控制台'
                  : 'Explore All 8 Modules in the Full Live Observatory'}
              </h2>
              <p className="text-[14px] text-fg-secondary leading-relaxed">
                {zh
                  ? '包含私人开源大模型网关、一键 AI 视频/画图应用商店、Colab 多号算力池、全局算力罗盘、远程 Jupyter 工作台与 Bezier 智能路由拓扑图。'
                  : 'Includes Private LLM Gateway, 1-Click AI App Store, Multi-Account Colab Pool, Compute Compass, Remote Jupyter Workbench & Bezier Smart Router.'}
              </p>
            </div>

            <div className="flex flex-wrap items-center gap-3 shrink-0">
              <button
                type="button"
                onClick={() => navigate(consolePath)}
                className="h-11 px-6 rounded-full text-[14px] font-semibold inline-flex items-center gap-2 cursor-pointer"
                style={{
                  backgroundColor: 'var(--accent)',
                  color: 'var(--accent-fg)',
                  boxShadow: 'var(--shadow-accent)',
                }}
              >
                <span>{zh ? '进入全屏在线控制台 →' : 'Launch Full Live Console →'}</span>
              </button>
              <a
                href={GITHUB_REPO_URL}
                target="_blank"
                rel="noopener noreferrer"
                className="h-11 px-5 rounded-full text-[13.5px] font-semibold border inline-flex items-center gap-2 no-underline"
                style={{
                  backgroundColor: 'var(--surface-1)',
                  borderColor: 'var(--control-border)',
                  color: 'var(--text-primary)',
                }}
              >
                <Star className="w-4 h-4 text-[var(--amber)]" />
                <span>{zh ? '在 GitHub 点亮 Star' : 'Star on GitHub'}</span>
              </a>
            </div>
          </div>
        </section>
      </main>

      {/* ====================================================================
       * 5. Clean Footer
       * ================================================================== */}
      <footer
        className="border-t py-8 px-4 sm:px-6"
        style={{
          borderColor: 'var(--chrome-border)',
          backgroundColor: 'var(--surface-1)',
        }}
      >
        <div className="max-w-[1260px] mx-auto flex flex-col sm:flex-row sm:items-center justify-between gap-4 text-[12.5px] text-fg-muted">
          <div className="flex flex-wrap items-center gap-3">
            <LandingBrandEmblem size={22} />
            <span className="font-bold text-fg">ColabHive</span>
            <span>·</span>
            <span>Apache-2.0 License</span>
            <span>·</span>
            <span>
              {zh
                ? '凭证本地 AES-256-GCM 加密 · 零公网穿透私有直连'
                : 'AES-256-GCM Local Vault · Zero-Tunnel Private Bridge'}
            </span>
          </div>

          <div className="flex flex-wrap items-center gap-4">
            <button
              type="button"
              onClick={() => navigate(consolePath)}
              className="text-fg-secondary hover:text-fg font-medium cursor-pointer bg-transparent border-0 p-0"
            >
              {zh ? '在线控制台' : 'Live Console'}
            </button>
            <a
              href={GITHUB_ARCH_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="text-fg-secondary hover:text-fg no-underline inline-flex items-center gap-1"
            >
              <span>{zh ? '架构规范' : 'Architecture Specs'}</span>
              <ExternalLink className="w-3 h-3" />
            </a>
            <a
              href={GITHUB_SDK_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="text-fg-secondary hover:text-fg no-underline inline-flex items-center gap-1"
            >
              <span>Python SDK</span>
              <ExternalLink className="w-3 h-3" />
            </a>
            <a
              href={GITHUB_RELEASES_URL}
              target="_blank"
              rel="noopener noreferrer"
              className="font-semibold text-[var(--emerald-text)] hover:underline no-underline inline-flex items-center gap-1"
            >
              <Download className="w-3.5 h-3.5" />
              <span>{zh ? '下载发行版' : 'GitHub Releases'}</span>
            </a>
          </div>
        </div>
      </footer>
    </div>
  );
}

export default LandingPage;
