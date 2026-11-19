import { useFocusEffect, useNavigation, type NavigationProp } from '@react-navigation/native';
import React, { useCallback, useState } from 'react';
import { Text, View } from 'react-native';

import { api, ApiError } from '../api/client';
import type { ProfileScore } from '../api/types';
import type { RootTabParams } from '../navigation/types';
import { shared, type } from '../theme';
import { ListCard, SectionHeader } from './kit';
import { Button, Loading } from './ui';

const BANDS = {
  collecting_evidence: {
    label: 'Collecting evidence',
    reason: 'Only conversations with clear evidence count. Older analyses without evidence do not count.',
  },
  strong: {
    label: 'Strong',
    reason: 'Most of these conversations showed clear plans and responsive engagement, with little coaching needed.',
  },
  developing: {
    label: 'Developing',
    reason: 'These conversations show a mix of strengths and opportunities to adjust your approach.',
  },
  needs_more_coaching: {
    label: 'Needs more coaching',
    reason: 'A scheduling constraint was repeatedly missed in more than one conversation.',
  },
} satisfies Record<ProfileScore['band'], { label: string; reason: string }>;

const SCORE_TIMEOUT_MS = 10_000;

type ScoreState =
  | { status: 'loading' | 'hidden' | 'error' }
  | { status: 'ready'; score: ProfileScore };

export function ProfileScoreCard(): React.ReactElement | null {
  const navigation = useNavigation<NavigationProp<RootTabParams>>();
  const [state, setState] = useState<ScoreState>({ status: 'loading' });
  const [attempt, setAttempt] = useState(0);

  useFocusEffect(useCallback(() => {
    let active = true;
    const controller = new AbortController();
    setState({ status: 'loading' });
    const timeout = setTimeout(() => {
      active = false;
      controller.abort();
      setState({ status: 'error' });
    }, SCORE_TIMEOUT_MS);
    void api.profileScore(controller.signal).then(
      (score) => {
        if (active) setState({ status: 'ready', score });
      },
      (error: unknown) => {
        if (active) setState({ status: error instanceof ApiError && error.status === 404 ? 'hidden' : 'error' });
      },
    ).finally(() => clearTimeout(timeout));
    return () => {
      active = false;
      clearTimeout(timeout);
      controller.abort();
    };
  }, [attempt]));

  if (state.status === 'hidden') return null;
  const score = state.status === 'ready' ? state.score : null;
  const band = score && BANDS[score.band];
  return (
    <View style={shared.card} testID="profile-score-card">
      <SectionHeader
        title="Dating profile score"
        caption="Private to you. Based on recent conversation analyses, not on whether you got a reply or a date."
        icon="sparkles-outline"
        hue="sage"
      />
      {state.status === 'loading' ? <Loading label="Loading your profile score…" /> : null}
      {state.status === 'error' ? (
        <>
          <Text style={type.caption}>Your profile score is unavailable right now.</Text>
          <Button label="Retry profile score" variant="secondary" onPress={() => setAttempt((current) => current + 1)} />
        </>
      ) : null}
      {score && band ? (
        <>
          <Text style={type.heading} accessibilityRole="header">{band.label}</Text>
          <Text style={type.caption}>
            {score.band === 'collecting_evidence'
              ? `${score.conversations_counted} of ${score.minimum_conversations} qualifying conversations`
              : `Based on ${score.conversations_counted} qualifying conversations`}
          </Text>
          <Text style={type.caption}>{band.reason}</Text>
          <Text style={type.caption}>Counts up to five distinct conversations submitted in the last 90 days.</Text>
          {score.band === 'needs_more_coaching' ? (
            <Text style={type.caption}>What would help you notice and respond when someone says a plan won’t work?</Text>
          ) : null}
          {score.band === 'collecting_evidence' ? (
            <Button label="Analyse a conversation" variant="secondary" onPress={() => navigation.navigate('Analyse', { screen: 'Submit' })} />
          ) : null}
          {score.evidence.map((source, index) => (
            <ListCard
              key={source.analysis_id}
              title={`Review analysis ${index + 1}`}
              subtitle={new Date(source.submitted_at).toLocaleDateString()}
              accessibilityLabel={`Review analysis ${index + 1}`}
              onPress={() => navigation.navigate('Analyse', {
                screen: 'Result',
                params: { analysisID: source.analysis_id, conversationID: source.conversation_id },
              })}
            />
          ))}
        </>
      ) : null}
    </View>
  );
}
