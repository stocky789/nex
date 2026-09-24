/* SPDX-License-Identifier: AGPL-3.0-only */
#include <gtk/gtk.h>
#include <gdk/gdkkeysyms.h>
#include <glib/gstdio.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include <fcntl.h>
#include <unistd.h>
#include <string.h>
#include <stdlib.h>
#include <signal.h>
#include <arpa/inet.h>
#include "nex-network.h"

static GtkWidget *window, *book, *back, *next, *error_label, *heading, *steps;
static GtkWidget *step_labels[5];
static GtkWidget *host, *network_hostname, *password, *repeat, *zone_entry, *keymap, *show_password, *timezone_area;
static gchar **timezones;
static GtkWidget *nic_list, *nic_details, *mode, *address, *gateway, *vlan, *management, *dns;
static GtkWidget *disk_list, *disk_summary, *review, *confirmation, *dhcp_retry;
static gboolean loading_nic;
static guint dhcp_debounce, dhcp_children;
static gint dhcp_link = -1;
typedef struct { GPid pid; gint output; guint timeout; gboolean expired; } DHCPPreview;
static DHCPPreview *dhcp_preview;
static GKeyFile *config;
static GPtrArray *nics, *disks;
static GArray *disk_sizes;
static gint selected_nic = -1, page;
static guint selected_disks;
static gboolean prepared, preparing, keyboard_ready = TRUE;
static const gchar *titles[] = { "Welcome to Nex Core", "Name this host", "Connect your host", "Choose the installation disks", "Review and install" };
static const gchar *modes[] = { "disabled", "dhcp", "static" };
static const gchar *keymaps[] = { "us", "gb", "de", "fr", "es" };
static const gchar *keyboard_names[] = { "English (United States)", "English (United Kingdom)", "German", "French", "Spanish" };

