// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseUKINameAndBootOrder(t *testing.T) {
	for name, want := range map[string]UKIEntry{
		"nex-core_dev_7.efi":       {Name: "nex-core_dev_7.efi", Label: "dev", Sequence: 7},
		"nex-core_1.2_rc_9+3.efi":  {Name: "nex-core_1.2_rc_9+3.efi", Label: "1.2_rc", Sequence: 9, Counted: true, Tries: 3},
		"nex-core_dev_8+1-2.efi":   {Name: "nex-core_dev_8+1-2.efi", Label: "dev", Sequence: 8, Counted: true, Tries: 1, Done: 2},
		"nex-core_dev_8+0-3.efi":   {Name: "nex-core_dev_8+0-3.efi", Label: "dev", Sequence: 8, Counted: true, Done: 3},
		"nex-core_dev_8+3.efi.new": {},
		"linux-nex-core_dev_8.efi": {},
		"nex-core_dev_x.efi":       {},
	} {
		got, ok := ParseUKIName(name)
		if ok != (want.Name != "") || got != want {
			t.Errorf("%s: %+v %v, want %+v", name, got, ok, want)
		}
	}
	order := BootOrder([]UKIEntry{{Sequence: 9, Counted: true, Tries: 3}, {Sequence: 8}, {Sequence: 7}})
	if order[0].Sequence != 9 || order[1].Sequence != 8 {
		t.Fatalf("a fresh counted entry did not sort first: %+v", order)
	}
	order = BootOrder([]UKIEntry{{Sequence: 9, Counted: true, Done: 3}, {Sequence: 8}})
	if order[0].Sequence != 8 {
		t.Fatalf("an exhausted entry still sorts first: %+v", order)
	}
}

// updateRig is an installed host (release 1 in slot A) with a Host over its file-backed disks.
type updateRig struct {
	t     *testing.T
	boot  BootMethod
	disks []string
	host  *Host
	rels  map[int]*Release
}

// eachBoot runs a scenario under both boot methods: the update engine must behave the same on either.
func eachBoot(t *testing.T, scenario func(t *testing.T, boot BootMethod)) {
	for _, boot := range []BootMethod{UEFI, BIOS} {
		t.Run(boot.Name(), func(t *testing.T) { scenario(t, boot) })
	}
}

// writer is a Writer with the rig's boot method.
func (r *updateRig) writer(run Runner) *Writer { return &Writer{Runner: run, Boot: r.boot} }

func newRig(t *testing.T, boot BootMethod, layout string, n int) *updateRig {
	t.Helper()
	sandbox(t)
	r := &updateRig{t: t, boot: boot, rels: map[int]*Release{}}
	for i := range n {
		// Distinct base names: the ESP of a node is mounted under its base name.
		p := filepath.Join(t.TempDir(), fmt.Sprintf("disk%d.img", i))
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, testDisk); err != nil {
			t.Fatal(err)
		}
		r.disks = append(r.disks, p)
	}
	rel1 := makeRelease(t, "r1", 1, 1, 16)
	w := r.writer(&recorder{})
	if err := w.Install(context.Background(), Options{Layout: layout, Disks: r.disks, ImageDir: rel1}); err != nil {
		t.Fatal(err)
	}
	r.rels[1], _ = ReadRelease(rel1)
	r.host = &Host{W: w,
		DiskPaths: func() ([]string, error) { return r.disks, nil },
		ESPs: func() ([]string, error) {
			var out []string
			for _, d := range r.disks {
				out = append(out, NodeName(d, 1))
			}
			return out, nil
		}}
	return r
}

func (r *updateRig) release(seq int) *Release {
	r.t.Helper()
	dir := makeRelease(r.t, fmt.Sprintf("r%d", seq), seq, int64(seq), 16)
	rel, err := ReadRelease(dir)
	if err != nil {
		r.t.Fatal(err)
	}
	r.rels[seq] = rel
	return rel
}

func (r *updateRig) esp(i int) string { return ESPRoot(NodeName(r.disks[i], 1)) }

func (r *updateRig) entries(i int) []UKIEntry {
	us, err := r.boot.entries(r.esp(i), "")
	if err != nil {
		r.t.Fatal(err)
	}
	return us
}

