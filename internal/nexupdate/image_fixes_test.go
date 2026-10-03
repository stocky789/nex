// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"nex.local/nex/internal/hostimage"
)

func readUpdate(t *testing.T, dir string) updateState {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	var st updateState
	if err = json.Unmarshal(b, &raw); err != nil || json.Unmarshal(raw["update"], &st) != nil {
		t.Fatalf("state.json: %v", err)
	}
	return st
}

func (r *rig) waitPhase(phase string) {
	r.t.Helper()
	for range 2000 {
		if r.status().Phase == phase {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("phase %s never reached", phase)
}

// rotated is a rig whose trust was rotated to version 2 (new root and release keys, the old root still
// listed) and adopted by one Check. The old root key expires after a year.
func rotated(t *testing.T) (*rig, ed25519.PrivateKey) {
	t.Helper()
	r := newRig(t)
	r.pub.release(2, "v2", true)
	rootPub2, rootKey2 := keyPair(t)
	relPub2, relKey2 := keyPair(t)
	day := 24 * time.Hour
	r.pub.trust = TrustDoc{Format: 1, Version: 2, Keys: []TrustKey{
		NewTrustKey("root-1", "root", r.pub.rootPub, t0.Add(365*day)),
		NewTrustKey("root-2", "root", rootPub2, t0.Add(1000*day)),
		NewTrustKey("rel-2", "release", relPub2, t0.Add(1000*day)),
	}}
	r.pub.publishTrust()
	r.pub.relKey, r.pub.expires, r.pub.serial = relKey2, t0.Add(900*day), 2
	r.pub.release(3, "v3", true)
	if st, err := r.eng.Check(context.Background()); err != nil || st.Available == nil {
		t.Fatalf("%+v %v", st, err)
	}
	if got := readUpdate(t, r.cfg.Dir).TrustVersion; got != 2 {
		t.Fatalf("adopted trust version %d, want 2", got)
	}
	return r, rootKey2
}

func TestPersistedTrustChainSurvivesTheExpiryOfTheKeyThatSignedIt(t *testing.T) {
	r, _ := rotated(t)
	// The version 1 root key (which signed version 2) is past its not_after now; version 2 lists keys
	// that are not. The chain was valid when adopted and must load as it is.
	r.advance(400 * 24 * time.Hour)
	r.restart()
	if _, err := r.eng.Check(context.Background()); err != nil {
		t.Fatalf("a persisted rotation signed by a since-expired key was dropped: %v", err)
	}
}

func TestInvalidPersistedTrustChainFailsClosed(t *testing.T) {
	forged := func(r *rig, version int) {
		doc := r.pub.trust
		doc.Version = version
		raw, _ := json.Marshal(doc)
		base := filepath.Join(r.cfg.TrustDir, "trust.2")
		os.WriteFile(base+".json", raw, 0o600)
		os.WriteFile(base+".sig", ed25519.Sign(r.pub.rootKey, raw), 0o600)
	}
	for name, damage := range map[string]func(r *rig){
		"signature corrupted": func(r *rig) {
			os.WriteFile(filepath.Join(r.cfg.TrustDir, "trust.2.sig"), make([]byte, 64), 0o600)
		},
		"signature missing": func(r *rig) { os.Remove(filepath.Join(r.cfg.TrustDir, "trust.2.sig")) },
		"chain deleted: the pinned trust would be older than the one adopted": func(r *rig) {
			os.Remove(filepath.Join(r.cfg.TrustDir, "trust.2.json"))
			os.Remove(filepath.Join(r.cfg.TrustDir, "trust.2.sig"))
		},
		"document of another version under the name of version 2": func(r *rig) { forged(r, 5) },
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := rotated(t)
			damage(r)
			r.restart()
			r.advance(2 * time.Minute)
			// The source still offers version 2: re-adopting it would be a fall back to older trust first.
			_, err := r.eng.Check(context.Background())
			wantCode(t, err, "trust_invalid")
		})
	}
}

func TestRollbackFloorBindsActivateAndRollback(t *testing.T) {
	ctx := context.Background()
	t.Run("a staged release below a raised floor is not armed", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		r.mustApply("req-apply-0001", nil)
		r.pub.serial, r.pub.minSeq = 2, 3
		r.pub.release(3, "v3", true)
		r.advance(2 * time.Minute)
		if st, err := r.eng.Check(ctx); err != nil || st.RollbackFloor != 3 {
			t.Fatalf("%+v %v", st, err)
		}
		_, _, err := r.eng.Activate(ctx, ActionRequest{RequestID: "req-activate-01"})
		wantCode(t, err, "below_rollback_floor")
		if len(r.plat.armed) != 0 {
			t.Fatalf("armed %v", r.plat.armed)
		}
	})
	t.Run("the previous version below a raised floor is not rolled back to", func(t *testing.T) {
		r := newRig(t)
		b2 := r.pub.release(2, "v2", true)
		r.mustApply("req-apply-0001", nil)
		if _, _, err := r.eng.Activate(ctx, ActionRequest{RequestID: "req-activate-01"}); err != nil {
			t.Fatal(err)
		}
		<-r.reboot
		r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, true)
		if st := r.status(); st.Phase != PhaseCommitted {
			t.Fatalf("%+v", st)
		}
		r.pub.serial, r.pub.minSeq = 2, 2
		r.pub.release(3, "v3", true)
		r.advance(2 * time.Minute)
		if st, err := r.eng.Check(ctx); err != nil || st.RollbackFloor != 2 {
			t.Fatalf("%+v %v", st, err)
		}
		_, _, err := r.eng.Rollback(ctx, ActionRequest{RequestID: "req-rollback-01"})
		wantCode(t, err, "below_rollback_floor")
		if len(r.plat.rolled) != 0 {
			t.Fatalf("rolled %v", r.plat.rolled)
		}
	})
}

func TestMetadataIsRevalidatedUnderTheFinalLock(t *testing.T) {
	r := newRig(t)
	r.pub.serial = 5
	r.pub.release(3, "v3", true)
	r.pub.release(4, "v4", false)
	nested := false
	r.src.onFetch = func(name string) {
		if name != "releases/3/manifest.json" || nested {
			return
		}
		// While this check waits on the network, another operation accepts a newer pointer that
		// also raises the floor.
		nested = true
		r.pub.serial, r.pub.minSeq = 7, 4
		r.pub.repoint(4)
		if _, err := r.eng.fetchMetadata(context.Background(), 0); err != nil {
			t.Errorf("the newer pointer: %v", err)
		}
	}
	_, err := r.eng.Check(context.Background())
	wantCode(t, err, "channel_replayed")
	base, channel := r.cfg.Settings()
	if st := readUpdate(t, r.cfg.Dir); st.Replay[replayKey(base, channel)].Serial != 7 || st.RollbackFloor != 4 {
		t.Fatalf("an older pointer lowered the record: %+v floor %d", st.Replay, st.RollbackFloor)
	}
}

