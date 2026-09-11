# Deployment usability rebuild — 2026-09-08

Rebuilt the Controller appliance, host-local package and Core ISO from the current
workspace, preserving the matching source archives. The fresh development appliance
manifest is signed; its matching trusted public key is embedded in the host package.
The private signing key stays outside the source tree and artifacts.

## Artifacts

- Appliance `0.1.0+usability1`: SHA256 `6f616f6b41b1a6e505f4577774f47e05f2fd37137459b03138e144b452508045`.
- Local package `0.1.0+usability1`: SHA256 `782786420eb06df95a58222f1e0b7b9c221289731c079f2ec0a6b40f072c5393`.
- Local source snapshot SHA256 `333ce86fc2a285874b61e662f620e82936c4e4baf5ca374e94088eece1bdef5b`.

The appliance builder checked the pinned Debian base digest, built the Controller/UI,
installed current firstboot code and checked qcow2 integrity. Independent review verified
the finished image digest and Ed25519 manifest signature, and compared critical runtime
source entries with the workspace. The new native test is explicitly retained by the
source allowlist; its regression passed2/2. Package compilation/typecheck/build passed.

## Native guest walkthrough

```sh
python3 tests/integration/appliance-dhcp-boot.py \
  --image dist/appliance-usability-test/controller.qcow2 \
  --evidence docs/qa/appliance/usability-dhcp-boot \
  --docker /tmp/nex-rootauth-docker
```

Exit zero: a disposable KVM overlay with 4 GiB RAM and two CPUs received DHCP on the
exact management MAC. The test verified hostname identity using the expected certificate,
read the ready marker over HTTPS, rejected a wrong password and logged in with a
one-character password. QGA confirmed active services and removed bootstrap/password
files and sensitive cloud-init caches. Overlay, seed and temporary logs were removed.
[Machine-readable evidence](usability-dhcp-boot/results.json) identifies the tested image.
Independent review checked disposable-resource safety, runtime results and limits.

This is actual guest boot on QEMU user-mode DHCP, not physical LAN testing or a full
host-local deploy/enrollment walkthrough. Lease-change reconnection remains covered
by focused backend checks, not a native multi-host scenario. No live host was modified.
A full fresh Core ISO installation was not repeated for this rebuild. External source
redistribution/artwork gates and hosted CI remain unverified.

API impact: none for this rebuild — it delivers the already documented changed contracts.
Guide impact: updated artifact availability. The original behavior assessment remains
[deployment usability](deployment-usability.md).

## Final package and ISO checks

The actual Debian13 package smoke exited zero: TLS IP verification, root PAM session,
password rotation, wrong/old-admin/locked/expired-account/expired-password rejection,
source/API routes and secret-free logs passed. Removal preserved configuration/storage.
The initialized banner printed only the web URL. Independent package extraction matched
the fresh appliance image, signed manifest/signature, source archives, public trust key
and clean-banner setup source; built UI contains the new DHCP/settings behavior.

ISO `dist/nex-core-0.1.0-alpha1-amd64.iso` SHA256:
`5de67b73dc078f00e11ade56f4f6af66c4bc9d9a697ec80351ea69882835a376`.
Checksum verification passed. The package extracted from the finished ISO matches the
tested package digest above. Documentation structure/links and whitespace checks passed.
