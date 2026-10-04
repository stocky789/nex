// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

const replacementByID = "/dev/disk/by-id/disk-new"

func testZFSReplaceRequest() protocol.ZFSReplaceRequest {
	return protocol.ZFSReplaceRequest{OperationID: "zfs-replace-1", RepositoryID: "repo-1", PoolID: "pool-1", HostID: "host-1",
		OldDevice: "/dev/disk/by-id/disk-old-part1",
		Device:    protocol.ZFSProvisionDevice{ByID: replacementByID, Serial: "new-serial", SizeBytes: 1073741824, Fingerprint: testZFSFingerprint}}
}

// replaceLab stubs the pool observation, the disk listing and every command, and records
// the commands in order: which destructive command ran, and when, is the whole safety story.
type replaceLab struct {
	calls  [][]string
	status *protocol.ZFSPoolStatus
	disks  []ZFSDeviceCandidate
	state  string
}

func newReplaceLab(t *testing.T, failedState string) *replaceLab {
	t.Helper()
	lab := &replaceLab{state: t.TempDir(), disks: []ZFSDeviceCandidate{{ByID: replacementByID, Serial: "new-serial", SizeBytes: 1073741824, Eligible: true, Fingerprint: testZFSFingerprint}}}
	// Leaves are named as a provisioned pool reports them: the partition ZFS created on
	// the selected whole disk, which is never the by-id name of the disk itself.
	lab.status = &protocol.ZFSPoolStatus{Name: "nex-repo-1", State: "DEGRADED", Devices: []protocol.ZFSPoolDevice{
		{Name: "/dev/disk/by-id/disk-keep-part1", Path: "/dev/disk/by-id/disk-keep-part1", State: "ONLINE"},
		{Name: "/dev/disk/by-id/disk-old-part1", Path: "/dev/disk/by-id/disk-old-part1", State: failedState},
	}}
	oldStatus, oldRun, oldDiscover := zfsReplaceStatus, zfsProvisionRun, mdDiscoverDevice
	t.Cleanup(func() { zfsReplaceStatus, zfsProvisionRun, mdDiscoverDevice = oldStatus, oldRun, oldDiscover })
	zfsReplaceStatus = func(context.Context, string) *protocol.ZFSPoolStatus { return lab.status }
	mdDiscoverDevice = func(context.Context) ([]ZFSDeviceCandidate, error) { return lab.disks, nil }
	zfsProvisionRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		lab.calls = append(lab.calls, append([]string{name}, args...))
		return nil, nil
	}
	return lab
}

func (l *replaceLab) ran(command ...string) bool {
	return slices.ContainsFunc(l.calls, func(call []string) bool { return slices.Equal(call, command) })
}

func TestZFSReplaceWipesThenReplacesAndReplaysItsOperation(t *testing.T) {
	lab := newReplaceLab(t, "OFFLINE")
	request := testZFSReplaceRequest()
	if err := NewZFSReplacer(lab.state).Replace(context.Background(), request); err != nil {
		t.Fatalf("Replace() = %v", err)
	}
	if !lab.ran("wipefs", "--all", replacementByID) || !lab.ran("zpool", "replace", "nex-repo-1", request.OldDevice, replacementByID) {
		t.Fatalf("unexpected commands: %#v", lab.calls)
	}
	if lab.calls[0][0] != "wipefs" {
		t.Fatalf("the disk was used before it was cleared: %#v", lab.calls)
	}
	// A redelivered operation ID replays its recorded outcome rather than acting again.
	before := len(lab.calls)
	if err := NewZFSReplacer(lab.state).Replace(context.Background(), request); err != nil || len(lab.calls) != before {
		t.Fatalf("the replayed operation acted again: %v, %#v", err, lab.calls)
	}
}

func TestZFSReplaceRefusesAnOnlineOrAbsentDeviceWithoutTouchingDisks(t *testing.T) {
	for name, prepare := range map[string]func(*replaceLab){
		"online":  func(*replaceLab) {},
		"absent":  func(l *replaceLab) { l.status.Devices = l.status.Devices[:1] },
		"unknown": func(l *replaceLab) { l.status = nil },
	} {
		lab := newReplaceLab(t, "ONLINE")
		prepare(lab)
		err := NewZFSReplacer(lab.state).Replace(context.Background(), testZFSReplaceRequest())
		if err == nil || len(lab.calls) != 0 {
			t.Fatalf("%s: err = %v, calls = %#v", name, err, lab.calls)
		}
	}
}

