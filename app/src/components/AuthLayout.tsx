import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import { StatusBar } from 'expo-status-bar';
import React, { useCallback, useRef } from 'react';
import {
  Image,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  useWindowDimensions,
  View,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, elevation, fonts, radii, type } from '../theme';
import { Landing } from './Landing';

// Two people, sunlit, among trees.
const HERO_IMAGE_URL =
  'https://images.unsplash.com/photo-1529333166437-7750a6dd5a70?auto=format&fit=crop&w=2000&q=80';

const PROOF: { icon: React.ComponentProps<typeof Ionicons>['name']; text: string }[] = [
  { icon: 'people-outline', text: 'Live human coaches' },
  { icon: 'pulse-outline', text: 'Honest conversation feedback' },
  { icon: 'shield-checkmark-outline', text: 'Never dates on your behalf' },
];

/**
 * Signed-out shell: a full-bleed hero photo with the form floating in the
 * middle. With `landing`, the Product/About story scrolls beneath the hero and
 * the top nav anchors to it; without it (sign-up, verify) only the hero shows.
 */
export function AuthLayout({
  children,
  landing = false,
}: {
  children: React.ReactNode;
  landing?: boolean;
}): React.ReactElement {
  const { width, height } = useWindowDimensions();
  const wide = width >= 900;
  const scrollRef = useRef<ScrollView>(null);
  const formRef = useRef<View>(null);
  const productRef = useRef<View>(null);
  const aboutRef = useRef<View>(null);

  const scrollTo = useCallback((target: React.RefObject<View | null>) => {
    const scroll = scrollRef.current;
    const node = target.current;
    if (!scroll || !node) return;
    const container = scroll.getInnerViewNode();
    if (!container) return;
    node.measureLayout(container, (_x, y) => scroll.scrollTo({ y: Math.max(0, y - 16), animated: true }));
  }, []);

  return (
    <View style={styles.screen}>
      <StatusBar style="light" />
      <ScrollView
        ref={scrollRef}
        style={{ flex: 1 }}
        contentContainerStyle={styles.scroll}
        keyboardShouldPersistTaps="handled"
        showsVerticalScrollIndicator={false}
      >
        <View style={[styles.hero, { minHeight: landing ? Math.max(height, 640) : height }]}>
          <Image source={{ uri: HERO_IMAGE_URL }} style={styles.fill} resizeMode="cover" />
          <LinearGradient
            colors={['rgba(58,44,36,0.55)', 'rgba(58,44,36,0.15)', 'rgba(58,44,36,0.35)', colors.bg]}
            locations={[0, 0.35, 0.72, 1]}
            style={styles.fill}
          />
          <LinearGradient
            colors={['rgba(185,83,95,0.25)', 'rgba(63,91,69,0.0)']}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 1 }}
            style={styles.fill}
          />
          <SafeAreaView edges={['top', 'left', 'right']} style={{ flex: 1 }}>
            <View style={styles.nav}>
              <View style={styles.brand}>
                <View style={styles.brandMark}>
                  <Ionicons name="leaf" size={16} color={colors.primaryText} />
                </View>
                <Text style={styles.wordmark}>Dating Humane</Text>
              </View>
              {landing ? (
                <View style={styles.navLinks}>
                  <NavLink label="Product" onPress={() => scrollTo(productRef)} />
                  <NavLink label="About" onPress={() => scrollTo(aboutRef)} />
                  <NavLink label="Sign in" onPress={() => scrollTo(formRef)} filled />
                </View>
              ) : null}
            </View>

            <View style={[styles.heroBody, wide && styles.heroBodyWide]}>
              {wide ? (
                <View style={styles.heroCopy}>
                  <Text style={styles.eyebrow}>Coaching for the part after the match</Text>
                  <Text style={styles.headline}>Better conversations.{'\n'}Real connection.</Text>
                  <Text style={styles.tagline}>
                    Human coaching and honest feedback on how your chats actually land — so you become a better dater,
                    not a better script.
                  </Text>
                  <View style={styles.proofRow}>
                    {PROOF.map((p) => (
                      <View key={p.text} style={styles.proof}>
                        <Ionicons name={p.icon} size={16} color={colors.sunTint} />
                        <Text style={styles.proofText}>{p.text}</Text>
                      </View>
                    ))}
                  </View>
                </View>
              ) : null}

              <View ref={formRef} collapsable={false} style={[styles.card, wide && styles.cardWide]}>
                {!wide ? (
                  <Text style={styles.compactTagline}>Better conversations. Real connection.</Text>
                ) : null}
                {children}
              </View>
            </View>

            {landing ? (
              <Pressable
                accessibilityRole="button"
                accessibilityLabel="Scroll to learn more"
                onPress={() => scrollTo(productRef)}
                style={styles.scrollHint}
              >
                <Text style={styles.scrollHintText}>Why Dating Humane</Text>
                <Ionicons name="chevron-down" size={18} color={colors.primaryText} />
              </Pressable>
            ) : null}
          </SafeAreaView>
        </View>

        {landing ? (
          <View style={styles.landing}>
            <Landing productRef={productRef} aboutRef={aboutRef} />
          </View>
        ) : null}
      </ScrollView>
    </View>
  );
}

