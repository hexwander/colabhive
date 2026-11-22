<div align="center">

<img src="./docs/assets/logo.svg" width="82" height="82" alt="ColabHive 三晶蜂巢张量标志" />

# ColabHive

**把散落的 Google Colab 账号汇聚为一体的私人多卡 GPU 算力池、4 路并发分片实验引擎与无头 AI Agent MCP 路由中枢 —— 同时提供原生无边框系统托盘 / 菜单栏桌面端与安全加固版云端团队服务器。**

<p>
  <a href="https://hexwander.github.io/colabhive/"><b>🎮 免安装在线交互试玩 (Live Demo)</b></a>
  &nbsp;·&nbsp;
  <a href="https://github.com/hexwander/colabhive/releases/latest"><b>🖥 下载 Windows 托盘端 &amp; macOS 菜单栏版</b></a>
  &nbsp;·&nbsp;
  <a href="#方式-3--安全加固版团队云服务器docker--自动-https"><b>☁️ 云服务器部署指南</b></a>
  &nbsp;·&nbsp;
  <a href="./README.md"><b>English</b></a>
</p>

<p>
  <a href="https://github.com/hexwander/colabhive/releases/latest"><img src="https://img.shields.io/badge/release-v1.0.0-F59E0B?style=flat-square" alt="Release v1.0.0" /></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-10B981?style=flat-square" alt="Apache-2.0 License" /></a>
  <a href="./go.mod"><img src="https://img.shields.io/badge/Go-1.26_单文件静态编译_(CGO__ENABLED%3D0)-0284C7?style=flat-square" alt="Go 1.26 Static Binary" /></a>
  <a href="#原生系统托盘与菜单栏体验"><img src="https://img.shields.io/badge/桌面端-Windows_托盘_%2B_macOS_菜单栏-18181B?style=flat-square" alt="Desktop Support" /></a>
  <a href="#一键接入主流-ai-编程-agent"><img src="https://img.shields.io/badge/MCP-Streamable_HTTP_%2B_SSE_%2B_stdio-2563EB?style=flat-square" alt="Tri-Protocol MCP" /></a>
  <a href="./README.md"><img src="https://img.shields.io/badge/界面语言-简体中文_%7C_English-D97706?style=flat-square" alt="Bilingual UI" /></a>
</p>

<br />

<a href="https://hexwander.github.io/colabhive/">
  <img src="./docs/assets/hero-showcase.png" width="100%" alt="ColabHive 精密算力观测台与原生四栏系统托盘实机展示" />
</a>

<sub>
  汲取 <a href="https://github.com/Wei-Shaw/sub2api"><code>Wei-Shaw/sub2api</code></a>、<a href="https://github.com/yetone/magpie"><code>yetone/magpie</code></a> 与 <a href="https://github.com/farion1231/cc-switch"><code>farion1231/cc-switch</code></a> 开源交互精髓 · 100% 兼容 Google 官方 Colab 双后端 API、<code>google-colab-cli</code> 与 <code>colab-mcp</code>。
</sub>

</div>

---

## ⚡ 30 秒极速安装

按你的使用场景一键部署——支持个人电脑系统托盘常驻，或实验室 7×24 小时云端共享服务器：

### 方式 1 —— Windows 原生系统托盘桌面端 + CLI（一行命令）

自动校验 SHA-256 并安装无黑框原生无边框桌面端 `colabhive-desktop.exe`（含 Magpie 四栏系统托盘）、`colabhive.exe` 与 100% 兼容的 `colab.exe`：

```powershell
irm https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.ps1 | iex
```

