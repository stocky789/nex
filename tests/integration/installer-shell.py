#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Compile and check the actual Nex phase classifier; requires cc and glib development headers."""
from pathlib import Path
import shlex
import subprocess
import tempfile

source = Path('installer/gui/nex-shell.c').read_text()
log_reader = source[source.index('static gchar *nex_log_tail('):source.index('static gboolean nex_scroll_log(')]
classifier = source[source.index('static int nex_question_stage('):source.index('static GtkWidget *nex_label')]
checks = r'''
int main(int argc, char **argv) {
    gchar *text, *data;
    assert(argc == 2);
    text = nex_log_tail(argv[1]);
    assert(strstr(text, "unavailable")); g_free(text);
    assert(g_file_set_contents(argv[1], "", 0, NULL));
    text = nex_log_tail(argv[1]); assert(!*text); g_free(text);
    data = g_malloc0(200001); memset(data, 'x', 200000);
    memcpy(data, "OLD ENTRY", 9); data[199999] = (char)0xff;
    assert(g_file_set_contents(argv[1], data, 200000, NULL)); g_free(data);
    text = nex_log_tail(argv[1]);
    assert(!strstr(text, "OLD ENTRY")); assert(g_utf8_validate(text, -1, NULL));
    assert(strlen(text) == 128 * 1024 + 2); g_free(text);

    assert(nex_question_stage(NULL) == -1);
    assert(nex_question_stage("") == -1);
    assert(nex_question_stage("debian-installer/language") == 0);
    assert(nex_question_stage("debian-installer/rescue") == -1);
    assert(nex_question_stage("keyboard-configuration/xkb-keymap") == 0);
    assert(nex_question_stage("netcfg/get_hostname") == 1);
    assert(nex_question_stage("passwd/root-password") == 2);
    assert(nex_question_stage("user-setup/password-empty") == 2);
    assert(nex_question_stage("partman/confirm") == 3);
    assert(nex_question_stage("partman-auto/select_disk") == 3);
    assert(nex_question_stage("base-installer/progress") == 4);
    assert(nex_question_stage("unknown/question") == -1);
    return 0;
}
'''
with tempfile.TemporaryDirectory(prefix='nex-shell-check-') as directory:
    path = Path(directory)
    (path / 'check.c').write_text('#include <assert.h>\n#include <glib.h>\n#include <string.h>\n#include <stdio.h>\n' + classifier + log_reader + checks)
    flags = shlex.split(subprocess.check_output(['pkg-config', '--cflags', '--libs', 'glib-2.0'], text=True))
    subprocess.run(['cc', '-Wall', '-Wextra', '-Werror', str(path / 'check.c'), '-o', str(path / 'check'), *flags], check=True)
    subprocess.run([str(path / 'check'), str(path / 'log')], check=True)
print('PASS: actual C phase mapping and log tails: missing/empty files, 128 KiB bound and UTF-8 recovery')