static GtkWidget *label(const gchar *text)
{
    GtkWidget *w = gtk_label_new(text);
    gtk_misc_set_alignment(GTK_MISC(w), 0, 0.5);
    gtk_label_set_line_wrap(GTK_LABEL(w), TRUE);
    gtk_label_set_max_width_chars(GTK_LABEL(w), 60);
    return w;
}
static void message(const gchar *text) { gtk_label_set_text(GTK_LABEL(error_label), text ? text : ""); gtk_widget_set_tooltip_text(error_label, text && *text ? text : NULL); }
static GtkWidget *entry(void) { GtkWidget *w = gtk_entry_new(); gtk_entry_set_max_length(GTK_ENTRY(w), 255); return w; }
static const gchar *value(GtkWidget *w) { return gtk_entry_get_text(GTK_ENTRY(w)); }
static void field(GtkWidget *box, const gchar *name, GtkWidget *control)
{
    GtkWidget *row = gtk_hbox_new(FALSE, 12), *l = gtk_label_new_with_mnemonic(name);
    gtk_misc_set_alignment(GTK_MISC(l), 0, 0.5);
    gtk_widget_set_size_request(l, 125, -1);
    gtk_label_set_mnemonic_widget(GTK_LABEL(l), GTK_IS_ENTRY(control) || GTK_IS_COMBO_BOX(control) || GTK_IS_SPIN_BUTTON(control) ? control : address);
    gtk_box_pack_start(GTK_BOX(row), l, FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(row), control, TRUE, TRUE, 0);
    gtk_box_pack_start(GTK_BOX(box), row, FALSE, FALSE, 3);
}
static gchar *read_sys(const gchar *path)
{
    gchar *s = NULL;
    if (!g_file_get_contents(path, &s, NULL, NULL)) return g_strdup("");
    return g_strstrip(s);
}
static gchar *property(const gchar *base, const gchar *name, const gchar *prop)
{
    gchar *path = g_build_filename(base, name, prop, NULL), *s = read_sys(path);
    g_free(path); return s;
}
static gchar *disk_serial(const gchar *name)
{
    gchar *block = g_build_filename("/sys/class/block", name, NULL), *serial = nex_install_disk_serial(block);
    g_free(block); return serial;
}
static gchar *nic_group(gint index) { return g_strdup_printf("NIC:%s", (gchar *)g_ptr_array_index(nics, index)); }
static void save_nic(void)
{
    gchar *group;
    gint m;
    if (selected_nic < 0) return;
    group = nic_group(selected_nic);
    m = gtk_combo_box_get_active(GTK_COMBO_BOX(mode));
    g_key_file_set_string(config, group, "mode", modes[MAX(m, 0)]);
    g_key_file_set_string(config, group, "address", m == 2 ? value(address) : "");
    g_key_file_set_string(config, group, "gateway", m == 2 ? value(gateway) : "");
    g_key_file_set_integer(config, group, "vlan", m > 0 ? gtk_spin_button_get_value_as_int(GTK_SPIN_BUTTON(vlan)) : 0);
    g_free(group);
}
static void load_entry(GtkWidget *w, const gchar *group, const gchar *key)
{
    gchar *s = g_key_file_get_string(config, group, key, NULL);
    gtk_entry_set_text(GTK_ENTRY(w), s ? s : ""); g_free(s);
}
/* Preview only: the helper never applies an address, route or resolver setting. */
static gboolean dhcp_kill(gpointer data)
{
    DHCPPreview *request = data;
    request->timeout = 0;
    kill(request->pid, SIGKILL);
    return FALSE;
}
static gboolean dhcp_timeout(gpointer data)
{
    DHCPPreview *request = data;
    request->expired = TRUE;
    kill(request->pid, SIGTERM);
    request->timeout = g_timeout_add_seconds(2, dhcp_kill, request);
    return FALSE;
}
static void dhcp_cancel(void)
{
    if (dhcp_debounce) { g_source_remove(dhcp_debounce); dhcp_debounce = 0; }
    if (dhcp_preview) {
        if (dhcp_preview->timeout) g_source_remove(dhcp_preview->timeout);
        dhcp_timeout(dhcp_preview);
        dhcp_preview = NULL;
    }
}
static void dhcp_finished(GPid pid, gint status, gpointer data)
{
    DHCPPreview *request = data;
    gchar output[64] = {0};
    struct in_addr ip;
    ssize_t length = read(request->output, output, sizeof(output) - 1);
    if (request->timeout) g_source_remove(request->timeout);
    close(request->output); g_spawn_close_pid(pid); dhcp_children--;
    if (request == dhcp_preview) {
        gboolean valid = !request->expired && WIFEXITED(status) && !WEXITSTATUS(status) &&
            length > 0 && length < (ssize_t)sizeof(output) - 1 &&
            strlen(output) == (gsize)length && inet_pton(AF_INET, g_strstrip(output), &ip) == 1 &&
            ntohl(ip.s_addr) != 0 && ntohl(ip.s_addr) < 0xe0000000 &&
            (ntohl(ip.s_addr) >> 24) != 127;
        gchar *text = valid ? g_strdup_printf("DHCP address: %s", output) : g_strdup("No DHCP address received");
        gtk_entry_set_text(GTK_ENTRY(address), text); g_free(text);
        gtk_widget_show(dhcp_retry); gtk_widget_set_sensitive(dhcp_retry, TRUE);
        dhcp_preview = NULL;
    }
    g_free(request);
}
static gboolean dhcp_start(gpointer unused)
{
    gchar *name, *carrier, *tag;
    gchar *argv[4];
    DHCPPreview *request;
    (void)unused;
    dhcp_debounce = 0;
    if (page != 2 || loading_nic || selected_nic < 0 || gtk_combo_box_get_active(GTK_COMBO_BOX(mode)) != 1) return FALSE;
    name = g_ptr_array_index(nics, selected_nic);
    carrier = property("/sys/class/net", name, "carrier");
    dhcp_link = !strcmp(carrier, "0") ? 0 : !strcmp(carrier, "1") ? 1 : -1;
    g_free(carrier);
    if (dhcp_link == 0) {
        gtk_entry_set_text(GTK_ENTRY(address), "No link — connect this adapter");
        gtk_widget_show(dhcp_retry); gtk_widget_set_sensitive(dhcp_retry, TRUE); return FALSE;
    }
    tag = g_strdup_printf("%d", gtk_spin_button_get_value_as_int(GTK_SPIN_BUTTON(vlan)));
    argv[0] = "/usr/lib/nex-installer/dhcp-preview"; argv[1] = name; argv[2] = tag; argv[3] = NULL;
    request = g_new0(DHCPPreview, 1);
    gtk_entry_set_text(GTK_ENTRY(address), "Looking for an IP address…");
    gtk_widget_show(dhcp_retry); gtk_widget_set_sensitive(dhcp_retry, FALSE);
    if (g_spawn_async_with_pipes(NULL, argv, NULL, G_SPAWN_DO_NOT_REAP_CHILD | G_SPAWN_STDERR_TO_DEV_NULL,
        NULL, NULL, &request->pid, NULL, &request->output, NULL, NULL)) {
        fcntl(request->output, F_SETFL, O_NONBLOCK);
        dhcp_preview = request; dhcp_children++;
        request->timeout = g_timeout_add_seconds(30, dhcp_timeout, request);
        g_child_watch_add(request->pid, dhcp_finished, request);
    } else {
        gtk_entry_set_text(GTK_ENTRY(address), "DHCP check unavailable"); gtk_widget_show(dhcp_retry); gtk_widget_set_sensitive(dhcp_retry, TRUE); g_free(request);
    }
    g_free(tag); return FALSE;
}
static void dhcp_changed(gpointer widget, gpointer unused)
{
    (void)widget; (void)unused;
    if (loading_nic) return;
    dhcp_cancel();
    gtk_widget_hide(dhcp_retry);
    if (page == 2 && selected_nic >= 0 && gtk_combo_box_get_active(GTK_COMBO_BOX(mode)) == 1) {
        gtk_entry_set_text(GTK_ENTRY(address), "Looking for an IP address…");
        gtk_widget_show(dhcp_retry); gtk_widget_set_sensitive(dhcp_retry, FALSE);
        dhcp_debounce = g_timeout_add(350, dhcp_start, NULL);
    }
}
static void mode_changed(GtkComboBox *box, gpointer unused)
{
    gint m = gtk_combo_box_get_active(box);
    (void)unused;
    gtk_widget_set_sensitive(address, m != 0);
    gtk_editable_set_editable(GTK_EDITABLE(address), m == 2);
    gtk_widget_set_sensitive(gateway, m == 2);
    gtk_widget_set_sensitive(vlan, m != 0);
    gtk_entry_set_text(GTK_ENTRY(address), ""); gtk_entry_set_text(GTK_ENTRY(gateway), "");
    if (m == 0) gtk_spin_button_set_value(GTK_SPIN_BUTTON(vlan), 0);
    dhcp_changed(NULL, NULL);
}
/* Carrier describes the cable/virtual link; operstate also reflects configuration. */
static const gchar *nic_link_status(const gchar *flags, const gchar *carrier)
{
    gchar *end;
    guint64 bits = g_ascii_strtoull(flags, &end, 0);
    if (!*flags || *end || bits > G_MAXUINT || !(bits & 1)) return "Link unknown";
    if (!strcmp(carrier, "1")) return "Link detected";
    if (!strcmp(carrier, "0")) return "No link";
    return "Link unknown";
}
static gboolean refresh_nic_details(gpointer unused)
{
    gchar *name, *mac, *flags, *carrier, *speed, *driver_path, *driver, *detail, *rate, *end;
    const gchar *status;
    guint64 mbps;
    (void)unused;
    if (page != 2 || selected_nic < 0) return TRUE;
    name = g_ptr_array_index(nics, selected_nic);
    mac = property("/sys/class/net", name, "address");
    flags = property("/sys/class/net", name, "flags");
    carrier = property("/sys/class/net", name, "carrier");
    speed = property("/sys/class/net", name, "speed");
    status = nic_link_status(flags, carrier);
    if (!loading_nic && gtk_combo_box_get_active(GTK_COMBO_BOX(mode)) == 1) {
        gint link = !strcmp(carrier, "0") ? 0 : !strcmp(carrier, "1") ? 1 : -1;
        if (link != dhcp_link) { dhcp_link = link; dhcp_changed(NULL, NULL); }
    }
    mbps = g_ascii_strtoull(speed, &end, 10);
    rate = !strcmp(status, "Link detected") && *speed && !*end && mbps > 0 && mbps <= G_MAXUINT
        ? g_strdup_printf("  •  %" G_GUINT64_FORMAT " Mbps", mbps) : g_strdup("");
    driver_path = g_strdup_printf("/sys/class/net/%s/device/driver", name);
    driver = g_file_read_link(driver_path, NULL);
    detail = g_markup_printf_escaped("%s  <span foreground=\"%s\">●</span>  %s%s\nMAC %s\n%s", name,
        !strcmp(status, "Link detected") ? "#21824A" : "#65738A", status, rate, *mac ? mac : "unavailable",
        driver ? (strrchr(driver, '/') ? strrchr(driver, '/') + 1 : driver) : "Driver unavailable");
    gtk_label_set_markup(GTK_LABEL(nic_details), detail);
    g_free(detail); g_free(driver); g_free(driver_path); g_free(rate);
    g_free(mac); g_free(flags); g_free(carrier); g_free(speed);
    return TRUE;
}
static void nic_changed(GtkTreeSelection *selection, gpointer unused)
{
    GtkTreeModel *model; GtkTreeIter iter; gchar *group, *s;
    gint index, m = 0;
    (void)unused;
    if (!gtk_tree_selection_get_selected(selection, &model, &iter)) return;
    save_nic(); dhcp_cancel(); loading_nic = TRUE; gtk_tree_model_get(model, &iter, 1, &index, -1); selected_nic = index;
    group = nic_group(index);
    s = g_key_file_get_string(config, group, "mode", NULL);
    if (s && !strcmp(s, "dhcp")) m = 1; else if (s && !strcmp(s, "static")) m = 2;
    gtk_combo_box_set_active(GTK_COMBO_BOX(mode), m); g_free(s);
    load_entry(address, group, "address"); load_entry(gateway, group, "gateway");
    gtk_spin_button_set_value(GTK_SPIN_BUTTON(vlan), g_key_file_get_integer(config, group, "vlan", NULL));
    g_free(group); loading_nic = FALSE; refresh_nic_details(NULL); dhcp_changed(NULL, NULL);
}
static GtkWidget *list_widget(void)
{
    GtkListStore *store = gtk_list_store_new(2, G_TYPE_STRING, G_TYPE_INT);
    GtkWidget *w = gtk_tree_view_new_with_model(GTK_TREE_MODEL(store));
    GtkCellRenderer *renderer = gtk_cell_renderer_text_new();
    gtk_tree_view_insert_column_with_attributes(GTK_TREE_VIEW(w), -1, "Device", renderer, "text", 0, NULL);
    gtk_tree_view_set_headers_visible(GTK_TREE_VIEW(w), FALSE);
    g_object_unref(store); return w;
}
static void append_device(GtkWidget *list, const gchar *text, gint index)
{
    GtkListStore *store = GTK_LIST_STORE(gtk_tree_view_get_model(GTK_TREE_VIEW(list))); GtkTreeIter iter;
    gtk_list_store_append(store, &iter); gtk_list_store_set(store, &iter, 0, text, 1, index, -1);
}
/* The Disk step is a tick list: one disk installs as it always has, two or more become a
 * mirror. Selection lives in the model's toggle column, not in the row highlight. */
enum { DISK_SELECTED, DISK_NAME, DISK_CAPACITY, DISK_TYPE, DISK_MODEL, DISK_SERIAL, DISK_CONTENTS, DISK_INDEX, DISK_COLUMNS };

