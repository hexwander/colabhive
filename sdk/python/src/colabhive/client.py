"""Pure-stdlib (+ optional httpx) client & credential utilities for ColabHive."""

from __future__ import annotations

import base64
import configparser
import io
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path
from typing import Any

try:
    import httpx as _httpx  # type: ignore[import-not-found]
except ImportError:  # pragma: no cover - optional dependency
    _httpx = None

XSSI_PREFIX = ")]}'\n"
DEFAULT_BASE_URL = "http://localhost:7860"
_DEFAULT_TIMEOUT: Any = object()
OFFICIAL_COLAB_CLI_CLIENT_ID = (
    "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com"
)
OFFICIAL_COLAB_CLI_CLIENT_SECRET = "d-FL95Q19q7MQmFpd7hHD0Ty"
GOOGLE_TOKEN_URI = "https://oauth2.googleapis.com/token"


# ==============================================================================
# Exceptions
# ==============================================================================


class ColabHiveError(Exception):
    """Base error raised by ColabHive SDK operations."""

    def __init__(
        self,
        message: str,
        *,
        status_code: int | None = None,
        payload: Any = None,
        url: str | None = None,
    ) -> None:
        super().__init__(message)
        self.status_code = status_code
        self.payload = payload
        self.url = url


class ColabHiveAuthError(ColabHiveError):
    """Raised on HTTP 401 / 403 authentication or permission errors."""


class ColabHiveNotFoundError(ColabHiveError):
    """Raised on HTTP 404 missing session, file, or invite code."""


class ColabHiveQuotaError(ColabHiveError):
    """Raised on HTTP 402 / 412 / 429 quota, concurrency, or guardrail limits."""


# ==============================================================================
# Ergonomic Response Models (Support both dict["k"] and obj.k access)
# ==============================================================================


def _wrap_value(value: Any) -> Any:
    if isinstance(value, AttrDict):
        return value
    if isinstance(value, dict):
        return AttrDict(value)
    if isinstance(value, list):
        return [_wrap_value(item) for item in value]
    return value


class AttrDict(dict[str, Any]):
    """Dictionary supporting recursive attribute-style property access."""

    def __init__(self, mapping: dict[str, Any] | None = None, **kwargs: Any) -> None:
        super().__init__()
        if mapping:
            for k, v in mapping.items():
                self[k] = _wrap_value(v)
        for k, v in kwargs.items():
            self[k] = _wrap_value(v)

    def __getattr__(self, name: str) -> Any:
        try:
            return self[name]
        except KeyError as exc:
            raise AttributeError(
                f"'{type(self).__name__}' object has no attribute '{name}'"
            ) from exc

    def __setattr__(self, name: str, value: Any) -> None:
        if name.startswith("_"):
            super().__setattr__(name, value)
        else:
            self[name] = _wrap_value(value)

    def to_dict(self) -> dict[str, Any]:
        def _unwrap(val: Any) -> Any:
            if isinstance(val, dict):
                return {k: _unwrap(v) for k, v in val.items()}
            if isinstance(val, list):
                return [_unwrap(item) for item in val]
            return val

        return _unwrap(dict(self))


class QuotaSummary(AttrDict):
    """Pool quota & Cloud GPU Equivalent Runtime response wrapper."""

    @property
    def total_available_cu(self) -> float:
        if "total_available_cu" in self:
            return float(self["total_available_cu"])
        totals = self.get("pool_totals")
        if isinstance(totals, dict) and "total_available_cu" in totals:
            return float(totals["total_available_cu"])
        return float(self.get("total_cu", 0.0))

    @property
    def active_burn_rate_cu_hr(self) -> float:
        if "active_burn_rate_cu_hr" in self:
            return float(self["active_burn_rate_cu_hr"])
        totals = self.get("pool_totals")
        if isinstance(totals, dict) and "active_burn_rate_cu_hr" in totals:
            return float(totals["active_burn_rate_cu_hr"])
        return float(self.get("burn_rate_cu_hr", 0.0))

    @property
    def gpu_equivalent_hours(self) -> AttrDict:
        for key in ("gpu_equivalence", "gpu_equivalent_hours"):
            if key in self and isinstance(self[key], dict):
                return AttrDict(self[key])
        totals = self.get("pool_totals")
        if isinstance(totals, dict):
            for key in ("gpu_equivalence", "gpu_equivalent_hours"):
                if isinstance(totals.get(key), dict):
                    return AttrDict(totals[key])
        return AttrDict(self.get("equivalences") or {})

    @property
    def accounts(self) -> list[AttrDict]:
        raw = self.get("accounts")
        return list(raw) if isinstance(raw, list) else []


class ExecResult(AttrDict):
    """Kernel code execution result matching Google's ExecuteCodeResult schema."""

    @property
    def stdout(self) -> str:
        return str(self.get("stdout") or "")

    @property
    def stderr(self) -> str:
        return str(self.get("stderr") or "")

    @property
    def result(self) -> Any:
        return self.get("result")

    @property
    def execution_error(self) -> Any:
        return self.get("executionError") or self.get("execution_error")

    @property
    def status(self) -> str:
        if "status" in self and self["status"]:
            return str(self["status"])
        return "error" if self.execution_error else "ok"

    @property
    def ok(self) -> bool:
        return self.status.lower() == "ok" and not self.execution_error

    @property
    def rich_outputs(self) -> list[AttrDict]:
        raw = self.get("richOutputs") or self.get("rich_outputs") or self.get("images") or []
        return list(raw) if isinstance(raw, list) else []


class ShardedExperimentResult(AttrDict):
    """Multi-account sharded experiment status & launch result."""

    @property
    def experiment_id(self) -> str:
        return str(self.get("experiment_id") or self.get("id") or "")

    @property
    def status(self) -> str:
        return str(self.get("status") or self.get("overall_status") or "RUNNING")

    @property
    def shards(self) -> list[AttrDict]:
        raw = self.get("shards")
        return list(raw) if isinstance(raw, list) else []

    @property
    def num_shards(self) -> int:
        if "num_shards" in self:
            return int(self["num_shards"])
        return len(self.shards)


