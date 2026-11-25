// Package appstore implements ColabHive v1.1 Pillar 2: the plugin-based 1-Click AI App Store
// with 3 real core built-in apps (Private AI Chat, ComfyUI Studio, JupyterLab), declarative
// community JSON plugins, private localhost port bridging, idle auto-shutdown, and automatic
// cross-account checkpoint failover.
package appstore

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/colabhive/colabhive/internal/models"
)

var (
	githubOwnerSegmentRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,99}$`)
	githubRepoSegmentRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,99}$`)
	modelFilenameRe      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,180}$`)
	modelSubdirSegRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\-]{0,80}$`)
)

// ShellQuotePOSIX wraps s in POSIX single quotes, safely escaping any embedded single quotes.
func ShellQuotePOSIX(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// ValidateAndSanitizeGitHubRepoURL enforces strict `https://github.com/<owner>/<repo>` validation
// and returns the canonical HTTPS clone URL alongside its POSIX shell-escaped form.
func ValidateAndSanitizeGitHubRepoURL(raw string) (cleanURL string, shellEscaped string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", errors.New("invalid custom_repo_url: https://github.com/<owner>/<repo> is required")
	}
	if strings.ContainsAny(trimmed, " \t\r\n\x00;&|$`()<>'\"\\!") {
		return "", "", errors.New("invalid custom_repo_url: forbidden shell metacharacters detected")
	}

	u, parseErr := url.Parse(trimmed)
	if parseErr != nil {
		return "", "", fmt.Errorf("invalid custom_repo_url: %w", parseErr)
	}
	if u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") {
		return "", "", errors.New("invalid custom_repo_url: only https://github.com/<owner>/<repo> URLs are permitted")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", errors.New("invalid custom_repo_url: credentials, query parameters, and fragments are forbidden")
	}

	cleanPath := strings.Trim(u.Path, "/")
	cleanPath = strings.TrimSuffix(cleanPath, ".git")
	parts := strings.Split(cleanPath, "/")
	if len(parts) != 2 {
		return "", "", errors.New("invalid custom_repo_url: expected https://github.com/<owner>/<repo>")
	}
	owner, repo := parts[0], parts[1]
	if owner == "." || owner == ".." || repo == "." || repo == ".." {
		return "", "", errors.New("invalid custom_repo_url: path traversal segments forbidden")
	}
	if !githubOwnerSegmentRe.MatchString(owner) || !githubRepoSegmentRe.MatchString(repo) {
		return "", "", errors.New("invalid custom_repo_url: owner and repository names must be alphanumeric")
	}

	canonical := fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
	return canonical, ShellQuotePOSIX(canonical), nil
}

// ValidateAndSanitizeModelDownloadURL strictly validates a direct model weight URL, allowing ONLY
// `https://huggingface.co/...` or `https://civitai.com/...` with zero shell metacharacters,
// no credentials, no fragments, and no whitespace.
func ValidateAndSanitizeModelDownloadURL(raw string) (cleanURL string, filename string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", errors.New("invalid model_download_url: URL is required")
	}
	if len(trimmed) > 2048 {
		return "", "", errors.New("invalid model_download_url: exceeds maximum length (2048 chars)")
	}
	if strings.ContainsAny(trimmed, " \t\r\n\x00;&|$`()<>'\"\\!") {
		return "", "", errors.New("invalid model_download_url: forbidden whitespace or shell metacharacters detected")
	}

	u, parseErr := url.Parse(trimmed)
	if parseErr != nil {
		return "", "", fmt.Errorf("invalid model_download_url: %w", parseErr)
	}
	if u.Scheme != "https" {
		return "", "", errors.New("invalid model_download_url: only https:// URLs are permitted")
	}
	if u.User != nil || u.Fragment != "" {
		return "", "", errors.New("invalid model_download_url: credentials and URL fragments are forbidden")
	}
	if u.Port() != "" {
		return "", "", errors.New("invalid model_download_url: custom ports are forbidden")
	}

	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host != "huggingface.co" && host != "civitai.com" {
		return "", "", errors.New("invalid model_download_url: only https://huggingface.co/... or https://civitai.com/... URLs are permitted")
	}

	trimmedPath := strings.Trim(u.Path, "/")
	if trimmedPath == "" {
		return "", "", errors.New("invalid model_download_url: file path is required")
	}
	segments := strings.Split(trimmedPath, "/")
	if len(segments) < 2 {
		return "", "", errors.New("invalid model_download_url: expected a complete model file path")
	}
	for _, seg := range segments {
		if seg == "" || seg == "." || seg == ".." || strings.Contains(seg, "..") {
			return "", "", errors.New("invalid model_download_url: path traversal segments are forbidden")
		}
	}

	derivedName := deriveSafeModelFilename(host, segments[len(segments)-1])
	u.Host = host
	return u.String(), derivedName, nil
}

func deriveSafeModelFilename(host string, lastSeg string) string {
	decoded, err := url.PathUnescape(lastSeg)
	if err != nil {
		decoded = lastSeg
	}
	cleaned := strings.TrimSpace(decoded)
	var b strings.Builder
	for i := 0; i < len(cleaned) && b.Len() < 160; i++ {
		c := cleaned[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			b.WriteByte(c)
		} else {
			b.WriteByte('_')
		}
	}
	name := strings.Trim(b.String(), "._-")
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", "_")
	}
	if name == "" {
		name = "custom_model"
	}

	lower := strings.ToLower(name)
	hasKnownExt := strings.HasSuffix(lower, ".safetensors") ||
		strings.HasSuffix(lower, ".ckpt") ||
		strings.HasSuffix(lower, ".pt") ||
		strings.HasSuffix(lower, ".pth") ||
		strings.HasSuffix(lower, ".bin") ||
		strings.HasSuffix(lower, ".gguf") ||
		strings.HasSuffix(lower, ".onnx")
	if !hasKnownExt {
		if host == "civitai.com" && !strings.HasPrefix(lower, "civitai_") {
			name = "civitai_" + name
		}
		name += ".safetensors"
	}
	if !modelFilenameRe.MatchString(name) {
		return "custom_model.safetensors"
	}
	return name
}

// ValidateModelTargetSubdir validates a ComfyUI/app relative model subdirectory (e.g. "models/checkpoints").
func ValidateModelTargetSubdir(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "models/checkpoints", nil
	}
	if strings.ContainsAny(trimmed, " \t\r\n\x00\\;&|$`()<>'\"!") {
		return "", fmt.Errorf("invalid target_subdir %q: forbidden characters", raw)
	}
	if strings.HasPrefix(trimmed, "/") {
		return "", fmt.Errorf("invalid target_subdir %q: must be relative", raw)
	}
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "..") {
		return "", fmt.Errorf("invalid target_subdir %q: traversal forbidden", raw)
	}
	segs := strings.Split(cleaned, "/")
	for _, seg := range segs {
		if !modelSubdirSegRe.MatchString(seg) {
			return "", fmt.Errorf("invalid target_subdir %q: unsafe segment %q", raw, seg)
		}
	}
	return cleaned, nil
}

// ValidateModelFilename validates a safe single model filename (no directories or traversal).
func ValidateModelFilename(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("invalid model filename: empty")
	}
	if strings.ContainsAny(trimmed, "/\\\x00:") || strings.Contains(trimmed, "..") || !modelFilenameRe.MatchString(trimmed) {
		return "", fmt.Errorf("invalid model filename %q", raw)
	}
	return trimmed, nil
}

// ValidateCustomStartCommand validates an optional user-supplied startup command so no shell
// chaining, redirection, command substitution, or subshell injection is ever possible.
func ValidateCustomStartCommand(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	if len(trimmed) > 512 {
		return "", errors.New("invalid custom_command: exceeds maximum length (512 chars)")
	}
	if strings.ContainsAny(trimmed, "\r\n\x00;&|$`()<>\\") {
		return "", errors.New("invalid custom_command: shell chaining, pipes, redirections, and substitutions are forbidden")
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", nil
	}
	bin := strings.ToLower(fields[0])
	switch bin {
	case "python", "python3", "uv", "streamlit", "gradio", "uvicorn", "gunicorn", "fastapi":
	default:
		return "", fmt.Errorf("invalid custom_command: launcher %q not allowed (supported: python, python3, uv, streamlit, gradio, uvicorn, gunicorn, fastapi)", fields[0])
	}
	return strings.Join(fields, " "), nil
}

