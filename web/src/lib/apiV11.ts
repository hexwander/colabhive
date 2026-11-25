/**
 * ColabHive v1.1 Typed API Client & Interactive Demo Sandbox (`web/src/lib/apiV11.ts`)
 *
 * Covers:
 *  - Pillar 1: Private AI Chat & Zero-Tunnel Local `/v1` + Ollama Gateway
 *  - Pillar 2: 1-Click AI App Store & Private Local Port Bridge (`127.0.0.1:<port>`)
 *  - Pillar 3: Auto-Checkpoint Preemption Relay & Shareable Compute Savings Receipt
 *
 * When `isDemoSiteMode()` is true (e.g. GitHub Pages Live Demo), all methods
 * run against an interactive in-memory demo sandbox. In live mode, real HTTP
 * responses and errors (503/502/422/timeout/network) are surfaced honestly.
 */

import type {
  AcceleratorType,
  Account,
  AppCategory,
  AppInstance,
  AppLaunchRequest,
  AppPluginManifest,
  AppTemplate,
  CheckpointPolicy,
  CheckpointRecord,
  CheckpointResumeRequest,
  CheckpointSyncRequest,
  LLMGatewayStatus,
  LLMModelSpec,
  LLMStartRequest,
  SavingsReceiptStats,
} from '../types';
import { APIError, UNAUTHORIZED_EVENT, getStoredAdminToken } from './api';
import { isDemoSiteMode } from './demoSiteAdapter';

// ============================================================================
// Chat Streaming Types
// ============================================================================

export type ChatRole = 'system' | 'user' | 'assistant';

export interface ChatMessage {
  role: ChatRole;
  content: string;
}

export interface ChatStreamMeta {
  modelId?: string;
  tokensPerSec?: number;
  accountLabel?: string;
  assignedGpu?: AcceleratorType;
  freeTierActive?: boolean;
  failoverNote?: string;
}

export type LLMProtocolKind = 'openai' | 'responses' | 'anthropic';

export interface ChatCompletionSummary {
  content: string;
  modelId: string;
  tokensGenerated: number;
  tokensPerSec: number;
  durationMs: number;
  ttftMs?: number;
  protocol?: LLMProtocolKind;
  status?: LLMGatewayStatus;
}

export const V11_DEMO_EVENT = 'colabhive:v11-state-changed';
export const WS_V11_EVENT = 'colabhive:ws-v11';

function notifyV11StateChanged(): void {
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new CustomEvent(V11_DEMO_EVENT));
  }
}

/** Strips any raw email address from labels so zero PII ever leaks into UI/receipts. */
export function sanitizeAccountLabel(label: string | undefined | null, zh = true): string {
  if (!label || !label.trim()) {
    return zh ? '账号 #1 (免费显卡)' : 'Account #1 (Free GPU)';
  }
  const cleaned = label.replace(
    /[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}/g,
    zh ? '私有账号' : 'Private Account',
  );
  return cleaned.trim();
}

const SAFE_MODEL_TAG_RE = /^[a-zA-Z0-9._:/-]+$/;

export type CustomModelParseResult =
  | { ok: true; modelId: string }
  | { ok: false; errorZh: string; errorEn: string };

/**
 * Normalizes and validates arbitrary Hugging Face GGUF URLs / `hf.co/...` tags /
 * `ollama run ...` snippets / custom Ollama `name:tag` strings with strict
 * shell-safe character allowlisting (`^[a-zA-Z0-9._:/-]+$`, max 180 chars, no `..`).
 */
