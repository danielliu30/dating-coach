import { Ionicons } from '@expo/vector-icons';
import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { ApiError } from '../api/client';
import { AuthLayout } from '../components/AuthLayout';
import { Divider } from '../components/kit';
import { Button, Field, Notice } from '../components/ui';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors, fonts, shared, type } from '../theme';

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
    <AuthLayout landing>
      <View style={{ gap: 6 }}>
        <Text style={type.eyebrow}>Welcome back</Text>
        <Text style={type.title}>Sign in to Dating Humane</Text>
        <Text style={type.caption}>Pick up your journey where you left it.</Text>
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
      <View style={{ gap: 14 }}>
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
        <Button label="Sign in" onPress={submit} loading={busy} icon="arrow-forward" />
      </View>
      <View style={styles.dividerRow}>
        <Divider />
      </View>
      <View style={styles.footer}>
        <Text style={type.caption}>New here?</Text>
        <Pressable accessibilityRole="button" onPress={() => navigation.navigate('SignUp')} hitSlop={8}>
          <Text style={styles.link}>Create an account</Text>
        </Pressable>
      </View>
      <View style={styles.reassure}>
        <Ionicons name="lock-closed-outline" size={14} color={colors.sageDeep} />
        <Text style={[type.caption, { color: colors.sageDeep }]}>Private by default. We never date on your behalf.</Text>
      </View>
    </AuthLayout>
  );
}

const styles = StyleSheet.create({
  dividerRow: { paddingVertical: 2 },
  footer: { flexDirection: 'row', justifyContent: 'center', alignItems: 'center', gap: 6 },
  link: { fontFamily: fonts.sansBold, fontSize: 14, color: colors.primaryDeep },
  reassure: { flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: 6 },
});