var builtinTemplates = []models.AppTemplate{
	{
		ID:                 "llm-chat",
		Name:               "Uncensored Open-Weight LLM Deployer (/v1)",
		NameZh:             "无审查开源大模型一键部署 (/v1)",
		Tagline:            "1-click Heretic & Abliterated DeepSeek-V4.1-Flash / GLM-5.3-Flash / Qwen3.8 / Gemma 4 GGUFs with local /v1 API",
		TaglineZh:          "一键部署 DeepSeek-V4.1-Flash / GLM-5.3-Flash / Qwen3.8 / Gemma 4 无审查模型，吐出本机 /v1 接口直连酒馆与 Cherry Studio",
		Description:        "Deploy top-downloaded Nov 2026 community uncensored (Heretic / Abliterated / GSQ-RCO) open-weight LLMs or any custom Hugging Face GGUF onto pooled Colab Free T4, L4, A100, or 96GB Blackwell G4 GPUs. Exposes private OpenAI /v1, Anthropic /v1/messages, and Ollama endpoints for SillyTavern, Cherry Studio, Open WebUI, and Cursor with automatic idle shutdown.",
		DescriptionZh:      "精选 2026 年 11 月社区下载量最高、口碑最好的 Heretic / Abliterated 正交去审查开源大模型（保留原版智商、零说教零拒答），一键部署到 Colab 免费 T4 / L4 / A100 / 96GB G4 显卡，吐出本机 /v1 接口直连 SillyTavern（酒馆）、Cherry Studio、Open WebUI 与 Cursor，闲置自动停表省钱。",
		Category:           models.AppCategoryWorkspace,
		Icon:               "⚡",
		DefaultRemotePort:  11434,
		DefaultLocalPort:   7860,
		MinVRAMGB:          8,
		RecommendedGPU:     models.AcceleratorT4,
		FreeTierCompatible: true,
		EstBootSec:         25,
		Highlights: []string{
			"Nov 2026 top uncensored GGUFs (DeepSeek-V4.1-Flash-1017, GLM-5.3-Flash, Qwen3.8-Flash-Next, Qwen3.8-27B, Gemma 4) + any hf.co repo",
			"Private localhost /v1 OpenAI, Anthropic & Ollama API for SillyTavern, Cherry Studio & Cursor",
			"Hot-reloadable llm_catalog.json, explicit GPU/account pinning & automatic idle auto-stop",
		},
		HighlightsZh: []string{
			"内置 2026 社区口碑榜 Heretic / Abliterated 去审查 GGUF + 支持粘贴任意 Hugging Face 链接",
			"本机 127.0.0.1:7860/v1 直连 SillyTavern 酒馆、Cherry Studio、Open WebUI 与 Cursor",
			"支持 llm_catalog.json 热更新模型榜、自选显卡/账号与闲置自动关机停表",
		},
		SupportsPortBridge:     true,
		Kind:                   models.PluginKindEngine,
		Provides:               []string{"llm.openai.v1"},
		UI:                     &models.PluginUIConfig{Mode: "builtin_route", Route: "/apps/llm"},
		PluginIntegrationTipEn: "Open /apps/llm to deploy and copy your local http://127.0.0.1:7860/v1 endpoint into SillyTavern, Cherry Studio, or Cursor.",
		PluginIntegrationTipZh: "进入 /apps/llm 一键部署模型，复制本机 http://127.0.0.1:7860/v1 地址直接填进 SillyTavern 酒馆、Cherry Studio 或 Cursor！",
		BootstrapScript:        llmChatBootstrapPy,
	},
	{
		ID:                 "wangp",
		Name:               "WanGP Video & Image Super-Studio",
		NameZh:             "WanGP 开源视频与生图全家桶",
		Tagline:            "Zero-node 1-click AI Video & Image studio: MiniMax H3, LTX-2.5, Wan 2.2, H3-Image, Fibo 2.0, Flux 3 & Qwen Image 2.1",
		TaglineZh:          "不用连节点的爆款视频/生图神器：一站通吃 MiniMax H3、LTX-2.5、Wan 2.2、H3-Image、Fibo 2.0、Flux 3 与 Qwen Image 2.1",
		Description:        "Launches DeepBeepMeep's flagship WanGP web studio on your pooled Colab 96GB Blackwell G4, H100, A100, L4, or Free T4. Includes batch generation queues, native audio-video sync (MiniMax H3 & LTX-2.5), built-in Hugging Face & Civitai LoRA/checkpoint browser, private localhost bridge, and automatic idle shutdown so you never burn idle CU.",
		DescriptionZh:      "一键部署 GitHub 1.2 万星爆款开源平台 WanGP（小白无需懂节点），把 Google AI Ultra 96GB G4 / H100 显存直接拉满！支持 MiniMax H3 音画同步、LTX-2.5、Wan 2.2、Fibo 2.0、Flux 3、Qwen Image 2.1 与批量排队出片，内置 Hugging Face / Civitai 自定义权重与 LoRA 下载，用完自动停表并同步成品到本地。",
		Category:           models.AppCategoryVideo,
		Icon:               "🎬",
		DefaultRemotePort:  7861,
		DefaultLocalPort:   7861,
		MinVRAMGB:          8,
		RecommendedGPU:     models.AcceleratorG4,
		FreeTierCompatible: true,
		EstBootSec:         60,
		Highlights: []string{
			"All top 2026 open video & image engines in one UI: MiniMax H3, LTX-2.5, Wan 2.2, Hunyuan 1.5, Fibo 2.0, Flux 3 & Z-Image",
			"Built-in generation queue + Civitai / Hugging Face custom finetunes & LoRA downloader (zero censorship)",
			"Private 127.0.0.1:7861 localhost bridge, auto-checkpoints /content/WanGP/outputs & idle auto-stop",
		},
		HighlightsZh: []string{
			"一个网页通吃 2026 最火模型：MiniMax H3 音画同步 / LTX-2.5 / Wan 2.2 / Fibo 2.0 / Flux 3 / Qwen Image 2.1",
			"支持一次排 10 条提示词后台出片 + 内置 Civitai / Hugging Face 自定义无审查权重与 LoRA 加载",
			"本机 127.0.0.1:7861 私有直连免穿透，自动同步 /content/WanGP/outputs 成品并在闲置时自动停卡止血",
		},
		SupportsPortBridge:     true,
		SupportsCustomModelURL: true,
		Kind:                   models.PluginKindEngine,
		Provides:               []string{"media.image.v1", "media.video.v1", "media.audio.v1"},
		MediaAPIBase:           "/colabhive/v1",
		UI:                     &models.PluginUIConfig{Mode: "builtin_route", Route: "/studio/media"},
		ModelOptions: []models.AppModelOption{
			{
				ID:             "minimax-h3",
				Name:           "MiniMax H3 + H3-Image Profile (Audio-Video Sync · Rec. 96GB G4 / H100)",
				NameZh:         "MiniMax H3 音画同步 + H3-Image 方案（推荐 96GB G4 / H100）",
				Summary:        "Launches WanGP ready for MiniMax H3 (HyperFlow 6-step / Singularity / Regenerate 2K) & H3-Image inside the web UI",
				SummaryZh:      "启动 WanGP 并针对 MiniMax H3（含 6 步加速 / 2K 精修音画同步）与 H3-Image 优化配置，进网页即可按需拉取模型开跑",
				MinVRAMGB:      24,
				RecommendedGPU: models.AcceleratorG4,
				DownloadURL:    "",
			},
			{
				ID:             "ltx-2.5",
				Name:           "LTX-2.5 Native Audio-Video Profile (32kHz Stereo · Rec. L4 / A100 / G4)",
				NameZh:         "LTX-2.5 原生音画同步方案（32kHz 立体声 · 推荐 L4 / A100 / G4）",
				Summary:        "Launches WanGP tuned for Lightricks LTX-2.5 synchronized audio+video, camera direction & first/last-frame interpolation",
				SummaryZh:      "启动 WanGP 适配 Lightricks LTX-2.5 原生立体声音画同步视频、精准运镜与首尾帧插值",
				MinVRAMGB:      16,
				RecommendedGPU: models.AcceleratorL4,
				DownloadURL:    "",
			},
			{
				ID:             "wan-2.2",
				Name:           "Wan 2.2 MoE A14B / 5B Profile (Apache 2.0 · Rich Community LoRAs)",
				NameZh:         "Wan 2.2 旗舰视频方案（Apache 2.0 · 社区 LoRA 生态最全）",
				Summary:        "Launches WanGP for Alibaba Wan 2.2 T2V/I2V (14B MoE & 5B) with full community LoRA & Civitai support",
				SummaryZh:      "启动 WanGP 适配阿里 Wan 2.2 文生/图生视频（MoE 14B & 5B），海量社区无审查 LoRA 即插即用",
				MinVRAMGB:      12,
				RecommendedGPU: models.AcceleratorL4,
				DownloadURL:    "",
			},
			{
				ID:             "image-pack",
				Name:           "2026 Image Pack (Fibo 2.0 / Flux 3 / Qwen Image 2.1 / Z-Image Turbo)",
				NameZh:         "2026 顶流生图方案（Fibo 2.0 / Flux 3 / Qwen Image 2.1 / Z-Image Turbo）",
				Summary:        "Launches WanGP ready for high-speed photorealistic generation, multi-ref editing & Viggle character swap",
				SummaryZh:      "启动 WanGP 专注 2026 最强开源生图与多参考图编辑，支持 2~4 步极速出图与人物换装",
				MinVRAMGB:      8,
				RecommendedGPU: models.AcceleratorT4,
				DownloadURL:    "",
			},
		},
		PluginIntegrationTipEn: "Open http://127.0.0.1:7861 in your browser to queue MiniMax H3 / LTX-2.5 / Wan 2.2 videos and load Hugging Face or Civitai LoRAs directly.",
		PluginIntegrationTipZh: "启动后直接在本机浏览器打开 http://127.0.0.1:7861，即可选模型排队生成视频/图片并加载 Civitai / HF 权重！",
		BootstrapScript:        wangpBootstrapPy,
	},
	{
		ID:                 "comfyui",
		Name:               "ComfyUI Pro Node Studio",
		NameZh:             "ComfyUI 进阶节点工坊",
		Tagline:            "Real ComfyUI with 2026 Comfy-Org weights (Z-Image Turbo / Qwen-Image / FLUX.2) & custom HF / Civitai URLs",
		TaglineZh:          "完整原版 ComfyUI：预置 2026 顶流权重（Z-Image Turbo / Qwen-Image / FLUX.2）与任意 HF / Civitai 自定义直链",
		Description:        "Launches authentic ComfyUI on your chosen Colab GPU and account. Pick blank mode (0 GB preload), Comfy-Org Z-Image Turbo BF16 (10M+ downloads), Qwen-Image FP8 (2.8M+ downloads), FLUX.2 Dev FP8, or paste any Hugging Face / Civitai direct download URL—ColabHive downloads and verifies the real model file before declaring ready.",
		DescriptionZh:      "适合拖入社区 .json 工作流与硬核生图/生视频定制：在指定 Colab 显卡与账号上启动完整原版 ComfyUI，可选「空白极速启动」「Z-Image Turbo（千万下载）」「Qwen-Image FP8」「FLUX.2 Dev FP8」或任意 Hugging Face / Civitai 直链，启动阶段真实下载校验权重并自动备份输出。",
		Category:           models.AppCategoryImage,
		Icon:               "🎨",
		DefaultRemotePort:  8188,
		DefaultLocalPort:   8188,
		MinVRAMGB:          8,
		RecommendedGPU:     models.AcceleratorL4,
		FreeTierCompatible: true,
		EstBootSec:         45,
		Highlights: []string{
			"Up-to-date 2026 Comfy-Org official weights: Z-Image Turbo (10M+ DL), Qwen-Image FP8, FLUX.2 Dev FP8 + custom URLs",
			"Private 127.0.0.1:8188 localhost bridge works directly with browser & Krita AI Diffusion",
			"Auto-checkpoints /content/ComfyUI/output across sessions and account failovers",
		},
		HighlightsZh: []string{
			"2026 最新官方权重预设：Z-Image Turbo（千万下载）、Qwen-Image FP8、FLUX.2 Dev FP8 + 任意 HF / Civitai 直链",
			"本机 127.0.0.1:8188 私有直连免公网穿透，支持浏览器与 Krita AI Diffusion 插件直连",
			"自动备份 /content/ComfyUI/output 成品目录，掉线换号不丢图",
		},
		SupportsPortBridge:     true,
		SupportsCustomModelURL: true,
		Kind:                   models.PluginKindEngine,
		UI:                     &models.PluginUIConfig{Mode: "remote_proxy"},
		ModelOptions: []models.AppModelOption{
			{
				ID:             "blank",
				Name:           "Blank ComfyUI (0 GB preload · drop any .json workflow inside UI)",
				NameZh:         "空白 ComfyUI（0 GB 预载 · 进界面后拖入社区工作流）",
				Summary:        "Clean ComfyUI installation with zero preloaded weights for fastest startup",
				SummaryZh:      "纯净空白原版 ComfyUI，不预载权重，启动最快，进界面后按需加载任意社区工作流与模型",
				MinVRAMGB:      8,
				RecommendedGPU: models.AcceleratorT4,
				DownloadURL:    "",
			},
			{
				ID:             "z-image-turbo",
				Name:           "Z-Image Turbo BF16 (Full 3-Piece Bundle: UNet + CLIP + VAE · T4 / L4)",
				NameZh:         "Z-Image Turbo BF16（完整三件套：主模型 + CLIP + VAE · T4 / L4）",
				Summary:        "Downloads full Comfy-Org Z-Image Turbo bundle (UNet + qwen_3_4b CLIP + ae VAE) ready for 1-click Run",
				SummaryZh:      "自动拉齐 Comfy-Org 完整三件套（主模型 + qwen_3_4b 编码器 + ae VAE），进模板直接点运行出图",
				MinVRAMGB:      12,
				RecommendedGPU: models.AcceleratorT4,
				DownloadURL:    "https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/diffusion_models/z_image_turbo_bf16.safetensors",
				TargetSubdir:   "models/diffusion_models",
				Filename:       "z_image_turbo_bf16.safetensors",
			},
			{
				ID:             "qwen-image-fp8",
				Name:           "Qwen-Image FP8 (Full 3-Piece Bundle · Best CN/EN Text · L4 / A100)",
				NameZh:         "Qwen-Image FP8（完整三件套 · 中英文字排版王者 · L4 / A100）",
				Summary:        "Downloads full Comfy-Org Qwen-Image FP8 bundle (UNet + qwen_2.5_vl_7b CLIP + VAE) ready for 1-click Run",
				SummaryZh:      "自动拉齐 Comfy-Org 完整三件套（主模型 + qwen_2.5_vl_7b 编码器 + VAE），进模板直接点运行出图",
				MinVRAMGB:      16,
				RecommendedGPU: models.AcceleratorL4,
				DownloadURL:    "https://huggingface.co/Comfy-Org/Qwen-Image/resolve/main/split_files/diffusion_models/qwen_image_2512_fp8_e4m3fn.safetensors",
				TargetSubdir:   "models/diffusion_models",
				Filename:       "qwen_image_2512_fp8_e4m3fn.safetensors",
			},
			{
				ID:             "flux2-dev-fp8",
				Name:           "FLUX.2 Dev FP8 (Full 3-Piece Bundle · Studio Photoreal · 96GB G4 / A100)",
				NameZh:         "FLUX.2 Dev FP8（完整三件套 · 顶级写实画质 · 96GB G4 / A100）",
				Summary:        "Downloads full Comfy-Org FLUX.2 Dev FP8 bundle (UNet + mistral_3_small CLIP + flux2 VAE) ready for 1-click Run",
				SummaryZh:      "自动拉齐 Comfy-Org 完整三件套（主模型 + mistral_3_small 编码器 + flux2 VAE），进模板直接点运行出图",
				MinVRAMGB:      24,
				RecommendedGPU: models.AcceleratorG4,
				DownloadURL:    "https://huggingface.co/Comfy-Org/flux2-dev/resolve/main/split_files/diffusion_models/flux2_dev_fp8mixed.safetensors",
				TargetSubdir:   "models/diffusion_models",
				Filename:       "flux2_dev_fp8mixed.safetensors",
			},
		},
		PluginIntegrationTipEn: "Open http://127.0.0.1:8188 in your browser or connect Krita AI Diffusion directly with zero public tunnel.",
		PluginIntegrationTipZh: "启动后直接在浏览器打开 http://127.0.0.1:8188，拖入社区 .json 工作流或直连 Krita AI Diffusion！",
		BootstrapScript:        comfyUIBootstrapPy,
	},
	{
		ID:                 "unsloth-studio",
		Name:               "Unsloth Studio Fine-Tuning",
		NameZh:             "Unsloth Studio 零代码微调",
		Tagline:            "Zero-code visual LLM fine-tuning & GGUF export on H100 / 96GB G4 / A100 / L4",
		TaglineZh:          "拖入文档零代码微调专属 Qwen / Gemma / Llama 模型，一键导出 GGUF 带回本地",
		Description:        "Launches Unsloth's official visual fine-tuning studio (`unsloth studio`) bound privately to 127.0.0.1:8890. Drop your notes, docs, or datasets, fine-tune 2x faster with 70% less VRAM, chat-verify inside Unsloth Studio, and export GGUF weights with automatic checkpoint backup.",
		DescriptionZh:      "一键启动官方可视化微调工作台 Unsloth Studio：无需写训练脚本，拖入你的文章、笔记或业务文档自动洗成训练集，在 H100 / 96GB G4 / L4 上 2 倍速微调专属大模型，页面内直接对话验收并导出 GGUF 权重。",
		Category:           models.AppCategoryWorkspace,
		Icon:               "🦥",
		DefaultRemotePort:  8890,
		DefaultLocalPort:   8890,
		MinVRAMGB:          12,
		RecommendedGPU:     models.AcceleratorA100,
		FreeTierCompatible: true,
		EstBootSec:         45,
		Highlights: []string{
			"Official `unsloth studio` zero-code visual fine-tuning UI + dataset prep + GGUF export",
			"2x faster training with 70% lower VRAM on H100, 96GB G4, A100, L4, or Free T4",
			"Private 127.0.0.1:8890 bridge with auto-checkpointed /content/unsloth_outputs",
		},
		HighlightsZh: []string{
			"官方 Unsloth Studio 可视化界面：拖文档洗数据、点按钮开训、训完直接对话验收导 GGUF",
			"相比传统微调提速 2 倍、显存省 70%，H100 / 96GB G4 / L4 / 免费 T4 全适配",
			"本机 127.0.0.1:8890 私有直连，自动同步 /content/unsloth_outputs 权重与 LoRA 产物",
		},
		SupportsPortBridge: true,
		ModelOptions: []models.AppModelOption{
			{
				ID:             "default",
				Name:           "Unsloth Studio Latest (Visual Fine-Tuning + GGUF Export)",
				NameZh:         "Unsloth Studio 最新官方版（零代码微调 + 一键导 GGUF）",
				Summary:        "Installs official unsloth package and boots the Unsloth Studio web interface on 127.0.0.1:8890",
				SummaryZh:      "自动安装官方 unsloth 并在本机私有桥接端口 127.0.0.1:8890 拉起可视化微调工作台",
				MinVRAMGB:      12,
				RecommendedGPU: models.AcceleratorA100,
			},
		},
		PluginIntegrationTipEn: "Open http://127.0.0.1:8890 in your browser to upload documents, run LoRA/QLoRA fine-tuning, and export GGUF.",
		PluginIntegrationTipZh: "启动后直接在浏览器打开 http://127.0.0.1:8890，拖入文档即可零代码训练专属模型并导出 GGUF！",
		BootstrapScript:        unslothStudioBootstrapPy,
	},
	{
		ID:                 "jupyterlab",
		Name:               "JupyterLab Cloud Lab",
		NameZh:             "JupyterLab 云端实验室",
		Tagline:            "Real PyTorch & CUDA JupyterLab IDE with per-instance random token & XSRF protection",
		TaglineZh:          "真实 PyTorch + CUDA JupyterLab 编程实验室，独立随机令牌 + XSRF 防护与自动备份",
		Description:        "Full-featured JupyterLab workspace bound privately to 127.0.0.1 with per-instance random token authentication, XSRF protection enabled, PyTorch & CUDA ready, plus automatic `/content` workspace checkpointing.",
		DescriptionZh:      "适合科研实验、自定义脚本与数据分析：在指定 Colab 显卡上一键拉起完整 JupyterLab（每实例独立随机 Token 鉴权并保留 XSRF 防护），预装 PyTorch + CUDA，自动保护 `/content` 代码与实验结果。",
		Category:           models.AppCategoryWorkspace,
		Icon:               "📓",
		DefaultRemotePort:  8888,
		DefaultLocalPort:   8889,
		MinVRAMGB:          0,
		RecommendedGPU:     models.AcceleratorT4,
		FreeTierCompatible: true,
		EstBootSec:         15,
		Highlights: []string{
			"PyTorch, CUDA, Transformers & uv ready out of the box",
			"Per-instance random token authentication & full XSRF protection enabled",
			"Auto-checkpoints /content workspace across sessions & account failovers",
		},
		HighlightsZh: []string{
			"开箱自带 PyTorch / CUDA / Transformers 与极速 uv 包管理器",
			"每实例独立随机 Token 鉴权并完整保留 XSRF 防护，VS Code 可安全挂载",
			"自动同步 /content 工作目录，训练中途换号续跑不丢代码与断点",
		},
		SupportsPortBridge: true,
		Kind:               models.PluginKindEngine,
		UI:                 &models.PluginUIConfig{Mode: "remote_proxy"},
		ModelOptions: []models.AppModelOption{
			{
				ID:             "default",
				Name:           "PyTorch + CUDA Ready (Default Runtime)",
				NameZh:         "PyTorch + CUDA 默认科研环境（开箱即用）",
				Summary:        "Standard Colab PyTorch + CUDA notebook runtime with zero extra weight downloads",
				SummaryZh:      "标准 PyTorch + CUDA 科研环境，无需下载额外权重，极速启动",
				MinVRAMGB:      0,
				RecommendedGPU: models.AcceleratorT4,
			},
		},
		PluginIntegrationTipEn: "Attach VS Code Jupyter extension to http://127.0.0.1:8889 using the per-instance token saved in /content/.colabhive_jupyter_token.",
		PluginIntegrationTipZh: "VS Code 的 Jupyter 插件可直接连接本机 127.0.0.1:8889（使用 /content/.colabhive_jupyter_token 中的随机安全令牌）！",
		BootstrapScript:        jupyterLabBootstrapPy,
	},
}

