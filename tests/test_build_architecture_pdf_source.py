"""Tests for the Mermaid handling in scripts/build_architecture_pdf_source.py.

The PDF is what people are handed, and Pandoc cannot draw Mermaid. So the
builder turns every diagram into an image the renderer makes: a fence in a page
becomes `![](generated/mermaid-pdf/<sha12>.png)` with its source saved beside,
and so does the image link Structurizr's Mermaid plugin leaves in ADRs, which
has to be decoded back to source first. A link that cannot be decoded must stop
the build: the alternative is a PDF with a hole where the diagram belongs.

Only pure functions are tested; nothing here starts Docker or Pandoc.

Run: python3 -m unittest discover -s tests
"""

import base64
import hashlib
import importlib.util
import json
import os
import subprocess
import tempfile
import unittest
import zlib
from pathlib import Path

SCRIPT = (
    Path(__file__).resolve().parents[1] / "scripts" / "build_architecture_pdf_source.py"
)

URL = "https://mermaid.ink"
SOURCE = "stateDiagram-v2\n    [*] --> Running\n    Running --> Done\n"
# Written by Structurizr's plugin with mermaid.url https://mermaid.ink,
# mermaid.format svg and mermaid.compress false, from SOURCE.
PLAIN_PAYLOAD = (
    "c3RhdGVEaWFncmFtLXYyCiAgICBbKl0gLS0-IFJ1bm5pbmcKICAgIFJ1bm5pbmcgLS0-IERvbmUK"
)
PLAIN_LINK = f"![]({URL}/svg/{PLAIN_PAYLOAD})"


