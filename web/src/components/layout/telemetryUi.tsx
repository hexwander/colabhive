import React, { useCallback, useState } from 'react';
import { Check, Copy } from 'lucide-react';
import type {
  AcceleratorType,
  Account,
  AccountHealth,
  AgentAPIKey,
  AgentClientConfigStatus,
  DaemonHealthStatus,
  ProxyConfig,
  QuotaSummary,
  RateCalibrationSource,
  SubscriptionTier,
} from '../../types';
import { DEFAULT_HARDWARE_CATALOG } from '../../types';
import { useI18n } from '../../i18n';
import { StatusPill, type SemanticTone } from '../ui/primitives';

// ============================================================================
// Shared Badges & Formatters (v3 "Liquid Glass")
//
// Emerald / amber / coral are reserved for health semantics. Hardware and
// subscription tiers use muted warm stone, obsidian ink, or sky tags instead.
// Labels are plain words a first-time user understands without a glossary.
// ============================================================================

const TIER_COLOR_MAP: Record<AcceleratorType, string> = {
  G4: 'var(--ch-tier-g4)',
  H100: 'var(--ch-tier-h100)',
  A100: 'var(--ch-tier-a100)',
  L4: 'var(--ch-tier-l4)',
  T4: 'var(--ch-tier-t4)',
  V6E1: 'var(--ch-tier-tpu)',
  V5E1: 'var(--ch-tier-tpu)',
  CPU: 'var(--ch-text-secondary)',
};

export function getAcceleratorColor(acc: AcceleratorType): string {
  return TIER_COLOR_MAP[acc] || 'var(--ch-text-secondary)';
}

export function HardwareTierBadge({
  accelerator,
  label,
  size = 'sm',
}: {
  accelerator: AcceleratorType;
  label?: string;
  size?: 'xs' | 'sm';
}) {
  const color = getAcceleratorColor(accelerator);
  return (
    <span
      className="ch-tier-tag select-none"
      style={{
        color: 'var(--text-primary)',
        fontSize: size === 'xs' ? '10px' : '11px',
        padding: size === 'xs' ? '1px 6px' : '2px 7px',
      }}
    >
      <span
        className="inline-block w-1.5 h-1.5 rounded-full shrink-0"
        style={{ backgroundColor: color }}
      />
      <span>{label ?? accelerator}</span>
    </span>
  );
}

export function SubscriptionTierBadge({ tier }: { tier: SubscriptionTier }) {
  const styles: Record<SubscriptionTier, { text: string; tone: SemanticTone }> = {
    AI_ULTRA: { text: 'AI Ultra', tone: 'accent' },
    PRO_PLUS: { text: 'Colab Pro+', tone: 'emerald' },
    PRO: { text: 'Colab Pro', tone: 'sky' },
    PAYG: { text: 'PAYG', tone: 'neutral' },
    FREE: { text: 'Free', tone: 'neutral' },
  };
  const s = styles[tier] ?? styles.FREE;
  return (
    <StatusPill tone={s.tone} size="xs" className="font-semibold">
      {s.text}
    </StatusPill>
  );
}

export function PlatformProviderBadge({
  account: _account,
  showHours: _showHours = true,
}: {
  account: Account;
  showHours?: boolean;
}) {
  return (
    <StatusPill tone="neutral" size="xs" className="font-semibold">
      Colab
    </StatusPill>
  );
}

export function AccountHealthBadge({
  health,
  compact = false,
}: {
  health: AccountHealth;
  compact?: boolean;
}) {
  const { locale } = useI18n();
  const zh = locale === 'zh-CN';

  // Only an account that is running a GPU right now gets a pulsing dot.
  const map: Record<AccountHealth, { label: string; tone: SemanticTone; pulse?: boolean }> = {
    HEALTHY: { label: zh ? '空闲可用' : 'Available', tone: 'emerald' },
    ACTIVE_WORKING: { label: zh ? '运行中' : 'Running', tone: 'amber', pulse: true },
    MAX_CONCURRENCY: { label: zh ? '满载运行' : 'Full Load', tone: 'accent' },
    COOLDOWN_RATE_LIMIT: { label: zh ? '短暂冷却中' : 'Cooling Down', tone: 'amber' },
    QUOTA_LOW_FREE_ONLY: { label: zh ? '仅免费额度' : 'Free Quota Only', tone: 'neutral' },
    PAUSED_CAP_REACHED: { label: zh ? '已达共享上限' : 'Share Cap Reached', tone: 'accent' },
    PAUSED_SCHEDULE_WINDOW: { label: zh ? '非共享时段' : 'Off Schedule', tone: 'neutral' },
    PAUSED_MANUAL: { label: zh ? '已暂停' : 'Paused', tone: 'neutral' },
    AUTH_EXPIRED: { label: zh ? '登录已过期' : 'Sign-in Expired', tone: 'rose' },
    REVOKED_BY_USER: { label: zh ? '已取消共享' : 'Unshared', tone: 'rose' },
    QUARANTINED_DENYLIST: { label: zh ? '风控保护中' : 'Protected Hold', tone: 'rose' },
    PROXY_ERROR: { label: zh ? '网络代理异常' : 'Proxy Issue', tone: 'rose' },
  };

  const item = map[health] ?? map.HEALTHY;
  return (
    <StatusPill
      tone={item.tone}
      dot
      pulse={item.pulse}
      title={compact ? item.label : undefined}
      className={compact ? 'px-[6px]' : undefined}
    >
      {compact ? null : item.label}
    </StatusPill>
  );
}

