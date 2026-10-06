// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestObserveGuest(t *testing.T) {
	const channelOnly = `<domain><devices><channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='disconnected'/></channel></devices></domain>`
	const full = `<domain><devices>
	<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='connected'/></channel>
	<channel type='qemu-vdagent'><source><clipboard copypaste='yes'/></source><target type='virtio' name='com.redhat.spice.0' state='connected'/></channel>
	<video><model type='virtio'/></video></devices></domain>`
	const loaded = `<domain><devices>
	<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='connected'/></channel>
	<channel type='qemu-vdagent'><source><clipboard copypaste='yes'/></source><target type='virtio' name='com.redhat.spice.0' state='connected'/></channel>
	<disk device='disk' type='file'><source file='/var/lib/nex-storage/guest.qcow2'/><target dev='vda' bus='virtio'/></disk>
	<disk device='cdrom' type='file'><source file='/var/lib/libvirt/images/debian.iso'/><target dev='sda' bus='sata'/><readonly/></disk>
	<video><model type='virtio'/></video></devices></domain>`
	// A VirtIO display with nothing in the guest to act on a resize request: the channel a
	// vdagent would open is absent from the definition entirely (BUG-0201).
	const noVdagent = `<domain><devices>
	<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='connected'/></channel>
	<video><model type='virtio'/></video></devices></domain>`
	// The guest runs its vdagent, but the display QEMU gave it cannot be resized.
	const qxlDisplay = `<domain><devices>
	<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='connected'/></channel>
	<channel type='qemu-vdagent'><source><clipboard copypaste='yes'/></source><target type='virtio' name='com.redhat.spice.0' state='connected'/></channel>
	<video><model type='qxl'/></video></devices></domain>`
	// Both named drives, drivers listed first, so only the aliases can tell them apart.
	const drives = `<domain><devices>
	<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='connected'/></channel>
	<channel type='qemu-vdagent'><source><clipboard copypaste='yes'/></source><target type='virtio' name='com.redhat.spice.0' state='connected'/></channel>
	<disk device='cdrom' type='file'><target dev='sdb' bus='sata'/><readonly/><alias name='ua-nex-cdrom-drivers'/></disk>
	<disk device='cdrom' type='file'><source file='/var/lib/libvirt/images/windows.iso'/><target dev='sda' bus='sata'/><readonly/><alias name='ua-nex-cdrom-install'/></disk>
	<video><model type='virtio'/></video></devices></domain>`
	fakeCommand(t, "virsh", `case "$3" in
dumpxml) printf '%s\n' "$GUEST_XML";;
qemu-agent-command) [ "$4" = --timeout ] || exit 3; [ -n "$GUEST_FAIL" ] && exit 1; printf '%s\n' "$5 $7" >> "$GUEST_LOG"
  case "$7" in
  *osinfo*) echo '{"return":{"id":"debian","pretty-name":"Debian GNU/Linux 13 (trixie)","name":"Debian GNU/Linux","version":"13"}}';;
  *host-name*) printf '{"return":{"host-name":"guest1%s"}}\n' "$GUEST_PAD";;
  *interfaces*) echo '{"return":[{"name":"lo","ip-addresses":[{"ip-address":"127.0.0.1","prefix":8}]},{"name":"enp1s0","ip-addresses":[{"ip-address":"192.168.122.10","prefix":24},{"ip-address":"fe80::1","prefix":64},{"ip-address":"2001:db8::10","prefix":64},{"ip-address":"bad","prefix":1}]}]}';;
  esac;;
*) exit 2;; esac`)
	log := t.TempDir() + "/log"
	t.Setenv("GUEST_LOG", log)
	id := "12345678-1234-4234-8234-123456789012"
	cases := []struct {
		name, xml, fail string
		facts           bool
		want            *protocol.GuestInfo
	}{
		{"agent channel only", channelOnly, "", true, &protocol.GuestInfo{Agent: "disconnected", Clipboard: "no-channel", Media: "no-drive"}},
		// BUG-0201: the VirtIO display is there, but nothing in the guest opened the vdagent
		// channel, so nothing would act on a resize request. Availability must say so.
		{"channels waiting", strings.ReplaceAll(full, "state='connected'", "state='disconnected'"), "", true, &protocol.GuestInfo{Agent: "disconnected", Clipboard: "waiting", Media: "no-drive"}},
		// The vdagent port can read connected in firmware, GRUB or with no OS at all, while
		// the guest-agent channel on the same bus shows nothing in the guest is running.
		{"vdagent port connected without a guest OS", strings.Replace(full, "state='connected'", "state='disconnected'", 1), "", true, &protocol.GuestInfo{Agent: "disconnected", Clipboard: "waiting", Media: "no-drive"}},
		{"VirtIO display without a vdagent channel", noVdagent, "", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "no-channel", Media: "no-drive"}},
		{"vdagent ready without a VirtIO display", qxlDisplay, "", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Media: "no-drive"}},
		{"attached without facts", full, "", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "no-drive"}},
		{"agent not answering", full, "1", true, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "no-drive"}},
		{"agent facts", full, "", true, &protocol.GuestInfo{Agent: "connected", OS: "Debian GNU/Linux 13 (trixie)", OSID: "debian", Hostname: "guest1", Clipboard: "ready", Resize: true, Media: "no-drive",
			Addresses: []protocol.GuestAddress{{Interface: "enp1s0", Address: "192.168.122.10/24"}, {Interface: "enp1s0", Address: "2001:db8::10/64"}}}},
		// The live tray of a running VM, which a guest can open itself: the durable media_id
		// stays as Nex recorded it, so only this observation can show the disc is gone.
		{"disc loaded", loaded, "", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "loaded"}},
		{"guest ejected the disc", strings.ReplaceAll(loaded, `<source file='/var/lib/libvirt/images/debian.iso'/>`, ""), "", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "empty"}},
		{"tray opened with media still named", strings.ReplaceAll(loaded, `<target dev='sda' bus='sata'/>`, `<target dev='sda' bus='sata' tray='open'/>`), "", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "empty"}},
		// A domain with neither Nex alias and exactly one cdrom; libvirt still echoes back
		// the alias it assigned itself. That drive is the install drive, and the VM has no
		// drivers drive to report.
		{"single drive named only by libvirt", strings.ReplaceAll(loaded, `<target dev='sda' bus='sata'/><readonly/>`, `<target dev='sda' bus='sata'/><readonly/><alias name='sata0-0-0'/>`),
			"", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "loaded"}},
		{"two named drives, drivers empty", drives, "", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "loaded", DriversMedia: "empty"}},
		{"two named drives, both loaded", strings.ReplaceAll(drives, `<disk device='cdrom' type='file'><target dev='sdb' bus='sata'/>`, `<disk device='cdrom' type='file'><source file='/var/lib/libvirt/images/virtio-win.iso'/><target dev='sdb' bus='sata'/>`),
			"", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "loaded", DriversMedia: "loaded"}},
		{"guest ejected the driver disc", strings.ReplaceAll(drives, `<target dev='sdb' bus='sata'/>`, `<target dev='sdb' bus='sata' tray='open'/>`),
			"", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "loaded", DriversMedia: "empty"}},
		{"guest ejected the install disc", strings.ReplaceAll(drives, `<source file='/var/lib/libvirt/images/windows.iso'/>`, ""),
			"", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true, Media: "empty", DriversMedia: "empty"}},
	}
	for _, c := range cases {
		t.Setenv("GUEST_XML", c.xml)
		t.Setenv("GUEST_FAIL", c.fail)
		got, err := observeGuest(context.Background(), managedVirsh, id)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.facts {
			guestFacts(context.Background(), managedVirsh, id, got)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
	if _, err := observeGuest(context.Background(), managedVirsh, "../etc"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	t.Setenv("GUEST_XML", "<broken")
	if _, err := observeGuest(context.Background(), managedVirsh, id); err == nil {
		t.Fatal("invalid XML accepted")
	}
	// Guest strings are bounded before they leave the host.
	t.Setenv("GUEST_XML", full)
	t.Setenv("GUEST_PAD", strings.Repeat("x", 300))
	g, _ := observeGuest(context.Background(), managedVirsh, id)
	guestFacts(context.Background(), managedVirsh, id, g)
	if g.Agent != "connected" || len(g.Hostname) != guestHostnameLimit {
		t.Fatalf("hostname not bounded: %d", len(g.Hostname))
	}
}

// BUG-0211: Windows 11 kept the registry product name Windows 10 wrote, and that name is what
// the guest agent answers with, so only the build number tells a Windows 11 guest from a
// Windows 10 one. Everything here is untrusted guest input: a build that is absent, not a
// number or out of range leaves what the guest said alone.
func TestGuestOSInfoIDsAndWindows11Name(t *testing.T) {
	fakeCommand(t, "virsh", `case "$3" in
dumpxml) printf '%s\n' "$GUEST_XML";;
qemu-agent-command) case "$7" in
  *osinfo*) printf '%s\n' "$GUEST_OSINFO";;
  *) exit 1;; esac;;
*) exit 2;; esac`)
	t.Setenv("GUEST_XML", `<domain><devices><channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='connected'/></channel></devices></domain>`)
	id := "12345678-1234-4234-8234-123456789012"
	cases := []struct{ name, osinfo, wantOS, wantID string }{
		{"Windows 11 reported as Windows 10", `{"return":{"id":"mswindows","name":"Microsoft Windows","pretty-name":"Windows 10 IoT Enterprise LTSC 2024","kernel-release":"26100","kernel-version":"10.0"}}`,
			"Windows 11 IoT Enterprise LTSC 2024", "mswindows"},
		{"first Windows 11 build", `{"return":{"id":"mswindows","pretty-name":"Windows 10 Pro","kernel-release":"22000"}}`, "Windows 11 Pro", "mswindows"},
		{"genuine Windows 10", `{"return":{"id":"mswindows","pretty-name":"Windows 10 Pro","kernel-release":"19045"}}`, "Windows 10 Pro", "mswindows"},
		{"build missing", `{"return":{"id":"mswindows","pretty-name":"Windows 10 Pro"}}`, "Windows 10 Pro", "mswindows"},
		{"build not a number", `{"return":{"id":"mswindows","pretty-name":"Windows 10 Pro","kernel-release":"26100 or so"}}`, "Windows 10 Pro", "mswindows"},
		{"build out of range", `{"return":{"id":"mswindows","pretty-name":"Windows 10 Pro","kernel-release":"999999999999999999999"}}`, "Windows 10 Pro", "mswindows"},
		{"Windows Server untouched", `{"return":{"id":"mswindows","pretty-name":"Windows Server 2022","kernel-release":"20348"}}`, "Windows Server 2022", "mswindows"},
		{"Debian", `{"return":{"id":"debian","pretty-name":"Debian GNU/Linux 13 (trixie)"}}`, "Debian GNU/Linux 13 (trixie)", "debian"},
		{"Ubuntu", `{"return":{"id":"ubuntu","pretty-name":"Ubuntu 24.04 LTS"}}`, "Ubuntu 24.04 LTS", "ubuntu"},
		{"Fedora", `{"return":{"id":"fedora","pretty-name":"Fedora Linux 43"}}`, "Fedora Linux 43", "fedora"},
		{"Alpine", `{"return":{"id":"alpine","pretty-name":"Alpine Linux v3.22"}}`, "Alpine Linux v3.22", "alpine"},
		{"Arch", `{"return":{"id":"arch","pretty-name":"Arch Linux"}}`, "Arch Linux", "arch"},
		{"Rocky", `{"return":{"id":"rocky","pretty-name":"Rocky Linux 10"}}`, "Rocky Linux 10", "rocky"},
		{"Alma", `{"return":{"id":"almalinux","pretty-name":"AlmaLinux 10"}}`, "AlmaLinux 10", "almalinux"},
		{"SUSE", `{"return":{"id":"opensuse-tumbleweed","pretty-name":"openSUSE Tumbleweed"}}`, "openSUSE Tumbleweed", "opensuse-tumbleweed"},
		{"Nix", `{"return":{"id":"nixos","pretty-name":"NixOS 25.05"}}`, "NixOS 25.05", "nixos"},
		{"Mint", `{"return":{"id":"linuxmint","pretty-name":"Linux Mint 22.2"}}`, "Linux Mint 22.2", "linuxmint"},
		{"Kali", `{"return":{"id":"kali","pretty-name":"Kali GNU/Linux Rolling"}}`, "Kali GNU/Linux Rolling", "kali"},
		{"missing ID", `{"return":{"pretty-name":"Custom Linux"}}`, "Custom Linux", ""},
		{"Linux guest untouched", `{"return":{"id":"debian","pretty-name":"Debian GNU/Linux 13 (trixie)","kernel-release":"6.12.43"}}`, "Debian GNU/Linux 13 (trixie)", "debian"},
		// A guest that does not even claim to be Windows cannot borrow the correction.
		{"non-Windows family claiming a Windows name", `{"return":{"id":"debian","pretty-name":"Windows 10 Pro","kernel-release":"26100"}}`, "Windows 10 Pro", "debian"},
		// The correction rewrites one literal in a string the host already bounded; it never
		// copies anything else the guest sent.
		{"hostile product name is reported, not executed", `{"return":{"id":"mswindows","pretty-name":"Windows 10 <script>alert(1)</script>","kernel-release":"26100"}}`,
			"Windows 11 <script>alert(1)</script>", "mswindows"},
	}
	for _, c := range cases {
		t.Setenv("GUEST_OSINFO", c.osinfo)
		g, err := observeGuest(context.Background(), managedVirsh, id)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		guestFacts(context.Background(), managedVirsh, id, g)
		if g.Agent != "connected" || g.OS != c.wantOS || g.OSID != c.wantID {
			t.Errorf("%s: agent %q OS %q ID %q want %q / %q", c.name, g.Agent, g.OS, g.OSID, c.wantOS, c.wantID)
		}
	}
	// A product name longer than the limit stays bounded through the correction.
	t.Setenv("GUEST_OSINFO", `{"return":{"id":"mswindows","pretty-name":"Windows 10 `+strings.Repeat("x", guestOSLimit)+`","kernel-release":"26100"}}`)
	g, err := observeGuest(context.Background(), managedVirsh, id)
	if err != nil {
		t.Fatal(err)
	}
	guestFacts(context.Background(), managedVirsh, id, g)
	if len(g.OS) != guestOSLimit || !strings.HasPrefix(g.OS, "Windows 11 ") {
		t.Fatalf("OS not bounded after correction: %d %q", len(g.OS), g.OS[:min(len(g.OS), 20)])
	}
}
