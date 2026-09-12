import { Ionicons } from '@expo/vector-icons';
import { useNavigation, type NavigationProp } from '@react-navigation/native';
import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import { api } from '../api/client';
import type { Coach, Slot } from '../api/types';
import { Avatar, Divider, GradientCard, IconDisc, MetaRow, SectionHeader, Skeleton, SkeletonCard } from '../components/kit';
import { Badge, Button, Chip, Field, Loading, Screen } from '../components/ui';
import { useAsync } from '../hooks/useAsync';
import { PHASE_LABELS } from '../lib/phases';
import type { CoachesStackParams, RootTabParams } from '../navigation/types';
import { colors, fonts, radii, shared, type } from '../theme';

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const DURATIONS = [30, 45, 60];

const minutesToClock = (minute: number): string =>
  `${String(Math.floor(minute / 60)).padStart(2, '0')}:${String(minute % 60).padStart(2, '0')}`;

const slotLabel = (slot: Slot): string =>
  new Date(slot.start).toLocaleString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  });

/** Splits a slot label into a day line and a time line for the slot tile. */
const slotParts = (slot: Slot): { day: string; time: string } => {
  const d = new Date(slot.start);
  return {
    day: d.toLocaleString(undefined, { weekday: 'short', month: 'short', day: 'numeric' }),
    time: d.toLocaleString(undefined, { hour: 'numeric', minute: '2-digit' }),
  };
};

/** "4.5 · 12 reviews" (or "No reviews yet") for the rating badge. */
const ratingLabel = (c: Pick<Coach, 'avg_rating' | 'review_count'>): string =>
  c.review_count > 0
    ? `★ ${c.avg_rating.toFixed(1)} · ${c.review_count} review${c.review_count === 1 ? '' : 's'}`
    : 'No reviews yet';

const STARS = [1, 2, 3, 4, 5];

