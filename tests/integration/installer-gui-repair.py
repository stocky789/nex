#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Draw the graphical wizard's Repair image choice under Xvfb in a throwaway container and save screenshots.

The wizard reads /sys, /cdrom and /run/nex-installer by absolute path, so the container gets a synthetic /sys
(two 25 GiB virtio disks, one NIC), a synthetic medium whose `nex-image-install.sh probe` reports
a complete two-disk nex-data volume, and the wizard compiled from installer/gui. Nothing of the workstation's real
hardware is visible. SIMULATION of the display and the hardware only; the installer behind the wizard is not run.

  tests/integration/installer-gui-repair.py [--image IMAGE] [--out DIR]

Needs docker and an image with gcc, pkg-config, GTK 2 dev files, xvfb, xdotool, imagemagick (default nex-lab-gtk:phase6).
Exits non-zero unless Repair is offered only when a data volume is found and can be selected. Writes welcome-no-repair.png, welcome.png (the choice), repair-chosen.png (Repair image selected) and page1..4.png (Begin setup, then Continue).
"""
import argparse
import os
import subprocess
import sys
import tempfile

REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

INNER = r'''
set -eu
mkdir -p /cdrom/nex /run/nex-installer /usr/lib/nex-installer /usr/share/nex-installer /out
cat > /cdrom/nex/nex-image-install.sh <<'EOT'
#!/bin/sh
[ "$1" = probe ] && [ ! -e /tmp/norepair ] && printf 'layout=raid1\ndisks=/dev/vda,/dev/vdb\n'
EOT
cp /w/installer/assets/nex-core-lockup.png /usr/share/nex-installer/
printf 'UTC\nAustralia/Sydney\n' > /usr/share/nex-installer/timezones
printf '#!/bin/sh\nexit 0\n' > /usr/lib/nex-installer/prepare; chmod 755 /usr/lib/nex-installer/prepare
printf '#!/bin/sh\nexit 0\n' > /usr/lib/nex-installer/validate-config; chmod 755 /usr/lib/nex-installer/validate-config
touch /run/nex-installer/prepared
cc -O2 -Wall -Wextra -Wno-deprecated-declarations $(pkg-config --cflags gtk+-2.0) /w/installer/gui/nex-wizard.c /w/installer/gui/nex-network.c -o /tmp/nex-wizard $(pkg-config --libs gtk+-2.0)
export DISPLAY=:99
Xvfb :99 -screen 0 1024x768x24 >/tmp/xvfb.log 2>&1 &
sleep 2
# True only when compare prints a pixel count above 200: ImageMagick error text or a tiny change (a focus ring) is no difference.
differs() { n=$(compare -metric AE "$1" "$2" null: 2>&1 || true); case "$n" in ''|*[!0-9]*) return 1;; esac; [ "$n" -gt 200 ]; }
# Control: with no complete data volume the wizard must not offer Repair.
touch /tmp/norepair
/tmp/nex-wizard >/tmp/wizard.log 2>&1 &
WIZARD=$!
sleep 4
import -window root /out/welcome-no-repair.png
kill $WIZARD; wait $WIZARD 2>/dev/null || true
rm /tmp/norepair
/tmp/nex-wizard >/tmp/wizard.log 2>&1 &
sleep 4
import -window root /out/welcome.png
xdotool key alt+a
sleep 1
xdotool key Down
sleep 1
import -window root /out/repair-chosen.png
# The Repair choice appears only when a data volume is found, and Down selects it.
differs /out/welcome-no-repair.png /out/welcome.png || { echo "FAIL: Repair image is not offered when nex-data is found"; exit 1; }
differs /out/welcome.png /out/repair-chosen.png || { echo "FAIL: Down did not select Repair image"; exit 1; }
for i in 1 2 3 4; do
    xdotool mousemove 952 733 click 1
    sleep 2
    import -window root /out/page$i.png
done
differs /out/repair-chosen.png /out/page1.png || { echo "FAIL: Begin setup did not leave the welcome page"; exit 1; }
echo "PASS: Repair offered only with a data volume and selectable (screenshot comparison; the installer behind the wizard is not run)"
'''


def fake_sys(root):
    """/sys/class/block/vda,vdb and /sys/class/net/ens3 with the attributes the wizard reads."""
    for name in ("vda", "vdb"):
        d = os.path.join(root, "devices", name)
        os.makedirs(os.path.join(d, "queue"), exist_ok=True)
        for attr, val in (("size", str(25 * 2 ** 21)), ("ro", "0"), ("removable", "0"), ("dev", "254:%d" % (0 if name == "vda" else 16)),
                          ("serial", "LABSERIAL-" + name.upper()), ("queue/rotational", "0")):
            open(os.path.join(d, attr), "w").write(val + "\n")
        os.makedirs(os.path.join(root, "class/block"), exist_ok=True)
        os.symlink(os.path.join("../../devices", name), os.path.join(root, "class/block", name))
    n = os.path.join(root, "devices", "ens3")
    os.makedirs(os.path.join(n, "device"), exist_ok=True)
    for attr, val in (("address", "52:54:00:12:34:56"), ("carrier", "1"), ("flags", "0x1003"), ("speed", "1000")):
        open(os.path.join(n, attr), "w").write(val + "\n")
    os.makedirs(os.path.join(root, "class/net"), exist_ok=True)
    os.symlink("../../devices/ens3", os.path.join(root, "class/net/ens3"))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--image", default="nex-lab-gtk:phase6")
    ap.add_argument("--out", default=os.path.join(REPO, ".cache/phase6/gui"))
    a = ap.parse_args()
    out = os.path.abspath(a.out)
    os.makedirs(out, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="nex-fake-sys-") as sysroot:
        fake_sys(sysroot)
        cmd = ["docker", "run", "--rm", "--cpus", "1", "--memory", "1g", "-v", f"{REPO}:/w:ro", "-v", f"{out}:/out", "-v", f"{sysroot}:/sys:ro",
               "--entrypoint", "sh", a.image, "-c", INNER]
        res = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
    print(res.stdout[-800:], res.stderr[-800:])
    sys.exit(res.returncode)


if __name__ == "__main__":
    main()
