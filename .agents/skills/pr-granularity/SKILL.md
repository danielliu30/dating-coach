---
name: pr-granularity
description: PR Granularity Playbook. Apply any time a code change is planned or made (implement, fix, refactor, add, remove) - decompose the change into the smallest logical units, one function or self-contained piece of logic per PR, with its tests, merged sequentially with dependencies first.
---

# PR Granularity Playbook

## When to apply

Any time a code change is planned or made: implement, fix, refactor, add, remove.
Apply it *before* writing code - the decomposition decides the branch/PR plan.

## Procedure

1. **Decompose into the smallest logical units.** Break the change so that each
   function (or single self-contained piece of logic) maps to exactly one PR.
2. **Split supporting edits into their own PRs.** If the change touches a shared
   helper or its callers, put those in separate PRs - do not bundle them with
   the main change.
3. **Preserve the existing flow.** New functionality must not break what already
   works. When a change would break an existing flow, create all the logical
   pieces needed to keep it working, and keep each piece as its own separate
   logical block/PR (don't combine them).
4. **Merge sequentially.** Because the pieces depend on each other, merge in
   explicit order: dependencies first, then the pieces that build on them.
   State the order in each PR description (e.g. "Merge after #12").
5. **Ship tests with the code.** Tests go in the same PR as the function they
   cover - never in a separate "add tests" PR and never deferred.

## Worked example

Task: "Add a `refund` endpoint that reuses the existing `chargeCard` helper,
which needs a new `amount` parameter."

| Order | PR | Contents |
|-------|----|----------|
| 1 | Extend `chargeCard` with an optional `amount` param | helper change + its tests; existing callers untouched and still passing |
| 2 | Update existing callers to pass `amount` explicitly | caller edits + their tests |
| 3 | Add `refund` handler | new function + its tests |
| 4 | Register the `/refund` route | routing + handler integration test |

Each PR is independently green and the existing charge flow works at every step.

## Checklist before opening each PR

- [ ] The PR changes exactly one function / self-contained piece of logic.
- [ ] Shared-helper and caller edits are not mixed with the main change.
- [ ] Existing flows still pass with only this PR applied on top of its dependencies.
- [ ] Tests for the changed code are included in this PR.
- [ ] The PR description names its dependency PR(s) and merge order.

## Automated merge protocol

`main` is protected: PRs are required, and a PR merges automatically once the
required checks pass (`build-test`, `typecheck-export`, `test`, `e2e`,
`dependency-gate`). Do not merge by hand.

- **Declare dependencies in the body.** Any PR that must land after another PR
  must contain `Depends on: #N` (aliases `Merge after #N`, `Blocked by #N` also
  work). The `dependency-gate` check fails while any declared dependency is
  unmerged. Circular dependencies deadlock the queue - never create them.
- **Branch dependent PRs off their parent's head branch** (stacked). When the
  parent merges, its branch is deleted, GitHub retargets the child to `main`,
  and the child's checks re-run against the new base. A dependent PR based on
  `main` instead still merges in order (the gate holds it), but its diff shows
  the parent's commits until the parent lands.
- **Auto-merge is enabled for you** on every non-draft PR (merge commit; merge
  commits keep stacked diffs clean - do not squash stacked PRs). Nothing else
  is needed after `gh pr create`.
- **Drafts are held**: the auto-merge marker is only set when a draft is marked
  ready for review.
- When a PR merges, `merge-gate.yml`'s cascade re-runs the gate on every open PR
  that depended on it and merges `main` into that branch, so each dependent
  lands with a fresh green run.
- Watch progress with `gh pr checks <n>` / `gh run list`. If the gate is red,
  fix the declared dependencies; if CI is red, fix the code - the queue resumes
  by itself.

## Forbidden

- One PR that adds a helper, updates its callers, and adds the feature that uses it.
- Splitting tests from the code they cover.
- Merging a dependent PR before its dependency.
