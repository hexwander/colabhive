/**
 * ColabHive v3 layered UI primitives — "Liquid Glass, Spring Micro-Motion".
 *
 * Every primitive sits on one of the four elevation tiers defined in
 * `src/index.css`:
 *
 *   L0 canvas → L1 `InsetWell` / `SegmentedTabs` track → L2 `LayeredCard`
 *             → L3 `SlideOver` / `DialogModal`
 *
 * Nest them concentrically: a `LayeredCard` (16px radius) holds `InsetWell`s
 * (12px), which hold buttons and inputs (8px) and `StatusPill`s (full).
 *
 * Motion primitives: `AnimatedNumber` (rAF counter), the sliding glass pill in
 * `SegmentedTabs`, and the `LayeredCard` entrance (`staggerIndex`).
 *
 * Import from `'../components/ui'` or `'../components/ui/primitives'`.
 */
import React, { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import clsx, { type ClassValue } from 'clsx';
import { Loader2, X } from 'lucide-react';

// The onboarding launchpad lives in its own file (it imports nothing from this
// module, so there is no import cycle) and is re-exported here for convenience.
export {
  QuickStartLaunchpad,
  QuickStartProvider,
  openQuickStartGuide,
  QUICKSTART_ELEMENT_ID,
} from './QuickStartLaunchpad';
export type {
  QuickStartContextValue,
  QuickStartLaunchpadProps,
  QuickStartProviderProps,
} from './QuickStartLaunchpad';

/** Joins conditional class names (thin wrapper over `clsx`). */
export function cn(...inputs: ClassValue[]): string {
  return clsx(inputs);
}

/** True when the user asked the OS to minimise motion. */
function prefersReducedMotion(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches
  );
}

// ============================================================================
// Semantic tone
// ============================================================================

/**
 * Semantic colour intent. Aliases resolve to six real tones:
 * `neutral|default` · `accent` · `emerald|success` · `amber|warning` ·
 * `coral|rose|danger` · `sky|info`. Use tones for meaning (health, caution,
 * error), never for decoration.
 */
export type SemanticTone =
  | 'neutral'
  | 'default'
  | 'accent'
  | 'emerald'
  | 'success'
  | 'amber'
  | 'warning'
  | 'coral'
  | 'rose'
  | 'danger'
  | 'sky'
  | 'info';

type ResolvedTone = 'neutral' | 'accent' | 'emerald' | 'amber' | 'coral' | 'sky';

const TONE_ALIAS: Record<SemanticTone, ResolvedTone> = {
  neutral: 'neutral',
  default: 'neutral',
  accent: 'accent',
  emerald: 'emerald',
  success: 'emerald',
  amber: 'amber',
  warning: 'amber',
  coral: 'coral',
  rose: 'coral',
  danger: 'coral',
  sky: 'sky',
  info: 'sky',
};

/** Collapses a `SemanticTone` alias to the value used for `data-tone`. */
export function resolveTone(tone: SemanticTone = 'neutral'): ResolvedTone {
  return TONE_ALIAS[tone] ?? 'neutral';
}

// ============================================================================
// PageHeader
// ============================================================================

export interface PageHeaderProps {
  /** Page title. Plain words — no index prefixes. */
  title: React.ReactNode;
  /** One calm sentence under the title. */
  subtitle?: React.ReactNode;
  /** Small context line above the title (e.g. parent section). */
  eyebrow?: React.ReactNode;
  /** Inline element next to the title, typically a `StatusPill`. */
  badge?: React.ReactNode;
  /** Right-aligned action cluster (buttons, tabs). */
  actions?: React.ReactNode;
  /** Optional row under the header (filters, `SegmentedTabs`). */
  children?: React.ReactNode;
  className?: string;
}

/** Page-level header: title, optional subtitle, right-aligned actions. */
export function PageHeader({
  title,
  subtitle,
  eyebrow,
  badge,
  actions,
  children,
  className,
}: PageHeaderProps) {
  return (
    <header className={cn('flex flex-col gap-4', className)}>
      <div className="flex flex-col sm:flex-row sm:items-start sm:justify-between gap-4">
        <div className="min-w-0 space-y-1.5">
          {eyebrow != null && (
            <div className="text-[11.5px] font-medium text-fg-muted">{eyebrow}</div>
          )}
          <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1.5">
            <h1 className="ui-title text-[22px] sm:text-[24px] font-semibold">{title}</h1>
            {badge}
          </div>
          {subtitle != null && (
            <p className="max-w-3xl text-[13.5px] leading-relaxed text-fg-secondary">{subtitle}</p>
          )}
        </div>
        {actions != null && (
          <div className="flex flex-wrap items-center gap-2 shrink-0 sm:justify-end">{actions}</div>
        )}
      </div>
      {children}
    </header>
  );
}

// ============================================================================
// LayeredCard (L2)
// ============================================================================

export interface LayeredCardProps extends Omit<React.HTMLAttributes<HTMLDivElement>, 'title'> {
  title?: React.ReactNode;
  subtitle?: React.ReactNode;
  /** Leading icon in the header bar. */
  icon?: React.ReactNode;
  /** Right side of the header bar. */
  headerRight?: React.ReactNode;
  /** Alias for `headerRight`. */
  actions?: React.ReactNode;
  /** Footer bar under a hairline divider. */
  footer?: React.ReactNode;
  /** Body padding. Default `'md'` → `p-5 sm:p-6`. */
  padding?: 'none' | 'sm' | 'md' | 'lg';
  /** Hover elevation + pointer cursor. With `onClick` it is keyboard-activatable. */
  interactive?: boolean;
  /** Quieter variant: half-tone fill and no ambient shadow. */
  subtle?: boolean;
  /** Fade-up entrance on mount (360ms). Default `true`. */
  animateIn?: boolean;
  /** Position in a staggered group: delays the entrance by `staggerIndex × 45ms`. */
  staggerIndex?: number;
  children: React.ReactNode;
}

const CARD_BODY_PADDING: Record<NonNullable<LayeredCardProps['padding']>, string> = {
  none: '',
  sm: 'p-4',
  md: 'p-5 sm:p-6',
  lg: 'p-6 sm:p-8',
};

const CARD_BAR_PADDING: Record<NonNullable<LayeredCardProps['padding']>, string> = {
  none: 'px-5',
  sm: 'px-4',
  md: 'px-5 sm:px-6',
  lg: 'px-6 sm:px-8',
};

/** Delay between consecutive items of a staggered entrance (matches `.ui-stagger`). */
const STAGGER_STEP_MS = 45;

