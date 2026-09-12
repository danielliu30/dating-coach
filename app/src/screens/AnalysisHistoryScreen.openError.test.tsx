import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import AnalysisHistoryScreen from './AnalysisHistoryScreen';

jest.mock('@react-navigation/native', () => {
  const React = require('react');
  return { useFocusEffect: (effect: () => void) => React.useEffect(effect, [effect]) };
});

jest.mock('../api/client', () => ({ api: { conversations: jest.fn(), latestResult: jest.fn() } }));

const mocked = api as jest.Mocked<typeof api>;
const navigation = { navigate: jest.fn() };
const props = { navigation, route: { key: 'History', name: 'History' } } as unknown as React.ComponentProps<
  typeof AnalysisHistoryScreen
>;

beforeEach(() => {
  mocked.conversations.mockResolvedValue([
    { id: 'conv1', title: 'Sourdough', platform: 'hinge', match_name: 'Sam', created_at: '2030-01-01T00:00:00Z' },
    { id: 'conv2', title: 'Hiking', platform: 'bumble', match_name: '', created_at: '2030-01-02T00:00:00Z' },
  ]);
});

describe('AnalysisHistoryScreen open failure', () => {
  it('shows the failure under the tapped card and does not navigate', async () => {
    mocked.latestResult.mockRejectedValue(new Error('no analysis yet'));
    render(<AnalysisHistoryScreen {...props} />);

    fireEvent.press(await screen.findByText('Sourdough'));

    await screen.findByText('no analysis yet');
    expect(navigation.navigate).not.toHaveBeenCalled();
    expect(screen.getByText('Sourdough')).toBeTruthy();
  });

  it('clears the failure once another open succeeds', async () => {
    mocked.latestResult.mockRejectedValueOnce(new Error('no analysis yet')).mockResolvedValue({
      id: 'a2',
      conversation_id: 'conv2',
      status: 'succeeded',
      model_version: 'v1',
      segments: null,
      overall: null,
      created_at: '2030-01-02T00:00:00Z',
    });
    render(<AnalysisHistoryScreen {...props} />);

    fireEvent.press(await screen.findByText('Sourdough'));
    await screen.findByText('no analysis yet');

    fireEvent.press(screen.getByText('Hiking'));
    await waitFor(() =>
      expect(navigation.navigate).toHaveBeenCalledWith('Result', { analysisID: 'a2', conversationID: 'conv2' }),
    );
    expect(screen.queryByText('no analysis yet')).toBeNull();
  });
});
