import { useCallback, useState } from 'react';
import { Check, Copy, Download, Share2, ShieldCheck } from 'lucide-react';
import type { QuotaSummary } from '../../types';
import { formatCU, formatHours } from '../../lib/api';
import { useI18n } from '../../i18n';
import { ActionButton, DialogModal } from '../ui/primitives';

interface ShareQuotaModalProps {
  open: boolean;
  onClose: () => void;
  summary: QuotaSummary;
}

/**
 * The share card always renders in the v4 Stardust Obsidian palette, whatever
 * the app theme, so exported images look crisp everywhere.
 */
const CARD = {
  canvas: '#111110',
  surface: '#1E1E1C',
  border: 'rgba(255, 255, 255, 0.08)',
  highlight: 'rgba(255, 255, 255, 0.05)',
  textPrimary: '#F4F4F2',
  textSecondary: '#A1A19A',
  textMuted: '#71716B',
  accent: '#272725',
  accentHover: '#383835',
  coral: '#F06449',
  emerald: '#22C55E',
  emeraldText: '#4ADE80',
  emeraldSoft: 'rgba(34, 197, 94, 0.14)',
  emeraldBorder: 'rgba(34, 197, 94, 0.28)',
} as const;

const FONT_SANS = '"Plus Jakarta Sans", "PingFang SC", "Noto Sans SC", system-ui, sans-serif';
const FONT_MONO = '"JetBrains Mono", ui-monospace, monospace';

function roundedRectPath(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  w: number,
  h: number,
  r: number,
): void {
  const radius = Math.min(r, w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + radius, y);
  ctx.arcTo(x + w, y, x + w, y + h, radius);
  ctx.arcTo(x + w, y + h, x, y + h, radius);
  ctx.arcTo(x, y + h, x, y, radius);
  ctx.arcTo(x, y, x + w, y, radius);
  ctx.closePath();
}

function hexagonPath(ctx: CanvasRenderingContext2D, cx: number, cy: number, r: number): void {
  ctx.beginPath();
  for (let i = 0; i < 6; i += 1) {
    const angle = (Math.PI / 3) * i - Math.PI / 2;
    const px = cx + r * Math.cos(angle);
    const py = cy + r * Math.sin(angle);
    if (i === 0) ctx.moveTo(px, py);
    else ctx.lineTo(px, py);
  }
  ctx.closePath();
}