class RemoteSession:
    """Handle to a pooled Google Colab CPU/GPU/TPU runtime session.

    Can be used as a context manager (`with client.session(gpu="G4") as s:`) to
    guarantee deterministic VM release and prevent idle Compute Unit drain.
    """

    def __init__(
        self,
        client: ColabHiveClient,
        data: dict[str, Any],
        *,
        stop_on_exit: bool = True,
    ) -> None:
        self._client = client
        self.data = AttrDict(data)
        self.stop_on_exit = stop_on_exit
        self._stopped = False

    @property
    def id(self) -> str:
        for key in ("session", "name", "id", "session_id"):
            val = self.data.get(key)
            if val:
                return str(val)
        raise ColabHiveError("Session payload missing session identifier", payload=self.data)

    @property
    def name(self) -> str:
        return self.id

    @property
    def accelerator(self) -> str:
        return str(
            self.data.get("assigned_accelerator")
            or self.data.get("accelerator")
            or self.data.get("gpu")
            or "CPU"
        )

    @property
    def endpoint(self) -> str:
        return str(self.data.get("endpoint") or "")

    @property
    def colab_web_url(self) -> str:
        return str(self.data.get("colab_web_url") or self.data.get("url") or "")

    def __getitem__(self, key: str) -> Any:
        return self.data[key]

    def get(self, key: str, default: Any = None) -> Any:
        return self.data.get(key, default)

    def __enter__(self) -> RemoteSession:
        return self

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        if self.stop_on_exit and not self._stopped:
            self.stop(ignore_errors=True)

    def exec(
        self,
        code: str,
        *,
        env: dict[str, str] | None = None,
        timeout_sec: float | None = None,
        inline_images: bool = False,
    ) -> ExecResult:
        return self._client.exec(
            code,
            session=self.id,
            env=env,
            timeout_sec=timeout_sec,
            inline_images=inline_images,
        )

    def upload(self, local_path: str | Path, remote_path: str | None = None) -> AttrDict:
        return self._client.upload(local_path, remote_path, session=self.id)

    def download(
        self, remote_path: str, local_path: str | Path | None = None
    ) -> Path:
        return self._client.download(remote_path, local_path, session=self.id)

    def install(
        self,
        *packages: str,
        requirements_path: str | Path | None = None,
    ) -> AttrDict:
        payload: dict[str, Any] = {"packages": list(packages)}
        if requirements_path is not None:
            req_file = Path(requirements_path)
            if not req_file.is_file():
                raise FileNotFoundError(f"Requirements file not found: {req_file}")
            remote_req = f"/content/{req_file.name}"
            self.upload(req_file, remote_req)
            payload["requirements_path"] = remote_req
        resp = self._client._request_json(
            "POST",
            [
                f"/api/v1/sessions/{urllib.parse.quote(self.id, safe='')}/install",
                f"/api/v1/sessions/{urllib.parse.quote(self.id, safe='')}/packages",
            ],
            json_body=payload,
        )
        return AttrDict(resp if isinstance(resp, dict) else {"result": resp})

    def status(self) -> AttrDict:
        resp = self._client._request_json(
            "GET",
            [f"/api/v1/sessions/{urllib.parse.quote(self.id, safe='')}"],
        )
        return AttrDict(resp if isinstance(resp, dict) else {"result": resp})

    def restart_kernel(self) -> AttrDict:
        resp = self._client._request_json(
            "POST",
            [f"/api/v1/sessions/{urllib.parse.quote(self.id, safe='')}/restart-kernel"],
            json_body={},
        )
        return AttrDict(resp if isinstance(resp, dict) else {"restarted": True})

    def url(self) -> str:
        if self.colab_web_url:
            return self.colab_web_url
        try:
            enc_id = urllib.parse.quote(self.id, safe="")
            resp = self._client._request_json(
                "GET",
                [
                    f"/api/v1/sessions/{enc_id}/open-url",
                    f"/api/v1/sessions/{enc_id}/url",
                ],
            )
            if isinstance(resp, dict) and resp.get("url"):
                return str(resp["url"])
        except ColabHiveNotFoundError:
            if not self.endpoint:
                raise
        if self.endpoint:
            enc = urllib.parse.quote(f"/tun/m/{self.endpoint}", safe="")
            return (
                f"https://colab.research.google.com/notebooks/empty.ipynb"
                f"?dbu={enc}#datalabBackendUrl=https://colab.research.google.com/tun/m/{self.endpoint}"
            )
        return ""

    def stop(self, *, ignore_errors: bool = False) -> AttrDict:
        try:
            res = self._client.stop_session(self.id)
            self._stopped = True
            return res
        except ColabHiveError:
            if ignore_errors:
                self._stopped = True
                return AttrDict({"session": self.id, "unassigned": False})
            raise


# ==============================================================================
# Keyring, OAuth Code & Credential Portability Helpers
# ==============================================================================


def escape_keyring_identifier(value: str) -> str:
    """Escape service/username exactly like `keyrings.alt.escape.escape()`.

    ASCII alphanumeric characters `[A-Za-z0-9]` remain literal; every other
    UTF-8 byte (including `-` -> `_2D` and `_` -> `_5F`) is escaped as `_%02X`.
    """
    out: list[str] = []
    for byte in value.encode("utf-8"):
        ch = chr(byte)
        if ("A" <= ch <= "Z") or ("a" <= ch <= "z") or ("0" <= ch <= "9"):
            out.append(ch)
        else:
            out.append(f"_{byte:02X}")
    return "".join(out)


def parse_plaintext_keyring(
    cfg_text: str,
    *,
    service: str = "google-colab-cli",
    username: str = "oauth_token",
) -> dict[str, Any]:
    """Parse `keyrings.alt.file.PlaintextKeyring` (`keyring_pass.cfg`) INI text."""
    cp = configparser.RawConfigParser()
    cp.read_string(cfg_text)

    section = escape_keyring_identifier(service)
    option = escape_keyring_identifier(username).lower()

    matched_section: str | None = None
    for sec in cp.sections():
        if sec.lower() == section.lower():
            matched_section = sec
            break
    if matched_section is None:
        raise ValueError(
            f"Keyring section [{section}] not found in configuration "
            f"(available sections: {cp.sections()})"
        )

    matched_opt: str | None = None
    for opt in cp.options(matched_section):
        if opt.lower() == option:
            matched_opt = opt
            break
    if matched_opt is None:
        raise ValueError(
            f"Keyring option '{option}' not found in section [{matched_section}]"
        )

    b64_raw = cp.get(matched_section, matched_opt).strip()
    decoded_bytes = base64.decodebytes(b64_raw.encode("ascii"))
    payload = json.loads(decoded_bytes.decode("utf-8"))
    if not isinstance(payload, dict):
        raise ValueError("Decoded keyring secret is not a JSON object")
    return payload