/** Translucent pill link used in the landing top nav. */
function NavLink({
  label,
  onPress,
  filled,
}: {
  label: string;
  onPress: () => void;
  filled?: boolean;
}): React.ReactElement {
  return (
    <Pressable
      accessibilityRole="button"
      onPress={onPress}
      style={({ pressed }) => [styles.navLink, filled && styles.navLinkFilled, pressed && { opacity: 0.8 }]}
    >
      <Text style={[styles.navLinkText, filled && { color: colors.primaryDeep }]}>{label}</Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  screen: { flex: 1, backgroundColor: colors.bg },
  scroll: { flexGrow: 1 },
  fill: { position: 'absolute', top: 0, left: 0, right: 0, bottom: 0, width: '100%', height: '100%' },
  hero: { width: '100%', backgroundColor: colors.moss },
  nav: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    flexWrap: 'wrap',
    paddingHorizontal: 22,
    paddingVertical: 18,
    gap: 12,
  },
  brand: { flexDirection: 'row', alignItems: 'center', gap: 10 },
  brandMark: {
    width: 32,
    height: 32,
    borderRadius: 16,
    backgroundColor: colors.primary,
    alignItems: 'center',
    justifyContent: 'center',
    ...elevation.primary,
  },
  wordmark: { fontFamily: fonts.sansBlack, fontSize: 20, letterSpacing: -0.4, color: colors.primaryText },
  navLinks: { flexDirection: 'row', gap: 8, flexWrap: 'wrap', flexShrink: 1, justifyContent: 'flex-end' },
  navLink: {
    paddingHorizontal: 14,
    paddingVertical: 8,
    borderRadius: radii.pill,
    backgroundColor: 'rgba(255,255,255,0.14)',
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.28)',
  },
  navLinkFilled: { backgroundColor: colors.surface, borderColor: colors.surface },
  navLinkText: { fontFamily: fonts.sansBold, fontSize: 13, color: colors.primaryText },
  heroBody: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 20,
    paddingVertical: 24,
    gap: 28,
  },
  heroBodyWide: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: 56,
    width: '100%',
    maxWidth: 1120,
    alignSelf: 'center',
    paddingHorizontal: 32,
  },
  heroCopy: { flex: 1, maxWidth: 560, gap: 18 },
  eyebrow: { ...type.eyebrow, color: colors.sunTint },
  headline: {
    fontFamily: fonts.sansBlack,
    color: colors.primaryText,
    fontSize: 58,
    lineHeight: 62,
    letterSpacing: -2,
  },
  tagline: { fontFamily: fonts.sans, color: 'rgba(255,255,255,0.9)', fontSize: 18, lineHeight: 27, maxWidth: 500 },
  proofRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 10, marginTop: 6 },
  proof: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: radii.pill,
    backgroundColor: 'rgba(58,44,36,0.35)',
    borderWidth: 1,
    borderColor: 'rgba(255,255,255,0.2)',
  },
  proofText: { fontFamily: fonts.sansSemi, fontSize: 13, color: colors.primaryText },
  card: {
    width: '100%',
    maxWidth: 440,
    gap: 16,
    padding: 24,
    borderRadius: radii.xl,
    backgroundColor: colors.surface,
    ...elevation.high,
  },
  cardWide: { maxWidth: 460, padding: 30 },
  compactTagline: { ...type.accent, textAlign: 'center', fontSize: 18, lineHeight: 24 },
  scrollHint: { alignSelf: 'center', alignItems: 'center', gap: 2, paddingBottom: 26, opacity: 0.9 },
  scrollHintText: { fontFamily: fonts.sansSemi, fontSize: 13, color: colors.primaryText },
  landing: { paddingHorizontal: 16, paddingTop: 8, paddingBottom: 32 },
});
