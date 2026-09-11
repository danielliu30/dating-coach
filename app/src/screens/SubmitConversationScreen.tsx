import { Ionicons } from '@expo/vector-icons';
import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useMemo, useState } from 'react';
import { StyleSheet, Text, View } from 'react-native';

import { api } from '../api/client';
import type { SubmitMessage } from '../api/types';
import { IconDisc, PageHeader, SectionHeader, StatRow, StatTile } from '../components/kit';
import { Button, Field, Screen } from '../components/ui';
import type { AnalysisStackParams } from '../navigation/types';
import { colors, fonts, radii, shared, type } from '../theme';

const STEPS: { icon: React.ComponentProps<typeof Ionicons>['name']; title: string; text: string }[] = [
  { icon: 'clipboard-outline', title: 'Paste the chat', text: 'One message per line, "me:" or "them:" first.' },
  { icon: 'analytics-outline', title: 'We read the rhythm', text: 'Where it flows, where it stalls, who is carrying it.' },
  { icon: 'bulb-outline', title: 'You get specifics', text: 'Per-stretch feedback you can actually use next time.' },
];

const SAMPLE = `me: your profile says you bake — what was the last thing you made?
them: sourdough, badly. it came out like a frisbee
me: a frisbee sounds structurally impressive. what went wrong, the starter?`;

/**
 * Parses a pasted transcript. Each line is "<who>: <message>", where who is
 * me/self/you for the user and them/match/her/him for the other person.
 */
export function parseTranscript(raw: string): { messages: SubmitMessage[]; errors: string[] } {
  const messages: SubmitMessage[] = [];
  const errors: string[] = [];

  raw
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean)
    .forEach((line, index) => {
      const match = /^([a-zA-Z]+)\s*:\s*(.+)$/.exec(line);
      if (!match) {
        errors.push(`Line ${index + 1} is missing a "me:" or "them:" prefix.`);
        return;
      }
      const [, who, body] = match;
      const key = who.toLowerCase();
      if (['me', 'self', 'i', 'you'].includes(key)) {
        messages.push({ sender: 'self', body });
      } else if (['them', 'match', 'they', 'her', 'him'].includes(key)) {
        messages.push({ sender: 'match', body });
      } else {
        errors.push(`Line ${index + 1}: unknown speaker "${who}".`);
      }
    });

  return { messages, errors };
}

export default function SubmitConversationScreen({
  navigation,
}: NativeStackScreenProps<AnalysisStackParams, 'Submit'>): React.ReactElement {
  const [title, setTitle] = useState('');
  const [platform, setPlatform] = useState('hinge');
  const [matchName, setMatchName] = useState('');
  const [raw, setRaw] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const parsed = useMemo(() => parseTranscript(raw), [raw]);

  const submit = async () => {
    if (parsed.messages.length === 0) {
      setError('Paste at least one message.');
      return;
    }
    if (parsed.errors.length > 0) {
      setError(parsed.errors[0]);
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const result = await api.submitConversation({
        title: title.trim() || 'Untitled conversation',
        platform: platform.trim() || 'unknown',
        match_name: matchName.trim(),
        messages: parsed.messages,
      });
      navigation.navigate('Result', { analysisID: result.id, conversationID: result.conversation_id });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not submit conversation');
    } finally {
      setBusy(false);
    }
  };

  const selfCount = parsed.messages.filter((m) => m.sender === 'self').length;
  const matchCount = parsed.messages.length - selfCount;

  return (
    <Screen>
      <PageHeader
        eyebrow="Conversation feedback"
        title="Analyse a conversation"
        subtitle="Paste a chat and get per-stretch feedback on where it is engaging and where it drags."
        gradient="sunrise"
        aside={<IconDisc icon="sparkles-outline" hue="sun" size={52} />}
      />
      <View style={styles.steps}>
        {STEPS.map((step, i) => (
          <View key={step.title} style={styles.step}>
            <View style={styles.stepTop}>
              <IconDisc icon={step.icon} hue={i === 0 ? 'sage' : i === 1 ? 'sky' : 'rose'} size={34} />
              <Text style={styles.stepIndex}>0{i + 1}</Text>
            </View>
            <Text style={type.subheading}>{step.title}</Text>
            <Text style={[type.caption, { fontSize: 12 }]}>{step.text}</Text>
          </View>
        ))}
      </View>
      <View style={shared.card}>
        <SectionHeader title="The conversation" caption="A title helps you find it later." icon="chatbubbles-outline" hue="sky" />
        <Field label="Title" value={title} onChangeText={setTitle} placeholder="Sourdough match" />
        <View style={shared.row}>
          <View style={{ flex: 1 }}>
            <Field label="Platform" value={platform} onChangeText={setPlatform} autoCapitalize="none" />
          </View>
          <View style={{ flex: 1 }}>
            <Field label="Their name" value={matchName} onChangeText={setMatchName} placeholder="optional" />
          </View>
        </View>
        <Field
          label={'Transcript — one message per line, prefixed "me:" or "them:"'}
          value={raw}
          onChangeText={setRaw}
          multiline
          placeholder={SAMPLE}
        />
        <StatRow>
          <StatTile value={parsed.messages.length} label="Parsed" icon="checkmark-done-outline" hue="sage" />
          <StatTile value={selfCount} label="From you" icon="person-outline" hue="rose" />
          <StatTile value={matchCount} label="From them" icon="people-outline" hue="sky" />
        </StatRow>
        {parsed.errors.length ? (
          <View style={styles.warn}>
            <Ionicons name="alert-circle-outline" size={16} color={colors.neutral} />
            <Text style={[type.caption, { color: colors.neutral, flex: 1 }]}>
              {parsed.errors.length} line{parsed.errors.length === 1 ? '' : 's'} need fixing · {parsed.errors[0]}
            </Text>
          </View>
        ) : null}
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Get feedback" icon="sparkles-outline" onPress={submit} loading={busy} />
        <Button label="Use the example" icon="document-text-outline" variant="secondary" onPress={() => setRaw(SAMPLE)} />
      </View>
      <View style={styles.history}>
        <IconDisc icon="time-outline" hue="sun" size={40} />
        <View style={{ flex: 1, gap: 2 }}>
          <Text style={type.subheading}>Past analyses</Text>
          <Text style={type.caption}>Revisit earlier conversations and see how your patterns shift.</Text>
        </View>
        <Button label="Open" icon="arrow-forward" variant="secondary" onPress={() => navigation.navigate('History')} />
      </View>
    </Screen>
  );
}

const styles = StyleSheet.create({
  steps: { flexDirection: 'row', gap: 10 },
  step: {
    flex: 1,
    gap: 6,
    padding: 12,
    borderRadius: radii.md,
    backgroundColor: colors.surface,
    borderWidth: 1,
    borderColor: colors.border,
  },
  stepTop: { flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' },
  stepIndex: { fontFamily: fonts.serifItalic, fontSize: 18, color: colors.sand },
  warn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    padding: 10,
    borderRadius: radii.md,
    backgroundColor: colors.noticeBg,
  },
  history: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
    padding: 14,
    borderRadius: radii.lg,
    backgroundColor: colors.sunTint,
  },
});
