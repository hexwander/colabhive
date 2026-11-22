"""`colabhive-sdk` CLI — 1-command Contributor Account Donation & Pool Helper."""

from __future__ import annotations

import argparse
import json
import sys
import webbrowser
from pathlib import Path
from typing import Any, Callable, TextIO

from . import __version__
from .client import (
    ColabHiveClient,
    ColabHiveError,
    discover_local_credential_paths,
    load_credential_from_path,
    mask_secret,
    normalize_oauth_code_input,
    parse_invite_url,
)


def _build_guardrails_from_args(args: argparse.Namespace) -> dict[str, Any]:
    gpus = [
        g.strip().upper()
        for g in (args.allow_gpu or "T4,L4,G4,A100").split(",")
        if g.strip()
    ]
    guardrails: dict[str, Any] = {
        "min_reserve_cu": float(args.reserve_cu),
        "allow_paid_cu_burn": not bool(args.free_only),
        "allowed_gpus": gpus,
        "allowed_accelerators": gpus,
        "max_concurrent_vms": int(args.max_vms),
        "max_concurrent_runtimes": int(args.max_vms),
        "allow_credential_propagation": False,
        "block_drive_propagation": True,
        "mask_email_in_dashboard": bool(args.mask_email),
    }
    if args.max_cu is not None:
        guardrails["max_donated_cu_total"] = float(args.max_cu)
    if args.daily_cu is not None:
        guardrails["max_donated_cu_daily"] = float(args.daily_cu)
    if args.proxy:
        guardrails["proxy_url"] = args.proxy.strip()
    if args.schedule:
        parts = args.schedule.strip().split("-", 1)
        if len(parts) != 2:
            raise ValueError(
                f"Invalid --schedule '{args.schedule}'; expected HH:MM-HH:MM (e.g. 22:00-09:00)"
            )
        win = {
            "enabled": True,
            "timezone": args.timezone or "UTC",
            "days_of_week": [0, 1, 2, 3, 4, 5, 6],
            "start_hour": parts[0].strip(),
            "end_hour": parts[1].strip(),
        }
        guardrails["schedule_window"] = win
        guardrails["active_window"] = win
    return guardrails


def _extract_receipt_url_and_token(
    base_url: str, response: dict[str, Any]
) -> tuple[str, str]:
    receipt_token = str(
        response.get("receipt_token") or response.get("receiptToken") or ""
    )
    receipt_url = str(
        response.get("manage_url")
        or response.get("manageUrl")
        or response.get("receipt_url")
        or response.get("receiptUrl")
        or ""
    )
    if receipt_token and not receipt_url:
        receipt_url = f"{base_url}/contribute/manage/{receipt_token}"
    return receipt_token, receipt_url


