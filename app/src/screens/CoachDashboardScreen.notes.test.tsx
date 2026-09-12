import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import type { CoachingSession } from '../api/types';
import CoachDashboardScreen from './CoachDashboardScreen';

jest.mock('@react-navigation/native', () => {
  const React = require('react');
  return {
    useFocusEffect: (effect: () => void) => React.useEffect(effect, [effect]),
    useNavigation: () => ({ navigate: jest.fn() }),
  };
});

jest.mock('../api/client', () => ({
  api: {
    coachSessions: jest.fn(),
    coachThreads: jest.fn(),
    setSessionNotes: jest.fn(),
  },
}));

jest.mock('../state/auth', () => ({
  useAuth: () => ({ user: { id: 'c1', display_name: 'Casey Coach', role: 'coach' } }),
}));

const mocked = api as jest.Mocked<typeof api>;

const session: CoachingSession = {
  id: 's1',
  user_id: 'u1',
  coach_id: 'c1',
  counterpart_name: 'Riley Client',
  scheduled_time: '2030-01-07T18:00:00Z',
  duration_minutes: 45,
  status: 'scheduled',
  topic: 'Opening lines',
  coach_notes: 'Existing notes from last time',
  payment_status: 'not_required',
  amount_cents: 0,
  currency: 'usd',
};

describe('CoachDashboardScreen save notes', () => {
  beforeEach(() => {
    mocked.coachSessions.mockResolvedValue([session]);
    mocked.coachThreads.mockResolvedValue([]);
    mocked.setSessionNotes.mockResolvedValue(session);
  });

  it('keeps the existing notes when "Save notes" is pressed without editing', async () => {
    render(<CoachDashboardScreen />);
    await screen.findByDisplayValue('Existing notes from last time');

    fireEvent.press(screen.getByText('Save notes'));

    await waitFor(() => expect(mocked.setSessionNotes).toHaveBeenCalledTimes(1));
    expect(mocked.setSessionNotes).toHaveBeenCalledWith('s1', 'Existing notes from last time');
  });

  it('sends the edited draft when the notes were changed', async () => {
    render(<CoachDashboardScreen />);
    const input = await screen.findByDisplayValue('Existing notes from last time');

    fireEvent.changeText(input, 'Follow up on openers');
    fireEvent.press(screen.getByText('Save notes'));

    await waitFor(() => expect(mocked.setSessionNotes).toHaveBeenCalledWith('s1', 'Follow up on openers'));
  });
});
