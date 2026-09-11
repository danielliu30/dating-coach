import { Ionicons } from '@expo/vector-icons';
import React, { useCallback, useEffect, useState } from 'react';
import { StyleSheet, Text, View } from 'react-native';

import { ApiError } from '../api/client';
import {
  DATING_PHASES,
  DATING_STYLES,
  MAX_DATING_PREFERENCES_LEN,
  type DatingPhase,
  type DatingStyle,
} from '../api/types';
import { Avatar, Divider, GradientCard, SectionHeader, StatusPill } from '../components/kit';
import { Badge, Button, Chip, Field, Screen } from '../components/ui';
import { useAuth } from '../state/auth';
import { colors, fonts, radii, shared, type } from '../theme';

// Typed to arm deletion. Matched case-insensitively so the phrase is a
// deliberate act rather than a typing test.
const CONFIRM_PHRASE = 'delete';

const STYLE_LABELS: Record<DatingStyle, string> = {
  in_person: 'In person',
  tinder: 'Tinder',
  hinge: 'Hinge',
  bumble: 'Bumble',
  coffee_meets_bagel: 'Coffee Meets Bagel',
  match: 'Match',
  okcupid: 'OkCupid',
  feeld: 'Feeld',
  speed_dating: 'Speed dating',
  friends_intro: 'Through friends',
};

const PHASE_LABELS: Record<DatingPhase, string> = {
  opening: 'Opening line',
  first_messages: 'First messages',
  building_rapport: 'Building rapport',
  flirting: 'Flirting',
  asking_out: 'Asking them out',
  first_date: 'First date',
  follow_up: 'Following up after a date',
  defining_relationship: 'Defining the relationship',
};

// Where a phase sits for the user: a strength, something being worked on, or
// neither. The backend refuses a phase on both sides, so the UI never offers it.
type PhaseStanding = 'strong' | 'working_on' | null;

/** Toggles `value` in `list`, keeping the vocabulary's order. */
function toggle<T extends string>(list: readonly T[], value: T, vocabulary: readonly T[]): T[] {
  const next = new Set(list);
  if (next.has(value)) next.delete(value);
  else next.add(value);
  return vocabulary.filter((v) => next.has(v));
}

/** Order-insensitive equality of two selections. */
function sameSet(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v) => b.includes(v));
}

