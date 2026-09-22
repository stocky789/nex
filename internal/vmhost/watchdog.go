// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// A watchdog is how a host shuts itself out. Every other fencing method in Nex is one host
// acting on another; this one is the machine's own hardware resetting it when Nex Core stops
// feeding a timer, which is the only exclusion that still works when the host is hung rather
// than merely unreachable.
//
// Two rules make detection safe to run on every host on every reconcile pass:
//
//   - Nothing here opens /dev/watchdog. On most drivers the open *is* the arming: the timer
//     starts and the machine resets unless something keeps writing to it. A host that has not
//     been told to arm a watchdog must not be reset by having been looked at, so detection
//     reads sysfs and names the device by its sysfs name — no path under /dev is written
//     anywhere in this file, and a test asserts that no string literal here names one.
//   - A driver Nex Core does not recognise is reported as unknown and counts as nothing. The
//     kernel's softdog is a timer inside the kernel that is meant to reset the machine, and a
//     hung kernel does not run it, so it is classified as software and never counts either.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"nex.local/nex/internal/protocol"
)

// watchdogSysDir is where the kernel exposes every registered watchdog. A package variable so
// a test can point detection at a tree it built.
var watchdogSysDir = "/sys/class/watchdog"

// watchdogKinds classifies a device. The keys are lower-case driver names, and — for the
// drivers that register without a parent device to read a driver name from — the identity
// strings those same drivers report. This is the only list: anything not in it is unknown.
var watchdogKinds = map[string]string{
	"itco_wdt":          protocol.PoolWatchdogHardware, // Intel chipset TCO.
	"sp5100_tco":        protocol.PoolWatchdogHardware, // AMD SP5100/FCH TCO.
	"sp5100 tco timer":  protocol.PoolWatchdogHardware,
	"ipmi_watchdog":     protocol.PoolWatchdogHardware, // The management controller's own timer.
	"ipmi":              protocol.PoolWatchdogHardware,
	"hpwdt":             protocol.PoolWatchdogHardware,
	"wdat_wdt":          protocol.PoolWatchdogHardware, // The ACPI WDAT table's device.
	"i6300esb":          protocol.PoolWatchdogHardware, // Also what a QEMU guest sees.
	"i6300esb timer":    protocol.PoolWatchdogHardware,
	"ib700wdt":          protocol.PoolWatchdogHardware, // Likewise.
	"ib700":             protocol.PoolWatchdogHardware,
	"mei_wdt":           protocol.PoolWatchdogHardware,
	"sbsa_gwdt":         protocol.PoolWatchdogHardware, // Arm SBSA generic watchdog.
	"softdog":           protocol.PoolWatchdogSoftware,
	"software watchdog": protocol.PoolWatchdogSoftware,
}

// watchdogVirtualVendors are the DMI system vendors that mean this host is itself a virtual
// machine. Three of the drivers above — i6300esb, ib700wdt and the ACPI wdat_wdt — are real
// chips on a physical board and also what a hypervisor presents to a guest, so the driver
// name alone cannot tell the two apart. A device on a virtual host really does reset that
// host, so Nex Core still arms it; it is reported as emulated so that no proof, row or guide
// sentence resting on it is ever read as evidence about physical hardware.
var watchdogVirtualVendors = map[string]bool{
	"qemu": true, "bochs": true, "kvm": true, "vmware, inc.": true,
	"innotek gmbh": true, "oracle corporation": true, "xen": true,
	"microsoft corporation": true, "parallels software international inc.": true,
	"amazon ec2": true, "google": true, "alibaba cloud": true,
}

// dmiSysVendorPath is the one file that answers "is this host a virtual machine". It is read,
// never executed: detection on this path must stay a sequence of regular-file reads.
var dmiSysVendorPath = "/sys/class/dmi/id/sys_vendor"

func hostIsVirtual() bool {
	b, err := os.ReadFile(dmiSysVendorPath)
	return err == nil && watchdogVirtualVendors[strings.ToLower(strings.TrimSpace(string(b)))]
}

// LocalWatchdog is this host's own watchdog capability, for the report the master records. A
// host with several devices reports the one Nex Core would use — the first hardware one, else
// the first of any kind — and says how many it found.
func LocalWatchdog() protocol.PoolHostWatchdog {
	entries, err := os.ReadDir(watchdogSysDir)
	if err != nil {
		return protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogNone}
	}
	virtual := hostIsVirtual()
	found := []protocol.PoolHostWatchdog{}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "watchdog") {
			continue
		}
		found = append(found, readWatchdog(filepath.Join(watchdogSysDir, entry.Name()), entry.Name(), virtual))
	}
	if len(found) == 0 {
		return protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogNone}
	}
	chosen := found[0]
	for _, w := range found {
		if protocol.PoolWatchdogArmable(w.Kind) {
			chosen = w
			break
		}
	}
	return chosen
}

// readWatchdog reads one device's sysfs directory. Every attribute is optional: drivers differ
// over which they expose, and an attribute that is not there is reported as not known rather
// than guessed at.
func readWatchdog(dir, name string, virtual bool) protocol.PoolHostWatchdog {
	w := protocol.PoolHostWatchdog{Device: name}
	identity := watchdogAttr(dir, "identity")
	// The driver name is the module behind the device, which is what the classification is
	// made on. A device with no parent driver link — the IPMI watchdog is one — leaves it
	// empty and is classified on the identity the kernel gave it instead.
	if link, err := os.Readlink(filepath.Join(dir, "device", "driver")); err == nil {
		w.Driver = filepath.Base(link)
	}
	w.Kind = watchdogKinds[strings.ToLower(w.Driver)]
	if w.Kind == "" {
		w.Kind = watchdogKinds[strings.ToLower(identity)]
	}
	if w.Kind == "" {
		w.Kind = protocol.PoolWatchdogUnknown
	}
	if w.Kind == protocol.PoolWatchdogHardware && virtual {
		w.Kind = protocol.PoolWatchdogEmulated
	}
	if timeout, err := strconv.Atoi(watchdogAttr(dir, "timeout")); err == nil && timeout > 0 {
		w.TimeoutSeconds = timeout
	}
	w.NoWayOut = watchdogAttr(dir, "nowayout") == "1"
	// "active" is the kernel saying the timer is running, which means something already
	// holds this device open. It is read, never acted on: taking a device another program
	// is feeding would be two owners of one timer.
	w.InUse = watchdogAttr(dir, "state") == "active"
	return w
}

func watchdogAttr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
