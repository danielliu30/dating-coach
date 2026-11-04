import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

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
  duration_minutes: 30,
  status: 'scheduled',
  topic: 'Opening lines',
  payment_status: 'not_required',
  amount_cents: 0,
  currency: 'usd',
};

// Twelve half-hour slots from 12:00 UTC, so every one is on Jan 8 in any test time zone.
const slots: Slot[] = Array.from({ length: 12 }, (_, i) => ({
  start: new Date(Date.UTC(2030, 0, 8, 12, i * 30)).toISOString(),
  duration_minutes: 30,
}));

const slotButtons = () => screen.queryAllByRole('button', { name: /Jan 8/ });
const showMore = () => screen.queryByRole('button', { name: /Show more times/ });

async function openRescheduleList() {
  await screen.findByText('Reschedule');
  fireEvent.press(screen.getByText('Reschedule'));
  await waitFor(() => expect(slotButtons()).toHaveLength(8));
}

describe('MySessionsScreen reschedule slot list', () => {
  beforeEach(() => {
    mocked.mySessions.mockResolvedValue([session]);
    mocked.openSlots.mockResolvedValue(slots);
    mocked.rescheduleSession.mockResolvedValue(session);
  });

  it('reveals slots beyond the eighth and reschedules onto one', async () => {
    render(<MySessionsScreen />);
    await openRescheduleList();
    expect(showMore()).toHaveTextContent('Show more times (4 more)');

    fireEvent.press(showMore()!);
    expect(slotButtons()).toHaveLength(12);
    expect(showMore()).toBeNull();

    fireEvent.press(slotButtons()[9]);
    await waitFor(() => expect(mocked.rescheduleSession).toHaveBeenCalledWith('s1', slots[9].start));
  });

  it('starts from the first page again when the panel is reopened', async () => {
    render(<MySessionsScreen />);
    await openRescheduleList();
    fireEvent.press(showMore()!);
    expect(slotButtons()).toHaveLength(12);

    fireEvent.press(screen.getByText('Close'));
    await openRescheduleList();
    expect(showMore()).not.toBeNull();
  });

  it('shows no "Show more" control when every slot already fits', async () => {
    mocked.openSlots.mockResolvedValue(slots.slice(0, 8));
    render(<MySessionsScreen />);
    await openRescheduleList();
    expect(showMore()).toBeNull();
  });
});
