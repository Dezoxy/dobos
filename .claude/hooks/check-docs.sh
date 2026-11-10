#!/usr/bin/env bash
# PostToolUse(Edit|Write): reminders that keep the documentation gates green.
set -euo pipefail
input="$(cat)"
f="$(printf '%s' "$input" | jq -r '.tool_input.file_path // .tool_response.filePath // empty' 2>/dev/null || true)"
[ -n "$f" ] || exit 0
msg=""
case "$f" in
  */docs/architecture/model/*.dsl|*/docs/architecture/workspace.dsl)
    msg="Model changed: run 'make check' (validate + inspect) and 'make mermaid-views' (derived Mermaid blocks), and update the view register in docs/architecture/README.md if a view key changed." ;;
  */docs/architecture/decisions/*.md)
    msg="ADR touched: first line '# N. Title', a 'Date: YYYY-MM-DD' line, '## Status' then '## Context'; list it in docs/architecture/README.md. A changed decision gets a new ADR, not an edit." ;;
  */.claude/skills/*/SKILL.md)
    msg="Skill changed: copy it byte-for-byte to .agents/skills/<name>/SKILL.md; the .claude copy is the source." ;;
  */CLAUDE.md)
    msg="CLAUDE.md changed: run 'cp CLAUDE.md AGENTS.md'; the twins must stay byte-identical." ;;
  */AGENTS.md)
    msg="AGENTS.md changed: edit CLAUDE.md instead and copy it over; the twins must stay byte-identical." ;;
  */docs/architecture/overview/*.md)
    msg="Overview page: title with '##', subsections with '###'; keep each embed on one line; run 'make docs'." ;;
  */docs/*.md|*/README.md)
    msg="Documentation changed: run 'make docs' before the PR (80-column prose, links, indexes, IDs)." ;;
  *) exit 0 ;;
esac
jq -nc --arg c "$msg" \
  '{hookSpecificOutput:{hookEventName:"PostToolUse",additionalContext:$c}}'
exit 0
