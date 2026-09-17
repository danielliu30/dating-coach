import { test, expect, type BrowserContext, type Page } from '@playwright/test';
import { api, openSlots, publishCoach } from '../helpers/api';
import { makeAccount, signUpAndVerify } from '../helpers/signUpAndVerify';
import { db, dbOne, waitForDb } from '../helpers/stack';
import { button, field, openTab, pickSlot, removeAllWindows } from '../helpers/ui';

/**
 * Booking edge cases from SKILL.md: the 409 race, booking outside availability,
 * rescheduling inside the session's own interval, and confirmation expiry by
 * the worker sweep. Each test gets its own coach so slot state never leaks
 * between them; the client is shared.
 */
test.describe('booking edge cases', () => {
  test.describe.configure({ mode: 'serial' });

  let clientCtx: BrowserContext;
  let clientPage: Page;
  let clientToken: string;
  const client = makeAccount('book-client', 'user');

  test.beforeAll(async ({ browser }) => {
    clientCtx = await browser.newContext();
    ({ page: clientPage, token: clientToken } = await signUpAndVerify(clientCtx, client));
  });

  test.afterAll(async () => {
    await clientCtx.close();
  });

  /** Creates a verified coach with all-week availability and returns its id, token and account. */
  async function freshCoach(ctx: BrowserContext, prefix: string) {
    const account = makeAccount(prefix, 'coach');
    const { page, userID, token } = await signUpAndVerify(ctx, account);
    await publishCoach(token);
    return { account, page, coachID: userID, token };
  }

  /** Reloads `page` (resetting the Coaches stack) and opens the detail screen for coach `name`. */
  async function openCoach(page: Page, name: string) {
    await page.goto('/');
    await openTab(page, 'Coaches');
    await page.getByText(name).filter({ visible: true }).first().click();
    await expect(page.getByText('Open slots · next 7 days')).toBeVisible();
  }

  test('two tabs booking the same slot: the second gets "slot is no longer available"', async ({ browser }) => {
    const coachCtx = await browser.newContext();
    const { account, coachID } = await freshCoach(coachCtx, 'race-coach');
    await coachCtx.close();

    const tabA = clientPage;
    const tabB = await clientCtx.newPage();
    await tabB.goto('/');
    await openCoach(tabA, account.displayName);
    await openCoach(tabB, account.displayName);

    // Both tabs pick the same chip from identical (stale for the loser) slot lists.
    const slotA = await pickSlot(tabA, 2);
    const slotB = await pickSlot(tabB, 2);
    expect(slotB).toBe(slotA);
    await field(tabA, 'What do you want to work on?').fill('race A');
    await field(tabB, 'What do you want to work on?').fill('race B');

    await Promise.all([button(tabA, 'Book session').click(), button(tabB, 'Book session').click()]);

    const outcomes = await Promise.all(
      [tabA, tabB].map(async (tab) => {
        const ok = tab.getByText('Session booked — see it under Sessions.');
        const conflict = tab.getByText('slot is no longer available');
        await expect(ok.or(conflict)).toBeVisible({ timeout: 20_000 });
        return (await ok.count()) > 0 ? 'booked' : 'conflict';
      }),
    );
    expect(outcomes.sort()).toEqual(['booked', 'conflict']);

    expect(await dbOne(`select count(*) from coaching_sessions where coach_id = '${coachID}'`)).toBe('1');
    expect(await dbOne(`select status from coaching_sessions where coach_id = '${coachID}'`)).toBe('pending');
    await tabB.close();
  });

  test('booking a stale slot after the coach removed the window fails with "coach is not available then"', async ({
    browser,
  }) => {
    const coachCtx = await browser.newContext();
    const { account, page: coachPage, coachID } = await freshCoach(coachCtx, 'window-coach');

    // Client loads the detail screen while the coach is still available.
    await openCoach(clientPage, account.displayName);
    await pickSlot(clientPage, 1);
    await field(clientPage, 'What do you want to work on?').fill('stale slot');

    // Coach removes every window from the Profile tab.
    await openTab(coachPage, 'Profile');
    await expect(coachPage.getByText('How clients see you')).toBeVisible();
    await removeAllWindows(coachPage);
    await button(coachPage, 'Save profile').click();
    await expect(coachPage.getByText('Profile and availability saved.')).toBeVisible();
    expect(await db(`select count(*) from coach_availability where coach_id = '${coachID}'`)).toBe('0');

    await button(clientPage, 'Book session').click();
    await expect(clientPage.getByText('coach is not available then')).toBeVisible();
    expect(await db(`select count(*) from coaching_sessions where coach_id = '${coachID}'`)).toBe('0');
    await coachCtx.close();
  });

  test('rescheduling into the session\'s own interval is accepted (exclude_session_id)', async ({ browser }) => {
    const coachCtx = await browser.newContext();
    const { coachID, token: coachToken } = await freshCoach(coachCtx, 'resched-coach');
    await coachCtx.close();

    // Book 60 minutes through the API and confirm it, so the session occupies
    // [start, start+60). The shifted start below must itself fit a 60-minute
    // slot, so skip a start whose +30 sits on the daily window's midnight edge.
    const slots = await openSlots(clientToken, coachID, { durationMinutes: 60 });
    const offered = new Set(slots.map((s) => Date.parse(s.start)));
    const start = slots.slice(3).find((s) => offered.has(Date.parse(s.start) + 30 * 60_000))?.start;
    if (!start) throw new Error('no open slot followed by a bookable half-hour shift');
    const booked = await api<{ id: string }>('POST', '/coaching/sessions', {
      token: clientToken,
      body: { coach_id: coachID, scheduled_time: start, duration_minutes: 60, topic: 'reschedule me' },
    });
    expect(booked.status).toBe(201);
    const sessionID = booked.body?.id ?? '';
    const confirmed = await api('POST', `/coach/sessions/${sessionID}/respond`, {
      token: coachToken,
      body: { action: 'confirm' },
    });
    expect(confirmed.status).toBe(200);

    // Half an hour later still overlaps the session itself; without the
    // exclusion it would be reported as taken.
    const shiftedMs = Date.parse(start) + 30 * 60_000;
    const shifted = new Date(shiftedMs).toISOString();
    const starts = (slots: { start: string }[]) => slots.map((s) => Date.parse(s.start));
    expect(starts(await openSlots(clientToken, coachID, { durationMinutes: 60 }))).not.toContain(shiftedMs);
    expect(
      starts(await openSlots(clientToken, coachID, { durationMinutes: 60, excludeSessionID: sessionID })),
    ).toContain(shiftedMs);

    const res = await api<{ scheduled_time: string }>('POST', `/coaching/sessions/${sessionID}/reschedule`, {
      token: clientToken,
      body: { scheduled_time: shifted },
    });
    expect(res.status).toBe(200);
    expect(Date.parse(res.body?.scheduled_time ?? '')).toBe(shiftedMs);
    expect(
      await dbOne(`select extract(epoch from scheduled_time)::bigint from coaching_sessions where id = '${sessionID}'`),
    ).toBe(String(shiftedMs / 1000));
  });

  test('an unanswered booking expires on the worker sweep and frees its slot', async ({ browser }) => {
    const coachCtx = await browser.newContext();
    const { account, coachID } = await freshCoach(coachCtx, 'expiry-coach');
    await coachCtx.close();

    await openCoach(clientPage, account.displayName);
    const before = await openSlots(clientToken, coachID);
    await pickSlot(clientPage, 2);
    await field(clientPage, 'What do you want to work on?').fill('expire me');
    await button(clientPage, 'Book session').click();
    await expect(clientPage.getByText('Session booked — see it under Sessions.')).toBeVisible();

    const sessionID = await dbOne(`select id from coaching_sessions where coach_id = '${coachID}'`);
    expect(await dbOne(`select status from coaching_sessions where id = '${sessionID}'`)).toBe('pending');
    const held = await openSlots(clientToken, coachID);
    expect(held.length).toBeLessThan(before.length);

    // The worker sweeps pending sessions whose respond_by has passed once a minute.
    await db(`update coaching_sessions set respond_by = now() - interval '1 minute' where id = '${sessionID}'`);
    const status = await waitForDb(
      `select status from coaching_sessions where id = '${sessionID}'`,
      (v) => v === 'expired',
      { timeoutMs: 90_000 },
    );
    expect(status).toBe('expired');

    const freed = await openSlots(clientToken, coachID);
    expect(freed.length).toBe(before.length);
  });
});
