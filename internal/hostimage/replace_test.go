// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The system disk replacement runs on file-backed disks, like the install and update tests: the "surviving" disk is
// the first disk of a RAID1 install, the replacement a blank file. The md array is a fake sysfs tree the runner
// updates when mdadm --add runs, and grub-install leaves its modules and boot code where the real one would.

type replaceRunner struct {
	*recorder
	rig *replaceRig
}

func (r *replaceRunner) Run(ctx context.Context, name string, args ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.recorder.Run(ctx, name, args...); err != nil {
		return err
	}
	switch {
	case name == "mdadm" && len(args) == 3 && args[0] == "--add":
		dir := filepath.Join(r.rig.arrayDir(), "dev-"+filepath.Base(args[2]))
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "state"), []byte("spare\n"), 0o644)
	case name == "grub-install":
		bootDir, disk := "", args[len(args)-1]
		for _, a := range args {
			if v, ok := strings.CutPrefix(a, "--boot-directory="); ok {
				bootDir = v
			}
		}
		os.MkdirAll(filepath.Join(bootDir, "grub", "i386-pc"), 0o755)
		os.WriteFile(filepath.Join(bootDir, "grub", "i386-pc", "normal.mod"), []byte("m"), 0o644)
		code := make([]byte, 446)
		copy(code, []byte{0xeb, 0x63, 0x90})
		copy(code[392:], "GRUB ")
		f, err := os.OpenFile(disk, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteAt(code, 0)
		return err
	}
	return nil
}

func (r *replaceRunner) count(prefix string) int {
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

type replaceRig struct {
	*updateRig
	target  string
	journal string
	run     *replaceRunner
	rh      *Host // the host that replaces: it runs from disk 0
	adds    int   // mdadm --add calls a finished replacement made, 1 unless a test made one fail
}

func (r *replaceRig) arrayDir() string { return filepath.Join(mdSysBlock, "md127", "md") }

// mirror is the nex-data array of the host with its one surviving member. degraded is the kernel's count.
func (r *replaceRig) mirror(degraded string) {
	r.t.Helper()
	dir := r.arrayDir()
	for name, value := range map[string]string{"level": "raid1", "degraded": degraded, "sync_action": "idle"} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			r.t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644)
	}
	member := filepath.Join(dir, "dev-"+filepath.Base(NodeName(r.disks[0], DataIndex)))
	os.MkdirAll(member, 0o755)
	os.WriteFile(filepath.Join(member, "state"), []byte("in_sync\n"), 0o644)
	node := filepath.Join(filepath.Dir(mdDevDir), "md127")
	os.WriteFile(node, nil, 0o644)
	os.MkdirAll(mdDevDir, 0o755)
	os.Remove(mdDevice()) // the install's stand-in for the node udev would create
	if err := os.Symlink("../md127", mdDevice()); err != nil {
		r.t.Fatal(err)
	}
}

func blankDisk(t *testing.T, name string, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(p, size); err != nil {
		t.Fatal(err)
	}
	return p
}

func newReplaceRig(t *testing.T, boot BootMethod) *replaceRig {
	t.Helper()
	r := &replaceRig{updateRig: newRig(t, boot, LayoutRAID1, 2), journal: filepath.Join(t.TempDir(), "state", "replace.json"), adds: 1}
	r.target = blankDisk(t, "new.img", testDisk)
	r.mirror("1")
	r.run = &replaceRunner{recorder: &recorder{}, rig: r}
	r.rh = &Host{W: &Writer{Runner: r.run, Boot: boot}, Running: func() (string, error) { return r.disks[0], nil }}
	// The disk the host runs from is only ever opened for reading.
	open := installDisk
	installDisk = func(p string, write bool) (*Disk, error) {
		if write && p == r.disks[0] {
			t.Errorf("the disk the host runs from was opened for writing")
		}
		return open(p, write)
	}
	t.Cleanup(func() { installDisk = open })
	return r
}

