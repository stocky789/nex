#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Drive installer/gui/wizard-text.sh against a fixture machine.

The text frontend exists so a minimal installation produces the same host as a
graphical one. It does that by writing the one /run/nex-installer/config.ini
handoff that nex-config validates and applies, so this check exercises the
question order, Go Back navigation, keyboard and validation gates and the exact
handoff the wizard emits.

The debconf stub below is stateful on purpose: cdebconf falls back to a
template's Default only while a question has no value, so a wizard that stores
an empty string silently loses the default. Answering <DEFAULT> here accepts
whatever cdebconf would have prefilled, which is the path an operator who
presses Enter takes.

No real device, network interface or disk is read or written: every path the
wizard touches hangs off NEX_WIZARD_ROOT, and ckbcomp, loadkeys, prepare and
validate-config are stubs inside the fixture.
"""
import os
import re
import shutil
import subprocess
import sys
import tempfile
import traceback
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
GUI = REPO / 'installer/gui'
WIZARD = GUI / 'wizard-text.sh'
TEMPLATES = GUI / 'wizard-text.templates'

# A stateful cdebconf stub. It keeps one value per question the way cdebconf
# does, answers scripted questions from a per-template queue, refuses any
# template the real templates file does not define, and records the whole
# exchange. Every variable of its own is _nex_-prefixed: the wizard sources this
# file, so an unprefixed name would clobber the wizard's own state.
CONFMODULE = r'''
# The real confmodule re-executes its caller under cdebconf when no frontend is
# running yet, so anything the wizard does before this point runs without one.
if [ -z "${DEBIAN_HAS_FRONTEND-}" ]; then
    DEBIAN_HAS_FRONTEND=1
    export DEBIAN_HAS_FRONTEND
    exec sh "$0" "$@"
fi

_nex_log=$NEX_TEST_LOG
_nex_answers=$NEX_TEST_ANSWERS
_nex_index=$NEX_TEST_INDEX
_nex_handoff=$NEX_TEST_HANDOFF
_nex_current=
RET=

_nex_say() { printf '%s\n' "$*" >> "$_nex_log"; }
_nex_mangle() { printf '%s' "$1" | tr -c 'A-Za-z0-9' '_'; }
# Index columns: 1 template, 2 type, 3 Default, 4 Choices.
_nex_field() { awk -F '\t' -v t="$1" -v f="$2" '$1 == t { print $f; exit }' "$_nex_index"; }
_nex_known() {
    if awk -F '\t' -v t="$1" 'BEGIN { s = 1 } $1 == t { s = 0; exit } END { exit s }' "$_nex_index"; then
        return 0
    fi
    _nex_say "UNKNOWN $1"
    exit 98
}
_nex_store() {
    _nex_k=_nex_v_$(_nex_mangle "$1")
    eval "$_nex_k=\$2"
    eval "${_nex_k}_set=1"
}
# What cdebconf would show: the stored value, else the Default, else the first
# choice of a select that has no Default.
_nex_prefill() {
    _nex_k=_nex_v_$(_nex_mangle "$1")
    if eval "[ -n \"\${${_nex_k}_set-}\" ]"; then eval "RET=\$$_nex_k"; return 0; fi
    RET=$(_nex_field "$1" 3)
    [ -n "$RET" ] && return 0
    if [ "$(_nex_field "$1" 2)" = select ]; then
        _nex_choices=$(_nex_field "$1" 4)
        if [ "$_nex_choices" = '${choices}' ]; then
            eval "_nex_choices=\${_nex_s_$(_nex_mangle "$1.choices")-}"
        fi
        RET=${_nex_choices%%,*}
    fi
    return 0
}

db_x_loadtemplatefile() { _nex_say "LOAD $1 $2"; }
db_capb() { _nex_say "CAPB $*"; }
db_settitle() { _nex_known "$1"; _nex_say "TITLE $1"; }
db_progress() { _nex_say "PROGRESS $*"; }
db_fset() { _nex_known "$1"; _nex_say "FSET $1 $2 $3"; }
db_set() { _nex_known "$1"; _nex_store "$1" "${2-}"; _nex_say "SET $1 ${2-}"; }
db_subst() {
    _nex_known "$1"
    eval "_nex_s_$(_nex_mangle "$1.$2")=\$3"
    _nex_say "SUBST $1 $2 $3"
}
db_get() { _nex_known "$1"; _nex_prefill "$1"; _nex_say "GET $1 $RET"; }
db_input() {
    _nex_known "$2"
    _nex_current=$2
    if [ -e "$_nex_handoff" ]; then _nex_say "HANDOFF present"; else _nex_say "HANDOFF absent"; fi
    _nex_say "INPUT $2"
}
db_go() {
    case $(_nex_field "$_nex_current" 2) in
        error|note|text) RET=; _nex_say "ANSWER $_nex_current <note>"; return 0 ;;
    esac
    _nex_queue=_nex_q_$(_nex_mangle "$_nex_current")
    eval "_nex_count=\${$_nex_queue:-0}"
    _nex_count=$((_nex_count + 1))
    eval "$_nex_queue=$_nex_count"
    _nex_answer=$(awk -v t="$_nex_current" -v n="$_nex_count" \
        '$1 == t { c++; if (c == n) { i = index($0, " "); print (i ? substr($0, i + 1) : ""); f = 1; exit } }
         END { if (!f) print "<EXHAUSTED>" }' "$_nex_answers")
    case $_nex_answer in
        '<EXHAUSTED>') _nex_say "EXHAUSTED $_nex_current"; exit 97 ;;
        '<BACK>') _nex_say "BACK $_nex_current"; return 30 ;;
        '<DEFAULT>') _nex_prefill "$_nex_current" ;;
        *) RET=$_nex_answer ;;
    esac
    _nex_store "$_nex_current" "$RET"
    _nex_say "ANSWER $_nex_current $RET"
    return 0
}
'''

# The initrd's console keymaps are incomplete: ckbcomp exits 0 for a layout it
# cannot expand and prints only its two header lines. This stub reproduces that
# for any layout listed in the fixture's ckbcomp-empty file.
CKBCOMP = r'''#!/bin/sh
printf 'CKBCOMP %s\n' "$*" >> "$NEX_TEST_LOG"
printf '%s\n' "$1" > "$NEX_TEST_STATE/keymap-last"
echo 'keymaps 0-4,6,8,10,12,14'
echo 'strings as usual'
if grep -qx -e "$*" -e "$1" "$NEX_TEST_STATE/ckbcomp-empty" 2>/dev/null; then exit 0; fi
echo 'keycode   1 = Escape'
echo 'keycode   2 = one exclam'
exit 0
'''

LOADKEYS = r'''#!/bin/sh
cat > /dev/null
printf 'LOADKEYS %s\n' "$*" >> "$NEX_TEST_LOG"
if [ -s "$NEX_TEST_STATE/loadkeys-fail" ] &&
   grep -qx -- "$(cat "$NEX_TEST_STATE/keymap-last" 2>/dev/null)" "$NEX_TEST_STATE/loadkeys-fail"; then
    echo 'loadkeys: cannot load the supplied keymap' >&2
    exit 1
fi
exit 0
'''

PREPARE = r'''#!/bin/sh
if [ -n "${DEBIAN_HAS_FRONTEND-}" ]; then _f=yes; else _f=no; fi
printf 'PREPARE frontend=%s\n' "$_f" >> "$NEX_TEST_LOG"
if [ -e "$NEX_TEST_STATE/prepare-fails" ]; then exit 1; fi
: > "$NEX_TEST_PREPARED"
exit 0
'''

# nex-config with a single CONFIG argument validates and prints its own message.
VALIDATE_CONFIG = r'''#!/bin/sh
_n=$(cat "$NEX_TEST_STATE/validate-calls" 2>/dev/null || echo 0)
_n=$((_n + 1))
printf '%s\n' "$_n" > "$NEX_TEST_STATE/validate-calls"
if [ -e "$1" ]; then _seen=present; else _seen=absent; fi
if [ "$_n" -le "$(cat "$NEX_TEST_STATE/validate-rejects" 2>/dev/null || echo 0)" ]; then
    printf 'VALIDATE %s reject %s %s\n' "$_n" "$_seen" "$1" >> "$NEX_TEST_LOG"
    printf '%s\n' "$NEX_TEST_REJECT_MESSAGE" >&2
    exit 1
fi
printf 'VALIDATE %s accept %s %s\n' "$_n" "$_seen" "$1" >> "$NEX_TEST_LOG"
exit 0
'''

# nex-config reads the handoff with GKeyFile, which unescapes \\ and \s, and
# then validates it with nex_network_validate. A golden string alone would only
# prove the wizard still emits what it emitted before, so decode and validate
# the file exactly the way the consumer does.
READER = r"""
#include <glib.h>
#include "nex-network.h"
int main(int argc, char **argv)
{
    GKeyFile *keys = g_key_file_new();
    GError *error = NULL;
    char *s, **groups;
    (void)argc;
    if (!g_key_file_load_from_file(keys, argv[1], G_KEY_FILE_NONE, &error)) {
        g_printerr("%s\n", error->message);
        return 1;
    }
    s = g_key_file_get_string(keys, "Host", "password", NULL);
    g_print("password=%s\n", s ? s : "");
    s = g_key_file_get_string(keys, "Network", "dns", NULL);
    g_print("dns=%s\n", s ? s : "");
    s = g_key_file_get_string(keys, "Network", "management", NULL);
    g_print("management=%s\n", s ? s : "");
    s = g_key_file_get_string(keys, "Host", "disk_size", NULL);
    g_print("disk_size=%s\n", s ? s : "");
    groups = g_key_file_get_groups(keys, NULL);
    for (int i = 0; groups[i]; i++)
        if (g_str_has_prefix(groups[i], "NIC:"))
            g_print("vlan:%s=%d\n", groups[i] + 4, g_key_file_get_integer(keys, groups[i], "vlan", NULL));
    g_print("validate=%s\n", nex_network_validate(keys, &error) ? "VALID" : error->message);
    return 0;
}
"""

NICS = {
    'eth0': {'address': '52:54:00:aa:bb:01', 'carrier': '1'},
    'eth1': {'address': '52:54:00:aa:bb:02', 'carrier': '0'},
}

# One offered disk. Everything else is rejected for a specific reason, and sdf
# is rejected by the partition guard alone: it is large, writable, fixed and is
# not the installation media.
BLOCKS = {
    'sda': {'size': '67108864', 'ro': '0', 'removable': '0', 'dev': '8:0',
            'queue/rotational': '0',
            'device/serial': 'TESTSERIAL1', 'device/model': 'QEMU HARDDISK'},
    'sdb': {'size': '2048', 'ro': '0', 'removable': '0', 'dev': '8:16'},          # under 16 GiB
    'sdc': {'size': '67108864', 'ro': '0', 'removable': '1', 'dev': '8:32'},      # removable
    'sde': {'size': '67108864', 'ro': '1', 'removable': '0', 'dev': '8:64'},      # read-only
    'sdd': {'size': '67108864', 'ro': '0', 'removable': '0', 'dev': '8:48'},      # installation media
    'sdf': {'size': '67108864', 'ro': '0', 'removable': '0', 'dev': '8:80',
            'partition': '1', 'device/model': 'QEMU HARDDISK'},                   # partition
    # SATA/SAS/SCSI disks have no device/serial: only the binary unit-serial VPD page.
    'sdg': {'size': '67108864', 'ro': '0', 'removable': '0', 'dev': '8:96',
            'queue/rotational': '1', 'device/model': 'QEMU HARDDISK',
            'device/vpd_pg80': b'\x00\x80\x00\x14TESTSERIAL2         \x00'},        # mirror partner
    'sdh': {'size': '33554432', 'ro': '0', 'removable': '0', 'dev': '8:112',
            'queue/rotational': '0',
            'device/serial': 'TESTSERIAL3', 'device/model': 'QEMU HARDDISK'},     # half the size
    'sdi': {'size': '63753421', 'ro': '0', 'removable': '0', 'dev': '8:128',
            'queue/rotational': '0',
            'serial': 'TESTSERIAL4', 'device/model': 'QEMU HARDDISK'},            # ~5% smaller; virtio-style serial
}

DISK_ONE = 'sda - 32 GiB - SSD - QEMU HARDDISK - TESTSERIAL1 - blank'
DISK_PARTNER = 'sdg - 32 GiB - HDD - QEMU HARDDISK - TESTSERIAL2 - has partitions'
DISK_SMALLER = 'sdh - 16 GiB - SSD - QEMU HARDDISK - TESTSERIAL3 - blank'
DISK_NEAR = 'sdi - 30 GiB - SSD - QEMU HARDDISK - TESTSERIAL4 - blank'
DISK_CHOICES = ', '.join([DISK_ONE, DISK_PARTNER, DISK_SMALLER, DISK_NEAR])
DISK_MIRROR = ', '.join([DISK_ONE, DISK_PARTNER])
DISK_MIRROR_NEAR = ', '.join([DISK_ONE, DISK_NEAR])
MANAGEMENT_CHOICES = ('eth0 - 52:54:00:aa:bb:01 - link detected, '
                      'eth1 - 52:54:00:aa:bb:02 - no link')
OFFLINE = 'Offline (no management network)'
PASSWORD = r'pass word\one'
REJECTION = 'Disk serial changed; select the disk again.'


def write(path, text):
    path.parent.mkdir(parents=True, exist_ok=True)
    if isinstance(text, bytes):
        path.write_bytes(text)
    else:
        path.write_text(text)


def executable(path, text):
    write(path, text)
    path.chmod(0o755)


def parse_templates(text):
    """The single-line fields of every stanza in a debconf templates file."""
    templates, current = {}, None
    for line in text.splitlines():
        if not line.strip():
            current = None
        elif line[0].isspace():
            continue
        else:
            name, _, value = line.partition(':')
            value = value.strip()
            if name == 'Template':
                current = {'type': '', 'default': '', 'choices': ''}
                templates[value] = current
            elif current is not None and name in ('Type', 'Default', 'Choices'):
                current[name.lower()] = value
    return templates


def build_fixture(root, nics=NICS, blocks=BLOCKS, prepared=True):
    for nic, attributes in nics.items():
        base = root / 'sys/class/net' / nic
        (base / 'device').mkdir(parents=True)
        for name, value in attributes.items():
            write(base / name, value + '\n')
    # Kernel-only interfaces have no device directory and are never offered.
    (root / 'sys/class/net/lo').mkdir(parents=True)
    write(root / 'sys/class/net/lo/address', '00:00:00:00:00:00\n')

    for name, attributes in blocks.items():
        base = root / 'sys/class/block' / name
        for attribute, value in attributes.items():
            write(base / attribute, value if isinstance(value, bytes) else value + '\n')
    # A leftover partition on the mirror partner: the wizard reports it as "has partitions"
    # so the operator sees which disk still holds data.
    write(root / 'sys/class/block/sdg/sdg1/partition', '1\n')
    # The mounted media partition and the whole disk behind it are both excluded.
    partition = root / 'sys/block/sdd/sdd1'
    write(partition / 'partition', '1\n')
    write(partition / 'size', '67108864\n')
    (root / 'sys/class/block/sdd1').symlink_to(partition)
    write(root / 'proc/mounts', '/dev/sdd1 /cdrom iso9660 ro 0 0\n')

    write(root / 'usr/share/nex-installer/timezones',
          'UTC\nAustralia/Sydney\nEurope/Paris\n')
    # The wizard loads the real templates, and the stub answers from them.
    templates = TEMPLATES.read_text()
    write(root / 'usr/share/nex-installer/wizard-text.templates', templates)
    write(root / 'index', ''.join(
        '{}\t{type}\t{default}\t{choices}\n'.format(name, **fields)
        for name, fields in parse_templates(templates).items()))
    write(root / 'usr/share/debconf/confmodule', CONFMODULE)

    (root / 'run/nex-installer').mkdir(parents=True)
    if prepared:
        write(root / 'run/nex-installer/prepared', '')
    (root / 'test-state').mkdir()
    executable(root / 'test-bin/ckbcomp', CKBCOMP)
    executable(root / 'test-bin/loadkeys', LOADKEYS)
    executable(root / 'usr/lib/nex-installer/prepare', PREPARE)
    executable(root / 'usr/lib/nex-installer/validate-config', VALIDATE_CONFIG)
    (root / 'log').touch()


class Session:
    """One wizard run and the exchange it produced."""

    def __init__(self, root, result):
        self.root = root
        self.result = result
        self.entries = (root / 'log').read_text().splitlines()

    @property
    def config_path(self):
        return self.root / 'run/nex-installer/config.ini'

    @property
    def config(self):
        path = self.config_path
        return path.read_text() if path.exists() else None

    def lines(self, kind):
        return [line[len(kind) + 1:] for line in self.entries if line.startswith(kind + ' ')]

    def asked(self):
        return self.lines('INPUT')

    def substitutions(self, template, key):
        prefix = 'SUBST {} {} '.format(template, key)
        return [line[len(prefix):] for line in self.entries if line.startswith(prefix)]

    def subst(self, template, key):
        values = self.substitutions(template, key)
        assert values, ('no substitution for {} {}'.format(template, key), self.explain())
        return values[-1]

    def explain(self):
        return ('exit={}'.format(self.result.returncode),
                'stderr=' + self.result.stderr[-400:],
                'log=' + '; '.join(self.entries[-14:]))


def run(root, shell, timeout=120):
    binaries = root / 'test-bin'
    command = [shell, str(WIZARD)]
    if shell == 'busybox':
        link = binaries / 'sh'
        if not link.exists():
            link.symlink_to(BUSYBOX)
        command = [str(link), str(WIZARD)]
    environment = dict(
        os.environ,
        PATH=str(binaries) + os.pathsep + os.environ.get('PATH', ''),
        NEX_WIZARD_ROOT=str(root),
        NEX_TEST_ANSWERS=str(root / 'answers'),
        NEX_TEST_INDEX=str(root / 'index'),
        NEX_TEST_LOG=str(root / 'log'),
        NEX_TEST_STATE=str(root / 'test-state'),
        NEX_TEST_HANDOFF=str(root / 'run/nex-installer/config.ini'),
        NEX_TEST_PREPARED=str(root / 'run/nex-installer/prepared'),
        NEX_TEST_REJECT_MESSAGE=REJECTION,
    )
    result = subprocess.run(command, env=environment, capture_output=True,
                            text=True, timeout=timeout)
    return Session(root, result)


def script(root, lines):
    write(root / 'answers', '\n'.join(lines) + '\n')


_READER = {}


def reader_binary():
    """Compile the GKeyFile/nex_network_validate reader once, or None."""
    if 'path' in _READER:
        return _READER['path']
    _READER['path'] = None
    compiler = shutil.which('cc') or shutil.which('gcc')
    flags = None
    if compiler and shutil.which('pkg-config'):
        flags = subprocess.run(['pkg-config', '--cflags', '--libs', 'glib-2.0'],
                               capture_output=True, text=True)
    if flags is not None and flags.returncode == 0:
        directory = Path(tempfile.mkdtemp(prefix='nex-text-wizard-'))
        _READER['directory'] = directory
        source = directory / 'reader.c'
        source.write_text(READER)
        binary = directory / 'reader'
        build = subprocess.run(
            [compiler, str(source), str(GUI / 'nex-network.c'), '-I', str(GUI),
             '-o', str(binary)] + flags.stdout.split(), capture_output=True, text=True)
        assert build.returncode == 0, build.stderr
        _READER['path'] = binary
    return _READER['path']


def decode(config):
    """Read the handoff exactly as nex-config does, or None when glib is absent."""
    binary = reader_binary()
    if binary is None:
        return None
    read = subprocess.run([str(binary), str(config)], capture_output=True, text=True)
    assert read.returncode == 0, read.stderr
    return dict(line.split('=', 1) for line in read.stdout.splitlines())


# ------------------------------------------------------------------- scenarios

DEFAULT_CONFIG = r'''[Host]
hostname=nexcore
timezone=UTC
keymap=us
password=nexcore-test-pw
disk=/dev/sda;
disk_size=34359738368;
disk_serial=TESTSERIAL1;
disk_major_minor=8:0;

[Network]
management=eth0
dns=

[NIC:eth0]
mac=52:54:00:aa:bb:01
mode=dhcp
address=
gateway=
vlan=0

[NIC:eth1]
mac=52:54:00:aa:bb:02
mode=disabled
address=
gateway=
vlan=0
'''

DEFAULT_SCRIPT = [
    'nex-installer/hostname <DEFAULT>',
    'nex-installer/keymap <DEFAULT>',
    'nex-installer/timezone-area <DEFAULT>',
    'nex-installer/password nexcore-test-pw',
    'nex-installer/password-again nexcore-test-pw',
    'nex-installer/management <DEFAULT>',
    'nex-installer/other-adapters <DEFAULT>',  # no: leave eth1 disabled
    'nex-installer/mode <DEFAULT>',   # eth0, the management adapter: dhcp
    'nex-installer/vlan <DEFAULT>',
    'nex-installer/dns <DEFAULT>',
    'nex-installer/disk ' + DISK_ONE,
    'nex-installer/erase true',
]

DEFAULT_ORDER = [
    'nex-installer/hostname',
    'nex-installer/keymap',
    'nex-installer/timezone-area',
    'nex-installer/password',
    'nex-installer/password-again',
    'nex-installer/management',
    'nex-installer/other-adapters',
    'nex-installer/mode',
    'nex-installer/vlan',
    'nex-installer/dns',
    'nex-installer/disk',
    'nex-installer/erase',
]


def scenario_default_answers(shell, root):
    """Pressing Enter at every question must install: the VLAN becomes 0, not empty."""
    build_fixture(root)
    script(root, DEFAULT_SCRIPT)
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.asked() == DEFAULT_ORDER, (session.asked(), session.explain())
    # sdf is large, writable, fixed and is not the media: only its partition
    # attribute keeps it out of this list.
    assert session.subst('nex-installer/disk', 'choices') == DISK_CHOICES
    # The question itself must offer 0: an empty value would show a blank prompt.
    assert 'nex-installer/vlan 0' in session.lines('SET'), session.lines('SET')
    assert session.config == DEFAULT_CONFIG, (session.config, session.explain())
    decoded = decode(session.config_path)
    if decoded is None:
        return 'no C compiler with glib-2.0; the handoff was not validated'
    assert decoded['vlan:eth0'] == '0', decoded
    assert decoded['validate'] == 'VALID', decoded
    return None


NAVIGATION_SCRIPT = [
    'nex-installer/hostname wrong.example.test',
    'nex-installer/keymap <BACK>',
    'nex-installer/hostname nexcore.example.test',
    'nex-installer/keymap gb',
    'nex-installer/timezone-area UTC',
    # UTC has no zone question, and Go Back from here must not bounce off it.
    'nex-installer/password <BACK>',
    'nex-installer/timezone-area Australia',
    'nex-installer/timezone Australia/Sydney',
    'nex-installer/password ' + PASSWORD,
    'nex-installer/password-again ' + PASSWORD,
    'nex-installer/management eth0 - 52:54:00:aa:bb:01 - link detected',
    'nex-installer/other-adapters false',
    'nex-installer/mode static',
    'nex-installer/vlan 10',
    'nex-installer/address 192.0.2.10/24',
    'nex-installer/gateway 192.0.2.1',
    'nex-installer/dns 192.0.2.53 192.0.2.54',
    'nex-installer/disk ' + DISK_ONE,
    'nex-installer/erase false',
    'nex-installer/disk ' + DISK_ONE,
    'nex-installer/erase true',
]

NAVIGATION_ORDER = [
    'nex-installer/hostname',
    'nex-installer/keymap',
    'nex-installer/hostname',
    'nex-installer/keymap',
    'nex-installer/timezone-area',
    'nex-installer/password',
    'nex-installer/timezone-area',
    'nex-installer/timezone',
    'nex-installer/password',
    'nex-installer/password-again',
    'nex-installer/management',
    'nex-installer/other-adapters',
    'nex-installer/mode',
    'nex-installer/vlan',
    'nex-installer/address',
    'nex-installer/gateway',
    'nex-installer/dns',
    'nex-installer/disk',
    'nex-installer/erase',
    'nex-installer/disk',
    'nex-installer/erase',
]

NAVIGATION_CONFIG = r'''[Host]
hostname=nexcore.example.test
timezone=Australia/Sydney
keymap=gb
password=pass\sword\\one
disk=/dev/sda;
disk_size=34359738368;
disk_serial=TESTSERIAL1;
disk_major_minor=8:0;

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


def scenario_navigation_and_handoff(shell, root):
    """Question order, Go Back across a skipped step, a declined erase, the handoff."""
    build_fixture(root)
    script(root, NAVIGATION_SCRIPT)
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.asked() == NAVIGATION_ORDER, (session.asked(), session.explain())
    assert session.subst('nex-installer/disk', 'choices') == DISK_CHOICES
    assert session.subst('nex-installer/management', 'choices').startswith(MANAGEMENT_CHOICES), \
        session.subst('nex-installer/management', 'choices')
    # Answering no to other adapters skips eth1; only the management NIC is asked.
    assert session.substitutions('nex-installer/mode', 'choices') == ['dhcp, static'], \
        session.substitutions('nex-installer/mode', 'choices')
    assert session.subst('nex-installer/other-adapters', 'detail') == \
        'Management adapter: eth0. Remaining (1): eth1. A normal install only needs the management adapter.', \
        session.subst('nex-installer/other-adapters', 'detail')
    assert session.config == NAVIGATION_CONFIG, (session.config, session.explain())
    assert session.config_path.stat().st_mode & 0o077 == 0, oct(session.config_path.stat().st_mode)
    assert not (root / 'run/nex-installer/config.ini.new').exists()
    decoded = decode(session.config_path)
    if decoded is None:
        return 'no C compiler with glib-2.0; the handoff was not decoded'
    assert decoded['password'] == PASSWORD, decoded
    assert decoded['dns'] == '192.0.2.53 192.0.2.54', decoded
    assert decoded['vlan:eth0'] == '10', decoded
    assert decoded['disk_size'] == '34359738368;', decoded
    assert decoded['validate'] == 'VALID', decoded
    return None


STATIC_SCRIPT = [
    'nex-installer/hostname nexcore.example.test',
    'nex-installer/keymap <DEFAULT>',
    'nex-installer/timezone-area <DEFAULT>',
    'nex-installer/password nexcore-test-pw',
    'nex-installer/password-again nexcore-test-pw',
    'nex-installer/management <DEFAULT>',
    'nex-installer/other-adapters <DEFAULT>',
    'nex-installer/mode static',
    'nex-installer/vlan 10',
    'nex-installer/address 192.0.2.10/24',
    'nex-installer/gateway 192.0.2.1',
    'nex-installer/dns <DEFAULT>',
    'nex-installer/disk ' + DISK_ONE,
    'nex-installer/erase true',
]


def scenario_erase_summary(shell, root):
    """The confirmation reviews every setting, including each adapter."""
    build_fixture(root)
    script(root, STATIC_SCRIPT)
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.subst('nex-installer/erase', 'disk') == '/dev/sda'
    assert session.subst('nex-installer/erase', 'disksize') == '32 GiB (34359738368 bytes)'
    assert session.subst('nex-installer/erase', 'serial') == 'TESTSERIAL1'
    assert session.subst('nex-installer/erase', 'layout') == 'one ext4 filesystem, no redundancy'
    assert session.subst('nex-installer/erase', 'hostname') == 'nexcore.example.test'
    assert session.subst('nex-installer/erase', 'timezone') == 'UTC'
    assert session.subst('nex-installer/erase', 'keymap') == 'us'
    assert session.subst('nex-installer/erase', 'management') == 'eth0 (static)'
    assert session.subst('nex-installer/erase', 'dns') == 'from DHCP or none'
    assert session.subst('nex-installer/erase', 'adapters') == \
        'eth0: static 192.0.2.10/24, gateway 192.0.2.1, VLAN 10; eth1: disabled', \
        session.subst('nex-installer/erase', 'adapters')
    return None


def scenario_mirror_selection(shell, root):
    """Two disks become one handoff listing both, in selection order."""
    build_fixture(root)
    script(root, DEFAULT_SCRIPT[:-2] + ['nex-installer/disk ' + DISK_MIRROR,
                                        'nex-installer/erase true'])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.subst('nex-installer/erase', 'disk') == '/dev/sda, /dev/sdg', session.explain()
    assert session.subst('nex-installer/erase', 'serial') == 'TESTSERIAL1, TESTSERIAL2'
    assert session.subst('nex-installer/erase', 'layout') == \
        'one system disk as 2 identical RAID1 copies (capacity not pooled); survives 1 disk failure'
    for line in ('disk=/dev/sda;/dev/sdg;', 'disk_size=34359738368;34359738368;',
                 'disk_serial=TESTSERIAL1;TESTSERIAL2;', 'disk_major_minor=8:0;8:96;'):
        assert line + '\n' in session.config, (line, session.config, session.explain())
    return None


def scenario_mirror_capacity_mismatch(shell, root):
    """A disk far smaller than the others is refused, and the question is asked again."""
    build_fixture(root)
    script(root, DEFAULT_SCRIPT[:-2] + [
        'nex-installer/disk ' + ', '.join([DISK_ONE, DISK_SMALLER]),
        'nex-installer/disk-error <note>',
        'nex-installer/disk ' + DISK_MIRROR,
        'nex-installer/erase true'])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert "within 10% of the smallest" in session.subst('nex-installer/disk-error', 'detail'), \
        session.explain()
    assert 'disk=/dev/sda;/dev/sdg;\n' in session.config, (session.config, session.explain())
    return None


def scenario_mirror_capacity_near(shell, root):
    """A partner about 5% smaller is accepted; the 10% rule is not equality-only."""
    build_fixture(root)
    script(root, DEFAULT_SCRIPT[:-2] + [
        'nex-installer/disk ' + DISK_MIRROR_NEAR,
        'nex-installer/erase true'])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert 'disk=/dev/sda;/dev/sdi;\n' in session.config, (session.config, session.explain())
    assert 'disk_serial=TESTSERIAL1;TESTSERIAL4;\n' in session.config, session.config
    return None


def scenario_empty_disk_selection_retried(shell, root):
    """Unticking every disk is refused in place; a later selection still hands off."""
    build_fixture(root)
    script(root, DEFAULT_SCRIPT[:-2] + [
        'nex-installer/disk',
        'nex-installer/disk-error <note>',
        'nex-installer/disk ' + DISK_ONE,
        'nex-installer/erase true'])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert 'Select at least one disk' in session.subst('nex-installer/disk-error', 'detail'), \
        session.explain()
    assert 'disk=/dev/sda;\n' in session.config, (session.config, session.explain())
    return None


def scenario_disk_serial_list_escape(shell, root):
    """A semicolon in a disk serial must not split the GKeyFile string list."""
    blocks = dict(BLOCKS)
    blocks['sda'] = dict(BLOCKS['sda'], **{'device/serial': 'AB;CD'})
    build_fixture(root, blocks=blocks)
    choice = 'sda - 32 GiB - SSD - QEMU HARDDISK - AB;CD - blank'
    script(root, DEFAULT_SCRIPT[:-2] + [
        'nex-installer/disk ' + choice,
        'nex-installer/erase true'])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.subst('nex-installer/disk', 'choices').startswith(choice), \
        session.subst('nex-installer/disk', 'choices')
    assert 'disk_serial=AB\\;CD;\n' in session.config, (session.config, session.explain())
    decoded = decode(session.config_path)
    if decoded is None:
        return 'no C compiler with glib-2.0; the handoff was not decoded'
    assert decoded['validate'] == 'VALID', decoded
    return None


def scenario_sysfs_values_trimmed(shell, root):
    """NVMe serials and SCSI models are space padded; the handoff must not be."""
    nics = {'eth0': {'address': '52:54:00:aa:bb:01  ', 'carrier': '1'}}
    blocks = dict(BLOCKS)
    blocks['sda'] = dict(BLOCKS['sda'], **{'device/serial': 'TESTSERIAL1   ',
                                           'device/model': '  QEMU HARDDISK  '})
    build_fixture(root, nics=nics, blocks=blocks)
    script(root, [
        'nex-installer/hostname <DEFAULT>',
        'nex-installer/keymap <DEFAULT>',
        'nex-installer/timezone-area <DEFAULT>',
        'nex-installer/password nexcore-test-pw',
        'nex-installer/password-again nexcore-test-pw',
        'nex-installer/management <DEFAULT>',
        'nex-installer/mode <DEFAULT>',
        'nex-installer/vlan <DEFAULT>',
        'nex-installer/dns <DEFAULT>',
        'nex-installer/disk ' + DISK_ONE,
        'nex-installer/erase true',
    ])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.subst('nex-installer/disk', 'choices') == DISK_CHOICES, \
        session.subst('nex-installer/disk', 'choices')
    assert session.subst('nex-installer/management', 'choices').startswith(
        'eth0 - 52:54:00:aa:bb:01 - link detected'), \
        session.subst('nex-installer/management', 'choices')
    assert session.subst('nex-installer/erase', 'serial') == 'TESTSERIAL1'
    written = session.config or ''
    assert 'disk_serial=TESTSERIAL1;\n' in written, (written, session.explain())
    assert 'mac=52:54:00:aa:bb:01\n' in written, (written, session.explain())
    return None


def keymap_script(first, second=None):
    lines = ['nex-installer/keymap ' + first]
    if second:
        lines.append('nex-installer/keymap ' + second)
    return ['nex-installer/hostname <DEFAULT>'] + lines + DEFAULT_SCRIPT[2:]


def scenario_keymap_arguments(shell, root):
    """fr has no bare console map: it must be requested as 'fr latin9'."""
    build_fixture(root)
    script(root, keymap_script('fr'))
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.lines('CKBCOMP') == ['fr latin9'], (session.lines('CKBCOMP'), session.explain())
    # ckbcomp emits UTF-8 keysyms, which loadkeys only accepts in Unicode mode:
    # without -u the gb, de and es maps are refused on a non-UTF-8 console and
    # the gate below would never let the operator past the keyboard question.
    assert session.lines('LOADKEYS') == ['-u -'], (session.lines('LOADKEYS'), session.explain())
    # The installed host still records the layout by its bare name.
    assert 'keymap=fr\n' in session.config, (session.config, session.explain())
    return None


def assert_keymap_retried(session, installed):
    assert session.result.returncode == 0, session.explain()
    asked = session.asked()
    assert 'nex-installer/keymap-error' in asked, (asked, session.explain())
    error = asked.index('nex-installer/keymap-error')
    assert asked[error + 1] == 'nex-installer/keymap', (asked, session.explain())
    # Credentials are never typed on a layout the console did not accept.
    password = asked.index('nex-installer/password')
    assert password > error, (asked, session.explain())
    assert 'keymap={}\n'.format(installed) in session.config, session.config


def scenario_keymap_empty_map_retried(shell, root):
    """A layout ckbcomp cannot expand is reported and asked again."""
    build_fixture(root)
    write(root / 'test-state/ckbcomp-empty', 'fr latin9\n')
    script(root, keymap_script('fr', 'us'))
    assert_keymap_retried(run(root, shell), 'us')
    return None


def scenario_keymap_load_failure_retried(shell, root):
    """A layout loadkeys rejects is reported and asked again."""
    build_fixture(root)
    write(root / 'test-state/loadkeys-fail', 'de\n')
    script(root, keymap_script('de', 'gb'))
    assert_keymap_retried(run(root, shell), 'gb')
    return None


RETRY_CONFIG = r'''[Host]
hostname=nexcore.example.test
timezone=Australia/Sydney
keymap=de
password=retain-this-pw
disk=/dev/sda;
disk_size=34359738368;
disk_serial=TESTSERIAL1;
disk_major_minor=8:0;

[Network]
management=eth0
dns=192.0.2.53

[NIC:eth0]
mac=52:54:00:aa:bb:01
mode=static
address=192.0.2.10/24
gateway=192.0.2.1
vlan=20

[NIC:eth1]
mac=52:54:00:aa:bb:02
mode=disabled
address=
gateway=
vlan=0
'''


def scenario_rejected_settings_keep_answers(shell, root):
    """A rejected handoff shows the validator's message and keeps every answer."""
    build_fixture(root)
    write(root / 'test-state/validate-rejects', '1\n')
    script(root, [
        'nex-installer/hostname nexcore.example.test',
        'nex-installer/hostname <DEFAULT>',
        'nex-installer/keymap de',
        'nex-installer/keymap <DEFAULT>',
        'nex-installer/timezone-area Australia',
        'nex-installer/timezone-area <DEFAULT>',
        'nex-installer/timezone Australia/Sydney',
        'nex-installer/timezone <DEFAULT>',
        'nex-installer/password retain-this-pw',
        'nex-installer/password <DEFAULT>',
        'nex-installer/password-again retain-this-pw',
        'nex-installer/password-again <DEFAULT>',
        'nex-installer/management <DEFAULT>',
        'nex-installer/management <DEFAULT>',
        'nex-installer/other-adapters <DEFAULT>',
        'nex-installer/other-adapters <DEFAULT>',
        'nex-installer/mode static',
        'nex-installer/vlan 20',
        'nex-installer/address 192.0.2.10/24',
        'nex-installer/gateway 192.0.2.1',
        'nex-installer/mode <DEFAULT>',
        'nex-installer/vlan <DEFAULT>',
        'nex-installer/address <DEFAULT>',
        'nex-installer/gateway <DEFAULT>',
        'nex-installer/dns 192.0.2.53',
        'nex-installer/dns <DEFAULT>',
        'nex-installer/disk ' + DISK_ONE,
        'nex-installer/erase true',
        # The rejection returns here with everything still filled in.
        'nex-installer/erase <BACK>',
        'nex-installer/disk ' + DISK_ONE,
        'nex-installer/erase true',
    ])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    calls = session.lines('VALIDATE')
    assert len(calls) == 2, (calls, session.explain())
    assert calls[0].startswith('1 reject present'), (calls, session.explain())
    assert calls[1].startswith('2 accept present'), (calls, session.explain())
    # The operator reads nex-config's own message, not a fixed string.
    assert session.subst('nex-installer/failed', 'detail') == REJECTION, \
        session.subst('nex-installer/failed', 'detail')
    asked = session.asked()
    assert asked.count('nex-installer/erase') >= 2, (asked, session.explain())
    # The rejected handoff is removed before the operator is asked again.
    handoffs = [line for line in session.entries
                if line.startswith('HANDOFF ') or line == 'INPUT nex-installer/failed']
    after = handoffs[handoffs.index('INPUT nex-installer/failed') + 1:]
    assert after and after[0] == 'HANDOFF absent', (after[:3], session.explain())
    assert session.config == RETRY_CONFIG, (session.config, session.explain())
    return None


