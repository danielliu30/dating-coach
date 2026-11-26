# dating-coach guardrails

Monorepo: `backend/` (Go: `cmd/api` + `cmd/worker`, `internal/*`, `migrations/`, sqlc), `app/` (Expo SDK 57 web + jest), `ml-analyzer/` (FastAPI + heuristic/LLM scorers), `e2e/` (Playwright against the compose gateway), `.github/` + `scripts/` (CI, merge gate; deploy pipelines live in dating-coach-infra).

Each service directory has its own `AGENTS.md` — read it before working there.

## Non-negotiables

- **Security:** never commit or log secrets/tokens; prod endpoints fail closed on auth; don't weaken startup credential rejection, auth checks, or proxy-trust settings.
- **Correctness:** tests ship in the same PR as the code they cover; gates fail closed (an unresolvable condition blocks, never passes).
- **Performance:** no needless work in request/scoring hot paths; keep `/healthz` endpoints dependency-free.
- **Availability:** DB migrations only go `up` — every migration must be backward-compatible so old code and new schema coexist during a deploy; scale-to-zero services hold no in-process state or timers.

## Merge protocol

`main` is protected (strict, PR required). Required checks: `build-test`, `typecheck-export`, `test`, `e2e`, `dependency-gate`, `merge-gate-tests`. One logical change per PR; dependencies declared in the body (`Depends on: #N`); same-repo non-draft PRs auto-merge on green via `merge-gate.yml`. See `.agents/skills/pr-granularity/SKILL.md`.

Loadtest-surfaced issues (`loadtest-finding` in danielliu30/dating-coach-loadtest): label the fix PR `loadtest-fix` — it dispatches the suite against the head SHA (or comments the manual command until `LOADTEST_DISPATCH_PAT` is set). Don't merge a load finding's fix on app checks alone; the finding must not recur under load.

## Workflow skills — apply every session

Invoke these whenever their trigger matches — they are part of the working protocol, not optional references:

- `dev-cycle` — end-to-end loop for any code-change task: decompose → implement → verify → review → PR → merge.
- `coding-efficiency` — every code-change task (implement, fix, refactor, add, remove).
- `pr-granularity` — any change that splits into logical units; decompose before coding.
- `dating-coach-verify` — before reporting any change; cheapest sufficient check.
- `review` — self-review the final `git diff` before reporting; also `/review <PR#>` for GitHub PRs.

This repo is canonical for `.agents/skills/` — the loadtest, pentest and infra repos carry verbatim copies. After editing a skill, run `scripts/sync-skills.sh` (or let the `skills-sync` workflow do it on merge); `scripts/sync-skills.sh --check` reports drift.

## Ops skills — invoke when needed

- `testing-dating-coach` — local stack bring-up, click-through recipes, adversarial scenarios.
- `running-dating-coach-in-docker` — images, local vs Cloud Run deploys (pipelines live in `danielliu30/dating-coach-infra`), rollback, backups.
