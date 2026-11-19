import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api, ApiError } from '../api/client';
import type { ProfileScore } from '../api/types';
import { ProfileScoreCard } from './ProfileScoreCard';

const mockNavigate = jest.fn();
jest.mock('@react-navigation/native', () => {
  const { useEffect } = jest.requireActual<typeof React>('react');
  return {
    useFocusEffect: (effect: () => void) => useEffect(effect, [effect]),
    useNavigation: () => ({ navigate: mockNavigate }),
  };
});
const getScore = jest.spyOn(api, 'profileScore');

const score = (band: ProfileScore['band']): ProfileScore => ({
  band,
  rubric_version: 'profile-v1',
  conversations_counted: band === 'collecting_evidence' ? 1 : 3,
  minimum_conversations: 3,
  evidence: [{ analysis_id: 'a1', conversation_id: 'c1', submitted_at: '2026-11-19T12:00:00Z' }],
});

beforeEach(() => {
  getScore.mockReset();
  mockNavigate.mockClear();
});

it.each([
  ['collecting_evidence', 'Collecting evidence'],
  ['strong', 'Strong'],
  ['developing', 'Developing'],
  ['needs_more_coaching', 'Needs more coaching'],
] as const)('displays %s with only the appropriate actions', async (band, label) => {
  getScore.mockResolvedValue(score(band));
  render(<ProfileScoreCard />);
  expect(await screen.findByRole('header', { name: label })).toBeTruthy();
  const isCollecting = band === 'collecting_evidence';
  expect(screen.getByText(isCollecting ? '1 of 3 qualifying conversations' : 'Based on 3 qualifying conversations')).toBeTruthy();
  const start = screen.queryByRole('button', { name: 'Analyse a conversation' });
  expect(Boolean(start)).toBe(isCollecting);
  if (start) {
    fireEvent.press(start);
    expect(mockNavigate).toHaveBeenLastCalledWith('Analyse', { screen: 'Submit' });
  }
  expect(Boolean(screen.queryByText(/What would help you notice/))).toBe(band === 'needs_more_coaching');
  fireEvent.press(screen.getByRole('button', { name: 'Review analysis 1' }));
  expect(mockNavigate).toHaveBeenLastCalledWith('Analyse', {
    screen: 'Result',
    params: { analysisID: 'a1', conversationID: 'c1' },
  });
});

it('keeps a failed request separate from a band and retries on demand', async () => {
  getScore.mockRejectedValueOnce(new ApiError(503, 'unavailable')).mockResolvedValueOnce(score('strong'));
  render(<ProfileScoreCard />);
  expect(screen.getByText('Loading your profile score…')).toBeTruthy();
  expect(await screen.findByText('Your profile score is unavailable right now.')).toBeTruthy();
  expect(screen.queryByText('Collecting evidence')).toBeNull();
  fireEvent.press(screen.getByRole('button', { name: 'Retry profile score' }));
  expect(await screen.findByRole('header', { name: 'Strong' })).toBeTruthy();
});

it('hides the score card if the server disables it', async () => {
  getScore.mockRejectedValue(new ApiError(404, 'not found'));
  render(<ProfileScoreCard />);
  await waitFor(() => expect(screen.queryByTestId('profile-score-card')).toBeNull());
});

it('times out a stalled score request instead of showing an endless loading state', () => {
  jest.useFakeTimers();
  try {
    getScore.mockImplementation(() => new Promise(() => undefined));
    render(<ProfileScoreCard />);
    expect(screen.getByText('Loading your profile score…')).toBeTruthy();
    act(() => jest.advanceTimersByTime(10_000));
    expect(screen.getByRole('button', { name: 'Retry profile score' })).toBeTruthy();
    expect(getScore.mock.calls[0]?.[0].aborted).toBe(true);
  } finally {
    jest.useRealTimers();
  }
});
