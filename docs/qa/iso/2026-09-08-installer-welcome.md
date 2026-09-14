# Installer welcome copy verification

2026-09-08, Nex Core 0.1.0-alpha1. Welcome introduces the host platform and reminds operators to
deploy Nex Controller separately; this alpha ISO installs Nex Core only.

| Check | Result |
|---|---|
| ISO SHA256 `c7f9421cd40f0fb2dab4d8eaeb4e47bc9077e1c2960bc99bee7210bf67940cdb`: retained wizard source, complete initrd, installer mode/syntax | Passed |
| Native `wizard-review.py` (test-only preparation marker, QEMU/KVM, 800×600, 4 GiB, no NIC, fresh 24 GiB raw disk) | Passed: all welcome text and Begin setup fit; dropdown, password reset, hostname and rejection checks passed (capture `welcome/welcome.png`, not committed) |
| Docs structure/whitespace; independent source review | Passed; no scope/API concern. Hosted CI and range coverage not claimed |

No full installation or physical hardware repeated for this copy change.
