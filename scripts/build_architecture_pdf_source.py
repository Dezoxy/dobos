#!/usr/bin/env python3
"""Assemble a Structurizr Documentation tab into one Markdown file for Pandoc.

Structurizr's Documentation tab is the Markdown files in the directory named
by `!docs` in workspace.dsl, joined in filename order, with
`![](embed:ViewKey)` lines showing views inline. This does the same for print:

- joins the files and replaces every embed with the PNG exported for that view;
  a diagram at least LANDSCAPE_RATIO times wider than tall gets its own
  landscape page;
- normalises headings so the top level used becomes level 1, whether the files
  use `#` or `##` for their sections;
- appends a "Decisions" section with every ADR the workspace imports with
  `!adrs`, in number order, each on its own page;
- appends the Markdown files listed in an optional `pdf-sections.txt` in the
  architecture directory (one path per line, relative to that directory; `#`
  starts a comment), for documents outside the Documentation tab such as a
  risk register;
- turns every Mermaid diagram into an image: a ```mermaid fence in a page,
  ADR or extra section, or the image link Structurizr's Mermaid plugin
  rewrites a fence into (`![](<mermaid.url>/svg/<base64url of the source>)`,
  or `pako:` plus the compressed source when `mermaid.compress` is on). The
  prefix is `mermaid.url` from the workspace JSON; without it no link is
  touched. The source lands in generated/mermaid-pdf/<sha12>.mmd and the text
  gets `![](generated/mermaid-pdf/<sha12>.png)`; scripts/render-mermaid.sh
  makes the PNGs before Pandoc runs. A link that cannot be decoded stops the
  build, because Pandoc would fetch it or fail on it and the PDF would ship a
  hole. Diagrams get no landscape page: the size caps below fit them;
- turns links to repository files into plain text, because on paper they lead
  nowhere; web links and in-page anchors stay links;
- appends a "Views" section with every view that the text does not embed, so
  the PDF shows the whole model even when the pages embed nothing;
- writes a YAML header for the Eisvogel cover (project, date, edition) and
  contents page.

Views come from Structurizr's JSON export, not from parsing the DSL, so their
keys, titles and descriptions are exactly what Structurizr sees. The edition is
the last commit that touched the architecture directory, marked when it has
uncommitted changes. The script prints the PDF file name to use, which carries
the same identity as the cover: <project>-architecture-<date>-<edition>.pdf,
with -dirty after the commit when the docs have uncommitted changes.

The generated directory sits inside the architecture directory and holds the
PNG export (`export -format png`) and the JSON export (`export -format json`,
workspace.json) of the same workspace. Image paths in the output are relative
to the architecture directory, so run Pandoc from there.

The canonical copy lives in development-base (scripts/); repositories copy it
unchanged and wrap it in their own command.

Usage:
    build_architecture_pdf_source.py <architecture-dir> <generated-dir> <output.md>
"""

from __future__ import annotations

import base64
import datetime as dt
import hashlib
import json
import re
import shutil
import struct
import subprocess
import sys
import zlib
from pathlib import Path

