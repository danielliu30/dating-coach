import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import type { CoachingSession } from '../api/types';
import MySessionsScreen from './MySessionsScreen';

const mockNavigate = jest.fn();

jest.mock('@react-navigation/native', () => {
  const React = require('react');
  return {
    useFocusEffect: (effect: () => void) => React.useEffect(effect, [effect]),
    useNavigation: () => ({ navigate: mockNavigate }),
  };
});

jest.mock('../api/client', () => ({
  api: {
    mySessions: jest.fn(),
    cancelSession: jest.fn(),
    startThread: jest.fn(),
  },
}));

const mocked = api as jest.Mocked<typeof api>;

const session: CoachingSession = {
  id: 's1',
  user_id: 'u1',
  coach_id: 'c1',
  counterpart_name: 'Casey Coach',
  scheduled_time: '2030-01-07T18:00:00Z',
  duration_minutes: 45,
  status: 'scheduled',
  topic: 'Opening lines',
  payment_status: 'not_required',
  amount_cents: 0,
  currency: 'usd',
};

describe('MySessionsScreen action errors', () => {
  beforeEach(() => {
    mocked.mySessions.mockResolvedValue([session]);
  });

  it('shows the cancel failure and keeps the session actionable', async () => {
    mocked.cancelSession.mockRejectedValue(new Error('too late to cancel'));
    render(<MySessionsScreen />);
    await screen.findByText('Cancel');

    fireEvent.press(screen.getByText('Cancel'));

    await screen.findByText('too late to cancel');
    expect(mocked.mySessions).toHaveBeenCalledTimes(1);
    expect(screen.getByText('Cancel')).toBeTruthy();
  });

  it('shows the message failure and does not navigate', async () => {
    mocked.startThread.mockRejectedValue(new Error('coach unavailable'));
    render(<MySessionsScreen />);
    await screen.findByText('Message');

    fireEvent.press(screen.getByText('Message'));

    await screen.findByText('coach unavailable');
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('clears a previous action error when the next action succeeds', async () => {
    mocked.cancelSession.mockRejectedValueOnce(new Error('too late to cancel')).mockResolvedValue(session);
    render(<MySessionsScreen />);
    await screen.findByText('Cancel');

    fireEvent.press(screen.getByText('Cancel'));
    await screen.findByText('too late to cancel');

    fireEvent.press(screen.getByText('Cancel'));
    await waitFor(() => expect(mocked.mySessions).toHaveBeenCalledTimes(2));
    expect(screen.queryByText('too late to cancel')).toBeNull();
  });
});