func TestRecoveredCurrentCarriesTheRunningImagesFacts(t *testing.T) {
	newBooted := func(r *rig) {
		r.plat.booted = Booted{RootHash: strings.Repeat("0", 64), Label: "base", Sequence: 1,
			StateFormat: 3, MinStateFormat: 2, MDGeneration: 1}
	}
	t.Run("a record built from the disk states what the image reads", func(t *testing.T) {
		r := newRig(t)
		newBooted(r)
		r.status()
		cur := readUpdate(t, r.cfg.Dir).Current
		if cur == nil || cur.StateFormat != 3 || cur.MinStateFormat != 2 || cur.MDGeneration != 1 {
			t.Fatalf("%+v", cur)
		}
		// So the md gate knows the running kernel generation: a generation-0 image is refused.
		r.pub.manifest = func(m *Manifest) { m.Compat.StateFormat = 3 }
		r.pub.release(2, "v2", true)
		r.apply("req-apply-0001", nil)
		if op := r.wait(); op.Error == nil || op.Error.Code != "md_generation_downgrade" {
			t.Fatalf("%+v", op)
		}
	})
}

func TestTwoProcessesNeverWriteTheRecordTogether(t *testing.T) {
	prev := lockWait
	lockWait = 20 * time.Millisecond
	t.Cleanup(func() { lockWait = prev })
	ctx := context.Background()
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.src.gate = make(chan struct{})
	r.apply("req-apply-0001", nil)
	r.waitPhase(PhaseDownloading)

	// nex-update beside nex-core-local: another engine over the same state directory.
	other := NewEngine(r.cfg)
	t.Cleanup(other.Close)
	_, err := other.Check(ctx)
	wantCode(t, err, "update_in_progress")
	_, _, err = other.Apply(ctx, ApplyRequest{RequestID: "req-other-0001"})
	wantCode(t, err, "update_in_progress")
	_, _, err = other.Activate(ctx, ActionRequest{RequestID: "req-other-0002"})
	wantCode(t, err, "update_in_progress")
	_, _, err = other.Cancel(ctx, ActionRequest{RequestID: "req-other-0003"})
	wantCode(t, err, "update_in_progress")
	if st, err := other.Status(ctx); err != nil || !st.Running || st.Phase != PhaseDownloading {
		t.Fatalf("a status read beside a running update: %+v %v", st, err)
	}

	close(r.src.gate)
	r.wait()
	// The other process reads what the first one did instead of acting on a stale cache.
	if st, err := other.Status(ctx); err != nil || st.Phase != PhaseStaged {
		t.Fatalf("%+v %v", st, err)
	}
	if _, _, err = other.Activate(ctx, ActionRequest{RequestID: "req-activate-01"}); err != nil {
		t.Fatal(err)
	}
	if st := r.status(); st.Phase != PhaseArmed {
		t.Fatalf("the first process missed the arming: %+v", st)
	}
	_, _, err = r.eng.Cancel(ctx, ActionRequest{RequestID: "req-cancel-0001"})
	wantCode(t, err, "already_armed")
}

