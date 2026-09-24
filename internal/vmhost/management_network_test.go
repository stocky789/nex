// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// fakeBootID and fakeUptime are the injected kernel boot identity: the rollback triggers read
// them instead of the wall clock, so a test can reboot the host, drift its RTC or spend its
// window without touching time.Now.
var (
	fakeBootID string
	fakeUptime time.Duration
)

// recordCommands fakes every command the engine may run through PATH — the seam vlan_test.go
// already uses — and returns a reader for the log of what was actually run. Tests assert on
// both what ran and, for the management bridge, what never did.
func recordCommands(t *testing.T, names ...string) func() []string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "commands.log")
	for _, name := range names {
		script := "#!/bin/sh\nprintf '%s\\n' \"" + name + " $*\" >> " + log + "\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return func() []string {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
}

// managementSysfs is the kernel view of a freshly installed host: three MAC-bound NICs and a
// br0 uplinked by nex0. ports are the extra devices enslaved to br0 — guest taps, in the tests
// that prove the bridge is never torn down under them.
func managementSysfs(t *testing.T, ports ...string) {
	t.Helper()
	sys := t.TempDir() + "/"
	t.Cleanup(NetworkSysfsRootsForTests(sys, t.TempDir()+"/"))
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for name, mac := range map[string]string{"nex0": "52:54:00:12:34:56", "nex1": "52:54:00:12:34:57", "nex2": "52:54:00:12:34:58", managementBridge: "52:54:00:12:34:56"} {
		write(sys+name+"/address", mac+"\n")
	}
	if err := os.MkdirAll(sys+managementBridge+"/bridge", 0755); err != nil {
		t.Fatal(err)
	}
	for _, port := range append([]string{"nex0"}, ports...) {
		write(sys+managementBridge+"/brif/"+port, "")
		if err := os.MkdirAll(sys+port, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../"+managementBridge, sys+port+"/master"); err != nil && !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
}

// managementRoot points the package's /etc targets at a scratch tree and installs the files a
// freshly installed host has, so no test can read or write the developer's real /etc. Every
// command the engine may run is faked and logged through PATH.
func managementRoot(t *testing.T) (root, dir string, commands func() []string) {
	t.Helper()
	root, dir = t.TempDir(), t.TempDir()
	previous := etcRoot
	etcRoot = root
	t.Cleanup(func() { etcRoot = previous })
	fakeBootID, fakeUptime = "boot-1111", time.Hour
	previousBoot := bootIdentity
	bootIdentity = func() (string, time.Duration, bool) { return fakeBootID, fakeUptime, true }
	t.Cleanup(func() { bootIdentity = previousBoot })
	previousAddrs := bridgeAddrs
	bridgeAddrs = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.0.2.77")} }
	t.Cleanup(func() { bridgeAddrs = previousAddrs })
	managementSysfs(t)
	commands = recordCommands(t, "ifup", "ifdown", "ip", "dhcpcd", "brctl")
	if err := os.MkdirAll(filepath.Join(root, "etc/network/interfaces.d"), 0755); err != nil {
		t.Fatal(err)
	}
	// What installer/gui/nex-network.c writes for a DHCP management bridge.
	installer := managementHeader + "\n# Nex Core agent adds guest VLAN bridges under interfaces.d.\n" +
		"source /etc/network/interfaces.d/*\n\nauto lo\niface lo inet loopback\n\n# enp1s0 (52:54:00:12:34:56) -> nex0\n" +
		"iface nex0 inet manual\nauto br0\niface br0 inet dhcp\n    bridge_ports nex0\n    bridge_stp off\n    bridge_fd 0\n    bridge_hw 52:54:00:12:34:56\n"
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("etc/network/interfaces", installer)
	write("etc/dhcpcd.conf", dhcpcdHeader+"\n")
	write("etc/resolv.conf", "nameserver 192.0.2.53\n")
	write("etc/network/interfaces.d/nex-nex0v100", "# a guest bridge that must never be touched\n")
	return root, dir, commands
}

// managementConnectivity is the observation the applied static spec makes count: a connection
// that terminated on the address that spec puts on the management bridge, from somewhere else.
func managementConnectivity(t *testing.T, dir, kind string, at time.Time) error {
	t.Helper()
	return RecordManagementConnectivity(dir, kind, at, netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.200"))
}

func staticSpec() ManagementNetworkSpec {
	return ManagementNetworkSpec{
		Interfaces: []ManagementNIC{
			{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "static", VLAN: 10, Address: "192.0.2.10/24", Gateway: "192.0.2.1", Management: true},
			{Name: "nex1", MAC: "52:54:00:12:34:57", Mode: "dhcp"},
			{Name: "nex2", MAC: "52:54:00:12:34:58", Mode: "disabled"},
		},
		DNS: []string{"192.0.2.53"},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestManagementNetworkRender(t *testing.T) {
	files, err := RenderManagementNetwork(staticSpec())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		managementHeader + "\n",
		"source /etc/network/interfaces.d/*\n",
		"auto lo\niface lo inet loopback\n",
		"iface nex0 inet manual\n",
		"iface nex0.10 inet manual\n    vlan-raw-device nex0\n",
		"auto br0\niface br0 inet static\n    address 192.0.2.10/24\n    gateway 192.0.2.1\n",
		"    bridge_ports nex0.10\n    bridge_stp off\n    bridge_fd 0\n    bridge_hw 52:54:00:12:34:56\n",
		"auto nex1\niface nex1 inet dhcp\n",
		"iface nex2 inet manual\n",
	} {
		if !strings.Contains(*files.Interfaces, want) {
			t.Errorf("interfaces missing %q:\n%s", want, *files.Interfaces)
		}
	}
	if strings.Contains(*files.Interfaces, "auto nex2") {
		t.Error("a disabled interface must not be brought up")
	}
	if want := "\ninterface nex1\n    nogateway\n    nohook resolv.conf\n"; !strings.Contains(*files.DHCPCD, want) {
		t.Errorf("dhcpcd must deny a secondary NIC routes and DNS:\n%s", *files.DHCPCD)
	}
	if *files.Resolv != resolvHeader+"\nnameserver 192.0.2.53\n" {
		t.Errorf("resolv.conf: %q", *files.Resolv)
	}

	// Round-trip: the inventory projection must recover the rendered Spec so the Controller
	// can prefill the edit form before any management-network.json exists.
	parsed, ok := ParseManagementNetworkSpec(*files.Interfaces, files.Resolv)
	if !ok {
		t.Fatal("ParseManagementNetworkSpec rejected a rendered file")
	}
	if !reflect.DeepEqual(parsed, staticSpec()) {
		t.Fatalf("parse round-trip:\ngot  %+v\nwant %+v", parsed, staticSpec())
	}

	// A DHCP management bridge lets DHCP own resolv.conf unless DNS is overridden.
	dhcp := ManagementNetworkSpec{Interfaces: []ManagementNIC{{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "dhcp", Management: true}}}
	files, err = RenderManagementNetwork(dhcp)
	if err != nil {
		t.Fatal(err)
	}
	if files.Resolv != nil || *files.DHCPCD != dhcpcdHeader+"\n" {
		t.Errorf("DHCP management owns DNS:\nresolv=%v\ndhcpcd=%s", files.Resolv, *files.DHCPCD)
	}
	if want := "auto br0\niface br0 inet dhcp\n    bridge_ports nex0\n"; !strings.Contains(*files.Interfaces, want) {
		t.Errorf("interfaces missing %q:\n%s", want, *files.Interfaces)
	}
	dhcp.DNS = []string{"192.0.2.53"}
	files, err = RenderManagementNetwork(dhcp)
	if err != nil {
		t.Fatal(err)
	}
	if files.Resolv == nil || !strings.Contains(*files.DHCPCD, "\ninterface br0\n    nohook resolv.conf\n") {
		t.Errorf("a DNS override must take resolv.conf away from dhcpcd:\n%s", *files.DHCPCD)
	}
}

// A freshly installed host has no management-network.json, but its /etc files are still the
// live configuration. Inventory must project them as Current with stage "none" so the
// Controller prefills the edit form instead of showing every interface as not configured.
func TestReadManagementNetworkStatusReportsCurrentWithoutRecord(t *testing.T) {
	_, dir, _ := managementRoot(t)
	status, err := ReadManagementNetworkStatus(dir)
	if err != nil || status == nil {
		t.Fatalf("status: %+v %v", status, err)
	}
	if status.Stage != protocol.ManagementNetworkNone || status.Revision != 0 || status.Spec != nil || status.Current == nil {
		t.Fatalf("expected stage none with Current only: %+v", status)
	}
	if len(status.Current.Interfaces) != 1 || !status.Current.Interfaces[0].Management || status.Current.Interfaces[0].Mode != "dhcp" || status.Current.Interfaces[0].Name != "nex0" {
		t.Fatalf("installer current: %+v", status.Current)
	}
	if len(status.Current.DNS) != 1 || status.Current.DNS[0] != "192.0.2.53" {
		t.Fatalf("installer DNS: %+v", status.Current.DNS)
	}

	// Foreign interfaces must not invent a Current or a none-stage status.
	root, dir, _ := managementRoot(t)
	if err := os.WriteFile(filepath.Join(root, "etc/network/interfaces"), []byte("# hand written\nauto lo\niface lo inet loopback\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if status, err = ReadManagementNetworkStatus(dir); err != nil || status != nil {
		t.Fatalf("foreign interfaces must stay silent: %+v %v", status, err)
	}
}

func TestManagementNetworkDHCPKeepsExistingResolvConf(t *testing.T) {
	ctx := context.Background()
	root, dir, _ := managementRoot(t)
	before := readFile(t, filepath.Join(root, "etc/resolv.conf"))
	spec := ManagementNetworkSpec{Interfaces: []ManagementNIC{{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "dhcp", Management: true}}}
	if _, err := StageManagementNetwork(dir, spec, "op-1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "etc/resolv.conf")); got != before {
		t.Fatalf("DHCP without a DNS override removed the existing resolver configuration: %q", got)
	}
}

func TestManagementNetworkValidation(t *testing.T) {
	if err := ValidateManagementNetworkSpec(staticSpec()); err != nil {
		t.Fatal(err)
	}
	mutate := func(f func(*ManagementNetworkSpec)) ManagementNetworkSpec {
		spec := staticSpec()
		spec.Interfaces = append([]ManagementNIC(nil), spec.Interfaces...)
		f(&spec)
		return spec
	}
	for name, spec := range map[string]ManagementNetworkSpec{
		"no management":        mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Management = false }),
		"two management":       mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].Management = true }),
		"dhcp with address":    mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].Address = "192.0.2.20/24" }),
		"static without CIDR":  mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Address = "192.0.2.10" }),
		"network address":      mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Address = "192.0.2.0/24" }),
		"broadcast address":    mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Address = "192.0.2.255/24" }),
		"loopback address":     mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Address = "127.0.0.1/8" }),
		"multicast address":    mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Address = "239.0.0.5/24" }),
		"IPv6 address":         mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Address = "2001:db8::1/64" }),
		"gateway elsewhere":    mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Gateway = "198.51.100.1" }),
		"gateway is broadcast": mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Gateway = "192.0.2.255" }),
		"gateway equals host":  mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].Gateway = "192.0.2.10" }),
		"gateway not on mgmt": mutate(func(s *ManagementNetworkSpec) {
			s.Interfaces[1].Mode, s.Interfaces[1].Address, s.Interfaces[1].Gateway = "static", "198.51.100.5/24", "198.51.100.1"
		}),
		"duplicate address": mutate(func(s *ManagementNetworkSpec) {
			s.Interfaces[1].Mode, s.Interfaces[1].Address = "static", "192.0.2.10/24"
		}),
		"duplicate MAC":       mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].MAC = "52:54:00:12:34:56" }),
		"multicast MAC":       mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].MAC = "53:54:00:12:34:57" }),
		"zero MAC":            mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].MAC = "00:00:00:00:00:00" }),
		"malformed MAC":       mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].MAC = "52-54-00-12-34-57" }),
		"VLAN too high":       mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].VLAN = 4095 }),
		"negative VLAN":       mutate(func(s *ManagementNetworkSpec) { s.Interfaces[0].VLAN = -1 }),
		"disabled with VLAN":  mutate(func(s *ManagementNetworkSpec) { s.Interfaces[2].VLAN = 5 }),
		"loopback name":       mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].Name = "lo" }),
		"path-like name":      mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].Name = "../etc" }),
		"long name":           mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].Name = "enx0011223344556" }),
		"duplicate name":      mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].Name = "nex0" }),
		"unknown mode":        mutate(func(s *ManagementNetworkSpec) { s.Interfaces[1].Mode = "auto" }),
		"bad DNS":             mutate(func(s *ManagementNetworkSpec) { s.DNS = []string{"not-an-address"} }),
		"disabled management": mutate(func(s *ManagementNetworkSpec) { s.Interfaces = s.Interfaces[2:] }),
	} {
		if err := ValidateManagementNetworkSpec(spec); err == nil {
			t.Errorf("%s accepted", name)
		} else if _, err := RenderManagementNetwork(spec); err == nil {
			t.Errorf("%s rendered", name)
		}
	}
}