// defaultSequence is what the boot loader would boot from disk i, after checking that this disk can
// really boot it: its slot carries the root hash of that release and the bytes of its erofs.
func (r *updateRig) defaultSequence(i int) int {
	r.t.Helper()
	us := r.entries(i)
	if len(us) == 0 {
		r.t.Fatalf("disk %d has no boot entry", i)
	}
	e := BootOrder(us)[0]
	rel := r.rels[e.Sequence]
	if rel == nil {
		r.t.Fatalf("disk %d boots unknown sequence %d", i, e.Sequence)
	}
	d, err := OpenDisk(r.disks[i], false)
	if err != nil {
		r.t.Fatal(err)
	}
	defer d.Close()
	slot, ok, err := slotSelected(d, rel.Manifest.RootHash)
	if err != nil || !ok {
		r.t.Fatalf("disk %d boots sequence %d but no slot is selected for it (%v)", i, e.Sequence, err)
	}
	want, _ := os.ReadFile(rel.Erofs())
	tb := readTable(r.t, r.disks[i])
	if got := readPart(r.t, r.disks[i], tb, "nex-root-"+slot, len(want)); !bytes.Equal(got, want) {
		r.t.Fatalf("disk %d: slot %s does not hold sequence %d", i, slot, e.Sequence)
	}
	return e.Sequence
}

// markTried is what the boot loader does when it boots the entry of seq once.
func (r *updateRig) markTried(i, seq int) {
	r.t.Helper()
	var err error
	if r.boot == BIOS {
		err = biosMarkTried(r.esp(i), seq)
	} else {
		err = MarkTried(r.esp(i), seq)
	}
	if err != nil {
		r.t.Fatal(err)
	}
}

// bless is what the bless step does to the entry of seq on disk i.
func (r *updateRig) bless(i, seq int) {
	r.t.Helper()
	var err error
	if r.boot == BIOS {
		err = biosBless(r.esp(i), seq)
	} else {
		err = Bless(r.esp(i), seq)
	}
	if err != nil {
		r.t.Fatal(err)
	}
}

// temps lists the half-written files on the boot partition of disk i.
func (r *updateRig) temps(i int) []string {
	var out []string
	filepath.WalkDir(r.esp(i), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, tempSuffix) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (r *updateRig) wantDefaults(want ...int) {
	r.t.Helper()
	for i, w := range want {
		if got := r.defaultSequence(i); got != w {
			r.t.Errorf("disk %d would boot sequence %d, want %d", i, got, w)
		}
	}
}

func withCrash(t *testing.T, point string, nth int) {
	t.Helper()
	n := 0
	crashPoint = func(p string) error {
		if p != point {
			return nil
		}
		if n++; n == nth {
			return errors.New("power cut at " + p)
		}
		return nil
	}
	t.Cleanup(func() { crashPoint = nil })
}

func TestStageArmBlessAndRollback(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newRig(t, boot, LayoutRAID1, 2)
		ctx := context.Background()
		r.wantDefaults(1, 1)
		rel2 := r.release(2)
		slot, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1)
		if err != nil || slot != "b" {
			t.Fatalf("stage: %q %v", slot, err)
		}
		r.wantDefaults(1, 1) // staged: nothing is armed
		if err = r.host.Arm(ctx, rel2); err != nil {
			t.Fatal(err)
		}
		r.wantDefaults(2, 2)
		for i := range r.disks {
			var armed, old bool
			for _, e := range r.entries(i) {
				armed = armed || e.Sequence == 2 && e.Counted && e.Tries == ArmTries
				old = old || e.Sequence == 1 && e.Blessed()
			}
			if !armed || !old {
				t.Errorf("disk %d: expected the armed entry beside the old one, got %+v", i, r.entries(i))
			}
		}
		// Three failed tries: the loader falls back to the old version on both disks.
		for range ArmTries {
			for i := range r.disks {
				r.markTried(i, 2)
			}
		}
		r.wantDefaults(1, 1)
		// A later try succeeds instead: re-arm, then bless.
		if err = r.host.Arm(ctx, rel2); err != nil {
			t.Fatal(err)
		}
		r.wantDefaults(2, 2)
		for i := range r.disks {
			r.bless(i, 2)
		}
		r.wantDefaults(2, 2)

		// Operator rollback arms the retained previous version.
		if err = r.host.Rollback(ctx, 2, 1, r.rels[1].Manifest.RootHash); err != nil {
			t.Fatal(err)
		}
		r.wantDefaults(1, 1)
		if e := BootOrder(r.entries(0))[0]; !e.Counted || e.Tries != ArmTries {
			t.Errorf("rolled-back entry is not armed with a counter: %+v", e)
		}
		// Staging the next release prunes everything but the running version and reuses slot B, which
		// holds the version just rolled back from. Running is now 1, in slot A.
		rel3 := r.release(3)
		if slot, err = r.host.StageSlot(ctx, rel3, r.rels[1].Manifest.RootHash, 1); err != nil || slot != "b" {
			t.Fatalf("stage after rollback: %q %v", slot, err)
		}
		for i := range r.disks {
			for _, e := range r.entries(i) {
				if e.Sequence != 1 {
					t.Errorf("disk %d keeps a stale entry %s over the slot being rewritten", i, e.Name)
				}
			}
		}
		r.wantDefaults(1, 1)
		// The previous slot was torn by that stage: rolling back to 2 is refused, nothing changes.
		if err = r.host.Rollback(ctx, 1, 2, r.rels[2].Manifest.RootHash); err == nil || !strings.Contains(err.Error(), "no slot holds root hash") {
			t.Fatalf("rollback to a rewritten slot: %v", err)
		}
		r.wantDefaults(1, 1)
	})
}