// A process waiting for the update lock must find the finished record when it gets the lock: the
// operation that ends saves its last state first and only then gives the lock back. Giving it back
// first let a `nex-update cancel` from a second process read the half-way record, cancel the apply
// that had just finished and remove its staging; the late save then raced the second process' write.
func TestTheFinishedRecordIsSavedBeforeTheLockIsGivenBack(t *testing.T) {
	prev := lockWait
	lockWait = 10 * time.Second
	t.Cleanup(func() { lockWait = prev })
	ctx := context.Background()
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.src.gate = make(chan struct{})
	r.apply("req-apply-0001", nil)
	r.waitPhase(PhaseDownloading)

	// Saving the record takes a while (a large foreign subtree of state.json, which every writer keeps), so a
	// process that gets the lock before the save is done reads the record as it was before the operation ended.
	r.eng.mu.Lock()
	r.eng.raw["ballast"] = json.RawMessage(`"` + strings.Repeat("x", 48<<20) + `"`)
	r.eng.mu.Unlock()

	other := NewEngine(r.cfg)
	t.Cleanup(other.Close)
	done := make(chan error, 1)
	go func() {
		_, _, err := other.Cancel(ctx, ActionRequest{RequestID: "req-cancel-0001"})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // the second process is now polling the lock the apply holds
	close(r.src.gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancel after the apply ended: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the second process never got the lock")
	}
	st := readUpdate(t, r.cfg.Dir)
	for _, rec := range st.Operations {
		if rec.Op.ID == "req-apply-0001" && (rec.Op.FinishedAt == nil || rec.Op.Phase != PhaseStaged) {
			t.Fatalf("the finished apply was rewritten by the second process: %+v", rec.Op)
		}
	}
	if st.Staged != nil || st.Phase != PhaseIdle {
		t.Fatalf("the staged release was not discarded by the cancel that saw it: phase %s staged %v", st.Phase, st.Staged)
	}
}

// The same ordering holds for an apply refused before any work starts (running guests): its record is
// dropped while the lock is still held, so a second process never finds it and "cancels" an apply that
// the caller was told never started.
func TestARefusedApplyLeavesNoRecordForTheNextProcess(t *testing.T) {
	prev := lockWait
	lockWait = 10 * time.Second
	t.Cleanup(func() { lockWait = prev })
	ctx := context.Background()
	r := newRig(t)
	inventory, entered := make(chan struct{}), make(chan struct{})
	r.cfg.RunningVMs = func(context.Context) ([]string, error) {
		entered <- struct{}{}
		<-inventory
		return []string{"web-1"}, nil
	}
	r.restart()
	r.pub.release(2, "v2", true)

	applied := make(chan error, 1)
	go func() {
		_, _, err := r.apply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" })
		applied <- err
	}()
	<-entered // the apply holds the lock and is reading the guest inventory
	r.eng.mu.Lock()
	r.eng.raw["ballast"] = json.RawMessage(`"` + strings.Repeat("x", 48<<20) + `"`)
	r.eng.mu.Unlock()

	other := NewEngine(r.cfg)
	t.Cleanup(other.Close)
	done := make(chan error, 1)
	go func() {
		_, _, err := other.Cancel(ctx, ActionRequest{RequestID: "req-cancel-0001"})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // the second process is polling the lock the apply holds
	close(inventory)
	wantCode(t, <-applied, "running_vms")
	select {
	case err := <-done:
		wantCode(t, err, "nothing_to_cancel")
	case <-time.After(30 * time.Second):
		t.Fatal("the second process never got the lock")
	}
}

func TestApplyChecksGuestsAgainBeforeArming(t *testing.T) {
	r := newRig(t)
	var vms atomic.Value
	vms.Store([]string(nil))
	r.cfg.RunningVMs = func(context.Context) ([]string, error) { return vms.Load().([]string), nil }
	r.restart()
	r.pub.release(2, "v2", true)
	r.src.gate = make(chan struct{})
	if _, _, err := r.apply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" }); err != nil {
		t.Fatalf("no guests run yet: %v", err)
	}
	r.waitPhase(PhaseDownloading)
	vms.Store([]string{"web-1"}) // started while the image downloads
	close(r.src.gate)
	op := r.wait()
	if op.Error == nil || op.Error.Code != "running_vms" {
		t.Fatalf("%+v", op)
	}
	if st := r.status(); st.Phase != PhaseStaged || len(r.plat.armed) != 0 {
		t.Fatalf("armed over a running guest: %+v %v", st, r.plat.armed)
	}
	select {
	case <-r.reboot:
		t.Fatal("rebooted over a running guest")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSlotImageNeedsTheZstdEncoding(t *testing.T) {
	r := newRig(t)
	r.pub.manifest = func(m *Manifest) { m.Components[0].Encoding = "" }
	r.pub.release(2, "v2", true)
	r.apply("req-apply-0001", nil)
	if op := r.wait(); op.Error == nil || op.Error.Code != "metadata_invalid" || len(r.plat.staged) != 0 {
		t.Fatalf("%+v", op)
	}
}

func TestExtractReleaseChecksTheWholeCompressedStream(t *testing.T) {
	good := buildRelease(t, "v2", 2, 2).blob
	// tar writers pad the archive past the end marker (a real release is large, so the checksum lies in a block the tar reader never asks for); the tar reader stops at the marker,
	// so only draining the stream reaches the frame checksum.
	zr, err := zstd.NewReader(bytes.NewReader(good))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	padded := zstdBytes(t, append(raw, make([]byte, 4<<20)...))
	var plain bytes.Buffer
	tw := tar.NewWriter(&plain)
	tw.WriteHeader(&tar.Header{Name: "roothash", Typeflag: tar.TypeReg, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()
	flip := func(b []byte, at int) []byte {
		c := append([]byte(nil), b...)
		c[at] ^= 0xff
		return c
	}
	if err := extractRelease(writeBlob(t, good), filepath.Join(t.TempDir(), "release")); err != nil {
		t.Fatalf("the intact blob: %v", err)
	}
	for name, blob := range map[string][]byte{
		"frame checksum damaged, tar content intact":             flip(good, len(good)-1),
		"frame checksum damaged behind the tar's record padding": flip(padded, len(padded)-1),
		"truncated inside the checksum":                          good[:len(good)-2],
		"damaged in the middle":                                  flip(good, len(good)/2),
		"an uncompressed tar (the former format)":                plain.Bytes(),
	} {
		dir := filepath.Join(t.TempDir(), "release")
		wantCode(t, extractRelease(writeBlob(t, blob), dir), "image_invalid")
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("%s: a release directory exists after the refusal", name)
		}
	}
}

func writeBlob(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The panel and the guide offer Roll back in the phase booted, the trial boot of an update. The first QEMU run of the
// Secure Boot lab issued it there and the host never rebooted: the engine refused with already_armed. The release to
// return to is the one the running update replaced, and once it is blessed the abandoned update is the retained previous.
func TestRollbackDuringTheTrialBootReturnsToTheReleaseItReplaced(t *testing.T) {
	r := newRig(t)
	b2 := r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" })
	select {
	case <-r.reboot:
	case <-time.After(5 * time.Second):
		t.Fatal("no reboot after the update was armed")
	}
	r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, false)
	if st := r.status(); st.Phase != PhaseBooted {
		t.Fatalf("not in the trial boot: %+v", st)
	}
	if _, _, err := r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-01"}); err != nil {
		t.Fatal(err)
	}
	if len(r.plat.rolled) != 1 || r.plat.rolled[0] != [2]int{2, 1} {
		t.Fatalf("rolled from the running update to the release it replaced, got %v", r.plat.rolled)
	}
	select {
	case <-r.reboot:
	case <-time.After(5 * time.Second):
		t.Fatal("no reboot after the rollback")
	}
	// What rollbackESP does on the ESPs: the replaced release's entry gets a fresh boot counter.
	r.plat.mu.Lock()
	r.plat.entries = append(r.plat.entries, hostimage.UKIEntry{Sequence: 1, Counted: true, Tries: 3})
	r.plat.mu.Unlock()
	if st := r.status(); st.Phase != PhaseArmed {
		t.Fatalf("%+v", st)
	}
	r.plat.boot(Booted{RootHash: strings.Repeat("0", 64), Label: "base", Sequence: 1}, true)
	roles := map[string]int{}
	st := r.status()
	for _, s := range st.Slots {
		roles[s.Role] = s.Sequence
	}
	if st.Phase != PhaseCommitted || roles["booted"] != 1 || roles["previous"] != 2 {
		t.Fatalf("%+v", st)
	}
}

func TestRollbackIsRefusedWhileAnUpdateIsArmedButNotBooted(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" })
	_, _, err := r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-01"})
	wantCode(t, err, "already_armed")
	if len(r.plat.rolled) != 0 {
		t.Fatalf("rolled back before the armed update ever booted: %v", r.plat.rolled)
	}
}

// A release that an earlier apply already staged is finished, not downloaded and written again: a
// stage_only apply followed by one that arms it, or a resumed request after a restart.
func TestApplyOfAnAlreadyStagedReleaseDoesNotStageItAgain(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	blobReads := func() int {
		r.src.mu.Lock()
		defer r.src.mu.Unlock()
		return len(r.src.offsets)
	}
	if len(r.plat.staged) != 1 || blobReads() == 0 {
		t.Fatalf("setup: staged %v, blob reads %d", r.plat.staged, blobReads())
	}
	before := blobReads()
	r.restart()
	r.mustApply("req-apply-0002", func(a *ApplyRequest) { a.Activate = "reboot" })
	select {
	case <-r.reboot:
	case <-time.After(5 * time.Second):
		t.Fatal("no reboot after the second apply")
	}
	if len(r.plat.staged) != 1 || blobReads() != before {
		t.Fatalf("the staged release was written again: staged %v, blob reads %d -> %d", r.plat.staged, before, blobReads())
	}
	if st := r.status(); st.Phase != PhaseArmed || len(r.plat.armed) != 1 || r.plat.armed[0] != 2 {
		t.Fatalf("%+v %v", st, r.plat.armed)
	}
}

// Cancel must not turn an Activate that is arming into "idle": the reboot is already on its way.
func TestCancelIsRefusedWhileAnActivationIsInFlight(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	gate := make(chan struct{})
	r.cfg.RunningVMs = func(context.Context) ([]string, error) { <-gate; return nil, nil }
	r.restart()
	activated := make(chan error, 1)
	go func() {
		_, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
		activated <- err
	}()
	for range 2000 {
		if r.status().Running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancelled := make(chan error, 1)
	go func() {
		_, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"})
		cancelled <- err
	}()
	var err error
	select {
	case err = <-cancelled:
	case <-time.After(5 * time.Second):
		close(gate) // a Cancel that waits for the activation to end is the defect
		t.Fatal("Cancel waited for the activation instead of refusing")
	}
	wantCode(t, err, "update_in_progress")
	close(gate)
	if err = <-activated; err != nil {
		t.Fatal(err)
	}
	if st := r.status(); st.Phase != PhaseArmed || len(r.plat.armed) != 1 {
		t.Fatalf("the activation was undone: %+v", st)
	}
}

// A stage the data volume cannot hold is refused before anything is downloaded, with the code the pool
// coordinator uses, and leaves the old state alone.
func TestApplyRefusesWhenTheVolumeCannotHoldTheRelease(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	prev := freeBytes
	freeBytes = func(string) (uint64, error) { return 1 << 20, nil }
	t.Cleanup(func() { freeBytes = prev })
	if _, _, err := r.apply("req-apply-0001", nil); err != nil {
		t.Fatal(err)
	}
	op := r.wait()
	if op.Error == nil || op.Error.Code != "insufficient_space" {
		t.Fatalf("%+v", op)
	}
	r.src.mu.Lock()
	reads := len(r.src.offsets)
	r.src.mu.Unlock()
	if st := r.status(); reads != 0 || len(r.plat.staged) != 0 || st.Phase != PhaseIdle {
		t.Fatalf("a refused stage went on: blob reads %d, staged %v, %+v", reads, r.plat.staged, st)
	}
	freeBytes = prev
	r.mustApply("req-apply-0002", nil)
}

func TestDiskFullIsReportedAsInsufficientSpace(t *testing.T) {
	wantCode(t, diskFull(fmt.Errorf("write: %w", syscall.ENOSPC)), "insufficient_space")
	other := errors.New("permission denied")
	if diskFull(other) != other {
		t.Fatal("another error was rewritten")
	}
}

// A failed activation that stays on record (an arming error, not a refusal) is still a failure when the
// caller repeats the request: the pool rollout would otherwise report the host as armed.
func TestRepeatedFailedActivationReturnsItsError(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	r.plat.armErr = errors.New("disk offline")
	_, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	if err == nil {
		t.Fatal("activation succeeded although arming failed")
	}
	_, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	wantCode(t, err, "internal")
	r.plat.armErr = nil
	if _, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-02"}); err != nil {
		t.Fatal(err)
	}
}

// The running image must say which release it is: a missing or non-numeric sequence would make staging
// prune the boot entry the host runs from.
func TestHostPlatformRefusesAnImageWithoutASequence(t *testing.T) {
	dir := t.TempDir()
	cmdline := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(cmdline, []byte("quiet roothash="+strings.Repeat("a", 64)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldCmd, oldOS := CmdlinePath, OSReleasePath
	CmdlinePath, OSReleasePath = cmdline, filepath.Join(dir, "os-release")
	t.Cleanup(func() { CmdlinePath, OSReleasePath = oldCmd, oldOS })
	for build, want := range map[string]int{"": 0, "v1": 0, "v1-x": 0, "v1-0": 0, "-3": 0, "my-label-7": 7} {
		if err := os.WriteFile(OSReleasePath, []byte("BUILD_ID="+build+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		b, err := hostPlatform{}.Booted(context.Background())
		if (want == 0) != (err != nil) || b.Sequence != want {
			t.Errorf("BUILD_ID=%q: %+v %v", build, b, err)
		}
	}
}

// waitFailure polls the status until the record carries a failure with the code.
func (r *rig) waitFailure(code string) ImageStatus {
	r.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := r.status(); (st.Failure != nil && st.Failure.Code == code) || (code == "" && st.Failure == nil) {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatalf("no failure %q on the record: %+v", code, r.status())
	return ImageStatus{}
}

// A restart that fails is not silent: the record says reboot_failed while the release stays armed, and
// repeating activate (a new request_id, or the same one) restarts again.
func TestFailedRebootIsRecordedAndActivateRepeatsIt(t *testing.T) {
	for _, sameID := range []bool{false, true} {
		t.Run(fmt.Sprint("same_request_id=", sameID), func(t *testing.T) {
			r := newRig(t)
			r.pub.release(2, "v2", true)
			r.mustApply("req-apply-0001", nil)
			var calls atomic.Int32
			r.cfg.Reboot = func(context.Context) error {
				if calls.Add(1) == 1 {
					return errors.New("systemctl: Failed to connect to bus")
				}
				r.reboot <- struct{}{}
				return nil
			}
			r.restart()
			ctx := context.Background()
			if _, _, err := r.eng.Activate(ctx, ActionRequest{RequestID: "req-activate-01"}); err != nil {
				t.Fatal(err)
			}
			if st := r.waitFailure("reboot_failed"); st.Phase != PhaseArmed {
				t.Fatalf("%+v", st)
			}
			id := "req-activate-02"
			if sameID {
				id = "req-activate-01"
			}
			if _, _, err := r.eng.Activate(ctx, ActionRequest{RequestID: id}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-r.reboot:
			case <-time.After(5 * time.Second):
				t.Fatal("activate did not restart the host again")
			}
			if st := r.waitFailure(""); st.Phase != PhaseArmed || len(r.plat.armed) != 1 {
				t.Fatalf("the release is armed once and stays armed: %+v armed=%v", st, r.plat.armed)
			}
		})
	}
}

// An arm that fails on a later disk and cannot take its entry back from the earlier ones leaves a release
// that may boot: the record says armed with the failure, and nothing restarts the host. Cancel takes the
// entry back (and refuses while that fails); once the release did boot it is refused for good.
func TestArmThatCouldNotBeUndoneIsRecordedAsArmed(t *testing.T) {
	r := newRig(t)
	b2 := r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	r.plat.armErr = fmt.Errorf("%w: /dev/vdb1: write failed", hostimage.ErrArmNotUndone)
	r.plat.entries = append(r.plat.entries, hostimage.UKIEntry{Sequence: 2, Counted: true, Tries: 3}) // the first disk still has it
	_, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	wantCode(t, err, "arm_incomplete")
	if st := r.status(); st.Phase != PhaseArmed || st.Failure == nil || st.Failure.Code != "arm_incomplete" {
		t.Fatalf("%+v", st)
	}
	r.plat.disarmErr = errors.New("umount: device busy")
	_, _, err = r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-0001"})
	wantCode(t, err, "arm_incomplete")
	if st := r.status(); st.Phase != PhaseArmed || st.Slots[len(st.Slots)-1].Role != "armed" {
		t.Fatalf("a Cancel that could not take the entry back must leave the release armed: %+v", st)
	}
	// The release started on the disk that kept it: it is booted, no longer cancellable.
	r.plat.disarmErr = nil
	r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, false)
	_, _, err = r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-0002"})
	wantCode(t, err, "already_armed")
	if len(r.plat.disarmed) != 0 {
		t.Fatalf("a release that booted was disarmed: %v", r.plat.disarmed)
	}
	select {
	case <-r.reboot:
		t.Fatal("the host was restarted after a half-done arm")
	case <-time.After(100 * time.Millisecond):
	}
}

// The way out of arm_incomplete when the half-armed disk never boots: Cancel takes the entry back from
// every disk, restores the running base's layers and discards the stage like any Cancel of a staged release.
func TestCancelAfterAnArmThatCouldNotBeUndoneDisarmsAndRestoresTheLayers(t *testing.T) {
	r := newLayerRig(t)
	r.pub.addComponents(r.pub.layerComponent("nex-zfs", []byte("zfs-for-v2-2"), "v2-2", false))
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	r.plat.armErr = fmt.Errorf("%w: /dev/vdb1: write failed", hostimage.ErrArmNotUndone)
	r.plat.entries = append(r.plat.entries, hostimage.UKIEntry{Sequence: 2, Counted: true, Tries: 3})
	_, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	wantCode(t, err, "arm_incomplete")
	if e := r.enabled(); e.Base != "v2-2" {
		t.Fatalf("the layers follow the release that may boot: %+v", e)
	}
	if _, _, err = r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-0001"}); err != nil {
		t.Fatal(err)
	}
	st := r.status()
	if st.Phase != PhaseIdle || st.Failure != nil || len(r.plat.disarmed) != 1 || r.plat.disarmed[0] != 2 {
		t.Fatalf("%+v disarmed=%v", st, r.plat.disarmed)
	}
	if e := r.enabled(); e.SHA256 != r.oldSHA || e.Base != "base-1" || !e.Enabled {
		t.Fatalf("the layers of the running base are not back: %+v", e)
	}
}

// Activate after arm_incomplete takes the entry back and arms the release again, whole this time.
func TestActivateAfterAnArmThatCouldNotBeUndoneArmsAgain(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	r.plat.armErr = fmt.Errorf("%w: /dev/vdb1: write failed", hostimage.ErrArmNotUndone)
	r.plat.entries = append(r.plat.entries, hostimage.UKIEntry{Sequence: 2, Counted: true, Tries: 3})
	_, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	wantCode(t, err, "arm_incomplete")
	r.plat.disarmErr = errors.New("umount: device busy")
	_, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-02"})
	wantCode(t, err, "arm_incomplete")
	if len(r.plat.armed) != 0 {
		t.Fatalf("armed again over an entry that could not be taken back: %v", r.plat.armed)
	}
	r.plat.disarmErr, r.plat.armErr = nil, nil
	if _, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-03"}); err != nil {
		t.Fatal(err)
	}
	if st := r.status(); st.Phase != PhaseArmed || st.Failure != nil || len(r.plat.disarmed) != 1 || len(r.plat.armed) != 1 {
		t.Fatalf("%+v disarmed=%v armed=%v", st, r.plat.disarmed, r.plat.armed)
	}
	select {
	case <-r.reboot:
	case <-time.After(5 * time.Second):
		t.Fatal("the retry did not restart the host")
	}
}

// When the half-armed entry is gone from every disk (the boot assessment finds none), the record goes back
// to staged and the layers to the running base's, like a failed arm that was undone.
func TestReconcileOfAnArmedReleaseWithoutEntriesRestoresTheLayers(t *testing.T) {
	r := newLayerRig(t)
	r.pub.addComponents(r.pub.layerComponent("nex-zfs", []byte("zfs-for-v2-2"), "v2-2", false))
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	r.plat.armErr = fmt.Errorf("%w: /dev/vdb1: write failed", hostimage.ErrArmNotUndone)
	_, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	wantCode(t, err, "arm_incomplete")
	if st := r.status(); st.Phase != PhaseStaged || st.Failure != nil || st.Slots[len(st.Slots)-1].Role != "staged" {
		t.Fatalf("%+v", st)
	}
	if e := r.enabled(); e.SHA256 != r.oldSHA || e.Base != "base-1" || !e.Enabled {
		t.Fatalf("the layers of the running base are not back: %+v", e)
	}
}

// The same through apply with activate=reboot: the failing arm must not put the record back to staged.
func TestApplyWhoseArmCouldNotBeUndoneStaysArmed(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.plat.armErr = fmt.Errorf("%w: /dev/vdb1: write failed", hostimage.ErrArmNotUndone)
	r.plat.entries = append(r.plat.entries, hostimage.UKIEntry{Sequence: 2, Counted: true, Tries: 3}) // the first disk still has it
	if _, _, err := r.apply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" }); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "arm_incomplete" {
		t.Fatalf("%+v", op)
	}
	if st := r.status(); st.Phase != PhaseArmed {
		t.Fatalf("%+v", st)
	}
}

// A release that replaces a staged one takes the files of the old stage with it.
func TestRestagingDropsTheStagingDirectoryOfTheReplacedRelease(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	old := filepath.Join(r.cfg.Dir, "staging", "2")
	if _, err := os.Stat(old); err != nil {
		t.Fatal(err)
	}
	r.pub.serial = 2
	r.pub.release(3, "v3", true)
	r.mustApply("req-apply-0002", nil)
	if _, err := os.Stat(old); err == nil {
		t.Error("the staging directory of the replaced release is kept")
	}
	if _, err := os.Stat(filepath.Join(r.cfg.Dir, "staging", "3", "release")); err != nil {
		t.Error(err)
	}
}

// A rollback that fails with no disk changed leaves the running release's layers; one that failed after a
// disk was switched keeps the previous release's layers and the record says armed with the failure, and
// repeating the rollback finishes the disks that are left.
func TestRollbackThatReachedSomeDisksIsRecordedAndRepeatable(t *testing.T) {
	setup := func(t *testing.T) (*layerRig, string) {
		r := newLayerRig(t)
		newLayer := []byte("zfs-for-v2-2")
		r.pub.addComponents(r.pub.layerComponent("nex-zfs", newLayer, "v2-2", false))
		b2 := r.pub.release(2, "v2", true)
		r.mustApply("req-apply-0001", nil)
		if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err != nil {
			t.Fatal(err)
		}
		r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, true)
		if st := r.status(); st.Phase != PhaseCommitted {
			t.Fatalf("%+v", st)
		}
		return r, hexOf(newLayer)
	}
	t.Run("no disk changed", func(t *testing.T) {
		r, newSHA := setup(t)
		r.plat.rollErr = errors.New("no UKI of sequence 1 to roll back to")
		_, _, err := r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-01"})
		wantCode(t, err, "previous_unavailable")
		if e := r.enabled(); e.SHA256 != newSHA || e.Base != "v2-2" {
			t.Fatalf("the layers of the running release are not back: %+v", e)
		}
		if st := r.status(); st.Phase != PhaseCommitted || st.Failure != nil {
			t.Fatalf("%+v", st)
		}
	})
	t.Run("after a disk changed", func(t *testing.T) {
		r, _ := setup(t)
		r.plat.rollErr = fmt.Errorf("%w: /dev/vdb1: input/output error", hostimage.ErrRollbackPartial)
		r.plat.entries = append(r.plat.entries, hostimage.UKIEntry{Sequence: 1, Counted: true, Tries: 3}) // the previous UKI, on the disk the rollback reached
		_, _, err := r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-01"})
		wantCode(t, err, "rollback_incomplete")
		st := r.status()
		if st.Phase != PhaseArmed || st.Failure == nil || st.Failure.Code != "rollback_incomplete" {
			t.Fatalf("%+v", st)
		}
		if e := r.enabled(); e.SHA256 != r.oldSHA || e.Base != "base-1" {
			t.Fatalf("the layers must follow the previous release that already boots on a disk: %+v", e)
		}
		if _, _, err = r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-0001"}); err == nil {
			t.Fatal("a half-done rollback was cancelled")
		}
		// Repeating it, with the fault gone, finishes the job; the running release stays the current one.
		r.plat.rollErr = nil
		if _, _, err = r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-02"}); err != nil {
			t.Fatal(err)
		}
		st = r.status()
		if st.Phase != PhaseArmed || st.Failure != nil || len(r.plat.rolled) != 1 || r.plat.rolled[0] != [2]int{2, 1} {
			t.Fatalf("%+v rolled=%v", st, r.plat.rolled)
		}
		if e := r.enabled(); e.SHA256 != r.oldSHA {
			t.Fatalf("%+v", e)
		}
	})
}

// The space estimate counts the layers stageLayers downloads: an on_demand layer the operator has enabled
// follows its base (it is not required, so only the predicate of stageLayers includes it); one that is not
// enabled is not counted.
func TestSpaceCheckCountsTheLayersThatAreStaged(t *testing.T) {
	r := newLayerRig(t)
	zfs := r.pub.layerComponent("nex-zfs", bytes.Repeat([]byte("z"), 8<<20), "v2-2", true)
	cluster := r.pub.layerComponent("nex-cluster", bytes.Repeat([]byte("c"), 8<<20), "v2-2", true)
	r.pub.addComponents(zfs, cluster)
	r.pub.release(2, "v2", true)
	st, err := r.eng.Check(context.Background())
	if err != nil || st.Available == nil {
		t.Fatalf("%+v %v", st, err)
	}
	var slot int64
	for _, c := range st.Available.Components {
		if c.Kind == "slot-image" {
			slot = c.Bytes
		}
	}
	// Room for the image and the enabled layer once each, not twice (the blob and its tree).
	prev := freeBytes
	freeBytes = func(string) (uint64, error) { return uint64(2*slot) + spaceReserve + uint64(zfs.Bytes), nil }
	t.Cleanup(func() { freeBytes = prev })
	if _, _, err = r.apply("req-apply-0001", nil); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "insufficient_space" {
		t.Fatalf("the enabled on_demand layer was not counted: %+v", op)
	}
	// Room for both copies of the enabled layer; the unenabled one needs none.
	freeBytes = func(string) (uint64, error) { return uint64(2*slot) + spaceReserve + uint64(2*zfs.Bytes), nil }
	r.mustApply("req-apply-0002", nil)
}

type snapshotArmPlatform struct {
	Platform
	beforeArm func()
}

func (p snapshotArmPlatform) Arm(ctx context.Context, rel *hostimage.Release) error {
	p.beforeArm()
	return p.Platform.Arm(ctx, rel)
}

// Restore the durable record observed just before Arm publishes its first boot entry, as a power
// cut would. The disks may then hold none, some, or all of the entries, or already have booted them.
func armPublicationSnapshot(t *testing.T, r *rig) {
	t.Helper()
	var snapshot []byte
	r.cfg.Reboot = nil
	r.cfg.Platform = snapshotArmPlatform{Platform: r.plat, beforeArm: func() {
		var err error
		snapshot, err = os.ReadFile(r.eng.statePath())
		if err != nil {
			t.Fatal(err)
		}
	}}
	r.restart()
	if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err != nil {
		t.Fatal(err)
	}
	r.eng.Close()
	if err := os.WriteFile(r.eng.statePath(), snapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	r.cfg.Platform = r.plat
	r.restart()
}

func TestInterruptedArmPublicationRetainsThePreviousReleaseAfterBoot(t *testing.T) {
	for _, blessed := range []bool{false, true} {
		t.Run(fmt.Sprint("blessed=", blessed), func(t *testing.T) {
			r := newRig(t)
			b := r.pub.release(2, "v2", true)
			r.mustApply("req-apply-0001", nil)
			armPublicationSnapshot(t, r)
			r.plat.boot(Booted{RootHash: b.rootHash, Sequence: 2, Label: "v2"}, blessed)
			st := r.status()
			state := readUpdate(t, r.cfg.Dir)
			if blessed {
				if st.Phase != PhaseCommitted || state.Current.Sequence != 2 || state.Previous == nil || state.Previous.Sequence != 1 {
					t.Fatalf("commit lost the previous release: %+v", st)
				}
			} else if st.Phase != PhaseBooted || state.Current.Sequence != 1 {
				t.Fatalf("trial boot lost its rollback target: %+v", st)
			}
			if _, _, err := r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-01"}); err != nil {
				t.Fatal(err)
			}
			if len(r.plat.rolled) != 1 || r.plat.rolled[0] != [2]int{2, 1} {
				t.Fatalf("rollback did not return to the original image: %v", r.plat.rolled)
			}
		})
	}
}

func TestInterruptedArmPublicationCleansUnbootedEntries(t *testing.T) {
	for _, entries := range []int{0, 1, 2} {
		t.Run(fmt.Sprint("entries=", entries), func(t *testing.T) {
			r := newRig(t)
			r.pub.release(2, "v2", true)
			r.mustApply("req-apply-0001", nil)
			armPublicationSnapshot(t, r)
			r.plat.mu.Lock()
			r.plat.entries = nil
			for range entries {
				r.plat.entries = append(r.plat.entries, hostimage.UKIEntry{Sequence: 2, Counted: true, Tries: 3})
			}
			r.plat.mu.Unlock()
			if st := r.status(); st.Phase != PhaseStaged || st.Failure != nil {
				t.Fatalf("unbooted publication was reported as complete: %+v", st)
			}
			if diskEntries, err := r.plat.Entries(context.Background()); err != nil || len(diskEntries) != 0 {
				t.Fatalf("published entries were not taken back: %v %v", diskEntries, err)
			}
			if _, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"}); err != nil {
				t.Fatal(err)
			}
			if st := r.status(); st.Phase != PhaseIdle {
				t.Fatalf("cleaned stage could not be cancelled: %+v", st)
			}
		})
	}
}

