import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import { api } from '../api/client';
import { NotificationSocket } from '../api/notificationSocket';
import type { AnalysisResult, NotificationEvent } from '../api/types';
import AnalysisHistoryScreen from './AnalysisHistoryScreen';
import AnalysisResultScreen from './AnalysisResultScreen';

jest.mock('@react-navigation/native', () => {
  const React = require('react');
  return { useFocusEffect: (effect: () => void) => React.useEffect(effect, [effect]) };
});

jest.mock('../api/client', () => ({
  api: {
    conversations: jest.fn(),
    latestResult: jest.fn(),
    analysisResult: jest.fn(),
    reanalyzeConversation: jest.fn(),
    labelConversation: jest.fn(),
  },
}));

jest.mock('../state/auth', () => ({ useAuth: () => ({ token: 'tok' }) }));

jest.mock('../api/notificationSocket', () => ({ NotificationSocket: jest.fn() }));

const mocked = api as jest.Mocked<typeof api>;
const SocketMock = NotificationSocket as unknown as jest.Mock;
const socketClose = jest.fn();
let pushEvent: (event: NotificationEvent) => void = () => {
  throw new Error('no notification socket was opened');
};

beforeEach(() => {
  SocketMock.mockReset();
  socketClose.mockReset();
  SocketMock.mockImplementation((_token: () => string | null, handlers: { onEvent: typeof pushEvent }) => {
    pushEvent = handlers.onEvent;
    return { connect: jest.fn(), close: socketClose };
  });
});
const navigation = { navigate: jest.fn() };

/** Analysis fixture; defaults to a completed result with one scored segment. */
const resultFixture = (overrides: Partial<AnalysisResult> = {}): AnalysisResult => ({
  id: 'a1',
  conversation_id: 'conv1',
  status: 'succeeded',
  model_version: 'v1.2',
  segments: [
    { start_position: 0, end_position: 2, engagement_score: 0.8, label: 'engaging', comment: 'Strong opener.' },
  ],
  overall: {
    engagement_score: 0.72,
    summary: 'Warm and curious.',
    strengths: ['Asked a real question'],
    improvements: ['Shorter replies'],
  },
  created_at: '2030-01-01T00:00:00Z',
  ...overrides,
});

describe('AnalysisHistoryScreen', () => {
  const props = { navigation, route: { key: 'History', name: 'History' } } as unknown as React.ComponentProps<
    typeof AnalysisHistoryScreen
  >;

  it('opens a conversation via latestResult and navigates to Result', async () => {
    mocked.conversations.mockResolvedValue([
      { id: 'conv1', title: 'Sourdough', platform: 'hinge', match_name: 'Sam', created_at: '2030-01-01T00:00:00Z' },
      { id: 'conv2', title: '', platform: 'bumble', match_name: '', created_at: '2030-01-02T00:00:00Z' },
    ]);
    mocked.latestResult.mockResolvedValue(resultFixture({ id: 'a9' }));
    render(<AnalysisHistoryScreen {...props} />);

    await screen.findByText('Sourdough');
    expect(screen.getByText('Untitled conversation')).toBeTruthy();
    expect(screen.getByText('With Sam')).toBeTruthy();
    expect(screen.getByText('No name given')).toBeTruthy();

    fireEvent.press(screen.getByText('Sourdough'));
    await waitFor(() =>
      expect(navigation.navigate).toHaveBeenCalledWith('Result', { analysisID: 'a9', conversationID: 'conv1' }),
    );
    expect(mocked.latestResult).toHaveBeenCalledWith('conv1');
  });

  it('empty-state action navigates to Submit', async () => {
    mocked.conversations.mockResolvedValue([]);
    render(<AnalysisHistoryScreen {...props} />);
    fireEvent.press(await screen.findByText('Analyse a conversation'));
    expect(navigation.navigate).toHaveBeenCalledWith('Submit');
  });

  it('renders the load error', async () => {
    mocked.conversations.mockRejectedValue(new Error('history offline'));
    render(<AnalysisHistoryScreen {...props} />);
    await screen.findByText('history offline');
  });
});