export function normalizeCustomModelInput(rawInput: string): CustomModelParseResult {
  let s = (rawInput || '').trim();
  if (!s) {
    return {
      ok: false,
      errorZh: '请输入 Hugging Face 模型链接（如 https://huggingface.co/作者/仓库-GGUF）或 hf.co/... 标签',
      errorEn: 'Please enter a Hugging Face GGUF URL (https://huggingface.co/owner/repo) or hf.co/... tag',
    };
  }

  // Strip common `ollama run ` / `ollama pull ` CLI prefixes copied from Hugging Face
  s = s.replace(/^ollama\s+(?:run|pull)\s+/i, '').trim();
  // Strip surrounding quotes if pasted from a shell snippet
  if ((s.startsWith('"') && s.endsWith('"')) || (s.startsWith("'") && s.endsWith("'"))) {
    s = s.slice(1, -1).trim();
  }

  // Normalize full Hugging Face URLs (`https://huggingface.co/owner/repo(:quant)?` or `https://hf.co/...`)
  const hfUrlMatch = s.match(
    /^https?:\/\/(?:www\.)?(?:huggingface\.co|hf\.co)\/([^/?#\s]+)\/([^/?#:\s]+)(?::([a-zA-Z0-9._-]+))?(?:\/[^\s]*)?$/i,
  );
  if (hfUrlMatch) {
    const [, owner, repo, quant] = hfUrlMatch;
    s = quant ? `hf.co/${owner}/${repo}:${quant}` : `hf.co/${owner}/${repo}`;
  } else if (/^[a-zA-Z0-9._-]+\/[a-zA-Z0-9._-]+(?::[a-zA-Z0-9._-]+)?$/.test(s) && !s.startsWith('hf.co/')) {
    // Bare `owner/repo` or `owner/repo:Q4_K_M` -> auto-prefix `hf.co/`
    s = `hf.co/${s}`;
  }

  s = s.replace(/\/+$/, '');

  if (s.length > 180) {
    return {
      ok: false,
      errorZh: '模型标识过长（最多 180 个字符）',
      errorEn: 'Model identifier too long (max 180 characters)',
    };
  }
  if (s.includes('..') || s.startsWith('-') || s.startsWith('/') || !SAFE_MODEL_TAG_RE.test(s)) {
    return {
      ok: false,
      errorZh: '包含不安全字符：仅支持字母、数字及 . _ : / -（例如 hf.co/unsloth/gemma-3-12b-it-GGUF:Q4_K_M）',
      errorEn: 'Invalid characters: only letters, digits, and . _ : / - are allowed',
    };
  }

  return { ok: true, modelId: s };
}

// ============================================================================
// Seeded Demo / Fallback Catalogs & In-Memory Interactive State
// ============================================================================

const DEMO_LLM_CATALOG: LLMModelSpec[] = [
  {
    id: 'qwen3.5-9b-heretic',
    display_name: 'Qwen3.5 9B Abliterated · Fast Free-GPU Uncensored',
    display_name_zh: 'Qwen3.5 9B Abliterated · 免费卡极速无审查',
    provider_family: 'Qwen',
    summary:
      'Top-downloaded 9B orthogonal-abliterated Qwen3.5 GGUF (511K+ HF downloads, ~5.7 GB) — zero refusal, crisp bilingual writing & fast tool calling at $0 on Free Colab T4.',
    summary_zh:
      'Hugging Face 51 万+下载口碑小钢炮（约 5.7 GB），正交消融去审查保留原版智商，中英双语、日常创作与指令遵循零拒答，免费 Colab T4 零成本秒跑。',
    parameter_size: '9B',
    quantization: 'Q4_K_M',
    context_length: 32768,
    min_vram_gb: 8,
    recommended_gpu: 'T4',
    free_tier_ready: true,
    default_engine: 'ollama',
    ollama_tag: 'hf.co/mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF:Q4_K_M',
    huggingface_repo: 'mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF',
    tags: ['免费卡秒开', '无审查去拒答', '51万+下载'],
  },
  {
    id: 'gemma4-12b-heretic',
    display_name: 'Gemma 4 12B Heretic · Free-GPU Writing & Roleplay',
    display_name_zh: 'Gemma 4 12B Heretic · 免费卡文笔与酒馆首选',
    provider_family: 'Gemma',
    summary:
      'Google Gemma 4 12B Heretic abliterated GGUF (127K+ HF downloads, 6.87 GB Q4_K_M) — natural literary prose, strong roleplay & zero lecturing right on Free 16GB Colab T4.',
    summary_zh:
      'Google 最新 Gemma 4 12B 正交 Heretic 去审查版（HF 12.7 万+下载、仅 6.87 GB），文笔细腻无说教，免费 16GB Colab T4 即可直接跑起酒馆角色扮演。',
    parameter_size: '12B',
    quantization: 'Q4_K_M',
    context_length: 65536,
    min_vram_gb: 10,
    recommended_gpu: 'T4',
    free_tier_ready: true,
    default_engine: 'ollama',
    ollama_tag: 'hf.co/culturerevolt/gemma-4-12b-heretic-abliterated-GGUF:Q4_K_M',
    huggingface_repo: 'culturerevolt/gemma-4-12b-heretic-abliterated-GGUF',
    tags: ['免费卡可跑', 'Heretic', '酒馆文笔'],
  },
  {
    id: 'qwen3.8-27b-abliterated',
    display_name: 'Qwen3.8 27B Abliterated · 4.5M-DL Community King',
    display_name_zh: 'Qwen3.8 27B Abliterated · 450万下载社区口碑王',
    provider_family: 'Qwen',
    summary:
      'The #1 community uncensored powerhouse (4.52M+ downloads, 2.5K+ likes on Hugging Face, 15.4 GB Q4_K_M). Frontier logic, coding, and unfiltered prose on 24GB L4 / A100 / 96GB G4.',
    summary_zh:
      '2026 社区实测第一梯队全能无审查王者（HF 452 万+下载、2,595 点赞、15.4 GB Q4_K_M），正交去审查几乎零降智，编程、中文长文与自由创作全面拉满。',
    parameter_size: '27B',
    quantization: 'Q4_K_M',
    context_length: 65536,
    min_vram_gb: 20,
    recommended_gpu: 'L4',
    free_tier_ready: false,
    default_engine: 'ollama',
    ollama_tag: 'hf.co/huihui-ai/Huihui-Qwen3.8-27B-abliterated-GGUF:Q4_K_M',
    huggingface_repo: 'huihui-ai/Huihui-Qwen3.8-27B-abliterated-GGUF',
    tags: ['社区TOP1', '450万+下载', '零降智去审查'],
  },
  {
    id: 'qwen3.8-27b-heretic-coder',
    display_name: 'Qwen3.8 27B Heretic NEO · 4.2K-Likes Coding & Creative',
    display_name_zh: 'Qwen3.8 27B Heretic NEO · 4.2K点赞编程与创作神作',
    provider_family: 'Qwen',
    summary:
      'Most-liked DavidAU Qwen3.8-27B Heretic NEO-CODER-MAX MTP build (1.83M+ downloads, 4,282 likes, 15.8 GB Q4_K_M) — razor-sharp coding, vivid storytelling & zero censorship on 24GB L4.',
    summary_zh:
      'Hugging Face 4,282 高赞、183 万+下载爆款 DavidAU Heretic NEO-CODER 精调版（15.8 GB），写代码与故事张力双顶级，24GB L4 显卡单卡满速驱动。',
    parameter_size: '27B',
    quantization: 'Q4_K_M',
    context_length: 65536,
    min_vram_gb: 20,
    recommended_gpu: 'L4',
    free_tier_ready: false,
    default_engine: 'ollama',
    ollama_tag:
      'hf.co/DavidAU/Qwen3.8-27B-TURBO-Fable-Cold-Fusion-735-882-Heretic-Uncensored-NEO-CODER-MAX-MTP-GGUF:Q4_K_M',
    huggingface_repo:
      'DavidAU/Qwen3.8-27B-TURBO-Fable-Cold-Fusion-735-882-Heretic-Uncensored-NEO-CODER-MAX-MTP-GGUF',
    tags: ['4.2K高赞', '编程+创作', 'Heretic'],
  },
  {
    id: 'gemma4-31b-heretic',
    display_name: 'Gemma 4 31B ARA-Heretic · Creative & Roleplay King',
    display_name_zh: 'Gemma 4 31B ARA-Heretic · 酒馆角色扮演与小说王者',
    provider_family: 'Gemma',
    summary:
      'Google Gemma 4 31B refined via ARA-Heretic orthogonal ablation (242K+ downloads, 19.4 GB Q4_K_M) — rich literary prose, 128K context, and zero lecturing for SillyTavern & writing.',
    summary_zh:
      'Google 最新 Gemma 4 31B 经 ARA-Heretic 正交去审查（HF 24 万+下载、19.4 GB），文笔自然不油腻、支持 128K 超长上下文，是 SillyTavern 酒馆角色扮演与小说创作公认口碑王。',
    parameter_size: '31B',
    quantization: 'Q4_K_M',
    context_length: 131072,
    min_vram_gb: 24,
    recommended_gpu: 'A100',
    free_tier_ready: false,
    default_engine: 'ollama',
    ollama_tag: 'hf.co/mradermacher/gemma-4-31B-it-ARA-heretic-GGUF:Q4_K_M',
    huggingface_repo: 'mradermacher/gemma-4-31B-it-ARA-heretic-GGUF',
    tags: ['酒馆RP口碑王', '128K长文', 'Heretic'],
  },
  {
    id: 'glm5.3-flash-uncensored',
    display_name: 'GLM-5.3-Flash Uncensored · Single-File Q4 Chinese Flagship',
    display_name_zh: 'GLM-5.3-Flash Uncensored · 智谱最新单文件 Q4 无审查旗舰',
    provider_family: 'GLM',
    summary:
      'Zhipu GLM-5.3-Flash 313B GSQ-RCO Uncensored single-file Q4 GGUF (127.7 GB, verified Ollama direct-pull) — runs on Colab 96GB Blackwell G4 + High-RAM with blazing Chinese writing & reasoning.',
    summary_zh:
      '智谱最新 GLM-5.3-Flash 313B GSQ-RCO 无审查单文件 Q4 版（127.7 GB，Ollama 原生直拉校验通过），依托 Colab 96GB Blackwell G4 + 高内存运行，中文创作、逻辑与 Agent 执行力极强。',
    parameter_size: '313B-MoE',
    quantization: 'Q4',
    context_length: 131072,
    min_vram_gb: 96,
    recommended_gpu: 'G4',
    free_tier_ready: false,
    default_engine: 'ollama',
    ollama_tag: 'hf.co/GCSA-AiLab/GLM-5.3-Flash-Uncensored-RCO-GSQ-GGUF:latest',
    huggingface_repo: 'GCSA-AiLab/GLM-5.3-Flash-Uncensored-RCO-GSQ-GGUF',
    tags: ['单文件直拉', '智谱最新', '中文旗舰'],
  },
  {
    id: 'qwen3.8-flash-next-abliterated',
    display_name: 'Qwen3.8-Flash-Next Abliterated · 1.75M-DL Single-G4 King',
    display_name_zh: 'Qwen3.8-Flash-Next Abliterated · 175万下载单卡旗舰',
    provider_family: 'Qwen',
    summary:
      '1.75M+ downloads flagship Qwen3.8-Flash-Next 126B Gyro Abliterated single-file GGUF (50.2 GB, verified Ollama direct-pull) — fits 100% in single 96GB G4 / 80GB H100 VRAM with huge context headroom.',
    summary_zh:
      'HF 175 万+下载 Qwen3.8-Flash-Next 126B 正交去审查单文件版（仅 50.2 GB，Ollama 原生直拉校验通过），全权重轻松装进单张 96GB G4 / 80GB H100 显存，代码、推理与无限制输出双拉满。',
    parameter_size: '126B-MoE',
    quantization: 'TQ3_0',
    context_length: 131072,
    min_vram_gb: 56,
    recommended_gpu: 'G4',
    free_tier_ready: false,
    default_engine: 'ollama',
    ollama_tag: 'hf.co/agentionai/Qwen3.8-Flash-Next-Gyro-abliterated-Huihui-GGUF:latest',
    huggingface_repo: 'agentionai/Qwen3.8-Flash-Next-Gyro-abliterated-Huihui-GGUF',
    tags: ['96GB单卡满速', '175万+下载', 'Abliterated'],
  },
  {
    id: 'deepseek-v4.1-flash-uncensored',
    display_name: 'DeepSeek-V4-Flash Abliterated · 284B MoE Single-G4 Flagship',
    display_name_zh: 'DeepSeek-V4-Flash Abliterated · 284B MoE 单卡旗舰',
    provider_family: 'DeepSeek',
    summary:
      'Top-downloaded single-file Huihui DeepSeek-V4-Flash 284B MoE Abliterated GGUF (600K+ DL, 80.8 GB Q2, verified Ollama direct-pull) — fits 100% inside a single 96GB Blackwell G4 VRAM at full speed.',
    summary_zh:
      'HF 60 万+下载 Huihui DeepSeek-V4-Flash 284B MoE 正交去审查单文件版（80.8 GB，Ollama 原生直拉校验通过），完整装进单张 96GB Blackwell G4 显存满速独享。',
    parameter_size: '284B-MoE',
    quantization: 'Q2',
    context_length: 131072,
    min_vram_gb: 84,
    recommended_gpu: 'G4',
    free_tier_ready: false,
    default_engine: 'ollama',
    ollama_tag: 'hf.co/huihui-ai/Huihui-DeepSeek-V4-Flash-abliterated-ds4-GGUF:latest',
    huggingface_repo: 'huihui-ai/Huihui-DeepSeek-V4-Flash-abliterated-ds4-GGUF',
    tags: ['单卡满速', '284B满血MoE', '96GB G4'],
  },
];

const DEMO_APP_TEMPLATES: AppTemplate[] = [
  {
    id: 'llm-chat',
    name: 'Uncensored Open-Weight LLM Deployer (/v1)',
    name_zh: '无审查开源大模型一键部署 (/v1)',
    tagline:
      '1-click Heretic & Abliterated DeepSeek-V4.1-Flash / GLM-5.3-Flash / Qwen3.8 / Gemma 4 GGUFs with local /v1 API',
    tagline_zh:
      '一键部署 DeepSeek-V4.1-Flash / GLM-5.3-Flash / Qwen3.8 / Gemma 4 无审查模型，吐出本机 /v1 接口直连酒馆与 Cherry Studio',
    description:
      'Deploy top-downloaded 2026 community uncensored (Heretic / Abliterated) open-weight LLMs or any custom Hugging Face GGUF onto pooled Colab GPUs with private /v1 endpoints for SillyTavern, Cherry Studio, Open WebUI, and Cursor.',
    description_zh:
      '精选 2026 社区下载量最高、口碑最好的 Heretic / Abliterated 正交去审查开源大模型（保留原版智商、零说教零拒答），一键部署到 Colab 显卡，吐出本机 /v1 接口直连 SillyTavern（酒馆）、Cherry Studio、Open WebUI 与 Cursor。',
    category: 'workspace',
    icon: '⚡',
    default_remote_port: 11434,
    default_local_port: 7860,
    min_vram_gb: 8,
    recommended_gpu: 'T4',
    free_tier_compatible: true,
    est_boot_sec: 25,
    highlights: [
      '2026 Heretic / Abliterated Top Models',
      'SillyTavern / Cherry Studio / Cursor /v1',
      'Hot-Reload JSON + Any hf.co GGUF',
    ],
    highlights_zh: [
      '2026 社区高口碑 Heretic / Abliterated 去审查榜',
      '本机 /v1 直连酒馆、Cherry Studio 与 Cursor',
      '支持任意 hf.co 权重与 llm_catalog.json 热更新',
    ],
    supports_port_bridge: true,
    plugin_integration_tip_en:
      'Open /apps/llm to deploy and copy http://127.0.0.1:7860/v1 into SillyTavern, Cherry Studio, or Cursor.',
    plugin_integration_tip_zh:
      '进入 /apps/llm 一键部署模型，将 http://127.0.0.1:7860/v1 填入酒馆、Cherry Studio 或 Cursor 即可。',
  },
  {
    id: 'wangp',
    name: 'WanGP Video & Image Super-Studio',
    name_zh: 'WanGP 开源视频与生图全家桶',
    tagline:
      'Zero-node 1-click AI Video & Image studio: MiniMax H3, LTX-2.5, Wan 2.2, H3-Image, Fibo 2.0, Flux 3 & Qwen Image 2.1',
    tagline_zh:
      '不用连节点的爆款视频/生图神器：一站通吃 MiniMax H3、LTX-2.5、Wan 2.2、H3-Image、Fibo 2.0、Flux 3 与 Qwen Image 2.1',
    description:
      'Launches DeepBeepMeep WanGP on pooled Colab 96GB G4, H100, A100, L4, or T4 with batch queues, native audio-video sync, built-in Hugging Face & Civitai custom weight/LoRA loader, private localhost bridge, and idle auto-stop.',
    description_zh:
      '一键部署 GitHub 1.2 万星爆款开源平台 WanGP（小白零门槛不用连节点），把 96GB G4 / H100 显存直接拉满！支持 MiniMax H3 音画同步、LTX-2.5、Wan 2.2、Fibo 2.0、Flux 3、批量排队出片与 Civitai / HF 自定义权重，用完自动停表止血。',
    category: 'video',
    icon: '🎬',
    default_remote_port: 7861,
    default_local_port: 7861,
    min_vram_gb: 8,
    recommended_gpu: 'G4',
    free_tier_compatible: true,
    est_boot_sec: 60,
    highlights: [
      'MiniMax H3 / LTX-2.5 / Wan 2.2 / Flux 3',
      'Built-In Queue + HF / Civitai LoRAs',
      '127.0.0.1:7861 Bridge + Idle Auto-Stop',
    ],
    highlights_zh: [
      'MiniMax H3 音画同步 / LTX-2.5 / Wan 2.2 / Flux 3',
      '支持批量排队出片 + 内置 Civitai / HF 自定义权重',
      '本机 7861 私有直连 + 闲置自动停卡防烧 CU',
    ],
    supports_port_bridge: true,
    supports_custom_model_url: true,
    model_options: [
      {
        id: 'minimax-h3',
        name: 'MiniMax H3 + H3-Image Profile (Audio-Video Sync · 96GB G4)',
        name_zh: 'MiniMax H3 音画同步 + H3-Image 方案（推荐 96GB G4 / H100）',
        summary: 'Ready for MiniMax H3 (HyperFlow 6-step / Singularity / 2K) & H3-Image inside WanGP.',
        summary_zh: '针对 MiniMax H3（6 步加速 / 2K 精修音画同步）与 H3-Image 优化配置，进网页直接开跑。',
        min_vram_gb: 24,
        recommended_gpu: 'G4',
        download_url: '',
      },
      {
        id: 'ltx-2.5',
        name: 'LTX-2.5 Native Audio-Video (32kHz Stereo · L4 / A100 / G4)',
        name_zh: 'LTX-2.5 原生音画同步方案（32kHz 立体声 · L4 / A100 / G4）',
        summary: 'Lightricks LTX-2.5 synchronized audio+video, camera direction & start/end frames.',
        summary_zh: '适配 Lightricks LTX-2.5 原生立体声音画同步视频、精细运镜与首尾帧控制。',
        min_vram_gb: 16,
        recommended_gpu: 'L4',
        download_url: '',
      },
      {
        id: 'wan-2.2',
        name: 'Wan 2.2 MoE A14B / 5B (Apache 2.0 · Rich LoRA Ecosystem)',
        name_zh: 'Wan 2.2 旗舰视频方案（Apache 2.0 · 社区 LoRA 生态最全）',
        summary: 'Alibaba Wan 2.2 T2V/I2V (14B MoE & 5B) with full community Civitai LoRA support.',
        summary_zh: '适配阿里 Wan 2.2 文生/图生视频（MoE 14B & 5B），海量社区无审查 LoRA 即插即用。',
        min_vram_gb: 12,
        recommended_gpu: 'L4',
        download_url: '',
      },
      {
        id: 'image-pack',
        name: '2026 Image Pack (Fibo 2.0 / Flux 3 / Qwen Image 2.1 / Z-Image)',
        name_zh: '2026 顶流生图方案（Fibo 2.0 / Flux 3 / Qwen Image 2.1 / Z-Image）',
        summary: 'High-speed photorealism, multi-ref editing & Viggle character swap.',
        summary_zh: '专注 2026 最强开源生图与多参考图编辑，支持 2~4 步极速出图与人物换装。',
        min_vram_gb: 8,
        recommended_gpu: 'T4',
        download_url: '',
      },
    ],
    plugin_integration_tip_en:
      'Open http://127.0.0.1:7861 in your browser to queue MiniMax H3 / LTX-2.5 / Wan 2.2 generations.',
    plugin_integration_tip_zh:
      '启动后打开本机 127.0.0.1:7861 即可直接排队生成视频/图片并加载 Civitai / HF 权重。',
  },
  {
    id: 'comfyui',
    name: 'ComfyUI Pro Node Studio',
    name_zh: 'ComfyUI 进阶节点工坊',
    tagline:
      'Real ComfyUI with 2026 Comfy-Org weights (Z-Image Turbo / Qwen-Image / FLUX.2) & custom HF / Civitai URLs',
    tagline_zh:
      '完整原版 ComfyUI：预置 2026 顶流权重（Z-Image Turbo / Qwen-Image / FLUX.2）与任意 HF / Civitai 自定义直链',
    description:
      'Launches authentic ComfyUI on your chosen Colab GPU and provisions 2026 Comfy-Org official weights (Z-Image Turbo, Qwen-Image FP8, FLUX.2 Dev FP8) or any custom HF/Civitai URL, bridged privately to 127.0.0.1:8188.',
    description_zh:
      '适合拖入社区 .json 工作流与硬核定制：在指定 Colab 显卡上启动完整原版 ComfyUI，可选 Z-Image Turbo（千万下载）、Qwen-Image FP8、FLUX.2 Dev FP8 或任意 HF / Civitai 直链，本机 8188 私有直连。',
    category: 'image',
    icon: '🎨',
    default_remote_port: 8188,
    default_local_port: 8188,
    min_vram_gb: 8,
    recommended_gpu: 'L4',
    free_tier_compatible: true,
    est_boot_sec: 45,
    highlights: [
      'Z-Image Turbo / Qwen-Image / FLUX.2',
      'Drop Any .json Workflow & Custom URL',
      '127.0.0.1:8188 + Auto Output Backup',
    ],
    highlights_zh: [
      '2026 预设：Z-Image Turbo / Qwen-Image / FLUX.2',
      '支持拖入社区 .json 工作流与任意直链权重',
      '本机 8188 私有直连 + 成品目录自动备份',
    ],
    supports_port_bridge: true,
    supports_custom_model_url: true,
    model_options: [
      {
        id: 'blank',
        name: 'Blank ComfyUI (0 GB preload · drop any .json workflow)',
        name_zh: '空白 ComfyUI（0 GB 预载 · 进界面后拖入社区工作流）',
        summary: 'Fastest boot with clean ComfyUI canvas and zero upfront weight download.',
        summary_zh: '最快启动：不预下载权重，进入 ComfyUI 后按需加载任意社区工作流与模型。',
        min_vram_gb: 8,
        recommended_gpu: 'T4',
        download_url: '',
      },
      {
        id: 'z-image-turbo',
        name: 'Z-Image Turbo BF16 (10M+ DL · 2-4 Step Photoreal · T4 / L4)',
        name_zh: 'Z-Image Turbo BF16（千万下载 · 2~4 步极速写实 · T4 / L4）',
        summary: 'Official Comfy-Org Z-Image Turbo BF16 diffusion model (10M+ downloads).',
        summary_zh: '从 Hugging Face 官方源真实下载 Comfy-Org Z-Image Turbo BF16，极速高写实出图。',
        min_vram_gb: 12,
        recommended_gpu: 'T4',
        download_url:
          'https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/diffusion_models/z_image_turbo_bf16.safetensors',
        target_subdir: 'models/diffusion_models',
        filename: 'z_image_turbo_bf16.safetensors',
      },
      {
        id: 'qwen-image-fp8',
        name: 'Qwen-Image FP8 (2.8M+ DL · Best Chinese/English Text · L4 / A100)',
        name_zh: 'Qwen-Image FP8（280万下载 · 中英文字排版王者 · L4 / A100）',
        summary: 'Official Comfy-Org Qwen-Image FP8 checkpoint (2.8M+ downloads) for layout & text.',
        summary_zh: '下载 Comfy-Org Qwen-Image FP8 权重，中英文海报排版与指令遵循极强。',
        min_vram_gb: 16,
        recommended_gpu: 'L4',
        download_url:
          'https://huggingface.co/Comfy-Org/Qwen-Image/resolve/main/split_files/diffusion_models/qwen_image_2512_fp8_e4m3fn.safetensors',
        target_subdir: 'models/diffusion_models',
        filename: 'qwen_image_2512_fp8_e4m3fn.safetensors',
      },
      {
        id: 'flux2-dev-fp8',
        name: 'FLUX.2 Dev FP8 (920K+ DL · Studio Photorealism · A100 / 96GB G4)',
        name_zh: 'FLUX.2 Dev FP8（92万下载 · 顶级写实画质 · A100 / 96GB G4）',
        summary: 'Official Comfy-Org FLUX.2 Dev FP8 diffusion weights for flagship image quality.',
        summary_zh: '下载 Comfy-Org FLUX.2 Dev FP8 旗舰权重，推荐 A100 / 96GB G4 显卡满速出图。',
        min_vram_gb: 24,
        recommended_gpu: 'A100',
        download_url:
          'https://huggingface.co/Comfy-Org/flux2-dev/resolve/main/split_files/diffusion_models/flux2_dev_fp8mixed.safetensors',
        target_subdir: 'models/diffusion_models',
        filename: 'flux2_dev_fp8mixed.safetensors',
      },
    ],
    plugin_integration_tip_en:
      'Open http://127.0.0.1:8188 in your browser or paste into Krita AI Diffusion — zero public tunnel.',
    plugin_integration_tip_zh:
      '支持浏览器打开或 Krita / PS 插件直连 127.0.0.1:8188，零公网穿透。',
  },
  {
    id: 'unsloth-studio',
    name: 'Unsloth Studio Fine-Tuning',
    name_zh: 'Unsloth Studio 零代码微调',
    tagline: 'Zero-code visual LLM fine-tuning & GGUF export on H100 / 96GB G4 / A100 / L4',
    tagline_zh: '拖入文档零代码微调专属 Qwen / Gemma / Llama 模型，一键导出 GGUF 带回本地',
    description:
      'Launches official Unsloth Studio (`unsloth studio`) privately on 127.0.0.1:8890. Drop your docs/notes, fine-tune 2x faster with 70% less VRAM, verify in UI, and export GGUF weights.',
    description_zh:
      '一键启动官方可视化微调工作台 Unsloth Studio：无需写脚本，拖入文档洗训练集，在 H100 / 96GB G4 上 2 倍速微调并直接导出 GGUF 权重。',
    category: 'workspace',
    icon: '🦥',
    default_remote_port: 8890,
    default_local_port: 8890,
    min_vram_gb: 12,
    recommended_gpu: 'A100',
    free_tier_compatible: true,
    est_boot_sec: 45,
    highlights: ['Zero-Code Visual Fine-Tuning', '2x Faster · 70% Less VRAM', '1-Click GGUF Export'],
    highlights_zh: ['拖文档零代码可视化微调', '提速 2 倍 · 显存省 70%', '训完一键导出 GGUF'],
    supports_port_bridge: true,
    model_options: [
      {
        id: 'default',
        name: 'Unsloth Studio Latest (Visual Fine-Tuning + GGUF Export)',
        name_zh: 'Unsloth Studio 最新官方版（零代码微调 + 一键导 GGUF）',
        summary: 'Installs official unsloth package and boots Unsloth Studio on 127.0.0.1:8890.',
        summary_zh: '自动安装官方 unsloth 并在本机 127.0.0.1:8890 拉起可视化微调工作台。',
        min_vram_gb: 12,
        recommended_gpu: 'A100',
        download_url: '',
      },
    ],
    plugin_integration_tip_en:
      'Open http://127.0.0.1:8890 in your browser to upload documents and export fine-tuned GGUF weights.',
    plugin_integration_tip_zh:
      '启动后在浏览器打开 127.0.0.1:8890，拖入文档即可零代码训练专属模型并导出 GGUF。',
  },
  {
    id: 'jupyterlab',
    name: 'JupyterLab Cloud Lab',
    name_zh: 'JupyterLab 云端实验室',
    tagline: 'Full PyTorch + CUDA notebook IDE with per-instance token auth & checkpoint sync',
    tagline_zh: '开箱即用的 PyTorch + CUDA 交互式实验室，独立 Token 鉴权 + 权重自动防丢',
    description:
      'Instant JupyterLab environment bound privately to 127.0.0.1 with per-instance random token authentication, XSRF protection, PyTorch, CUDA, and automatic checkpoint backup.',
    description_zh:
      '适合科研实验、自定义脚本与数据分析：自动启用每实例随机 Token 与 XSRF 保护，支持 VS Code 挂载与掉线自动换号续跑。',
    category: 'workspace',
    icon: '📓',
    default_remote_port: 8888,
    default_local_port: 8889,
    min_vram_gb: 0,
    recommended_gpu: 'T4',
    free_tier_compatible: true,
    est_boot_sec: 20,
    highlights: ['PyTorch + CUDA Ready', 'Per-Instance Token + XSRF', 'Auto Checkpoint Sync'],
    highlights_zh: ['PyTorch + CUDA 免配置', '独立随机 Token 与 XSRF 保护', '权重自动回传本机'],
    supports_port_bridge: true,
    model_options: [
      {
        id: 'default',
        name: 'PyTorch + CUDA Base Environment (0 GB extra)',
        name_zh: 'PyTorch + CUDA 预装环境（0 GB 额外下载）',
        summary: 'Standard Colab PyTorch / CUDA runtime with zero extra model downloads.',
        summary_zh: '使用云端预装 PyTorch + CUDA 与 uv 环境，无需额外下载模型，秒级进入实验室。',
        min_vram_gb: 0,
        recommended_gpu: 'T4',
        download_url: '',
      },
    ],
    plugin_integration_tip_en:
      'Attach VS Code Jupyter extension to http://127.0.0.1:8889 using /content/.colabhive_jupyter_token.',
    plugin_integration_tip_zh:
      '支持 VS Code Jupyter 插件直连 127.0.0.1:8889（使用 /content/.colabhive_jupyter_token 随机安全令牌）。',
  },
];

interface DemoV11State {
  llmStatus: LLMGatewayStatus;
  appInstances: AppInstance[];
  checkpointPolicy: CheckpointPolicy;
  checkpoints: CheckpointRecord[];
}

const nowIso = () => new Date().toISOString();
const agoIso = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString();
function isZhDemoLocale(): boolean {
  try {
    const loc = localStorage.getItem('ch_locale') || '';
    if (loc) return loc.toLowerCase().startsWith('zh');
  } catch {
    // ignore storage errors
  }
  return typeof navigator !== 'undefined' && navigator.language.toLowerCase().startsWith('zh');
}

const demoState: DemoV11State = {
  llmStatus: {
    running: true,
    state: 'ready',
    status_step: 'Uncensored GGUF loaded in cloud GPU VRAM · /v1 endpoint ready',
    status_step_zh: '无审查模型已加载至云端显卡 · 本机 /v1 接口随时可连',
    active_model_id: 'qwen3.8-27b-abliterated',
    active_model_name: 'Qwen3.8 27B Abliterated · 450万下载社区口碑王',
    engine: 'ollama',
    strategy: 'waste-zero',
    session_id: 'sess-colab-l4-01',
    account_id: 'acc_proplus_02',
    account_label: 'ProPlus-Alex (Colab Pro+ L4)',
    provider: 'colab',
    assigned_gpu: 'L4',
    free_tier_active: false,
    auto_failover: true,
    failover_count: 1,
    last_failover_note: '原账号额度耗尽 → 已在 1.8 秒内自动无感切至备用号，/v1 接口未断连',
    openai_base_url: 'http://127.0.0.1:7860/v1',
    anthropic_base_url: 'http://127.0.0.1:7860',
    ollama_base_url: 'http://127.0.0.1:7860',
    idle_auto_stop_min: 30,
    requests_served: 38,
    tokens_generated: 26480,
    avg_tokens_per_sec: 49.4,
    started_at: agoIso(42),
    last_active_at: agoIso(1),
  },
  appInstances: [
    {
      id: 'app-wangp-demo-01',
      template_id: 'wangp',
      name: 'WanGP Video & Image Super-Studio',
      name_zh: 'WanGP 开源视频与生图全家桶',
      category: 'video',
      state: 'running',
      status_step: 'Local private port 127.0.0.1:7861 bridged to 96GB G4 GPU',
      status_step_zh: '本机私有直连 127.0.0.1:7861 已就绪 · MiniMax H3 / LTX-2.5 随时排队出片',
      progress_pct: 100,
      session_id: 'sess-colab-g4-02',
      account_id: 'acc_ultra_01',
      account_label: 'Lab Ultra #1 (96GB G4)',
      provider: 'colab',
      assigned_gpu: 'G4',
      free_tier_active: false,
      remote_port: 7861,
      local_port: 7861,
      local_bridge_active: true,
      local_url: 'http://127.0.0.1:7861',
      web_proxy_url: '/apps/proxy/app-wangp-demo-01/',
      idle_auto_stop_min: 45,
      auto_resume_enabled: true,
      failover_count: 1,
      last_failover_note: '已从进度快照恢复并切换至 Lab Ultra #1 (82%)',
      selected_model_id: 'minimax-h3',
      selected_model_name: 'MiniMax H3 音画同步 + H3-Image 方案（推荐 96GB G4 / H100）',
      checkpoint_dir: '/content/WanGP/outputs',
      started_at: agoIso(28),
      last_active_at: agoIso(2),
    },
  ],
  checkpointPolicy: {
    auto_resume_enabled: true,
    sync_interval_sec: 180,
    watch_paths: ['/content/WanGP/outputs', '/content/ComfyUI/output', '/content/unsloth_outputs'],
    max_snapshots_per_job: 5,
    updated_at: agoIso(15),
  },
  checkpoints: [
    {
      id: 'ckpt-relay-01',
      job_id: 'app-wangp-demo-01',
      job_title: 'WanGP 视频与生图全家桶 · MiniMax H3 批量出片',
      job_kind: 'app',
      source_session_id: 'sess-colab-free-01',
      source_account_label: '账号 #1 (Colab Pro+)',
      resumed_session_id: 'sess-colab-g4-02',
      resumed_account_label: 'Lab Ultra #1 (96GB G4)',
      assigned_gpu: 'G4',
      remote_path: '/content/WanGP/outputs',
      local_snapshot_dir: '~/.colabhive/checkpoints/app-wangp-demo-01',
      file_count: 21,
      total_bytes: 186 * 1024 * 1024,
      progress_pct: 82,
      relay_count: 1,
      status: 'resumed',
      last_relay_note_en:
        'Account #1 quota ended -> Restored snapshot on Lab Ultra #1 from 82%',
      last_relay_note_zh: '已从快照恢复并切换至 Lab Ultra #1 (82%)',
      created_at: agoIso(54),
      updated_at: agoIso(19),
    },
    {
      id: 'ckpt-sync-02',
      job_id: 'exp-lora-portrait',
      job_title: '📓 JupyterLab 云端实验室 · LoRA 微调训练 (Step 940/1000)',
      job_kind: 'experiment',
      source_session_id: 'sess-colab-free-02',
      source_account_label: 'Free-T4-Standby (Colab T4)',
      assigned_gpu: 'T4',
      remote_path: '/content/checkpoints',
      local_snapshot_dir: '~/.colabhive/checkpoints/exp-lora-portrait',
      file_count: 6,
      total_bytes: 328 * 1024 * 1024,
      progress_pct: 94,
      relay_count: 0,
      status: 'synced',
      last_relay_note_en: 'Latest progress snapshot safely backed up to your computer (94%)',
      last_relay_note_zh: '最新进度 (94%) 已自动备份回你的电脑，随时掉线都能一键换号接着跑',
      created_at: agoIso(12),
      updated_at: agoIso(3),
    },
  ],
};

function buildDemoSavingsReceipt(): SavingsReceiptStats {
  return {
    generated_at: nowIso(),
    pool_title: 'ColabHive 私人聚合算力池',
    total_accounts: 4,
    providers_connected: ['Google Colab'],
    total_gpu_hours: 186.5,
    free_gpu_hours_harvested: 92.4,
    g4_equivalent_hours: 64.8,
    total_cu_burned: 192.4,
    saved_idle_waste_cu: 84.6,
    waste_zero_efficiency_pct: 98.4,
    auto_resume_relays: 11,
    auto_teardowns: 29,
    zombie_aborts: 4,
    estimated_saved_usd: 268.5,
    estimated_saved_cny: 1933,
    top_gpus_used: ['RTX PRO 6000 G4 (96GB)', 'NVIDIA L4 (24GB)', 'Tesla T4 (16GB)'],
    rank_badge_en: '🏆 Free Compute Grandmaster',
    rank_badge_zh: '🏆 算力白嫖宗师',
    share_copy_en:
      'Pooled 4 Google Colab accounts in ColabHive: 186.5h total cloud GPU runtime (92.4h free tier, ≈ $269 / ¥1,933 at Vast.ai/RunPod avg rates), 98.4% zero-waste efficiency, and 11 auto-resume relays! -> https://github.com/hexwander/colabhive',
    share_copy_zh:
      '用 ColabHive 把多个 Google Colab 账号聚合成私人云算力池！累计运行 186.5 小时云端显卡（含 92.4 小时免费卡），等效云端租卡价值约 ¥1,933（$269，按 Vast.ai/RunPod 均价），11 次掉线自动换号接着跑 👉 https://github.com/hexwander/colabhive',
  };
}

// ============================================================================
// HTTP Helper — Strict Demo-Only Fixtures (Zero Silent Fallbacks in Live Mode)
// ============================================================================

const demoInstalledPlugins: AppTemplate[] = [];
const VALID_PLUGIN_CATEGORIES: ReadonlySet<string> = new Set([
  'video',
  'image',
  'audio',
  'workspace',
  'custom',
]);
const SAFE_PLUGIN_ID_RE = /^[a-z0-9][a-z0-9-]{1,48}$/;

function unwrapArray<T>(raw: unknown, keys: string[]): T[] {
  if (Array.isArray(raw)) return raw as T[];
  if (raw && typeof raw === 'object') {
    for (const k of keys) {
      const candidate = (raw as Record<string, unknown>)[k];
      if (Array.isArray(candidate)) return candidate as T[];
    }
  }
  return [];
}

function extractApiErrorMessage(body: unknown, fallback: string): string {
  if (body && typeof body === 'object') {
    const rec = body as Record<string, unknown>;
    if (rec.error && typeof rec.error === 'object' && 'message' in rec.error) {
      const nested = String((rec.error as { message?: unknown }).message ?? '').trim();
      if (nested) return nested;
    }
    if (typeof rec.error === 'string' && rec.error.trim()) {
      return rec.error.trim();
    }
    if (typeof rec.message === 'string' && rec.message.trim()) {
      return rec.message.trim();
    }
  }
  if (typeof body === 'string' && body.trim()) {
    return body.trim();
  }
  return fallback;
}

const DEFAULT_REQUEST_TIMEOUT_MS = 20_000;
/** 25-minute deadline covering VM allocation + runtime install + 900s model pulls/weight downloads. */
export const LONG_OPERATION_TIMEOUT_MS = 1_500_000;

async function v11Request<T>(
  path: string,
  init: RequestInit = {},
  demoFixtureFn: () => T | Promise<T>,
  timeoutMs: number = DEFAULT_REQUEST_TIMEOUT_MS,
): Promise<T> {
  // Demo fixtures MUST ONLY execute when `isDemoSiteMode() === true`.
  if (isDemoSiteMode()) {
    return demoFixtureFn();
  }

  const headers = new Headers(init.headers);
  if (!headers.has('Accept')) {
    headers.set('Accept', 'application/json');
  }
  if (init.body && !(init.body instanceof FormData) && !headers.has('Content-Type')) {
    headers.set('Content-Type', 'application/json');
  }
  const token = getStoredAdminToken();
  if (token && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${token}`);
  }

  const callerSignal = init.signal;
  let controller: AbortController | null = null;
  let timeoutId: ReturnType<typeof setTimeout> | null = null;
  let timedOut = false;
  let signal: AbortSignal | undefined = callerSignal ?? undefined;
  const onCallerAbort = () => {
    controller?.abort(callerSignal?.reason);
  };

  if (typeof AbortController !== 'undefined') {
    controller = new AbortController();
    signal = controller.signal;
    if (callerSignal) {
      if (callerSignal.aborted) {
        controller.abort(callerSignal.reason);
      } else {
        callerSignal.addEventListener('abort', onCallerAbort, { once: true });
      }
    }
    if (timeoutMs > 0 && Number.isFinite(timeoutMs)) {
      timeoutId = setTimeout(() => {
        timedOut = true;
        controller?.abort();
      }, timeoutMs);
    }
  }

  try {
    const res = await fetch(path, { ...init, headers, signal });
    if (res.status === 204) {
      return undefined as T;
    }

    const contentType = res.headers.get('content-type') || '';
    const isJson = contentType.includes('application/json');
    const body = isJson ? await res.json().catch(() => null) : await res.text().catch(() => '');

    if (!res.ok) {
      if (res.status === 401 && typeof window !== 'undefined') {
        window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT, { detail: { path } }));
      }
      const msg = extractApiErrorMessage(body, `HTTP ${res.status} ${res.statusText}`);
      throw new APIError(msg, res.status, body);
    }

    return body as T;
  } catch (err) {
    if (timedOut && !callerSignal?.aborted) {
      throw new APIError('请求超时，请检查 ColabHive 服务状态后重试 (Request timed out)', 504);
    }
    throw err;
  } finally {
    if (callerSignal) {
      callerSignal.removeEventListener('abort', onCallerAbort);
    }
    if (timeoutId !== null) {
      clearTimeout(timeoutId);
    }
  }
}

// ============================================================================
// Realistic Simulated Token-by-Token Streaming Generator (Demo / Offline)
// ============================================================================

const demoCustomModels: LLMModelSpec[] = [];

/** Synthesizes a clean `LLMModelSpec` for arbitrary `hf.co/...` or custom Ollama tags. */
export function resolveLLMModelSpec(modelId: string, extraCatalog?: LLMModelSpec[]): LLMModelSpec {
  const parsed = normalizeCustomModelInput(modelId);
  const cleanId = parsed.ok ? parsed.modelId : modelId.trim();

  const found =
    extraCatalog?.find((m) => m.id === cleanId) ??
    demoCustomModels.find((m) => m.id === cleanId) ??
    DEMO_LLM_CATALOG.find((m) => m.id === cleanId);
  if (found) return found;

  const lower = cleanId.toLowerCase();
  const isHuge = /(?:70b|72b|110b|123b|405b|671b)/i.test(lower);
  const isMedium = !isHuge && /(?:24b|27b|30b|32b|34b)/i.test(lower);
  const paramMatch = cleanId.match(/(\d+(?:\.\d+)?[bB])/);
  const paramSize = paramMatch ? paramMatch[1].toUpperCase() : isHuge ? '70B' : isMedium ? '32B' : 'GGUF';

  const recommendedGpu: AcceleratorType = isHuge ? 'G4' : isMedium ? 'L4' : 'T4';
  const freeTierReady = !isHuge && !isMedium;
  const minVramGb = isHuge ? 48 : isMedium ? 22 : 10;

  const isHf = lower.startsWith('hf.co/');
  const shortRepo = isHf ? cleanId.slice('hf.co/'.length) : cleanId;
  const quantMatch = shortRepo.split(':')[1] || 'Q4_K_M';

  return {
    id: cleanId,
    display_name: isHf ? `HF · ${shortRepo}` : `Custom · ${cleanId}`,
    display_name_zh: isHf ? `HF 社区微调 · ${shortRepo}` : `自定义模型 · ${cleanId}`,
    provider_family: isHf ? 'HuggingFace' : 'Custom',
    summary: isHf
      ? `Pulled directly from Hugging Face (${shortRepo}) into your private cloud GPU.`
      : `Custom Ollama model tag (${cleanId}) running privately on your GPU pool.`,
    summary_zh: isHf
      ? `从 Hugging Face 直拉加载（${shortRepo}），支持社区微调版、角色扮演与无审查开源权重。`
      : `自定义加载的开源模型（${cleanId}），在你的私有云端显卡独享运行。`,
    parameter_size: paramSize,
    quantization: quantMatch,
    context_length: 32768,
    min_vram_gb: minVramGb,
    recommended_gpu: recommendedGpu,
    free_tier_ready: freeTierReady,
    default_engine: 'ollama',
    ollama_tag: cleanId,
    huggingface_repo: isHf ? shortRepo.split(':')[0] : undefined,
    tags: isHf ? ['HuggingFace直拉', freeTierReady ? '免费卡可跑' : '大显存满血'] : ['自定义模型'],
  };
}

function registerDemoCustomModel(spec: LLMModelSpec): void {
  if (DEMO_LLM_CATALOG.some((m) => m.id === spec.id)) return;
  const existingIdx = demoCustomModels.findIndex((m) => m.id === spec.id);
  if (existingIdx >= 0) {
    demoCustomModels[existingIdx] = spec;
  } else {
    demoCustomModels.unshift(spec);
  }
}

function craftSimulatedReply(messages: ChatMessage[], modelId: string): string {
  const lastUser = [...messages].reverse().find((m) => m.role === 'user')?.content?.trim() ?? '';
  const systemPrompt = messages.find((m) => m.role === 'system')?.content ?? '';
  const isChinese = /[\u4e00-\u9fa5]/.test(lastUser + systemPrompt) || lastUser.length === 0;
  const spec = resolveLLMModelSpec(modelId);
  const isReasoning =
    modelId.toLowerCase().includes('deepseek-r1') ||
    modelId.toLowerCase().includes('qwq') ||
    modelId.toLowerCase().includes('r1');

  const lower = (lastUser + ' ' + systemPrompt).toLowerCase();

  if (isChinese) {
    // 1. Translation intent
    if (lower.includes('翻译') || lower.includes('translate') || systemPrompt.includes('翻译')) {
      const prefix = isReasoning
        ? `<think>\n正在识别原文语境、专业术语与句式节奏，采用“信、达、雅”标准输出地道双语对照译文……\n</think>\n\n`
        : '';
      return (
        prefix +
        `### 🌐 沉浸式双语精译结果\n\n` +
        `> **核心译文（自然母语级表达）：**\n` +
        `> 真正好用的算力工具，不该让普通人去折腾复杂的命令行和公网隧道。ColabHive 把散落在多个平台的免费显卡汇聚成一池，点一下就能开口聊天、生成超清画作与视频；就算单张免费卡到点下线，也会在后台自动换号接着跑。\n\n` +
        `**重点词汇与润色亮点：**\n` +
        `- **无感切换 (` +
        '`Seamless Failover`' +
        `)**：译为“后台自动无感切换”，避免生硬机翻感\n` +
        `- **本机私有直连 (` +
        '`127.0.0.1 Loopback`' +
        `)**：保留关键技术准确性，同时用大白话点明“免公网穿透、安全防封”\n\n` +
        `如果你有整篇英文论文或网页段落，直接粘贴进来，我可以按段落逐句对照翻译！`
      );
    }

    // 2. Coding / Debugging intent
    if (
      lower.includes('代码') ||
      lower.includes('python') ||
      lower.includes('报错') ||
      lower.includes('bug') ||
      lower.includes('写一个') ||
      lower.includes('函数') ||
      systemPrompt.includes('代码')
    ) {
      const prefix = isReasoning
        ? `<think>\n分析需求边界与异常处理：需要兼顾可读性、零额外依赖与超时重试保护，确保复制过去即可直接运行。\n</think>\n\n`
        : '';
      return (
        prefix +
        `没问题！下面是一份结构清晰、自带超时保护与流式输出的直接可用代码，直接接入本机 \`http://127.0.0.1:7860/v1\` 即可白嫖云端显卡推理：\n\n` +
        '```python\n' +
        'from openai import OpenAI\n\n' +
        '# 直接连接本机 ColabHive 私有网关，零公网穿透、零 API 账单\n' +
        'client = OpenAI(\n' +
        '    base_url="http://127.0.0.1:7860/v1",\n' +
        '    api_key="colabhive-local",\n' +
        ')\n\n' +
        'stream = client.chat.completions.create(\n' +
        `    model="${spec.id}",\n` +
        '    messages=[\n' +
        '        {"role": "system", "content": "你是一位严谨高效的资深工程师。"},\n' +
        `        {"role": "user", "content": ${JSON.stringify(lastUser.slice(0, 48) || '请帮我优化这段代码')}},\n` +
        '    ],\n' +
        '    stream=True,\n' +
        ')\n\n' +
        'for chunk in stream:\n' +
        '    delta = chunk.choices[0].delta.content or ""\n' +
        '    print(delta, end="", flush=True)\n' +
        '```\n\n' +
        `### 💡 关键排错与提速建议\n` +
        `1. **零配置复用**：任何支持 OpenAI 接口的工具（Cursor、Cherry Studio、Python 脚本）只需把地址改成 \`http://127.0.0.1:7860/v1\` 即可。\n` +
        `2. **自动续连保护**：即使当前免费显卡时长耗尽，ColabHive 也会自动分配池内下一张空闲卡继续响应，脚本端无需重写重连逻辑。`
      );
    }

    // 3. Copywriting / Polishing intent
    if (
      lower.includes('文案') ||
      lower.includes('小红书') ||
      lower.includes('润色') ||
      lower.includes('周报') ||
      lower.includes('标题') ||
      systemPrompt.includes('文案')
    ) {
      const prefix = isReasoning
        ? `<think>\n拆解读者痛点（租显卡贵、配环境难、跑到90%掉线崩溃），用高共鸣口语化短句打造高互动率文案……\n</think>\n\n`
        : '';
      return (
        prefix +
        `为你准备了 **2 版可直接发布的爆款成稿**，兼顾真实感与收藏率：\n\n` +
        `### 🔥 版本一：干货种草风（适合小红书 / 即刻 / X）\n` +
        `**标题：别再花冤枉钱租显卡了！把闲置免费号攒成私人 AI 工作站太香了 😭**\n\n` +
        `以前想跑满血大模型、用 ComfyUI 出超清图或者做 AI 视频，不是自己电脑显存爆红，就是按小时租卡心疼钱包……\n\n` +
        `最近把几个 Google Colab 账号放进 **ColabHive** 组成私人算力池：\n` +
        `- 💬 **打开就能免费聊大模型**：连 Cherry Studio、沉浸式翻译都能直接填 \`127.0.0.1:7860/v1\` 白嫖\n` +
        `- 🎨 **像手机应用商店一样点一下启动**：AI 视频、Flux 超清画图、语音克隆一键秒开，还能直连 Krita / PS 插件\n` +
        `- 🛡️ **掉线自动换号接着跑**：1 号卡免费时长用完，自动把进度搬到 2 号卡接着跑，再也没白跑过一次！\n\n` +
        `---\n` +
        `需要我针对你的具体产品或个人语气再调整得更犀利/更专业一点吗？`
      );
    }

    // 4. Default helpful assistant reply echoing user's topic naturally
    const prefix = isReasoning
      ? `<think>\n用户提问：“${lastUser.slice(0, 60) || '你好'}”\n当前运行于 ${spec.display_name_zh}（${demoState.llmStatus.account_label ?? '账号 #1 免费显卡'}），组织结构清晰、通俗易懂的回答。\n</think>\n\n`
      : '';
    return (
      prefix +
      `你好！当前正在通过 **${spec.display_name_zh}** 为你实时解答（运行在池内云端显卡，完全不占你本机内存与显卡）：\n\n` +
      `针对你提到的 **「${lastUser.slice(0, 50) || '高效使用私人免费算力池'}」**，给你整理了最实用的 3 个要点：\n\n` +
      `1. **零门槛直接用**：无论是长文档总结、中英互译、写代码还是改方案，直接在这里多轮追问即可，上下文全部保存在你本机浏览器。\n` +
      `2. **免费额度最大化**：建议开启账号池里的 **「🎁 优先烧免费额度」**，系统会先把所有 Colab 免费时段用满，不花一分钱。\n` +
      `3. **常用软件一键白嫖**：展开下方的 **「🔌 接入常用软件」** 面板，复制 \`http://127.0.0.1:7860/v1\` 就能让沉浸式翻译、Cherry Studio、Cursor 直接共享这台云端大模型！\n\n` +
      `你可以继续发给我具体的文本、代码或问题，我们直接开始 👇`
    );
  }

  // English response
  const enPrefix = isReasoning
    ? `<think>\nAnalyzing "${lastUser.slice(0, 60) || 'Hello'}" on ${spec.display_name} (${demoState.llmStatus.assigned_gpu ?? 'T4'} cloud GPU)...\n</think>\n\n`
    : '';
  return (
    enPrefix +
    `Running live on **${spec.display_name}** via your private ColabHive GPU pool (` +
    `\`127.0.0.1:7860\`, zero third-party middlemen):\n\n` +
    `Here is a clear breakdown for **"${lastUser.slice(0, 56) || 'getting the most out of pooled free GPUs'}"**:\n\n` +
    `- **Zero-Cost Priority**: Free Colab T4 windows are automatically scheduled first before touching any paid credits.\n` +
    `- **Seamless Cross-Account Relay**: If Account #1 hits its free session limit mid-conversation, ColabHive automatically spins up Account #2 so your chat never drops.\n` +
    `- **Plug Into Your Favorite Apps**: Copy \`http://127.0.0.1:7860/v1\` below into **Cherry Studio**, **Immersive Translate**, **Cursor**, or **Claude Code** in 10 seconds.\n\n` +
    '```bash\n' +
    'curl http://127.0.0.1:7860/v1/chat/completions \\\n' +
    '  -H "Content-Type: application/json" \\\n' +
    `  -d '{"model":"${spec.id}","messages":[{"role":"user","content":"Hello!"}]}'\n` +
    '```\n\n' +
    `What would you like to build, translate, or debug next?`
  );
}

