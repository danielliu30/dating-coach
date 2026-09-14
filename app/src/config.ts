import { Platform } from 'react-native';

/**
 * Backend base URL. Override per environment with EXPO_PUBLIC_API_URL
 * (e.g. http://192.168.1.20:8080 so a phone on the LAN can reach your machine).
 */
const fallback = Platform.select({
  android: 'http://10.0.2.2:8080',
  default: 'http://localhost:8080',
}) as string;

/**
 * An empty EXPO_PUBLIC_API_URL means "same origin" (the web bundle is served
 * behind the same reverse proxy as the API), so resolve it from the page URL.
 */
function resolveBase(): string {
  const configured = process.env.EXPO_PUBLIC_API_URL;
  if (configured === undefined) return fallback;
  const trimmed = configured.replace(/\/+$/, '');
  if (trimmed !== '') return trimmed;
  if (Platform.OS === 'web' && typeof window !== 'undefined') return window.location.origin;
  return fallback;
}

export const API_BASE_URL = resolveBase();

export const API_PREFIX = '/api/v1';

/**
 * Google OAuth web client ID for "Continue with Google" (the same value the
 * API checks ID tokens against as GOOGLE_CLIENT_ID). Empty disables the
 * button; the email/password flow is unaffected.
 */
export const GOOGLE_CLIENT_ID: string = (process.env.EXPO_PUBLIC_GOOGLE_CLIENT_ID ?? '').trim();

export function wsURL(path: string, token: string): string {
  const base = API_BASE_URL.replace(/^http/, 'ws');
  return `${base}${API_PREFIX}${path}?token=${encodeURIComponent(token)}`;
}
