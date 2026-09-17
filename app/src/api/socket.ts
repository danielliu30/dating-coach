import { api } from './client';
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

// Close code the API uses when it drops a socket whose session it will not serve
// any more, whether the account was revoked or the token behind it expired.
const POLICY_VIOLATION = 1008;

interface Handlers {
  onEvent: (event: ChatEvent) => void;
  onStatus?: (status: 'connecting' | 'open' | 'closed') => void;
}

/**
 * Thin WebSocket wrapper for a chat thread. Reconnects with linear backoff so a
 * backgrounded phone or a redeployed API does not silently break the chat.
 *
 * The access token is read per attempt rather than captured once: it lives for
 * minutes, and the handshake carries it, so a socket holding the value it was
 * built with would reconnect on an expired credential forever.
 */
export class ChatSocket {
  private socket: WebSocket | null = null;
  private attempts = 0;
  private closed = false;
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
   * and retried with backoff.
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
    this.attempts += 1;
    const delay = Math.min(1000 * this.attempts, 10_000);
    this.retry = setTimeout(() => this.connect(), delay);
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
    if (event.type === 'message') this.outbox.push(event);
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
