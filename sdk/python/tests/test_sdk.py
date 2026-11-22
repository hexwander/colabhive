"""Comprehensive unit tests for ColabHive Python SDK & `colabhive-sdk` CLI."""

from __future__ import annotations

import base64
import io
import json
import sys
import tempfile
import threading
import unittest
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

# Ensure `python -m unittest discover -s sdk/python/tests` works without prior `pip install`
_SRC_DIR = Path(__file__).resolve().parents[1] / "src"
if str(_SRC_DIR) not in sys.path:
    sys.path.insert(0, str(_SRC_DIR))

from colabhive import (  # noqa: E402
    ColabHiveAuthError,
    ColabHiveClient,
    ColabHiveNotFoundError,
    ColabHiveQuotaError,
    HivePool,
    dump_plaintext_keyring,
    escape_keyring_identifier,
    load_credential_from_path,
    mask_secret,
    normalize_oauth_code_input,
    parse_invite_url,
    parse_plaintext_keyring,
)
from colabhive.cli import main as cli_main  # noqa: E402


class _MockHiveState:
    def __init__(self) -> None:
        self.requests: list[dict[str, Any]] = []
        self.uploaded_files: dict[str, bytes] = {
            "/content/existing.txt": b"hello from colab vm\n"
        }
        self.stopped_sessions: list[str] = []
        self.exp_polls: int = 0
        self.contributions: list[dict[str, Any]] = []
        self.revoked_receipts: list[str] = []