func TestInterruptedArmPublicationReportsFailedCleanup(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	armPublicationSnapshot(t, r)
	r.plat.disarmErr = errors.New("boot disk unavailable")
	if st := r.status(); st.Phase != PhaseArmed || st.Failure == nil || st.Failure.Code != "arm_incomplete" {
		t.Fatalf("unsafe publication was not reported: %+v", st)
	}
	_, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"})
	wantCode(t, err, "arm_incomplete")
	r.plat.disarmErr = nil
	if _, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-002"}); err != nil {
		t.Fatal(err)
	}
	if st := r.status(); st.Phase != PhaseIdle {
		t.Fatalf("repaired publication could not be cancelled: %+v", st)
	}
}

func interruptOperation(t *testing.T, r *rig, id, phase string) {
	t.Helper()
	r.eng.Close()
	if err := r.eng.enter(lockWait); err != nil {
		t.Fatal(err)
	}
	r.eng.mu.Lock()
	if err := r.eng.load(); err != nil {
		t.Fatal(err)
	}
	r.eng.updateOp(id, func(o *Operation) {
		o.FinishedAt, o.Phase = nil, phase
	})
	if err := r.eng.save(); err != nil {
		t.Fatal(err)
	}
	r.eng.mu.Unlock()
	r.eng.leave()
	r.restart()
}

