import { api, type SessionEndReason } from './client';
import { wsURL } from '../config';
import type { NotificationEvent } from './types';

// Same spacing and close code as the chat socket; see socket.ts.
const attemptsPerRenewal = 6;
const POLICY_VIOLATION = 1008;

interface Handlers {
  onEvent: (event: NotificationEvent) => void;
  onStatus?: (status: 'connecting' | 'open' | 'closed') => void;
}

/**
 * The signed-in user's notifications socket (`/notifications/ws`), which the
 * API pushes analysis completions on. Delivery is best effort, so callers treat
 * every `ready` frame (sent on each successful connect) as a cue to refetch
 * whatever they are waiting on.
 *
 * Reconnects with the chat socket's rules: linear backoff capped at 10s, the
 * token read per attempt, and a session renewal before retrying a handshake
 * that never opened or a socket the API closed as expired or revoked.
 */
export class NotificationSocket {
  private socket: WebSocket | null = null;
  private attempts = 0;
  private closed = false;
  private opened = false;
  private retry: ReturnType<typeof setTimeout> | null = null;
  private failures = 0;

  constructor(
    private readonly token: () => string | null,
    private readonly handlers: Handlers,
  ) {}

  /** Opens the socket; a no-op after close(), while one is open, or without a token. */
  connect(): void {
    if (this.closed || this.socket) return;
    const token = this.token();
    if (!token) {
      this.handlers.onStatus?.('closed');
      return;
    }
    this.handlers.onStatus?.('connecting');

    const socket = new WebSocket(wsURL('/notifications/ws', token));
    this.socket = socket;

    socket.onopen = () => {
      this.attempts = 0;
      this.opened = true;
      this.failures = 0;
      this.handlers.onStatus?.('open');
    };
    socket.onmessage = (event) => {
      if (socket !== this.socket) return;
      try {
        this.handlers.onEvent(JSON.parse(String(event.data)) as NotificationEvent);
      } catch {
        // ignore malformed frames
      }
    };
    socket.onclose = (event) => {
      if (socket !== this.socket) return;
      this.socket = null;
      const unopened = !this.opened;
      this.opened = false;
      this.handlers.onStatus?.('closed');
      const refused = event.code !== POLICY_VIOLATION ? null : event.reason === 'session revoked' ? 'revoked' : 'expired';
      void this.scheduleReconnect(unopened, refused);
    };
    socket.onerror = () => socket.close();
  }

  /**
   * Queues the next attempt, renewing the session first when the handshake
   * never opened (spaced every attemptsPerRenewal failures) or the API refused
   * the session (always), passing on whether it said the session expired or
   * was revoked so a refused renewal tells the user which. A rejected renewal
   * means the user is signed out, so the socket stops for good.
   */
  private async scheduleReconnect(unopened: boolean, refused: SessionEndReason | null): Promise<void> {
    if (this.closed) return;
    if (unopened || refused) {
      const due = refused !== null || this.failures % attemptsPerRenewal === 0;
      if (!refused) this.failures += 1;
      if (due && (await api.renewSession(refused ?? 'expired')) === 'rejected') {
        this.close();
        return;
      }
    }
    if (this.closed) return;
    this.attempts += 1;
    const delay = Math.min(1000 * this.attempts, 10_000);
    this.retry = setTimeout(() => this.connect(), delay);
  }

  /** Closes the socket and cancels any pending reconnect, permanently. */
  close(): void {
    this.closed = true;
    if (this.retry) clearTimeout(this.retry);
    const socket = this.socket;
    this.socket = null;
    socket?.close();
  }
}
