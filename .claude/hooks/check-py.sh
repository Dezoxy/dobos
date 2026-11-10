#!/usr/bin/env bash
# PostToolUse(Edit|Write): advisory ruff lint and format check on the edited
# Python file. Injects findings back to the model; does not block.
set -euo pipefail
input="$(cat)"
f="$(printf '%s' "$input" | jq -r '.tool_input.file_path // .tool_response.filePath // empty' 2>/dev/null || true)"
case "$f" in *.py) ;; *) exit 0 ;; esac
[ -f "$f" ] || exit 0
if command -v ruff >/dev/null 2>&1; then RUFF=(ruff)
elif command -v uvx >/dev/null 2>&1; then RUFF=(uvx --quiet ruff)
else exit 0; fi
out=""
lint="$("${RUFF[@]}" check --quiet --output-format concise "$f" 2>&1 || true)"
[ -n "$lint" ] && out="ruff check:\n${lint}\n"
"${RUFF[@]}" format --check --quiet "$f" >/dev/null 2>&1 || \
  out="${out}ruff format would reformat ${f}; run 'ruff format ${f}'.\n"
[ -z "$out" ] && exit 0
jq -nc --arg c "$(printf '%b' "$out")" \
  '{hookSpecificOutput:{hookEventName:"PostToolUse",additionalContext:$c}}'
exit 0
