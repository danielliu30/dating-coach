import AsyncStorage from '@react-native-async-storage/async-storage';
import { act, renderHook, waitFor } from '@testing-library/react-native';
import React from 'react';

import { ApiError, api } from '../api/client';
import type { AuthSession, Profile } from '../api/types';
import { AuthProvider, useAuth } from './auth';

jest.mock('../api/client', () => {
  const actual = jest.requireActual<typeof import('../api/client')>('../api/client');
  return {
    ApiError: actual.ApiError,
    api: {
      useToken: jest.fn(),
      usePrincipal: jest.fn(),
      useRenewal: jest.fn(),
      onSessionRejected: jest.fn(),
      signIn: jest.fn(),
      signUp: jest.fn(),
      refreshSession: jest.fn(),
      verifyEmail: jest.fn(),
      resendVerification: jest.fn(),
      me: jest.fn(),
      updateDatingProfile: jest.fn(),
      deleteAccount: jest.fn(),
    },
  };
});

const mocked = api as jest.Mocked<typeof api>;
const STORAGE_KEY = 'dating-coach.session';

const user: Profile = {
  id: 'u1',
  email: 'a@b.c',
  display_name: 'A',
  role: 'user',
  email_verified: true,
  dating_styles: [],
  phases_strong: [],
  phases_working_on: [],
  dating_preferences: '',
};

const inOneHour = () => new Date(Date.now() + 3600_000).toISOString();
const anHourAgo = () => new Date(Date.now() - 3600_000).toISOString();

const session = (overrides: Partial<AuthSession> = {}): AuthSession => ({
  token: 'access',
  refresh_token: 'refresh',
  expires_at: inOneHour(),
  user,
  ...overrides,
});

const wrapper = ({ children }: { children: React.ReactNode }) => <AuthProvider>{children}</AuthProvider>;

/** Renders useAuth under a provider and waits for the stored-session restore to finish. */
async function renderAuth() {
  const rendered = renderHook(() => useAuth(), { wrapper });
  await waitFor(() => expect(rendered.result.current.ready).toBe(true));
  return rendered;
}

const stored = async () => JSON.parse((await AsyncStorage.getItem(STORAGE_KEY)) ?? 'null') as AuthSession | null;

/** Returns the renewal callback the provider registered on the api client. */
const registeredRenewal = () => {
  const call = mocked.useRenewal.mock.calls.at(-1);
  if (!call) throw new Error('useRenewal was not registered');
  return call[0];
};

/** Returns the rejection handler the provider registered on the api client. */
const registeredRejection = () => {
  const call = mocked.onSessionRejected.mock.calls.at(-1);
  if (!call) throw new Error('onSessionRejected was not registered');
  return call[0];
};

beforeEach(async () => {
  await AsyncStorage.clear();
});

describe('AuthProvider sign in / sign up', () => {
  it('signIn persists the session under the storage key and exposes token + user', async () => {
    mocked.signIn.mockResolvedValue(session());
    const { result } = await renderAuth();

    await act(async () => {
      await result.current.signIn('a@b.c', 'pw');
    });

    expect(mocked.signIn).toHaveBeenCalledWith({ email: 'a@b.c', password: 'pw' });
    expect(result.current.token).toBe('access');
    expect(result.current.user).toEqual(user);
    expect(await stored()).toMatchObject({ token: 'access', refresh_token: 'refresh' });
  });

  it('signUp maps displayName to display_name and persists', async () => {
    mocked.signUp.mockResolvedValue(session({ token: 'signup', refresh_token: undefined }));
    const { result } = await renderAuth();

    await act(async () => {
      await result.current.signUp({ email: 'a@b.c', password: 'pw', displayName: 'A', role: 'coach' });
    });

    expect(mocked.signUp).toHaveBeenCalledWith({ email: 'a@b.c', password: 'pw', display_name: 'A', role: 'coach' });
    expect(result.current.token).toBe('signup');
    expect(await stored()).toMatchObject({ token: 'signup' });
  });

  it('registers token and principal providers that follow the session', async () => {
    mocked.signIn.mockResolvedValue(session());
    const { result } = await renderAuth();
    const token = mocked.useToken.mock.calls.at(-1)?.[0];
    const principal = mocked.usePrincipal.mock.calls.at(-1)?.[0];
    if (!token || !principal) throw new Error('providers not registered');

    expect(token()).toBeNull();
    const before = principal();
    await act(async () => {
      await result.current.signIn('a@b.c', 'pw');
    });
    expect(token()).toBe('access');
    expect(principal()).not.toBe(before);
  });
});

