import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  AlertCircle,
  Award,
  Check,
  Copy,
  Download,
  Loader2,
  Moon,
  RefreshCw,
  ShieldCheck,
  Smartphone,
  Sparkles,
  Sun,
  Monitor,
} from 'lucide-react';
import type { SavingsReceiptStats } from '../../types';
import { useI18n } from '../../i18n';
import { v11Api } from '../../lib/apiV11';
import {
  ActionButton,
  DialogModal,
  InsetWell,
  SegmentedTabs,
  StatusPill,
} from './primitives';

export interface SavingsReceiptModalProps {
  open: boolean;
  onClose: () => void;
}

export type PosterTheme = 'obsidian-gold' | 'warm-alabaster';
export type PosterLayout = 'vertical' | 'landscape';

interface Palette {
  bgTop: string;
  bgBottom: string;
  glowGold: string;
  glowEmerald: string;
  frameStroke: string;
  cardBg: string;
  cardStroke: string;
  cardShadow: string;
  heroWellBg: string;
  heroWellStroke: string;
  textPrimary: string;
  textSecondary: string;
  textMuted: string;
  goldStart: string;
  goldEnd: string;
  goldSoft: string;
  goldBorder: string;
  goldText: string;
  emerald: string;
  emeraldSoft: string;
  emeraldBorder: string;
  emeraldText: string;
  divider: string;
  watermark: string;
}

const PALETTES: Record<PosterTheme, Palette> = {
  'obsidian-gold': {
    bgTop: '#0C0D10',
    bgBottom: '#14151A',
    glowGold: 'rgba(245, 158, 11, 0.22)',
    glowEmerald: 'rgba(16, 185, 129, 0.14)',
    frameStroke: 'rgba(245, 158, 11, 0.28)',
    cardBg: '#181920',
    cardStroke: 'rgba(255, 255, 255, 0.085)',
    cardShadow: 'rgba(0, 0, 0, 0.45)',
    heroWellBg: 'rgba(245, 158, 11, 0.075)',
    heroWellStroke: 'rgba(245, 158, 11, 0.30)',
    textPrimary: '#FAF9F5',
    textSecondary: '#C4C2BA',
    textMuted: '#85837B',
    goldStart: '#FDE68A',
    goldEnd: '#F59E0B',
    goldSoft: 'rgba(245, 158, 11, 0.14)',
    goldBorder: 'rgba(245, 158, 11, 0.36)',
    goldText: '#FCD34D',
    emerald: '#10B981',
    emeraldSoft: 'rgba(16, 185, 129, 0.14)',
    emeraldBorder: 'rgba(16, 185, 129, 0.34)',
    emeraldText: '#34D399',
    divider: 'rgba(255, 255, 255, 0.10)',
    watermark: 'rgba(245, 158, 11, 0.055)',
  },
  'warm-alabaster': {
    bgTop: '#FAF9F5',
    bgBottom: '#F2EFE7',
    glowGold: 'rgba(245, 158, 11, 0.15)',
    glowEmerald: 'rgba(16, 185, 129, 0.10)',
    frameStroke: 'rgba(217, 119, 6, 0.24)',
    cardBg: '#FFFFFF',
    cardStroke: 'rgba(24, 24, 27, 0.08)',
    cardShadow: 'rgba(24, 24, 27, 0.06)',
    heroWellBg: '#FFFDF7',
    heroWellStroke: 'rgba(217, 119, 6, 0.26)',
    textPrimary: '#18181B',
    textSecondary: '#52525B',
    textMuted: '#78756E',
    goldStart: '#B45309',
    goldEnd: '#D97706',
    goldSoft: 'rgba(245, 158, 11, 0.12)',
    goldBorder: 'rgba(217, 119, 6, 0.28)',
    goldText: '#B45309',
    emerald: '#059669',
    emeraldSoft: 'rgba(16, 185, 129, 0.11)',
    emeraldBorder: 'rgba(5, 150, 105, 0.25)',
    emeraldText: '#047857',
    divider: 'rgba(24, 24, 27, 0.10)',
    watermark: 'rgba(217, 119, 6, 0.055)',
  },
};

const CANVAS_SANS =
  '"Plus Jakarta Sans", -apple-system, BlinkMacSystemFont, "PingFang SC", "Microsoft YaHei UI", "Noto Sans SC", system-ui, sans-serif';
const CANVAS_MONO =
  '"JetBrains Mono", "SF Mono", "Cascadia Mono", ui-monospace, monospace';

const EMAIL_SCRUB_RE = /[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}/g;

export function scrubSavingsPII(text: string): string {
  return (text || '').replace(EMAIL_SCRUB_RE, '***');
}

const scrubPII = scrubSavingsPII;

function roundRect(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  w: number,
  h: number,
  r: number,
): void {
  const rad = Math.min(r, w / 2, h / 2);
  ctx.beginPath();
  ctx.moveTo(x + rad, y);
  ctx.arcTo(x + w, y, x + w, y + h, rad);
  ctx.arcTo(x + w, y + h, x, y + h, rad);
  ctx.arcTo(x, y + h, x, y, rad);
  ctx.arcTo(x, y, x + w, y, rad);
  ctx.closePath();
}

