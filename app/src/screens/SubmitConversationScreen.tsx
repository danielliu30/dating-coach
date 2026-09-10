import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useMemo, useState } from 'react';
import { Text, View } from 'react-native';

import { api } from '../api/client';
import type { SubmitMessage } from '../api/types';
import { Button, Field, Screen } from '../components/ui';
import type { AnalysisStackParams } from '../navigation/types';
import { shared } from '../theme';

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

  return (
    <Screen>
      <Text style={shared.title}>Analyse a conversation</Text>
      <Text style={shared.muted}>
        Paste a chat and get per-stretch feedback on where it is engaging and where it drags.
      </Text>
      <View style={shared.card}>
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
        <Text style={shared.muted}>
          {parsed.messages.length} message{parsed.messages.length === 1 ? '' : 's'} parsed
          {parsed.errors.length ? ` · ${parsed.errors.length} line(s) need fixing` : ''}
        </Text>
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Get feedback" onPress={submit} loading={busy} />
        <Button label="Use the example" variant="secondary" onPress={() => setRaw(SAMPLE)} />
      </View>
      <Button label="Past analyses" variant="secondary" onPress={() => navigation.navigate('History')} />
      <Button label="Check your profile photos" variant="secondary" onPress={() => navigation.navigate('Photos')} />
    </Screen>
  );
}
