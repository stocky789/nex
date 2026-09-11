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
validator = source.split('static void fail(', 1)[1].split('static void protected_write(', 1)[0]
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
        else if (g_str_has_suffix(path,"/size")) s=!strcmp(scenario,"small") ? "2048" : "104857600";
        else if (g_str_has_suffix(path,"/dev")) s=!strcmp(scenario,"identity") ? "202:16" : "202:0";
        else if (g_str_has_suffix(path,"/device/serial")) s=!strcmp(scenario,"serial") ? "replacement" : "test-disk";
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
    g_key_file_set_string(config,"Host","disk",argv[2]);
    g_key_file_set_string(config,"Host","disk_size",!strcmp(scenario,"capacity") ? "53687091712" : "53687091200");
    g_key_file_set_string(config,"Host","disk_serial","test-disk");
    g_key_file_set_string(config,"Host","disk_major_minor","202:0");
    g_key_file_set_string(config,"Network","management","");
    validate(config,TRUE);
    g_key_file_free(config); return 0;
}
'''
with tempfile.TemporaryDirectory(prefix='nex-disk-check-') as directory:
    path=Path(directory)
    (path/'check.c').write_text(harness)
    flags=shlex.split(subprocess.check_output(['pkg-config','--cflags','--libs','glib-2.0'],text=True))
    subprocess.run(['cc','-Wall','-Wextra','-Werror','-Iinstaller/gui',str(path/'check.c'),'installer/gui/nex-network.c','-o',str(path/'check'),*flags],check=True)
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
print('PASS: actual disk validator accepts Xen/SCSI/virtio/NVMe whole disks and rejects unsafe targets (simulated device boundary)')
