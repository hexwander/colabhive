package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/colabhive/colabhive/internal/models"
)

func lookupEnv(key string) (string, bool) {
	return os.LookupEnv(key)
}

func isValidASCIIExperimentIDMCP(id string) bool {
	if len(id) == 0 || len(id) > 128 || id[0] == '.' || id[len(id)-1] == '.' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	stem := id
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}

func isSafeMCPPathSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	if strings.ContainsAny(seg, "/\\:\x00") {
		return false
	}
	return true
}

func canAccessHostFilesystem(cctx callContext, cloudMode bool) bool {
	return !cloudMode && cctx.transport == models.TransportMCPStdio && cctx.apiKey == nil
}

func (s *Server) canAccessHostFS(cctx callContext) bool {
	return canAccessHostFilesystem(cctx, s.isCloudMode())
}

func (s *Server) registerTool(def ToolDefinition, fn toolHandlerFunc) {
	s.tools = append(s.tools, def)
	s.toolMap[def.Name] = fn
}

func (s *Server) registerAllTools() {
	// ========================================================================
	// Suite A — Official Google Colab MCP Compatible Tools
	// ========================================================================

	s.registerTool(ToolDefinition{
		Name:        "list_runtime_specs",
		Description: "List all supported Google Colab CPU/GPU/TPU hardware specifications, VRAM capacities, and reference Compute Unit burn rates.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
	}, s.toolListRuntimeSpecs)

	s.registerTool(ToolDefinition{
		Name:        "get_subscription",
		Description: "Retrieve pooled Google Colab subscription tiers, paid Compute Unit balances, Free milli-CCU refill timers, and GPU-Hour Equivalence across accounts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"refresh": map[string]any{"type": "boolean", "default": false, "description": "Force a live ccu-info / v1/user-info refresh."},
			},
			"additionalProperties": false,
		},
	}, s.toolGetSubscription)

	s.registerTool(ToolDefinition{
		Name:        "create_runtime",
		Description: "Provision a persistent Google Colab CPU/GPU/TPU runtime from the multi-account pool and bind it to this MCP session.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":          map[string]any{"type": "string", "description": "Optional human-readable runtime/session name."},
				"accelerator":   map[string]any{"type": "string", "enum": []string{"G4", "H100", "A100", "L4", "T4", "V6E1", "V5E1", "CPU"}, "default": "G4"},
				"gpu":           map[string]any{"type": "string", "enum": []string{"G4", "H100", "A100", "L4", "T4"}},
				"high_mem":      map[string]any{"type": "boolean", "default": true},
				"strategy":      map[string]any{"type": "string", "enum": []string{"waste-zero", "max-balance", "sticky-session", "round-robin", "free-opportunistic"}},
				"account":       map[string]any{"type": "string", "description": "Optional account ID, email, or tag:<name>."},
				"auto_fallback": map[string]any{"type": "boolean", "default": true},
			},
		},
	}, s.toolCreateRuntime)

	s.registerTool(ToolDefinition{
		Name:        "list_runtimes",
		Description: "List active Google Colab runtimes and Jupyter kernel sessions across the pool.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":       map[string]any{"type": "string"},
				"experiment_id": map[string]any{"type": "string"},
				"active_only":   map[string]any{"type": "boolean", "default": true},
			},
		},
	}, s.toolListRuntimes)

	s.registerTool(ToolDefinition{
		Name:        "delete_runtime",
		Description: "Terminate and unassign a Colab runtime immediately so zero idle Compute Units are wasted.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"runtime": map[string]any{"type": "string", "description": "Runtime/session ID or name (defaults to sticky session)."},
				"session": map[string]any{"type": "string", "description": "Alias for runtime session ID or name."},
			},
		},
	}, s.toolDeleteRuntime)

	s.registerTool(ToolDefinition{
		Name:        "open_colab_browser_connection",
		Description: "Attach or create a pooled Colab runtime and return the instant Colab Web UI URL (#datalabBackendUrl=...) attached to the same live kernel.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":      map[string]any{"type": "string"},
				"gpu":          map[string]any{"type": "string", "enum": []string{"T4", "L4", "G4", "A100", "H100"}},
				"open_browser": map[string]any{"type": "boolean", "default": false},
			},
		},
	}, s.toolOpenBrowserConnection)

	s.registerTool(ToolDefinition{
		Name:        "execute_code",
		Description: "Execute stateful Python/IPython (!shell) code inside the persistent Jupyter kernel (/content). Auto-provisions a runtime if none is bound.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code":          map[string]any{"type": "string", "description": "Python or IPython code to run in /content."},
				"session":       map[string]any{"type": "string", "description": "Target session ID/name (defaults to sticky session)."},
				"gpu":           map[string]any{"type": "string", "enum": []string{"T4", "L4", "G4", "A100", "H100"}},
				"timeout_sec":   map[string]any{"type": "number", "default": 120},
				"inline_images": map[string]any{"type": "boolean", "default": false, "description": "Include base64 ImageContent blocks alongside out-of-band artifact URLs."},
			},
			"required": []string{"code"},
		},
	}, s.toolExecuteCode)

	s.registerTool(ToolDefinition{
		Name:        "get_cells",
		Description: "Retrieve cells from the session's virtual Colab notebook.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":         map[string]any{"type": "string"},
				"include_outputs": map[string]any{"type": "boolean", "default": true},
			},
		},
	}, s.toolGetCells)

	s.registerTool(ToolDefinition{
		Name:        "add_code_cell",
		Description: "Insert a Python code cell into the session's notebook and optionally execute it.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"code":    map[string]any{"type": "string"},
				"index":   map[string]any{"type": "integer"},
				"run":     map[string]any{"type": "boolean", "default": false},
				"session": map[string]any{"type": "string"},
			},
			"required": []string{"code"},
		},
	}, s.toolAddCodeCell)

	s.registerTool(ToolDefinition{
		Name:        "add_text_cell",
		Description: "Insert a Markdown cell into the session's notebook.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{"type": "string"},
				"index":   map[string]any{"type": "integer"},
				"session": map[string]any{"type": "string"},
			},
			"required": []string{"content"},
		},
	}, s.toolAddTextCell)

	s.registerTool(ToolDefinition{
		Name:        "run_code_cell",
		Description: "Execute an existing notebook code cell by cell_id.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cell_id":     map[string]any{"type": "string"},
				"timeout_sec": map[string]any{"type": "number", "default": 120},
				"session":     map[string]any{"type": "string"},
			},
			"required": []string{"cell_id"},
		},
	}, s.toolRunCodeCell)

	s.registerTool(ToolDefinition{
		Name:        "update_cell",
		Description: "Update the source content of an existing notebook cell.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cell_id": map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
				"session": map[string]any{"type": "string"},
			},
			"required": []string{"cell_id", "content"},
		},
	}, s.toolUpdateCell)

	s.registerTool(ToolDefinition{
		Name:        "delete_cell",
		Description: "Delete a cell from the session's notebook.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cell_id": map[string]any{"type": "string"},
				"session": map[string]any{"type": "string"},
			},
			"required": []string{"cell_id"},
		},
	}, s.toolDeleteCell)

	s.registerTool(ToolDefinition{
		Name:        "move_cell",
		Description: "Move a cell to a new 0-based index in the session's notebook.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cell_id":   map[string]any{"type": "string"},
				"new_index": map[string]any{"type": "integer"},
				"session":   map[string]any{"type": "string"},
			},
			"required": []string{"cell_id", "new_index"},
		},
	}, s.toolMoveCell)

	// ========================================================================
	// Suite B — Colab CLI Headless Session, File & Package Tools
	// ========================================================================

	s.registerTool(ToolDefinition{
		Name:        "colab_session_create",
		Description: "Allocate a persistent Google Colab CPU/GPU/TPU VM from the multi-account pool with HTTP 400/412 failover.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":       map[string]any{"type": "string"},
				"gpu":           map[string]any{"type": "string", "enum": []string{"G4", "A100", "H100", "L4", "T4"}},
				"tpu":           map[string]any{"type": "string", "enum": []string{"v6e1", "v5e1"}},
				"high_mem":      map[string]any{"type": "boolean", "default": true},
				"strategy":      map[string]any{"type": "string", "default": "waste-zero"},
				"account":       map[string]any{"type": "string"},
				"auto_fallback": map[string]any{"type": "boolean", "default": true},
				"experiment_id": map[string]any{"type": "string"},
			},
			"required": []string{"session"},
		},
	}, s.toolCreateRuntime)

	s.registerTool(ToolDefinition{
		Name:        "colab_session_list",
		Description: "List active Colab sessions across pooled accounts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"account":       map[string]any{"type": "string"},
				"experiment_id": map[string]any{"type": "string"},
				"sync_backend":  map[string]any{"type": "boolean", "default": false},
			},
		},
	}, s.toolListRuntimes)

	s.registerTool(ToolDefinition{
		Name:        "colab_session_status",
		Description: "Inspect detailed GPU VRAM/SM%/Temp telemetry and `/proc/{pid}/stat` anti-zombie health for a session.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string"},
			},
			"required": []string{"session"},
		},
	}, s.toolSessionStatus)

	s.registerTool(ToolDefinition{
		Name:        "colab_session_stop",
		Description: "Stop a Colab session and immediately unassign its VM.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string"},
			},
			"required": []string{"session"},
		},
	}, s.toolDeleteRuntime)

	s.registerTool(ToolDefinition{
		Name:        "colab_kernel_restart",
		Description: "Restart the Python kernel (clearing RAM/VRAM while keeping /content and /root/.cache/huggingface intact).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string"},
			},
			"required": []string{"session"},
		},
	}, s.toolKernelRestart)

	s.registerTool(ToolDefinition{
		Name:        "colab_kernel_interrupt",
		Description: "Send SIGINT (KeyboardInterrupt) to the running kernel execution.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string"},
			},
			"required": []string{"session"},
		},
	}, s.toolKernelInterrupt)

	s.registerTool(ToolDefinition{
		Name:        "colab_exec",
		Description: "Execute Python code or a local `.py`/`.ipynb` file on a named Colab session with optional env vars.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":     map[string]any{"type": "string"},
				"code":        map[string]any{"type": "string"},
				"local_file":  map[string]any{"type": "string"},
				"env":         map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
				"timeout_sec": map[string]any{"type": "number", "default": 120},
			},
			"required": []string{"session"},
		},
	}, s.toolColabExec)

	s.registerTool(ToolDefinition{
		Name:        "colab_file_upload",
		Description: "Upload inline content/base64 (or a local file over local stdio) to `/content/...` on a Colab VM.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":        map[string]any{"type": "string"},
				"local_path":     map[string]any{"type": "string"},
				"remote_path":    map[string]any{"type": "string"},
				"content":        map[string]any{"type": "string", "description": "Optional inline UTF-8 content when local_path is omitted."},
				"content_base64": map[string]any{"type": "string", "description": "Optional base64 bytes when local_path is omitted."},
			},
			"required": []string{"remote_path"},
		},
	}, s.toolFileUpload)

	s.registerTool(ToolDefinition{
		Name:        "colab_file_download",
		Description: "Download a remote file from `/content/...` on a Colab VM.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":     map[string]any{"type": "string"},
				"remote_path": map[string]any{"type": "string"},
				"local_path":  map[string]any{"type": "string"},
			},
			"required": []string{"remote_path"},
		},
	}, s.toolFileDownload)

	s.registerTool(ToolDefinition{
		Name:        "colab_file_list",
		Description: "List files and directories under `/content` on a Colab VM.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":     map[string]any{"type": "string"},
				"remote_path": map[string]any{"type": "string", "default": "/content"},
			},
		},
	}, s.toolFileList)

	s.registerTool(ToolDefinition{
		Name:        "colab_file_ls",
		Description: "Alias for colab_file_list: list directory entries under `/content` on a Colab VM.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":     map[string]any{"type": "string"},
				"remote_path": map[string]any{"type": "string", "default": "/content"},
			},
		},
	}, s.toolFileList)

	s.registerTool(ToolDefinition{
		Name:        "colab_file_rm",
		Description: "Delete a remote file on a Colab VM.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":     map[string]any{"type": "string"},
				"remote_path": map[string]any{"type": "string"},
			},
			"required": []string{"remote_path"},
		},
	}, s.toolFileRemove)

	s.registerTool(ToolDefinition{
		Name:        "colab_install_packages",
		Description: "Install Python packages on a Colab VM via fast `uv pip install --system`.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session":           map[string]any{"type": "string"},
				"packages":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"requirements_path": map[string]any{"type": "string"},
				"requirements_txt":  map[string]any{"type": "string"},
			},
		},
	}, s.toolInstallPackages)

	s.registerTool(ToolDefinition{
		Name:        "colab_session_url",
		Description: "Return the instant Colab Web UI URL attached to an active headless runtime.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session": map[string]any{"type": "string"},
			},
		},
	}, s.toolSessionURL)

	// ========================================================================
	// Suite C — Multi-Account Pool Observatory & Sharded Research Experiments
	// ========================================================================

	s.registerTool(ToolDefinition{
		Name:        "pool_quota_summary",
		Description: "Query live Compute Unit balances, burn rates, contributor reserves, and Cloud GPU-Hour Equivalence across pooled Google accounts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"refresh_live": map[string]any{"type": "boolean", "default": false},
				"tag":          map[string]any{"type": "string"},
			},
		},
	}, s.toolPoolQuotaSummary)

	s.registerTool(ToolDefinition{
		Name:        "experiment_launch_sharded",
		Description: "Allocate K parallel GPU VMs across pooled accounts, upload scripts/files, inject COLABHIVE_SHARD_ID=0..K-1, and attach anti-zombie watchdogs.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"experiment_id":         map[string]any{"type": "string"},
				"title":                 map[string]any{"type": "string"},
				"script_path":           map[string]any{"type": "string"},
				"script_code":           map[string]any{"type": "string"},
				"script_args":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"upload_files":          map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"packages":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"gpu":                   map[string]any{"type": "string", "enum": []string{"G4", "A100", "H100", "L4", "T4"}, "default": "G4"},
				"high_mem":              map[string]any{"type": "boolean", "default": true},
				"num_shards":            map[string]any{"type": "integer", "default": 4},
				"strategy":              map[string]any{"type": "string", "default": "waste-zero"},
				"local_output_dir":      map[string]any{"type": "string"},
				"auto_stop_on_complete": map[string]any{"type": "boolean", "default": true},
				"env":                   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			},
			"required": []string{"experiment_id"},
		},
	}, s.toolExperimentLaunchSharded)

	s.registerTool(ToolDefinition{
		Name:        "experiment_status",
		Description: "Inspect non-blocking shard progress, `/proc/{pid}/stat` anti-zombie health, GPU VRAM, and synced JSONL record counts.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"experiment_id": map[string]any{"type": "string"},
			},
			"required": []string{"experiment_id"},
		},
	}, s.toolExperimentStatus)

	s.registerTool(ToolDefinition{
		Name:        "experiment_collect_and_teardown",
		Description: "Merge `/content/shard_{0..K-1}.jsonl` into `merged.jsonl`, export `.ipynb` provenance logs, and guarantee zero-leak VM unassignment across all shards.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"experiment_id":      map[string]any{"type": "string"},
				"force_kill_running": map[string]any{"type": "boolean", "default": false},
			},
			"required": []string{"experiment_id"},
		},
	}, s.toolExperimentCollectAndTeardown)
}

