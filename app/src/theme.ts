import { Platform, StyleSheet, type TextStyle, type ViewStyle } from 'react-native';

/**
 * Dating Humane palette: warm, sunlit and alive. Linen and sand grounds,
 * a soft terracotta-rose accent, and leafy sage / sky / sun tones for
 * secondary meaning. Semantic keys (`engaging`, `neutral`, `flat`) stay
 * traffic-light coloured so scores read at a glance.
 */
export const colors = {
  bg: '#f7f1e8',
  surface: '#fffcf8',
  surfaceAlt: '#fbf5ec',
  surfaceSunken: '#efe5d8',
  border: '#ebe0d2',
  text: '#2b2521',
  muted: '#7a6d64',
  primary: '#d9707a',
  primaryDeep: '#b9535f',
  primaryLight: '#f0a2a8',
  primaryTint: '#fbe7e6',
  primaryText: '#ffffff',
  sage: '#8fae8b',
  sageDeep: '#5f7f5c',
  sageTint: '#e8f0e3',
  moss: '#3f5b45',
  sky: '#9cc0d3',
  skyTint: '#e6f0f5',
  sun: '#f2b56b',
  sunTint: '#fdefdc',
  sand: '#e9d7bf',
  bark: '#5a4638',
  engaging: '#4f9a5b',
  neutral: '#e09a3c',
  noticeBg: '#fdf1e0',
  flat: '#d1554f',
  shadow: '#3a2c24',
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
  sunrise: [colors.sun, colors.primary] as const,
  meadow: [colors.sage, colors.sageDeep] as const,
  sky: [colors.sky, colors.sage] as const,
  dusk: [colors.primaryDeep, colors.moss] as const,
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
    borderColor: 'rgba(58, 44, 36, 0.06)',
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
