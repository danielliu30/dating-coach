import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import React from 'react';
import { Pressable, StyleSheet, Text, View, type StyleProp, type ViewStyle } from 'react-native';

import { colors, elevation, fonts, gradients, radii, type } from '../theme';
import { Button, type IconName } from './ui';

type Gradient = keyof typeof gradients;
/** Gradients dark enough to carry white text. */
type DarkGradient = Exclude<Gradient, 'dawnSurface'>;

/** Translucent (~12%) fill for a `#rrggbb` tone; falls back to the sunken surface for other colour formats. */
function tint(tone: string): string {
  return /^#[0-9a-f]{6}$/i.test(tone) ? `${tone}1f` : colors.surfaceSunken;
}

/** Hue families used for tinted chrome (avatars, icon discs, stat tiles). */
export type Hue = 'rose' | 'sage' | 'sky' | 'sun';

const hues: Record<Hue, { fg: string; bg: string }> = {
  rose: { fg: colors.primaryDeep, bg: colors.primaryTint },
  sage: { fg: colors.sageDeep, bg: colors.sageTint },
  sky: { fg: '#3f6e86', bg: colors.skyTint },
  sun: { fg: '#a86a1c', bg: colors.sunTint },
};

/** Picks a stable hue for a name so the same person always gets the same colour. */
export function hueFor(seed: string): Hue {
  const keys: Hue[] = ['rose', 'sage', 'sky', 'sun'];
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = (h * 31 + seed.charCodeAt(i)) >>> 0;
  return keys[h % keys.length];
}

/** Up to two initials from a display name, e.g. "Ana Lopez" → "AL". */
export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return '?';
  const first = parts[0][0] ?? '';
  const last = parts.length > 1 ? parts[parts.length - 1][0] ?? '' : '';
  return (first + last).toUpperCase();
}

/**
 * Circular initials avatar tinted by `hueFor(name)`. `size` is the diameter;
 * an `online` dot is drawn on the bottom-right when provided.
 */
export function Avatar({
  name,
  size = 48,
  online,
}: {
  name: string;
  size?: number;
  online?: boolean;
}): React.ReactElement {
  const hue = hues[hueFor(name)];
  return (
    <View style={{ width: size, height: size }}>
      <View
        style={[
          styles.avatar,
          { width: size, height: size, borderRadius: size / 2, backgroundColor: hue.bg },
        ]}
      >
        <Text style={[styles.avatarText, { color: hue.fg, fontSize: size * 0.38 }]}>{initials(name)}</Text>
      </View>
      {online !== undefined ? (
        <View
          style={[
            styles.presence,
            { backgroundColor: online ? colors.engaging : colors.sand, width: size * 0.28, height: size * 0.28 },
          ]}
        />
      ) : null}
    </View>
  );
}

/** Small tinted disc holding an icon; the workhorse for card leading visuals. */
export function IconDisc({
  icon,
  hue = 'rose',
  size = 44,
}: {
  icon: IconName;
  hue?: Hue;
  size?: number;
}): React.ReactElement {
  const h = hues[hue];
  return (
    <View style={[styles.disc, { width: size, height: size, borderRadius: size / 2, backgroundColor: h.bg }]}>
      <Ionicons name={icon} size={Math.round(size * 0.48)} color={h.fg} />
    </View>
  );
}

/**
 * Screen-top hero band with an eyebrow, display title, supporting copy and an
 * optional right-aligned slot (`aside`) for an avatar or action. Uses a soft
 * gradient so the page never opens on a bare background.
 */