// CuratedTemplates returns deep copies of the 5 real built-in 1-Click AI App Store core templates
// (`llm-chat`, `wangp`, `comfyui`, `unsloth-studio`, `jupyterlab`).
func CuratedTemplates() []models.AppTemplate {
	out := make([]models.AppTemplate, len(builtinTemplates))
	for i, t := range builtinTemplates {
		out[i] = cloneAppTemplate(t)
	}
	return out
}

// IsBuiltinTemplateID reports whether id matches one of the 3 built-in core app templates.
func IsBuiltinTemplateID(id string) bool {
	target := strings.ToLower(strings.TrimSpace(id))
	for _, t := range builtinTemplates {
		if t.ID == target {
			return true
		}
	}
	return false
}

// FindTemplate looks up a built-in core AppTemplate by ID (including BootstrapScript).
func FindTemplate(id string) (models.AppTemplate, bool) {
	target := strings.ToLower(strings.TrimSpace(id))
	for _, t := range builtinTemplates {
		if t.ID == target {
			return cloneAppTemplate(t), true
		}
	}
	return models.AppTemplate{}, false
}

func cloneAppTemplate(t models.AppTemplate) models.AppTemplate {
	cp := t
	cp.Highlights = append([]string(nil), t.Highlights...)
	cp.HighlightsZh = append([]string(nil), t.HighlightsZh...)
	cp.Provides = append([]string(nil), t.Provides...)
	cp.Consumes = append([]string(nil), t.Consumes...)
	cp.OptionalCapabilities = append([]string(nil), t.OptionalCapabilities...)
	cp.Permissions = append([]string(nil), t.Permissions...)
	cp.UI = clonePluginUIConfig(t.UI)
	if len(t.ModelOptions) > 0 {
		cp.ModelOptions = append([]models.AppModelOption(nil), t.ModelOptions...)
	}
	return cp
}

