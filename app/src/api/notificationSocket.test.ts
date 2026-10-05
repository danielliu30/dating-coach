import { wsURL } from '../config';
import { api } from './client';
import { NotificationSocket } from './notificationSocket';
import type { NotificationEvent } from './types';

jest.mock('./client', () => ({
  api: { renewSession: jest.fn() },
}));

const renewSession = api.renewSession as jest.MockedFunction<typeof api.renewSession>;

/** Stand-in for the browser WebSocket that lets a test drive open/message/close by hand. */
class FakeSocket {
  static instances: FakeSocket[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  readyState = FakeSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  send = jest.fn();
  close = jest.fn(() => {
    this.readyState = FakeSocket.CLOSED;
  });

  constructor(readonly url: string) {
    FakeSocket.instances.push(this);
  }

  open() {
    this.readyState = FakeSocket.OPEN;
    this.onopen?.();
  }

  /** Simulates the server closing the connection; `opened` sockets must call open() first. */
  serverClose(code = 1006, reason = '') {
    this.readyState = FakeSocket.CLOSED;
    this.onclose?.({ code, reason });
  }

  receive(event: NotificationEvent) {
    this.onmessage?.({ data: JSON.stringify(event) });
  }
}

const latest = () => {
  const socket = FakeSocket.instances.at(-1);
  if (!socket) throw new Error('no socket was created');
  return socket;
};

/** Lets the async reconnect scheduling (which awaits a renewal) settle. */
const flush = async () => {
  for (let i = 0; i < 5; i += 1) await Promise.resolve();
};

beforeEach(() => {
  jest.useFakeTimers();
  FakeSocket.instances = [];
  globalThis.WebSocket = FakeSocket as unknown as typeof WebSocket;
  renewSession.mockResolvedValue('renewed');
});

afterEach(() => {
  jest.useRealTimers();
});

describe('NotificationSocket', () => {
  const url = (token: string) => wsURL('/notifications/ws', token);

  it('connects with the current token and reports status', () => {
    const onStatus = jest.fn();
    new NotificationSocket(() => 'tok', { onEvent: jest.fn(), onStatus }).connect();

    expect(latest().url).toBe(url('tok'));
    expect(onStatus).toHaveBeenLastCalledWith('connecting');
    latest().open();
    expect(onStatus).toHaveBeenLastCalledWith('open');
  });

  it('does not open a socket without a token', () => {
    const onStatus = jest.fn();
    new NotificationSocket(() => null, { onEvent: jest.fn(), onStatus }).connect();

    expect(FakeSocket.instances).toHaveLength(0);
    expect(onStatus).toHaveBeenCalledWith('closed');
  });

  it('forwards parsed events and ignores malformed frames', () => {
    const onEvent = jest.fn();
    new NotificationSocket(() => 'tok', { onEvent }).connect();
    latest().open();

    latest().receive({ type: 'ready' });
    latest().onmessage?.({ data: 'not json' });
    latest().receive({ type: 'analysis_ready', analysis_id: 'a1', conversation_id: 'c1' });

    expect(onEvent.mock.calls).toEqual([
      [{ type: 'ready' }],
      [{ type: 'analysis_ready', analysis_id: 'a1', conversation_id: 'c1' }],
    ]);
  });

  it('backs off linearly, capped at 10s, and resets once a connection opens', async () => {
    new NotificationSocket(() => 'tok', { onEvent: jest.fn() }).connect();

    for (let attempt = 1; attempt <= 12; attempt += 1) {
      latest().serverClose();
      await flush();
      const expected = Math.min(1000 * attempt, 10_000);
      jest.advanceTimersByTime(expected - 1);
      expect(FakeSocket.instances).toHaveLength(attempt);
      jest.advanceTimersByTime(1);
      expect(FakeSocket.instances).toHaveLength(attempt + 1);
    }

    latest().open();
    latest().serverClose();
    await flush();
    const before = FakeSocket.instances.length;
    jest.advanceTimersByTime(999);
    expect(FakeSocket.instances).toHaveLength(before);
    jest.advanceTimersByTime(1);
    expect(FakeSocket.instances).toHaveLength(before + 1);
  });

  it('spaces renewals for handshakes that never opened every 6 failures', async () => {
    new NotificationSocket(() => 'tok', { onEvent: jest.fn() }).connect();

    for (let failure = 1; failure <= 13; failure += 1) {
      latest().serverClose();
      await flush();
      jest.advanceTimersByTime(10_000);
    }
    expect(renewSession).toHaveBeenCalledTimes(3);
  });

  it('renews then reconnects with the new token when the API closes the session as expired', async () => {
    let token = 'expired';
    renewSession.mockImplementation(async () => {
      token = 'renewed';
      return 'renewed';
    });
    new NotificationSocket(() => token, { onEvent: jest.fn() }).connect();
    latest().open();

    latest().serverClose(1008);
    await flush();
    expect(renewSession).toHaveBeenCalledTimes(1);
    jest.advanceTimersByTime(1000);
    expect(latest().url).toBe(url('renewed'));

    // Refused closes are not subject to the spacing.
    latest().open();
    latest().serverClose(1008);
    await flush();
    expect(renewSession).toHaveBeenCalledTimes(2);
  });

  it('stops for good when the renewal is rejected', async () => {
    renewSession.mockResolvedValue('rejected');
    new NotificationSocket(() => 'tok', { onEvent: jest.fn() }).connect();
    latest().open();

    latest().serverClose(1008);
    await flush();
    jest.advanceTimersByTime(60_000);
    expect(FakeSocket.instances).toHaveLength(1);
  });

  it('retries with backoff when the renewal was merely unavailable', async () => {
    renewSession.mockResolvedValue('unavailable');
    new NotificationSocket(() => 'tok', { onEvent: jest.fn() }).connect();

    latest().serverClose();
    await flush();
    jest.advanceTimersByTime(1000);
    expect(FakeSocket.instances).toHaveLength(2);
  });

  it('close() cancels the pending retry and never reconnects', async () => {
    const socket = new NotificationSocket(() => 'tok', { onEvent: jest.fn() });
    socket.connect();
    latest().open();
    latest().serverClose();
    await flush();

    socket.close();
    jest.advanceTimersByTime(60_000);
    expect(FakeSocket.instances).toHaveLength(1);
    socket.connect();
    expect(FakeSocket.instances).toHaveLength(1);
  });

  it('tells the renewal a revoked session apart from an expired one', async () => {
    const socket = new NotificationSocket(() => 'tok', { onEvent: jest.fn() });
    socket.connect();
    latest().open();
    latest().serverClose(1008, 'session revoked');
    await flush();
    expect(renewSession).toHaveBeenLastCalledWith('revoked');

    jest.advanceTimersByTime(1000);
    latest().open();
    latest().serverClose(1008, 'session expired');
    await flush();
    expect(renewSession).toHaveBeenLastCalledWith('expired');
    socket.close();
  });

  it('a second connect() while connected opens nothing, so close() leaves no socket behind', async () => {
    const onEvent = jest.fn();
    const socket = new NotificationSocket(() => 'tok', { onEvent });
    socket.connect();
    socket.connect();
    expect(FakeSocket.instances).toHaveLength(1);

    const only = latest();
    socket.close();
    expect(only.close).toHaveBeenCalled();
    only.serverClose();
    only.receive({ type: 'ready' });
    await flush();
    jest.advanceTimersByTime(30_000);
    expect(FakeSocket.instances).toHaveLength(1);
    expect(onEvent).not.toHaveBeenCalled();
  });

  it('an error event closes the underlying socket', () => {
    new NotificationSocket(() => 'tok', { onEvent: jest.fn() }).connect();
    latest().onerror?.();
    expect(latest().close).toHaveBeenCalled();
  });
});
