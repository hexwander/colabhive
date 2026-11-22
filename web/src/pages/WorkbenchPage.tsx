import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertOctagon,
  Cpu,
  ExternalLink,
  FolderOpen,
  Image as ImageIcon,
  NotebookPen,
  Pin,
  Plus,
  RefreshCw,
  RotateCcw,
  Square,
  Terminal as TerminalIcon,
  X,
} from 'lucide-react';
import {
  DEFAULT_HARDWARE_CATALOG,
  type Account,
  type Experiment,
  type ExperimentShard,
  type HardwareSpec,
  type KernelRichOutput,
  type RuntimeSession,
  type SessionStreamChunk,
  type WSEvent,
} from '../types';
import { api, formatBytes, useColabHiveEvents } from '../lib/api';
import { useI18n } from '../i18n';
import {
  ActionButton,
  AnimatedNumber,
  CircularQuotaRing,
  EmptyState,
  InsetWell,
  LayeredCard,
  PageHeader,
  SegmentedQuotaBar,
  SegmentedTabs,
  Skeleton,
  StatusPill,
  cn,
  resolveTone,
  type QuotaRingTone,
} from '../components/ui';
import { LiveTerminalConsole } from '../components/terminal/LiveTerminalConsole';
import {
  CodeWaterfallPanel,
  collectRichOutputs,
  type ExecutedCellEntry,
} from '../components/terminal/CodeWaterfallPanel';
import { RemoteFileExplorer } from '../components/terminal/RemoteFileExplorer';
import {
  LaunchShardedModal,
  ProvisionSessionModal,
} from '../components/terminal/WorkbenchModals';
import {
  DEMO_ACCOUNTS,
  DEMO_EXEC_HISTORY,
  DEMO_EXPERIMENTS,
  DEMO_SESSIONS,
  DEMO_STREAM_CHUNKS,
  formatElapsedSec,
  normalizeUtilPct,
} from '../components/terminal/workbenchMockData';

export interface WorkbenchPageProps {
  experimentId?: string;
  sessionId?: string;
  onOpenConnect?: () => void;
  onNavigate?: (path: string) => void;
  onSessionsSynced?: () => void;
}

type CenterViewTab = 'terminal' | 'cells' | 'plots' | 'files';

