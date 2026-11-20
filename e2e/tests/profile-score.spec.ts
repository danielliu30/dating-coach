import { expect, test, type Page } from '@playwright/test';
import { api, tokenOf } from '../helpers/api';
import { makeAccount, signIn, signUpAndVerify } from '../helpers/signUpAndVerify';
import { dbCount, startService, stopService, waitForApi, waitForDb, waitForMl } from '../helpers/stack';
import { button, expectSignedOut, field, openTab } from '../helpers/ui';

const scorePath = '/analysis/profile-score';

function transcript(venue: string, behavior: 'strong' | 'partial' | 'missed'): string[] {
  return [
    `me: I'd like to go to ${venue} with you.`,
    "them: I can't make Friday.",
    behavior === 'strong' ? 'me: Got it, how about Saturday?' :
      behavior === 'partial' ? 'me: How about Saturday?' : 'me: How about Friday?',
    'them: Did you see my reply?',
    behavior === 'missed' ? 'me: Would Friday work?' : `me: We could meet at ${venue}.`,
  ];
}

async function submit(page: Page, title: string, messages: readonly string[]) {
  await page.goto('/analyse');
  await field(page, 'Title').fill(title);
  await field(page, 'Transcript — one message per line, prefixed "me:" or "them:"').fill(messages.join('\n'));
  const [submission] = await Promise.all([
    page.waitForResponse((response) =>
      response.url().endsWith('/api/v1/analysis/conversations') && response.request().method() === 'POST',
    ),
    button(page, 'Get feedback').click(),
  ]);
  expect(submission.status()).toBe(202);
  const result = await submission.json() as { id: string; conversation_id: string };
  expect(result.id).toMatch(/^[a-f0-9-]{36}$/);
  expect(result.conversation_id).toMatch(/^[a-f0-9-]{36}$/);
  return result;
}

const completed = (page: Page) => expect(page.getByText('Overall engagement', { exact: true })
  .and(page.locator(':not([aria-hidden="true"] *)'))
  .filter({ visible: true })).toBeVisible({ timeout: 60_000 });
const scoreCard = (page: Page) => page.getByTestId('profile-score-card');

async function expectBand(page: Page, label: string, count: string) {
  await expect(scoreCard(page).getByRole('heading', { name: label, exact: true })).toBeVisible();
  await expect(scoreCard(page).getByText(count, { exact: true })).toBeVisible();
}

