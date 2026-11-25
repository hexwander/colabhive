import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  ArrowLeft,
  Brain,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Copy,
  Cpu,
  ExternalLink,
  Flame,
  Loader2,
  Play,
  RefreshCw,
  Send,
  ShieldCheck,
  Sparkles,
  Square,
  Terminal,
  Zap,
} from 'lucide-react';
import { ReasoningOutputBox } from '../components/llm/ReasoningOutputBox';
import type {
  AcceleratorType,
  Account,
  LLMGatewayStatus,
  LLMModelSpec,
  QuotaSummary,
  WSEvent,
} from '../types';
import {
  normalizeCustomModelInput,
  resolveLLMModelSpec,
  v11Api,
  V11_DEMO_EVENT,
  WS_V11_EVENT,
  type LLMProtocolKind,
} from '../lib/apiV11';
import { isDemoSiteMode } from '../lib/demoSiteAdapter';
import { formatFriendlyAppError } from '../lib/friendlyErrors';
import { useI18n } from '../i18n';
import {
  ActionButton,
  cn,
  InsetWell,
  LayeredCard,
  PageHeader,
  SegmentedTabs,
  StatusPill,
} from '../components/ui/primitives';

export interface LLMDeployPageProps {
  accounts?: Account[];
  summary?: QuotaSummary;
  authRequired?: boolean;
  publicUrl?: string;
  onNavigate?: (to: string) => void;
}

type ModelFilterTab = 'all' | 'free' | 'pro' | 'custom';
type ClientGuideTab = 'sillytavern' | 'cherrystudio' | 'openwebui' | 'coding';

const GPU_CHOICES: Array<{
  id: '' | AcceleratorType;
  labelZh: string;
  labelEn: string;
  hintZh: string;
  hintEn: string;
}> = [
  { id: '', labelZh: '自动匹配显存', labelEn: 'Auto-match VRAM', hintZh: '按所选模型推荐', hintEn: 'Matches model' },
  { id: 'T4', labelZh: 'T4 (16GB · 入门/免费号可用)', labelEn: 'T4 (16GB · Entry / Free Tier)', hintZh: '免费号免费 · 付费号低耗 CU', hintEn: 'Free on Free tier, low CU on paid' },
  { id: 'L4', labelZh: 'L4 (24GB)', labelEn: 'L4 (24GB)', hintZh: '27B / 35B MoE 甜点', hintEn: '27B–35B sweet spot' },
  { id: 'A100', labelZh: 'A100 (40/80GB)', labelEn: 'A100 (40/80GB)', hintZh: '31B 高精长上下文', hintEn: 'High-prec 31B' },
  { id: 'G4', labelZh: 'G4 (96GB Blackwell)', labelEn: 'G4 (96GB Blackwell)', hintZh: 'Ultra 满血旗舰', hintEn: 'Ultra flagship' },
];

const IDLE_MINUTES_OPTIONS = [15, 30, 60, 120] as const;

function isExecutableColabAccount(acc: Account): boolean {
  if (!acc.enabled) return false;
  if (acc.provider && acc.provider !== 'colab') return false;
  return acc.health !== 'AUTH_EXPIRED' && acc.health !== 'REVOKED_BY_USER';
}

function formatContextK(ctx: number): string {
  if (!ctx || ctx <= 0) return '32K';
  const k = Math.round(ctx / 1024);
  return `${k}K`;
}

const GPU_EST_CU_PER_HR: Record<string, number> = {
  G4: 9.66,
  H100: 14.4,
  A100: 8.88,
  L4: 4.82,
  T4: 1.96,
  V6E1: 6.2,
  V5E1: 2.1,
};

function formatElapsedDuration(startedAtIso: string | undefined, nowMs: number, zh: boolean): string {
  if (!startedAtIso) return '';
  const startMs = Date.parse(startedAtIso);
  if (!Number.isFinite(startMs) || startMs <= 0) return '';
  const totalSec = Math.max(0, Math.floor((nowMs - startMs) / 1000));
  const hrs = Math.floor(totalSec / 3600);
  const mins = Math.floor((totalSec % 3600) / 60);
  const secs = totalSec % 60;
  if (hrs > 0) return zh ? `${hrs} 小时 ${mins} 分` : `${hrs}h ${mins}m`;
  if (mins > 0) return zh ? `${mins} 分 ${secs} 秒` : `${mins}m ${secs}s`;
  return zh ? `${secs} 秒` : `${secs}s`;
}

function formatClockMMSS(elapsedSec: number): string {
  const s = Math.max(0, Math.floor(elapsedSec));
  const m = Math.floor(s / 60);
  const r = s % 60;
  return `${String(m).padStart(2, '0')}:${String(r).padStart(2, '0')}`;
}

interface DeployProgressView {
  stageIdx: 0 | 1 | 2 | 3;
  barPct: number;
  downloadPct: number | null;
  liveText: string;
}

function deriveDeployProgress(
  status: LLMGatewayStatus | null,
  busyStart: boolean,
  zh: boolean,
): DeployProgressView {
  const stepZh = (status?.status_step_zh || '').trim();
  const stepEn = (status?.status_step || '').trim();
  const combined = `${stepZh} ${stepEn}`.toLowerCase();
  const pctMatch = /(\d{1,3})%/.exec(`${stepZh} ${stepEn}`);
  const downloadPct = pctMatch ? Math.min(100, Math.max(0, Number(pctMatch[1]))) : null;

  let stageIdx: 0 | 1 | 2 | 3 = 0;
  if (status?.running && status.state === 'ready' && !busyStart) {
    stageIdx = 3;
  } else if (status?.state === 'pulling') {
    if (/预热|载入显存|warming|loading weights into gpu/.test(combined)) {
      stageIdx = 3;
    } else if (
      downloadPct !== null ||
      /下载|拉取|清单|校验|写入|download|pulling|manifest|verifying|writing/.test(combined)
    ) {
      stageIdx = 2;
    } else {
      stageIdx = 1;
    }
  } else {
    stageIdx = 0;
  }

  let barPct = 14;
  if (stageIdx === 1) {
    barPct = 36;
  } else if (stageIdx === 2) {
    if (downloadPct !== null) {
      barPct = Math.min(92, Math.round(40 + downloadPct * 0.50));
    } else if (/校验|写入|verifying|writing/.test(combined)) {
      barPct = 92;
    } else {
      barPct = 44;
    }
  } else if (stageIdx === 3) {
    barPct = status?.running && status.state === 'ready' && !busyStart ? 100 : 95;
  }

  const liveText =
    (zh ? stepZh : stepEn) ||
    (zh
      ? '正在向 Google Colab 申请可用显卡并探测库存（每档约 10~28 秒）...'
      : 'Requesting Colab GPU and probing regional availability...');

  return { stageIdx, barPct, downloadPct, liveText };
}

