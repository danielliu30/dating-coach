import { Platform } from 'react-native';

import { GOOGLE_CLIENT_ID } from '../config';

/** Where the Google Identity Services library is loaded from on the web. */
const GIS_SCRIPT_URL = 'https://accounts.google.com/gsi/client';

/** The slice of Google Identity Services the app uses (the library ships no types). */
interface GoogleIdentityServices {
  accounts: {
    id: {
      initialize(config: {
        client_id: string;
        callback: (response: { credential?: string }) => void;
        cancel_on_tap_outside?: boolean;
        use_fedcm_for_prompt?: boolean;
      }): void;
      prompt(listener?: (notification: PromptMomentNotification) => void): void;
    };
  };
}

interface PromptMomentNotification {
  isNotDisplayed(): boolean;
  isSkippedMoment(): boolean;
  isDismissedMoment(): boolean;
  getNotDisplayedReason(): string;
  getSkippedReason(): string;
  getDismissedReason(): string;
}

declare global {
  interface Window {
    google?: GoogleIdentityServices;
  }
}

/**
 * Whether "Continue with Google" can work in this build: a client ID is
 * configured (EXPO_PUBLIC_GOOGLE_CLIENT_ID) and the platform has an
 * implementation. Only the web bundle is covered today; native builds hide the
 * button rather than show one that cannot finish.
 */
export const googleSignInAvailable: boolean = GOOGLE_CLIENT_ID !== '' && Platform.OS === 'web';

let scriptLoad: Promise<GoogleIdentityServices> | null = null;

/**
 * Loads Google Identity Services once and resolves with its global. Rejects
 * when the script cannot be fetched (offline, blocked by an extension), and
 * forgets the attempt so a later call retries instead of failing forever.
 */
function loadGoogleIdentityServices(): Promise<GoogleIdentityServices> {
  if (window.google) return Promise.resolve(window.google);
  if (scriptLoad) return scriptLoad;
  scriptLoad = new Promise<GoogleIdentityServices>((resolve, reject) => {
    const script = document.createElement('script');
    script.src = GIS_SCRIPT_URL;
    script.async = true;
    script.onload = () => {
      if (window.google) {
        resolve(window.google);
      } else {
        scriptLoad = null;
        reject(new Error('Google sign-in did not load'));
      }
    };
    script.onerror = () => {
      scriptLoad = null;
      reject(new Error('could not load Google sign-in; check your connection or ad blocker'));
    };
    document.head.appendChild(script);
  });
  return scriptLoad;
}

/**
 * Asks Google for an ID token for the signed-in Google account, using the
 * Identity Services prompt. Resolves with the raw JWT the backend verifies at
 * POST /auth/google; nothing about the account is decoded client-side.
 *
 * Rejects when Google sign-in is not available on this build
 * (`googleSignInAvailable` is false), when the library cannot load, when the
 * browser suppresses the prompt (third-party cookies or FedCM disabled, or a
 * recent dismissal put it in a cool-down), or when the user closes it.
 */
export async function requestGoogleIdToken(): Promise<string> {
  if (!googleSignInAvailable) {
    throw new Error(
      GOOGLE_CLIENT_ID === ''
        ? 'Google sign-in is not configured for this app'
        : 'Google sign-in is only available on the web for now',
    );
  }
  const google = await loadGoogleIdentityServices();
  return new Promise<string>((resolve, reject) => {
    let settled = false;
    const settle = (fn: () => void) => {
      if (settled) return;
      settled = true;
      fn();
    };
    google.accounts.id.initialize({
      client_id: GOOGLE_CLIENT_ID,
      cancel_on_tap_outside: false,
      use_fedcm_for_prompt: true,
      callback: ({ credential }) =>
        settle(() => (credential ? resolve(credential) : reject(new Error('Google did not return a sign-in token')))),
    });
    google.accounts.id.prompt((moment) => {
      if (moment.isNotDisplayed()) {
        settle(() =>
          reject(
            new Error(
              `Google sign-in could not open (${moment.getNotDisplayedReason()}); allow third-party sign-in in your browser and try again`,
            ),
          ),
        );
      } else if (moment.isSkippedMoment()) {
        settle(() => reject(new Error(`Google sign-in was skipped (${moment.getSkippedReason()})`)));
      } else if (moment.isDismissedMoment() && moment.getDismissedReason() !== 'credential_returned') {
        settle(() => reject(new Error('Google sign-in was closed before finishing')));
      }
    });
  });
}
