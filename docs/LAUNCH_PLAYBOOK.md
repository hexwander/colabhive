# ColabHive v1.1 — Open-Source Viral Launch Playbook (Global + Chinese Communities)

Ready-to-publish launch kit for [`https://github.com/hexwander/colabhive`](https://github.com/hexwander/colabhive), featuring real hardware benchmarks, 100% zero-PII 2× Retina screenshots, and platform-native copy tailored for **GitHub**, **Reddit (`r/comfyui`, `r/LocalLLaMA`, `r/StableDiffusion`)**, **Hacker News (`Show HN`)**, **X/Twitter**, **Linux.do**, **V2EX**, **小红书**, and **知乎**.

---

## 1. Pre-Launch GitHub Checklist (3 Minutes)

1. **Merge & Tag `v1.1.0` (only when you're ready!)**:
   ```bash
   git checkout main && git merge feat/v1.1
   git tag -a v1.1.0 -m "ColabHive v1.1.0 — 0ms Native Media Studio, ComfyUI Desktop Cloud Bridge, Uncensored LLM /v1, Unsloth Studio & Safe GPU Waitlist"
   git push origin main --tags
   ```
2. **Update Repository About / Description**:
   - **Description**:
     `Turn Google Colab accounts (96GB G4 / H100 / L4 / Free T4) into a private AI supercomputer: 0ms Native Video/Image Studio, 1-click Uncensored LLM /v1 API, ComfyUI Desktop cloud bridge, Unsloth fine-tuning & 8-IDE MCP router.`
   - **Website**: `https://hexwander.github.io/colabhive/`
   - **Topics**:
     `google-colab` `comfyui` `wangp` `unsloth` `mcp-server` `local-llm` `openai-api` `sillytavern` `claude-code` `cursor` `gpu-cluster` `golang` `video-generation` `self-hosted`
3. **Upload Social Preview**: `docs/assets/social-preview-og.png` (`1280×640`) in **Settings → General → Social preview**.
4. **Verify Release Assets**: Confirm CI publishes Windows zip (`colabhive-desktop.exe`, `colabhive.exe`, `colab.exe`, `web/dist`), macOS `ColabHive.app`, Linux tarballs, and `SHA256SUMS.txt`.

---

## 2. Reddit — 3 High-Impact Subreddit Posts

### A. `r/comfyui` (Highest Viral Potential!)
- **Attach Image**: `docs/assets/v11-comfy-desktop.png` *(Official ComfyUI Desktop showing `✓ Ready · 127.x.y.z:8188` sitting directly next to paid `Comfy Cloud RTX 6000 Pro 96GB`!)*
- **Title**:
  ```text
  Comfy Cloud wants $960/yr for RTX 6000 Pro 96GB — so I built an open-source bridge so official ComfyUI Desktop connects directly to your own Google Colab 96GB G4 / L4 / Free T4 (zero public tunnel)
  ```
- **Body**:
  ```text
  If you already pay for Google AI Ultra (which literally includes the exact same 96GB RTX PRO 6000 Blackwell G4!) or Colab Pro+/Free T4, paying another $192–$960/yr for Comfy Cloud hurts.

  I just shipped ColabHive v1.1 (Apache-2.0, single static Go binary + Windows Tray / macOS Menu Bar app). Here's how the ComfyUI Desktop integration works:

  1. Pick ComfyUI in ColabHive's App Store (Blank 0GB, Z-Image Turbo, Qwen-Image FP8, FLUX.2 Dev FP8, or paste any HF/CivitAI URL) and choose your GPU tier (Free T4 / L4 24GB / 96GB G4 / H100).
  2. ColabHive boots real ComfyUI on Colab and maps it over our custom zlib-compressed Jupyter WS tunnel (`Comm Tunnel v3`) to an isolated local loopback address (`127.x.y.z:8188`) — zero ngrok, zero public URLs, full WebSocket `/ws` progress streaming, and zero collision with your local `localhost:8188`.
  3. Open official ComfyUI Desktop → "Connect to Server" → paste `http://127.x.y.z:8188`. Done! Zero local VRAM, zero local disk bloat, and `/content/ComfyUI/output` auto-syncs back to your local machine.
  4. Added **Safe GPU Waitlist Mode** (polite 15–45s anti-ban backoff + 0ms cancel) and **Auto-Fallback** (`H100 -> G4 -> A100 -> L4 -> T4`) to survive Colab's 503/412 capacity shortages, plus automatic idle shutdown so you never forget a running GPU.

  GitHub (Apache-2.0): https://github.com/hexwander/colabhive
  Zero-install interactive web sandbox: https://hexwander.github.io/colabhive/

  Happy to answer technical questions about the Jupyter Comm tunnel or adding custom ComfyUI nodes/presets!
  ```

### B. `r/LocalLLaMA`
- **Attach Images**: `docs/assets/v11-llm-deploy.png` + `docs/assets/v11-media-studio.png`
- **Title**:
  ```text
  [Open Source] ColabHive v1.1: Turn pooled Google Colab GPUs (96GB G4 / H100 / L4 / Free T4) into a private local OpenAI/Anthropic/Ollama /v1 server + Unsloth zero-code fine-tuning + 0ms Native Video/Image Studio
  ```
- **Body**:
  ```text
  Built this in pure Go (`CGO_ENABLED=0`) + React because managing multiple Colab accounts (AI Ultra 96GB Blackwell G4, Pro+, expiring 90-day CUs, and Free T4 windows) across browser tabs was painful and leaky.

  What's new in v1.1 (everything live-tested on real G4/L4/T4):
  - **1-Click Uncensored LLM `/v1` Deployer**: Launch abliterated/Heretic Qwen3, Gemma 4, DeepSeek, GLM, or paste ANY `hf.co/<owner>/<repo>` GGUF URL. Exposes native OpenAI `/v1/chat/completions`, Anthropic `/v1/messages`, and Ollama `/api/chat` on `127.0.0.1:7860` — works out of the box with SillyTavern, Cherry Studio, Cursor, and Claude Code.
  - **Unsloth Studio Zero-Code Fine-Tuning**: Official visual fine-tuning UI (`15.7 KB` SPA, loads in ~2.3s over our compressed tunnel) with automatic `/content/unsloth_outputs` checkpoint sync & GGUF export.
  - **0ms Native Multimodal Media Studio (Powered by WanGP)**: Replaced WanGP's 23 MB remote Gradio page with a 0ms local React studio + Permanent Local Asset Vault (`T2I` 7.2s, `I2I`, `T2V`, `I2V` 17.2s hot VRAM, and `Qwen3-TTS` audio verified on 96GB G4). Shut down the cloud GPU immediately after generating — every image/video/audio stays permanently on local disk with lineage tracking.
  - **Safe GPU Waitlist & Anti-Zombie Watchdog**: Polite 15–45s jittered waitlist for Colab 503/412 shortages, `/proc/{pid}/stat` Linux zombie (`Z`) reaping, and 30/45/60m idle auto-stop.
  - **Headless Tri-Protocol MCP Server** for 8 coding IDEs (Claude Code, Codex, Cursor, Antigravity, Gemini CLI, Windsurf, OpenCode, Claude Desktop).

  Code (Apache-2.0): https://github.com/hexwander/colabhive
  Live browser demo: https://hexwander.github.io/colabhive/
  ```

### C. `r/StableDiffusion`
- **Attach Image**: `docs/assets/v11-media-studio.png`
- **Title**:
  ```text
  I replaced WanGP's 23MB remote Gradio page with a 0ms local React Media Studio + Permanent Local Asset Vault backed by pooled Colab 96GB Blackwell G4 GPUs (Apache-2.0)
  ```
- **Highlight**: Focus on `Z-Image NitroFusion2` in 7.2s, `FLUX.3 Klein` I2I with lineage, `LTX-2 Distilled 19B` I2V in 17.2s hot VRAM, `Qwen3-TTS`, offline permanent local gallery (0 CU burned while browsing!), plus swappable UI Plugin Platform v2 & ComfyUI Desktop bridge.

---

## 3. Hacker News (`Show HN`)

- **Best Timing**: Tue–Thu, 08:30–10:00 US Eastern (12:30–14:00 UTC).
- **Title** (`79` chars):
  ```text
  Show HN: ColabHive v1.1 – Turn Colab GPUs into a 0ms local AI studio & MCP pool
  ```
- **First Author Comment**:
  ```text
  Hi HN — I built ColabHive (single static Pure-Go binary + native Windows Tray / macOS Menu Bar app) to turn scattered Google Colab accounts (AI Ultra 96GB Blackwell G4, Pro+, expiring 90-day CUs, and Free T4 windows) into a local-first GPU workstation without public ngrok tunnels or silent zombie CU leaks.

  A few systems problems we had to solve for v1.1:
  1. Tunneling heavy WebUIs without public ports (Comm Tunnel v3): Instead of exposing ngrok/Gradio URLs, ColabHive multiplexes TCP over Google's authenticated Jupyter WebSocket Comm channel. Raw multiplexing hit severe head-of-line blocking on 23MB Gradio pages (35s+ timeouts), so v3 adds per-chunk zlib compression + 384KB credit-based flow control — cutting `/gradio_api/info` from 35.8s to 3.7s and sustaining ComfyUI Desktop HTTP + WebSocket (`101 Switching Protocols`) over isolated `127.x.y.z:<port>` loopback addresses.
  2. Decoupling Cloud GPU Engines from Frontend UIs (Plugin Platform v2): Even with a fast tunnel, remote Gradio UIs vanish when you stop billing the GPU. So we built a local `/sdk/v1/*` capability bus + Permanent Local Asset Vault (`parent_asset_id` lineage) + isolated loopback UI sandboxes. Our built-in Native Media Studio (powered by WanGP's headless API) opens in 0ms locally (`40KB` gzipped), streams live denoising previews over SSE from a serial GPU job queue, and keeps all generated T2I/I2I/T2V/I2V/TTS assets permanently on local disk even after releasing the 96GB G4.
  3. Surviving Colab 503/412 capacity shortages without triggering bans: Added a polite jittered-backoff GPU waitlist (`15s -> 45s`, instantly cancellable with 0 CU burn) + explicit tier fallback (`H100 -> G4 -> A100 -> L4 -> T4`), alongside our existing `/proc/{pid}/stat` anti-zombie reaper and 8-IDE headless MCP router.

  Zero-install interactive sandbox: https://hexwander.github.io/colabhive/
  Source (Apache-2.0) & Plugin Platform v2 spec: https://github.com/hexwander/colabhive

  Happy to discuss Jupyter Comm multiplexing, loopback XSS isolation for local UI plugins, or Colab's dual-backend runtime protocol!
  ```

---

## 4. X / Twitter 6-Post Viral Thread

- **Post 1/6 (Hook)** — Attach `docs/assets/v11-media-studio.png`
  ```text
  Paying $249/mo for Google AI Ultra (96GB Blackwell G4) or sitting on expiring Colab Pro+ CUs?

  Meet ColabHive v1.1 🐝 — open-source (Apache-2.0) local AI workstation that squeezes 100% value out of your Colab GPUs:
  • 0ms Native Video/Image Studio (WanGP headless)
  • ComfyUI Desktop cloud bridge (skip $960/yr Comfy Cloud!)
  • 1-click Uncensored LLM /v1 API
  • Unsloth zero-code fine-tuning
  • 8-IDE MCP router

  🧵👇 https://github.com/hexwander/colabhive
  ```
- **Post 2/6 (ComfyUI Desktop Killer Comparison)** — Attach `docs/assets/v11-comfy-desktop.png`
  ```text
  Look at the bottom of official ComfyUI Desktop 👀

  Right: Paid Comfy Cloud (RTX 6000 Pro 96GB — up to $960/yr)
  Left: ✓ Ready · ColabHive (127.x.y.z:8188) running on YOUR OWN Colab GPU!

  Zero ngrok. Zero local VRAM. Zero local disk bloat. Full WebSocket streaming + automatic local output backup.
  ```
- **Post 3/6 (0ms Native Media Studio + Local Asset Vault)** — Attach `docs/assets/v11-media-studio.png`
  ```text
  Remote Gradio pages (23MB+) lag over tunnels and lose all your work when Colab disconnects.

  ColabHive Plugin Platform v2 splits cloud compute from local UI:
  • 0ms local React studio + Permanent Local Asset Vault
  • Live-verified on 96GB G4: Z-Image T2I (7.2s), FLUX.3 I2I, LTX-2 I2V (17.2s hot VRAM), Qwen3-TTS
  • Release the GPU immediately — your gallery stays offline forever at 0 CU/hr.
  ```
- **Post 4/6 (1-Click Uncensored LLM `/v1` + Unsloth)** — Attach `docs/assets/v11-llm-deploy.png`
  ```text
  Need a private uncensored LLM API?
  Deploy Heretic/Abliterated Qwen3, Gemma 4, DeepSeek, GLM, or ANY `hf.co/...` GGUF onto pooled Free T4 / L4 / 96GB G4 GPUs.

  Exposes OpenAI `/v1`, Anthropic `/v1/messages` & Ollama `/api/chat` on `127.0.0.1:7860` — instant plug-in for SillyTavern, Cherry Studio, Cursor & Claude Code.
  ```
- **Post 5/6 (Beat Colab 503/412 + Comm Tunnel v3)** — Attach `docs/assets/v11-launch-waitlist.png`
  ```text
  Colab throwing 503/412 GPU shortages?
  Toggle "Safe GPU Waitlist" — polite 15–45s anti-ban jittered retries, live countdown logs, 0ms cancel (0 CU burned), plus Auto-Fallback (`H100 → G4 → A100 → L4 → T4`).

  Powered by Comm Tunnel v3 (`zlib` + 384KB credit flow) — 5×–10× faster remote UIs over private loopback!
  ```
- **Post 6/6 (Savings Poster + 1-Line Install)** — Attach `docs/assets/v11-savings-poster.png`
  ```text
  Every idle GPU auto-stops (`30/45/60m`), every zombie process is reaped via `/proc/stat`, and `/savings` exports a 100% zero-PII 2× Retina savings receipt.

  Try the live browser demo (no install): https://hexwander.github.io/colabhive/
  Install on Windows / macOS / Linux in 30s: https://github.com/hexwander/colabhive
  ```

---

## 5. 中文社区爆款推广文案（直接复制即发！）

### A. Linux.do（L 站 — 最强爆火阵地！）
- **配图顺序**：`v11-comfy-desktop.png`（镇楼神图！）+ `v11-media-studio.png` + `v11-llm-deploy.png` + `v11-launch-waitlist.png` + `v11-savings-poster.png`
- **标题**：
  ```text
  [开源] ColabHive v1.1：把 Google AI Ultra 96GB G4 / 免费 T4 榨干回本！直连官方 ComfyUI Desktop 省 $960/年、0ms 原生视频生图台、一键无审查 /v1 接酒馆、安全候补抢卡
  ```
- **正文**：
  ```markdown
  手里有 Google AI Ultra（96GB Blackwell G4 顶级大显存卡）、Colab Pro+ 或者几个免费 T4 号的佬友应该都深有体会：
  - 官方 Comfy Cloud 租一张 RTX 6000 Pro 96GB 一年要 **$960（约 ¥6900）**，但咱 AI Ultra 号里本来就有同款神卡；
  - 自己在 Colab 里起 Gradio 动辄 23MB 加载半分钟还 502，一断线生成的图和视频全飞；
  - 高峰期经常遇到谷歌 503/412 缺卡，手动狂点既累又怕触发风控；
  - 跑完忘记关机按 9.66 CU/h 烧到天亮心在滴血。

  于是我用纯 Go（静态单文件 + Windows 原生托盘 / macOS 菜单栏）肝出了 **ColabHive v1.1**（Apache-2.0 全开源），今天把 5 大模块在真实 96GB G4 / L4 / 免费 T4 上全跑通了，直接端上来给佬友们白嫖：

  ### 1. 官方 ComfyUI Desktop 无缝白嫖自家云端显卡（见首图！）
  - 一键在云端拉起原版 ComfyUI（内置 2026 Comfy-Org 官方 `Z-Image Turbo` / `Qwen-Image FP8` / `FLUX.2 Dev FP8` + 任意 HF/CivitAI 直链），通过我们自研的 `Comm Tunnel v3`（zlib 压缩 + 流控，提速 5–10 倍）映射到本机独立回环地址 `127.x.y.z:8188`（**免 ngrok、免公网暴露、不抢本地 8188 端口**）。
  - 打开官方 ComfyUI Desktop →「连接到服务器」填上地址，直接 0 本地显存、0 本地硬盘占用跑云端 96GB G4 / L4，成品自动备份回本地！

  ### 2. 0ms 原生多模态创作台（Powered by WanGP）+ 本地永久画廊
  - 彻底甩掉 23MB 卡顿 Gradio！前端是 0ms 秒开的本地 React 创作台，云端跑无头 GPU 引擎（`media.v1`）+ 串行防冲突队列。
  - **96GB G4 真机实测 5 大模态全绿**：文生图 `Z-Image NitroFusion2` **7.2 秒**、图生图 `FLUX.3 Klein` 自动带血缘、图生视频 `LTX-2 Distilled 19B` 显存热启 **17.2 秒**、`Qwen3-TTS` 中文语音合成。
  - 出完图立刻释放显卡停表，所有作品和提示词永久留在本地硬盘画廊，支持一键送入图生图/图生视频，还能用 **插件平台 v2** 一键换社区自定义 UI！

  ### 3. 一键无审查大模型 `/v1` 部署（直连酒馆 / Cherry Studio / Cursor）
  - 内置 2026 社区热门 Heretic / Abliterated 无审查模型（Qwen3 / Gemma 4 / DeepSeek / GLM）及**任意 `hf.co/...` GGUF 直链**。
  - 本机 `127.0.0.1:7860` 原生兼容 OpenAI `/v1` + Anthropic `/v1/messages` + Ollama `/api/chat`。

  ### 4. Unsloth 零代码微调 + JupyterLab 一键免登 + 安全候补抢卡
  - **Unsloth Studio**：拖文档洗数据 → 2 倍速微调 → 导 GGUF，首屏 2.28 秒；
  - **JupyterLab**：每实例独立随机 Token + XSRF，点击直接免输密码进 Lab / 连 VS Code；
  - **安全候补抢卡模式**：遇到谷歌 503/412 缺卡自动按 15–45 秒礼貌抖动退避排队（防风控），支持 0ms 随时取消（0 消耗）+ `H100→G4→A100→L4→T4` 自动顺延！
  - **8 大 AI IDE 一键 MCP 接入**（Claude Code / Codex / Cursor / Antigravity / Gemini CLI / Windsurf / OpenCode / Claude Desktop）+ 2× 视网膜零隐私省钱海报。

  - **GitHub 开源地址（求个 Star 鼓励一下！）**：https://github.com/hexwander/colabhive
  - **免安装在线交互体验**：https://hexwander.github.io/colabhive/
  - Windows 一行安装：`irm https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.ps1 | iex`

  欢迎佬友们提 Issue / 写第三方 `colabhive-app.json` 或 UI 插件，有啥想加的模型和工作流评论区直接说！
  ```

### B. V2EX（`/go/create` 创造节点）
- **标题**：
  ```text
  开源了 ColabHive v1.1：用 Go 写的本地 Colab 多号 GPU 算力池，支持 0ms 视频生图创作台、ComfyUI Desktop 直连、无审查 /v1 API 与 8 大 IDE MCP 路由
  ```
- **要点**：简洁克制工程师风——直接列出痛点（多账号 CU 过期、Gradio 隧道慢、503/412 缺卡、僵尸进程偷烧）、核心架构（纯 Go 静态单文件、Comm Tunnel v3 zlib 流控、Plugin Platform v2 本地沙盒 + 永久资产库、安全退避候补）、附真机实测数据 + GitHub 链接 + 在线沙盒链接。

### C. 小红书 / 朋友圈（高点击图文笔记）
- **封面图**：`docs/assets/v11-comfy-desktop.png` 或 `docs/assets/v11-media-studio.png`（配合 `v11-savings-poster.png` 当第 2 张图！）
- **标题（3 个备选爆款标题）**：
  1. `花 $960 买 Comfy Cloud？我开源了个神器，直接用自己的 Colab 96G 显卡直连 ComfyUI Desktop！`
  2. `开源了！把吃灰的 Google Colab 变成 0 延迟视频生图创作台 + 私人无审查大模型 API 🐝`
  3. `7 秒出 4K 写实图、17 秒图生视频！给 Colab 做了个本地永久画廊 + 自动防烧钱停表神器`
- **正文**：
  ```text
  买了 Google AI Ultra 或 Colab Pro+ 额度用不完？免费 T4 每次都要手动切号？
  我把折腾了几个月的私人算力神器 ColabHive v1.1 彻底开源了（免费 Apache-2.0，Windows / macOS 双端桌面托盘）：

  ✅ 官方 ComfyUI Desktop 直接连上自家云端 96GB G4 / L4 显卡（0 本机显存占用，省下一年 7000 块 Comfy Cloud 订阅费！）
  ✅ 0ms 秒开本地多模态创作台（文生图 7 秒、图生视频 17 秒、中文语音合成），关掉云显卡作品永久留本地硬盘！
  ✅ 一键部署无审查开源大模型（Qwen3 / Gemma 4 / DeepSeek / 任意 HF GGUF），吐出本机 /v1 直接连酒馆、Cherry Studio、Cursor！
  ✅ 内置 Unsloth 零代码微调 & JupyterLab 一键免登！
  ✅ 遇到谷歌 503/412 缺卡，开启「安全候补抢卡」后台礼貌排队，抢到自动启动，用完 30/45/60 分钟自动停表绝不偷烧额度！

  GitHub 搜索：hexwander/colabhive（开源免费，觉得好用欢迎点颗 Star ⭐）
  #开源项目 #AI绘画 #ComfyUI #GoogleColab #独立开发 #大模型部署 #人工智能
  ```

### D. 知乎（技术向长文 / 回答）
- **切入问题**：*「Google AI Ultra 怎么用回本？」「如何免费/低成本使用 ComfyUI 和本地大模型？」「有哪些提升 AI 编程 Agent 效率的开源 MCP 工具？」*
- **结构**：痛点算账（Comfy Cloud \$960/年 vs Vast/RunPod 账单 vs 闲置过期 CU）→ 真机实测表（96GB G4 5 大模态 + Comm Tunnel v3 压测）→ 架构图（Plugin Platform v2 本地 0ms UI + 独立沙盒 + 本地资产库）→ 30 秒安装教程 + GitHub 链接。
