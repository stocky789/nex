// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
)

// The management network is the one configuration a host can lose itself with, so every edit is
// staged, applied with a deadline and rolled back by the host itself unless the host observes
// its own connectivity over the new files and an operator confirms within the window. Nothing
// here depends on the Controller, on an open browser, or even on this process surviving:
// RecoverManagementNetwork runs unconditionally at agent start, so a host that is cut off and
// power-cycled comes back on its previous configuration.
//
// Nothing here ever tears the management bridge down. Guests on the untagged default network
// have their tap devices enslaved to br0 itself, so an ifdown/ifup of it (which ifupdown turns
// into brctl delbr) would cut their networking until each domain restarted. Apply and rollback
// therefore reconfigure br0 in place and move only its uplink port; see applyManagementBridge.
//
// The render is a Go port of the installer's (installer/gui/nex-network.c:143-199): whole-file
// /etc/network/interfaces, /etc/dhcpcd.conf and /etc/resolv.conf, MAC-bound nexN names and a br0
// management bridge, with the installer's validation rules (nex-network.c:72-112). Guest VLAN
// bridges under /etc/network/interfaces.d are never read, written or brought down here; the
// rendered interfaces file keeps the source line that includes them (vlan.go:85-101).
//
// NIC renaming stays the installer's: /etc/systemd/network/*.link is not rewritten at runtime,
// because a rename takes effect only on boot and could not be undone inside a rollback window.
// The spec therefore names interfaces that already exist on the host.

const (
	managementBridge = "br0"
	// The first line of an interfaces file Nex rendered. The installer writes it byte for byte
	// (nex-network.c:153), so a freshly installed host is recognized without any state file.
	managementHeader = "# Nex Core: interface identity is bound by MAC in /etc/systemd/network."
	dhcpcdHeader     = "# Nex Core: only management DHCP owns routes and DNS."
	resolvHeader     = "# Nex Core DNS"

	// Stages of the durable record. A record is never deleted: the last outcome stays
	// readable. They are reported in inventory, so the strings live in protocol.
	managementCancelled  = protocol.ManagementNetworkCancelled
	managementRolledBack = protocol.ManagementNetworkRolledBack

	// ManagementRollbackRequested is the reason recorded when the operator cancels an applied,
	// unconfirmed change: the same restore as the automatic one, asked for early. It is a
	// recorded reason, not an error, and RequestedRollback on the record says so in a field
	// rather than leaving the two apart only by their wording.
	ManagementRollbackRequested = "rolled back at the operator's request"

	// The operator chooses the window per request; these bound it. Too short cannot be confirmed
	// by hand, too long leaves a host unreachable for most of a working day.
	DefaultManagementWindow = 10 * time.Minute
	MinManagementWindow     = 2 * time.Minute
	MaxManagementWindow     = 30 * time.Minute
)

// ErrManagementNetworkConflict marks every refusal that is a conflict rather than bad input:
// a revision mismatch, an action that does not match the recorded stage, or a confirmation the
// host cannot justify. The command and REST layers map it to 409; its text is the wire-visible
// marker the Controller matches, so it is declared once in protocol.
var ErrManagementNetworkConflict = errors.New(protocol.ManagementNetworkConflict)

// etcRoot is "/" on a host; tests point it at a scratch directory so no test can reach the real
// /etc. Commands (ifdown/ifup) are executed through run, whose PATH tests already replace.
var (
	etcRoot = "/"
	// bootIdentity is how every rollback trigger avoids the wall clock, and bridgeAddrs is how
	// a connectivity observation is checked against the management bridge. Both are variables
	// so a test can inject a wrong RTC, a jumped clock or a DHCP lease; nothing else rewrites
	// them.
	bootIdentity = systemBootIdentity
	bridgeAddrs  = systemBridgeAddrs
	managementMu sync.Mutex
)

// The spec and its inventory projection are wire types: they are declared in protocol (the
// Controller decodes both) and aliased here so this package keeps reading as the owner of the
// rules. Name is the on-host interface name (the installer's nexN), MAC binds it, and exactly
// one NIC carries Management.
type (
	ManagementNIC           = protocol.ManagementNIC
	ManagementNetworkSpec   = protocol.ManagementNetworkSpec
	ManagementNetworkStatus = protocol.ManagementNetworkStatus
)

// ManagementNetworkFiles is the rendered (or snapshotted) content of the three files an edit
// touches. A nil field means the file does not exist: rollback removes it again.
type ManagementNetworkFiles struct {
	Interfaces *string `json:"interfaces,omitempty"`
	DHCPCD     *string `json:"dhcpcd,omitempty"`
	Resolv     *string `json:"resolv,omitempty"`
}

