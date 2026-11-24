---
name: dev-cycle
description: Full development-review cycle for a code task - break large prompts into smaller work items, implement minimally and in-scope only, verify, self-review, fix, repeat until clean, then drive the PR(s) through CI and review comments to merge.
allowed-tools:
  - read
  - edit
  - grep
  - glob
  - exec
---

# Development-review cycle

Run this loop for any code-change task. Each phase delegates to its own
skill - this skill is the sequence, not the details.

## Loop

1. **Decompose.** Break a large prompt into smaller work items; sequence
   their PRs per `pr-granularity`.
2. **Implement** the current item per `coding-efficiency`.
3. **Verify** per `dating-coach-verify`. Red -> fix -> re-run until green.
4. **Self-review** the diff with `review`; act on its findings until clean.
5. **Open the PR.** `gh pr create`; the body declares `Depends on: #N` for
   stacks. Auto-merge arms itself.
6. **Drive it to green.** `gh pr checks <n>`; CI red -> fix -> push -> repeat.
   Address every review comment (`gh pr view <n> --comments`, including
   automated reviewers like Devin Review) -> push -> re-check.
7. **Merge.** Let the queue merge on green (`pr-granularity` owns the
   protocol); a stack's next PR retargets automatically - continue the loop
   on the next item.
8. **Report.** PRs shipped, verification run, residual minors/nits.

## Exit conditions

Stop and report rather than looping forever: blocked on a decision or
credential, a required check failing for environmental reasons, or scope
ambiguity after one honest attempt. Pause for confirmation before destructive
operations.
