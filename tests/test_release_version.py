# SPDX-License-Identifier: AGPL-3.0-only
"""The release version must agree across the tag, CHANGELOG and Debian package."""
import importlib.machinery
import importlib.util
import re
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

> **Maintainers / agents:** Put new entries here until a release is cut.

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
        dirty = CHANGELOG.replace(
            "> **Maintainers / agents:** Put new entries here until a release is cut.",
            "- An entry someone forgot to move.")
        with self.assertRaisesRegex(release.ReleaseVersionError, r"\[Unreleased\] still has entries"):
            release.resolve("v0.1.6-alpha1", dirty)

    def test_blockquote_guidance_is_not_an_entry(self):
        tag, _ = release.resolve("v0.1.6-alpha1", CHANGELOG)
        self.assertEqual(tag, "0.1.6-alpha1")

    def test_real_changelog_parses_and_is_ordered_newest_first(self):
        text = (ROOT / "CHANGELOG.md").read_text()
        versions = release.changelog_versions(text)
        self.assertIn("0.1.5-alpha1", versions)
        self.assertNotIn("Unreleased", versions)
        # The newest heading is whatever a build would use. Pinning the literal here only
        # made every release fail this test for the one reason that is not a defect, so
        # assert the property the test is named for instead: the file is ordered, and the
        # head of it is what release-version reports.
        self.assertEqual(versions[0], release.current(text)[0])
        ordered = sorted(versions, key=lambda v: tuple(
            int(part) if part.isdigit() else -1
            for part in re.split(r"[.-]", v)), reverse=True)
        self.assertEqual(versions, ordered)


class DevelopmentBuildTests(unittest.TestCase):
    """Builds of the dev branch are pre-releases of the release main publishes next."""

    def dpkg_lt(self, lower, higher):
        return subprocess.run(["dpkg", "--compare-versions", lower, "lt", higher],
                              check=False).returncode == 0

    def test_build_after_a_published_release_numbers_the_next_patch(self):
        # dev's newest heading is already on main, so the build works toward 0.1.7.
        version = release.dev_version(CHANGELOG, CHANGELOG, 231)
        self.assertEqual(version, "0.1.7-dev.231")
        self.assertEqual(release.debian_version(version), "0.1.7~dev.231")
        if not shutil.which("dpkg"):
            self.skipTest("dpkg required to check Debian version ordering")
        deb = release.debian_version(version)
        self.assertTrue(self.dpkg_lt("0.1.6-alpha1".replace("-", "~"), deb), "above the published release")
        self.assertTrue(self.dpkg_lt(deb, "0.1.7"), "below the release it works toward")
        self.assertTrue(self.dpkg_lt(deb, release.debian_version("0.1.7-dev.1000")), "build numbers compare as numbers")

    def test_heading_cut_on_dev_is_the_release_being_built(self):
        cut = CHANGELOG.replace("## [0.1.6-alpha1]", "## [0.1.7] — 2026-09-11\n\n- Cut.\n\n## [0.1.6-alpha1]")
        # main still stops at 0.1.6-alpha1: dev builds below 0.1.7 so the stable 0.1.7
        # published at promotion sorts above every build made while it waited.
        self.assertEqual(release.dev_version(cut, CHANGELOG, 5), "0.1.7-dev.5")

    def test_main_ahead_of_dev_is_refused(self):
        cut = CHANGELOG.replace("## [0.1.6-alpha1]", "## [0.1.7] — 2026-09-11\n\n- Cut.\n\n## [0.1.6-alpha1]")
        with self.assertRaisesRegex(release.ReleaseVersionError, "merge main into dev"):
            release.dev_version(CHANGELOG, cut, 5)

    def test_prerelease_heading_converts_every_dash(self):
        self.assertEqual(release.debian_version("0.2.0-rc.1-dev.7"), "0.2.0~rc.1~dev.7")

    def test_notes_are_the_unreleased_entries_without_the_maintainer_block(self):
        text = CHANGELOG.replace(
            "> **Maintainers / agents:** Put new entries here until a release is cut.",
            "> **Maintainers / agents:** Put new entries here until a release is cut.\n\n- Staged work.")
        self.assertEqual(release.unreleased_notes(text), "- Staged work.\n")
        self.assertEqual(release.unreleased_notes(CHANGELOG),
                         "No changes since the last stable release.\n")

    def test_cli_needs_the_build_number_and_the_stable_changelog(self):
        import io
        from contextlib import redirect_stderr
        with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
            release.main(["--current", "--channel", "dev"])


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