// ManagementNetworkRecord is <state-dir>/management-network.json: the staged change, the files it
// replaces and the rollback deadline. It is the only durable record of the edit, and it is
// written before /etc is touched so a crash can always be resolved from it.
type ManagementNetworkRecord struct {
	OperationID      string                  `json:"operation_id"`
	Revision         uint64                  `json:"revision"`
	Stage            string                  `json:"stage"`
	Spec             ManagementNetworkSpec   `json:"spec"`
	Previous         *ManagementNetworkFiles `json:"previous,omitempty"`
	WindowSeconds    int                     `json:"window_seconds"`
	Deadline         time.Time               `json:"deadline,omitempty"`
	AppliedAt        time.Time               `json:"applied_at,omitempty"`
	ConfirmedAt      time.Time               `json:"confirmed_at,omitempty"`
	ConnectivityAt   time.Time               `json:"connectivity_at,omitempty"`
	ConnectivityKind string                  `json:"connectivity_kind,omitempty"`
	Error            string                  `json:"error,omitempty"`
	// BootID and AppliedUptime are what make the rollback triggers independent of the wall
	// clock, which is the one thing a host cut off by its own new configuration may come back
	// without: BootID is /proc/sys/kernel/random/boot_id at apply, so "this host rebooted" is a
	// string comparison and never a timestamp one, and AppliedUptime is /proc/uptime at the
	// same instant, so "the window ran out" is measured inside a single boot. Deadline stays,
	// but only as the wall-clock value an operator is shown. A record written before these
	// fields existed carries neither and falls back to the clock comparisons it was written
	// with; the reason recorded then names the clock as the only remaining measure.
	BootID        string        `json:"boot_id,omitempty"`
	AppliedUptime time.Duration `json:"applied_uptime_ns,omitempty"`
	// RequestedRollback is set when the rollback was the operator's own cancel of an applied
	// change rather than a deadline, a reboot or a failed apply. It is reported, so a
	// Controller can show a deliberate undo as an outcome instead of a failure.
	RequestedRollback bool      `json:"requested_rollback,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// ---------------------------------------------------------------- validation

var (
	managementNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
)

func ipv4(s string) (uint32, bool) {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() {
		return 0, false
	}
	b := a.As4()
	return binary.BigEndian.Uint32(b[:]), true
}

// managementCIDR applies the installer's address rules (nex-network.c:56-71): IPv4 only, a 1-32
// prefix, no 0.0.0.0, no loopback, no multicast/reserved, and no network or broadcast address
// outside a point-to-point prefix.
func managementCIDR(s string) (ip, mask uint32, ok bool) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() || p.Bits() < 1 {
		return 0, 0, false
	}
	if ip, ok = ipv4(p.Addr().String()); !ok {
		return 0, 0, false
	}
	mask = ^uint32(0) << (32 - p.Bits())
	ok = ip != 0 && ip>>24 != 127 && ip>>28 < 14
	if p.Bits() < 31 && (ip&^mask == 0 || ip&^mask == ^mask) {
		ok = false
	}
	return ip, mask, ok
}

// ValidateManagementNetworkSpec holds every rule the installer enforces, with a message that
// names the offending interface instead of the installer's single combined sentence.
func ValidateManagementNetworkSpec(spec ManagementNetworkSpec) error {
	for _, server := range spec.DNS {
		if _, ok := ipv4(server); !ok {
			return fmt.Errorf("DNS server %q is not an IPv4 address", server)
		}
	}
	names, macs, addresses := map[string]bool{}, map[string]bool{}, map[uint32]bool{}
	management := 0
	for _, nic := range spec.Interfaces {
		switch {
		// managementBridge is the name the render gives the bridge itself, so a NIC called br0
		// would produce two stanzas for one device and an apply that cannot come up.
		case len(nic.Name) > 15 || !managementNamePattern.MatchString(nic.Name) || nic.Name == "lo" || nic.Name == managementBridge:
			return fmt.Errorf("interface name %q is not a usable interface name", nic.Name)
		case names[nic.Name]:
			return fmt.Errorf("interface %s is listed twice", nic.Name)
		}
		hw, err := net.ParseMAC(nic.MAC)
		if err != nil || !strings.EqualFold(nic.MAC, hw.String()) {
			return fmt.Errorf("interface %s has an invalid MAC address", nic.Name)
		}
		// A multicast (odd first octet) or all-zero address never identifies a NIC.
		if hw[0]&1 != 0 || hw.String() == "00:00:00:00:00:00" {
			return fmt.Errorf("interface %s needs a unicast hardware address", nic.Name)
		}
		mac := hw.String()
		if macs[mac] {
			return fmt.Errorf("MAC address %s is used by two interfaces", mac)
		}
		if nic.VLAN < 0 || nic.VLAN > 4094 {
			return fmt.Errorf("interface %s has VLAN %d outside 0-4094", nic.Name, nic.VLAN)
		}
		names[nic.Name], macs[mac] = true, true
		switch nic.Mode {
		case "disabled":
			if nic.Management || nic.Address != "" || nic.Gateway != "" || nic.VLAN != 0 {
				return fmt.Errorf("disabled interface %s must carry no address, gateway, VLAN or management role", nic.Name)
			}
		case "dhcp":
			if nic.Address != "" || nic.Gateway != "" {
				return fmt.Errorf("interface %s uses DHCP, so it must carry no address or gateway", nic.Name)
			}
		case "static":
			ip, mask, ok := managementCIDR(nic.Address)
			if !ok {
				return fmt.Errorf("interface %s needs a usable IPv4 address in CIDR form, for example 192.0.2.10/24", nic.Name)
			}
			if addresses[ip] {
				return fmt.Errorf("address %s is used by two interfaces", nic.Address)
			}
			addresses[ip] = true
			if nic.Gateway != "" {
				gateway, valid := ipv4(nic.Gateway)
				if !nic.Management || !valid || gateway&mask != ip&mask || gateway == ip ||
					(mask != 0xfffffffe && mask != 0xffffffff && (gateway&^mask == 0 || gateway&^mask == ^mask)) {
					return fmt.Errorf("interface %s needs a gateway inside its own subnet, and only the management interface may have one", nic.Name)
				}
			}
		default:
			return fmt.Errorf("interface %s has unknown mode %q", nic.Name, nic.Mode)
		}
		if nic.Management {
			management++
		}
	}
	if management != 1 {
		return fmt.Errorf("exactly one management interface is required, %d given", management)
	}
	return nil
}

func managementNIC(spec ManagementNetworkSpec) ManagementNIC {
	for _, nic := range spec.Interfaces {
		if nic.Management {
			return nic
		}
	}
	return ManagementNIC{}
}

func managementPort(nic ManagementNIC) string {
	if nic.VLAN == 0 {
		return nic.Name
	}
	return fmt.Sprintf("%s.%d", nic.Name, nic.VLAN)
}

// ---------------------------------------------------------------- rendering

// RenderManagementNetwork renders the three files, in the installer's shapes and order
// (nex-network.c:151-199). The management NIC is always bridged into br0 so guests and the host
// share the port; every other enabled NIC configures its own logical interface and, on DHCP,
// gets a dhcpcd stanza that denies it routes and resolv.conf.
func RenderManagementNetwork(spec ManagementNetworkSpec) (ManagementNetworkFiles, error) {
	if err := ValidateManagementNetworkSpec(spec); err != nil {
		return ManagementNetworkFiles{}, err
	}
	dhcpcd := dhcpcdHeader + "\n"
	interfaces := managementHeader + "\n# Nex Core agent adds guest VLAN bridges under interfaces.d.\n" +
		"source /etc/network/interfaces.d/*\n\nauto lo\niface lo inet loopback\n"
	managementDHCP, customDNS := false, len(spec.DNS) > 0
	for _, nic := range spec.Interfaces {
		port := managementPort(nic)
		logical := port
		if nic.Management {
			logical = managementBridge
		}
		interfaces += fmt.Sprintf("\n# %s (%s)\n", nic.Name, nic.MAC)
		if nic.VLAN != 0 || nic.Management {
			interfaces += fmt.Sprintf("iface %s inet manual\n", nic.Name)
		}
		if nic.VLAN != 0 && nic.Management {
			interfaces += fmt.Sprintf("iface %s inet manual\n    vlan-raw-device %s\n", port, nic.Name)
		}
		if nic.Mode == "disabled" {
			interfaces += fmt.Sprintf("iface %s inet manual\n", nic.Name)
			continue
		}
		interfaces += fmt.Sprintf("auto %s\niface %s inet %s\n", logical, logical, nic.Mode)
		if nic.Mode == "static" {
			interfaces += fmt.Sprintf("    address %s\n", nic.Address)
			if nic.Gateway != "" {
				interfaces += fmt.Sprintf("    gateway %s\n", nic.Gateway)
			}
		} else if nic.Management {
			managementDHCP = true
			if customDNS {
				dhcpcd += fmt.Sprintf("\ninterface %s\n    nohook resolv.conf\n", logical)
			}
		} else {
			dhcpcd += fmt.Sprintf("\ninterface %s\n    nogateway\n    nohook resolv.conf\n", logical)
		}
		if nic.Management {
			interfaces += fmt.Sprintf("    bridge_ports %s\n    bridge_stp off\n    bridge_fd 0\n    bridge_hw %s\n", port, nic.MAC)
		}
	}
	files := ManagementNetworkFiles{Interfaces: &interfaces, DHCPCD: &dhcpcd}
	// Management DHCP without an override owns resolv.conf; otherwise Nex writes it.
	if !managementDHCP || customDNS {
		resolv := resolvHeader + "\n"
		for _, server := range spec.DNS {
			resolv += "nameserver " + server + "\n"
		}
		files.Resolv = &resolv
	}
	return files, nil
}

// ---------------------------------------------------------------- durable record

func managementRecordPath(dir string) string { return filepath.Join(dir, "management-network.json") }

func loadManagementRecord(dir string) (*ManagementNetworkRecord, error) {
	b, err := os.ReadFile(managementRecordPath(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rec := &ManagementNetworkRecord{}
	if err := json.Unmarshal(b, rec); err != nil {
		return nil, fmt.Errorf("management network record unreadable: %v", err)
	}
	return rec, nil
}

func saveManagementRecord(dir string, rec *ManagementNetworkRecord) error {
	rec.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(managementRecordPath(dir), append(b, '\n'))
}

// ---------------------------------------------------------------- /etc targets

func managementTargets() (interfaces, dhcpcd, resolv string) {
	return filepath.Join(etcRoot, "etc/network/interfaces"),
		filepath.Join(etcRoot, "etc/dhcpcd.conf"),
		filepath.Join(etcRoot, "etc/resolv.conf")
}

// readManagementTarget reads one target file, refusing anything that is not a plain file: a
// symlink or a device would make an atomic rename either escape /etc or silently do nothing.
// A missing file returns nil, which rollback reproduces by removing the file again.
func readManagementTarget(path string) (*string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (%s); replace it with a plain file before changing the management network", path, info.Mode().Type())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := string(b)
	return &text, nil
}

// snapshotManagementFiles proves ownership before it returns anything: Core overwrites
// /etc/network/interfaces and /etc/dhcpcd.conf only when their first line is the one Nex Core
// or the Nex installer writes, matched exactly, in the manner of ownsInterfacesHeader
// (vlan.go:55-65). /etc/resolv.conf carries no proof because dhcpcd legitimately owns it on a
// DHCP host; it is snapshotted and restored either way.
func snapshotManagementFiles() (*ManagementNetworkFiles, error) {
	interfacesPath, dhcpcdPath, resolvPath := managementTargets()
	files := &ManagementNetworkFiles{}
	for _, target := range []struct {
		path, header string
		into         **string
	}{{interfacesPath, managementHeader, &files.Interfaces}, {dhcpcdPath, dhcpcdHeader, &files.DHCPCD}, {resolvPath, "", &files.Resolv}} {
		content, err := readManagementTarget(target.path)
		if err != nil {
			return nil, err
		}
		if target.header != "" {
			if content == nil {
				return nil, fmt.Errorf("%s is missing; Nex Core will not create a management network configuration it cannot prove it rendered", target.path)
			}
			if first, _, _ := strings.Cut(*content, "\n"); first != target.header {
				return nil, fmt.Errorf("%s was not written by Nex Core or the Nex installer; edit it from the host console instead", target.path)
			}
		}
		*target.into = content
	}
	return files, nil
}

// writeManagementFiles replaces the targets through temp file + rename, so a crash leaves each
// file either wholly old or wholly new. A nil field is a file that must not exist.
func writeManagementFiles(files ManagementNetworkFiles) error {
	interfacesPath, dhcpcdPath, resolvPath := managementTargets()
	for _, target := range []struct {
		path    string
		content *string
	}{{interfacesPath, files.Interfaces}, {dhcpcdPath, files.DHCPCD}, {resolvPath, files.Resolv}} {
		if target.content == nil {
			if err := os.Remove(target.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target.path), 0755); err != nil {
			return err
		}
		if err := AtomicWrite(target.path, []byte(*target.content), 0644); err != nil {
			return fmt.Errorf("write %s: %v", target.path, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- the bridge itself

// managementBridgePorts is /sys/class/net/br0/brif: every device currently enslaved to the
// management bridge. Guests on the untagged default network (managed.go DefaultNetworks
// registers one per bridge on a physical port) have their tap devices here, beside the uplink.
func managementBridgePorts() []string {
	entries, err := os.ReadDir(sysNet + managementBridge + "/brif")
	if err != nil {
		return nil
	}
	ports := make([]string, 0, len(entries))
	for _, e := range entries {
		ports = append(ports, e.Name())
	}
	sort.Strings(ports)
	return ports
}

// guestPortsOnManagementBridge is every port of br0 that is not its management uplink: guest
// taps that would lose their networking if the bridge were torn down and rebuilt.
func guestPortsOnManagementBridge(uplink string) []string {
	var guests []string
	for _, port := range managementBridgePorts() {
		if port != uplink {
			guests = append(guests, port)
		}
	}
	return guests
}

// managementBridgeConfig is what a rendered /etc/network/interfaces says br0 must look like.
// It is parsed out of the file rather than built from a spec because rollback's target is the
// previous file, which no spec describes: one parser gives apply and rollback one code path.
type managementBridgeConfig struct {
	Port    string // the single uplink port named by bridge_ports
	MAC     string // bridge_hw
	Method  string // static | dhcp | manual
	Address string
	Gateway string
}

func parseManagementBridge(interfaces string) managementBridgeConfig {
	cfg, inside := managementBridgeConfig{}, false
	for _, line := range strings.Split(interfaces, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "iface":
			// "iface br0 inet <method>" opens the stanza; any other iface closes it.
			inside = len(fields) >= 4 && fields[1] == managementBridge
			if inside {
				cfg.Method = fields[3]
			}
			continue
		case "auto", "allow-hotplug", "source", "source-directory", "mapping":
			inside = false
			continue
		}
		if !inside || len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "address":
			cfg.Address = fields[1]
		case "gateway":
			cfg.Gateway = fields[1]
		case "bridge_ports":
			cfg.Port = fields[1]
		case "bridge_hw":
			cfg.MAC = fields[1]
		}
	}
	return cfg
}

// attachManagementPort makes port a port of br0, creating the 802.1Q sub-interface first when
// the management NIC carries a VLAN. It adds a port; it never removes one it did not add.
func attachManagementPort(ctx context.Context, port string) error {
	if raw, tag, tagged := strings.Cut(port, "."); tagged {
		if !exists(sysNet + raw) {
			return fmt.Errorf("%s does not exist on this host", raw)
		}
		if _, err := run(ctx, "ip", "link", "set", raw, "up"); err != nil {
			return fmt.Errorf("bring up %s: %v", raw, err)
		}
		if !exists(sysNet + port) {
			if _, err := run(ctx, "ip", "link", "add", "link", raw, "name", port, "type", "vlan", "id", tag); err != nil {
				return fmt.Errorf("create %s: %v", port, err)
			}
		}
	} else if !exists(sysNet + port) {
		return fmt.Errorf("%s does not exist on this host", port)
	}
	if _, err := run(ctx, "ip", "link", "set", port, "up"); err != nil {
		return fmt.Errorf("bring up %s: %v", port, err)
	}
	if _, err := run(ctx, "ip", "link", "set", port, "master", managementBridge); err != nil {
		return fmt.Errorf("enslave %s to %s: %v", port, managementBridge, err)
	}
	return nil
}

// applyManagementBridge brings br0 onto want, coming from have, without ever destroying it.
//
// This is not an ifdown/ifup: ifupdown's bridge hook turns "ifdown br0" into "brctl delbr br0",
// and every guest on the untagged default network has its tap enslaved to br0 itself, so the
// bridge would come back with bridge_ports only and those guests would stay dark until each
// domain restarted. Both apply and rollback go through here, so neither disturbs a running
// guest. What changes instead is the layer 3 configuration in place — hardware address,
// addresses, default route, DHCP client — and the uplink port, only when the management
// interface or its VLAN actually changed, by adding the new port and detaching only the old
// one. Guest ports are never named.
//
// It never runs ifdown, "ip link del", "ip link set br0 down", brctl, or any command that
// removes a port it did not add. The single exception is a host with no br0 at all, where
// there is nothing to protect and ifupdown may build it from the file that was just written.
//
// The rendered files are the same ones the previous implementation wrote, so the next boot
// brings the host up on exactly this configuration through ifupdown as before.
func applyManagementBridge(ctx context.Context, want, have managementBridgeConfig) error {
	if !exists(sysNet + managementBridge) {
		if _, err := run(ctx, "ifup", managementBridge); err != nil {
			return fmt.Errorf("bring up %s: %v", managementBridge, err)
		}
		return nil
	}
	if want.Port != "" && want.Port != have.Port {
		if err := attachManagementPort(ctx, want.Port); err != nil {
			return err
		}
		if have.Port != "" && exists(sysNet+have.Port) && master(have.Port) == managementBridge {
			if _, err := run(ctx, "ip", "link", "set", have.Port, "nomaster"); err != nil {
				return fmt.Errorf("detach %s from %s: %v", have.Port, managementBridge, err)
			}
		}
	}
	if want.MAC != "" && !strings.EqualFold(want.MAC, netAttr(managementBridge, "address")) {
		if _, err := run(ctx, "ip", "link", "set", managementBridge, "address", want.MAC); err != nil {
			return fmt.Errorf("set the hardware address of %s: %v", managementBridge, err)
		}
	}
	if _, err := run(ctx, "ip", "link", "set", managementBridge, "up"); err != nil {
		return fmt.Errorf("bring up %s: %v", managementBridge, err)
	}
	if want.Method == "dhcp" {
		if have.Method != "dhcp" && have.Address != "" {
			// A static address left behind would outlive the lease that replaces it.
			if _, err := run(ctx, "ip", "-4", "addr", "flush", "dev", managementBridge); err != nil {
				return fmt.Errorf("clear the addresses of %s: %v", managementBridge, err)
			}
		}
		// dhcpcd is the host's DHCP client (installer/packages.txt); -n rebinds a running one
		// and starts it otherwise, so it re-reads the /etc/dhcpcd.conf just written.
		if _, err := run(ctx, "dhcpcd", "-n", managementBridge); err != nil {
			return fmt.Errorf("start DHCP on %s: %v", managementBridge, err)
		}
		return nil
	}
	if have.Method == "dhcp" {
		// No lease may re-add an address over the static one that replaces it.
		_, _ = run(ctx, "dhcpcd", "-k", managementBridge)
	}
	if want.Address != have.Address {
		if _, err := run(ctx, "ip", "-4", "addr", "flush", "dev", managementBridge); err != nil {
			return fmt.Errorf("clear the addresses of %s: %v", managementBridge, err)
		}
		if want.Address != "" {
			if _, err := run(ctx, "ip", "-4", "addr", "add", want.Address, "dev", managementBridge); err != nil {
				return fmt.Errorf("set %s on %s: %v", want.Address, managementBridge, err)
			}
		}
	}
	switch {
	case want.Gateway != "":
		if _, err := run(ctx, "ip", "-4", "route", "replace", "default", "via", want.Gateway, "dev", managementBridge); err != nil {
			return fmt.Errorf("set the default route via %s: %v", want.Gateway, err)
		}
	case have.Gateway != "":
		_, _ = run(ctx, "ip", "-4", "route", "del", "default", "dev", managementBridge)
	}
	return nil
}

// currentManagementBridge is what br0 is configured as right now, read from the interfaces file
// on disk rather than from the record: it is correct both after a completed file swap and after
// a crash that never performed one.
func currentManagementBridge() managementBridgeConfig {
	interfaces, _, _ := managementTargets()
	content, err := readManagementTarget(interfaces)
	if err != nil || content == nil {
		return managementBridgeConfig{}
	}
	return parseManagementBridge(*content)
}

// ManagementEtcRootForTests points the three management files at root and returns the restore
// function. It exists for the cross-package end-to-end check, where a command the Controller
// queued is executed by the real agent against this engine and must not reach the real /etc;
// production code never calls it.
func ManagementEtcRootForTests(root string) func() {
	managementMu.Lock()
	defer managementMu.Unlock()
	previous := etcRoot
	etcRoot = root
	return func() {
		managementMu.Lock()
		defer managementMu.Unlock()
		etcRoot = previous
	}
}

// ManagementConnectivityForTests records the observation a confirm depends on, bypassing the
// check that the connection carrying it terminated on the management bridge. It exists for the
// cross-package end-to-end checks, which run entirely over loopback and therefore cannot
// produce a connection that terminated anywhere else; production code never calls it, and what
// the check itself accepts and refuses is covered in this package.
func ManagementConnectivityForTests(dir string) error {
	managementMu.Lock()
	defer managementMu.Unlock()
	rec, err := loadManagementRecord(dir)
	if err != nil || rec == nil || rec.Stage != protocol.ManagementNetworkApplied {
		return err
	}
	rec.ConnectivityAt, rec.ConnectivityKind = time.Now().UTC(), "controller-report"
	return saveManagementRecord(dir, rec)
}

func conflictf(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), ErrManagementNetworkConflict)
}

// ---------------------------------------------------------------- host-side checks

// guestVLANUplinks reads the guest network stanzas the agent wrote under
// /etc/network/interfaces.d — the durable host-side record of a registered guest VLAN — and
// returns, per uplink interface name, the ports those bridges are built on. It is read from
// the host rather than passed in: which guest networks exist is the host's own knowledge, and
// a management edit must not depend on a Controller remembering them.
func guestVLANUplinks() map[string][]string {
	dir := filepath.Join(etcRoot, "etc/network/interfaces.d")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	used := map[string][]string{}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "nex-") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if fields := strings.Fields(line); len(fields) == 2 && fields[0] == "bridge_ports" {
				uplink, _, _ := strings.Cut(fields[1], ".")
				used[uplink] = append(used[uplink], fields[1])
			}
		}
	}
	return used
}

// checkManagementSpecAgainstHost is everything the validator cannot know from the spec alone:
// that the interfaces it names are the ones on this machine, that it does not take a
// registered guest network dark, and that the management uplink can be moved without tearing
// down a bridge guests are attached to. All three are refusals at stage time, where nothing
// has been touched yet, rather than an apply that comes up wrong and self-rolls back.
func checkManagementSpecAgainstHost(spec ManagementNetworkSpec, rendered ManagementNetworkFiles) error {
	for _, nic := range spec.Interfaces {
		switch mac := netAttr(nic.Name, "address"); {
		case mac == "":
			return fmt.Errorf("interface %s does not exist on this host", nic.Name)
		case !strings.EqualFold(mac, nic.MAC):
			return fmt.Errorf("interface %s has hardware address %s on this host, not %s; the spec names a different card", nic.Name, mac, nic.MAC)
		}
	}
	guests := guestVLANUplinks()
	for _, nic := range spec.Interfaces {
		ports := guests[nic.Name]
		if len(ports) == 0 {
			continue
		}
		if nic.Mode == "disabled" {
			return fmt.Errorf("interface %s carries registered guest networks (%s) and cannot be disabled; remove those networks first", nic.Name, strings.Join(ports, ", "))
		}
		if port := managementPort(nic); nic.Management {
			for _, guest := range ports {
				if guest == port {
					return fmt.Errorf("%s is already the port of a registered guest network; the management interface cannot share it", port)
				}
			}
		}
	}
	want, have := parseManagementBridge(deref(rendered.Interfaces)), currentManagementBridge()
	if want.Port == have.Port || !exists(sysNet+managementBridge) {
		return nil
	}
	if m := master(want.Port); m != "" && m != managementBridge {
		return fmt.Errorf("%s is already a port of bridge %s; free it before making it the management uplink", want.Port, m)
	}
	if attached := guestPortsOnManagementBridge(have.Port); len(attached) > 0 && have.Port == "" {
		return fmt.Errorf("%s does not record which port uplinks it, and guests are attached to it (%s); moving the management uplink would mean rebuilding the bridge and cutting them off. Change the uplink from the host console, or move those guests first",
			managementBridge, strings.Join(attached, ", "))
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ---------------------------------------------------------------- state machine

// StageManagementNetwork validates the spec, snapshots the files it would replace and records
// the intent. It writes nothing into /etc, so a staged change that is never applied costs
// nothing and can be cancelled or simply replaced.
func StageManagementNetwork(dir string, spec ManagementNetworkSpec, operationID string, expectedRevision uint64, window time.Duration) (*ManagementNetworkRecord, error) {
	managementMu.Lock()
	defer managementMu.Unlock()
	if operationID == "" {
		return nil, errors.New("an operation ID is required")
	}
	if window == 0 {
		window = DefaultManagementWindow
	}
	if window < MinManagementWindow || window > MaxManagementWindow {
		return nil, fmt.Errorf("rollback window must be between %v and %v", MinManagementWindow, MaxManagementWindow)
	}
	rendered, err := RenderManagementNetwork(spec)
	if err != nil {
		return nil, err
	}
	current, err := loadManagementRecord(dir)
	if err != nil {
		return nil, err
	}
	revision := uint64(0)
	if current != nil {
		revision = current.Revision
		if current.OperationID == operationID && current.Stage == protocol.ManagementNetworkStaged {
			// An exact retry of the same staging replays it. The same id carrying a different
			// spec is not a retry at all: answering "succeeded" would report on the recorded
			// change while the operator is looking at the one they just sent.
			if !reflect.DeepEqual(current.Spec, spec) {
				return nil, conflictf("management network change %s is already staged with a different configuration", operationID)
			}
			return current, nil
		}
		if current.Stage == protocol.ManagementNetworkApplied {
			return nil, conflictf("a management network change is already applied and waiting to be confirmed or rolled back")
		}
	}
	if expectedRevision != 0 && expectedRevision != revision {
		return nil, conflictf("management network revision is %d, not %d", revision, expectedRevision)
	}
	if err = checkManagementSpecAgainstHost(spec, rendered); err != nil {
		return nil, err
	}
	previous, err := snapshotManagementFiles()
	if err != nil {
		return nil, err
	}
	rec := &ManagementNetworkRecord{
		OperationID:   operationID,
		Revision:      revision,
		Stage:         protocol.ManagementNetworkStaged,
		Spec:          spec,
		Previous:      previous,
		WindowSeconds: int(window / time.Second),
	}
	if err := saveManagementRecord(dir, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// ApplyManagementNetwork swaps the files in and bounces the management interface. The record is
// moved to "applied" with its deadline *before* /etc is touched: a crash between the two leaves
// a record that promises a rollback of files that were never changed, which is harmless, while
// the opposite order could leave a host on a new configuration no record knows about.
func ApplyManagementNetwork(ctx context.Context, dir, operationID string) (*ManagementNetworkRecord, error) {
	managementMu.Lock()
	defer managementMu.Unlock()
	rec, err := loadManagementRecord(dir)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.OperationID != operationID {
		return nil, conflictf("no management network change is staged as %s", operationID)
	}
	if rec.Stage == protocol.ManagementNetworkApplied {
		return rec, nil // the same apply, repeated; the deadline is the one already recorded
	}
	if rec.Stage != protocol.ManagementNetworkStaged {
		return nil, conflictf("management network change %s is %s", operationID, rec.Stage)
	}
	files, err := RenderManagementNetwork(rec.Spec)
	if err != nil {
		return nil, err
	}
	// The snapshot is what a rollback restores, so it must still be what is on disk.
	current, err := snapshotManagementFiles()
	if err != nil {
		return nil, err
	}
	if !sameManagementFiles(current, rec.Previous) {
		return nil, conflictf("the host network configuration changed since this edit was staged; stage it again")
	}
	// DHCP may replace resolv.conf through its hook, but a host where that hook cannot do so must
	// keep the resolver configuration it already has. A nil render therefore leaves it alone
	// during apply; the snapshot still restores a genuinely absent file on rollback.
	if files.Resolv == nil {
		files.Resolv = current.Resolv
	}
	now := time.Now().UTC()
	rec.Stage, rec.AppliedAt, rec.Deadline, rec.Error = protocol.ManagementNetworkApplied, now, now.Add(time.Duration(rec.WindowSeconds)*time.Second), ""
	// The clock-free copy of the same deadline. Both are recorded before /etc is touched, so a
	// host that comes back with a wrong RTC still knows whether it rebooted and how much of its
	// window it has spent. Only a boot identity this host can actually read is recorded.
	rec.BootID, rec.AppliedUptime = "", 0
	if id, uptime, ok := bootIdentity(); ok && id != "" {
		rec.BootID, rec.AppliedUptime = id, uptime
	}
	if err := saveManagementRecord(dir, rec); err != nil {
		return nil, err
	}
	have := parseManagementBridge(deref(current.Interfaces))
	if err := writeManagementFiles(files); err != nil {
		return nil, errors.Join(err, rollbackLocked(ctx, dir, rec, "applying the new configuration failed"))
	}
	if err := applyManagementBridge(ctx, parseManagementBridge(*files.Interfaces), have); err != nil {
		return nil, errors.Join(err, rollbackLocked(ctx, dir, rec, "the new configuration would not come up"))
	}
	return rec, nil
}

func sameManagementFiles(a, b *ManagementNetworkFiles) bool {
	if a == nil || b == nil {
		return a == b
	}
	same := func(x, y *string) bool {
		return (x == nil) == (y == nil) && (x == nil || *x == *y)
	}
	return same(a.Interfaces, b.Interfaces) && same(a.DHCPCD, b.DHCPCD) && same(a.Resolv, b.Resolv)
}

// systemBridgeAddrs is every IPv4 address currently configured on the management bridge. It is
// what "the host was reached over the new configuration" means on a DHCP management interface,
// where the address the spec asks for is whatever the lease gave.
func systemBridgeAddrs() []netip.Addr {
	iface, err := net.InterfaceByName(managementBridge)
	if err != nil {
		return nil
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if prefix, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, prefix.Addr().Unmap())
		}
	}
	return out
}

// onManagementBridge is the whole point of a connectivity observation: the connection that
// carried it must have terminated on an address the applied spec puts on the management bridge.
// A static spec names that address; a DHCP one takes whatever br0 currently holds. Anything
// else — a Controller report that went out over a second, unchanged NIC, or a local API call on
// 127.0.0.1 from the host console — proves nothing about the configuration under test.
func onManagementBridge(spec ManagementNetworkSpec, local netip.Addr) bool {
	local = local.Unmap()
	if !local.IsValid() || local.IsLoopback() || local.IsUnspecified() {
		return false
	}
	nic := managementNIC(spec)
	if nic.Mode == "static" {
		prefix, err := netip.ParsePrefix(nic.Address)
		return err == nil && prefix.Addr().Unmap() == local
	}
	for _, addr := range bridgeAddrs() {
		if addr.Unmap() == local {
			return true
		}
	}
	return false
}

// ConnectionAddr is the IP of one end of a connection, from a "host:port" as net.Addr and
// http.Request.RemoteAddr both spell it. Anything that is not an IP socket — a unix socket, an
// empty string — yields an invalid address, which is never evidence of anything.
func ConnectionAddr(hostPort string) netip.Addr {
	ap, err := netip.ParseAddrPort(hostPort)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

// RecordManagementConnectivity records that the host was reached over the network it is being
// tested on: an authenticated Controller report or a host-local API call whose connection
// terminated on the management bridge itself. Confirmation depends on it because an operator
// can otherwise confirm from a path that is not the one under test — a second NIC, the console
// on loopback, or a cached browser tab — and a confirmed change drops the rollback.
//
// local is the local address of the connection that carried the observation (for a report, the
// client socket's; for a served request, the listening socket's), and peer the far end, which
// must not be this machine.
func RecordManagementConnectivity(dir, kind string, at time.Time, local, peer netip.Addr) error {
	managementMu.Lock()
	defer managementMu.Unlock()
	switch kind {
	case "controller-report", "local-api":
	default:
		return fmt.Errorf("unknown connectivity observation %q", kind)
	}
	rec, err := loadManagementRecord(dir)
	if err != nil || rec == nil || rec.Stage != protocol.ManagementNetworkApplied {
		return err
	}
	if at.Before(rec.AppliedAt) || !rec.ConnectivityAt.IsZero() {
		return nil
	}
	if !onManagementBridge(rec.Spec, local) || !peer.IsValid() || peer.Unmap().IsLoopback() {
		return nil
	}
	rec.ConnectivityAt, rec.ConnectivityKind = at.UTC(), kind
	return saveManagementRecord(dir, rec)
}

// ConfirmManagementNetwork keeps an applied change. It is refused until the host has observed
// its own connectivity over the new configuration, and after the deadline, when the change has
// been (or is about to be) rolled back.
func ConfirmManagementNetwork(dir, operationID string) (*ManagementNetworkRecord, error) {
	managementMu.Lock()
	defer managementMu.Unlock()
	rec, err := loadManagementRecord(dir)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.OperationID != operationID {
		return nil, conflictf("no management network change is applied as %s", operationID)
	}
	if rec.Stage == protocol.ManagementNetworkConfirmed {
		return rec, nil
	}
	if rec.Stage != protocol.ManagementNetworkApplied {
		return nil, conflictf("management network change %s is %s", operationID, rec.Stage)
	}
	// The same clock-free measure recovery uses, so a confirm can never be accepted for a
	// change the next recovery pass is about to restore.
	if managementRollbackDue(rec) != "" {
		return nil, conflictf("the rollback deadline for %s has passed; the previous configuration is being restored", operationID)
	}
	if rec.ConnectivityAt.IsZero() {
		return nil, conflictf("this host has not yet been reached over an address of %s itself under the new configuration; a Controller report that arrived over another interface, or a local API call from this machine, does not count", managementBridge)
	}
	rec.Stage, rec.ConfirmedAt, rec.Revision, rec.Previous = protocol.ManagementNetworkConfirmed, time.Now().UTC(), rec.Revision+1, nil
	rec.Deadline = time.Time{}
	if err := saveManagementRecord(dir, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// CancelManagementNetwork undoes a change the operator no longer wants. A staged change is
// discarded, having touched nothing. An applied, unconfirmed change is rolled back at once,
// which is what an operator who can see the new configuration is wrong, and still has a
// connection to say so, needs: strictly safer than waiting the window out, and the same restore
// the deadline would have performed. A confirmed, cancelled or already rolled-back change is a
// conflict: there is nothing left to undo, and saying otherwise would hide a live configuration.
func CancelManagementNetwork(ctx context.Context, dir, operationID string) (*ManagementNetworkRecord, error) {
	managementMu.Lock()
	defer managementMu.Unlock()
	rec, err := loadManagementRecord(dir)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.OperationID != operationID {
		return nil, conflictf("no management network change is staged as %s", operationID)
	}
	switch rec.Stage {
	case managementCancelled:
		return rec, nil
	case protocol.ManagementNetworkStaged:
		rec.Stage, rec.Previous = managementCancelled, nil
	case protocol.ManagementNetworkApplied:
		// The restore itself may cut this host off the Controller, so nothing here depends on
		// reporting the result: rollbackLocked is re-runnable and leaves the record "applied"
		// until the previous configuration is actually back up, so a repeated cancel resumes it.
		rec.RequestedRollback = true
		return rec, rollbackLocked(ctx, dir, rec, ManagementRollbackRequested)
	default:
		return nil, conflictf("management network change %s is %s and cannot be cancelled", operationID, rec.Stage)
	}
	if err := saveManagementRecord(dir, rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// RollbackManagementNetwork restores the snapshot and brings the management interface back on
// it. It is safe to run repeatedly and at any time: restoring files that are already correct
// changes nothing, and the record only leaves "applied" once the interface is actually back up,
// so a rollback interrupted half way is retried by the next call instead of being forgotten.
func RollbackManagementNetwork(ctx context.Context, dir, reason string) (*ManagementNetworkRecord, error) {
	managementMu.Lock()
	defer managementMu.Unlock()
	rec, err := loadManagementRecord(dir)
	if err != nil {
		return nil, err
	}
	if rec == nil || rec.Stage != protocol.ManagementNetworkApplied {
		return rec, nil
	}
	return rec, rollbackLocked(ctx, dir, rec, reason)
}

func rollbackLocked(ctx context.Context, dir string, rec *ManagementNetworkRecord, reason string) error {
	if rec.Previous == nil {
		return errors.New("no previous management network configuration was recorded")
	}
	// What the bridge is on right now, read from disk: correct both when the apply completed
	// its file swap and when it crashed before one, where there is nothing to undo.
	have := currentManagementBridge()
	if err := writeManagementFiles(*rec.Previous); err != nil {
		rec.Error = reason
		return errors.Join(err, saveManagementRecord(dir, rec))
	}
	if err := applyManagementBridge(ctx, parseManagementBridge(deref(rec.Previous.Interfaces)), have); err != nil {
		// The files are already back; only the interface reconfiguration is outstanding, and
		// the record stays "applied" so the next recovery pass retries exactly this step.
		rec.Error = reason
		return errors.Join(err, saveManagementRecord(dir, rec))
	}
	rec.Stage, rec.Error, rec.Deadline = managementRolledBack, reason, time.Time{}
	return saveManagementRecord(dir, rec)
}

// RecoverManagementNetwork is called unconditionally when the agent starts, and again on every
// probe tick, which is the in-process deadline timer. It rolls back an applied, unconfirmed
// change when the deadline has passed, and also when the host has restarted since the apply:
// a reboot is not evidence that the new configuration works, the operator's window was spent
// while the host was down, and the previous configuration is the one known to work. Within the
// window, with no reboot, it leaves the change alone so the operator can still confirm it.
func RecoverManagementNetwork(ctx context.Context, dir string) (*ManagementNetworkRecord, error) {
	managementMu.Lock()
	defer managementMu.Unlock()
	rec, err := loadManagementRecord(dir)
	if err != nil || rec == nil || rec.Stage != protocol.ManagementNetworkApplied {
		return rec, err
	}
	if reason := managementRollbackDue(rec); reason != "" {
		return rec, rollbackLocked(ctx, dir, rec, reason)
	}
	return rec, nil
}

const (
	managementDeadlineReason = "the change was not confirmed before the rollback deadline"
	managementRebootReason   = "the host restarted before the change was confirmed"
)

// managementRollbackDue decides whether an applied change must be restored, without trusting
// the wall clock wherever it can avoid it. That matters because the host this feature exists
// for is one its own new configuration cut off from NTP: it reboots with whatever its RTC says,
// often behind the apply, and a deadline compared against time.Now() would then never pass.
//
//   - "this host rebooted" is a boot-id comparison, never a timestamp one. A different
//     /proc/sys/kernel/random/boot_id means a restart whatever the clock reads.
//   - "the window ran out" is measured inside one boot, as elapsed /proc/uptime since the
//     apply, so a clock that jumps backwards or forwards mid-window changes nothing. Suspend
//     and resume do count against the window, because /proc/uptime counts suspended time; a
//     host that was asleep for the whole window comes back rolled back, which is the safe
//     direction — nobody confirmed it.
//   - rec.Deadline is what an operator is shown, and is the fallback only for a record written
//     before boot identity was recorded, or on a kernel that will not report one.
func managementRollbackDue(rec *ManagementNetworkRecord) string {
	id, uptime, ok := bootIdentity()
	if rec.BootID != "" && id != "" {
		if id != rec.BootID {
			return managementRebootReason
		}
		if ok {
			if uptime-rec.AppliedUptime >= time.Duration(rec.WindowSeconds)*time.Second {
				return managementDeadlineReason
			}
			return ""
		}
		// Same boot, but no elapsed time to measure it with: the clock below is all that is left.
	}
	if ok && id == "" && !rec.AppliedAt.IsZero() && time.Now().Add(-uptime).After(rec.AppliedAt) {
		return managementRebootReason
	}
	if !rec.Deadline.IsZero() && time.Now().After(rec.Deadline) {
		return managementDeadlineReason
	}
	return ""
}

// managementNICComment matches the section headers both the installer and the renderer write:
// "# nex0 (52:54:00:12:34:56)" or "# enp1s0 (52:54:00:12:34:56) -> nex0".
var managementNICComment = regexp.MustCompile(`(?i)^#\s+(\S+)\s+\(([0-9a-f:]+)\)(?:\s*->\s*(\S+))?\s*$`)

// ParseManagementNetworkSpec recovers a ManagementNetworkSpec from Nex-rendered interfaces and
// resolv.conf content. interfaces must begin with managementHeader; otherwise ok is false and
// the files are treated as foreign. A nil resolv yields an empty DNS list.
func ParseManagementNetworkSpec(interfaces string, resolv *string) (ManagementNetworkSpec, bool) {
	if !strings.HasPrefix(interfaces, managementHeader) {
		return ManagementNetworkSpec{}, false
	}
	bridge := parseManagementBridge(interfaces)
	if bridge.Port == "" || (bridge.Method != "static" && bridge.Method != "dhcp") {
		return ManagementNetworkSpec{}, false
	}
	mgmtName, vlan := bridge.Port, 0
	if raw, tag, tagged := strings.Cut(bridge.Port, "."); tagged {
		mgmtName, vlan = raw, 0
		if n, err := fmt.Sscanf(tag, "%d", &vlan); n != 1 || err != nil || vlan < 1 || vlan > 4094 {
			return ManagementNetworkSpec{}, false
		}
	}
	type pending struct {
		name, mac string
		body      string
	}
	var sections []pending
	var cur *pending
	for _, line := range strings.Split(interfaces, "\n") {
		if m := managementNICComment.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			name := m[1]
			if m[3] != "" {
				name = m[3]
			}
			hw, err := net.ParseMAC(m[2])
			if err != nil {
				continue
			}
			sections = append(sections, pending{name: name, mac: hw.String()})
			cur = &sections[len(sections)-1]
			continue
		}
		if cur != nil {
			cur.body += line + "\n"
		}
	}
	byName := map[string]ManagementNIC{}
	for _, sec := range sections {
		nic := ManagementNIC{Name: sec.name, MAC: sec.mac, Mode: "disabled"}
		if sec.name == mgmtName {
			nic.Mode, nic.Management, nic.VLAN = bridge.Method, true, vlan
			if bridge.Method == "static" {
				nic.Address, nic.Gateway = bridge.Address, bridge.Gateway
			}
			if nic.MAC == "" && bridge.MAC != "" {
				if hw, err := net.ParseMAC(bridge.MAC); err == nil {
					nic.MAC = hw.String()
				}
			}
			byName[nic.Name] = nic
			continue
		}
		mode := secondaryIfaceMode(sec.body, sec.name)
		nic.Mode = mode
		if mode == "static" {
			nic.Address, nic.Gateway = secondaryIfaceAddress(sec.body, sec.name)
			if nic.Gateway != "" {
				// Only the management interface may carry a gateway.
				nic.Gateway = ""
			}
		}
		byName[nic.Name] = nic
	}
	if _, ok := byName[mgmtName]; !ok {
		mac := bridge.MAC
		if hw, err := net.ParseMAC(mac); err == nil {
			mac = hw.String()
		}
		byName[mgmtName] = ManagementNIC{Name: mgmtName, MAC: mac, Mode: bridge.Method, VLAN: vlan, Address: bridge.Address, Gateway: bridge.Gateway, Management: true}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	spec := ManagementNetworkSpec{Interfaces: make([]ManagementNIC, 0, len(names))}
	for _, name := range names {
		spec.Interfaces = append(spec.Interfaces, byName[name])
	}
	if resolv != nil {
		for _, line := range strings.Split(*resolv, "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "nameserver" {
				if _, ok := ipv4(fields[1]); ok {
					spec.DNS = append(spec.DNS, fields[1])
				}
			}
		}
	}
	if err := ValidateManagementNetworkSpec(spec); err != nil {
		return ManagementNetworkSpec{}, false
	}
	return spec, true
}

