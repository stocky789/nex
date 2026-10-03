#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Restart nex-core-local on a lab host and require it to keep reporting to its Controller.

Regression for BUG-1908: with a DHCP-deployed Controller the embedded agent was stopped as
soon as the Core service came back, so the Controller showed the host stale although both
were healthy. A unit test cannot stand in for the Controller here (guest-agent discovery
fixes port 8443 and refuses loopback), so this runs against a disposable lab pair.

Environment (nothing is stored in the repository):
  NEX_LAB_CORE         ssh target of the lab Core, for example root@core.lab.example
  NEX_LAB_CONTROLLER   ssh target of the lab Controller appliance
  NEX_LAB_SSHPASS      optional password; keys are used when unset (needs sshpass)
Restarts nex-core-local on the selected Core only; guests are untouched.
"""
import datetime
import os
import subprocess
import sys
import time

SSH = ['-o', 'StrictHostKeyChecking=accept-new', '-o', 'LogLevel=ERROR', '-o', 'ConnectTimeout=8']
LAST_SEEN = (
    "import sqlite3;"
    "print(sqlite3.connect('file:/var/lib/nex-controller/controller.db?mode=ro',uri=True)"
    ".execute('select last_seen from hosts').fetchall()[0][0])"
)


def ssh(target, command):
    argv = ['ssh', *SSH, target, command]
    if os.environ.get('NEX_LAB_SSHPASS'):
        argv = ['sshpass', '-e', *argv]
    return subprocess.run(argv, check=True, capture_output=True, text=True, timeout=60).stdout.strip()


def report_age(controller):
    seen = ssh(controller, f'python3 -c "{LAST_SEEN}"')
    when = datetime.datetime.fromisoformat(seen.replace('Z', '+00:00'))
    return seen, (datetime.datetime.now(datetime.UTC) - when).total_seconds()


def main():
    core, controller = os.environ.get('NEX_LAB_CORE'), os.environ.get('NEX_LAB_CONTROLLER')
    if not core or not controller:
        sys.exit('set NEX_LAB_CORE and NEX_LAB_CONTROLLER to a disposable lab pair')
    if os.environ.get('NEX_LAB_SSHPASS'):
        os.environ['SSHPASS'] = os.environ['NEX_LAB_SSHPASS']
    before, age = report_age(controller)
    if age > 10:
        sys.exit(f'precondition failed: host already stale before the restart (last report {age:.0f}s ago)')
    ssh(core, 'systemctl restart nex-core-local')
    # A report must arrive after the restart and then keep arriving: a restarted agent that
    # reports once and stops, or never starts, is the failure this guards.
    deadline = time.time() + 90
    while time.time() < deadline:
        seen, age = report_age(controller)
        if seen != before and age < 10:
            break
        time.sleep(3)
    else:
        sys.exit('FAIL: no report reached the Controller within 90s of the Core restart')
    time.sleep(20)
    seen_later, age = report_age(controller)
    if seen_later == seen or age > 10:
        sys.exit(f'FAIL: reporting stopped after the restart (last report {age:.0f}s ago)')
    print('PASS: host kept reporting after nex-core-local restart')


if __name__ == '__main__':
    main()