func TestArmRefusesWhenTheSlotIsNotStaged(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newRig(t, boot, LayoutSingle, 1)
		rel2 := r.release(2)
		if err := r.host.Arm(context.Background(), rel2); err == nil || !strings.Contains(err.Error(), "no slot holds root hash") {
			t.Fatalf("armed a release that is not on the disk: %v", err)
		}
		r.wantDefaults(1)
		if len(r.entries(0)) != 1 {
			t.Fatalf("entry written for an unstaged slot: %+v", r.entries(0))
		}
	})
}

func TestStageRefusesUnknownRunningSlot(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newRig(t, boot, LayoutSingle, 1)
		rel2 := r.release(2)
		before := diskDigest(t, r.disks[0])
		if _, err := r.host.StageSlot(context.Background(), rel2, strings.Repeat("ab", 32), 1); err == nil {
			t.Fatal("staged without knowing which slot runs")
		}
		if diskDigest(t, r.disks[0]) != before {
			t.Fatal("disk changed by a refused stage")
		}
	})
}

// A power cut at every step of staging and arming: each disk still boots a complete version.
func TestCrashAtEveryUpdateStepLeavesABootableVersion(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		cases := []struct {
			name  string
			point string
			nth   int
			want  []int // sequence each disk would boot afterwards
			slot  bool  // fail inside the slot write instead of at a crash point
		}{
			{name: "after the stale entries are pruned", point: "stage.pruned", nth: 1, want: []int{1, 1}},
			{name: "slot write torn", slot: true, want: []int{1, 1}},
			{name: "half-written .new on the first ESP", point: "arm.temp", nth: 1, want: []int{1, 1}},
			{name: "half-written .new on the second ESP", point: "arm.temp", nth: 2, want: []int{2, 1}},
			{name: "after the first ESP is armed", point: "arm.esp-done", nth: 1, want: []int{2, 1}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				r := newRig(t, boot, LayoutRAID1, 2)
				ctx := context.Background()
				rel2 := r.release(2)
				booted := r.rels[1].Manifest.RootHash
				rec := &recorder{fail: map[string]error{}}
				if c.slot {
					// The settle after the invalidation fails: the slot is torn, nothing was armed.
					rec.fail["udevadm settle"] = errors.New("power cut")
				}
				r.host.W = r.writer(rec)
				if c.point != "" {
					withCrash(t, c.point, c.nth)
				}
				_, err := r.host.StageSlot(ctx, rel2, booted, 1)
				if c.slot {
					if err == nil {
						t.Fatal("slot write did not fail")
					}
					r.wantDefaults(c.want...)
					return
				}
				if c.point == "stage.pruned" {
					if err == nil {
						t.Fatal("stage did not stop")
					}
					r.wantDefaults(c.want...)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = r.host.Arm(ctx, rel2); err == nil {
					t.Fatal("arm did not stop")
				}
				r.wantDefaults(c.want...)
				// A retry after the interruption completes the arm everywhere.
				crashPoint = nil
				if err = r.host.Arm(ctx, rel2); err != nil {
					t.Fatal(err)
				}
				r.wantDefaults(2, 2)
				for i := range r.disks {
					if left := r.temps(i); len(left) != 0 {
						t.Errorf("disk %d: leftover temp files %v", i, left)
					}
				}
			})
		}
	})
}