// pythonHTTPReadinessProbeHelper is embedded in every live bootstrap script to guarantee a real
// TCP + HTTP GET readiness check on 127.0.0.1:<port> before reporting the app ready.
const pythonHTTPReadinessProbeHelper = `
class _AppBootstrapFailure(Exception):
    pass

def _fail_bootstrap(msg):
    raise _AppBootstrapFailure(msg)

def _emit_step(pct, step, step_zh):
    import json as _ch_json
    try:
        print("[CH_STEP] " + _ch_json.dumps({"pct": int(pct), "step": str(step), "step_zh": str(step_zh)}, ensure_ascii=False), flush=True)
    except Exception:
        pass

def _read_log_tail(log_path, max_bytes=1200):
    try:
        with open(log_path, "rb") as fh:
            fh.seek(0, os.SEEK_END)
            size = fh.tell()
            fh.seek(max(0, size - max_bytes))
            return fh.read().decode("utf-8", errors="replace").strip()
    except Exception:
        return ""

def _wait_http_ready(proc, port, log_path, health_path="/", timeout_sec=75):
    import http.client, socket, time
    deadline = time.monotonic() + timeout_sec
    last_err = "no response yet"
    while time.monotonic() < deadline:
        rc = proc.poll()
        if rc is not None:
            tail = _read_log_tail(log_path)
            _fail_bootstrap(f"app process exited prematurely with code {rc}: {tail}")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=2.0):
                pass
            conn = http.client.HTTPConnection("127.0.0.1", port, timeout=3.0)
            try:
                conn.request("GET", health_path)
                resp = conn.getresponse()
                resp.read(256)
                if 100 <= resp.status < 500:
                    return resp.status
                last_err = f"HTTP {resp.status}"
            finally:
                conn.close()
        except Exception as exc:
            last_err = str(exc)
        time.sleep(0.5)
    tail = _read_log_tail(log_path)
    _fail_bootstrap(f"readiness probe failed on http://127.0.0.1:{port}{health_path} after {timeout_sec}s ({last_err}): {tail}")
`

