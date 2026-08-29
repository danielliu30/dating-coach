import { Platform } from 'react-native';

/**
 * Backend base URL. Override per environment with EXPO_PUBLIC_API_URL
 * (e.g. http://192.168.1.20:8080 so a phone on the LAN can reach your machine).
 */
const fallback = Platform.select({
  android: 'http://10.0.2.2:8080',
  default: 'http://localhost:8080',
}) as string;

export const API_BASE_URL = (process.env.EXPO_PUBLIC_API_URL ?? fallback).replace(/\/+$/, '');

export const API_PREFIX = '/api/v1';

export function wsURL(path: string, token: string): string {
  const base = API_BASE_URL.replace(/^http/, 'ws');
  return `${base}${API_PREFIX}${path}?token=${encodeURIComponent(token)}`;
}