def _make_handler(state: _MockHiveState) -> type[BaseHTTPRequestHandler]:
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, fmt: str, *args: Any) -> None:
            return  # keep test runner output clean

        def _read_json(self) -> Any:
            length = int(self.headers.get("Content-Length", "0") or "0")
            raw = self.rfile.read(length) if length > 0 else b""
            return json.loads(raw.decode("utf-8")) if raw else {}

        def _send_json(
            self, status: int, obj: Any, *, xssi: bool = False
        ) -> None:
            text = json.dumps(obj)
            if xssi:
                text = ")]}'\n" + text
            body = text.encode("utf-8")
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def _record(self, method: str, body: Any = None) -> tuple[str, dict[str, list[str]]]:
            parsed = urllib.parse.urlparse(self.path)
            qs = urllib.parse.parse_qs(parsed.query)
            state.requests.append(
                {
                    "method": method,
                    "path": parsed.path,
                    "query": qs,
                    "auth": self.headers.get("Authorization"),
                    "body": body,
                }
            )
            return parsed.path, qs

        def do_GET(self) -> None:  # noqa: N802
            path, qs = self._record("GET")

            if path == "/api/v1/auth-check":
                if self.headers.get("Authorization") != "Bearer ch_sk_test_123":
                    self._send_json(401, {"error": "invalid api key"})
                    return
                self._send_json(200, {"ok": True})
                return

            # Simulate 404 on /api/v1/pool/summary so fallback to /api/v1/pool/quota is exercised
            if path == "/api/v1/pool/summary":
                self._send_json(404, {"error": "not mounted"})
                return

            if path == "/api/v1/pool/quota":
                self._send_json(
                    200,
                    {
                        "pool_totals": {
                            "accounts_total": 3,
                            "accounts_healthy": 3,
                            "total_available_cu": 942.5,
                            "active_burn_rate_cu_hr": 9.66,
                            "gpu_equivalent_hours": {
                                "G4_96GB": {"rate_cu_hr": 9.66, "schedulable_hours": 97.57},
                                "A100_40GB": {"rate_cu_hr": 8.88, "schedulable_hours": 106.14},
                            },
                        },
                        "accounts": [
                            {"account_id": "acc_1", "email": "lab1@uni.edu", "current_balance_cu": 482.5}
                        ],
                    },
                    xssi=True,
                )
                return

            if path == "/api/v1/sessions":
                self._send_json(
                    200,
                    {"sessions": [{"session": "py-g4", "accelerator": "G4", "status": "IDLE"}]},
                )
                return

            if path.startswith("/api/v1/sessions/") and path.endswith("/files/download"):
                remote_path = (qs.get("path") or [""])[0]
                if remote_path not in state.uploaded_files:
                    self._send_json(404, {"error": f"file not found: {remote_path}"})
                    return
                raw = state.uploaded_files[remote_path]
                self._send_json(
                    200,
                    {
                        "name": Path(remote_path).name,
                        "format": "base64",
                        "content": base64.b64encode(raw).decode("ascii"),
                    },
                )
                return

            if path.startswith("/api/v1/sessions/") and len(path.split("/")) == 5:
                sid = path.split("/")[4]
                self._send_json(
                    200,
                    {
                        "session": sid,
                        "accelerator": "G4",
                        "status": "IDLE",
                        "vram_used_gb": 12.4,
                    },
                )
                return

            if path.startswith("/api/v1/experiments/"):
                exp_id = path.split("/")[4]
                state.exp_polls += 1
                status_str = "COMPLETED" if state.exp_polls >= 2 else "RUNNING"
                self._send_json(
                    200,
                    {
                        "experiment_id": exp_id,
                        "overall_status": status_str,
                        "completed_shards": 4 if status_str == "COMPLETED" else 2,
                        "shards": [{"shard_id": i, "status": status_str} for i in range(4)],
                    },
                )
                return

            if path.startswith("/api/public/contribute/"):
                code = path.split("/")[-1]
                self._send_json(
                    200,
                    {
                        "invite_code": code,
                        "pool_name": "DeepMind Lab Cluster",
                        "default_guardrails": {"min_reserve_cu": 50.0},
                    },
                )
                return

            self._send_json(404, {"error": f"unknown GET {path}"})

        def do_POST(self) -> None:  # noqa: N802
            body = self._read_json()
            path, _ = self._record("POST", body)

            if path == "/api/v1/sessions":
                if body.get("gpu") == "H100_EXHAUSTED":
                    self._send_json(429, {"error": "pool rate limited"})
                    return
                sid = body.get("session") or "sess-01"
                ep = f"gpu-g4-s-{sid}"
                self._send_json(
                    201,
                    {
                        "session": sid,
                        "endpoint": ep,
                        "assigned_accelerator": body.get("gpu") or "CPU",
                        "account_email": "lab1@uni.edu",
                    },
                )
                return

            if path.startswith("/api/v1/sessions/") and path.endswith("/exec"):
                sid = path.split("/")[4]
                code = body.get("code", "")
                self._send_json(
                    200,
                    {
                        "session": sid,
                        "status": "ok",
                        "stdout": f"executed:{code}\n",
                        "stderr": "",
                        "result": "42",
                        "executionError": None,
                        "richOutputs": [],
                    },
                )
                return

            if path == "/api/v1/exec":
                self._send_json(
                    200,
                    {
                        "session": "sticky-auto-1",
                        "status": "ok",
                        "stdout": "sticky ok\n",
                        "result": "True",
                    },
                )
                return

            if path.startswith("/api/v1/sessions/") and (
                path.endswith("/files") or path.endswith("/files/upload")
            ):
                dest = body["remote_path"]
                chunk = body.get("chunk")
                data = base64.b64decode(body["content"].encode("ascii"))
                if chunk is None or chunk == 1:
                    state.uploaded_files[dest] = data
                else:
                    state.uploaded_files[dest] = state.uploaded_files.get(dest, b"") + data
                self._send_json(200, {"uploaded": True, "remote_path": dest})
                return

            if path.startswith("/api/v1/sessions/") and path.endswith("/install"):
                self._send_json(
                    200,
                    {"installed": True, "installer": "uv", "packages": body.get("packages", [])},
                )
                return

            if path.startswith("/api/v1/sessions/") and path.endswith("/restart-kernel"):
                self._send_json(200, {"restarted": True})
                return

            if path in ("/api/v1/experiments", "/api/v1/experiments/sharded"):
                exp_id = body["experiment_id"]
                num_shards = body["num_shards"]
                self._send_json(
                    201,
                    {
                        "experiment_id": exp_id,
                        "status": "RUNNING",
                        "num_shards": num_shards,
                        "gpu": body["gpu"],
                        "shards": [
                            {"shard_id": i, "session": f"{exp_id}_s{i}", "status": "RUNNING"}
                            for i in range(num_shards)
                        ],
                    },
                )
                return

            if path.startswith("/api/v1/experiments/") and (
                path.endswith("/teardown") or path.endswith("/collect")
            ):
                exp_id = path.split("/")[4]
                self._send_json(
                    200,
                    {
                        "experiment_id": exp_id,
                        "all_vms_unassigned": True,
                        "merged_records_count": 400,
                    },
                )
                return

            if path.startswith("/api/public/contribute/") and path.endswith("/start"):
                self._send_json(
                    200,
                    {
                        "state": "st_csrf_99",
                        "auth_url": (
                            "https://accounts.google.com/o/oauth2/auth"
                            "?client_id=764086051850&scope=colaboratory&token_usage=remote&state=st_csrf_99"
                        ),
                    },
                )
                return

            if path.startswith("/api/public/contribute/") and (
                path.endswith("/complete") or path.endswith("/import")
            ):
                state.contributions.append(body)
                self._send_json(
                    200,
                    {
                        "receipt_token": "ch_rcpt_abc789",
                        "account_summary": {
                            "email": "friend@stanford.edu",
                            "paidComputeUnitsBalance": 310.0,
                        },
                    },
                )
                return

            if path.startswith("/api/public/contribute/manage/") and path.endswith("/revoke"):
                tok = path.split("/")[5]
                state.revoked_receipts.append(tok)
                self._send_json(200, {"revoked": True, "receipt_token": tok})
                return

            self._send_json(404, {"error": f"unknown POST {path}"})

        def do_DELETE(self) -> None:  # noqa: N802
            path, _ = self._record("DELETE")
            if path.startswith("/api/v1/sessions/"):
                sid = path.split("/")[4]
                state.stopped_sessions.append(sid)
                self._send_json(200, {"session": sid, "unassigned": True})
                return
            self._send_json(404, {"error": f"unknown DELETE {path}"})

    return Handler