func TestInterruptedArmedApplyResumesOnlyCompletionAndReboot(t *testing.T) {
	for _, boot := range []string{"not_booted", "trial", "committed"} {
		t.Run(boot, func(t *testing.T) {
			r := newRig(t)
			b := r.pub.release(2, "v2", true)
			r.cfg.RebootDelay = time.Hour
			r.restart()
			mod := func(a *ApplyRequest) { a.Activate = "reboot" }
			r.mustApply("req-apply-0001", mod)
			interruptOperation(t, r, "req-apply-0001", PhaseWriting)
			if boot != "not_booted" {
				r.plat.boot(Booted{RootHash: b.rootHash, Sequence: 2, Label: "v2"}, boot == "committed")
			}
			r.src.down = true // Completion must not depend on fetching or rewriting the armed image.
			r.eng.cfg.RebootDelay = time.Millisecond
			op, replayed, err := r.apply("req-apply-0001", mod)
			if err != nil || !replayed || op.FinishedAt == nil || op.Phase != PhaseArmed {
				t.Fatalf("armed replay did not finish at its boundary: %+v %v %v", op, replayed, err)
			}
			r.eng.wg.Wait()
			if len(r.plat.staged) != 1 || len(r.plat.armed) != 1 {
				t.Fatalf("replay rewrote or rearmed the slot: staged=%v armed=%v", r.plat.staged, r.plat.armed)
			}
			select {
			case <-r.reboot:
				if boot != "not_booted" {
					t.Fatal("replayed apply restarted an already booted release")
				}
			default:
				if boot == "not_booted" {
					t.Fatal("replayed apply did not restart the still-armed release")
				}
			}
			if boot != "committed" {
				_, _, err = r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"})
				wantCode(t, err, "already_armed")
			}
		})
	}
}

