/* SPDX-License-Identifier: AGPL-3.0-only */
#ifndef NEX_NETWORK_H
#define NEX_NETWORK_H
#include <glib.h>
gboolean nex_install_disk_name_supported(const char *name);
gchar *nex_install_disk_serial(const char *block);
gchar *nex_hostname_normalize(const char *hostname);
gboolean nex_network_validate(GKeyFile *config, GError **error);
gboolean nex_network_write(GKeyFile *config, const char *target_root, GError **error);
#endif