EMBED = re.compile(r"^!\[[^\]]*\]\(embed:([A-Za-z0-9_-]+)\)\s*$")
HEADING = re.compile(r"^(#{1,6})(\s.*)$")
FENCE = re.compile(r"^(```|~~~)")
# A fence with its marker and info string, for finding the Mermaid ones. Unlike
# FENCE this keeps the indent and lets a fence close only on its own kind.
FENCE_OPEN = re.compile(r"^(?P<indent>\s*)(?P<marker>`{3,}|~{3,})(?P<info>.*)$")
FENCE_CLOSE = re.compile(r"^\s*(?P<marker>`{3,}|~{3,})\s*$")
# Mermaid sources and their PNGs, inside the generated directory.
MERMAID_DIR = "mermaid-pdf"
DOCS_DIRECTIVE = re.compile(r"^\s*!docs\s+(\S+)")
# A Markdown link whose target has no URL scheme and is not an in-page anchor:
# a path in the repository. Images (![...]) are left alone.
REPO_LINK = re.compile(r"(?<!!)\[([^\]]+)\]\((?![A-Za-z][A-Za-z0-9+.-]*:|#)[^)\s]+\)")
# Optional list of extra Markdown files, relative to the architecture directory.
EXTRA_SECTIONS = "pdf-sections.txt"
NEW_PAGE = "```{=latex}\n\\clearpage\n```"
# Structurizr rewrites a link from one ADR to another as an anchor on the
# target's ID, such as (#9). The PDF gives each ADR the anchor adr-<ID>.
ADR_LINK = re.compile(r"\]\(#([^)\s]+)\)")
# Reading order for views the text does not embed: zoom in, then behaviour,
# then where it runs.
VIEW_ORDER = [
    "systemLandscapeViews",
    "systemContextViews",
    "containerViews",
    "componentViews",
    "filteredViews",
    "dynamicViews",
    "deploymentViews",
    "customViews",
    "imageViews",
]
# Structurizr's default software-system blue: the cover rule and a light tint.
COVER_BLUE = "1168BD"
COVER_FILL = "E8F1FB"
# A diagram at least this much wider than tall gets its own landscape page: on
# A4 that enlarges it by 30% to 50%. Narrower ones stay in the text flow.
LANDSCAPE_RATIO = 1.3
# Characters of the edition commit ID on the cover, in the file name and tag.
EDITION_LENGTH = 7
# Wide diagrams get a physically landscape page, so the running header and
# footer lie along the long edge like the diagram. See LANDSCAPE_MACROS.
LANDSCAPE_OPEN = "```{=latex}\n\\landscapepage\n```"
LANDSCAPE_CLOSE = "```{=latex}\n\\portraitpage\n```"
# \pdfpagewidth is a XeTeX primitive, so the PDF engine must stay xelatex.
# geometry cannot change the paper mid-document, so these set it directly:
# the XeTeX page size, the text block with the same 2 cm margins and the
# header/footer Eisvogel reserves, then KOMA recomputes the header width.
# \@colht is LaTeX's page box height; the footer sits below it, so it has to
# follow \textheight or the footer lands off the page or mid-text.
LANDSCAPE_MACROS = r"""```{=latex}
\makeatletter
\newcommand{\hl@setpage}[2]{%
  \clearpage
  \setlength{\pdfpagewidth}{#1}\setlength{\pdfpageheight}{#2}%
  \setlength{\paperwidth}{#1}\setlength{\paperheight}{#2}%
  \setlength{\hoffset}{0pt}\setlength{\voffset}{0pt}%
  \setlength{\oddsidemargin}{\dimexpr 2cm - 1in\relax}%
  \setlength{\evensidemargin}{\oddsidemargin}%
  \setlength{\topmargin}{\dimexpr 2cm - 1in\relax}%
  \setlength{\textwidth}{\dimexpr #1 - 4cm\relax}%
  \setlength{\textheight}{\dimexpr #2 - 4cm - \headheight - \headsep - \footskip\relax}%
  \setlength{\hsize}{\textwidth}\setlength{\linewidth}{\textwidth}%
  \setlength{\columnwidth}{\textwidth}%
  \global\vsize\textheight\global\@colht\textheight\global\@colroom\textheight%
  \KOMAoptions{headwidth=text,footwidth=text}}
\newcommand{\landscapepage}{\hl@setpage{297mm}{210mm}}
\newcommand{\portraitpage}{\hl@setpage{210mm}{297mm}}
% Pandoc caps an image's WIDTH at the text block but never its height, so a
% tall view -- a dynamic or a deep container view -- renders taller than the
% page. LaTeX then cannot place it where it falls, pushes it to a page of its
% own and leaves the remainder of the previous page empty: a sheet carrying
% nothing but the running header and footer. Capping the height as well, with
% the aspect ratio kept, makes a tall view shrink to fit instead.
\def\maxwidth{\ifdim\Gin@nat@width>\linewidth\linewidth\else\Gin@nat@width\fi}
\def\maxheight{\ifdim\Gin@nat@height>0.92\textheight 0.92\textheight\else\Gin@nat@height\fi}
\makeatother
\setkeys{Gin}{width=\maxwidth,height=\maxheight,keepaspectratio}
```"""


def load_workspace(generated: Path) -> dict:
    path = generated / "workspace.json"
    try:
        return json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as exc:
        sys.exit(f"cannot read {path}: {exc} (export the workspace as JSON first)")


