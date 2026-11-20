import AsyncStorage from '@react-native-async-storage/async-storage';
import * as SecureStore from 'expo-secure-store';
import { Platform } from 'react-native';

import { sessionStorage } from './sessionStorage';

jest.mock('expo-secure-store', () => ({
  getItemAsync: jest.fn(),
  setItemAsync: jest.fn(),
  deleteItemAsync: jest.fn(),
}));

const secure = jest.mocked(SecureStore);
const KEY = 'dating-coach.session';
const record = JSON.stringify({ token: 'access', refresh_token: 'refresh', user: { id: 'u1' } });

beforeEach(async () => {
  jest.clearAllMocks();
  await AsyncStorage.clear();
  secure.getItemAsync.mockResolvedValue(null);
  secure.setItemAsync.mockResolvedValue(undefined);
  secure.deleteItemAsync.mockResolvedValue(undefined);
});

afterEach(() => jest.restoreAllMocks());

describe.each(['ios', 'android'] as const)('%s session storage', (platform) => {
  beforeEach(() => jest.replaceProperty(Platform, 'OS', platform));

  it('writes access and refresh credentials only to secure storage', async () => {
    await AsyncStorage.setItem('preferences', 'keep');
    await sessionStorage.write(record);
    expect(secure.setItemAsync).toHaveBeenCalledWith(KEY, record);
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
    expect(await AsyncStorage.getItem('preferences')).toBe('keep');
  });

  it('reads secure storage and removes any stale legacy credentials', async () => {
    secure.getItemAsync.mockResolvedValue(record);
    await AsyncStorage.setItem(KEY, 'old credentials');
    await expect(sessionStorage.read()).resolves.toBe(record);
    expect(secure.getItemAsync).toHaveBeenCalledWith(KEY);
    expect(secure.setItemAsync).not.toHaveBeenCalled();
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
  });

  it('migrates a legacy session on read before deleting the old entry', async () => {
    await AsyncStorage.setItem(KEY, record);
    secure.setItemAsync.mockImplementationOnce(async () => {
      expect(await AsyncStorage.getItem(KEY)).toBe(record);
    });
    await expect(sessionStorage.read()).resolves.toBe(record);
    expect(secure.setItemAsync).toHaveBeenCalledWith(KEY, record);
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
    secure.getItemAsync.mockResolvedValue(record);
    await expect(sessionStorage.read()).resolves.toBe(record);
    expect(secure.setItemAsync).toHaveBeenCalledTimes(1);
  });

  it('returns null when neither store has a session', async () => {
    await expect(sessionStorage.read()).resolves.toBeNull();
    expect(secure.setItemAsync).not.toHaveBeenCalled();
  });

  it('clears both stores without touching other AsyncStorage data', async () => {
    await AsyncStorage.setItem(KEY, record);
    await AsyncStorage.setItem('preferences', 'keep');
    await sessionStorage.clear();
    expect(secure.deleteItemAsync).toHaveBeenCalledWith(KEY);
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
    expect(await AsyncStorage.getItem('preferences')).toBe('keep');
  });

  it('preserves the legacy entry if migration cannot write securely', async () => {
    await AsyncStorage.setItem(KEY, record);
    secure.setItemAsync.mockRejectedValueOnce(new Error('keychain locked'));
    await expect(sessionStorage.read()).rejects.toThrow('keychain locked');
    expect(await AsyncStorage.getItem(KEY)).toBe(record);
  });

  it('does not fall back to insecure storage when a secure read fails', async () => {
    secure.getItemAsync.mockRejectedValueOnce(new Error('keychain locked'));
    await expect(sessionStorage.read()).rejects.toThrow('keychain locked');
    expect(AsyncStorage.getItem).not.toHaveBeenCalled();
  });

  it('does not fall back to insecure storage when a secure write fails', async () => {
    secure.setItemAsync.mockRejectedValueOnce(new Error('keychain locked'));
    await expect(sessionStorage.write(record)).rejects.toThrow('keychain locked');
    expect(AsyncStorage.setItem).not.toHaveBeenCalled();
  });

  it('still clears legacy storage when secure deletion fails', async () => {
    await AsyncStorage.setItem(KEY, record);
    secure.deleteItemAsync.mockRejectedValueOnce(new Error('keychain locked'));
    await expect(sessionStorage.clear()).rejects.toThrow('keychain locked');
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
  });

  it('still clears secure storage when legacy deletion fails', async () => {
    jest.spyOn(AsyncStorage, 'removeItem').mockRejectedValueOnce(new Error('disk'));
    await expect(sessionStorage.clear()).rejects.toThrow('disk');
    expect(secure.deleteItemAsync).toHaveBeenCalledWith(KEY);
  });
});

describe('web session storage', () => {
  beforeEach(() => jest.replaceProperty(Platform, 'OS', 'web'));

  it('reads, writes and clears using the existing AsyncStorage key', async () => {
    await expect(sessionStorage.read()).resolves.toBeNull();
    await sessionStorage.write(record);
    expect(await AsyncStorage.getItem(KEY)).toBe(record);
    await expect(sessionStorage.read()).resolves.toBe(record);
    await sessionStorage.clear();
    await expect(sessionStorage.read()).resolves.toBeNull();
    expect(secure.getItemAsync).not.toHaveBeenCalled();
    expect(secure.setItemAsync).not.toHaveBeenCalled();
    expect(secure.deleteItemAsync).not.toHaveBeenCalled();
  });

  it('propagates storage errors', async () => {
    jest.spyOn(AsyncStorage, 'getItem').mockRejectedValueOnce(new Error('disk'));
    await expect(sessionStorage.read()).rejects.toThrow('disk');
  });
});
