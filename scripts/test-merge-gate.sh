#!/usr/bin/env bash
# Offline test harness for the merge-gate scripts. Stubs `gh` on PATH and drives
# dependency-gate.sh / merge-cascade.sh / pr-deps.sh through fixture bodies.
# Run: bash scripts/test-merge-gate.sh   (also runs in CI as `merge-gate-tests`)
set -u
ROOT=$(cd "$(dirname "$0")/.." && pwd)
FIX=$(mktemp -d)
trap 'rm -rf "$FIX"' EXIT
export FIXTURE_DIR="$FIX"
export GITHUB_REPOSITORY="o/r"

cat > "$FIX/gh" <<'EOF'
#!/usr/bin/env bash
# Minimal gh stub for the merge-gate scripts.
if [ "$1 $2" = "pr view" ]; then
  n=$3; field=""
  while [ $# -gt 0 ]; do [ "$1" = "--json" ] && { field=$2; break; }; shift; done
  f="$FIXTURE_DIR/$field-$n"
  [ -f "$f" ] && cat "$f" || exit 1
elif [ "$1 $2" = "pr list" ]; then
  cat "$FIXTURE_DIR/pr-list.tsv"
elif [ "$1" = "workflow" ]; then
  echo "$*" >> "$FIXTURE_DIR/workflow-calls"
elif [ "$1" = "api" ]; then
  case "$*" in
    *check-runs*) echo "$*" >> "$FIXTURE_DIR/check-runs" ;;
    *update-branch*)
      echo "$*" >> "$FIXTURE_DIR/api-calls"
      [ -f "$FIXTURE_DIR/api-fail" ] && exit 1
      exit 0 ;;
    *) echo "$*" >> "$FIXTURE_DIR/api-calls" ;;
  esac
fi
EOF
chmod +x "$FIX/gh"
export PATH="$FIX:$PATH"

pass=0; fail=0
t() { # t <name> <want-exit> <cmd...>
  local name=$1 want=$2; shift 2
  "$@" >/dev/null 2>&1; local got=$?
  if [ "$want" = "$got" ]; then pass=$((pass+1)); printf 'ok    %s\n' "$name"
  else fail=$((fail+1)); printf 'FAIL  %s (want %s, got %s)\n' "$name" "$want" "$got"; fi
}
body()  { printf '%s' "$2" > "$FIX/body-$1"; }
state() { printf '%s' "$2" > "$FIX/mergedAt-$1"; }
sha()   { printf '%s' "$2" > "$FIX/headRefOid-$1"; }
GATE() { bash "$ROOT/scripts/dependency-gate.sh" "$1"; }
DEPS() { printf '%s' "$1" | bash -c '. '"$ROOT"'/scripts/pr-deps.sh; extract_dep_numbers' | tr '\n' ' '; }

# Fixture universe: #1 open, #2 merged, #9 unresolvable, #50 merged.
state 1 open; state 2 merged; state 50 merged

# --- dependency-gate.sh behaviour ---
body 100 'plain change, no deps';                                    t 'no deps passes'                 0 GATE 100
body 101 'Depends on: #1';                                           t 'open dep blocks'                1 GATE 101
body 102 'Depends on: #2';                                           t 'merged dep passes'              0 GATE 102
body 103 'Depends on: #9';                                           t 'unresolvable dep fails closed'  1 GATE 103
body 104 'Depends on: #104';                                         t 'self-reference ignored'         0 GATE 104
body 105 'Depends on: #1, #2';                                       t 'list with open dep blocks'      1 GATE 105
body 106 'Merge after #2 and #1';                                    t 'alias + and-list blocks'        1 GATE 106
body 107 'Blocked by #2';                                            t 'blocked-by alias passes'        0 GATE 107
body 108 'See #1 for context';                                       t 'bare mention is not a dep'      0 GATE 108
body 109 'DEPENDS ON #2';                                            t 'case-insensitive'               0 GATE 109
body 110 'Depends on:#2';                                            t 'no space after colon'           0 GATE 110
body 111 'Depends on: #2, #1';                                       t 'merged+open list blocks'        1 GATE 111
body 112 'Depends on #2. Also see #1';                               t 'dep span stops at sentence'     0 GATE 112
body 113 'Depends on: #1

Depends on: #2';                                                        t 'one dep per line blocks'        1 GATE 113
body 115 'depends on #2';                                            t 'lowercase no-colon'             0 GATE 115
# 116 has no body fixture: gh fails -> vacuous pass (PR is gone; nothing to block on)
t 'vanished PR passes vacuously'                                     0 GATE 116