// ============================================================================
// Principal Authorization & Session Resolution Helpers
// ============================================================================

func (s *Server) authorizeSession(ctx context.Context, cctx callContext, sessIDOrName string) (*models.RuntimeSession, error) {
	if s.runtime == nil {
		return nil, errors.New("runtime engine unavailable")
	}
	sess, err := s.runtime.GetSession(ctx, sessIDOrName)
	if err != nil {
		return nil, err
	}
	if sess == nil {
		return nil, fmt.Errorf("session %q not found", sessIDOrName)
	}
	if cctx.apiKey != nil {
		if sess.AdoptedExternal {
			return nil, errors.New("forbidden: scoped API key cannot access external Colab sessions")
		}
		if sess.APIKeyID != cctx.apiKey.ID {
			return nil, errors.New("forbidden: session belongs to a different API key or principal")
		}
	}
	return sess, nil
}

func (s *Server) authorizeExperiment(ctx context.Context, cctx callContext, expID string) (*models.Experiment, error) {
	if s.runtime == nil {
		return nil, errors.New("runtime engine unavailable")
	}
	cleanID := strings.TrimSpace(expID)
	if cleanID == "" {
		return nil, errors.New("experiment_id is required")
	}
	exp, err := s.runtime.GetExperiment(ctx, cleanID)
	if err != nil {
		return nil, err
	}
	if exp == nil {
		return nil, fmt.Errorf("experiment %q not found", cleanID)
	}
	if cctx.apiKey != nil {
		if exp.APIKeyID != "" {
			if exp.APIKeyID != cctx.apiKey.ID {
				return nil, errors.New("forbidden: experiment belongs to a different API key or principal")
			}
			return exp, nil
		}
		matchedAny := false
		for _, sh := range exp.Shards {
			if sh.SessionID == "" {
				continue
			}
			sess, sErr := s.runtime.GetSession(ctx, sh.SessionID)
			if sErr != nil || sess == nil || sess.ID != sh.SessionID || sess.AdoptedExternal || sess.APIKeyID != cctx.apiKey.ID {
				return nil, errors.New("forbidden: experiment belongs to a different API key or principal")
			}
			matchedAny = true
		}
		if !matchedAny {
			return nil, errors.New("forbidden: experiment belongs to a different API key or principal")
		}
	}
	return exp, nil
}

