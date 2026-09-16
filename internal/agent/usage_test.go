// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"strings"
	"testing"
	"time"
)

// A rate needs two samples and an honest window. Every refusal here would otherwise publish a
// number an operator would act on: a wrapped subtraction after a guest reboot, a divide by
// zero, or an average over a window nobody measured.
func TestUsageRateNeedsTwoSamplesAndRefusesAResetCounter(t *testing.T) {
	base := time.Now()
	previous := usageSample{at: base, cpuNS: 2e9}
	for _, c := range []struct {
		name    string
		at      time.Time
		cpuNS   uint64
		vcpus   uint64
		percent uint16
		window  uint32
		ok      bool
	}{
		{"half of two vCPUs busy", base.Add(10 * time.Second), 3e9, 2, 5, 10000, true},
		{"every vCPU busy", base.Add(10 * time.Second), 22e9, 2, 100, 10000, true},
		{"counter reset by a guest reboot", base.Add(10 * time.Second), 1e9, 2, 0, 0, false},
		{"no vCPUs is a divide by zero", base.Add(10 * time.Second), 3e9, 0, 0, 0, false},
		{"implausible vCPU count is a misparse", base.Add(10 * time.Second), 3e9, 4096, 0, 0, false},
		{"same instant is not a window", base, 3e9, 2, 0, 0, false},
		{"window older than the sample cap", base.Add(3 * time.Minute), 3e9, 2, 0, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			percent, window, ok := usageRate(previous, c.at, c.cpuNS, c.vcpus)
			if ok != c.ok || (ok && (percent != c.percent || window != c.window)) {
				t.Fatalf("usageRate = (%d, %d, %v), want (%d, %d, %v)", percent, window, ok, c.percent, c.window, c.ok)
			}
		})
	}
}

// A counter delta large enough to wrap uint64 when multiplied by 100 must clamp, not wrap. A
// wrapped product lands on a plausible small number instead of an obviously wrong one, which is
// the failure an operator would never catch.
func TestUsageRateClampsInsteadOfOverflowing(t *testing.T) {
	base := time.Now()
	previous := usageSample{at: base, cpuNS: 0}
	percent, window, ok := usageRate(previous, base.Add(time.Second), 1<<62, 1)
	if !ok || percent != usageMaxPercent {
		t.Fatalf("usageRate = (%d, %d, %v), want (%d, 1000, true)", percent, window, ok, usageMaxPercent)
	}
}

// A sub-millisecond window must still report 1, never 0: the Controller refuses a rate paired
// with a zero window, and that refusal discards the whole host report.
func TestUsageRateFloorsTheWindowAtOneMillisecond(t *testing.T) {
	base := time.Now()
	previous := usageSample{at: base, cpuNS: 0}
	_, window, ok := usageRate(previous, base.Add(100*time.Microsecond), 1000, 1)
	if !ok || window != 1 {
		t.Fatalf("window = %d (ok=%v), want 1", window, ok)
	}
}

const vmID = "0f3c2a71-5d6e-4b8a-9c1d-2e4f6a8b0c2d"

func domstats(extra ...string) string {
	return strings.Join(append([]string{
		"cpu.time=5000000000",
		"vcpu.current=2",
		"balloon.current=2097152",
		"balloon.maximum=4194304",
	}, extra...), "\n")
}

// Only the VM's own disks count. An installer ISO mounted from the pool's media directory is
// not this VM's storage, and counting it would report a VM as larger than it is for as long as
// the disc stays in the drive.
func TestParseDomainStatsCountsOnlyThisVMsOwnDisks(t *testing.T) {
	out := domstats(
		"balloon.rss=3145728",
		"block.count=2",
		"block.0.path=/srv/pool/nex-vms/"+vmID+"/disk-0.qcow2",
		"block.0.physical=1073741824",
		"block.0.capacity=21474836480",
		"block.1.path=/srv/pool/nex-media/debian-13.iso",
		"block.1.physical=999",
		"block.1.capacity=999",
	)
	s, ok := parseDomainStats(vmID, out)
	if !ok {
		t.Fatal("sample rejected")
	}
	if s.diskUsedBytes == nil || *s.diskUsedBytes != 1073741824 {
		t.Fatalf("diskUsedBytes = %v, want the qcow2 only", s.diskUsedBytes)
	}
	if s.diskCapacityBytes == nil || *s.diskCapacityBytes != 21474836480 {
		t.Fatalf("diskCapacityBytes = %v, want the qcow2 only", s.diskCapacityBytes)
	}
	if s.balloonRSSKiB == nil || *s.balloonRSSKiB != 3145728 {
		t.Fatalf("balloonRSSKiB = %v", s.balloonRSSKiB)
	}
	if s.cpuNS != 5000000000 || s.vcpus != 2 || s.balloonCurrentKiB != 2097152 {
		t.Fatalf("cpu/vcpu/balloon misparsed: %+v", s)
	}
}

// --nowait means libvirt answers what it can without the domain's job and omits the rest.
// Those members must come back absent, which is a different fact from zero.
func TestParseDomainStatsLeavesMonitorMembersUnknown(t *testing.T) {
	s, ok := parseDomainStats(vmID, domstats())
	if !ok {
		t.Fatal("sample rejected")
	}
	if s.balloonRSSKiB != nil || s.diskUsedBytes != nil || s.diskCapacityBytes != nil {
		t.Fatalf("absent members must stay nil, got %+v", s)
	}
}

// A block entry libvirt answered without its sizes is skipped rather than counted as zero,
// which would report a VM's disks as empty while it is busy.
func TestParseDomainStatsSkipsADiskWithoutSizes(t *testing.T) {
	out := domstats(
		"block.count=1",
		"block.0.path=/srv/pool/nex-vms/"+vmID+"/disk-0.qcow2",
	)
	s, ok := parseDomainStats(vmID, out)
	if !ok {
		t.Fatal("sample rejected")
	}
	if s.diskUsedBytes != nil || s.diskCapacityBytes != nil {
		t.Fatalf("a disk without sizes must not be counted, got %+v", s)
	}
}

// Without the CPU counter there is no rate on this tick or the next, so the sample is worthless
// and the probe must re-report the previous one instead of caching this.
func TestParseDomainStatsRejectsASampleWithoutTheCPUCounter(t *testing.T) {
	if _, ok := parseDomainStats(vmID, "vcpu.current=2\nballoon.current=2097152"); ok {
		t.Fatal("a sample with no cpu.time must be rejected")
	}
}
