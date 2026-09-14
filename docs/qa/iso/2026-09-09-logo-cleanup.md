# Installer logo background cleanup

2026-09-09. Development checkout based on `1fb97824a9acdcfc25e071eb48c18e9645ca2c16`.
Bug: BUG-0030 ([ledger](../../BUG_LEDGER.md)).

## Problem and change

Project owner confirmed the installer works but reported a distorted logo on a rippled background:
both GTK consumers cropped the textured reference board. The original native capture reproduces the
rectangle (`2026-09-09-logo-cleanup/before.png`, not committed).

The [transparent lockup](../../../installer/assets/nex-core-lockup.png) keeps the rounded lettering,
crossed x, separator and Core descriptor. Wizard and cdebconf shell share it with proportional GDK
scaling inside unchanged 200×48 and 240×58 allocations; missing/undersized asset falls back to text.
Brand board unchanged; raster derivative, not a vector master.

## Asset provenance

Built-in imagegen edited `docs/brand/nex-brand.png`; no third-party asset or dependency. Selected output
SHA256 `38b50b93090b802019edcb9b49b792965fc29b2dafc319b8fb35fe4252fe8851`. Prompt:

> Edit target: supplied Nex brand board. Extract ONLY the existing lower-left product lockup reading 'nex | Core' (original board bounds x45,y510,width400,height96). Preserve the exact original letterforms, relative sizes and spacing, particularly continuous crossed x and rounded n/e. Output a tightly bounded horizontal PNG product lockup with genuinely transparent background, modest even transparent padding. Flatten dark letter fills to solid graphite #17233B; preserve thin vertical separator as solid silver-blue #C6CEE4. Remove all pale board background, ripple, grain, shadows, uneven fill, texture and gradients. Do not redesign lettering, substitute font, add an n tile, add labels or other board elements. Production installer asset.

Real RGBA transparency; GTK crops padding at 150,190 (1880×360); no post-generation raster edits. A
second crop-only generation produced an opaque checkerboard and was rejected.

## Environment and method

Built only the GTK initrd and modified frontend source archive in `/tmp/nex-logo-work` with the
`nex-iso-builder` image and cached Debian 13.6 base ISO; `dist` ISO and main staging untouched. Final
initrd SHA256 `41abf2928d2b9567a471e479ddbd1749f7c7fa4d05fc0ea7cc4ab20768016e29`. QEMU/KVM with staged
kernel and alpha ISO: wizard review at 800×600, 2 CPUs, 4 GiB, no NIC, new disposable file disk (no
install); progress review diskless with simulated debconf transitions against the real GTK module.

```sh
python3 tests/integration/installer-logo.py /tmp/nex-logo-work/.cache/nex-iso/stage/install.amd/gtk/initrd.gz
bash installer/check.sh
python3 tests/integration/installer-shell.py
python3 tests/integration/wizard-review.py dist/nex-core-0.1.0-alpha1-amd64.iso --kernel .cache/nex-iso/stage/install.amd/vmlinuz --initrd /tmp/nex-logo-work/.cache/nex-iso/stage/install.amd/gtk/initrd.gz --output /tmp/nex-logo-wizard-final
python3 tests/integration/installer-progress.py dist/nex-core-0.1.0-alpha1-amd64.iso --kernel .cache/nex-iso/stage/install.amd/vmlinuz --initrd /tmp/nex-logo-work/.cache/nex-iso/stage/install.amd/gtk/initrd.gz --output /tmp/nex-logo-progress-final
```

Output directories must not pre-exist.

## Results and regression

| Check | Result |
|---|---|
| Native wizard logo-margin pixel check on old initrd | Failed: `Logo background must blend into the native header without texture` ([actual failure](2026-09-09-logo-cleanup/regression-before.txt)) |
| Initrd packaging regression on old image | Failed: missing clean asset |
| Initial final-asset test | Caught a ten-pixel content shift from reduced image height; fixed by keeping the GTK allocation |
| Final native wizard review | Passed: timezone dropdowns, password reset, hostname validation, review, disk rejection ([wizard result](2026-09-09-logo-cleanup/wizard.txt)) |
| Final diskless progress review | Passed: active/gap/resumed layout, error acknowledgement, log inspection, completion ([progress result](2026-09-09-logo-cleanup/progress.txt)) |
| Captures `after-wizard.png`, `after-progress.png` (not committed), visually inspected | Flat header, no pale rectangle or ripple, descriptor clear |
| Initrd artwork bytes, parent-directory order, shell and contract checks; source archive holds exact asset and both consumers | Passed |
| `check_docs.py --all`, 13 checker unit tests, `git diff --check` | Passed; range coverage not run |
| Independent read-only review (consumers, packaging, captures, logs; reran initrd regression) | No findings |

Both checks already run in both installer workflows; hosted CI not run.

Not done: full ISO rebuild, real disk or installed host, fresh install, physical hardware, UEFI,
1024×768. Project owner's working-install confirmation is user evidence, not an agent-run test.
Sources take effect in the next normal ISO build.
