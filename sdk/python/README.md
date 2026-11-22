# `colabhive` — Python SDK & Zero-Friction Contributor CLI

Pure-stdlib Python client (with optional `httpx` connection pooling) and 1-command account donation helper (`colabhive-sdk donate`) for **[ColabHive](../../README.md)**.

## Installation

```bash
# Zero external dependencies (pure Python 3.10+ stdlib)
pip install ./sdk/python

# With optional httpx connection pooling
pip install "./sdk/python[httpx]"
```

## Python SDK Quickstart

```python
from colabhive import ColabHiveClient

client = ColabHiveClient(
    base_url="http://localhost:7860",
    api_key="ch_sk_live_your_key",
)

# 1. Inspect pool Compute Units & Cloud GPU Equivalent Hours
quota = client.quota()
print(quota["total_available_cu"], quota["gpu_equivalent_hours"])

# 2. Allocate a persistent Blackwell G4 (96 GB) session & execute stateful code
with client.session(gpu="G4") as sess:
    sess.upload("./eval_config.json", "/content/eval_config.json")
    out = sess.exec("import torch; print(torch.cuda.get_device_name(0))")
    print(out.stdout)
    sess.download("/content/eval_config.json", "./downloaded_config.json")

# 3. Launch a 4-way sharded sweep across 4 pooled Colab accounts in parallel
exp = client.run_sharded("eval_qwen27b.py", shards=4, gpu="G4")
print(exp.experiment_id, exp.status)
```

## 1-Command Contributor CLI (`colabhive-sdk donate`)

Lab-mates or friends can contribute idle Colab Pro/Pro+ CUs (with hard personal reserve floors and zero Google Drive access) or migrate an existing local `colab` CLI `keyringrc.cfg` in one command:

```bash
# Interactive remote OAuth copy-paste flow with 50 CU personal reserve floor
colabhive-sdk donate https://hive.lab.example.com/contribute/ch_inv_8x92kP --reserve-cu 50

# Transfer an existing local `google-colab-cli` keyring (`keyringrc.cfg` / `keyring_pass.cfg`)
colabhive-sdk donate https://hive.lab.example.com/contribute/ch_inv_8x92kP \
  --keyring-path ~/.config/python_keyring/keyringrc.cfg \
  --reserve-cu 80 \
  --allow-gpu T4,L4,G4,A100
```
