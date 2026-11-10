#!/usr/bin/env python3
"""Fail when documentation contradicts the tree it documents.

The mechanical half of the /docs-sync audit (.claude/skills/docs-sync/SKILL.md).
It only checks claims derivable from the repo: mirrored files, resolvable
links, indexes, ADR format, the view register, cross-referenced IDs and Mermaid
blocks. A green run means "nothing provably false", not "docs are good".

Every check whose subject is missing is skipped, not failed, so a repository
adopts them as it grows: no docs index, no view register, no speaker notes and
no requirement documents still passes. What exists must be consistent.

The canonical copy lives in development-base (scripts/); repositories copy it
unchanged.

Run from anywhere: python3 scripts/check_docs_consistency.py
"""

from __future__ import annotations

import os
import re
import sys
from pathlib import Path
from typing import NamedTuple

REPO = Path(__file__).resolve().parents[1]
ARCH = REPO / "docs" / "architecture"
ADR_DIR = ARCH / "decisions"
ARCH_INDEX = ARCH / "README.md"
DOCS_INDEX = REPO / "docs" / "README.md"
VIEWS_DSL = ARCH / "model" / "views.dsl"
# `make mermaid-views` writes each view here as normalised Mermaid; a derived
# block in a README must equal its file (check_mermaid).
MERMAID_VIEWS = ARCH / "generated" / "mermaid-views"

# Generated output and templates with example links are not documentation.
# `worktrees` is where Claude Code puts session-local agent checkouts; it holds
# whole copies of the repository, so scanning it double-reports the real tree.
SKIP_DIRS = {"generated", "templates", "node_modules", "vendor", ".git", "worktrees"}
# Vendored rule sets (for example ECC's) keep upstream's relative links to
# directories installed globally, not in this repo. They are not documentation.
VENDORED = REPO / ".claude" / "rules"

# Cited ID pattern -> the file that must define it (as a table row or heading).
ID_OWNERS = {
    r"\bC-\d{2}\b": ARCH / "requirements" / "constraints.md",
    r"\bQA-\d{2}\b": ARCH / "requirements" / "quality-attributes.md",
    r"\bA-\d{2}\b": ARCH / "requirements" / "assumptions.md",
    r"\bP-\d{2}\b": ARCH / "principles" / "architecture-principles.md",
    r"\bRISK-\d{3}\b": ARCH / "risks" / "architecture-risks.md",
    r"\bTD-\d{3}\b": ARCH / "risks" / "technical-debt.md",
    r"\bT-\d{2}\b": ARCH / "security" / "threat-model.md",
}

ADR_NAME = re.compile(r"^(\d{4})-[a-z0-9]+(?:-[a-z0-9]+)*\.md$")
ADR_STATUSES = {"Proposed", "Accepted", "Rejected", "Deprecated", "Superseded"}
LINK_RE = re.compile(r"\[([^\]]+)\]\(([^)\s]+)\)")
# Documents imported into Structurizr's Documentation tab cannot use relative
# links: the tab renders them outside the repository tree, so `../risks/x.md`
# resolves to nothing for the reader. They link by absolute URL instead, which
# check_links() skips along with every other http(s) link. Set this to the
# repository's own "owner/name" and those links are checked on disk again.
# Leave it None and the check is skipped.
SELF_REPO: str | None = None
INLINE_CODE = re.compile(r"`[^`]*`")
PROSE_WIDTH = 80
# Copied skills stay byte-identical to their canonical source, so a consuming
# repository does not get to rewrap them.
SKILL_DIRS = (REPO / ".claude" / "skills", REPO / ".agents" / "skills")
# Agent and command definitions are prompts, and copies from an agent kit keep
# their upstream line lengths, so they are exempt like skills.
AGENT_DIRS = (REPO / ".claude" / "agents", REPO / ".claude" / "commands")
OVERVIEW = ARCH / "overview"
# overview/ IS the imported folder, so its own files need no pointer. ADRs are
# imported by `!adrs`, not `!docs`, and reach the document by their own route.
NOT_SYMLINKED = {"overview", "decisions"}
FENCE = re.compile(r"^\s*(```|~~~)")
# A view definition in views.dsl; group 1 is its key.
VIEW_DEFINITION = re.compile(
    r"^(?:systemLandscape|systemContext\s+\S+|container\s+\S+|component\s+\S+|"
    r'dynamic\s+\S+|deployment\s+\S+\s+\S+)\s+"([^"]+)"',
    re.MULTILINE,
)


