import { test, expect, type BrowserContext, type Page } from '@playwright/test';
import { publishCoach } from '../helpers/api';
import { makeAccount, signUpAndVerify } from '../helpers/signUpAndVerify';
import { dbCount, dbOne, sleep, startService, stopService, waitForApi, waitForDb } from '../helpers/stack';
import { button, field, openTab } from '../helpers/ui';

/**
 * Recipes that take services down mid-flow: the chat offline queue replaying
 * exactly once, and the analysis result screen's "Try again" after the worker
 * and API were unavailable. Services are always restarted in afterEach so a
 * failure cannot leave the stack broken for the next spec.
 */
test.describe('service outages', () => {
  test.describe.configure({ mode: 'serial' });

  let clientCtx: BrowserContext;
  let coachCtx: BrowserContext;
  let clientPage: Page;
  let clientID: string;
  const client = makeAccount('outage-client', 'user');
  const coach = makeAccount('outage-coach', 'coach');

  test.beforeAll(async ({ browser }) => {
    coachCtx = await browser.newContext();
    const signedInCoach = await signUpAndVerify(coachCtx, coach);
    await publishCoach(signedInCoach.token);
    clientCtx = await browser.newContext();
    ({ page: clientPage, userID: clientID } = await signUpAndVerify(clientCtx, client));
  });

  test.afterEach(async () => {
    await startService('worker');
    await startService('api');
    await waitForApi();
  });

  test.afterAll(async () => {
    await clientCtx.close();
    await coachCtx.close();
  });

  test('a chat message sent while the API is down replays exactly once after it returns', async () => {
    await openTab(clientPage, 'Coaches');
    await clientPage.getByText(coach.displayName).filter({ visible: true }).first().click();
    await button(clientPage, 'Start a live chat').click();
    const status = clientPage.getByText(/^(Connected|Connecting…|Reconnecting…)$/);
    await expect(status).toHaveText('Connected', { timeout: 30_000 });

    await stopService('api');
    await expect(status).toHaveText('Reconnecting…', { timeout: 60_000 });

    const body = `queued while offline ${Date.now()}`;
    await clientPage.getByPlaceholder('Message your coach').fill(body);
    await clientPage.getByRole('button', { name: 'send' }).click();

    await startService('api');
    await waitForApi();
    await expect(status).toHaveText('Connected', { timeout: 60_000 });

    await waitForDb(`select count(*) from chat_messages where body = '${body}'`, (v) => v === '1', { timeoutMs: 30_000 });
    await expect(clientPage.getByText(body)).toBeVisible();
    // Stays at one: the queued send is not replayed a second time by the reconnect.
    await sleep(5_000);
    expect(await dbCount(`select count(*) from chat_messages where body = '${body}'`)).toBe(1);
    expect(await dbOne(`select sender_id from chat_messages where body = '${body}'`)).toBe(clientID);
  });

  test('analysis result screen recovers via "Try again" after worker + API outage', async () => {
    test.setTimeout(5 * 60_000);
    // No worker: the conversation stays queued so the result screen keeps polling.
    await stopService('worker');

    await openTab(clientPage, 'Analyse');
    await expect(clientPage.getByText('Analyse a conversation')).toBeVisible();
    const title = `Outage ${Date.now()}`;
    await field(clientPage, 'Title').fill(title);
    await button(clientPage, 'Use the example').click();
    await button(clientPage, 'Get feedback').click();
    await expect(clientPage.getByText('Queued for analysis…')).toBeVisible({ timeout: 30_000 });

    const conversationID = await dbOne(`select id from conversations where user_id = '${clientID}' and title = '${title}'`);
    expect(await dbOne(`select status from analysis_results where conversation_id = '${conversationID}'`)).toBe('pending');

    // Now the polls fail too; after MAX_POLL_FAILURES the screen offers "Try again".
    await stopService('api');
    const retry = button(clientPage, 'Try again');
    await expect(retry).toBeVisible({ timeout: 120_000 });

    await startService('api');
    await startService('worker');
    await waitForApi();
    await retry.click();

    await expect(clientPage.getByText('Overall engagement')).toBeVisible({ timeout: 90_000 });
    await expect(clientPage.getByText('%', { exact: true }).filter({ visible: true })).toBeVisible();
    const status = await waitForDb(
      `select status from analysis_results where conversation_id = '${conversationID}' order by created_at desc limit 1`,
      (v) => v === 'succeeded',
    );
    expect(status).toBe('succeeded');
  });
});
