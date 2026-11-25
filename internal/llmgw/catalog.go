// Package llmgw implements ColabHive's 1-Click Uncensored / Open-Weight LLM Deployer
// and local OpenAI (`/v1/*`), Anthropic (`/v1/messages`), and Ollama (`/api/*`) gateway
// backed by pooled Google Colab GPUs with automatic cross-account preemption failover
// and hot-reloadable JSON model catalogs.
package llmgw

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/colabhive/colabhive/internal/models"
)

// DefaultModelID is auto-selected when a caller omits model_id or invokes `/v1/chat/completions` cold.
const DefaultModelID = "qwen3.5-9b-heretic"

const maxCustomModelTagLen = 180
const maxExternalCatalogBytes = 1 << 20 // 1 MiB cap for hot-reloaded llm_catalog.json

var (
	// validModelCharsetRe strictly enforces `^[a-zA-Z0-9._:/-]+$` while also requiring a leading
	// alphanumeric character so CLI flag injection (`-flag`) and shell metacharacters are impossible.
	validModelCharsetRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]*$`)

	// paramSizeBillionsRe extracts parameter size hints like `8b`, `9b`, `14b`, `27b`, `31b`, `35b`, `70b`, `123b`, `8x7b`, `8x22b`.
	paramSizeBillionsRe = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:(\d+)x)?(\d+(?:\.\d+)?)b(?:$|[^a-z0-9])`)
)

