import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, api } from '../api/client';
import type { RenewalOutcome } from '../api/client';
import type { AuthSession, Profile, Role } from '../api/types';

const STORAGE_KEY = 'dating-coach.session';

/**
 * Why the app dropped a session on its own. 'expired' means the refresh token
 * ran out its lifetime; 'revoked' means it was refused while it should still
 * have been valid, which is what deleting the account or ending the session
 * elsewhere looks like from here. An explicit sign-out leaves this null.
 */
export type SessionEndedReason = 'expired' | 'revoked';

interface AuthState {
  ready: boolean;
  token: string | null;
  user: Profile | null;
  /** Set when the app signed the user out for them; cleared by acknowledge. */
  endedReason: SessionEndedReason | null;
  acknowledgeSessionEnded: () => void;
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
  const [endedReason, setEndedReason] = useState<SessionEndedReason | null>(null);
  const tokenRef = useRef<string | null>(null);
  const expiresRef = useRef<string>('');
  const refreshTokenRef = useRef<string | null>(null);
  const refreshExpiresRef = useRef<string>('');
  // One in-flight renewal is shared by every request that hits a 401 at once,
  // so the rotating refresh token is spent by a single exchange.
  const renewalRef = useRef<Promise<RenewalOutcome> | null>(null);

  // The client reads the token through a ref so requests always use the latest
  // one without re-creating the client on every render.
  tokenRef.current = token;

  // Writes a session everywhere it is held: refs, state and storage. Kept out
  // of the render body so the renewal handler registered once on mount can use
  // it, and declared before the effects that call it.
  const persist = useCallback(async (session: AuthSession) => {
    tokenRef.current = session.token;
    expiresRef.current = session.expires_at ?? '';
    refreshTokenRef.current = session.refresh_token ?? null;
    refreshExpiresRef.current = session.refresh_expires_at ?? '';
    setToken(session.token);
    setUser(session.user);
    setEndedReason(null);
    try {
      await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session));
    } catch (error) {
      // The stored session is now an older rotation than the live one, and its
      // refresh token has already been spent: replaying it on the next start
      // would look like a leak and revoke this session's whole family. Drop it
      // so a restart begins signed out instead of ending the live session.
      await AsyncStorage.removeItem(STORAGE_KEY).catch(() => undefined);
      throw error;
    }
    return session.user;
  }, []);

  useEffect(() => {
    api.useToken(() => tokenRef.current);
    // Access tokens last minutes, so a 401 is normally just expiry: trade the
    // refresh token for a new pair and let the client retry.
    api.onAccessTokenExpired(() => {
      if (renewalRef.current) return renewalRef.current;
      const stored = refreshTokenRef.current;
      if (!stored) return Promise.resolve<RenewalOutcome>('invalid');
      renewalRef.current = api
        .refresh(stored)
        .then(async (session): Promise<RenewalOutcome> => {
          // Signing out or signing in elsewhere while the exchange was in
          // flight wins: reviving the session it replaced would sign the user
          // back in behind their back.
          if (refreshTokenRef.current !== stored) return 'unavailable';
          await persist(session);
          return 'renewed';
        })
        // Only a rejected refresh token ends the session; an outage or a failed
        // write leaves it in place to be retried.
        .catch((error: unknown): RenewalOutcome =>
          error instanceof ApiError && error.status === 401 ? 'invalid' : 'unavailable',
        )
        .finally(() => {
          renewalRef.current = null;
        });
      return renewalRef.current;
    });
    // A token can also expire while the app is open; drop it centrally so the
    // UI leaves the authenticated tabs instead of failing every request, and
    // record why so the sign-in screen can explain the eviction. A refresh
    // token refused before its own expiry was withdrawn server-side.
    api.onSessionRejected(() => {
      setEndedReason(isFresh(refreshExpiresRef.current) ? 'revoked' : 'expired');
      tokenRef.current = null;
      expiresRef.current = '';
      refreshTokenRef.current = null;
      refreshExpiresRef.current = '';
      setToken(null);
      setUser(null);
      void AsyncStorage.removeItem(STORAGE_KEY).catch(() => undefined);
    });
  }, [persist]);

  useEffect(() => {
    void (async () => {
      try {
        const raw = await AsyncStorage.getItem(STORAGE_KEY);
        const stored = raw ? (JSON.parse(raw) as Partial<AuthSession>) : null;
        // A stale access token is fine to restore as long as the refresh token
        // behind it still lives: the first request renews it.
        if (
          stored?.token &&
          stored.user &&
          (isFresh(stored.expires_at) || isFresh(stored.refresh_expires_at))
        ) {
          tokenRef.current = stored.token;
          expiresRef.current = stored.expires_at ?? '';
          refreshTokenRef.current = stored.refresh_token ?? null;
          refreshExpiresRef.current = stored.refresh_expires_at ?? '';
          setToken(stored.token);
          setUser(stored.user);
        } else if (raw) {
          // The app was left closed past the refresh token's lifetime.
          setEndedReason('expired');
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

  // Drops the session everywhere it is held: refs, state and storage. The
  // eviction notice is cleared too, since this path is the user's own doing.
  const clearSession = useCallback(async () => {
    setEndedReason(null);
    tokenRef.current = null;
    expiresRef.current = '';
    refreshTokenRef.current = null;
    refreshExpiresRef.current = '';
    setToken(null);
    setUser(null);
    await AsyncStorage.removeItem(STORAGE_KEY);
  }, []);

  // Keeps a refreshed profile across restarts instead of only in memory.
  const persistUser = useCallback(async (profile: Profile) => {
    setUser(profile);
    if (!tokenRef.current) return profile;
    await AsyncStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        token: tokenRef.current,
        expires_at: expiresRef.current,
        refresh_token: refreshTokenRef.current ?? undefined,
        refresh_expires_at: refreshExpiresRef.current,
        user: profile,
      }),
    );
    return profile;
  }, []);

  const value = useMemo<AuthState>(
    () => ({
      ready,
      token,
      user,
      endedReason,
      acknowledgeSessionEnded: () => setEndedReason(null),
      signIn: async (email, password) => persist(await api.signIn({ email, password })),
      signUp: async ({ email, password, displayName, role }) =>
        persist(await api.signUp({ email, password, display_name: displayName, role })),
      verify: async (verificationToken) => {
        // Verifying as the account being verified swaps the sign-up token for a
        // session one; the sign-up token cannot reach the private API the app is
        // about to show. An empty token means the backend did not recognise the
        // caller as that account (signed out, expired, or a different account),
        // so any session on hand is dropped rather than paired with the
        // verified profile.
        const sent = tokenRef.current;
        const session = await api.verifyEmail(verificationToken);
        if (session.token) return persist(session);
        if (tokenRef.current === sent) await clearSession();
        return session.user;
      },
      resendVerification: async (email) => {
        await api.resendVerification(email);
      },
      refresh: async () => {
        if (!tokenRef.current) return;
        await persistUser(await api.me());
      },
      signOut: clearSession,
    }),
    [clearSession, endedReason, persist, persistUser, ready, token, user],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const context = useContext(AuthContext);
  if (!context) throw new Error('useAuth must be used inside AuthProvider');
  return context;
}