func TestCancelStagedInterruptedOperationAllowsTheNextApply(t *testing.T) {
	for _, kind := range []string{"apply", "import"} {
		t.Run(kind, func(t *testing.T) {
			r := newRig(t)
			r.pub.release(2, "v2", true)
			if kind == "import" {
				r.cfg.InboxDir = r.pub.dir
				r.restart()
				if _, _, err := r.eng.Import(context.Background(), ImportRequest{RequestID: "req-stage-0001", Media: "inbox"}); err != nil {
					t.Fatal(err)
				}
				if op := r.wait(); op.Error != nil {
					t.Fatalf("import: %+v", op)
				}
			} else {
				r.mustApply("req-stage-0001", nil)
			}
			interruptOperation(t, r, "req-stage-0001", PhaseWriting)
			if _, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"}); err != nil {
				t.Fatal(err)
			}
			r.restart()
			op, found, err := r.eng.Operation("req-stage-0001")
			if err != nil || !found || op.Phase != "cancelled" || op.FinishedAt == nil || op.Progress != nil {
				t.Fatalf("interrupted operation was not durably cancelled: %+v %v %v", op, found, err)
			}
			if _, err := os.Stat(r.eng.stagingDir(2)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("cancelled stage was retained: %v", err)
			}
			r.mustApply("req-apply-0002", nil)
		})
	}
}

