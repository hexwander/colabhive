package appstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

var (
	// ErrAppPluginNotFound is returned when a third-party app plugin ID does not exist.
	ErrAppPluginNotFound = errors.New("app plugin not found")

	// ErrPluginManifestTooLarge is returned when a plugin manifest request or serialized JSON exceeds MaxPluginManifestBytes.
	ErrPluginManifestTooLarge = errors.New("plugin manifest exceeds maximum allowed size (256 KiB)")

	pluginIDRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,48}$`)
	modelOptionIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	capabilityIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,63}$`)
)

var allowedPluginPermissions = map[string]bool{
	"assets:read":          true,
	"assets:write":         true,
	"media:invoke":         true,
	"engines:read":         true,
	"jobs:manage":          true,
	"cap:llm.openai.v1":    true,
	"cap:media.image.v1":   true,
	"cap:media.video.v1":   true,
	"cap:media.audio.v1":   true,
}

const (
	// MaxPluginManifestBytes is the single authoritative 256 KiB size cap enforced across
	// HTTP decode, atomic persistence, and runtime directory reload so installed plugins
	// can never be silently dropped on reload.
	MaxPluginManifestBytes = 256 << 10
	maxPluginManifestBytes = MaxPluginManifestBytes
	minPluginPort          = 1024
	maxPluginPort          = 65535
)

// AppPluginManifest defines the declarative JSON specification for a community or
// third-party ColabHive AI App Store plugin stored under:
//   - v1 single-file engine: `<dataDir>/plugins/<id>.json` or `~/.colabhive/plugins/<id>.json`
//   - v2 directory bundle (engine, local UI, or both): `<dataDir>/plugins/<id>/manifest.json` or `~/.colabhive/plugins/<id>/manifest.json`
type AppPluginManifest struct {
	SchemaVersion          string                  `json:"schema_version,omitempty"`
	ID                     string                  `json:"id"`
	Kind                   models.PluginKind       `json:"kind,omitempty"`
	Name                   string                  `json:"name"`
	NameZh                 string                  `json:"name_zh,omitempty"`
	Tagline                string                  `json:"tagline,omitempty"`
	TaglineZh              string                  `json:"tagline_zh,omitempty"`
	Description            string                  `json:"description,omitempty"`
	DescriptionZh          string                  `json:"description_zh,omitempty"`
	Category               models.AppCategory      `json:"category"`
	Icon                   string                  `json:"icon,omitempty"`
	Author                 string                  `json:"author,omitempty"`
	Version                string                  `json:"version,omitempty"`
	HomepageURL            string                  `json:"homepage_url,omitempty"`
	RepoURL                string                  `json:"repo_url,omitempty"`
	StartCommand           string                  `json:"start_command,omitempty"`
	RemotePort             int                     `json:"remote_port,omitempty"`
	LocalPort              int                     `json:"local_port,omitempty"`
	MinVRAMGB              int                     `json:"min_vram_gb,omitempty"`
	RecommendedGPU         models.AcceleratorType  `json:"recommended_gpu,omitempty"`
	FreeTierCompatible     *bool                   `json:"free_tier_compatible,omitempty"`
	SupportsPortBridge     *bool                   `json:"supports_port_bridge,omitempty"`
	EstBootSec             int                     `json:"est_boot_sec,omitempty"`
	Highlights             []string                `json:"highlights,omitempty"`
	HighlightsZh           []string                `json:"highlights_zh,omitempty"`
	PluginIntegrationTipEn string                  `json:"plugin_integration_tip_en,omitempty"`
	PluginIntegrationTipZh string                  `json:"plugin_integration_tip_zh,omitempty"`
	CheckpointDir          string                  `json:"checkpoint_dir,omitempty"`
	HealthPath             string                  `json:"health_path,omitempty"`
	Provides               []string                `json:"provides,omitempty"`
	Consumes               []string                `json:"consumes,omitempty"`
	OptionalCapabilities   []string                `json:"optional_capabilities,omitempty"`
	Permissions            []string                `json:"permissions,omitempty"`
	MediaAPIBase           string                  `json:"media_api_base,omitempty"`
	UI                     *models.PluginUIConfig  `json:"ui,omitempty"`
	ModelOptions           []models.AppModelOption `json:"model_options,omitempty"`
	SupportsCustomModelURL bool                    `json:"supports_custom_model_url,omitempty"`

	// RootDir is the resolved absolute directory for directory-based v2 plugin bundles (never serialized).
	RootDir string `json:"-"`
}

type loadedPluginEntry struct {
	manifest AppPluginManifest
	template models.AppTemplate
	source   string // "data_dir" | "user_home"
}