class TestColabHiveSDK(unittest.TestCase):
    def setUp(self) -> None:
        self.state = _MockHiveState()
        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), _make_handler(self.state))
        self.port = self.httpd.server_address[1]
        self.base_url = f"http://127.0.0.1:{self.port}"
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()
        self.thread.join(timeout=3.0)

    def test_quota_session_exec_upload_download_pure_stdlib(self) -> None:
        with ColabHiveClient(
            base_url=self.base_url,
            api_key="ch_sk_test_123",
            use_httpx=False,
        ) as client:
            self.assertEqual(client.transport_backend, "urllib")

            # 1. Quota summary (exercises 404 fallback + XSSI strip + typed/dict properties)
            quota = client.quota(refresh_live=True)
            self.assertAlmostEqual(quota.total_available_cu, 942.5)
            self.assertAlmostEqual(quota.active_burn_rate_cu_hr, 9.66)
            self.assertIn("G4_96GB", quota.gpu_equivalent_hours)
            self.assertEqual(len(quota.accounts), 1)
            self.assertEqual(quota["accounts"][0]["email"], "lab1@uni.edu")

            # 2. Sticky auto-exec before creating an explicit session
            sticky_out = client.exec("1 + 1")
            self.assertTrue(sticky_out.ok)
            self.assertEqual(sticky_out.stdout, "sticky ok\n")

            # 3. Context-managed G4 session + exec + chunked upload + download + auto-stop
            with tempfile.TemporaryDirectory() as tmp:
                tmp_dir = Path(tmp)
                src_file = tmp_dir / "payload.bin"
                payload_bytes = b"colabhive-shard-checkpoint-" * 200
                src_file.write_bytes(payload_bytes)

                with client.session(gpu="G4", name="exp_g4_0") as sess:
                    self.assertEqual(sess.id, "exp_g4_0")
                    self.assertEqual(sess.accelerator, "G4")
                    self.assertIn("datalabBackendUrl=", sess.url())

                    exec_res = client.exec("print(6 * 7)")
                    self.assertTrue(exec_res.ok)
                    self.assertEqual(exec_res.stdout, "executed:print(6 * 7)\n")
                    self.assertEqual(exec_res.result, "42")

                    # Test multi-chunk upload & roundtrip download
                    up_res = client.upload(
                        src_file, "/content/payload.bin", chunk_size=1024
                    )
                    self.assertTrue(up_res["uploaded"])
                    self.assertEqual(up_res.bytes, len(payload_bytes))

                    dst_file = tmp_dir / "downloaded" / "payload.bin"
                    out_path = client.download("/content/payload.bin", dst_file)
                    self.assertEqual(out_path.read_bytes(), payload_bytes)

                    pkg_res = sess.install("transformers", "accelerate")
                    self.assertTrue(pkg_res.installed)
                    self.assertTrue(sess.restart_kernel().restarted)
                    self.assertEqual(sess.status().status, "IDLE")

                # Verify VM was automatically unassigned on `with` exit
                self.assertIn("exp_g4_0", self.state.stopped_sessions)

    def test_run_sharded_and_collect(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            script_file = Path(tmp) / "eval_shard.py"
            script_file.write_text(
                "import os\nprint('shard', os.environ['COLABHIVE_SHARD_ID'])\n",
                encoding="utf-8",
            )
            aux_file = Path(tmp) / "prompts.jsonl"
            aux_file.write_text('{"id": 1}\n', encoding="utf-8")

            with HivePool(
                base_url=self.base_url,
                api_key="ch_sk_test_123",
                use_httpx=False,
            ) as pool:
                exp = pool.run_sharded(
                    script_file,
                    shards=4,
                    gpu="G4",
                    experiment_id="bench_qwen",
                    upload_files=[(str(aux_file), "/content/prompts.jsonl")],
                    wait=True,
                    poll_interval_sec=0.01,
                    timeout_sec=5.0,
                )
                self.assertEqual(exp.experiment_id, "bench_qwen")
                self.assertEqual(exp.status, "COMPLETED")
                self.assertEqual(exp.num_shards, 4)

                collected = pool.collect_experiment("bench_qwen")
                self.assertTrue(collected.all_vms_unassigned)
                self.assertEqual(collected.merged_records_count, 400)

    def test_error_mapping(self) -> None:
        with ColabHiveClient(
            base_url=self.base_url, api_key="wrong_key", use_httpx=False
        ) as bad_client:
            with self.assertRaises(ColabHiveAuthError) as auth_ctx:
                bad_client._request_json("GET", "/api/v1/auth-check")
            self.assertEqual(auth_ctx.exception.status_code, 401)

            with self.assertRaises(ColabHiveQuotaError) as quota_ctx:
                bad_client.session(gpu="H100_EXHAUSTED")
            self.assertEqual(quota_ctx.exception.status_code, 429)

            with self.assertRaises(ColabHiveNotFoundError):
                bad_client.session(gpu="G4", name="s1")
                bad_client.download("/content/does_not_exist.bin")

    def test_keyring_codec_and_xdg_rc_resolution(self) -> None:
        self.assertEqual(
            escape_keyring_identifier("google-colab-cli"), "google_2Dcolab_2Dcli"
        )
        self.assertEqual(escape_keyring_identifier("oauth_token"), "oauth_5Ftoken")

        fake_token = {
            "token": "ya29.fake_access_token_for_unit_test",
            "refresh_token": "1//0gFakeRefreshTokenForUnitTest999",
            "token_uri": "https://oauth2.googleapis.com/token",
            "client_id": "764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com",
            "client_secret": "d-FL95Q19q7MQmFpd7hHD0Ty",
            "scopes": ["openid", "https://www.googleapis.com/auth/colaboratory"],
        }
        ini_text = dump_plaintext_keyring(fake_token)
        self.assertIn("[google_2Dcolab_2Dcli]", ini_text)
        self.assertIn("oauth_5ftoken", ini_text)

        parsed = parse_plaintext_keyring(ini_text)
        self.assertEqual(parsed["refresh_token"], fake_token["refresh_token"])

        # Simulate Linux/WSL2 XDG split: ~/.config/python_keyring/keyringrc.cfg -> ~/.local/share/python_keyring/keyring_pass.cfg
        with tempfile.TemporaryDirectory() as tmp:
            fake_home = Path(tmp)
            rc_dir = fake_home / ".config" / "python_keyring"
            pass_dir = fake_home / ".local" / "share" / "python_keyring"
            rc_dir.mkdir(parents=True)
            pass_dir.mkdir(parents=True)

            rc_path = rc_dir / "keyringrc.cfg"
            rc_path.write_text(
                "[backend]\ndefault-keyring=keyrings.alt.file.PlaintextKeyring\n",
                encoding="utf-8",
            )
            (pass_dir / "keyring_pass.cfg").write_text(ini_text, encoding="utf-8")

            loaded = load_credential_from_path(rc_path)
            self.assertEqual(loaded["source_format"], "plaintext_keyring")
            self.assertEqual(loaded["refresh_token"], fake_token["refresh_token"])
            self.assertIn("...", mask_secret(loaded["refresh_token"]))

            # Also verify gcloud ADC JSON loading
            adc_path = fake_home / "adc.json"
            adc_path.write_text(
                json.dumps(
                    {
                        "type": "authorized_user",
                        "client_id": fake_token["client_id"],
                        "client_secret": fake_token["client_secret"],
                        "refresh_token": "1//0gAdcFakeRefresh12345",
                    }
                ),
                encoding="utf-8",
            )
            adc_loaded = load_credential_from_path(adc_path)
            self.assertEqual(adc_loaded["source_format"], "gcloud_adc")
            self.assertEqual(adc_loaded["refresh_token"], "1//0gAdcFakeRefresh12345")

    def test_oauth_url_and_invite_parsing(self) -> None:
        code, state = normalize_oauth_code_input(
            "https://sdk.cloud.google.com/applicationdefaultauthcode.html"
            "?state=csrf_abc&code=4%2F0AVGzR1B_test_code&scope=colaboratory"
        )
        self.assertEqual(code, "4/0AVGzR1B_test_code")
        self.assertEqual(state, "csrf_abc")

        raw_code, raw_state = normalize_oauth_code_input("  4/0A_direct_code  ")
        self.assertEqual(raw_code, "4/0A_direct_code")
        self.assertIsNone(raw_state)

        base, inv = parse_invite_url("https://hive.lab.edu/contribute/ch_inv_8x92kP")
        self.assertEqual(base, "https://hive.lab.edu")
        self.assertEqual(inv, "ch_inv_8x92kP")

    def test_cli_donate_oauth_and_keyring_and_revoke(self) -> None:
        invite_url = f"{self.base_url}/contribute/ch_inv_team42"

        # 1. Interactive browser copy-paste OAuth donation
        out_buf = io.StringIO()
        err_buf = io.StringIO()
        prompts: list[str] = []

        def fake_input(prompt: str) -> str:
            prompts.append(prompt)
            return (
                "https://sdk.cloud.google.com/applicationdefaultauthcode.html"
                "?code=4%2F0A_pasted_from_browser&state=st_csrf_99"
            )

        rc = cli_main(
            [
                "donate",
                invite_url,
                "--mode",
                "oauth",
                "--reserve-cu",
                "65",
                "--allow-gpu",
                "G4,A100",
                "--schedule",
                "22:00-08:00",
            ],
            input_fn=fake_input,
            stdout=out_buf,
            stderr=err_buf,
            client_factory=lambda url: ColabHiveClient(base_url=url, use_httpx=False),
        )
        self.assertEqual(rc, 0, msg=err_buf.getvalue())
        self.assertIn("ch_rcpt_abc789", out_buf.getvalue())
        self.assertEqual(len(self.state.contributions), 1)
        first_contrib = self.state.contributions[0]
        self.assertEqual(first_contrib["code"], "4/0A_pasted_from_browser")
        self.assertEqual(first_contrib["guardrails"]["min_reserve_cu"], 65.0)
        self.assertTrue(first_contrib["guardrails"]["block_drive_propagation"])
        self.assertEqual(
            first_contrib["guardrails"]["allowed_accelerators"], ["G4", "A100"]
        )

        # 2. 1-command local keyringrc.cfg transfer (`colabhive-sdk donate <url> --keyring-path ... --yes`)
        with tempfile.TemporaryDirectory() as tmp:
            tmp_dir = Path(tmp)
            rc_file = tmp_dir / "keyringrc.cfg"
            pass_file = tmp_dir / "keyring_pass.cfg"
            rc_file.write_text(
                "[backend]\ndefault-keyring=keyrings.alt.file.PlaintextKeyring\n",
                encoding="utf-8",
            )
            pass_file.write_text(
                dump_plaintext_keyring({"refresh_token": "1//0gKeyringDonateTest888"}),
                encoding="utf-8",
            )

            json_out = io.StringIO()
            rc2 = cli_main(
                [
                    "donate",
                    invite_url,
                    "--keyring-path",
                    str(rc_file),
                    "--reserve-cu",
                    "80",
                    "--yes",
                    "--json",
                ],
                stdout=json_out,
                stderr=err_buf,
                client_factory=lambda url: ColabHiveClient(base_url=url, use_httpx=False),
            )
            self.assertEqual(rc2, 0)
            parsed_json = json.loads(json_out.getvalue())
            self.assertEqual(parsed_json["receipt_token"], "ch_rcpt_abc789")
            self.assertEqual(
                self.state.contributions[1]["credential"]["refresh_token"],
                "1//0gKeyringDonateTest888",
            )

        # 3. 1-command self-service revoke + quota CLI
        rev_out = io.StringIO()
        rc3 = cli_main(
            ["revoke", f"{self.base_url}/contribute/manage/ch_rcpt_abc789"],
            stdout=rev_out,
            stderr=err_buf,
            client_factory=lambda url: ColabHiveClient(base_url=url, use_httpx=False),
        )
        self.assertEqual(rc3, 0)
        self.assertIn("ch_rcpt_abc789", self.state.revoked_receipts)

        q_out = io.StringIO()
        rc4 = cli_main(
            ["quota", "--server", self.base_url],
            stdout=q_out,
            stderr=err_buf,
            client_factory=lambda **kw: ColabHiveClient(**kw, use_httpx=False),
        )
        self.assertEqual(rc4, 0)
        self.assertIn("942.50 CU", q_out.getvalue())

    def test_auto_transport_selection(self) -> None:
        # Exercises default transport (httpx when installed, urllib otherwise)
        with ColabHiveClient(
            base_url=self.base_url, api_key="ch_sk_test_123"
        ) as client:
            self.assertIn(client.transport_backend, ("httpx", "urllib"))
            q = client.quota()
            self.assertAlmostEqual(q.total_available_cu, 942.5)
            sessions = client.list_sessions()
            self.assertEqual(len(sessions), 1)
            self.assertEqual(sessions[0].session, "py-g4")


if __name__ == "__main__":
    unittest.main()
