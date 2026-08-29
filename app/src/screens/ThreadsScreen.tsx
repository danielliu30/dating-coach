import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React from 'react';
import { FlatList, Pressable, Text, View } from 'react-native';

import { api } from '../api/client';
import { Badge, Empty, Loading, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { ChatsStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors, shared } from '../theme';
import { formatWhen } from './MySessionsScreen';

export default function ThreadsScreen({
  navigation,
}: NativeStackScreenProps<ChatsStackParams, 'Threads'>): React.ReactElement {
  const { user } = useAuth();
  const isCoach = user?.role === 'coach';
  const { data, error, loading } = useAsync(() => (isCoach ? api.coachThreads('active') : api.threads()), [isCoach]);

  return (
    <Screen scroll={false}>
      <Text style={shared.title}>Live chats</Text>
      <Text style={shared.muted}>
        {isCoach ? 'Clients currently talking to you.' : 'Open a coach from the Coaches tab to start a new chat.'}
      </Text>
      {loading && !data ? <Loading /> : null}
      {error ? <Text style={shared.error}>{error}</Text> : null}
      <FlatList
        data={data ?? []}
        keyExtractor={(thread) => thread.id}
        contentContainerStyle={{ gap: 12, paddingVertical: 12 }}
        ListEmptyComponent={loading ? null : <Empty text="No chats yet." />}
        renderItem={({ item }) => (
          <Pressable
            accessibilityRole="button"
            onPress={() =>
              navigation.navigate('Chat', {
                threadID: item.id,
                title: item.counterpart_name ?? 'Chat',
              })
            }
            style={({ pressed }) => [shared.card, pressed && { opacity: 0.9 }]}
          >
            <View style={[shared.row, { justifyContent: 'space-between' }]}>
              <Text style={shared.heading}>{item.counterpart_name ?? 'Chat'}</Text>
              <Badge
                text={item.counterpart_online ? 'online' : 'offline'}
                tone={item.counterpart_online ? colors.engaging : colors.muted}
              />
            </View>
            <Text style={shared.muted}>
              {item.status === 'closed' ? 'Closed' : 'Active'} · last message {formatWhen(item.last_message_at)}
            </Text>
          </Pressable>
        )}
      />
    </Screen>
  );
}
