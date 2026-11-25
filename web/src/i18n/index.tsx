import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useMemo,
  useState,
} from 'react';
import { maskEmail, maskIP, maskProxyUrl, maskSecret } from '../lib/api';

export type Locale = 'zh-CN' | 'en-US';
export type ThemeMode = 'dark' | 'light';

/**
 * v3 copy guideline: say what the user gets, in the words they would use.
 * "账号" not "凭证", "还能跑多久" not "续航", "接入" not "注入".
 * Every key that existed in v2 is still here; v3 only adds keys.
 */
export const zhCN = {
  // Brand & Global Shell
  'app.name': 'ColabHive',
  'app.tagline': '把多个 Colab 账号的算力汇成一池，让 AI 编程助手随时调用云端显卡',
  'nav.observatory': '算力总览',
  'nav.chat': '无审查大模型部署',
  'nav.apps': 'AI 应用商店',
  'nav.apps_sub_chat': 'WanGP / 无审查',
  'nav.savings': '算力省钱账单',
  'nav.savings_receipt': '省钱账单',
  'nav.chat_hint': 'Heretic / Abliterated 无审查开源模型一键部署 · 吐出本机 /v1',
  'nav.apps_hint': 'WanGP 视频生图 · ComfyUI · Unsloth 微调 · 第三方插件',
  'nav.pool': '账号与算力池',
  'nav.workbench': '实验工作台',
  'nav.router': 'MCP 接入与路由',
  'nav.settings': '密钥与代理设置',
  'nav.privacy_mask': '隐藏隐私',
  'nav.privacy_mask_on': '已隐藏隐私',
  'nav.connect_agent': '接入 AI 助手',
  'nav.share_card': '生成算力分享图',
  'nav.theme_dark': '深色',
  'nav.theme_light': '浅色',
  'footer.daemon_healthy': '本地服务运行正常',
  'footer.daemon_offline': '正在重新连接…',
  'footer.keepalive': '防掉线保活',
  'footer.anti_zombie': '卡死任务自动清理已开启',
  'footer.firewall': '好友账号安全保护已开启',
  'footer.ws_latency': '实时连接延迟',

  // v4 Stardust structured navigation, search & live blocks
  'nav.quick_guide': '新手快速引导',
  'nav.quick_guide_hint': '3 步连上算力池与 AI 助手',
  'nav.zone_overview_hint': '看还能跑多久 · 3 步开始',
  'nav.zone_pool_hint': '添加账号 · 导入本机凭证',
  'nav.zone_workbench_hint': '代码日志 · 显卡实时负载',
  'nav.zone_router_hint': '连上 AI 助手 · 选号与密钥',
  'nav.sub_router': '选号与路由',
  'nav.sub_settings': '密钥与代理',
  'nav.hub_tab_router': '智能选号',
  'nav.hub_tab_settings': '密钥与代理',
  'nav.status_demo': '演示数据预览',
  'nav.status_connected': '本地服务已连接',
  'nav.status_degraded': '本地服务异常',
  'nav.retry_connect': '重试连接本地服务',
  'nav.search_placeholder': '搜索账号、实验或工具...',
  'nav.group_workspace': '主工作区',
  'nav.group_devtools': '开发者与高级工具',
  'nav.group_system': '接入与系统',
  'nav.live_tasks': '运行中',
  'nav.watchlist': '额度提醒',
  'nav.pool_rings': '卡池实时余量',
  'nav.top_pool_label': '池内余量',

  // Observatory (`/`) — Canonical Glossary Keys
  'hero.total_cu': '可用算力总额 (CU)',
  'hero.schedulable_cu': '仅算可用额度',
  'hero.burn_rate': '当前消耗速度',
  'hero.depletion_eta': '预计还能跑多久',
  'hero.active_vms': '正在运行的显卡',
  'hero.saved_waste': '自动省下的闲置额度',
  'hero.idle_infinite': '∞ 当前空闲，不扣额度',
  'hero.payg_equiv': '约合按量付费价值',

  // Cloud GPU Equivalent Runtime Matrix
  'matrix.title': '各型号显卡还能跑多久',
  'matrix.subtitle': '根据实时消耗速度，把抽象的 CU 额度换算成每种显卡真正能跑的小时数',
  'matrix.scope_pool': '全部账号合计',
  'matrix.scope_account': '按单账号查看',
  'matrix.scope_shards': '多卡并行提速',
  'matrix.calibrated': '实测精准换算',
  'matrix.empirical': '历史均值换算',
  'matrix.reference': '官方标准换算',
  'matrix.shard_simulator': '多卡并行省时计算器',
  'matrix.wall_clock': '实际等待耗时',
  'matrix.zero_extra_cu': '多张卡同时跑能成倍缩短等待时间，总消耗额度完全不变',
  'matrix.free_window': '有免费卡可用',
  'matrix.locked': '当前套餐不支持',

  // Account Pool & Onboarding (`/pool`)
  'pool.title': 'Colab 账号与算力池',
  'pool.add_account': '添加我的 Colab 账号',
  'pool.import_migrate': '导入已有登录凭证',
  'pool.refresh_all': '刷新全部余额',
  'pool.oauth_wizard': '用 Google 登录添加账号（远程服务器也能用）',
  'pool.wizard_step1': '1. 选择网络出口与分组',
  'pool.wizard_step2': '2. 生成 Google 登录链接（只申请必要权限）',
  'pool.wizard_step3': '3. 粘贴回调链接或授权码',
  'pool.wizard_step4': '4. 检查余额并确认添加',
  'pool.test_proxy': '测试网络是否连通',
  'pool.copy_auth_url': '复制登录链接',
  'pool.open_auth_window': '在新窗口打开登录',
  'pool.paste_placeholder': '粘贴 4/0A... 授权码，或粘贴整条 localhost / sdk.cloud.google.com 回调 URL',
  'pool.exchange_and_add': '验证并添加账号',
  'pool.auto_detect_cli': '自动检测本机与 WSL2 中的 Google 账号',
  'pool.adc_json_tab': '上传或粘贴 JSON 凭证文件',
  'pool.vault_tab': '导出或恢复加密备份包 (.chvault)',
  'pool.remaining': '可用余额',
  'pool.reserve_floor': '保底留存',
  'pool.contributor_badge': '好友共享',
  'pool.free_first_title': '🎁 优先烧免费额度 (Free-First)',
  'pool.free_first_desc': '开启后交互式应用与 AI 聊天优先调度 Google Colab 免费 T4 窗口，零元用满后再顺延至付费 CU 账号。',
  'pool.free_first_on': '免费优先已开启',
  'pool.free_first_off': '一键开启免费优先',
  'pool.badge_colab': 'Colab',

  // Live Experiment Workbench (`/exp`)
  'exp.title': '实验实况与远程 GPU 终端',
  'exp.new_session': '启动新 GPU 实例',
  'exp.launch_sharded': '多卡并行跑实验',
  'exp.anti_zombie': '卡死任务自动检测 (/proc/stat)',
  'exp.keepalive_pulse': '自动保活，防止 15 分钟被回收',
  'exp.hot_restart': '只重启任务（保留已下载的模型缓存）',
  'exp.open_colab_web': '在 Colab 官网打开同一个实例',
  'exp.kill_release': '停止并立即释放显卡',
  'exp.terminal': '实时终端输出',
  'exp.waterfall': '运行步骤与各阶段耗时',
  'exp.artifacts': '图表结果与 /content 文件',
  'exp.all_shards': '全部并行任务汇总',

  // Agent Session Router & Control Plane (`/router` & `/cfg`)
  'router.title': 'AI 助手接入与智能选号规则',
  'router.free_first': '🎁 优先烧免费额度（先用满 Colab 免费卡）',
  'router.waste_zero': '快过期额度优先（自动防浪费）',
  'router.max_balance': '余额最多优先（适合长时训练）',
  'router.sticky': '固定同一台实例（保留显存与模型缓存）',
  'router.round_robin': '多账号轮流分配（最大化并行卡数）',
  'router.free_opportunistic': '优先白嫖免费 T4 / CPU',
  'router.fallback_chain': '显卡缺货或报错时，自动换用其他型号',
  'router.sticky_table': '当前固定在同一实例上的会话',
  'router.trace_inspector': 'AI 助手调用记录（实时）',

  // Keys, Proxies & 1-Click Agent Connect Drawer
  'cfg.api_keys': 'AI 助手访问密钥 (ch_sk_...)',
  'cfg.create_key': '新建访问密钥',
  'cfg.proxies': '网络代理（每个账号可走独立出口）',
  'cfg.add_proxy': '添加代理',
  'connect.title': '一键连上你的 AI 编程助手',
  'connect.subtitle': '自动把 ColabHive 算力工具写入 AI 助手配置，首次修改前自动备份原文件，随时可一键撤销',
  'connect.inject': '一键接入',
  'connect.injected': '已连上（点击断开）',
  'connect.copy_snippet': '复制配置内容',
  'connect.backup_safe': '原配置已自动备份',
  'connect.step_pick': '选好常用编程助手',
  'connect.step_inject': '点击一键接入',
  'connect.step_use': '对话中直接调用显卡',

  // v3 Quick Start Launchpad (`{n}` is replaced with a live count)
  'quickstart.title': '3 步快速开始',
  'quickstart.value_prop': '把散落在多个 Google 账号里的 Colab 额度汇成一池，让你的 AI 编程助手随时调用云端大显存 GPU。',
  'quickstart.progress': '已完成 {n}/3',
  'quickstart.collapse': '收起引导',
  'quickstart.expand': '展开查看',
  'quickstart.collapsed_line': '新手引导：3 步连上 Colab 算力池与 AI 助手（进度 {n}/3）',
  'quickstart.demo_pill': '正在浏览演示数据 · 如何接入真实账号？',
  'quickstart.real_pill': '如何安全接入真实账号？',
  'quickstart.demo_step1': '在这台电脑上启动 ColabHive 本地服务',
  'quickstart.demo_step2': '启动后回到这里重试连接，连上后再添加你的账号',
  'quickstart.demo_note': '演示数据只是让你提前看到界面效果，不会消耗任何真实额度。',
  'quickstart.cmd_generic': 'macOS / Linux',
  'quickstart.cmd_windows': 'Windows',
  'quickstart.retry': '重试连接本地服务',
  'quickstart.retrying': '正在连接…',
  'quickstart.retry_failed': '还没连上。请确认上面的命令正在运行，然后再试一次。',
  'quickstart.helper_title_demo': '两步接入你的真实账号',
  'quickstart.helper_title_real': '你的账号是这样被保护的',
  'quickstart.safe_oauth_title': '走 Google 官方登录，不经手密码',
  'quickstart.safe_oauth_body': '好友通过共享链接授权时只申请运行 Colab 必需的权限，不含 Google 云盘；添加你自己的账号时，权限与官方 colab 命令行工具一致。',
  'quickstart.safe_vault_title': '登录凭证只存在本机',
  'quickstart.safe_vault_body': '凭证用 AES-256-GCM 加密后保存在运行 ColabHive 的这台机器上。',
  'quickstart.safe_revoke_title': '随时可以收回',
  'quickstart.safe_revoke_body': '在「账号与共享」里移除账号，或到 Google 账号的安全设置里撤销授权。',
  'quickstart.step': '第 {n} 步',
  'quickstart.done': '已完成',
  'quickstart.s1_title': '添加或共享 Colab 账号',
  'quickstart.s1_body': '用 OAuth 安全登录自己的 Google 账号，或生成免密链接邀请朋友共享闲置额度（严禁读取云盘，可设保底水位）。',
  'quickstart.s1_primary': '添加我的账号',
  'quickstart.s1_secondary': '邀请朋友共享',
  'quickstart.s1_demo_badge': '当前含 {n} 个演示账号',
  'quickstart.s1_live_badge': '已接入 {n} 个账号',
  'quickstart.s1_todo_badge': '还没有添加账号',
  'quickstart.s2_title': '一键连上你的 AI 编程助手',
  'quickstart.s2_body': '支持 Claude Code、Antigravity、Cursor、Codex 等，一键自动写入 MCP 配置，不用手动翻找配置文件。',
  'quickstart.s2_primary': '一键接入 AI 助手',
  'quickstart.s2_secondary': '选号策略',
  'quickstart.s2_live_badge': 'AI 助手已连上',
  'quickstart.s2_todo_badge': '还没有连接',
  'quickstart.s3_title': '提交首个云端 GPU 实验',
  'quickstart.s3_body': '直接对 AI 说「帮我在 Colab 跑这段代码」，或进入实验实况页打开网页终端、实时查看显卡负载与输出。',
  'quickstart.s3_primary': '查看实验实况',
  'quickstart.s3_secondary': '复制 AI 测试指令',
  'quickstart.s3_copied': '已复制指令！',
  'quickstart.s3_demo_badge': '{n} 台演示 GPU 运行中',
  'quickstart.s3_live_badge': '{n} 台 GPU 运行中',
  'quickstart.s3_todo_badge': '还没有运行中的实验',
  'quickstart.test_prompt': '请调用 ColabHive 查看当前可用 GPU 算力池，并分配一张空闲 GPU 运行 nvidia-smi',

  // Common Actions & Statuses
  'common.save': '保存更改',
  'common.cancel': '取消',
  'common.delete': '删除',
  'common.copy': '复制',
  'common.copied': '已复制',
  'common.loading': '正在加载最新数据…',
  'common.healthy': '空闲可用',
  'common.working': '运行中',
  'common.cooldown': '短暂冷却中',
  'common.expired': '需重新登录',
  'quick.chat': '无审查大模型部署 (/v1)',
  'quick.apps': 'WanGP 视频 & AI 应用商店',
  'quick.free_first': '免费额度优先开关',
} as const;

