import { useNavigation, type NavigationProp } from '@react-navigation/native';
import React, { useState } from 'react';
import { ScrollView, Text, TextInput, View } from 'react-native';

import { api } from '../api/client';
import type { CoachingSession } from '../api/types';
import { Badge, Button, Empty, Loading, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { RootTabParams } from '../navigation/types';
import { colors, shared } from '../theme';
import { formatWhen, sessionTone } from './MySessionsScreen';

export default function CoachDashboardScreen(): React.ReactElement {
  const navigation = useNavigation<NavigationProp<RootTabParams>>();
  const sessions = useAsync(() => api.coachSessions('scheduled'));
  const threads = useAsync(() => api.coachThreads('active'));
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [busyID, setBusyID] = useState<string | null>(null);

  const act = async (session: CoachingSession, action: 'complete' | 'no_show' | 'notes') => {
    setBusyID(session.id);
    try {
      if (action === 'notes') {
        await api.setSessionNotes(session.id, notes[session.id] ?? '');
      } else {
        await api.setSessionStatus(session.id, action === 'complete' ? 'completed' : 'no_show');
      }
      await sessions.reload();
    } finally {
      setBusyID(null);
    }
  };

  return (
    <Screen>
      <Text style={shared.title}>Coach dashboard</Text>

      <Text style={shared.heading}>Active chats</Text>
      {threads.loading && !threads.data ? <Loading /> : null}
      {threads.data?.length === 0 ? <Empty text="No clients are chatting right now." /> : null}
      <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: 12 }}>
        {(threads.data ?? []).map((thread) => (
          <View key={thread.id} style={[shared.card, { width: 240 }]}>
            <Text style={shared.heading}>{thread.counterpart_name ?? 'Client'}</Text>
            <Badge
              text={thread.counterpart_online ? 'online' : 'offline'}
              tone={thread.counterpart_online ? colors.engaging : colors.muted}
            />
            <Text style={shared.muted}>last message {formatWhen(thread.last_message_at)}</Text>
            <Button
              label="Open chat"
              variant="secondary"
              onPress={() =>
                navigation.navigate('Chats', {
                  screen: 'Chat',
                  params: { threadID: thread.id, title: thread.counterpart_name ?? 'Client' },
                })
              }
            />
          </View>
        ))}
      </ScrollView>

      <Text style={shared.heading}>Upcoming sessions</Text>
      {sessions.loading && !sessions.data ? <Loading /> : null}
      {sessions.error ? <Text style={shared.error}>{sessions.error}</Text> : null}
      {sessions.data?.length === 0 ? <Empty text="Nothing booked yet." /> : null}
      {(sessions.data ?? []).map((session) => (
        <View key={session.id} style={shared.card}>
          <View style={[shared.row, { justifyContent: 'space-between' }]}>
            <Text style={shared.heading}>{session.counterpart_name ?? 'Client'}</Text>
            <Badge text={session.status} tone={sessionTone(session.status)} />
          </View>
          <Text style={shared.body}>{formatWhen(session.scheduled_time)}</Text>
          <Text style={shared.muted}>
            {session.duration_minutes} min{session.topic ? ` · ${session.topic}` : ''}
          </Text>
          <TextInput
            value={notes[session.id] ?? session.coach_notes ?? ''}
            onChangeText={(text) => setNotes((current) => ({ ...current, [session.id]: text }))}
            placeholder="Session notes"
            placeholderTextColor={colors.muted}
            multiline
            style={{
              borderWidth: 1,
              borderColor: colors.border,
              borderRadius: 10,
              padding: 10,
              minHeight: 70,
              color: colors.text,
            }}
          />
          <View style={shared.row}>
            <View style={{ flex: 1 }}>
              <Button
                label="Save notes"
                variant="secondary"
                loading={busyID === session.id}
                onPress={() => void act(session, 'notes')}
              />
            </View>
            <View style={{ flex: 1 }}>
              <Button label="Completed" onPress={() => void act(session, 'complete')} />
            </View>
            <View style={{ flex: 1 }}>
              <Button label="No show" variant="secondary" onPress={() => void act(session, 'no_show')} />
            </View>
          </View>
        </View>
      ))}
    </Screen>
  );
}
