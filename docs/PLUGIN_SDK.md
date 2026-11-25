# ColabHive AI App Store Plugin SDK (1-File JSON Guide)

> **5 分钟发布一个免编译的 ColabHive 云端 GPU 应用插件**  
> **Publish a zero-code, 1-file JSON GPU app plugin for ColabHive in 5 minutes**

ColabHive's AI App Store ships with **3 100%-real built-in core apps**—**`llm-chat` (私人免费 AI 大模型聊天)**, **`comfyui` (ComfyUI 图像与视频工坊)**, and **`jupyterlab` (JupyterLab 云端实验室)**—and supports any community or third-party AI app via a single declarative JSON manifest (`colabhive-app.json`) loaded dynamically at runtime from:

1. `<dataDir>/plugins/*.json` *(workspace / server data directory — highest priority)*
2. `~/.colabhive/plugins/*.json` *(user-level global plugins directory)*

No Go toolchain or binary rebuild is needed: drop a single `.json` manifest (up to **256 KiB**) into either directory—or install it live via the admin REST API—and your app immediately appears in the ColabHive App Store with explicit GPU/account/model launch controls, private `127.0.0.1` port bridging (with full WebSocket & app cookie support), idle auto-shutdown, and automatic cross-account checkpoint failover.

---

## 1. 5-Minute Quickstart / 5 分钟快速上手

### Step 1: Create your 1-file manifest / 创建单文件插件清单

Save the following as `~/.colabhive/plugins/kokoro-tts.json` (or `<dataDir>/plugins/kokoro-tts.json`):

```json
{
  "schema_version": "1",
  "id": "kokoro-tts",
  "name": "Kokoro 82M Ultra-Fast TTS Studio",
  "name_zh": "Kokoro 超快开源语音合成工作台",
  "tagline": "Natural multilingual speech synthesis on Free Colab T4 GPUs",
  "tagline_zh": "免费 Colab T4 显卡秒级生成自然中英文口播配音",
  "description": "Lightweight 82M-parameter neural TTS with Gradio Web UI and automatic output checkpointing.",
  "description_zh": "轻量级 82M 参数神经语音合成，打开即用 Gradio 界面，生成音频自动同步至本机快照目录。",
  "category": "audio",
  "icon": "🔊",
  "author": "your-github-handle",
  "version": "1.0.0",
  "homepage_url": "https://github.com/hexgrad/kokoro",
  "repo_url": "https://github.com/hexgrad/kokoro",
  "start_command": "python3 app.py",
  "remote_port": 7864,
  "local_port": 7864,
  "min_vram_gb": 4,
  "recommended_gpu": "T4",
  "free_tier_compatible": true,
  "supports_port_bridge": true,
  "est_boot_sec": 30,
  "checkpoint_dir": "/content/checkpoints/kokoro-tts",
  "health_path": "/",
  "highlights": [
    "Runs smoothly on $0 Free Colab T4 GPUs",
    "Auto-saves generated WAV/MP3 files across account relays"
  ],
  "highlights_zh": [
    "免费 Colab T4 即可流畅运行，零成本配音",
    "生成音频自动纳入断点快照，换号重连不丢文件"
  ],
  "plugin_integration_tip_en": "Connect local audio scripts directly to http://127.0.0.1:7864.",
  "plugin_integration_tip_zh": "本地剪辑脚本可直接调用 http://127.0.0.1:7864 私有接口！"
}
```

### Step 2: Install, Launch & Manage via REST API / 通过管理接口安装、启动与停止

