#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Build Nex's presentation layer against the exact installer ABI, inside builder.
set -euo pipefail
cd /work
cache=/work/.cache/nex-iso/gui
stage=/work/.cache/nex-iso/stage
source=$cache/cdebconf
mkdir -p "$cache"
(cd "$cache" && apt-get source --download-only cdebconf=0.280)
rm -rf "$source"
dpkg-source -x "$cache/cdebconf_0.280.dsc" "$source"
cp installer/gui/nex-shell.c "$source/src/modules/frontend/gtk/"
cp installer/assets/nex-core-lockup.png "$source/nex-core-lockup.png"
cp installer/gui/nex-{wizard,network,config}.c installer/gui/nex-network.h "$source/"
cp installer/gui/dhcp-preview.sh "$source/"
python3 - "$source" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1]) / 'src/modules/frontend/gtk/ui.c'
s = p.read_text()
# Exact pinned source boundaries; fail rather than silently patch a different ABI.
start = '/** Handle the expose event for the banner.'
end = '/** Create the main container where questions or progress will be added.'
assert s.count(start) == s.count(end) == 1
s = s[:s.index(start)] + s[s.index(end):]
start = '/** Create all common widgets inside the main window.'
end = '/** Handle the closing of the frontend window.'
assert s.count(start) == s.count(end) == 1
s = s[:s.index(start)] + '#include "nex-shell.c"\n\n' + s[s.index(end):]
old = 'tmp = g_strdup_printf("<b>%s</b>", fe->title);'
assert s.count(old) == 1
s = s.replace(old, 'tmp = g_markup_printf_escaped("<b>%s</b>", fe->title ? fe->title : "Nex Core setup");')
old = 'gtk_label_set_markup(GTK_LABEL(fe_data->title), tmp);'
assert s.count(old) == 1
s = s.replace(old, old + '\n    nex_update_shell(fe);')
# Questions/errors retain their native handlers; progress uses one Nex panel.
old = '    gtk_widget_show_all(fe_data->target_box);'
assert s.count(old) == 1
s = s.replace(old, '    cdebconf_gtk_nex_set_waiting(fe, FALSE);\n' + old)
old = '                         cdebconf_gtk_callback_widget_destroy, NULL /* no data */);'
assert s.count(old) == 1
s = s.replace(old, old + '\n    cdebconf_gtk_nex_set_waiting(fe, TRUE);')
p.write_text(s)
p = p.with_name('ui.h')
s = p.read_text()
old = '#endif /* !_UI_H_ */'
assert s.count(old) == 1
s = s.replace(old, 'void cdebconf_gtk_nex_set_waiting(struct frontend *fe, gboolean waiting);\nvoid cdebconf_gtk_nex_progress(struct frontend *fe, const char *step, const char *info, gdouble fraction);\nvoid cdebconf_gtk_nex_completion(GtkWidget *question_box);\nvoid cdebconf_gtk_nex_failure(struct frontend *fe, GtkWidget *question_box);\n\n' + old)
p.write_text(s)
p = p.with_name('progress.c')
s = p.read_text()
# Keep the original widgets as private state for title/fraction and cancellation,
# but never mount or show the legacy progress layout.
old = """    if (NULL == gtk_widget_get_parent(progress_data->progress_box)) {
        gtk_box_pack_start(
            GTK_BOX(fe_data->target_box), progress_data->progress_box,
            FALSE /* don't expand */, FALSE /* don't fill */, DEFAULT_PADDING);
    }
"""
assert s.count(old) == 1
s = s.replace(old, '')
old = '    g_object_ref(G_OBJECT(progress_box));'
assert s.count(old) == 1
s = s.replace(old, '    g_object_ref_sink(G_OBJECT(progress_box));')
old = '        g_object_unref(progress_box);\n        gtk_widget_destroy(progress_box);'
assert s.count(old) == 1
s = s.replace(old, '        gtk_widget_destroy(progress_box);\n        g_object_unref(progress_box);')
old = '    progress_data->progress_box = progress_box;'
assert s.count(old) == 1
s = s.replace(old, old + '\n    gtk_widget_set_no_show_all(progress_box, TRUE);')
old = '        if (NULL != progress_data->progress_box) {'
assert s.count(old) == 1
s = s.replace(old, '        if (NULL != progress_data->progress_box &&\n            NULL != gtk_widget_get_parent(progress_data->progress_box)) {')
old = '    gtk_widget_show_all(progress_data->progress_box);'
assert s.count(old) == 1
s = s.replace(old, """    cdebconf_gtk_nex_progress(fe,
        gtk_progress_bar_get_text(GTK_PROGRESS_BAR(progress_data->progress_bar)),
        gtk_entry_get_text(GTK_ENTRY(progress_data->progress_label)),
        gtk_progress_bar_get_fraction(GTK_PROGRESS_BAR(progress_data->progress_bar)));""")
old = '    gtk_entry_set_text(GTK_ENTRY(progress_data->progress_label), info_desc);'
assert s.count(old) == 1
s = s.replace(old, old + '\n    cdebconf_gtk_show_progress(fe);')
old = '    destroy_progress(fe);'
assert s.count(old) == 1
s = s.replace(old, old + '\n    cdebconf_gtk_nex_set_waiting(fe, TRUE);')
p.write_text(s)
p = p.with_name('cdebconf_gtk.c')
s = p.read_text()
old = '    gtk_widget_show_all(fe_data->window);'
assert s.count(old) == 1
s = s.replace(old, old + '\n    cdebconf_gtk_nex_set_waiting(fe, TRUE);')
p.write_text(s)
# Preserve the native successful finish-install GO/reboot gate and handlers.
p = p.with_name('go.c')
s = p.read_text()
old = '    label = cdebconf_gtk_get_text(fe, "debconf/button-continue", "Continue");'
assert s.count(old) == 1
s = s.replace(old, '''    label = fe->questions && fe->questions->tag &&
        !strcmp(fe->questions->tag, "finish-install/reboot_in_progress")
        ? g_strdup("Restart now")
        : cdebconf_gtk_get_text(fe, "debconf/button-continue", "Continue");''')
