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
static void fail(const char *message) {g_error("%s",message);}
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
    g_string_free(seed,TRUE);g_strfreev(name_parts);
    g_free(hostname);g_free(zone);g_free(keymap);g_free(disk);
}
static void check(const char *input,const char *shortname,const char *domain,const char *canonical) {
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
    char *expected=g_strdup_printf("%s\n",shortname);
    g_assert_cmpstr(hostname,==,expected);g_free(expected);
    expected=g_strdup_printf("127.0.0.1 localhost\n127.0.1.1 %s%s%s\n::1 localhost ip6-localhost ip6-loopback\n",canonical,*domain?" ":"",*domain?shortname:"");
    g_assert_cmpstr(hosts,==,expected);g_free(expected);
    expected=g_strdup_printf("d-i netcfg/get_hostname string %s\nd-i netcfg/get_domain string %s\n",shortname,domain);
    g_assert_nonnull(strstr(seed,expected));g_free(expected);
    g_key_file_free(config);
}
int main(void) {
    files=g_hash_table_new_full(g_str_hash,g_str_equal,g_free,g_free);
    check("nexcore.example.test","nexcore","example.test","nexcore.example.test");
    check("7nex-core.lab.example","7nex-core","lab.example","7nex-core.lab.example");
    check("nexcore","nexcore","","nexcore");
    check("NexCore.Example.Test.","NexCore","Example.Test","NexCore.Example.Test");
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
