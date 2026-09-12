import { act, fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import CoachDashboardScreen from './CoachDashboardScreen';
import { sessionFixture, threadFixture } from './coachTestUtils.testutil';

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
    coachSessions: jest.fn(),
    coachThreads: jest.fn(),
    setSessionNotes: jest.fn(),
    setSessionStatus: jest.fn(),
  },
}));

jest.mock('../state/auth', () => ({ useAuth: () => ({ user: { id: 'coach-1', display_name: 'Casey Coach', role: 'coach' } }) }));

const mocked = api as jest.Mocked<typeof api>;

/** Never-settling promise so the busy state stays observable. */
const pending = <T,>() => new Promise<T>(() => undefined);

const renderLoaded = async ({
  sessions = [sessionFixture({ counterpart_name: 'Riley Client' })],
  threads = [threadFixture()],
} = {}) => {
  mocked.coachSessions.mockResolvedValue(sessions);
  mocked.coachThreads.mockResolvedValue(threads);
  render(<CoachDashboardScreen />);
  await screen.findByText(/^Good/);
  if (sessions.length) await screen.findAllByText(sessions[0].counterpart_name ?? 'Client');
  if (threads.length) await screen.findAllByText(threads[0].counterpart_name ?? 'Client');
};

describe('CoachDashboardScreen data', () => {
  it('loads scheduled sessions and active threads and greets by first name', async () => {
    await renderLoaded();
    expect(mocked.coachSessions).toHaveBeenCalledWith('scheduled');
    expect(mocked.coachThreads).toHaveBeenCalledWith('active');
    expect(screen.getByText(/^Good (morning|afternoon|evening), Casey$/)).toBeTruthy();
    expect(screen.getByText('Online')).toBeTruthy();
    expect(screen.getByText('45m')).toBeTruthy();
  });

  it('shows load errors for each half independently', async () => {
    mocked.coachSessions.mockRejectedValue(new Error('sessions down'));
    mocked.coachThreads.mockRejectedValue(new Error('threads down'));
    render(<CoachDashboardScreen />);
    await screen.findByText('sessions down');
    expect(screen.getByText('threads down')).toBeTruthy();
    expect(screen.queryByText('Nothing booked yet')).toBeNull();
    expect(screen.queryByText('Quiet for now')).toBeNull();
  });

  it('empty-state action for sessions navigates to Profile', async () => {
    await renderLoaded({ sessions: [], threads: [] });
    await screen.findByText('Nothing booked yet');
    expect(screen.getByText('Quiet for now')).toBeTruthy();
    fireEvent.press(screen.getByText('Update your availability'));
    expect(mockNavigate).toHaveBeenCalledWith('Profile');
  });
});

describe('CoachDashboardScreen chat navigation', () => {
  it('All chats opens the Threads list', async () => {
    await renderLoaded();
    fireEvent.press(screen.getByText('All chats'));
    expect(mockNavigate).toHaveBeenCalledWith('Chats', { screen: 'Threads' });
  });

  it('Open chat opens the thread with the client name as title', async () => {
    await renderLoaded();
    fireEvent.press(screen.getByText('Open chat'));
    expect(mockNavigate).toHaveBeenCalledWith('Chats', {
      screen: 'Chat',
      params: { threadID: 't1', title: 'Riley Client' },
    });
  });
});

describe('CoachDashboardScreen session actions', () => {
  it('Save notes sends the edited draft then reloads sessions', async () => {
    mocked.setSessionNotes.mockResolvedValue({} as never);
    await renderLoaded();
    fireEvent.changeText(screen.getByPlaceholderText(/What came up/), 'Great progress');
    await act(async () => {
      fireEvent.press(screen.getByText('Save notes'));
    });
    expect(mocked.setSessionNotes).toHaveBeenCalledWith('s1', 'Great progress');
    expect(mocked.coachSessions).toHaveBeenCalledTimes(2);
  });

  it('Completed marks the session completed then reloads', async () => {
    mocked.setSessionStatus.mockResolvedValue({} as never);
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Completed'));
    });
    expect(mocked.setSessionStatus).toHaveBeenCalledWith('s1', 'completed');
    expect(mocked.coachSessions).toHaveBeenCalledTimes(2);
  });

  it('No show marks the session no_show then reloads', async () => {
    mocked.setSessionStatus.mockResolvedValue({} as never);
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('No show'));
    });
    expect(mocked.setSessionStatus).toHaveBeenCalledWith('s1', 'no_show');
    expect(mocked.coachSessions).toHaveBeenCalledTimes(2);
  });

  it('disables only the busy session’s buttons while an action is in flight', async () => {
    mocked.setSessionStatus.mockReturnValue(pending());
    await renderLoaded({
      sessions: [
        sessionFixture({ id: 's1', counterpart_name: 'Riley Client' }),
        sessionFixture({ id: 's2', counterpart_name: 'Sam Client' }),
      ],
    });
    const [firstCompleted, secondCompleted] = screen.getAllByRole('button', { name: 'Completed' });
    await act(async () => {
      fireEvent.press(firstCompleted);
    });
    expect(firstCompleted).toBeDisabled();
    expect(screen.getAllByRole('button', { name: 'No show' })[0]).toBeDisabled();
    expect(secondCompleted).toBeEnabled();
    expect(screen.getAllByText('Save notes')).toHaveLength(1);

    fireEvent.press(firstCompleted);
    expect(mocked.setSessionStatus).toHaveBeenCalledTimes(1);
  });
});
