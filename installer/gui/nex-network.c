/* SPDX-License-Identifier: AGPL-3.0-only */
#include "nex-network.h"
#include <arpa/inet.h>
#include <errno.h>
#include <glib/gstdio.h>
#include <string.h>
#include <unistd.h>
#include <sys/stat.h>

gboolean nex_install_disk_name_supported(const char *name)
{
    if (!name || !*name) return FALSE;
    for (const char *p = name; *p; p++)
        if (!g_ascii_isalnum(*p)) return FALSE;
    /* Partition and device safety checks use sysfs, not spelling alone. */
    return g_str_has_prefix(name, "sd") || g_str_has_prefix(name, "vd") ||
        g_str_has_prefix(name, "xvd") || g_str_has_prefix(name, "nvme") || g_str_has_prefix(name, "mmcblk");
}

/* NVMe reports device/serial and virtio-blk serial. SATA, SAS and SCSI disks only
 * expose the unit-serial VPD page: a 4-byte binary header, then the serial. */
gchar *nex_install_disk_serial(const char *block)
{
    static const struct { const char *attribute; gsize header; } sources[] =
        { { "device/serial", 0 }, { "serial", 0 }, { "device/vpd_pg80", 4 } };
    for (guint i = 0; i < G_N_ELEMENTS(sources); i++) {
        gchar *path = g_build_filename(block, sources[i].attribute, NULL), *data = NULL, *serial;
        gsize length = 0;
        gboolean found = g_file_get_contents(path, &data, &length, NULL);
        g_free(path);
        if (!found) continue;
        serial = length > sources[i].header ? g_strstrip(g_strndup(data + sources[i].header, length - sources[i].header)) : g_strdup("");
        g_free(data);
        if (*serial) return serial;
        g_free(serial);
    }
    return g_strdup("");
}

