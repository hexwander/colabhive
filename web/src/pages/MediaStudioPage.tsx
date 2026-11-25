import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  ArrowLeft,
  Check,
  ChevronDown,
  ChevronUp,
  Clapperboard,
  Code2,
  Copy,
  Cpu,
  Download,
  ExternalLink,
  Film,
  Image as ImageIcon,
  Layers,
  Loader2,
  Music,
  Pin,
  PinOff,
  Play,
  Plus,
  RefreshCw,
  Search,
  Sparkles,
  Square,
  Terminal,
  Trash2,
  Upload,
  Wand2,
  X,
} from 'lucide-react';
import type {
  Account,
  AppInstance,
  AppLaunchRequest,
  AppTemplate,
  AssetKind,
  CapabilitiesSnapshot,
  MediaAsset,
  MediaEngineSummary,
  MediaJob,
  MediaModelInfo,
  QuotaSummary,
  UIPluginDescriptor,
} from '../types';
import { useI18n } from '../i18n';
import {
  isStudioPinnedToSidebar,
  pluginSdk,
  setStudioPinnedToSidebar,
} from '../lib/pluginSdk';
import { v11Api } from '../lib/apiV11';
import { AppLaunchDrawer } from '../components/ui/AppLaunchDrawer';
import {
  ActionButton,
  DialogModal,
  InsetWell,
  LayeredCard,
  StatusPill,
  cn,
} from '../components/ui/primitives';

export interface MediaStudioPageProps {
  accounts: Account[];
  summary: QuotaSummary;
  onNavigate?: (path: string) => void;
}

type CreationMode = 't2i' | 'i2i' | 't2v' | 'i2v' | 'audio';

const ASPECT_PRESETS: ReadonlyArray<{
  id: string;
  label: string;
  res720: string;
  res1080: string;
}> = [
  { id: '16:9', label: '16:9', res720: '1280x720', res1080: '1920x1088' },
  { id: '9:16', label: '9:16', res720: '720x1280', res1080: '1088x1920' },
  { id: '1:1', label: '1:1', res720: '1024x1024', res1080: '1280x1280' },
  { id: '4:3', label: '4:3', res720: '1024x768', res1080: '1408x1056' },
  { id: '3:2', label: '3:2', res720: '1152x768', res1080: '1536x1024' },
];

function getFallbackModels(zh: boolean): MediaModelInfo[] {
  return [
    {
      id: 'z_image_turbo',
      name: zh ? 'Z-Image Turbo (极速写实生图)' : 'Z-Image Turbo (Fast Photorealistic T2I)',
      family: 'z_image',
      family_label: 'Z-Image',
      main_output: ['image'],
      inputs: ['text'],
      cached: false,
      featured: true,
      default_steps: 8,
    },
    {
      id: 'z_image',
      name: zh ? 'Z-Image Base 6B (中英原生高精度生图)' : 'Z-Image Base 6B (High-Detail Bilingual T2I)',
      family: 'z_image',
      family_label: 'Z-Image',
      main_output: ['image'],
      inputs: ['text'],
      cached: false,
      featured: true,
      default_steps: 16,
    },
    {
      id: 'ltx2_22B_distilled',
      name: zh ? 'LTX-2 22B Distilled (音画同步极速视频)' : 'LTX-2 22B Distilled (Audio-Sync Fast Video)',
      family: 'ltx2',
      family_label: 'LTX-2',
      main_output: ['video'],
      inputs: ['text', 'image'],
      cached: false,
      featured: true,
      default_steps: 8,
    },
    {
      id: 'wan22_t2v',
      name: zh ? 'Wan 2.2 旗舰视频模型' : 'Wan 2.2 Flagship Video Model',
      family: 'wan',
      family_label: 'Wan 2.2',
      main_output: ['video'],
      inputs: ['text', 'image'],
      cached: false,
      featured: true,
      default_steps: 20,
    },
    {
      id: 'qwen3_tts_voice_design',
      name: zh ? 'Qwen3-TTS 语音合成与音色设计' : 'Qwen3-TTS Voice Design & Speech',
      family: 'qwen_tts',
      family_label: 'Qwen3-TTS',
      main_output: ['audio'],
      inputs: ['text'],
      cached: false,
      featured: true,
      default_steps: 4,
    },
  ];
}

function modelMatchesMode(m: MediaModelInfo, mode: CreationMode): boolean {
  const mainOuts = (m.main_output && m.main_output.length > 0 ? m.main_output : m.outputs ?? []).map((x) =>
    x.toLowerCase(),
  );
  const allOuts = (m.outputs && m.outputs.length > 0 ? m.outputs : mainOuts).map((x) => x.toLowerCase());
  const caps = m.capabilities ?? {};
  const hasExplicitCaps = Object.keys(caps).length > 0;

  // Never let 3D / mesh models leak into 2D Image or Video tabs.
  if (mainOuts.includes('3d') && !mainOuts.includes('image') && !mainOuts.includes('video')) {
    return false;
  }

  if (mode === 'audio') {
    if (hasExplicitCaps) {
      return mainOuts.includes('audio') || Boolean(caps.text_to_audio) || Boolean(caps.audio_to_audio);
    }
    return mainOuts.includes('audio') || allOuts.includes('audio');
  }
  if (mode === 't2i') {
    if (hasExplicitCaps) {
      return Boolean(caps.text_to_image) && mainOuts.includes('image');
    }
    return mainOuts.includes('image');
  }
  if (mode === 'i2i') {
    if (hasExplicitCaps) {
      return Boolean(caps.image_to_image) && mainOuts.includes('image');
    }
    return mainOuts.includes('image');
  }
  if (mode === 't2v') {
    if (hasExplicitCaps) {
      return Boolean(caps.text_to_video) && mainOuts.includes('video');
    }
    return mainOuts.includes('video');
  }
  // i2v
  if (hasExplicitCaps) {
    return Boolean(caps.image_to_video) && mainOuts.includes('video');
  }
  return mainOuts.includes('video');
}

function modelPriorityScore(m: MediaModelInfo, mode: CreationMode): number {
  const id = m.id.toLowerCase();
  const fam = (m.family ?? '').toLowerCase();
  let score = m.cached ? 500 : 0;
  if (m.featured) score += 20;

  if (mode === 't2i' || mode === 'i2i') {
    if (id.includes('z_image_turbo')) score += 120;
    else if (id === 'z_image' || id.startsWith('z_image_base') || fam === 'z_image') score += 110;
    else if (id === 'flux3_klein' || fam === 'flux3') score += 105;
    else if (id.includes('qwen') && id.includes('image')) score += 98;
    else if (id === 'minimax_h3_image' || fam === 'h3_image') score += 94;
    else if (id === 'fibo2_dev' || fam === 'fibo2') score += 88;
    else if (fam === 'flux2') score += 82;
    else if (fam === 'flux') score += 70;
  } else if (mode === 't2v' || mode === 'i2v') {
    if (id.includes('minimax_h3_fasth3') || id.includes('hyperflow')) score += 120;
    else if (fam === 'minimax_h3' || id.includes('minimax_h3')) score += 110;
    else if (id.includes('ltx2') && id.includes('distilled')) score += 106;
    else if (fam.includes('ltx2') || id.includes('ltx2')) score += 100;
    else if (id.includes('wan22') || id.includes('wan2_2')) score += 95;
    else if (fam === 'hunyuan_1_5') score += 85;
    else if (fam.startsWith('wan')) score += 75;
  }
  return score;
}