// ValidatePluginManifest validates and canonicalizes a declarative third-party App Store plugin manifest.
func ValidatePluginManifest(raw AppPluginManifest) (AppPluginManifest, error) {
	m := raw
	m.SchemaVersion = strings.TrimSpace(m.SchemaVersion)
	if m.SchemaVersion == "" {
		m.SchemaVersion = "1"
	}

	kind := models.PluginKind(strings.ToLower(strings.TrimSpace(string(m.Kind))))
	switch kind {
	case "", models.PluginKindEngine:
		m.Kind = models.PluginKindEngine
	case models.PluginKindUI, models.PluginKindBundle:
		m.Kind = kind
		if m.SchemaVersion == "1" {
			m.SchemaVersion = "2"
		}
	default:
		return AppPluginManifest{}, fmt.Errorf("invalid plugin kind %q: must be engine, ui, or bundle", raw.Kind)
	}

	m.ID = strings.TrimSpace(m.ID)
	if !pluginIDRe.MatchString(m.ID) {
		return AppPluginManifest{}, fmt.Errorf("invalid plugin id %q: must match ^[a-z0-9][a-z0-9-]{1,48}$", raw.ID)
	}
	if IsBuiltinTemplateID(m.ID) {
		return AppPluginManifest{}, fmt.Errorf("invalid plugin id %q: conflicts with built-in app template", m.ID)
	}

	cat := models.AppCategory(strings.ToLower(strings.TrimSpace(string(m.Category))))
	switch cat {
	case models.AppCategoryVideo,
		models.AppCategoryImage,
		models.AppCategoryAudio,
		models.AppCategoryWorkspace,
		models.AppCategoryCustom:
		m.Category = cat
	default:
		return AppPluginManifest{}, fmt.Errorf("invalid plugin category %q: must be one of video, image, audio, workspace, custom", raw.Category)
	}

	m.Name = strings.TrimSpace(m.Name)
	m.NameZh = strings.TrimSpace(m.NameZh)
	if m.Name == "" && m.NameZh != "" {
		m.Name = m.NameZh
	}
	if m.Name == "" {
		return AppPluginManifest{}, errors.New("invalid plugin manifest: name is required")
	}
	if m.NameZh == "" {
		m.NameZh = m.Name
	}
	if len(m.Name) > 140 || len(m.NameZh) > 140 {
		return AppPluginManifest{}, errors.New("invalid plugin manifest: name exceeds maximum length (140 chars)")
	}

	m.Tagline = strings.TrimSpace(m.Tagline)
	m.TaglineZh = strings.TrimSpace(m.TaglineZh)
	if m.Tagline == "" {
		m.Tagline = m.Name
	}
	if m.TaglineZh == "" {
		m.TaglineZh = m.Tagline
	}
	m.Description = strings.TrimSpace(m.Description)
	m.DescriptionZh = strings.TrimSpace(m.DescriptionZh)
	if len(m.Description) > 2048 || len(m.DescriptionZh) > 2048 {
		return AppPluginManifest{}, errors.New("invalid plugin manifest: description exceeds maximum length (2048 chars)")
	}
	if m.Description == "" {
		m.Description = m.Tagline
	}
	if m.DescriptionZh == "" {
		m.DescriptionZh = m.Description
	}

	if m.Kind == models.PluginKindUI {
		// Pure UI plugins run locally on an isolated loopback origin and do not require a remote GPU port.
		if m.RemotePort != 0 && (m.RemotePort < minPluginPort || m.RemotePort > maxPluginPort) {
			return AppPluginManifest{}, fmt.Errorf("invalid remote_port %d: must be 0 or between %d and %d", m.RemotePort, minPluginPort, maxPluginPort)
		}
		if m.LocalPort != 0 && (m.LocalPort < minPluginPort || m.LocalPort > maxPluginPort) {
			return AppPluginManifest{}, fmt.Errorf("invalid local_port %d: must be 0 or between %d and %d", m.LocalPort, minPluginPort, maxPluginPort)
		}
	} else {
		if m.RemotePort < minPluginPort || m.RemotePort > maxPluginPort {
			return AppPluginManifest{}, fmt.Errorf("invalid remote_port %d: must be between %d and %d", m.RemotePort, minPluginPort, maxPluginPort)
		}
		if m.LocalPort == 0 {
			m.LocalPort = m.RemotePort
		} else if m.LocalPort < minPluginPort || m.LocalPort > maxPluginPort {
			return AppPluginManifest{}, fmt.Errorf("invalid local_port %d: must be between %d and %d", m.LocalPort, minPluginPort, maxPluginPort)
		}
	}

	validateCaps := func(field string, caps []string) ([]string, error) {
		if len(caps) == 0 {
			return nil, nil
		}
		if len(caps) > 16 {
			return nil, fmt.Errorf("invalid %s: exceeds maximum 16 entries", field)
		}
		seen := make(map[string]struct{}, len(caps))
		out := make([]string, 0, len(caps))
		for _, c := range caps {
			norm := strings.ToLower(strings.TrimSpace(c))
			if !capabilityIDRe.MatchString(norm) {
				return nil, fmt.Errorf("invalid %s entry %q", field, c)
			}
			if _, dup := seen[norm]; dup {
				continue
			}
			seen[norm] = struct{}{}
			out = append(out, norm)
		}
		return out, nil
	}
	var capErr error
	if m.Provides, capErr = validateCaps("provides", m.Provides); capErr != nil {
		return AppPluginManifest{}, capErr
	}
	if m.Consumes, capErr = validateCaps("consumes", m.Consumes); capErr != nil {
		return AppPluginManifest{}, capErr
	}
	if m.OptionalCapabilities, capErr = validateCaps("optional_capabilities", m.OptionalCapabilities); capErr != nil {
		return AppPluginManifest{}, capErr
	}
	if len(m.Permissions) > 0 {
		if len(m.Permissions) > 16 {
			return AppPluginManifest{}, errors.New("invalid permissions: exceeds maximum 16 entries")
		}
		seenPerm := make(map[string]struct{}, len(m.Permissions))
		cleanPerms := make([]string, 0, len(m.Permissions))
		for _, p := range m.Permissions {
			norm := strings.ToLower(strings.TrimSpace(p))
			if !allowedPluginPermissions[norm] {
				return AppPluginManifest{}, fmt.Errorf("invalid plugin permission %q", p)
			}
			if _, dup := seenPerm[norm]; dup {
				continue
			}
			seenPerm[norm] = struct{}{}
			cleanPerms = append(cleanPerms, norm)
		}
		m.Permissions = cleanPerms
	}

	if strings.TrimSpace(m.MediaAPIBase) != "" {
		mb := strings.TrimSpace(m.MediaAPIBase)
		if !strings.HasPrefix(mb, "/") || strings.ContainsAny(mb, " \t\r\n\x00\\") || strings.Contains(mb, "..") {
			return AppPluginManifest{}, fmt.Errorf("invalid plugin media_api_base %q", raw.MediaAPIBase)
		}
		m.MediaAPIBase = strings.TrimRight(mb, "/")
	}

	if m.UI != nil {
		uiCopy := *m.UI
		uiCopy.Mode = strings.ToLower(strings.TrimSpace(uiCopy.Mode))
		if uiCopy.Mode == "" {
			if m.Kind == models.PluginKindUI || m.Kind == models.PluginKindBundle {
				uiCopy.Mode = "local_bundle"
			} else {
				uiCopy.Mode = "remote_proxy"
			}
		}
		switch uiCopy.Mode {
		case "remote_proxy", "local_bundle", "builtin_route":
		default:
			return AppPluginManifest{}, fmt.Errorf("invalid plugin ui.mode %q", m.UI.Mode)
		}
		if uiCopy.Mode == "local_bundle" {
			bd := strings.TrimSpace(uiCopy.BundleDir)
			if bd == "" {
				bd = "ui/dist"
			}
			cleanBD, bErr := ValidateModelTargetSubdir(bd)
			if bErr != nil {
				return AppPluginManifest{}, fmt.Errorf("invalid plugin ui.bundle_dir: %w", bErr)
			}
			uiCopy.BundleDir = cleanBD
			entry := strings.TrimSpace(uiCopy.Entry)
			if entry == "" {
				entry = "index.html"
			}
			if strings.ContainsAny(entry, "\\\x00") || strings.Contains(entry, "..") || strings.HasPrefix(entry, "/") {
				return AppPluginManifest{}, fmt.Errorf("invalid plugin ui.entry %q", uiCopy.Entry)
			}
			uiCopy.Entry = entry
		}
		if uiCopy.Route != "" && (!strings.HasPrefix(uiCopy.Route, "/") || strings.Contains(uiCopy.Route, "..")) {
			return AppPluginManifest{}, fmt.Errorf("invalid plugin ui.route %q", uiCopy.Route)
		}
		m.UI = &uiCopy
	} else if m.Kind == models.PluginKindUI {
		m.UI = &models.PluginUIConfig{
			Mode:      "local_bundle",
			BundleDir: "ui/dist",
			Entry:     "index.html",
		}
	}

	if strings.TrimSpace(m.RepoURL) != "" {
		cleanRepo, _, err := ValidateAndSanitizeGitHubRepoURL(m.RepoURL)
		if err != nil {
			return AppPluginManifest{}, fmt.Errorf("invalid plugin repo_url: %w", err)
		}
		m.RepoURL = cleanRepo
	} else {
		m.RepoURL = ""
	}

	if strings.TrimSpace(m.StartCommand) != "" {
		cleanCmd, err := ValidateCustomStartCommand(m.StartCommand)
		if err != nil {
			return AppPluginManifest{}, fmt.Errorf("invalid plugin start_command: %w", err)
		}
		m.StartCommand = cleanCmd
	} else {
		m.StartCommand = ""
	}

	if strings.TrimSpace(m.HomepageURL) != "" {
		hp := strings.TrimSpace(m.HomepageURL)
		u, err := url.Parse(hp)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(hp, "\r\n\x00") {
			return AppPluginManifest{}, fmt.Errorf("invalid plugin homepage_url %q: must be an http/https URL", raw.HomepageURL)
		}
		m.HomepageURL = hp
	}

	if m.MinVRAMGB < 0 || m.MinVRAMGB > 1024 {
		return AppPluginManifest{}, fmt.Errorf("invalid min_vram_gb %d: must be between 0 and 1024", m.MinVRAMGB)
	}

	if strings.TrimSpace(string(m.RecommendedGPU)) == "" {
		m.RecommendedGPU = models.AcceleratorT4
	} else {
		gpu, ok := normalizeKnownAccelerator(m.RecommendedGPU)
		if !ok {
			return AppPluginManifest{}, fmt.Errorf("invalid recommended_gpu %q", raw.RecommendedGPU)
		}
		m.RecommendedGPU = gpu
	}

	if m.EstBootSec <= 0 {
		m.EstBootSec = 35
	} else if m.EstBootSec > 600 {
		m.EstBootSec = 600
	}

	m.Icon = strings.TrimSpace(m.Icon)
	if m.Icon == "" {
		m.Icon = "🧩"
	}

	m.Author = strings.TrimSpace(m.Author)
	m.Version = strings.TrimSpace(m.Version)
	if m.Version == "" {
		m.Version = "1.0.0"
	}

	if strings.TrimSpace(m.CheckpointDir) == "" {
		m.CheckpointDir = "/content/checkpoints/" + m.ID
	} else {
		cp, err := validatePluginCheckpointDir(m.CheckpointDir)
		if err != nil {
			return AppPluginManifest{}, err
		}
		m.CheckpointDir = cp
	}

	if strings.TrimSpace(m.HealthPath) == "" {
		m.HealthPath = "/"
	} else {
		hp := strings.TrimSpace(m.HealthPath)
		if !strings.HasPrefix(hp, "/") || strings.ContainsAny(hp, " \t\r\n\x00\\") || strings.Contains(hp, "..") {
			return AppPluginManifest{}, fmt.Errorf("invalid plugin health_path %q: must be a safe relative HTTP path starting with /", raw.HealthPath)
		}
		m.HealthPath = hp
	}

	if len(m.Highlights) > 12 {
		m.Highlights = append([]string(nil), m.Highlights[:12]...)
	} else {
		m.Highlights = append([]string(nil), m.Highlights...)
	}
	if len(m.HighlightsZh) > 12 {
		m.HighlightsZh = append([]string(nil), m.HighlightsZh[:12]...)
	} else {
		m.HighlightsZh = append([]string(nil), m.HighlightsZh...)
	}

	if len(m.ModelOptions) > 0 {
		if len(m.ModelOptions) > 20 {
			return AppPluginManifest{}, errors.New("invalid plugin model_options: exceeds maximum 20 options")
		}
		seenOpt := make(map[string]struct{}, len(m.ModelOptions))
		cleanOpts := make([]models.AppModelOption, 0, len(m.ModelOptions))
		for _, opt := range m.ModelOptions {
			o := opt
			o.ID = strings.ToLower(strings.TrimSpace(o.ID))
			if !modelOptionIDRe.MatchString(o.ID) {
				return AppPluginManifest{}, fmt.Errorf("invalid model_option id %q", opt.ID)
			}
			if _, dup := seenOpt[o.ID]; dup {
				return AppPluginManifest{}, fmt.Errorf("invalid model_options: duplicate id %q", o.ID)
			}
			seenOpt[o.ID] = struct{}{}
			o.Name = strings.TrimSpace(o.Name)
			o.NameZh = strings.TrimSpace(o.NameZh)
			if o.Name == "" && o.NameZh != "" {
				o.Name = o.NameZh
			}
			if o.Name == "" {
				o.Name = o.ID
			}
			if o.NameZh == "" {
				o.NameZh = o.Name
			}
			o.Summary = strings.TrimSpace(o.Summary)
			o.SummaryZh = strings.TrimSpace(o.SummaryZh)
			if o.MinVRAMGB < 0 || o.MinVRAMGB > 1024 {
				return AppPluginManifest{}, fmt.Errorf("invalid model_option %q min_vram_gb %d", o.ID, o.MinVRAMGB)
			}
			if strings.TrimSpace(string(o.RecommendedGPU)) == "" {
				o.RecommendedGPU = m.RecommendedGPU
			} else {
				gpu, ok := normalizeKnownAccelerator(o.RecommendedGPU)
				if !ok {
					return AppPluginManifest{}, fmt.Errorf("invalid model_option %q recommended_gpu %q", o.ID, opt.RecommendedGPU)
				}
				o.RecommendedGPU = gpu
			}
			if strings.TrimSpace(o.DownloadURL) != "" {
				cleanURL, derivedFile, uErr := ValidateAndSanitizeModelDownloadURL(o.DownloadURL)
				if uErr != nil {
					return AppPluginManifest{}, fmt.Errorf("invalid model_option %q: %w", o.ID, uErr)
				}
				o.DownloadURL = cleanURL
				sub, sErr := ValidateModelTargetSubdir(o.TargetSubdir)
				if sErr != nil {
					return AppPluginManifest{}, fmt.Errorf("invalid model_option %q: %w", o.ID, sErr)
				}
				o.TargetSubdir = sub
				if strings.TrimSpace(o.Filename) != "" {
					fn, fErr := ValidateModelFilename(o.Filename)
					if fErr != nil {
						return AppPluginManifest{}, fmt.Errorf("invalid model_option %q: %w", o.ID, fErr)
					}
					o.Filename = fn
				} else {
					o.Filename = derivedFile
				}
			} else {
				o.DownloadURL = ""
			}
			cleanOpts = append(cleanOpts, o)
		}
		m.ModelOptions = cleanOpts
	}

	m.PluginIntegrationTipEn = strings.TrimSpace(m.PluginIntegrationTipEn)
	m.PluginIntegrationTipZh = strings.TrimSpace(m.PluginIntegrationTipZh)
	if m.PluginIntegrationTipEn == "" {
		m.PluginIntegrationTipEn = fmt.Sprintf("Community plugin bridged locally to http://127.0.0.1:%d with zero public tunnel.", m.LocalPort)
	}
	if m.PluginIntegrationTipZh == "" {
		m.PluginIntegrationTipZh = fmt.Sprintf("社区插件已通过私有通道映射至本机 http://127.0.0.1:%d，零公网穿透！", m.LocalPort)
	}

	if m.FreeTierCompatible == nil {
		v := true
		m.FreeTierCompatible = &v
	}
	if m.SupportsPortBridge == nil {
		v := true
		m.SupportsPortBridge = &v
	}

	return m, nil
}