func (r *replaceRig) request(id string) ReplaceRequest {
	r.t.Helper()
	st, err := os.Stat(r.target)
	if err != nil {
		r.t.Fatal(err)
	}
	return ReplaceRequest{RequestID: id, ByID: "/dev/disk/by-id/ata-NEW-DISK", Serial: "NEWDISK1", SizeBytes: uint64(st.Size()),
		Target: r.target, BootedRootHash: r.rels[1].Manifest.RootHash}
}

func (r *replaceRig) replace(req ReplaceRequest) error {
	return r.rh.ReplaceDisk(context.Background(), r.journal, req)
}

// untouched fails the test when the replacement disk holds anything but zeros at its ends and no table.
func (r *replaceRig) untouched() {
	r.t.Helper()
	d, err := OpenDisk(r.target, false)
	if err != nil {
		r.t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.ReadTable(); !errors.Is(err, ErrNoGPT) {
		r.t.Errorf("the replacement disk got a partition table: %v", err)
	}
	if blank, err := d.blank(); err != nil || !blank {
		r.t.Errorf("the replacement disk was written to: blank=%v %v", blank, err)
	}
	for _, c := range r.run.calls {
		if strings.HasPrefix(c, "mkfs.fat") || strings.HasPrefix(c, "mdadm --add") {
			r.t.Errorf("a refused replacement ran %q", c)
		}
	}
}

func (r *replaceRig) record() *ReplaceRecord {
	r.t.Helper()
	rec, err := ReadReplaceRecord(r.journal)
	if err != nil || rec == nil {
		r.t.Fatalf("no replacement record: %v", err)
	}
	return rec
}

// espTree is the boot partition of a disk without what grub-install writes.
func (r *replaceRig) espTree(disk string) map[string]string {
	out := tree(r.t, ESPRoot(NodeName(disk, ESPIndex)))
	for k := range out {
		if strings.HasPrefix(k, "grub/i386-pc/") {
			delete(out, k)
		}
	}
	return out
}

// wantReplaced checks what a finished replacement leaves: the surviving disk's layout, its verified slots, its
// boot partition, a boot loader, and the new member added once.
func (r *replaceRig) wantReplaced() {
	r.t.Helper()
	srcT, tb := readTable(r.t, r.disks[0]), readTable(r.t, r.target)
	plan, err := planOf(srcT)
	if err != nil {
		r.t.Fatal(err)
	}
	if err = sameLayout(tb, plan); err != nil {
		r.t.Errorf("the replacement disk's table differs from the surviving disk's: %v", err)
	}
	rec := r.record()
	if tb.DiskID.String() != rec.DiskID || tb.DiskID == srcT.DiskID {
		r.t.Errorf("disk GUID %s, record %s, surviving disk %s", tb.DiskID, rec.DiskID, srcT.DiskID)
	}
	if rec.Status != ReplaceSucceeded || rec.Phase != ReplaceComplete || rec.Error != "" || rec.FinishedAt == nil {
		r.t.Errorf("record: %+v", rec)
	}
	d, err := OpenDisk(r.target, false)
	if err != nil {
		r.t.Fatal(err)
	}
	defer d.Close()
	for slot, rel := range map[string]*Release{"a": r.rels[1]} {
		if s, ok, err := slotSelected(d, rel.Manifest.RootHash); err != nil || !ok || s != slot {
			r.t.Errorf("slot %s of the replacement does not select release %d: %q %v %v", slot, rel.Manifest.Sequence, s, ok, err)
		}
		want, _ := os.ReadFile(rel.Erofs())
		if got := readPart(r.t, r.target, tb, "nex-root-"+slot, len(want)); !bytes.Equal(got, want) {
			r.t.Errorf("slot %s of the replacement does not hold release %d", slot, rel.Manifest.Sequence)
		}
	}
	if got, want := r.espTree(r.target), r.espTree(r.disks[0]); !reflect.DeepEqual(got, want) {
		r.t.Errorf("boot partitions differ: %v vs %v", keysOf(got), keysOf(want))
	}
	if r.boot == BIOS {
		head := diskHead(r.t, r.target, 446)
		if !hasGrubBootCode(head) {
			r.t.Error("the replacement disk has no GRUB boot code")
		}
		if n := r.run.count("grub-install"); n != 1 {
			r.t.Errorf("grub-install ran %d times", n)
		}
	} else if n := r.run.count("efibootmgr --create --disk " + r.target); n != 1 {
		r.t.Errorf("the firmware boot entry was registered %d times", n)
	}
	if n := r.run.count("mdadm --add " + mdDevice() + " " + NodeName(r.target, DataIndex)); n != r.adds {
		r.t.Errorf("the new member was added %d times", n)
	}
	if st, ok := (dataArray{dir: r.arrayDir()}).member(filepath.Base(NodeName(r.target, DataIndex))); !ok || st != "spare" {
		r.t.Errorf("the new member's state: %q %v", st, ok)
	}
}

// crashTrace counts the crash points a run passes and stops it at the nth pass of one of them.
type crashTrace struct {
	counts map[string]int
	stop   string
	nth    int
}

func traceCrashes(t *testing.T, stop string, nth int) *crashTrace {
	t.Helper()
	tr := &crashTrace{counts: map[string]int{}, stop: stop, nth: nth}
	crashPoint = func(p string) error {
		tr.counts[p]++
		if p == tr.stop && tr.counts[p] == tr.nth {
			return errors.New("power cut at " + p)
		}
		return nil
	}
	t.Cleanup(func() { crashPoint = nil })
	return tr
}

func TestReplaceDiskRebuildsTheReplacementFromTheSurvivor(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newReplaceRig(t, boot)
		tr := traceCrashes(t, "", 0)
		if err := r.replace(r.request("req-1")); err != nil {
			t.Fatal(err)
		}
		r.wantReplaced()
		if tr.counts["lay.wiped"] != 1 || r.run.count("mkfs.fat -F 32 -n NEXESP "+NodeName(r.target, ESPIndex)) != 1 {
			t.Errorf("the replacement was laid %d times and formatted %d times", tr.counts["lay.wiped"], r.run.count("mkfs.fat"))
		}
		if left := r.temps(0); len(left) != 0 {
			t.Errorf("leftover temp files %v", left)
		}
		// The mount of the boot partitions is released.
		if m, u := r.run.count("mount -t vfat"), r.run.count("umount "); m != u {
			t.Errorf("%d mounts, %d unmounts", m, u)
		}
		// The same request again replays the outcome and writes nothing.
		before := len(r.run.calls)
		if err := r.replace(r.request("req-1")); err != nil {
			t.Fatal(err)
		}
		if len(r.run.calls) != before {
			t.Errorf("a repeated request ran %v", r.run.calls[before:])
		}
	})
}