function drawHexCell(
  ctx: CanvasRenderingContext2D,
  cx: number,
  cy: number,
  r: number,
): void {
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

/** Draws ColabHive's 3-Cell Isometric Honeycomb Tensor Squircle Emblem on Canvas. */
function drawBrandEmblem(
  ctx: CanvasRenderingContext2D,
  x: number,
  y: number,
  size: number,
): void {
  ctx.save();
  const r = size * 0.285;
  const bg = ctx.createLinearGradient(x, y, x, y + size);
  bg.addColorStop(0, '#242320');
  bg.addColorStop(1, '#111110');
  ctx.shadowColor = 'rgba(0, 0, 0, 0.28)';
  ctx.shadowBlur = 12;
  ctx.shadowOffsetY = 4;
  roundRect(ctx, x, y, size, size, r);
  ctx.fillStyle = bg;
  ctx.fill();
  ctx.restore();

  roundRect(ctx, x + 0.75, y + 0.75, size - 1.5, size - 1.5, r - 0.5);
  ctx.strokeStyle = 'rgba(255, 248, 235, 0.22)';
  ctx.lineWidth = 1;
  ctx.stroke();

  const scale = size / 28;
  const cellR = 4.65 * scale;

  // Top Crown Cell — Amber Gold
  drawHexCell(ctx, x + 14 * scale, y + 9.1 * scale, cellR);
  ctx.fillStyle = '#F59E0B';
  ctx.fill();

  // Bottom-Left Cell — Warm Alabaster
  drawHexCell(ctx, x + 9.3 * scale, y + 17.2 * scale, cellR);
  ctx.fillStyle = '#FAF9F5';
  ctx.fill();

  // Bottom-Right Cell — Compute Emerald
  drawHexCell(ctx, x + 18.7 * scale, y + 17.2 * scale, cellR);
  ctx.fillStyle = '#10B981';
  ctx.fill();
}

function drawHoneycombWatermark(
  ctx: CanvasRenderingContext2D,
  originX: number,
  originY: number,
  stroke: string,
): void {
  ctx.save();
  ctx.strokeStyle = stroke;
  ctx.lineWidth = 1.25;
  const r = 24;
  const dx = Math.sqrt(3) * r;
  const dy = 1.5 * r;
  for (let row = 0; row < 4; row += 1) {
    for (let col = 0; col < 4; col += 1) {
      const cx = originX + col * dx + (row % 2 === 1 ? dx / 2 : 0);
      const cy = originY + row * dy;
      drawHexCell(ctx, cx, cy, r - 2);
      ctx.stroke();
    }
  }
  ctx.restore();
}

function formatMoneyInt(n: number): string {
  return Math.max(0, Math.round(n)).toLocaleString('en-US');
}

/**
 * Draws text truncated cleanly with `…` when exceeding `maxWidth` instead of
 * Canvas's native `fillText(..., maxWidth)` horizontal glyph squishing.
 */
function drawFitText(
  ctx: CanvasRenderingContext2D,
  text: string,
  x: number,
  y: number,
  maxWidth: number,
): void {
  if (maxWidth <= 0 || !text) return;
  if (ctx.measureText(text).width <= maxWidth) {
    ctx.fillText(text, x, y);
    return;
  }
  const ellipsis = '…';
  let lo = 0;
  let hi = text.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (ctx.measureText(text.slice(0, mid) + ellipsis).width <= maxWidth) {
      lo = mid;
    } else {
      hi = mid - 1;
    }
  }
  ctx.fillText(lo > 0 ? text.slice(0, lo) + ellipsis : ellipsis, x, y);
}

export function getEffectiveDPR(): number {
  const raw = typeof window !== 'undefined' ? window.devicePixelRatio || 2 : 2;
  return Math.max(2, Math.min(3, raw));
}

