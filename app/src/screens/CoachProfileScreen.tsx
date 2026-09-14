import { Ionicons } from '@expo/vector-icons';
import { useFocusEffect } from '@react-navigation/native';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { ApiError, api } from '../api/client';
import { DATING_PHASES, type AvailabilityWindow, type CoachApprovalStatus, type DatingPhase } from '../api/types';
import { Avatar, GradientCard, SectionHeader, SkeletonCard, StatusPill } from '../components/kit';
import { Badge, Button, Chip, Field, Screen } from '../components/ui';
import { PHASE_LABELS, toggle } from '../lib/phases';
import { useAuth } from '../state/auth';
import { colors, fonts, radii, shared, type } from '../theme';

const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

const clock = (minute: number): string =>
  `${String(Math.floor(minute / 60)).padStart(2, '0')}:${String(minute % 60).padStart(2, '0')}`;

type WindowDraft = {
  key: string;
  weekday: number;
  start: string;
  end: string;
};

const draftOf = (window: AvailabilityWindow, index: number): WindowDraft => ({
  key: `${window.weekday}-${window.start_minute}-${window.end_minute}-${index}`,
  weekday: window.weekday,
  start: clock(window.start_minute),
  end: clock(window.end_minute),
});

const describe = (reason: unknown): string =>
  reason instanceof Error ? reason.message : 'unknown error';

/**
 * Banner copy for a coach whose profile is not (yet) shown to clients, or null
 * when the coach is approved and nothing needs saying.
 */
const approvalNotice = (status: CoachApprovalStatus): { title: string; body: string; tone: string } | null => {
  switch (status) {
    case 'pending':
      return {
        title: 'Your profile is under review',
        body: 'Clients can’t see or book you until an admin approves it. You can keep editing in the meantime.',
        tone: colors.neutral,
      };
    case 'rejected':
      return {
        title: 'Your profile was not approved',
        body: 'Clients can’t see or book you. Update your profile and reach out to us to request another review.',
        tone: colors.flat,
      };
    default:
      return null;
  }
};

const parseClock = (value: string): number | null => {
  const match = /^(\d{1,2}):(\d{2})$/.exec(value.trim());
  if (!match) return null;
  const minutes = Number(match[1]) * 60 + Number(match[2]);
  return minutes >= 0 && minutes <= 24 * 60 ? minutes : null;
};