/**
 * Elevated L2 liquid glass card: 16px radius, specular top edge, tinted ambient
 * shadow, and a gentle fade-up entrance. Optional header and footer bars are
 * separated from the body by hairline dividers.
 */
export const LayeredCard = React.forwardRef<HTMLDivElement, LayeredCardProps>(function LayeredCard(
  {
    title,
    subtitle,
    icon,
    headerRight,
    actions,
    footer,
    padding = 'md',
    interactive = false,
    subtle = false,
    animateIn = true,
    staggerIndex,
    className,
    style,
    children,
    onClick,
    onKeyDown,
    role,
    tabIndex,
    ...rest
  },
  ref,
) {
  const right = headerRight ?? actions;
  const hasHeader = title != null || subtitle != null || icon != null || right != null;
  const actsAsButton = interactive && onClick != null && role == null;

  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    onKeyDown?.(e);
    if (!actsAsButton || e.defaultPrevented || e.target !== e.currentTarget) return;
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      e.currentTarget.click();
    }
  };

  const enterDelayMs =
    animateIn && staggerIndex != null && staggerIndex > 0
      ? Math.min(Math.round(staggerIndex), 12) * STAGGER_STEP_MS
      : 0;
  const mergedStyle: React.CSSProperties | undefined =
    enterDelayMs > 0
      ? ({ ...style, '--ui-enter-delay': `${enterDelayMs}ms` } as React.CSSProperties)
      : style;

  return (
    <div
      ref={ref}
      className={cn(
        'ui-card flex flex-col',
        animateIn && 'ui-enter',
        interactive && 'ui-card-interactive',
        subtle && 'ui-card-subtle',
        className,
      )}
      style={mergedStyle}
      onClick={onClick}
      onKeyDown={handleKeyDown}
      role={actsAsButton ? 'button' : role}
      tabIndex={actsAsButton ? (tabIndex ?? 0) : tabIndex}
      {...rest}
    >
      {hasHeader && (
        <div
          className={cn(
            'flex items-start justify-between gap-3 py-4 border-b border-line-faint',
            CARD_BAR_PADDING[padding],
          )}
        >
          <div className="flex items-start gap-2.5 min-w-0">
            {icon != null && (
              <span className="mt-px shrink-0 text-fg-secondary [&>svg]:w-4 [&>svg]:h-4">
                {icon}
              </span>
            )}
            <div className="min-w-0">
              {title != null && (
                <h3 className="text-[14px] font-semibold leading-snug text-fg">{title}</h3>
              )}
              {subtitle != null && (
                <p className="mt-0.5 text-[12.5px] leading-relaxed text-fg-secondary">{subtitle}</p>
              )}
            </div>
          </div>
          {right != null && <div className="flex items-center gap-2 shrink-0">{right}</div>}
        </div>
      )}

      <div className={cn('flex-1 min-w-0', CARD_BODY_PADDING[padding])}>{children}</div>

      {footer != null && (
        <div className={cn('py-3.5 border-t border-line-faint', CARD_BAR_PADDING[padding])}>
          {footer}
        </div>
      )}
    </div>
  );
});

// ============================================================================
// InsetWell (L1)
// ============================================================================

export interface InsetWellProps extends Omit<React.HTMLAttributes<HTMLDivElement>, 'title'> {
  /** Quiet caption above the content. */
  label?: React.ReactNode;
  /** Alias for `label`. */
  title?: React.ReactNode;
  /** Right side of the caption row. */
  headerRight?: React.ReactNode;
  /** Default `'md'` → `p-3.5 sm:p-4`. */
  padding?: 'none' | 'xs' | 'sm' | 'md' | 'lg';
  children: React.ReactNode;
}

const WELL_PADDING: Record<NonNullable<InsetWellProps['padding']>, string> = {
  none: '',
  xs: 'p-2',
  sm: 'p-3',
  md: 'p-3.5 sm:p-4',
  lg: 'p-5',
};

/**
 * Recessed L1 well: 12px radius (concentric inside a `LayeredCard`), inner
 * shadow, hairline border. Groups related controls or read-only detail.
 */
export const InsetWell = React.forwardRef<HTMLDivElement, InsetWellProps>(function InsetWell(
  { label, title, headerRight, padding = 'md', className, children, ...rest },
  ref,
) {
  const caption = label ?? title;
  const hasHeader = caption != null || headerRight != null;

  return (
    <div ref={ref} className={cn('ui-well min-w-0', WELL_PADDING[padding], className)} {...rest}>
      {hasHeader && (
        <div
          className={cn(
            'flex items-center justify-between gap-2',
            padding === 'none' ? 'px-3.5 pt-3 pb-2.5' : 'mb-3',
          )}
        >
          <div className="min-w-0 text-[11.5px] font-medium text-fg-muted">{caption}</div>
          {headerRight != null && (
            <div className="flex items-center gap-2 shrink-0">{headerRight}</div>
          )}
        </div>
      )}
      {children}
    </div>
  );
});

// ============================================================================
// SegmentedTabs
// ============================================================================

export interface SegmentedTabItem<T extends string = string> {
  /** Option key. Provide `value` or `id`. */
  value?: T;
  /** Alias for `value`. */
  id?: T;
  label: React.ReactNode;
  icon?: React.ReactNode;
  /** Free-form trailing element (e.g. a `StatusPill`). */
  badge?: React.ReactNode;
  /** Compact trailing count. */
  count?: React.ReactNode;
  disabled?: boolean;
}

export interface SegmentedTabsProps<T extends string = string> {
  options?: SegmentedTabItem<T>[];
  /** Alias for `options`. */
  items?: SegmentedTabItem<T>[];
  value: T;
  onChange: (value: T) => void;
  /** Default `'md'`. */
  size?: 'sm' | 'md';
  /** Stretch to the container with equal-width tabs. */
  fullWidth?: boolean;
  className?: string;
  /** Accessible name for the tab list. */
  'aria-label'?: string;
}

interface SegThumbRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/**
 * Segmented control: a recessed L1 track with a glass pill that slides behind
 * the active tab. The pill is measured from the active tab's real box, so it
 * follows label, locale and container changes. Arrow keys, Home and End move
 * the selection.
 */