describe('AuthProvider restore', () => {
  it('restores a fresh stored session without hitting the API', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    const { result } = await renderAuth();

    expect(result.current.token).toBe('access');
    expect(result.current.user).toEqual(user);
    expect(mocked.refreshSession).not.toHaveBeenCalled();
  });

  it('rotates an expired stored session through refreshSession', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session({ token: 'stale', expires_at: anHourAgo() })));
    mocked.refreshSession.mockResolvedValue(session({ token: 'rotated', refresh_token: 'refresh2' }));
    const { result } = await renderAuth();

    expect(mocked.refreshSession).toHaveBeenCalledWith('refresh');
    expect(result.current.token).toBe('rotated');
    expect(await stored()).toMatchObject({ token: 'rotated', refresh_token: 'refresh2' });
  });

  it('a refused refresh on restore forgets the session and reports expired', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session({ expires_at: anHourAgo() })));
    mocked.refreshSession.mockRejectedValue(new ApiError(401, 'spent'));
    const { result } = await renderAuth();

    expect(result.current.token).toBeNull();
    expect(result.current.signedOutReason).toBe('expired');
    expect(await stored()).toBeNull();
  });

  it('an unreachable backend on restore keeps the stored session for next launch', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session({ expires_at: anHourAgo() })));
    mocked.refreshSession.mockRejectedValue(new TypeError('network'));
    const { result } = await renderAuth();

    expect(result.current.token).toBeNull();
    expect(result.current.signedOutReason).toBeNull();
    expect(await stored()).not.toBeNull();
  });

  it('forgets an expired session that has no refresh token', async () => {
    await AsyncStorage.setItem(
      STORAGE_KEY,
      JSON.stringify(session({ refresh_token: undefined, expires_at: anHourAgo() })),
    );
    const { result } = await renderAuth();

    expect(result.current.token).toBeNull();
    expect(mocked.refreshSession).not.toHaveBeenCalled();
    expect(await stored()).toBeNull();
  });

  it('forgets unparseable or incomplete stored blobs', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, '{not json');
    const first = await renderAuth();
    expect(first.result.current.token).toBeNull();
    expect(await stored()).toBeNull();
    first.unmount();

    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify({ token: 'x' }));
    const second = await renderAuth();
    expect(second.result.current.token).toBeNull();
    expect(await stored()).toBeNull();
  });
});

describe('AuthProvider verify', () => {
  it('installs the session returned by verify', async () => {
    mocked.verifyEmail.mockResolvedValue(session({ token: 'verified' }));
    const { result } = await renderAuth();

    await act(async () => {
      await result.current.verify('a@b.c', '123456');
    });

    expect(mocked.verifyEmail).toHaveBeenCalledWith('a@b.c', '123456');
    expect(result.current.token).toBe('verified');
  });

  it('clears the session on hand when verify returns an empty token', async () => {
    mocked.signUp.mockResolvedValue(session({ token: 'signup', refresh_token: undefined }));
    mocked.verifyEmail.mockResolvedValue(session({ token: '', refresh_token: undefined }));
    const { result } = await renderAuth();
    await act(async () => {
      await result.current.signUp({ email: 'a@b.c', password: 'pw', displayName: 'A', role: 'user' });
    });
    expect(result.current.token).toBe('signup');

    let returned: Profile | null = null;
    await act(async () => {
      returned = await result.current.verify('a@b.c', '123456');
    });

    expect(returned).toEqual(user);
    expect(result.current.token).toBeNull();
    expect(await stored()).toBeNull();
  });

  it('resendVerification forwards the email', async () => {
    mocked.resendVerification.mockResolvedValue({ status: 'sent' });
    const { result } = await renderAuth();
    await act(async () => {
      await result.current.resendVerification(' a@b.c ');
    });
    expect(mocked.resendVerification).toHaveBeenCalledWith(' a@b.c ');
  });
});

