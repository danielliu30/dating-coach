import { Platform } from 'react-native';

type Identity = typeof import('./googleIdentity');

/** Loads the module fresh with the given client ID and platform baked in. */
function load(clientID: string, os: typeof Platform.OS = 'web'): Identity {
  jest.resetModules();
  process.env.EXPO_PUBLIC_GOOGLE_CLIENT_ID = clientID;
  Platform.OS = os;
  return require('./googleIdentity') as Identity;
}

const originalOS = Platform.OS;

afterEach(() => {
  Platform.OS = originalOS;
  delete process.env.EXPO_PUBLIC_GOOGLE_CLIENT_ID;
  delete window.google;
});

describe('googleSignInAvailable', () => {
  it('is false without a client ID or off the web', () => {
    expect(load('').googleSignInAvailable).toBe(false);
    expect(load('cid', 'ios').googleSignInAvailable).toBe(false);
    expect(load('cid').googleSignInAvailable).toBe(true);
  });
});

describe('requestGoogleIdToken', () => {
  it('refuses before touching Google when not available', async () => {
    await expect(load('').requestGoogleIdToken()).rejects.toThrow(/not configured/);
    await expect(load('cid', 'android').requestGoogleIdToken()).rejects.toThrow(/only available on the web/);
  });

  it('initialises with the configured client ID and resolves with the credential', async () => {
    const { requestGoogleIdToken } = load('cid');
    const initialize = jest.fn();
    const prompt = jest.fn();
    window.google = { accounts: { id: { initialize, prompt } } };

    const pending = requestGoogleIdToken();
    await Promise.resolve();
    expect(initialize).toHaveBeenCalledWith(expect.objectContaining({ client_id: 'cid' }));
    initialize.mock.calls[0][0].callback({ credential: 'jwt-from-google' });
    await expect(pending).resolves.toBe('jwt-from-google');
  });

  it('rejects when the browser will not show the prompt', async () => {
    const { requestGoogleIdToken } = load('cid');
    window.google = {
      accounts: {
        id: {
          initialize: jest.fn(),
          prompt: (listener) =>
            listener?.({
              isNotDisplayed: () => true,
              isSkippedMoment: () => false,
              isDismissedMoment: () => false,
              getNotDisplayedReason: () => 'opt_out_or_no_session',
              getSkippedReason: () => '',
              getDismissedReason: () => '',
            }),
        },
      },
    };
    await expect(requestGoogleIdToken()).rejects.toThrow(/opt_out_or_no_session/);
  });
});