func secondaryIfaceMode(body, name string) string {
	mode, auto := "disabled", false
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "auto", "allow-hotplug":
			if len(fields) >= 2 && fields[1] == name {
				auto = true
			}
		case "iface":
			if len(fields) >= 4 && fields[1] == name {
				switch fields[3] {
				case "dhcp", "static":
					mode = fields[3]
				case "manual":
					mode = "disabled"
				}
			}
		}
	}
	if mode != "disabled" && !auto {
		// A static/dhcp stanza without auto is not brought up; treat as disabled.
		return "disabled"
	}
	return mode
}

func secondaryIfaceAddress(body, name string) (address, gateway string) {
	inside := false
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "iface":
			inside = len(fields) >= 4 && fields[1] == name
			continue
		case "auto", "allow-hotplug", "source", "source-directory", "mapping":
			inside = false
			continue
		}
		if !inside || len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "address":
			address = fields[1]
		case "gateway":
			gateway = fields[1]
		}
	}
	return address, gateway
}

// currentManagementNetworkSpec is the on-disk configuration parsed for inventory. It returns
// ok=false when the files are foreign or incomplete; callers then omit Current rather than
// inventing one.
func currentManagementNetworkSpec() (ManagementNetworkSpec, bool) {
	interfacesPath, _, resolvPath := managementTargets()
	interfaces, err := readManagementTarget(interfacesPath)
	if err != nil || interfaces == nil {
		return ManagementNetworkSpec{}, false
	}
	resolv, err := readManagementTarget(resolvPath)
	if err != nil {
		resolv = nil
	}
	return ParseManagementNetworkSpec(*interfaces, resolv)
}