func TestFinishedActivationReplayRecoversTheDelayedReboot(t *testing.T) {
	for _, state := range []string{"still_armed", "trial", "committed", "newer_activation", "newer_release"} {
		t.Run(state, func(t *testing.T) {
			r := newRig(t)
			b := r.pub.release(2, "v2", true)
			r.mustApply("req-apply-0001", nil)
			r.cfg.RebootDelay = time.Hour
			r.restart()
			req := ActionRequest{RequestID: "req-activate-01"}
			if _, _, err := r.eng.Activate(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			r.restart() // Close cancelled the pending delay without recording reboot_failed.
			switch state {
			case "trial", "committed", "newer_release":
				r.plat.boot(Booted{RootHash: b.rootHash, Sequence: 2, Label: "v2"}, state != "trial")
				r.status()
			case "newer_activation":
				if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-02"}); err != nil {
					t.Fatal(err)
				}
				r.restart()
			}
			if state == "newer_release" {
				r.pub.serial = 2
				r.pub.release(3, "v3", true)
				r.mustApply("req-apply-0002", nil)
				if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-02"}); err != nil {
					t.Fatal(err)
				}
				r.restart()
			}
			r.eng.cfg.RebootDelay = time.Millisecond
			op, replayed, err := r.eng.Activate(context.Background(), req)
			if err != nil || !replayed || op.Phase != PhaseArmed {
				t.Fatalf("activation replay: %+v %v %v", op, replayed, err)
			}
			r.eng.wg.Wait()
			select {
			case <-r.reboot:
				if state != "still_armed" {
					t.Fatal("old activation restarted an already booted or newer operation")
				}
			default:
				if state == "still_armed" {
					t.Fatal("activation replay lost the delayed reboot")
				}
			}
		})
	}
}

