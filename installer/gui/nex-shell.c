/* SPDX-License-Identifier: AGPL-3.0-only
 * Original Nex wizard shell. Included by cdebconf's GTK frontend ui.c.
 * Question rendering, navigation and installation remain owned by d-i.
 */

/* The wizard's own steps, then the one it does not have. The operator reaches these
 * screens straight out of nex-wizard.c, so the rail has to continue that list instead of
 * renaming the journey halfway through it (BUG-0209). Keep the first five in step with the
 * names[] arrays in nex-wizard.c.
 */
static const char *nex_stages[] = {
    "Welcome", "Host", "Network", "Disk", "Review", "Install"
};

/* Template namespaces are stable across translations. An unrecognised
 * question deliberately has no highlighted stage; these are not checkpoints.
 * Stages match the wizard page that collected the same setting, so a question the
 * wizard could not preseed lands on the step the operator already recognises.
 */
static int nex_question_stage(const char *tag)
{
    static const struct { const char *prefix; int stage; } namespaces[] = {
        { "localechooser/", 1 },
        { "keyboard-configuration/", 1 }, { "console-setup/", 1 },
        { "tzsetup/", 1 }, { "clock-setup/", 1 },
        { "passwd/", 1 }, { "user-setup/", 1 },
        { "netcfg/", 2 }, { "ethdetect/", 2 },
        { "partman/", 3 }, { "partman-", 3 },
        { "base-installer/", 5 }, { "bootstrap-base/", 5 },
        { "apt-setup/", 5 }, { "pkgsel/", 5 }, { "tasksel/", 5 },
        { "grub-installer/", 5 }, { "finish-install/", 5 }
    };
    unsigned int i;

    if (tag == NULL)
        return -1;
    if (!strcmp(tag, "debian-installer/language") ||
        !strcmp(tag, "debian-installer/country") ||
        !strcmp(tag, "debian-installer/locale"))
        return 1;
    for (i = 0; i < G_N_ELEMENTS(namespaces); i++)
        if (g_str_has_prefix(tag, namespaces[i].prefix))
            return namespaces[i].stage;
    return -1;
}

static GtkWidget *nex_label(const char *text, const char *name)
{
    GtkWidget *label = gtk_label_new(text);
    gtk_widget_set_name(label, name);
    gtk_misc_set_alignment(GTK_MISC(label), 0, 0.5);
    return label;
}

/* Read existing diagnostic tails only when requested; never log answers here. */
#include <stdio.h>
#include "template.h"

/* How much of each installer log the live Install panel and View log show. */
enum { NEX_LOG_TAIL = 128 * 1024, NEX_LIVE_LOG_TAIL = 48 * 1024 };

/* The completion question. It is asked once the install has already written the disk,
 * which is why that screen offers no way back and no Go Back button (see build-gui.sh).
 */
gboolean cdebconf_gtk_nex_is_completion(struct frontend *fe)
{
    return fe->questions != NULL && fe->questions->tag != NULL &&
        !strcmp(fe->questions->tag, "finish-install/reboot_in_progress");
}

static gboolean nex_has_failure(struct frontend *fe)
{
    struct question *q;
    for (q = fe->questions; q != NULL; q = q->next)
        if ((q->template && q->template->type && !strcmp(q->template->type, "error")) ||
            (q->tag && !strcmp(q->tag, "debian-installer/main-menu/item-failure")))
            return TRUE;
    return FALSE;
}

static gchar *nex_log_tail(const char *path, long limit)
{
    FILE *file = fopen(path, "rb");
    gchar *buffer, *valid;
    long length;
    size_t used;
    if (file == NULL)
        return g_strdup("Log unavailable.\n");
    if (fseek(file, 0, SEEK_END) || (length = ftell(file)) < 0 ||
        fseek(file, length > limit ? length - limit : 0, SEEK_SET)) {
        fclose(file);
        return g_strdup("Could not read this log.\n");
    }
    buffer = g_malloc(limit + 1);
    used = fread(buffer, 1, limit, file);
    if (ferror(file)) {
        fclose(file);
        g_free(buffer);
        return g_strdup("Could not read this log.\n");
    }
    fclose(file);
    buffer[used] = '\0';
    valid = g_utf8_make_valid(buffer, used);
    g_free(buffer);
    return valid;
}

