# Contributing to ColabHive

Thank you for helping build **ColabHive** — the open-source multi-account Google Colab GPU observatory, parallel shard runner, and zero-leak MCP/CLI router for AI Agents!

Whether you are fixing a protocol edge case, adding a new AI Agent 1-click connector, polishing the Precision Compute Observatory UI, or improving documentation, we welcome your contributions.

---

## Table of Contents

1. [Architecture Overview](#1-architecture-overview)
2. [Local Development Setup](#2-local-development-setup)
3. [Testing & Quality Gate](#3-testing--quality-gate)
4. [Strict Credential Privacy & Zero-Burn Rules](#4-strict-credential-privacy--zero-burn-rules)
5. [Pull Request Guidelines](#5-pull-request-guidelines)
6. [Security Vulnerability Disclosure](#6-security-vulnerability-disclosure)

---

## 1. Architecture Overview

ColabHive compiles into a **single `CGO_ENABLED=0` static Go binary** (`colabhive`, plus `colab` drop-in CLI compatibility) paired with an embedded React + TypeScript **Precision Compute Observatory** SPA and a zero-dependency **Python SDK (`sdk/python`)**.

```text
colabhive/
├── cmd/colabhive/          # Unified CLI (`serve`, `mcp`, `connect`, `pool`, + `colab` v0.8.0 commands)
├── internal/
│   ├── models/             # Canonical domain types, hardware catalog & service interfaces
│   ├── store/              # Pure-Go SQLite (`modernc.org/sqlite`) + AES-256-GCM vault
│   ├── pool/               # OAuth2 PKCE wizard, keyring/ADC/.chvault portability, Contributor Guardrails
│   ├── runtime/            # Dual-API Colab client (/v1beta + /tun/m), Jupyter ZMQ WS, Anti-Zombie engine
│   ├── router/             # Waste-Zero / Sticky-Session / Auto-Tier-Fallback scheduler & cc-switch injector
│   └── server/             # REST API (`/api/v1/*`), Contributor Portal, WS streams & MCP (`/mcp` + `stdio`)
├── web/                    # Vite + React + TypeScript + Tailwind v4 ("Precision Compute Observatory")
├── sdk/python/             # `colabhive` Python SDK & `colabhive-sdk donate` Contributor CLI
├── docs/architecture/      # Normative wire-protocol, official API & UX specifications
├── Dockerfile              # Multi-stage Node -> Go (CGO_ENABLED=0) -> Alpine build
└── docker-compose.yml      # 1-command self-hosted stack with persistent `/data` volume
```

Before making deep protocol or UX changes, please read the three authoritative architecture specs in [`docs/architecture/`](./docs/architecture/):
- [`colab-protocol-spec.md`](./docs/architecture/colab-protocol-spec.md) — Google OAuth2 PKCE remote flow, `keyrings.alt` INI escaping, `/tun/m/*` XSSI wire protocol, Jupyter ZMQ-over-WebSocket v5.x, 15m idle reaper keepalive, `/proc/{pid}/stat` anti-zombie detection, and CLI/MCP contracts.
- [`google-official-and-ccswitch-spec.md`](./docs/architecture/google-official-and-ccswitch-spec.md) — Official `colaboratory.googleapis.com/v1beta` + `colab.pa.googleapis.com/v1/user-info` dual-backend architecture, `cc-switch` surgical config injection, and External Contributor Safety Firewall.
- [`product-ux-spec.md`](./docs/architecture/product-ux-spec.md) — Precision Compute Observatory CSS tokens, Magpie GPU-Hour Equivalence Matrix, Live Experiment Workbench, and bilingual (`zh-CN` / `en-US`) glossary.

---

## 2. Local Development Setup

### Prerequisites
- **Go**: `1.22+` (tested with `1.24+`, pure-Go SQLite — no C compiler required for `CGO_ENABLED=0` builds)
- **Node.js & pnpm**: Node `20+` (`22 LTS` recommended) and `pnpm 9+` / `10+`
- **Python**: `3.10+` (only needed when working on `sdk/python/`)

### Step 1: Build & Run the Frontend Observatory (`web/`)
```bash
cd web
pnpm install
pnpm run dev      # Starts Vite dev server (proxies /api, /ws, /mcp to :7860)
pnpm run build    # Produces web/dist for Go static embedding
```

### Step 2: Run the Go Control Plane & CLI
```bash
# Build static binary
CGO_ENABLED=0 go build -o colabhive ./cmd/colabhive

# Launch Control Plane + Web UI on http://localhost:7860
./colabhive serve --port 7860 --data-dir ./data
```

### Step 3: Python SDK & Contributor CLI (`sdk/python/`)
The Python SDK uses **100% Python standard library** by default (with optional `httpx` acceleration):
```bash
# Editable install (with optional httpx transport)
pip install -e "./sdk/python[httpx]"

# Run unit tests directly without installing anything
PYTHONPATH=sdk/python/src python -m unittest discover -s sdk/python/tests -v
```

---

## 3. Testing & Quality Gate

Every pull request runs through [`.github/workflows/ci.yml`](./.github/workflows/ci.yml). Please verify all three checks locally before opening a PR:

```bash
# 1. Go static analysis & race-detector test suite
go vet ./...
go test -race ./...

# 2. Frontend TypeScript & production bundle check
pnpm --dir web run build

# 3. Python SDK unit test suite
python -m unittest discover -s sdk/python/tests -v
```

---

## 4. Strict Credential Privacy & Zero-Burn Rules

ColabHive handles sensitive Google OAuth2 credentials and paid Compute Units. All contributors and automated coding agents **must** follow these rules:

1. **NEVER commit real credentials or session artifacts**:
   - Never include `keyringrc.cfg`, `keyring_pass.cfg`, `application_default_credentials.json`, `.chvault`, `token.json`, `sessions.json`, real `refresh_token` (`1//0...`), `access_token` (`ya29...`), or `X-Colab-Runtime-Proxy-Token` strings in code, tests, fixtures, or PR descriptions.
2. **Zero Live Compute Spend in Automated Tests**:
   - Unit and integration tests **must** run against local `httptest.Server` / mock HTTP transports (including Colab's `)]}'\n` XSSI prefix simulation) and **never** allocate real Colab VMs or consume Compute Units in CI.
3. **Surgical Non-Destructive Agent Config Edits (`cc-switch` Law)**:
   - Any change to `colabhive connect` (which injects MCP configs into Antigravity, Claude Code, Codex, Cursor, Windsurf, Gemini CLI, or OpenCode) **must** create a timestamped first-write backup under `~/.colabhive/backups/` and preserve all unrelated user keys, hooks, and comments.
4. **Contributor Safety Invariants Are Non-Negotiable**:
   - External Contributor OAuth flows (`/contribute/:code`) must **never** request Google Drive (`drive`, `drive.file`) or `cloud-platform` scopes.
   - The WebSocket `colab_request` (`dfs_ephemeral` / `request_auth`) firewall and `min_reserve_cu` preemption logic must always fail closed for contributed accounts.

---

## 5. Pull Request Guidelines

- **Focused PRs**: Keep pull requests scoped to a single feature, bugfix, or documentation improvement.
- **Bilingual Parity (`en-US` & `zh-CN`)**:
  - When adding UI strings in `web/src/i18n/`, update **both** `zh-CN` and `en-US` dictionaries using the canonical domain glossary in `docs/architecture/product-ux-spec.md`.
  - Design UI changes to honor the **Precision Compute Observatory** aesthetic (`Bricolage Grotesque`, `Plus Jakarta Sans`, `JetBrains Mono` with `tabular-nums`, crisp `1px` borders — no circular gauges or generic purple gradients).
- **Commit Messages**: Follow Conventional Commits (`feat(router): ...`, `fix(runtime): ...`, `docs(readme): ...`, `test(sdk): ...`).

---

## 6. Security Vulnerability Disclosure

We take the security of pooled OAuth credentials, proxy isolation, and contributor guardrails seriously.

**Please do NOT report security vulnerabilities through public GitHub issues.**

If you discover a vulnerability involving:
- AES-256-GCM vault encryption or key derivation,
- Contributor OAuth scope escalation or WebSocket `colab_request` firewall bypass,
- Per-account HTTP/SOCKS5 proxy leak (direct egress fallback when a proxy is pinned),
- API key (`ch_sk_...`) authentication bypass or cross-group privilege escalation,

please report it privately via **GitHub Private Vulnerability Reporting** (`Security` -> `Report a vulnerability` tab on the repository) with reproduction steps and affected file paths. Maintainers will acknowledge receipt within **48 hours** and coordinate a patch and advisory credit prior to public disclosure.

---

## License

By contributing to ColabHive, you agree that your contributions will be licensed under the [Apache License 2.0](./LICENSE).
