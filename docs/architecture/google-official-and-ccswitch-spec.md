# Google Official Colab Ecosystem, `cc-switch` / `magpie` UX Benchmark & Contributor Auth Specification

> **Status**: Authoritative Architecture Specification (`official-docs-recon`)  
> **Target Repository**: `d:\git_proj\colabhive`  
> **Primary Sources Verified**: `github.com/googlecolab/*` (`colab-mcp`, `colab-vscode`, `google-colab-cli`, `colabtools`, `jupyter-kernel-client`), `colaboratory.googleapis.com` OpenAPI `v1beta` (`rev 20260913`), `developers.google.com/colab`, `farion1231/cc-switch`, and `yetone/magpie`.

---

## Executive Summary

Google has officially transitioned Colaboratory from a browser-only notebook UI into a programmatic developer platform spanning **four open-source repositories** under [`github.com/googlecolab`](https://github.com/googlecolab) and **three backend API surfaces** (`colab.research.google.com`, `colab.pa.googleapis.com`, and the new `colaboratory.googleapis.com`).

However, every official Google tool (`colab-vscode`, `colab-mcp`, `google-colab-cli`) enforces a **strict single-account, single-user model** (`supportsMultipleAccounts: false`), and `googlecolab/colab-mcp` suffers from a single-browser-tab WebSocket lock (`1013 Server is busy`) plus dynamic `notifications/tools/list_changed` tool-hiding that breaks several MCP clients.

**ColabHive** bridges this gap by combining:
1. **100% Official Protocol Compliance**: Dual-backend support for Google's **Colab Public API (`colaboratory.googleapis.com/v1beta`)** and **Session Tunnel API (`colab.research.google.com/tun/m/...`)**, plus **GAPI (`colab.pa.googleapis.com/v1/...`)** quota telemetry and **Colab Server Extension (`/api/colab/resources`)** GPU/RAM monitoring.
2. **Viral Open-Source Polish (`cc-switch` + `magpie` DNA)**: Surgical, non-destructive 1-click MCP/CLI config injection across Claude Code, Codex, Gemini CLI, Cursor, Windsurf, OpenCode, and Antigravity; `colabhive://` deep links; live quota-window account cards; and smart `waste-zero` / `pace` routing.
3. **Zero-Friction External Contributor Authorization**: Shareable Guest Portal links (`/contribute/:invite_code`) built on Google's official remote OAuth flow (`sdk.cloud.google.com/applicationdefaultauthcode.html` + least-privilege `colaboratory` scope without Google Drive access), backed by hard scheduler guardrails (CU reserve floors, GPU tier caps, time-window schedules, per-account proxy isolation, kernel auth-propagation firewall, and 1-click self-service revocation).

---

## 1. Primary Source of Truth: Official Google Colab Repositories & APIs

### 1.1 Verified Official Repository & Documentation Matrix

| Official Resource | Verified URL | Version / Rev | Role in Google's Official Stack |
| :--- | :--- | :--- | :--- |
| **`googlecolab/colab-mcp`** | https://github.com/googlecolab/colab-mcp | `v1.0.1` (Mar 2026) | Official local `stdio` FastMCP proxy bridging local AI agents to a live Colab browser tab over WebSocket (`subprotocol: "mcp"`). |
| **Hosted Colab Cloud MCP** | `https://colaboratory.googleapis.com/mcp` | `v1beta` (2026) | Official cloud-hosted Streamable HTTP MCP server exposing headless runtime & `execute_code` tools over OAuth2 (`colaboratory` scope). |
| **`googlecolab/colab-vscode`** | https://github.com/googlecolab/colab-vscode | `main` (Nov 2025–2026) | Official VS Code extension containing canonical TypeScript clients for OAuth2 PKCE, `v1/user-info` CCU polling, `v1beta` LRO runtime management, token refresh, and Jupyter WebSocket proxying. |
| **`googlecolab/google-colab-cli`** | https://github.com/googlecolab/google-colab-cli | `main` (2026) | Official Python CLI (`colab`) implementing dual-backend (`--public-api` / `/tun/m`) session management, remote OAuth copy-paste flow, and ZMQ streaming execution. |
| **`googlecolab/colabtools`** | https://github.com/googlecolab/colabtools | `main` | Official `google.colab` Python package preinstalled on Colab VMs, including `google.colab.runtime.unassign()` and Tornado `/api/colab/resources` GPU/RAM monitor. |
| **`googlecolab/jupyter-kernel-client`** | https://github.com/googlecolab/jupyter-kernel-client | `v0.9.0` | Official Python ZMQ-over-WebSocket client used by both `colab-mcp` and `google-colab-cli`. |
| **Colab Developer Docs** | https://developers.google.com/colab | Current | Official developer hub for Colab API & MCP integration. |
| **Colab Blog: MCP Launch** | https://developers.googleblog.com/en/announcing-the-colab-mcp-server-connect-any-ai-agent-to-google-colab/ | Mar 17, 2026 | Official announcement of `googlecolab/colab-mcp`. |
| **Colab Blog: VS Code Launch** | https://developers.googleblog.com/en/google-colab-is-coming-to-vs-code | Nov 13, 2025 | Official announcement of `googlecolab/colab-vscode`. |
| **Colab Blog: CLI Launch** | https://developers.googleblog.com/en/introducing-the-google-colab-cli/ | 2026 | Official announcement of `googlecolab/google-colab-cli`. |
| **Colab FAQ & Runtime Versions** | https://research.google.com/colaboratory/faq.html<br>https://research.google.com/colaboratory/runtime-version-faq.html | Current | Official CU expiration (90 days), max lifetime (12h free / 24h paid background), dynamic idle reaper policy, and runtime image tags. |

---

### 1.2 Official Google Colab MCP Architecture (Two Official Tracks)

Google ships **two distinct MCP architectures** for Colab. ColabHive must understand both so its MCP router is a drop-in superset without inheriting `colab-mcp`'s browser bottlenecks.

#### Track A: Open-Source Browser-Bridge MCP (`googlecolab/colab-mcp` `v1.0.1`)

Verified directly from `src/colab_mcp/__init__.py`, `src/colab_mcp/session.py`, and `src/colab_mcp/websocket_server.py`:

```mermaid
sequenceDiagram
    participant Agent as Local MCP Client (Claude/Gemini/Antigravity)
    participant Proxy as colab-mcp (FastMCP stdio)
    participant WSS as Local ColabWebSocketServer (ws://localhost:PORT)
    participant Browser as Browser Tab (colab.research.google.com)

    Agent->>Proxy: tools/list
    Proxy-->>Agent: [open_colab_browser_connection] (only 1 tool initially!)
    Agent->>Proxy: tools/call open_colab_browser_connection()
    Proxy->>Browser: webbrowser.open_new("https://colab.research.google.com/notebooks/empty.ipynb#mcpProxyToken=TOKEN&mcpProxyPort=PORT")
    Note over Proxy,WSS: Waits up to UI_CONNECTION_TIMEOUT = 60.0s (progress 1/3 -> 2/3 -> 3/3)
    Browser->>WSS: WebSocket Connect (Origin: https://colab.research.google.com, Subprotocol: "mcp", ?access_token=TOKEN or Bearer TOKEN)
    WSS-->>Proxy: connection_live.set() (acquires single-client asyncio.Lock)
    Proxy-->>Agent: notifications/tools/list_changed
    Agent->>Proxy: tools/list
    Proxy->>WSS: Forward JSON-RPC tools/list to Colab Frontend
    WSS-->>Agent: Unlocked Notebook Tools (add_code_cell, run_code_cell, etc.)
```

* **Exact Transport & Launch Spec**:
  * **Client Side**: `stdio` (`uvx git+https://github.com/googlecolab/colab-mcp`, entrypoint `colab_mcp:main`, Python `>=3.13`, `fastmcp==2.14.5`, `mcp[cli]>=1.20.0`).
  * **Browser Bridge Side**: `websockets.serve` on `ws://localhost:0` (OS-assigned ephemeral port), `subprotocols=[Subprotocol("mcp")]`, `allowed_origins=["https://colab.research.google.com", "https://colab.google.com"]`, authenticated via `secrets.token_urlsafe(16)` passed in URL fragment `#mcpProxyToken=<token>&mcpProxyPort=<port>` and verified in HTTP upgrade via `?access_token=<token>` or `Authorization: Bearer <token>`.
* **Exact Tools Exposed**:
  1. `open_colab_browser_connection() -> bool` (statically injected via `ToolInjectionMiddleware`; returns `structured_content: {"result": true|false}`).
  2. Dynamically proxied from the Colab browser frontend once `fe_connected == True`: `add_code_cell`, `add_text_cell`, `run_code_cell`, `get_cells`, `update_cell`, `delete_cell`, `move_cell`.
* **Three Architectural Bottlenecks in `googlecolab/colab-mcp` (and How ColabHive Solves Them)**:
  1. **"Invisible Tools" Client Bug**: `ColabProxyMiddleware` hides all notebook tools until `fe_connected` flips and emits `notifications/tools/list_changed`. Clients that cache `tools/list` at startup (e.g., Claude Desktop, Codex Desktop, older IDE bridges) never see `run_code_cell`. **ColabHive Rule**: Register **all** MCP tools statically at startup; never gate tool visibility on `notifications/tools/list_changed`.
  2. **Single-Client Exclusivity (`1013 Server is busy`)**: `ColabWebSocketServer._connection_handler` holds a global `asyncio.Lock()` and rejects any second connection with WebSocket close code `1013`. Parallel subagents or sharded experiments are impossible. **ColabHive Rule**: Headless backend multiplexing across $N$ accounts $\times$ $M$ runtimes $\times$ $K$ kernels.
  3. **Mandatory Live Browser Tab & `empty.ipynb` Scratchpad**: Requires a GUI desktop browser and forces `/notebooks/empty.ipynb`. **ColabHive Rule**: 100% headless runtime provisioning and ZMQ/REST execution, with optional 1-click "Open in Colab Web" URL generation when human inspection is desired.

#### Track B: Official Hosted Cloud MCP (`https://colaboratory.googleapis.com/mcp`) & `ExecuteCode` Contract

Verified from `developers.google.com/colab` and `colab-vscode/src/colab/client/v2/colab-api.json`:

* **Endpoint**: `https://colaboratory.googleapis.com/mcp` (MCP **Streamable HTTP** transport, OAuth2 Bearer authentication with scope `https://www.googleapis.com/auth/colaboratory` on an allowlisted GCP project).
* **Official Server-Side Toolset**:
  * **Runtime & Quota Tools**: `list_runtime_specs`, `get_subscription`, `create_runtime`, `get_runtime`, `list_runtimes`, `delete_runtime`.
  * **Session & Execution Tools**: `create_session`, `list_sessions`, `delete_session`, `execute_code`.
* **Official `ExecuteCodeResult` & `RichOutput` Schema (`colab-api.json` rev `20260913`)**:
  Google's `ExecuteCodeResult` establishes a critical best practice for AI agent context windows — **never inline megabytes of base64 plot/widget bytes directly into `stdout`/text tool responses**:
  ```json
  {
    "session": "runtimes/r-abc123/sessions/sess-01",
    "executionId": "exec-uuid-4",
    "executionCount": 3,
    "stdout": "Epoch 1/10 - loss: 0.241\n",
    "stderr": "",
    "outputTruncated": false,
    "result": "0.9812",
    "executionError": null,
    "richOutputs": [
      {
        "index": 0,
        "mimeTypes": ["image/png", "text/html"],
        "filePath": "/content/.dsm_outputs/msg-9f81/out-000.json",
        "sizeBytes": "48210"
      }
    ]
  }
  ```
  > [!TIP]
  > **ColabHive Adoption**: ColabHive's `colab_execute_code` tool MUST follow Google's `ExecuteCodeResult` structure — returning clean `stdout`, `stderr`, `result`, `executionError` (`{name, value, traceback[]}`), `outputTruncated`, and structured `richOutputs[]` (saving PNG/HTML artifacts both to `/content/.colabhive_outputs/` and streaming them to the ColabHive Live Experiment Visualizer UI, only returning inline `ImageContent` when `inline_images=true` is requested).

---

### 1.3 Official Google Colab Backend Domains & Dual-API Architecture

Verified from `colab-vscode/scripts/generate-config.mts`, `colab-vscode/src/colab/client/v1/index.ts`, `colab-vscode/src/colab/client/v2/colab-api.json`, and `google-colab-cli/docs/public_api_migration_plan.md`:

#### Production Domain Trio

| Config Key (`colab-vscode`) | Production Base URL | Auth Requirement | Primary Endpoints |
| :--- | :--- | :--- | :--- |
| **`ColabApiDomain`** | `https://colab.research.google.com` | OAuth Bearer (`userinfo.email` + `colaboratory`); **requires `?authuser=0`**; responses start with XSSI prefix `)]}'\n` | `GET /tun/m/assignments`<br>`GET` + `POST /tun/m/assign` (`X-Goog-Colab-Token`)<br>`GET` + `POST /tun/m/unassign/{endpoint}`<br>`GET /tun/m/ccu-info`<br>`GET /vscode/experiment-state` |
| **`ColabGapiDomain`** | `https://colab.pa.googleapis.com` | OAuth Bearer (`colaboratory` scope); pure ProtoJSON (no XSSI prefix) | `GET /v1/user-info?get_ccu_consumption_info=true`<br>`POST /v1/credential-propagation:enable` |
| **`ColabPublicApiDomain`** | `https://colaboratory.googleapis.com` | OAuth Bearer (`colaboratory` scope) **AND allowlisted OAuth `client_id` / quota project** | `GET /v1beta/runtimes`<br>`POST /v1beta/runtimes?requestId={uuid}&runtimeId={id}` (LRO)<br>`GET /v1beta/runtimes/{runtime}`<br>`DELETE /v1beta/runtimes/{runtime}?allowMissing=true`<br>`GET /v1beta/runtimespecs`<br>`GET /v1beta/subscription`<br>`GET /v1/operations/{opId}:wait?timeout=200s` |

> [!IMPORTANT]
> **Why ColabHive MUST Implement Dual-Backend Dispatch (`Public API` + `Tunnel API` Fallback)**:
> As documented in `googlecolab/google-colab-cli/docs/public_api_migration_plan.md`, `colaboratory.googleapis.com` rejects (`HTTP 403`) tokens minted by non-allowlisted custom GCP OAuth projects or ADC credentials without an allowlisted quota project, whereas `colab.research.google.com/tun/m/...` accepts any Google OAuth token with `userinfo.email`.
>
> ColabHive must probe both backends once on account onboarding (`check_api_access()`), cache `public_api_supported: bool` and `tunnel_api_supported: bool` per account, and persist **both** `runtime_name` (`runtimes/{runtime_id}`) and `endpoint` (`m-s-...`) on every session record!

#### Required Client Headers (`colab-vscode/src/colab/headers.ts` & `google-colab-cli/src/colab_cli/client.py`)

* **Control-Plane Requests**:
  * `Authorization: Bearer <access_token>`
  * `Accept: application/json`
  * `X-Colab-Client-Agent: vscode` (or `colab-cli`)
  * On `/tun/m/assign` and `/tun/m/unassign/{endpoint}` `POST` steps: `X-Goog-Colab-Token: <xsrf_token_from_GET>`
* **Runtime Data-Plane Requests (Jupyter REST + WebSocket to `connectionInfo.url`)**:
  * `X-Colab-Runtime-Proxy-Token: <connectionInfo.token>`
  * `X-Colab-Client-Agent: vscode`

---

### 1.4 Official OAuth2 Flows, Client IDs, Scopes & Token Lifecycle

Verified from `colab-vscode/src/auth/*` and `google-colab-cli/src/colab_cli/auth.py` + `oauth_config.json`:

1. **Official OAuth Scopes (`colab-vscode/src/auth/scopes.ts`)**:
   * **Minimum Required Scopes for Colab Compute (`REQUIRED_SCOPES`)**:
     * `openid` (or implicit with `profile` / `email`)
     * `profile` (`https://www.googleapis.com/auth/userinfo.profile`)
     * `email` (`https://www.googleapis.com/auth/userinfo.email`)
     * `https://www.googleapis.com/auth/colaboratory` *(Read, write, and delete your Colab resources)*
   * **Optional Read-Only Scope (`colab-api.json`)**:
     * `https://www.googleapis.com/auth/colaboratory.readonly`
   * **Strictly Optional Drive Scope (`DRIVE_SCOPES` — NEVER required for compute!)**:
     * `https://www.googleapis.com/auth/drive.file`
2. **Official Shipped First-Party CLI OAuth Client (`google-colab-cli/src/colab_cli/oauth_config.json`)**:
   ```json
   {
     "installed": {
       "client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
       "project_id": "colab-cli",
       "auth_uri": "https://accounts.google.com/o/oauth2/auth",
       "token_uri": "https://oauth2.googleapis.com/token",
       "client_secret": "d-FL95Q19q7MQmFpd7hHD0Ty",
       "redirect_uris": ["http://localhost"]
     }
   }
   ```
3. **Three Official Authentication Flows Across Google's Repos**:
   * **Flow 1 — Remote HTTPS Auth-Code Copy-Paste (`google-colab-cli/src/colab_cli/auth.py`)**:
     * Uses `REMOTE_REDIRECT_URI = "https://sdk.cloud.google.com/applicationdefaultauthcode.html"` (registered specifically to client `764086051850-...`) with query parameters `prompt=consent&token_usage=remote&access_type=offline&response_type=code`.
     * Works on **any** headless server, Docker container, WSL2 instance, or remote contributor browser without needing localhost port forwarding or a public domain!
   * **Flow 2 — Local Loopback Server + PKCE S256 (`colab-vscode/src/auth/flows/loopback.ts`)**:
     * Starts `127.0.0.1:<ephemeral_port>`, generates PKCE `S256` (`code_challenge` + `code_verifier`), `access_type: "offline"`, `prompt: "consent"`, `state: "nonce=<uuid>"`.
   * **Flow 3 — Proxied Redirect (`colab-vscode/src/auth/flows/proxied.ts`)**:
     * Uses `redirect_uri = "https://colab.research.google.com/vscode/redirect"` for remote extension hosts where loopback cannot bind.
4. **Official Token Refresh & Revocation Rules (`colab-vscode/src/auth/auth-provider.ts`)**:
   * Refresh access tokens proactively at **`REFRESH_MARGIN_MS = 5 * 60 * 1000` (5 minutes)** prior to `expiry_date`.
   * Fetch account identity from `GET https://www.googleapis.com/oauth2/v2/userinfo` (`{ name, email, picture }`).
   * Detect revoked credentials immediately when `https://oauth2.googleapis.com/token` returns **`HTTP 400` with `invalid_grant`** -> transition account state to `REVOKED` and stop polling.

---

### 1.5 Official Quota (`ConsumptionUserInfo`), Provisioning, Keepalive & In-VM Telemetry Schemas

#### A. Rich CCU & Free-Quota Endpoint (`GET https://colab.pa.googleapis.com/v1/user-info?get_ccu_consumption_info=true`)

Verified from `colab-vscode/src/colab/client/v1/api.ts` (`ConsumptionUserInfoSchema`):

```json
{
  "subscriptionTier": "SUBSCRIPTION_TIER_PRO_PLUS",
  "paidComputeUnitsBalance": 482.35,
  "consumptionRateHourly": 8.88,
  "assignmentsCount": 1,
  "eligibleAccelerators": [
    { "variant": "VARIANT_GPU", "models": ["T4", "L4", "G4", "A100", "H100"] },
    { "variant": "VARIANT_TPU", "models": ["V5E1", "V6E1"] }
  ],
  "ineligibleAccelerators": [],
  "freeCcuQuotaInfo": {
    "remainingTokens": "14500",
    "nextRefillTimestampSec": 1763798400
  }
}
```

* **Key Discovery from `colab-vscode`**:
  * Unlike `/tun/m/ccu-info` (which only returns `{ currentBalance, consumptionRateHourly, assignmentsCount }`), `colab.pa.googleapis.com/v1/user-info?get_ccu_consumption_info=true` returns:
    1. Exact **`subscriptionTier`** (`SUBSCRIPTION_TIER_NONE`, `SUBSCRIPTION_TIER_PRO`, `SUBSCRIPTION_TIER_PRO_PLUS`).
    2. Exact per-account **`eligibleAccelerators`** and **`ineligibleAccelerators`** (so ColabHive's router knows *before* calling `assign` which accounts are eligible for `H100`/`A100`/`G4`!).
    3. **`freeCcuQuotaInfo.remainingTokens`** (ProtoJSON `Int64` string in **milli-CCUs** — divide by `1000` to get remaining Free CUs!) and **`nextRefillTimestampSec`** (exact Unix epoch when the account's free quota refills!).
* **Official Polling Cadence (`colab-vscode/src/colab/consumption/poller.ts`)**:
  * Poll every `60s` (`POLL_INTERVAL_MS = 60_000`, `TASK_TIMEOUT_MS = 10_000`), plus immediate `runNow()` whenever an assignment starts or stops.
  * On network failure, apply jittered exponential interval skipping (`min(2^(failures-1), 15) * (0.5 + rand())`, ceiling `15` intervals = 1 request per 15 min).

#### B. Runtime Provisioning, Canonical Error Codes & Auto-Tier Fallback

Verified from `colab-vscode/src/colab/client/v2/index.ts`, `colab-vscode/src/jupyter/assignments.ts`, and `google-colab-cli/src/colab_cli/client.py`:

* **Supported Hardware Matrix (`Accelerator` enum in `google-colab-cli`)**:
  * `CPU`: `variant="VARIANT_CPU"`, `accelerator="NONE"`, shapes `SHAPE_STANDARD` / `SHAPE_HIGHMEM`.
  * `GPU`: `variant="VARIANT_GPU"`, `accelerator` in `["T4", "L4", "G4", "A100", "H100"]`.
  * `TPU`: `variant="VARIANT_TPU"`, `accelerator` in `["V5E1", "V6E1"]`.
  * **High-Mem Shape Quirks (`HIGH_MEM_ONLY_ACCELERATORS`)**: `L4`, `V5E1`, and `V6E1` only exist in High-RAM shapes; passing `shape=hm` on `/tun/m/assign` must be omitted for those three models (`resolve_assign_shape` in `client.py`).
* **Public API Long-Running Operation (`CreateRuntime`)**:
  1. `POST https://colaboratory.googleapis.com/v1beta/runtimes?requestId={uuid4}` with body `{"runtimeSpec": {"variant": "VARIANT_GPU", "accelerator": "A100", "shape": "SHAPE_HIGHMEM"}}`.
  2. If `operation.done == false`, call `GET https://colaboratory.googleapis.com/v1/operations/{operationId}:wait?timeout=200s`.
  3. Map `operation.error` (`google.rpc.Status` + `google.rpc.ErrorInfo.reason`):
     * `reason == "TOO_MANY_ACTIVE_RUNTIMES"` (or HTTP `412` on `/tun/m/assign`) -> Account hit max concurrent VMs; route to next account in pool!
     * `reason == "QUOTA_EXCEEDED_USAGE_TIME"` -> Account quota exhausted; place account in cooldown until `nextRefillTimestampSec` or CU top-up.
     * `reason == "DENYLISTED"` -> Account flagged by Colab abuse filter; quarantine immediately (`QUARANTINED_DENYLIST`) and alert admin/contributor.
     * `code == 9 (FAILED_PRECONDITION)` with accelerator -> Hardware stockout (`AcceleratorUnavailableError`); trigger `assignWithFallback` across next account or fallback GPU tier (`H100 -> A100 -> G4 -> L4 -> T4`).
* **Runtime Proxy Token Refresh (`ConnectionRefresher`)**:
  * `connectionInfo.token` is short-lived (~1 hour, `expireTime` / `tokenExpiresInSeconds`).
  * Refresh **5 minutes before expiry** (`REFRESH_BUFFER_MS = 300_000`) via `GET /v1beta/runtimes/{id}` or `GET /tun/m/assignments`, retrying after `30s` on transient errors, and hot-swapping `X-Colab-Runtime-Proxy-Token` on subsequent REST/WebSocket calls.

#### C. Out-of-Band GPU / RAM / Disk Telemetry (`GET {proxy_url}/api/colab/resources`)

Verified from `googlecolab/colabtools/google/colab/_serverextension/_handlers.py` and `_resource_monitor.py`:

* Every Colab VM runs a built-in Tornado `APIHandler` at **`GET {connectionInfo.url}/api/colab/resources`** (authenticated via `X-Colab-Runtime-Proxy-Token`, returning XSSI prefix `)]}'\n` + JSON).
* Because this handler queries `nvidia-smi --query-gpu=index,name,memory.used,memory.total,utilization.gpu,utilization.memory --format=csv,nounits,noheader` and `psutil` inside the Jupyter Tornado server process (NOT inside the IPython kernel), **ColabHive can poll live GPU VRAM, GPU SM utilization %, RAM, and Disk every 10 seconds (`ResourcePollIntervalMs = 10000`) even while the Python kernel is 100% blocked inside a PyTorch training loop!**
  ```json
  {
    "memory": {
      "totalBytes": 89612345344,
      "freeBytes": 61203947520,
      "kernels": [{ "uuid": "8f2c...", "usedBytes": 14201929728 }]
    },
    "disks": [
      { "filesystem": { "label": "kernel", "totalBytes": 253055090688, "usedBytes": 42190848000 } }
    ],
    "gpus": [
      {
        "name": "NVIDIA A100-SXM4-40GB",
        "memoryUsedBytes": 24159191040,
        "memoryTotalBytes": 42949672960,
        "gpuUtilization": 0.94,
        "memoryUtilization": 0.56,
        "everUsed": true
      }
    ]
  }
  ```

---

## 2. Viral Open-Source Benchmark: `farion1231/cc-switch` & `yetone/magpie`

To make ColabHive an instant GitHub star-magnet, we distill the exact architectural and UX mechanics that propelled **`farion1231/cc-switch`** (`ccswitch.io`, Tauri 2 + Rust + React + SQLite) and **`yetone/magpie`** (`usemagpie.ai`, Go + Wails + Local Gateway + TUI/CLI/Docker) to massive developer adoption.

### 2.1 Why `cc-switch` & `magpie` Went Viral: 6 Product Laws for ColabHive

| Viral Pattern | How `cc-switch` & `magpie` Execute It | How ColabHive Adopts & Elevates It |
| :--- | :--- | :--- |
| **1. Surgical, Non-Destructive Config Injection ("Minimal Intrusion")** | Never overwrites whole config files. Backs up originals before first write (`~/.cc-switch/backups/live-first-write/`), performs AST/key-level atomic writes preserving user comments/formatting/hooks, and cleanly restores original configs on toggle-off or `--uninstall`. | **`colabhive connect <agent>` & 1-Click UI Sync Toggles**: Injects *only* `mcpServers.colabhive` (plus optional `colab-operator` skill symlink), backs up untouched configs to `~/.colabhive/backups/first-write/`, and removes only its own key on disconnect. |
| **2. Zero-Config Existing Credential Discovery** | On first launch, auto-discovers existing local sign-ins (`~/.claude`, `~/.codex`, Keychain) so the dashboard is populated in 0 seconds, with a privacy toggle (`MAGPIE_NO_ACCOUNTS=1`). | On first launch, ColabHive offers **1-Click Local Import** from `google-colab-cli` keyring (`service="google-colab-cli"`), legacy `~/.config/colab-cli/token.json`, `keyrings.alt` `keyringrc.cfg`, and `gcloud` ADC (`--no-auto-discovery` opt-out). |
| **3. At-a-Glance Quota Cards with Reset Countdowns** | Provider cards & system tray show live remaining window percentages and reset timers (`"5h 94% left · resets 18:00"`), plus health/circuit-breaker badges. | **ColabHive Compute Cards**: Show Tier (`Pro+` / `Pro` / `Free`), Paid CU tank + burn ETA (`"482.4 CU · 54h left at 8.88/hr"`), Free milli-CU tank + refill countdown (`"14.5 Free CU · refills in 3h 12m"`), active GPU VRAM bar, Contributor cap badge, and Proxy status. |
| **4. Smart Quota-Aware Routing Modes** | `magpie` offers `smart` (consume quota renewing soonest so zero quota is wasted at reset), `pace` (burn evenly across time window), `usage` (least-used first), `order`, plus sticky conversation affinity. | **ColabHive Router Policies**: `waste-zero` (prioritize expiring 90-day CUs & refilling Free milli-CCUs), `max-balance` (spread heavy sharded runs), `contributor-fair-share` (respect donated caps), `sticky-session` (pin agent conversation to warm kernel), and `auto-tier-fallback` (`H100 -> A100 -> G4 -> L4 -> T4`). |
| **5. `ccswitch://` Deep Links & "Add to App" Web Buttons** | `ccswitch://` URI protocol + `ccswitch.io/deplink.html` lets docs/websites/teammates share 1-click import links for providers, MCP servers, and skills. | **`colabhive://` Deep Links & Shareable Connect Snippets**: Team admins share `colabhive://connect?url=https://hive.lab.internal/mcp&key=ch_sk_...` or 1-click CLI `colabhive connect --all` to wire an engineer's entire IDE/CLI fleet in 2 seconds. |
| **6. Dual Desktop / Web / CLI / Docker Ergonomics** | `magpie` ships one Go binary serving Web UI, Tray, TUI (`magpie tui`), CLI, and Docker (`ghcr.io/...`); `cc-switch` pairs its desktop app with WSL2 UNC path awareness (`\\wsl.localhost\Ubuntu\home\...`). | ColabHive runs identically on a developer laptop (`colabhive up` -> opens Web Observatory + local MCP router, auto-detecting Windows + WSL2 agents) OR on a team Linux/Docker server (`docker compose up -d`). |

---

### 2.2 Exact 1-Click Agent MCP Config Injection Matrix

Verified from `farion1231/cc-switch`, `yetone/magpie/docs/reference.md`, and Antigravity's official `mcp_servers.md`:

> [!WARNING]
> **Critical Transport Compatibility Rule**: Antigravity's `mcp_config.json` **rejects legacy HTTP+SSE (`2024-11-05`)** and requires **MCP Streamable HTTP (`2025-03-26`) at `/mcp`** or **local `stdio`**. Therefore, ColabHive MUST expose:
> 1. **Local `stdio` bridge**: `colabhive mcp stdio` (authenticates via `COLABHIVE_API_KEY` env var or local daemon socket).
> 2. **Remote Streamable HTTP (`2025-03-26`)**: `POST/GET /mcp` supporting **both** `Authorization: Bearer ch_sk_...` header **and** URL token `/mcp?key=ch_sk_...` (for clients whose config schema only accepts `"url"` without custom `"headers"`).

| Target AI Tool | Config File Path (macOS / Linux / Windows / WSL2) | Format | Exact Surgical Key Injected by ColabHive | Hot-Reload Behavior |
| :--- | :--- | :--- | :--- | :--- |
| **Antigravity (AGY)** | `~/.gemini/config/mcp_config.json`<br>*(Win: `%USERPROFILE%\.gemini\config\mcp_config.json`)* | JSON | `mcpServers.colabhive = { "url": "http://127.0.0.1:8765/mcp?key=ch_sk_..." }` *(or `stdio` `"command": "colabhive", "args": ["mcp", "stdio"]`)* | Auto-discovered on next session / MCP refresh |
| **Claude Code** | `~/.claude.json` *(global user MCP)* or `<workspace>/.mcp.json` *(team)* | JSON | `mcpServers.colabhive = { "type": "http", "url": "http://127.0.0.1:8765/mcp", "headers": { "Authorization": "Bearer ch_sk_..." } }` | Immediate (`claude mcp list` verifies) |
| **Claude Desktop** | macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`<br>Win: `%APPDATA%\Claude\claude_desktop_config.json` | JSON | `mcpServers.colabhive = { "command": "colabhive", "args": ["mcp", "stdio"], "env": { "COLABHIVE_API_KEY": "ch_sk_..." } }` | Requires Claude Desktop restart (UI shows toast reminder) |
| **OpenAI Codex CLI / App** | `~/.codex/config.toml`<br>*(Win: `%USERPROFILE%\.codex\config.toml`)* | TOML | `[mcp_servers.colabhive]`<br>`url = "http://127.0.0.1:8765/mcp"`<br>`bearer_token_env_var = "COLABHIVE_API_KEY"` *(or `command = "colabhive"`, `args = ["mcp", "stdio"]`)* | Restart `codex` / Codex daemon (`cc-switch` pattern: offer 1-click check) |
| **Gemini CLI** | `~/.gemini/settings.json`<br>*(Win: `%USERPROFILE%\.gemini\settings.json`)* | JSON | `mcpServers.colabhive = { "httpUrl": "http://127.0.0.1:8765/mcp", "headers": { "Authorization": "Bearer ch_sk_..." }, "timeout": 60000 }` | Restart `gemini` CLI |
| **Cursor** | `~/.cursor/mcp.json` *(global)* or `.cursor/mcp.json` *(workspace)* | JSON | `mcpServers.colabhive = { "url": "http://127.0.0.1:8765/mcp?key=ch_sk_..." }` | Immediate in Cursor MCP panel |
| **Windsurf** | `~/.codeium/windsurf/mcp_config.json` | JSON | `mcpServers.colabhive = { "serverUrl": "http://127.0.0.1:8765/mcp?key=ch_sk_..." }` | Reload Windsurf MCP plugins |
| **OpenCode** | `~/.config/opencode/opencode.jsonc` | JSONC | `mcp.colabhive = { "type": "remote", "url": "http://127.0.0.1:8765/mcp", "headers": { "Authorization": "Bearer ch_sk_..." }, "enabled": true }` | Preserves `//` comments via JSONC AST edit |
| **WSL2 Cross-Boundary (Windows Host)** | `\\wsl.localhost\<Distro>\home\<user>\.{claude.json,codex/config.toml,gemini/settings.json}` | JSON / TOML | Uses `stdio` bridge or WSL2 host-reachable endpoint (detects NAT vs `networkingMode=mirrored`, warning user just like `cc-switch`!) | Syncs Windows + WSL2 agents in one click |

---

## 3. Zero-Friction External Contributor Authorization Specification
### ("方便别人把有算力的账号直接授权给我们的开源程序去调度")

### 3.1 Product Goal & Threat Model

In research labs, hackathon teams, and open-source AI communities, many people hold **idle Colab Pro / Pro+ compute units (which expire after 90 days!)** or unused **daily Free GPU quotas**, while a teammate's autonomous coding/ML agent needs 4–8 GPUs in parallel for hyperparameter sweeps or evaluation shards.

**Design Mandate**: Allow any external contributor to donate idle Colab compute to a ColabHive instance in **under 30 seconds via a shareable link**, with **zero password sharing**, **zero Google Drive exposure**, **hard compute-unit safety caps**, and **instant 1-click self-service revocation**.

#### Five Hard Security & Privacy Invariants

1. **Least-Privilege OAuth Scope (`colaboratory` ONLY)**:
   * Contributor OAuth URLs request **strictly**:
     `openid https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/colaboratory`
   * **NEVER** request `drive`, `drive.file`, `drive.readonly`, or `cloud-platform` on contributor flows. Contributors receive cryptographic assurance from Google's own consent screen that ColabHive **cannot read or modify their Google Drive or GCP billing**.
2. **WebSocket Kernel Auth-Propagation Firewall**:
   * As revealed in `colab-vscode/src/jupyter/colab-proxy-websocket.ts`, code running inside a Colab kernel (e.g., `google.colab.drive.mount('/content/drive')` or `google.colab.auth.authenticate_user()`) sends a Jupyter message `{ header: { msg_type: "colab_request" }, metadata: { colab_request_type: "request_auth", colab_msg_id } }`.
   * For any account where `is_contributor == true` (or `allow_credential_propagation == false`), ColabHive's WebSocket proxy **intercepts and immediately rejects** `colab_request` with `ColabInputReplyMessage { error: "Blocked by ColabHive Contributor Firewall: Drive/GCP credential propagation is disabled on donated accounts." }` and **never calls** `POST /v1/credential-propagation:enable`.
3. **Zero Token Leakage to Downstream Agents**:
   * AI agents and researchers authenticate to ColabHive exclusively using scoped ColabHive API keys (`ch_sk_...`). Raw Google `access_token`, `refresh_token`, and `X-Colab-Runtime-Proxy-Token` values never leave the ColabHive backend process and are encrypted at rest (`AES-256-GCM` keyed by `COLABHIVE_MASTER_KEY`).
4. **Pre-Flight + Live-Burn CU Reserve Floor Enforcement**:
   * If a contributor sets `min_reserve_cu = 50.0` ("Always keep at least 50 CUs for my own coursework"), ColabHive refuses new GPU assignments when `paidComputeUnitsBalance - max(2.5, estimated_15m_burn) <= 50.0`, and automatically drains + unassigns any running VM the moment live `v1/user-info` polling approaches `50.0 + safety_margin_cu`.
5. **Per-Account Sticky Egress Proxy Isolation**:
   * To protect contributors from Google's multi-account IP correlation (`DENYLISTED` in `colab-vscode/src/colab/client/v2/index.ts`), each contributed account can bind to a dedicated `http://` or `socks5://` proxy (or inherit a pool proxy slot), used consistently across OAuth token refresh, control-plane API calls, and Jupyter WebSocket tunnels.

---

### 3.2 End-to-End Contributor Authorization UX & Sequence Flow

```mermaid
sequenceDiagram
    autonumber
    actor Admin as Pool Admin
    participant Hive as ColabHive Server (Guest Portal)
    actor Contrib as External Contributor (Browser)
    participant Google as Google OAuth2 & Colab GAPI

    Admin->>Hive: POST /api/v1/contribute/invites (label="ICML Sweep", expires_in="7d", default_reserve_cu=50)
    Hive-->>Admin: Shareable Link: https://hive.example.com/contribute/ch_inv_8x92kP

    Admin->>Contrib: Sends link over Slack / WeChat / Discord
    Contrib->>Hive: Opens /contribute/ch_inv_8x92kP (No login required!)
    Hive-->>Contrib: Renders Bilingual Guest Portal: Pool Identity + Interactive Guardrail Sliders

    Note over Contrib: Sets Guardrails: Keep >= 50 CU | Allow T4/L4/A100 | Max 1 VM | Auto-expire in 14d

    alt Mode A: Universal Zero-Domain Flow (Default — Works on localhost / LAN / Tailscale / NAT!)
        Contrib->>Hive: Clicks "Generate Google Auth Link" (submits guardrails)
        Hive-->>Contrib: Returns Official Google Auth URL (client_id=764086051850-..., redirect_uri=sdk.cloud.google.com/applicationdefaultauthcode.html, token_usage=remote, PKCE S256)
        Contrib->>Google: Approves "Colaboratory" scope in Google tab -> Copies Auth Code (4/0A...)
        Contrib->>Hive: Pastes Auth Code (or full redirect URL) into Step 2 box
    else Mode B: 1-Click Direct Web OAuth Callback (Custom HTTPS Domain Configured)
        Contrib->>Google: Clicks "1-Click Authorize with Google" (redirect_uri=https://hive.../api/v1/contribute/callback, state=signed_nonce)
        Google->>Hive: GET /api/v1/contribute/callback?code=4/0A...&state=...
    end

    Hive->>Google: POST https://oauth2.googleapis.com/token (exchange code + code_verifier -> refresh_token)
    Hive->>Google: GET /oauth2/v2/userinfo + GET colab.pa.googleapis.com/v1/user-info?get_ccu_consumption_info=true
    Google-->>Hive: { email: "alice@uni.edu", tier: "PRO_PLUS", paidComputeUnitsBalance: 320.5, eligibleAccelerators: [...] }
    Hive->>Hive: Encrypts refresh_token (AES-256-GCM), saves guardrails, mints Contributor Receipt Token (ch_rcpt_...)
    Hive-->>Contrib: Renders Private Contributor Receipt Card (/contribute/manage/ch_rcpt_...) with Live Telemetry & 1-Click Revoke!
```

---

### 3.3 Three Supported Onboarding Modes (Works Everywhere)

ColabHive supports **three authorization modes simultaneously** so open-source users never hit an OAuth wall regardless of how they deploy ColabHive:

| Mode | When Used | `client_id` & `redirect_uri` | User Friction |
| :--- | :--- | :--- | :--- |
| **Mode A: Universal Remote Auth-Code Copy-Paste (Default)** | Zero-config out-of-the-box! Works on `localhost`, Docker, campus LAN, Tailscale, Cloudflare Tunnel, or SSH port-forward without registering a GCP OAuth app. | Official `colab-cli` client `764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com` + `https://sdk.cloud.google.com/applicationdefaultauthcode.html` (`prompt=consent&token_usage=remote` + PKCE `S256`). | **2 clicks + 1 paste** (~20 seconds). Smart input box accepts either raw `4/0A...` auth code OR full URL if pasted accidentally. |
| **Mode B: 1-Click HTTPS Web Callback** | Team deployments with a public HTTPS URL and custom GCP OAuth Web Client (`COLABHIVE_OAUTH_CLIENT_ID` / `SECRET` configured). | Admin's OAuth Web Client + `https://<colabhive-host>/api/v1/contribute/callback` *(Note: automatically uses Tunnel API `/tun/m/...` if custom GCP project isn't allowlisted on `colaboratory.googleapis.com`!)*. | **1 click** (~8 seconds, zero copy-paste). |
| **Mode C: Contributor CLI / Portable Bundle** | Power contributors who already use `colab` CLI or prefer terminal donation. | `uvx colabhive donate https://<host>/contribute/<invite_code> --reserve-cu 50 --allow-gpu T4,L4,A100` | **1 terminal command** (runs local loopback auth on contributor's machine or reads consented local token and uploads over TLS). |

---

### 3.4 Contributor Safety Guardrails & Scheduler Enforcement Rules

Every contributed account carries an immutable-by-agents `AccountGuardrails` policy configurable by the contributor at onboarding and editable anytime via their **Contributor Receipt Portal (`/contribute/manage/:receipt_token`)**:

```json
{
  "min_reserve_cu": 50.0,
  "max_donated_cu_total": 150.0,
  "max_donated_cu_daily": 30.0,
  "allow_paid_cu_burn": true,
  "allowed_variants": ["CPU", "GPU"],
  "allowed_accelerators": ["T4", "L4", "G4", "A100"],
  "max_concurrent_runtimes": 1,
  "active_window": {
    "timezone": "Asia/Shanghai",
    "days_of_week": [1, 2, 3, 4, 5, 6, 0],
    "start_hour": "22:00",
    "end_hour": "09:00"
  },
  "expires_at": "2026-12-05T00:00:00Z",
  "allow_credential_propagation": false,
  "mask_email_in_dashboard": false,
  "proxy_url": "socks5://user:pass@proxy-hk-02.internal:1080"
}
```

#### Exact Scheduler Enforcement State Machine

1. **Pre-Allocation Admission Filter (`can_schedule(account, requested_spec)`)**:
   * Rejects immediately if `now > expires_at`, `status != ACTIVE`, or `now` is outside `active_window`.
   * Rejects if `requested_spec.accelerator` is not in `allowed_accelerators` or `active_assignments >= max_concurrent_runtimes`.
   * If `allow_paid_cu_burn == false`: allows assignment **only** if `paidComputeUnitsBalance == 0` (Free tier / milli-CCU mode) or `estimated_cu_per_hour == 0`.
   * **Reserve Floor Check**: Computes `effective_donatable_cu = min(paidComputeUnitsBalance - min_reserve_cu, max_donated_cu_total - donated_cu_used_total, max_donated_cu_daily - donated_cu_used_today)`. Requires `effective_donatable_cu >= max(2.0, estimated_cu_rate_hr * 0.25)` (at least 15 minutes of safe runway above the reserve floor!).
2. **60-Second Live Burn Watchdog (`GuardrailEnforcer` hooked into `ConsumptionPoller`)**:
   * Every 60s on `v1/user-info` update, records `delta_cu` burned by ColabHive-owned runtimes into `contribution_ledger`.
   * When `effective_donatable_cu <= consumptionRateHourly * (5 / 60)` (within **5 minutes** of hitting the contributor's reserve floor or daily/total cap) OR within 5 minutes of `active_window.end_hour`:
     1. Emits a `ColabHiveGuardrailPreempt` warning event on the live experiment WebSocket stream.
     2. Gracefully deletes Jupyter sessions and calls `unassign` (`DELETE /v1beta/runtimes/{id}` or `POST /tun/m/unassign/{endpoint}`) **before** breaching the contributor's `min_reserve_cu`!
     3. Transitions account state to `PAUSED_CAP_REACHED` or `PAUSED_SCHEDULE_WINDOW`.
3. **1-Click Self-Service Emergency Kill & Revoke (`POST /api/v1/contribute/manage/:receipt_token/revoke`)**:
   * Contributor clicks **"Stop & Revoke My Account"** on their bookmarked Receipt Page (no admin approval needed!):
     1. Immediately terminates & unassigns all ColabHive-provisioned runtimes on that Google account.
     2. Calls `POST https://oauth2.googleapis.com/revoke?token=<refresh_token>` to invalidate the OAuth grant at Google.
     3. Permanently wipes the encrypted `refresh_token` and `access_token` from SQLite, leaving only anonymized ledger totals if desired.
   * Also handles external revocation gracefully: if the contributor revokes access via https://myaccount.google.com/permissions, the next refresh receives `400 invalid_grant` (`isInvalidGrantError` from `colab-vscode`) and automatically transitions the account to `REVOKED_BY_USER`.

---

### 3.5 Guest Portal & Contributor Receipt API Endpoints

| Method & Path | Auth | Purpose |
| :--- | :--- | :--- |
| `POST /api/v1/contribute/invites` | Admin Bearer | Create a shareable invite code (`ch_inv_...`) with pool title, welcome note, TTL, max redemptions, default guardrails, and optional auto-assigned proxy group. |
| `GET /api/v1/contribute/invites` | Admin Bearer | List active/expired invite links and redemption counts. |
| `DELETE /api/v1/contribute/invites/:code` | Admin Bearer | Revoke an invite link (existing contributed accounts remain unaffected unless revoked). |
| `GET /api/v1/contribute/portal/:code` | **Public (Rate-Limited)** | Fetch public invite metadata (`pool_name`, `maintainer_name`, `default_guardrails`, `supported_auth_modes`, `required_scopes`) to render the Guest Portal UI. |
| `POST /api/v1/contribute/portal/:code/start` | **Public (Rate-Limited)** | Submit chosen `AccountGuardrails`, generate server-side PKCE `code_verifier` + `state` session (`TTL = 10m`), and return Google OAuth `auth_url`. |
| `POST /api/v1/contribute/portal/:code/complete` | **Public (Rate-Limited)** | Exchange pasted `code` (or full callback URL) + stored `code_verifier` for tokens, probe `v1/user-info` + dual API capability, persist encrypted account + guardrails, and return `{ receipt_token: "ch_rcpt_...", receipt_url, account_summary }`. |
| `GET /api/v1/contribute/callback` | **Public OAuth Callback** | Mode B direct HTTPS callback handler; completes exchange and `302` redirects contributor's browser straight to `/contribute/manage/:receipt_token`. |
| `GET /api/v1/contribute/manage/:receipt_token` | **Receipt Token (`ch_rcpt_...`)** | Contributor self-service status JSON: live CU balance, `min_reserve_cu`, CUs donated today/total, currently running ColabHive VMs (hardware + runtime duration, zero experiment code leaked!), and guardrail config. |
| `PATCH /api/v1/contribute/manage/:receipt_token` | **Receipt Token (`ch_rcpt_...`)** | Update contributor guardrails (`min_reserve_cu`, `allowed_accelerators`, `active_window`, or toggle `paused: true/false`) in real time. |
| `POST /api/v1/contribute/manage/:receipt_token/revoke` | **Receipt Token (`ch_rcpt_...`)** | 1-click kill active VMs + Google OAuth token revocation + secret wipe. |

---

## 4. Architectural Synthesis: How ColabHive Unifies Everything

```mermaid
flowchart TB
    subgraph Agents["AI Coding Agents & Researchers (1-Click Surgical Config Sync)"]
        CC["Claude Code / Desktop"]
        CX["Codex CLI / App"]
        GM["Gemini CLI"]
        AG["Antigravity / Cursor / Windsurf"]
        CLI["colabhive CLI (100% colab-cli compatible + --shards / --strategy)"]
    end

    subgraph Hive["ColabHive Control Plane & Router"]
        MCP["Unified MCP Server<br/>(stdio + Streamable HTTP /mcp)<br/>Static Tool Registry (No list_changed bug!)"]
        RTR["Smart Compute Router<br/>(waste-zero / max-balance / sticky-session / auto-tier-fallback)"]
        GRD["Contributor Guardrail Enforcer &<br/>WebSocket Auth-Propagation Firewall"]
        PORTAL["Zero-Friction Contributor Guest Portal<br/>(/contribute/:invite_code + /manage/:receipt)"]
    end

    subgraph Google["Official Google Colab Dual-Backend Infrastructure (Per-Account Proxy Isolated)"]
        PUB["Colab Public API<br/>colaboratory.googleapis.com/v1beta"]
        TUN["Colab Session Tunnel API<br/>colab.research.google.com/tun/m"]
        GAPI["Colab GAPI Quota Poller<br/>colab.pa.googleapis.com/v1/user-info"]
        VMS["Colab GPU/TPU Runtimes<br/>(Jupyter ZMQ WS + /api/colab/resources)"]
    end

    Agents --> MCP & CLI
    MCP & CLI --> RTR
    PORTAL --> GRD
    RTR --> GRD
    GRD --> PUB & TUN & GAPI & VMS
```

### Summary of Key Implementation Mandates for Downstream Builders

1. **Dual-Backend Colab Client**: Implement `Public API` (`colaboratory.googleapis.com/v1beta`) + `Tunnel API` (`colab.research.google.com/tun/m/...`) auto-fallback, using `colab.pa.googleapis.com/v1/user-info?get_ccu_consumption_info=true` as the primary quota/tier/free-milli-CCU poller and `GET {proxy_url}/api/colab/resources` for zero-overhead 10s GPU/VRAM/RAM telemetry.
2. **MCP Server Compatibility**: Serve **both** `stdio` and **MCP `2025-03-26` Streamable HTTP at `/mcp`**, pre-register all tools statically at startup (avoiding `googlecolab/colab-mcp`'s `list_changed` invisibility bug), and structure execution outputs after Google's official `ExecuteCodeResult` (`richOutputs` written out-of-band rather than bloating agent context).
3. **`cc-switch` & `magpie` Viral Polish**: Ship surgical, backup-first 1-click config injectors (`colabhive connect`) for Antigravity, Claude Code, Claude Desktop, Codex, Gemini CLI, Cursor, Windsurf, and OpenCode (including WSL2 UNC paths), `colabhive://` deep links, and live quota-countdown account cards.
4. **External Contributor Portal First-Class Citizen**: Ship `/contribute/:invite_code` and `/contribute/manage/:receipt_token` with Mode A (`sdk.cloud.google.com/applicationdefaultauthcode.html` zero-domain remote auth) enabled out-of-the-box, strict `colaboratory`-only scope, WebSocket `colab_request` credential-propagation firewall, hard `min_reserve_cu` watchdog preemption, per-account proxy binding, and 1-click self-service revoke.
