import { act, fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import MySessionsScreen from './MySessionsScreen';
import { sessionFixture, slotFixture } from './coachTestUtils.testutil';

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
    openSlots: jest.fn(),
    rescheduleSession: jest.fn(),
    cancelSession: jest.fn(),
    startThread: jest.fn(),
  },
}));

const mocked = api as jest.Mocked<typeof api>;
const SLOT = '2030-02-01T18:00:00Z';

const renderLoaded = async (sessions = [sessionFixture()]) => {
  mocked.mySessions.mockResolvedValue(sessions);
  render(<MySessionsScreen />);
  await screen.findByText('Your sessions');
  if (sessions.length > 0) await screen.findAllByText(sessions[0].counterpart_name ?? 'Coach');
};

describe('MySessionsScreen list', () => {
  it('renders the empty state whose action opens the coach list', async () => {
    await renderLoaded([]);
    await screen.findByText('No sessions yet');
    fireEvent.press(screen.getByText('Find a coach'));
    expect(mockNavigate).toHaveBeenCalledWith('Coaches', { screen: 'CoachList' });
  });

  it('shows the load error', async () => {
    mocked.mySessions.mockRejectedValue(new Error('sessions down'));
    render(<MySessionsScreen />);
    await screen.findByText('sessions down');
  });

  it('only renders action buttons for scheduled sessions', async () => {
    await renderLoaded([
      sessionFixture({ id: 's1', status: 'scheduled' }),
      sessionFixture({ id: 's2', status: 'completed', counterpart_name: 'Done Coach' }),
      sessionFixture({ id: 's3', status: 'cancelled', counterpart_name: 'Gone Coach' }),
    ]);
    expect(screen.getAllByText('Message')).toHaveLength(1);
    expect(screen.getAllByText('Reschedule')).toHaveLength(1);
    expect(screen.getAllByText('Cancel')).toHaveLength(1);
    expect(screen.getAllByText('Completed').length).toBeGreaterThan(0);
    expect(screen.getByText('Cancelled')).toBeTruthy();
  });

  it('shows coach notes and the stat tiles', async () => {
    await renderLoaded([
      sessionFixture({ id: 's1', coach_notes: 'Bring your last three openers.' }),
      sessionFixture({ id: 's2', status: 'completed', duration_minutes: 30 }),
    ]);
    expect(screen.getByText('Bring your last three openers.')).toBeTruthy();
    expect(screen.getByText('Upcoming')).toBeTruthy();
    expect(screen.getByText('30m')).toBeTruthy();
  });
});

describe('MySessionsScreen actions', () => {
  it('Message starts a thread scoped to the session and opens the chat', async () => {
    mocked.startThread.mockResolvedValue({ id: 't3' } as never);
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Message'));
    });
    expect(mocked.startThread).toHaveBeenCalledWith('c1', 's1');
    expect(mockNavigate).toHaveBeenCalledWith('Chats', {
      screen: 'Chat',
      params: { threadID: 't3', title: 'Casey Coach' },
    });
  });

  it('Reschedule opens the picker with slots excluding this session, and Close hides it', async () => {
    mocked.openSlots.mockResolvedValue([slotFixture(SLOT)]);
    await renderLoaded();

    await act(async () => {
      fireEvent.press(screen.getByText('Reschedule'));
    });
    expect(mocked.openSlots).toHaveBeenCalledWith('c1', 45, 's1');
    expect(screen.getByText('Pick a new time')).toBeTruthy();
    expect(screen.getByText('Close')).toBeTruthy();

    fireEvent.press(screen.getByText('Close'));
    expect(screen.queryByText('Pick a new time')).toBeNull();
    expect(screen.getByText('Reschedule')).toBeTruthy();
  });

  it('picking a slot reschedules, closes the picker and reloads', async () => {
    mocked.openSlots.mockResolvedValue([slotFixture(SLOT)]);
    mocked.rescheduleSession.mockResolvedValue({} as never);
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Reschedule'));
    });
    await act(async () => {
      fireEvent.press(screen.getAllByRole('button').at(-1)!);
    });
    expect(mocked.rescheduleSession).toHaveBeenCalledWith('s1', SLOT);
    expect(screen.queryByText('Pick a new time')).toBeNull();
    expect(mocked.mySessions).toHaveBeenCalledTimes(2);
  });

  it('shows the slot load error and the empty-slot copy', async () => {
    mocked.openSlots.mockRejectedValueOnce(new Error('no calendar'));
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Reschedule'));
    });
    expect(screen.getByText('no calendar')).toBeTruthy();
  });

  it('shows the no-slot copy when nothing is open', async () => {
    mocked.openSlots.mockResolvedValue([]);
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Reschedule'));
    });
    expect(screen.getByText('No open slots in the next few weeks.')).toBeTruthy();
  });

  it('a failed reschedule shows the error and keeps the picker open', async () => {
    mocked.openSlots.mockResolvedValue([slotFixture(SLOT)]);
    mocked.rescheduleSession.mockRejectedValue(new Error('slot gone'));
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Reschedule'));
    });
    await act(async () => {
      fireEvent.press(screen.getAllByRole('button').at(-1)!);
    });
    expect(screen.getByText('slot gone')).toBeTruthy();
    expect(screen.getByText('Pick a new time')).toBeTruthy();
    expect(mocked.mySessions).toHaveBeenCalledTimes(1);
  });

  it('Cancel calls cancelSession then reloads the list', async () => {
    mocked.cancelSession.mockResolvedValue({} as never);
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Cancel'));
    });
    expect(mocked.cancelSession).toHaveBeenCalledWith('s1');
    expect(mocked.mySessions).toHaveBeenCalledTimes(2);
  });
});
