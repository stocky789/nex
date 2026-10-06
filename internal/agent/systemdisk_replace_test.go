// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/hostimage"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// diskRunner stands in for the tools the replacement runs: it records every call, makes mdadm --add show up in
// the fake array the way the kernel would, and can hold or fail the add.
type diskRunner struct {
	mu       sync.Mutex
	calls    []string
	arrayDir string
	block    chan struct{} // while it is open, mdadm --add waits
	failAdd  error
}

func (r *diskRunner) Run(ctx context.Context, name string, args ...string) error {
	r.mu.Lock()
	r.calls = append(r.calls, strings.Join(append([]string{name}, args...), " "))
	block, fail := r.block, r.failAdd
	r.mu.Unlock()
	switch {
	case name == "mdadm" && len(args) > 1 && args[0] == "--create": // the node udev would create
		os.MkdirAll(filepath.Dir(args[1]), 0o755)
		os.WriteFile(args[1], nil, 0o644)
	case name == "mdadm" && len(args) == 3 && args[0] == "--add":
		if block != nil {
			select {
			case <-block:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if fail != nil {
			return fail
		}
		dir := filepath.Join(r.arrayDir, "dev-"+filepath.Base(args[2]))
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "state"), []byte("spare\n"), 0o644)
	}
	return nil
}

// hold makes mdadm --add wait until release; the run is then in its last step.
func (r *diskRunner) hold() {
	r.mu.Lock()
	r.block = make(chan struct{})
	r.mu.Unlock()
}

func (r *diskRunner) release() {
	r.mu.Lock()
	if r.block != nil {
		close(r.block)
		r.block = nil
	}
	r.mu.Unlock()
}

func (r *diskRunner) fail(err error) {
	r.mu.Lock()
	r.failAdd = err
	r.mu.Unlock()
}

func (r *diskRunner) count(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// diskLab is a host with a surviving disk (the first of a RAID1 install), a blank replacement, and the fakes the
// agent reads its disks through.
type diskLab struct {
	t       *testing.T
	dir     string
	base    string // the sandbox the host's mount directories and md sysfs live in
	disks   []string
	target  string
	hash    string
	runner  *diskRunner
	devices []vmhost.ZFSDeviceCandidate
	// installed is how many tool calls the install that set the lab up made.
	installed int
}

func newDiskLab(t *testing.T) *diskLab {
	t.Helper()
	base := t.TempDir()
	t.Cleanup(hostimage.SandboxMounts(base))
	lab := &diskLab{t: t, dir: t.TempDir(), base: base}
	size := hostimage.MinDiskBytes() + 64*hostimage.MiB
	disk := func(name string) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, size); err != nil {
			t.Fatal(err)
		}
		return p
	}
	lab.disks, lab.target = []string{disk("disk0.img"), disk("disk1.img")}, disk("new.img")
	release := t.TempDir()
	if err := hostimage.SyntheticRelease(release, "r1", 1, 1, 16); err != nil {
		t.Fatal(err)
	}
	rel, err := hostimage.ReadRelease(release)
	if err != nil {
		t.Fatal(err)
	}
	lab.hash = rel.Manifest.RootHash
	lab.runner = &diskRunner{arrayDir: filepath.Join(base, "sys-block", "md127", "md")}
	install := &hostimage.Writer{Runner: lab.runner, Boot: hostimage.UEFI}
	if err = install.Install(context.Background(), hostimage.Options{Layout: hostimage.LayoutRAID1, Disks: lab.disks, ImageDir: release}); err != nil {
		t.Fatal(err)
	}
	// The kernel's view of the mirror: degraded, with the surviving disk's partition in sync.
	for name, value := range map[string]string{"level": "raid1", "degraded": "1", "sync_action": "idle"} {
		os.MkdirAll(lab.runner.arrayDir, 0o755)
		os.WriteFile(filepath.Join(lab.runner.arrayDir, name), []byte(value+"\n"), 0o644)
	}
	member := filepath.Join(lab.runner.arrayDir, "dev-"+filepath.Base(hostimage.NodeName(lab.disks[0], hostimage.DataIndex)))
	os.MkdirAll(member, 0o755)
	os.WriteFile(filepath.Join(member, "state"), []byte("in_sync\n"), 0o644)
	os.WriteFile(filepath.Join(base, "md127"), nil, 0o644)
	os.Remove(filepath.Join(base, "md", hostimage.DataMDName))
	if err = os.Symlink("../md127", filepath.Join(base, "md", hostimage.DataMDName)); err != nil {
		t.Fatal(err)
	}

	lab.installed = len(lab.runner.calls)
	lab.devices = []vmhost.ZFSDeviceCandidate{{Path: lab.target, ByID: "/dev/disk/by-id/ata-NEW", Serial: "NEW1", SizeBytes: uint64(size),
		Eligible: true, Fingerprint: strings.Repeat("a", 64)}}
	oldDiscover, oldHost, oldBooted, oldWait, oldResolve := systemDiskDiscover, systemDiskHost, systemDiskBooted, systemDiskWait, systemDiskResolve
	systemDiskDiscover = func(context.Context) ([]vmhost.ZFSDeviceCandidate, error) { return lab.devices, nil }
	systemDiskHost = func() *hostimage.Host {
		return &hostimage.Host{W: &hostimage.Writer{Runner: lab.runner, Boot: hostimage.UEFI}, Running: func() (string, error) { return lab.disks[0], nil }}
	}
	systemDiskBooted = func(context.Context) (string, error) { return lab.hash, nil }
	systemDiskResolve = func(string) (string, error) { return lab.target, nil }
	systemDiskWait = time.Minute
	t.Cleanup(func() {
		systemDisks.Lock()
		run := systemDisks.run
		systemDisks.Unlock()
		if run != nil {
			lab.runner.release()
			<-run.done
		}
		systemDisks.Lock()
		systemDisks.run, systemDisks.lastLog = nil, ""
		systemDisks.Unlock()
		systemDiskDiscover, systemDiskHost, systemDiskBooted, systemDiskWait, systemDiskResolve = oldDiscover, oldHost, oldBooted, oldWait, oldResolve
	})
	return lab
}

