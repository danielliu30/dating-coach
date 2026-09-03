import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { StyleSheet, Text, View } from 'react-native';

import { AuthLayout } from '../components/AuthLayout';
import { Button, Field } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors } from '../theme';

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
    try {
      await signIn(email.trim(), password);
    } catch (err) {
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
