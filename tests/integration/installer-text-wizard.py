#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Drive installer/gui/wizard-text.sh against a fixture machine.

The text frontend exists so a minimal installation produces the same host as a
graphical one. It does that by writing the one /run/nex-installer/config.ini
handoff that nex-config validates and applies, so this check exercises the
question order, Go Back navigation and the exact handoff the wizard emits.

No real device, network interface or disk is read or written: every path the
wizard touches hangs off NEX_WIZARD_ROOT.
"""
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
WIZARD = REPO / 'installer/gui/wizard-text.sh'

# A debconf stub: it answers scripted questions in order, refuses any question the
# script does not expect, and records what the wizard asked and substituted.
CONFMODULE = r'''
_nex_answers=$NEX_TEST_ANSWERS
_nex_log=$NEX_TEST_LOG
_nex_line=0
_nex_current=
RET=
db_x_loadtemplatefile() { :; }
db_capb() { :; }
db_settitle() { :; }
db_fset() { :; }
db_progress() { :; }
db_set() { :; }
db_subst() { printf 'SUBST %s %s %s\n' "$1" "$2" "$3" >> "$_nex_log"; }
db_input() { _nex_current=$2; printf 'INPUT %s\n' "$2" >> "$_nex_log"; }
db_go() {
    _nex_line=$((_nex_line + 1))
    entry=$(sed -n "${_nex_line}p" "$_nex_answers")
    template=${entry%% *}
    answer=${entry#* }
    if [ "$template" != "$_nex_current" ]; then
        printf 'UNEXPECTED asked %s, script line %s is %s\n' \
            "$_nex_current" "$_nex_line" "${template:-<end of script>}" >> "$_nex_log"
        exit 99
    fi
    [ "$answer" = "<BACK>" ] && return 30
    RET=$answer
    return 0
}
db_get() { :; }
'''

# nex-config reads the handoff with GKeyFile, which unescapes \\ and \s. A golden
# string alone would only prove the wizard still emits what it emitted before, so
# decode the file the way the consumer does and compare against the typed answers.
KEYFILE_READER = r"""
#include <glib.h>
#include <string.h>
int main(int argc, char **argv)
{
    GKeyFile *keys = g_key_file_new();
    GError *error = NULL;
    if (!g_key_file_load_from_file(keys, argv[1], G_KEY_FILE_NONE, &error)) {
        g_printerr("%s\n", error->message);
        return 1;
    }
    g_print("%s\n", g_key_file_get_string(keys, "Host", "password", NULL));
    g_print("%s\n", g_key_file_get_string(keys, "Network", "dns", NULL));
    g_print("%d\n", g_key_file_get_integer(keys, "NIC:eth0", "vlan", NULL));
    g_print("%" G_GUINT64_FORMAT "\n", g_key_file_get_uint64(keys, "Host", "disk_size", NULL));
    return 0;
}
"""

NICS = {
    'eth0': {'address': '52:54:00:aa:bb:01', 'carrier': '1'},
    'eth1': {'address': '52:54:00:aa:bb:02', 'carrier': '0'},
}

# One offered disk. Everything else is rejected for a specific reason.
BLOCKS = {
    'sda': {'size': '67108864', 'ro': '0', 'removable': '0', 'dev': '8:0',
            'device/serial': 'TESTSERIAL1', 'device/model': 'QEMU HARDDISK'},
    'sdb': {'size': '2048', 'ro': '0', 'removable': '0', 'dev': '8:16'},          # under 16 GiB
    'sdc': {'size': '67108864', 'ro': '0', 'removable': '1', 'dev': '8:32'},      # removable
    'sde': {'size': '67108864', 'ro': '1', 'removable': '0', 'dev': '8:64'},      # read-only
    'sdd': {'size': '67108864', 'ro': '0', 'removable': '0', 'dev': '8:48'},      # installation media
}

PASSWORD = r'pass word\one'

SCRIPT = [
    'nex-installer/hostname wrong.example.test',
    'nex-installer/keymap <BACK>',
    'nex-installer/hostname nexcore.example.test',
    'nex-installer/keymap gb',
    'nex-installer/timezone-area UTC',
    # UTC has no zone question, and Go Back from here must not bounce off it.
    'nex-installer/password <BACK>',
    'nex-installer/timezone-area Australia',
    'nex-installer/timezone Australia/Sydney',
    f'nex-installer/password {PASSWORD}',
    f'nex-installer/password-again {PASSWORD}',
    'nex-installer/management eth0 - 52:54:00:aa:bb:01 - link detected',
    'nex-installer/mode static',
    'nex-installer/vlan 10',
    'nex-installer/address 192.0.2.10/24',
    'nex-installer/gateway 192.0.2.1',
    'nex-installer/mode disabled',
    'nex-installer/dns 192.0.2.53 192.0.2.54',
    'nex-installer/disk sda - 32 GiB - QEMU HARDDISK',
    'nex-installer/erase false',
    'nex-installer/disk sda - 32 GiB - QEMU HARDDISK',
    'nex-installer/erase true',
]

EXPECTED_CONFIG = r'''[Host]
hostname=nexcore.example.test
timezone=Australia/Sydney
keymap=gb
password=pass\sword\\one
disk=/dev/sda
disk_size=34359738368
disk_serial=TESTSERIAL1
disk_major_minor=8:0

[Network]
management=eth0
dns=192.0.2.53 192.0.2.54

[NIC:eth0]
mac=52:54:00:aa:bb:01
mode=static
address=192.0.2.10/24
gateway=192.0.2.1
vlan=10

[NIC:eth1]
mac=52:54:00:aa:bb:02
mode=disabled
address=
gateway=
vlan=0
'''


def write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text)


def build_fixture(root):
    for nic, attributes in NICS.items():
        base = root / 'sys/class/net' / nic
        (base / 'device').mkdir(parents=True)
        for name, value in attributes.items():
            write(base / name, value + '\n')
    # Kernel-only interfaces have no device directory and are never offered.
    (root / 'sys/class/net/lo').mkdir(parents=True)
    write(root / 'sys/class/net/lo/address', '00:00:00:00:00:00\n')

    for name, attributes in BLOCKS.items():
        base = root / 'sys/class/block' / name
        for attribute, value in attributes.items():
            write(base / attribute, value + '\n')
    # The mounted media partition and the whole disk behind it are both excluded.
    partition = root / 'sys/block/sdd/sdd1'
    write(partition / 'partition', '1\n')
    write(partition / 'size', '67108864\n')
    (root / 'sys/class/block/sdd1').symlink_to(partition)
    write(root / 'proc/mounts', '/dev/sdd1 /cdrom iso9660 ro 0 0\n')

    write(root / 'usr/share/nex-installer/timezones',
          'UTC\nAustralia/Sydney\nEurope/Paris\n')
    write(root / 'usr/share/nex-installer/wizard-text.templates', '')
    write(root / 'usr/share/debconf/confmodule', CONFMODULE)
    (root / 'run/nex-installer').mkdir(parents=True)
    write(root / 'run/nex-installer/prepared', '')


def decode_with_keyfile(root, config):
    """Read the handoff exactly as nex-config does, or None when glib is absent."""
    compiler = shutil.which('cc') or shutil.which('gcc')
    flags = subprocess.run(['pkg-config', '--cflags', '--libs', 'glib-2.0'],
                           capture_output=True, text=True)
    if not compiler or flags.returncode != 0:
        return None
    source = root / 'keyfile-reader.c'
    binary = root / 'keyfile-reader'
    source.write_text(KEYFILE_READER)
    build = subprocess.run([compiler, str(source), '-o', str(binary)] + flags.stdout.split(),
                           capture_output=True, text=True)
    assert build.returncode == 0, build.stderr
    read = subprocess.run([str(binary), str(config)], capture_output=True, text=True)
    assert read.returncode == 0, read.stderr
    return read.stdout.splitlines()


def run(root, answers, log):
    environment = dict(os.environ,
                       NEX_WIZARD_ROOT=str(root),
                       NEX_TEST_ANSWERS=str(answers),
                       NEX_TEST_LOG=str(log))
    return subprocess.run(['sh', str(WIZARD)], env=environment,
                          capture_output=True, text=True)


def main():
    if not shutil.which('sh'):
        sys.exit('sh is required')
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        build_fixture(root)
        answers = root / 'answers'
        log = root / 'log'
        write(answers, '\n'.join(SCRIPT) + '\n')
        log.touch()
        result = run(root, answers, log)
        entries = log.read_text().splitlines()
        assert result.returncode == 0, (result.returncode, result.stderr, entries[-5:])

        asked = [line.split(' ', 1)[1] for line in entries if line.startswith('INPUT ')]
        expected = [line.split(' ', 1)[0] for line in SCRIPT]
        assert asked == expected, (asked, expected)

        substitutions = {}
        for line in entries:
            if line.startswith('SUBST '):
                _, template, key, value = line.split(' ', 3)
                substitutions[(template, key)] = value
        # Only the one supported disk is offered: not the small, removable,
        # read-only, partition or installation-media devices.
        assert substitutions[('nex-installer/disk', 'choices')] == \
            'sda - 32 GiB - QEMU HARDDISK', substitutions
        assert substitutions[('nex-installer/management', 'choices')] == \
            'eth0 - 52:54:00:aa:bb:01 - link detected, ' \
            'eth1 - 52:54:00:aa:bb:02 - no link', substitutions
        # The confirmation names the disk it erases and reports the reviewed settings.
        assert substitutions[('nex-installer/erase', 'disk')] == '/dev/sda'
        assert substitutions[('nex-installer/erase', 'hostname')] == 'nexcore.example.test'
        assert substitutions[('nex-installer/erase', 'management')] == 'eth0 (static)'
        assert substitutions[('nex-installer/erase', 'serial')] == 'TESTSERIAL1'
        # A management adapter cannot be switched off; another one has no gateway.
        assert substitutions[('nex-installer/mode', 'choices')] == 'disabled, dhcp, static'

        config = root / 'run/nex-installer/config.ini'
        actual = config.read_text()
        assert actual == EXPECTED_CONFIG, (actual, EXPECTED_CONFIG)
        assert config.stat().st_mode & 0o077 == 0, oct(config.stat().st_mode)
        assert not (root / 'run/nex-installer/config.ini.new').exists()

        decoded = decode_with_keyfile(root, config)
        if decoded is None:
            print('SKIP: no C compiler with glib-2.0; GKeyFile decoding was not checked')
        else:
            assert decoded == [PASSWORD, '192.0.2.53 192.0.2.54', '10', '34359738368'], decoded

    # An operator who declines the erase confirmation and then quits leaves no handoff.
    with tempfile.TemporaryDirectory() as directory:
        root = Path(directory)
        build_fixture(root)
        answers = root / 'answers'
        log = root / 'log'
        write(answers, 'nex-installer/hostname <BACK>\n')
        log.touch()
        result = run(root, answers, log)
        assert result.returncode == 99, (result.returncode, log.read_text())
        assert not (root / 'run/nex-installer/config.ini').exists()

    print('PASS: text wizard question order, Go Back, device filtering and handoff')


if __name__ == '__main__':
    main()