// curatedCatalog holds the verified November 2026 highest-downloaded community uncensored
// (Heretic / Abliterated / GSQ-RCO orthogonal refusal-ablation) open-weight GGUF lineup on Hugging Face.
// Can be overridden or extended at runtime via `<dataDir>/llm_catalog.json` or `COLABHIVE_LLM_CATALOG_PATH`.
var curatedCatalog = []models.LLMModelSpec{
	{
		ID:              "qwen3.5-9b-heretic",
		DisplayName:     "Qwen3.5 9B Abliterated · Fast Free-GPU Uncensored",
		DisplayNameZh:   "Qwen3.5 9B Abliterated · 免费卡极速无审查",
		ProviderFamily:  "Qwen",
		Summary:         "Top-downloaded 9B orthogonal-abliterated Qwen3.5 GGUF (511K+ HF downloads, ~5.7 GB) — zero refusal, crisp bilingual writing & fast tool calling at $0 on Free Colab T4.",
		SummaryZh:       "Hugging Face 51 万+下载口碑小钢炮（约 5.7 GB），正交消融去审查保留原版智商，中英双语、日常创作与指令遵循零拒答，免费 Colab T4 零成本秒跑。",
		ParameterSize:   "9B",
		Quantization:    "Q4_K_M",
		ContextLength:   32768,
		MinVRAMGB:       8,
		RecommendedGPU:  models.AcceleratorT4,
		FreeTierReady:   true,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       "hf.co/mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF:Q4_K_M",
		HuggingFaceRepo: "mradermacher/Huihui-Qwen3.5-9B-abliterated-GGUF",
		Tags:            []string{"uncensored", "abliterated", "free-gpu", "fast"},
	},
	{
		ID:              "gemma4-12b-heretic",
		DisplayName:     "Gemma 4 12B Heretic · Free-GPU Writing & Roleplay",
		DisplayNameZh:   "Gemma 4 12B Heretic · 免费卡文笔与酒馆首选",
		ProviderFamily:  "Gemma",
		Summary:         "Google Gemma 4 12B Heretic abliterated GGUF (127K+ HF downloads, 6.87 GB Q4_K_M) — natural literary prose, strong roleplay & zero lecturing right on Free 16GB Colab T4.",
		SummaryZh:       "Google 最新 Gemma 4 12B 正交 Heretic 去审查版（HF 12.7 万+下载、仅 6.87 GB），文笔细腻无说教，免费 16GB Colab T4 即可直接跑起酒馆角色扮演。",
		ParameterSize:   "12B",
		Quantization:    "Q4_K_M",
		ContextLength:   65536,
		MinVRAMGB:       10,
		RecommendedGPU:  models.AcceleratorT4,
		FreeTierReady:   true,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       "hf.co/culturerevolt/gemma-4-12b-heretic-abliterated-GGUF:Q4_K_M",
		HuggingFaceRepo: "culturerevolt/gemma-4-12b-heretic-abliterated-GGUF",
		Tags:            []string{"uncensored", "heretic", "free-gpu", "creative-rp"},
	},
	{
		ID:              "qwen3.8-27b-abliterated",
		DisplayName:     "Qwen3.8 27B Abliterated · 4.5M-DL Community King",
		DisplayNameZh:   "Qwen3.8 27B Abliterated · 450万下载社区口碑王",
		ProviderFamily:  "Qwen",
		Summary:         "The #1 community uncensored powerhouse (4.52M+ downloads, 2.5K+ likes on Hugging Face, 15.4 GB Q4_K_M). Delivers frontier logic, coding, and unfiltered prose on 24GB L4 / A100 / 96GB G4.",
		SummaryZh:       "2026 社区实测第一梯队全能无审查王者（HF 452 万+下载、2,595 点赞、15.4 GB Q4_K_M），正交去审查几乎零降智，编程、中文长文与自由创作全面拉满。",
		ParameterSize:   "27B",
		Quantization:    "Q4_K_M",
		ContextLength:   65536,
		MinVRAMGB:       20,
		RecommendedGPU:  models.AcceleratorL4,
		FreeTierReady:   false,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       "hf.co/huihui-ai/Huihui-Qwen3.8-27B-abliterated-GGUF:Q4_K_M",
		HuggingFaceRepo: "huihui-ai/Huihui-Qwen3.8-27B-abliterated-GGUF",
		Tags:            []string{"uncensored", "abliterated", "hot-top1", "pro-gpu"},
	},
	{
		ID:              "qwen3.8-27b-heretic-coder",
		DisplayName:     "Qwen3.8 27B Heretic NEO · 4.2K-Likes Coding & Creative",
		DisplayNameZh:   "Qwen3.8 27B Heretic NEO · 4.2K点赞编程与创作神作",
		ProviderFamily:  "Qwen",
		Summary:         "Most-liked DavidAU Qwen3.8-27B Heretic NEO-CODER-MAX MTP build (1.83M+ downloads, 4,282 likes, 15.8 GB Q4_K_M) — razor-sharp coding, vivid storytelling & zero censorship on 24GB L4.",
		SummaryZh:       "Hugging Face 4,282 高赞、183 万+下载爆款 DavidAU Heretic NEO-CODER 精调版（15.8 GB），写代码与故事张力双顶级，24GB L4 显卡单卡满速驱动。",
		ParameterSize:   "27B",
		Quantization:    "Q4_K_M",
		ContextLength:   65536,
		MinVRAMGB:       20,
		RecommendedGPU:  models.AcceleratorL4,
		FreeTierReady:   false,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       "hf.co/DavidAU/Qwen3.8-27B-TURBO-Fable-Cold-Fusion-735-882-Heretic-Uncensored-NEO-CODER-MAX-MTP-GGUF:Q4_K_M",
		HuggingFaceRepo: "DavidAU/Qwen3.8-27B-TURBO-Fable-Cold-Fusion-735-882-Heretic-Uncensored-NEO-CODER-MAX-MTP-GGUF",
		Tags:            []string{"uncensored", "heretic", "coding-rp", "pro-gpu"},
	},
	{
		ID:              "gemma4-31b-heretic",
		DisplayName:     "Gemma 4 31B ARA-Heretic · Creative & Roleplay King",
		DisplayNameZh:   "Gemma 4 31B ARA-Heretic · 酒馆角色扮演与小说王者",
		ProviderFamily:  "Gemma",
		Summary:         "Google Gemma 4 31B refined via ARA-Heretic orthogonal ablation (242K+ downloads, 19.4 GB Q4_K_M) — rich literary prose, 128K context, and zero lecturing for SillyTavern & writing on A100/G4.",
		SummaryZh:       "Google 最新 Gemma 4 31B 经 ARA-Heretic 正交去审查（HF 24 万+下载、19.4 GB），文笔自然不油腻、支持 128K 超长上下文，是 SillyTavern 酒馆角色扮演与小说创作公认口碑王。",
		ParameterSize:   "31B",
		Quantization:    "Q4_K_M",
		ContextLength:   131072,
		MinVRAMGB:       24,
		RecommendedGPU:  models.AcceleratorA100,
		FreeTierReady:   false,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       "hf.co/mradermacher/gemma-4-31B-it-ARA-heretic-GGUF:Q4_K_M",
		HuggingFaceRepo: "mradermacher/gemma-4-31B-it-ARA-heretic-GGUF",
		Tags:            []string{"uncensored", "heretic", "creative-rp", "long-context"},
	},
	{
		ID:              "glm5.3-flash-uncensored",
		DisplayName:     "GLM-5.3-Flash Uncensored · Single-File Q4 Chinese Flagship",
		DisplayNameZh:   "GLM-5.3-Flash Uncensored · 智谱最新单文件 Q4 无审查旗舰",
		ProviderFamily:  "GLM",
		Summary:         "Zhipu GLM-5.3-Flash 313B GSQ-RCO Uncensored single-file Q4 GGUF (127.7 GB, verified Ollama direct-pull) — runs on Colab 96GB Blackwell G4 + High-RAM with blazing Chinese writing & reasoning.",
		SummaryZh:       "智谱最新 GLM-5.3-Flash 313B GSQ-RCO 无审查单文件 Q4 版（127.7 GB，Ollama 原生直拉校验通过），依托 Colab 96GB Blackwell G4 + 高内存运行，中文创作、逻辑与 Agent 执行力极强。",
		ParameterSize:   "313B-MoE",
		Quantization:    "Q4",
		ContextLength:   131072,
		MinVRAMGB:       96,
		RecommendedGPU:  models.AcceleratorG4,
		FreeTierReady:   false,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       "hf.co/GCSA-AiLab/GLM-5.3-Flash-Uncensored-RCO-GSQ-GGUF:latest",
		HuggingFaceRepo: "GCSA-AiLab/GLM-5.3-Flash-Uncensored-RCO-GSQ-GGUF",
		Tags:            []string{"uncensored", "glm5.3", "96gb-g4", "flagship"},
	},
	{
		ID:              "qwen3.8-flash-next-abliterated",
		DisplayName:     "Qwen3.8-Flash-Next Abliterated · 1.75M-DL Single-G4 King",
		DisplayNameZh:   "Qwen3.8-Flash-Next Abliterated · 175万下载单卡旗舰",
		ProviderFamily:  "Qwen",
		Summary:         "1.75M+ downloads flagship Qwen3.8-Flash-Next 126B Gyro Abliterated single-file GGUF (50.2 GB, verified Ollama direct-pull) — fits 100% in single 96GB G4 / 80GB H100 VRAM with huge context headroom.",
		SummaryZh:       "HF 175 万+下载 Qwen3.8-Flash-Next 126B 正交去审查单文件版（仅 50.2 GB，Ollama 原生直拉校验通过），全权重轻松装进单张 96GB G4 / 80GB H100 显存，代码、推理与无限制输出双拉满。",
		ParameterSize:   "126B-MoE",
		Quantization:    "TQ3_0",
		ContextLength:   131072,
		MinVRAMGB:       56,
		RecommendedGPU:  models.AcceleratorG4,
		FreeTierReady:   false,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       "hf.co/agentionai/Qwen3.8-Flash-Next-Gyro-abliterated-Huihui-GGUF:latest",
		HuggingFaceRepo: "agentionai/Qwen3.8-Flash-Next-Gyro-abliterated-Huihui-GGUF",
		Tags:            []string{"uncensored", "abliterated", "96gb-g4", "flagship"},
	},
	{
		ID:              "deepseek-v4.1-flash-uncensored",
		DisplayName:     "DeepSeek-V4-Flash Abliterated · 284B MoE Single-G4 Flagship",
		DisplayNameZh:   "DeepSeek-V4-Flash Abliterated · 284B MoE 单卡旗舰",
		ProviderFamily:  "DeepSeek",
		Summary:         "Top-downloaded single-file Huihui DeepSeek-V4-Flash 284B MoE Abliterated GGUF (600K+ DL, 80.8 GB Q2, verified Ollama direct-pull) — fits 100% inside a single 96GB Blackwell G4 VRAM at full speed.",
		SummaryZh:       "HF 60 万+下载 Huihui DeepSeek-V4-Flash 284B MoE 正交去审查单文件版（80.8 GB，Ollama 原生直拉校验通过），完整装进单张 96GB Blackwell G4 显存满速独享。",
		ParameterSize:   "284B-MoE",
		Quantization:    "Q2",
		ContextLength:   131072,
		MinVRAMGB:       84,
		RecommendedGPU:  models.AcceleratorG4,
		FreeTierReady:   false,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       "hf.co/huihui-ai/Huihui-DeepSeek-V4-Flash-abliterated-ds4-GGUF:latest",
		HuggingFaceRepo: "huihui-ai/Huihui-DeepSeek-V4-Flash-abliterated-ds4-GGUF",
		Tags:            []string{"uncensored", "deepseek-v4", "96gb-g4", "flagship"},
	},
}