OFFLINE_CONFIG = r'''[Host]
hostname=nexcore
timezone=UTC
keymap=us
password=nexcore-test-pw
disk=/dev/sda;
disk_size=34359738368;
disk_serial=TESTSERIAL1;
disk_major_minor=8:0;

[Network]
management=
dns=

[NIC:eth0]
mac=52:54:00:aa:bb:01
mode=static
address=192.0.2.10/24
gateway=
vlan=0

[NIC:eth1]
mac=52:54:00:aa:bb:02
mode=disabled
address=
gateway=
vlan=0
'''


def scenario_offline_management(shell, root):
    """An offline install keeps the adapters but has no management network."""
    build_fixture(root)
    script(root, [
        'nex-installer/hostname <DEFAULT>',
        'nex-installer/keymap <DEFAULT>',
        'nex-installer/timezone-area <DEFAULT>',
        'nex-installer/password nexcore-test-pw',
        'nex-installer/password-again nexcore-test-pw',
        'nex-installer/management ' + OFFLINE,
        'nex-installer/other-adapters true',
        'nex-installer/mode static',
        'nex-installer/vlan <DEFAULT>',
        'nex-installer/address 192.0.2.10/24',
        'nex-installer/mode <DEFAULT>',   # eth1: Disabled → disabled
        'nex-installer/dns <DEFAULT>',
        'nex-installer/disk ' + DISK_ONE,
        'nex-installer/erase true',
    ])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    # The adapters stay first so the highlighted default remains a real adapter.
    assert session.subst('nex-installer/management', 'choices') == \
        MANAGEMENT_CHOICES + ', ' + OFFLINE, \
        session.subst('nex-installer/management', 'choices')
    assert session.substitutions('nex-installer/mode', 'choices') == \
        ['Disabled, dhcp, static', 'Disabled, dhcp, static'], \
        session.substitutions('nex-installer/mode', 'choices')
    assert session.subst('nex-installer/other-adapters', 'detail') == \
        'Offline install. Adapters (2): eth0, eth1. Answer no to leave every adapter unconfigured.', \
        session.subst('nex-installer/other-adapters', 'detail')
    # Only a management adapter may carry a default route.
    assert 'nex-installer/gateway' not in session.asked(), (session.asked(), session.explain())
    assert session.subst('nex-installer/erase', 'management') == 'Offline'
    assert session.config == OFFLINE_CONFIG, (session.config, session.explain())
    decoded = decode(session.config_path)
    if decoded is None:
        return 'no C compiler with glib-2.0; the handoff was not validated'
    assert decoded['management'] == '', decoded
    assert decoded['validate'] == 'VALID', decoded
    return None