func (s *Server) resolveStickyForCaller(ctx context.Context, stickyKey string, apiKeyID string) (*models.RuntimeSession, bool) {
	if s.router == nil || strings.TrimSpace(stickyKey) == "" {
		return nil, false
	}
	if sr, ok := s.router.(scopedStickyRouter); ok {
		return sr.ResolveStickySessionForKey(ctx, stickyKey, apiKeyID)
	}
	if apiKeyID != "" {
		return nil, false
	}
	return s.router.ResolveStickySession(ctx, stickyKey)
}

func (s *Server) resolveTargetSessionID(ctx context.Context, cctx callContext, args map[string]any) (string, error) {
	explicit := firstStr(argStr(args, "session"), argStr(args, "runtime"), argStr(args, "session_id"))
	if explicit != "" {
		sess, err := s.authorizeSession(ctx, cctx, explicit)
		if err != nil {
			return "", err
		}
		return sess.ID, nil
	}
	if cctx.stickyKey != "" && s.router != nil {
		if sess, ok := s.resolveStickyForCaller(ctx, cctx.stickyKey, cctx.apiKeyID()); ok && sess != nil && !sess.AdoptedExternal {
			if cctx.apiKey == nil || sess.APIKeyID == cctx.apiKey.ID {
				return sess.ID, nil
			}
		}
	}
	if s.runtime != nil {
		list, err := s.runtime.ListSessions(ctx, models.SessionFilter{ActiveOnly: true})
		if err == nil {
			var managed []*models.RuntimeSession
			for _, item := range list {
				if item == nil || item.AdoptedExternal {
					continue
				}
				if item.Status == models.SessionStopped || item.Status == models.SessionLost {
					continue
				}
				if cctx.apiKey != nil && item.APIKeyID != cctx.apiKey.ID {
					continue
				}
				managed = append(managed, item)
			}
			if len(managed) == 1 {
				return managed[0].ID, nil
			}
		}
	}
	return "", errors.New("no target session specified and no active sticky session bound; pass 'session' or call create_runtime first")
}

// ============================================================================
// Tool Handlers
// ============================================================================

func (s *Server) toolListRuntimeSpecs(_ context.Context, _ callContext, _ map[string]any) (*CallToolResult, traceSummary, error) {
	return jsonToolResult(map[string]any{
		"runtime_specs": models.HardwareCatalog,
	}), traceSummary{}, nil
}

func (s *Server) toolGetSubscription(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	return s.toolPoolQuotaSummary(ctx, cctx, map[string]any{
		"refresh_live": argBool(args, "refresh", false),
	})
}

func accountMatchesAllowedGroups(acc *models.Account, allowedGroups []string) bool {
	if acc == nil {
		return false
	}
	if len(allowedGroups) == 0 {
		return true
	}
	for _, want := range allowedGroups {
		for _, g := range acc.Groups {
			if strings.EqualFold(strings.TrimSpace(g), strings.TrimSpace(want)) {
				return true
			}
		}
	}
	return false
}

func gpuAllowedForAPIKey(gpu models.AcceleratorType, allowedGPUs []models.AcceleratorType) bool {
	if len(allowedGPUs) == 0 {
		return true
	}
	for _, g := range allowedGPUs {
		if g == gpu {
			return true
		}
	}
	return false
}

func (s *Server) toolPoolQuotaSummary(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	refresh := (argBool(args, "refresh_live", false) || argBool(args, "refresh", false)) && cctx.apiKey == nil
	var summary *models.QuotaSummary
	var accounts []*models.Account
	var err error

	if s.pool != nil {
		if refresh {
			summary, err = s.pool.RefreshAllQuotas(ctx)
		} else {
			summary, err = s.pool.GetQuotaSummary(ctx)
		}
		accounts, _ = s.pool.ListAccounts(ctx)
	}
	if err != nil {
		return nil, traceSummary{}, err
	}
	if summary == nil {
		summary = &models.QuotaSummary{
			GPUEquivalence: map[models.AcceleratorType]models.GPUHourEquivalence{},
		}
	}
	if accounts == nil {
		accounts = []*models.Account{}
	}
	tagFilter := strings.TrimSpace(argStr(args, "tag"))
	if tagFilter != "" {
		filtered := make([]*models.Account, 0, len(accounts))
		for _, a := range accounts {
			if a == nil {
				continue
			}
			for _, t := range a.Tags {
				if strings.EqualFold(t, tagFilter) {
					filtered = append(filtered, a)
					break
				}
			}
		}
		accounts = filtered
	}

	// Scoped `ch_sk_...` callers: ignore live refresh, restrict summary to allowed groups/GPUs,
	// redact owner account emails/sensitive pool metadata, and return key remaining budget!
	if cctx.apiKey != nil {
		key := cctx.apiKey
		scopedSum := &models.QuotaSummary{
			GPUEquivalence: make(map[models.AcceleratorType]models.GPUHourEquivalence),
			UpdatedAt:      time.Now().UTC(),
		}
		eligibleAccIDs := make(map[string]bool)
		if s.pool != nil {
			var eligibleAccs []*models.Account
			for _, a := range accounts {
				if a == nil || a.Health == models.HealthRevokedByUser {
					continue
				}
				if !accountMatchesAllowedGroups(a, key.AllowedGroups) {
					continue
				}
				eligibleAccs = append(eligibleAccs, a)
				eligibleAccIDs[a.ID] = true
				scopedSum.AccountsTotal++
				if a.Health.IsSchedulable() && a.Enabled {
					scopedSum.AccountsHealthy++
				}
				scopedSum.TotalAvailableCU += a.PaidCUBalance
				sched := a.SchedulableCU
				if sched <= 0 && a.PaidCUBalance > a.Guardrails.MinReserveCU {
					sched = a.PaidCUBalance - a.Guardrails.MinReserveCU
				}
				scopedSum.SchedulableCU += sched
				slots := a.MaxConcurrentVMs
				if slots <= 0 {
					slots = 2
				}
				scopedSum.TotalVMSlots += slots
			}
			if len(eligibleAccs) > 0 && key.MaxConcurrentVMs > 0 && (scopedSum.TotalVMSlots == 0 || key.MaxConcurrentVMs < scopedSum.TotalVMSlots) {
				scopedSum.TotalVMSlots = key.MaxConcurrentVMs
			}
			scopedSum.TotalAvailableCU = math.Round(scopedSum.TotalAvailableCU*100) / 100
			scopedSum.SchedulableCU = math.Round(scopedSum.SchedulableCU*100) / 100
			for _, spec := range models.HardwareCatalog {
				if !gpuAllowedForAPIKey(spec.Accelerator, key.AllowedGPUs) {
					continue
				}
				rate := spec.DefaultBurnRateCUHr
				availHrs := 0.0
				schedHrs := 0.0
				if rate > 0 {
					availHrs = math.Round((scopedSum.TotalAvailableCU/rate)*100) / 100
					schedHrs = math.Round((scopedSum.SchedulableCU/rate)*100) / 100
				}
				scopedSum.GPUEquivalence[spec.Accelerator] = models.GPUHourEquivalence{
					Accelerator:       spec.Accelerator,
					DisplayName:       spec.DisplayName,
					ShortLabel:        spec.ShortLabel,
					VRAMGB:            spec.VRAMGB,
					BurnRateCUHr:      rate,
					CalibrationSource: models.CalibrationReference,
					AvailableHours:    availHrs,
					SchedulableHours:  schedHrs,
					EligibleAccounts:  scopedSum.AccountsHealthy,
					MaxParallelSlots:  scopedSum.TotalVMSlots,
				}
			}
		} else if tagFilter == "" {
			// Fallback when s.pool == nil and no tag filter is requested: copy aggregate totals while redacting sensitive owner fields.
			cp := *summary
			cp.AccountsContributor = 0
			cp.EarliestBatchExpiresAt = nil
			cp.EarliestBatchExpiryCU = 0
			cp.SavedIdleWasteCU = 0
			cp.AutoTeardownsCount = 0
			cp.ZombieAbortsCount = 0
			scopedSum = &cp
		}

		if s.runtime != nil {
			if list, lErr := s.runtime.ListSessions(ctx, models.SessionFilter{ActiveOnly: true}); lErr == nil {
				activeOwn := 0
				burnOwn := 0.0
				for _, sess := range list {
					if sess == nil || sess.AdoptedExternal || sess.APIKeyID != key.ID ||
						sess.Status == models.SessionStopped || sess.Status == models.SessionLost {
						continue
					}
					if s.pool != nil && !eligibleAccIDs[sess.AccountID] {
						continue
					}
					activeOwn++
					burnOwn += sess.BurnRateCUHr
				}
				scopedSum.ActiveVMs = activeOwn
				scopedSum.ActiveBurnRateCUHr = math.Round(burnOwn*100) / 100
			}
		}

		budgetInfo := map[string]any{
			"key_name":            key.Name,
			"key_prefix":          key.KeyPrefix,
			"max_budget_cu_total": key.MaxBudgetCUTotal,
			"used_cu_total":       key.UsedCUTotal,
			"max_budget_cu_daily": key.MaxBudgetCUDaily,
			"used_cu_today":       key.UsedCUToday,
			"max_concurrent_vms":  key.MaxConcurrentVMs,
			"allowed_groups":      key.AllowedGroups,
			"allowed_gpus":        key.AllowedGPUs,
		}
		if key.MaxBudgetCUTotal > 0 {
			remTotal := math.Max(0, math.Round((key.MaxBudgetCUTotal-key.UsedCUTotal)*100)/100)
			budgetInfo["remaining_cu_total"] = remTotal
			if remTotal < scopedSum.SchedulableCU {
				scopedSum.SchedulableCU = remTotal
			}
		}
		if key.MaxBudgetCUDaily > 0 {
			remDaily := math.Max(0, math.Round((key.MaxBudgetCUDaily-key.UsedCUToday)*100)/100)
			budgetInfo["remaining_cu_daily"] = remDaily
		}

		return jsonToolResult(map[string]any{
			"pool_totals":    scopedSum,
			"summary":        scopedSum,
			"accounts":       []any{},
			"api_key_budget": budgetInfo,
		}), traceSummary{}, nil
	}

	if tagFilter != "" {
		summary = s.computeTagFilteredSummary(ctx, accounts)
	}

	return jsonToolResult(map[string]any{
		"pool_totals": summary,
		"summary":     summary,
		"accounts":    accounts,
	}), traceSummary{}, nil
}