func TestZFSReplaceRefusesAnIneligibleReplacementAndOldDataWithoutConfirmation(t *testing.T) {
	lab := newReplaceLab(t, "FAULTED")
	lab.disks[0].Eligible, lab.disks[0].Reason = false, "in use"
	if err := NewZFSReplacer(lab.state).Replace(context.Background(), testZFSReplaceRequest()); err == nil || len(lab.calls) != 0 {
		t.Fatalf("an ineligible replacement was used: %v, %#v", err, lab.calls)
	}
	dirty := newReplaceLab(t, "FAULTED")
	dirty.disks[0].RequiresErase = true
	if err := NewZFSReplacer(dirty.state).Replace(context.Background(), testZFSReplaceRequest()); err == nil || len(dirty.calls) != 0 {
		t.Fatalf("a disk with old data was erased without a confirmation: %v, %#v", err, dirty.calls)
	}
	// The same operation, now carrying the typed confirmation, is what clears the disk.
	confirmed := testZFSReplaceRequest()
	confirmed.Device.EraseConfirmation = replacementByID
	if err := NewZFSReplacer(dirty.state).Replace(context.Background(), confirmed); err != nil || !dirty.ran("wipefs", "--all", replacementByID) {
		t.Fatalf("the confirmed replacement did not run: %v, %#v", err, dirty.calls)
	}
	swapped := testZFSReplaceRequest()
	swapped.Device.ByID = "/dev/disk/by-id/disk-other"
	if err := NewZFSReplacer(dirty.state).Replace(context.Background(), swapped); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("the operation ID was reused for a different disk: %v", err)
	}
}

// An agent that stopped between wipefs and zpool replace resumes: the disk it already
// cleared cannot match its fingerprint any more, so the identity that survives a wipe is
// what is checked, and the disk is never cleared a second time by another operation.
func TestZFSReplaceResumesAfterAnInterruptedWipe(t *testing.T) {
	lab := newReplaceLab(t, "OFFLINE")
	request := testZFSReplaceRequest()
	journal := `{"request":{"operation_id":"zfs-replace-1","repository_id":"repo-1","pool_id":"pool-1","host_id":"host-1","old_device":"/dev/disk/by-id/disk-old-part1","device":{"by_id":"` + replacementByID + `","serial":"new-serial","size_bytes":1073741824,"fingerprint":"` + testZFSFingerprint + `"}},"stage":"wipe-started","status":"needs_attention"}`
	if err := os.WriteFile(filepath.Join(lab.state, request.OperationID+".json"), []byte(journal), 0600); err != nil {
		t.Fatal(err)
	}
	lab.disks[0].Fingerprint = strings.Repeat("b", 64)
	if err := NewZFSReplacer(lab.state).Replace(context.Background(), request); err != nil {
		t.Fatalf("the interrupted replacement did not resume: %v", err)
	}
	if !lab.ran("zpool", "replace", "nex-repo-1", request.OldDevice, replacementByID) {
		t.Fatalf("the resumed replacement did not run: %#v", lab.calls)
	}
	// A different disk in that slot is never accepted on the strength of the journal.
	swapped := newReplaceLab(t, "OFFLINE")
	if err := os.WriteFile(filepath.Join(swapped.state, request.OperationID+".json"), []byte(journal), 0600); err != nil {
		t.Fatal(err)
	}
	swapped.disks[0].Serial, swapped.disks[0].Fingerprint = "another-serial", strings.Repeat("b", 64)
	if err := NewZFSReplacer(swapped.state).Replace(context.Background(), request); err == nil || len(swapped.calls) != 0 {
		t.Fatalf("a swapped disk was accepted: %v, %#v", err, swapped.calls)
	}
}