func TestCrashDuringRollbackLeavesABootableVersion(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		for _, c := range []struct {
			point string
			nth   int
			want  []int
		}{
			{"rollback.armed", 1, []int{2, 2}}, // previous re-armed but the running one is not yet marked: it still sorts first
			{"rollback.esp-done", 1, []int{1, 2}},
		} {
			t.Run(c.point, func(t *testing.T) {
				r := newRig(t, boot, LayoutRAID1, 2)
				ctx := context.Background()
				rel2 := r.release(2)
				if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
					t.Fatal(err)
				}
				if err := r.host.Arm(ctx, rel2); err != nil {
					t.Fatal(err)
				}
				for i := range r.disks {
					r.bless(i, 2)
				}
				withCrash(t, c.point, c.nth)
				if err := r.host.Rollback(ctx, 2, 1, r.rels[1].Manifest.RootHash); err == nil {
					t.Fatal("rollback did not stop")
				}
				r.wantDefaults(c.want...)
				crashPoint = nil
				if err := r.host.Rollback(ctx, 2, 1, r.rels[1].Manifest.RootHash); err != nil {
					t.Fatal(err)
				}
				r.wantDefaults(1, 1)
			})
		}
	})
}

// faultRunner is a recorder whose umount fails for the calls that failUmount selects (1-based).
type faultRunner struct {
	recorder
	umounts    int
	failUmount func(n int) bool
}

func (f *faultRunner) Run(ctx context.Context, name string, args ...string) error {
	if name == "umount" {
		f.mu.Lock()
		f.umounts++
		n := f.umounts
		f.mu.Unlock()
		if f.failUmount(n) {
			return errors.New("umount: device busy")
		}
	}
	return f.recorder.Run(ctx, name, args...)
}

// An Arm that fails on the second ESP takes the entry back from the first one too: the release stays
// staged and every disk boots what it booted before.
func TestArmThatFailsOnALaterESPLeavesNothingArmed(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newRig(t, boot, LayoutRAID1, 2)
		ctx := context.Background()
		rel2 := r.release(2)
		if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
			t.Fatal(err)
		}
		// Arm: ESP 1 mounts and unmounts (umount 1), ESP 2 is written and its umount fails (umount 2); the
		// undo then succeeds on both.
		r.host.W = r.writer(&faultRunner{recorder: recorder{fail: map[string]error{}}, failUmount: func(n int) bool { return n == 2 }})
		err := r.host.Arm(ctx, rel2)
		if err == nil || errors.Is(err, ErrArmNotUndone) || !strings.Contains(err.Error(), "umount") {
			t.Fatalf("arm: %v", err)
		}
		r.wantDefaults(1, 1)
		for i := range r.disks {
			for _, e := range r.entries(i) {
				if e.Sequence == 2 {
					t.Errorf("disk %d keeps the entry %s of the failed arm", i, e.Name)
				}
			}
			if left := r.temps(i); len(left) != 0 {
				t.Errorf("disk %d: leftover temp files %v", i, left)
			}
		}
		// The release is still staged: the same Arm now works.
		r.host.W = r.writer(&recorder{})
		if err = r.host.Arm(ctx, rel2); err != nil {
			t.Fatal(err)
		}
		r.wantDefaults(2, 2)
	})
}

// When the undo fails as well, the error says so, because a disk may still boot the release.
func TestArmThatCannotBeUndoneSaysSo(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newRig(t, boot, LayoutRAID1, 2)
		ctx := context.Background()
		rel2 := r.release(2)
		if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
			t.Fatal(err)
		}
		// Every umount from the second ESP's on fails: the arm of ESP 2 fails, and so does the undo of both ESPs.
		r.host.W = r.writer(&faultRunner{recorder: recorder{fail: map[string]error{}}, failUmount: func(n int) bool { return n >= 2 }})
		err := r.host.Arm(ctx, rel2)
		if !errors.Is(err, ErrArmNotUndone) {
			t.Fatalf("arm: %v", err)
		}
	})
}

// A power cut stops the process: the undo must not run for it.
func TestPowerCutDuringArmIsNotUndone(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newRig(t, boot, LayoutRAID1, 2)
		ctx := context.Background()
		rel2 := r.release(2)
		if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
			t.Fatal(err)
		}
		withCrash(t, "arm.esp-done", 1)
		if err := r.host.Arm(ctx, rel2); err == nil || errors.Is(err, ErrArmNotUndone) {
			t.Fatalf("arm: %v", err)
		}
		r.wantDefaults(2, 1)
	})
}