def scenario_configure_other_adapter(shell, root):
    """Answering yes walks every leftover adapter; Disabled still stores disabled."""
    build_fixture(root)
    script(root, [
        'nex-installer/hostname <DEFAULT>',
        'nex-installer/keymap <DEFAULT>',
        'nex-installer/timezone-area <DEFAULT>',
        'nex-installer/password nexcore-test-pw',
        'nex-installer/password-again nexcore-test-pw',
        'nex-installer/management <DEFAULT>',
        'nex-installer/other-adapters true',
        'nex-installer/mode <DEFAULT>',   # eth0 management: dhcp
        'nex-installer/vlan <DEFAULT>',
        'nex-installer/mode dhcp',        # eth1 additional
        'nex-installer/vlan <DEFAULT>',
        'nex-installer/dns <DEFAULT>',
        'nex-installer/disk ' + DISK_ONE,
        'nex-installer/erase true',
    ])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.asked() == [
        'nex-installer/hostname',
        'nex-installer/keymap',
        'nex-installer/timezone-area',
        'nex-installer/password',
        'nex-installer/password-again',
        'nex-installer/management',
        'nex-installer/other-adapters',
        'nex-installer/mode',
        'nex-installer/vlan',
        'nex-installer/mode',
        'nex-installer/vlan',
        'nex-installer/dns',
        'nex-installer/disk',
        'nex-installer/erase',
    ], (session.asked(), session.explain())
    assert session.substitutions('nex-installer/mode', 'choices') == \
        ['dhcp, static', 'Disabled, dhcp, static'], \
        session.substitutions('nex-installer/mode', 'choices')
    assert 'mode=dhcp\n' in session.config
    assert session.config.count('mode=dhcp\n') == 2, session.config
    assert 'mode=disabled\n' not in session.config, session.config
    return None


