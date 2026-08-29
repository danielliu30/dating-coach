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

  const [weekdays, setWeekdays] = useState<number[]>([1, 2, 3, 4, 5]);
  const [start, setStart] = useState('17:00');
  const [end, setEnd] = useState('21:00');

  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [loading, setLoading] = useState(true);

  // Load the saved profile once so saving cannot overwrite it with defaults.
  const coachID = user?.id;
  useEffect(() => {
    if (!coachID) return;
    let cancelled = false;
    void (async () => {
      try {
        const [profile, windows] = await Promise.all([
          api.getCoach(coachID),
          api.coachAvailability(coachID),
        ]);
        if (cancelled) return;
        setHeadline(profile.headline);
        setBio(profile.bio);
        setSpecialties(profile.specialties.join(', '));
        setRate(String(profile.hourly_rate_cents / 100));
        setTimezone(profile.timezone);
        setYears(String(profile.years_experience));
        setAccepting(profile.accepting_clients);
        if (windows.length > 0) {
          setWeekdays(windows.map((w) => w.weekday).sort((a, b) => a - b));
          setStart(clock(windows[0].start_minute));
          setEnd(clock(windows[0].end_minute));
        }
      } catch (err) {
        // 404 simply means the coach has not published a profile yet.
        if (!cancelled && !(err instanceof ApiError && err.status === 404)) {
          setError(err instanceof Error ? err.message : 'could not load profile');
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [coachID]);

  const save = async () => {
    const startMinute = parseClock(start);
    const endMinute = parseClock(end);
    if (startMinute === null || endMinute === null || endMinute <= startMinute) {
      setError('Availability times must be HH:MM with end after start.');
      return;
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
      const windows: AvailabilityWindow[] = weekdays.map((weekday) => ({
        weekday,
        start_minute: startMinute,
        end_minute: endMinute,
      }));
      await api.setAvailability(windows);
      setStatus('Profile and availability saved.');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'could not save profile');
    } finally {
      setBusy(false);
    }
  };

  const toggleDay = (day: number) =>
    setWeekdays((current) =>
      current.includes(day) ? current.filter((value) => value !== day) : [...current, day].sort(),
    );

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
        <Text style={shared.muted}>Times are in your profile timezone.</Text>
        <View style={[shared.row, { flexWrap: 'wrap' }]}>
          {WEEKDAYS.map((label, day) => (
            <Pressable
              key={label}
              accessibilityRole="checkbox"
              accessibilityState={{ checked: weekdays.includes(day) }}
              onPress={() => toggleDay(day)}
              style={[styles.day, weekdays.includes(day) && styles.dayActive]}
            >
              <Text style={weekdays.includes(day) ? styles.dayActiveLabel : styles.dayLabel}>{label}</Text>
            </Pressable>
          ))}
        </View>
        <View style={shared.row}>
          <View style={{ flex: 1 }}>
            <Field label="From" value={start} onChangeText={setStart} placeholder="17:00" />
          </View>
          <View style={{ flex: 1 }}>
            <Field label="To" value={end} onChangeText={setEnd} placeholder="21:00" />
          </View>
        </View>
        <Text style={shared.muted}>
          {weekdays.map((day) => WEEKDAYS[day]).join(', ') || 'No days'} · {clock(parseClock(start) ?? 0)}–
          {clock(parseClock(end) ?? 0)}
        </Text>
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
  dayActive: { borderColor: colors.primary, backgroundColor: '#fdf0f4' },
  dayLabel: { color: colors.muted, fontWeight: '600', fontSize: 13 },
  dayActiveLabel: { color: colors.primary, fontWeight: '700', fontSize: 13 },
});