// Rollback tells a failure that changed nothing from one that reached some ESPs: the caller keeps the layers
// of the running release only for the first, and repeating the call finishes the second.
func TestRollbackErrorSaysWhetherAnESPWasChanged(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		arm := func(t *testing.T) *updateRig {
			r := newRig(t, boot, LayoutRAID1, 2)
			ctx := context.Background()
			rel2 := r.release(2)
			if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
				t.Fatal(err)
			}
			if err := r.host.Arm(ctx, rel2); err != nil {
				t.Fatal(err)
			}
			for i := range r.disks {
				r.bless(i, 2)
			}
			return r
		}
		// Rollback mounts each ESP once to check it (umounts 1, 2) and once to switch it (umounts 3, 4).
		for _, c := range []struct {
			name    string
			umount  int
			partial bool
			want    []int
		}{
			{"check of the first ESP", 1, false, []int{2, 2}},
			{"check of the second ESP", 2, false, []int{2, 2}},
			{"switching the first ESP", 3, true, nil},
			{"switching the second ESP", 4, true, nil},
		} {
			t.Run(c.name, func(t *testing.T) {
				r := arm(t)
				ctx := context.Background()
				r.host.W = r.writer(&faultRunner{recorder: recorder{fail: map[string]error{}}, failUmount: func(n int) bool { return n == c.umount }})
				err := r.host.Rollback(ctx, 2, 1, r.rels[1].Manifest.RootHash)
				if err == nil || errors.Is(err, ErrRollbackPartial) != c.partial {
					t.Fatalf("rollback: %v (partial wanted %v)", err, c.partial)
				}
				if c.want != nil {
					r.wantDefaults(c.want...)
				}
				r.host.W = r.writer(&recorder{})
				if err = r.host.Rollback(ctx, 2, 1, r.rels[1].Manifest.RootHash); err != nil {
					t.Fatalf("repeated rollback: %v", err)
				}
				r.wantDefaults(1, 1)
			})
		}
	})
}

// A power cut during Rollback is no partial error to act on: the process is gone.
func TestPowerCutDuringRollbackIsNotReportedAsPartial(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newRig(t, boot, LayoutRAID1, 2)
		ctx := context.Background()
		rel2 := r.release(2)
		if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
			t.Fatal(err)
		}
		if err := r.host.Arm(ctx, rel2); err != nil {
			t.Fatal(err)
		}
		for i := range r.disks {
			r.bless(i, 2)
		}
		withCrash(t, "rollback.esp-done", 1)
		if err := r.host.Rollback(ctx, 2, 1, r.rels[1].Manifest.RootHash); err == nil || errors.Is(err, ErrRollbackPartial) {
			t.Fatalf("rollback: %v", err)
		}
	})
}

// Disarm is the way out of an Arm that could not be undone: afterwards no disk boots the release, and a disk
// that cannot be cleaned does not stop the others from being cleaned.
func TestDisarmTakesTheEntryOffEveryESP(t *testing.T) {
	eachBoot(t, func(t *testing.T, boot BootMethod) {
		r := newRig(t, boot, LayoutRAID1, 2)
		ctx := context.Background()
		rel2 := r.release(2)
		if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
			t.Fatal(err)
		}
		r.host.W = r.writer(&faultRunner{recorder: recorder{fail: map[string]error{}}, failUmount: func(n int) bool { return n >= 2 }})
		if err := r.host.Arm(ctx, rel2); !errors.Is(err, ErrArmNotUndone) {
			t.Fatalf("arm: %v", err)
		}
		// The first ESP keeps the entry, as the engine's record of an arm_incomplete release says it may.
		r.host.W = r.writer(&faultRunner{recorder: recorder{fail: map[string]error{}}, failUmount: func(n int) bool { return n == 2 }})
		if err := r.host.Disarm(ctx, 2); err == nil {
			t.Fatal("a disk that failed to unmount was reported clean")
		}
		r.host.W = r.writer(&recorder{})
		if err := r.host.Disarm(ctx, 2); err != nil {
			t.Fatal(err)
		}
		r.wantDefaults(1, 1)
		for i := range r.disks {
			for _, e := range r.entries(i) {
				if e.Sequence == 2 {
					t.Errorf("disk %d keeps the entry %s", i, e.Name)
				}
			}
		}
	})
}
