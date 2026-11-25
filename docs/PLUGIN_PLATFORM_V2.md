# ColabHive Plugin Platform v2 — Architecture & Developer Spec

> **Design Goal**: Decouple **Cloud GPU Engines** from **Local-First Studio UIs** behind a tiny, stable **Capability Protocol + Host SDK**, so:
> 1. **0ms Local Speed**: Studio UIs run locally instead of proxying 20+ MB remote Gradio pages across the WAN.
> 2. **100% Replaceable UIs**: If a community author builds a better Image/Video/Audio Studio UI (in React, Vue, Svelte, or plain HTML), users can install it and swap it in as their default UI with one click—**ColabHive's built-in Media Studio uses the exact same public `/sdk/v1/*` APIs with zero private backdoors**.
> 3. **Cross-Plugin Reuse**: Assets, job queues, GPU billing/keepalive, Comm Tunnel v3 transport, and cross-plugin capabilities (such as invoking `llm.openai.v1` from a video studio for prompt enhancement, or handing a generated image asset directly to an I2V video engine) are provided once by the ColabHive Host.

---

## 1. Three-Layer Orthogonal Architecture

```text
┌──────────────────────────────────────────────────────────────────────────┐
│  Layer 1 · Local UI Plugins (0ms Instant Load · Isolated Loopback Origin)│
│  [Builtin Media Studio]  [Community UI Bundle]  [Remote Gradio Fallback] │
│                         │  consumes: ["media.image.v1", "media.video.v1"]│
├─────────────────────────▼────────────────────────────────────────────────┤
│  Layer 2 · ColabHive Host SDK (/sdk/v1/* · Least-Privilege Scoped Token) │
│  • Asset Vault    : Local persistent blobs + metadata + lineage graph    │
│  • Capability Bus : Discover & invoke capabilities (media.v1, llm.v1)    │
│  • Job Runner     : Queues, SSE live previews/progress, cancel, recover  │
│  • UI Sandbox     : Serves ui/dist/ on isolated 127.a.b.c:<port> origin  │
│  • Compute/Tunnel : Multi-account GPU pool + Comm Tunnel v3 (zlib+FC)    │
├─────────────────────────▲────────────────────────────────────────────────┤
│  Layer 3 · Cloud Engine Plugins (Headless Workers on Colab A100/L4/T4)   │
│  [WanGP Headless (shared/api.py)]  [ComfyUI Headless]  [Ollama /v1]      │
│                         │  provides: ["media.image.v1", "media.video.v1"]│
└──────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Plugin Manifest v2 (100% Backward Compatible with v1)

Existing single-file v1 manifests (`~/.colabhive/plugins/<id>.json`) continue to work unchanged as `kind: "engine"` with `ui.mode: "remote_proxy"`.

v2 plugins are distributed as a directory (`~/.colabhive/plugins/<id>/` or `<dataDir>/plugins/<id>/`):

```text
~/.colabhive/plugins/my-cinema-studio/
├── manifest.json          # Required, <= 256 KiB
├── ui/
│   └── dist/
│       └── index.html     # Optional local static bundle (React/Vue/Svelte/HTML)
└── worker/                # Optional headless cloud worker script
```

### Example: Third-Party Local UI Plugin (`kind: "ui"`)

```json
{
  "schema_version": "2",
  "id": "cinema-studio-ui",
  "kind": "ui",
  "name": "Cinema Studio UI",
  "name_zh": "电影分镜创作台",
  "version": "1.0.0",
  "author": "community-author",
  "category": "video",
  "consumes": ["media.image.v1", "media.video.v1"],
  "optional_capabilities": ["llm.openai.v1"],
  "permissions": [
    "assets:read",
    "assets:write",
    "media:invoke",
    "cap:llm.openai.v1"
  ],
  "ui": {
    "mode": "local_bundle",
    "bundle_dir": "ui/dist",
    "entry": "index.html"
  }
}
```

### Example: Cloud Engine Plugin (`kind: "engine"`)

```json
{
  "schema_version": "2",
  "id": "custom-flux-engine",
  "kind": "engine",
  "name": "Custom Flux Worker",
  "category": "image",
  "remote_port": 7861,
  "recommended_gpu": "L4",
  "provides": ["media.image.v1"],
  "media_api_base": "/colabhive/v1",
  "ui": {
    "mode": "remote_proxy"
  }
}
```

### Supported Capability Identifiers

| Capability ID | Description | Reference Providers |
| :--- | :--- | :--- |
| `media.image.v1` | Text-to-Image & Image-to-Image generation | `wangp` (Headless `shared/api.py`), future `comfyui` |
| `media.video.v1` | Text-to-Video & Image-to-Video generation | `wangp` (Headless `shared/api.py`), future `comfyui` |
| `media.audio.v1` | Music, SFX, Speech synthesis | `wangp`, community TTS/Audio engines |
| `llm.openai.v1` | OpenAI `/v1` chat completions & prompt enhancement | ColabHive LLM Gateway (`llmgw`) |

---

## 3. Standard `media.v1` Engine Wire Contract

Any cloud GPU engine declaring `media.image.v1`, `media.video.v1`, or `media.audio.v1` implements this minimal JSON + SSE contract on its remote port (tunneled privately by ColabHive over Comm Tunnel v3):

| Endpoint | Purpose |
| :--- | :--- |
| `GET /colabhive/v1/health` | Engine readiness, attribution (`powered_by`), active loaded model, GPU info |
| `GET /colabhive/v1/models` | Live model catalog (modalities, capabilities, cached availability, family, resolutions) |
| `GET /colabhive/v1/models/{model_id}/defaults` | Pristine default parameters & schema for a model |
| `POST /colabhive/v1/uploads` | Stage a local input asset (image/video/audio) on the remote VM for I2I/I2V |
| `POST /colabhive/v1/jobs` | Submit generation job (`model_id`, `mode`, `prompt`, `resolution`, `steps`, `seed`, `input_path`, ...) |
| `GET /colabhive/v1/jobs/{job_id}/events` | SSE stream emitting `progress`, `preview` (JPEG base64), `log`, `done`, `error` |
| `POST /colabhive/v1/jobs/{job_id}/cancel` | Cancel an active generation cleanly |
| `GET /colabhive/v1/files/{file_id}` | Stream generated PNG/MP4/WAV back to ColabHive's Local Asset Vault |

---

## 4. ColabHive Host SDK (`/sdk/v1/*`) & Sandbox Security

### Security Isolation
- Third-party `local_bundle` UI plugins are served on a **per-plugin isolated loopback origin** (`http://127.a.b.c:<port>`), strictly separate from the ColabHive control plane (`127.0.0.1:7860`).
- ColabHive serves `/__colabhive_sdk.js` on that isolated origin, injecting `window.ColabHiveSDK` pre-bound with a **random per-session, least-privilege token (`ch_plug_...`)** and CORS restricted strictly to that exact origin.
- Third-party UIs can **never** read Google OAuth tokens, admin tokens, `/api/v1/accounts`, or `/api/v1/keys`; they can only call `/sdk/v1/*` routes permitted by their declared `permissions`.

### Host SDK Endpoints

- **Discovery & UI Preferences**
  - `GET /sdk/v1/capabilities` — Live capability providers, active engines, installed UI plugins, and user-selected default UIs
  - `PUT /sdk/v1/ui-preferences/{capability}` — Switch user's preferred UI plugin for a capability (e.g. `media.v1`)
  - `POST /sdk/v1/ui-plugins/{id}/open` — Start (or reuse) an isolated loopback sandbox listener for a local UI plugin and return its URL
- **Unified Local Asset Vault (Cross-Plugin Reuse)**
  - `GET /sdk/v1/assets` — List/search persisted local assets (`image`, `video`, `audio`) even with 0 GPUs running
  - `POST /sdk/v1/assets` — Upload/register a media asset (`multipart/form-data` or base64 JSON)
  - `GET /sdk/v1/assets/{id}` / `GET /sdk/v1/assets/{id}/file` / `DELETE /sdk/v1/assets/{id}`
- **Media Orchestrator**
  - `GET /sdk/v1/media/engines` — Active running media engines + GPU/account/burn telemetry
  - `GET /sdk/v1/media/instances/{id}/models` — Engine model catalog (cached per instance)
  - `GET /sdk/v1/media/instances/{id}/models/{model_id}/defaults` — Model default settings
  - `POST /sdk/v1/media/jobs` — Submit generation (automatically stages `input_asset_id` remotely & saves outputs into Local Asset Vault)
  - `GET /sdk/v1/media/jobs` / `GET /sdk/v1/media/jobs/{job_id}/events` (SSE) / `POST /sdk/v1/media/jobs/{job_id}/cancel`
- **Cross-Plugin Capability Bus**
  - `POST /sdk/v1/capabilities/llm.openai.v1/enhance-prompt` — Enhance/translate image or video prompts using ColabHive's live LLM Gateway

---

## 5. Building a Custom UI Plugin in 10 Minutes

1. Create `~/.colabhive/plugins/my-studio/manifest.json` (see Section 2).
2. Build any static web app into `~/.colabhive/plugins/my-studio/ui/dist/index.html`.
3. Include `<script src="/__colabhive_sdk.js"></script>` in your `index.html`:

```js
const sdk = window.ColabHiveSDK;
const caps = await sdk.getCapabilities();
const engines = await sdk.listMediaEngines();
const assets = await sdk.listAssets({ kind: 'image' });

const job = await sdk.createMediaJob({
  instance_id: engines[0].instance_id,
  model_id: 'z_image_turbo',
  mode: 't2i',
  prompt: 'A cinematic astronaut rabbit in an orbital station',
  resolution: '1280x720',
  steps: 8,
});

sdk.subscribeJobEvents(job.id, (evt) => {
  console.log(evt.type, evt); // progress | preview | log | done | error
});
```

4. Click **Reload Plugins** in ColabHive's Media Studio or App Store, and select **My Studio** in the UI Plugin Switcher!

---

## 6. Third-Party Engine Attribution

The built-in `wangp` headless engine integrates **WanGP (`shared/api.py`)** by **DeepBeepMeep** in accordance with the WanGP API Terms and Conditions. Both the ColabHive UI and documentation clearly disclose WanGP engine attribution whenever a WanGP-backed session is used.
