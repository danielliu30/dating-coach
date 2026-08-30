import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { Text, View } from 'react-native';

import { ApiError } from '../api/client';
import { Button, Field, Screen } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { shared } from '../theme';

export default function SignInScreen({
  navigation,
}: NativeStackScreenProps<AuthStackParams, 'SignIn'>): React.ReactElement {
  const { signIn } = useAuth();
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
    <Screen>
      <View style={{ gap: 4, paddingTop: 24, paddingBottom: 8 }}>
        <Text style={shared.title}>Welcome back</Text>
        <Text style={shared.muted}>Coaching and conversation feedback, in one place.</Text>
      </View>
      <View style={shared.card}>
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
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Sign in" onPress={submit} loading={busy} />
      </View>
      <Button label="Create an account" variant="secondary" onPress={() => navigation.navigate('SignUp')} />
      <Button label="I have a verification code" variant="secondary" onPress={() => navigation.navigate('Verify')} />
    </Screen>
  );
}