export type TranslationKey = keyof typeof zhCN;

export const enUS: Record<TranslationKey, string> = {
  // Brand & Global Shell
  'app.name': 'ColabHive',
  'app.tagline': 'Pool Colab compute across accounts so your AI coding assistant can run cloud GPUs anytime',
  'nav.observatory': 'Compute Overview',
  'nav.chat': 'Uncensored LLM Deploy',
  'nav.apps': 'AI App Store',
  'nav.apps_sub_chat': 'WanGP / LLM',
  'nav.savings': 'Compute Savings',
  'nav.savings_receipt': 'Savings Receipt',
  'nav.chat_hint': '1-Click Heretic / Abliterated open-weight LLM /v1 endpoint',
  'nav.apps_hint': 'WanGP Video & Image · ComfyUI · Unsloth Studio · Plugins',
  'nav.pool': 'Accounts & Pool',
  'nav.workbench': 'Experiment Workbench',
  'nav.router': 'MCP & Routing',
  'nav.settings': 'Keys & Proxies',
  'nav.privacy_mask': 'Hide Privacy',
  'nav.privacy_mask_on': 'Privacy Hidden',
  'nav.connect_agent': 'Connect AI',
  'nav.share_card': 'Share Snapshot',
  'nav.theme_dark': 'Dark',
  'nav.theme_light': 'Light',
  'footer.daemon_healthy': 'Local Service Running',
  'footer.daemon_offline': 'Reconnecting…',
  'footer.keepalive': 'Keep-Alive',
  'footer.anti_zombie': 'Stuck-Job Cleanup On',
  'footer.firewall': 'Friend Account Protection On',
  'footer.ws_latency': 'Live Link Latency',

  // v4 Stardust structured navigation, search & live blocks
  'nav.quick_guide': 'Quick Start Guide',
  'nav.quick_guide_hint': 'Get set up in 3 steps',
  'nav.zone_overview_hint': 'Runtime left & quick start',
  'nav.zone_pool_hint': 'Add accounts & import local CLI',
  'nav.zone_workbench_hint': 'Live code, logs & GPU load',
  'nav.zone_router_hint': 'Connect AI IDEs, routing & keys',
  'nav.sub_router': 'Routing',
  'nav.sub_settings': 'Keys & Proxies',
  'nav.hub_tab_router': 'Smart Routing',
  'nav.hub_tab_settings': 'Keys & Proxies',
  'nav.status_demo': 'Demo Data Preview',
  'nav.status_connected': 'Local Service Connected',
  'nav.status_degraded': 'Local Service Issue',
  'nav.retry_connect': 'Retry Local Service',
  'nav.search_placeholder': 'Search accounts, jobs, tools...',
  'nav.group_workspace': 'Workspace',
  'nav.group_devtools': 'Developer & Advanced',
  'nav.group_system': 'Integrations & System',
  'nav.live_tasks': 'Running',
  'nav.watchlist': 'Quota Watchlist',
  'nav.pool_rings': 'Live Pool Quota',
  'nav.top_pool_label': 'Pool Quota',

  // Observatory (`/`) — Canonical Glossary Keys
  'hero.total_cu': 'Total Available Compute (CU)',
  'hero.schedulable_cu': 'Usable Quota Only',
  'hero.burn_rate': 'Current Usage Rate',
  'hero.depletion_eta': 'Est. Runtime Left',
  'hero.active_vms': 'Running GPUs',
  'hero.saved_waste': 'Saved Idle Quota',
  'hero.idle_infinite': '∞ Idle right now (0 CU/h)',
  'hero.payg_equiv': 'Est. Pay-As-You-Go Value',

  // Cloud GPU Equivalent Runtime Matrix
  'matrix.title': 'Remaining Hours by GPU Model',
  'matrix.subtitle': 'Converts abstract CU balance into real usable runtime hours for each GPU',
  'matrix.scope_pool': 'All Accounts',
  'matrix.scope_account': 'By Account',
  'matrix.scope_shards': 'Parallel Speedup',
  'matrix.calibrated': 'Live Calibrated',
  'matrix.empirical': 'Historical Avg',
  'matrix.reference': 'Reference Rate',
  'matrix.shard_simulator': 'Parallel Speedup Calculator',
  'matrix.wall_clock': 'Actual Wait Time',
  'matrix.zero_extra_cu': 'Running across multiple GPUs cuts wait time with zero extra CU cost',
  'matrix.free_window': 'Free GPU Ready',
  'matrix.locked': 'Unavailable on Plan',

  // Account Pool & Onboarding (`/pool`)
  'pool.title': 'Colab Accounts & Compute Pool',
  'pool.add_account': 'Add My Colab Account',
  'pool.import_migrate': 'Import Saved Login',
  'pool.refresh_all': 'Refresh Balances',
  'pool.oauth_wizard': 'Add an Account with Google Sign-In (Works on Remote Servers)',
  'pool.wizard_step1': '1. Pick Network Exit & Group',
  'pool.wizard_step2': '2. Create a Google Sign-In Link (Minimum Permissions)',
  'pool.wizard_step3': '3. Paste the Callback URL or Auth Code',
  'pool.wizard_step4': '4. Check Balance & Confirm',
  'pool.test_proxy': 'Test Network Connection',
  'pool.copy_auth_url': 'Copy Sign-In Link',
  'pool.open_auth_window': 'Open Sign-In Window',
  'pool.paste_placeholder': 'Paste 4/0A... auth code OR full localhost / sdk.cloud.google.com redirect URL',
  'pool.exchange_and_add': 'Verify & Add Account',
  'pool.auto_detect_cli': 'Scan This PC / WSL2 for Signed-In colab Accounts',
  'pool.adc_json_tab': 'Import gcloud ADC / JSON',
  'pool.vault_tab': 'Encrypted Backup (.chvault)',
  'pool.remaining': 'Available',
  'pool.reserve_floor': 'Safety Reserve',
  'pool.contributor_badge': 'Friend Shared',
  'pool.free_first_title': '🎁 Zero-Dollar Priority (Free-First)',
  'pool.free_first_desc': 'Prioritizes $0 Google Colab Free T4 windows for apps and AI Chat before falling back to paid CU accounts.',
  'pool.free_first_on': 'Free-First Active',
  'pool.free_first_off': 'Enable Free-First',
  'pool.badge_colab': 'Colab',

  // Live Experiment Workbench (`/exp`)
  'exp.title': 'Live Experiments & GPU Console',
  'exp.new_session': 'Start GPU Session',
  'exp.launch_sharded': 'Run Parallel Experiment',
  'exp.anti_zombie': 'Stuck-Job Detection (/proc/stat)',
  'exp.keepalive_pulse': 'Auto Keep-Alive (Beats the 15m Timeout)',
  'exp.hot_restart': 'Restart Job Only (Keep Model Cache)',
  'exp.open_colab_web': 'Open Same Session on Colab',
  'exp.kill_release': 'Stop & Release GPU',
  'exp.terminal': 'Live Terminal Output',
  'exp.waterfall': 'Run Steps & Timing',
  'exp.artifacts': 'Charts & /content Files',
  'exp.all_shards': 'All Parallel Jobs',

  // Agent Session Router & Control Plane (`/router` & `/cfg`)
  'router.title': 'AI Agent Hub & Smart Account Routing',
  'router.free_first': '🎁 Free-First (Drain Free Colab First)',
  'router.waste_zero': 'Expiring Quota First (Zero Waste)',
  'router.max_balance': 'Highest Balance First (Long Runs)',
  'router.sticky': 'Keep Same GPU Session (Preserve Cache)',
  'router.round_robin': 'Even Spread Across Accounts',
  'router.free_opportunistic': 'Free GPU First When Available',
  'router.fallback_chain': 'Auto-Switch GPU Model When Out of Stock',
  'router.sticky_table': 'Sessions Pinned to One GPU',
  'router.trace_inspector': 'Live AI Assistant Call Log',

  // Keys, Proxies & 1-Click Agent Connect Drawer
  'cfg.api_keys': 'AI Assistant Access Keys (ch_sk_...)',
  'cfg.create_key': 'New Access Key',
  'cfg.proxies': 'Network Proxies (Per-Account Exit)',
  'cfg.add_proxy': 'Add Proxy',
  'connect.title': 'Connect Your AI Coding Assistant in 1 Click',
  'connect.subtitle': 'Safely adds ColabHive MCP tools to your AI coding assistant with automatic backup and 1-click undo',
  'connect.inject': '1-Click Connect',
  'connect.injected': 'Connected (Click to Disconnect)',
  'connect.copy_snippet': 'Copy Config',
  'connect.backup_safe': 'Original Config Backed Up',
  'connect.step_pick': 'Pick your coding assistant',
  'connect.step_inject': 'Click 1-Click Connect',
  'connect.step_use': 'Run cloud GPUs right in chat',

  // v3 Quick Start Launchpad (`{n}` is replaced with a live count)
  'quickstart.title': 'Quick Start in 3 Steps',
  'quickstart.value_prop': 'Pool Colab compute across accounts and let your AI coding assistant run GPU jobs in one click.',
  'quickstart.progress': '{n}/3 Ready',
  'quickstart.collapse': 'Collapse',
  'quickstart.expand': 'Expand',
  'quickstart.collapsed_line': 'Quick Guide: connect your Colab pool and AI assistant in 3 steps ({n}/3 ready)',
  'quickstart.demo_pill': 'Viewing Demo Data · How to connect real accounts?',
  'quickstart.real_pill': 'How are real accounts kept safe?',
  'quickstart.demo_step1': 'Start the ColabHive local service on this computer',
  'quickstart.demo_step2': 'Once it is running, retry the connection here, then add your account',
  'quickstart.demo_note': 'Demo data only previews the interface. It never spends real quota.',
  'quickstart.cmd_generic': 'macOS / Linux',
  'quickstart.cmd_windows': 'Windows',
  'quickstart.retry': 'Retry Local Service',
  'quickstart.retrying': 'Connecting…',
  'quickstart.retry_failed': 'Still not connected. Make sure the command above is running, then try again.',
  'quickstart.helper_title_demo': 'Connect your real accounts in two steps',
  'quickstart.helper_title_real': 'How your accounts are protected',
  'quickstart.safe_oauth_title': "Google's own sign-in, no passwords",
  'quickstart.safe_oauth_body': 'Friend-share links request only what Colab needs, with no Google Drive access. Adding your own account uses the same permissions as the official colab CLI.',
  'quickstart.safe_vault_title': 'Sign-in data stays on this machine',
  'quickstart.safe_vault_body': 'Credentials are encrypted with AES-256-GCM and stored on the machine that runs ColabHive.',
  'quickstart.safe_revoke_title': 'Take it back anytime',
  'quickstart.safe_revoke_body': 'Remove an account in Accounts & Share, or revoke access from your Google Account security settings.',
  'quickstart.step': 'Step {n}',
  'quickstart.done': 'Done',
  'quickstart.s1_title': 'Add or Share Colab Accounts',
  'quickstart.s1_body': 'Sign in to your own Google account with OAuth, or send a password-free link so a friend can share idle quota (no Drive access, with a reserve floor).',
  'quickstart.s1_primary': 'Add My Account',
  'quickstart.s1_secondary': 'Invite a Friend',
  'quickstart.s1_demo_badge': '{n} demo accounts shown',
  'quickstart.s1_live_badge': '{n} accounts connected',
  'quickstart.s1_todo_badge': 'No account added yet',
  'quickstart.s2_title': 'Connect Your AI Assistant',
  'quickstart.s2_body': 'Works with Claude Code, Antigravity, Cursor, Codex and more. One click writes the MCP config, so you never hunt for config files.',
  'quickstart.s2_primary': 'Connect AI in 1 Click',
  'quickstart.s2_secondary': 'Routing Rules',
  'quickstart.s2_live_badge': 'AI assistant connected',
  'quickstart.s2_todo_badge': 'Not connected yet',
  'quickstart.s3_title': 'Run Your First GPU Job',
  'quickstart.s3_body': 'Tell your AI "run this code on Colab", or open Experiments for a web terminal with live GPU load and output.',
  'quickstart.s3_primary': 'View Experiments',
  'quickstart.s3_secondary': 'Copy AI Test Prompt',
  'quickstart.s3_copied': 'Prompt copied!',
  'quickstart.s3_demo_badge': '{n} demo GPUs running',
  'quickstart.s3_live_badge': '{n} GPUs running',
  'quickstart.s3_todo_badge': 'No experiment running yet',
  'quickstart.test_prompt': 'Use ColabHive to show the available GPU compute pool, then allocate one idle GPU and run nvidia-smi',

  // Common Actions & Statuses
  'common.save': 'Save Changes',
  'common.cancel': 'Cancel',
  'common.delete': 'Delete',
  'common.copy': 'Copy',
  'common.copied': 'Copied',
  'common.loading': 'Loading the latest data…',
  'common.healthy': 'Available',
  'common.working': 'Running',
  'common.cooldown': 'Cooling Down',
  'common.expired': 'Sign in Again',
  'quick.chat': 'Uncensored LLM Deploy (/v1)',
  'quick.apps': 'WanGP Video & AI App Store',
  'quick.free_first': 'Free-First Toggle',
};

