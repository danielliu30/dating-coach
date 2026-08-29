import React, { useEffect, useState } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { ApiError, api } from '../api/client';
import type { AvailabilityWindow } from '../api/types';
import { Button, Field, Loading, Screen } from '../components/ui';
import { useAuth } from '../state/auth';
import { colors, shared } from '../theme';

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
  const [rate, setRate] = useState('120');
  const [timezone, setTimezone] = useState('UTC');
  const [years, setYears] = useState('3');
  const [accepting, setAccepting] = useState(true);

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
  useEffect(() => {
    if (!coachID) return;
    let cancelled = false;
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
        setRate(String(profile.value.hourly_rate_cents / 100));
        setTimezone(profile.value.timezone);
        setYears(String(profile.value.years_experience));
        setAccepting(profile.value.accepting_clients);
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
      await api.upsertCoachProfile({
        headline: headline.trim(),
        bio: bio.trim(),
        specialties: specialties
          .split(',')
          .map((item) => item.trim())
          .filter(Boolean),
        hourly_rate_cents: Math.round(Number(rate) * 100) || 0,
        timezone: timezone.trim() || 'UTC',
        years_experience: Number(years) || 0,
        accepting_clients: accepting,
      });
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

  if (loading) {
    return (
      <Screen>
        <Text style={shared.title}>Your coach profile</Text>
        <Loading />
      </Screen>
    );
  }

  return (
    <Screen>
      <Text style={shared.title}>Your coach profile</Text>
      <Text style={shared.muted}>This is what clients see in the coach directory.</Text>
      <View style={shared.card}>
        <Field label="Headline" value={headline} onChangeText={setHeadline} placeholder="Opening messages that land" />
        <Field label="Bio" value={bio} onChangeText={setBio} multiline />
        <Field
          label="Specialties (comma separated)"
          value={specialties}
          onChangeText={setSpecialties}
          placeholder="openers, profile review, texting"
        />
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
        >
          <Text style={shared.body}>{accepting ? '✓ Accepting new clients' : '✗ Not accepting new clients'}</Text>
        </Pressable>
      </View>

      <View style={shared.card}>
        <Text style={shared.heading}>Weekly availability</Text>
        <Text style={shared.muted}>
          One row per window: add several rows for split hours or different hours per day. Times are in your profile
          timezone.
        </Text>
        {windows.length === 0 ? <Text style={shared.muted}>No availability published.</Text> : null}
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
            <Pressable accessibilityRole="button" onPress={() => removeWindow(window.key)}>
              <Text style={styles.link}>Remove window</Text>
            </Pressable>
          </View>
        ))}
        <Pressable accessibilityRole="button" onPress={addWindow}>
          <Text style={styles.link}>+ Add window</Text>
        </Pressable>
      </View>

      {status ? <Text style={shared.muted}>{status}</Text> : null}
      {error ? <Text style={shared.error}>{error}</Text> : null}
      <Button label="Save profile" onPress={save} loading={busy} />
    </Screen>
  );
}

const styles = StyleSheet.create({
  day: {
    borderWidth: 1,
    borderColor: colors.border,
    backgroundColor: colors.surface,
    borderRadius: 999,
    paddingVertical: 8,
    paddingHorizontal: 12,
  },
  window: {
    borderTopWidth: 1,
    borderTopColor: colors.border,
    paddingTop: 12,
    marginTop: 12,
    gap: 8,
  },
  dayActive: { borderColor: colors.primary, backgroundColor: '#fdf0f4' },
  dayLabel: { color: colors.muted, fontWeight: '600', fontSize: 13 },
  dayActiveLabel: { color: colors.primary, fontWeight: '700', fontSize: 13 },
  link: { color: colors.primary, fontWeight: '600', fontSize: 14 },
});