static gboolean nex_scroll_log(gpointer data)
{
    GtkWidget *view = data;
    if (gtk_widget_get_realized(view)) {
        GtkTextBuffer *buffer = gtk_text_view_get_buffer(GTK_TEXT_VIEW(view));
        GtkTextIter end;
        gtk_widget_grab_focus(view);
        gtk_text_buffer_get_end_iter(buffer, &end);
        gtk_text_buffer_place_cursor(buffer, &end);
        gtk_text_view_scroll_to_mark(GTK_TEXT_VIEW(view),
            gtk_text_buffer_get_insert(buffer), 0.0, TRUE, 0.0, 1.0);
    }
    return FALSE;
}

static void nex_append_log_files(GString *text, long limit)
{
    const char *paths[] = { "/var/log/syslog", "/var/log/partman", "/var/log/nex-installer-prepare.log" };
    unsigned int i;
    for (i = 0; i < G_N_ELEMENTS(paths); i++) {
        gchar *tail = nex_log_tail(paths[i], limit);
        g_string_append_printf(text, "%s\n%s\n\n", paths[i], tail);
        g_free(tail);
    }
}

static void nex_view_log(GtkWidget *button, gpointer parent)
{
    GtkWidget *dialog, *scroll, *view, *caption;
    GtkTextBuffer *buffer;
    GString *text = g_string_new("");
    (void)button;
    nex_append_log_files(text, NEX_LOG_TAIL);
    dialog = gtk_dialog_new_with_buttons("Nex Core installation log", GTK_WINDOW(parent),
        GTK_DIALOG_MODAL | GTK_DIALOG_DESTROY_WITH_PARENT, "_Close", GTK_RESPONSE_CLOSE, NULL);
    gtk_widget_set_name(dialog, "nex-installer-log");
    gtk_window_set_default_size(GTK_WINDOW(dialog),
        MIN(720, gdk_screen_width() - 40), MIN(560, gdk_screen_height() - 40));
    caption = nex_label("Recent log entries (up to 128 KiB per file). Close to return to the error.", "nex-caption");
    gtk_label_set_line_wrap(GTK_LABEL(caption), TRUE);
    gtk_label_set_width_chars(GTK_LABEL(caption), 50);
    gtk_box_pack_start(GTK_BOX(gtk_dialog_get_content_area(GTK_DIALOG(dialog))), caption, FALSE, FALSE, 8);
    scroll = gtk_scrolled_window_new(NULL, NULL);
    gtk_scrolled_window_set_policy(GTK_SCROLLED_WINDOW(scroll), GTK_POLICY_AUTOMATIC, GTK_POLICY_AUTOMATIC);
    view = gtk_text_view_new();
    gtk_text_view_set_editable(GTK_TEXT_VIEW(view), FALSE);
    gtk_text_view_set_wrap_mode(GTK_TEXT_VIEW(view), GTK_WRAP_WORD_CHAR);
    buffer = gtk_text_view_get_buffer(GTK_TEXT_VIEW(view));
    gtk_text_buffer_set_text(buffer, text->str, text->len);
    g_string_free(text, TRUE);
    gtk_container_add(GTK_CONTAINER(scroll), view);
    gtk_box_pack_start(GTK_BOX(gtk_dialog_get_content_area(GTK_DIALOG(dialog))), scroll, TRUE, TRUE, 8);
    g_signal_connect_swapped(dialog, "response", G_CALLBACK(gtk_widget_destroy), dialog);
    gtk_widget_show_all(dialog);
    gtk_window_present(GTK_WINDOW(dialog));
    gdk_threads_add_idle_full(G_PRIORITY_LOW, nex_scroll_log, g_object_ref(view), g_object_unref);
}