function tokenizeForStream(text: string): string[] {
  const chunks: string[] = [];
  let i = 0;
  while (i < text.length) {
    const step = text[i] === '\n' ? 1 : 2 + ((i * 7) % 3);
    chunks.push(text.slice(i, i + step));
    i += step;
  }
  return chunks;
}

async function sleepAbortable(ms: number, signal?: AbortSignal): Promise<void> {
  if (signal?.aborted) {
    throw new DOMException('Aborted', 'AbortError');
  }
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      reject(new DOMException('Aborted', 'AbortError'));
    };
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

async function simulateStreamingChat(
  messages: ChatMessage[],
  modelId: string,
  onDelta: (delta: string, meta?: ChatStreamMeta) => void,
  signal?: AbortSignal,
): Promise<ChatCompletionSummary> {
  const spec = modelId
    ? resolveLLMModelSpec(modelId)
    : resolveLLMModelSpec(demoState.llmStatus.active_model_id || DEMO_LLM_CATALOG[0].id);
  registerDemoCustomModel(spec);

  if (!demoState.llmStatus.running || demoState.llmStatus.active_model_id !== spec.id) {
    demoState.llmStatus = {
      ...demoState.llmStatus,
      running: true,
      state: 'ready',
      active_model_id: spec.id,
      active_model_name: spec.display_name_zh,
      assigned_gpu: spec.recommended_gpu,
      free_tier_active: spec.free_tier_ready,
      account_label: spec.free_tier_ready
        ? 'Free-T4-Standby (Colab 免费卡 T4)'
        : `AI-Ultra-01 (Colab ${spec.recommended_gpu})`,
      provider: 'colab',
      started_at: nowIso(),
    };
    notifyV11StateChanged();
  }

  const fullText = craftSimulatedReply(messages, spec.id);
  const pieces = tokenizeForStream(fullText);
  const startedAt = performance.now();
  let acc = '';
  const baseSpeed = spec.free_tier_ready ? 51.8 : 38.4;

  await sleepAbortable(140, signal);

  for (let idx = 0; idx < pieces.length; idx += 1) {
    await sleepAbortable(18, signal);
    const piece = pieces[idx];
    acc += piece;
    const elapsedSec = Math.max(0.08, (performance.now() - startedAt) / 1000);
    const liveSpeed = Number((baseSpeed + Math.sin(idx * 0.4) * 3.2).toFixed(1));
    onDelta(piece, {
      modelId: spec.id,
      tokensPerSec: elapsedSec > 0.25 ? liveSpeed : baseSpeed,
      accountLabel: demoState.llmStatus.account_label,
      assignedGpu: demoState.llmStatus.assigned_gpu,
      freeTierActive: demoState.llmStatus.free_tier_active,
      failoverNote: demoState.llmStatus.last_failover_note,
    });
  }

  const durationMs = Math.max(120, Math.round(performance.now() - startedAt));
  const tokensGenerated = pieces.length;
  const finalTps = Number(baseSpeed.toFixed(1));

  demoState.llmStatus = {
    ...demoState.llmStatus,
    requests_served: demoState.llmStatus.requests_served + 1,
    tokens_generated: demoState.llmStatus.tokens_generated + tokensGenerated,
    avg_tokens_per_sec: finalTps,
    last_active_at: nowIso(),
  };
  notifyV11StateChanged();

  return {
    content: acc,
    modelId: spec.id,
    tokensGenerated,
    tokensPerSec: finalTps,
    durationMs,
    status: { ...demoState.llmStatus },
  };
}

