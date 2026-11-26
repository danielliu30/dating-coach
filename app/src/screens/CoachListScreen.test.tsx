import { render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import type { Coach } from '../api/types';
import CoachListScreen from './CoachListScreen';

jest.mock('@react-navigation/native', () => {
  const React = require('react');
  return {
    useFocusEffect: (effect: () => void) => React.useEffect(effect, [effect]),
  };
});

jest.mock('../api/client', () => ({
  api: {
    listCoaches: jest.fn(),
  },
}));

// The signed-in coach's own profile must not appear in the directory.
jest.mock('../state/auth', () => ({
  useAuth: () => ({ user: { id: 'me', display_name: 'Casey Coach', role: 'coach' } }),
}));

const mocked = api as jest.Mocked<typeof api>;

const coach = (id: string, name: string): Coach => ({
  id,
  display_name: name,
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
});

describe('CoachListScreen', () => {
  it('filters the signed-in coach out of the directory', async () => {
    mocked.listCoaches.mockResolvedValue([coach('me', 'Casey Coach'), coach('c2', 'Riley Coach')]);

    const ScreenAny = CoachListScreen as unknown as React.ComponentType<{ navigation: unknown }>;
    render(<ScreenAny navigation={{ navigate: jest.fn() }} />);

    await waitFor(() => expect(screen.getByText('Riley Coach')).toBeTruthy());
    expect(screen.queryByText('Casey Coach')).toBeNull();
  });
});
