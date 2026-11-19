import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useEffect, useState } from 'react';
import { Text, View } from 'react-native';

import { api } from '../api/client';
import { AuthLayout } from '../components/AuthLayout';
import { Button, Field } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { shared, type } from '../theme';

export default function ForgotPasswordScreen({
  route,
  navigation,
}: NativeStackScreenProps<AuthStackParams, 'ForgotPassword'>): React.ReactElement {
  const [email, setEmail] = useState(route.params?.email ?? '');
  const [sent, setSent] = useState(false);
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [resendUntil, setResendUntil] = useState(0);
  const [remaining, setRemaining] = useState(0);

  useEffect(() => {
    if (!resendUntil) return;
    const timer = setInterval(() => {
      const seconds = Math.max(0, Math.ceil((resendUntil - Date.now()) / 1000));
      setRemaining(seconds);
      if (!seconds) clearInterval(timer);
    }, 1000);
    return () => clearInterval(timer);
  }, [resendUntil]);

  const send = async () => {
    if (busy || (sent && remaining > 0)) return;
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await api.forgotPassword(email.trim());
      setSent(true);
      setCode('');
      setRemaining(60);
      setResendUntil(Date.now() + 60_000);
      setStatus('If an account exists, a reset code has been sent. Codes expire after a short time or 3 wrong attempts.');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not send reset code');
    } finally {
      setBusy(false);
    }
  };

  const reset = async () => {
    if (busy) return;
    setError(null);
    if (Array.from(password).length < 8) {
      setError('Password must be at least 8 characters.');
      return;
    }
    const bytes = Array.from(password).reduce((size, char) => {
      const point = char.codePointAt(0)!;
      return size + (point <= 0x7f ? 1 : point <= 0x7ff ? 2 : point <= 0xffff ? 3 : 4);
    }, 0);
    if (bytes > 72) {
      setError('Password must be at most 72 bytes.');
      return;
    }
    if (!password.trim()) {
      setError('Password cannot be only whitespace.');
      return;
    }
    if (password !== confirm) {
      setError('Passwords do not match.');
      return;
    }
    setBusy(true);
    try {
      await api.resetPassword(email.trim(), code.trim(), password);
      navigation.navigate('SignIn', { email: email.trim(), notice: 'Your password has been reset. Sign in with your new password.' });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Could not reset password');
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout>
      <Text style={type.title}>Reset your password</Text>
      <View style={{ gap: 14 }}>
        <Field label="Email" accessibilityLabel="Email" value={email} onChangeText={setEmail} editable={!sent && !busy} autoCapitalize="none" keyboardType="email-address" autoComplete="email" />
        {sent ? (
          <>
            <Field label="Reset code" accessibilityLabel="Reset code" value={code} onChangeText={setCode} keyboardType="number-pad" maxLength={6} />
            <Field label="New password" accessibilityLabel="New password" value={password} onChangeText={setPassword} secureTextEntry autoComplete="new-password" />
            <Field label="Confirm password" accessibilityLabel="Confirm password" value={confirm} onChangeText={setConfirm} secureTextEntry autoComplete="new-password" />
          </>
        ) : null}
        {status ? <Text style={type.caption}>{status}</Text> : null}
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label={sent ? 'Reset password' : 'Send code'} onPress={sent ? reset : send} loading={busy} disabled={!email.trim()} />
        {sent ? (
          <>
            <Button label={remaining > 0 ? `Resend code (${remaining}s)` : 'Resend code'} variant="secondary" onPress={send} disabled={busy || remaining > 0} />
            <Button label="Use a different email" variant="secondary" disabled={busy} onPress={() => {
              setSent(false);
              setCode('');
              setPassword('');
              setConfirm('');
              setError(null);
              setStatus(null);
            }} />
          </>
        ) : null}
        <Button label="Back to sign in" variant="secondary" onPress={() => navigation.navigate('SignIn', { email })} disabled={busy} />
      </View>
    </AuthLayout>
  );
}