export default function CoachDetailScreen({
  route,
}: NativeStackScreenProps<CoachesStackParams, 'CoachDetail'>): React.ReactElement {
  const { coachID } = route.params;
  const navigation = useNavigation<NavigationProp<RootTabParams>>();

  const [duration, setDuration] = useState(45);
  const [topic, setTopic] = useState('');
  const [selected, setSelected] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const coach = useAsync(() => api.getCoach(coachID), [coachID]);
  const availability = useAsync(() => api.coachAvailability(coachID), [coachID]);
  const slots = useAsync(() => api.openSlots(coachID, duration), [coachID, duration]);
  const reviews = useAsync(() => api.listCoachReviews(coachID), [coachID]);
  // The form is only offered once the signed-in client has a completed session
  // with this coach; the server enforces the same rule.
  const completed = useAsync(
    async () => (await api.mySessions('completed')).some((s) => s.coach_id === coachID),
    [coachID],
  );

  const [rating, setRating] = useState(0);
  const [comment, setComment] = useState('');
  const [reviewBusy, setReviewBusy] = useState(false);
  const [reviewError, setReviewError] = useState<string | null>(null);
  const [reviewStatus, setReviewStatus] = useState<string | null>(null);

  const submitReview = async () => {
    if (rating < 1) {
      setReviewError('Pick a star rating first.');
      return;
    }
    setReviewBusy(true);
    setReviewError(null);
    setReviewStatus(null);
    try {
      await api.submitCoachReview(coachID, { rating, comment: comment.trim() });
      setReviewStatus('Thanks — your review is posted.');
      setComment('');
      setRating(0);
      await Promise.all([reviews.reload(), coach.reload()]);
    } catch (err) {
      setReviewError(err instanceof Error ? err.message : 'could not post review');
    } finally {
      setReviewBusy(false);
    }
  };

  const book = async () => {
    if (!selected) {
      setError('Pick a time slot first.');
      return;
    }
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await api.bookSession({
        coach_id: coachID,
        scheduled_time: selected,
        duration_minutes: duration,
        topic: topic.trim(),
      });
      setStatus('Session booked — see it under Sessions.');
      setSelected(null);
      await slots.reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not book');
    } finally {
      setBusy(false);
    }
  };

  const startChat = async () => {
    setError(null);
    try {
      const thread = await api.startThread(coachID);
      navigation.navigate('Chats', {
        screen: 'Chat',
        params: { threadID: thread.id, title: route.params.coachName },
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not start chat');
    }
  };

  if (!coach.data) {
    return (
      <Screen>
        {coach.loading ? (
          <>
            <SkeletonCard />
            <Skeleton height={120} radius={radii.lg} />
          </>
        ) : (
          <Text style={shared.error}>{coach.error}</Text>
        )}
      </Screen>
    );
  }

  const c = coach.data;
  const rate = (c.hourly_rate_cents / 100).toFixed(0);
  const selectedSlot = (slots.data ?? []).find((s) => s.start === selected) ?? null;

  return (
    <Screen>
      <GradientCard gradient="meadow">
        <View style={styles.profileRow}>
          <Avatar name={c.display_name} size={72} />
          <View style={{ flex: 1, gap: 4 }}>
            <Text style={[type.eyebrow, { color: 'rgba(255,255,255,0.85)' }]}>Dating Humane coach</Text>
            <Text style={[type.title, { color: colors.primaryText }]}>{c.display_name}</Text>
            {c.headline ? <Text style={[type.body, { color: 'rgba(255,255,255,0.92)' }]}>{c.headline}</Text> : null}
          </View>
        </View>
        <View style={styles.factRow}>
          <View style={styles.fact}>
            <Text style={styles.factValue}>${rate}</Text>
            <Text style={styles.factLabel}>per hour</Text>
          </View>
          <View style={styles.fact}>
            <Text style={styles.factValue}>{c.years_experience > 0 ? `${c.years_experience}y` : 'New'}</Text>
            <Text style={styles.factLabel}>experience</Text>
          </View>
          <View style={[styles.fact, { flex: 1.4 }]}>
            <Text style={styles.factValue} numberOfLines={1}>
              {c.timezone}
            </Text>
            <Text style={styles.factLabel}>timezone</Text>
          </View>
        </View>
      </GradientCard>

      <View style={shared.card}>
        <SectionHeader title="About" caption={c.accepting_clients ? 'Accepting new clients' : 'Not accepting clients'} />
        {c.bio ? (
          <Text style={type.body}>{c.bio}</Text>
        ) : (
          <Text style={type.caption}>This coach has not written a bio yet.</Text>
        )}
        {c.specialties.length > 0 || c.phases.length > 0 ? (
          <>
            <Text style={styles.label}>Specialties</Text>
            <View style={[shared.row, { flexWrap: 'wrap', gap: 6 }]}>
              {c.phases.map((phase) => (
                <Badge key={`phase-${phase}`} text={PHASE_LABELS[phase]} tone={colors.primary} />
              ))}
              {c.specialties.map((specialty) => (
                <Badge key={`specialty-${specialty}`} text={specialty} tone={colors.primaryDeep} />
              ))}
            </View>
          </>
        ) : null}
        <View style={[shared.row, { flexWrap: 'wrap', gap: 6 }]}>
          <Badge text={ratingLabel(c)} tone={colors.primary} />
        </View>
        <Divider />
        <View style={styles.chatRow}>
          <IconDisc icon="chatbubbles-outline" hue="sky" size={44} />
          <View style={{ flex: 1, gap: 2 }}>
            <Text style={type.subheading}>Need a quick perspective?</Text>
            <Text style={type.caption}>Start a live chat — no booking required.</Text>
          </View>
        </View>
        <Button label="Start a live chat" icon="chatbubble-outline" variant="secondary" onPress={startChat} />
      </View>

      <View style={shared.card}>
        <SectionHeader
          title="Reviews"
          caption={c.review_count > 0 ? `${c.avg_rating.toFixed(1)} average from ${c.review_count}` : 'From clients who completed a session'}
        />
        {reviews.loading ? (
          <Skeleton height={48} />
        ) : reviews.data && reviews.data.length > 0 ? (
          reviews.data.map((review) => (
            <View key={review.id} style={styles.review}>
              <View style={shared.row}>
                <Text style={type.subheading}>{review.reviewer_name}</Text>
                <Badge text={`★ ${review.rating}`} tone={colors.primary} />
              </View>
              {review.comment ? <Text style={type.body}>{review.comment}</Text> : null}
              <Text style={type.caption}>{new Date(review.created_at).toLocaleDateString()}</Text>
            </View>
          ))
        ) : (
          <Text style={type.caption}>No reviews yet.</Text>
        )}
        {completed.data ? (
          <>
            <Divider />
            <Text style={styles.label}>Rate your sessions</Text>
            <View style={[shared.row, { flexWrap: 'wrap', gap: 6 }]}>
              {STARS.map((star) => (
                <Chip
                  key={star}
                  label={`${star} ★`}
                  selected={star <= rating}
                  tone={colors.primary}
                  onPress={() => setRating(star)}
                />
              ))}
            </View>
            <Field
              label="Comment (optional)"
              value={comment}
              onChangeText={setComment}
              placeholder="What helped most?"
              multiline
            />
            {reviewStatus ? (
              <View style={shared.row}>
                <Ionicons name="checkmark-circle" size={16} color={colors.engaging} />
                <Text style={[type.caption, { color: colors.engaging }]}>{reviewStatus}</Text>
              </View>
            ) : null}
            {reviewError ? <Text style={shared.error}>{reviewError}</Text> : null}
            <Button label="Post review" icon="star-outline" variant="secondary" loading={reviewBusy} onPress={submitReview} />
          </>
        ) : null}
      </View>

      <View style={shared.card}>
        <SectionHeader title="Weekly availability" caption={`Shown in ${c.timezone}`} />
        {availability.data?.length ? (
          <View style={{ gap: 8 }}>
            {availability.data.map((window, index) => (
              <View key={`${window.weekday}-${index}`} style={styles.window}>
                <View style={styles.windowDay}>
                  <Text style={styles.windowDayText}>{WEEKDAYS[window.weekday] ?? window.weekday}</Text>
                </View>
                <Ionicons name="time-outline" size={16} color={colors.sageDeep} />
                <Text style={type.body}>
                  {minutesToClock(window.start_minute)} – {minutesToClock(window.end_minute)}
                </Text>
              </View>
            ))}
          </View>
        ) : (
          <MetaRow icon="calendar-clear-outline" text="This coach has not published availability yet." />
        )}
      </View>

      <View style={shared.card}>
        <SectionHeader title="Book a session" caption="Pick a length, choose a time, tell them what to focus on." />
        <Text style={styles.label}>Session length</Text>
        <View style={shared.row}>
          {DURATIONS.map((minutes) => {
            const active = duration === minutes;
            return (
              <Pressable
                key={minutes}
                accessibilityRole="radio"
                accessibilityState={{ selected: active }}
                onPress={() => setDuration(minutes)}
                style={[styles.duration, active && styles.durationActive]}
              >
                <Text style={[styles.durationValue, active && { color: colors.primaryText }]}>{minutes}</Text>
                <Text style={[styles.durationUnit, active && { color: 'rgba(255,255,255,0.85)' }]}>min</Text>
              </Pressable>
            );
          })}
        </View>

        <Text style={styles.label}>Open slots · next 7 days</Text>
        {slots.loading && !slots.data ? <Loading /> : null}
        {slots.data?.length === 0 ? (
          <MetaRow icon="calendar-clear-outline" text="No open slots in this window. Try another length." />
        ) : null}
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: 8 }}>
          {(slots.data ?? []).map((slot) => {
            const active = selected === slot.start;
            const parts = slotParts(slot);
            return (
              <Pressable
                key={slot.start}
                accessibilityRole="button"
                accessibilityLabel={slotLabel(slot)}
                onPress={() => setSelected(slot.start)}
                style={[styles.slot, active && styles.slotActive]}
              >
                <Text style={[styles.slotDay, active && { color: colors.primaryDeep }]}>{parts.day}</Text>
                <Text style={[styles.slotTime, active && { color: colors.primaryDeep }]}>{parts.time}</Text>
                {active ? <Ionicons name="checkmark-circle" size={16} color={colors.primary} /> : null}
              </Pressable>
            );
          })}
        </ScrollView>

        <Field
          label="What do you want to work on?"
          value={topic}
          onChangeText={setTopic}
          placeholder="Opening messages on Hinge"
        />
        <View style={styles.summary}>
          <Ionicons name="receipt-outline" size={16} color={colors.bark} />
          <Text style={[type.caption, { flex: 1, color: colors.bark }]}>
            {selectedSlot
              ? `${duration} min with ${c.display_name} · ${slotLabel(selectedSlot)}`
              : `${duration} min with ${c.display_name} · pick a time above`}
          </Text>
          <Text style={styles.summaryPrice}>${(Math.round((c.hourly_rate_cents * duration) / 60) / 100).toFixed(2)}</Text>
        </View>
        {status ? (
          <View style={shared.row}>
            <Ionicons name="checkmark-circle" size={16} color={colors.engaging} />
            <Text style={[type.caption, { color: colors.engaging }]}>{status}</Text>
          </View>
        ) : null}
        {error ? <Text style={shared.error}>{error}</Text> : null}
        <Button label="Book session" icon="calendar-outline" onPress={book} loading={busy} />
      </View>
    </Screen>
  );
}

