#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the actual Debian handoff renderer with in-memory file boundaries."""
from pathlib import Path
import subprocess
import tempfile

source = Path('installer/gui/nex-config.c').read_text()
wizard = Path('installer/gui/nex-wizard.c').read_text()
assert 'nex_hostname_normalize(' in source, 'Privileged handoff must validate the hostname'
assert 'nex_hostname_normalize(' in wizard, 'GUI must use the same hostname validation'
render = source.split('    char *hostname=value(config,"hostname"),', 1)[1].split('    /* Retain only non-secret', 1)[0]
check = r'''
#include <glib.h>
#include <string.h>
#include <sys/stat.h>
#include "nex-network.h"
static GHashTable *files;
static char *value(GKeyFile *config, const char *key) {
    return g_key_file_get_string(config,"Host",key,NULL);
}
static char **disk_list(GKeyFile *config, const char *key, gsize *count) {
    char **entries = g_key_file_get_string_list(config, "Host", key, count, NULL);
    if (!entries) { *count = 0; entries = g_new0(char *, 1); }
    return entries;
}
static void fail(const char *message) {g_error("%s",message);}
static gboolean image_mode=FALSE;
static gboolean image_edition(void) {return image_mode;} /* FALSE: classic answers; check_image() flips it */
static void protected_write(const char *path,const char *text) {
    g_hash_table_replace(files,g_strdup(path),g_strdup(text));
}
static int fake_chmod(const char *path,mode_t mode) {
    g_assert_nonnull(g_hash_table_lookup(files,path));g_assert_cmpint(mode,==,0644);return 0;
}
#define chmod fake_chmod
static void render(GKeyFile *config) {
    const char *hash="$6$test-only-hash";
    char *hostname=value(config,"hostname"),''' + render + r'''
    g_string_free(seed,TRUE);g_string_free(targets,TRUE);g_string_free(members,TRUE);g_string_free(var_members,TRUE);
    g_strfreev(name_parts);g_free(hostname);g_free(zone);g_free(keymap);g_strfreev(disk);
}
static void check_hostname(const char *input,const char *shortname,const char *domain,const char *canonical) {
    char *normalized=nex_hostname_normalize(input);g_assert_nonnull(normalized);g_free(normalized);
    GKeyFile *config=g_key_file_new();
    g_key_file_set_string(config,"Host","hostname",input);
    g_key_file_set_string(config,"Host","timezone","UTC");
    g_key_file_set_string(config,"Host","keymap","us");
    g_key_file_set_string(config,"Host","disk","/dev/vda");
    g_hash_table_remove_all(files);render(config);
    char *retained=value(config,"hostname");g_assert_cmpstr(retained,==,canonical);g_free(retained);
    const char *hostname=g_hash_table_lookup(files,"/run/nex-installer/target/etc/hostname");
    const char *hosts=g_hash_table_lookup(files,"/run/nex-installer/target/etc/hosts");
    const char *seed=g_hash_table_lookup(files,"/run/nex-installer/answers.cfg");
    g_assert(strstr(seed,"d-i finish-install/reboot_in_progress seen false\n"));
    g_assert(!strstr(seed,"d-i finish-install/reboot_in_progress note\n"));
    g_assert(strstr(seed,"d-i partman-auto/method string regular\n"));
    g_assert(strstr(seed,"d-i partman-auto/disk string /dev/vda\n"));
    g_assert(strstr(seed,"d-i grub-installer/bootdev string /dev/vda\n"));
    g_assert(!strstr(seed,"partman-auto-raid"));
    char *expected=g_strdup_printf("%s\n",shortname);
    g_assert_cmpstr(hostname,==,expected);g_free(expected);
    expected=g_strdup_printf("127.0.0.1 localhost\n127.0.1.1 %s%s%s\n::1 localhost ip6-localhost ip6-loopback\n",canonical,*domain?" ":"",*domain?shortname:"");
    g_assert_cmpstr(hosts,==,expected);g_free(expected);
    expected=g_strdup_printf("d-i netcfg/get_hostname string %s\nd-i netcfg/get_domain string %s\n",shortname,domain);
    g_assert_nonnull(strstr(seed,expected));g_free(expected);
    g_key_file_free(config);
}
static void check_disks(const char **disks, gsize count, const char *targets, const char *members, const char *var_members) {
    GKeyFile *config=g_key_file_new();
    g_key_file_set_string(config,"Host","hostname","nexcore.example.test");
    g_key_file_set_string(config,"Host","timezone","UTC");
    g_key_file_set_string(config,"Host","keymap","us");
    g_key_file_set_string_list(config,"Host","disk",disks,count);
    g_hash_table_remove_all(files);render(config);
    const char *seed=g_hash_table_lookup(files,"/run/nex-installer/answers.cfg");
    char *disk_line=g_strdup_printf("d-i partman-auto/disk string %s\n",targets);
    g_assert_nonnull(strstr(seed,disk_line));g_free(disk_line);
    if (count == 1) {
        g_assert(strstr(seed,"d-i partman-auto/method string regular\n"));
        g_assert(!strstr(seed,"partman-auto-raid"));
        /* One disk: / and /var are separate ext4 filesystems, never one. */
        g_assert(strstr(seed,"mountpoint{ / } . "));
        g_assert(strstr(seed,"mountpoint{ /var } . "));
        g_assert(!strstr(seed,"atomic"));
        char *boot=g_strdup_printf("d-i grub-installer/bootdev string %s\n",targets);
        g_assert_nonnull(strstr(seed,boot));g_free(boot);
    } else {
        g_assert(strstr(seed,"d-i partman-auto/method string raid\n"));
        char *recipe=g_strdup_printf("d-i partman-auto-raid/recipe string 1 %u 0 ext4 / %s . 1 %u 0 ext4 /var %s .\n",(unsigned)count,members,(unsigned)count,var_members);
        g_assert_nonnull(strstr(seed,recipe));g_free(recipe);
        g_assert(strstr(seed,"d-i partman-partitioning/default_label string gpt\n"));
        g_assert(strstr(seed,"d-i grub-installer/force-efi-extra-removable boolean true\n"));
        gboolean uefi=g_file_test("/sys/firmware/efi",G_FILE_TEST_IS_DIR);
        char *boot=g_strdup_printf("d-i grub-installer/bootdev string %s\n",targets);
        if (uefi) g_assert(strstr(seed,boot)==NULL); else g_assert_nonnull(strstr(seed,boot));
        g_free(boot);
    }
    g_key_file_free(config);
}
/* The image edition has no partman or GRUB step, but Debian Installer still runs its keyboard, root account, clock and
 * network-name steps before the hook that starts the image install. Without their answers it stops to ask the operator
 * again (found in the first QEMU run of the image ISO: "Configure the keyboard" after the wizard). */
static void check_image(void) {
    GKeyFile *config=g_key_file_new();
    const char *disks[]={"/dev/vda","/dev/vdb"};
    g_key_file_set_string(config,"Host","hostname","nexcore.example.test");
    g_key_file_set_string(config,"Host","timezone","Australia/Sydney");
    g_key_file_set_string(config,"Host","keymap","gb");
    g_key_file_set_string_list(config,"Host","disk",disks,2);
    image_mode=TRUE;g_hash_table_remove_all(files);render(config);image_mode=FALSE;
    const char *seed=g_hash_table_lookup(files,"/run/nex-installer/answers.cfg");
    g_assert_nonnull(seed);
    g_assert(strstr(seed,"d-i debian-installer/locale string en_US.UTF-8\n"));
    g_assert(strstr(seed,"d-i keyboard-configuration/xkb-keymap select gb\n"));
    g_assert(strstr(seed,"d-i time/zone string Australia/Sydney\n"));
    g_assert(strstr(seed,"d-i clock-setup/utc boolean true\n"));
    g_assert(strstr(seed,"d-i netcfg/get_hostname string nexcore\n"));
    g_assert(strstr(seed,"d-i passwd/root-login boolean true\n"));
    g_assert(strstr(seed,"d-i passwd/make-user boolean false\n"));
    g_assert(strstr(seed,"d-i passwd/root-password-crypted password $6$test-only-hash\n"));
    g_assert(!strstr(seed,"partman"));
    g_assert(!strstr(seed,"grub-installer"));
    g_key_file_free(config);
}
int main(void) {
    files=g_hash_table_new_full(g_str_hash,g_str_equal,g_free,g_free);
    check_hostname("nexcore.example.test","nexcore","example.test","nexcore.example.test");
    check_hostname("7nex-core.lab.example","7nex-core","lab.example","7nex-core.lab.example");
    check_hostname("nexcore","nexcore","","nexcore");
    check_hostname("NexCore.Example.Test.","NexCore","Example.Test","NexCore.Example.Test");
    const char *one[]={"/dev/sda"};
    const char *two[]={"/dev/sda","/dev/nvme0n1"};
    const char *three[]={"/dev/sda","/dev/nvme0n1","/dev/vda"};
    check_disks(one,1,"/dev/sda","/dev/sda3","/dev/sda4");
    check_disks(two,2,"/dev/sda /dev/nvme0n1","/dev/sda3#/dev/nvme0n1p3","/dev/sda4#/dev/nvme0n1p4");
    check_disks(three,3,"/dev/sda /dev/nvme0n1 /dev/vda","/dev/sda3#/dev/nvme0n1p3#/dev/vda3","/dev/sda4#/dev/nvme0n1p4#/dev/vda4");
    check_image();
    g_hash_table_destroy(files);return 0;
}
'''
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    (root / 'check.c').write_text(check)
    flags = subprocess.check_output(['pkg-config', '--cflags', '--libs', 'glib-2.0'], text=True).split()
    subprocess.run(['cc', '-Wall', '-Wextra', '-Werror', '-Iinstaller/gui', str(root / 'check.c'),
                    'installer/gui/nex-network.c', '-o', str(root / 'check'), *flags], check=True)
    subprocess.run([str(root / 'check')], check=True)
print('PASS: actual hostname/domain preseed and target hostname/hosts generation (in-memory file boundary; no disk operations)')
print('PASS: one-, two- and three-disk partman-auto disk/raid(/ and /var)/bootdev lines from render()')
