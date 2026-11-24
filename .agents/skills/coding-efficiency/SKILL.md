---
name: coding-efficiency
description: Efficient-change playbook for this repo. Apply to any code-change task (implement, fix, refactor, add, remove) - read the touched service's AGENTS.md, batch exploration, make the smallest correct diff, ship tests with the code, and verify with the cheapest sufficient check.
allowed-tools:
  - read
  - edit
  - grep
  - glob
  - exec
---

# Coding efficiency playbook

## When to apply

Any code-change task: implement, fix, refactor, add, remove. Apply it *before*
writing code - the exploration and verification choices decide how many rounds
the task takes.

## Procedure

1. **Read the guardrails of the service you are touching first.** Each service
   directory has its own `AGENTS.md` (`backend/`, `app/`, `ml-analyzer/`,
   `e2e/`); its invariants are hard constraints, not suggestions. The root
   `AGENTS.md` covers the cross-cutting rules.
2. **Map the change in as few rounds as possible.** Batch grep/glob and
   parallel reads instead of serially opening files. Read the imports and
   callers of code you are about to touch. Confirm a dependency is already in
   use before importing it - check `go.mod` / `package.json` /
   `requirements.txt`, do not assume.
3. **Make the smallest correct diff.** Edit surgically. Reuse existing helpers,
   patterns and conventions; match the surrounding code style. No drive-by
   refactors, no dead code, no speculative abstractions.
4. **Ship tests with the code.** Tests go in the same change as the code they
   cover - never deferred. Put them where the service keeps them: `*_test.go`
   beside the Go code, `app/test_*.py` in ml-analyzer, jest specs in `app/`,
   Playwright specs in `e2e/` for user-visible flows.
5. **Verify with the cheapest sufficient check.** Use the
   `dating-coach-verify` skill to pick the minimum command set for the change
   type. Run the narrowest test that covers what you changed; full gates and
   the e2e suite only when the change warrants them - CI runs everything on
   every PR anyway.
6. **Self-review the final diff.** Run `git diff` before reporting and check it
   against the service invariants from step 1. For a deeper pass, invoke the
   `review` skill.
7. **Decompose before you code, not after.** If the task splits into multiple
   logical units, apply the `pr-granularity` skill to plan the PR sequence
   first - one function or self-contained piece of logic per PR, dependencies
   first.

## Forbidden

- Importing or adding a dependency without checking it is already used. For new
  deps: prefer versions published long enough to be vetted, pin them, no
  floating ranges.
- Guessing an API's signature or behavior instead of reading its usage in the
  codebase.
- Weakening a gate, auth check, security header, or proxy-trust setting to make
  something pass.
- Running the full e2e suite or rebuilding the compose stack when a targeted
  unit run answers the question.
- Leaving the working tree dirtier than you found it: stray debug prints,
  commented-out code, unrelated reformats.