/* Keep the live Install log pinned to new output unless the operator scrolled up. */
static gboolean nex_live_log_at_bottom(GtkWidget *view)
{
    GtkAdjustment *v = gtk_text_view_get_vadjustment(GTK_TEXT_VIEW(view));
    gdouble upper, page, value;
    if (v == NULL)
        return TRUE;
    upper = gtk_adjustment_get_upper(v);
    page = gtk_adjustment_get_page_size(v);
    value = gtk_adjustment_get_value(v);
    return upper <= page || value >= upper - page - 24.0;
}

static void nex_refresh_live_log(GtkWidget *window)
{
    GtkWidget *view = g_object_get_data(G_OBJECT(window), "nex-live-log");
    GtkTextBuffer *buffer;
    GString *text;
    gboolean follow;
    if (view == NULL)
        return;
    follow = nex_live_log_at_bottom(view);
    text = g_string_new("");
    nex_append_log_files(text, NEX_LIVE_LOG_TAIL);
    buffer = gtk_text_view_get_buffer(GTK_TEXT_VIEW(view));
    gtk_text_buffer_set_text(buffer, text->str, text->len);
    g_string_free(text, TRUE);
    if (follow)
        gdk_threads_add_idle_full(G_PRIORITY_LOW, nex_scroll_log, g_object_ref(view), g_object_unref);
}

/* Refresh the live log and animate Still working… while the Install panel is showing,
 * so a long step like late preseed does not look frozen when the fraction is not moving.
 */
static gboolean nex_progress_tick(gpointer window)
{
    GtkWidget *panel = g_object_get_data(G_OBJECT(window), "nex-waiting");
    GtkWidget *activity = g_object_get_data(G_OBJECT(window), "nex-progress-activity");
    guint phase;
    static const char *dots[] = {".  ", ".. ", "...", "   "};
    if (panel == NULL || !gtk_widget_get_visible(panel)) {
        g_object_set_data(G_OBJECT(window), "nex-progress-tick", NULL);
        return FALSE;
    }
    phase = GPOINTER_TO_UINT(g_object_get_data(G_OBJECT(window), "nex-progress-phase")) + 1;
    g_object_set_data(G_OBJECT(window), "nex-progress-phase", GUINT_TO_POINTER(phase));
    if (activity != NULL) {
        gchar *caption = g_strdup_printf("Still working%s", dots[phase % 4]);
        gtk_label_set_text(GTK_LABEL(activity), caption);
        g_free(caption);
    }
    /* Refresh the live log about once a second (tick is 100 ms). */
    if (phase % 10 == 0)
        nex_refresh_live_log(window);
    return TRUE;
}

static void nex_progress_activity_set(GtkWidget *window, gboolean on)
{
    guint id = GPOINTER_TO_UINT(g_object_get_data(G_OBJECT(window), "nex-progress-tick"));
    if (on) {
        if (id == 0) {
            g_object_set_data(G_OBJECT(window), "nex-progress-phase", GUINT_TO_POINTER(0));
            nex_refresh_live_log(window);
            id = g_timeout_add(100, nex_progress_tick, window);
            g_object_set_data(G_OBJECT(window), "nex-progress-tick", GUINT_TO_POINTER(id));
        }
    } else if (id != 0) {
        g_source_remove(id);
        g_object_set_data(G_OBJECT(window), "nex-progress-tick", NULL);
    }
}