func normalizeKnownAccelerator(raw models.AcceleratorType) (models.AcceleratorType, bool) {
	gpu := models.AcceleratorType(strings.ToUpper(strings.TrimSpace(string(raw))))
	switch gpu {
	case models.AcceleratorT4,
		models.AcceleratorL4,
		models.AcceleratorA100,
		models.AcceleratorH100,
		models.AcceleratorG4,
		models.AcceleratorV5E1,
		models.AcceleratorV6E1,
		models.AcceleratorCPU:
		return gpu, true
	default:
		return "", false
	}
}

func validatePluginCheckpointDir(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n\x00\\") {
		return "", fmt.Errorf("invalid plugin checkpoint_dir %q", raw)
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("invalid plugin checkpoint_dir %q: traversal forbidden", raw)
		}
	}
	cleaned := path.Clean("/" + strings.TrimLeft(trimmed, "/"))
	if cleaned != "/content" && !strings.HasPrefix(cleaned, "/content/") {
		return "", fmt.Errorf("invalid plugin checkpoint_dir %q: must stay under /content/", raw)
	}
	return cleaned, nil
}

// PluginToTemplate converts a validated AppPluginManifest into a runnable models.AppTemplate.
func PluginToTemplate(m AppPluginManifest) models.AppTemplate {
	freeTier := true
	if m.FreeTierCompatible != nil {
		freeTier = *m.FreeTierCompatible
	}
	supportsBridge := true
	if m.SupportsPortBridge != nil {
		supportsBridge = *m.SupportsPortBridge
	}
	highlights := append([]string(nil), m.Highlights...)
	if len(highlights) == 0 {
		highlights = []string{
			fmt.Sprintf("Declarative community plugin (%s)", m.ID),
			fmt.Sprintf("Private 127.0.0.1:%d localhost bridge", m.LocalPort),
		}
	}
	highlightsZh := append([]string(nil), m.HighlightsZh...)
	if len(highlightsZh) == 0 {
		highlightsZh = []string{
			fmt.Sprintf("社区声明式应用插件 (%s)", m.ID),
			fmt.Sprintf("本机 127.0.0.1:%d 私有直连免穿透", m.LocalPort),
		}
	}
	var opts []models.AppModelOption
	if len(m.ModelOptions) > 0 {
		opts = append([]models.AppModelOption(nil), m.ModelOptions...)
	}
	return models.AppTemplate{
		ID:                     m.ID,
		Name:                   m.Name,
		NameZh:                 m.NameZh,
		Tagline:                m.Tagline,
		TaglineZh:              m.TaglineZh,
		Description:            m.Description,
		DescriptionZh:          m.DescriptionZh,
		Category:               m.Category,
		Icon:                   m.Icon,
		DefaultRemotePort:      m.RemotePort,
		DefaultLocalPort:       m.LocalPort,
		MinVRAMGB:              m.MinVRAMGB,
		RecommendedGPU:         m.RecommendedGPU,
		FreeTierCompatible:     freeTier,
		EstBootSec:             m.EstBootSec,
		Highlights:             highlights,
		HighlightsZh:           highlightsZh,
		SupportsPortBridge:     supportsBridge,
		PluginIntegrationTipEn: m.PluginIntegrationTipEn,
		PluginIntegrationTipZh: m.PluginIntegrationTipZh,
		IsPlugin:               true,
		Author:                 m.Author,
		Version:                m.Version,
		Kind:                   m.Kind,
		Provides:               append([]string(nil), m.Provides...),
		Consumes:               append([]string(nil), m.Consumes...),
		OptionalCapabilities:   append([]string(nil), m.OptionalCapabilities...),
		Permissions:            append([]string(nil), m.Permissions...),
		MediaAPIBase:           m.MediaAPIBase,
		UI:                     clonePluginUIConfig(m.UI),
		ModelOptions:           opts,
		SupportsCustomModelURL: m.SupportsCustomModelURL,
		BootstrapScript:        customGitHubBootstrapPy,
	}
}

