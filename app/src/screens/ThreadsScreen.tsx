import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React from 'react';
import { FlatList, Text, View } from 'react-native';

import { api } from '../api/client';
import {
  Avatar,
  EmptyState,
  ListCard,
  MetaRow,
  PageHeader,
  SkeletonCard,
  StatusPill,
} from '../components/kit';
import { Screen } from '../components/ui';
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
  const threads = data ?? [];

  return (
    <Screen scroll={false}>
      <FlatList
        data={threads}
        keyExtractor={(thread) => thread.id}
        contentContainerStyle={{ gap: 12, paddingBottom: 24 }}
        ListHeaderComponent={
          <View style={{ gap: 16, marginBottom: 4 }}>
            <PageHeader
              eyebrow="Live chats"
              title={isCoach ? 'Your conversations' : 'Talk it through'}
              subtitle={
                isCoach
                  ? 'Clients currently talking to you. Reply in real time.'
                  : 'Live chats with your coaches. Open a coach from the Coaches tab to start a new one.'
              }
              gradient="sky"
            />
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
          loading || error ? null : (
            <EmptyState
              icon="chatbubble-ellipses-outline"
              hue="sky"
              title="No chats yet"
              text={
                isCoach
                  ? 'When a client opens a live chat with you it will appear here.'
                  : 'Pick a coach and start a live chat whenever you want a second perspective.'
              }
            />
          )
        }
        renderItem={({ item }) => {
          const name = item.counterpart_name ?? 'Chat';
          return (
            <ListCard
              onPress={() => navigation.navigate('Chat', { threadID: item.id, title: name })}
              leading={<Avatar name={name} size={52} online={item.counterpart_online} />}
              title={name}
              subtitle={item.counterpart_online ? 'Online now' : 'Away — messages are saved'}
              trailing={
                <StatusPill
                  text={item.status === 'closed' ? 'Closed' : 'Active'}
                  tone={item.status === 'closed' ? colors.muted : colors.engaging}
                />
              }
            >
              <MetaRow icon="time-outline" text={`Last message ${formatWhen(item.last_message_at)}`} />
            </ListCard>
          );
        }}
      />
    </Screen>
  );
}
