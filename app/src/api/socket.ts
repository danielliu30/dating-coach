import { ApiError, api } from './client';
import { wsURL } from '../config';
import type { ChatEvent } from './types';

/**
 * Failed handshakes between renewals. A handshake carries no status code, so
 * an expired token and a thread the server will never serve look identical;
 * spacing renewals bounds what the second case costs, while the first still
 * renews on its first failure. Counted in attempts rather than wall-clock
 * time, which a device is free to move backwards. At the 10s backoff ceiling
 * this is roughly one renewal a minute.
 */
const attemptsPerRenewal = 6;

/**
 * Consecutive attempts without the socket ever opening before the retries stop.
 * A backstop for failures the thread probe below cannot classify; at the 10s
 * backoff ceiling this is a few minutes of retrying.
 */
const maxAttempts = 30;

// HTTP statuses for a thread the server will never serve again.
const GONE = new Set([404, 410]);

// Close code the API uses when it drops a socket whose session it will not serve
// any more, whether the account was revoked or the token behind it expired.
const POLICY_VIOLATION = 1008;

interface Handlers {
  onEvent: (event: ChatEvent) => void;
  onStatus?: (status: 'connecting' | 'open' | 'closed') => void;
}

/**
 * Thin WebSocket wrapper for a chat thread. Reconnects with linear backoff so a
 * backgrounded phone or a redeployed API does not silently break the chat, and
 * gives up once the thread is gone or after maxAttempts failed tries.
 *
 * The access token is read per attempt rather than captured once: it lives for
 * minutes, and the handshake carries it, so a socket holding the value it was
 * built with would reconnect on an expired credential forever.
 */
export class ChatSocket {
  private socket: WebSocket | null = null;
  private attempts = 0;
  private closed = false;
  /** Set when the retries gave up rather than the caller closing the socket. */
  private ended = false;
  private opened = false;
  private retry: ReturnType<typeof setTimeout> | null = null;
  private failures = 0;
  private outbox: ChatEvent[] = [];

  constructor(
    private readonly threadID: string,
    private readonly token: () => string | null,
    private readonly handlers: Handlers,
  ) {}

  connect(): void {
    if (this.closed) return;
    const token = this.token();
    if (!token) {
      this.handlers.onStatus?.('closed');
      return;
    }
    this.handlers.onStatus?.('connecting');

    const socket = new WebSocket(wsURL(`/chat/threads/${this.threadID}/ws`, token));
    this.socket = socket;

    socket.onopen = () => {
      this.attempts = 0;
      this.opened = true;
      this.failures = 0;
      this.handlers.onStatus?.('open');
      this.flush();
    };
    socket.onmessage = (event) => {
      try {
        this.handlers.onEvent(JSON.parse(String(event.data)) as ChatEvent);
      } catch {
        // ignore malformed frames
      }
    };
    socket.onclose = (event) => {
      const unopened = !this.opened;
      this.opened = false;
      this.handlers.onStatus?.('closed');
      void this.scheduleReconnect(unopened, event.code === POLICY_VIOLATION);
    };
    socket.onerror = () => socket.close();
  }

  /**
   * Queues the next attempt. A handshake that closed without ever opening may
   * be an expired access token, and no HTTP 401 exists here to renew it, so the
   * shared renewal runs first. Only a refused refresh token ends the retries; a
   * renewal that merely could not be reached is treated like any other outage
   * and retried with backoff. A thread the probe reports gone, or maxAttempts
   * attempts in a row, also end them.
   *
   * Renewals are spaced every attemptsPerRenewal failures, so a handshake the
   * server refuses for its own reasons cannot turn the retry loop into a
   * refresh-token mill. refused lifts that spacing: the API closed the socket
   * itself because it will not serve the session again, which is either an
   * expired token, renewable straight away, or a revoked account, whose renewal
   * is refused and signs the user out rather than reconnecting forever.
   */
  private async scheduleReconnect(unopened: boolean, refused: boolean): Promise<void> {
    if (this.closed) return;
    if (unopened || refused) {
      const due = refused || this.failures % attemptsPerRenewal === 0;
      if (!refused) this.failures += 1;
      if (due && (await api.renewSession()) === 'rejected') {
        this.close();
        return;
      }
    }
    if (this.closed) return;
    if (unopened && (await this.threadGone())) {
      this.end();
      return;
    }
    if (this.closed) return;
    if (this.attempts >= maxAttempts) {
      this.end();
      return;
    }
    this.attempts += 1;
    const delay = Math.min(1000 * this.attempts, 10_000);
    this.retry = setTimeout(() => this.connect(), delay);
  }

  /**
   * A failed handshake hides its HTTP status, so a thread that is gone (and
   * whose socket route 404s) looks like an outage. The thread's history uses
   * the same lookup as the socket route, so its status stands in for the
   * handshake's. Anything other than a definite 404/410 is retried as before.
   */
  private async threadGone(): Promise<boolean> {
    try {
      await api.threadMessages(this.threadID, 1);
      return false;
    } catch (err) {
      return err instanceof ApiError && GONE.has(err.status);
    }
  }

  /**
   * Gives up on the thread. Queued messages, and any sent afterwards, are
   * rejected like a server-refused send so the caller does not keep them
   * pending for a reconnect that will never come.
   */
  private end(): void {
    const pending = this.outbox;
    this.close();
    this.ended = true;
    this.handlers.onStatus?.('closed');
    for (const event of pending) this.reject(event);
  }

  private reject(event: ChatEvent): void {
    this.handlers.onEvent({ type: 'error', body: 'chat is no longer available', client_id: event.client_id });
  }

  /**
   * Sends a chat message, or queues it while the socket is down. clientID, when
   * given, rides on the frame and comes back on the server's echo or rejection
   * of this exact send, so the caller can match either to what it rendered.
   */
  send(body: string, clientID?: string): void {
    this.emit(clientID === undefined ? { type: 'message', body } : { type: 'message', body, client_id: clientID });
  }

  typing(typing: boolean): void {
    this.emit({ type: 'typing', typing });
  }

  /**
   * Messages typed while the socket is down are held and replayed on reconnect;
   * transient events (typing) are dropped because they are stale by then.
   */
  private emit(event: ChatEvent): void {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(event));
      return;
    }
    if (event.type !== 'message') return;
    if (this.ended) this.reject(event);
    else this.outbox.push(event);
  }

  private flush(): void {
    const pending = this.outbox;
    this.outbox = [];
    for (const event of pending) this.emit(event);
  }

  close(): void {
    this.closed = true;
    this.outbox = [];
    if (this.retry) clearTimeout(this.retry);
    this.socket?.close();
    this.socket = null;
  }
}