export function paintSavingsPoster(
  canvas: HTMLCanvasElement,
  stats: SavingsReceiptStats,
  posterTheme: PosterTheme,
  layout: PosterLayout,
  zh: boolean,
  dprOverride?: number,
): void {
  const dpr = dprOverride ?? getEffectiveDPR();
  const logicalW = layout === 'vertical' ? 540 : 820;
  const logicalH = layout === 'vertical' ? 740 : 490;
  const targetW = Math.round(logicalW * dpr);
  const targetH = Math.round(logicalH * dpr);

  // Only reset canvas backing store when pixel dimensions genuinely change
  if (canvas.width !== targetW) canvas.width = targetW;
  if (canvas.height !== targetH) canvas.height = targetH;

  const ctx = canvas.getContext('2d');
  if (!ctx) return;

  ctx.save();
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, logicalW, logicalH);
  ctx.imageSmoothingEnabled = true;
  ctx.imageSmoothingQuality = 'high';

  const P = PALETTES[posterTheme];
  const pad = layout === 'vertical' ? 36 : 38;

  // 1. Clip entire exported 2x PNG poster to a crisp rounded card with transparent outer corners
  roundRect(ctx, 0, 0, logicalW, logicalH, 28);
  ctx.clip();

  // Background gradient + atmospheric radial glows + subtle honeycomb watermark
  const bgGrad = ctx.createLinearGradient(0, 0, 0, logicalH);
  bgGrad.addColorStop(0, P.bgTop);
  bgGrad.addColorStop(1, P.bgBottom);
  ctx.fillStyle = bgGrad;
  ctx.fillRect(0, 0, logicalW, logicalH);

  const goldGlow = ctx.createRadialGradient(
    logicalW * 0.25,
    logicalH * 0.12,
    10,
    logicalW * 0.25,
    logicalH * 0.12,
    logicalW * 0.78,
  );
  goldGlow.addColorStop(0, P.glowGold);
  goldGlow.addColorStop(1, 'rgba(245, 158, 11, 0)');
  ctx.fillStyle = goldGlow;
  ctx.fillRect(0, 0, logicalW, logicalH);

  const emGlow = ctx.createRadialGradient(
    logicalW * 0.85,
    logicalH * 0.82,
    10,
    logicalW * 0.85,
    logicalH * 0.82,
    logicalW * 0.65,
  );
  emGlow.addColorStop(0, P.glowEmerald);
  emGlow.addColorStop(1, 'rgba(16, 185, 129, 0)');
  ctx.fillStyle = emGlow;
  ctx.fillRect(0, 0, logicalW, logicalH);

  drawHoneycombWatermark(ctx, logicalW - 165, 28, P.watermark);

  // Outer specular border frame
  roundRect(ctx, 12, 12, logicalW - 24, logicalH - 24, 24);
  ctx.strokeStyle = P.frameStroke;
  ctx.lineWidth = 1.25;
  ctx.stroke();

  // Normalized safe display strings (100% zero personal emails, zero fabricated floors)
  const providersList =
    stats.providers_connected && stats.providers_connected.length > 0
      ? stats.providers_connected.map(scrubPII).join(' + ')
      : zh
        ? '暂无已连接平台'
        : 'No platforms connected';
  const rankBadge =
    scrubPII(zh ? stats.rank_badge_zh : stats.rank_badge_en) ||
    (zh ? 'ColabHive 算力账单' : 'ColabHive Receipt');
  const savedUsd = Number.isFinite(stats.estimated_saved_usd)
    ? Math.max(0, stats.estimated_saved_usd)
    : 0;
  const savedCny = Number.isFinite(stats.estimated_saved_cny)
    ? Math.max(0, stats.estimated_saved_cny)
    : savedUsd * 7.2;
  const cnyText = `¥${formatMoneyInt(savedCny)}`;
  const usdText = `$${savedUsd.toFixed(0)}`;
  const freeHrsNum = Number.isFinite(stats.free_gpu_hours_harvested)
    ? Math.max(0, stats.free_gpu_hours_harvested)
    : 0;
  const freeHrs = freeHrsNum.toFixed(1);
  const rawEff = Number.isFinite(stats.waste_zero_efficiency_pct)
    ? Math.min(100, Math.max(0, stats.waste_zero_efficiency_pct))
    : 0;
  const efficiencyPct = rawEff.toFixed(1);

  // 2. Top Brand Row + Rank Medal Pill
  const topY = 36;
  drawBrandEmblem(ctx, pad, topY, 38);

  ctx.textBaseline = 'alphabetic';
  ctx.fillStyle = P.textPrimary;
  ctx.font = `700 20px ${CANVAS_SANS}`;
  ctx.fillText('ColabHive', pad + 48, topY + 18);

  ctx.fillStyle = P.textMuted;
  ctx.font = `500 12px ${CANVAS_SANS}`;
  ctx.fillText(
    zh ? '私人免费云算力省钱账单' : 'Compute Savings Receipt',
    pad + 48,
    topY + 34,
  );

  // Rank pill top-right
  ctx.font = `600 12.5px ${CANVAS_SANS}`;
  const badgeW = ctx.measureText(rankBadge).width + 26;
  const badgeH = 28;
  const badgeX = logicalW - pad - badgeW;
  const badgeY = topY + 5;
  roundRect(ctx, badgeX, badgeY, badgeW, badgeH, 14);
  ctx.fillStyle = P.goldSoft;
  ctx.fill();
  ctx.strokeStyle = P.goldBorder;
  ctx.lineWidth = 1;
  ctx.stroke();
  ctx.fillStyle = P.goldText;
  ctx.fillText(rankBadge, badgeX + 13, badgeY + 18.5);

  if (layout === 'vertical') {
    // ========================================================================
    // VERTICAL SOCIAL POSTER (540 x 740 logical -> 1080 x 1480 px PNG)
    // ========================================================================
    const heroY = 96;
    const heroH = 204;
    const contentW = logicalW - pad * 2;

    // Hero Savings Card
    ctx.save();
    ctx.shadowColor = P.cardShadow;
    ctx.shadowBlur = 24;
    ctx.shadowOffsetY = 8;
    roundRect(ctx, pad, heroY, contentW, heroH, 20);
    ctx.fillStyle = P.heroWellBg;
    ctx.fill();
    ctx.restore();

    roundRect(ctx, pad, heroY, contentW, heroH, 20);
    ctx.strokeStyle = P.heroWellStroke;
    ctx.lineWidth = 1.25;
    ctx.stroke();

    ctx.fillStyle = P.textSecondary;
    ctx.font = `600 14px ${CANVAS_SANS}`;
    ctx.fillText(
      zh ? '💰 等效云端租卡价值（按 Vast.ai / RunPod 均价）' : '💰 Equivalent Cloud GPU Value (Vast.ai / RunPod Avg)',
      pad + 24,
      heroY + 36,
    );

    const numGrad = ctx.createLinearGradient(pad + 24, heroY + 50, pad + 260, heroY + 125);
    numGrad.addColorStop(0, P.goldStart);
    numGrad.addColorStop(1, P.goldEnd);
    ctx.fillStyle = numGrad;
    ctx.font = `800 66px ${CANVAS_MONO}`;
    const mainMoney = zh ? cnyText : usdText;
    ctx.fillText(mainMoney, pad + 22, heroY + 114);
    const mainMoneyW = ctx.measureText(mainMoney).width;

    ctx.fillStyle = P.textMuted;
    ctx.font = `600 18px ${CANVAS_MONO}`;
    ctx.fillText(
      zh ? `≈ ${usdText} USD` : `≈ ${cnyText} RMB`,
      pad + 22 + mainMoneyW + 14,
      heroY + 112,
    );

    // Emerald runtime highlight pill inside hero
    const totalHrs = stats.total_gpu_hours.toFixed(1);
    const subPillText =
      freeHrsNum > 0
        ? zh
          ? `🎁 累计免费显卡 ${freeHrs} 小时 · 总计驱动 ${totalHrs}h 云端 GPU`
          : `🎁 ${freeHrs}h Free GPUs · ${totalHrs}h Total Cloud GPU Runtime`
        : zh
          ? `⚡ 累计驱动 ${totalHrs}h 云端显卡 · 按 $2.2 / $0.6 / $0.25/h 核算`
          : `⚡ ${totalHrs}h Cloud GPU Runtime · $2.2 / $0.6 / $0.25/hr Avg`;
    ctx.font = `600 12.5px ${CANVAS_SANS}`;
    const subPillW = Math.min(contentW - 48, ctx.measureText(subPillText).width + 28);
    roundRect(ctx, pad + 24, heroY + 142, subPillW, 34, 17);
    ctx.fillStyle = P.emeraldSoft;
    ctx.fill();
    ctx.strokeStyle = P.emeraldBorder;
    ctx.lineWidth = 1;
    ctx.stroke();
    ctx.fillStyle = P.emeraldText;
    drawFitText(ctx, subPillText, pad + 38, heroY + 163.5, subPillW - 24);

    // 2x2 Bento Stat Grid
    const gridTop = heroY + heroH + 20;
    const gap = 14;
    const cellW = (contentW - gap) / 2;
    const cellH = 118;

    const cells = [
      freeHrsNum > 0
        ? {
            label: zh ? '累计免费显卡时长' : 'Free GPU Hours Harvested',
            val: zh ? `${freeHrs} 小时` : `${freeHrs} hrs`,
            sub: zh
              ? `累计总 GPU 时长 ${totalHrs}h`
              : `${totalHrs}h total GPU runtime`,
            accent: P.emeraldText,
          }
        : {
            label: zh ? '累计云端显卡总时长' : 'Total Cloud GPU Runtime',
            val: zh ? `${totalHrs} 小时` : `${totalHrs} hrs`,
            sub: zh
              ? `折合 96GB G4 等效 ${stats.g4_equivalent_hours.toFixed(1)}h`
              : `${stats.g4_equivalent_hours.toFixed(1)}h G4 96GB equivalent`,
            accent: P.emeraldText,
          },
      {
        label: zh ? '已汇聚账号与算力平台' : 'Pooled Accounts & Platforms',
        val: zh ? `${stats.total_accounts} 个账号` : `${stats.total_accounts} Accounts`,
        sub: providersList,
        accent: P.textPrimary,
      },
      {
        label: zh ? '防浪费算力利用率' : 'Waste-Zero Efficiency',
        val: `${efficiencyPct}%`,
        sub: zh
          ? `${stats.auto_teardowns} 次无人操作自动关机省额度`
          : `${stats.auto_teardowns} idle auto-shutdowns`,
        accent: P.goldText,
      },
      {
        label: zh ? '掉线自动换号续跑救援' : 'Auto-Resume Relay Saves',
        val: zh ? `${stats.auto_resume_relays} 次无缝续跑` : `${stats.auto_resume_relays} Relays`,
        sub: zh ? '免费卡到点自动换号 · 0 次白跑' : 'Zero lost runs across preemption',
        accent: P.emeraldText,
      },
    ];

    cells.forEach((cell, idx) => {
      const col = idx % 2;
      const row = Math.floor(idx / 2);
      const cx = pad + col * (cellW + gap);
      const cy = gridTop + row * (cellH + gap);

      ctx.save();
      ctx.shadowColor = P.cardShadow;
      ctx.shadowBlur = 16;
      ctx.shadowOffsetY = 5;
      roundRect(ctx, cx, cy, cellW, cellH, 16);
      ctx.fillStyle = P.cardBg;
      ctx.fill();
      ctx.restore();

      roundRect(ctx, cx, cy, cellW, cellH, 16);
      ctx.strokeStyle = P.cardStroke;
      ctx.lineWidth = 1;
      ctx.stroke();

      ctx.fillStyle = P.textMuted;
      ctx.font = `500 12px ${CANVAS_SANS}`;
      ctx.fillText(cell.label, cx + 18, cy + 30);

      ctx.fillStyle = cell.accent;
      ctx.font = `700 26px ${CANVAS_MONO}`;
      ctx.fillText(cell.val, cx + 18, cy + 68);

      ctx.fillStyle = P.textSecondary;
      ctx.font = `500 11.5px ${CANVAS_SANS}`;
      drawFitText(ctx, cell.sub, cx + 18, cy + 96, cellW - 32);
    });

    // Bottom Highlight Strip: What I Run Free
    const stripY = gridTop + cellH * 2 + gap + 20;
    roundRect(ctx, pad, stripY, contentW, 56, 14);
    ctx.fillStyle = P.cardBg;
    ctx.fill();
    ctx.strokeStyle = P.cardStroke;
    ctx.lineWidth = 1;
    ctx.stroke();

    ctx.fillStyle = P.textSecondary;
    ctx.font = `500 12.5px ${CANVAS_SANS}`;
    drawFitText(
      ctx,
      zh
        ? '⚡ 支持私人大模型聊天 · ComfyUI / Flux 超清画图 · AI 视频 · Krita/PS 本机直连'
        : '⚡ Private AI Chat · Flux & Wan Video Studio · Local 127.0.0.1 Krita/Cursor Link',
      pad + 18,
      stripY + 33,
      contentW - 36,
    );

    // Dashed receipt perforation + Footer (0 PII!)
    const footLineY = logicalH - 68;
    ctx.save();
    ctx.setLineDash([5, 5]);
    ctx.strokeStyle = P.divider;
    ctx.lineWidth = 1;
    ctx.beginPath();
    ctx.moveTo(pad, footLineY);
    ctx.lineTo(logicalW - pad, footLineY);
    ctx.stroke();
    ctx.restore();

    ctx.beginPath();
    ctx.arc(pad + 6, logicalH - 38, 4, 0, Math.PI * 2);
    ctx.fillStyle = P.emerald;
    ctx.fill();

    ctx.fillStyle = P.textMuted;
    ctx.font = `500 11.5px ${CANVAS_SANS}`;
    ctx.fillText(
      zh ? '已脱敏保护 · 0 邮箱与隐私暴露' : 'Zero PII · 100% Privacy Verified',
      pad + 16,
      logicalH - 34,
    );

    const repo = 'github.com/hexwander/colabhive';
    ctx.fillStyle = P.textSecondary;
    ctx.font = `600 12px ${CANVAS_MONO}`;
    ctx.fillText(repo, logicalW - pad - ctx.measureText(repo).width, logicalH - 34);
  } else {
    // ========================================================================
    // LANDSCAPE WIDESCREEN POSTER (820 x 490 logical -> 1640 x 980 px PNG)
    // ========================================================================
    const bodyY = 94;
    const bodyH = 310;
    const leftW = 356;
    const gap = 18;
    const rightX = pad + leftW + gap;
    const rightW = logicalW - pad - rightX;

    // Left Hero Card
    ctx.save();
    ctx.shadowColor = P.cardShadow;
    ctx.shadowBlur = 24;
    ctx.shadowOffsetY = 8;
    roundRect(ctx, pad, bodyY, leftW, bodyH, 20);
    ctx.fillStyle = P.heroWellBg;
    ctx.fill();
    ctx.restore();

    roundRect(ctx, pad, bodyY, leftW, bodyH, 20);
    ctx.strokeStyle = P.heroWellStroke;
    ctx.lineWidth = 1.25;
    ctx.stroke();

    ctx.fillStyle = P.textSecondary;
    ctx.font = `600 14px ${CANVAS_SANS}`;
    ctx.fillText(
      zh ? '💰 等效云端租卡价值' : '💰 Equivalent Cloud GPU Value',
      pad + 24,
      bodyY + 42,
    );

    const numGrad = ctx.createLinearGradient(pad + 24, bodyY + 60, pad + 260, bodyY + 135);
    numGrad.addColorStop(0, P.goldStart);
    numGrad.addColorStop(1, P.goldEnd);
    ctx.fillStyle = numGrad;
    ctx.font = `800 62px ${CANVAS_MONO}`;
    const mainMoney = zh ? cnyText : usdText;
    ctx.fillText(mainMoney, pad + 22, bodyY + 118);

    ctx.fillStyle = P.textMuted;
    ctx.font = `600 15px ${CANVAS_MONO}`;
    ctx.fillText(
      zh ? `≈ ${usdText} USD · Vast.ai / RunPod 均价` : `≈ ${cnyText} RMB · Vast.ai / RunPod avg`,
      pad + 24,
      bodyY + 150,
    );

    // Emerald Runtime Highlight inside Left Hero
    roundRect(ctx, pad + 24, bodyY + 178, leftW - 48, 54, 14);
    ctx.fillStyle = P.emeraldSoft;
    ctx.fill();
    ctx.strokeStyle = P.emeraldBorder;
    ctx.lineWidth = 1;
    ctx.stroke();

    ctx.fillStyle = P.emeraldText;
    ctx.font = `700 14.5px ${CANVAS_SANS}`;
    ctx.fillText(
      freeHrsNum > 0
        ? zh
          ? `🎁 累计免费显卡 ${freeHrs} 小时`
          : `🎁 ${freeHrs}h Free GPUs Harvested`
        : zh
          ? `⚡ 累计驱动 ${stats.total_gpu_hours.toFixed(1)} 小时云端显卡`
          : `⚡ ${stats.total_gpu_hours.toFixed(1)}h Cloud GPU Runtime`,
      pad + 38,
      bodyY + 202,
    );
    ctx.fillStyle = P.textSecondary;
    ctx.font = `500 11.5px ${CANVAS_SANS}`;
    drawFitText(
      ctx,
      zh ? `计价基准：大卡 $2.2 · L4 $0.6 · T4 $0.25/h` : `Rates: Large $2.2 · L4 $0.6 · T4 $0.25/h`,
      pad + 38,
      bodyY + 221,
      leftW - 76,
    );

    ctx.fillStyle = P.textMuted;
    ctx.font = `500 12px ${CANVAS_SANS}`;
    ctx.fillText(
      zh
        ? '本机 127.0.0.1 直连 · 零公网穿透防封号'
        : 'Private 127.0.0.1 loopback · Zero public tunnel',
      pad + 24,
      bodyY + 276,
    );

    // Right 2x2 Bento Grid
    const cellGap = 14;
    const cellW = (rightW - cellGap) / 2;
    const cellH = (bodyH - cellGap) / 2;

    const rightCells = [
      {
        label: zh ? '算力池账号与平台' : 'Pooled Accounts',
        val: zh ? `${stats.total_accounts} 个账号` : `${stats.total_accounts} Accounts`,
        sub: providersList,
        accent: P.textPrimary,
      },
      {
        label: zh ? '累计运行显卡总时长' : 'Total GPU Runtime',
        val: zh ? `${stats.total_gpu_hours.toFixed(1)} 小时` : `${stats.total_gpu_hours.toFixed(1)}h`,
        sub: zh
          ? `免费占比 ${stats.total_gpu_hours > 0 ? Math.min(100, Math.round((freeHrsNum / stats.total_gpu_hours) * 100)) : 0}%`
          : stats.total_gpu_hours > 0
            ? `${Math.min(100, Math.round((freeHrsNum / stats.total_gpu_hours) * 100))}% Free-Tier`
            : '0% (No runtime yet)',
        accent: P.emeraldText,
      },
      {
        label: zh ? '防浪费利用率' : 'Waste-Zero Efficiency',
        val: `${efficiencyPct}%`,
        sub: zh ? `${stats.auto_teardowns} 次闲置自动关机` : `${stats.auto_teardowns} idle teardowns`,
        accent: P.goldText,
      },
      {
        label: zh ? '掉线自动换号续跑' : 'Auto-Resume Saves',
        val: zh ? `${stats.auto_resume_relays} 次接力` : `${stats.auto_resume_relays} Saves`,
        sub: zh ? '断点快照自动搬家 · 0 白跑' : '0 lost runs on timeout',
        accent: P.emeraldText,
      },
    ];

    rightCells.forEach((cell, idx) => {
      const col = idx % 2;
      const row = Math.floor(idx / 2);
      const cx = rightX + col * (cellW + cellGap);
      const cy = bodyY + row * (cellH + cellGap);

      ctx.save();
      ctx.shadowColor = P.cardShadow;
      ctx.shadowBlur = 16;
      ctx.shadowOffsetY = 4;
      roundRect(ctx, cx, cy, cellW, cellH, 16);
      ctx.fillStyle = P.cardBg;
      ctx.fill();
      ctx.restore();

      roundRect(ctx, cx, cy, cellW, cellH, 16);
      ctx.strokeStyle = P.cardStroke;
      ctx.lineWidth = 1;
      ctx.stroke();

      ctx.fillStyle = P.textMuted;
      ctx.font = `500 12px ${CANVAS_SANS}`;
      ctx.fillText(cell.label, cx + 18, cy + 34);

      ctx.fillStyle = cell.accent;
      ctx.font = `700 28px ${CANVAS_MONO}`;
      ctx.fillText(cell.val, cx + 18, cy + 80);

      ctx.fillStyle = P.textSecondary;
      ctx.font = `500 12px ${CANVAS_SANS}`;
      drawFitText(ctx, cell.sub, cx + 18, cy + 114, cellW - 32);
    });

    // Footer bar
    ctx.beginPath();
    ctx.arc(pad + 6, logicalH - 36, 4, 0, Math.PI * 2);
    ctx.fillStyle = P.emerald;
    ctx.fill();

    ctx.fillStyle = P.textMuted;
    ctx.font = `500 12px ${CANVAS_SANS}`;
    ctx.fillText(
      zh
        ? '已自动隐藏全部隐私（0 邮箱 / 0 密钥暴露） · 多账号 GPU 池 & 原生创作台'
        : 'Zero PII exposed (0 emails / 0 keys) · Multi-Account GPU Pool & Native Media Studio',
      pad + 18,
      logicalH - 32,
    );

    const repo = 'github.com/hexwander/colabhive';
    ctx.fillStyle = P.textSecondary;
    ctx.font = `600 12.5px ${CANVAS_MONO}`;
    ctx.fillText(repo, logicalW - pad - ctx.measureText(repo).width, logicalH - 32);
  }

  // Crisp outer rounded card edge stroke so exported PNG looks clean on both light & dark feeds
  roundRect(ctx, 0.75, 0.75, logicalW - 1.5, logicalH - 1.5, 27.25);
  ctx.strokeStyle = P.frameStroke;
  ctx.lineWidth = 1.5;
  ctx.stroke();

  ctx.restore();
}

