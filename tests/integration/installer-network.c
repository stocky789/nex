/* SPDX-License-Identifier: AGPL-3.0-only */
/* cc -Wall -Wextra -Werror tests/integration/installer-network.c installer/gui/nex-network.c $(pkg-config --cflags --libs glib-2.0) -o /tmp/nex-network-test && /tmp/nex-network-test */
#include "../../installer/gui/nex-network.h"
#include <glib/gstdio.h>
#include <string.h>
#include <sys/stat.h>
static void invalid(GKeyFile *c, const char *group, const char *key, const char *value)
{
    char *old = g_key_file_get_string(c, group, key, NULL);
    GError *error = NULL;
    g_key_file_set_string(c, group, key, value);
    g_assert_false(nex_network_validate(c, &error));
    g_assert_nonnull(error);
    g_clear_error(&error);
    g_key_file_set_string(c, group, key, old);
    g_free(old);
}
static void hostname_cases(void)
{
    const char *valid[] = { "nexcore.example.test", "7nex-core.example.test", "NexCore.Example.Test",
        "7", "123.example", "host.123", "xn--bcher-kva.example", "a", "a-b", NULL };
    const char *invalid_names[] = { "", ".", "..", ".nex", "nex..home", "nex.home..",
        "-nex", "nex-", "nex.-home", "nex.home-", "nex_home", "nex home",
        "nex\nhome", "nex\rhome", "nex/home", "nex;reboot", "nex\thome",
        "192.0.2.1", "192.0.2.1.", "2001:db8::1", "bücher.example", NULL };
    for (int i=0;valid[i];i++) {
        char *name=nex_hostname_normalize(valid[i]);
        g_assert_cmpstr(name,==,valid[i]);g_free(name);
        char *absolute=g_strconcat(valid[i],".",NULL);
        name=nex_hostname_normalize(absolute);
        g_assert_cmpstr(name,==,valid[i]);g_free(name);g_free(absolute);
    }
    g_assert_null(nex_hostname_normalize(NULL));
    for (int i=0;invalid_names[i];i++) g_assert_null(nex_hostname_normalize(invalid_names[i]));
    char *label63=g_strnfill(63,'a'), *label64=g_strnfill(64,'a');
    char *name=nex_hostname_normalize(label63);
    g_assert_cmpstr(name,==,label63);g_free(name);
    g_assert_null(nex_hostname_normalize(label64));
    char *last61=g_strnfill(61,'b');
    char *max=g_strdup_printf("%s.%s.%s.%s",label63,label63,label63,last61);
    g_assert_cmpuint(strlen(max),==,253);
    name=nex_hostname_normalize(max);g_assert_cmpstr(name,==,max);g_free(name);
    char *absolute=g_strconcat(max,".",NULL);
    name=nex_hostname_normalize(absolute);g_assert_cmpstr(name,==,max);g_free(name);
    char *too_long=g_strconcat(max,"b",NULL);
    g_assert_cmpuint(strlen(too_long),==,254);g_assert_null(nex_hostname_normalize(too_long));
    char *long_absolute=g_strconcat(too_long,".",NULL);
    g_assert_null(nex_hostname_normalize(long_absolute));
    g_free(long_absolute);g_free(too_long);g_free(absolute);g_free(max);g_free(last61);g_free(label64);g_free(label63);
}
static void management_bridge_cases(void)
{
    GKeyFile *c = g_key_file_new();
    GError *e = NULL;
    char *root = g_dir_make_tmp("nex-bridge-XXXXXX", &e);
    char *path = g_build_filename(root, "etc/network/interfaces", NULL);
    /* Select the second discovered NIC, to catch accidental first-NIC binding. */
    g_assert_true(g_key_file_load_from_data(c,
        "[Network]\nmanagement=eth1\ndns=1.1.1.1\n"
        "[NIC:eth0]\nmac=52:54:00:12:34:01\nmode=dhcp\naddress=\ngateway=\nvlan=0\n"
        "[NIC:eth1]\nmac=52:54:00:12:34:02\nmode=static\naddress=192.0.2.2/24\ngateway=192.0.2.1\nvlan=0\n", -1, 0, &e));
    for (int dhcp = 0; dhcp < 2; dhcp++) {
        g_key_file_set_string(c, "NIC:eth1", "mode", dhcp ? "dhcp" : "static");
        g_key_file_set_string(c, "NIC:eth1", "address", dhcp ? "" : "192.0.2.2/24");
        g_key_file_set_string(c, "NIC:eth1", "gateway", dhcp ? "" : "192.0.2.1");
        for (int tagged = 0; tagged < 2; tagged++) {
            g_key_file_set_integer(c, "NIC:eth1", "vlan", tagged ? 42 : 0);
            g_assert_true(nex_network_write(c, root, &e));
            char *contents;
            g_assert_true(g_file_get_contents(path, &contents, NULL, &e));
            g_assert_nonnull(strstr(contents, "auto nex0\niface nex0 inet dhcp"));
            g_assert_nonnull(strstr(contents, "iface nex1 inet manual\n"));
            g_assert_nonnull(strstr(contents, dhcp ? "auto br0\niface br0 inet dhcp\n" :
                "auto br0\niface br0 inet static\n    address 192.0.2.2/24\n    gateway 192.0.2.1\n"));
            g_assert_nonnull(strstr(contents, tagged ? "    bridge_ports nex1.42\n" : "    bridge_ports nex1\n"));
            g_assert_nonnull(strstr(contents, "    bridge_hw 52:54:00:12:34:02\n"));
            g_assert_null(strstr(contents, "auto nex1"));
            if (tagged) g_assert_nonnull(strstr(contents, "iface nex1.42 inet manual\n    vlan-raw-device nex1\n"));
            g_free(contents);
        }
    }
    g_key_file_set_string(c, "Network", "management", "");
    g_key_file_set_string(c, "NIC:eth0", "mode", "disabled");
    g_key_file_set_string(c, "NIC:eth1", "mode", "disabled");
    g_key_file_set_integer(c, "NIC:eth1", "vlan", 0);
    g_assert_true(nex_network_write(c, root, &e));
    char *contents;
    g_assert_true(g_file_get_contents(path, &contents, NULL, &e));
    g_assert_null(strstr(contents, "br0"));
    g_assert_null(strstr(contents, "bridge_"));
    g_assert_null(strstr(contents, "auto nex"));
    g_free(contents); g_free(path); g_free(root); g_key_file_free(c);
}
int main(void)
{
    hostname_cases();
    management_bridge_cases();
    umask(0077);
    GKeyFile *c = g_key_file_new();
    GError *e = NULL;
    g_assert_true(g_key_file_load_from_data(c,
        "[Network]\nmanagement=enp1s0\ndns=192.0.2.1 1.1.1.1\n"
        "[NIC:enp1s0]\nmac=52:54:00:12:34:01\nmode=static\naddress=192.0.2.2/24\ngateway=192.0.2.1\nvlan=42\n"
        "[NIC:enp2s0]\nmac=52:54:00:12:34:02\nmode=dhcp\naddress=\ngateway=\nvlan=0\n", -1, 0, &e));
    g_assert_true(nex_network_validate(c, &e));
    invalid(c, "Network", "management", "missing");
    invalid(c, "Network", "dns", "8.8.8.8\npost-up reboot");
    invalid(c, "NIC:enp1s0", "address", "192.0.2.2/33");
    invalid(c, "NIC:enp1s0", "address", "192.0.2.0/24");
    invalid(c, "NIC:enp1s0", "gateway", "192.0.3.1");
    invalid(c, "NIC:enp1s0", "gateway", "192.0.2.255");
    invalid(c, "NIC:enp1s0", "mac", "01:00:00:00:00:01");
    invalid(c, "NIC:enp1s0", "vlan", "4095");
    invalid(c, "NIC:enp1s0", "vlan", "42;reboot");
    invalid(c, "NIC:enp2s0", "mac", "52:54:00:12:34:01");
    invalid(c, "NIC:enp2s0", "gateway", "192.0.2.1");
    g_assert_false(nex_network_write(c, "/", &e)); g_clear_error(&e);
    char *root = g_dir_make_tmp("nex-network-XXXXXX", &e), *path, *contents;
    g_assert_true(nex_network_write(c, root, &e));
    {
        const char *dirs[] = { "etc", "etc/network", "etc/systemd", "etc/systemd/network", NULL };
        struct stat st;
        for (int i = 0; dirs[i]; i++) {
            char *p = g_build_filename(root, dirs[i], NULL);
            g_assert_cmpint(stat(p, &st), ==, 0);
            g_assert_cmpint(st.st_mode & 0777, ==, 0755);
            g_free(p);
        }
        g_assert_cmpint(stat(root, &st), ==, 0);
        g_assert_cmpint(st.st_mode & 0777, ==, 0700);
        const char *files[] = { "etc/network/interfaces", "etc/resolv.conf", "etc/dhcpcd.conf", "etc/systemd/network/10-nex0.link", NULL };
        for (int i = 0; files[i]; i++) {
            char *p = g_build_filename(root, files[i], NULL);
            g_assert_cmpint(stat(p, &st), ==, 0);
            g_assert_cmpint(st.st_mode & 0777, ==, 0644);
            g_free(p);
        }
    }
    path = g_build_filename(root, "etc/network/interfaces", NULL);
    g_assert_true(g_file_get_contents(path, &contents, NULL, &e));
    g_assert_nonnull(strstr(contents, "iface nex0.42 inet manual\n    vlan-raw-device nex0"));
    g_assert_nonnull(strstr(contents, "auto br0\niface br0 inet static\n    address 192.0.2.2/24\n    gateway 192.0.2.1"));
    g_assert_nonnull(strstr(contents, "    bridge_ports nex0.42\n    bridge_stp off\n    bridge_fd 0\n    bridge_hw 52:54:00:12:34:01"));
    g_assert_null(strstr(contents, "auto nex0"));
    g_assert_nonnull(strstr(contents, "iface nex1 inet dhcp"));
    g_free(contents); g_free(path);
    path = g_build_filename(root, "etc/dhcpcd.conf", NULL);
    g_assert_true(g_file_get_contents(path, &contents, NULL, &e));
    g_assert_nonnull(strstr(contents, "interface nex1\n    nogateway\n    nohook resolv.conf"));
    g_free(contents); g_free(path);
    path = g_build_filename(root, "etc/systemd/network/10-nex0.link", NULL);
    g_assert_true(g_file_get_contents(path, &contents, NULL, &e));
    g_assert_nonnull(strstr(contents, "MACAddress=52:54:00:12:34:01"));
    g_free(contents); g_free(path);
    g_key_file_set_string(c, "NIC:enp2s0", "mode", "static");
    g_key_file_set_string(c, "NIC:enp2s0", "address", "192.0.2.2/24");
    g_assert_false(nex_network_validate(c, &e)); g_clear_error(&e);
    g_key_file_set_string(c, "NIC:enp2s0", "address", "198.51.100.2/24");
    g_assert_true(nex_network_validate(c, &e));
    g_key_file_set_string(c, "NIC:enp1s0", "mode", "dhcp");
    g_key_file_set_string(c, "NIC:enp1s0", "address", "");
    g_key_file_set_string(c, "NIC:enp1s0", "gateway", "");
    g_assert_true(nex_network_validate(c, &e));
    g_assert_true(nex_network_write(c, root, &e));
    path = g_build_filename(root, "etc/dhcpcd.conf", NULL);
    g_assert_true(g_file_get_contents(path, &contents, NULL, &e));
    g_assert_nonnull(strstr(contents, "interface br0\n    nohook resolv.conf"));
    g_assert_null(strstr(contents, "nogateway"));
    g_free(contents); g_free(path);
    path = g_build_filename(root, "etc/resolv.conf", NULL);
    g_assert_true(g_file_get_contents(path, &contents, NULL, &e));
    g_assert_nonnull(strstr(contents, "nameserver 192.0.2.1\nnameserver 1.1.1.1"));
    g_free(contents); g_free(path);
    g_assert_false(nex_network_write(c, "/tmp/..", &e)); g_clear_error(&e);
    g_assert_false(nex_network_write(c, "/proc/nex-network-test", &e)); g_assert_nonnull(e); g_clear_error(&e);
    g_print("PASS: hostname RFC boundaries and normalization, NIC validation, management bridge static/DHCP/tagged/untagged/offline, MAC binding, secondary DHCP policy; output %s\n", root);
    g_free(root); g_key_file_free(c);
    return 0;
}
