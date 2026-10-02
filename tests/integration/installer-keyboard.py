#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Compile actual keyboard-change callback; simulate its GTK and process boundary."""
from pathlib import Path
import re, subprocess, tempfile
source=Path('installer/gui/nex-wizard.c').read_text()
assert 'keyboard_changed(GTK_COMBO_BOX(keymap), NULL); prepare();' in source, 'Initial and restarted wizard must apply its displayed layout'
callback=source.split('static void keyboard_changed(',1)[1].split('static gboolean collect_repair(void)',1)[0]
keymaps=re.search(r'static const gchar \*keymaps\[\] = .*?;',source).group()
check=r'''
#include <glib.h>
#include <sys/wait.h>
#include <string.h>
#include <assert.h>
typedef int GtkComboBox;
typedef int GtkWidget;
#define GTK_ENTRY(x) (x)
static GtkWidget p=1,r=2,*password=&p,*repeat=&r;
static gboolean keyboard_ready;
static int selected, cleared, result;
static const char *expected, *notice;
static int gtk_combo_box_get_active(GtkComboBox *box) {(void)box; return selected;}
static void gtk_entry_set_text(GtkWidget *w,const char *s) {assert(w==password||w==repeat); assert(!*s);cleared++;}
static void message(const char *s) {notice=s;}
static gboolean fake_spawn(const gchar *cwd,gchar **argv,gchar **env,GSpawnFlags flags,GSpawnChildSetupFunc setup,gpointer data,gchar **out,gchar **err,gint *status,GError **error) {
 (void)cwd;(void)env;(void)flags;(void)setup;(void)data;(void)out;(void)err;(void)error;
 assert(!strcmp(argv[0],"/usr/bin/setxkbmap"));assert(!strcmp(argv[1],"-layout"));assert(!strcmp(argv[2],expected));assert(argv[3]==NULL);*status=result;return TRUE;
}
#define g_spawn_sync fake_spawn
'''+keymaps+'\nstatic void keyboard_changed('+callback+r'''
int main(void) {
 selected=2;expected="de";result=0;keyboard_changed(NULL,NULL);assert(keyboard_ready&&cleared==2&&! *notice);
 selected=1;expected="gb";result=256;cleared=0;keyboard_changed(NULL,NULL);assert(!keyboard_ready&&cleared==2&&strstr(notice,"could not be applied"));
 return 0;
}
'''
with tempfile.TemporaryDirectory() as d:
 p=Path(d);(p/'check.c').write_text(check)
 flags=subprocess.check_output(['pkg-config','--cflags','--libs','glib-2.0'],text=True).split()
 subprocess.run(['cc','-Wall','-Wextra','-Werror',str(p/'check.c'),'-o',str(p/'check'),*flags],check=True)
 subprocess.run([str(p/'check')],check=True)
print('PASS: selected live keyboard applied, credentials cleared, layout failure blocks handoff (simulated boundary)')