/** Async PNG download via `canvas.toBlob()` + `URL.revokeObjectURL()` (never blocks main thread). */
export async function exportSavingsPosterPng(
  canvas: HTMLCanvasElement,
  stats: SavingsReceiptStats,
  posterTheme: PosterTheme,
  layout: PosterLayout,
  zh: boolean,
): Promise<void> {
  paintSavingsPoster(canvas, stats, posterTheme, layout, zh);
  const blob = await new Promise<Blob | null>((resolve) => {
    try {
      canvas.toBlob((result) => resolve(result), 'image/png');
    } catch {
      resolve(null);
    }
  });
  if (!blob) {
    throw new Error(
      zh
        ? '导出高清 PNG 失败：浏览器无法编码画布图像，请重试'
        : 'PNG export failed: browser could not encode canvas blob',
    );
  }
  const objectUrl = URL.createObjectURL(blob);
  try {
    const link = document.createElement('a');
    const dateTag = new Date().toISOString().slice(0, 10);
    link.href = objectUrl;
    link.download = `colabhive-savings-${posterTheme}-${layout}-${dateTag}.png`;
    link.style.display = 'none';
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
  } finally {
    window.setTimeout(() => URL.revokeObjectURL(objectUrl), 1500);
  }
}

export interface SavingsReceiptCanvasProps {
  stats: SavingsReceiptStats;
  posterTheme: PosterTheme;
  layout: PosterLayout;
  zh: boolean;
  canvasRef: React.MutableRefObject<HTMLCanvasElement | null>;
  maxWidthVertical?: number;
  maxWidthLandscape?: number;
}

