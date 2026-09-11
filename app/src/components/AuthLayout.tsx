import React from 'react';
import { Image, ScrollView, StyleSheet, Text, useWindowDimensions, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, elevation, radii } from '../theme';

// Replace this with your own happy-couple asset when ready.
const COUPLE_IMAGE_URL =
  'https://images.unsplash.com/photo-1516589178581-6cd7833ae3b2?auto=format&fit=crop&w=1800&q=80';

export function AuthLayout({ children }: { children: React.ReactNode }): React.ReactElement {
  const { width } = useWindowDimensions();
  const wide = width >= 900;

  return (
    <View style={styles.screen}>
      <Image source={{ uri: COUPLE_IMAGE_URL }} style={styles.backdrop} resizeMode="cover" />
      <View pointerEvents="none" style={styles.scrim} />
      <View pointerEvents="none" style={styles.scrimBottom} />
      <View pointerEvents="none" style={styles.scrimBrand} />
      <SafeAreaView style={styles.safe}>
        <ScrollView
          contentContainerStyle={styles.scroll}
          keyboardShouldPersistTaps="handled"
        >
          {wide && (
            <View style={styles.hero}>
              <Text style={styles.wordmark}>Dating Humane</Text>
              <Text style={styles.headline}>Better conversations.{'\n'}Real connection.</Text>
              <Text style={styles.tagline}>
                Human coaching plus honest feedback on how your chats actually land.
              </Text>
            </View>
          )}
          <View style={[styles.card, wide && styles.cardWide]}>
            {!wide && <Text style={styles.wordmarkCompact}>Dating Humane</Text>}
            {children}
          </View>
        </ScrollView>
      </SafeAreaView>
    </View>
  );
}

const styles = StyleSheet.create({
  screen: {
    flex: 1,
    backgroundColor: colors.shadow,
  },
  backdrop: {
    position: 'absolute', top: 0, left: 0, right: 0, bottom: 0,
    width: '100%',
    height: '100%',
  },
  scrim: {
    position: 'absolute', top: 0, left: 0, right: 0, bottom: 0,
    backgroundColor: colors.shadow,
    opacity: 0.38,
  },
  scrimBottom: {
    position: 'absolute',
    left: 0,
    right: 0,
    bottom: 0,
    height: '55%',
    backgroundColor: '#000000',
    opacity: 0.32,
  },
  scrimBrand: {
    position: 'absolute',
    left: 0,
    right: 0,
    top: 0,
    height: '40%',
    backgroundColor: colors.primaryDeep,
    opacity: 0.14,
  },
  safe: {
    flex: 1,
  },
  scroll: {
    flexGrow: 1,
    justifyContent: 'center',
    padding: 20,
    gap: 24,
  },
  hero: {
    width: '100%',
    maxWidth: 720,
    alignSelf: 'center',
    alignItems: 'center',
    gap: 14,
    marginBottom: 8,
  },
  wordmark: {
    color: colors.primaryText,
    fontSize: 15,
    fontWeight: '800',
    letterSpacing: 2.5,
    textTransform: 'uppercase',
    opacity: 0.9,
  },
  wordmarkCompact: {
    alignSelf: 'center',
    color: colors.primary,
    fontSize: 13,
    fontWeight: '800',
    letterSpacing: 2.5,
    textTransform: 'uppercase',
  },
  headline: {
    color: colors.primaryText,
    fontSize: 56,
    lineHeight: 60,
    fontWeight: '800',
    letterSpacing: -1.5,
    textAlign: 'center',
  },
  tagline: {
    color: colors.primaryText,
    fontSize: 18,
    lineHeight: 26,
    opacity: 0.86,
    maxWidth: 480,
    textAlign: 'center',
  },
  card: {
    width: '100%',
    maxWidth: 440,
    alignSelf: 'center',
    gap: 16,
    padding: 24,
    borderRadius: radii.xl,
    backgroundColor: colors.surface,
    ...elevation.high,
  },
  cardWide: {
    maxWidth: 460,
    padding: 32,
  },
});
