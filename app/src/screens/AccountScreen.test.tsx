import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import type { Profile } from '../api/types';
import { useAuth } from '../state/auth';
import AccountScreen from './AccountScreen';

jest.mock('../state/auth', () => ({ useAuth: jest.fn() }));

const mockUseAuth = useAuth as jest.Mock;

const baseUser: Profile = {
  id: 'u1',
  email: 'me@example.com',
  display_name: 'Dana',
  role: 'user',
  email_verified: true,
  dating_styles: ['hinge'],
  phases_strong: ['opening'],
  phases_working_on: ['flirting'],
  dating_preferences: 'Something serious',
};

type AuthStub = {
  user: Partial<Profile> | null;
  signOut: jest.Mock;
  refresh: jest.Mock;
  deleteAccount: jest.Mock;
  updateDatingProfile: jest.Mock;
};

/** Builds a useAuth stub around `user`; each call returns the same fns so rerenders don't retrigger effects. */
function authStub(user: Partial<Profile> | null = baseUser): AuthStub {
  return {
    user,
    signOut: jest.fn(),
    refresh: jest.fn().mockResolvedValue(undefined),
    deleteAccount: jest.fn(),
    updateDatingProfile: jest.fn(),
  };
}

function mount(stub: AuthStub) {
  mockUseAuth.mockImplementation(() => stub);
  const utils = render(<AccountScreen />);
  /** Swaps the user the hook returns and re-renders, like a context update would. */
  const setUser = (user: Partial<Profile> | null) => {
    stub.user = user;
    utils.rerender(<AccountScreen />);
  };
  return { ...utils, setUser };
}

const saveButton = () => screen.getByRole('button', { name: 'Save dating profile' });
/** The checkbox-role Chip whose label is `name`. */
const chip = (name: string) => screen.getByRole('checkbox', { name });
/** The "Delete account" button (the section header carries the same text). */
const deleteButton = () => screen.getByRole('button', { name: 'Delete account' });
/** The "Strength"/"Working on" chips for the phase row at `index` in DATING_PHASES order. */
const phaseChips = (index: number) => ({
  strength: screen.getAllByRole('checkbox', { name: 'Strength' })[index],
  working: screen.getAllByRole('checkbox', { name: 'Working on' })[index],
});

describe('AccountScreen dating profile', () => {
  it('renders the saved profile and keeps Save disabled until something changes', () => {
    mount(authStub());
    expect(screen.getByText('Dana')).toBeTruthy();
    expect(screen.getByText('me@example.com')).toBeTruthy();
    expect(screen.getByText('Member')).toBeTruthy();
    expect(screen.getByText('Email verified')).toBeTruthy();
    expect(chip('Hinge')).toBeChecked();
    expect(chip('Tinder')).not.toBeChecked();
    expect(saveButton()).toBeDisabled();
  });

  it('style chips toggle and enable Save; toggling back disables it again', () => {
    mount(authStub());
    fireEvent.press(chip('Tinder'));
    expect(chip('Tinder')).toBeChecked();
    expect(saveButton()).toBeEnabled();
    fireEvent.press(chip('Tinder'));
    expect(saveButton()).toBeDisabled();
  });

  it('setStanding never leaves a phase on both sides and toggles off when re-pressed', () => {
    mount(authStub());
    const opening = phaseChips(0); // saved as strong
    expect(opening.strength).toBeChecked();
    expect(opening.working).not.toBeChecked();

    fireEvent.press(opening.working);
    expect(phaseChips(0).working).toBeChecked();
    expect(phaseChips(0).strength).not.toBeChecked();

    fireEvent.press(phaseChips(0).working);
    expect(phaseChips(0).working).not.toBeChecked();
    expect(phaseChips(0).strength).not.toBeChecked();
  });

  it('saves trimmed preferences with the drafted lists and shows "Saved."', async () => {
    const stub = authStub();
    stub.updateDatingProfile.mockImplementation(async (input) => {
      const next = { ...baseUser, ...input };
      stub.user = next;
      return next;
    });
    const { rerender } = mount(stub);
    fireEvent.press(chip('Bumble'));
    fireEvent.press(phaseChips(1).strength); // first_messages
    fireEvent.changeText(screen.getByDisplayValue('Something serious'), '  Someone outdoorsy  ');
    fireEvent.press(saveButton());

    await waitFor(() =>
      expect(stub.updateDatingProfile).toHaveBeenCalledWith({
        dating_styles: ['hinge', 'bumble'],
        phases_strong: ['opening', 'first_messages'],
        phases_working_on: ['flirting'],
        dating_preferences: 'Someone outdoorsy',
      }),
    );
    rerender(<AccountScreen />);
    await screen.findByText('Saved.');
    expect(saveButton()).toBeDisabled();
  });

  it('shows the save failure copy and re-enables Save', async () => {
    const stub = authStub();
    stub.updateDatingProfile.mockRejectedValue(new Error('boom'));
    mount(stub);
    fireEvent.press(chip('Bumble'));
    fireEvent.press(saveButton());
    await screen.findByText('Could not save your dating profile. Please try again.');
    expect(saveButton()).toBeEnabled();
  });

  it('a refresh returning the same saved lists keeps unsaved draft edits', () => {
    const { setUser } = mount(authStub());
    fireEvent.press(chip('Bumble'));
    fireEvent.changeText(screen.getByDisplayValue('Something serious'), 'Draft text');

    setUser({ ...baseUser, dating_styles: ['hinge'] });

    expect(chip('Bumble')).toBeChecked();
    expect(screen.getByDisplayValue('Draft text')).toBeTruthy();
    expect(saveButton()).toBeEnabled();
  });

  it('changed saved lists sync into the drafts', () => {
    const { setUser } = mount(authStub());
    fireEvent.press(chip('Bumble'));

    setUser({ ...baseUser, dating_styles: ['tinder'], phases_working_on: [], dating_preferences: 'New server text' });

    expect(chip('Tinder')).toBeChecked();
    expect(chip('Bumble')).not.toBeChecked();
    expect(chip('Hinge')).not.toBeChecked();
    expect(phaseChips(3).working).not.toBeChecked(); // flirting
    expect(screen.getByDisplayValue('New server text')).toBeTruthy();
    expect(saveButton()).toBeDisabled();
  });
});