def scenario_management_not_first_nic(shell, root):
    """Skipping others still configures the chosen management NIC, even when it is not first."""
    build_fixture(root)
    script(root, [
        'nex-installer/hostname <DEFAULT>',
        'nex-installer/keymap <DEFAULT>',
        'nex-installer/timezone-area <DEFAULT>',
        'nex-installer/password nexcore-test-pw',
        'nex-installer/password-again nexcore-test-pw',
        'nex-installer/management eth1 - 52:54:00:aa:bb:02 - link detected',
        'nex-installer/other-adapters false',
        'nex-installer/mode <DEFAULT>',   # eth1 only; eth0 is skipped
        'nex-installer/vlan <DEFAULT>',
        'nex-installer/dns <DEFAULT>',
        'nex-installer/disk ' + DISK_ONE,
        'nex-installer/erase true',
    ])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    assert session.asked().count('nex-installer/mode') == 1, (session.asked(), session.explain())
    assert session.subst('nex-installer/mode', 'nic') == 'eth1', session.explain()
    assert 'management=eth1\n' in session.config, session.config
    assert re.search(r'\[NIC:eth0\]\nmac=.*\nmode=disabled\n', session.config), session.config
    assert re.search(r'\[NIC:eth1\]\nmac=.*\nmode=dhcp\n', session.config), session.config
    return None