// A pool that is already resilvering onto the replacement is the success this operation was
// asking for; ZFS retires the old disk itself when it finishes. The replacement is in the
// pool as the partition ZFS made on it, while the failed leaf is still reported inside the
// replacing- vdev, so a retry that did not recognise the disk would clear a live member.
func TestZFSReplaceAdoptsAPoolAlreadyResilveringOntoTheReplacement(t *testing.T) {
	lab := newReplaceLab(t, "OFFLINE")
	lab.status.Devices = append(lab.status.Devices, protocol.ZFSPoolDevice{Name: replacementByID + "-part1", Path: replacementByID + "-part1", State: "ONLINE"})
	lab.status.Scan = &protocol.ZFSPoolScan{Function: "RESILVER", State: "SCANNING"}
	if err := NewZFSReplacer(lab.state).Replace(context.Background(), testZFSReplaceRequest()); err != nil || len(lab.calls) != 0 {
		t.Fatalf("the running resilver was not adopted: %v, %#v", err, lab.calls)
	}
}

// A disk that is already an ordinary healthy member must not be adopted as success while
// the named failed leaf is still present without a running resilver — that would let the
// master rewrite two failed slots onto one disk.
func TestZFSReplaceRefusesAHealthyMemberAlreadyInThePool(t *testing.T) {
	lab := newReplaceLab(t, "OFFLINE")
	lab.status.Devices = append(lab.status.Devices, protocol.ZFSPoolDevice{Name: replacementByID + "-part1", Path: replacementByID + "-part1", State: "ONLINE"})
	err := NewZFSReplacer(lab.state).Replace(context.Background(), testZFSReplaceRequest())
	if err == nil || !strings.Contains(err.Error(), "already part of the pool") || len(lab.calls) != 0 {
		t.Fatalf("a healthy member was adopted: %v, %#v", err, lab.calls)
	}
}

// OldDevice already inside a replacing vdev is refused before wipefs, so a second submit
// cannot erase another spare while the first resilver is still running.
func TestZFSReplaceRefusesOldDeviceAlreadyBeingReplaced(t *testing.T) {
	lab := newReplaceLab(t, "OFFLINE")
	lab.status.Devices[1].Parent = "replacing"
	err := NewZFSReplacer(lab.state).Replace(context.Background(), testZFSReplaceRequest())
	if err == nil || !strings.Contains(err.Error(), "already being replaced") || len(lab.calls) != 0 {
		t.Fatalf("a nested replace wiped a disk: %v, %#v", err, lab.calls)
	}
}

// The replacement must be at least as large as the failed leaf's whole disk before wipefs.
func TestZFSReplaceRefusesAnUndersizedReplacement(t *testing.T) {
	lab := newReplaceLab(t, "OFFLINE")
	lab.disks = append(lab.disks, ZFSDeviceCandidate{ByID: "/dev/disk/by-id/disk-old", Serial: "old-serial", SizeBytes: 2 << 30, Eligible: true, Fingerprint: strings.Repeat("c", 64)})
	lab.disks[0].SizeBytes = 1 << 30
	request := testZFSReplaceRequest()
	request.Device.SizeBytes = 1 << 30
	err := NewZFSReplacer(lab.state).Replace(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "smaller than the failed disk") || len(lab.calls) != 0 {
		t.Fatalf("an undersized disk was wiped: %v, %#v", err, lab.calls)
	}
}