def dump_plaintext_keyring(
    token_dict: dict[str, Any],
    *,
    service: str = "google-colab-cli",
    username: str = "oauth_token",
) -> str:
    """Serialize an OAuth token dict into `keyrings.alt` `keyring_pass.cfg` INI format."""
    section = escape_keyring_identifier(service)
    option = escape_keyring_identifier(username).lower()

    cp = configparser.RawConfigParser()
    cp.add_section(section)
    encoded = base64.encodebytes(json.dumps(token_dict).encode("utf-8")).decode("ascii")
    cp.set(section, option, "\n" + encoded.rstrip("\n"))

    buf = io.StringIO()
    cp.write(buf)
    return buf.getvalue()


def _resolve_keyring_pass_from_rc(rc_path: Path, cfg_text: str) -> Path:
    """Given a `keyringrc.cfg` path & content, locate the companion `keyring_pass.cfg`."""
    cp = configparser.RawConfigParser()
    cp.read_string(cfg_text)
    if cp.has_section("backend") and cp.has_option("backend", "file-path"):
        custom = Path(cp.get("backend", "file-path").strip()).expanduser()
        if custom.is_file():
            return custom

    candidates: list[Path] = [
        rc_path.parent / "keyring_pass.cfg",
    ]
    # Linux / WSL2 XDG split: ~/.config/python_keyring/keyringrc.cfg -> ~/.local/share/python_keyring/keyring_pass.cfg
    parts = list(rc_path.parts)
    if ".config" in parts:
        idx = parts.index(".config")
        home_prefix = Path(*parts[:idx])
        candidates.append(
            home_prefix / ".local" / "share" / "python_keyring" / "keyring_pass.cfg"
        )
    xdg_data = os.environ.get("XDG_DATA_HOME")
    if xdg_data:
        candidates.append(Path(xdg_data) / "python_keyring" / "keyring_pass.cfg")

    for candidate in candidates:
        if candidate.is_file():
            return candidate

    raise FileNotFoundError(
        f"Found keyring config '{rc_path}', but companion 'keyring_pass.cfg' was not found "
        f"in {[str(c) for c in candidates]}"
    )


def load_credential_from_path(path: str | Path) -> dict[str, Any]:
    """Load & normalize Google Colab OAuth credentials from `keyringrc.cfg`,
    `keyring_pass.cfg`, `token.json`, or `gcloud` `application_default_credentials.json`.
    """
    target = Path(path).expanduser()
    if not target.is_file():
        raise FileNotFoundError(f"Credential file does not exist: {target}")

    raw_text = target.read_text(encoding="utf-8").strip()
    source_format = "unknown"
    raw_obj: dict[str, Any]

    if raw_text.startswith("{"):
        parsed = json.loads(raw_text)
        if not isinstance(parsed, dict):
            raise ValueError(f"Expected JSON object in {target}")
        if parsed.get("schema") == "colabhive.credential_bundle":
            if parsed.get("encrypted"):
                raise ValueError(
                    "Encrypted .chvault bundles must be imported via ColabHive server/CLI with passphrase"
                )
            accounts = parsed.get("accounts") or []
            if not accounts:
                raise ValueError("Credential bundle contains zero accounts")
            first = accounts[0]
            raw_obj = dict(first.get("oauth") or first)
            if first.get("email"):
                raw_obj.setdefault("email", first["email"])
            source_format = "colabhive_bundle"
        elif parsed.get("type") == "authorized_user":
            raw_obj = parsed
            source_format = "gcloud_adc"
        else:
            raw_obj = parsed
            source_format = "oauth_token_json"
    else:
        # INI format: either keyring_pass.cfg directly or keyringrc.cfg pointing to keyring_pass.cfg
        cp = configparser.RawConfigParser()
        cp.read_string(raw_text)
        expected_sec = escape_keyring_identifier("google-colab-cli").lower()
        has_token_sec = any(s.lower() == expected_sec for s in cp.sections())
        if has_token_sec:
            raw_obj = parse_plaintext_keyring(raw_text)
            source_format = "plaintext_keyring"
        else:
            pass_path = _resolve_keyring_pass_from_rc(target, raw_text)
            raw_obj = parse_plaintext_keyring(pass_path.read_text(encoding="utf-8"))
            source_format = "plaintext_keyring"

    refresh_token = str(raw_obj.get("refresh_token") or "").strip()
    access_token = str(raw_obj.get("access_token") or raw_obj.get("token") or "").strip()
    if not refresh_token and not access_token:
        raise ValueError(
            f"Credential file '{target}' does not contain a 'refresh_token' or 'access_token'"
        )

    scopes = raw_obj.get("scopes") or raw_obj.get("scope") or []
    if isinstance(scopes, str):
        scopes = [s for s in scopes.split() if s]

    return {
        "source_format": source_format,
        "source_path": str(target),
        "email": str(raw_obj.get("email") or raw_obj.get("account") or ""),
        "access_token": access_token,
        "refresh_token": refresh_token,
        "client_id": str(raw_obj.get("client_id") or OFFICIAL_COLAB_CLI_CLIENT_ID),
        "client_secret": str(
            raw_obj.get("client_secret") or OFFICIAL_COLAB_CLI_CLIENT_SECRET
        ),
        "token_uri": str(raw_obj.get("token_uri") or GOOGLE_TOKEN_URI),
        "scopes": list(scopes),
    }