def workspace_views(workspace: dict) -> list[dict]:
    """Every view, zooming in: landscape, context, containers ... deployment.

    The JSON export lists its view collections alphabetically, so the reading
    order is set here; any collection not named comes last.
    """
    collections = workspace.get("views", {})
    names = [name for name in VIEW_ORDER if name in collections]
    names += sorted(name for name in collections if name not in VIEW_ORDER)
    return [
        view
        for name in names
        if isinstance(collections[name], list)
        for view in collections[name]
        if isinstance(view, dict) and "key" in view
    ]


def docs_dir(arch_dir: Path) -> Path:
    """The directory named by the workspace's !docs directive."""
    for line in (arch_dir / "workspace.dsl").read_text().splitlines():
        match = DOCS_DIRECTIVE.match(line)
        if match:
            return arch_dir / match.group(1)
    sys.exit(f"no !docs directive in {arch_dir / 'workspace.dsl'}")


def png_size(path: Path) -> tuple[int, int]:
    """Width and height from the PNG IHDR chunk."""
    with path.open("rb") as handle:
        head = handle.read(24)
    if head[:8] != b"\x89PNG\r\n\x1a\n":
        sys.exit(f"{path} is not a PNG")
    width, height = struct.unpack(">II", head[16:24])
    return width, height


def image(view: dict, generated: Path, *, room: bool = False) -> tuple[str, bool]:
    """The view's PNG as Markdown, and whether it needs a landscape page.

    With room, the diagram keeps a fifth of the page free for the heading and
    description placed above it, so all three stay on one page.

    No caption: the exported PNG already carries the view's title and
    description, so a caption would repeat it.
    """
    key = view["key"]
    png = generated / f"{key}.png"
    if not png.exists():
        sys.exit(f"no exported PNG for view {key} in {generated}")
    alt = (view.get("title") or view.get("description") or key).replace('"', "'")
    width, height = png_size(png)
    size = ' width="100%" height="80%"' if room else ""
    markdown = f'![]({generated.name}/{key}.png){{fig-alt="{alt}"{size}}}'
    return markdown, width / height >= LANDSCAPE_RATIO


def on_page(blocks: list[str], landscape: bool) -> str:
    """Markdown blocks, wrapped in a landscape page when the diagram needs one."""
    if landscape:
        blocks = [LANDSCAPE_OPEN, *blocks, LANDSCAPE_CLOSE]
    return "\n\n".join(blocks)


def mermaid_url(workspace: dict) -> str | None:
    """The render server of the workspace's Mermaid plugin, or None when it is off.

    The DSL sets it as `"mermaid.url"` in the views properties, which the JSON
    export keeps under views.configuration.properties.
    """
    views = workspace.get("views")
    configuration = views.get("configuration") if isinstance(views, dict) else None
    properties = (
        configuration.get("properties") if isinstance(configuration, dict) else None
    )
    url = properties.get("mermaid.url") if isinstance(properties, dict) else None
    return url.rstrip("/") if isinstance(url, str) and url.strip() else None


def decode_plugin_link(payload: str) -> str:
    """The Mermaid source behind the last part of a plugin link.

    Plain: the source as base64url. With mermaid.compress on: `pako:` and the
    base64url of the zlib-compressed JSON `{"code": ...}`. Anything else stops
    the build naming the link, because an image that cannot be fetched or
    rendered is a hole in a document people are handed.
    """
    try:
        compressed = payload.startswith("pako:")
        data = payload.removeprefix("pako:")
        if not re.fullmatch(r"[A-Za-z0-9_-]+={0,2}", data):
            raise ValueError("not base64url")
        raw = base64.urlsafe_b64decode(data + "=" * (-len(data) % 4))
        if compressed:
            source = json.loads(zlib.decompress(raw, zlib.MAX_WBITS | 32))["code"]
        else:
            source = raw.decode("utf-8")
        if not isinstance(source, str) or not source.strip():
            raise ValueError("no diagram source in it")
    except (ValueError, zlib.error, KeyError, TypeError) as exc:
        shown = payload if len(payload) <= 48 else payload[:48] + "..."
        sys.exit(
            f"cannot decode the Mermaid link ending in {shown}: {exc}. Check that "
            f"mermaid.compress in the workspace matches how the link was written."
        )
    return source


def mermaid_image(source: str, generated: Path) -> str:
    """Save a diagram's source for the renderer; the image line that will show it.

    Named by a hash of the source, so the same diagram written twice is one
    file. render-mermaid.sh turns each .mmd in the folder into the .png the
    line points at, before Pandoc runs.
    """
    if not source.endswith("\n"):
        source += "\n"
    digest = hashlib.sha256(source.encode("utf-8")).hexdigest()[:12]
    folder = generated / MERMAID_DIR
    folder.mkdir(parents=True, exist_ok=True)
    (folder / f"{digest}.mmd").write_text(source, encoding="utf-8")
    return f"![]({generated.name}/{MERMAID_DIR}/{digest}.png)"