test('account score updates from real worker evidence and stays private through sign-out and deletion', async ({ browser, context }) => {
  const account = makeAccount('profile-score', 'user');
  const { page, userID } = await signUpAndVerify(context, account);
  const strong = [
    "me: I'd like to try the cafe together.",
    "them: I can't make Friday.",
    'me: How about Friday?',
    'me: Sorry, I missed that. How about Saturday?',
    'them: Let me check.',
    'me: We could meet at the cafe.',
  ];

  await test.step('collect evidence and ignore thin or duplicate transcripts', async () => {
    await openTab(page, 'Account');
    await expectBand(page, 'Collecting evidence', '0 of 3 qualifying conversations');
    await page.getByRole('checkbox', { name: 'Reading feedback', exact: true }).click();
    await button(page, 'Save dating profile').click();
    await expect(page.getByText('Saved.', { exact: true })).toBeVisible();
    await button(page, 'Analyse a conversation').click();
    await expect(field(page, 'Title')).toBeVisible();
    await submit(page, 'Too thin', ['me: Would you like to try the cafe?', 'them: Let me check.']);
    await completed(page);
    await submit(page, 'Considerate plan', strong);
    await completed(page);
    await expect(page.getByText('Your goals', { exact: true })).toBeVisible();
    await expect(page.getByText('Reading feedback', { exact: true }).filter({ visible: true })).toBeVisible();
    await openTab(page, 'Account');
    await expectBand(page, 'Collecting evidence', '1 of 3 qualifying conversations');
    await submit(page, 'Reimport with different title and spacing', strong.map((line) => line.replaceAll(' ', '  ')));
    await completed(page);
    await openTab(page, 'Account');
    await expectBand(page, 'Collecting evidence', '1 of 3 qualifying conversations');
  });

  await test.step('Developing, then Strong, then Needs more coaching', async () => {
    for (const venue of ['the market', 'the garden']) {
      await submit(page, `Plan for ${venue}`, transcript(venue, 'partial'));
      await completed(page);
    }
    await openTab(page, 'Account');
    await expectBand(page, 'Developing', 'Based on 3 qualifying conversations');
    for (const venue of ['the park', 'the bookstore']) {
      await submit(page, `Plan for ${venue}`, transcript(venue, 'strong'));
      await completed(page);
    }
    await openTab(page, 'Account');
    await expectBand(page, 'Strong', 'Based on 5 qualifying conversations');
    for (const venue of ['the museum', 'the cinema']) {
      await submit(page, `Plan for ${venue}`, transcript(venue, 'missed'));
      await completed(page);
    }
    await openTab(page, 'Account');
    await expectBand(page, 'Needs more coaching', 'Based on 5 qualifying conversations');
    await expect(scoreCard(page).getByText(/scheduling constraint was repeatedly missed/)).toBeVisible();
    await expect(scoreCard(page).getByText(/What would help you notice/)).toHaveCount(1);
  });

  const score = await api<{ evidence: { analysis_id: string; conversation_id: string; submitted_at: string }[] }>(
    'GET', scorePath, { token: await tokenOf(page) },
  );
  expect(score.status).toBe(200);
  const sources = score.body?.evidence ?? [];
  expect(sources).toHaveLength(5);
  expect(Object.keys(sources[0] ?? {}).sort()).toEqual(['analysis_id', 'conversation_id', 'submitted_at']);
  expect(JSON.stringify(score.body)).not.toContain('the cinema');

  await test.step('open two different saved results from the score card', async () => {
    for (const [index, source] of sources.slice(0, 2).entries()) {
      await button(page, `Review analysis ${index + 1}`).click();
      await completed(page);
      await expect(page).toHaveURL(new RegExp(`/analyse/${source.analysis_id}(?:\\?|$)`));
      await button(page, 'View my profile score').click();
      await expectBand(page, 'Needs more coaching', 'Based on 5 qualifying conversations');
    }
  });

  const controlToken = await test.step('survive reload and sign-in without sharing the score with another account', async () => {
    await page.reload();
    await expectBand(page, 'Needs more coaching', 'Based on 5 qualifying conversations');
    await button(page, 'Sign out').click();
    await expectSignedOut(page);
    await signIn(context, account, page);
    await openTab(page, 'Account');
    await expectBand(page, 'Needs more coaching', 'Based on 5 qualifying conversations');

    const otherContext = await browser.newContext({ viewport: { width: 390, height: 844 } });
    try {
      const other = await signUpAndVerify(otherContext, makeAccount('profile-score-other', 'user'));
      await openTab(other.page, 'Account');
      await expectBand(other.page, 'Collecting evidence', '0 of 3 qualifying conversations');
      expect((await api('GET', `${scorePath}?user_id=${userID}`, { token: other.token })).body).toMatchObject({
        band: 'collecting_evidence', conversations_counted: 0, evidence: [],
      });
      const first = sources[0]!;
      expect((await api('GET', `/analysis/results/${first.analysis_id}`, { token: other.token })).status).toBe(403);
      await submit(other.page, 'A separate plan', transcript('the bakery', 'strong'));
      await completed(other.page);
      await openTab(other.page, 'Account');
      await expectBand(other.page, 'Collecting evidence', '1 of 3 qualifying conversations');
      return tokenOf(other.page);
    } finally {
      await otherContext.close();
    }
  });

  await test.step('a coach has no score card or score API access', async () => {
    const coachContext = await browser.newContext();
    try {
      const coach = await signUpAndVerify(coachContext, makeAccount('profile-score-coach', 'coach'));
      await openTab(coach.page, 'Account');
      await expect(coach.page.getByText('Dating Humane account', { exact: true })).toBeVisible();
      await expect(scoreCard(coach.page)).toHaveCount(0);
      expect((await api('GET', scorePath, { token: coach.token })).status).toBe(404);
    } finally {
      await coachContext.close();
    }
  });

  await test.step('delete the account and its supporting evidence', async () => {
    const oldToken = await tokenOf(page);
    await button(page, 'Delete account').click();
    await field(page, 'Type "delete" to confirm').fill('delete');
    await button(page, 'Permanently delete account').click();
    await expectSignedOut(page);
    await waitForDb(`select count(*) from users where id = '${userID}'`, (count) => count === '0');
    const first = sources[0]!;
    expect(await dbCount(`select count(*) from analysis_results where id = '${first.analysis_id}'`)).toBe(0);
    expect(await dbCount(`select count(*) from conversations where id = '${first.conversation_id}'`)).toBe(0);
    expect((await api('GET', scorePath, { token: oldToken })).status).toBe(401);
    expect((await api('GET', scorePath, { token: controlToken })).body).toMatchObject({ conversations_counted: 1 });
  });
});

test('score outage is separate from the rating; a successful analysis retry counts once', async ({ context }) => {
  const { page } = await signUpAndVerify(context, makeAccount('profile-score-outage', 'user'));
  await openTab(page, 'Account');
  await expectBand(page, 'Collecting evidence', '0 of 3 qualifying conversations');

  await test.step('failed analysis stays out of the score, then retry succeeds', async () => {
    await stopService('ml-analyzer');
    try {
      await submit(page, 'Retry this plan', transcript('the coffee shop', 'strong'));
      await expect(page.getByText('Analysis failed', { exact: true })).toBeVisible({ timeout: 90_000 });
      await openTab(page, 'Account');
      await expectBand(page, 'Collecting evidence', '0 of 3 qualifying conversations');
    } finally {
      await startService('ml-analyzer');
      await waitForMl();
    }
    await openTab(page, 'Analyse');
    await button(page, 'Try again').click();
    await completed(page);
    await openTab(page, 'Account');
    await expectBand(page, 'Collecting evidence', '1 of 3 qualifying conversations');
  });

  await test.step('score service failure shows retry, never a band or zero count', async () => {
    await openTab(page, 'Analyse');
    await stopService('api');
    try {
      await openTab(page, 'Account');
      await expect(scoreCard(page).getByText('Your profile score is unavailable right now.')).toBeVisible();
      await expect(scoreCard(page).getByText('Collecting evidence')).toHaveCount(0);
    } finally {
      await startService('api');
      await waitForApi();
    }
    await button(page, 'Retry profile score').click();
    await expectBand(page, 'Collecting evidence', '1 of 3 qualifying conversations');
  });
});
