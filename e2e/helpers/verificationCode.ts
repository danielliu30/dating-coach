import { compose, sleep } from './stack';

const CODE_RE = /Your verification code is: (\d{6})/;

/**
 * Scrapes the most recent six-digit verification code the api logged for
 * `email`. Without SMTP the api writes each mail as one JSON log line carrying
 * `"to":"<email>"` and a body containing `Your verification code is: NNNNNN`;
 * this reads `docker compose logs api --since 5m`, keeps the lines addressed
 * to `email`, and returns the code from the last one (a resend supersedes the
 * earlier code). Retries for up to `timeoutMs` because the log line can trail
 * the HTTP response by a moment. Codes expire after 3 minutes and are
 * discarded after 3 wrong attempts, so callers should use them promptly.
 */
export async function verificationCode(email: string, timeoutMs = 20_000): Promise<string> {
  const needle = `"to":${JSON.stringify(email)}`;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    const logs = await compose(['logs', 'api', '--since', '5m', '--no-log-prefix']);
    const line = logs
      .split('\n')
      .filter((l) => l.includes(needle) && CODE_RE.test(l))
      .pop();
    const match = line ? CODE_RE.exec(line) : null;
    if (match?.[1]) return match[1];
    if (Date.now() > deadline) throw new Error(`no verification code logged for ${email} within ${timeoutMs}ms`);
    await sleep(500);
  }
}