func (s *Server) computeTagFilteredSummary(ctx context.Context, accounts []*models.Account) *models.QuotaSummary {
	sum := &models.QuotaSummary{
		GPUEquivalence: make(map[models.AcceleratorType]models.GPUHourEquivalence, len(models.HardwareCatalog)),
		UpdatedAt:      time.Now().UTC(),
	}
	allowedAccIDs := make(map[string]bool, len(accounts))
	for _, a := range accounts {
		if a == nil || a.Health == models.HealthRevokedByUser {
			continue
		}
		allowedAccIDs[a.ID] = true
		sum.AccountsTotal++
		if a.Health.IsSchedulable() && a.Enabled {
			sum.AccountsHealthy++
		}
		if a.ActiveAssignments > 0 {
			sum.AccountsWorking++
		}
		if a.IsContributor {
			sum.AccountsContributor++
		}
		sum.TotalAvailableCU += a.PaidCUBalance
		sched := a.SchedulableCU
		if sched <= 0 && a.PaidCUBalance > a.Guardrails.MinReserveCU {
			sched = a.PaidCUBalance - a.Guardrails.MinReserveCU
		}
		sum.SchedulableCU += sched
		slots := a.MaxConcurrentVMs
		if slots <= 0 {
			slots = 2
		}
		sum.TotalVMSlots += slots
		if a.FreeQuota != nil && a.FreeQuota.RemainingMilliCCU > 0 {
			sum.FreeT4WindowsReady++
		}
	}
	sum.TotalAvailableCU = math.Round(sum.TotalAvailableCU*100) / 100
	sum.SchedulableCU = math.Round(sum.SchedulableCU*100) / 100
	sum.EstimatedValueUSD = math.Round(sum.TotalAvailableCU*0.10*100) / 100

	if s.runtime != nil && len(allowedAccIDs) > 0 {
		if list, lErr := s.runtime.ListSessions(ctx, models.SessionFilter{ActiveOnly: true}); lErr == nil {
			for _, sess := range list {
				if sess != nil && allowedAccIDs[sess.AccountID] &&
					sess.Status != models.SessionStopped && sess.Status != models.SessionLost {
					sum.ActiveVMs++
					sum.ActiveBurnRateCUHr += sess.BurnRateCUHr
					if spec, ok := models.GetHardwareSpec(sess.Accelerator); ok {
						sum.TotalOnlineVRAMGB += spec.VRAMGB
					}
				}
			}
			sum.ActiveBurnRateCUHr = math.Round(sum.ActiveBurnRateCUHr*100) / 100
		}
	}

	for _, spec := range models.HardwareCatalog {
		rate := spec.DefaultBurnRateCUHr
		availHrs := 0.0
		schedHrs := 0.0
		if rate > 0 {
			availHrs = math.Round((sum.TotalAvailableCU/rate)*100) / 100
			schedHrs = math.Round((sum.SchedulableCU/rate)*100) / 100
		}
		sum.GPUEquivalence[spec.Accelerator] = models.GPUHourEquivalence{
			Accelerator:       spec.Accelerator,
			DisplayName:       spec.DisplayName,
			ShortLabel:        spec.ShortLabel,
			VRAMGB:            spec.VRAMGB,
			BurnRateCUHr:      rate,
			CalibrationSource: models.CalibrationReference,
			AvailableHours:    availHrs,
			SchedulableHours:  schedHrs,
			EligibleAccounts:  sum.AccountsHealthy,
			MaxParallelSlots:  sum.TotalVMSlots,
		}
	}
	return sum
}

func (s *Server) toolCreateRuntime(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	if s.runtime == nil {
		return nil, traceSummary{}, errors.New("runtime engine unavailable")
	}

	rawAccel := firstStr(argStr(args, "gpu"), argStr(args, "tpu"), argStr(args, "accelerator"), "G4")
	accel, err := models.NormalizeAccelerator(rawAccel)
	if err != nil {
		return nil, traceSummary{}, err
	}

	strat := models.RoutingStrategy(firstStr(argStr(args, "strategy"), string(cctx.defaultStrategy)))
	accSel := firstStr(argStr(args, "account"), cctx.defaultAccount)
	name := firstStr(argStr(args, "session"), argStr(args, "name"))

	sess, err := s.runtime.CreateSession(ctx, models.SessionAllocRequest{
		Name:            name,
		Accelerator:     accel,
		HighMem:         argBool(args, "high_mem", true),
		Strategy:        strat,
		AccountSelector: accSel,
		AllowedGroups:   cctx.allowedGroups(),
		AutoFallback:    argBool(args, "auto_fallback", true),
		StickyKey:       cctx.stickyKey,
		APIKeyID:        cctx.apiKeyID(),
		CallerName:      cctx.clientName,
		ExperimentID:    argStr(args, "experiment_id"),
	})
	if err != nil {
		return nil, traceSummary{reqGPU: accel}, err
	}

	fb := ""
	if sess.FallbackApplied {
		fb = fmt.Sprintf("%s -> %s", sess.RequestedAccelerator, sess.Accelerator)
	}

	payload := map[string]any{
		"session":                   sess.Name,
		"id":                        sess.ID,
		"endpoint":                  sess.Endpoint,
		"variant":                   sess.Variant,
		"requested_accelerator":     sess.RequestedAccelerator,
		"assigned_accelerator":      sess.Accelerator,
		"fallback_applied":          sess.FallbackApplied,
		"machine_shape":             sess.Shape,
		"estimated_burn_rate_cu_hr": sess.BurnRateCUHr,
		"colab_web_url":             sess.ColabWebURL,
	}
	if cctx.apiKey == nil {
		payload["account_id"] = sess.AccountID
		payload["account_email"] = sess.AccountEmail
	}
	return jsonToolResult(payload), traceSummary{
		sessionID:    sess.ID,
		accountID:    sess.AccountID,
		accountEmail: sess.AccountEmail,
		reqGPU:       accel,
		assignedGPU:  sess.Accelerator,
		fallback:     fb,
	}, nil
}

