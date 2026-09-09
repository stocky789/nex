// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"nex.local/nex/internal/protocol"
)

// Guest VLAN networks are additive: one Linux bridge per (uplink, VLAN) on top of an 802.1Q
// sub-interface, exactly like a management bridge on a tagged port. The management bridge,
// its untagged path, addresses and routes are never touched, so applying one cannot cut
// the host off. Removal is not implemented; the bridge persists until an operator deletes
// its /etc/network/interfaces.d file.
const sysNet = "/sys/class/net/"

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

// interfacesFile renders the ifupdown stanza that recreates the bridge at boot. The VLAN
// device is created explicitly so no bridge-utils VLAN convention is relied upon.
func interfacesFile(n protocol.NetworkRegistration) string {
	port := guestPort(n)
	s := fmt.Sprintf("# Nex guest network %q (%s): VLAN %d on %s. Managed by Nex Core; do not edit.\nauto %s\niface %s inet manual\n", n.Name, n.ID, n.VLAN, n.Uplink, n.Source, n.Source)
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

// ensureGuestBridge creates (idempotently) and persists the bridge for a VLAN registration.
// networkDir is /etc/network on a host; tests supply a scratch directory.
func ensureGuestBridge(ctx context.Context, n protocol.NetworkRegistration, networkDir string) error {
	if err := protocol.ValidateVLAN(n); err != nil {
		return err
	}
	port, bridge := guestPort(n), n.Source
	if !exists(sysNet + n.Uplink) {
		return fmt.Errorf("uplink %s does not exist on this host", n.Uplink)
	}
	if exists(sysNet+n.Uplink+"/bridge") || exists("/proc/net/vlan/"+n.Uplink) || n.Uplink == "lo" {
		return fmt.Errorf("uplink %s must be a physical NIC or bond, not a bridge, VLAN device or loopback", n.Uplink)
	}
	interfaces, err := os.ReadFile(filepath.Join(networkDir, "interfaces"))
	if err != nil {
		return fmt.Errorf("host network configuration unreadable: %v", err)
	}
	if m := master(n.Uplink); m != "" && exists(sysNet+m+"/bonding") {
		return fmt.Errorf("uplink %s is a member of bond %s; use the bond as the uplink", n.Uplink, m)
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
	} else if exists(sysNet + port) {
		if m := master(port); m != "" && m != bridge {
			return fmt.Errorf("VLAN %d on %s is already used by %s in the host configuration", n.VLAN, n.Uplink, m)
		}
	} else if _, err := run(ctx, "ip", "link", "add", "link", n.Uplink, "name", port, "type", "vlan", "id", fmt.Sprint(n.VLAN)); err != nil {
		return fmt.Errorf("create VLAN device %s: %v", port, err)
	}
	if !exists(sysNet + bridge) {
		if _, err := run(ctx, "ip", "link", "add", bridge, "type", "bridge", "stp_state", "0", "forward_delay", "0"); err != nil {
			return fmt.Errorf("create bridge %s: %v", bridge, err)
		}
	} else if !exists(sysNet + bridge + "/bridge") {
		return fmt.Errorf("%s exists but is not a bridge", bridge)
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
	dir := filepath.Join(networkDir, "interfaces.d")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(dir, "nex-"+bridge), []byte(interfacesFile(n))); err != nil {
		return fmt.Errorf("persist bridge configuration: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "nex-"+bridge), 0644); err != nil {
		return err
	}
	if err := ensureSourceLine(filepath.Join(networkDir, "interfaces")); err != nil {
		return fmt.Errorf("enable interfaces.d: %v", err)
	}
	return verifyGuestBridge(n)
}

// verifyGuestBridge proves the bridge exists with its port attached; it says nothing about
// whether the switch carries the VLAN.
func verifyGuestBridge(n protocol.NetworkRegistration) error {
	if !exists(sysNet + n.Source + "/brif/" + guestPort(n)) {
		return fmt.Errorf("bridge %s with port %s is not present; re-register the network", n.Source, guestPort(n))
	}
	return nil
}