def mermaid_images(lines: list[str], generated: Path, url: str | None) -> list[str]:
    """The lines with every Mermaid diagram replaced by the image of its render.

    Fences are found the way Markdown finds them: a fence closes only on its
    own character, at least as long, so a four-backtick block can show a
    Mermaid example without it becoming a diagram. A plugin link is decoded
    only when it starts with the workspace's own mermaid.url; another site's
    image is not ours to fetch. The image gets a paragraph to itself, so Pandoc
    makes a figure of it even when the fence sat right under a sentence.
    """
    link = None
    if url:
        link = re.compile(
            r"!\[[^\]]*\]\("
            + re.escape(url)
            + r"/(?:svg|png)/([^)\s?]+)(?:\?[^)\s]*)?\)"
        )
    out: list[str] = []
    fence: tuple[str, bool, str] | None = None  # marker, is Mermaid, indent
    body: list[str] = []
    for line in lines:
        if fence is None:
            opening = FENCE_OPEN.match(line)
            marker = opening["marker"] if opening else ""
            if opening and not (marker[0] == "`" and "`" in opening["info"]):
                mermaid = opening["info"].strip().lower().split()[:1] == ["mermaid"]
                fence, body = (marker, mermaid, opening["indent"]), []
                if not mermaid:
                    out.append(line)
            elif link:
                out.append(
                    link.sub(
                        lambda m: mermaid_image(decode_plugin_link(m[1]), generated),
                        line,
                    )
                )
            else:
                out.append(line)
            continue
        marker, mermaid, indent = fence
        closing = FENCE_CLOSE.match(line)
        if (
            closing
            and closing["marker"][0] == marker[0]
            and len(closing["marker"]) >= len(marker)
        ):
            if mermaid:
                image_line = mermaid_image("\n".join(body) + "\n", generated)
                out += ["", indent + image_line, ""]
            else:
                out.append(line)
            fence = None
        elif mermaid:
            body.append(line)
        else:
            out.append(line)
    if fence and fence[1]:
        sys.exit("a Mermaid fence is never closed; run make docs to find it")
    return out


def reset_mermaid(generated: Path) -> None:
    """Empty the Mermaid folder, so a diagram cut from the text is not rendered."""
    folder = generated / MERMAID_DIR
    try:
        shutil.rmtree(folder)
    except FileNotFoundError:
        pass
    except OSError as exc:
        sys.exit(f"cannot clear {folder}: {exc}")


def git(*args: str) -> str:
    return subprocess.run(
        ["git", *args], check=True, capture_output=True, text=True
    ).stdout.strip()


def edition(arch_dir: Path) -> tuple[str, bool]:
    """Short SHA of the last commit touching the model and docs, and whether dirty.

    Always 7 characters, as GitHub shows commits, unless git needs more to be
    unambiguous. Git's automatic length depends on the clone (a CI runner gave
    8 where a laptop gave 7), which would give one model two edition names.
    """
    # A repository without commits (just bootstrapped) has no HEAD, and `git log`
    # exits 128 there; it has no edition yet, but `git status` still works.
    head = subprocess.run(
        ["git", "rev-parse", "--verify", "--quiet", "HEAD"],
        check=False,
        capture_output=True,
    )
    full = (
        git("log", "-1", "--format=%H", "--", str(arch_dir))
        if head.returncode == 0
        else ""
    )
    sha = git("rev-parse", f"--short={EDITION_LENGTH}", full) if full else "uncommitted"
    dirty = bool(git("status", "--porcelain", "--", str(arch_dir)))
    return sha, dirty


def pdf_name(project: str, day: dt.date, sha: str, dirty: bool) -> str:
    """File name with the cover's identity: homelab-architecture-2026-09-18-84f0b41.pdf."""
    slug = re.sub(r"[^a-z0-9]+", "-", project.lower()).strip("-") or "architecture"
    suffix = "-dirty" if dirty else ""
    return f"{slug}-architecture-{day.isoformat()}-{sha}{suffix}.pdf"