describe('AuthProvider sign out / delete', () => {
  it('signOut clears state and storage without a signed-out reason', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    const { result } = await renderAuth();

    await act(async () => {
      await result.current.signOut();
    });

    expect(result.current.token).toBeNull();
    expect(result.current.user).toBeNull();
    expect(result.current.signedOutReason).toBeNull();
    expect(await stored()).toBeNull();
  });

  it('deleteAccount clears the session even when storage removal fails', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    mocked.deleteAccount.mockResolvedValue({ status: 'deleting' });
    const { result } = await renderAuth();
    (AsyncStorage.removeItem as jest.Mock).mockRejectedValueOnce(new Error('disk'));

    await act(async () => {
      await expect(result.current.deleteAccount()).resolves.toBeUndefined();
    });

    expect(mocked.deleteAccount).toHaveBeenCalledTimes(1);
    expect(result.current.token).toBeNull();
    expect(result.current.user).toBeNull();
  });

  it('deleteAccount leaves the session in place when the API call fails', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    mocked.deleteAccount.mockRejectedValue(new ApiError(500, 'nope'));
    const { result } = await renderAuth();

    await act(async () => {
      await expect(result.current.deleteAccount()).rejects.toBeInstanceOf(ApiError);
    });

    expect(result.current.token).toBe('access');
  });

  it('a rejected session reported by the client signs out with the given reason', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    const { result } = await renderAuth();

    act(() => registeredRejection()('revoked'));

    expect(result.current.token).toBeNull();
    expect(result.current.signedOutReason).toBe('revoked');
    await waitFor(async () => expect(await stored()).toBeNull());

    act(() => result.current.dismissSignedOutReason());
    expect(result.current.signedOutReason).toBeNull();
  });

  it('a rejection arriving after sign-out is ignored', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    const { result } = await renderAuth();
    await act(async () => {
      await result.current.signOut();
    });

    act(() => registeredRejection()('expired'));
    expect(result.current.signedOutReason).toBeNull();
  });
});

describe('AuthProvider renewal', () => {
  it('the registered renewal rotates the held refresh token and installs the result', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    mocked.refreshSession.mockResolvedValue(session({ token: 'renewed', refresh_token: 'refresh2' }));
    const { result } = await renderAuth();

    let outcome: string | undefined;
    await act(async () => {
      outcome = await registeredRenewal()();
    });

    expect(outcome).toBe('renewed');
    expect(mocked.refreshSession).toHaveBeenCalledWith('refresh');
    expect(result.current.token).toBe('renewed');
  });

  it('reports rejected for a refused refresh and unavailable for a network failure', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    await renderAuth();

    mocked.refreshSession.mockRejectedValueOnce(new ApiError(401, 'spent'));
    await expect(registeredRenewal()()).resolves.toBe('rejected');

    mocked.refreshSession.mockRejectedValueOnce(new TypeError('offline'));
    await expect(registeredRenewal()()).resolves.toBe('unavailable');
  });

  it('reports rejected without a refresh token to renew', async () => {
    await renderAuth();
    await expect(registeredRenewal()()).resolves.toBe('rejected');
    expect(mocked.refreshSession).not.toHaveBeenCalled();
  });

  it('a renewal that lands after sign-out does not write the old session back', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    let settle!: (value: AuthSession) => void;
    mocked.refreshSession.mockReturnValue(
      new Promise<AuthSession>((resolve) => {
        settle = resolve;
      }),
    );
    const { result } = await renderAuth();

    const renewal = registeredRenewal()();
    await act(async () => {
      await result.current.signOut();
    });
    let outcome: string | undefined;
    await act(async () => {
      settle(session({ token: 'late' }));
      outcome = await renewal;
    });

    expect(outcome).toBe('unavailable');
    expect(result.current.token).toBeNull();
    expect(await stored()).toBeNull();
  });
});

describe('AuthProvider profile', () => {
  it('refresh re-fetches the profile and persists it alongside the token', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    mocked.me.mockResolvedValue({ ...user, display_name: 'Renamed' });
    const { result } = await renderAuth();

    await act(async () => {
      await result.current.refresh();
    });

    expect(result.current.user?.display_name).toBe('Renamed');
    expect(await stored()).toMatchObject({ token: 'access', user: { display_name: 'Renamed' } });
  });

  it('refresh is a no-op while signed out', async () => {
    const { result } = await renderAuth();
    await act(async () => {
      await result.current.refresh();
    });
    expect(mocked.me).not.toHaveBeenCalled();
  });

  it('updateDatingProfile persists the returned profile', async () => {
    await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(session()));
    const input = { dating_styles: ['hinge' as const], phases_strong: [], phases_working_on: [], dating_preferences: 'x' };
    mocked.updateDatingProfile.mockResolvedValue({ ...user, dating_styles: ['hinge'] });
    const { result } = await renderAuth();

    await act(async () => {
      await result.current.updateDatingProfile(input);
    });

    expect(mocked.updateDatingProfile).toHaveBeenCalledWith(input);
    expect(result.current.user?.dating_styles).toEqual(['hinge']);
    expect(await stored()).toMatchObject({ user: { dating_styles: ['hinge'] } });
  });
});

describe('useAuth', () => {
  it('throws outside a provider', () => {
    const spy = jest.spyOn(console, 'error').mockImplementation(() => undefined);
    expect(() => renderHook(() => useAuth())).toThrow('useAuth must be used inside AuthProvider');
    spy.mockRestore();
  });
});
