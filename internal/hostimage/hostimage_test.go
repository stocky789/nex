// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var testDisk = MinDiskBytes() + 64*MiB

func TestPlanLayout(t *testing.T) {
	plan, err := Plan(testDisk, LayoutRAID1)
	if err != nil {
		t.Fatal(err)
	}
	labels := []string{ESPLabel, "nex-root-a", "nex-verity-a", "nex-root-b", "nex-verity-b", DataLabel}
	if len(plan) != len(labels) {
		t.Fatalf("got %d partitions", len(plan))
	}
	end := int64(0)
	for i, p := range plan {
		if p.Label != labels[i] || p.Index != i+1 || p.Offset%MiB != 0 || p.Size%MiB != 0 || p.Offset < end {
			t.Errorf("partition %d bad: %+v (previous end %d)", i+1, p, end)
		}
		end = p.Offset + p.Size
	}
	if plan[0].Size != ESPSize || plan[0].TypeName() != "esp" || plan[1].TypeName() != "root-x86-64" || plan[2].TypeName() != "root-x86-64-verity" {
		t.Errorf("sizes or types wrong: %+v", plan[:3])
	}
	if plan[5].TypeName() != "linux-raid" {
		t.Errorf("raid1 data type is %s", plan[5].TypeName())
	}
	if end > testDisk-backupReserve {
		t.Errorf("layout ends at %d, inside the backup GPT (%d)", end, testDisk-backupReserve)
	}
	single, _ := Plan(testDisk, LayoutSingle)
	if single[5].TypeName() != "linux-generic" {
		t.Errorf("single data type is %s", single[5].TypeName())
	}
	if _, err = Plan(MinDiskBytes()-1, LayoutSingle); err == nil {
		t.Error("a disk below the minimum was accepted")
	}
	if _, err = Plan(testDisk, "stripe"); err == nil {
		t.Error("unknown layout accepted")
	}
}

func TestValidateLayout(t *testing.T) {
	for _, c := range []struct {
		layout string
		disks  int
		ok     bool
	}{{"single", 1, true}, {"single", 2, false}, {"raid1", 2, true}, {"raid1", 3, true}, {"raid1", 1, false}, {"raid0", 2, false}, {"single", 0, false}} {
		if err := ValidateLayout(c.layout, c.disks); (err == nil) != c.ok {
			t.Errorf("%s/%d: err=%v", c.layout, c.disks, err)
		}
	}
}

