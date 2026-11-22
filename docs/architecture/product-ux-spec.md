# ColabHive — Product Architecture & Flagship UI/UX Specification

> **Document Owner**: `product-recon`  
> **Target Repository**: `colabhive` (`d:\git_proj\colabhive`)  
> **Inspiration Lineage**: [`Wei-Shaw/sub2api`](https://github.com/Wei-Shaw/sub2api) (Multi-Account OAuth Gateway, Proxy Isolation, Sticky Routing, Cooldowns & Scoped Keys) × [`yetone/magpie`](https://github.com/yetone/magpie) & [`magpie-quota-estimator`](https://github.com/ZiHao256/magpie-quota-estimator) (Precision Quota Observatory, Equivalence Chips & 1-Click Agent Switcher) × Battle-Tested `colab` CLI Kernel Persistence & Sharded GPU Workflows  
> **Default Locale**: `zh-CN` (Simplified Chinese) with instant zero-reload `en-US` toggle  
> **Design Archetype**: **Precision Compute Observatory** (Obsidian Telemetry Dark & Drafting Alabaster Light)

---

## 1. Product Thesis & Reference Architecture Synthesis

### 1.1 The Problem ColabHive Solves
AI researchers, indie ML engineers, and autonomous coding agents (Antigravity, Claude Code, Cursor, Codex) routinely hold **multiple Google accounts** across tiers (Google AI Ultra, Colab Pro+, Colab Pro, Pay-As-You-Go, and Free accounts). Today, this compute capacity is trapped in fragmented browser tabs and single-account CLI keyrings:
1. **Invisible & Expiring Quota**: Compute Units (CUs) expire 90 days after purchase or reset on monthly subscription boundaries, yet there is no unified dashboard translating raw CUs into **real remaining GPU runtime hours** (`G4`, `H100`, `A100`, `L4`, `T4`, `TPU v6e`).
2. **Silent Compute Drain**: Detached processes that hit Linux zombie states (`Z`), dropped ZMQ WebSockets, or forgotten idle notebooks silently burn `8.88–18.0 CU/hr` for hours.
3. **Single-Account Bottlenecks & Tier Rejections**: Provisioning `H100` or multiple `G4` 96GB GPUs on one account frequently hits HTTP `400` entitlement/concurrency walls, whereas sharding a benchmark across **4–6 accounts in parallel** costs the exact same total CUs (`4 GPUs × 1 hr == 1 GPU × 4 hr`) while delivering a **4×–6× wall-clock speedup**.
4. **High Friction for Agents**: Coding agents cannot interactively complete browser OAuth, manage per-account proxies, survive 15-minute idle reapers, or switch accounts mid-experiment when one account hits zero CUs.

### 1.2 Reference DNA Matrix (`sub2api` × `magpie` × `ColabHive`)

| Capability Pillar | Reference Pattern Adopted | ColabHive Domain Adaptation |
| :--- | :--- | :--- |
| **Headless OAuth Onboarding** | **`sub2api`**: Select proxy -> Generate OAuth URL -> Authorize in any browser -> Paste callback URL or Auth Code | **4-Step Google OAuth2 Wizard**: Binds per-account HTTP/SOCKS5 proxy *before* token exchange, parses pasted `localhost` redirect URLs or raw codes, and immediately hydrates `ccu-info` balance & tier entitlements. |
| **Credential Portability** | **`sub2api`**: Admin JSON batch import/export & CLI migration scripts | **Zero-Friction Transfer Hub**: 1-click auto-discovery of local/WSL2 `colab` CLI `keyringrc.cfg`, `gcloud` ADC JSON, raw `refresh_token` JSON, and AES-256-GCM `.chvault` cross-server bundles. |
| **Sticky Session + Escape** | **`sub2api`**: `session_hash` affinity to preserve context/cache + Sticky Escape when upstream degrades | **Stateful Kernel Affinity**: Pins an Agent/MCP `session_id` to a live Colab Jupyter Kernel (preserving Python RAM globals & `/root/.cache/huggingface` 60GB weights), escaping cleanly only on VM death or quota exhaustion. |
| **Cooldown & Health FSM** | **`sub2api`**: Automatic 429 cooldowns, concurrency caps, and group-scoped API keys | **2-Dimensional Health + Accelerator Matrix**: Tracks account health *and* per-GPU entitlement (e.g. `H100: HTTP 400 Restricted` while `G4 / A100: Available`), plus scoped `ch_sk_...` keys with CU budgets. |
| **Quota Observatory Cards** | **`magpie` / `magpie-quota-estimator`**: Clean linear progress tracks, reset countdowns, and `Whole ≈ $Val` equivalence tags | **GPU-Hour Equivalence Matrix**: Converts raw CUs into exact `Xh Ym` runtime across `G4`, `H100`, `A100`, `L4`, `T4`, `TPU v6e/v5e`, dynamically calibrated via live EWMA burn rates from Colab `ccu-info`. |
| **1-Click Agent Injection** | **`magpie`**: Unified local gateway & seamless native config injection for Claude Code, Codex, etc. | **1-Click Agent Connect Drawer**: Generates & optionally injects ready-to-run MCP (`stdio` / `SSE` / `Streamable HTTP`), CLI, and Python SDK configs for Antigravity, Claude Code, Cursor, and Codex. |

---

## 2. Global Information Architecture & Navigation Shell

ColabHive runs as a **single self-contained binary / Docker container** serving both the API/MCP Control Plane and the Web Observatory UI on port `7860` (configurable via `COLABHIVE_PORT`).

### 2.1 Application Shell Layout
```text
┌──────────────────────────────────────────────────────────────────────────────────────────────────────┐
│ [🐝 ColabHive v1.0]  [● Pool: 1,428.4 CU · 🔥 28.20 CU/h · 3 Active VMs]   [👁️ 晒图脱敏] [中/EN] [⚡ Connect Agent] │
├────────────────────┬─────────────────────────────────────────────────────────────────────────────────┤
│ NAVIGATION (220px) │ MAIN WORKSPACE CANVAS (Fluid Grid, Max 1680px, Hairline Precision Borders)      │
│                    │                                                                                 │
│ ◈ 总览观测台        │  ┌─────────────────┬─────────────────┬─────────────────┬─────────────────────┐  │
│   Observatory  [/] │  │ Hero KPI 1      │ Hero KPI 2      │ Hero KPI 3      │ Hero KPI 4 & 5      │  │
│                    │  └─────────────────┴─────────────────┴─────────────────┴─────────────────────┘  │
│ ◈ 账号池与凭证      │  ┌───────────────────────────────────────────────────────────────────────────┐  │
│   Accounts [/pool] │  │ Cloud GPU Equivalent Remaining Runtime Matrix (Pool / Per-Account Toggle) │  │
│                    │  └───────────────────────────────────────────────────────────────────────────┘  │
│ ◈ 实时实验工作台    │  ┌───────────────────────┬───────────────────────┬───────────────────────────┐  │
│   Workbench [/exp] │  │ Magpie Account Card 1 │ Magpie Account Card 2 │ Magpie Account Card 3     │  │
│                    │  └───────────────────────┴───────────────────────┴───────────────────────────┘  │
│ ◈ Agent 路由中枢    │                                                                                 │
│   Router [/router] │                                                                                 │
│                    │                                                                                 │
│ ◈ 密钥与代理设置    │                                                                                 │
│   Keys/Proxy [/cfg]│                                                                                 │
├────────────────────┴─────────────────────────────────────────────────────────────────────────────────┤
│ STATUS FOOTER: Daemon Healthy · Keepalive Loop: 8s · Anti-Zombie Watchdog: Armed · WS Latency: 18ms  │
└──────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

### 2.2 Primary Route Map
| Route | Nav Label (`zh-CN` / `en-US`) | Core Responsibility |
| :--- | :--- | :--- |
| `/` | **总览观测台** / `Observatory` | Hero telemetry strip, Cloud GPU Equivalence Matrix, Magpie-style Account Quota Grid, Active VM quick-strip. |
| `/pool` | **账号池与凭证** / `Account Pool` | Account table/grid, 4-Step OAuth2 Wizard, Zero-Friction Credential Import/Export Hub, Account Groups (`group_ids`), Health/Cooldown FSM controls. |
| `/exp` & `/exp/:id` | **实时实验工作台** / `Workbench` | Multi-shard experiment groups, live WebSocket ANSI terminal, cell execution waterfall, inline Matplotlib gallery, VRAM/SM%/Temp telemetry, `/content` file browser. |
| `/router` | **Agent 路由中枢** / `Agent Router` | Live Sankey/Node topology (`Agent -> Strategy -> Session -> Account -> GPU`), Routing Strategy configurator, Sticky Session bindings, Live MCP/API Trace Inspector. |
| `/cfg` | **密钥与代理** / `Keys & Proxies` | Scoped Agent API keys (`ch_sk_...`) with CU caps, HTTP/SOCKS5 Proxy Pool manager with latency/IP check, Webhook alerts (Telegram / Bark / Discord / Feishu), System backup. |

> **Global Persistent Actions (Top Right Header)**:
> - **`[👁️ 晒图脱敏 / Privacy Mask]`**: 1-click toggle that masks all emails (`hex***@gmail.com`), proxy IPs (`198.51.*.*`), and session secrets across the entire UI, plus an **"Export Share Card (PNG)"** button for safe community sharing.
> - **`[中 / EN]`**: Instant zero-reload language toggle (`zh-CN` ↔ `en-US`).
> - **`[⚡ 接入 Agent / Connect Agent]`**: Opens the global right-hand slide-over **1-Click Agent Connect Drawer** from any screen.

---

## 3. Module 1: Account Pool & Credential Onboarding Flows (`/pool`)

### 3.1 Account Pool Data Model & Isolation Primitives
Every pooled Google account is managed as an isolated compute unit provider with strict network and group boundaries:
- **Identity & Tier Metadata**: `account_id` (`acc_...`), `email`, `display_alias`, detected tier badge (`AI Ultra`, `Colab Pro+`, `Colab Pro`, `Pay-As-You-Go`, `Free`), `group_ids` (e.g., `["research-core", "high-vram", "shared-team"]`), priority weight (`1–100`), and `max_concurrent_vms` (default `2` for Pro/Free, up to `5` for Pro+/Ultra).
- **Per-Account Proxy Isolation (`proxy_id`)**:
  - Supports `direct`, `http://user:pass@host:port`, `https://...`, and `socks5://user:pass@host:port`.
  - **Strict Egress Pinning Rule**: Every outbound packet for a given account—OAuth2 token exchange, token refresh, Colab `ccu-info` polling, `assign`/`unassign`, and Jupyter ZMQ-over-WebSocket tunnels—MUST traverse its bound `proxy_id` with a consistent User-Agent fingerprint so Google never flags sudden geo/ASN jumps.

### 3.2 Flow A: 4-Step Headless-Safe OAuth2 Auth-Code Wizard
Designed specifically so users running ColabHive on a local Windows/macOS machine, WSL2, or a remote headless Linux VPS / Docker container can onboard any Google account in `< 25 seconds` without fighting localhost port-forwarding or cookie collisions.

```mermaid
sequenceDiagram
    autonumber
    actor User as User (Any Browser / Profile)
    participant UI as ColabHive Web Modal
    participant API as ColabHive Control Plane
    participant Proxy as Bound Proxy (Optional)
    participant Google as Google OAuth2 + Colab API

    User->>UI: Click "+ 添加账号 (Add Account)"
    UI->>API: Step 1: Select Proxy + Groups/Tags + Click "Test Proxy"
    API->>Proxy: Probe https://colab.research.google.com (Measure RTT & Exit IP)
    Proxy-->>UI: 200 OK · Exit IP: 45.xxx.xxx.12 (US-California · 62ms)
    UI->>API: Step 2: POST /api/v1/accounts/oauth/start {proxy_id, group_ids, alias}
    API-->>UI: Generate Auth URL (PKCE code_challenge + CSRF state + offline access)
    User->>Google: Open Auth URL in Incognito / Profile Browser & Approve
    Google-->>User: Redirect to localhost callback OR display Authorization Code
    User->>UI: Step 3: Paste FULL localhost URL OR raw Auth Code
    UI->>API: POST /api/v1/accounts/oauth/exchange {session_state, raw_input}
    API->>API: Smart-parse `code` & validate `state` from URL or raw string
    API->>Proxy: Exchange `code` + `code_verifier` for `access_token` & `refresh_token`
    Proxy->>Google: POST oauth2.googleapis.com/token + GET colab ccu-info
    Google-->>API: Tokens + {paid_balance: 482.5, burn_rate: 0, eligible_gpus: ["G4","A100","L4","T4"]}
    API-->>UI: Step 4: Instant Account Card Preview (Balance + GPU Hours Hydrated!)
```

#### Wizard Step-by-Step UX Specification:
1. **Step 1 — Network & Grouping Pre-Flight (`网络隔离与分组`)**:
   - Dropdown to pick an existing proxy from the Proxy Pool, inline form to add a new `HTTP/SOCKS5` URI, or `Direct Connection (直连)`.
   - **`[测试连通性 / Test Egress]`** button renders an inline telemetry pill: `✓ Connected · Exit IP: 38.x.x.x (US-LAX) · 58ms`.
   - Multi-select tag chips for `Account Groups` (`default`, `pro-plus`, `agent-pool`) + custom notes (e.g., *"Lab Account #3 — Renews monthly on 15th"*).
2. **Step 2 — Generate Scoped Authorization URL (`生成授权链接`)**:
   - Displays the generated Google OAuth2 URL in a monospace read-only box with two primary buttons: **`[📋 复制授权链接 / Copy URL]`** and **`[↗ 新窗口打开 / Open Window]`**.
   - Inline helper callout: *"💡 Tip: Paste into an Incognito window or dedicated Chrome profile if authorizing multiple Google accounts consecutively."*
3. **Step 3 — Smart Paste Box (`粘贴回调链接或授权码`)**:
   - Single forgiving input field accepting **either**:
     - A full failed browser redirect URL (e.g., `http://localhost:54321/callback?state=ch_9f8a...&code=4/0AVG7fiQ...&scope=...` — standard when ColabHive runs in Docker/remote SSH and the user's laptop browser hits `ERR_CONNECTION_REFUSED` on localhost!), **OR**
     - A raw authorization code string (`4/0AVG7fiQ...`).
   - Client-side regex immediately highlights `✓ Valid Authorization Code Detected` on paste and auto-triggers Step 4 (or enables `[立即验证并入池 / Exchange & Add]`).
4. **Step 4 — Instant Hydration & Verification (`额度探针与入池确认`)**:
   - Displays celebratory inline telemetry within 1.5s: Google email, avatar initial, detected tier (`Colab Pro+`), live Paid CU balance (`482.50 CU`), equivalent `G4 96GB` hours (`49h 56m`), and two CTA buttons: **`[完成并继续添加下一个 / Save & Add Another]`** (for rapid multi-account onboarding) or **`[完成 / Done]`**.

### 3.3 Flow B: Zero-Friction Credential Transfer & Migration Hub
Located in the **`[📥 导入 / 迁移凭证 (Import & Migrate)]`** modal with four dedicated tabs:

1. **Tab 1: 1-Click Local & WSL2 `colab` CLI Auto-Detect (`自动扫描本机 / WSL2 CLI`)**:
   - Backend scans known read-only candidate paths (only upon explicit user click + confirmation):
     - Linux / macOS: `~/.config/python_keyring/keyringrc.cfg` and `~/.config/colab/`
     - Windows Host + WSL2 Bridge: enumerates `\\wsl.localhost\<Distro>\home\<user>\.config\python_keyring\keyringrc.cfg` (e.g., Ubuntu `/home/emily/.config/python_keyring/keyringrc.cfg`) and `%APPDATA%` / `%USERPROFILE%\.config\colab`.
   - Parses `keyrings.alt.file.PlaintextKeyring` INI/config entries without touching live sessions, shows discovered account email + masked token preview (`ya29.***` / `1//0g***`), lets user assign a proxy/group, and imports in 1 click.
2. **Tab 2: `gcloud` ADC & Raw Token JSON (`ADC / JSON 凭证导入`)**:
   - Drag-and-drop zone or JSON editor accepting:
     - Google Cloud Application Default Credentials (`application_default_credentials.json` containing `client_id`, `client_secret`, `refresh_token`, `type: "authorized_user"`).
     - Single or array of raw OAuth credential objects (`[{"alias": "acc-1", "refresh_token": "1//...", "proxy": "socks5://..."}]`).
3. **Tab 3: Cross-Server Encrypted Vault Bundle (`.chvault` / Batch JSON)**:
   - **Export (`导出账号池`)**: Select all or filtered accounts -> choose **Encrypted Vault (`.chvault`, AES-256-GCM + Argon2id KDF passphrase)** (recommended) or **Plain Admin JSON** -> optional toggle to include/exclude bound Proxy Pool definitions and Routing Rules.
   - **Import (`跨服恢复`)**: Drop `.chvault` + enter passphrase -> preview conflict resolution strategy (`Skip Existing Email`, `Update Refresh Token & Proxy`, or `Merge Groups`) -> bulk verify all imported accounts concurrently (`5 accounts/sec` health check).
   - **CLI Parity**: `colabhive pool export -o pool.chvault --encrypt` and `colabhive pool import pool.chvault`.

### 3.4 Flow C: Shareable External Contributor Portal (`/contribute/:invite_token`)
To let labmates, teammates, or open-source community peers contribute idle Colab Pro/Pro+/Ultra quota **without exposing their Google password or granting ColabHive admin panel access**:
1. **Admin Creates Invite Link (`/pool` -> `[🤝 生成算力贡献邀请链接 / Create Contributor Link]`)**:
   - Generates a scoped URL (`https://hive.example.com/contribute/ch_inv_8f3k2a`) pre-bound to a target Account Group (e.g., `lab-contributors`), default egress proxy policy, and link expiry / max redemptions.
2. **Zero-Admin Guest Wizard (`/contribute/:invite_token`)**:
   - **Contributor Guardrails First**: Before authorizing, the contributor sets their own non-negotiable safety boundaries enforced by the ColabHive scheduler:
     - **Personal Safety Floor (`保底自留额度`)**: e.g., *"Never spend below `50.0 CU`"* (hard stop when `paid_balance <= 50.0`).
     - **Max Contribution Cap (`最高贡献上限`)**: e.g., *"Contribute up to `200.0 CU` total or `30.0 CU/day`"*.
     - **Schedule Window (`允许调度时段`)**: e.g., *"Idle nights/weekends only (`00:00–09:00`)"* or *"24/7"*.
   - **2-Click Google OAuth Code Exchange**: Same headless-safe Auth URL -> Paste Code/Callback flow (zero password exposure, scoped strictly to Colab runtime tokens).
   - **Self-Service Contributor Receipt (`/my-contribution/:receipt_token`)**: Issues a bookmarkable read-only receipt URL where the contributor can watch live CUs contributed, check remaining personal floor, or click **`[🛑 一键撤回授权 / Instant Disconnect & Purge Token]`** at any time.

### 3.5 Account Health & Cooldown State Machine
Inspired by `sub2api`'s battle-tested upstream scheduling FSM, ColabHive separates **Account-Level Health** from **Per-Accelerator Entitlements** so a single rejected GPU request never wastes an otherwise healthy account:

```mermaid
stateDiagram-v2
    [*] --> HEALTHY: OAuth Exchange / Import OK
    HEALTHY --> ACTIVE_WORKING: >=1 VM Assigned (Slots Available)
    ACTIVE_WORKING --> MAX_CONCURRENCY: Active VMs == max_concurrent_vms
    MAX_CONCURRENCY --> ACTIVE_WORKING: VM Stopped / Released
    ACTIVE_WORKING --> HEALTHY: All VMs Released

    HEALTHY --> COOLDOWN_RATE_LIMIT: HTTP 429 / Abuse Throttle
    ACTIVE_WORKING --> COOLDOWN_RATE_LIMIT: HTTP 429 on New Assign
    COOLDOWN_RATE_LIMIT --> HEALTHY: Cooldown Timer Expires (Exp Backoff 2m -> 15m -> 60m)

    HEALTHY --> QUOTA_LOW_FREE_ONLY: Paid CU <= Reserve Threshold (e.g. 2.0 CU)
    QUOTA_LOW_FREE_ONLY --> HEALTHY: Monthly Renewal / Top-Up Detected

    HEALTHY --> AUTH_EXPIRED: OAuth Refresh 400/401 (invalid_grant)
    HEALTHY --> PROXY_ERROR: SOCKS5/HTTP Proxy Timeout x3
    PROXY_ERROR --> HEALTHY: Proxy Probe Recovers
    AUTH_EXPIRED --> HEALTHY: Re-Authorize via 4-Step Wizard
```

- **Per-Accelerator Entitlement Matrix (Sub-State)**: Stored per account as `gpu_capabilities`:
  - `AVAILABLE` (Green dot): Verified or eligible (`G4`, `A100`, `L4`, `T4`).
  - `TIER_RESTRICTED` (Amber slash): Returned HTTP `400` entitlement/quota block on `assign` (e.g., `H100` blocked on an account that still freely provisions `G4` 96GB and `A100`!). Automatically places *only that accelerator* on a 6-hour soft cooldown for that account while keeping `G4`/`A100` `AVAILABLE`.

---

## 4. Module 2: Magpie-Style Quota Observatory & GPU-Hour Equivalence Matrix (`/`)

### 4.1 Pool-Wide Hero Telemetry Strip
Five razor-sharp horizontal telemetry cards across the top of `/` using tabular monospace counters:

1. **总可用算力单元 (`Total Available CUs`)**:
   - Primary figure: **`1,428.42 CU`** (`≈ $142.84 USD` Pay-As-You-Go value chip, `magpie`-style).
   - Sub-line: `Across 6 Healthy Accounts · +2 Free T4 Windows Ready`.
2. **实时总燃烧率 (`Active Burn Rate`)**:
   - Primary figure: **`28.20 CU/hr`** (`≈ $2.82/hr`) with live animated waveform pulse when `> 0`.
   - Sub-line: `2× G4 (19.32) + 1× A100 (8.88) · Calibrated via ccu-info`.
3. **当前负载续航 (`Pool Depletion ETA`)**:
   - Primary figure: **`50h 39m`** (at current active burn rate; displays `∞ Idle (0.00 CU/h)` when no VMs are running).
   - Sub-line: `Earliest CU batch expiry: 118.4 CU in 14d 06h`.
4. **在线算力实例 (`Active Accelerators`)**:
   - Primary figure: **`3 VMs / 14 Slots`** (`288 GB Total VRAM Online`).
   - Sub-line: `2× RTX PRO 6000 G4 (96GB) · 1× A100-SXM4 (40GB)`.
5. **累计防漏损节省 (`Saved Idle-Waste CUs`)**:
   - Primary figure: **`+312.6 CU Saved`** (`≈ $31.26`).
   - Sub-line: `19 Auto-Teardowns (`colab run`) + 4 Zombie (`Z`) Aborts`.

### 4.2 Cloud GPU Equivalent Remaining Runtime Calculator
Directly below the Hero Strip sits ColabHive's signature visual component: the **GPU-Hour Equivalence Matrix**, featuring a segmented scope switcher (`[🌐 全池汇总 Pool Total]` | `[👤 按单账号拆解 Per-Account Breakdown]` | `[⚡ 并发分片模拟 Parallel Shard Simulator]`).

#### Calculation Formula & Dynamic `ccu-info` Calibration Engine
For any account $i$ with available paid balance $B_i$ (minus user-configured safety reserve $R_i$, default `0 CU`) and accelerator tier $g$:
$$\text{Remaining Hours}(i, g) = \begin{cases} \dfrac{\max(0, B_i - R_i)}{\hat{r}_{i,g}}, & \text{if } g \notin \text{Restricted}(i) \\ 0 \text{ (Locked)}, & \text{if } g \in \text{Restricted}(i) \end{cases}$$
$$\text{Pool Total Equivalent Hours}(g) = \sum_{i \in \text{Healthy}} \text{Remaining Hours}(i, g)$$

Where $\hat{r}_{i,g}$ is resolved by the **3-Tier Rate Calibration Hierarchy**:
1. **`[🟢 Live Calibrated]`**: Exact `burn_rate` reported by Google Colab's `ccu-info` endpoint while account $i$ is actively running hardware $g$ (or EWMA $\alpha = 0.7$ over the last 5 verified runs of $(g, \text{memory\_shape})$ on this pool).
2. **`[🔵 Pool Empirical]`**: Historical measured burn rate for $(g, \text{high\_mem})$ across any account in the user's ColabHive SQLite telemetry table.
3. **`[⚪ Reference Baseline]`**: Built-in 2026 Colab reference constants (editable in settings) before first connection:

| Hardware Tier | Specs & VRAM | CLI Flag | Reference Burn Rate | Typical Range | Optimal Workload Guidance Badge |
| :--- | :--- | :--- | :---: | :---: | :--- |
| **`G4`** | **RTX PRO 6000 Blackwell · 96 GB** (`SM 12.0`) | `--gpu G4` | **`9.66 CU/hr`** | `6.70 – 10.00` | `≥26B BF16 Models (Qwen-27B / Gemma-26B)` |
| **`H100`** | **NVIDIA H100 SXM5 · 80 GB** (`SM 9.0`) | `--gpu H100` | **`12.50 CU/hr`** | `12.50 – 18.00` | `Pro+/Ultra Gated · FP8 / Dense Training` |
| **`A100`** | **NVIDIA A100 SXM4 · 40/80 GB** (`SM 8.0`) | `--gpu A100 --high-mem` | **`8.88 CU/hr`** | `7.00 – 10.60` | `≤14B BF16 · Native Triton / causal-conv1d` |
| **`L4`** | **NVIDIA L4 Ada · 24 GB** (`SM 8.9`) | `--gpu L4` | **`4.52 CU/hr`** | `1.80 – 4.52` | `7B–8B Inference / LoRA / Diffusion` |
| **`T4`** | **NVIDIA T4 Turing · 16 GB** (`SM 7.5`) | `--gpu T4` | **`1.96 CU/hr`** | `1.10 – 1.96` | `Lightweight Eval + Free Dynamic Window` |
| **`TPU v6e`** | **Trillium TPU v6e-1 · 32 GB HBM** | `--tpu v6e1` | **`4.08 CU/hr`** | `4.00 – 4.15` | `JAX / XLA / PyTorch-XLA High Perf/CU` |
| **`TPU v5e`** | **TPU v5e-1 · 16 GB HBM** | `--tpu v5e1` | **`2.85 CU/hr`** | `2.50 – 3.30` | `Budget JAX / Embedding Extraction` |
| **`CPU`** | **Standard / High-RAM x86 VM** | *(default)* | **`0.80 CU/hr`** | `0.50 – 1.10` | `Data Prep / Orchestration (+ Free Tier)` |

- **Parallel Shard Simulator Slider (`并发分片推演`)**: Interactive slider `[1× Shard ───●─── 4× Shards ────── 8× Shards]` right inside the matrix header! Moving it to `4× G4 Shards` immediately shows: **`Wall-Clock Runtime: 36h 58m @ 38.64 CU/hr (Across 4 Accounts)`**, reinforcing the core mental model that parallel sharding multiplies research speed at zero extra CU cost.

### 4.3 Magpie-Style Account Quota Cards
Each account in the Observatory renders as a compact, high-density **Magpie Quota Card** (3-column responsive grid on desktop, 1-column on mobile; **zero circular gauges**):

```text
┌─────────────────────────────────────────────────────────────────────────┐
│ ● lab-ultra@example.com          [AI Ultra] [🇺🇸 Socks5·LAX 54ms] [⋮]   │
│   Group: research-core · Sticky: 1 Agent          [🟢 HEALTHY · 1/4 VM] │
├─────────────────────────────────────────────────────────────────────────┤
│ Paid Quota Balance                      386.40 / 500.00 CU (≈ $38.64)   │
│ [███████████████████████░░░░░░░] 77.3% Remaining  · 🔥 Live: 9.66 CU/h  │
│ Cycle resets in 18d 04h · Expiring soon: 0.0 CU                         │
├─────────────────────────────────────────────────────────────────────────┤
│ EQUIVALENT RUNTIME (Calibrated)                                         │
│ ┌──────────────┬──────────────┬──────────────┬──────────────┐           │
│ │ G4 96G       │ A100 40G     │ L4 24G       │ T4 16G       │           │
│ │ 40h 00m 🟢   │ 43h 30m 🟢   │ 85h 29m ⚪   │ 197h 08m +Free│          │
│ └──────────────┴──────────────┴──────────────┴──────────────┘           │
│ Capabilities: [G4 ✓] [A100 ✓] [L4 ✓] [T4 ✓] [v6e ✓] [H100 ⊘ HTTP 400]   │
├─────────────────────────────────────────────────────────────────────────┤
│ ▶ Active: exp_qwen27b_g4_0 (G4 · 51.2/96GB · 01:14:22 · Keepalive ✓)    │
└─────────────────────────────────────────────────────────────────────────┘
```
- **Progress Fill Threshold Semantics**:
  - `> 35% Remaining`: Phosphor Emerald (`--ch-emerald`)
  - `15% – 35% Remaining`: Voltaic Honey Amber (`--ch-amber`)
  - `< 15% Remaining` or `< 72h to Expiry`: Vermilion Alert (`--ch-danger`) with subtle left-border accent bar.

---

## 5. Module 3: Live Experiment Visualizer & Sharded Workbench (`/exp`)

### 5.1 Experiment & Parallel Shard Group View
When a user or Agent launches either a single job (`colabhive run --gpu G4 train.py`) or a multi-account sharded sweep (`colabhive run --gpu G4 --shards 4 --experiment d1_bench eval.py`), `/exp` groups all child sessions under one **Experiment Header Card**:
- **Shard Matrix Pill Strip**: Horizontal selector `[All Shards (4/4 Running)]` | `[#0 G4 · acc_01 · 68%]` | `[#1 G4 · acc_02 · 71%]` | `[#2 G4 · acc_03 · 65%]` | `[#3 G4 · acc_04 · 70%]`.
- **Aggregate Progress & Burn**: Live merged JSONL record counter (`342 / 500 samples · 4.8 samples/min`), accumulated CUs spent so far (`6.44 CU`), and estimated CUs at completion (`9.41 CU`).

### 5.2 Four-Quadrant Workbench Layout
```text
┌─────────────────────────────────────────────────────────────────────────────────────────────────┐
│ EXP: d1_round5_bench [4× G4 Shards] · Elapsed 00:19:42 · Spent 12.68 CU                         │
│ Actions: [↗ Open in Colab Web] [📥 Export .ipynb/.jsonl] [♻️ Restart PID (Keep Cache)] [🛑 Kill & Release VM] │
├──────────────────────────────────────────────────────┬──────────────────────────────────────────┤
│ LIVE HARDWARE & LIVENESS GUARD STRIP (Selected: #0)  │ EXECUTION TIMELINE & CELL WATERFALL      │
│ VRAM: [███████████░░░░░░░] 51.4 / 96.0 GB (53.5%)    │ ✓ [00:08] Assign G4 VM (acc_01 · 4.2s)   │
│ SM%:  [████████████████░░] 91%  · Temp: 64°C · 285W  │ ✓ [00:19] uv pip install -r req.txt      │
│ 🛡️ Anti-Zombie (/proc/4182/stat = R): HEALTHY        │ ✓ [00:05] Smoke-test imports + Self-Check│
│ 💓 15m Reaper Keepalive: Pulse 3s ago (Next in 5s)   │ ● [19:10] Shard #0 Worker (PID 4182)     │
│ 📦 Weight Cache (/root/.cache/hf): 54.2 GB HOT       │ ○ [Pending] Sync shard_0.jsonl & Stop VM │
├──────────────────────────────────────────────────────┼──────────────────────────────────────────┤
│ LIVE WEBSOCKET TERMINAL (xterm.js · stdout/stderr)   │ INLINE ARTIFACTS & /content EXPLORER     │
│ [Filter: All | stdout | stderr | [colab]] [🔍 Search]│ [Tab: 📊 Plots (3)] [Tab: 📁 /content]   │
│ 15:14:02 [colab] Attached kernel 9a4f... on G4       │ ┌──────────────────┬──────────────────┐  │
│ 15:14:11 [setup] Verified /proc/4182/stat != Z       │ │ loss_curve.png   │ attn_head_4.png  │  │
│ 15:18:44 Epoch 1 | Step 140/200 | loss=1.2841        │ │ [Click Lightbox] │ [Compare Shards] │  │
│ 15:19:01 Saved /content/shard_0.jsonl (86 rows) █    │ └──────────────────┴──────────────────┘  │
│                                                      │ 📁 /content/                             │
│                                                      │  ├─ 📄 shard_0.jsonl (1.8 MB · Synced ✓) │
│                                                      │  ├─ 📄 worker.log    (142 KB · Live)     │
│                                                      │  └─ 📄 DONE.flag     (waiting...)        │
└──────────────────────────────────────────────────────┴──────────────────────────────────────────┘
```

### 5.3 Critical Operational UX Protections Built Into `/exp`
1. **Anti-Zombie & Crash Guard Banner**: Polls `/proc/{pid}/stat` every `8s` inside the keepalive loop. If a background `Popen` process enters Linux `Z` (Zombie) state or prints an uncaught `Traceback` / `OutOfMemoryError` in `worker.log`, the Workbench flashes a high-priority Vermilion banner and—depending on policy—either **auto-releases the VM within 30s** (preventing hours of silent CU drain) or holds the hot cache for `5m` while alerting the Agent/User.
2. **15-Min Idle Reaper & 35–60 Min ZMQ Reconnect Telemetry**: Displays exact seconds since last ZMQ WebSocket heartbeat (`Pulse 3s ago`) and counts transparent ZMQ stream reconnections (`Reconnects: 1 · Zero Kernel State Lost`) so users trust multi-hour runs.
3. **Hot-VM Weight Cache Reuse Action (`[♻️ 仅重启进程·保留权重 / Hot-Restart Worker]`)**: Kills *only* the Python worker PID on `/content`, uploads updated code, and re-runs immediately while preserving `50–70 GB` of HuggingFace weights in `/root/.cache/huggingface`—saving 6–10 minutes of download CUs per iteration!
4. **1-Click Safe Actions**:
   - **`[↗ Open in Colab Web]`**: Calls the session `url` bridge to open Google's official Colab Notebook UI attached to the *exact same live CLI kernel* (using a reminder pill showing which Google account profile owns it).
   - **`[🛑 Kill & Release VM]`**: Triggers `sync_outputs` checkpoint download followed by guaranteed `unassign` with a 45s timeout lock, updating the account's status to `0.00 CU/hr` immediately.

---

## 6. Module 4: Agent Session Router & Control Plane UI (`/router` & `/cfg`)

### 6.1 Visual Topology & Live Route Table
At the top of `/router`, an interactive **4-Column Flow Canvas** (built with lightweight SVG Bezier links that pulse on active `exec` / MCP calls) visualizes real-time compute routing:

```text
[MCP / CLI Clients]        [Router Strategy]         [Colab Kernel Sessions]     [Account Pool & Hardware]
┌──────────────────┐                                 ┌─────────────────────┐     ┌─────────────────────────┐
│ 🤖 Antigravity   │───┐                         ┌──▶│ sess_g4_0 (Sticky)  │────▶│ hex***@gmail · G4 96GB  │
│ key: ch_sk_prod  │   │   ┌─────────────────┐   │   ├─────────────────────┤     ├─────────────────────────┤
├──────────────────┤   ├──▶│ ⚡ Waste-Zero +  │──┼──▶│ sess_g4_1 (Shard 1) │────▶│ lab02***@gmail · G4 96G │
│ 🛠️ Claude Code   │───┤   │ Sticky Escape + │   │   ├─────────────────────┤     ├─────────────────────────┤
│ key: ch_sk_dev   │   │   │ Tier Waterfall  │   └──▶│ sess_a100_0 (Sticky)│────▶│ pro03***@gmail · A100   │
└──────────────────┘   │   └─────────────────┘       └─────────────────────┘     └─────────────────────────┘
```

Below the canvas sits the **Active Sticky Bindings & Route Table** showing: `Client / Agent Name`, `Sticky Key (session_hash)`, `Pinned Kernel ID`, `Bound Account + Proxy`, `Hardware`, `Idle TTL Countdown`, and a `[Unpin / Migrate]` action.

### 6.2 Routing Policy Controls
Administrators configure routing policies globally or per API Key group via crisp toggle cards:

1. **Account Selection Strategy (`选号调度策略`)**:
   - **`Waste-Zero / Expiring-First` (Recommended Default)**: Prioritizes accounts with CUs closest to their 90-day expiration date or monthly subscription reset window, driving expired-CU waste to zero.
   - **`Max-Balance-First` (Deepest Reservoir)**: Routes long-running training or multi-shard jobs to accounts with the highest remaining CU balance to minimize mid-job account exhaustion.
   - **`Round-Robin / Even-Spread`**: Distributes concurrent VMs evenly across healthy accounts to maximize aggregate parallel GPU slots (`4–6+ GPUs` simultaneously).
   - **`Free-Tier-Opportunistic`**: Attempts Free `T4` / `CPU` windows on zero-balance accounts first for lightweight tasks before touching Paid CU accounts.
2. **Sticky-Agent-Session (`会话粘性与故障逃逸`)**:
   - Computes `sticky_key` from explicit `--session` / MCP `session_name` -> `X-ColabHive-Session-Id` -> `(api_key_id, mcp_client_session_id)`.
   - Keeps consecutive `colab_exec` / `colab_upload` calls pinned to the exact live Jupyter kernel so imported models and Python variables persist in RAM/VRAM.
   - **Sticky Escape Conditions**: Automatically breaks sticky affinity (and optionally replays setup script on a fresh account) ONLY when: (a) account hits `QUOTA_EXHAUSTED`, (b) VM is reaped by Google backend, or (c) `assign` hits `COOLDOWN_RATE_LIMIT`.
3. **Auto-GPU-Tier-Fallback Waterfall (`硬件降级瀑布流`)**:
   - Visual drag-to-reorder fallback chain when Colab's `assign` returns HTTP `400` (capacity or tier restriction):
   - Preset chains:
     - *Large Model (`≥26B` VRAM-Strict)*: `H100 (80G) ➔ G4 (96G) ➔ [Fail Fast — Do NOT silently drop to 40G OOM!]`
     - *General Compute / Throughput*: `H100 ➔ G4 ➔ A100 (--high-mem) ➔ L4 ➔ T4`
   - Note: Directly fixes the notorious `colab` CLI gotcha where unrecognized or unavailable flags behave unpredictably!

### 6.3 Live MCP & REST Call Trace Inspector
A collapsible bottom/side drawer in `/router` streaming every incoming Agent tool call in real time:
- Columns: `Timestamp`, `Caller (Antigravity / CLI)`, `Transport (MCP-SSE / MCP-stdio / CLI)`, `Tool / Endpoint` (`colab_exec`, `colab_run_sharded`, `colab_usage`), `Target Account & GPU`, `Duration`, `CU Delta`, `Status (200 OK / 400 Fallback -> G4)`.
- Clicking any row expands the exact JSON-RPC request arguments, truncated `stdout`/`stderr` return payload, intercepted plot thumbnails, and fallback decision trace.

### 6.4 Scoped Agent API Key Management (`ch_sk_...`)
Modeled after `sub2api`'s multi-tenant key governance (`/cfg`):
- Key format: `ch_sk_live_<24_char_base62>` (masked after creation, 1-click copy).
- Per-key guardrails:
  - **Allowed Account Groups (`group_ids`)**: Restrict an experimental agent to `["sandbox-accounts"]` while keeping `["flagship-ultra"]` for primary research.
  - **CU Budget Hard Cap & Window**: Total max CUs (e.g., `50.0 CU` max) and Daily CU burn cap (`20.0 CU/day`).
  - **GPU Tier Allowlist & Max Concurrent VMs**: e.g., allow `T4, L4, A100, G4`, max `4` concurrent VMs, auto-kill idle VMs after `20m`.

### 6.5 Global 1-Click Agent Connect Drawer (`[⚡ 接入 Agent]`)
Slide-over drawer (`width: 520px`) accessible from the top bar everywhere, combining `magpie` & `cc-switch` (`farion1231/cc-switch`) zero-friction ergonomics. User selects an API Key (`ch_sk_...`) + Transport (`Local stdio` vs `Remote Streamable HTTP / SSE`):
- **Local Host Auto-Inject Mode (`cc-switch` Pattern)**: When ColabHive runs on `127.0.0.1` / WSL2, each agent tab detects whether the target CLI's config file exists on disk, displays a `[⚡ 一键注入配置 / 1-Click Inject]` toggle alongside `[↺ 还原备份 / Restore Backup]` (creating an atomic timestamped `.bak` snapshot under `~/.colabhive/agent-config-backups/` before writing), plus a `[📋 Copy Snippet]` button for remote servers:

1. **Antigravity (`mcp_config.json`)**:
   ```json
   {
     "mcpServers": {
       "colabhive": {
         "serverUrl": "http://127.0.0.1:7860/mcp",
         "headers": {
           "Authorization": "Bearer ch_sk_live_xxxxxxxxxxxxxxxx"
         }
       }
     }
   }
   ```
2. **Claude Code (`~/.claude.json` / CLI 1-Liner)**:
   ```bash
   claude mcp add --transport http colabhive http://127.0.0.1:7860/mcp \
     --header "Authorization: Bearer ch_sk_live_xxxxxxxxxxxxxxxx"
   ```
3. **Cursor (`~/.cursor/mcp.json` / `.cursor/mcp.json`)**: Ready-to-copy MCP JSON block + `cursor://anysphere.cursor-deeplink/mcp/install?...` 1-click install button.
4. **Codex (`~/.codex/config.toml`)**:
   ```toml
   [mcp_servers.colabhive]
   url = "http://127.0.0.1:7860/mcp"
   bearer_token_env_var = "COLABHIVE_API_KEY"
   ```
5. **Gemini CLI (`~/.gemini/settings.json`)**:
   ```json
   {
     "mcpServers": {
       "colabhive": {
         "httpUrl": "http://127.0.0.1:7860/mcp",
         "headers": { "Authorization": "Bearer ch_sk_live_xxxxxxxxxxxxxxxx" }
       }
     }
   }
   ```
6. **Drop-In CLI (`colab` Compatible) & Python SDK**:
   ```bash
   export COLABHIVE_URL="http://127.0.0.1:7860"
   export COLABHIVE_API_KEY="ch_sk_live_xxxxxxxxxxxxxxxx"
   # 100% compatible with `colab` CLI + multi-account sharding flags:
   colabhive run --gpu G4 --shards 4 --strategy waste-zero eval_27b.py
   ```
   ```python
   from colabhive import HivePool

   pool = HivePool(base_url="http://127.0.0.1:7860", api_key="ch_sk_live_xxx")
   results = pool.map_shards("eval_shard.py", shards=4, gpu="G4")
   ```

---

## 7. Module 5: Visual Design System ("Precision Compute Observatory")

### 7.1 Aesthetic Philosophy & Anti-Slop Mandate
Following strict high-craft frontend engineering principles:
- **Zero Generic AI Aesthetics**: Strictly forbid `Inter`, `Roboto`, `Arial`, purple-to-indigo SaaS gradients, blurry neon glow blobs, and circular speedometer gauges.
- **Conceptual Anchor**: **Precision Compute Observatory** — inspired by Swiss railway/CERN telemetry consoles, Teenage Engineering hardware legends, and Google Colab's warm amber heritage elevated into a dark obsidian instrument panel.
- **Signature Visual Details**:
  1. **Architectural Hairline Grid**: `1px` crisp borders (`--ch-border`) with `5px × 5px` `+` crosshair ticks at major container intersections.
  2. **Subtle Honeycomb Micro-Matrix**: Background features a barely perceptible `24px` hexagonal/radial dot pattern (`opacity: 0.035`) layered beneath a `2%` monochromatic SVG film grain—evoking the "Hive" without cartoon bee illustrations.
  3. **Tabular Phosphor Numerals**: Every CU balance, `CU/hr` burn rate, countdown timer, and GPU percentage uses strict monospace `font-variant-numeric: tabular-nums slashed-zero` so live-updating numbers never jitter horizontally.

### 7.2 Color Palette & Exact CSS Token Specification

```css
/* Dark Theme (Default): "Obsidian Telemetry" */
:root,
[data-theme="dark"] {
  --ch-bg-canvas:        #090B0E; /* Deepest obsidian chassis */
  --ch-bg-surface-1:     #10141B; /* Primary card & sidebar surface */
  --ch-bg-surface-2:     #161C26; /* Elevated modal / hover / sub-tile */
  --ch-bg-terminal:      #06080A; /* Pure contrast xterm.js well */

  --ch-border-subtle:    #1E2633; /* Hairline structural grid */
  --ch-border-strong:    #2D384B; /* Active card & input border */
  --ch-crosshair:        #3B4961; /* Corner + registration marks */

  --ch-text-primary:     #F1F5F9; /* Crisp instrument white */
  --ch-text-secondary:   #94A3B8; /* Muted slate labels */
  --ch-text-muted:       #64748B; /* Metadata & timestamps */

  /* Brand & Telemetry Accents */
  --ch-amber:            #F59E0B; /* Voltaic Honey Amber — Brand Primary & Active Burn */
  --ch-amber-glow:       rgba(245, 158, 11, 0.14);
  --ch-emerald:          #10B981; /* Phosphor Emerald — Healthy Quota / Calibrated / Live Pulse */
  --ch-cyan:             #06B6D4; /* Cryo Cyan — Flagship 80G/96G Hardware (G4 / H100) */
  --ch-cobalt:           #3B82F6; /* Precision Cobalt — Agent Sticky Links & A100 Tier */
  --ch-danger:           #EF4444; /* Vermilion Alert — Zombie Process / Auth Expired / <15% CU */

  /* Hardware Tier Pill Accents */
  --ch-tier-g4:          #06B6D4; /* Blackwell 96GB */
  --ch-tier-h100:        #F59E0B; /* Hopper 80GB */
  --ch-tier-a100:        #3B82F6; /* Ampere 40/80GB */
  --ch-tier-l4:          #10B981; /* Ada 24GB */
  --ch-tier-t4:          #8B9BB4; /* Turing 16GB / Free */
  --ch-tier-tpu:         #EC4899; /* TPU v6e / v5e Coral-Magenta */
}

/* Light Theme: "Drafting Alabaster" (Warm Editorial Engineering Paper) */
[data-theme="light"] {
  --ch-bg-canvas:        #F5F4EF; /* Warm unbleached drafting paper */
  --ch-bg-surface-1:     #FFFFFF; /* Pure crisp card surface */
  --ch-bg-surface-2:     #ECEAE2; /* Tactile recessed control surface */
  --ch-bg-terminal:      #11151C; /* Terminal stays dark for ANSI legibility */

  --ch-border-subtle:    #DFDDD4;
  --ch-border-strong:    #C5C2B6;
  --ch-crosshair:        #9E9B8E;

  --ch-text-primary:     #11161F;
  --ch-text-secondary:   #4B5563;
  --ch-text-muted:       #6B7280;

  --ch-amber:            #D97706; /* Deepened amber for WCAG AAA contrast on light */
  --ch-amber-glow:       rgba(217, 119, 6, 0.10);
  --ch-emerald:          #059669;
  --ch-cyan:             #0891B2;
  --ch-cobalt:           #2563EB;
  --ch-danger:           #DC2626;
}
```

### 7.3 Typography System (Bilingual `zh-CN` + `en-US` Precision Pairing)
- **Display & Section Headings (`--font-display`)**:
  - `'Bricolage Grotesque', 'PingFang SC', 'HarmonyOS Sans SC', 'Noto Sans SC', sans-serif`
  - Weight `600–700`, letter-spacing `-0.02em` (`0em` when `[lang="zh-CN"]` for clean CJK kerning). Gives headings an unmistakable technical-editorial authority.
- **UI Body & Controls (`--font-sans`)**:
  - `'Plus Jakarta Sans', 'PingFang SC', 'Microsoft YaHei UI', 'Noto Sans SC', sans-serif`
  - Weight `400 / 500 / 600`, `13.5px` base density for dashboard tables and cards (`line-height: 1.5`).
- **Telemetry Numerals, Terminal & Code (`--font-mono`)**:
  - `'JetBrains Mono', 'Berkeley Mono', 'Cascadia Mono', monospace`
  - Mandatory CSS utility `.ch-num`: `font-family: var(--font-mono); font-variant-numeric: tabular-nums slashed-zero; letter-spacing: -0.03em;`.
- **Type Scale**:
  - Hero KPI Counter: `28px / 32px` (`--font-mono`, weight `700`)
  - Page Title: `20px / 28px` (`--font-display`, weight `600`)
  - Card Title / Account Email: `14px / 20px` (`--font-mono` for email, `--font-sans` `600` for title)
  - Dense Table & Progress Meta: `12px / 16px` (`--font-mono` for numbers, `--font-sans` for labels)
  - Micro Badges / Axis Ticks: `11px / 14px` (uppercase in `en-US` with `0.05em` tracking)

### 7.4 Motion, Spacing & Responsive Rules
- **Corner Radii**: Sharp architectural radii — `6px` for primary cards, `4px` for buttons/inputs/progress bars, `2px` for hardware tier tags (never bubbly `24px` pills).
- **Orchestrated Boot Choreography**: On initial `/` load, Hero KPI cards reveal with a crisp `180ms` `cubic-bezier(0.16, 1, 0.3, 1)` translate-Y (`6px -> 0`) staggered by `35ms`, while Magpie progress bars fill smoothly over `420ms`. Subsequent WebSocket updates mutate numbers in-place with zero re-mount flicker.
- **Live Burn Pulse**: Any card with `burn_rate > 0` renders a `6px` emerald/amber dot using a subtle CSS dual-ring keyframe (`2.4s` cycle), pausing immediately when idle (`0.00 CU/hr`).
- **Responsive Breakpoints**:
  - `≥1440px`: Left sidebar (`220px`) + 5-col Hero Strip + 3-col Account Card Grid.
  - `1024px–1439px`: Collapsible icon rail (`64px`) + 3-col Hero + 2-col Account Grid.
  - `<768px` (Mobile On-the-Go Monitoring): Bottom tab bar (`Observatory`, `Pool`, `Workbench`, `Router`) + swipeable Hero strip + 1-col cards + 1-tap `[🛑 Kill & Release VM]` emergency brake for checking experiments from a phone!

### 7.5 Bilingual (`zh-CN` Default + `en-US` Toggle) i18n Specification
- **Implementation**: Lightweight compile-time typed dictionary (`src/i18n/locales/{zh-CN,en-US}.ts`) backed by `localStorage.getItem('ch_locale') || 'zh-CN'`, updating `<html lang="zh-CN">` synchronously with zero network fetch and zero page reload.
- **Canonical Domain Glossary (Mandatory Consistency)**:

| Key | `zh-CN` (Default) | `en-US` |
| :--- | :--- | :--- |
| `hero.total_cu` | 总可用算力单元 (CU) | Total Available CUs |
| `hero.burn_rate` | 实时燃烧率 | Active Burn Rate |
| `hero.depletion_eta` | 当前负载预计续航 | Pool Depletion ETA |
| `hero.saved_waste` | 防空转累计节省 | Saved Idle-Waste CUs |
| `matrix.title` | 云端 GPU 等效剩余可用时长 | Cloud GPU Equivalent Remaining Runtime |
| `matrix.calibrated` | 实测动态校准 | Live `ccu-info` Calibrated |
| `pool.oauth_wizard` | OAuth 授权码向导（支持远程/无头部署） | OAuth Auth-Code Wizard (Headless-Safe) |
| `pool.auto_detect_cli` | 一键扫描本机 / WSL2 `colab` 凭证 | 1-Click Local / WSL2 `colab` CLI Import |
| `router.waste_zero` | 零浪费优先（临期额度先用） | Waste-Zero (Expiring-First) |
| `router.sticky` | 会话粘性绑定（保留内核与权重缓存） | Sticky Kernel Affinity (Preserve RAM & Cache) |
| `exp.anti_zombie` | 防僵尸进程守护 (`/proc/stat`) | Anti-Zombie Watchdog (`/proc/stat`) |
| `exp.hot_restart` | 仅重启任务进程（保留 HF 权重缓存） | Hot-Restart Worker PID (Keep HF Cache) |
| `nav.privacy_mask` | 晒图脱敏 | Privacy Mask |

---

## 8. Viral Open-Source Blueprint (GitHub Star-Magnet Packaging)

To achieve immediate traction across GitHub Trending, X/Twitter, Reddit (`r/LocalLLaMA`, `r/MachineLearning`), Linux.do, and V2EX—following the exact growth playbook of `Wei-Shaw/sub2api` and `yetone/magpie`—ColabHive's repository presentation and onboarding must meet the following specifications:

### 8.1 Above-the-Fold `README.md` & `README.zh-CN.md` Blueprint
1. **Header Block**:
   - Crisp geometric SVG emblem (amber hexagonal aperture + terminal prompt) + Bilingual switcher (`简体中文 | English`).
   - **One-Sentence Hook**: *"Pool multiple Google Colab accounts into one self-hosted GPU observatory, parallel shard runner, and zero-leak MCP/CLI router for AI Agents — inspired by `sub2api` & `magpie`."*
   - Status badges: Docker Pulls, Release Version, MIT/Apache-2.0 License, MCP Ready (`Antigravity · Claude Code · Cursor · Codex`), `zh-CN / en-US`.
2. **8-Second Hero Showcase GIF / Interactive SVG**:
   - Frame 1 (`0–3s`): Observatory showing `1,428 CU` converted live into **`147h of G4 96GB`** / **`160h of A100`** across 6 accounts with `[👁️ Privacy Mask]` toggled on.
   - Frame 2 (`3–5.5s`): Launching `colabhive run --gpu G4 --shards 4 eval.py` and watching 4 accounts light up in parallel inside the Live Workbench.
   - Frame 3 (`5.5–8s`): Clicking `[⚡ Connect Agent]` -> 1-click injecting MCP into Antigravity / Claude Code.
3. **"Why ColabHive?" Comparison Table**:
   - Compares **Browser Colab Tabs** vs **Single-Account `colab-cli`** vs **ColabHive** across: Multi-Account CU Pooling, GPU-Hour Equivalence & Expiry Alerts, Parallel Cross-Account Sharding (`4×–6×` speedup), Per-Account SOCKS5/HTTP Proxy Isolation, 15m Reaper + Anti-Zombie (`Z`) Guard, `H100 -> G4 -> A100` Auto-Fallback, and Multi-Agent MCP Gateway (`ch_sk_...`).

### 8.2 Zero-Friction 30-Second Quickstarts
Zero external database required out of the box (embedded SQLite in `/data/colabhive.db` with AES-256-GCM encrypted credential column; optional `DATABASE_URL=postgres://...` for shared lab deployments):

```bash
# Option A: 1-Command Docker Quickstart (Recommended for VPS / Homelab / Desktop)
docker run -d \
  --name colabhive \
  -p 7860:7860 \
  -v colabhive_data:/data \
  -e COLABHIVE_SECRET_KEY="$(openssl rand -hex 32)" \
  ghcr.io/colabhive/colabhive:latest

# Option B: Zero-Docker Local Launch (Auto-detects local/WSL2 colab CLI keyring!)
uvx colabhive serve --port 7860 --open
```

### 8.3 Built-In Community Virality Loops
1. **1-Click "Share Quota Snapshot" (`生成算力池晒图卡片`)**:
   - Clicking `[👁️ 晒图脱敏]` -> `[📸 Export PNG Card]` renders a high-DPI (`2x`) dark-obsidian summary card displaying *only* anonymized stats (`6 Pooled Accounts · 1,428.4 CU · ≈ 147.8 hrs RTX PRO 6000 96GB · +312.6 Idle CUs Saved`) with a subtle `colabhive` GitHub badge—purpose-built for sharing on social feeds without ever leaking an email or IP.
2. **Security-First Trust Signals**:
   - Prominent documentation of **Local-First Credential Vault** (tokens never leave user's server except directly to `*.googleapis.com` / `*.google.com` via the account's pinned proxy), read-only local keyring import, and deterministic `trap` VM teardown guarantees.

---

## 9. Frontend Component & API Contract Checklist (Implementation Handoff)

| UI Module | Primary Frontend Components | Required Control-Plane Endpoints / Streams |
| :--- | :--- | :--- |
| **Observatory (`/`)** | `HeroTelemetryStrip`, `GpuEquivalenceMatrix`, `ShardSimulatorSlider`, `MagpieAccountCard`, `PrivacyMaskProvider` | `GET /api/v1/pool/summary`, `GET /api/v1/accounts`, `WS /api/v1/ws/telemetry` (push every `5s` or on state transition) |
| **Onboarding (`/pool`)** | `OAuthWizardModal`, `ProxyTestPill`, `CredentialMigrationModal` (`KeyringAutoScanTab`, `JsonDropTab`, `VaultBundleTab`), `AccountStateBadge` | `POST /api/v1/proxies/test`, `POST /api/v1/accounts/oauth/{start,exchange}`, `GET /api/v1/import/scan-local`, `POST /api/v1/pool/{import,export}` |
| **Workbench (`/exp`)** | `ShardGroupHeader`, `HardwareLivenessBar`, `XtermLogStream`, `ExecutionWaterfall`, `ArtifactLightbox`, `RemoteFileTree` | `GET /api/v1/experiments/:id`, `WS /api/v1/ws/sessions/:id/stream`, `POST /api/v1/sessions/:id/{open-url,hot-restart,stop}`, `GET/POST /api/v1/sessions/:id/files` |
| **Router (`/router`, `/cfg`)** | `RouteTopologyCanvas`, `StickySessionTable`, `PolicyConfigPanel`, `FallbackChainSortable`, `McpTraceDrawer`, `ApiKeyModal`, `AgentConnectDrawer` | `GET/PUT /api/v1/router/policy`, `DELETE /api/v1/router/sticky/:hash`, `WS /api/v1/ws/traces`, `CRUD /api/v1/keys`, `POST /mcp` (Streamable HTTP) & `GET /mcp/sse` |
