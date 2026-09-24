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
assert 'Tick one disk for a single install, or two or more for RAID1 copies of one system disk.' in wizard
assert 'Ticked disks are erased; others are left alone.' in wizard
# Nested page+tree scrollbars and unwrapped summaries clipped BMC consoles; pin wrap width
# and keep the disk list in its own scrolled window with ellipsized columns.
assert 'GTK_POLICY_NEVER' in wizard and 'wrap_label_allocate' in wizard
assert 'PANGO_ELLIPSIZE_END' in wizard and 'disk_text_column' in wizard
assert 'gtk_scrolled_window_set_shadow_type' in wizard
assert 'compact' in wizard and 'gdk_screen_get_height' in wizard
# The tick list, its keyboard toggle and the live summary are what make a mirror discoverable.
assert 'gtk_cell_renderer_toggle_new()' in wizard and 'disk_toggled' in wizard
assert 'GDK_space' in wizard and 'GDK_Return' in wizard, 'the tick list must be usable from the keyboard'
for column in ('"Use"', '"Device"', '"Capacity"', '"Type"', '"Model"', '"Serial"', '"Contents"'):
    assert column in wizard, column
assert 'queue/rotational' in wizard and 'has_partitions' in wizard
assert 'What you get: one %.1f GiB system disk on all %u drives' in wizard
assert 'RAID1 copies — not pooled storage' in wizard
assert 'What you get: one system disk, no redundancy.' in wizard
assert 'Tip: two or more similar-sized disks give RAID1 copies' in wizard
assert "foreground='#B3261E'" in wizard, 'selection problems must be reported as the operator ticks'
assert 'if (disks->len == 1)' in wizard, 'a single eligible disk is preselected; a mirror never is'
assert 'bytes < 16ULL * 1024 * 1024 * 1024) continue' in wizard, 'GTK discover must skip disks below 16 GiB like the text frontend'
assert 'Attach a writable internal disk and restart the installer.' in wizard
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
    }
    assert(s && "unexpected filesystem read");
    *out=g_strdup(s); return TRUE;
}
static char *stub_serial(const char *block) {
    return g_strdup(!strcmp(scenario,"serial") ? "replacement"
        : strstr(block,"/xvdc") ? "third-disk"
        : strstr(block,"/xvdb") ? "partner-disk" : "test-disk");
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
#define nex_install_disk_serial stub_serial
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
    assert(strstr(text, "Tip: two or more similar-sized disks give RAID1 copies")); g_free(text);
    text = disk_summary_text(1, 2, 128 * gib, 128 * gib, TRUE);
    assert(!strcmp(text, "What you get: one system disk, no redundancy.")); g_free(text);
    text = disk_summary_text(2, 2, 128 * gib, 128 * gib, TRUE);
    assert(strstr(text, "What you get: one 128.0 GiB system disk on all 2 drives (RAID1 copies — not pooled storage). Survives 1 disk failure.")); g_free(text);
    text = disk_summary_text(2, 2, 100 * gib, 105 * gib, TRUE);
    assert(strstr(text, "one 100.0 GiB system disk on all 2 drives")); g_free(text);
    text = disk_summary_text(2, 2, 100 * gib, 112 * gib, TRUE);
    assert(strstr(text, "within 10% of the smallest selected disk")); g_free(text);
    text = disk_summary_text(3, 3, 50 * gib, 50 * gib, TRUE);
    assert(strstr(text, "What you get: one 50.0 GiB system disk on all 3 drives (RAID1 copies — not pooled storage). Survives 2 disk failures.")); g_free(text);
    text = disk_summary_text(5, 5, 128 * gib, 128 * gib, TRUE);
    assert(strstr(text, "What you get: one 128.0 GiB system disk on all 5 drives (RAID1 copies — not pooled storage). Survives 4 disk failures.")); g_free(text);
    return 0;
}
'''
# BUG-0529: GTK2 expand columns could never be dragged narrower and the other columns sat at
# their minimum widths. Allocate the real disk list under Xvfb and drag its dividers with XTest.
columns = wizard[wizard.index('/* Wide Model/Serial columns must ellipsize'):wizard.index('/* GtkLabel wrap ignores')]
column_harness = r'''
#include <gtk/gtk.h>
#include <assert.h>
enum { DISK_SELECTED, DISK_NAME, DISK_CAPACITY, DISK_TYPE, DISK_MODEL, DISK_SERIAL, DISK_CONTENTS, DISK_INDEX, DISK_COLUMNS };
static void disk_toggled(GtkCellRendererToggle *cell, gchar *path, gpointer unused) { (void)cell; (void)path; (void)unused; }
static gboolean disk_key(GtkWidget *widget, GdkEventKey *event, gpointer unused) { (void)widget; (void)event; (void)unused; return FALSE; }
''' + columns + r'''
static GtkWidget *view;
static gint width(gint i) { return gtk_tree_view_column_get_width(gtk_tree_view_get_column(GTK_TREE_VIEW(view), i)); }
static void settle(void) { gdk_flush(); g_usleep(150000); while (gtk_events_pending()) gtk_main_iteration(); }
/* Drag a column's right-hand divider (its own 6 px handle window) by dx pixels through
 * xdotool, a separate X client, exactly as an operator's pointer arrives; the main loop
 * keeps running meanwhile so the list handles the press, motion and release live. */