def page_lines(pages: list[Path], project: str) -> list[str]:
    """The pages joined; a first line repeating the project name is the cover's."""
    lines: list[str] = []
    for index, page in enumerate(pages):
        text = page.read_text().splitlines()
        if index == 0 and text and text[0].lstrip("#").strip() == project:
            text = text[1:]
        lines.extend([*text, ""])
    return lines


def heading_shift(lines: list[str]) -> int:
    """How many levels to lift headings so the top level used becomes 1."""
    levels, fenced = [], False
    for line in lines:
        if FENCE.match(line):
            fenced = not fenced
        elif not fenced and (match := HEADING.match(line)):
            levels.append(len(match.group(1)))
    return min(levels) - 1 if levels else 0


def plain_links(line: str) -> str:
    """Links to repository files as their text; web links and anchors unchanged."""
    return REPO_LINK.sub(r"\1", line)


def body(
    lines: list[str],
    views: dict[str, dict],
    generated: Path,
    *,
    top: int = 1,
    unnumbered: bool = False,
    anchor: str | None = None,
    mermaid: str | None = None,
) -> tuple[str, set[str]]:
    """Lines with headings normalised and embeds replaced; also the embedded keys.

    The top heading level used becomes `top`. With unnumbered, headings carry
    no section number (an ADR has its own), and only the top level is listed
    in the contents, and the first top-level heading gets the anchor. Mermaid
    diagrams become images first (see mermaid_images); `mermaid` is the
    workspace's mermaid.url, or None when its plugin is off.
    """
    lines = mermaid_images(lines, generated, mermaid)
    shift, fenced = heading_shift(lines) - (top - 1), False
    out: list[str] = []
    embedded: set[str] = set()
    for line in lines:
        if FENCE.match(line):
            fenced = not fenced
        heading = None if fenced else HEADING.match(line)
        embed = None if fenced else EMBED.match(line)
        if heading:
            level = len(heading.group(1)) - shift
            text = plain_links(heading.group(2))
            if unnumbered and level == top and anchor:
                text += f" {{#{anchor} .unnumbered}}"
                anchor = None
            elif unnumbered:
                text += " {.unnumbered}" if level == top else " {.unnumbered .unlisted}"
            out.append("#" * level + text)
        elif embed:
            key = embed.group(1)
            if key not in views:
                sys.exit(f"the documentation embeds {key}, which is not a view")
            markdown, wide = image(views[key], generated)
            out.append(on_page([markdown], wide))
            embedded.add(key)
        else:
            out.append(line if fenced else plain_links(line))
    return "\n".join(out), embedded


def decision_order(decision: dict) -> tuple[int, str]:
    """Numeric IDs in number order, then any others by ID."""
    ident = str(decision.get("id", ""))
    return (int(ident), "") if ident.isdigit() else (sys.maxsize, ident)


def decisions(
    workspace: dict, views: dict[str, dict], generated: Path, mermaid: str | None
) -> tuple[str, set[str]]:
    """A section with every Markdown ADR in the workspace, one per page."""
    records = [
        record
        for record in workspace.get("documentation", {}).get("decisions", [])
        if isinstance(record, dict) and record.get("content")
    ]
    markdown = [
        record for record in records if record.get("format", "Markdown") == "Markdown"
    ]
    if len(markdown) < len(records):
        print(
            f"skipped {len(records) - len(markdown)} non-Markdown ADRs", file=sys.stderr
        )
    if not markdown:
        return "", set()
    ids = {str(record.get("id")) for record in markdown}

    def to_adr(match: re.Match[str]) -> str:
        target = match.group(1)
        return f"](#adr-{target})" if target in ids else match.group(0)

    parts = [
        "",
        "# Decisions",
        "",
        "Architecture decision records, in number order.",
        "",
    ]
    embedded: set[str] = set()
    for record in sorted(markdown, key=decision_order):
        content = ADR_LINK.sub(to_adr, record["content"])
        text, keys = body(
            content.splitlines(),
            views,
            generated,
            top=2,
            unnumbered=True,
            anchor=f"adr-{record.get('id')}",
            mermaid=mermaid,
        )
        parts += [NEW_PAGE, "", text, ""]
        embedded |= keys
    return "\n".join(parts), embedded


