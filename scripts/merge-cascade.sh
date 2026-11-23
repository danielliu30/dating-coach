#!/usr/bin/env bash
# Runs after PR $1 merges; keeps the merge train moving.
#
# Required checks run with `strict: true` (branch must be up to date with main),
# so every merge makes every other open non-draft PR out of date. For each one
# we merge the new base into its head, which fires `synchronize` and re-runs the
# full check suite against the code it will actually merge into.
#
# Open PRs that declared $1 as a dependency additionally get `dependency-gate`
# re-evaluated and posted onto their head SHA when update-branch is a no-op
# (e.g. already up to date), so the required check never stays stale.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/pr-deps.sh"

MERGED=${1:?usage: merge-cascade.sh <merged-pr-number>}
: "${GITHUB_REPOSITORY:?}"

gh pr list --state open --json number,isDraft,body --limit 200 \
  --jq '.[] | "\(.number)\t\(.isDraft)\t\(.body // "" | gsub("\n"; " "))"' \
| while IFS=$'\t' read -r n draft body; do
    [ -n "$n" ] || continue
    dependent=$(printf '%s' "$body" | extract_dep_numbers | grep -qx "$MERGED" && echo yes || echo no)
    # Drafts stay out of the train unless they are declared dependents; they
    # are refreshed when auto-merge is armed at ready_for_review instead.
    [ "$draft" = "true" ] && [ "$dependent" = no ] && continue

    if gh api -X PUT "repos/$GITHUB_REPOSITORY/pulls/$n/update-branch" >/dev/null 2>&1; then
      echo "PR #$n: merged base into head; full check suite re-runs on the new commit"
      continue
    fi
    [ "$dependent" = yes ] || continue

    # update-branch was a no-op: refresh the gate status on the current head.
    if bash "$HERE/dependency-gate.sh" "$n"; then c=success; else c=failure; fi
    sha=$(gh pr view "$n" --json headRefOid --jq .headRefOid)
    echo "PR #$n: posting dependency-gate=$c onto $sha"
    gh api -X POST "repos/$GITHUB_REPOSITORY/check-runs" \
      -f name=dependency-gate -f head_sha="$sha" -f status=completed \
      -f conclusion="$c" \
      -f "output[title]=dependency gate" \
      -f "output[summary]=re-evaluated after #$MERGED merged" || true
  done