var mcpEmailRedactRegex = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

func redactEmailsInMCPText(s string, knownEmail string) string {
	if s == "" {
		return ""
	}
	if knownEmail != "" {
		s = strings.ReplaceAll(s, " ("+knownEmail+")", "")
		s = strings.ReplaceAll(s, knownEmail, "pooled-account")
	}
	return mcpEmailRedactRegex.ReplaceAllString(s, "pooled-account")
}

func redactSessionForScopedKey(sess *models.RuntimeSession) *models.RuntimeSession {
	if sess == nil {
		return nil
	}
	cp := *sess
	origEmail := sess.AccountEmail
	cp.AccountEmail = ""
	cp.AccountID = ""
	cp.StatusMessage = redactEmailsInMCPText(cp.StatusMessage, origEmail)
	if len(sess.Timeline) > 0 {
		tl := make([]models.ExecutionStep, len(sess.Timeline))
		for i, step := range sess.Timeline {
			step.Label = redactEmailsInMCPText(step.Label, origEmail)
			step.Detail = redactEmailsInMCPText(step.Detail, origEmail)
			tl[i] = step
		}
		cp.Timeline = tl
	}
	if len(sess.RecentLogs) > 0 {
		logs := make([]string, len(sess.RecentLogs))
		for i, line := range sess.RecentLogs {
			logs[i] = redactEmailsInMCPText(line, origEmail)
		}
		cp.RecentLogs = logs
	}
	if len(sess.Cells) > 0 {
		cells := make([]models.NotebookCell, len(sess.Cells))
		for i, cell := range sess.Cells {
			if len(cell.Outputs) > 0 {
				outs := make([]models.KernelRichOutput, len(cell.Outputs))
				for j, ro := range cell.Outputs {
					ro.TextData = redactEmailsInMCPText(ro.TextData, origEmail)
					ro.Title = redactEmailsInMCPText(ro.Title, origEmail)
					outs[j] = ro
				}
				cell.Outputs = outs
			}
			cells[i] = cell
		}
		cp.Cells = cells
	}
	return &cp
}

func (s *Server) pooledEmailsForRedaction(ctx context.Context, primary string) []string {
	var out []string
	seen := make(map[string]bool)
	add := func(em string) {
		em = strings.TrimSpace(em)
		if em == "" {
			return
		}
		lw := strings.ToLower(em)
		if !seen[lw] {
			seen[lw] = true
			out = append(out, em)
		}
	}
	add(primary)
	if s.pool != nil {
		if accs, err := s.pool.ListAccounts(ctx); err == nil {
			for _, a := range accs {
				if a != nil {
					add(a.Email)
				}
			}
		}
	}
	return out
}

func replaceKnownMCPEmailsCI(text string, emails []string) string {
	if text == "" || len(emails) == 0 {
		return text
	}
	for _, em := range emails {
		if em == "" {
			continue
		}
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(em))
		text = re.ReplaceAllString(text, "pooled-account")
	}
	return text
}

func redactMCPRichOutputs(outs []models.KernelRichOutput, knownEmails []string) []models.KernelRichOutput {
	if len(outs) == 0 || len(knownEmails) == 0 {
		return outs
	}
	res := make([]models.KernelRichOutput, len(outs))
	for i, ro := range outs {
		ro.TextData = replaceKnownMCPEmailsCI(ro.TextData, knownEmails)
		ro.Title = replaceKnownMCPEmailsCI(ro.Title, knownEmails)
		res[i] = ro
	}
	return res
}

func redactExecResultForScopedKey(res *models.KernelExecResult, knownEmails []string) *models.KernelExecResult {
	if res == nil {
		return nil
	}
	cp := *res
	cp.Stdout = replaceKnownMCPEmailsCI(cp.Stdout, knownEmails)
	cp.Stderr = replaceKnownMCPEmailsCI(cp.Stderr, knownEmails)
	cp.Result = replaceKnownMCPEmailsCI(cp.Result, knownEmails)
	cp.RichOutputs = redactMCPRichOutputs(cp.RichOutputs, knownEmails)
	if cp.ExecutionError != nil {
		errCp := *cp.ExecutionError
		errCp.Value = replaceKnownMCPEmailsCI(errCp.Value, knownEmails)
		if len(errCp.Traceback) > 0 {
			tb := make([]string, len(errCp.Traceback))
			for i, ln := range errCp.Traceback {
				tb[i] = replaceKnownMCPEmailsCI(ln, knownEmails)
			}
			errCp.Traceback = tb
		}
		cp.ExecutionError = &errCp
	}
	return &cp
}

func redactExperimentForScopedKey(exp *models.Experiment) *models.Experiment {
	if exp == nil {
		return nil
	}
	cp := *exp
	cp.LocalOutputDir = ""
	cp.MergedJSONLPath = ""
	if len(exp.Shards) > 0 {
		shards := make([]models.ExperimentShard, len(exp.Shards))
		for i, sh := range exp.Shards {
			origEmail := sh.AccountEmail
			sh.AccountEmail = ""
			sh.AccountID = ""
			if sh.LogTail != "" {
				sh.LogTail = redactEmailsInMCPText(sh.LogTail, origEmail)
			}
			shards[i] = sh
		}
		cp.Shards = shards
	}
	return &cp
}

func redactTeardownResultForScopedKey(res *models.ExperimentTeardownResult) *models.ExperimentTeardownResult {
	if res == nil {
		return nil
	}
	cp := *res
	cp.MergedJSONLPath = ""
	cp.ExportedNotebooks = nil
	return &cp
}

func (s *Server) toolListRuntimes(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	if s.runtime == nil {
		return jsonToolResult(map[string]any{"sessions": []any{}}), traceSummary{}, nil
	}
	list, err := s.runtime.ListSessions(ctx, models.SessionFilter{
		AccountID:    argStr(args, "account"),
		ExperimentID: argStr(args, "experiment_id"),
		ActiveOnly:   argBool(args, "active_only", false),
		SyncBackend:  argBool(args, "sync_backend", false) && cctx.apiKey == nil,
	})
	if err != nil {
		return nil, traceSummary{}, err
	}
	if cctx.apiKey != nil {
		scoped := make([]*models.RuntimeSession, 0, len(list))
		for _, sess := range list {
			if sess != nil && !sess.AdoptedExternal && sess.APIKeyID == cctx.apiKey.ID {
				scoped = append(scoped, redactSessionForScopedKey(sess))
			}
		}
		list = scoped
	}
	if list == nil {
		list = []*models.RuntimeSession{}
	}
	return jsonToolResult(map[string]any{"sessions": list}), traceSummary{}, nil
}

func (s *Server) toolSessionStatus(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	sess, err := s.authorizeSession(ctx, cctx, sid)
	if err != nil {
		return nil, traceSummary{}, err
	}
	outSess := sess
	if cctx.apiKey != nil {
		outSess = redactSessionForScopedKey(sess)
	}
	return jsonToolResult(outSess), traceSummary{
		sessionID:    sess.ID,
		accountID:    sess.AccountID,
		accountEmail: sess.AccountEmail,
		assignedGPU:  sess.Accelerator,
	}, nil
}

func (s *Server) toolStopRuntime(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	sess, err := s.authorizeSession(ctx, cctx, sid)
	if err != nil {
		return nil, traceSummary{}, err
	}
	if err := s.runtime.StopSession(ctx, sess.ID); err != nil {
		return nil, traceSummary{sessionID: sess.ID}, err
	}
	tr := traceSummary{
		sessionID:    sess.ID,
		accountID:    sess.AccountID,
		accountEmail: sess.AccountEmail,
		assignedGPU:  sess.Accelerator,
	}
	return jsonToolResult(map[string]any{
		"session":              sess.ID,
		"unassigned":           true,
		"endpoint":             sess.Endpoint,
		"cu_consumed_estimate": sess.AccumulatedCU,
	}), tr, nil
}

