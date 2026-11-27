import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { ApiError, api } from '../api/client';
import type { RenewalResult, SessionEndReason, SignOutScope } from '../api/client';
import type { AuthSession, DatingProfileInput, Profile, Role } from '../api/types';
import { requestGoogleIdToken } from '../lib/googleIdentity';
import { Platform } from 'react-native';
import { sessionStorage } from './sessionStorage';

// How long anything waits on a sign-out's server-side revocation, so a hung
// connection cannot stall the caller or the next sign-in.
export const SIGN_OUT_TIMEOUT_MS = 5000;

// Resolves once work settles or SIGN_OUT_TIMEOUT_MS passes, whichever is first.
const withinSignOutTimeout = async (work: Promise<unknown>): Promise<void> => {
  let timer: ReturnType<typeof setTimeout> | undefined;
  await Promise.race([
    work.catch(() => undefined),
    new Promise<void>((resolve) => {
      timer = setTimeout(resolve, SIGN_OUT_TIMEOUT_MS);
    }),
  ]);
  clearTimeout(timer);
};

// A session as stored: received_at is when the app obtained its access token,
// on the device's clock, and is absent from sessions stored before it was kept.
type StoredSession = AuthSession & { received_at?: number | null };

// Scopes a revocation to the moment the user signed out, or leaves it unscoped
// (taking effect as it lands) when the token's receipt time is unknown.
const scopeAt = (refreshToken: string, receivedAt: number, signedOutAt: number): SignOutScope | undefined =>
  refreshToken && Number.isFinite(receivedAt)
    ? { refresh_token: refreshToken, token_age_ms: Math.round(signedOutAt - receivedAt) }
    : undefined;

// Revokes the session these credentials belong to as of signedOutAt, so a
// request that lands late cannot end a session opened after it. It runs after
// the app has dropped them, so an expired access token is renewed here from
// the captured refresh token rather than through the shared renewal, which only
// knows the session on hand.
const revoke = async (token: string, refreshToken: string, receivedAt: number, signedOutAt: number): Promise<void> => {
  try {
    await api.signOut(token, scopeAt(refreshToken, receivedAt, signedOutAt));
    return;
  } catch (error) {
    if (!isRefused(error) || !refreshToken) return;
  }
  let renewed: AuthSession;
  let renewedAt: number;
  try {
    renewed = await api.refreshSession(refreshToken);
    renewedAt = Date.now();
  } catch (error) {
    // Usually a renewal in flight spent the token first, and the session it
    // minted is dropped unheld. Presenting the spent token again trips the
    // server's replay detection, which revokes every session the account has:
    // the same end state as the sign-out.
    if (isRefused(error)) await api.refreshSession(refreshToken).catch(() => undefined);
    return;
  }
  await api.signOut(renewed.token, scopeAt(renewed.refresh_token ?? '', renewedAt, signedOutAt));
};

/**
 * Why the app signed the user out without being asked to. `expired` is the
 * default reading of a refused refresh token; `revoked` is used only where the
 * API withdrew the session outright, which the user cannot fix by waiting.
 */
export type SignedOutReason = SessionEndReason;

