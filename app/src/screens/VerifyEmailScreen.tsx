import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useCallback, useState } from 'react';
import { Text, View } from 'react-native';

import { Button, Field, Screen } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { shared } from '../theme';

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

  const submit = useCallback(async () => {
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      // With a sign-up session on hand a successful verify swaps in a full
      // session and the navigator leaves this stack on its own; without one
      // there are no credentials to show, so hand the user to sign-in.
      const signedIn = Boolean(token);
      await verify(email.trim(), code.trim());
      if (!signedIn) {
        navigation.navigate('SignIn');
        return;
      }
      setStatus('Email verified.');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not verify');
    } finally {
      setBusy(false);
    }
  }, [verify, email, code, token, navigation]);

  const resend = async () => {
    setError(null);
    try {
      await resendVerification(email.trim());
      setStatus('Verification code sent.');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not resend');
    }
  };

  return (
    <Screen>
      <View style={{ gap: 4, paddingTop: 24, paddingBottom: 8 }}>
        <Text style={shared.title}>Verify your email</Text>
        <Text style={shared.muted}>Enter the 6-digit code sent to your email. It expires in 3 minutes.</Text>
      </View>
      <View style={shared.card}>
        <Field label="Email" value={email} editable={false} autoCapitalize="none" keyboardType="email-address" />
        <Field
          label="Verification code"
          value={code}
          onChangeText={setCode}
          autoCapitalize="none"
          keyboardType="number-pad"
          maxLength={6}
        />
        {status ? <Text style={shared.muted}>{status}</Text> : null}
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Verify" onPress={() => void submit()} loading={busy} />
        <Button label="Resend code" variant="secondary" onPress={resend} />
      </View>
      {token ? null : (
        <Button label="Back to sign in" variant="secondary" onPress={() => navigation.navigate('SignIn')} />
      )}
    </Screen>
  );
}