describe('AnalysisResultScreen', () => {
  const props = {
    navigation,
    route: { key: 'Result', name: 'Result', params: { analysisID: 'a1', conversationID: 'conv1' } },
  } as unknown as React.ComponentProps<typeof AnalysisResultScreen>;

  beforeEach(() => jest.useFakeTimers());
  afterEach(() => jest.useRealTimers());

  const flush = () => act(async () => {});

  it('renders the completed result', async () => {
    mocked.analysisResult.mockResolvedValue(resultFixture());
    render(<AnalysisResultScreen {...props} />);
    await flush();

    expect(mocked.analysisResult).toHaveBeenCalledWith('a1');
    expect(screen.getByText('72')).toBeTruthy();
    expect(screen.getByText('Engaging')).toBeTruthy();
    expect(screen.getByText('Warm and curious.')).toBeTruthy();
    expect(screen.getByText('Asked a real question')).toBeTruthy();
    expect(screen.getByText('model v1.2')).toBeTruthy();
    expect(screen.getByText('Messages 1–3')).toBeTruthy();
    expect(screen.queryByText('A pattern we noticed')).toBeNull();
    expect(screen.queryByText('Worth asking yourself')).toBeNull();
  });

  it('renders patterns and reflection questions when the analyzer sends them', async () => {
    mocked.analysisResult.mockResolvedValue(
      resultFixture({
        overall: {
          ...resultFixture().overall!,
          patterns: ['You sent about twice as many messages as they did in this conversation (4 to 2).'],
          reflection_questions: ['Setting how they responded aside for a moment: did you actually enjoy this conversation?'],
        },
      }),
    );
    render(<AnalysisResultScreen {...props} />);
    await flush();

    expect(screen.getByText('A pattern we noticed')).toBeTruthy();
    expect(screen.getByText(/twice as many messages/)).toBeTruthy();
    expect(screen.getByText('Worth asking yourself')).toBeTruthy();
    expect(screen.getByText(/did you actually enjoy this conversation/)).toBeTruthy();
  });

  it('refetches when the push for this analysis arrives instead of polling', async () => {
    mocked.analysisResult
      .mockResolvedValueOnce(resultFixture({ status: 'pending', segments: null, overall: null }))
      .mockResolvedValueOnce(resultFixture());
    render(<AnalysisResultScreen {...props} />);
    await flush();
    expect(screen.getByText('Queued for analysis…')).toBeTruthy();

    // Nothing is fetched on the old 2s cadence while waiting for the push.
    await act(async () => {
      jest.advanceTimersByTime(10_000);
    });
    expect(mocked.analysisResult).toHaveBeenCalledTimes(1);

    // Another analysis settling is not this screen's business.
    await act(async () => pushEvent({ type: 'analysis_ready', analysis_id: 'other', conversation_id: 'c9' }));
    expect(mocked.analysisResult).toHaveBeenCalledTimes(1);

    await act(async () => pushEvent({ type: 'analysis_ready', analysis_id: 'a1', conversation_id: 'conv1' }));
    expect(mocked.analysisResult).toHaveBeenCalledTimes(2);
    expect(screen.getByText('Warm and curious.')).toBeTruthy();
  });

  it('shows the failed state when analysis_failed is pushed', async () => {
    mocked.analysisResult
      .mockResolvedValueOnce(resultFixture({ status: 'running', segments: null, overall: null }))
      .mockResolvedValueOnce(resultFixture({ status: 'failed', error: 'model crashed' }));
    render(<AnalysisResultScreen {...props} />);
    await flush();
    expect(screen.getByText('Scoring your conversation…')).toBeTruthy();

    await act(async () => pushEvent({ type: 'analysis_failed', analysis_id: 'a1', conversation_id: 'conv1' }));
    expect(screen.getByText('Analysis failed')).toBeTruthy();
    expect(screen.getByText('model crashed')).toBeTruthy();
  });

  it('refetches on every socket (re)connect, covering pushes it missed', async () => {
    mocked.analysisResult
      .mockResolvedValueOnce(resultFixture({ status: 'pending', segments: null, overall: null }))
      .mockResolvedValueOnce(resultFixture());
    render(<AnalysisResultScreen {...props} />);
    await flush();

    await act(async () => pushEvent({ type: 'ready' }));
    expect(mocked.analysisResult).toHaveBeenCalledTimes(2);
    expect(screen.getByText('Warm and curious.')).toBeTruthy();
  });

  it('ignores reconnects once the result has settled, so a flaky network cannot replace it', async () => {
    mocked.analysisResult.mockResolvedValue(resultFixture());
    render(<AnalysisResultScreen {...props} />);
    await flush();

    await act(async () => pushEvent({ type: 'ready' }));
    await act(async () => pushEvent({ type: 'analysis_ready', analysis_id: 'a1' }));
    expect(mocked.analysisResult).toHaveBeenCalledTimes(1);
    expect(screen.getByText('Warm and curious.')).toBeTruthy();
  });

  it('keeps a 30s fallback poll until it settles, without doubling up on pushes', async () => {
    mocked.analysisResult
      .mockResolvedValueOnce(resultFixture({ status: 'pending', segments: null, overall: null }))
      .mockResolvedValueOnce(resultFixture({ status: 'running', segments: null, overall: null }))
      .mockResolvedValueOnce(resultFixture({ status: 'running', segments: null, overall: null }))
      .mockResolvedValueOnce(resultFixture());
    render(<AnalysisResultScreen {...props} />);
    await flush();

    await act(async () => {
      jest.advanceTimersByTime(30_000);
    });
    expect(screen.getByText('Scoring your conversation…')).toBeTruthy();
    expect(mocked.analysisResult).toHaveBeenCalledTimes(2);

    // A reconnect refetch restarts the fallback timer rather than adding one.
    await act(async () => pushEvent({ type: 'ready' }));
    expect(mocked.analysisResult).toHaveBeenCalledTimes(3);
    await act(async () => {
      jest.advanceTimersByTime(29_999);
    });
    expect(mocked.analysisResult).toHaveBeenCalledTimes(3);
    await act(async () => {
      jest.advanceTimersByTime(1);
    });
    expect(mocked.analysisResult).toHaveBeenCalledTimes(4);
    expect(screen.getByText('Warm and curious.')).toBeTruthy();

    await act(async () => {
      jest.advanceTimersByTime(120_000);
    });
    expect(mocked.analysisResult).toHaveBeenCalledTimes(4);
  });

  it('follows a retried analysis id with the same socket, and closes it on unmount', async () => {
    mocked.analysisResult.mockResolvedValueOnce(resultFixture({ status: 'failed', error: 'model crashed' }));
    mocked.reanalyzeConversation.mockResolvedValue(resultFixture({ id: 'a2', status: 'pending', segments: null, overall: null }));
    mocked.analysisResult.mockResolvedValueOnce(resultFixture({ id: 'a2', status: 'pending', segments: null, overall: null }));
    const view = render(<AnalysisResultScreen {...props} />);
    await flush();
    fireEvent.press(screen.getByText('Try again'));
    await flush();
    await flush();
    const calls = mocked.analysisResult.mock.calls.length;

    // The old id's events no longer apply; the new one's do.
    await act(async () => pushEvent({ type: 'analysis_ready', analysis_id: 'a1', conversation_id: 'conv1' }));
    expect(mocked.analysisResult).toHaveBeenCalledTimes(calls);
    mocked.analysisResult.mockResolvedValueOnce(resultFixture({ id: 'a2' }));
    await act(async () => pushEvent({ type: 'analysis_ready', analysis_id: 'a2', conversation_id: 'conv1' }));
    expect(mocked.analysisResult).toHaveBeenLastCalledWith('a2');
    expect(screen.getByText('Warm and curious.')).toBeTruthy();
    expect(SocketMock).toHaveBeenCalledTimes(1);

    view.unmount();
    expect(socketClose).toHaveBeenCalled();
  });

  it('gives up after 5 poll failures with backoff, and "Try again" resets and re-polls', async () => {
    mocked.analysisResult.mockRejectedValue(new Error('gateway timeout'));
    render(<AnalysisResultScreen {...props} />);
    await flush();
    // failures 1..4 schedule retries at 2s, 4s, 6s, 8s
    for (let i = 1; i <= 4; i += 1) {
      expect(screen.queryByText('gateway timeout')).toBeNull();
      await act(async () => {
        jest.advanceTimersByTime(2000 * i);
      });
    }
    expect(mocked.analysisResult).toHaveBeenCalledTimes(5);
    expect(screen.getByText('gateway timeout')).toBeTruthy();
    expect(screen.getByText('We lost the thread')).toBeTruthy();

    mocked.analysisResult.mockResolvedValue(resultFixture());
    fireEvent.press(screen.getByText('Try again'));
    await flush();
    expect(screen.queryByText('We lost the thread')).toBeNull();
    expect(screen.getByText('Warm and curious.')).toBeTruthy();
  });

  it('a failed analysis offers "Try again", which re-queues and follows the new analysis id', async () => {
    mocked.analysisResult.mockResolvedValueOnce(resultFixture({ status: 'failed', error: 'model crashed' }));
    mocked.reanalyzeConversation.mockResolvedValue(resultFixture({ id: 'a2', status: 'pending', segments: null, overall: null }));
    mocked.analysisResult.mockResolvedValue(resultFixture({ id: 'a2' }));
    render(<AnalysisResultScreen {...props} />);
    await flush();
    expect(screen.getByText('Analysis failed')).toBeTruthy();
    expect(screen.getByText('model crashed')).toBeTruthy();

    fireEvent.press(screen.getByText('Try again'));
    await flush();
    expect(mocked.reanalyzeConversation).toHaveBeenCalledWith('conv1');
    await flush();
    expect(mocked.analysisResult).toHaveBeenLastCalledWith('a2');
    expect(screen.getByText('Warm and curious.')).toBeTruthy();
  });

  it('re-queue failure shows the error and keeps the failed card', async () => {
    mocked.analysisResult.mockResolvedValue(resultFixture({ status: 'failed', error: 'model crashed' }));
    mocked.reanalyzeConversation.mockRejectedValue(new Error('queue full'));
    render(<AnalysisResultScreen {...props} />);
    await flush();
    fireEvent.press(screen.getByText('Try again'));
    await flush();
    expect(screen.getByText('queue full')).toBeTruthy();
    expect(screen.getByText('Analysis failed')).toBeTruthy();
  });

  it('labelling the outcome posts the consented label and shows the thanks/failure copy', async () => {
    mocked.analysisResult.mockResolvedValue(resultFixture());
    mocked.labelConversation.mockResolvedValueOnce(undefined as never).mockRejectedValueOnce(new Error('label failed'));
    render(<AnalysisResultScreen {...props} />);
    await flush();

    fireEvent.press(screen.getByText('Ghosted'));
    await flush();
    expect(mocked.labelConversation).toHaveBeenCalledWith('conv1', { outcome: 'ghosted', reply_received: false, consented: true });
    expect(screen.getByText('Thanks — this helps train the model.')).toBeTruthy();

    fireEvent.press(screen.getByText('Date set'));
    await flush();
    expect(mocked.labelConversation).toHaveBeenLastCalledWith('conv1', { outcome: 'date_set', reply_received: true, consented: true });
    expect(screen.getByText('label failed')).toBeTruthy();
  });
});