// wipe-started is durable before wipefs, and a resume still demands erase confirmation when
// the live disk has gained data since the interrupted attempt.
func TestZFSReplaceJournalsWipeStartedBeforeWipeAndKeepsEraseCheck(t *testing.T) {
	lab := newReplaceLab(t, "OFFLINE")
	oldSave := saveZFSReplaceJournal
	t.Cleanup(func() { saveZFSReplaceJournal = oldSave })
	var stages []string
	saveZFSReplaceJournal = func(path string, journal zfsReplaceJournal) error {
		stages = append(stages, journal.Stage)
		lab.calls = append(lab.calls, []string{"journal", journal.Stage})
		return oldSave(path, journal)
	}
	if err := NewZFSReplacer(lab.state).Replace(context.Background(), testZFSReplaceRequest()); err != nil {
		t.Fatalf("Replace() = %v", err)
	}
	marked, wiped := -1, -1
	for i, call := range lab.calls {
		if call[0] == "journal" && call[1] == "wipe-started" && marked < 0 {
			marked = i
		}
		if call[0] == "wipefs" && wiped < 0 {
			wiped = i
		}
	}
	if marked < 0 || wiped < 0 || marked > wiped {
		t.Fatalf("wipe was not durable before it happened: %#v", lab.calls)
	}

	resume := newReplaceLab(t, "OFFLINE")
	request := testZFSReplaceRequest()
	journal := `{"request":{"operation_id":"zfs-replace-1","repository_id":"repo-1","pool_id":"pool-1","host_id":"host-1","old_device":"/dev/disk/by-id/disk-old-part1","device":{"by_id":"` + replacementByID + `","serial":"new-serial","size_bytes":1073741824,"fingerprint":"` + testZFSFingerprint + `"}},"stage":"wipe-started","status":"needs_attention"}`
	if err := os.WriteFile(filepath.Join(resume.state, request.OperationID+".json"), []byte(journal), 0600); err != nil {
		t.Fatal(err)
	}
	resume.disks[0].Fingerprint = strings.Repeat("b", 64)
	resume.disks[0].RequiresErase = true
	if err := NewZFSReplacer(resume.state).Replace(context.Background(), request); err == nil || !strings.Contains(err.Error(), "old data") || len(resume.calls) != 0 {
		t.Fatalf("wipe-started resume skipped erase confirmation: %v, %#v", err, resume.calls)
	}
}

// The same adoption against `zpool status --json` output rather than a hand-built
// observation. testdata/zpool-status-replacing-derived.json is derived, not captured: the
// captured degraded output of the lab pool with its two leaves renamed to the by-id
// partition paths a provisioned pool uses, the failed leaf and its replacement moved into a
// replacing-0 vdev, and the captured resilver's scan copied in.
func TestZFSReplaceAdoptsADecodedReplacingVdev(t *testing.T) {
	raw, err := os.ReadFile("testdata/zpool-status-replacing-derived.json")
	if err != nil {
		t.Fatal(err)
	}
	status := decodeZFSPoolStatus(raw, "fixturepool")
	if status == nil || len(status.Devices) != 3 {
		t.Fatalf("derived replacing fixture decoded as %#v", status)
	}
	lab := newReplaceLab(t, "OFFLINE")
	lab.status = status
	request := testZFSReplaceRequest()
	request.OldDevice = "/dev/disk/by-id/virtio-NEX-CORE1-DATA-5-part1"
	request.Device.ByID = "/dev/disk/by-id/virtio-NEX-CORE1-DATA-4"
	if err := NewZFSReplacer(lab.state).Replace(context.Background(), request); err != nil || len(lab.calls) != 0 {
		t.Fatalf("the resilver reported by ZFS was not adopted: %v, %#v", err, lab.calls)
	}
}

func TestZFSReplaceRefusesAnUnreadableJournal(t *testing.T) {
	lab := newReplaceLab(t, "OFFLINE")
	if err := os.WriteFile(filepath.Join(lab.state, "zfs-replace-1.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	err := NewZFSReplacer(lab.state).Replace(context.Background(), testZFSReplaceRequest())
	if err == nil || len(lab.calls) != 0 || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an unreadable journal did not stop the replacement: %v, %#v", err, lab.calls)
	}
}

// ZFS replace takes the same host-wide disk-ops flock as system-disk replace and ZFS provision.
func TestZFSReplaceTakesHostDiskOpsLock(t *testing.T) {
	host := t.TempDir()
	state := filepath.Join(host, "zfs-replace")
	if err := os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	held, err := withExclusiveDiskLock(filepath.Join(host, "system-disk-replace"), errors.New("locked"))
	if err != nil {
		t.Fatal(err)
	}
	lab := newReplaceLab(t, "OFFLINE")
	lab.state = state
	done := make(chan error, 1)
	go func() { done <- NewZFSReplacer(state).Replace(context.Background(), testZFSReplaceRequest()) }()
	select {
	case err := <-done:
		held()
		t.Fatalf("ZFS replace ran while the host disk-ops lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	held()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ZFS replace after unlock: %v", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("ZFS replace did not proceed after the host disk-ops lock was released")
	}
}
