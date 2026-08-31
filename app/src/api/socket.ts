import { api } from './client';
import { wsURL } from '../config';
import type { ChatEvent } from './types';

/**
 * Reads a JWT's `exp` claim as milliseconds since the epoch, or null when the
 * token is not a readable JWT. Nothing is verified here — the signature is the
 * server's business; this only asks when the server will stop accepting it.
 */
function expiryOf(token: string): number | null {
  const payload = token.split('.')[1];
  if (!payload) return null;
  try {
    const claims = JSON.parse(atob(payload.replace(/-/g, '+').replace(/_/g, '/'))) as {
      exp?: number;
    };
    return typeof claims.exp === 'number' ? claims.exp * 1000 : null;
  } catch {
    return null;
  }
}

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
  private renewed = false;
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
      this.renewed = false;
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
    socket.onclose = () => {
      const unopened = !this.opened;
      this.opened = false;
      this.handlers.onStatus?.('closed');
      void this.scheduleReconnect(unopened ? token : null);
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
   * used is the token the failed handshake carried, or null if the socket had
   * opened; renewal is asked for only once that token is spent, so a handshake
   * the server refuses for its own reasons — a thread that is gone, or not the
   * caller's — retries on backoff instead of rotating a fresh refresh token
   * every time.
   */
  private async scheduleReconnect(used: string | null): Promise<void> {
    if (this.closed) return;
    if (used !== null && this.spent(used)) {
      const result = await api.renewSession();
      if (result === 'rejected') {
        this.close();
        return;
      }
      // Only a renewal that happened spends the fallback's single attempt: an
      // unreachable backend must not strand the socket on a dead token.
      if (result === 'renewed') this.renewed = true;
    }
    if (this.closed) return;
    this.attempts += 1;
    const delay = Math.min(1000 * this.attempts, 10_000);
    this.retry = setTimeout(() => this.connect(), delay);
  }

  /**
   * Whether token has run out and is worth renewing. A token whose expiry
   * cannot be read falls back to one renewal per connected run, which is the
   * most that can be justified without knowing when it dies.
   */
  private spent(token: string): boolean {
    const expiry = expiryOf(token);
    return expiry === null ? !this.renewed : expiry <= Date.now();
  }

  send(body: string): void {
    this.emit({ type: 'message', body });
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
