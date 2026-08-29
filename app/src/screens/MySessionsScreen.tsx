import { useNavigation, type NavigationProp } from '@react-navigation/native';
import React, { useState } from 'react';
import { FlatList, Text, View } from 'react-native';

import { api } from '../api/client';
import type { CoachingSession, Slot } from '../api/types';
import { Badge, Button, Empty, Loading, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { RootTabParams } from '../navigation/types';
import { colors, shared } from '../theme';

export const sessionTone = (status: CoachingSession['status']): string =>
  status === 'scheduled' ? colors.engaging : status === 'cancelled' ? colors.flat : colors.muted;

export const formatWhen = (iso: string): string =>
  new Date(iso).toLocaleString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  });

export default function MySessionsScreen(): React.ReactElement {
  const navigation = useNavigation<NavigationProp<RootTabParams>>();
  const { data, error, loading, reload } = useAsync(() => api.mySessions());
  const [busyID, setBusyID] = useState<string | null>(null);
  const [reschedulingID, setReschedulingID] = useState<string | null>(null);
  const [slots, setSlots] = useState<Slot[] | null>(null);
  const [slotError, setSlotError] = useState<string | null>(null);

  const openReschedule = async (session: CoachingSession) => {
    setReschedulingID(session.id);
    setSlots(null);
    setSlotError(null);
    try {
      setSlots(await api.openSlots(session.coach_id, session.duration_minutes));
    } catch (err) {
      setSlotError(err instanceof Error ? err.message : 'could not load open slots');
    }
  };

  const reschedule = async (session: CoachingSession, start: string) => {
    setBusyID(session.id);
    setSlotError(null);
    try {
      await api.rescheduleSession(session.id, start);
      setReschedulingID(null);
      setSlots(null);
      await reload();
    } catch (err) {
      setSlotError(err instanceof Error ? err.message : 'could not reschedule');
    } finally {
      setBusyID(null);
    }
  };

  const cancel = async (sessionID: string) => {
    setBusyID(sessionID);
    try {
      await api.cancelSession(sessionID);
      await reload();
    } finally {
      setBusyID(null);
    }
  };

  const chat = async (session: CoachingSession) => {
    const thread = await api.startThread(session.coach_id, session.id);
    navigation.navigate('Chats', {
      screen: 'Chat',
      params: { threadID: thread.id, title: session.counterpart_name ?? 'Coach' },
    });
  };

  return (
    <Screen scroll={false}>
      <Text style={shared.title}>Your sessions</Text>
      {loading && !data ? <Loading /> : null}
      {error ? <Text style={shared.error}>{error}</Text> : null}
      <FlatList
        data={data ?? []}
        keyExtractor={(session) => session.id}
        contentContainerStyle={{ gap: 12, paddingVertical: 12 }}
        ListEmptyComponent={loading ? null : <Empty text="No sessions booked yet." />}
        renderItem={({ item }) => (
          <View style={shared.card}>
            <View style={[shared.row, { justifyContent: 'space-between' }]}>
              <Text style={shared.heading}>{item.counterpart_name ?? 'Coach'}</Text>
              <Badge text={item.status} tone={sessionTone(item.status)} />
            </View>
            <Text style={shared.body}>{formatWhen(item.scheduled_time)}</Text>
            <Text style={shared.muted}>
              {item.duration_minutes} min{item.topic ? ` · ${item.topic}` : ''}
            </Text>
            {item.coach_notes ? <Text style={shared.body}>Coach notes: {item.coach_notes}</Text> : null}
            {item.status === 'scheduled' ? (
              <View style={shared.row}>
                <View style={{ flex: 1 }}>
                  <Button label="Message coach" variant="secondary" onPress={() => void chat(item)} />
                </View>
                <View style={{ flex: 1 }}>
                  <Button
                    label={reschedulingID === item.id ? 'Close' : 'Reschedule'}
                    variant="secondary"
                    onPress={() => {
                      if (reschedulingID === item.id) {
                        setReschedulingID(null);
                      } else {
                        void openReschedule(item);
                      }
                    }}
                  />
                </View>
                <View style={{ flex: 1 }}>
                  <Button
                    label="Cancel"
                    variant="secondary"
                    loading={busyID === item.id}
                    onPress={() => void cancel(item.id)}
                  />
                </View>
              </View>
            ) : null}
            {reschedulingID === item.id ? (
              <View style={{ gap: 8 }}>
                <Text style={shared.heading}>Pick a new time</Text>
                {slotError ? <Text style={shared.error}>{slotError}</Text> : null}
                {!slots && !slotError ? <Loading /> : null}
                {slots?.length === 0 ? <Empty text="No open slots in the next few weeks." /> : null}
                {slots?.slice(0, 8).map((slot) => (
                  <Button
                    key={slot.start}
                    label={formatWhen(slot.start)}
                    variant="secondary"
                    loading={busyID === item.id}
                    onPress={() => void reschedule(item, slot.start)}
                  />
                ))}
              </View>
            ) : null}
          </View>
        )}
      />
    </Screen>
  );
}