func TestInterruptedArmPublicationRestoresTheRunningLayers(t *testing.T) {
	r := newLayerRig(t)
	r.pub.addComponents(r.pub.layerComponent("nex-zfs", []byte("zfs-for-v2-2"), "v2-2", false))
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	armPublicationSnapshot(t, r.rig)
	if st := r.status(); st.Phase != PhaseStaged || st.Failure != nil {
		t.Fatalf("interrupted publication was not recovered: %+v", st)
	}
	if enabled := r.enabled(); enabled.SHA256 != r.oldSHA || enabled.Base != "base-1" || !enabled.Enabled {
		t.Fatalf("running layers were not restored: %+v", enabled)
	}
}

func TestInterruptedActivationReplayAfterBootDoesNotRearmOrReboot(t *testing.T) {
	for _, blessed := range []bool{false, true} {
		t.Run(fmt.Sprint("blessed=", blessed), func(t *testing.T) {
			r := newRig(t)
			b := r.pub.release(2, "v2", true)
			r.mustApply("req-apply-0001", nil)
			armPublicationSnapshot(t, r)
			r.plat.boot(Booted{RootHash: b.rootHash, Sequence: 2, Label: "v2"}, blessed)
			r.eng.cfg.Reboot = func(context.Context) error { r.reboot <- struct{}{}; return nil }
			op, replayed, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
			if err != nil || !replayed || op.FinishedAt == nil || op.Phase != PhaseArmed {
				t.Fatalf("booted interrupted activation did not finish: %+v %v %v", op, replayed, err)
			}
			r.eng.wg.Wait()
			if len(r.plat.armed) != 1 {
				t.Fatalf("replay rearmed a booted image: %v", r.plat.armed)
			}
			select {
			case <-r.reboot:
				t.Fatal("replayed activation restarted an already booted release")
			default:
			}
		})
	}
}

func TestFailedNonArmingRequestDoesNotSuppressThePreviousActivationReboot(t *testing.T) {
	for _, retry := range []string{"delayed_callback", "exact_replay"} {
		t.Run(retry, func(t *testing.T) {
			r := newRig(t)
			r.pub.release(2, "v2", true)
			r.mustApply("req-apply-0001", nil)
			r.cfg.RebootDelay = time.Hour
			r.restart()
			req := ActionRequest{RequestID: "req-activate-01"}
			if _, _, err := r.eng.Activate(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			r.eng.cfg.RunningVMs = func(context.Context) ([]string, error) {
				return nil, errors.New("VM inventory unavailable")
			}
			_, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-02"})
			wantCode(t, err, "vm_inventory_unavailable")
			failed, found, err := r.eng.Operation("req-activate-02")
			if err != nil || !found || failed.FinishedAt == nil || failed.Error == nil {
				t.Fatalf("non-arming failure was not retained: %+v %v %v", failed, found, err)
			}
			r.eng.Close()
			r.cfg.RebootDelay = time.Millisecond
			r.restart()
			if retry == "exact_replay" {
				op, replayed, err := r.eng.Activate(context.Background(), req)
				if err != nil || !replayed || op.Error != nil {
					t.Fatalf("successful activation replay changed after a failed request: %+v %v %v", op, replayed, err)
				}
			} else {
				// Fire A's delayed callback against the persisted B failure without timing a race
				// between B's HTTP request and the original hour-long timer.
				r.eng.scheduleReboot(req.RequestID)
			}
			r.eng.wg.Wait()
			select {
			case <-r.reboot:
			default:
				t.Fatal("a request that never armed suppressed the successful activation's reboot")
			}
			if st := r.status(); st.Phase != PhaseArmed || len(r.plat.armed) != 1 {
				t.Fatalf("the retained activation changed: %+v armed=%v", st, r.plat.armed)
			}
		})
	}
}