export function PageHeader({
  eyebrow,
  title,
  subtitle,
  aside,
  gradient = 'sunrise',
  children,
}: {
  eyebrow?: string;
  title: string;
  subtitle?: string;
  aside?: React.ReactNode;
  gradient?: DarkGradient;
  children?: React.ReactNode;
}): React.ReactElement {
  return (
    <View style={styles.header}>
      <LinearGradient
        colors={gradients[gradient]}
        start={{ x: 0, y: 0 }}
        end={{ x: 1, y: 1 }}
        style={styles.headerGradient}
      />
      <View style={styles.headerOrb} />
      <View style={styles.headerOrbSmall} />
      <View style={styles.headerRow}>
        <View style={{ flex: 1, gap: 6 }}>
          {eyebrow ? <Text style={styles.headerEyebrow}>{eyebrow}</Text> : null}
          <Text style={styles.headerTitle}>{title}</Text>
          {subtitle ? <Text style={styles.headerSubtitle}>{subtitle}</Text> : null}
        </View>
        {aside}
      </View>
      {children}
    </View>
  );
}

/**
 * Heading + optional caption/action line that introduces a group of cards.
 * An optional `icon` renders a small tinted disc in front of the title.
 */
export function SectionHeader({
  title,
  caption,
  action,
  icon,
  hue = 'sage',
}: {
  title: string;
  caption?: string;
  action?: { label: string; onPress: () => void };
  icon?: IconName;
  hue?: Hue;
}): React.ReactElement {
  return (
    <View style={styles.sectionHeader}>
      {icon ? <IconDisc icon={icon} hue={hue} size={36} /> : null}
      <View style={{ flex: 1, gap: 2 }}>
        <Text style={type.heading}>{title}</Text>
        {caption ? <Text style={type.caption}>{caption}</Text> : null}
      </View>
      {action ? (
        <Pressable accessibilityRole="button" onPress={action.onPress} hitSlop={8}>
          <Text style={styles.sectionAction}>{action.label}</Text>
        </Pressable>
      ) : null}
    </View>
  );
}

/** Compact metric card (value, label, icon) for summary strips. */
export function StatTile({
  value,
  label,
  icon,
  hue = 'rose',
}: {
  value: string | number;
  label: string;
  icon: IconName;
  hue?: Hue;
}): React.ReactElement {
  const h = hues[hue];
  return (
    <View style={styles.stat}>
      <View style={[styles.statIcon, { backgroundColor: h.bg }]}>
        <Ionicons name={icon} size={16} color={h.fg} />
      </View>
      <Text style={styles.statValue}>{value}</Text>
      <Text style={styles.statLabel}>{label}</Text>
    </View>
  );
}

/** Horizontal strip that lays `StatTile`s out with even gaps. */
export function StatRow({ children }: { children: React.ReactNode }): React.ReactElement {
  return <View style={styles.statRow}>{children}</View>;
}

/** Icon + label + value line used inside cards for metadata. */
export function MetaRow({
  icon,
  text,
  tone = colors.muted,
}: {
  icon: IconName;
  text: string;
  tone?: string;
}): React.ReactElement {
  return (
    <View style={styles.meta}>
      <Ionicons name={icon} size={15} color={tone} />
      <Text style={[type.caption, { color: tone, flexShrink: 1 }]}>{text}</Text>
    </View>
  );
}

/**
 * Guided empty state: illustration disc, title, helpful copy and an optional
 * primary action so an empty list reads as a next step rather than a void.
 */
export function EmptyState({
  icon,
  title,
  text,
  action,
  hue = 'sage',
}: {
  icon: IconName;
  title: string;
  text: string;
  action?: { label: string; onPress: () => void; icon?: IconName };
  hue?: Hue;
}): React.ReactElement {
  return (
    <View style={styles.empty}>
      <View style={[styles.emptyDisc, { backgroundColor: hues[hue].bg }]}>
        <View style={[styles.emptyDiscInner, { backgroundColor: colors.surface }]}>
          <Ionicons name={icon} size={28} color={hues[hue].fg} />
        </View>
      </View>
      <Text style={[type.heading, { textAlign: 'center' }]}>{title}</Text>
      <Text style={[type.body, styles.emptyText]}>{text}</Text>
      {action ? (
        <View style={{ marginTop: 6, alignSelf: 'stretch' }}>
          <Button label={action.label} onPress={action.onPress} icon={action.icon} />
        </View>
      ) : null}
    </View>
  );
}