func clonePluginUIConfig(cfg *models.PluginUIConfig) *models.PluginUIConfig {
	if cfg == nil {
		return nil
	}
	cp := *cfg
	return &cp
}

func resolvePluginsDirs(dataDir string, disableUserHome bool) (primaryDir string, extraDirs []string) {
	cleanData := strings.TrimSpace(dataDir)
	if cleanData != "" && cleanData != ":memory:" {
		primaryDir = filepath.Join(cleanData, "plugins")
	}
	if !disableUserHome {
		homeBase := strings.TrimSpace(os.Getenv("COLABHIVE_HOME"))
		if homeBase == "" {
			if h, err := os.UserHomeDir(); err == nil && strings.TrimSpace(h) != "" {
				homeBase = filepath.Join(h, ".colabhive")
			}
		}
		if homeBase != "" {
			homePlugins := filepath.Join(homeBase, "plugins")
			if primaryDir == "" {
				primaryDir = homePlugins
			} else if !sameCleanPath(primaryDir, homePlugins) {
				extraDirs = append(extraDirs, homePlugins)
			}
		}
	}
	if primaryDir == "" {
		primaryDir = filepath.Join(os.TempDir(), "colabhive", "plugins")
	}
	return primaryDir, extraDirs
}

func sameCleanPath(a, b string) bool {
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA == nil && errB == nil {
		return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// openConfinedPluginsRoot opens the `plugins` directory strictly confined through its parent directory
// via `os.OpenRoot(filepath.Dir(dir))` + `Lstat(filepath.Base(dir))` + `parentRoot.OpenRoot(base)`,
// rejecting any symlinked or irregular `plugins` directory on read, install, and delete.
func openConfinedPluginsRoot(dir string, createIfMissing bool) (*os.Root, error) {
	cleanDir := filepath.Clean(strings.TrimSpace(dir))
	if cleanDir == "" || cleanDir == "." || cleanDir == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid plugins directory %q", dir)
	}
	parentDir := filepath.Dir(cleanDir)
	baseName := filepath.Base(cleanDir)
	if baseName == "" || baseName == "." || baseName == ".." || parentDir == cleanDir {
		return nil, fmt.Errorf("invalid plugins directory %q", dir)
	}

	if createIfMissing {
		if err := os.MkdirAll(parentDir, 0o700); err != nil {
			return nil, fmt.Errorf("create plugins parent directory: %w", err)
		}
	}

	parentRoot, err := os.OpenRoot(parentDir)
	if err != nil {
		return nil, err
	}
	defer parentRoot.Close()

	info, lErr := parentRoot.Lstat(baseName)
	if lErr != nil {
		if !errors.Is(lErr, os.ErrNotExist) {
			return nil, lErr
		}
		if !createIfMissing {
			return nil, os.ErrNotExist
		}
		if mkErr := parentRoot.Mkdir(baseName, 0o700); mkErr != nil && !errors.Is(mkErr, os.ErrExist) {
			return nil, fmt.Errorf("create plugins directory: %w", mkErr)
		}
		info, lErr = parentRoot.Lstat(baseName)
		if lErr != nil {
			return nil, lErr
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
		return nil, fmt.Errorf("refusing to use symlinked plugins directory %q", cleanDir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("refusing to use non-directory plugins path %q", cleanDir)
	}

	pluginsRoot, err := parentRoot.OpenRoot(baseName)
	if err != nil {
		return nil, fmt.Errorf("open confined plugins directory %q: %w", cleanDir, err)
	}
	return pluginsRoot, nil
}

func loadPluginsFromRootDir(dir string, source string) map[string]*loadedPluginEntry {
	out := make(map[string]*loadedPluginEntry)
	if strings.TrimSpace(dir) == "" {
		return out
	}
	root, err := openConfinedPluginsRoot(dir, false)
	if err != nil {
		return out
	}
	defer root.Close()

	dirFile, err := root.Open(".")
	if err != nil {
		return out
	}
	entries, readErr := dirFile.ReadDir(-1)
	_ = dirFile.Close()
	if readErr != nil {
		return out
	}

	for _, ent := range entries {
		name := ent.Name()
		if strings.HasPrefix(name, ".") || strings.ContainsAny(name, "/\\\x00:") || strings.Contains(name, "..") {
			continue
		}
		info, lErr := root.Lstat(name)
		if lErr != nil || info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		// v1 single-file JSON plugin: `<pluginsDir>/<id>.json`
		if info.Mode().IsRegular() && strings.HasSuffix(strings.ToLower(name), ".json") {
			if info.Size() <= 0 || info.Size() > maxPluginManifestBytes {
				continue
			}
			f, oErr := root.Open(name)
			if oErr != nil {
				continue
			}
			raw, rErr := io.ReadAll(io.LimitReader(f, maxPluginManifestBytes+1))
			_ = f.Close()
			if rErr != nil || int64(len(raw)) > maxPluginManifestBytes {
				continue
			}
			var manifest AppPluginManifest
			if uErr := json.Unmarshal(raw, &manifest); uErr != nil {
				continue
			}
			valid, vErr := ValidatePluginManifest(manifest)
			if vErr != nil {
				continue
			}
			out[valid.ID] = &loadedPluginEntry{
				manifest: valid,
				template: PluginToTemplate(valid),
				source:   source,
			}
			continue
		}

		// v2 directory bundle plugin: `<pluginsDir>/<id>/manifest.json` (+ optional `ui/dist/`)
		if info.IsDir() {
			manifestRel := name + "/manifest.json"
			mInfo, mErr := root.Lstat(manifestRel)
			if mErr != nil || mInfo.Mode()&os.ModeSymlink != 0 || !mInfo.Mode().IsRegular() {
				continue
			}
			if mInfo.Size() <= 0 || mInfo.Size() > maxPluginManifestBytes {
				continue
			}
			f, oErr := root.Open(manifestRel)
			if oErr != nil {
				continue
			}
			raw, rErr := io.ReadAll(io.LimitReader(f, maxPluginManifestBytes+1))
			_ = f.Close()
			if rErr != nil || int64(len(raw)) > maxPluginManifestBytes {
				continue
			}
			var manifest AppPluginManifest
			if uErr := json.Unmarshal(raw, &manifest); uErr != nil {
				continue
			}
			valid, vErr := ValidatePluginManifest(manifest)
			if vErr != nil {
				continue
			}
			valid.RootDir = filepath.Join(dir, name)
			out[valid.ID] = &loadedPluginEntry{
				manifest: valid,
				template: PluginToTemplate(valid),
				source:   source,
			}
		}
	}
	return out
}

func writePluginManifestAtomic(pluginsDir string, manifest AppPluginManifest) error {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if int64(len(encoded)) > maxPluginManifestBytes {
		return fmt.Errorf("%w: serialized manifest is %d bytes (max %d)", ErrPluginManifestTooLarge, len(encoded), maxPluginManifestBytes)
	}

	root, err := openConfinedPluginsRoot(pluginsDir, true)
	if err != nil {
		return fmt.Errorf("open plugins root: %w", err)
	}
	defer root.Close()

	filename := manifest.ID + ".json"
	if info, lErr := root.Lstat(filename); lErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to overwrite non-regular plugin path %q", filename)
		}
	} else if !errors.Is(lErr, os.ErrNotExist) {
		return lErr
	}

	randBuf := make([]byte, 8)
	_, _ = rand.Read(randBuf)
	tmpName := ".tmp-" + manifest.ID + "-" + hex.EncodeToString(randBuf) + ".json"

	f, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, wErr := f.Write(encoded); wErr != nil {
		_ = f.Close()
		_ = root.Remove(tmpName)
		return wErr
	}
	if cErr := f.Close(); cErr != nil {
		_ = root.Remove(tmpName)
		return cErr
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*10) * time.Millisecond)
		}
		if rErr := root.Rename(tmpName, filename); rErr == nil {
			return nil
		} else {
			lastErr = rErr
		}
	}
	_ = root.Remove(tmpName)
	return lastErr
}