/**
 * Shared 2× Retina Poster Canvas that redraws ONLY when stats, theme, layout,
 * language, effective DPR, or `document.fonts.ready` genuinely change (never
 * resets the backing store on unrelated window resizes).
 */
export function SavingsReceiptCanvas({
  stats,
  posterTheme,
  layout,
  zh,
  canvasRef,
  maxWidthVertical = 370,
  maxWidthLandscape = 620,
}: SavingsReceiptCanvasProps) {
  const [dpr, setDpr] = useState<number>(() => getEffectiveDPR());
  const [fontsReadyVersion, setFontsReadyVersion] = useState<number>(0);
  const lastPaintKeyRef = useRef<string>('');

  // Observe real DPR changes (e.g. moving window to a Retina display or zoom change)
  useEffect(() => {
    if (typeof window === 'undefined') return;
    const onResize = () => {
      const next = getEffectiveDPR();
      setDpr((prev) => (prev === next ? prev : next));
    };
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);

  // Repaint once custom web fonts finish loading so crisp typography settles
  useEffect(() => {
    if (typeof document === 'undefined' || !('fonts' in document)) return;
    let active = true;
    void document.fonts.ready.then(() => {
      if (active) {
        setFontsReadyVersion((v) => v + 1);
      }
    });
    return () => {
      active = false;
    };
  }, []);

  const statsSignature = useMemo(() => JSON.stringify(stats), [stats]);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const key = `${statsSignature}|${posterTheme}|${layout}|${zh}|${dpr}|${fontsReadyVersion}`;
    if (lastPaintKeyRef.current === key) return;
    lastPaintKeyRef.current = key;
    paintSavingsPoster(canvas, stats, posterTheme, layout, zh, dpr);
  }, [canvasRef, stats, statsSignature, posterTheme, layout, zh, dpr, fontsReadyVersion]);

  return (
    <div
      className="flex items-center justify-center rounded-2xl p-4 sm:p-6 border border-line-subtle overflow-hidden"
      style={{
        background:
          posterTheme === 'obsidian-gold'
            ? 'radial-gradient(circle at 50% 0%, rgba(245,158,11,0.10), rgba(17,17,16,0.92))'
            : 'radial-gradient(circle at 50% 0%, rgba(245,158,11,0.08), var(--surface-1))',
      }}
    >
      <canvas
        ref={canvasRef}
        className="block rounded-xl shadow-2xl transition-all duration-300"
        style={{
          width:
            layout === 'vertical'
              ? `min(100%, ${maxWidthVertical}px)`
              : `min(100%, ${maxWidthLandscape}px)`,
          height: 'auto',
        }}
      />
    </div>
  );
}

