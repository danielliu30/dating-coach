import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { Text, View } from 'react-native';

import { Button, Field, Screen } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { shared } from '../theme';

export default function VerifyEmailScreen({
  route,
  navigation,
}: NativeStackScreenProps<AuthStackParams, 'Verify'>): React.ReactElement {
  const { verify, resendVerification, user } = useAuth();
  const [email, setEmail] = useState(route.params?.email ?? user?.email ?? '');
  const [token, setToken] = useState('');
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await verify(token.trim());
      setStatus('Email verified.');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not verify');
    } finally {
      setBusy(false);
    }
  };

  const resend = async () => {
    setError(null);
    try {
      await resendVerification(email.trim());
      setStatus('Verification email sent.');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not resend');
    }
  };

  return (
    <Screen>
      <View style={{ gap: 4, paddingTop: 24, paddingBottom: 8 }}>
        <Text style={shared.title}>Verify your email</Text>
        <Text style={shared.muted}>Paste the token from the verification email.</Text>
      </View>
      <View style={shared.card}>
        <Field label="Email" value={email} onChangeText={setEmail} autoCapitalize="none" keyboardType="email-address" />
        <Field label="Verification token" value={token} onChangeText={setToken} autoCapitalize="none" />
        {status ? <Text style={shared.muted}>{status}</Text> : null}
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Verify" onPress={submit} loading={busy} />
        <Button label="Resend email" variant="secondary" onPress={resend} />
      </View>
      <Button label="Back to sign in" variant="secondary" onPress={() => navigation.navigate('SignIn')} />
    </Screen>
  );
}