describe('AccountScreen bootstrap when dating_preferences is unknown', () => {
  const stale: Partial<Profile> = { ...baseUser, dating_preferences: undefined };

  it('locks the controls, calls refresh, and unlocks once the profile lands', async () => {
    const stub = authStub(stale);
    const { setUser } = mount(stub);

    expect(stub.refresh).toHaveBeenCalledTimes(1);
    expect(screen.getByText('Refreshing your profile before it can be edited…')).toBeTruthy();
    expect(chip('Tinder')).toBeDisabled();
    fireEvent.press(chip('Tinder'));
    expect(chip('Tinder')).not.toBeChecked();
    expect(saveButton()).toBeDisabled();

    setUser(baseUser);
    expect(screen.queryByText(/Refreshing your profile/)).toBeNull();
    expect(chip('Tinder')).toBeEnabled();
    fireEvent.press(chip('Tinder'));
    expect(saveButton()).toBeEnabled();
  });

  it('shows the retry path when the bootstrap refresh fails, and retries on demand', async () => {
    const stub = authStub(stale);
    stub.refresh.mockRejectedValueOnce(new Error('offline')).mockResolvedValue(undefined);
    mount(stub);

    await screen.findByText('Could not refresh your profile. Editing stays off until it loads.');
    expect(chip('Tinder')).toBeDisabled();

    fireEvent.press(screen.getByText('Try again'));
    expect(stub.refresh).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(screen.queryByText(/Could not refresh your profile/)).toBeNull());
    expect(screen.getByText('Refreshing your profile before it can be edited…')).toBeTruthy();
  });
});

describe('AccountScreen session + delete', () => {
  it('"Refresh profile" and "Sign out" call through', () => {
    const stub = authStub();
    mount(stub);
    fireEvent.press(screen.getByText('Refresh profile'));
    expect(stub.refresh).toHaveBeenCalledTimes(1);
    fireEvent.press(screen.getByText('Sign out'));
    expect(stub.signOut).toHaveBeenCalledTimes(1);
  });

  it('requires the typed "delete" phrase (case-insensitive) before deleting', async () => {
    const stub = authStub();
    stub.deleteAccount.mockResolvedValue(undefined);
    mount(stub);

    fireEvent.press(deleteButton());
    const confirm = screen.getByRole('button', { name: 'Permanently delete account' });
    expect(confirm).toBeDisabled();
    fireEvent.press(confirm);
    expect(stub.deleteAccount).not.toHaveBeenCalled();

    const input = screen.getByDisplayValue('');
    fireEvent.changeText(input, 'delet');
    expect(screen.getByRole('button', { name: 'Permanently delete account' })).toBeDisabled();
    fireEvent.changeText(input, ' DELETE ');
    expect(screen.getByRole('button', { name: 'Permanently delete account' })).toBeEnabled();

    fireEvent.press(screen.getByRole('button', { name: 'Permanently delete account' }));
    await waitFor(() => expect(stub.deleteAccount).toHaveBeenCalledTimes(1));
  });

  it('"Keep my account" closes the confirm and clears the phrase', () => {
    mount(authStub());
    fireEvent.press(deleteButton());
    fireEvent.changeText(screen.getByDisplayValue(''), 'delete');
    fireEvent.press(screen.getByRole('button', { name: 'Keep my account' }));
    expect(screen.queryByRole('button', { name: 'Permanently delete account' })).toBeNull();
    fireEvent.press(deleteButton());
    expect(screen.getByRole('button', { name: 'Permanently delete account' })).toBeDisabled();
  });

  it('delete failure shows the fixed copy and re-enables the flow', async () => {
    const stub = authStub();
    stub.deleteAccount.mockRejectedValue(new Error('server said no'));
    const warn = jest.spyOn(console, 'warn').mockImplementation(() => {});
    mount(stub);

    fireEvent.press(deleteButton());
    fireEvent.changeText(screen.getByDisplayValue(''), 'delete');
    fireEvent.press(screen.getByRole('button', { name: 'Permanently delete account' }));

    await screen.findByText('Could not delete your account. Please try again.');
    expect(screen.queryByText('server said no')).toBeNull();
    expect(screen.getByRole('button', { name: 'Permanently delete account' })).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Keep my account' })).toBeEnabled();
    warn.mockRestore();
  });

  it('delete success leaves the screen busy until the session removal unmounts it', async () => {
    const stub = authStub();
    let resolve!: () => void;
    stub.deleteAccount.mockReturnValue(new Promise<void>((r) => (resolve = r)));
    const { setUser, unmount } = mount(stub);

    fireEvent.press(deleteButton());
    fireEvent.changeText(screen.getByDisplayValue(''), 'delete');
    fireEvent.press(screen.getByRole('button', { name: 'Permanently delete account' }));
    expect(screen.getByRole('button', { name: 'Keep my account' })).toBeDisabled();

    await act(async () => {
      setUser(null);
      resolve();
    });
    expect(screen.queryByText('Could not delete your account. Please try again.')).toBeNull();
    unmount();
  });
});