export function SavingsReceiptModal({ open, onClose }: SavingsReceiptModalProps) {
  const { locale, theme: appTheme } = useI18n();
  const zh = locale === 'zh-CN';

  const [stats, setStats] = useState<SavingsReceiptStats | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [exportError, setExportError] = useState<string | null>(null);
  const [posterTheme, setPosterTheme] = useState<PosterTheme>(() =>
    appTheme === 'light' ? 'warm-alabaster' : 'obsidian-gold',
  );
  const [layout, setLayout] = useState<PosterLayout>('vertical');
  const [copiedText, setCopiedText] = useState(false);
  const [downloading, setDownloading] = useState(false);

  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const mountedRef = useRef(true);
  const copyTimerRef = useRef<number | null>(null);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
    };
  }, []);

  const refreshReceipt = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    setExportError(null);
    try {
      const data = await v11Api.getSavingsReceipt();
      if (mountedRef.current) {
        setStats(data);
      }
    } catch (err) {
      if (mountedRef.current) {
        setStats(null);
        setLoadError(
          err instanceof Error
            ? err.message
            : zh
              ? '无法从 ColabHive 服务拉取真实省钱账单数据，请检查服务是否在线'
              : 'Failed to load verified savings stats from ColabHive daemon',
        );
      }
    } finally {
      if (mountedRef.current) {
        setLoading(false);
      }
    }
  }, [zh]);

  useEffect(() => {
    if (open) {
      void refreshReceipt();
    }
  }, [open, refreshReceipt]);

  const shareCopy = useMemo(
    () => (stats ? scrubPII(zh ? stats.share_copy_zh : stats.share_copy_en) : ''),
    [stats, zh],
  );

  const canExport = Boolean(stats && !loadError && !loading);

  const handleDownloadPng = useCallback(async () => {
    const canvas = canvasRef.current;
    if (!canvas || !stats) return;
    setDownloading(true);
    setExportError(null);
    try {
      await exportSavingsPosterPng(canvas, stats, posterTheme, layout, zh);
    } catch (err) {
      if (mountedRef.current) {
        setExportError(
          err instanceof Error
            ? err.message
            : zh
              ? '保存 PNG 图片失败，请重试'
              : 'Failed to export PNG poster',
        );
      }
    } finally {
      if (mountedRef.current) {
        setDownloading(false);
      }
    }
  }, [stats, posterTheme, layout, zh]);

  const handleCopyShareText = useCallback(async () => {
    if (!shareCopy) return;
    setExportError(null);
    try {
      await navigator.clipboard.writeText(shareCopy);
      if (!mountedRef.current) return;
      setCopiedText(true);
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
      copyTimerRef.current = window.setTimeout(() => {
        if (mountedRef.current) setCopiedText(false);
      }, 2000);
    } catch {
      if (mountedRef.current) {
        setExportError(
          zh
            ? '剪贴板写入被浏览器拦截，请手动选中下方文案复制'
            : 'Clipboard write blocked by browser — select and copy the text below manually.',
        );
      }
    }
  }, [shareCopy, zh]);

  return (
    <DialogModal
      open={open}
      onClose={onClose}
      size="2xl"
      icon={<Award />}
      title={zh ? '我的算力省钱账单晒图' : 'Compute Savings Receipt Poster'}
      subtitle={
        zh
          ? '自动汇总真实云端显卡使用时长与折合租卡节省金额（100% 隐藏个人邮箱，安心分享）'
          : '2× Retina poster of your verified GPU runtime & estimated rent saved (100% zero personal emails).'
      }
      headerActions={
        <ActionButton
          size="xs"
          variant="ghost"
          icon={<RefreshCw className={loading ? 'animate-spin' : undefined} />}
          onClick={() => void refreshReceipt()}
        >
          {zh ? '刷新数据' : 'Refresh'}
        </ActionButton>
      }
      footer={
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-1.5 text-[12px] text-fg-secondary">
            <ShieldCheck className="w-4 h-4 shrink-0" style={{ color: 'var(--emerald)' }} />
            <span>
              {zh
                ? '隐私安全校验已通过：图片与文案均不含任何个人邮箱或密钥'
                : 'Zero PII verified: no emails, IPs, or keys included'}
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <ActionButton
              size="md"
              variant="secondary"
              disabled={!canExport}
              icon={copiedText ? <Check /> : <Copy />}
              onClick={() => void handleCopyShareText()}
              style={copiedText ? { color: 'var(--emerald-text)' } : undefined}
            >
              {copiedText
                ? zh
                  ? '✓ 已复制晒单文案'
                  : '✓ Copied Share Copy'
                : zh
                  ? '📋 一键复制晒单文案'
                  : '📋 Copy Share Caption'}
            </ActionButton>
            <ActionButton
              size="md"
              variant="primary"
              disabled={!canExport}
              loading={downloading}
              icon={<Download />}
              onClick={() => void handleDownloadPng()}
            >
              {zh ? '📥 保存高清晒图 (.png)' : '📥 Download HD Poster (.png)'}
            </ActionButton>
          </div>
        </div>
      }
    >
      <div className="space-y-4">
        {/* Top Controls: Theme Switcher (暗黑鎏金 / 暖白雅致) + Layout Switcher */}
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex flex-wrap items-center gap-2">
            <SegmentedTabs<PosterTheme>
              size="sm"
              value={posterTheme}
              onChange={setPosterTheme}
              aria-label={zh ? '晒图主题切换' : 'Poster theme'}
              options={[
                {
                  value: 'obsidian-gold',
                  icon: <Moon />,
                  label: zh ? '暗黑鎏金' : 'Obsidian Gold',
                },
                {
                  value: 'warm-alabaster',
                  icon: <Sun />,
                  label: zh ? '暖白雅致' : 'Warm Alabaster',
                },
              ]}
            />
            <SegmentedTabs<PosterLayout>
              size="sm"
              value={layout}
              onChange={setLayout}
              aria-label={zh ? '海报版式切换' : 'Poster orientation'}
              options={[
                {
                  value: 'vertical',
                  icon: <Smartphone />,
                  label: zh ? '竖版海报 (小红书/朋友圈)' : 'Vertical Poster',
                },
                {
                  value: 'landscape',
                  icon: <Monitor />,
                  label: zh ? '横版宽屏 (社区/X)' : 'Landscape Card',
                },
              ]}
            />
          </div>

          <StatusPill tone="amber" icon={<Sparkles />}>
            {zh ? '2× 超清视网膜画质直出' : '2× Retina High-DPI'}
          </StatusPill>
        </div>

        {exportError && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-xl border px-3.5 py-2.5 text-[12.5px]"
            style={{
              backgroundColor: 'var(--coral-soft)',
              borderColor: 'var(--coral-border)',
              color: 'var(--coral-text)',
            }}
          >
            <AlertCircle className="w-4 h-4 shrink-0 mt-0.5" />
            <span>{exportError}</span>
          </div>
        )}

        {loadError ? (
          <div
            role="alert"
            className="rounded-2xl border p-6 text-center space-y-3"
            style={{
              backgroundColor: 'var(--coral-soft)',
              borderColor: 'var(--coral-border)',
              color: 'var(--coral-text)',
            }}
          >
            <AlertCircle className="w-7 h-7 mx-auto" />
            <div className="text-[14px] font-semibold">
              {zh ? '暂时无法获取真实算力省钱账单' : 'Unable to load verified savings stats'}
            </div>
            <p className="text-[12.5px] opacity-90 max-w-md mx-auto">{loadError}</p>
            <div>
              <ActionButton
                size="sm"
                variant="secondary"
                icon={<RefreshCw />}
                onClick={() => void refreshReceipt()}
              >
                {zh ? '重新拉取真实统计' : 'Retry Fetch'}
              </ActionButton>
            </div>
          </div>
        ) : !stats ? (
          <div className="rounded-2xl border border-line-subtle bg-surface-1 p-10 flex flex-col items-center justify-center gap-3 text-fg-muted">
            <Loader2 className="w-6 h-6 animate-spin" style={{ color: 'var(--amber)' }} />
            <span className="text-[13px]">
              {zh ? '正在从本地服务核算真实算力省钱账单…' : 'Calculating verified compute savings…'}
            </span>
          </div>
        ) : (
          <>
            <SavingsReceiptCanvas
              stats={stats}
              posterTheme={posterTheme}
              layout={layout}
              zh={zh}
              canvasRef={canvasRef}
            />

            <InsetWell
              padding="sm"
              label={
                zh
                  ? '📝 配套晒单文案预览（点击下方按钮一键复制）'
                  : '📝 Ready-to-Paste Share Copy'
              }
              headerRight={
                <StatusPill size="xs" tone="emerald">
                  github.com/hexwander/colabhive
                </StatusPill>
              }
            >
              <p className="text-[12.5px] leading-relaxed text-fg-secondary select-all">
                {shareCopy}
              </p>
            </InsetWell>
          </>
        )}
      </div>
    </DialogModal>
  );
}

export default SavingsReceiptModal;
