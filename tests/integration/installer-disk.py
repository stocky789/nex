#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Run the actual handoff validator against simulated block-device boundaries.

No real disks are accessed. This guards Xen disk naming and existing erase safety;
it does not establish installation support on a real Xen hypervisor.
"""
from pathlib import Path
import os
import shlex
import subprocess
import tempfile

source = Path(os.environ.get('NEX_DISK_VALIDATOR_SOURCE', 'installer/gui/nex-config.c')).read_text()
wizard = Path('installer/gui/nex-wizard.c').read_text()
assert 'Tick one disk, or two or more to mirror.\\nEverything on the ticked disks is erased; other disks are left alone.' in wizard, 'Disk warning must retain its visible continuation line at 800x600'
# The tick list, its keyboard toggle and the live summary are what make a mirror discoverable.
assert 'gtk_cell_renderer_toggle_new()' in wizard and 'disk_toggled' in wizard
assert 'GDK_space' in wizard and 'GDK_Return' in wizard, 'the tick list must be usable from the keyboard'
for column in ('"Use"', '"Device"', '"Capacity"', '"Type"', '"Model"', '"Serial"', '"Contents"'):
    assert column in wizard, column
assert 'queue/rotational' in wizard and 'has_partitions' in wizard
assert 'RAID1 mirror, smallest disk %.1f GiB, keeps running if one disk fails.' in wizard
assert '1 disk: single disk, ext4, no redundancy.' in wizard
assert 'Tip: tick two disks of similar size' in wizard
assert "foreground='#B3261E'" in wizard, 'selection problems must be reported as the operator ticks'
assert 'if (disks->len == 1)' in wizard, 'a single eligible disk is preselected; a mirror never is'
assert 'bytes < 16ULL * 1024 * 1024 * 1024) continue' in wizard, 'GTK discover must skip disks below 16 GiB like the text frontend'
assert 'writable internal disk attached,\\nthen restart the installer.' in wizard, 'No-disk help must retain a visible continuation line at 800x600'
validator = source.split('static void fail(', 1)[1].split('static void protected_write(', 1)[0]
summary = wizard[wizard.index('static gchar *disk_summary_text('):wizard.index('static void disks_changed(')]
harness = r'''
#define _GNU_SOURCE
#include <glib.h>
#include <sys/stat.h>
#include <sys/sysmacros.h>
#include <stdlib.h>
#include <string.h>
#include <assert.h>
#include "nex-network.h"
static const char *scenario;
static gboolean contents(const gchar *path, gchar **out, gsize *length, GError **error) {
    (void)length; (void)error;
    const char *s=NULL;
    if (!strcmp(path,"/usr/share/nex-installer/timezones")) s="UTC\n";
    else if (!strcmp(path,"/proc/mounts")) s=!strcmp(scenario,"media") ? "/dev/xvda1 /cdrom iso9660 ro 0 0\n" : "";
    else if (g_str_has_prefix(path,"/sys/class/block/")) {
        if (g_str_has_suffix(path,"/ro")) s=!strcmp(scenario,"readonly") ? "1" : "0";
        else if (g_str_has_suffix(path,"/removable")) s=!strcmp(scenario,"removable") ? "1" : "0";
        /* Mirror partners: half, ~5% smaller, just over 10% smaller, or a third equal member. */
        else if (g_str_has_suffix(path,"/size")) {
            if (!strcmp(scenario,"small")) s="2048";
            else if (strstr(path,"/xvdb/")) {
                if (!strcmp(scenario,"mismatch")) s="52428800";
                else if (!strcmp(scenario,"near")) s="99614720";
                else if (!strcmp(scenario,"over")) s="93323264";
                else s="104857600";
            } else if (strstr(path,"/xvdc/")) s="104857600";
            else s="104857600";
        }
        else if (g_str_has_suffix(path,"/dev")) s=!strcmp(scenario,"identity") ? "202:16"
            : strstr(path,"/xvdc/") ? "202:32"
            : strstr(path,"/xvdb/") ? "202:16" : "202:0";
        else if (g_str_has_suffix(path,"/device/serial")) s=!strcmp(scenario,"serial") ? "replacement"
            : strstr(path,"/xvdc/") ? "third-disk"
            : strstr(path,"/xvdb/") ? "partner-disk" : "test-disk";
    }
    assert(s && "unexpected filesystem read");
    *out=g_strdup(s); return TRUE;
}
static int disk_stat(const char *path, struct stat *st) {
    assert(g_str_has_prefix(path,"/dev/"));
    if (!strcmp(scenario,"missing")) return -1;
    memset(st,0,sizeof *st);
    st->st_mode=!strcmp(scenario,"regular") ? S_IFREG : S_IFBLK;
    st->st_rdev=makedev(202,1); return 0;
}
static gboolean file_test(const gchar *path, GFileTest test) {
    assert(test==G_FILE_TEST_EXISTS && g_str_has_suffix(path,"/partition"));
    return !strcmp(scenario,"partition");
}
static char *disk_realpath(const char *path, char *resolved) {
    assert(!resolved);
    if (g_str_has_prefix(path,"/sys/dev/block/")) return strdup("/sys/devices/vbd/block/xvda/xvda1");
    assert(!strcmp(path,"/sys/class/block/xvda"));
    return strdup("/sys/devices/vbd/block/xvda");
}
#define g_file_get_contents contents
#define stat(path, out) disk_stat(path, out)
#define g_file_test file_test
#define realpath disk_realpath
static void fail(''' + validator + r'''
int main(int argc, char **argv) {
    assert(argc==3); scenario=argv[1];
    GKeyFile *config=g_key_file_new();
    g_key_file_set_string(config,"Host","hostname","nexcore.example.test");
    g_key_file_set_string(config,"Host","timezone","UTC");
    g_key_file_set_string(config,"Host","keymap","us");
    g_key_file_set_string(config,"Host","password","disposable-secret");
    /* One disk, or a ;-separated mirror, exactly as both wizards write it. */
    char **names=g_strsplit(argv[2],";",-1);
    GString *disk=g_string_new(""),*size=g_string_new(""),*serial=g_string_new(""),*dev=g_string_new("");
    for (int i=0;names[i];i++) {
        gboolean partner=g_str_has_suffix(names[i],"xvdb");
        gboolean third=g_str_has_suffix(names[i],"xvdc");
        g_string_append_printf(disk,"%s;",names[i]);
        const char *bytes="53687091200";
        if (!strcmp(scenario,"capacity")) bytes="53687091712";
        else if (partner && !strcmp(scenario,"mismatch")) bytes="26843545600";
        else if (partner && !strcmp(scenario,"near")) bytes="51002736640";
        else if (partner && !strcmp(scenario,"over")) bytes="47781511168";
        g_string_append_printf(size,"%s;",bytes);
        g_string_append_printf(serial,"%s;",third ? "third-disk" : partner ? "partner-disk" : "test-disk");
        g_string_append_printf(dev,"%s;",third ? "202:32" : partner ? "202:16" : "202:0");
    }
    g_key_file_set_string(config,"Host","disk",disk->str);
    g_key_file_set_string(config,"Host","disk_size",size->str);
    g_key_file_set_string(config,"Host","disk_serial",serial->str);
    g_key_file_set_string(config,"Host","disk_major_minor",dev->str);
    g_key_file_set_string(config,"Network","management","");
    validate(config,TRUE);
    g_key_file_free(config); return 0;
}
'''
summary_harness = r'''
#include <glib.h>
#include <string.h>
#include <assert.h>
''' + summary + r'''
int main(void) {
    gchar *text;
    const guint64 gib = 1024ULL * 1024 * 1024;
    text = disk_summary_text(0, 1, 0, 0, FALSE);
    assert(!strcmp(text, "No disk selected.")); g_free(text);
    text = disk_summary_text(0, 2, 0, 0, TRUE);
    assert(strstr(text, "Tip: tick two disks of similar size")); g_free(text);
    text = disk_summary_text(1, 2, 128 * gib, 128 * gib, TRUE);
    assert(!strcmp(text, "1 disk: single disk, ext4, no redundancy.")); g_free(text);
    text = disk_summary_text(2, 2, 128 * gib, 128 * gib, TRUE);
    assert(strstr(text, "2 disks: RAID1 mirror, smallest disk 128.0 GiB")); g_free(text);
    text = disk_summary_text(2, 2, 100 * gib, 105 * gib, TRUE);
    assert(strstr(text, "smallest disk 100.0 GiB")); g_free(text);
    text = disk_summary_text(2, 2, 100 * gib, 112 * gib, TRUE);
    assert(strstr(text, "within 10% of the smallest selected disk")); g_free(text);
    text = disk_summary_text(3, 3, 50 * gib, 50 * gib, TRUE);
    assert(strstr(text, "3 disks: RAID1 mirror, smallest disk 50.0 GiB")); g_free(text);
    return 0;
}
'''
with tempfile.TemporaryDirectory(prefix='nex-disk-check-') as directory:
    path=Path(directory)
    (path/'check.c').write_text(harness)
    (path/'summary.c').write_text(summary_harness)
    flags=shlex.split(subprocess.check_output(['pkg-config','--cflags','--libs','glib-2.0'],text=True))
    subprocess.run(['cc','-Wall','-Wextra','-Werror','-Iinstaller/gui',str(path/'check.c'),'installer/gui/nex-network.c','-o',str(path/'check'),*flags],check=True)
    subprocess.run(['cc','-Wall','-Wextra','-Werror',str(path/'summary.c'),'-o',str(path/'summary'),*flags],check=True)
    subprocess.run([str(path/'summary')],check=True)
    def check(scenario, disk, error=None):
        result=subprocess.run([str(path/'check'),scenario,disk],capture_output=True,text=True)
        assert result.returncode==(1 if error else 0), (scenario,disk,result.returncode,result.stderr)
        if error:
            assert error in result.stderr, (scenario,disk,result.stderr)
    for name in ['xvda','xvdz','xvdaa','sda','sdaa','vda','vdaa','nvme0n1','nvme12n34','nvme0c0n1']:
        check('valid','/dev/'+name)
    for name in ['xvda/../sda','sda_bad','loop0','sr0','Xvda','']:
        check('valid','/dev/'+name,'supported installation disk')
    for name in ['xvda1','sda1','vda1','nvme0n1p1']:
        check('partition','/dev/'+name,'whole disk')
    for scenario, error in {
        'missing':'no longer available', 'regular':'no longer available',
        'partition':'whole disk', 'readonly':'read-only', 'removable':'Removable media',
        'small':'capacity changed', 'capacity':'capacity changed',
        'identity':'identity changed', 'serial':'serial changed', 'media':'Installation media cannot be erased',
    }.items():
        check(scenario,'/dev/xvda',error)
    # A mirror validates every member: the same identity rules, a capacity within
    # 10% of the smallest member, and no disk selected twice.
    check('valid','/dev/xvda;/dev/xvdb')
    check('near','/dev/xvda;/dev/xvdb')  # ~5% apart must still be accepted
    check('over','/dev/xvda;/dev/xvdb','within 10%')  # just over 10% must be refused
    check('valid','/dev/xvda;/dev/xvdb;/dev/xvdc')  # three equal members
    check('mismatch','/dev/xvda;/dev/xvdb','within 10%')
    check('valid','/dev/xvda;/dev/xvda','only once')
    check('valid','/dev/xvda;/dev/sda_bad','supported installation disk')
    check('serial','/dev/xvda;/dev/xvdb','serial changed')
    check('valid','','Select the disk or disks')
print('PASS: actual disk validator accepts Xen/SCSI/virtio/NVMe whole disks, validates every mirror member, and rejects unsafe targets (simulated device boundary)')
print('PASS: disk_summary_text reports single, equal mirror, 5%-apart, over-10% and empty selections')
