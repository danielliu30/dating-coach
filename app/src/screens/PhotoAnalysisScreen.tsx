import React, { useMemo, useState } from 'react';
import { Image, Text, View } from 'react-native';

import { api } from '../api/client';
import { MAX_ANALYSIS_IMAGES } from '../api/types';
import type { ImageAnalysis, ImageAssessment, ImageRef } from '../api/types';
import { Badge, Button, Field, Screen, ScoreBar } from '../components/ui';
import { colors, shared } from '../theme';

const SAMPLE = `https://images.example.com/me/hiking.jpg
https://images.example.com/me/dinner-with-friends.jpg`;

/**
 * Absolute http(s) URL: a hostname made of letters, digits, dots and hyphens
 * (or a bracketed IPv6 literal), an optional port, then an optional
 * path/query/fragment. No userinfo. A plain regex rather than `new URL`
 * because React Native's URL polyfill is a thin string wrapper that does
 * not validate.
 */
const PHOTO_LINK_RE = /^https?:\/\/(?:[a-z0-9-]+(?:\.[a-z0-9-]+)*\.?|\[[0-9a-f:.]+\])(?::\d{1,5})?(?:[/?#]\S*)?$/i;

/**
 * Parses pasted photo links, one per line, into image refs for the image
 * track. Blank lines are skipped. A line is rejected unless it matches
 * PHOTO_LINK_RE (stricter than the server, which only requires an http(s)
 * scheme and a non-empty authority), or once more
 * than MAX_ANALYSIS_IMAGES valid lines have been seen. Errors name the
 * 1-based line so the user can fix the exact one.
 */
export function parsePhotoLinks(raw: string): { images: ImageRef[]; errors: string[] } {
  const images: ImageRef[] = [];
  const errors: string[] = [];

  raw
    .split('\n')
    .map((line) => line.trim())
    .forEach((line, index) => {
      if (!line) {
        return;
      }
      if (!PHOTO_LINK_RE.test(line)) {
        errors.push(`Line ${index + 1} is not an http(s) link.`);
        return;
      }
      if (images.length >= MAX_ANALYSIS_IMAGES) {
        errors.push(`Line ${index + 1}: at most ${MAX_ANALYSIS_IMAGES} photos per check.`);
        return;
      }
      images.push({ url: line });
    });

  return { images, errors };
}

/** One photo's verdict: thumbnail, the two yes/no calls with their scores, and the tailored note. */
function PhotoCard({ image, assessment }: { image?: ImageRef; assessment: ImageAssessment }): React.ReactElement {
  return (
    <View style={shared.card}>
      <View style={[shared.row, { alignItems: 'flex-start' }]}>
        {image?.url ? (
          <Image
            source={{ uri: image.url }}
            accessibilityLabel={`Photo ${assessment.index + 1}`}
            style={{ width: 72, height: 72, borderRadius: 10, backgroundColor: colors.border }}
          />
        ) : null}
        <View style={{ flex: 1, gap: 8 }}>
          <Text style={shared.heading}>Photo {assessment.index + 1}</Text>
          <View style={[shared.row, { flexWrap: 'wrap' }]}>
            <Badge
              text={assessment.is_clear ? 'Clear' : 'Not clear'}
              tone={assessment.is_clear ? colors.engaging : colors.flat}
            />
            <Badge
              text={assessment.is_customer_focal_point ? 'You are the focus' : 'Focus unclear'}
              tone={assessment.is_customer_focal_point ? colors.engaging : colors.flat}
            />
          </View>
        </View>
      </View>
      <Text style={shared.muted}>Clarity · {Math.round(assessment.clarity_score * 100)}%</Text>
      <ScoreBar score={assessment.clarity_score} />
      <Text style={shared.muted}>Focus on you · {Math.round(assessment.subject_focus_score * 100)}%</Text>
      <ScoreBar score={assessment.subject_focus_score} />
      {assessment.feedback ? <Text style={shared.body}>{assessment.feedback}</Text> : null}
    </View>
  );
}

export default function PhotoAnalysisScreen(): React.ReactElement {
  const [raw, setRaw] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [submitted, setSubmitted] = useState<ImageRef[]>([]);
  const [result, setResult] = useState<ImageAnalysis | null>(null);

  const parsed = useMemo(() => parsePhotoLinks(raw), [raw]);

  const submit = async () => {
    if (parsed.images.length === 0) {
      setError('Paste at least one photo link.');
      return;
    }
    if (parsed.errors.length > 0) {
      setError(parsed.errors[0]);
      return;
    }
    setBusy(true);
    setError(null);
    setResult(null);
    setSubmitted([]);
    try {
      const analysis = await api.analyzeImages({ images: parsed.images });
      setSubmitted(parsed.images);
      setResult(analysis);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not analyse photos');
    } finally {
      setBusy(false);
    }
  };

  const overall = result?.overall;

  return (
    <Screen>
      <Text style={shared.title}>Check your profile photos</Text>
      <Text style={shared.muted}>
        Paste links to your photos. Each is checked for sharpness and lighting, and for whether you are
        clearly the focus — with notes tailored to what you have said you are looking for.
      </Text>
      <View style={shared.card}>
        <Field
          label={`Photo links — one per line, up to ${MAX_ANALYSIS_IMAGES}`}
          value={raw}
          onChangeText={setRaw}
          multiline
          autoCapitalize="none"
          autoCorrect={false}
          placeholder={SAMPLE}
        />
        <Text style={shared.muted}>
          {parsed.images.length} photo{parsed.images.length === 1 ? '' : 's'} ready
          {parsed.errors.length ? ` · ${parsed.errors.length} line(s) need fixing` : ''}
        </Text>
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Check photos" onPress={submit} loading={busy} />
      </View>

      {result && overall ? (
        <>
          <View style={shared.card}>
            <Text style={shared.heading}>Overall</Text>
            {overall.summary ? <Text style={shared.body}>{overall.summary}</Text> : null}
            {overall.strengths.length ? (
              <View style={{ gap: 4 }}>
                <Text style={shared.muted}>What works</Text>
                {overall.strengths.map((item) => (
                  <Text key={item} style={shared.body}>
                    • {item}
                  </Text>
                ))}
              </View>
            ) : null}
            {overall.improvements.length ? (
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
          {result.images.map((assessment) => (
            <PhotoCard
              key={assessment.index}
              image={submitted[assessment.index]}
              assessment={assessment}
            />
          ))}
        </>
      ) : null}
    </Screen>
  );
}
