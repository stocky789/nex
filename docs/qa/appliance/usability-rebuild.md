# Deployment usability rebuild — 2026-09-08

Rebuild delivering [deployment usability](deployment-usability.md). Appliance manifest signed;
matching public key embedded in the host package; private key outside tree and artifacts.

## Artifacts

| Artifact | SHA256 |
| --- | --- |
| Appliance `0.1.0+usability1` | `6f616f6b41b1a6e505f4577774f47e05f2fd37137459b03138e144b452508045` |
| Local package `0.1.0+usability1` | `782786420eb06df95a58222f1e0b7b9c221289731c079f2ec0a6b40f072c5393` |
| Local source snapshot | `333ce86fc2a285874b61e662f620e82936c4e4baf5ca374e94088eece1bdef5b` |
| `dist/nex-core-0.1.0-alpha1-amd64.iso` | `5de67b73dc078f00e11ade56f4f6af66c4bc9d9a697ec80351ea69882835a376` |

Builder checked pinned Debian base digest, built Controller/UI, installed firstboot code, checked
qcow2 integrity. Independent review verified image digest, Ed25519 signature and critical source
entries. New native test retained by source allowlist; regression 2/2. Package
compile/typecheck/build passed.

## Native guest walkthrough

```sh
python3 tests/integration/appliance-dhcp-boot.py \
  --image dist/appliance-usability-test/controller.qcow2 \
  --evidence docs/qa/appliance/usability-dhcp-boot \
  --docker /tmp/nex-rootauth-docker
```

Exit zero: disposable KVM overlay (4 GiB, 2 CPU) got DHCP on the exact management MAC; hostname
identity verified via expected certificate; ready marker read over HTTPS; wrong password rejected,
one-character password accepted; QGA confirmed services and removal of bootstrap/password files and
sensitive cloud-init caches. Overlay, seed, logs removed. [Evidence](usability-dhcp-boot/results.json).
Independent review checked resource safety, results and limits.

Limits: QEMU user-mode DHCP, not physical LAN or full deploy/enrollment; lease-change reconnection
only in backend tests; no fresh ISO installation.

## Final package and ISO checks

Debian 13 package smoke exited zero: TLS IP verification, root PAM, password rotation,
wrong/old-admin/locked/expired-account/expired-password rejection, source/API routes, secret-free
logs, removal kept configuration/storage, banner URL only. Independent extraction matched appliance
image, signed manifest/signature, source archives, trust key and banner source; UI contains the
DHCP/settings behavior. ISO checksum passed; its extracted package matches the tested digest.
