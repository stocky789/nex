#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Compile the real image-edition handoff in nex-config.c (write_image_env and image_seed) with in-memory file
boundaries and check what nex-image-install.sh would read: layout from the disk count, the disk list, the mode,
and the /etc seed files. SIMULATION of the file system only; no disk, no /run, no installer is touched."""
from pathlib import Path
import subprocess
import tempfile

source = Path('installer/gui/nex-config.c').read_text()
start = source.index('/* Stage one file of the /etc seed')
end = source.index('int main(int argc')
block = source[start:end]
assert 'write_image_env(' in block and 'image_seed(' in block

check = r'''
#include <glib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <sys/stat.h>
static GHashTable *files;
static GHashTable *modes;
static char *link_target;
static void fail(const char *message) {g_error("%s",message);}
static void protected_write(const char *path,const char *text) {
    g_hash_table_replace(files,g_strdup(path),g_strdup(text));
}
static int fake_chmod(const char *path,mode_t mode) {
    g_hash_table_replace(modes,g_strdup(path),GINT_TO_POINTER((int)mode));return 0;
}
static int fake_symlink(const char *target,const char *path) {
    g_assert_cmpstr(path,==,"/run/nex-installer/target/etc/localtime");
    g_free(link_target);link_target=g_strdup(target);return 0;
}
#define chmod fake_chmod
#define symlink fake_symlink
#define unlink(path) ((void)(path))
#define g_mkdir_with_parents(path,mode) ((void)(path),(void)(mode),0)
''' + block + r'''
static const char *env(void) {return g_hash_table_lookup(files,"/run/nex-installer/image.env");}
static void reset(void) {
    if (files) {g_hash_table_destroy(files);g_hash_table_destroy(modes);}
    files=g_hash_table_new_full(g_str_hash,g_str_equal,g_free,g_free);
    modes=g_hash_table_new(g_str_hash,g_str_equal);
}
int main(void) {
    char *one[]={"/dev/nvme0n1",NULL}, *two[]={"/dev/sda","/dev/sdb",NULL}, *three[]={"/dev/vda","/dev/vdb","/dev/vdc",NULL};
    reset();
    write_image_env("repair",one,1);
    g_assert_cmpstr(env(),==,"mode=repair\nlayout=single\ndisks=/dev/nvme0n1\n");
    reset();
    write_image_env("repair",two,2);
    g_assert_cmpstr(env(),==,"mode=repair\nlayout=raid1\ndisks=/dev/sda,/dev/sdb\n");
    reset();
    image_seed("Australia/Sydney","de","$6$test-only-hash",three,3);
    g_assert_cmpstr(env(),==,"mode=install\nlayout=raid1\ndisks=/dev/vda,/dev/vdb,/dev/vdc\n");
    g_assert_cmpstr(link_target,==,"../usr/share/zoneinfo/Australia/Sydney");
    g_assert_cmpstr(g_hash_table_lookup(files,"/run/nex-installer/target/etc/timezone"),==,"Australia/Sydney\n");
    g_assert_cmpstr(g_hash_table_lookup(files,"/run/nex-installer/target/etc/vconsole.conf"),==,"KEYMAP=de\n");
    g_assert_nonnull(strstr(g_hash_table_lookup(files,"/run/nex-installer/target/etc/default/keyboard"),"XKBLAYOUT=\"de\""));
    const char *shadow=g_hash_table_lookup(files,"/run/nex-installer/target/etc/shadow");
    g_assert_true(g_str_has_prefix(shadow,"root:$6$test-only-hash:"));
    g_assert_cmpint(GPOINTER_TO_INT(g_hash_table_lookup(modes,"/run/nex-installer/target/etc/shadow")),==,0640);
    reset();
    image_seed("UTC","us","$6$x",one,1);
    g_assert_cmpstr(env(),==,"mode=install\nlayout=single\ndisks=/dev/nvme0n1\n");
    return 0;
}
'''
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    (root / 'check.c').write_text(check)
    flags = subprocess.check_output(['pkg-config', '--cflags', '--libs', 'glib-2.0'], text=True).split()
    subprocess.run(['cc', '-Wall', '-Wextra', '-Werror', str(root / 'check.c'), '-o', str(root / 'check'), *flags], check=True)
    subprocess.run([str(root / 'check')], check=True)
print('PASS: image.env layout/mode/disks and the /etc seed from the real nex-config.c (in-memory file boundary; no disk operations)')
