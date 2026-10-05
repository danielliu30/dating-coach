import { test, expect, type BrowserContext, type Page, type WebSocketRoute } from '@playwright/test';
import { publishCoach } from '../helpers/api';
import { makeAccount, signUpAndVerify } from '../helpers/signUpAndVerify';
import { dbCount, dbOne, sleep, startService, stopService, waitForApi, waitForDb } from '../helpers/stack';
import { button, field, openTab } from '../helpers/ui';

/**
 * Recipes that take services down mid-flow: the chat offline queue replaying
 * exactly once, the analysis result screen's "Try again" after the worker and
 * API were unavailable, and the result screen following the notifications
 * socket (push, and a refetch on reconnect for a push it missed). Services are always restarted in afterEach so a
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
    // The pill flips between "Reconnecting…" (socket closed) and "Connecting…"
    // (retry handshake hanging on a dead upstream); only "not Connected" is stable.
    await expect(status).toHaveText(/^(Connecting…|Reconnecting…)$/, { timeout: 60_000 });

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

  test.describe('analysis result via the notifications socket', () => {
    /** Frames the API sent on each notifications socket, in connection order. */
    let connections: { route: WebSocketRoute; frames: string[] }[];
    /** While set, frames from the API are recorded but not handed to the page. */
    let dropping: boolean;

    test.beforeEach(async () => {
      connections = [];
      dropping = false;
      await clientPage.routeWebSocket(/\/notifications\/ws/, (ws) => {
        const server = ws.connectToServer();
        const connection = { route: ws, frames: [] as string[] };
        connections.push(connection);
        server.onMessage((message) => {
          connection.frames.push(String(message));
          if (!dropping) ws.send(message);
        });
      });
      // The route only sees sockets opened after it is installed, and a fresh
      // load also resets the Analyse stack to its submit screen.
      await clientPage.goto('/');
    });

    test.afterEach(async () => {
      await clientPage.unrouteAll({ behavior: 'ignoreErrors' });
    });

    const connection = (i: number) => {
      const c = connections[i];
      if (!c) throw new Error(`notifications socket #${i + 1} was never opened`);
      return c;
    };
    const types = (frames: string[]) => frames.map((f) => (JSON.parse(f) as { type: string }).type);

    /** Submits the example transcript with the worker stopped and returns the queued analysis id. */
    const submitQueued = async (title: string): Promise<string> => {
      await stopService('worker');
      await openTab(clientPage, 'Analyse');
      await expect(clientPage.getByText('Analyse a conversation')).toBeVisible();
      await field(clientPage, 'Title').fill(title);
      await button(clientPage, 'Use the example').click();
      await button(clientPage, 'Get feedback').click();
      await expect(clientPage.getByText('Queued for analysis…')).toBeVisible({ timeout: 30_000 });
      await expect.poll(() => connections.some((c) => types(c.frames).includes('ready')), { timeout: 30_000 }).toBe(true);
      return dbOne(
        `select a.id from analysis_results a join conversations c on c.id = a.conversation_id
         where c.user_id = '${clientID}' and c.title = '${title}'`,
      );
    };

    /** Counts the page's GETs of one analysis result from now on. */
    const countResultFetches = (analysisID: string) => {
      const counter = { n: 0 };
      clientPage.on('request', (req) => {
        if (req.method() === 'GET' && req.url().endsWith(`/analysis/results/${analysisID}`)) counter.n += 1;
      });
      return counter;
    };

    test('result screen reaches "Overall engagement" from the analysis_ready push', async () => {
      const analysisID = await submitQueued(`Push ${Date.now()}`);
      const fetches = countResultFetches(analysisID);

      // The fallback poll is 30s, so finishing well inside it means the push did it.
      await startService('worker');
      await expect(clientPage.getByText('Overall engagement')).toBeVisible({ timeout: 20_000 });
      const pushed = connections.flatMap((c) => c.frames).map((f) => JSON.parse(f) as Record<string, string>);
      expect(pushed).toContainEqual(expect.objectContaining({ type: 'analysis_ready', analysis_id: analysisID }));
      expect(fetches.n).toBe(1);
      expect(connections).toHaveLength(1);
    });

    test('a socket reconnect refetches a result whose push was missed', async () => {
      const analysisID = await submitQueued(`Missed push ${Date.now()}`);

      // The page stops hearing the API, then the worker finishes the analysis.
      dropping = true;
      await startService('worker');
      await waitForDb(`select status from analysis_results where id = '${analysisID}'`, (v) => v === 'succeeded');
      await expect.poll(() => types(connection(0).frames), { timeout: 15_000 }).toContain('analysis_ready');
      await expect(clientPage.getByText('Queued for analysis…')).toBeVisible();

      // Dropping the socket makes the client reconnect; the new connection's
      // ready frame is its cue to refetch what it missed.
      const fetches = countResultFetches(analysisID);
      dropping = false;
      await connection(0).route.close({ code: 1012, reason: 'service restart' });
      await expect(clientPage.getByText('Overall engagement')).toBeVisible({ timeout: 15_000 });
      expect(connections.length).toBeGreaterThanOrEqual(2);
      expect(types(connection(1).frames)).toContain('ready');
      expect(fetches.n).toBeGreaterThanOrEqual(1);
    });
  });
});
