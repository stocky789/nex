# Final ISO and Controller acceptance

Date: 2026-09-16. Sanitized results from disposable nested-KVM labs. Captures and raw records
remain ignored local evidence.

## Final ISO

The final ISO SHA-256 was
`403dc489fc8fb7d948fe3d27a561c4203a54642c27ac18141dae6ede4c84267c`.
Static package, retained-source and parsed OVMF certificate inspection passed; the final run
emitted `INSTALL_PASS`, `OVMF_2023_CERTIFICATES_PASS`, `ROOT_ONLY_PASS`, `FIRST_BOOT_PASS` and
`REBOOT_PASS`. The visual completion-screen
acceptance for BUG-0209 ran on an earlier 0.2.8 build with unchanged installer code: it stayed
open until **Restart now**, then rebooted naturally. It is not a visual inspection claim for this
exact SHA.

The bundled OVMF package carries the enrolled 2023 Microsoft certificate template. This is
package and installed-artifact evidence, not whole-ISO redistribution certification.

## Firmware and Secure Boot

The deployed final package completed this lifecycle: Secure Boot on → rename → Secure Boot off
→ Secure Boot on. The UUID-keyed NVRAM path, hash and boot entries survived rename; host NVRAM
reported SecureBootEnable `01`, then `00`, then `01`; keys and boot entries were retained; and
delete removed both domain and NVRAM. SecureBootEnable was read with the OVMF vendor GUID
`f0a30bc7-af08-4556-99c4-001009c93a44`; PK and SetupMode use the EFI global-variable GUID, and
dbx uses the image-security GUID. This corrects the earlier OVMF GUID assumption.

In a signed Debian guest, Secure Boot on exposed EFI value `01`; after Core set Secure Boot off,
the guest SecureBoot EFI variable was absent and host SecureBootEnable was `00`. This proves the
setting reaches both firmware state and the guest. It does not prove the Windows TPM/Secure Boot
independence matrix. Fresh 60 GiB cases created, booted and deleted with the expected live XML
combinations, but Windows Setup did not reach the non-IoT requirements decision; optical-prompt
timing and the manual Setup flow limited that harness.

## Controller and installer flows

Chromium against deployed Controller/Core passed the console Start flow at the visible CD boot
prompt, offered and completed install-media eject with both durable media fields empty, discarded
a failed HTTPS-downgrade import while retaining task history, and showed a successful upload's
live host-transfer state. The existing dedicated-memory, console layout and sign-in checks passed.
The exact Type into VM string, including capitals and shifted characters, passed in the final
Windows check. The earlier 0.2.8 installer completion check passed as described above.

The current graceful-shutdown timing check did not submit shutdown because guest-agent readiness
did not arrive within its bounded attempts. BUG-0213's historical delay therefore remains
unexplained and current graceful timing remains unverified.

## Limits

This is disposable nested-virtualization evidence. Physical hardware, whole-ISO redistribution,
and the independent Windows TPM/Secure Boot matrix remain open.
