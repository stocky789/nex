// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// labWatchdogs builds a sysfs tree the way the kernel exposes one: a directory per device,
// with its attributes as files and its driver as a symbolic link under device/driver.
func labWatchdogs(t *testing.T, devices map[string]map[string]string) {
	t.Helper()
	root := t.TempDir()
	for name, attrs := range devices {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(dir, "device"), 0755); err != nil {
			t.Fatal(err)
		}
		for attr, value := range attrs {
			if attr == "driver" {
				if err := os.Symlink("../../../bus/platform/drivers/"+value, filepath.Join(dir, "device", "driver")); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.WriteFile(filepath.Join(dir, attr), []byte(value+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	previous, previousDMI := watchdogSysDir, dmiSysVendorPath
	watchdogSysDir, dmiSysVendorPath = root, filepath.Join(root, "sys_vendor")
	t.Cleanup(func() { watchdogSysDir, dmiSysVendorPath = previous, previousDMI })
}

// labVirtualHost makes the DMI vendor the one a hypervisor writes, which is what turns a
// hardware classification into an emulated one.
func labVirtualHost(t *testing.T, vendor string) {
	t.Helper()
	if err := os.WriteFile(dmiSysVendorPath, []byte(vendor+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// What each classification has to come out as. softdog is the load-bearing one: the owner's
// decision is that it never counts, and it is reported rather than hidden so an operator sees
// why their host is not protected.
func TestWatchdogDetectionClassifiesByDriver(t *testing.T) {
	for name, row := range map[string]struct {
		devices map[string]map[string]string
		want    protocol.PoolHostWatchdog
	}{
		"an Intel chipset TCO watchdog": {
			map[string]map[string]string{"watchdog0": {"driver": "iTCO_wdt", "identity": "iTCO_wdt", "timeout": "30", "nowayout": "0", "state": "inactive"}},
			protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware, Device: "watchdog0", Driver: "iTCO_wdt", TimeoutSeconds: 30},
		},
		"an AMD SP5100 TCO watchdog": {
			map[string]map[string]string{"watchdog0": {"driver": "sp5100_tco", "identity": "SP5100 TCO timer", "timeout": "60", "nowayout": "1"}},
			protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware, Device: "watchdog0", Driver: "sp5100_tco", TimeoutSeconds: 60, NoWayOut: true},
		},
		"the management controller's own timer, which registers with no driver link": {
			map[string]map[string]string{"watchdog0": {"identity": "IPMI", "timeout": "45"}},
			protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware, Device: "watchdog0", TimeoutSeconds: 45},
		},
		"the kernel's software watchdog, which never counts": {
			map[string]map[string]string{"watchdog0": {"driver": "softdog", "identity": "Software Watchdog", "timeout": "60"}},
			protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogSoftware, Device: "watchdog0", Driver: "softdog", TimeoutSeconds: 60},
		},
		"a driver Nex Core has never heard of": {
			map[string]map[string]string{"watchdog0": {"driver": "acme_wdt", "identity": "ACME 9000", "timeout": "10"}},
			protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogUnknown, Device: "watchdog0", Driver: "acme_wdt", TimeoutSeconds: 10},
		},
		"a device something else already holds open": {
			map[string]map[string]string{"watchdog0": {"driver": "iTCO_wdt", "timeout": "30", "state": "active"}},
			protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogHardware, Device: "watchdog0", Driver: "iTCO_wdt", TimeoutSeconds: 30, InUse: true},
		},
		"no watchdog at all": {
			map[string]map[string]string{},
			protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogNone},
		},
	} {
		t.Run(name, func(t *testing.T) {
			labWatchdogs(t, row.devices)
			if got := LocalWatchdog(); got != row.want {
				t.Fatalf("LocalWatchdog() = %#v, want %#v", got, row.want)
			}
			if err := LocalWatchdog().Valid(); err != nil {
				t.Fatalf("Nex Core reported a watchdog it would refuse: %v", err)
			}
		})
	}
}

// A host with a software watchdog and a real one must report the real one: the choice decides
// whether the pool counts that host at all.
func TestWatchdogDetectionPrefersTheHardwareDevice(t *testing.T) {
	labWatchdogs(t, map[string]map[string]string{
		"watchdog0": {"driver": "softdog", "identity": "Software Watchdog", "timeout": "60"},
		"watchdog1": {"driver": "iTCO_wdt", "identity": "iTCO_wdt", "timeout": "30"},
	})
	got := LocalWatchdog()
	if got.Kind != protocol.PoolWatchdogHardware || got.Device != "watchdog1" {
		t.Fatalf("LocalWatchdog() = %#v, want the hardware device", got)
	}
}

// The rule the whole file exists for. Opening /dev/watchdog arms the timer on most drivers, so
// a host that has not been told to arm one must not be reset by having been looked at. Two
// assertions: detection is complete with no device node anywhere, and no string literal in the
// detection source names one.
func TestWatchdogDetectionNeverOpensTheDeviceNode(t *testing.T) {
	labWatchdogs(t, map[string]map[string]string{"watchdog0": {"driver": "iTCO_wdt", "timeout": "30"}})
	if got := LocalWatchdog(); got.Kind != protocol.PoolWatchdogHardware || got.TimeoutSeconds != 30 {
		t.Fatalf("detection needed more than sysfs: %#v", got)
	}
	source, err := os.ReadFile("watchdog.go")
	if err != nil {
		t.Fatal(err)
	}
	// The property is "no open of a character device", not a spelling, so every way this
	// file could open or run one is refused as well as the path itself.
	for _, forbidden := range []string{`"/dev`, "os.Open", "os.Create", "os.WriteFile", "exec.Command", "wdctl"} {
		if strings.Contains(string(source), forbidden) {
			t.Fatalf("the watchdog detection source contains %q; detection must only read regular files", forbidden)
		}
	}
}

// A lab watchdog is a real device to the guest and no evidence at all about hardware, so a
// host that is itself a virtual machine reports it as emulated. Nex Core still arms it.
func TestAWatchdogOnAVirtualHostIsEmulated(t *testing.T) {
	for _, vendor := range []string{"QEMU", "VMware, Inc.", "innotek GmbH"} {
		labWatchdogs(t, map[string]map[string]string{"watchdog0": {"driver": "i6300esb", "timeout": "30"}})
		labVirtualHost(t, vendor)
		got := LocalWatchdog()
		if got.Kind != protocol.PoolWatchdogEmulated {
			t.Fatalf("a watchdog on a %s host reported %q, want emulated", vendor, got.Kind)
		}
		if !protocol.PoolWatchdogArmable(got.Kind) {
			t.Fatal("an emulated watchdog must still be armable, or no nested lab can exercise arming")
		}
	}
	// The same device on a physical board stays hardware.
	labWatchdogs(t, map[string]map[string]string{"watchdog0": {"driver": "i6300esb", "timeout": "30"}})
	labVirtualHost(t, "Example Systems Inc.")
	if got := LocalWatchdog(); got.Kind != protocol.PoolWatchdogHardware {
		t.Fatalf("a watchdog on a physical host reported %q, want hardware", got.Kind)
	}
}
