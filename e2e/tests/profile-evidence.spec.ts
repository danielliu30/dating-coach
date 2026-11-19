import { expect, test } from '@playwright/test';
import { api } from '../helpers/api';
import { makeAccount, signUpAndVerify } from '../helpers/signUpAndVerify';
import { dbOne } from '../helpers/stack';
import { button, field, openTab } from '../helpers/ui';

test('analysis click-through persists cited evidence on the owner-only result', async ({ browser, context }) => {
  const { page, token } = await signUpAndVerify(context, makeAccount('profile-evidence', 'user'));
  const cases = [
    {
      title: 'Repairs an overlooked constraint',
      transcript: [
        "me: I'd like to try the cafe together.",
        "them: I can't make Friday.",
        'me: How about Friday?',
        'me: Sorry, I missed that. How about Saturday?',
        'them: Let me check.',
        'me: We could meet at the cafe.',
      ],
      coverage: 'eligible',
      feedback: 'strong',
      selfCorrection: { level: 'strong', self_positions: [2, 3] },
    },
    {
      title: 'Repeatedly overlooks a constraint',
      transcript: [
        "me: I'd like to try the cafe together.",
        "them: I can't make Friday.",
        'me: How about Friday?',
        'them: Did you see my reply?',
        'me: Would Friday work?',
      ],
      coverage: 'eligible',
      feedback: 'needs_coaching',
      selfCorrection: { level: 'needs_coaching', self_positions: [2, 4], issue: 'repeated_ignored_constraint' },
    },
    {
      title: 'Not enough evidence',
      transcript: ['me: Would you like to try the cafe?', 'them: Let me check.'],
      coverage: 'insufficient',
      feedback: 'unknown',
      selfCorrection: { level: 'unknown', self_positions: [] },
    },
  ];
  const resultPaths: string[] = [];
  for (const example of cases) {
    await test.step(example.title, async () => {
      await page.goto('/analyse');
      await expect(page.getByText('Analyse a conversation')).toBeVisible();
      await field(page, 'Title').fill(example.title);
      await field(page, 'Transcript — one message per line, prefixed "me:" or "them:"').fill(example.transcript.join('\n'));
      const [submission] = await Promise.all([
        page.waitForResponse((response) =>
          response.url().endsWith('/api/v1/analysis/conversations') && response.request().method() === 'POST',
        ),
        button(page, 'Get feedback').click(),
      ]);
      expect(submission.status()).toBe(202);
      const { id } = await submission.json() as { id: string };
      expect(id).toMatch(/^[a-f0-9-]{36}$/);
      const path = `/analysis/results/${id}`;
      resultPaths.push(path);

      await expect(page.getByText('Overall engagement')).toBeVisible({ timeout: 90_000 });
      const result = await api<{ status: string; overall: { profile_evidence: unknown } }>('GET', path, { token });
      expect(result.status).toBe(200);
      expect(result.body?.status).toBe('succeeded');
      const evidence = result.body?.overall.profile_evidence;
      expect(evidence).toMatchObject({
        rubric_version: 'profile-v1',
        coverage: example.coverage,
        feedback_needed: { level: example.feedback },
        self_correction: example.selfCorrection,
      });
      const stored = await dbOne(`select overall->'profile_evidence' from analysis_results where id = '${id}'`);
      expect(JSON.parse(stored)).toEqual(evidence);
    });
  }

  await page.goto('/analyse');
  await button(page, 'Open').click();
  await page.getByText(cases[0].title, { exact: true }).click();
  await expect(page.getByText('Overall engagement')).toBeVisible();

  const stranger = await browser.newContext();
  try {
    const second = await signUpAndVerify(stranger, makeAccount('profile-stranger', 'user'));
    await openTab(second.page, 'Analyse');
    await button(second.page, 'Open').click();
    await expect(second.page.getByText(cases[0].title, { exact: true })).toHaveCount(0);
    for (const path of resultPaths) {
      expect((await api('GET', path, { token: second.token })).status).toBe(403);
      expect((await api('GET', path)).status).toBe(401);
    }
  } finally {
    await stranger.close();
  }
});
