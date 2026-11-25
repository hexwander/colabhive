package pluginhost

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxAssetUploadBytes = 512 << 20 // 512 MiB cap per generated/uploaded media artifact
)

var (
	ErrAssetNotFound = errors.New("media asset not found")
	ErrAssetTooLarge = errors.New("media asset exceeds 512 MiB limit")
)

// AssetKind classifies stored local media artifacts.
type AssetKind string

const (
	AssetKindImage AssetKind = "image"
	AssetKindVideo AssetKind = "video"
	AssetKindAudio AssetKind = "audio"
)

// MediaAsset describes one persistently stored local media artifact in ColabHive's Unified Asset Vault.
// Because blobs and metadata live on local disk (`<dataDir>/assets/`), assets survive cloud GPU
// shutdowns, ColabHive restarts, and can be handed directly between plugins via `id` / `parent_asset_id`.
type MediaAsset struct {
	ID             string         `json:"id"`
	Kind           AssetKind      `json:"kind"`
	MimeType       string         `json:"mime_type"`
	Filename       string         `json:"filename"`
	SizeBytes      int64          `json:"size_bytes"`
	Prompt         string         `json:"prompt,omitempty"`
	NegativePrompt string         `json:"negative_prompt,omitempty"`
	ModelID        string         `json:"model_id,omitempty"`
	ModelName      string         `json:"model_name,omitempty"`
	Mode           string         `json:"mode,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	Steps          int            `json:"steps,omitempty"`
	Seed           int64          `json:"seed,omitempty"`
	DurationSec    float64        `json:"duration_sec,omitempty"`
	GenerationMS   int64          `json:"generation_ms,omitempty"`
	EngineID       string         `json:"engine_id,omitempty"`
	InstanceID     string         `json:"instance_id,omitempty"`
	JobID          string         `json:"job_id,omitempty"`
	SourcePluginID string         `json:"source_plugin_id,omitempty"`
	ParentAssetID  string         `json:"parent_asset_id,omitempty"`
	Extra          map[string]any `json:"extra,omitempty"`
	FileURL        string         `json:"file_url"`
	CreatedAt      time.Time      `json:"created_at"`
}

// AssetFilter narrows `AssetVault.List` queries.
type AssetFilter struct {
	Kind     AssetKind
	EngineID string
	Query    string
	Limit    int
}

// AssetVault persists generated and uploaded media artifacts on local disk for cross-plugin reuse.
type AssetVault struct {
	mu       sync.RWMutex
	baseDir  string
	blobsDir string
	index    map[string]*MediaAsset
}

// NewAssetVault initializes a local persistent Asset Vault under `<dataDir>/assets`.
func NewAssetVault(dataDir string) *AssetVault {
	base := strings.TrimSpace(dataDir)
	if base == "" || base == ":memory:" {
		base = filepath.Join(os.TempDir(), "colabhive-assets")
	} else {
		base = filepath.Join(base, "assets")
	}
	v := &AssetVault{
		baseDir:  base,
		blobsDir: filepath.Join(base, "blobs"),
		index:    make(map[string]*MediaAsset),
	}
	_ = os.MkdirAll(v.blobsDir, 0o755)
	v.loadIndex()
	return v
}

func (v *AssetVault) indexPath() string {
	return filepath.Join(v.baseDir, "index.json")
}

func (v *AssetVault) loadIndex() {
	raw, err := os.ReadFile(v.indexPath())
	if err != nil || len(raw) == 0 {
		return
	}
	var items []MediaAsset
	if json.Unmarshal(raw, &items) != nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range items {
		it := items[i]
		if it.ID == "" || it.Filename == "" {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(v.blobsDir, it.Filename)); statErr != nil {
			continue
		}
		it.FileURL = "/sdk/v1/assets/" + it.ID + "/file"
		cp := it
		v.index[it.ID] = &cp
	}
}

func (v *AssetVault) persistIndexLocked() {
	items := make([]MediaAsset, 0, len(v.index))
	for _, a := range v.index {
		if a != nil {
			items = append(items, *a)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(v.baseDir, 0o755)
	tmp := v.indexPath() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, v.indexPath())
}

func newAssetID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "ast_" + hex.EncodeToString(b[:])
}

func detectKindAndExt(filename, mimeType string) (AssetKind, string, string) {
	ext := strings.ToLower(strings.TrimSpace(filepath.Ext(filename)))
	mt := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if ext == "" && mt != "" {
		if exts, err := mime.ExtensionsByType(mt); err == nil && len(exts) > 0 {
			ext = strings.ToLower(exts[0])
		}
	}
	switch ext {
	case ".mp4", ".webm", ".mov", ".mkv":
		if mt == "" {
			if ext == ".webm" {
				mt = "video/webm"
			} else {
				mt = "video/mp4"
			}
		}
		return AssetKindVideo, ext, mt
	case ".wav", ".mp3", ".ogg", ".flac", ".m4a":
		if mt == "" {
			mt = mime.TypeByExtension(ext)
			if mt == "" {
				mt = "audio/wav"
			}
		}
		return AssetKindAudio, ext, mt
	case ".jpg", ".jpeg":
		return AssetKindImage, ".jpg", "image/jpeg"
	case ".webp":
		return AssetKindImage, ".webp", "image/webp"
	case ".gif":
		return AssetKindImage, ".gif", "image/gif"
	case ".png":
		return AssetKindImage, ".png", "image/png"
	default:
		if strings.HasPrefix(mt, "video/") {
			return AssetKindVideo, ".mp4", mt
		}
		if strings.HasPrefix(mt, "audio/") {
			return AssetKindAudio, ".wav", mt
		}
		return AssetKindImage, ".png", "image/png"
	}
}

// Save streams data into the local Asset Vault atomically and persists its metadata.
func (v *AssetVault) Save(meta MediaAsset, origFilename string, r io.Reader) (*MediaAsset, error) {
	if r == nil {
		return nil, errors.New("asset content reader is required")
	}
	if err := os.MkdirAll(v.blobsDir, 0o755); err != nil {
		return nil, err
	}

	id := newAssetID()
	kind, ext, mimeType := detectKindAndExt(origFilename, meta.MimeType)
	if meta.Kind != "" {
		kind = meta.Kind
	}
	blobName := id + ext
	blobPath := filepath.Join(v.blobsDir, blobName)
	tmpPath := blobPath + ".tmp"

	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	n, copyErr := io.Copy(f, io.LimitReader(r, maxAssetUploadBytes+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return nil, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return nil, closeErr
	}
	if n == 0 {
		_ = os.Remove(tmpPath)
		return nil, errors.New("cannot save empty media asset")
	}
	if n > maxAssetUploadBytes {
		_ = os.Remove(tmpPath)
		return nil, ErrAssetTooLarge
	}
	if err := os.Rename(tmpPath, blobPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, err
	}

	now := time.Now().UTC()
	asset := meta
	asset.ID = id
	asset.Kind = kind
	asset.MimeType = mimeType
	asset.Filename = blobName
	asset.SizeBytes = n
	asset.FileURL = "/sdk/v1/assets/" + id + "/file"
	if asset.CreatedAt.IsZero() {
		asset.CreatedAt = now
	}

	v.mu.Lock()
	v.index[id] = &asset
	v.persistIndexLocked()
	cp := cloneAsset(&asset)
	v.mu.Unlock()
	return cp, nil
}

// List returns stored assets newest-first matching filter.
func (v *AssetVault) List(filter AssetFilter) []*MediaAsset {
	v.mu.RLock()
	defer v.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(filter.Query))
	out := make([]*MediaAsset, 0, len(v.index))
	for _, a := range v.index {
		if a == nil {
			continue
		}
		if filter.Kind != "" && a.Kind != filter.Kind {
			continue
		}
		if filter.EngineID != "" && !strings.EqualFold(a.EngineID, filter.EngineID) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(a.Prompt + " " + a.ModelName + " " + a.ModelID + " " + a.ID)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, cloneAsset(a))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out
}

// Get returns metadata for a single asset by ID.
func (v *AssetVault) Get(id string) (*MediaAsset, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	a, ok := v.index[strings.TrimSpace(id)]
	if !ok || a == nil {
		return nil, ErrAssetNotFound
	}
	return cloneAsset(a), nil
}

// OpenBlob opens the raw media file on disk for streaming or remote upload staging.
func (v *AssetVault) OpenBlob(id string) (*MediaAsset, *os.File, error) {
	a, err := v.Get(id)
	if err != nil {
		return nil, nil, err
	}
	if strings.ContainsAny(a.Filename, `/\`) || strings.Contains(a.Filename, "..") {
		return nil, nil, fmt.Errorf("unsafe asset filename %q", a.Filename)
	}
	f, err := os.Open(filepath.Join(v.blobsDir, a.Filename))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, ErrAssetNotFound
		}
		return nil, nil, err
	}
	return a, f, nil
}

// ServeFile serves the asset binary with Range support (for smooth MP4 scrubbing) and immutable caching.
func (v *AssetVault) ServeFile(w http.ResponseWriter, r *http.Request, id string) {
	asset, f, err := v.OpenBlob(id)
	if err != nil {
		if errors.Is(err, ErrAssetNotFound) {
			http.Error(w, "asset not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", asset.MimeType)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, asset.Filename, asset.CreatedAt, f)
}

// Delete removes an asset's metadata and local blob from disk.
func (v *AssetVault) Delete(id string) error {
	cleanID := strings.TrimSpace(id)
	v.mu.Lock()
	a, ok := v.index[cleanID]
	if !ok || a == nil {
		v.mu.Unlock()
		return ErrAssetNotFound
	}
	fname := a.Filename
	delete(v.index, cleanID)
	v.persistIndexLocked()
	v.mu.Unlock()

	if fname != "" && !strings.ContainsAny(fname, `/\`) && !strings.Contains(fname, "..") {
		_ = os.Remove(filepath.Join(v.blobsDir, fname))
	}
	return nil
}

func cloneAsset(a *MediaAsset) *MediaAsset {
	if a == nil {
		return nil
	}
	cp := *a
	if len(a.Extra) > 0 {
		cp.Extra = make(map[string]any, len(a.Extra))
		for k, v := range a.Extra {
			cp.Extra[k] = v
		}
	}
	return &cp
}
