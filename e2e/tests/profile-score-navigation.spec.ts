import { expect, test } from '@playwright/test';
import { makeAccount, signUpAndVerify } from '../helpers/signUpAndVerify';
import { button, field, openTab } from '../helpers/ui';

test('opening saved score evidence first keeps the analysis submission screen reachable', async ({ context }) => {
  const { page } = await signUpAndVerify(context, makeAccount('profile-score-navigation', 'user'));
  await openTab(page, 'Analyse');
  await field(page, 'Title').fill('A plan for Saturday');
  await field(page, 'Transcript — one message per line, prefixed "me:" or "them:"').fill([
    "me: I'd like to try the cafe together.",
    "them: I can't make Friday.",
    'me: Got it, how about Saturday?',
    'them: Let me check.',
    'me: We could try the cafe by the station.',
  ].join('\n'));
  await button(page, 'Get feedback').click();
  await expect(page.getByText('Overall engagement', { exact: true })).toBeVisible({ timeout: 60_000 });

  await page.goto('/account');
  await button(page, 'Review analysis 1').click();
  await expect(page.getByText('Overall engagement', { exact: true })).toBeVisible();
  await openTab(page, 'Analyse');
  await expect(field(page, 'Title')).toBeVisible();
  await button(page, 'Open').click();
  await expect(page).toHaveURL(/\/analyse\/history$/);
});