/* Keep the native reason and recovery controls; add diagnostics without answering GO. */
void cdebconf_gtk_nex_failure(struct frontend *fe, GtkWidget *question_box)
{
    struct frontend_data *data = fe->data;
    GtkWidget *button;
    if (!nex_has_failure(fe))
        return;
    button = gtk_button_new_with_mnemonic("_View log");
    g_signal_connect(button, "clicked", G_CALLBACK(nex_view_log), data->window);
    gtk_box_pack_start(GTK_BOX(question_box), button, FALSE, FALSE, 8);
}

static void nex_update_shell(struct frontend *fe)
{
    struct frontend_data *data = fe->data;
    struct question *question = fe->questions;
    GtkWidget *label;
    gchar *markup;
    char *info;
    gboolean complete = cdebconf_gtk_nex_is_completion(fe);
    int stage = -1;
    unsigned int i;
    static const char *hints[] = {
        "Nex Core is installing on this host.",
        "Set the name, keyboard, time and root password for this host.",
        "Choose the connection and identity for this host.",
        "Check the target disk carefully. Writing changes can erase its data.",
        "Review the settings collected before any disk is written.",
        "Follow the installation progress and any prompts below."
    };

    if (question != NULL)
        stage = nex_question_stage(question->tag);
    else if (data->progress_data != NULL && fe->progress_title != NULL)
        stage = nex_question_stage(fe->progress_title->tag);

    /* Set plain text before applying markup so installer titles cannot inject it. */
    gtk_label_set_text(GTK_LABEL(data->title), nex_has_failure(fe) ? "Installation needs attention" : complete ? "Nex Core installation complete" :
        question != NULL && question->tag != NULL &&
        !strcmp(question->tag, "main-menu/select")
            ? "Installation options"
            : (fe->title ? fe->title : "Install Nex Core"));
    for (i = 0; i < G_N_ELEMENTS(nex_stages); i++) {
        gchar *key = g_strdup_printf("nex-stage-%u", i);
        label = g_object_get_data(G_OBJECT(data->window), key);
        g_free(key);
        if (label == NULL)
            continue;
        /* Steps the wizard already took keep the wizard's own completed mark, so the
         * rail reads as one journey across the hand-off instead of restarting.
         */
        if (stage >= 0 && i < (unsigned int)stage)
            markup = g_markup_printf_escaped(
                "<span foreground='#397057'>✓   %s</span>", nex_stages[i]);
        else
            markup = g_markup_printf_escaped(
                i == (unsigned int)stage
                    ? "<span foreground='#2456D9'><b>%s</b>  •</span>"
                    : "<span foreground='#65738A'>%s</span>", nex_stages[i]);
        gtk_label_set_markup(GTK_LABEL(label), markup);
        g_free(markup);
    }
    label = g_object_get_data(G_OBJECT(data->window), "nex-purpose");
    if (label != NULL)
        gtk_label_set_text(GTK_LABEL(label), nex_has_failure(fe)
            ? "This step could not complete. Review the error and installation log before continuing." : complete
            ? "Nex Core is installed and ready to start." : stage < 0
            ? "Review the installer instructions below to continue." : hints[stage]);
    label = g_object_get_data(G_OBJECT(data->window), "nex-info");
    if (label != NULL) {
        info = fe->info ? q_get_description(fe, fe->info) : NULL;
        gtk_label_set_text(GTK_LABEL(label), info ? info : "Host installation");
        g_free(info);
    }
}

/* The native finish-install question remains the completion/reboot gate. */
void cdebconf_gtk_nex_completion(GtkWidget *question_box)
{
    GtkWidget *panel = gtk_vbox_new(FALSE, 18);
    GtkWidget *status = nex_label("", "nex-heading");
    GtkWidget *instructions = nex_label(
        "Remove or disconnect the installation media, then choose Restart now "
        "to boot from the installed disk.", "nex-caption");

    gtk_label_set_markup(GTK_LABEL(status),
        "<span foreground='#21824A'>✓</span>  Nex Core is ready");
    gtk_label_set_line_wrap(GTK_LABEL(instructions), TRUE);
    gtk_label_set_width_chars(GTK_LABEL(instructions), 44);
    gtk_container_set_border_width(GTK_CONTAINER(panel), 18);
    gtk_box_pack_start(GTK_BOX(panel), status, FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(panel), instructions, FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(question_box), panel, TRUE, TRUE, 0);
}

