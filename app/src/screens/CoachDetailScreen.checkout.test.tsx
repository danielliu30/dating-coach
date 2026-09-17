import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';
import { Linking } from 'react-native';

import { api } from '../api/client';
import type { Coach, CoachingSession, Slot } from '../api/types';
import CoachDetailScreen from './CoachDetailScreen';

jest.mock('@react-navigation/native', () => {
  const React = require('react');
  return {
    useFocusEffect: (effect: () => void) => React.useEffect(effect, [effect]),
    useNavigation: () => ({ navigate: jest.fn() }),
  };
});

jest.mock('../api/client', () => ({
  api: {
    getCoach: jest.fn(),
    coachAvailability: jest.fn(),
    openSlots: jest.fn(),
    listCoachReviews: jest.fn(),
    coachReviewSummary: jest.fn(),
    mySessions: jest.fn(),
    bookSession: jest.fn(),
  },
}));

const mocked = api as jest.Mocked<typeof api>;

const coach: Coach = {
  id: 'c1',
  display_name: 'Casey Coach',
  headline: '',
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
  approval_status: 'approved',
};

const slotsFor = (duration: number): Slot[] => [
  { start: '2030-01-07T18:00:00Z', duration_minutes: duration },
  { start: '2030-01-07T19:00:00Z', duration_minutes: duration },
];

/** Renders the coach detail screen for `coach` with `slotsFor` answering every open-slots query. */
function mount() {
  mocked.getCoach.mockResolvedValue(coach);
  mocked.coachAvailability.mockResolvedValue([]);
  mocked.openSlots.mockImplementation(async (_id, duration = 45) => slotsFor(duration));
  mocked.listCoachReviews.mockResolvedValue([]);
  mocked.coachReviewSummary.mockResolvedValue({ model_version: 'heuristic', recommended: 0, total: 0, summary: '', strengths: [] });
  mocked.mySessions.mockResolvedValue([]);
  const ScreenAny = CoachDetailScreen as unknown as React.ComponentType<{ route: unknown; navigation: unknown }>;
  return render(<ScreenAny route={{ params: { coachID: 'c1', coachName: 'Casey Coach' } }} navigation={{}} />);
}

const booking = (checkout_url: string): CoachingSession => ({
  id: 's1',
  user_id: 'u1',
  coach_id: 'c1',
  counterpart_name: 'Casey Coach',
  scheduled_time: '2030-01-07T18:00:00Z',
  duration_minutes: 45,
  status: 'pending_payment',
  topic: '',
  payment_status: 'pending',
  amount_cents: 9000,
  currency: 'usd',
  checkout_url,
});

/** Picks the first open slot and presses "Book session". */
async function bookFirstSlot() {
  await waitFor(() => expect(screen.getAllByRole('button', { name: /Jan 7/ }).length).toBeGreaterThan(0));
  fireEvent.press(screen.getAllByRole('button', { name: /Jan 7/ })[0]);
  fireEvent.press(screen.getByRole('button', { name: 'Book session' }));
}

describe('CoachDetailScreen checkout URL', () => {
  beforeEach(() => {
    jest.spyOn(Linking, 'openURL').mockResolvedValue(true);
  });

  it('clears a previous checkout link when the next booking attempt fails', async () => {
    mocked.bookSession.mockResolvedValueOnce(booking('https://pay.example/first'));
    mount();

    await bookFirstSlot();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Continue to payment' })).toBeTruthy());

    mocked.bookSession.mockRejectedValueOnce(new Error('slot no longer available'));
    await bookFirstSlot();
    await waitFor(() => expect(screen.getByText('slot no longer available')).toBeTruthy());
    expect(screen.queryByRole('button', { name: 'Continue to payment' })).toBeNull();
  });

  it('shows the link for the newest held session only', async () => {
    mocked.bookSession.mockResolvedValueOnce(booking('https://pay.example/first'));
    mount();
    await bookFirstSlot();
    await waitFor(() => expect(Linking.openURL).toHaveBeenCalledWith('https://pay.example/first'));

    mocked.bookSession.mockResolvedValueOnce(booking('https://pay.example/second'));
    await bookFirstSlot();
    await waitFor(() => expect(Linking.openURL).toHaveBeenCalledWith('https://pay.example/second'));

    fireEvent.press(screen.getByRole('button', { name: 'Continue to payment' }));
    await waitFor(() => expect(Linking.openURL).toHaveBeenCalledTimes(3));
    expect(Linking.openURL).toHaveBeenLastCalledWith('https://pay.example/second');
  });
});