# --- extract_dep_numbers edge cases ---
[ "$(DEPS 'Depends on: #5,#7')" = "5 7 " ] && { pass=$((pass+1)); echo 'ok    comma list no space'; } || { fail=$((fail+1)); echo "FAIL  comma list no space -> $(DEPS 'Depends on: #5,#7')"; }
[ "$(DEPS 'Depends on: #12 and #34 and #56')" = "12 34 56 " ] && { pass=$((pass+1)); echo 'ok    chained and list'; } || { fail=$((fail+1)); echo 'FAIL  chained and list'; }
[ "$(DEPS 'Merge after #3. Depends on #4')" = "3 4 " ] && { pass=$((pass+1)); echo 'ok    two spans same line'; } || { fail=$((fail+1)); echo 'FAIL  two spans same line'; }
[ "$(DEPS 'Depends on #1234')" = "1234 " ] && { pass=$((pass+1)); echo 'ok    multi-digit number'; } || { fail=$((fail+1)); echo 'FAIL  multi-digit number'; }
[ -z "$(DEPS '')" ] && { pass=$((pass+1)); echo 'ok    empty body'; } || { fail=$((fail+1)); echo 'FAIL  empty body'; }
[ -z "$(DEPS 'fixes #1, closes #2')" ] && { pass=$((pass+1)); echo 'ok    non-dep keywords ignored'; } || { fail=$((fail+1)); echo 'FAIL  non-dep keywords ignored'; }

# --- merge-cascade.sh ---
# Open PRs; 200/201/204 depend on the just-merged #50, 202/203 do not.
cat > "$FIX/pr-list.tsv" <<EOF
200	Depends on: #50
201	Depends on: #51 and #50
202	an unrelated PR
203	Depends on: #5
204	Merge after #50, #52
EOF
body 200 'Depends on: #50';      sha 200 sha-of-200
body 201 'Depends on: #51 and #50'; sha 201 sha-of-201
body 204 'Merge after #50, #52';    sha 204 sha-of-204
state 50 merged; state 51 open; state 52 open; state 5 open

# happy path: update-branch succeeds -> no manual check-run posts needed
: > "$FIX/api-calls"; : > "$FIX/check-runs"; rm -f "$FIX/api-fail"
t 'cascade exits 0'                 0 bash "$ROOT/scripts/merge-cascade.sh" 50
grep -q 'pulls/200/update-branch' "$FIX/api-calls" && grep -q 'pulls/201/update-branch' "$FIX/api-calls" && grep -q 'pulls/204/update-branch' "$FIX/api-calls" \
  && ! grep -q 'pulls/202/' "$FIX/api-calls" && ! grep -q 'pulls/203/' "$FIX/api-calls" \
  && { pass=$((pass+1)); echo 'ok    cascade updates dependent branches only'; } || { fail=$((fail+1)); echo "FAIL  cascade updates dependent branches only: $(cat "$FIX/api-calls")"; }
[ ! -s "$FIX/check-runs" ] && { pass=$((pass+1)); echo 'ok    no manual posts when update-branch works'; } || { fail=$((fail+1)); echo 'FAIL  no manual posts when update-branch works'; }

# fallback path: update-branch is a no-op -> gate re-evaluated + posted onto head
: > "$FIX/api-calls"; : > "$FIX/check-runs"; : > "$FIX/api-fail"
bash "$ROOT/scripts/merge-cascade.sh" 50 >/dev/null 2>&1
grep -q 'head_sha=sha-of-200' "$FIX/check-runs" && grep -q 'conclusion=success' "$FIX/check-runs" \
  && { pass=$((pass+1)); echo 'ok    fallback posts success for cleared dep'; } || { fail=$((fail+1)); echo "FAIL  fallback posts success for cleared dep: $(cat "$FIX/check-runs")"; }
grep 'head_sha=sha-of-201' "$FIX/check-runs" | grep -q 'conclusion=failure' \
  && { pass=$((pass+1)); echo 'ok    fallback posts failure for still-blocked dep'; } || { fail=$((fail+1)); echo "FAIL  fallback posts failure for still-blocked dep: $(cat "$FIX/check-runs")"; }
rm -f "$FIX/api-fail"

# cascading on a PR nobody depends on is a no-op
: > "$FIX/api-calls"; : > "$FIX/check-runs"
bash "$ROOT/scripts/merge-cascade.sh" 999 >/dev/null 2>&1
[ ! -s "$FIX/api-calls" ] && [ ! -s "$FIX/check-runs" ] && { pass=$((pass+1)); echo 'ok    no dependents -> nothing posted'; } || { fail=$((fail+1)); echo 'FAIL  no dependents -> nothing posted'; }

echo "---"
echo "$pass passed, $fail failed"
[ "$fail" = 0 ]