/* The same panel stays mapped during progress and between operations.
 * A gap clears the step fraction; it never implies whole-install progress.
 * Callers hold the frontend's GTK lock, just like the question/progress code.
 */
void cdebconf_gtk_nex_set_waiting(struct frontend *fe, gboolean waiting)
{
    struct frontend_data *data = fe->data;
    GtkWidget *panel = g_object_get_data(G_OBJECT(data->window), "nex-waiting");
    GList *children;

    if (panel == NULL)
        return;
    children = gtk_container_get_children(GTK_CONTAINER(data->target_box));
    waiting = waiting && children == NULL;
    g_list_free(children);
    if (waiting) {
        gtk_widget_hide(data->target_box);
        gtk_widget_show(panel);
        gtk_label_set_text(GTK_LABEL(g_object_get_data(G_OBJECT(data->window),
            "nex-progress-step")), "Preparing the next installation step…");
        gtk_label_set_text(GTK_LABEL(g_object_get_data(G_OBJECT(data->window),
            "nex-progress-info")), "");
        gtk_progress_bar_set_fraction(GTK_PROGRESS_BAR(g_object_get_data(G_OBJECT(data->window),
            "nex-progress-track")), 0.0);
        gtk_label_set_text(GTK_LABEL(data->title), "Install Nex Core");
        gtk_label_set_text(GTK_LABEL(g_object_get_data(G_OBJECT(data->window),
            "nex-purpose")), "Follow the installation progress and any prompts below.");
        nex_progress_activity_set(data->window, TRUE);
    } else {
        nex_progress_activity_set(data->window, FALSE);
        gtk_widget_hide(panel);
        gtk_widget_show(data->target_box);
    }
}

/* Keep native progress accessibility/state, with an original flat track. */
static gboolean nex_draw_progress(GtkWidget *widget, GdkEventExpose *event, gpointer unused)
{
    cairo_t *cr = gdk_cairo_create(gtk_widget_get_window(widget));
    GtkAllocation allocation;
    gdouble fraction = gtk_progress_bar_get_fraction(GTK_PROGRESS_BAR(widget));
    gint x, y;
    (void)unused;
    gtk_widget_get_allocation(widget, &allocation);
    x = gtk_widget_get_has_window(widget) ? 0 : allocation.x;
    y = gtk_widget_get_has_window(widget) ? 0 : allocation.y;
    gdk_cairo_rectangle(cr, &event->area);
    cairo_clip(cr);
    cairo_set_source_rgb(cr, 225.0 / 255, 232.0 / 255, 242.0 / 255);
    cairo_rectangle(cr, x, y, allocation.width, allocation.height);
    cairo_fill(cr);
    cairo_set_source_rgb(cr, 36.0 / 255, 86.0 / 255, 217.0 / 255);
    cairo_rectangle(cr, x, y, allocation.width * fraction, allocation.height);
    cairo_fill(cr);
    cairo_destroy(cr);
    return TRUE;
}

void cdebconf_gtk_nex_progress(struct frontend *fe, const char *step,
                              const char *info, gdouble fraction)
{
    struct frontend_data *data = fe->data;
    cdebconf_gtk_nex_set_waiting(fe, TRUE);
    gtk_label_set_text(GTK_LABEL(g_object_get_data(G_OBJECT(data->window),
        "nex-progress-step")), step ? step : "Installing Nex Core");
    gtk_label_set_text(GTK_LABEL(g_object_get_data(G_OBJECT(data->window),
        "nex-progress-info")), info ? info : "");
    gtk_progress_bar_set_fraction(GTK_PROGRESS_BAR(g_object_get_data(G_OBJECT(data->window),
        "nex-progress-track")), fraction);
}