export default function CoachProfileScreen(): React.ReactElement {
  const { user } = useAuth();
  const [headline, setHeadline] = useState('');
  const [bio, setBio] = useState('');
  const [specialties, setSpecialties] = useState('');
  const [phases, setPhases] = useState<DatingPhase[]>([]);
  const [rate, setRate] = useState('120');
  const [timezone, setTimezone] = useState('UTC');
  const [years, setYears] = useState('3');
  const [accepting, setAccepting] = useState(true);
  // Null until the saved profile arrives (or a fresh coach saves one).
  const [approval, setApproval] = useState<CoachApprovalStatus | null>(null);

  const [windows, setWindows] = useState<WindowDraft[]>(() =>
    [1, 2, 3, 4, 5].map((weekday) => ({ key: `default-${weekday}`, weekday, start: '17:00', end: '21:00' })),
  );

  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);

  // Both halves load independently: a failure on one must neither discard the
  // other nor leave the editor showing defaults that a save would commit.
  const coachID = user?.id;
  const [loadFailed, setLoadFailed] = useState(false);
  const [refreshError, setRefreshError] = useState<string | null>(null);
  // Every read of approval_status (mount load, focus refresh) takes a ticket and
  // may only write if nothing newer has been applied yet: a slow older read cannot
  // overwrite a fresher one, while a failed newer read discards nothing.
  const approvalRead = useRef({ issued: 0, applied: 0 });
  const applyApproval = (ticket: number, value: CoachApprovalStatus) => {
    if (ticket <= approvalRead.current.applied) return false;
    approvalRead.current.applied = ticket;
    setApproval(value);
    return true;
  };
  useEffect(() => {
    if (!coachID) return;
    let cancelled = false;
    const ticket = ++approvalRead.current.issued;
    void (async () => {
      const [profile, saved] = await Promise.allSettled([
        api.getCoach(coachID),
        api.coachAvailability(coachID),
      ]);
      if (cancelled) return;
      const problems: string[] = [];
      if (profile.status === 'fulfilled') {
        setHeadline(profile.value.headline);
        setBio(profile.value.bio);
        setSpecialties(profile.value.specialties.join(', '));
        setPhases(profile.value.phases ?? []);
        setRate(String(profile.value.hourly_rate_cents / 100));
        setTimezone(profile.value.timezone);
        setYears(String(profile.value.years_experience));
        setAccepting(profile.value.accepting_clients);
        applyApproval(ticket, profile.value.approval_status);
        // 404 simply means the coach has not published a profile yet.
      } else if (!(profile.reason instanceof ApiError && profile.reason.status === 404)) {
        setLoadFailed(true);
        problems.push(`profile (${describe(profile.reason)})`);
      }
      if (saved.status === 'fulfilled') {
        if (saved.value.length > 0) setWindows(saved.value.map(draftOf));
      } else {
        problems.push(`availability (${describe(saved.reason)})`);
      }
      if (problems.length > 0) {
        setError(`Could not load ${problems.join(' and ')}. Reload before saving.`);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [coachID]);

  // An admin decides approval outside this screen and the tab stays mounted
  // between visits, so re-read just the status on focus; form edits are kept.
  useFocusEffect(
    useCallback(() => {
      if (!coachID) return;
      let cancelled = false;
      const ticket = ++approvalRead.current.issued;
      api
        .getCoach(coachID)
        .then((profile) => {
          if (cancelled) return;
          if (applyApproval(ticket, profile.approval_status)) setRefreshError(null);
        })
        .catch((reason: unknown) => {
          if (cancelled || ticket !== approvalRead.current.issued) return;
          // 404 means nothing saved yet; anything else leaves the banner possibly stale.
          if (reason instanceof ApiError && reason.status === 404) return;
          setRefreshError(`Could not refresh your review status (${describe(reason)}); it may be out of date.`);
        });
      return () => {
        cancelled = true;
      };
    }, [coachID]),
  );

  const save = async () => {
    if (loadFailed) {
      setError('Your saved profile could not be loaded, so saving now would overwrite it. Reload first.');
      return;
    }
    const payload: AvailabilityWindow[] = [];
    for (const window of windows) {
      const startMinute = parseClock(window.start);
      const endMinute = parseClock(window.end);
      if (startMinute === null || endMinute === null || endMinute <= startMinute) {
        setError(`${WEEKDAYS[window.weekday]} times must be HH:MM with end after start.`);
        return;
      }
      payload.push({ weekday: window.weekday, start_minute: startMinute, end_minute: endMinute });
    }
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      const saved = await api.upsertCoachProfile({
        headline: headline.trim(),
        bio: bio.trim(),
        specialties: specialties
          .split(',')
          .map((item) => item.trim())
          .filter(Boolean),
        phases,
        hourly_rate_cents: Math.round(Number(rate) * 100) || 0,
        timezone: timezone.trim() || 'UTC',
        years_experience: Number(years) || 0,
        accepting_clients: accepting,
      });
      applyApproval(++approvalRead.current.issued, saved.approval_status);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not save profile');
      setBusy(false);
      return;
    }
    // The profile request is already committed, so an availability failure is a
    // partial save and has to say so.
    try {
      const { availability } = await api.setAvailability(payload);
      setWindows((availability ?? []).map(draftOf));
      setStatus('Profile and availability saved.');
    } catch (err) {
      const detail = err instanceof Error ? err.message : 'unknown error';
      setError(`Profile saved, but availability did not save (${detail}). Save again to retry.`);
    } finally {
      setBusy(false);
    }
  };

  const updateWindow = (key: string, patch: Partial<WindowDraft>) =>
    setWindows((current) => current.map((w) => (w.key === key ? { ...w, ...patch } : w)));

  const addWindow = () =>
    setWindows((current) => [
      ...current,
      { key: `new-${Date.now()}-${current.length}`, weekday: 1, start: '17:00', end: '21:00' },
    ]);

  const removeWindow = (key: string) => setWindows((current) => current.filter((w) => w.key !== key));

  const notice = approval ? approvalNotice(approval) : null;
  const name = user?.display_name ?? 'You';
  const specialtyList = specialties
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean);

  if (loading) {
    return (
      <Screen>
        <Text style={shared.title}>Your coach profile</Text>
        <SkeletonCard />
        <SkeletonCard />
      </Screen>
    );
  }

  return (
    <Screen>
      {notice ? (
        <View accessible accessibilityRole="alert" style={[styles.notice, { borderColor: notice.tone }]}>
          <Ionicons name="time-outline" size={20} color={notice.tone} />
          <View style={{ flex: 1, gap: 2 }}>
            <Text style={[type.subheading, { color: notice.tone }]}>{notice.title}</Text>
            <Text style={type.caption}>{notice.body}</Text>
          </View>
        </View>
      ) : null}
      <GradientCard gradient="meadow">
        <View style={styles.previewRow}>
          <Avatar name={name} size={64} />
          <View style={{ flex: 1, gap: 4 }}>
            <Text style={[type.eyebrow, { color: 'rgba(255,255,255,0.85)' }]}>How clients see you</Text>
            <Text style={[type.title, { color: colors.primaryText }]}>{name}</Text>
            <Text style={[type.caption, { color: 'rgba(255,255,255,0.9)' }]} numberOfLines={2}>
              {headline.trim() || 'Your headline appears here'}
            </Text>
          </View>
        </View>
        <View style={styles.previewPills}>
          <StatusPill text={accepting ? 'Accepting clients' : 'Not accepting'} tone={colors.primaryText} onDark />
          <StatusPill text={`$${Number(rate) || 0}/hr`} tone={colors.primaryText} onDark />
          <StatusPill text={`${Number(years) || 0} yrs`} tone={colors.primaryText} onDark />
        </View>
        {specialtyList.length || phases.length ? (
          <View style={styles.previewPills}>
            {phases.map((phase) => (
              <Badge key={`phase-${phase}`} text={PHASE_LABELS[phase]} tone={colors.primaryText} />
            ))}
            {specialtyList.map((item) => (
              <Badge key={`specialty-${item}`} text={item} tone={colors.primaryText} />
            ))}
          </View>
        ) : null}
      </GradientCard>

      <View style={shared.card}>
        <SectionHeader
          title="Profile"
          caption="This is what clients see in the coach directory."
          icon="person-circle-outline"
          hue="sage"
        />
        <Field label="Headline" value={headline} onChangeText={setHeadline} placeholder="Opening messages that land" />
        <Field label="Bio" value={bio} onChangeText={setBio} multiline />
        <Field
          label="Specialties (comma separated)"
          value={specialties}
          onChangeText={setSpecialties}
          placeholder="openers, profile review, texting"
        />
        <Text style={type.eyebrow}>Dating phases you coach</Text>
        <View style={[shared.row, { flexWrap: 'wrap' }]}>
          {DATING_PHASES.map((phase) => (
            <Chip
              key={phase}
              label={PHASE_LABELS[phase]}
              selected={phases.includes(phase)}
              tone={colors.primary}
              onPress={() => setPhases((prev) => toggle(prev, phase, DATING_PHASES))}
            />
          ))}
        </View>
        <View style={shared.row}>
          <View style={{ flex: 1 }}>
            <Field label="Hourly rate (USD)" value={rate} onChangeText={setRate} keyboardType="numeric" />
          </View>
          <View style={{ flex: 1 }}>
            <Field label="Years experience" value={years} onChangeText={setYears} keyboardType="numeric" />
          </View>
        </View>
        <Field label="Timezone (IANA)" value={timezone} onChangeText={setTimezone} placeholder="America/New_York" />
        <Pressable
          accessibilityRole="switch"
          accessibilityState={{ checked: accepting }}
          onPress={() => setAccepting((value) => !value)}
          style={[styles.toggle, accepting && styles.toggleOn]}
        >
          <Ionicons
            name={accepting ? 'checkmark-circle' : 'close-circle-outline'}
            size={22}
            color={accepting ? colors.engaging : colors.muted}
          />
          <View style={{ flex: 1 }}>
            <Text style={type.subheading}>{accepting ? 'Accepting new clients' : 'Not accepting new clients'}</Text>
            <Text style={type.caption}>
              {accepting ? 'You appear in the directory and can be booked.' : 'Hidden from new bookings.'}
            </Text>
          </View>
          <View style={[styles.track, accepting && styles.trackOn]}>
            <View style={[styles.knob, accepting && styles.knobOn]} />
          </View>
        </Pressable>
      </View>

      <View style={shared.card}>
        <SectionHeader
          title="Weekly availability"
          caption="One row per window: add several rows for split hours or different hours per day. Times are in your profile timezone."
          icon="time-outline"
          hue="sun"
        />
        {windows.length === 0 ? (
          <View style={styles.none}>
            <Ionicons name="calendar-clear-outline" size={18} color={colors.muted} />
            <Text style={type.caption}>No availability published. Add a window so clients can book you.</Text>
          </View>
        ) : null}
        {windows.map((window) => (
          <View key={window.key} style={styles.window}>
            <View style={[shared.row, { flexWrap: 'wrap' }]}>
              {WEEKDAYS.map((label, day) => (
                <Pressable
                  key={label}
                  accessibilityRole="radio"
                  accessibilityState={{ checked: window.weekday === day }}
                  onPress={() => updateWindow(window.key, { weekday: day })}
                  style={[styles.day, window.weekday === day && styles.dayActive]}
                >
                  <Text style={window.weekday === day ? styles.dayActiveLabel : styles.dayLabel}>{label}</Text>
                </Pressable>
              ))}
            </View>
            <View style={shared.row}>
              <View style={{ flex: 1 }}>
                <Field
                  label="From"
                  value={window.start}
                  onChangeText={(value) => updateWindow(window.key, { start: value })}
                  placeholder="17:00"
                />
              </View>
              <View style={{ flex: 1 }}>
                <Field
                  label="To"
                  value={window.end}
                  onChangeText={(value) => updateWindow(window.key, { end: value })}
                  placeholder="21:00"
                />
              </View>
            </View>
            <Pressable accessibilityRole="button" onPress={() => removeWindow(window.key)} style={styles.linkRow}>
              <Ionicons name="trash-outline" size={15} color={colors.flat} />
              <Text style={[styles.link, { color: colors.flat }]}>Remove window</Text>
            </Pressable>
          </View>
        ))}
        <Pressable accessibilityRole="button" onPress={addWindow} style={styles.add}>
          <Ionicons name="add-circle-outline" size={18} color={colors.primaryDeep} />
          <Text style={styles.link}>Add window</Text>
        </Pressable>
      </View>

      {status ? (
        <View style={shared.row}>
          <Ionicons name="checkmark-circle" size={16} color={colors.engaging} />
          <Text style={[type.caption, { color: colors.engaging }]}>{status}</Text>
        </View>
      ) : null}
      {error ? <Text style={shared.error}>{error}</Text> : null}
      {refreshError ? <Text style={shared.error}>{refreshError}</Text> : null}
      <Button label="Save profile" icon="save-outline" onPress={save} loading={busy} />
    </Screen>
  );
}

const styles = StyleSheet.create({
  notice: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    gap: 10,
    padding: 12,
    borderRadius: radii.md,
    borderWidth: 1.5,
    backgroundColor: colors.surfaceAlt,
  },
  previewRow: { flexDirection: 'row', alignItems: 'center', gap: 14 },
  previewPills: { flexDirection: 'row', flexWrap: 'wrap', gap: 6 },
  toggle: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
    padding: 12,
    borderRadius: radii.md,
    borderWidth: 1.5,
    borderColor: colors.border,
    backgroundColor: colors.surfaceAlt,
  },
  toggleOn: { borderColor: colors.sage, backgroundColor: colors.sageTint },
  track: { width: 42, height: 24, borderRadius: 12, backgroundColor: colors.sand, padding: 2 },
  trackOn: { backgroundColor: colors.engaging },
  knob: { width: 20, height: 20, borderRadius: 10, backgroundColor: colors.surface },
  knobOn: { alignSelf: 'flex-end' },
  none: { flexDirection: 'row', alignItems: 'center', gap: 8 },
  day: {
    borderWidth: 1.5,
    borderColor: colors.border,
    backgroundColor: colors.surface,
    borderRadius: 999,
    paddingVertical: 8,
    paddingHorizontal: 12,
  },
  window: {
    padding: 12,
    marginTop: 4,
    gap: 8,
    borderRadius: radii.md,
    backgroundColor: colors.surfaceAlt,
    borderWidth: 1,
    borderColor: colors.border,
  },
  dayActive: { borderColor: colors.primary, backgroundColor: colors.primaryTint },
  dayLabel: { color: colors.muted, fontFamily: fonts.sansSemi, fontSize: 13 },
  dayActiveLabel: { color: colors.primaryDeep, fontFamily: fonts.sansBold, fontSize: 13 },
  linkRow: { flexDirection: 'row', alignItems: 'center', gap: 6, alignSelf: 'flex-start' },
  add: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 6,
    paddingVertical: 12,
    borderRadius: radii.md,
    borderWidth: 1.5,
    borderStyle: 'dashed',
    borderColor: colors.primaryLight,
  },
  link: { color: colors.primaryDeep, fontFamily: fonts.sansBold, fontSize: 14 },
});