/* SSD, HDD or NVMe, so the operator can tell the disks apart at a glance. */
static const gchar *disk_kind(const gchar *name)
{
    gchar *rotational;
    const gchar *kind;
    if (g_str_has_prefix(name, "nvme")) return "NVMe";
    rotational = property("/sys/class/block", name, "queue/rotational");
    kind = !strcmp(rotational, "0") ? "SSD" : !strcmp(rotational, "1") ? "HDD" : "\342\200\224";
    g_free(rotational); return kind;
}
/* A disk that already holds partitions is one to look at twice before erasing it. */
static gboolean has_partitions(const gchar *name)
{
    gchar *base = g_strdup_printf("/sys/class/block/%s", name);
    GDir *dir = g_dir_open(base, 0, NULL);
    const gchar *entry;
    gboolean found = FALSE;
    while (dir && (entry = g_dir_read_name(dir))) {
        gchar *path = g_build_filename(base, entry, "partition", NULL);
        found |= g_file_test(path, G_FILE_TEST_EXISTS);
        g_free(path);
    }
    if (dir) g_dir_close(dir);
    g_free(base); return found;
}
/* Two listed disks close enough in size to mirror: what the tip offers. */
static gboolean similar_disks(void)
{
    guint i, j;
    for (i = 0; i < disk_sizes->len; i++)
        for (j = i + 1; j < disk_sizes->len; j++) {
            guint64 a = g_array_index(disk_sizes, guint64, i), b = g_array_index(disk_sizes, guint64, j);
            guint64 small = MIN(a, b), large = MAX(a, b);
            if (small >= 16ULL * 1024 * 1024 * 1024 && large <= small + small / 10) return TRUE;
        }
    return FALSE;
}
/* Live Disk summary: plain outcome of the selection (RAID1 only for 2+ disks). */
static gchar *disk_summary_text(guint selected, guint listed, guint64 smallest, guint64 largest, gboolean has_similar)
{
    if (selected && largest > smallest + smallest / 10)
        return g_strdup("<span foreground='#B3261E'>Every mirror disk must be within 10% of the smallest selected disk's capacity, because a mirror is only as large as its smallest member. Select disks of similar size.</span>");
    if (!selected)
        return g_strdup(listed > 1 && has_similar
            ? "No disk selected.\nTip: two or more similar-sized disks give RAID1 copies of one system disk — more disks mean more copies, not more space."
            : "No disk selected.");
    if (selected == 1)
        return g_strdup("What you get: one system disk, no redundancy.");
    /* RAID1 only: every selected disk is another full copy. Usable size is the smallest. */
    return g_markup_printf_escaped(
        "What you get: one %.1f GiB system disk on all %u drives (RAID1 copies — not pooled storage). Survives %u disk failure%s.",
        smallest / (1024.0 * 1024 * 1024), selected, selected - 1,
        (selected - 1) == 1 ? "" : "s");
}
static void disks_changed(void)
{
    GtkTreeModel *model = gtk_tree_view_get_model(GTK_TREE_VIEW(disk_list));
    GtkTreeIter iter;
    GPtrArray *names = g_ptr_array_new_with_free_func(g_free), *sizes = g_ptr_array_new_with_free_func(g_free);
    GPtrArray *serials = g_ptr_array_new_with_free_func(g_free), *ids = g_ptr_array_new_with_free_func(g_free);
    guint64 smallest = 0, largest = 0;
    gboolean valid;
    gchar *summary;
    for (valid = gtk_tree_model_get_iter_first(model, &iter); valid; valid = gtk_tree_model_iter_next(model, &iter)) {
        gboolean selected; gint index; gchar *name, *s; guint64 bytes;
        gtk_tree_model_get(model, &iter, DISK_SELECTED, &selected, DISK_INDEX, &index, -1);
        if (!selected) continue;
        name = g_ptr_array_index(disks, index);
        bytes = g_array_index(disk_sizes, guint64, index);
        if (!smallest || bytes < smallest) smallest = bytes;
        if (bytes > largest) largest = bytes;
        g_ptr_array_add(names, g_strdup_printf("/dev/%s", name));
        g_ptr_array_add(sizes, g_strdup_printf("%" G_GUINT64_FORMAT, bytes));
        g_ptr_array_add(serials, disk_serial(name));
        s = property("/sys/class/block", name, "dev"); g_ptr_array_add(ids, s);
    }
    selected_disks = names->len;
    g_key_file_set_string_list(config, "Host", "disk", (const gchar **)names->pdata, names->len);
    g_key_file_set_string_list(config, "Host", "disk_size", (const gchar **)sizes->pdata, sizes->len);
    g_key_file_set_string_list(config, "Host", "disk_serial", (const gchar **)serials->pdata, serials->len);
    g_key_file_set_string_list(config, "Host", "disk_major_minor", (const gchar **)ids->pdata, ids->len);
    g_ptr_array_free(names, TRUE); g_ptr_array_free(sizes, TRUE); g_ptr_array_free(serials, TRUE); g_ptr_array_free(ids, TRUE);
    summary = disk_summary_text(selected_disks, disks->len, smallest, largest, similar_disks());
    gtk_label_set_markup(GTK_LABEL(disk_summary), summary);
    g_free(summary);
    gtk_toggle_button_set_active(GTK_TOGGLE_BUTTON(confirmation), FALSE);
}
static void disk_toggle_row(GtkTreeIter *iter)
{
    GtkTreeModel *model = gtk_tree_view_get_model(GTK_TREE_VIEW(disk_list));
    gboolean selected;
    gtk_tree_model_get(model, iter, DISK_SELECTED, &selected, -1);
    gtk_list_store_set(GTK_LIST_STORE(model), iter, DISK_SELECTED, !selected, -1);
    disks_changed();
}
static void disk_toggled(GtkCellRendererToggle *cell, gchar *path, gpointer unused)
{
    GtkTreeIter iter;
    (void)cell; (void)unused;
    if (gtk_tree_model_get_iter_from_string(gtk_tree_view_get_model(GTK_TREE_VIEW(disk_list)), &iter, path))
        disk_toggle_row(&iter);
}
/* Space and Enter tick the focused row wherever the cursor column sits. */
static gboolean disk_key(GtkWidget *widget, GdkEventKey *event, gpointer unused)
{
    GtkTreePath *path = NULL; GtkTreeIter iter;
    (void)unused;
    if (event->keyval != GDK_space && event->keyval != GDK_Return && event->keyval != GDK_KP_Enter) return FALSE;
    gtk_tree_view_get_cursor(GTK_TREE_VIEW(widget), &path, NULL);
    if (!path) return FALSE;
    if (gtk_tree_model_get_iter(gtk_tree_view_get_model(GTK_TREE_VIEW(widget)), &iter, path)) disk_toggle_row(&iter);
    gtk_tree_path_free(path);
    return TRUE;
}
/* Wide Model/Serial columns must ellipsize; otherwise the Disk page forces a
 * horizontal scrollbar and clips the summary on BMC/IPMI consoles (800×600 /
 * 1024×768). */
static void disk_text_column(GtkTreeView *view, const gchar *title, gint col)
{
    GtkCellRenderer *cell = gtk_cell_renderer_text_new();
    GtkTreeViewColumn *column;
    g_object_set(cell, "ellipsize", PANGO_ELLIPSIZE_END, NULL);
    column = gtk_tree_view_column_new_with_attributes(title, cell, "text", col, NULL);
    gtk_tree_view_column_set_resizable(column, TRUE);
    gtk_tree_view_column_set_sizing(column, GTK_TREE_VIEW_COLUMN_FIXED);
    gtk_tree_view_append_column(view, column);
}
/* Share the list width by weight. GTK2 expand columns can never be dragged
 * narrower (their slice of the slack is re-added on top of the dragged width),
 * so the text columns get fixed widths instead; dragging a divider then works. */
