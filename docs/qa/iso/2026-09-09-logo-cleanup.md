# Installer logo background cleanup

Date: 2026-09-09. Development checkout based on `1fb97824a9acdcfc25e071eb48c18e9645ca2c16`.

## Problem and change

project owner confirmed the installer works, but reported a distorted logo with a rippled
background. Both GTK consumers cropped the pale, textured design-reference board
and painted it over the flat installer canvas. The original native capture
reproduces that rectangle; this is separate from installer functionality.

![Original textured logo background](2026-09-09-logo-cleanup/before.png)

The dedicated [transparent lockup](../../../installer/assets/nex-core-lockup.png)
retains the rounded Nex lettering, crossed x, separator and Core descriptor.
Both wizard and cdebconf shell use the same asset with proportional native GDK
scaling. Their original image allocations remain 200×48 and 240×58 so content
and navigation do not move. Missing/undersized asset fallback remains text.
The original brand board is unchanged; this is a raster derivative, not a vector master.

API impact: none — local file → GTK image → header rendering; no Controller
operation, Core command/result, client-facing API or Debian question contract changes.
Guide impact: none — installation choices, operator steps, confirmations and recovery
are unchanged. Existing artwork redistribution terms still apply.

## Asset provenance

Built-in imagegen edited `docs/brand/nex-brand.png`; no third-party asset or
runtime/build dependency was added. Selected output SHA256:
`38b50b93090b802019edcb9b49b792965fc29b2dafc319b8fb35fe4252fe8851`.

Prompt used for the selected output:

> Edit target: supplied Nex brand board. Extract ONLY the existing lower-left product lockup reading 'nex | Core' (original board bounds x45,y510,width400,height96). Preserve the exact original letterforms, relative sizes and spacing, particularly continuous crossed x and rounded n/e. Output a tightly bounded horizontal PNG product lockup with genuinely transparent background, modest even transparent padding. Flatten dark letter fills to solid graphite #17233B; preserve thin vertical separator as solid silver-blue #C6CEE4. Remove all pale board background, ripple, grain, shadows, uneven fill, texture and gradients. Do not redesign lettering, substitute font, add an n tile, add labels or other board elements. Production installer asset.

The selected output has real RGBA transparency. Native GTK crops its surplus
padding at 150,190 (1880×360); no post-generation raster editing was used. A second
crop-only generation produced an opaque checkerboard and was rejected, not shipped.

## Environment and method

Built only the GTK initrd and matching modified frontend source archive in
`/tmp/nex-logo-work`, using the installed `nex-iso-builder` Docker image and the
cached Debian 13.6 base ISO. Existing `dist` ISO and main staging tree were not
changed. Final initrd SHA256:
`41abf2928d2b9567a471e479ddbd1749f7c7fa4d05fc0ea7cc4ab20768016e29`.

Booted the final native initrd with the existing staged kernel and Nex alpha ISO
in QEMU/KVM. Wizard review uses 800×600, 2 CPUs, 4 GiB RAM, no NIC and a newly
created disposable regular-file disk; it does not authorize installation.
Progress review is diskless and uses simulated debconf transitions against the
real rebuilt GTK module. It verifies rendering/error interaction, not storage work.

Commands, from the repository root (the temporary output directories must not exist):

```sh
python3 tests/integration/installer-logo.py /tmp/nex-logo-work/.cache/nex-iso/stage/install.amd/gtk/initrd.gz
bash installer/check.sh
python3 tests/integration/installer-shell.py
python3 tests/integration/wizard-review.py dist/nex-core-0.1.0-alpha1-amd64.iso --kernel .cache/nex-iso/stage/install.amd/vmlinuz --initrd /tmp/nex-logo-work/.cache/nex-iso/stage/install.amd/gtk/initrd.gz --output /tmp/nex-logo-wizard-final
python3 tests/integration/installer-progress.py dist/nex-core-0.1.0-alpha1-amd64.iso --kernel .cache/nex-iso/stage/install.amd/vmlinuz --initrd /tmp/nex-logo-work/.cache/nex-iso/stage/install.amd/gtk/initrd.gz --output /tmp/nex-logo-progress-final
```

## Results and regression

The new native wizard pixel check compares three logo-margin pixels with the
surrounding canvas. It failed against the old initrd with
`Logo background must blend into the native header without texture`:
[actual failure](2026-09-09-logo-cleanup/regression-before.txt).
The initrd packaging regression also failed against the old image's missing clean
asset. Both checks already run in both GitHub and Gitea installer workflows on
pushes and pull requests; hosted CI has not been run for this unpushed change.

Initial final-asset testing caught a ten-pixel content shift from reducing image
height; retaining the original GTK allocation fixed it before acceptance.

Final native wizard review passed, including timezone dropdowns, password reset,
hostname validation, review content and disk rejection. Final diskless progress
review passed active/gap/resumed layout, error acknowledgement, log inspection,
and completion acknowledgement. See [wizard result](2026-09-09-logo-cleanup/wizard.txt)
and [progress result](2026-09-09-logo-cleanup/progress.txt).

![Clean logo in final native wizard](2026-09-09-logo-cleanup/after-wizard.png)

![Clean logo in final native progress shell](2026-09-09-logo-cleanup/after-progress.png)

Both final captures were visually inspected. The header is flat with no pale
rectangle or ripple, and the Core descriptor remains clear. Initrd artwork bytes,
parent-directory ordering, installer shell checks and installer contract checks
passed. The retained modified source archive contains exact bytes of the selected
asset and both GTK consumers.

No full ISO was rebuilt, no real disk or installed host was changed, and no fresh
installation, physical hardware, UEFI or 1024×768 walkthrough was repeated. project owner's
working-install confirmation is user evidence, not an agent-run installation test.
The corrected sources take effect in the next normal ISO build.

Documentation structure checking (`python3 scripts/check_docs.py --all`), all 13
checker unit tests, and `git diff --check` passed. Git-range coverage was not run
in the shared dirty checkout. An independent read-only reviewer inspected both
consumers, asset/source packaging, before/after native captures, original failure
and final pass logs, and independently reran the initrd regression successfully.
No implementation findings remain; the root agent owns the coordinated bug-ledger
and platform-plan updates for this batch.