interface AuthState {
  ready: boolean;
  token: string | null;
  user: Profile | null;
  /** Set when the app signed the user out on its own, so a screen can say so. */
  signedOutReason: SignedOutReason | null;
  dismissSignedOutReason: () => void;
  signIn: (email: string, password: string) => Promise<Profile>;
  signUp: (input: { email: string; password: string; displayName: string; role: Role }) => Promise<Profile>;
  /**
   * Runs the Google prompt and signs in as (or creates) the account for that
   * Google address. `role` only matters for a brand-new account. The resulting
   * profile is already verified, so the navigator enters the app directly.
   */
  signInWithGoogle: (role?: Role) => Promise<Profile>;
  verify: (email: string, code: string) => Promise<Profile>;
  resendVerification: (email: string) => Promise<{ status: string; resend_after_seconds?: number }>;
  refresh: () => Promise<void>;
  updateDatingProfile: (input: DatingProfileInput) => Promise<Profile>;
  deleteAccount: () => Promise<void>;
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

/**
 * Outcome of one refresh-token exchange. `stale` is the extra case the client
 * needs internally: the exchange was refused, but the session it asked about
 * had already been replaced, so the verdict is about a session no longer held.
 */
type RotationResult = RenewalResult | 'revoked' | 'stale';

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
const parseSession = (raw: string | null): (Partial<StoredSession> & Pick<AuthSession, 'token' | 'user'>) | null => {
  if (!raw) return null;
  try {
    const stored = JSON.parse(raw) as Partial<StoredSession>;
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
  // When the access token on hand was obtained, on the device's clock; NaN when
  // unknown.
  const receivedRef = useRef<number>(Number.NaN);
  // Bumped whenever the session is replaced or dropped, so a renewal that lands
  // after a sign-out cannot write the old session back over it.
  const generationRef = useRef(0);
  // Identifies the party the app is acting for rather than the credential it
  // holds: bumped when a session starts, ends or changes hands, but not when a
  // renewal swaps the access token of the session already on hand. A request
  // that outlives its principal must never be replayed under the next one.
  const principalRef = useRef(0);
  const userIDRef = useRef<string | null>(null);
  // The last sign-out's server-side revocation. A sign-in waits for it, so a
  // revocation that lands late cannot take the new session down with it.
  const revocationRef = useRef<Promise<void>>(Promise.resolve());

  // The client reads the token through a ref so requests always use the latest
  // one without re-creating the client on every render.
  tokenRef.current = token;
  useEffect(() => {
    api.useToken(() => tokenRef.current);
    api.usePrincipal(() => principalRef.current);
    // A token can also expire while the app is open; drop it centrally so the
    // UI leaves the authenticated tabs instead of failing every request.
    api.onSessionRejected((reason) => {
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
      receivedRef.current = Number.NaN;
      setToken(null);
      setUser(null);
      setSignedOutReason(reason);
      void sessionStorage.clear().catch(() => undefined);
    });
  }, []);

  const persist = useCallback(async (session: AuthSession, receivedAt: number = Date.now()) => {
    generationRef.current += 1;
    if (userIDRef.current !== session.user.id) principalRef.current += 1;
    userIDRef.current = session.user.id;
    tokenRef.current = session.token;
    refreshRef.current = session.refresh_token ?? '';
    expiresRef.current = session.expires_at ?? '';
    receivedRef.current = receivedAt;
    setToken(session.token);
    setUser(session.user);
    setSignedOutReason(null);
    const record: StoredSession = { ...session, received_at: receivedAt };
    // On web the refresh token rides an httpOnly cookie instead; keeping it
    // out of JS-readable storage is the whole point, so the copy in memory
    // (refreshRef) is the only one this tab holds.
    if (Platform.OS === 'web') record.refresh_token = undefined;
    await sessionStorage.write(JSON.stringify(record));
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
      if (!refreshToken && Platform.OS !== 'web') return 'rejected';
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
        if (generation !== generationRef.current) return 'stale';
        return error instanceof ApiError && error.message === 'session revoked' ? 'revoked' : 'rejected';
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
      const forget = () => sessionStorage.clear().catch(() => undefined);
      try {
        const raw = await sessionStorage.read();
        const stored = parseSession(raw);
        if (!stored) {
          if (raw) await forget();
        } else if (isFresh(stored)) {
          await persist(
            {
              token: stored.token,
              refresh_token: stored.refresh_token,
              expires_at: stored.expires_at ?? '',
              user: stored.user,
            },
            stored.received_at ?? Number.NaN,
          );
        } else if (!stored.refresh_token && Platform.OS !== 'web') {
          await forget();
        } else {
          try {
            // An empty token is fine on web: the httpOnly cookie carries it.
            await persist(await api.refreshSession(stored.refresh_token ?? ''));
          } catch (error) {
            // Only a refusal is final, and only a refusal is worth explaining:
            // an unreachable backend leaves the app signed out for now but
            // keeps the session for the next launch.
            if (isRefused(error)) {
              setSignedOutReason(error instanceof ApiError && error.message === 'session revoked' ? 'revoked' : 'expired');
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
    receivedRef.current = Number.NaN;
    setToken(null);
    setUser(null);
    setSignedOutReason(null);
    await sessionStorage.clear();
  }, []);

  // Keeps a refreshed profile across restarts instead of only in memory.
  const persistUser = useCallback(async (profile: Profile) => {
    setUser(profile);
    if (!tokenRef.current) return profile;
    await sessionStorage.write(
      JSON.stringify({
        token: tokenRef.current,
        refresh_token: refreshRef.current,
        expires_at: expiresRef.current,
        received_at: receivedRef.current,
        user: profile,
      } satisfies StoredSession),
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
      signIn: async (email, password) => {
        await withinSignOutTimeout(revocationRef.current);
        return persist(await api.signIn({ email, password }));
      },
      signUp: async ({ email, password, displayName, role }) => {
        await withinSignOutTimeout(revocationRef.current);
        return persist(await api.signUp({ email, password, display_name: displayName, role }));
      },
      signInWithGoogle: async (role) => {
        const idToken = await requestGoogleIdToken();
        await withinSignOutTimeout(revocationRef.current);
        return persist(await api.signInWithGoogle({ id_token: idToken, role }));
      },
      verify: async (email, code) => {
        // Verifying as the account being verified swaps the sign-up token for a
        // session one; the sign-up token cannot reach the private API the app is
        // about to show. An empty token means the backend did not recognise the
        // caller as that account (signed out, expired, or a different account),
        // so any session on hand is dropped rather than paired with the
        // verified profile.
        const sent = tokenRef.current;
        const session = await api.verifyEmail(email, code);
        if (session.token) return persist(session);
        if (tokenRef.current === sent) await clearSession();
        return session.user;
      },
      resendVerification: async (email) => {
        return api.resendVerification(email);
      },
      refresh: async () => {
        if (!tokenRef.current) return;
        await persistUser(await api.me());
      },
      // A response that lands after the session changed hands (signed out, or
      // into another account) is returned but not persisted, so it cannot be
      // paired with a token that belongs to someone else.
      updateDatingProfile: async (input) => {
        const sent = principalRef.current;
        const profile = await api.updateDatingProfile(input);
        if (principalRef.current !== sent) return profile;
        return persistUser(profile);
      },
      // The deletion is certain once this resolves, and the token on hand can no
      // longer be renewed, so the session is dropped locally rather than left to
      // outlive the account for the minutes its access token has left.
      deleteAccount: async () => {
        await api.deleteAccount();
        // The account is already gone by here, so failing to erase the stored
        // copy of the session must not be reported as a failed deletion: a
        // session restored from it cannot be renewed and is dropped.
        await clearSession().catch(() => undefined);
      },
      // Drops the session locally first, so nothing a closed app or a second
      // tap does can bring it back, then revokes it server-side so its refresh
      // token stops working. A failed revocation still leaves this device
      // signed out.
      signOut: async () => {
        const token = tokenRef.current;
        if (token) {
          revocationRef.current = revoke(token, refreshRef.current, receivedRef.current, Date.now()).catch(
            () => undefined,
          );
        }
        try {
          await clearSession();
        } finally {
          await withinSignOutTimeout(revocationRef.current);
        }
      },
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