func (l *diskLab) command(requestID string) protocol.Command {
	return protocol.Command{OperationID: "op-" + requestID, Kind: protocol.SystemDiskReplaceCommandKind,
		SystemDiskReplace: &protocol.SystemDiskReplaceRequest{HostID: "host-1", RequestID: requestID, Device: protocol.ZFSProvisionDevice{
			ByID: "/dev/disk/by-id/ata-NEW", Serial: "NEW1", SizeBytes: l.devices[0].SizeBytes, Fingerprint: strings.Repeat("a", 64)}}}
}

func (l *diskLab) record() *hostimage.ReplaceRecord {
	l.t.Helper()
	rec, err := hostimage.ReadReplaceRecord(systemDiskJournal(l.dir))
	if err != nil || rec == nil {
		l.t.Fatalf("no replacement record: %v", err)
	}
	return rec
}

func TestSystemDiskReplaceCommandRoutingAndAdmission(t *testing.T) {
	if !managedCommand(protocol.SystemDiskReplaceCommandKind) {
		t.Fatal("host-system-disk-replace is not an admitted managed command")
	}
	cmd := protocol.Command{OperationID: "op-1", Kind: protocol.SystemDiskReplaceCommandKind,
		SystemDiskReplace: &protocol.SystemDiskReplaceRequest{HostID: "host-1", RequestID: "req-1"}}
	if host := commandHost(cmd); host != "host-1" {
		t.Fatalf("a replacement is addressed to host %q", host)
	}
	if host := commandHost(protocol.Command{OperationID: "op-1", Kind: protocol.SystemDiskReplaceCommandKind}); host != "" {
		t.Fatalf("a replacement with no request is addressed to %q", host)
	}
}