func TestManagementNetworkStageApplyConfirm(t *testing.T) {
	ctx, root, dir := context.Background(), "", ""
	root, dir, _ = managementRoot(t)
	before := readFile(t, filepath.Join(root, "etc/network/interfaces"))

	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 7, 0); err == nil {
		t.Fatal("stale expected_revision accepted")
	}
	rec, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Stage != protocol.ManagementNetworkStaged || rec.WindowSeconds != int(DefaultManagementWindow/time.Second) {
		t.Fatalf("staged record: %+v", rec)
	}
	if readFile(t, filepath.Join(root, "etc/network/interfaces")) != before {
		t.Fatal("staging must not touch /etc")
	}
	if again, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil || again.Stage != protocol.ManagementNetworkStaged {
		t.Fatalf("re-staging the same operation must be idempotent: %v", err)
	}
	for _, window := range []time.Duration{time.Minute, time.Hour} {
		if _, err := StageManagementNetwork(dir, staticSpec(), "op-window", 0, window); err == nil {
			t.Fatalf("window %v accepted", window)
		}
	}

	if _, err := ApplyManagementNetwork(ctx, dir, "op-other"); err == nil {
		t.Fatal("applying an unknown operation accepted")
	}
	if _, err := ConfirmManagementNetwork(dir, "op-1"); err == nil {
		t.Fatal("confirming a staged change accepted")
	}
	if _, err = ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
		t.Fatal(err)
	}
	applied := readFile(t, filepath.Join(root, "etc/network/interfaces"))
	if !strings.Contains(applied, "address 192.0.2.10/24") {
		t.Fatalf("apply did not write the new configuration:\n%s", applied)
	}
	if readFile(t, filepath.Join(root, "etc/network/interfaces.d/nex-nex0v100")) != "# a guest bridge that must never be touched\n" {
		t.Fatal("guest bridges under interfaces.d must never be rewritten")
	}
	// A repeated apply is the same apply: the deadline does not move.
	rec, _ = loadManagementRecord(dir)
	repeat, err := ApplyManagementNetwork(ctx, dir, "op-1")
	if err != nil || !repeat.Deadline.Equal(rec.Deadline) {
		t.Fatalf("double apply must be idempotent: %v %v", err, repeat)
	}
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-2", 0, 0); err == nil {
		t.Fatal("staging over an applied, unconfirmed change accepted")
	}

	// Confirmation is refused until the host has been reached over the new configuration.
	if _, err := ConfirmManagementNetwork(dir, "op-1"); err == nil {
		t.Fatal("confirmed without a connectivity observation")
	}
	if err := managementConnectivity(t, dir, "carrier-pigeon", time.Now()); err == nil {
		t.Fatal("unknown observation accepted")
	}
	if err := managementConnectivity(t, dir, "local-api", rec.AppliedAt.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmManagementNetwork(dir, "op-1"); err == nil {
		t.Fatal("an observation from before the apply is not evidence")
	}
	if err := managementConnectivity(t, dir, "controller-report", time.Now()); err != nil {
		t.Fatal(err)
	}
	confirmed, err := ConfirmManagementNetwork(dir, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Revision != 1 || confirmed.Previous != nil || confirmed.ConfirmedAt.IsZero() {
		t.Fatalf("confirmed record: %+v", confirmed)
	}
	if again, err := ConfirmManagementNetwork(dir, "op-1"); err != nil || again.Revision != 1 {
		t.Fatalf("confirm must be idempotent: %v", err)
	}
	// A confirmed change leaves nothing to recover and the next edit is revision 1.
	if _, err := RecoverManagementNetwork(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(root, "etc/network/interfaces")) != applied {
		t.Fatal("a confirmed configuration must survive recovery")
	}
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-3", 0, 5*time.Minute); err != nil {
		t.Fatalf("an omitted revision must allow a new stage: %v", err)
	}
	status, err := ReadManagementNetworkStatus(dir)
	// The window the operator chose is reported, not only the deadline an apply would set:
	// a staged change must still be able to say how long it will give after a page reload.
	if err != nil || status.Revision != 1 || status.Stage != protocol.ManagementNetworkStaged || status.WindowSeconds != 300 {
		t.Fatalf("status: %+v %v", status, err)
	}
	if cancelled, err := CancelManagementNetwork(ctx, dir, "op-3"); err != nil || cancelled.Stage != managementCancelled {
		t.Fatalf("cancel a staged change: %v", err)
	}
	if again, err := CancelManagementNetwork(ctx, dir, "op-3"); err != nil || again.Stage != managementCancelled {
		t.Fatalf("cancel must be idempotent: %v", err)
	}
}

