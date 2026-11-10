#!/usr/bin/env bash
# Regression test for .claude/hooks/guard-bash.sh. Each line of
# guard-bash-cases.jsonl carries a sample command and the expected decision
# (deny, ask or none). Run: bash tests/test_guard_bash.sh
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
hook="$here/../.claude/hooks/guard-bash.sh"
fail=0
n=0
while IFS= read -r line; do
  [ -z "$line" ] && continue
  n=$((n + 1))
  expect="$(jq -r '.expect' <<<"$line")"
  got="$(jq -c '{tool_input:{command:.command}}' <<<"$line" | bash "$hook" \
    | jq -r '.hookSpecificOutput.permissionDecision // "none"')"
  [ -z "$got" ] && got=none
  if [ "$got" != "$expect" ]; then
    echo "FAIL case $n: expected $expect, got $got"
    fail=1
  else
    echo "ok   case $n: $expect"
  fi
done < "$here/guard-bash-cases.jsonl"
exit "$fail"
