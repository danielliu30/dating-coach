import React, { useState } from 'react';
import { Text, View } from 'react-native';

import { Badge, Button, Field, Screen } from '../components/ui';
import { API_BASE_URL } from '../config';
import { useAuth } from '../state/auth';
import { colors, shared } from '../theme';

// Typed to arm deletion. Matched case-insensitively so the phrase is a
// deliberate act rather than a typing test.
const CONFIRM_PHRASE = 'delete';

export default function AccountScreen(): React.ReactElement {
  const { user, signOut, refresh, deleteAccount } = useAuth();
  const [confirming, setConfirming] = useState(false);
  const [phrase, setPhrase] = useState('');
  const [deleting, setDeleting] = useState(false);
  const [error, setError] = useState<string | null>(null);

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

  return (
    <Screen>
      <Text style={shared.title}>Account</Text>
      <View style={shared.card}>
        <Text style={shared.heading}>{user?.display_name}</Text>
        <Text style={shared.body}>{user?.email}</Text>
        <View style={shared.row}>
          <Badge text={user?.role ?? 'user'} tone={colors.primary} />
          <Badge
            text={user?.email_verified ? 'email verified' : 'email not verified'}
            tone={user?.email_verified ? colors.engaging : colors.neutral}
          />
        </View>
        <Text style={shared.muted}>API: {API_BASE_URL}</Text>
      </View>
      <Button label="Refresh profile" variant="secondary" onPress={() => void refresh()} />
      <Button label="Sign out" onPress={() => void signOut()} />

      <View style={shared.card}>
        <Text style={shared.heading}>Delete account</Text>
        <Text style={shared.muted}>
          Permanently removes your profile, sessions, chats and analyses, and signs you out
          everywhere. This cannot be undone.
        </Text>
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