class Failures(list):
    def add(self, check: str, detail: str) -> None:
        self.append((check, detail))


def read(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def rel(path: Path) -> str:
    return path.relative_to(REPO).as_posix()


def prose(text: str) -> str:
    """The text outside fenced blocks: a sample ADR or risk is an example, not a claim."""
    out, fenced = [], False
    for line in text.splitlines():
        if FENCE.match(line):
            fenced = not fenced
        elif not fenced:
            out.append(line)
    return "\n".join(out)


def markdown_files() -> list[Path]:
    """Every document this repository authors, wherever it sits.

    Three roots hold documentation wholesale -- docs/, .claude/, .agents/ --
    and the instruction files sit at the top. Everything else that documents a
    part of the tree does so as a README.md beside the thing it describes: a
    Terraform module, a service, a package. Those go stale exactly like the
    rest, and used to be unreachable by every check here, so they are collected
    too.

    That last pass skips dot-directories. They hold caches and vendored
    downloads -- .terraform/ ships a README with every provider it unpacks --
    and those documents belong to somebody else. The two dot-directories this
    repository does own are named as roots above, so nothing is lost.
    """
    roots = [REPO / "docs", REPO / ".claude", REPO / ".agents"]
    files = [REPO / p for p in ("README.md", "AGENTS.md", "CLAUDE.md")]
    for root in roots:
        files += [
            p
            for p in root.rglob("*.md")
            if not SKIP_DIRS.intersection(p.relative_to(REPO).parts)
            and VENDORED not in p.parents
            # A symlink into overview/ is an alias for a document that already
            # appears under its own folder. Counting it twice would demand its
            # own index entry and report every finding in it twice.
            and not p.is_symlink()
        ]
    collected = {p.resolve() for p in files}
    files += [
        p
        for p in REPO.rglob("README.md")
        if p.resolve() not in collected
        and not SKIP_DIRS.intersection(p.relative_to(REPO).parts)
        and not any(part.startswith(".") for part in p.relative_to(REPO).parts)
        and not p.is_symlink()
    ]
    return [p for p in files if p.exists()]


def check_twins(f: Failures) -> None:
    """AGENTS.md and CLAUDE.md carry the same instructions, byte for byte."""
    twins = [REPO / "AGENTS.md", REPO / "CLAUDE.md"]
    present = [p for p in twins if p.exists()]
    if not present:
        return  # a repository on its first day: no subject yet
    if len(present) == 1:
        missing = next(p for p in twins if p not in present)
        f.add(
            "twins",
            f"{missing.name} is missing; cp {present[0].name} {missing.name}",
        )
        return
    if read(twins[0]) != read(twins[1]):
        f.add(
            "twins",
            "AGENTS.md and CLAUDE.md differ; edit CLAUDE.md, then cp CLAUDE.md AGENTS.md",
        )


def check_skill_mirror(f: Failures) -> None:
    """.agents/skills/ mirrors .claude/skills/ byte for byte."""
    claude, agents = REPO / ".claude" / "skills", REPO / ".agents" / "skills"
    for src in sorted(claude.glob("*/SKILL.md")):
        mirror = agents / src.parent.name / "SKILL.md"
        if not mirror.exists():
            f.add("skill-mirror", f"{rel(src)} has no mirror at {rel(mirror)}")
        elif mirror.read_bytes() != src.read_bytes():
            f.add(
                "skill-mirror",
                f"{rel(mirror)} differs from {rel(src)} (the .claude/ copy is the source)",
            )
    for mirror in sorted(agents.glob("*/SKILL.md")):
        if not (claude / mirror.parent.name / "SKILL.md").exists():
            f.add(
                "skill-mirror", f"{rel(mirror)} mirrors a skill that no longer exists"
            )


def check_links(f: Failures) -> None:
    """Every relative markdown link resolves to something on disk.

    `embed:` is Structurizr's scheme for showing a view inside a documentation
    page. It names a view key, not a file, so it has nothing to resolve. Only
    an embed carrying alt text reaches here at all, because the link pattern
    needs a non-empty label.
    """
    for src in markdown_files():
        for text, link in LINK_RE.findall(prose(read(src))):
            if link.startswith(("http://", "https://", "#", "mailto:", "embed:")):
                continue
            if not (src.parent / link.split("#")[0]).exists():
                f.add("links", f"{rel(src)}: [{text}]({link}) does not resolve")


def check_docs_index(f: Failures) -> None:
    """Every doc under docs/ is linked from docs/README.md or the architecture README."""
    if not DOCS_INDEX.exists():
        return  # a repository without a docs index has nothing to enforce
    indexes = {DOCS_INDEX: read(DOCS_INDEX), ARCH_INDEX: read(ARCH_INDEX)}
    for doc in sorted((REPO / "docs").rglob("*.md")):
        parts = doc.relative_to(REPO).parts
        if doc in indexes or SKIP_DIRS.intersection(parts) or "decisions" in parts:
            continue
        linked = any(
            (index.parent / link.split("#")[0]).resolve() == doc.resolve()
            for index, text in indexes.items()
            for _, link in LINK_RE.findall(text)
            if not link.startswith(("http://", "https://", "#"))
        )
        if not linked:
            f.add(
                "docs-index",
                f"{rel(doc)} is not linked from docs/README.md or {rel(ARCH_INDEX)}",
            )


def check_adrs(f: Failures) -> None:
    """ADRs import cleanly into Structurizr and are listed in the architecture README."""
    if not ADR_DIR.is_dir():
        return
    index = read(ARCH_INDEX)
    numbers = []
    for path in sorted(ADR_DIR.iterdir()):
        match = ADR_NAME.match(path.name)
        if not path.is_file() or not match:
            f.add(
                "adrs",
                f"{rel(path)}: only NNNN-kebab-title.md files belong in decisions/",
            )
            continue
        number = int(match.group(1))
        numbers.append(number)
        lines = read(path).splitlines()
        if not lines or not lines[0].startswith(f"# {number}. "):
            f.add("adrs", f"{rel(path)}: first line must be '# {number}. <title>'")
        if not any(re.fullmatch(r"Date: \d{4}-\d{2}-\d{2}", ln) for ln in lines):
            f.add("adrs", f"{rel(path)}: needs a 'Date: YYYY-MM-DD' line")
        if "## Status" not in lines or "## Context" not in lines:
            f.add("adrs", f"{rel(path)}: needs '## Status' followed by '## Context'")
        else:
            start, end = lines.index("## Status"), lines.index("## Context")
            status = next((ln for ln in lines[start + 1 : end] if ln.strip()), "")
            if status.split(" ")[0] not in ADR_STATUSES:
                f.add(
                    "adrs",
                    f"{rel(path)}: status must start with one of {sorted(ADR_STATUSES)}",
                )
        if f"(decisions/{path.name})" not in index:
            f.add("adrs", f"{rel(path)} is not listed in {rel(ARCH_INDEX)}")
    if numbers and sorted(numbers) != list(range(1, len(numbers) + 1)):
        f.add(
            "adrs",
            f"ADR numbers must run 1..{len(numbers)} without gaps: {sorted(numbers)}",
        )


def check_view_register(f: Failures) -> None:
    """The README view register lists exactly the views defined in views.dsl."""
    if not VIEWS_DSL.exists() or "## View register" not in read(ARCH_INDEX):
        return  # no register to reconcile yet
    dsl = read(VIEWS_DSL)
    defined = set(VIEW_DEFINITION.findall(dsl))
    section = read(ARCH_INDEX).split("## View register", 1)
    registered = (
        set(
            re.findall(
                r"^\|\s*([A-Za-z][A-Za-z0-9]+)\s*\|",
                section[1].split("\n## ", 1)[0],
                re.MULTILINE,
            )
        )
        - {"Key"}
        if len(section) == 2
        else set()
    )
    for key in sorted(defined - registered):
        f.add(
            "view-register",
            f"view '{key}' is in views.dsl but not in the README view register",
        )
    for key in sorted(registered - defined):
        f.add(
            "view-register",
            f"view '{key}' is in the README view register but not in views.dsl",
        )


def check_speaker_notes(f: Failures) -> None:
    """Every view in views.dsl has a '### <key>' section in the speaker notes."""
    notes_path = ARCH / "talks" / "speaker-notes.md"
    if not notes_path.exists() or not VIEWS_DSL.exists():
        return  # speaker notes are optional; enforce them once they exist
    notes = set(re.findall(r"^### (\S+)\s*$", read(notes_path), re.MULTILINE))
    defined = set(VIEW_DEFINITION.findall(read(VIEWS_DSL)))
    for key in sorted(defined - notes):
        f.add("speaker-notes", f"view '{key}' has no section in {rel(notes_path)}")
    for key in sorted(notes - defined):
        f.add(
            "speaker-notes",
            f"{rel(notes_path)} has a section for '{key}', which is not a view",
        )


def check_ids(f: Failures) -> None:
    """Every cited requirement/risk ID is defined in its owning document."""
    sources = markdown_files() + sorted(ARCH.rglob("*.dsl"))
    for pattern, owner in ID_OWNERS.items():
        if not owner.exists():
            continue  # this repository does not keep that ID family
        owner_text = read(owner)
        defined = set(
            re.findall(
                r"^(?:\|\s*|#+\s*)(" + pattern.strip(r"\b") + r")\b",
                owner_text,
                re.MULTILINE,
            )
        )
        for src in sources:
            for cited in sorted(set(re.findall(pattern, prose(read(src))))):
                if cited not in defined:
                    f.add(
                        "ids",
                        f"{rel(src)} cites {cited}, which {rel(owner)} does not define",
                    )


def check_self_links(f: Failures) -> None:
    """Absolute links back into this repository resolve to a real file.

    Inline code is stripped first: a URL inside backticks is an example of the
    form to use, not a claim that a file exists. Writing the convention down
    must not fail the check that enforces it.
    """
    if not SELF_REPO:
        return  # this repository has not declared its own name
    pattern = re.compile(
        rf"https://github\.com/{re.escape(SELF_REPO)}/blob/[^/]+/([^)#\s]+)"
    )
    for src in markdown_files():
        for path in pattern.findall(INLINE_CODE.sub("", prose(read(src)))):
            if not (REPO / path).exists():
                f.add(
                    "self-links",
                    f"{rel(src)}: a link to {path} does not resolve",
                )


def check_line_width(f: Failures) -> None:
    """Prose wraps, so a one-word edit does not rewrite a whole paragraph's diff.

    Only prose. A table row is as wide as its widest cell and a fenced block is
    code; neither can wrap, and a formatter that pads them makes the problem
    worse rather than better -- measured: aligning this repository's tables took
    its longest line from 815 columns to 1015. A line held over the limit by a
    single unbreakable token, such as an absolute URL, is left alone because
    there is nowhere to break it. An image line is left alone because splitting
    `![alt](embed:Key)` stops the PDF builder recognising the embed.
    """
    for src in markdown_files():
        if any(d in src.parents for d in SKILL_DIRS + AGENT_DIRS):
            continue
        fenced = False
        for number, line in enumerate(read(src).splitlines(), 1):
            if FENCE.match(line):
                fenced = not fenced
                continue
            if fenced or line.lstrip().startswith(("|", "#", "![")):
                continue
            if len(line) <= PROSE_WIDTH:
                continue
            if (
                len(line) - max((len(w) for w in line.split()), default=0)
                <= PROSE_WIDTH
            ):
                continue
            f.add(
                "line-width",
                f"{rel(src)}:{number}: prose line is {len(line)} columns, "
                f"over {PROSE_WIDTH}; wrap it",
            )


def check_overview_complete(f: Failures) -> None:
    """Every architecture document reaches the Documentation tab and the PDF.

    Structurizr imports overview/ and does not recurse, so a document is in the
    tab -- and therefore in the PDF, which is built from the same folder -- only
    if a file in overview/ points at it. Registers stay in their own folders,
    where their IDs are owned, and are symlinked in as NN-name.md; each is
    authored once and the number fixes its order. A register nobody symlinked is
    invisible in the artifact people are handed, while still looking present in
    the repository.
    """
    if not OVERVIEW.is_dir():
        return
    linked = set()
    for link in sorted(OVERVIEW.iterdir()):
        if not (link.is_symlink() and link.suffix == ".md"):
            continue
        # Deleting a register the repository does not need is right; leaving
        # its symlink behind is not. A dangling link is a section of the
        # document that points at nothing.
        if not link.exists():
            f.add(
                "overview-complete",
                f"{rel(link)} points at {os.readlink(link)}, which does not "
                f"exist; delete the symlink too",
            )
            continue
        linked.add(link.resolve())
    for doc in sorted(ARCH.rglob("*.md")):
        parts = doc.relative_to(ARCH).parts
        if len(parts) == 1 or parts[0] in NOT_SYMLINKED:
            continue
        if SKIP_DIRS.intersection(parts) or doc.is_symlink():
            continue
        if doc.resolve() not in linked:
            f.add(
                "overview-complete",
                f"{rel(doc)} is in no reading path: symlink it into "
                f"{rel(OVERVIEW)}/ as NN-name.md, or it stays out of the PDF",
            )


ATX_HEADING = re.compile(r"^(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$")
WORKSPACE_NAME = re.compile(r'^\s*workspace\s+"([^"]+)"', re.MULTILINE)


def workspace_name() -> str | None:
    """The name declared in workspace.dsl -- the one level-1 heading allowed."""
    dsl = ARCH / "workspace.dsl"
    if not dsl.exists():
        return None
    match = WORKSPACE_NAME.search(read(dsl))
    return match.group(1) if match else None


def headings(text: str) -> list[tuple[int, int, str]]:
    """(line number, level, title) for every ATX heading outside fenced code.

    A `#` inside a fence is a shell comment or a sample document, not a heading.
    """
    found, fenced = [], False
    for number, line in enumerate(text.splitlines(), 1):
        if FENCE.match(line):
            fenced = not fenced
            continue
        if fenced:
            continue
        match = ATX_HEADING.match(line)
        if match:
            found.append((number, len(match.group(1)), match.group(2)))
    return found


def check_heading_visibility(f: Failures) -> None:
    """Every imported document shows its section title in the page and the nav.

    Structurizr's Documentation tab HIDES a level-1 (`#`) heading: it appears
    neither in the rendered page nor in the navigation. The PDF builder
    normalises heading levels, so the same document looks correct in print --
    which is how a template teaching "every section is an `#` heading" shipped
    to five repositories with every section title missing, past a checker that
    confirmed each document was included but never looked at how it rendered.

    So, for each document overview/ imports (following symlinks, reported at
    the real path where the fix belongs):

    - a level-1 heading is an error, except one: the workspace name as the very
      first heading of the first document, which the PDF builder uses as the
      cover line;
    - the document must have a level-2 heading, or it has no entry in the nav;
    - heading levels must not skip (`##` straight to `####`).

    ADRs are imported by `!adrs` and rendered separately, so they are out of
    scope; so is anything inside a fenced block.
    """
    if not OVERVIEW.is_dir():
        return  # nothing imported yet
    name = workspace_name()
    documents = sorted(
        p for p in OVERVIEW.iterdir() if p.suffix == ".md" and p.exists()
    )
    seen = set()
    for index, doc in enumerate(documents):
        real = doc.resolve()
        if real in seen:
            continue  # the same register symlinked twice is one document
        seen.add(real)
        where = rel(real)
        found = headings(read(doc))
        has_section = hidden = False
        previous = None
        for position, (number, level, title) in enumerate(found):
            if level == 1:
                opening = index == 0 and position == 0 and title == name
                if not opening:
                    hidden = True
                    f.add(
                        "headings",
                        f"{where}:{number}: '# {title}' is hidden -- Structurizr "
                        f"drops a level-1 heading from both the page and the "
                        f"navigation. Use '## {title}' and demote the rest of the "
                        f"document by one level.",
                    )
            elif level == 2:
                has_section = True
            if previous is not None and level > previous + 1:
                f.add(
                    "headings",
                    f"{where}:{number}: heading level skips from {previous} to "
                    f"{level} ('{'#' * level} {title}'); use level {previous + 1}.",
                )
            previous = level
        # A hidden title already tells the reader what to change; saying the
        # document ALSO lacks a visible section would report one fault twice.
        if not has_section and not hidden:
            f.add(
                "headings",
                f"{where}: no visible section heading -- open the document with "
                f"'## <title>' so it appears in the navigation.",
            )


class Fence(NamedTuple):
    """A fenced block: opening line (1-based), closing line, info string, body."""

    start: int
    end: int | None  # None when the file ends before the fence closes
    info: str
    body: list[str]


FENCE_OPEN = re.compile(r"^\s*(?P<marker>`{3,}|~{3,})(?P<info>.*)$")
FENCE_CLOSE = re.compile(r"^\s*(?P<marker>`{3,}|~{3,})\s*$")
# The provenance of a derived block: a view exported as Mermaid. Only this
# exact form counts; PROVENANCE_START catches a misspelt one so it cannot pass
# for an ordinary comment.
PROVENANCE = re.compile(r"^\s*<!-- mermaid-view: (?P<key>\S+) -->\s*$")
PROVENANCE_START = re.compile(r"^\s*<!--\s*mermaid-view\b")
MERMAID_TYPES = frozenset(
    {
        "flowchart", "graph", "sequenceDiagram", "stateDiagram", "stateDiagram-v2",
        "erDiagram", "classDiagram", "gantt", "timeline", "journey", "mindmap",
        "pie", "quadrantChart", "requirementDiagram", "gitGraph", "sankey-beta",
        "xychart-beta", "block-beta", "packet-beta", "kanban",
    }
)  # fmt: skip
# Structure is the Structurizr model's; a second drawing of it is a second
# source of truth.
STRUCTURE_TYPES = frozenset(
    {
        "C4Context", "C4Container", "C4Component", "C4Dynamic", "C4Deployment",
        "architecture-beta",
    }
)  # fmt: skip


def fences(text: str) -> tuple[list[Fence], list[tuple[int, str | None]]]:
    """Every fenced block in text, and the provenance comments outside them.

    A fence closes on a line of the same character, at least as long, with
    nothing after it (CommonMark). So a four-backtick block can show a
    three-backtick example, and a tilde fence does not end on backticks. The
    toggle in prose() is enough for what it does, but it would read the example
    inside a longer fence as a real diagram. A comment is (line, key), or
    (line, None) when it starts like a provenance comment and is malformed.
    """
    found: list[Fence] = []
    comments: list[tuple[int, str | None]] = []
    opener: tuple[str, int, str] | None = None
    body: list[str] = []
    for number, line in enumerate(text.split("\n"), 1):
        if opener is None:
            opening = FENCE_OPEN.match(line)
            # Inline code such as ```x``` is not a fence: a backtick fence's
            # info string cannot contain a backtick.
            marker = opening["marker"] if opening else ""
            if opening and not (marker[0] == "`" and "`" in opening["info"]):
                opener, body = (marker, number, opening["info"].strip()), []
            elif PROVENANCE_START.match(line):
                exact = PROVENANCE.match(line)
                comments.append((number, exact["key"] if exact else None))
            continue
        closing = FENCE_CLOSE.match(line)
        if (
            closing
            and closing["marker"][0] == opener[0][0]
            and len(closing["marker"]) >= len(opener[0])
        ):
            found.append(Fence(opener[1], number, opener[2], body))
            opener = None
        else:
            body.append(line)
    if opener:
        found.append(Fence(opener[1], None, opener[2], body))
    return found, comments


def mermaid_fences(
    text: str,
) -> tuple[list[tuple[Fence, str | None]], list[tuple[int, str | None]]]:
    """The mermaid fences with their view key, and the comments that head none.

    A derived block is a mermaid fence on the line directly below
    `<!-- mermaid-view: Key -->`. The key is None for a hand-written diagram.
    """
    found, comments = fences(text)
    mermaid = [f for f in found if f.info.lower().split()[:1] == ["mermaid"]]
    heads = {f.start for f in mermaid}
    keys = {line + 1: key for line, key in comments if key and line + 1 in heads}
    stray = [(line, key) for line, key in comments if not (key and line + 1 in heads)]
    return [(f, keys.get(f.start)) for f in mermaid], stray


def mermaid_type(body: list[str]) -> str | None:
    """The diagram type: the first word of the first line that says anything.

    Blank lines, `%%` comments (which include `%%{init: ...}%%`) and a leading
    `---` front-matter block come before the type and are skipped.
    """
    lines = iter(body)
    for line in lines:
        text = line.strip()
        if not text or text.startswith("%%"):
            continue
        if text == "---":
            for inner in lines:
                if inner.strip() == "---":
                    break
            continue
        return text.split()[0]
    return None


def imported_documents() -> set[Path]:
    """Real paths of what the Documentation tab imports: overview/ and decisions/.

    A symlink in overview/ counts as the file it points at, which is where the
    check reports it.
    """
    return {
        p.resolve()
        for folder in (OVERVIEW, ADR_DIR)
        if folder.is_dir()
        for p in folder.iterdir()
        if p.exists()
    }


def check_mermaid(f: Failures) -> None:
    """Mermaid blocks render, stay out of the model's territory and stay derived.

    Structurizr owns structure; Mermaid draws what the model does not (states,
    decisions, dependencies, data sketches). GitHub shows a broken fence as an
    error box and Structurizr as a code block, and neither says which file
    caused it, so the mechanical faults are caught here:

    - a fence that never closes swallows the rest of the page;
    - a first line that is not a diagram type is a typo GitHub renders as an
      error box;
    - `C4Context`, `architecture-beta` and their kin redraw the model by hand,
      which is a second source of truth;
    - `<!-- mermaid-view: Key -->` marks a block derived from view Key by `make
      mermaid-views`: it must head a mermaid fence, name a view that exists,
      stay out of imported documents (the tab shows a Mermaid fence as code, so
      those embed the view instead) and equal the generated file, so a model
      change cannot leave the diagram behind. Without that file, or without
      views.dsl, the comparison is skipped, not failed.
    """
    known = (
        set(VIEW_DEFINITION.findall(read(VIEWS_DSL))) if VIEWS_DSL.exists() else None
    )
    imported = imported_documents()
    for src in markdown_files():
        blocks, stray = mermaid_fences(read(src))
        for line, key in stray:
            what = (
                "malformed provenance comment; write it exactly as "
                "<!-- mermaid-view: Key -->"
                if key is None
                else f"<!-- mermaid-view: {key} --> must sit on the line "
                f"directly above a mermaid fence, with nothing between"
            )
            f.add("mermaid", f"{rel(src)}:{line}: {what}")
        for fence, view in blocks:
            where = f"{rel(src)}:{fence.start}"
            if fence.end is None:
                f.add("mermaid", f"{where}: mermaid fence is never closed")
                continue
            kind = mermaid_type(fence.body)
            if kind in STRUCTURE_TYPES:
                f.add(
                    "mermaid",
                    f"{where}: {kind}: structure belongs to the Structurizr "
                    f"model; embed the view, or derive it with "
                    f"<!-- mermaid-view: Key -->",
                )
            elif kind is None:
                f.add("mermaid", f"{where}: mermaid fence is empty")
            elif kind not in MERMAID_TYPES:
                f.add(
                    "mermaid",
                    f"{where}: '{kind}' is not a Mermaid diagram type; start the "
                    f"block with one such as flowchart, sequenceDiagram, "
                    f"stateDiagram-v2 or erDiagram",
                )
            if view is None:
                continue
            if known is not None and view not in known:
                f.add("mermaid", f"{where}: view '{view}' is not defined in views.dsl")
            if src.resolve() in imported:
                f.add(
                    "mermaid",
                    f"{where}: derived block '{view}' sits in a document the "
                    f"Documentation tab imports, which shows it as code; use "
                    f"![alt](embed:{view}) there",
                )
            generated = MERMAID_VIEWS / f"{view}.mmd"
            if generated.exists():
                expected = read(generated).removesuffix("\n")
                if "\n".join(fence.body) != expected:
                    f.add(
                        "mermaid",
                        f"{where}: derived block '{view}' is stale; "
                        f"run make mermaid-views",
                    )


CHECKS = (
    check_twins,
    check_skill_mirror,
    check_links,
    check_self_links,
    check_line_width,
    check_docs_index,
    check_overview_complete,
    check_heading_visibility,
    check_adrs,
    check_view_register,
    check_speaker_notes,
    check_ids,
    check_mermaid,
)


def main() -> int:
    failures = Failures()
    for check in CHECKS:
        check(failures)
    if not failures:
        print(f"docs consistency: {len(CHECKS)} checks passed")
        return 0
    print("docs consistency: documentation contradicts the tree\n", file=sys.stderr)
    for name, detail in failures:
        print(f"  [{name}] {detail}", file=sys.stderr)
    print(
        "\nFix the docs in this branch. See .claude/skills/docs-sync/SKILL.md.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main())
