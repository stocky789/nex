// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/hostimage"
)

// mdRunner stands in for the installer tools: nothing is executed, but mdadm --create leaves the
// device node the slot writer waits for.
type mdRunner struct{ mu sync.Mutex }

func (r *mdRunner) Run(_ context.Context, name string, args ...string) error {
	if name == "mdadm" && len(args) > 1 && args[0] == "--create" {
		os.MkdirAll(filepath.Dir(args[1]), 0o755)
		os.WriteFile(args[1], nil, 0o644)
	}
	return nil
}

// TestEngineOnFileBackedDisks drives the whole pipeline through the real slot writer and ESP code on
// two file-backed disks: stage, arm, failed boot, retry, blessed boot, rollback.
func TestEngineOnFileBackedDisks(t *testing.T) {
	defer hostimage.SandboxMounts(t.TempDir())()
	ctx := context.Background()
	var disks []string
	for i := range 2 {
		p := filepath.Join(t.TempDir(), fmt.Sprintf("disk%d.img", i))
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		f.Truncate(hostimage.MinDiskBytes() + 64*hostimage.MiB)
		f.Close()
		disks = append(disks, p)
	}
	w := &hostimage.Writer{Runner: &mdRunner{}}
	rel1 := t.TempDir()
	if err := hostimage.SyntheticRelease(rel1, "base", 1, 1, 16); err != nil {
		t.Fatal(err)
	}
	if err := w.Install(ctx, hostimage.Options{Layout: hostimage.LayoutRAID1, Disks: disks, ImageDir: rel1}); err != nil {
		t.Fatal(err)
	}
	r1, err := hostimage.ReadRelease(rel1)
	if err != nil {
		t.Fatal(err)
	}
	host := &hostimage.Host{W: w,
		DiskPaths: func() ([]string, error) { return disks, nil },
		ESPs: func() ([]string, error) {
			var out []string
			for _, d := range disks {
				out = append(out, hostimage.NodeName(d, 1))
			}
			return out, nil
		}}

	dir := t.TempDir()
	prevOS, prevCmd := OSReleasePath, CmdlinePath
	t.Cleanup(func() { OSReleasePath, CmdlinePath = prevOS, prevCmd })
	OSReleasePath, CmdlinePath = filepath.Join(dir, "os-release"), filepath.Join(dir, "cmdline")
	bootInto := func(label string, seq int, rootHash string) {
		os.WriteFile(OSReleasePath, []byte(fmt.Sprintf("IMAGE_ID=nex-core\nBUILD_ID=\"%s-%d\"\n", label, seq)), 0o644)
		os.WriteFile(CmdlinePath, []byte("ro roothash="+rootHash+" panic=10"), 0o644)
	}
	bootInto("base", 1, r1.Manifest.RootHash)
	espDefault := func(i int) hostimage.UKIEntry {
		t.Helper()
		all, err := host.Entries(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var us []hostimage.UKIEntry
		for _, u := range all {
			if u.Node == hostimage.NodeName(disks[i], 1) {
				us = append(us, u)
			}
		}
		if len(us) == 0 {
			t.Fatalf("disk %d has no entry", i)
		}
		return hostimage.BootOrder(us)[0]
	}
	esps := func(fn func(root string)) {
		for _, d := range disks {
			fn(hostimage.ESPRoot(hostimage.NodeName(d, 1)))
		}
	}

	r := newRig(t)
	r.plat = nil
	cfg := r.cfg
	cfg.Platform = NewHostPlatform(host)
	cfg.Dir, cfg.TrustDir = filepath.Join(dir, "image"), filepath.Join(dir, "update")
	cfg.RunningVMs = nil
	eng := NewEngine(cfg)
	defer eng.Close()
	r.eng = eng
	b2 := r.pub.release(2, "v2", true)

	waitDone := func() Operation { return r.wait() }
	// Stage and arm in one go.
	if _, _, err = eng.Apply(ctx, ApplyRequest{RequestID: "req-apply-0001", Activate: "reboot"}); err != nil {
		t.Fatal(err)
	}
	if op := waitDone(); op.Error != nil || op.Phase != PhaseArmed {
		t.Fatalf("%+v", op)
	}
	select {
	case <-r.reboot:
	case <-time.After(time.Minute):
		t.Fatal("no reboot")
	}
	for i := range disks {
		if e := espDefault(i); e.Sequence != 2 || e.Tries != 3 {
			t.Fatalf("disk %d: %+v", i, e)
		}
	}
	// Three failed boots of version 2.
	for range hostimage.ArmTries {
		esps(func(root string) {
			if err := hostimage.MarkTried(root, 2); err != nil {
				t.Fatal(err)
			}
		})
	}
	for i := range disks {
		if e := espDefault(i); e.Sequence != 1 {
			t.Fatalf("disk %d falls back to %+v", i, e)
		}
	}
	st, err := eng.Status(ctx)
	if err != nil || st.Phase != PhaseRolledBack || st.Failure == nil || st.Failure.Code != "boot_failed" {
		t.Fatalf("%+v %v", st, err)
	}

	// Retry: the exhausted entry is removed before its slot is rewritten, and the new one is armed.
	if _, _, err = eng.Apply(ctx, ApplyRequest{RequestID: "req-apply-0002", Activate: "reboot"}); err != nil {
		t.Fatal(err)
	}
	if op := waitDone(); op.Error != nil || op.Phase != PhaseArmed {
		t.Fatalf("%+v", op)
	}
	<-r.reboot
	bootInto("v2", 2, b2.rootHash)
	if st, err = eng.Status(ctx); err != nil || st.Phase != PhaseBooted {
		t.Fatalf("%+v %v", st, err)
	}
	esps(func(root string) {
		if err := hostimage.Bless(root, 2); err != nil {
			t.Fatal(err)
		}
	})
	if st, err = eng.Status(ctx); err != nil || st.Phase != PhaseCommitted {
		t.Fatalf("%+v %v", st, err)
	}
	for i := range disks {
		if e := espDefault(i); e.Sequence != 2 || e.Counted {
			t.Fatalf("disk %d: %+v", i, e)
		}
	}

	// Operator rollback to the retained previous version.
	if _, _, err = eng.Rollback(ctx, ActionRequest{RequestID: "req-rollback-01"}); err != nil {
		t.Fatal(err)
	}
	<-r.reboot
	for i := range disks {
		if e := espDefault(i); e.Sequence != 1 || e.Tries != 3 {
			t.Fatalf("disk %d after rollback: %+v", i, e)
		}
	}
	bootInto("base", 1, r1.Manifest.RootHash)
	esps(func(root string) { hostimage.Bless(root, 1) })
	if st, err = eng.Status(ctx); err != nil || st.Phase != PhaseCommitted || st.Slots[0].Sequence != 1 || !strings.Contains(fmt.Sprint(st.Slots), "previous") {
		t.Fatalf("%+v %v", st, err)
	}
	// The state record is only a cache: a lost state.json is rebuilt from the boot state.
	os.Remove(filepath.Join(cfg.Dir, "state.json"))
	eng2 := NewEngine(cfg)
	defer eng2.Close()
	if st, err = eng2.Status(ctx); err != nil || st.Slots[0].Sequence != 1 || st.Slots[0].RootHash != r1.Manifest.RootHash {
		t.Fatalf("%+v %v", st, err)
	}
}
