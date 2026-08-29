import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { api } from '../api/client';
import type { AuthSession, Profile, Role } from '../api/types';

const STORAGE_KEY = 'dating-coach.session';

interface AuthState {
  ready: boolean;
  token: string | null;
  user: Profile | null;
  signIn: (email: string, password: string) => Promise<Profile>;
  signUp: (input: { email: string; password: string; displayName: string; role: Role }) => Promise<Profile>;
  verify: (token: string) => Promise<Profile>;
  resendVerification: (email: string) => Promise<void>;
  refresh: () => Promise<void>;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

// A persisted session is only usable while its token is still valid; restoring
// an expired one lands the user in authenticated tabs where every request 401s.
const isFresh = (expiresAt: string | undefined): boolean => {
  if (!expiresAt) return false;
  const expiry = Date.parse(expiresAt);
  return Number.isFinite(expiry) && expiry > Date.now();
};

export function AuthProvider({ children }: { children: React.ReactNode }): React.ReactElement {
  const [ready, setReady] = useState(false);
  const [token, setToken] = useState<string | null>(null);
  const [user, setUser] = useState<Profile | null>(null);
  const tokenRef = useRef<string | null>(null);
  const expiresRef = useRef<string>('');

  // The client reads the token through a ref so requests always use the latest
  // one without re-creating the client on every render.
  tokenRef.current = token;
  useEffect(() => {
    api.useToken(() => tokenRef.current);
    // A token can also expire while the app is open; drop it centrally so the
    // UI leaves the authenticated tabs instead of failing every request.
    api.onSessionRejected(() => {
      tokenRef.current = null;
      expiresRef.current = '';
      setToken(null);
      setUser(null);
      void AsyncStorage.removeItem(STORAGE_KEY).catch(() => undefined);
    });
  }, []);

  useEffect(() => {
    void (async () => {
      try {
        const raw = await AsyncStorage.getItem(STORAGE_KEY);
        const stored = raw ? (JSON.parse(raw) as Partial<AuthSession>) : null;
        if (stored?.token && stored.user && isFresh(stored.expires_at)) {
          tokenRef.current = stored.token;
          expiresRef.current = stored.expires_at ?? '';
          setToken(stored.token);
          setUser(stored.user);
        } else if (raw) {
          await AsyncStorage.removeItem(STORAGE_KEY);
        }
      } catch {
        // Unreadable or corrupt session: drop it and start signed out rather
        // than leaving the app stuck on the loading screen.
        await AsyncStorage.removeItem(STORAGE_KEY).catch(() => undefined);
      } finally {
        setReady(true);
      }
    })();
  }, []);

  const persist = useCallback(async (session: AuthSession) => {
    tokenRef.current = session.token;
    expiresRef.current = session.expires_at ?? '';
    setToken(session.token);
    setUser(session.user);
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session));
    return session.user;
  }, []);

  // Keeps a refreshed profile across restarts instead of only in memory.
  const persistUser = useCallback(async (profile: Profile) => {
    setUser(profile);
    if (!tokenRef.current) return profile;
    await AsyncStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({ token: tokenRef.current, expires_at: expiresRef.current, user: profile }),
    );
    return profile;
  }, []);

  const value = useMemo<AuthState>(
    () => ({
      ready,
      token,
      user,
      signIn: async (email, password) => persist(await api.signIn({ email, password })),
      signUp: async ({ email, password, displayName, role }) =>
        persist(await api.signUp({ email, password, display_name: displayName, role })),
      verify: async (verificationToken) => persistUser(await api.verifyEmail(verificationToken)),
      resendVerification: async (email) => {
        await api.resendVerification(email);
      },
      refresh: async () => {
        if (!tokenRef.current) return;
        await persistUser(await api.me());
      },
      signOut: async () => {
        tokenRef.current = null;
        expiresRef.current = '';
        setToken(null);
        setUser(null);
        await AsyncStorage.removeItem(STORAGE_KEY);
      },
    }),
    [persist, persistUser, ready, token, user],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const context = useContext(AuthContext);
  if (!context) throw new Error('useAuth must be used inside AuthProvider');
  return context;
}
