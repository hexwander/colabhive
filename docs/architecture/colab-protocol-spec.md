# ColabHive Protocol & Runtime Specification (`colab-protocol-spec.md`)

> **Status**: Normative Architecture & Wire-Protocol Specification  
> **Baseline Compatibility**: Official Google `google-colab-cli` (`v0.8.0`, Apache-2.0), `googlecolab/colab-vscode`, and `googlecolab/colab-mcp`  
> **Scope**: Google OAuth2 & Multi-Account Credential Portability, Colab Tunnel (`TFE`) & Control-Plane HTTP APIs, Jupyter REST + ZMQ-over-WebSocket (`v5.x`) Runtime Protocol, Anti-Zombie / Keepalive / Reconnect Engine, CLI Compatibility Matrix, and Dual-Mode MCP Server Tool Schemas (`stdio` + Streamable HTTP / SSE).

---

## 1. System Protocol Topology

```
┌──────────────────────────────────────────────────────────────────────────────────────┐
│                         Agents / Researchers / CI Pipelines                          │
│   colab / colabhive CLI   │   MCP Clients (stdio / HTTP+SSE)   │   ColabHive Web UI  │
└─────────────┬─────────────┴─────────────────┬──────────────────┴──────────┬──────────┘
              │                               │                             │
              ▼                               ▼                             ▼
┌──────────────────────────────────────────────────────────────────────────────────────┐
│                        ColabHive Multi-Account Control Plane                         │
│  ┌─────────────────────┐  ┌──────────────────────────┐  ┌─────────────────────────┐  │
│  │ Credential Vault &  │  │  Pool Scheduler & Quota  │  │   Watchdog & Reconnect  │  │
│  │ Importer/Exporter   │  │  Router (Waste-Zero/CU)  │  │  (15m Reaper / Zombie)  │  │
│  └──────────┬──────────┘  └────────────┬─────────────┘  └────────────┬────────────┘  │
│             └──────────────────────────┼─────────────────────────────┘               │
│                                        │ Per-Account Sticky Proxy (HTTP / SOCKS5)    │
└────────────────────────────────────────┼─────────────────────────────────────────────┘
                                         │
        ┌────────────────────────────────┼────────────────────────────────────┐
        ▼                                ▼                                    ▼
┌──────────────────────┐   ┌─────────────────────────────────┐   ┌──────────────────────────┐
│ Google OAuth2 / IAM  │   │ Colab Session / Tunnel Frontend │   │ Colab VM Runtime Proxy   │
│ oauth2.googleapis    │   │ colab.research.google.com/tun/m │   │ 8080-<ep>-c.*.colab.dev  │
│ accounts.google.com  │   │  • GET  /ccu-info               │   │  • /api/kernels (REST)   │
│ sdk.cloud.google.com │   │  • GET  /assignments            │   │  • /api/sessions (REST)  │
│                      │   │  • GET+POST /assign (XSRF)      │   │  • /api/contents (REST)  │
│                      │   │  • GET+POST /unassign/<ep>      │   │  • /api/kernels/*/channels│
│                      │   │  • GET  /<ep>/keep-alive/       │   │    (ZMQ-over-WebSocket)  │
│                      │   │  • GET+POST /credentials-prop.. │   │  • /colab/ssh (WS SSHv2) │
└──────────────────────┘   └─────────────────────────────────┘   └──────────────────────────┘
```

---

## 2. Authentication & Multi-Account Credential Portability

### 2.1 Official Public OAuth2 Client Configuration & Scopes

Google's official `google-colab-cli` (`v0.8.0`, `colab_cli/oauth_config.json`) authenticates using Google's Cloud SDK Installed-App OAuth2 client. Crucially, **this exact `client_id` is server-side allowlisted by Google for the remote copy-paste redirect URI (`https://sdk.cloud.google.com/applicationdefaultauthcode.html`)**; substituting an arbitrary custom GCP OAuth `client_id` with that redirect URI results in `400 redirect_uri_mismatch`.

#### Bundled Public OAuth2 Client (`colab_cli/oauth_config.json`)

```json
{
  "installed": {
    "client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
    "project_id": "colab-cli",
    "auth_uri": "https://accounts.google.com/o/oauth2/auth",
    "token_uri": "https://oauth2.googleapis.com/token",
    "auth_provider_x509_cert_url": "https://www.googleapis.com/oauth2/v1/certs",
    "client_secret": "d-FL95Q19q7MQmFpd7hHD0Ty",
    "redirect_uris": ["http://localhost"]
  }
}
```

#### Required OAuth2 Scopes (`PUBLIC_SCOPES`)

| Scope URI | Required By | Failure Symptom When Missing |
| :--- | :--- | :--- |
| `openid` | Google OIDC & `gcloud` ADC | OIDC `id_token` omitted; `gcloud` rejects scope list |
| `https://www.googleapis.com/auth/userinfo.profile` | Account display name & avatar (`ColabHive` UI) | Missing profile metadata on account cards |
| `https://www.googleapis.com/auth/userinfo.email` | **Mandatory**: `colab.research.google.com` session backend | `HTTP 401 Unauthorized` on `/tun/m/assignments` and `/tun/m/assign` |
| `https://www.googleapis.com/auth/cloud-platform` | **Mandatory** for `gcloud auth application-default login` | `gcloud` CLI refuses to mint ADC token |
| `https://www.googleapis.com/auth/colaboratory` | **Mandatory**: Colab Tunnel & `colab.pa.googleapis.com` APIs | `HTTP 403 Forbidden` / insufficient scope on runtime operations |
| `https://www.googleapis.com/auth/drive.file` | Optional / Default in `oauth2` mode | Drive file-scoped operations unavailable |

---

### 2.2 Interactive Copy-Paste Auth-Code Flow (Headless, Web UI & Contributor Onboarding)

Google blocked legacy out-of-band (`urn:ietf:wg:oauth:2.0:oob`) flows in 2022, and `http://localhost:<port>` loopback callbacks fail on remote servers, Docker containers, WSL2 bridges, and external contributor onboarding (where a teammate authorizes their idle Colab Pro+/Ultra account on their own laptop and hands the auth code to a shared ColabHive server).

ColabHive implements the **Cloud SDK Remote Auth-Code Flow** (`REMOTE_REDIRECT_URI` + `token_usage=remote` + PKCE `S256`):

#### Step 1 — Generate Authorization URL (`POST /api/v1/accounts/oauth/start`)

ColabHive generates a cryptographically random `state` (32 bytes URL-safe base64) and PKCE `code_verifier` (43–128 chars `[A-Za-z0-9-._~]`, `code_challenge = BASE64URL_NOPAD(SHA256(code_verifier))`), caches `(state, code_verifier, proxy_url, contributor_guardrails)` in memory/Redis with a `600s` TTL, and constructs:

```text
https://accounts.google.com/o/oauth2/auth
  ?response_type=code
  &client_id=764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com
  &redirect_uri=https%3A%2F%2Fsdk.cloud.google.com%2Fapplicationdefaultauthcode.html
  &scope=openid+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fuserinfo.profile+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fuserinfo.email+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcolaboratory+https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fdrive.file
  &state=<csrf_state_token>
  &prompt=consent
  &access_type=offline
  &token_usage=remote
  &code_challenge=<s256_challenge>
  &code_challenge_method=S256
```

* **`token_usage=remote`**: Essential Google consent parameter instructing `sdk.cloud.google.com/applicationdefaultauthcode.html` to render the copyable authorization code (`4/0A...`) in the browser.
* **`prompt=consent` & `access_type=offline`**: Forces Google to issue a persistent `refresh_token` (`1//0...`) even if the user previously authorized `colab-cli` or `gcloud` on another machine.

#### Step 2 — Normalize Pasted Input & Exchange Code (`POST /api/v1/accounts/oauth/exchange`)

Users or external contributors may paste **either**:
1. Raw authorization code: `4/0AVGzR1B...`
2. Full redirect URL copied from address bar: `https://sdk.cloud.google.com/applicationdefaultauthcode.html?state=...&code=4%2F0AVGzR1B...&scope=...` (or a `http://localhost:.../?code=...` URL if custom OAuth client loopback mode was used).

**Input Normalization Rule**: If `input.strip().startswith(("http://", "https://"))` or `"code="` appears in `input`, parse via `urllib.parse.parse_qs(urlparse(input).query)["code"][0]`; otherwise use `input.strip()`.

