# ColabHive v1.0 — Open-Source Launch Playbook

Ready-to-publish launch kit for [`https://github.com/hexwander/colabhive`](https://github.com/hexwander/colabhive), written in a direct engineering voice tailored to each community's norms.

---

## 1. 3-Minute GitHub Repository Setup Checklist (Do This First)

Before posting anywhere, complete these 5 repository steps so every inbound visitor sees the live sandbox, OpenGraph card, and prebuilt release binaries:

1. **Upload Social Preview Card (`1280×640`)**
   - Go to **Settings -> General -> Social preview -> Edit -> Upload an image...**
   - Upload [`docs/assets/social-preview-og.png`](./assets/social-preview-og.png).
   - Verify link unfurling on [opengraph.xyz](https://www.opengraph.xyz/) or X Card Validator (`summary_large_image`).
2. **Enable GitHub Pages via GitHub Actions**
   - Go to **Settings -> Pages -> Build and deployment -> Source** and select **GitHub Actions**.
   - `.github/workflows/pages.yml` automatically builds `web/dist-pages` (`VITE_DEMO_SITE=true`) and deploys the zero-backend interactive sandbox to:
     **`https://hexwander.github.io/colabhive/`**
3. **Set Repository Description, Website & Topics**
   - **About -> Description**:
     `Pool multiple Google Colab accounts into one self-hosted GPU observatory, parallel shard runner, and headless MCP router — Windows system-tray app + hardened Cloud server.`
   - **About -> Website**: `https://hexwander.github.io/colabhive/`
   - **About -> Topics** (copy-paste all):
     `google-colab` `mcp` `mcp-server` `gpu-cluster` `claude-code` `codex` `cursor` `ai-agents` `golang` `system-tray` `llm-evaluation` `self-hosted` `cuda` `jupyter`
4. **Enable GitHub Discussions**
   - Go to **Settings -> General -> Features -> Check "Discussions"** so `.github/ISSUE_TEMPLATE/config.yml` routes Q&A cleanly away from bug trackers.
5. **Tag & Cut `v1.0.0` Release**
   ```bash
   git tag -a v1.0.0 -m "ColabHive v1.0.0 — Multi-Account Colab GPU Pool, Native System Tray & Headless MCP Router"
   git push origin main --tags
   ```
   Wait ~3 minutes for `.github/workflows/ci.yml` to publish `colabhive_v1.0.0_windows_amd64.zip` (containing `colabhive-desktop.exe`, `colabhive.exe`, `colab.exe`, and `web/dist`), Linux/macOS tarballs, and `SHA256SUMS.txt` so the 1-line `install.ps1` / `install.sh` scripts resolve `/releases/latest` immediately.

---

## 2. Hacker News (`Show HN`)

- **Best Timing**: Tuesday–Thursday, 08:30–10:00 US Eastern (12:30–14:00 UTC).
- **Submission URL**: `https://github.com/hexwander/colabhive` *(or link the repo and put the live GitHub Pages sandbox in the first line of text/comment)*
- **Title** (`79` chars, under HN's `80`-char limit):
  ```text
  Show HN: ColabHive – Pool Google Colab accounts into one MCP GPU router & tray
  ```

### First Author Comment (Post Immediately After Submitting)

```text
Hi HN — I built ColabHive after burning through way too many paid Google Colab Compute Units (CUs) while running LLM evals and AI coding agents across multiple accounts.

If you have two or three Google accounts (e.g., an AI Ultra 2,500 CU plan, a Pro+ subscription, or labmates with 90-day expiring CUs), Google's official tooling fights you at every step:
- `google-colab-cli` hardcodes `supportsMultipleAccounts: false`.
- `colab-mcp` requires a live Chrome tab, locks to a single client (`1013 Server is busy`), and suffers from a dynamic `list_changed` timing quirk where MCP clients miss tools at startup.
- Worst of all: if your training/eval subprocess crashes into a Linux zombie (`Z`) state inside the Colab VM, `os.kill(pid, 0)` still returns true—so an idle RTX PRO 6000 Blackwell (G4, 96 GB VRAM) sits there silently draining 9.66 CU/hr (~$1/hr) until the 24h timeout.

ColabHive is a single static Go binary (`CGO_ENABLED=0`, pure-Go SQLite + AES-256-GCM vault) that pools multiple Colab accounts into one control plane:

1. Parallel sharding at identical CU cost: `4 GPUs x 1 hour == 1 GPU x 4 hours` in Compute Units. `colabhive run --gpu G4 --shards 4 eval.py` provisions 4 VMs across separate accounts with atomic slot reservation and merges the outputs.
2. Headless tri-protocol MCP (`/mcp` Streamable HTTP, SSE, and `stdio`): works concurrently across Claude Code, Codex, Cursor, Antigravity, Gemini CLI, Windsurf, and OpenCode, with cross-account failover on HTTP 400 GPU stockouts, 412 slot limits, or mid-run preemption.
3. Anti-zombie & hot-cache watchdog: polls `/proc/{pid}/stat` every 8s over ZMQ/WebSocket to catch `Z` states and unassigns VMs deterministically on exit—or hot-restarts just the worker PID so 60 GB of HuggingFace weights stay warm in `/root/.cache`.
4. Safe teammate CU sharing (`/contribute/:code`): labmates can donate expiring CUs in 30 seconds via Cloud SDK remote PKCE using *only* the `colaboratory` OAuth scope (zero Google Drive scope), plus a WebSocket proxy firewall that blocks in-kernel `google.colab.drive.mount` calls and enforces a hard `min_reserve_cu` floor.
5. Dual deployment: ships as a frameless native desktop + system-tray / menu-bar app on Windows (`colabhive-desktop.exe`) and macOS Apple Silicon / Intel (`ColabHive.app`, 4-tab flyout inspired by Magpie + 1-click config sync inspired by cc-switch), or as a hardened `--mode cloud` Docker stack behind Caddy/Nginx.

You can click around the live interactive console + system-tray sandbox in the browser with zero install here:
https://hexwander.github.io/colabhive/

Code (Apache-2.0) and protocol docs (`v1beta` + `/tun/m` dual backend, ZMQ wire format, keyring compatibility):
https://github.com/hexwander/colabhive

Happy to answer questions about Google's Colab runtime protocol, MCP streaming pitfalls behind reverse proxies, or pure-Go WebView2/tray packaging!
```

---

## 3. X / Twitter Launch Thread (5-Post Thread + Media Pairings)

### Post 1/5 — The Hook + Hero Studio Visual
- **Attach Media**: `docs/assets/hero-showcase.png` (`1600×940`)

```text
Meet ColabHive v1.0 🐝

Turn scattered Google Colab accounts (AI Ultra, Pro+, Pro & expiring lab CUs) into one self-hosted GPU cluster, 4x parallel shard runner, and headless MCP router for AI coding agents.

Ships as a frameless Windows System-Tray + macOS Menu Bar app + Docker Cloud Server.

100% open-source (Apache-2.0):
https://github.com/hexwander/colabhive
```

### Post 2/5 — The Native 4-Tab Magpie System Tray GIF
- **Attach Media**: `docs/assets/tray-demo.gif`

```text
Why juggle browser profiles or bleed $1/hr on crashed Colab notebooks?

`colabhive-desktop.exe` lives quietly in your system tray with 4 instant tabs:
• Quota: Live CU -> G4 (96GB) / H100 / A100 hours
• Agent: 1-click MCP toggles for Claude Code, Codex, Cursor, Antigravity & more
• Instances: Live VRAM/GPU% + 1-click attach in Colab Web
• Router: Real-time MCP tool trace feed
```

### Post 3/5 — 4× Parallel GPU Sharding & Anti-Zombie Engineering
- **Attach Media**: `docs/assets/bento-features.png` (`1600×1060`)

```text
Simple Colab math most people miss:
Running 4x G4 (96GB Blackwell) GPUs across 4 accounts for 1 hour costs the EXACT same Compute Units as 1 GPU for 4 hours—at 4x wall-clock speed.

ColabHive handles atomic slot locking, sticky per-account SOCKS5/HTTP proxies, cross-account 400/429 failover, and `/proc/stat` zombie (`Z`) reaping so you never leak paid CUs.
```

### Post 4/5 — 30-Second Zero-Drive Teammate CU Sharing
- **Attach Media**: `docs/assets/screen-contribute.png`

```text
Got labmates with 90-day Colab Pro+ CUs about to expire?

Send them a `/contribute/:code` link:
1. 30s remote PKCE auth — requests `colaboratory` scope ONLY (zero Google Drive access)
2. Kernel WebSocket firewall blocks `drive.mount` inside VMs
3. Hard `min_reserve_cu` floor ("never spend below 80 CU") + 1-click self-revoke
```

### Post 5/5 — Live Zero-Install Browser Demo & 1-Line Install
- **Attach Media**: *(None — let GitHub Pages / Repo OpenGraph card `docs/assets/social-preview-og.png` unfurl cleanly!)*

```text
Try the interactive console & live 4-tab system tray right in your browser (no login, no install):
👉 https://hexwander.github.io/colabhive/

Or install in 10 seconds:
Windows (Tray + CLI):
irm https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.ps1 | iex

macOS / Linux:
curl -fsSL https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.sh | sh

Repo & Colab wire-protocol specs:
⭐ https://github.com/hexwander/colabhive
```

---

## 4. Reddit (`r/LocalLLaMA` & `r/MachineLearning`)

### `r/LocalLLaMA` Post (Practical VRAM / Eval / Agent Focus)

- **Flair**: `Resources` or `News`
- **Attach Media**: Gallery of `docs/assets/hero-showcase.png`, `docs/assets/tray-demo.gif`, `docs/assets/bento-features.png`, `docs/assets/screen-workbench.png`
- **Title**:
  ```text
  [Open Source] ColabHive: Pool multiple Google Colab accounts (96GB Blackwell G4 / A100 / H100) into one multi-GPU shard runner, system tray observatory & headless MCP server
  ```
- **Body**:
  ```markdown
  If you use Google Colab for `27B–70B` BF16 evals (especially now that Colab has **96 GB GDDR7 RTX PRO 6000 Blackwell `G4`** instances at `~9.66 CU/hr`), you've probably hit the single-account wall in Google's official `colab-cli` and `colab-mcp`, or lost CUs when a Python worker segfaulted and left the VM running idle.

  I open-sourced **ColabHive** (`Apache-2.0`, single static Go binary) to pool all my personal + lab Colab accounts into a single local tray app / team server:

  - **Live Browser Sandbox (No Install)**: https://hexwander.github.io/colabhive/
  - **GitHub Repo**: https://github.com/hexwander/colabhive

  ### What it actually solves in day-to-day LLM work:
  1. **True K-Way Sharding at 1x CU Cost**: `colabhive run --gpu G4 --shards 4 eval_mmlu.py` spins up 4× 96GB G4 VMs across 4 pooled accounts in parallel (`384 GB` aggregate VRAM), streams logs over WebSockets, and auto-unassigns each VM the second its shard finishes.
  2. **Hot-Restart Without Re-Downloading 60GB HF Weights**: When tweaking an eval script, ColabHive can kill & restart just the worker PID while keeping `/root/.cache/huggingface` hot in VM RAM/disk.
  3. **`/proc/{pid}/stat` Anti-Zombie Reaper**: Standard `os.kill(pid, 0)` checks fail to detect Linux `Z` (defunct) processes, which is why crashed background runs silently drain CUs. ColabHive inspects `/proc/{pid}/stat` every 8s and tears down dead sessions automatically.
  4. **Headless Multi-Agent MCP Router**: Connects Claude Code, Codex, Cursor, Antigravity, Windsurf, and OpenCode simultaneously over Streamable HTTP (`/mcp`), SSE, or `stdio` — no Chrome tab required, no `1013 Server is busy` lock, and automatic cross-account failover when one account hits an H100/G4 stockout (`HTTP 400`) or rate limit.
  5. **Zero-Drive Labmate CU Pooling**: Generate an invite link (`/contribute/:code`) so friends with expiring 90-day CUs can share compute with `colaboratory`-only OAuth scope (physically zero Google Drive access), a `min_reserve_cu` floor, and 1-click self-revoke.

  Runs locally on Windows (`colabhive-desktop.exe` system tray), macOS/Linux (`colabhive serve`), or on a VPS (`docker compose --profile caddy up -d`). Feedback and PRs welcome!
  ```

### `r/MachineLearning` `[P]` Post (Concise Research Infrastructure Tone)

- **Title**:
  ```text
  [P] ColabHive: Self-hosted multi-account Google Colab scheduler, K-way benchmark shard runner, and zero-Drive lab CU sharing server
  ```
- **Body**:
  ```markdown
  Sharing **ColabHive**, an Apache-2.0 infrastructure tool we built to turn fragmented academic Google Colab subscriptions (AI Ultra, Pro+, Pro, and expiring 90-day Compute Unit batches) into a deterministic, shared lab evaluation pool.

  - **Repository & Protocol Specs**: https://github.com/hexwander/colabhive
  - **Interactive Demo**: https://hexwander.github.io/colabhive/

  **Technical highlights:**
  - **Waste-Zero Scheduling & Calibrated GPU-Hour Telemetry**: Polls `ccu-info` and `v1/user-info` to prioritize expiring CU batches first (`waste-zero`) and calibrates live EWMA burn rates into concrete remaining hours for `G4 (96GB)`, `H100 (80GB)`, `A100`, `L4`, `T4`, and `TPU v6e`.
  - **Least-Privilege Contributor Isolation**: Lab members contribute idle quota via Cloud SDK remote PKCE requesting exclusively `https://www.googleapis.com/auth/colaboratory` (no `drive` or `cloud-platform` scopes), backed by a WebSocket kernel frame firewall that drops `colab_request` credential propagation attempts and enforces per-contributor `min_reserve_cu` guardrails.
  - **Drop-In CLI, Python 3.10+ Zero-Dep SDK & MCP**: 100% compatible `colab run` stdout semantics, context-managed `with client.session(gpu="G4"):` auto-unassignment, and multi-tenant `ch_sk_...` API keys with per-researcher CU budgets in `--mode cloud`.
  ```

---

## 5. 中文技术社区首发帖（Linux.do / V2EX `创造`节点 / 知乎）

### A. Linux.do & V2EX（`/go/create` 分享创造）首发帖

**标题候选（任选其一）：**
- `开源了 ColabHive：把多个 Google Colab 账号汇成私人多卡 GPU 算力池与 AI Agent 路由中枢（附 Windows 原生托盘端 + 在线试玩）`
- `[开源创造] ColabHive v1.0：多账号 Colab 96GB G4/A100 算力池化、4 卡并发分片与免密 MCP 路由网关`

**正文（直接复制发布，配图顺序：`hero-showcase.png` -> `tray-demo.gif` -> `bento-features.png` -> `screen-contribute.png`）：**

```markdown
大家好，分享一个打磨了很久的开源工具 **ColabHive**（Apache-2.0 协议，纯 Go 单文件静态编译 + React 观测台 + Windows 原生系统托盘端）。

- 🎮 **免安装在线交互试玩（含实机 4 栏托盘可点击体验）**：https://hexwander.github.io/colabhive/
- ⭐ **GitHub 仓库**：https://github.com/hexwander/colabhive

### 为什么要做 ColabHive？

平时跑大模型评测、微调或者挂 Claude Code / Codex / Cursor / Antigravity 写代码跑 GPU 实验时，手里往往不止一个 Google 账号（比如 2500 CU 的 AI Ultra、500 CU 的 Pro+、几个免费号，或者同门手里快满 90 天过期作废的闲置 CU）。但官方工具链用起来有几个非常痛的硬伤：

1. **官方 `colab-cli` 硬编码锁死单账号**（`supportsMultipleAccounts: false`），多账号只能手动来回切配置；
2. **官方 `colab-mcp` 必须弹窗依赖 Chrome 浏览器**，且同一时间只允许单客户端连接（报 `1013 Server is busy`），还因为 `list_changed` 动态通知机制导致很多 IDE 启动时扫不到工具；
3. **后台崩溃静默烧干付费额度**：Python 训练子进程一旦崩成 Linux 僵尸态（`Z`），常规 `os.kill(pid, 0)` 依然返回存活，一块 `96GB` 显存的 Blackwell `G4` 就这样空转一晚上白烧上百 CU。

### ColabHive 核心特性

1. **🐝 原生无边框桌面端（Windows 系统托盘 `colabhive-desktop.exe` + macOS Apple Silicon 菜单栏 `ColabHive.app`）**
   - 致敬 `Magpie` 与 `cc-switch` 的原生轻量交互，Windows 无黑框常驻系统托盘、macOS 原生驻留顶部菜单栏（`NSPopover`），点开即看四栏面板（**额度 · Agent · 实例 · 路由**）。
   - 将抽象的 CU 实时换算成 **G4 (96GB) / H100 / A100 / L4 / T4 / TPU v6e** 真实可跑小时数，支持 `waste-zero` 临期额度优先消耗。
2. **⚡ 一键外科手术式接入 8 大 AI 编程工具**
   - 本机模式免密开箱即用（`http://127.0.0.1:7860/mcp`），一键开关自动备份并无损写入 **Claude Code、Codex、Antigravity、Cursor、Gemini CLI、Windsurf、OpenCode、Claude Desktop**（同时打通 Windows 宿主机与 WSL2 配置）。
3. **🚀 多账号 K 路并发分片 & 跨账号自动容灾**
   - `4 张 G4 跑 1 小时 == 1 张 G4 跑 4 小时`，耗费总 CU 完全一致但耗时直降为 1/4！一条 `colabhive run --gpu G4 --shards 4 eval.py` 自动跨账号原子锁槽并发执行。
   - 遇到单号 `HTTP 400` 缺卡、`412` 并发满载、`429` 限流或代理抖动，毫秒级自动漂移至池内下一个健康账号；单账号独立绑定固定 `HTTP / SOCKS5` 出口代理，绝不串 IP。
4. **🛡️ 8 秒级 `/proc/{pid}/stat` 防僵尸守护 + 60GB 权重热重启**
   - 穿透识别 Linux `Z` 僵尸进程，跑完或异常立即确定性 `unassign` 释放计费；改代码重跑可只重启 Worker PID，保留 `/root/.cache/huggingface` 里的几十 GB 模型权重免重复下载。
5. **🤝 30 秒免云盘权限同伴算力共享（`/contribute/:code`）**
   - 实验室同门或群友有闲置临期 CU？发一个邀请链接，对方 30 秒完成远程 PKCE 授权：**仅申请 `colaboratory` 单一权限（物理零 Google Drive 权限）** + WebSocket 内核层拦截 `drive.mount` + 自定义保底自留额度（如“至少给我留 80 CU”）+ 专属回执一键自助撤回。

### 10 秒一键体验

**Windows（原生托盘桌面端 + CLI）：**
```powershell
irm https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.ps1 | iex
```

**macOS / Linux：**
```bash
curl -fsSL https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.sh | sh
```

**团队云服务器（Docker + Caddy 自动签发 HTTPS）：**
```bash
docker compose -f docker-compose.cloud.yml --env-file .env.cloud --profile caddy up -d
```

仓库里还整理了完整的 Google Colab 双后端协议（`v1beta` + `/tun/m`）、Jupyter ZMQ WebSocket 帧协议与防掉线架构规范文档。欢迎各位佬体验拍砖，求个 Star 支持！🙏
```

---

### B. 知乎深度技术文章大纲（《我们如何逆向梳理官方协议，把散落的 Google Colab 账号变成多卡并发集群与 AI Agent 路由中枢》）

1. **痛点引入：为什么手握 2500 CU 的 AI Ultra 和多个 Pro+，跑实验依然像在“手工作坊”？**
   - 算一笔账：Colab 2026 新上线 `NVIDIA RTX PRO 6000 Blackwell (G4, 96GB GDDR7)` 仅 `9.66 CU/h`，性价比极高，但单账号并发受限、90 天过期作废、半夜断连空转烧钱。
2. **拆解 Google 官方 Colab 双后端架构与 `colab-mcp` 的三大工程缺陷**
   - `colaboratory.googleapis.com/v1beta` vs `colab.research.google.com/tun/m` 职责边界；
   - 为什么官方 `colab-mcp` 会报 `1013 Server is busy` 以及 `notifications/tools/list_changed` 导致工具在客户端“隐身”？ColabHive 如何通过全无头三协议网关根治。
3. **核心调度设计：并发原子槽位预占、二维健康状态机与单账号 Sticky Proxy 隔离**
   - 解决 4 分片同时拉起时的 `HTTP 412` 竞态踩踏；
   - `H100 -> G4 -> A100 -> L4 -> T4` 瀑布流自动降级与跨账号死机自愈。
4. **消灭每一分冤枉钱：`/proc/{pid}/stat` 防僵尸机制与热缓存秒级重跑**
   - 详解为什么 Python `os.kill(pid, 0)` 抓不到 `Z (zombie)` 进程，以及 8 秒心跳巡检的状态转换图。
5. **零信任算力众筹：如何在物理隔离 Google Drive 的前提下让实验室同门安全共享闲置 CU？**
   - OAuth 最小 Scope + WebSocket `colab_request` 内核防火墙 + `min_reserve_cu` 保底熔断。
6. **本地原生桌面端（`colabhive-desktop.exe`）与云端多租户服务器（`--mode cloud`）双模实践**
   - 附上在线交互沙盒（`https://hexwander.github.io/colabhive/`）与 GitHub 开源地址（`https://github.com/hexwander/colabhive`）。