export function SegmentedTabs<T extends string = string>({
  options,
  items,
  value,
  onChange,
  size = 'md',
  fullWidth = false,
  className,
  'aria-label': ariaLabel,
}: SegmentedTabsProps<T>) {
  const list = options ?? items ?? [];
  const keyOf = (item: SegmentedTabItem<T>) => (item.value ?? item.id) as T;

  const trackRef = useRef<HTMLDivElement | null>(null);
  const [thumb, setThumb] = useState<SegThumbRect | null>(null);
  // False until the first measured position has been painted: the pill snaps
  // into place on mount and only glides on later changes.
  const [thumbAnimated, setThumbAnimated] = useState(false);

  const measureThumb = useCallback(() => {
    const track = trackRef.current;
    if (!track) return;
    const active = track.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]');
    // A zero-width tab means the control is hidden (`display: none` ancestor).
    const next: SegThumbRect | null =
      active && active.offsetWidth > 0
        ? {
            x: active.offsetLeft,
            y: active.offsetTop,
            w: active.offsetWidth,
            h: active.offsetHeight,
          }
        : null;
    setThumb((prev) => {
      if (prev === next) return prev;
      if (
        prev &&
        next &&
        prev.x === next.x &&
        prev.y === next.y &&
        prev.w === next.w &&
        prev.h === next.h
      ) {
        return prev;
      }
      return next;
    });
  }, []);

  // Re-measure after every render: the selection, a label, a count or the
  // locale may have changed. `setThumb` bails out when the box is unchanged.
  useLayoutEffect(() => {
    measureThumb();
  });

  // Container resizes, late web-font swaps and becoming visible again.
  useEffect(() => {
    const track = trackRef.current;
    if (!track || typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(() => measureThumb());
    observer.observe(track);
    track.querySelectorAll<HTMLElement>('[role="tab"]').forEach((tab) => observer.observe(tab));
    return () => observer.disconnect();
  }, [measureThumb, list.length]);

  const thumbReady = thumb !== null;
  useEffect(() => {
    if (!thumbReady) {
      setThumbAnimated(false);
      return;
    }
    const frame = window.requestAnimationFrame(() => setThumbAnimated(true));
    return () => window.cancelAnimationFrame(frame);
  }, [thumbReady]);

  const handleKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const enabled = list.filter((item) => !item.disabled);
    if (enabled.length === 0) return;
    const current = enabled.findIndex((item) => keyOf(item) === value);
    let next = current;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = (current + 1) % enabled.length;
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp')
      next = (current - 1 + enabled.length) % enabled.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = enabled.length - 1;
    else return;

    e.preventDefault();
    const nextKey = keyOf(enabled[next]);
    onChange(nextKey);
    const tabs = e.currentTarget.querySelectorAll<HTMLButtonElement>('[role="tab"]');
    tabs.forEach((tab) => {
      if (tab.dataset.key === nextKey) tab.focus();
    });
  };

  return (
    <div
      ref={trackRef}
      role="tablist"
      aria-label={ariaLabel}
      data-thumb={thumbReady ? 'ready' : undefined}
      onKeyDown={handleKeyDown}
      className={cn('ui-seg', size === 'sm' && 'ui-seg-sm', fullWidth && 'ui-seg-full', className)}
    >
      <span
        aria-hidden="true"
        className="ui-seg-thumb"
        data-ready={thumbReady}
        data-animate={thumbAnimated}
        style={
          thumb
            ? {
                width: thumb.w,
                height: thumb.h,
                transform: `translate3d(${thumb.x}px, ${thumb.y}px, 0)`,
              }
            : undefined
        }
      />
      {list.map((item) => {
        const key = keyOf(item);
        const selected = key === value;
        return (
          <button
            key={key}
            type="button"
            role="tab"
            data-key={key}
            aria-selected={selected}
            tabIndex={selected ? 0 : -1}
            disabled={item.disabled}
            onClick={() => {
              if (!selected) onChange(key);
            }}
            className="ui-seg-item relative z-10"
          >
            {item.icon != null && (
              <span className="shrink-0 [&>svg]:w-3.5 [&>svg]:h-3.5">{item.icon}</span>
            )}
            <span className="truncate">{item.label}</span>
            {item.count != null && (
              <span className="ch-num text-[10.5px] text-fg-muted">{item.count}</span>
            )}
            {item.badge}
          </button>
        );
      })}
    </div>
  );
}

// ============================================================================
// StatusPill
// ============================================================================

export interface StatusPillProps {
  children?: React.ReactNode;
  /** Alias for `children`. */
  label?: React.ReactNode;
  /** Default `'neutral'`. */
  tone?: SemanticTone;
  /** Leading 6px indicator dot. */
  dot?: boolean;
  /** Gentle ring pulse on the dot. Reserve for genuinely live state. */
  pulse?: boolean;
  icon?: React.ReactNode;
  /** Default `'sm'`. */
  size?: 'xs' | 'sm' | 'md';
  /** Monospace tabular text (IDs, counts, versions). */
  mono?: boolean;
  className?: string;
  title?: string;
}

/** Rounded status pill: translucent tone fill, delicate border, optional dot. */
export function StatusPill({
  children,
  label,
  tone = 'neutral',
  dot = false,
  pulse = false,
  icon,
  size = 'sm',
  mono = false,
  className,
  title,
}: StatusPillProps) {
  const content = children ?? label;
  return (
    <span
      data-tone={resolveTone(tone)}
      title={title}
      className={cn(
        'ui-pill',
        size === 'xs' && 'ui-pill-xs',
        size === 'md' && 'ui-pill-md',
        mono && 'ch-num',
        className,
      )}
    >
      {(dot || pulse) && <span className={cn('ui-dot', pulse && 'ui-dot-pulse')} />}
      {icon != null && <span className="shrink-0 [&>svg]:w-3 [&>svg]:h-3">{icon}</span>}
      {content}
    </span>
  );
}

// ============================================================================
// AnimatedNumber
// ============================================================================

export interface AnimatedNumberProps {
  value: number;
  /** Fixed fraction digits. Default `0`. Ignored when `format` is given. */
  decimals?: number;
  /** Custom formatter for the in-flight value (e.g. `formatCU`). */
  format?: (value: number) => string;
  prefix?: React.ReactNode;
  suffix?: React.ReactNode;
  /** Default `450`. */
  durationMs?: number;
  /** Default `true`: a subtle `0.88 × value → value` glide on mount. */
  animateOnMount?: boolean;
  className?: string;
}

