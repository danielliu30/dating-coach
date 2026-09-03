import React from 'react';
import { Image, ScrollView, StyleSheet, useWindowDimensions, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';

import { colors } from '../theme';

// Replace this with your own happy-couple asset when ready.
const COUPLE_IMAGE_URL =
  'https://images.unsplash.com/photo-1516589178581-6cd7833ae3b2?auto=format&fit=crop&w=1200&q=80';

export function AuthLayout({ children }: { children: React.ReactNode }): React.ReactElement {
  const { width } = useWindowDimensions();
  const showImage = width >= 900;

  return (
    <SafeAreaView style={styles.screen}>
      <View style={styles.container}>
        <ScrollView
          contentContainerStyle={styles.formScroll}
          keyboardShouldPersistTaps="handled"
          style={styles.formPanel}
        >
          <View style={styles.formContent}>{children}</View>
        </ScrollView>
        {showImage && (
          <View style={styles.imagePanel}>
            <Image source={{ uri: COUPLE_IMAGE_URL }} style={styles.image} resizeMode="cover" />
          </View>
        )}
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  screen: {
    flex: 1,
    backgroundColor: colors.surface,
  },
  container: {
    flex: 1,
    flexDirection: 'row',
  },
  formPanel: {
    flex: 1,
    backgroundColor: colors.surface,
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
  imagePanel: {
    flex: 1,
    backgroundColor: colors.bg,
  },
  image: {
    width: '100%',
    height: '100%',
  },
});