// DefaultCatalog returns a defensive copy of the 8 curated Nov 2026 uncensored open-weight LLM specs.
func DefaultCatalog() []models.LLMModelSpec {
	return cloneCatalogSpecs(curatedCatalog)
}

func cloneCatalogSpecs(in []models.LLMModelSpec) []models.LLMModelSpec {
	out := make([]models.LLMModelSpec, len(in))
	for i, item := range in {
		cp := item
		if len(item.Tags) > 0 {
			cp.Tags = append([]string(nil), item.Tags...)
		}
		out[i] = cp
	}
	return out
}

// LoadCatalogWithOverride reads an optional hot-updatable JSON model catalog from
// `COLABHIVE_LLM_CATALOG_PATH`, `explicitPath`, `<dataDir>/llm_catalog.json`, or
// `~/.colabhive/llm_catalog.json`. Invalid or missing files safely fall back to DefaultCatalog().
func LoadCatalogWithOverride(dataDir, explicitPath string, disableUserHome bool) []models.LLMModelSpec {
	var candidates []string
	if envPath := strings.TrimSpace(os.Getenv("COLABHIVE_LLM_CATALOG_PATH")); envPath != "" {
		candidates = append(candidates, envPath)
	}
	if p := strings.TrimSpace(explicitPath); p != "" {
		candidates = append(candidates, p)
	}
	if d := strings.TrimSpace(dataDir); d != "" {
		candidates = append(candidates, filepath.Join(d, "llm_catalog.json"))
	}
	if !disableUserHome {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			candidates = append(candidates, filepath.Join(home, ".colabhive", "llm_catalog.json"))
		}
	}

	for _, cand := range candidates {
		specs, ok := loadCatalogJSONFile(cand)
		if ok && len(specs) > 0 {
			return specs
		}
	}
	return DefaultCatalog()
}

