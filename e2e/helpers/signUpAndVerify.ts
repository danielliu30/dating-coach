import { expect, type BrowserContext, type Page } from '@playwright/test';
import { storedSession } from './api';
import { dbOne } from './stack';
import { button, field } from './ui';
import { verificationCode } from './verificationCode';

/** Account details for `signUpAndVerify`. `role` is the API role; the UI label is derived from it. */
export interface Account {
  email: string;
  password: string;
  displayName: string;
  role: 'user' | 'coach';
}

/** What `signUpAndVerify` hands back: the signed-in page, the account's user id and its access token. */
export interface SignedIn {
  page: Page;
  userID: string;
  token: string;
}

const ROLE_LABEL: Record<Account['role'], string> = {
  user: 'Someone dating',
  coach: 'A coach',
};

let counter = 0;

/**
 * Builds a unique, recognisable test account. Emails are
 * `e2e-<prefix>-<stamp>@example.com` (display name `<prefix> <stamp>`) so DB rows from one run never
 * collide with another and are easy to find in `docker compose logs api`.
 */
export function makeAccount(prefix: string, role: Account['role']): Account {
  counter += 1;
  const stamp = `${Date.now().toString(36)}${counter}`;
  return {
    email: `e2e-${prefix}-${stamp}@example.com`.toLowerCase(),
    password: 'correct-horse-battery',
    displayName: `${prefix} ${stamp}`,
    role,
  };
}

/**
 * Creates an account through the real UI: opens the app in a new page of
 * `context`, fills the Sign Up screen (role radio "Someone dating" → `user`,
 * "A coach" → `coach`), then enters the six-digit code scraped from the api
 * logs on the Verify screen. Resolves once the app is inside the signed-in
 * tabs, with the user's id (from `users`) and the access token the app stored.
 *
 * Use one context per role: the app keeps its session in localStorage under
 * `dating-coach.session`, so two accounts in one context would overwrite each
 * other. If the first code is rejected the helper presses "Resend code" and
 * retries once with the newer code.
 */
export async function signUpAndVerify(context: BrowserContext, account: Account): Promise<SignedIn> {
  const page = await context.newPage();
  await page.goto('/');
  await expect(page.getByText('Sign in to Dating Humane')).toBeVisible({ timeout: 60_000 });
  await button(page, 'Create an account').click();

  await expect(page.getByText('Create your account')).toBeVisible();
  await field(page, 'Name').fill(account.displayName);
  await field(page, 'Email').fill(account.email);
  await field(page, 'Password').fill(account.password);
  await page.getByRole('radio', { name: ROLE_LABEL[account.role] }).click();
  await button(page, 'Create account').click();

  await expect(page.getByText('Check your inbox')).toBeVisible({ timeout: 30_000 });
  let code = await verificationCode(account.email);
  await field(page, 'Verification code').fill(code);
  await button(page, 'Verify email').click();

  const signedIn = page.getByRole('tab').first();
  const accepted = await signedIn
    .waitFor({ timeout: 20_000 })
    .then(() => true)
    .catch(() => false);
  if (!accepted) {
    await button(page, 'Resend code').click();
    code = await verificationCode(account.email);
    await field(page, 'Verification code').fill(code);
    await button(page, 'Verify email').click();
    await expect(signedIn).toBeVisible({ timeout: 30_000 });
  }

  const userID = await dbOne(`select id from users where email = '${account.email}'`);
  const token = (await storedSession(page)).token;
  return { page, userID, token };
}

/**
 * Signs an existing, verified account in through the Sign In screen and
 * resolves with the page once the tabs are showing. Uses `page` when given
 * (it must already show Sign In, e.g. right after Sign out), otherwise opens
 * a new page in `context`.
 */
export async function signIn(
  context: BrowserContext,
  account: Pick<Account, 'email' | 'password'>,
  page?: Page,
): Promise<Page> {
  if (!page) {
    page = await context.newPage();
    await page.goto('/');
  }
  await expect(page.getByText('Sign in to Dating Humane')).toBeVisible({ timeout: 60_000 });
  await field(page, 'Email').fill(account.email);
  await field(page, 'Password').fill(account.password);
  await button(page, 'Sign in').click();
  await expect(page.getByRole('tab').first()).toBeVisible({ timeout: 30_000 });
  return page;
}
