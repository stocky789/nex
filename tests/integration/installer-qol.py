#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Run actual installer callbacks with simulated GTK boundaries; native ISO test complements this."""
from pathlib import Path
import subprocess
import tempfile

source = Path('installer/gui/nex-wizard.c').read_text()
assert 'keyboard_test' not in source, 'Unrequested keyboard-test field remains'
assert 'gtk_entry_completion' not in source, 'Timezone still requires a text entry'
assert 'timezone_area = gtk_combo_box_new_text()' in source
assert 'zone_entry = gtk_combo_box_new_text()' in source
callbacks = source[source.index('static void fit_screen('):source.index('static void timezone_area_changed(')]
timezone_callback = source[source.index('static void timezone_area_changed('):source.index('static void timezone_choices(')]
visibility = source[source.index('static void password_visibility('):source.index('static void keyboard_changed(')]
check = r'''
#include <glib.h>
#include <assert.h>
#include <string.h>
typedef int GdkScreen;
typedef int GtkToggleButton;
typedef struct { GPtrArray *items; int active; } GtkComboBox;
#define GTK_COMBO_BOX(x) (x)
#define GTK_LIST_STORE(x) (x)
static GtkComboBox zone, *zone_entry=&zone;
static gchar *timezones[]={"America/New_York", "Australia/Perth", "Australia/Sydney", "UTC", NULL};
static gchar *gtk_combo_box_get_active_text(GtkComboBox *box) {return box->active < 0 ? NULL : g_strdup(g_ptr_array_index(box->items,box->active));}
static GPtrArray *gtk_combo_box_get_model(GtkComboBox *box) {return box->items;}
static void gtk_list_store_clear(GPtrArray *items) {g_ptr_array_set_size(items,0);}
static void gtk_combo_box_append_text(GtkComboBox *box,const gchar *text) {g_ptr_array_add(box->items,g_strdup(text));}
static void gtk_combo_box_set_active(GtkComboBox *box,int active) {box->active=active;}

#define GTK_WINDOW(x) (x)
#define GTK_ENTRY(x) (x)
static int window, password, repeat;
static int width=1024, height=768, moved, resized, native_resized, realized=1;
static int visible, visibility_calls;

static int gdk_screen_get_width(GdkScreen *s) {(void)s;return width;}
static int gdk_screen_get_height(GdkScreen *s) {(void)s;return height;}
static void gtk_window_move(int w,int x,int y) {(void)w;assert(x==0&&y==0);moved++;}
static void gtk_window_resize(int w,int x,int y) {(void)w;assert(x==width&&y==height);resized++;}
static int gtk_widget_get_window(int w) {(void)w;return realized;}
static void gdk_window_move_resize(int w,int x,int y,int a,int b) {(void)w;assert(!x&&!y&&a==width&&b==height);native_resized++;}
static int gtk_toggle_button_get_active(GtkToggleButton *b) {(void)b;return visible;}
static void gtk_entry_set_visibility(int entry,int value) {(void)entry;assert(value==visible);visibility_calls++;}
''' + callbacks + timezone_callback + visibility + r'''
int main(void) {
 GtkComboBox area={g_ptr_array_new_with_free_func(g_free),0};
 zone.items=g_ptr_array_new_with_free_func(g_free);
 gtk_combo_box_append_text(&area,"UTC");gtk_combo_box_append_text(&area,"Australia");gtk_combo_box_append_text(&area,"America");
 timezone_area_changed(&area,NULL);assert(zone.items->len==1&&!strcmp(g_ptr_array_index(zone.items,0),"UTC"));
 area.active=1;timezone_area_changed(&area,NULL);assert(zone.items->len==2&&zone.active==0&&!strcmp(g_ptr_array_index(zone.items,1),"Australia/Sydney"));
 zone.active=1;area.active=2;timezone_area_changed(&area,NULL);assert(zone.items->len==1&&zone.active==0&&!strcmp(g_ptr_array_index(zone.items,0),"America/New_York"));
 area.active=-1;timezone_area_changed(&area,NULL);assert(zone.items->len==0);
 g_ptr_array_free(area.items,TRUE);g_ptr_array_free(zone.items,TRUE);
 fit_screen(NULL,NULL);assert(moved==1&&resized==1&&native_resized==1);
 width=800;height=600;realized=0;fit_screen(NULL,NULL);assert(moved==2&&resized==2&&native_resized==1);
 visible=1;password_visibility(NULL,NULL);assert(visibility_calls==2);
 visible=0;password_visibility(NULL,NULL);assert(visibility_calls==4);
 return 0;
}
'''
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    (root / 'check.c').write_text(check)
    flags = subprocess.check_output(['pkg-config', '--cflags', '--libs', 'glib-2.0'], text=True).split()
    subprocess.run(['cc', '-Wall', '-Wextra', '-Werror', str(root / 'check.c'), '-o', str(root / 'check'), *flags], check=True)
    subprocess.run([str(root / 'check')], check=True)
print('PASS: fullscreen geometry, noneditable timezone controls and area switching and both password fields (simulated GTK boundary)')