// An operator who can still reach a host whose new configuration is wrong undoes it at once:
// cancelling an applied, unconfirmed change restores the previous files byte for byte, exactly
// as the deadline would have, and records that they asked for it rather than that anything
// failed. A change that is already undone has nothing left to cancel.
func TestManagementNetworkOperatorRollback(t *testing.T) {
	ctx := context.Background()
	root, dir, _ := managementRoot(t)
	before := map[string]string{}
	for _, rel := range []string{"etc/network/interfaces", "etc/dhcpcd.conf", "etc/resolv.conf"} {
		before[rel] = readFile(t, filepath.Join(root, rel))
	}
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
		t.Fatal(err)
	}
	if applied := readFile(t, filepath.Join(root, "etc/network/interfaces")); applied == before["etc/network/interfaces"] {
		t.Fatal("apply changed nothing, so the rollback below would prove nothing")
	}
	rolled, err := CancelManagementNetwork(ctx, dir, "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Stage != managementRolledBack || !rolled.RequestedRollback || rolled.Error != ManagementRollbackRequested {
		t.Fatalf("the operator's rollback was not recorded as one: %+v", rolled)
	}
	if !rolled.Deadline.IsZero() {
		t.Fatalf("a rolled-back change must carry no deadline: %+v", rolled)
	}
	for rel, content := range before {
		if got := readFile(t, filepath.Join(root, rel)); got != content {
			t.Fatalf("%s was not restored byte for byte:\n%s", rel, got)
		}
	}
	status, err := ReadManagementNetworkStatus(dir)
	if err != nil || !status.RequestedRollback || status.Stage != managementRolledBack || status.LastError != ManagementRollbackRequested {
		t.Fatalf("status: %+v %v", status, err)
	}
	// Nothing is left to undo, and recovery has nothing to retry either.
	if _, err := CancelManagementNetwork(ctx, dir, "op-1"); !errors.Is(err, ErrManagementNetworkConflict) {
		t.Fatalf("cancelling an already rolled-back change: %v", err)
	}
	if _, err := RecoverManagementNetwork(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != before["etc/network/interfaces"] {
		t.Fatal("recovery must leave a rolled-back configuration alone")
	}
	// The revision did not move, so the operator can stage the next attempt straight away.
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-2", 0, 0); err != nil {
		t.Fatal(err)
	}
}

