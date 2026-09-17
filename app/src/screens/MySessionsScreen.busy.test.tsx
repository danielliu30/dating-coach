import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';
import { ActivityIndicator } from 'react-native';

import { api } from '../api/client';
import type { CoachingSession, Slot } from '../api/types';
import MySessionsScreen from './MySessionsScreen';

jest.mock('@react-navigation/native', () => {
  const React = require('react');
  return {
    useFocusEffect: (effect: () => void) => React.useEffect(effect, [effect]),
    useNavigation: () => ({ navigate: jest.fn() }),
  };
});

jest.mock('../api/client', () => ({
  api: {
    mySessions: jest.fn(),
    cancelSession: jest.fn(),
    openSlots: jest.fn(),
    rescheduleSession: jest.fn(),
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

const slots: Slot[] = [
  { start: '2030-01-08T10:00:00Z', duration_minutes: 45 },
  { start: '2030-01-08T11:00:00Z', duration_minutes: 45 },
];

/** `Button` swaps its label for a spinner while loading, so count spinners. */
const spinners = () => screen.UNSAFE_queryAllByType(ActivityIndicator);
const slotButtons = () => screen.queryAllByRole('button', { name: /Jan 8/ });

async function openRescheduleList() {
  render(<MySessionsScreen />);
  await screen.findByText('Reschedule');
  fireEvent.press(screen.getByText('Reschedule'));
  await waitFor(() => expect(slotButtons()).toHaveLength(2));
}

describe('MySessionsScreen action busy state', () => {
  beforeEach(() => {
    mocked.mySessions.mockResolvedValue([session]);
    mocked.openSlots.mockResolvedValue(slots);
  });

  it('spins only the tapped slot while rescheduling and holds the rest', async () => {
    let finish: () => void = () => {};
    mocked.rescheduleSession.mockImplementationOnce(() => new Promise((resolve) => (finish = () => resolve(session))));
    await openRescheduleList();

    fireEvent.press(slotButtons()[0]);
    await waitFor(() => expect(mocked.rescheduleSession).toHaveBeenCalledWith('s1', slots[0].start));

    expect(spinners()).toHaveLength(1);
    expect(slotButtons()).toHaveLength(1);
    expect(slotButtons()[0]).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled();

    fireEvent.press(screen.getByRole('button', { name: 'Cancel' }));
    expect(mocked.cancelSession).not.toHaveBeenCalled();

    await act(async () => finish());
    await waitFor(() => expect(spinners()).toHaveLength(0));
  });

  it('spins only Cancel while cancelling and holds the slot list', async () => {
    let finish: () => void = () => {};
    mocked.cancelSession.mockImplementationOnce(() => new Promise((resolve) => (finish = () => resolve(session))));
    await openRescheduleList();

    fireEvent.press(screen.getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(mocked.cancelSession).toHaveBeenCalledTimes(1));

    expect(spinners()).toHaveLength(1);
    expect(slotButtons()).toHaveLength(2);
    expect(slotButtons()[0]).toBeDisabled();
    expect(slotButtons()[1]).toBeDisabled();

    await act(async () => finish());
    await waitFor(() => expect(spinners()).toHaveLength(0));
    expect(slotButtons()[0]).toBeEnabled();
  });
});
