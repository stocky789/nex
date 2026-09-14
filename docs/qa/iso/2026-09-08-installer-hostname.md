# Installer hostname standards and verification

2026-09-08, Nex Core 0.1.0-alpha1. Bug: BUG-0018 ([ledger](../../BUG_LEDGER.md)).

## Standards decision

`nexcore.example.invalid` is valid ASCII hostname syntax; the previous GUI and backend rejected it.

- [RFC 1123 §2.1](https://www.rfc-editor.org/rfc/rfc1123.html#section-2.1): digit-leading labels allowed; accept short and qualified ASCII names with letters, digits and internal hyphens.
- [RFC 1035 §§2.3.1–2.3.4](https://www.rfc-editor.org/rfc/rfc1035.html#section-2.3.1): case-insensitive; 63-byte labels, 255-byte wire limit, so at most 253 text bytes excluding a final dot.
- [RFC 1034 §3.1](https://www.rfc-editor.org/rfc/rfc1034.html#section-3.1): accept one final root dot and remove it for installed configuration.
- Private suffixes need no registration or lookup; numeric labels allowed; no alphabetic public TLD required. IP literals, whitespace, underscores, empty labels and edge hyphens rejected.
- ASCII profile only, including `xn--`; Unicode input needs its ASCII IDNA form ([RFC 5890 §2.3.2.1](https://www.rfc-editor.org/rfc/rfc5890.html#section-2.3.2.1)). No IDNA2008 validation claim; [GLib hostname conversion](https://docs.gtk.org/glib/func.hostname_to_ascii.html) is not used as a substitute.
- [Debian hostname(5)](https://manpages.debian.org/trixie/systemd/hostname.5.en.html) recommends a short static name: split qualified input into first label and domain; keep full name plus short alias in `/etc/hosts`.

## Verification

ISO SHA256 `21f83a2fe9310886d93b94d3d764055863c27219939d2c5ab65858b5ce8811da`.

| Check | Result |
|---|---|
| Checksum, complete initrd, retained wizard/config source | Passed |
| Strict C hostname/network regression | Passed: short/qualified, mixed case, digit-leading/numeric, ASCII Punycode, single root-dot normalization, 63/64-byte label and 253/254-byte name boundaries, invalid separators/characters/IPs |
| `installer-hostname.py` (compiled backend output block, in-memory file boundary) | Passed: short hostname, full/short hosts aliases, separate Debian hostname/domain. Simulation, not an installed boot |
| Keyboard, QoL, protected handoff, installer-mode checks | Passed |
| Native `wizard-review.py` (final ISO kernel/initrd, test-only preparation marker, QEMU/KVM, 4 GiB, no NIC, 800×600, disposable 24 GiB raw disk) | Passed: Host accepts `nexcore.example.invalid`; Network rejects underscore then accepts `7nex-core.example.invalid.`; Host retains the final dot; continuing removes it so Review shows `7nex-core.example.invalid`; password reset and wrong-disk checks pass; first 1 MiB untouched; VM stopped |
| Docs structure/whitespace | Passed; both installer workflows run the regressions; hosted CI and range coverage not claimed |
| Independent source review; independent native visual review | No blocking issues; reviewer reran boundary and backend checks |

Captures (not committed): `hostname/network-hostname.png`, `host-after-network-edit.png`, `review.png`.

Not tested: full installation/reboot, physical hardware, DNS registration. Output contents were
checked through the actual renderer at a simulated file boundary. Local hosts aliases create no DNS record.