export default function AccountScreen(): React.ReactElement {
  const { user, signOut, refresh, deleteAccount, updateDatingProfile } = useAuth();
  const [confirming, setConfirming] = useState(false);
  const [phrase, setPhrase] = useState('');
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Sessions persisted before these fields existed have no lists; treat them
  // as empty rather than crashing on `.includes`.
  const savedStyles = user?.dating_styles ?? [];
  const savedStrong = user?.phases_strong ?? [];
  const savedWorking = user?.phases_working_on ?? [];
  const savedPreferences = user?.dating_preferences ?? '';
  // A snapshot from before dating_preferences existed does not say what the
  // server holds, and the save below PATCHes every field, so saving from it
  // would blank the server's value. Re-fetch the profile first and keep Save
  // off until it lands.
  const preferencesUnknown = user !== null && user.dating_preferences === undefined;
  const [bootstrapFailed, setBootstrapFailed] = useState(false);
  const bootstrap = useCallback(() => {
    setBootstrapFailed(false);
    refresh().catch(() => setBootstrapFailed(true));
  }, [refresh]);
  useEffect(() => {
    if (preferencesUnknown) bootstrap();
  }, [preferencesUnknown, bootstrap]);

  const [styles, setStyles] = useState<DatingStyle[]>(savedStyles);
  const [strong, setStrong] = useState<DatingPhase[]>(savedStrong);
  const [working, setWorking] = useState<DatingPhase[]>(savedWorking);
  const [preferences, setPreferences] = useState(savedPreferences);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [savedAt, setSavedAt] = useState<number | null>(null);
  // Every dating-profile control is frozen until the bootstrap settles, so the
  // refreshed profile cannot land on top of edits made in the meantime.
  const locked = saving || preferencesUnknown;

  // Drafts follow the saved profile only when its lists actually change (a
  // save, or an edit made elsewhere); a refresh that returns the same lists
  // leaves unsaved choices alone.
  const savedKey = JSON.stringify([savedStyles, savedStrong, savedWorking, savedPreferences]);
  useEffect(() => {
    const [nextStyles, nextStrong, nextWorking, nextPreferences] = JSON.parse(savedKey) as [
      DatingStyle[],
      DatingPhase[],
      DatingPhase[],
      string,
    ];
    setStyles(nextStyles);
    setStrong(nextStrong);
    setWorking(nextWorking);
    setPreferences(nextPreferences);
  }, [savedKey]);

  const dirty =
    !sameSet(styles, savedStyles) ||
    !sameSet(strong, savedStrong) ||
    !sameSet(working, savedWorking) ||
    preferences.trim() !== savedPreferences;

  const standingOf = (phase: DatingPhase): PhaseStanding =>
    strong.includes(phase) ? 'strong' : working.includes(phase) ? 'working_on' : null;

  // Selecting a side clears the other so a phase is never on both.
  const setStanding = (phase: DatingPhase, standing: PhaseStanding) => {
    setSavedAt(null);
    setStrong((prev) => {
      const without = prev.filter((p) => p !== phase);
      return standing === 'strong' ? DATING_PHASES.filter((p) => p === phase || without.includes(p)) : without;
    });
    setWorking((prev) => {
      const without = prev.filter((p) => p !== phase);
      return standing === 'working_on' ? DATING_PHASES.filter((p) => p === phase || without.includes(p)) : without;
    });
  };

  const save = async () => {
    setSaving(true);
    setSaveError(null);
    try {
      await updateDatingProfile({
        dating_styles: styles,
        phases_strong: strong,
        phases_working_on: working,
        dating_preferences: preferences.trim(),
      });
      setSavedAt(Date.now());
    } catch (err) {
      setSaveError(err instanceof ApiError ? err.message : 'Could not save your dating profile. Please try again.');
    } finally {
      setSaving(false);
    }
  };

  const closeConfirm = () => {
    setConfirming(false);
    setPhrase('');
    setError(null);
  };

  const confirmDelete = async () => {
    setDeleting(true);
    setError(null);
    try {
      // Unmounts this screen on success: losing the session sends the app back
      // to the signed-out stack, so nothing after this runs.
      await deleteAccount();
    } catch (err) {
      // Only reached when the deletion was never accepted, and the reason is
      // always something the account's owner can do nothing about, so it stays
      // in the log rather than on screen.
      console.warn('delete account', err);
      setError('Could not delete your account. Please try again.');
      setDeleting(false);
    }
  };

  const name = user?.display_name ?? 'You';
  const stylesCount = styles.length;
  const phasesCount = strong.length + working.length;

  return (
    <Screen>
      <GradientCard gradient="dusk">
        <View style={local.profileRow}>
          <Avatar name={name} size={68} />
          <View style={{ flex: 1, gap: 4 }}>
            <Text style={[type.eyebrow, { color: 'rgba(255,255,255,0.85)' }]}>Dating Humane account</Text>
            <Text style={[type.title, { color: colors.primaryText }]}>{name}</Text>
            <Text style={[type.caption, { color: 'rgba(255,255,255,0.85)' }]}>{user?.email}</Text>
          </View>
        </View>
        <View style={local.pillRow}>
          <StatusPill text={user?.role === 'coach' ? 'Coach' : 'Member'} tone={colors.primaryText} onDark />
          <StatusPill
            text={user?.email_verified ? 'Email verified' : 'Email not verified'}
            tone={colors.primaryText}
            onDark
          />
        </View>
        <View style={local.factRow}>
          <View style={local.fact}>
            <Text style={local.factValue}>{stylesCount}</Text>
            <Text style={local.factLabel}>ways you meet people</Text>
          </View>
          <View style={local.fact}>
            <Text style={local.factValue}>{phasesCount}</Text>
            <Text style={local.factLabel}>phases marked</Text>
          </View>
          <View style={local.fact}>
            <Text style={local.factValue}>{preferences.trim() ? 'Set' : '—'}</Text>
            <Text style={local.factLabel}>looking for</Text>
          </View>
        </View>
      </GradientCard>

      <View style={shared.card}>
        <SectionHeader
          title="How you date"
          caption="Pick everywhere you meet people. Your coach tailors advice to these."
          icon="compass-outline"
          hue="sage"
        />
        <View style={local.chips}>
          {DATING_STYLES.map((style) => (
            <Chip
              key={style}
              label={STYLE_LABELS[style]}
              selected={styles.includes(style)}
              disabled={locked}
              onPress={() => {
                setSavedAt(null);
                setStyles((prev) => toggle(prev, style, DATING_STYLES));
              }}
            />
          ))}
        </View>
      </View>

      <View style={shared.card}>
        <SectionHeader
          title="Phases of dating"
          caption="For each stage, mark whether it is a strength or something you are working on. Leave it blank if neither applies."
          icon="footsteps-outline"
          hue="sun"
        />
        <View style={local.legend}>
          <Badge text="strength" tone={colors.engaging} />
          <Badge text="working on" tone={colors.neutral} />
        </View>
        {DATING_PHASES.map((phase) => {
          const standing = standingOf(phase);
          return (
            <View key={phase} style={local.phaseRow}>
              <View style={shared.row}>
                <View
                  style={[
                    local.phaseDot,
                    standing === 'strong' && { backgroundColor: colors.engaging },
                    standing === 'working_on' && { backgroundColor: colors.neutral },
                  ]}
                />
                <Text style={local.phaseLabel}>{PHASE_LABELS[phase]}</Text>
              </View>
              <View style={local.phaseChoices}>
                <Chip
                  label="Strength"
                  tone={colors.engaging}
                  selected={standing === 'strong'}
                  disabled={locked}
                  onPress={() => setStanding(phase, standing === 'strong' ? null : 'strong')}
                />
                <Chip
                  label="Working on"
                  tone={colors.neutral}
                  selected={standing === 'working_on'}
                  disabled={locked}
                  onPress={() => setStanding(phase, standing === 'working_on' ? null : 'working_on')}
                />
              </View>
            </View>
          );
        })}
      </View>

      <View style={shared.card}>
        <SectionHeader
          title="What you are looking for"
          caption="In your own words. Your analyses use this to judge whether your messages and photos surface it."
          icon="heart-outline"
          hue="rose"
        />
        <Field
          label={`${preferences.length}/${MAX_DATING_PREFERENCES_LEN}`}
          value={preferences}
          onChangeText={(text) => {
            setSavedAt(null);
            setPreferences(text);
          }}
          placeholder="e.g. Something serious with someone who likes the outdoors and can laugh at themselves"
          multiline
          maxLength={MAX_DATING_PREFERENCES_LEN}
          editable={!locked}
        />
        {saveError ? <Text style={shared.error}>{saveError}</Text> : null}
        {savedAt && !dirty ? (
          <View style={shared.row}>
            <Ionicons name="checkmark-circle" size={16} color={colors.engaging} />
            <Text style={[type.caption, { color: colors.engaging }]}>Saved.</Text>
          </View>
        ) : null}
        {preferencesUnknown && !bootstrapFailed ? (
          <Text style={shared.muted}>Refreshing your profile before it can be edited…</Text>
        ) : null}
        {preferencesUnknown && bootstrapFailed ? (
          <>
            <Text style={shared.error}>Could not refresh your profile. Editing stays off until it loads.</Text>
            <Button label="Try again" variant="secondary" onPress={bootstrap} />
          </>
        ) : null}
        <Button
          label="Save dating profile"
          icon="save-outline"
          disabled={!dirty || locked}
          loading={saving}
          onPress={() => void save()}
        />
      </View>

      <View style={shared.card}>
        <SectionHeader title="Session" caption="Signed in on this device." icon="shield-checkmark-outline" hue="sky" />
        <View style={local.actions}>
          <View style={{ flex: 1 }}>
            <Button label="Refresh profile" icon="refresh-outline" variant="secondary" onPress={() => void refresh()} />
          </View>
          <View style={{ flex: 1 }}>
            <Button label="Sign out" icon="log-out-outline" onPress={() => void signOut()} />
          </View>
        </View>
      </View>

      <View style={[shared.card, local.danger]}>
        <SectionHeader
          title="Delete account"
          caption="Permanently removes your profile, sessions, chats and analyses, and signs you out everywhere. This cannot be undone."
          icon="trash-outline"
          hue="rose"
        />
        <Divider />
        {confirming ? (
          <View style={{ gap: 12 }}>
            <Field
              label={`Type "${CONFIRM_PHRASE}" to confirm`}
              value={phrase}
              onChangeText={setPhrase}
              autoCapitalize="none"
              autoCorrect={false}
              editable={!deleting}
            />
            {error ? <Text style={shared.error}>{error}</Text> : null}
            <Button
              label="Permanently delete account"
              disabled={phrase.trim().toLowerCase() !== CONFIRM_PHRASE}
              loading={deleting}
              onPress={() => void confirmDelete()}
            />
            <Button label="Keep my account" variant="secondary" disabled={deleting} onPress={closeConfirm} />
          </View>
        ) : (
          <Button label="Delete account" variant="secondary" onPress={() => setConfirming(true)} />
        )}
      </View>
    </Screen>
  );
}