static char *value(GKeyFile *c, const char *group, const char *key)
{
    char *v = g_key_file_get_string(c, group, key, NULL);
    return v ? v : g_strdup("");
}
static gboolean fail(GError **e, const char *message)
{
    g_set_error_literal(e, G_FILE_ERROR, G_FILE_ERROR_INVAL, message);
    return FALSE;
}
static gboolean ipv4(const char *s, guint32 *out)
{
    struct in_addr a;
    if (inet_pton(AF_INET, s, &a) != 1) return FALSE;
    *out = ntohl(a.s_addr);
    return TRUE;
}
gchar *nex_hostname_normalize(const char *hostname)
{
    if (!hostname || !*hostname) return NULL;
    gsize length = strlen(hostname);
    if (hostname[length - 1] == '.') length--;
    if (!length || length > 253) return NULL;
    const char *label = hostname, *end = hostname + length;
    for (const char *p = hostname; ; p++) {
        if (p == end || *p == '.') {
            if (p == label || p - label > 63 || !g_ascii_isalnum(*label) || !g_ascii_isalnum(p[-1])) return NULL;
            if (p == end) break;
            label = p + 1;
        } else if (!g_ascii_isalnum(*p) && *p != '-') return NULL;
    }
    char *name = g_strndup(hostname, length);
    guint32 address;
    if (ipv4(name, &address)) { g_free(name); return NULL; }
    return name;
}
static gboolean cidr(const char *s, guint32 *ip, guint32 *mask)
{
    if (!g_regex_match_simple("^[0-9.]+/[0-9]+$", s, 0, 0)) return FALSE;
    char **parts = g_strsplit(s, "/", -1), *end;
    gboolean ok = FALSE;
    if (g_strv_length(parts) == 2 && *parts[1]) {
        long bits = strtol(parts[1], &end, 10);
        if (!*end && bits >= 1 && bits <= 32 && ipv4(parts[0], ip)) {
            *mask = 0xffffffffU << (32 - bits);
            ok = *ip != 0 && (*ip >> 24) != 127 && (*ip >> 28) < 14;
            if (bits < 31 && ((*ip & ~*mask) == 0 || (*ip & ~*mask) == ~*mask)) ok = FALSE;
        }
    }
    g_strfreev(parts);
    return ok;
}
gboolean nex_network_validate(GKeyFile *c, GError **e)
{
    char **groups = g_key_file_get_groups(c, NULL);
    char *management = value(c, "Network", "management"), *dns = value(c, "Network", "dns");
    char **servers = g_strsplit_set(dns, " \t", -1);
    GHashTable *ips = g_hash_table_new(g_direct_hash, g_direct_equal);
    GHashTable *macs = g_hash_table_new_full(g_str_hash, g_str_equal, g_free, NULL);
    gboolean found = !*management, ok = TRUE;
    guint32 ip, mask, gateway;
    for (int i = 0; servers[i]; i++) if (*servers[i] && !ipv4(servers[i], &ip)) ok = FALSE;
    for (int i = 0; groups[i] && ok; i++) {
        const char *group = groups[i];
        if (!g_str_has_prefix(group, "NIC:")) continue;
        const char *name = group + 4;
        char *mac = value(c, group, "mac"), *mode = value(c, group, "mode");
        char *address = value(c, group, "address"), *gw = value(c, group, "gateway");
        char *vlan = value(c, group, "vlan"), *end;
        long tag = strtol(vlan, &end, 10);
        gboolean manager = !strcmp(name, management);
        char *normalized = g_ascii_strdown(mac, -1);
        ok = strlen(name) <= 15 && g_regex_match_simple("^[A-Za-z][A-Za-z0-9_-]*$", name, 0, 0)
            && strcmp(name, "lo") && g_regex_match_simple("^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$", mac, 0, 0)
            && !(strtoul(mac, NULL, 16) & 1) && strcmp(normalized, "00:00:00:00:00:00")
            && !g_hash_table_contains(macs, normalized) && *vlan && !*end && tag >= 0 && tag <= 4094;
        g_hash_table_add(macs, normalized);
        if (!strcmp(mode, "disabled")) { if (manager || *address || *gw || tag) ok = FALSE; }
        else if (!strcmp(mode, "dhcp")) { if (*address || *gw) ok = FALSE; }
        else if (!strcmp(mode, "static")) {
            if (!cidr(address, &ip, &mask)) ok = FALSE;
            else {
                if (g_hash_table_contains(ips, GUINT_TO_POINTER(ip))) ok = FALSE;
                g_hash_table_add(ips, GUINT_TO_POINTER(ip));
                if (*gw && (!manager || !ipv4(gw, &gateway) || (gateway & mask) != (ip & mask) || gateway == ip || (mask != 0xfffffffeU && mask != 0xffffffffU && ((gateway & ~mask) == 0 || (gateway & ~mask) == ~mask)))) ok = FALSE;
            }
        } else ok = FALSE;
        if (manager && strcmp(mode, "disabled")) found = TRUE;
        g_free(mac); g_free(mode); g_free(address); g_free(gw); g_free(vlan);
    }
    g_hash_table_destroy(ips); g_hash_table_destroy(macs);
    g_strfreev(groups); g_strfreev(servers); g_free(management); g_free(dns);
    return ok && found ? TRUE : fail(e, "Invalid NIC settings: check management, unique MAC/IP, IPv4 prefix, gateway and VLAN (0-4094).");
}
static gboolean save(const char *root, const char *relative, const char *data, GError **error)
{
    char *path = g_build_filename(root, relative, NULL), *dir = g_path_get_dirname(path);
    gboolean ok = g_mkdir_with_parents(dir, 0755) == 0;
    if (!ok) g_set_error(error, G_FILE_ERROR, g_file_error_from_errno(errno), "Cannot create %s: %s", dir, g_strerror(errno));
    /* These are public OS configuration files, even when the caller protects
     * its private handoff with umask 0077. Never chmod the staging root. */
    if (ok) {
        char *relative_dir = g_path_get_dirname(relative);
        char **parts = g_strsplit(relative_dir, "/", -1);
        char *current = g_strdup(root);
        for (int i = 0; parts[i] && ok; i++) {
            char *next = g_build_filename(current, parts[i], NULL);
            g_free(current); current = next;
            if (g_chmod(current, 0755)) {
                g_set_error(error, G_FILE_ERROR, g_file_error_from_errno(errno), "Cannot set directory permissions on %s: %s", current, g_strerror(errno));
                ok = FALSE;
            }
        }
        g_free(current); g_strfreev(parts); g_free(relative_dir);
    }
    if (ok) ok = g_file_set_contents(path, data, -1, error);
    if (ok && g_chmod(path, 0644)) {
        g_set_error(error, G_FILE_ERROR, g_file_error_from_errno(errno), "Cannot set file permissions on %s: %s", path, g_strerror(errno));
        ok = FALSE;
    }
    g_free(path); g_free(dir);
    return ok;
}
gboolean nex_network_write(GKeyFile *c, const char *root, GError **error)
{
    if (!root || !g_path_is_absolute(root)) return fail(error, "An explicit target filesystem is required.");
    char *canonical = g_canonicalize_filename(root, NULL);
    gboolean host_root = !strcmp(canonical, "/");
    g_free(canonical);
    if (host_root) return fail(error, "An explicit target filesystem is required.");
    if (!nex_network_validate(c, error)) return FALSE;
    char **groups = g_key_file_get_groups(c, NULL);
    GString *dhcpcd = g_string_new("# Nex Core: only management DHCP owns routes and DNS.\n");
    GString *interfaces = g_string_new("# Nex Core: interface identity is bound by MAC in /etc/systemd/network.\n# Nex Core agent adds guest VLAN bridges under interfaces.d.\nsource /etc/network/interfaces.d/*\n\nauto lo\niface lo inet loopback\n");
    char *management = value(c, "Network", "management");
    char *dns_override = value(c, "Network", "dns");
    gboolean custom_dns = *g_strstrip(dns_override) != 0;
    gboolean ok = TRUE, dhcp = FALSE;
    int index = 0;
    for (int i = 0; groups[i] && ok; i++) {
        if (!g_str_has_prefix(groups[i], "NIC:")) continue;
        char *mac = value(c, groups[i], "mac"), *mode = value(c, groups[i], "mode");
        char *address = value(c, groups[i], "address"), *gateway = value(c, groups[i], "gateway");
        int tag = g_key_file_get_integer(c, groups[i], "vlan", NULL);
        char *name = g_strdup_printf("nex%d", index++);
        char *port = tag ? g_strdup_printf("%s.%d", name, tag) : g_strdup(name);
        gboolean manager = !strcmp(groups[i] + 4, management);
        const char *logical = manager ? "br0" : port;
        char *linkpath = g_strdup_printf("etc/systemd/network/10-%s.link", name);
        char *link = g_strdup_printf("[Match]\nMACAddress=%s\n\n[Link]\nNamePolicy=\nName=%s\n", mac, name);
        ok = save(root, linkpath, link, error);
        g_string_append_printf(interfaces, "\n# %s (%s) -> %s\n", groups[i] + 4, mac, name);
        if (tag || manager) g_string_append_printf(interfaces, "iface %s inet manual\n", name);
        if (tag && manager) g_string_append_printf(interfaces, "iface %s inet manual\n    vlan-raw-device %s\n", port, name);
        if (!strcmp(mode, "disabled")) g_string_append_printf(interfaces, "iface %s inet manual\n", name);
        else {
            g_string_append_printf(interfaces, "auto %s\niface %s inet %s\n", logical, logical, mode);
            if (!strcmp(mode, "static")) {
                g_string_append_printf(interfaces, "    address %s\n", address);
                if (*gateway) g_string_append_printf(interfaces, "    gateway %s\n", gateway);
            } else {
                if (manager) {
                    dhcp = TRUE;
                    if (custom_dns) g_string_append_printf(dhcpcd, "\ninterface %s\n    nohook resolv.conf\n", logical);
                } else g_string_append_printf(dhcpcd, "\ninterface %s\n    nogateway\n    nohook resolv.conf\n", logical);
            }
            if (manager) g_string_append_printf(interfaces, "    bridge_ports %s\n    bridge_stp off\n    bridge_fd 0\n    bridge_hw %s\n", port, mac);
        }
        g_free(mac); g_free(mode); g_free(address); g_free(gateway); g_free(name); g_free(port); g_free(linkpath); g_free(link);
    }
    if (ok) ok = save(root, "etc/network/interfaces", interfaces->str, error);
    if (ok) ok = save(root, "etc/dhcpcd.conf", dhcpcd->str, error);
    if (ok && (!dhcp || custom_dns)) {
        char *dns = value(c, "Network", "dns");
        char **servers = g_strsplit_set(dns, " \t", -1);
        GString *resolv = g_string_new("# Nex Core installer DNS\n");
        for (int i = 0; servers[i]; i++) if (*servers[i]) g_string_append_printf(resolv, "nameserver %s\n", servers[i]);
        ok = save(root, "etc/resolv.conf", resolv->str, error);
        g_strfreev(servers); g_free(dns); g_string_free(resolv, TRUE);
    }
    g_strfreev(groups); g_free(management); g_free(dns_override); g_string_free(interfaces, TRUE); g_string_free(dhcpcd, TRUE);
    return ok;
}
