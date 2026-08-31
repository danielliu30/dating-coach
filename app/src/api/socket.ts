import { wsURL } from '../config';
import { api } from './client';
import type { ChatEvent } from './types';

// Close code the API uses when it drops a socket whose account was revoked.
const POLICY_VIOLATION = 1008;

interface Handlers {
  onEvent: (event: ChatEvent) => void;
  onStatus?: (status: 'connecting' | 'open' | 'closed') => void;
}

/**
 * Thin WebSocket wrapper for a chat thread. Reconnects with linear backoff so a
 * backgrounded phone or a redeployed API does not silently break the chat.
 */
export class ChatSocket {
  private socket: WebSocket | null = null;
  private attempts = 0;
  private closed = false;
  private retry: ReturnType<typeof setTimeout> | null = null;
  private outbox: ChatEvent[] = [];

  constructor(
    private readonly threadID: string,
    private readonly token: string,
    private readonly handlers: Handlers,
  ) {}

  connect(): void {
    if (this.closed) return;
    this.handlers.onStatus?.('connecting');

    const socket = new WebSocket(wsURL(`/chat/threads/${this.threadID}/ws`, this.token));
    this.socket = socket;

    socket.onopen = () => {
      this.attempts = 0;
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
      this.handlers.onStatus?.('closed');
      // A revoked session is not a transient failure: reconnecting would replay
      // the same rejected token every ten seconds forever, so the session is
      // dropped through the handler a 401 would use instead.
      if (event.code === POLICY_VIOLATION) {
        this.closed = true;
        this.outbox = [];
        api.rejectSession(this.token);
        return;
      }
      this.scheduleReconnect();
    };
    socket.onerror = () => socket.close();
  }

  private scheduleReconnect(): void {
    if (this.closed) return;
    this.attempts += 1;
    const delay = Math.min(1000 * this.attempts, 10_000);
    this.retry = setTimeout(() => this.connect(), delay);
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
