import { test, expect, type BrowserContext, type Page } from '@playwright/test';
import { confirmBooking, openSlots } from '../helpers/api';
import { makeAccount, signUpAndVerify } from '../helpers/signUpAndVerify';
import { db, dbCount, dbOne, waitForDb } from '../helpers/stack';
import { button, field, openTab, pickSlot, removeAllWindows } from '../helpers/ui';

/**
 * The primary flow, end to end, against the real stack: two accounts sign up
 * in separate browser contexts, the coach publishes a profile from the Profile
 * tab, the client books through the Coaches tab, the coach confirms, the two
 * chat live over the WebSocket, and the client gets an analysis back from the
 * worker + ml-analyzer. Every step is asserted in Postgres as well as in the UI.
 */
test.describe.serial('happy path', () => {
  const coach = makeAccount('coach', 'coach');
  const client = makeAccount('client', 'user');
  let coachCtx: BrowserContext;
  let clientCtx: BrowserContext;
  let coachPage: Page;
  let clientPage: Page;
  let coachID: string;
  let clientID: string;
  let coachToken: string;
  let clientToken: string;
  let sessionID: string;

  test.beforeAll(async ({ browser }) => {
    coachCtx = await browser.newContext();
    clientCtx = await browser.newContext();
  });

  test.afterAll(async () => {
    await coachCtx?.close();
    await clientCtx?.close();
  });

  test('1. coach and client sign up and verify in separate contexts', async () => {
    const c = await signUpAndVerify(coachCtx, coach);
    coachPage = c.page;
    coachID = c.userID;
    coachToken = c.token;
    const u = await signUpAndVerify(clientCtx, client);
    clientPage = u.page;
    clientID = u.userID;
    clientToken = u.token;

    expect(await dbOne(`select email_verified || '|' || role from users where id = '${coachID}'`)).toBe('true|coach');
    expect(await dbOne(`select email_verified || '|' || role from users where id = '${clientID}'`)).toBe('true|user');
    expect(coachID).not.toBe(clientID);
  });

  test('2. coach publishes profile + availability; client can now see them', async () => {
    // Before the profile exists the client's Coaches list must not show them.
    await openTab(clientPage, 'Coaches');
    await expect(clientPage.getByText(coach.displayName)).toHaveCount(0);
    expect(await dbCount(`select count(*) from coaches where user_id = '${coachID}'`)).toBe(0);

    await openTab(coachPage, 'Profile');
    await expect(coachPage.getByText('How clients see you')).toBeVisible();
    await field(coachPage, 'Headline').fill('Opening messages that land');
    await field(coachPage, 'Bio').fill('Fixture coach created by the e2e suite.');
    await field(coachPage, 'Specialties (comma separated)').fill('openers, texting');
    await field(coachPage, 'Hourly rate (USD)').fill('120');
    await field(coachPage, 'Years experience').fill('4');
    await field(coachPage, 'Timezone (IANA)').fill('UTC');

    // Replace the default weekday-evening windows with two windows, today and
    // tomorrow (UTC), covering the whole day so slots exist whenever the suite runs.
    await removeAllWindows(coachPage);
    await expect(coachPage.getByText('No availability published. Add a window so clients can book you.')).toBeVisible();
    const today = new Date().getUTCDay();
    const tomorrow = (today + 1) % 7;
    const WEEKDAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
    for (const [i, weekday] of [today, tomorrow].entries()) {
      await button(coachPage, 'Add window').click();
      await coachPage.getByRole('radio', { name: WEEKDAYS[weekday] ?? '' }).nth(i).click();
      await field(coachPage, 'From').nth(i).fill('00:00');
      await field(coachPage, 'To').nth(i).fill('24:00');
    }
    await button(coachPage, 'Save profile').click();
    await expect(coachPage.getByText('Profile and availability saved.')).toBeVisible();

    expect(
      await dbOne(`select hourly_rate_cents || '|' || timezone || '|' || accepting_clients from coaches where user_id = '${coachID}'`),
    ).toBe('12000|UTC|true');
    const windows = await db(
      `select weekday || ':' || start_minute || '-' || end_minute from coach_availability where coach_id = '${coachID}' order by weekday`,
    );
    expect(windows.split('\n').sort()).toEqual([`${today}:0-1440`, `${tomorrow}:0-1440`].sort());

    // Coaches list is fetched on focus: switch away and back.
    await openTab(clientPage, 'Account');
    await openTab(clientPage, 'Coaches');
    await expect(clientPage.getByText(coach.displayName)).toBeVisible({ timeout: 20_000 });
  });

  test('3. client books a slot from the coach detail screen', async () => {
    await clientPage.getByText(coach.displayName).click();
    await expect(clientPage.getByText('Book a session', { exact: true })).toBeVisible();
    const before = await openSlots(clientToken, coachID);
    expect(before.length).toBeGreaterThan(2);

    const chosen = before[1]!;
    await pickSlot(clientPage, 1);
    await field(clientPage, 'What do you want to work on?').fill('Opening messages on Hinge');
    await button(clientPage, 'Book session').click();
    await expect(clientPage.getByText('Session booked — see it under Sessions.')).toBeVisible();

    const row = await dbOne(
      `select id || '|' || status || '|' || duration_minutes || '|' || topic from coaching_sessions where user_id = '${clientID}' and coach_id = '${coachID}'`,
    );
    const [id, status, duration, topic] = row.split('|');
    sessionID = id!;
    expect(status).toBe('pending');
    expect(duration).toBe('45');
    expect(topic).toBe('Opening messages on Hinge');
    expect(await dbOne(`select respond_by is not null from coaching_sessions where id = '${sessionID}'`)).toBe('t');

    // The slot is held: it no longer appears in the coach's open slots.
    const after = await openSlots(clientToken, coachID);
    expect(after.map((s) => s.start)).not.toContain(chosen.start);
    // A 60-minute booking on the 30-minute slot grid holds every overlapping start.
    expect(after.length).toBeLessThan(before.length);

    await openTab(clientPage, 'Sessions');
    await expect(clientPage.getByText(coach.displayName).filter({ visible: true }).first()).toBeVisible();
  });

  test('4. coach confirms the booking → scheduled', async () => {
    const res = await confirmBooking(coachToken, sessionID);
    expect(res.status).toBe(200);
    expect(res.body?.status).toBe('scheduled');
    expect(await dbOne(`select status || '|' || (confirmed_at is not null) from coaching_sessions where id = '${sessionID}'`)).toBe(
      'scheduled|true',
    );

    await openTab(coachPage, 'Dashboard');
    await expect(coachPage.getByText('Upcoming sessions')).toBeVisible();
    await expect(coachPage.getByText(client.displayName).first()).toBeVisible({ timeout: 20_000 });
  });

  test('5. live chat between client and coach over the WebSocket', async () => {
    // The Coaches tab keeps its stack, so the detail screen from step 3 is still on top.
    await openTab(clientPage, 'Coaches');
    const startChat = button(clientPage, 'Start a live chat');
    if ((await startChat.count()) === 0) {
      await clientPage.getByText(coach.displayName).filter({ visible: true }).first().click();
    }
    await startChat.click();
    await expect(clientPage.getByText('Connected', { exact: true })).toBeVisible({ timeout: 30_000 });

    const threadID = await dbOne(
      `select id from chat_threads where user_id = '${clientID}' and coach_id = '${coachID}' and status = 'active'`,
    );

    await openTab(coachPage, 'Chats');
    await openTab(coachPage, 'Dashboard');
    await expect(coachPage.getByText('Active chats').first()).toBeVisible();
    await button(coachPage, 'Open chat').first().click();
    await expect(coachPage.getByText('Connected', { exact: true })).toBeVisible({ timeout: 30_000 });

    const fromClient = `hello coach ${Date.now()}`;
    await clientPage.getByPlaceholder('Message your coach').fill(fromClient);
    await clientPage.getByRole('button', { name: 'send' }).click();
    await expect(coachPage.getByText(fromClient)).toBeVisible({ timeout: 20_000 });

    const fromCoach = `hello client ${Date.now()}`;
    await coachPage.getByPlaceholder('Reply to your client').fill(fromCoach);
    await coachPage.getByRole('button', { name: 'send' }).click();
    await expect(clientPage.getByText(fromCoach)).toBeVisible({ timeout: 20_000 });

    expect(await dbCount(`select count(*) from chat_messages where thread_id = '${threadID}'`)).toBe(2);
    expect(await dbOne(`select sender_id from chat_messages where body = '${fromClient}'`)).toBe(clientID);
    expect(await dbOne(`select sender_id from chat_messages where body = '${fromCoach}'`)).toBe(coachID);
  });

  test('6. client submits a conversation and gets a heuristic analysis', async () => {
    await openTab(clientPage, 'Analyse');
    await expect(clientPage.getByText('Analyse a conversation')).toBeVisible();
    const title = `Sourdough match ${Date.now()}`;
    await field(clientPage, 'Title').fill(title);
    await button(clientPage, 'Use the example').click();
    await button(clientPage, 'Get feedback').click();

    await expect(clientPage.getByText('Overall engagement')).toBeVisible({ timeout: 90_000 });
    // The ring shows Math.round(score * 100) next to a "%" unit.
    await expect(clientPage.getByText(/^\d{1,3}$/).filter({ visible: true }).first()).toBeVisible();
    await expect(clientPage.getByText('%', { exact: true }).filter({ visible: true })).toBeVisible();

    const conversationID = await dbOne(`select id from conversations where user_id = '${clientID}' and title = '${title}'`);
    const result = await waitForDb(
      `select status || '|' || model_version from analysis_results where conversation_id = '${conversationID}' order by created_at desc limit 1`,
      (v) => v.startsWith('succeeded|'),
      { timeoutMs: 60_000 },
    );
    expect(result).toBe('succeeded|heuristic-v2');
    expect(await dbOne(`select (overall->>'engagement_score')::numeric between 0 and 1 from analysis_results where conversation_id = '${conversationID}'`)).toBe('t');
  });
});
