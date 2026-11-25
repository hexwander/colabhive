# Changelog

All notable changes to **ColabHive** will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.1.0] - 2026-11-25

### Added
- **Plugin Platform v2 & 0ms Native Multimodal Media Studio (`/studio/media`, `internal/pluginhost`, `docs/PLUGIN_PLATFORM_V2.md`)**:
  - Decouples cloud GPU engines from local frontend UIs via a public `/sdk/v1/*` capability bus, **Permanent Local Asset Vault** (`data/assets` with prompt/seed/resolution/lineage `parent_asset_id`), and isolated loopback sandbox origins for swappable third-party UI plugins.
  - Built-in **0ms Native Media Studio** (`40 KB` gzipped React UI vs `23.3 MB` remote Gradio) powered by headless **WanGP (`media.v1`, DeepBeepMeep)** with serial GPU execution queuing, live denoising preview SSE streaming, exact `image_refs` / `image_start` / `8k+1` frame mapping, and dynamic sidebar surfacing (appears when active, pinned, or open).
  - Live-verified across all 5 modalities on 96GB Blackwell G4: **T2I** (`NitroFusion2 Z-Image` in `7.2s`), **I2I** (`FLUX.3 Klein` with lineage), **T2V** (`LTX-2 Distilled 19B`), **I2V** (`17.2s` hot VRAM), and **Audio/TTS** (`Qwen3-TTS VoiceDesign`).
- **5-App Cloud GPU App Store & Comm Tunnel v3 (`/apps`, `internal/appstore`, `internal/commtunnel`)**:
  - **🔓 Uncensored Open-Weight LLM Deployer (`/apps/llm`)**: 1-click deploy Heretic / Abliterated 2026 community models (`Qwen3`, `Gemma 4`, `DeepSeek`, `GLM`) or arbitrary `hf.co/...` GGUF URLs exposing local **OpenAI `/v1`**, **Anthropic `/v1/messages`**, and **Ollama `/api/chat`** for SillyTavern, Cherry Studio, Cursor & Claude Code.
  - **🎨 ComfyUI Pro Node Studio (`comfyui`)**: Real ComfyUI (`v0.46+`) with verified 2026 Comfy-Org weights (`Z-Image Turbo`, `Qwen-Image FP8`, `FLUX.2 Dev FP8`) + custom HF/CivitAI URLs over isolated per-app loopback `127.x.y.z:8188` — connects directly to **official ComfyUI Desktop ("Connect to Server")** and **Krita AI Diffusion** with full WebSocket `/ws` (`HTTP 101`) support and `/content/ComfyUI/output` backup.
  - **🦥 Unsloth Studio Zero-Code Fine-Tuning (`unsloth-studio`)**: Official visual fine-tuning studio (`15.7 KB` SPA, `2.28s` load) with automatic `/content/unsloth_outputs` checkpointing & GGUF export.
  - **📓 JupyterLab Cloud Lab (`jupyterlab`)**: Real PyTorch + CUDA environment with per-instance random token + XSRF protection and **1-click browser & VS Code auto-login** (automatically attaches `/lab?token=...` on `LocalURL` while masking raw tokens in UI badges).
  - **Comm Tunnel v3**: Upgraded Jupyter WS Comm tunnel with `zlib` chunk compression + `384 KB` credit-based flow control, eliminating HTTP/WS head-of-line blocking and cutting remote WebUI latency by **5.4× – 9.6×**.
  - **Live App Rehydration**: Persists running studio metadata across daemon restarts so active cloud GPUs and local bridges reconnect automatically.
- **Safe Polite-Backoff GPU Waitlist Mode, Auto-Fallback & Friendly Shortage Diagnostics (`AppLaunchDrawer`, `friendlyErrors.ts`)**:
  - Added **Safe GPU Waitlist (`queue_retry_enabled`)** to beat Google Colab `HTTP 503 / 412` capacity shortages safely: retries with anti-ban jittered backoff (`15s → 45s`), live 5s countdowns in deploy logs, and instant `0ms` cancel with `0 CU` burned.
  - Added explicit **Auto-Fallback (`auto_fallback`)** toggle (`H100 → G4 → A100 → L4 → T4`) and structured human-readable shortage diagnostics with 1-click waitlist retry across App Store, Launch Drawer, and LLM Deployer.
- **Standalone 2× Retina Compute Savings Poster Studio & Auto-Checkpoint Relay (`/savings`, `internal/checkpoint`)**:
  - Dedicated `/savings` page with honest GPU savings calculations and 1-click 2× Retina HTML5 `<canvas>` PNG export (`Obsidian Gold` & `Warm Alabaster`, 100% PII/email redacted).
  - Immutable per-checkpoint `<checkpointID>/` snapshotting with symlink-proof `os.OpenRoot` local I/O and cross-account preemption resume.
- **Router & Workbench Polish**:
  - Deduplicated legacy `free-opportunistic` strategy card into `free-first` in UI selectors while retaining full backend/CLI/MCP compatibility.
  - Bounded default `GET /api/v1/sessions` payload to **100% active sessions + 50 most recent historical rows** (with `?all=true` / `?limit=N` override) for snappy Workbench loading.

### Security & Reliability
- Hardened `--mode cloud` and protected auth boundaries: scoped `ch_sk_live_...` agent keys verified blocked (`403 Forbidden`) from admin `/api/v1/accounts` while authorized on `/mcp`; isolated per-plugin sandbox origins prevent cross-plugin XSS; strict Hugging Face / CivitAI HTTPS redirect validation prevents SSRF and strips `HF_TOKEN` off-origin.
- Eliminated silent live-mode demo fallbacks across backend and frontend, surfacing genuine upstream errors cleanly while preserving `VITE_DEMO_SITE=1` showcase interactivity.

## [1.0.0] - 2026-11-22

### Added
- **Multi-Account Google Colab Pool & Quota Observatory (`/`, `/pool`)**: Unified pooling across Google AI Ultra (`2,500 CU`), Pro+ (`500 CU`), Pro (`100 CU`), Pay-As-You-Go, and Free Dynamic Window accounts with EWMA GPU-hour equivalence calibration, 4-step headless OAuth2 PKCE, and non-intrusive external VM sync.
- **Waste-Zero Scheduler, Auto-Failover & Tri-Protocol MCP Control Plane (`/router`, `/cfg`)**: `waste-zero`, `max-balance`, `sticky-session`, `round-robin`, and `free-first` routing; `/mcp` Streamable HTTP + SSE + `stdio`; 1-click `cc-switch` style config sync across 8 AI IDEs; scoped `ch_sk_...` agent API keys.
- **Live Experiment Workbench, Sharded Runner & Anti-Zombie Watchdog (`/exp`)**: Parallel $K$-way sharded runs (`colabhive run --gpu G4 --shards N`), live ANSI terminal, inline Matplotlib artifacts, 8s ZMQ keepalive, and `/proc/{pid}/stat` Linux zombie (`Z`) auto-reclamation.
- **Zero-Drive External Contributor Portal (`/contribute/:code`)**: Least-privilege `colaboratory`-only OAuth scope, WebSocket Drive-mount firewall, personal `min_reserve_cu` floor, and 1-click self-service revocation receipts.
- **Native Windows Desktop & System Tray App (`colabhive-desktop.exe`)** + **macOS `ColabHive.app` Menu Bar** + **Drop-in `colab` CLI & Zero-Dependency Python SDK** + **Docker Cloud Server Stack**.

[1.1.0]: https://github.com/hexwander/colabhive/compare/v1.0.0...v1.1.0
[1.0.0]: https://github.com/hexwander/colabhive/releases/tag/v1.0.0
