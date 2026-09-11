import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React from 'react';
import { FlatList, Text, View } from 'react-native';

import { api } from '../api/client';
import { EmptyState, IconDisc, ListCard, MetaRow, PageHeader, SkeletonCard, StatRow, StatTile } from '../components/kit';
import { Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import type { AnalysisStackParams } from '../navigation/types';
import { shared } from '../theme';
import { formatWhen } from './MySessionsScreen';

export default function AnalysisHistoryScreen({
  navigation,
}: NativeStackScreenProps<AnalysisStackParams, 'History'>): React.ReactElement {
  const { data, error, loading } = useAsync(() => api.conversations());
  const conversations = data ?? [];
  const platforms = new Set(conversations.map((c) => c.platform)).size;
  const named = conversations.filter((c) => c.match_name).length;

  const open = async (conversationID: string) => {
    const result = await api.latestResult(conversationID);
    navigation.navigate('Result', { analysisID: result.id, conversationID });
  };

  return (
    <Screen scroll={false}>
      <FlatList
        data={conversations}
        keyExtractor={(conversation) => conversation.id}
        contentContainerStyle={{ gap: 12, paddingBottom: 24 }}
        ListHeaderComponent={
          <View style={{ gap: 16, marginBottom: 4 }}>
            <PageHeader
              eyebrow="Your history"
              title="Past analyses"
              subtitle="Every conversation you have had looked at. Reopen one to see the feedback again."
              gradient="sky"
            />
            {conversations.length > 0 ? (
              <StatRow>
                <StatTile value={conversations.length} label="Conversations" icon="chatbubbles-outline" hue="sky" />
                <StatTile value={platforms} label="Platforms" icon="apps-outline" hue="sun" />
                <StatTile value={named} label="With a name" icon="person-outline" hue="rose" />
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
              icon="document-text-outline"
              hue="sky"
              title="Nothing analysed yet"
              text="Paste a conversation on the Analyse tab and it will show up here with its feedback."
              action={{ label: 'Analyse a conversation', icon: 'sparkles-outline', onPress: () => navigation.navigate('Submit') }}
            />
          )
        }
        renderItem={({ item }) => (
          <ListCard
            onPress={() => void open(item.id)}
            leading={<IconDisc icon="chatbox-ellipses-outline" hue="sky" size={48} />}
            title={item.title || 'Untitled conversation'}
            subtitle={item.match_name ? `With ${item.match_name}` : 'No name given'}
          >
            <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 14 }}>
              <MetaRow icon="phone-portrait-outline" text={item.platform} />
              <MetaRow icon="time-outline" text={formatWhen(item.created_at)} />
            </View>
          </ListCard>
        )}
      />
    </Screen>
  );
}
