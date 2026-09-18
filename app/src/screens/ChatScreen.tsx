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
  const connectionRef = useRef(connection);
  connectionRef.current = connection;
  // Ids of pending bubbles the server has not seen yet: sent while disconnected,
  // so still in the socket's outbox. Opening a connection flushes the outbox,
  // but the server may still reject them, so they only stop counting as queued
  // once that connection ends (or their echo/rejection arrives).
  const queuedRef = useRef(new Set<string | number>());
  const flushedRef = useRef(new Set<string | number>());

  /**
   * Mirrors the socket status into state and tracks which queued bubbles have
   * been handed to the server: ids flushed on a connection leave `queuedRef`
   * when that connection closes, so a later history can settle them.
   */
  const onStatus = useCallback((status: 'connecting' | 'open' | 'closed') => {
    if (status === 'open') {
      flushedRef.current = new Set(queuedRef.current);
    } else if (status === 'closed') {
      flushedRef.current.forEach((id) => queuedRef.current.delete(id));
      flushedRef.current.clear();
    }
    setConnection(status);
  }, []);
  const signedIn = token !== null;

  useEffect(() => navigation.setOptions({ title }), [navigation, title]);

  const onEvent = useCallback(
    (event: ChatEvent) => {
      switch (event.type) {
        case 'history': {
          // History arrives on every (re)connect, before the socket's outbox is
          // flushed. A pending bubble the server did receive (sent on an open
          // socket, echo lost with the connection) is settled by a matching
          // history message; bubbles still queued in the outbox are kept as is,
          // since an older identical message is not theirs.
          const history = (event.messages ?? []).map(toGifted);
          setMessages((current) => {
            const known = new Set(current.map((m) => m._id));
            const pending = current.filter((m) => m.pending);
            history
              .filter((m) => m.user._id === user?.id && !known.has(m._id))
              .reverse()
              .forEach((m) => {
                const i = pending.findLastIndex((p) => !queuedRef.current.has(p._id) && p.text === m.text);
                if (i !== -1) pending.splice(i, 1);
              });
            return [...pending, ...history];
          });
          break;
        }
        case 'message': {
          if (!event.message_id) return;
          const persisted: IMessage = {
            _id: event.message_id,
            text: event.body ?? '',
            createdAt: event.created_at ? new Date(event.created_at) : new Date(),
            user: { _id: event.sender_id ?? 'unknown' },
            sent: true,
          };
          setMessages((current) => {
            if (current.some((m) => m._id === persisted._id)) return current;
            // Our own echo names the bubble it settles; an echo from a server
            // that does not settles the oldest pending bubble with the same text.
            let pendingIndex = -1;
            if (event.client_id) {
              pendingIndex = current.findIndex((m) => m.pending && String(m._id) === event.client_id);
            } else if (event.sender_id === user?.id) {
              pendingIndex = current.findLastIndex((m) => m.pending && m.text === persisted.text);
            }
            if (pendingIndex === -1) return GiftedChat.append(current, [persisted]);
            queuedRef.current.delete(current[pendingIndex]._id);
            return current.map((m, i) => (i === pendingIndex ? persisted : m));
          });
          break;
        }
        case 'typing':
          if (event.sender_id && event.sender_id !== user?.id) setPeerTyping(Boolean(event.typing));
          break;
        case 'error': {
          // The server only reports errors for rejected sends and names the
          // send it refused; without a name, the oldest pending bubble is taken.
          setMessages((current) => {
            const rejected = event.client_id
              ? current.findIndex((m) => m.pending && String(m._id) === event.client_id)
              : current.findLastIndex((m) => m.pending);
            if (rejected === -1) return current;
            queuedRef.current.delete(current[rejected]._id);
            const notice: IMessage = {
              _id: `error-${Date.now()}-${rejected}`,
              text: `Not sent: ${event.body ?? 'message rejected'}`,
              createdAt: new Date(),
              user: { _id: 'system' },
              system: true,
            };
            return [notice, ...current.filter((_, i) => i !== rejected)];
          });
          break;
        }
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
      onStatus,
    });
    socketRef.current = socket;
    socket.connect();
    return () => {
      socket.close();
      socketRef.current = null;
    };
  }, [onEvent, onStatus, signedIn, threadID]);

  /**
   * Shows each outgoing message at once as a pending bubble and hands it to the
   * socket, which queues it while disconnected. The server's echo (see onEvent)
   * replaces the pending bubble with the persisted message.
   */
  const onSend = useCallback((outgoing: IMessage[] = []) => {
    if (connectionRef.current !== 'open') outgoing.forEach((message) => queuedRef.current.add(message._id));
    setMessages((current) =>
      GiftedChat.append(
        current,
        // The server trims bodies, so the echo is matched against trimmed text.
        outgoing.map((message) => ({ ...message, text: message.text.trim(), pending: true, sent: false })),
      ),
    );
    outgoing.forEach((message) => socketRef.current?.send(message.text, String(message._id)));
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
