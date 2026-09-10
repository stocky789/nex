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
"""
import argparse
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
HEADING = re.compile(r"^## \[(?P<version>[^\]]+)\]")
TAG = re.compile(r"^v(?P<version>\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?)$")
PRERELEASE = re.compile(r"^(?P<release>\d+\.\d+\.\d+)-(?P<pre>[0-9A-Za-z.]+)$")


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
    """The Unreleased section is a placeholder; entries belong under a version."""
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
    return f"{match.group('release')}~{match.group('pre')}"


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


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--tag", help="release tag, e.g. v0.1.6-alpha1")
    source.add_argument("--current", action="store_true",
                        help="newest CHANGELOG version, without tag validation")
    parser.add_argument("--changelog", default=str(ROOT / "CHANGELOG.md"))
    parser.add_argument("--format", choices=("debian", "tag", "both"), default="debian",
                        help="debian prints 0.1.6~alpha1; tag prints 0.1.6-alpha1")
    args = parser.parse_args(argv)
    try:
        text = Path(args.changelog).read_text()
    except OSError as error:
        print(f"release-version: {error}", file=sys.stderr)
        return 1
    try:
        tag_version, deb = current(text) if args.current else resolve(args.tag, text)
    except ReleaseVersionError as error:
        print(f"release-version: {error}", file=sys.stderr)
        return 1
    if args.format == "debian":
        print(deb)
    elif args.format == "tag":
        print(tag_version)
    else:
        print(f"{tag_version} {deb}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
