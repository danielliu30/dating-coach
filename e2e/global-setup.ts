import { compose, db, dbOne, waitForApi, waitForMl } from './helpers/stack';

/** Compose services the suite depends on; every one must be running before tests start. */
const REQUIRED = ['postgres', 'redis', 'rabbitmq', 'api', 'worker', 'ml-analyzer', 'web', 'nginx'];

/**
 * Playwright global setup: verifies the stack brought up by
 * `docker compose --env-file e2e/.env --profile gateway up -d --build` is
 * ready. Polls `/healthz` and `/ml/healthz` through nginx, checks
 * `docker compose ps` shows every required service running (and healthy where
 * a healthcheck exists), and confirms migrations applied on the fresh DB.
 * Fails fast with the offending service instead of letting every spec time out.
 */
export default async function globalSetup(): Promise<void> {
  await waitForApi();
  const ml = await waitForMl();
  console.log(`ml-analyzer ready: model_version=${String(ml.model_version)} backend=${String(ml.active_backend)}`);

  const ps = await compose(['ps', '--format', 'json']);
  const rows = ps
    .split('\n')
    .filter((line) => line.trim() !== '')
    .map((line) => JSON.parse(line) as { Service: string; State: string; Health: string });
  for (const name of REQUIRED) {
    const row = rows.find((r) => r.Service === name);
    if (!row) throw new Error(`service ${name} is not part of the running stack:\n${ps}`);
    if (row.State !== 'running') throw new Error(`service ${name} is ${row.State}, expected running`);
    if (row.Health && row.Health !== 'healthy') throw new Error(`service ${name} health is ${row.Health}`);
  }

  const version = Number(await dbOne('select version from schema_migrations'));
  const dirty = await dbOne('select dirty from schema_migrations');
  if (!(version >= 7) || dirty !== 'f') {
    throw new Error(`schema_migrations is at version ${version} (dirty=${dirty}); expected a clean schema >= 7`);
  }
  console.log(`schema_migrations version=${version}`);

  // Accounts from earlier runs (makeAccount() uses this domain) would otherwise
  // accumulate and push fresh coaches past the directory's default page of 25.
  const purged = await db(
    "with gone as (delete from users where email like 'e2e-%@example.com' returning 1) select count(*) from gone",
  );
  console.log(`purged ${purged.trim()} stale e2e account(s)`);
}