NO_NIC_CONFIG = r'''[Host]
hostname=nexcore
timezone=UTC
keymap=us
password=nexcore-test-pw
disk=/dev/sda;
disk_size=34359738368;
disk_serial=TESTSERIAL1;
disk_major_minor=8:0;

[Network]
management=
dns=
'''


def scenario_no_network_adapter(shell, root):
    """A host with no adapter installs offline instead of refusing to start."""
    build_fixture(root, nics={})
    script(root, [
        'nex-installer/hostname <DEFAULT>',
        'nex-installer/keymap <DEFAULT>',
        'nex-installer/timezone-area <DEFAULT>',
        'nex-installer/password nexcore-test-pw',
        'nex-installer/password-again nexcore-test-pw',
        'nex-installer/dns <DEFAULT>',
        'nex-installer/disk ' + DISK_ONE,
        'nex-installer/erase true',
    ])
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    asked = session.asked()
    assert 'nex-installer/no-nic' in asked, (asked, session.explain())
    assert 'nex-installer/management' not in asked, (asked, session.explain())
    assert session.subst('nex-installer/erase', 'management') == 'Offline'
    assert session.config == NO_NIC_CONFIG, (session.config, session.explain())
    decoded = decode(session.config_path)
    if decoded is None:
        return 'no C compiler with glib-2.0; the handoff was not validated'
    assert decoded['validate'] == 'VALID', decoded
    return None


