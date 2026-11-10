"""Tests for scripts/mermaid_blocks.py: derived blocks and Mermaid extraction.

A derived block is a Mermaid fence under `<!-- mermaid-view: Key -->`. `sync`
fills it from Structurizr's export of view Key; the checker later compares the
block with the file `sync` wrote, so what `sync` writes and what it puts in the
README have to be the same text. `extract` hands every fence to the renderer.

Run: python3 -m unittest discover -s tests
"""

import contextlib
import hashlib
import importlib.util
import io
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "mermaid_blocks.py"

# A node and an edge copied from a real `structurizr export -format mermaid`.
RAW_NODE = (
    """1["<div style='font-weight: bold'>Claimant</div>"""
    """<div style='font-size: 70%; margin-top: 0px'>[Person]</div>"""
    """<div style='font-size: 80%; margin-top:10px'>Reports a motor or property"""
    """<br />loss and follows the claim. A<br />synthetic persona in the"""
    """<br />demo.</div>"]"""
)
RAW_EDGE = (
    """1-- "<div>Submits claims and checks<br />their status through</div>"""
    """<div style='font-size: 70%'>[HTTPS]</div>" -->10"""
)
CLEAN_NODE = (
    '1["Claimant<br/>[Person]<br/>Reports a motor or property<br/>'
    'loss and follows the claim. A<br/>synthetic persona in the<br/>demo."]'
)
CLEAN_EDGE = (
    '1-- "Submits claims and checks<br/>their status through<br/>[HTTPS]" -->10'
)

RAW_VIEW = f"""graph LR
  linkStyle default fill:#ffffff

  subgraph diagram ["System Context View: Claims"]
    style diagram fill:#ffffff,stroke:#ffffff

    {RAW_NODE}
    style 1 fill:#ede9fe,stroke:#2563eb,color:#1f2937

    {RAW_EDGE}
  end"""  # Structurizr writes no newline after `end`.


