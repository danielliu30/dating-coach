import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import React, { useState } from 'react';
import {
  ActivityIndicator,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
  type StyleProp,
  type TextInputProps,
  type ViewStyle,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, elevation, fonts, gradients, radii, scoreColor, shared } from '../theme';

export type IconName = React.ComponentProps<typeof Ionicons>['name'];

/** Screen shell: safe area + centred, max-width content column for web. */
export function Screen({
  children,
  scroll = true,
  contentStyle,
}: {
  children: React.ReactNode;
  scroll?: boolean;
  contentStyle?: StyleProp<ViewStyle>;
}): React.ReactElement {
  const body = scroll ? (
    <ScrollView contentContainerStyle={[shared.content, contentStyle]} keyboardShouldPersistTaps="handled">
      {children}
    </ScrollView>
  ) : (
    <View style={[shared.content, { flex: 1 }, contentStyle]}>{children}</View>
  );
  return (
    <SafeAreaView style={shared.screen} edges={['top', 'left', 'right']}>
      <View pointerEvents="none" style={styles.screenGlow} />
      <View pointerEvents="none" style={styles.screenGlowSage} />
      {body}
    </SafeAreaView>
  );
}

export function Button({
  label,
  onPress,
  variant = 'primary',
  disabled,
  loading,
  icon,
}: {
  label: string;
  onPress: () => void;
  variant?: 'primary' | 'secondary';
  disabled?: boolean;
  loading?: boolean;
  icon?: IconName;
}): React.ReactElement {
  const isPrimary = variant === 'primary';
  const inactive = disabled || loading;
  const fg = isPrimary ? colors.primaryText : colors.primaryDeep;
  return (
    <Pressable
      accessibilityRole="button"
      onPress={onPress}
      disabled={inactive}
      style={({ pressed }) => [
        styles.button,
        isPrimary ? styles.buttonPrimary : styles.buttonSecondary,
        isPrimary && !inactive && elevation.primary,
        !isPrimary && !inactive && elevation.low,
        pressed && styles.buttonPressed,
        !isPrimary && pressed && styles.buttonSecondaryPressed,
        inactive && styles.buttonDisabled,
      ]}
    >
      {isPrimary ? (
        <LinearGradient
          pointerEvents="none"
          colors={gradients.primary}
          start={{ x: 0, y: 0 }}
          end={{ x: 1, y: 1 }}
          style={styles.buttonGradient}
        />
      ) : null}
      {loading ? (
        <ActivityIndicator color={fg} />
      ) : (
        <View style={styles.buttonInner}>
          {icon ? <Ionicons name={icon} size={18} color={fg} /> : null}
          <Text style={[styles.buttonLabel, { color: fg }]}>{label}</Text>
        </View>
      )}
    </Pressable>
  );
}

export function Field({
  label,
  ...props
}: TextInputProps & { label: string }): React.ReactElement {
  const [focused, setFocused] = useState(false);
  const onFocus: NonNullable<TextInputProps['onFocus']> = (e) => {
    setFocused(true);
    props.onFocus?.(e);
  };
  const onBlur: NonNullable<TextInputProps['onBlur']> = (e) => {
    setFocused(false);
    props.onBlur?.(e);
  };
  return (
    <View style={styles.field}>
      <Text style={styles.fieldLabel}>{label}</Text>
      <TextInput
        placeholderTextColor={colors.muted}
        {...props}
        onFocus={onFocus}
        onBlur={onBlur}
        style={[
          styles.input,
          focused && styles.inputFocused,
          props.multiline && styles.inputMultiline,
          props.editable === false && styles.inputDisabled,
          props.style,
        ]}
      />
    </View>
  );
}

export function ScoreBar({ score }: { score: number }): React.ReactElement {
  const pct = Math.round(Math.max(0, Math.min(1, score)) * 100);
  return (
    <View style={styles.barTrack}>
      <View style={[styles.barFill, { width: `${pct}%`, backgroundColor: scoreColor(score) }]}>
        <View style={styles.barSheen} />
      </View>
    </View>
  );
}

