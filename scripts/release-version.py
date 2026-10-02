#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Resolve the release version from a git tag and CHANGELOG.md.

One version identifies a Nex Core release: the git tag, the CHANGELOG heading and
the Debian package version are the same number in three notations. This refuses to
print a version unless they agree, so a release cannot ship stamped with a version
its changelog does not describe.

    v0.1.6-alpha1  ->  ## [0.1.6-alpha1]  ->  0.1.6~alpha1

Debian sorts '~' before everything, so a pre-release sorts below its final release;
'-' would sort above it and make 0.1.6-alpha1 look newer than 0.1.6.

The development channel (the `dev` branch) numbers a build as a pre-release of the release `main` will
publish next, `0.1.7-dev.231` for build 231, so it sorts below that release and above every release
already published. Releasing from `main` never needs the build number.
"""
import argparse
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
HEADING = re.compile(r"^## \[(?P<version>[^\]]+)\]")
PLACEHOLDER = "No changes since the last stable release."
TAG = re.compile(r"^v(?P<version>\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)$")
PRERELEASE = re.compile(r"^(?P<release>\d+\.\d+\.\d+)-(?P<pre>[0-9A-Za-z.-]+)$")


class ReleaseVersionError(RuntimeError):
    pass


def changelog_versions(text):
    """Every version heading, newest first, excluding the Unreleased placeholder."""
    versions = []
    for line in text.splitlines():
        match = HEADING.match(line)
        if match:
            version = match.group("version").strip()
            if version.lower() != "unreleased":
                versions.append(version)
    return versions


def unreleased_is_empty(text):
    """True when [Unreleased] has no release entries (blockquote guidance allowed).

    Day-to-day work accumulates under [Unreleased]. Before tagging a release those
    entries must move under the numbered heading being shipped.
    """
    lines = text.splitlines()
    try:
        start = next(i for i, line in enumerate(lines)
                     if HEADING.match(line) and "unreleased" in line.lower())
    except StopIteration:
        return True
    for line in lines[start + 1:]:
        if HEADING.match(line):
            return True
        stripped = line.strip()
        # The maintainer instruction block is a blockquote, not a release entry.
        if stripped and not stripped.startswith(">"):
            return False
    return True


def debian_version(version):
    match = PRERELEASE.match(version)
    if not match:
        return version
    return f"{match.group('release')}~{match.group('pre').replace('-', '~')}"


def resolve(tag, text):
    match = TAG.match(tag.strip())
    if not match:
        raise ReleaseVersionError(
            f"tag {tag!r} must look like v0.1.6 or v0.1.6-alpha1")
    version = match.group("version")
    versions = changelog_versions(text)
    if not versions:
        raise ReleaseVersionError("CHANGELOG.md has no version headings")
    if version not in versions:
        raise ReleaseVersionError(
            f"CHANGELOG.md has no '## [{version}]' section; "
            f"newest heading is '{versions[0]}'")
    if version != versions[0]:
        raise ReleaseVersionError(
            f"tag {tag} is not the newest CHANGELOG version "
            f"(newest is '{versions[0]}'); releasing an older version would "
            "publish a package that sorts below what hosts already have")
    if not unreleased_is_empty(text):
        raise ReleaseVersionError(
            "CHANGELOG.md [Unreleased] still has entries; move them under "
            f"'## [{version}]' before tagging")
    return version, debian_version(version)


def current(text):
    """The newest CHANGELOG version, without requiring a tag.

    Used as the default build version so an untagged local build is still stamped
    with the version its changelog describes rather than a hardcoded constant.
    """
    versions = changelog_versions(text)
    if not versions:
        raise ReleaseVersionError("CHANGELOG.md has no version headings")
    return versions[0], debian_version(versions[0])


def sort_key(version):
    """Order of two CHANGELOG versions: numeric release, then a final above its pre-releases."""
    release, _, pre = version.partition("-")
    return tuple(int(part) for part in release.split(".")), pre == "", pre


def dev_version(text, stable_text, build):
    """The version of development build `build`: a pre-release of the next stable release.

    The next release is the newest heading when `main` has not published it yet (it was cut on
    `dev`), otherwise that heading with the patch number raised by one.
    """
    top = current(text)[0]
    published = current(stable_text)[0]
    if sort_key(published) > sort_key(top):
        raise ReleaseVersionError(
            f"main already published {published}, newer than dev's {top}; merge main into dev first, "
            "or this build would sort below a stable release")
    if top == published:
        major, minor, patch = top.partition("-")[0].split(".")
        top = f"{major}.{minor}.{int(patch) + 1}"
    return f"{top}-dev.{build}"


def notes(text, version):
    """The CHANGELOG body of one version heading, for use as release notes."""
    lines = text.splitlines()
    body = []
    inside = False
    for line in lines:
        match = HEADING.match(line)
        if match:
            if inside:
                break
            inside = match.group("version").strip() == version
            continue
        if inside:
            body.append(line)
    if not inside:
        raise ReleaseVersionError(f"CHANGELOG.md has no '## [{version}]' section")
    return "\n".join(body).strip() + "\n"


def dev_notes(text, stable_text):
    """What a development build ships.

    When dev's newest heading is ahead of main's published version the release was cut on dev: its
    entries come first, then whatever was added to [Unreleased] since the cut. Otherwise the notes
    are the [Unreleased] entries.
    """
    top = current(text)[0]
    if sort_key(top) > sort_key(current(stable_text)[0]):
        cut = notes(text, top).strip()
        unreleased = unreleased_notes(text)
        if unreleased.startswith(PLACEHOLDER):
            return cut + "\n"
        return f"{cut}\n\n{unreleased}"
    return unreleased_notes(text)


def unreleased_notes(text):
    """[Unreleased] without its maintainer instruction block."""
    body = "\n".join(line for line in notes(text, "Unreleased").splitlines()
                     if not line.lstrip().startswith(">")).strip()
    return (body or PLACEHOLDER) + "\n"


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--tag", help="release tag, e.g. v0.1.6-alpha1")
    source.add_argument("--current", action="store_true",
                        help="newest CHANGELOG version, without tag validation")
    parser.add_argument("--changelog", default=str(ROOT / "CHANGELOG.md"))
    parser.add_argument("--channel", choices=("stable", "dev"), default="stable",
                        help="dev numbers a build of the dev branch; needs --current, --build and --stable-changelog")
    parser.add_argument("--build", type=int, help="development build number (git rev-list --count HEAD)")
    parser.add_argument("--stable-changelog",
                        help="CHANGELOG.md of main, to learn which release is already published")
    parser.add_argument("--format", choices=("debian", "tag", "both"), default="debian",
                        help="debian prints 0.1.6~alpha1; tag prints 0.1.6-alpha1")
    parser.add_argument("--notes", action="store_true",
                        help="print the version's CHANGELOG section instead of the version")
    args = parser.parse_args(argv)
    if args.channel == "dev" and not (args.current and args.build and args.stable_changelog):
        parser.error("--channel dev needs --current, --build and --stable-changelog")
    try:
        text = Path(args.changelog).read_text()
        stable_text = Path(args.stable_changelog).read_text() if args.stable_changelog else ""
    except OSError as error:
        print(f"release-version: {error}", file=sys.stderr)
        return 1
    try:
        if args.channel == "dev":
            tag_version = dev_version(text, stable_text, args.build)
            deb = debian_version(tag_version)
        else:
            tag_version, deb = current(text) if args.current else resolve(args.tag, text)
    except ReleaseVersionError as error:
        print(f"release-version: {error}", file=sys.stderr)
        return 1
    if args.notes:
        print(dev_notes(text, stable_text) if args.channel == "dev" else notes(text, tag_version), end="")
    elif args.format == "debian":
        print(deb)
    elif args.format == "tag":
        print(tag_version)
    else:
        print(f"{tag_version} {deb}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