```bash
# Install / update plugin atomically into <dataDir>/plugins/<id>.json (Admin-only, max 256 KiB)
curl -X POST http://127.0.0.1:7860/api/v1/apps/plugins \
  -H "Authorization: Bearer $COLABHIVE_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d @kokoro-tts.json

# List all loaded community plugins
curl http://127.0.0.1:7860/api/v1/apps/plugins \
  -H "Authorization: Bearer $COLABHIVE_ADMIN_TOKEN"

# Verify your plugin card appears alongside the 3 built-in core apps (llm-chat, comfyui, jupyterlab)
curl http://127.0.0.1:7860/api/v1/apps/templates \
  -H "Authorization: Bearer $COLABHIVE_ADMIN_TOKEN"

# Launch with explicit GPU, optional pinned account_id, and model selection
curl -X POST http://127.0.0.1:7860/api/v1/apps/launch \
  -H "Authorization: Bearer $COLABHIVE_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"template_id":"kokoro-tts","preferred_gpu":"T4","account_id":"","idle_auto_stop_min":45}'

# Safe stop (saves final checkpoint first; aborts with HTTP 409 if backup fails unless ?force=true)
curl -X POST "http://127.0.0.1:7860/api/v1/apps/instances/<instance_id>/stop" \
  -H "Authorization: Bearer $COLABHIVE_ADMIN_TOKEN"

# Force stop (skip blocking on pre-stop checkpoint backup failure)
curl -X POST "http://127.0.0.1:7860/api/v1/apps/instances/<instance_id>/stop?force=true" \
  -H "Authorization: Bearer $COLABHIVE_ADMIN_TOKEN"

# Remove plugin
curl -X DELETE http://127.0.0.1:7860/api/v1/apps/plugins/kokoro-tts \
  -H "Authorization: Bearer $COLABHIVE_ADMIN_TOKEN"
```

*(In keyless local desktop mode without `AdminToken`, omit the `Authorization` header.)*

---

## 2. Ready-to-Use Community Plugin Examples / 常用社区插件模板示例

Drop any of these declarative manifests into `<dataDir>/plugins/` or install them directly via `POST /api/v1/apps/plugins`:

### Example A: Fooocus (SDXL Paint WebUI)

```json
{
  "schema_version": "1",
  "id": "fooocus",
  "name": "Fooocus SDXL Paint Studio",
  "name_zh": "Fooocus 极简 AI 绘画工作台",
  "tagline": "Zero-prompt-engineering SDXL image generation on Free T4 GPUs",
  "tagline_zh": "免调参极简 SDXL 绘画界面，免费 T4 显卡直接出图",
  "category": "image",
  "icon": "🖌️",
  "author": "community",
  "version": "1.0.0",
  "homepage_url": "https://github.com/lllyasviel/Fooocus",
  "repo_url": "https://github.com/lllyasviel/Fooocus",
  "start_command": "python3 entry_with_update.py --listen 127.0.0.1 --port 7865",
  "remote_port": 7865,
  "local_port": 7865,
  "min_vram_gb": 12,
  "recommended_gpu": "T4",
  "free_tier_compatible": true,
  "supports_port_bridge": true,
  "est_boot_sec": 60,
  "checkpoint_dir": "/content/custom_app/outputs",
  "health_path": "/"
}
```

### Example B: Open WebUI (Private Chat & Local RAG)

```json
{
  "schema_version": "1",
  "id": "open-webui",
  "name": "Open WebUI Workspace",
  "name_zh": "Open WebUI 私人知识库聊天",
  "tagline": "Self-hosted PDF/Markdown RAG chat workspace backed by Ollama",
  "tagline_zh": "支持拖拽文档检索与多模型对比的开源知识库聊天界面",
  "category": "workspace",
  "icon": "🗣️",
  "author": "community",
  "version": "1.0.0",
  "homepage_url": "https://github.com/open-webui/open-webui",
  "start_command": "uv run --with open-webui open-webui serve --host 127.0.0.1 --port 8080",
  "remote_port": 8080,
  "local_port": 3080,
  "min_vram_gb": 8,
  "recommended_gpu": "T4",
  "free_tier_compatible": true,
  "supports_port_bridge": true,
  "est_boot_sec": 45,
  "checkpoint_dir": "/content/checkpoints/open-webui",
  "health_path": "/"
}
```

### Example C: Wan 2.2 Video / Custom GitHub Gradio App

```json
{
  "schema_version": "1",
  "id": "wan-video-community",
  "name": "Wan 2.2 Video Studio (Community)",
  "name_zh": "Wan 2.2 开源视频工坊（社区插件）",
  "tagline": "Run Wan 2.2 Gradio video generation on 24GB L4 or 96GB G4 GPUs",
  "tagline_zh": "在 L4 / A100 / G4 大显存显卡上运行开源 Wan 2.2 视频生成界面",
  "category": "video",
  "icon": "🎬",
  "author": "community",
  "version": "1.0.0",
  "homepage_url": "https://github.com/Wan-Video/Wan2.2",
  "repo_url": "https://github.com/Wan-Video/Wan2.2",
  "start_command": "python3 app.py",
  "remote_port": 7866,
  "local_port": 7866,
  "min_vram_gb": 24,
  "recommended_gpu": "G4",
  "free_tier_compatible": false,
  "supports_port_bridge": true,
  "est_boot_sec": 90,
  "checkpoint_dir": "/content/checkpoints/wan-video-community",
  "health_path": "/",
  "supports_custom_model_url": true
}
```

