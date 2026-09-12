import { act, fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import CoachDetailScreen from './CoachDetailScreen';
import { coachFixture, slotFixture } from './coachTestUtils.testutil';

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
    getCoach: jest.fn(),
    coachAvailability: jest.fn(),
    openSlots: jest.fn(),
    bookSession: jest.fn(),
    startThread: jest.fn(),
  },
}));

const mocked = api as jest.Mocked<typeof api>;
const props = {
  navigation: { navigate: jest.fn() },
  route: { key: 'CoachDetail', name: 'CoachDetail', params: { coachID: 'c1', coachName: 'Casey Coach' } },
} as unknown as React.ComponentProps<typeof CoachDetailScreen>;

const SLOT_A = '2030-01-07T18:00:00Z';
const SLOT_B = '2030-01-08T18:00:00Z';

beforeEach(() => {
  mocked.getCoach.mockResolvedValue(coachFixture());
  mocked.coachAvailability.mockResolvedValue([{ weekday: 1, start_minute: 17 * 60, end_minute: 21 * 60 }]);
  mocked.openSlots.mockResolvedValue([slotFixture(SLOT_A), slotFixture(SLOT_B)]);
});

const renderLoaded = async () => {
  render(<CoachDetailScreen {...props} />);
  await screen.findByText('Casey Coach');
  await screen.findAllByRole('button', { name: /Jan/ });
};

const slotButtons = () => screen.getAllByRole('button', { name: /Jan/ });

describe('CoachDetailScreen loading', () => {
  it('fetches coach, availability and 45-minute slots for the route coach', async () => {
    await renderLoaded();
    expect(mocked.getCoach).toHaveBeenCalledWith('c1');
    expect(mocked.coachAvailability).toHaveBeenCalledWith('c1');
    expect(mocked.openSlots).toHaveBeenCalledWith('c1', 45);
  });

  it('renders the fetched weekly availability windows', async () => {
    await renderLoaded();
    expect(screen.getByText('Mon')).toBeTruthy();
    expect(screen.getByText('17:00 – 21:00')).toBeTruthy();
  });

  it('shows the not-published copy when availability is empty', async () => {
    mocked.coachAvailability.mockResolvedValue([]);
    await renderLoaded();
    expect(screen.getByText('This coach has not published availability yet.')).toBeTruthy();
  });

  it('shows the coach load error instead of the page', async () => {
    mocked.getCoach.mockRejectedValue(new Error('coach gone'));
    render(<CoachDetailScreen {...props} />);
    await screen.findByText('coach gone');
    expect(screen.queryByText('Book session')).toBeNull();
  });

  it('changing the duration refetches slots and clears nothing else', async () => {
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('60'));
    });
    expect(mocked.openSlots).toHaveBeenLastCalledWith('c1', 60);
    expect(screen.getByRole('radio', { name: /60/ })).toBeSelected();
  });

  it('shows the no-slots copy when the window is empty', async () => {
    mocked.openSlots.mockResolvedValue([]);
    render(<CoachDetailScreen {...props} />);
    await screen.findByText('No open slots in this window. Try another length.');
  });
});

describe('CoachDetailScreen booking', () => {
  it('refuses to book without a selected slot and does not call the API', async () => {
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Book session'));
    });
    expect(screen.getByText('Pick a time slot first.')).toBeTruthy();
    expect(mocked.bookSession).not.toHaveBeenCalled();
  });

  it('books the selected slot with duration and trimmed topic, then clears selection and reloads slots', async () => {
    mocked.bookSession.mockResolvedValue({} as never);
    await renderLoaded();

    fireEvent.press(slotButtons()[0]);
    fireEvent.changeText(screen.getByPlaceholderText('Opening messages on Hinge'), '  Hinge openers  ');
    expect(screen.getByText(/45 min with Casey Coach · (?!pick a time)/)).toBeTruthy();

    await act(async () => {
      fireEvent.press(screen.getByText('Book session'));
    });

    expect(mocked.bookSession).toHaveBeenCalledWith({
      coach_id: 'c1',
      scheduled_time: SLOT_A,
      duration_minutes: 45,
      topic: 'Hinge openers',
    });
    expect(screen.getByText('Session booked — see it under Sessions.')).toBeTruthy();
    expect(screen.getByText('45 min with Casey Coach · pick a time above')).toBeTruthy();
    expect(mocked.openSlots).toHaveBeenCalledTimes(2);
  });

  it('shows the booking error and keeps the selection', async () => {
    mocked.bookSession.mockRejectedValue(new Error('slot taken'));
    await renderLoaded();
    fireEvent.press(slotButtons()[1]);
    await act(async () => {
      fireEvent.press(screen.getByText('Book session'));
    });
    expect(screen.getByText('slot taken')).toBeTruthy();
    expect(screen.queryByText(/pick a time above/)).toBeNull();
    expect(mocked.openSlots).toHaveBeenCalledTimes(1);
  });

  it('shows the price for the chosen duration', async () => {
    await renderLoaded();
    expect(screen.getByText('$90.00')).toBeTruthy();
    await act(async () => {
      fireEvent.press(screen.getByText('30'));
    });
    expect(screen.getByText('$60.00')).toBeTruthy();
  });
});

describe('CoachDetailScreen live chat', () => {
  it('starts a thread for the coach and navigates to the Chat screen', async () => {
    mocked.startThread.mockResolvedValue({ id: 't9' } as never);
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Start a live chat'));
    });
    expect(mocked.startThread).toHaveBeenCalledWith('c1');
    expect(mockNavigate).toHaveBeenCalledWith('Chats', {
      screen: 'Chat',
      params: { threadID: 't9', title: 'Casey Coach' },
    });
  });

  it('surfaces a start-thread failure without navigating', async () => {
    mocked.startThread.mockRejectedValue(new Error('coach offline'));
    await renderLoaded();
    await act(async () => {
      fireEvent.press(screen.getByText('Start a live chat'));
    });
    expect(screen.getByText('coach offline')).toBeTruthy();
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});