func loadCatalogJSONFile(filePath string) ([]models.LLMModelSpec, bool) {
	fi, err := os.Stat(filePath)
	if err != nil || fi.IsDir() || fi.Size() <= 0 || fi.Size() > maxExternalCatalogBytes {
		return nil, false
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return nil, false
	}
	var parsed []models.LLMModelSpec
	if err := json.Unmarshal(raw, &parsed); err != nil {
		var wrapped struct {
			Models []models.LLMModelSpec `json:"models"`
		}
		if wErr := json.Unmarshal(raw, &wrapped); wErr != nil || len(wrapped.Models) == 0 {
			return nil, false
		}
		parsed = wrapped.Models
	}
	validated := make([]models.LLMModelSpec, 0, len(parsed))
	seen := make(map[string]struct{}, len(parsed))
	for _, sp := range parsed {
		id := strings.TrimSpace(sp.ID)
		if id == "" || len(id) > maxCustomModelTagLen || !validModelCharsetRe.MatchString(id) {
			continue
		}
		key := strings.ToLower(id)
		if _, dup := seen[key]; dup {
			continue
		}
		tagRaw := strings.TrimSpace(sp.OllamaTag)
		if tagRaw == "" && strings.TrimSpace(sp.HuggingFaceRepo) != "" {
			tagRaw = "hf.co/" + strings.TrimSpace(sp.HuggingFaceRepo)
		}
		if tagRaw == "" {
			tagRaw = id
		}
		normTag, nErr := NormalizeCustomModelTag(tagRaw)
		if nErr != nil {
			continue
		}
		sp.ID = id
		sp.OllamaTag = normTag
		if strings.TrimSpace(sp.DisplayName) == "" {
			sp.DisplayName = id
		}
		if strings.TrimSpace(sp.DisplayNameZh) == "" {
			sp.DisplayNameZh = sp.DisplayName
		}
		if sp.DefaultEngine == "" {
			sp.DefaultEngine = models.LLMEngineOllama
		}
		if sp.RecommendedGPU == "" {
			sp.RecommendedGPU = models.AcceleratorT4
		}
		if sp.ContextLength <= 0 {
			sp.ContextLength = 32768
		}
		if sp.MinVRAMGB < 0 {
			sp.MinVRAMGB = 8
		}
		seen[key] = struct{}{}
		validated = append(validated, sp)
	}
	if len(validated) == 0 {
		return nil, false
	}
	return validated, true
}

