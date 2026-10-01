// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
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
	if st := readUpdate(t, r.cfg.Dir); st.ChannelSerial != 7 || st.RollbackFloor != 4 {
		t.Fatalf("an older pointer lowered the record: serial %d floor %d", st.ChannelSerial, st.RollbackFloor)
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
	t.Run("a record kept without the facts is completed from the image", func(t *testing.T) {
		r := newRig(t)
		newBooted(r)
		r.status()
		p := filepath.Join(r.cfg.Dir, "state.json")
		b, _ := os.ReadFile(p)
		os.WriteFile(p, bytes.Replace(b, []byte(`"state_format": 3`), []byte(`"state_format": 0`), 1), 0o600)
		if got := readUpdate(t, r.cfg.Dir).Current.StateFormat; got != 0 {
			t.Fatalf("setup: %d", got)
		}
		r.status()
		if got := readUpdate(t, r.cfg.Dir).Current.StateFormat; got != 3 {
			t.Fatalf("state_format %d", got)
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