const llmChatBootstrapPy = `import os, shutil, subprocess, sys
` + pythonHTTPReadinessProbeHelper + `
port = int(os.environ.get("COLABHIVE_REMOTE_PORT", "11434"))
ckpt_dir = os.environ.get("COLABHIVE_CHECKPOINT_DIR", "/content/checkpoints/llm-chat").strip() or "/content/checkpoints/llm-chat"
log_path = "/content/llm_chat.log"
os.makedirs(ckpt_dir, exist_ok=True)

if not shutil.which("ollama"):
    if not shutil.which("curl"):
        _fail_bootstrap("neither ollama nor curl is available in runtime")
    subprocess.run(["sh", "-c", "curl -fsSL https://ollama.com/install.sh | sh"], check=True)
if not shutil.which("ollama"):
    _fail_bootstrap("ollama binary not found after installation")

env = dict(os.environ, OLLAMA_HOST=f"127.0.0.1:{port}", OLLAMA_MODELS=os.path.join(ckpt_dir, "models"))
with open(log_path, "a", encoding="utf-8") as log_fh:
    proc = subprocess.Popen(
        ["ollama", "serve"],
        env=env, stdout=log_fh, stderr=subprocess.STDOUT,
    )
with open("/content/llm_chat.pid", "w", encoding="utf-8") as pid_fh:
    pid_fh.write(str(proc.pid))

status = _wait_http_ready(proc, port, log_path, health_path="/api/tags", timeout_sec=60)
print(f"[colabhive-appstore] llm-chat ready on 127.0.0.1:{port} (probe HTTP {status})")
`

const wangpBootstrapPy = `import ipaddress, os, posixpath, shutil, subprocess, sys, urllib.error, urllib.parse, urllib.request
` + pythonHTTPReadinessProbeHelper + `
port = int(os.environ.get("COLABHIVE_REMOTE_PORT", "7861"))
app_dir = "/content/WanGP"
ckpt_dir = os.environ.get("COLABHIVE_CHECKPOINT_DIR", "/content/WanGP/outputs").strip() or "/content/WanGP/outputs"
model_id = os.environ.get("COLABHIVE_MODEL_ID", "minimax-h3").strip() or "minimax-h3"
model_url = os.environ.get("COLABHIVE_MODEL_DOWNLOAD_URL", "").strip()
model_subdir = os.environ.get("COLABHIVE_MODEL_SUBDIR", "loras").strip() or "loras"
model_filename = os.environ.get("COLABHIVE_MODEL_FILENAME", "").strip()
log_path = "/content/wangp.log"

entry_py = os.path.join(app_dir, "wgp.py")
if not os.path.isfile(entry_py):
    _emit_step(50, "Cloning WanGP repository from GitHub...", "正在从 GitHub 克隆 WanGP 源码仓库...")
    if not shutil.which("git"):
        _fail_bootstrap("git is required to clone WanGP")
    if os.path.isdir(app_dir) and not os.listdir(app_dir):
        os.rmdir(app_dir)
    if not os.path.isdir(app_dir):
        subprocess.run(
            ["git", "clone", "--depth", "1", "https://github.com/deepbeepmeep/WanGP.git", app_dir],
            check=True,
        )
    else:
        tmp_clone = "/content/.wangp_clone_tmp"
        if os.path.exists(tmp_clone):
            shutil.rmtree(tmp_clone)
        try:
            subprocess.run(
                ["git", "clone", "--depth", "1", "https://github.com/deepbeepmeep/WanGP.git", tmp_clone],
                check=True,
            )
            for _entry in os.listdir(tmp_clone):
                _src = os.path.join(tmp_clone, _entry)
                _dst = os.path.join(app_dir, _entry)
                if os.path.isdir(_src):
                    if os.path.isdir(_dst):
                        for _root, _dirs, _files in os.walk(_src):
                            _rel = os.path.relpath(_root, _src)
                            _target_dir = _dst if _rel == "." else os.path.join(_dst, _rel)
                            os.makedirs(_target_dir, exist_ok=True)
                            for _f in _files:
                                _f_src = os.path.join(_root, _f)
                                _f_dst = os.path.join(_target_dir, _f)
                                if not os.path.exists(_f_dst):
                                    shutil.copy2(_f_src, _f_dst)
                    else:
                        shutil.move(_src, _dst)
                elif not os.path.exists(_dst):
                    shutil.move(_src, _dst)
        finally:
            if os.path.exists(tmp_clone):
                shutil.rmtree(tmp_clone, ignore_errors=True)

if not os.path.isfile(entry_py):
    _fail_bootstrap(f"WanGP entrypoint not found at {entry_py}")

deps_marker = "/tmp/.colabhive_wangp_deps_ok"
req_txt = os.path.join(app_dir, "requirements.txt")
if os.path.isfile(req_txt) and not os.path.isfile(deps_marker):
    _emit_step(58, "Installing WanGP Python dependencies (requirements.txt)...", "正在安装 WanGP Python 依赖包（首次约 1~3 分钟）...")
    subprocess.run([sys.executable, "-m", "pip", "install", "--progress-bar", "off", "-r", req_txt], check=True)
    with open(deps_marker, "w", encoding="utf-8") as _dm:
        _dm.write("ok\n")

os.makedirs(ckpt_dir, exist_ok=True)

if model_url:
    parsed = urllib.parse.urlparse(model_url)
    host = (parsed.hostname or "").lower()
    if parsed.scheme != "https" or host not in ("huggingface.co", "civitai.com"):
        _fail_bootstrap(f"unsupported model download URL host: {model_url}")
    norm_sub = posixpath.normpath(model_subdir.replace("\\", "/")).lstrip("/")
    if not norm_sub or norm_sub.startswith("..") or "/../" in f"/{norm_sub}/":
        norm_sub = "loras"
    fname = model_filename or os.path.basename(parsed.path) or "custom_weight.safetensors"
    if "/" in fname or "\\" in fname or ".." in fname:
        _fail_bootstrap(f"invalid model filename: {fname}")
    dest_dir = os.path.join(app_dir, norm_sub)
    os.makedirs(dest_dir, exist_ok=True)
    dest_path = os.path.join(dest_dir, fname)
    if not (os.path.isfile(dest_path) and os.path.getsize(dest_path) > 0):
        class _SafeRedirect(urllib.request.HTTPRedirectHandler):
            def redirect_request(self, req, fp, code, msg, headers, newurl):
                next_req = super().redirect_request(req, fp, code, msg, headers, newurl)
                if next_req is None:
                    return None
                p = urllib.parse.urlparse(next_req.full_url)
                nh = (p.hostname or "").lower()
                if p.scheme != "https" or not nh or nh in ("localhost", "127.0.0.1", "::1"):
                    raise _AppBootstrapFailure(f"blocked redirect destination: {next_req.full_url}")
                try:
                    ip = ipaddress.ip_address(nh)
                    if ip.is_private or ip.is_loopback or ip.is_link_local or ip.is_reserved:
                        raise _AppBootstrapFailure(f"blocked private-IP redirect: {next_req.full_url}")
                except ValueError:
                    pass
                if not (p.scheme == "https" and nh == "huggingface.co" and (p.port is None or p.port == 443)):
                    next_req.remove_header("Authorization")
                    next_req.unredirected_hdrs.pop("Authorization", None)
                return next_req
        opener = urllib.request.build_opener(_SafeRedirect())
        headers = {"User-Agent": "ColabHive/1.1"}
        hf_tok = os.environ.get("HF_TOKEN", "").strip()
        if hf_tok and host == "huggingface.co":
            headers["Authorization"] = f"Bearer {hf_tok}"
        tmp_path = dest_path + ".downloading"
        try:
            with opener.open(urllib.request.Request(model_url, headers=headers), timeout=600) as resp:
                if getattr(resp, "status", resp.getcode()) != 200:
                    _fail_bootstrap(f"model download HTTP {getattr(resp, 'status', resp.getcode())}")
                with open(tmp_path, "wb") as out_fh:
                    while True:
                        chunk = resp.read(1024 * 1024)
                        if not chunk:
                            break
                        out_fh.write(chunk)
            os.replace(tmp_path, dest_path)
        except Exception as exc:
            if os.path.exists(tmp_path):
                os.remove(tmp_path)
            _fail_bootstrap(f"custom weight download failed: {exc}")

launch_mode = os.environ.get("COLABHIVE_LAUNCH_MODE", "headless").strip().lower() or "headless"
env = dict(
    os.environ,
    SERVER_NAME="127.0.0.1",
    SERVER_PORT=str(port),
    GRADIO_SERVER_NAME="127.0.0.1",
    GRADIO_SERVER_PORT=str(port),
    COLABHIVE_WANGP_DIR=app_dir,
    COLABHIVE_WANGP_PROFILE=model_id,
    COLABHIVE_CHECKPOINT_DIR=ckpt_dir,
    COLABHIVE_REMOTE_PORT=str(port),
)

if launch_mode == "headless":
    import base64 as _ch_b64
    worker_py = os.path.join(app_dir, "colabhive_media_worker.py")
    with open(worker_py, "wb") as _wfh:
        _wfh.write(_ch_b64.b64decode("__COLABHIVE_WANGP_WORKER_B64__"))
    _emit_step(85, f"Starting WanGP Headless media.v1 engine on 127.0.0.1:{port}...", f"依赖安装完成，正在启动 WanGP 无头算力引擎（media.v1 · 端口 {port}）...")
    with open(log_path, "a", encoding="utf-8") as log_fh:
        proc = subprocess.Popen(
            [sys.executable, worker_py],
            cwd=app_dir, env=env, stdin=subprocess.DEVNULL, stdout=log_fh, stderr=subprocess.STDOUT, start_new_session=True,
        )
    with open("/content/wangp.pid", "w", encoding="utf-8") as pid_fh:
        pid_fh.write(str(proc.pid))
    _emit_step(92, "Initializing WanGP shared.api session (media.v1)...", "正在初始化 WanGP 算力引擎内核（首次约 10~20 秒）...")
    status = _wait_http_ready(proc, port, log_path, health_path="/colabhive/v1/health", timeout_sec=180)
    _emit_step(98, f"WanGP media.v1 engine ready (HTTP {status})", f"WanGP 算力引擎已就绪（HTTP {status}），可在本地原生创作台 0ms 秒开！")
    print(f"[colabhive-appstore] wangp headless media.v1 ready on 127.0.0.1:{port} (profile={model_id}; probe HTTP {status})", flush=True)
else:
    _emit_step(85, f"Starting WanGP Gradio web server on 127.0.0.1:{port}...", f"依赖安装完成，正在启动 WanGP 原生 Gradio 网页服务（端口 {port}）...")
    with open(log_path, "a", encoding="utf-8") as log_fh:
        proc = subprocess.Popen(
            [sys.executable, entry_py, "--server-port", str(port)],
            cwd=app_dir, env=env, stdin=subprocess.DEVNULL, stdout=log_fh, stderr=subprocess.STDOUT, start_new_session=True,
        )
    with open("/content/wangp.pid", "w", encoding="utf-8") as pid_fh:
        pid_fh.write(str(proc.pid))
    _emit_step(92, "Waiting for WanGP Gradio UI HTTP readiness...", "正在等待 WanGP 网页界面完成初始化...")
    status = _wait_http_ready(proc, port, log_path, health_path="/", timeout_sec=180)
    _emit_step(98, f"WanGP ready (HTTP {status})", f"WanGP 服务已就绪（HTTP {status}），正在建立本机私有直连...")
    print(f"[colabhive-appstore] wangp ready on 127.0.0.1:{port} (profile={model_id}; probe HTTP {status})", flush=True)
`

