import type { NativeStackScreenProps } from '@react-navigation/native-stack';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';
import { Bubble, GiftedChat, InputToolbar, type IMessage } from 'react-native-gifted-chat';
import { SafeAreaView } from 'react-native-safe-area-context';

import { ChatSocket } from '../api/socket';
import type { ChatEvent, ChatMessage } from '../api/types';
import { Avatar } from '../components/kit';
import type { ChatsStackParams } from '../navigation/types';
import { useAuth } from '../state/auth';
import { colors, CONTENT_MAX_WIDTH, fonts, radii, shared } from '../theme';

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
  // The socket reads the token per connection attempt, so a renewal reaches the
  // next reconnect without tearing the open one down.
  const tokenRef = useRef(token);
  tokenRef.current = token;
  const signedIn = token !== null;

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
    if (!signedIn) return;
    const socket = new ChatSocket(threadID, () => tokenRef.current, {
      onEvent,
      onStatus: setConnection,
    });
    socketRef.current = socket;
    socket.connect();
    return () => {
      socket.close();
      socketRef.current = null;
    };
  }, [onEvent, signedIn, threadID]);

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
  const isCoach = user?.role === 'coach';
  const statusTone = connection === 'open' ? colors.engaging : connection === 'connecting' ? colors.neutral : colors.flat;
  const statusText = connection === 'open' ? 'Connected' : connection === 'connecting' ? 'Connecting…' : 'Reconnecting…';

  return (
    <SafeAreaView style={shared.screen} edges={['left', 'right', 'bottom']}>
      <View style={styles.status}>
        <View style={styles.statusInner}>
          <Avatar name={title} size={36} />
          <View style={{ flex: 1, gap: 1 }}>
            <Text style={styles.statusName} numberOfLines={1}>
              {title}
            </Text>
            <View style={styles.statusRow}>
              <View style={[styles.dot, { backgroundColor: statusTone }]} />
              <Text style={[styles.statusText, { color: statusTone }]}>
                {peerTyping ? 'Typing…' : statusText}
              </Text>
            </View>
          </View>
          <Text style={styles.statusHint}>{isCoach ? 'Client chat' : 'Live coach chat'}</Text>
        </View>
      </View>
      <View style={styles.chat}>
        <GiftedChat
          messages={messages}
          onSend={onSend}
          user={currentUser}
          isTyping={peerTyping}
          isSendButtonAlwaysVisible
          isScrollToBottomEnabled={Platform.OS === 'web'}
          textInputProps={{
            onChangeText: onInputTextChanged,
            placeholder: isCoach ? 'Reply to your client' : 'Message your coach',
            style: styles.input,
          }}
          renderBubble={(props) => (
            <Bubble
              {...props}
              wrapperStyle={{ left: styles.bubbleLeft, right: styles.bubbleRight }}
              textStyle={{ left: styles.bubbleTextLeft, right: styles.bubbleTextRight }}
            />
          )}
          renderInputToolbar={(props) => (
            <InputToolbar {...props} containerStyle={styles.toolbar} primaryStyle={{ alignItems: 'center' }} />
          )}
          renderChatEmpty={() => (
            <View style={styles.emptyWrap}>
              <View style={styles.emptyCard}>
                <Text style={styles.emptyTitle}>Say hello</Text>
                <Text style={styles.emptyText}>
                  {isCoach
                    ? 'Your client is here for a second perspective. Ask what is on their mind.'
                    : 'Share what happened, paste a message you are stuck on, or ask how to follow up.'}
                </Text>
              </View>
            </View>
          )}
        />
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  status: {
    borderBottomWidth: 1,
    borderBottomColor: colors.border,
    backgroundColor: colors.surface,
  },
  statusInner: {
    width: '100%',
    maxWidth: CONTENT_MAX_WIDTH,
    alignSelf: 'center',
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
    paddingHorizontal: 16,
    paddingVertical: 10,
  },
  statusName: { fontFamily: fonts.sansBold, fontSize: 15, color: colors.text },
  statusRow: { flexDirection: 'row', alignItems: 'center', gap: 6 },
  dot: { width: 8, height: 8, borderRadius: 4 },
  statusText: { fontFamily: fonts.sansSemi, fontSize: 12 },
  statusHint: { fontFamily: fonts.sans, fontSize: 12, color: colors.muted },
  chat: {
    flex: 1,
    width: '100%',
    maxWidth: CONTENT_MAX_WIDTH,
    alignSelf: 'center',
  },
  bubbleLeft: {
    backgroundColor: colors.surface,
    borderRadius: radii.md,
    borderBottomLeftRadius: 6,
    borderWidth: 1,
    borderColor: colors.border,
    paddingHorizontal: 4,
    paddingVertical: 2,
  },
  bubbleRight: {
    backgroundColor: colors.primary,
    borderRadius: radii.md,
    borderBottomRightRadius: 6,
    paddingHorizontal: 4,
    paddingVertical: 2,
  },
  bubbleTextLeft: { fontFamily: fonts.sans, fontSize: 15, lineHeight: 21, color: colors.text },
  bubbleTextRight: { fontFamily: fonts.sans, fontSize: 15, lineHeight: 21, color: colors.primaryText },
  toolbar: {
    backgroundColor: colors.surface,
    borderTopWidth: 1,
    borderTopColor: colors.border,
    paddingHorizontal: 8,
    paddingVertical: 4,
  },
  input: { fontFamily: fonts.sans, fontSize: 15, color: colors.text, lineHeight: 20 },
  emptyWrap: { flex: 1, alignItems: 'center', justifyContent: 'center', padding: 24, transform: [{ scaleY: -1 }] },
  emptyCard: {
    maxWidth: 360,
    gap: 6,
    padding: 18,
    borderRadius: radii.lg,
    backgroundColor: colors.sageTint,
    borderWidth: 1,
    borderColor: colors.border,
  },
  emptyTitle: { fontFamily: fonts.sansBold, fontSize: 16, color: colors.moss },
  emptyText: { fontFamily: fonts.sans, fontSize: 14, lineHeight: 20, color: colors.text },
});
