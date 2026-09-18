import { Ionicons } from '@expo/vector-icons';
import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { api } from '../api/client';
import type { AnalysisResult, Outcome } from '../api/types';
import { Divider, EmptyState, IconDisc, SectionHeader, Skeleton, SkeletonCard, StatusPill } from '../components/kit';
import { Badge, Button, Loading, Screen, ScoreBar, type IconName } from '../components/ui';
import type { AnalysisStackParams } from '../navigation/types';
import { colors, fonts, radii, scoreColor, shared, type } from '../theme';

const OUTCOMES: { value: Outcome; label: string; icon: IconName }[] = [
  { value: 'ghosted', label: 'Ghosted', icon: 'cloud-outline' },
  { value: 'kept_talking', label: 'Kept talking', icon: 'chatbubbles-outline' },
  { value: 'number_exchanged', label: 'Numbers', icon: 'call-outline' },
  { value: 'date_set', label: 'Date set', icon: 'calendar-outline' },
];

/** One-word read of an engagement score in [0, 1] for the hero card. */
const scoreWord = (score: number): string =>
  score >= 0.66 ? 'Engaging' : score >= 0.4 ? 'Steady' : 'Flat';

const POLL_MS = 2000;
const MAX_POLL_FAILURES = 5;

export default function AnalysisResultScreen({
  route,
}: NativeStackScreenProps<AnalysisStackParams, 'Result'>): React.ReactElement {
  const { conversationID } = route.params;
  // A retry scores the conversation under a new analysis id, so the id being
  // polled outlives the one this screen was opened with.
  const [analysisID, setAnalysisID] = useState(route.params.analysisID);
  const [result, setResult] = useState<AnalysisResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [retrying, setRetrying] = useState(false);
  // Kept apart from `error`, whose retry re-polls: a queue request that failed
  // has to be reissued, not polled for.
  const [retryError, setRetryError] = useState<string | null>(null);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const [labelStatus, setLabelStatus] = useState<string | null>(null);
  const [labelFailed, setLabelFailed] = useState(false);
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

  // A failed analysis keeps the transcript, so the analyzer can score it again
  // once whatever broke it is back: queue a new run and follow that one.
  const retryAnalysis = async () => {
    setRetrying(true);
    setRetryError(null);
    try {
      const next = await api.reanalyzeConversation(conversationID);
      failures.current = 0;
      setResult(next);
      setAnalysisID(next.id);
      // An unfinished run is returned as-is, leaving the polled id unchanged,
      // so nothing would restart the poll loop that the failure ended.
      if (next.id === analysisID) void poll();
    } catch (err) {
      setRetryError(err instanceof Error ? err.message : 'could not queue the analysis again');
    } finally {
      setRetrying(false);
    }
  };

  const saveLabel = async (value: Outcome) => {
    setOutcome(value);
    setLabelStatus(null);
    setLabelFailed(false);
    try {
      await api.labelConversation(conversationID, {
        outcome: value,
        reply_received: value !== 'ghosted',
        consented: true,
      });
      setLabelStatus('Thanks — this helps train the model.');
    } catch (err) {
      setLabelFailed(true);
      setLabelStatus(err instanceof Error ? err.message : 'could not save');
    }
  };

  if (error) {
    return (
      <Screen>
        <EmptyState
          icon="cloud-offline-outline"
          hue="rose"
          title="We lost the thread"
          text={error}
          action={{
            label: 'Try again',
            icon: 'refresh-outline',
            onPress: () => {
              failures.current = 0;
              setError(null);
              void poll();
            },
          }}
        />
      </Screen>
    );
  }

  if (!result || result.status === 'pending' || result.status === 'running') {
    return (
      <Screen>
        <View style={styles.pending}>
          <IconDisc icon={result?.status === 'running' ? 'analytics-outline' : 'hourglass-outline'} hue="sun" size={64} />
          <Loading label={result?.status === 'running' ? 'Scoring your conversation…' : 'Queued for analysis…'} />
          <Text style={[type.caption, { textAlign: 'center' }]}>
            This usually takes a few seconds. We look at rhythm, reciprocity and where the energy shifts.
          </Text>
        </View>
        <SkeletonCard />
        <Skeleton height={80} radius={radii.lg} />
      </Screen>
    );
  }

  if (result.status === 'failed') {
    return (
      <Screen>
        <View style={shared.card}>
          <SectionHeader
            title="Analysis failed"
            caption="Your transcript is safe — we can score it again."
            icon="alert-circle-outline"
            hue="rose"
          />
          <Text style={type.body}>{result.error || 'The analyzer could not score this conversation.'}</Text>
          {retryError ? <Text style={shared.error}>{retryError}</Text> : null}
          <Button label="Try again" icon="refresh-outline" loading={retrying} onPress={() => void retryAnalysis()} />
        </View>
      </Screen>
    );
  }

  const overall = result.overall;
  const score = overall?.engagement_score ?? 0;
  const tone = scoreColor(score);
  const segments = result.segments ?? [];

  return (
    <Screen>
      <View style={[shared.card, styles.hero]}>
        <View style={styles.heroRow}>
          <View style={[styles.ring, { borderColor: tone }]}>
            <Text style={[styles.ringValue, { color: tone }]}>{Math.round(score * 100)}</Text>
            <Text style={styles.ringUnit}>%</Text>
          </View>
          <View style={{ flex: 1, gap: 6 }}>
            <Text style={type.eyebrow}>Overall engagement</Text>
            <Text style={type.title}>{scoreWord(score)}</Text>
            <StatusPill text={`${segments.length} stretch${segments.length === 1 ? '' : 'es'} scored`} tone={colors.sageDeep} />
          </View>
        </View>
        <ScoreBar score={score} />
        {overall?.summary ? <Text style={type.body}>{overall.summary}</Text> : null}
        {overall?.strengths?.length ? (
          <View style={[styles.list, { backgroundColor: colors.sageTint }]}>
            <View style={shared.row}>
              <Ionicons name="leaf-outline" size={16} color={colors.sageDeep} />
              <Text style={[type.eyebrow, { color: colors.sageDeep }]}>What worked</Text>
            </View>
            {overall.strengths.map((item) => (
              <View key={item} style={styles.bullet}>
                <Ionicons name="checkmark-circle" size={16} color={colors.engaging} />
                <Text style={[type.body, { flex: 1 }]}>{item}</Text>
              </View>
            ))}
          </View>
        ) : null}
        {overall?.improvements?.length ? (
          <View style={[styles.list, { backgroundColor: colors.sunTint }]}>
            <View style={shared.row}>
              <Ionicons name="sunny-outline" size={16} color={colors.neutral} />
              <Text style={[type.eyebrow, { color: colors.neutral }]}>What to change</Text>
            </View>
            {overall.improvements.map((item) => (
              <View key={item} style={styles.bullet}>
                <Ionicons name="arrow-forward-circle" size={16} color={colors.neutral} />
                <Text style={[type.body, { flex: 1 }]}>{item}</Text>
              </View>
            ))}
          </View>
        ) : null}
        {overall?.patterns?.length ? (
          <View style={[styles.list, { backgroundColor: colors.skyTint }]}>
            <View style={shared.row}>
              <Ionicons name="repeat-outline" size={16} color={colors.sageDeep} />
              <Text style={[type.eyebrow, { color: colors.sageDeep }]}>A pattern we noticed</Text>
            </View>
            {overall.patterns.map((item) => (
              <View key={item} style={styles.bullet}>
                <Ionicons name="ellipse-outline" size={16} color={colors.sageDeep} />
                <Text style={[type.body, { flex: 1 }]}>{item}</Text>
              </View>
            ))}
            <Text style={type.caption}>Just an observation, not a verdict. Is it worth thinking about?</Text>
          </View>
        ) : null}
        {overall?.reflection_questions?.length ? (
          <View style={[styles.list, { backgroundColor: colors.primaryTint }]}>
            <View style={shared.row}>
              <Ionicons name="help-circle-outline" size={16} color={colors.primary} />
              <Text style={[type.eyebrow, { color: colors.primary }]}>Worth asking yourself</Text>
            </View>
            {overall.reflection_questions.map((item) => (
              <View key={item} style={styles.bullet}>
                <Ionicons name="chatbubble-ellipses-outline" size={16} color={colors.primary} />
                <Text style={[type.body, { flex: 1 }]}>{item}</Text>
              </View>
            ))}
          </View>
        ) : null}
        <Divider />
        <View style={shared.row}>
          <Ionicons name="hardware-chip-outline" size={14} color={colors.muted} />
          <Text style={type.caption}>model {result.model_version}</Text>
        </View>
      </View>

      <SectionHeader
        title="Where it was engaging"
        caption="Stretch by stretch, so you can see where the energy rose and where it dipped."
      />
      {segments.map((segment, i) => {
        const segTone = scoreColor(segment.engagement_score);
        return (
          <View key={`${segment.start_position}-${segment.end_position}`} style={shared.card}>
            <View style={[shared.row, { justifyContent: 'space-between' }]}>
              <View style={[shared.row, { gap: 10 }]}>
                <View style={[styles.segIndex, { backgroundColor: `${segTone}22` }]}>
                  <Text style={[styles.segIndexText, { color: segTone }]}>{i + 1}</Text>
                </View>
                <View>
                  <Text style={type.subheading}>
                    Messages {segment.start_position + 1}–{segment.end_position + 1}
                  </Text>
                  <Text style={type.caption}>{Math.round(segment.engagement_score * 100)}% engagement</Text>
                </View>
              </View>
              <Badge text={segment.label} tone={segTone} />
            </View>
            <ScoreBar score={segment.engagement_score} />
            {segment.comment ? <Text style={type.body}>{segment.comment}</Text> : null}
          </View>
        );
      })}

      <View style={shared.card}>
        <SectionHeader
          title="What happened next?"
          caption="Optional, and it stays with your account — outcomes are what let us train a better model."
          icon="help-buoy-outline"
          hue="sky"
        />
        <View style={styles.outcomes}>
          {OUTCOMES.map((option) => {
            const active = outcome === option.value;
            return (
              <Pressable
                key={option.value}
                accessibilityRole="radio"
                accessibilityState={{ selected: active }}
                onPress={() => void saveLabel(option.value)}
                style={[styles.outcome, active && styles.outcomeActive]}
              >
                <Ionicons name={option.icon} size={20} color={active ? colors.primaryDeep : colors.muted} />
                <Text style={[styles.outcomeLabel, active && { color: colors.primaryDeep }]}>{option.label}</Text>
              </Pressable>
            );
          })}
        </View>
        {labelStatus ? (
          <View style={shared.row}>
            <Ionicons
              name={labelFailed ? 'alert-circle-outline' : 'heart-outline'}
              size={14}
              color={labelFailed ? colors.flat : colors.sageDeep}
            />
            <Text style={labelFailed ? shared.error : [type.caption, { color: colors.sageDeep }]}>{labelStatus}</Text>
          </View>
        ) : null}
      </View>

      <Button label="Refresh" icon="refresh-outline" variant="secondary" onPress={() => void poll()} />
    </Screen>
  );
}