// LookupModelSpecInCatalog resolves a model from catalog by ID or Ollama tag, case-insensitively.
func LookupModelSpecInCatalog(idOrTag string, catalog []models.LLMModelSpec) (models.LLMModelSpec, bool) {
	if len(catalog) == 0 {
		catalog = curatedCatalog
	}
	needle := strings.ToLower(strings.TrimSpace(idOrTag))
	if needle == "" {
		if sp, ok := LookupModelSpecInCatalog(DefaultModelID, catalog); ok {
			return sp, true
		}
		cp := catalog[0]
		cp.Tags = append([]string(nil), cp.Tags...)
		return cp, true
	}
	needleNoLatest := strings.TrimSuffix(needle, ":latest")
	for _, spec := range catalog {
		idLower := strings.ToLower(spec.ID)
		tagLower := strings.ToLower(spec.OllamaTag)
		repoLower := strings.ToLower(spec.HuggingFaceRepo)
		if needle == idLower ||
			needle == tagLower ||
			needleNoLatest == idLower ||
			needleNoLatest == tagLower ||
			(repoLower != "" && (needle == repoLower || needle == "hf.co/"+repoLower)) {
			cp := spec
			cp.Tags = append([]string(nil), spec.Tags...)
			return cp, true
		}
	}
	return models.LLMModelSpec{}, false
}

// LookupModelSpec resolves one of the curated catalog models by its canonical ID or Ollama tag.
func LookupModelSpec(idOrTag string) (models.LLMModelSpec, bool) {
	return LookupModelSpecInCatalog(idOrTag, curatedCatalog)
}

// IsProprietaryClientModelAlias reports whether raw is a closed-source model identifier sent by
// third-party OpenAI/Anthropic clients (Cursor, Claude Code, Immersive Translate) that should map
// to the active or default open-source model on cold chat requests.
func IsProprietaryClientModelAlias(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	for _, pfx := range []string{"gpt-", "chatgpt-", "claude-", "o1", "o3", "o4", "text-davinci"} {
		if strings.HasPrefix(lower, pfx) {
			return true
		}
	}
	return lower == "default" || lower == "auto"
}

