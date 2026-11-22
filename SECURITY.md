# Security Policy

ColabHive manages Google OAuth2 credentials, Compute Unit (CU) budgets, per-account proxy tunnels, and remote Jupyter execution kernels. Security and strict isolation are first-class architectural invariants.

## Supported Versions

| Version | Supported |
| :--- | :---: |
| `1.0.x` | ✅ |
| `< 1.0.0` | ❌ |

---

## 1. Cryptographic Vault & Credential Storage Model

### At-Rest Encryption (`AES-256-GCM` + `HKDF-SHA256`)
- **Zero Plaintext Tokens in SQLite**: Every Google OAuth2 `refresh_token`, `access_token`, and proxy credential stored in `colabhive.db` is encrypted at rest using authenticated **`AES-256-GCM`** (`v1:` envelope with unique 96-bit CSPRNG nonces per encryption operation) before reaching SQLite.
- **Key Derivation & `.master.key` Lifecycle**:
  - At-rest encryption keys are derived via **`HKDF-SHA256`** (`salt = "colabhive-vault-salt-v1"`, `info = "colabhive-at-rest-aes-256-gcm"`).
  - In `--mode local`, if `COLABHIVE_MASTER_KEY` / `--master-key` is not explicitly provided, ColabHive atomically generates a 256-bit (64-hex-character) CSPRNG secret and persists it alongside the database as `<data-dir>/.master.key` with `0600` permissions (synced before rename, never overwriting an existing key).
  - In `--mode cloud`, ColabHive **refuses to boot** unless `COLABHIVE_MASTER_KEY` (or `--auto-admin-token`) is explicitly configured.
- **Zero Secret Leakage in API / Logs**: Struct serialization (`json:"-"`) strips encrypted and raw token fields from all REST `/api/v1/*`, WebSocket `/ws/events`, and MCP responses. Local CLI credential discovery (`keyringrc.cfg`, `application_default_credentials.json`) is strictly **read-only** and only runs upon explicit user action.

### Cross-Server `.chvault` Bundles (`PBKDF2-HMAC-SHA256`)
- Portable `.chvault` exports encrypt owner account credentials with **`AES-256-GCM`** keyed via **`PBKDF2-HMAC-SHA256` (`600,000` iterations)** and a random per-bundle salt.
- **Contributor Non-Exportability**: Contributed guest accounts (`IsContributor = true`) are **never** included in `.chvault` or JSON exports under any flag combination.

---

## 2. External Contributor Safety & Google Drive Firewall

When teammates or lab peers donate idle Colab quota through `/contribute/:invite_code`, ColabHive enforces defense-in-depth isolation:

1. **Least-Privilege OAuth Scopes**: Contributor OAuth authorization URLs request **only** Colaboratory runtime access (`colaboratory` + basic identity) and **strictly exclude** `drive`, `drive.readonly`, `drive.file`, and `cloud-platform` scopes.
2. **Jupyter `colab_request` Drive Firewall (`BlockDrivePropagation`)**: During kernel execution over ZMQ-over-WebSocket, ColabHive inspects every `iopub` `colab_request` frame. Any attempt to invoke `google.colab.drive.mount(...)` or `request_auth` (`dfs_ephemeral`) on a contributed session is **immediately intercepted and rejected** on the kernel `stdin` channel before any credential propagation can occur.
3. **Owner Account Overwrite Protection**: Neither `/contribute/:code` OAuth flows nor raw credential donations can overwrite or downgrade an existing pool Owner account, and mismatched `(refresh_token, access_token)` pairs are rejected during upstream verification.
4. **Hard Budget Guardrails & Instant Self-Service Revocation**:
   - Enforces personal CU reserve floors (`min_reserve_cu` + 15-minute accelerator runway margin), daily/total donation caps, GPU allowlists, and timezone-aware schedule windows.
   - Contributors receive a private `/my-contribution/:receipt_token` capability URL (stored only as a SHA-256 hash) allowing **1-click emergency revocation**, which drains active VMs, calls Google's `https://oauth2.googleapis.com/revoke` endpoint, and permanently wipes stored ciphertext upon confirmation.