// ============================================================================
// Exported `v11Api` Client
// ============================================================================

export const v11Api = {
  // --------------------------------------------------------------------------
  // Pillar 1: Private AI Chat & Local `/v1` + Ollama Gateway
  // --------------------------------------------------------------------------
  getLLMCatalog: async (): Promise<LLMModelSpec[]> => {
    const demoCatalog = () => [...demoCustomModels, ...DEMO_LLM_CATALOG];
    const raw = await v11Request<unknown>('/api/v1/llm/catalog', {}, demoCatalog);
    const list = unwrapArray<LLMModelSpec>(raw, ['models', 'catalog', 'items', 'data']);
    if (isDemoSiteMode()) {
      const seen = new Set(list.map((m) => m.id));
      const extras = demoCustomModels.filter((m) => !seen.has(m.id));
      return [...extras, ...list];
    }
    return list;
  },

  getLLMStatus: async (): Promise<LLMGatewayStatus> => {
    const status = await v11Request<LLMGatewayStatus>(
      '/api/v1/llm/status',
      {},
      () => {
        const zh = isZhDemoLocale();
        const spec = DEMO_LLM_CATALOG.find((m) => m.id === demoState.llmStatus.active_model_id);
        return {
          ...demoState.llmStatus,
          active_model_name: spec
            ? zh
              ? spec.display_name_zh
              : spec.display_name
            : demoState.llmStatus.active_model_name,
          last_failover_note: zh
            ? demoState.llmStatus.last_failover_note
            : 'Source quota depleted -> Seamless failover to backup account in 1.8s with zero /v1 disconnect',
        };
      },
    );
    return {
      ...status,
      account_label: status.account_label
        ? sanitizeAccountLabel(status.account_label)
        : undefined,
      openai_base_url: status.openai_base_url || '',
      anthropic_base_url: status.anthropic_base_url || '',
      ollama_base_url: status.ollama_base_url || '',
    };
  },

  startLLM: async (req: LLMStartRequest): Promise<LLMGatewayStatus> => {
    const parsed = normalizeCustomModelInput(req.model_id);
    if (!parsed.ok) {
      throw new APIError(parsed.errorZh, 400);
    }
    const normalizedReq: LLMStartRequest = { ...req, model_id: parsed.modelId };

    return v11Request<LLMGatewayStatus>(
      '/api/v1/llm/start',
      {
        method: 'POST',
        body: JSON.stringify(normalizedReq),
      },
      async () => {
        const spec = resolveLLMModelSpec(parsed.modelId);
        registerDemoCustomModel(spec);
        const gpu = normalizedReq.preferred_gpu ?? spec.recommended_gpu;
        const isFree = spec.free_tier_ready && (gpu === 'T4' || gpu === 'CPU');
        const chosenAccountId = normalizedReq.account_id?.trim() || (isFree ? 'acc_free_04' : 'acc_ultra_01');
        const chosenAccountLabel = normalizedReq.account_id?.trim()
          ? `指定 Colab 账号 (${normalizedReq.account_id.trim()})`
          : isFree
            ? 'Free-T4-Standby (Colab 免费卡 T4)'
            : `AI-Ultra-01 (Colab ${gpu})`;
        demoState.llmStatus = {
          ...demoState.llmStatus,
          running: true,
          state: 'ready',
          status_step: `Running ${spec.display_name} on ${gpu}`,
          status_step_zh: `已在 Colab ${gpu} 加载 ${spec.display_name_zh} · 随时可聊`,
          active_model_id: spec.id,
          active_model_name: spec.display_name_zh,
          engine: normalizedReq.engine ?? spec.default_engine,
          strategy: normalizedReq.strategy ?? (isFree ? 'free-first' : 'waste-zero'),
          session_id: isFree ? 'sess-free-colab-01' : 'sess-colab-gpu-01',
          account_id: chosenAccountId,
          account_label: chosenAccountLabel,
          provider: 'colab',
          assigned_gpu: gpu,
          free_tier_active: isFree,
          auto_failover: normalizedReq.auto_failover ?? true,
          idle_auto_stop_min:
            normalizedReq.idle_auto_stop_min ?? demoState.llmStatus.idle_auto_stop_min ?? 30,
          avg_tokens_per_sec: isFree ? 51.2 : 44.8,
          started_at: nowIso(),
          last_active_at: nowIso(),
          error_message: undefined,
        };
        notifyV11StateChanged();
        return { ...demoState.llmStatus };
      },
      LONG_OPERATION_TIMEOUT_MS,
    );
  },

  stopLLM: async (): Promise<LLMGatewayStatus> => {
    return v11Request<LLMGatewayStatus>(
      '/api/v1/llm/stop',
      { method: 'POST' },
      async () => {
        demoState.llmStatus = {
          ...demoState.llmStatus,
          running: false,
          state: 'stopped',
          status_step: 'Stopped · Burning 0 GPU quota',
          status_step_zh: '已停止释放显卡 · 当前 0 额度消耗',
        };
        notifyV11StateChanged();
        return { ...demoState.llmStatus };
      },
    );
  },

  streamChat: async (
    messages: ChatMessage[],
    modelId: string,
    onDelta: (delta: string, meta?: ChatStreamMeta) => void,
    signal?: AbortSignal,
    boundAuthToken?: string | null,
    options?: {
      think?: boolean;
      maxTokens?: number;
      onReasoningDelta?: (delta: string) => void;
    },
  ): Promise<ChatCompletionSummary> => {
    // Demo fixtures MUST ONLY execute when `isDemoSiteMode() === true`.
    if (isDemoSiteMode()) {
      return simulateStreamingChat(messages, modelId, onDelta, signal);
    }

    const headers = new Headers({
      'Content-Type': 'application/json',
      Accept: 'text/event-stream, application/json',
    });
    // Use the exact credential token bound to this conversation snapshot when supplied by the caller
    // (eliminating any TOCTOU window where shared storage changes between history build and fetch).
    const token = boundAuthToken !== undefined ? boundAuthToken : getStoredAdminToken();
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
    }

    const reqPayload: Record<string, unknown> = {
      model: modelId,
      messages,
      stream: true,
    };
    if (typeof options?.think === 'boolean') {
      reqPayload.think = options.think;
      if (!options.think) {
        reqPayload.reasoning_effort = 'none';
      }
    }
    if (typeof options?.maxTokens === 'number' && options.maxTokens > 0) {
      reqPayload.max_tokens = options.maxTokens;
    }

    const res = await fetch('/api/v1/llm/chat/completions', {
      method: 'POST',
      headers,
      body: JSON.stringify(reqPayload),
      signal,
    });

    if (!res.ok) {
      if (res.status === 401 && typeof window !== 'undefined') {
        window.dispatchEvent(
          new CustomEvent(UNAUTHORIZED_EVENT, {
            detail: { path: '/api/v1/llm/chat/completions' },
          }),
        );
      }
      const ct = res.headers.get('content-type') || '';
      const errBody = ct.includes('application/json')
        ? await res.json().catch(() => null)
        : await res.text().catch(() => '');
      const msg = extractApiErrorMessage(errBody, `HTTP ${res.status} ${res.statusText}`);
      throw new APIError(msg, res.status, errBody);
    }

    const startedAt = performance.now();
    const contentType = (res.headers.get('content-type') || '').toLowerCase();

    // Non-streaming JSON response
    if (contentType.includes('application/json') && !contentType.includes('event-stream')) {
      const json = (await res.json().catch(() => null)) as {
        model?: string;
        error?: { message?: string } | string;
        choices?: Array<{
          message?: { content?: string; reasoning_content?: string; reasoning?: string };
        }>;
        usage?: { completion_tokens?: number };
      } | null;
      if (!json) {
        throw new APIError('服务端返回了无效的 JSON 响应 (Invalid JSON response)', 502);
      }
      if (json.error) {
        throw new APIError(extractApiErrorMessage(json, 'Inference error'), 502, json);
      }
      const actualModelId =
        typeof json.model === 'string' && json.model.trim() ? json.model.trim() : modelId;
      const reasoning =
        json.choices?.[0]?.message?.reasoning_content ?? json.choices?.[0]?.message?.reasoning;
      const bodyText = json.choices?.[0]?.message?.content ?? '';
      const combined = reasoning ? `<think>\n${reasoning}\n</think>\n\n${bodyText}` : bodyText;
      const durationMs = Math.max(50, Math.round(performance.now() - startedAt));
      const estTokens =
        json.usage?.completion_tokens ?? Math.max(1, Math.round(combined.length / 2.5));
      const tps = Number(((estTokens / durationMs) * 1000).toFixed(1));
      if (options?.onReasoningDelta) {
        if (reasoning) options.onReasoningDelta(reasoning);
        if (bodyText) onDelta(bodyText, { tokensPerSec: tps, modelId: actualModelId });
      } else if (combined) {
        onDelta(combined, { tokensPerSec: tps, modelId: actualModelId });
      }
      return {
        content: combined,
        modelId: actualModelId,
        tokensGenerated: estTokens,
        tokensPerSec: tps,
        durationMs,
      };
    }

    if (!res.body) {
      throw new APIError('服务端未返回流式响应体 (Empty response stream)', 502);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    const isNdjson = contentType.includes('ndjson');

    let buffer = '';
    let fullContent = '';
    let inReasoning = false;
    let chunkCount = 0;
    let reportedCompletionTokens = 0;
    let actualModelId = modelId;
    let sawDone = false;
    let streamFinished = false;

    interface StreamChunkPayload {
      type?: string;
      model?: string;
      done?: boolean;
      error?: { message?: string; type?: string } | string;
      message?: { content?: string } | string;
      choices?: Array<{
        delta?: { content?: string; reasoning_content?: string; reasoning?: string };
        message?: { content?: string; reasoning_content?: string; reasoning?: string };
        finish_reason?: string | null;
      }>;
      delta?: { text?: string; thinking?: string };
      response?: string;
      usage?: { completion_tokens?: number };
    }

    const applyParsedPayload = (parsed: StreamChunkPayload) => {
      if (parsed.error || parsed.type === 'error') {
        const msg = extractApiErrorMessage(parsed, '云端推理流返回错误 (Stream error)');
        throw new APIError(msg, 502, parsed);
      }

      if (typeof parsed.model === 'string' && parsed.model.trim()) {
        actualModelId = parsed.model.trim();
      }

      if (parsed.usage?.completion_tokens) {
        reportedCompletionTokens = parsed.usage.completion_tokens;
      }

      if (parsed.type === 'message_stop' || parsed.done === true) {
        sawDone = true;
      }

      const msgContent =
        parsed.message && typeof parsed.message === 'object' ? parsed.message.content : undefined;
      const reasoningDelta =
        parsed.choices?.[0]?.delta?.reasoning_content ??
        parsed.choices?.[0]?.delta?.reasoning ??
        parsed.choices?.[0]?.message?.reasoning_content ??
        parsed.choices?.[0]?.message?.reasoning ??
        parsed.delta?.thinking ??
        '';
      const contentDelta =
        parsed.choices?.[0]?.delta?.content ??
        parsed.choices?.[0]?.message?.content ??
        parsed.delta?.text ??
        msgContent ??
        parsed.response ??
        '';

      if (options?.onReasoningDelta) {
        if (reasoningDelta) {
          chunkCount += 1;
          fullContent += reasoningDelta;
          options.onReasoningDelta(reasoningDelta);
        }
        if (contentDelta) {
          chunkCount += 1;
          fullContent += contentDelta;
          const elapsedSec = Math.max(0.05, (performance.now() - startedAt) / 1000);
          const tps = Number((chunkCount / elapsedSec).toFixed(1));
          onDelta(contentDelta, { tokensPerSec: tps, modelId: actualModelId });
        }
        return;
      }

      let emitPiece = '';
      if (reasoningDelta) {
        if (!inReasoning) {
          emitPiece += '<think>\n';
          inReasoning = true;
        }
        emitPiece += reasoningDelta;
      }
      if (contentDelta) {
        if (inReasoning) {
          emitPiece += '\n</think>\n\n';
          inReasoning = false;
        }
        emitPiece += contentDelta;
      }

      if (emitPiece) {
        chunkCount += 1;
        fullContent += emitPiece;
        const elapsedSec = Math.max(0.05, (performance.now() - startedAt) / 1000);
        const tps = Number((chunkCount / elapsedSec).toFixed(1));
        onDelta(emitPiece, { tokensPerSec: tps, modelId: actualModelId });
      }
    };

    let lineBuf = '';
    let pendingCR = false;
    let sseEventName = 'message';
    let sseDataLines: string[] = [];
    let sseHasFields = false;

    const dispatchSSEEvent = (): boolean => {
      const eventName = sseEventName;
      const dataLines = sseDataLines;
      sseEventName = 'message';
      sseDataLines = [];
      sseHasFields = false;

      if (dataLines.length === 0) {
        if (eventName === 'error') {
          throw new APIError('云端推理流中断 (SSE error event)', 502);
        }
        return false;
      }

      const payloadStr = dataLines.join('\n').trim();
      if (!payloadStr) return false;

      if (eventName === 'error') {
        let parsedErr: unknown = payloadStr;
        try {
          parsedErr = JSON.parse(payloadStr);
        } catch {
          // Use raw text error payload
        }
        throw new APIError(
          extractApiErrorMessage(parsedErr, '云端推理流返回错误 (SSE error)'),
          502,
          parsedErr,
        );
      }

      if (payloadStr === '[DONE]') {
        sawDone = true;
        return true;
      }

      let parsed: StreamChunkPayload;
      try {
        parsed = JSON.parse(payloadStr) as StreamChunkPayload;
      } catch {
        throw new APIError(
          `流式响应 JSON 解析失败 (Malformed SSE frame): ${payloadStr.slice(0, 120)}`,
          502,
        );
      }
      applyParsedPayload(parsed);
      return sawDone;
    };

    const processSSELine = (line: string): boolean => {
      if (line === '') {
        // WHATWG SSE spec: an event block is ONLY dispatched upon an empty line terminator.
        if (sseHasFields) {
          return dispatchSSEEvent();
        }
        return false;
      }
      if (line.startsWith(':')) {
        return false;
      }
      sseHasFields = true;
      const colonIdx = line.indexOf(':');
      const field = colonIdx >= 0 ? line.slice(0, colonIdx) : line;
      let value = colonIdx >= 0 ? line.slice(colonIdx + 1) : '';
      if (value.startsWith(' ')) {
        value = value.slice(1);
      }
      if (field === 'event') {
        sseEventName = value.trim() || 'message';
      } else if (field === 'data') {
        sseDataLines.push(value);
      }
      return false;
    };

    const feedSSEChunk = (text: string): boolean => {
      // Exact WHATWG line-ending handling: supports LF (\n), CRLF (\r\n), and CR-only (\r)
      // across arbitrary TCP/chunk splits. Blank lines (\n\n, \r\n\r\n, \r\r) dispatch events.
      // Unterminated trailing frames at EOF without a blank-line terminator are NEVER dispatched.
      for (let i = 0; i < text.length; i += 1) {
        const ch = text[i];
        if (pendingCR) {
          pendingCR = false;
          if (ch === '\n') {
            // Second char of a CRLF sequence already handled on '\r'.
            continue;
          }
        }
        if (ch === '\r') {
          pendingCR = true;
          const line = lineBuf;
          lineBuf = '';
          if (processSSELine(line)) return true;
        } else if (ch === '\n') {
          const line = lineBuf;
          lineBuf = '';
          if (processSSELine(line)) return true;
        } else {
          lineBuf += ch;
        }
      }
      return false;
    };

    const processNdjsonLine = (rawLine: string): boolean => {
      const trimmed = rawLine.trim();
      if (!trimmed) return false;
      let parsed: StreamChunkPayload;
      try {
        parsed = JSON.parse(trimmed) as StreamChunkPayload;
      } catch {
        throw new APIError(`NDJSON 流解析失败: ${trimmed.slice(0, 120)}`, 502);
      }
      applyParsedPayload(parsed);
      return sawDone;
    };

    try {
      while (!sawDone) {
        const { value, done } = await reader.read();
        if (done) {
          streamFinished = true;
          const tail = decoder.decode();
          if (isNdjson) {
            buffer += tail;
            if (buffer.trim()) {
              for (const line of buffer.split(/\r\n|\n|\r/)) {
                if (processNdjsonLine(line)) break;
              }
              buffer = '';
            }
          } else if (tail) {
            // Feed any final UTF-8 bytes without fabricating a blank-line event terminator at EOF.
            feedSSEChunk(tail);
          }
          break;
        }

        const chunkText = decoder.decode(value, { stream: true });

        if (isNdjson) {
          buffer += chunkText;
          const lines = buffer.split(/\r\n|\n|\r/);
          buffer = lines.pop() ?? '';
          for (const line of lines) {
            if (processNdjsonLine(line)) break;
          }
        } else {
          if (feedSSEChunk(chunkText)) {
            break;
          }
        }
      }

      if (signal?.aborted) {
        throw new DOMException('Aborted', 'AbortError');
      }

      if (!sawDone) {
        throw new APIError(
          '云端流式响应在完成前意外中断（未收到 [DONE] 标志），已中止保存以防残缺回复，请重试',
          502,
        );
      }

      if (inReasoning) {
        fullContent += '\n</think>\n\n';
        onDelta('\n</think>\n\n', { modelId: actualModelId });
      }
    } finally {
      if (!streamFinished || signal?.aborted) {
        void reader.cancel().catch(() => {});
      }
      reader.releaseLock();
    }

    const durationMs = Math.max(60, Math.round(performance.now() - startedAt));
    const tokensGenerated =
      reportedCompletionTokens > 0
        ? reportedCompletionTokens
        : Math.max(chunkCount, Math.max(1, Math.round(fullContent.length / 3)));
    const tokensPerSec = Number(((tokensGenerated / durationMs) * 1000).toFixed(1));

    return {
      content: fullContent,
      modelId: actualModelId,
      tokensGenerated,
      tokensPerSec,
      durationMs,
    };
  },

  probeLLMProtocol: async (
    protocol: LLMProtocolKind,
    prompt: string,
    modelId: string,
    onDelta: (delta: string) => void,
    signal?: AbortSignal,
    opts?: {
      think?: boolean;
      maxTokens?: number;
      onReasoningDelta?: (delta: string) => void;
    },
  ): Promise<ChatCompletionSummary> => {
    const startedAt = performance.now();
    let ttftMs: number | undefined;
    const markTTFT = () => {
      if (ttftMs === undefined) {
        ttftMs = Math.max(1, Math.round(performance.now() - startedAt));
      }
    };
    const emitDelta = (piece: string) => {
      if (!piece) return;
      markTTFT();
      onDelta(piece);
    };
    const emitReasoning = (piece: string) => {
      if (!piece) return;
      markTTFT();
      if (opts?.onReasoningDelta) {
        opts.onReasoningDelta(piece);
      } else {
        onDelta(piece);
      }
    };
    const maxTokens = opts?.maxTokens ?? 512;

    if (protocol === 'openai') {
      const res = await v11Api.streamChat(
        [{ role: 'user', content: prompt }],
        modelId,
        (delta) => emitDelta(delta),
        signal,
        undefined,
        {
          think: opts?.think,
          maxTokens,
          onReasoningDelta: opts?.onReasoningDelta ? (d) => emitReasoning(d) : undefined,
        },
      );
      return { ...res, ttftMs, protocol };
    }

    if (isDemoSiteMode()) {
      const res = await simulateStreamingChat(
        [{ role: 'user', content: prompt }],
        modelId,
        (delta) => emitDelta(delta),
        signal,
      );
      return { ...res, ttftMs, protocol };
    }

    const headers = new Headers({
      'Content-Type': 'application/json',
      Accept: 'text/event-stream, application/json',
    });
    const token = getStoredAdminToken();
    if (token) {
      headers.set('Authorization', `Bearer ${token}`);
      if (protocol === 'anthropic') {
        headers.set('x-api-key', token);
      }
    }
    if (protocol === 'anthropic') {
      headers.set('anthropic-version', '2023-06-01');
    }

    const endpoint = protocol === 'responses' ? '/v1/responses' : '/v1/messages';
    const bodyObj: Record<string, unknown> =
      protocol === 'responses'
        ? {
            model: modelId,
            input: prompt,
            stream: true,
            max_output_tokens: maxTokens,
          }
        : {
            model: modelId,
            max_tokens: maxTokens,
            stream: true,
            messages: [{ role: 'user', content: prompt }],
          };
    if (typeof opts?.think === 'boolean') {
      bodyObj.think = opts.think;
      if (protocol === 'anthropic') {
        bodyObj.thinking = { type: opts.think ? 'enabled' : 'disabled' };
      } else if (protocol === 'responses') {
        bodyObj.reasoning = { effort: opts.think ? 'medium' : 'none' };
      }
    }
    const body = JSON.stringify(bodyObj);

    const res = await fetch(endpoint, { method: 'POST', headers, body, signal });
    if (!res.ok) {
      const ct = res.headers.get('content-type') || '';
      const errBody = ct.includes('application/json')
        ? await res.json().catch(() => null)
        : await res.text().catch(() => '');
      throw new APIError(
        extractApiErrorMessage(errBody, `HTTP ${res.status} ${res.statusText}`),
        res.status,
        errBody,
      );
    }

    const contentType = (res.headers.get('content-type') || '').toLowerCase();
    if (contentType.includes('application/json') && !contentType.includes('event-stream')) {
      const json = (await res.json().catch(() => null)) as Record<string, unknown> | null;
      let text = '';
      let reasoningText = '';
      if (json && protocol === 'responses') {
        text = typeof json.output_text === 'string' ? json.output_text : '';
        if (Array.isArray(json.output)) {
          for (const item of json.output as Array<{ type?: string; summary?: Array<{ text?: string }> }>) {
            if (item?.type === 'reasoning' && Array.isArray(item.summary)) {
              reasoningText += item.summary.map((s) => s?.text || '').join('');
            }
          }
        }
      } else if (json && Array.isArray(json.content)) {
        for (const blk of json.content as Array<{ type?: string; text?: string; thinking?: string }>) {
          if (blk?.type === 'thinking' && blk.thinking) reasoningText += blk.thinking;
          else if (blk?.text) text += blk.text;
        }
      }
      if (reasoningText) emitReasoning(reasoningText);
      if (text) emitDelta(text);
      const durationMs = Math.max(50, Math.round(performance.now() - startedAt));
      const combinedLen = reasoningText.length + text.length;
      const estTok = Math.max(1, Math.round(combinedLen / 2.5));
      return {
        content: text,
        modelId,
        tokensGenerated: estTok,
        tokensPerSec: Number(((estTok / durationMs) * 1000).toFixed(1)),
        durationMs,
        ttftMs,
        protocol,
      };
    }

    if (!res.body) {
      throw new APIError('服务端未返回流式响应体 (Empty stream)', 502);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let fullContent = '';
    let reasoningLen = 0;
    let outputTokens = 0;
    let buf = '';
    let curEvent = '';

    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        const lines = buf.split(/\r?\n/);
        buf = lines.pop() ?? '';
        for (const rawLine of lines) {
          const line = rawLine.trim();
          if (!line) {
            curEvent = '';
            continue;
          }
          if (line.startsWith(':')) continue;
          if (line.startsWith('event:')) {
            curEvent = line.slice(6).trim();
            continue;
          }
          if (!line.startsWith('data:')) continue;
          const dataStr = line.slice(5).trim();
          if (!dataStr || dataStr === '[DONE]') continue;
          let parsed: Record<string, unknown>;
          try {
            parsed = JSON.parse(dataStr) as Record<string, unknown>;
          } catch {
            continue;
          }
          const evtType = (typeof parsed.type === 'string' ? parsed.type : curEvent) || '';
          if (evtType === 'error') {
            throw new APIError(extractApiErrorMessage(parsed, 'Protocol stream error'), 502, parsed);
          }
          if (protocol === 'responses') {
            if (evtType === 'response.reasoning_summary_text.delta' && typeof parsed.delta === 'string') {
              reasoningLen += parsed.delta.length;
              emitReasoning(parsed.delta);
            } else if (evtType === 'response.output_text.delta' && typeof parsed.delta === 'string') {
              fullContent += parsed.delta;
              emitDelta(parsed.delta);
            } else if (evtType === 'response.completed') {
              const respObj = parsed.response as { usage?: { output_tokens?: number } } | undefined;
              if (respObj?.usage?.output_tokens) outputTokens = respObj.usage.output_tokens;
            }
          } else if (protocol === 'anthropic') {
            if (evtType === 'content_block_delta') {
              const d = parsed.delta as { type?: string; text?: string; thinking?: string } | undefined;
              if (d?.thinking) {
                reasoningLen += d.thinking.length;
                emitReasoning(d.thinking);
              } else if (d?.text) {
                fullContent += d.text;
                emitDelta(d.text);
              }
            } else if (evtType === 'message_delta') {
              const u = parsed.usage as { output_tokens?: number } | undefined;
              if (u?.output_tokens) outputTokens = u.output_tokens;
            }
          }
        }
      }
    } finally {
      if (signal?.aborted) {
        void reader.cancel().catch(() => {});
      }
      reader.releaseLock();
    }

    const durationMs = Math.max(50, Math.round(performance.now() - startedAt));
    const tokensGenerated =
      outputTokens > 0
        ? outputTokens
        : Math.max(1, Math.round((fullContent.length + reasoningLen) / 2.5));
    return {
      content: fullContent,
      modelId,
      tokensGenerated,
      tokensPerSec: Number(((tokensGenerated / durationMs) * 1000).toFixed(1)),
      durationMs,
      ttftMs,
      protocol,
    };
  },

  // --------------------------------------------------------------------------
  // Pillar 2: 1-Click AI App Store & Private Local Port Bridge
  // --------------------------------------------------------------------------
  getAppTemplates: async (signal?: AbortSignal): Promise<AppTemplate[]> => {
    const demoCatalog = () => {
      const merged = [...DEMO_APP_TEMPLATES];
      for (const p of demoInstalledPlugins) {
        const idx = merged.findIndex((item) => item.id === p.id);
        if (idx >= 0) merged[idx] = p;
        else merged.push(p);
      }
      return merged;
    };
    const raw = await v11Request<unknown>('/api/v1/apps/templates', { signal }, demoCatalog);
    return unwrapArray<AppTemplate>(raw, ['templates', 'apps', 'items', 'data']);
  },

  installAppPlugin: async (manifestJson: string): Promise<AppTemplate> => {
    let parsedRaw: unknown;
    try {
      parsedRaw = JSON.parse(manifestJson);
    } catch {
      throw new APIError('插件清单 JSON 格式不正确，请检查引号、逗号或大括号', 400);
    }
    if (!parsedRaw || typeof parsedRaw !== 'object' || Array.isArray(parsedRaw)) {
      throw new APIError('插件清单必须是一个 JSON 对象 (colabhive-app.json)', 400);
    }
    const manifest = parsedRaw as Partial<AppPluginManifest>;
    const id = typeof manifest.id === 'string' ? manifest.id.trim() : '';
    if (!SAFE_PLUGIN_ID_RE.test(id)) {
      throw new APIError(
        '插件 id 不合法：仅支持小写字母、数字和连字符，长度 2–49 位（例如 my-video-studio）',
        400,
      );
    }
    const category =
      typeof manifest.category === 'string' && VALID_PLUGIN_CATEGORIES.has(manifest.category)
        ? (manifest.category as AppCategory)
        : null;
    if (!category) {
      throw new APIError(
        '插件 category 必须为 video / image / audio / workspace / custom 之一',
        400,
      );
    }
    const remotePort = Number(manifest.remote_port);
    if (!Number.isInteger(remotePort) || remotePort < 1024 || remotePort > 65535) {
      throw new APIError('插件 remote_port 必须为 1024–65535 之间的整数端口', 400);
    }

    return v11Request<AppTemplate>(
      '/api/v1/apps/plugins',
      {
        method: 'POST',
        body: JSON.stringify(parsedRaw),
      },
      async () => {
        const nameEn = (manifest.name || id).trim();
        const nameZh = (manifest.name_zh || nameEn).trim();
        const localPort =
          manifest.local_port &&
          Number.isInteger(manifest.local_port) &&
          manifest.local_port >= 1024 &&
          manifest.local_port <= 65535
            ? manifest.local_port
            : remotePort;
        const tpl: AppTemplate = {
          id,
          name: nameEn,
          name_zh: nameZh,
          tagline: manifest.tagline?.trim() || `${nameEn} community plugin`,
          tagline_zh: manifest.tagline_zh?.trim() || `${nameZh} · 第三方社区插件`,
          description:
            manifest.description?.trim() ||
            `Installed from colabhive-app.json manifest (${manifest.repo_url || 'custom runtime'}).`,
          description_zh:
            manifest.description_zh?.trim() ||
            `通过 colabhive-app.json 清单接入的第三方 AI 应用插件${manifest.repo_url ? `（${manifest.repo_url}）` : ''}。`,
          category,
          icon: manifest.icon?.trim() || '🧩',
          default_remote_port: remotePort,
          default_local_port: localPort,
          min_vram_gb: typeof manifest.min_vram_gb === 'number' ? manifest.min_vram_gb : 8,
          recommended_gpu: manifest.recommended_gpu ?? 'T4',
          free_tier_compatible: manifest.free_tier_compatible ?? true,
          est_boot_sec: typeof manifest.est_boot_sec === 'number' ? manifest.est_boot_sec : 45,
          highlights:
            Array.isArray(manifest.highlights) && manifest.highlights.length > 0
              ? manifest.highlights.map(String)
              : ['Community Plugin', `Port ${remotePort}`],
          highlights_zh:
            Array.isArray(manifest.highlights_zh) && manifest.highlights_zh.length > 0
              ? manifest.highlights_zh.map(String)
              : ['第三方插件', `端口 ${remotePort}`],
          supports_port_bridge: manifest.supports_port_bridge ?? true,
          plugin_integration_tip_en: manifest.plugin_integration_tip_en,
          plugin_integration_tip_zh: manifest.plugin_integration_tip_zh,
          author: manifest.author,
          version: manifest.version,
          is_plugin: true,
        };
        const idx = demoInstalledPlugins.findIndex((p) => p.id === tpl.id);
        if (idx >= 0) demoInstalledPlugins[idx] = tpl;
        else demoInstalledPlugins.push(tpl);
        notifyV11StateChanged();
        return { ...tpl };
      },
    );
  },

  deleteAppPlugin: async (id: string): Promise<void> => {
    return v11Request<void>(
      `/api/v1/apps/plugins/${encodeURIComponent(id)}`,
      { method: 'DELETE' },
      async () => {
        const idx = demoInstalledPlugins.findIndex((p) => p.id === id);
        if (idx >= 0) {
          demoInstalledPlugins.splice(idx, 1);
          notifyV11StateChanged();
        }
      },
    );
  },

  getAppInstances: async (signal?: AbortSignal): Promise<AppInstance[]> => {
    const raw = await v11Request<unknown>(
      '/api/v1/apps/instances',
      { signal },
      () => {
        const zh = isZhDemoLocale();
        return demoState.appInstances.map((inst) => {
          const tpl = DEMO_APP_TEMPLATES.find((t) => t.id === inst.template_id);
          const opt = tpl?.model_options?.find((m) => m.id === inst.selected_model_id);
          return {
            ...inst,
            selected_model_name: opt
              ? zh
                ? opt.name_zh
                : opt.name
              : inst.selected_model_name,
            last_failover_note:
              inst.id === 'app-wangp-demo-01' && !zh
                ? 'Restored from checkpoint snapshot and relayed to Lab Ultra #1 (82%)'
                : inst.last_failover_note,
          };
        });
      },
    );
    const list = unwrapArray<AppInstance>(raw, ['instances', 'apps', 'items', 'data']);
    return list.map((inst) => ({
      ...inst,
      account_label: inst.account_label
        ? sanitizeAccountLabel(inst.account_label)
        : undefined,
    }));
  },

  launchApp: async (req: AppLaunchRequest): Promise<AppInstance> => {
    return v11Request<AppInstance>(
      '/api/v1/apps/launch',
      {
        method: 'POST',
        body: JSON.stringify(req),
      },
      async () => {
        const allDemoTemplates = [...DEMO_APP_TEMPLATES, ...demoInstalledPlugins];
        const tpl =
          allDemoTemplates.find((t) => t.id === req.template_id) ??
          DEMO_APP_TEMPLATES.find((t) => t.id === 'comfyui') ??
          DEMO_APP_TEMPLATES[0];
        const existing = demoState.appInstances.find(
          (i) => i.template_id === tpl.id && (i.state === 'running' || i.state === 'starting'),
        );
        if (existing) {
          existing.idle_auto_stop_min = req.idle_auto_stop_min ?? existing.idle_auto_stop_min;
          existing.last_active_at = nowIso();
          notifyV11StateChanged();
          return { ...existing };
        }

        const usedPorts = new Set(
          demoState.appInstances
            .filter((i) => i.state !== 'stopped')
            .map((i) => i.local_port ?? i.remote_port),
        );
        let localPort = req.preferred_local_port ?? tpl.default_local_port;
        while (usedPorts.has(localPort)) {
          localPort += 1;
        }

        const matchedModel = tpl.model_options?.find((m) => m.id === req.selected_model_id);
        const gpu =
          req.preferred_gpu ?? matchedModel?.recommended_gpu ?? tpl.recommended_gpu ?? 'T4';
        const isFree = gpu === 'T4' || gpu === 'CPU';
        const chosenAccountId =
          req.account_id?.trim() || (isFree ? 'acc_free_04' : 'acc_proplus_02');
        const chosenAccountLabel = req.account_id?.trim()
          ? `指定 Colab 账号 (${req.account_id.trim()})`
          : isFree
            ? 'Free-T4-Standby (Colab 免费卡 T4)'
            : `ProPlus-Alex (Colab ${gpu})`;
        const selectedModelName = req.model_download_url?.trim()
          ? `自定义模型 (${req.model_download_url.trim().split('/').pop() || 'custom'})`
          : matchedModel?.name_zh || matchedModel?.name || (tpl.id === 'comfyui' ? '空白环境' : undefined);
        const checkpointDir =
          tpl.id === 'comfyui'
            ? '/content/ComfyUI/output'
            : tpl.id === 'jupyterlab'
              ? '/content'
              : `/content/checkpoints/${tpl.id}`;
        const id = `app-${tpl.id}-${Date.now().toString(36).slice(-5)}`;
        const created: AppInstance = {
          id,
          template_id: tpl.id,
          name: req.name?.trim() || tpl.name,
          name_zh: req.name?.trim() || tpl.name_zh,
          category: tpl.category,
          state: 'running',
          status_step: `Ready on private local port 127.0.0.1:${localPort}`,
          status_step_zh: `云端 ${gpu} 已就绪 · 本机直连 127.0.0.1:${localPort} 已打通`,
          progress_pct: 100,
          session_id: `sess-${id}`,
          account_id: chosenAccountId,
          account_label: chosenAccountLabel,
          provider: 'colab',
          assigned_gpu: gpu,
          free_tier_active: isFree,
          remote_port: req.custom_remote_port ?? tpl.default_remote_port,
          local_port: localPort,
          local_bridge_active: true,
          local_url: `http://127.0.0.1:${localPort}`,
          web_proxy_url: `/apps/proxy/${id}/`,
          idle_auto_stop_min: req.idle_auto_stop_min ?? 45,
          auto_resume_enabled: req.auto_resume_enabled ?? demoState.checkpointPolicy.auto_resume_enabled,
          failover_count: 0,
          selected_model_id: matchedModel?.id || req.selected_model_id,
          selected_model_name: selectedModelName,
          model_download_url: req.model_download_url?.trim() || undefined,
          checkpoint_dir: checkpointDir,
          custom_repo_url: req.custom_repo_url,
          custom_command: req.custom_command,
          started_at: nowIso(),
          last_active_at: nowIso(),
        };
        demoState.appInstances = [created, ...demoState.appInstances.filter((i) => i.state !== 'stopped')];
        notifyV11StateChanged();
        return { ...created };
      },
      LONG_OPERATION_TIMEOUT_MS,
    );
  },

  updateAppIdleTimeout: async (id: string, idleMin: number): Promise<AppInstance> => {
    return v11Request<AppInstance>(
      `/api/v1/apps/instances/${encodeURIComponent(id)}`,
      {
        method: 'PATCH',
        body: JSON.stringify({ idle_auto_stop_min: idleMin }),
      },
      async () => {
        if (!Number.isInteger(idleMin) || idleMin < 5 || idleMin > 720) {
          throw new APIError('idle_auto_stop_min must be between 5 and 720 minutes', 400);
        }
        const idx = demoState.appInstances.findIndex((i) => i.id === id);
        if (idx < 0) {
          throw new APIError('App instance not found', 404);
        }
        const updated: AppInstance = {
          ...demoState.appInstances[idx],
          idle_auto_stop_min: idleMin,
          last_active_at: nowIso(),
        };
        demoState.appInstances[idx] = updated;
        notifyV11StateChanged();
        return { ...updated };
      },
    );
  },

  stopApp: async (id: string, force = false): Promise<AppInstance | void> => {
    const query = force ? '?force=true' : '';
    return v11Request<AppInstance | void>(
      `/api/v1/apps/instances/${encodeURIComponent(id)}/stop${query}`,
      { method: 'POST' },
      async () => {
        demoState.appInstances = demoState.appInstances.filter((i) => i.id !== id);
        notifyV11StateChanged();
      },
      LONG_OPERATION_TIMEOUT_MS,
    );
  },

  restartApp: async (id: string): Promise<AppInstance> => {
    return v11Request<AppInstance>(
      `/api/v1/apps/instances/${encodeURIComponent(id)}/restart`,
      { method: 'POST' },
      async () => {
        const idx = demoState.appInstances.findIndex((i) => i.id === id);
        if (idx < 0) {
          throw new APIError('App instance not found', 404);
        }
        const updated: AppInstance = {
          ...demoState.appInstances[idx],
          state: 'running',
          progress_pct: 100,
          status_step_zh: '已重新启动并恢复上次进度快照',
          last_active_at: nowIso(),
        };
        demoState.appInstances[idx] = updated;
        notifyV11StateChanged();
        return { ...updated };
      },
      LONG_OPERATION_TIMEOUT_MS,
    );
  },

  // --------------------------------------------------------------------------
  // Pillar 4: Auto-Checkpoint Preemption Relay & Shareable Savings Receipt
  // --------------------------------------------------------------------------
  getCheckpointPolicy: async (signal?: AbortSignal): Promise<CheckpointPolicy> => {
    return v11Request<CheckpointPolicy>(
      '/api/v1/checkpoints/policy',
      { signal },
      () => ({ ...demoState.checkpointPolicy }),
    );
  },

  updateCheckpointPolicy: async (policy: CheckpointPolicy): Promise<CheckpointPolicy> => {
    return v11Request<CheckpointPolicy>(
      '/api/v1/checkpoints/policy',
      {
        method: 'PUT',
        body: JSON.stringify(policy),
      },
      async () => {
        demoState.checkpointPolicy = {
          ...demoState.checkpointPolicy,
          ...policy,
          updated_at: nowIso(),
        };
        demoState.appInstances = demoState.appInstances.map((inst) => ({
          ...inst,
          auto_resume_enabled: demoState.checkpointPolicy.auto_resume_enabled,
        }));
        notifyV11StateChanged();
        return { ...demoState.checkpointPolicy };
      },
    );
  },

  listCheckpoints: async (signal?: AbortSignal): Promise<CheckpointRecord[]> => {
    const raw = await v11Request<unknown>(
      '/api/v1/checkpoints',
      { signal },
      () => demoState.checkpoints.map((c) => ({ ...c })),
    );
    const list = unwrapArray<CheckpointRecord>(raw, ['checkpoints', 'records', 'items', 'data']);
    return list.map((rec) => ({
      ...rec,
      source_account_label: sanitizeAccountLabel(rec.source_account_label),
      resumed_account_label: rec.resumed_account_label
        ? sanitizeAccountLabel(rec.resumed_account_label)
        : undefined,
    }));
  },

  syncCheckpoint: async (req: CheckpointSyncRequest): Promise<CheckpointRecord> => {
    return v11Request<CheckpointRecord>(
      '/api/v1/checkpoints/sync',
      {
        method: 'POST',
        body: JSON.stringify(req),
      },
      async () => {
        const activeApp = demoState.appInstances[0];
        const created: CheckpointRecord = {
          id: `ckpt-${Date.now().toString(36).slice(-6)}`,
          job_id: req.job_id || activeApp?.id || 'manual-snapshot',
          job_title:
            req.job_title ||
            (activeApp ? `${activeApp.name_zh} · 手动安全快照` : '🛡️ 当前云端任务进度快照'),
          job_kind: (req.job_kind as CheckpointRecord['job_kind']) || 'app',
          source_session_id: req.session_id || activeApp?.session_id || 'sess-active-01',
          source_account_label: sanitizeAccountLabel(activeApp?.account_label || '账号 #1 (免费显卡)'),
          assigned_gpu: activeApp?.assigned_gpu ?? 'T4',
          remote_path: req.remote_path || activeApp?.checkpoint_dir || '/content/outputs',
          local_snapshot_dir: '~/.colabhive/checkpoints/latest',
          file_count: 14,
          total_bytes: 128 * 1024 * 1024,
          progress_pct: req.progress_pct ?? 88,
          relay_count: 0,
          status: 'synced',
          last_relay_note_en: 'Manual snapshot saved to local disk — ready for instant account handoff',
          last_relay_note_zh: '已手动保存最新进度快照回电脑！一旦当前账号到点下线可立即换号接着跑',
          resume_command: req.resume_command,
          created_at: nowIso(),
          updated_at: nowIso(),
        };
        demoState.checkpoints = [created, ...demoState.checkpoints];
        notifyV11StateChanged();
        return { ...created };
      },
      LONG_OPERATION_TIMEOUT_MS,
    );
  },

  resumeCheckpoint: async (
    id: string,
    req: CheckpointResumeRequest = {},
  ): Promise<CheckpointRecord> => {
    return v11Request<CheckpointRecord>(
      `/api/v1/checkpoints/${encodeURIComponent(id)}/resume`,
      {
        method: 'POST',
        body: JSON.stringify(req),
      },
      async () => {
        const idx = demoState.checkpoints.findIndex((c) => c.id === id);
        const base = idx >= 0 ? demoState.checkpoints[idx] : demoState.checkpoints[0];
        const nextAcctNum = ((base?.relay_count ?? 0) % 3) + 2;
        const pct = base?.progress_pct ?? 82;
        const updated: CheckpointRecord = {
          ...base,
          id: base?.id ?? id,
          resumed_session_id: `sess-relay-${Date.now().toString(36).slice(-4)}`,
          resumed_account_label: `账号 #${nextAcctNum} (Colab 算力池)`,
          assigned_gpu: req.preferred_gpu ?? base?.assigned_gpu ?? 'T4',
          relay_count: (base?.relay_count ?? 0) + 1,
          status: 'resumed',
          last_relay_note_en: `${base?.source_account_label ?? 'Account #1'} quota ended -> Resumed on Account #${nextAcctNum} from ${pct}%`,
          last_relay_note_zh: `${base?.source_account_label ?? '账号 #1'} 免费时长耗尽 → 已在 账号 #${nextAcctNum} 从 ${pct}% 自动接着跑`,
          updated_at: nowIso(),
        };
        if (idx >= 0) {
          demoState.checkpoints[idx] = updated;
        } else {
          demoState.checkpoints = [updated, ...demoState.checkpoints];
        }
        notifyV11StateChanged();
        return { ...updated };
      },
      LONG_OPERATION_TIMEOUT_MS,
    );
  },

  getSavingsReceipt: async (): Promise<SavingsReceiptStats> => {
    const raw = await v11Request<SavingsReceiptStats>(
      '/api/v1/stats/savings-card',
      {},
      () => buildDemoSavingsReceipt(),
    );
    // Strictly scrub any accidental email pattern so 100% zero PII reaches the poster
    const scrub = (s: string) =>
      (s || '').replace(/[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}/g, '私有池账号');
    return {
      ...raw,
      pool_title: scrub(raw.pool_title || 'ColabHive 私人聚合算力池'),
      share_copy_en: scrub(raw.share_copy_en),
      share_copy_zh: scrub(raw.share_copy_zh),
    };
  },
};

export default v11Api;