// NormalizeCustomModelTag validates and normalizes curated IDs, Hugging Face URLs
// (`https://huggingface.co/owner/repo` -> `hf.co/owner/repo`), `hf.co/...` GGUF tags,
// and custom Ollama `name:tag` identifiers while strictly blocking path traversal and shell metacharacters.
func NormalizeCustomModelTag(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultModelID, nil
	}

	// Support copy-pasted `ollama run <tag>` / `ollama pull <tag>` snippets ONLY when clean.
	lowerPre := strings.ToLower(s)
	if strings.HasPrefix(lowerPre, "ollama run ") {
		s = strings.TrimSpace(s[len("ollama run "):])
	} else if strings.HasPrefix(lowerPre, "ollama pull ") {
		s = strings.TrimSpace(s[len("ollama pull "):])
	}

	if s == "" {
		return "", fmt.Errorf("invalid model_id: empty model identifier")
	}
	if len(s) > maxCustomModelTagLen {
		return "", fmt.Errorf("invalid model_id: length %d exceeds maximum of %d characters", len(s), maxCustomModelTagLen)
	}
	if strings.Contains(s, "..") {
		return "", fmt.Errorf("invalid model_id %q: path traversal sequence '..' is not allowed", raw)
	}
	if !validModelCharsetRe.MatchString(s) {
		return "", fmt.Errorf("invalid model_id %q: must match ^[a-zA-Z0-9._:/-]+$ with no spaces or shell metacharacters", raw)
	}

	// Normalize Hugging Face web URLs into Ollama's native `hf.co/<owner>/<repo>[:<quant>]` pull syntax.
	hfPrefixes := []string{
		"https://www.huggingface.co/",
		"http://www.huggingface.co/",
		"https://huggingface.co/",
		"http://huggingface.co/",
		"www.huggingface.co/",
		"huggingface.co/",
		"https://hf.co/",
		"http://hf.co/",
	}
	lowerCheck := strings.ToLower(s)
	for _, pfx := range hfPrefixes {
		if strings.HasPrefix(lowerCheck, pfx) {
			s = "hf.co/" + s[len(pfx):]
			lowerCheck = strings.ToLower(s)
			break
		}
	}

	if strings.Contains(s, "://") {
		return "", fmt.Errorf("invalid model_id %q: only huggingface.co / hf.co URLs or Ollama tags are supported", raw)
	}

	s = strings.TrimSuffix(s, "/")
	if strings.Contains(s, "//") {
		return "", fmt.Errorf("invalid model_id %q: consecutive slashes are not allowed", raw)
	}
	if strings.HasSuffix(s, ":") || strings.HasSuffix(s, "/") {
		return "", fmt.Errorf("invalid model_id %q: trailing separator is not allowed", raw)
	}

	if strings.HasPrefix(strings.ToLower(s), "hf.co/") {
		rest := s[len("hf.co/"):]
		// Strip browser tab suffixes like `/tree/main` if user copied from HF Files page.
		for _, webSuffix := range []string{"/tree/main", "/blob/main"} {
			if strings.HasSuffix(strings.ToLower(rest), webSuffix) {
				rest = rest[:len(rest)-len(webSuffix)]
			}
		}
		repoPart := rest
		quantPart := ""
		if colon := strings.IndexByte(rest, ':'); colon >= 0 {
			repoPart = rest[:colon]
			quantPart = rest[colon+1:]
			if quantPart == "" || strings.Contains(quantPart, "/") || strings.Contains(quantPart, ":") {
				return "", fmt.Errorf("invalid model_id %q: malformed quantization tag after ':'", raw)
			}
		}
		segs := strings.Split(repoPart, "/")
		if len(segs) != 2 || segs[0] == "" || segs[1] == "" {
			return "", fmt.Errorf("invalid Hugging Face model %q: expected hf.co/<owner>/<repo>[:<quant>]", raw)
		}
		if quantPart != "" {
			s = "hf.co/" + segs[0] + "/" + segs[1] + ":" + quantPart
		} else {
			s = "hf.co/" + segs[0] + "/" + segs[1]
		}
	} else {
		// Validate optional `<namespace>/<name>[:<tag>]` or `<name>[:<tag>]` structure.
		if strings.Count(s, ":") > 1 || strings.Count(s, "/") > 1 {
			return "", fmt.Errorf("invalid model_id %q: expected <name>[:<tag>], <owner>/<name>[:<tag>], or hf.co/<owner>/<repo>[:<quant>]", raw)
		}
	}

	if !validModelCharsetRe.MatchString(s) {
		return "", fmt.Errorf("invalid model_id %q: normalized tag failed character validation", raw)
	}
	return s, nil
}

