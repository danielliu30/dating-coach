#!/usr/bin/env bash
# Required `dependency-gate` check: exit 1 while PR $1 declares dependencies
# that are not yet merged. A dependency that cannot be resolved (deleted PR,
# issue-only number, permission error) blocks the merge - fail closed.
set -u
cd "$(dirname "$0")" && . ./pr-deps.sh

PR=${1:?usage: dependency-gate.sh <pr-number>}
body=$(gh pr view "$PR" --json body --jq '.body // ""' 2>/dev/null || true)
deps=$(printf '%s' "$body" | extract_dep_numbers)

if [ -z "$deps" ]; then
  echo "No dependencies declared on PR #$PR"
  exit 0
fi

fail=0
for d in $deps; do
  if [ "$d" = "$PR" ]; then
    echo "Ignoring self-reference #$d"
    continue
  fi
  merged=$(gh pr view "$d" --json mergedAt --jq 'if .mergedAt then "merged" else "open" end' 2>/dev/null || echo "unresolvable")
  echo "dependency #$d: $merged"
  [ "$merged" = "merged" ] || fail=1
done

if [ "$fail" != 0 ]; then
  echo "::error::PR #$PR has unmerged dependencies; auto-merge stays blocked until they land."
  exit 1
fi
