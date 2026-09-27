# Recovery S0: firmware rollback on the +nex3 candidate

Date: 2026-09-26. Status: **the BIOS quiesced-capture and Secure Boot plus vTPM
paired-state rollback probes passed on the +nex3 Core candidate. Snapshot deletion was
intentionally not run; this evidence does not close S0 or establish product support.**

## Candidate and patch comparison

The package-based Debian 13 Core candidate reported libvirt
`11.3.0-3+deb13u3+nex3`, QEMU `10.0.13`, OVMF `2025.02-8+deb13u1`, and swtpm
`0.7.1-1.5`. It was not installed from the Core ISO. The install owner reported that the
selected 15-package libvirt upgrade simulated 15 upgrades, zero new packages and zero
removals, and was installed after the 21 existing disposable domains were checked stopped
with autostart disabled, no QEMU processes, and no `snapshotDeleteInProgress` metadata. The
owner also reported a matching post-install stopped inventory. The apt simulation and
before/after inventory receipts were not retained with this evidence, so these setup details
are owner-reported context rather than independently replayable proof. The candidate package
manifest SHA256 was `116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e`.

Compared with the retained +nex2 candidate, the four shared patches were byte-identical:
unique revert overlay names (`ecb2849a817be9e3f51472266c2df6a298fdbf8761bcf70ef84aaa007df44bc2`),
partial backing-chain handling (`2a312a310209c2dc94d3cbb1ec26b186c352e266674071ebab5e917cd5c89926`),
QMP JSON depth (`4d3aa59cd6bbb2168a1f64e26eb875c0be37af7dfa7390cf620fb67e66322ad9`), and
read-only backing-file AppArmor policy (`766c2450f194adbbc1fb8956841e885c2cc51f629b89949961bc0c2bd94438a5`).
The +nex2 candidate also had two snapshot-deletion recovery patches that are absent from
+nex3. Those deletion-specific changes are not exercised or certified here.

## Results

The updated probe's `--preserve-snapshot` mode kept each successful domain, native snapshot,
disk heads, and firmware state. It did not call `snapshot-delete`, undefine the domain, or
remove its private subtree.

| Profile | Observed result |
|---|---|
| BIOS, QGA-quiesced running capture | Guest filesystem was thawed after capture. Revert left the domain paused; the probe stopped it before guest checks, verified the baseline disk marker, and shut it down. The external snapshot and domain remain retained. |
| UEFI Secure Boot + vTPM 2.0, stopped capture | Guest reported Secure Boot enabled. Native disk revert did not restore NVRAM and swtpm bytes, so while shut off the probe restored the saved paired state. It then verified the baseline disk marker, unsealed the baseline test secret, kept Secure Boot enabled, and confirmed the future EFI entry and TPM handle were absent. The native snapshot and domain remain retained. |

The installed `/usr/lib/libvirt/virt-aa-helper` passed the helper regression: read-only backing
files received no write grant or explicit deny, and the omitted-chain-suffix and empty
backing-store-terminator checks passed. Both new domains were shut off after the probes, with
their snapshot metadata present and no `snapshotDeleteInProgress` flag. No existing domain was
changed by either probe. Raw outputs and generated IDs remain in ignored `.cache` evidence
directories under the `recovery-s0-firmware-evidence-nex3-coreb-*` prefix.

## Limits

These are two package-based nested guests on local storage. The firmware-state comparison is
synthetic test data, not a production workload. This run did not test snapshot deletion,
repeated revert, interrupted operations, the NFS/SMB/iSCSI backends, physical firmware,
PCR-bound TPM secrets, an ISO-installed Core candidate, or guest OS installation. The earlier
+nex2 deletion results are separate historical evidence; they do not change the explicit
no-delete scope of these +nex3 runs. S0 remains open.
