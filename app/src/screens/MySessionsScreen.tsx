import { Ionicons } from '@expo/vector-icons';
import { useNavigation, type NavigationProp } from '@react-navigation/native';
import React, { useRef, useState } from 'react';
import { FlatList, Linking, Pressable, StyleSheet, Text, View } from 'react-native';

import { api } from '../api/client';
import type { CoachingSession, Slot } from '../api/types';
import {
  Avatar,
  Divider,
  EmptyState,
  MetaRow,
  PageHeader,
  SkeletonCard,
  StatRow,
  StatTile,
  StatusPill,
} from '../components/kit';
import { Button, Empty, Loading, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { RootTabParams } from '../navigation/types';
import { colors, fonts, radii, shared, type } from '../theme';

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

/** Human label for a session status, e.g. `pending_payment` → "Pending payment". */
const statusLabel = (status: CoachingSession['status']): string =>
  status.replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

/** Month abbreviation and day-of-month for the calendar tile. */
const calendarParts = (iso: string): { month: string; day: string } => {
  const d = new Date(iso);
  return {
    month: d.toLocaleString(undefined, { month: 'short' }).toUpperCase(),
    day: String(d.getDate()),
  };
};

/** A request a client can fire from a session card: cancel, or reschedule to a given start. */
type SessionAction = 'cancel' | `reschedule:${string}`;

export default function MySessionsScreen(): React.ReactElement {
  const navigation = useNavigation<NavigationProp<RootTabParams>>();
  const { data, error, loading, reload } = useAsync(() => api.mySessions());
  // In-flight request per session id; sessions absent from the map are idle.
  // The ref is the lock (checked synchronously, so two taps in one render
  // cannot both start); the state mirrors it for rendering.
  const busyRef = useRef<Record<string, SessionAction>>({});
  const [busy, setBusy] = useState<Record<string, SessionAction>>({});
  const [reschedulingID, setReschedulingID] = useState<string | null>(null);
  const [slots, setSlots] = useState<Slot[] | null>(null);
  const [slotError, setSlotError] = useState<string | null>(null);
  const [linkError, setLinkError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<{ sessionID: string; message: string } | null>(null);

  const sessions = data ?? [];
  const now = Date.now();
  const upcoming = sessions.filter((s) => s.status === 'scheduled' && new Date(s.scheduled_time).getTime() > now).length;
  const completed = sessions.filter((s) => s.status === 'completed').length;
  const minutes = sessions.filter((s) => s.status === 'completed').reduce((sum, s) => sum + s.duration_minutes, 0);

  const openReschedule = async (session: CoachingSession) => {
    setReschedulingID(session.id);
    setSlots(null);
    setSlotError(null);
    try {
      // Excluding this session frees the times it currently occupies, so small
      // shifts inside its own window are offered.
      setSlots(await api.openSlots(session.coach_id, session.duration_minutes, session.id));
    } catch (err) {
      setSlotError(err instanceof Error ? err.message : 'could not load open slots');
    }
  };

  /** Marks `action` in flight for `sessionID`; returns false if that session is already busy. */
  const begin = (sessionID: string, action: SessionAction): boolean => {
    if (sessionID in busyRef.current) return false;
    busyRef.current = { ...busyRef.current, [sessionID]: action };
    setBusy(busyRef.current);
    return true;
  };
  const end = (sessionID: string) => {
    const { [sessionID]: _done, ...rest } = busyRef.current;
    busyRef.current = rest;
    setBusy(rest);
  };
  const isBusy = (sessionID: string, action: SessionAction): boolean => busy[sessionID] === action;
  const isBlocked = (sessionID: string, action: SessionAction): boolean =>
    sessionID in busy && busy[sessionID] !== action;

  const reschedule = async (session: CoachingSession, start: string) => {
    if (!begin(session.id, `reschedule:${start}`)) return;
    setSlotError(null);
    try {
      await api.rescheduleSession(session.id, start);
      setReschedulingID(null);
      setSlots(null);
      await reload();
    } catch (err) {
      setSlotError(err instanceof Error ? err.message : 'could not reschedule');
    } finally {
      end(session.id);
    }
  };

  const cancel = async (sessionID: string) => {
    if (!begin(sessionID, 'cancel')) return;
    setActionError(null);
    try {
      await api.cancelSession(sessionID);
      await reload();
    } catch (err) {
      setActionError({ sessionID, message: err instanceof Error ? err.message : 'could not cancel the session' });
    } finally {
      end(sessionID);
    }
  };

  const chat = async (session: CoachingSession) => {
    setActionError(null);
    try {
      const thread = await api.startThread(session.coach_id, session.id);
      navigation.navigate('Chats', {
        screen: 'Chat',
        params: { threadID: thread.id, title: session.counterpart_name ?? 'Coach' },
      });
    } catch (err) {
      setActionError({ sessionID: session.id, message: err instanceof Error ? err.message : 'could not open the chat' });
    }
  };

  return (
    <Screen scroll={false}>
      <FlatList
        data={sessions}
        keyExtractor={(session) => session.id}
        contentContainerStyle={{ gap: 12, paddingBottom: 24 }}
        ListHeaderComponent={
          <View style={{ gap: 16, marginBottom: 4 }}>
            <PageHeader
              eyebrow="Coaching sessions"
              title="Your sessions"
              subtitle="Upcoming and past time with your coaches, all in one place."
              gradient="sunrise"
            />
            {sessions.length > 0 ? (
              <StatRow>
                <StatTile value={upcoming} label="Upcoming" icon="calendar-outline" hue="rose" />
                <StatTile value={completed} label="Completed" icon="checkmark-done-outline" hue="sage" />
                <StatTile value={`${minutes}m`} label="Time coached" icon="hourglass-outline" hue="sun" />
              </StatRow>
            ) : null}
            {error ? <Text style={shared.error}>{error}</Text> : null}
            {linkError ? <Text style={shared.error}>{linkError}</Text> : null}
            {loading && !data ? (
              <>
                <SkeletonCard />
                <SkeletonCard />
              </>
            ) : null}
          </View>
        }
        ListEmptyComponent={
          loading || error ? null : (
            <EmptyState
              icon="calendar-outline"
              hue="rose"
              title="No sessions yet"
              text="Book time with a coach when you want a real person in your corner — before a date, after one, or whenever you feel stuck."
              action={{
                label: 'Find a coach',
                icon: 'people-outline',
                onPress: () => navigation.navigate('Coaches', { screen: 'CoachList' }),
              }}
            />
          )
        }
        renderItem={({ item }) => {
          const name = item.counterpart_name ?? 'Coach';
          const cal = calendarParts(item.scheduled_time);
          const tone = sessionTone(item.status);
          return (
            <View style={shared.card}>
              <View style={styles.top}>
                <View style={[styles.calendar, { borderColor: `${tone}55` }]}>
                  <Text style={[styles.calMonth, { color: tone }]}>{cal.month}</Text>
                  <Text style={styles.calDay}>{cal.day}</Text>
                </View>
                <View style={{ flex: 1, gap: 4 }}>
                  <View style={[shared.row, { gap: 8 }]}>
                    <Avatar name={name} size={24} />
                    <Text style={type.heading} numberOfLines={1}>
                      {name}
                    </Text>
                  </View>
                  <Text style={type.body}>{formatWhen(item.scheduled_time)}</Text>
                </View>
                <StatusPill text={statusLabel(item.status)} tone={tone} />
              </View>
              <View style={styles.metaRow}>
                <MetaRow icon="hourglass-outline" text={`${item.duration_minutes} min`} />
                {item.topic ? <MetaRow icon="chatbox-ellipses-outline" text={item.topic} /> : null}
              </View>
              {item.meeting_url ? (
                <Pressable
                  style={styles.notes}
                  accessibilityRole="link"
                  onPress={() => {
                    setLinkError(null);
                    void Linking.openURL(item.meeting_url ?? '').catch(() => setLinkError('Could not open the meeting link.'));
                  }}
                >
                  <Ionicons name="videocam-outline" size={16} color={colors.primaryDeep} />
                  <View style={{ flex: 1, gap: 2 }}>
                    <Text style={styles.notesLabel}>Join link</Text>
                    <Text style={[type.body, styles.link]} numberOfLines={1}>
                      {item.meeting_url}
                    </Text>
                  </View>
                </Pressable>
              ) : null}
              {item.coach_notes ? (
                <View style={styles.notes}>
                  <Ionicons name="reader-outline" size={16} color={colors.sageDeep} />
                  <View style={{ flex: 1, gap: 2 }}>
                    <Text style={styles.notesLabel}>Coach notes</Text>
                    <Text style={type.body}>{item.coach_notes}</Text>
                  </View>
                </View>
              ) : null}
              {item.status === 'scheduled' ? (
                <>
                  <Divider />
                  <View style={styles.actions}>
                    <View style={{ flex: 1 }}>
                      <Button
                        label="Message"
                        icon="chatbubble-outline"
                        variant="secondary"
                        onPress={() => void chat(item)}
                      />
                    </View>
                    <View style={{ flex: 1 }}>
                      <Button
                        label={reschedulingID === item.id ? 'Close' : 'Reschedule'}
                        icon={reschedulingID === item.id ? 'close-outline' : 'swap-horizontal-outline'}
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
                        loading={isBusy(item.id, 'cancel')}
                        disabled={isBlocked(item.id, 'cancel')}
                        onPress={() => void cancel(item.id)}
                      />
                    </View>
                  </View>
                  {actionError?.sessionID === item.id ? <Text style={shared.error}>{actionError.message}</Text> : null}
                </>
              ) : null}
              {reschedulingID === item.id ? (
                <View style={styles.reschedule}>
                  <Text style={type.heading}>Pick a new time</Text>
                  {slotError ? <Text style={shared.error}>{slotError}</Text> : null}
                  {!slots && !slotError ? <Loading /> : null}
                  {slots?.length === 0 ? (
                    <Empty text="No open slots in the next few weeks." icon="calendar-clear-outline" />
                  ) : null}
                  {slots?.slice(0, 8).map((slot) => (
                    <Button
                      key={slot.start}
                      label={formatWhen(slot.start)}
                      variant="secondary"
                      loading={isBusy(item.id, `reschedule:${slot.start}`)}
                      disabled={isBlocked(item.id, `reschedule:${slot.start}`)}
                      onPress={() => void reschedule(item, slot.start)}
                    />
                  ))}
                </View>
              ) : null}
            </View>
          );
        }}
      />
    </Screen>
  );
}

const styles = StyleSheet.create({
  top: { flexDirection: 'row', alignItems: 'center', gap: 14 },
  calendar: {
    width: 54,
    height: 58,
    borderRadius: radii.md,
    borderWidth: 1.5,
    backgroundColor: colors.surfaceAlt,
    alignItems: 'center',
    justifyContent: 'center',
  },
  calMonth: { fontFamily: fonts.sansBold, fontSize: 10, letterSpacing: 1 },
  calDay: { fontFamily: fonts.sansBlack, fontSize: 22, lineHeight: 26, color: colors.text },
  metaRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 14 },
  notes: {
    flexDirection: 'row',
    gap: 10,
    padding: 12,
    borderRadius: radii.md,
    backgroundColor: colors.sageTint,
  },
  notesLabel: { ...type.eyebrow, fontSize: 11 },
  link: { color: colors.primaryDeep, textDecorationLine: 'underline' },
  actions: { flexDirection: 'row', gap: 8 },
  reschedule: { gap: 8, padding: 14, borderRadius: radii.md, backgroundColor: colors.surfaceAlt },
});
