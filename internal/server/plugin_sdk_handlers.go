package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/appstore"
	"github.com/colabhive/colabhive/internal/pluginhost"
)

const ctxKeyPluginSandbox ctxKey = "colabhive_plugin_sandbox"

func extractPluginToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	if h := strings.TrimSpace(r.Header.Get("X-ColabHive-Plugin-Token")); strings.HasPrefix(h, "ch_plug_") {
		return h
	}
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			cand := strings.TrimSpace(auth[7:])
			if strings.HasPrefix(cand, "ch_plug_") {
				return cand
			}
		}
	}
	if r.URL != nil {
		if q := strings.TrimSpace(r.URL.Query().Get("plugin_token")); strings.HasPrefix(q, "ch_plug_") {
			return q
		}
	}
	return ""
}

func withPluginSandbox(ctx context.Context, sess *pluginhost.SandboxSession) context.Context {
	return context.WithValue(ctx, ctxKeyPluginSandbox, sess)
}

func pluginSandboxFromRequest(r *http.Request) *pluginhost.SandboxSession {
	if r == nil {
		return nil
	}
	sess, _ := r.Context().Value(ctxKeyPluginSandbox).(*pluginhost.SandboxSession)
	return sess
}

// RegisterPluginPlatformV2Routes mounts ColabHive's open Plugin Platform v2 Host SDK (`/sdk/v1/*`):
// - Capability discovery & selectable default UI plugins
// - Isolated local UI plugin sandbox launcher
// - Persistent Local Asset Vault (cross-plugin image/video/audio sharing)
// - Standardized `media.v1` job runner & SSE live preview/progress stream
// - Cross-plugin capability invocations (e.g. `llm.openai.v1` prompt enhancement)
func RegisterPluginPlatformV2Routes(mux *http.ServeMux, host *pluginhost.Host, apps *appstore.Manager) {
	if mux == nil || host == nil {
		return
	}

	requirePerm := func(perm string, controlPlaneOnly bool, next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if sess := pluginSandboxFromRequest(r); sess != nil {
				if controlPlaneOnly {
					writeJSONError(w, http.StatusForbidden, "forbidden: endpoint requires ColabHive control plane")
					return
				}
				if perm != "" && !sess.HasPermission(perm) {
					writeJSONError(w, http.StatusForbidden, fmt.Sprintf("forbidden: plugin %q lacks required permission %q", sess.PluginID, perm))
					return
				}
			}
			next(w, r)
		}
	}

	// 1. Capabilities Discovery & Selectable UI Preferences
	mux.HandleFunc("GET /sdk/v1/capabilities", requirePerm("", false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, host.GetCapabilities())
	}))

	mux.HandleFunc("PUT /sdk/v1/ui-preferences/{capability}", requirePerm("", true, func(w http.ResponseWriter, r *http.Request) {
		capName := strings.TrimSpace(r.PathValue("capability"))
		var body struct {
			UIPluginID string `json:"ui_plugin_id"`
		}
		if err := decodeJSONBodyLimit(r, &body, 16<<10); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON request body: "+err.Error())
			return
		}
		if err := host.Sandbox().SetUIPreference(capName, body.UIPluginID); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"capability":     capName,
			"ui_plugin_id":   body.UIPluginID,
			"ui_preferences": host.Sandbox().GetUIPreferences(),
		})
	}))

	mux.HandleFunc("POST /sdk/v1/ui-plugins/{id}/open", requirePerm("", true, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		sess, err := host.OpenUIPlugin(id)
		if err != nil {
			if errors.Is(err, appstore.ErrAppPluginNotFound) {
				writeJSONError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, sess)
	}))

	mux.HandleFunc("POST /sdk/v1/ui-plugins/reload", requirePerm("", true, func(w http.ResponseWriter, r *http.Request) {
		if apps != nil {
			apps.ReloadPlugins()
		}
		writeJSON(w, http.StatusOK, host.GetCapabilities())
	}))

	// 2. Unified Local Asset Vault (cross-plugin persistence & pipeline handoff)
	mux.HandleFunc("GET /sdk/v1/assets", requirePerm("assets:read", false, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit, _ := strconv.Atoi(strings.TrimSpace(q.Get("limit")))
		if limit <= 0 || limit > 200 {
			limit = 100
		}
		items := host.Assets().List(pluginhost.AssetFilter{
			Kind:     pluginhost.AssetKind(strings.ToLower(strings.TrimSpace(q.Get("kind")))),
			EngineID: strings.TrimSpace(q.Get("engine_id")),
			Query:    strings.TrimSpace(q.Get("q")),
			Limit:    limit,
		})
		writeJSON(w, http.StatusOK, items)
	}))

	mux.HandleFunc("POST /sdk/v1/assets", requirePerm("assets:write", false, func(w http.ResponseWriter, r *http.Request) {
		ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
		sourcePlugin := "colabhive-host"
		if ps := pluginSandboxFromRequest(r); ps != nil {
			sourcePlugin = ps.PluginID
		}

		if strings.HasPrefix(ct, "multipart/form-data") {
			if err := r.ParseMultipartForm(64 << 20); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid multipart upload: "+err.Error())
				return
			}
			file, header, err := r.FormFile("file")
			if err != nil {
				writeJSONError(w, http.StatusBadRequest, "missing multipart 'file' field: "+err.Error())
				return
			}
			defer file.Close()

			var meta pluginhost.MediaAsset
			if rawMeta := strings.TrimSpace(r.FormValue("meta")); rawMeta != "" {
				_ = json.Unmarshal([]byte(rawMeta), &meta)
			}
			if meta.SourcePluginID == "" {
				meta.SourcePluginID = sourcePlugin
			}
			if meta.MimeType == "" && header != nil {
				meta.MimeType = header.Header.Get("Content-Type")
			}
			fname := "upload.png"
			if header != nil && strings.TrimSpace(header.Filename) != "" {
				fname = header.Filename
			}
			asset, sErr := host.Assets().Save(meta, fname, file)
			if sErr != nil {
				writeJSONError(w, http.StatusBadRequest, sErr.Error())
				return
			}
			writeJSON(w, http.StatusCreated, asset)
			return
		}

		var body struct {
			Filename       string `json:"filename"`
			MimeType       string `json:"mime_type"`
			DataBase64     string `json:"data_base64"`
			Prompt         string `json:"prompt"`
			ModelID        string `json:"model_id"`
			ModelName      string `json:"model_name"`
			ParentAssetID  string `json:"parent_asset_id"`
			SourcePluginID string `json:"source_plugin_id"`
		}
		if err := decodeJSONBodyLimit(r, &body, 64<<20); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON asset upload: "+err.Error())
			return
		}
		rawB64 := strings.TrimSpace(body.DataBase64)
		if idx := strings.Index(rawB64, ","); strings.HasPrefix(rawB64, "data:") && idx >= 0 {
			rawB64 = rawB64[idx+1:]
		}
		decoded, err := base64.StdEncoding.DecodeString(rawB64)
		if err != nil || len(decoded) == 0 {
			writeJSONError(w, http.StatusBadRequest, "invalid or empty data_base64 payload")
			return
		}
		if body.SourcePluginID == "" {
			body.SourcePluginID = sourcePlugin
		}
		asset, sErr := host.Assets().Save(pluginhost.MediaAsset{
			MimeType:       body.MimeType,
			Prompt:         body.Prompt,
			ModelID:        body.ModelID,
			ModelName:      body.ModelName,
			ParentAssetID:  body.ParentAssetID,
			SourcePluginID: body.SourcePluginID,
		}, body.Filename, bytes.NewReader(decoded))
		if sErr != nil {
			writeJSONError(w, http.StatusBadRequest, sErr.Error())
			return
		}
		writeJSON(w, http.StatusCreated, asset)
	}))

	mux.HandleFunc("GET /sdk/v1/assets/{id}", requirePerm("assets:read", false, func(w http.ResponseWriter, r *http.Request) {
		asset, err := host.Assets().Get(r.PathValue("id"))
		if err != nil {
			if errors.Is(err, pluginhost.ErrAssetNotFound) {
				writeJSONError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, asset)
	}))

	mux.HandleFunc("GET /sdk/v1/assets/{id}/file", requirePerm("assets:read", false, func(w http.ResponseWriter, r *http.Request) {
		host.Assets().ServeFile(w, r, r.PathValue("id"))
	}))

	mux.HandleFunc("DELETE /sdk/v1/assets/{id}", requirePerm("assets:write", false, func(w http.ResponseWriter, r *http.Request) {
		if err := host.Assets().Delete(r.PathValue("id")); err != nil {
			if errors.Is(err, pluginhost.ErrAssetNotFound) {
				writeJSONError(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": r.PathValue("id")})
	}))

	// 3. Standardized `media.v1` Engine Discovery, Models & Jobs Orchestrator
	mux.HandleFunc("GET /sdk/v1/media/engines", requirePerm("engines:read", false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, host.ListMediaEngines())
	}))

	mux.HandleFunc("GET /sdk/v1/media/instances/{id}/models", requirePerm("engines:read", false, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		refresh := r.URL.Query().Get("refresh") == "1" || strings.EqualFold(r.URL.Query().Get("refresh"), "true")
		modelsList, err := host.ListEngineModels(r.Context(), id, refresh)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"instance_id": id,
			"models":      modelsList,
		})
	}))

	mux.HandleFunc("GET /sdk/v1/media/instances/{id}/models/{modelId}/defaults", requirePerm("engines:read", false, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.PathValue("id"))
		modelID := strings.TrimSpace(r.PathValue("modelId"))
		out, err := host.GetModelDefaults(r.Context(), id, modelID)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, out)
	}))

	mux.HandleFunc("POST /sdk/v1/media/jobs", requirePerm("media:invoke", false, func(w http.ResponseWriter, r *http.Request) {
		var req pluginhost.MediaJobRequest
		if err := decodeJSONBodyLimit(r, &req, 1<<20); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid media job request: "+err.Error())
			return
		}
		if ps := pluginSandboxFromRequest(r); ps != nil && req.SourcePluginID == "" {
			req.SourcePluginID = ps.PluginID
		}
		job, err := host.CreateMediaJob(r.Context(), req)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, job)
	}))

	mux.HandleFunc("GET /sdk/v1/media/jobs", requirePerm("jobs:manage", false, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, host.ListMediaJobs())
	}))

	mux.HandleFunc("GET /sdk/v1/media/jobs/{id}", requirePerm("jobs:manage", false, func(w http.ResponseWriter, r *http.Request) {
		job, ok := host.GetMediaJob(r.PathValue("id"))
		if !ok {
			writeJSONError(w, http.StatusNotFound, "media job not found")
			return
		}
		writeJSON(w, http.StatusOK, job)
	}))

	mux.HandleFunc("POST /sdk/v1/media/jobs/{id}/cancel", requirePerm("jobs:manage", false, func(w http.ResponseWriter, r *http.Request) {
		job, err := host.CancelMediaJob(r.Context(), r.PathValue("id"))
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, job)
	}))

	mux.HandleFunc("GET /sdk/v1/media/jobs/{id}/events", requirePerm("jobs:manage", false, func(w http.ResponseWriter, r *http.Request) {
		snap, ch, unsub, ok := host.SubscribeJobEvents(r.PathValue("id"))
		if !ok {
			writeJSONError(w, http.StatusNotFound, "media job not found")
			return
		}
		defer unsub()

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		fl, _ := w.(http.Flusher)

		send := func(ev pluginhost.MediaJobEvent) bool {
			b, err := json.Marshal(ev)
			if err != nil {
				return true
			}
			if _, wErr := fmt.Fprintf(w, "data: %s\n\n", string(b)); wErr != nil {
				return false
			}
			if fl != nil {
				fl.Flush()
			}
			return true
		}

		// Initial snapshot so late-joining UI plugins immediately show current progress & preview!
		if !send(pluginhost.MediaJobEvent{
			Type:           "snapshot",
			JobID:          snap.ID,
			State:          snap.State,
			Phase:          snap.Phase,
			Status:         snap.StatusText,
			Progress:       snap.Progress,
			CurrentStep:    snap.CurrentStep,
			TotalSteps:     snap.TotalSteps,
			PreviewDataURL: snap.PreviewDataURL,
			Error:          snap.Error,
			Assets:         snap.Assets,
			Timestamp:      time.Now().UTC(),
		}) {
			return
		}
		if snap.State == pluginhost.MediaJobDone || snap.State == pluginhost.MediaJobError || snap.State == pluginhost.MediaJobCanceled {
			return
		}

		keepalive := time.NewTicker(12 * time.Second)
		defer keepalive.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-keepalive.C:
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			case ev := <-ch:
				if !send(ev) {
					return
				}
				if ev.Type == "done" || ev.Type == "error" || ev.Type == "canceled" {
					return
				}
			}
		}
	}))

	// 4. Cross-Plugin Capability Bus: `llm.openai.v1` Prompt Enhancement
	mux.HandleFunc("POST /sdk/v1/capabilities/llm.openai.v1/enhance-prompt", requirePerm("cap:llm.openai.v1", false, func(w http.ResponseWriter, r *http.Request) {
		var req pluginhost.EnhancePromptRequest
		if err := decodeJSONBodyLimit(r, &req, 64<<10); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid prompt enhance request: "+err.Error())
			return
		}
		resp, err := host.EnhancePrompt(r.Context(), req)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}))
}
