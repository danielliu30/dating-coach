import { act, fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';

import { ApiError, api } from '../api/client';
import CoachProfileScreen from './CoachProfileScreen';
import { coachFixture } from './coachTestUtils.testutil';

jest.mock('../api/client', () => {
  const actual = jest.requireActual<typeof import('../api/client')>('../api/client');
  return {
    ApiError: actual.ApiError,
    api: {
      getCoach: jest.fn(),
      coachAvailability: jest.fn(),
      upsertCoachProfile: jest.fn(),
      setAvailability: jest.fn(),
    },
  };
});

jest.mock('../state/auth', () => ({ useAuth: () => ({ user: { id: 'coach-1', display_name: 'Casey Coach', role: 'coach' } }) }));

const mocked = api as jest.Mocked<typeof api>;
const MON_WINDOW = { weekday: 1, start_minute: 9 * 60, end_minute: 12 * 60 };

const renderLoaded = async () => {
  render(<CoachProfileScreen />);
  await screen.findByText('Save profile');
};

const save = () =>
  act(async () => {
    fireEvent.press(screen.getByText('Save profile'));
  });

beforeEach(() => {
  mocked.getCoach.mockResolvedValue(
    coachFixture({ id: 'coach-1', headline: 'Saved headline', bio: 'Saved bio', hourly_rate_cents: 15000, years_experience: 7 }),
  );
  mocked.coachAvailability.mockResolvedValue([MON_WINDOW]);
  mocked.upsertCoachProfile.mockResolvedValue({} as never);
  mocked.setAvailability.mockResolvedValue({ availability: [MON_WINDOW] });
});

describe('CoachProfileScreen load', () => {
  it('seeds the editor from the saved profile and availability', async () => {
    await renderLoaded();
    expect(mocked.getCoach).toHaveBeenCalledWith('coach-1');
    expect(mocked.coachAvailability).toHaveBeenCalledWith('coach-1');
    expect(screen.getByDisplayValue('Saved headline')).toBeTruthy();
    expect(screen.getByDisplayValue('150')).toBeTruthy();
    expect(screen.getByDisplayValue('7')).toBeTruthy();
    expect(screen.getByDisplayValue('09:00')).toBeTruthy();
    expect(screen.getByDisplayValue('12:00')).toBeTruthy();
    expect(screen.queryByDisplayValue('17:00')).toBeNull();
  });

  it('treats a 404 profile as not yet published: defaults stay, saving still allowed', async () => {
    mocked.getCoach.mockRejectedValue(new ApiError(404, 'not found'));
    mocked.coachAvailability.mockResolvedValue([]);
    await renderLoaded();
    expect(screen.queryByText(/Could not load/)).toBeNull();
    expect(screen.getAllByDisplayValue('17:00')).toHaveLength(5);
    expect(screen.getByDisplayValue('120')).toBeTruthy();
    await save();
    expect(mocked.upsertCoachProfile).toHaveBeenCalledTimes(1);
  });

  it('a non-404 profile failure shows the load error and blocks saving', async () => {
    mocked.getCoach.mockRejectedValue(new ApiError(500, 'boom'));
    await renderLoaded();
    expect(screen.getByText('Could not load profile (boom). Reload before saving.')).toBeTruthy();
    await save();
    expect(mocked.upsertCoachProfile).not.toHaveBeenCalled();
    expect(screen.getByText(/saving now would overwrite it/)).toBeTruthy();
  });

  it('an availability failure reports itself but does not block saving the profile', async () => {
    mocked.coachAvailability.mockRejectedValue(new Error('cal down'));
    await renderLoaded();
    expect(screen.getByText('Could not load availability (cal down). Reload before saving.')).toBeTruthy();
    await save();
    expect(mocked.upsertCoachProfile).toHaveBeenCalledTimes(1);
  });
});

describe('CoachProfileScreen validation', () => {
  it('rejects a malformed time and names the weekday without calling the API', async () => {
    await renderLoaded();
    fireEvent.changeText(screen.getByDisplayValue('09:00'), '9am');
    await save();
    expect(screen.getByText('Mon times must be HH:MM with end after start.')).toBeTruthy();
    expect(mocked.upsertCoachProfile).not.toHaveBeenCalled();
    expect(mocked.setAvailability).not.toHaveBeenCalled();
  });

  it('rejects end <= start', async () => {
    await renderLoaded();
    fireEvent.changeText(screen.getByDisplayValue('12:00'), '09:00');
    await save();
    expect(screen.getByText('Mon times must be HH:MM with end after start.')).toBeTruthy();
    expect(mocked.upsertCoachProfile).not.toHaveBeenCalled();
  });
});

describe('CoachProfileScreen save', () => {
  it('saves the trimmed profile, then availability in minutes, and shows success', async () => {
    await renderLoaded();
    fireEvent.changeText(screen.getByDisplayValue('Saved headline'), '  New headline ');
    fireEvent.changeText(screen.getByPlaceholderText('openers, profile review, texting'), ' openers, , texting ');
    fireEvent.changeText(screen.getByDisplayValue('150'), '99.5');
    fireEvent.changeText(screen.getByDisplayValue('America/New_York'), '  ');
    fireEvent.press(screen.getByRole('switch'));
    await save();

    expect(mocked.upsertCoachProfile).toHaveBeenCalledWith({
      headline: 'New headline',
      bio: 'Saved bio',
      specialties: ['openers', 'texting'],
      hourly_rate_cents: 9950,
      timezone: 'UTC',
      years_experience: 7,
      accepting_clients: false,
    });
    expect(mocked.setAvailability).toHaveBeenCalledWith([MON_WINDOW]);
    expect(screen.getByText('Profile and availability saved.')).toBeTruthy();
    const order = [mocked.upsertCoachProfile.mock.invocationCallOrder[0], mocked.setAvailability.mock.invocationCallOrder[0]];
    expect(order[0]).toBeLessThan(order[1]);
  });

  it('a profile failure stops before availability and shows the error', async () => {
    mocked.upsertCoachProfile.mockRejectedValue(new Error('profile rejected'));
    await renderLoaded();
    await save();
    expect(screen.getByText('profile rejected')).toBeTruthy();
    expect(mocked.setAvailability).not.toHaveBeenCalled();
    expect(screen.queryByText(/saved/)).toBeNull();
  });

  it('an availability failure after a saved profile reports the partial save', async () => {
    mocked.setAvailability.mockRejectedValue(new Error('cal rejected'));
    await renderLoaded();
    await save();
    expect(
      screen.getByText('Profile saved, but availability did not save (cal rejected). Save again to retry.'),
    ).toBeTruthy();
    expect(screen.queryByText('Profile and availability saved.')).toBeNull();
  });

  it('replaces the drafts with the server-returned availability', async () => {
    mocked.setAvailability.mockResolvedValue({ availability: [{ weekday: 3, start_minute: 8 * 60, end_minute: 10 * 60 }] });
    await renderLoaded();
    await save();
    expect(screen.getByDisplayValue('08:00')).toBeTruthy();
    expect(screen.queryByDisplayValue('09:00')).toBeNull();
  });
});

describe('CoachProfileScreen editing', () => {
  it('toggles accepting clients and updates the preview pill', async () => {
    await renderLoaded();
    expect(screen.getByText('Accepting clients')).toBeTruthy();
    fireEvent.press(screen.getByRole('switch'));
    expect(screen.getByText('Not accepting')).toBeTruthy();
    expect(screen.getByText('Not accepting new clients')).toBeTruthy();
  });

  it('adds a default window, changes its weekday, and removes it again', async () => {
    await renderLoaded();
    fireEvent.press(screen.getByText('Add window'));
    expect(screen.getAllByText('Remove window')).toHaveLength(2);
    expect(screen.getByDisplayValue('17:00')).toBeTruthy();

    const satRadios = screen.getAllByRole('radio', { name: 'Sat' });
    fireEvent.press(satRadios[1]);
    expect(satRadios[1]).toBeChecked();
    expect(screen.getAllByRole('radio', { name: 'Mon' })[1]).not.toBeChecked();

    fireEvent.press(screen.getAllByText('Remove window')[1]);
    expect(screen.getAllByText('Remove window')).toHaveLength(1);
    expect(screen.queryByDisplayValue('17:00')).toBeNull();
  });

  it('removing every window shows the no-availability hint and saves an empty list', async () => {
    mocked.setAvailability.mockResolvedValue({ availability: [] });
    await renderLoaded();
    fireEvent.press(screen.getByText('Remove window'));
    expect(screen.getByText(/No availability published/)).toBeTruthy();
    await save();
    expect(mocked.setAvailability).toHaveBeenCalledWith([]);
  });
});
