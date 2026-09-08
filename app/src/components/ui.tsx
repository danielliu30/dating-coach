import React from 'react';
import {
  ActivityIndicator,
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

import { colors, scoreColor, shared } from '../theme';

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
  return (
    <Pressable
      accessibilityRole="button"
      onPress={onPress}
      disabled={disabled || loading}
      style={({ pressed }) => [
        styles.button,
        isPrimary ? styles.buttonPrimary : styles.buttonSecondary,
        (pressed || loading) && styles.buttonPressed,
        (disabled || loading) && styles.buttonDisabled,
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
  return (
    <View style={{ gap: 6 }}>
      <Text style={shared.muted}>{label}</Text>
      <TextInput
        placeholderTextColor={colors.muted}
        {...props}
        style={[
          styles.input,
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
      <View style={[styles.barFill, { width: `${pct}%`, backgroundColor: scoreColor(score) }]} />
    </View>
  );
}

export function Badge({ text, tone = colors.muted }: { text: string; tone?: string }): React.ReactElement {
  return (
    <View style={[styles.badge, { borderColor: tone }]}>
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
      <View style={{ flex: 1, gap: 2 }}>
        <Text style={styles.noticeTitle}>{title}</Text>
        <Text style={styles.noticeText}>{text}</Text>
      </View>
      {onDismiss ? (
        <Pressable accessibilityRole="button" accessibilityLabel="Dismiss" onPress={onDismiss} hitSlop={8}>
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
        { borderColor: selected ? tone : colors.border, backgroundColor: selected ? tone : colors.surface },
        pressed && styles.buttonPressed,
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
      <ActivityIndicator color={colors.primary} />
      {label ? <Text style={shared.muted}>{label}</Text> : null}
    </View>
  );
}

export function Empty({ text }: { text: string }): React.ReactElement {
  return (
    <View style={styles.center}>
      <Text style={shared.muted}>{text}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  button: {
    borderRadius: 12,
    paddingVertical: 13,
    paddingHorizontal: 18,
    alignItems: 'center',
    justifyContent: 'center',
    minHeight: 48,
  },
  buttonPrimary: { backgroundColor: colors.primary },
  buttonSecondary: { backgroundColor: colors.surface, borderWidth: 1, borderColor: colors.primary },
  buttonPressed: { opacity: 0.85 },
  buttonDisabled: { opacity: 0.5 },
  buttonPrimaryLabel: { color: colors.primaryText, fontWeight: '600', fontSize: 15 },
  buttonSecondaryLabel: { color: colors.primary, fontWeight: '600', fontSize: 15 },
  input: {
    backgroundColor: colors.surface,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: 10,
    paddingHorizontal: 12,
    paddingVertical: 11,
    fontSize: 15,
    color: colors.text,
  },
  inputMultiline: { minHeight: 120, textAlignVertical: 'top' },
  inputDisabled: { backgroundColor: colors.bg, color: colors.muted },
  barTrack: {
    height: 8,
    borderRadius: 4,
    backgroundColor: colors.border,
    overflow: 'hidden',
  },
  barFill: { height: 8, borderRadius: 4 },
  badge: {
    borderWidth: 1,
    borderRadius: 999,
    paddingHorizontal: 9,
    paddingVertical: 3,
  },
  badgeText: { fontSize: 12, fontWeight: '600' },
  chip: {
    borderWidth: 1,
    borderRadius: 999,
    paddingHorizontal: 14,
    paddingVertical: 8,
    minHeight: 36,
    justifyContent: 'center',
  },
  chipText: { fontSize: 14, fontWeight: '600' },
  center: { paddingVertical: 32, alignItems: 'center', gap: 8 },
  notice: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: 12,
    backgroundColor: colors.noticeBg,
    borderWidth: 1,
    borderColor: colors.neutral,
    borderRadius: 12,
    padding: 14,
  },
  noticeTitle: { fontSize: 15, fontWeight: '600', color: colors.text },
  noticeText: { fontSize: 14, color: colors.text, lineHeight: 20 },
  noticeDismiss: { fontSize: 15, color: colors.muted },
});
