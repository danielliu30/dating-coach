import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { Button, Field, Screen } from '../components/ui';
import type { Role } from '../api/types';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors, shared } from '../theme';

export default function SignUpScreen({
  navigation,
}: NativeStackScreenProps<AuthStackParams, 'SignUp'>): React.ReactElement {
  const { signUp } = useAuth();
  const [email, setEmail] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [role, setRole] = useState<Role>('user');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      await signUp({ email: email.trim(), password, displayName: displayName.trim(), role });
      navigation.navigate('Verify', { email: email.trim() });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not sign up');
    } finally {
      setBusy(false);
    }
  };

  return (
    <Screen>
      <View style={{ gap: 4, paddingTop: 24, paddingBottom: 8 }}>
        <Text style={shared.title}>Create your account</Text>
        <Text style={shared.muted}>We email a verification code right after sign-up.</Text>
      </View>
      <View style={shared.card}>
        <Field label="Name" value={displayName} onChangeText={setDisplayName} placeholder="Alex" />
        <Field
          label="Email"
          value={email}
          onChangeText={setEmail}
          autoCapitalize="none"
          keyboardType="email-address"
          placeholder="you@example.com"
        />
        <Field
          label="Password"
          value={password}
          onChangeText={setPassword}
          secureTextEntry
          placeholder="at least 8 characters"
        />
        <Text style={shared.muted}>I am signing up as</Text>
        <View style={shared.row}>
          {(['user', 'coach'] as const).map((option) => (
            <Pressable
              key={option}
              accessibilityRole="radio"
              accessibilityState={{ selected: role === option }}
              onPress={() => setRole(option)}
              style={[styles.option, role === option && styles.optionActive]}
            >
              <Text style={role === option ? styles.optionActiveLabel : styles.optionLabel}>
                {option === 'user' ? 'Someone dating' : 'A coach'}
              </Text>
            </Pressable>
          ))}
        </View>
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Sign up" onPress={submit} loading={busy} />
      </View>
      <Button label="I already have an account" variant="secondary" onPress={() => navigation.navigate('SignIn')} />
    </Screen>
  );
}

const styles = StyleSheet.create({
  option: {
    flex: 1,
    borderWidth: 1,
    borderColor: colors.border,
    borderRadius: 10,
    paddingVertical: 12,
    alignItems: 'center',
    backgroundColor: colors.surface,
  },
  optionActive: { borderColor: colors.primary, backgroundColor: '#fdf0f4' },
  optionLabel: { color: colors.muted, fontWeight: '600' },
  optionActiveLabel: { color: colors.primary, fontWeight: '700' },
});