static void drag(gint i, gint dx)
{
    GtkTreeViewColumn *column = gtk_tree_view_get_column(GTK_TREE_VIEW(view), i);
    gint x, y; gchar *command;
    gdk_window_get_origin(column->window, &x, &y);
    x += 3; y += column->button->allocation.height / 2;
    command = g_strdup_printf("xdotool mousemove %d %d sleep 0.1 mousedown 1 sleep 0.2 mousemove %d %d sleep 0.1 mousemove %d %d sleep 0.1 mouseup 1",
        x, y, x + dx / 2, y, x + dx, y);
    assert(g_spawn_command_line_async(command, NULL));
    g_free(command);
    for (i = 0; i < 10; i++) settle();
    g_printerr("after dragging %s by %d:", gtk_tree_view_column_get_title(column), dx);
    for (i = 0; i < 7; i++) g_printerr(" %d", width(i));
    g_printerr("\n");
}
int main(int argc, char **argv)
{
    GtkWidget *window, *scroll; GtkTreeIter iter; GtkAdjustment *h;
    gtk_init(&argc, &argv);
    window = gtk_window_new(GTK_WINDOW_TOPLEVEL); gtk_window_set_default_size(GTK_WINDOW(window), 940, 200);
    view = disk_list_widget();
    gtk_list_store_append(GTK_LIST_STORE(gtk_tree_view_get_model(GTK_TREE_VIEW(view))), &iter);
    gtk_list_store_set(GTK_LIST_STORE(gtk_tree_view_get_model(GTK_TREE_VIEW(view))), &iter, DISK_NAME, "/dev/nvme0n1",
        DISK_CAPACITY, "1863.0 GiB", DISK_TYPE, "NVMe", DISK_MODEL, "Example Vendor Datacentre NVMe 2TB Model Name",
        DISK_SERIAL, "EXAMPLE0123456789SERIAL", DISK_CONTENTS, "Has partitions", -1);
    scroll = gtk_scrolled_window_new(NULL, NULL);
    gtk_scrolled_window_set_policy(GTK_SCROLLED_WINDOW(scroll), GTK_POLICY_AUTOMATIC, GTK_POLICY_AUTOMATIC);
    gtk_container_add(GTK_CONTAINER(scroll), view); gtk_container_add(GTK_CONTAINER(window), scroll);
    gtk_widget_show_all(window); settle();
    /* Default spread: Use 40 px, then 13/12/8/25/22/20 % of the remaining 900 px, filling the list. */
    assert(width(0) == 40 && width(1) == 117 && width(2) == 108 && width(3) == 72);
    assert(width(4) == 225 && width(5) == 198 && width(6) == 180);
    h = gtk_tree_view_get_hadjustment(GTK_TREE_VIEW(view));
    assert(gtk_adjustment_get_upper(h) == gtk_adjustment_get_page_size(h));
    drag(4, -60); assert(width(4) == 165 && width(6) == 240);  /* Model shrinks; Contents takes the slack */
    drag(5, -50); assert(width(5) == 148 && width(6) == 290);
    drag(1, 40); assert(width(1) == 157 && width(6) == 250);  /* Device grows at Contents' expense */
    return 0;
}
'''
serial_harness = r'''
#include <stdio.h>
#include "nex-network.h"
int main(int argc, char **argv) { gchar *s = nex_install_disk_serial(argv[argc - 1]); printf("%s", s); g_free(s); return 0; }
'''
with tempfile.TemporaryDirectory(prefix='nex-disk-check-') as directory:
    path=Path(directory)
    (path/'check.c').write_text(harness)
    (path/'serial.c').write_text(serial_harness)
    (path/'summary.c').write_text(summary_harness)
    flags=shlex.split(subprocess.check_output(['pkg-config','--cflags','--libs','glib-2.0'],text=True))
    subprocess.run(['cc','-Wall','-Wextra','-Werror','-Iinstaller/gui',str(path/'check.c'),'installer/gui/nex-network.c','-o',str(path/'check'),*flags],check=True)
    subprocess.run(['cc','-Wall','-Wextra','-Werror',str(path/'summary.c'),'-o',str(path/'summary'),*flags],check=True)
    subprocess.run([str(path/'summary')],check=True)
    subprocess.run(['cc','-Wall','-Wextra','-Werror','-Iinstaller/gui',str(path/'serial.c'),'installer/gui/nex-network.c','-o',str(path/'serial'),*flags],check=True)
    # BUG-0530: only NVMe has device/serial. virtio-blk reports serial and SATA/SAS/SCSI only
    # the binary unit-serial VPD page, so the same helper falls back in that order.
    for name, files, expected in [
        ('nvme0n1', {'device/serial': b'  NVME0123456   \n', 'device/vpd_pg80': b'\x00\x80\x00\x05OTHER'}, 'NVME0123456'),
        ('vda', {'serial': b'virtio-serial-1\n'}, 'virtio-serial-1'),
        ('sda', {'device/vpd_pg80': b'\x00\x80\x00\x14S3Z8NB0K123456X     \x00\x00', 'device/model': b'EXAMPLE SSD\n'}, 'S3Z8NB0K123456X'),
        ('sdb', {'device/vpd_pg80': b'\x00\x80\x00\x00'}, ''),
        ('xvda', {'device/model': b'\n'}, ''),
    ]:
        block=path/'sys/class/block'/name
        for attribute, data in files.items():
            (block/attribute).parent.mkdir(parents=True, exist_ok=True); (block/attribute).write_bytes(data)
        actual=subprocess.run([str(path/'serial'),str(block)],capture_output=True,text=True,check=True).stdout
        assert actual==expected, (name, actual, expected)
    (path/'columns.c').write_text(column_harness)
    gtk=shlex.split(subprocess.check_output(['pkg-config','--cflags','--libs','gtk+-2.0'],text=True))
    subprocess.run(['cc','-Wall','-Wextra','-Werror','-Wno-deprecated-declarations',str(path/'columns.c'),'-o',str(path/'columns'),*gtk],check=True)
    # xvfb-run's default screen is 640x480: wide enough for a BMC console, not for the 940 px list.
    subprocess.run(['xvfb-run','--auto-servernum','--server-args=-screen 0 1280x1024x24',str(path/'columns')],check=True)
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
print('PASS: disk list columns share the width by weight and every divider drags narrower or wider (Xvfb)')
print('PASS: nex_install_disk_serial reads NVMe device/serial, virtio-blk serial and the SCSI unit-serial VPD page, in that order')
