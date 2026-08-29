import { StyleSheet } from 'react-native';

export const colors = {
  bg: '#faf7f7',
  surface: '#ffffff',
  border: '#e7e0e0',
  text: '#1e1b1b',
  muted: '#6f6767',
  primary: '#d6336c',
  primaryText: '#ffffff',
  engaging: '#2f9e44',
  neutral: '#f08c00',
  flat: '#e03131',
};

/** Content wider than this is centred on web instead of stretching. */
export const CONTENT_MAX_WIDTH = 720;

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
    padding: 16,
    gap: 12,
  },
  card: {
    backgroundColor: colors.surface,
    borderRadius: 14,
    borderWidth: 1,
    borderColor: colors.border,
    padding: 16,
    gap: 8,
  },
  title: {
    fontSize: 24,
    fontWeight: '700',
    color: colors.text,
  },
  heading: {
    fontSize: 17,
    fontWeight: '600',
    color: colors.text,
  },
  body: {
    fontSize: 15,
    color: colors.text,
    lineHeight: 21,
  },
  muted: {
    fontSize: 13,
    color: colors.muted,
  },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
  },
  error: {
    color: colors.flat,
    fontSize: 14,
  },
});