export function CalibrationBadge({ source }: { source: RateCalibrationSource }) {
  const { t, locale } = useI18n();
  const zh = locale === 'zh-CN';
  if (source === 'live') {
    return (
      <StatusPill
        tone="emerald"
        size="xs"
        dot
        title={
          zh
            ? '按这个账号最近的真实消耗速度换算，最准确'
            : 'Based on this account’s actual recent usage rate. Most accurate.'
        }
      >
        {t('matrix.calibrated')}
      </StatusPill>
    );
  }
  if (source === 'empirical') {
    return (
      <StatusPill
        tone="accent"
        size="xs"
        dot
        title={zh ? '按所有账号的历史平均消耗速度换算' : 'Based on the historical average across accounts'}
      >
        {t('matrix.empirical')}
      </StatusPill>
    );
  }
  return (
    <StatusPill
      tone="neutral"
      size="xs"
      dot
      title={zh ? '按 Colab 官方公布的标准速度换算' : 'Based on Colab’s published reference rate'}
    >
      {t('matrix.reference')}
    </StatusPill>
  );
}

export function formatRelativeCountdown(isoDate?: string, locale: 'zh-CN' | 'en-US' = 'zh-CN'): string {
  if (!isoDate) return '—';
  const target = Date.parse(isoDate);
  if (Number.isNaN(target)) return '—';
  const diffMs = target - Date.now();
  if (diffMs <= 0) return locale === 'zh-CN' ? '已到期' : 'Expired';
  const totalMin = Math.floor(diffMs / 60000);
  const days = Math.floor(totalMin / 1440);
  const hours = Math.floor((totalMin % 1440) / 60);
  const mins = totalMin % 60;
  if (days > 0) {
    return `${days}d ${String(hours).padStart(2, '0')}h`;
  }
  return `${hours}h ${String(mins).padStart(2, '0')}m`;
}

export function formatUptime(sec?: number): string {
  if (!sec || sec <= 0) return '00:00:00';
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
}

export function CopyButton({
  text,
  label,
  className = '',
}: {
  text: string;
  label?: string;
  className?: string;
}) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(
    async (e: React.MouseEvent) => {
      e.stopPropagation();
      try {
        await navigator.clipboard.writeText(text);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1800);
      } catch {
        // Fallback textarea copy
        const ta = document.createElement('textarea');
        ta.value = text;
        document.body.appendChild(ta);
        ta.select();
        document.execCommand('copy');
        document.body.removeChild(ta);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1800);
      }
    },
    [text],
  );

  return (
    <button
      type="button"
      onClick={handleCopy}
      className={`ui-btn ui-btn-secondary ui-btn-xs ${className}`}
      style={
        copied
          ? { color: 'var(--emerald-text)', borderColor: 'var(--emerald-border)' }
          : undefined
      }
    >
      {copied ? <Check className="w-3 h-3" /> : <Copy className="w-3 h-3" />}
      <span>{copied ? t('common.copied') : (label ?? t('common.copy'))}</span>
    </button>
  );
}

// ============================================================================
// Offline Preview Snapshot Telemetry (Used ONLY if Go daemon is unreachable)
// ============================================================================

const futureIso = (hoursFromNow: number) =>
  new Date(Date.now() + hoursFromNow * 3600 * 1000).toISOString();