**Token Exchange HTTP Request** (routed via the account's configured HTTP/SOCKS5 proxy, if any):

```http
POST https://oauth2.googleapis.com/token HTTP/1.1
Host: oauth2.googleapis.com
Content-Type: application/x-www-form-urlencoded

code=4%2F0AVGzR1B...&client_id=764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com&client_secret=d-FL95Q19q7MQmFpd7hHD0Ty&redirect_uri=https%3A%2F%2Fsdk.cloud.google.com%2Fapplicationdefaultauthcode.html&grant_type=authorization_code&code_verifier=<pkce_code_verifier>
```

**Token Exchange Response (`200 OK`)**:

```json
{
  "access_token": "ya29.a0AeDClZ...",
  "expires_in": 3599,
  "refresh_token": "1//0gX9kL...",
  "scope": "openid https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/colaboratory https://www.googleapis.com/auth/drive.file https://www.googleapis.com/auth/cloud-platform",
  "token_type": "Bearer",
  "id_token": "eyJhbGciOiJSUzI1NiIs..."
}
```

#### Step 3 — Refreshing Access Tokens & Verifying Identity (`whoami` / `tokeninfo`)

* **Access Token Refresh (`POST https://oauth2.googleapis.com/token`)**:
  ```http
  POST https://oauth2.googleapis.com/token HTTP/1.1
  Content-Type: application/x-www-form-urlencoded

  client_id=764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com&client_secret=d-FL95Q19q7MQmFpd7hHD0Ty&refresh_token=1%2F%2F0gX9kL...&grant_type=refresh_token
  ```
  * Refresh proactively when `expires_at - now <= 300s` (5 minutes).
  * If Google returns `400 {"error": "invalid_grant"}`, transition the account state in ColabHive to `NEEDS_REAUTH` immediately and alert the dashboard (never enter an infinite retry loop).

* **Token Introspection (`tokeninfo`)**:
  ```http
  GET https://oauth2.googleapis.com/tokeninfo?access_token=ya29.a0AeDClZ... HTTP/1.1
  Host: oauth2.googleapis.com
  ```
  > **Protocol Quirk (`colab_cli/commands/utility.py`)**: Do **NOT** send an `Authorization: Bearer` header alongside `?access_token=...` on `oauth2.googleapis.com/tokeninfo`; send the query parameter alone.
  * Returns `{"azp": "...", "aud": "...", "sub": "109876543210...", "scope": "...", "exp": "...", "expires_in": "3598", "email": "researcher@gmail.com", "email_verified": "true"}`.
  * For display name & avatar (`name`, `picture`), decode the OIDC `id_token` JWT payload or call `GET https://www.googleapis.com/oauth2/v3/userinfo` with `Authorization: Bearer <access_token>`.

---

### 2.3 Exact Credential File Formats & Zero-Friction Portability Schemas

ColabHive supports lossless 2-way import and export across **four** credential storage formats so users can migrate seamlessly between WSL2 `colab` CLI, `gcloud` ADC, Windows/macOS/Linux hosts, and multi-server ColabHive clusters.

#### Format A: `keyrings.alt.file.PlaintextKeyring` (`keyringrc.cfg` + `keyring_pass.cfg`)

Used by `google-colab-cli` (`v0.8.0`) on headless Linux and WSL2 Ubuntu when D-Bus `SecretService` is unavailable or bypassed.

1. **Platform Discovery Paths (`keyring.util.platform_`)**:
   * **Linux / WSL2**:
     * Backend config: `${XDG_CONFIG_HOME:-~/.config}/python_keyring/keyringrc.cfg`
     * Password store: `${XDG_DATA_HOME:-~/.local/share}/python_keyring/keyring_pass.cfg` (file mode `0600`)
   * **Windows**:
     * Backend config: `%LOCALAPPDATA%\Python Keyring\keyringrc.cfg`
     * Password store: `%LOCALAPPDATA%\Python Keyring\keyring_pass.cfg`
   * **Environment Override**: `PYTHON_KEYRING_BACKEND=keyrings.alt.file.PlaintextKeyring`

2. **`keyringrc.cfg` Exact INI Schema**:
   ```ini
   [backend]
   default-keyring=keyrings.alt.file.PlaintextKeyring
   ```

3. **`keyring_pass.cfg` Exact Escaping & Base64 Schema (`keyrings/alt/escape.py` + `file_base.py`)**:
   * `colab_cli` calls `keyring.set_password("google-colab-cli", "oauth_token", creds.to_json())`.
   * **CRITICAL INI Escaping Rule**: `keyrings.alt.escape.escape()` allows **only** ASCII letters and digits (`[A-Za-z0-9]`). Every other UTF-8 byte — **including hyphens (`-` = `0x2D`) and underscores (`_` = `0x5F`)** — is escaped as `_%02X`:
     * Service `"google-colab-cli"` $\rightarrow$ section **`[google_2Dcolab_2Dcli]`**
     * Username `"oauth_token"` $\rightarrow$ escaped `"oauth_5Ftoken"` $\rightarrow$ `RawConfigParser` lowercases option keys on write to **`oauth_5ftoken`**!
   * **Payload Encoding**: `'\n' + base64.encodebytes(token_json.encode('utf-8')).decode('ascii')` (written by `RawConfigParser` with continuation-line tab indentation).

   **Exact `keyring_pass.cfg` Wire File Example**:
   ```ini
   [google_2Dcolab_2Dcli]
   oauth_5ftoken = 
   	eyJ0b2tlbiI6ICJ5YTI5LmEwLi4uIiwgInJlZnJlc2hfdG9rZW4iOiAiMS8vMGdYLi4uIiwgInRv
   	a2VuX3VyaSI6ICJodHRwczovL29hdXRoMi5nb29nbGVhcGlzLmNvbS90b2tlbiIsICJjbGllbnRf
   	aWQiOiAiNzY0MDg2MDUxODUwLTZxcjRwNmdwaTZobjUwNnB0OGVqdXE4M2RpMzQxaHVyLmFwcHMu
   	Z29vZ2xldXNlcmNvbnRlbnQuY29tIiwgImNsaWVudF9zZWNyZXQiOiAiZC1GTDk1UTE5cTdNUW1G
   	cGQ3aEhEMFR5IiwgInNjb3BlcyI6IFsib3BlbmlkIiwgImh0dHBzOi8vd3d3Lmdvb2dsZWFwaXMu
   	Y29tL2F1dGgvdXNlcmluZm8ucHJvZmlsZSIsICJodHRwczovL3d3dy5nb29nbGVhcGlzLmNvbS9h
   	dXRoL3VzZXJpbmZvLmVtYWlsIiwgImh0dHBzOi8vd3d3Lmdvb2dsZWFwaXMuY29tL2F1dGgvY2xv
   	dWQtcGxhdGZvcm0iLCAiaHR0cHM6Ly93d3cuZ29vZ2xlYXBpcy5jb20vYXV0aC9jb2xhYm9yYXRv
   	cnkiLCAiaHR0cHM6Ly93d3cuZ29vZ2xlYXBpcy5jb20vYXV0aC9kcml2ZS5maWxlIl0sICJ1bml2
   	ZXJzZV9kb21haW4iOiAiZ29vZ2xlYXBpcy5jb20iLCAiYWNjb3VudCI6ICIiLCAiZXhwaXJ5Ijog
   	IjIwMjYtMTEtMjFUMDg6MTU6MDAuMTIzNDU2WiJ9
   ```

   **Reference Python Codec for ColabHive Importer/Exporter**:
   ```python
   import base64, configparser, json

   SECTION = "google_2Dcolab_2Dcli"
   OPTION = "oauth_5ftoken"  # RawConfigParser case-insensitively matches oauth_5Ftoken

   def parse_plaintext_keyring(cfg_text: str) -> dict:
       cp = configparser.RawConfigParser()
       cp.read_string(cfg_text)
       b64_raw = cp.get(SECTION, OPTION)
       return json.loads(base64.decodebytes(b64_raw.encode("ascii")).decode("utf-8"))

   def dump_plaintext_keyring(token_dict: dict) -> str:
       cp = configparser.RawConfigParser()
       cp.add_section(SECTION)
       b64 = "\n" + base64.encodebytes(json.dumps(token_dict).encode("utf-8")).decode("ascii")
       cp.set(SECTION, OPTION, b64)
       from io import StringIO
       buf = StringIO()
       cp.write(buf)
       return buf.getvalue()
   ```

#### Format B: Authorized User Token JSON (`Credentials.to_json()` & Legacy `~/.config/colab-cli/token.json`)

```json
{
  "token": "ya29.a0AeDClZ...",
  "refresh_token": "1//0gX9kL...",
  "token_uri": "https://oauth2.googleapis.com/token",
  "client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
  "client_secret": "d-FL95Q19q7MQmFpd7hHD0Ty",
  "scopes": [
    "openid",
    "https://www.googleapis.com/auth/userinfo.profile",
    "https://www.googleapis.com/auth/userinfo.email",
    "https://www.googleapis.com/auth/cloud-platform",
    "https://www.googleapis.com/auth/colaboratory",
    "https://www.googleapis.com/auth/drive.file"
  ],
  "universe_domain": "googleapis.com",
  "account": "",
  "expiry": "2026-11-21T08:15:00.123456Z"
}
```

#### Format C: `gcloud` Application Default Credentials (`application_default_credentials.json`)

* **Paths**: `~/.config/gcloud/application_default_credentials.json` (Linux/macOS), `%APPDATA%\gcloud\application_default_credentials.json` (Windows), or `$GOOGLE_APPLICATION_CREDENTIALS`.
* **Exact JSON Schema**:
  ```json
  {
    "account": "",
    "client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
    "client_secret": "d-FL95Q19q7MQmFpd7hHD0Ty",
    "quota_project_id": "colab-cli",
    "refresh_token": "1//0gX9kL...",
    "type": "authorized_user",
    "universe_domain": "googleapis.com"
  }
  ```
* **Latency Avoidance Note**: Never invoke `google.auth.default()` unpatched on ADC files missing `quota_project_id` (`google.auth._cloud_sdk.get_project_id()` spawns a blocking `gcloud` subprocess adding 2–13s latency). Always instantiate `google.oauth2.credentials.Credentials` or direct `httpx` OAuth2 token refresh directly from `client_id`, `client_secret`, and `refresh_token`.

#### Format D: ColabHive Multi-Account Portable Bundle (`colabhive.credential_bundle` v1)

Designed for migrating an entire multi-account compute pool across servers (`colabhive pool export` / `colabhive pool import` or Web UI drag-and-drop), supporting both plaintext JSON and passphrase-protected `AES-256-GCM` (`PBKDF2-HMAC-SHA256`, 600,000 iterations) encrypted envelopes:

```json
{
  "schema": "colabhive.credential_bundle",
  "version": "1.0",
  "exported_at": "2026-11-21T07:30:00Z",
  "encrypted": false,
  "accounts": [
    {
      "email": "lab-ultra-01@gmail.com",
      "label": "Lab Ultra #1 (Blackwell G4)",
      "subscription_tier": "PRO_PLUS",
      "tags": ["g4", "a100", "lab-shared"],
      "proxy_url": "socks5://user:pass@10.0.0.12:1080",
      "oauth": {
        "client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
        "client_secret": "d-FL95Q19q7MQmFpd7hHD0Ty",
        "token_uri": "https://oauth2.googleapis.com/token",
        "refresh_token": "1//0gX9kL...",
        "scopes": [
          "openid",
          "https://www.googleapis.com/auth/userinfo.profile",
          "https://www.googleapis.com/auth/userinfo.email",
          "https://www.googleapis.com/auth/cloud-platform",
          "https://www.googleapis.com/auth/colaboratory",
          "https://www.googleapis.com/auth/drive.file"
        ]
      },
      "guardrails": {
        "enabled": true,
        "priority": 10,
        "reserve_cu_floor": 50.0,
        "max_cu_contribution": null,
        "max_concurrent_sessions": 5,
        "allowed_accelerators": ["G4", "A100", "L4", "T4", "V6E1", "V5E1", "NONE"]
      }
    }
  ]
}
```
When `"encrypted": true`, `"accounts"` is replaced by `"kdf": {"alg": "PBKDF2-HMAC-SHA256", "iterations": 600000, "salt_b64": "..."}` and `"cipher": {"alg": "AES-256-GCM", "nonce_b64": "...", "ciphertext_b64": "..."}`.

---

## 3. Colab Backend HTTP APIs (`colab.research.google.com` & `colab.pa.googleapis.com`)

### 3.1 Global Wire Conventions, Headers & XSSI Prefix

| Property | Exact Specification |
| :--- | :--- |
| **Session / Tunnel Domain (`Prod.domain`)** | `https://colab.research.google.com` (base path `/tun/m`) |
| **Control-Plane Domain (`Prod.api`)** | `https://colab.pa.googleapis.com` |
| **Mandatory Query Parameter** | `authuser=0` on **all** `colab.research.google.com` requests |
| **Mandatory Request Headers** | `Authorization: Bearer <oauth2_access_token>`<br>`Accept: application/json`<br>`X-Colab-Client-Agent: colab-cli` |
| **Tunnel Ping Header** | `X-Colab-Tunnel: Google` (required on `/tun/m/<endpoint>/keep-alive/`) |
| **XSRF Mutation Header** | `X-Goog-Colab-Token: <xsrf_token>` (required on `POST /tun/m/assign`, `POST /tun/m/unassign/<ep>`, and `POST /tun/m/credentials-propagation/<ep>`) |
| **XSSI Response Prefix** | Every JSON response from `colab.research.google.com/tun/m/*` begins with the 5-byte prefix **`)]}'\n`** (`0x29 0x5d 0x7d 0x27 0x0a`). Strip `v[5:]` if `v.startswith(")]}'\n")` before `json.loads()`. |

---

### 3.2 Compute Unit Balance, Burn Rate & Accelerator Hardware Matrix (`ccu-info` & `v1/user-info`)

#### Primary Endpoint: `GET /tun/m/ccu-info`

Works universally with Cloud SDK OAuth2 / ADC user tokens without requiring a GCP project quota binding:

```http
GET /tun/m/ccu-info?authuser=0 HTTP/1.1
Host: colab.research.google.com
Authorization: Bearer ya29.a0AeDClZ...
Accept: application/json
X-Colab-Client-Agent: colab-cli
```

**Response (`200 OK`, after stripping `)]}'\n`)**:

```json
{
  "currentBalance": 482.54,
  "consumptionRateHourly": 9.66,
  "assignmentsCount": 1,
  "eligibleGpus": ["T4", "L4", "G4", "A100"],
  "eligibleTpus": ["V5E1", "V6E1"],
  "freeCcuQuotaInfo": {
    "remainingTokens": 1500000,
    "nextRefillTimestampSec": 1763769600
  }
}
```
* **Guaranteed Core Fields** (`colab_cli/consumption.py`):
  * `currentBalance` (`float`): Remaining paid Compute Units (CU) on the account.
  * `consumptionRateHourly` (`float`): Live aggregate burn rate (`CU/hr`) across all currently assigned runtimes on this account.
  * `assignmentsCount` (`int`): Count of active server-side VM assignments on this account.
* **Extended Telemetry Fields** (present depending on tier/rollout; parse with `extra="ignore"` / `Optional`):
  * `eligibleGpus` (`list[str]`), `eligibleTpus` (`list[str]`), `freeCcuQuotaInfo` (`dict`).

#### Secondary Endpoint: `GET https://colab.pa.googleapis.com/v1/user-info?get_ccu_consumption_info=true`

Used by `googlecolab/colab-vscode` to retrieve `subscriptionTier` (`SUBSCRIPTION_TIER_NONE`, `SUBSCRIPTION_TIER_PRO`, `SUBSCRIPTION_TIER_PRO_PLUS`), `paidComputeUnitsBalance`, `consumptionRateHourly`, `assignmentsCount`, and `eligibleAccelerators`.
* **Fallback Rule**: Calling `colab.pa.googleapis.com` with non-whitelisted OAuth client projects returns `403 PERMISSION_DENIED (USER_PROJECT_DENIED)`. ColabHive **always uses `GET https://colab.research.google.com/tun/m/ccu-info?authuser=0` as primary** and dynamically calibrates empirical per-tier burn rates whenever `assignmentsCount == 1`!

#### Hardware Accelerator Catalog, Wire Enums & Empirical Burn Rates

| CLI Flag | `variant` (Query / Post Int) | `accelerator` (Wire Enum) | Hardware Spec & Architecture | `--high-mem` (`shape=hm`) | Reference Burn Rate (`CU/hr`) | Optimal Workload Routing |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `--gpu G4` | `"GPU"` / `1` | `"G4"` | **NVIDIA RTX PRO 6000 Blackwell** (`96 GB` GDDR7, `SM 12.0`) | Supported (`shape=hm`) | **`~9.66 CU/hr`** | Mandatory for `>=26B` BF16 models (`48–85 GB` VRAM); highest VRAM tier |
| `--gpu H100` | `"GPU"` / `1` | `"H100"` | **NVIDIA H100 SXM** (`80 GB` HBM3, `SM 9.0`) | Supported (`shape=hm`) | **`~12.50 CU/hr`** | Hopper FP8 / FlashAttention-3; often entitlement/capacity-gated (`HTTP 400/503`) |
| `--gpu A100` | `"GPU"` / `1` | `"A100"` | **NVIDIA A100-SXM4** (`40 GB` / `80 GB`, `SM 8.0`) | Supported (`shape=hm`) | **`~8.88 CU/hr`** | Best throughput/CU for `<=14B` BF16 models; full prebuilt `SM 8.0` Triton/CUDA wheels |
| `--gpu L4` | `"GPU"` / `1` | `"L4"` | **NVIDIA L4** (`24 GB` GDDR6, `SM 8.9` Ada) | **Omit** (Single High-RAM shape) | **`~4.52 CU/hr`** | `7B–9B` inference, LoRA fine-tuning, vision/audio pipelines |
| `--gpu T4` | `"GPU"` / `1` | `"T4"` | **NVIDIA Tesla T4** (`16 GB` GDDR6, `SM 7.5`) | Supported (`shape=hm`) | **`~1.96 CU/hr`** (`0.0` Free) | Light `<=4B` FP16 / INT4 inference, unit tests, Free-tier opportunistic pool |
| `--tpu v6e1` | `"TPU"` / `2` | `"V6E1"` | **Cloud TPU v6e-1 Trillium** (`1 chip`, `32 GB` HBM) | **Omit** (Single shape) | **`~5.30 CU/hr`** | JAX / XLA / PyTorch-XLA training & evaluation |
| `--tpu v5e1` | `"TPU"` / `2` | `"V5E1"` | **Cloud TPU v5e-1** (`1 chip`, `16 GB` HBM) | **Omit** (Single shape) | **`~3.85 CU/hr`** | Cost-efficient JAX / XLA workloads |
| *(omitted)* | `"DEFAULT"` / `0` | `"NONE"` | **x86_64 Cloud CPU VM** | Supported (`shape=hm`) | **`~0.74 CU/hr`** (`0.0` Free) | Data preprocessing, evaluation aggregation, CPU benchmarks |

> **Shape Omission Rule (`HIGH_MEM_ONLY_ACCELERATORS`)**: `L4`, `V5E1`, and `V6E1` only exist in a single High-RAM machine shape. `resolve_assign_shape()` **must omit** `shape=hm` from `/tun/m/assign` query params for these three accelerators even if `--high-mem` was requested.

---

### 3.3 Listing Active Assignments & Proxy Token Refresh (`GET /tun/m/assignments`)

```http
GET /tun/m/assignments?authuser=0 HTTP/1.1
Host: colab.research.google.com
Authorization: Bearer ya29.a0AeDClZ...
Accept: application/json
X-Colab-Client-Agent: colab-cli
```

**Response (`200 OK`, after stripping `)]}'\n`)**:

```json
{
  "assignments": [
    {
      "accelerator": "G4",
      "endpoint": "gpu-g4-s-2k8m9p1x4z7q",
      "variant": 1,
      "machineShape": 1,
      "runtimeProxyInfo": {
        "token": "ChAKDm...",
        "tokenExpiresInSeconds": 3600,
        "url": "https://8080-gpu-g4-s-2k8m9p1x4z7q-c.us-central1-a.prod.colab.dev"
      }
    }
  ]
}
```
* **Integer Wire Enums in Response**:
  * `variant`: `0` = `DEFAULT` (CPU), `1` = `GPU`, `2` = `TPU`
  * `machineShape`: `0` = `STANDARD` ("Standard"), `1` = `HIGH_RAM` ("High-RAM")
* **Proxy Token Auto-Refresh (`TOKEN_REFRESH_MARGIN = 300s`)**:
  * `runtimeProxyInfo.token` is a short-lived (~1 hour) capability token scoping access to `runtimeProxyInfo.url`.
  * Before any runtime operation (or every 45 minutes in ColabHive's background daemon), if `token_expires_at - now <= 300s`, call `GET /tun/m/assignments?authuser=0` and update `(token, url, token_expires_at)` for matching `endpoint`. If `endpoint` is no longer in `assignments`, the VM was idle-pruned or preempted by Google — mark session `LOST/PRUNED` immediately.

---

### 3.4 Provisioning Runtimes (`GET` + `POST /tun/m/assign`) & Auto-Fallback Engine

#### Notebook Hash (`nbh`) Encoding (`uuid_to_web_safe_base64`)

Every session allocation generates a fresh UUIDv4 (`36` chars) and transforms hyphens to underscores with `8` trailing dots to form a **44-character web-safe notebook hash**:

```python
def uuid_to_web_safe_base64(u: uuid.UUID) -> str:
    s = str(u)                       # len == 36, e.g. "f47ac10b-58cc-4372-a567-0e02b2c3d479"
    return s.replace("-", "_") + ("." * (44 - len(s)))
    # -> "f47ac10b_58cc_4372_a567_0e02b2c3d479........"
```

#### Step 1 — Pre-Flight / XSRF Fetch (`GET /tun/m/assign`)

```http
GET /tun/m/assign?nbh=f47ac10b_58cc_4372_a567_0e02b2c3d479........&variant=GPU&accelerator=G4&shape=hm&authuser=0 HTTP/1.1
Host: colab.research.google.com
Authorization: Bearer ya29.a0AeDClZ...
Accept: application/json
X-Colab-Client-Agent: colab-cli
```

Returns **one of two union schemas** (after stripping `)]}'\n`):
1. **`GetAssignmentResponse` (No VM assigned yet to `nbh` — XSRF token issued)**:
   ```json
   {
     "acc": "G4",
     "nbh": "f47ac10b_58cc_4372_a567_0e02b2c3d479........",
     "token": "AJv9s8_xsrf_assign_token...",
     "variant": "GPU"
   }
   ```
   > **Silent A100 Fallback Gotcha**: If an invalid/unknown `accelerator` string is sent to `/tun/m/assign`, the Colab backend silently normalizes `"acc": "A100"`. Always validate `accelerator` against the strict enum (`NONE, T4, L4, G4, A100, H100, V5E1, V6E1`) before calling `GET /tun/m/assign` AND assert `resp["acc"] == requested_accelerator`!

2. **`Assignment` (Already assigned — e.g. during timeout reconciliation retry)**:
   ```json
   {
     "endpoint": "gpu-g4-s-2k8m9p1x4z7q",
     "runtimeProxyInfo": {
       "token": "ChAKDm...",
       "tokenExpiresInSeconds": 3600,
       "url": "https://8080-gpu-g4-s-2k8m9p1x4z7q-c.us-central1-a.prod.colab.dev"
     }
   }
   ```

#### Step 2 — Commit Allocation (`POST /tun/m/assign`)

```http
POST /tun/m/assign?nbh=f47ac10b_58cc_4372_a567_0e02b2c3d479........&variant=GPU&accelerator=G4&shape=hm&authuser=0 HTTP/1.1
Host: colab.research.google.com
Authorization: Bearer ya29.a0AeDClZ...
Accept: application/json
X-Colab-Client-Agent: colab-cli
X-Goog-Colab-Token: AJv9s8_xsrf_assign_token...
Content-Length: 0
```

**Response (`200 OK`, `PostAssignmentResponse`, after stripping `)]}'\n`)**:

```json
{
  "accelerator": "G4",
  "endpoint": "gpu-g4-s-2k8m9p1x4z7q",
  "variant": 1,
  "runtimeProxyInfo": {
    "token": "ChAKDm...",
    "tokenExpiresInSeconds": 3600,
    "url": "https://8080-gpu-g4-s-2k8m9p1x4z7q-c.us-central1-a.prod.colab.dev"
  }
}
```

#### HTTP Error Semantics, Reconciliation Retry & Pool Failover Matrix

| HTTP Status | Meaning on Colab Backend | Single-Account CLI Behavior (`v0.8.0`) | ColabHive Multi-Account Pool Router Behavior |
| :--- | :--- | :--- | :--- |
| **`400 Bad Request`** | Account lacks entitlement or quota for requested `accelerator` (e.g. `H100` on standard Pro+, `A100` on Free) | Exits code `1` with accelerator rejection hint | Mark `(account_id, accelerator)` **ineligible** (`cooldown = 30m`); immediately try next eligible account in pool, then step down `Auto-GPU-Tier-Fallback` chain (`H100 -> G4 -> A100 -> L4 -> T4 -> CPU`). |
| **`412 Precondition Failed`** | `TooManyAssignmentsError`: max concurrent VMs reached on this account or temporary usage cap | Exits code `1` (`Allocation refused (precondition failed)`) | Mark account `AT_SESSION_LIMIT` while `assignmentsCount` unchanged; immediately failover to next healthy account in pool! |
| **`502 / 503 / 504` or `ReadTimeout`** | Regional accelerator capacity exhaustion or slow VM boot | Exponential backoff (`10s * 2^attempt`, 3 retries); calls `GET /tun/m/assign` with **same `nbh`** first to reconcile uncertain POST | Reconcile `GET /tun/m/assign` with **same `nbh`** once; if not allocated and `503` persists, failover to another account or fallback GPU tier without double-billing! |
| **`401 / 403`** | Expired/revoked OAuth token or missing `userinfo.email` / `colaboratory` scope | Exits code `1` | Attempt single OAuth token refresh; if still `401/403`, mark account `AUTH_ERROR` and route to next account. |

> **Mandatory Post-Assign Warmup**: Immediately after `POST /tun/m/assign` succeeds, wait `3–5s` (or retry `POST <proxy_url>/api/kernels` with `2s, 4s, 8s` backoff on connection/timeout) while the container reverse proxy (`*.prod.colab.dev`) finishes initializing.

---

### 3.5 Releasing Runtimes (`GET` + `POST /tun/m/unassign/<endpoint>`)

1. **Fetch Unassign XSRF Token**:
   ```http
   GET /tun/m/unassign/gpu-g4-s-2k8m9p1x4z7q?authuser=0 HTTP/1.1
   Host: colab.research.google.com
   Authorization: Bearer ya29.a0AeDClZ...
   Accept: application/json
   X-Colab-Client-Agent: colab-cli
   ```
   Response (after stripping `)]}'\n`): `{"token": "AJv9s8_xsrf_unassign_token..."}`

2. **Commit Unassign**:
   ```http
   POST /tun/m/unassign/gpu-g4-s-2k8m9p1x4z7q?authuser=0 HTTP/1.1
   Host: colab.research.google.com
   Authorization: Bearer ya29.a0AeDClZ...
   Accept: application/json
   X-Colab-Client-Agent: colab-cli
   X-Goog-Colab-Token: AJv9s8_xsrf_unassign_token...
   Content-Length: 0
   ```
   Response: `200 OK` (`{}` or empty after stripping `)]}'\n`).

3. **Zero-Leak Teardown Sequence**:
   Always execute: (a) best-effort `DELETE <proxy_url>/api/kernels/<kernel_id>` (`timeout=10s`), (b) `GET + POST /tun/m/unassign/<endpoint>` wrapped in hard `45s` timeout + 2 retries, (c) verify removal via `GET /tun/m/assignments` on background sweep so hung WebSockets never leak `~9.66 CU/hr` GPUs.

---

### 3.6 Keepalive, 15-Minute Idle Reaper, 35–60 Min ZMQ Drop & Anti-Zombie Engine

Colab enforces **three distinct liveness hazards** that naive clients fail on:

#### Hazard 1 — Tunnel Idle Timeout $\rightarrow$ TFE HTTP Keep-Alive (`60s` cadence)

```http
GET /tun/m/gpu-g4-s-2k8m9p1x4z7q/keep-alive/?authuser=0 HTTP/1.1
Host: colab.research.google.com
Authorization: Bearer ya29.a0AeDClZ...
X-Colab-Tunnel: Google
X-Colab-Client-Agent: colab-cli
Accept: application/json
```
Updates `LastActiveTime` in Google's Tunnel Frontend (`TFE`) while an Agent/CLI holds an active session lease.

#### Hazard 2 — 15-Minute Idle Kernel Reaper & 35–60 Minute ZMQ WebSocket Drop

* **15-Min Idle Reaper**: Even if `/keep-alive/` is pinged, background `subprocess.Popen` processes inside a Colab VM get reaped if no Jupyter kernel cell execution occurs for ~15 minutes.
* **35–60 Min WebSocket Drop**: Long-running single `execute_request` WebSocket streams over `*.prod.colab.dev` frequently drop around 35–60 minutes (`SSLEOFError`, `503`, `Connection was lost`).
* **ColabHive Background Job Protocol**:
  1. Write runner metadata under `/content/.colabhive/jobs/<job_id>/`: `worker.pid`, `worker.log`, `progress.jsonl`, and `exit.status`.
  2. Spawn the heavy Python command via `subprocess.Popen(..., start_new_session=True)` in a fast launcher snippet that performs a **mandatory 8-second startup self-check** before returning `pid`.
  3. Run a **Reconnectable Poll & Keepalive Loop** on the ColabHive server: every `8–15s` (or inside `1800s` bounded streaming slices with automatic proxy-token refresh and WebSocket reconnect), execute a lightweight kernel probe cell that:
     * Keeps the Jupyter kernel `busy`/`active` (defeating the 15-min idle reaper),
     * Checks `/proc/{pid}/stat` anti-zombie liveness,
     * Reads incremental byte deltas `[offset:]` from `worker.log` and `progress.jsonl`, and
     * Samples `nvidia-smi --query-gpu=memory.used,memory.total,utilization.gpu,temperature.gpu --format=csv,noheader,nounits`.

#### Hazard 3 — The Linux Zombie (`Z`) CU-Drain Trap (`/proc/{pid}/stat` Mandatory Check)

> **CRITICAL**: When a `subprocess.Popen` child spawned inside a persistent IPython kernel crashes or exits without being `wait()`ed by the parent kernel object, Linux retains it as a **zombie (`Z` state)** in the process table. Calling `os.kill(pid, 0)` on a zombie **returns success (`None`)** — causing naive pollers to think the crashed training job is still running and silently burn `9.66 CU/hr` for hours!

**Normative Anti-Zombie Probe Executed Inside the Kernel**:

```python
import os, json

def probe_worker(pid: int, log_path: str, log_offset: int) -> dict:
    state_code = "MISSING"
    alive = False
    stat_path = f"/proc/{pid}/stat"
    if os.path.exists(stat_path):
        try:
            with open(stat_path, "r", encoding="utf-8", errors="replace") as f:
                raw = f.read()
            # Process comm name in (...) may contain spaces or ')'; rfind(')') is mandatory!
            rparen = raw.rfind(")")
            state_code = raw[rparen + 2] if rparen != -1 and len(raw) > rparen + 2 else "?"
            alive = state_code not in ("Z", "X", "x")
        except OSError:
            alive = False

    new_log = ""
    new_offset = log_offset
    if os.path.exists(log_path):
        with open(log_path, "rb") as lf:
            lf.seek(log_offset)
            chunk = lf.read(65536)
            new_offset = lf.tell()
            new_log = chunk.decode("utf-8", errors="replace")

    fatal_markers = (
        "Traceback (most recent call last):",
        "ModuleNotFoundError:",
        "ImportError:",
        "SyntaxError:",
        "torch.OutOfMemoryError:",
        "CUDA out of memory",
    )
    has_fatal_trace = any(m in new_log for m in fatal_markers)
    return {
        "pid": pid,
        "state": state_code,
        "alive": alive,
        "new_offset": new_offset,
        "log_delta": new_log,
        "has_fatal_trace": has_fatal_trace,
    }
```
* **Mandatory 3–5 Minute Post-Launch Watchdog**: Every background job registers an automatic `240s` post-launch check verifying forward progress (`new_offset > startup_offset` or first `progress.jsonl` record emitted).

---

### 3.7 Browser Attachment URL (`colab url`), WebSocket SSH (`/colab/ssh`), and Drive Mounting

#### 1. Attaching Browser UI to an Active Headless Session (`colab url`)

Opening a standard Colab URL allocates a *second* VM. To attach the Colab Web UI directly to an existing ColabHive/CLI VM without calling `/tun/m/assign`, emit **both** `?dbu=` (fully URL-encoded path with `safe=""`) and `#datalabBackendUrl=` (raw unencoded full URL matching page origin):

```text
https://colab.research.google.com/notebooks/empty.ipynb?dbu=%2Ftun%2Fm%2Fgpu-g4-s-2k8m9p1x4z7q#datalabBackendUrl=https://colab.research.google.com/tun/m/gpu-g4-s-2k8m9p1x4z7q
```

#### 2. WebSocket-Bridged SSHv2 (`/colab/ssh`)

* **WebSocket Upgrade URL**:
  ```text
  wss://8080-<endpoint>-c.<region>.prod.colab.dev/colab/ssh?colab-runtime-proxy-token=<proxy_token>&colab-client-agent=colab-cli&colab-client-agent=ssh-mode-<interactive|proxy|cmd>
  ```
* **Upgrade Headers**:
  * `X-Colab-Ssh-Pubkey: <openssh_public_key_line>` — **Accepted key types**: `ssh-ed25519`, `ecdsa-sha2-nistp256`, `ecdsa-sha2-nistp384`, `ecdsa-sha2-nistp521`. **`ssh-rsa` is rejected with `HTTP 400 unsupported key type`!**
  * `X-Colab-Client-Agent: colab-cli`
* **Wire Framing**: Raw SSHv2 binary byte stream over WebSocket `OPCODE_BINARY` frames bridged to `ssh -o ProxyCommand=... -o StrictHostKeyChecking=no root@colab-runtime`.
* **Handshake Error Codes**:
  * `400`: `missing pubkey` or `unsupported key type` (`ssh-rsa`)
  * `401`: Invalid/expired `colab-runtime-proxy-token`
  * `404`: Runtime created without `/colab/ssh` support
  * `429`: Concurrent SSH session limit reached (`{"error": "already-active-session", "limit": 5}`)
  * `502`: In-VM `sshd` unreachable

#### 3. Google Drive Ephemeral Mount (`credentials-propagation`)

When `from google.colab import drive; drive.mount('/content/drive')` runs with `allow_stdin=True`, the kernel emits a custom WebSocket frame (`msg_type == "colab_request"`, `content.request.authType == "dfs_ephemeral"`, `metadata.colab_msg_id == "<id>"`):
1. `GET https://colab.research.google.com/tun/m/credentials-propagation/<endpoint>?authuser=0&authtype=dfs_ephemeral&version=2&dryrun=true&propagate=true&record=false` $\rightarrow$ XSSI-stripped `{"token": "<xsrf>"}`.
2. `POST` same URL (`dryrun=true`), header `x-goog-colab-token: <xsrf>`, ` multipart/form-data` `file_id="empty.ipynb"` $\rightarrow$ returns `{"success": true}` or `{"success": false, "unauthorized_redirect_uri": "https://accounts.google.com/..."}`.
3. Once authorized, `POST` with `dryrun=false` $\rightarrow$ `200 OK` $\rightarrow$ send Jupyter `input_reply` on `stdin` channel with `parent_header = colab_request.header` and `content = {"value": {"type": "colab_reply", "colab_msg_id": "<id>"}}`.

---

## 4. Jupyter Kernel & File Protocol (`*.prod.colab.dev`)

### 4.1 Runtime Proxy Authentication

Every request to `<proxy_url>` (`https://8080-<endpoint>-c.<region>.prod.colab.dev`) authenticates with `runtimeProxyInfo.token` (**NOT** the Google OAuth2 bearer token):

* **HTTP Headers (Kernels & Sessions REST + WebSocket Upgrade)**:
  ```http
  X-Colab-Runtime-Proxy-Token: <runtime_proxy_token>
  X-Colab-Client-Agent: colab-cli
  Authorization: Bearer <runtime_proxy_token>
  Accept: application/json
  ```
* **Query Parameters**:
  * `/api/contents/*`: `?authuser=0&colab-runtime-proxy-token=<runtime_proxy_token>`
  * `/api/kernels/<kernel_id>/channels`: `?session_id=<client_session_uuid>&token=<runtime_proxy_token>&colab-runtime-proxy-token=<runtime_proxy_token>`

---

### 4.2 Jupyter REST Endpoints (`/api/kernels`, `/api/sessions`, `/api/contents`)

| Method & Path | Query / JSON Request Body | Response (`200` / `201` / `204`) & Semantics |
| :--- | :--- | :--- |
| `GET /api/kernels` | — | `[{"id": "9f8e...", "name": "python3", "last_activity": "2026-11-21T07:20:00.000000Z", "execution_state": "idle", "connections": 1}]` |
| `POST /api/kernels` | `{"name": "python3", "path": null}` | `201 Created` $\rightarrow$ Kernel model (`id`, `name`, `execution_state`). Cache `kernel_id` in session state so subsequent calls reattach! |
| `GET /api/kernels/{id}` | — | Kernel model, or `404 Not Found` if kernel died/restarted. |
| `POST /api/kernels/{id}/interrupt` | — | `204 No Content` — sends `SIGINT` (`KeyboardInterrupt`) to running execution without losing in-RAM variables. |
| `POST /api/kernels/{id}/restart` | — | `200 OK` — resets Python interpreter RAM/VRAM while keeping VM filesystem (`/content`, `/root/.cache/huggingface`) intact! |
| `DELETE /api/kernels/{id}` | — | `204 No Content` — terminates kernel process (only invoke on `stop` / teardown; **never** on normal CLI/MCP disconnect!). |
| `GET /api/sessions` | — | Lists active notebook-to-kernel session bindings (`[{"id": "...", "path": "empty.ipynb", "type": "notebook", "kernel": {"id": "..."}}]`). |
| `POST /api/sessions` | `{"path": "empty.ipynb", "name": "", "type": "notebook", "kernel": {"name": "python3", "id": "<optional_existing_id>"}}` | Binds notebook path to kernel so `colab url` Web UI and headless CLI/MCP attach to the **same** live kernel! |
| `GET /api/contents/{path}` | `?authuser=0&colab-runtime-proxy-token=<tok>` (add `&content=1` for file download; omit for dir listing; `path` relative to `/`, e.g. `content/data.json`) | Directory: `{"type": "directory", "content": [{"name": "sample_data", "path": "content/sample_data", "type": "directory"}, ...]}`<br>File (`content=1`): `{"name": "out.jsonl", "path": "content/out.jsonl", "type": "file", "format": "text"|"base64"|"json", "content": "..."}` |
| `PUT /api/contents/{path}` | `{"name": "train.py", "path": "content/train.py", "type": "file", "format": "base64", "content": "<b64>", "chunk": 1}` | `200 OK` / `201 Created` — writes file on VM. For files `>25 MB`, split into multi-chunk `PUT` (`chunk: 1, 2, ..., -1` for final chunk) or stream via `/colab/ssh`! |
| `DELETE /api/contents/{path}` | `?authuser=0&colab-runtime-proxy-token=<tok>` | `204 No Content` — deletes remote file (`404` $\rightarrow$ `FileNotFoundError`). |

---

### 4.3 ZMQ-over-WebSocket Wire Protocol (Jupyter Messaging `v5.3` / `v5.4`)

#### WebSocket Connection & Persistence Rule

* **Endpoint**: `wss://8080-<endpoint>-c.<region>.prod.colab.dev/api/kernels/<kernel_id>/channels?session_id=<session_uuid>&token=<proxy_token>&colab-runtime-proxy-token=<proxy_token>`
* **Kernel Persistence Across Disconnections**: Clients MUST set `_own_kernel = False` — disconnecting closes only the WebSocket (`stop_channels()` + `ws.close()`), leaving the Python kernel alive in RAM/VRAM (`torch.cuda` tensors, loaded 50 GB LLM weights, globals, and imports persist across invocations).

#### Wire Framing Subprotocols (`wsclient.py` + `utils.py`)

Colab's Jupyter proxy supports both standard Jupyter WebSocket wire encodings:

1. **Default Subprotocol (`JupyterSubprotocol.DEFAULT`, no `Sec-WebSocket-Protocol` header — used by `colab-cli`)**:
   * **Text Frame (`OPCODE_TEXT`, when `buffers == []`)**: Single UTF-8 JSON object:
     ```json
     {
       "channel": "shell",
       "header": {
         "msg_id": "a1b2c3d4-e5f6-4789-abcd-0123456789ab",
         "session": "f9e8d7c6-b5a4-4321-9876-fedcba987654",
         "username": "colabhive",
         "date": "2026-11-21T07:25:00.123456Z",
         "msg_type": "execute_request",
         "version": "5.3"
       },
       "parent_header": {},
       "metadata": {},
       "content": {
         "code": "import torch\nprint(torch.cuda.get_device_name(0))",
         "silent": false,
         "store_history": true,
         "user_expressions": {},
         "allow_stdin": false,
         "stop_on_error": true
       }
     }
     ```
   * **Binary Frame (`OPCODE_BINARY`, when binary `buffers` are attached)**:
     * `[0..4]`: Big-endian `uint32` `nbufs` (`1 + len(buffers)`, must be `>= 2`).
     * `[4 .. 4*(nbufs+1)]`: `nbufs` big-endian `uint32` byte offsets.
     * `Buffer 0`: UTF-8 JSON envelope (`header`, `parent_header`, `metadata`, `content`, `channel`).
     * `Buffer 1..nbufs-1`: Raw contiguous binary buffers (e.g., comm/widget array payloads).

2. **`v1.kernel.websocket.jupyter.org` Subprotocol (`Sec-WebSocket-Protocol: v1.kernel.websocket.jupyter.org`)**:
   * Always `OPCODE_BINARY` with little-endian `uint64` offset table: `[offset_count (u64 LE), offsets[0..offset_count] (u64 LE), channel_utf8, header_json, parent_header_json, metadata_json, content_json, *buffers]`.

#### Message Lifecycle & Output Interception Contract

Before executing user code on any session, ColabHive runs the idempotent working-directory prelude (`silent=True`, `store_history=False`):
```python
import os; os.makedirs('/content', exist_ok=True); os.chdir('/content')
```

For each `execute_request` (`msg_id`), filter incoming `iopub` messages where `msg["parent_header"].get("msg_id") == msg_id` (plus unparented `status` messages where `execution_state in {"dead", "restarting"}` which immediately raise `RuntimeError("Kernel dead/restarting")`):

| Channel | `header.msg_type` | Key `content` Fields | ColabHive Handling & Interception |
| :--- | :--- | :--- | :--- |
| `iopub` | `status` | `{"execution_state": "busy"\|"idle"\|"starting"\|"restarting"\|"dead"}` | Marks start (`busy`) and completion (`idle` $\rightarrow$ then read `execute_reply` on `shell` channel). |
| `iopub` | `stream` | `{"name": "stdout"\|"stderr", "text": "..."}` | Streamed live over WebSocket to UI/CLI/MCP; resets idle `--timeout` clock on every chunk! |
| `iopub` | `execute_result` | `{"execution_count": 1, "data": {"text/plain": "...", "text/html": "..."}, "metadata": {}}` | Rendered via priority `text/markdown > text/html (html2text) > text/plain`. |
| `iopub` | `display_data` / `update_display_data` | `{"data": {"image/png": "<b64>", "image/jpeg": "<b64>", "text/plain": "<Figure...>"}, "metadata": {}, "transient": {"display_id": "..."}}` | **Plot/Image Interception**: Extracts `image/png` / `image/jpeg` base64, persists to artifact store / `--output-image`, streams inline to Web UI & MCP `ImageContent`, and emits Kitty Graphics escape `\033_Ga=T,f=100;<b64>\033\\` ONLY when `sys.stdout.isatty()`. |
| `iopub` | `clear_output` | `{"wait": bool}` | Clears accumulated cell output buffer. |
| `iopub` | `error` | `{"ename": "ZeroDivisionError", "evalue": "...", "traceback": ["\u001b[0;31m..."]}` | Appends structured error; for `run` mode, maps `ename == "SystemExit"` to CPython exit code (`""\|"None"\|"0" -> 0`, `<int> -> int`, `<non-int> -> 1`) while suppressing traceback noise. |
| `shell` | `execute_reply` | `{"status": "ok"\|"error"\|"abort", "execution_count": 1}` | Finalizes execution; if `status == "error"` with no `iopub` `error` frame, synthesizes fallback error output. |

#### Idle Timeout vs. Wall-Clock Timeout (`_execute_streaming`)

`timeout` (default `30.0s`, `0` or `None` = unlimited) is strictly an **inter-message idle silence timeout**, NOT a total wall-clock cap: a training loop printing progress every 10 seconds runs indefinitely under `--timeout 30`, while a hung network call with zero output for 30s raises `TimeoutError` cleanly.

#### Package Installation (`uv pip install --system`) & `.ipynb` Execution

* **Fast Package Installer (`install`)**: If `-r requirements.txt` is supplied, upload via `PUT /api/contents/content/<basename>`, then run:
  ```python
  import subprocess, sys
  try:
      subprocess.check_call(['uv', 'pip', 'install', '--system', *packages])
  except Exception:
      subprocess.check_call([sys.executable, '-m', 'pip', 'install', *packages])
  ```
* **Notebook Execution (`exec -f nb.ipynb`)**: Parse with `nbformat.read(..., as_version=4)`, ensure every cell has a UUID `cell.id`, extract `# @title <label>` for progress logging, prepend `--env KEY=VALUE` assignments, map `stream` / `display_data` / `execute_result` / `error` back onto `cell.outputs`, and write `<stem>_output.ipynb` inside a `finally:` block so partial progress is preserved even if cell `k` fails.

---

## 5. Full CLI Compatibility Matrix (`colab` v0.8.0 + `colabhive` Pool Superset)

ColabHive ships a 100% drop-in CLI (`colabhive`, optionally aliased/symlinked as `colab`) that honors every flag, output format, local state schema, exit code, and stream separation rule of official `google-colab-cli v0.8.0`, while extending it with multi-account pool routing and sharded experiment orchestration.

### 5.1 Global Options (Must Precede Subcommand in Strict `colab` Mode; Accepted Anywhere in `colabhive`)

| Flag | Type & Default | `colab v0.8.0` Contract & `colabhive` Extension |
| :--- | :--- | :--- |
| `-c, --client-oauth-config <PATH>` | `str` (`~/.colab-cli-oauth-config.json`) | Custom Google OAuth client JSON; falls back to bundled Cloud SDK config (`764086051850-...`). |
| `--config <PATH>` | `Optional[str]` (`~/.config/colab-cli/sessions.json`) | Isolates local session state file (protected by `filelock.ReadWriteLock` at `<PATH>.lock`, file mode `0600`). |
| `--auth <oauth2\|adc\|pool>` | `enum` (`oauth2` in WSL2 / `pool` when connected to ColabHive daemon) | Selects credential strategy. `pool` routes via ColabHive Control Plane (`COLABHIVE_SERVER_URL` + `COLABHIVE_API_KEY` or local embedded SQLite vault). |
| `--logtostderr` | `bool` (`false`) | Writes `_RedactingFormatter` debug logs (automatically masking `Authorization`, `Cookie`, `X-Colab-Runtime-Proxy-Token`, `X-Goog-Colab-Token`, `refresh_token`, `client_secret`) to `stderr` in addition to `~/.config/colab-cli/colab.log`. |
| `--account <ID\|EMAIL\|TAG>` | `Optional[str]` (`None`) | **[ColabHive Pool]** Pin command to a specific account ID, email address, or tag selector (`tag:lab-ultra`). |
| `--strategy <NAME>` | `enum` (`waste-zero`) | **[ColabHive Pool]** Account selection policy: `waste-zero` (expiring/free-window CUs first), `max-balance` (highest remaining CUs), `sticky` (reuse account bound to agent/project), `round-robin`, or `cheapest`. |
| `--server <URL>` | `Optional[str]` (env `COLABHIVE_URL`) | **[ColabHive Pool]** Target remote ColabHive gateway URL instead of local embedded pool. |

---

### 5.2 Complete Command, Flag, Exit-Code & Stream Contract Matrix

| Command | Flags & Arguments | Exit Codes | `stdout` Contract | `stderr` Contract |
| :--- | :--- | :--- | :--- | :--- |
| **`new`** | `-s/--session <NAME>` (auto 6-hex if omitted)<br>`--gpu <T4\|L4\|G4\|H100\|A100>`<br>`--tpu <v5e1\|v6e1>`<br>`--high-mem`<br>*Pool*: `--account`, `--strategy`, `--fallback <auto\|none\|T1,T2>`, `--experiment <EXP>` | `0`: Allocated<br>`1`: Quota/400/412/Auth error<br>`2`: Invalid `--gpu`/`--tpu` value | `[colab] Creating session '<name>'...\n[colab] Session READY.` | `--high-mem ignored` notice; `400` entitlement or `412` precondition error details |
| **`exec`** | `-s/--session <NAME>`<br>`-f/--file <script.py\|nb.ipynb>` (or piped `stdin`)<br>`--output-image <PATH>`<br>`--timeout <SEC>` (`30.0`, `0`=unlimited)<br>`--env KEY=VALUE` (repeatable) | `0`: Completed (note: `exec` returns `0` even on cell Python exception unless transport fails; check `stderr`/outputs)<br>`1`: Missing stdin TTY / 404 pruned / timeout<br>`2`: Invalid `--env` | Kernel `stdout` stream + rendered `execute_result`/`display_data` + `[Image saved to: ...]` + notebook progress lines | Kernel `stderr` stream + formatted ANSI Python tracebacks |
| **`run`** | `<script.py> [script_args...]`<br>`-s/--session <NAME>` (`run-<6hex>`)<br>`--gpu`, `--tpu`, `--high-mem`<br>`--keep` (preserve VM)<br>`--timeout <SEC>` (`30.0`)<br>`--env KEY=VALUE` (before or after `<script.py>`)<br>*Pool*: `--account`, `--strategy`, `--fallback`, `--shards <K>`, `--experiment <EXP>`, `--watchdog` | **`0`**: Script succeeded or `sys.exit(0)`/`sys.exit()`<br>**`<N>`**: Script called `sys.exit(<int N>)`<br>**`1`**: Uncaught exception, `sys.exit("msg")`, 400/412, or transport error<br>**`2`**: Script file missing locally (**validated BEFORE allocating VM!**) or bad `--env` | **Pure script `stdout` ONLY** (clean for `colab run job.py > out.jsonl` and `#!/usr/bin/env -S colab run --gpu G4` shebangs!) | **All `[colab] ...` lifecycle chatter** (`Creating session...`, `Session READY...`, `Stopping session...`, `Session terminated.`) + script `stderr` + non-`SystemExit` tracebacks |
| **`repl`** | `-s/--session <NAME>`<br>`--output-image <PATH>` | `0`: Clean `/quit` or piped EOF<br>`1`: Session not found / 404 lost | Interactive `prompt_toolkit` REPL (or non-interactive execution if `!stdin.isatty()`) | Session lost / error diagnostics |
| **`sessions`** | *(no required args)*<br>*Pool*: `--account <ID\|all>`, `--json` | `0`: Always `0` on valid auth | `[<name\|?>] <endpoint> \| Hardware: <CPU\|G4\|...> \| Shape: <Standard\|High-RAM> \| Variant: <DEFAULT\|GPU\|TPU>` (or `[colab] No active sessions found on server.`) + `[colab] Pruned N stale local session(s).` | Auth warnings |
| **`status`** | `-s/--session <NAME>` (all local if omitted)<br>*Pool*: `--experiment <EXP>`, `--json` | `0`: Printed status | Same line as `sessions` + ` \| Status: <IDLE\|BUSY (exec(...))>` + `\n  Last Execution: <file> [\| Cell: <id>] at YYYY-MM-DD HH:MM:SS` | — |
| **`stop`** | `-s/--session <NAME>` (or `--all` / `--experiment <EXP>` in `colabhive` only) | `0`: Stopped (or already missing)<br>`1`: Multiple sessions & `-s` omitted | `[colab] Stopping session '<name>'...\n[colab] Session terminated.` | — |
| **`restart-kernel`** | `-s/--session <NAME>` | `0`: Kernel restarted (`POST /api/kernels/<id>/restart`)<br>`1`: Session missing | *(silent on success)* | Errors if session missing |
| **`ls`** | `-s/--session <NAME>`<br>`[PATH]` (default `"content"`) | `0`: Listed<br>`1`: 404 / error | Sorted entries (`dirs/` first with trailing `/`, then files, one per line) | — |
| **`rm`** | `-s/--session <NAME>`<br>`<REMOTE_PATH>` | `0`: Deleted<br>`1`: 404 / error | `[colab] Deleted <path>` | — |
| **`upload`** | `-s/--session <NAME>`<br>`<LOCAL_PATH> <REMOTE_PATH>` | `0`: Uploaded<br>`1`: Local file missing or PUT failed | `[colab] Uploaded '<local>' to '<remote>'` | — |
| **`download`** | `-s/--session <NAME>`<br>`<REMOTE_PATH> <LOCAL_PATH>` | `0`: Downloaded<br>`1`: Remote 404 / directory / error | `[colab] Downloaded '<remote>' to '<local>'` | — |
| **`edit`** | `-s/--session <NAME>`<br>`<REMOTE_PATH>` | `0`: Unchanged or uploaded after `$EDITOR` SHA-256 diff<br>`1`: Upload error | `[colab] Edited and uploaded '<remote>'` or `[colab] No changes made...` | — |
| **`install`** | `-s/--session <NAME>`<br>`[PACKAGES...]`<br>`-r/--requirement <FILE>` | `0`: Installed (`uv pip install --system` -> `pip` fallback)<br>`1`: No args or missing `-r` file | `[colab] Installing packages on <name> (preferring uv)...` + `uv`/`pip` output | Subprocess `stderr` / errors |
| **`drivemount`** | `-s/--session <NAME>`<br>`[MOUNT_PATH]` (`/content/drive`) | `0`: Mounted (interactive human TTY required; `600s` timeout) | Auth prompt URL + `Mounted at /content/drive` | Errors |
| **`auth`** *(hidden)* | `-s/--session <NAME>` | `0`: Injects VM-side GCP creds (`google.colab.auth.authenticate_user()`) | Interactive Google consent prompt (`600s` timeout) | Errors |
| **`usage`** | *Pool*: `--account <ID\|all>`, `--gpu-hours`, `--json` | `0`: Success<br>`1`: API failure | Exact 3-line format:<br>`Current balance: <X.XX> compute units`<br>`Usage rate: <Y.YY>/hr`<br>`Active assignments: <N>` | `[colab] Warning: failed to fetch compute-unit info: ...` |
| **`log`** | `-s/--session <NAME>` (lists logged sessions if omitted)<br>`-n/--lines <INT>`<br>`-t/--type <EVENT_TYPE>`<br>`-o/--output <PATH.{ipynb,md,txt,jsonl}>` | `0`: Printed or exported | Event summary lines `[YYYY-MM-DD HH:MM:SS] EXEC: ...` or `[colab] Exported history to '<path>'.` | — |
| **`url`** | `-s/--session <NAME>`<br>`--host <ORIGIN>` (`https://colab.research.google.com`)<br>`--open` | `0`: Printed raw pipeable URL<br>`1`: Session not found | Single raw line (NO `[colab]` prefix, auto-update banner suppressed):<br>`https://colab.research.google.com/notebooks/empty.ipynb?dbu=%2Ftun%2Fm%2F<ep>#datalabBackendUrl=https://colab.research.google.com/tun/m/<ep>` | `[colab] Session '<name>' not found.` |
| **`ssh`** | `-s/--session <NAME>`<br>`--proxy-mode`<br>`-i/--identity <KEY_PATH>`<br>`--gpu`, `--tpu`, `--high-mem`<br>`--rm` (stop auto-created/proxied VM on exit, traps `SIGHUP/SIGTERM/SIGINT`)<br>`[-- CMD [ARGS]...]` | `<ssh_exit_code>` (`0..255`)<br>`1`: WS handshake error (400/401/404/429/502)<br>`2`: Missing Ed25519/ECDSA key, missing session, or bad flag combo | Raw SSH stdout / ProxyCommand binary stream (auto-update banner suppressed when `!stdout.isatty()`) | Handshake diagnostics & `--gpu/--rm ignored` warnings |
| **`whoami`** *(hidden)* | *(uses global `--auth` / `--account`)* | `0`: Token valid<br>`1`: Refresh or `tokeninfo` HTTP 400 | `Auth provider: ...`<br>`Email: ...`<br>`Audience: ...`<br>`Expires in: <N>m`<br>`Scopes:\n  - ...` | `[colab] whoami: ...` |
| **`pay` / `version` / `update` / `readme` / `skill` / `help`** | `update [--install]`<br>`help [CMD]` | `0` (`2` on unknown `help` cmd) | `Version: 0.8.0` / PyPI update status / bundled docs | Errors |

---

### 5.3 Local State File Compatibility Schemas (`~/.config/colab-cli/`)

To maintain two-way interoperability with an installed `colab` CLI on the same host:

1. **`~/.config/colab-cli/sessions.json`** (locked via `~/.config/colab-cli/sessions.json.lock` using `filelock.ReadWriteLock`, permissions `0600`):
   ```json
   {
     "exp1_g4_0": {
       "name": "exp1_g4_0",
       "token": "ChAKDm...",
       "url": "https://8080-gpu-g4-s-2k8m9p1x4z7q-c.us-central1-a.prod.colab.dev",
       "endpoint": "gpu-g4-s-2k8m9p1x4z7q",
       "token_expires_at": "2026-11-21T08:20:00.000000Z",
       "variant": "GPU",
       "accelerator": "G4",
       "machine_shape": "HIGH_RAM",
       "kernel_id": "9f8e7d6c-5b4a-3210-fedc-ba9876543210",
       "session_id": "12345678-9abc-def0-1234-56789abcdef0",
       "last_execution": ["train.py", null, "2026-11-21 15:18:42"],
       "running": null
     }
   }
   ```
2. **`~/.config/colab-cli/history/<session_name>.jsonl`** (permissions `0600`):
   * Append-only JSONL records with `"timestamp"` (UTC ISO-8601) and `"event_type"` $\in$ `{"session_created", "session_terminated", "execution", "file_operation", "automation", "automation_result", "colab_request", "drive_auth_needed", "drive_auth_success", "stdin_request", "input_reply", "repl_started"}`.

---

## 6. Complete MCP Server Specification & Tool Schemas (`stdio` + `Streamable HTTP / SSE`)

### 6.1 MCP Transport, Authentication & Static+Dynamic Discovery Architecture

ColabHive exposes a unified Model Context Protocol (`2025-03-26` / `2025-06-18` spec) server supporting **both** local single-user agents and multi-tenant networked agent swarms:

| Transport Mode | Endpoint / Command | Authentication & Session Affinity | Target MCP Clients |
| :--- | :--- | :--- | :--- |
| **`stdio`** | `colabhive mcp stdio [--account ...] [--strategy waste-zero]` | Local OS trust or `COLABHIVE_API_KEY=ch_sk_...` env var; sticky session bound per stdio process lifetime | Claude Desktop, Claude Code, Antigravity, Cursor, Codex CLI, Windsurf, Gemini CLI |
| **Streamable HTTP** *(Primary Network)* | `POST /mcp` (JSON-RPC / SSE stream)<br>`GET /mcp` (Server-initiated notifications)<br>`DELETE /mcp` (Close MCP session) | `Authorization: Bearer ch_sk_<32_alnum>` (or `X-ColabHive-Key: ch_sk_...`); **`Mcp-Session-Id` header binds `Sticky-Agent-Session` routing** to the same Colab VM & Google account! | Remote Agents, Multi-Agent Swarms, Antigravity / Claude Code HTTP MCP |
| **Legacy HTTP + SSE** | `GET /mcp/sse` $\rightarrow$ emits `endpoint` event `/mcp/messages?session_id=<id>` | `Authorization: Bearer ch_sk_...` or `?api_key=ch_sk_...` | Legacy MCP SDKs (`2024-11-05`) |

#### Solving `googlecolab/colab-mcp`'s Dynamic-Tool & Browser Bottlenecks

Upstream `googlecolab/colab-mcp` is a stateless `FastMCPProxy` (`websocket_server.py`) that:
1. Exposes **only** `open_colab_browser_connection` at startup,
2. Opens a local Chrome tab with `#mcpProxyToken=<TOKEN>&mcpProxyPort=<PORT>` waiting for the browser UI to connect back to `ws://127.0.0.1:<PORT>`, and
3. Announces cell tools asynchronously via `notifications/tools/list_changed` — which breaks clients that cache tool lists at initialization and fails completely on headless servers or multi-account pools.

**ColabHive solves both while preserving 100% tool-level compatibility**:
* **Immediate Static Tool Registration**: All three tool suites below are advertised immediately in `tools/list` at connection startup (zero waiting on `notifications/tools/list_changed`), while *still* emitting `notifications/tools/list_changed` if a live browser tab connects via `open_colab_browser_connection`.
* **Headless Virtual Notebook Backing**: Calling `add_code_cell` / `run_code_cell` / `execute_code` works **100% headlessly** against an in-memory + history-backed `.ipynb` model attached to a pooled Colab VM kernel — AND if the user clicks **"Open in Colab Web"** (`colab_session_url` / `open_colab_browser_connection`), the browser attaches to the **exact same live runtime**!

---

### 6.2 Suite A — Official `googlecolab/colab-mcp` Compatible Tools

#### 1. `open_colab_browser_connection`
Allocates (or attaches to) a Colab runtime via the pool router and returns the instant browser attachment URL (`#datalabBackendUrl=...`, optionally opening a local browser tab or binding the `#mcpProxyToken` WebSocket bridge).

* **Input Schema**:
  ```json
  {
    "type": "object",
    "properties": {
      "session": { "type": "string", "description": "Optional session name to attach or create." },
      "gpu": { "type": "string", "enum": ["T4", "L4", "G4", "A100", "H100"], "description": "Optional GPU tier if creating a new session." },
      "open_browser": { "type": "boolean", "default": false, "description": "Launch local default browser if available; false returns URL headlessly." }
    },
    "additionalProperties": false
  }
  ```
* **Return Payload**:
  ```json
  {
    "connected": true,
    "session": "mcp-a8f31c",
    "account_email": "lab-ultra-01@gmail.com",
    "accelerator": "G4",
    "colab_web_url": "https://colab.research.google.com/notebooks/empty.ipynb?dbu=%2Ftun%2Fm%2Fgpu-g4-s-2k8m9p1x4z7q#datalabBackendUrl=https://colab.research.google.com/tun/m/gpu-g4-s-2k8m9p1x4z7q"
  }
  ```

#### 2. `execute_code`
Stateful Python/IPython execution inside the active (or specified) Colab runtime's persistent Jupyter kernel (`/content` working directory). Auto-provisions a runtime according to pool policy if none is bound to the current MCP session.

* **Input Schema**:
  ```json
  {
    "type": "object",
    "properties": {
      "code": { "type": "string", "description": "Python or IPython (!shell / %%bash) code to execute in /content." },
      "session": { "type": "string", "description": "Target session name (defaults to sticky session bound to this MCP client)." },
      "gpu": { "type": "string", "enum": ["T4", "L4", "G4", "A100", "H100"], "description": "GPU accelerator if auto-provisioning on first call." },
      "timeout_sec": { "type": "number", "default": 120, "description": "Idle silence timeout in seconds (0 for unlimited)." }
    },
    "required": ["code"],
    "additionalProperties": false
  }
  ```
* **Return Payload** (MCP `content` array includes `TextContent` JSON summary **plus** native `ImageContent` `{type: "image", data: "<b64>", mimeType: "image/png"}` whenever `display_data` emits plots!):
  ```json
  {
    "session": "mcp-a8f31c",
    "execution_count": 4,
    "status": "ok",
    "stdout": "NVIDIA RTX PRO 6000 Blackwell Server Edition\n",
    "stderr": "",
    "result": "'95.6 GB VRAM'",
    "images": [
      { "mime_type": "image/png", "artifact_url": "/api/v1/artifacts/mcp-a8f31c/plot_004.png" }
    ],
    "executionError": null,
    "duration_ms": 412
  }
  ```

#### 3. Notebook Cell Manipulation Tools (`get_cells`, `add_code_cell`, `add_text_cell`, `update_cell`, `run_code_cell`, `delete_cell`, `move_cell`)

| Tool Name | Input JSON Schema (`properties` & `required`) | Return Payload Summary |
| :--- | :--- | :--- |
| **`get_cells`** | `{"session": {"type": "string"}, "include_outputs": {"type": "boolean", "default": true}}` | `{"session": "...", "cells": [{"cell_id": "c_01", "index": 0, "cell_type": "code"\|"markdown", "source": "...", "execution_count": 1, "outputs": [...]}]}` |
| **`add_code_cell`** | `{"code": {"type": "string"}, "index": {"type": "integer"}, "run": {"type": "boolean", "default": false}, "session": {"type": "string"}}` (req: `["code"]`) | `{"cell_id": "c_02", "index": 1, "executed": false, "execution": null}` |
| **`add_text_cell`** | `{"content": {"type": "string"}, "index": {"type": "integer"}, "session": {"type": "string"}}` (req: `["content"]`) | `{"cell_id": "c_03", "index": 2, "cell_type": "markdown"}` |
| **`update_cell`** | `{"cell_id": {"type": "string"}, "content": {"type": "string"}, "session": {"type": "string"}}` (req: `["cell_id", "content"]`) | `{"cell_id": "c_02", "updated": true}` |
| **`run_code_cell`** | `{"cell_id": {"type": "string"}, "timeout_sec": {"type": "number", "default": 120}, "session": {"type": "string"}}` (req: `["cell_id"]`) | Same execution result envelope as `execute_code` + updates stored cell `outputs` |
| **`delete_cell`** | `{"cell_id": {"type": "string"}, "session": {"type": "string"}}` (req: `["cell_id"]`) | `{"cell_id": "c_02", "deleted": true, "remaining_cells": 2}` |
| **`move_cell`** | `{"cell_id": {"type": "string"}, "new_index": {"type": "integer"}, "session": {"type": "string"}}` (req: `["cell_id", "new_index"]`) | `{"cell_id": "c_02", "old_index": 3, "new_index": 1}` |

---

### 6.3 Suite B — Headless Session, Runtime, File & Package Tools

#### 1. `colab_session_create`
Explicitly provisions a persistent Colab VM through ColabHive's multi-account router with automatic HTTP `400`/`412`/`503` failover.

```json
{
  "name": "colab_session_create",
  "description": "Allocate a persistent Google Colab CPU/GPU/TPU VM from the multi-account pool.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "session": { "type": "string", "description": "Unique human-readable session name (e.g. 'eval_g4_0')." },
      "gpu": { "type": "string", "enum": ["G4", "A100", "H100", "L4", "T4"], "description": "Requested GPU tier (G4=96GB Blackwell ~9.66 CU/hr, A100=40/80GB ~8.88 CU/hr, L4=24GB ~4.52 CU/hr, T4=16GB ~1.96 CU/hr)." },
      "tpu": { "type": "string", "enum": ["v6e1", "v5e1"], "description": "Requested TPU tier (mutually exclusive with gpu)." },
      "high_mem": { "type": "boolean", "default": true, "description": "Request High-RAM host shape where supported." },
      "strategy": { "type": "string", "enum": ["waste-zero", "max-balance", "sticky", "round-robin", "cheapest"], "default": "waste-zero" },
      "account": { "type": "string", "description": "Optional specific account ID, email, or tag:<name>." },
      "auto_fallback": { "type": "boolean", "default": true, "description": "Automatically try other accounts or step down GPU tier on HTTP 400/412/503." },
      "experiment_id": { "type": "string", "description": "Optional experiment group ID for Live Experiment Visualizer." }
    },
    "required": ["session"],
    "additionalProperties": false
  }
}
```
* **Return Payload**:
  ```json
  {
    "session": "eval_g4_0",
    "endpoint": "gpu-g4-s-2k8m9p1x4z7q",
    "account_id": "acc_01J9...",
    "account_email": "lab-ultra-01@gmail.com",
    "variant": "GPU",
    "requested_accelerator": "G4",
    "assigned_accelerator": "G4",
    "fallback_applied": false,
    "machine_shape": "HIGH_RAM",
    "estimated_burn_rate_cu_hr": 9.66,
    "account_remaining_cu": 482.54,
    "colab_web_url": "https://colab.research.google.com/notebooks/empty.ipynb?dbu=%2Ftun%2Fm%2Fgpu-g4-s-2k8m9p1x4z7q#datalabBackendUrl=https://colab.research.google.com/tun/m/gpu-g4-s-2k8m9p1x4z7q"
  }
  ```

#### 2. Lifecycle, File & Package Tools

| Tool Name | Input Schema (`properties` & `required`) | Return Payload |
| :--- | :--- | :--- |
| **`colab_session_list`** | `{"account": {"type": "string"}, "experiment_id": {"type": "string"}, "sync_backend": {"type": "boolean", "default": true}}` | `{"sessions": [{"session": "...", "endpoint": "...", "account_email": "...", "accelerator": "G4", "shape": "HIGH_RAM", "status": "IDLE", "burn_rate_cu_hr": 9.66, "uptime_sec": 840, "keepalive_healthy": true}]}` |
| **`colab_session_status`** | `{"session": {"type": "string"}, "probe_gpu": {"type": "boolean", "default": true}}` (req: `["session"]`) | Session metadata + live `gpu_telemetry` (`vram_used_mb`, `vram_total_mb`, `sm_util_pct`, `temp_c`) + `worker_health` (`pid`, `proc_state`, `zombie_detected: false`, `last_keepalive_at`) |
| **`colab_session_stop`** | `{"session": {"type": "string"}}` (req: `["session"]`) | `{"session": "eval_g4_0", "unassigned": true, "endpoint": "...", "cu_consumed_estimate": 2.25}` |
| **`colab_kernel_restart`** | `{"session": {"type": "string"}}` (req: `["session"]`) | `{"session": "eval_g4_0", "restarted": true, "kernel_id": "...", "note": "/content and /root/.cache preserved"}` |
| **`colab_kernel_interrupt`** | `{"session": {"type": "string"}}` (req: `["session"]`) | `{"session": "eval_g4_0", "interrupted": true}` |
| **`colab_exec`** | `{"session": {"type": "string"}, "code": {"type": "string"}, "local_file": {"type": "string"}, "env": {"type": "object", "additionalProperties": {"type": "string"}}, "timeout_sec": {"type": "number", "default": 120}, "output_image_path": {"type": "string"}}` (req: `["session"]`) | `{"status": "ok"\|"error", "stdout": "...", "stderr": "...", "outputs": [...], "saved_images": [...], "notebook_output_path": "..."}` |
| **`colab_file_upload`** | `{"session": {"type": "string"}, "local_path": {"type": "string"}, "remote_path": {"type": "string"}}` (req: `["session", "local_path", "remote_path"]`) | `{"uploaded": true, "remote_path": "/content/data.json", "bytes": 1048576}` |
| **`colab_file_download`** | `{"session": {"type": "string"}, "remote_path": {"type": "string"}, "local_path": {"type": "string"}}` (req: `["session", "remote_path", "local_path"]`) | `{"downloaded": true, "local_path": "./results.jsonl", "bytes": 524288}` |
| **`colab_file_ls`** | `{"session": {"type": "string"}, "remote_path": {"type": "string", "default": "/content"}}` (req: `["session"]`) | `{"path": "/content", "entries": [{"name": "shard_0.jsonl", "type": "file", "size": 48120}]}` |
| **`colab_file_rm`** | `{"session": {"type": "string"}, "remote_path": {"type": "string"}}` (req: `["session", "remote_path"]`) | `{"deleted": true, "remote_path": "/content/tmp.bin"}` |
| **`colab_install_packages`** | `{"session": {"type": "string"}, "packages": {"type": "array", "items": {"type": "string"}}, "requirements_path": {"type": "string"}}` (req: `["session"]`) | `{"installed": true, "installer": "uv", "stdout": "Installation Complete (via uv)!"}` |
| **`colab_session_url`** | `{"session": {"type": "string"}}` (req: `["session"]`) | `{"session": "...", "url": "https://colab.research.google.com/notebooks/empty.ipynb?dbu=...#datalabBackendUrl=..."}` |

---

### 6.4 Suite C — Multi-Account Pool Observatory & Sharded Research Experiment Tools

These high-level tools turn multi-GPU sharding (`1 GPU x 4 hr == 4 GPUs x 1 hr` at identical CU cost!) and anti-zombie reliability into single atomic Agent MCP calls.

#### 1. `pool_quota_summary`
Queries live `ccu-info` & active assignments across all pooled Google accounts and computes the **Cloud GPU Equivalent Remaining Runtime Matrix**.

* **Input Schema**:
  ```json
  {
    "type": "object",
    "properties": {
      "refresh_live": { "type": "boolean", "default": true, "description": "Poll live /tun/m/ccu-info across enabled accounts (cached 30s)." },
      "tag": { "type": "string", "description": "Optional tag filter." }
    },
    "additionalProperties": false
  }
  ```
* **Return Payload**:
  ```json
  {
    "pool_totals": {
      "accounts_total": 4,
      "accounts_healthy": 4,
      "total_available_cu": 1420.80,
      "schedulable_cu_after_reserves": 1270.80,
      "active_burn_rate_cu_hr": 19.32,
      "active_sessions": 2,
      "pool_depletion_eta_hours": 65.78,
      "gpu_equivalent_hours": {
        "G4_96GB": { "rate_cu_hr": 9.66, "schedulable_hours": 131.55 },
        "H100_80GB": { "rate_cu_hr": 12.50, "schedulable_hours": 101.66 },
        "A100_40GB": { "rate_cu_hr": 8.88, "schedulable_hours": 143.11 },
        "L4_24GB": { "rate_cu_hr": 4.52, "schedulable_hours": 281.15 },
        "T4_16GB": { "rate_cu_hr": 1.96, "schedulable_hours": 648.37 },
        "TPU_V6E1": { "rate_cu_hr": 5.30, "schedulable_hours": 239.77 },
        "TPU_V5E1": { "rate_cu_hr": 3.85, "schedulable_hours": 330.08 }
      }
    },
    "accounts": [
      {
        "account_id": "acc_01",
        "email": "lab-ultra-01@gmail.com",
        "status": "HEALTHY",
        "current_balance_cu": 482.54,
        "reserve_cu_floor": 50.0,
        "schedulable_cu": 432.54,
        "consumption_rate_hourly": 9.66,
        "assignments_count": 1,
        "eligible_gpus": ["T4", "L4", "G4", "A100"]
      }
    ]
  }
  ```

#### 2. `experiment_launch_sharded`
Allocates `num_shards` concurrent VMs across the multi-account pool, uploads workspace files, installs dependencies via `uv`, smoke-tests imports, spawns each shard worker in background mode with `COLABHIVE_SHARD_ID=0..K-1` & `COLABHIVE_NUM_SHARDS=K`, runs the **8-second startup self-check**, and attaches ColabHive's server-side **15m keepalive + `/proc/{pid}/stat` anti-zombie + JSONL checkpoint sync daemon** + automatic teardown-on-completion!

* **Input Schema**:
  ```json
  {
    "type": "object",
    "properties": {
      "experiment_id": { "type": "string", "description": "Unique experiment identifier (e.g. 'qwen27b_mmlu_r1')." },
      "script_path": { "type": "string", "description": "Local Python entrypoint script to run on each shard." },
      "script_args": { "type": "array", "items": { "type": "string" }, "default": [] },
      "upload_files": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "local_path": { "type": "string" },
            "remote_path": { "type": "string" }
          },
          "required": ["local_path", "remote_path"]
        },
        "default": []
      },
      "packages": { "type": "array", "items": { "type": "string" }, "default": [] },
      "gpu": { "type": "string", "enum": ["G4", "A100", "H100", "L4", "T4"], "default": "G4" },
      "high_mem": { "type": "boolean", "default": true },
      "num_shards": { "type": "integer", "minimum": 1, "maximum": 32, "default": 4 },
      "strategy": { "type": "string", "enum": ["waste-zero", "max-balance", "round-robin", "cheapest"], "default": "waste-zero" },
      "remote_checkpoint_glob": { "type": "string", "default": "/content/shard_*.jsonl", "description": "Remote JSONL/checkpoint pattern synced continuously by the background poller." },
      "local_output_dir": { "type": "string", "description": "Local directory to sync shard logs and checkpoints into." },
      "auto_stop_on_complete": { "type": "boolean", "default": true, "description": "Immediately unassign each shard VM as soon as its worker finishes so zero idle CUs are burned." },
      "env": { "type": "object", "additionalProperties": { "type": "string" } }
    },
    "required": ["experiment_id", "script_path", "local_output_dir"],
    "additionalProperties": false
  }
  ```
* **Return Payload**:
  ```json
  {
    "experiment_id": "qwen27b_mmlu_r1",
    "status": "RUNNING",
    "num_shards": 4,
    "aggregate_burn_rate_cu_hr": 38.64,
    "watchdog_next_check_in_sec": 240,
    "shards": [
      {
        "shard_id": 0,
        "session": "qwen27b_mmlu_r1_s0",
        "account_email": "lab-ultra-01@gmail.com",
        "accelerator": "G4",
        "pid": 4812,
        "startup_self_check": "PASSED",
        "startup_log_preview": "Loaded shard 0/4 (250 items) on NVIDIA RTX PRO 6000 Blackwell..."
      }
    ]
  }
  ```

#### 3. `experiment_status`
Non-blocking status, anti-zombie health, GPU telemetry, checkpoint record counts, and log tail across all shards of an experiment.

* **Input Schema**:
  ```json
  {
    "type": "object",
    "properties": {
      "experiment_id": { "type": "string" },
      "log_tail_lines": { "type": "integer", "default": 25 }
    },
    "required": ["experiment_id"],
    "additionalProperties": false
  }
  ```
* **Return Payload**:
  ```json
  {
    "experiment_id": "qwen27b_mmlu_r1",
    "overall_status": "RUNNING",
    "completed_shards": 2,
    "running_shards": 2,
    "failed_shards": 0,
    "synced_records_total": 780,
    "cu_burned_so_far": 6.44,
    "shards": [
      {
        "shard_id": 0,
        "session": "qwen27b_mmlu_r1_s0",
        "status": "COMPLETED_AND_UNASSIGNED",
        "exit_code": 0,
        "synced_records": 250,
        "zombie_detected": false
      },
      {
        "shard_id": 1,
        "session": "qwen27b_mmlu_r1_s1",
        "status": "RUNNING",
        "pid": 4905,
        "proc_state": "R",
        "zombie_detected": false,
        "vram_used_gb": 51.4,
        "sm_util_pct": 98,
        "synced_records": 140,
        "log_tail": "[140/250] acc=0.8429 | 3.12s/it"
      }
    ]
  }
  ```

#### 4. `experiment_collect_and_teardown`
Performs final checkpoint sync across any remaining shards, deterministically merges `/content/shard_{0..K-1}.jsonl` into `<local_output_dir>/merged.jsonl`, exports per-shard `.ipynb` provenance logs, and guarantees `GET + POST /tun/m/unassign/<endpoint>` teardown across every VM in the experiment.

* **Input Schema**:
  ```json
  {
    "type": "object",
    "properties": {
      "experiment_id": { "type": "string" },
      "force_kill_running": { "type": "boolean", "default": false, "description": "If true, aborts still-running shards and releases their VMs immediately." },
      "merge_jsonl_filename": { "type": "string", "default": "merged.jsonl" }
    },
    "required": ["experiment_id"],
    "additionalProperties": false
  }
  ```
* **Return Payload**:
  ```json
  {
    "experiment_id": "qwen27b_mmlu_r1",
    "all_vms_unassigned": true,
    "active_vms_remaining": 0,
    "merged_jsonl_path": "d:/git_proj/colabhive/runs/qwen27b_mmlu_r1/merged.jsonl",
    "merged_records_count": 1000,
    "exported_notebooks": [
      "d:/git_proj/colabhive/runs/qwen27b_mmlu_r1/qwen27b_mmlu_r1_s0.ipynb"
    ],
    "total_cu_consumed": 8.92
  }
  ```
