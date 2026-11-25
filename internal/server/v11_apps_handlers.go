package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/colabhive/colabhive/internal/appstore"
	"github.com/colabhive/colabhive/internal/checkpoint"
	"github.com/colabhive/colabhive/internal/models"
)

// RegisterV11AppsAndCheckpointRoutes mounts ColabHive v1.1 Pillar 2 (Plugin-Based 1-Click AI App Store &
// Private Port Bridge) and Pillar 4 (Auto-Checkpoint Relay & Shareable Savings Receipt) routes.
func RegisterV11AppsAndCheckpointRoutes(
	mux *http.ServeMux,
	apps *appstore.Manager,
	ckpt *checkpoint.Manager,
	st models.Store,
	pool models.AccountPoolService,
	rt models.RuntimeEngine,
	cloudMode bool,
) {
	if mux == nil {
		return
	}
	if apps != nil {
		apps.SetCloudMode(cloudMode)
	}

	// -------------------------------------------------------------------------
	// Pillar 2: Plugin-Based 1-Click AI App Store & Private Port Bridge
	// -------------------------------------------------------------------------

	mux.HandleFunc("GET /api/v1/apps/templates", func(w http.ResponseWriter, r *http.Request) {
		if apps != nil {
			writeJSON(w, http.StatusOK, apps.ListTemplates())
			return
		}
		writeJSON(w, http.StatusOK, appstore.CuratedTemplates())
	})

	mux.HandleFunc("GET /api/v1/apps/plugins", func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSON(w, http.StatusOK, []appstore.AppPluginManifest{})
			return
		}
		writeJSON(w, http.StatusOK, apps.ListPlugins())
	})

	mux.HandleFunc("POST /api/v1/apps/plugins", func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "app store manager is not initialized")
			return
		}
		var manifest appstore.AppPluginManifest
		if err := decodeJSONBodyLimit(r, &manifest, appstore.MaxPluginManifestBytes); err != nil {
			if errors.Is(err, errRequestBodyTooLarge) {
				writeJSONError(w, http.StatusRequestEntityTooLarge, appstore.ErrPluginManifestTooLarge.Error())
				return
			}
			writeJSONError(w, http.StatusBadRequest, "invalid plugin manifest JSON: "+err.Error())
			return
		}
		tpl, err := apps.InstallPlugin(manifest)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, tpl)
	})

	mux.HandleFunc("DELETE /api/v1/apps/plugins/{id}", func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "app store manager is not initialized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		if err := apps.DeletePlugin(id); err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted": true,
			"id":      id,
		})
	})

	mux.HandleFunc("GET /api/v1/apps/instances", func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSON(w, http.StatusOK, []*models.AppInstance{})
			return
		}
		writeJSON(w, http.StatusOK, apps.ListInstances())
	})

	mux.HandleFunc("GET /api/v1/apps/instances/{id}", func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "app store manager is not initialized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		inst, err := apps.GetInstance(id)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, inst)
	})

	mux.HandleFunc("PATCH /api/v1/apps/instances/{id}", func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "app store manager is not initialized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		var patch appstore.AppInstanceConfigPatch
		if err := decodeJSONBodyLimit(r, &patch, 64<<10); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON request body: "+err.Error())
			return
		}
		inst, err := apps.UpdateConfig(r.Context(), id, patch)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, inst)
	})

	mux.HandleFunc("POST /api/v1/apps/launch", func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "app store manager is not initialized")
			return
		}
		var req models.AppLaunchRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON request body: "+err.Error())
			return
		}
		if cloudMode {
			// Hardened cloud mode: lock off host 127.0.0.1 port binding to prevent loopback probing.
			req.PreferredLocalPort = 0
		}
		inst, err := apps.Launch(r.Context(), req)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, inst)
	})

	stopInstanceHandler := func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "app store manager is not initialized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		force := false
		if r.URL != nil {
			switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("force"))) {
			case "true", "1", "yes":
				force = true
			}
		}
		inst, err := apps.StopWithOptions(r.Context(), id, force)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, inst)
	}
	mux.HandleFunc("POST /api/v1/apps/instances/{id}/stop", stopInstanceHandler)
	mux.HandleFunc("DELETE /api/v1/apps/instances/{id}", stopInstanceHandler)

	mux.HandleFunc("POST /api/v1/apps/instances/{id}/restart", func(w http.ResponseWriter, r *http.Request) {
		if apps == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "app store manager is not initialized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		inst, err := apps.Restart(r.Context(), id)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, inst)
	})

	// Web Proxy endpoints (`/apps/proxy/{id}/` & `/apps/proxy/{id}/{rest...}`).
	// P0 Same-Origin XSS isolation: in cloud mode (or any non-demo protected deployment),
	// interactive Studio HTML MUST NOT be served on the control-plane origin -> return HTTP 422 JSON.
	mux.HandleFunc("/apps/proxy/{id}", func(w http.ResponseWriter, r *http.Request) {
		if cloudMode {
			writeJSONError(w, http.StatusUnprocessableEntity, appstore.ErrIsolatedOriginRequired.Error())
			return
		}
		if apps != nil {
			if err := apps.CheckSameOriginProxyAllowed(r); err != nil {
				writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
				return
			}
		}
		id := strings.TrimSpace(r.PathValue("id"))
		if !isSafePathSegment(id) {
			writeJSONError(w, http.StatusBadRequest, "invalid app instance id")
			return
		}
		target := "/apps/proxy/" + id + "/"
		if r.URL != nil && r.URL.RawQuery != "" {
			if cleanQ := appstore.SanitizeProxyRawQuery(r.URL.RawQuery); cleanQ != "" {
				target += "?" + cleanQ
			}
		}
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	})

	mux.HandleFunc("/apps/proxy/{id}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
		if cloudMode {
			writeJSONError(w, http.StatusUnprocessableEntity, appstore.ErrIsolatedOriginRequired.Error())
			return
		}
		if apps == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "app store manager is not initialized")
			return
		}
		if err := apps.CheckSameOriginProxyAllowed(r); err != nil {
			writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		if !isSafePathSegment(id) {
			writeJSONError(w, http.StatusBadRequest, "invalid app instance id")
			return
		}
		rest := r.PathValue("rest")
		apps.ServeProxyHTTP(w, r, id, rest)
	})

	// -------------------------------------------------------------------------
	// Pillar 4: Auto-Checkpoint Preemption Relay & Shareable Savings Receipt
	// -------------------------------------------------------------------------

	mux.HandleFunc("GET /api/v1/checkpoints/policy", func(w http.ResponseWriter, r *http.Request) {
		if ckpt == nil {
			writeJSON(w, http.StatusOK, models.DefaultCheckpointPolicy())
			return
		}
		writeJSON(w, http.StatusOK, ckpt.GetPolicy())
	})

	updatePolicyHandler := func(w http.ResponseWriter, r *http.Request) {
		if ckpt == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "checkpoint manager is not initialized")
			return
		}
		req := ckpt.GetPolicy()
		if err := decodeJSONBody(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON request body: "+err.Error())
			return
		}
		updated, err := ckpt.UpdatePolicy(req)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, updated)
	}
	mux.HandleFunc("PUT /api/v1/checkpoints/policy", updatePolicyHandler)
	mux.HandleFunc("PATCH /api/v1/checkpoints/policy", updatePolicyHandler)
	mux.HandleFunc("POST /api/v1/checkpoints/policy", updatePolicyHandler)

	mux.HandleFunc("GET /api/v1/checkpoints", func(w http.ResponseWriter, r *http.Request) {
		if ckpt == nil {
			writeJSON(w, http.StatusOK, []*models.CheckpointRecord{})
			return
		}
		writeJSON(w, http.StatusOK, ckpt.ListCheckpoints())
	})

	mux.HandleFunc("POST /api/v1/checkpoints/sync", func(w http.ResponseWriter, r *http.Request) {
		if ckpt == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "checkpoint manager is not initialized")
			return
		}
		var req models.CheckpointSyncRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON request body: "+err.Error())
			return
		}
		rec, err := ckpt.SyncSessionCheckpoint(r.Context(), req)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})

	mux.HandleFunc("POST /api/v1/checkpoints/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		if ckpt == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "checkpoint manager is not initialized")
			return
		}
		id := strings.TrimSpace(r.PathValue("id"))
		var req models.CheckpointResumeRequest
		if err := decodeJSONBody(r, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON request body: "+err.Error())
			return
		}
		rec, err := ckpt.ResumeFromCheckpoint(r.Context(), id, req)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})

	savingsHandler := func(w http.ResponseWriter, r *http.Request) {
		stats, err := checkpoint.ComputeSavingsReceipt(r.Context(), st, pool, rt, ckpt)
		if err != nil {
			writeJSONError(w, mapV11AppsErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, stats)
	}
	mux.HandleFunc("GET /api/v1/stats/savings-card", savingsHandler)
	mux.HandleFunc("GET /api/v1/stats/savings-receipt", savingsHandler)
}

func mapV11AppsErrorStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if errors.Is(err, appstore.ErrAppInstanceNotFound) ||
		errors.Is(err, appstore.ErrAppPluginNotFound) ||
		errors.Is(err, checkpoint.ErrCheckpointNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, appstore.ErrIsolatedOriginRequired) {
		return http.StatusUnprocessableEntity
	}
	if errors.Is(err, appstore.ErrPluginManifestTooLarge) {
		return http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, appstore.ErrPreStopCheckpointSyncFailed) {
		return http.StatusConflict
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(err.Error())), "invalid ") {
		return http.StatusBadRequest
	}
	return mapServiceErrorStatus(err)
}