---

## 3. Manifest Schema Reference / 清单字段规范 (`AppPluginManifest`)

| Field | Required | Type | Validation / Default | Description |
| :--- | :---: | :--- | :--- | :--- |
| `schema_version` | No | `string` | Default `"1"` | Manifest schema version |
| `id` | **Yes** | `string` | `^[a-z0-9][a-z0-9-]{1,48}$`; cannot collide with built-in core IDs (`llm-chat`, `comfyui`, `jupyterlab`) | Unique plugin slug and filename `<id>.json` |
| `name` | **Yes** | `string` | `1..140` chars (falls back to `name_zh` if omitted) | English display name |
| `name_zh` | No | `string` | `<= 140` chars (defaults to `name`) | Simplified Chinese display name |
| `category` | **Yes** | `string` | One of: `video`, `image`, `audio`, `workspace`, `custom` | App Store filter category |
| `remote_port` | **Yes** | `int` | `1024..65535` | Loopback port your app binds on `127.0.0.1` inside the GPU VM |
| `local_port` | No | `int` | `1024..65535` (defaults to `remote_port`; falls back to ephemeral port if busy) | Preferred local `127.0.0.1:<port>` bridge port |
| `repo_url` | No | `string` | Strict `https://github.com/<owner>/<repo>` (zero shell metacharacters, no credentials/query/fragment) | Public GitHub repository cloned automatically (`--depth 1`) |
| `start_command` | No | `string` | Max `512` chars; allowlisted launcher (`python`, `python3`, `uv`, `streamlit`, `gradio`, `uvicorn`, `gunicorn`, `fastapi`); zero shell metacharacters (`;&\|$`()<>\\`) | Explicit process launch command (`shell=False`) |
| `recommended_gpu` | No | `string` | `T4`, `L4`, `A100`, `H100`, `G4`, `V5E1`, `V6E1`, `CPU` (default `"T4"`) | Preferred accelerator (overridable by user's `preferred_gpu`) |
| `min_vram_gb` | No | `int` | `0..1024` (default `0`) | Minimum recommended VRAM in GB |
| `free_tier_compatible` | No | `bool` | Default `true` | Enables `free-first` scheduling on Free Colab T4 accounts |
| `supports_port_bridge` | No | `bool` | Default `true` | Binds private `127.0.0.1:<local_port>` bridge in local desktop mode |
| `checkpoint_dir` | No | `string` | Must stay under `/content/` without `..` (default `/content/checkpoints/<id>`) | Remote directory synced periodically by ColabHive's checkpoint engine |
| `health_path` | No | `string` | Starts with `/`, no whitespace or `..` (default `"/"`) | HTTP GET path probed on `127.0.0.1:<remote_port>` before marking ready |
| `model_options` | No | `AppModelOption[]` | Up to `20` items; any `download_url` must be `https://huggingface.co/...` or `https://civitai.com/...` with zero shell metacharacters | Optional explicit weight presets shown in the launch drawer |
| `supports_custom_model_url` | No | `bool` | Default `false` | Allows users to paste a custom Hugging Face / Civitai URL at launch |
| `tagline` / `tagline_zh` | No | `string` | Defaults to `name` | Short 1-line subtitle |
| `description` / `description_zh` | No | `string` | `<= 2048` chars | Detailed App Store card description |
| `icon` | No | `string` | Default `"🧩"` | Emoji icon shown on the App Store card |
| `highlights` / `highlights_zh` | No | `string[]` | Up to `12` items | Feature bullet points |
| `plugin_integration_tip_en` / `_zh` | No | `string` | Auto-generated if omitted | Local tool integration hint |
| `author` / `version` / `homepage_url` | No | `string` | `homepage_url` must be `http://` or `https://` | Publisher metadata |

> **Manifest Size Limit**: Both the HTTP upload body (`POST /api/v1/apps/plugins`), atomic disk write, and runtime loader enforce an aligned **256 KiB (`262,144` bytes)** cap so a successfully installed plugin can never be silently dropped on reload.

---

## 4. Runtime Environment Contract / 云端运行环境约定

When ColabHive launches your plugin on a GPU VM, the bootstrap runner:

1. Clones `repo_url` with `git clone --depth 1` (checked loudly with `check=True`) into `/content/custom_app`.
2. Installs `/content/custom_app/requirements.txt` via `pip install -q -r` (`check=True`) if present.
3. Injects standard environment variables into your app process (`shell=False`):
   - `COLABHIVE_REMOTE_PORT` & `PORT`: assigned `remote_port`
   - `GRADIO_SERVER_NAME="127.0.0.1"`, `GRADIO_SERVER_PORT="<remote_port>"`
   - `STREAMLIT_SERVER_ADDRESS="127.0.0.1"`, `STREAMLIT_SERVER_PORT="<remote_port>"`
   - `COLABHIVE_CHECKPOINT_DIR`: your validated `checkpoint_dir`
   - `COLABHIVE_MODEL_ID`, `COLABHIVE_MODEL_DOWNLOAD_URL`, `COLABHIVE_MODEL_SUBDIR`, `COLABHIVE_MODEL_FILENAME` *(when a model preset or custom model URL is selected)*
4. Probes `http://127.0.0.1:<remote_port><health_path>` (TCP + HTTP GET `< 500`) until ready or timeout, failing fast with log tail if your process exits prematurely.

**Best Practice / 最佳实践**:
- Always bind your server to **`127.0.0.1`** (never `0.0.0.0` and never start public `ngrok`/`gradio share=True` tunnels).
- Save generated assets, models, or user state inside `os.environ["COLABHIVE_CHECKPOINT_DIR"]` so ColabHive's periodic watchdog automatically snapshots and restores them seamlessly across account failovers.

---

## 5. Security Guarantees / 安全边界

- **Zero Shell Injection**: `repo_url` is strictly canonicalized to `https://github.com/<owner>/<repo>.git`, `model_download_url` is strictly restricted to `https://huggingface.co/...` or `https://civitai.com/...` with zero shell metacharacters/credentials/whitespace, and `start_command` is parsed with `shlex.split` (`shell=False`) against an explicit launcher allowlist.
- **Parent-Confined Root Persistence (`os.OpenRoot`)**: The `plugins` directory itself is opened through its parent root (`os.OpenRoot(filepath.Dir(dir))` + `Lstat` + `OpenRoot(base)`), rejecting symlinked `plugins` directories as well as symlink/non-regular manifest files on read, install, and delete.
- **Admin-Only Mutation**: `POST /api/v1/apps/plugins` and `DELETE /api/v1/apps/plugins/{id}` require the control-plane admin token in protected/cloud deployments; scoped `ch_sk_...` agent keys receive `403 Forbidden`.
- **DNS-Rebinding, Cross-Site & Origin Isolation**: Private `127.0.0.1:<local_port>` bridges strictly validate exact loopback `Host` (`127.0.0.1:<port>` / `localhost:<port>`), exact same-origin `http://127.0.0.1:<port>` / `http://localhost:<port>` `Origin`, and block `Sec-Fetch-Site: cross-site` with `403 Forbidden`. Control-plane credentials (`X-ColabHive-Key`, `x-api-key`, `ch_adm_`/`ch_sk_` bearer tokens, and auth query params) are stripped while app `Cookie`, `Set-Cookie`, and WebSocket (`Upgrade`, `Connection: Upgrade`, `Sec-WebSocket-*`) headers are preserved. Live cloud deployments without an isolated Studio origin fail closed (`422 Unprocessable Entity`) BEFORE allocating any billing GPU VM.

---

## 6. Pre-Publish Checklist / 发布前检查清单

- [ ] `id` is lowercase alphanumeric + hyphens (`^[a-z0-9][a-z0-9-]{1,48}$`) and does not collide with `llm-chat`, `comfyui`, or `jupyterlab`
- [ ] Serialized JSON manifest fits strictly under **256 KiB**
- [ ] App binds `127.0.0.1:$PORT` (or honors `GRADIO_SERVER_PORT` / `STREAMLIT_SERVER_PORT`)
- [ ] `GET http://127.0.0.1:<remote_port><health_path>` returns HTTP `200..499` within `est_boot_sec`
- [ ] Outputs persist under `COLABHIVE_CHECKPOINT_DIR` (`/content/checkpoints/<id>`)
- [ ] Tested install + explicit GPU/model launch + `PATCH /api/v1/apps/instances/{id}` + safe stop cleanly