const styles = StyleSheet.create({
  profileRow: { flexDirection: 'row', alignItems: 'center', gap: 16 },
  factRow: { flexDirection: 'row', gap: 8, marginTop: 4 },
  fact: {
    flex: 1,
    padding: 10,
    borderRadius: radii.md,
    backgroundColor: 'rgba(255,255,255,0.16)',
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.22)',
  },
  factValue: { fontFamily: fonts.sansBlack, fontSize: 17, color: colors.primaryText, letterSpacing: -0.3 },
  factLabel: { fontFamily: fonts.sans, fontSize: 11, color: 'rgba(255,255,255,0.8)' },
  label: {
    fontFamily: fonts.sansBold,
    fontSize: 11.5,
    letterSpacing: 0.8,
    textTransform: 'uppercase',
    color: colors.bark,
    marginTop: 2,
  },
  chatRow: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  review: {
    gap: 4,
    padding: 12,
    borderRadius: radii.md,
    backgroundColor: colors.sageTint,
  },
  window: { flexDirection: 'row', alignItems: 'center', gap: 10 },
  windowDay: {
    width: 44,
    paddingVertical: 4,
    borderRadius: radii.sm,
    backgroundColor: colors.sageTint,
    alignItems: 'center',
  },
  windowDayText: { fontFamily: fonts.sansBold, fontSize: 12, color: colors.sageDeep },
  duration: {
    flex: 1,
    alignItems: 'center',
    paddingVertical: 12,
    borderRadius: radii.md,
    borderWidth: 1.5,
    borderColor: colors.border,
    backgroundColor: colors.surfaceAlt,
  },
  durationActive: { borderColor: colors.primary, backgroundColor: colors.primary },
  durationValue: { fontFamily: fonts.sansBlack, fontSize: 20, color: colors.text, letterSpacing: -0.5 },
  durationUnit: { ...type.caption, fontSize: 11 },
  slot: {
    minWidth: 128,
    gap: 2,
    paddingVertical: 10,
    paddingHorizontal: 14,
    borderRadius: radii.md,
    borderWidth: 1.5,
    borderColor: colors.border,
    backgroundColor: colors.surfaceAlt,
  },
  slotActive: { borderColor: colors.primary, backgroundColor: colors.primaryTint },
  slotDay: { ...type.caption, fontFamily: fonts.sansSemi, fontSize: 12 },
  slotTime: { fontFamily: fonts.sansBold, fontSize: 15, color: colors.text },
  summary: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    padding: 12,
    borderRadius: radii.md,
    backgroundColor: colors.sunTint,
  },
  summaryPrice: { fontFamily: fonts.sansBlack, fontSize: 16, color: colors.text },
});