const unslothStudioBootstrapPy = `import glob, os, shutil, subprocess, sys
` + pythonHTTPReadinessProbeHelper + `
port = int(os.environ.get("COLABHIVE_REMOTE_PORT", "8890"))
ckpt_dir = os.environ.get("COLABHIVE_CHECKPOINT_DIR", "/content/unsloth_outputs").strip() or "/content/unsloth_outputs"
log_path = "/content/unsloth_studio.log"
os.makedirs(ckpt_dir, exist_ok=True)

home_dir = os.path.expanduser("~")
studio_home = os.path.join(home_dir, ".unsloth", "studio")
deps_marker = "/tmp/.colabhive_unsloth_studio_ok"

def _find_unsloth_bin():
    candidates = [
        os.path.join(home_dir, ".local", "bin", "unsloth"),
        os.path.join(studio_home, "unsloth_studio", "bin", "unsloth"),
        os.path.join(studio_home, "venv", "bin", "unsloth"),
    ]
    candidates.extend(glob.glob(os.path.join(studio_home, "*", "bin", "unsloth")))
    for c in candidates:
        if os.path.isfile(c) and os.access(c, os.X_OK):
            return c
    return shutil.which("unsloth")

if not os.path.isfile(deps_marker) or not os.path.isdir(studio_home):
    _emit_step(58, "Installing Unsloth Studio environment...", "正在云端安装 Unsloth Studio 微调环境...")
    inst_env = dict(os.environ, UNSLOTH_SKIP_AUTOSTART="1", NO_COLOR="1")
    subprocess.run(
        "curl -fsSL https://unsloth.ai/install.sh | sh",
        shell=True,
        env=inst_env,
        check=True,
    )
    with open(deps_marker, "w", encoding="utf-8") as _dm:
        _dm.write("ok\n")

unsloth_bin = _find_unsloth_bin()
if unsloth_bin:
    cmd = [unsloth_bin, "studio", "-H", "127.0.0.1", "-p", str(port)]
else:
    cmd = ["unsloth", "studio", "-H", "127.0.0.1", "-p", str(port)]

env = dict(
    os.environ,
    UNSLOTH_STUDIO_OUTPUT_DIR=ckpt_dir,
    PATH=f"{os.path.join(home_dir, '.local', 'bin')}:{os.environ.get('PATH', '')}",
)
_emit_step(85, f"Starting Unsloth Studio on 127.0.0.1:{port}...", f"正在启动 Unsloth Studio 服务（端口 {port}）...")
with open(log_path, "a", encoding="utf-8") as log_fh:
    proc = subprocess.Popen(
        cmd, cwd="/content", env=env, stdin=subprocess.DEVNULL, stdout=log_fh, stderr=subprocess.STDOUT, start_new_session=True,
    )
with open("/content/unsloth_studio.pid", "w", encoding="utf-8") as pid_fh:
    pid_fh.write(str(proc.pid))

status = _wait_http_ready(proc, port, log_path, health_path="/", timeout_sec=150)
_emit_step(98, f"Unsloth Studio ready (HTTP {status})", f"Unsloth Studio 已就绪（HTTP {status}）")
print(f"[colabhive-appstore] unsloth-studio ready on 127.0.0.1:{port} (probe HTTP {status})", flush=True)
`