static void disk_list_allocate(GtkWidget *view, GtkAllocation *allocation, gpointer unused)
{
    static const gint percent[] = { 13, 12, 8, 25, 22, 20 }; /* Device, Capacity, Type, Model, Serial, Contents */
    static gint width;
    GList *columns, *l; gint text; guint i;
    (void)unused;
    if (allocation->width <= 1 || allocation->width == width) return;
    width = allocation->width;
    columns = gtk_tree_view_get_columns(GTK_TREE_VIEW(view));
    text = width - gtk_tree_view_column_get_fixed_width(columns->data);
    for (l = columns->next, i = 0; i < G_N_ELEMENTS(percent); l = l->next, i++)
        gtk_tree_view_column_set_fixed_width(l->data, text * percent[i] / 100);
    g_list_free(columns);
}
static GtkWidget *disk_list_widget(void)
{
    GtkListStore *store = gtk_list_store_new(DISK_COLUMNS, G_TYPE_BOOLEAN, G_TYPE_STRING, G_TYPE_STRING,
        G_TYPE_STRING, G_TYPE_STRING, G_TYPE_STRING, G_TYPE_STRING, G_TYPE_INT);
    GtkWidget *w = gtk_tree_view_new_with_model(GTK_TREE_MODEL(store));
    GtkCellRenderer *toggle = gtk_cell_renderer_toggle_new();
    GtkTreeViewColumn *use;
    g_signal_connect(toggle, "toggled", G_CALLBACK(disk_toggled), NULL);
    use = gtk_tree_view_column_new_with_attributes("Use", toggle, "active", DISK_SELECTED, NULL);
    gtk_tree_view_column_set_sizing(use, GTK_TREE_VIEW_COLUMN_FIXED);
    gtk_tree_view_column_set_fixed_width(use, 40);
    gtk_tree_view_append_column(GTK_TREE_VIEW(w), use);
    disk_text_column(GTK_TREE_VIEW(w), "Device", DISK_NAME);
    disk_text_column(GTK_TREE_VIEW(w), "Capacity", DISK_CAPACITY);
    disk_text_column(GTK_TREE_VIEW(w), "Type", DISK_TYPE);
    disk_text_column(GTK_TREE_VIEW(w), "Model", DISK_MODEL);
    disk_text_column(GTK_TREE_VIEW(w), "Serial", DISK_SERIAL);
    disk_text_column(GTK_TREE_VIEW(w), "Contents", DISK_CONTENTS);
    gtk_tree_view_set_headers_clickable(GTK_TREE_VIEW(w), FALSE);
    g_signal_connect(w, "size-allocate", G_CALLBACK(disk_list_allocate), NULL);
    g_signal_connect(w, "key-press-event", G_CALLBACK(disk_key), NULL);
    g_object_unref(store); return w;
}
/* GtkLabel wrap ignores the parent width unless we pin the request on allocate. */
static void wrap_label_allocate(GtkWidget *widget, GtkAllocation *allocation, gpointer unused)
{
    gint previous;
    (void)unused;
    if (allocation->width <= 1) return;
    previous = GPOINTER_TO_INT(g_object_get_data(G_OBJECT(widget), "nex-wrap-width"));
    if (previous == allocation->width) return;
    g_object_set_data(G_OBJECT(widget), "nex-wrap-width", GINT_TO_POINTER(allocation->width));
    gtk_widget_set_size_request(widget, allocation->width, -1);
}
static gboolean installation_media(const gchar *name)
{
    gchar *mounts = read_sys("/proc/mounts"), **lines = g_strsplit(mounts, "\n", -1);
    gboolean found = FALSE;
    for (guint i = 0; lines[i]; i++) {
        gchar **fields = g_strsplit(lines[i], " ", 4);
        if (g_strv_length(fields) >= 2 && !strcmp(fields[1], "/cdrom")) {
            gchar *device = realpath(fields[0], NULL);
            if (device) {
                gchar *base = g_path_get_basename(device), *sys = g_strdup_printf("/sys/class/block/%s", base), *resolved = realpath(sys, NULL);
                found = !strcmp(base, name);
                if (resolved) { gchar *parent = g_path_get_dirname(resolved), *parent_name = g_path_get_basename(parent); found |= !strcmp(parent_name, name); g_free(parent); g_free(parent_name); free(resolved); }
                g_free(sys); g_free(base); free(device);
            }
        }
        g_strfreev(fields);
    }
    g_strfreev(lines); g_free(mounts); return found;
}
static gint compare_names(gconstpointer a, gconstpointer b) { return strcmp(*(const gchar **)a, *(const gchar **)b); }
static void discover(void)
{
    GDir *dir; const gchar *name; gchar *path, *s, *group, *size, *model, *text;
    GPtrArray *names = g_ptr_array_new_with_free_func(g_free); guint i;
    /* names is reused for the block devices below. */
    dir = g_dir_open("/sys/class/net", 0, NULL);
    while (dir && (name = g_dir_read_name(dir))) g_ptr_array_add(names, g_strdup(name));
    if (dir) g_dir_close(dir);
    g_ptr_array_sort(names, compare_names); /* same order as the text wizard's sorted glob, so nex0..nexN match in both frontends */
    for (i = 0; i < names->len; i++) {
        name = g_ptr_array_index(names, i);
        path = g_strdup_printf("/sys/class/net/%s/device", name);
        if (!g_file_test(path, G_FILE_TEST_EXISTS)) { g_free(path); continue; } g_free(path);
        g_ptr_array_add(nics, g_strdup(name)); append_device(nic_list, name, nics->len - 1);
        gtk_combo_box_append_text(GTK_COMBO_BOX(management), name);
        group = nic_group(nics->len - 1); s = property("/sys/class/net", name, "address");
        g_key_file_set_string(config, group, "mac", s); g_key_file_set_string(config, group, "mode", "disabled");
        g_key_file_set_integer(config, group, "vlan", 0);
        g_key_file_set_string(config, group, "address", ""); g_key_file_set_string(config, group, "gateway", "");
        g_free(group); g_free(s);
    }
    g_ptr_array_free(names, TRUE);
    names = g_ptr_array_new_with_free_func(g_free);
    dir = g_dir_open("/sys/class/block", 0, NULL);
    while (dir && (name = g_dir_read_name(dir))) g_ptr_array_add(names, g_strdup(name));
    if (dir) g_dir_close(dir);
    g_ptr_array_sort(names, compare_names); /* the text frontend's glob is sorted; show the same order */
    for (i = 0; i < names->len; i++) {
        GtkTreeIter iter; gboolean skip; guint64 bytes; gchar *serial, *capacity;
        name = g_ptr_array_index(names, i);
        if (!nex_install_disk_name_supported(name)) continue;
        path = g_strdup_printf("/sys/class/block/%s/partition", name); skip = g_file_test(path, G_FILE_TEST_EXISTS); g_free(path);
        s = property("/sys/class/block", name, "ro"); skip |= strcmp(s, "0") != 0; g_free(s);
        s = property("/sys/class/block", name, "removable"); skip |= strcmp(s, "0") != 0; g_free(s);
        if (skip || installation_media(name)) continue;
        size = property("/sys/class/block", name, "size");
        bytes = g_ascii_strtoull(size, NULL, 10) * 512; g_free(size);
        /* Same 16 GiB floor as the text frontend and nex-config: do not list undersized disks. */
        if (!bytes || bytes < 16ULL * 1024 * 1024 * 1024) continue;
        model = property("/sys/class/block", name, "device/model");
        serial = disk_serial(name);
        capacity = g_strdup_printf("%.1f GiB", bytes / (1024.0 * 1024 * 1024));
        text = g_strdup_printf("/dev/%s", name);
        g_ptr_array_add(disks, g_strdup(name));
        g_array_append_val(disk_sizes, bytes);
        gtk_list_store_append(GTK_LIST_STORE(gtk_tree_view_get_model(GTK_TREE_VIEW(disk_list))), &iter);
        gtk_list_store_set(GTK_LIST_STORE(gtk_tree_view_get_model(GTK_TREE_VIEW(disk_list))), &iter,
            DISK_SELECTED, FALSE, DISK_NAME, text, DISK_CAPACITY, capacity, DISK_TYPE, disk_kind(name),
            DISK_MODEL, *model ? model : "\342\200\224", DISK_SERIAL, *serial ? serial : "\342\200\224",
            DISK_CONTENTS, has_partitions(name) ? "Has partitions" : "Blank",
            DISK_INDEX, (gint)(disks->len - 1), -1);
        g_free(text); g_free(capacity); g_free(model); g_free(serial);
    }
    g_ptr_array_free(names, TRUE);
    /* One eligible disk is the only possible answer, so tick it; a mirror is never assumed. */
    if (disks->len == 1) {
        GtkTreeIter iter;
        if (gtk_tree_model_get_iter_first(gtk_tree_view_get_model(GTK_TREE_VIEW(disk_list)), &iter))
            gtk_list_store_set(GTK_LIST_STORE(gtk_tree_view_get_model(GTK_TREE_VIEW(disk_list))), &iter, DISK_SELECTED, TRUE, -1);
    }
    disks_changed();
    if (!nics->len) gtk_label_set_text(GTK_LABEL(nic_details), "No physical network adapters detected.\nYou can install offline and configure networking locally later.");
    else { GtkTreePath *first = gtk_tree_path_new_first(); gtk_tree_selection_select_path(gtk_tree_view_get_selection(GTK_TREE_VIEW(nic_list)), first); gtk_tree_path_free(first); }
    gtk_widget_set_sensitive(mode, nics->len > 0);
    gtk_widget_set_sensitive(vlan, FALSE);
}
static void prepare_done(GPid pid, gint status, gpointer unused)
{
    (void)unused; g_spawn_close_pid(pid); preparing = FALSE;
    prepared = WIFEXITED(status) && WEXITSTATUS(status) == 0;
    if (prepared) { gchar *detail = read_sys("/run/nex-installer/error"); discover(); message(detail); g_free(detail); gtk_button_set_label(GTK_BUTTON(next), "Begin setup"); }
    else { gchar *detail = read_sys("/run/nex-installer/error"); message(*detail ? detail : "Preparation failed. Retry to detect hardware again. Details are available in the installer log."); g_free(detail); gtk_button_set_label(GTK_BUTTON(next), "Retry preparation"); }
    gtk_widget_set_sensitive(next, TRUE);
}
static void prepare(void)
{
    gchar *argv[] = { "/usr/lib/nex-installer/prepare", NULL }; GError *error = NULL; GPid pid;
    preparing = TRUE; gtk_widget_set_sensitive(next, FALSE); gtk_button_set_label(GTK_BUTTON(next), "Preparing Nex Core…");
    if (!g_spawn_async(NULL, argv, NULL, G_SPAWN_DO_NOT_REAP_CHILD | G_SPAWN_STDOUT_TO_DEV_NULL | G_SPAWN_STDERR_TO_DEV_NULL, NULL, NULL, &pid, &error)) {
        preparing = FALSE; message(error->message); g_clear_error(&error); gtk_widget_set_sensitive(next, TRUE); gtk_button_set_label(GTK_BUTTON(next), "Retry preparation");
    } else g_child_watch_add(pid, prepare_done, NULL);
}
/* Debian Installer has no window manager to honor maximize requests. */
static void fit_screen(GdkScreen *screen, gpointer unused)
{
    gint width = gdk_screen_get_width(screen), height = gdk_screen_get_height(screen);
    (void)unused;
    gtk_window_move(GTK_WINDOW(window), 0, 0);
    gtk_window_resize(GTK_WINDOW(window), width, height);
    if (gtk_widget_get_window(window))
        gdk_window_move_resize(gtk_widget_get_window(window), 0, 0, width, height);
}
static void timezone_area_changed(GtkComboBox *box, gpointer unused)
{
    gchar *area = gtk_combo_box_get_active_text(box);
    (void)unused;
    gtk_list_store_clear(GTK_LIST_STORE(gtk_combo_box_get_model(GTK_COMBO_BOX(zone_entry))));
    if (area && !strcmp(area, "UTC")) gtk_combo_box_append_text(GTK_COMBO_BOX(zone_entry), "UTC");
    else if (area) {
        gchar *prefix = g_strconcat(area, "/", NULL);
        for (guint i = 0; timezones[i]; i++)
            if (g_str_has_prefix(timezones[i], prefix))
                gtk_combo_box_append_text(GTK_COMBO_BOX(zone_entry), timezones[i]);
        g_free(prefix);
    }
    gtk_combo_box_set_active(GTK_COMBO_BOX(zone_entry), 0);
    g_free(area);
}
static void timezone_choices(void)
{
    gchar *data = read_sys("/usr/share/nex-installer/timezones");
    GHashTable *areas = g_hash_table_new_full(g_str_hash, g_str_equal, g_free, NULL);
    timezones = g_strsplit(data, "\n", -1); g_free(data);
    gtk_combo_box_append_text(GTK_COMBO_BOX(timezone_area), "UTC");
    for (guint i = 0; timezones[i]; i++) {
        const gchar *slash = strchr(timezones[i], '/');
        if (slash) {
            gchar *area = g_strndup(timezones[i], slash - timezones[i]);
            if (!g_hash_table_contains(areas, area)) {
                gtk_combo_box_append_text(GTK_COMBO_BOX(timezone_area), area);
                g_hash_table_add(areas, area);
            } else g_free(area);
        }
    }
    g_hash_table_destroy(areas);
    g_signal_connect(timezone_area, "changed", G_CALLBACK(timezone_area_changed), NULL);
    gtk_combo_box_set_active(GTK_COMBO_BOX(timezone_area), 0);
}
static void password_visibility(GtkToggleButton *button, gpointer unused)
{
    gboolean visible = gtk_toggle_button_get_active(button);
    (void)unused;
    gtk_entry_set_visibility(GTK_ENTRY(password), visible);
    gtk_entry_set_visibility(GTK_ENTRY(repeat), visible);
}
static void keyboard_changed(GtkComboBox *box, gpointer unused)
{
    (void)unused;
    gint index=gtk_combo_box_get_active(box), status=0;
    gchar *args[]={"/usr/bin/setxkbmap", "-layout", (gchar *)keymaps[MAX(index,0)], NULL};
    keyboard_ready=g_spawn_sync(NULL,args,NULL,G_SPAWN_STDOUT_TO_DEV_NULL|G_SPAWN_STDERR_TO_DEV_NULL,NULL,NULL,NULL,NULL,&status,NULL) && WIFEXITED(status) && !WEXITSTATUS(status);
    gtk_entry_set_text(GTK_ENTRY(password),"");gtk_entry_set_text(GTK_ENTRY(repeat),"");
    message(keyboard_ready ? "" : "The selected keyboard layout could not be applied. Select a working layout before entering credentials.");
}
static gboolean collect(void)
{
    GError *error = NULL; gint index;
    save_nic();
    if (!keyboard_ready) { message("Select a working keyboard layout before continuing."); return FALSE; }
    gchar *normalized_hostname = nex_hostname_normalize(value(host));
    if (!normalized_hostname) { message("Use an ASCII host name or FQDN, e.g. nexcore.example.test."); gtk_widget_grab_focus(page == 2 ? network_hostname : host); return FALSE; }
    gtk_entry_set_text(GTK_ENTRY(host), normalized_hostname); g_free(normalized_hostname);
    {
        gchar *zone = gtk_combo_box_get_active_text(GTK_COMBO_BOX(zone_entry));
        if (!zone) { message("Select a timezone from the list."); gtk_widget_grab_focus(zone_entry); return FALSE; }
        g_key_file_set_string(config, "Host", "timezone", zone); g_free(zone);
    }
    if (g_utf8_strlen(value(password), -1) < 8 || strlen(value(password)) > 512 || strcmp(value(password), value(repeat))) { message("Use at least 8 characters and enter the same password twice."); gtk_widget_grab_focus(g_utf8_strlen(value(password), -1) < 8 ? password : repeat); return FALSE; }
    g_key_file_set_string(config, "Host", "hostname", value(host));
    g_key_file_set_string(config, "Host", "password", value(password));
    g_key_file_set_string(config, "Host", "keymap", keymaps[MAX(0, gtk_combo_box_get_active(GTK_COMBO_BOX(keymap)))]);
    index = gtk_combo_box_get_active(GTK_COMBO_BOX(management));
    g_key_file_set_string(config, "Network", "management", index > 0 ? g_ptr_array_index(nics, index - 1) : "");
    g_key_file_set_string(config, "Network", "dns", value(dns));
    if (page >= 2 && !nex_network_validate(config, &error)) { message(error ? error->message : "Check the network settings."); g_clear_error(&error); return FALSE; }
    if (page < 3) return TRUE;
    if (!selected_disks) { message("Select the disk to erase and install Nex Core onto, or two or more disks for a mirror."); return FALSE; }
    {
        gsize count; gchar **sizes = g_key_file_get_string_list(config, "Host", "disk_size", &count, NULL);
        guint64 smallest = 0; const gchar *reason = NULL;
        for (gsize i = 0; i < count; i++) {
            guint64 bytes = g_ascii_strtoull(sizes[i], NULL, 10);
            if (bytes < 16ULL * 1024 * 1024 * 1024) reason = "Every disk must be at least 16 GiB.";
            if (!smallest || bytes < smallest) smallest = bytes;
        }
        /* A mirror is only as large as its smallest member. */
        for (gsize i = 0; !reason && i < count; i++)
            if (g_ascii_strtoull(sizes[i], NULL, 10) > smallest + smallest / 10)
                reason = "Every mirror disk must be within 10% of the smallest selected disk's capacity, because a mirror is only as large as its smallest member. Select disks of similar size.";
        g_strfreev(sizes);
        if (reason) { message(reason); return FALSE; }
    }
    return TRUE;
}
static void show_review(void)
{
    GString *s = g_string_new(NULL); guint i; gchar *group, *m, *a, *g, *management_name, *zone, *erase_label, *targets, *layout, **selection, **sizes;
    gsize count; guint64 capacity = 0;
    zone = g_key_file_get_string(config, "Host", "timezone", NULL);
    management_name = g_key_file_get_string(config, "Network", "management", NULL);
    g_string_append_printf(s, "HOST\n%s   •   %s   •   Keyboard %s\nRoot password: set\n\nNETWORK\nManagement: %s\nDNS: %s\n", value(host), zone, keymaps[gtk_combo_box_get_active(GTK_COMBO_BOX(keymap))], management_name && *management_name ? management_name : "Offline", *value(dns) ? value(dns) : "Automatic / none");
    g_free(zone);
    for (i = 0; i < nics->len; i++) {
        group = nic_group(i); m = g_key_file_get_string(config, group, "mode", NULL); a = g_key_file_get_string(config, group, "address", NULL); g = g_key_file_get_string(config, group, "gateway", NULL);
        if (!g_strcmp0(management_name, g_ptr_array_index(nics, i))) {
            int tag = g_key_file_get_integer(config, group, "vlan", NULL);
            g_string_append_printf(s, "%s → br0 (via nex%u", (gchar *)g_ptr_array_index(nics, i), i);
            if (tag) g_string_append_printf(s, ".%d", tag);
            g_string_append_printf(s, "): %s", m);
        } else g_string_append_printf(s, "%s → nex%u: %s", (gchar *)g_ptr_array_index(nics, i), i, m);
        if (!strcmp(m, "static")) g_string_append_printf(s, "   %s   gateway %s", a ? a : "", g && *g ? g : "none");
        if (g_key_file_get_integer(config, group, "vlan", NULL)) g_string_append_printf(s, "   VLAN %d", g_key_file_get_integer(config, group, "vlan", NULL));
        g_string_append_c(s, '\n'); g_free(group); g_free(m); g_free(a); g_free(g);
    }
    g_free(management_name);
    selection = g_key_file_get_string_list(config, "Host", "disk", &count, NULL);
    targets = g_strjoinv(" + ", selection);
    /* A mirror is as large as its smallest member, so that is the capacity to name. */
    sizes = g_key_file_get_string_list(config, "Host", "disk_size", NULL, NULL);
    for (i = 0; sizes && sizes[i]; i++) {
        guint64 bytes = g_ascii_strtoull(sizes[i], NULL, 10);
        if (!capacity || bytes < capacity) capacity = bytes;
    }
    if (count > 1)
        layout = g_strdup_printf("one %.1f GiB system disk ×%u RAID1 copies (not pooled; survives %u)",
            capacity / (1024.0 * 1024 * 1024), (guint)count, (guint)count - 1);
    else
        layout = g_strdup("one system disk, ext4, no redundancy");
    g_string_append_printf(s, "\nERASE AND INSTALL\n%s   •   Whole disk%s   •   %s\nAll existing partitions and data on %s will be removed.",
        targets, count > 1 ? "s" : "", layout, count > 1 ? "these disks" : "this disk");
    gtk_text_buffer_set_text(gtk_text_view_get_buffer(GTK_TEXT_VIEW(review)), s->str, -1); g_string_free(s, TRUE);
    if (count > 1)
        erase_label = g_strdup_printf("_Erase all data on %s (%.1f GiB ×%u RAID1)", targets,
            capacity / (1024.0 * 1024 * 1024), (guint)count);
    else
        erase_label = g_strdup_printf("_Erase all data on %s (%.1f GiB)", targets,
            capacity / (1024.0 * 1024 * 1024));
    g_strfreev(sizes); g_free(layout); g_free(targets); g_strfreev(selection);
    gtk_button_set_label(GTK_BUTTON(confirmation), erase_label); g_free(erase_label);
    gtk_toggle_button_set_active(GTK_TOGGLE_BUTTON(confirmation), FALSE);
}
static void install(void)
{
    gchar *data, *stderr_text = NULL, *argv[] = { "/usr/lib/nex-installer/validate-config", "/run/nex-installer/config.ini", NULL };
    gsize length, written = 0; gint fd, status, sync_status, close_status; GError *error = NULL;
    if (!gtk_toggle_button_get_active(GTK_TOGGLE_BUTTON(confirmation))) { message("Confirm that all data on the selected disks can be erased."); return; }
    if (!collect()) return;
    if (g_mkdir_with_parents("/run/nex-installer", 0700) < 0) { message("Cannot create protected installation settings."); return; }
    data = g_key_file_to_data(config, &length, NULL);
    fd = g_open("/run/nex-installer/config.ini.tmp", O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0600);
    if (fd < 0) { message("Cannot save protected installation settings. Check the installer log."); g_free(data); return; }
    while (written < length) { ssize_t n = write(fd, data + written, length - written); if (n <= 0) break; written += n; }
    memset(data, 0, length); g_free(data);
    sync_status = fsync(fd); close_status = close(fd);
    if (sync_status != 0 || close_status != 0 || written != length || g_rename("/run/nex-installer/config.ini.tmp", "/run/nex-installer/config.ini") != 0) { g_unlink("/run/nex-installer/config.ini.tmp"); message("Could not save installation settings. Installation has not started."); return; }
    if (!g_spawn_sync(NULL, argv, NULL, G_SPAWN_STDOUT_TO_DEV_NULL, NULL, NULL, NULL, &stderr_text, &status, &error) || !WIFEXITED(status) || WEXITSTATUS(status)) {
        g_unlink("/run/nex-installer/config.ini"); message(error ? error->message : stderr_text && *stderr_text ? stderr_text : "Settings were rejected. Review the host, network and disk selection."); g_clear_error(&error); g_free(stderr_text); return;
    }
    g_free(stderr_text); gtk_main_quit();
}
static void go(GtkButton *button, gpointer unused)
{
    (void)unused;
    if (preparing) return;
    if (GTK_WIDGET(button) == back) page--;
    else {
        if (!prepared) { prepare(); return; }
        if (page > 0 && !collect()) return;
        if (page == 4) { install(); return; }
        page++; if (page == 4) show_review();
    }
    dhcp_changed(NULL, NULL);
    if (page != 1) gtk_toggle_button_set_active(GTK_TOGGLE_BUTTON(show_password), FALSE);
    message(""); gtk_notebook_set_current_page(GTK_NOTEBOOK(book), page);
    gtk_label_set_text(GTK_LABEL(heading), titles[page]);
    for (gint i = 0; i < 5; i++) {
        const gchar *names[] = { "Welcome", "Host", "Network", "Disk", "Review" };
        gchar *markup = g_markup_printf_escaped(i == page ? "<span foreground='#2456D9'><b>%d   %s</b></span>" : "<span foreground='#65738A'>%d   %s</span>", i + 1, names[i]);
        if (i < page) { g_free(markup); markup = g_markup_printf_escaped("<span foreground='#397057'>✓   %s</span>", names[i]); }
        gtk_label_set_markup(GTK_LABEL(step_labels[i]), markup); g_free(markup);
    }
    if (page==1) gtk_widget_grab_focus(host);
    else if (page==2) { refresh_nic_details(NULL); gtk_widget_grab_focus(nic_list); }
    else if (page==3) gtk_widget_grab_focus(disk_list);
    else if (page==4) gtk_widget_grab_focus(confirmation);
    gtk_widget_set_sensitive(back, page > 0);
    gtk_button_set_label(GTK_BUTTON(next), page != 4 ? "Continue" : selected_disks > 1 ? "Erase disks and install Nex Core" : "Erase disk and install Nex Core");
}
static GtkWidget *page_box(void)
{
    /* Never scroll sideways: BMC consoles are narrow; wrap/ellipsize instead. */
    GtkWidget *scroll = gtk_scrolled_window_new(NULL, NULL), *box = gtk_vbox_new(FALSE, 8);
    gtk_scrolled_window_set_policy(GTK_SCROLLED_WINDOW(scroll), GTK_POLICY_NEVER, GTK_POLICY_AUTOMATIC);
    gtk_container_set_border_width(GTK_CONTAINER(box), 2);
    gtk_scrolled_window_add_with_viewport(GTK_SCROLLED_WINDOW(scroll), box);
    gtk_viewport_set_shadow_type(GTK_VIEWPORT(gtk_bin_get_child(GTK_BIN(scroll))), GTK_SHADOW_NONE);
    gtk_notebook_append_page(GTK_NOTEBOOK(book), scroll, NULL); return box;
}
static gboolean prevent_close(GtkWidget *w, GdkEvent *event, gpointer unused) { (void)w; (void)event; (void)unused; return TRUE; }
int main(int argc, char **argv)
{
    GtkWidget *root, *header, *brand, *box, *row, *editor, *buttons, *scroll, *body, *panel, *content; GdkPixbuf *board, *crop, *scaled; guint i;
    gtk_init(&argc, &argv); umask(0077);
    config = g_key_file_new(); nics = g_ptr_array_new_with_free_func(g_free); disks = g_ptr_array_new_with_free_func(g_free);
    disk_sizes = g_array_new(FALSE, FALSE, sizeof(guint64));
    gtk_rc_parse_string(
        "style \"nex\" { font_name=\"FreeSans 11\" bg[NORMAL]=\"#EFF3F8\" fg[NORMAL]=\"#17233B\" text[NORMAL]=\"#17233B\" base[NORMAL]=\"#FFFFFF\" base[SELECTED]=\"#2456D9\" text[SELECTED]=\"#FFFFFF\" GtkWidget::focus-line-width=1 GtkWidget::focus-padding=2 } class \"GtkWidget\" style \"nex\" "
        "style \"surface\" { bg[NORMAL]=\"#FFFFFF\" } widget \"*.form\" style \"surface\" widget \"*.form.*\" style \"surface\" "
        "style \"control\" { xthickness=1 ythickness=1 bg[NORMAL]=\"#F2F5FA\" bg[PRELIGHT]=\"#E4EBF6\" bg[ACTIVE]=\"#D8E3F4\" GtkButton::inner-border={10,10,5,5} GtkEntry::inner-border={6,6,4,4} } class \"GtkEntry\" style \"control\" class \"GtkButton\" style \"control\" class \"GtkComboBox\" style \"control\" "
        "style \"primary\" { xthickness=0 ythickness=0 bg[NORMAL]=\"#2456D9\" bg[PRELIGHT]=\"#1948BF\" bg[ACTIVE]=\"#133B9F\" fg[NORMAL]=\"#FFFFFF\" fg[PRELIGHT]=\"#FFFFFF\" fg[ACTIVE]=\"#FFFFFF\" } widget \"*.primary\" style \"primary\" widget \"*.primary.*\" style \"primary\" "
        "style \"title\" { font_name=\"FreeSans Bold 18\" } widget \"*.heading\" style \"title\" "
        "style \"error\" { fg[NORMAL]=\"#A02020\" } widget \"*.error\" style \"error\"");
    window = gtk_window_new(GTK_WINDOW_TOPLEVEL); gtk_window_set_title(GTK_WINDOW(window), "Install Nex Core"); gtk_window_set_decorated(GTK_WINDOW(window), FALSE); fit_screen(gtk_widget_get_screen(window), NULL); g_signal_connect(gtk_widget_get_screen(window), "size-changed", G_CALLBACK(fit_screen), NULL); g_signal_connect(window, "delete-event", G_CALLBACK(prevent_close), NULL);
    {
        GdkScreen *screen = gtk_widget_get_screen(window);
        gboolean compact = gdk_screen_get_width(screen) <= 1024 || gdk_screen_get_height(screen) <= 768;
        gint border = compact ? 8 : 14;
        if (compact)
            gtk_rc_parse_string(
                "style \"nex\" { font_name=\"FreeSans 10\" } class \"GtkWidget\" style \"nex\" "
                "style \"title\" { font_name=\"FreeSans Bold 14\" } widget \"*.heading\" style \"title\" "
                "style \"control\" { GtkButton::inner-border={8,8,4,4} GtkEntry::inner-border={5,5,3,3} } class \"GtkEntry\" style \"control\" class \"GtkButton\" style \"control\" class \"GtkComboBox\" style \"control\"");
        root = gtk_vbox_new(FALSE, compact ? 6 : 10); gtk_container_set_border_width(GTK_CONTAINER(root), border); gtk_container_add(GTK_CONTAINER(window), root);
        header = gtk_hbox_new(FALSE, 8); gtk_box_pack_start(GTK_BOX(root), header, FALSE, FALSE, 0);
        board = gdk_pixbuf_new_from_file("/usr/share/nex-installer/nex-core-lockup.png", NULL);
        if (board && gdk_pixbuf_get_width(board) >= 2030 && gdk_pixbuf_get_height(board) >= 550) {
            gint brand_w = compact ? 150 : 200, brand_h = compact ? 28 : 38;
            crop = gdk_pixbuf_new_subpixbuf(board, 150, 190, 1880, 360); scaled = gdk_pixbuf_scale_simple(crop, brand_w, brand_h, GDK_INTERP_BILINEAR); brand = gtk_image_new_from_pixbuf(scaled); atk_object_set_name(gtk_widget_get_accessible(brand), "Nex Core"); g_object_unref(scaled); g_object_unref(crop);
            gtk_widget_set_size_request(brand, brand_w, brand_h + (compact ? 4 : 10));
        }
        else { brand = label("Nex Core"); gtk_widget_set_size_request(brand, compact ? 120 : 200, compact ? 28 : 48); }
        if (board) g_object_unref(board);
        gtk_box_pack_start(GTK_BOX(header), brand, FALSE, FALSE, 0); gtk_box_pack_end(GTK_BOX(header), label("HOST INSTALLATION"), FALSE, FALSE, 0);
        body = gtk_hbox_new(FALSE, compact ? 10 : 18); gtk_box_pack_start(GTK_BOX(root), body, TRUE, TRUE, 0);
        steps = gtk_vbox_new(FALSE, compact ? 8 : 16); gtk_widget_set_size_request(steps, compact ? 88 : 108, -1); gtk_box_pack_start(GTK_BOX(body), steps, FALSE, FALSE, 0);
        for (i = 0; i < 5; i++) {
            const gchar *names[] = { "Welcome", "Host", "Network", "Disk", "Review" };
            gchar *markup = g_markup_printf_escaped(i == 0 ? "<span foreground='#2456D9'><b>%d   %s</b></span>" : "<span foreground='#65738A'>%d   %s</span>", i + 1, names[i]);
            step_labels[i] = label(""); gtk_label_set_line_wrap(GTK_LABEL(step_labels[i]), FALSE); gtk_label_set_markup(GTK_LABEL(step_labels[i]), markup); g_free(markup);
            gtk_box_pack_start(GTK_BOX(steps), step_labels[i], FALSE, FALSE, compact ? 2 : 6);
        }
        panel = gtk_event_box_new(); gtk_widget_set_name(panel, "form"); gtk_box_pack_start(GTK_BOX(body), panel, TRUE, TRUE, 0);
        content = gtk_vbox_new(FALSE, compact ? 8 : 12); gtk_container_set_border_width(GTK_CONTAINER(content), compact ? 10 : 16); gtk_container_add(GTK_CONTAINER(panel), content);
    }
    heading = label(titles[0]); gtk_widget_set_name(heading, "heading"); gtk_label_set_line_wrap(GTK_LABEL(heading), FALSE); gtk_label_set_ellipsize(GTK_LABEL(heading), PANGO_ELLIPSIZE_END); gtk_box_pack_start(GTK_BOX(content), heading, FALSE, FALSE, 0);
    book = gtk_notebook_new(); gtk_notebook_set_show_tabs(GTK_NOTEBOOK(book), FALSE); gtk_notebook_set_show_border(GTK_NOTEBOOK(book), FALSE); gtk_box_pack_start(GTK_BOX(content), book, TRUE, TRUE, 0);
    box = page_box();
    gtk_box_pack_start(GTK_BOX(box), label("Nex Core is the foundation for your virtual machines.\nBuilt on Debian with KVM and libvirt, it turns this machine\ninto a dedicated virtualization host."), FALSE, FALSE, 8);
    gtk_box_pack_start(GTK_BOX(box), label("Configure your host, network adapters and installation disk.\nYou'll review your settings before any disk is erased."), FALSE, FALSE, 8);
    row = label(""); gtk_label_set_markup(GTK_LABEL(row), "<b>Plan your Nex Controller</b>");
    gtk_box_pack_start(GTK_BOX(box), row, FALSE, FALSE, 4);
    gtk_box_pack_start(GTK_BOX(box), label("Don't forget to deploy at least one Nex Controller for your\nenvironment, for central management of your hosts and VMs."), FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(box), label("This installer sets up Nex Core and its local web UI.\nDeploy Nex Controller from that UI or separately."), FALSE, FALSE, 8);

    box = page_box(); host = entry(); password = entry(); repeat = entry(); zone_entry = gtk_combo_box_new_text(); timezone_area = gtk_combo_box_new_text(); keymap = gtk_combo_box_new_text();
    gtk_entry_set_visibility(GTK_ENTRY(password), FALSE); gtk_entry_set_visibility(GTK_ENTRY(repeat), FALSE);
    for (i = 0; i < G_N_ELEMENTS(keymaps); i++) gtk_combo_box_append_text(GTK_COMBO_BOX(keymap), keyboard_names[i]);
    timezone_choices(); show_password = gtk_check_button_new_with_mnemonic("_Show passwords");
    g_signal_connect(show_password, "toggled", G_CALLBACK(password_visibility), NULL);
    gtk_combo_box_set_active(GTK_COMBO_BOX(keymap), 0);
    field(box, "_Hostname", host); field(box, "_Keyboard", keymap);
    field(box, "Timezone _area", timezone_area); field(box, "_Timezone", zone_entry);
    field(box, "_Root password", password); field(box, "Confirm _password", repeat);
    gtk_box_pack_start(GTK_BOX(box), show_password, FALSE, FALSE, 0);
    g_signal_connect(keymap, "changed", G_CALLBACK(keyboard_changed), NULL);
    gtk_widget_set_tooltip_text(zone_entry, "Choose an area, then select your timezone from the list. UTC is universal time.");
    gtk_widget_set_tooltip_text(password, "At least 8 characters. Root provides local recovery access.");
    gtk_widget_set_tooltip_text(keymap, "Applied immediately. Changing layout clears both password fields.");
    box = page_box(); gtk_box_pack_start(GTK_BOX(box), label("Choose an adapter to configure it. Settings apply after installation."), FALSE, FALSE, 0);
    network_hostname = gtk_entry_new_with_buffer(gtk_entry_get_buffer(GTK_ENTRY(host))); field(box, "_Hostname", network_hostname);
    row = gtk_hbox_new(FALSE, 16); gtk_box_pack_start(GTK_BOX(box), row, FALSE, FALSE, 0);
    nic_list = list_widget(); gtk_widget_set_size_request(nic_list, 120, 170); scroll = gtk_scrolled_window_new(NULL, NULL); gtk_scrolled_window_set_policy(GTK_SCROLLED_WINDOW(scroll), GTK_POLICY_AUTOMATIC, GTK_POLICY_AUTOMATIC); gtk_container_add(GTK_CONTAINER(scroll), nic_list); gtk_box_pack_start(GTK_BOX(row), scroll, FALSE, FALSE, 0);
    editor = gtk_vbox_new(FALSE, 2); gtk_box_pack_start(GTK_BOX(row), editor, TRUE, TRUE, 0); nic_details = label("Detecting network adapters…"); gtk_box_pack_start(GTK_BOX(editor), nic_details, FALSE, FALSE, 0);
    mode = gtk_combo_box_new_text(); gtk_combo_box_append_text(GTK_COMBO_BOX(mode), "Disabled"); gtk_combo_box_append_text(GTK_COMBO_BOX(mode), "Automatic (DHCP)"); gtk_combo_box_append_text(GTK_COMBO_BOX(mode), "Static IPv4"); address = entry(); gateway = entry(); vlan = gtk_spin_button_new_with_range(0, 4094, 1);
    field(editor, "Address _mode", mode);
    row = gtk_hbox_new(FALSE, 8); gtk_box_pack_start(GTK_BOX(row), address, TRUE, TRUE, 0);
    dhcp_retry = gtk_button_new_with_label("Retry"); gtk_widget_set_no_show_all(dhcp_retry, TRUE);
    gtk_box_pack_start(GTK_BOX(row), dhcp_retry, FALSE, FALSE, 0);
    g_signal_connect(dhcp_retry, "clicked", G_CALLBACK(dhcp_changed), NULL);
    field(editor, "_Address / prefix", row); field(editor, "_Gateway", gateway); field(editor, "_VLAN ID", vlan);
    gtk_widget_set_tooltip_text(address, "IPv4 address with prefix, for example 192.168.1.20/24.");
    gtk_widget_set_tooltip_text(gateway, "Default IPv4 router; only set this on the management adapter.");
    gtk_widget_set_tooltip_text(vlan, "0: untagged network. 1–4094: VLAN tag; the switch must carry this VLAN.");
    management = gtk_combo_box_new_text(); gtk_combo_box_append_text(GTK_COMBO_BOX(management), "Offline (no management network)"); gtk_combo_box_set_active(GTK_COMBO_BOX(management), 0); dns = entry(); field(box, "_Management NIC", management); field(box, "_DNS servers", dns);
    gtk_widget_set_tooltip_text(dns, "Space-separated IPv4 DNS servers. Leave blank for DHCP-provided DNS on the management adapter.");
    gtk_box_pack_start(GTK_BOX(box), label("Use IPv4/prefix, for example 192.168.1.20/24. Separate DNS addresses with spaces.\nVLAN 0 is untagged; use 1–4094 for a tagged connection.\nOnly the management adapter may have a default gateway."), FALSE, FALSE, 0);
    g_signal_connect(vlan, "value-changed", G_CALLBACK(dhcp_changed), NULL);
    g_signal_connect(mode, "changed", G_CALLBACK(mode_changed), NULL); g_signal_connect(gtk_tree_view_get_selection(GTK_TREE_VIEW(nic_list)), "changed", G_CALLBACK(nic_changed), NULL);
    box = page_box(); gtk_box_pack_start(GTK_BOX(box), label("Tick one disk for a single install, or two or more for RAID1 copies of one system disk. The summary shows what you get. Ticked disks are erased; others are left alone."), FALSE, FALSE, 0);
    disk_list = disk_list_widget();
    scroll = gtk_scrolled_window_new(NULL, NULL);
    gtk_scrolled_window_set_policy(GTK_SCROLLED_WINDOW(scroll), GTK_POLICY_AUTOMATIC, GTK_POLICY_AUTOMATIC);
    gtk_scrolled_window_set_shadow_type(GTK_SCROLLED_WINDOW(scroll), GTK_SHADOW_IN);
    gtk_container_add(GTK_CONTAINER(scroll), disk_list);
    gtk_widget_set_size_request(scroll, -1, 100);
    gtk_box_pack_start(GTK_BOX(box), scroll, TRUE, TRUE, 0);
    disk_summary = label("No disk selected.");
    gtk_label_set_max_width_chars(GTK_LABEL(disk_summary), -1);
    g_signal_connect(disk_summary, "size-allocate", G_CALLBACK(wrap_label_allocate), NULL);
    gtk_box_pack_start(GTK_BOX(box), disk_summary, FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(box), label("No disks listed? Attach a writable internal disk and restart the installer."), FALSE, FALSE, 0);
    box = page_box(); scroll = gtk_scrolled_window_new(NULL, NULL); gtk_scrolled_window_set_policy(GTK_SCROLLED_WINDOW(scroll), GTK_POLICY_AUTOMATIC, GTK_POLICY_AUTOMATIC); review = gtk_text_view_new(); gtk_text_view_set_editable(GTK_TEXT_VIEW(review), FALSE); gtk_text_view_set_cursor_visible(GTK_TEXT_VIEW(review), FALSE); gtk_text_view_set_wrap_mode(GTK_TEXT_VIEW(review), GTK_WRAP_WORD_CHAR); gtk_container_add(GTK_CONTAINER(scroll), review); gtk_widget_set_size_request(scroll, -1, 120); gtk_box_pack_start(GTK_BOX(box), scroll, TRUE, TRUE, 0); confirmation = gtk_check_button_new_with_mnemonic("_Erase all data on the selected disks"); gtk_box_pack_start(GTK_BOX(box), confirmation, FALSE, FALSE, 0);
    error_label = label(""); gtk_widget_set_name(error_label, "error"); gtk_label_set_line_wrap(GTK_LABEL(error_label), FALSE); gtk_label_set_ellipsize(GTK_LABEL(error_label), PANGO_ELLIPSIZE_END); gtk_box_pack_start(GTK_BOX(content), error_label, FALSE, FALSE, 0);
    buttons = gtk_hbox_new(FALSE, 12); back = gtk_button_new_with_mnemonic("_Back"); next = gtk_button_new_with_label("Preparing Nex Core…"); gtk_widget_set_name(next, "primary"); gtk_button_set_relief(GTK_BUTTON(back), GTK_RELIEF_NONE); gtk_widget_set_sensitive(back, FALSE); gtk_box_pack_start(GTK_BOX(buttons), back, FALSE, FALSE, 0); gtk_box_pack_end(GTK_BOX(buttons), next, FALSE, FALSE, 0); gtk_box_pack_start(GTK_BOX(content), buttons, FALSE, FALSE, 0); g_signal_connect(back, "clicked", G_CALLBACK(go), NULL); g_signal_connect(next, "clicked", G_CALLBACK(go), NULL);
    gtk_widget_show_all(window); fit_screen(gtk_widget_get_screen(window), NULL); gtk_window_present(GTK_WINDOW(window)); keyboard_changed(GTK_COMBO_BOX(keymap), NULL); prepare(); g_timeout_add_seconds(2, refresh_nic_details, NULL); gtk_main();
    dhcp_cancel();
    while (dhcp_children) g_main_context_iteration(NULL, TRUE);
    g_strfreev(timezones); g_key_file_free(config); g_ptr_array_free(nics, TRUE); g_ptr_array_free(disks, TRUE); g_array_free(disk_sizes, TRUE); return 0;
}
