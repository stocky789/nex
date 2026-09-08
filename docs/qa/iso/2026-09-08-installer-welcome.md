# Installer welcome copy verification

Nex Core 0.1.0-alpha1, 2026-09-08. Introduces the host platform and reminds operators
to deploy Nex Controller separately. This alpha ISO installs Nex Core only.

API impact: none; static copy changes no local handoff or Controller/Core contract.
Guide impact: updated.

ISO SHA256: `c7f9421cd40f0fb2dab4d8eaeb4e47bc9077e1c2960bc99bee7210bf67940cdb`.
The retained wizard source matches the working source. Complete-initrd checks and
installer mode/syntax checks passed.

The existing native `wizard-review.py` walkthrough passed using this ISO’s kernel/initrd
with its test-only preparation marker, QEMU/KVM at 800×600, 4 GiB RAM, no NICs and a
fresh disposable 24 GiB raw-file disk. All welcome text and Begin setup fit the display;
the subsequent dropdown, password reset, hostname and rejection checks passed.
No full installation or physical-hardware test was repeated for this static-copy change.
Documentation structure and whitespace checks passed; hosted CI and clean-commit range
coverage are not claimed. Independent source review found no material scope/API concern.

![Updated welcome page](welcome/welcome.png)