function formatFixed(value: number, decimals: number): string {
  return value.toLocaleString('en-US', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

/**
 * Tabular numeral that glides to each new value with `requestAnimationFrame`
 * and cubic-out easing. Snaps immediately under `prefers-reduced-motion`, and
 * finishes in one step when the tab was in the background.
 */
export function AnimatedNumber({
  value,
  decimals = 0,
  format,
  prefix,
  suffix,
  durationMs = 450,
  animateOnMount = true,
  className,
}: AnimatedNumberProps) {
  const target = Number.isFinite(value) ? value : 0;
  const [display, setDisplay] = useState<number>(() =>
    animateOnMount && durationMs > 0 && !prefersReducedMotion() ? target * 0.88 : target,
  );
  // Mirrors `display` so a new target always starts from what is on screen.
  const displayRef = useRef(display);

  useEffect(() => {
    const from = displayRef.current;
    if (from === target) return;

    if (durationMs <= 0 || prefersReducedMotion()) {
      displayRef.current = target;
      setDisplay(target);
      return;
    }

    let frame = 0;
    const startedAt = performance.now();
    const tick = (now: number) => {
      const progress = Math.min(1, Math.max(0, (now - startedAt) / durationMs));
      const eased = 1 - Math.pow(1 - progress, 3);
      const next = progress >= 1 ? target : from + (target - from) * eased;
      displayRef.current = next;
      setDisplay(next);
      if (progress < 1) frame = window.requestAnimationFrame(tick);
    };
    frame = window.requestAnimationFrame(tick);
    return () => window.cancelAnimationFrame(frame);
  }, [target, durationMs]);

  return (
    <span className={cn('ch-num tabular-nums', className)}>
      {prefix}
      {format ? format(display) : formatFixed(display, decimals)}
      {suffix}
    </span>
  );
}

// ============================================================================
// MetricBlock
// ============================================================================

export interface MetricBlockProps {
  label: React.ReactNode;
  /**
   * A `number` glides to each new value (see `AnimatedNumber`); any other node
   * renders as-is. Pass `<AnimatedNumber format={…} />` for custom formatting.
   */
  value: React.ReactNode;
  /** Fraction digits for a numeric `value`. Default: the digits `value` has (max 3). */
  valueDecimals?: number;
  /** Muted unit rendered inline after the value. */
  unit?: React.ReactNode;
  /** Quiet line under the value. */
  subtext?: React.ReactNode;
  /** Alias for `subtext`. */
  hint?: React.ReactNode;
  /** Trailing element on the label row, typically a `StatusPill`. */
  badge?: React.ReactNode;
  icon?: React.ReactNode;
  /** Colours the numeral. Default `'neutral'` (primary text). */
  tone?: SemanticTone;
  /** Numeral size. Default `'md'`. */
  size?: 'sm' | 'md' | 'lg' | 'xl';
  /** Wrap the block in an `InsetWell`. */
  inWell?: boolean;
  className?: string;
}

const METRIC_VALUE_SIZE: Record<NonNullable<MetricBlockProps['size']>, string> = {
  sm: 'text-[18px] leading-6',
  md: 'text-[24px] leading-8',
  lg: 'text-[32px] leading-10',
  xl: 'text-[40px] leading-[48px]',
};

/** Fraction digits a number is written with, capped at 3 (`12.5` → 1). */
function fractionDigitsOf(value: number): number {
  if (!Number.isFinite(value) || Number.isInteger(value)) return 0;
  const text = String(value);
  const dot = text.indexOf('.');
  if (dot < 0 || text.includes('e')) return 2;
  return Math.min(3, text.length - dot - 1);
}

/** Telemetry numeral: muted label, crisp tabular value with inline unit, quiet subtext. */
export function MetricBlock({
  label,
  value,
  valueDecimals,
  unit,
  subtext,
  hint,
  badge,
  icon,
  tone = 'neutral',
  size = 'md',
  inWell = false,
  className,
}: MetricBlockProps) {
  const resolved = resolveTone(tone);
  const note = subtext ?? hint;
  const renderedValue =
    typeof value === 'number' ? (
      <AnimatedNumber value={value} decimals={valueDecimals ?? fractionDigitsOf(value)} />
    ) : (
      value
    );

  const body = (
    <div data-tone={resolved} className={cn('min-w-0', !inWell && className)}>
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5 min-w-0 text-[12px] font-medium text-fg-secondary">
          {icon != null && (
            <span className="shrink-0 text-fg-muted [&>svg]:w-3.5 [&>svg]:h-3.5">{icon}</span>
          )}
          <span className="truncate">{label}</span>
        </div>
        {badge != null && <div className="shrink-0">{badge}</div>}
      </div>
      <div className="mt-1.5 flex flex-wrap items-baseline gap-x-1.5">
        <span
          className={cn('ch-num font-semibold tracking-tight', METRIC_VALUE_SIZE[size])}
          style={{ color: resolved === 'neutral' ? 'var(--text-primary)' : 'var(--tone-fg)' }}
        >
          {renderedValue}
        </span>
        {unit != null && <span className="text-[12px] font-medium text-fg-muted">{unit}</span>}
      </div>
      {note != null && (
        <div className="mt-1 text-[11.5px] leading-snug text-fg-muted">{note}</div>
      )}
    </div>
  );

  if (!inWell) return body;
  return <InsetWell className={className}>{body}</InsetWell>;
}

// ============================================================================
// v4 Stardust Signature Primitives:
//   CircularQuotaRing · SegmentedQuotaBar · OrbitalCompassGraphic
// ============================================================================

export type QuotaRingTone = 'emerald' | 'coral' | 'amber' | 'sky' | 'neutral';

export interface CircularQuotaRingProps {
  /** Quota percentage from 0 to 100. */
  value: number;
  /** SVG diameter in pixels. Default `44`. */
  size?: number;
  /** Ring stroke thickness in pixels. Default `3.5`. */
  strokeWidth?: number;
  /** Semantic colour tone. Auto-derived from `value` when omitted. */
  tone?: QuotaRingTone;
  /** Optional custom center label (defaults to `{Math.round(value)}%`). */
  label?: string;
  /** Show centered percentage text. Defaults to `size >= 28`. */
  showValue?: boolean;
  className?: string;
}

const QUOTA_TONE_COLOR: Record<QuotaRingTone, string> = {
  emerald: 'var(--emerald)',
  coral: 'var(--coral)',
  amber: 'var(--amber)',
  sky: 'var(--sky)',
  neutral: 'var(--text-muted)',
};

const QUOTA_TONE_SOFT: Record<QuotaRingTone, string> = {
  emerald: 'var(--emerald-soft)',
  coral: 'var(--coral-soft)',
  amber: 'var(--amber-soft)',
  sky: 'var(--sky-soft)',
  neutral: 'var(--neutral-soft)',
};

function autoQuotaTone(pct: number): 'emerald' | 'amber' | 'coral' {
  if (pct <= 15) return 'coral';
  if (pct <= 35) return 'amber';
  return 'emerald';
}

/**
 * Clean Stardust SVG circular quota gauge: subtle neutral track circle, smooth
 * animated stroke arc from 12 o'clock, and crisp tabular percentage centered
 * inside (or compact radial icon when `size < 28`).
 */
export function CircularQuotaRing({
  value,
  size = 44,
  strokeWidth = 3.5,
  tone,
  label,
  showValue,
  className,
}: CircularQuotaRingProps) {
  const clamped = Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0));
  const resolvedTone: QuotaRingTone = tone ?? autoQuotaTone(clamped);
  const strokeColor = QUOTA_TONE_COLOR[resolvedTone];

  const safeSize = Math.max(12, size);
  const safeStroke = Math.min(strokeWidth, safeSize / 3);
  const radius = (safeSize - safeStroke) / 2;
  const circumference = 2 * Math.PI * radius;
  const dashOffset = circumference * (1 - clamped / 100);
  const displayValue = showValue ?? safeSize >= 28;
  const centerText = label ?? `${Math.round(clamped)}%`;
  const fontSize = safeSize >= 42 ? 11.5 : safeSize >= 34 ? 10.5 : Math.max(9, Math.round(safeSize * 0.27));

  return (
    <span
      role="meter"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(clamped)}
      aria-label={centerText}
      className={cn('relative inline-flex items-center justify-center shrink-0 select-none', className)}
      style={{ width: safeSize, height: safeSize }}
    >
      <svg
        width={safeSize}
        height={safeSize}
        viewBox={`0 0 ${safeSize} ${safeSize}`}
        className="block -rotate-90"
        aria-hidden="true"
      >
        <circle
          cx={safeSize / 2}
          cy={safeSize / 2}
          r={radius}
          fill="none"
          stroke="var(--quota-track)"
          strokeWidth={safeStroke}
        />
        {clamped > 0 && (
          <circle
            cx={safeSize / 2}
            cy={safeSize / 2}
            r={radius}
            fill="none"
            stroke={strokeColor}
            strokeWidth={safeStroke}
            strokeLinecap="round"
            strokeDasharray={circumference}
            strokeDashoffset={dashOffset}
            style={{
              transition:
                'stroke-dashoffset 560ms cubic-bezier(0.22, 1, 0.36, 1), stroke 220ms ease',
            }}
          />
        )}
      </svg>
      {displayValue && (
        <span
          className="ch-num absolute inset-0 flex items-center justify-center font-bold tracking-tight text-fg leading-none"
          style={{ fontSize: `${fontSize}px` }}
        >
          {centerText}
        </span>
      )}
    </span>
  );
}

