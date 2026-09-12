import { test, expect, type BrowserContext, type Page } from '@playwright/test';
import { api, SESSION_KEY } from '../helpers/api';
import { makeAccount, signUpAndVerify, type Account } from '../helpers/signUpAndVerify';
import { db, dbCount, dbOne, startService, stopService, waitForDb } from '../helpers/stack';
import { button, expectSignedOut, field, openTab, sessionEndedNotice } from '../helpers/ui';

/**
 * Account deletion end to end: the type-to-confirm flow on the Account screen,
 * `DELETE /auth/me` answering 202, the worker removing the rows, and the
 * broker-down variant where the outbox row waits for the relay. A control
 * account created alongside proves only the requested account is affected.
 */
test.describe('account deletion', () => {
  test.describe.configure({ mode: 'serial' });

  let controlCtx: BrowserContext;
  let controlID: string;
  const control = makeAccount('control', 'user');

  test.beforeAll(async ({ browser }) => {
    controlCtx = await browser.newContext();
    ({ userID: controlID } = await signUpAndVerify(controlCtx, control));
  });

  test.afterEach(async () => {
    await startService('rabbitmq');
  });

  test.afterAll(async () => {
    expect(await dbOne(`select deleted_at is null from users where id = '${controlID}'`)).toBe('t');
    await controlCtx.close();
  });

  /** Walks the Account screen's type-to-confirm deletion and resolves with the DELETE /auth/me status. */
  async function deleteFromAccountScreen(page: Page): Promise<number> {
    await openTab(page, 'Account');
    await button(page, 'Delete account').click();
    const confirm = button(page, 'Permanently delete account');
    await expect(confirm).toBeDisabled();
    await field(page, 'Type "delete" to confirm').fill('delete');
    await expect(confirm).toBeEnabled();
    const [response] = await Promise.all([
      page.waitForResponse((res) => res.url().endsWith('/api/v1/auth/me') && res.request().method() === 'DELETE'),
      confirm.click(),
    ]);
    return response.status();
  }

  /** Signs `account` in through the API and resolves with the HTTP status (200 while it exists, 401 once deleted). */
  async function signInStatus(account: Account): Promise<number> {
    const res = await api('POST', '/auth/signin', { body: { email: account.email, password: account.password } });
    return res.status;
  }

  test('type-to-confirm deletes only the requested account; the worker removes the rows', async ({ browser }) => {
    const ctx = await browser.newContext();
    const victim = makeAccount('victim', 'user');
    const { page, userID } = await signUpAndVerify(ctx, victim);
    expect(await signInStatus(victim)).toBe(200);

    expect(await deleteFromAccountScreen(page)).toBe(202);

    // Successful self-delete lands on a clean Sign In screen: no session, no banner.
    await expectSignedOut(page);
    await expect(sessionEndedNotice(page)).toHaveCount(0);
    expect(await page.evaluate((key) => window.localStorage.getItem(key), SESSION_KEY)).toBeNull();

    await waitForDb(`select count(*) from users where id = '${userID}'`, (v) => v === '0', { timeoutMs: 60_000 });
    expect(await dbCount(`select count(*) from account_deletions where user_id = '${userID}'`)).toBe(0);
    expect(await dbCount(`select count(*) from refresh_tokens where user_id = '${userID}'`)).toBe(0);
    expect(await signInStatus(victim)).toBe(401);

    // The control account is untouched and can still sign in.
    expect(await dbCount(`select count(*) from users where id = '${controlID}'`)).toBe(1);
    expect(await signInStatus(control)).toBe(200);
    await ctx.close();
  });

  test('with rabbitmq down the deletion is still accepted and the relay drains it once the broker is back', async ({
    browser,
  }) => {
    test.setTimeout(4 * 60_000);
    const ctx = await browser.newContext();
    const victim = makeAccount('broker-down', 'user');
    const { page, userID } = await signUpAndVerify(ctx, victim);

    await stopService('rabbitmq');
    expect(await deleteFromAccountScreen(page)).toBe(202);
    await expectSignedOut(page);
    await expect(sessionEndedNotice(page)).toHaveCount(0);

    // Durable state is written even though nothing could be published.
    expect(await dbOne(`select deleted_at is not null from users where id = '${userID}'`)).toBe('t');
    expect(await dbOne(`select published_at is null from account_deletions where user_id = '${userID}'`)).toBe('t');
    expect(await signInStatus(victim)).toBe(401);

    await startService('rabbitmq');
    // The worker's relay republishes unpublished deletions every few seconds
    // once the broker accepts connections again, and the consumer removes the rows.
    await waitForDb(`select count(*) from users where id = '${userID}'`, (v) => v === '0', { timeoutMs: 180_000 });
    expect(await dbCount(`select count(*) from account_deletions where user_id = '${userID}'`)).toBe(0);
    expect(await signInStatus(victim)).toBe(401);
    expect(await dbCount(`select count(*) from users where id = '${controlID}'`)).toBe(1);
    await ctx.close();
  });
});