func TestReplaceDiskCopiesThePreviousSlotToo(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newReplaceRig(t, boot)
		rel2 := r.release(2)
		if _, err := r.host.StageSlot(context.Background(), rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
			t.Fatal(err)
		}
		if err := r.replace(r.request("req-1")); err != nil {
			t.Fatal(err)
		}
		d, err := OpenDisk(r.target, false)
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		for hash, slot := range map[string]string{r.rels[1].Manifest.RootHash: "a", rel2.Manifest.RootHash: "b"} {
			if s, ok, err := slotSelected(d, hash); err != nil || !ok || s != slot {
				t.Errorf("slot %s of the replacement: %q %v %v", slot, s, ok, err)
			}
		}
		want, _ := os.ReadFile(rel2.Erofs())
		if got := readPart(t, r.target, readTable(t, r.target), "nex-root-b", len(want)); !bytes.Equal(got, want) {
			t.Error("slot b of the replacement does not hold the staged release")
		}
	})
}

func TestReplaceDiskSkipsAPreviousSlotThatDoesNotVerify(t *testing.T) {
	r := newReplaceRig(t, UEFI)
	rel2 := r.release(2)
	if _, err := r.host.StageSlot(context.Background(), rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
		t.Fatal(err)
	}
	flipByte(t, r.disks[0], "nex-root-b", 100)
	if err := r.replace(r.request("req-1")); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDisk(r.target, false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, ok, _ := slotSelected(d, rel2.Manifest.RootHash); ok {
		t.Error("a slot that did not verify on the surviving disk was selected on the replacement")
	}
	if _, ok, _ := slotSelected(d, r.rels[1].Manifest.RootHash); !ok {
		t.Error("the running slot was not copied")
	}
}

// flipByte inverts one byte of a partition on a file-backed disk.
func flipByte(t *testing.T, disk, label string, off int64) {
	t.Helper()
	tb := readTable(t, disk)
	p := part(t, tb, label)
	f, err := os.OpenFile(disk, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b := make([]byte, 1)
	at := tb.Offset(p) + off
	if _, err = f.ReadAt(b, at); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	if _, err = f.WriteAt(b, at); err != nil {
		t.Fatal(err)
	}
}

// A power cut at every named step, then a retry: the replacement always ends identical, nothing destructive is
// repeated, the disk the host runs from is never written, and the other disk's request continues it too.
func TestReplaceDiskInterruptedAtEveryStepConverges(t *testing.T) {
	points := []string{"replace.accepted", "replace.laying", "lay.wiped", "replace.laid", "slot.invalidated", "slot.written",
		"replace.slot", "replace.formatted", "replace.converged", "replace.added"}
	for _, boot := range []BootMethod{UEFI, BIOS} {
		bootPoints := points
		if boot == BIOS {
			bootPoints = append(append([]string{}, points...), "converge.files", "converge.env", "converge.entries")
		}
		for _, point := range bootPoints {
			for _, again := range []string{"same request", "new request"} {
				t.Run(boot.Name()+"/"+point+"/"+again, func(t *testing.T) {
					r := newReplaceRig(t, boot)
					tr := traceCrashes(t, point, 1)
					err := r.replace(r.request("req-1"))
					var pc powerCut
					if !errors.As(err, &pc) {
						t.Fatalf("the replacement did not stop at %s: %v", point, err)
					}
					if rec := r.record(); rec.Status != ReplaceRunning {
						t.Fatalf("an interrupted replacement is %q, not running", rec.Status)
					}
					tr.stop = ""
					id := "req-1"
					if again == "new request" {
						id = "req-2"
					}
					if err = r.replace(r.request(id)); err != nil {
						t.Fatalf("the retry after %s failed: %v", point, err)
					}
					r.wantReplaced()
					want := 1
					if point == "lay.wiped" {
						want = 2 // the cut came after the wipe, before the table was written
					}
					if n := tr.counts["lay.wiped"]; n != want {
						t.Errorf("after a cut at %s the disk was laid %d times, want %d", point, n, want)
					}
					if n := r.run.count("mkfs.fat"); n > 1 {
						t.Errorf("the boot partition was formatted %d times", n)
					}
				})
			}
		}
	}
}

func TestReplaceDiskRefusesTheDiskTheHostRunsFrom(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newReplaceRig(t, boot)
		req := r.request("req-1")
		req.Target, req.SizeBytes = r.disks[0], uint64(testDisk)
		err := r.replace(req)
		var ref *refusal
		if !errors.As(err, &ref) || !strings.Contains(err.Error(), "running system started from") {
			t.Fatalf("the booted disk was not refused: %v", err)
		}
		// A symlink to the same disk is the same disk.
		link := filepath.Join(t.TempDir(), "alias.img")
		if err = os.Symlink(r.disks[0], link); err != nil {
			t.Skip("no symlinks")
		}
		req.Target, req.RequestID = link, "req-2"
		if err = r.replace(req); !errors.As(err, &ref) || !strings.Contains(err.Error(), "running system started from") {
			t.Fatalf("an alias of the booted disk was not refused: %v", err)
		}
		for _, c := range r.run.calls {
			if strings.HasPrefix(c, "mkfs.fat") || strings.HasPrefix(c, "mdadm --add") {
				t.Errorf("a refused replacement ran %q", c)
			}
		}
	})
}