---

## 3. Per-Account Proxy Egress Pinning

- When an account is bound to an `http://`, `https://`, or `socks5://` proxy, **100% of outbound traffic** for that account—OAuth2 code exchange, proactive token refresh, `/tun/m/ccu-info` quota polling, `/tun/m/assign` & `unassign`, and Jupyter WebSocket streams—traverses that pinned proxy.
- ColabHive **never** silently falls back to direct host egress if a pinned proxy is unreachable (`PROXY_ERROR` fails closed).

---

## 4. Production `--mode cloud` Security Checklist

Before exposing ColabHive on a shared lab server, VPS, or public domain, verify all items below:

- [ ] **Enable Hardened Cloud Mode**: Run with `--mode cloud` (`COLABHIVE_MODE=cloud`).
- [ ] **Set Distinct 256-Bit Secrets**: Generate independent secrets via `openssl rand -hex 32` for both `COLABHIVE_ADMIN_TOKEN` and `COLABHIVE_MASTER_KEY` (see [`.env.cloud.example`](./.env.cloud.example)). Never commit `.env.cloud` or `.master.key`.
- [ ] **Terminate TLS via Reverse Proxy**: Bind ColabHive to loopback (`COLABHIVE_BIND_ADDR=127.0.0.1`) behind Caddy, Nginx, or Cloudflare Tunnel with HTTPS/WSS enabled, and set `--trust-proxy` (`COLABHIVE_TRUST_PROXY=true`) only when behind a trusted reverse proxy that overwrites `X-Forwarded-For` / `X-Forwarded-Proto`.
- [ ] **Issue Scoped `ch_sk_...` Keys for AI Agents**: Never share `COLABHIVE_ADMIN_TOKEN` with coding agents or CI runners. Mint scoped `ch_sk_...` keys in **Keys & Proxies (`/cfg`)** restricted by Account Group, GPU allowlist, max concurrent VMs, and daily/total CU caps.
- [ ] **Single-Use WebSocket Tickets**: Keep ColabHive updated (`v1.0.0+`) so browser WebSocket connections authenticate via ephemeral single-use tickets (`POST /api/v1/ws-ticket`, 60s TTL) rather than long-lived query-string bearer tokens.
- [ ] **Restrict Data Volume Permissions**: Ensure `/data` (or `$COLABHIVE_DATA_DIR`) is owned by a non-root service user with `0700` directory permissions and `0600` on `colabhive.db` and `.master.key`.

---

## 5. Reporting a Vulnerability

**Please do NOT open a public GitHub Issue or Discussion for security vulnerabilities.**

If you discover a security issue—including vault cryptography flaws, authentication/authorization bypasses, WebSocket ticket issues, Contributor Drive Firewall bypasses, SSRF/proxy-pinning leaks, or `colabhive connect` file-write vulnerabilities—please report it privately:

1. Navigate to the [**GitHub Security Advisories — Report a Vulnerability**](https://github.com/hexwander/colabhive/security/advisories/new) page on this repository.
2. Include:
   - Affected component(s) and file paths (e.g., `internal/vault/`, `internal/server/`, `internal/runtime/`, `web/`),
   - Deployment mode (`--mode local` vs `--mode cloud`),
   - Minimal step-by-step reproduction or proof-of-concept (using mock servers—never real Google credentials),
   - Potential impact assessment.

### Response SLA
- **Acknowledgement**: Within **48 hours** of submission.
- **Triage & Remediation Plan**: Within **5 business days**.
- **Coordinated Disclosure**: We will work with you to verify the fix, publish a patched release + GitHub Security Advisory, and credit your discovery (unless you prefer anonymity).