def discover_local_credential_paths(*, include_wsl: bool = True) -> list[Path]:
    """Return existing local candidate paths for `colab` CLI keyring / token / ADC files.

    Note: Only checks path existence (`is_file()`); never reads secret contents
    until the user explicitly selects a path to import or donate.
    """
    home = Path.home()
    candidates: list[Path] = []

    xdg_cfg = Path(os.environ.get("XDG_CONFIG_HOME", home / ".config"))
    xdg_data = Path(os.environ.get("XDG_DATA_HOME", home / ".local" / "share"))
    candidates.extend(
        [
            xdg_data / "python_keyring" / "keyring_pass.cfg",
            xdg_cfg / "python_keyring" / "keyringrc.cfg",
            xdg_cfg / "colab-cli" / "token.json",
            xdg_cfg / "gcloud" / "application_default_credentials.json",
        ]
    )

    if sys.platform == "win32":
        local_app = os.environ.get("LOCALAPPDATA")
        app_data = os.environ.get("APPDATA")
        if local_app:
            candidates.append(Path(local_app) / "Python Keyring" / "keyring_pass.cfg")
            candidates.append(Path(local_app) / "Python Keyring" / "keyringrc.cfg")
        if app_data:
            candidates.append(
                Path(app_data) / "gcloud" / "application_default_credentials.json"
            )
        if include_wsl:
            wsl_root = Path(r"\\wsl.localhost")
            try:
                if wsl_root.exists():
                    for distro in wsl_root.iterdir():
                        home_dir = distro / "home"
                        if home_dir.is_dir():
                            for user_dir in home_dir.iterdir():
                                candidates.append(
                                    user_dir
                                    / ".local"
                                    / "share"
                                    / "python_keyring"
                                    / "keyring_pass.cfg"
                                )
                                candidates.append(
                                    user_dir
                                    / ".config"
                                    / "python_keyring"
                                    / "keyringrc.cfg"
                                )
            except OSError:
                pass

    env_adc = os.environ.get("GOOGLE_APPLICATION_CREDENTIALS")
    if env_adc:
        candidates.append(Path(env_adc))

    seen: set[str] = set()
    found: list[Path] = []
    for path in candidates:
        try:
            if path.is_file():
                key = str(path)
                if key not in seen:
                    seen.add(key)
                    found.append(path)
        except OSError:
            continue
    return found


def mask_secret(value: str, *, prefix_len: int = 6, suffix_len: int = 3) -> str:
    """Mask sensitive OAuth tokens or API keys for safe terminal display."""
    cleaned = value.strip()
    if not cleaned:
        return "<empty>"
    if len(cleaned) <= prefix_len + suffix_len + 3:
        return cleaned[:2] + "***"
    return f"{cleaned[:prefix_len]}...{cleaned[-suffix_len:]}"


def normalize_oauth_code_input(raw_input: str) -> tuple[str, str | None]:
    """Extract `(code, state)` from either a raw Google auth code (`4/0A...`) or
    a pasted callback URL (`https://sdk.cloud.google.com/...?code=4%2F0A...&state=...`).
    """
    text = raw_input.strip()
    if not text:
        raise ValueError("Authorization code input cannot be empty")

    if text.startswith(("http://", "https://")) or "code=" in text:
        parsed = urllib.parse.urlparse(text if "://" in text else f"https://dummy/?{text}")
        qs = urllib.parse.parse_qs(parsed.query)
        if not qs and parsed.fragment:
            qs = urllib.parse.parse_qs(parsed.fragment)
        codes = qs.get("code")
        if not codes or not codes[0].strip():
            raise ValueError(f"No 'code=' parameter found in pasted URL: {text}")
        states = qs.get("state")
        return codes[0].strip(), (states[0].strip() if states and states[0] else None)

    return text, None


def parse_invite_url(
    invite_url_or_code: str, default_base_url: str | None = None
) -> tuple[str, str]:
    """Parse a shareable ColabHive Contributor URL into `(base_url, invite_code)`.

    Accepts:
      - `https://hive.lab.edu/contribute/ch_inv_8x92kP`
      - `http://localhost:7860/c/ch_inv_8x92kP?lang=zh`
      - Raw invite code `ch_inv_8x92kP` (uses `default_base_url` or `COLABHIVE_URL`)
    """
    raw = invite_url_or_code.strip()
    if not raw:
        raise ValueError("Invite URL or invite code is required")

    if raw.startswith(("http://", "https://")):
        parsed = urllib.parse.urlparse(raw)
        if not parsed.scheme or not parsed.netloc:
            raise ValueError(f"Invalid invite URL: {raw}")
        segments = [seg for seg in parsed.path.split("/") if seg]
        if not segments:
            raise ValueError(f"Invite URL missing invite token path segment: {raw}")
        # Handle /contribute/<code>, /c/<code>, or /api/.../contribute/<code>
        invite_code = segments[-1]
        prefix_path = ""
        for marker in ("contribute", "c"):
            if marker in segments:
                idx = segments.index(marker)
                if idx > 0 and segments[:idx] != ["api", "public"] and segments[:idx] != ["api", "v1"]:
                    prefix_path = "/" + "/".join(segments[:idx])
                break
        base_url = f"{parsed.scheme}://{parsed.netloc}{prefix_path}".rstrip("/")
        return base_url, invite_code

    resolved_base = (
        default_base_url
        or os.environ.get("COLABHIVE_URL")
        or DEFAULT_BASE_URL
    ).rstrip("/")
    return resolved_base, raw


# ==============================================================================
# Main ColabHive SDK Client
# ==============================================================================