func TestSystemDiskReplaceRefusalsAreDefiniteResults(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(*diskLab, *protocol.Command)
		want string
	}{
		"no request":     {func(l *diskLab, c *protocol.Command) { c.SystemDiskReplace = nil }, "carries no request"},
		"invalid device": {func(l *diskLab, c *protocol.Command) { c.SystemDiskReplace.Device.Fingerprint = "" }, "device is invalid"},
		"wrong confirmation": {func(l *diskLab, c *protocol.Command) {
			c.SystemDiskReplace.Device.EraseConfirmation = "/dev/disk/by-id/other"
		}, "device is invalid"},
		"serial changed": {func(l *diskLab, c *protocol.Command) { l.devices[0].Serial = "OTHER" }, "changed since it was selected"},
		"size changed":   {func(l *diskLab, c *protocol.Command) { l.devices[0].SizeBytes++ }, "changed since it was selected"},
		"fingerprint changed": {func(l *diskLab, c *protocol.Command) {
			l.devices[0].Fingerprint = strings.Repeat("b", 64)
		}, "changed since it was selected, or is in use"},
		"disk is in use":       {func(l *diskLab, c *protocol.Command) { l.devices[0].Eligible = false }, "or is in use"},
		"old data unconfirmed": {func(l *diskLab, c *protocol.Command) { l.devices[0].RequiresErase = true }, "not confirmed for erasure"},
		"disk gone":            {func(l *diskLab, c *protocol.Command) { l.devices = nil }, "no longer reports"},
	} {
		t.Run(name, func(t *testing.T) {
			lab := newDiskLab(t)
			cmd := lab.command("req-1")
			c.edit(lab, &cmd)
			result := ExecuteSystemDiskReplace(context.Background(), lab.dir, cmd)
			if result.Status != "failed" || !strings.Contains(result.Error, c.want) || result.OperationID != cmd.OperationID {
				t.Fatalf("%+v, want a failure containing %q", result, c.want)
			}
			if _, err := os.Stat(filepath.Dir(systemDiskJournal(lab.dir))); err == nil {
				t.Error("a refused request opened the replacement record")
			}
			if len(lab.runner.calls) != lab.installed {
				t.Errorf("a refused request ran %v", lab.runner.calls[lab.installed:])
			}
		})
	}
}

func TestSystemDiskReplaceRunsToCompletionAndReports(t *testing.T) {
	lab := newDiskLab(t)
	// A disk with old data needs the typed confirmation, which names that exact disk.
	lab.devices[0].RequiresErase = true
	cmd := lab.command("req-1")
	if result := ExecuteSystemDiskReplace(context.Background(), lab.dir, cmd); result.Status != "failed" {
		t.Fatalf("an unconfirmed erase ran: %+v", result)
	}
	cmd = lab.command("req-2")
	cmd.SystemDiskReplace.Device.EraseConfirmation = cmd.SystemDiskReplace.Device.ByID
	result := ExecuteSystemDiskReplace(context.Background(), lab.dir, cmd)
	if result.Status != "succeeded" || result.Error != "" {
		t.Fatalf("%+v", result)
	}
	rec := lab.record()
	if rec.Status != hostimage.ReplaceSucceeded || rec.Phase != hostimage.ReplaceComplete || rec.RequestID != "req-2" || rec.ByID != cmd.SystemDiskReplace.Device.ByID {
		t.Fatalf("record: %+v", rec)
	}
	node := hostimage.NodeName(lab.target, hostimage.DataIndex)
	if lab.runner.count("mdadm --add "+filepath.Join(lab.base, "md", hostimage.DataMDName)+" "+node) != 1 {
		t.Fatalf("the mirror member was not added exactly once: %v", lab.runner.calls)
	}

	// Replaying the request, or a redelivery of its command, answers from the record: the disk, now a member of
	// the mirror and no longer eligible, is not looked at again and nothing runs.
	lab.devices[0].Eligible = false
	before := len(lab.runner.calls)
	if again := ExecuteSystemDiskReplace(context.Background(), lab.dir, cmd); again.Status != "succeeded" || len(lab.runner.calls) != before {
		t.Fatalf("replay: %+v, ran %v", again, lab.runner.calls[before:])
	}

	// The host reports the record with the kernel's progress of the mirror onto the new disk.
	percent := 12.5
	md := protocol.HardwareMDArrays{Arrays: []protocol.MDArray{{Name: "md127", Device: "/dev/md127", Level: "raid1", RaidDisks: 2, Degraded: 1,
		SyncAction: "recover", SyncPercent: &percent, SyncSpeedKBs: 90000,
		Members: []protocol.MDMember{{Name: "other6", State: "in_sync"}, {Name: filepath.Base(node), State: "spare"}}}}}
	report := systemDiskReport(lab.dir, md)
	if report == nil || report.BootedDisk != lab.disks[0] || report.Replacement == nil {
		t.Fatalf("report: %+v", report)
	}
	r := report.Replacement
	if r.Status != "succeeded" || r.Phase != "complete" || r.RequestID != "req-2" || r.Serial != "NEW1" || r.FinishedAt == nil {
		t.Errorf("replacement: %+v", r)
	}
	if r.Resync == nil || r.Resync.Array != "md127" || r.Resync.Member != filepath.Base(node) || r.Resync.State != "spare" ||
		r.Resync.Action != "recover" || r.Resync.Percent == nil || *r.Resync.Percent != 12.5 || r.Resync.SpeedKBs != 90000 || r.Resync.Degraded != 1 {
		t.Errorf("resync: %+v", r.Resync)
	}
	// What the Controller reads is JSON: the report survives the round trip.
	b, err := json.Marshal(protocol.HostHardware{SystemDisk: report})
	if err != nil || !strings.Contains(string(b), `"system_disk":{"booted_disk"`) {
		t.Errorf("%s %v", b, err)
	}
}

