#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Run actual carrier callback and link probe with simulated GTK/sysfs boundaries."""
from pathlib import Path
import subprocess
import tempfile

source = Path('installer/gui/nex-wizard.c').read_text()
assert 'g_timeout_add_seconds(2, refresh_nic_details, NULL)' in source
callbacks = source[source.index('static const gchar *nic_link_status('):source.index('static void nic_changed(')]
assert '"operstate"' not in callbacks, 'Operational state is not carrier state'
check = r'''
#include <glib.h>
#include <string.h>
#include <assert.h>
static int page=2,selected_nic=0,reads;
static GPtrArray *nics;
static int mode, loading_nic, dhcp_link=-1, dhcp_changes;
#define GTK_COMBO_BOX(x) (x)
static int gtk_combo_box_get_active(int box) { return box; }
static void dhcp_changed(gpointer widget,gpointer unused) { (void)widget;(void)unused;dhcp_changes++; }
static char *nic_details,*rendered;
static const char *flags="0x1003",*carrier="1",*speed="1000";
#define GTK_LABEL(x) (x)
static char *property(const char *root,const char *name,const char *key) {
 assert(!strcmp(root,"/sys/class/net")&&!strcmp(name,"enX0")); reads++;
 if(!strcmp(key,"flags"))return g_strdup(flags);
 if(!strcmp(key,"carrier"))return g_strdup(carrier);
 if(!strcmp(key,"speed"))return g_strdup(speed);
 assert(!strcmp(key,"address"));return g_strdup("52:54:00:12:34:10");
}
static void gtk_label_set_markup(char *widget,const char *text) {
 assert(widget==nic_details);g_free(rendered);rendered=g_strdup(text);
}
''' + callbacks + r'''
int main(void) {
 assert(!strcmp(nic_link_status("0x1003","1"),"Link detected"));
 assert(!strcmp(nic_link_status("0x1003","0"),"No link"));
 const char *badflags[]={"","bad","0x0","0x1002","-1","4294967296"};
 for(unsigned i=0;i<G_N_ELEMENTS(badflags);i++)assert(!strcmp(nic_link_status(badflags[i],"1"),"Link unknown"));
 const char *badcarrier[]={"","2","-1","unknown"};
 for(unsigned i=0;i<G_N_ELEMENTS(badcarrier);i++)assert(!strcmp(nic_link_status("0x1",badcarrier[i]),"Link unknown"));
 nics=g_ptr_array_new();g_ptr_array_add(nics,"enX0");
 assert(refresh_nic_details(NULL));assert(strstr(rendered,"Link detected")&&strstr(rendered,"1000 Mbps"));assert(strstr(rendered,"#21824A"));
 carrier="0";assert(refresh_nic_details(NULL));assert(strstr(rendered,"No link")&&!strstr(rendered,"Mbps"));assert(strstr(rendered,"#65738A")&&!strstr(rendered,"#21824A"));
 carrier="";refresh_nic_details(NULL);assert(strstr(rendered,"Link unknown"));
 flags="0x1002";carrier="1";refresh_nic_details(NULL);assert(strstr(rendered,"Link unknown"));
 flags="0x1003";speed="-1";refresh_nic_details(NULL);assert(strstr(rendered,"Link detected")&&!strstr(rendered,"Mbps"));
 speed="0";refresh_nic_details(NULL);assert(!strstr(rendered,"Mbps"));
 reads=0;page=1;refresh_nic_details(NULL);assert(!reads);
 page=2;selected_nic=-1;refresh_nic_details(NULL);assert(!reads);
 page=2;selected_nic=0;mode=1;carrier="1";refresh_nic_details(NULL);assert(dhcp_changes==1);
 refresh_nic_details(NULL);assert(dhcp_changes==1);
 carrier="0";refresh_nic_details(NULL);assert(dhcp_changes==2);
 g_free(rendered);g_ptr_array_free(nics,TRUE);return 0;
}
'''
prepare = Path('installer/gui/prepare.sh').read_text()
probe = prepare[prepare.index('for interface in /sys/class/net/*; do'):prepare.index('touch /run/nex-installer/prepared')]
with tempfile.TemporaryDirectory() as directory:
    root = Path(directory)
    (root / 'check.c').write_text(check)
    flags = subprocess.check_output(['pkg-config', '--cflags', '--libs', 'glib-2.0'], text=True).split()
    subprocess.run(['cc', '-Wall', '-Wextra', '-Werror', str(root / 'check.c'), '-o', str(root / 'check'), *flags], check=True)
    subprocess.run([str(root / 'check')], check=True)
    for name, up, device in [('connected', False, True), ('failed', False, True), ('suppression-failed', False, True), ('existing', True, True), ('lo', True, False)]:
        interface = root / 'net' / name
        interface.mkdir(parents=True)
        (interface / 'flags').write_text('0x1003\n' if up else '0x1002\n')
        if device:
            (interface / 'device').mkdir()
        ipv6 = root / 'ipv6' / name
        ipv6.mkdir(parents=True)
        if name == 'suppression-failed':
            (ipv6 / 'disable_ipv6').mkdir()  # Opening a directory fails even as root.
        else:
            (ipv6 / 'disable_ipv6').write_text('0\n')
    ip = root / 'ip'
    ip.write_text('''#!/bin/sh
set -eu
[ "$1 $2 $3" = "link set dev" ] && [ "$5" = up ]
[ "$(cat "'''+str(root)+'''/ipv6/$4/disable_ipv6")" = 1 ]
echo "$4" >> "'''+str(root)+'''/enabled"
[ "$4" != failed ]
''')
    ip.chmod(0o755)
    probe = probe.replace('/sys/class/net', str(root / 'net')).replace('/proc/sys/net/ipv6/conf', str(root / 'ipv6')).replace('/sbin/ip', str(ip))
    result = subprocess.run(['sh', '-eu', '-c', probe], check=True, capture_output=True, text=True)
    assert (root / 'enabled').read_text().splitlines() == ['connected', 'failed']
    assert 'Cannot suppress IPv6 on suppression-failed' in result.stdout
    assert 'Cannot enable failed' in result.stdout, 'Probe failure must be reported without blocking offline installation'
    assert (root / 'ipv6/existing/disable_ipv6').read_text() == '0\n', 'Already configured adapter was modified'
print('PASS: carrier/unknown labels, periodic read-only GTK callback, IPv6 suppression before address-free probe, existing NIC preservation and probe failure (simulated boundaries)')
