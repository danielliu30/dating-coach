import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';
import { TextInput } from 'react-native';

import { ApiError, api } from '../api/client';
import type { Coach } from '../api/types';
import CoachProfileScreen from './CoachProfileScreen';

// Drive focus by hand so a test can blur and "return to the tab" without a
// navigator; blur() runs the effect's cleanup exactly like the real hook.
type FocusEffect = () => void | (() => void);
const mockFocus = {
  effect: null as FocusEffect | null,
  cleanup: undefined as void | (() => void),
  focus() {
    this.cleanup = this.effect?.();
  },
  blur() {
    this.cleanup?.();
    this.cleanup = undefined;
  },
};
jest.mock('@react-navigation/native', () => {
  const { useEffect } = require('react');
  return {
    useFocusEffect: (effect: FocusEffect) => {
      mockFocus.effect = effect;
      useEffect(() => {
        mockFocus.focus();
        return () => mockFocus.blur();
      }, [effect]);
    },
  };
});

/** A getCoach response the test resolves by hand, to order overlapping reads. */
const deferred = () => {
  let resolve!: (value: Coach) => void;
  const promise = new Promise<Coach>((r) => {
    resolve = r;
  });
  return { promise, resolve };
};

jest.mock('../api/client', () => {
  const actual = jest.requireActual('../api/client');
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

jest.mock('../state/auth', () => ({
  useAuth: () => ({ user: { id: 'c1', display_name: 'Casey', role: 'coach' } }),
}));

const mocked = api as jest.Mocked<typeof api>;

const coach = (approval_status: Coach['approval_status']): Coach => ({
  id: 'c1',
  display_name: 'Casey',
  headline: 'Openers that land',
  bio: '',
  specialties: [],
  phases: [],
  hourly_rate_cents: 12000,
  timezone: 'UTC',
  years_experience: 3,
  accepting_clients: true,
  avg_rating: 0,
  review_count: 0,
  recommend_count: 0,
  approval_status,
});

const REVIEW = 'Your profile is under review';
const REJECTED = 'Your profile was not approved';

beforeEach(() => {
  mocked.coachAvailability.mockResolvedValue([]);
  mocked.setAvailability.mockResolvedValue({ availability: [] });
});

describe('CoachProfileScreen approval banner', () => {
  it('preserves unchanged legacy text when saving profile and availability edits', async () => {
    const legacy = {
      ...coach('pending'),
      headline: ` ${'a'.repeat(201)} `,
      bio: ` ${'b'.repeat(5001)} `,
      specialties: Array.from({ length: 21 }, () => 's'.repeat(101)),
    };
    mocked.getCoach.mockResolvedValue(legacy);
    mocked.upsertCoachProfile.mockClear();
    mocked.setAvailability.mockClear();
    mocked.upsertCoachProfile.mockResolvedValue(legacy);
    render(<CoachProfileScreen />);
    await screen.findByDisplayValue(legacy.headline);
    fireEvent.changeText(screen.getByDisplayValue('120'), '150');
    fireEvent.press(screen.getByRole('button', { name: 'Save profile' }));
    await waitFor(() => expect(mocked.setAvailability).toHaveBeenCalled());
    expect(mocked.upsertCoachProfile).toHaveBeenCalledWith(expect.objectContaining({
      headline: legacy.headline, bio: legacy.bio, specialties: legacy.specialties, hourly_rate_cents: 15000,
    }));
  });

  it('bounds profile inputs to the server limits', async () => {
    mocked.getCoach.mockResolvedValue(coach('pending'));
    render(<CoachProfileScreen />);
    await screen.findByDisplayValue('Openers that land');
    const inputs = screen.UNSAFE_getAllByType(TextInput);
    expect(inputs.slice(0, 3).map((input) => input.props.maxLength)).toEqual([200, 5000, 2038]);
    expect(screen.getByPlaceholderText('America/New_York').props.maxLength).toBe(100);
  });

  it.each(['a'.repeat(101), Array.from({ length: 21 }, () => 'skill').join(',')])(
    'rejects specialties outside the per-item or count limit', async (value) => {
      mocked.getCoach.mockResolvedValue(coach('pending'));
      mocked.upsertCoachProfile.mockClear();
      render(<CoachProfileScreen />);
      await screen.findByDisplayValue('Openers that land');
      fireEvent.changeText(screen.getByPlaceholderText('openers, profile review, texting'), value);
      fireEvent.press(screen.getByRole('button', { name: 'Save profile' }));
      expect(await screen.findByText('Use at most 20 specialties, each at most 100 characters.')).toBeTruthy();
      expect(mocked.upsertCoachProfile).not.toHaveBeenCalled();
    },
  );

  it('shows the under-review banner for a pending coach and keeps the editor usable', async () => {
    mocked.getCoach.mockResolvedValue(coach('pending'));
    render(<CoachProfileScreen />);
    await screen.findByText(REVIEW);
    expect(screen.getByRole('alert')).toBeTruthy();
    expect(screen.getByDisplayValue('Openers that land')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Save profile' })).toBeTruthy();
  });

  it('shows the rejected banner and no banner at all once approved', async () => {
    mocked.getCoach.mockResolvedValue(coach('rejected'));
    render(<CoachProfileScreen />);
    await screen.findByText(REJECTED);
    screen.unmount();

    mocked.getCoach.mockResolvedValue(coach('approved'));
    render(<CoachProfileScreen />);
    await screen.findByDisplayValue('Openers that land');
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('re-reads only the approval status when the tab regains focus, keeping form edits', async () => {
    mocked.getCoach.mockResolvedValue(coach('pending'));
    render(<CoachProfileScreen />);
    await screen.findByText(REVIEW);
    fireEvent.changeText(screen.getByDisplayValue('Openers that land'), 'Edited headline');

    mocked.getCoach.mockResolvedValue(coach('approved'));
    await act(async () => {
      mockFocus.blur();
      mockFocus.focus();
    });
    await waitFor(() => expect(screen.queryByText(REVIEW)).toBeNull());
    expect(screen.getByDisplayValue('Edited headline')).toBeTruthy();
  });

  it('ignores a slow initial load that lands after a newer focus refresh', async () => {
    const initial = deferred();
    mocked.getCoach.mockReturnValueOnce(initial.promise);
    render(<CoachProfileScreen />);

    mocked.getCoach.mockResolvedValue(coach('approved'));
    await act(async () => {
      mockFocus.blur();
      mockFocus.focus();
    });
    await act(async () => {
      initial.resolve(coach('pending'));
    });
    await screen.findByDisplayValue('Openers that land');
    expect(screen.queryByText(REVIEW)).toBeNull();
  });

  it('keeps the banner from a successful initial load when the first focus refresh fails', async () => {
    // Mount load and first focus read start together; the newer (focus) one fails.
    mocked.getCoach.mockResolvedValueOnce(coach('pending')).mockRejectedValueOnce(new ApiError(503, 'unavailable'));
    render(<CoachProfileScreen />);
    await screen.findByText(REVIEW);
    await screen.findByText(/Could not refresh your review status/);
  });

  it('says so when the focus refresh fails, keeping the last known status', async () => {
    mocked.getCoach.mockResolvedValue(coach('pending'));
    render(<CoachProfileScreen />);
    await screen.findByText(REVIEW);

    mocked.getCoach.mockRejectedValue(new ApiError(503, 'unavailable'));
    await act(async () => {
      mockFocus.blur();
      mockFocus.focus();
    });
    await screen.findByText(/Could not refresh your review status/);
    expect(screen.getByText(REVIEW)).toBeTruthy();

    mocked.getCoach.mockResolvedValue(coach('approved'));
    await act(async () => {
      mockFocus.blur();
      mockFocus.focus();
    });
    await waitFor(() => expect(screen.queryByText(/Could not refresh/)).toBeNull());
    expect(screen.queryByText(REVIEW)).toBeNull();
  });

  it('shows the banner after a first-time save, when there was no profile to load', async () => {
    mocked.getCoach.mockRejectedValue(new ApiError(404, 'not found'));
    mocked.upsertCoachProfile.mockResolvedValue(coach('pending'));
    render(<CoachProfileScreen />);
    const save = await screen.findByRole('button', { name: 'Save profile' });
    expect(screen.queryByText(REVIEW)).toBeNull();

    fireEvent.press(save);
    await waitFor(() => expect(screen.getByText(REVIEW)).toBeTruthy());
    expect(mocked.upsertCoachProfile).toHaveBeenCalledTimes(1);
  });
});
