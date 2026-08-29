import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { api } from '../api/client';
import type { AnalysisResult, Outcome } from '../api/types';
import { Badge, Button, Loading, Screen, ScoreBar } from '../components/ui';
import type { AnalysisStackParams } from '../navigation/types';
import { colors, scoreColor, shared } from '../theme';

const OUTCOMES: { value: Outcome; label: string }[] = [
  { value: 'ghosted', label: 'Ghosted' },
  { value: 'kept_talking', label: 'Kept talking' },
  { value: 'number_exchanged', label: 'Numbers' },
  { value: 'date_set', label: 'Date set' },
];

const POLL_MS = 2000;
const MAX_POLL_FAILURES = 5;

export default function AnalysisResultScreen({
  route,
}: NativeStackScreenProps<AnalysisStackParams, 'Result'>): React.ReactElement {
  const { analysisID, conversationID } = route.params;
  const [result, setResult] = useState<AnalysisResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [labelStatus, setLabelStatus] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const failures = useRef(0);

  // The worker fills the result asynchronously, so poll until it settles. A
  // transient request failure retries with backoff instead of giving up.
  const poll = useCallback(async () => {
    try {
      const next = await api.analysisResult(analysisID);
      failures.current = 0;
      setError(null);
      setResult(next);
      if (next.status === 'pending' || next.status === 'running') {
        timer.current = setTimeout(() => void poll(), POLL_MS);
      }
    } catch (err) {
      failures.current += 1;
      if (failures.current >= MAX_POLL_FAILURES) {
        setError(err instanceof Error ? err.message : 'could not load analysis');
        return;
      }
      timer.current = setTimeout(() => void poll(), POLL_MS * failures.current);
    }
  }, [analysisID]);

  useEffect(() => {
    void poll();
    return () => {
      if (timer.current) clearTimeout(timer.current);
    };
  }, [poll]);

  const saveLabel = async (value: Outcome) => {
    setOutcome(value);
    setLabelStatus(null);
    try {
      await api.labelConversation(conversationID, {
        outcome: value,
        reply_received: value !== 'ghosted',
        consented: true,
      });
      setLabelStatus('Thanks — this helps train the model.');
    } catch (err) {
      setLabelStatus(err instanceof Error ? err.message : 'could not save');
    }
  };

  if (error) {
    return (
      <Screen>
        <Text style={shared.error}>{error}</Text>
        <Button
          label="Try again"
          onPress={() => {
            failures.current = 0;
            setError(null);
            void poll();
          }}
        />
      </Screen>
    );
  }

  if (!result || result.status === 'pending' || result.status === 'running') {
    return (
      <Screen>
        <Loading label={result?.status === 'running' ? 'Scoring your conversation…' : 'Queued for analysis…'} />
      </Screen>
    );
  }

  if (result.status === 'failed') {
    return (
      <Screen>
        <View style={shared.card}>
          <Text style={shared.heading}>Analysis failed</Text>
          <Text style={shared.body}>{result.error || 'The analyzer could not score this conversation.'}</Text>
        </View>
      </Screen>
    );
  }

  const overall = result.overall;

  return (
    <Screen>
      <View style={shared.card}>
        <View style={[shared.row, { justifyContent: 'space-between' }]}>
          <Text style={shared.heading}>Overall engagement</Text>
          <Badge
            text={`${Math.round((overall?.engagement_score ?? 0) * 100)}%`}
            tone={scoreColor(overall?.engagement_score ?? 0)}
          />
        </View>
        <ScoreBar score={overall?.engagement_score ?? 0} />
        {overall?.summary ? <Text style={shared.body}>{overall.summary}</Text> : null}
        {overall?.strengths?.length ? (
          <View style={{ gap: 4 }}>
            <Text style={shared.muted}>What worked</Text>
            {overall.strengths.map((item) => (
              <Text key={item} style={shared.body}>
                • {item}
              </Text>
            ))}
          </View>
        ) : null}
        {overall?.improvements?.length ? (
          <View style={{ gap: 4 }}>
            <Text style={shared.muted}>What to change</Text>
            {overall.improvements.map((item) => (
              <Text key={item} style={shared.body}>
                • {item}
              </Text>
            ))}
          </View>
        ) : null}
        <Text style={shared.muted}>model: {result.model_version}</Text>
      </View>

      <Text style={shared.heading}>Where it was engaging</Text>
      {(result.segments ?? []).map((segment) => (
        <View key={`${segment.start_position}-${segment.end_position}`} style={shared.card}>
          <View style={[shared.row, { justifyContent: 'space-between' }]}>
            <Text style={shared.heading}>
              Messages {segment.start_position + 1}–{segment.end_position + 1}
            </Text>
            <Badge text={segment.label} tone={scoreColor(segment.engagement_score)} />
          </View>
          <ScoreBar score={segment.engagement_score} />
          {segment.comment ? <Text style={shared.body}>{segment.comment}</Text> : null}
        </View>
      ))}

      <View style={shared.card}>
        <Text style={shared.heading}>What happened next?</Text>
        <Text style={shared.muted}>
          Optional, and it stays with your account — outcomes are what let us train a better model.
        </Text>
        <View style={[shared.row, { flexWrap: 'wrap' }]}>
          {OUTCOMES.map((option) => (
            <Pressable
              key={option.value}
              accessibilityRole="radio"
              accessibilityState={{ selected: outcome === option.value }}
              onPress={() => void saveLabel(option.value)}
              style={[styles.chip, outcome === option.value && styles.chipActive]}
            >
              <Text style={outcome === option.value ? styles.chipActiveLabel : styles.chipLabel}>
                {option.label}
              </Text>
            </Pressable>
          ))}
        </View>
        {labelStatus ? <Text style={shared.muted}>{labelStatus}</Text> : null}
      </View>

      <Button label="Refresh" variant="secondary" onPress={() => void poll()} />
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