// expire spends the whole rollback window. The boot-relative clock is what the triggers
// actually read; the wall-clock deadline is moved with it only because it is what an operator
// is shown and what a record written before boot identity existed falls back to.
func expire(t *testing.T, dir string) *ManagementNetworkRecord {
	t.Helper()
	rec, err := loadManagementRecord(dir)
	if err != nil || rec == nil {
		t.Fatal("no record", err)
	}
	fakeUptime += time.Duration(rec.WindowSeconds)*time.Second + time.Second
	rec.Deadline = time.Now().Add(-time.Second)
	if err := saveManagementRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestManagementNetworkRollbackOnDeadline(t *testing.T) {
	ctx := context.Background()
	root, dir, _ := managementRoot(t)
	before := map[string]string{}
	for _, rel := range []string{"etc/network/interfaces", "etc/dhcpcd.conf", "etc/resolv.conf"} {
		before[rel] = readFile(t, filepath.Join(root, rel))
	}
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
		t.Fatal(err)
	}
	// Inside the window, with no reboot, recovery leaves the change alone.
	if _, err := RecoverManagementNetwork(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(root, "etc/network/interfaces")) == before["etc/network/interfaces"] {
		t.Fatal("recovery rolled back inside the window")
	}
	expire(t, dir)
	if _, err := ConfirmManagementNetwork(dir, "op-1"); err == nil {
		t.Fatal("confirmed after the deadline")
	}
	rec, err := RecoverManagementNetwork(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Stage != managementRolledBack || rec.Error == "" {
		t.Fatalf("rolled back record: %+v", rec)
	}
	for rel, want := range before {
		if got := readFile(t, filepath.Join(root, rel)); got != want {
			t.Errorf("%s was not restored byte for byte:\n%s", rel, got)
		}
	}
	// Re-running after a completed rollback changes nothing, and a late confirm is refused.
	if _, err := RecoverManagementNetwork(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmManagementNetwork(dir, "op-1"); err == nil {
		t.Fatal("confirmed a rolled-back change")
	}
	// The rolled-back attempt did not consume a revision, so the next edit stages at 0.
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-2", 0, 0); err != nil {
		t.Fatal(err)
	}
}

func TestManagementNetworkRollbackRetriedAfterFailure(t *testing.T) {
	ctx := context.Background()
	root, dir, _ := managementRoot(t)
	before := readFile(t, filepath.Join(root, "etc/network/interfaces"))
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
		t.Fatal(err)
	}
	expire(t, dir)
	fakeCommand(t, "ip", "exit 1") // the bridge will not take the previous configuration
	if _, err := RecoverManagementNetwork(ctx, dir); err == nil {
		t.Fatal("a failed rollback must be reported")
	}
	rec, _ := loadManagementRecord(dir)
	if rec.Stage != protocol.ManagementNetworkApplied || rec.Previous == nil {
		t.Fatalf("a half-finished rollback must stay retryable: %+v", rec)
	}
	if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != before {
		t.Fatalf("the previous files must already be back:\n%s", got)
	}
	fakeCommand(t, "ip", "exit 0")
	rec, err := RecoverManagementNetwork(ctx, dir)
	if err != nil || rec.Stage != managementRolledBack {
		t.Fatalf("rollback must be re-runnable: %v %+v", err, rec)
	}
	if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != before {
		t.Fatalf("the previous files must be byte-identical:\n%s", got)
	}
}

// TestManagementNetworkCrashWindows covers the two crashes that can happen around the file swap.
// The record is written before /etc is touched, so a crash before the swap leaves a record that
// promises a rollback of files that never changed, and a crash after the swap but before the
// interface bounce leaves new files that the same rollback undoes.
func TestManagementNetworkCrashWindows(t *testing.T) {
	ctx := context.Background()
	for name, crash := range map[string]func(root string){
		"before the file swap": func(string) {},
		"after the file swap": func(root string) {
			files, err := RenderManagementNetwork(staticSpec())
			if err != nil {
				t.Fatal(err)
			}
			if err := writeManagementFiles(files); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, dir, _ := managementRoot(t)
			before := readFile(t, filepath.Join(root, "etc/network/interfaces"))
			if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil {
				t.Fatal(err)
			}
			// Simulate a crash inside ApplyManagementNetwork: the record is already applied.
			rec, _ := loadManagementRecord(dir)
			now := time.Now().UTC()
			rec.Stage, rec.AppliedAt, rec.Deadline = protocol.ManagementNetworkApplied, now, now.Add(-time.Second)
			if err := saveManagementRecord(dir, rec); err != nil {
				t.Fatal(err)
			}
			crash(root)
			rec, err := RecoverManagementNetwork(ctx, dir)
			if err != nil || rec.Stage != managementRolledBack {
				t.Fatalf("recovery: %v %+v", err, rec)
			}
			if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != before {
				t.Fatalf("recovery must end on the previous files:\n%s", got)
			}
		})
	}
}