func deletePluginManifestInRoot(pluginsDir string, id string) error {
	root, err := openConfinedPluginsRoot(pluginsDir, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrAppPluginNotFound
		}
		return err
	}
	defer root.Close()

	filename := id + ".json"
	info, lErr := root.Lstat(filename)
	if lErr != nil {
		if errors.Is(lErr, os.ErrNotExist) {
			return ErrAppPluginNotFound
		}
		return lErr
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("refusing to delete non-regular plugin path %q", filename)
	}
	if rErr := root.Remove(filename); rErr != nil {
		if errors.Is(rErr, os.ErrNotExist) {
			return ErrAppPluginNotFound
		}
		return rErr
	}
	return nil
}

// PluginsDir returns the primary writable plugin directory (`<dataDir>/plugins`).
func (m *Manager) PluginsDir() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.pluginsDir
}

// ReloadPlugins rescans `<dataDir>/plugins/*.json` and `~/.colabhive/plugins/*.json`
// using parent-confined root I/O.
func (m *Manager) ReloadPlugins() {
	m.mu.RLock()
	primaryDir := m.pluginsDir
	extraDirs := append([]string(nil), m.extraPluginDirs...)
	m.mu.RUnlock()

	merged := make(map[string]*loadedPluginEntry)
	for _, dir := range extraDirs {
		for id, ent := range loadPluginsFromRootDir(dir, "user_home") {
			merged[id] = ent
		}
	}
	for id, ent := range loadPluginsFromRootDir(primaryDir, "data_dir") {
		merged[id] = ent
	}

	m.mu.Lock()
	m.plugins = merged
	m.mu.Unlock()
}