def _cmd_donate(
    args: argparse.Namespace,
    *,
    input_fn: Callable[[str], str] = input,
    stdout: TextIO = sys.stdout,
    stderr: TextIO = sys.stderr,
    client_factory: Callable[[str], ColabHiveClient] | None = None,
) -> int:
    try:
        base_url, invite_code = parse_invite_url(args.invite_url, args.server)
        guardrails = _build_guardrails_from_args(args)
    except ValueError as exc:
        print(f"[colabhive-sdk] Error: {exc}", file=stderr)
        return 2

    make_client = client_factory or (lambda url: ColabHiveClient(base_url=url))
    client = make_client(base_url)

    try:
        # Best-effort fetch of public invite metadata for friendly banner
        portal_info: dict[str, Any] = {}
        try:
            portal_info = client.get_contribute_portal(invite_code)
        except ColabHiveError:
            portal_info = {}

        pool_name = (
            portal_info.get("pool_title")
            or portal_info.get("pool_name")
            or portal_info.get("title")
            or base_url
        )

        if not args.json:
            print(
                f"=== ColabHive Contributor Onboarding ({pool_name}) ===",
                file=stdout,
            )
            print(
                f"  Guardrails : Keep >= {guardrails['min_reserve_cu']:.1f} CU personal reserve | "
                f"Allowed GPUs: {','.join(guardrails['allowed_accelerators'])} | "
                f"Max VMs: {guardrails['max_concurrent_runtimes']} | "
                f"Google Drive Propagation: BLOCKED",
                file=stdout,
            )

        # Determine donation mode:
        #   1) Explicit --keyring-path / --token-file -> local credential transfer
        #   2) Explicit --auth-code or --mode oauth -> remote OAuth copy-paste flow
        #   3) Interactive auto mode -> offer detected local keyring OR browser OAuth
        explicit_cred_path = args.keyring_path or args.token_file
        chosen_mode = args.mode

        if explicit_cred_path:
            chosen_mode = "keyring"
        elif args.auth_code:
            chosen_mode = "oauth"
        elif chosen_mode == "auto":
            discovered = discover_local_credential_paths()
            if discovered and not args.yes:
                print("\nDetected local Colab credential file(s):", file=stdout)
                for idx, p in enumerate(discovered, start=1):
                    print(f"  [{idx}] Transfer local credential: {p}", file=stdout)
                print(
                    f"  [0] Authorize a Google account via Browser Copy-Paste OAuth (least-privilege 'colaboratory' scope)",
                    file=stdout,
                )
                choice = input_fn("Select option [0]: ").strip()
                if choice and choice != "0":
                    try:
                        sel_idx = int(choice) - 1
                        if 0 <= sel_idx < len(discovered):
                            explicit_cred_path = str(discovered[sel_idx])
                            chosen_mode = "keyring"
                    except ValueError:
                        pass
            if chosen_mode == "auto":
                chosen_mode = "oauth"

        if chosen_mode == "keyring":
            if not explicit_cred_path:
                discovered = discover_local_credential_paths()
                if not discovered:
                    print(
                        "[colabhive-sdk] No local keyringrc.cfg / keyring_pass.cfg / ADC found. "
                        "Pass --keyring-path <PATH> or use --mode oauth.",
                        file=stderr,
                    )
                    return 1
                explicit_cred_path = str(discovered[0])

            cred = load_credential_from_path(explicit_cred_path)
            masked_tok = mask_secret(
                cred.get("refresh_token") or cred.get("access_token") or ""
            )
            if not args.json:
                print(
                    f"  Loaded     : {cred['source_format']} from {cred['source_path']} "
                    f"(token: {masked_tok})",
                    file=stdout,
                )
            if not args.yes:
                confirm = input_fn(
                    f"Upload this credential to {base_url} with the guardrails above? [y/N]: "
                ).strip().lower()
                if confirm not in ("y", "yes"):
                    print("[colabhive-sdk] Donation cancelled.", file=stderr)
                    return 1

            resp = client.complete_contribution(
                invite_code,
                credential=cred,
                guardrails=guardrails,
            )
        else:
            start_resp = client.start_contribution(invite_code, guardrails=guardrails)
            auth_url = str(
                start_resp.get("auth_url") or start_resp.get("authUrl") or ""
            )
            state = str(start_resp.get("state") or "") or None

            if not args.auth_code:
                if not auth_url:
                    raise ColabHiveError(
                        "Server did not return an OAuth authorization URL",
                        payload=start_resp,
                    )
                print(
                    "\nStep 1: Open this official Google authorization URL in any browser\n"
                    "        (Requests 'colaboratory' compute scope ONLY — zero Google Drive access):\n",
                    file=stdout,
                )
                print(f"  {auth_url}\n", file=stdout)
                if args.open_browser:
                    try:
                        webbrowser.open(auth_url)
                    except Exception:
                        pass
                raw_pasted = input_fn(
                    "Step 2: Paste the Google authorization code (4/0A...) OR full redirect URL:\n> "
                ).strip()
            else:
                raw_pasted = args.auth_code

            norm_code, pasted_state = normalize_oauth_code_input(raw_pasted)
            resp = client.complete_contribution(
                invite_code,
                code=norm_code,
                state=pasted_state or state,
                guardrails=guardrails,
            )

        receipt_token, receipt_url = _extract_receipt_url_and_token(base_url, resp)
        if args.json:
            out = resp.to_dict() if hasattr(resp, "to_dict") else dict(resp)
            if receipt_url:
                out.setdefault("receipt_url", receipt_url)
            print(json.dumps(out, indent=2), file=stdout)
        else:
            acct = (
                resp.get("status")
                or resp.get("account_summary")
                or resp.get("account")
                or resp
            )
            email = (
                acct.get("masked_email")
                or acct.get("email")
                or "verified-account"
            )
            balance = (
                acct.get("paid_cu_balance")
                if acct.get("paid_cu_balance") is not None
                else acct.get("paidComputeUnitsBalance")
                or acct.get("current_balance_cu")
            )
            print("\n[OK] Thank you! Account successfully contributed to ColabHive.", file=stdout)
            print(f"  Account       : {email}", file=stdout)
            if balance is not None:
                print(f"  Live Balance  : {balance} CU (Personal reserve floor: {guardrails['min_reserve_cu']} CU)", file=stdout)
            if receipt_token:
                print(f"  Receipt Token : {receipt_token}", file=stdout)
            if receipt_url:
                print(f"  Manage/Revoke : {receipt_url}", file=stdout)
                print(
                    f"  1-CLI Revoke  : colabhive-sdk revoke {receipt_token} --server {base_url}",
                    file=stdout,
                )
        return 0
    except (FileNotFoundError, ValueError, ColabHiveError) as exc:
        print(f"[colabhive-sdk] Donation failed: {exc}", file=stderr)
        return 1
    finally:
        client.close()


