// SPDX-License-Identifier: AGPL-3.0-only
#define _GNU_SOURCE
#include <blkid/blkid.h>
#include <iscsi/iscsi.h>
#include <iscsi/scsi-lowlevel.h>
#include <errno.h>
#include <fcntl.h>
#include <inttypes.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/prctl.h>
#include <sys/random.h>
#include <sys/resource.h>
#include <sys/syscall.h>
#include <unistd.h>

#define MAX_LUNS 64
#define MAX_PARTITIONS 32
#define READ_LIMIT (16U * 1024U * 1024U)
#define READ_CHUNK (1024U * 1024U)
#define CAPABILITY "storage-iscsi-inspect-v1"

static struct iscsi_context *connection;
static int probe_fd = -1, probe_lun, probe_failed;
static uint32_t block_size;
static uint64_t media_size, probe_bytes;
static unsigned int probe_reads;
static ssize_t (*probe_reader)(void *, size_t, uint64_t);
static struct scsi_task *(*test_unit_ready)(struct iscsi_context *, int) = iscsi_testunitready_sync;

static uint64_t be_number(const unsigned char *p, size_t n)
{
    uint64_t value = 0;
    for (size_t i = 0; i < n; i++) value = (value << 8) | p[i];
    return value;
}

static int task_ok(struct scsi_task *task, size_t minimum)
{
    return task && task->status == SCSI_STATUS_GOOD && task->datain.size >= 0 &&
           (size_t)task->datain.size >= minimum && task->datain.data;
}

static int lun_ready(int lun)
{
    // Fresh sessions can report pending UNIT ATTENTIONs. Consume them with
    // read-only TUR, matching libiscsi's bounded full-connect initialization.
    for (unsigned int attention = 0; attention <= 10; attention++) {
        struct scsi_task *task = test_unit_ready(connection, lun);
        int ready = task && task->status == SCSI_STATUS_GOOD;
        int unit_attention = task && task->status == SCSI_STATUS_CHECK_CONDITION &&
                             task->sense.key == SCSI_SENSE_UNIT_ATTENTION;
        scsi_free_scsi_task(task);
        if (ready) return 1;
        if (!unit_attention) return 0;
    }
    return 0;
}

static ssize_t iscsi_read_range(void *buffer, size_t count, uint64_t offset)
{
    uint64_t start = offset / block_size * block_size;
    size_t skip = (size_t)(offset - start);
    size_t length = (skip + count + block_size - 1) / block_size * block_size;
    struct scsi_task *task = iscsi_read16_sync(connection, probe_lun,
        start / block_size, (uint32_t)length, (int)block_size, 0, 0, 0, 0, 0);
    if (!task_ok(task, length) || (size_t)task->datain.size != length) {
        scsi_free_scsi_task(task);
        errno = EIO;
        return -1;
    }
    memcpy(buffer, task->datain.data + skip, count);
    scsi_free_scsi_task(task);
    return (ssize_t)count;
}

/* libblkid 2.41 has no supported custom-I/O API. Only the private, size-only memfd
 * is interposed; it never supplies sparse zeroes as evidence. Other descriptors use
 * normal Linux syscalls. The runtime selfcheck refuses a library that bypasses this
 * adapter. New libblkid releases need their supported VFS API, not relaxed checks. */
static ssize_t probe_read(void *buffer, size_t count, off_t offset)
{
    probe_reads++;
    if (probe_failed || offset < 0 || (uint64_t)offset > media_size) goto fail;
    if (count > media_size - (uint64_t)offset) count = (size_t)(media_size - (uint64_t)offset);
    if (count == 0) return 0;
    uint64_t start = (uint64_t)offset / block_size * block_size;
    uint64_t end = ((uint64_t)offset + count + block_size - 1) / block_size * block_size;
    if (count > READ_CHUNK || end - start > READ_CHUNK || end > media_size ||
        end - start > READ_LIMIT - probe_bytes) goto fail;
    probe_bytes += end - start;
    ssize_t result = probe_reader(buffer, count, (uint64_t)offset);
    if (result != (ssize_t)count) goto fail;
    return result;
fail:
    probe_failed = 1;
    errno = EIO;
    return -1;
}

