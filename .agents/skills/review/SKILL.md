---
name: review
description: Review local changes or a GitHub PR against the repo's guardrails - severity-ordered findings with file:line, a verdict, and the tests that would prove the risky parts.
argument-hint: "[PR number]"
allowed-tools:
  - read
  - grep
  - glob
  - exec
---

# Code review

## Pick the diff

- **No argument - local changes.** `git diff --staged` if anything is staged,
  else `git diff`; on a feature branch with committed work,
  `git diff main...HEAD` (or `origin/main...HEAD`).
- **Numeric argument - a GitHub PR.** `gh pr view <n>`, `gh pr diff <n>`,
  `gh pr checks <n>`. Fetch review comments too: `gh pr view <n> --comments`
  plus `gh api repos/{owner}/{repo}/pulls/<n>/comments` for inline notes -
  automated reviewers (e.g. Devin Review) count as findings; verify each
  against the code before accepting or rebutting it. For fork PRs, inspect
  the `.github/**` portion of the diff before anything else - a fork could
  weaken its own checks - and never auto-approve one (see the
  `pr-granularity` skill).

Read enough surrounding code and the diff's callers to judge correctness - a
diff alone hides context.

## Repo checklist (from AGENTS.md non-negotiables)

**Security**
- No secrets, tokens, verification codes or credentials committed or logged
  (log variable names, never values).
- Auth checks not weakened; prod endpoints still fail closed; startup
  credential rejection intact.
- `TRUSTED_PROXY_*` / `X-Forwarded-*` handling unchanged or justified.
- `nginx.conf` / web security headers not weakened.
- Tests don't capture or print tokens, codes, or WS frames carrying the JWT.

**Correctness**
- Tests ship in the same change as the code they cover.
- Gates fail closed: an unresolvable condition blocks, never passes.
- Stacked PRs declare `Depends on: #N` in the body; no circular dependencies.
- `/pubsub/*` and `/tasks/*` worker handlers are idempotent and replay-safe.

**Performance**
- No needless work in request or scoring hot paths.
- `/healthz` endpoints stay dependency-free.

**Availability**
- Migrations only go `up` and are backward-compatible (old binary + new schema
  coexist mid-deploy); merged migrations never edited; no duplicate version
  numbers in `backend/migrations`.
- Scale-to-zero services (worker) hold no in-process state or timers.
- Chat offline-queue, token-refresh and WS-replay behavior keeps the e2e
  `resilience` contract.

**General quality**
- Logic errors, missed edge cases, wrong error-handling boundaries.
- Consistent with surrounding conventions; reuses existing helpers.
- Addresses only the asked scope: no dead code, bloat, stray debug output, or
  out-of-scope changes - scope creep belongs in its own PR.

## Report

Findings ordered by severity:

| Severity | Meaning |
|---|---|
| blocker | Must fix before merge - security, data loss, breaks an existing flow, weakens a gate |
| major | Should fix - real bug, missing tests, invariant risk |
| minor | Worth fixing - edge case, clarity |
| nit | Optional - style, naming |

Each finding: `file:line`, what is wrong, and a suggested fix. Then:

- **Verdict**: approve / request changes / comment-only.
- **Proving tests**: the specific tests that would demonstrate the risky parts
  are correct.
- **Self-review loop**: when the diff is yours, fix blocker/major findings and
  re-verify instead of reporting them - only minors/nits may ship flagged.

## Posting

Print findings by default. Only run `gh pr review <n>` (`--comment` or
`--request-changes`) when the user explicitly asks. A review is read-only
otherwise - never modify code while reviewing.

For merge order, dependency declarations, or auto-merge questions, defer to
the `pr-granularity` skill.