const comfyUIBootstrapPy = `import ipaddress, os, posixpath, shutil, subprocess, sys, urllib.error, urllib.parse, urllib.request
` + pythonHTTPReadinessProbeHelper + `
port = int(os.environ.get("COLABHIVE_REMOTE_PORT", "8188"))
app_dir = "/content/ComfyUI"
ckpt_dir = os.environ.get("COLABHIVE_CHECKPOINT_DIR", "/content/ComfyUI/output").strip() or "/content/ComfyUI/output"
model_id = os.environ.get("COLABHIVE_MODEL_ID", "blank").strip() or "blank"
model_url = os.environ.get("COLABHIVE_MODEL_DOWNLOAD_URL", "").strip()
model_subdir = os.environ.get("COLABHIVE_MODEL_SUBDIR", "models/checkpoints").strip() or "models/checkpoints"
model_filename = os.environ.get("COLABHIVE_MODEL_FILENAME", "").strip()
log_path = "/content/comfyui.log"

main_py = os.path.join(app_dir, "main.py")
if not os.path.isfile(main_py):
    _emit_step(50, "Cloning ComfyUI repository from GitHub...", "正在从 GitHub 克隆 ComfyUI 源码仓库...")
    if not shutil.which("git"):
        _fail_bootstrap("git is required to clone ComfyUI")
    if os.path.isdir(app_dir) and not os.listdir(app_dir):
        os.rmdir(app_dir)
    if not os.path.isdir(app_dir):
        subprocess.run(
            ["git", "clone", "--depth", "1", "https://github.com/comfyanonymous/ComfyUI.git", app_dir],
            check=True,
        )
    else:
        # Restored checkpoint output directory may already exist inside /content/ComfyUI:
        # clone into a temporary directory and merge into app_dir WITHOUT overwriting restored output files!
        tmp_clone = "/content/.comfyui_clone_tmp"
        if os.path.exists(tmp_clone):
            shutil.rmtree(tmp_clone)
        try:
            subprocess.run(
                ["git", "clone", "--depth", "1", "https://github.com/comfyanonymous/ComfyUI.git", tmp_clone],
                check=True,
            )
            for _entry in os.listdir(tmp_clone):
                _src = os.path.join(tmp_clone, _entry)
                _dst = os.path.join(app_dir, _entry)
                if os.path.isdir(_src):
                    if os.path.isdir(_dst):
                        for _root, _dirs, _files in os.walk(_src):
                            _rel = os.path.relpath(_root, _src)
                            _target_dir = _dst if _rel == "." else os.path.join(_dst, _rel)
                            os.makedirs(_target_dir, exist_ok=True)
                            for _f in _files:
                                _f_src = os.path.join(_root, _f)
                                _f_dst = os.path.join(_target_dir, _f)
                                if not os.path.exists(_f_dst):
                                    shutil.copy2(_f_src, _f_dst)
                    else:
                        shutil.move(_src, _dst)
                elif not os.path.exists(_dst):
                    shutil.move(_src, _dst)
        finally:
            if os.path.exists(tmp_clone):
                shutil.rmtree(tmp_clone, ignore_errors=True)

if not os.path.isfile(main_py):
    _fail_bootstrap(f"ComfyUI entrypoint not found at {main_py}")

# Always ensure requirements.txt is installed once per fresh VM (even if a restored snapshot already included main.py)!
deps_marker = "/tmp/.colabhive_comfyui_deps_ok"
req_txt = os.path.join(app_dir, "requirements.txt")
if os.path.isfile(req_txt) and not os.path.isfile(deps_marker):
    _emit_step(60, "Installing ComfyUI Python dependencies...", "正在安装 ComfyUI Python 依赖包...")
    subprocess.run([sys.executable, "-m", "pip", "install", "--progress-bar", "off", "-r", req_txt], check=True)
    with open(deps_marker, "w", encoding="utf-8") as _dm:
        _dm.write("ok\n")

os.makedirs(ckpt_dir, exist_ok=True)

class _SafeModelRedirectHandler(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        next_req = super().redirect_request(req, fp, code, msg, headers, newurl)
        if next_req is None:
            return None
        parsed_next = urllib.parse.urlparse(next_req.full_url)
        next_host = (parsed_next.hostname or "").lower()
        next_port = parsed_next.port
        if parsed_next.scheme != "https" or not next_host:
            raise _AppBootstrapFailure(f"blocked insecure redirect destination: {next_req.full_url}")
        if next_host in ("localhost", "127.0.0.1", "::1"):
            raise _AppBootstrapFailure(f"blocked loopback redirect destination: {next_req.full_url}")
        try:
            ip = ipaddress.ip_address(next_host)
            if ip.is_private or ip.is_loopback or ip.is_link_local or ip.is_reserved:
                raise _AppBootstrapFailure(f"blocked private-IP redirect destination: {next_req.full_url}")
        except ValueError:
            pass
        # Keep HF_TOKEN strictly on the exact original HTTPS origin (https://huggingface.co on default port 443)!
        if not (parsed_next.scheme == "https" and next_host == "huggingface.co" and (next_port is None or next_port == 443)):
            next_req.remove_header("Authorization")
            next_req.remove_header("authorization")
            next_req.unredirected_hdrs.pop("Authorization", None)
            next_req.unredirected_hdrs.pop("authorization", None)
        return next_req

_safe_model_opener = urllib.request.build_opener(_SafeModelRedirectHandler())

def _provision_model_weight(url_str, subdir, fname):
    parsed = urllib.parse.urlparse(url_str)
    host = (parsed.hostname or "").lower()
    if parsed.scheme != "https" or host not in ("huggingface.co", "civitai.com"):
        _fail_bootstrap(f"unsupported model download URL host: {url_str}")
    norm_sub = posixpath.normpath(subdir.replace("\\", "/")).lstrip("/")
    if not norm_sub or norm_sub.startswith("..") or "/../" in f"/{norm_sub}/":
        _fail_bootstrap(f"invalid target_subdir: {subdir}")
    if not fname:
        fname = os.path.basename(parsed.path) or "custom_model.safetensors"
    if "/" in fname or "\\" in fname or ".." in fname or fname in (".", ".."):
        _fail_bootstrap(f"invalid model filename: {fname}")

    dest_dir = os.path.join(app_dir, norm_sub)
    os.makedirs(dest_dir, exist_ok=True)
    dest_path = os.path.join(dest_dir, fname)
    tmp_path = dest_path + ".downloading"

    if os.path.isfile(dest_path) and os.path.getsize(dest_path) > 0:
        print(f"[colabhive-appstore] model already present: {dest_path} ({os.path.getsize(dest_path)} bytes)")
        return dest_path

    headers = {"User-Agent": "ColabHive/1.1"}
    hf_tok = os.environ.get("HF_TOKEN", "").strip()
    if hf_tok and host == "huggingface.co":
        headers["Authorization"] = f"Bearer {hf_tok}"

    req = urllib.request.Request(url_str, headers=headers)
    total_written = 0
    expected_len = None
    try:
        with _safe_model_opener.open(req, timeout=600) as resp:
            status_code = getattr(resp, "status", resp.getcode())
            if status_code != 200:
                _fail_bootstrap(f"model download failed with HTTP {status_code} for {url_str}")
            cl_hdr = resp.headers.get("Content-Length")
            if cl_hdr and cl_hdr.isdigit():
                expected_len = int(cl_hdr)
                free_bytes = __import__("shutil").disk_usage(dest_dir).free
                if free_bytes < expected_len + 1024 * 1024 * 1024:
                    _fail_bootstrap(f"insufficient Colab disk space for {fname}: need {(expected_len/1e9):.1f} GB + 1 GB headroom, only {(free_bytes/1e9):.1f} GB free")
            _emit_step(68, f"Downloading model weights ({fname})...", f"正在下载模型权重 {fname}...")
            _dl_t0 = __import__("time").monotonic()
            _dl_last = _dl_t0
            with open(tmp_path, "wb") as out_fh:
                while True:
                    chunk = resp.read(1024 * 1024)
                    if not chunk:
                        break
                    out_fh.write(chunk)
                    total_written += len(chunk)
                    _now = __import__("time").monotonic()
                    if _now - _dl_last >= 2.0:
                        _dl_last = _now
                        _mbps = (total_written / 1048576.0) / max(0.2, _now - _dl_t0)
                        if expected_len and expected_len > 0:
                            _dpct = min(99, int(total_written * 100 / expected_len))
                            _pct_bar = 68 + int(_dpct * 0.20)
                            _emit_step(_pct_bar, f"Downloading {fname}: {_dpct}% ({total_written/1e9:.2f}/{expected_len/1e9:.2f} GB · {_mbps:.0f} MB/s)", f"正在下载权重 {fname}：{_dpct}% ({total_written/1e9:.2f}/{expected_len/1e9:.2f} GB · {_mbps:.0f} MB/s)")
                        else:
                            _emit_step(76, f"Downloading {fname}: {total_written/1e9:.2f} GB ({_mbps:.0f} MB/s)", f"正在下载权重 {fname}：{total_written/1e9:.2f} GB ({_mbps:.0f} MB/s)")
    except _AppBootstrapFailure:
        if os.path.exists(tmp_path):
            os.remove(tmp_path)
        raise
    except urllib.error.HTTPError as he:
        if os.path.exists(tmp_path):
            os.remove(tmp_path)
        _fail_bootstrap(f"model download failed with HTTP {he.code} ({he.reason}) for {url_str}")
    except Exception as exc:
        if os.path.exists(tmp_path):
            os.remove(tmp_path)
        _fail_bootstrap(f"model download failed for {url_str}: {exc}")

    if total_written <= 0:
        if os.path.exists(tmp_path):
            os.remove(tmp_path)
        _fail_bootstrap(f"model download produced empty file (0 bytes) for {url_str}")
    if expected_len is not None and expected_len > 0 and total_written != expected_len:
        if os.path.exists(tmp_path):
            os.remove(tmp_path)
        _fail_bootstrap(f"model download truncated: got {total_written} of {expected_len} bytes for {url_str}")

    os.replace(tmp_path, dest_path)
    final_size = os.path.getsize(dest_path) if os.path.isfile(dest_path) else 0
    if final_size <= 0:
        _fail_bootstrap(f"model file missing or empty after download at {dest_path}")
    print(f"[colabhive-appstore] provisioned model {dest_path} ({final_size} bytes)")
    return dest_path

_COMPANION_BUNDLES = {
    "z-image-turbo": [
        ("https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/text_encoders/qwen_3_4b.safetensors", "models/text_encoders", "qwen_3_4b.safetensors"),
        ("https://huggingface.co/Comfy-Org/z_image_turbo/resolve/main/split_files/vae/ae.safetensors", "models/vae", "ae.safetensors"),
    ],
    "qwen-image-fp8": [
        ("https://huggingface.co/Comfy-Org/Qwen-Image/resolve/main/split_files/text_encoders/qwen_2.5_vl_7b_fp8_scaled.safetensors", "models/text_encoders", "qwen_2.5_vl_7b_fp8_scaled.safetensors"),
        ("https://huggingface.co/Comfy-Org/Qwen-Image/resolve/main/split_files/vae/qwen_image_vae.safetensors", "models/vae", "qwen_image_vae.safetensors"),
    ],
    "flux2-dev-fp8": [
        ("https://huggingface.co/Comfy-Org/flux2-dev/resolve/main/split_files/text_encoders/mistral_3_small_flux2_fp8.safetensors", "models/text_encoders", "mistral_3_small_flux2_fp8.safetensors"),
        ("https://huggingface.co/Comfy-Org/flux2-dev/resolve/main/split_files/vae/flux2-vae.safetensors", "models/vae", "flux2-vae.safetensors"),
    ],
}

provisioned_note = "blank (0 GB preloaded weights)"
if model_url:
    weight_path = _provision_model_weight(model_url, model_subdir, model_filename)
    for _comp_url, _comp_sub, _comp_fn in _COMPANION_BUNDLES.get(model_id, []):
        _provision_model_weight(_comp_url, _comp_sub, _comp_fn)
    provisioned_note = f"model={model_id} path={weight_path}"
else:
    print("[colabhive-appstore] comfyui blank mode (0 GB preloaded weights)")

_emit_step(90, f"Starting ComfyUI server on 127.0.0.1:{port}...", f"正在启动 ComfyUI 节点工坊服务（端口 {port}）...")
with open(log_path, "a", encoding="utf-8") as log_fh:
    proc = subprocess.Popen(
        [sys.executable, main_py, "--listen", "127.0.0.1", "--port", str(port), "--output-directory", ckpt_dir],
        cwd=app_dir, stdin=subprocess.DEVNULL, stdout=log_fh, stderr=subprocess.STDOUT, start_new_session=True,
    )
with open("/content/comfyui.pid", "w", encoding="utf-8") as pid_fh:
    pid_fh.write(str(proc.pid))

status = _wait_http_ready(proc, port, log_path, health_path="/system_stats", timeout_sec=120)
_emit_step(98, f"ComfyUI ready (HTTP {status})", f"ComfyUI 服务已就绪（HTTP {status}）")
print(f"[colabhive-appstore] comfyui ready on 127.0.0.1:{port} ({provisioned_note}; probe HTTP {status})", flush=True)
`

