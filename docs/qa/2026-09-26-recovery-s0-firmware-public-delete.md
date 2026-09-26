# S0 Secure Boot/vTPM public snapshot deletion probe

## Scope

This contributor-only probe tests public, stopped snapshot consolidation and deletion on a disposable +nex3 guest with two disks, UEFI Secure Boot, and vTPM 2.0. It uses public libvirt operations and does not invoke integrated snapshot deletion. It validates point-A consolidation while retaining point B and a live current C head, then restores the paired point-B firmware state and boots the VM.

## Method and environment

On 2026-09-26, the probe ran in a fresh, private clone of the selected disposable Core guest. The selected candidate manifest SHA-256 was `116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e`; the installed libvirt packages matched the `11.3.0-3+deb13u3+nex3` candidate closure, with the recorded QEMU 10.0.13 runtime. The outer guest used KVM acceleration; the inner firmware VM used QEMU TCG. The native preflight found only the previously preserved stopped fixture, no QEMU process or pid file for it, and AppArmor enabled. The tested VM's observed process context exactly matched its generated libvirt UUID with `(enforce)`.

The reviewed runner and test were archived with the private run record before launch. The source image used for the clone retained its original SHA-256 after a graceful `sync` and system poweroff; the outer QEMU exited with status 0. A prior run had reached cleanup but failed to serialize its terminal JSON, so it is not counted. Run5 is the first complete run with the refreshed final receipt and all expected observations.

## Results

- Captured stopped point A and point B for both disks, then saved the point-B NVRAM and swtpm state, including file bytes and metadata. Created future guest disk markers, a future EFI entry, and a persistent TPM handle after point B.
- All six initial, committed-D, rebased-B, and current-C logical image comparisons passed for each disk (12 comparisons total). Both public `blockcommit` commands returned 0 with no remaining blockcommit job. The observed backing chains retained the full D suffix, including the base image where present. Current C remained byte-equivalent to its pre-consolidation reference.
- Rewrote the actual point-B snapshot metadata to remove its point-A parent and point each saved disk at D. The metadata-only point-A deletion succeeded while point B remained current. The full reference scan then passed across the fixture and existing domains, including active/inactive XML, snapshot metadata, and source chains; no selected A top remained referenced. Only the two exact, unique A files were unlinked.
- Reverted point B, restored its paired NVRAM and swtpm state, and booted the VM. Readback confirmed `/dev/vdb` was mounted at `/mnt/recovery`, with both point-B disk markers present. Secure Boot remained enabled, the point-B sealed secret unsealed, and the future EFI entry and persistent TPM handle were absent.
- Final cleanup undefine returned 0, the generated domain was absent, and its private work directory was removed.

The operation receipt is atomically persisted before cleanup and refreshed afterward with the cleanup outcome. The locally fetched receipt therefore contains the final cleanup state; it is not a byte-for-byte copy of the initial pre-cleanup receipt. The private final result JSON SHA-256 is `9dfac0b857a8a938065694df8e9dd498fd42be8a7ab152f53b78a525ac250ed6`; the refreshed receipt SHA-256 is `7a8f824e5ff74f618fd2e562778254b54a8f564f1d8c4c5f4fff12d09f8164d0`.

## Verification and limits

`python3 -m unittest tests/test_recovery_s0_firmware.py` passed 17 tests. `python3 -m py_compile tests/integration/recovery_s0_firmware.py tests/test_recovery_s0_firmware.py` passed, as did `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`. An independent reviewer checked the source archive and native result. The native run proves this selected +nex3 TCG firmware profile and public two-disk operation sequence; it does not establish integrated snapshot deletion or behavior on other storage backends, firmware combinations, or hardware acceleration profiles.