const dictionaries: Record<Locale, Record<string, string>> = {
  'zh-CN': zhCN,
  'en-US': enUS,
};

const LOCALE_STORAGE_KEY = 'ch_locale';
/** v4 key: starts every browser cleanly on the Stardust Warm Light default. */
export const THEME_STORAGE_KEY = 'colabhive_theme_v4';
const PRIVACY_MASK_STORAGE_KEY = 'ch_privacy_mask';

export interface I18nContextValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  toggleLocale: () => void;
  t: (key: TranslationKey | (string & {}), fallback?: string) => string;

  theme: ThemeMode;
  setTheme: (theme: ThemeMode) => void;
  toggleTheme: () => void;

  privacyMask: boolean;
  setPrivacyMask: (masked: boolean) => void;
  togglePrivacyMask: () => void;

  displayEmail: (email: string | undefined | null) => string;
  displayIP: (ip: string | undefined | null) => string;
  displayProxy: (url: string | undefined | null) => string;
  displaySecret: (value: string | undefined | null) => string;
}

const I18nContext = createContext<I18nContextValue | null>(null);

function readStorage(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStorage(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    // Ignore storage write errors
  }
}

/** Saved theme, or Liquid Glass Light when nothing valid is stored. */
function readInitialTheme(): ThemeMode {
  const saved = readStorage(THEME_STORAGE_KEY);
  return saved === 'dark' || saved === 'light' ? saved : 'light';
}

