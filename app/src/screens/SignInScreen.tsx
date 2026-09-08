import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { StyleSheet, Text, View } from 'react-native';

import { ApiError } from '../api/client';
import { AuthLayout } from '../components/AuthLayout';
import { Button, Field, Notice } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors } from '../theme';

export default function SignInScreen({
  navigation,
}: NativeStackScreenProps<AuthStackParams, 'SignIn'>): React.ReactElement {
  const { signIn, signedOutReason, dismissSignedOutReason } = useAuth();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setError(null);
    const address = email.trim();
    try {
      await signIn(address, password);
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) {
        navigation.navigate('Verify', { email: address });
        return;
      }
      setError(err instanceof Error ? err.message : 'could not sign in');
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthLayout>
      <View style={{ gap: 4 }}>
        <Text style={styles.title}>Welcome back</Text>
        <Text style={styles.subtitle}>Coaching and conversation feedback, in one place.</Text>
      </View>
      {signedOutReason ? (
        <Notice
          title={signedOutReason === 'revoked' ? 'Your session was ended' : 'You were signed out'}
          text={
            signedOutReason === 'revoked'
              ? 'We ended this session to keep your account safe. Sign in again, and change your password if you did not expect this.'
              : 'Your session expired, so we signed you out to keep your account safe. Sign in to pick up where you left off.'
          }
          onDismiss={dismissSignedOutReason}
        />
      ) : null}
      <View style={styles.card}>
        <Field
          label="Email"
          value={email}
          onChangeText={setEmail}
          autoCapitalize="none"
          keyboardType="email-address"
          autoComplete="email"
          placeholder="you@example.com"
        />
        <Field
          label="Password"
          value={password}
          onChangeText={setPassword}
          secureTextEntry
          autoComplete="current-password"
          placeholder="••••••••"
        />
        {error ? <Text style={styles.error}>{error}</Text> : null}
        <Button label="Sign in" onPress={submit} loading={busy} />
      </View>
      <Button
        label="Create an account"
        variant="secondary"
        onPress={() => navigation.navigate('SignUp')}
      />
    </AuthLayout>
  );
}

const styles = StyleSheet.create({
  title: { fontSize: 28, fontWeight: '700', color: colors.text },
  subtitle: { fontSize: 14, color: colors.muted, lineHeight: 20 },
  card: {
    backgroundColor: colors.surface,
    borderRadius: 14,
    borderWidth: 1,
    borderColor: colors.border,
    padding: 16,
    gap: 10,
  },
  error: { color: colors.flat, fontSize: 14 },
});