def scenario_prepare_runs_before_frontend(shell, root):
    """Hardware discovery must run before the wizard's frontend exists, once."""
    build_fixture(root, prepared=False)
    script(root, DEFAULT_SCRIPT)
    session = run(root, shell)
    assert session.result.returncode == 0, session.explain()
    prepares = session.lines('PREPARE')
    assert prepares == ['frontend=no'], (prepares, session.explain())
    assert session.entries[0].startswith('PREPARE '), (session.entries[:3], session.explain())
    assert session.config == DEFAULT_CONFIG, (session.config, session.explain())
    return None


def scenario_prepare_failure_stops_setup(shell, root):
    """A failed preparation is reported and writes no handoff."""
    build_fixture(root, prepared=False)
    write(root / 'test-state/prepare-fails', '')
    script(root, DEFAULT_SCRIPT)
    session = run(root, shell)
    assert session.result.returncode != 0, session.explain()
    assert 'nex-installer/failed' in session.asked(), (session.asked(), session.explain())
    assert session.config is None, session.config
    return None


def scenario_abandoned_setup_leaves_no_handoff(shell, root):
    """Going back out of the first question installs nothing."""
    build_fixture(root)
    script(root, ['nex-installer/hostname <BACK>'])
    session = run(root, shell)
    assert session.result.returncode != 0, session.explain()
    assert session.config is None, session.config
    return None


