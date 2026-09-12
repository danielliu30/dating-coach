import { act, fireEvent, render, screen } from '@testing-library/react-native';
import React from 'react';

import type { ChatEvent } from '../api/types';
import ChatScreen from './ChatScreen';

type Handlers = { onEvent: (event: ChatEvent) => void; onStatus: (s: 'connecting' | 'open' | 'closed') => void };

const sockets: Array<{
  threadID: string;
  tokenProvider: () => string | null;
  handlers: Handlers;
  connect: jest.Mock;
  close: jest.Mock;
  send: jest.Mock;
  typing: jest.Mock;
}> = [];

jest.mock('../api/socket', () => ({
  ChatSocket: jest.fn().mockImplementation((threadID: string, tokenProvider: () => string | null, handlers: Handlers) => {
    const socket = {
      threadID,
      tokenProvider,
      handlers,
      connect: jest.fn(),
      close: jest.fn(),
      send: jest.fn(),
      typing: jest.fn(),
    };
    sockets.push(socket);
    return socket;
  }),
}));

const mockAuth = {
  token: 'tok' as string | null,
  user: { id: 'me', role: 'user' } as { id: string; role: string } | null,
};
jest.mock('../state/auth', () => ({ useAuth: () => mockAuth }));

jest.mock('react-native-gifted-chat', () => require('./giftedChatStub.testutil').giftedChatStub);

const navigation = { setOptions: jest.fn() };
const props = {
  navigation,
  route: { key: 'Chat', name: 'Chat', params: { threadID: 't1', title: 'Casey Coach' } },
} as unknown as React.ComponentProps<typeof ChatScreen>;

const latest = () => sockets[sockets.length - 1];
const emit = (event: ChatEvent) => act(() => latest().handlers.onEvent(event));
const status = (s: 'connecting' | 'open' | 'closed') => act(() => latest().handlers.onStatus(s));

beforeEach(() => {
  sockets.length = 0;
  mockAuth.token = 'tok';
  mockAuth.user = { id: 'me', role: 'user' };
});

describe('ChatScreen socket lifecycle', () => {
  it('connects to the route thread when signed in and closes on unmount', () => {
    const view = render(<ChatScreen {...props} />);
    expect(sockets).toHaveLength(1);
    expect(latest().threadID).toBe('t1');
    expect(latest().tokenProvider()).toBe('tok');
    expect(latest().connect).toHaveBeenCalledTimes(1);
    expect(navigation.setOptions).toHaveBeenCalledWith({ title: 'Casey Coach' });

    view.unmount();
    expect(latest().close).toHaveBeenCalledTimes(1);
  });

  it('does not open a socket while signed out', () => {
    mockAuth.token = null;
    render(<ChatScreen {...props} />);
    expect(sockets).toHaveLength(0);
  });

  it('hands the socket the latest token without reconnecting', () => {
    const view = render(<ChatScreen {...props} />);
    mockAuth.token = 'rotated';
    view.rerender(<ChatScreen {...props} />);
    expect(sockets).toHaveLength(1);
    expect(latest().tokenProvider()).toBe('rotated');
  });

  it('maps connection status to the header copy', () => {
    render(<ChatScreen {...props} />);
    expect(screen.getByText('Connecting…')).toBeTruthy();
    status('open');
    expect(screen.getByText('Connected')).toBeTruthy();
    status('closed');
    expect(screen.getByText('Reconnecting…')).toBeTruthy();
  });
});

describe('ChatScreen messages', () => {
  it('replaces the list with history and appends new messages', () => {
    render(<ChatScreen {...props} />);
    expect(screen.getByText('Say hello')).toBeTruthy();
    emit({
      type: 'history',
      messages: [{ id: 'm1', thread_id: 't1', sender_id: 'c1', body: 'Hi there', created_at: '2030-01-01T00:00:00Z' }],
    });
    expect(screen.getByText('c1: Hi there')).toBeTruthy();
    expect(screen.queryByText('Say hello')).toBeNull();

    emit({ type: 'message', message_id: 'm2', sender_id: 'me', body: 'Hello!' });
    expect(screen.getByText('me: Hello!')).toBeTruthy();
    expect(screen.getByText('c1: Hi there')).toBeTruthy();
  });

  it('drops duplicate message events with the same message_id', () => {
    render(<ChatScreen {...props} />);
    emit({ type: 'message', message_id: 'm2', sender_id: 'c1', body: 'once' });
    emit({ type: 'message', message_id: 'm2', sender_id: 'c1', body: 'once' });
    emit({ type: 'message', sender_id: 'c1', body: 'no id' });
    expect(screen.getAllByText('c1: once')).toHaveLength(1);
    expect(screen.queryByText('c1: no id')).toBeNull();
  });

  it('sends each outgoing message over the socket and clears typing', () => {
    render(<ChatScreen {...props} />);
    fireEvent.changeText(screen.getByPlaceholderText('Message your coach'), 'hey');
    expect(latest().typing).toHaveBeenLastCalledWith(true);
    fireEvent.press(screen.getByText('Send'));
    expect(latest().send).toHaveBeenCalledWith('hey');
    expect(latest().typing).toHaveBeenLastCalledWith(false);
  });

  it('shows Typing… only for the peer, never for own typing echoes', () => {
    render(<ChatScreen {...props} />);
    emit({ type: 'typing', sender_id: 'me', typing: true });
    expect(screen.queryByText('Typing…')).toBeNull();
    emit({ type: 'typing', sender_id: 'c1', typing: true });
    expect(screen.getByText('Typing…')).toBeTruthy();
    emit({ type: 'typing', sender_id: 'c1', typing: false });
    expect(screen.queryByText('Typing…')).toBeNull();
    expect(screen.getByText('Connecting…')).toBeTruthy();
  });

  it('uses coach copy for coaches', () => {
    mockAuth.user = { id: 'me', role: 'coach' };
    render(<ChatScreen {...props} />);
    expect(screen.getByText('Client chat')).toBeTruthy();
    expect(screen.getByPlaceholderText('Reply to your client')).toBeTruthy();
  });

  it('clears the typing flag 3s after the last keystroke', () => {
    jest.useFakeTimers();
    try {
      render(<ChatScreen {...props} />);
      fireEvent.changeText(screen.getByPlaceholderText('Message your coach'), 'h');
      act(() => {
        jest.advanceTimersByTime(2999);
      });
      expect(latest().typing).toHaveBeenLastCalledWith(true);
      act(() => {
        jest.advanceTimersByTime(1);
      });
      expect(latest().typing).toHaveBeenLastCalledWith(false);
    } finally {
      jest.useRealTimers();
    }
  });
});