// ListPlugins returns all currently loaded third-party plugin manifests sorted by ID.
func (m *Manager) ListPlugins() []AppPluginManifest {
	m.ReloadPlugins()
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]AppPluginManifest, 0, len(m.plugins))
	for _, ent := range m.plugins {
		if ent != nil {
			out = append(out, clonePluginManifest(ent.manifest))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// InstallPlugin validates manifest, atomically persists it into `<dataDir>/plugins/<id>.json`
// using parent-confined root I/O (`os.OpenRoot`), updates the live registry, and returns the resulting AppTemplate.
func (m *Manager) InstallPlugin(manifest AppPluginManifest) (models.AppTemplate, error) {
	valid, err := ValidatePluginManifest(manifest)
	if err != nil {
		return models.AppTemplate{}, err
	}

	m.pluginWriteMu.Lock()
	defer m.pluginWriteMu.Unlock()

	m.mu.RLock()
	pluginsDir := m.pluginsDir
	m.mu.RUnlock()

	if err := writePluginManifestAtomic(pluginsDir, valid); err != nil {
		return models.AppTemplate{}, fmt.Errorf("install plugin %q: %w", valid.ID, err)
	}

	tpl := PluginToTemplate(valid)
	m.mu.Lock()
	if m.plugins == nil {
		m.plugins = make(map[string]*loadedPluginEntry)
	}
	m.plugins[valid.ID] = &loadedPluginEntry{
		manifest: valid,
		template: tpl,
		source:   "data_dir",
	}
	m.mu.Unlock()
	return cloneAppTemplate(tpl), nil
}

// DeletePlugin removes an installed third-party plugin `<id>.json` from `<dataDir>/plugins/`
// using parent-confined root I/O (`os.OpenRoot`). Built-in templates cannot be deleted.
func (m *Manager) DeletePlugin(id string) error {
	cleanID := strings.TrimSpace(id)
	if IsBuiltinTemplateID(cleanID) {
		return fmt.Errorf("forbidden: built-in app template %q cannot be deleted", cleanID)
	}
	if !pluginIDRe.MatchString(cleanID) {
		return fmt.Errorf("invalid plugin id %q", id)
	}

	m.pluginWriteMu.Lock()
	defer m.pluginWriteMu.Unlock()

	m.mu.RLock()
	pluginsDir := m.pluginsDir
	m.mu.RUnlock()

	if err := deletePluginManifestInRoot(pluginsDir, cleanID); err != nil {
		return err
	}

	m.ReloadPlugins()
	return nil
}

// ListUIPlugins returns all loaded v2 UI or bundle plugins (`kind == "ui" || kind == "bundle"`).
func (m *Manager) ListUIPlugins() []AppPluginManifest {
	m.ReloadPlugins()
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]AppPluginManifest, 0)
	for _, ent := range m.plugins {
		if ent == nil {
			continue
		}
		if ent.manifest.Kind == models.PluginKindUI || ent.manifest.Kind == models.PluginKindBundle {
			out = append(out, clonePluginManifest(ent.manifest))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// GetPluginManifest looks up a loaded third-party plugin manifest by ID (including its resolved RootDir).
func (m *Manager) GetPluginManifest(id string) (AppPluginManifest, bool) {
	m.ReloadPlugins()
	target := strings.ToLower(strings.TrimSpace(id))
	m.mu.RLock()
	defer m.mu.RUnlock()
	if ent, ok := m.plugins[target]; ok && ent != nil {
		return clonePluginManifest(ent.manifest), true
	}
	return AppPluginManifest{}, false
}

func clonePluginManifest(m AppPluginManifest) AppPluginManifest {
	cp := m
	cp.Highlights = append([]string(nil), m.Highlights...)
	cp.HighlightsZh = append([]string(nil), m.HighlightsZh...)
	cp.Provides = append([]string(nil), m.Provides...)
	cp.Consumes = append([]string(nil), m.Consumes...)
	cp.OptionalCapabilities = append([]string(nil), m.OptionalCapabilities...)
	cp.Permissions = append([]string(nil), m.Permissions...)
	cp.UI = clonePluginUIConfig(m.UI)
	if len(m.ModelOptions) > 0 {
		cp.ModelOptions = append([]models.AppModelOption(nil), m.ModelOptions...)
	}
	if m.FreeTierCompatible != nil {
		v := *m.FreeTierCompatible
		cp.FreeTierCompatible = &v
	}
	if m.SupportsPortBridge != nil {
		v := *m.SupportsPortBridge
		cp.SupportsPortBridge = &v
	}
	return cp
}
