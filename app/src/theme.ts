import { Platform, StyleSheet, type TextStyle, type ViewStyle } from 'react-native';

/**
 * Dating Humane palette: greens and tans, like sun through leaves. Linen
 * and sand grounds, a leafy fern accent, and moss / eucalyptus / honey
 * tones for secondary meaning. Semantic keys (`engaging`, `neutral`, `flat`) stay
 * traffic-light coloured so scores read at a glance.
 */
export const colors = {
  bg: '#f4efe4',
  surface: '#fdfbf6',
  surfaceAlt: '#f8f3e8',
  surfaceSunken: '#ebe3d2',
  border: '#e3d9c4',
  text: '#27302a',
  muted: '#6f6e5e',
  primary: '#5c8a5e',
  primaryDeep: '#3f6444',
  primaryLight: '#9dbb97',
  primaryTint: '#e5efe1',
  primaryText: '#ffffff',
  sage: '#93ae8d',
  sageDeep: '#5f7f5c',
  sageTint: '#e8f0e3',
  moss: '#2f4a38',
  sky: '#a3bcae',
  skyTint: '#e7efe9',
  sun: '#d8a76a',
  sunTint: '#f6ead6',
  sand: '#d9c3a0',
  bark: '#6b5238',
  engaging: '#4f9a5b',
  neutral: '#d0963c',
  noticeBg: '#f7ecd9',
  flat: '#c25a4a',
  shadow: '#2f3326',
};

/** Content wider than this is centred on web instead of stretching. */
export const CONTENT_MAX_WIDTH = 720;

/** Wider column used by dashboards that lay cards out in a grid. */
export const WIDE_CONTENT_MAX_WIDTH = 1040;

/** Font families registered in App.tsx (Manrope for UI, Fraunces for accents). */
export const fonts = {
  sans: 'Manrope_500Medium',
  sansSemi: 'Manrope_600SemiBold',
  sansBold: 'Manrope_700Bold',
  sansBlack: 'Manrope_800ExtraBold',
  serifItalic: 'Fraunces_500Medium_Italic',
  serifItalicBold: 'Fraunces_700Bold_Italic',
};

export const radii = {
  sm: 12,
  md: 16,
  lg: 24,
  xl: 32,
  pill: 999,
};

/** Gradient stop pairs shared by heroes, buttons and feature cards. */
export const gradients = {
  primary: [colors.primary, colors.primaryDeep] as const,
  sunrise: [colors.sun, colors.bark] as const,
  meadow: [colors.sage, colors.sageDeep] as const,
  sky: [colors.sky, colors.sage] as const,
  dusk: [colors.moss, colors.bark] as const,
  dawnSurface: [colors.surface, colors.surfaceAlt] as const,
};

/** `#rrggbb` → `rgba(r, g, b, alpha)` for CSS shadows; other formats are returned unchanged. */
const rgba = (hex: string, alpha: number): string => {
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(hex);
  if (!m) return hex;
  return `rgba(${parseInt(m[1], 16)}, ${parseInt(m[2], 16)}, ${parseInt(m[3], 16)}, ${alpha})`;
};

/**
 * Builds a cross-platform shadow style. Android only honours `elevation`
 * (scaled from `level`); iOS gets the `shadow*` props and web a CSS
 * `boxShadow` built from the same values. Returns a spreadable style;
 * callers with `overflow: 'hidden'` clip their own shadow on iOS.
 */
const elevate = (
  level: 1 | 2 | 3,
  { opacity, radius, offsetY, shadowColor = colors.shadow }: {
    opacity: number;
    radius: number;
    offsetY: number;
    shadowColor?: string;
  },
): ViewStyle =>
  Platform.select<ViewStyle>({
    android: { elevation: level * 2, shadowColor },
    web: { boxShadow: `0 ${offsetY}px ${radius}px ${rgba(shadowColor, opacity)}` },
    default: {
      shadowColor,
      shadowOffset: { width: 0, height: offsetY },
      shadowOpacity: opacity,
      shadowRadius: radius,
    },
  }) ?? {};

/** Soft, layered shadows shared by cards, inputs and controls. */
export const elevation = {
  low: elevate(1, { opacity: 0.06, radius: 8, offsetY: 2 }),
  mid: elevate(2, { opacity: 0.09, radius: 20, offsetY: 8 }),
  high: elevate(3, { opacity: 0.16, radius: 36, offsetY: 16 }),
  primary: elevate(2, { opacity: 0.32, radius: 16, offsetY: 8, shadowColor: colors.primary }),
};

export const scoreColor = (score: number): string =>
  score >= 0.66 ? colors.engaging : score >= 0.4 ? colors.neutral : colors.flat;

/** Type scale used across screens; pairs with `fonts`. */
export const type: Record<
  'display' | 'title' | 'heading' | 'subheading' | 'body' | 'caption' | 'eyebrow' | 'accent',
  TextStyle
> = {
  display: { fontFamily: fonts.sansBlack, fontSize: 40, lineHeight: 44, letterSpacing: -1.2, color: colors.text },
  title: { fontFamily: fonts.sansBlack, fontSize: 28, lineHeight: 34, letterSpacing: -0.6, color: colors.text },
  heading: { fontFamily: fonts.sansBold, fontSize: 18, lineHeight: 24, letterSpacing: -0.2, color: colors.text },
  subheading: { fontFamily: fonts.sansSemi, fontSize: 15, lineHeight: 20, color: colors.text },
  body: { fontFamily: fonts.sans, fontSize: 15, lineHeight: 22, color: colors.text },
  caption: { fontFamily: fonts.sans, fontSize: 13, lineHeight: 18, color: colors.muted },
  eyebrow: {
    fontFamily: fonts.sansBold,
    fontSize: 12,
    lineHeight: 16,
    letterSpacing: 1.2,
    textTransform: 'uppercase',
    color: colors.sageDeep,
  },
  accent: { fontFamily: fonts.serifItalic, fontSize: 20, lineHeight: 26, color: colors.primaryDeep },
};

export const shared = StyleSheet.create({
  screen: {
    flex: 1,
    backgroundColor: colors.bg,
  },
  content: {
    width: '100%',
    maxWidth: CONTENT_MAX_WIDTH,
    alignSelf: 'center',
    paddingHorizontal: 18,
    paddingVertical: 20,
    paddingBottom: 40,
    gap: 16,
  },
  card: {
    backgroundColor: colors.surface,
    borderRadius: radii.lg,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: 'rgba(47, 51, 38, 0.06)',
    padding: 20,
    gap: 10,
    ...elevation.mid,
  },
  title: type.title,
  heading: type.heading,
  body: type.body,
  muted: type.caption,
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 10,
  },
  error: {
    fontFamily: fonts.sansSemi,
    color: colors.flat,
    fontSize: 14,
    lineHeight: 20,
  },
});