export interface SegmentedQuotaBarProps {
  /** Current quota percentage from 0 to 100. */
  value: number;
  /** Number of discrete micro-segments. Default `22`. */
  segments?: number;
  /** Semantic colour tone. Auto-derived from `value` when omitted. */
  tone?: 'emerald' | 'coral' | 'amber' | 'sky';
  /**
   * Optional projected / post-refill percentage (0–100) rendered as hollow
   * outline micro-ticks after solid ticks (e.g. `45% -> 100%` `■■■■■■□□□□□`).
   */
  projectedValue?: number;
  /** Optional vertical pace-reference marker position (0–100). */
  markerValue?: number;
  className?: string;
}

/**
 * Discrete multi-tick Stardust progress bar: renders crisp `h-2.5 rounded-[3px]`
 * micro-segments with solid fill, optional hollow outline projected ticks, and
 * optional pace-reference marker.
 */
export function SegmentedQuotaBar({
  value,
  segments = 22,
  tone,
  projectedValue,
  markerValue,
  className,
}: SegmentedQuotaBarProps) {
  const total = Math.max(4, Math.min(60, Math.round(segments)));
  const clamped = Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0));
  const resolvedTone = tone ?? autoQuotaTone(clamped);
  const solidColor = QUOTA_TONE_COLOR[resolvedTone];
  const softColor = QUOTA_TONE_SOFT[resolvedTone];

  const filledCount =
    clamped <= 0 ? 0 : Math.min(total, Math.max(1, Math.round((clamped / 100) * total)));

  const projClamped =
    projectedValue != null && Number.isFinite(projectedValue)
      ? Math.max(clamped, Math.min(100, projectedValue))
      : null;
  const projectedCount =
    projClamped != null && projClamped > clamped
      ? Math.min(total, Math.max(filledCount, Math.round((projClamped / 100) * total)))
      : filledCount;

  const markerPct =
    markerValue != null && Number.isFinite(markerValue)
      ? Math.max(0, Math.min(100, markerValue))
      : null;

  return (
    <div
      role="progressbar"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={Math.round(clamped)}
      className={cn('relative flex items-center gap-[3px] w-full select-none', className)}
    >
      {Array.from({ length: total }, (_, idx) => {
        const isFilled = idx < filledCount;
        const isProjected = !isFilled && idx < projectedCount;

        return (
          <span
            key={idx}
            aria-hidden="true"
            className="h-2.5 flex-1 min-w-[3px] rounded-[3px] transition-[background-color,border-color,opacity,transform] duration-300"
            style={{
              transitionDelay: `${idx * 10}ms`,
              backgroundColor: isFilled
                ? solidColor
                : isProjected
                  ? softColor
                  : 'var(--quota-track)',
              border: isProjected
                ? `1.5px solid color-mix(in srgb, ${solidColor} 68%, transparent)`
                : '1px solid transparent',
            }}
          />
        );
      })}

      {markerPct !== null && (
        <span
          aria-hidden="true"
          className="pointer-events-none absolute top-1/2 -translate-y-1/2 -translate-x-1/2 w-[1.5px] h-4 rounded-full"
          style={{
            left: `${markerPct}%`,
            backgroundColor: 'var(--text-secondary)',
            opacity: 0.55,
          }}
        />
      )}
    </div>
  );
}

export interface OrbitalCompassGraphicProps {
  /** SVG width & height in pixels. Default `96`. */
  size?: number;
  /** Number of healthy / abundant pool tracks or accounts. */
  healthyCount: number;
  /** Number of warning / expiring / low-quota tracks or accounts. */
  warningCount: number;
  /** Number of actively running sessions or GPU tracks. */
  activeCount: number;
  className?: string;
}

