import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';
import { GiftedChat, type IMessage } from 'react-native-gifted-chat';
import { SafeAreaView } from 'react-native-safe-area-context';

import { ChatSocket } from '../api/socket';
import type { ChatEvent, ChatMessage } from '../api/types';
import type { ChatsStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors, CONTENT_MAX_WIDTH, shared } from '../theme';

const toGifted = (message: ChatMessage): IMessage => ({
  _id: message.id,
  text: message.body,
  createdAt: new Date(message.created_at),
  user: { _id: message.sender_id },
});

export default function ChatScreen({
  route,
  navigation,
}: NativeStackScreenProps<ChatsStackParams, 'Chat'>): React.ReactElement {
  const { threadID, title } = route.params;
  const { token, user } = useAuth();
  const [messages, setMessages] = useState<IMessage[]>([]);
  const [connection, setConnection] = useState<'connecting' | 'open' | 'closed'>('connecting');
  const [peerTyping, setPeerTyping] = useState(false);
  const socketRef = useRef<ChatSocket | null>(null);
  const typingTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => navigation.setOptions({ title }), [navigation, title]);

  const onEvent = useCallback(
    (event: ChatEvent) => {
      switch (event.type) {
        case 'history':
          setMessages((event.messages ?? []).map(toGifted));
          break;
        case 'message':
          if (!event.message_id) return;
          setMessages((current) =>
            current.some((m) => m._id === event.message_id)
              ? current
              : GiftedChat.append(current, [
                  {
                    _id: event.message_id as string,
                    text: event.body ?? '',
                    createdAt: event.created_at ? new Date(event.created_at) : new Date(),
                    user: { _id: event.sender_id ?? 'unknown' },
                  },
                ]),
          );
          break;
        case 'typing':
          if (event.sender_id && event.sender_id !== user?.id) setPeerTyping(Boolean(event.typing));
          break;
        default:
          break;
      }
    },
    [user?.id],
  );

  useEffect(() => {
    if (!token) return;
    const socket = new ChatSocket(threadID, token, { onEvent, onStatus: setConnection });
    socketRef.current = socket;
    socket.connect();
    return () => {
      socket.close();
      socketRef.current = null;
    };
  }, [onEvent, threadID, token]);

  // The socket echoes the persisted message back, so sending is fire-and-forget.
  const onSend = useCallback((outgoing: IMessage[] = []) => {
    outgoing.forEach((message) => socketRef.current?.send(message.text));
    socketRef.current?.typing(false);
  }, []);

  const onInputTextChanged = useCallback((text: string) => {
    socketRef.current?.typing(text.length > 0);
    if (typingTimer.current) clearTimeout(typingTimer.current);
    typingTimer.current = setTimeout(() => socketRef.current?.typing(false), 3000);
  }, []);

  const currentUser = useMemo(() => ({ _id: user?.id ?? 'me' }), [user?.id]);

  return (
    <SafeAreaView style={shared.screen} edges={['left', 'right', 'bottom']}>
      <View style={styles.status}>
        <Text style={shared.muted}>
          {connection === 'open' ? 'Connected' : connection === 'connecting' ? 'Connecting…' : 'Reconnecting…'}
        </Text>
      </View>
      <View style={styles.chat}>
        <GiftedChat
          messages={messages}
          onSend={onSend}
          user={currentUser}
          isTyping={peerTyping}
          isSendButtonAlwaysVisible
          isScrollToBottomEnabled={Platform.OS === 'web'}
          textInputProps={{ onChangeText: onInputTextChanged, placeholder: 'Message your coach' }}
        />
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  status: {
    paddingHorizontal: 16,
    paddingVertical: 6,
    borderBottomWidth: 1,
    borderBottomColor: colors.border,
    backgroundColor: colors.surface,
  },
  chat: {
    flex: 1,
    width: '100%',
    maxWidth: CONTENT_MAX_WIDTH,
    alignSelf: 'center',
  },
});