/** Shimmer-less skeleton block for content that is still loading. */
export function Skeleton({
  height = 16,
  width = '100%',
  radius = radii.sm,
  style,
}: {
  height?: number;
  width?: number | `${number}%`;
  radius?: number;
  style?: StyleProp<ViewStyle>;
}): React.ReactElement {
  return <View style={[{ height, width, borderRadius: radius, backgroundColor: colors.surfaceSunken }, style]} />;
}

/** Card-shaped skeleton showing an avatar, title and two lines of copy. */
export function SkeletonCard(): React.ReactElement {
  return (
    <View style={[styles.skeletonCard]}>
      <View style={{ flexDirection: 'row', gap: 12, alignItems: 'center' }}>
        <Skeleton height={44} width={44} radius={22} />
        <View style={{ flex: 1, gap: 8 }}>
          <Skeleton height={14} width="55%" />
          <Skeleton height={12} width="35%" />
        </View>
      </View>
      <Skeleton height={12} />
      <Skeleton height={12} width="80%" />
    </View>
  );
}

/**
 * Small filled pill with a leading dot, for lifecycle states like
 * "Active" / "Pending". `tone` colours both dot and text; `onDark` swaps the
 * tinted fill for a translucent white one so it reads on gradient cards.
 */
export function StatusPill({
  text,
  tone,
  onDark = false,
}: {
  text: string;
  tone: string;
  onDark?: boolean;
}): React.ReactElement {
  return (
    <View style={[styles.pill, { backgroundColor: onDark ? 'rgba(255,255,255,0.18)' : tint(tone) }]}>
      <View style={[styles.pillDot, { backgroundColor: tone }]} />
      <Text style={[styles.pillText, { color: tone }]}>{text}</Text>
    </View>
  );
}

/**
 * Pressable list card with a leading visual, title/subtitle stack and a
 * trailing chevron; the standard row for coaches, threads and sessions.
 */
export function ListCard({
  onPress,
  leading,
  title,
  subtitle,
  trailing,
  children,
  accessibilityLabel,
}: {
  onPress?: () => void;
  leading?: React.ReactNode;
  title: string;
  subtitle?: string;
  trailing?: React.ReactNode;
  children?: React.ReactNode;
  accessibilityLabel?: string;
}): React.ReactElement {
  return (
    <Pressable
      accessibilityRole={onPress ? 'button' : undefined}
      accessibilityLabel={accessibilityLabel}
      onPress={onPress}
      disabled={!onPress}
      style={({ pressed }) => [styles.listCard, pressed && styles.listCardPressed]}
    >
      <View style={styles.listRow}>
        {leading}
        <View style={{ flex: 1, gap: 2 }}>
          <Text style={type.heading} numberOfLines={1}>
            {title}
          </Text>
          {subtitle ? (
            <Text style={type.caption} numberOfLines={2}>
              {subtitle}
            </Text>
          ) : null}
        </View>
        {trailing}
        {onPress ? <Ionicons name="chevron-forward" size={18} color={colors.sand} /> : null}
      </View>
      {children}
    </Pressable>
  );
}

/** Thin sand-coloured rule for separating groups inside a card. */
export function Divider(): React.ReactElement {
  return <View style={styles.divider} />;
}

/** Full-width gradient card with white text for callouts and CTAs. */
export function GradientCard({
  gradient = 'dusk',
  children,
  style,
}: {
  gradient?: Gradient;
  children: React.ReactNode;
  style?: StyleProp<ViewStyle>;
}): React.ReactElement {
  return (
    <View style={[styles.gradientCard, style]}>
      <LinearGradient
        colors={gradients[gradient]}
        start={{ x: 0, y: 0 }}
        end={{ x: 1, y: 1 }}
        style={styles.fill}
      />
      <View style={styles.gradientOrb} />
      <View style={{ gap: 10 }}>{children}</View>
    </View>
  );
}

