#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise native fullscreen wizard setup and require explicit disk-erasure confirmation.

Requires QEMU and Pillow. Only a newly created regular-file disk is attached.
The test-only prepare marker is appended to the supplied shipping initrd.
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
parser.add_argument('--nics', type=int, choices=(0, 2), default=0)
parser.add_argument('--capture-progress', action='store_true', help='After rejection checks, install onto the fresh disposable file disk and capture backend progress')
parser.add_argument('--capture-completion', action='store_true', help='Install onto the disposable file disk, verify the success screen waits, then click Restart now and require a natural reboot')
parser.add_argument('--resolution', choices=('800x600', '1024x768'), default='800x600')
args = parser.parse_args()
width, height = map(int, args.resolution.split('x'))
footer = height - 57
for name in ('iso', 'kernel', 'initrd'):
    path = getattr(args, name).resolve(strict=True)
    assert path.is_file(), f'{name} must be a regular file'
    setattr(args, name, path)
output = args.output.resolve()
output.mkdir(parents=True, exist_ok=False)
disk = output / 'disposable.raw'
with disk.open('xb') as stream:
    stream.truncate(24 * 1024**3)
prepare = Path('installer/gui/prepare.sh').read_bytes() + b"\nif grep -q '^PROGRESS ' /var/log/nex-installer-prepare.log; then exit 1; fi\necho NEX_QA_PREPARED >/dev/ttyS0\n"
if args.nics:
    prepare += b"""
# Observe only: no address/client/route configuration in the test overlay.
sleep 3
ip -o addr show > /run/nex-qa-addresses
ip route show > /run/nex-qa-routes
ip -6 route show >> /run/nex-qa-routes
cat /run/nex-qa-addresses /run/nex-qa-routes >/dev/ttyS0
if grep -v ' lo ' /run/nex-qa-addresses | grep -q 'inet'; then
    echo NEX_QA_UNEXPECTED_ADDRESS >/dev/ttyS0
fi
if test -s /run/nex-qa-routes; then echo NEX_QA_UNEXPECTED_ROUTE >/dev/ttyS0; fi
echo NEX_QA_LINK_PROBE_CHECKED >/dev/ttyS0
"""
initrd = output / 'test-only-initrd.gz'
prepare_path = 'usr/lib/nex-installer/prepare'
overlay = {prepare_path: prepare}
if args.capture_completion:
    # Observe the normal finish-install sequence without replacing any installer
    # operation or manufacturing a successful completion.
    overlay['usr/lib/finish-install.d/19nex-qa-final-prompt'] = b'#!/bin/sh\necho NEX_QA_FINAL_PROMPT_READY >/dev/ttyS0\n'
initrd.write_bytes(args.initrd.read_bytes() + archive(overlay, {path: 0o100755 for path in overlay}))
sock, serial = output / 'qmp.sock', output / 'serial.log'
accel = 'kvm' if os.access('/dev/kvm', os.R_OK | os.W_OK) else 'tcg'
command = ['qemu-system-x86_64', '-accel', accel, '-cpu', 'host' if accel == 'kvm' else 'max',
           '-m', '4096', '-smp', '2', '-display', 'none', '-monitor', 'none',
           '-qmp', f'unix:{sock},server=on,wait=off', '-serial', f'file:{serial}',
           '-no-reboot', '-vga', 'none', '-device', f'VGA,xres={width},yres={height}',
           '-device', 'qemu-xhci,id=xhci', '-device', 'usb-tablet,bus=xhci.0',
           '-drive', f'file={disk},format=raw,if=virtio', '-cdrom', str(args.iso),
           '-kernel', str(args.kernel), '-initrd', str(initrd),
           '-append', 'vga=788' if width == 800 else 'vga=791']
command.extend(['-nic', 'none'] if not args.nics else [
    '-nic', 'user,id=nexqa0,model=virtio-net-pci,mac=52:54:00:12:34:10',
    '-nic', 'user,id=nexqa1,model=virtio-net-pci,mac=52:54:00:12:34:11'])
(output / 'command.json').write_text(json.dumps(command, indent=2) + '\n')


def click(x, y):
    qmp(sock, 'input-send-event', {'events': [
        {'type': 'abs', 'data': {'axis': 'x', 'value': round(x * 32767 / (width - 1))}},
        {'type': 'abs', 'data': {'axis': 'y', 'value': round(y * 32767 / (height - 1))}}]})
    for down in (True, False):
        qmp(sock, 'input-send-event', {'events': [{'type': 'btn', 'data': {'down': down, 'button': 'left'}}]})
        time.sleep(0.15)
    time.sleep(0.5)


def key(*codes):
    for down in (True, False):
        qmp(sock, 'input-send-event', {'events': [
            {'type': 'key', 'data': {'down': down, 'key': {'type': 'qcode', 'data': code}}}
            for code in (codes if down else reversed(codes))]})
        time.sleep(0.06)


def type_text(value):
    for char in value:
        code = {' ': 'spc', '_': 'minus', '-': 'minus', '.': 'dot', '/': 'slash'}.get(char, char.lower())
        key(*(['shift', code] if char.isupper() or char == '_' else [code]))


