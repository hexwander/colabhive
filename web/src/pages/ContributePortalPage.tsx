import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  Bookmark,
  CheckCircle2,
  ExternalLink,
  HeartHandshake,
  Info,
  Lock,
  Pause,
  Play,
  RefreshCw,
  ShieldAlert,
  ShieldCheck,
  Terminal,
  Zap,
} from 'lucide-react';
import type {
  AcceleratorType,
  ContributorGuardrails,
  ContributorInvite,
  ContributorReceiptStatus,
  OAuthPendingSession,
} from '../types';
import { ALL_ACCELERATORS } from '../types';
import { api, APIError, formatCU } from '../lib/api';
import { useI18n } from '../i18n';
import { navigate } from '../components/layout/navigation';
import {
  AccountHealthBadge,
  CopyButton,
  DEMO_INVITES,
  formatUptime,
  HardwareTierBadge,
  SubscriptionTierBadge,
} from '../components/layout/telemetryUi';
import {
  ActionButton,
  AnimatedNumber,
  CircularQuotaRing,
  cn,
  IconButton,
  InsetWell,
  LayeredCard,
  SegmentedQuotaBar,
  Skeleton,
  StatusPill,
  Switch,
  type SemanticTone,
} from '../components/ui/primitives';

export interface ContributePortalPageProps {
  inviteCode?: string;
  receiptToken?: string;
}

// ============================================================================
// Shared building blocks
// ============================================================================

const fmtCU = (v: number) => formatCU(v);
const fmtCU1 = (v: number) => formatCU(v, 1);

/**
 * Only synthesize demo invite/receipt previews when the code or receipt token
 * explicitly starts with `demo` / `ch_inv_demo` / `ch_rcpt_demo` (or `?demo=1`).
 */
function isExplicitDemoPreview(tokenOrCode?: string): boolean {
  const v = (tokenOrCode || '').trim().toLowerCase();
  if (!v) return false;
  if (
    v.startsWith('demo') ||
    v.startsWith('ch_inv_demo') ||
    v.startsWith('ch_rcpt_demo')
  ) {
    return true;
  }
  if (typeof window !== 'undefined') {
    const qs = new URLSearchParams(window.location.search);
    if (qs.get('demo') === '1' || qs.get('COLABHIVE_DEMO') === '1') {
      return true;
    }
  }
  return false;
}

// The built-in demo invite ships with English copy. In zh-CN the guest page
// swaps it for natural Chinese so no English paragraph is left on the page.
const DEMO_POOL_TITLE_EN = 'ICML 2027 Open Reasoning Sharded Pool';
const DEMO_MAINTAINER_EN = 'Systems & Alignment Lab';
const DEMO_WELCOME_PREFIX_EN = 'Donate idle';

function localizeInviteCopy(
  invite: ContributorInvite | null,
  zh: boolean,
): { poolTitle: string; maintainer: string; welcomeNote: string } {
  const poolTitle = invite?.pool_title ?? '';
  const maintainer = invite?.maintainer_name ?? '';
  const welcomeNote = invite?.welcome_note ?? '';
  if (!zh) return { poolTitle, maintainer, welcomeNote };
  return {
    poolTitle:
      poolTitle === DEMO_POOL_TITLE_EN ? 'ICML 2027 联合推理实验算力共享池' : poolTitle,
    maintainer: maintainer === DEMO_MAINTAINER_EN ? '系统与对齐实验室' : maintainer,
    welcomeNote: welcomeNote.startsWith(DEMO_WELCOME_PREFIX_EN)
      ? '把闲置或快要过期的 Colab 算力安全借给伙伴加速多卡实验。系统会自动留足你的自用保底额度，绝不读取你的 Google 云盘，随时可一键收回。'
      : welcomeNote,
  };
}

/** Quiet semantic message strip (success / error feedback). */
function Notice({
  tone,
  icon,
  children,
}: {
  tone: Extract<SemanticTone, 'emerald' | 'rose' | 'amber' | 'accent'>;
  icon?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div
      data-tone={tone}
      role={tone === 'rose' ? 'alert' : tone === 'accent' ? 'note' : 'status'}
      className="flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[12.5px] leading-relaxed [&>svg]:w-4 [&>svg]:h-4 [&>svg]:shrink-0 [&>svg]:mt-px"
      style={{
        color: 'var(--tone-fg)',
        backgroundColor: 'var(--tone-soft)',
        borderColor: 'var(--tone-border)',
      }}
    >
      {icon}
      <span className="min-w-0">{children}</span>
    </div>
  );
}

/** Pill toggle for an accelerator tier. */
function GpuPillToggle({
  accelerator,
  active,
  onToggle,
}: {
  accelerator: AcceleratorType;
  active: boolean;
  onToggle: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onToggle}
      className={cn(
        'rounded-full px-3 py-1 text-xs ch-num font-semibold border transition-colors cursor-pointer',
        active
          ? 'bg-[var(--accent)] text-[var(--accent-fg)] border-[var(--accent)]'
          : 'bg-surface-1 text-fg-muted border-line-faint hover:text-fg-secondary hover:border-line-strong',
      )}
    >
      {accelerator}
    </button>
  );
}

/** One label + switch row inside a hairline-divided well. */
function SwitchRow({
  label,
  checked,
  onChange,
}: {
  label: string;
  checked: boolean;
  onChange: (next: boolean) => void;
}) {
  return (
    <label className="flex items-center justify-between gap-3 px-3 py-2.5 cursor-pointer">
      <span className="text-[12.5px] text-fg">{label}</span>
      <Switch checked={checked} onChange={onChange} label={label} />
    </label>
  );
}