// A host that is power-cycled while an unconfirmed change is live comes back on the previous
// configuration even if the deadline has not passed: a reboot is not evidence that the new
// configuration works, and nobody was there to confirm it.
func TestManagementNetworkRollbackAfterReboot(t *testing.T) {
	ctx := context.Background()
	root, dir, _ := managementRoot(t)
	before := readFile(t, filepath.Join(root, "etc/network/interfaces"))
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 30*time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
		t.Fatal(err)
	}
	fakeBootID = "boot-2222" // the machine restarted since the apply
	rec, err := RecoverManagementNetwork(ctx, dir)
	if err != nil || rec.Stage != managementRolledBack || !strings.Contains(rec.Error, "restarted") {
		t.Fatalf("a reboot must roll the change back: %v %+v", err, rec)
	}
	if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != before {
		t.Fatalf("previous configuration:\n%s", got)
	}
}

func TestManagementNetworkRefusesForeignFiles(t *testing.T) {
	interfaces := "etc/network/interfaces"
	for name, damage := range map[string]func(root string){
		"foreign interfaces file": func(root string) {
			os.WriteFile(filepath.Join(root, interfaces), []byte("# hand written\nauto br0\n"), 0644)
		},
		"missing interfaces file": func(root string) { os.Remove(filepath.Join(root, interfaces)) },
		"foreign dhcpcd.conf": func(root string) {
			os.WriteFile(filepath.Join(root, "etc/dhcpcd.conf"), []byte("# hand written\n"), 0644)
		},
		"interfaces is a symlink": func(root string) {
			os.Remove(filepath.Join(root, interfaces))
			os.Symlink("/dev/null", filepath.Join(root, interfaces))
		},
		"resolv.conf is a symlink": func(root string) {
			os.Remove(filepath.Join(root, "etc/resolv.conf"))
			os.Symlink("../run/resolv.conf", filepath.Join(root, "etc/resolv.conf"))
		},
		"resolv.conf is a directory": func(root string) {
			os.Remove(filepath.Join(root, "etc/resolv.conf"))
			os.Mkdir(filepath.Join(root, "etc/resolv.conf"), 0755)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, dir, _ := managementRoot(t)
			damage(root)
			if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err == nil {
				t.Fatal("staged against a file Nex cannot prove it rendered")
			} else if strings.Contains(err.Error(), "conflict") {
				t.Fatalf("expected a refusal naming the file, got %v", err)
			}
		})
	}
	// A configuration Nex Core itself rendered is recognized on the next edit.
	_, dir, _ := managementRoot(t)
	files, err := RenderManagementNetwork(staticSpec())
	if err != nil {
		t.Fatal(err)
	}
	if err := writeManagementFiles(files); err != nil {
		t.Fatal(err)
	}
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil {
		t.Fatalf("Core must recognize its own render: %v", err)
	}
}

// A change staged against one configuration must not be applied onto another.
func TestManagementNetworkApplyRefusesChangedFiles(t *testing.T) {
	ctx := context.Background()
	root, dir, _ := managementRoot(t)
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "etc/resolv.conf"), []byte("nameserver 192.0.2.54\n"), 0644)
	if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err == nil {
		t.Fatal("applied against a configuration that changed after staging")
	}
}

// TestManagementNetworkNetns applies and rolls back a real configuration with the host's own
// ifupdown, on dummy interfaces, inside a disposable namespace with a private copy of /etc.
// It is a namespace simulation, not a hardware test: there is no real uplink, and a rollback
// that restores an unreachable configuration cannot be distinguished from one that does not.
// Run it as root on a host that has ifupdown and iproute2, with the copy of /etc made
// OUTSIDE the namespace (inside `unshare -U` every uid but root is unmapped, so the copy
// cannot reproduce ownership) and sysfs remounted inside it (a new network namespace still
// shows the host's /sys until it is, and the test reads /sys/class/net/nex0/address).
// `--mount-proc` cannot be used: mounting /proc in this namespace is not permitted.
//
//	go test -c -o /tmp/vmhost.test ./internal/vmhost
//	rm -rf /tmp/nex-etc && mkdir -p /tmp/nex-etc && tar -C /etc -cf - . | tar -C /tmp/nex-etc -xf -
//	cat > /tmp/netns.sh <<'EOS'
//	set -e
//	mount --bind /tmp/nex-etc /etc
//	mount -t sysfs none /sys
//	NEX_NETNS_TEST=1 /tmp/vmhost.test -test.run TestManagementNetworkNetns -test.v
//	EOS
//	unshare -Urnm sh /tmp/netns.sh
//
// The private /etc must hold a Nex-rendered interfaces file, dhcpcd.conf and resolv.conf, or
// the test skips: see s7/checks/s3_mgmt_network.py for the seed a cloud image needs.
func TestManagementNetworkNetns(t *testing.T) {
	if os.Getenv("NEX_NETNS_TEST") == "" {
		t.Skip("set NEX_NETNS_TEST=1 inside a disposable network namespace with a private /etc")
	}
	for _, tool := range []string{"ip", "ifup", "ifdown"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed; ifupdown and iproute2 are required for a real apply", tool)
		}
	}
	ctx, dir := context.Background(), t.TempDir()
	ip := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v %s", args, err, out)
		}
	}
	ip("link", "add", "nex0", "type", "dummy")
	mac := strings.TrimSpace(readFile(t, "/sys/class/net/nex0/address"))
	// The real /etc of the namespace: the caller mounted a private copy over it.
	previous := etcRoot
	etcRoot = "/"
	t.Cleanup(func() { etcRoot = previous })
	before, err := snapshotManagementFiles()
	if err != nil {
		t.Skipf("this namespace has no Nex-rendered /etc to edit: %v", err)
	}
	spec := ManagementNetworkSpec{Interfaces: []ManagementNIC{{Name: "nex0", MAC: mac, Mode: "static", Address: "192.0.2.10/24", Gateway: "192.0.2.1", Management: true}}}
	if _, err = StageManagementNetwork(dir, spec, "op-netns", 0, 0); err != nil {
		t.Fatal("stage:", err)
	}
	if _, err = ApplyManagementNetwork(ctx, dir, "op-netns"); err != nil {
		t.Fatal("apply:", err)
	}
	if out, err := exec.Command("ip", "-o", "addr", "show", "dev", managementBridge).CombinedOutput(); err != nil || !strings.Contains(string(out), "192.0.2.10/24") {
		t.Fatalf("%s did not come up on the new address: %v %s", managementBridge, err, out)
	}
	// Nothing confirms, so the window running out decides; recovery must restore every byte.
	// The trigger this drives is the real one: since the boot-relative rollback landed,
	// managementRollbackDue measures the window as elapsed /proc/uptime since the apply and
	// ignores rec.Deadline while the boot is unchanged, so moving the deadline alone no
	// longer rolls anything back (BUG-0359). Both are moved here, because a kernel that
	// reports no boot identity falls back to the deadline.
	rec, err := loadManagementRecord(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec.AppliedUptime -= time.Duration(rec.WindowSeconds+1) * time.Second
	rec.Deadline = time.Now().Add(-time.Second)
	if err = saveManagementRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	if rec, err = RecoverManagementNetwork(ctx, dir); err != nil || rec.Stage != managementRolledBack {
		t.Fatalf("recovery: %v %+v", err, rec)
	}
	after, err := snapshotManagementFiles()
	if err != nil {
		t.Fatal(err)
	}
	if !sameManagementFiles(before, after) {
		t.Fatal("the rollback did not restore the previous configuration byte for byte")
	}
	if out, _ := exec.Command("ip", "-o", "addr", "show", "dev", managementBridge).CombinedOutput(); strings.Contains(string(out), "192.0.2.10/24") {
		t.Fatalf("the rolled-back address is still live: %s", out)
	}
}