/** 10% tint of a `#rrggbb` colour; non-hex tones fall back to the alt surface. */
const tint = (color: string): string => (/^#[0-9a-f]{6}$/i.test(color) ? `${color}1a` : colors.surfaceAlt);

export function Badge({ text, tone = colors.muted }: { text: string; tone?: string }): React.ReactElement {
  return (
    <View style={[styles.badge, { borderColor: tone, backgroundColor: tint(tone) }]}>
      <Text style={[styles.badgeText, { color: tone }]}>{text}</Text>
    </View>
  );
}

/**
 * A banner for something the app did to the user rather than something the
 * user did: it stands apart from field-level errors, and can be dismissed when
 * `onDismiss` is given.
 */
export function Notice({
  title,
  text,
  onDismiss,
}: {
  title: string;
  text: string;
  onDismiss?: () => void;
}): React.ReactElement {
  return (
    <View accessibilityRole="alert" style={styles.notice}>
      <View style={styles.noticeAccent} />
      <View style={{ flex: 1, gap: 2 }}>
        <Text style={styles.noticeTitle}>{title}</Text>
        <Text style={styles.noticeText}>{text}</Text>
      </View>
      {onDismiss ? (
        <Pressable
          accessibilityRole="button"
          accessibilityLabel="Dismiss"
          onPress={onDismiss}
          hitSlop={8}
          style={({ pressed }) => [styles.noticeDismissButton, pressed && styles.buttonDisabled]}
        >
          <Text style={styles.noticeDismiss}>✕</Text>
        </Pressable>
      ) : null}
    </View>
  );
}

/**
 * Toggleable pill for multi-select lists. `tone` colours the selected state;
 * a disabled chip renders dimmed and ignores presses.
 */
export function Chip({
  label,
  selected,
  onPress,
  tone = colors.primary,
  disabled,
}: {
  label: string;
  selected: boolean;
  onPress: () => void;
  tone?: string;
  disabled?: boolean;
}): React.ReactElement {
  return (
    <Pressable
      accessibilityRole="checkbox"
      accessibilityState={{ checked: selected, disabled }}
      onPress={onPress}
      disabled={disabled}
      style={({ pressed }) => [
        styles.chip,
        selected
          ? { borderColor: tone, backgroundColor: tone, shadowColor: tone, ...elevation.low }
          : styles.chipIdle,
        pressed && styles.chipPressed,
        disabled && styles.buttonDisabled,
      ]}
    >
      <Text style={[styles.chipText, { color: selected ? colors.primaryText : colors.text }]}>{label}</Text>
    </Pressable>
  );
}

export function Loading({ label }: { label?: string }): React.ReactElement {
  return (
    <View style={styles.center}>
      <View style={styles.centerBubble}>
        <ActivityIndicator color={colors.primary} />
      </View>
      {label ? <Text style={shared.muted}>{label}</Text> : null}
    </View>
  );
}

export function Empty({ text, icon = 'leaf-outline' }: { text: string; icon?: IconName }): React.ReactElement {
  return (
    <View style={styles.center}>
      <View style={styles.emptyCard}>
        <View style={styles.emptyIcon}>
          <Ionicons name={icon} size={22} color={colors.sageDeep} />
        </View>
        <Text style={[shared.body, styles.emptyText]}>{text}</Text>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  screenGlow: {
    position: 'absolute',
    top: -160,
    right: -120,
    width: 420,
    height: 420,
    borderRadius: 210,
    backgroundColor: colors.primaryTint,
    opacity: 0.7,
  },
  screenGlowSage: {
    position: 'absolute',
    top: 40,
    left: -180,
    width: 360,
    height: 360,
    borderRadius: 180,
    backgroundColor: colors.sageTint,
    opacity: 0.8,
  },
  button: {
    borderRadius: radii.pill,
    paddingVertical: 14,
    paddingHorizontal: 22,
    alignItems: 'center',
    justifyContent: 'center',
    minHeight: 52,
    overflow: 'hidden',
  },
  buttonGradient: { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0 },
  buttonInner: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  buttonPrimary: { backgroundColor: colors.primary },
  buttonSecondary: {
    backgroundColor: colors.surface,
    borderWidth: 1.5,
    borderColor: colors.primaryTint,
  },
  buttonPressed: { transform: [{ scale: 0.98 }], opacity: 0.92 },
  buttonSecondaryPressed: { backgroundColor: colors.primaryTint },
  buttonDisabled: { opacity: 0.5 },
  buttonLabel: { fontFamily: fonts.sansBold, fontSize: 15, letterSpacing: 0.2 },
  field: { gap: 6 },
  fieldLabel: {
    ...shared.muted,
    fontFamily: fonts.sansBold,
    textTransform: 'uppercase',
    letterSpacing: 0.8,
    fontSize: 11.5,
    paddingHorizontal: 2,
    color: colors.bark,
  },
  input: {
    backgroundColor: colors.surfaceAlt,
    borderWidth: 1.5,
    borderColor: colors.border,
    borderRadius: radii.md,
    paddingHorizontal: 16,
    paddingVertical: 13,
    fontSize: 15,
    fontFamily: fonts.sans,
    color: colors.text,
    ...elevation.low,
  },
  inputFocused: {
    borderColor: colors.primary,
    backgroundColor: colors.surface,
    ...Platform.select({ web: { outlineWidth: 0 }, default: {} }),
    ...elevation.mid,
  },
  inputMultiline: { minHeight: 120, textAlignVertical: 'top' },
  inputDisabled: { backgroundColor: colors.surfaceSunken, color: colors.muted, borderColor: 'transparent' },
  barTrack: {
    height: 10,
    borderRadius: 5,
    backgroundColor: colors.surfaceSunken,
    overflow: 'hidden',
  },
  barFill: { height: 10, borderRadius: 5, overflow: 'hidden' },
  barSheen: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    height: 5,
    backgroundColor: 'rgba(255, 255, 255, 0.3)',
  },
  badge: {
    borderWidth: 1,
    borderRadius: radii.pill,
    paddingHorizontal: 10,
    paddingVertical: 4,
  },
  badgeText: { fontSize: 12, fontFamily: fonts.sansBold, letterSpacing: 0.2 },
  chip: {
    borderWidth: 1.5,
    borderRadius: radii.pill,
    paddingHorizontal: 16,
    paddingVertical: 9,
    minHeight: 38,
    justifyContent: 'center',
  },
  chipIdle: { borderColor: colors.border, backgroundColor: colors.surface, ...elevation.low },
  chipPressed: { opacity: 0.85, transform: [{ scale: 0.97 }] },
  chipText: { fontSize: 14, fontFamily: fonts.sansSemi },
  center: { paddingVertical: 36, alignItems: 'center', gap: 12 },
  centerBubble: {
    width: 56,
    height: 56,
    borderRadius: 28,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.surface,
    ...elevation.mid,
  },
  emptyCard: {
    alignItems: 'center',
    gap: 12,
    paddingVertical: 26,
    paddingHorizontal: 28,
    borderRadius: radii.lg,
    backgroundColor: colors.surfaceAlt,
    borderWidth: 1.5,
    borderColor: colors.sand,
    borderStyle: 'dashed',
    width: '100%',
  },
  emptyIcon: {
    width: 48,
    height: 48,
    borderRadius: 24,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.sageTint,
  },
  emptyText: { textAlign: 'center', color: colors.muted },
  notice: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: 12,
    backgroundColor: colors.noticeBg,
    borderRadius: radii.md,
    padding: 14,
    paddingLeft: 16,
    ...elevation.low,
  },
  noticeAccent: {
    position: 'absolute',
    top: 10,
    bottom: 10,
    left: 0,
    width: 4,
    borderRadius: 2,
    backgroundColor: colors.neutral,
  },
  noticeTitle: { fontSize: 15, fontFamily: fonts.sansBold, color: colors.text },
  noticeText: { fontSize: 14, fontFamily: fonts.sans, color: colors.text, lineHeight: 20 },
  noticeDismissButton: {
    width: 28,
    height: 28,
    borderRadius: 14,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: 'rgba(59, 20, 32, 0.06)',
  },
  noticeDismiss: { fontSize: 13, color: colors.muted, fontFamily: fonts.sansBold },
});
