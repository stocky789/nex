/* SPDX-License-Identifier: AGPL-3.0-only
 * Validate the local wizard handoff and prepare existing Debian installer inputs.
 * Never partitions disks. Netinst activates the selected installer network.
 */
#define _GNU_SOURCE
#include <glib.h>
#include <crypt.h>
#include <fcntl.h>
#include <sys/random.h>
#include <sys/stat.h>
#include <sys/sysmacros.h>
#include <unistd.h>
#include <stdlib.h>
#include <string.h>
#include "nex-network.h"

static void fail(const char *message)
{
    g_printerr("%s\n", message);
    exit(1);
}
static char *value(GKeyFile *config, const char *key)
{
    char *s = g_key_file_get_string(config, "Host", key, NULL);
    return s ? s : g_strdup("");
}
static char *read_text(const char *path)
{
    char *s = NULL;
    if (!g_file_get_contents(path, &s, NULL, NULL)) return g_strdup("");
    return g_strstrip(s);
}
/* The selected disks, one list entry each; a single disk is a one-entry list. */
static char **disk_list(GKeyFile *config, const char *key, gsize *count)
{
    char **entries = g_key_file_get_string_list(config, "Host", key, count, NULL);
    if (!entries) { *count = 0; entries = g_new0(char *, 1); }
    return entries;
}
/* Whole-disk erase safety and last-moment identity of one selected disk. */
static guint64 validate_disk(const char *disk, const char *size, const char *serial, const char *dev)
{
    struct stat st;
    if (!g_str_has_prefix(disk,"/dev/") || !nex_install_disk_name_supported(disk+5))
        fail("Select a supported installation disk.");
    if (stat(disk,&st) || !S_ISBLK(st.st_mode)) fail("The selected disk is no longer available.");
    char *base = g_strdup_printf("/sys/class/block/%s",disk+5);
    char *path = g_strdup_printf("%s/partition",base);
    if (g_file_test(path,G_FILE_TEST_EXISTS)) fail("Select a whole disk, not a partition.");
    g_free(path); path=g_strdup_printf("%s/ro",base);
    char *actual=read_text(path);
    if (strcmp(actual,"0")) fail("The selected disk is read-only.");
    g_free(actual);g_free(path);path=g_strdup_printf("%s/removable",base);actual=read_text(path);
    if (strcmp(actual,"0")) fail("Removable media cannot be the GUI installation target.");
    g_free(actual);g_free(path);path=g_strdup_printf("%s/size",base);actual=read_text(path);
    guint64 bytes=g_ascii_strtoull(actual,NULL,10)*512;
    char *expected_size=g_strdup_printf("%" G_GUINT64_FORMAT,bytes);
    if (bytes < 16ULL*1024*1024*1024 || strcmp(expected_size,size)) fail("Disk capacity changed or is below 16 GiB; select the disk again.");
    g_free(actual);g_free(path);path=g_strdup_printf("%s/dev",base);actual=read_text(path);
    if (strcmp(actual,dev)) fail("Disk identity changed; select the disk again.");
    g_free(actual);actual=nex_install_disk_serial(base);
    if (strcmp(actual,serial)) fail("Disk serial changed; select the disk again.");
    /* Never select the parent disk that supplies mounted installation media. */
    char *mounts=read_text("/proc/mounts");
    char **lines=g_strsplit(mounts,"\n",-1);
    for (int i=0;lines[i];i++) {
        char **fields=g_strsplit(lines[i]," ",3);
        if (fields[0] && fields[1] && !strcmp(fields[1],"/cdrom")) {
            struct stat media;
            if (!stat(fields[0],&media) && S_ISBLK(media.st_mode)) {
                char *link=g_strdup_printf("/sys/dev/block/%u:%u",major(media.st_rdev),minor(media.st_rdev));
                char *resolved=realpath(link,NULL), *selected=realpath(base,NULL);
                if (resolved && selected && g_str_has_prefix(resolved,selected) &&
                    (!resolved[strlen(selected)] || resolved[strlen(selected)]=='/')) fail("Installation media cannot be erased.");
                free(resolved);free(selected);g_free(link);
            }
        }
        g_strfreev(fields);
    }
    g_free(base);g_free(path);g_free(actual);g_free(expected_size);g_free(mounts);g_strfreev(lines);
    return bytes;
}
static void validate(GKeyFile *config, gboolean password_required)
{
    GError *error = NULL;
    char *hostname = value(config, "hostname"), *zone = value(config, "timezone");
    char *keymap = value(config, "keymap"), *password = value(config, "password");
    gsize disks = 0, sizes = 0, serials = 0, devs = 0;
    char **disk = disk_list(config, "disk", &disks), **size = disk_list(config, "disk_size", &sizes);
    char **serial = disk_list(config, "disk_serial", &serials), **dev = disk_list(config, "disk_major_minor", &devs);
    char *zones = read_text("/usr/share/nex-installer/timezones");
    char *needle = g_strdup_printf("\n%s\n", zone);
    char *haystack = g_strdup_printf("\n%s\n", zones);
    char *normalized_hostname = nex_hostname_normalize(hostname);
    if (!normalized_hostname)
        fail("Use an ASCII host name or FQDN, e.g. nexcore.example.test.");
    g_free(normalized_hostname);
    if (!strstr(haystack, needle) || strchr(zone, '\n') || !zone[0])
        fail("Select a valid timezone from the supplied list.");
    if (strcmp(keymap,"us") && strcmp(keymap,"gb") && strcmp(keymap,"de") && strcmp(keymap,"fr") && strcmp(keymap,"es"))
        fail("Select a supported keyboard layout.");
    if (password_required && (!password[0] || strlen(password) > 512 || strchr(password,'\n') || strchr(password,'\r')))
        fail("Enter a nonempty root password of at most 512 bytes, without line breaks.");
    if (!nex_network_validate(config, &error)) fail(error ? error->message : "Network settings are invalid.");
    if (g_file_test("/cdrom/nex/netinst.tsv", G_FILE_TEST_EXISTS)) {
        char *management = g_key_file_get_string(config, "Network", "management", NULL);
        if (!management || !*management) fail("The net installer requires a management network. Select an adapter or use the offline ISO.");
        g_free(management);
    }
    char **nic_groups=g_key_file_get_groups(config,NULL);
    for (int i=0;nic_groups[i];i++) {
        if (!g_str_has_prefix(nic_groups[i],"NIC:")) continue;
        char *nic_path=g_strdup_printf("/sys/class/net/%s/address",nic_groups[i]+4);
        char *observed=read_text(nic_path);
        char *selected=g_key_file_get_string(config,nic_groups[i],"mac",NULL);
        if (!selected || g_ascii_strcasecmp(observed,selected)) fail("A network adapter changed or disappeared; restart setup and select it again.");
        g_free(nic_path);g_free(observed);g_free(selected);
    }
    g_strfreev(nic_groups);
    if (!disks || disks != sizes || disks != serials || disks != devs)
        fail("Select the disk or disks to erase and install Nex Core onto.");
    guint64 smallest = 0;
    for (gsize i = 0; i < disks; i++) {
        for (gsize j = 0; j < i; j++)
            if (!strcmp(disk[i],disk[j])) fail("Select each mirror disk only once.");
        guint64 bytes = validate_disk(disk[i],size[i],serial[i],dev[i]);
        if (!smallest || bytes < smallest) smallest = bytes;
    }
    /* A mirror is only as large as its smallest member; refuse a badly mismatched pair. */
    for (gsize i = 0; i < disks; i++)
        if (g_ascii_strtoull(size[i],NULL,10) > smallest + smallest / 10)
            fail("Every mirror disk must be within 10% of the smallest selected disk's capacity, because a mirror is only as large as its smallest member. Select disks of similar size.");
    explicit_bzero(password,strlen(password));
    g_free(password);g_free(hostname);g_free(zone);g_free(keymap);
    g_strfreev(disk);g_strfreev(size);g_strfreev(serial);g_strfreev(dev);
    g_free(zones);g_free(needle);g_free(haystack);
}
static void protected_write(const char *path, const char *data)
{
    GError *error=NULL;
    if (!g_file_set_contents_full(path,data,-1,G_FILE_SET_CONTENTS_CONSISTENT|G_FILE_SET_CONTENTS_DURABLE,0600,&error))
        fail("Unable to save the installation settings; no installation was started.");
}
int main(int argc,char **argv)
{
    umask(0077);
    gboolean apply=argc==3 && !strcmp(argv[1],"--apply");
    gboolean recheck=argc==3 && !strcmp(argv[1],"--recheck");
    if (!(apply||recheck||argc==2)) fail("Usage: nex-config [--apply|--recheck] CONFIG");
    const char *path=argv[argc-1];
    struct stat st;
    if (lstat(path,&st)||!S_ISREG(st.st_mode)||st.st_uid!=0||(st.st_mode&0077)) fail("Installation settings must be a private root-owned file.");
    GKeyFile *config=g_key_file_new();
    if (!g_key_file_load_from_file(config,path,G_KEY_FILE_NONE,NULL)) fail("Unable to read installation settings.");
    validate(config,!recheck);
    if (!apply) return 0;
    if (strcmp(path,"/run/nex-installer/config.ini")) fail("Refusing an unexpected installation handoff path.");
    if (g_file_test("/cdrom/nex/netinst.tsv", G_FILE_TEST_EXISTS)) {
        char *management = g_key_file_get_string(config, "Network", "management", NULL);
        char *group = g_strdup_printf("NIC:%s", management);
        char *args[] = { "/usr/lib/nex-installer/netinst-network", management,
            g_key_file_get_string(config, group, "mode", NULL),
            g_key_file_get_string(config, group, "vlan", NULL),
            g_key_file_get_string(config, group, "address", NULL),
            g_key_file_get_string(config, group, "gateway", NULL),
            g_key_file_get_string(config, "Network", "dns", NULL), NULL };
        for (int i = 2; i < 7; i++) if (!args[i]) args[i] = g_strdup("");
        int status = 0;
        /* No shell interpolation; neither command receives the root password. */
        if (!g_spawn_sync(NULL, args, NULL, G_SPAWN_STDOUT_TO_DEV_NULL | G_SPAWN_STDERR_TO_DEV_NULL,
                          NULL, NULL, NULL, NULL, &status, NULL) || !g_spawn_check_wait_status(status, NULL))
            fail("Could not activate the installer network. Check the selected adapter, DHCP or static settings, and VLAN, then retry.");
        for (int i = 1; i < 7; i++) g_free(args[i]);
        g_free(group);
        char *check[] = { "/bin/sh", "/cdrom/nex/netinst-fetch.sh", "--check", NULL };
        if (!g_spawn_sync(NULL, check, NULL, G_SPAWN_STDOUT_TO_DEV_NULL | G_SPAWN_STDERR_TO_DEV_NULL,
                          NULL, NULL, NULL, NULL, &status, NULL) || !g_spawn_check_wait_status(status, NULL))
            fail("Net installer sources are unavailable. Check internet access, DNS and system time, then retry. No disk has been erased.");
    }
    char *password=value(config,"password");
    unsigned char entropy[24];
    if (getrandom(entropy,sizeof entropy,0)!=sizeof entropy) fail("Unable to obtain secure randomness.");
    const char *alphabet="./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
    char salt[32]="$6$";
    for (int i=0;i<16;i++) salt[3+i]=alphabet[entropy[i]&63];
    salt[19]='$';salt[20]=0;
    char *hash=crypt(password,salt);
    if (!hash || hash[0]!='\x24') fail("Unable to protect root credentials.");
    GError *error=NULL;
    if (!nex_network_write(config,"/run/nex-installer/target",&error)) fail(error ? error->message : "Unable to stage network settings.");
    char *hostname=value(config,"hostname"),*zone=value(config,"timezone"),*keymap=value(config,"keymap");
    /* disk_list must stay below the hostname line: installer-hostname.py extracts render from there. */
    gsize disks=0;
    char **disk=disk_list(config,"disk",&disks);
    char *normalized_hostname=nex_hostname_normalize(hostname);
    g_free(hostname); hostname=normalized_hostname;
    g_key_file_set_string(config,"Host","hostname",hostname);
    char **name_parts=g_strsplit(hostname,".",2);
    const char *shortname=name_parts[0], *domain=name_parts[1] ? name_parts[1] : "";
    char *hostfile=g_strdup_printf("%s\n",shortname);
    char *hosts=g_strdup_printf("127.0.0.1 localhost\n127.0.1.1 %s%s%s\n::1 localhost ip6-localhost ip6-loopback\n",hostname,*domain ? " " : "",*domain ? shortname : "");
    protected_write("/run/nex-installer/target/etc/hostname",hostfile);
    protected_write("/run/nex-installer/target/etc/hosts",hosts);
    if (chmod("/run/nex-installer/target/etc/hostname",0644) || chmod("/run/nex-installer/target/etc/hosts",0644)) fail("Unable to set host configuration permissions.");
    g_free(hostfile);g_free(hosts);
    /* One disk keeps the whole-disk ext4 layout; two or more become an md RAID1 mirror of
     * one ext4 root. Every mirror disk gets the same GPT layout on either firmware - a
     * 1 MiB BIOS boot partition, a 512 MiB ESP, then the array member - so the member is
     * always partition 3 and either firmware can boot the host from any of the disks. */
    gboolean uefi=g_file_test("/sys/firmware/efi",G_FILE_TEST_IS_DIR);
    GString *targets=g_string_new(disk[0]), *members=g_string_new("");
    for (gsize i=0;i<disks;i++) {
        const char *name=disk[i];
        if (i) g_string_append_printf(targets," %s",name);
        /* Partition 3 on every member: the mirror recipe below always lays down the
         * BIOS boot partition and the ESP first, whichever firmware is running. */
        g_string_append_printf(members,"%s%s%s3",i ? "#" : "",name,
            g_ascii_isdigit(name[strlen(name)-1]) ? "p" : "");
    }
    GString *seed=g_string_new("");
    g_string_append_printf(seed,
        "d-i debian-installer/locale string en_US.UTF-8\n"
        "d-i keyboard-configuration/xkb-keymap select %s\n"
        "d-i time/zone string %s\n"
        "d-i clock-setup/utc boolean true\n"
        "d-i clock-setup/ntp boolean false\n"
        "d-i netcfg/enable boolean false\n"
        "d-i netcfg/get_hostname string %s\n"
        "d-i netcfg/get_domain string %s\n"
        "d-i passwd/root-login boolean true\n"
        "d-i passwd/make-user boolean false\n"
        "d-i passwd/root-password-crypted password %s\n"
        "d-i partman-auto/disk string %s\n",keymap,zone,shortname,domain,hash,targets->str);
    if (disks > 1)
        g_string_append_printf(seed,
            "d-i partman-auto/method string raid\n"
            /* GPT on both firmwares: grub-install --target=i386-pc needs the BIOS boot
             * partition, which only GPT has, and a fixed label keeps the member index
             * constant instead of following partman's per-disk default. */
            "d-i partman-partitioning/default_label string gpt\n"
            "d-i partman-auto/expert_recipe string nex-mirror :: "
            "1 1 1 free $iflabel{ gpt } method{ biosgrub } . "
            "538 538 538 free $iflabel{ gpt } method{ efi } format{ } . "
            "1000 20000 1000000000 raid $primary{ } method{ raid } .\n"
            "d-i partman-auto-raid/recipe string 1 %u 0 ext4 / %s .\n"
            "d-i partman-md/confirm boolean true\n"
            "d-i partman-md/confirm_nooverwrite boolean true\n"
            /* The mirror holds one ext4 root and no swap; answer partman's warning
             * rather than interrupt the operator with a partitioning question. */
            "d-i partman-basicfilesystems/no_swap boolean false\n"
            /* Every ESP also gets the removable boot path, so the firmware boots
             * whichever disk survives without an NVRAM entry of its own. */
            "d-i grub-installer/force-efi-extra-removable boolean true\n",(unsigned)disks,members->str);
    else
        g_string_append(seed,
            "d-i partman-auto/method string regular\n"
            "d-i partman-auto/choose_recipe select atomic\n");
    g_string_append(seed,
        "d-i partman-partitioning/confirm_write_new_label boolean true\n"
        "d-i partman/choose_partition select finish\n"
        "d-i partman/confirm boolean true\n"
        "d-i partman/confirm_nooverwrite boolean true\n");
    /* Legacy BIOS boots from whichever disk survives, so GRUB goes on every one of
     * them. A UEFI install writes the ESP mounted by partman; late-install copies it
     * to the other members' ESPs, which grub-efi-amd64 does not manage itself. */
    if (!uefi || disks == 1)
        g_string_append_printf(seed,"d-i grub-installer/bootdev string %s\n",targets->str);
    g_string_append(seed,
        "d-i grub-installer/only_debian boolean true\n"
        "d-i finish-install/reboot_in_progress seen false\n");
    protected_write("/run/nex-installer/answers.cfg",seed->str);
    /* Retain only non-secret selections for last-moment disk identity checking. */
    g_key_file_remove_key(config,"Host","password",NULL);
    char *safe=g_key_file_to_data(config,NULL,NULL);
    protected_write(path,safe);
    explicit_bzero(password,strlen(password)); explicit_bzero(hash,strlen(hash));
    explicit_bzero(seed->str,seed->len);
    g_free(safe);g_free(password);g_string_free(seed,TRUE);g_key_file_free(config);
    g_string_free(targets,TRUE);g_string_free(members,TRUE);
    g_strfreev(name_parts);g_free(hostname);g_free(zone);g_free(keymap);g_strfreev(disk);
    return 0;
}
