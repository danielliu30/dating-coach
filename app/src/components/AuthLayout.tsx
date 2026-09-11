import React from 'react';
import { Image, ScrollView, StyleSheet, useWindowDimensions, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors, elevation, radii } from '../theme';

// Replace this with your own happy-couple asset when ready.
const COUPLE_IMAGE_URL =
  'https://images.unsplash.com/photo-1516589178581-6cd7833ae3b2?auto=format&fit=crop&w=1200&q=80';

export function AuthLayout({ children }: { children: React.ReactNode }): React.ReactElement {
  const { width } = useWindowDimensions();
  const showImage = width >= 900;

  return (
    <SafeAreaView style={styles.screen}>
      <View pointerEvents="none" style={styles.glowTop} />
      <View pointerEvents="none" style={styles.glowBottom} />
      <View style={styles.container}>
        <ScrollView
          contentContainerStyle={styles.formScroll}
          keyboardShouldPersistTaps="handled"
          style={styles.formPanel}
        >
          <View style={[styles.formContent, showImage && styles.formCard]}>{children}</View>
        </ScrollView>
        {showImage && (
          <View style={styles.imagePanel}>
            <View style={styles.imageClip}>
              <Image source={{ uri: COUPLE_IMAGE_URL }} style={styles.image} resizeMode="cover" />
              <View pointerEvents="none" style={styles.imageTintTop} />
              <View pointerEvents="none" style={styles.imageTintBottom} />
              <View pointerEvents="none" style={styles.panelSeam} />
            </View>
          </View>
        )}
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  screen: {
    flex: 1,
    backgroundColor: colors.bg,
    overflow: 'hidden',
  },
  glowTop: {
    position: 'absolute',
    top: -180,
    left: -120,
    width: 460,
    height: 460,
    borderRadius: 230,
    backgroundColor: colors.primaryTint,
    opacity: 0.7,
  },
  glowBottom: {
    position: 'absolute',
    bottom: -220,
    right: -100,
    width: 520,
    height: 520,
    borderRadius: 260,
    backgroundColor: colors.surfaceSunken,
    opacity: 0.6,
  },
  container: {
    flex: 1,
    flexDirection: 'row',
  },
  formPanel: {
    flex: 1,
  },
  formScroll: {
    flexGrow: 1,
    justifyContent: 'center',
    padding: 24,
  },
  formContent: {
    width: '100%',
    maxWidth: 420,
    alignSelf: 'center',
    gap: 16,
  },
  formCard: {
    maxWidth: 460,
    padding: 32,
    borderRadius: radii.xl,
    backgroundColor: colors.surface,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: 'rgba(59, 20, 32, 0.06)',
    ...elevation.high,
  },
  imagePanel: {
    flex: 1,
    backgroundColor: colors.surfaceSunken,
    borderTopLeftRadius: radii.xl,
    borderBottomLeftRadius: radii.xl,
    ...elevation.high,
  },
  imageClip: {
    flex: 1,
    borderTopLeftRadius: radii.xl,
    borderBottomLeftRadius: radii.xl,
    overflow: 'hidden',
  },
  image: {
    width: '100%',
    height: '100%',
  },
  imageTintTop: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    height: '45%',
    backgroundColor: colors.primaryDeep,
    opacity: 0.18,
  },
  imageTintBottom: {
    position: 'absolute',
    bottom: 0,
    left: 0,
    right: 0,
    height: '55%',
    backgroundColor: colors.shadow,
    opacity: 0.28,
  },
  panelSeam: {
    position: 'absolute',
    top: 0,
    bottom: 0,
    left: 0,
    width: 24,
    backgroundColor: colors.shadow,
    opacity: 0.12,
  },
});
