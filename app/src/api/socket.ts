import { wsURL } from '../config';
import type { ChatEvent } from './types';

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
    };
    socket.onmessage = (event) => {
      try {
        this.handlers.onEvent(JSON.parse(String(event.data)) as ChatEvent);
      } catch {
        // ignore malformed frames
      }
    };
    socket.onclose = () => {
      this.handlers.onStatus?.('closed');
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

  private emit(event: ChatEvent): void {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify(event));
    }
  }

  close(): void {
    this.closed = true;
    if (this.retry) clearTimeout(this.retry);
    this.socket?.close();
    this.socket = null;
  }
}