function formatDurationMs(ms?: number): string {
  if (!ms || ms <= 0) return '';
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function formatBytesShort(n?: number): string {
  if (!n || n <= 0) return '0 B';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export default function MediaStudioPage({ accounts, onNavigate }: MediaStudioPageProps) {
  const { locale } = useI18n();
  const zh = locale === 'zh-CN';

  // Host SDK state (strictly public `/sdk/v1/*`)
  const [caps, setCaps] = useState<CapabilitiesSnapshot | null>(null);
  const [engines, setEngines] = useState<MediaEngineSummary[]>([]);
  const [selectedInstanceId, setSelectedInstanceId] = useState<string>('');
  const [models, setModels] = useState<MediaModelInfo[]>([]);
  const [modelsLoading, setModelsLoading] = useState<boolean>(false);
  const [modelsErr, setModelsErr] = useState<string>('');

  // Local Persistent Asset Gallery
  const [assets, setAssets] = useState<MediaAsset[]>([]);
  const [assetsLoading, setAssetsLoading] = useState<boolean>(true);
  const [galleryFilter, setGalleryFilter] = useState<AssetKind | ''>('');
  const [gallerySearch, setGallerySearch] = useState<string>('');
  const [lightboxAsset, setLightboxAsset] = useState<MediaAsset | null>(null);

  // Creation form state
  const [mode, setMode] = useState<CreationMode>('t2i');
  const [modelSearch, setModelSearch] = useState<string>('');
  const [selectedModelId, setSelectedModelId] = useState<string>('z_image_turbo');
  const [prompt, setPrompt] = useState<string>('');
  const [negativePrompt, setNegativePrompt] = useState<string>('');
  const [aspectId, setAspectId] = useState<string>('16:9');
  const [qualityTier, setQualityTier] = useState<'720p' | '1080p'>('720p');
  const [steps, setSteps] = useState<number>(12);
  const [durationSec, setDurationSec] = useState<number>(4);
  const [seed, setSeed] = useState<string>('');
  const [inputAsset, setInputAsset] = useState<MediaAsset | null>(null);
  const [showAdvanced, setShowAdvanced] = useState<boolean>(false);
  const [extraJsonText, setExtraJsonText] = useState<string>('');
  const [uploadingRef, setUploadingRef] = useState<boolean>(false);

  // Active jobs & live SSE stream
  const [jobs, setJobs] = useState<MediaJob[]>([]);
  const [submitting, setSubmitting] = useState<boolean>(false);
  const [enhancing, setEnhancing] = useState<boolean>(false);
  const [showJobLogs, setShowJobLogs] = useState<boolean>(false);
  const [banner, setBanner] = useState<{ tone: 'info' | 'error' | 'success'; text: string } | null>(null);
  const [copiedId, setCopiedId] = useState<string>('');

  // UI Plugin Switcher & Launch Drawer
  const [pinnedSidebar, setPinnedSidebar] = useState<boolean>(() => isStudioPinnedToSidebar());
  const [uiMenuOpen, setUiMenuOpen] = useState<boolean>(false);
  const [pluginGuideOpen, setPluginGuideOpen] = useState<boolean>(false);
  const [wangpTemplate, setWangpTemplate] = useState<AppTemplate | null>(null);
  const [launchDrawerOpen, setLaunchDrawerOpen] = useState<boolean>(false);
  const [launchingEngine, setLaunchingEngine] = useState<boolean>(false);
  const [launchingInst, setLaunchingInst] = useState<AppInstance | null>(null);
  const [stoppingEngine, setStoppingEngine] = useState<boolean>(false);

  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const activeSseUnsubRef = useRef<(() => void) | null>(null);

  const activeEngine = useMemo(
    () => engines.find((e) => e.instance_id === selectedInstanceId) ?? engines[0] ?? null,
    [engines, selectedInstanceId],
  );

  const activeJob = useMemo(
    () => jobs.find((j) => j.state === 'running' || j.state === 'queued') ?? null,
    [jobs],
  );

  const selectedUIPluginId = caps?.ui_preferences?.['media.v1'] || 'builtin:media-studio';
  const availableUIPlugins = useMemo(
    () =>
      (caps?.ui_plugins ?? []).filter(
        (u) => !u.consumes || u.consumes.length === 0 || u.consumes.some((c) => c.startsWith('media.')),
      ),
    [caps],
  );

  const refreshHostState = useCallback(async () => {
    try {
      const [c, eng, jList] = await Promise.all([
        pluginSdk.getCapabilities(),
        pluginSdk.listMediaEngines(),
        pluginSdk.listMediaJobs(),
      ]);
      setCaps(c);
      setEngines(Array.isArray(eng) ? eng : []);
      setJobs(Array.isArray(jList) ? jList : []);
      if (Array.isArray(eng) && eng.length > 0) {
        setSelectedInstanceId((prev) =>
          prev && eng.some((e) => e.instance_id === prev) ? prev : eng[0].instance_id,
        );
      }
    } catch {
      // Quietly ignore background poll errors.
    }
  }, []);

  const refreshAssets = useCallback(async () => {
    setAssetsLoading(true);
    try {
      const list = await pluginSdk.listAssets({
        kind: galleryFilter,
        q: gallerySearch.trim() || undefined,
        limit: 120,
      });
      setAssets(Array.isArray(list) ? list : []);
    } catch {
      setAssets([]);
    } finally {
      setAssetsLoading(false);
    }
  }, [galleryFilter, gallerySearch]);

  useEffect(() => {
    void refreshHostState();
    const timer = window.setInterval(() => void refreshHostState(), 5000);
    return () => window.clearInterval(timer);
  }, [refreshHostState]);

  useEffect(() => {
    void refreshAssets();
  }, [refreshAssets]);

  // Preload WanGP template metadata so 1-click "启动云端算力引擎" opens Launch Drawer immediately!
  useEffect(() => {
    void v11Api
      .getAppTemplates()
      .then((tpls) => {
        const w = tpls.find((t) => t.id === 'wangp') ?? null;
        setWangpTemplate(w);
      })
      .catch(() => {});
  }, []);

  // Fetch live models whenever a ready headless engine is selected.
  const loadEngineModels = useCallback(
    async (instanceId: string, force = false) => {
      if (!instanceId) return;
      setModelsLoading(true);
      setModelsErr('');
      try {
        const list = await pluginSdk.listEngineModels(instanceId, force);
        setModels(list);
      } catch (err) {
        setModelsErr(err instanceof Error ? err.message : String(err));
      } finally {
        setModelsLoading(false);
      }
    },
    [],
  );

  useEffect(() => {
    if (activeEngine?.instance_id && activeEngine.media_api_ready) {
      void loadEngineModels(activeEngine.instance_id, false);
    } else {
      setModels([]);
    }
  }, [activeEngine?.instance_id, activeEngine?.media_api_ready, loadEngineModels]);

  // Subscribe to live SSE updates for any active job.
  useEffect(() => {
    if (activeSseUnsubRef.current) {
      activeSseUnsubRef.current();
      activeSseUnsubRef.current = null;
    }
    if (!activeJob) return;
    const unsub = pluginSdk.subscribeMediaJobEvents(activeJob.id, (evt) => {
      setJobs((prev) =>
        prev.map((j) => {
          if (j.id !== evt.job_id) return j;
          const nextLogs = evt.log_line
            ? [...(j.logs ?? []), evt.log_line].slice(-120)
            : j.logs;
          return {
            ...j,
            state: evt.state ?? j.state,
            phase: evt.phase ?? j.phase,
            status_text: evt.status ?? j.status_text,
            progress: typeof evt.progress === 'number' && evt.progress > 0 ? evt.progress : j.progress,
            current_step: evt.current_step ?? j.current_step,
            total_steps: evt.total_steps ?? j.total_steps,
            preview_data_url: evt.preview_data_url || j.preview_data_url,
            error: evt.error ?? j.error,
            assets: evt.assets ?? j.assets,
            logs: nextLogs,
          };
        }),
      );
      if (evt.type === 'done') {
        setBanner({
          tone: 'success',
          text: zh ? '生成完成！成品已自动同步至本地永久画廊。' : 'Generation complete! Saved to your permanent local gallery.',
        });
        void refreshAssets();
        void refreshHostState();
      } else if (evt.type === 'error') {
        setBanner({
          tone: 'error',
          text: evt.error || (zh ? '生成失败，请展开日志查看详情。' : 'Generation failed.'),
        });
      }
    });
    activeSseUnsubRef.current = unsub;
    return () => {
      unsub();
      activeSseUnsubRef.current = null;
    };
  }, [activeJob?.id, refreshAssets, refreshHostState, zh]);

  const fallbackModels = useMemo(() => getFallbackModels(zh), [zh]);
  const effectiveModels = models.length > 0 ? models : fallbackModels;
  const filteredModels = useMemo(() => {
    const q = modelSearch.trim().toLowerCase();
    return effectiveModels
      .filter((m) => {
        if (!modelMatchesMode(m, mode)) return false;
        if (!q) return true;
        return `${m.id} ${m.name} ${m.family_label ?? ''} ${m.description ?? ''}`.toLowerCase().includes(q);
      })
      .sort((a, b) => {
        const diff = modelPriorityScore(b, mode) - modelPriorityScore(a, mode);
        if (diff !== 0) return diff;
        return a.name.localeCompare(b.name);
      });
  }, [effectiveModels, mode, modelSearch]);

  // Auto-select top-ranked matching model when switching modes or when live catalog loads.
  useEffect(() => {
    if (filteredModels.length > 0 && !filteredModels.some((m) => m.id === selectedModelId)) {
      const preferred = filteredModels[0];
      if (preferred) {
        setSelectedModelId(preferred.id);
        if (preferred.default_steps) setSteps(preferred.default_steps);
      }
    }
  }, [filteredModels, selectedModelId]);

  const selectedModel = useMemo(
    () => effectiveModels.find((m) => m.id === selectedModelId) ?? filteredModels[0] ?? null,
    [effectiveModels, filteredModels, selectedModelId],
  );

  // Sync each model's true optimal step count (e.g., 2 steps for NitroFusion2, 4 for Fibo Edit Turbo, 8 for LTX-2 Distilled).
  useEffect(() => {
    if (!activeEngine?.instance_id || !activeEngine.media_api_ready || !selectedModel?.id) return;
    let cancelled = false;
    void pluginSdk
      .getModelDefaults(activeEngine.instance_id, selectedModel.id)
      .then((res) => {
        if (cancelled) return;
        const defs = res?.defaults as Record<string, unknown> | undefined;
        const defSteps = Number(defs?.num_inference_steps ?? 0);
        if (Number.isFinite(defSteps) && defSteps >= 1 && defSteps <= 60) {
          setSteps(Math.round(defSteps));
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [activeEngine?.instance_id, activeEngine?.media_api_ready, selectedModel?.id]);

  const selectedResolution = useMemo(() => {
    const p = ASPECT_PRESETS.find((x) => x.id === aspectId) ?? ASPECT_PRESETS[0];
    return qualityTier === '1080p' ? p.res1080 : p.res720;
  }, [aspectId, qualityTier]);

  const handleUploadReference = async (file: File) => {
    setUploadingRef(true);
    setBanner(null);
    try {
      const saved = await pluginSdk.uploadAsset(file, {
        prompt: zh ? `参考素材：${file.name}` : `Reference: ${file.name}`,
      });
      setInputAsset(saved);
      void refreshAssets();
    } catch (err) {
      setBanner({ tone: 'error', text: err instanceof Error ? err.message : String(err) });
    } finally {
      setUploadingRef(false);
    }
  };

  const handleEnhancePrompt = async () => {
    if (!prompt.trim()) {
      setBanner({ tone: 'info', text: zh ? '请先输入一句基础想法，再点 AI 扩写。' : 'Enter a short idea first.' });
      return;
    }
    setEnhancing(true);
    setBanner(null);
    try {
      const res = await pluginSdk.enhancePrompt({
        prompt: prompt.trim(),
        mode: mode === 't2v' || mode === 'i2v' ? 'video' : 'image',
        language: 'en',
      });
      setPrompt(res.enhanced_prompt);
      setBanner({
        tone: 'success',
        text: zh
          ? `已跨插件调用本地大模型（${res.model_name || res.model_id}）完成提示词润色`
          : `Prompt enhanced via cross-plugin LLM (${res.model_name || res.model_id})`,
      });
    } catch (err) {
      setBanner({ tone: 'error', text: err instanceof Error ? err.message : String(err) });
    } finally {
      setEnhancing(false);
    }
  };

  const handleGenerate = async () => {
    if (!activeEngine || !activeEngine.media_api_ready) {
      setLaunchDrawerOpen(true);
      return;
    }
    if (!prompt.trim()) {
      setBanner({ tone: 'info', text: zh ? '请输入提示词（支持中文或英文）。' : 'Please enter a prompt.' });
      return;
    }
    let extraSettings: Record<string, unknown> | undefined;
    if (extraJsonText.trim()) {
      try {
        extraSettings = JSON.parse(extraJsonText) as Record<string, unknown>;
      } catch {
        setBanner({ tone: 'error', text: zh ? '高级覆盖 JSON 格式无效。' : 'Invalid Advanced JSON.' });
        return;
      }
    }
    setSubmitting(true);
    setBanner(null);
    try {
      const parsedSeed = seed.trim() ? Number.parseInt(seed.trim(), 10) : 0;
      const job = await pluginSdk.createMediaJob({
        instance_id: activeEngine.instance_id,
        model_id: selectedModel?.id || selectedModelId,
        model_name: selectedModel?.name,
        mode,
        prompt: prompt.trim(),
        negative_prompt: negativePrompt.trim() || undefined,
        resolution: selectedResolution,
        steps: mode === 'audio' ? undefined : steps,
        seed: Number.isFinite(parsedSeed) && parsedSeed > 0 ? parsedSeed : undefined,
        duration_sec: mode === 't2v' || mode === 'i2v' || mode === 'audio' ? durationSec : undefined,
        input_asset_id: (mode === 'i2i' || mode === 'i2v') && inputAsset ? inputAsset.id : undefined,
        source_plugin_id: 'builtin:media-studio',
        extra_settings: extraSettings,
      });
      setJobs((prev) => [job, ...prev.filter((x) => x.id !== job.id)]);
    } catch (err) {
      setBanner({ tone: 'error', text: err instanceof Error ? err.message : String(err) });
    } finally {
      setSubmitting(false);
    }
  };

  const handleCancelJob = async (jobId: string) => {
    try {
      await pluginSdk.cancelMediaJob(jobId);
      void refreshHostState();
    } catch (err) {
      setBanner({ tone: 'error', text: err instanceof Error ? err.message : String(err) });
    }
  };

  const handleSelectUIPlugin = async (ui: UIPluginDescriptor) => {
    setUiMenuOpen(false);
    setBanner(null);
    try {
      await pluginSdk.setUIPreference('media.v1', ui.id);
      if (ui.mode === 'local_bundle') {
        const sess = await pluginSdk.openUIPlugin(ui.id);
        window.open(sess.url, '_blank', 'noopener,noreferrer');
        setBanner({
          tone: 'success',
          text: zh
            ? `已切换默认创作台为「${ui.name_zh || ui.name}」，并在独立沙箱 ${sess.url} 打开`
            : `Switched default UI to ${ui.name} at ${sess.url}`,
        });
      } else if (ui.mode === 'remote_proxy') {
        if (activeEngine?.native_url) {
          window.open(activeEngine.native_url, '_blank', 'noopener,noreferrer');
        } else {
          setBanner({
            tone: 'info',
            text: zh
              ? '当前无原生网页模式实例；启动引擎时可切换为「原版 Gradio 网页（专家模式）」。'
              : 'Launch an engine in Classic Native WebUI mode to open the upstream web UI.',
          });
        }
      }
      void refreshHostState();
    } catch (err) {
      setBanner({ tone: 'error', text: err instanceof Error ? err.message : String(err) });
    }
  };

  const handleLaunchEngine = async (req: AppLaunchRequest) => {
    setLaunchingEngine(true);
    setBanner(null);
    const poll = window.setInterval(async () => {
      try {
        const list = await v11Api.getAppInstances();
        const cur = list.find((x) => x.template_id === req.template_id && x.state === 'starting');
        if (cur) setLaunchingInst(cur);
      } catch {
        // ignore
      }
    }, 1500);
    try {
      const inst = await v11Api.launchApp(req);
      setLaunchingInst(inst);
      setLaunchDrawerOpen(false);
      await refreshHostState();
      if (inst.launch_mode === 'native_webui' && inst.local_url) {
        window.open(inst.local_url, '_blank', 'noopener,noreferrer');
      } else {
        setBanner({
          tone: 'success',
          text: zh
            ? `云端算力引擎已在 ${inst.assigned_gpu} 就绪！现在可直接零延迟生图/生视频。`
            : `Cloud engine ready on ${inst.assigned_gpu}!`,
        });
      }
    } catch (err) {
      setBanner({ tone: 'error', text: err instanceof Error ? err.message : String(err) });
    } finally {
      window.clearInterval(poll);
      setLaunchingEngine(false);
      setLaunchingInst(null);
    }
  };

  const handleStopActiveEngine = async () => {
    if (!activeEngine) return;
    setStoppingEngine(true);
    try {
      await v11Api.stopApp(activeEngine.instance_id, true);
      setBanner({
        tone: 'success',
        text: zh
          ? `已释放云端 ${activeEngine.assigned_gpu} 显卡（当前 0 消耗），本地画廊作品已永久保留。`
          : `Released cloud ${activeEngine.assigned_gpu} (0 CU/h burn). Local gallery remains intact.`,
      });
      await refreshHostState();
    } catch (err) {
      setBanner({ tone: 'error', text: err instanceof Error ? err.message : String(err) });
    } finally {
      setStoppingEngine(false);
    }
  };

  const handleSendAssetToMode = (asset: MediaAsset, targetMode: 'i2i' | 'i2v') => {
    setMode(targetMode);
    setInputAsset(asset);
    setLightboxAsset(null);
    setBanner({
      tone: 'info',
      text: zh
        ? `已将本地资产 ${asset.id} 载入「${targetMode === 'i2v' ? '图生视频' : '图生图'}」参考槽（无需重新下载上传）`
        : `Loaded asset ${asset.id} into ${targetMode.toUpperCase()} reference slot.`,
    });
  };

  const handleReuseAssetParams = (asset: MediaAsset) => {
    if (asset.prompt) setPrompt(asset.prompt);
    if (asset.negative_prompt) setNegativePrompt(asset.negative_prompt);
    if (asset.model_id) setSelectedModelId(asset.model_id);
    if (asset.steps) setSteps(asset.steps);
    if (asset.seed) setSeed(String(asset.seed));
    setLightboxAsset(null);
  };

  const handleDeleteAsset = async (assetId: string) => {
    try {
      await pluginSdk.deleteAsset(assetId);
      if (lightboxAsset?.id === assetId) setLightboxAsset(null);
      if (inputAsset?.id === assetId) setInputAsset(null);
      void refreshAssets();
    } catch (err) {
      setBanner({ tone: 'error', text: err instanceof Error ? err.message : String(err) });
    }
  };

  const copyText = (id: string, val: string) => {
    void navigator.clipboard?.writeText(val);
    setCopiedId(id);
    window.setTimeout(() => setCopiedId(''), 1500);
  };

  return (
    <div className="space-y-4">
      {/* Top Studio Header Bar */}
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-[20px] font-bold tracking-tight text-fg">
              {zh ? '原生多模态创作台' : 'Native Media Studio'}
            </h1>
            <StatusPill size="xs" tone="accent">
              {zh ? '0ms 本地秒开 · Plugin SDK v2' : '0ms Local · Plugin SDK v2'}
            </StatusPill>
            <StatusPill size="xs" tone="neutral">
              Powered by WanGP (DeepBeepMeep)
            </StatusPill>
          </div>
          <p className="text-[12.5px] text-fg-muted mt-0.5">
            {zh
              ? '本地极速 UI + 云端无头 GPU 引擎 + 本地永久画廊。完全基于开放 /sdk/v1 协议，支持一键替换为任意社区插件 UI。'
              : 'Local-first UI + remote headless GPU engine + persistent local vault. Built purely on public /sdk/v1.'}
          </p>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {onNavigate && (
            <ActionButton
              variant="ghost"
              size="sm"
              icon={<ArrowLeft className="w-3.5 h-3.5" />}
              onClick={() => onNavigate('/apps')}
            >
              {zh ? '返回应用商店' : 'App Store'}
            </ActionButton>
          )}
          <ActionButton
            variant={pinnedSidebar ? 'secondary' : 'ghost'}
            size="sm"
            icon={pinnedSidebar ? <PinOff className="w-3.5 h-3.5" /> : <Pin className="w-3.5 h-3.5" />}
            onClick={() => {
              const next = !pinnedSidebar;
              setPinnedSidebar(next);
              setStudioPinnedToSidebar(next);
            }}
            title={
              pinnedSidebar
                ? zh
                  ? '取消固定：空闲且离开创作台后将自动从侧边栏收起'
                  : 'Unpin from sidebar'
                : zh
                  ? '固定到侧边栏：随时一键进入原生创作台与本地画廊'
                  : 'Pin to sidebar'
            }
          >
            {pinnedSidebar ? (zh ? '已固定侧边栏' : 'Pinned') : zh ? '固定到侧边栏' : 'Pin'}
          </ActionButton>
          {/* Active Cloud Engine Pill or 1-Click Start */}
          {activeEngine ? (
            <div className="inline-flex items-center gap-2 rounded-xl border border-line-subtle bg-surface-1 px-3 py-1.5 shadow-[var(--shadow-card)]">
              <span className="w-2 h-2 rounded-full bg-[var(--emerald)] animate-pulse" />
              <div className="text-[12px] leading-tight">
                <div className="font-semibold text-fg flex items-center gap-1.5">
                  <Cpu className="w-3.5 h-3.5 text-fg-muted" />
                  <span>{activeEngine.assigned_gpu}</span>
                  <span className="text-fg-muted font-normal">· {activeEngine.account_label}</span>
                </div>
                <div className="text-[10.5px] text-fg-muted">
                  {activeEngine.media_api_ready
                    ? zh
                      ? 'media.v1 无头引擎在线'
                      : 'media.v1 headless ready'
                    : zh
                      ? '原版网页模式（点右侧打开）'
                      : 'Classic WebUI mode'}
                </div>
              </div>
              <ActionButton
                variant="danger"
                size="xs"
                loading={stoppingEngine}
                onClick={handleStopActiveEngine}
                icon={<Square className="w-3 h-3" />}
              >
                {zh ? '释放显卡' : 'Stop GPU'}
              </ActionButton>
            </div>
          ) : (
            <ActionButton
              variant="primary"
              size="sm"
              icon={<Play className="w-3.5 h-3.5" />}
              onClick={() => setLaunchDrawerOpen(true)}
            >
              {zh ? '启动云端显卡引擎' : 'Launch Cloud GPU Engine'}
            </ActionButton>
          )}

          {/* Replaceable UI Plugin Switcher */}
          <div className="relative">
            <ActionButton
              variant="secondary"
              size="sm"
              icon={<Layers className="w-3.5 h-3.5" />}
              onClick={() => setUiMenuOpen((v) => !v)}
            >
              {zh ? '切换插件 UI' : 'Switch UI Plugin'}
              <ChevronDown className="w-3.5 h-3.5 ml-0.5 opacity-70" />
            </ActionButton>

            {uiMenuOpen && (
              <div className="absolute right-0 mt-1.5 w-80 rounded-2xl border border-line-subtle bg-surface-1 shadow-[var(--shadow-popover)] p-2 z-40 space-y-1">
                <div className="px-2.5 py-1.5 flex items-center justify-between border-b border-line-faint">
                  <span className="text-[11px] font-semibold uppercase tracking-wider text-fg-muted">
                    {zh ? 'media.v1 可选界面插件' : 'Selectable media.v1 UI Plugins'}
                  </span>
                  <button
                    type="button"
                    onClick={async () => {
                      const next = await pluginSdk.reloadUIPlugins();
                      setCaps(next);
                    }}
                    className="text-[11px] text-fg-muted hover:text-fg inline-flex items-center gap-1 cursor-pointer"
                  >
                    <RefreshCw className="w-3 h-3" />
                    {zh ? '扫描' : 'Rescan'}
                  </button>
                </div>

                {availableUIPlugins.map((ui) => {
                  const isCurrent = selectedUIPluginId === ui.id;
                  return (
                    <button
                      key={ui.id}
                      type="button"
                      onClick={() => void handleSelectUIPlugin(ui)}
                      className={cn(
                        'w-full text-left rounded-xl px-2.5 py-2 transition-colors cursor-pointer flex items-start justify-between gap-2',
                        isCurrent ? 'bg-surface-2' : 'hover:bg-surface-2/70',
                      )}
                    >
                      <div className="min-w-0">
                        <div className="text-[12.5px] font-semibold text-fg flex items-center gap-1.5">
                          <span className="truncate">{zh ? ui.name_zh || ui.name : ui.name}</span>
                          {!ui.builtin && (
                            <StatusPill size="xs" tone="emerald">
                              {zh ? '社区 UI' : 'Community'}
                            </StatusPill>
                          )}
                        </div>
                        <div className="text-[11px] text-fg-muted line-clamp-2 mt-0.5">
                          {zh ? ui.description_zh || ui.description : ui.description}
                        </div>
                      </div>
                      <span className="shrink-0 mt-0.5">
                        {isCurrent ? (
                          <Check className="w-4 h-4 text-[var(--emerald)]" />
                        ) : ui.mode === 'local_bundle' || ui.mode === 'remote_proxy' ? (
                          <ExternalLink className="w-3.5 h-3.5 text-fg-muted" />
                        ) : null}
                      </span>
                    </button>
                  );
                })}

                <div className="pt-1 border-t border-line-faint">
                  <button
                    type="button"
                    onClick={() => {
                      setUiMenuOpen(false);
                      setPluginGuideOpen(true);
                    }}
                    className="w-full text-left rounded-xl px-2.5 py-2 text-[12px] font-medium text-fg-secondary hover:bg-surface-2 flex items-center gap-2 cursor-pointer"
                  >
                    <Code2 className="w-3.5 h-3.5" />
                    {zh ? '开发者：如何 10 分钟写一个更强的 UI 插件？' : 'Build a custom UI plugin in 10 mins'}
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>
      </div>

      {banner && (
        <div
          className={cn(
            'rounded-xl border px-3.5 py-2.5 text-[12.5px] flex items-center justify-between gap-3',
            banner.tone === 'error'
              ? 'border-[var(--coral-border)] bg-[var(--coral-soft)] text-[var(--coral-text)]'
              : banner.tone === 'success'
                ? 'border-[var(--emerald-border)] bg-[var(--emerald-soft)] text-[var(--emerald-text)]'
                : 'border-line-subtle bg-surface-1 text-fg-secondary',
          )}
        >
          <div className="flex items-center gap-2 min-w-0">
            <AlertCircle className="w-4 h-4 shrink-0" />
            <span className="break-words">{banner.text}</span>
          </div>
          <button type="button" onClick={() => setBanner(null)} className="shrink-0 opacity-70 hover:opacity-100 cursor-pointer">
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      )}

      {/* Main Split Studio Layout: Left Creation Panel + Right Live Canvas & Persistent Gallery */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-4 items-start">
        {/* LEFT: Creation Dock */}
        <LayeredCard padding="md" className="lg:col-span-5 xl:col-span-4 space-y-4">
          {/* Creation Mode Switcher */}
          <div className="grid grid-cols-5 gap-1 rounded-xl bg-surface-2 p-1 border border-line-faint">
            {(
              [
                { id: 't2i', label: zh ? '文生图' : 'T2I', icon: ImageIcon },
                { id: 'i2i', label: zh ? '图生图' : 'I2I', icon: Wand2 },
                { id: 't2v', label: zh ? '文生视频' : 'T2V', icon: Film },
                { id: 'i2v', label: zh ? '图生视频' : 'I2V', icon: Clapperboard },
                { id: 'audio', label: zh ? '音频' : 'Audio', icon: Music },
              ] as const
            ).map((item) => {
              const Icon = item.icon;
              const active = mode === item.id;
              return (
                <button
                  key={item.id}
                  type="button"
                  onClick={() => setMode(item.id)}
                  className={cn(
                    'flex flex-col items-center justify-center gap-0.5 py-1.5 rounded-lg text-[11px] font-semibold transition-all cursor-pointer',
                    active
                      ? 'bg-surface-1 text-fg shadow-[var(--shadow-control)]'
                      : 'text-fg-muted hover:text-fg',
                  )}
                >
                  <Icon className="w-3.5 h-3.5" />
                  <span>{item.label}</span>
                </button>
              );
            })}
          </div>

          {/* Model Selector */}
          <div className="space-y-1.5">
            <div className="flex items-center justify-between">
              <label className="text-[12px] font-semibold text-fg">
                {zh ? '模型选择' : 'Model'}
              </label>
              <div className="flex items-center gap-2">
                {modelsLoading && <Loader2 className="w-3.5 h-3.5 animate-spin text-fg-muted" />}
                {activeEngine?.media_api_ready && (
                  <button
                    type="button"
                    onClick={() => void loadEngineModels(activeEngine.instance_id, true)}
                    className="text-[11px] text-fg-muted hover:text-fg inline-flex items-center gap-1 cursor-pointer"
                  >
                    <RefreshCw className="w-3 h-3" />
                    {zh ? '刷新目录' : 'Refresh'}
                  </button>
                )}
              </div>
            </div>

            <div className="relative">
              <Search className="w-3.5 h-3.5 text-fg-muted absolute left-2.5 top-2.5 pointer-events-none" />
              <input
                type="text"
                value={modelSearch}
                onChange={(e) => setModelSearch(e.target.value)}
                placeholder={zh ? '搜索 Z-Image / Flux / Wan 2.2 / LTX-2 / MiniMax H3...' : 'Filter models...'}
                className="w-full rounded-xl border border-line-subtle bg-surface-2 pl-8 pr-3 py-1.5 text-[12px] text-fg placeholder:text-fg-muted focus:outline-none focus:border-[var(--accent)]"
              />
            </div>

            <div className="max-h-40 overflow-y-auto space-y-1 pr-0.5">
              {filteredModels.slice(0, 30).map((m) => {
                const picked = m.id === (selectedModel?.id ?? selectedModelId);
                return (
                  <button
                    key={m.id}
                    type="button"
                    onClick={() => {
                      setSelectedModelId(m.id);
                      if (m.default_steps) setSteps(m.default_steps);
                    }}
                    className={cn(
                      'w-full text-left rounded-xl border px-2.5 py-2 transition-all cursor-pointer flex items-center justify-between gap-2',
                      picked
                        ? 'border-[var(--accent)] bg-surface-2 shadow-[var(--shadow-control)]'
                        : 'border-line-faint bg-surface-1 hover:border-line-strong',
                    )}
                  >
                    <div className="min-w-0">
                      <div className="text-[12px] font-semibold text-fg truncate">{m.name}</div>
                      <div className="text-[10.5px] text-fg-muted truncate font-mono">{m.id}</div>
                    </div>
                    <div className="flex items-center gap-1 shrink-0">
                      {m.cached ? (
                        <StatusPill size="xs" tone="emerald">
                          {zh ? '已缓存' : 'Cached'}
                        </StatusPill>
                      ) : m.featured ? (
                        <StatusPill size="xs" tone="accent">
                          {zh ? '精选' : 'Top'}
                        </StatusPill>
                      ) : null}
                    </div>
                  </button>
                );
              })}
              {filteredModels.length === 0 && (
                <div className="text-[11.5px] text-fg-muted text-center py-3">
                  {zh ? '未找到匹配模型，清空搜索试试。' : 'No matching models.'}
                </div>
              )}
            </div>
            {modelsErr && (
              <div className="text-[11px] text-[var(--coral-text)]">{modelsErr}</div>
            )}
          </div>

          {/* Reference Image Slot for I2I / I2V */}
          {(mode === 'i2i' || mode === 'i2v') && (
            <InsetWell
              padding="sm"
              label={zh ? '参考图 / 首帧素材（支持从本地画廊直选）' : 'Reference / Start Frame'}
              className="space-y-2"
            >
              <input
                ref={fileInputRef}
                type="file"
                accept="image/*,video/*"
                className="hidden"
                onChange={(e) => {
                  const f = e.target.files?.[0];
                  if (f) void handleUploadReference(f);
                  e.target.value = '';
                }}
              />
              {inputAsset ? (
                <div className="flex items-center gap-2.5">
                  <img
                    src={pluginSdk.assetFileUrl(inputAsset.id)}
                    alt={inputAsset.filename}
                    className="w-14 h-14 rounded-lg object-cover border border-line-subtle shrink-0"
                  />
                  <div className="min-w-0 flex-1">
                    <div className="text-[12px] font-semibold text-fg truncate">{inputAsset.filename}</div>
                    <div className="text-[10.5px] text-fg-muted truncate">{inputAsset.id}</div>
                  </div>
                  <ActionButton variant="ghost" size="xs" onClick={() => setInputAsset(null)}>
                    <X className="w-3.5 h-3.5" />
                  </ActionButton>
                </div>
              ) : (
                <button
                  type="button"
                  disabled={uploadingRef}
                  onClick={() => fileInputRef.current?.click()}
                  className="w-full rounded-xl border border-dashed border-line-strong py-3 px-3 text-[12px] text-fg-secondary hover:bg-surface-1 flex items-center justify-center gap-2 cursor-pointer"
                >
                  {uploadingRef ? <Loader2 className="w-4 h-4 animate-spin" /> : <Upload className="w-4 h-4" />}
                  {zh ? '上传本地图片，或在右侧画廊点「发送到图生视频」' : 'Upload image or pick from gallery'}
                </button>
              )}
            </InsetWell>
          )}

          {/* Prompt Composer + Cross-Plugin LLM Enhancement */}
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <label className="text-[12px] font-semibold text-fg">
                {zh ? '创作提示词（Prompt）' : 'Prompt'}
              </label>
              <button
                type="button"
                disabled={enhancing}
                onClick={() => void handleEnhancePrompt()}
                title={
                  caps?.llm_ready
                    ? zh
                      ? `跨插件调用本地大模型（${caps.llm_model_name}）润色提示词`
                      : `Enhance using live LLM (${caps.llm_model_name})`
                    : zh
                      ? '启动无审查大模型后即可一键 AI 扩写'
                      : 'Start an LLM to enable AI prompt enhancement'
                }
                className="inline-flex items-center gap-1 rounded-lg px-2 py-0.5 text-[11px] font-semibold transition-colors cursor-pointer bg-surface-2 hover:bg-surface-3 text-fg-secondary border border-line-subtle"
              >
                {enhancing ? <Loader2 className="w-3 h-3 animate-spin" /> : <Sparkles className="w-3 h-3 text-[var(--amber-text)]" />}
                {zh ? 'AI 扩写提示词' : 'AI Enhance'}
                {caps?.llm_ready && <span className="w-1.5 h-1.5 rounded-full bg-[var(--emerald)]" />}
              </button>
            </div>
            <textarea
              rows={4}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder={
                zh
                  ? '描述你想生成的画面，例如：一只穿宇航服的可爱小兔子漂浮在宇宙空间站，电影级霓虹光影，极致细节...'
                  : 'Describe your scene in detail...'
              }
              className="w-full rounded-xl border border-line-subtle bg-surface-2 px-3 py-2 text-[12.5px] text-fg placeholder:text-fg-muted focus:outline-none focus:border-[var(--accent)] resize-y"
            />
          </div>

          {/* Aspect Ratio & Quality */}
          {mode !== 'audio' && (
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <span className="text-[12px] font-semibold text-fg">{zh ? '画幅与清晰度' : 'Aspect & Quality'}</span>
                <div className="inline-flex rounded-lg bg-surface-2 p-0.5 border border-line-faint text-[11px]">
                  {(['720p', '1080p'] as const).map((tier) => (
                    <button
                      key={tier}
                      type="button"
                      onClick={() => setQualityTier(tier)}
                      className={cn(
                        'px-2 py-0.5 rounded-md font-semibold cursor-pointer',
                        qualityTier === tier ? 'bg-surface-1 text-fg shadow-xs' : 'text-fg-muted',
                      )}
                    >
                      {tier}
                    </button>
                  ))}
                </div>
              </div>
              <div className="grid grid-cols-5 gap-1.5">
                {ASPECT_PRESETS.map((p) => (
                  <button
                    key={p.id}
                    type="button"
                    onClick={() => setAspectId(p.id)}
                    className={cn(
                      'rounded-xl border py-1.5 text-[11.5px] font-semibold transition-all cursor-pointer',
                      aspectId === p.id
                        ? 'border-[var(--accent)] bg-surface-2 text-fg shadow-[var(--shadow-control)]'
                        : 'border-line-faint bg-surface-1 text-fg-muted hover:text-fg',
                    )}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
              <div className="text-[11px] text-fg-muted text-right font-mono">{selectedResolution}</div>
            </div>
          )}

          {/* Steps & Video Duration */}
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1">
              <div className="flex items-center justify-between text-[11.5px]">
                <span className="font-semibold text-fg">{zh ? '推理步数' : 'Steps'}</span>
                <span className="ch-num text-fg-secondary">{steps}</span>
              </div>
              <input
                type="range"
                min={1}
                max={40}
                value={steps}
                onChange={(e) => setSteps(Number(e.target.value))}
                className="w-full accent-[var(--accent)]"
              />
            </div>

            {mode === 't2v' || mode === 'i2v' || mode === 'audio' ? (
              <div className="space-y-1">
                <div className="flex items-center justify-between text-[11.5px]">
                  <span className="font-semibold text-fg">{mode === 'audio' ? (zh ? '音频时长' : 'Audio Length') : zh ? '视频时长' : 'Duration'}</span>
                  <span className="ch-num text-fg-secondary">{durationSec}s</span>
                </div>
                <input
                  type="range"
                  min={2}
                  max={mode === 'audio' ? 30 : 10}
                  value={durationSec}
                  onChange={(e) => setDurationSec(Number(e.target.value))}
                  className="w-full accent-[var(--accent)]"
                />
              </div>
            ) : (
              <div className="space-y-1">
                <label className="text-[11.5px] font-semibold text-fg block">
                  {zh ? '随机种子（留空随机）' : 'Seed (optional)'}
                </label>
                <input
                  type="text"
                  value={seed}
                  onChange={(e) => setSeed(e.target.value.replace(/[^\d]/g, ''))}
                  placeholder={zh ? '随机' : 'Random'}
                  className="w-full rounded-lg border border-line-subtle bg-surface-2 px-2.5 py-1 text-[12px] font-mono text-fg"
                />
              </div>
            )}
          </div>

          {/* Collapsible Advanced Settings */}
          <div className="border-t border-line-faint pt-2">
            <button
              type="button"
              onClick={() => setShowAdvanced((v) => !v)}
              className="w-full flex items-center justify-between text-[11.5px] font-medium text-fg-muted hover:text-fg cursor-pointer"
            >
              <span>{zh ? '高级参数（负向提示词 / 引擎自定义 JSON）' : 'Advanced (Negative Prompt / Extra JSON)'}</span>
              {showAdvanced ? <ChevronUp className="w-3.5 h-3.5" /> : <ChevronDown className="w-3.5 h-3.5" />}
            </button>
            {showAdvanced && (
              <div className="mt-2.5 space-y-2.5">
                <input
                  type="text"
                  value={negativePrompt}
                  onChange={(e) => setNegativePrompt(e.target.value)}
                  placeholder={zh ? '负向提示词（不想出现的元素）' : 'Negative prompt'}
                  className="w-full rounded-xl border border-line-subtle bg-surface-2 px-3 py-1.5 text-[12px] text-fg"
                />
                <textarea
                  rows={2}
                  value={extraJsonText}
                  onChange={(e) => setExtraJsonText(e.target.value)}
                  placeholder='{"guidance_scale": 5.0}'
                  className="w-full rounded-xl border border-line-subtle bg-surface-2 px-3 py-1.5 text-[11.5px] font-mono text-fg"
                />
              </div>
            )}
          </div>

          {/* Primary Action CTA */}
          {activeJob ? (
            <ActionButton
              variant="danger"
              size="md"
              className="w-full justify-center"
              icon={<Square className="w-4 h-4" />}
              onClick={() => void handleCancelJob(activeJob.id)}
            >
              {zh ? `正在生成（${activeJob.progress}%）· 点击取消` : `Generating (${activeJob.progress}%) · Cancel`}
            </ActionButton>
          ) : (
            <ActionButton
              variant="primary"
              size="md"
              loading={submitting}
              className="w-full justify-center"
              icon={<Sparkles className="w-4 h-4" />}
              onClick={() => void handleGenerate()}
            >
              {activeEngine?.media_api_ready
                ? zh
                  ? `立即生成（${activeEngine.assigned_gpu}）`
                  : `Generate on ${activeEngine.assigned_gpu}`
                : zh
                  ? '启动云端显卡后开始生成'
                  : 'Launch Cloud GPU to Generate'}
            </ActionButton>
          )}
        </LayeredCard>

        {/* RIGHT: Live Generation Canvas + Persistent Local Asset Gallery */}
        <div className="lg:col-span-7 xl:col-span-8 space-y-4">
          {/* Active Job Live Progress & Denoising Preview Card */}
          {activeJob && (
            <LayeredCard padding="md" className="space-y-3 border-[var(--accent)]">
              <div className="flex items-center justify-between gap-2">
                <div className="flex items-center gap-2 min-w-0">
                  <Loader2 className="w-4 h-4 animate-spin text-[var(--accent)] shrink-0" />
                  <span className="text-[13px] font-semibold text-fg truncate">
                    {activeJob.status_text || (zh ? '云端正在推理生成...' : 'Generating on cloud GPU...')}
                  </span>
                </div>
                <div className="flex items-center gap-2 shrink-0">
                  {activeJob.total_steps ? (
                    <StatusPill size="xs" tone="accent">
                      Step {activeJob.current_step ?? 0}/{activeJob.total_steps}
                    </StatusPill>
                  ) : null}
                  <span className="ch-num text-[12px] font-bold text-fg">{activeJob.progress}%</span>
                </div>
              </div>

              <div className="h-2 w-full rounded-full bg-surface-2 overflow-hidden">
                <div
                  className="h-full rounded-full transition-all duration-300"
                  style={{
                    width: `${Math.max(4, Math.min(100, activeJob.progress))}%`,
                    backgroundColor: 'var(--accent)',
                  }}
                />
              </div>

              {activeJob.preview_data_url && (
                <div className="rounded-xl overflow-hidden border border-line-subtle bg-black/5 flex justify-center">
                  <img
                    src={activeJob.preview_data_url}
                    alt="Live denoising preview"
                    className="max-h-72 object-contain"
                  />
                </div>
              )}

              <div>
                <button
                  type="button"
                  onClick={() => setShowJobLogs((v) => !v)}
                  className="text-[11.5px] text-fg-muted hover:text-fg inline-flex items-center gap-1.5 cursor-pointer"
                >
                  <Terminal className="w-3.5 h-3.5" />
                  {zh
                    ? `${showJobLogs ? '收起' : '展开'}云端推理日志（${activeJob.logs?.length ?? 0} 行）`
                    : `${showJobLogs ? 'Hide' : 'Show'} cloud worker logs (${activeJob.logs?.length ?? 0})`}
                </button>
                {showJobLogs && (
                  <pre className="mt-2 max-h-44 overflow-y-auto rounded-xl bg-neutral-950 text-neutral-100 p-3 text-[11px] font-mono leading-relaxed whitespace-pre-wrap">
                    {(activeJob.logs ?? []).length > 0
                      ? activeJob.logs!.join('\n')
                      : zh
                        ? '正在等待远端日志输出...'
                        : 'Waiting for worker output...'}
                  </pre>
                )}
              </div>
            </LayeredCard>
          )}

          {/* Persistent Local Gallery Card */}
          <LayeredCard padding="md" className="space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2">
                <h2 className="text-[14px] font-bold text-fg">
                  {zh ? '本地永久画廊' : 'Local Asset Vault'}
                </h2>
                <StatusPill size="xs" tone="neutral">
                  {assets.length} {zh ? '个作品' : 'items'}
                </StatusPill>
                {!activeEngine && (
                  <StatusPill size="xs" tone="emerald">
                    {zh ? '离线可用 · 0 消耗' : 'Offline · 0 burn'}
                  </StatusPill>
                )}
              </div>

              <div className="flex flex-wrap items-center gap-2">
                <div className="inline-flex rounded-xl bg-surface-2 p-0.5 border border-line-faint text-[11.5px]">
                  {(
                    [
                      { id: '', label: zh ? '全部' : 'All' },
                      { id: 'image', label: zh ? '图片' : 'Images' },
                      { id: 'video', label: zh ? '视频' : 'Videos' },
                      { id: 'audio', label: zh ? '音频' : 'Audio' },
                    ] as const
                  ).map((f) => (
                    <button
                      key={f.id || 'all'}
                      type="button"
                      onClick={() => setGalleryFilter(f.id)}
                      className={cn(
                        'px-2.5 py-1 rounded-lg font-medium cursor-pointer',
                        galleryFilter === f.id ? 'bg-surface-1 text-fg shadow-xs' : 'text-fg-muted hover:text-fg',
                      )}
                    >
                      {f.label}
                    </button>
                  ))}
                </div>

                <div className="relative">
                  <Search className="w-3.5 h-3.5 text-fg-muted absolute left-2.5 top-2 pointer-events-none" />
                  <input
                    type="text"
                    value={gallerySearch}
                    onChange={(e) => setGallerySearch(e.target.value)}
                    placeholder={zh ? '搜索提示词或模型...' : 'Search prompts...'}
                    className="w-40 sm:w-48 rounded-xl border border-line-subtle bg-surface-2 pl-8 pr-2.5 py-1 text-[12px] text-fg focus:outline-none focus:border-[var(--accent)]"
                  />
                </div>
              </div>
            </div>

            {assetsLoading ? (
              <div className="py-12 flex items-center justify-center text-fg-muted gap-2 text-[12.5px]">
                <Loader2 className="w-4 h-4 animate-spin" />
                {zh ? '正在加载本地资产库...' : 'Loading local gallery...'}
              </div>
            ) : assets.length === 0 ? (
              <div className="rounded-2xl border border-dashed border-line-subtle bg-surface-2/40 py-14 px-6 text-center space-y-2">
                <div className="w-10 h-10 rounded-2xl bg-surface-2 border border-line-subtle mx-auto flex items-center justify-center text-fg-muted">
                  <Sparkles className="w-5 h-5" />
                </div>
                <div className="text-[13.5px] font-semibold text-fg">
                  {zh ? '你的本地永久画廊还是空的' : 'Your persistent local gallery is empty'}
                </div>
                <p className="text-[12px] text-fg-muted max-w-md mx-auto">
                  {zh
                    ? '在左侧输入提示词并启动云端显卡生成第一张图片或视频。生成结果会自动同步到本地磁盘，随时关停云端 A100 作品也不会丢失，还可在不同插件之间一键流转！'
                    : 'Generate your first image or video on the left. Outputs sync automatically to local disk and stay forever even after you stop your cloud GPU.'}
                </p>
                {!activeEngine && (
                  <div className="pt-2 flex items-center justify-center gap-2">
                    <ActionButton
                      variant="primary"
                      size="sm"
                      icon={<Plus className="w-3.5 h-3.5" />}
                      onClick={() => setLaunchDrawerOpen(true)}
                    >
                      {zh ? '一键启动 WanGP 无头算力引擎' : 'Launch WanGP Headless Engine'}
                    </ActionButton>
                    {onNavigate && (
                      <ActionButton variant="ghost" size="sm" onClick={() => onNavigate('/apps')}>
                        {zh ? '返回应用商店' : 'Back to App Store'}
                      </ActionButton>
                    )}
                  </div>
                )}
              </div>
            ) : (
              <div className="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 gap-3">
                {assets.map((asset) => {
                  const fileUrl = pluginSdk.assetFileUrl(asset.id);
                  return (
                    <div
                      key={asset.id}
                      onClick={() => setLightboxAsset(asset)}
                      className="group rounded-2xl border border-line-subtle bg-surface-2/40 overflow-hidden hover:border-line-strong hover:shadow-[var(--shadow-card)] transition-all cursor-pointer flex flex-col"
                    >
                      <div className="relative aspect-video bg-black/10 overflow-hidden flex items-center justify-center">
                        {asset.kind === 'video' ? (
                          <video
                            src={fileUrl}
                            muted
                            playsInline
                            preload="metadata"
                            className="w-full h-full object-cover"
                          />
                        ) : asset.kind === 'audio' ? (
                          <div className="flex flex-col items-center gap-2 text-fg-muted p-4">
                            <Music className="w-8 h-8" />
                            <audio src={fileUrl} controls className="w-full max-w-[220px]" onClick={(e) => e.stopPropagation()} />
                          </div>
                        ) : (
                          <img
                            src={fileUrl}
                            alt={asset.prompt || asset.filename}
                            loading="lazy"
                            className="w-full h-full object-cover group-hover:scale-[1.02] transition-transform duration-300"
                          />
                        )}
                        <div className="absolute top-2 left-2 flex items-center gap-1">
                          <StatusPill size="xs" tone={asset.kind === 'video' ? 'accent' : 'neutral'}>
                            {asset.kind === 'video' ? 'VIDEO' : asset.kind === 'audio' ? 'AUDIO' : 'IMAGE'}
                          </StatusPill>
                          {asset.generation_ms ? (
                            <StatusPill size="xs" tone="neutral">
                              {formatDurationMs(asset.generation_ms)}
                            </StatusPill>
                          ) : null}
                        </div>
                      </div>

                      <div className="p-2.5 space-y-1.5 flex-1 flex flex-col justify-between">
                        <p className="text-[12px] text-fg line-clamp-2 leading-snug">
                          {asset.prompt || asset.filename}
                        </p>
                        <div className="flex items-center justify-between gap-1 text-[10.5px] text-fg-muted">
                          <span className="truncate font-mono">{asset.model_name || asset.model_id || 'Local'}</span>
                          <span className="shrink-0">{formatBytesShort(asset.size_bytes)}</span>
                        </div>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </LayeredCard>
        </div>
      </div>

      {/* Asset Detail Lightbox + Cross-Plugin Pipeline Actions */}
      <DialogModal
        open={Boolean(lightboxAsset)}
        onClose={() => setLightboxAsset(null)}
        title={lightboxAsset?.model_name || lightboxAsset?.model_id || (zh ? '作品详情' : 'Asset Details')}
        size="lg"
      >
        {lightboxAsset && (
          <div className="space-y-4">
            <div className="rounded-2xl overflow-hidden bg-black/90 flex items-center justify-center max-h-[60vh]">
              {lightboxAsset.kind === 'video' ? (
                <video
                  src={pluginSdk.assetFileUrl(lightboxAsset.id)}
                  controls
                  autoPlay
                  loop
                  className="max-h-[60vh] w-full object-contain"
                />
              ) : lightboxAsset.kind === 'audio' ? (
                <div className="p-8 w-full">
                  <audio src={pluginSdk.assetFileUrl(lightboxAsset.id)} controls className="w-full" />
                </div>
              ) : (
                <img
                  src={pluginSdk.assetFileUrl(lightboxAsset.id)}
                  alt={lightboxAsset.prompt || lightboxAsset.filename}
                  className="max-h-[60vh] object-contain"
                />
              )}
            </div>

            {lightboxAsset.prompt && (
              <InsetWell
                padding="sm"
                label="Prompt"
                headerRight={
                  <button
                    type="button"
                    onClick={() => copyText(lightboxAsset.id, lightboxAsset.prompt || '')}
                    className="text-[11px] text-fg-muted hover:text-fg inline-flex items-center gap-1 cursor-pointer"
                  >
                    {copiedId === lightboxAsset.id ? <Check className="w-3 h-3 text-[var(--emerald)]" /> : <Copy className="w-3 h-3" />}
                    {copiedId === lightboxAsset.id ? (zh ? '已复制' : 'Copied') : zh ? '复制' : 'Copy'}
                  </button>
                }
              >
                <p className="text-[12.5px] text-fg whitespace-pre-wrap">{lightboxAsset.prompt}</p>
              </InsetWell>
            )}

            <div className="flex flex-wrap gap-2 text-[11.5px] text-fg-muted">
              {lightboxAsset.resolution && <StatusPill size="xs" tone="neutral">{lightboxAsset.resolution}</StatusPill>}
              {lightboxAsset.steps ? <StatusPill size="xs" tone="neutral">{lightboxAsset.steps} steps</StatusPill> : null}
              {lightboxAsset.seed ? <StatusPill size="xs" tone="neutral">seed {lightboxAsset.seed}</StatusPill> : null}
              {lightboxAsset.generation_ms ? (
                <StatusPill size="xs" tone="emerald">{formatDurationMs(lightboxAsset.generation_ms)}</StatusPill>
              ) : null}
              <StatusPill size="xs" tone="neutral">{formatBytesShort(lightboxAsset.size_bytes)}</StatusPill>
              {lightboxAsset.parent_asset_id && (
                <StatusPill size="xs" tone="accent">
                  {zh ? `源自 ${lightboxAsset.parent_asset_id}` : `from ${lightboxAsset.parent_asset_id}`}
                </StatusPill>
              )}
            </div>

            <div className="flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-line-faint">
              <div className="flex flex-wrap items-center gap-2">
                <ActionButton
                  variant="secondary"
                  size="sm"
                  icon={<RefreshCw className="w-3.5 h-3.5" />}
                  onClick={() => handleReuseAssetParams(lightboxAsset)}
                >
                  {zh ? '复用参数' : 'Reuse Params'}
                </ActionButton>
                {lightboxAsset.kind === 'image' && (
                  <>
                    <ActionButton
                      variant="primary"
                      size="sm"
                      icon={<Clapperboard className="w-3.5 h-3.5" />}
                      onClick={() => handleSendAssetToMode(lightboxAsset, 'i2v')}
                    >
                      {zh ? '发送到图生视频' : 'Send to I2V'}
                    </ActionButton>
                    <ActionButton
                      variant="secondary"
                      size="sm"
                      icon={<Wand2 className="w-3.5 h-3.5" />}
                      onClick={() => handleSendAssetToMode(lightboxAsset, 'i2i')}
                    >
                      {zh ? '发送到图生图' : 'Send to I2I'}
                    </ActionButton>
                  </>
                )}
              </div>

              <div className="flex items-center gap-2">
                <a
                  href={pluginSdk.assetFileUrl(lightboxAsset.id)}
                  download={lightboxAsset.filename}
                  className="inline-flex items-center gap-1.5 rounded-xl border border-line-subtle bg-surface-2 px-3 py-1.5 text-[12px] font-semibold text-fg hover:bg-surface-3"
                >
                  <Download className="w-3.5 h-3.5" />
                  {zh ? '下载原文件' : 'Download'}
                </a>
                <ActionButton
                  variant="danger"
                  size="sm"
                  icon={<Trash2 className="w-3.5 h-3.5" />}
                  onClick={() => void handleDeleteAsset(lightboxAsset.id)}
                >
                  {zh ? '删除' : 'Delete'}
                </ActionButton>
              </div>
            </div>
          </div>
        )}
      </DialogModal>

      {/* Third-Party UI Plugin Developer Quickstart Modal */}
      <DialogModal
        open={pluginGuideOpen}
        onClose={() => setPluginGuideOpen(false)}
        title={zh ? 'Plugin Platform v2 · 如何替换官方 UI？' : 'Plugin Platform v2 · Replace Default UI'}
        size="md"
      >
        <div className="space-y-3 text-[12.5px] text-fg-secondary leading-relaxed">
          <p>
            {zh
              ? 'ColabHive 官方创作台不走任何私有接口，完全调用公开的 /sdk/v1/*。社区作者可以用 React / Vue / Svelte / 原生 HTML 打包任意前端，直接替代官方 UI：'
              : 'ColabHive Native Media Studio uses only public /sdk/v1/* APIs. Any community UI bundle can replace it cleanly:'}
          </p>
          <pre className="rounded-xl bg-neutral-950 text-neutral-100 p-3 text-[11px] font-mono overflow-x-auto">
{`~/.colabhive/plugins/my-cinema-ui/
├── manifest.json        # kind: "ui", consumes: ["media.image.v1", "media.video.v1"]
└── ui/dist/index.html   # <script src="/__colabhive_sdk.js"></script>`}
          </pre>
          <p className="text-[11.5px] text-fg-muted">
            {zh
              ? '放好目录后点击右上角「切换插件 UI → 扫描」，ColabHive 会把它挂载到独立隔离回环沙箱（0ms 秒开），并注入带最小权限令牌的 window.ColabHiveSDK。完整规范见仓库 docs/PLUGIN_PLATFORM_V2.md。'
              : 'Click Switch UI Plugin -> Rescan. ColabHive mounts your bundle on an isolated loopback sandbox with window.ColabHiveSDK. See docs/PLUGIN_PLATFORM_V2.md.'}
          </p>
        </div>
      </DialogModal>

      {/* Embedded Cloud Engine Launch Drawer */}
      <AppLaunchDrawer
        open={launchDrawerOpen}
        template={wangpTemplate}
        accounts={accounts}
        launching={launchingEngine}
        launchingInstance={launchingInst}
        onClose={() => setLaunchDrawerOpen(false)}
        onConfirm={handleLaunchEngine}
        onCancelLaunch={async (instId) => {
          try {
            await v11Api.stopApp(instId, true);
          } catch {
            // ignore
          }
        }}
      />
    </div>
  );
}
