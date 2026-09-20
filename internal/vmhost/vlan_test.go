// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func vlanNet(uplink string, tag int) protocol.NetworkRegistration {
	return protocol.NetworkRegistration{ID: "net-1", PoolID: "pool", HostID: "host", Name: "Servers", Kind: "vlan", Uplink: uplink, VLAN: tag, Source: protocol.GuestBridge(uplink, tag)}
}

func TestVLANShapeAndRendering(t *testing.T) {
	good := vlanNet("nex1", 100)
	if err := protocol.ValidateVLAN(good); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]protocol.NetworkRegistration{
		"tag too high":     vlanNet("nex1", 4095),
		"negative tag":     vlanNet("nex1", -1),
		"long uplink":      vlanNet("enx001122334455", 10),
		"dotted uplink":    vlanNet("nex1.5", 10),
		"wrong source":     {Kind: "vlan", Uplink: "nex1", VLAN: 100, Source: "br0"},
		"path-like uplink": vlanNet("../x", 1),
	} {
		if protocol.ValidateVLAN(bad) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if protocol.GuestBridge("nex0", 4094) != "nex0v4094" || len(protocol.GuestBridge("abcdefghij", 4094)) > 15 {
		t.Fatal("bridge naming must stay within IFNAMSIZ")
	}
	if guestPort(good) != "nex1.100" || guestPort(vlanNet("nex1", 0)) != "nex1" {
		t.Fatal("port derivation")
	}
	text := interfacesFile(good)
	for _, want := range []string{"auto nex1v100\n", "iface nex1v100 inet manual\n", "ip link add link nex1 name nex1.100 type vlan id 100\n", "    bridge_ports nex1.100\n", "    bridge_stp off\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("stanza missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(interfacesFile(vlanNet("nex1", 0)), "type vlan") {
		t.Fatal("untagged bridge must not create a VLAN device")
	}
	installer := "auto lo\niface lo inet loopback\n\niface nex0 inet manual\nauto br0\niface br0 inet dhcp\n    bridge_ports nex0\n\nauto nex1\niface nex1 inet static\n    address 192.0.2.2/24\n\niface nex2 inet manual\n"
	if !hostOwnsUplink(installer, "nex1") || hostOwnsUplink(installer, "nex0") || hostOwnsUplink(installer, "nex2") || hostOwnsUplink(installer, "nex") {
		t.Fatal("host address ownership detection")
	}
	// Domain XML attaches VLAN networks as plain bridges.
	b, err := testDomainXML(protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", Name: "g", CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512}, Disks: []protocol.VMDisk{{StorageID: "st-1", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}, NICs: []protocol.VMNIC{{NetworkID: "net-1", Model: "virtio"}}, Boot: protocol.VMBoot{Firmware: "bios"}}, []protocol.NetworkRegistration{good}, []string{"/d"}, "")
	if err != nil || !strings.Contains(string(b), `<interface type="bridge">`) || !strings.Contains(string(b), `<source bridge="nex1v100">`) {
		t.Fatalf("VLAN domain interface: %v\n%s", err, b)
	}
	// The engine rejects mixed or malformed network kinds before touching the host.
	for _, n := range []protocol.NetworkRegistration{{ID: "n", PoolID: "p", HostID: "h", Kind: "bridge", Source: "br0", VLAN: 5}, {ID: "n", PoolID: "p", HostID: "h", Kind: "vlan", Uplink: "nex0", VLAN: 5, Source: "other"}} {
		n := n
		if validateManaged(protocol.Command{OperationID: "op", Kind: "network-register", Network: &n}) == nil {
			t.Errorf("accepted %+v", n)
		}
	}
}

func TestEnsureSourceLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interfaces")
	for _, existing := range []string{"auto lo\niface lo inet loopback\n\nsource /etc/network/interfaces.d/*\n", "source-directory /etc/network/interfaces.d\nauto lo\n"} {
		os.WriteFile(path, []byte(existing), 0644)
		if err := ensureSourceLine(path); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(path); string(b) != existing {
			t.Fatalf("existing source line must be left alone:\n%s", b)
		}
	}
	os.WriteFile(path, []byte("auto lo\niface lo inet loopback"), 0644)
	if err := ensureSourceLine(path); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "auto lo\niface lo inet loopback\n\n# Nex guest networks\nsource /etc/network/interfaces.d/*\n" {
		t.Fatalf("appended source line:\n%s", b)
	}
}

// TestGuestVLANNetns exercises real bridge creation. Run it inside a disposable network
// namespace with sysfs remounted, for example:
//
//	go test -c -o /tmp/vmhost.test ./internal/vmhost
//	unshare -Urnm sh -c 'mount -t sysfs none /sys && NEX_NETNS_TEST=1 /tmp/vmhost.test -test.run TestGuestVLANNetns -test.v'
func TestGuestVLANNetns(t *testing.T) {
	if os.Getenv("NEX_NETNS_TEST") == "" {
		t.Skip("set NEX_NETNS_TEST=1 inside a disposable network namespace")
	}
	ctx := context.Background()
	ip := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v %s", args, err, out)
		}
	}
	ip("link", "add", "d0", "type", "dummy")
	ip("link", "add", "d1", "type", "dummy")
	ip("link", "add", "d2", "type", "dummy")
	ip("addr", "add", "192.0.2.10/24", "dev", "d1")
	ip("link", "add", "other", "type", "bridge")
	ip("link", "add", "link", "d0", "name", "d0.200", "type", "vlan", "id", "200")
	ip("link", "set", "d0.200", "master", "other")
	dir := t.TempDir()
	installer := "# Nex Core\nauto lo\niface lo inet loopback\n\niface d0 inet manual\nauto br0\niface br0 inet dhcp\n    bridge_ports d0\n\niface d2 inet dhcp\n"
	os.WriteFile(filepath.Join(dir, "interfaces"), []byte(installer), 0644)

	n := vlanNet("d0", 100)
	e := &ManagedEngine{stateDir: filepath.Join(dir, "state"), networkDir: dir}
	result, err := e.Execute(ctx, protocol.Command{OperationID: "op-vlan", Kind: "network-register", Network: &n}, nil)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("register VLAN: %v %+v", err, result)
	}
	for _, path := range []string{"/sys/class/net/d0v100/bridge", "/sys/class/net/d0v100/brif/d0.100"} {
		if !exists(path) {
			t.Fatalf("%s missing after registration", path)
		}
	}
	stanza, _ := os.ReadFile(filepath.Join(dir, "interfaces.d", "nex-d0v100"))
	if string(stanza) != interfacesFile(n) {
		t.Fatalf("persisted stanza:\n%s", stanza)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "interfaces")); !strings.Contains(string(b), "source /etc/network/interfaces.d/*") || !strings.HasPrefix(string(b), installer) {
		t.Fatalf("interfaces file must keep installer content and gain the source line:\n%s", b)
	}
	// Applying again is idempotent, and a VM create reverifies the same bridge.
	if err := ensureGuestBridge(ctx, n, dir); err != nil {
		t.Fatal("second apply:", err)
	}
	if err := verifyNetwork(ctx, n); err != nil {
		t.Fatal("verify:", err)
	}
	// Conflicts are refused without touching the existing configuration.
	for name, bad := range map[string]protocol.NetworkRegistration{
		"VLAN owned by host bridge": vlanNet("d0", 200),
		"untagged NIC with address": vlanNet("d1", 0),
		"untagged NIC in ifupdown":  vlanNet("d2", 0),
		"missing uplink":            vlanNet("d9", 10),
		"bridge as uplink":          vlanNet("other", 10),
		"vlan device as uplink":     {Kind: "vlan", Uplink: "d0.200", VLAN: 3, Source: "d0.200v3"},
	} {
		if err := ensureGuestBridge(ctx, bad, dir); err == nil {
			t.Errorf("%s accepted", name)
		} else {
			t.Logf("%s: %v", name, err)
		}
	}
	if master("d0.200") != "other" || exists("/sys/class/net/d1v0") || exists("/sys/class/net/d2v0") {
		t.Fatal("refused registrations must leave host devices alone")
	}
	if info, _ := os.Stat(filepath.Join(dir, "interfaces")); info.Mode().Perm() != 0644 {
		t.Fatalf("interfaces mode changed to %v", info.Mode().Perm())
	}
	// An unused NIC that is up (so it has a link-local IPv6 address) may carry an untagged guest bridge.
	ip("link", "add", "d3", "type", "dummy")
	ip("link", "set", "d3", "up")
	if err := ensureGuestBridge(ctx, vlanNet("d3", 0), dir); err != nil || !exists("/sys/class/net/d3v0/brif/d3") {
		t.Fatal("untagged guest bridge:", err)
	}
	// Removal tears down exactly what registration built, and nothing else.
	ip("link", "add", "d5", "type", "dummy")
	second := vlanNet("d0", 100)
	second.ID, second.Name, second.PoolID, second.HostID = "net-2", "Second", "pool", "host"
	if err := ensureGuestBridge(ctx, vlanNet("d5", 100), dir); err != nil {
		t.Fatal("bystander bridge:", err)
	}
	// A bridge with a port Nex did not attach is refused: it could be a live VM's tap.
	ip("link", "add", "extra", "type", "dummy")
	ip("link", "set", "extra", "master", "d0v100")
	if err := removeGuestBridge(ctx, n, dir); err == nil || !strings.Contains(err.Error(), "still has attached interfaces") {
		t.Fatal("bridge with an extra port accepted:", err)
	}
	ip("link", "set", "extra", "nomaster")
	// A second Nex stanza on the same VLAN device keeps that device alive.
	os.WriteFile(filepath.Join(dir, "interfaces.d", "nex-other"), []byte(interfacesFile(second)), 0644)
	if err := removeGuestBridge(ctx, n, dir); err != nil {
		t.Fatal("remove:", err)
	}
	if exists("/sys/class/net/d0v100") || exists(filepath.Join(dir, "interfaces.d", "nex-d0v100")) {
		t.Fatal("bridge or stanza survived removal")
	}
	if !exists("/sys/class/net/d0.100") {
		t.Fatal("VLAN device named by another Nex stanza must survive")
	}
	os.Remove(filepath.Join(dir, "interfaces.d", "nex-other"))
	// Removing again is a no-op, and now nothing keeps the VLAN device.
	if err := removeGuestBridge(ctx, n, dir); err != nil {
		t.Fatal("second removal:", err)
	}
	if exists("/sys/class/net/d0.100") {
		t.Fatal("unused VLAN device survived")
	}
	// A bridge with no Nex stanza is never touched, even when it carries the name Nex
	// would derive for that uplink and VLAN.
	ip("link", "add", "d0v300", "type", "bridge")
	if err := removeGuestBridge(ctx, vlanNet("d0", 300), dir); err == nil || !strings.Contains(err.Error(), "was not created by Nex") {
		t.Fatal("foreign bridge accepted:", err)
	}
	if !exists("/sys/class/net/d0v300") || !exists("/sys/class/net/other") || master("d0.200") != "other" {
		t.Fatal("refused removal disturbed an operator's bridge")
	}
	// The uplink, the bystander guest bridge and the interfaces file are all untouched.
	if !exists("/sys/class/net/d0") {
		t.Fatal("uplink disturbed")
	}
	if !exists("/sys/class/net/d5v100/brif/d5.100") {
		t.Fatal("another host's guest bridge was torn down")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "interfaces")); !strings.HasPrefix(string(b), installer) || !strings.Contains(string(b), "source /etc/network/interfaces.d/*") {
		t.Fatalf("interfaces file changed by removal:\n%s", b)
	}
	// A bond member is refused; the bond itself must be the uplink.
	if out, err := exec.Command("ip", "link", "add", "bond0", "type", "bond").CombinedOutput(); err != nil {
		t.Logf("bonding unavailable in this namespace, member check not exercised: %s", out)
	} else {
		ip("link", "add", "d4", "type", "dummy")
		ip("link", "set", "d4", "master", "bond0")
		if err := ensureGuestBridge(ctx, vlanNet("d4", 10), dir); err == nil || !strings.Contains(err.Error(), "bond") {
			t.Fatal("bond member accepted:", err)
		}
		if err := ensureGuestBridge(ctx, vlanNet("bond0", 10), dir); err != nil || !exists("/sys/class/net/bond0v10/brif/bond0.10") {
			t.Fatal("bond uplink:", err)
		}
	}
}

