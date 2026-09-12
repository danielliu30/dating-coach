import React, { useState } from 'react';
import { Pressable, Text, TextInput, View } from 'react-native';

type Msg = { _id: string | number; text: string; user: { _id: string | number } };

type Props = {
  messages: Msg[];
  onSend: (messages: Msg[]) => void;
  textInputProps: { onChangeText: (text: string) => void; placeholder: string };
  renderChatEmpty: () => React.ReactNode;
};

/**
 * Minimal stand-in for GiftedChat: lists `sender: body` lines, forwards keystrokes to
 * `textInputProps.onChangeText`, and a Send button that emits the current draft via `onSend`.
 */
function GiftedChat({ messages, onSend, textInputProps, renderChatEmpty }: Props): React.ReactElement {
  const [draft, setDraft] = useState('');
  return (
    <View>
      {messages.length === 0 ? renderChatEmpty() : null}
      {messages.map((m) => (
        <Text key={String(m._id)}>{`${m.user._id}: ${m.text}`}</Text>
      ))}
      <TextInput
        placeholder={textInputProps.placeholder}
        value={draft}
        onChangeText={(text) => {
          setDraft(text);
          textInputProps.onChangeText(text);
        }}
      />
      <Pressable accessibilityRole="button" onPress={() => onSend([{ _id: 'local', text: draft, user: { _id: 'me' } }])}>
        <Text>Send</Text>
      </Pressable>
    </View>
  );
}

/** Mirrors GiftedChat.append: newest messages go first. */
GiftedChat.append = (current: Msg[], incoming: Msg[]): Msg[] => [...incoming, ...current];

/** Module shape for `jest.mock('react-native-gifted-chat', ...)`. */
export const giftedChatStub = { GiftedChat, Bubble: View, InputToolbar: View };