func TestSystemDiskReportWithNothingToSay(t *testing.T) {
	lab := newDiskLab(t)
	systemDiskHost = func() *hostimage.Host {
		return &hostimage.Host{W: &hostimage.Writer{Runner: lab.runner, Boot: hostimage.UEFI}, Running: func() (string, error) { return "", os.ErrNotExist }}
	}
	if report := systemDiskReport(lab.dir, protocol.HardwareMDArrays{}); report != nil {
		t.Errorf("a host that knows neither its boot disk nor a replacement reported %+v", report)
	}
}

func TestSystemDiskReplaceStillRunningAnswersWithoutWaitingAndKeepsGoing(t *testing.T) {
	lab := newDiskLab(t)
	lab.runner.hold()
	systemDiskWait = 20 * time.Millisecond
	result := ExecuteSystemDiskReplace(context.Background(), lab.dir, lab.command("req-1"))
	if result.Status != "needs_attention" || !strings.HasPrefix(result.Error, "still_running:") {
		t.Fatalf("%+v", result)
	}
	if rec := lab.record(); rec.Status != hostimage.ReplaceRunning {
		t.Fatalf("record: %+v", rec)
	}
	// Another disk is refused while this one runs; this disk's command attaches to the run.
	other := lab.command("req-2")
	other.SystemDiskReplace.Device.ByID = "/dev/disk/by-id/ata-OTHER"
	if result = ExecuteSystemDiskReplace(context.Background(), lab.dir, other); result.Status != "failed" || !strings.Contains(result.Error, "still running") {
		t.Fatalf("%+v", result)
	}
	systemDisks.Lock()
	run := systemDisks.run
	systemDisks.Unlock()
	lab.runner.release()
	<-run.done
	systemDiskWait = time.Minute
	if result = ExecuteSystemDiskReplace(context.Background(), lab.dir, lab.command("req-1")); result.Status != "succeeded" {
		t.Fatalf("%+v", result)
	}
	if lab.runner.count("mdadm --add") != 1 {
		t.Errorf("the mirror member was added %d times", lab.runner.count("mdadm --add"))
	}
}

