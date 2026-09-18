import { execFile } from 'node:child_process';
import path from 'node:path';
import { promisify } from 'node:util';

const run = promisify(execFile);

/** Repository root; every compose command runs from here so the default docker-compose.yml is found. */
export const REPO_ROOT = path.resolve(__dirname, '..', '..');

/**
 * Arguments that select the e2e stack: the dedicated env file and the gateway
 * profile (so `up`/`down`/`ps` include web + nginx).
 */
const COMPOSE_ARGS = ['compose', '--env-file', path.join('e2e', '.env'), '--profile', 'gateway'];

/** Origin the browser and the health polls use; must match NGINX_PORT in e2e/.env. */
export const BASE_URL = `http://localhost:${process.env.NGINX_PORT ?? '80'}`;

/**
 * Runs `docker compose <args>` against the e2e stack and resolves with stdout.
 *
 * Environment variables in `env` are layered over the process environment,
 * which is how a test overrides a value from e2e/.env for one `up -d <service>`
 * (shell variables win over `--env-file` in compose interpolation). Rejects
 * with the compose error (stderr included) on a non-zero exit.
 */
export async function compose(args: string[], env: NodeJS.ProcessEnv = {}): Promise<string> {
  const { stdout } = await run('docker', [...COMPOSE_ARGS, ...args], {
    cwd: REPO_ROOT,
    env: { ...process.env, ...env },
    maxBuffer: 64 * 1024 * 1024,
  });
  return stdout;
}

/**
 * Runs one SQL statement inside the postgres container with
 * `psql -tAc` (tuples only, unaligned) and resolves with the trimmed output.
 * One column per line, columns separated by `|`. An empty result set resolves
 * to ''. The statement must not contain unbalanced double quotes.
 */
export async function db(sql: string): Promise<string> {
  const out = await compose(['exec', '-T', 'postgres', 'psql', '-U', 'datingcoach', '-d', 'datingcoach', '-tAc', sql]);
  return out.trim();
}

/** `db` for statements that return exactly one row of one column. Throws when nothing comes back. */
export async function dbOne(sql: string): Promise<string> {
  const out = await db(sql);
  if (out === '') throw new Error(`query returned no rows: ${sql}`);
  const first = out.split('\n')[0];
  return first ?? '';
}

/** `db` for `select count(*) ...` style statements; resolves with the number. */
export async function dbCount(sql: string): Promise<number> {
  return Number(await dbOne(sql));
}

/**
 * Re-runs `sql` every `intervalMs` until `predicate` accepts the result or
 * `timeoutMs` passes, resolving with the accepted value. This is how tests
 * wait on the worker (deletions, booking expiry) without sleeping blindly.
 */
export async function waitForDb(
  sql: string,
  predicate: (value: string) => boolean,
  { timeoutMs = 60_000, intervalMs = 1_000 } = {},
): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  let last = '';
  while (Date.now() < deadline) {
    last = await db(sql);
    if (predicate(last)) return last;
    await sleep(intervalMs);
  }
  throw new Error(`timed out waiting on ${sql}; last result: ${JSON.stringify(last)}`);
}

/**
 * Container state of a compose service plus its last log lines, for error
 * messages when a service never comes back after a restart. Never throws: a
 * broken compose invocation is reported inline so the original failure is kept.
 */
export async function serviceDiagnostics(name: string, tailLines = 40): Promise<string> {
  const sections: string[] = [];
  for (const args of [
    ['ps', '-a', '--format', 'table {{.Service}}\t{{.State}}\t{{.Status}}'],
    ['logs', '--no-color', '--no-log-prefix', '--tail', String(tailLines), name],
  ]) {
    try {
      sections.push(`$ docker compose ${args.join(' ')}\n${(await compose(args)).trimEnd()}`);
    } catch (err) {
      sections.push(`$ docker compose ${args.join(' ')}\n${err instanceof Error ? err.message : String(err)}`);
    }
  }
  return sections.join('\n\n');
}

/** Stops a compose service (`docker compose stop <name>`); its container stays around to be started again. */
export async function stopService(name: string): Promise<void> {
  await compose(['stop', name]);
}

/** Starts a previously stopped compose service (`docker compose start <name>`). */
export async function startService(name: string): Promise<void> {
  await compose(['start', name]);
}

/**
 * Recreates one service with extra environment overrides, e.g.
 * `recreateService('api', { JWT_TTL: '30s' })`. Pass `{}` to go back to the
 * values in e2e/.env. `--no-deps` keeps the rest of the stack untouched.
 */
export async function recreateService(name: string, env: NodeJS.ProcessEnv): Promise<void> {
  await compose(['up', '-d', '--no-deps', '--force-recreate', name], env);
}

/** Promise that resolves after `ms` milliseconds. */
export function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

/**
 * Polls `GET /healthz` through nginx until it answers 200 with
 * `{"status":"ok"}` or `timeoutMs` passes. Used after (re)starting the api so
 * a test never races the container coming up.
 */
export async function waitForApi(timeoutMs = 90_000): Promise<void> {
  await waitForJson(`${BASE_URL}/healthz`, (body) => body.status === 'ok', timeoutMs, 'api');
}

/**
 * Polls the ml-analyzer through nginx (`GET /ml/healthz`) until it reports a
 * `model_version`, resolving with the health payload.
 */
export async function waitForMl(timeoutMs = 90_000): Promise<Record<string, unknown>> {
  return waitForJson(`${BASE_URL}/ml/healthz`, (body) => typeof body.model_version === 'string', timeoutMs, 'ml-analyzer');
}

/**
 * Fetches `url` until the JSON body satisfies `ok` or `timeoutMs` passes.
 * Connection errors and non-JSON bodies (nginx 502 pages) count as "not yet".
 * On timeout the error carries `serviceDiagnostics(service)` so a container
 * that never came back can be told apart from one nginx could not reach.
 */
async function waitForJson(
  url: string,
  ok: (body: Record<string, unknown>) => boolean,
  timeoutMs: number,
  service: string,
): Promise<Record<string, unknown>> {
  const deadline = Date.now() + timeoutMs;
  let lastError = 'no response yet';
  while (Date.now() < deadline) {
    try {
      const res = await fetch(url);
      const text = await res.text();
      const body = JSON.parse(text) as Record<string, unknown>;
      if (res.ok && ok(body)) return body;
      lastError = `${res.status} ${text}`;
    } catch (err) {
      lastError = err instanceof Error ? err.message : String(err);
    }
    await sleep(1_000);
  }
  throw new Error(`${url} not ready after ${timeoutMs}ms: ${lastError}\n\n${await serviceDiagnostics(service)}`);
}
