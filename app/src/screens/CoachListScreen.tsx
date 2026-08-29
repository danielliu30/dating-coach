import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React from 'react';
import { FlatList, Pressable, Text, View } from 'react-native';

import { api } from '../api/client';
import { Badge, Empty, Loading, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { CoachesStackParams } from '../navigation/types';
import { colors, shared } from '../theme';

export default function CoachListScreen({
  navigation,
}: NativeStackScreenProps<CoachesStackParams, 'CoachList'>): React.ReactElement {
  const { data, error, loading } = useAsync(() => api.listCoaches(true));

  return (
    <Screen scroll={false}>
      <Text style={shared.title}>Coaches</Text>
      <Text style={shared.muted}>Book a session or start a live chat with a human coach.</Text>
      {loading && !data ? <Loading /> : null}
      {error ? <Text style={shared.error}>{error}</Text> : null}
      <FlatList
        data={data ?? []}
        keyExtractor={(coach) => coach.id}
        contentContainerStyle={{ gap: 12, paddingVertical: 12 }}
        ListEmptyComponent={loading ? null : <Empty text="No coaches are accepting clients yet." />}
        renderItem={({ item }) => (
          <Pressable
            accessibilityRole="button"
            onPress={() =>
              navigation.navigate('CoachDetail', { coachID: item.id, coachName: item.display_name })
            }
            style={({ pressed }) => [shared.card, pressed && { opacity: 0.9 }]}
          >
            <View style={[shared.row, { justifyContent: 'space-between' }]}>
              <Text style={shared.heading}>{item.display_name}</Text>
              <Text style={shared.muted}>${(item.hourly_rate_cents / 100).toFixed(0)}/hr</Text>
            </View>
            {item.headline ? <Text style={shared.body}>{item.headline}</Text> : null}
            <View style={[shared.row, { flexWrap: 'wrap' }]}>
              {item.specialties.map((specialty) => (
                <Badge key={specialty} text={specialty} tone={colors.primary} />
              ))}
              {item.years_experience > 0 ? <Badge text={`${item.years_experience}y experience`} /> : null}
              <Badge text={item.timezone} />
            </View>
          </Pressable>
        )}
      />
    </Screen>
  );
}