func TestReplaceDiskNeedsConfirmationToEraseAnUsedDisk(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T, r *replaceRig){
		"data without a table": func(t *testing.T, r *replaceRig) {
			f, _ := os.OpenFile(r.target, os.O_WRONLY, 0)
			defer f.Close()
			f.WriteAt([]byte("an old file system"), 1024)
		},
		"a partition table of another host": func(t *testing.T, r *replaceRig) {
			other := blankDisk(t, "other.img", testDisk)
			if err := (&Writer{Runner: &recorder{}, Boot: r.boot}).Install(context.Background(),
				Options{Layout: LayoutRAID1, Disks: []string{other, blankDisk(t, "other2.img", testDisk)}, ImageDir: makeRelease(t, "x", 9, 9, 16)}); err != nil {
				t.Fatal(err)
			}
			r.target = other
		},
		"the failed disk itself": func(t *testing.T, r *replaceRig) { r.target = r.disks[1] },
	} {
		t.Run(name, func(t *testing.T) {
			r := newReplaceRig(t, UEFI)
			prepare(t, r)
			req := r.request("req-1")
			err := r.replace(req)
			var ref *refusal
			if !errors.As(err, &ref) || !strings.Contains(err.Error(), "confirm erasing") {
				t.Fatalf("an unconfirmed erase was not refused: %v", err)
			}
			if n := r.run.count("mkfs.fat") + r.run.count("mdadm --add"); n != 0 {
				t.Fatalf("a refused replacement ran %d commands", n)
			}
			if name == "data without a table" {
				if head := diskHead(t, r.target, 1100)[1024:]; !bytes.HasPrefix(head, []byte("an old file system")) {
					t.Fatal("a refused replacement overwrote the disk")
				}
			}
			// The same disk with the typed confirmation is replaced.
			req.RequestID, req.Erase = "req-2", true
			if err = r.replace(req); err != nil {
				t.Fatalf("a confirmed erase failed: %v", err)
			}
			r.wantReplaced()
		})
	}
}

