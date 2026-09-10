# Create VM dropdowns: in-page select pickers (BUG-0064)

Date: 2026-09-10. Controller bundle built from this change (`web/dist`), Brave 152
(`--ozone-platform=wayland`) on Hyprland 0.56.2, Playwright Chromium 1234 and Firefox 153.
Whole-desktop captures were taken with `grim`; page captures with Playwright. The linked
PNG captures stay local: `docs/qa/**/*.png` is git-ignored, as for earlier QA records.

## What was reproduced

| Case | Method | Result |
| --- | --- | --- |
| Plain page, one `<select>`, Brave native picker | synthetic click, then pointer moved by a virtual mouse | popup opened, then closed |
| Real Create VM form, Brave native picker, pointer still | synthetic click, desktop idle | popup stayed open 6 s ([capture](brave-native-picker-pointer-still.png), frames at 0.15–6 s plus page capture) |
| Real Create VM form, Brave native picker, pointer moving | synthetic click while the desktop was in use | OS captures show no popup while the page capture still shows it open |
| Real Create VM form, `appearance: base-select`, pointer moving | synthetic click while the desktop was in use | picker stayed open 6 s in every frame ([capture](brave-base-select-pointer-moving.png)) |
| Plain page, native vs base-select, Brave and Firefox 153 | synthetic click | [contact sheet](plain-page-native-vs-base-select.png): Brave rows keep both open with the pointer still; Firefox 153 does not support base-select (`CSS.supports` false) and keeps its native picker |

Conclusion: nothing in the Controller closes the popup. With the pointer still, the native
popup stays open under the real form; it is the compositor dismissing the native popup
window on pointer movement. An in-page picker is immune, so the stylesheet now sets
`appearance: base-select` on `select` and `::picker(select)` in `web/src/style.css` and
`web/src/local.css`.

Limit: the injected real click (uinput virtual mouse) could not be repeated cleanly because
the desktop was in active use; the native-versus-base comparison above relies on synthetic
clicks with the pointer moving. The reporter's real-pointer confirmation is still needed.

## Checks run

- `tests/integration/form_select_picker.py` (headless Chromium, Controller stylesheet):
  computed `appearance: base-select`; `:open` after the click; `:open` after a controlled
  re-selection of the options; picker drawn below the control in the page capture
  (50040 pixels); ArrowUp + Enter chose SATA and closed the picker. Against the committed
  stylesheet the same check fails (`appearance` is `auto`).
- Built bundle against a real Controller with a simulated host (headless Chromium):
  Placement host, Storage and Bus pickers opened in-page, stayed open 800 ms and closed on
  Escape; a pointer choice through the Bus picker set the value to SATA in the React form.
  Captures: [placement host](create-vm-placement-host.png), [storage](create-vm-storage.png),
  [bus](create-vm-bus.png).
- `tests/integration/managed_vm_browser.py --simulated` against the rebuilt bundle: all
  five acceptance groups passed, including the six-step keyboard-accessible VM creation.
