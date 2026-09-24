#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise shipping cdebconf GTK progress transitions in a diskless QEMU VM.

Test-only debconf commands simulate component transitions, including a real error
question. This checks native rendering, not a complete installation.
"""
import argparse
import json
import os
from pathlib import Path
import runpy
import subprocess
import time
from PIL import Image, ImageChops

helpers = runpy.run_path(str(Path(__file__).with_name('iso-smoke.py')))
qmp, archive = helpers['qmp'], helpers['archive']
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('iso', type=Path)
parser.add_argument('--kernel', type=Path, required=True)
parser.add_argument('--initrd', type=Path, required=True)
parser.add_argument('--output', type=Path, required=True)
args = parser.parse_args()
for name in ('iso', 'kernel', 'initrd'):
    path = getattr(args, name).resolve(strict=True)
    assert path.is_file(), f'{name} must be a regular file'
    setattr(args, name, path)
output = args.output.resolve()
output.mkdir(parents=True, exist_ok=False)
script = b'''#!/bin/sh
. /usr/share/debconf/confmodule
set -eu
trap 'echo NEX_PROGRESS_FAILED >/dev/ttyS0' EXIT
db_x_loadtemplatefile /nex-progress.templates nex-progress
# The shell remains alive between progress bars, as main-menu does.
db_progress START 0 100 nex-progress/title
db_progress INFO nex-progress/info
db_progress SET 40
echo NEX_PROGRESS_ACTIVE >/dev/ttyS0
sleep 8
db_progress STOP
echo NEX_PROGRESS_GAP >/dev/ttyS0
sleep 8
printf '%s\n' 'NEX_TEST_LOG: the simulated storage operation failed.' >/var/log/nex-installer-prepare.log
db_input critical nex-progress/error
echo NEX_PROGRESS_PROMPT >/dev/ttyS0
db_go
echo NEX_PROGRESS_ACKNOWLEDGED >/dev/ttyS0
db_progress START 0 100 nex-progress/title
db_progress INFO nex-progress/resume
db_progress SET 60
echo NEX_PROGRESS_RESUMED >/dev/ttyS0
sleep 8
db_fset nex-progress/error seen false
db_input critical nex-progress/error
echo NEX_PROGRESS_NESTED_PROMPT >/dev/ttyS0
db_go
echo NEX_PROGRESS_NESTED_RESUMED >/dev/ttyS0
sleep 8
db_progress STOP
echo NEX_PROGRESS_FINAL_GAP >/dev/ttyS0
sleep 8
db_fset finish-install/reboot_in_progress seen false
db_input critical finish-install/reboot_in_progress
echo NEX_PROGRESS_COMPLETION >/dev/ttyS0
db_go
echo NEX_PROGRESS_RESTART_REQUESTED >/dev/ttyS0
echo NEX_PROGRESS_PASS >/dev/ttyS0
trap - EXIT
sleep 300
'''
templates = b'''Template: nex-progress/title
Type: text
Description: Installing Nex Core

Template: nex-progress/info
Type: text
Description: Configuring the disposable test environment

Template: nex-progress/resume
Type: text
Description: Continuing after the test prompt

Template: finish-install/reboot_in_progress
Type: note
Description: Installation complete
 The test completed.

Template: nex-progress/error
Type: error
Description: Test installation requires attention
 This is a test-only error. Press Continue to resume installation progress.

'''
hook = b'export DEBIAN_FRONTEND=gtk\nexec /usr/lib/cdebconf/debconf -- /nex-progress.sh\n'
initrd = output / 'test-only-initrd.gz'
initrd.write_bytes(args.initrd.read_bytes() + archive({
    'usr/lib/debian-installer.d/S66nex-wizard': hook,
    'nex-progress.sh': script, 'nex-progress.templates': templates,
}, {'nex-progress.sh': 0o100755}))
sock, serial = output / 'qmp.sock', output / 'serial.log'
accel = 'kvm' if os.access('/dev/kvm', os.R_OK | os.W_OK) else 'tcg'
command = ['qemu-system-x86_64', '-accel', accel, '-cpu', 'host' if accel == 'kvm' else 'max',
           '-m', '2048', '-smp', '2', '-display', 'none', '-monitor', 'none',
           '-qmp', f'unix:{sock},server=on,wait=off', '-serial', f'file:{serial}',
           '-no-reboot', '-vga', 'none', '-device', 'VGA,xres=1024,yres=768',
           '-device', 'qemu-xhci,id=xhci', '-device', 'usb-tablet,bus=xhci.0',
           '-nic', 'none', '-cdrom', str(args.iso),
           '-kernel', str(args.kernel), '-initrd', str(initrd), '-append', 'vga=791']
(output / 'command.json').write_text(json.dumps(command, indent=2) + '\n')


def wait(marker):
    deadline = time.monotonic() + 180
    while True:
        log = serial.read_text(errors='replace') if serial.exists() else ''
        assert 'NEX_PROGRESS_FAILED' not in log, 'Test protocol failed; inspect serial.log'
        if marker in log:
            return
        assert process.poll() is None, 'QEMU exited; inspect qemu.log'
        assert time.monotonic() < deadline, f'Timed out waiting for {marker}'
        time.sleep(0.2)


def capture(name):
    time.sleep(0.5)
    ppm = output / f'{name}.ppm'
    qmp(sock, 'screendump', {'filename': str(ppm)})
    image = Image.open(ppm).convert('RGB')
    image.save(output / f'{name}.png')
    assert image.size == (1024, 768)
    return image


def key(*codes):
    for down in (True, False):
        qmp(sock, 'input-send-event', {'events': [
            {'type': 'key', 'data': {'down': down, 'key': {'type': 'qcode', 'data': code}}}
            for code in (codes if down else reversed(codes))]})
        time.sleep(0.08)


def continue_prompt():
    qmp(sock, 'input-send-event', {'events': [
        {'type': 'abs', 'data': {'axis': 'x', 'value': round(950 * 32767 / 1023)}},
        {'type': 'abs', 'data': {'axis': 'y', 'value': round(695 * 32767 / 767)}}]})
    for down in (True, False):
        qmp(sock, 'input-send-event', {'events': [
            {'type': 'btn', 'data': {'down': down, 'button': 'left'}}]})
        time.sleep(0.1)


def content_pixels(image):
    # Exclude the permanent title/purpose/sidebar/footer: only the main body
    # distinguishes the old empty target box from a visible transition page.
    return sum(max(image.getpixel((x, y))) < 170
               for y in range(192, 620) for x in range(162, 1000))


def check_progress_panel(active, gap, resumed, final_gap):
    # Inspect the actual framebuffer: every phase must use the same flat track,
    # with the backend's 40%/60% fractions and no stale fill between components.
    frames = (active, gap, resumed, final_gap)
    track_color, fill_color = (225, 232, 242), (36, 86, 217)
    bounds = []
    for frame, fraction in zip(frames, (0.4, 0, 0.6, 0)):
        pixels = frame.load()
        rows = []
        for y in range(190, 620):
            xs = [x for x in range(162, 1010)
                  if pixels[x, y] in (track_color, fill_color)]
            if len(xs) > 400:
                assert xs[-1] - xs[0] + 1 - len(xs) <= 1, 'Progress track has gaps or native decorations'
                rows.append((y, xs[0], xs[-1] + 1))
        assert len(rows) == 10, 'Expected one flat 10-pixel Nex progress track in every phase'
        y, left, right = rows[0]
        assert rows == [(y + offset, left, right) for offset in range(10)], 'Progress track is not rectangular'
        bounds.append((left, y, right, y + 10))
        fill = sum(pixels[x, y + 5] == fill_color for x in range(left, right))
        assert abs(fill - (right - left) * fraction) <= 1, 'Track does not show the real component fraction'
    assert len(set(bounds)) == 1, 'Progress panel moves between active, waiting and resumed steps'
    # Branding and the installation heading must not disappear during START.
    for frame in frames[1:]:
        assert not ImageChops.difference(active.crop((0, 0, 1024, 190)),
                                        frame.crop((0, 0, 1024, 190))).getbbox(), 'Installation header changes between steps'
    # INFO must display the current component detail, then clear it at STOP.
    left, top, right, bottom = bounds[0]
    detail = (left, bottom + 8, right, bottom + 44)
    assert ImageChops.difference(active.crop(detail), resumed.crop(detail)).getbbox(), 'Component detail text did not update'
    assert ImageChops.difference(active.crop(detail), gap.crop(detail)).getbbox(), 'Component detail text stayed visible during a gap'
    # The permanent power reminder below the detail text stays in place too.
    bottom = bounds[0][3] + 50
    for frame in frames[1:]:
        assert not ImageChops.difference(active.crop((150, bottom, 1000, bottom + 28)),
                                        frame.crop((150, bottom, 1000, bottom + 28))).getbbox(), 'Progress panel footer moves between steps'
    return bounds[0]


with (output / 'qemu.log').open('w') as log:
    process = subprocess.Popen(command, stdout=log, stderr=log)
    try:
        wait('NEX_PROGRESS_ACTIVE')
        active = capture('active-progress')
        wait('NEX_PROGRESS_GAP')
        gap = capture('between-components')
        wait('NEX_PROGRESS_PROMPT')
        prompt = capture('error-prompt')
        key('alt', 'v')
        log_view = capture('error-log')
        changed = ImageChops.difference(prompt, log_view)
        assert sum(changed.getpixel((x, y)) != (0, 0, 0)
                   for y in range(100, 700, 2) for x in range(150, 1000, 2)) > 2000, 'View log did not open'
        assert 'NEX_PROGRESS_ACKNOWLEDGED' not in serial.read_text(errors='replace'), 'Opening the log dismissed the error'
        key('ctrl', 'home')
        log_start = capture('error-log-start')
        area = (154, 160, 845, 600)
        assert ImageChops.difference(log_view.crop(area), log_start.crop(area)).getbbox(), 'Log did not open at recent entries or keyboard scrolling failed'
        key('ctrl', 'end')
        log_tail = capture('error-log-tail')
        difference = ImageChops.difference(log_view.crop(area), log_tail.crop(area))
        assert sum(pixel != (0, 0, 0) for pixel in difference.getdata()) < 100, 'Log did not initially display its latest entries'
        key('esc')
        closed_log = capture('error-log-closed')
        # Focus can move to View log on close; compare the reason, not buttons.
        reason = (162, 120, 1000, 270)
        assert not ImageChops.difference(prompt.crop(reason), closed_log.crop(reason)).getbbox(), 'Closing log did not restore the underlying error'
        continue_prompt()
        wait('NEX_PROGRESS_ACKNOWLEDGED')
        wait('NEX_PROGRESS_RESUMED')
        resumed = capture('resumed-progress')
        wait('NEX_PROGRESS_NESTED_PROMPT')
        nested_prompt = capture('nested-error-prompt')
        assert ImageChops.difference(resumed, nested_prompt).getbbox(), 'Error inside active progress was hidden'
        continue_prompt()
        wait('NEX_PROGRESS_NESTED_RESUMED')
        nested_resumed = capture('nested-resumed-progress')
        assert not ImageChops.difference(resumed.crop((150, 100, 1000, 360)),
                                        nested_resumed.crop((150, 100, 1000, 360))).getbbox(), 'Active progress did not recover after nested error'
        wait('NEX_PROGRESS_FINAL_GAP')
        final_gap = capture('final-transition')
        wait('NEX_PROGRESS_COMPLETION')
        completion = capture('installation-complete')
        time.sleep(2)
        assert 'NEX_PROGRESS_RESTART_REQUESTED' not in serial.read_text(errors='replace'), 'Completion did not wait for acknowledgement'
        key('ret')
        wait('NEX_PROGRESS_RESTART_REQUESTED')
        wait('NEX_PROGRESS_PASS')
        counts = {name: content_pixels(image) for name, image in (
            ('active', active), ('gap', gap), ('prompt', prompt),
            ('resumed', resumed), ('final_gap', final_gap), ('completion', completion))}
        (output / 'results.json').write_text(json.dumps(counts, indent=2) + '\n')
        assert counts['active'] > 100, 'Progress content is missing'
        assert counts['gap'] > 100 and counts['final_gap'] > 100, 'Main installation section went blank between components'
        assert counts['prompt'] > 100, 'Error prompt was hidden'
        assert counts['completion'] > 100, 'Completion prompt was hidden'
        assert not ImageChops.difference(gap.crop((150, 100, 1000, 360)), final_gap.crop((150, 100, 1000, 360))).getbbox(), 'Transition retained stale component progress'
        track_bounds = check_progress_panel(active, gap, resumed, final_gap)
        (output / 'panel.json').write_text(json.dumps({'track_bounds': track_bounds, 'fractions': [0.4, 0, 0.6, 0]}, indent=2) + '\n')
        diagnostics = serial.read_text(errors='replace')
        assert 'Gtk-CRITICAL' not in diagnostics and 'GLib-GObject-CRITICAL' not in diagnostics, 'GTK lifecycle error in transition'
        assert ImageChops.difference(gap, prompt).getbbox(), 'Error did not replace waiting page'
        assert ImageChops.difference(gap, resumed).getbbox(), 'Progress did not resume after acknowledgement'
        print(f'Native diskless progress transitions and error acknowledgement passed: {output}')
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=15)
        sock.unlink(missing_ok=True)
