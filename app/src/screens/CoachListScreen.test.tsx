import { fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import CoachListScreen from './CoachListScreen';
import { coachFixture } from './coachTestUtils.testutil';

jest.mock('@react-navigation/native', () => {
  const React = require('react');
  return { useFocusEffect: (effect: () => void) => React.useEffect(effect, [effect]) };
});

jest.mock('../api/client', () => ({ api: { listCoaches: jest.fn() } }));

const mocked = api as jest.Mocked<typeof api>;
const navigation = { navigate: jest.fn() };
const props = { navigation, route: { key: 'CoachList', name: 'CoachList' } } as unknown as React.ComponentProps<
  typeof CoachListScreen
>;

describe('CoachListScreen', () => {
  it('lists accepting coaches and opens the detail with id + name on press', async () => {
    mocked.listCoaches.mockResolvedValue([coachFixture(), coachFixture({ id: 'c2', display_name: 'Drew', headline: '' })]);
    render(<CoachListScreen {...props} />);

    await screen.findByText('Casey Coach');
    expect(mocked.listCoaches).toHaveBeenCalledWith(true);
    expect(screen.getByText('Human dating coach')).toBeTruthy();
    expect(screen.getByText('Accepting clients')).toBeTruthy();

    fireEvent.press(screen.getByText('Casey Coach'));
    expect(navigation.navigate).toHaveBeenCalledWith('CoachDetail', { coachID: 'c1', coachName: 'Casey Coach' });
  });

  it('shows the empty state once loading finishes with no coaches', async () => {
    mocked.listCoaches.mockResolvedValue([]);
    render(<CoachListScreen {...props} />);
    await screen.findByText('No coaches yet');
    expect(screen.queryByText('Accepting clients')).toBeNull();
  });

  it('shows the error and no empty state when loading fails', async () => {
    mocked.listCoaches.mockRejectedValue(new Error('directory offline'));
    render(<CoachListScreen {...props} />);
    await screen.findByText('directory offline');
    expect(screen.getByText('No coaches yet')).toBeTruthy();
  });

  it('does not render the empty state while the first load is pending', () => {
    mocked.listCoaches.mockReturnValue(new Promise(() => undefined));
    render(<CoachListScreen {...props} />);
    expect(screen.queryByText('No coaches yet')).toBeNull();
  });
});
