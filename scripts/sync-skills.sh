#!/usr/bin/env bash
# sync-skills.sh — keep .agents/skills in lockstep across the dating-coach
# family of repos.
#
# The app repo (this checkout) is canonical. Sibling repos get a verbatim
# copy of .agents/skills plus the AGENTS.md "Workflow skills" section that
# declares them.
#
# Usage:
#   scripts/sync-skills.sh [--check] [target-dir ...]
#
#   --check   report drift without writing; exit 1 if any target differs
#   targets   sibling checkouts to sync; default is the usual sibling layout
#             ../dating-coach-{loadtest,pentest,infra} relative to repo root
#
# Env:
#   SKILL_TARGETS  space-separated target dirs (overrides defaults)
#
# CI (.github/workflows/skills-sync.yml) clones each sibling, runs this
# script against the clones, and pushes when the tree changes.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/.agents/skills"

MODE="sync"
TARGETS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --check) MODE="check"; shift ;;
    *) TARGETS+=("$1"); shift ;;
  esac
done

if [ ${#TARGETS[@]} -eq 0 ]; then
  if [ -n "${SKILL_TARGETS:-}" ]; then
    # shellcheck disable=SC2206
    TARGETS=($SKILL_TARGETS)
  else
    TARGETS=(
      "$ROOT/../dating-coach-loadtest"
      "$ROOT/../dating-coach-pentest"
      "$ROOT/../dating-coach-infra"
    )
  fi
fi

[ -d "$SRC" ] || { echo "no canonical skills at $SRC" >&2; exit 1; }

status=0
for t in "${TARGETS[@]}"; do
  t="$(cd "$t" 2>/dev/null && pwd)" || { echo "skip $t (not a directory)"; continue; }
  dest="$t/.agents/skills"

  if [ "$MODE" = "check" ]; then
    if [ ! -d "$dest" ]; then
      echo "DRIFT $t: missing $dest"
      status=1
      continue
    fi
    if ! diff -r -q "$SRC" "$dest" >/dev/null 2>&1; then
      echo "DRIFT $t:"
      diff -r -q "$SRC" "$dest" | sed 's/^/  /' || true
      status=1
    else
      echo "OK    $t"
    fi
    continue
  fi

  mkdir -p "$t/.agents"
  rm -rf "$dest"
  cp -r "$SRC" "$dest"
  changed="$(git -C "$t" status --porcelain -- .agents/skills 2>/dev/null | wc -l | tr -d ' ')"
  echo "SYNC  $t ($changed changed path(s) under .agents/skills)"
done

exit $status