ssize_t read(int fd, void *buffer, size_t count)
{
    if (fd != probe_fd || probe_fd < 0) return syscall(SYS_read, fd, buffer, count);
    off_t offset = syscall(SYS_lseek, fd, 0, SEEK_CUR);
    ssize_t result = probe_read(buffer, count, offset);
    if (result > 0 && syscall(SYS_lseek, fd, result, SEEK_CUR) < 0) return -1;
    return result;
}

ssize_t pread(int fd, void *buffer, size_t count, off_t offset)
{
    if (fd != probe_fd || probe_fd < 0) return syscall(SYS_pread64, fd, buffer, count, offset);
    return probe_read(buffer, count, offset);
}

ssize_t pread64(int fd, void *buffer, size_t count, off64_t offset)
{
    return pread(fd, buffer, count, offset);
}

static int open_probe(uint64_t size)
{
    probe_fd = memfd_create("nex-iscsi-probe", MFD_CLOEXEC | MFD_ALLOW_SEALING);
    if (probe_fd < 0) return -1;
    if (ftruncate(probe_fd, (off_t)size) ||
        fcntl(probe_fd, F_ADD_SEALS, F_SEAL_WRITE | F_SEAL_GROW | F_SEAL_SHRINK | F_SEAL_SEAL) < 0) {
        close(probe_fd);
        probe_fd = -1;
        return -1;
    }
    media_size = size;
    probe_bytes = 0;
    probe_reads = 0;
    probe_failed = 0;
    return 0;
}

static void close_probe(void)
{
    int fd = probe_fd;
    probe_fd = -1;
    if (fd >= 0) close(fd);
}

static ssize_t check_reader(void *buffer, size_t count, uint64_t offset)
{
    (void)buffer; (void)count; (void)offset;
    errno = EIO;
    return -1;
}

static int adapter_supported(void)
{
    const char *version = NULL;
    blkid_get_library_version(&version, NULL);
    if (!version || strncmp(version, "2.41.", 5)) return 0;
    block_size = 512;
    probe_reader = check_reader;
    if (open_probe(1024 * 1024)) return 0;
    blkid_probe probe = blkid_new_probe();
    if (probe && !blkid_probe_set_device(probe, probe_fd, 8192, 512 * 1024)) {
        blkid_probe_enable_partitions(probe, 1);
        (void)blkid_do_safeprobe(probe);
    }
    int supported = probe_reads > 0 && probe_failed;
    blkid_free_probe(probe);
    close_probe();
    probe_reader = iscsi_read_range;
    return supported;
}

/* A UUID is shown to an operator and later registered, so only a non-empty run of
 * visible ASCII is accepted; anything else is treated as absent for that LUN. */
static int printable_token(const char *text)
{
    if (!*text) return 0;
    for (const unsigned char *p = (const unsigned char *)text; *p; p++)
        if (*p < 33 || *p > 126) return 0;
    return 1;
}

struct observation {
    uint64_t lun, size;
    char wwid[65], uuid[256], type[64], error[512];
};

static void reason(struct observation *o, const char *text)
{
    size_t used = strlen(o->error);
    snprintf(o->error + used, sizeof(o->error) - used, "%s%s", used ? "; " : "", text);
}

/* Validate every descriptor, including those not selected for identity. Only
 * logical-unit NAA binary identifiers match the native block WWN contract. */
static int vpd_wwid(const unsigned char *data, size_t size, char *wwid)
{
    if (size < 4 || data[1] != 0x83 || (data[0] & 31) != 0) return -1;
    size_t end = 4 + (size_t)be_number(data + 2, 2);
    if (end > size || end > 4096) return -1;
    int best = 0, ambiguous = 0;
    for (size_t at = 4; at < end;) {
        if (end - at < 4) return -1;
        const unsigned char *d = data + at;
        size_t length = d[3];
        if (!length || length > end - at - 4) return -1;
        int type = d[1] & 15, association = (d[1] >> 4) & 3;
        if (association == 0 && (d[0] & 15) == 1 && type == 3) {
            int naa = d[4] >> 4;
            if (!((length == 8 && (naa == 2 || naa == 3 || naa == 5)) ||
                  (length == 16 && naa == 6))) return -1;
            // Match the native scsi_id WWN preference, not descriptor order.
            int priority = naa == 6 ? 3 : naa == 5 ? 2 : 1;
            char value[65];
            for (size_t j = 0; j < length; j++) snprintf(value + j * 2, 3, "%02x", d[4 + j]);
            if (priority == best && strcmp(wwid, value)) ambiguous = 1;
            if (priority > best) { strcpy(wwid, value); best = priority; ambiguous = 0; }
        }
        at += 4 + length;
    }
    if (ambiguous) return -1;
    return best ? 0 : 1;
}