function polarPoint(cx: number, cy: number, r: number, angleDeg: number): { x: number; y: number } {
  const rad = ((angleDeg - 90) * Math.PI) / 180;
  return {
    x: cx + r * Math.cos(rad),
    y: cy + r * Math.sin(rad),
  };
}

function describeOrbitArc(
  cx: number,
  cy: number,
  r: number,
  startDeg: number,
  endDeg: number,
): string {
  const start = polarPoint(cx, cy, r, endDeg);
  const end = polarPoint(cx, cy, r, startDeg);
  const largeArc = endDeg - startDeg <= 180 ? '0' : '1';
  return `M ${start.x.toFixed(2)} ${start.y.toFixed(2)} A ${r} ${r} 0 ${largeArc} 0 ${end.x.toFixed(2)} ${end.y.toFixed(2)}`;
}

/**
 * Warm Neutral Orbital Compass SVG radar: concentric faint rings,
 * emerald/sky/coral orbital arc sweeps, and glowing orbital nodes.
 */
export function OrbitalCompassGraphic({
  size = 96,
  healthyCount,
  warningCount,
  activeCount,
  className,
}: OrbitalCompassGraphicProps) {
  const gradId = useId();
  const totalPool = Math.max(1, healthyCount + warningCount);
  const healthyRatio = Math.max(0.22, Math.min(0.92, healthyCount / totalPool));

  // Outer emerald orbit sweep (clockwise from -30deg)
  const outerStart = -28;
  const outerEnd = outerStart + Math.round(healthyRatio * 235);
  const outerTip = polarPoint(50, 50, 40, outerEnd);
  const outerTail = polarPoint(50, 50, 40, outerStart);

  // Middle active orbit sweep
  const activeSpan = activeCount > 0 ? Math.min(200, 75 + activeCount * 32) : 64;
  const midStart = 148;
  const midEnd = midStart + activeSpan;
  const midTip = polarPoint(50, 50, 29, midEnd);

  // Inner warning / balance orbit
  const hasWarning = warningCount > 0;
  const innerStart = hasWarning ? 210 : 52;
  const innerEnd = innerStart + (hasWarning ? Math.min(150, 68 + warningCount * 34) : 110);
  const innerTip = polarPoint(50, 50, 18, innerEnd);

  return (
    <div
      role="img"
      aria-label={`Orbital compass: ${healthyCount} healthy, ${activeCount} active, ${warningCount} warning`}
      className={cn('relative inline-flex items-center justify-center shrink-0 select-none', className)}
      style={{ width: size, height: size }}
    >
      <svg width={size} height={size} viewBox="0 0 100 100" fill="none" aria-hidden="true">
        <defs>
          <radialGradient id={gradId} cx="50%" cy="50%" r="50%">
            <stop offset="0%" stopColor="var(--emerald)" stopOpacity="0.09" />
            <stop offset="62%" stopColor="var(--sky)" stopOpacity="0.03" />
            <stop offset="100%" stopColor="var(--emerald)" stopOpacity="0" />
          </radialGradient>
        </defs>

        {/* Soft ambient core halo */}
        <circle cx="50" cy="50" r="46" fill={`url(#${gradId})`} />

        {/* Faint cardinal ticks */}
        <line x1="50" y1="4" x2="50" y2="9" stroke="var(--border-strong)" strokeWidth="1" />
        <line x1="50" y1="91" x2="50" y2="96" stroke="var(--border-strong)" strokeWidth="1" />
        <line x1="4" y1="50" x2="9" y2="50" stroke="var(--border-strong)" strokeWidth="1" />
        <line x1="91" y1="50" x2="96" y2="50" stroke="var(--border-strong)" strokeWidth="1" />

        {/* Three concentric orbital tracks */}
        <circle cx="50" cy="50" r="40" stroke="var(--border-subtle)" strokeWidth="1.1" />
        <circle
          cx="50"
          cy="50"
          r="29"
          stroke="var(--border-subtle)"
          strokeWidth="1"
          strokeDasharray="2.5 3"
        />
        <circle cx="50" cy="50" r="18" stroke="var(--border-subtle)" strokeWidth="1" />

        {/* Outer Emerald healthy sweep + glowing nodes */}
        <path
          d={describeOrbitArc(50, 50, 40, outerStart, outerEnd)}
          stroke="var(--emerald)"
          strokeWidth="2.4"
          strokeLinecap="round"
        />
        <circle cx={outerTail.x} cy={outerTail.y} r="2.2" fill="var(--emerald)" fillOpacity="0.55" />
        <circle cx={outerTip.x} cy={outerTip.y} r="6.8" fill="var(--emerald)" fillOpacity="0.18" />
        <circle cx={outerTip.x} cy={outerTip.y} r="3.4" fill="var(--emerald)" />

        {/* Middle Sky active track sweep + node */}
        <path
          d={describeOrbitArc(50, 50, 29, midStart, midEnd)}
          stroke="var(--sky)"
          strokeOpacity={activeCount > 0 ? 0.9 : 0.4}
          strokeWidth="2"
          strokeLinecap="round"
        />
        <circle cx={midTip.x} cy={midTip.y} r="5.8" fill="var(--sky)" fillOpacity="0.16" />
        <circle cx={midTip.x} cy={midTip.y} r="3" fill="var(--sky)" />

        {/* Inner Coral warning or Amber harmonic sweep + node */}
        <path
          d={describeOrbitArc(50, 50, 18, innerStart, innerEnd)}
          stroke={hasWarning ? 'var(--coral)' : 'var(--amber)'}
          strokeWidth="2.1"
          strokeLinecap="round"
        />
        <circle
          cx={innerTip.x}
          cy={innerTip.y}
          r="6"
          fill={hasWarning ? 'var(--coral)' : 'var(--amber)'}
          fillOpacity="0.18"
        />
        <circle
          cx={innerTip.x}
          cy={innerTip.y}
          r="3.1"
          fill={hasWarning ? 'var(--coral)' : 'var(--amber)'}
        />

        {/* Center Stardust nucleus */}
        <circle
          cx="50"
          cy="50"
          r="6"
          fill="var(--surface-2)"
          stroke="var(--border-strong)"
          strokeWidth="1.1"
        />
        <circle
          cx="50"
          cy="50"
          r="2.6"
          fill={hasWarning ? 'var(--coral)' : 'var(--emerald)'}
        />
      </svg>
    </div>
  );
}

// ============================================================================
// ActionButton / IconButton / Switch
// ============================================================================

