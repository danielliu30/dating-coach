#!/usr/bin/env bash
# Shared dependency-declaration parsing for the merge gate.
# A PR body declares ordering with lines like:
#   Depends on: #12        (aliases: Merge after #N, Blocked by #N)
#   Depends on: #1, #2 and #3
# Anything else mentioning #N (e.g. "fixes #9") is NOT a dependency.

# Reads a PR body on stdin, prints the unique dependency PR numbers on stdout.
extract_dep_numbers() {
  grep -oEi '(depends on|merge after|blocked by)[:[:space:]]*(#[0-9]+[,[:space:]]*(and[[:space:]]+)?)+' \
    | grep -oE '#[0-9]+' | grep -oE '[0-9]+' | sort -u
}