def _cmd_revoke(
    args: argparse.Namespace,
    *,
    stdout: TextIO = sys.stdout,
    stderr: TextIO = sys.stderr,
    client_factory: Callable[[str], ColabHiveClient] | None = None,
) -> int:
    raw = args.receipt.strip()
    base_url = (args.server or "").rstrip("/")
    token = raw
    if raw.startswith(("http://", "https://")):
        base_url, token = parse_invite_url(raw, args.server)
    elif not base_url:
        base_url = "http://localhost:7860"

    make_client = client_factory or (lambda url: ColabHiveClient(base_url=url))
    client = make_client(base_url)
    try:
        res = client.revoke_contribution(token)
        if args.json:
            print(json.dumps(res.to_dict(), indent=2), file=stdout)
        else:
            print(
                f"[OK] Contribution '{token}' revoked: active VMs unassigned and OAuth token wiped.",
                file=stdout,
            )
        return 0
    except ColabHiveError as exc:
        print(f"[colabhive-sdk] Revoke failed: {exc}", file=stderr)
        return 1
    finally:
        client.close()


def _cmd_quota(
    args: argparse.Namespace,
    *,
    stdout: TextIO = sys.stdout,
    stderr: TextIO = sys.stderr,
    client_factory: Callable[..., ColabHiveClient] | None = None,
) -> int:
    make_client = client_factory or ColabHiveClient
    client = make_client(base_url=args.server, api_key=args.api_key)
    try:
        q = client.quota(refresh_live=args.refresh)
        if args.json:
            print(json.dumps(q.to_dict(), indent=2), file=stdout)
        else:
            print(f"Total Available CUs : {q.total_available_cu:.2f} CU", file=stdout)
            print(f"Active Burn Rate    : {q.active_burn_rate_cu_hr:.2f} CU/hr", file=stdout)
            eq = q.gpu_equivalent_hours
            if eq:
                print("Equivalent Runtime  :", file=stdout)
                for tier, info in eq.items():
                    hrs = info.get("schedulable_hours", info) if isinstance(info, dict) else info
                    print(f"  - {tier:<12}: {hrs} hrs", file=stdout)
        return 0
    except ColabHiveError as exc:
        print(f"[colabhive-sdk] Quota check failed: {exc}", file=stderr)
        return 1
    finally:
        client.close()


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="colabhive-sdk",
        description="ColabHive Python SDK & Zero-Friction Contributor CLI",
    )
    parser.add_argument(
        "--version", action="version", version=f"colabhive-sdk {__version__}"
    )
    sub = parser.add_subparsers(dest="command", required=True)

    # donate <invite_url>
    p_donate = sub.add_parser(
        "donate",
        help="Contribute a Google Colab account or local keyringrc.cfg to a ColabHive pool in 1 command",
    )
    p_donate.add_argument(
        "invite_url",
        help="Shareable invite URL (https://<host>/contribute/<invite_code>) or invite code",
    )
    p_donate.add_argument(
        "--server",
        default=None,
        help="ColabHive base URL when passing a bare invite code",
    )
    p_donate.add_argument(
        "--mode",
        choices=["auto", "oauth", "keyring"],
        default="auto",
        help="Onboarding mode: interactive auto-detect, remote copy-paste OAuth, or local keyring transfer",
    )
    p_donate.add_argument(
        "--keyring-path",
        default=None,
        help="Path to local keyringrc.cfg or keyring_pass.cfg to transfer directly",
    )
    p_donate.add_argument(
        "--token-file",
        default=None,
        help="Path to local token.json or gcloud application_default_credentials.json",
    )
    p_donate.add_argument(
        "--auth-code",
        default=None,
        help="Non-interactive Google authorization code (4/0A...) or pasted callback URL",
    )
    p_donate.add_argument(
        "--reserve-cu",
        type=float,
        default=50.0,
        help="Personal safety reserve floor in Compute Units that ColabHive will never spend below (default: 50.0)",
    )
    p_donate.add_argument(
        "--max-cu",
        type=float,
        default=None,
        help="Optional maximum total CUs to contribute",
    )
    p_donate.add_argument(
        "--daily-cu",
        type=float,
        default=None,
        help="Optional maximum daily CUs to contribute",
    )
    p_donate.add_argument(
        "--allow-gpu",
        default="T4,L4,G4,A100",
        help="Comma-separated list of allowed accelerators (default: T4,L4,G4,A100)",
    )
    p_donate.add_argument(
        "--max-vms",
        type=int,
        default=1,
        help="Maximum concurrent VMs allowed on this account (default: 1)",
    )
    p_donate.add_argument(
        "--free-only",
        action="store_true",
        help="Forbid Paid CU burn (only schedule Free-tier GPU/CPU windows)",
    )
    p_donate.add_argument(
        "--schedule",
        default=None,
        help="Allowed active window in HH:MM-HH:MM format (e.g. 22:00-09:00)",
    )
    p_donate.add_argument(
        "--timezone",
        default="UTC",
        help="Timezone for --schedule (default: UTC)",
    )
    p_donate.add_argument(
        "--proxy",
        default=None,
        help="Optional dedicated http:// or socks5:// egress proxy URL for this account",
    )
    p_donate.add_argument(
        "--mask-email",
        action="store_true",
        help="Mask your email address in the pool dashboard by default",
    )
    p_donate.add_argument(
        "--open-browser",
        action="store_true",
        help="Open the Google OAuth consent URL in the local browser automatically",
    )
    p_donate.add_argument(
        "-y",
        "--yes",
        action="store_true",
        help="Skip interactive confirmation prompts",
    )
    p_donate.add_argument(
        "--json",
        action="store_true",
        help="Emit machine-readable JSON output",
    )

    # revoke <receipt_url_or_token>
    p_revoke = sub.add_parser(
        "revoke",
        help="Emergency 1-command revoke & VM kill using your Contributor Receipt Token",
    )
    p_revoke.add_argument(
        "receipt",
        help="Receipt URL (https://<host>/contribute/manage/ch_rcpt_...) or token (ch_rcpt_...)",
    )
    p_revoke.add_argument("--server", default=None, help="ColabHive server base URL")
    p_revoke.add_argument("--json", action="store_true", help="Emit JSON output")

    # quota
    p_quota = sub.add_parser(
        "quota",
        help="Display pool Compute Unit balance & GPU-Hour Equivalence Matrix",
    )
    p_quota.add_argument("--server", default=None, help="ColabHive server base URL")
    p_quota.add_argument("--api-key", default=None, help="ColabHive API key (ch_sk_...)")
    p_quota.add_argument(
        "--refresh", action="store_true", help="Force live ccu-info poll"
    )
    p_quota.add_argument("--json", action="store_true", help="Emit JSON output")

    return parser


def main(
    argv: list[str] | None = None,
    *,
    input_fn: Callable[[str], str] = input,
    stdout: TextIO = sys.stdout,
    stderr: TextIO = sys.stderr,
    client_factory: Callable[..., ColabHiveClient] | None = None,
) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)

    if args.command == "donate":
        return _cmd_donate(
            args,
            input_fn=input_fn,
            stdout=stdout,
            stderr=stderr,
            client_factory=client_factory,
        )
    if args.command == "revoke":
        return _cmd_revoke(
            args,
            stdout=stdout,
            stderr=stderr,
            client_factory=client_factory,
        )
    if args.command == "quota":
        return _cmd_quota(
            args,
            stdout=stdout,
            stderr=stderr,
            client_factory=client_factory,
        )
    parser.print_help(stderr)
    return 2


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(main())
