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
        for newer, older in zip(versions, versions[1:]):
            self.assertFalse(release.newer_than(older, newer), f"{older} precedes {newer}")


class DevelopmentBuildTests(unittest.TestCase):
    """Builds of the dev branch are pre-releases of the release main publishes next."""

    def dpkg_lt(self, lower, higher):
        return subprocess.run(["dpkg", "--compare-versions", lower, "lt", higher],
                              check=False).returncode == 0

    def test_build_after_a_published_release_numbers_the_next_patch(self):
        # dev's newest heading is already on main, so the build works toward 0.1.7.
        version = release.dev_version(CHANGELOG, CHANGELOG, 231)
        self.assertEqual(version, "0.1.7-dev.231")
        self.assertEqual(release.debian_version(version), "0.1.7~~dev.231")
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

    def test_development_builds_sort_below_next_stable_prereleases_for_any_counter(self):
        if not shutil.which("dpkg"):
            self.skipTest("dpkg required to check Debian version ordering")
        stable = CHANGELOG.replace("0.1.6-alpha1", "0.2.0")
        for build in (0, 1, 100, 999, 1000, 10 ** 100):
            version = release.debian_version(release.dev_version(stable, stable, build))
            with self.subTest(build=build):
                self.assertTrue(self.dpkg_lt("0.2.0", version))
            for prerelease in ("0", "alpha1", "alpha10", "beta1", "rc9", "rc10", "rc.10"):
                with self.subTest(build=build, prerelease=prerelease):
                    self.assertTrue(self.dpkg_lt(version, release.debian_version(f"0.2.1-{prerelease}")))

    def test_cutting_alpha_rc_and_final_keeps_development_builds_monotonic(self):
        if not shutil.which("dpkg"):
            self.skipTest("dpkg required to check Debian version ordering")
        stable = CHANGELOG.replace("0.1.6-alpha1", "0.2.0")
        previous = release.debian_version(release.dev_version(stable, stable, 100))
        for build, heading in enumerate(("0.2.1-alpha1", "0.2.1-rc9", "0.2.1-rc10", "0.2.1"), 101):
            cut = stable.replace("## [0.2.0]", f"## [{heading}]\n\n- Cut.\n\n## [0.2.0]")
            with self.subTest(heading=heading):
                version = release.dev_version(cut, stable, build)
                self.assertEqual(version, f"0.2.1-dev.{build}")
                deb = release.debian_version(version)
                self.assertTrue(self.dpkg_lt(previous, deb), "cutting a heading must not downgrade development")
                self.assertTrue(self.dpkg_lt(deb, release.debian_version(heading)))
                previous = deb

    def test_publishing_a_prerelease_advances_development_to_the_next_patch(self):
        if not shutil.which("dpkg"):
            self.skipTest("dpkg required to check Debian version ordering")
        for heading in ("0.2.1-alpha1", "0.2.1-rc10", "0.2.1"):
            published = CHANGELOG.replace("0.1.6-alpha1", heading)
            with self.subTest(heading=heading):
                version = release.dev_version(published, published, 102)
                self.assertEqual(version, "0.2.2-dev.102")
                self.assertTrue(self.dpkg_lt(release.debian_version(heading), release.debian_version(version)))

    def test_main_numerically_newer_prerelease_is_refused(self):
        for lower, higher in (("rc9", "rc10"), ("rc.9", "rc.10"), ("alpha99", "alpha100")):
            dev = CHANGELOG.replace("0.1.6-alpha1", f"0.2.0-{lower}")
            main = CHANGELOG.replace("0.1.6-alpha1", f"0.2.0-{higher}")
            with self.subTest(lower=lower, higher=higher):
                with self.assertRaisesRegex(release.ReleaseVersionError, "merge main into dev"):
                    release.dev_version(dev, main, 5)

    def test_dev_numerically_newer_prerelease_notes_are_kept_but_same_base_build_is_refused(self):
        for lower, higher in (("rc9", "rc10"), ("rc.9", "rc.10"), ("alpha99", "alpha100")):
            main = CHANGELOG.replace("0.1.6-alpha1", f"0.2.0-{lower}")
            dev = CHANGELOG.replace("0.1.6-alpha1", f"0.2.0-{higher}")
            with self.subTest(lower=lower, higher=higher):
                with self.assertRaisesRegex(release.ReleaseVersionError, "development version .* below main"):
                    release.dev_version(dev, main, 6)
                self.assertEqual(release.dev_notes(dev, main), "### Minor Feature\n\n- Something.\n")

    def test_cli_accepts_zero_and_arbitrarily_large_build_counters(self):
        import io
        import tempfile
        from contextlib import redirect_stdout
        with tempfile.TemporaryDirectory() as tmp:
            changelog = Path(tmp) / "CHANGELOG.md"
            changelog.write_text(CHANGELOG)
            for build in (0, 10 ** 100):
                with self.subTest(build=build):
                    output = io.StringIO()
                    with redirect_stdout(output):
                        result = release.main([
                            "--current", "--channel", "dev", "--build", str(build),
                            "--changelog", str(changelog), "--stable-changelog", str(changelog),
                            "--format", "both"])
                    self.assertEqual(result, 0)
                    self.assertEqual(output.getvalue(), f"0.1.7-dev.{build} 0.1.7~~dev.{build}\n")

    def test_notes_are_the_unreleased_entries_without_the_maintainer_block(self):
        text = CHANGELOG.replace(
            "> **Maintainers / agents:** Put new entries here until a release is cut.",
            "> **Maintainers / agents:** Put new entries here until a release is cut.\n\n- Staged work.")
        self.assertEqual(release.unreleased_notes(text), "- Staged work.\n")
        self.assertEqual(release.unreleased_notes(CHANGELOG),
                         "No changes since the last stable release.\n")

    def test_notes_of_a_heading_cut_on_dev_are_that_headings_entries(self):
        cut = CHANGELOG.replace("## [0.1.6-alpha1]", "## [0.1.7] — 2026-09-11\n\n- Cut entry.\n\n## [0.1.6-alpha1]")
        self.assertEqual(release.dev_notes(cut, CHANGELOG), "- Cut entry.\n")
        # Entries added to [Unreleased] after the cut ship in the same builds.
        staged = cut.replace(
            "> **Maintainers / agents:** Put new entries here until a release is cut.",
            "> **Maintainers / agents:** Put new entries here until a release is cut.\n\n- Staged work.")
        self.assertEqual(release.dev_notes(staged, CHANGELOG), "- Cut entry.\n\n- Staged work.\n")
        # Once main has published the heading, only [Unreleased] is new.
        self.assertEqual(release.dev_notes(CHANGELOG, CHANGELOG),
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