static int report_luns(const unsigned char *data, size_t size, uint64_t *luns, size_t *count)
{
    if (size < 8) return -1;
    uint64_t bytes = be_number(data, 4);
    if (bytes % 8 || bytes > MAX_LUNS * 8 || bytes > size - 8) return -1;
    *count = (size_t)bytes / 8;
    for (size_t i = 0; i < *count; i++) {
        const unsigned char *d = data + 8 + i * 8;
        if (be_number(d + 2, 6) || ((d[0] >> 6) != 0 && (d[0] >> 6) != 1) ||
            ((d[0] >> 6) == 0 && d[0] != 0)) return -1;
        // Linux's HCTL LUN and libiscsi's caller LUN preserve the addressing bits.
        luns[i] = be_number(d, 2);
        for (size_t j = 0; j < i; j++) if (luns[j] == luns[i]) return -1;
    }
    return 0;
}

static int probe_filesystem(uint64_t offset, uint64_t size, struct observation *found)
{
    blkid_probe probe = blkid_new_probe();
    if (!probe) return -1;
    int result = -1;
    if (blkid_probe_set_device(probe, probe_fd, (blkid_loff_t)offset, (blkid_loff_t)size) ||
        blkid_probe_set_sectorsize(probe, block_size) ||
        blkid_probe_enable_partitions(probe, 0) ||
        blkid_probe_set_superblocks_flags(probe, BLKID_SUBLKS_TYPE | BLKID_SUBLKS_UUID)) goto done;
    result = blkid_do_safeprobe(probe);
    if (result == 0) {
        const char *type = NULL, *uuid = NULL;
        size_t length = 0;
        if (blkid_probe_lookup_value(probe, "TYPE", &type, &length) || !length || length > sizeof(found->type)) {
            result = -1;
            goto done;
        }
        snprintf(found->type, sizeof(found->type), "%s", type);
        if (!blkid_probe_lookup_value(probe, "UUID", &uuid, &length) && length <= sizeof(found->uuid))
            snprintf(found->uuid, sizeof(found->uuid), "%s", uuid);
    }
done:
    blkid_free_probe(probe);
    return result;
}