func TestReplaceDiskRefusesBeforeWriting(t *testing.T) {
	for name, c := range map[string]struct {
		prepare func(t *testing.T, r *replaceRig, req *ReplaceRequest)
		want    string
	}{
		"the selected size is not the disk's": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) { req.SizeBytes++ }, "not the"},
		"a disk too small for the layout": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) {
			req.Target = blankDisk(t, "small.img", testDisk-3*GiB)
			req.SizeBytes = uint64(testDisk - 3*GiB)
		}, "too small"},
		"a healthy mirror": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) {
			os.WriteFile(filepath.Join(r.arrayDir(), "degraded"), []byte("0\n"), 0o644)
		}, "not degraded"},
		"a mirror that is still resyncing": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) {
			os.WriteFile(filepath.Join(r.arrayDir(), "sync_action"), []byte("recover\n"), 0o644)
		}, "still recover"},
		"a host without a mirror": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) {
			os.Remove(mdDevice())
		}, "no mirrored nex-data"},
		"a mirror that is not RAID1": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) {
			os.WriteFile(filepath.Join(r.arrayDir(), "level"), []byte("raid5\n"), 0o644)
		}, "only a RAID1 mirror"},
		"a running slot that does not verify": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) {
			flipByte(t, r.disks[0], "nex-root-a", 100)
		}, "does not verify"},
		"a root hash the surviving disk does not hold": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) {
			req.BootedRootHash = strings.Repeat("ab", 32)
		}, "running root hash"},
		"a boot entry that is not blessed yet": {func(t *testing.T, r *replaceRig, req *ReplaceRequest) {
			root := ESPRoot(NodeName(r.disks[0], ESPIndex))
			if err := writeGrubEnv(filepath.Join(root, filepath.FromSlash(biosEnv)), grubEnv{triesVar("a"): "3"}); err != nil {
				t.Fatal(err)
			}
		}, "not blessed"},
	} {
		for _, boot := range []BootMethod{UEFI, BIOS} {
			t.Run(name+"/"+boot.Name(), func(t *testing.T) {
				r := newReplaceRig(t, boot)
				req := r.request("req-1")
				c.prepare(t, r, &req)
				tr := traceCrashes(t, "", 0)
				err := r.replace(req)
				var ref *refusal
				if !errors.As(err, &ref) || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("want a refusal containing %q, got %v", c.want, err)
				}
				if tr.counts["lay.wiped"] != 0 {
					t.Error("the disk was wiped")
				}
				if req.Target == r.target {
					r.untouched()
				}
				if rec := r.record(); rec.Status != ReplaceFailed || rec.Error != err.Error() || rec.Phase != ReplaceAccepted {
					t.Errorf("record of a refusal: %+v", rec)
				}
			})
		}
	}
}