// ---------------------------------------------------------------- C-4: the bridge survives

// nomasterTargets is every device the engine detached from a bridge, and destroys reports any
// command that would have torn the management bridge down. Both read the log of what actually
// ran, which is the only thing a scratch sysfs can prove; see the lab checklist for what a real
// host has to show.
func inspectBridgeCommands(t *testing.T, log []string) (nomaster []string) {
	t.Helper()
	for _, line := range log {
		fields := strings.Fields(line)
		switch {
		case fields[0] == "ifdown", fields[0] == "brctl",
			strings.Contains(line, "link del"),
			strings.Contains(line, "link set "+managementBridge+" down"):
			t.Fatalf("a command that destroys %s ran: %q\nlog:\n%s", managementBridge, line, strings.Join(log, "\n"))
		}
		if len(fields) == 5 && fields[0] == "ip" && fields[4] == "nomaster" {
			nomaster = append(nomaster, fields[3])
		}
	}
	return nomaster
}

// Guests on the untagged default network have their taps enslaved to br0 itself. Apply, the
// deadline rollback and the operator's own rollback must all leave that bridge standing: what
// changes is its layer 3 configuration and, only when the management uplink moves, which single
// port uplinks it.
func TestManagementNetworkNeverDestroysTheBridge(t *testing.T) {
	ctx := context.Background()
	for name, finish := range map[string]func(t *testing.T, dir string){
		"confirm": func(t *testing.T, dir string) {
			if err := managementConnectivity(t, dir, "controller-report", time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := ConfirmManagementNetwork(dir, "op-1"); err != nil {
				t.Fatal(err)
			}
		},
		"deadline rollback": func(t *testing.T, dir string) {
			expire(t, dir)
			if rec, err := RecoverManagementNetwork(ctx, dir); err != nil || rec.Stage != managementRolledBack {
				t.Fatalf("recovery: %v %+v", err, rec)
			}
		},
		"operator rollback": func(t *testing.T, dir string) {
			if rec, err := CancelManagementNetwork(ctx, dir, "op-1"); err != nil || rec.Stage != managementRolledBack {
				t.Fatalf("cancel: %v %+v", err, rec)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, dir, commands := managementRoot(t)
			managementSysfs(t, "vnet0", "vnet1") // two running guests share the management bridge
			if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
				t.Fatal(err)
			}
			finish(t, dir)
			log := commands()
			if len(log) == 0 {
				t.Fatal("the apply ran no commands at all, so this proves nothing")
			}
			// The uplink moved nex0 -> nex0.10 and back; no guest tap may ever be detached.
			for _, detached := range inspectBridgeCommands(t, log) {
				if strings.HasPrefix(detached, "vnet") {
					t.Fatalf("a guest port was detached from %s: %q\nlog:\n%s", managementBridge, detached, strings.Join(log, "\n"))
				}
			}
			if !strings.Contains(strings.Join(log, "\n"), "ip link set nex0.10 master "+managementBridge) {
				t.Fatalf("the new uplink was never enslaved:\n%s", strings.Join(log, "\n"))
			}
		})
	}
}

// A change that touches only layer 3 must not move any port at all.
func TestManagementNetworkKeepsTheUplinkWhenOnlyAddressingChanges(t *testing.T) {
	ctx := context.Background()
	_, dir, commands := managementRoot(t)
	managementSysfs(t, "vnet0")
	spec := ManagementNetworkSpec{
		Interfaces: []ManagementNIC{{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "static", Address: "192.0.2.10/24", Gateway: "192.0.2.1", Management: true}},
		DNS:        []string{"192.0.2.53"},
	}
	if _, err := StageManagementNetwork(dir, spec, "op-1", 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
		t.Fatal(err)
	}
	log := commands()
	if detached := inspectBridgeCommands(t, log); len(detached) != 0 {
		t.Fatalf("the uplink did not change, yet %v was detached:\n%s", detached, strings.Join(log, "\n"))
	}
	for _, unwanted := range []string{"master " + managementBridge, "link add"} {
		if strings.Contains(strings.Join(log, "\n"), unwanted) {
			t.Fatalf("an unchanged uplink must not be re-enslaved or re-created (%q):\n%s", unwanted, strings.Join(log, "\n"))
		}
	}
	for _, want := range []string{"dhcpcd -k " + managementBridge, "ip -4 addr add 192.0.2.10/24 dev " + managementBridge, "ip -4 route replace default via 192.0.2.1 dev " + managementBridge} {
		if !strings.Contains(strings.Join(log, "\n"), want) {
			t.Fatalf("the layer 3 reconfiguration is missing %q:\n%s", want, strings.Join(log, "\n"))
		}
	}
}

// ---------------------------------------------------------------- C-3: no wall clock

