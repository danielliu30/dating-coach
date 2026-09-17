import { act, fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import React from 'react';

import type { ChatEvent } from '../api/types';
import ChatScreen from './ChatScreen';

interface Handlers {
  onEvent: (event: ChatEvent) => void;
  onStatus?: (status: 'connecting' | 'open' | 'closed') => void;
}

const mockSockets: { threadID: string; handlers: Handlers; send: jest.Mock }[] = [];

type Msg = { _id: string | number; text: string; createdAt: Date; pending?: boolean; user: { _id: string } };

// Gifted Chat pulls in reanimated/worklets natives that do not load under jest, so
// stand in a list + input that call the same props. `append` keeps the real
// newest-first ordering the screen relies on.
jest.mock('react-native-gifted-chat', () => {
  const React = require('react');
  const { Pressable, Text, TextInput, View } = require('react-native');
  function GiftedChat({
    messages,
    onSend,
    textInputProps,
  }: {
    messages: Msg[];
    onSend: (m: Msg[]) => void;
    textInputProps: { onChangeText: (t: string) => void; placeholder: string };
  }) {
    const [text, setText] = React.useState('');
    return React.createElement(
      View,
      null,
      messages.map((m) =>
        React.createElement(Text, { key: String(m._id), testID: m.pending ? 'pending' : 'sent' }, m.text),
      ),
      React.createElement(TextInput, {
        placeholder: textInputProps.placeholder,
        value: text,
        onChangeText: (t: string) => {
          setText(t);
          textInputProps.onChangeText(t);
        },
      }),
      React.createElement(
        Pressable,
        {
          onPress: () => {
            onSend([{ _id: `local-${Date.now()}-${Math.random()}`, text, createdAt: new Date(), user: { _id: 'me' } }]);
            setText('');
          },
        },
        React.createElement(Text, null, 'Send'),
      ),
    );
  }
  GiftedChat.append = (current: Msg[] = [], incoming: Msg[] = []) => [...incoming].reverse().concat(current);
  return { GiftedChat, Bubble: View, InputToolbar: View };
});

jest.mock('../api/socket', () => ({
  ChatSocket: jest.fn().mockImplementation((threadID: string, _token: () => string | null, handlers: Handlers) => {
    const socket = { threadID, handlers, send: jest.fn(), typing: jest.fn(), connect: jest.fn(), close: jest.fn() };
    mockSockets.push(socket);
    return socket;
  }),
}));

jest.mock('../state/auth', () => ({
  useAuth: () => ({ token: 't', user: { id: 'me', display_name: 'Riley', role: 'user' } }),
}));

const latest = () => {
  const socket = mockSockets.at(-1);
  if (!socket) throw new Error('no socket was created');
  return socket;
};

function mount() {
  const ScreenAny = ChatScreen as unknown as React.ComponentType<{ route: unknown; navigation: unknown }>;
  return render(
    <ScreenAny route={{ params: { threadID: 'th1', title: 'Casey Coach' } }} navigation={{ setOptions: jest.fn() }} />,
  );
}

async function typeAndSend(text: string) {
  fireEvent.changeText(screen.getByPlaceholderText('Message your coach'), text);
  fireEvent.press(await screen.findByText('Send'));
}

describe('ChatScreen optimistic sending', () => {
  beforeEach(() => {
    mockSockets.length = 0;
  });

  it('shows a sent message immediately while the socket is disconnected', async () => {
    mount();
    await waitFor(() => expect(mockSockets).toHaveLength(1));
    act(() => latest().handlers.onStatus?.('closed'));

    await typeAndSend('are you there?');

    expect(await screen.findByText('are you there?')).toBeTruthy();
    expect(screen.getAllByTestId('pending')).toHaveLength(1);
    expect(latest().send).toHaveBeenCalledWith('are you there?');
  });

  it('replaces the pending bubble with the server echo instead of duplicating it', async () => {
    mount();
    await waitFor(() => expect(mockSockets).toHaveLength(1));
    act(() => latest().handlers.onStatus?.('open'));

    await typeAndSend('hello coach');
    expect(await screen.findByText('hello coach')).toBeTruthy();

    act(() =>
      latest().handlers.onEvent({
        type: 'message',
        message_id: 'm1',
        sender_id: 'me',
        body: 'hello coach',
        created_at: '2030-01-07T18:00:00Z',
      }),
    );

    expect(screen.getAllByText('hello coach')).toHaveLength(1);
    expect(screen.queryAllByTestId('pending')).toHaveLength(0);
    expect(screen.getAllByTestId('sent')).toHaveLength(1);
  });

  it('still appends messages from the other side', async () => {
    mount();
    await waitFor(() => expect(mockSockets).toHaveLength(1));

    await typeAndSend('ping');
    act(() =>
      latest().handlers.onEvent({
        type: 'message',
        message_id: 'm2',
        sender_id: 'coach',
        body: 'ping',
        created_at: '2030-01-07T18:00:00Z',
      }),
    );

    // Our pending "ping" and the coach's "ping" are different messages.
    expect(screen.getAllByText('ping')).toHaveLength(2);
  });
});
