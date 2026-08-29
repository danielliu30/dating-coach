import React from 'react';
import { Text, View } from 'react-native';

import { Badge, Button, Screen } from '../components/ui';
import { API_BASE_URL } from '../config';
import { useAuth } from '../state/auth';
import { colors, shared } from '../theme';

export default function AccountScreen(): React.ReactElement {
  const { user, signOut, refresh } = useAuth();

  return (
    <Screen>
      <Text style={shared.title}>Account</Text>
      <View style={shared.card}>
        <Text style={shared.heading}>{user?.display_name}</Text>
        <Text style={shared.body}>{user?.email}</Text>
        <View style={shared.row}>
          <Badge text={user?.role ?? 'user'} tone={colors.primary} />
          <Badge
            text={user?.email_verified ? 'email verified' : 'email not verified'}
            tone={user?.email_verified ? colors.engaging : colors.neutral}
          />
        </View>
        <Text style={shared.muted}>API: {API_BASE_URL}</Text>
      </View>
      <Button label="Refresh profile" variant="secondary" onPress={() => void refresh()} />
      <Button label="Sign out" onPress={() => void signOut()} />
    </Screen>
  );
}