// ReadManagementNetworkStatus is the inventory projection: what this host reports about its own
// management network, including how long an unconfirmed change still has and — when the
// on-disk files are Nex-managed — the live configuration an operator sees before staging.
func ReadManagementNetworkStatus(dir string) (*ManagementNetworkStatus, error) {
	managementMu.Lock()
	defer managementMu.Unlock()
	rec, err := loadManagementRecord(dir)
	if err != nil {
		return nil, err
	}
	current, hasCurrent := currentManagementNetworkSpec()
	if rec == nil {
		if !hasCurrent {
			return nil, nil
		}
		return &ManagementNetworkStatus{
			Revision: 0, Stage: protocol.ManagementNetworkNone, Current: &current, ObservedAt: time.Now().UTC(),
		}, nil
	}
	status := &ManagementNetworkStatus{
		Revision: rec.Revision, Stage: rec.Stage, OperationID: rec.OperationID, WindowSeconds: rec.WindowSeconds,
		Spec: &rec.Spec, Deadline: rec.Deadline, LastError: rec.Error, RequestedRollback: rec.RequestedRollback,
		ObservedAt: time.Now().UTC(),
	}
	if hasCurrent {
		status.Current = &current
	}
	if rec.Stage == protocol.ManagementNetworkApplied {
		// What is left of the window, measured the way the rollback measures it, so the
		// countdown an operator watches cannot disagree with the trigger.
		remaining := time.Until(rec.Deadline)
		if id, uptime, ok := bootIdentity(); ok && id != "" && id == rec.BootID {
			remaining = time.Duration(rec.WindowSeconds)*time.Second - (uptime - rec.AppliedUptime)
		}
		if remaining > 0 {
			status.SecondsRemaining = int(remaining / time.Second)
		}
	}
	return status, nil
}

// readKernelBootID reads and validates the kernel boot identity shared by management-network
// rollback and pool VM start admission.
func readKernelBootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", errors.New("local boot identity is unavailable")
	}
	id := strings.TrimSpace(string(b))
	if !uuidPattern.MatchString(id) {
		return "", errors.New("local boot identity is unavailable")
	}
	return id, nil
}

// systemBootIdentity reads the kernel's boot identity, which is what lets every rollback
// trigger avoid the wall clock. /proc/sys/kernel/random/boot_id is regenerated on each boot and
// is only ever compared for equality. /proc/uptime measures elapsed time inside this boot; it
// counts time spent suspended, so a suspended host spends its rollback window. An unreadable
// boot id yields "", an unreadable uptime yields ok=false, and each falls back to the recorded
// wall-clock deadline, which is the only measure left.
func systemBootIdentity() (id string, uptime time.Duration, ok bool) {
	if bootID, err := readKernelBootID(); err == nil {
		id = bootID
	}
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return id, 0, false
	}
	field, _, _ := strings.Cut(string(b), " ")
	seconds, err := time.ParseDuration(field + "s")
	if err != nil {
		return id, 0, false
	}
	return id, seconds, true
}