const jupyterLabBootstrapPy = `import os, secrets, shutil, subprocess, sys
` + pythonHTTPReadinessProbeHelper + `
port = int(os.environ.get("COLABHIVE_REMOTE_PORT", "8888"))
log_path = "/content/jupyterlab_app.log"
token_path = "/content/.colabhive_jupyter_token"
os.makedirs("/content", exist_ok=True)

if not shutil.which("jupyter-lab"):
    _emit_step(58, "Installing JupyterLab...", "正在安装 JupyterLab 运行环境...")
    subprocess.run([sys.executable, "-m", "pip", "install", "--progress-bar", "off", "jupyterlab"], check=True)
if not shutil.which("jupyter-lab") and not shutil.which("jupyter"):
    _fail_bootstrap("jupyter-lab binary not found after installation")

token = secrets.token_urlsafe(24)
fd = os.open(token_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as tfh:
    tfh.write(token + "\n")

env = dict(os.environ, JUPYTER_TOKEN=token)
jup_cmd = [shutil.which("jupyter-lab") or "jupyter-lab"] if shutil.which("jupyter-lab") else ["jupyter", "lab"]
_emit_step(85, f"Starting JupyterLab on 127.0.0.1:{port}...", f"正在启动 JupyterLab 服务（端口 {port}）...")
with open(log_path, "a", encoding="utf-8") as log_fh:
    proc = subprocess.Popen(
        jup_cmd + [
            "--ip=127.0.0.1",
            f"--port={port}",
            "--no-browser",
            "--allow-root",
            f"--ServerApp.token={token}",
        ],
        cwd="/content",
        env=env,
        stdin=subprocess.DEVNULL,
        stdout=log_fh,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
with open("/content/jupyterlab_app.pid", "w", encoding="utf-8") as pid_fh:
    pid_fh.write(str(proc.pid))

status = _wait_http_ready(proc, port, log_path, health_path="/api", timeout_sec=60)
_emit_step(98, f"JupyterLab ready (HTTP {status})", f"JupyterLab 已就绪（HTTP {status}）")
print(f"[colabhive-appstore] jupyterlab ready on 127.0.0.1:{port} (probe HTTP {status}; XSRF & token auth enabled)", flush=True)
`

// customGitHubBootstrapPy runs declarative community JSON plugins installed via colabhive-app.json.
const customGitHubBootstrapPy = `import os, shlex, shutil, subprocess, sys
` + pythonHTTPReadinessProbeHelper + `
repo_url = os.environ.get("COLABHIVE_CUSTOM_REPO_URL", "").strip()
custom_cmd = os.environ.get("COLABHIVE_CUSTOM_CMD", "").strip()
port = int(os.environ.get("COLABHIVE_REMOTE_PORT", "7860"))
ckpt_dir = os.environ.get("COLABHIVE_CHECKPOINT_DIR", "/content/checkpoints").strip() or "/content/checkpoints"
health_path = os.environ.get("COLABHIVE_HEALTH_PATH", "/").strip() or "/"
log_path = "/content/custom_app.log"
os.makedirs(ckpt_dir, exist_ok=True)

target_dir = "/content/custom_app"
if repo_url:
    if not repo_url.startswith("https://github.com/"):
        _fail_bootstrap(f"unsupported repo_url: {repo_url}")
    if not os.path.isdir(target_dir):
        _emit_step(52, f"Cloning plugin repository {repo_url}...", "正在克隆插件 GitHub 仓库...")
        if not shutil.which("git"):
            _fail_bootstrap("git is required to clone plugin repository")
        subprocess.run(
            ["git", "clone", "--depth", "1", "--", repo_url, target_dir],
            check=True,
        )
    req_txt = os.path.join(target_dir, "requirements.txt")
    if os.path.isfile(req_txt):
        _emit_step(65, "Installing plugin Python dependencies...", "正在安装插件 Python 依赖...")
        subprocess.run([sys.executable, "-m", "pip", "install", "--progress-bar", "off", "-r", req_txt], check=True)
    if not custom_cmd:
        for cand in ("app.py", "webui.py", "main.py"):
            if os.path.isfile(os.path.join(target_dir, cand)):
                custom_cmd = f"python3 {cand}"
                break

if not custom_cmd:
    _fail_bootstrap("no start_command supplied and no app.py/webui.py/main.py entrypoint discovered")

argv = shlex.split(custom_cmd)
run_cwd = target_dir if os.path.isdir(target_dir) else "/content"
env = dict(
    os.environ,
    GRADIO_SERVER_NAME="127.0.0.1",
    GRADIO_SERVER_PORT=str(port),
    STREAMLIT_SERVER_ADDRESS="127.0.0.1",
    STREAMLIT_SERVER_PORT=str(port),
    PORT=str(port),
    COLABHIVE_CHECKPOINT_DIR=ckpt_dir,
)
with open(log_path, "a", encoding="utf-8") as log_fh:
    proc = subprocess.Popen(
        argv, shell=False, cwd=run_cwd, env=env,
        stdin=subprocess.DEVNULL, stdout=log_fh, stderr=subprocess.STDOUT, start_new_session=True,
    )
with open("/content/custom_app.pid", "w", encoding="utf-8") as pid_fh:
    pid_fh.write(str(proc.pid))

status = _wait_http_ready(proc, port, log_path, health_path=health_path, timeout_sec=90)
print(f"[colabhive-appstore] plugin app ready on 127.0.0.1:{port} (probe HTTP {status}) repo={repo_url}")
`
