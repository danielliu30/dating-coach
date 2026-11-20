import AsyncStorage from '@react-native-async-storage/async-storage';
import * as SecureStore from 'expo-secure-store';
import { Platform } from 'react-native';

const STORAGE_KEY = 'dating-coach.session';

export const sessionStorage = {
  async read(): Promise<string | null> {
    if (Platform.OS === 'web') return AsyncStorage.getItem(STORAGE_KEY);

    const stored = await SecureStore.getItemAsync(STORAGE_KEY);
    if (stored !== null) {
      await AsyncStorage.removeItem(STORAGE_KEY);
      return stored;
    }

    const legacy = await AsyncStorage.getItem(STORAGE_KEY);
    if (legacy !== null) {
      await SecureStore.setItemAsync(STORAGE_KEY, legacy);
      await AsyncStorage.removeItem(STORAGE_KEY);
    }
    return legacy;
  },

  async write(value: string): Promise<void> {
    if (Platform.OS === 'web') {
      await AsyncStorage.setItem(STORAGE_KEY, value);
      return;
    }
    await SecureStore.setItemAsync(STORAGE_KEY, value);
    await AsyncStorage.removeItem(STORAGE_KEY);
  },

  async clear(): Promise<void> {
    if (Platform.OS === 'web') {
      await AsyncStorage.removeItem(STORAGE_KEY);
      return;
    }
    const results = await Promise.allSettled([
      SecureStore.deleteItemAsync(STORAGE_KEY),
      AsyncStorage.removeItem(STORAGE_KEY),
    ]);
    for (const result of results) {
      if (result.status === 'rejected') throw result.reason;
    }
  },
};