static void inspect_filesystems(struct observation *o)
{
    if (open_probe(o->size)) { reason(o, "Native filesystem probe is unavailable"); return; }
    blkid_probe table = blkid_new_probe();
    int incomplete = 0, ambiguous = 0, found_count = 0;
    struct observation found = {0};
    int result = probe_filesystem(0, o->size, &found);
    if (result == 0) found_count++;
    else if (result == -2) ambiguous = 1;
    else if (result < 0) incomplete = 1;
    if (!table || blkid_probe_set_device(table, probe_fd, 0, (blkid_loff_t)o->size) ||
        blkid_probe_set_sectorsize(table, block_size) ||
        blkid_probe_enable_superblocks(table, 0) || blkid_probe_enable_partitions(table, 1) ||
        blkid_do_fullprobe(table) < 0) incomplete = 1;
    if (!incomplete && table) {
        blkid_partlist parts = blkid_probe_get_partitions(table);
        int count = parts ? blkid_partlist_numof_partitions(parts) : 0;
        if (count < 0 || count > MAX_PARTITIONS) incomplete = 1;
        for (int i = 0; i < count && !incomplete; i++) {
            blkid_partition part = blkid_partlist_get_partition(parts, i);
            if (blkid_partition_is_extended(part)) continue;
            blkid_loff_t start = blkid_partition_get_start(part), sectors = blkid_partition_get_size(part);
            if (start < 0 || sectors <= 0 || (uint64_t)start > o->size / 512 ||
                (uint64_t)sectors > o->size / 512 - (uint64_t)start) { incomplete = 1; break; }
            struct observation candidate = {0};
            result = probe_filesystem((uint64_t)start * 512, (uint64_t)sectors * 512, &candidate);
            if (result == 0) { found_count++; found = candidate; }
            else if (result == -2) ambiguous = 1;
            else if (result < 0) incomplete = 1;
        }
    }
    if (probe_failed || incomplete || probe_reads == 0)
        reason(o, "Filesystem inspection incomplete: unreadable metadata or bounded probe limit");
    else if (ambiguous || found_count > 1)
        reason(o, "Multiple or ambiguous filesystem signatures; select identity manually");
    else if (found_count == 0)
        reason(o, "No filesystem signature detected: blank or unsupported media");
    else {
        strcpy(o->type, found.type);
        strcpy(o->uuid, found.uuid);
        if (o->uuid[0] && !printable_token(o->uuid)) {
            o->uuid[0] = 0;
            reason(o, "Detected filesystem UUID is not printable ASCII");
        } else if (!o->uuid[0]) reason(o, "Detected filesystem has no UUID");
        if (strcmp(o->type, "ext4") && strcmp(o->type, "xfs") && strcmp(o->type, "gfs2"))
            reason(o, "Detected filesystem is unsupported for iSCSI repositories");
    }
    blkid_free_probe(table);
    close_probe();
}

static int inspect_lun(struct observation *o)
{
    probe_lun = (int)o->lun;
    int ready = lun_ready(probe_lun);
    struct scsi_task *task = iscsi_inquiry_sync(connection, probe_lun, 0, 0, 96);
    if (task_ok(task, 36) && (task->datain.data[0] >> 5) == 0 &&
        (task->datain.data[0] & 31) != 0 && (task->datain.data[0] & 31) != 14 &&
        (task->datain.data[0] & 31) != 20) {
        scsi_free_scsi_task(task);
        return 0; // Positively identified controller/CD/tape, not a block-storage LUN.
    }
    scsi_free_scsi_task(task);
    if (!ready) {
        reason(o, "LUN is not ready or has persistent unit attention");
        return 1;
    }
    task = iscsi_inquiry_sync(connection, probe_lun, 1, 0x83, 4096);
    if (!task_ok(task, 4) || vpd_wwid(task->datain.data, (size_t)task->datain.size, o->wwid)) {
        o->wwid[0] = 0;
        reason(o, "Stable LUN WWID unavailable or unsupported");
    }
    scsi_free_scsi_task(task);
    task = iscsi_readcapacity16_sync(connection, probe_lun);
    if (!task_ok(task, 32)) {
        reason(o, "LUN capacity is unreadable");
        scsi_free_scsi_task(task);
        return 1;
    }
    uint64_t last = be_number(task->datain.data, 8);
    uint64_t block = be_number(task->datain.data + 8, 4);
    scsi_free_scsi_task(task);
    if (block < 512 || block > 65536 || (block & (block - 1)) || last >= (uint64_t)INT64_MAX / block) {
        reason(o, "LUN capacity or block size is unsupported");
        return 1;
    }
    block_size = (uint32_t)block;
    o->size = (last + 1) * block;
    inspect_filesystems(o);
    return 1;
}

static void json_string(const char *text)
{
    putchar('"');
    for (const unsigned char *p = (const unsigned char *)text; *p; p++) {
        if (*p == '"' || *p == '\\') { putchar('\\'); putchar(*p); }
        else if (*p < 32 || *p >= 127) printf("\\u%04x", *p);
        else putchar(*p);
    }
    putchar('"');
}

static int input_line(char *buffer, size_t size)
{
    size_t at = 0;
    for (;;) {
        int c = getchar();
        if (c == '\n') { buffer[at] = 0; return 0; }
        if (c == EOF || c == 0 || c == '\r' || at + 1 >= size) return -1;
        buffer[at++] = (char)c;
    }
}