// ResolveModelSpec resolves a curated catalog model OR synthesizes a validated custom
// Hugging Face GGUF / Ollama `LLMModelSpec` with automatic parameter-size VRAM & GPU tier inference.
func ResolveModelSpec(raw string) (models.LLMModelSpec, error) {
	return ResolveModelSpecInCatalog(raw, curatedCatalog)
}

// ResolveModelSpecInCatalog resolves against a supplied (possibly hot-reloaded) catalog before
// synthesizing a validated custom Hugging Face GGUF or Ollama spec.
func ResolveModelSpecInCatalog(raw string, catalog []models.LLMModelSpec) (models.LLMModelSpec, error) {
	if strings.TrimSpace(raw) == "" {
		sp, _ := LookupModelSpecInCatalog(DefaultModelID, catalog)
		return sp, nil
	}
	if sp, ok := LookupModelSpecInCatalog(raw, catalog); ok {
		return sp, nil
	}

	normTag, err := NormalizeCustomModelTag(raw)
	if err != nil {
		return models.LLMModelSpec{}, err
	}
	if sp, ok := LookupModelSpecInCatalog(normTag, catalog); ok {
		return sp, nil
	}

	recGPU, freeReady, minVRAM, paramLabel := inferCustomModelHardware(normTag)
	quant := extractQuantizationLabel(normTag)
	family := inferCustomProviderFamily(normTag)

	isHF := strings.HasPrefix(normTag, "hf.co/")
	hfRepo := ""
	shortTitle := normTag
	if isHF {
		withoutHost := strings.TrimPrefix(normTag, "hf.co/")
		repoOnly := withoutHost
		if colon := strings.IndexByte(withoutHost, ':'); colon >= 0 {
			repoOnly = withoutHost[:colon]
		}
		hfRepo = repoOnly
		if slash := strings.IndexByte(repoOnly, '/'); slash >= 0 && slash+1 < len(repoOnly) {
			shortTitle = repoOnly[slash+1:]
		} else {
			shortTitle = repoOnly
		}
		if quant != "" && quant != "GGUF" {
			shortTitle = fmt.Sprintf("%s (%s)", shortTitle, quant)
		}
	}

	tierHintZh := "免费显卡可跑"
	if !freeReady {
		tierHintZh = fmt.Sprintf("推荐 %s 大显存", recGPU)
	}

	displayName := fmt.Sprintf("%s · Custom Model", shortTitle)
	displayNameZh := fmt.Sprintf("%s · 自定义模型 (%s)", shortTitle, tierHintZh)
	summary := fmt.Sprintf("Custom model (%s) pulled directly onto your private pooled %s GPU via Ollama with zero external logging.", normTag, recGPU)
	summaryZh := fmt.Sprintf("直接拉取至云端 %s 显卡私有运行的自定义模型（%s），支持无审查微调权重与 Hugging Face GGUF 直连。", recGPU, normTag)
	tags := []string{"custom"}
	if isHF {
		displayName = fmt.Sprintf("%s · Hugging Face GGUF", shortTitle)
		displayNameZh = fmt.Sprintf("%s · Hugging Face 直连 (%s)", shortTitle, tierHintZh)
		tags = append(tags, "huggingface", "gguf")
	}
	if freeReady {
		tags = append(tags, "free-gpu")
	} else {
		tags = append(tags, "pro-gpu")
	}

	return models.LLMModelSpec{
		ID:              normTag,
		DisplayName:     displayName,
		DisplayNameZh:   displayNameZh,
		ProviderFamily:  family,
		Summary:         summary,
		SummaryZh:       summaryZh,
		ParameterSize:   paramLabel,
		Quantization:    quant,
		ContextLength:   32768,
		MinVRAMGB:       minVRAM,
		RecommendedGPU:  recGPU,
		FreeTierReady:   freeReady,
		DefaultEngine:   models.LLMEngineOllama,
		OllamaTag:       normTag,
		HuggingFaceRepo: hfRepo,
		Tags:            tags,
	}, nil
}