> 习惯手动解压？可直接前往 [**GitHub Releases**](https://github.com/hexwander/colabhive/releases/latest) 下载 `colabhive_v1.0.0_windows_amd64.zip`，双击运行 `colabhive-desktop.exe`。

### 方式 2 —— macOS（`ColabHive.app` 菜单栏版 + Apple Silicon / Intel）与 Linux（一行命令）

自动识别 macOS / Linux（`arm64` / `amd64`），校验 `SHA256SUMS.txt` 并安装 macOS 原生菜单栏应用 `ColabHive.app` 以及 `colabhive`、`colab` 命令行工具：

```bash
curl -fsSL https://raw.githubusercontent.com/hexwander/colabhive/main/scripts/install.sh | sh
```

### 方式 3 —— 安全加固版团队云服务器（Docker + 自动 HTTPS）

一键拉起 `colabhive serve --mode cloud --trust-proxy` 与 Caddy 自动 Let's Encrypt HTTPS 伴生容器（内置 `/mcp*` `flush_interval -1` 零缓冲流式转发、`1800s` GPU 超时保护与强制 `COLABHIVE_ADMIN_TOKEN` + `AES-256-GCM` 保险箱）：

```bash
cp .env.cloud.example .env.cloud && chmod 600 .env.cloud
# 编辑 .env.cloud 填入公网域名 COLABHIVE_DOMAIN、COLABHIVE_PUBLIC_URL 与两个 256-bit 随机密钥
docker compose -f docker-compose.cloud.yml --env-file .env.cloud --profile caddy up -d
```

---

## 🐝 原生系统托盘与菜单栏体验

<table>
  <tr>
    <td width="58%" valign="middle">
      <h3>随叫随到的算力中枢 —— 零终端黑框打扰</h3>
      <p>
        致敬 <b>Magpie</b> 与 <b>cc-switch</b> 的原生工具质感，ColabHive 在 <b>Windows</b>（<code>colabhive-desktop.exe</code>，DWM 无边框圆角窗口 + 系统托盘）与 <b>macOS Apple Silicon / Intel</b>（<code>ColabHive.app</code>，原生红黄绿沉浸窗口 + 顶部菜单栏 <code>NSPopover</code>）上提供一致的极速四栏面板；关闭主窗口自动退入后台常驻，绝不中断正在运行的云端 GPU 会话：
      </p>
      <ul>
        <li><b><code>额度 · Quota</code></b>：将全池可用 CU 实时折算为 <b>G4 (96 GB)</b>、<b>H100</b>、<b>A100</b> 可跑小时数，搭配单账号双圆环余量与并发仪表。</li>
        <li><b><code>Agent · 8 大工具</code></b>：一键外科手术式将本机免密 MCP 写入 <b>Claude Code</b>、<b>Codex</b>、<b>Antigravity</b>、<b>Cursor</b>、<b>Gemini CLI</b>、<b>Windsurf</b>、<b>OpenCode</b> 与 <b>Claude Desktop</b>（首次写入自动备份）。</li>
        <li><b><code>实例 · Live GPUs</code></b>：实时监控显存与 GPU 负载条，一键<b>在官方 Colab 网页打开同一内核</b>，并默认保护网页端自建 Notebook 免遭后台 Agent 抢占。</li>
        <li><b><code>路由 · Live Traces</code></b>：<code>[调用方] ── (🐝) ── [● 命中账号 · GPU]</code> 胶囊流向图、实时 MCP 调用瀑布流与<b>开机自启</b>开关。</li>
      </ul>
      <p>
        👉 <a href="https://hexwander.github.io/colabhive/"><b>免安装在浏览器里直接点击体验实机四栏托盘 →</b></a>
      </p>
    </td>
    <td width="42%" align="center" valign="middle">
      <img src="./docs/assets/tray-demo.gif" width="380" alt="ColabHive Magpie 风格四栏系统托盘实机交互动图" />
    </td>
  </tr>
</table>

---

## 🎯 四大核心工程能力一览

<p align="center">
  <img src="./docs/assets/bento-features.png" width="100%" alt="ColabHive 核心能力四宫格总览" />
</p>

### 为什么大模型研究者与 AI 编程重度用户都在切到 ColabHive？

跑评测、微调或驱动自主 AI 编程助手时，开发者和实验室往往散落着**多个 Google 账号**（`2,500 CU` AI Ultra、`500 CU` Pro+、`100 CU` Pro、免费号，以及同门手里快满 **90 天过期作废的闲置算力**）。而官方工具强制锁死单账号、绑定桌面 Chrome 标签页，且一旦子进程崩成 Linux 僵尸态（`Z`）就会静默烧干付费额度：

| 核心能力对比 | 浏览器多开 Colab 标签页 | Google 官方 `colab-cli` / `colab-mcp` | **🐝 ColabHive** |
| :--- | :---: | :---: | :--- |
| **多账号算力池化** | 手动来回切浏览器配置 | ❌ 硬编码限制单账号 (`supportsMultipleAccounts: false`) | ✅ **无限账号池化汇聚**，并发原子槽位锁 + `waste-zero` 临期额度优先调度 |
| **额度与续航感知** | 藏在二级菜单的纯数字 | 仅打印单账号原始浮点数 | ✅ **实时 GPU 等效可用时长矩阵**（`G4 96GB` / `H100` / `A100` / `L4` / `T4` / `TPU v6e`）+ 免费池刷新倒计时 |
| **多卡并发分片加速** | 手动复制 $K$ 份 Notebook | ❌ 仅支持串行逐台执行 | ✅ **1 条命令 $K$ 路并发分片**（`4 卡 × 1 小时 == 1 卡 × 4 小时`，**总耗费 CU 完全一致，提速 $4\times$！**） |
| **同伴闲置算力共享** | ❌ 绝不能共享 Google 密码 | ❌ 需拷贝高权限本地凭证 | ✅ **30 秒免密贡献门户 (`/contribute/:code`)** + **物理零云盘权限** + **自留保底线** + **一键撤回** |
| **AI Agent MCP 接入** | ❌ 无 | ⚠️ 强依赖 Chrome 标签页、`1013 Server is busy` 单连接锁、`list_changed` 工具隐身 Bug | ✅ **100% 无头三协议 MCP**（`Streamable HTTP /mcp` + `SSE` + `stdio`），启动即暴露全量工具，多 Agent 并发 |
| **防僵尸进程与防掉线** | 锁屏断网 / 15m 空闲强杀 | ⚠️ 35–60m ZMQ 断流；Linux `Z` 僵尸进程骗过 `os.kill(pid,0)` 空转烧干 `9.66 CU/h` | ✅ **可重连 `8s` 心跳保活 + `/proc/{pid}/stat` 防僵尸巡检 + 完工/异常确定性 `unassign` 释放** |
| **跨账号无缝容灾** | 手动反复点击重连 | ❌ 直接报错退出 (`exit 1`) | ✅ **二维健康状态机、中途抢占自愈 + 硬件瀑布流自动顺延**（`H100 ➔ G4 ➔ A100 ➔ L4 ➔ T4`） |
| **单账号出口 IP 隔离** | 全局代理牵一发而动全身 | ❌ 多账号共用宿主机出口 IP | ✅ **单账号独立固定 `HTTP` / `SOCKS5` 代理绑定**（覆盖 OAuth 换票、`/tun/m` 与 Jupyter WS 全链路） |

---

## 🖥️ 实机界面全景导览

### 1. 8 大编程工具无损接入与实时路由拓扑（`/router`）

一键检测并无损写入本地 AI 助手配置（自动快照备份至 `~/.colabhive/backups/first-write/`，原生打通 **Windows 宿主机 + WSL2** 双端路径），实时透视每一条粘性内核绑定与 JSON-RPC 调用耗时：

<p align="center">
  <img src="./docs/assets/screen-router.png" width="100%" alt="ColabHive AI 助手路由拓扑与一键接入抽屉" />
</p>

### 2. 4× Blackwell `G4` 并发分片实验工作台与 60GB 权重热重启（`/exp`）

跨 4 个账号同时拉起 4 块 `NVIDIA RTX PRO 6000 Blackwell (96 GB GDDR7)` 显卡（合计 `384 GB` 显存），统一聚合实时终端流与 Matplotlib 曲线画廊；改代码调试时支持**仅重启任务 Worker PID**，保留 `/root/.cache/huggingface` 内数十 GB 模型权重秒级重跑：

<p align="center">
  <img src="./docs/assets/screen-workbench.png" width="100%" alt="ColabHive 四卡并发分片实验工作台与远程 GPU 终端" />
</p>

### 3. 30 秒零云盘权限同伴算力共享门户（`/contribute/:code`）

让实验室同门安心借出即将过期的闲置 CU：**仅申请 `colaboratory` 单一权限**（从 Google 授权页层面物理杜绝读取任何 Google Drive 文件）、WebSocket 内核层拦截 `drive.mount` 越权调用、触及 `min_reserve_cu` 自留保底线前自动排空停机，并支持凭专属回执随时一键撤回：

<p align="center">
  <img src="./docs/assets/screen-contribute.png" width="100%" alt="ColabHive 零云盘权限好友算力安全共享门户" />
</p>

### 4. 安全加固版云端团队服务器（`--mode cloud --trust-proxy`）

面向实验室公共算力池与远程团队设计：管理后台强制启用 `COLABHIVE_ADMIN_TOKEN` 口令门禁与一次性 60 秒 WebSocket 票据（`/api/v1/ws-ticket`），物理封禁宿主机文件探测接口（`403 Forbidden`），并为每位成员签发带预算硬顶与邮箱脱敏的独立 `ch_sk_live_...` 密钥：

<p align="center">
  <img src="./docs/assets/screen-cloud-mode.png" width="100%" alt="ColabHive 云端部署模式与管理员门禁" />
</p>

---

## 📊 2026 Colab 全系显卡等效时长与跨账号容灾矩阵

ColabHive 内置三级 EWMA 实测燃烧率校准引擎，将枯燥的 CU 数字精准折算为 2026 全系硬件的真实可跑时长：

| 硬件档位 | 显卡架构与显存规格 | CLI / SDK 参数 | 基准燃烧率参考 | 推荐负载定位 |
| :--- | :--- | :---: | :---: | :--- |
| **`G4`** | **NVIDIA RTX PRO 6000 Blackwell · 96 GB GDDR7** (`SM 12.0`) | `--gpu G4` | **`~9.66 CU/hr`** | `27B–70B` BF16 大模型全量推理与长上下文评测首选 |
| **`H100`** | **NVIDIA H100 SXM5 · 80 GB HBM3** (`SM 9.0`) | `--gpu H100` | **`~12.50 CU/hr`** | Hopper FP8 / FlashAttention-3 高密预训练与全参微调 |
| **`A100`** | **NVIDIA A100 SXM4 · 40 / 80 GB** (`SM 8.0`) | `--gpu A100` | **`~8.88 CU/hr`** | `<=14B` BF16 综合性价比之王，完整兼容预编译 Triton 算子 |
| **`L4`** | **NVIDIA L4 Ada · 24 GB GDDR6** (`SM 8.9`) | `--gpu L4` | **`~4.52 CU/hr`** | `7B–9B` 推理、LoRA 超参扫描、多模态生成 |
| **`T4`** | **NVIDIA Tesla T4 · 16 GB GDDR6** (`SM 7.5`) | `--gpu T4` | **`~1.96 CU/hr`** *(免费层 0)* | INT4/FP16 轻量推理 + 自动利用免费层 milli-CCU 窗口 |
| **`TPU v6e / v5e`** | **Cloud TPU v6e-1 (32 GB) / v5e-1 (16 GB)** | `--tpu v6e1` | **`~5.30 / 3.85 CU/hr`** | 高能效 JAX / XLA / PyTorch-XLA 训练任务 |

<details>
<summary><b>🔀 展开查看：跨账号异常与抢占自动容灾矩阵</b></summary>

<br />

| 上游异常场景 | 官方工具默认表现 | ColabHive 智能路由与守护引擎动作 |
| :--- | :--- | :--- |
| **`HTTP 400` 区域缺卡 / 订阅档位受限** | 直接报错中断任务 | 将 `(账号, 机型)` 标记临时冷却；优先漂移至池内其它可用账号，或按 `H100 ➔ G4 ➔ A100 ➔ L4 ➔ T4` 自动顺延 |
| **`HTTP 412` 单账号并发实例达上限** | 直接退出 (`exit 1`) | 内存级并发原子槽位预占锁杜绝分片踩踏；溢出请求毫秒级切往下一健康账号 |
| **`HTTP 429` / `5xx` 控制面限流抖动** | 工具调用失败 | 指数退避 + 立即横向切换至绑定独立出口代理的健康账号重试 |
| **OAuth `invalid_grant` 令牌失效** | 弹窗阻塞无头流程 | 自动隔离失效账号并点亮告警徽标，当前请求无缝漂移至备用账号，Agent 无感知 |
| **执行中途云端实例被抢占回收** | 连接挂死丢失响应 | `8s` 窗口自动重连 ZMQ 通道；若实例已销毁则自动在健康账号拉起新会话恢复服务 |
| **训练子进程崩溃进入 Linux 僵尸态 (`Z`)** | `os.kill(pid, 0)` 误判存活白烧额度 | 每 `8s` 读取 `/proc/{pid}/stat` 第 3 字段，精准捕获 `Z` 态并立即触发 `/tun/m/unassign` 停机止损 |

</details>

---

## 🔌 一键接入主流 AI 编程 Agent

在**本地桌面模式**下，直接在系统托盘 **`Agent`** 页拨动开关即可免密直连（`http://127.0.0.1:7860/mcp` 零密钥开箱即用！）；在**云端团队模式**下，配置 HTTPS 地址与受预算约束的 `ch_sk_live_...` 密钥：

<details open>
<summary><b>Antigravity / Cursor / Windsurf / Claude Code (MCP Streamable HTTP)</b></summary>

**本地桌面模式（本机回环免密直连）：**
```json
{
  "mcpServers": {
    "colabhive": {
      "url": "http://127.0.0.1:7860/mcp"
    }
  }
}
```

**云端团队服务器（携带分权预算密钥 `ch_sk_...`）：**
```json
{
  "mcpServers": {
    "colabhive": {
      "url": "https://hive.lab.example.com/mcp",
      "headers": {
        "Authorization": "Bearer ch_sk_live_xxxxxxxxxxxxxxxx"
      }
    }
  }
}
```
*（Claude Code 命令行一键添加：`claude mcp add --transport http colabhive https://hive.lab.example.com/mcp --header "Authorization: Bearer ch_sk_live_xxxxxxxxxxxxxxxx"`）*
</details>

<details>
<summary><b>OpenAI Codex (<code>~/.codex/config.toml</code>)</b></summary>

```toml
# 本地免密模式
[mcp_servers.colabhive]
url = "http://127.0.0.1:7860/mcp"
tool_timeout_sec = 1800

# 云端模式将 url 替换为 https://hive.lab.example.com/mcp 并添加：
# http_headers = { Authorization = "Bearer ch_sk_live_xxxxxxxxxxxxxxxx" }
```
</details>

<details>
<summary><b>Gemini CLI (<code>~/.gemini/settings.json</code>)</b></summary>

```json
{
  "mcpServers": {
    "colabhive": {
      "httpUrl": "http://127.0.0.1:7860/mcp",
      "timeout": 1800000
    }
  }
}
```
</details>

<details>
<summary><b>Claude Desktop / 本地 <code>stdio</code> 桥接模式</b></summary>

```json
{
  "mcpServers": {
    "colabhive": {
      "command": "/absolute/path/to/colabhive",
      "args": ["mcp", "stdio", "--strategy", "waste-zero"]
    }
  }
}
```
*（`colabhive mcp` 会自动读取 `~/.colabhive/daemon.json` 无缝桥接至正在运行的桌面守护进程。）*
</details>

---

## 🧰 100% 兼容命令行与零依赖 Python SDK

### 无缝接管 `colab` CLI 与多账号并发分片评测

```bash
# 查看全池汇总 CU 与各档 GPU 等效剩余可用小时数
colabhive usage --gpu-hours

# 100% 兼容 `colab run`：纯净脚本 stdout 输出，运行结束确保自动释放计费！
colab run --gpu G4 eval_single.py > predictions.jsonl

# 跨 4 个 Colab 账号并发拉起 4 块 96GB G4 分片评测（总耗费 CU 不变，耗时缩短至 1/4！）
colabhive run --gpu G4 --shards 4 --strategy waste-zero --experiment qwen27b_eval eval_shard.py
```

### Python 3.10+ SDK（`colabhive-sdk`）与同伴一键捐献命令

零第三方强制依赖（纯 Python 标准库即可运行，亦可选装 `pip install "./sdk/python[httpx]"` 开启连接池）：

```python
from colabhive import ColabHiveClient

client = ColabHiveClient(
    base_url="http://127.0.0.1:7860",  # 云端模式填入 https://hive.lab.example.com 与 api_key="ch_sk_live_..."
)

# 1. 上下文管理自动分配 Blackwell G4 (96 GB) 实例 —— 退出代码块自动释放显卡！
with client.session(gpu="G4") as sess:
    sess.upload("./config.yaml", "/content/config.yaml")
    res = sess.exec("import torch; print(torch.cuda.get_device_name(0))")
    print(res.stdout)

# 2. 1 行代码跨账号启动 4 分片并发评测
exp = client.run_sharded("eval_shard.py", shards=4, gpu="G4", wait=True)
print(exp.experiment_id, exp.status)
```

```bash
# 同门 1 条命令贡献闲置算力（自动锁定 50 CU 保底自留额度，物理零 Google Drive 权限）
colabhive-sdk donate https://hive.lab.example.com/contribute/ch_inv_8x92kP --reserve-cu 50
```

---

## 🏗️ 系统架构全景与双模安全边界

```mermaid
flowchart TB
    subgraph Clients["AI 编程助手、科研人员与算力贡献者"]
        TRAY["原生无边框桌面端 + 四栏托盘 / 菜单栏 (Windows & macOS)"]
        AG["Antigravity · Cursor · Windsurf · Claude Desktop"]
        CC["Claude Code · Codex · Gemini CLI · OpenCode"]
        CLI["colabhive / colab CLI + Python SDK"]
        GUEST["外部算力贡献者 (/contribute/:code 与 colabhive-sdk donate)"]
    end

    subgraph Edge["安全部署边界（本地回环 :7860 | 云端 Caddy / Nginx HTTPS 反代）"]
        RPX["可选 Caddy / Nginx TLS 反向代理<br/>flush_interval -1 · proxy_buffering off · 1800s GPU 分配超时"]
    end

    subgraph Hive["ColabHive 统一控制面与路由中枢 (纯 Go 单文件静态编译)"]
        MCP["统一 MCP 服务器 (Streamable HTTP /mcp + SSE + stdio)<br/>静态工具表 · RichOutputs 旁路落盘 · 外部网页会话防抢占保护"]
        RTR["智能算力路由器<br/>并发原子槽位锁 · 临期零浪费 · 会话粘性绑定 · 跨账号故障自动漂移"]
        WDG["高可靠守护与安全防火墙<br/>8s 防僵尸检测 (/proc/stat) · 空闲自动回收 · 保底额度熔断 · 云盘权限防火墙"]
        VLT["纯 Go SQLite + AES-256-GCM 加密凭证金库 (.chvault 跨服迁移)"]
    end

    subgraph Google["Google 官方 Colab 双后端基础设施（按账号严格隔离出口代理）"]
        PUB["Colab Public API<br/>colaboratory.googleapis.com/v1beta"]
        TUN["Colab Session Tunnel API<br/>colab.research.google.com/tun/m"]
        GAPI["Colab GAPI 额度与机型权限探针<br/>colab.pa.googleapis.com/v1/user-info"]
        VM["Colab GPU / TPU 实例运行时<br/>Jupyter ZMQ-over-WS + /api/colab/resources"]
    end

    TRAY --> Hive
    AG & CC & CLI & GUEST --> RPX --> Hive
    MCP --> RTR
    RTR --> WDG & VLT
    WDG -->|"单账号固定 HTTP / SOCKS5 代理"| PUB & TUN & GAPI & VM
```

| 安全与运维维度 | 🖥️ **本地桌面模式 (`colabhive-desktop` / `--mode local`)** | ☁️ **云端团队服务器 (`--mode cloud --trust-proxy`)** |
| :--- | :--- | :--- |
| **客户端形态** | Windows 无边框圆角托盘端（`-H=windowsgui`）+ macOS 原生菜单栏应用（`ColabHive.app`） | 非 root Alpine 容器或无头静态二进制，前置 Caddy / Nginx 反代 |
| **管理后台鉴权** | 本机回环 `127.0.0.1:7860` 开箱即用（严格校验 `Host` 防 DNS 重绑定；可选 `--admin-token`） | **强制开启 `COLABHIVE_ADMIN_TOKEN`** + 60 秒一次性 WebSocket 鉴权票据（`/api/v1/ws-ticket`） |
| **Agent MCP 鉴权** | **本机免密直连（`http://127.0.0.1:7860/mcp`）** + `~/.colabhive/daemon.json` 自动寻址 | **分权 `ch_sk_...` 密钥**，支持账号组 ACL、每日/总 CU 硬顶与账号邮箱严格脱敏 |
| **宿主机文件接口** | 开启：支持扫描本机与 WSL2 凭证（`scan-local`）及 `cc-switch` 外科手术式注入 | **物理禁用（`403 Forbidden`）**：硬关闭 `scan-local`、宿主机配置改写与桌面控制桥 |
| **凭证库静态加密** | 数据库同目录自动维护 `.master.key`（`AES-256-GCM`）+ `PBKDF2-SHA256` `.chvault` 导出 | 强制显式传入 `COLABHIVE_MASTER_KEY`（`AES-256-GCM`），结构体层 `json:"-"` 杜绝序列化泄露 |

---

## 📚 源码编译与深度协议规范

- **从源码一键构建发行版**：
  - Windows (`pwsh`)：`.\scripts\build-release.ps1`（自动构建 `web/dist`、`bin/colabhive.exe`、`bin/colab.exe` 与 `bin/colabhive-desktop.exe`）
  - macOS / Linux (`bash`)：`./scripts/build-release.sh`（自动构建命令行双二进制、`web/dist` 与 macOS `ColabHive.app` 应用包）
- **架构与协议文档**：
  - [`docs/architecture/colab-protocol-spec.md`](./docs/architecture/colab-protocol-spec.md) — Colab 隧道协议、OAuth2 PKCE、Keyring 转义、Jupyter ZMQ WS 与防僵尸守护规范
  - [`docs/architecture/google-official-and-ccswitch-spec.md`](./docs/architecture/google-official-and-ccswitch-spec.md) — Google 官方双后端 API（`v1beta` + `/tun/m`）、`cc-switch` 无损同步与贡献者防火墙规范
  - [`docs/architecture/product-ux-spec.md`](./docs/architecture/product-ux-spec.md) — Precision Compute Observatory 视觉设计系统与双语交互规范
  - [`SECURITY.md`](./SECURITY.md) · [`CONTRIBUTING.md`](./CONTRIBUTING.md) · [`CHANGELOG.md`](./CHANGELOG.md)

---

## ⭐ Star 趋势与开源致谢

## 演进路线图 (v1.1 Roadmap)

非常欢迎社区开发者提交 Issue 与 PR 共建！**v1.1+** 核心规划如下：

- [ ] **96GB G4 免隧道本机 `/v1` 大模型 API 网关**：在池化的 Colab 96GB 大显存实例上一键拉起 `vLLM` / `llama.cpp` / `Ollama`，通过现有鉴权通道直接反代至本机 `http://127.0.0.1:7860/v1/chat/completions` 与 `/v1/messages`（无需暴露公网隧道，支持多号耗尽无感切号，无缝接入 Magpie、Cursor、Claude Code、Cherry Studio 与 Open WebUI）。
- [ ] **一键云端 GPU 应用模板中心（1-Click App Templates）**：内置 **ComfyUI 视频与多模态工作流**（Wan2.2 / 混元视频 / Flux 权重云盘软链复用）、**Gradio 演示台**与 **JupyterLab** 一键部署卡片，自带 8 秒防掉线保活、闲置自动释放省额度与分享链接打码保护。
- [ ] **可插拔多算力源扩展（Multi-Provider Pool）**：在现有调度引擎上扩展 `Provider` 插件接口，支持接入 **Vast.ai**、**RunPod** 低价按秒竞价显卡、**Kaggle Notebooks**（每周 30 小时免费双卡 T4 离线分片）与 **Modal** 弹性算力。
- [ ] **更多界面多语言包**：扩充繁体中文、日语（`日本語`）、韩语（`한국어`）等国际化语言包。

---

由衷致谢 [`googlecolab/google-colab-cli`](https://github.com/googlecolab/google-colab-cli)、[`googlecolab/colab-vscode`](https://github.com/googlecolab/colab-vscode)、[`googlecolab/colab-mcp`](https://github.com/googlecolab/colab-mcp)、[`Wei-Shaw/sub2api`](https://github.com/Wei-Shaw/sub2api)、[`yetone/magpie`](https://github.com/yetone/magpie) 与 [`farion1231/cc-switch`](https://github.com/farion1231/cc-switch) 开源项目带来的协议参考与交互灵感。

<div align="center">
  <a href="https://star-history.com/#hexwander/colabhive&Date">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=hexwander/colabhive&type=Date&theme=dark" />
      <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=hexwander/colabhive&type=Date" />
      <img alt="ColabHive Star History Chart" src="https://api.star-history.com/svg?repos=hexwander/colabhive&type=Date" width="760" />
    </picture>
  </a>
</div>

## 开源许可

本项目基于 [Apache License 2.0](./LICENSE) 协议开源。