old = '    if (!is_action_box_filled(fe)) {'
assert s.count(old) == 1
s = s.replace(old, '    cdebconf_gtk_nex_failure(fe, question_box);\n' + old)
p.write_text(s)
p = p.with_name('handlers.c')
s = p.read_text()
old = "    /* Let's use an empty container... */"
assert s.count(old) == 1
s = s.replace(old, '''    if (question->tag &&
        !strcmp(question->tag, "finish-install/reboot_in_progress")) {
        cdebconf_gtk_nex_completion(question_box);
        return DC_OK;
    }
''' + old)
p.write_text(s)
PY
(cd "$source" && autoreconf -fi && ./configure --prefix=/usr --libexecdir=/usr/lib \
    --enable-d-i --without-rpath --with-db='rfc822db stack' --with-frontend=gtk \
    --with-default-frontend=gtk --with-syslog-logging --disable-selinux \
    --with-conffile=/etc/cdebconf.conf && make -C src/modules/frontend/gtk)
root=$cache/initrd
rm -rf "$root"
mkdir -p "$root"
(cd "$root" && gzip -dc "$stage/install.amd/gtk/initrd.gz" | cpio -idm --quiet --no-absolute-filenames)
python3 - "$root" <<'PY'
from pathlib import Path
import sys
root = Path(sys.argv[1])
status = (root / 'var/lib/dpkg/status').read_text()
package = next(p for p in status.split('\n\n') if p.startswith('Package: cdebconf-gtk-udeb\n'))
assert '\nVersion: 0.280\n' in package + '\n', 'GTK frontend ABI changed'
assert (root / 'usr/lib/cdebconf/frontend/gtk.so').is_file()
PY
# Add the Nex wizard before the native installation engine starts.
overlay=$cache/overlay
rm -rf "$overlay"
mkdir -p "$overlay/usr/lib/cdebconf/frontend" "$overlay/usr/share/nex-installer" \
    "$overlay/usr/bin" "$overlay/usr/lib/nex-installer" "$overlay/usr/lib/debian-installer.d" "$overlay/usr/lib/partman/init.d"
cc -O2 -Wall -Wextra -Wno-deprecated-declarations $(pkg-config --cflags gtk+-2.0) \
    installer/gui/nex-wizard.c installer/gui/nex-network.c -o "$overlay/usr/bin/nex-wizard" $(pkg-config --libs gtk+-2.0)
cc -O2 -Wall -Wextra $(pkg-config --cflags glib-2.0) \
    installer/gui/nex-config.c installer/gui/nex-network.c -o "$overlay/usr/bin/nex-config" $(pkg-config --libs glib-2.0) -lcrypt
ln -s /usr/bin/nex-config "$overlay/usr/lib/nex-installer/validate-config"
install -m 755 installer/gui/recheck.sh "$overlay/usr/lib/partman/init.d/00nex-recheck"
install -m 755 installer/gui/prepare.sh "$overlay/usr/lib/nex-installer/prepare"
install -m 755 installer/gui/dhcp-preview.sh "$overlay/usr/lib/nex-installer/dhcp-preview"
install -m 644 installer/gui/start.sh "$overlay/usr/lib/debian-installer.d/S66nex-wizard"
# Load safe package/account defaults before the GUI; no file-preseed screen.
sed '/^d-i preseed\/early_command /d' installer/preseed.cfg > "$overlay/preseed.cfg"
{ echo UTC; awk '!/^#/ {print $3}' /usr/share/zoneinfo/zone.tab; } | sort -u > "$overlay/usr/share/nex-installer/timezones"
install -m 644 installer/assets/nex-core-lockup.png "$overlay/usr/share/nex-installer/nex-core-lockup.png"
install -m 644 "$source/src/modules/frontend/gtk/gtk.so" "$overlay/usr/lib/cdebconf/frontend/gtk.so"
(cd "$overlay" && find . -print0 | cpio --null -o -H newc --quiet | gzip -n) >> "$stage/install.amd/gtk/initrd.gz"
# Retain exact upstream and modified build source, not just a Nex-only patch.
mkdir -p "$stage/nex/docs"
cp "$source/debian/copyright" "$stage/nex/docs/cdebconf-copyright"
tar --sort=name --owner=0 --group=0 --numeric-owner \
    --exclude='*.opic' --exclude='*.so' -czf "$stage/nex/nex-gui-source.tar.gz" \
    -C "$cache" cdebconf cdebconf_0.280.dsc cdebconf_0.280.tar.xz
(cd "$stage/nex" && sha256sum nex-gui-source.tar.gz > nex-gui-source.tar.gz.sha256)
printf '%s\n' 'Nex custom GTK installer shell; cdebconf 0.280 ABI; original Nex additions AGPL-3.0-only.' \
    'Upstream cdebconf BSD-2-Clause; dependency notices and source obligations remain applicable.' \
    > "$stage/nex/docs/gui-build.txt"
