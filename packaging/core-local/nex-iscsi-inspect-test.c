// SPDX-License-Identifier: AGPL-3.0-only
#define main inspector_main
#include "nex-iscsi-inspect.c"
#undef main
#include <sys/stat.h>

/* Unlike assert(), always evaluates its argument, so a build with NDEBUG still runs the checks. */
#define check(expr) do { \
    if (!(expr)) { fprintf(stderr, "%s:%d: check failed: %s\n", __FILE__, __LINE__, #expr); exit(1); } \
} while (0)

static int backing_fd = -1;
static ssize_t backing_read(void *buffer, size_t count, uint64_t offset)
{
    return syscall(SYS_pread64, backing_fd, buffer, count, offset);
}

static void image_close(void)
{
    if (backing_fd >= 0) close(backing_fd);
    backing_fd = -1;
}

static void test_report_luns(void)
{
    unsigned char data[24] = {0, 0, 0, 16};
    data[17] = 7;
    uint64_t luns[MAX_LUNS];
    size_t count;
    check(!report_luns(data, sizeof(data), luns, &count));
    check(count == 2 && luns[0] == 0 && luns[1] == 7);
    data[16] = 0x41; data[17] = 0;
    check(!report_luns(data, sizeof(data), luns, &count));
    check(luns[1] == 16640); // Linux HCTL and libiscsi preserve flat addressing bits.
    data[16] = 0; data[17] = 7;
    check(report_luns(data, sizeof(data) - 1, luns, &count) < 0);
    data[17] = 0;
    check(report_luns(data, sizeof(data), luns, &count) < 0);
    data[17] = 7; data[18] = 1;
    check(report_luns(data, sizeof(data), luns, &count) < 0);
    data[18] = 0; data[3] = 15;
    check(report_luns(data, sizeof(data), luns, &count) < 0);
    data[3] = 0;
    check(!report_luns(data, 8, luns, &count) && count == 0);
}

static void test_actual_wwid(void)
{
    unsigned char vpd[] = {0, 0x83, 0, 12, 1, 3, 0, 8, 0x50, 1, 2, 3, 4, 5, 6, 7};
    char wwid[65] = {0};
    check(!vpd_wwid(vpd, sizeof(vpd), wwid));
    check(!strcmp(wwid, "5001020304050607"));
    vpd[5] = 0x13; // Target-port WWN must never stand in for a LUN identity.
    wwid[0] = 0;
    check(vpd_wwid(vpd, sizeof(vpd), wwid) == 1 && !wwid[0]);
    vpd[5] = 2; // EUI has no native lsblk WWN; it cannot supply registration identity.
    check(vpd_wwid(vpd, sizeof(vpd), wwid) == 1 && !wwid[0]);
    vpd[5] = 3;
    check(vpd_wwid(vpd, sizeof(vpd) - 1, wwid) < 0);
    vpd[7] = 9;
    check(vpd_wwid(vpd, sizeof(vpd), wwid) < 0);
    vpd[7] = 8; vpd[8] = 0x60;
    check(vpd_wwid(vpd, sizeof(vpd), wwid) < 0);
}

static void test_native_wwn_preference(void)
{
    unsigned char vpd[] = {
        0, 0x83, 0, 32,
        1, 3, 0, 8, 0x30, 1, 2, 3, 4, 5, 6, 7,
        1, 3, 0, 16, 0x60, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15
    };
    char wwid[65] = {0};
    check(!vpd_wwid(vpd, sizeof(vpd), wwid));
    check(!strcmp(wwid, "600102030405060708090a0b0c0d0e0f"));
    vpd[8] = 0x50;
    check(!vpd_wwid(vpd, sizeof(vpd), wwid)); // NAA6 also wins over NAA5.
    unsigned char reordered[sizeof(vpd)];
    memcpy(reordered, vpd, 4);
    memcpy(reordered + 4, vpd + 16, 20);
    memcpy(reordered + 24, vpd + 4, 12);
    check(!vpd_wwid(reordered, sizeof(reordered), wwid));
    check(!strcmp(wwid, "600102030405060708090a0b0c0d0e0f"));
    unsigned char conflict[44];
    memcpy(conflict, vpd, 4);
    conflict[3] = 40;
    memcpy(conflict + 4, vpd + 16, 20);
    memcpy(conflict + 24, vpd + 16, 20);
    check(!vpd_wwid(conflict, sizeof(conflict), wwid)); // Identical IDs agree.
    conflict[43]++;
    check(vpd_wwid(conflict, sizeof(conflict), wwid) < 0);
}

static int ready_calls, pending_attention, ready_status, ready_lun, ready_sense;
static struct scsi_task *ready_observation(struct iscsi_context *ctx, int lun)
{
    (void)ctx;
    check(lun == ready_lun);
    ready_calls++;
    struct scsi_task *task = calloc(1, sizeof(*task));
    check(task);
    task->status = ready_status;
    task->sense.key = ready_sense;
    if (pending_attention-- > 0) task->status = SCSI_STATUS_CHECK_CONDITION;
    return task;
}

static void test_first_session_readiness(void)
{
    struct scsi_task *(*old)(struct iscsi_context *, int) = test_unit_ready;
    test_unit_ready = ready_observation;
    ready_sense = SCSI_SENSE_UNIT_ATTENTION;
    ready_calls = 0; pending_attention = 1; ready_status = SCSI_STATUS_GOOD; ready_lun = 16640;
    check(lun_ready(ready_lun) && ready_calls == 2);
    ready_calls = 0; pending_attention = 12;
    check(!lun_ready(ready_lun) && ready_calls == 11); // Persistent UA never becomes success.
    ready_calls = 0; pending_attention = 0; ready_status = SCSI_STATUS_BUSY;
    check(!lun_ready(ready_lun) && ready_calls == 1); // No retry for unrelated status.
    ready_calls = 0; ready_status = SCSI_STATUS_CHECK_CONDITION; ready_sense = SCSI_SENSE_NOT_READY;
    check(!lun_ready(ready_lun) && ready_calls == 1);
    test_unit_ready = old;
}

static void test_chap_input_bound(void)
{
    char input[MAX_STRING_SIZE + 3], secret[MAX_STRING_SIZE + 1];
    memset(input, 's', sizeof(input));
    input[MAX_STRING_SIZE] = '\n';
    FILE *original = stdin;
    stdin = fmemopen(input, MAX_STRING_SIZE + 1, "r");
    check(stdin);
    check(!input_line(secret, sizeof(secret)) && strlen(secret) == MAX_STRING_SIZE);
    fclose(stdin);
    input[MAX_STRING_SIZE] = 's'; input[MAX_STRING_SIZE + 1] = '\n';
    stdin = fmemopen(input, MAX_STRING_SIZE + 2, "r");
    check(stdin);
    check(input_line(secret, sizeof(secret)) < 0);
    fclose(stdin);
    stdin = original;
}

static void test_read_adapter(void)
{
    backing_fd = memfd_create("nex-iscsi-test-media", MFD_CLOEXEC);
    check(backing_fd >= 0 && !ftruncate(backing_fd, 8192));
    const char marker[] = "observed-media";
    check(pwrite(backing_fd, marker, sizeof(marker), 1025) == sizeof(marker));
    check(!open_probe(8192));
    block_size = 512;
    probe_reader = backing_read;
    struct stat st;
    check(!fstat(probe_fd, &st) && st.st_size == 8192);
    check(lseek(probe_fd, 1025, SEEK_SET) == 1025);
    char data[sizeof(marker)];
    check(read(probe_fd, data, sizeof(data)) == sizeof(data));
    check(!memcmp(data, marker, sizeof(marker)));
    check(lseek(probe_fd, 0, SEEK_CUR) == 1025 + sizeof(data));
    check(pread(probe_fd, data, sizeof(data), 1025) == sizeof(data));
    check(lseek(probe_fd, 0, SEEK_CUR) == 1025 + sizeof(data));
    check(pread(probe_fd, data, sizeof(data), 8191) == 1);
    check(pread(probe_fd, data, sizeof(data), 8192) == 0);
    errno = 0;
    check(read(-1, data, sizeof(data)) == -1 && errno == EBADF);
    check(lseek(backing_fd, 1025, SEEK_SET) == 1025);
    check(read(backing_fd, data, sizeof(data)) == sizeof(data));
    check(!memcmp(data, marker, sizeof(marker)));
    probe_bytes = READ_LIMIT - 512;
    check(pread(probe_fd, data, sizeof(data), 1025) == sizeof(data));
    check(probe_bytes == READ_LIMIT);
    check(pread(probe_fd, data, sizeof(data), 1025) == -1 && probe_failed);
    close_probe();
    check(probe_fd == -1);
    check(pread(backing_fd, data, sizeof(data), 1025) == sizeof(data));
    check(!memcmp(data, marker, sizeof(marker))); // The source was never changed.
    image_close();
}

#define IMAGE_SIZE (8U * 1024U * 1024U)
#define IMAGE_UUID_1 "01020304-0506-0708-090a-0b0c0d0e0f10"
#define IMAGE_UUID_2 "02030405-0607-0809-0a0b-0c0d0e0f1011"

static void put_le(unsigned char *p, uint64_t value, size_t bytes)
{
    for (size_t i = 0; i < bytes; i++) p[i] = (unsigned char)(value >> (8 * i));
}

static uint32_t crc32_ieee(const unsigned char *p, size_t n)
{
    uint32_t crc = 0xffffffffU;
    for (size_t i = 0; i < n; i++) {
        crc ^= p[i];
        for (int bit = 0; bit < 8; bit++) crc = (crc >> 1) ^ (0xedb88320U & (0U - (crc & 1U)));
    }
    return ~crc;
}

static void image_new(void)
{
    image_close();
    backing_fd = memfd_create("nex-iscsi-test-image", MFD_CLOEXEC);
    check(backing_fd >= 0 && !ftruncate(backing_fd, IMAGE_SIZE));
}

static void image_write(uint64_t offset, const void *data, size_t size)
{
    check(pwrite(backing_fd, data, size, (off_t)offset) == (ssize_t)size);
}

/* The smallest superblock libblkid identifies as ext4: magic, the extents feature and a UUID
 * whose bytes run seed, seed+1, ... so the expected canonical text is known. */
static void image_ext4(uint64_t offset, unsigned char seed)
{
    unsigned char sb[0x78] = {0};
    put_le(sb + 0x38, 0xef53, 2);
    put_le(sb + 0x60, 0x40, 4);
    for (unsigned char i = 0; i < 16; i++) sb[0x68 + i] = (unsigned char)(seed + i);
    image_write(offset + 1024, sb, sizeof(sb));
}

/* Protective MBR plus a primary GPT (the backup is optional for libblkid) with entries
 * {first_lba, sector_count}. */
static void image_gpt(const uint64_t (*parts)[2], size_t count)
{
    unsigned char pmbr[512] = {0};
    pmbr[446 + 4] = 0xee;
    put_le(pmbr + 446 + 8, 1, 4);
    put_le(pmbr + 446 + 12, IMAGE_SIZE / 512 - 1, 4);
    pmbr[510] = 0x55;
    pmbr[511] = 0xaa;
    image_write(0, pmbr, sizeof(pmbr));
    static unsigned char entries[128 * 128];
    memset(entries, 0, sizeof(entries));
    for (size_t i = 0; i < count; i++) {
        unsigned char *entry = entries + i * 128;
        memset(entry, 1, 16);
        memset(entry + 16, (int)(i + 1), 16);
        put_le(entry + 32, parts[i][0], 8);
        put_le(entry + 40, parts[i][0] + parts[i][1] - 1, 8);
    }
    uint64_t last = IMAGE_SIZE / 512 - 1;
    unsigned char header[92] = {0};
    memcpy(header, "EFI PART", 8);
    put_le(header + 8, 0x10000, 4);
    put_le(header + 12, sizeof(header), 4);
    put_le(header + 24, 1, 8);
    put_le(header + 32, last, 8);
    put_le(header + 40, 34, 8);
    put_le(header + 48, last - 33, 8);
    memset(header + 56, 9, 16);
    put_le(header + 72, 2, 8);
    put_le(header + 80, 128, 4);
    put_le(header + 84, 128, 4);
    put_le(header + 88, crc32_ieee(entries, sizeof(entries)), 4);
    put_le(header + 16, crc32_ieee(header, sizeof(header)), 4);
    image_write(512, header, sizeof(header));
    image_write(1024, entries, sizeof(entries));
}

static struct observation inspect_image(void)
{
    struct observation o = {0};
    block_size = 512;
    probe_reader = backing_read;
    o.size = IMAGE_SIZE;
    inspect_filesystems(&o);
    return o;
}

static void test_inspect_filesystems(void)
{
    struct observation o;
    image_new();
    o = inspect_image();
    check(!o.type[0] && !o.uuid[0] && strstr(o.error, "No filesystem signature"));

    image_ext4(0, 1);
    o = inspect_image();
    check(!strcmp(o.type, "ext4") && !strcmp(o.uuid, IMAGE_UUID_1) && !o.error[0]);
    image_close();

    const uint64_t one[][2] = {{2048, 8192}};
    image_new();
    image_gpt(one, 1);
    image_ext4(2048 * 512, 1);
    o = inspect_image();
    check(!strcmp(o.type, "ext4") && !strcmp(o.uuid, IMAGE_UUID_1) && !o.error[0]);
    image_close();

    const uint64_t two[][2] = {{2048, 4096}, {8192, 4096}};
    image_new();
    image_gpt(two, 2);
    image_ext4(2048 * 512, 1);
    image_ext4(8192 * 512, 2);
    o = inspect_image();
    check(!o.type[0] && !o.uuid[0] && strstr(o.error, "Multiple or ambiguous"));
    image_close();

    image_new();
    image_gpt(two, 2);
    image_ext4(2048 * 512, 1);
    o = inspect_image();
    check(!strcmp(o.type, "ext4") && !strcmp(o.uuid, IMAGE_UUID_1) && !o.error[0]); // One filesystem, one empty partition.
    image_close();
}

static void test_printable_token_and_names(void)
{
    check(printable_token("01020304-0506-0708-090a-0b0c0d0e0f10") && printable_token("~") && printable_token("!"));
    check(!printable_token("") && !printable_token("a b") && !printable_token("a\n") &&
          !printable_token("a\x7f") && !printable_token("caf\xc3\xa9"));
    char name[260] = "iqn.";
    memset(name + 4, 'a', 219);
    name[223] = 0;
    check(iscsi_name(name));
    name[223] = 'a'; name[224] = 0;
    check(!iscsi_name(name));
    check(iscsi_name("eui.0011223344556677") && iscsi_name("naa.5001020304050607"));
    check(!iscsi_name("") && !iscsi_name("iqn") && !iscsi_name("nqn.2026-09.test:x") && !iscsi_name("iqn.a b") &&
          !iscsi_name("iqn.a/b") && !iscsi_name("iqn.a\\b") && !iscsi_name("iqn.a\tb") && !iscsi_name("iqn.a\x7f"));
}

static void test_json_string(void)
{
    char *text = NULL;
    size_t length = 0;
    FILE *original = stdout, *capture = open_memstream(&text, &length);
    check(capture);
    stdout = capture;
    json_string("plain");
    json_string("q\"b\\n\n\x7f\xc3");
    json_string("");
    stdout = original;
    check(!fclose(capture));
    check(text && !strcmp(text, "\"plain\"\"q\\\"b\\\\n\\u000a\\u007f\\u00c3\"\"\""));
    free(text);
}

int main(void)
{
    int supported = adapter_supported();
    check(supported);
    test_report_luns();
    test_actual_wwid();
    test_native_wwn_preference();
    test_first_session_readiness();
    test_chap_input_bound();
    test_read_adapter();
    test_inspect_filesystems();
    test_printable_token_and_names();
    test_json_string();
    puts("native iSCSI inspection tests passed");
    return 0;
}
