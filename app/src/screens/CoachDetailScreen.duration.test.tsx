import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import type { Coach, Slot } from '../api/types';
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

/** The session-length radio for `minutes`; the tiles render "30 | 45 | 60" in order. */
const durationRadio = (minutes: 30 | 45 | 60) => screen.getAllByRole('radio')[[30, 45, 60].indexOf(minutes)];

describe('CoachDetailScreen duration', () => {
  it('drops the selected slot when the session length changes', async () => {
    mount();
    await waitFor(() => expect(screen.getAllByRole('button', { name: /Jan 7/ }).length).toBeGreaterThan(0));

    fireEvent.press(screen.getAllByRole('button', { name: /Jan 7/ })[0]);
    expect(screen.getByText(/45 min with Casey Coach · (?!pick a time)/)).toBeTruthy();

    fireEvent.press(durationRadio(60));
    await waitFor(() => expect(mocked.openSlots).toHaveBeenLastCalledWith('c1', 60));
    expect(screen.getByText('60 min with Casey Coach · pick a time above')).toBeTruthy();

    fireEvent.press(screen.getByRole('button', { name: 'Book session' }));
    await waitFor(() => expect(screen.getByText('Pick a time slot first.')).toBeTruthy());
    expect(mocked.bookSession).not.toHaveBeenCalled();
  });

  it('keeps the selected slot when the same length is pressed again', async () => {
    mount();
    await waitFor(() => expect(screen.getAllByRole('button', { name: /Jan 7/ }).length).toBeGreaterThan(0));

    fireEvent.press(screen.getAllByRole('button', { name: /Jan 7/ })[0]);
    fireEvent.press(durationRadio(45));
    expect(screen.queryByText('45 min with Casey Coach · pick a time above')).toBeNull();
    expect(mocked.openSlots).toHaveBeenCalledTimes(1);
  });
});
