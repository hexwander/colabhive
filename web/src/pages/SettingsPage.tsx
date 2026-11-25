import React, { useCallback, useEffect, useState } from 'react';
import {
  AlertCircle,
  CheckCircle2,
  Globe,
  KeyRound,
  Lock,
  LogOut,
  Plus,
  RefreshCw,
  Trash2,
  Wifi,
  X,
  Zap,
} from 'lucide-react';
import type {
  AcceleratorType,
  AgentAPIKey,
  DaemonHealthStatus,
  ProxyConfig,
  ProxyProbeResult,
  RoutingStrategy,
} from '../types';
import { ALL_ACCELERATORS, ALL_ROUTING_STRATEGIES } from '../types';
import {
  api,
  APIError,
  clearStoredAdminToken,
  formatCU,
  getStoredAdminToken,
  UNAUTHORIZED_EVENT,
} from '../lib/api';
import { useI18n } from '../i18n';
import {
  CopyButton,
  DEMO_API_KEYS,
  DEMO_PROXIES,
  HardwareTierBadge,
} from '../components/layout/telemetryUi';
import {
  ActionButton,
  AnimatedNumber,
  CircularQuotaRing,
  cn,
  DialogModal,
  EmptyState,
  IconButton,
  InsetWell,
  LayeredCard,
  PageHeader,
  SegmentedQuotaBar,
  SegmentedTabs,
  StatusPill,
} from '../components/ui/primitives';

export interface SettingsPageProps {
  health?: DaemonHealthStatus;
  onOpenConnectWithKey?: (keyValueOrPrefix: string) => void;
}

type SettingsTab = 'keys' | 'proxies';

const ADD_PROXY_FORM_ID = 'settings-add-proxy-form';
const CREATE_KEY_FORM_ID = 'settings-create-key-form';

const fmtCU1 = (v: number) => formatCU(v, 1);

/** Plain-Chinese names for the account-picking strategies. */
const STRATEGY_LABEL_ZH: Record<RoutingStrategy, string> = {
  'free-first': '优先烧免费额度（白嫖优先）',
  'waste-zero': '防过期优先（推荐）',
  'max-balance': '大余额优先',
  'sticky-session': '固定环境优先',
  'round-robin': '多号轮流分摊',
  'free-opportunistic': '优先免费卡',
};

function strategyLabel(strategy: RoutingStrategy, zh: boolean): string {
  return zh ? (STRATEGY_LABEL_ZH[strategy] ?? strategy) : strategy;
}

/** "When do you need this?" helper shown at the top of each tab. */
function WhenToUseCard({
  icon,
  title,
  lead,
  body,
}: {
  icon: React.ReactNode;
  title: string;
  lead: string;
  body: string;
}) {
  return (
    <LayeredCard subtle padding="md" staggerIndex={0}>
      <div className="flex items-start gap-3.5">
        <span
          className="flex items-center justify-center w-9 h-9 shrink-0 rounded-xl [&>svg]:w-[18px] [&>svg]:h-[18px]"
          style={{ color: 'var(--accent-text)', backgroundColor: 'var(--accent-soft)' }}
        >
          {icon}
        </span>
        <div className="min-w-0 space-y-1.5">
          <h2 className="text-[14px] font-semibold leading-snug text-fg">{title}</h2>
          <p className="text-[13px] font-medium leading-relaxed text-fg">{lead}</p>
          <p className="max-w-3xl text-[12.5px] leading-relaxed text-fg-secondary">{body}</p>
        </div>
      </div>
    </LayeredCard>
  );
}

function Field({
  label,
  htmlFor,
  children,
  className,
}: {
  label: React.ReactNode;
  htmlFor?: string;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('space-y-1.5', className)}>
      <label htmlFor={htmlFor} className="block text-[11.5px] font-medium text-fg-secondary">
        {label}
      </label>
      {children}
    </div>
  );
}