func TestReplaceDiskOfAnotherDiskWhileOneRunsIsRefused(t *testing.T) {
	r := newReplaceRig(t, UEFI)
	traceCrashes(t, "replace.slot", 1)
	if err := r.replace(r.request("req-1")); err == nil {
		t.Fatal("no cut")
	}
	crashPoint = nil
	req := r.request("req-2")
	req.ByID = "/dev/disk/by-id/ata-OTHER"
	err := r.replace(req)
	var ref *refusal
	if !errors.As(err, &ref) || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("a second replacement while one is unfinished: %v", err)
	}
	// The unfinished one is still continued by its own disk.
	if err = r.replace(r.request("req-3")); err != nil {
		t.Fatal(err)
	}
	r.wantReplaced()
}

func TestReplaceDiskRecordsAFailureAndAnotherRequestContinuesIt(t *testing.T) {
	r := newReplaceRig(t, UEFI)
	r.run.fail = map[string]error{"mdadm --add": errors.New("mdadm: add failed")}
	tr := traceCrashes(t, "", 0)
	err := r.replace(r.request("req-1"))
	if err == nil || !strings.Contains(err.Error(), "add failed") {
		t.Fatalf("%v", err)
	}
	rec := r.record()
	if rec.Status != ReplaceFailed || rec.Phase != ReplaceData || !strings.Contains(rec.Error, "add failed") || rec.FinishedAt == nil {
		t.Fatalf("record: %+v", rec)
	}
	// The same request replays the failure without touching anything.
	calls := len(r.run.calls)
	if err2 := r.replace(r.request("req-1")); err2 == nil || err2.Error() != err.Error() || len(r.run.calls) != calls {
		t.Fatalf("replay: %v (%d new calls)", err2, len(r.run.calls)-calls)
	}
	// A new request for the same disk continues: the disk is not laid or formatted again.
	r.run.fail, r.adds = nil, 2
	if err = r.replace(r.request("req-2")); err != nil {
		t.Fatal(err)
	}
	r.wantReplaced()
	if tr.counts["lay.wiped"] != 1 || r.run.count("mkfs.fat") != 1 {
		t.Errorf("the continuation laid %d times and formatted %d times", tr.counts["lay.wiped"], r.run.count("mkfs.fat"))
	}
}