static int iscsi_name(const char *name)
{
    if (strlen(name) > 223 || (strncmp(name, "iqn.", 4) && strncmp(name, "eui.", 4) && strncmp(name, "naa.", 4))) return 0;
    for (const unsigned char *p = (const unsigned char *)name; *p; p++)
        if (*p < 33 || *p > 126 || *p == '/' || *p == '\\') return 0;
    return 1;
}

static void quiet_log(int level, const char *message)
{
    (void)level; (void)message;
}

int main(int argc, char **argv)
{
    struct rlimit no_core = {0, 0};
    (void)setrlimit(RLIMIT_CORE, &no_core);
    struct rlimit memory_limit = {256U * 1024U * 1024U, 256U * 1024U * 1024U};
    if (setrlimit(RLIMIT_AS, &memory_limit)) return 1;
    // Core bounds the helper at 8s and kills it on cancellation; the alarm fires first so a
    // stalled target never outlives the request.
    alarm(7);
    if (prctl(PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)) return 1;
    if (!adapter_supported()) return 1;
    if (argc == 2 && !strcmp(argv[1], "--probe")) {
        if (puts(CAPABILITY) < 0 || fflush(stdout)) return 1;
        return 0;
    }
    if (argc != 1) return 1;
    char initiator[224], portal[254], target[224], username[254], secret[MAX_STRING_SIZE + 1];
    if (input_line(initiator, sizeof(initiator)) || input_line(portal, sizeof(portal)) ||
        input_line(target, sizeof(target)) || input_line(username, sizeof(username)) ||
        input_line(secret, sizeof(secret)) || getchar() != EOF || !iscsi_name(initiator) ||
        !iscsi_name(target) || !portal[0] || (!!username[0] != !!secret[0])) return 1;
    uint32_t random_id[2];
    if (getrandom(random_id, sizeof(random_id), 0) != sizeof(random_id)) return 1;
    connection = iscsi_create_context(initiator);
    if (!connection) return 1;
    iscsi_set_log_level(connection, 0);
    iscsi_set_log_fn(connection, quiet_log);
    iscsi_set_noautoreconnect(connection, 1);
    int status = 1;
    if (iscsi_set_isid_random(connection, random_id[0], random_id[1]) ||
        iscsi_set_timeout(connection, 2) || iscsi_set_session_type(connection, ISCSI_SESSION_NORMAL) ||
        iscsi_set_targetname(connection, target) ||
        (username[0] && iscsi_set_initiator_username_pwd(connection, username, secret))) goto done;
    explicit_bzero(secret, sizeof(secret));
    if (iscsi_connect_sync(connection, portal) || iscsi_login_sync(connection)) goto done;
    struct scsi_task *task = iscsi_reportluns_sync(connection, SCSI_REPORTLUNS_REPORT_ALL_LUNS, 8 + MAX_LUNS * 8);
    uint64_t luns[MAX_LUNS];
    size_t count = 0;
    if (!task_ok(task, 8) || report_luns(task->datain.data, (size_t)task->datain.size, luns, &count)) {
        scsi_free_scsi_task(task);
        goto done;
    }
    scsi_free_scsi_task(task);
    struct observation observations[MAX_LUNS] = {{0}};
    size_t observed = 0;
    for (size_t i = 0; i < count; i++) {
        observations[observed].lun = luns[i];
        if (inspect_lun(&observations[observed])) observed++;
    }
    (void)iscsi_logout_sync(connection);
    putchar('[');
    for (size_t i = 0; i < observed; i++) {
        struct observation *o = &observations[i];
        printf("%s{\"lun\":%" PRIu64, i ? "," : "", o->lun);
        if (o->size) printf(",\"size_bytes\":%" PRIu64, o->size);
        if (o->wwid[0]) { printf(",\"wwid\":"); json_string(o->wwid); }
        if (o->uuid[0]) { printf(",\"filesystem_uuid\":"); json_string(o->uuid); }
        if (o->type[0]) { printf(",\"filesystem_type\":"); json_string(o->type); }
        if (o->error[0]) { printf(",\"error\":"); json_string(o->error); }
        putchar('}');
    }
    puts("]");
    status = fflush(stdout) || ferror(stdout) ? 1 : 0;
done:
    explicit_bzero(secret, sizeof(secret));
    close_probe();
    iscsi_destroy_context(connection);
    return status;
}