const local = StyleSheet.create({
  profileRow: { flexDirection: 'row', alignItems: 'center', gap: 16 },
  pillRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 8 },
  factRow: { flexDirection: 'row', gap: 8 },
  fact: {
    flex: 1,
    padding: 10,
    borderRadius: radii.md,
    backgroundColor: 'rgba(255,255,255,0.16)',
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.22)',
  },
  factValue: { fontFamily: fonts.sansBlack, fontSize: 18, color: colors.primaryText, letterSpacing: -0.3 },
  factLabel: { fontFamily: fonts.sans, fontSize: 11, color: 'rgba(255,255,255,0.8)' },
  chips: { flexDirection: 'row', flexWrap: 'wrap', gap: 8 },
  legend: { flexDirection: 'row', gap: 8 },
  phaseRow: { gap: 8, paddingVertical: 10, borderTopWidth: 1, borderTopColor: colors.border },
  phaseDot: { width: 8, height: 8, borderRadius: 4, backgroundColor: colors.border },
  phaseLabel: { fontFamily: fonts.sansSemi, fontSize: 15, color: colors.text },
  phaseChoices: { flexDirection: 'row', gap: 8 },
  actions: { flexDirection: 'row', gap: 8 },
  danger: { borderColor: `${colors.flat}33`, borderWidth: 1 },
});
