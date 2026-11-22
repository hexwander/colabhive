"""ColabHive Python SDK & Zero-Friction Contributor CLI."""

from .client import (
    AttrDict,
    ColabHiveAuthError,
    ColabHiveClient,
    ColabHiveError,
    ColabHiveNotFoundError,
    ColabHiveQuotaError,
    ExecResult,
    QuotaSummary,
    RemoteSession,
    ShardedExperimentResult,
    discover_local_credential_paths,
    dump_plaintext_keyring,
    escape_keyring_identifier,
    load_credential_from_path,
    mask_secret,
    normalize_oauth_code_input,
    parse_invite_url,
    parse_plaintext_keyring,
)

# Convenience alias matching product-ux-spec snippets
HivePool = ColabHiveClient

__version__ = "1.0.0"

__all__ = [
    "__version__",
    "AttrDict",
    "ColabHiveAuthError",
    "ColabHiveClient",
    "ColabHiveError",
    "ColabHiveNotFoundError",
    "ColabHiveQuotaError",
    "ExecResult",
    "HivePool",
    "QuotaSummary",
    "RemoteSession",
    "ShardedExperimentResult",
    "discover_local_credential_paths",
    "dump_plaintext_keyring",
    "escape_keyring_identifier",
    "load_credential_from_path",
    "mask_secret",
    "normalize_oauth_code_input",
    "parse_invite_url",
    "parse_plaintext_keyring",
]