/** Detects a Google auth code inside a raw code or a pasted redirect URL. */
function detectAuthCode(raw: string): { valid: boolean; code: string; fromUrl: boolean } {
  const trimmed = raw.trim();
  if (!trimmed) return { valid: false, code: '', fromUrl: false };

  if (trimmed.startsWith('http://') || trimmed.startsWith('https://') || trimmed.includes('code=')) {
    try {
      const normalized = trimmed.startsWith('http') ? trimmed : `http://localhost/?${trimmed}`;
      const code = new URL(normalized).searchParams.get('code') || '';
      return { valid: code.length >= 10, code, fromUrl: true };
    } catch {
      const m = trimmed.match(/[?&]code=([^&#\s]+)/);
      if (m) {
        const code = decodeURIComponent(m[1]);
        return { valid: code.length >= 10, code, fromUrl: true };
      }
    }
  }

  return { valid: trimmed.length >= 10, code: trimmed, fromUrl: false };
}

export function ContributePortalPage({
  inviteCode,
  receiptToken,
}: ContributePortalPageProps) {
  const { locale, t } = useI18n();
  const zh = locale === 'zh-CN';

  // Landing lookup input when visiting `/contribute` without code/token
  const [lookupInput, setLookupInput] = useState('');

  if (receiptToken) {
    return <ContributorReceiptView receiptToken={receiptToken} />;
  }

  if (inviteCode) {
    return <ContributorGuestOnboardingView inviteCode={inviteCode} />;
  }

  const handleLookupSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const v = lookupInput.trim();
    if (!v) return;
    const manageMatch = v.match(/\/contribute\/manage\/([^/?#]+)/);
    if (manageMatch) {
      navigate(`/contribute/manage/${manageMatch[1]}`);
      return;
    }
    const invMatch = v.match(/\/contribute\/([^/?#]+)/);
    if (invMatch) {
      navigate(`/contribute/${invMatch[1]}`);
      return;
    }
    if (v.startsWith('ch_rcpt_')) {
      navigate(`/contribute/manage/${v}`);
      return;
    }
    navigate(`/contribute/${v}`);
  };

  return (
    <div className="max-w-2xl mx-auto space-y-5">
      <LayeredCard padding="lg" staggerIndex={0}>
        <div className="space-y-6">
          <div className="space-y-3">
            <StatusPill tone="emerald" icon={<HeartHandshake />}>
              {zh ? '免密码 · 零云盘权限 · 随时一键收回' : 'No Password · Zero Drive Access · 1-Click Revoke'}
            </StatusPill>
            <h1 className="ui-title text-[22px] font-semibold">{t('contribute.title')}</h1>
            <p className="text-[13.5px] leading-relaxed text-fg-secondary">
              {t('contribute.subtitle')}
            </p>
          </div>

          <form onSubmit={handleLookupSubmit} className="space-y-2">
            <label htmlFor="contribute-lookup" className="block text-[12px] font-medium text-fg-secondary">
              {zh
                ? '粘贴朋友发来的邀请链接或邀请码 (ch_inv_...)，或你之前保存的回执链接 (ch_rcpt_...)'
                : 'Paste the invite link or code a friend sent you (ch_inv_...), or the receipt link you saved earlier (ch_rcpt_...)'}
            </label>
            <div className="flex flex-col sm:flex-row gap-2">
              <input
                id="contribute-lookup"
                type="text"
                value={lookupInput}
                onChange={(e) => setLookupInput(e.target.value)}
                placeholder="ch_inv_icml2026 or ch_rcpt_..."
                className="ui-input ch-num flex-1"
              />
              <ActionButton variant="primary" size="md" type="submit">
                {zh ? '继续 →' : 'Continue →'}
              </ActionButton>
            </div>
          </form>

          <InsetWell padding="sm">
            <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-2.5">
              <span className="text-[12px] text-fg-muted">
                {zh ? '想先看看共享流程长什么样？' : 'Want to see how sharing works first?'}
              </span>
              <div className="flex flex-wrap gap-2">
                <ActionButton
                  variant="secondary"
                  size="xs"
                  onClick={() => navigate('/contribute/demo_icml2026')}
                >
                  {zh ? '预览：接受朋友邀请' : 'Preview: accept an invite'}
                </ActionButton>
                <ActionButton
                  variant="secondary"
                  size="xs"
                  onClick={() => navigate('/contribute/manage/ch_rcpt_demo_alice99')}
                >
                  {zh ? '预览：我的共享回执' : 'Preview: my sharing receipt'}
                </ActionButton>
              </div>
            </div>
          </InsetWell>
        </div>
      </LayeredCard>
    </div>
  );
}

// ============================================================================
// Part 1: Public Contributor Guest Onboarding (`/contribute/:code`)
// ============================================================================

function ContributorGuestOnboardingView({ inviteCode }: { inviteCode: string }) {
  const { locale, t } = useI18n();
  const zh = locale === 'zh-CN';

  const [invite, setInvite] = useState<ContributorInvite | null>(null);
  const [loading, setLoading] = useState(true);
  const [inviteLoadError, setInviteLoadError] = useState<{
    status?: number;
    message: string;
  } | null>(null);

  // Personal Safety Guardrails configured by the Contributor
  const [minReserveCu, setMinReserveCu] = useState<number>(50);
  const [maxTotalCu, setMaxTotalCu] = useState<number>(200);
  const [maxDailyCu, setMaxDailyCu] = useState<number>(40);
  const [allowPaidBurn, setAllowPaidBurn] = useState<boolean>(true);
  const [allowedGpus, setAllowedGpus] = useState<AcceleratorType[]>([
    'G4',
    'A100',
    'L4',
    'T4',
  ]);
  const [maxConcurrentVms, setMaxConcurrentVms] = useState<number>(2);
  const [nightOnlyWindow, setNightOnlyWindow] = useState<boolean>(false);
  const [maskMyEmail, setMaskMyEmail] = useState<boolean>(false);

  // Remote OAuth Exchange State
  const [pendingOAuth, setPendingOAuth] = useState<OAuthPendingSession | null>(null);
  const [codeOrUrl, setCodeOrUrl] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadInvite = useCallback(() => {
    let active = true;
    setLoading(true);
    setInviteLoadError(null);
    api
      .getPublicInvite(inviteCode)
      .then((inv) => {
        if (!active) return;
        setInvite(inv);
        if (inv.default_guardrails) {
          setMinReserveCu(inv.default_guardrails.min_reserve_cu ?? 50);
          setMaxTotalCu(inv.default_guardrails.max_donated_cu_total ?? 200);
          setMaxDailyCu(inv.default_guardrails.max_donated_cu_daily ?? 40);
          setAllowPaidBurn(inv.default_guardrails.allow_paid_cu_burn ?? true);
          if (inv.default_guardrails.allowed_gpus?.length) {
            setAllowedGpus(inv.default_guardrails.allowed_gpus);
          }
          setMaxConcurrentVms(inv.default_guardrails.max_concurrent_vms || 2);
          setNightOnlyWindow(Boolean(inv.default_guardrails.schedule_window?.enabled));
        }
      })
      .catch((err) => {
        if (!active) return;
        if (isExplicitDemoPreview(inviteCode)) {
          setInvite({ ...DEMO_INVITES[0], code: inviteCode });
          return;
        }
        const status = err instanceof APIError ? err.status : undefined;
        const message =
          status === 404
            ? zh
              ? `未找到邀请链接「${inviteCode}」，请核对链接是否完整或向发起人索取最新链接。`
              : `Invite "${inviteCode}" was not found. Please verify the link with the organizer.`
            : status === 410
              ? zh
                ? '该邀请链接已过期、已撤销或达到最大接入人数上限。'
                : 'This invite link has expired, been revoked, or reached its maximum redemptions.'
              : status === 429
                ? zh
                  ? '请求过于频繁，请稍候片刻再试。'
                  : 'Too many requests — please wait a moment and try again.'
                : err instanceof Error
                  ? err.message
                  : zh
                    ? '无法加载邀请信息，请检查网络连接后重试。'
                    : 'Unable to load invite details. Please check your connection and try again.';
        setInvite(null);
        setInviteLoadError({ status, message });
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [inviteCode, zh]);

  useEffect(() => {
    return loadInvite();
  }, [loadInvite]);

  const builtGuardrails: ContributorGuardrails = useMemo(
    () => ({
      min_reserve_cu: minReserveCu,
      max_donated_cu_total: maxTotalCu,
      max_donated_cu_daily: maxDailyCu,
      donated_cu_used_total: 0,
      donated_cu_used_today: 0,
      allow_paid_cu_burn: allowPaidBurn,
      allowed_gpus: allowedGpus,
      max_concurrent_vms: maxConcurrentVms,
      schedule_window: nightOnlyWindow
        ? {
            enabled: true,
            timezone: Intl.DateTimeFormat().resolvedOptions().timeZone || 'Asia/Shanghai',
            days_of_week: [0, 1, 2, 3, 4, 5, 6],
            start_hour: '22:00',
            end_hour: '09:00',
          }
        : undefined,
      block_drive_propagation: true,
      mask_email_in_dashboard: maskMyEmail,
      paused: false,
    }),
    [
      allowPaidBurn,
      allowedGpus,
      maskMyEmail,
      maxConcurrentVms,
      maxDailyCu,
      maxTotalCu,
      minReserveCu,
      nightOnlyWindow,
    ],
  );

  const detectedCode = useMemo(() => detectAuthCode(codeOrUrl), [codeOrUrl]);

  const toggleGpu = (g: AcceleratorType) => {
    setAllowedGpus((prev) =>
      prev.includes(g) ? prev.filter((x) => x !== g) : [...prev, g],
    );
  };

  const handleStartAuth = async () => {
    setSubmitting(true);
    setError(null);
    try {
      const sess = await api.startContribution(inviteCode, builtGuardrails, 'remote_copy_paste');
      setPendingOAuth(sess);
    } catch (err) {
      if (isExplicitDemoPreview(inviteCode)) {
        const stateId = `ch_contrib_demo_${Math.random().toString(36).slice(2, 10)}`;
        setPendingOAuth({
          state: stateId,
          code_challenge: 'S256',
          auth_url: `https://accounts.google.com/o/oauth2/auth?client_id=764086051850-6qr4p6gpi6hn506pt8ejuq83di341hur.apps.googleusercontent.com&redirect_uri=${encodeURIComponent(
            'https://sdk.cloud.google.com/applicationdefaultauthcode.html',
          )}&response_type=code&scope=${encodeURIComponent(
            'openid https://www.googleapis.com/auth/userinfo.profile https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/colaboratory',
          )}&access_type=offline&prompt=consent&token_usage=remote&state=${stateId}`,
          redirect_uri: 'https://sdk.cloud.google.com/applicationdefaultauthcode.html',
          mode: 'remote_copy_paste',
          scopes: ['openid', 'email', 'colaboratory'],
          is_contributor: true,
          invite_code: inviteCode,
          guardrails: builtGuardrails,
          expires_at: new Date(Date.now() + 600_000).toISOString(),
          created_at: new Date().toISOString(),
        });
      } else {
        setError(
          err instanceof Error
            ? err.message
            : zh
              ? '无法发起授权请求，请检查邀请链接是否仍然有效。'
              : 'Failed to start authorization. Please check whether this invite link is still active.',
        );
      }
    } finally {
      setSubmitting(false);
    }
  };

  const handleCompleteAuth = async () => {
    if (!pendingOAuth || !codeOrUrl.trim()) return;
    setSubmitting(true);
    setError(null);
    try {
      const receipt = await api.completeContribution(
        inviteCode,
        pendingOAuth.state,
        codeOrUrl.trim(),
      );
      try {
        localStorage.setItem('ch_last_receipt_token', receipt.receipt_token);
      } catch {
        // ignore
      }
      navigate(`/contribute/manage/${receipt.receipt_token}`);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Authorization exchange failed');
    } finally {
      setSubmitting(false);
    }
  };

  const origin = typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:7860';
  const cliDonateCmd = `uvx colabhive donate ${origin}/contribute/${inviteCode} --reserve-cu ${minReserveCu} --allow-gpu ${allowedGpus.join(',')}`;

  if (loading) {
    return (
      <div className="max-w-2xl mx-auto space-y-5" aria-busy="true" aria-label={t('common.loading')}>
        <LayeredCard padding="lg">
          <div className="space-y-6">
            <div className="space-y-3">
              <Skeleton className="h-3.5 w-56" />
              <Skeleton className="h-7 w-3/4" />
              <Skeleton className="h-4 w-full" />
            </div>
            <Skeleton className="h-24 w-full rounded-xl" />
            <Skeleton className="h-28 w-full rounded-xl" />
            <div className="flex justify-end">
              <Skeleton className="h-8 w-56" />
            </div>
          </div>
        </LayeredCard>
      </div>
    );
  }

  if (inviteLoadError || !invite) {
    return (
      <div className="max-w-2xl mx-auto space-y-5">
        <LayeredCard padding="lg">
          <div className="space-y-4">
            <h1 className="ui-title text-[20px] font-semibold">
              {zh ? '邀请链接无法使用' : 'Invite link unavailable'}
            </h1>
            <Notice tone="rose" icon={<AlertCircle />}>
              {inviteLoadError?.message ||
                (zh ? '邀请码不存在或已失效。' : 'Invite code not found or no longer active.')}
            </Notice>
            <div className="flex flex-wrap gap-2 pt-1">
              <ActionButton variant="secondary" icon={<RefreshCw />} onClick={() => loadInvite()}>
                {zh ? '重试' : 'Retry'}
              </ActionButton>
              <ActionButton variant="primary" onClick={() => navigate('/contribute')}>
                {zh ? '输入其他邀请码' : 'Enter another invite code'}
              </ActionButton>
            </div>
          </div>
        </LayeredCard>
      </div>
    );
  }

  const reserveTankPct = Math.min(85, Math.max(8, (minReserveCu / 500) * 100));
  const inviteCopy = localizeInviteCopy(invite, zh);

  const guarantees: { icon: React.ReactNode; title: string; body: string }[] = [
    {
      icon: <Lock className="w-3.5 h-3.5 shrink-0 text-[var(--emerald-text)]" />,
      title: zh ? '绝不读取 Google 云盘' : 'Never reads your Google Drive',
      body: zh
        ? '仅申请运行 Colab 必需的基础算力权限，无法查看或挂载你云盘里的任何私人文件。'
        : 'Only asks for the basic permission Colab needs to run — it cannot view or mount any private file in your Drive.',
    },
    {
      icon: <ShieldCheck className="w-3.5 h-3.5 shrink-0 text-[var(--emerald-text)]" />,
      title: zh ? '自动拦截越权访问' : 'Blocks out-of-bounds access',
      body: zh
        ? '代码运行环境内置安全隔离墙，对方无法读取你的 Google 登录凭据或挂载网盘。'
        : 'The runtime has a built-in safety wall, so the other side cannot read your Google sign-in or mount your Drive.',
    },
    {
      icon: <Zap className="w-3.5 h-3.5 shrink-0 text-[var(--emerald-text)]" />,
      title: zh ? '留足自用额度 + 随时收回' : 'Your reserve stays + revoke anytime',
      body: zh
        ? '触及你设定的自留底线前自动停机；保存专属回执链接，无需对方同意随时一键停机收回。'
        : 'GPUs stop automatically before your reserve is touched; keep your private receipt link to stop and revoke in one click — no approval needed.',
    },
  ];

  return (
    <div className="max-w-2xl mx-auto space-y-5">
      {/* Card 1 — who is asking, and the three safety promises */}
      <LayeredCard padding="lg" staggerIndex={0}>
        <div className="space-y-6">
          <div className="space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-[11.5px] text-fg-muted">
                {zh ? '邀请码' : 'Invite'} <span className="ch-num">{invite?.code}</span> ·{' '}
                {zh ? '发起人' : 'From'} {inviteCopy.maintainer}
              </span>
              <StatusPill tone="emerald" mono>
                {zh
                  ? `已有 ${invite?.used_count ?? 0}/${invite?.max_uses ?? 10} 位朋友加入`
                  : `${invite?.used_count ?? 0}/${invite?.max_uses ?? 10} friends joined`}
              </StatusPill>
            </div>
            <h1 className="ui-title text-[22px] font-semibold">
              {inviteCopy.poolTitle || t('contribute.title')}
            </h1>
            {inviteCopy.welcomeNote && (
              <p className="text-[13.5px] leading-relaxed text-fg-secondary">
                {inviteCopy.welcomeNote}
              </p>
            )}
          </div>

          {/* Three security guarantees in one recessed well */}
          <InsetWell padding="md">
            <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 sm:gap-0 sm:divide-x divide-line-faint">
              {guarantees.map((g, i) => (
                <div
                  key={g.title}
                  className={cn(
                    'space-y-1.5',
                    i === 0 && 'sm:pr-4',
                    i === 1 && 'sm:px-4',
                    i === 2 && 'sm:pl-4',
                  )}
                >
                  <div className="flex items-center gap-1.5 text-[12.5px] font-semibold text-fg">
                    {g.icon}
                    <span>{g.title}</span>
                  </div>
                  <p className="text-[11.5px] leading-relaxed text-fg-secondary">{g.body}</p>
                </div>
              ))}
            </div>
            <p className="mt-3.5 pt-3 border-t border-line-faint text-[11.5px] leading-relaxed text-fg-muted">
              {t('contribute.firewall_notice')}
            </p>
          </InsetWell>
        </div>
      </LayeredCard>

      {/* Card 2 — the limits you set yourself */}
      <LayeredCard padding="lg" staggerIndex={1}>
        <div className="space-y-5">
          <div className="space-y-1">
            <h2 className="text-[15px] font-semibold text-fg">
              {zh ? '第 1 步 · 自己定好借出的上限' : 'Step 1 · Set your own lending limits'}
            </h2>
            <p className="text-[12.5px] leading-relaxed text-fg-secondary">
              {zh
                ? '下面每一项都由你说了算，授权之后也能随时修改。'
                : 'Every limit below is yours to decide, and you can change it any time afterwards.'}
            </p>
          </div>

          {/* Min reserve tank & Stardust segmented bar */}
          <InsetWell padding="md">
            <div className="space-y-3.5">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0 space-y-0.5">
                  <label htmlFor="guest-min-reserve" className="block text-[13px] font-bold text-fg">
                    {t('contribute.min_reserve')}
                  </label>
                  <span className="block text-[11.5px] text-fg-muted">
                    {zh
                      ? '触及保底安全仓前自动停机，你的自用额度 100% 锁定保护'
                      : 'GPUs stop automatically before touching your personal safety reserve'}
                  </span>
                </div>
                <div className="flex items-center gap-2.5 shrink-0">
                  <span className="text-[18px] leading-6 ch-num font-bold text-[var(--emerald-text)]">
                    ≥ <AnimatedNumber value={minReserveCu} decimals={0} durationMs={240} /> CU
                  </span>
                  <CircularQuotaRing
                    value={reserveTankPct}
                    size={40}
                    strokeWidth={3.4}
                    tone="sky"
                  />
                </div>
              </div>

              <input
                id="guest-min-reserve"
                type="range"
                min={0}
                max={400}
                step={5}
                value={minReserveCu}
                onChange={(e) => setMinReserveCu(Number(e.target.value))}
                className="w-full accent-[var(--accent)] cursor-pointer"
              />

              <div className="space-y-2.5 pt-0.5">
                <div className="space-y-1">
                  <div className="flex items-baseline justify-between gap-2 text-[11.5px]">
                    <span className="text-fg-secondary">
                      {zh ? '自留保底安全仓 · 绝不动用' : 'Protected personal reserve'}
                    </span>
                    <span className="ch-num font-bold text-fg">
                      {minReserveCu} CU ({Math.round(reserveTankPct)}%)
                    </span>
                  </div>
                  <SegmentedQuotaBar
                    value={reserveTankPct}
                    segments={22}
                    tone="sky"
                  />
                </div>

                <div className="space-y-1">
                  <div className="flex items-baseline justify-between gap-2 text-[11.5px]">
                    <span className="text-fg-secondary">
                      {allowPaidBurn
                        ? zh
                          ? '可借出共享余量 · 仅超出保底部分'
                          : 'Lendable surplus · Above reserve only'
                        : zh
                          ? '仅开放免费显卡窗口 · 付费额度不借出'
                          : 'Free GPU window only · Paid quota locked'}
                    </span>
                    <span className="ch-num font-bold text-[var(--emerald-text)]">
                      {allowPaidBurn ? `${Math.round(100 - reserveTankPct)}%` : 'Free T4'}
                    </span>
                  </div>
                  <SegmentedQuotaBar
                    value={allowPaidBurn ? Math.max(8, 100 - reserveTankPct) : 100}
                    segments={22}
                    tone="emerald"
                  />
                </div>
              </div>
            </div>
          </InsetWell>

          {/* Allowed GPUs + caps */}
          <div className="space-y-4">
            <div className="space-y-2">
              <div className="text-[12.5px] font-medium text-fg">{t('contribute.allowed_gpus')}</div>
              <div className="flex flex-wrap gap-1.5">
                {ALL_ACCELERATORS.map((g) => (
                  <GpuPillToggle
                    key={g}
                    accelerator={g}
                    active={allowedGpus.includes(g)}
                    onToggle={() => toggleGpu(g)}
                  />
                ))}
              </div>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div className="space-y-1.5">
                <label htmlFor="guest-max-total" className="block text-[11.5px] font-medium text-fg-secondary">
                  {t('contribute.max_total')}
                </label>
                <input
                  id="guest-max-total"
                  type="number"
                  min={5}
                  value={maxTotalCu}
                  onChange={(e) => setMaxTotalCu(Number(e.target.value))}
                  className="ui-input ch-num"
                />
              </div>

              <div className="space-y-1.5">
                <label htmlFor="guest-max-daily" className="block text-[11.5px] font-medium text-fg-secondary">
                  {t('contribute.max_daily')}
                </label>
                <input
                  id="guest-max-daily"
                  type="number"
                  min={5}
                  value={maxDailyCu}
                  onChange={(e) => setMaxDailyCu(Number(e.target.value))}
                  className="ui-input ch-num"
                />
              </div>

              <div className="space-y-1.5">
                <label htmlFor="guest-max-vms" className="block text-[11.5px] font-medium text-fg-secondary">
                  {zh ? '最多同时借出几台显卡' : 'Max Concurrent VMs'}
                </label>
                <select
                  id="guest-max-vms"
                  value={maxConcurrentVms}
                  onChange={(e) => setMaxConcurrentVms(Number(e.target.value))}
                  className="ui-input ch-num"
                >
                  <option value={1}>{zh ? '1 台显卡' : '1 VM'}</option>
                  <option value={2}>{zh ? '2 台显卡' : '2 VMs'}</option>
                  <option value={3}>{zh ? '3 台显卡' : '3 VMs'}</option>
                </select>
              </div>
            </div>

            <InsetWell padding="none" className="divide-y divide-line-faint">
              <SwitchRow
                label={zh ? '允许借出超过自留额度的付费算力' : 'Lend paid quota above my reserve'}
                checked={allowPaidBurn}
                onChange={setAllowPaidBurn}
              />
              <SwitchRow
                label={zh ? '只在夜间闲时借出 (22:00–09:00)' : 'Idle nights only (22:00–09:00)'}
                checked={nightOnlyWindow}
                onChange={setNightOnlyWindow}
              />
              <SwitchRow
                label={zh ? '对发起人隐藏我的完整邮箱' : 'Hide my full email from the organizer'}
                checked={maskMyEmail}
                onChange={setMaskMyEmail}
              />
            </InsetWell>
          </div>
        </div>
      </LayeredCard>

      {/* Card 3 — Google sign-in + paste box */}
      <LayeredCard padding="lg" staggerIndex={2}>
        <div className="space-y-5">
          <div className="space-y-1">
            <h2 className="text-[15px] font-semibold text-fg">
              {zh ? '第 2 步 · 用 Google 官方页面登录授权' : 'Step 2 · Sign in on Google’s official page'}
            </h2>
            <p className="text-[12.5px] leading-relaxed text-fg-secondary">
              {zh
                ? '全程在 Google 自己的页面上完成，不需要把密码告诉任何人。'
                : 'Everything happens on Google’s own page — you never share a password with anyone.'}
            </p>
          </div>

          {error && (
            <Notice tone="rose" icon={<AlertCircle />}>
              {error}
            </Notice>
          )}

          {!pendingOAuth ? (
            <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-3">
              <div className="text-[11.5px] leading-relaxed text-fg-muted">
                {zh ? (
                  '仅申请权限：Colab 算力运行 + 基础邮箱识别（绝不含 Google 云盘权限）'
                ) : (
                  <>
                    Scope: <code className="ch-num text-fg-secondary">colaboratory</code> +{' '}
                    <code className="ch-num text-fg-secondary">userinfo.email</code> (No Drive)
                  </>
                )}
              </div>
              <ActionButton
                variant="primary"
                size="md"
                loading={submitting}
                disabled={allowedGpus.length === 0}
                onClick={handleStartAuth}
                className="shrink-0"
              >
                {t('contribute.generate_link')}
              </ActionButton>
            </div>
          ) : (
            <InsetWell padding="md" className="ui-anim-fade">
              <div className="space-y-4">
                <div className="space-y-2">
                  <div className="text-[12.5px] font-medium text-fg">
                    {zh
                      ? 'A · 打开 Google 官方登录页并点击同意'
                      : 'A · Open the official Google sign-in page and approve'}
                  </div>
                  <div className="flex flex-wrap items-center gap-2">
                    <a
                      href={pendingOAuth.auth_url}
                      target="_blank"
                      rel="noreferrer"
                      className="ui-btn ui-btn-secondary"
                    >
                      <ExternalLink className="w-3.5 h-3.5 shrink-0" />
                      {t('pool.open_auth_window')}
                    </a>
                    <CopyButton text={pendingOAuth.auth_url} label={t('pool.copy_auth_url')} />
                  </div>
                </div>

                <div className="space-y-2">
                  <Notice tone="accent" icon={<Info />}>
                    {zh
                      ? '提示：如果授权后跳转到打不开的 localhost 页面属于正常现象，直接把浏览器地址栏整串网址或授权码粘贴到下方即可！'
                      : 'Tip: If redirected to an unreachable localhost page, that is normal — just paste the full URL from your address bar below!'}
                  </Notice>
                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <label htmlFor="guest-auth-code" className="text-[12.5px] font-medium text-fg">
                      {zh
                        ? 'B · 把地址栏整串网址（或授权码）粘贴到这里'
                        : 'B · Paste the full address-bar URL (or the code) here'}
                    </label>
                    {detectedCode.valid && (
                      <StatusPill tone="emerald" size="xs" dot mono>
                        {detectedCode.fromUrl
                          ? zh
                            ? '已从网址中识别'
                            : 'Found in URL'
                          : zh
                            ? '已识别授权码'
                            : 'Code detected'}
                        : {detectedCode.code.slice(0, 10)}…
                      </StatusPill>
                    )}
                  </div>
                  <textarea
                    id="guest-auth-code"
                    rows={2}
                    value={codeOrUrl}
                    onChange={(e) => setCodeOrUrl(e.target.value)}
                    placeholder={t('pool.paste_placeholder')}
                    className="ui-input h-auto py-2.5 ch-num resize-y"
                  />
                </div>

                <div className="flex justify-end">
                  <ActionButton
                    variant="primary"
                    size="md"
                    loading={submitting}
                    disabled={codeOrUrl.trim().length < 8}
                    onClick={handleCompleteAuth}
                  >
                    {zh ? '完成授权并领取我的回执' : 'Finish & Get My Receipt'}
                  </ActionButton>
                </div>
              </div>
            </InsetWell>
          )}

          {/* CLI one-liner */}
          <div className="pt-5 border-t border-line-faint space-y-2">
            <div className="flex items-center justify-between gap-2">
              <span className="inline-flex items-center gap-1.5 text-[12px] text-fg-secondary">
                <Terminal className="w-3.5 h-3.5" />
                {zh ? '习惯用终端？也可以一行命令完成' : 'Prefer a terminal? One command does it'}
              </span>
              <CopyButton text={cliDonateCmd} />
            </div>
            <div className="ui-code px-3.5 py-2.5 text-[11.5px] ch-num whitespace-pre-wrap break-all leading-relaxed">
              {cliDonateCmd}
            </div>
          </div>
        </div>
      </LayeredCard>
    </div>
  );
}

// ============================================================================
// Part 2: Self-Service Contributor Receipt Portal (`/contribute/manage/:token`)
// ============================================================================

function ContributorReceiptView({ receiptToken }: { receiptToken: string }) {
  const { locale, t } = useI18n();
  const zh = locale === 'zh-CN';

  const [status, setStatus] = useState<ContributorReceiptStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [minReserveDraft, setMinReserveDraft] = useState<number>(60);
  const [maxDailyCuDraft, setMaxDailyCuDraft] = useState<number>(45);
  const [maxConcurrentDraft, setMaxConcurrentDraft] = useState<number>(2);
  const [nightOnlyWindowDraft, setNightOnlyWindowDraft] = useState<boolean>(false);
  const [allowedGpusDraft, setAllowedGpusDraft] = useState<AcceleratorType[]>([]);

  const dirtyGuardrailsRef = useRef<boolean>(false);
  const [dirtyGuardrails, setDirtyGuardrails] = useState<boolean>(false);
  const hasLoadedOnceRef = useRef<boolean>(false);

  const [busy, setBusy] = useState(false);
  const [revokedDone, setRevokedDone] = useState(false);
  const [toast, setToast] = useState<string | null>(null);
  const [guardrailError, setGuardrailError] = useState<string | null>(null);
  const [revokeError, setRevokeError] = useState<string | null>(null);

  const markGuardrailsDirty = useCallback(() => {
    dirtyGuardrailsRef.current = true;
    setDirtyGuardrails(true);
  }, []);

  const applyGuardrailsToDraft = useCallback((g: ContributorGuardrails) => {
    setMinReserveDraft(g.min_reserve_cu);
    setMaxDailyCuDraft(g.max_donated_cu_daily ?? 45);
    setMaxConcurrentDraft(g.max_concurrent_vms || 2);
    setNightOnlyWindowDraft(Boolean(g.schedule_window?.enabled));
    setAllowedGpusDraft(g.allowed_gpus);
  }, []);

  const loadStatus = useCallback(
    async (forceSyncDraft = false) => {
      try {
        const s = await api.getContributionStatus(receiptToken);
        hasLoadedOnceRef.current = true;
        setLoadError(null);
        setStatus(s);
        if (!dirtyGuardrailsRef.current || forceSyncDraft) {
          applyGuardrailsToDraft(s.guardrails);
          if (forceSyncDraft) {
            dirtyGuardrailsRef.current = false;
            setDirtyGuardrails(false);
          }
        }
      } catch (err) {
        if (isExplicitDemoPreview(receiptToken)) {
          const demo: ContributorReceiptStatus = {
            account_id: 'acc_contrib_03',
            masked_email: 'ali***@stanford.edu',
            subscription_tier: 'PRO_PLUS',
            health: 'ACTIVE_WORKING',
            paid_cu_balance: 342.0,
            schedulable_cu: 282.0,
            live_burn_rate_cu_hr: 8.88,
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
              receipt_token: receiptToken,
            },
            active_vms: [
              {
                session_id: 'sess_a100_01',
                accelerator: 'A100',
                burn_rate_cu_hr: 8.88,
                uptime_sec: 4460,
                started_at: new Date(Date.now() - 4460_000).toISOString(),
              },
            ],
            last_polled_at: new Date().toISOString(),
          };
          hasLoadedOnceRef.current = true;
          setLoadError(null);
          setStatus((prev) => prev ?? demo);
          if (!dirtyGuardrailsRef.current || forceSyncDraft) {
            applyGuardrailsToDraft(demo.guardrails);
            if (forceSyncDraft) {
              dirtyGuardrailsRef.current = false;
              setDirtyGuardrails(false);
            }
          }
          return;
        }

        if (!hasLoadedOnceRef.current) {
          const status = err instanceof APIError ? err.status : undefined;
          setLoadError(
            status === 404
              ? zh
                ? '未找到该回执凭证，链接可能输入有误或授权已被彻底移除。'
                : 'Receipt token not found. Please check your saved receipt link.'
              : status === 429
                ? zh
                  ? '查询过于频繁，请稍候再试。'
                  : 'Too many requests — please wait a moment and try again.'
                : err instanceof Error
                  ? err.message
                  : zh
                    ? '无法连接服务查询共享状态，请检查网络后重试。'
                    : 'Unable to load contribution status. Please check your connection and retry.',
          );
        }
      } finally {
        setLoading(false);
      }
    },
    [applyGuardrailsToDraft, receiptToken, zh],
  );

  useEffect(() => {
    dirtyGuardrailsRef.current = false;
    setDirtyGuardrails(false);
    hasLoadedOnceRef.current = false;
    void loadStatus(true);
    const timer = window.setInterval(() => {
      void loadStatus(false);
    }, 15_000);
    return () => window.clearInterval(timer);
  }, [loadStatus]);

  const handleUpdateGuardrails = async (patch?: { paused?: boolean }) => {
    if (!status) return;
    setBusy(true);
    setToast(null);
    setGuardrailError(null);

    const isPauseToggleOnly = patch?.paused !== undefined;
    const tz =
      status.guardrails.schedule_window?.timezone ||
      Intl.DateTimeFormat().resolvedOptions().timeZone ||
      'Asia/Shanghai';
    const nextSchedule = nightOnlyWindowDraft
      ? {
          enabled: true,
          timezone: tz,
          days_of_week: status.guardrails.schedule_window?.days_of_week?.length
            ? status.guardrails.schedule_window.days_of_week
            : [0, 1, 2, 3, 4, 5, 6],
          start_hour: status.guardrails.schedule_window?.start_hour || '22:00',
          end_hour: status.guardrails.schedule_window?.end_hour || '09:00',
        }
      : {
          enabled: false,
          timezone: tz,
          days_of_week: [0, 1, 2, 3, 4, 5, 6],
          start_hour: '22:00',
          end_hour: '09:00',
        };

    const requestPatch = isPauseToggleOnly
      ? { paused: patch.paused }
      : {
          min_reserve_cu: minReserveDraft,
          max_donated_cu_daily: maxDailyCuDraft,
          max_concurrent_vms: maxConcurrentDraft,
          schedule_window: nextSchedule,
          allowed_gpus: allowedGpusDraft,
          paused: status.guardrails.paused,
        };

    try {
      const updated = await api.updateContributionGuardrails(receiptToken, requestPatch);
      setStatus(updated);
      if (!isPauseToggleOnly) {
        dirtyGuardrailsRef.current = false;
        setDirtyGuardrails(false);
        applyGuardrailsToDraft(updated.guardrails);
      }
      setToast(zh ? '新设置已立即生效' : 'Your new limits are live');
    } catch (err) {
      if (isExplicitDemoPreview(receiptToken)) {
        setStatus((prev) =>
          prev
            ? {
                ...prev,
                health:
                  (patch?.paused ?? prev.guardrails.paused) ? 'PAUSED_MANUAL' : 'HEALTHY',
                schedulable_cu: isPauseToggleOnly
                  ? prev.schedulable_cu
                  : Math.max(0, prev.paid_cu_balance - minReserveDraft),
                guardrails: isPauseToggleOnly
                  ? { ...prev.guardrails, paused: Boolean(patch?.paused) }
                  : {
                      ...prev.guardrails,
                      min_reserve_cu: minReserveDraft,
                      max_donated_cu_daily: maxDailyCuDraft,
                      max_concurrent_vms: maxConcurrentDraft,
                      schedule_window: nextSchedule,
                      allowed_gpus: allowedGpusDraft,
                    },
              }
            : null,
        );
        if (!isPauseToggleOnly) {
          dirtyGuardrailsRef.current = false;
          setDirtyGuardrails(false);
        }
        setToast(zh ? '预览模式：新设置已更新' : 'Preview mode: limits updated');
      } else {
        setGuardrailError(
          err instanceof Error
            ? err.message
            : zh
              ? '保存设置失败，请检查网络连接后重试'
              : 'Failed to save limits. Please check your connection and try again.',
        );
      }
    } finally {
      setBusy(false);
    }
  };

  const handleEmergencyRevoke = async () => {
    const ok = window.confirm(
      zh
        ? '确认立即停掉你账号上所有在跑的显卡、向 Google 撤销本次授权，并彻底删除保存的登录凭证？'
        : 'Stop every GPU running on your account now, revoke this Google authorization, and permanently delete the stored sign-in?',
    );
    if (!ok) return;
    setBusy(true);
    setRevokeError(null);
    setToast(null);
    try {
      if (isExplicitDemoPreview(receiptToken)) {
        setRevokedDone(true);
        return;
      }
      await api.revokeContribution(receiptToken);
      setRevokedDone(true);
    } catch (err) {
      setRevokeError(
        err instanceof Error
          ? err.message
          : zh
            ? '撤回授权失败，请检查网络连接后重试，或直接前往 Google 账号安全页移除授权。'
            : 'Failed to revoke access. Please check your connection and retry, or revoke directly in your Google Account security settings.',
      );
    } finally {
      setBusy(false);
    }
  };

  const receiptUrl =
    typeof window !== 'undefined'
      ? window.location.href
      : `/contribute/manage/${receiptToken}`;

  if (loading) {
    return (
      <div className="max-w-2xl mx-auto space-y-5" aria-busy="true" aria-label={t('common.loading')}>
        <LayeredCard padding="lg">
          <div className="space-y-5">
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-7 w-2/3" />
            <Skeleton className="h-20 w-full rounded-xl" />
            <Skeleton className="h-12 w-full rounded-xl" />
          </div>
        </LayeredCard>
      </div>
    );
  }

  if (loadError || !status) {
    return (
      <div className="max-w-2xl mx-auto space-y-5">
        <LayeredCard padding="lg">
          <div className="space-y-4">
            <h1 className="ui-title text-[20px] font-semibold">
              {zh ? '无法打开共享回执' : 'Receipt unavailable'}
            </h1>
            <Notice tone="rose" icon={<AlertCircle />}>
              {loadError || (zh ? '回执不存在或已失效。' : 'Receipt not found or no longer active.')}
            </Notice>
            <div className="flex flex-wrap gap-2 pt-1">
              <ActionButton variant="secondary" icon={<RefreshCw />} onClick={() => void loadStatus(true)}>
                {zh ? '重试' : 'Retry'}
              </ActionButton>
              <ActionButton variant="primary" onClick={() => navigate('/contribute')}>
                {zh ? '返回贡献页' : 'Back to portal'}
              </ActionButton>
            </div>
          </div>
        </LayeredCard>
      </div>
    );
  }

  if (revokedDone || status.health === 'REVOKED_BY_USER') {
    return (
      <div className="max-w-2xl mx-auto space-y-5">
        <LayeredCard padding="lg">
          <div className="flex flex-col items-center text-center py-4">
            <div
              data-tone="emerald"
              className="mb-4 flex items-center justify-center w-11 h-11 rounded-xl border"
              style={{
                color: 'var(--tone-fg)',
                backgroundColor: 'var(--tone-soft)',
                borderColor: 'var(--tone-border)',
              }}
            >
              <CheckCircle2 className="w-5 h-5" />
            </div>
            <h1 className="ui-title text-[18px] font-semibold">
              {zh ? '已停机，并收回了 Google 授权' : 'GPUs stopped and Google access revoked'}
            </h1>
            <p className="mt-2 max-w-md text-[13px] leading-relaxed text-fg-secondary">
              {zh
                ? '你账号上所有在跑的显卡已立即停止，ColabHive 已向 Google 撤销本次授权，并删除了保存的全部登录凭证。感谢你的分享！'
                : 'Every GPU on your account has been stopped, the Google authorization has been revoked, and all stored sign-in data has been deleted. Thank you for sharing!'}
            </p>
            <div className="mt-5 flex flex-wrap justify-center gap-2">
              <StatusPill tone="emerald" dot>
                {zh ? '显卡已停止' : 'GPUs stopped'}
              </StatusPill>
              <StatusPill tone="emerald" dot>
                {zh ? 'Google 授权已收回' : 'Google access revoked'}
              </StatusPill>
              <StatusPill tone="emerald" dot>
                {zh ? '登录凭证已删除' : 'Sign-in data deleted'}
              </StatusPill>
            </div>
          </div>
        </LayeredCard>
      </div>
    );
  }

  const paused = status.guardrails.paused;
  const lending = status.live_burn_rate_cu_hr > 0;

  // Balance split: what stays with the owner vs. what may be lent out.
  const reservedCu = Math.max(0, status.guardrails.min_reserve_cu);
  const lendableCu = Math.max(0, status.schedulable_cu ?? status.paid_cu_balance - reservedCu);
  const balanceBase = Math.max(status.paid_cu_balance, reservedCu, 1);
  const reservedPct = Math.min(100, (reservedCu / balanceBase) * 100);
  const lendablePct = Math.min(100 - reservedPct, (lendableCu / balanceBase) * 100);

  return (
    <div className="max-w-2xl mx-auto space-y-5">
      {/* Receipt telemetry */}
      <LayeredCard padding="lg" staggerIndex={0}>
        <div className="space-y-5">
          <div className="space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="inline-flex items-center gap-1.5 text-[11.5px] font-medium text-fg-muted">
                <Bookmark className="w-3.5 h-3.5" />
                {zh
                  ? '请收藏这条专属回执链接，凭它随时调整或收回'
                  : 'Bookmark this private receipt link — it lets you adjust or revoke anytime'}
              </span>
              <div className="flex items-center gap-1">
                <CopyButton text={receiptUrl} label={zh ? '复制回执链接' : 'Copy receipt link'} />
                <IconButton label={zh ? '刷新状态' : 'Refresh status'} onClick={() => void loadStatus(false)}>
                  <RefreshCw className="w-3.5 h-3.5" />
                </IconButton>
              </div>
            </div>
            <div className="flex items-center justify-between gap-3">
              <div className="space-y-1.5 min-w-0">
                <h1 className="ui-title text-[22px] font-semibold">{t('contribute.receipt_title')}</h1>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="ch-num text-[13.5px] font-semibold text-fg">{status.masked_email}</span>
                  <SubscriptionTierBadge tier={status.subscription_tier} />
                  <AccountHealthBadge health={status.health} />
                </div>
              </div>
              <CircularQuotaRing
                value={lendablePct}
                size={46}
                strokeWidth={3.6}
                tone={paused ? 'amber' : 'emerald'}
              />
            </div>
          </div>

          <InsetWell padding="md">
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-x-4 gap-y-4 sm:gap-x-0 sm:divide-x divide-line-faint">
              <ReceiptMetric
                className="sm:pr-4"
                label={zh ? '我的账号余额' : 'My balance'}
                value={<AnimatedNumber value={status.paid_cu_balance} format={fmtCU} />}
                unit="CU"
              />
              <ReceiptMetric
                className="sm:px-4"
                label={zh ? '为我自己保留' : 'Kept for myself'}
                value={<AnimatedNumber value={status.guardrails.min_reserve_cu} format={fmtCU} />}
                unit="CU"
              />
              <ReceiptMetric
                className="sm:px-4"
                label={zh ? '今天 / 累计已借出' : 'Lent today / total'}
                value={
                  <>
                    <AnimatedNumber
                      value={status.guardrails.donated_cu_used_today}
                      format={fmtCU1}
                    />
                    {' / '}
                    <AnimatedNumber
                      value={status.guardrails.donated_cu_used_total}
                      format={fmtCU1}
                    />
                  </>
                }
                unit="CU"
              />
              <ReceiptMetric
                className="sm:pl-4"
                label={zh ? '当前消耗速度' : 'Current usage rate'}
                value={<AnimatedNumber value={status.live_burn_rate_cu_hr} format={fmtCU} />}
                unit="CU/h"
                valueClassName={lending ? 'text-[var(--coral-text)]' : undefined}
              />
            </div>

            {/* Reserve vs. lendable surplus using SegmentedQuotaBar */}
            <div className="mt-4 pt-3.5 border-t border-line-faint space-y-2">
              <SegmentedQuotaBar
                value={lendablePct}
                segments={22}
                tone={paused ? 'amber' : 'emerald'}
              />
              <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1 text-[11.5px] text-fg-muted">
                <span className="inline-flex items-center gap-1.5">
                  <span
                    className="w-2 h-2 rounded-[2px] shrink-0"
                    style={{ backgroundColor: paused ? 'var(--amber)' : 'var(--emerald)' }}
                  />
                  {paused
                    ? zh
                      ? '已暂停借出'
                      : 'Lending paused'
                    : zh
                      ? '当前可借出余量'
                      : 'Lendable surplus'}
                  <span className="ch-num font-medium text-fg">{formatCU(lendableCu, 0)} CU</span>
                </span>
                <span className="inline-flex items-center gap-1.5">
                  <span
                    className="w-2 h-2 rounded-[2px] shrink-0"
                    style={{ backgroundColor: 'var(--sky)' }}
                  />
                  {zh ? '为我自己保底留存' : 'Protected personal reserve'}
                  <span className="ch-num font-medium text-fg">{formatCU(reservedCu, 0)} CU</span>
                </span>
              </div>
            </div>
          </InsetWell>

          {/* Active VMs */}
          <div className="space-y-2">
            <div className="text-[12px] font-medium text-fg-secondary">
              {zh ? '正在使用你算力的显卡' : 'GPUs running on your account right now'}{' '}
              <span className="ch-num text-fg-muted">({status.active_vms.length})</span>
            </div>
            {status.active_vms.length === 0 ? (
              <InsetWell padding="sm">
                <span className="text-[12px] text-fg-muted">
                  {zh
                    ? '现在没有任务在用你的算力，空闲时不扣任何额度'
                    : 'Nothing is using your compute right now — idle time costs nothing'}
                </span>
              </InsetWell>
            ) : (
              status.active_vms.map((vm) => (
                <InsetWell key={vm.session_id} padding="sm">
                  <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-1.5 ch-num text-[12px]">
                    <div className="flex items-center gap-2.5 min-w-0">
                      <span data-tone="amber" className="ui-dot ui-dot-pulse" />
                      <HardwareTierBadge accelerator={vm.accelerator} />
                      <span className="truncate text-fg">{vm.session_id}</span>
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="text-[var(--amber-text)]">
                        {formatCU(vm.burn_rate_cu_hr)} CU/h
                      </span>
                      <span className="text-fg-muted">
                        {zh ? '已运行' : 'Uptime'} {formatUptime(vm.uptime_sec)}
                      </span>
                    </div>
                  </div>
                </InsetWell>
              ))
            )}
          </div>
        </div>
      </LayeredCard>

      {/* Self-service guardrail controls */}
      <LayeredCard padding="lg" staggerIndex={1}>
        <div className="space-y-5">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 className="text-[15px] font-semibold text-fg">
              {zh ? '随时调整自留额度，或暂停借出' : 'Adjust your reserve or pause lending anytime'}
            </h2>
            <ActionButton
              variant="secondary"
              disabled={busy}
              icon={paused ? <Play /> : <Pause />}
              onClick={() => handleUpdateGuardrails({ paused: !paused })}
            >
              {paused
                ? zh
                  ? '恢复借出'
                  : 'Resume lending'
                : zh
                  ? '暂停借出'
                  : 'Pause lending'}
            </ActionButton>
          </div>

          {toast && (
            <Notice tone="emerald" icon={<CheckCircle2 />}>
              {toast}
            </Notice>
          )}

          {guardrailError && (
            <Notice tone="rose" icon={<AlertCircle />}>
              {guardrailError}
            </Notice>
          )}

          <InsetWell padding="md">
            <div className="space-y-3">
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <label htmlFor="receipt-min-reserve" className="text-[12.5px] font-bold text-fg">
                  {t('contribute.min_reserve')}
                </label>
                <span className="text-[18px] leading-6 ch-num font-bold text-[var(--emerald-text)]">
                  ≥ <AnimatedNumber value={minReserveDraft} decimals={0} durationMs={240} /> CU
                </span>
              </div>
              <input
                id="receipt-min-reserve"
                type="range"
                min={0}
                max={400}
                step={5}
                value={minReserveDraft}
                onChange={(e) => {
                  markGuardrailsDirty();
                  setMinReserveDraft(Number(e.target.value));
                }}
                className="w-full accent-[var(--accent)] cursor-pointer"
              />
              <SegmentedQuotaBar
                value={Math.min(
                  100,
                  Math.round((minReserveDraft / Math.max(status.paid_cu_balance, 400)) * 100),
                )}
                segments={22}
                tone="sky"
              />
            </div>
          </InsetWell>

          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <label htmlFor="receipt-max-daily" className="block text-[11.5px] font-medium text-fg-secondary">
                {t('contribute.max_daily')}
              </label>
              <input
                id="receipt-max-daily"
                type="number"
                min={5}
                value={maxDailyCuDraft}
                onChange={(e) => {
                  markGuardrailsDirty();
                  setMaxDailyCuDraft(Number(e.target.value));
                }}
                className="ui-input ch-num"
              />
            </div>
            <div className="space-y-1.5">
              <label htmlFor="receipt-max-vms" className="block text-[11.5px] font-medium text-fg-secondary">
                {zh ? '最多同时借出几台显卡' : 'Max Concurrent VMs'}
              </label>
              <select
                id="receipt-max-vms"
                value={maxConcurrentDraft}
                onChange={(e) => {
                  markGuardrailsDirty();
                  setMaxConcurrentDraft(Number(e.target.value));
                }}
                className="ui-input ch-num"
              >
                <option value={1}>{zh ? '1 台显卡' : '1 VM'}</option>
                <option value={2}>{zh ? '2 台显卡' : '2 VMs'}</option>
                <option value={3}>{zh ? '3 台显卡' : '3 VMs'}</option>
              </select>
            </div>
          </div>

          <InsetWell padding="none">
            <SwitchRow
              label={zh ? '只在夜间闲时借出 (22:00–09:00)' : 'Idle nights only (22:00–09:00)'}
              checked={nightOnlyWindowDraft}
              onChange={(next) => {
                markGuardrailsDirty();
                setNightOnlyWindowDraft(next);
              }}
            />
          </InsetWell>

          <div className="space-y-2">
            <div className="text-[12.5px] font-medium text-fg">{t('contribute.allowed_gpus')}</div>
            <div className="flex flex-wrap gap-1.5">
              {ALL_ACCELERATORS.map((g) => (
                <GpuPillToggle
                  key={g}
                  accelerator={g}
                  active={allowedGpusDraft.includes(g)}
                  onToggle={() => {
                    markGuardrailsDirty();
                    setAllowedGpusDraft((prev) =>
                      prev.includes(g) ? prev.filter((x) => x !== g) : [...prev, g],
                    );
                  }}
                />
              ))}
            </div>
          </div>

          <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
            {dirtyGuardrails ? (
              <div className="flex items-center gap-2 text-[11.5px] text-fg-muted">
                <span>{zh ? '有未保存的修改' : 'Unsaved changes'}</span>
                <button
                  type="button"
                  onClick={() => {
                    applyGuardrailsToDraft(status.guardrails);
                    dirtyGuardrailsRef.current = false;
                    setDirtyGuardrails(false);
                  }}
                  className="underline underline-offset-2 text-fg-secondary hover:text-fg cursor-pointer"
                >
                  {zh ? '撤销修改' : 'Reset'}
                </button>
              </div>
            ) : (
              <span />
            )}
            <ActionButton variant="primary" loading={busy} onClick={() => handleUpdateGuardrails()}>
              {t('common.save')}
            </ActionButton>
          </div>
        </div>
      </LayeredCard>

      {/* Emergency revoke */}
      <LayeredCard padding="md" staggerIndex={2}>
        <div className="space-y-3">
          {revokeError && (
            <Notice tone="rose" icon={<AlertCircle />}>
              {revokeError}
            </Notice>
          )}
          <div className="flex flex-col sm:flex-row sm:items-center sm:justify-between gap-4">
            <div className="min-w-0 space-y-1">
              <div className="flex items-center gap-2 text-[14px] font-semibold text-fg">
                <ShieldAlert className="w-4 h-4 shrink-0 text-[var(--rose-text)]" />
                <span>
                  {zh ? '一键停机并收回授权' : 'Stop everything & revoke access'}
                </span>
              </div>
              <p className="text-[12.5px] leading-relaxed text-fg-secondary">
                {zh
                  ? '不需要对方同意：立即停掉你账号上所有在跑的显卡，向 Google 撤销本次授权，并删除 ColabHive 保存的全部登录凭证。'
                  : 'No approval needed: stops every GPU running on your account, revokes this authorization at Google, and deletes all sign-in data ColabHive stored.'}
              </p>
            </div>
            <ActionButton
              variant="danger"
              size="md"
              disabled={busy}
              onClick={handleEmergencyRevoke}
              className="shrink-0"
            >
              {t('contribute.instant_revoke')}
            </ActionButton>
          </div>
        </div>
      </LayeredCard>
    </div>
  );
}

function ReceiptMetric({
  label,
  value,
  unit,
  className,
  valueClassName,
}: {
  label: string;
  value: React.ReactNode;
  unit: string;
  className?: string;
  valueClassName?: string;
}) {
  return (
    <div className={cn('min-w-0', className)}>
      <div className="text-[11.5px] text-fg-muted">{label}</div>
      <div className="mt-1 flex flex-wrap items-baseline gap-x-1">
        <span
          className={cn('ch-num text-[17px] leading-6 font-semibold', valueClassName ?? 'text-fg')}
        >
          {value}
        </span>
        <span className="text-[11px] text-fg-muted">{unit}</span>
      </div>
    </div>
  );
}

export default ContributePortalPage;
