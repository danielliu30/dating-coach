import { useNavigation, type NavigationProp } from '@react-navigation/native';
import React, { useState } from 'react';
import { ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';

import { api } from '../api/client';
import type { CoachingSession } from '../api/types';
import {
  Avatar,
  Divider,
  EmptyState,
  MetaRow,
  PageHeader,
  SectionHeader,
  SkeletonCard,
  StatRow,
  StatTile,
  StatusPill,
} from '../components/kit';
import { Button, Field, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { RootTabParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors, fonts, radii, shared, type } from '../theme';
import { formatWhen, sessionTone } from './MySessionsScreen';

/** Greeting keyed to the local hour, e.g. "Good morning". */
/** The per-session requests a coach can fire from a dashboard card. */
type SessionAction = 'complete' | 'no_show' | 'notes' | 'meeting_url';

const greeting = (): string => {
  const hour = new Date().getHours();
  return hour < 12 ? 'Good morning' : hour < 18 ? 'Good afternoon' : 'Good evening';
};

export default function CoachDashboardScreen(): React.ReactElement {
  const navigation = useNavigation<NavigationProp<RootTabParams>>();
  const { user } = useAuth();
  const sessions = useAsync(() => api.coachSessions('scheduled'));
  const threads = useAsync(() => api.coachThreads('active'));
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [meetingUrls, setMeetingUrls] = useState<Record<string, string>>({});
  const [actError, setActError] = useState<string | null>(null);
  const [busy, setBusy] = useState<{ sessionID: string; action: SessionAction } | null>(null);

  /** Whether `action` on `session` is the request currently in flight. */
  const isBusy = (session: CoachingSession, action: SessionAction): boolean =>
    busy?.sessionID === session.id && busy.action === action;
  /** Whether some other request on `session` is in flight, so this action must wait. */
  const isBlocked = (session: CoachingSession, action: SessionAction): boolean =>
    busy?.sessionID === session.id && busy.action !== action;

  const act = async (session: CoachingSession, action: SessionAction) => {
    if (busy?.sessionID === session.id) return;
    setBusy({ sessionID: session.id, action });
    setActError(null);
    try {
      if (action === 'notes') {
        await api.setSessionNotes(session.id, notes[session.id] ?? session.coach_notes ?? '');
      } else if (action === 'meeting_url') {
        await api.setSessionMeetingUrl(session.id, (meetingUrls[session.id] ?? session.meeting_url ?? '').trim());
      } else {
        await api.setSessionStatus(session.id, action === 'complete' ? 'completed' : 'no_show');
      }
      await sessions.reload();
    } catch (err) {
      setActError(err instanceof Error ? err.message : 'could not update session');
    } finally {
      setBusy(null);
    }
  };

  const threadList = threads.data ?? [];
  const sessionList = sessions.data ?? [];
  const online = threadList.filter((t) => t.counterpart_online).length;
  const minutes = sessionList.reduce((sum, s) => sum + s.duration_minutes, 0);
  const firstName = user?.display_name?.split(/\s+/)[0];

  return (
    <Screen>
      <PageHeader
        eyebrow="Coach dashboard"
        title={firstName ? `${greeting()}, ${firstName}` : greeting()}
        subtitle="Here is who is waiting on you today and what is coming up."
        gradient="meadow"
        aside={user ? <Avatar name={user.display_name} size={56} /> : undefined}
      />
      <StatRow>
        <StatTile value={threadList.length} label="Active chats" icon="chatbubbles-outline" hue="sky" />
        <StatTile value={online} label="Online now" icon="radio-button-on-outline" hue="sage" />
        <StatTile value={sessionList.length} label="Upcoming" icon="calendar-outline" hue="rose" />
        <StatTile value={`${minutes}m`} label="Booked" icon="hourglass-outline" hue="sun" />
      </StatRow>

      <SectionHeader
        title="Active chats"
        caption="Clients talking to you right now."
        action={{ label: 'All chats', onPress: () => navigation.navigate('Chats', { screen: 'Threads' }) }}
      />
      {threads.loading && !threads.data ? <SkeletonCard /> : null}
      {threads.error ? <Text style={shared.error}>{threads.error}</Text> : null}
      {threadList.length === 0 && !threads.loading && !threads.error ? (
        <EmptyState
          icon="chatbubble-ellipses-outline"
          hue="sky"
          title="Quiet for now"
          text="No clients are chatting right now. New conversations will appear here the moment they start."
        />
      ) : null}
      <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: 12 }}>
        {threadList.map((thread) => {
          const name = thread.counterpart_name ?? 'Client';
          return (
            <View key={thread.id} style={[shared.card, styles.threadCard]}>
              <View style={[shared.row, { gap: 12 }]}>
                <Avatar name={name} size={44} online={thread.counterpart_online} />
                <View style={{ flex: 1, gap: 2 }}>
                  <Text style={type.subheading} numberOfLines={1}>
                    {name}
                  </Text>
                  <StatusPill
                    text={thread.counterpart_online ? 'Online' : 'Away'}
                    tone={thread.counterpart_online ? colors.engaging : colors.muted}
                  />
                </View>
              </View>
              <MetaRow icon="time-outline" text={`Last message ${formatWhen(thread.last_message_at)}`} />
              <Button
                label="Open chat"
                icon="arrow-forward"
                variant="secondary"
                onPress={() =>
                  navigation.navigate('Chats', {
                    screen: 'Chat',
                    params: { threadID: thread.id, title: name },
                  })
                }
              />
            </View>
          );
        })}
      </ScrollView>

      <SectionHeader title="Upcoming sessions" caption="Add notes before or after, then mark how it went." />
      {sessions.loading && !sessions.data ? <SkeletonCard /> : null}
      {sessions.error ? <Text style={shared.error}>{sessions.error}</Text> : null}
      {actError ? <Text style={shared.error}>{actError}</Text> : null}
      {sessionList.length === 0 && !sessions.loading && !sessions.error ? (
        <EmptyState
          icon="calendar-clear-outline"
          hue="rose"
          title="Nothing booked yet"
          text="When clients book time with you, sessions land here with their topic and a place for your notes."
          action={{
            label: 'Update your availability',
            icon: 'time-outline',
            onPress: () => navigation.navigate('Profile'),
          }}
        />
      ) : null}
      {sessionList.map((session) => {
        const name = session.counterpart_name ?? 'Client';
        return (
          <View key={session.id} style={shared.card}>
            <View style={[shared.row, { justifyContent: 'space-between', gap: 12 }]}>
              <View style={[shared.row, { gap: 12, flex: 1 }]}>
                <Avatar name={name} size={44} />
                <View style={{ flex: 1, gap: 2 }}>
                  <Text style={type.heading} numberOfLines={1}>
                    {name}
                  </Text>
                  <Text style={type.body}>{formatWhen(session.scheduled_time)}</Text>
                </View>
              </View>
              <StatusPill text={session.status} tone={sessionTone(session.status)} />
            </View>
            <View style={styles.metaRow}>
              <MetaRow icon="hourglass-outline" text={`${session.duration_minutes} min`} />
              {session.topic ? <MetaRow icon="chatbox-ellipses-outline" text={session.topic} /> : null}
            </View>
            <Divider />
            <Field
              label="Meeting link"
              value={meetingUrls[session.id] ?? session.meeting_url ?? ''}
              onChangeText={(text) => setMeetingUrls((current) => ({ ...current, [session.id]: text }))}
              placeholder="https://meet.example.com/your-room"
              autoCapitalize="none"
              autoCorrect={false}
              keyboardType="url"
            />
            <Button
              label="Save meeting link"
              icon="videocam-outline"
              variant="secondary"
              loading={isBusy(session, 'meeting_url')}
              disabled={isBlocked(session, 'meeting_url')}
              onPress={() => void act(session, 'meeting_url')}
            />
            <Text style={styles.label}>Session notes</Text>
            <TextInput
              value={notes[session.id] ?? session.coach_notes ?? ''}
              onChangeText={(text) => setNotes((current) => ({ ...current, [session.id]: text }))}
              placeholder="What came up, what to follow up on next time…"
              placeholderTextColor={colors.muted}
              multiline
              style={styles.notes}
            />
            <View style={styles.actions}>
              <View style={{ flex: 1 }}>
                <Button
                  label="Save notes"
                  icon="save-outline"
                  variant="secondary"
                  loading={isBusy(session, 'notes')}
                  disabled={isBlocked(session, 'notes')}
                  onPress={() => void act(session, 'notes')}
                />
              </View>
              <View style={{ flex: 1 }}>
                <Button
                  label="Completed"
                  icon="checkmark-outline"
                  loading={isBusy(session, 'complete')}
                  disabled={isBlocked(session, 'complete')}
                  onPress={() => void act(session, 'complete')}
                />
              </View>
              <View style={{ flex: 1 }}>
                <Button
                  label="No show"
                  variant="secondary"
                  loading={isBusy(session, 'no_show')}
                  disabled={isBlocked(session, 'no_show')}
                  onPress={() => void act(session, 'no_show')}
                />
              </View>
            </View>
          </View>
        );
      })}
    </Screen>
  );
}

const styles = StyleSheet.create({
  threadCard: { width: 260 },
  metaRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 14 },
  label: {
    fontFamily: fonts.sansBold,
    fontSize: 11.5,
    letterSpacing: 0.8,
    textTransform: 'uppercase',
    color: colors.bark,
  },
  notes: {
    fontFamily: fonts.sans,
    fontSize: 15,
    lineHeight: 21,
    borderWidth: 1.5,
    borderColor: colors.border,
    backgroundColor: colors.surfaceAlt,
    borderRadius: radii.md,
    padding: 12,
    minHeight: 84,
    color: colors.text,
    textAlignVertical: 'top',
  },
  actions: { flexDirection: 'row', gap: 8 },
});