// The host this feature exists for is one its own new configuration cut off, which reboots with
// whatever its RTC says. Every trigger must therefore work with a wrong, jumped or frozen clock.
func TestManagementNetworkRollbackIsClockIndependent(t *testing.T) {
	ctx := context.Background()
	// apply returns the scratch /etc, the state dir and the configuration a rollback must
	// restore, captured before the apply replaced it.
	apply := func(t *testing.T) (root, dir, before string) {
		root, dir, _ = managementRoot(t)
		before = readFile(t, filepath.Join(root, "etc/network/interfaces"))
		if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 30*time.Minute); err != nil {
			t.Fatal(err)
		}
		if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
			t.Fatal(err)
		}
		rec, err := loadManagementRecord(dir)
		if err != nil || rec.BootID == "" {
			t.Fatalf("the apply recorded no boot identity: %v %+v", err, rec)
		}
		return root, dir, before
	}

	t.Run("a reboot with a wrong RTC still rolls back", func(t *testing.T) {
		root, dir, before := apply(t)
		// The host came back on a clock an hour behind the apply, so every wall-clock
		// comparison says the window has not even started, and barely any uptime has passed.
		rec, _ := loadManagementRecord(dir)
		rec.AppliedAt, rec.Deadline = time.Now().Add(time.Hour), time.Now().Add(90*time.Minute)
		if err := saveManagementRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
		fakeBootID, fakeUptime = "boot-2222", 30*time.Second
		if rec, err := RecoverManagementNetwork(ctx, dir); err != nil || rec.Stage != managementRolledBack || rec.Error != managementRebootReason {
			t.Fatalf("a wrong RTC after a reboot disabled the rollback: %v %+v", err, rec)
		}
		if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != before {
			t.Fatalf("the previous configuration is not back:\n%s", got)
		}
	})

	t.Run("a clock that jumps forward does not roll back early", func(t *testing.T) {
		root, dir, _ := apply(t)
		applied := readFile(t, filepath.Join(root, "etc/network/interfaces"))
		rec, _ := loadManagementRecord(dir)
		rec.Deadline = time.Now().Add(-time.Hour) // as an NTP step forward would leave it
		if err := saveManagementRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
		if rec, err := RecoverManagementNetwork(ctx, dir); err != nil || rec.Stage != protocol.ManagementNetworkApplied {
			t.Fatalf("a clock step rolled a live change back: %v %+v", err, rec)
		}
		if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != applied {
			t.Fatal("a clock step must not restore anything")
		}
		// And confirming is still allowed, because nothing has actually timed out.
		if err := managementConnectivity(t, dir, "local-api", time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := ConfirmManagementNetwork(dir, "op-1"); err != nil {
			t.Fatalf("a clock step refused a confirm inside the window: %v", err)
		}
	})

	t.Run("a clock that jumps backwards does not postpone the rollback", func(t *testing.T) {
		root, dir, before := apply(t)
		rec, _ := loadManagementRecord(dir)
		rec.Deadline = time.Now().Add(time.Hour) // the clock went back; the deadline looks far off
		if err := saveManagementRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
		fakeUptime += 31 * time.Minute // but the window is spent inside this boot
		if rec, err := RecoverManagementNetwork(ctx, dir); err != nil || rec.Stage != managementRolledBack || rec.Error != managementDeadlineReason {
			t.Fatalf("the window was spent and nothing rolled back: %v %+v", err, rec)
		}
		if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != before {
			t.Fatalf("the previous configuration is not back:\n%s", got)
		}
	})

	// A record written before boot identity existed still rolls back on the comparisons it was
	// written with: an upgrade must not leave an in-flight change with no trigger at all.
	t.Run("a record from the previous format still rolls back", func(t *testing.T) {
		root, dir, before := apply(t)
		rec, _ := loadManagementRecord(dir)
		rec.BootID, rec.AppliedUptime = "", 0
		rec.AppliedAt, rec.Deadline = time.Now().Add(-time.Hour), time.Now().Add(-30*time.Minute)
		if err := saveManagementRecord(dir, rec); err != nil {
			t.Fatal(err)
		}
		fakeBootID = "" // a kernel that reports no boot id either
		if rec, err := RecoverManagementNetwork(ctx, dir); err != nil || rec.Stage != managementRolledBack {
			t.Fatalf("an old record lost its rollback: %v %+v", err, rec)
		}
		if got := readFile(t, filepath.Join(root, "etc/network/interfaces")); got != before {
			t.Fatalf("the previous configuration is not back:\n%s", got)
		}
	})
}

// ---------------------------------------------------------------- C-2: what counts as reached

