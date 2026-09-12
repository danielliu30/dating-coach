import { wsURL } from '../config';
import { api } from './client';
import { ChatSocket } from './socket';
import type { ChatEvent } from './types';

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
  onclose: ((event: { code: number }) => void) | null = null;
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
  serverClose(code = 1006) {
    this.readyState = FakeSocket.CLOSED;
    this.onclose?.({ code });
  }

  receive(event: ChatEvent) {
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

describe('ChatSocket', () => {
  it('connects to the thread URL with the current token and reports status', () => {
    const onStatus = jest.fn();
    const socket = new ChatSocket('t1', () => 'tok', { onEvent: jest.fn(), onStatus });

    socket.connect();

    expect(latest().url).toBe(wsURL('/chat/threads/t1/ws', 'tok'));
    expect(onStatus).toHaveBeenLastCalledWith('connecting');
    latest().open();
    expect(onStatus).toHaveBeenLastCalledWith('open');
  });

  it('reports closed and does not open a socket without a token', () => {
    const onStatus = jest.fn();
    new ChatSocket('t1', () => null, { onEvent: jest.fn(), onStatus }).connect();

    expect(FakeSocket.instances).toHaveLength(0);
    expect(onStatus).toHaveBeenCalledWith('closed');
  });

  it('forwards parsed events and ignores malformed frames', () => {
    const onEvent = jest.fn();
    new ChatSocket('t1', () => 'tok', { onEvent }).connect();
    latest().open();

    latest().receive({ type: 'typing', typing: true });
    latest().onmessage?.({ data: 'not json' });

    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(onEvent).toHaveBeenCalledWith({ type: 'typing', typing: true });
  });

  it('backs off linearly, capped at 10s, and resets attempts once a connection opens', async () => {
    const socket = new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() });
    socket.connect();

    for (let attempt = 1; attempt <= 12; attempt += 1) {
      latest().serverClose();
      await flush();
      const expected = Math.min(1000 * attempt, 10_000);
      jest.advanceTimersByTime(expected - 1);
      expect(FakeSocket.instances).toHaveLength(attempt);
      jest.advanceTimersByTime(1);
      expect(FakeSocket.instances).toHaveLength(attempt + 1);
    }

    // Opening resets the counter: the next retry is back to 1s.
    latest().open();
    latest().serverClose();
    await flush();
    const before = FakeSocket.instances.length;
    jest.advanceTimersByTime(999);
    expect(FakeSocket.instances).toHaveLength(before);
    jest.advanceTimersByTime(1);
    expect(FakeSocket.instances).toHaveLength(before + 1);
  });

  it('renews the session before retrying when the handshake never opened', async () => {
    new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() }).connect();

    latest().serverClose();
    await flush();
    expect(renewSession).toHaveBeenCalledTimes(1);
    jest.advanceTimersByTime(1000);
    expect(FakeSocket.instances).toHaveLength(2);
  });

  it('spaces renewals every 6 unopened failures', async () => {
    new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() }).connect();

    for (let failure = 1; failure <= 13; failure += 1) {
      latest().serverClose();
      await flush();
      jest.advanceTimersByTime(10_000);
    }
    // Failures 1, 7 and 13 are due for renewal.
    expect(renewSession).toHaveBeenCalledTimes(3);
  });

  it('close code 1008 forces a renewal even after the socket had opened', async () => {
    new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() }).connect();
    latest().open();

    latest().serverClose(1008);
    await flush();
    expect(renewSession).toHaveBeenCalledTimes(1);

    // And again straight away: refused closes are not subject to the spacing.
    jest.advanceTimersByTime(1000);
    latest().open();
    latest().serverClose(1008);
    await flush();
    expect(renewSession).toHaveBeenCalledTimes(2);
  });

  it('stops retrying when the renewal is rejected', async () => {
    renewSession.mockResolvedValue('rejected');
    const onStatus = jest.fn();
    const socket = new ChatSocket('t1', () => 'tok', { onEvent: jest.fn(), onStatus });
    socket.connect();

    latest().serverClose();
    await flush();
    jest.advanceTimersByTime(60_000);

    expect(FakeSocket.instances).toHaveLength(1);
    socket.send('late');
    expect(latest().send).not.toHaveBeenCalled();
  });

  it('retries with backoff when the renewal was merely unavailable', async () => {
    renewSession.mockResolvedValue('unavailable');
    new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() }).connect();

    latest().serverClose();
    await flush();
    jest.advanceTimersByTime(1000);
    expect(FakeSocket.instances).toHaveLength(2);
  });

  it('queues messages while down and replays them on reconnect, dropping typing events', async () => {
    const socket = new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() });
    socket.connect();
    latest().open();
    latest().serverClose();
    await flush();

    socket.send('hello');
    socket.typing(true);
    socket.send('again');

    jest.advanceTimersByTime(1000);
    const reconnected = latest();
    expect(reconnected.send).not.toHaveBeenCalled();
    reconnected.open();

    expect(reconnected.send.mock.calls.map(([frame]) => JSON.parse(String(frame)))).toEqual([
      { type: 'message', body: 'hello' },
      { type: 'message', body: 'again' },
    ]);
  });

  it('sends immediately while open', () => {
    const socket = new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() });
    socket.connect();
    latest().open();

    socket.send('hi');
    socket.typing(false);

    expect(latest().send.mock.calls.map(([frame]) => JSON.parse(String(frame)))).toEqual([
      { type: 'message', body: 'hi' },
      { type: 'typing', typing: false },
    ]);
  });

  it('close() cancels the pending retry, empties the outbox and never reconnects', async () => {
    const socket = new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() });
    socket.connect();
    latest().open();
    latest().serverClose();
    await flush();
    socket.send('lost');

    socket.close();
    jest.advanceTimersByTime(60_000);
    expect(FakeSocket.instances).toHaveLength(1);

    socket.connect();
    expect(FakeSocket.instances).toHaveLength(1);
  });

  it('reads the token per attempt so a renewed token is used on reconnect', async () => {
    let token = 'first';
    new ChatSocket('t1', () => token, { onEvent: jest.fn() }).connect();
    latest().open();
    token = 'second';
    latest().serverClose();
    await flush();
    jest.advanceTimersByTime(1000);

    expect(latest().url).toBe(wsURL('/chat/threads/t1/ws', 'second'));
  });

  it('an error event closes the underlying socket', () => {
    new ChatSocket('t1', () => 'tok', { onEvent: jest.fn() }).connect();
    latest().onerror?.();
    expect(latest().close).toHaveBeenCalled();
  });
});
