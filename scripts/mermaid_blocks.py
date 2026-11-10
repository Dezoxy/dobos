#!/usr/bin/env python3
"""Keep the Mermaid in this repository's Markdown derived, valid and renderable.

Structurizr owns structure; Mermaid draws what the model does not. Two jobs
follow, one subcommand each. Both walk the Markdown set the docs checker scans
(check_docs_consistency.markdown_files) and use its fence scanner, so the
checker and this script can never disagree about what a Mermaid block is.

  sync     A derived block is a Mermaid fence directly under
           `<!-- mermaid-view: Key -->`. Read Structurizr's raw export of view
           Key (generated/mermaid-export/structurizr-<Key>.mmd, from
           `structurizr export -format mermaid`), normalise it, write it to
           generated/mermaid-views/<Key>.mmd and rewrite the block's body to
           match. Nobody edits a derived body: the model does, through this.
           Running it twice changes nothing the second time.

  extract  Write every Mermaid fence in the Markdown to
           generated/mermaid-render/<sha12>.mmd, and index.tsv (hash, file,
           line) to map a hash back to where it came from. render-mermaid.sh
           renders them, which catches the syntax error GitHub would only show
           as an error box on the pushed page.

Normalisation, and why: the export wraps every label in styled <div> elements.
Whether GitHub's Mermaid renders those is unverified, so the derived block uses
plain text lines joined by <br/>. Colours (the `style` lines) and everything
else stay as exported. The transform is a fixed sequence of substitutions, so
the same export always gives the same block.

The canonical copy lives in development-base (scripts/); repositories copy it
unchanged, together with check_docs_consistency.py, which it imports.

Run from anywhere:
    mermaid_blocks.py sync
    mermaid_blocks.py extract
"""

from __future__ import annotations

import argparse
import hashlib
import re
import sys
from collections.abc import Iterable
from pathlib import Path

# The checker sits beside this script; it is not an installed package.
sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_docs_consistency as checker

REPO = checker.REPO
GENERATED = checker.ARCH / "generated"
EXPORT_DIR = GENERATED / "mermaid-export"
# One definition with the checker, which compares derived blocks against it.
VIEWS_DIR = checker.MERMAID_VIEWS
RENDER_DIR = GENERATED / "mermaid-render"
# Structurizr names each file after the view key: structurizr-<Key>.mmd.
EXPORT_PREFIX = "structurizr-"
INDEX = "index.tsv"

# Labels are the double-quoted strings; a raw quote inside one would already
# break the export as Mermaid.
LABEL = re.compile(r'"[^"\n]*"')
DIV_OPEN = re.compile(r"<div\b[^>]*>")
BREAK = re.compile(r"<br\s*/?>")
BREAKS = re.compile(r"(?:<br/>){2,}")


def normalise_label(label: str) -> str:
    """One quoted label as plain lines joined by <br/>.

    `<div style=...>` opens vanish, `</div>` becomes a line break, `<br />`
    loses its space, repeated breaks collapse and the break a closing `</div>`
    leaves before the closing quote goes.
    """
    label = BREAK.sub("<br/>", label)
    label = DIV_OPEN.sub("", label)
    label = label.replace("</div>", "<br/>")
    label = BREAKS.sub("<br/>", label)
    return label.replace('<br/>"', '"')


def normalise(text: str) -> str:
    """A raw Structurizr Mermaid export as GitHub-safe text ending in one newline."""
    return LABEL.sub(lambda m: normalise_label(m.group(0)), text).rstrip("\n") + "\n"


def write_if_changed(path: Path, text: str) -> bool:
    """Write text unless the file already holds it, so a rerun leaves no trace."""
    if path.exists() and path.read_text(encoding="utf-8") == text:
        return False
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    return True


