import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React from 'react';
import { FlatList, StyleSheet, Text, View } from 'react-native';

import { api } from '../api/client';
import { Avatar, EmptyState, ListCard, MetaRow, PageHeader, SkeletonCard, StatRow, StatTile } from '../components/kit';
import { Badge, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import { PHASE_LABELS } from '../lib/phases';
import type { CoachesStackParams } from '../navigation/types';
import { colors, fonts, shared, type } from '../theme';

export default function CoachListScreen({
  navigation,
}: NativeStackScreenProps<CoachesStackParams, 'CoachList'>): React.ReactElement {
  const { data, error, loading } = useAsync(() => api.listCoaches(true));
  const coaches = data ?? [];
  const specialties = new Set(coaches.flatMap((c) => c.specialties)).size;
  const avgYears = coaches.length
    ? Math.round(coaches.reduce((sum, c) => sum + c.years_experience, 0) / coaches.length)
    : 0;

  return (
    <Screen scroll={false}>
      <FlatList
        data={coaches}
        keyExtractor={(coach) => coach.id}
        contentContainerStyle={{ gap: 12, paddingBottom: 24 }}
        ListHeaderComponent={
          <View style={{ gap: 16, marginBottom: 4 }}>
            <PageHeader
              eyebrow="Dating Humane coaches"
              title="Talk to a real person"
              subtitle="Book a session or start a live chat with a human coach who will actually listen."
              gradient="meadow"
            />
            {coaches.length > 0 ? (
              <StatRow>
                <StatTile value={coaches.length} label="Accepting clients" icon="people-outline" hue="sage" />
                <StatTile value={specialties} label="Specialties" icon="ribbon-outline" hue="sun" />
                <StatTile value={`${avgYears}y`} label="Avg. experience" icon="leaf-outline" hue="sky" />
              </StatRow>
            ) : null}
            {error ? <Text style={shared.error}>{error}</Text> : null}
            {loading && !data ? (
              <>
                <SkeletonCard />
                <SkeletonCard />
              </>
            ) : null}
          </View>
        }
        ListEmptyComponent={
          loading ? null : (
            <EmptyState
              icon="leaf-outline"
              title="No coaches yet"
              text="Nobody is accepting new clients right now. Check back soon — new coaches join regularly."
            />
          )
        }
        renderItem={({ item }) => (
          <ListCard
            onPress={() => navigation.navigate('CoachDetail', { coachID: item.id, coachName: item.display_name })}
            leading={<Avatar name={item.display_name} size={52} />}
            title={item.display_name}
            subtitle={item.headline || 'Human dating coach'}
            trailing={
              <View style={styles.rate}>
                <Text style={styles.rateValue}>${(item.hourly_rate_cents / 100).toFixed(0)}</Text>
                <Text style={styles.rateUnit}>/hr</Text>
              </View>
            }
          >
            {item.specialties.length > 0 || item.phases.length > 0 ? (
              <View style={[shared.row, { flexWrap: 'wrap', gap: 6 }]}>
                {item.phases.map((phase) => (
                  <Badge key={`phase-${phase}`} text={PHASE_LABELS[phase]} tone={colors.primary} />
                ))}
                {item.specialties.map((specialty) => (
                  <Badge key={`specialty-${specialty}`} text={specialty} tone={colors.primaryDeep} />
                ))}
              </View>
            ) : null}
            <View style={styles.metaRow}>
              {item.years_experience > 0 ? (
                <MetaRow icon="leaf-outline" text={`${item.years_experience} years coaching`} />
              ) : null}
              <MetaRow icon="globe-outline" text={item.timezone} />
            </View>
          </ListCard>
        )}
      />
    </Screen>
  );
}

const styles = StyleSheet.create({
  rate: { alignItems: 'flex-end' },
  rateValue: { fontFamily: fonts.sansBlack, fontSize: 20, color: colors.text, letterSpacing: -0.5 },
  rateUnit: { ...type.caption, fontSize: 11 },
  metaRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 14 },
});
