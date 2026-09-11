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

import { colors, elevation, radii, scoreColor, shared } from '../theme';

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
}: {
  label: string;
  onPress: () => void;
  variant?: 'primary' | 'secondary';
  disabled?: boolean;
  loading?: boolean;
}): React.ReactElement {
  const isPrimary = variant === 'primary';
  const inactive = disabled || loading;
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
        (pressed || loading) && (isPrimary ? styles.buttonPrimaryPressed : styles.buttonSecondaryPressed),
        inactive && styles.buttonDisabled,
      ]}
    >
      {loading ? (
        <ActivityIndicator color={isPrimary ? colors.primaryText : colors.primary} />
      ) : (
        <Text style={isPrimary ? styles.buttonPrimaryLabel : styles.buttonSecondaryLabel}>{label}</Text>
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

export function Empty({ text }: { text: string }): React.ReactElement {
  return (
    <View style={styles.center}>
      <View style={styles.emptyCard}>
        <Text style={[shared.muted, styles.emptyText]}>{text}</Text>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  screenGlow: {
    position: 'absolute',
    top: -120,
    left: -80,
    right: -80,
    height: 320,
    borderBottomLeftRadius: 400,
    borderBottomRightRadius: 400,
    backgroundColor: colors.primaryTint,
    opacity: 0.55,
  },
  button: {
    borderRadius: radii.md,
    paddingVertical: 14,
    paddingHorizontal: 20,
    alignItems: 'center',
    justifyContent: 'center',
    minHeight: 50,
  },
  buttonPrimary: {
    backgroundColor: colors.primary,
    borderBottomWidth: 3,
    borderBottomColor: colors.primaryDeep,
  },
  buttonSecondary: {
    backgroundColor: colors.surface,
    borderWidth: 1.5,
    borderColor: colors.primaryTint,
  },
  buttonPrimaryPressed: { backgroundColor: colors.primaryDeep, borderBottomWidth: 0, paddingTop: 17 },
  buttonSecondaryPressed: { backgroundColor: colors.primaryTint },
  buttonDisabled: { opacity: 0.5 },
  buttonPrimaryLabel: { color: colors.primaryText, fontWeight: '700', fontSize: 15, letterSpacing: 0.2 },
  buttonSecondaryLabel: { color: colors.primary, fontWeight: '700', fontSize: 15, letterSpacing: 0.2 },
  field: { gap: 6 },
  fieldLabel: {
    ...shared.muted,
    fontWeight: '600',
    textTransform: 'uppercase',
    letterSpacing: 0.6,
    fontSize: 12,
    paddingHorizontal: 2,
  },
  input: {
    backgroundColor: colors.surfaceAlt,
    borderWidth: 1.5,
    borderColor: colors.border,
    borderRadius: radii.md,
    paddingHorizontal: 14,
    paddingVertical: 12,
    fontSize: 15,
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
  badgeText: { fontSize: 12, fontWeight: '700', letterSpacing: 0.2 },
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
  chipText: { fontSize: 14, fontWeight: '600' },
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
    paddingVertical: 18,
    paddingHorizontal: 24,
    borderRadius: radii.lg,
    backgroundColor: colors.surfaceAlt,
    borderWidth: 1.5,
    borderColor: colors.border,
    borderStyle: 'dashed',
  },
  emptyText: { textAlign: 'center' },
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
  noticeTitle: { fontSize: 15, fontWeight: '700', color: colors.text },
  noticeText: { fontSize: 14, color: colors.text, lineHeight: 20 },
  noticeDismissButton: {
    width: 28,
    height: 28,
    borderRadius: 14,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: 'rgba(59, 20, 32, 0.06)',
  },
  noticeDismiss: { fontSize: 13, color: colors.muted, fontWeight: '700' },
});
