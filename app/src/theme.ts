import { Platform, StyleSheet, type ViewStyle } from 'react-native';

export const colors = {
  bg: '#f4eef0',
  surface: '#ffffff',
  surfaceAlt: '#fbf7f8',
  surfaceSunken: '#ece4e7',
  border: '#ece4e6',
  text: '#1e1b1b',
  muted: '#6f6767',
  primary: '#d6336c',
  primaryDeep: '#b8285a',
  primaryLight: '#f0669a',
  primaryTint: '#fce8ef',
  primaryText: '#ffffff',
  engaging: '#2f9e44',
  neutral: '#f08c00',
  noticeBg: '#fff4e6',
  flat: '#e03131',
  shadow: '#3b1420',
};

/** Content wider than this is centred on web instead of stretching. */
export const CONTENT_MAX_WIDTH = 720;

export const radii = {
  sm: 10,
  md: 14,
  lg: 20,
  xl: 28,
  pill: 999,
};

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
    default: {
      shadowColor,
      shadowOffset: { width: 0, height: offsetY },
      shadowOpacity: opacity,
      shadowRadius: radius,
    },
  }) ?? {};

/** Soft, layered shadows shared by cards, inputs and controls. */
export const elevation = {
  low: elevate(1, { opacity: 0.06, radius: 6, offsetY: 2 }),
  mid: elevate(2, { opacity: 0.1, radius: 16, offsetY: 6 }),
  high: elevate(3, { opacity: 0.16, radius: 28, offsetY: 12 }),
  primary: elevate(2, { opacity: 0.35, radius: 14, offsetY: 6, shadowColor: colors.primary }),
};

export const scoreColor = (score: number): string =>
  score >= 0.66 ? colors.engaging : score >= 0.4 ? colors.neutral : colors.flat;

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
    gap: 16,
  },
  card: {
    backgroundColor: colors.surface,
    borderRadius: radii.lg,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: 'rgba(59, 20, 32, 0.06)',
    padding: 20,
    gap: 10,
    ...elevation.mid,
  },
  title: {
    fontSize: 28,
    fontWeight: '800',
    letterSpacing: -0.5,
    lineHeight: 34,
    color: colors.text,
  },
  heading: {
    fontSize: 18,
    fontWeight: '700',
    letterSpacing: -0.2,
    lineHeight: 24,
    color: colors.text,
  },
  body: {
    fontSize: 15,
    color: colors.text,
    lineHeight: 22,
  },
  muted: {
    fontSize: 13,
    lineHeight: 18,
    color: colors.muted,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 10,
  },
  error: {
    color: colors.flat,
    fontSize: 14,
    lineHeight: 20,
    fontWeight: '500',
  },
});