export function ShareQuotaModal({ open, onClose, summary }: ShareQuotaModalProps) {
  const { locale, t } = useI18n();
  const zh = locale === 'zh-CN';
  const [copiedMd, setCopiedMd] = useState(false);
  const [exporting, setExporting] = useState(false);

  const g4Hours = summary.gpu_equivalence?.G4?.schedulable_hours ?? summary.total_available_cu / 9.66;
  const h100Hours = summary.gpu_equivalence?.H100?.schedulable_hours ?? summary.total_available_cu / 12.5;
  const a100Hours = summary.gpu_equivalence?.A100?.schedulable_hours ?? summary.total_available_cu / 8.88;
  const l4Hours = summary.gpu_equivalence?.L4?.schedulable_hours ?? summary.total_available_cu / 4.52;

  const tiers = [
    { short: 'G4 · 96GB', full: 'RTX PRO 6000 G4 · 96GB', hrs: formatHours(g4Hours), rate: '9.66 CU/h', color: '#22C55E' },
    { short: 'H100 · 80GB', full: 'NVIDIA H100 · 80GB', hrs: formatHours(h100Hours), rate: '12.50 CU/h', color: '#F06449' },
    { short: 'A100 · 40GB', full: 'NVIDIA A100 · 40GB', hrs: formatHours(a100Hours), rate: '8.88 CU/h', color: '#60A5FA' },
    { short: 'L4 · 24GB', full: 'NVIDIA L4 · 24GB', hrs: formatHours(l4Hours), rate: '4.52 CU/h', color: '#2DD4BF' },
  ];

  const markdownSnapshot = [
    `**ColabHive Compute Pool Snapshot**`,
    `- Pooled Accounts: \`${summary.accounts_healthy}/${summary.accounts_total}\` Healthy (\`0 PII Exposed\`)`,
    `- Total Compute Pool: \`${formatCU(summary.total_available_cu)} CU\` (\`≈ $${formatCU(summary.estimated_value_usd)}\` PAYG)`,
    `- Equivalent Runtime: **G4 96GB** \`${formatHours(g4Hours)}\` · **A100** \`${formatHours(a100Hours)}\` · **L4** \`${formatHours(l4Hours)}\``,
    `- Saved Idle-Waste: \`+${formatCU(summary.saved_idle_waste_cu)} CU\` (Keepalive 8s + Anti-Zombie Armed)`,
  ].join('\n');

  const handleCopyMarkdown = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(markdownSnapshot);
      setCopiedMd(true);
      window.setTimeout(() => setCopiedMd(false), 1800);
    } catch {
      // ignore
    }
  }, [markdownSnapshot]);

  const handleDownloadPng = () => {
    setExporting(true);
    try {
      const canvas = document.createElement('canvas');
      const W = 1200;
      const H = 630;
      canvas.width = W;
      canvas.height = H;
      const ctx = canvas.getContext('2d');
      if (!ctx) return;

      const PAD = 64;

      // L0 canvas + subtle warm emerald stardust top glow
      ctx.fillStyle = CARD.canvas;
      ctx.fillRect(0, 0, W, H);
      const glow = ctx.createRadialGradient(W / 2, -120, 0, W / 2, -120, 760);
      glow.addColorStop(0, 'rgba(34, 197, 94, 0.14)');
      glow.addColorStop(1, 'rgba(34, 197, 94, 0)');
      ctx.fillStyle = glow;
      ctx.fillRect(0, 0, W, H);

      // Brand mark
      const markSize = 44;
      const markY = 56;
      const markGradient = ctx.createLinearGradient(PAD, markY, PAD + markSize, markY + markSize);
      markGradient.addColorStop(0, CARD.accentHover);
      markGradient.addColorStop(1, CARD.accent);
      roundedRectPath(ctx, PAD, markY, markSize, markSize, 14);
      ctx.fillStyle = markGradient;
      ctx.fill();
      hexagonPath(ctx, PAD + markSize / 2, markY + markSize / 2, 13);
      ctx.strokeStyle = '#FFFFFF';
      ctx.lineWidth = 2.6;
      ctx.lineJoin = 'round';
      ctx.stroke();
      hexagonPath(ctx, PAD + markSize / 2, markY + markSize / 2, 4.8);
      ctx.fillStyle = '#FFFFFF';
      ctx.fill();

      // Wordmark + context
      ctx.textBaseline = 'alphabetic';
      ctx.fillStyle = CARD.textPrimary;
      ctx.font = `600 26px ${FONT_SANS}`;
      ctx.fillText('ColabHive', PAD + markSize + 16, markY + 31);
      const wordmarkWidth = ctx.measureText('ColabHive').width;
      ctx.fillStyle = CARD.textMuted;
      ctx.font = `500 18px ${FONT_SANS}`;
      ctx.fillText(
        zh ? '算力池快照' : 'Compute pool snapshot',
        PAD + markSize + 16 + wordmarkWidth + 16,
        markY + 30,
      );

      // Privacy verification pill (right-aligned)
      const pillText = zh ? '已隐藏隐私 · 零邮箱 / 零 IP' : 'Privacy verified · Zero PII';
      ctx.font = `600 15px ${FONT_SANS}`;
      const pillTextWidth = ctx.measureText(pillText).width;
      const pillW = pillTextWidth + 50;
      const pillH = 34;
      const pillX = W - PAD - pillW;
      const pillY = markY + (markSize - pillH) / 2;
      roundedRectPath(ctx, pillX, pillY, pillW, pillH, pillH / 2);
      ctx.fillStyle = CARD.emeraldSoft;
      ctx.fill();
      ctx.strokeStyle = CARD.emeraldBorder;
      ctx.lineWidth = 1.5;
      ctx.stroke();
      ctx.beginPath();
      ctx.arc(pillX + 20, pillY + pillH / 2, 4, 0, Math.PI * 2);
      ctx.fillStyle = CARD.emerald;
      ctx.fill();
      ctx.fillStyle = CARD.emeraldText;
      ctx.fillText(pillText, pillX + 34, pillY + 22);

      // Hero numeral
      ctx.fillStyle = CARD.textSecondary;
      ctx.font = `500 18px ${FONT_SANS}`;
      ctx.fillText(zh ? '总可用算力单元' : 'Total pooled compute units', PAD, 178);

      const heroValue = formatCU(summary.total_available_cu);
      ctx.fillStyle = CARD.textPrimary;
      ctx.font = `600 84px ${FONT_MONO}`;
      ctx.fillText(heroValue, PAD - 3, 266);
      const heroWidth = ctx.measureText(heroValue).width;
      ctx.fillStyle = CARD.textMuted;
      ctx.font = `500 30px ${FONT_SANS}`;
      ctx.fillText('CU', PAD + heroWidth + 12, 266);

      ctx.fillStyle = CARD.textSecondary;
      ctx.font = `500 19px ${FONT_MONO}`;
      ctx.fillText(
        `≈ $${formatCU(summary.estimated_value_usd)} ${zh ? '按量价' : 'PAYG'}  ·  ${summary.accounts_healthy} ${
          zh ? '个可用账号' : 'healthy accounts'
        }  ·  ${formatCU(summary.active_burn_rate_cu_hr)} CU/h`,
        PAD,
        310,
      );

      // Four equivalent-runtime cards (L2)
      const gap = 20;
      const cardW = (W - PAD * 2 - gap * 3) / 4;
      const cardH = 156;
      const cardY = 352;

      tiers.forEach((tier, idx) => {
        const x = PAD + idx * (cardW + gap);

        ctx.save();
        ctx.shadowColor = 'rgba(0, 0, 0, 0.45)';
        ctx.shadowBlur = 28;
        ctx.shadowOffsetY = 10;
        roundedRectPath(ctx, x, cardY, cardW, cardH, 18);
        ctx.fillStyle = CARD.surface;
        ctx.fill();
        ctx.restore();

        roundedRectPath(ctx, x + 0.5, cardY + 0.5, cardW - 1, cardH - 1, 18);
        ctx.strokeStyle = CARD.border;
        ctx.lineWidth = 1;
        ctx.stroke();

        ctx.beginPath();
        ctx.arc(x + 26, cardY + 34, 4.5, 0, Math.PI * 2);
        ctx.fillStyle = tier.color;
        ctx.fill();

        ctx.fillStyle = CARD.textSecondary;
        ctx.font = `500 15px ${FONT_SANS}`;
        ctx.fillText(tier.full, x + 40, cardY + 39);

        ctx.fillStyle = CARD.textPrimary;
        ctx.font = `600 36px ${FONT_MONO}`;
        ctx.fillText(tier.hrs, x + 22, cardY + 96);

        ctx.fillStyle = CARD.textMuted;
        ctx.font = `500 14px ${FONT_MONO}`;
        ctx.fillText(`@ ${tier.rate}`, x + 22, cardY + 128);
      });

      // Footer
      ctx.beginPath();
      ctx.arc(PAD + 5, 561, 4, 0, Math.PI * 2);
      ctx.fillStyle = CARD.emerald;
      ctx.fill();
      ctx.fillStyle = CARD.textSecondary;
      ctx.font = `500 16px ${FONT_MONO}`;
      ctx.fillText(
        `+${formatCU(summary.saved_idle_waste_cu)} CU ${zh ? '空闲自动省下' : 'idle waste saved'}  ·  ${
          summary.auto_teardowns_count
        } ${zh ? '次自动回收' : 'auto-teardowns'}  ·  ${summary.zombie_aborts_count} ${
          zh ? '次卡死清理' : 'zombie aborts'
        }`,
        PAD + 20,
        567,
      );

      const repoText = 'github.com/colabhive/colabhive';
      ctx.fillStyle = CARD.textMuted;
      ctx.font = `500 15px ${FONT_MONO}`;
      ctx.fillText(repoText, W - PAD - ctx.measureText(repoText).width, 567);

      const url = canvas.toDataURL('image/png');
      const a = document.createElement('a');
      a.href = url;
      a.download = `colabhive-quota-card-${new Date().toISOString().slice(0, 10)}.png`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
    } finally {
      window.setTimeout(() => setExporting(false), 350);
    }
  };

  return (
    <DialogModal
      open={open}
      onClose={onClose}
      size="2xl"
      icon={<Share2 />}
      title={t('nav.share_card')}
      subtitle={
        zh
          ? '导出 1200×630 卡片，自动剥离所有账号邮箱、代理 IP 与密钥。'
          : 'Exports a 1200×630 card with no emails, proxy IPs, or tokens.'
      }
      footer={
        <div className="flex flex-wrap items-center justify-end gap-2">
          <ActionButton
            size="md"
            icon={copiedMd ? <Check /> : <Copy />}
            onClick={handleCopyMarkdown}
            style={copiedMd ? { color: 'var(--emerald-text)' } : undefined}
          >
            {copiedMd ? t('common.copied') : zh ? '复制 Markdown' : 'Copy Markdown'}
          </ActionButton>
          <ActionButton
            variant="primary"
            size="md"
            icon={<Download />}
            loading={exporting}
            onClick={handleDownloadPng}
          >
            {zh ? '下载 PNG 卡片' : 'Download PNG'}
          </ActionButton>
        </div>
      }
    >
      {/* Live preview — mirrors the exported canvas, always in the dark palette */}
      <div
        className="relative overflow-hidden rounded-2xl border p-5 sm:p-6"
        style={{
          backgroundColor: CARD.canvas,
          borderColor: CARD.border,
          color: CARD.textPrimary,
          backgroundImage:
            'radial-gradient(520px 220px at 50% -90px, rgba(34, 197, 94, 0.14), transparent 72%)',
        }}
      >
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex items-center gap-2.5">
            <span
              className="inline-flex items-center justify-center w-7 h-7 rounded-[9px]"
              style={{
                backgroundImage: `linear-gradient(145deg, ${CARD.accentHover}, ${CARD.accent})`,
              }}
            >
              <svg viewBox="0 0 20 20" className="w-4 h-4" fill="none" aria-hidden="true">
                <polygon
                  points="10,2.8 16.24,6.4 16.24,13.6 10,17.2 3.76,13.6 3.76,6.4"
                  stroke="#FFFFFF"
                  strokeWidth="1.6"
                  strokeLinejoin="round"
                />
                <polygon
                  points="10,7.4 12.25,8.7 12.25,11.3 10,12.6 7.75,11.3 7.75,8.7"
                  fill="#FFFFFF"
                />
              </svg>
            </span>
            <span className="text-[14px] font-semibold">ColabHive</span>
            <span className="text-[12px]" style={{ color: CARD.textMuted }}>
              {zh ? '算力池快照' : 'Compute pool snapshot'}
            </span>
          </div>
          <span
            className="inline-flex items-center gap-1.5 h-[22px] px-2.5 rounded-full border text-[11px] font-medium"
            style={{
              color: CARD.emeraldText,
              backgroundColor: CARD.emeraldSoft,
              borderColor: CARD.emeraldBorder,
            }}
          >
            <ShieldCheck className="w-3 h-3" />
            {zh ? '已隐藏隐私 · 零邮箱 / 零 IP' : 'Privacy verified · Zero PII'}
          </span>
        </div>

        <div className="mt-6">
          <div className="text-[12px] font-medium" style={{ color: CARD.textSecondary }}>
            {zh ? '总可用算力单元' : 'Total pooled compute units'}
          </div>
          <div className="mt-1 flex flex-wrap items-baseline gap-x-2">
            <span className="ch-num text-[40px] leading-[48px] font-semibold tracking-tight">
              {formatCU(summary.total_available_cu)}
            </span>
            <span className="text-[16px] font-medium" style={{ color: CARD.textMuted }}>
              CU
            </span>
          </div>
          <div className="ch-num mt-1 text-[12px]" style={{ color: CARD.textSecondary }}>
            ≈ ${formatCU(summary.estimated_value_usd)} {zh ? '按量价' : 'PAYG'} ·{' '}
            {summary.accounts_healthy}{' '}
            {zh ? '个可用账号' : 'healthy accounts'} · {summary.active_vms}{' '}
            {zh ? '台运行中 GPU' : 'active VMs'}
          </div>
        </div>

        <div className="mt-5 grid grid-cols-2 sm:grid-cols-4 gap-2.5">
          {tiers.map((tier) => (
            <div
              key={tier.short}
              className="rounded-xl border p-3"
              style={{
                backgroundColor: CARD.surface,
                borderColor: CARD.border,
                boxShadow: `inset 0 1px 0 ${CARD.highlight}, 0 8px 20px -6px rgba(0, 0, 0, 0.45)`,
              }}
            >
              <div
                className="flex items-center gap-1.5 text-[11px] font-medium"
                style={{ color: CARD.textSecondary }}
              >
                <span
                  className="w-1.5 h-1.5 rounded-full shrink-0"
                  style={{ backgroundColor: tier.color }}
                />
                {tier.short}
              </div>
              <div className="ch-num mt-1.5 text-[19px] leading-6 font-semibold">{tier.hrs}</div>
              <div className="ch-num mt-0.5 text-[10.5px]" style={{ color: CARD.textMuted }}>
                @ {tier.rate}
              </div>
            </div>
          ))}
        </div>

        <div
          className="ch-num mt-5 flex flex-wrap items-center justify-between gap-2 text-[11px]"
          style={{ color: CARD.textSecondary }}
        >
          <span className="inline-flex items-center gap-1.5">
            <span className="w-1.5 h-1.5 rounded-full" style={{ backgroundColor: CARD.emerald }} />
            +{formatCU(summary.saved_idle_waste_cu)} CU {zh ? '空闲自动省下' : 'idle waste saved'} ·{' '}
            {summary.zombie_aborts_count} {zh ? '次卡死清理' : 'zombie aborts'}
          </span>
          <span style={{ color: CARD.textMuted }}>github.com/colabhive/colabhive</span>
        </div>
      </div>
    </DialogModal>
  );
}
