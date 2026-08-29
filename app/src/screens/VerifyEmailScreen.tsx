import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Linking, Text, View } from 'react-native';

import { Button, Field, Screen } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { shared } from '../theme';

function tokenFromURL(url: string): string | null {
  const query = url.split('?')[1];
  if (!query) return null;
  for (const pair of query.split('&')) {
    const [key, value] = pair.split('=');
    if (key === 'token' && value) return decodeURIComponent(value);
  }
  return null;
}

export default function VerifyEmailScreen({
  route,
  navigation,
}: NativeStackScreenProps<AuthStackParams, 'Verify'>): React.ReactElement {
  const { verify, resendVerification, signOut, token: session, user } = useAuth();
  const [email, setEmail] = useState(route.params?.email ?? user?.email ?? '');
  const [token, setToken] = useState('');
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submitToken = useCallback(
    async (value: string) => {
      setBusy(true);
      setError(null);
      setStatus(null);
      try {
        await verify(value);
        setStatus('Email verified.');
      } catch (err) {
        setError(err instanceof Error ? err.message : 'could not verify');
      } finally {
        setBusy(false);
      }
    },
    [verify],
  );

  const submit = () => submitToken(token.trim());

  // The verification email links to <app>/verify?token=..., so honour a token in
  // the launch URL instead of asking the user to paste it.
  const consumedLink = useRef(false);
  useEffect(() => {
    if (consumedLink.current) return;
    consumedLink.current = true;

    void (async () => {
      const url = await Linking.getInitialURL();
      const linked = url ? tokenFromURL(url) : null;
      if (!linked) return;
      setToken(linked);
      await submitToken(linked);
    })();
  }, [submitToken]);

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
        <Text style={shared.muted}>Open the link in the verification email, or paste its token here.</Text>
      </View>
      <View style={shared.card}>
        <Field label="Email" value={email} onChangeText={setEmail} autoCapitalize="none" keyboardType="email-address" />
        <Field label="Verification token" value={token} onChangeText={setToken} autoCapitalize="none" />
        {status ? <Text style={shared.muted}>{status}</Text> : null}
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Verify" onPress={() => void submit()} loading={busy} />
        <Button label="Resend email" variant="secondary" onPress={resend} />
      </View>
      {session ? (
        <Button label="Sign out" variant="secondary" onPress={() => void signOut()} />
      ) : (
        <Button label="Back to sign in" variant="secondary" onPress={() => navigation.navigate('SignIn')} />
      )}
    </Screen>
  );
}
