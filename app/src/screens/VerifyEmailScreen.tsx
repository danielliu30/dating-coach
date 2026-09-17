import { Ionicons } from '@expo/vector-icons';
import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useCallback, useEffect, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { AuthLayout } from '../components/AuthLayout';
import { Divider, IconDisc } from '../components/kit';
import { Button, Field } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors, fonts, radii, shared, type } from '../theme';

export default function VerifyEmailScreen({
  route,
  navigation,
}: NativeStackScreenProps<AuthStackParams, 'Verify'>): React.ReactElement {
  const { verify, resendVerification, token, user } = useAuth();
  const [email] = useState(route.params?.email ?? user?.email ?? '');
  const [code, setCode] = useState('');
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [resending, setResending] = useState(false);
  const [verified, setVerified] = useState(false);

  // A verify that hands back a session moves the app to the tabs by itself.
  // When it does not — no sign-up session, or one that had already expired and
  // was dropped — the only thing left to do here is sign in.
  useEffect(() => {
    if (verified && !token) navigation.navigate('SignIn');
  }, [verified, token, navigation]);

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await verify(email.trim(), code.trim());
      setStatus('Email verified.');
      setVerified(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not verify');
    } finally {
      setBusy(false);
    }
  }, [verify, email, code]);

  const resend = useCallback(async () => {
    if (resending) return;
    setResending(true);
    setError(null);
    try {
      await resendVerification(email.trim());
      setStatus('Verification code sent.');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not resend');
    } finally {
      setResending(false);
    }
  }, [resending, resendVerification, email]);

  return (
    <AuthLayout>
      <View style={styles.intro}>
        <IconDisc icon="mail-unread-outline" hue="sun" size={56} />
        <View style={{ flex: 1, gap: 4 }}>
          <Text style={type.eyebrow}>One last step</Text>
          <Text style={type.title}>Check your inbox</Text>
        </View>
      </View>
      <Text style={type.body}>
        We sent a 6-digit code to <Text style={{ fontFamily: fonts.sansBold }}>{email || 'your email'}</Text>. Enter it
        below to verify your account.
      </Text>
      <View style={{ gap: 14 }}>
        <Field label="Email" value={email} editable={false} autoCapitalize="none" keyboardType="email-address" />
        <Field
          label="Verification code"
          value={code}
          onChangeText={setCode}
          autoCapitalize="none"
          keyboardType="number-pad"
          maxLength={6}
          placeholder="••••••"
          style={styles.codeInput}
        />
        {status ? (
          <View style={styles.status}>
            <Ionicons name="checkmark-circle" size={16} color={colors.engaging} />
            <Text style={[type.caption, { color: colors.engaging }]}>{status}</Text>
          </View>
        ) : null}
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Verify email" onPress={() => void submit()} loading={busy} icon="shield-checkmark-outline" />
      </View>
      <View style={styles.hint}>
        <Ionicons name="time-outline" size={14} color={colors.muted} />
        <Text style={type.caption}>Codes expire after 3 minutes.</Text>
        <Pressable
          accessibilityRole="button"
          accessibilityState={{ disabled: resending, busy: resending }}
          disabled={resending}
          onPress={() => void resend()}
          hitSlop={8}
        >
          <Text style={[styles.link, resending && styles.linkDisabled]}>{resending ? 'Sending…' : 'Resend code'}</Text>
        </Pressable>
      </View>
      {token ? null : (
        <>
          <Divider />
          <Pressable
            accessibilityRole="button"
            onPress={() => navigation.navigate('SignIn')}
            hitSlop={8}
            style={styles.back}
          >
            <Ionicons name="arrow-back" size={14} color={colors.primaryDeep} />
            <Text style={styles.link}>Back to sign in</Text>
          </Pressable>
        </>
      )}
    </AuthLayout>
  );
}

const styles = StyleSheet.create({
  intro: { flexDirection: 'row', alignItems: 'center', gap: 14 },
  codeInput: {
    fontFamily: fonts.sansBlack,
    fontSize: 24,
    letterSpacing: 8,
    textAlign: 'center',
    borderRadius: radii.md,
  },
  status: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  hint: { flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 6, flexWrap: 'wrap' },
  link: { fontFamily: fonts.sansBold, fontSize: 14, color: colors.primaryDeep },
  linkDisabled: { color: colors.muted },
  back: { flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 6 },
});
