import { Ionicons } from '@expo/vector-icons';
import React, { useState } from 'react';
import { Pressable, StyleSheet, Text, useWindowDimensions, View } from 'react-native';

import { colors, fonts, radii, type } from '../theme';
import type { IconName } from './ui';

type Feature = {
  id: string;
  icon: IconName;
  label: string;
  heading: string;
  points: { icon: IconName; text: string }[];
};

const FEATURES: Feature[] = [
  {
    id: 'chat',
    icon: 'chatbubbles-outline',
    label: 'Live chat',
    heading: 'Real-time chat with your coach',
    points: [
      {
        icon: 'flash-outline',
        text: "Each thread runs on a dedicated WebSocket — messages arrive the moment they're sent, no refresh.",
      },
      {
        icon: 'cloud-upload-outline',
        text: "If your connection drops, messages you typed are held on your device and replayed when you're back.",
      },
      {
        icon: 'key-outline',
        text: "Your session renews itself in the background, so a long conversation doesn't cut out mid-thought.",
      },
    ],
  },
  {
    id: 'booking',
    icon: 'calendar-outline',
    label: 'Booking',
    heading: 'From coach profile to booked session',
    points: [
      {
        icon: 'people-outline',
        text: "Browse approved coaches and open a profile to see the next week's open slots.",
      },
      {
        icon: 'time-outline',
        text: 'Pick a time, say what you want to work on, and the session is booked in one step.',
      },
      {
        icon: 'chatbox-ellipses-outline',
        text: 'Every coach pairing keeps a private chat thread, so prep and follow-ups stay in one place.',
      },
    ],
  },
  {
    id: 'analyzer',
    icon: 'sparkles-outline',
    label: 'Analyzer',
    heading: 'Feedback on your real conversations',
    points: [
      {
        icon: 'enter-outline',
        text: 'Import a conversation and it joins the scoring queue — you can keep using the app while it runs.',
      },
      {
        icon: 'pulse-outline',
        text: 'The analyzer scores each stretch of the chat for engagement, then writes a summary: strengths, improvements, and patterns.',
      },
      {
        icon: 'pricetag-outline',
        text: 'You label what actually happened — ghosted, a date, a great talk — and that feeds back into better advice.',
      },
    ],
  },
];

/**
 * "How it works" tabs for the signed-out landing. A vertical rail on wide
 * layouts (the same sidebar pattern as the app shell) that folds to a
 * horizontal chip row on narrow ones. Tabs deliberately use button role,
 * not tab — the sign-in flow detects the signed-in state by the absence of
 * role="tab" elements on this page.
 */
export function FeatureTabs(): React.ReactElement {
  const { width } = useWindowDimensions();
  const side = width >= 720;
  const [active, setActive] = useState(0);
  const feature = FEATURES[active];

  return (
    <View style={[styles.wrap, side && styles.wrapRow]}>
      <View style={[styles.rail, side ? styles.railSide : styles.railTop]}>
        {FEATURES.map((f, i) => {
          const selected = i === active;
          return (
            <Pressable
              key={f.id}
              accessibilityRole="button"
              accessibilityState={{ selected }}
              accessibilityLabel={f.label}
              onPress={() => setActive(i)}
              style={({ pressed }) => [styles.tab, selected && styles.tabActive, pressed && styles.tabPressed]}
            >
              <Ionicons name={f.icon} size={18} color={selected ? colors.primaryDeep : colors.muted} />
              <Text style={[styles.tabLabel, selected && { color: colors.primaryDeep }]}>{f.label}</Text>
            </Pressable>
          );
        })}
      </View>
      <View style={styles.panel}>
        <Text style={type.heading}>{feature.heading}</Text>
        {feature.points.map((p) => (
          <View key={p.text} style={styles.point}>
            <View style={styles.pointIcon}>
              <Ionicons name={p.icon} size={16} color={colors.sageDeep} />
            </View>
            <Text style={[type.body, styles.pointText]}>{p.text}</Text>
          </View>
        ))}
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { gap: 16 },
  wrapRow: { flexDirection: 'row', alignItems: 'stretch' },
  rail: { gap: 6 },
  railSide: { width: 170, borderRightWidth: StyleSheet.hairlineWidth, borderRightColor: colors.border, paddingRight: 12 },
  railTop: { flexDirection: 'row', flexWrap: 'wrap' },
  tab: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    paddingHorizontal: 12,
    paddingVertical: 10,
    borderRadius: radii.md,
    minHeight: 42,
  },
  tabActive: { backgroundColor: colors.primaryTint },
  tabPressed: { opacity: 0.75 },
  tabLabel: { fontFamily: fonts.sansBold, fontSize: 13, color: colors.muted },
  panel: { flex: 1, gap: 14, padding: 20, borderRadius: radii.lg, backgroundColor: colors.surfaceAlt },
  point: { flexDirection: 'row', alignItems: 'flex-start', gap: 10 },
  pointIcon: {
    width: 30,
    height: 30,
    borderRadius: 15,
    backgroundColor: colors.sageTint,
    alignItems: 'center',
    justifyContent: 'center',
  },
  pointText: { flex: 1, color: colors.text },
});