def load_builder():
    spec = importlib.util.spec_from_file_location("build_pdf_source", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


build = load_builder()


def pako_payload(source):
    """What the plugin writes with mermaid.compress true."""
    document = json.dumps({"code": source, "mermaid": {"theme": "default"}})
    packed = zlib.compress(document.encode("utf-8"))
    return "pako:" + base64.urlsafe_b64encode(packed).decode("ascii").rstrip("=")


def digest(source):
    return hashlib.sha256(source.encode("utf-8")).hexdigest()[:12]


class DecodePluginLink(unittest.TestCase):
    def test_plain_link_from_the_plugin_decodes_to_the_source(self):
        self.assertEqual(build.decode_plugin_link(PLAIN_PAYLOAD), SOURCE)

    def test_the_fixture_is_what_the_plugin_would_write(self):
        encoded = base64.urlsafe_b64encode(SOURCE.encode("utf-8")).decode("ascii")
        self.assertEqual(encoded, PLAIN_PAYLOAD)

    def test_pako_link_decodes_to_the_source(self):
        self.assertEqual(build.decode_plugin_link(pako_payload(SOURCE)), SOURCE)

    def test_padding_is_optional(self):
        padded = base64.urlsafe_b64encode(b"graph LR\n  a-->b").decode("ascii")
        self.assertTrue(padded.endswith("="))
        self.assertEqual(build.decode_plugin_link(padded), "graph LR\n  a-->b")
        self.assertEqual(
            build.decode_plugin_link(padded.rstrip("=")), "graph LR\n  a-->b"
        )

    def test_non_ascii_source_survives(self):
        source = 'flowchart LR\n  a["Kéri a fizetést"] --> b\n'
        self.assertEqual(build.decode_plugin_link(pako_payload(source)), source)


class UndecodableLinks(unittest.TestCase):
    """Each of these must stop the build with a message, not emit an image."""

    def assertStops(self, payload):
        with self.assertRaises(SystemExit) as raised:
            build.decode_plugin_link(payload)
        message = str(raised.exception)
        self.assertIn("cannot decode the Mermaid link", message)
        self.assertIn("mermaid.compress", message)
        return message

    def test_not_base64(self):
        self.assertStops("!!not base64!!")

    def test_base64_of_bytes_that_are_not_text(self):
        self.assertStops(base64.urlsafe_b64encode(b"\xff\xfe\xfd").decode("ascii"))

    def test_empty_source(self):
        self.assertStops(base64.urlsafe_b64encode(b"   \n").decode("ascii"))

    def test_pako_prefix_on_a_payload_that_is_not_compressed(self):
        self.assertStops("pako:" + PLAIN_PAYLOAD)

    def test_pako_without_a_code_field(self):
        packed = zlib.compress(json.dumps({"mermaid": {}}).encode("utf-8"))
        self.assertStops("pako:" + base64.urlsafe_b64encode(packed).decode("ascii"))

    def test_pako_that_is_not_json(self):
        packed = zlib.compress(b"not json")
        self.assertStops("pako:" + base64.urlsafe_b64encode(packed).decode("ascii"))

    def test_a_long_link_is_shortened_in_the_message(self):
        message = self.assertStops("!" * 200)
        self.assertNotIn("!" * 100, message)


class MermaidUrl(unittest.TestCase):
    def test_read_from_the_views_configuration_properties(self):
        workspace = {"views": {"configuration": {"properties": {"mermaid.url": URL}}}}
        self.assertEqual(build.mermaid_url(workspace), URL)

    def test_trailing_slash_is_dropped(self):
        workspace = {
            "views": {"configuration": {"properties": {"mermaid.url": URL + "/"}}}
        }
        self.assertEqual(build.mermaid_url(workspace), URL)

    def test_absent_means_the_plugin_is_off(self):
        for workspace in (
            {},
            {"views": {}},
            {"views": {"configuration": {}}},
            {"views": {"configuration": {"properties": {}}}},
            {"views": {"configuration": {"properties": {"mermaid.url": ""}}}},
        ):
            with self.subTest(workspace=workspace):
                self.assertIsNone(build.mermaid_url(workspace))


class Images(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.arch = Path(self.tmp.name).resolve()
        self.generated = self.arch / "generated"
        self.generated.mkdir()
        self.folder = self.generated / "mermaid-pdf"

    def sources(self):
        return {p.name: p.read_text() for p in sorted(self.folder.glob("*.mmd"))}

    def image_line(self, source):
        return f"![](generated/mermaid-pdf/{digest(source)}.png)"

    # -- fences ---------------------------------------------------------------

    def test_a_fence_becomes_an_image_line_and_a_saved_source(self):
        lines = ["Text.", "", "```mermaid", *SOURCE.splitlines(), "```", "", "After."]
        out = build.mermaid_images(lines, self.generated, None)
        self.assertEqual(
            out, ["Text.", "", "", self.image_line(SOURCE), "", "", "After."]
        )
        self.assertEqual(self.sources(), {f"{digest(SOURCE)}.mmd": SOURCE})

    def test_the_image_gets_a_paragraph_to_itself(self):
        """A fence may sit right under a sentence; an inline image is no figure."""
        out = build.mermaid_images(
            ["A sentence.", "```mermaid", "graph LR", "```"], self.generated, None
        )
        self.assertEqual(out[:3], ["A sentence.", "", out[2]])
        self.assertTrue(out[2].startswith("![](generated/mermaid-pdf/"))
        self.assertEqual(out[3], "")

    def test_tilde_fence_and_uppercase_info_string(self):
        out = build.mermaid_images(
            ["~~~Mermaid", "graph LR", "~~~"], self.generated, None
        )
        self.assertIn(self.image_line("graph LR\n"), out)

    def test_two_fences_with_the_same_source_share_one_file(self):
        block = ["```mermaid", "graph LR", "```"]
        out = build.mermaid_images([*block, "", *block], self.generated, None)
        self.assertEqual(out.count(self.image_line("graph LR\n")), 2)
        self.assertEqual(len(self.sources()), 1)

    def test_other_fences_pass_through_untouched(self):
        lines = ["```bash", "echo hi", "```", "", "~~~", "plain", "~~~"]
        self.assertEqual(build.mermaid_images(lines, self.generated, None), lines)
        self.assertFalse(self.folder.exists())

    def test_a_longer_fence_can_show_a_mermaid_example(self):
        lines = [
            "````markdown",
            "```mermaid",
            "graph LR",
            "```",
            "",
            "```mermaid",
            "graph TD",
            "```",
            "````",
        ]
        self.assertEqual(build.mermaid_images(lines, self.generated, None), lines)
        self.assertFalse(self.folder.exists())

    def test_inline_code_that_looks_like_a_fence_is_text(self):
        lines = ["```mermaid```", "still prose"]
        self.assertEqual(build.mermaid_images(lines, self.generated, None), lines)

    def test_an_indented_fence_keeps_its_indent(self):
        out = build.mermaid_images(
            ["- item", "  ```mermaid", "  graph LR", "  ```"], self.generated, None
        )
        self.assertIn("  " + self.image_line("  graph LR\n"), out)

    def test_an_unclosed_mermaid_fence_stops_the_build(self):
        with self.assertRaises(SystemExit) as raised:
            build.mermaid_images(["```mermaid", "graph LR"], self.generated, None)
        self.assertIn("never closed", str(raised.exception))

    # -- plugin links ---------------------------------------------------------

    def test_a_plain_plugin_link_becomes_the_same_image_as_the_fence(self):
        out = build.mermaid_images(
            ["Before.", PLAIN_LINK, "After."], self.generated, URL
        )
        self.assertEqual(out, ["Before.", self.image_line(SOURCE), "After."])
        self.assertEqual(self.sources(), {f"{digest(SOURCE)}.mmd": SOURCE})

    def test_a_pako_plugin_link_decodes_too(self):
        link = f"![]({URL}/png/{pako_payload(SOURCE)})"
        out = build.mermaid_images([link], self.generated, URL)
        self.assertEqual(out, [self.image_line(SOURCE)])
        self.assertEqual(self.sources(), {f"{digest(SOURCE)}.mmd": SOURCE})

    def test_a_self_hosted_server_with_a_trailing_slash_and_a_path(self):
        url = "http://localhost:3000/mermaid"
        out = build.mermaid_images(
            [f"![]({url}/svg/{PLAIN_PAYLOAD})"], self.generated, url
        )
        self.assertEqual(out, [self.image_line(SOURCE)])

    def test_links_to_other_servers_are_not_ours_to_decode(self):
        lines = [
            "![logo](https://example.com/svg/abc)",
            f"![]({URL}/svg/{PLAIN_PAYLOAD})",
        ]
        out = build.mermaid_images(lines, self.generated, "https://kroki.io")
        self.assertEqual(out, lines)
        self.assertFalse(self.folder.exists())

    def test_without_a_configured_url_no_link_is_touched(self):
        self.assertEqual(
            build.mermaid_images([PLAIN_LINK], self.generated, None), [PLAIN_LINK]
        )

    def test_an_undecodable_link_stops_the_build(self):
        with self.assertRaises(SystemExit) as raised:
            build.mermaid_images([f"![]({URL}/svg/@@@)"], self.generated, URL)
        self.assertIn("cannot decode the Mermaid link", str(raised.exception))

    def test_a_link_shown_inside_a_code_fence_is_left_alone(self):
        lines = ["```markdown", PLAIN_LINK, "```"]
        self.assertEqual(build.mermaid_images(lines, self.generated, URL), lines)

    # -- the whole document ---------------------------------------------------

    def test_body_turns_fences_into_images_and_leaves_the_rest_as_before(self):
        lines = ["## Payment states", "", "```mermaid", "graph LR", "```", "", "Done."]
        text, embedded = build.body(lines, {}, self.generated)
        self.assertIn(self.image_line("graph LR\n"), text.splitlines())
        self.assertIn("# Payment states", text)
        self.assertEqual(embedded, set())

    def test_body_decodes_links_only_when_given_the_url(self):
        text, _ = build.body([PLAIN_LINK], {}, self.generated, mermaid=URL)
        self.assertEqual(text.strip(), self.image_line(SOURCE))
        text, _ = build.body([PLAIN_LINK], {}, self.generated)
        self.assertEqual(text.strip(), PLAIN_LINK)

    def test_body_without_mermaid_is_what_it_was(self):
        lines = [
            "## A",
            "",
            "See [the guide](docs/guide.md).",
            "",
            "```bash",
            "ls",
            "```",
        ]
        text, _ = build.body(lines, {}, self.generated)
        self.assertEqual(
            text.splitlines(),
            ["# A", "", "See the guide.", "", "```bash", "ls", "```"],
        )
        self.assertFalse(self.folder.exists())

    def test_adr_content_is_converted_through_decisions(self):
        workspace = {
            "documentation": {
                "decisions": [
                    {
                        "id": "1",
                        "format": "Markdown",
                        "content": f"# 1. Use it\n\n## Status\n\nAccepted\n\n{PLAIN_LINK}\n",
                    }
                ]
            }
        }
        text, _ = build.decisions(workspace, {}, self.generated, URL)
        self.assertIn(self.image_line(SOURCE), text.splitlines())

    def test_reset_empties_the_folder(self):
        build.mermaid_image("graph LR\n", self.generated)
        self.assertTrue(self.folder.exists())
        build.reset_mermaid(self.generated)
        self.assertFalse(self.folder.exists())
        build.reset_mermaid(self.generated)  # nothing there is not an error


class Edition(unittest.TestCase):
    """edition() names the PDF; a repository without commits must not crash it."""

    def setUp(self):
        self.cwd = os.getcwd()
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.addCleanup(os.chdir, self.cwd)
        os.chdir(self.tmp.name)
        self.arch = Path("docs/architecture")
        self.arch.mkdir(parents=True)
        (self.arch / "workspace.dsl").write_text("workspace {}\n")
        self.git("init", "--quiet")

    def git(self, *args):
        config = ["-c", "user.name=t", "-c", "user.email=t@example.com"]
        config += ["-c", "commit.gpgsign=false"]
        return subprocess.run(
            ["git", *config, *args], check=True, capture_output=True, text=True
        ).stdout.strip()

    def test_repository_without_commits_is_uncommitted(self):
        sha, dirty = build.edition(self.arch)
        self.assertEqual(sha, "uncommitted")
        self.assertTrue(dirty)  # the untracked model is what `git status` sees

    def test_single_commit_gives_a_seven_character_sha(self):
        self.git("add", "-A")
        self.git("commit", "--quiet", "-m", "model")
        sha, dirty = build.edition(self.arch)
        self.assertEqual(len(sha), 7)
        self.assertEqual(sha, self.git("rev-parse", "HEAD")[:7])
        self.assertFalse(dirty)


if __name__ == "__main__":
    unittest.main()