export const DEMO_DAEMON_HEALTH: DaemonHealthStatus = {
  healthy: true,
  version: '1.0.0',
  uptime_sec: 86420,
  keepalive_loop_sec: 8,
  resource_poll_sec: 10,
  anti_zombie_armed: true,
  contributor_firewall_on: true,
  active_sessions: 3,
  active_experiments: 1,
  connected_ws_clients: 2,
  timestamp: new Date().toISOString(),
};

export const DEMO_PROXIES: ProxyConfig[] = [
  {
    id: 'prx_us_lax_01',
    name: 'US-LAX Residential #1',
    protocol: 'socks5',
    url: 'socks5://user:pass@38.142.90.14:1080',
    masked_url: 'socks5://38.142.*.*:1080',
    region: 'US-California',
    exit_ip: '38.142.90.14',
    latency_ms: 52,
    healthy: true,
    bound_accounts: 2,
    last_checked_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
  },
  {
    id: 'prx_us_iad_02',
    name: 'US-East Academic Static',
    protocol: 'http',
    url: 'http://proxy:secret@198.51.100.42:8080',
    masked_url: 'http://198.51.*.*:8080',
    region: 'US-Virginia',
    exit_ip: '198.51.100.42',
    latency_ms: 74,
    healthy: true,
    bound_accounts: 2,
    last_checked_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
  },
  {
    id: 'prx_sg_03',
    name: 'SG-ISP Dedicated Node',
    protocol: 'socks5',
    url: 'socks5://lab:key@103.214.18.88:1080',
    masked_url: 'socks5://103.214.*.*:1080',
    region: 'AP-Singapore',
    exit_ip: '103.214.18.88',
    latency_ms: 39,
    healthy: true,
    bound_accounts: 1,
    last_checked_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
  },
];

function buildAccountEquiv(schedulableCu: number, h100Restricted = false) {
  const out: Account['gpu_equivalence'] = {};
  for (const spec of DEFAULT_HARDWARE_CATALOG) {
    const locked = h100Restricted && spec.accelerator === 'H100';
    const rate = spec.default_burn_rate_cu_hr;
    out[spec.accelerator] = {
      accelerator: spec.accelerator,
      display_name: spec.display_name,
      short_label: spec.short_label,
      vram_gb: spec.vram_gb,
      burn_rate_cu_hr: rate,
      calibration_source:
        spec.accelerator === 'G4' || spec.accelerator === 'A100' ? 'live' : 'reference',
      available_hours: locked ? 0 : schedulableCu / rate,
      schedulable_hours: locked ? 0 : schedulableCu / rate,
      eligible_accounts: locked ? 0 : 1,
      max_parallel_slots: locked ? 0 : 2,
      free_windows_ready: spec.free_tier_eligible ? 1 : 0,
      locked,
    };
  }
  return out;
}

