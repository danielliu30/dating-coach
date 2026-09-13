import { test, expect, type BrowserContext } from '@playwright/test';
import { api, openSlots, publishCoach } from '../helpers/api';
import { makeAccount, signUpAndVerify, type Account } from '../helpers/signUpAndVerify';
import { db, dbOne } from '../helpers/stack';
import { openTab } from '../helpers/ui';

/** Bearer token from a password sign-in, so a role changed in Postgres reaches the JWT. */
async function passwordToken(account: Pick<Account, 'email' | 'password'>): Promise<string> {
  const res = await api<{ token: string }>('POST', '/auth/signin', {
    body: { email: account.email, password: account.password },
  });
  if (res.status !== 200 || !res.body) throw new Error(`signin returned ${res.status}: ${JSON.stringify(res.body)}`);
  return res.body.token;
}

/**
 * Tries to book the coach's first open slot as `clientToken`. Returns the HTTP
 * status and, on success, the new session's id so a later test can check it
 * still exists.
 */
async function tryBooking(clientToken: string, coachID: string): Promise<{ status: number; sessionID?: string }> {
  const start = (await openSlots(clientToken, coachID))[0]?.start;
  if (!start) throw new Error('no open slots');
  const res = await api<{ id: string }>('POST', '/coaching/sessions', {
    token: clientToken,
    body: { coach_id: coachID, scheduled_time: start, duration_minutes: 45 },
  });
  return { status: res.status, sessionID: res.body?.id };
}

/** The client's sessions with `coachID` as seen by GET /coaching/sessions. */
async function sessionsWith(clientToken: string, coachID: string): Promise<{ id: string; status: string }[]> {
  const res = await api<{ sessions: { id: string; coach_id: string; status: string }[] }>(
    'GET',
    '/coaching/sessions',
    { token: clientToken },
  );
  expect(res.status).toBe(200);
  return (res.body?.sessions ?? []).filter((s) => s.coach_id === coachID);
}

/**
 * The admin review queue end to end: a coach who has published a profile and
 * availability stays out of the directory and unbookable while pending, only an
 * admin can decide, approval makes the coach visible and bookable, and rejection
 * hides them again. Admin is granted the documented way (UPDATE users in
 * Postgres, then re-authenticate).
 */
test.describe('coach approval', () => {
  test.describe.configure({ mode: 'serial' });

  let clientCtx: BrowserContext;
  let coachCtx: BrowserContext;
  let clientToken: string;
  let coachToken: string;
  let coachID: string;
  let adminToken: string;
  let bookedSessionID: string;
  const client = makeAccount('appr-client', 'user');
  const coach = makeAccount('appr-coach', 'coach');
  const admin = makeAccount('appr-admin', 'user');

  test.beforeAll(async ({ browser }) => {
    clientCtx = await browser.newContext();
    coachCtx = await browser.newContext();
    ({ token: clientToken } = await signUpAndVerify(clientCtx, client));
    ({ token: coachToken, userID: coachID } = await signUpAndVerify(coachCtx, coach));
    await publishCoach(coachToken, { approve: false });

    const adminCtx = await browser.newContext();
    await signUpAndVerify(adminCtx, admin);
    await adminCtx.close();
    await db(`update users set role = 'admin' where email = '${admin.email}'`);
    adminToken = await passwordToken(admin);
  });

  test.afterAll(async () => {
    await clientCtx.close();
    await coachCtx.close();
  });

  test('a published coach starts pending: hidden from the directory and unbookable', async () => {
    expect(await dbOne(`select approval_status from coaches where user_id = '${coachID}'`)).toBe('pending');
    const clientPage = clientCtx.pages()[0]!;
    await openTab(clientPage, 'Coaches');
    await expect(clientPage.getByText(coach.displayName)).toHaveCount(0);
    expect((await tryBooking(clientToken, coachID)).status).toBe(409);
  });

  test('only an admin may use /admin: user and coach get 403, admin sees the pending coach', async () => {
    for (const token of [clientToken, coachToken]) {
      expect((await api('GET', '/admin/coaches?status=pending', { token })).status).toBe(403);
      expect((await api('POST', `/admin/coaches/${coachID}/approve`, { token })).status).toBe(403);
    }
    expect(await dbOne(`select approval_status from coaches where user_id = '${coachID}'`)).toBe('pending');

    const queue = await api<{ coaches: { id: string; email: string; display_name: string; approval_status: string }[] }>(
      'GET',
      '/admin/coaches?status=pending&limit=100',
      { token: adminToken },
    );
    expect(queue.status).toBe(200);
    const entry = queue.body?.coaches.find((c) => c.id === coachID);
    expect(entry).toMatchObject({ email: coach.email, display_name: coach.displayName, approval_status: 'pending' });
  });

  test('admin approval makes the coach visible and bookable', async () => {
    const approved = await api<{ approval_status: string }>('POST', `/admin/coaches/${coachID}/approve`, {
      token: adminToken,
    });
    expect(approved.status).toBe(200);
    expect(approved.body?.approval_status).toBe('approved');

    // The Coaches list is fetched on focus: switch away and back.
    const clientPage = clientCtx.pages()[0]!;
    await openTab(clientPage, 'Account');
    await openTab(clientPage, 'Coaches');
    await expect(clientPage.getByText(coach.displayName)).toBeVisible({ timeout: 20_000 });
    const booked = await tryBooking(clientToken, coachID);
    expect(booked.status).toBe(201);
    bookedSessionID = booked.sessionID!;
  });

  test('admin rejection hides the coach again and blocks new bookings', async () => {
    const rejected = await api<{ approval_status: string }>('POST', `/admin/coaches/${coachID}/reject`, {
      token: adminToken,
    });
    expect(rejected.status).toBe(200);
    expect(rejected.body?.approval_status).toBe('rejected');

    const clientPage = clientCtx.pages()[0]!;
    await openTab(clientPage, 'Account');
    await openTab(clientPage, 'Coaches');
    await expect(clientPage.getByText(coach.displayName)).toHaveCount(0);
    expect((await tryBooking(clientToken, coachID)).status).toBe(409);

    // Rejection only stops new business; the session booked while approved is untouched.
    const kept = (await sessionsWith(clientToken, coachID)).find((s) => s.id === bookedSessionID);
    expect(kept).toBeDefined();
    expect(kept!.status).not.toBe('cancelled');
  });

  test('unknown coach ids and bad statuses are rejected', async () => {
    expect(
      (await api('POST', '/admin/coaches/00000000-0000-0000-0000-000000000000/approve', { token: adminToken })).status,
    ).toBe(404);
    expect((await api('GET', '/admin/coaches?status=bogus', { token: adminToken })).status).toBe(400);
  });
});
