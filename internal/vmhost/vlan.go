// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"nex.local/nex/internal/protocol"
)

// Guest VLAN networks are additive: one Linux bridge per (uplink, VLAN) on top of an 802.1Q
// sub-interface, exactly like a management bridge on a tagged port. The management bridge,
// its untagged path, addresses and routes are never touched, so applying one cannot cut
// the host off. removeGuestBridge is the inverse, and touches only a bridge Nex provably
// created: /etc/network/interfaces itself, its source line and the uplink stay untouched.
// sysNet and procVLAN are the kernel views every guest-network check reads. They are
// variables so a test can point them at scratch directories; nothing else rewrites them.
var sysNet, procVLAN = "/sys/class/net/", "/proc/net/vlan/"

// NetworkSysfsRootsForTests points the guest-network checks at scratch kernel views and
// returns the restore function, as ManagementEtcRootForTests does for /etc.
func NetworkSysfsRootsForTests(sysfs, vlanProc string) func() {
	previousSys, previousVLAN := sysNet, procVLAN
	sysNet, procVLAN = sysfs, vlanProc
	return func() { sysNet, procVLAN = previousSys, previousVLAN }
}

func guestPort(n protocol.NetworkRegistration) string {
	if n.VLAN == 0 {
		return n.Uplink
	}
	return fmt.Sprintf("%s.%d", n.Uplink, n.VLAN)
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func master(dev string) string {
	target, err := os.Readlink(sysNet + dev + "/master")
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// netAttr reads one sysfs attribute of a network device, "" when the device or the attribute
// is absent.
func netAttr(dev, name string) string {
	b, err := os.ReadFile(sysNet + dev + "/" + name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// interfacesOwnership is the part of the stanza header that names which network Nex wrote the
// file for. It survives a rename, which changes only the quoted name before it.
func interfacesOwnership(n protocol.NetworkRegistration) string {
	return fmt.Sprintf(" (%s): VLAN %d on %s. Managed by Nex Core", n.ID, n.VLAN, n.Uplink)
}

const interfacesHeader, interfacesTrailer = "# Nex guest network ", "; do not edit."

// ownsInterfacesHeader is the ownership proof for the one destructive network operation Core
// has, so it matches the header exactly rather than looking for a substring: the operator's
// name is rendered before the ownership text and a name that embeds another network's
// ownership string would otherwise pass that network's proof. The name is the header's only
// free text and is quoted, so stripping the quoted run leaves a tail that must equal this
// network's ID, VLAN and uplink byte for byte.
func ownsInterfacesHeader(header string, n protocol.NetworkRegistration) bool {
	rest, ok := strings.CutPrefix(header, interfacesHeader)
	if !ok {
		return false
	}
	quoted, err := strconv.QuotedPrefix(rest)
	if err != nil {
		return false
	}
	return rest[len(quoted):] == interfacesOwnership(n)+interfacesTrailer
}

// interfacesFile renders the ifupdown stanza that recreates the bridge at boot. The VLAN
// device is created explicitly so no bridge-utils VLAN convention is relied upon.
func interfacesFile(n protocol.NetworkRegistration) string {
	port := guestPort(n)
	s := fmt.Sprintf("%s%q%s%s\nauto %s\niface %s inet manual\n", interfacesHeader, n.Name, interfacesOwnership(n), interfacesTrailer, n.Source, n.Source)
	if n.VLAN != 0 {
		s += fmt.Sprintf("    pre-up ip link show %s >/dev/null 2>&1 || ip link add link %s name %s type vlan id %d\n", port, n.Uplink, port, n.VLAN)
	}
	s += fmt.Sprintf("    pre-up ip link set %s up\n    bridge_ports %s\n    bridge_stp off\n    bridge_fd 0\n", n.Uplink, port)
	return s
}

// hostOwnsUplink reports whether ifupdown configures addresses on the NIC itself, which an
// untagged guest bridge would break by enslaving it.
func hostOwnsUplink(interfaces, uplink string) bool {
	return regexp.MustCompile(`(?m)^\s*iface\s+` + regexp.QuoteMeta(uplink) + `\s+inet6?\s+(dhcp|static)`).MatchString(interfaces)
}

func ensureSourceLine(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if regexp.MustCompile(`(?m)^source(-directory)?\s+/etc/network/interfaces\.d(/\*)?\s*$`).Match(b) {
		return nil
	}
	text := string(b)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	if err := atomicWrite(path, []byte(text+"\n# Nex guest networks\nsource /etc/network/interfaces.d/*\n")); err != nil {
		return err
	}
	return os.Chmod(path, 0644)
}

// CheckNetworkApply reports every refusal a guest-network apply raises that can be
// evaluated without touching anything, so a pool member that cannot apply yet can refuse
// locally, journal nothing and retry the moment the operator fixes the cause.
// ensureGuestBridge calls it first, so the precheck and the native apply cannot drift apart
// — the same conditions, in the same words. It only ever refuses: the conditions it cannot
// see from here (an uplink the host itself is addressed on, unreadable host network
// configuration) are still refused by ensureGuestBridge alone.
func CheckNetworkApply(n protocol.NetworkRegistration) error {
	if err := protocol.ValidateVLAN(n); err != nil {
		return err
	}
	port, bridge := guestPort(n), n.Source
	if !exists(sysNet + n.Uplink) {
		return fmt.Errorf("uplink %s does not exist on this host", n.Uplink)
	}
	if exists(sysNet+n.Uplink+"/bridge") || exists(procVLAN+n.Uplink) || n.Uplink == "lo" {
		return fmt.Errorf("uplink %s must be a physical NIC or bond, not a bridge, VLAN device or loopback", n.Uplink)
	}
	if m := master(n.Uplink); m != "" && exists(sysNet+m+"/bonding") {
		return fmt.Errorf("uplink %s is a member of bond %s; use the bond as the uplink", n.Uplink, m)
	}
	if n.VLAN != 0 && exists(sysNet+port) {
		if m := master(port); m != "" && m != bridge {
			return fmt.Errorf("VLAN %d on %s is already used by %s in the host configuration", n.VLAN, n.Uplink, m)
		}
	}
	if exists(sysNet+bridge) && !exists(sysNet+bridge+"/bridge") {
		return fmt.Errorf("%s exists but is not a bridge", bridge)
	}
	return nil
}

// ensureGuestBridge creates (idempotently) and persists the bridge for a VLAN registration.
// networkDir is /etc/network on a host; tests supply a scratch directory.
func ensureGuestBridge(ctx context.Context, n protocol.NetworkRegistration, networkDir string) error {
	if err := CheckNetworkApply(n); err != nil {
		return err
	}
	port, bridge := guestPort(n), n.Source
	interfaces, err := os.ReadFile(filepath.Join(networkDir, "interfaces"))
	if err != nil {
		return fmt.Errorf("host network configuration unreadable: %v", err)
	}
	if n.VLAN == 0 {
		if m := master(n.Uplink); m != "" && m != bridge {
			return fmt.Errorf("uplink %s already belongs to %s; choose a VLAN ID or register that bridge", n.Uplink, m)
		}
		iface, err := net.InterfaceByName(n.Uplink)
		if err != nil {
			return err
		}
		addressed := hostOwnsUplink(string(interfaces), n.Uplink)
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			// Automatic fe80:: addresses do not mean the host uses the NIC.
			if ip, ok := a.(*net.IPNet); !ok || !ip.IP.IsLinkLocalUnicast() {
				addressed = true
			}
		}
		if addressed {
			return fmt.Errorf("uplink %s carries host addresses; an untagged guest bridge would disconnect it, choose a VLAN ID", n.Uplink)
		}
	} else if !exists(sysNet + port) {
		if _, err := run(ctx, "ip", "link", "add", "link", n.Uplink, "name", port, "type", "vlan", "id", fmt.Sprint(n.VLAN)); err != nil {
			return fmt.Errorf("create VLAN device %s: %v", port, err)
		}
	}
	// A bridge that exists but is not a bridge was refused by CheckNetworkApply above.
	if !exists(sysNet + bridge) {
		if _, err := run(ctx, "ip", "link", "add", bridge, "type", "bridge", "stp_state", "0", "forward_delay", "0"); err != nil {
			return fmt.Errorf("create bridge %s: %v", bridge, err)
		}
	}
	if master(port) != bridge {
		if _, err := run(ctx, "ip", "link", "set", port, "master", bridge); err != nil {
			return fmt.Errorf("attach %s to %s: %v", port, bridge, err)
		}
	}
	for _, dev := range []string{n.Uplink, port, bridge} {
		if _, err := run(ctx, "ip", "link", "set", dev, "up"); err != nil {
			return fmt.Errorf("bring up %s: %v", dev, err)
		}
	}
	if err := writeGuestBridgeFile(n, networkDir); err != nil {
		return err
	}
	if err := ensureSourceLine(filepath.Join(networkDir, "interfaces")); err != nil {
		return fmt.Errorf("enable interfaces.d: %v", err)
	}
	return verifyGuestBridge(n)
}

// writeGuestBridgeFile persists the boot-time stanza. A rename calls it alone: the stanza's
// only variable part is the name in its header comment, so no link is disturbed.
func writeGuestBridgeFile(n protocol.NetworkRegistration, networkDir string) error {
	if err := protocol.ValidateVLAN(n); err != nil {
		return err
	}
	path := filepath.Join(networkDir, "interfaces.d", "nex-"+n.Source)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := atomicWrite(path, []byte(interfacesFile(n))); err != nil {
		return fmt.Errorf("persist bridge configuration: %v", err)
	}
	return os.Chmod(path, 0644)
}

// checkGuestBridgeRemovable evaluates every refusal removeGuestBridge would raise without
// touching anything, so pool convergence can skip a teardown it knows will be refused instead
// of journalling the same failure on every probe tick. removeGuestBridge calls it first, so
// the two cannot drift apart.
func checkGuestBridgeRemovable(n protocol.NetworkRegistration, networkDir string) error {
	if err := protocol.ValidateVLAN(n); err != nil {
		return err
	}
	port, bridge := guestPort(n), n.Source
	file := filepath.Join(networkDir, "interfaces.d", "nex-"+bridge)
	notOurs := fmt.Errorf("bridge %s was not created by Nex; remove it by hand", bridge)
	switch b, err := os.ReadFile(file); {
	case err == nil:
		header, _, _ := strings.Cut(string(b), "\n")
		if !ownsInterfacesHeader(header, n) {
			return notOurs
		}
	case errors.Is(err, os.ErrNotExist):
		// No proof: only a bridge that is already gone may be treated as removed.
		if exists(sysNet + bridge) {
			return notOurs
		}
	default:
		return err
	}
	if !exists(sysNet + bridge) {
		return nil
	}
	if !exists(sysNet + bridge + "/bridge") {
		return fmt.Errorf("%s exists but is not a bridge", bridge)
	}
	ports, err := os.ReadDir(sysNet + bridge + "/brif")
	if err != nil {
		return err
	}
	for _, p := range ports {
		if p.Name() != port {
			return errors.New("network still has attached interfaces")
		}
	}
	return nil
}

// removeGuestBridge tears down a guest VLAN bridge. It is the only destructive network
// operation Core performs, so it acts only on a bridge Nex provably created: the
// interfaces.d file it wrote must be present and name this network. It is idempotent —
// an absent bridge and an absent file are a completed removal — so an interrupted
// removal settles by re-running. The uplink, the management bridge, addresses, routes and
// /etc/network/interfaces (including its source line) are never named, let alone changed.
func removeGuestBridge(ctx context.Context, n protocol.NetworkRegistration, networkDir string) error {
	if err := checkGuestBridgeRemovable(n, networkDir); err != nil {
		return err
	}
	port, bridge := guestPort(n), n.Source
	dir := filepath.Join(networkDir, "interfaces.d")
	file := filepath.Join(dir, "nex-"+bridge)
	if exists(sysNet + bridge) {
		ports, err := os.ReadDir(sysNet + bridge + "/brif")
		if err != nil {
			return err
		}
		for _, p := range ports {
			// A live VM tap or a port an operator attached; tearing the bridge down
			// would cut it off. checkGuestBridgeRemovable read the same directory a
			// moment ago under the same engine lock; this re-read is the one that counts.
			if p.Name() != port {
				return errors.New("network still has attached interfaces")
			}
		}
		if _, err = run(ctx, "ip", "link", "set", bridge, "down"); err != nil {
			return fmt.Errorf("bring down bridge %s: %v", bridge, err)
		}
		if _, err = run(ctx, "ip", "link", "del", bridge); err != nil {
			return fmt.Errorf("delete bridge %s: %v", bridge, err)
		}
	}
	// The 802.1Q sub-interface is Nex's only when Nex created it: it must be a VLAN device,
	// belong to no other bridge now that ours is gone, and be named by no other Nex stanza.
	if n.VLAN != 0 && exists(sysNet+port) && exists("/proc/net/vlan/"+port) && master(port) == "" {
		used, err := vlanPortInUse(dir, "nex-"+bridge, port)
		if err != nil {
			return err
		}
		if !used {
			if _, err = run(ctx, "ip", "link", "del", port); err != nil {
				return fmt.Errorf("delete VLAN device %s: %v", port, err)
			}
		}
	}
	if err := os.Remove(file); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove bridge configuration: %v", err)
	}
	return nil
}

// vlanPortInUse reports whether a Nex stanza other than except attaches port to a bridge.
func vlanPortInUse(dir, except, port string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == except || !strings.HasPrefix(entry.Name(), "nex-") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return false, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if strings.TrimSpace(line) == "bridge_ports "+port {
				return true, nil
			}
		}
	}
	return false, nil
}

// verifyGuestBridge proves the bridge exists with its port attached; it says nothing about
// whether the switch carries the VLAN.
func verifyGuestBridge(n protocol.NetworkRegistration) error {
	if !exists(sysNet + n.Source + "/brif/" + guestPort(n)) {
		return fmt.Errorf("bridge %s with port %s is not present; re-register the network", n.Source, guestPort(n))
	}
	return nil
}
