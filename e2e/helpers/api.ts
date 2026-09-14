import type { BrowserContext, Page } from '@playwright/test';
import { BASE_URL, dbOne } from './stack';

/** localStorage key the web app persists its session under. */
export const SESSION_KEY = 'dating-coach.session';

/** Shape of the persisted session blob (mirrors AuthSession in app/src/api/types.ts). */
export interface StoredSession {
  token: string;
  refresh_token?: string;
  expires_at: string;
  user: { id: string; email: string; role: string; display_name: string };
}

/** Result of a raw API call: HTTP status plus the parsed JSON body (or null when the body was not JSON). */
export interface ApiResponse<T = Record<string, unknown>> {
  status: number;
  body: T | null;
}

/**
 * Calls the real API through nginx (`/api/v1<path>`) from the test process,
 * with an optional bearer token, and returns status + parsed body without
 * throwing on 4xx/5xx. Used for the steps the UI has no control for (coach
 * confirmation, reschedule) and for asserting raw status codes (202, 401, 409).
 */
export async function api<T = Record<string, unknown>>(
  method: string,
  path: string,
  { token, body }: { token?: string; body?: unknown } = {},
): Promise<ApiResponse<T>> {
  const res = await fetch(`${BASE_URL}/api/v1${path}`, {
    method,
    headers: {
      Accept: 'application/json',
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let parsed: T | null = null;
  try {
    parsed = text ? (JSON.parse(text) as T) : null;
  } catch {
    parsed = null;
  }
  return { status: res.status, body: parsed };
}

/**
 * Reads the session the web app persisted in `page`'s localStorage. Throws
 * when nothing is stored, i.e. the page is signed out.
 */
export async function storedSession(page: Page): Promise<StoredSession> {
  const raw = await page.evaluate((key) => window.localStorage.getItem(key), SESSION_KEY);
  if (!raw) throw new Error('no session in localStorage');
  return JSON.parse(raw) as StoredSession;
}

/** Access token of the account signed in on `page`. */
export async function tokenOf(page: Page): Promise<string> {
  return (await storedSession(page)).token;
}

/**
 * Overwrites the persisted session blob with `raw` (a string so tests can
 * store corrupt JSON) via a fresh page in `context`; a reload afterwards
 * makes the app restore from it.
 */
export async function writeStoredSession(context: BrowserContext, raw: string): Promise<void> {
  const page = await context.newPage();
  await page.goto('/');
  await page.evaluate(([key, value]) => window.localStorage.setItem(key as string, value as string), [SESSION_KEY, raw]);
  await page.close();
}

/** Coach-side confirmation of a pending booking: `POST /coach/sessions/{id}/respond {action:"confirm"}`. */
export function confirmBooking(coachToken: string, sessionID: string) {
  return api<{ status: string }>('POST', `/coach/sessions/${sessionID}/respond`, {
    token: coachToken,
    body: { action: 'confirm' },
  });
}

/** Open slots for `coachID` as seen by the caller, optionally excluding the session being rescheduled. */
export async function openSlots(
  token: string,
  coachID: string,
  { durationMinutes = 45, excludeSessionID }: { durationMinutes?: number; excludeSessionID?: string } = {},
): Promise<{ start: string; duration_minutes: number }[]> {
  const exclude = excludeSessionID ? `&exclude_session_id=${excludeSessionID}` : '';
  const res = await api<{ slots: { start: string; duration_minutes: number }[] | null }>(
    'GET',
    `/coaching/coaches/${coachID}/slots?duration_minutes=${durationMinutes}${exclude}`,
    { token },
  );
  if (res.status !== 200) throw new Error(`slots returned ${res.status}: ${JSON.stringify(res.body)}`);
  return res.body?.slots ?? [];
}

/** Availability windows covering every weekday around the clock, so a coach always has open slots. */
export const ALL_WEEK: { weekday: number; start_minute: number; end_minute: number }[] = Array.from(
  { length: 7 },
  (_, weekday) => ({ weekday, start_minute: 0, end_minute: 1440 }),
);

/**
 * Records an admin decision on a coach profile straight in Postgres, the
 * documented fallback when no admin account is signed in. Resolves with the
 * stored status; throws when the user has no coach profile row yet.
 */
export async function setCoachApproval(coachID: string, status: 'pending' | 'approved' | 'rejected'): Promise<string> {
  return dbOne(`update coaches set approval_status = '${status}', updated_at = now() where user_id = '${coachID}' returning approval_status`);
}

/**
 * Publishes a coach profile and availability straight through the API, for
 * specs whose subject is not the Profile screen, and (unless `approve` is
 * false) marks the profile approved so clients can see and book it at once.
 */
export async function publishCoach(
  coachToken: string,
  {
    rateCents = 12000,
    windows = ALL_WEEK,
    approve = true,
  }: { rateCents?: number; windows?: typeof ALL_WEEK; approve?: boolean } = {},
): Promise<void> {
  const profile = await api('PUT', '/coach/profile', {
    token: coachToken,
    body: {
      headline: 'E2E coach',
      bio: 'Fixture coach for the end-to-end suite.',
      specialties: ['openers', 'texting'],
      hourly_rate_cents: rateCents,
      timezone: 'UTC',
      years_experience: 3,
      accepting_clients: true,
    },
  });
  if (profile.status !== 200) throw new Error(`profile upsert returned ${profile.status}: ${JSON.stringify(profile.body)}`);
  const availability = await api('PUT', '/coach/availability', { token: coachToken, body: { availability: windows } });
  if (availability.status !== 200) {
    throw new Error(`availability returned ${availability.status}: ${JSON.stringify(availability.body)}`);
  }
  if (approve) {
    const me = await api<{ id: string }>('GET', '/auth/me', { token: coachToken });
    if (me.status !== 200 || !me.body) throw new Error(`/auth/me returned ${me.status}`);
    await setCoachApproval(me.body.id, 'approved');
  }
}