func (s *Server) toolDeleteRuntime(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	return s.toolStopRuntime(ctx, cctx, args)
}

func (s *Server) toolOpenBrowserConnection(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	if s.runtime == nil {
		return nil, traceSummary{}, errors.New("runtime engine unavailable")
	}
	sid := argStr(args, "session")
	var sess *models.RuntimeSession
	var err error
	if sid != "" {
		if existing, gErr := s.runtime.GetSession(ctx, sid); gErr == nil && existing != nil {
			authSess, aErr := s.authorizeSession(ctx, cctx, existing.ID)
			if aErr != nil {
				return nil, traceSummary{}, aErr
			}
			sess = authSess
		}
	} else if cctx.stickyKey != "" && s.router != nil {
		if sticky, ok := s.resolveStickyForCaller(ctx, cctx.stickyKey, cctx.apiKeyID()); ok && sticky != nil && !sticky.AdoptedExternal {
			if cctx.apiKey != nil && sticky.APIKeyID != cctx.apiKey.ID {
				return nil, traceSummary{}, errors.New("forbidden: sticky session belongs to a different API key or principal")
			}
			sess = sticky
		}
	}
	if sess == nil {
		gpu, nErr := models.NormalizeAccelerator(firstStr(argStr(args, "gpu"), "G4"))
		if nErr != nil {
			return nil, traceSummary{}, nErr
		}
		sess, err = s.runtime.CreateSession(ctx, models.SessionAllocRequest{
			Name:            sid,
			Accelerator:     gpu,
			HighMem:         true,
			Strategy:        cctx.effectiveStrategy(argStr(args, "strategy")),
			AccountSelector: cctx.effectiveAccount(argStr(args, "account")),
			AllowedGroups:   cctx.allowedGroups(),
			AutoFallback:    true,
			StickyKey:       cctx.stickyKey,
			APIKeyID:        cctx.apiKeyID(),
			CallerName:      cctx.clientName,
		})
		if err != nil {
			return nil, traceSummary{reqGPU: gpu}, err
		}
	}
	webURL, _ := s.runtime.GetColabWebURL(ctx, sess.ID)
	payload := map[string]any{
		"connected":     true,
		"result":        true,
		"session":       sess.Name,
		"accelerator":   sess.Accelerator,
		"colab_web_url": webURL,
	}
	if cctx.apiKey == nil {
		payload["account_email"] = sess.AccountEmail
	}
	return jsonToolResult(payload), traceSummary{
		sessionID:    sess.ID,
		accountID:    sess.AccountID,
		accountEmail: sess.AccountEmail,
		assignedGPU:  sess.Accelerator,
	}, nil
}

func (s *Server) toolExecuteCode(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	if s.runtime == nil {
		return nil, traceSummary{}, errors.New("runtime engine unavailable")
	}
	code := argStr(args, "code")
	if strings.TrimSpace(code) == "" {
		return nil, traceSummary{}, errors.New("'code' is required")
	}
	var autoGPU models.AcceleratorType
	if g := argStr(args, "gpu"); g != "" {
		if norm, err := models.NormalizeAccelerator(g); err == nil {
			autoGPU = norm
		}
	} else {
		autoGPU = models.AcceleratorG4
	}

	explicitSession := argStr(args, "session")
	if explicitSession != "" {
		if existing, gErr := s.runtime.GetSession(ctx, explicitSession); gErr == nil && existing != nil {
			authSess, aErr := s.authorizeSession(ctx, cctx, existing.ID)
			if aErr != nil {
				return nil, traceSummary{reqGPU: autoGPU}, aErr
			}
			explicitSession = authSess.ID
		}
	} else if cctx.apiKey != nil && cctx.stickyKey != "" && s.router != nil {
		if bound, ok := s.resolveStickyForCaller(ctx, cctx.stickyKey, cctx.apiKeyID()); ok && bound != nil {
			if bound.AdoptedExternal || bound.APIKeyID != cctx.apiKey.ID {
				return nil, traceSummary{reqGPU: autoGPU}, errors.New("forbidden: sticky session belongs to a different API key or principal")
			}
		}
	}

	inlineImg := argBool(args, "inline_images", false)
	res, err := s.runtime.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID:        explicitSession,
		Code:             code,
		IdleTimeoutSec:   argFloat(args, "timeout_sec", 120),
		StoreHistory:     true,
		InlineImages:     inlineImg,
		AutoProvisionGPU: autoGPU,
		StickyKey:        cctx.stickyKey,
		CallerName:       cctx.clientName,
		APIKeyID:         cctx.apiKeyID(),
		Strategy:         cctx.effectiveStrategy(argStr(args, "strategy")),
		AccountSelector:  cctx.effectiveAccount(argStr(args, "account")),
	})
	if err != nil {
		return nil, traceSummary{reqGPU: autoGPU}, err
	}

	// Do NOT populate `cuDelta` here (#129 P1-4): CU burn is tracked canonically by the runtime watchdog loop.
	tr := traceSummary{reqGPU: autoGPU}
	if sess, sErr := s.runtime.GetSession(ctx, res.Session); sErr == nil && sess != nil {
		tr.sessionID = sess.ID
		tr.accountID = sess.AccountID
		tr.accountEmail = sess.AccountEmail
		tr.assignedGPU = sess.Accelerator
	}

	if cctx.apiKey != nil {
		res = redactExecResultForScopedKey(res, s.pooledEmailsForRedaction(ctx, tr.accountEmail))
	}

	// Build Google-compliant ExecuteCodeResult JSON + optional native MCP ImageContent blocks.
	summaryCopy := *res
	if !inlineImg && len(summaryCopy.RichOutputs) > 0 {
		stripped := make([]models.KernelRichOutput, len(summaryCopy.RichOutputs))
		for i, ro := range summaryCopy.RichOutputs {
			ro.InlineBase64 = ""
			stripped[i] = ro
		}
		summaryCopy.RichOutputs = stripped
	}
	encoded, _ := json.MarshalIndent(summaryCopy, "", "  ")
	blocks := []ContentBlock{{Type: "text", Text: string(encoded)}}
	if inlineImg {
		for _, ro := range res.RichOutputs {
			if ro.InlineBase64 != "" {
				mime := "image/png"
				for _, m := range ro.MimeTypes {
					if strings.HasPrefix(m, "image/") {
						mime = m
						break
					}
				}
				blocks = append(blocks, ContentBlock{
					Type:     "image",
					Data:     ro.InlineBase64,
					MimeType: mime,
				})
			}
		}
	}

	return &CallToolResult{
		Content:           blocks,
		StructuredContent: summaryCopy,
		IsError:           res.Status == "error",
	}, tr, nil
}