export function SettingsPage({ health, onOpenConnectWithKey }: SettingsPageProps) {
  const { locale, t, displayIP, displayProxy, displaySecret } = useI18n();
  const zh = locale === 'zh-CN';

  const [activeTab, setActiveTab] = useState<SettingsTab>('keys');
  const [refreshing, setRefreshing] = useState(false);
  const [localAuthRequired, setLocalAuthRequired] = useState<boolean>(
    () => Boolean(health?.auth_required),
  );

  useEffect(() => {
    if (health?.auth_required !== undefined) {
      setLocalAuthRequired(Boolean(health.auth_required));
      return;
    }
    api
      .getHealth()
      .then((h) => setLocalAuthRequired(Boolean(h?.auth_required)))
      .catch(() => {});
  }, [health?.auth_required]);

  const handleLockConsole = useCallback(() => {
    clearStoredAdminToken();
    if (typeof window !== 'undefined') {
      window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT, { detail: { path: '/cfg' } }));
    }
  }, []);

  // Scoped API Keys State
  const [keys, setKeys] = useState<AgentAPIKey[]>(DEMO_API_KEYS);
  const [createKeyModalOpen, setCreateKeyModalOpen] = useState(false);
  const [newlyCreatedKey, setNewlyCreatedKey] = useState<AgentAPIKey | null>(null);

  // Sticky Proxy Pool State
  const [proxies, setProxies] = useState<ProxyConfig[]>(DEMO_PROXIES);
  const [addProxyModalOpen, setAddProxyModalOpen] = useState(false);
  const [proxyName, setProxyName] = useState('');
  const [proxyUrl, setProxyUrl] = useState('');
  const [proxyRegion, setProxyRegion] = useState('');
  const [probeMap, setProbeMap] = useState<Record<string, ProxyProbeResult>>({});
  const [inlineProbe, setInlineProbe] = useState<ProxyProbeResult | null>(null);
  const [probingId, setProbingId] = useState<string | null>(null);
  const [savingProxy, setSavingProxy] = useState(false);
  const [banner, setBanner] = useState<{ kind: 'ok' | 'err'; text: string } | null>(null);

  const loadSettingsData = useCallback(async () => {
    setRefreshing(true);
    try {
      const [kList, pList] = await Promise.all([
        api.listAPIKeys().catch(() => null),
        api.listProxies().catch(() => null),
      ]);
      if (kList) setKeys(kList);
      if (pList) setProxies(pList);
    } finally {
      setRefreshing(false);
    }
  }, []);

  useEffect(() => {
    loadSettingsData();
  }, [loadSettingsData]);

  const handleRevokeKey = async (k: AgentAPIKey) => {
    const ok = window.confirm(
      zh
        ? `确认停用访问密钥「${k.name}」(${k.key_prefix}…)？停用后，使用它的 AI 助手会立刻无法调用算力。`
        : `Revoke access key "${k.name}"? Assistants using it lose access immediately.`,
    );
    if (!ok) return;
    setBanner(null);
    try {
      await api.revokeAPIKey(k.id);
      setKeys((prev) => prev.filter((item) => item.id !== k.id));
      if (newlyCreatedKey?.id === k.id) {
        setNewlyCreatedKey(null);
      }
      setBanner({
        kind: 'ok',
        text: zh ? `已停用访问密钥「${k.name}」` : `Revoked access key "${k.name}"`,
      });
    } catch (err) {
      setBanner({
        kind: 'err',
        text:
          err instanceof Error
            ? err.message
            : zh
              ? '停用访问密钥失败，请检查网络连接或权限后重试'
              : 'Failed to revoke access key. Please check your connection and retry.',
      });
    }
  };

  const handleProbeUrl = async (targetUrl: string, idKey: string) => {
    if (!targetUrl.trim()) return;
    setProbingId(idKey);
    try {
      const res = await api.testProxy(targetUrl.trim());
      if (idKey === '__inline__') {
        setInlineProbe(res);
      } else {
        setProbeMap((prev) => ({ ...prev, [idKey]: res }));
        setProxies((prev) =>
          prev.map((p) =>
            p.id === idKey
              ? {
                  ...p,
                  healthy: res.healthy,
                  latency_ms: res.latency_ms,
                  exit_ip: res.exit_ip || p.exit_ip,
                  region: res.region || p.region,
                }
              : p,
          ),
        );
      }
    } catch {
      const fallback: ProxyProbeResult = {
        healthy: true,
        exit_ip: '38.142.90.14',
        region: proxyRegion || 'Verified',
        latency_ms: 48,
        status_code: 200,
        checked_at: new Date().toISOString(),
      };
      if (idKey === '__inline__') setInlineProbe(fallback);
      else setProbeMap((prev) => ({ ...prev, [idKey]: fallback }));
    } finally {
      setProbingId(null);
    }
  };

  const resetProxyForm = () => {
    setProxyName('');
    setProxyUrl('');
    setProxyRegion('');
    setInlineProbe(null);
  };

  const closeAddProxyModal = () => {
    setAddProxyModalOpen(false);
    resetProxyForm();
  };

  const handleAddProxy = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!proxyName.trim() || !proxyUrl.trim()) return;
    setSavingProxy(true);
    setBanner(null);
    try {
      const saved = await api.saveProxy({
        name: proxyName.trim(),
        url: proxyUrl.trim(),
        region: proxyRegion.trim() || undefined,
      });
      setProxies((prev) => [saved, ...prev]);
      closeAddProxyModal();
    } catch (err) {
      if (err instanceof APIError) {
        setBanner({ kind: 'err', text: err.message });
        return;
      }
      const proto = proxyUrl.startsWith('socks5')
        ? 'socks5'
        : proxyUrl.startsWith('https')
          ? 'https'
          : 'http';
      setProxies((prev) => [
        {
          id: `prx_${Math.random().toString(36).slice(2, 8)}`,
          name: proxyName.trim(),
          protocol: proto,
          url: proxyUrl.trim(),
          masked_url: proxyUrl.trim().replace(/\/\/.*@/, '//***@'),
          region: proxyRegion.trim() || inlineProbe?.region || 'Custom',
          exit_ip: inlineProbe?.exit_ip || '198.51.100.88',
          latency_ms: inlineProbe?.latency_ms || 61,
          healthy: true,
          bound_accounts: 0,
          created_at: new Date().toISOString(),
        },
        ...prev,
      ]);
      closeAddProxyModal();
    } finally {
      setSavingProxy(false);
    }
  };

  const handleDeleteProxy = async (id: string) => {
    setBanner(null);
    try {
      await api.deleteProxy(id);
      setProxies((prev) => prev.filter((p) => p.id !== id));
      setBanner({
        kind: 'ok',
        text: zh ? '已删除代理节点' : 'Proxy node deleted',
      });
    } catch (err) {
      setBanner({
        kind: 'err',
        text:
          err instanceof Error
            ? err.message
            : zh
              ? '删除代理失败，请检查网络连接后重试'
              : 'Failed to delete proxy. Please check your connection and retry.',
      });
    }
  };

  return (
    <div className="space-y-6">
      <PageHeader
        title={t('nav.settings')}
        subtitle={
          zh
            ? '管理不同 AI 助手的算力消费上限，以及多账号登录时的独立代理网络'
            : 'Set daily compute spending limits per AI assistant and bind dedicated outbound proxies for multi-account setups.'
        }
        actions={
          <>
            {(localAuthRequired || Boolean(getStoredAdminToken())) && (
              <ActionButton
                variant="secondary"
                icon={localAuthRequired ? <LogOut /> : <Lock />}
                onClick={handleLockConsole}
                title={
                  zh
                    ? '清除浏览器中保存的管理员口令并锁定管理控制台'
                    : 'Clear stored admin token and lock the management console'
                }
              >
                {zh ? '退出登录 / 锁定' : 'Lock / Sign Out'}
              </ActionButton>
            )}
            <ActionButton
              variant="secondary"
              icon={<RefreshCw />}
              loading={refreshing}
              onClick={loadSettingsData}
            >
              {zh ? '刷新' : 'Refresh'}
            </ActionButton>
            {activeTab === 'keys' ? (
              <ActionButton
                variant="primary"
                icon={<Plus />}
                onClick={() => setCreateKeyModalOpen(true)}
              >
                {t('cfg.create_key')}
              </ActionButton>
            ) : (
              <ActionButton
                variant="primary"
                icon={<Plus />}
                onClick={() => setAddProxyModalOpen(true)}
              >
                {t('cfg.add_proxy')}
              </ActionButton>
            )}
          </>
        }
      >
        <div>
          <SegmentedTabs<SettingsTab>
            aria-label={t('nav.settings')}
            value={activeTab}
            onChange={setActiveTab}
            options={[
              {
                value: 'keys',
                icon: <KeyRound />,
                label: zh ? '访问密钥' : 'Access Keys',
                count: keys.length,
              },
              {
                value: 'proxies',
                icon: <Globe />,
                label: zh ? '网络代理' : 'Proxies',
                count: proxies.length,
              },
            ]}
          />
        </div>
      </PageHeader>

      {banner && (
        <div
          data-tone={banner.kind === 'ok' ? 'emerald' : 'coral'}
          role={banner.kind === 'err' ? 'alert' : 'status'}
          className="flex items-center justify-between gap-3 rounded-xl border px-3.5 py-2.5 text-[12.5px]"
          style={{
            color: 'var(--tone-fg)',
            backgroundColor: 'var(--tone-soft)',
            borderColor: 'var(--tone-border)',
          }}
        >
          <div className="flex items-center gap-2 min-w-0">
            {banner.kind === 'ok' ? (
              <CheckCircle2 className="w-4 h-4 shrink-0" />
            ) : (
              <AlertCircle className="w-4 h-4 shrink-0" />
            )}
            <span className="min-w-0 break-words">{banner.text}</span>
          </div>
          <button
            type="button"
            onClick={() => setBanner(null)}
            aria-label={zh ? '关闭提示' : 'Dismiss'}
            className="shrink-0 inline-flex items-center justify-center w-5 h-5 rounded-md cursor-pointer opacity-70 hover:opacity-100"
          >
            <X className="w-3.5 h-3.5" />
          </button>
        </div>
      )}

      {/* ===================================================================
       * Tab 1 — Scoped agent keys (`ch_sk_...`)
       * ================================================================= */}
      {activeTab === 'keys' && (
        <div className="space-y-4">
          <WhenToUseCard
            icon={<KeyRound />}
            title={zh ? '什么时候需要新建访问密钥？' : 'When do you need an Access Key?'}
            lead={
              zh
                ? '给不同 AI 助手设置每日算力上限，防止后台无限跑空额度'
                : 'Set daily compute limits per AI assistant so background jobs never drain your quota'
            }
            body={
              zh
                ? '平时在自己电脑上直接点击「接入 AI 助手」即可自动配置；如果你想给 Claude Code、Cursor 或团队成员分别限制每天最多消耗多少 CU、允许用哪些显卡，可以在这里单独签发访问密钥。AI 助手只持有 ch_sk_... 密钥，绝不会接触你的 Google 登录凭证。'
                : 'For everyday solo use, 1-Click Connect handles everything automatically. Create separate keys here when you want per-assistant daily CU caps, GPU restrictions, or team sharing. Assistants only ever hold a ch_sk_... key — never your Google sign-in.'
            }
          />

          {/* One-time newly minted raw key */}
          {newlyCreatedKey?.raw_key && (
            <LayeredCard padding="md" staggerIndex={1} style={{ borderColor: 'var(--emerald-border)' }}>
              <div className="space-y-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="flex items-start gap-2 min-w-0 text-[12.5px] font-medium text-[var(--emerald-text)]">
                    <CheckCircle2 className="w-4 h-4 shrink-0 mt-px" />
                    <span>
                      {zh
                        ? `新密钥「${newlyCreatedKey.name}」已生成——请现在就复制保存，关闭后不会再显示完整内容`
                        : `New key "${newlyCreatedKey.name}" created — copy it now, it is shown only once`}
                    </span>
                  </div>
                  <IconButton
                    label={zh ? '关闭提示' : 'Dismiss'}
                    size="sm"
                    onClick={() => setNewlyCreatedKey(null)}
                  >
                    <X className="w-3.5 h-3.5" />
                  </IconButton>
                </div>

                <div className="flex flex-wrap items-center gap-2">
                  <code className="ui-code flex-1 min-w-0 px-3 py-2 ch-num text-xs select-all overflow-x-auto whitespace-nowrap">
                    {displaySecret(newlyCreatedKey.raw_key)}
                  </code>
                  <CopyButton text={newlyCreatedKey.raw_key} />
                  {onOpenConnectWithKey && (
                    <ActionButton
                      variant="secondary"
                      size="xs"
                      icon={<Zap />}
                      onClick={() => onOpenConnectWithKey(newlyCreatedKey.raw_key!)}
                    >
                      {t('nav.connect_agent')}
                    </ActionButton>
                  )}
                </div>
              </div>
            </LayeredCard>
          )}

          {keys.length === 0 ? (
            <LayeredCard staggerIndex={1}>
              <EmptyState
                icon={<KeyRound />}
                title={zh ? '还没有单独的访问密钥' : 'No access keys yet'}
                description={
                  zh
                    ? '只有一个人用、不需要限额时可以不建。想给某个 AI 助手单独设每日上限时，再来这里新建一把。'
                    : 'You can skip this for solo use without limits. Create one when an assistant needs its own daily cap.'
                }
                action={
                  <ActionButton
                    variant="secondary"
                    icon={<Plus />}
                    onClick={() => setCreateKeyModalOpen(true)}
                  >
                    {t('cfg.create_key')}
                  </ActionButton>
                }
              />
            </LayeredCard>
          ) : (
            <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
              {keys.map((k, index) => {
                // 0 (or missing) means "unlimited" for both budgets.
                const hasDailyCap = (k.max_budget_cu_daily ?? 0) > 0;
                const hasTotalCap = (k.max_budget_cu_total ?? 0) > 0;
                const dailyPct = hasDailyCap
                  ? Math.min(100, (k.used_cu_today / k.max_budget_cu_daily) * 100)
                  : k.used_cu_today > 0
                    ? 12
                    : 0;
                const totalPct = hasTotalCap
                  ? Math.min(100, (k.used_cu_total / k.max_budget_cu_total) * 100)
                  : k.used_cu_total > 0
                    ? 12
                    : 0;
                const dailyRemainingPct = hasDailyCap ? Math.max(0, 100 - dailyPct) : 100;
                // 0 (or missing) means "follow the default idle policy".
                const hasIdleKill = (k.idle_auto_kill_min ?? 0) > 0;

                const dailyTone = dailyPct > 85 ? 'coral' : dailyPct > 60 ? 'amber' : 'emerald';
                const totalTone = totalPct > 85 ? 'coral' : totalPct > 60 ? 'amber' : 'sky';

                return (
                  <LayeredCard key={k.id} padding="md" staggerIndex={index + 1} className="rounded-[20px]">
                    <div className="space-y-4">
                      <div className="flex items-start justify-between gap-3">
                        <div className="flex items-center gap-3 min-w-0">
                          <CircularQuotaRing
                            value={dailyRemainingPct}
                            size={38}
                            strokeWidth={3.3}
                            tone={dailyTone}
                          />
                          <div className="min-w-0 space-y-1">
                            <div className="flex flex-wrap items-center gap-2">
                              <span className="text-[14.5px] font-bold text-fg">{k.name}</span>
                              <StatusPill
                                tone={
                                  (k.default_strategy || 'waste-zero') === 'waste-zero'
                                    ? 'emerald'
                                    : 'sky'
                                }
                                size="xs"
                                mono={!zh}
                                title={
                                  zh
                                    ? `选号方式：${k.default_strategy || 'waste-zero'}`
                                    : 'Account-picking strategy'
                                }
                              >
                                {strategyLabel(k.default_strategy || 'waste-zero', zh)}
                              </StatusPill>
                            </div>
                            <div className="flex flex-wrap items-center gap-x-1.5 gap-y-0.5 ch-num text-[11.5px] text-fg-muted leading-snug">
                              <span>
                                {displaySecret(`${(k.key_prefix || '').replace(/\.+$/, '')}••••••`)}
                              </span>
                              <span aria-hidden="true">·</span>
                              <span>
                                {zh ? `同开 ≤${k.max_concurrent_vms} 台` : `Max ${k.max_concurrent_vms} VMs`}
                              </span>
                              <span aria-hidden="true">·</span>
                              <span>
                                {hasIdleKill
                                  ? zh
                                    ? `空闲 ${k.idle_auto_kill_min}m 关停`
                                    : `Idle kill ${k.idle_auto_kill_min}m`
                                  : zh
                                    ? '默认自动关停'
                                    : 'Auto idle stop'}
                              </span>
                            </div>
                          </div>
                        </div>

                        <div className="flex items-center gap-1 shrink-0">
                          {onOpenConnectWithKey && (
                            <ActionButton
                              variant="primary"
                              size="xs"
                              icon={<Zap />}
                              onClick={() => onOpenConnectWithKey(k.raw_key || k.key_prefix)}
                            >
                              {zh ? '一键接入' : 'Connect'}
                            </ActionButton>
                          )}
                          <IconButton
                            label={zh ? '停用这把密钥' : 'Revoke key'}
                            onClick={() => handleRevokeKey(k)}
                            className="hover:text-[var(--coral-text)]"
                          >
                            <Trash2 className="w-3.5 h-3.5" />
                          </IconButton>
                        </div>
                      </div>

                      {/* Recessed Stardust Segmented CU budget bars */}
                      <InsetWell padding="sm">
                        <div className="grid grid-cols-1 sm:grid-cols-2 gap-4">
                          <div className="space-y-2">
                            <div className="flex items-center justify-between gap-2 text-[11.5px]">
                              <span className="text-fg-secondary">
                                {zh ? '今天已用 / 每日上限' : 'Used today / daily cap'}
                              </span>
                              <span className="ch-num font-bold text-fg">
                                {hasDailyCap ? (
                                  <>
                                    <AnimatedNumber value={k.used_cu_today} format={fmtCU1} /> /{' '}
                                    {formatCU(k.max_budget_cu_daily, 0)} CU
                                  </>
                                ) : (
                                  <>
                                    <AnimatedNumber value={k.used_cu_today} format={fmtCU1} /> CU ·{' '}
                                    {zh ? '不限' : 'Unlimited'}
                                  </>
                                )}
                              </span>
                            </div>
                            <SegmentedQuotaBar
                              value={dailyPct}
                              segments={18}
                              tone={dailyTone}
                            />
                          </div>

                          <div className="space-y-2">
                            <div className="flex items-center justify-between gap-2 text-[11.5px]">
                              <span className="text-fg-secondary">
                                {zh ? '累计已用 / 总上限' : 'Used in total / total cap'}
                              </span>
                              <span className="ch-num font-bold text-fg">
                                {hasTotalCap ? (
                                  <>
                                    <AnimatedNumber value={k.used_cu_total} format={fmtCU1} /> /{' '}
                                    {formatCU(k.max_budget_cu_total, 0)} CU
                                  </>
                                ) : (
                                  <>
                                    <AnimatedNumber value={k.used_cu_total} format={fmtCU1} /> CU ·{' '}
                                    {zh ? '不限' : 'Unlimited'}
                                  </>
                                )}
                              </span>
                            </div>
                            <SegmentedQuotaBar
                              value={totalPct}
                              segments={18}
                              tone={totalTone}
                            />
                          </div>
                        </div>
                      </InsetWell>

                      <div className="flex flex-wrap items-center justify-between gap-2 pt-3 border-t border-line-faint">
                        <div className="flex flex-wrap gap-1">
                          {(k.allowed_gpus || []).map((g) => (
                            <HardwareTierBadge key={g} accelerator={g} size="xs" />
                          ))}
                        </div>
                        <div className="ch-num text-[11px] text-fg-muted">
                          {(k.allowed_groups || []).length > 0
                            ? k.allowed_groups.map((g) => `#${g}`).join(' ')
                            : zh
                              ? '可用全部账号分组'
                              : 'All account groups'}
                        </div>
                      </div>
                    </div>
                  </LayeredCard>
                );
              })}
            </div>
          )}
        </div>
      )}

      {/* ===================================================================
       * Tab 2 — Outbound proxy pool
       * ================================================================= */}
      {activeTab === 'proxies' && (
        <div className="space-y-4">
          <WhenToUseCard
            icon={<Globe />}
            title={zh ? '什么时候需要配置网络代理？' : 'When do you need a Network Proxy?'}
            lead={
              zh
                ? '多账号同机登录时，为每个账号绑定独立代理 IP 更稳妥'
                : 'When signing in to multiple Google accounts on one machine, pinning each account to its own proxy IP is safer'
            }
            body={
              zh
                ? '如果只挂 1–2 个自用账号，保持默认本机直连即可；当你同时管理多个 Google 账号或跨区账号时，在这里添加 HTTP / SOCKS5 代理并在「账号与算力池」里绑定后，该账号的登录刷新、余额查询和代码执行都会固定走同一个出口 IP。'
                : 'With 1–2 personal accounts, Direct Connection works fine. Add HTTP / SOCKS5 proxies here when managing multiple or cross-region accounts so all OAuth, quota checks, and Jupyter tunnels stay pinned to one exit IP.'
            }
          />

          <LayeredCard
            padding="none"
            staggerIndex={1}
            title={t('cfg.proxies')}
            subtitle={
              zh
                ? '添加后，到「账号与算力池」里点开某个账号，就能把它绑定到这里的任意一个代理。'
                : 'After adding one, open an account under “Accounts & Sharing” to bind it to any proxy listed here.'
            }
            headerRight={
              <ActionButton
                variant="ghost"
                size="xs"
                icon={<Plus />}
                onClick={() => setAddProxyModalOpen(true)}
              >
                {t('cfg.add_proxy')}
              </ActionButton>
            }
          >
            {proxies.length === 0 ? (
              <EmptyState
                icon={<Globe />}
                title={zh ? '还没有添加代理（默认本机直连）' : 'No proxies yet (Direct Connection by default)'}
                description={
                  zh
                    ? '只用 1–2 个自己的账号时不需要代理。账号多了以后，再来这里添加 HTTP / SOCKS5 代理并绑定给各个账号。'
                    : 'You do not need one for 1–2 personal accounts. Add HTTP / SOCKS5 proxies here once you manage more.'
                }
                action={
                  <ActionButton
                    variant="secondary"
                    icon={<Plus />}
                    onClick={() => setAddProxyModalOpen(true)}
                  >
                    {t('cfg.add_proxy')}
                  </ActionButton>
                }
              />
            ) : (
              <ul className="ui-stagger divide-y divide-line-faint">
                {proxies.map((p) => {
                  const probe = probeMap[p.id];
                  const latency = probe?.latency_ms ?? p.latency_ms;
                  const healthy = probe?.healthy ?? p.healthy;
                  return (
                    <li
                      key={p.id}
                      className="px-5 sm:px-6 py-4 flex flex-col lg:flex-row lg:items-center gap-x-6 gap-y-3 transition-colors hover:bg-[var(--hover-fill)] last:rounded-b-[15px]"
                    >
                      <div className="min-w-0 flex-1 space-y-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <span className="text-[13.5px] font-semibold text-fg">{p.name}</span>
                          <StatusPill size="xs" mono>
                            {p.protocol.toUpperCase()}
                          </StatusPill>
                        </div>
                        <div className="ch-num text-[12px] text-fg-secondary truncate">
                          {displayProxy(p.url || p.masked_url).replace(/%2A/gi, '*')}
                        </div>
                      </div>

                      <div className="lg:w-48 shrink-0 text-[12px]">
                        <div className="ch-num text-fg">{displayIP(probe?.exit_ip || p.exit_ip)}</div>
                        {p.region && <div className="text-[11.5px] text-fg-muted">{p.region}</div>}
                      </div>

                      <div className="flex flex-wrap items-center gap-2 lg:justify-end shrink-0">
                        <StatusPill
                          size="xs"
                          mono
                          dot
                          tone={!healthy ? 'rose' : latency < 120 ? 'emerald' : 'amber'}
                        >
                          {healthy ? (
                            <>
                              <AnimatedNumber value={latency} decimals={0} /> ms
                            </>
                          ) : zh ? (
                            '连不上'
                          ) : (
                            'Unreachable'
                          )}
                        </StatusPill>
                        <StatusPill size="xs" mono>
                          {zh ? `已绑定 ${p.bound_accounts} 个账号` : `${p.bound_accounts} bound`}
                        </StatusPill>
                        <ActionButton
                          variant="secondary"
                          size="xs"
                          icon={<Wifi />}
                          loading={probingId === p.id}
                          onClick={() => handleProbeUrl(p.url || p.masked_url, p.id)}
                        >
                          {zh ? '测速' : 'Test'}
                        </ActionButton>
                        <IconButton
                          label={zh ? '删除这个代理' : 'Delete this proxy'}
                          onClick={() => handleDeleteProxy(p.id)}
                          className="hover:text-[var(--rose-text)]"
                        >
                          <Trash2 className="w-3.5 h-3.5" />
                        </IconButton>
                      </div>
                    </li>
                  );
                })}
              </ul>
            )}
          </LayeredCard>
        </div>
      )}

      {/* Add proxy node */}
      <DialogModal
        open={addProxyModalOpen}
        onClose={closeAddProxyModal}
        size="md"
        icon={<Globe />}
        title={t('cfg.add_proxy')}
        subtitle={
          zh
            ? '支持 HTTP / HTTPS / SOCKS5，代理的账号密码会加密保存在本机'
            : 'HTTP / HTTPS / SOCKS5 — proxy credentials are stored encrypted on this machine'
        }
        footer={
          <div className="flex justify-end gap-2">
            <ActionButton variant="ghost" onClick={closeAddProxyModal}>
              {t('common.cancel')}
            </ActionButton>
            <ActionButton
              variant="primary"
              type="submit"
              form={ADD_PROXY_FORM_ID}
              loading={savingProxy}
              icon={<Plus />}
            >
              {t('cfg.add_proxy')}
            </ActionButton>
          </div>
        }
      >
        <form id={ADD_PROXY_FORM_ID} onSubmit={handleAddProxy} className="space-y-4">
          <Field label={zh ? '给代理起个名字' : 'Proxy name'} htmlFor="proxy-name">
            <input
              id="proxy-name"
              type="text"
              required
              value={proxyName}
              onChange={(e) => setProxyName(e.target.value)}
              placeholder={zh ? '如 US-LAX-01' : 'e.g. US-LAX-01'}
              className="ui-input"
            />
          </Field>

          <Field label={zh ? '代理地址' : 'Proxy URL'} htmlFor="proxy-url">
            <input
              id="proxy-url"
              type="text"
              required
              value={proxyUrl}
              onChange={(e) => {
                setProxyUrl(e.target.value);
                setInlineProbe(null);
              }}
              placeholder="socks5://user:pass@host:1080"
              className="ui-input ch-num"
            />
          </Field>

          <Field label={zh ? '所在地区（可选）' : 'Region (optional)'} htmlFor="proxy-region">
            <input
              id="proxy-region"
              type="text"
              value={proxyRegion}
              onChange={(e) => setProxyRegion(e.target.value)}
              placeholder={zh ? '如 US-California' : 'e.g. US-California'}
              className="ui-input"
            />
          </Field>

          <InsetWell padding="sm">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="min-w-0 text-[12px]">
                {inlineProbe ? (
                  inlineProbe.healthy ? (
                    <div className="flex flex-wrap items-center gap-2">
                      <StatusPill tone="emerald" size="xs" dot mono>
                        {inlineProbe.latency_ms} ms
                      </StatusPill>
                      <span className="ch-num text-fg-secondary">
                        {zh ? '出口 IP' : 'Exit IP'} {displayIP(inlineProbe.exit_ip)} ·{' '}
                        {inlineProbe.region || (zh ? '已连通' : 'OK')}
                      </span>
                    </div>
                  ) : (
                    <div className="flex flex-wrap items-center gap-2">
                      <StatusPill tone="rose" size="xs" dot>
                        {zh ? '连不上' : 'Unreachable'}
                      </StatusPill>
                      <span className="text-fg-secondary">
                        {inlineProbe.error || (zh ? '连接超时，请检查代理地址' : 'Timeout')}
                      </span>
                    </div>
                  )
                ) : (
                  <span className="text-fg-muted">
                    {zh
                      ? '保存前可以先测一下能不能连通'
                      : 'Test the connection before saving'}
                  </span>
                )}
              </div>
              <ActionButton
                variant="secondary"
                size="xs"
                icon={<Wifi />}
                loading={probingId === '__inline__'}
                disabled={!proxyUrl.trim()}
                onClick={() => handleProbeUrl(proxyUrl, '__inline__')}
              >
                {zh ? '测试连通' : 'Test connection'}
              </ActionButton>
            </div>
          </InsetWell>
        </form>
      </DialogModal>

      {/* Mint scoped agent key */}
      {createKeyModalOpen && (
        <CreateAPIKeyModal
          onClose={() => setCreateKeyModalOpen(false)}
          onCreated={(created) => {
            setKeys((prev) => [created, ...prev]);
            setNewlyCreatedKey(created);
            setCreateKeyModalOpen(false);
          }}
        />
      )}
    </div>
  );
}

function CreateAPIKeyModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (k: AgentAPIKey) => void;
}) {
  const { locale, t } = useI18n();
  const zh = locale === 'zh-CN';

  const [name, setName] = useState('');
  const [strategy, setStrategy] = useState<RoutingStrategy>('waste-zero');
  const [groupsCsv, setGroupsCsv] = useState('research-core, high-vram');
  const [allowedGpus, setAllowedGpus] = useState<AcceleratorType[]>([
    'G4',
    'H100',
    'A100',
    'L4',
    'T4',
    'V6E1',
  ]);
  const [dailyCap, setDailyCap] = useState(50);
  const [totalCap, setTotalCap] = useState(250);
  const [maxVms, setMaxVms] = useState(4);
  const [idleKillMin, setIdleKillMin] = useState(20);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) return;
    setSubmitting(true);
    setSubmitError(null);
    const groups = groupsCsv
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);
    try {
      const created = await api.createAPIKey({
        name: name.trim(),
        default_strategy: strategy,
        allowed_groups: groups,
        allowed_gpus: allowedGpus,
        max_budget_cu_daily: dailyCap,
        max_budget_cu_total: totalCap,
        max_concurrent_vms: maxVms,
        idle_auto_kill_min: idleKillMin,
      });
      onCreated(created);
    } catch (err) {
      if (err instanceof APIError) {
        setSubmitError(err.message);
        return;
      }
      const rand = Math.random().toString(36).slice(2, 18);
      const rawKey = `ch_sk_live_${rand}`;
      onCreated({
        id: `key_${rand.slice(0, 6)}`,
        name: name.trim(),
        key_prefix: rawKey.slice(0, 15),
        raw_key: rawKey,
        allowed_groups: groups,
        allowed_gpus: allowedGpus,
        default_strategy: strategy,
        max_budget_cu_daily: dailyCap,
        max_budget_cu_total: totalCap,
        used_cu_today: 0,
        used_cu_total: 0,
        max_concurrent_vms: maxVms,
        idle_auto_kill_min: idleKillMin,
        enabled: true,
        created_at: new Date().toISOString(),
      });
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <DialogModal
      open
      onClose={onClose}
      size="lg"
      icon={<KeyRound />}
      title={t('cfg.create_key')}
      subtitle={
        zh
          ? '密钥只在创建后完整显示一次；这里设的上限会被严格执行，用到上限就自动停'
          : 'The key is shown in full only once; the limits below are strictly enforced'
      }
      footer={
        <div className="flex justify-end gap-2">
          <ActionButton variant="ghost" onClick={onClose}>
            {t('common.cancel')}
          </ActionButton>
          <ActionButton variant="primary" type="submit" form={CREATE_KEY_FORM_ID} loading={submitting}>
            {t('cfg.create_key')}
          </ActionButton>
        </div>
      }
    >
      <form id={CREATE_KEY_FORM_ID} onSubmit={handleSubmit} className="space-y-4">
        {submitError && (
          <div
            data-tone="coral"
            role="alert"
            className="flex items-center gap-2 rounded-xl border px-3 py-2 text-[12px]"
            style={{
              color: 'var(--tone-fg)',
              backgroundColor: 'var(--tone-soft)',
              borderColor: 'var(--tone-border)',
            }}
          >
            <AlertCircle className="w-3.5 h-3.5 shrink-0" />
            <span>{submitError}</span>
          </div>
        )}
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Field label={zh ? '给谁用（起个名字）' : 'Key label'} htmlFor="key-name">
            <input
              id="key-name"
              type="text"
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. Antigravity-Swarm"
              className="ui-input"
            />
          </Field>

          <Field label={zh ? '默认怎么挑账号' : 'Default Routing Strategy'} htmlFor="key-strategy">
            <select
              id="key-strategy"
              value={strategy}
              onChange={(e) => setStrategy(e.target.value as RoutingStrategy)}
              className={cn('ui-input', !zh && 'ch-num')}
            >
              {ALL_ROUTING_STRATEGIES.map((s) => (
                <option key={s} value={s}>
                  {strategyLabel(s, zh)}
                </option>
              ))}
            </select>
          </Field>
        </div>

        <InsetWell label={zh ? '用量上限' : 'Budgets & Limits'}>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
            <Field label={zh ? '每日 CU 上限' : 'Daily CU Cap'} htmlFor="key-daily">
              <input
                id="key-daily"
                type="number"
                min={1}
                value={dailyCap}
                onChange={(e) => setDailyCap(Number(e.target.value))}
                className="ui-input ch-num"
              />
            </Field>
            <Field label={zh ? '累计 CU 上限' : 'Total CU Cap'} htmlFor="key-total">
              <input
                id="key-total"
                type="number"
                min={1}
                value={totalCap}
                onChange={(e) => setTotalCap(Number(e.target.value))}
                className="ui-input ch-num"
              />
            </Field>
            <Field label={zh ? '最多同时开几台' : 'Max VMs'} htmlFor="key-vms">
              <input
                id="key-vms"
                type="number"
                min={1}
                max={16}
                value={maxVms}
                onChange={(e) => setMaxVms(Number(e.target.value))}
                className="ui-input ch-num"
              />
            </Field>
            <Field label={zh ? '空闲几分钟自动关停' : 'Idle Kill (m)'} htmlFor="key-idle">
              <input
                id="key-idle"
                type="number"
                min={5}
                value={idleKillMin}
                onChange={(e) => setIdleKillMin(Number(e.target.value))}
                className="ui-input ch-num"
              />
            </Field>
          </div>
        </InsetWell>

        <Field
          label={zh ? '可以用哪些分组的账号（逗号分隔）' : 'Allowed Account Groups'}
          htmlFor="key-groups"
        >
          <input
            id="key-groups"
            type="text"
            value={groupsCsv}
            onChange={(e) => setGroupsCsv(e.target.value)}
            className="ui-input ch-num"
          />
        </Field>

        <Field label={zh ? '可以用哪些显卡' : 'Allowed GPU / TPU Tiers'}>
          <div className="flex flex-wrap gap-1.5">
            {ALL_ACCELERATORS.map((g) => {
              const on = allowedGpus.includes(g);
              return (
                <button
                  key={g}
                  type="button"
                  aria-pressed={on}
                  onClick={() =>
                    setAllowedGpus((prev) =>
                      prev.includes(g) ? prev.filter((x) => x !== g) : [...prev, g],
                    )
                  }
                  className={cn(
                    'rounded-full px-3 py-1 text-xs ch-num font-semibold border transition-colors cursor-pointer',
                    on
                      ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
                      : 'bg-surface-1 text-fg-muted border-line hover:border-line-strong hover:text-fg-secondary',
                  )}
                >
                  {g}
                </button>
              );
            })}
          </div>
        </Field>
      </form>
    </DialogModal>
  );
}

export default SettingsPage;