// A replacement that a restart of Core interrupted continues by itself: the record says running and the disk proves
// it is the one that was laid.
func TestSystemDiskReplaceResumesAfterARestart(t *testing.T) {
	lab := newDiskLab(t)
	lab.runner.fail(os.ErrInvalid)
	if result := ExecuteSystemDiskReplace(context.Background(), lab.dir, lab.command("req-1")); result.Status != "failed" {
		t.Fatalf("%+v", result)
	}
	// What a power cut after the boot partition leaves: the record still says running.
	rec := lab.record()
	rec.Status, rec.Error, rec.FinishedAt, rec.Phase = hostimage.ReplaceRunning, "", nil, hostimage.ReplaceData
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(systemDiskJournal(lab.dir), b, 0o600); err != nil {
		t.Fatal(err)
	}
	lab.runner.fail(nil)
	systemDisks.Lock()
	systemDisks.run = nil
	systemDisks.Unlock()
	lab.devices[0].Eligible = false // the disk carries the new partitions now: only its identity is compared

	ResumeSystemDiskReplace(context.Background(), lab.dir)
	systemDisks.Lock()
	run := systemDisks.run
	systemDisks.Unlock()
	if run == nil {
		t.Fatal("the unfinished replacement was not continued")
	}
	<-run.done
	if run.err != nil {
		t.Fatal(run.err)
	}
	if rec = lab.record(); rec.Status != hostimage.ReplaceSucceeded || rec.RequestID != "req-1" {
		t.Fatalf("record: %+v", rec)
	}
	// A finished replacement is not started again.
	systemDisks.Lock()
	systemDisks.run = nil
	systemDisks.Unlock()
	ResumeSystemDiskReplace(context.Background(), lab.dir)
	if systemDisks.run != nil {
		t.Error("a finished replacement was resumed")
	}
}

func TestSystemDiskReplaceOfTheRunningDiskIsRefusedAndWritesNothing(t *testing.T) {
	lab := newDiskLab(t)
	// Discovery would never offer it, but a stale or forged request must not get further than Core's own check.
	lab.devices = []vmhost.ZFSDeviceCandidate{{Path: lab.disks[0], ByID: "/dev/disk/by-id/ata-NEW", Serial: "NEW1",
		SizeBytes: lab.devices[0].SizeBytes, Eligible: true, Fingerprint: strings.Repeat("a", 64)}}
	result := ExecuteSystemDiskReplace(context.Background(), lab.dir, lab.command("req-1"))
	if result.Status != "failed" || !strings.Contains(result.Error, "running system started from") {
		t.Fatalf("%+v", result)
	}
	if len(lab.runner.calls) != lab.installed {
		t.Errorf("a disk selection of the running disk ran %v", lab.runner.calls[lab.installed:])
	}
}

// An unfinished replacement of a disk that was pulled would keep every other disk from being replaced: it is given
// up once its disk is no longer reported, and not while the disk is still there.
func TestSystemDiskReplaceGivesUpAnUnfinishedReplacementOfAPulledDisk(t *testing.T) {
	for _, present := range []bool{false, true} {
		lab := newDiskLab(t)
		unfinished := hostimage.ReplaceRecord{RequestID: "req-0", ByID: "/dev/disk/by-id/ata-PULLED", Serial: "PULLED1", SizeBytes: lab.devices[0].SizeBytes,
			DiskID: "11111111-2222-4333-8444-555555555555", Phase: hostimage.ReplaceSlots, Status: hostimage.ReplaceRunning, StartedAt: time.Now().UTC()}
		b, _ := json.Marshal(unfinished)
		if err := os.MkdirAll(filepath.Dir(systemDiskJournal(lab.dir)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(systemDiskJournal(lab.dir), b, 0o600); err != nil {
			t.Fatal(err)
		}
		if present {
			lab.devices = append(lab.devices, vmhost.ZFSDeviceCandidate{Path: filepath.Join(lab.dir, "pulled"), ByID: unfinished.ByID, Serial: unfinished.Serial, SizeBytes: unfinished.SizeBytes})
		}
		result := ExecuteSystemDiskReplace(context.Background(), lab.dir, lab.command("req-1"))
		if present {
			if result.Status != "failed" || !strings.Contains(result.Error, "still running") {
				t.Fatalf("a disk that is still there was given up: %+v", result)
			}
			if rec := lab.record(); rec.Status != hostimage.ReplaceRunning || rec.RequestID != "req-0" {
				t.Fatalf("record: %+v", rec)
			}
			continue
		}
		if result.Status != "succeeded" {
			t.Fatalf("a pulled disk kept the next replacement from running: %+v", result)
		}
		if rec := lab.record(); rec.Status != hostimage.ReplaceSucceeded || rec.RequestID != "req-1" || rec.ByID != "/dev/disk/by-id/ata-NEW" {
			t.Fatalf("record: %+v", rec)
		}
	}
}
