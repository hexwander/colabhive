package appstore

import (
	"encoding/base64"
	"strings"
)

func init() {
	b64 := base64.StdEncoding.EncodeToString([]byte(wangpHeadlessWorkerPy))
	for i := range builtinTemplates {
		if builtinTemplates[i].ID == "wangp" {
			builtinTemplates[i].BootstrapScript = strings.ReplaceAll(
				builtinTemplates[i].BootstrapScript,
				"__COLABHIVE_WANGP_WORKER_B64__",
				b64,
			)
		}
	}
}

// wangpHeadlessWorkerPy implements ColabHive's standard `media.v1` JSON + SSE engine contract
// directly on top of DeepBeepMeep's official WanGP `shared/api.py` (zero Gradio 23 MB web overhead,
// keeping the active model warm in GPU VRAM, streaming live denoising previews and download logs).
// Attribution: Powered by WanGP (DeepBeepMeep) per WanGP API Terms and Conditions.
const wangpHeadlessWorkerPy = `#!/usr/bin/env python3
import base64
import email.parser
import email.policy
import io
import json
import mimetypes
import os
import queue
import sys
import threading
import time
import traceback
import urllib.parse
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

APP_DIR = os.environ.get("COLABHIVE_WANGP_DIR", "/content/WanGP")
OUTPUT_DIR = os.environ.get("COLABHIVE_CHECKPOINT_DIR", "/content/WanGP/outputs")
UPLOAD_DIR = os.path.join(APP_DIR, "_ch_uploads")
PORT = int(os.environ.get("COLABHIVE_REMOTE_PORT", "7861"))

os.makedirs(OUTPUT_DIR, exist_ok=True)
os.makedirs(UPLOAD_DIR, exist_ok=True)
if APP_DIR not in sys.path:
    sys.path.insert(0, APP_DIR)
os.chdir(APP_DIR)

print("[colabhive-media-worker] Initializing WanGP shared.api session (Powered by WanGP / DeepBeepMeep)...", flush=True)
from shared.api import init as wangp_init  # noqa: E402

SESSION = wangp_init(
    root=Path(APP_DIR),
    output_dir=Path(OUTPUT_DIR),
    cli_args=["--attention", "sdpa"],
    console_output=True,
    console_isatty=False,
)
print("[colabhive-media-worker] WanGP shared.api session ready.", flush=True)

JOBS_LOCK = threading.Lock()
JOBS = {}
EXEC_QUEUE = queue.Queue()
FILES_LOCK = threading.Lock()
FILES = {}

FEATURED_TOKENS = (
    "z_image",
    "flux",
    "qwen",
    "fibo",
    "wan22",
    "wan2_2",
    "ltx2",
    "minimax",
    "h3",
    "hunyuan",
)


def _guess_kind(path_str, mime_type):
    ext = os.path.splitext(path_str)[1].lower()
    if ext in (".mp4", ".webm", ".mov", ".mkv") or (mime_type or "").startswith("video/"):
        return "video"
    if ext in (".wav", ".mp3", ".ogg", ".flac", ".m4a") or (mime_type or "").startswith("audio/"):
        return "audio"
    return "image"


def _is_cached(meta):
    avail = meta.get("availability")
    if isinstance(avail, dict):
        if avail.get("ready") or avail.get("downloaded") or avail.get("available"):
            return True
        st = str(avail.get("status") or "").lower()
        if st in ("ready", "downloaded", "local", "complete", "available"):
            return True
    return False


def _serialize_models():
    raw_list = SESSION.list_model_metadata(include_availability=True) or []
    out = []
    for item in raw_list:
        if not isinstance(item, dict):
            continue
        meta = item.get("metadata") if isinstance(item.get("metadata"), dict) else item
        mid = str(meta.get("model_type") or item.get("model_type") or "").strip()
        if not mid:
            continue
        name = str(item.get("name") or meta.get("name") or mid).strip()
        fam = str(meta.get("family") or "").strip()
        fam_label = str(meta.get("family_label") or fam).strip()
        desc = str(item.get("description") or meta.get("description") or "").strip()
        main_out = meta.get("main_output") or []
        if isinstance(main_out, str):
            main_out = [main_out]
        outputs = meta.get("outputs") or main_out
        if isinstance(outputs, str):
            outputs = [outputs]
        inputs = meta.get("inputs") or ["text"]
        if isinstance(inputs, str):
            inputs = [inputs]
        caps = meta.get("capabilities") if isinstance(meta.get("capabilities"), dict) else {}
        low = (mid + " " + name + " " + fam).lower()
        featured = any(tok in low for tok in FEATURED_TOKENS)
        cached = _is_cached(meta) or _is_cached(item)
        out.append({
            "id": mid,
            "name": name,
            "family": fam,
            "family_label": fam_label,
            "description": desc[:280],
            "main_output": [str(x) for x in main_out],
            "outputs": [str(x) for x in outputs],
            "inputs": [str(x) for x in inputs],
            "capabilities": {str(k): bool(v) for k, v in caps.items()},
            "cached": cached,
            "featured": featured,
        })
    out.sort(key=lambda m: (0 if m["cached"] else 1, 0 if m["featured"] else 1, m["family_label"].lower(), m["name"].lower()))
    return out


class _JobRecord:
    def __init__(self, job_id):
        self.job_id = job_id
        self.api_job = None
        self.lock = threading.Lock()
        self.history = []
        self.subs = []
        self.finished = False
        self.canceled = False

    def push(self, evt):
        with self.lock:
            self.history.append(evt)
            if len(self.history) > 250:
                self.history = self.history[-250:]
            for q in list(self.subs):
                try:
                    q.put_nowait(evt)
                except Exception:
                    pass
            if evt.get("type") in ("done", "error", "canceled"):
                self.finished = True

    def subscribe(self):
        q = queue.Queue(maxsize=256)
        with self.lock:
            snap = list(self.history)
            done = self.finished
            if not done:
                self.subs.append(q)
        return snap, q, done

    def unsubscribe(self, q):
        with self.lock:
            if q in self.subs:
                self.subs.remove(q)


def _encode_preview_jpeg(pil_img):
    if pil_img is None:
        return ""
    try:
        img = pil_img.convert("RGB")
        img.thumbnail((512, 512))
        buf = io.BytesIO()
        img.save(buf, format="JPEG", quality=75)
        return "data:image/jpeg;base64," + base64.b64encode(buf.getvalue()).decode("ascii")
    except Exception:
        return ""


def _build_wangp_settings(req):
    model_id = str(req.get("model_id") or "").strip()
    if not model_id:
        raise ValueError("model_id is required")
    settings = {}
    try:
        defs = SESSION.get_default_settings(model_id)
        if isinstance(defs, dict):
            settings.update(defs)
    except Exception:
        pass

    settings["model_type"] = model_id
    settings["prompt"] = str(req.get("prompt") or "").strip()
    neg = str(req.get("negative_prompt") or "").strip()
    if neg:
        settings["negative_prompt"] = neg
    res = str(req.get("resolution") or "").strip()
    if res:
        settings["resolution"] = res
    steps = int(req.get("steps") or 0)
    if steps > 0:
        settings["num_inference_steps"] = steps
    seed = req.get("seed")
    if seed is not None and int(seed) > 0:
        settings["seed"] = int(seed)
    gs = req.get("guidance_scale")
    if gs is not None:
        try:
            settings["guidance_scale"] = float(gs)
        except Exception:
            pass
    vlen = int(req.get("video_length") or 0)
    dur = req.get("duration_sec")
    fps = int(req.get("fps") or 0)
    if fps > 0:
        settings["force_fps"] = fps
    if vlen > 0:
        settings["video_length"] = vlen
    elif dur is not None and float(dur) > 0:
        dur_f = float(dur)
        def_vlen = int(settings.get("video_length") or 0)
        def_dur = float(settings.get("duration_seconds") or 0)
        if def_vlen > 0 and def_dur == 0 and str(req.get("mode") or "").lower() in ("t2v", "i2v", "video"):
            # Convert seconds to WanGP 8k+1 frame count (e.g. 24fps * 3s -> 73 frames).
            eff_fps = fps if fps > 0 else 24
            raw_frames = max(17, int(round(dur_f * eff_fps)))
            aligned = ((raw_frames - 1) // 8) * 8 + 1
            settings["video_length"] = max(17, aligned)
        else:
            settings["duration_seconds"] = dur_f

    mode = str(req.get("mode") or "t2i").strip().lower()
    input_path = str(req.get("input_path") or "").strip()
    if input_path and os.path.isfile(input_path):
        img_meta = {}
        try:
            schema = SESSION.get_model_schema(model_id)
            if isinstance(schema, dict):
                md = schema.get("metadata") if isinstance(schema.get("metadata"), dict) else schema
                mi = md.get("media_inputs") if isinstance(md.get("media_inputs"), dict) else {}
                if isinstance(mi.get("image"), dict):
                    img_meta = mi["image"]
        except Exception:
            img_meta = {}
        supports_start = bool(img_meta.get("start"))
        supports_ref = bool(img_meta.get("reference") or img_meta.get("single_reference") or img_meta.get("multiple_references"))
        if mode in ("i2v", "video") and supports_start:
            settings["image_start"] = input_path
            settings["image_prompt_type"] = "S"
        elif supports_ref:
            settings["image_refs"] = [input_path]
            settings["image_start"] = None
            settings["image_prompt_type"] = ""
        elif supports_start:
            settings["image_start"] = input_path
            settings["image_prompt_type"] = "S"
        else:
            settings["image_refs"] = [input_path]

    extra = req.get("extra_settings")
    if isinstance(extra, dict):
        for k, v in extra.items():
            if k and not str(k).startswith("_"):
                settings[k] = v
    return settings


def _execute_single_job(rec, settings):
    if rec.canceled:
        rec.push({"type": "canceled"})
        return
    try:
        rec.push({"type": "progress", "phase": "loading_model", "status": "正在加载/初始化云端模型...", "progress": 5})
        api_job = SESSION.submit_task(settings)
        rec.api_job = api_job
        if rec.canceled:
            try:
                api_job.cancel()
            except Exception:
                pass
            try:
                api_job.result()
            except Exception:
                pass
            rec.push({"type": "canceled"})
            return

        last_preview_ts = 0.0
        for ev in api_job.events.iter(timeout=0.25):
            if rec.canceled:
                try:
                    api_job.cancel()
                except Exception:
                    pass
                break
            kind = getattr(ev, "kind", "")
            data = getattr(ev, "data", None)
            if kind == "progress" and data is not None:
                rec.push({
                    "type": "progress",
                    "phase": str(getattr(data, "phase", "") or "inference"),
                    "status": str(getattr(data, "status", "") or ""),
                    "progress": int(getattr(data, "progress", 0) or 0),
                    "current_step": int(getattr(data, "current_step", 0) or 0),
                    "total_steps": int(getattr(data, "total_steps", 0) or 0),
                })
            elif kind == "preview" and data is not None:
                now = time.time()
                if now - last_preview_ts >= 0.45:
                    last_preview_ts = now
                    img_url = _encode_preview_jpeg(getattr(data, "image", None))
                    if img_url:
                        rec.push({
                            "type": "preview",
                            "phase": str(getattr(data, "phase", "") or "inference"),
                            "status": str(getattr(data, "status", "") or ""),
                            "progress": int(getattr(data, "progress", 0) or 0),
                            "current_step": int(getattr(data, "current_step", 0) or 0),
                            "total_steps": int(getattr(data, "total_steps", 0) or 0),
                            "preview_data_url": img_url,
                        })
            elif kind == "stream" and data is not None:
                txt = str(getattr(data, "text", "") or "").strip()
                if txt:
                    rec.push({"type": "log", "text": txt[:400]})
            elif kind == "error" and data is not None:
                msg = str(getattr(data, "message", "") or "generation error")
                rec.push({"type": "log", "text": "[error] " + msg})

        result = api_job.result()
        if rec.canceled:
            rec.push({"type": "canceled"})
            return

        gen_files = list(getattr(result, "generated_files", None) or [])
        if getattr(result, "success", False) and gen_files:
            file_refs = []
            for p in gen_files:
                abs_p = os.path.abspath(str(p))
                if not os.path.isfile(abs_p):
                    continue
                fid = "f_" + uuid.uuid4().hex[:12]
                fname = os.path.basename(abs_p)
                mt = mimetypes.guess_type(fname)[0] or "application/octet-stream"
                with FILES_LOCK:
                    FILES[fid] = abs_p
                file_refs.append({
                    "file_id": fid,
                    "filename": fname,
                    "mime_type": mt,
                    "kind": _guess_kind(fname, mt),
                })
            if file_refs:
                rec.push({"type": "done", "files": file_refs})
                return

        errs = getattr(result, "errors", None) or []
        err_msg = "; ".join(str(getattr(e, "message", e)) for e in errs if e) or "generation produced no output files"
        rec.push({"type": "error", "error": err_msg})
    except Exception as exc:
        rec.push({"type": "error", "error": str(exc) + "\n" + traceback.format_exc()[-400:]})


def _gpu_worker_loop():
    while True:
        rec, settings = EXEC_QUEUE.get()
        try:
            _execute_single_job(rec, settings)
        finally:
            EXEC_QUEUE.task_done()


threading.Thread(target=_gpu_worker_loop, daemon=True).start()


def _parse_multipart_file(content_type, raw_body):
    header = f"Content-Type: {content_type}\r\nMIME-Version: 1.0\r\n\r\n".encode("utf-8")
    msg = email.parser.BytesParser(policy=email.policy.default).parsebytes(header + raw_body)
    if not msg.is_multipart():
        raise ValueError("expected multipart/form-data")
    for part in msg.iter_parts():
        fname = part.get_filename()
        if fname:
            return fname, part.get_payload(decode=True) or b""
    raise ValueError("no file part found in multipart upload")


class _Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        return

    def _send_json(self, code, obj):
        raw = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        p = parsed.path.rstrip("/") or "/"

        if p == "/":
            html = (
                "<!doctype html><html><body style='font-family:system-ui;padding:32px;max-width:640px;margin:auto'>"
                "<h2>ColabHive Headless Media Worker (media.v1)</h2>"
                "<p>Engine active & ready. Powered by <b>WanGP (DeepBeepMeep)</b>.</p>"
                "<p>Open ColabHive Native Media Studio at <code>/studio/media</code> for instant 0ms UI.</p>"
                "</body></html>"
            ).encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(html)))
            self.end_headers()
            self.wfile.write(html)
            return

        if p == "/colabhive/v1/health":
            self._send_json(200, {
                "ok": True,
                "engine": "wangp",
                "api_version": "media.v1",
                "powered_by": "WanGP (DeepBeepMeep)",
            })
            return

        if p == "/colabhive/v1/models":
            try:
                self._send_json(200, {"models": _serialize_models(), "powered_by": "WanGP (DeepBeepMeep)"})
            except Exception as exc:
                self._send_json(500, {"error": str(exc)})
            return

        if p.startswith("/colabhive/v1/models/") and p.endswith("/defaults"):
            mid = urllib.parse.unquote(p[len("/colabhive/v1/models/"):-len("/defaults")])
            try:
                defs = SESSION.get_default_settings(mid) or {}
                schema = None
                try:
                    schema = SESSION.get_model_schema(mid)
                except Exception:
                    schema = None
                self._send_json(200, {"model_id": mid, "defaults": defs, "schema": schema})
            except Exception as exc:
                self._send_json(400, {"error": str(exc)})
            return

        if p.startswith("/colabhive/v1/jobs/") and p.endswith("/events"):
            jid = urllib.parse.unquote(p[len("/colabhive/v1/jobs/"):-len("/events")])
            with JOBS_LOCK:
                rec = JOBS.get(jid)
            if rec is None:
                self._send_json(404, {"error": "job not found"})
                return
            self.close_connection = True
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream; charset=utf-8")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            snap, q, already_done = rec.subscribe()
            try:
                for evt in snap:
                    self.wfile.write(("data: " + json.dumps(evt, ensure_ascii=False) + "\n\n").encode("utf-8"))
                    self.wfile.flush()
                    if evt.get("type") in ("done", "error", "canceled"):
                        return
                if already_done:
                    return
                while True:
                    try:
                        evt = q.get(timeout=10.0)
                    except queue.Empty:
                        self.wfile.write(b": keepalive\n\n")
                        self.wfile.flush()
                        continue
                    self.wfile.write(("data: " + json.dumps(evt, ensure_ascii=False) + "\n\n").encode("utf-8"))
                    self.wfile.flush()
                    if evt.get("type") in ("done", "error", "canceled"):
                        return
            except Exception:
                return
            finally:
                rec.unsubscribe(q)
            return

        if p.startswith("/colabhive/v1/files/"):
            fid = urllib.parse.unquote(p[len("/colabhive/v1/files/"):])
            with FILES_LOCK:
                abs_p = FILES.get(fid)
            if not abs_p or not os.path.isfile(abs_p):
                self._send_json(404, {"error": "file not found"})
                return
            sz = os.path.getsize(abs_p)
            mt = mimetypes.guess_type(abs_p)[0] or "application/octet-stream"
            self.send_response(200)
            self.send_header("Content-Type", mt)
            self.send_header("Content-Length", str(sz))
            self.end_headers()
            with open(abs_p, "rb") as fh:
                while True:
                    chunk = fh.read(131072)
                    if not chunk:
                        break
                    self.wfile.write(chunk)
            return

        self._send_json(404, {"error": "not found"})

    def do_POST(self):
        parsed = urllib.parse.urlparse(self.path)
        p = parsed.path.rstrip("/") or "/"
        clen = int(self.headers.get("Content-Length") or "0")
        raw = self.rfile.read(min(clen, 80 * 1024 * 1024)) if clen > 0 else b""

        if p == "/colabhive/v1/uploads":
            try:
                ct = self.headers.get("Content-Type", "")
                fname, data = _parse_multipart_file(ct, raw)
                ext = os.path.splitext(os.path.basename(fname))[1].lower() or ".png"
                if ext not in (".png", ".jpg", ".jpeg", ".webp", ".mp4", ".webm", ".wav", ".mp3"):
                    ext = ".png"
                dest = os.path.join(UPLOAD_DIR, f"up_{uuid.uuid4().hex[:12]}{ext}")
                with open(dest, "wb") as fh:
                    fh.write(data)
                self._send_json(200, {"path": dest, "size_bytes": len(data)})
            except Exception as exc:
                self._send_json(400, {"error": str(exc)})
            return

        if p == "/colabhive/v1/jobs":
            try:
                req = json.loads(raw.decode("utf-8") if raw else "{}")
                settings = _build_wangp_settings(req)
                jid = "rjob_" + uuid.uuid4().hex[:12]
                rec = _JobRecord(jid)
                with JOBS_LOCK:
                    JOBS[jid] = rec
                q_ahead = EXEC_QUEUE.qsize()
                if q_ahead > 0:
                    rec.push({"type": "progress", "phase": "queued", "status": f"排队中（前方 {q_ahead} 个生成任务）...", "progress": 2})
                EXEC_QUEUE.put((rec, settings))
                self._send_json(200, {"job_id": jid})
            except Exception as exc:
                self._send_json(400, {"error": str(exc)})
            return

        if p.startswith("/colabhive/v1/jobs/") and p.endswith("/cancel"):
            jid = urllib.parse.unquote(p[len("/colabhive/v1/jobs/"):-len("/cancel")])
            with JOBS_LOCK:
                rec = JOBS.get(jid)
            if rec is None:
                self._send_json(404, {"error": "job not found"})
                return
            rec.canceled = True
            if rec.api_job is not None:
                try:
                    rec.api_job.cancel()
                except Exception:
                    pass
            self._send_json(200, {"canceled": True, "job_id": jid})
            return

        self._send_json(404, {"error": "not found"})


if __name__ == "__main__":
    srv = ThreadingHTTPServer(("127.0.0.1", PORT), _Handler)
    print(f"[colabhive-media-worker] Listening on http://127.0.0.1:{PORT}/colabhive/v1", flush=True)
    srv.serve_forever()
`