func (s *Server) toolColabExec(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	localFile := strings.TrimSpace(argStr(args, "local_file"))
	if localFile != "" && !s.canAccessHostFS(cctx) {
		return nil, traceSummary{}, errors.New("host filesystem 'local_file' is not permitted over remote or API-key MCP; pass inline 'code' instead")
	}
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	envMap := argStringMap(args, "env")
	res, err := s.runtime.ExecuteCode(ctx, models.KernelExecRequest{
		SessionID:       sid,
		Code:            argStr(args, "code"),
		LocalFilePath:   localFile,
		Env:             envMap,
		IdleTimeoutSec:  argFloat(args, "timeout_sec", 120),
		StoreHistory:    true,
		StickyKey:       cctx.stickyKey,
		CallerName:      cctx.clientName,
		APIKeyID:        cctx.apiKeyID(),
		Strategy:        cctx.effectiveStrategy(argStr(args, "strategy")),
		AccountSelector: cctx.effectiveAccount(argStr(args, "account")),
	})
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	if cctx.apiKey != nil {
		knownEmail := ""
		if sess, sErr := s.runtime.GetSession(ctx, sid); sErr == nil && sess != nil {
			knownEmail = sess.AccountEmail
		}
		res = redactExecResultForScopedKey(res, s.pooledEmailsForRedaction(ctx, knownEmail))
	}
	return jsonToolResult(res), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolGetCells(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	cells, err := s.runtime.GetCells(ctx, sid, argBool(args, "include_outputs", true))
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	if cctx.apiKey != nil && len(cells) > 0 {
		knownEmails := s.pooledEmailsForRedaction(ctx, "")
		redacted := make([]models.NotebookCell, len(cells))
		for i, c := range cells {
			c.Outputs = redactMCPRichOutputs(c.Outputs, knownEmails)
			redacted[i] = c
		}
		cells = redacted
	}
	return jsonToolResult(map[string]any{"session": sid, "cells": cells}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolAddCodeCell(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	explicitSess := firstStr(argStr(args, "session"), argStr(args, "runtime"), argStr(args, "session_id"))
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		if explicitSess != "" || strings.Contains(strings.ToLower(err.Error()), "forbidden") {
			return nil, traceSummary{}, err
		}
		// Auto-provision sticky session ONLY when no explicit session was requested!
		sess, cErr := s.runtime.CreateSession(ctx, models.SessionAllocRequest{
			Accelerator:     models.AcceleratorG4,
			HighMem:         true,
			Strategy:        cctx.effectiveStrategy(argStr(args, "strategy")),
			AccountSelector: cctx.effectiveAccount(argStr(args, "account")),
			AllowedGroups:   cctx.allowedGroups(),
			AutoFallback:    true,
			StickyKey:       cctx.stickyKey,
			APIKeyID:        cctx.apiKeyID(),
			CallerName:      cctx.clientName,
		})
		if cErr != nil {
			return nil, traceSummary{}, cErr
		}
		sid = sess.ID
	}
	idxPtr := argOptionalInt(args, "index")
	runNow := argBool(args, "run", false)
	cell, execRes, err := s.runtime.MutateCell(ctx, sid, models.CellMutationRequest{
		Operation: "add_code",
		Content:   argStr(args, "code"),
		Index:     idxPtr,
		RunOnAdd:  runNow,
	})
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	if cctx.apiKey != nil && execRes != nil {
		execRes = redactExecResultForScopedKey(execRes, s.pooledEmailsForRedaction(ctx, ""))
	}
	return jsonToolResult(map[string]any{
		"cell_id":   cell.CellID,
		"index":     cell.Index,
		"executed":  runNow,
		"execution": execRes,
	}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolAddTextCell(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	cell, _, err := s.runtime.MutateCell(ctx, sid, models.CellMutationRequest{
		Operation: "add_text",
		Content:   argStr(args, "content"),
		Index:     argOptionalInt(args, "index"),
	})
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(cell), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolRunCodeCell(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	_, execRes, err := s.runtime.MutateCell(ctx, sid, models.CellMutationRequest{
		Operation:  "run",
		CellID:     argStr(args, "cell_id"),
		TimeoutSec: argFloat(args, "timeout_sec", 120),
	})
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	if cctx.apiKey != nil && execRes != nil {
		execRes = redactExecResultForScopedKey(execRes, s.pooledEmailsForRedaction(ctx, ""))
	}
	return jsonToolResult(execRes), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolUpdateCell(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	cell, _, err := s.runtime.MutateCell(ctx, sid, models.CellMutationRequest{
		Operation: "update",
		CellID:    argStr(args, "cell_id"),
		Content:   argStr(args, "content"),
	})
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(map[string]any{"cell_id": cell.CellID, "updated": true}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolDeleteCell(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	cell, _, err := s.runtime.MutateCell(ctx, sid, models.CellMutationRequest{
		Operation: "delete",
		CellID:    argStr(args, "cell_id"),
	})
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	rem, _ := s.runtime.GetCells(ctx, sid, false)
	return jsonToolResult(map[string]any{
		"cell_id":         cell.CellID,
		"deleted":         true,
		"remaining_cells": len(rem),
	}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolMoveCell(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	newIdx := int(argFloat(args, "new_index", 0))
	cell, _, err := s.runtime.MutateCell(ctx, sid, models.CellMutationRequest{
		Operation: "move",
		CellID:    argStr(args, "cell_id"),
		Index:     &newIdx,
	})
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(map[string]any{
		"cell_id":   cell.CellID,
		"new_index": cell.Index,
	}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolKernelRestart(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	if err := s.runtime.RestartKernel(ctx, sid); err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(map[string]any{
		"session":   sid,
		"restarted": true,
		"note":      "/content and /root/.cache preserved",
	}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolKernelInterrupt(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	if err := s.runtime.InterruptKernel(ctx, sid); err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(map[string]any{"session": sid, "interrupted": true}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolFileUpload(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	localPath := strings.TrimSpace(argStr(args, "local_path"))
	if localPath != "" && !s.canAccessHostFS(cctx) {
		return nil, traceSummary{}, errors.New("host filesystem 'local_path' is not permitted over remote or API-key MCP; pass inline 'content' or 'content_base64' instead")
	}
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	remotePath := argStr(args, "remote_path")
	var data []byte
	if b64, hasB64 := args["content_base64"]; hasB64 && b64 != nil {
		if b64Str, ok := b64.(string); ok && b64Str != "" {
			data, err = base64.StdEncoding.DecodeString(b64Str)
			if err != nil {
				return nil, traceSummary{sessionID: sid}, fmt.Errorf("invalid content_base64: %w", err)
			}
		}
	}
	if data == nil {
		if rawInline, hasContent := args["content"]; hasContent && rawInline != nil {
			if inlineStr, ok := rawInline.(string); ok {
				data = []byte(inlineStr)
			}
		}
	}
	if data == nil {
		if localPath != "" {
			data, err = os.ReadFile(localPath)
			if err != nil {
				return nil, traceSummary{sessionID: sid}, err
			}
		} else {
			return nil, traceSummary{sessionID: sid}, errors.New("supply content or content_base64 (or local_path over local stdio)")
		}
	}

	if err := s.runtime.UploadFile(ctx, sid, remotePath, data); err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(map[string]any{
		"uploaded":    true,
		"remote_path": remotePath,
		"bytes":       len(data),
	}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolFileDownload(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	localPath := strings.TrimSpace(argStr(args, "local_path"))
	if localPath != "" && !s.canAccessHostFS(cctx) {
		return nil, traceSummary{}, errors.New("host filesystem 'local_path' is not permitted over remote or API-key MCP; omit 'local_path' to receive file bytes inline")
	}
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	remotePath := argStr(args, "remote_path")
	data, err := s.runtime.DownloadFile(ctx, sid, remotePath)
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	if localPath != "" {
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return nil, traceSummary{sessionID: sid}, err
		}
		if err := os.WriteFile(localPath, data, 0o644); err != nil {
			return nil, traceSummary{sessionID: sid}, err
		}
	}
	resp := map[string]any{
		"downloaded":  true,
		"remote_path": remotePath,
		"local_path":  localPath,
		"bytes":       len(data),
	}
	if localPath == "" {
		if len(data) <= 32768 {
			resp["content_preview"] = string(data)
		}
		if len(data) <= (8 << 20) {
			resp["content_base64"] = base64.StdEncoding.EncodeToString(data)
		}
	}
	return jsonToolResult(resp), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolFileList(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	remPath := firstStr(argStr(args, "remote_path"), "/content")
	entries, err := s.runtime.ListFiles(ctx, sid, remPath)
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(map[string]any{
		"path":    remPath,
		"entries": entries,
	}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolFileRemove(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	remPath := argStr(args, "remote_path")
	if err := s.runtime.DeleteFile(ctx, sid, remPath); err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(map[string]any{"deleted": true, "remote_path": remPath}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolInstallPackages(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	reqPath := strings.TrimSpace(argStr(args, "requirements_path"))
	if reqPath != "" && !s.canAccessHostFS(cctx) {
		return nil, traceSummary{}, errors.New("host filesystem 'requirements_path' is not permitted over remote or API-key MCP; pass 'requirements_txt' or 'packages' instead")
	}
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	pkgs := argStringSlice(args, "packages")
	reqTxt := argStr(args, "requirements_txt")
	if reqTxt == "" && reqPath != "" {
		raw, rErr := os.ReadFile(reqPath)
		if rErr != nil {
			return nil, traceSummary{sessionID: sid}, rErr
		}
		reqTxt = string(raw)
	}
	res, err := s.runtime.InstallPackages(ctx, sid, pkgs, reqTxt)
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	if cctx.apiKey != nil && res != nil {
		knownEmail := ""
		if sess, sErr := s.runtime.GetSession(ctx, sid); sErr == nil && sess != nil {
			knownEmail = sess.AccountEmail
		}
		res = redactExecResultForScopedKey(res, s.pooledEmailsForRedaction(ctx, knownEmail))
	}
	return jsonToolResult(map[string]any{
		"installed": res.Status == "ok",
		"installer": "uv",
		"stdout":    res.Stdout,
		"stderr":    res.Stderr,
	}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolSessionURL(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	sid, err := s.resolveTargetSessionID(ctx, cctx, args)
	if err != nil {
		return nil, traceSummary{}, err
	}
	u, err := s.runtime.GetColabWebURL(ctx, sid)
	if err != nil {
		return nil, traceSummary{sessionID: sid}, err
	}
	return jsonToolResult(map[string]any{"session": sid, "url": u}), traceSummary{sessionID: sid}, nil
}

func (s *Server) toolExperimentLaunchSharded(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	if s.runtime == nil {
		return nil, traceSummary{}, errors.New("runtime engine unavailable")
	}
	expID := strings.TrimSpace(argStr(args, "experiment_id"))
	if expID != "" && !isValidASCIIExperimentIDMCP(expID) {
		return nil, traceSummary{}, errors.New("invalid experiment_id: must be 1..128 ASCII [A-Za-z0-9._-] characters without '..' or path separators")
	}
	if expID != "" {
		if existing, gErr := s.runtime.GetExperiment(ctx, expID); gErr == nil && existing != nil {
			return nil, traceSummary{}, fmt.Errorf("experiment %q already exists", expID)
		}
		if allExps, lErr := s.runtime.ListExperiments(ctx); lErr == nil {
			for _, ex := range allExps {
				if ex != nil && strings.EqualFold(ex.ID, expID) {
					return nil, traceSummary{}, fmt.Errorf("experiment %q already exists", expID)
				}
			}
		}
	}

	hostAllowed := s.canAccessHostFS(cctx)
	scriptPath := strings.TrimSpace(argStr(args, "script_path"))
	scriptCode := argStr(args, "script_code")
	localOutDir := strings.TrimSpace(argStr(args, "local_output_dir"))

	if !hostAllowed {
		if scriptPath != "" {
			return nil, traceSummary{}, errors.New("host filesystem 'script_path' is not permitted over remote or API-key MCP; pass inline 'script_code' instead")
		}
		if localOutDir != "" {
			return nil, traceSummary{}, errors.New("host filesystem 'local_output_dir' is not permitted over remote or API-key MCP")
		}
	}

	accel, err := models.NormalizeAccelerator(firstStr(argStr(args, "gpu"), argStr(args, "accelerator"), "G4"))
	if err != nil {
		return nil, traceSummary{}, err
	}

	var stageDir string
	defer func() {
		if stageDir != "" {
			_ = os.RemoveAll(stageDir)
		}
	}()

	var uploads []models.ExperimentFileSpec
	if rawUp, ok := args["upload_files"].([]any); ok {
		for idx, item := range rawUp {
			m, okMap := item.(map[string]any)
			if !okMap {
				continue
			}
			lp := strings.TrimSpace(argStr(m, "local_path"))
			remPath := argStr(m, "remote_path")
			b64 := firstStr(argStr(m, "content_base64"), argStr(m, "content_b64"))
			inlineTxt, hasInlineTxt := m["content"].(string)

			if !hostAllowed && lp != "" {
				return nil, traceSummary{}, errors.New("host filesystem 'upload_files[].local_path' is not permitted over remote or API-key MCP; pass inline 'content' or 'content_base64'")
			}

			var inlineBytes []byte
			hasInline := false
			if b64 != "" {
				decoded, dErr := base64.StdEncoding.DecodeString(b64)
				if dErr != nil {
					return nil, traceSummary{}, fmt.Errorf("invalid upload_files[%d].content_base64: %w", idx, dErr)
				}
				inlineBytes = decoded
				hasInline = true
			} else if hasInlineTxt {
				inlineBytes = []byte(inlineTxt)
				b64 = base64.StdEncoding.EncodeToString(inlineBytes)
				hasInline = true
			}

			if hasInline {
				if stageDir == "" {
					stageDir, err = os.MkdirTemp("", "colabhive-mcp-exp-*")
					if err != nil {
						return nil, traceSummary{}, err
					}
				}
				baseName := filepath.Base(firstStr(remPath, "file.bin"))
				if !isSafeMCPPathSegment(baseName) {
					baseName = "file.bin"
				}
				staged := filepath.Join(stageDir, fmt.Sprintf("%d_%s", idx, baseName))
				if wErr := os.WriteFile(staged, inlineBytes, 0o600); wErr != nil {
					return nil, traceSummary{}, wErr
				}
				lp = staged
			}

			uploads = append(uploads, models.ExperimentFileSpec{
				LocalPath:     lp,
				RemotePath:    remPath,
				ContentBase64: b64,
			})
		}
	}

	exp, err := s.runtime.LaunchShardedExperiment(ctx, models.ExperimentLaunchRequest{
		ExperimentID:         expID,
		Title:                argStr(args, "title"),
		ScriptPath:           scriptPath,
		ScriptCode:           scriptCode,
		ScriptArgs:           argStringSlice(args, "script_args"),
		UploadFiles:          uploads,
		Packages:             argStringSlice(args, "packages"),
		Accelerator:          accel,
		HighMem:              argBool(args, "high_mem", true),
		NumShards:            int(argFloat(args, "num_shards", 4)),
		Strategy:             models.RoutingStrategy(firstStr(argStr(args, "strategy"), string(cctx.defaultStrategy), "waste-zero")),
		RemoteCheckpointGlob: argStr(args, "remote_checkpoint_glob"),
		LocalOutputDir:       localOutDir,
		AutoStopOnComplete:   argBool(args, "auto_stop_on_complete", true),
		Env:                  argStringMap(args, "env"),
		APIKeyID:             cctx.apiKeyID(),
	})
	if err != nil {
		return nil, traceSummary{reqGPU: accel}, err
	}
	outExp := exp
	if cctx.apiKey != nil {
		outExp = redactExperimentForScopedKey(exp)
	}
	return jsonToolResult(outExp), traceSummary{reqGPU: accel, assignedGPU: exp.Accelerator}, nil
}

func (s *Server) toolExperimentStatus(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	exp, err := s.authorizeExperiment(ctx, cctx, argStr(args, "experiment_id"))
	if err != nil {
		return nil, traceSummary{}, err
	}
	outExp := exp
	if cctx.apiKey != nil {
		outExp = redactExperimentForScopedKey(exp)
	}
	return jsonToolResult(outExp), traceSummary{assignedGPU: exp.Accelerator}, nil
}

func (s *Server) toolExperimentCollectAndTeardown(ctx context.Context, cctx callContext, args map[string]any) (*CallToolResult, traceSummary, error) {
	exp, err := s.authorizeExperiment(ctx, cctx, argStr(args, "experiment_id"))
	if err != nil {
		return nil, traceSummary{}, err
	}
	res, err := s.runtime.CollectAndTeardownExperiment(
		ctx,
		exp.ID,
		argBool(args, "force_kill_running", false),
	)
	if err != nil {
		return nil, traceSummary{}, err
	}
	outRes := res
	if cctx.apiKey != nil {
		outRes = redactTeardownResultForScopedKey(res)
	}
	return jsonToolResult(outRes), traceSummary{assignedGPU: exp.Accelerator}, nil
}

// ============================================================================
// Argument Coercion Helpers
// ============================================================================

func argStr(args map[string]any, key string) string {
	if v, ok := args[key]; ok {
		if s, okStr := v.(string); okStr {
			return s
		}
	}
	return ""
}

func argBool(args map[string]any, key string, def bool) bool {
	if v, ok := args[key]; ok {
		if b, okBool := v.(bool); okBool {
			return b
		}
	}
	return def
}

func argFloat(args map[string]any, key string, def float64) float64 {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		case json.Number:
			f, err := n.Float64()
			if err == nil {
				return f
			}
		}
	}
	return def
}

func argOptionalInt(args map[string]any, key string) *int {
	if v, ok := args[key]; ok && v != nil {
		val := int(argFloat(args, key, 0))
		return &val
	}
	return nil
}

func argStringSlice(args map[string]any, key string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	if s, okSlice := raw.([]string); okSlice {
		return s
	}
	if arr, okArr := raw.([]any); okArr {
		out := make([]string, 0, len(arr))
		for _, item := range arr {
			if str, okStr := item.(string); okStr {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func argStringMap(args map[string]any, key string) map[string]string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	if m, okMap := raw.(map[string]any); okMap {
		out := make(map[string]string, len(m))
		for k, v := range m {
			if s, okStr := v.(string); okStr {
				out[k] = s
			} else {
				out[k] = fmt.Sprintf("%v", v)
			}
		}
		return out
	}
	return nil
}
