# SPDX-License-Identifier: AGPL-3.0-only
"""The release version must agree across the tag, CHANGELOG and Debian package."""
import importlib.machinery
import importlib.util
import shutil
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
loader = importlib.machinery.SourceFileLoader(
    "release_version", str(ROOT / "scripts/release-version.py"))
spec = importlib.util.spec_from_loader(loader.name, loader)
release = importlib.util.module_from_spec(spec)
loader.exec_module(release)

CHANGELOG = """# Changelog

## [Unreleased]

> **Maintainers:** Leave this section empty.

## [0.1.6-alpha1] — 2026-09-10

### Minor Feature

- Something.

## [0.1.5-alpha1] — 2026-09-09

### Bug Fixes

- Something older.
"""


class ResolveTests(unittest.TestCase):
    def test_newest_version_resolves_to_a_debian_prerelease(self):
        tag, deb = release.resolve("v0.1.6-alpha1", CHANGELOG)
        self.assertEqual(tag, "0.1.6-alpha1")
        # '~' sorts below the final release; '-' would sort above it.
        self.assertEqual(deb, "0.1.6~alpha1")

    def test_final_release_is_unchanged(self):
        self.assertEqual(release.debian_version("0.1.6"), "0.1.6")

    def test_prerelease_sorts_below_its_release(self):
        # The property the '~' conversion exists for. This is dpkg's ordering, not
        # lexical ordering: '~' is 0x7E, so a string compare gives the opposite answer.
        self.assertEqual(release.debian_version("0.2.0-rc.1"), "0.2.0~rc.1")
        if not shutil.which("dpkg"):
            self.skipTest("dpkg required to check Debian version ordering")
        for lower, higher in (("0.1.6-alpha1", "0.1.6"), ("0.1.6-alpha1", "0.1.6-beta1"),
                              ("0.1.5", "0.1.6-alpha1")):
            with self.subTest(lower=lower, higher=higher):
                result = subprocess.run(
                    ["dpkg", "--compare-versions", release.debian_version(lower),
                     "lt", release.debian_version(higher)], check=False)
                self.assertEqual(result.returncode, 0,
                                 f"{lower} must sort below {higher}")

    def test_tag_absent_from_changelog_is_refused(self):
        with self.assertRaisesRegex(release.ReleaseVersionError, "no '## \\[0.9.9\\]' section"):
            release.resolve("v0.9.9", CHANGELOG)

    def test_releasing_an_older_version_is_refused(self):
        # Publishing 0.1.5 after 0.1.6 would sort below what hosts already have.
        with self.assertRaisesRegex(release.ReleaseVersionError, "not the newest"):
            release.resolve("v0.1.5-alpha1", CHANGELOG)

    def test_malformed_tag_is_refused(self):
        for tag in ("0.1.6", "v0.1", "release-0.1.6", "v0.1.6.7", ""):
            with self.subTest(tag=tag):
                with self.assertRaises(release.ReleaseVersionError):
                    release.resolve(tag, CHANGELOG)

    def test_unreleased_entries_block_the_release(self):
        dirty = CHANGELOG.replace("> **Maintainers:** Leave this section empty.",
                                  "- An entry someone forgot to move.")
        with self.assertRaisesRegex(release.ReleaseVersionError, r"\[Unreleased\] still has entries"):
            release.resolve("v0.1.6-alpha1", dirty)

    def test_blockquote_guidance_is_not_an_entry(self):
        tag, _ = release.resolve("v0.1.6-alpha1", CHANGELOG)
        self.assertEqual(tag, "0.1.6-alpha1")

    def test_real_changelog_parses_and_is_ordered_newest_first(self):
        versions = release.changelog_versions((ROOT / "CHANGELOG.md").read_text())
        self.assertIn("0.1.5-alpha1", versions)
        self.assertEqual(versions[0], "0.2.2")
        self.assertNotIn("Unreleased", versions)


class CliTests(unittest.TestCase):
    def test_cli_reports_failure_without_printing_a_version(self):
        import io
        from contextlib import redirect_stderr, redirect_stdout
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = release.main(["--tag", "v9.9.9", "--changelog", str(ROOT / "CHANGELOG.md")])
        self.assertEqual(code, 1)
        self.assertEqual(out.getvalue(), "", "a rejected tag must not print a version")
        self.assertIn("release-version:", err.getvalue())


if __name__ == "__main__":
    unittest.main()