/* Render the transparent Core lockup without the reference board texture. Keep its
 * drawn letterforms instead of approximating the mark with a system font.
 */
static GtkWidget *nex_brand_image(void)
{
    GdkPixbuf *board, *lockup, *scaled;
    GtkWidget *image;

    board = gdk_pixbuf_new_from_file("/usr/share/nex-installer/nex-core-lockup.png", NULL);
    if (board == NULL)
        return nex_label("Nex Core", "nex-brand");
    if (gdk_pixbuf_get_width(board) < 2030 || gdk_pixbuf_get_height(board) < 550) {
        g_object_unref(board);
        return nex_label("Nex Core", "nex-brand");
    }
    lockup = gdk_pixbuf_new_subpixbuf(board, 150, 190, 1880, 360);
    scaled = gdk_pixbuf_scale_simple(lockup, 240, 46, GDK_INTERP_BILINEAR);
    image = gtk_image_new_from_pixbuf(scaled);
    gtk_widget_set_size_request(image, 240, 58);
    gtk_misc_set_alignment(GTK_MISC(image), 0, 0.5);
    atk_object_set_name(gtk_widget_get_accessible(image), "Nex Core");
    g_object_unref(scaled);
    g_object_unref(lockup);
    g_object_unref(board);
    return image;
}