export interface ActionButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  /** Default `'secondary'`. Use `'primary'` (Ink) or `'coral'` (#E5533D) for primary CTAs. */
  variant?: 'primary' | 'coral' | 'secondary' | 'ghost' | 'danger';
  /** Default `'sm'` (28px). */
  size?: 'xs' | 'sm' | 'md';
  /** Leading icon. */
  icon?: React.ReactNode;
  /** Trailing icon. */
  iconRight?: React.ReactNode;
  /** Shows a spinner in place of the leading icon and disables the button. */
  loading?: boolean;
  fullWidth?: boolean;
}

/** Button with four variants on the v2 control scale. */
export const ActionButton = React.forwardRef<HTMLButtonElement, ActionButtonProps>(
  function ActionButton(
    {
      variant = 'secondary',
      size = 'sm',
      icon,
      iconRight,
      loading = false,
      fullWidth = false,
      className,
      children,
      disabled,
      type = 'button',
      ...rest
    },
    ref,
  ) {
    return (
      <button
        ref={ref}
        type={type}
        disabled={disabled || loading}
        aria-busy={loading || undefined}
        className={cn(
          'ui-btn',
          `ui-btn-${variant}`,
          size === 'xs' && 'ui-btn-xs',
          size === 'md' && 'ui-btn-md',
          fullWidth && 'w-full',
          className,
        )}
        {...rest}
      >
        {loading ? (
          <Loader2 className="w-3.5 h-3.5 shrink-0 animate-spin" />
        ) : (
          icon != null && <span className="shrink-0 [&>svg]:w-3.5 [&>svg]:h-3.5">{icon}</span>
        )}
        {children}
        {iconRight != null && (
          <span className="shrink-0 [&>svg]:w-3.5 [&>svg]:h-3.5">{iconRight}</span>
        )}
      </button>
    );
  },
);

/** Alias of `ActionButton`. */
export const Button = ActionButton;

export interface IconButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  /** Required accessible name (also used as the tooltip unless `title` is set). */
  label: string;
  /** Default `'md'` (28px). */
  size?: 'sm' | 'md';
  children: React.ReactNode;
}

/** Square ghost button for a single icon. */
export const IconButton = React.forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { label, size = 'md', className, children, type = 'button', title, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      aria-label={label}
      title={title ?? label}
      className={cn('ui-icon-btn', size === 'sm' && 'w-6 h-6 rounded-md', className)}
      {...rest}
    >
      {children}
    </button>
  );
});

export interface SwitchProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  /** Accessible name when there is no visible `<label>` wrapping the switch. */
  label?: string;
  disabled?: boolean;
  id?: string;
  className?: string;
}

/** Toggle switch (`role="switch"`). */
export function Switch({ checked, onChange, label, disabled, id, className }: SwitchProps) {
  return (
    <button
      id={id}
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn('ui-switch', className)}
    />
  );
}

// ============================================================================
// EmptyState / Skeleton
// ============================================================================

export interface EmptyStateProps {
  icon?: React.ReactNode;
  title: React.ReactNode;
  description?: React.ReactNode;
  /** Usually one `ActionButton`. */
  action?: React.ReactNode;
  className?: string;
}

/** Centered placeholder for empty lists and zero-data panels. */
export function EmptyState({ icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div className={cn('flex flex-col items-center text-center px-6 py-10', className)}>
      {icon != null && (
        <div className="mb-3.5 flex items-center justify-center w-10 h-10 rounded-xl border border-line-faint bg-surface-1 text-fg-muted [&>svg]:w-[18px] [&>svg]:h-[18px]">
          {icon}
        </div>
      )}
      <div className="text-[14px] font-semibold text-fg">{title}</div>
      {description != null && (
        <p className="mt-1 max-w-sm text-[12.5px] leading-relaxed text-fg-secondary">
          {description}
        </p>
      )}
      {action != null && <div className="mt-4">{action}</div>}
    </div>
  );
}

export interface SkeletonProps extends React.HTMLAttributes<HTMLDivElement> {
  className?: string;
}

/** Shimmering loading placeholder. Size it with width/height utilities. */
export function Skeleton({ className, ...rest }: SkeletonProps) {
  return <div aria-hidden="true" className={cn('ui-skeleton', className)} {...rest} />;
}

// ============================================================================
// Overlay plumbing (shared by SlideOver and DialogModal)
// ============================================================================

const overlayStack: string[] = [];
let scrollLockCount = 0;
let restoreBodyStyle: { overflow: string; paddingRight: string } | null = null;

function lockBodyScroll(): void {
  if (scrollLockCount === 0) {
    const { body, documentElement } = document;
    restoreBodyStyle = { overflow: body.style.overflow, paddingRight: body.style.paddingRight };
    const scrollbarWidth = window.innerWidth - documentElement.clientWidth;
    if (scrollbarWidth > 0) {
      const current = parseFloat(window.getComputedStyle(body).paddingRight) || 0;
      body.style.paddingRight = `${current + scrollbarWidth}px`;
    }
    body.style.overflow = 'hidden';
  }
  scrollLockCount += 1;
}

function unlockBodyScroll(): void {
  scrollLockCount = Math.max(0, scrollLockCount - 1);
  if (scrollLockCount === 0 && restoreBodyStyle) {
    document.body.style.overflow = restoreBodyStyle.overflow;
    document.body.style.paddingRight = restoreBodyStyle.paddingRight;
    restoreBodyStyle = null;
  }
}

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

/**
 * Overlay behaviour: Escape closes the top-most overlay only, body scroll is
 * locked while any overlay is open, Tab stays inside the panel, and focus
 * returns to the opener on close.
 */
function useOverlay(
  open: boolean,
  onClose: () => void,
  panelRef: React.RefObject<HTMLDivElement | null>,
): void {
  const id = useId();
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  useEffect(() => {
    if (!open) return;

    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    overlayStack.push(id);
    lockBodyScroll();
    panelRef.current?.focus({ preventScroll: true });

    const handleKeyDown = (e: KeyboardEvent) => {
      if (overlayStack[overlayStack.length - 1] !== id) return;

      if (e.key === 'Escape') {
        e.stopPropagation();
        onCloseRef.current();
        return;
      }

      if (e.key !== 'Tab') return;
      const panel = panelRef.current;
      if (!panel) return;
      const focusable = Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
        (el) => el.offsetParent !== null,
      );
      if (focusable.length === 0) {
        e.preventDefault();
        return;
      }
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || active === panel)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      } else if (active instanceof Node && !panel.contains(active)) {
        e.preventDefault();
        first.focus();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
      const idx = overlayStack.lastIndexOf(id);
      if (idx >= 0) overlayStack.splice(idx, 1);
      unlockBodyScroll();
      if (opener && document.contains(opener)) opener.focus({ preventScroll: true });
    };
  }, [open, id, panelRef]);
}

