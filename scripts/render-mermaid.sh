#!/usr/bin/env bash
# Render every Mermaid file in a folder to a PNG beside it.
#
#   render-mermaid.sh <dir>
#
# Each <dir>/<name>.mmd becomes <dir>/<name>.png (scale 2, white background)
# with the pinned Mermaid CLI image. `make mermaid-render` uses it to prove
# that every Mermaid block in the Markdown parses, which GitHub would otherwise
# only show as an error box on the pushed page; architecture-pdf.sh uses it to
# make the images Pandoc needs.
#
# One container per file, so a failure names its file. Every file is tried and
# the exit status is 1 if any failed. A failure prints the file and, when
# <dir>/index.tsv (hash, Markdown file, line; written by
# `mermaid_blocks.py extract`) exists, where the diagram came from, then the
# renderer's own message. A folder with no .mmd files is not an error.
#
#   MERMAID_IMAGE  required: the pinned Mermaid CLI image, e.g.
#                  minlag/mermaid-cli:12.0.1 (~630 MB). Keep it identical to
#                  the Makefile's pin.
#
# The container runs as you, so the folder needs no chmod and the PNGs are
# yours. HOME=/tmp because the browser inside keeps a cache there and the user
# has no home directory.
set -euo pipefail

: "${MERMAID_IMAGE:?Set MERMAID_IMAGE to your pinned Mermaid CLI image}"
[ -d "${1:-}" ] || { echo "usage: render-mermaid.sh <dir>" >&2; exit 2; }
command -v docker >/dev/null || { echo "render-mermaid: docker not found" >&2; exit 1; }
dir="$(cd "$1" && pwd)"

shopt -s nullglob
files=("${dir}"/*.mmd)
if [ "${#files[@]}" -eq 0 ]; then
  echo "render-mermaid: no .mmd files in $1, nothing to render"
  exit 0
fi

log="$(mktemp)"
trap 'rm -f "${log}"' EXIT
failed=0
for src in "${files[@]}"; do
  name="$(basename "${src}" .mmd)"
  # A PNG from an earlier run must not pass for this run's result.
  rm -f "${dir}/${name}.png"
  if ! docker run --rm -u "$(id -u):$(id -g)" -e HOME=/tmp -v "${dir}:/data" \
    "${MERMAID_IMAGE}" -i "/data/${name}.mmd" -o "/data/${name}.png" -s 2 -b white \
    >"${log}" 2>&1; then
    failed=$((failed + 1))
    echo "render-mermaid: FAILED ${src}" >&2
    if [ -f "${dir}/index.tsv" ]; then
      awk -F'\t' -v hash="${name}" '$1 == hash { printf "  from %s:%s\n", $2, $3 }' \
        "${dir}/index.tsv" >&2
    fi
    # The parse error is the first lines; the Node stack trace below is noise.
    { grep -Ev '^[[:space:]]+at |^Parser\.parseError' "${log}" || true; } | sed 's/^/  /' >&2
  fi
done

if [ "${failed}" -gt 0 ]; then
  echo "render-mermaid: ${failed} of ${#files[@]} diagram(s) failed" >&2
  exit 1
fi
echo "render-mermaid: rendered ${#files[@]} Mermaid diagram(s) in $1"