static void create_main_widgets(struct frontend *fe, GtkWidget *window)
{
    struct frontend_data *data = fe->data;
    GtkWidget *outer, *header, *brand, *wordmark, *body;
    GtkWidget *rail, *rail_surface, *page, *page_surface, *label, *footer;
    unsigned int i;

    gtk_rc_parse_string(
        "style \"nex-base\" { font_name = \"FreeSans 11\" "
        "fg[NORMAL] = \"#17233B\" text[NORMAL] = \"#17233B\" "
        "bg[NORMAL] = \"#EFF3F8\" base[NORMAL] = \"#FFFFFF\" "
        "bg[SELECTED] = \"#2456D9\" base[SELECTED] = \"#2456D9\" "
        "fg[SELECTED] = \"#FFFFFF\" text[SELECTED] = \"#FFFFFF\" } "
        "style \"nex-track\" { xthickness=0 ythickness=0 GtkProgressBar::min-horizontal-bar-height=10 } "
        "widget \"*.nex-progress-track\" style \"nex-track\" "
        "style \"nex-paper\" { bg[NORMAL] = \"#FFFFFF\" } "
        "style \"nex-rail\" { bg[NORMAL] = \"#EFF3F8\" } "
        "style \"nex-brand\" { font_name = \"FreeSans Bold 20\" } "
        "style \"nex-heading\" { font_name = \"FreeSans Bold 20\" } "
        "style \"nex-caption\" { font_name = \"FreeSans 10\" } "
        "style \"nex-live-log\" { font_name = \"FreeMono 9\" "
        "text[NORMAL] = \"#17233B\" base[NORMAL] = \"#F7F9FC\" } "
        "widget \"*.nex-live-log\" style \"nex-live-log\" "
        "style \"nex-controls\" { xthickness=1 ythickness=1 "
        "GtkButton::inner-border={12,12,7,7} GtkEntry::inner-border={8,8,6,6} "
        "bg[NORMAL]=\"#EFF3F8\" bg[PRELIGHT]=\"#E1E8F2\" "
        "bg[ACTIVE]=\"#D3DDEC\" } "
        "widget_class \"*GtkButton*\" style \"nex-controls\" "
        "widget_class \"*GtkEntry*\" style \"nex-controls\" "
        "widget \"nex-installer*\" style \"nex-base\" "
        "widget \"*.nex-paper*\" style \"nex-paper\" "
        "widget \"*.nex-rail*\" style \"nex-rail\" "
        "widget \"*.nex-brand\" style \"nex-brand\" "
        "widget \"*.nex-heading\" style \"nex-heading\" "
        "widget \"*.nex-caption\" style \"nex-caption\"");
    gtk_widget_set_name(window, "nex-installer");
    gtk_window_set_title(GTK_WINDOW(window), "Nex Core — Install");

    outer = gtk_vbox_new(FALSE, 0);
    gtk_container_add(GTK_CONTAINER(window), outer);
    header = gtk_event_box_new();
    brand = gtk_hbox_new(FALSE, 12);
    gtk_container_set_border_width(GTK_CONTAINER(brand), 16);
    gtk_container_add(GTK_CONTAINER(header), brand);
    wordmark = gtk_vbox_new(FALSE, 0);
    gtk_box_pack_start(GTK_BOX(wordmark), nex_brand_image(), FALSE, FALSE, 0);
    label = nex_label("Host installation", "nex-caption");
    g_object_set_data(G_OBJECT(window), "nex-info", label);
    gtk_box_pack_start(GTK_BOX(wordmark), label, FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(brand), wordmark, TRUE, TRUE, 0);
    gtk_box_pack_end(GTK_BOX(brand), nex_label("Nex Core setup", "nex-caption"), FALSE, FALSE, 0);
    gtk_box_pack_start(GTK_BOX(outer), header, FALSE, FALSE, 0);
    data->banner_widget = header;
    data->banner_adjusted = TRUE;
    data->banner_workaround_needed = FALSE;

    body = gtk_hbox_new(FALSE, 0);
    gtk_box_pack_start(GTK_BOX(outer), body, TRUE, TRUE, 0);
    rail_surface = gtk_event_box_new();
    gtk_widget_set_name(rail_surface, "nex-rail");
    rail = gtk_vbox_new(FALSE, 0);
    gtk_container_set_border_width(GTK_CONTAINER(rail), 16);
    gtk_container_add(GTK_CONTAINER(rail_surface), rail);
    gtk_box_pack_start(GTK_BOX(rail), nex_label("Steps", "nex-caption"), FALSE, FALSE, 10);
    for (i = 0; i < G_N_ELEMENTS(nex_stages); i++) {
        gchar *key = g_strdup_printf("nex-stage-%u", i);
        label = nex_label(nex_stages[i], "nex-stage");
        gtk_label_set_width_chars(GTK_LABEL(label), 14);
        g_object_set_data(G_OBJECT(window), key, label);
        g_free(key);
        gtk_box_pack_start(GTK_BOX(rail), label, FALSE, FALSE, 13);
    }
    label = nex_label("✓  Done\n•  Current step", "nex-caption");
    gtk_box_pack_end(GTK_BOX(rail), label, FALSE, FALSE, 6);
    gtk_box_pack_start(GTK_BOX(body), rail_surface, FALSE, FALSE, 0);

    page_surface = gtk_event_box_new();
    gtk_widget_set_name(page_surface, "nex-paper");
    page = gtk_vbox_new(FALSE, 0);
    gtk_container_set_border_width(GTK_CONTAINER(page), 18);
    gtk_container_add(GTK_CONTAINER(page_surface), page);
    data->title = nex_label("Install Nex Core", "nex-heading");
    g_object_ref(data->title);
    gtk_label_set_line_wrap(GTK_LABEL(data->title), TRUE);
    gtk_label_set_width_chars(GTK_LABEL(data->title), 30);
    gtk_box_pack_start(GTK_BOX(page), data->title, FALSE, FALSE, 4);
    label = nex_label("Review the installer instructions below to continue.", "nex-caption");
    gtk_label_set_line_wrap(GTK_LABEL(label), TRUE);
    gtk_label_set_width_chars(GTK_LABEL(label), 48);
    g_object_set_data(G_OBJECT(window), "nex-purpose", label);
    gtk_box_pack_start(GTK_BOX(page), label, FALSE, FALSE, 4);
    create_target_box(fe, page);
    {
        GtkWidget *panel = gtk_vbox_new(FALSE, 16);
        GtkWidget *track = gtk_progress_bar_new();
        GtkWidget *scroll, *view, *activity;
        g_object_set_data(G_OBJECT(window), "nex-waiting", panel);
        label = nex_label("Preparing the next installation step…", "nex-progress-step");
        gtk_label_set_ellipsize(GTK_LABEL(label), PANGO_ELLIPSIZE_END);
        g_object_set_data(G_OBJECT(window), "nex-progress-step", label);
        gtk_box_pack_start(GTK_BOX(panel), label, FALSE, FALSE, 0);
        gtk_widget_set_name(track, "nex-progress-track");
        gtk_widget_set_size_request(track, -1, 10);
        atk_object_set_name(gtk_widget_get_accessible(track), "Current installation step progress");
        g_signal_connect(track, "expose-event", G_CALLBACK(nex_draw_progress), NULL);
        g_object_set_data(G_OBJECT(window), "nex-progress-track", track);
        gtk_box_pack_start(GTK_BOX(panel), track, FALSE, FALSE, 0);
        label = nex_label("", "nex-caption");
        gtk_label_set_ellipsize(GTK_LABEL(label), PANGO_ELLIPSIZE_END);
        g_object_set_data(G_OBJECT(window), "nex-progress-info", label);
        gtk_box_pack_start(GTK_BOX(panel), label, FALSE, FALSE, 0);
        gtk_box_pack_start(GTK_BOX(panel), nex_label("Please keep this machine powered on.",
            "nex-caption"), FALSE, FALSE, 0);
        activity = nex_label("Still working…", "nex-caption");
        g_object_set_data(G_OBJECT(window), "nex-progress-activity", activity);
        gtk_box_pack_start(GTK_BOX(panel), activity, FALSE, FALSE, 0);
        label = nex_label("Installer activity", "nex-caption");
        gtk_box_pack_start(GTK_BOX(panel), label, FALSE, FALSE, 0);
        scroll = gtk_scrolled_window_new(NULL, NULL);
        gtk_scrolled_window_set_policy(GTK_SCROLLED_WINDOW(scroll),
            GTK_POLICY_AUTOMATIC, GTK_POLICY_AUTOMATIC);
        gtk_scrolled_window_set_shadow_type(GTK_SCROLLED_WINDOW(scroll), GTK_SHADOW_IN);
        view = gtk_text_view_new();
        gtk_widget_set_name(view, "nex-live-log");
        gtk_text_view_set_editable(GTK_TEXT_VIEW(view), FALSE);
        gtk_text_view_set_cursor_visible(GTK_TEXT_VIEW(view), FALSE);
        gtk_text_view_set_wrap_mode(GTK_TEXT_VIEW(view), GTK_WRAP_CHAR);
        g_object_set_data(G_OBJECT(window), "nex-live-log", view);
        gtk_container_add(GTK_CONTAINER(scroll), view);
        gtk_box_pack_start(GTK_BOX(panel), scroll, TRUE, TRUE, 0);
        gtk_widget_show_all(panel);
        gtk_widget_set_no_show_all(panel, TRUE);
        gtk_widget_hide(panel);
        gtk_box_pack_start(GTK_BOX(page), panel, TRUE, TRUE, 24);
    }
    gtk_box_pack_start(GTK_BOX(page), gtk_hseparator_new(), FALSE, FALSE, 0);
    create_action_box(fe, page);
    gtk_box_pack_start(GTK_BOX(body), page_surface, TRUE, TRUE, 0);

    footer = nex_label("Local installer • Alpha build • Verify the selected disk before writing changes.", "nex-caption");
    gtk_misc_set_padding(GTK_MISC(footer), 16, 9);
    gtk_box_pack_end(GTK_BOX(outer), footer, FALSE, FALSE, 0);
}
