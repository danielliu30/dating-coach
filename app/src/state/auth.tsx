import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, api } from '../api/client';
import type { RenewalResult } from '../api/client';
import type { AuthSession, Profile, Role } from '../api/types';

const STORAGE_KEY = 'dating-coach.session';

/**
 * Why the app signed the user out without being asked to. `expired` is the
 * only involuntary case: the refresh token was refused, either because it aged
 * out or because a replay revoked the account's tokens.
 */
export type SignedOutReason = 'expired';

interface AuthState {
  ready: boolean;
  token: string | null;
  user: Profile | null;
  /** Set when the app signed the user out on its own, so a screen can say so. */
  signedOutReason: SignedOutReason | null;
  dismissSignedOutReason: () => void;
  signIn: (email: string, password: string) => Promise<Profile>;
  signUp: (input: { email: string; password: string; displayName: string; role: Role }) => Promise<Profile>;
  verify: (token: string) => Promise<Profile>;
  resendVerification: (email: string) => Promise<void>;
  refresh: () => Promise<void>;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

/**
 * Outcome of one refresh-token exchange. `stale` is the extra case the client
 * needs internally: the exchange was refused, but the session it asked about
 * had already been replaced, so the verdict is about a session no longer held.
 */
type RotationResult = RenewalResult | 'stale';

// Whether the stored access token is still worth sending. Restoring an expired
// one lands the user in authenticated tabs where every request 401s — and the
// chat WebSocket, which carries the token in its URL and never sees a 401,
// would just retry forever.
const isFresh = (stored: Partial<AuthSession>): boolean => {
  const expiry = Date.parse(stored.expires_at ?? '');
  return Number.isFinite(expiry) && expiry > Date.now();
};

// Whether the backend refused the refresh token, as opposed to the request not
// reaching it. Only a refusal is worth throwing the stored session away for.
const isRefused = (error: unknown): boolean => error instanceof ApiError && error.status === 401;

// Reads a stored session, or null when there is nothing usable to restore:
// unparseable JSON and a blob missing the token or the profile are both worth
// forgetting, and neither says anything about the backend.
const parseSession = (raw: string | null): (Partial<AuthSession> & Pick<AuthSession, 'token' | 'user'>) | null => {
  if (!raw) return null;
  try {
    const stored = JSON.parse(raw) as Partial<AuthSession>;
    return stored.token && stored.user ? { ...stored, token: stored.token, user: stored.user } : null;
  } catch {
    return null;
  }
};

export function AuthProvider({ children }: { children: React.ReactNode }): React.ReactElement {
  const [ready, setReady] = useState(false);
  const [token, setToken] = useState<string | null>(null);
  const [user, setUser] = useState<Profile | null>(null);
  const [signedOutReason, setSignedOutReason] = useState<SignedOutReason | null>(null);
  const tokenRef = useRef<string | null>(null);
  const refreshRef = useRef<string>('');
  const expiresRef = useRef<string>('');
  // Bumped whenever the session is replaced or dropped, so a renewal that lands
  // after a sign-out cannot write the old session back over it.
  const generationRef = useRef(0);
  // Identifies the party the app is acting for rather than the credential it
  // holds: bumped when a session starts, ends or changes hands, but not when a
  // renewal swaps the access token of the session already on hand. A request
  // that outlives its principal must never be replayed under the next one.
  const principalRef = useRef(0);
  const userIDRef = useRef<string | null>(null);

  // The client reads the token through a ref so requests always use the latest
  // one without re-creating the client on every render.
  tokenRef.current = token;
  useEffect(() => {
    api.useToken(() => tokenRef.current);
    api.usePrincipal(() => principalRef.current);
    // A token can also expire while the app is open; drop it centrally so the
    // UI leaves the authenticated tabs instead of failing every request.
    api.onSessionRejected(() => {
      // A request already in flight when the user signed out lands here with
      // nothing left to reject: dropping an absent session is a no-op, and
      // blaming an expiry for a sign-out the user asked for is a lie.
      if (!tokenRef.current) return;
      generationRef.current += 1;
      principalRef.current += 1;
      userIDRef.current = null;
      tokenRef.current = null;
      refreshRef.current = '';
      expiresRef.current = '';
      setToken(null);
      setUser(null);
      setSignedOutReason('expired');
      void AsyncStorage.removeItem(STORAGE_KEY).catch(() => undefined);
    });
  }, []);

  const persist = useCallback(async (session: AuthSession) => {
    generationRef.current += 1;
    if (userIDRef.current !== session.user.id) principalRef.current += 1;
    userIDRef.current = session.user.id;
    tokenRef.current = session.token;
    refreshRef.current = session.refresh_token ?? '';
    expiresRef.current = session.expires_at ?? '';
    setToken(session.token);
    setUser(session.user);
    setSignedOutReason(null);
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session));
    return session.user;
  }, []);

  /**
   * Exchanges refreshToken for a new session and installs it. Returns
   * `rejected` only when the backend refused a token the app still holds;
   * `stale` when it refused one that had already been replaced, and
   * `unavailable` when the answer never arrived or arrived too late to use.
   * An empty refreshToken counts as refused: there is nothing left to renew.
   */
  const rotate = useCallback(
    async (refreshToken: string): Promise<RotationResult> => {
      if (!refreshToken) return 'rejected';
      const generation = generationRef.current;
      try {
        const session = await api.refreshSession(refreshToken);
        // Signed out, or signed in as someone else, while this was in flight:
        // the session on hand now is newer than the one just minted.
        if (generation !== generationRef.current) return 'unavailable';
        await persist(session);
        return 'renewed';
      } catch (error) {
        if (!isRefused(error)) return 'unavailable';
        return generation === generationRef.current ? 'rejected' : 'stale';
      }
    },
    [persist],
  );

  // Access tokens last minutes, so the client renews them behind the app rather
  // than dropping the user onto the sign-in screen every quarter of an hour. A
  // refused refresh token is unrecoverable: the client signs the user out.
  useEffect(() => {
    api.useRenewal(async () => {
      const result = await rotate(refreshRef.current);
      if (result !== 'stale') return result;
      // The refusal was aimed at a session the app has already replaced, so it
      // says nothing about the replacement directly — except that a refusal
      // can be a replay, which revokes every refresh token the account has,
      // and the replacement may be one of them. Ask about it rather than
      // leaving the user on a session that is already dead.
      if (!refreshRef.current) return 'unavailable';
      const held = await rotate(refreshRef.current);
      return held === 'stale' ? 'unavailable' : held;
    });
  }, [rotate]);

  // Restores the stored session, renewing it up front when the app was closed
  // for longer than an access token lives, so nothing is handed a token that is
  // already expired.
  useEffect(() => {
    void (async () => {
      const forget = () => AsyncStorage.removeItem(STORAGE_KEY).catch(() => undefined);
      try {
        const raw = await AsyncStorage.getItem(STORAGE_KEY);
        const stored = parseSession(raw);
        if (!stored) {
          if (raw) await forget();
        } else if (isFresh(stored)) {
          await persist({
            token: stored.token,
            refresh_token: stored.refresh_token,
            expires_at: stored.expires_at ?? '',
            user: stored.user,
          });
        } else if (!stored.refresh_token) {
          await forget();
        } else {
          try {
            await persist(await api.refreshSession(stored.refresh_token));
          } catch (error) {
            // Only a refusal is final, and only a refusal is worth explaining:
            // an unreachable backend leaves the app signed out for now but
            // keeps the session for the next launch.
            if (isRefused(error)) {
              setSignedOutReason('expired');
              await forget();
            }
          }
        }
      } catch {
        // Storage itself failed; start signed out rather than leaving the app
        // on the loading screen.
      } finally {
        setReady(true);
      }
    })();
  }, [persist]);

  // Drops the session everywhere it is held: refs, state and storage.
  const clearSession = useCallback(async () => {
    generationRef.current += 1;
    principalRef.current += 1;
    userIDRef.current = null;
    tokenRef.current = null;
    refreshRef.current = '';
    expiresRef.current = '';
    setToken(null);
    setUser(null);
    setSignedOutReason(null);
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
        refresh_token: refreshRef.current,
        expires_at: expiresRef.current,
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
      signedOutReason,
      dismissSignedOutReason: () => setSignedOutReason(null),
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
    [clearSession, persist, persistUser, ready, signedOutReason, token, user],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const context = useContext(AuthContext);
  if (!context) throw new Error('useAuth must be used inside AuthProvider');
  return context;
}
