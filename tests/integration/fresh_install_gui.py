#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Drive the real shipping 800x600 Nex graphical wizard on a prepared blank-file lab.

Run configure only after visually verifying its welcome screen; it stops at review.
Run install only after inspecting the recorded exact disposable target/confirmation.
Passwords are read privately and never captured visibly. No installer payload is edited.
"""
import argparse
import json
from pathlib import Path
import time
from fresh_install_lab import ROOT, key, type_text, click, capture, qmp, validate_target


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lab-dir', type=Path, required=True)
    p.add_argument('--stage', choices=('configure', 'install', 'restart', 'console-key'), required=True)
    p.add_argument('--evidence', type=Path, default=ROOT / 'docs/qa/fresh-install')
    args = p.parse_args()
    lab = validate_target(args.lab_dir)
    config = json.loads((lab / 'config.json').read_text())
    assert config['disposable']
    def shot(name):
        time.sleep(.5)
        capture(lab, args.evidence / (name + '.png'))
    def field(mnemonic, value):
        key(lab, 'alt', mnemonic); key(lab, 'ctrl', 'a'); type_text(lab, value)
    if args.stage == 'configure':
        shot('installer-welcome')
        click(lab, 740, 543); time.sleep(.5)
        field('h', config['hostname'])
        password = Path(config['root_password_file']).read_text()
        field('r', password); field('p', password)
        shot('installer-host')
        click(lab, 740, 543); time.sleep(.5)
        click(lab, 500, 284); key(lab, 'home'); key(lab, 'down'); key(lab, 'ret')
        time.sleep(6)
        click(lab, 500, 438); key(lab, 'home'); key(lab, 'down'); key(lab, 'ret')
        shot('installer-network')
        click(lab, 740, 543); time.sleep(.5)
        click(lab, 300, 194)
        shot('installer-target')
        click(lab, 740, 543)
        shot('installer-review')
        click(lab, 740, 543)
        shot('installer-confirmation-required')
        with (lab / 'host.raw').open('rb') as disk:
            assert disk.read(1024 * 1024) == bytes(1024 * 1024), 'unconfirmed disk selection wrote target'
        print('Configured actual wizard; target remains blank after unconfirmed install. Inspect review before install stage.')
    elif args.stage == 'install':
        with (lab / 'host.raw').open('rb') as disk:
            assert disk.read(1024 * 1024) == bytes(1024 * 1024), 'installation stage only accepts the still-blank new lab target'
        key(lab, 'alt', 'e')
        shot('installer-confirmed')
        click(lab, 740, 543)
        time.sleep(5)
        shot('installer-progress-start')
    elif args.stage == 'restart':
        shot('installer-complete')
        click(lab, 730, 530)
    else:
        # Installed console only: append this lab's public key, never copy an image
        # or edit a mounted disk. Root password remains masked by the login prompt.
        type_text(lab, 'root'); key(lab, 'ret'); time.sleep(1)
        type_text(lab, Path(config['root_password_file']).read_text()); key(lab, 'ret'); time.sleep(2)
        pub = (lab / 'id_ed25519.pub').read_text().strip()
        command = "umask 077; mkdir -p /root/.ssh; echo '" + pub + "' >> /root/.ssh/authorized_keys; chmod 600 /root/.ssh/authorized_keys"
        type_text(lab, command); key(lab, 'ret'); time.sleep(1)
        command = "sshd -T | grep permitrootlogin > /root/nex-qa-ssh-before; echo 'PermitRootLogin prohibit-password' > /etc/ssh/sshd_config.d/00-00-nex-qa-key-only.conf; sshd -t && systemctl reload ssh; sshd -T | grep permitrootlogin > /root/nex-qa-ssh-after"
        type_text(lab, command); key(lab, 'ret'); time.sleep(1)
        type_text(lab, 'clear'); key(lab, 'ret')
        shot('installed-root-console')


if __name__ == '__main__':
    main()
