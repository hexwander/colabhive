"""Regression unit tests for ColabHiveClient edge cases (PARTIAL_FAILURE, raw JSON download, timeout_sec=0, sticky_key/auto_teardown)."""

from __future__ import annotations

import io
import json
import sys
import tempfile
import threading
import unittest
import urllib.parse
import urllib.request
from email.message import Message
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

_SRC_DIR = Path(__file__).resolve().parents[1] / "src"
if str(_SRC_DIR) not in sys.path:
    sys.path.insert(0, str(_SRC_DIR))

from colabhive import ColabHiveClient, HivePool  # noqa: E402


class _CaptureTimeoutOpener(urllib.request.OpenerDirector):
    """Custom opener capturing exact `timeout` values passed by `_raw_request`."""

    def __init__(self, response_obj: dict[str, Any]) -> None:
        super().__init__()
        self.timeouts: list[float | None] = []
        self.bodies: list[dict[str, Any]] = []
        self._response_bytes = json.dumps(response_obj).encode("utf-8")

    def open(self, fullurl: Any, data: Any = None, timeout: Any = "UNSET") -> Any:  # type: ignore[override]
        self.timeouts.append(timeout)
        if isinstance(fullurl, urllib.request.Request) and fullurl.data:
            self.bodies.append(json.loads(fullurl.data.decode("utf-8")))
        hdrs = Message()
        hdrs["Content-Type"] = "application/json"

        class _FakeResp(io.BytesIO):
            status = 200
            headers = hdrs

        return _FakeResp(self._response_bytes)


