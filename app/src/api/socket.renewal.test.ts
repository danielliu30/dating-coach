import { API_BASE_URL, API_PREFIX } from '../config';
import { ApiError, api } from './client';
import { ChatSocket } from './socket';

// Exercises ChatSocket against the real ApiClient, so the thread probe's own
// HTTP handling is part of what is measured, not mocked away.

type FetchMock = jest.MockedFunction<typeof fetch>;

const reply = (status: number, body?: unknown): Response =>
  ({
    ok: status >= 200 && status < 300,
    status,
    text: async () => (body === undefined ? '' : JSON.stringify(body)),
  }) as Response;

class FakeSocket {
  static instances: FakeSocket[] = [];
  static readonly OPEN = 1;

  readyState = 0;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: ((event: { code: number }) => void) | null = null;
  onerror: (() => void) | null = null;
  send = jest.fn();
  close = jest.fn();

  constructor(readonly url: string) {
    FakeSocket.instances.push(this);
  }

  serverClose(code = 1006) {
    this.readyState = 3;
    this.onclose?.({ code });
  }
}

const latest = () => {
  const socket = FakeSocket.instances.at(-1);
  if (!socket) throw new Error('no socket was created');
  return socket;
};

/** Lets the reconnect scheduling, which awaits fetches, settle. */
const flush = async () => {
  for (let i = 0; i < 50; i += 1) await Promise.resolve();
};

let fetchMock: FetchMock;
const calls = (path: string) =>
  fetchMock.mock.calls.filter(([url]) => String(url).startsWith(`${API_BASE_URL}${API_PREFIX}${path}`)).length;

beforeEach(() => {
  jest.useFakeTimers();
  FakeSocket.instances = [];
  globalThis.WebSocket = FakeSocket as unknown as typeof WebSocket;
  fetchMock = jest.fn(async (url: RequestInfo | URL) => {
    if (String(url).includes('/auth/ws-ticket')) return reply(200, { ticket: 'tkt' });
    return String(url).includes('/auth/refresh') ? reply(503, { error: 'unavailable' }) : reply(401, { error: 'expired' });
  }) as FetchMock;
  globalThis.fetch = fetchMock;
  api.useToken(() => 'expired');
  // Mirrors rotate in state/auth.tsx: only a refused refresh token is a verdict.
  api.useRenewal(async () => {
    try {
      await api.refreshSession('refresh');
      return 'renewed';
    } catch (err) {
      return err instanceof ApiError && err.status === 401 ? 'rejected' : 'unavailable';
    }
  });
});

afterEach(() => {
  jest.useRealTimers();
});

describe('ChatSocket with the real ApiClient', () => {
  it('renews an expired token only on scheduled failures while refresh is unavailable', async () => {
    const socket = new ChatSocket('t1', () => 'expired', { onEvent: jest.fn() });
    await socket.connect();

    const refreshes: number[] = [];
    const probes: number[] = [];
    for (let failure = 1; failure <= 13; failure += 1) {
      latest().serverClose();
      await flush();
      refreshes.push(calls('/auth/refresh'));
      probes.push(calls('/chat/threads/t1/messages'));
      jest.advanceTimersByTime(10_000);
      await flush();
      expect(FakeSocket.instances).toHaveLength(failure + 1);
    }

    // One refresh, and one probe that never refreshes, on failures 1, 7 and 13.
    expect(refreshes).toEqual([1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2, 3]);
    expect(probes).toEqual(refreshes);
    socket.close();
  });
});
