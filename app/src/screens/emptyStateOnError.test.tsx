import { render, screen } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import AnalysisHistoryScreen from './AnalysisHistoryScreen';
import CoachListScreen from './CoachListScreen';
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
    listCoaches: jest.fn(),
    mySessions: jest.fn(),
    conversations: jest.fn(),
  },
}));

const mocked = api as jest.Mocked<typeof api>;
const stackProps = { navigation: { navigate: jest.fn() }, route: { key: 'k', name: 'n' } };

describe('list screens on load failure', () => {
  it('CoachList shows the error without the "No coaches yet" card', async () => {
    mocked.listCoaches.mockRejectedValue(new Error('directory offline'));
    render(<CoachListScreen {...(stackProps as unknown as React.ComponentProps<typeof CoachListScreen>)} />);
    await screen.findByText('directory offline');
    expect(screen.queryByText('No coaches yet')).toBeNull();
  });

  it('MySessions shows the error without the "No sessions yet" card', async () => {
    mocked.mySessions.mockRejectedValue(new Error('sessions offline'));
    render(<MySessionsScreen />);
    await screen.findByText('sessions offline');
    expect(screen.queryByText('No sessions yet')).toBeNull();
  });

  it('AnalysisHistory shows the error without the "Nothing analysed yet" card', async () => {
    mocked.conversations.mockRejectedValue(new Error('history offline'));
    render(<AnalysisHistoryScreen {...(stackProps as unknown as React.ComponentProps<typeof AnalysisHistoryScreen>)} />);
    await screen.findByText('history offline');
    expect(screen.queryByText('Nothing analysed yet')).toBeNull();
  });
});
