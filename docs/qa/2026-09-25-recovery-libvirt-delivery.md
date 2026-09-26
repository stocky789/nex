# Recovery S0 matched libvirt delivery wiring

Status: installer and release integration is implemented and focused checks pass.
No new ISO was built through the integrated recipe, no external release was
published, and native S0 acceptance remains open pending its own final lifecycle
results.

## Delivery behavior

`installer/build.sh` invokes the retained `packaging/libvirt/build-matched-libvirt.sh`
recipe and its `tests/integration/libvirt_readonly_policy.py` test helper in a
pinned Debian 13 build image using an init process and the existing named APT
archive cache. The four APT metadata/archive operations take a shared `flock` on
that archive volume; the lock is released before the package build. It still
verifies the builder's SHA-256 list,
binary manifest, patched source descriptor and `11.3.0-3+deb13u3+nex1` version
before continuing. After resolving the existing Debian package closure, the
installer replaces only packages whose `Source` is `libvirt`, whose Debian
version is `11.3.0-3+deb13u3`, and for which the candidate has the same binary
package name and architecture. It fails on missing or duplicate matches and
does not add the other candidate binaries to the installer repository.

The ISO retains the original Debian source artifacts and the matching patched
source descriptor/delta, upstream copyright notice, build inputs and log, exact
recipe and patches, the full candidate binary manifest, and a checksum list for
the retained evidence. `replaced-packages.tsv` records each selected original
and replacement package identity, version and SHA-256. The selected replacement
packages remain in the normal Nex package repository and package manifest, where
the existing compliance inventory records their actual bytes and matching source
version. The source archive now includes the installer integration, package
builder and patches, release publisher, and the libvirt lifecycle CI runner.

For network installation, the netinst manifest recognizes rebuilt `libvirt`
source packages, places their exact selected `.deb` bytes in the hash-named
download payload set, and records the pinned download URLs and hashes. The
release publisher accepts those package-specific payload names only when the
filename digest, manifest digest and file bytes agree. Both release workflows
validate the package identity, local version and hash-derived filename before
publishing; they transfer and publish all generated netinst payloads.

The build retains third-party notices and source without changing Nex API
contracts or pool state. Debian libvirt and its modifications retain their own
licensing and attribution; this integration does not relicense them. See
[`LICENSING.md`](../LICENSING.md) and the separate
[matched-build evidence](2026-09-25-recovery-libvirt-backport.md).

## Checks and limits

Focused unit tests exercised slot replacement with synthetic `.deb` packages,
including an unmatched candidate package that remained outside the installer
repository; netinst manifest creation for a `libvirt` source package; exact
source descriptor retention in the release source bundle; and hash-pinned
publication of a libvirt payload. `installer/check.sh` passed. The following
commands also passed:

```sh
python3 -m unittest discover -s tests -p 'test_netinst*.py'
python3 -m unittest tests.test_release_publish tests.test_build_source tests.test_source_bundle
```

The Debian APT-dependent netinst suite also ran without a skip in an isolated
`debian:trixie-slim` container pinned by image digest, with `python3` and
`dpkg-dev` installed. A minimal read-only test snapshot included the netinst
tests, their direct installer/script inputs, and both mirrored release workflow
files; it excluded `.cache` and private lab artifacts. Result: 14 tests passed.
This verifies the real `apt-get`/`dpkg-scanpackages` path used by the test, not a
netinst installation.

The matched package/source builder has separate completed build and helper-policy
evidence in the linked report. This integration has not run a complete installer
ISO rebuild or netinst installation, and no public release or real host package
installation was performed here. The matched candidate must still pass its
native S0 lifecycle rerun before the S0 gate can close.

API impact: none. No Controller/Core or client-facing API changed. Guide impact:
none; there is no new operator workflow. State owner: none. This packaging change
adds no durable Nex product state. S0 and the recovery plan remain open.

## Four-patch candidate selection — 2026-09-26

The installer and both release workflow payload checks now require the distinct
`11.3.0-3+deb13u3+nex3` source descriptor/package cohort. Synthetic fixtures use the same version and still
verify replacement only within existing libvirt package/architecture slots.
`bash -n installer/build.sh` passed; `python3 -m unittest discover -s tests
-p 'test_netinst*.py'` passed 19 tests with one Debian APT prerequisite skip.
All 15 `test_release_publish.py` tests passed. Independent review caught and
prompted correction of the release workflows' stale candidate version.
An initial module-style invocation could not import the sibling release-test helper;
the documented discovery invocation above resolved that test-runner path issue.

The separate four-patch build produced 73 binaries with 90 manifest entries;
independent review verified all listed checksums and retained original/patched
Debian source artifacts. Its recipe SHA-256 is
`3bf950c83fa9279de22e80c736984304e275d77ffcecd0609893cc5466ac46a5`;
the manifest SHA-256 is
`116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e`.
Upstream QMP and AppArmor-helper tests passed. The build owner also reported
passing one- and two-disk TCG session compatibility checks; these are not the
interrupted public consolidation acceptance campaign.

Initial enforcing AppArmor fixture attempts failed during SSH identity setup and
script staging before native operations. A later two-disk integrated snapshot-delete
control failed and remains separate unresolved evidence for that unsupported path.
The chosen public consolidation route subsequently passed in a fresh enforcing
system-libvirt guest using the owner-reported candidate-reuse invocation: two-disk blockcommit without
data deletion, metadata-only removal, reference-checked unlink, reopen and retained
point checks. Its result SHA-256 is
`a55fabdd9d911d2d10b43c9208fa9dbb5142a0159d7a3956036b58cd772d6e64`.
This is a local TCG control, not interruption or multi-backend certification.
The raw runtime result records installed versions, not the candidate manifest digest;
its exact-build linkage relies on that execution context rather than a hash in the result.
No complete ISO rebuild or netinst installation is established by these results.
The earlier container result above belongs to its original candidate/test snapshot.
