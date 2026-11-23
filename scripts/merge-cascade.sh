#!/usr/bin/env bash
# Runs after PR $1 merges: for every open PR that declared it as a dependency,
# merge the new base into the branch (fresh full check suite via `synchronize`),
# and if the branch is already up to date (no merge possible), re-evaluate the
# dependency gate directly and post the `dependency-gate` check run onto the
# dependent's head SHA so its required check stops showing the stale failure.
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/pr-deps.sh"

MERGED=${1:?usage: merge-cascade.sh <merged-pr-number>}
: "${GITHUB_REPOSITORY:?}"

gh pr list --state open --json number,body --limit 200 \
  --jq '.[] | "\(.number)\t\(.body // "" | gsub("\n"; " "))"' \
| while IFS=$'\t' read -r n body; do
    [ -n "$n" ] || continue
    printf '%s' "$body" | extract_dep_numbers | grep -qx "$MERGED" || continue

    echo "PR #$n declared a dependency on #$MERGED"
    if gh api -X PUT "repos/$GITHUB_REPOSITORY/pulls/$n/update-branch" >/dev/null 2>&1; then
      echo "  merged base into head; full check suite will re-run on the new commit"
      continue
    fi

    # update-branch was a no-op (already up to date / stacked retarget pending):
    # refresh the gate status on the current head ourselves.
    if bash "$HERE/dependency-gate.sh" "$n"; then c=success; else c=failure; fi
    sha=$(gh pr view "$n" --json headRefOid --jq .headRefOid)
    echo "  posting dependency-gate=$c onto $sha"
    gh api -X POST "repos/$GITHUB_REPOSITORY/check-runs" \
      -f name=dependency-gate -f head_sha="$sha" -f status=completed \
      -f conclusion="$c" \
      -f "output[title]=dependency gate" \
      -f "output[summary]=re-evaluated after #$MERGED merged" || true
  done