def extra_sections(
    arch_dir: Path, views: dict[str, dict], generated: Path, mermaid: str | None
) -> tuple[str, set[str]]:
    """The files listed in pdf-sections.txt, each from a new page."""
    listing = arch_dir / EXTRA_SECTIONS
    if not listing.exists():
        return "", set()
    root = arch_dir.resolve()
    parts: list[str] = []
    embedded: set[str] = set()
    for raw in listing.read_text().splitlines():
        entry = raw.split("#", 1)[0].strip()
        if not entry:
            continue
        path = (arch_dir / entry).resolve()
        if root not in path.parents or path.suffix != ".md" or not path.is_file():
            sys.exit(f"{listing}: {entry} is not a Markdown file inside {arch_dir}")
        text, keys = body(
            path.read_text().splitlines(), views, generated, mermaid=mermaid
        )
        parts += ["", NEW_PAGE, "", text]
        embedded |= keys
    return "\n".join(parts), embedded


def appendix(views: list[dict], embedded: set[str], generated: Path) -> str:
    """A final section with every view the text does not embed."""
    rest = [view for view in views if view["key"] not in embedded]
    if not rest:
        return ""
    parts = ["", "# Views", "", "Views the documentation above does not show.", ""]
    for view in rest:
        # Heading and description go on the diagram's page, landscape or not.
        blocks = [f"## {view.get('title') or view['key']}"]
        if view.get("description"):
            blocks.append(view["description"])
        markdown, wide = image(view, generated, room=True)
        parts += [on_page([*blocks, markdown], wide), ""]
    return "\n".join(parts)


def header(project: str, edition_text: str, today: dt.date) -> str:
    return "\n".join(
        [
            "---",
            f'title: "{project}"',
            'subtitle: "Architecture documentation"',
            f'author: "Edition {edition_text}"',
            f'date: "{today.day} {today:%B %Y}"',
            "lang: en-GB",
            "titlepage: true",
            f'titlepage-color: "{COVER_FILL}"',
            f'titlepage-rule-color: "{COVER_BLUE}"',
            'titlepage-text-color: "1F2937"',
            "toc: true",
            "toc-own-page: true",
            'toc-title: "Contents"',
            "numbersections: true",
            "colorlinks: true",
            # C4 styles often reserve red for a warning; links stay blue.
            "linkcolor: NavyBlue",
            "urlcolor: NavyBlue",
            "toccolor: black",
            'geometry: "a4paper,margin=2cm"',
            "header-includes: |",
            *("  " + line for line in LANDSCAPE_MACROS.splitlines()),
            'float-placement-figure: "H"',
            f'footer-left: "{project} architecture, edition {edition_text}"',
            "---",
            "",
        ]
    )


def main() -> int:
    if len(sys.argv) != 4:
        sys.exit(__doc__)
    arch_dir, generated, output = (Path(arg) for arg in sys.argv[1:])
    if generated.resolve().parent != arch_dir.resolve():
        sys.exit(f"{generated} must be a directory directly inside {arch_dir}")
    workspace = load_workspace(generated)
    views = workspace_views(workspace)
    project = workspace.get("name") or "Architecture"
    pages = sorted(docs_dir(arch_dir).glob("*.md"))
    if not pages:
        sys.exit(f"no Markdown files in {docs_dir(arch_dir)}")
    by_key = {view["key"]: view for view in views}
    url = mermaid_url(workspace)
    reset_mermaid(generated)
    text, embedded = body(page_lines(pages, project), by_key, generated, mermaid=url)
    adrs, adr_views = decisions(workspace, by_key, generated, url)
    extra, extra_views = extra_sections(arch_dir, by_key, generated, url)
    embedded |= adr_views | extra_views
    sha, dirty = edition(arch_dir)
    edition_text = f"{sha} with uncommitted changes" if dirty else sha
    today = dt.datetime.now(dt.UTC).astimezone().date()  # the operator's local date
    output.write_text(
        header(project, edition_text, today)
        + text
        + adrs
        + extra
        + appendix(views, embedded, generated)
    )
    diagrams = len(list((generated / MERMAID_DIR).glob("*.mmd")))
    print(
        f"wrote {output} for {project}, edition {edition_text}: "
        f"{len(embedded)} views embedded, "
        f"{len(views) - len(embedded)} in the Views section"
        + (f", {diagrams} Mermaid diagrams to render" if diagrams else ""),
        file=sys.stderr,
    )
    print(pdf_name(project, today, sha, dirty))
    return 0


if __name__ == "__main__":
    sys.exit(main())