class TestColabHiveClientRegressions(unittest.TestCase):
    def setUp(self) -> None:
        self.requests: list[dict[str, Any]] = []
        self.exp_polls = 0
        self.raw_json_bytes = json.dumps(
            {
                "id": "sample_01",
                "format": "base64",
                "content": "not_base64_and_must_not_be_unwrapped!!!",
                "nested": {"score": 0.99},
            }
        ).encode("utf-8")

        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, fmt: str, *args: Any) -> None:
                return

            def _read_json(self) -> Any:
                length = int(self.headers.get("Content-Length", "0") or "0")
                raw = self.rfile.read(length) if length > 0 else b""
                return json.loads(raw.decode("utf-8")) if raw else {}

            def _send_json(self, status: int, obj: Any) -> None:
                raw = json.dumps(obj).encode("utf-8")
                self.send_response(status)
                self.send_header("Content-Type", "application/json; charset=utf-8")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

            def do_GET(self) -> None:  # noqa: N802
                parsed = urllib.parse.urlparse(self.path)
                qs = urllib.parse.parse_qs(parsed.query)
                outer.requests.append({"method": "GET", "path": parsed.path, "qs": qs})

                if parsed.path.endswith("/files/download"):
                    rem = (qs.get("path") or [""])[0]
                    if rem == "/content/results.json":
                        self.send_response(200)
                        self.send_header("Content-Type", "application/octet-stream")
                        self.send_header("Content-Length", str(len(outer.raw_json_bytes)))
                        self.end_headers()
                        self.wfile.write(outer.raw_json_bytes)
                        return
                    if rem == "/content/envelope.txt":
                        self._send_json(
                            200,
                            {
                                "name": "envelope.txt",
                                "format": "text",
                                "content": "unwrapped envelope text",
                            },
                        )
                        return

                if parsed.path.startswith("/api/v1/experiments/"):
                    exp_id = parsed.path.split("/")[4]
                    outer.exp_polls += 1
                    st = "PARTIAL_FAILURE" if outer.exp_polls >= 2 else "RUNNING"
                    self._send_json(
                        200,
                        {
                            "experiment_id": exp_id,
                            "status": st,
                            "shards": [
                                {"shard_id": 0, "status": "COMPLETED"},
                                {"shard_id": 1, "status": "FAILED"},
                            ],
                        },
                    )
                    return

                self._send_json(404, {"error": "not found"})

            def do_POST(self) -> None:  # noqa: N802
                parsed = urllib.parse.urlparse(self.path)
                body = self._read_json()
                outer.requests.append({"method": "POST", "path": parsed.path, "body": body})

                if parsed.path == "/api/v1/sessions":
                    self._send_json(
                        200,
                        {
                            "session": body.get("session") or "sess-sticky",
                            "assigned_accelerator": body.get("gpu") or "G4",
                            "sticky_key": body.get("sticky_key"),
                        },
                    )
                    return

                if parsed.path == "/api/v1/experiments":
                    self._send_json(
                        201,
                        {
                            "experiment_id": body["experiment_id"],
                            "status": "RUNNING",
                            "num_shards": body["num_shards"],
                        },
                    )
                    return

                self._send_json(404, {"error": "not found"})

            def do_DELETE(self) -> None:  # noqa: N802
                self._send_json(200, {"unassigned": True})

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.base_url = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    def tearDown(self) -> None:
        self.httpd.shutdown()
        self.httpd.server_close()
        self.thread.join(timeout=3.0)

    def test_download_preserves_octet_stream_json_and_unwraps_json_envelope(self) -> None:
        with ColabHiveClient(base_url=self.base_url, use_httpx=False) as client:
            with tempfile.TemporaryDirectory() as tmp:
                tmp_dir = Path(tmp)
                with client.session(gpu="G4", sticky_key="sticky-dl") as sess:
                    raw_out = sess.download(
                        "/content/results.json", tmp_dir / "results.json"
                    )
                    self.assertEqual(raw_out.read_bytes(), self.raw_json_bytes)

                    env_out = sess.download(
                        "/content/envelope.txt", tmp_dir / "envelope.txt"
                    )
                    self.assertEqual(
                        env_out.read_text(encoding="utf-8"), "unwrapped envelope text"
                    )

    def test_partial_failure_terminates_wait_and_auto_teardown_alias(self) -> None:
        with HivePool(base_url=self.base_url, use_httpx=False) as pool:
            res = pool.map_shards(
                script="print('hello shard')\n",
                shards=2,
                gpu="G4",
                experiment_id="exp_partial",
                auto_teardown=False,
                wait=True,
                poll_interval_sec=0.01,
                timeout_sec=0,
            )
            self.assertEqual(res.status, "PARTIAL_FAILURE")
            self.assertGreaterEqual(self.exp_polls, 2)

            exp_posts = [
                r["body"]
                for r in self.requests
                if r["method"] == "POST" and r["path"] == "/api/v1/experiments"
            ]
            self.assertEqual(len(exp_posts), 1)
            self.assertFalse(exp_posts[0]["auto_stop_on_complete"])

    def test_sticky_key_forwarded_on_session(self) -> None:
        with HivePool(base_url=self.base_url, use_httpx=False) as pool:
            with pool.session(gpu="G4", strategy="waste-zero", sticky_key="exp-qwen27b") as vm:
                self.assertEqual(vm.get("sticky_key"), "exp-qwen27b")
            sess_posts = [
                r["body"]
                for r in self.requests
                if r["method"] == "POST" and r["path"] == "/api/v1/sessions"
            ]
            self.assertEqual(len(sess_posts), 1)
            self.assertEqual(sess_posts[0]["sticky_key"], "exp-qwen27b")

    def test_exec_timeout_sec_zero_means_unlimited_timeout(self) -> None:
        opener = _CaptureTimeoutOpener(
            {"session": "s_unlim", "status": "ok", "stdout": "done\n"}
        )
        client = ColabHiveClient(
            base_url="http://127.0.0.1:9",
            timeout=90.0,
            opener=opener,
        )
        # 1. Explicit timeout_sec=0 -> unlimited (opener receives timeout=None, body timeout_sec=0.0)
        res_unlim = client.exec("long_train()", timeout_sec=0)
        self.assertTrue(res_unlim.ok)
        self.assertIsNone(opener.timeouts[0])
        self.assertEqual(opener.bodies[0]["timeout_sec"], 0.0)

        # 2. Default timeout_sec=None -> inherits client.timeout (90.0) + 15.0 buffer = 105.0
        res_def = client.exec("quick_eval()")
        self.assertTrue(res_def.ok)
        self.assertAlmostEqual(opener.timeouts[1] or 0.0, 105.0)
        self.assertEqual(opener.bodies[1]["timeout_sec"], 90.0)


if __name__ == "__main__":
    unittest.main()