class ColabHiveClient:
    """Official Python SDK client for ColabHive.

    Uses `httpx` automatically when installed for connection pooling, and falls
    back cleanly to Python's standard-library `urllib.request` with zero external
    runtime dependencies.
    """

    def __init__(
        self,
        base_url: str | None = None,
        api_key: str | None = None,
        *,
        timeout: float | None = 120.0,
        strategy: str = "waste-zero",
        use_httpx: bool | None = None,
        httpx_client: Any = None,
        opener: urllib.request.OpenerDirector | None = None,
    ) -> None:
        resolved_url = (
            base_url
            if base_url is not None
            else os.environ.get("COLABHIVE_URL", DEFAULT_BASE_URL)
        ).rstrip("/")
        if not resolved_url:
            resolved_url = DEFAULT_BASE_URL
        self.base_url = resolved_url
        self.api_key = (
            api_key
            if api_key is not None
            else os.environ.get("COLABHIVE_API_KEY", "")
        ).strip()
        if timeout is None:
            self.timeout: float | None = None
        else:
            t_val = float(timeout)
            self.timeout = None if t_val <= 0 else t_val
        self.strategy = strategy
        self._opener = opener
        self._owns_httpx = False
        self._default_session_id: str | None = None
        self._client_session_key = f"py-sdk-{uuid.uuid4().hex[:12]}"

        if httpx_client is not None:
            self._httpx = httpx_client
        elif opener is not None:
            # Explicit stdlib opener supplied (e.g. custom TLS/proxy or test harness)
            self._httpx = None
        elif use_httpx is False or _httpx is None:
            self._httpx = None
        else:
            self._httpx = _httpx.Client(timeout=self.timeout)
            self._owns_httpx = True

    @property
    def transport_backend(self) -> str:
        """Returns `'httpx'` or `'urllib'` depending on active HTTP engine."""
        return "httpx" if self._httpx is not None else "urllib"

    def close(self) -> None:
        if self._owns_httpx and self._httpx is not None:
            self._httpx.close()
            self._httpx = None

    def __enter__(self) -> ColabHiveClient:
        return self

    def __exit__(self, exc_type: Any, exc: Any, tb: Any) -> None:
        self.close()

    # --------------------------------------------------------------------------
    # Low-Level Transport Layer
    # --------------------------------------------------------------------------

    def _build_headers(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        headers: dict[str, str] = {
            "Accept": "application/json",
            "User-Agent": "colabhive-python-sdk/1.0.0",
            "X-ColabHive-Session-Id": self._client_session_key,
        }
        if self.api_key:
            headers["Authorization"] = f"Bearer {self.api_key}"
            headers["X-ColabHive-Key"] = self.api_key
        if extra:
            headers.update(extra)
        return headers

    def _decode_json_bytes(self, raw_bytes: bytes) -> Any:
        if not raw_bytes:
            return {}
        text = raw_bytes.decode("utf-8", errors="replace")
        if text.startswith(XSSI_PREFIX):
            text = text[len(XSSI_PREFIX) :]
        text_stripped = text.strip()
        if not text_stripped:
            return {}
        try:
            return json.loads(text_stripped)
        except json.JSONDecodeError:
            return {"raw": text}

    def _raise_for_status(self, status_code: int, raw_bytes: bytes, url: str) -> None:
        if 200 <= status_code < 300:
            return
        payload = self._decode_json_bytes(raw_bytes)
        msg = f"HTTP {status_code} calling {url}"
        if isinstance(payload, dict):
            detail = (
                payload.get("error")
                or payload.get("message")
                or payload.get("detail")
            )
            if isinstance(detail, dict):
                detail = detail.get("message") or json.dumps(detail)
            if detail:
                msg = f"{msg}: {detail}"

        if status_code in (401, 403):
            raise ColabHiveAuthError(
                msg, status_code=status_code, payload=payload, url=url
            )
        if status_code == 404:
            raise ColabHiveNotFoundError(
                msg, status_code=status_code, payload=payload, url=url
            )
        if status_code in (402, 412, 429):
            raise ColabHiveQuotaError(
                msg, status_code=status_code, payload=payload, url=url
            )
        raise ColabHiveError(msg, status_code=status_code, payload=payload, url=url)

    def _raw_request(
        self,
        method: str,
        path: str,
        *,
        params: dict[str, Any] | None = None,
        json_body: Any = None,
        headers: dict[str, str] | None = None,
        timeout: float | None | object = _DEFAULT_TIMEOUT,
    ) -> tuple[int, dict[str, str], bytes, str]:
        url = f"{self.base_url}{path if path.startswith('/') else '/' + path}"
        if params:
            clean_params = {
                k: ("true" if v is True else "false" if v is False else str(v))
                for k, v in params.items()
                if v is not None
            }
            if clean_params:
                sep = "&" if "?" in url else "?"
                url = f"{url}{sep}{urllib.parse.urlencode(clean_params)}"

        req_headers = self._build_headers(headers)
        body_bytes: bytes | None = None
        if json_body is not None:
            body_bytes = json.dumps(json_body).encode("utf-8")
            req_headers.setdefault("Content-Type", "application/json")

        eff_timeout: float | None
        if timeout is _DEFAULT_TIMEOUT:
            eff_timeout = self.timeout
        elif timeout is None:
            eff_timeout = None
        else:
            t_num = float(timeout)  # type: ignore[arg-type]
            eff_timeout = None if t_num <= 0 else t_num

        if self._httpx is not None:
            try:
                resp = self._httpx.request(
                    method=method.upper(),
                    url=url,
                    content=body_bytes,
                    headers=req_headers,
                    timeout=eff_timeout,
                )
                resp_headers = {k.lower(): v for k, v in resp.headers.items()}
                return resp.status_code, resp_headers, resp.content, url
            except Exception as exc:
                if isinstance(exc, ColabHiveError):
                    raise
                raise ColabHiveError(
                    f"Network error calling {url}: {exc}", url=url
                ) from exc

        req = urllib.request.Request(
            url=url,
            data=body_bytes,
            headers=req_headers,
            method=method.upper(),
        )
        try:
            if self._opener is not None:
                resp_ctx = self._opener.open(req, timeout=eff_timeout)
            else:
                resp_ctx = urllib.request.urlopen(req, timeout=eff_timeout)
            with resp_ctx as resp:
                status = int(getattr(resp, "status", 200))
                resp_headers = {k.lower(): v for k, v in resp.headers.items()}
                data = resp.read()
                return status, resp_headers, data, url
        except urllib.error.HTTPError as http_err:
            try:
                err_bytes = http_err.read() if http_err.fp is not None else b""
                err_headers = (
                    {k.lower(): v for k, v in http_err.headers.items()}
                    if http_err.headers
                    else {}
                )
                return int(http_err.code), err_headers, err_bytes, url
            finally:
                http_err.close()
        except urllib.error.URLError as url_err:
            raise ColabHiveError(
                f"Connection failed for {url}: {url_err.reason}", url=url
            ) from url_err

    def _request_json(
        self,
        method: str,
        paths: str | list[str],
        *,
        params: dict[str, Any] | None = None,
        json_body: Any = None,
        headers: dict[str, str] | None = None,
        timeout: float | None | object = _DEFAULT_TIMEOUT,
    ) -> Any:
        candidate_paths = [paths] if isinstance(paths, str) else list(paths)
        last_not_found: ColabHiveNotFoundError | None = None

        for idx, path in enumerate(candidate_paths):
            status, _, raw_bytes, url = self._raw_request(
                method,
                path,
                params=params,
                json_body=json_body,
                headers=headers,
                timeout=timeout,
            )
            if status == 404 and idx < len(candidate_paths) - 1:
                try:
                    self._raise_for_status(status, raw_bytes, url)
                except ColabHiveNotFoundError as nf:
                    last_not_found = nf
                    continue
            self._raise_for_status(status, raw_bytes, url)
            return self._decode_json_bytes(raw_bytes)

        if last_not_found is not None:
            raise last_not_found
        raise ColabHiveError("No endpoint paths supplied")

    def _resolve_session_id(self, session: str | RemoteSession | None) -> str | None:
        if isinstance(session, RemoteSession):
            return session.id
        if isinstance(session, str) and session.strip():
            return session.strip()
        return self._default_session_id

    # --------------------------------------------------------------------------
    # Core Compute Pool, Session, Execution, Sharding & File Methods
    # --------------------------------------------------------------------------

    def quota(
        self, *, refresh_live: bool = False, tag: str | None = None
    ) -> QuotaSummary:
        """Fetch live pool Compute Unit quota & GPU-Hour Equivalence Matrix."""
        params: dict[str, Any] = {}
        if refresh_live:
            params["refresh"] = True
            params["refresh_live"] = True
        if tag:
            params["tag"] = tag

        payload = self._request_json(
            "GET",
            [
                "/api/v1/pool/summary",
                "/api/v1/pool/quota",
                "/api/v1/quota",
            ],
            params=params,
        )
        return QuotaSummary(payload if isinstance(payload, dict) else {"data": payload})

    def session(
        self,
        gpu: str | None = "G4",
        *,
        tpu: str | None = None,
        name: str | None = None,
        high_mem: bool = True,
        strategy: str | None = None,
        sticky_key: str | None = None,
        account: str | None = None,
        auto_fallback: bool = True,
        experiment_id: str | None = None,
        stop_on_exit: bool = True,
    ) -> RemoteSession:
        """Allocate a persistent Google Colab CPU/GPU/TPU runtime from the pool."""
        session_name = name or f"py-{uuid.uuid4().hex[:6]}"
        accel = tpu.upper() if tpu else ((gpu or "CPU").upper())
        body: dict[str, Any] = {
            "session": session_name,
            "name": session_name,
            "accelerator": accel,
            "high_mem": high_mem,
            "strategy": strategy or self.strategy,
            "auto_fallback": auto_fallback,
        }
        if tpu:
            body["tpu"] = tpu
        elif gpu and gpu.upper() != "CPU":
            body["gpu"] = gpu.upper()
        if sticky_key:
            body["sticky_key"] = sticky_key
        if account:
            body["account"] = account
            body["account_selector"] = account
        if experiment_id:
            body["experiment_id"] = experiment_id

        payload = self._request_json(
            "POST",
            ["/api/v1/sessions"],
            json_body=body,
        )
        sess_dict = payload if isinstance(payload, dict) else {"session": session_name}
        sess_dict.setdefault("session", session_name)
        remote = RemoteSession(self, sess_dict, stop_on_exit=stop_on_exit)
        self._default_session_id = remote.id
        return remote

    def list_sessions(
        self,
        *,
        account: str | None = None,
        experiment_id: str | None = None,
    ) -> list[AttrDict]:
        """List active Colab runtime sessions managed by ColabHive."""
        payload = self._request_json(
            "GET",
            ["/api/v1/sessions"],
            params={"account": account, "experiment_id": experiment_id},
        )
        if isinstance(payload, list):
            return [AttrDict(item) for item in payload]
        if isinstance(payload, dict) and isinstance(payload.get("sessions"), list):
            return [AttrDict(item) for item in payload["sessions"]]
        return []

    def stop_session(self, session: str | RemoteSession) -> AttrDict:
        """Terminate a Colab session and immediately unassign its VM."""
        sid = session.id if isinstance(session, RemoteSession) else session.strip()
        enc = urllib.parse.quote(sid, safe="")
        payload = self._request_json(
            "DELETE",
            [f"/api/v1/sessions/{enc}"],
        )
        if self._default_session_id == sid:
            self._default_session_id = None
        return AttrDict(
            payload if isinstance(payload, dict) else {"session": sid, "unassigned": True}
        )

    def exec(
        self,
        code: str,
        *,
        session: str | RemoteSession | None = None,
        gpu: str | None = None,
        env: dict[str, str] | None = None,
        timeout_sec: float | None = None,
        inline_images: bool = False,
    ) -> ExecResult:
        """Execute stateful Python/IPython code on an active or sticky Colab kernel."""
        sid = self._resolve_session_id(session)
        if timeout_sec is None:
            eff_timeout_sec = self.timeout if self.timeout is not None else 0.0
        else:
            eff_timeout_sec = max(0.0, float(timeout_sec))

        body: dict[str, Any] = {
            "code": code,
            "timeout_sec": eff_timeout_sec,
            "idle_timeout_sec": eff_timeout_sec,
            "store_history": True,
            "inline_images": inline_images,
        }
        if sid:
            body["session"] = sid
            body["session_id"] = sid
        if gpu:
            body["gpu"] = gpu.upper()
            body["auto_provision_gpu"] = gpu.upper()
        if env:
            body["env"] = env

        paths = (
            [
                f"/api/v1/sessions/{urllib.parse.quote(sid, safe='')}/exec",
                "/api/v1/exec",
            ]
            if sid
            else ["/api/v1/exec", "/api/v1/sessions/exec"]
        )
        req_timeout: float | None = (
            None
            if eff_timeout_sec <= 0
            else max(self.timeout or 0.0, eff_timeout_sec + 15.0)
        )
        payload = self._request_json(
            "POST",
            paths,
            json_body=body,
            timeout=req_timeout,
        )
        res = ExecResult(payload if isinstance(payload, dict) else {"result": payload})
        if res.get("session") and not self._default_session_id:
            self._default_session_id = str(res["session"])
        return res

    def run_sharded(
        self,
        script: str | Path,
        shards: int = 4,
        gpu: str = "G4",
        *,
        high_mem: bool = True,
        strategy: str | None = None,
        experiment_id: str | None = None,
        script_args: list[str] | None = None,
        upload_files: list[tuple[str, str] | dict[str, str]] | None = None,
        packages: list[str] | None = None,
        env: dict[str, str] | None = None,
        local_output_dir: str | Path | None = None,
        auto_stop_on_complete: bool = True,
        auto_teardown: bool | None = None,
        wait: bool = False,
        poll_interval_sec: float = 2.0,
        timeout_sec: float | None = None,
    ) -> ShardedExperimentResult:
        """Launch a parallel multi-account sharded experiment (`COLABHIVE_SHARD_ID=0..K-1`).

        `script` can be either a path to a local `.py` file (read & bundled
        automatically) or inline Python source code.
        """
        if shards < 1:
            raise ValueError("shards must be >= 1")

        exp_id = experiment_id or f"exp_{uuid.uuid4().hex[:8]}"
        script_path_str: str
        script_code: str | None = None

        script_candidate = Path(script) if isinstance(script, Path) else None
        if script_candidate is None and "\n" not in script and len(script) < 260:
            maybe_path = Path(script)
            if maybe_path.is_file():
                script_candidate = maybe_path
            elif script.endswith(".py"):
                raise FileNotFoundError(f"Local script file not found: {script}")

        if script_candidate is not None:
            if not script_candidate.is_file():
                raise FileNotFoundError(f"Local script file not found: {script_candidate}")
            script_path_str = str(script_candidate)
            script_code = script_candidate.read_text(encoding="utf-8")
            script_filename = script_candidate.name
        else:
            script_path_str = "inline_shard_worker.py"
            script_code = str(script)
            script_filename = "inline_shard_worker.py"

        normalized_uploads: list[dict[str, str]] = []
        for item in upload_files or []:
            if isinstance(item, tuple):
                local_p, remote_p = item
                local_file = Path(local_p)
                entry: dict[str, str] = {
                    "local_path": str(local_file),
                    "remote_path": remote_p,
                }
                if local_file.is_file():
                    entry["content_b64"] = base64.b64encode(
                        local_file.read_bytes()
                    ).decode("ascii")
                normalized_uploads.append(entry)
            else:
                normalized_uploads.append(dict(item))

        effective_auto_stop = (
            auto_stop_on_complete if auto_teardown is None else bool(auto_teardown)
        )
        out_dir = str(Path(local_output_dir or Path("runs") / exp_id))
        body: dict[str, Any] = {
            "experiment_id": exp_id,
            "script_path": script_path_str,
            "script_filename": script_filename,
            "script_code": script_code,
            "script_args": list(script_args or []),
            "upload_files": normalized_uploads,
            "packages": list(packages or []),
            "accelerator": gpu.upper(),
            "gpu": gpu.upper(),
            "high_mem": high_mem,
            "num_shards": int(shards),
            "shards": int(shards),
            "strategy": strategy or self.strategy,
            "local_output_dir": out_dir,
            "auto_stop_on_complete": effective_auto_stop,
        }
        if env:
            body["env"] = env

        payload = self._request_json(
            "POST",
            [
                "/api/v1/experiments",
                "/api/v1/experiments/sharded",
            ],
            json_body=body,
        )
        result = ShardedExperimentResult(
            payload if isinstance(payload, dict) else {"experiment_id": exp_id}
        )
        result.setdefault("experiment_id", exp_id)

        if wait:
            deadline = (
                time.monotonic() + float(timeout_sec)
                if (timeout_sec is not None and float(timeout_sec) > 0)
                else None
            )
            terminal_states = {
                "COMPLETED",
                "SUCCEEDED",
                "PARTIAL_FAILURE",
                "FAILED",
                "ERROR",
                "CANCELLED",
            }
            while result.status.upper() not in terminal_states:
                if deadline is not None and time.monotonic() >= deadline:
                    raise TimeoutError(
                        f"Timed out waiting for sharded experiment '{result.experiment_id}'"
                    )
                time.sleep(poll_interval_sec)
                result = self.experiment_status(result.experiment_id)

        return result

    # Alias matching product-ux-spec snippet
    map_shards = run_sharded

    def experiment_status(self, experiment_id: str) -> ShardedExperimentResult:
        """Query live status, anti-zombie health, and synced record counts for an experiment."""
        enc = urllib.parse.quote(experiment_id, safe="")
        payload = self._request_json("GET", [f"/api/v1/experiments/{enc}"])
        return ShardedExperimentResult(
            payload if isinstance(payload, dict) else {"experiment_id": experiment_id}
        )

    def collect_experiment(
        self, experiment_id: str, *, force_kill_running: bool = False
    ) -> AttrDict:
        """Merge shard JSONL checkpoints and guarantee teardown across all shard VMs."""
        enc = urllib.parse.quote(experiment_id, safe="")
        payload = self._request_json(
            "POST",
            [
                f"/api/v1/experiments/{enc}/teardown",
                f"/api/v1/experiments/{enc}/collect",
            ],
            json_body={"force_kill_running": force_kill_running},
        )
        return AttrDict(
            payload if isinstance(payload, dict) else {"experiment_id": experiment_id}
        )

    def upload(
        self,
        local_path: str | Path,
        remote_path: str | None = None,
        *,
        session: str | RemoteSession | None = None,
        chunk_size: int = 20 * 1024 * 1024,
    ) -> AttrDict:
        """Upload a local file to `/content/...` on a Colab runtime session."""
        src = Path(local_path)
        if not src.is_file():
            raise FileNotFoundError(f"Local file not found: {src}")

        sid = self._resolve_session_id(session)
        if not sid:
            raise ColabHiveError(
                "upload() requires an active session (call client.session(...) first or pass session='...')"
            )

        dest = remote_path or f"/content/{src.name}"
        raw_bytes = src.read_bytes()
        enc_sid = urllib.parse.quote(sid, safe="")
        paths = [
            f"/api/v1/sessions/{enc_sid}/files",
            f"/api/v1/sessions/{enc_sid}/files/upload",
        ]

        if len(raw_bytes) <= chunk_size or chunk_size <= 0:
            b64_data = base64.b64encode(raw_bytes).decode("ascii")
            payload = self._request_json(
                "POST",
                paths,
                json_body={
                    "name": src.name,
                    "remote_path": dest,
                    "path": dest.lstrip("/"),
                    "format": "base64",
                    "content": b64_data,
                    "content_base64": b64_data,
                    "size_bytes": len(raw_bytes),
                },
            )
        else:
            total_chunks = (len(raw_bytes) + chunk_size - 1) // chunk_size
            payload = {}
            for idx in range(total_chunks):
                part = raw_bytes[idx * chunk_size : (idx + 1) * chunk_size]
                chunk_num = -1 if idx == total_chunks - 1 else idx + 1
                b64_part = base64.b64encode(part).decode("ascii")
                payload = self._request_json(
                    "POST",
                    paths,
                    json_body={
                        "name": src.name,
                        "remote_path": dest,
                        "path": dest.lstrip("/"),
                        "format": "base64",
                        "chunk": chunk_num,
                        "content": b64_part,
                        "content_base64": b64_part,
                    },
                )

        res = AttrDict(payload if isinstance(payload, dict) else {})
        res.setdefault("uploaded", True)
        res.setdefault("remote_path", dest)
        res.setdefault("bytes", len(raw_bytes))
        return res

    def download(
        self,
        remote_path: str,
        local_path: str | Path | None = None,
        *,
        session: str | RemoteSession | None = None,
    ) -> Path:
        """Download a remote file from `/content/...` on a Colab VM to local disk."""
        sid = self._resolve_session_id(session)
        if not sid:
            raise ColabHiveError(
                "download() requires an active session (call client.session(...) first or pass session='...')"
            )

        dest = Path(local_path) if local_path is not None else Path(Path(remote_path).name)
        dest.parent.mkdir(parents=True, exist_ok=True)

        enc_sid = urllib.parse.quote(sid, safe="")
        candidates = [
            (f"/api/v1/sessions/{enc_sid}/files/download", {"path": remote_path}),
            (f"/api/v1/sessions/{enc_sid}/files", {"path": remote_path, "content": 1}),
        ]

        last_nf: ColabHiveNotFoundError | None = None
        for idx, (path, params) in enumerate(candidates):
            status, headers, raw_bytes, url = self._raw_request(
                "GET", path, params=params
            )
            if status == 404 and idx < len(candidates) - 1:
                try:
                    self._raise_for_status(status, raw_bytes, url)
                except ColabHiveNotFoundError as nf:
                    last_nf = nf
                    continue
            self._raise_for_status(status, raw_bytes, url)

            ctype = headers.get("content-type", "").strip().lower()
            if ctype.startswith("application/json"):
                decoded = self._decode_json_bytes(raw_bytes)
                if (
                    isinstance(decoded, dict)
                    and "content" in decoded
                    and (
                        "format" in decoded
                        or "name" in decoded
                        or "type" in decoded
                        or "mimetype" in decoded
                    )
                ):
                    fmt = str(decoded.get("format") or "text").lower()
                    content_val = decoded["content"]
                    if fmt == "base64" and isinstance(content_val, str):
                        dest.write_bytes(base64.b64decode(content_val.encode("ascii")))
                    elif isinstance(content_val, str):
                        dest.write_text(content_val, encoding="utf-8")
                    else:
                        dest.write_text(json.dumps(content_val), encoding="utf-8")
                    return dest.resolve()

            dest.write_bytes(raw_bytes)
            return dest.resolve()

        if last_nf is not None:
            raise last_nf
        raise ColabHiveError(f"Failed to download {remote_path}")

    # --------------------------------------------------------------------------
    # External Contributor Auth Portal & Keyring Donation Endpoints
    # --------------------------------------------------------------------------

    def get_contribute_portal(self, invite_code: str) -> AttrDict:
        """Fetch public Contributor Guest Portal metadata & default guardrails."""
        enc = urllib.parse.quote(invite_code, safe="")
        payload = self._request_json(
            "GET",
            [
                f"/api/public/contribute/{enc}",
                f"/api/v1/contribute/portal/{enc}",
            ],
        )
        return AttrDict(payload if isinstance(payload, dict) else {"invite_code": invite_code})

    def start_contribution(
        self, invite_code: str, guardrails: dict[str, Any] | None = None
    ) -> AttrDict:
        """Start a remote PKCE Google OAuth session (`colaboratory` least-privilege scope)."""
        enc = urllib.parse.quote(invite_code, safe="")
        payload = self._request_json(
            "POST",
            [
                f"/api/public/contribute/{enc}/start",
                f"/api/v1/contribute/portal/{enc}/start",
            ],
            json_body={"guardrails": guardrails or {}},
        )
        return AttrDict(payload if isinstance(payload, dict) else {})

    def complete_contribution(
        self,
        invite_code: str,
        *,
        code: str | None = None,
        state: str | None = None,
        credential: dict[str, Any] | None = None,
        guardrails: dict[str, Any] | None = None,
    ) -> AttrDict:
        """Complete an external account donation via pasted auth code OR local keyring token."""
        enc = urllib.parse.quote(invite_code, safe="")
        body: dict[str, Any] = {}
        if guardrails:
            body["guardrails"] = guardrails
        if code is not None:
            norm_code, url_state = normalize_oauth_code_input(code)
            body["code"] = norm_code
            body["raw_input"] = code
            if state or url_state:
                body["state"] = state or url_state
            paths = [
                f"/api/public/contribute/{enc}/complete",
                f"/api/v1/contribute/portal/{enc}/complete",
            ]
        elif credential is not None:
            body["credential"] = credential
            paths = [
                f"/api/public/contribute/{enc}/import",
                f"/api/public/contribute/{enc}/complete",
                f"/api/v1/contribute/portal/{enc}/complete",
            ]
        else:
            raise ValueError("Either 'code' or 'credential' must be supplied")

        payload = self._request_json("POST", paths, json_body=body)
        return AttrDict(payload if isinstance(payload, dict) else {})

    def revoke_contribution(self, receipt_token: str) -> AttrDict:
        """1-click contributor emergency VM kill + Google OAuth revoke + secret wipe."""
        enc = urllib.parse.quote(receipt_token.strip(), safe="")
        payload = self._request_json(
            "POST",
            [
                f"/api/public/contribute/manage/{enc}/revoke",
                f"/api/v1/contribute/manage/{enc}/revoke",
            ],
            json_body={},
        )
        return AttrDict(payload if isinstance(payload, dict) else {"revoked": True})