def normalise_exports(export_dir: Path, views_dir: Path) -> dict[str, str]:
    """Normalise every exported view into views_dir/<Key>.mmd; key -> text.

    A file for a view the model no longer has is removed, so the folder always
    mirrors the last export. With no export at all nothing is removed: an empty
    export is a missing step, not a model without views.
    """
    views = {
        path.stem.removeprefix(EXPORT_PREFIX): normalise(
            path.read_text(encoding="utf-8")
        )
        for path in sorted(export_dir.glob(f"{EXPORT_PREFIX}*.mmd"))
    }
    for key, text in views.items():
        write_if_changed(views_dir / f"{key}.mmd", text)
    if views:
        for old in views_dir.glob("*.mmd"):
            if old.stem not in views:
                old.unlink()
    return views


def sync(export_dir: Path, views_dir: Path, files: Iterable[Path], root: Path) -> int:
    """Normalise the export and rewrite every derived block; the exit status.

    All keys are checked before any file is written, so a typo in one README
    cannot leave the others half updated.
    """
    views = normalise_exports(export_dir, views_dir)
    pending = []  # (path, text, [(fence, key)]) for files with derived blocks
    missing = []
    for path in sorted(files):
        text = path.read_text(encoding="utf-8")
        blocks, _ = checker.mermaid_fences(text)
        derived = [(f, key) for f, key in blocks if key and f.end is not None]
        if not derived:
            continue
        pending.append((path, text, derived))
        for fence, key in derived:
            if key not in views:
                missing.append(
                    f"{export_dir / f'{EXPORT_PREFIX}{key}.mmd'} is missing: "
                    f"{path.relative_to(root).as_posix()}:{fence.start - 1} "
                    f"derives view '{key}' (is it a view key in views.dsl?)"
                )
    if missing:
        print("\n".join(missing), file=sys.stderr)
        print("run make mermaid-views to export the views first", file=sys.stderr)
        return 1
    updated = blocks_changed = 0
    for path, text, derived in pending:
        lines = text.split("\n")
        # Back to front: a longer or shorter body moves every line after it.
        for fence, key in reversed(derived):
            body = views[key].rstrip("\n").split("\n")
            if lines[fence.start : fence.end - 1] != body:
                lines[fence.start : fence.end - 1] = body
                blocks_changed += 1
        if write_if_changed(path, "\n".join(lines)):
            updated += 1
            print(f"  updated {path.relative_to(root).as_posix()}")
    print(
        f"mermaid-views: {len(views)} views in {views_dir.relative_to(root)}, "
        f"{blocks_changed} derived blocks rewritten in {updated} files"
    )
    return 0


def extract(files: Iterable[Path], out_dir: Path, root: Path) -> int:
    """Write every closed Mermaid fence to out_dir/<sha12>.mmd, plus the index.

    The folder is emptied first: a diagram deleted from the Markdown must stop
    being rendered, and a fixed one must stop failing. The unclosed fences the
    checker reports are skipped here.
    """
    out_dir.mkdir(parents=True, exist_ok=True)
    for old in [*out_dir.glob("*.mmd"), *out_dir.glob("*.png"), out_dir / INDEX]:
        old.unlink(missing_ok=True)
    rows = []
    for path in sorted(files):
        blocks, _ = checker.mermaid_fences(path.read_text(encoding="utf-8"))
        for fence, _key in blocks:
            if fence.end is None:
                continue
            source = "\n".join(fence.body) + "\n"
            digest = hashlib.sha256(source.encode("utf-8")).hexdigest()[:12]
            write_if_changed(out_dir / f"{digest}.mmd", source)
            rows.append(f"{digest}\t{path.relative_to(root).as_posix()}\t{fence.start}")
    (out_dir / INDEX).write_text("".join(f"{row}\n" for row in rows), encoding="utf-8")
    distinct = len({row.split("\t")[0] for row in rows})
    print(
        f"mermaid-render: {len(rows)} Mermaid blocks ({distinct} distinct) "
        f"extracted to {out_dir.relative_to(root)}"
    )
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("sync", help="rewrite derived blocks from the Mermaid export")
    commands.add_parser("extract", help="write every Mermaid fence for rendering")
    args = parser.parse_args(argv)
    files = checker.markdown_files()
    if args.command == "sync":
        return sync(EXPORT_DIR, VIEWS_DIR, files, REPO)
    return extract(files, RENDER_DIR, REPO)


if __name__ == "__main__":
    sys.exit(main())
