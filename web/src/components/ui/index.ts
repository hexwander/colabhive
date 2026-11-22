/**
 * ColabHive v4 Stardust UI Primitives ("Stardust Apple Studio Aesthetic").
 *
 * ```ts
 * import {
 *   LayeredCard,
 *   InsetWell,
 *   StatusPill,
 *   AnimatedNumber,
 *   CircularQuotaRing,
 *   SegmentedQuotaBar,
 *   OrbitalCompassGraphic,
 *   QuickStartLaunchpad,
 * } from '../components/ui';
 * ```
 */
export * from './primitives';

export {
  CircularQuotaRing,
  SegmentedQuotaBar,
  OrbitalCompassGraphic,
} from './primitives';
export type {
  QuotaRingTone,
  CircularQuotaRingProps,
  SegmentedQuotaBarProps,
  OrbitalCompassGraphicProps,
} from './primitives';

// Explicit re-exports so the onboarding API is discoverable from this entry.
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