const styles = StyleSheet.create({
  fill: { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0 },
  avatar: { alignItems: 'center', justifyContent: 'center', ...elevation.low },
  avatarText: { fontFamily: fonts.sansBlack, letterSpacing: -0.5 },
  presence: {
    position: 'absolute',
    right: -1,
    bottom: -1,
    borderRadius: radii.pill,
    borderWidth: 2.5,
    borderColor: colors.surface,
  },
  disc: { alignItems: 'center', justifyContent: 'center' },
  header: {
    borderRadius: radii.xl,
    padding: 22,
    gap: 16,
    overflow: 'hidden',
    ...elevation.mid,
  },
  headerGradient: { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0, opacity: 0.95 },
  headerOrb: {
    pointerEvents: 'none',
    position: 'absolute',
    right: -60,
    top: -80,
    width: 220,
    height: 220,
    borderRadius: 110,
    backgroundColor: 'rgba(255,255,255,0.18)',
  },
  headerOrbSmall: {
    pointerEvents: 'none',
    position: 'absolute',
    left: -30,
    bottom: -70,
    width: 160,
    height: 160,
    borderRadius: 80,
    backgroundColor: 'rgba(255,255,255,0.12)',
  },
  headerRow: { flexDirection: 'row', alignItems: 'center', gap: 16 },
  headerEyebrow: { ...type.eyebrow, color: 'rgba(255,255,255,0.85)' },
  headerTitle: { ...type.title, color: colors.primaryText },
  headerSubtitle: { ...type.body, color: 'rgba(255,255,255,0.9)' },
  sectionHeader: { flexDirection: 'row', alignItems: 'center', gap: 12, paddingHorizontal: 2, marginTop: 4 },
  sectionAction: { fontFamily: fonts.sansBold, fontSize: 14, color: colors.primaryDeep },
  statRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 10 },
  stat: {
    flexGrow: 1,
    flexBasis: 140,
    backgroundColor: colors.surface,
    borderRadius: radii.lg,
    padding: 14,
    gap: 8,
    ...elevation.low,
  },
  statIcon: { width: 30, height: 30, borderRadius: 15, alignItems: 'center', justifyContent: 'center' },
  statValue: { fontFamily: fonts.sansBlack, fontSize: 22, lineHeight: 26, letterSpacing: -0.5, color: colors.text },
  statLabel: { ...type.caption, fontSize: 12 },
  meta: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  empty: {
    alignItems: 'center',
    gap: 10,
    padding: 28,
    borderRadius: radii.xl,
    backgroundColor: colors.surface,
    ...elevation.mid,
  },
  emptyDisc: { width: 88, height: 88, borderRadius: 44, alignItems: 'center', justifyContent: 'center', marginBottom: 6 },
  emptyDiscInner: { width: 60, height: 60, borderRadius: 30, alignItems: 'center', justifyContent: 'center', ...elevation.low },
  emptyText: { textAlign: 'center', color: colors.muted, maxWidth: 360 },
  skeletonCard: {
    backgroundColor: colors.surface,
    borderRadius: radii.lg,
    padding: 18,
    gap: 12,
    ...elevation.low,
  },
  pill: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    paddingHorizontal: 10,
    paddingVertical: 5,
    borderRadius: radii.pill,
  },
  pillDot: { width: 7, height: 7, borderRadius: 4 },
  pillText: { fontFamily: fonts.sansBold, fontSize: 12, letterSpacing: 0.2 },
  listCard: {
    backgroundColor: colors.surface,
    borderRadius: radii.lg,
    padding: 16,
    gap: 12,
    ...elevation.mid,
  },
  listCardPressed: { opacity: 0.92, transform: [{ scale: 0.99 }] },
  listRow: { flexDirection: 'row', alignItems: 'center', gap: 14 },
  divider: { height: StyleSheet.hairlineWidth, backgroundColor: colors.border },
  gradientCard: { borderRadius: radii.xl, padding: 22, overflow: 'hidden', ...elevation.mid },
  gradientOrb: {
    pointerEvents: 'none',
    position: 'absolute',
    right: -50,
    bottom: -60,
    width: 180,
    height: 180,
    borderRadius: 90,
    backgroundColor: 'rgba(255,255,255,0.14)',
  },
});