func TestNodeName(t *testing.T) {
	for in, want := range map[string]string{"/dev/vda": "/dev/vda1", "/dev/nvme0n1": "/dev/nvme0n1p1", "/dev/mmcblk0": "/dev/mmcblk0p1"} {
		if got := NodeName(in, 1); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestSlotIDsFollowRootHash(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdeffedcba9876543210fedcba9876543210"
	root, verity, err := SlotIDs(hash)
	if err != nil {
		t.Fatal(err)
	}
	if root.String() != "01234567-89ab-cdef-0123-456789abcdef" || verity.String() != "fedcba98-7654-3210-fedc-ba9876543210" {
		t.Errorf("got %s and %s", root, verity)
	}
	for _, bad := range []string{"", "ABCDEF", strings.Repeat("g", 64), hash[:63]} {
		if _, _, err = SlotIDs(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestGPTRoundTripAndBackupFallback(t *testing.T) {
	path := sparseDisk(t, testDisk)
	d, err := OpenDisk(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = LayDisk(context.Background(), &recorder{}, d, LayoutSingle); err != nil {
		t.Fatal(err)
	}
	tb, err := d.ReadTable()
	if err != nil || len(tb.Parts) != 6 {
		t.Fatalf("table: %v %v", tb, err)
	}
	seen := map[GUID]bool{}
	for _, p := range tb.Parts {
		if seen[p.ID] {
			t.Errorf("duplicate partition GUID %s", p.ID)
		}
		seen[p.ID] = true
	}
	// Destroy the primary header: the backup must still read, with the same content.
	if _, err = d.File.WriteAt(make([]byte, 512), 512); err != nil {
		t.Fatal(err)
	}
	again, err := d.ReadTable()
	if err != nil || again.DiskID != tb.DiskID || again.Parts[5].Label != DataLabel {
		t.Fatalf("backup fallback failed: %v", err)
	}
	// Destroy both.
	if _, err = d.File.WriteAt(make([]byte, 512), testDisk-512); err != nil {
		t.Fatal(err)
	}
	if _, err = d.ReadTable(); err == nil {
		t.Error("a disk without any GPT header read as valid")
	}
}

func TestGPTIsReadableBySfdisk(t *testing.T) {
	if _, err := exec.LookPath("sfdisk"); err != nil {
		t.Skip("sfdisk not installed")
	}
	path := sparseDisk(t, testDisk)
	d, _ := OpenDisk(path, true)
	if err := LayDisk(context.Background(), &recorder{}, d, LayoutRAID1); err != nil {
		t.Fatal(err)
	}
	d.Close()
	out, err := exec.Command("sfdisk", "--json", path).CombinedOutput()
	if err != nil {
		t.Fatalf("sfdisk: %v\n%s", err, out)
	}
	for _, want := range []string{`"label": "gpt"`, `"name": "nex-esp"`, `"name": "nex-root-a"`, `"name": "nex-verity-b"`, `"name": "nex-data"`,
		"C12A7328-F81F-11D2-BA4B-00A0C93EC93B", "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709", "2C7357ED-EBD2-46D9-AEC1-23D437EC2BF5"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("sfdisk output lacks %s:\n%s", want, out)
		}
	}
}

func TestVerityMatchesVeritysetup(t *testing.T) {
	if _, err := exec.LookPath("veritysetup"); err != nil {
		t.Skip("veritysetup not installed")
	}
	dir := t.TempDir()
	data := make([]byte, 700*verityBlock) // two hash levels
	for i := range data {
		data[i] = byte(i * 7)
	}
	salt := bytes.Repeat([]byte{0xab}, 32)
	if err := os.WriteFile(filepath.Join(dir, "data"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("veritysetup", "format", "--salt=ab"+strings.Repeat("ab", 31), "--uuid=22222222-3333-4444-5555-666666666666",
		filepath.Join(dir, "data"), filepath.Join(dir, "hash")).CombinedOutput()
	if err != nil {
		t.Skipf("veritysetup format: %v: %s", err, out)
	}
	var want string
	for _, l := range strings.Split(string(out), "\n") {
		if f := strings.Fields(l); len(f) == 3 && f[0] == "Root" {
			want = f[2]
		}
	}
	image, got := makeVerityImage(t, data, salt)
	if got != want {
		t.Fatalf("root hash %s, veritysetup says %s", got, want)
	}
	real, _ := os.ReadFile(filepath.Join(dir, "hash"))
	if !bytes.Equal(real[verityBlock:], image[verityBlock:]) {
		t.Error("hash tree differs from veritysetup's")
	}
	if err = VerifyVerity(filepath.Join(dir, "data"), filepath.Join(dir, "hash"), want); err != nil {
		t.Errorf("a veritysetup image does not verify: %v", err)
	}
	if err = VerifyVerity(filepath.Join(dir, "data"), filepath.Join(dir, "hash"), strings.Repeat("0", 64)); err == nil {
		t.Error("a wrong root hash verified")
	}
}

func TestReadReleaseRejectsTamper(t *testing.T) {
	dir := makeRelease(t, "r1", 1, 1, 8)
	if _, err := ReadRelease(dir); err != nil {
		t.Fatalf("good release: %v", err)
	}
	flip := func(name string) {
		t.Helper()
		p := filepath.Join(dir, name)
		b, _ := os.ReadFile(p)
		b[len(b)/2] ^= 1
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"root.erofs", "root.verity", "esp/EFI/BOOT/BOOTX64.EFI", "nex-core_r1_1.efi"} {
		flip(name)
		if _, err := ReadRelease(dir); err == nil {
			t.Errorf("tampered %s accepted", name)
		}
		flip(name)
	}
	// A consistent manifest over a mismatching erofs/verity pair is caught by the hash tree check.
	other := makeRelease(t, "r1", 1, 2, 8)
	b, _ := os.ReadFile(filepath.Join(other, "root.verity"))
	if err := os.WriteFile(filepath.Join(dir, "root.verity"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRelease(dir); err == nil {
		t.Error("manifest-inconsistent release accepted")
	}
}

func TestReadReleaseRejectsBadManifest(t *testing.T) {
	dir := makeRelease(t, "r1", 1, 1, 4)
	if err := os.Rename(filepath.Join(dir, "nex-core_r1_1.efi"), filepath.Join(dir, "nex-core_r1_2.efi")); err != nil {
		t.Fatal(err)
	}
	m, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	m = bytes.ReplaceAll(m, []byte("nex-core_r1_1.efi"), []byte("nex-core_r1_2.efi"))
	os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o644)
	if _, err := ReadRelease(dir); err == nil || !strings.Contains(err.Error(), "does not match label") {
		t.Errorf("UKI with the wrong sequence accepted: %v", err)
	}
	m = bytes.ReplaceAll(m, []byte("root.erofs"), []byte("../root.erofs"))
	os.WriteFile(filepath.Join(dir, "manifest.json"), m, 0o644)
	if _, err := ReadRelease(dir); err == nil {
		t.Error("manifest path escaping the release directory accepted")
	}
}

func openDisks(t *testing.T, paths ...string) []*Disk {
	t.Helper()
	var ds []*Disk
	for _, p := range paths {
		d, err := OpenDisk(p, true)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		ds = append(ds, d)
	}
	return ds
}

func TestInstallSingleDisk(t *testing.T) {
	root := sandbox(t)
	disk := sparseDisk(t, testDisk)
	rel := makeRelease(t, "r1", 1, 1, 64)
	seed := filepath.Join(root, "seed")
	os.MkdirAll(filepath.Join(seed, "ssh"), 0o755)
	os.WriteFile(filepath.Join(seed, "hostname"), []byte("host1\n"), 0o644)
	os.Symlink("../usr/share/zoneinfo/UTC", filepath.Join(seed, "localtime"))
	rec := &recorder{}
	w := &Writer{Runner: rec}
	if err := w.Install(context.Background(), Options{Layout: LayoutSingle, Disks: []string{disk}, ImageDir: rel, EtcSeed: seed}); err != nil {
		t.Fatal(err)
	}
	r, _ := ReadRelease(rel)
	rootID, verityID, _ := SlotIDs(r.Manifest.RootHash)
	tb := readTable(t, disk)
	if a := part(t, tb, "nex-root-a"); a.ID != rootID {
		t.Errorf("slot A root GUID %s, want %s", a.ID, rootID)
	}
	if a := part(t, tb, "nex-verity-a"); a.ID != verityID {
		t.Errorf("slot A verity GUID %s, want %s", a.ID, verityID)
	}
	for _, l := range []string{"nex-root-b", "nex-verity-b"} {
		if p := part(t, tb, l); p.ID == rootID || p.ID == verityID {
			t.Errorf("empty slot %s carries a release GUID", l)
		}
	}
	want, _ := os.ReadFile(filepath.Join(rel, "root.erofs"))
	if got := readPart(t, disk, tb, "nex-root-a", len(want)); !bytes.Equal(got, want) {
		t.Error("root.erofs not written to slot A")
	}
	if b := readPart(t, disk, tb, "nex-root-b", 4096); !bytes.Equal(b, make([]byte, 4096)) {
		t.Error("slot B is not empty")
	}
	if i, j := rec.index("mkfs.ext4 -F -q -L nex-data "+disk+"6"), rec.index("mount -t ext4 "+disk+"6"); i < 0 || j < i {
		t.Errorf("data file system not made before it is mounted: %v", rec.calls)
	}
	if rec.index("mdadm") >= 0 {
		t.Error("single layout ran mdadm")
	}
	if rec.index("mkfs.fat -F 32 -n NEXESP "+disk+"1") < 0 {
		t.Errorf("ESP not formatted: %v", rec.calls)
	}
	esp := filepath.Join(espMountBase, filepath.Base(disk+"1"))
	for rel, want := range map[string]string{"EFI/BOOT/BOOTX64.EFI": "boot-loader", "loader/loader.conf": "timeout 0\n", "EFI/Linux/nex-core_r1_1.efi": "uki-r11"} {
		if b, err := os.ReadFile(filepath.Join(esp, rel)); err != nil || string(b) != want {
			t.Errorf("ESP %s: %q %v", rel, b, err)
		}
	}
	data := filepath.Join(mountBase, "data")
	if b, err := os.ReadFile(filepath.Join(data, etcUpper, "hostname")); err != nil || string(b) != "host1\n" {
		t.Errorf("seeded hostname: %q %v", b, err)
	}
	if l, err := os.Readlink(filepath.Join(data, etcUpper, "localtime")); err != nil || l != "../usr/share/zoneinfo/UTC" {
		t.Errorf("seeded symlink: %q %v", l, err)
	}
	if st, err := os.Stat(filepath.Join(data, etcWork)); err != nil || !st.IsDir() {
		t.Error("overlay work directory missing")
	}
	if rec.index("umount "+data) < 0 {
		t.Error("data volume left mounted")
	}
}

func TestInstallRAID1(t *testing.T) {
	sandbox(t)
	d1, d2 := sparseDisk(t, testDisk), sparseDisk(t, testDisk+256*MiB)
	rel := makeRelease(t, "r1", 1, 1, 16)
	rec := &recorder{}
	if err := (&Writer{Runner: rec}).Install(context.Background(), Options{Layout: LayoutRAID1, Disks: []string{d1, d2}, ImageDir: rel}); err != nil {
		t.Fatal(err)
	}
	var create string
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "mdadm --create") {
			create = c
		}
	}
	if !strings.Contains(create, "--name=nex-data") || !strings.Contains(create, "--raid-devices=2") ||
		!strings.HasSuffix(create, d1+"6 "+d2+"6") || !strings.Contains(create, "--level=1") {
		t.Errorf("mdadm call: %q", create)
	}
	if rec.index("mkfs.ext4 -F -q -L nex-data "+mdDevice()) < 0 || rec.index("mdadm --stop "+mdDevice()) < 0 {
		t.Errorf("mirror not formatted or stopped: %v", rec.calls)
	}
	// The two disks are different sizes, yet both carry slot A with the same GUIDs, and the same
	// partition offsets before nex-data.
	t1, t2 := readTable(t, d1), readTable(t, d2)
	for _, l := range []string{ESPLabel, "nex-root-a", "nex-verity-a", "nex-root-b", "nex-verity-b"} {
		p1, p2 := part(t, t1, l), part(t, t2, l)
		if t1.Offset(p1) != t2.Offset(p2) || t1.Size(p1) != t2.Size(p2) {
			t.Errorf("%s differs between disks", l)
		}
	}
	if part(t, t1, "nex-root-a").ID != part(t, t2, "nex-root-a").ID {
		t.Error("slot A GUIDs differ between disks")
	}
	if part(t, t1, "nex-root-b").ID == part(t, t2, "nex-root-b").ID {
		t.Error("empty slot B carries the same GUID on both disks")
	}
	for _, d := range []string{d1, d2} {
		if _, err := os.Stat(filepath.Join(espMountBase, filepath.Base(d+"1"), "EFI/Linux/nex-core_r1_1.efi")); err != nil {
			t.Errorf("ESP of %s: %v", d, err)
		}
	}
	if p := part(t, t1, DataLabel); p.Type != mustGUID(typeRAID) {
		t.Error("data partitions are not typed linux-raid")
	}
}

func TestInstallRefusesBeforeWriting(t *testing.T) {
	sandbox(t)
	small := sparseDisk(t, MinDiskBytes()-MiB)
	big := sparseDisk(t, testDisk)
	rel := makeRelease(t, "r1", 1, 1, 4)
	rec := &recorder{}
	w := &Writer{Runner: rec}
	for name, o := range map[string]Options{
		"small disk": {Layout: LayoutRAID1, Disks: []string{big, small}, ImageDir: rel},
		"duplicate":  {Layout: LayoutRAID1, Disks: []string{big, big}, ImageDir: rel},
		"layout":     {Layout: LayoutRAID1, Disks: []string{big}, ImageDir: rel},
		"no release": {Layout: LayoutSingle, Disks: []string{big}, ImageDir: t.TempDir()},
	} {
		if err := w.Install(context.Background(), o); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(rec.calls) != 0 {
		t.Errorf("tools ran before validation: %v", rec.calls)
	}
	if b, _ := os.ReadFile(big); !bytes.Equal(b[:4096], make([]byte, 4096)) {
		t.Error("disk written by a refused install")
	}
}

func installed(t *testing.T, layout string, disks ...string) (string, []*Disk) {
	t.Helper()
	rel := makeRelease(t, "r1", 1, 1, 16)
	if err := (&Writer{Runner: &recorder{}}).Install(context.Background(), Options{Layout: layout, Disks: disks, ImageDir: rel}); err != nil {
		t.Fatal(err)
	}
	return rel, nil
}

func TestWriteSlotOrderingAndTornSlot(t *testing.T) {
	sandbox(t)
	d1, d2 := sparseDisk(t, testDisk), sparseDisk(t, testDisk)
	installed(t, LayoutRAID1, d1, d2)
	before := readTable(t, d1)
	slotA := part(t, before, "nex-root-a").ID

	// Release 2 whose hash tree on disk 2 will not read back right: damage the file after it was verified.
	rel2 := makeRelease(t, "r2", 2, 2, 16)
	r2, err := ReadRelease(rel2)
	if err != nil {
		t.Fatal(err)
	}
	disks := openDisks(t, d1, d2)
	// Corrupt the source after verification so the write lands bad bytes on both disks.
	b, _ := os.ReadFile(filepath.Join(rel2, "root.erofs"))
	b[100] ^= 0xff
	os.WriteFile(filepath.Join(rel2, "root.erofs"), b, 0o644)
	w := &Writer{Runner: &recorder{}}
	if err = w.WriteSlot(context.Background(), disks, "b", r2, ""); err == nil || !strings.Contains(err.Error(), "read back wrong") {
		t.Fatalf("a slot that does not verify was committed: %v", err)
	}
	rootID, verityID, _ := SlotIDs(r2.Manifest.RootHash)
	for _, p := range []string{d1, d2} {
		tb := readTable(t, p)
		for _, l := range []string{"nex-root-b", "nex-verity-b"} {
			if id := part(t, tb, l).ID; id == rootID || id == verityID {
				t.Errorf("%s: torn slot %s selectable by roothash", p, l)
			}
		}
		if part(t, tb, "nex-root-a").ID != slotA {
			t.Errorf("%s: the running slot A changed", p)
		}
	}
	// Restore and write properly: both disks end up identical and selectable.
	b[100] ^= 0xff
	os.WriteFile(filepath.Join(rel2, "root.erofs"), b, 0o644)
	if err = w.WriteSlot(context.Background(), disks, "b", r2, r2.Manifest.RootHash[:0]); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{d1, d2} {
		tb := readTable(t, p)
		if part(t, tb, "nex-root-b").ID != rootID || part(t, tb, "nex-verity-b").ID != verityID {
			t.Errorf("%s: slot B not selected by roothash after a good write", p)
		}
		if got := readPart(t, p, tb, "nex-root-b", len(b)); !bytes.Equal(got, b) {
			t.Errorf("%s: slot B content wrong", p)
		}
	}
}

func TestWriteSlotRefusesRunningSlot(t *testing.T) {
	sandbox(t)
	d1 := sparseDisk(t, testDisk)
	rel, _ := installed(t, LayoutSingle, d1)
	r1, _ := ReadRelease(rel)
	r2dir := makeRelease(t, "r2", 2, 2, 16)
	r2, _ := ReadRelease(r2dir)
	before, _ := os.ReadFile(d1)
	w := &Writer{Runner: &recorder{}}
	err := w.WriteSlot(context.Background(), openDisks(t, d1), "a", r2, r1.Manifest.RootHash)
	if err == nil || !strings.Contains(err.Error(), "running system") {
		t.Fatalf("running slot rewritten: %v", err)
	}
	if after, _ := os.ReadFile(d1); !bytes.Equal(before, after) {
		t.Error("disk changed by a refused slot write")
	}
}

func TestRepairKeepsData(t *testing.T) {
	sandbox(t)
	d1, d2 := sparseDisk(t, testDisk), sparseDisk(t, testDisk)
	installed(t, LayoutRAID1, d1, d2)
	before := readTable(t, d1)
	data := part(t, before, DataLabel)
	marker := bytes.Repeat([]byte("keep"), 2048)
	for _, p := range []string{d1, d2} {
		f, _ := os.OpenFile(p, os.O_RDWR, 0)
		f.WriteAt(marker, before.Offset(data)+7*MiB)
		f.WriteAt([]byte("old-esp"), before.Offset(part(t, before, ESPLabel)))
		f.Close()
	}
	rel2 := makeRelease(t, "r2", 2, 2, 16)
	rec := &recorder{}
	if err := (&Writer{Runner: rec}).Repair(context.Background(), Options{Layout: LayoutRAID1, Disks: []string{d1, d2}, ImageDir: rel2}); err != nil {
		t.Fatal(err)
	}
	r2, _ := ReadRelease(rel2)
	rootID, _, _ := SlotIDs(r2.Manifest.RootHash)
	for _, p := range []string{d1, d2} {
		after := readTable(t, p)
		if got := part(t, after, DataLabel); got != data && p == d1 {
			t.Errorf("%s: nex-data entry changed: %+v -> %+v", p, data, got)
		}
		if after.DiskID != before.DiskID && p == d1 {
			t.Error("disk GUID changed")
		}
		if got := readPart(t, p, after, DataLabel, 0); got != nil {
			_ = got
		}
		f, _ := os.Open(p)
		b := make([]byte, len(marker))
		f.ReadAt(b, after.Offset(part(t, after, DataLabel))+7*MiB)
		f.Close()
		if !bytes.Equal(b, marker) {
			t.Errorf("%s: nex-data content lost", p)
		}
		if part(t, after, "nex-root-a").ID != rootID {
			t.Errorf("%s: slot A not the new release", p)
		}
		if got := readPart(t, p, after, ESPLabel, 7); string(got) == "old-esp" {
			t.Errorf("%s: ESP not rewritten", p)
		}
	}
	for _, c := range rec.calls {
		if strings.HasPrefix(c, "mdadm") || strings.HasPrefix(c, "mkfs.ext4") || strings.Contains(c, "nex-data") {
			t.Errorf("repair touched the data volume: %s", c)
		}
	}
}

func TestRepairRefusesWithoutData(t *testing.T) {
	sandbox(t)
	d1 := sparseDisk(t, testDisk) // never installed
	rel := makeRelease(t, "r1", 1, 1, 4)
	before, _ := os.ReadFile(d1)
	rec := &recorder{}
	if err := (&Writer{Runner: rec}).Repair(context.Background(), Options{Layout: LayoutSingle, Disks: []string{d1}, ImageDir: rel}); err == nil {
		t.Fatal("repair of a blank disk accepted")
	}
	if after, _ := os.ReadFile(d1); !bytes.Equal(before, after) || len(rec.calls) != 0 {
		t.Error("a refused repair wrote to the disk or ran tools")
	}
	d2 := sparseDisk(t, testDisk)
	installed(t, LayoutSingle, d2)
	if err := (&Writer{Runner: rec}).Repair(context.Background(), Options{Layout: LayoutRAID1, Disks: []string{d2, d1}, ImageDir: rel}); err == nil {
		t.Fatal("repair with one blank disk of a mirror accepted")
	}
	if err := (&Writer{Runner: rec}).Repair(context.Background(), Options{Layout: LayoutSingle, Disks: []string{d2}, ImageDir: rel, EtcSeed: "/x"}); err == nil {
		t.Fatal("repair accepted an /etc seed")
	}
}
