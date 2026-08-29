import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React from 'react';
import { FlatList, Pressable, Text } from 'react-native';

import { api } from '../api/client';
import { Empty, Loading, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { AnalysisStackParams } from '../navigation/types';
import { shared } from '../theme';
import { formatWhen } from './MySessionsScreen';

export default function AnalysisHistoryScreen({
  navigation,
}: NativeStackScreenProps<AnalysisStackParams, 'History'>): React.ReactElement {
  const { data, error, loading } = useAsync(() => api.conversations());

  const open = async (conversationID: string) => {
    const result = await api.latestResult(conversationID);
    navigation.navigate('Result', { analysisID: result.id, conversationID });
  };

  return (
    <Screen scroll={false}>
      <Text style={shared.title}>Past analyses</Text>
      {loading && !data ? <Loading /> : null}
      {error ? <Text style={shared.error}>{error}</Text> : null}
      <FlatList
        data={data ?? []}
        keyExtractor={(conversation) => conversation.id}
        contentContainerStyle={{ gap: 12, paddingVertical: 12 }}
        ListEmptyComponent={loading ? null : <Empty text="You have not submitted a conversation yet." />}
        renderItem={({ item }) => (
          <Pressable
            accessibilityRole="button"
            onPress={() => void open(item.id)}
            style={({ pressed }) => [shared.card, pressed && { opacity: 0.9 }]}
          >
            <Text style={shared.heading}>{item.title || 'Untitled conversation'}</Text>
            <Text style={shared.muted}>
              {item.platform}
              {item.match_name ? ` · ${item.match_name}` : ''} · {formatWhen(item.created_at)}
            </Text>
          </Pressable>
        )}
      />
    </Screen>
  );
}