def scenario_templates_exist():
    """Every template the wizard asks for is defined in the templates file."""
    defined = set(parse_templates(TEMPLATES.read_text()))
    # Template names are whole words; /usr/lib/nex-installer/prepare is a path.
    used = set(re.findall(r'(?<![\w/.-])nex-installer/[a-z][a-z-]*(?![\w./-])',
                          WIZARD.read_text()))
    missing = sorted(used - defined)
    assert not missing, missing
    return None


def scenario_pure_ascii():
    """The VT shows these bytes before bterm starts, so both files stay ASCII."""
    for path in (WIZARD, TEMPLATES):
        data = path.read_bytes()
        bad = sorted({byte for byte in data if byte > 127})
        assert not bad, (path.name, bad)
    return None


SHELL_SCENARIOS = [
    scenario_default_answers,
    scenario_navigation_and_handoff,
    scenario_erase_summary,
    scenario_sysfs_values_trimmed,
    scenario_mirror_selection,
    scenario_mirror_capacity_mismatch,
    scenario_mirror_capacity_near,
    scenario_empty_disk_selection_retried,
    scenario_disk_serial_list_escape,
    scenario_keymap_arguments,
    scenario_keymap_empty_map_retried,
    scenario_keymap_load_failure_retried,
    scenario_rejected_settings_keep_answers,
    scenario_offline_management,
    scenario_configure_other_adapter,
    scenario_management_not_first_nic,
    scenario_no_network_adapter,
    scenario_prepare_runs_before_frontend,
    scenario_prepare_failure_stops_setup,
    scenario_abandoned_setup_leaves_no_handoff,
]