func TestReplaceDiskOfACancelledContextStaysRunning(t *testing.T) {
	r := newReplaceRig(t, UEFI)
	ctx, cancel := context.WithCancel(context.Background())
	crashPoint = func(p string) error {
		if p == "replace.slot" {
			cancel()
		}
		return nil
	}
	t.Cleanup(func() { crashPoint = nil })
	if err := r.rh.ReplaceDisk(ctx, r.journal, r.request("req-1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if rec := r.record(); rec.Status != ReplaceRunning {
		t.Fatalf("a cancelled replacement is %q", rec.Status)
	}
	crashPoint = nil
	if err := r.replace(r.request("req-1")); err != nil {
		t.Fatal(err)
	}
	r.wantReplaced()
}

func TestReplaceDiskHoldsTheDiskLock(t *testing.T) {
	defer func(w time.Duration) { diskLockWait = w }(diskLockWait)
	diskLockWait = 50 * time.Millisecond
	r := newReplaceRig(t, UEFI)
	unlock, err := lockDisks(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.replace(r.request("req-1")); err == nil || !strings.Contains(err.Error(), "system disk operation") {
		t.Errorf("a replacement ran while the disks were locked: %v", err)
	}
	if _, err = r.host.StageSlot(context.Background(), r.release(2), r.rels[1].Manifest.RootHash, 1); err == nil ||
		!errors.Is(err, syscall.EWOULDBLOCK) {
		t.Errorf("an update stage ran while the disks were locked: %v", err)
	}
	r.untouched()
	unlock()

	// The other way round: a replacement that is running blocks the update steps and the ESP sync.
	traceCrashes(t, "", 0)
	blocked := make(chan error, 2)
	crashPoint = func(p string) error {
		if p == "replace.laid" {
			_, err := r.host.StageSlot(context.Background(), r.release(2), r.rels[1].Manifest.RootHash, 1)
			blocked <- err
			blocked <- r.host.Arm(context.Background(), r.rels[1])
		}
		return nil
	}
	if err = r.replace(r.request("req-2")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-blocked; err == nil || !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Errorf("an update step ran during a replacement: %v", err)
		}
	}
	r.wantReplaced()
}

func TestESPSyncLeavesTheOtherBootPartitionsToARunningDiskOperation(t *testing.T) {
	defer func(w time.Duration) { diskLockWait = w }(diskLockWait)
	diskLockWait = 50 * time.Millisecond
	sandbox(t)
	booted, other := t.TempDir(), t.TempDir()
	fakeBIOSSystem(t, "vda1", "", map[string]string{"vda1": booted, "vdb1": other})
	rec := &recorder{}
	unlock, err := lockDisks(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = (&Writer{Runner: rec}).ESPSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("the sync ran %v during a disk operation", rec.calls)
	}
}

func TestReplaceRecordSurvivesAnUnreadableFile(t *testing.T) {
	r := newReplaceRig(t, UEFI)
	os.MkdirAll(filepath.Dir(r.journal), 0o755)
	os.WriteFile(r.journal, []byte("{not json"), 0o600)
	if err := r.replace(r.request("req-1")); err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("%v", err)
	}
	r.untouched()
}

func TestPlanOfRequiresTheNexLayout(t *testing.T) {
	r := newReplaceRig(t, UEFI)
	tb := readTable(t, r.disks[0])
	plan, err := planOf(tb)
	if err != nil || len(plan) != 7 || plan[0].Label != ESPLabel || plan[DataIndex-1].Label != DataLabel {
		t.Fatalf("%+v %v", plan, err)
	}
	tb.Parts[0].Label = "other"
	if _, err = planOf(tb); err == nil {
		t.Error("a table without the layout was accepted")
	}
	short := &Table{Parts: tb.Parts[:3]}
	if _, err = planOf(short); err == nil {
		t.Error("a short table was accepted")
	}
}

func TestAbandonReplaceFailsOnlyARunningReplacement(t *testing.T) {
	r := newReplaceRig(t, UEFI)
	if err := AbandonReplace(r.journal, "gone"); err != nil {
		t.Fatalf("no record: %v", err)
	}
	if rec, err := ReadReplaceRecord(r.journal); rec != nil || err != nil {
		t.Fatalf("an abandon invented a record: %+v %v", rec, err)
	}
	traceCrashes(t, "replace.slot", 1)
	if err := r.replace(r.request("req-1")); err == nil {
		t.Fatal("no cut")
	}
	crashPoint = nil
	if err := AbandonReplace(r.journal, "the disk was removed"); err != nil {
		t.Fatal(err)
	}
	rec := r.record()
	if rec.Status != ReplaceFailed || rec.Error != "the disk was removed" || rec.FinishedAt == nil || rec.Phase != ReplaceSlots {
		t.Fatalf("record: %+v", rec)
	}
	// Another disk is no longer kept out by it, and a finished record is not touched again.
	req := r.request("req-2")
	req.ByID = "/dev/disk/by-id/ata-ANOTHER"
	if err := r.replace(req); err == nil || !strings.Contains(err.Error(), "confirm erasing") {
		t.Fatalf("the half-laid disk, now another request's, was erased without a confirmation: %v", err)
	}
	req.RequestID, req.Erase = "req-3", true
	if err := r.replace(req); err != nil {
		t.Fatal(err)
	}
	if err := AbandonReplace(r.journal, "again"); err != nil {
		t.Fatal(err)
	}
	if rec = r.record(); rec.Status != ReplaceSucceeded || rec.Error != "" {
		t.Fatalf("record: %+v", rec)
	}
}