export function WorkbenchPage({
  experimentId: propExperimentId,
  sessionId: propSessionId,
  onSessionsSynced,
}: WorkbenchPageProps = {}) {
  const { locale, t, displayEmail, displaySecret } = useI18n();
  const isZh = locale === 'zh-CN';

  // Data state
  const [sessions, setSessions] = useState<RuntimeSession[]>([]);
  const [experiments, setExperiments] = useState<Experiment[]>([]);
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [hardwareCatalog, setHardwareCatalog] =
    useState<HardwareSpec[]>(DEFAULT_HARDWARE_CATALOG);
  const [streamChunks, setStreamChunks] = useState<SessionStreamChunk[]>([]);
  const [execBySession, setExecBySession] = useState<Record<string, ExecutedCellEntry[]>>({});
  const seededLogsBySessionRef = useRef<Record<string, string>>({});
  const seededShardsRef = useRef<Set<string>>(new Set());

  const ingestSeededLogs = useCallback(
    (nextSessions: RuntimeSession[], nextExps: Experiment[] = []) => {
      const additions: SessionStreamChunk[] = [];
      const replaceSessionIds = new Set<string>();

      for (const exp of nextExps) {
        for (const sh of exp.shards ?? []) {
          const shardKey = `${exp.id}#${sh.shard_id}`;
          if (sh.log_tail && !seededShardsRef.current.has(shardKey)) {
            seededShardsRef.current.add(shardKey);
            additions.push({
              session_id: sh.session_id,
              experiment_id: exp.id,
              shard_index: sh.shard_id,
              stream: 'stdout',
              text: sh.log_tail,
              timestamp: sh.started_at || exp.updated_at,
            });
          }
        }
      }

      for (const s of nextSessions) {
        if (s.status === 'STOPPED' || s.status === 'LOST') continue;
        const lines = Array.isArray(s.recent_logs)
          ? s.recent_logs.filter((line) => typeof line === 'string' && line.length > 0)
          : [];

        if (lines.length > 0) {
          const joined = lines.join('\n');
          const prevJoined = seededLogsBySessionRef.current[s.id];
          if (prevJoined === undefined || prevJoined === '__status__') {
            if (prevJoined === '__status__') {
              replaceSessionIds.add(s.id);
            }
            seededLogsBySessionRef.current[s.id] = joined;
            if (s.adopted_external) {
              additions.push({
                session_id: s.id,
                experiment_id: s.experiment_id,
                shard_index: s.shard_index,
                stream: 'colab',
                text: `\x1b[32m[cloud-sync]\x1b[0m ${
                  isZh
                    ? `已自动同步云端在跑实例 ${s.notebook_name || s.name} (${s.accelerator}) · 实时日志尾部：`
                    : `Synced live Cloud VM ${s.notebook_name || s.name} (${s.accelerator}) · Live log tail:`
                }`,
                timestamp: s.updated_at || s.created_at,
              });
            }
            additions.push({
              session_id: s.id,
              experiment_id: s.experiment_id,
              shard_index: s.shard_index,
              stream: 'stdout',
              text: joined,
              timestamp: s.updated_at || s.created_at,
            });
          } else if (joined !== prevJoined) {
            seededLogsBySessionRef.current[s.id] = joined;
            if (joined.startsWith(prevJoined)) {
              const delta = joined.slice(prevJoined.length).replace(/^\n+/, '');
              if (delta) {
                additions.push({
                  session_id: s.id,
                  experiment_id: s.experiment_id,
                  shard_index: s.shard_index,
                  stream: 'stdout',
                  text: delta,
                  timestamp: s.updated_at || new Date().toISOString(),
                });
              }
            } else {
              replaceSessionIds.add(s.id);
              if (s.adopted_external) {
                additions.push({
                  session_id: s.id,
                  experiment_id: s.experiment_id,
                  shard_index: s.shard_index,
                  stream: 'colab',
                  text: `\x1b[32m[cloud-sync]\x1b[0m ${
                    isZh
                      ? `已同步云端在跑实例 ${s.notebook_name || s.name} (${s.accelerator}) 最新日志尾部：`
                      : `Updated live log tail for Cloud VM ${s.notebook_name || s.name} (${s.accelerator}):`
                  }`,
                  timestamp: s.updated_at || new Date().toISOString(),
                });
              }
              additions.push({
                session_id: s.id,
                experiment_id: s.experiment_id,
                shard_index: s.shard_index,
                stream: 'stdout',
                text: joined,
                timestamp: s.updated_at || new Date().toISOString(),
              });
            }
          }
        } else if (!seededLogsBySessionRef.current[s.id] && s.status_message) {
          seededLogsBySessionRef.current[s.id] = '__status__';
          additions.push({
            session_id: s.id,
            experiment_id: s.experiment_id,
            shard_index: s.shard_index,
            stream: 'colab',
            text: `[session ${s.notebook_name || s.name}] ${s.status_message} (${s.accelerator} · ${s.backend})`,
            timestamp: s.updated_at,
          });
        }
      }

      if (additions.length === 0 && replaceSessionIds.size === 0) return;

      setStreamChunks((prev) => {
        const base =
          replaceSessionIds.size > 0
            ? prev.filter((c) => !replaceSessionIds.has(c.session_id))
            : prev;
        const next = [...base, ...additions];
        return next.length > 1500 ? next.slice(next.length - 1500) : next;
      });
    },
    [isZh],
  );

  // Loading & preview mode
  const [initialLoading, setInitialLoading] = useState(true);
  const [isOfflineDemo, setIsOfflineDemo] = useState(false);
  const [actionBusy, setActionBusy] = useState<string | null>(null);
  const [toastBanner, setToastBanner] = useState<{
    tone: 'emerald' | 'amber' | 'danger';
    text: string;
  } | null>(null);

  // Selection state: focus either an Experiment (+ optional shard) or a Standalone Session
  const [selectedExperimentId, setSelectedExperimentId] = useState<string | null>(
    propExperimentId || null,
  );
  const [selectedShardFilter, setSelectedShardFilter] = useState<number | 'all'>('all');
  const [selectedSessionId, setSelectedSessionId] = useState<string | null>(
    propSessionId || null,
  );

  // Workspace tab (one full-width panel at a time)
  const [activeTab, setActiveTab] = useState<CenterViewTab>('terminal');

  // Modals
  const [showProvisionModal, setShowProvisionModal] = useState(false);
  const [showShardedModal, setShowShardedModal] = useState(false);

  // Live 1-second clock tick for elapsed timers & keepalive pulse seconds
  const [nowMs, setNowMs] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  // Extract `/exp/:id` from pathname or `colabhive:navigate` events if not passed as prop
  useEffect(() => {
    if (propExperimentId) {
      if (propExperimentId.startsWith('sess_')) {
        setSelectedExperimentId(null);
        setSelectedSessionId(propExperimentId);
      } else {
        setSelectedExperimentId(propExperimentId);
      }
      return;
    }
    if (typeof window === 'undefined') return;

    const syncFromPath = () => {
      const m = window.location.pathname.match(/^\/exp\/([^/]+)/);
      if (m?.[1]) {
        const target = decodeURIComponent(m[1]);
        if (target.startsWith('sess_')) {
          setSelectedExperimentId(null);
          setSelectedSessionId(target);
        } else {
          setSelectedExperimentId(target);
        }
      }
    };

    syncFromPath();
    window.addEventListener('popstate', syncFromPath);
    window.addEventListener('colabhive:navigate', syncFromPath);
    return () => {
      window.removeEventListener('popstate', syncFromPath);
      window.removeEventListener('colabhive:navigate', syncFromPath);
    };
  }, [propExperimentId]);

  const activateDemoPreview = useCallback(() => {
    setIsOfflineDemo(true);
    setExperiments(DEMO_EXPERIMENTS);
    setSessions(DEMO_SESSIONS);
    setAccounts(DEMO_ACCOUNTS);
    setStreamChunks(DEMO_STREAM_CHUNKS);
    setExecBySession({
      sess_g4_shard0: DEMO_EXEC_HISTORY.map((res, i) => ({
        id: res.executionId,
        code:
          i === 0
            ? '!nvidia-smi --query-gpu=name,memory.used,memory.total,utilization.gpu,temperature.gpu --format=csv'
            : 'import torch\nprint("CUDA Device:", torch.cuda.get_device_name(0))\nprint({"shard": 0, "synced_records": 93, "mmlu_pro_acc": 0.8172})',
        submittedAt: new Date(Date.now() - (2 - i) * 60_000).toISOString(),
        result: res,
      })),
    });
    setSelectedExperimentId((prev) => prev || DEMO_EXPERIMENTS[0].id);
    setSelectedSessionId((prev) => prev || DEMO_SESSIONS[0].id);
  }, []);

  const fetchWorkbenchState = useCallback(
    async (withSync = false) => {
      try {
        const [sessList, expList, accList, hwList] = await Promise.all([
          api.listSessions(withSync ? { sync: true } : undefined),
          api.listExperiments(),
          api.listAccounts().catch(() => [] as Account[]),
          api.getHardwareCatalog().catch(() => DEFAULT_HARDWARE_CATALOG),
        ]);

        const safeSessions = Array.isArray(sessList) ? sessList : [];
        const activeInitialSessions = safeSessions.filter(
          (s) => s.status !== 'STOPPED' && s.status !== 'LOST',
        );
        const safeExps = Array.isArray(expList) ? expList : [];
        setSessions(safeSessions);
        setExperiments(safeExps);
        setAccounts(Array.isArray(accList) ? accList : []);
        if (Array.isArray(hwList) && hwList.length > 0) {
          setHardwareCatalog(hwList);
        }
        setIsOfflineDemo(false);

        // Default initial selection
        if (safeExps.length > 0) {
          setSelectedExperimentId((prev) =>
            prev && safeExps.some((e) => e.id === prev) ? prev : safeExps[0].id,
          );
          const firstShardSess = safeExps[0].shards?.[0]?.session_id;
          if (firstShardSess) {
            setSelectedSessionId((prev) => prev || firstShardSess);
          }
        } else if (activeInitialSessions.length > 0) {
          setSelectedSessionId((prev) =>
            prev && activeInitialSessions.some((s) => s.id === prev)
              ? prev
              : activeInitialSessions[0].id,
          );
        } else {
          setSelectedExperimentId(null);
          setSelectedSessionId(null);
        }

        // Seed terminal console buffer from shard log_tails & session.recent_logs
        ingestSeededLogs(activeInitialSessions, safeExps);
      } catch {
        // Backend offline during standalone UI preview -> hydrate realistic observatory state
        activateDemoPreview();
      } finally {
        setInitialLoading(false);
      }
    },
    [activateDemoPreview, ingestSeededLogs],
  );

  useEffect(() => {
    void fetchWorkbenchState(true);
  }, [fetchWorkbenchState]);

  // Resolve `/exp/:id` when `:id` belongs to a RuntimeSession regardless of ID prefix
  useEffect(() => {
    if (!selectedExperimentId) return;
    const isExp = experiments.some((e) => e.id === selectedExperimentId);
    if (!isExp && sessions.some((s) => s.id === selectedExperimentId)) {
      setSelectedSessionId(selectedExperimentId);
      setSelectedExperimentId(null);
    }
  }, [experiments, sessions, selectedExperimentId]);

  // Real-time WebSocket event subscription
  const handleWSEvent = useCallback(
    (evt: WSEvent) => {
      if (!evt || !evt.type) return;

      if (evt.type === 'session_output' && evt.payload) {
        const chunk = evt.payload as SessionStreamChunk;
        setStreamChunks((prev) => {
          const next = [...prev, chunk];
          return next.length > 1500 ? next.slice(next.length - 1500) : next;
        });
        return;
      }

      if (evt.type === 'session_updated' && evt.payload) {
        const updated = evt.payload as RuntimeSession;
        setSessions((prev) => {
          const idx = prev.findIndex((s) => s.id === updated.id);
          if (idx === -1) return [updated, ...prev];
          const copy = [...prev];
          copy[idx] = updated;
          return copy;
        });
        ingestSeededLogs([updated]);
        return;
      }

      if (evt.type === 'experiment_updated' && evt.payload) {
        const updated = evt.payload as Experiment;
        setExperiments((prev) => {
          const idx = prev.findIndex((e) => e.id === updated.id);
          if (idx === -1) return [updated, ...prev];
          const copy = [...prev];
          copy[idx] = updated;
          return copy;
        });
        return;
      }

      if (evt.type === 'zombie_alert' && evt.payload) {
        const alertData = evt.payload as { session_id?: string; message?: string };
        const target = alertData.session_id || (isZh ? '云端实例' : 'the worker');
        setToastBanner({
          tone: 'danger',
          text:
            alertData.message ||
            (isZh
              ? `检测到 ${target} 上的 Python 进程已卡死无响应，请重启任务或释放显卡，避免空转扣费`
              : `The Python process on ${target} is hung. Restart the task or release the GPU to stop idle billing.`),
        });
      }
    },
    [ingestSeededLogs, isZh],
  );

  const wsState = useColabHiveEvents(handleWSEvent, !isOfflineDemo);

  // Derived active Experiment & active Session
  const activeExperiment = useMemo(
    () => experiments.find((e) => e.id === selectedExperimentId) || null,
    [experiments, selectedExperimentId],
  );

  // Active (non-stopped, non-lost) sessions & standalone kernel sessions
  const liveSessions = useMemo(
    () => sessions.filter((s) => s.status !== 'STOPPED' && s.status !== 'LOST'),
    [sessions],
  );

  const standaloneSessions = useMemo(
    () => liveSessions.filter((s) => !s.experiment_id),
    [liveSessions],
  );

  const displayedKernelSessions = useMemo(
    () => (experiments.length === 0 ? liveSessions : standaloneSessions),
    [experiments.length, liveSessions, standaloneSessions],
  );

  const activeSession = useMemo<RuntimeSession | null>(() => {
    if (activeExperiment) {
      if (selectedShardFilter !== 'all') {
        const shard = activeExperiment.shards.find((sh) => sh.shard_id === selectedShardFilter);
        if (shard) {
          const found = sessions.find((s) => s.id === shard.session_id);
          if (found) return found;
        }
      }
      // Fallback to first running shard session of the selected experiment
      const firstShard =
        activeExperiment.shards.find((sh) => sh.status === 'RUNNING') ||
        activeExperiment.shards[0];
      if (firstShard) {
        const found = sessions.find((s) => s.id === firstShard.session_id);
        if (found) return found;
      }
    }
    if (selectedSessionId) {
      const match =
        liveSessions.find((s) => s.id === selectedSessionId) ||
        sessions.find((s) => s.id === selectedSessionId);
      if (match) return match;
    }
    return liveSessions[0] || sessions[0] || null;
  }, [activeExperiment, selectedShardFilter, selectedSessionId, liveSessions, sessions]);

  // Filtered stream chunks for current focus
  const visibleStreamChunks = useMemo(() => {
    if (activeExperiment) {
      const shardSessionIds = new Set(activeExperiment.shards.map((s) => s.session_id));
      return streamChunks.filter(
        (c) => c.experiment_id === activeExperiment.id || shardSessionIds.has(c.session_id),
      );
    }
    if (activeSession) {
      return streamChunks.filter((c) => c.session_id === activeSession.id);
    }
    return streamChunks;
  }, [streamChunks, activeExperiment, activeSession]);

  const currentExecEntries = useMemo(() => {
    const sid = activeSession?.id || 'default';
    return execBySession[sid] || [];
  }, [execBySession, activeSession?.id]);

  const pushLocalStreamChunk = useCallback(
    (stream: 'stdout' | 'stderr' | 'colab', text: string, rich?: KernelRichOutput) => {
      const chunk: SessionStreamChunk = {
        session_id: activeSession?.id || 'local',
        experiment_id: activeExperiment?.id,
        shard_index:
          selectedShardFilter !== 'all'
            ? selectedShardFilter
            : activeSession?.shard_index,
        stream,
        text,
        rich_output: rich,
        timestamp: new Date().toISOString(),
      };
      setStreamChunks((prev) => [...prev, chunk]);
    },
    [activeSession, activeExperiment, selectedShardFilter],
  );

  // Operational Safety Actions
  const handleOpenInColabWeb = async () => {
    if (!activeSession) return;
    setActionBusy('open-web');
    try {
      let targetUrl = activeSession.colab_web_url;
      if (!isOfflineDemo) {
        const res = await api.getSessionWebUrl(activeSession.id).catch(() => null);
        if (res?.url) targetUrl = res.url;
      }
      window.open(targetUrl || 'https://colab.research.google.com', '_blank', 'noopener,noreferrer');
      setToastBanner({
        tone: 'emerald',
        text: isZh
          ? `已在浏览器打开 Colab 网页（请切换到 ${displayEmail(activeSession.account_email)} 账号，即可查看并操作同一个云端实例）`
          : `Opened Colab in your browser — switch to ${displayEmail(activeSession.account_email)} to see this exact runtime`,
      });
    } finally {
      setActionBusy(null);
    }
  };

  const handleHotRestartWorker = async () => {
    if (!activeSession) return;
    setActionBusy('hot-restart');
    try {
      if (!isOfflineDemo) {
        await api.hotRestartWorker(activeSession.id);
      } else {
        await new Promise((r) => window.setTimeout(r, 380));
      }
      const hfSize = formatBytes(activeSession.resources?.hf_cache_bytes || 58_196_803_584);
      pushLocalStreamChunk(
        'colab',
        `\x1b[33m[hot-restart]\x1b[0m Killed stale worker PID ${activeSession.worker?.pid || '—'} -> Respawned clean worker while keeping /root/.cache/huggingface (${hfSize}) HOT in RAM/NVMe!`,
      );
      setToastBanner({
        tone: 'amber',
        text: isZh
          ? `已重启 Python 脚本，${hfSize} 模型权重完整保留，无需重新下载`
          : `Restarted the Python script and kept ${hfSize} of downloaded model weights — nothing to re-download`,
      });
    } catch (err) {
      setToastBanner({
        tone: 'danger',
        text: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setActionBusy(null);
    }
  };

  const handleStopAndRelease = async () => {
    if (!(activeExperiment && selectedShardFilter === 'all') && activeSession?.adopted_external) {
      const confirmed = window.confirm(
        isZh
          ? '该实例是你在外部启动并同步进来的任务，确定要远程停止并释放显卡吗？'
          : 'This runtime was started externally and synced into ColabHive. Are you sure you want to remotely stop it and release the GPU?',
      );
      if (!confirmed) return;
    }

    setActionBusy('stop');
    try {
      if (activeExperiment && selectedShardFilter === 'all') {
        if (!isOfflineDemo) {
          await api.teardownExperiment(activeExperiment.id, true);
          await fetchWorkbenchState();
          onSessionsSynced?.();
        } else {
          setExperiments((prev) =>
            prev.map((e) =>
              e.id === activeExperiment.id
                ? {
                    ...e,
                    status: 'CANCELLED',
                    running_shards: 0,
                    aggregate_burn_rate_cu_hr: 0,
                  }
                : e,
            ),
          );
        }
        pushLocalStreamChunk(
          'colab',
          `\x1b[31m[teardown]\x1b[0m Synced JSONL outputs & unassigned all ${activeExperiment.num_shards} shard VMs -> 0.00 CU/hr`,
        );
        setToastBanner({
          tone: 'emerald',
          text: isZh
            ? `已结束实验「${activeExperiment.title}」并释放全部显卡，算力扣费已归零 (0.00 CU/h)`
            : `Stopped ${activeExperiment.title} and released every GPU · billing is now 0.00 CU/h`,
        });
      } else if (activeSession) {
        if (!isOfflineDemo) {
          await api.stopSession(activeSession.id);
          await fetchWorkbenchState();
          onSessionsSynced?.();
        } else {
          setSessions((prev) =>
            prev.map((s) =>
              s.id === activeSession.id
                ? { ...s, status: 'STOPPED', burn_rate_cu_hr: 0 }
                : s,
            ),
          );
        }
        pushLocalStreamChunk(
          'colab',
          `\x1b[31m[unassign]\x1b[0m Released runtime ${activeSession.endpoint} -> Burn rate now 0.00 CU/hr`,
        );
        setToastBanner({
          tone: 'emerald',
          text: isZh
            ? `已结束 ${activeSession.name} 并释放显卡，算力扣费已归零`
            : `Stopped ${activeSession.name} and released the GPU · billing is now 0.00 CU/h`,
        });
      }
    } catch (err) {
      setToastBanner({
        tone: 'danger',
        text: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setActionBusy(null);
    }
  };

  // Telemetry metrics from `/api/colab/resources`
  const primaryGpu = activeSession?.resources?.gpus?.[0];
  const vramUsedBytes = primaryGpu?.memory_used_bytes ?? 0;
  const vramTotalBytes =
    primaryGpu?.memory_total_bytes ||
    (hardwareCatalog.find((h) => h.accelerator === activeSession?.accelerator)?.vram_gb || 0) *
      1024 ** 3;
  const vramPct =
    vramTotalBytes > 0 ? Math.min(100, Math.round((vramUsedBytes / vramTotalBytes) * 100)) : 0;
  const smPct = normalizeUtilPct(primaryGpu?.gpu_utilization);
  const gpuTempC = primaryGpu?.temperature_c ?? (smPct > 50 ? 64 : 42);
  const gpuPowerW = primaryGpu?.power_watts;
  const vramUsedGb = vramUsedBytes / 1024 ** 3;
  const vramTotalGb = vramTotalBytes / 1024 ** 3;

  const ramUsedBytes = activeSession?.resources?.memory_used_bytes ?? 0;
  const ramTotalBytes = activeSession?.resources?.memory_total_bytes ?? 0;
  const ramPct =
    ramTotalBytes > 0 ? Math.min(100, Math.round((ramUsedBytes / ramTotalBytes) * 100)) : 0;
  const ramUsedGb = ramUsedBytes / 1024 ** 3;
  const ramTotalGb = ramTotalBytes / 1024 ** 3;

  const diskUsedBytes = activeSession?.resources?.disk_used_bytes ?? 0;
  const hfCacheBytes = activeSession?.resources?.hf_cache_bytes ?? 0;

  const lastKeepaliveSecAgo = useMemo(() => {
    if (!activeSession?.last_keepalive_at) return 3;
    const diff = Math.round((nowMs - Date.parse(activeSession.last_keepalive_at)) / 1000);
    if (Number.isNaN(diff) || diff < 0) return 2;
    return isOfflineDemo ? diff % 8 : diff;
  }, [activeSession?.last_keepalive_at, nowMs, isOfflineDemo]);

  const zombieDetected =
    Boolean(activeSession?.worker?.zombie_detected) ||
    activeSession?.status === 'ZOMBIE_ALERT' ||
    activeSession?.worker?.proc_state === 'Z';

  const liveElapsedSec = useMemo(() => {
    if (!activeSession) return 0;
    if (activeSession.status === 'STOPPED') return activeSession.uptime_sec;
    const createdMs = Date.parse(activeSession.created_at);
    if (!Number.isNaN(createdMs)) {
      return Math.max(activeSession.uptime_sec, Math.floor((nowMs - createdMs) / 1000));
    }
    return activeSession.uptime_sec;
  }, [activeSession, nowMs]);

  const emptyWorkspace =
    !initialLoading && experiments.length === 0 && liveSessions.length === 0;
  const showSkeleton = initialLoading && experiments.length === 0 && sessions.length === 0;

  // Cloud assignment sync feedback for the header control
  const [syncingCloud, setSyncingCloud] = useState(false);

  const handleSyncCloudSessions = async () => {
    setSyncingCloud(true);
    try {
      if (!isOfflineDemo) {
        const res = await api.syncRemoteSessions();
        const syncedSessions = Array.isArray(res?.sessions) ? res.sessions : [];
        if (syncedSessions.length > 0) {
          ingestSeededLogs(syncedSessions);
        }
        await fetchWorkbenchState(false);
        onSessionsSynced?.();

        const liveSynced = syncedSessions.filter(
          (s) => s.status !== 'STOPPED' && s.status !== 'LOST',
        );
        if (liveSynced.length > 0 && !selectedExperimentId && !selectedSessionId) {
          setSelectedSessionId(liveSynced[0].id);
        }

        const adoptedCount = res?.adopted_count ?? 0;
        const activeCount = res?.active_count ?? liveSynced.length;
        if (adoptedCount > 0) {
          setToastBanner({
            tone: 'emerald',
            text: isZh
              ? `已同步云端实例：新纳管 ${adoptedCount} 台外部在跑实例（当前共 ${activeCount} 台在线）`
              : `Synced Cloud VMs: adopted ${adoptedCount} external running runtime(s) (${activeCount} active total)`,
          });
        } else if (activeCount > 0) {
          setToastBanner({
            tone: 'emerald',
            text: isZh
              ? `云端实例同步完成：当前共 ${activeCount} 台云端实例在线，显存与日志已刷新`
              : `Cloud VMs synced: ${activeCount} active runtime(s) up to date`,
          });
        } else {
          setToastBanner({
            tone: 'amber',
            text: isZh
              ? '已完成云端扫描：各已接入账号下当前暂无正在运行的 Colab 实例'
              : 'Cloud scan complete: no active Colab runtimes found across pooled accounts',
          });
        }
      } else {
        await new Promise((r) => window.setTimeout(r, 320));
        setToastBanner({
          tone: 'amber',
          text: isZh
            ? '当前处于演示模式，连接本地 ColabHive 服务后即可同步真实 Google Colab 云端实例'
            : 'Running in preview mode — connect the local service to sync live Google Colab VMs',
        });
      }
    } catch (err) {
      setToastBanner({
        tone: 'danger',
        text: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setSyncingCloud(false);
    }
  };

  // Workspace header derivations
  const viewingAllShards = Boolean(activeExperiment) && selectedShardFilter === 'all';
  const workspaceTitle =
    activeExperiment && selectedShardFilter === 'all'
      ? activeExperiment.title
      : activeSession?.name || 'Colab Runtime';
  const acceleratorLabel = activeSession?.accelerator || activeExperiment?.accelerator || 'G4';
  const hardwareLabel =
    activeExperiment && selectedShardFilter === 'all'
      ? `${activeExperiment.num_shards}× ${activeExperiment.accelerator}`
      : `${acceleratorLabel}${
          activeSession?.shape === 'HIGH_RAM' ? (isZh ? ' · 大内存' : ' · High-RAM') : ''
        }`;
  // Numeric so the telemetry numerals can glide between samples
  const burnRateNum = viewingAllShards
    ? (activeExperiment?.aggregate_burn_rate_cu_hr ?? 0)
    : (activeSession?.burn_rate_cu_hr ?? 0);
  const spentCuNum = viewingAllShards
    ? (activeExperiment?.cu_burned_so_far ?? 0)
    : (activeSession?.accumulated_cu ?? 0);
  // Shimmer and live meters only while the GPU is genuinely allocated
  const isSessionLive =
    activeSession != null &&
    activeSession.status !== 'STOPPED' &&
    activeSession.status !== 'LOST' &&
    activeSession.status !== 'ERROR';
  const gpuShortName =
    primaryGpu?.name
      ?.replace(/^NVIDIA\s+/i, '')
      .replace(/\s+Server Edition$/i, '')
      .trim() ||
    activeSession?.accelerator ||
    'GPU';
  const workerPid = activeSession?.worker?.pid;
  const runningShardCount = activeExperiment
    ? activeExperiment.shards.filter((sh) => sh.status === 'RUNNING').length
    : 0;

  // CU meter: share of the estimated total for a parallel run, otherwise the
  // live burn rate against the fastest GPU tier in the catalog.
  const estimatedTotalCu = viewingAllShards ? (activeExperiment?.estimated_total_cu ?? 0) : 0;
  const maxTierBurnRate = Math.max(
    1,
    ...hardwareCatalog.map((h) => h.default_burn_rate_cu_hr || 0),
  );
  const burnRateCeiling =
    maxTierBurnRate * (viewingAllShards ? Math.max(1, activeExperiment?.num_shards ?? 1) : 1);
  const burnMeterPct = Math.max(
    0,
    Math.min(
      100,
      Math.round(
        estimatedTotalCu > 0
          ? (spentCuNum / estimatedTotalCu) * 100
          : (burnRateNum / burnRateCeiling) * 100,
      ),
    ),
  );
  const burnMeterLabel =
    estimatedTotalCu > 0
      ? isZh
        ? '已用算力占预计总消耗的比例'
        : 'Share of the estimated total CU'
      : isZh
        ? '当前每小时消耗速度'
        : 'Current hourly burn rate';

  const hotRestartTitle = isZh
    ? '只重启 Python 脚本，保留云端已下载的几十 GB 模型权重！'
    : 'Restarts only the Python script while keeping tens of GBs of downloaded model weights!';
  const stopReleaseTitle = isZh
    ? '立即停止任务并归还云端显卡，算力扣费马上归零'
    : 'Stop immediately and release the GPU so CU billing drops to zero';

  const richOutputCount = useMemo(
    () =>
      collectRichOutputs(activeExperiment?.artifacts, activeSession?.cells, currentExecEntries)
        .length,
    [activeExperiment?.artifacts, activeSession?.cells, currentExecEntries],
  );

  // Shared panel wiring
  const handleSelectShardFilter = (sh: number | 'all') => {
    setSelectedShardFilter(sh);
    if (sh !== 'all' && activeExperiment) {
      const target = activeExperiment.shards.find((x) => x.shard_id === sh);
      if (target) setSelectedSessionId(target.session_id);
    }
  };

  const appendExecEntry = (entry: ExecutedCellEntry) => {
    const sid = activeSession?.id || 'default';
    setExecBySession((prev) => ({
      ...prev,
      [sid]: [...(prev[sid] || []), entry],
    }));
  };

  const updateExecEntry = (id: string, patch: Partial<ExecutedCellEntry>) => {
    const sid = activeSession?.id || 'default';
    setExecBySession((prev) => ({
      ...prev,
      [sid]: (prev[sid] || []).map((e) => (e.id === id ? { ...e, ...patch } : e)),
    }));
  };

  const metaItems: React.ReactNode[] = [
    <span key="acct" className="text-fg">
      {displayEmail(activeSession?.account_email)}
    </span>,
    <span key="elapsed">
      {isZh ? '已运行' : 'Elapsed'} {formatElapsedSec(liveElapsedSec)}
    </span>,
    <span key="burn">
      {isZh ? '每小时消耗 ' : 'Burn '}
      <AnimatedNumber value={burnRateNum} decimals={2} /> CU{isZh ? '（本次已用 ' : '/h ('}
      <AnimatedNumber value={spentCuNum} decimals={2} /> CU{isZh ? '）' : ' total)'}
    </span>,
  ];
  if (activeSession?.sticky_key) {
    metaItems.push(
      <span
        key="sticky"
        className="inline-flex items-center gap-1"
        title={isZh ? '固定环境标识：同一 AI 助手会一直复用这个实例' : 'Pinned environment key'}
      >
        <Pin className="w-3 h-3 shrink-0" />
        {displaySecret(activeSession.sticky_key)}
      </span>,
    );
  }
  if (activeSession?.endpoint) {
    metaItems.push(
      <span
        key="endpoint"
        className="text-fg-muted"
        title={isZh ? '云端实例地址' : 'Runtime endpoint'}
      >
        {displaySecret(activeSession.endpoint)}
      </span>,
    );
  }

  return (
    <div className="space-y-6 ch-reveal">
      <PageHeader
        title={t('exp.title')}
        subtitle={
          isZh
            ? '实时查看云端显卡负载与温度、追踪代码输出日志，随时保留模型权重一键重启任务'
            : 'Monitor live GPU VRAM and temperature, stream console logs, and hot-restart scripts while keeping cached models.'
        }
        badge={
          <StatusPill
            size="sm"
            dot
            pulse={wsState.connected && !isOfflineDemo}
            mono
            tone={isOfflineDemo ? 'amber' : wsState.connected ? 'emerald' : 'neutral'}
          >
            {isOfflineDemo
              ? isZh
                ? '演示快照'
                : 'Preview Snapshot'
              : wsState.connected
                ? `${isZh ? '实时' : 'Live'} · ${wsState.latencyMs ?? 12}ms`
                : isZh
                  ? '已同步'
                  : 'Synced'}
          </StatusPill>
        }
        actions={
          <>
            <ActionButton
              variant="secondary"
              size="md"
              icon={<RefreshCw className={cn(syncingCloud && 'animate-spin')} />}
              loading={syncingCloud}
              disabled={syncingCloud}
              title={
                isZh
                  ? '扫描所有已接入账号下正在运行的 Colab 云端实例，自动纳管显存、温度、文件与实时日志'
                  : 'Scan pooled accounts for live Colab runtimes and sync VRAM, temperature, files & logs'
              }
              onClick={() => void handleSyncCloudSessions()}
            >
              {isZh ? '同步云端实例' : 'Sync Cloud VMs'}
            </ActionButton>
            <ActionButton
              variant="secondary"
              size="md"
              icon={<Cpu />}
              title={
                isZh
                  ? '从算力池自动挑选账号，分配一台云端显卡'
                  : 'Allocate one cloud GPU from your account pool'
              }
              onClick={() => setShowProvisionModal(true)}
            >
              {isZh ? '启动新 GPU 实例' : 'New GPU Session'}
            </ActionButton>
            <ActionButton
              variant="primary"
              size="md"
              icon={<Plus />}
              title={
                isZh
                  ? '把任务自动拆到多张云端显卡同时运行，等待时间成倍缩短'
                  : 'Split one job across several cloud GPUs to finish much sooner'
              }
              onClick={() => setShowShardedModal(true)}
            >
              {isZh ? '多卡并行跑实验' : 'Run Parallel Shards'}
            </ActionButton>
          </>
        }
      />

      {/* Dismissible feedback banner */}
      {toastBanner && (
        <div
          data-tone={resolveTone(toastBanner.tone)}
          role="status"
          className="flex items-center justify-between gap-3 rounded-xl border px-4 py-2.5 text-[12.5px]"
          style={{
            backgroundColor: 'var(--tone-soft)',
            borderColor: 'var(--tone-border)',
            color: 'var(--tone-fg)',
          }}
        >
          <span className="min-w-0 break-words">{toastBanner.text}</span>
          <button
            type="button"
            onClick={() => setToastBanner(null)}
            aria-label={isZh ? '关闭提示' : 'Dismiss'}
            title={isZh ? '关闭提示' : 'Dismiss'}
            className="shrink-0 inline-flex items-center justify-center w-6 h-6 rounded-md cursor-pointer opacity-70 transition-opacity hover:opacity-100"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      )}

      {showSkeleton ? (
        <div className="grid grid-cols-1 lg:grid-cols-[280px_minmax(0,1fr)] gap-6 items-start">
          <LayeredCard padding="none" className="p-3" staggerIndex={0}>
            <div className="space-y-2.5">
              <Skeleton className="h-4 w-28" />
              <Skeleton className="h-[72px] w-full rounded-xl" />
              <Skeleton className="h-[72px] w-full rounded-xl" />
              <Skeleton className="h-12 w-full rounded-xl" />
            </div>
          </LayeredCard>
          <LayeredCard padding="none" className="p-5 sm:p-6" staggerIndex={1}>
            <div className="space-y-5">
              <Skeleton className="h-6 w-2/5" />
              <Skeleton className="h-4 w-3/5" />
              <Skeleton className="h-20 w-full rounded-xl" />
              <Skeleton className="h-[420px] w-full rounded-xl" />
            </div>
          </LayeredCard>
        </div>
      ) : emptyWorkspace ? (
        <LayeredCard padding="lg" staggerIndex={0}>
          <EmptyState
            icon={<Cpu />}
            title={isZh ? '当前暂无正在运行的实验' : 'No running experiments right now'}
            description={
              isZh
                ? '启动一台云端 GPU 实例开始交互式跑代码，或把任务自动拆到多张显卡同时运行；空闲未分配时零算力消耗。'
                : 'Start a cloud GPU session to run code interactively, or split a job across several GPUs at once — zero CU burn while idle.'
            }
            action={
              <div className="flex flex-wrap items-center justify-center gap-2">
                <ActionButton
                  variant="primary"
                  size="md"
                  icon={<Plus />}
                  onClick={() => setShowProvisionModal(true)}
                >
                  {isZh ? '+ 启动新 GPU 实例' : '+ New GPU Session'}
                </ActionButton>
                <ActionButton
                  variant="secondary"
                  size="md"
                  icon={<RefreshCw className={cn(syncingCloud && 'animate-spin')} />}
                  loading={syncingCloud}
                  onClick={() => void handleSyncCloudSessions()}
                >
                  {isZh ? '同步云端实例' : 'Sync Cloud VMs'}
                </ActionButton>
                <ActionButton
                  variant="secondary"
                  size="md"
                  icon={<Cpu />}
                  onClick={() => setShowShardedModal(true)}
                >
                  {isZh ? '多卡并行跑实验' : 'Run Parallel Shards'}
                </ActionButton>
              </div>
            }
          />
        </LayeredCard>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-[280px_minmax(0,1fr)] gap-6 items-start">
          {/* ================================================================
           * LEFT RAIL: sharded runs + active sessions
           * ================================================================ */}
          <aside aria-label={isZh ? '实验与 GPU 实例' : 'Runs and sessions'} className="min-w-0">
            <LayeredCard padding="none" className="p-3" staggerIndex={0}>
              <div className="space-y-4">
                {/* Group 1 — Parallel runs */}
                <section>
                  <RailHeading
                    label={isZh ? '并发实验' : 'Parallel Runs'}
                    count={experiments.length}
                  />

                  {experiments.length === 0 ? (
                    <RailNote>{isZh ? '暂无并发实验' : 'No parallel runs yet'}</RailNote>
                  ) : (
                    <div className="space-y-2 ui-stagger">
                      {experiments.map((exp) => {
                        const isSelected = selectedExperimentId === exp.id;
                        const progressPct =
                          exp.target_records_total && exp.target_records_total > 0
                            ? Math.min(
                                100,
                                Math.round(
                                  (exp.synced_records_total / exp.target_records_total) * 100,
                                ),
                              )
                            : exp.shards.length > 0
                              ? Math.round(
                                  exp.shards.reduce((acc, s) => acc + s.progress_pct, 0) /
                                    exp.shards.length,
                                )
                              : 0;
                        const expBarTone: 'emerald' | 'coral' | 'amber' =
                          exp.status === 'FAILED'
                            ? 'coral'
                            : exp.status === 'CANCELLED'
                              ? 'amber'
                              : 'emerald';

                        return (
                          <div
                            key={exp.id}
                            className={cn(
                              'rounded-2xl p-3 transition-all',
                              isSelected
                                ? 'bg-surface-2 border-[1.5px] border-[var(--accent)] shadow-[var(--shadow-card)]'
                                : 'bg-surface-1/75 border border-line-faint hover:bg-surface-2 hover:border-line-strong',
                            )}
                          >
                            <button
                              type="button"
                              aria-current={isSelected ? 'true' : undefined}
                              onClick={() => {
                                setSelectedExperimentId(exp.id);
                                setSelectedShardFilter('all');
                                if (exp.shards[0]?.session_id) {
                                  setSelectedSessionId(exp.shards[0].session_id);
                                }
                              }}
                              className="block w-full text-left cursor-pointer space-y-2.5"
                            >
                              <div className="flex items-start justify-between gap-2">
                                <span
                                  className="text-[12.5px] font-semibold text-fg line-clamp-1"
                                  title={exp.title}
                                >
                                  {exp.title}
                                </span>
                                <StatusPill
                                  size="xs"
                                  tone={
                                    exp.status === 'RUNNING'
                                      ? 'emerald'
                                      : exp.status === 'FAILED'
                                        ? 'coral'
                                        : 'neutral'
                                  }
                                  dot={exp.status === 'RUNNING'}
                                  mono
                                  className="shrink-0"
                                >
                                  {exp.num_shards}× {exp.accelerator}
                                </StatusPill>
                              </div>

                              <SegmentedQuotaBar
                                value={progressPct}
                                segments={16}
                                tone={expBarTone}
                              />

                              <div className="ch-num flex items-center justify-between gap-2 text-[11px] text-fg-muted">
                                <span>
                                  <span className="font-semibold text-fg">
                                    <AnimatedNumber value={progressPct} suffix="%" />
                                  </span>{' '}
                                  · {exp.synced_records_total}
                                  {exp.target_records_total ? `/${exp.target_records_total}` : ''}
                                  {isZh ? ' 条' : ''}
                                </span>
                                <span className="shrink-0">
                                  <AnimatedNumber
                                    value={exp.cu_burned_so_far}
                                    decimals={2}
                                    suffix=" CU"
                                  />
                                </span>
                              </div>
                            </button>

                            {/* Shard filter pills */}
                            <div className="mt-2.5 flex flex-wrap items-center gap-1">
                              <ShardPill
                                active={isSelected && selectedShardFilter === 'all'}
                                title={isZh ? '查看全部并行卡' : 'Show all shards'}
                                onClick={() => {
                                  setSelectedExperimentId(exp.id);
                                  setSelectedShardFilter('all');
                                }}
                              >
                                {isZh ? '全部' : 'All'}
                              </ShardPill>

                              {exp.shards.map((sh) => (
                                <ShardPill
                                  key={sh.shard_id}
                                  active={isSelected && selectedShardFilter === sh.shard_id}
                                  dotColor={shardStatusColor(sh)}
                                  title={`${isZh ? '并行卡' : 'Shard'} #${sh.shard_id} · ${displayEmail(sh.account_email)} · ${shardStatusLabel(sh, isZh)}`}
                                  onClick={() => {
                                    setSelectedExperimentId(exp.id);
                                    setSelectedShardFilter(sh.shard_id);
                                    setSelectedSessionId(sh.session_id);
                                  }}
                                >
                                  #{sh.shard_id}
                                </ShardPill>
                              ))}
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  )}
                </section>

                <div className="border-t border-line-faint" />

                {/* Group 2 — Standalone GPU sessions */}
                <section>
                  <RailHeading
                    label={isZh ? '独立 GPU 实例' : 'GPU Sessions'}
                    count={displayedKernelSessions.length}
                  />

                  {displayedKernelSessions.length === 0 ? (
                    <RailNote>
                      {isZh ? '暂无独立运行的 GPU 实例' : 'No standalone GPU sessions'}
                    </RailNote>
                  ) : (
                    <div className="space-y-2 ui-stagger">
                      {displayedKernelSessions.map((sess) => {
                        const isFocused = !selectedExperimentId && selectedSessionId === sess.id;
                        const isZombie =
                          sess.status === 'ZOMBIE_ALERT' || Boolean(sess.worker?.zombie_detected);

                        return (
                          <button
                            key={sess.id}
                            type="button"
                            aria-current={isFocused ? 'true' : undefined}
                            onClick={() => {
                              setSelectedExperimentId(null);
                              setSelectedShardFilter('all');
                              setSelectedSessionId(sess.id);
                            }}
                            className={cn(
                              'block w-full text-left rounded-2xl p-3 transition-all cursor-pointer',
                              isFocused
                                ? isZombie
                                  ? 'bg-[var(--coral-soft)] border-[1.5px] border-[var(--coral)] shadow-[var(--shadow-card)]'
                                  : 'bg-surface-2 border-[1.5px] border-[var(--accent)] shadow-[var(--shadow-card)]'
                                : isZombie
                                  ? 'bg-[var(--coral-soft)]/60 border border-[var(--coral-border)] hover:bg-[var(--coral-soft)]'
                                  : 'bg-surface-1/75 border border-line-faint hover:bg-surface-2 hover:border-line-strong',
                            )}
                          >
                            <div className="flex items-center justify-between gap-2">
                              <div className="flex items-center gap-2 min-w-0">
                                <span
                                  data-tone={isZombie ? 'coral' : 'emerald'}
                                  className="ui-dot"
                                />
                                <span className="text-[12.5px] font-semibold text-fg truncate">
                                  {sess.name}
                                </span>
                              </div>
                              <StatusPill
                                size="xs"
                                tone={isZombie ? 'coral' : isFocused ? 'accent' : 'neutral'}
                                mono
                                className="shrink-0"
                              >
                                {sess.accelerator}
                              </StatusPill>
                            </div>

                            {(sess.adopted_external || sess.notebook_name) && (
                              <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                                {sess.adopted_external && (
                                  <StatusPill tone="sky" size="xs" dot>
                                    {isZh ? '云端在跑实例（已自动同步）' : 'Synced Cloud VM'}
                                  </StatusPill>
                                )}
                                {sess.notebook_name && (
                                  <span
                                    className="inline-flex items-center gap-1 max-w-full truncate text-[11px] font-medium text-[var(--emerald-text)]"
                                    title={sess.notebook_name}
                                  >
                                    <NotebookPen className="w-3 h-3 shrink-0" />
                                    <span className="truncate">{sess.notebook_name}</span>
                                  </span>
                                )}
                              </div>
                            )}

                            <div className="ch-num mt-1.5 flex items-center justify-between gap-2 text-[11px] text-fg-muted">
                              <span className="truncate">{displayEmail(sess.account_email)}</span>
                              <span className="inline-flex items-center gap-1.5 shrink-0">
                                <AnimatedNumber
                                  value={sess.burn_rate_cu_hr}
                                  decimals={2}
                                  suffix=" CU/h"
                                />
                                {sess.sticky_key && (
                                  <Pin
                                    className="w-3 h-3"
                                    aria-label={isZh ? '固定环境' : 'Pinned environment'}
                                  />
                                )}
                              </span>
                            </div>
                          </button>
                        );
                      })}
                    </div>
                  )}
                </section>
              </div>
            </LayeredCard>
          </aside>

          {/* ================================================================
           * RIGHT WORKSPACE
           * ================================================================ */}
          <LayeredCard padding="none" className="p-5 sm:p-6 min-w-0" staggerIndex={1}>
            <div className="space-y-5">
              {/* Hung-process alert — only when the watchdog actually trips */}
              {zombieDetected && (
                <div
                  data-tone="coral"
                  role="alert"
                  className="flex flex-wrap items-center justify-between gap-3 rounded-xl border px-4 py-3"
                  style={{
                    backgroundColor: 'var(--tone-soft)',
                    borderColor: 'var(--tone-border)',
                  }}
                >
                  <div className="flex items-start gap-2.5 min-w-0">
                    <AlertOctagon
                      className="w-4 h-4 shrink-0 mt-0.5"
                      style={{ color: 'var(--tone-fg)' }}
                    />
                    <div className="min-w-0">
                      <div
                        className="text-[13px] font-semibold"
                        style={{ color: 'var(--tone-fg)' }}
                      >
                        {isZh ? '检测到 Python 进程已卡死' : 'The Python process is hung'}
                        <span className="ch-num ml-2 font-normal">PID {workerPid ?? '—'}</span>
                      </div>
                      <div className="mt-0.5 text-[12px] leading-relaxed text-fg-secondary">
                        {isZh
                          ? '后台任务已经没有响应，但显卡仍在计费。可以「保留权重重启任务」——只重启脚本、不用重新下载模型；或者「结束并释放显卡」，让扣费立刻归零。'
                          : 'The task stopped responding but the GPU is still billing. Hot-restart it (only the script restarts; downloaded models stay) or stop and release the GPU so billing drops to zero.'}
                      </div>
                    </div>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <ActionButton
                      variant="secondary"
                      size="sm"
                      icon={<RotateCcw />}
                      loading={actionBusy === 'hot-restart'}
                      disabled={!activeSession || actionBusy !== null}
                      onClick={() => void handleHotRestartWorker()}
                      title={hotRestartTitle}
                    >
                      {isZh ? '保留权重重启任务' : 'Hot-Restart (Keep Models)'}
                    </ActionButton>
                    <ActionButton
                      variant="danger"
                      size="sm"
                      icon={<Square />}
                      loading={actionBusy === 'stop'}
                      disabled={actionBusy !== null}
                      onClick={() => void handleStopAndRelease()}
                      title={stopReleaseTitle}
                    >
                      {isZh ? '结束并释放显卡' : 'Stop & Stop Billing'}
                    </ActionButton>
                  </div>
                </div>
              )}

              {/* Workspace header */}
              <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
                <div className="space-y-1.5 min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <h2
                      className="ui-title text-[17px] sm:text-[19px] font-semibold text-fg truncate"
                      title={workspaceTitle}
                    >
                      {workspaceTitle}
                    </h2>

                    <StatusPill tone="accent" mono>
                      {hardwareLabel}
                    </StatusPill>

                    {!viewingAllShards && activeSession?.adopted_external && (
                      <StatusPill tone="sky" dot>
                        {isZh ? '云端在跑实例（已自动同步）' : 'Synced Cloud VM'}
                      </StatusPill>
                    )}

                    {!viewingAllShards && activeSession?.notebook_name && (
                      <StatusPill
                        tone="emerald"
                        icon={<NotebookPen />}
                        title={isZh ? '关联的 Colab 笔记本' : 'Colab Notebook'}
                      >
                        {activeSession.notebook_name}
                      </StatusPill>
                    )}

                    {/* One calm health pill; full liveness detail on hover / focus */}
                    <span className="group relative inline-flex">
                      <button
                        type="button"
                        className="inline-flex rounded-full cursor-default"
                        aria-label={isZh ? '查看防掉线与防卡死详情' : 'Show keep-alive details'}
                      >
                        <StatusPill
                          tone={zombieDetected ? 'coral' : isSessionLive ? 'emerald' : 'neutral'}
                          dot
                          pulse={isSessionLive && !zombieDetected}
                        >
                          {zombieDetected
                            ? isZh
                              ? '进程卡死 · 待处理'
                              : 'Process Hung · Action Needed'
                            : isZh
                              ? '防掉线保活运行中'
                              : 'Auto Keep-Alive Active'}
                        </StatusPill>
                      </button>

                      <span className="hidden group-hover:block group-focus-within:block absolute left-0 top-full pt-2 z-30">
                        <span className="ui-float ui-float-pop ui-anim-pop block rounded-xl p-3 w-72 text-[11.5px] space-y-1.5">
                          <LivenessRow label={isZh ? '防掉线自动保活' : 'Auto keep-alive'}>
                            {isZh
                              ? `每 8 秒自动心跳（上次 ${lastKeepaliveSecAgo} 秒前）· 防止闲置断联被回收`
                              : `Heartbeat every 8s (last ${lastKeepaliveSecAgo}s ago) · prevents idle disconnects`}
                          </LivenessRow>
                          <LivenessRow label={isZh ? '防卡死自动巡检' : 'Hung-process watchdog'}>
                            {zombieDetected ? (
                              <span style={{ color: 'var(--coral-text)' }}>
                                {isZh
                                  ? `Python 进程已卡死无响应 (PID ${workerPid ?? '—'}) · 请重启任务或释放显卡`
                                  : `Python process is hung (PID ${workerPid ?? '—'}) · restart it or release the GPU`}
                              </span>
                            ) : isZh ? (
                              `Python 进程运行正常 (PID ${workerPid ?? '—'})`
                            ) : (
                              `Python process healthy (PID ${workerPid ?? '—'})`
                            )}
                          </LivenessRow>
                          <LivenessRow label={isZh ? '断网无感重连' : 'Seamless reconnect'}>
                            {isZh
                              ? `累计自动重连 ${activeSession?.zmq_reconnect_count ?? 0} 次 · 运行状态零丢失`
                              : `${activeSession?.zmq_reconnect_count ?? 0} automatic reconnects · zero state lost`}
                          </LivenessRow>
                          <LivenessRow label={isZh ? '显卡负载同步' : 'GPU load sync'}>
                            {isZh
                              ? '每 10 秒实时刷新显存、算力与温度'
                              : 'VRAM, utilization and temperature refresh every 10s'}
                          </LivenessRow>
                        </span>
                      </span>
                    </span>

                    {activeSession?.fallback_applied && (
                      <StatusPill
                        tone="amber"
                        mono
                        title={
                          isZh
                            ? `${activeSession.requested_accelerator} 缺货，已自动换用 ${activeSession.accelerator}`
                            : `${activeSession.requested_accelerator} was out of stock; switched to ${activeSession.accelerator}`
                        }
                      >
                        {activeSession.requested_accelerator} → {activeSession.accelerator}
                      </StatusPill>
                    )}
                  </div>

                  <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[12px] text-fg-secondary ch-num">
                    {metaItems.map((item, idx) => (
                      <React.Fragment key={idx}>
                        {idx > 0 && (
                          <span aria-hidden="true" className="text-fg-muted">
                            ·
                          </span>
                        )}
                        {item}
                      </React.Fragment>
                    ))}
                  </div>
                </div>

                <div className="flex flex-wrap items-center gap-2 shrink-0">
                  <ActionButton
                    variant="secondary"
                    size="sm"
                    icon={<ExternalLink />}
                    loading={actionBusy === 'open-web'}
                    disabled={!activeSession || actionBusy !== null}
                    onClick={() => void handleOpenInColabWeb()}
                    title={
                      isZh
                        ? '在浏览器打开 Google Colab 网页端，直接查看并操作同一个云端实例'
                        : 'Open this exact runtime in Google Colab web UI'
                    }
                  >
                    {isZh ? '在 Colab 网页查看' : 'Open in Colab'}
                  </ActionButton>
                  <ActionButton
                    variant="secondary"
                    size="sm"
                    icon={<RotateCcw />}
                    loading={actionBusy === 'hot-restart'}
                    disabled={!activeSession || actionBusy !== null}
                    onClick={() => void handleHotRestartWorker()}
                    title={hotRestartTitle}
                  >
                    {isZh ? '保留权重重启任务' : 'Hot-Restart (Keep Models)'}
                  </ActionButton>
                  <ActionButton
                    variant="danger"
                    size="sm"
                    icon={<Square />}
                    loading={actionBusy === 'stop'}
                    disabled={actionBusy !== null}
                    onClick={() => void handleStopAndRelease()}
                    title={stopReleaseTitle}
                  >
                    {viewingAllShards
                      ? isZh
                        ? '结束并释放全部显卡'
                        : 'Stop All & Stop Billing'
                      : isZh
                        ? '结束并释放显卡'
                        : 'Stop & Stop Billing'}
                  </ActionButton>
                </div>
              </div>

              {/* Unified Stardust hardware telemetry strip */}
              <InsetWell padding="xs">
                <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5 gap-2.5">
                  <TelemetryCell
                    label={isZh ? '显存占用' : 'GPU VRAM'}
                    value={`${vramUsedGb.toFixed(1)}/${vramTotalGb > 0 ? Math.round(vramTotalGb) : 0}G`}
                    subline={
                      activeSession?.shape === 'HIGH_RAM'
                        ? isZh
                          ? '大内存 · 专用显存'
                          : 'High-RAM · Dedicated'
                        : isZh
                          ? '专用显存驻留'
                          : 'Allocated VRAM'
                    }
                    pct={Math.round(vramPct)}
                    tone={vramPct > 92 ? 'coral' : vramPct > 78 ? 'amber' : 'sky'}
                    showRing
                    segments={16}
                  />
                  <TelemetryCell
                    label={isZh ? 'GPU 负载' : 'GPU Load'}
                    value={`${Math.round(smPct)}%`}
                    subline={
                      <span title={primaryGpu?.name || gpuShortName}>
                        {gpuPowerW
                          ? `${Math.round(gpuPowerW)}W · ${acceleratorLabel}`
                          : gpuShortName}
                      </span>
                    }
                    pct={Math.round(smPct)}
                    tone="emerald"
                    showRing
                    segments={16}
                  />
                  <TelemetryCell
                    label={isZh ? '核心温度' : 'Core Temp'}
                    value={`${Math.round(gpuTempC)}°C`}
                    subline={
                      gpuTempC < 72
                        ? isZh
                          ? '散热良好'
                          : 'Cool & nominal'
                        : isZh
                          ? '温度偏高'
                          : 'Running warm'
                    }
                    pct={Math.min(100, Math.round(gpuTempC))}
                    tone={gpuTempC >= 80 ? 'coral' : gpuTempC >= 70 ? 'amber' : 'emerald'}
                    segments={16}
                  />
                  <TelemetryCell
                    label={isZh ? '本轮消耗' : 'CU Burned'}
                    value={`${spentCuNum.toFixed(2)} CU`}
                    subline={`${burnRateNum.toFixed(2)} CU/h`}
                    pct={burnMeterPct}
                    meterLabel={burnMeterLabel}
                    tone="amber"
                    segments={16}
                  />
                  <TelemetryCell
                    label={isZh ? '系统内存' : 'Host RAM'}
                    value={`${ramUsedGb.toFixed(1)} / ${ramTotalGb > 0 ? ramTotalGb.toFixed(1).replace(/\.0$/, '') : '0'}G`}
                    subline={
                      hfCacheBytes > 0
                        ? isZh
                          ? `缓存 ${formatCompactBytes(hfCacheBytes)}`
                          : `HF ${formatCompactBytes(hfCacheBytes)}`
                        : isZh
                          ? `磁盘 ${formatCompactBytes(diskUsedBytes)}`
                          : `Disk ${formatCompactBytes(diskUsedBytes)}`
                    }
                    pct={Math.round(ramPct)}
                    tone="sky"
                    segments={16}
                  />
                </div>
              </InsetWell>

              {/* Live shard progress — tactile Stardust hardware cards per parallel GPU */}
              {activeExperiment && activeExperiment.shards.length > 0 && (
                <InsetWell
                  padding="sm"
                  label={isZh ? '并行分片实时进度' : 'Live Shard Progress'}
                  headerRight={
                    <StatusPill
                      size="xs"
                      tone={runningShardCount > 0 ? 'emerald' : 'neutral'}
                      dot={runningShardCount > 0}
                      mono
                    >
                      {runningShardCount}/{activeExperiment.shards.length}{' '}
                      {isZh ? '张卡运行中' : 'running'}
                    </StatusPill>
                  }
                >
                  <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-4 gap-2.5 ui-stagger">
                    {activeExperiment.shards.map((sh) => {
                      const shardPct = Math.max(0, Math.min(100, Math.round(sh.progress_pct)));
                      const shardFocused = selectedShardFilter === sh.shard_id;
                      const shardColor = shardStatusColor(sh);
                      const shardTone = shardQuotaTone(sh);
                      return (
                        <button
                          key={sh.shard_id}
                          type="button"
                          aria-pressed={shardFocused}
                          onClick={() =>
                            handleSelectShardFilter(shardFocused ? 'all' : sh.shard_id)
                          }
                          title={
                            isZh
                              ? `并行卡 #${sh.shard_id} · ${displayEmail(sh.account_email)} · 点击${shardFocused ? '返回全部并行卡' : '只看这张卡'}`
                              : `Shard #${sh.shard_id} · ${displayEmail(sh.account_email)} · click to ${shardFocused ? 'show all shards' : 'focus this shard'}`
                          }
                          className={cn(
                            'min-w-0 rounded-[14px] px-3 py-2.5 text-left cursor-pointer transition-all bg-surface-2',
                            shardFocused
                              ? 'border-[1.5px] border-[var(--accent)] shadow-[var(--shadow-card)]'
                              : 'border border-line-faint shadow-[var(--shadow-control)] hover:border-line-strong hover:-translate-y-[1px]',
                          )}
                        >
                          <span className="flex items-start justify-between gap-2">
                            <span className="min-w-0 space-y-0.5">
                              <span className="inline-flex items-center gap-1.5 min-w-0 text-[12.5px]">
                                <span
                                  className="w-[6px] h-[6px] rounded-full shrink-0"
                                  style={{ backgroundColor: shardColor }}
                                />
                                <span className="ch-num whitespace-nowrap font-semibold text-fg">
                                  {isZh ? '并行卡' : 'Shard'} #{sh.shard_id}
                                </span>
                              </span>
                              <span className="ch-num block whitespace-nowrap text-[11px] text-fg-muted">
                                {sh.synced_records}
                                {sh.target_records ? ` / ${sh.target_records}` : ''}{' '}
                                {isZh ? '步' : 'steps'}
                              </span>
                            </span>
                            <CircularQuotaRing
                              value={shardPct}
                              size={36}
                              strokeWidth={3.4}
                              tone={shardTone}
                            />
                          </span>

                          <span className="mt-2.5 block">
                            <SegmentedQuotaBar
                              value={shardPct}
                              segments={16}
                              tone={shardTone}
                            />
                          </span>

                          <span className="ch-num mt-2 flex items-center justify-between gap-2 text-[10.5px] text-fg-muted">
                            <span className="whitespace-nowrap">{shardStatusLabel(sh, isZh)}</span>
                            <span className="shrink-0 whitespace-nowrap">
                              {sh.vram_used_gb != null && sh.vram_used_gb > 0
                                ? `${sh.vram_used_gb.toFixed(0)}G${sh.temp_c != null ? ` · ${sh.temp_c}°C` : ''}`
                                : `${(sh.cu_burned ?? 0).toFixed(2)} CU`}
                            </span>
                          </span>
                        </button>
                      );
                    })}
                  </div>
                </InsetWell>
              )}

              {/* Focused workspace tabs — one full-width panel at a time */}
              <SegmentedTabs<CenterViewTab>
                aria-label={isZh ? '工作区视图' : 'Workspace view'}
                value={activeTab}
                onChange={setActiveTab}
                options={[
                  {
                    value: 'terminal',
                    label: isZh ? '实时日志' : 'Live Logs',
                    icon: <TerminalIcon />,
                  },
                  {
                    value: 'cells',
                    label: isZh ? '交互运行代码' : 'Run Code',
                    icon: <NotebookPen />,
                  },
                  {
                    value: 'plots',
                    label: isZh ? '生成图表' : 'Generated Charts',
                    icon: <ImageIcon />,
                    count: richOutputCount > 0 ? richOutputCount : undefined,
                  },
                  {
                    value: 'files',
                    label: isZh ? '云端文件与装包' : 'Cloud Files & Packages',
                    icon: <FolderOpen />,
                  },
                ]}
              />

              {activeTab === 'terminal' && (
                <LiveTerminalConsole
                  chunks={visibleStreamChunks}
                  shards={activeExperiment?.shards}
                  selectedShardFilter={selectedShardFilter}
                  onSelectShardFilter={handleSelectShardFilter}
                  downloadBaseName={activeExperiment?.id || activeSession?.name || 'colabhive'}
                  isStreaming={activeSession?.status !== 'STOPPED'}
                  onClear={() => {
                    if (!viewingAllShards && activeSession) {
                      delete seededLogsBySessionRef.current[activeSession.id];
                      setStreamChunks((prev) =>
                        prev.filter((c) => c.session_id !== activeSession.id),
                      );
                    } else {
                      seededLogsBySessionRef.current = {};
                      seededShardsRef.current.clear();
                      setStreamChunks([]);
                    }
                  }}
                  minHeightClass="h-[480px]"
                />
              )}

              {activeTab === 'cells' && (
                <CodeWaterfallPanel
                  sessionId={activeSession?.id}
                  sessionName={activeSession?.name}
                  timeline={activeSession?.timeline}
                  cells={activeSession?.cells}
                  execEntries={currentExecEntries}
                  extraArtifacts={activeExperiment?.artifacts}
                  isOfflineDemo={isOfflineDemo}
                  onAppendExecEntry={appendExecEntry}
                  onUpdateExecEntry={updateExecEntry}
                  onPushStreamText={pushLocalStreamChunk}
                />
              )}

              {activeTab === 'plots' && (
                <CodeWaterfallPanel
                  sessionId={activeSession?.id}
                  sessionName={activeSession?.name}
                  timeline={activeSession?.timeline}
                  cells={activeSession?.cells}
                  execEntries={currentExecEntries}
                  extraArtifacts={activeExperiment?.artifacts}
                  isOfflineDemo={isOfflineDemo}
                  galleryOnly
                  onAppendExecEntry={() => {}}
                  onUpdateExecEntry={() => {}}
                />
              )}

              {activeTab === 'files' && (
                <RemoteFileExplorer
                  sessionId={activeSession?.id}
                  isOfflineDemo={isOfflineDemo}
                  onPushStreamText={(s, txt) => pushLocalStreamChunk(s, txt)}
                />
              )}
            </div>
          </LayeredCard>
        </div>
      )}

      {/* Modals */}
      <ProvisionSessionModal
        open={showProvisionModal}
        accounts={accounts}
        hardwareCatalog={hardwareCatalog}
        isOfflineDemo={isOfflineDemo}
        onClose={() => setShowProvisionModal(false)}
        onCreated={(created) => {
          setSessions((prev) => [created, ...prev]);
          setSelectedExperimentId(null);
          setSelectedSessionId(created.id);
          pushLocalStreamChunk(
            'colab',
            `\x1b[32m[provisioned]\x1b[0m Session ${created.name} (${created.accelerator}) attached on ${created.account_email}`,
          );
        }}
      />

      <LaunchShardedModal
        open={showShardedModal}
        hardwareCatalog={hardwareCatalog}
        isOfflineDemo={isOfflineDemo}
        onClose={() => setShowShardedModal(false)}
        onLaunched={(exp) => {
          setExperiments((prev) => [exp, ...prev]);
          setSelectedExperimentId(exp.id);
          setSelectedShardFilter('all');
          pushLocalStreamChunk(
            'colab',
            `\x1b[33m[sharded-launch]\x1b[0m Dispatched ${exp.num_shards}× ${exp.accelerator} shards for ${exp.title}`,
          );
        }}
      />
    </div>
  );
}

// ============================================================================
// Local presentational helpers
// ============================================================================

/** Semantic status hue for a shard: running → emerald, done → sky, failed/zombie → coral. */
function shardStatusColor(sh: ExperimentShard): string {
  const isBad =
    sh.zombie_detected || sh.status.includes('FAIL') || sh.status.includes('ZOMBIE');
  if (isBad) return 'var(--coral)';
  if (sh.status.includes('COMPLETED')) return 'var(--sky)';
  return 'var(--emerald)';
}

function shardQuotaTone(sh: ExperimentShard): 'emerald' | 'coral' | 'amber' | 'sky' {
  const isBad =
    sh.zombie_detected || sh.status.includes('FAIL') || sh.status.includes('ZOMBIE');
  if (isBad) return 'coral';
  if (sh.status.includes('COMPLETED')) return 'sky';
  if (sh.status.includes('CANCEL') || sh.status.includes('STOP')) return 'amber';
  return 'emerald';
}

/** Plain-words shard state (the raw status stays in the data, not in the UI). */
function shardStatusLabel(sh: ExperimentShard, isZh: boolean): string {
  const status = sh.status.toUpperCase();
  if (sh.zombie_detected || status.includes('ZOMBIE')) return isZh ? '进程卡死' : 'Hung';
  if (status.includes('FAIL') || status.includes('ERROR')) return isZh ? '运行失败' : 'Failed';
  if (status.includes('COMPLETED')) return isZh ? '已完成' : 'Done';
  if (status === 'RUNNING') return isZh ? '运行中' : 'Running';
  if (status.includes('CANCEL') || status.includes('STOP')) return isZh ? '已停止' : 'Stopped';
  if (status.includes('SYNC')) return isZh ? '同步结果中' : 'Syncing';
  return isZh ? '准备中' : 'Starting';
}

function RailHeading({ label, count }: { label: string; count: number }) {
  return (
    <div className="flex items-center justify-between px-1.5 pb-2 text-[11.5px] font-medium text-fg-muted">
      <span>{label}</span>
      <span className="ch-num">{count}</span>
    </div>
  );
}

function RailNote({ children }: { children: React.ReactNode }) {
  return <p className="px-1.5 py-2 text-[12px] text-fg-muted">{children}</p>;
}

function ShardPill({
  active,
  dotColor,
  title,
  onClick,
  children,
}: {
  active: boolean;
  dotColor?: string;
  title?: string;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={title}
      aria-pressed={active}
      className={cn(
        'ch-num inline-flex items-center gap-1 h-5 px-2 rounded-full border text-[10.5px] cursor-pointer transition-colors',
        active
          ? 'bg-[var(--accent)] border-transparent text-[var(--accent-fg)] font-semibold shadow-[var(--shadow-control)]'
          : 'bg-surface-2/85 border-line-faint text-fg-secondary hover:text-fg hover:border-line-strong',
      )}
    >
      {dotColor && (
        <span
          className="w-[5px] h-[5px] rounded-full shrink-0"
          style={{ backgroundColor: dotColor }}
        />
      )}
      {children}
    </button>
  );
}

function LivenessRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <span className="flex flex-col gap-0.5">
      <span className="font-medium text-fg-muted">{label}</span>
      <span className="ch-num text-fg">{children}</span>
    </span>
  );
}

function formatCompactBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0G';
  const gb = bytes / 1024 ** 3;
  if (gb >= 10) return `${Math.round(gb)}G`;
  if (gb >= 1) return `${gb.toFixed(1).replace(/\.0$/, '')}G`;
  const mb = Math.max(1, Math.round(bytes / 1024 ** 2));
  return `${mb}M`;
}

/**
 * Tactile Stardust hardware instrument tile: crisp `#FFFFFF` surface inside the
 * recessed stone well, concise single-line header + primary metric, optional
 * `CircularQuotaRing` gauge, discrete `SegmentedQuotaBar` ticks, and full-width subline.
 */
function TelemetryCell({
  label,
  value,
  subline,
  pct,
  tone,
  showRing = false,
  segments = 16,
  meterLabel,
  className,
}: {
  label: string;
  value: React.ReactNode;
  subline?: React.ReactNode;
  pct: number;
  tone: 'emerald' | 'coral' | 'amber' | 'sky';
  showRing?: boolean;
  segments?: number;
  /** Accessible name of the meter when it measures something other than `label`. */
  meterLabel?: string;
  className?: string;
}) {
  const safePct = Number.isFinite(pct) ? Math.max(0, Math.min(100, Math.round(pct))) : 0;
  const ringTone: QuotaRingTone = tone;
  return (
    <div
      className={cn(
        'min-w-0 flex flex-col justify-between rounded-[14px] border border-line-faint bg-surface-2 px-3 py-2.5 shadow-[var(--shadow-control)]',
        className,
      )}
      title={meterLabel}
    >
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <div className="whitespace-nowrap text-[11.5px] font-medium text-fg-muted">{label}</div>
          <div className="ch-num mt-0.5 whitespace-nowrap text-[14px] font-semibold text-fg">
            {value}
          </div>
        </div>

        {showRing && (
          <CircularQuotaRing
            value={safePct}
            size={36}
            strokeWidth={3.4}
            tone={ringTone}
          />
        )}
      </div>

      <div className="mt-2.5" aria-label={meterLabel ?? label}>
        <SegmentedQuotaBar value={safePct} segments={segments} tone={tone} />
      </div>

      {subline != null && subline !== '' && (
        <div className="ch-num mt-1.5 truncate text-[11px] text-fg-muted">{subline}</div>
      )}
    </div>
  );
}

export default WorkbenchPage;