export function LLMDeployPage({
  accounts = [],
  authRequired = false,
  publicUrl,
  onNavigate,
}: LLMDeployPageProps = {}) {
  const { locale, displayEmail } = useI18n();
  const zh = locale === 'zh-CN';

  const [catalog, setCatalog] = useState<LLMModelSpec[]>([]);
  const [status, setStatus] = useState<LLMGatewayStatus | null>(null);
  const [loading, setLoading] = useState(true);

  const [filterTab, setFilterTab] = useState<ModelFilterTab>('all');
  const [selectedModelId, setSelectedModelId] = useState<string>('');
  const userPickedModelRef = useRef(false);
  const [customModelInput, setCustomModelInput] = useState<string>('');
  const [selectedGpu, setSelectedGpu] = useState<'' | AcceleratorType>('');
  const [selectedAccountId, setSelectedAccountId] = useState<string>('');
  const [idleMinutes, setIdleMinutes] = useState<number>(30);

  const [busyAction, setBusyAction] = useState<'start' | 'stop' | null>(null);
  const [feedback, setFeedback] = useState<{ tone: 'emerald' | 'coral' | 'amber'; text: string } | null>(null);

  const [clientTab, setClientTab] = useState<ClientGuideTab>('cherrystudio');
  const [copiedKey, setCopiedKey] = useState<string | null>(null);

  // Lightweight 1-line connectivity & output smoke test (OpenAI / Responses / Anthropic)
  const [pingProtocol, setPingProtocol] = useState<LLMProtocolKind>('openai');
  const [pingThink, setPingThink] = useState<boolean>(true);
  const [pingPrompt, setPingPrompt] = useState<string>(zh ? '你好' : 'Hello');
  const [pinging, setPinging] = useState(false);
  const [pingThinking, setPingThinking] = useState<string>('');
  const [pingReply, setPingReply] = useState<string>('');
  const [pingAborted, setPingAborted] = useState<boolean>(false);
  const [pingThinkDurationMs, setPingThinkDurationMs] = useState<number | undefined>(undefined);
  const [pingMeta, setPingMeta] = useState<{
    ms: number;
    ttftMs?: number;
    tps?: number;
    protocol: LLMProtocolKind;
    aborted?: boolean;
  } | null>(null);
  const [pingError, setPingError] = useState<string>('');
  const pingAbortRef = useRef<AbortController | null>(null);
  const pingRunRef = useRef<{
    t0: number;
    proto: LLMProtocolKind;
    ttftMs?: number;
    thinkT0?: number;
    thinkDone?: boolean;
  } | null>(null);
  const [nowTickMs, setNowTickMs] = useState<number>(() => Date.now());
  const [deployStartedAtMs, setDeployStartedAtMs] = useState<number | null>(null);
  const [deployLogsOpen, setDeployLogsOpen] = useState<boolean>(true);
  const deployLogBoxRef = useRef<HTMLDivElement | null>(null);
  const topCardRef = useRef<HTMLDivElement | null>(null);
  const stopRequestedRef = useRef(false);

  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      pingAbortRef.current?.abort();
    };
  }, []);

  const colabAccounts = useMemo(
    () => accounts.filter(isExecutableColabAccount),
    [accounts],
  );
  const allAccountsConcurrencyFull = useMemo(
    () =>
      colabAccounts.length > 0 &&
      colabAccounts.every((acc) => {
        if (acc.health !== 'MAX_CONCURRENCY') return false;
        if (acc.cooldown_until) {
          const untilMs = Date.parse(acc.cooldown_until);
          if (Number.isFinite(untilMs) && untilMs <= nowTickMs) return false;
        }
        return true;
      }),
    [colabAccounts, nowTickMs],
  );
  const totalPoolActiveGPUs = useMemo(
    () => colabAccounts.reduce((sum, acc) => sum + Math.max(0, acc.active_assignments || 0), 0),
    [colabAccounts],
  );

  const refreshState = useCallback(async () => {
    try {
      const [cat, st] = await Promise.all([
        v11Api.getLLMCatalog(),
        v11Api.getLLMStatus(),
      ]);
      if (!mountedRef.current) return;
      setCatalog(cat);
      setStatus(st);
      if (st.idle_auto_stop_min && st.idle_auto_stop_min >= 5) {
        setIdleMinutes(st.idle_auto_stop_min);
      }
      if (st.running && st.active_model_id) {
        if (!userPickedModelRef.current) {
          const inCatalog = cat.some((m) => m.id === st.active_model_id);
          if (inCatalog) {
            setSelectedModelId(st.active_model_id);
          } else {
            setFilterTab('custom');
            setCustomModelInput((prev) => prev || st.active_model_id || '');
          }
        }
      } else if (cat.length > 0) {
        setSelectedModelId((prev) => (prev && cat.some((m) => m.id === prev) ? prev : cat[0].id));
      }
    } catch (err) {
      if (!mountedRef.current) return;
      setFeedback({
        tone: 'coral',
        text: err instanceof Error ? err.message : zh ? '读取部署状态失败' : 'Failed to load status',
      });
    } finally {
      if (mountedRef.current) setLoading(false);
    }
  }, [zh]);

  useEffect(() => {
    void refreshState();
    const onDemoChange = () => void refreshState();
    const onWsV11 = (e: Event) => {
      const detail = (e as CustomEvent<WSEvent>).detail;
      if (detail?.type === 'llm_gateway_updated' && detail.payload) {
        const nextSt = detail.payload as LLMGatewayStatus;
        setStatus(nextSt);
        if (nextSt.running && nextSt.active_model_id && !userPickedModelRef.current) {
          setSelectedModelId(nextSt.active_model_id);
        }
        return;
      }
      void refreshState();
    };
    window.addEventListener(V11_DEMO_EVENT, onDemoChange);
    window.addEventListener(WS_V11_EVENT, onWsV11);
    const poll = window.setInterval(() => {
      if (document.visibilityState === 'visible') {
        void v11Api.getLLMStatus().then((st) => {
          if (mountedRef.current) setStatus(st);
        }).catch(() => undefined);
      }
    }, 8000);
    return () => {
      window.removeEventListener(V11_DEMO_EVENT, onDemoChange);
      window.removeEventListener(WS_V11_EVENT, onWsV11);
      window.clearInterval(poll);
    };
  }, [refreshState]);

  const customParsed = useMemo(() => {
    if (!customModelInput.trim()) return null;
    return normalizeCustomModelInput(customModelInput);
  }, [customModelInput]);

  const effectiveModelId = useMemo(() => {
    if (filterTab === 'custom') {
      return customParsed?.ok ? customParsed.modelId : '';
    }
    return selectedModelId || catalog[0]?.id || 'qwen3.5-9b-heretic';
  }, [filterTab, customParsed, selectedModelId, catalog]);

  const selectedSpec = useMemo(() => {
    if (!effectiveModelId) return null;
    return (
      catalog.find((m) => m.id === effectiveModelId) ??
      resolveLLMModelSpec(effectiveModelId, catalog)
    );
  }, [effectiveModelId, catalog]);

  const filteredCatalog = useMemo(() => {
    if (filterTab === 'free') return catalog.filter((m) => m.free_tier_ready);
    if (filterTab === 'pro') return catalog.filter((m) => !m.free_tier_ready);
    return catalog;
  }, [catalog, filterTab]);

  const resolvedOrigin = useMemo(() => {
    const raw = (status?.openai_base_url || '').replace(/\/v1\/?$/, '').trim();
    if (raw) return raw;
    if (publicUrl) return publicUrl.replace(/\/+$/, '');
    if (typeof window !== 'undefined' && window.location.origin.startsWith('http')) {
      return window.location.origin;
    }
    return 'http://127.0.0.1:7860';
  }, [status?.openai_base_url, publicUrl]);

  const openaiBaseUrl = `${resolvedOrigin}/v1`;
  const anthropicBaseUrl = (status?.anthropic_base_url || '').replace(/\/+$/, '') || resolvedOrigin;
  const activeClientModelId = status?.running && status.active_model_id
    ? status.active_model_id
    : effectiveModelId || 'qwen3.8-27b-abliterated';

  const isDeployingNow =
    busyAction === 'start' ||
    status?.state === 'starting' ||
    status?.state === 'pulling' ||
    status?.state === 'failover';

  useEffect(() => {
    if (!status?.running && !status?.session_id && !isDeployingNow) return;
    setNowTickMs(Date.now());
    const id = window.setInterval(() => setNowTickMs(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [status?.running, status?.session_id, isDeployingNow]);

  // Fast 1.5s status poll during deployment so every GPU probe & GGUF download % updates smoothly
  useEffect(() => {
    if (!isDeployingNow) return;
    if (deployStartedAtMs === null) {
      setDeployStartedAtMs(Date.now());
    }
    const fastPoll = window.setInterval(() => {
      void v11Api.getLLMStatus().then((st) => {
        if (mountedRef.current) setStatus(st);
      }).catch(() => undefined);
    }, 1500);
    return () => window.clearInterval(fastPoll);
  }, [isDeployingNow, deployStartedAtMs]);

  useEffect(() => {
    if (!isDeployingNow && deployStartedAtMs !== null) {
      setDeployStartedAtMs(null);
    }
  }, [isDeployingNow, deployStartedAtMs]);

  const deployLogs = status?.deploy_logs ?? [];

  // Auto-open live logs while deploying or on error, and auto-scroll smoothly as new lines arrive.
  useEffect(() => {
    if (isDeployingNow || status?.state === 'error') {
      setDeployLogsOpen(true);
    }
  }, [isDeployingNow, status?.state]);

  useEffect(() => {
    if (!deployLogsOpen || !deployLogBoxRef.current) return;
    deployLogBoxRef.current.scrollTop = deployLogBoxRef.current.scrollHeight;
  }, [deployLogsOpen, deployLogs.length]);

  const copyText = useCallback(async (key: string, val: string) => {
    try {
      await navigator.clipboard.writeText(val);
      setCopiedKey(key);
      window.setTimeout(() => {
        if (mountedRef.current) setCopiedKey((cur) => (cur === key ? null : cur));
      }, 1800);
    } catch {
      // ignore clipboard errors
    }
  }, []);

  const handleDeploy = async () => {
    if (filterTab === 'custom') {
      if (!customModelInput.trim()) {
        setFeedback({
          tone: 'amber',
          text: zh
            ? '请先输入 Hugging Face GGUF 仓库地址（例如 hf.co/owner/repo:Q4_K_M）'
            : 'Enter a Hugging Face GGUF repo (e.g. hf.co/owner/repo:Q4_K_M)',
        });
        return;
      }
      if (customParsed && !customParsed.ok) {
        setFeedback({ tone: 'coral', text: zh ? customParsed.errorZh : customParsed.errorEn });
        return;
      }
    }
    if (!effectiveModelId) return;

    stopRequestedRef.current = false;
    setBusyAction('start');
    setDeployStartedAtMs(Date.now());
    setFeedback(null);
    // Scroll smoothly to the top live progress card so the user immediately sees step-by-step progress
    window.requestAnimationFrame(() => {
      topCardRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
    try {
      const next = await v11Api.startLLM({
        model_id: effectiveModelId,
        preferred_gpu: selectedGpu || undefined,
        account_id: selectedAccountId || undefined,
        strategy: selectedSpec?.free_tier_ready && (!selectedGpu || selectedGpu === 'T4')
          ? 'free-first'
          : 'waste-zero',
        idle_auto_stop_min: idleMinutes,
        auto_failover: true,
      });
      if (!mountedRef.current) return;
      setStatus(next);
      setFeedback({
        tone: 'emerald',
        text: zh
          ? `部署成功！${next.active_model_name || next.active_model_id} 已在 ${next.assigned_gpu || 'GPU'} 就绪，直接把 ${openaiBaseUrl} 填进客户端即可使用。`
          : `Deployed ${next.active_model_id} on ${next.assigned_gpu || 'GPU'}. Point your client to ${openaiBaseUrl}.`,
      });
    } catch (err) {
      if (!mountedRef.current) return;
      if (stopRequestedRef.current) return;
      setFeedback({
        tone: 'coral',
        text: err instanceof Error ? err.message : zh ? '部署失败' : 'Deployment failed',
      });
    } finally {
      if (mountedRef.current) setBusyAction((cur) => (cur === 'start' ? null : cur));
    }
  };

  const handleStop = async () => {
    stopRequestedRef.current = true;
    setBusyAction('stop');
    setFeedback(null);
    try {
      const next = await v11Api.stopLLM();
      if (!mountedRef.current) return;
      setStatus(next);
      setFeedback({
        tone: 'emerald',
        text: zh
          ? '已停止模型服务并释放云端显卡，当前 0 额度消耗。'
          : 'Stopped model deployment and released cloud GPU (0 burn).',
      });
    } catch (err) {
      if (!mountedRef.current) return;
      setFeedback({
        tone: 'coral',
        text: err instanceof Error ? err.message : zh ? '停止失败' : 'Stop failed',
      });
    } finally {
      if (mountedRef.current) setBusyAction(null);
    }
  };

  const handleAbortPing = () => {
    const run = pingRunRef.current;
    pingAbortRef.current?.abort();
    pingAbortRef.current = null;
    if (!mountedRef.current) return;
    setPinging(false);
    setPingAborted(true);
    if (run) {
      if (run.thinkT0 && !run.thinkDone) {
        run.thinkDone = true;
        setPingThinkDurationMs(Math.max(100, Math.round(performance.now() - run.thinkT0)));
      }
      setPingMeta({
        ms: Math.max(1, Math.round(performance.now() - run.t0)),
        ttftMs: run.ttftMs,
        protocol: run.proto,
        aborted: true,
      });
    }
  };

  const handleQuickPing = async () => {
    if (!pingPrompt.trim() || pinging) return;
    pingAbortRef.current?.abort();
    const ctrl = new AbortController();
    pingAbortRef.current = ctrl;
    const t0 = performance.now();
    const proto = pingProtocol;
    pingRunRef.current = { t0, proto };
    setPinging(true);
    setPingThinking('');
    setPingReply('');
    setPingAborted(false);
    setPingThinkDurationMs(undefined);
    setPingError('');
    setPingMeta(null);
    try {
      const summary = await v11Api.probeLLMProtocol(
        proto,
        pingPrompt.trim(),
        activeClientModelId,
        (delta) => {
          if (!mountedRef.current || ctrl.signal.aborted) return;
          const run = pingRunRef.current;
          if (run) {
            if (run.ttftMs === undefined) {
              run.ttftMs = Math.max(1, Math.round(performance.now() - run.t0));
            }
            if (run.thinkT0 && !run.thinkDone) {
              run.thinkDone = true;
              setPingThinkDurationMs(Math.max(100, Math.round(performance.now() - run.thinkT0)));
            }
          }
          setPingReply((prev) => prev + delta);
        },
        ctrl.signal,
        {
          think: pingThink,
          maxTokens: 512,
          onReasoningDelta: (thinkChunk) => {
            if (!mountedRef.current || ctrl.signal.aborted) return;
            const run = pingRunRef.current;
            if (run) {
              if (run.ttftMs === undefined) {
                run.ttftMs = Math.max(1, Math.round(performance.now() - run.t0));
              }
              if (!run.thinkT0) {
                run.thinkT0 = performance.now();
              }
              setPingThinkDurationMs(Math.max(100, Math.round(performance.now() - run.thinkT0)));
            }
            setPingThinking((prev) => prev + thinkChunk);
          },
        },
      );
      if (!mountedRef.current || ctrl.signal.aborted) return;
      const run = pingRunRef.current;
      if (run?.thinkT0 && !run.thinkDone) {
        run.thinkDone = true;
        setPingThinkDurationMs(Math.max(100, Math.round(performance.now() - run.thinkT0)));
      }
      setPingMeta({
        ms: Math.round(summary.durationMs || performance.now() - t0),
        ttftMs: summary.ttftMs ?? run?.ttftMs,
        tps: summary.tokensPerSec,
        protocol: proto,
      });
    } catch (err) {
      if (!mountedRef.current) return;
      if (ctrl.signal.aborted || (err instanceof DOMException && err.name === 'AbortError')) return;
      setPingError(
        err instanceof Error ? err.message : zh ? '联通测试失败' : 'Connectivity test failed',
      );
    } finally {
      if (mountedRef.current && pingAbortRef.current === ctrl) {
        setPinging(false);
      }
    }
  };

  const isRunning = Boolean(status?.running && status.state === 'ready' && busyAction !== 'start');
  const isBusyState = isDeployingNow;

  const deployProgress = useMemo(
    () => deriveDeployProgress(status, busyAction === 'start', zh),
    [status, busyAction, zh],
  );
  const deployElapsedSec = deployStartedAtMs
    ? Math.max(0, Math.floor((nowTickMs - deployStartedAtMs) / 1000))
    : 0;

  const deployStages = useMemo(
    () =>
      zh
        ? [
            { id: 0, label: '申请 Colab 显卡' },
            { id: 1, label: '启动 Ollama 引擎' },
            { id: 2, label: '拉取模型权重' },
            { id: 3, label: '暴露 /v1 就绪' },
          ]
        : [
            { id: 0, label: 'Allocate GPU' },
            { id: 1, label: 'Boot Ollama' },
            { id: 2, label: 'Pull Weights' },
            { id: 3, label: 'Expose /v1' },
          ],
    [zh],
  );

  const statusTone = isRunning
    ? 'emerald'
    : isBusyState
      ? 'amber'
      : status?.state === 'error'
        ? 'coral'
        : 'neutral';

  const nav = (to: string) => {
    if (onNavigate) onNavigate(to);
    else if (typeof window !== 'undefined') window.location.assign(to);
  };

  const curlSnippet = `curl ${openaiBaseUrl}/chat/completions \\
  -H "Content-Type: application/json" \\
  -H "Authorization: Bearer ${authRequired ? '<YOUR_ADMIN_TOKEN>' : 'colabhive-local'}" \\
  -d '{"model":"${activeClientModelId}","messages":[{"role":"user","content":"Hello!"}]}'`;

  return (
    <div className="space-y-5 pb-10">
      <PageHeader
        title={zh ? '无审查开源大模型一键部署' : 'Uncensored Open-Weight LLM Deployer'}
        subtitle={
          zh
            ? '精选 2026 社区下载量最高、口碑最好的 Heretic / Abliterated 正交去审查 GGUF 模型（保留原版智商、零说教零拒答），一键部署到 Colab 显卡并暴露本机 /v1 接口，直连酒馆、Cherry Studio 与 Cursor。'
            : 'Deploy top-downloaded 2026 Heretic & Abliterated uncensored open-weight GGUFs onto pooled Colab GPUs and expose a private localhost /v1 endpoint for SillyTavern, Cherry Studio, Open WebUI, and Cursor.'
        }
        badge={
          <StatusPill tone={statusTone} dot pulse={isRunning || isBusyState}>
            {isRunning
              ? zh
                ? `运行中 · ${status?.assigned_gpu || 'GPU'}`
                : `Live · ${status?.assigned_gpu || 'GPU'}`
              : isBusyState
                ? zh
                  ? '正在分配显卡与加载权重…'
                  : 'Provisioning & loading…'
                : zh
                  ? '待机 · 0 消耗'
                  : 'Idle · 0 Burn'}
          </StatusPill>
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <ActionButton
              size="sm"
              variant="secondary"
              icon={<ArrowLeft />}
              onClick={() => nav('/apps')}
            >
              {zh ? '返回应用商店' : 'App Store'}
            </ActionButton>
            <ActionButton
              size="sm"
              variant="secondary"
              icon={<RefreshCw className={loading ? 'animate-spin' : undefined} />}
              onClick={() => void refreshState()}
            >
              {zh ? '刷新状态' : 'Refresh'}
            </ActionButton>
          </div>
        }
      />

      {feedback && (() => {
        const friendlyErr = feedback.tone === 'coral' ? formatFriendlyAppError(feedback.text, zh) : null;
        return (
          <div
            role={feedback.tone === 'coral' ? 'alert' : 'status'}
            className="rounded-2xl border px-4 py-3 text-[12.5px] font-medium flex items-start justify-between gap-3"
            style={{
              backgroundColor:
                feedback.tone === 'emerald'
                  ? 'var(--emerald-soft)'
                  : feedback.tone === 'amber'
                    ? 'var(--amber-soft)'
                    : 'var(--coral-soft)',
              borderColor:
                feedback.tone === 'emerald'
                  ? 'var(--emerald-border)'
                  : feedback.tone === 'amber'
                    ? 'var(--amber-border)'
                    : 'var(--coral-border)',
              color:
                feedback.tone === 'emerald'
                  ? 'var(--emerald-text)'
                  : feedback.tone === 'amber'
                    ? 'var(--amber-text)'
                    : 'var(--coral-text)',
            }}
          >
            <div className="flex items-start gap-2.5 min-w-0 flex-1">
              {feedback.tone === 'emerald' ? (
                <CheckCircle2 className="w-4 h-4 shrink-0 mt-0.5" />
              ) : (
                <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
              )}
              {friendlyErr ? (
                <div className="min-w-0 flex-1 space-y-1 font-normal">
                  <div className="font-semibold text-[13px] leading-snug">{friendlyErr.title}</div>
                  <div className="opacity-90 leading-relaxed">{friendlyErr.summary}</div>
                  {friendlyErr.suggestion && (
                    <div className="opacity-95 leading-relaxed">{friendlyErr.suggestion}</div>
                  )}
                  {friendlyErr.raw && friendlyErr.raw !== friendlyErr.summary && (
                    <details className="text-[11.5px] opacity-80 pt-0.5">
                      <summary className="cursor-pointer select-none hover:opacity-100">
                        {zh ? '查看技术详情' : 'Technical details'}
                      </summary>
                      <pre className="mt-1 p-2 rounded-lg bg-black/10 text-[11px] whitespace-pre-wrap break-all font-mono">
                        {friendlyErr.raw}
                      </pre>
                    </details>
                  )}
                </div>
              ) : (
                <span className="leading-relaxed">{feedback.text}</span>
              )}
            </div>
            <button
              type="button"
              onClick={() => setFeedback(null)}
              className="shrink-0 whitespace-nowrap text-[12px] underline opacity-80 hover:opacity-100 cursor-pointer bg-transparent border-0 pt-0.5"
            >
              {zh ? '知道了' : 'Dismiss'}
            </button>
          </div>
        );
      })()}

      {/* 1. Live Deployment Status Bar + Live 4-Stage Progress + Active GPU Reclaim Warning + Top Quick Smoke Test */}
      <div ref={topCardRef} className="scroll-mt-4">
        <LayeredCard
          padding="md"
          className="rounded-[20px]"
          style={
            isRunning || isBusyState
              ? {
                  borderColor: status?.free_tier_active && isRunning ? 'var(--emerald-border)' : 'var(--amber-border)',
                }
              : undefined
          }
        >
          <div className="space-y-4">
            <div className="flex flex-col lg:flex-row lg:items-center justify-between gap-4">
              <div className="space-y-1.5 min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-[12px] font-semibold text-fg-muted">
                    {zh ? '当前云端推理实例' : 'Active Cloud Inference Node'}
                  </span>
                  {isBusyState && (
                    <StatusPill size="xs" tone="amber" dot pulse>
                      {zh
                        ? `部署中 · ${formatClockMMSS(deployElapsedSec)}`
                        : `Deploying · ${formatClockMMSS(deployElapsedSec)}`}
                    </StatusPill>
                  )}
                  {status?.free_tier_active && isRunning && (
                    <StatusPill size="xs" tone="emerald">
                      {zh ? '0 元免费卡运行中' : '$0 Free GPU'}
                    </StatusPill>
                  )}
                  {!status?.free_tier_active && isRunning && status?.assigned_gpu && (
                    <StatusPill size="xs" tone="amber" dot pulse>
                      {zh
                        ? `付费显卡运行中 · 约 ${(GPU_EST_CU_PER_HR[status.assigned_gpu] ?? 4.82).toFixed(2)} CU/h`
                        : `Paid GPU active · ~${(GPU_EST_CU_PER_HR[status.assigned_gpu] ?? 4.82).toFixed(2)} CU/h`}
                    </StatusPill>
                  )}
                  {isRunning && status?.idle_auto_stop_min ? (
                    <StatusPill size="xs" tone="sky">
                      {zh
                        ? `闲置 ${status.idle_auto_stop_min} 分钟兜底停机`
                        : `Auto-stop after ${status.idle_auto_stop_min}m idle`}
                    </StatusPill>
                  ) : null}
                </div>
                <div className="text-[15.5px] font-bold text-fg truncate">
                  {(() => {
                    const activeCatalogSpec = status?.active_model_id
                      ? catalog.find((m) => m.id === status.active_model_id)
                      : undefined;
                    if (isRunning) {
                      return zh
                        ? activeCatalogSpec?.display_name_zh || status?.active_model_name || status?.active_model_id
                        : activeCatalogSpec?.display_name || status?.active_model_name || status?.active_model_id;
                    }
                    if (isBusyState) {
                      return zh
                        ? `正在部署 ${activeCatalogSpec?.display_name_zh || status?.active_model_name || selectedSpec?.display_name_zh || effectiveModelId}…`
                        : `Deploying ${activeCatalogSpec?.display_name || status?.active_model_id || selectedSpec?.display_name || effectiveModelId}…`;
                    }
                    return zh
                      ? '尚未部署模型 — 从下方任选一款无审查模型一键启动'
                      : 'No model deployed yet — pick an uncensored GGUF below to launch';
                  })()}
                </div>
                <div className="text-[12px] text-fg-secondary flex flex-wrap items-center gap-x-3 gap-y-1 ch-num">
                  <span>
                    {isBusyState
                      ? zh
                        ? `阶段 ${deployProgress.stageIdx + 1}/4 · ${deployStages[deployProgress.stageIdx]?.label || ''}`
                        : `Step ${deployProgress.stageIdx + 1}/4 · ${deployStages[deployProgress.stageIdx]?.label || ''}`
                      : zh
                        ? status?.status_step_zh || '待机中'
                        : status?.status_step || 'Stopped'}
                  </span>
                  {isBusyState && status?.assigned_gpu && (
                    <span className="text-fg-muted">· {status.assigned_gpu}</span>
                  )}
                  {(isRunning || (isBusyState && status?.account_label)) &&
                    status?.account_label &&
                    !(zh ? status?.status_step_zh : status?.status_step)?.includes(status.account_label) && (
                      <span className="text-fg-muted">· {status.account_label}</span>
                    )}
                  {isRunning && status?.started_at && (
                    <span className="text-fg-muted">
                      · {zh ? '已运行 ' : 'Uptime '}
                      {formatElapsedDuration(status.started_at, nowTickMs, zh)}
                    </span>
                  )}
                  {isRunning && (status?.avg_tokens_per_sec ?? 0) > 0 && (
                    <span style={{ color: 'var(--emerald-text)' }}>
                      · {status?.avg_tokens_per_sec?.toFixed(1)} tok/s
                    </span>
                  )}
                </div>
              </div>

              <div className="flex flex-wrap items-center gap-2 shrink-0">
                <button
                  type="button"
                  onClick={() => void copyText('top-url', openaiBaseUrl)}
                  className="inline-flex items-center gap-1.5 h-8.5 px-3 rounded-xl border text-[12px] font-medium ch-num text-fg bg-surface-1 hover:border-line-strong cursor-pointer transition-colors"
                  style={{ borderColor: 'var(--border-subtle)' }}
                  title={zh ? '复制 OpenAI / Responses 接口地址' : 'Copy OpenAI / Responses endpoint'}
                >
                  {copiedKey === 'top-url' ? (
                    <Check className="w-3.5 h-3.5" style={{ color: 'var(--emerald-text)' }} />
                  ) : (
                    <Copy className="w-3.5 h-3.5 text-fg-muted" />
                  )}
                  <span>{openaiBaseUrl}</span>
                </button>

                {(isRunning || isBusyState || status?.session_id) && (
                  <ActionButton
                    size="sm"
                    variant="danger"
                    icon={<Square />}
                    loading={busyAction === 'stop'}
                    disabled={busyAction === 'stop'}
                    onClick={() => void handleStop()}
                  >
                    {isBusyState && !isRunning
                      ? zh
                        ? '取消部署并释放显卡'
                        : 'Cancel & Release GPU'
                      : zh
                        ? '立即停止并回收显卡'
                        : 'Stop & Reclaim GPU'}
                  </ActionButton>
                )}
              </div>
            </div>

            {/* Live 4-Stage Deployment Progress Panel (real-time GPU probe + Ollama pull %/GB/MB/s) */}
            {isBusyState && (
              <InsetWell padding="sm" className="space-y-3" data-testid="llm-deploy-progress">
                <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
                  {deployStages.map((stage) => {
                    const done = stage.id < deployProgress.stageIdx;
                    const active = stage.id === deployProgress.stageIdx;
                    return (
                      <div
                        key={stage.id}
                        className={cn(
                          'flex items-center gap-2 rounded-xl border px-2.5 py-2 text-[11.5px] transition-colors',
                          done
                            ? 'font-medium'
                            : active
                              ? 'font-semibold shadow-xs'
                              : 'text-fg-muted bg-surface-1/60',
                        )}
                        style={
                          done
                            ? {
                                backgroundColor: 'var(--emerald-soft)',
                                borderColor: 'var(--emerald-border)',
                                color: 'var(--emerald-text)',
                              }
                            : active
                              ? {
                                  backgroundColor: 'var(--amber-soft)',
                                  borderColor: 'var(--amber-border)',
                                  color: 'var(--amber-text)',
                                }
                              : { borderColor: 'var(--border-faint)' }
                        }
                      >
                        {done ? (
                          <CheckCircle2 className="w-3.5 h-3.5 shrink-0" />
                        ) : active ? (
                          <Loader2 className="w-3.5 h-3.5 shrink-0 animate-spin" />
                        ) : (
                          <span
                            className="w-3.5 h-3.5 rounded-full border inline-flex items-center justify-center text-[9.5px] ch-num shrink-0"
                            style={{ borderColor: 'var(--border-subtle)' }}
                          >
                            {stage.id + 1}
                          </span>
                        )}
                        <span className="truncate">{stage.label}</span>
                      </div>
                    );
                  })}
                </div>

                <div className="space-y-1.5">
                  <div className="flex flex-wrap items-center justify-between gap-2 text-[12px]">
                    <div className="flex items-center gap-2 min-w-0 font-medium text-fg">
                      <Loader2 className="w-3.5 h-3.5 shrink-0 animate-spin" style={{ color: 'var(--amber-text)' }} />
                      <span className="truncate">{deployProgress.liveText}</span>
                    </div>
                    <div className="text-[11.5px] ch-num text-fg-secondary shrink-0">
                      {deployProgress.downloadPct !== null ? `${deployProgress.downloadPct}% · ` : ''}
                      {zh ? `已耗时 ${formatClockMMSS(deployElapsedSec)}` : `Elapsed ${formatClockMMSS(deployElapsedSec)}`}
                    </div>
                  </div>
                  <div
                    className="h-2 w-full rounded-full overflow-hidden"
                    style={{ backgroundColor: 'var(--surface-3, rgba(120,113,108,0.16))' }}
                  >
                    <div
                      className="h-full rounded-full transition-[width] duration-500 ease-out"
                      style={{
                        width: `${deployProgress.barPct}%`,
                        backgroundColor: deployProgress.stageIdx >= 2 ? 'var(--emerald)' : 'var(--accent)',
                      }}
                    />
                  </div>
                </div>

                <div className="text-[11.5px] text-fg-muted leading-relaxed">
                  {deployProgress.stageIdx === 0
                    ? zh
                      ? '提示：Google Colab 每档显卡库存探测最长约 28 秒，若缺卡或账号在跑实验占满并发槽位，系统会自动切换下一档显卡与账号；随时可点右上角「取消部署并释放显卡」。'
                      : 'Tip: Each Colab GPU tier probe takes up to ~28s and auto-falls back across tiers/accounts if busy or at concurrency limit. Click "Cancel & Release GPU" anytime to abort.'
                    : deployProgress.stageIdx === 1
                      ? zh
                        ? '已成功拿到云端显卡，正在远程安装并启动 Ollama 推理守护进程（通常 15~30 秒），完成后将立即开始拉取模型权重。'
                        : 'Cloud GPU allocated — installing & starting remote Ollama daemon (~15–30s) before pulling model weights.'
                      : zh
                        ? '正在从 Hugging Face 直连下载 GGUF 权重到云端显卡内存，百分比与实时下载速度每秒自动刷新。'
                        : 'Downloading GGUF weights directly from Hugging Face into cloud GPU memory — percentage and speed refresh live.'}
                </div>
              </InsetWell>
            )}

            {/* Collapsible Live / Recent Deployment Logs Terminal */}
            {(isBusyState || deployLogs.length > 0) && (
              <div
                className="rounded-xl border overflow-hidden"
                style={{
                  borderColor:
                    status?.state === 'error'
                      ? 'var(--coral-border)'
                      : 'var(--border-subtle)',
                  backgroundColor: 'var(--surface-2)',
                }}
                data-testid="llm-deploy-logs"
              >
                <div
                  className="px-3 py-2 flex items-center justify-between gap-2 text-[11.5px]"
                  style={{
                    borderBottom: deployLogsOpen ? '1px solid var(--border-faint)' : undefined,
                  }}
                >
                  <button
                    type="button"
                    onClick={() => setDeployLogsOpen((v) => !v)}
                    className="inline-flex items-center gap-1.5 font-semibold text-fg hover:text-accent cursor-pointer transition-colors"
                  >
                    {deployLogsOpen ? (
                      <ChevronDown className="w-3.5 h-3.5 text-fg-muted" />
                    ) : (
                      <ChevronRight className="w-3.5 h-3.5 text-fg-muted" />
                    )}
                    <Terminal className="w-3.5 h-3.5 text-fg-muted" />
                    <span>
                      {isBusyState
                        ? zh
                          ? '实时部署日志'
                          : 'Live Deploy Logs'
                        : zh
                          ? '最近部署日志'
                          : 'Last Deploy Logs'}
                    </span>
                    <span
                      className="px-1.5 py-0.5 rounded-md text-[10.5px] ch-num font-medium"
                      style={{
                        backgroundColor: 'var(--surface-3, rgba(120,113,108,0.14))',
                        color: 'var(--text-secondary)',
                      }}
                    >
                      {zh ? `${deployLogs.length} 条` : `${deployLogs.length} lines`}
                    </span>
                  </button>

                  {deployLogs.length > 0 && (
                    <button
                      type="button"
                      onClick={() => void copyText('deploy-logs', deployLogs.join('\n'))}
                      className="inline-flex items-center gap-1 px-2 py-1 rounded-lg border text-[11px] font-medium text-fg-secondary hover:text-fg bg-surface-1 cursor-pointer transition-colors"
                      style={{ borderColor: 'var(--border-faint)' }}
                    >
                      {copiedKey === 'deploy-logs' ? (
                        <Check className="w-3 h-3" style={{ color: 'var(--emerald-text)' }} />
                      ) : (
                        <Copy className="w-3 h-3" />
                      )}
                      <span>
                        {copiedKey === 'deploy-logs'
                          ? zh
                            ? '已复制'
                            : 'Copied'
                          : zh
                            ? '复制日志'
                            : 'Copy logs'}
                      </span>
                    </button>
                  )}
                </div>

                {deployLogsOpen && (
                  <div
                    ref={deployLogBoxRef}
                    className="max-h-52 overflow-y-auto px-3.5 py-2.5 font-mono text-[11.5px] leading-relaxed space-y-1"
                    style={{
                      backgroundColor: '#18181B',
                      color: '#F4F4F5',
                    }}
                  >
                    {deployLogs.length === 0 ? (
                      <div className="text-zinc-400 animate-pulse">
                        {zh
                          ? '等待云端节点返回部署日志…'
                          : 'Waiting for cloud GPU node deployment logs…'}
                      </div>
                    ) : (
                      deployLogs.map((line, idx) => {
                        const lower = line.toLowerCase();
                        const isErrLine =
                          line.includes('失败') ||
                          lower.includes('error') ||
                          lower.includes('failed');
                        const isSuccessLine =
                          line.includes('已自动释放') ||
                          line.includes('部署完成') ||
                          line.includes('预热完成') ||
                          line.includes('已分配云端显卡');
                        const isEngineRaw = line.includes('[Ollama]');
                        return (
                          <div
                            key={`${idx}-${line.slice(0, 24)}`}
                            className="break-all whitespace-pre-wrap"
                            style={{
                              color: isErrLine
                                ? '#FCA5A5'
                                : isSuccessLine
                                  ? '#6EE7B7'
                                  : isEngineRaw
                                    ? '#A1A1AA'
                                    : '#F4F4F5',
                            }}
                          >
                            {line}
                          </div>
                        );
                      })
                    )}
                  </div>
                )}
              </div>
            )}

            {/* Prominent Manual GPU Reclaim Reminder when an instance is actually allocated */}
            {(isRunning || Boolean(status?.session_id)) && (
              <div
                className="rounded-xl border px-3.5 py-2.5 flex flex-col sm:flex-row sm:items-center justify-between gap-2.5 text-[12px] leading-relaxed"
                style={
                  status?.free_tier_active
                    ? {
                        backgroundColor: 'var(--emerald-soft)',
                        borderColor: 'var(--emerald-border)',
                        color: 'var(--emerald-text)',
                      }
                    : {
                        backgroundColor: 'var(--amber-soft)',
                        borderColor: 'var(--amber-border)',
                        color: 'var(--amber-text)',
                      }
                }
              >
                <div className="flex items-start gap-2 min-w-0">
                  <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
                  <span>
                    {status?.free_tier_active
                      ? zh
                        ? `当前占用免费 T4 显卡（0 付费 CU 消耗）。对话结束后建议点击右上角「立即停止并回收显卡」，为下次留出更多免费时段（未手动关闭时将于闲置 ${status?.idle_auto_stop_min || idleMinutes} 分钟后自动回收）。`
                        : `Running on $0 Free T4. Click "Stop & Reclaim GPU" when done to preserve your free window (idle auto-stop after ${status?.idle_auto_stop_min || idleMinutes}m).`
                      : zh
                        ? `云端 ${status?.assigned_gpu || 'GPU'} 正在持续计费运行（约 ${(GPU_EST_CU_PER_HR[status?.assigned_gpu || ''] ?? 4.82).toFixed(2)} CU/h）。用完请务必点击右上角「立即停止并回收显卡」，不要干等 ${status?.idle_auto_stop_min || idleMinutes} 分钟闲置兜底白烧额度！`
                        : `Cloud ${status?.assigned_gpu || 'GPU'} is actively burning ~${(GPU_EST_CU_PER_HR[status?.assigned_gpu || ''] ?? 4.82).toFixed(2)} CU/h. Always click "Stop & Reclaim GPU" right after use instead of waiting ${status?.idle_auto_stop_min || idleMinutes}m for idle auto-stop.`}
                  </span>
                </div>
              </div>
            )}

          {/* Top-Mounted Quick Output Smoke Test (immediately visible after deploy, zero scrolling) */}
          {isRunning && (
            <InsetWell padding="sm" className="space-y-3">
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div className="flex items-center gap-1.5">
                  <Terminal className="w-4 h-4 text-fg-muted shrink-0" />
                  <span className="text-[13px] font-bold text-fg">
                    {zh ? '快速测试输出（验证模型与协议是否正常吐字）' : 'Quick Output Test (Verify Live Streaming)'}
                  </span>
                  {isDemoSiteMode() && (
                    <StatusPill size="xs" tone="amber">
                      {zh ? '演示模拟' : 'Demo'}
                    </StatusPill>
                  )}
                </div>

                <div className="flex flex-wrap items-center gap-1.5">
                  <button
                    type="button"
                    aria-pressed={pingThink}
                    disabled={pinging}
                    onClick={() => setPingThink((v) => !v)}
                    title={
                      zh
                        ? '关闭深度思考可跳过思维链实现 1~2 秒直出；开启后将自动识别并折叠展示思维链'
                        : 'Disable thinking for instant ~1–2s direct reply; enable to stream & auto-collapse Chain-of-Thought'
                    }
                    className={cn(
                      'inline-flex items-center gap-1 h-7 px-2.5 rounded-lg border text-[11.5px] font-medium cursor-pointer transition-colors disabled:opacity-50',
                      pingThink
                        ? 'font-semibold'
                        : 'bg-transparent text-fg-muted border-[var(--border-faint)] hover:text-fg hover:bg-[var(--hover-fill)]',
                    )}
                    style={
                      pingThink
                        ? {
                            backgroundColor: 'var(--amber-soft)',
                            borderColor: 'var(--amber-border)',
                            color: 'var(--amber-text)',
                          }
                        : undefined
                    }
                  >
                    <Brain className="w-3.5 h-3.5 shrink-0" />
                    <span>{zh ? `深度思考: ${pingThink ? '开' : '关'}` : `Thinking: ${pingThink ? 'On' : 'Off'}`}</span>
                  </button>

                  <div className="flex flex-wrap items-center gap-1" role="tablist" aria-label={zh ? '选择测试协议' : 'Test protocol'}>
                    {(
                      [
                        { id: 'openai' as const, label: zh ? 'OpenAI 兼容' : 'OpenAI Chat' },
                        { id: 'responses' as const, label: 'OpenAI Responses' },
                        { id: 'anthropic' as const, label: zh ? 'Anthropic 兼容' : 'Anthropic' },
                      ] as const
                    ).map((p) => (
                      <button
                        key={p.id}
                        type="button"
                        role="tab"
                        aria-selected={pingProtocol === p.id}
                        onClick={() => setPingProtocol(p.id)}
                        className={cn(
                          'h-7 px-2.5 rounded-lg border text-[11.5px] font-medium cursor-pointer transition-colors',
                          pingProtocol === p.id
                            ? 'bg-surface-2 text-fg border-[var(--border-strong)] font-semibold shadow-xs'
                            : 'bg-transparent text-fg-muted border-transparent hover:text-fg hover:bg-[var(--hover-fill)]',
                        )}
                      >
                        {p.label}
                      </button>
                    ))}
                  </div>
                </div>
              </div>

              <div className="flex items-center gap-2">
                <input
                  type="text"
                  value={pingPrompt}
                  onChange={(e) => setPingPrompt(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && !e.shiftKey) {
                      e.preventDefault();
                      if (!pinging) void handleQuickPing();
                    }
                  }}
                  placeholder={zh ? '输入一句测试提示词，立即验证模型吐字…' : 'Type a prompt to test live output…'}
                  className="flex-1 min-w-0 h-9 px-3 rounded-xl border bg-surface-2 text-[12.5px] text-fg focus:outline-none focus:border-[var(--accent)]"
                  style={{ borderColor: 'var(--border-subtle)' }}
                />
                {pinging ? (
                  <ActionButton
                    size="sm"
                    variant="danger"
                    icon={<Square />}
                    onClick={handleAbortPing}
                  >
                    {zh ? '停止生成' : 'Stop'}
                  </ActionButton>
                ) : (
                  <ActionButton
                    size="sm"
                    variant="primary"
                    icon={<Send />}
                    disabled={!pingPrompt.trim()}
                    onClick={() => void handleQuickPing()}
                  >
                    {zh ? '测试吐字' : 'Test Output'}
                  </ActionButton>
                )}
              </div>

              {(pingError || pingReply || pingThinking || pinging) && (
                <div
                  className="rounded-xl border bg-surface-2 px-3.5 py-2.5 max-h-[340px] overflow-y-auto space-y-2.5"
                  style={{ borderColor: 'var(--border-faint)' }}
                >
                  {pingError ? (
                    <div className="text-[12px] leading-relaxed" style={{ color: 'var(--coral-text)' }}>
                      {pingError}
                    </div>
                  ) : (
                    <>
                      <ReasoningOutputBox
                        rawText={pingReply}
                        thinkingText={pingThinking}
                        streaming={pinging}
                        thinkingDurationMs={pingThinkDurationMs}
                        aborted={pingAborted}
                        zh={zh}
                      />
                      {pingMeta && (
                        <div
                          className="pt-2 border-t text-[11px] ch-num text-fg-muted flex flex-wrap items-center gap-x-2.5 gap-y-0.5"
                          style={{ borderColor: 'var(--border-faint)' }}
                        >
                          {pingMeta.aborted ? (
                            <span style={{ color: 'var(--amber-text)' }}>
                              ⏹ {zh ? '已手动打断输出' : 'Stopped manually'}
                            </span>
                          ) : (
                            <span style={{ color: 'var(--emerald-text)' }}>✓ HTTP 200</span>
                          )}
                          <span>
                            ·{' '}
                            {pingMeta.protocol === 'openai'
                              ? 'POST /v1/chat/completions'
                              : pingMeta.protocol === 'responses'
                                ? 'POST /v1/responses'
                                : 'POST /v1/messages'}
                          </span>
                          {pingMeta.ttftMs ? (
                            <span>
                              · {zh ? `首字 ${pingMeta.ttftMs} ms` : `TTFT ${pingMeta.ttftMs} ms`}
                            </span>
                          ) : null}
                          <span>· {zh ? `总耗时 ${pingMeta.ms} ms` : `Total ${pingMeta.ms} ms`}</span>
                          {pingMeta.tps ? <span>· {pingMeta.tps.toFixed(1)} tok/s</span> : null}
                        </div>
                      )}
                    </>
                  )}
                </div>
              )}
            </InsetWell>
          )}
        </div>
      </LayeredCard>
      </div>

      {/* 2. 2026 Community Uncensored Model Selector + Hardware Controls */}
      <LayeredCard padding="md" className="rounded-[20px]">
        <div className="space-y-5">
          <div className="space-y-3">
            <div className="space-y-1">
              <div className="flex items-center gap-2">
                <Flame className="w-4 h-4 shrink-0" style={{ color: 'var(--coral)' }} />
                <h2 className="text-[15px] font-bold text-fg">
                  {zh ? '1. 选择 2026 社区最热无审查模型' : '1. Pick a Top 2026 Uncensored Model'}
                </h2>
              </div>
              <p className="text-[12px] text-fg-muted">
                {zh
                  ? '采用 Heretic / Abliterated 正交去审查权重（保留 99% 原版智商、零拒答）；支持在 data/llm_catalog.json 热更新模型榜。'
                  : 'Orthogonal Heretic / Abliterated GGUFs preserve full reasoning quality without refusals. Hot-updatable via data/llm_catalog.json.'}
              </p>
            </div>

            <div>
              <SegmentedTabs<ModelFilterTab>
                aria-label={zh ? '筛选模型档位' : 'Filter model tier'}
                value={filterTab}
                onChange={setFilterTab}
                options={[
                  {
                    value: 'all',
                    label: zh ? '全部' : 'All',
                    count: catalog.length,
                  },
                  {
                    value: 'free',
                    label: zh ? 'T4 轻量 (16GB)' : 'T4 Light (16GB)',
                    count: catalog.filter((m) => m.free_tier_ready).length,
                  },
                  {
                    value: 'pro',
                    label: zh ? '大显存进阶 (L4/A100/G4)' : 'Pro GPUs (L4/A100/G4)',
                    count: catalog.filter((m) => !m.free_tier_ready).length,
                  },
                  {
                    value: 'custom',
                    label: zh ? '自定义 HF 链接' : 'Custom HF URL',
                  },
                ]}
              />
            </div>
          </div>

          {filterTab === 'custom' ? (
            <InsetWell padding="md" className="space-y-3">
              <div className="text-[12.5px] font-semibold text-fg">
                {zh
                  ? '粘贴任意 Hugging Face GGUF 仓库或 Ollama 标签'
                  : 'Paste any Hugging Face GGUF repository or Ollama tag'}
              </div>
              <input
                type="text"
                value={customModelInput}
                onChange={(e) => setCustomModelInput(e.target.value)}
                placeholder="hf.co/huihui-ai/Huihui-Qwen3.8-27B-abliterated-GGUF:Q4_K"
                className="w-full h-10 px-3 rounded-xl border border-line bg-surface-2 text-[13px] text-fg ch-num focus:outline-none focus:border-[var(--accent)]"
              />
              {customParsed && !customParsed.ok && (
                <p className="text-[11.5px]" style={{ color: 'var(--coral-text)' }}>
                  {zh ? customParsed.errorZh : customParsed.errorEn}
                </p>
              )}
              {customParsed?.ok && selectedSpec && (
                <div className="flex flex-wrap items-center gap-2 text-[11.5px] text-fg-secondary">
                  <StatusPill size="xs" tone="emerald">
                    {zh ? '格式校验通过' : 'Validated'}
                  </StatusPill>
                  <span className="ch-num">{customParsed.modelId}</span>
                  <span>
                    · {zh ? `自动推算推荐 ${selectedSpec.recommended_gpu}（需 ≥${selectedSpec.min_vram_gb}GB 显存）` : `Auto-inferred ${selectedSpec.recommended_gpu} (≥${selectedSpec.min_vram_gb}GB VRAM)`}
                  </span>
                </div>
              )}
              <p className="text-[11.5px] text-fg-muted leading-relaxed">
                {zh
                  ? '直接粘贴 https://huggingface.co/owner/repo 或 hf.co/owner/repo:Q4_K_M 即可拉取任意社区最新 Heretic / Abliterated / 角色扮演 GGUF 模型。'
                  : 'Supports https://huggingface.co/owner/repo or hf.co/owner/repo:Q4_K_M for any newly released community GGUF.'}
              </p>
            </InsetWell>
          ) : (
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              {filteredCatalog.map((m) => {
                const selected = selectedModelId === m.id;
                const isActiveRunning = isRunning && status?.active_model_id === m.id;
                return (
                  <button
                    key={m.id}
                    type="button"
                    onClick={() => {
                      userPickedModelRef.current = true;
                      setSelectedModelId(m.id);
                    }}
                    className={cn(
                      'rounded-2xl border p-4 text-left cursor-pointer transition-all flex flex-col justify-between gap-3',
                      selected
                        ? isActiveRunning
                          ? 'bg-surface-2 border-[var(--emerald)] ring-1 ring-[var(--emerald)]/20 shadow-[var(--shadow-card)]'
                          : 'bg-surface-2 border-[var(--accent)] ring-1 ring-[var(--accent)]/15 shadow-[var(--shadow-card)]'
                        : isActiveRunning
                          ? 'bg-surface-1 border-[var(--emerald-border)] hover:bg-surface-2'
                          : 'bg-surface-1 border-line hover:border-line-strong hover:bg-surface-2',
                    )}
                  >
                    <div className="space-y-2">
                      <div className="flex items-start justify-between gap-2.5">
                        <div className="min-w-0">
                          <div className="text-[13.5px] font-bold text-fg leading-snug">
                            {zh ? m.display_name_zh : m.display_name}
                          </div>
                          <div className="ch-num text-[11px] text-fg-muted mt-0.5 truncate">
                            {m.huggingface_repo || m.ollama_tag}
                          </div>
                        </div>
                        <div className="flex items-center gap-1 shrink-0">
                          {isActiveRunning && (
                            <StatusPill size="xs" tone="emerald" dot pulse>
                              {zh ? '运行中' : 'Live'}
                            </StatusPill>
                          )}
                          <StatusPill size="xs" tone={m.free_tier_ready ? 'emerald' : 'amber'} mono>
                            {m.free_tier_ready
                              ? `T4 · ${m.min_vram_gb}G+`
                              : `${m.recommended_gpu} · ${m.min_vram_gb}G+`}
                          </StatusPill>
                        </div>
                      </div>
                      <p className="text-[12px] leading-relaxed text-fg-secondary">
                        {zh ? m.summary_zh : m.summary}
                      </p>
                    </div>

                    <div className="flex flex-wrap items-center justify-between gap-2 pt-2.5 border-t border-line-faint text-[11px] text-fg-muted ch-num">
                      <span>
                        {m.parameter_size} · {m.quantization} · {formatContextK(m.context_length)} ctx
                      </span>
                      {m.huggingface_repo && (
                        <a
                          href={`https://huggingface.co/${m.huggingface_repo}`}
                          target="_blank"
                          rel="noopener noreferrer"
                          onClick={(e) => e.stopPropagation()}
                          className="inline-flex items-center gap-1 text-fg-secondary hover:text-fg no-underline"
                        >
                          <span>HF</span>
                          <ExternalLink className="w-3 h-3" />
                        </a>
                      )}
                    </div>
                  </button>
                );
              })}
            </div>
          )}

          {/* Hardware & Account Deployment Bar */}
        <InsetWell padding="md" className="space-y-3">
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
            <label className="space-y-1 block">
              <span className="text-[11.5px] font-semibold text-fg-secondary flex items-center gap-1">
                <Cpu className="w-3.5 h-3.5" />
                <span>{zh ? '2. 选择显卡档位' : '2. Target GPU'}</span>
              </span>
              <select
                value={selectedGpu}
                onChange={(e) => setSelectedGpu(e.target.value as '' | AcceleratorType)}
                className="w-full h-9 px-2.5 rounded-xl border bg-surface-2 text-[12.5px] text-fg focus:outline-none focus:border-[var(--accent)]"
                style={{ borderColor: 'var(--border-subtle)' }}
              >
                {GPU_CHOICES.map((g) => (
                  <option key={g.id || 'auto'} value={g.id}>
                    {zh ? `${g.labelZh} · ${g.hintZh}` : `${g.labelEn} · ${g.hintEn}`}
                  </option>
                ))}
              </select>
            </label>

            <label className="space-y-1 block">
              <span className="text-[11.5px] font-semibold text-fg-secondary flex items-center gap-1">
                <ShieldCheck className="w-3.5 h-3.5" />
                <span>{zh ? '3. 选择算力池账号' : '3. Account Routing'}</span>
              </span>
              <select
                value={selectedAccountId}
                onChange={(e) => setSelectedAccountId(e.target.value)}
                className="w-full h-9 px-2.5 rounded-xl border bg-surface-2 text-[12.5px] text-fg focus:outline-none focus:border-[var(--accent)]"
                style={{ borderColor: 'var(--border-subtle)' }}
              >
                <option value="">
                  {zh ? '智能选号（优先免费卡 / 临期额度）' : 'Smart Auto-Route (Free / Expiring First)'}
                </option>
                {colabAccounts.map((acc) => {
                  const full = acc.health === 'MAX_CONCURRENCY';
                  const active = acc.active_assignments || 0;
                  const suffix = full
                    ? zh
                      ? ` · 并发已满 (${active} 卡)`
                      : ` · Concurrency Full (${active} GPUs)`
                    : active > 0
                      ? zh
                        ? ` · 在跑 ${active} 卡`
                        : ` · ${active} active`
                      : '';
                  return (
                    <option key={acc.id} value={acc.id}>
                      {acc.alias || displayEmail(acc.email)} · {Math.round(acc.schedulable_cu)} CU{suffix}
                    </option>
                  );
                })}
              </select>
            </label>

            <label className="space-y-1 block">
              <span className="text-[11.5px] font-semibold text-fg-secondary flex items-center gap-1">
                <Sparkles className="w-3.5 h-3.5" />
                <span>{zh ? '4. 闲置自动停表防烧 CU' : '4. Idle Auto-Shutdown'}</span>
              </span>
              <select
                value={idleMinutes}
                onChange={(e) => setIdleMinutes(Number(e.target.value))}
                className="w-full h-9 px-2.5 rounded-xl border bg-surface-2 text-[12.5px] text-fg focus:outline-none focus:border-[var(--accent)]"
                style={{ borderColor: 'var(--border-subtle)' }}
              >
                {IDLE_MINUTES_OPTIONS.map((m) => (
                  <option key={m} value={m}>
                    {zh ? `${m} 分钟无调用自动释放显卡` : `Auto-stop after ${m} min idle`}
                  </option>
                ))}
              </select>
            </label>
          </div>

          {allAccountsConcurrencyFull && !isRunning && !isBusyState && (
            <div
              className="rounded-xl border px-3.5 py-2.5 flex flex-col sm:flex-row sm:items-center justify-between gap-2.5 text-[12px] leading-relaxed"
              style={{
                backgroundColor: 'var(--amber-soft)',
                borderColor: 'var(--amber-border)',
                color: 'var(--amber-text)',
              }}
            >
              <div className="flex items-start gap-2 min-w-0">
                <AlertTriangle className="w-4 h-4 shrink-0 mt-0.5" />
                <span>
                  {zh
                    ? `当前全部 ${colabAccounts.length} 个 Colab 账号均已达 Google 并发上限（共 ${totalPoolActiveGPUs} 张显卡在跑，触发 HTTP 412 冷却），并非 T4/显卡缺货。在【实验工作台】停止任意 1 台空闲或已完成的显卡后，将立即解锁该账号冷却。`
                    : `All ${colabAccounts.length} Colab accounts are currently at Google's concurrency limit (${totalPoolActiveGPUs} active GPUs, HTTP 412 cooldown) — not a T4/GPU stockout. Stopping any 1 idle or finished GPU in the Workbench immediately lifts cooldown.`}
                </span>
              </div>
              <button
                type="button"
                onClick={() => nav('/exp')}
                className="inline-flex items-center gap-1 h-7.5 px-3 rounded-lg border text-[11.5px] font-semibold shrink-0 cursor-pointer bg-surface-1 hover:bg-surface-2 transition-colors"
                style={{ borderColor: 'var(--amber-border)', color: 'var(--amber-text)' }}
              >
                {zh ? '前往实验工作台释放显卡 →' : 'Free GPU in Workbench →'}
              </button>
            </div>
          )}

          <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-3 pt-1">
            {isBusyState ? (
              <button
                type="button"
                onClick={() => topCardRef.current?.scrollIntoView({ behavior: 'smooth', block: 'start' })}
                className="flex items-center gap-2 text-left text-[12px] font-medium min-w-0 cursor-pointer hover:underline"
                style={{ color: 'var(--amber-text)' }}
              >
                <Loader2 className="w-3.5 h-3.5 shrink-0 animate-spin" />
                <span className="truncate">
                  {deployProgress.liveText}
                </span>
                <span className="ch-num shrink-0 opacity-80">
                  · {formatClockMMSS(deployElapsedSec)}
                </span>
              </button>
            ) : (
              <div className="text-[12px] text-fg-muted">
                {selectedSpec
                  ? zh
                    ? `即将部署：${selectedSpec.display_name_zh}（建议显存 ≥ ${selectedSpec.min_vram_gb}GB，推荐 ${selectedSpec.recommended_gpu}）`
                    : `Deploying: ${selectedSpec.display_name} (≥${selectedSpec.min_vram_gb}GB VRAM, rec. ${selectedSpec.recommended_gpu})`
                  : zh
                    ? '请选择模型或输入有效的 Hugging Face GGUF 链接'
                    : 'Select a model or enter a valid Hugging Face GGUF repo'}
              </div>
            )}
            <div className="flex items-center gap-2 shrink-0">
              {isBusyState && (
                <ActionButton
                  size="md"
                  variant="danger"
                  icon={<Square />}
                  loading={busyAction === 'stop'}
                  disabled={busyAction === 'stop'}
                  onClick={() => void handleStop()}
                >
                  {zh ? '取消部署' : 'Cancel'}
                </ActionButton>
              )}
              <ActionButton
                size="md"
                variant="primary"
                icon={isRunning ? <Zap /> : <Play />}
                loading={isBusyState}
                disabled={busyAction !== null || isBusyState || !effectiveModelId}
                onClick={() => void handleDeploy()}
              >
                {isBusyState
                  ? zh
                    ? `部署中 (${formatClockMMSS(deployElapsedSec)})`
                    : `Deploying (${formatClockMMSS(deployElapsedSec)})`
                  : isRunning
                    ? zh
                      ? '切换并重新部署模型'
                      : 'Switch & Redeploy'
                    : zh
                      ? '一键部署并暴露 /v1 接口'
                      : 'Deploy & Expose /v1 API'}
              </ActionButton>
            </div>
          </div>
        </InsetWell>
        </div>
      </LayeredCard>

      {/* 3. External Client Setup — All 4 Wire Protocols (`OpenAI` / `Responses` / `Anthropic` / `Ollama`) */}
      <LayeredCard padding="md" className="rounded-[20px]">
        <div className="space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h2 className="text-[14.5px] font-bold text-fg">
                {zh ? '一键接入外部客户端（全协议原生兼容）' : 'Connect Your Favorite Client (All 4 Protocols)'}
              </h2>
              <p className="text-[12px] text-fg-muted mt-0.5">
                {zh
                  ? '原生同时暴露 OpenAI Chat、OpenAI Responses、Anthropic Messages 与 Ollama 协议；所有推理仅在你自己的云端显卡内存中完成，零第三方中转。'
                  : 'Exposes OpenAI Chat, OpenAI Responses, Anthropic Messages, and Ollama natively — all inference stays inside your private cloud GPU memory.'}
              </p>
            </div>
            <StatusPill size="xs" tone="emerald">
              OpenAI · Responses · Anthropic · Ollama
            </StatusPill>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-2.5">
            {[
              {
                key: 'base',
                label: zh ? 'OpenAI / Responses 地址' : 'OpenAI / Responses URL',
                value: openaiBaseUrl,
              },
              {
                key: 'anthropic',
                label: zh ? 'Anthropic 地址（不加 /v1）' : 'Anthropic URL (no /v1)',
                value: anthropicBaseUrl,
              },
              { key: 'model', label: 'Model ID', value: activeClientModelId },
              {
                key: 'key',
                label: 'API Key',
                value: authRequired ? '<Admin Token>' : 'colabhive-local',
              },
            ].map((item) => (
              <button
                key={item.key}
                type="button"
                onClick={() => void copyText(item.key, item.value)}
                className="rounded-xl border px-3 py-2 text-left bg-surface-1 hover:border-line-strong cursor-pointer transition-colors min-w-0"
                style={{ borderColor: 'var(--border-subtle)' }}
              >
                <div className="flex items-center justify-between gap-1 text-[10.5px] font-semibold text-fg-muted">
                  <span className="truncate">{item.label}</span>
                  {copiedKey === item.key ? (
                    <Check className="w-3 h-3 shrink-0" style={{ color: 'var(--emerald-text)' }} />
                  ) : (
                    <Copy className="w-3 h-3 shrink-0" />
                  )}
                </div>
                <div className="ch-num text-[12px] font-semibold text-fg truncate mt-0.5">
                  {item.value}
                </div>
              </button>
            ))}
          </div>

          <div className="flex flex-wrap items-center gap-1.5 pt-0.5">
            {(
              [
                { id: 'cherrystudio', label: 'Cherry Studio' },
                { id: 'sillytavern', label: zh ? 'SillyTavern（酒馆）' : 'SillyTavern' },
                { id: 'openwebui', label: 'Open WebUI / Chatbox' },
                { id: 'coding', label: zh ? 'Claude Code / Cursor / curl' : 'Claude Code / Cursor / curl' },
              ] as const
            ).map((t) => (
              <button
                key={t.id}
                type="button"
                onClick={() => setClientTab(t.id)}
                className={cn(
                  'h-7 px-2.5 rounded-lg border text-[11.5px] font-medium cursor-pointer transition-colors',
                  clientTab === t.id
                    ? 'bg-surface-2 text-fg border-[var(--border-strong)] font-semibold'
                    : 'bg-transparent text-fg-muted border-transparent hover:text-fg hover:bg-[var(--hover-fill)]',
                )}
              >
                {t.label}
              </button>
            ))}
          </div>

          <InsetWell padding="sm" className="text-[12px] leading-relaxed text-fg-secondary space-y-1.5">
            {clientTab === 'cherrystudio' && (
              <>
                <div className="font-semibold text-fg">
                  {zh ? 'Cherry Studio / 沉浸式翻译接入（三种协议全支持）：' : 'Cherry Studio / Immersive Translate (All 3 protocol tabs supported):'}
                </div>
                <ol className="list-decimal pl-4 space-y-1">
                  <li>
                    {zh
                      ? '设置 → 模型服务 → 添加自定义供应商，类型任选「OpenAI 兼容」「OpenAI Responses」或「Anthropic 兼容」均可原生跑通。'
                      : 'Settings → Providers → Add Custom Provider: OpenAI Compatible, OpenAI Responses, or Anthropic Compatible all work natively.'}
                  </li>
                  <li>
                    {zh ? '选 OpenAI 兼容 / OpenAI Responses 时地址填 ' : 'For OpenAI / Responses set Base URL to '}
                    <code className="ch-num text-fg font-semibold">{openaiBaseUrl}</code>
                    {zh ? '；选 Anthropic 兼容时地址填 ' : '; for Anthropic set Base URL to '}
                    <code className="ch-num text-fg font-semibold">{anthropicBaseUrl}</code>
                    {zh ? '（不加 /v1），密钥填 colabhive-local。' : ' (without /v1), Key: colabhive-local.'}
                  </li>
                  <li>
                    {zh ? '点击「管理」拉取模型或直接填入 ' : 'Fetch models or add '}
                    <code className="ch-num text-fg font-semibold">{activeClientModelId}</code>
                    {zh ? '，对话完毕记得回到本页顶部点「立即停止并回收显卡」。' : ', and remember to click "Stop & Reclaim GPU" at the top when finished.'}
                  </li>
                </ol>
              </>
            )}
            {clientTab === 'sillytavern' && (
              <>
                <div className="font-semibold text-fg">
                  {zh ? 'SillyTavern（酒馆）无审查角色扮演直连步骤：' : 'SillyTavern direct connection:'}
                </div>
                <ol className="list-decimal pl-4 space-y-1">
                  <li>
                    {zh
                      ? '顶部插头图标 → API 选择 Chat Completion → Chat Completion Source 选 Custom (OpenAI-compatible)。'
                      : 'Open API Connections → Chat Completion → Source: Custom (OpenAI-compatible).'}
                  </li>
                  <li>
                    {zh ? 'Custom Endpoint (Base URL) 填入：' : 'Set Custom Endpoint (Base URL) to: '}
                    <code className="ch-num text-fg font-semibold">{openaiBaseUrl}</code>
                  </li>
                  <li>
                    {zh ? '模型 ID 填入 ' : 'Set Model ID to '}
                    <code className="ch-num text-fg font-semibold">{activeClientModelId}</code>
                    {zh ? '（API Key 填任意字符如 colabhive-local），点 Connect 即可无限制开聊！' : ' (any API key like colabhive-local) and click Connect.'}
                  </li>
                </ol>
              </>
            )}
            {clientTab === 'openwebui' && (
              <>
                <div className="font-semibold text-fg">
                  {zh ? 'Open WebUI / Chatbox / LobeChat 接入：' : 'Open WebUI / Chatbox / LobeChat:'}
                </div>
                <p>
                  {zh ? '将 OpenAI API Base URL 设为 ' : 'Set OpenAI API Base URL to '}
                  <code className="ch-num text-fg font-semibold">{openaiBaseUrl}</code>
                  {zh ? '，或直接使用 Ollama 原生地址 ' : ' or native Ollama URL '}
                  <code className="ch-num text-fg font-semibold">{resolvedOrigin}</code>
                  {zh ? '（自动暴露 /api/tags 与 /api/chat）。' : ' (/api/tags & /api/chat supported natively).'}
                </p>
              </>
            )}
            {clientTab === 'coding' && (
              <div className="space-y-2">
                <div className="flex items-center justify-between gap-2">
                  <span className="font-semibold text-fg">
                    {zh ? '终端 curl / Claude Code / Cursor 测试命令：' : 'Terminal curl / Claude Code / Cursor:'}
                  </span>
                  <button
                    type="button"
                    onClick={() => void copyText('curl', curlSnippet)}
                    className="inline-flex items-center gap-1 text-[11px] font-semibold text-fg-secondary hover:text-fg cursor-pointer"
                  >
                    {copiedKey === 'curl' ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
                    <span>{copiedKey === 'curl' ? (zh ? '已复制' : 'Copied') : zh ? '复制' : 'Copy'}</span>
                  </button>
                </div>
                <pre className="p-2.5 rounded-xl bg-surface-2 border text-[11px] ch-num overflow-x-auto text-fg" style={{ borderColor: 'var(--border-faint)' }}>
                  {curlSnippet}
                </pre>
                <div className="text-[11.5px] text-fg-muted">
                  {zh ? 'Claude Code 环境变量直连：' : 'Claude Code env vars: '}
                  <code className="ch-num text-fg">
                    ANTHROPIC_BASE_URL={anthropicBaseUrl} ANTHROPIC_API_KEY={authRequired ? '<ADMIN_TOKEN>' : 'colabhive-local'}
                  </code>
                </div>
              </div>
            )}
          </InsetWell>
        </div>
      </LayeredCard>
    </div>
  );
}

export default LLMDeployPage;
