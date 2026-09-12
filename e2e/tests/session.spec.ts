import { test, expect } from '@playwright/test';
import { SESSION_KEY, storedSession, writeStoredSession } from '../helpers/api';
import { makeAccount, signUpAndVerify } from '../helpers/signUpAndVerify';
import { db, dbCount, recreateService, waitForApi } from '../helpers/stack';
import { button, expectSignedOut, openTab, sessionEndedNotice } from '../helpers/ui';

/**
 * Persisted-session recovery and the session-ended notice on the Sign In
 * screen. The copy the app shows is decided by `SessionEndReason` in
 * app/src/api/client.ts: a refused renewal is always reported as `expired`
 * ("You were signed out"); the `revoked` copy ("Your session was ended") is
 * reserved for an explicit statement from the API, which no route makes
 * today. These tests pin that mapping so a change to it is a deliberate one.
 */
test.describe('persisted session', () => {
  test.describe.configure({ mode: 'serial' });

  test('corrupt localStorage blob → Sign In, not a stuck splash', async ({ browser }) => {
    const ctx = await browser.newContext();
    const { page } = await signUpAndVerify(ctx, makeAccount('corrupt', 'user'));
    await writeStoredSession(ctx, '{{{not json');
    await page.reload();
    await expectSignedOut(page);
    await expect(sessionEndedNotice(page)).toHaveCount(0);
    expect(await page.evaluate((key) => window.localStorage.getItem(key), SESSION_KEY)).toBeNull();
    await ctx.close();
  });

  test('past expires_at with a live refresh token → renewed silently, still signed in', async ({ browser }) => {
    const ctx = await browser.newContext();
    const { page, userID } = await signUpAndVerify(ctx, makeAccount('renew', 'user'));
    const stored = await storedSession(page);
    const rows = await dbCount(`select count(*) from refresh_tokens where user_id = '${userID}'`);
    await writeStoredSession(ctx, JSON.stringify({ ...stored, expires_at: '2000-01-01T00:00:00Z' }));
    await page.reload();
    await expect(page.getByRole('tab', { name: /Coaches/ })).toBeVisible({ timeout: 30_000 });
    await expect(sessionEndedNotice(page)).toHaveCount(0);
    const renewed = await storedSession(page);
    // Compared as a boolean so a failure never prints token material.
    expect(renewed.refresh_token !== stored.refresh_token).toBe(true);
    expect(Date.parse(renewed.expires_at)).toBeGreaterThan(Date.now());
    expect(await dbCount(`select count(*) from refresh_tokens where user_id = '${userID}'`)).toBe(rows + 1);
    await ctx.close();
  });

  test('past expires_at with the refresh tokens deleted → Sign In with the "expired" notice', async ({ browser }) => {
    const ctx = await browser.newContext();
    const { page, userID } = await signUpAndVerify(ctx, makeAccount('startup-expired', 'user'));
    const stored = await storedSession(page);
    await db(`delete from refresh_tokens where user_id = '${userID}'`);
    await writeStoredSession(ctx, JSON.stringify({ ...stored, expires_at: '2000-01-01T00:00:00Z' }));
    await page.reload();
    await expectSignedOut(page);
    await expect(page.getByText('You were signed out')).toBeVisible();
    await expect(page.getByText('Your session was ended')).toHaveCount(0);
    expect(await page.evaluate((key) => window.localStorage.getItem(key), SESSION_KEY)).toBeNull();

    // Dismiss clears the banner; signing back in works.
    await page.getByRole('button', { name: 'Dismiss' }).click();
    await expect(sessionEndedNotice(page)).toHaveCount(0);
    await ctx.close();
  });

  test('explicit Sign out shows no notice', async ({ browser }) => {
    const ctx = await browser.newContext();
    const { page } = await signUpAndVerify(ctx, makeAccount('signout', 'user'));
    await openTab(page, 'Account');
    await button(page, 'Sign out').click();
    await expectSignedOut(page);
    await expect(sessionEndedNotice(page)).toHaveCount(0);
    expect(await page.evaluate((key) => window.localStorage.getItem(key), SESSION_KEY)).toBeNull();
    await ctx.close();
  });
});

test.describe('short-lived tokens (JWT_TTL=30s, REFRESH_TOKEN_TTL=40s)', () => {
  test.describe.configure({ mode: 'serial' });
  test.setTimeout(5 * 60_000);

  test.beforeAll(async () => {
    await recreateService('api', { JWT_TTL: '30s', REFRESH_TOKEN_TTL: '40s' });
    await waitForApi();
  });

  test.afterAll(async () => {
    await recreateService('api', {});
    await waitForApi();
  });

  test('an expired access token is renewed behind "Refresh profile" while the refresh token lives', async ({
    browser,
  }) => {
    const ctx = await browser.newContext();
    const account = makeAccount('rotate', 'user');
    const { page, userID } = await signUpAndVerify(ctx, account);
    const before = await dbCount(`select count(*) from refresh_tokens where user_id = '${userID}'`);

    await page.waitForTimeout(32_000); // access token expired, refresh token (40s) still valid
    await openTab(page, 'Account');
    await button(page, 'Refresh profile').click();

    await expect(page.getByRole('tab', { name: /Account/ })).toBeVisible();
    await expect(sessionEndedNotice(page)).toHaveCount(0);
    // Rotation: a new refresh row, the old one marked used.
    await expect
      .poll(() => dbCount(`select count(*) from refresh_tokens where user_id = '${userID}'`), { timeout: 15_000 })
      .toBe(before + 1);
    expect(
      await dbCount(`select count(*) from refresh_tokens where user_id = '${userID}' and used_at is not null`),
    ).toBeGreaterThan(0);
    await ctx.close();
  });

  test('once both tokens have expired, the next request lands on Sign In with the "expired" notice', async ({
    browser,
  }) => {
    const ctx = await browser.newContext();
    const account = makeAccount('idle', 'user');
    const { page } = await signUpAndVerify(ctx, account);

    await page.waitForTimeout(45_000); // past REFRESH_TOKEN_TTL
    await openTab(page, 'Account');
    await button(page, 'Refresh profile').click();

    await expectSignedOut(page);
    await expect(page.getByText('You were signed out')).toBeVisible();
    await expect(page.getByText('Your session was ended')).toHaveCount(0);
    await ctx.close();
  });

  test('deleting the refresh tokens server-side ends the session with the same "expired" copy', async ({ browser }) => {
    const ctx = await browser.newContext();
    const account = makeAccount('revoked', 'user');
    const { page, userID } = await signUpAndVerify(ctx, account);
    await db(`delete from refresh_tokens where user_id = '${userID}'`);

    await page.waitForTimeout(32_000); // access token expired; nothing left to renew with
    await openTab(page, 'Account');
    await button(page, 'Refresh profile').click();

    await expectSignedOut(page);
    // A refused renewal cannot tell revocation from expiry, so the app says "expired".
    await expect(page.getByText('You were signed out')).toBeVisible();
    await expect(page.getByText('Your session was ended')).toHaveCount(0);
    await ctx.close();
  });
});
