# Quiet keyboard initialization

Date: 2026-09-08. Nex Core 0.1.0-alpha1.

API impact: none; local GTK callback only. Guide impact: updated.

The actual callback regression failed on the old successful-layout notice and passed
after the fix. Successful application clears the message; failed application still
reports an error and blocks continuation. Both password fields are still cleared.
Strict GTK compilation passed. Final ISO SHA256:
`67a2a5bd32de453a576ce698a11fe507fd8628cb4b6a1079ceecfc1a759a9baa`.
Checksum, complete-initrd checks and retained wizard source comparison passed.

Native `wizard-review.py` passed at 800×600 under QEMU/KVM with no NICs, 4 GiB RAM
and a disposable 24 GiB raw disk. Inspected Welcome capture has no keyboard notice.
The walkthrough also passed timezone selection, password visibility reset, hostname
synchronization and unchecked disk confirmation rejection. The VM was stopped.

![Welcome without a startup warning](keyboard-notice/welcome.png)

No full installation, real keyboard hardware or hosted CI was tested for this slice.
The callback test simulates GTK/process boundaries; native execution uses the rebuilt
ISO kernel/initrd. Documentation structure and whitespace checks passed.