// Confirmation drops the rollback, so the observation it depends on must be a connection that
// actually terminated on the management bridge under the applied configuration. A loopback call
// from the host console and a report that left over a second, unchanged NIC are exactly the two
// paths that prove nothing.
func TestManagementNetworkConnectivityMustTerminateOnTheBridge(t *testing.T) {
	ctx := context.Background()
	addr := netip.MustParseAddr
	for name, tc := range map[string]struct {
		spec          ManagementNetworkSpec
		local, peer   netip.Addr
		shouldConfirm bool
	}{
		"loopback local api":         {staticSpec(), addr("127.0.0.1"), addr("127.0.0.1"), false},
		"another NIC to controller":  {staticSpec(), addr("198.51.100.5"), addr("192.0.2.200"), false},
		"the host talking to itself": {staticSpec(), addr("192.0.2.10"), addr("127.0.0.1"), false},
		"no address at all":          {staticSpec(), netip.Addr{}, addr("192.0.2.200"), false},
		"the management address":     {staticSpec(), addr("192.0.2.10"), addr("192.0.2.200"), true},
		// On DHCP the spec names no address, so what counts is what br0 actually holds.
		"a lease br0 does not hold": {dhcpManagementSpec(), addr("192.0.2.78"), addr("192.0.2.200"), false},
		"the lease br0 holds":       {dhcpManagementSpec(), addr("192.0.2.77"), addr("192.0.2.200"), true},
	} {
		t.Run(name, func(t *testing.T) {
			_, dir, _ := managementRoot(t)
			if _, err := StageManagementNetwork(dir, tc.spec, "op-1", 0, 0); err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyManagementNetwork(ctx, dir, "op-1"); err != nil {
				t.Fatal(err)
			}
			if err := RecordManagementConnectivity(dir, "controller-report", time.Now(), tc.local, tc.peer); err != nil {
				t.Fatal(err)
			}
			_, err := ConfirmManagementNetwork(dir, "op-1")
			if tc.shouldConfirm {
				if err != nil {
					t.Fatalf("a genuine observation was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("an observation that proves nothing confirmed the change")
			}
			if !strings.Contains(err.Error(), managementBridge) {
				t.Fatalf("the refusal must say which path counts: %v", err)
			}
			if rec, _ := loadManagementRecord(dir); !rec.ConnectivityAt.IsZero() {
				t.Fatalf("the observation was recorded anyway: %+v", rec)
			}
		})
	}
}

func dhcpManagementSpec() ManagementNetworkSpec {
	return ManagementNetworkSpec{Interfaces: []ManagementNIC{{Name: "nex1", MAC: "52:54:00:12:34:57", Mode: "dhcp", Management: true}}}
}

// ---------------------------------------------------------------- C-12, P-2, P-9: stage refusals

// Staging is where a spec meets the actual host, and the only point at which nothing has been
// touched yet. A spec that names a card this machine does not have, that would take a
// registered guest network dark, or that cannot move the management uplink without rebuilding a
// bridge guests are attached to, is refused here rather than applied and self-rolled-back.
func TestManagementNetworkStageChecksTheHost(t *testing.T) {
	guestStanza := "# Nex guest network \"Servers\" (net-1): VLAN 100 on nex1. Managed by Nex Core; do not edit.\nauto nex1v100\niface nex1v100 inet manual\n    bridge_ports nex1.100\n"
	nic := func(name, mac, mode string) ManagementNIC {
		return ManagementNIC{Name: name, MAC: mac, Mode: mode}
	}
	management := func(name, mac string, vlan int) ManagementNIC {
		return ManagementNIC{Name: name, MAC: mac, Mode: "static", VLAN: vlan, Address: "192.0.2.10/24", Management: true}
	}
	for name, tc := range map[string]struct {
		setup    func(t *testing.T, root string)
		spec     ManagementNetworkSpec
		contains string
	}{
		"an interface this host does not have": {
			spec:     ManagementNetworkSpec{Interfaces: []ManagementNIC{management("nex9", "52:54:00:12:34:99", 0)}},
			contains: "nex9 does not exist on this host",
		},
		"the right name on the wrong card": {
			spec:     ManagementNetworkSpec{Interfaces: []ManagementNIC{management("nex0", "52:54:00:aa:bb:cc", 0)}},
			contains: "names a different card",
		},
		"disabling an uplink a guest network needs": {
			setup: func(t *testing.T, root string) {
				writeGuest(t, root, "nex-nex1v100", guestStanza)
			},
			spec:     ManagementNetworkSpec{Interfaces: []ManagementNIC{management("nex0", "52:54:00:12:34:56", 0), nic("nex1", "52:54:00:12:34:57", "disabled")}},
			contains: "carries registered guest networks (nex1.100) and cannot be disabled",
		},
		"taking the port a guest network is built on": {
			setup: func(t *testing.T, root string) {
				writeGuest(t, root, "nex-nex1v100", guestStanza)
			},
			spec:     ManagementNetworkSpec{Interfaces: []ManagementNIC{management("nex1", "52:54:00:12:34:57", 100)}},
			contains: "already the port of a registered guest network",
		},
		"a new uplink that belongs to another bridge": {
			setup: func(t *testing.T, root string) {
				if err := os.Symlink("../nex1v100", sysNet+"nex1/master"); err != nil {
					t.Fatal(err)
				}
			},
			spec:     ManagementNetworkSpec{Interfaces: []ManagementNIC{management("nex1", "52:54:00:12:34:57", 0)}},
			contains: "already a port of bridge nex1v100",
		},
		"moving an uplink the host cannot identify, under attached guests": {
			setup: func(t *testing.T, root string) {
				managementSysfs(t, "vnet0")
				// A Nex-rendered file that names no bridge_ports: the engine has no record of
				// which port to detach, and a guest tap is on the bridge.
				current := readFile(t, filepath.Join(root, "etc/network/interfaces"))
				if err := os.WriteFile(filepath.Join(root, "etc/network/interfaces"),
					[]byte(strings.ReplaceAll(current, "    bridge_ports nex0\n", "")), 0644); err != nil {
					t.Fatal(err)
				}
			},
			spec:     ManagementNetworkSpec{Interfaces: []ManagementNIC{management("nex0", "52:54:00:12:34:56", 10)}},
			contains: "guests are attached to it (nex0, vnet0)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			root, dir, _ := managementRoot(t)
			if tc.setup != nil {
				tc.setup(t, root)
			}
			_, err := StageManagementNetwork(dir, tc.spec, "op-1", 0, 0)
			if err == nil {
				t.Fatal("the spec was staged")
			}
			if !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("refusal %q does not contain %q", err, tc.contains)
			}
			if _, statErr := os.Stat(managementRecordPath(dir)); statErr == nil {
				t.Fatal("a refused stage must leave no record")
			}
		})
	}
	// The same guest network does not block an edit that leaves its uplink alone.
	root, dir, _ := managementRoot(t)
	writeGuest(t, root, "nex-nex1v100", guestStanza)
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil {
		t.Fatalf("an unrelated edit was refused: %v", err)
	}
}

func writeGuest(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "etc/network/interfaces.d", name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// Re-sending the same operation id with a different spec is not a retry: answering "succeeded"
// would report on the recorded change while the operator is looking at the one they just sent.
func TestManagementNetworkStageReplayWithADifferentSpecIsAConflict(t *testing.T) {
	_, dir, _ := managementRoot(t)
	if _, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil {
		t.Fatal(err)
	}
	changed := staticSpec()
	changed.Interfaces[0].Address = "192.0.2.11/24"
	if _, err := StageManagementNetwork(dir, changed, "op-1", 0, 0); !errors.Is(err, ErrManagementNetworkConflict) {
		t.Fatalf("a replay carrying a different spec must be a conflict: %v", err)
	}
	rec, err := loadManagementRecord(dir)
	if err != nil || rec.Spec.Interfaces[0].Address != "192.0.2.10/24" {
		t.Fatalf("the staged change was overwritten: %v %+v", err, rec)
	}
	if again, err := StageManagementNetwork(dir, staticSpec(), "op-1", 0, 0); err != nil || again.Stage != protocol.ManagementNetworkStaged {
		t.Fatalf("an exact retry must still replay: %v", err)
	}
}
