import { useNavigation, type NavigationProp } from '@react-navigation/native';
import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import { api } from '../api/client';
import type { Slot } from '../api/types';
import { Badge, Button, Field, Loading, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { CoachesStackParams, RootTabParams } from '../navigation/types';
import { colors, shared } from '../theme';

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const DURATIONS = [30, 45, 60];

const minutesToClock = (minute: number): string =>
  `${String(Math.floor(minute / 60)).padStart(2, '0')}:${String(minute % 60).padStart(2, '0')}`;

const slotLabel = (slot: Slot): string =>
  new Date(slot.start).toLocaleString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  });

export default function CoachDetailScreen({
  route,
}: NativeStackScreenProps<CoachesStackParams, 'CoachDetail'>): React.ReactElement {
  const { coachID } = route.params;
  const navigation = useNavigation<NavigationProp<RootTabParams>>();

  const [duration, setDuration] = useState(45);
  const [topic, setTopic] = useState('');
  const [selected, setSelected] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const coach = useAsync(() => api.getCoach(coachID), [coachID]);
  const availability = useAsync(() => api.coachAvailability(coachID), [coachID]);
  const slots = useAsync(() => api.openSlots(coachID, duration), [coachID, duration]);

  const book = async () => {
    if (!selected) {
      setError('Pick a time slot first.');
      return;
    }
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await api.bookSession({
        coach_id: coachID,
        scheduled_time: selected,
        duration_minutes: duration,
        topic: topic.trim(),
      });
      setStatus('Session booked — see it under Sessions.');
      setSelected(null);
      await slots.reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not book');
    } finally {
      setBusy(false);
    }
  };

  const startChat = async () => {
    setError(null);
    try {
      const thread = await api.startThread(coachID);
      navigation.navigate('Chats', {
        screen: 'Chat',
        params: { threadID: thread.id, title: route.params.coachName },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not start chat');
    }
  };

  if (!coach.data) {
    return (
      <Screen>
        {coach.loading ? <Loading /> : <Text style={shared.error}>{coach.error}</Text>}
      </Screen>
    );
  }

  return (
    <Screen>
      <View style={shared.card}>
        <Text style={shared.title}>{coach.data.display_name}</Text>
        {coach.data.headline ? <Text style={shared.heading}>{coach.data.headline}</Text> : null}
        {coach.data.bio ? <Text style={shared.body}>{coach.data.bio}</Text> : null}
        <View style={[shared.row, { flexWrap: 'wrap' }]}>
          {coach.data.specialties.map((specialty) => (
            <Badge key={specialty} text={specialty} tone={colors.primary} />
          ))}
          <Badge text={`$${(coach.data.hourly_rate_cents / 100).toFixed(0)}/hr`} />
          <Badge text={coach.data.timezone} />
        </View>
        <Button label="Start a live chat" variant="secondary" onPress={startChat} />
      </View>

      <View style={shared.card}>
        <Text style={shared.heading}>Weekly availability</Text>
        {availability.data?.length ? (
          availability.data.map((window, index) => (
            <Text key={`${window.weekday}-${index}`} style={shared.body}>
              {WEEKDAYS[window.weekday] ?? window.weekday} {minutesToClock(window.start_minute)} –{' '}
              {minutesToClock(window.end_minute)} ({coach.data?.timezone})
            </Text>
          ))
        ) : (
          <Text style={shared.muted}>This coach has not published availability yet.</Text>
        )}
      </View>

      <View style={shared.card}>
        <Text style={shared.heading}>Book a session</Text>
        <Text style={shared.muted}>Session length</Text>
        <View style={shared.row}>
          {DURATIONS.map((minutes) => (
            <Pressable
              key={minutes}
              accessibilityRole="radio"
              accessibilityState={{ selected: duration === minutes }}
              onPress={() => setDuration(minutes)}
              style={[styles.chip, duration === minutes && styles.chipActive]}
            >
              <Text style={duration === minutes ? styles.chipActiveLabel : styles.chipLabel}>
                {minutes} min
              </Text>
            </Pressable>
          ))}
        </View>

        <Text style={shared.muted}>Open slots (next 7 days)</Text>
        {slots.loading && !slots.data ? <Loading /> : null}
        {slots.data?.length === 0 ? <Text style={shared.muted}>No open slots in this window.</Text> : null}
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: 8 }}>
          {(slots.data ?? []).map((slot) => (
            <Pressable
              key={slot.start}
              accessibilityRole="button"
              onPress={() => setSelected(slot.start)}
              style={[styles.chip, selected === slot.start && styles.chipActive]}
            >
              <Text style={selected === slot.start ? styles.chipActiveLabel : styles.chipLabel}>
                {slotLabel(slot)}
              </Text>
            </Pressable>
          ))}
        </ScrollView>

        <Field
          label="What do you want to work on?"
          value={topic}
          onChangeText={setTopic}
          placeholder="Opening messages on Hinge"
        />
        {status ? <Text style={shared.muted}>{status}</Text> : null}
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Book session" onPress={book} loading={busy} />
      </View>
    </Screen>
  );
}

const styles = StyleSheet.create({
  chip: {
    borderWidth: 1,
    borderColor: colors.border,
    backgroundColor: colors.surface,
    borderRadius: 999,
    paddingVertical: 9,
    paddingHorizontal: 14,
  },
  chipActive: { borderColor: colors.primary, backgroundColor: '#fdf0f4' },
  chipLabel: { color: colors.muted, fontWeight: '600', fontSize: 13 },
  chipActiveLabel: { color: colors.primary, fontWeight: '700', fontSize: 13 },
});
