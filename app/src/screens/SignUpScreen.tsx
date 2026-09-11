import { Ionicons } from '@expo/vector-icons';
import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { AuthLayout } from '../components/AuthLayout';
import { Divider } from '../components/kit';
import { Button, Field } from '../components/ui';
import type { Role } from '../api/types';
import type { AuthStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors, fonts, radii, shared, type } from '../theme';

const ROLE_OPTIONS: {
  value: Role;
  icon: React.ComponentProps<typeof Ionicons>['name'];
  title: string;
  text: string;
}[] = [
  { value: 'user', icon: 'heart-outline', title: 'Someone dating', text: 'Get feedback and coaching' },
  { value: 'coach', icon: 'people-outline', title: 'A coach', text: 'Offer sessions to daters' },
];

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
    <AuthLayout>
      <View style={{ gap: 6 }}>
        <Text style={type.eyebrow}>Join Dating Humane</Text>
        <Text style={type.title}>Create your account</Text>
        <Text style={type.caption}>We email a verification code right after sign-up.</Text>
      </View>
      <View style={{ gap: 14 }}>
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
        <View style={{ gap: 8 }}>
          <Text style={styles.roleLabel}>I am signing up as</Text>
          <View style={styles.row}>
            {ROLE_OPTIONS.map((option) => {
              const active = role === option.value;
              return (
                <Pressable
                  key={option.value}
                  accessibilityRole="radio"
                  accessibilityState={{ selected: active }}
                  onPress={() => setRole(option.value)}
                  style={[styles.option, active && styles.optionActive]}
                >
                  <View style={[styles.optionIcon, active && styles.optionIconActive]}>
                    <Ionicons name={option.icon} size={18} color={active ? colors.primaryText : colors.sageDeep} />
                  </View>
                  <Text style={[styles.optionTitle, active && { color: colors.primaryDeep }]}>{option.title}</Text>
                  <Text style={styles.optionText}>{option.text}</Text>
                  {active ? (
                    <Ionicons name="checkmark-circle" size={18} color={colors.primary} style={styles.optionCheck} />
                  ) : null}
                </Pressable>
              );
            })}
          </View>
        </View>
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Create account" onPress={submit} loading={busy} icon="sparkles-outline" />
      </View>
      <Divider />
      <View style={styles.footer}>
        <Text style={type.caption}>Already have an account?</Text>
        <Pressable accessibilityRole="button" onPress={() => navigation.navigate('SignIn')} hitSlop={8}>
          <Text style={styles.link}>Sign in</Text>
        </Pressable>
      </View>
    </AuthLayout>
  );
}

const styles = StyleSheet.create({
  roleLabel: {
    fontFamily: fonts.sansBold,
    fontSize: 11.5,
    letterSpacing: 0.8,
    textTransform: 'uppercase',
    color: colors.bark,
    paddingHorizontal: 2,
  },
  row: { flexDirection: 'row', gap: 10 },
  option: {
    flex: 1,
    gap: 6,
    padding: 14,
    borderRadius: radii.md,
    borderWidth: 1.5,
    borderColor: colors.border,
    backgroundColor: colors.surfaceAlt,
  },
  optionActive: { borderColor: colors.primary, backgroundColor: colors.primaryTint },
  optionIcon: {
    width: 34,
    height: 34,
    borderRadius: 17,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: colors.sageTint,
  },
  optionIconActive: { backgroundColor: colors.primary },
  optionTitle: { fontFamily: fonts.sansBold, fontSize: 14, color: colors.text },
  optionText: { ...type.caption, fontSize: 12 },
  optionCheck: { position: 'absolute', top: 10, right: 10 },
  footer: { flexDirection: 'row', justifyContent: 'center', alignItems: 'center', gap: 6 },
  link: { fontFamily: fonts.sansBold, fontSize: 14, color: colors.primaryDeep },
});
