import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { ApiError, api } from '../api/client';
import type { Coach } from '../api/types';
import CoachProfileScreen from './CoachProfileScreen';

// Drive focus by hand so a test can "return to the tab" without a navigator.
const mockFocus: { effect: (() => void | (() => void)) | null } = { effect: null };
jest.mock('@react-navigation/native', () => {
  const { useEffect } = require('react');
  return {
    useFocusEffect: (effect: () => void | (() => void)) => {
      mockFocus.effect = effect;
      useEffect(effect, [effect]);
    },
  };
});

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
      mockFocus.effect?.();
    });
    await waitFor(() => expect(screen.queryByText(REVIEW)).toBeNull());
    expect(screen.getByDisplayValue('Edited headline')).toBeTruthy();
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