export const DEMO_ACCOUNTS: Account[] = [
  {
    id: 'acc_ultra_01',
    email: 'lab-ultra@research.example',
    display_name: 'Primary Research Ultra',
    alias: 'Ultra-Core-01',
    notes: 'Google AI Ultra · Primary 96GB G4 shard node',
    subscription_tier: 'AI_ULTRA',
    health: 'ACTIVE_WORKING',
    enabled: true,
    priority: 95,
    groups: ['research-core', 'high-vram'],
    tags: ['blackwell-96g', 'sticky'],
    proxy_id: 'prx_us_lax_01',
    proxy: DEMO_PROXIES[0],
    max_concurrent_vms: 4,
    active_assignments: 1,
    sticky_agent_count: 1,
    paid_cu_balance: 486.4,
    reference_cycle_cu: 500,
    schedulable_cu: 486.4,
    live_burn_rate_cu_hr: 9.66,
    cycle_resets_at: futureIso(18 * 24 + 4),
    expiring_soon_cu: 0,
    free_quota: {
      remaining_milli_ccu: 15000,
      remaining_free_cu: 15.0,
      next_refill_at: futureIso(6.2),
    },
    gpu_entitlements: {
      G4: { accelerator: 'G4', status: 'AVAILABLE', calibrated_burn_rate: 9.66, calibration_source: 'live' },
      H100: { accelerator: 'H100', status: 'TIER_RESTRICTED', reason: 'HTTP 400 regional stockout · 6h soft cooldown', cooldown_until: futureIso(4.5) },
      A100: { accelerator: 'A100', status: 'AVAILABLE', calibrated_burn_rate: 8.88, calibration_source: 'live' },
      L4: { accelerator: 'L4', status: 'AVAILABLE', calibration_source: 'empirical' },
      T4: { accelerator: 'T4', status: 'AVAILABLE', calibration_source: 'reference' },
      V6E1: { accelerator: 'V6E1', status: 'AVAILABLE', calibration_source: 'reference' },
    },
    gpu_equivalence: buildAccountEquiv(486.4, true),
    public_api_supported: true,
    tunnel_api_supported: true,
    is_contributor: false,
    guardrails: {
      min_reserve_cu: 0,
      donated_cu_used_total: 0,
      donated_cu_used_today: 0,
      allow_paid_cu_burn: true,
      allowed_gpus: ['G4', 'H100', 'A100', 'L4', 'T4', 'V6E1', 'V5E1', 'CPU'],
      max_concurrent_vms: 4,
      block_drive_propagation: false,
      mask_email_in_dashboard: false,
      paused: false,
    },
    active_session_summary: 'exp_qwen27b_s0 · G4 96GB (54.2/96.0 GB · Keepalive 8s ✓)',
    last_polled_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
  {
    id: 'acc_proplus_02',
    email: 'alex.chen@stanford-ai.example',
    display_name: 'Lab Pro+ Node 02',
    alias: 'ProPlus-02',
    notes: 'Expiring PAYG top-up prioritized by Waste-Zero policy',
    subscription_tier: 'PRO_PLUS',
    health: 'ACTIVE_WORKING',
    enabled: true,
    priority: 90,
    groups: ['research-core', 'high-vram'],
    tags: ['expiring-batch'],
    proxy_id: 'prx_us_lax_01',
    proxy: DEMO_PROXIES[0],
    max_concurrent_vms: 3,
    active_assignments: 1,
    sticky_agent_count: 1,
    paid_cu_balance: 412.8,
    reference_cycle_cu: 500,
    schedulable_cu: 412.8,
    live_burn_rate_cu_hr: 9.66,
    cycle_resets_at: futureIso(11 * 24 + 9),
    earliest_batch_expires_at: futureIso(14 * 24 + 6),
    expiring_soon_cu: 118.4,
    gpu_entitlements: {
      G4: { accelerator: 'G4', status: 'AVAILABLE', calibrated_burn_rate: 9.66, calibration_source: 'live' },
      H100: { accelerator: 'H100', status: 'AVAILABLE', calibrated_burn_rate: 12.5, calibration_source: 'empirical' },
      A100: { accelerator: 'A100', status: 'AVAILABLE', calibrated_burn_rate: 8.88, calibration_source: 'live' },
      L4: { accelerator: 'L4', status: 'AVAILABLE' },
      T4: { accelerator: 'T4', status: 'AVAILABLE' },
      V6E1: { accelerator: 'V6E1', status: 'AVAILABLE' },
    },
    gpu_equivalence: buildAccountEquiv(412.8, false),
    public_api_supported: true,
    tunnel_api_supported: true,
    is_contributor: false,
    guardrails: {
      min_reserve_cu: 0,
      donated_cu_used_total: 0,
      donated_cu_used_today: 0,
      allow_paid_cu_burn: true,
      allowed_gpus: ['G4', 'H100', 'A100', 'L4', 'T4', 'V6E1', 'V5E1', 'CPU'],
      max_concurrent_vms: 3,
      block_drive_propagation: false,
      mask_email_in_dashboard: false,
      paused: false,
    },
    active_session_summary: 'exp_qwen27b_s1 · G4 96GB (51.8/96.0 GB · Keepalive 8s ✓)',
    last_polled_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
  {
    id: 'acc_contrib_03',
    email: 'maya.lin@mit-vision.example',
    display_name: 'Maya Lin (Contributed Pro+)',
    alias: 'Contrib-Maya',
    notes: 'External contributor · 60 CU personal safety floor enforced',
    subscription_tier: 'PRO_PLUS',
    health: 'ACTIVE_WORKING',
    enabled: true,
    priority: 75,
    groups: ['lab-contributors'],
    tags: ['contributed', 'firewall-on'],
    proxy_id: 'prx_us_iad_02',
    proxy: DEMO_PROXIES[1],
    max_concurrent_vms: 2,
    active_assignments: 1,
    sticky_agent_count: 0,
    paid_cu_balance: 342.0,
    reference_cycle_cu: 500,
    schedulable_cu: 282.0,
    live_burn_rate_cu_hr: 8.88,
    cycle_resets_at: futureIso(22 * 24),
    expiring_soon_cu: 0,
    gpu_entitlements: {
      G4: { accelerator: 'G4', status: 'AVAILABLE' },
      H100: { accelerator: 'H100', status: 'INELIGIBLE', reason: 'Disabled by contributor guardrail' },
      A100: { accelerator: 'A100', status: 'AVAILABLE', calibrated_burn_rate: 8.88, calibration_source: 'live' },
      L4: { accelerator: 'L4', status: 'AVAILABLE' },
      T4: { accelerator: 'T4', status: 'AVAILABLE' },
    },
    gpu_equivalence: buildAccountEquiv(282.0, true),
    public_api_supported: false,
    tunnel_api_supported: true,
    is_contributor: false,
    invite_code: 'ch_inv_icml2026',
    guardrails: {
      min_reserve_cu: 60,
      max_donated_cu_total: 250,
      max_donated_cu_daily: 45,
      donated_cu_used_total: 38.4,
      donated_cu_used_today: 11.2,
      allow_paid_cu_burn: true,
      allowed_gpus: ['G4', 'A100', 'L4', 'T4'],
      max_concurrent_vms: 2,
      block_drive_propagation: true,
      mask_email_in_dashboard: false,
      paused: false,
      receipt_token: 'ch_rcpt_demo_alice99',
    },
    active_session_summary: 'claude_eval_a100 · A100 40GB (28.4/40.0 GB · Firewall ✓)',
    last_polled_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
  {
    id: 'acc_pro_04',
    email: 'dev-pro@robotics.example',
    display_name: 'Eval Pro Standby',
    alias: 'Pro-Standby-04',
    subscription_tier: 'PRO',
    health: 'HEALTHY',
    enabled: true,
    priority: 70,
    groups: ['research-core', 'sandbox'],
    tags: ['l4-lora'],
    proxy_id: 'prx_sg_03',
    proxy: DEMO_PROXIES[2],
    max_concurrent_vms: 2,
    active_assignments: 0,
    sticky_agent_count: 0,
    paid_cu_balance: 187.22,
    reference_cycle_cu: 200,
    schedulable_cu: 187.22,
    live_burn_rate_cu_hr: 0,
    cycle_resets_at: futureIso(25 * 24 + 2),
    expiring_soon_cu: 0,
    free_quota: {
      remaining_milli_ccu: 14500,
      remaining_free_cu: 14.5,
      next_refill_at: futureIso(3.2),
    },
    gpu_entitlements: {
      G4: { accelerator: 'G4', status: 'AVAILABLE' },
      H100: { accelerator: 'H100', status: 'TIER_RESTRICTED', reason: 'Colab Pro+ / Ultra required' },
      A100: { accelerator: 'A100', status: 'AVAILABLE' },
      L4: { accelerator: 'L4', status: 'AVAILABLE' },
      T4: { accelerator: 'T4', status: 'AVAILABLE' },
      V5E1: { accelerator: 'V5E1', status: 'AVAILABLE' },
    },
    gpu_equivalence: buildAccountEquiv(187.22, true),
    public_api_supported: true,
    tunnel_api_supported: true,
    is_contributor: false,
    guardrails: {
      min_reserve_cu: 0,
      donated_cu_used_total: 0,
      donated_cu_used_today: 0,
      allow_paid_cu_burn: true,
      allowed_gpus: ['G4', 'A100', 'L4', 'T4', 'V5E1', 'CPU'],
      max_concurrent_vms: 2,
      block_drive_propagation: false,
      mask_email_in_dashboard: false,
      paused: false,
    },
    last_polled_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  },
];

export const DEMO_QUOTA_SUMMARY: QuotaSummary = {
  accounts_total: 4,
  accounts_healthy: 4,
  accounts_working: 3,
  accounts_contributor: 1,
  total_available_cu: 1480.42,
  schedulable_cu: 1420.42,
  estimated_value_usd: 148.04,
  active_burn_rate_cu_hr: 28.2,
  pool_depletion_eta_hours: 50.37,
  earliest_batch_expiry_cu: 118.4,
  earliest_batch_expires_at: futureIso(14 * 24 + 6),
  active_vms: 3,
  total_vm_slots: 13,
  total_online_vram_gb: 232,
  free_t4_windows_ready: 3,
  saved_idle_waste_cu: 312.6,
  auto_teardowns_count: 19,
  zombie_aborts_count: 4,
  gpu_equivalence: {
    G4: {
      accelerator: 'G4',
      display_name: 'NVIDIA RTX PRO 6000 Blackwell',
      short_label: 'G4 96GB',
      vram_gb: 96,
      burn_rate_cu_hr: 9.66,
      calibration_source: 'live',
      available_hours: 147.87,
      schedulable_hours: 141.66,
      eligible_accounts: 4,
      max_parallel_slots: 11,
      free_windows_ready: 0,
      locked: false,
    },
    H100: {
      accelerator: 'H100',
      display_name: 'NVIDIA H100 SXM5',
      short_label: 'H100 80GB',
      vram_gb: 80,
      burn_rate_cu_hr: 12.5,
      calibration_source: 'empirical',
      available_hours: 33.02,
      schedulable_hours: 33.02,
      eligible_accounts: 1,
      max_parallel_slots: 3,
      free_windows_ready: 0,
      locked: false,
    },
    A100: {
      accelerator: 'A100',
      display_name: 'NVIDIA A100 SXM4',
      short_label: 'A100 40/80GB',
      vram_gb: 40,
      burn_rate_cu_hr: 8.88,
      calibration_source: 'live',
      available_hours: 160.86,
      schedulable_hours: 154.1,
      eligible_accounts: 4,
      max_parallel_slots: 11,
      free_windows_ready: 0,
      locked: false,
    },
    L4: {
      accelerator: 'L4',
      display_name: 'NVIDIA L4',
      short_label: 'L4 24GB',
      vram_gb: 24,
      burn_rate_cu_hr: 4.52,
      calibration_source: 'empirical',
      available_hours: 316.02,
      schedulable_hours: 302.75,
      eligible_accounts: 4,
      max_parallel_slots: 11,
      free_windows_ready: 0,
      locked: false,
    },
    T4: {
      accelerator: 'T4',
      display_name: 'NVIDIA Tesla T4',
      short_label: 'T4 16GB',
      vram_gb: 16,
      burn_rate_cu_hr: 1.96,
      calibration_source: 'reference',
      available_hours: 728.79,
      schedulable_hours: 698.17,
      eligible_accounts: 4,
      max_parallel_slots: 11,
      free_windows_ready: 2,
      locked: false,
    },
    V6E1: {
      accelerator: 'V6E1',
      display_name: 'Cloud TPU v6e-1 Trillium',
      short_label: 'TPU v6e-1 32GB',
      vram_gb: 32,
      burn_rate_cu_hr: 5.3,
      calibration_source: 'reference',
      available_hours: 269.51,
      schedulable_hours: 258.19,
      eligible_accounts: 3,
      max_parallel_slots: 9,
      free_windows_ready: 0,
      locked: false,
    },
    V5E1: {
      accelerator: 'V5E1',
      display_name: 'Cloud TPU v5e-1',
      short_label: 'TPU v5e-1 16GB',
      vram_gb: 16,
      burn_rate_cu_hr: 3.85,
      calibration_source: 'reference',
      available_hours: 371.02,
      schedulable_hours: 355.43,
      eligible_accounts: 4,
      max_parallel_slots: 11,
      free_windows_ready: 0,
      locked: false,
    },
    CPU: {
      accelerator: 'CPU',
      display_name: 'Cloud x86_64 CPU VM',
      short_label: 'CPU VM',
      vram_gb: 0,
      burn_rate_cu_hr: 0.74,
      calibration_source: 'reference',
      available_hours: 1930.3,
      schedulable_hours: 1849.22,
      eligible_accounts: 4,
      max_parallel_slots: 11,
      free_windows_ready: 2,
      locked: false,
    },
  },
  updated_at: new Date().toISOString(),
};

export const DEMO_API_KEYS: AgentAPIKey[] = [
  {
    id: 'key_prod_agy',
    name: 'Antigravity Flagship Swarm',
    key_prefix: 'ch_sk_live_9f8a4c',
    allowed_groups: ['research-core', 'high-vram', 'lab-contributors'],
    allowed_gpus: ['G4', 'H100', 'A100', 'L4', 'T4', 'V6E1'],
    default_strategy: 'waste-zero',
    max_budget_cu_total: 400,
    max_budget_cu_daily: 80,
    used_cu_total: 124.8,
    used_cu_today: 26.4,
    max_concurrent_vms: 6,
    idle_auto_kill_min: 20,
    enabled: true,
    last_used_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
  },
  {
    id: 'key_dev_claude',
    name: 'Claude Code Sandbox Eval',
    key_prefix: 'ch_sk_live_3d1b7e',
    allowed_groups: ['sandbox', 'lab-contributors'],
    allowed_gpus: ['A100', 'L4', 'T4', 'CPU'],
    default_strategy: 'sticky-session',
    max_budget_cu_total: 100,
    max_budget_cu_daily: 25,
    used_cu_total: 19.2,
    used_cu_today: 4.8,
    max_concurrent_vms: 2,
    idle_auto_kill_min: 15,
    enabled: true,
    last_used_at: new Date().toISOString(),
    created_at: new Date().toISOString(),
  },
];

/**
 * Strips known UI placeholder/fake/masked key strings so broken `ch_sk_...`
 * tokens are never injected into local MCP client configs (which would trigger
 * HTTP 401 on `/mcp`). Empty string means keyless localhost open access.
 */
export function sanitizeAgentApiKey(raw?: string | null): string {
  const val = (raw ?? '').trim();
  if (!val) return '';
  if (
    val === 'ch_sk_live_default' ||
    val === 'ch_sk_live_demo' ||
    val.includes('...') ||
    val.includes('…') ||
    val.includes('xxxx') ||
    val.includes('••••') ||
    val.endsWith('_live_token') ||
    val.endsWith('_minted')
  ) {
    return '';
  }
  return val;
}

export function buildFallbackClientConfigs(
  baseUrl: string,
  apiKey = '',
  transport: 'http' | 'stdio' = 'http',
): AgentClientConfigStatus[] {
  const cleanBase = baseUrl.replace(/\/$/, '') || 'http://127.0.0.1:7860';
  const mcpUrl = `${cleanBase}/mcp`;
  const cleanKey = sanitizeAgentApiKey(apiKey);
  const authHeaders = cleanKey ? { Authorization: `Bearer ${cleanKey}` } : undefined;

  const stdioEnv: Record<string, string> = {
    COLABHIVE_URL: cleanBase,
  };
  if (cleanKey) {
    stdioEnv.COLABHIVE_API_KEY = cleanKey;
  }

  const stdioJson = JSON.stringify(
    {
      mcpServers: {
        colabhive: {
          command: 'colabhive',
          args: ['mcp', 'stdio'],
          env: stdioEnv,
        },
      },
    },
    null,
    2,
  );

  const claudeCodeStdioJson = JSON.stringify(
    {
      mcpServers: {
        colabhive: {
          type: 'stdio',
          command: 'colabhive',
          args: ['mcp', 'stdio'],
          env: stdioEnv,
        },
      },
    },
    null,
    2,
  );

  return [
    {
      client_id: 'antigravity',
      display_name: 'Antigravity',
      config_path: '~/.gemini/config/mcp_config.json',
      wsl_config_path: '\\\\wsl.localhost\\Ubuntu\\home\\user\\.gemini\\config\\mcp_config.json',
      format: 'json',
      transport,
      detected: true,
      injected: false,
      backup_exists: true,
      backup_path: '~/.colabhive/backups/first-write/antigravity.mcp_config.json.bak',
      hot_reload_note: 'MCP Streamable HTTP (2025-03-26) · Auto-discovered on MCP refresh',
      snippet:
        transport === 'stdio'
          ? stdioJson
          : JSON.stringify(
              {
                mcpServers: {
                  colabhive: {
                    url: mcpUrl,
                    ...(authHeaders ? { headers: authHeaders } : {}),
                  },
                },
              },
              null,
              2,
            ),
    },
    {
      client_id: 'claude-code',
      display_name: 'Claude Code',
      config_path: '~/.claude.json',
      wsl_config_path: '\\\\wsl.localhost\\Ubuntu\\home\\user\\.claude.json',
      format: 'json',
      transport,
      detected: true,
      injected: false,
      backup_exists: true,
      backup_path: '~/.colabhive/backups/first-write/claude-code.json.bak',
      hot_reload_note: 'Immediate — verify via `claude mcp list`',
      cli_command: cleanKey
        ? `claude mcp add --transport http colabhive ${mcpUrl} --header "Authorization: Bearer ${cleanKey}"`
        : `claude mcp add --transport http colabhive ${mcpUrl}`,
      snippet:
        transport === 'stdio'
          ? claudeCodeStdioJson
          : JSON.stringify(
              {
                mcpServers: {
                  colabhive: {
                    type: 'http',
                    url: mcpUrl,
                    ...(authHeaders ? { headers: authHeaders } : {}),
                  },
                },
              },
              null,
              2,
            ),
    },
    {
      client_id: 'claude-desktop',
      display_name: 'Claude Desktop',
      config_path: '%APPDATA%\\Claude\\claude_desktop_config.json',
      format: 'json',
      transport: 'stdio',
      detected: true,
      injected: false,
      backup_exists: false,
      hot_reload_note: 'Restart Claude Desktop after injection to reload stdio servers',
      snippet: stdioJson,
    },
    {
      client_id: 'codex',
      display_name: 'OpenAI Codex',
      config_path: '~/.codex/config.toml',
      wsl_config_path: '\\\\wsl.localhost\\Ubuntu\\home\\user\\.codex\\config.toml',
      format: 'toml',
      transport,
      detected: true,
      injected: false,
      backup_exists: true,
      backup_path: '~/.colabhive/backups/first-write/codex.config.toml.bak',
      hot_reload_note: 'Preserves existing TOML tables via surgical key edit',
      snippet:
        transport === 'stdio'
          ? `[mcp_servers.colabhive]\ncommand = "colabhive"\nargs = ["mcp", "stdio"]\ntool_timeout_sec = 1800\n\n[mcp_servers.colabhive.env]\nCOLABHIVE_URL = "${cleanBase}"${
              cleanKey ? `\nCOLABHIVE_API_KEY = "${cleanKey}"` : ''
            }`
          : `[mcp_servers.colabhive]\nurl = "${mcpUrl}"\ntool_timeout_sec = 1800${
              cleanKey ? `\nhttp_headers = { Authorization = "Bearer ${cleanKey}" }` : ''
            }`,
    },
    {
      client_id: 'gemini-cli',
      display_name: 'Gemini CLI',
      config_path: '~/.gemini/settings.json',
      wsl_config_path: '\\\\wsl.localhost\\Ubuntu\\home\\user\\.gemini\\settings.json',
      format: 'json',
      transport,
      detected: true,
      injected: false,
      backup_exists: false,
      hot_reload_note: 'Restart `gemini` CLI session to attach ColabHive tools',
      snippet:
        transport === 'stdio'
          ? stdioJson
          : JSON.stringify(
              {
                mcpServers: {
                  colabhive: {
                    httpUrl: mcpUrl,
                    ...(authHeaders ? { headers: authHeaders } : {}),
                    timeout: 1800000,
                  },
                },
              },
              null,
              2,
            ),
    },
    {
      client_id: 'cursor',
      display_name: 'Cursor',
      config_path: '~/.cursor/mcp.json',
      format: 'json',
      transport,
      detected: true,
      injected: false,
      backup_exists: false,
      hot_reload_note: 'Reloads automatically in Cursor Settings -> MCP',
      snippet:
        transport === 'stdio'
          ? stdioJson
          : JSON.stringify(
              {
                mcpServers: {
                  colabhive: {
                    url: mcpUrl,
                    ...(authHeaders ? { headers: authHeaders } : {}),
                  },
                },
              },
              null,
              2,
            ),
    },
    {
      client_id: 'windsurf',
      display_name: 'Windsurf',
      config_path: '~/.codeium/windsurf/mcp_config.json',
      format: 'json',
      transport,
      detected: false,
      injected: false,
      backup_exists: false,
      hot_reload_note: 'Click Refresh in Windsurf Cascade MCP panel',
      snippet:
        transport === 'stdio'
          ? stdioJson
          : JSON.stringify(
              {
                mcpServers: {
                  colabhive: {
                    serverUrl: mcpUrl,
                    ...(authHeaders ? { headers: authHeaders } : {}),
                  },
                },
              },
              null,
              2,
            ),
    },
    {
      client_id: 'opencode',
      display_name: 'OpenCode',
      config_path: '~/.config/opencode/opencode.json',
      wsl_config_path: '\\\\wsl.localhost\\Ubuntu\\home\\user\\.config\\opencode\\opencode.json',
      format: 'jsonc',
      transport,
      detected: false,
      injected: false,
      backup_exists: false,
      hot_reload_note: 'JSON/JSONC config merge preserving existing providers & MCP servers',
      snippet:
        transport === 'stdio'
          ? `{\n  // ColabHive multi-account GPU pool\n  "mcp": {\n    "colabhive": {\n      "type": "local",\n      "command": ["colabhive", "mcp", "stdio"],${
              cleanKey ? `\n      "environment": { "COLABHIVE_API_KEY": "${cleanKey}" },` : ''
            }\n      "enabled": true\n    }\n  }\n}`
          : `{\n  // ColabHive multi-account GPU pool\n  "mcp": {\n    "colabhive": {\n      "type": "remote",\n      "url": "${mcpUrl}",${
              cleanKey ? `\n      "headers": { "Authorization": "Bearer ${cleanKey}" },` : ''
            }\n      "enabled": true\n    }\n  }\n}`,
    },
  ];
}
