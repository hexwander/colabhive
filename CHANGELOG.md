# Changelog

All notable changes to **ColabHive** will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-11-22

### Added
- **Multi-Account Google Colab Pool & Quota Observatory (`/`, `/pool`)**:
  - Unified pooling across Google AI Ultra (`2,500 CU`), Colab Pro+ (`500 CU`), Colab Pro (`100 CU`), Pay-As-You-Go, and Free Dynamic Window accounts.
  - **Magpie-Style GPU-Hour Equivalence Matrix**: Real-time translation of raw Compute Units into remaining runtime hours across `G4` (RTX PRO 6000 Blackwell 96GB), `H100` (80GB), `A100` (40/80GB), `L4` (24GB), `T4` (16GB), `TPU v6e-1`, `TPU v5e-1`, and `CPU`, dynamically calibrated via EWMA burn-rate sampling from `/tun/m/ccu-info`.
  - **4-Step Headless-Safe OAuth2 PKCE Wizard**: Supports remote SSH/Docker copy-paste flows alongside read-only 1-click auto-discovery of local/WSL2 `colab` CLI keyrings (`keyrings.alt`), `gcloud` ADC JSON, and AES-256-GCM encrypted `.chvault` portable bundles.
  - **Non-Intrusive External VM Sync**: Discovers and adopts already-running Google Colab VMs (`POST /api/v1/sessions/sync`), streaming live `/content/worker.log` output and VRAM/GPU telemetry without interrupting existing workloads.
- **Waste-Zero Scheduler, Auto-Failover & MCP Control Plane (`/router`, `/cfg`)**:
  - Four scheduling strategies: `waste-zero` (expiring/resetting CUs first), `max-balance`, `round-robin`, and `free-first`.
  - **Stateful Sticky Kernel Affinity + Escape**: Pins consecutive AI Agent calls to warm Jupyter kernels (preserving Python globals and `/root/.cache/huggingface` weights) with automatic failover across HTTP `412` concurrency caps, HTTP `429` rate limits, and HTTP `400` per-GPU tier restrictions (`H100 -> G4 -> A100 -> L4 -> T4`).
  - **Dual-Transport MCP Server (`/mcp` Streamable HTTP + `colabhive mcp` stdio)** & **1-Click Agent Injector (`cc-switch` pattern)** with atomic `.bak` backups for **Claude Code**, **Codex**, **Antigravity**, **Cursor**, **Gemini CLI**, **Windsurf**, **OpenCode**, and **Claude Desktop**.
  - **Scoped `ch_sk_...` Agent API Keys**: Per-key account group boundaries, GPU tier allowlists, max concurrent VMs, and daily/total CU hard caps.
- **Live Experiment Workbench, Sharded Runner & Anti-Zombie Watchdog (`/exp`)**:
  - Parallel multi-account sharded execution (`colabhive run --gpu G4 --shards N`) with deterministic JSONL shard merging and guaranteed `unassign` teardown.
  - Real-time WebSocket ANSI terminal, virtual notebook cells, inline Matplotlib PNG artifact capture, `/content` remote file explorer, 8-second ZMQ reaper keepalive, and `/proc/{pid}/stat` Linux zombie (`Z`) auto-reclamation.
- **Zero-Drive External Contributor Portal (`/contribute/:code`)**:
  - Shareable guest invite links with least-privilege OAuth scopes (no Google Drive or GCP access), kernel-level `colab_request` (`dfs_ephemeral`) Drive mount firewall, contributor-defined CU reserve floors & schedule windows, and 1-click self-service revocation receipts (`/my-contribution/:receipt_token`).
- **Native Windows Desktop & System Tray App (`colabhive-desktop.exe`)**:
  - Pure-Go (`CGO_ENABLED=0`, `-H=windowsgui`) Win32 + WebView2 desktop client featuring single-instance guard, dynamic tray telemetry icon, borderless Magpie-style `/tray` flyout popover (`Agent / Quota / Sessions / Router`), and startup toggle.
- **100% Drop-In `colab` CLI Compatibility & Zero-Dependency Python SDK (`sdk/python`)**:
  - `colab` alias supporting `new`, `exec`, `run`, `sessions`, `upload`, `download`, `url`, `stop`, and `usage --gpu-hours`.
  - Pure-stdlib Python `colabhive` SDK (with optional `httpx` transport) and `colabhive-sdk donate` CLI.
- **Hardened Cloud Deployment, 1-Line Installers & Interactive Live Showcase**:
  - `--mode cloud` production hardening with mandatory `COLABHIVE_ADMIN_TOKEN` + `COLABHIVE_MASTER_KEY`, ephemeral single-use WebSocket tickets (`POST /api/v1/ws-ticket`), per-account SOCKS5/HTTP proxy pinning, Docker Compose + Caddy TLS stack, `scripts/install.ps1` & `scripts/install.sh` SHA-256 verified installers, and GitHub Pages interactive live demo.

[1.0.0]: https://github.com/hexwander/colabhive/releases/tag/v1.0.0