def load_module():
    spec = importlib.util.spec_from_file_location("mermaid_blocks", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


mb = load_module()


class Normalise(unittest.TestCase):
    """GitHub-safe labels: plain lines joined by <br/>, nothing else changed."""

    def test_node_label_from_a_real_export(self):
        self.assertEqual(mb.normalise(RAW_NODE), CLEAN_NODE + "\n")

    def test_edge_label_from_a_real_export(self):
        self.assertEqual(mb.normalise(RAW_EDGE), CLEAN_EDGE + "\n")

    def test_no_styled_markup_survives_in_a_whole_view(self):
        text = mb.normalise(RAW_VIEW)
        for leftover in ("<div", "</div>", "<br />", "style='"):
            self.assertNotIn(leftover, text)
        self.assertIn(CLEAN_NODE, text)
        self.assertIn(CLEAN_EDGE, text)

    def test_colours_and_structure_are_kept_as_exported(self):
        text = mb.normalise(RAW_VIEW)
        for kept in (
            "graph LR",
            "  linkStyle default fill:#ffffff",
            '  subgraph diagram ["System Context View: Claims"]',
            "    style diagram fill:#ffffff,stroke:#ffffff",
            "    style 1 fill:#ede9fe,stroke:#2563eb,color:#1f2937",
            "  end",
        ):
            self.assertIn(kept + "\n", text)

    def test_ends_with_exactly_one_newline(self):
        self.assertTrue(mb.normalise(RAW_VIEW).endswith("  end\n"))
        self.assertTrue(mb.normalise("graph LR\n\n\n").endswith("graph LR\n"))
        self.assertFalse(mb.normalise("graph LR\n\n\n").endswith("\n\n"))

    def test_repeated_breaks_collapse_and_the_last_one_before_the_quote_goes(self):
        self.assertEqual(
            mb.normalise('a["<div>One</div><br /><br /><div>Two</div>"]'),
            'a["One<br/>Two"]\n',
        )

    def test_is_deterministic_and_idempotent(self):
        once = mb.normalise(RAW_VIEW)
        self.assertEqual(once, mb.normalise(RAW_VIEW))
        self.assertEqual(mb.normalise(once), once)


class Tree(unittest.TestCase):
    """A throwaway repository with an export, some Markdown and output folders."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.export = self.root / "generated" / "mermaid-export"
        self.views = self.root / "generated" / "mermaid-views"
        self.render = self.root / "generated" / "mermaid-render"

    def write(self, name, body):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(body).lstrip("\n"), encoding="utf-8")
        return path

    def export_view(self, key, text=RAW_VIEW):
        path = self.export / f"structurizr-{key}.mmd"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def markdown(self):
        return sorted(self.root.rglob("*.md"))

    def run_sync(self):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            status = mb.sync(self.export, self.views, self.markdown(), self.root)
        return status, out.getvalue(), err.getvalue()

    def run_extract(self):
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            status = mb.extract(self.markdown(), self.render, self.root)
        return status, out.getvalue()


class Sync(Tree):
    README = """
        # Architecture

        Context at a glance.

        <!-- mermaid-view: SystemContext -->
        ```mermaid
        flowchart LR
          pending["Run make mermaid-views"]
        ```

        After the diagram.
        """

    def setUp(self):
        super().setUp()
        self.export_view("SystemContext")
        self.readme = self.write("docs/architecture/README.md", self.README)

    def test_placeholder_is_replaced_by_the_normalised_view(self):
        status, _, _ = self.run_sync()
        self.assertEqual(status, 0)
        self.assertEqual(
            self.readme.read_text(encoding="utf-8"),
            "# Architecture\n\nContext at a glance.\n\n"
            "<!-- mermaid-view: SystemContext -->\n```mermaid\n"
            + mb.normalise(RAW_VIEW)
            + "```\n\nAfter the diagram.\n",
        )

    def test_the_view_is_written_beside_for_the_checker_to_compare(self):
        self.run_sync()
        written = (self.views / "SystemContext.mmd").read_text(encoding="utf-8")
        self.assertEqual(written, mb.normalise(RAW_VIEW))

    def test_block_body_is_what_the_checker_compares_it_with(self):
        """check_mermaid: the body equals the view file, minus one newline."""
        self.run_sync()
        text = self.readme.read_text(encoding="utf-8")
        ((fence, key),) = mb.checker.mermaid_fences(text)[0]
        self.assertEqual(key, "SystemContext")
        written = (self.views / "SystemContext.mmd").read_text(encoding="utf-8")
        self.assertEqual("\n".join(fence.body), written.removesuffix("\n"))

    def test_second_run_changes_nothing(self):
        self.run_sync()
        files = [self.readme, self.views / "SystemContext.mmd"]
        before = [(p.read_bytes(), p.stat().st_mtime_ns) for p in files]
        status, out, _ = self.run_sync()
        self.assertEqual(status, 0)
        self.assertEqual(
            [(p.read_bytes(), p.stat().st_mtime_ns) for p in files], before
        )
        self.assertIn("0 derived blocks rewritten in 0 files", out)

    def test_a_hand_edited_body_is_overwritten(self):
        self.run_sync()
        text = self.readme.read_text(encoding="utf-8")
        self.readme.write_text(text.replace("graph LR", "graph TD"), encoding="utf-8")
        self.run_sync()
        self.assertIn("graph LR", self.readme.read_text(encoding="utf-8"))

    def test_a_changed_export_updates_the_block(self):
        self.run_sync()
        self.export_view("SystemContext", RAW_VIEW.replace("Claimant", "Policyholder"))
        self.run_sync()
        text = self.readme.read_text(encoding="utf-8")
        self.assertIn("Policyholder", text)
        self.assertNotIn("Claimant", text)

    def test_every_derived_block_in_every_file_is_filled(self):
        self.export_view("Containers", RAW_VIEW.replace("Claimant", "Portal"))
        other = self.write(
            "docs/other/README.md",
            """
            <!-- mermaid-view: Containers -->
            ```mermaid
            flowchart LR
            ```

            <!-- mermaid-view: SystemContext -->
            ```mermaid
            flowchart LR
            ```
            """,
        )
        self.run_sync()
        text = other.read_text(encoding="utf-8")
        self.assertEqual(text.count("graph LR"), 2)
        self.assertIn("Portal", text)
        self.assertIn("Claimant", text)

    def test_hand_written_diagrams_are_left_alone(self):
        page = self.write(
            "docs/notes.md",
            """
            ```mermaid
            stateDiagram-v2
                [*] --> Pending
            ```
            """,
        )
        before = page.read_bytes()
        self.run_sync()
        self.assertEqual(page.read_bytes(), before)

    def test_missing_export_names_the_file_and_the_key(self):
        self.write(
            "docs/other/README.md",
            "<!-- mermaid-view: Containers -->\n```mermaid\nflowchart LR\n```\n",
        )
        before = self.readme.read_bytes()
        status, _, err = self.run_sync()
        self.assertEqual(status, 1)
        self.assertIn("structurizr-Containers.mmd", err)
        self.assertIn("'Containers'", err)
        self.assertIn("docs/other/README.md:1", err)
        # Nothing is half done: the block that could be filled was not.
        self.assertEqual(self.readme.read_bytes(), before)

    def test_without_derived_blocks_and_without_an_export_it_is_a_no_op(self):
        for path in self.export.iterdir():
            path.unlink()
        self.write("docs/notes.md", "No diagrams here.\n")
        self.readme.write_text("# Architecture\n", encoding="utf-8")
        status, _, _ = self.run_sync()
        self.assertEqual(status, 0)

    def test_a_view_the_model_dropped_is_removed_from_the_views_folder(self):
        self.run_sync()
        stale = self.views / "Retired.mmd"
        stale.write_text("graph LR\n", encoding="utf-8")
        self.run_sync()
        self.assertFalse(stale.exists())
        self.assertTrue((self.views / "SystemContext.mmd").exists())

    def test_no_export_at_all_removes_nothing(self):
        self.run_sync()
        for path in self.export.iterdir():
            path.unlink()
        self.readme.write_text("# Architecture\n", encoding="utf-8")
        self.run_sync()
        self.assertTrue((self.views / "SystemContext.mmd").exists())


class Extract(Tree):
    def digest(self, source):
        return hashlib.sha256(source.encode("utf-8")).hexdigest()[:12]

    def rows(self):
        text = (self.render / "index.tsv").read_text(encoding="utf-8")
        return [line.split("\t") for line in text.splitlines()]

    def test_every_fence_becomes_a_file_named_by_the_hash_of_its_source(self):
        self.write(
            "docs/a.md",
            """
            Text.

            ```mermaid
            stateDiagram-v2
                [*] --> Pending
            ```
            """,
        )
        status, _ = self.run_extract()
        self.assertEqual(status, 0)
        source = "stateDiagram-v2\n    [*] --> Pending\n"
        written = self.render / f"{self.digest(source)}.mmd"
        self.assertEqual(written.read_text(encoding="utf-8"), source)

    def test_index_maps_each_hash_to_file_and_opening_line(self):
        self.write("docs/a.md", "Text.\n\n```mermaid\nflowchart LR\n  a --> b\n```\n")
        self.write("README.md", "~~~mermaid\nsequenceDiagram\n  A->>B: hi\n~~~\n")
        self.run_extract()
        self.assertEqual(
            self.rows(),
            [
                [self.digest("sequenceDiagram\n  A->>B: hi\n"), "README.md", "1"],
                [self.digest("flowchart LR\n  a --> b\n"), "docs/a.md", "3"],
            ],
        )

    def test_identical_sources_share_a_file_but_keep_both_index_rows(self):
        block = "```mermaid\nflowchart LR\n  a --> b\n```\n"
        self.write("docs/a.md", block)
        self.write("docs/b.md", block)
        _, out = self.run_extract()
        self.assertEqual(len(list(self.render.glob("*.mmd"))), 1)
        self.assertEqual([row[1] for row in self.rows()], ["docs/a.md", "docs/b.md"])
        self.assertIn("2 Mermaid blocks (1 distinct)", out)

    def test_derived_blocks_are_extracted_too(self):
        """Their labels are what GitHub renders, so they get the syntax check."""
        self.write(
            "docs/architecture/README.md",
            "<!-- mermaid-view: Key -->\n```mermaid\nflowchart LR\n```\n",
        )
        self.run_extract()
        self.assertEqual(self.rows()[0][1:], ["docs/architecture/README.md", "2"])

    def test_other_languages_and_unclosed_fences_are_skipped(self):
        self.write("docs/a.md", "```bash\necho hi\n```\n\n```mermaid\nflowchart LR\n")
        self.run_extract()
        self.assertEqual(list(self.render.glob("*.mmd")), [])
        self.assertEqual(self.rows(), [])

    def test_a_fence_inside_a_longer_fence_is_an_example_not_a_diagram(self):
        self.write(
            "docs/a.md",
            "````md\n```mermaid\nflowchart LR\n```\n\n"
            "```mermaid\nflowchart TD\n```\n````\n",
        )
        self.run_extract()
        self.assertEqual(self.rows(), [])

    def test_previous_output_is_cleared_first(self):
        self.render.mkdir(parents=True)
        for name in ("deadbeef0000.mmd", "deadbeef0000.png"):
            (self.render / name).write_bytes(b"old")
        (self.render / "index.tsv").write_text("deadbeef0000\told.md\t1\n")
        self.write("docs/a.md", "```mermaid\nflowchart LR\n```\n")
        self.run_extract()
        kept = self.digest("flowchart LR\n")
        self.assertEqual(
            sorted(p.name for p in self.render.iterdir()),
            sorted([f"{kept}.mmd", "index.tsv"]),
        )

    def test_no_fences_leaves_an_empty_index_and_no_diagrams(self):
        self.write("docs/a.md", "Just prose.\n")
        status, _ = self.run_extract()
        self.assertEqual(status, 0)
        self.assertEqual(list(self.render.glob("*.mmd")), [])
        self.assertEqual((self.render / "index.tsv").read_text(), "")


class CommandLine(Tree):
    """main() wires the subcommands to the repository's folders."""

    def patched(self):
        return mock.patch.multiple(
            mb,
            REPO=self.root,
            EXPORT_DIR=self.export,
            VIEWS_DIR=self.views,
            RENDER_DIR=self.render,
        )

    def test_extract_and_sync_run_through_main(self):
        self.export_view("SystemContext")
        readme = self.write(
            "README.md",
            "<!-- mermaid-view: SystemContext -->\n```mermaid\nflowchart LR\n```\n",
        )
        with (
            self.patched(),
            mock.patch.object(mb.checker, "markdown_files", self.markdown),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            self.assertEqual(mb.main(["sync"]), 0)
            self.assertEqual(mb.main(["extract"]), 0)
        self.assertIn("graph LR", readme.read_text(encoding="utf-8"))
        self.assertEqual(len(list(self.render.glob("*.mmd"))), 1)

    def test_a_subcommand_is_required(self):
        with (
            contextlib.redirect_stderr(io.StringIO()),
            self.assertRaises(SystemExit) as raised,
        ):
            mb.main([])
        self.assertEqual(raised.exception.code, 2)


if __name__ == "__main__":
    unittest.main()