/**
 * Writes the theme onto <html>. `data-theme-ready` tells `index.css` that the
 * static `data-theme="dark"` from `index.html` has been replaced by the real
 * choice, which ends the pre-hydration light canvas.
 */
function applyThemeAttribute(theme: ThemeMode): void {
  if (typeof document === 'undefined') return;
  const root = document.documentElement;
  if (root.getAttribute('data-theme') !== theme) root.setAttribute('data-theme', theme);
  if (!root.hasAttribute('data-theme-ready')) root.setAttribute('data-theme-ready', '');
}

// Runs once at module evaluation, before React mounts: the first React paint is
// already in the right theme.
applyThemeAttribute(readInitialTheme());

export function I18nProvider({ children }: { children: React.ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() => {
    const saved = readStorage(LOCALE_STORAGE_KEY);
    return saved === 'en-US' || saved === 'zh-CN' ? saved : 'zh-CN';
  });

  const [theme, setThemeState] = useState<ThemeMode>(readInitialTheme);

  const [privacyMask, setPrivacyMaskState] = useState<boolean>(() => {
    return readStorage(PRIVACY_MASK_STORAGE_KEY) === 'true';
  });

  useEffect(() => {
    document.documentElement.lang = locale;
    writeStorage(LOCALE_STORAGE_KEY, locale);
  }, [locale]);

  // Layout effect: the attribute lands in the same frame as the re-render.
  useLayoutEffect(() => {
    applyThemeAttribute(theme);
  }, [theme]);

  useEffect(() => {
    applyThemeAttribute(theme);
    writeStorage(THEME_STORAGE_KEY, theme);
  }, [theme]);

  useEffect(() => {
    writeStorage(PRIVACY_MASK_STORAGE_KEY, String(privacyMask));
  }, [privacyMask]);

  const setLocale = useCallback((next: Locale) => setLocaleState(next), []);
  const toggleLocale = useCallback(
    () => setLocaleState((prev) => (prev === 'zh-CN' ? 'en-US' : 'zh-CN')),
    [],
  );

  const setTheme = useCallback((next: ThemeMode) => setThemeState(next), []);
  const toggleTheme = useCallback(
    () => setThemeState((prev) => (prev === 'dark' ? 'light' : 'dark')),
    [],
  );

  const setPrivacyMask = useCallback((masked: boolean) => setPrivacyMaskState(masked), []);
  const togglePrivacyMask = useCallback(() => setPrivacyMaskState((prev) => !prev), []);

  const t = useCallback(
    (key: TranslationKey | (string & {}), fallback?: string): string => {
      return dictionaries[locale][key] ?? dictionaries['en-US'][key] ?? fallback ?? key;
    },
    [locale],
  );

  const displayEmail = useCallback(
    (email: string | undefined | null) => maskEmail(email, privacyMask),
    [privacyMask],
  );

  const displayIP = useCallback(
    (ip: string | undefined | null) => maskIP(ip, privacyMask),
    [privacyMask],
  );

  const displayProxy = useCallback(
    (url: string | undefined | null) => maskProxyUrl(url, privacyMask),
    [privacyMask],
  );

  const displaySecret = useCallback(
    (value: string | undefined | null) => maskSecret(value, privacyMask),
    [privacyMask],
  );

  const value = useMemo<I18nContextValue>(
    () => ({
      locale,
      setLocale,
      toggleLocale,
      t,
      theme,
      setTheme,
      toggleTheme,
      privacyMask,
      setPrivacyMask,
      togglePrivacyMask,
      displayEmail,
      displayIP,
      displayProxy,
      displaySecret,
    }),
    [
      locale,
      setLocale,
      toggleLocale,
      t,
      theme,
      setTheme,
      toggleTheme,
      privacyMask,
      setPrivacyMask,
      togglePrivacyMask,
      displayEmail,
      displayIP,
      displayProxy,
      displaySecret,
    ],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  const ctx = useContext(I18nContext);
  if (!ctx) {
    throw new Error('useI18n must be used inside <I18nProvider>');
  }
  return ctx;
}