def field(mnemonic, text):
    key('alt', mnemonic)
    key('ctrl', 'a')
    type_text(text)
    time.sleep(0.5)


def capture(name):
    ppm = output / (name + '.ppm')
    qmp(sock, 'screendump', {'filename': str(ppm)})
    image = Image.open(ppm).convert('RGB')
    image.save(output / (name + '.png'))
    return image


def dark_pixels(image, bounds):
    return sum(max(image.getpixel((x, y))) < 140
               for y in range(bounds[1], bounds[3]) for x in range(bounds[0], bounds[2]))


def red_pixels(image, bounds):
    return sum(r > 150 and g < 100 and b < 100
               for y in range(bounds[1], bounds[3]) for x in range(bounds[0], bounds[2])
               for r, g, b in [image.getpixel((x, y))])


with (output / 'qemu.log').open('w') as log:
    process = subprocess.Popen(command, stdout=log, stderr=log)
    try:
        deadline = time.monotonic() + 180
        ready_marker = 'NEX_QA_LINK_PROBE_CHECKED' if args.nics else 'NEX_QA_PREPARED'
        while not serial.exists() or ready_marker not in serial.read_text(errors='replace'):
            assert process.poll() is None, 'QEMU exited; inspect qemu.log'
            assert time.monotonic() < deadline, 'Hardware preparation timed out; inspect serial.log'
            time.sleep(1)
        time.sleep(3)
        welcome = capture('welcome')
        assert welcome.size == (width, height), 'Unexpected native display resolution'
        for corner in ((width - 5, 5), (5, height - 5), (width - 5, height - 5)):
            assert welcome.getpixel(corner) == welcome.getpixel((5, 5)), 'Installer leaves desktop exposed; no window manager maximizes it'
        click(width - 60, footer)
        field('h', 'nexcore.matthome')
        field('r', 'disposabletest')
        field('p', 'disposabletest')
        # Both timezone controls are native, noneditable dropdowns. Select using
        # arrows only; no timezone string is entered into any text field.
        key('alt', 'a'); key('home')
        for _ in range(7): key('down')  # UTC then alphabetic areas -> Australia.
        key('alt', 't'); key('end')  # Last Australia entry is Sydney.
        key('alt', 'down')
        capture('timezone-dropdown')
        key('esc')
        capture('timezone-selected')
        key('alt', 's')
        visible = capture('passwords-visible')
        key('alt', 's')
        hidden = capture('passwords-hidden')
        password_bounds = (308, 325, 550, 392)
        assert dark_pixels(hidden, password_bounds) > dark_pixels(visible, password_bounds) * 1.2, 'Both masked password rows should replace visible text with bullets'
        key('alt', 's')  # Leave Host with disclosure ON; navigation must reset it.
        click(width - 60, footer)
        network = capture('network')
        assert red_pixels(network, (145, 70, width - 18, height - 40)) == 0, 'Host validation still blocks network page'
        if args.nics:
            probe_log = serial.read_text(errors='replace')
            assert 'NEX_QA_LINK_PROBE_CHECKED' in probe_log, 'Missing address/route observation'
            assert 'NEX_QA_UNEXPECTED_' not in probe_log, 'Link probing configured an address or route'
            # Partially entered settings must survive the live carrier refresh.
            click(500, 269); key('end'); key('ret')
            field('a', '172.')
            key('alt', 'h')  # Keep the blinking cursor outside the NIC editor crop.
            time.sleep(2.5)
            linked = capture('nic-link-detected')
            # Toggle both backends: discovery ordering is not an identity contract.
            for name in ('nexqa0', 'nexqa1'):
                qmp(sock, 'set_link', {'name': name, 'up': False})
            time.sleep(3)
            unplugged = capture('nic-no-link')
            label_bounds = (300, 188, width - 35, 218)
            assert ImageChops.difference(linked.crop(label_bounds), unplugged.crop(label_bounds)).getbbox(), 'Carrier loss did not update visible status'
            editor_bounds = (300, 255, width - 35, 410)
            assert not ImageChops.difference(linked.crop(editor_bounds), unplugged.crop(editor_bounds)).getbbox(), 'Carrier refresh replaced in-progress NIC settings'
            for name in ('nexqa0', 'nexqa1'):
                qmp(sock, 'set_link', {'name': name, 'up': True})
            time.sleep(3)
            recovered = capture('nic-link-recovered')
            assert not ImageChops.difference(linked.crop(label_bounds), recovered.crop(label_bounds)).getbbox(), 'Link recovery did not restore detected status'
            assert not ImageChops.difference(linked.crop(editor_bounds), recovered.crop(editor_bounds)).getbbox(), 'Link recovery replaced in-progress settings'
            click(500, 269); key('home'); key('down'); key('ret')  # Management DHCP.
            searching = capture('dhcp-searching')
            time.sleep(5)
            leased = capture('dhcp-address')
            address_bounds = (440, 292, width - 115, 318)
            assert ImageChops.difference(searching.crop(address_bounds), leased.crop(address_bounds)).getbbox(), 'DHCP search did not resolve visibly'
            for name in ('nexqa0', 'nexqa1'):
                qmp(sock, 'set_link', {'name': name, 'up': False})
            time.sleep(3)
            no_link = capture('dhcp-no-link')
            click(width - 55, 309)  # Retry remains safe while disconnected.
            time.sleep(1)
            capture('dhcp-retry-no-link')
            assert ImageChops.difference(leased.crop(address_bounds), no_link.crop(address_bounds)).getbbox(), 'DHCP result stayed stale after link loss'
            for name in ('nexqa0', 'nexqa1'):
                qmp(sock, 'set_link', {'name': name, 'up': True})
            time.sleep(5)
            capture('dhcp-recovered')
            click(500, 269); key('end'); key('ret')
            key('alt', 'h')
            cleared = capture('dhcp-to-static')
            assert dark_pixels(cleared, address_bounds) < 10, 'DHCP status leaked into editable static address'
            click(500, 269); key('home'); key('down'); key('ret')
            click(500, 423); key('home'); key('down'); key('ret')  # Select first NIC for br0.
        field('h', '_invalid')
        click(width - 60, footer)
        invalid_host = capture('invalid-network-hostname')
        assert red_pixels(invalid_host, (145, 70, width - 18, height - 40)) > 100, 'Invalid network hostname must block navigation'
        field('h', '7nex-core.matthome.')
        click(193, footer)
        returned = capture('host-after-network-edit')
        assert dark_pixels(returned, password_bounds) > dark_pixels(visible, password_bounds) * 1.2, 'Password disclosure persisted after returning to Host'
        click(width - 60, footer)
        capture('network-hostname')
        click(width - 60, footer)
        click(300, 194)
        capture('disk')
        click(width - 60, footer)
        review = capture('review')
        assert review.size == (width, height), 'Unexpected native GTK display'
        # The former GtkLabel defect painted only HOST. Require visible text in
        # both network and erase-summary bands, not merely a page title/border.
        for name, bounds in [('network', (165, 200, width - 55, 270)), ('erase', (165, 280, width - 55, 365))]:
            count = dark_pixels(review, bounds)
            assert count > 300, f'{name} review content missing ({count} dark pixels)'
        click(width - 60, footer)
        rejected = capture('unchecked-confirmation')
        assert red_pixels(rejected, (18, height - 250, width - 20, height - 50)) > 100, 'Expected visible erase confirmation error'
        key('alt', 'e')
        checked = capture('checked-confirmation')
        checkbox_bounds = (164, height - 145, 186, height - 108)
        assert ImageChops.difference(rejected.crop(checkbox_bounds), checked.crop(checkbox_bounds)).getbbox(), 'Erase checkbox did not become checked'
        # Revisiting Review requires explicit consent again for the current disk.
        click(193, footer)
        click(width - 60, footer)
        capture('confirmation-reset')
        click(width - 60, footer)
        reset_rejected = capture('reset-confirmation-rejected')
        assert red_pixels(reset_rejected, (18, height - 250, width - 20, height - 50)) > 100, 'Erase consent persisted after leaving Review'
        with disk.open('rb') as stream:
            assert stream.read(1024 * 1024) == bytes(1024 * 1024), 'Unchecked erase confirmation wrote disk'
        if args.capture_progress or args.capture_completion:
            key('alt', 'e')
            click(width - 60, footer)
            time.sleep(35)
            capture('installation-progress')
        if args.capture_completion:
            deadline = time.monotonic() + 1200
            while 'NEX_QA_FINAL_PROMPT_READY' not in serial.read_text(errors='replace'):
                assert process.poll() is None, 'Installer exited before completion; inspect serial.log'
                if time.monotonic() >= deadline:
                    capture('completion-timeout')
                    raise AssertionError('Installation did not reach its final prompt; inspect completion-timeout.png and serial.log')
                time.sleep(1)
            time.sleep(3)
            assert process.poll() is None, 'Installer rebooted without waiting for Restart now'
            completed = capture('installation-complete')
            assert dark_pixels(completed, (200, 150, width - 20, height - 80)) > 300, 'Completion page is blank'
            click(width - 70, height - 70)
            try:
                process.wait(timeout=90)
            except subprocess.TimeoutExpired:
                capture('restart-timeout')
                raise AssertionError('Restart now did not cause a natural QEMU exit')
            assert process.returncode == 0, f'QEMU failed during restart: {process.returncode}'
            (output / 'completion-result.json').write_text(json.dumps({
                'final_prompt_reached': True, 'waited_for_restart': True,
                'restart_exited_naturally': True, 'qemu_returncode': process.returncode
            }, indent=2) + '\n')
        if args.capture_completion:
            print('PASS: native completion remained open and Restart now caused a natural reboot')
        print(f'PASS: native fullscreen, timezone dropdowns, password reset, shared hostname and disk rejection ({args.resolution}, {args.nics} NICs, {accel}); evidence: {output}')
    finally:
        if process.poll() is None:
            process.terminate()
            process.wait(timeout=15)