interface OverlayHeaderProps {
  titleId: string;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  icon?: React.ReactNode;
  headerActions?: React.ReactNode;
  onClose: () => void;
}

function OverlayHeader({
  titleId,
  title,
  subtitle,
  icon,
  headerActions,
  onClose,
}: OverlayHeaderProps) {
  return (
    <div className="flex items-start justify-between gap-3 px-5 py-4 border-b border-line-faint shrink-0">
      <div className="flex items-start gap-3 min-w-0">
        {icon != null && (
          <span
            className="flex items-center justify-center w-8 h-8 shrink-0 rounded-[10px] border [&>svg]:w-4 [&>svg]:h-4"
            style={{
              color: 'var(--accent-text)',
              backgroundColor: 'var(--accent-soft)',
              borderColor: 'var(--accent-border)',
            }}
          >
            {icon}
          </span>
        )}
        <div className="min-w-0">
          <h2 id={titleId} className="ui-title text-[15px] font-semibold leading-snug">
            {title}
          </h2>
          {subtitle != null && (
            <p className="mt-0.5 text-[12.5px] leading-relaxed text-fg-secondary">{subtitle}</p>
          )}
        </div>
      </div>
      <div className="flex items-center gap-0.5 shrink-0">
        {headerActions}
        <IconButton label="Close" onClick={onClose}>
          <X className="w-4 h-4" />
        </IconButton>
      </div>
    </div>
  );
}

// ============================================================================
// SlideOver (L3)
// ============================================================================

export interface SlideOverProps {
  open: boolean;
  onClose: () => void;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  /** Rendered inside an accent badge next to the title. */
  icon?: React.ReactNode;
  /** Extra header controls, placed before the close button. */
  headerActions?: React.ReactNode;
  /** Sticky footer bar. */
  footer?: React.ReactNode;
  /** Default `'xl'` → `max-w-xl`. */
  width?: 'md' | 'lg' | 'xl' | '2xl';
  children: React.ReactNode;
  /** Applied to the scrollable body. */
  className?: string;
}

const SLIDE_OVER_WIDTH: Record<NonNullable<SlideOverProps['width']>, string> = {
  md: 'max-w-md',
  lg: 'max-w-lg',
  xl: 'max-w-xl',
  '2xl': 'max-w-2xl',
};

/**
 * Right-edge drawer on L3 frosted glass. Closes on Escape or backdrop click,
 * locks body scroll, traps focus. Rendered in a portal on `document.body`.
 */
export function SlideOver({
  open,
  onClose,
  title,
  subtitle,
  icon,
  headerActions,
  footer,
  width = 'xl',
  children,
  className,
}: SlideOverProps) {
  const panelRef = useRef<HTMLDivElement | null>(null);
  const titleId = useId();
  useOverlay(open, onClose, panelRef);

  if (!open || typeof document === 'undefined') return null;

  return createPortal(
    <div className="fixed inset-0 z-[70] flex justify-end">
      <div className="ui-scrim ui-anim-fade absolute inset-0" onClick={onClose} aria-hidden="true" />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={cn(
          'ui-float ui-anim-slide relative flex flex-col w-full h-full outline-none overflow-hidden',
          'sm:my-2.5 sm:mr-2.5 sm:h-[calc(100%-20px)] sm:rounded-2xl',
          SLIDE_OVER_WIDTH[width],
        )}
      >
        <OverlayHeader
          titleId={titleId}
          title={title}
          subtitle={subtitle}
          icon={icon}
          headerActions={headerActions}
          onClose={onClose}
        />
        <div className={cn('flex-1 min-h-0 overflow-y-auto px-5 py-5', className)}>{children}</div>
        {footer != null && (
          <div className="px-5 py-3.5 border-t border-line-faint shrink-0">{footer}</div>
        )}
      </div>
    </div>,
    document.body,
  );
}

// ============================================================================
// DialogModal (L3)
// ============================================================================

export interface DialogModalProps {
  open: boolean;
  onClose: () => void;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  /** Rendered inside an accent badge next to the title. */
  icon?: React.ReactNode;
  /** Extra header controls, placed before the close button. */
  headerActions?: React.ReactNode;
  /** Footer bar, typically right-aligned actions. */
  footer?: React.ReactNode;
  /** Default `'lg'` → `max-w-lg`. */
  size?: 'sm' | 'md' | 'lg' | 'xl' | '2xl';
  children: React.ReactNode;
  /** Applied to the scrollable body. */
  className?: string;
}

const DIALOG_SIZE: Record<NonNullable<DialogModalProps['size']>, string> = {
  sm: 'max-w-sm',
  md: 'max-w-md',
  lg: 'max-w-lg',
  xl: 'max-w-xl',
  '2xl': 'max-w-2xl',
};

/**
 * Centered dialog on L3 frosted glass (16px radius). Closes on Escape or
 * backdrop click, locks body scroll, traps focus. Rendered in a portal.
 */
export function DialogModal({
  open,
  onClose,
  title,
  subtitle,
  icon,
  headerActions,
  footer,
  size = 'lg',
  children,
  className,
}: DialogModalProps) {
  const panelRef = useRef<HTMLDivElement | null>(null);
  const titleId = useId();
  useOverlay(open, onClose, panelRef);

  if (!open || typeof document === 'undefined') return null;

  return createPortal(
    <div className="fixed inset-0 z-[70] flex items-center justify-center p-4 sm:p-6">
      <div className="ui-scrim ui-anim-fade absolute inset-0" onClick={onClose} aria-hidden="true" />
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
        className={cn(
          'ui-float ui-anim-scale relative flex flex-col w-full max-h-[calc(100dvh-2rem)] sm:max-h-[calc(100dvh-3rem)] rounded-2xl outline-none overflow-hidden',
          DIALOG_SIZE[size],
        )}
      >
        <OverlayHeader
          titleId={titleId}
          title={title}
          subtitle={subtitle}
          icon={icon}
          headerActions={headerActions}
          onClose={onClose}
        />
        <div className={cn('flex-1 min-h-0 overflow-y-auto px-5 py-5', className)}>{children}</div>
        {footer != null && (
          <div className="px-5 py-3.5 border-t border-line-faint shrink-0">{footer}</div>
        )}
      </div>
    </div>,
    document.body,
  );
}
