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
3. **Keep the code minimal - no bloat.** Implement only what the task asks.
   Adhere to the codebase's coding conventions: reuse existing helpers and
   patterns, match the surrounding style, edit surgically. No drive-by
   refactors, dead code, speculative abstractions, or unrelated changes.
4. **Ship tests with the code.** Tests go in the same change as the code they
   cover - never deferred. Put them where the service keeps them: `*_test.go`
   beside the Go code, `app/test_*.py` in ml-analyzer, jest specs in `app/`,
   Playwright specs in `e2e/` for user-visible flows.
5. **Verify with the cheapest sufficient check.** Use the
   `dating-coach-verify` skill to pick the minimum command set for the change
   type. Run the narrowest test that covers what you changed; full gates and
   the e2e suite only when the change warrants them - CI runs everything on
   every PR anyway.
6. **Self-review the final diff.** Run `git diff`, then invoke the `review`
   skill on it and act on its findings before reporting.
7. **Decompose before you code.** Large prompts and multi-unit tasks are
   broken into PR-sized pieces first - `dev-cycle` owns the end-to-end
   sequence, `pr-granularity` owns the PR-splitting rules.
8. **Contain scope creep.** The diff stays scoped to the task at hand. When the
   work grows a new logic block, do not bundle it - give it its own PR, stacked
   on the current branch when it depends on the current one (the
   `pr-granularity` skill covers the stacking protocol).

## Forbidden

- Importing or adding a dependency without checking it is already used. For new
  deps: prefer versions published long enough to be vetted, pin them, no
  floating ranges.
- Guessing an API's signature or behavior instead of reading its usage in the
  codebase.
- Weakening a gate, auth check, security header, or proxy-trust setting to make
  something pass.
- Bundling a new logic block into a PR it does not belong to.
- Running the full e2e suite or rebuilding the compose stack when a targeted
  unit run answers the question.
- Leaving the working tree dirtier than you found it: stray debug prints,
  commented-out code, unrelated reformats.
