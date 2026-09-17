import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';
import { ActivityIndicator } from 'react-native';

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
    setSessionMeetingUrl: jest.fn(),
    setSessionStatus: jest.fn(),
  },
}));

jest.mock('../state/auth', () => ({
  useAuth: () => ({ user: { id: 'c1', display_name: 'Casey Coach', role: 'coach' } }),
}));

const mocked = api as jest.Mocked<typeof api>;

const session = (id: string): CoachingSession => ({
  id,
  user_id: 'u1',
  coach_id: 'c1',
  counterpart_name: `Client ${id}`,
  scheduled_time: '2030-01-07T18:00:00Z',
  duration_minutes: 45,
  status: 'scheduled',
  topic: 'Opening lines',
  payment_status: 'not_required',
  amount_cents: 0,
  currency: 'usd',
});

/** Buttons labelled `name`, in card order (one per rendered session). */
const buttons = (name: string) => screen.getAllByRole('button', { name });
/** `Button` swaps its label for a spinner while loading, so count spinners. */
const spinners = () => screen.UNSAFE_queryAllByType(ActivityIndicator);

describe('CoachDashboardScreen action busy state', () => {
  beforeEach(() => {
    mocked.coachSessions.mockResolvedValue([session('s1'), session('s2')]);
    mocked.coachThreads.mockResolvedValue([]);
  });

  it('marks only the pressed action busy and leaves other cards untouched', async () => {
    let finish: () => void = () => {};
    mocked.setSessionNotes.mockImplementationOnce(() => new Promise((resolve) => (finish = () => resolve(session('s1')))));
    render(<CoachDashboardScreen />);
    await waitFor(() => expect(buttons('Save notes')).toHaveLength(2));

    fireEvent.press(buttons('Save notes')[0]);
    await waitFor(() => expect(mocked.setSessionNotes).toHaveBeenCalledTimes(1));

    // Exactly one spinner: the pressed "Save notes" (its label is replaced while loading).
    expect(spinners()).toHaveLength(1);
    expect(buttons('Save notes')).toHaveLength(1);
    // Same card: siblings are held but keep their labels.
    expect(buttons('Completed')[0]).toBeDisabled();
    expect(buttons('No show')[0]).toBeDisabled();
    expect(buttons('Save meeting link')[0]).toBeDisabled();

    // Other card: fully interactive.
    expect(buttons('Save notes')[0]).toBeEnabled();
    expect(buttons('Completed')[1]).toBeEnabled();

    fireEvent.press(buttons('Completed')[0]);
    expect(mocked.setSessionStatus).not.toHaveBeenCalled();

    await act(async () => finish());
    await waitFor(() => expect(buttons('Save notes')).toHaveLength(2));
    expect(spinners()).toHaveLength(0);
    expect(buttons('Completed')[0]).toBeEnabled();
  });

  it('tracks requests on different cards independently', async () => {
    let finishA: () => void = () => {};
    let finishB: () => void = () => {};
    mocked.setSessionNotes.mockImplementationOnce(() => new Promise((resolve) => (finishA = () => resolve(session('s1')))));
    mocked.setSessionMeetingUrl.mockImplementationOnce(() => new Promise((resolve) => (finishB = () => resolve(session('s2')))));
    render(<CoachDashboardScreen />);
    await waitFor(() => expect(buttons('Save notes')).toHaveLength(2));

    fireEvent.press(buttons('Save notes')[0]);
    fireEvent.press(buttons('Save meeting link')[1]);
    await waitFor(() => expect(mocked.setSessionMeetingUrl).toHaveBeenCalledTimes(1));
    expect(spinners()).toHaveLength(2);

    await act(async () => finishA());
    await waitFor(() => expect(spinners()).toHaveLength(1));
    // Card B is still held after card A settled.
    expect(buttons('Save notes')[1]).toBeDisabled();
    expect(buttons('Completed')[1]).toBeDisabled();
    expect(buttons('Completed')[0]).toBeEnabled();

    await act(async () => finishB());
    await waitFor(() => expect(spinners()).toHaveLength(0));
    expect(buttons('Completed')[1]).toBeEnabled();
  });
});
