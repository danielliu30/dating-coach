import { render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import type { Coach } from '../api/types';
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
    startThread: jest.fn(),
  },
}));

// The signed-in user IS the coach being viewed.
jest.mock('../state/auth', () => ({
  useAuth: () => ({ user: { id: 'c1', display_name: 'Casey Coach', role: 'coach' } }),
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

describe('CoachDetailScreen self view', () => {
  it('hides booking and chat when the coach views their own profile', async () => {
    mocked.getCoach.mockResolvedValue(coach);
    mocked.coachAvailability.mockResolvedValue([]);
    mocked.openSlots.mockResolvedValue([]);
    mocked.listCoachReviews.mockResolvedValue([]);
    mocked.coachReviewSummary.mockResolvedValue({
      model_version: 'heuristic',
      recommended: 0,
      total: 0,
      summary: '',
      strengths: [],
    });
    mocked.mySessions.mockResolvedValue([]);

    const ScreenAny = CoachDetailScreen as unknown as React.ComponentType<{ route: unknown; navigation: unknown }>;
    render(<ScreenAny route={{ params: { coachID: 'c1', coachName: 'Casey Coach' } }} navigation={{}} />);

    await waitFor(() => expect(screen.getByText('This is your public coach profile.')).toBeTruthy());
    expect(screen.queryByRole('button', { name: 'Book session' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Start a live chat' })).toBeNull();
    expect(screen.queryByText('Book a session')).toBeNull();
  });
});
