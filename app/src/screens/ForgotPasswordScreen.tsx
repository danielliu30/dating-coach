import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
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

  const send = async () => {
    if (busy) return;
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await api.forgotPassword(email.trim());
      setSent(true);
      setStatus('If an account exists, a reset code has been sent. Codes expire in 15 minutes after at most 3 wrong attempts.');
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
        {sent ? <Button label="Resend code" variant="secondary" onPress={send} disabled={busy} /> : null}
        <Button label="Back to sign in" variant="secondary" onPress={() => navigation.navigate('SignIn', { email })} disabled={busy} />
      </View>
    </AuthLayout>
  );
}