STATIC_SCENARIOS = [scenario_templates_exist, scenario_pure_ascii]

BUSYBOX = shutil.which('busybox')


def report(name, work):
    """Run one scenario, report it, and never stop the rest of the run."""
    try:
        note = work()
    except Exception:  # a scenario failure must not hide the ones after it
        print('FAIL: {}\n    {}'.format(
            name, traceback.format_exc().strip().replace('\n', '\n    ')))
        return False
    if note:
        print('SKIP: {}: {}'.format(name, note))
    else:
        print('PASS: ' + name)
    return True


def main():
    system = shutil.which('sh')
    if not system:
        sys.exit('sh is required')
    shells = [('sh', system)]
    if BUSYBOX:
        # The installer runs busybox ash, which is stricter than most /bin/sh.
        shells.append(('busybox-ash', 'busybox'))
    else:
        print('SKIP: busybox is not installed; the wizard was not run under ash')
    passed = True
    try:
        for scenario in STATIC_SCENARIOS:
            passed &= report(scenario.__name__[len('scenario_'):], scenario)
        for label, shell in shells:
            for scenario in SHELL_SCENARIOS:
                name = '{} [{}]'.format(scenario.__name__[len('scenario_'):], label)

                def work(scenario=scenario, shell=shell):
                    with tempfile.TemporaryDirectory() as directory:
                        return scenario(shell, Path(directory))

                passed &= report(name, work)
    finally:
        if _READER.get('directory'):
            shutil.rmtree(_READER['directory'], ignore_errors=True)
    if not passed:
        sys.exit('text wizard scenarios failed')
    print('PASS: text wizard question order, defaults, keyboard, validation, '
          'offline and handoff')


if __name__ == '__main__':
    main()