func inferCustomModelHardware(normTag string) (gpu models.AcceleratorType, freeReady bool, minVRAMGB int, paramSize string) {
	matches := paramSizeBillionsRe.FindAllStringSubmatch(strings.ToLower(normTag), -1)
	maxBillions := 0.0
	rawLabel := ""
	for _, m := range matches {
		if len(m) < 3 {
			continue
		}
		base, err := strconv.ParseFloat(m[2], 64)
		if err != nil || base <= 0 {
			continue
		}
		eff := base
		label := strings.ToUpper(m[2]) + "B"
		if m[1] != "" {
			if mult, mErr := strconv.ParseFloat(m[1], 64); mErr == nil && mult > 1 {
				// MoE total parameters (e.g. 8x7B ~ 47B active memory class -> L4; 8x22B -> G4)
				eff = mult * base * 0.65
				label = m[1] + "x" + label
			}
		}
		if eff > maxBillions {
			maxBillions = eff
			rawLabel = label
		}
	}

	if rawLabel == "" {
		rawLabel = "Custom"
	}

	switch {
	case maxBillions >= 48:
		// e.g. 70B, 72B, 104B, 123B, 405B, 8x22B
		return models.AcceleratorG4, false, 48, rawLabel
	case maxBillions >= 20:
		// e.g. 22B, 24B, 27B, 30B, 32B, 34B, 8x7B
		return models.AcceleratorL4, false, 22, rawLabel
	case maxBillions >= 11:
		// e.g. 12B, 13B, 14B, 16B — fits inside 16GB Free T4 in Q4_K_M
		return models.AcceleratorT4, true, 12, rawLabel
	default:
		return models.AcceleratorT4, true, 8, rawLabel
	}
}

func extractQuantizationLabel(normTag string) string {
	if idx := strings.LastIndexByte(normTag, ':'); idx >= 0 && idx+1 < len(normTag) {
		after := normTag[idx+1:]
		upper := strings.ToUpper(after)
		if strings.Contains(upper, "Q") || strings.Contains(upper, "FP") || strings.Contains(upper, "BF16") || strings.Contains(upper, "F16") {
			return upper
		}
	}
	if strings.Contains(strings.ToLower(normTag), "gguf") {
		return "GGUF"
	}
	return "Q4_K_M"
}

func inferCustomProviderFamily(normTag string) string {
	lower := strings.ToLower(normTag)
	switch {
	case strings.Contains(lower, "qwen") || strings.Contains(lower, "qwq"):
		return "Qwen"
	case strings.Contains(lower, "deepseek"):
		return "DeepSeek"
	case strings.Contains(lower, "gemma"):
		return "Gemma"
	case strings.Contains(lower, "glm"):
		return "GLM"
	case strings.Contains(lower, "minimax"):
		return "MiniMax"
	case strings.Contains(lower, "llama"):
		return "Llama"
	case strings.Contains(lower, "mistral") || strings.Contains(lower, "mixtral"):
		return "Mistral"
	case strings.HasPrefix(lower, "hf.co/"):
		return "HuggingFace"
	default:
		return "Custom"
	}
}

// EstimatedModelBytes returns an approximate GGUF artifact byte size for Ollama `/api/tags` metadata.
func EstimatedModelBytes(spec models.LLMModelSpec) int64 {
	const gb = int64(1000 * 1000 * 1000)
	switch spec.ID {
	case "qwen3.5-9b-heretic":
		return 5700000000
	case "gemma4-12b-heretic":
		return 6870000000
	case "qwen3.8-27b-abliterated":
		return 15410000000
	case "qwen3.8-27b-heretic-coder":
		return 16810000000
	case "gemma4-31b-heretic":
		return 17400000000
	case "glm5.3-flash-uncensored":
		return 127660000000
	case "qwen3.8-flash-next-abliterated":
		return 50220000000
	case "deepseek-v4.1-flash-uncensored":
		return 80760000000
	default:
		if spec.MinVRAMGB > 0 {
			return int64(spec.MinVRAMGB) * gb * 8 / 10
		}
		return 5 * gb
	}
}