func TestAppliedFromJournal(t *testing.T) {
	e := &ManagedEngine{stateDir: t.TempDir(), networkDir: t.TempDir()}
	if got, _ := e.Applied(context.Background()); got == nil || len(got.Networks) != 0 {
		t.Fatalf("empty journal must yield an empty state, got %+v", got)
	}
	n := vlanNet("nex0", 15)
	n.OperationID, n.Status = "op-net", "applied"
	s := protocol.StorageRegistration{ID: "st-1", PoolID: "pool", HostID: "host", Name: "Local", Path: "/srv/vms", OperationID: "op-st", Status: "applied"}
	m := protocol.Media{ID: "iso-1", PoolID: "pool", HostID: "host", StorageID: "st-1", Name: "debian.iso", SizeBytes: 40000, SHA256: strings.Repeat("ab", 32), OperationID: "op-iso", Status: "ready"}
	v := protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", PoolID: "pool", HostID: "host", Name: "guest", MediaID: "iso-1", CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512}, Disks: []protocol.VMDisk{{StorageID: "st-1", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}}, NICs: []protocol.VMNIC{{NetworkID: "net-1", Model: "virtio"}}, Boot: protocol.VMBoot{Firmware: "bios"}}
	for _, j := range []managedJournal{
		{Command: protocol.Command{OperationID: "op-st", Kind: "storage-register", Storage: &s}, Result: protocol.Result{OperationID: "op-st", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "op-net", Kind: "network-register", Network: &n}, Result: protocol.Result{OperationID: "op-net", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "op-iso", Kind: "iso-import", Media: &m}, Result: protocol.Result{OperationID: "op-iso", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "op-vm", Kind: "vm-create", VM: &v}, Result: protocol.Result{OperationID: "op-vm", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "op-failed", Kind: "network-register", Network: &protocol.NetworkRegistration{ID: "net-2", PoolID: "pool", HostID: "host", Kind: "bridge", Source: "br9"}}, Result: protocol.Result{OperationID: "op-failed", Status: "failed"}},
	} {
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := e.Applied(context.Background())
	if len(got.Storage) != 1 || len(got.Networks) != 1 || len(got.Media) != 1 || len(got.VMs) != 1 {
		t.Fatalf("applied state %+v", got)
	}
	if !reflect.DeepEqual(got.Storage[0], protocol.StorageRegistration{ID: "st-1", PoolID: "pool", HostID: "host", Name: "Local", Path: "/srv/vms", Generation: 1}) {
		t.Fatalf("storage %+v", got.Storage[0])
	}
	if !reflect.DeepEqual(got.Networks[0], protocol.NetworkRegistration{ID: "net-1", PoolID: "pool", HostID: "host", Name: "Servers", Kind: "vlan", Source: "nex0v15", Uplink: "nex0", VLAN: 15}) {
		t.Fatalf("network %+v", got.Networks[0])
	}
	if got.Media[0].OperationID != "" || got.Media[0].Status != "" || got.Media[0].ID != "iso-1" || !reflect.DeepEqual(got.VMs[0], v) {
		t.Fatalf("media %+v vm %+v", got.Media[0], got.VMs[0])
	}
}

// The pool convergence precheck and the native apply must refuse the same host conditions
// in the same words: they are one function, and this proves that for every condition it
// covers. A divergence here is the D3 defect returning — a member that refuses locally with
// a sentence the apply would never have produced, or applies something the check allowed.
func TestCheckNetworkApplyAndTheNativeApplyRefuseAlike(t *testing.T) {
	sys, proc := t.TempDir()+"/", t.TempDir()+"/"
	t.Cleanup(NetworkSysfsRootsForTests(sys, proc))
	mk := func(elem ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(elem...), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := func(from, to string) {
		t.Helper()
		if err := os.Symlink(filepath.Join(sys, to), filepath.Join(sys, from, "master")); err != nil {
			t.Fatal(err)
		}
	}
	// Every uplink below is a separate scratch NIC, so one case never sets up the next.
	mk(sys, "nexa")             // a plain NIC: only its VLAN port is in the way
	mk(sys, "nexb", "bridge")   // a bridge offered as an uplink
	mk(sys, "nexc")             // a bonded NIC
	mk(sys, "bond0", "bonding") //
	link("nexc", "bond0")       //
	mk(sys, "nexd")             // its VLAN port belongs to another bridge
	mk(sys, "nexd.100")         //
	mk(sys, "otherbr")          //
	link("nexd.100", "otherbr") //
	mk(sys, "nexe")             // its guest bridge name is taken by a non-bridge
	mk(sys, protocol.GuestBridge("nexe", 100))
	mk(proc, "nexf") // a VLAN device offered as an uplink
	mk(sys, "nexf")  //
	mk(sys, "nexg")  // valid so far: only the registration itself is wrong
	for _, c := range []struct {
		name string
		n    protocol.NetworkRegistration
		want string
	}{
		{"invalid registration", vlanNet("nexg", 4095), "VLAN"},
		{"missing uplink", vlanNet("nexz", 100), "does not exist on this host"},
		{"bridge as uplink", vlanNet("nexb", 100), "must be a physical NIC or bond"},
		{"VLAN device as uplink", vlanNet("nexf", 100), "must be a physical NIC or bond"},
		{"bond member as uplink", vlanNet("nexc", 100), "is a member of bond bond0"},
		{"VLAN port in use", vlanNet("nexd", 100), "already used by otherbr"},
		{"bridge name is not a bridge", vlanNet("nexe", 100), "is not a bridge"},
	} {
		precheck := CheckNetworkApply(c.n)
		apply := ensureGuestBridge(context.Background(), c.n, t.TempDir())
		switch {
		case precheck == nil || apply == nil:
			t.Fatalf("%s: precheck = %v, native apply = %v", c.name, precheck, apply)
		case precheck.Error() != apply.Error():
			t.Fatalf("%s: precheck %q and native apply %q disagree", c.name, precheck, apply)
		case !strings.Contains(precheck.Error(), c.want):
			t.Fatalf("%s: refusal = %q, want it to name %q", c.name, precheck, c.want)
		}
	}
	// The check never approves an apply on its own: a registration it passes is still
	// subject to everything only the native apply can see.
	mk(sys, "nexa.100")
	if err := CheckNetworkApply(vlanNet("nexa", 100)); err != nil {
		t.Fatalf("a registration with nothing in its way was refused: %v", err)
	}
}
