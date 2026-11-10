#!/usr/bin/env bash
# PostToolUse(Edit|Write): advisory checks for Terraform, Helm charts and
# GitHub Actions workflows. Injects findings; does not block.
set -euo pipefail
input="$(cat)"
f="$(printf '%s' "$input" | jq -r '.tool_input.file_path // .tool_response.filePath // empty' 2>/dev/null || true)"
[ -n "$f" ] || exit 0
out=""
case "$f" in
  *.tf|*.tfvars)
    if command -v terraform >/dev/null 2>&1; then
      d="$(dirname "$f")"
      terraform -chdir="$d" fmt -check >/dev/null 2>&1 || \
        out="terraform fmt would reformat ${d}; run 'terraform -chdir=${d} fmt'."
    fi
  ;;
  */helm/*|*/charts/*)
    chart="$(dirname "$f")"
    while [ "$chart" != "/" ] && [ ! -f "$chart/Chart.yaml" ]; do chart="$(dirname "$chart")"; done
    if [ -f "$chart/Chart.yaml" ] && command -v helm >/dev/null 2>&1; then
      lint="$(helm lint "$chart" 2>&1 | grep -E '\[(ERROR|WARNING)\]' || true)"
      [ -n "$lint" ] && out="helm lint ${chart}:\n${lint}"
    fi
  ;;
  */.github/workflows/*.yml|*/.github/workflows/*.yaml)
    if command -v actionlint >/dev/null 2>&1; then
      lint="$(actionlint "$f" 2>&1 || true)"
      [ -n "$lint" ] && out="actionlint:\n${lint}"
    fi
  ;;
  *) exit 0 ;;
esac
[ -z "$out" ] && exit 0
jq -nc --arg c "$(printf '%b' "$out")" \
  '{hookSpecificOutput:{hookEventName:"PostToolUse",additionalContext:$c}}'
exit 0