const styles = StyleSheet.create({
  pending: { alignItems: 'center', gap: 10, paddingVertical: 24 },
  hero: { gap: 14 },
  heroRow: { flexDirection: 'row', alignItems: 'center', gap: 16 },
  ring: {
    width: 92,
    height: 92,
    borderRadius: 46,
    borderWidth: 6,
    alignItems: 'center',
    justifyContent: 'center',
    flexDirection: 'row',
    backgroundColor: colors.surfaceAlt,
  },
  ringValue: { fontFamily: fonts.sansBlack, fontSize: 30, letterSpacing: -1 },
  ringUnit: { fontFamily: fonts.sansBold, fontSize: 13, color: colors.muted, marginTop: 8 },
  list: { gap: 8, padding: 12, borderRadius: radii.md },
  bullet: { flexDirection: 'row', alignItems: 'flex-start', gap: 8 },
  segIndex: { width: 32, height: 32, borderRadius: 16, alignItems: 'center', justifyContent: 'center' },
  segIndexText: { fontFamily: fonts.sansBlack, fontSize: 14 },
  outcomes: { flexDirection: 'row', flexWrap: 'wrap', gap: 8 },
  outcome: {
    flexGrow: 1,
    flexBasis: '45%',
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    paddingVertical: 12,
    paddingHorizontal: 14,
    borderRadius: radii.md,
    borderWidth: 1.5,
    borderColor: colors.border,
    backgroundColor: colors.surfaceAlt,
  },
  outcomeActive: { borderColor: colors.primary, backgroundColor: colors.primaryTint },
  outcomeLabel: { fontFamily: fonts.sansSemi, fontSize: 14, color: colors.text },
});
