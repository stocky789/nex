// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/netip"
	"strconv"
	"strings"

	"nex.local/nex/internal/protocol"
)

// Guest agent answers are bounded (seconds) so a hung guest cannot stall host observation;
// a healthy agent answers within milliseconds.
const guestAgentTimeout = "1"

const guestAddressLimit = 32

type guestDomainXML struct {
	Devices struct {
		Channels []struct {
			Type   string `xml:"type,attr"`
			Target struct {
				Name  string `xml:"name,attr"`
				State string `xml:"state,attr"`
			} `xml:"target"`
		} `xml:"channel"`
		Video []struct {
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
		} `xml:"video"`
		Disks []struct {
			Device string `xml:"device,attr"`
			Source *struct {
				File string `xml:"file,attr"`
				Dev  string `xml:"dev,attr"`
			} `xml:"source"`
			Target struct {
				Tray string `xml:"tray,attr"`
			} `xml:"target"`
			Alias struct {
				Name string `xml:"name,attr"`
			} `xml:"alias"`
		} `xml:"disk"`
	} `xml:"devices"`
}

// Guest-reported strings are bounded before they leave the host; the Controller refuses
// longer values, so a hostile guest cannot bloat reports.
const (
	guestOSLimit       = 4096
	guestHostnameLimit = 200
	guestNameLimit     = 200
)

func bounded(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) > limit {
		return s[:limit]
	}
	return s
}

// observeGuest reads the live definition of a running VM for its console channels and
// display. A guest that has not opened its agent channel is "disconnected"; one that has
// is "unresponsive" until guestFacts hears from it.
func observeGuest(ctx context.Context, virsh func(context.Context, ...string) ([]byte, error), id string) (*protocol.GuestInfo, error) {
	if !uuidPattern.MatchString(id) {
		return nil, errors.New("invalid VM ID")
	}
	b, err := virsh(ctx, "dumpxml", id)
	if err != nil {
		return nil, errors.New("cannot read the running VM definition")
	}
	var d guestDomainXML
	if err = xml.Unmarshal(b, &d); err != nil {
		return nil, errors.New("invalid VM definition")
	}
	g := &protocol.GuestInfo{Agent: "no-channel", Clipboard: "no-channel"}
	for _, ch := range d.Devices.Channels {
		attached := ch.Target.State == "connected"
		switch {
		case ch.Target.Name == "org.qemu.guest_agent.0":
			g.Agent = "disconnected"
			if attached {
				g.Agent = "unresponsive"
			}
		case ch.Type == "qemu-vdagent":
			g.Clipboard = "waiting"
			if attached {
				g.Clipboard = "ready"
			}
		}
	}
	virtioDisplay := false
	for _, v := range d.Devices.Video {
		if v.Model.Type == "virtio" {
			virtioDisplay = true
		}
	}
	// A VirtIO display is necessary but not sufficient: it only means QEMU would accept a
	// resize request, and a guest with no driver for it silently ignores one. Reporting the
	// device model as the answer offered "Resize guest to window" on a Windows guest that
	// never changed resolution until its VirtIO guest tools were installed (BUG-0201).
	//
	// The one guest-side signal this host has is the qemu-vdagent channel, already observed
	// above: the guest opens it only when it is running the vdagent that applies a console's
	// display request. That is the component that performs the resolution change, and it
	// arrives with the same guest tools as the VirtIO display driver (virtio-win on Windows,
	// spice-vdagent beside the virtio-gpu driver on Linux), so an open channel is evidence
	// that the display side is installed too.
	//
	// The test is deliberately conservative. A guest that could resize without an open
	// vdagent channel is reported as unable, which costs an option the operator can still
	// live without; the reverse offered a control that did nothing, which is the bug.
	g.Resize = virtioDisplay && g.Clipboard == "ready"
	// The live tray, not the persistent definition: a guest that ejects its own disc
	// disconnects the source here while Nex's durable media_id stays as it was. Reporting
	// the difference is what lets the console offer the recorded ISO again; nothing about
	// this observation changes the VM definition.
	var install, drivers, unnamed string
	for _, disk := range d.Devices.Disks {
		if disk.Device != "cdrom" {
			continue
		}
		state := protocol.MediaEmpty
		if disk.Target.Tray != "open" && disk.Source != nil && (disk.Source.File != "" || disk.Source.Dev != "") {
			state = protocol.MediaLoaded
		}
		switch disk.Alias.Name {
		case cdromInstallAlias:
			if install == "" {
				install = state
			}
		case cdromDriversAlias:
			if drivers == "" {
				drivers = state
			}
		default:
			// libvirt names every device it generates itself, so a cdrom carrying neither
			// Nex alias belongs to a domain an older Core defined. Those have exactly one,
			// and it is always the install drive.
			if unnamed == "" {
				unnamed = state
			}
		}
	}
	if install == "" {
		install = unnamed
	}
	g.Media, g.DriversMedia = protocol.MediaNoDrive, drivers
	if install != "" {
		g.Media = install
	}
	return g, nil
}

// windowsBuild11 is the first Windows 11 build. Windows 11 kept the registry product name
// Windows 10 wrote, and that name is what the QEMU guest agent reports, so an honest Windows
// 11 guest calls itself "Windows 10 ..." and only its build number tells the two apart.
const windowsBuild11 = 22000

// correctWindowsName repairs that one name. Reporting it verbatim showed a Windows 11 guest
// as "Windows 10 IoT Enterprise LTSC 2024" (BUG-0211).
//
// Everything here is guest-reported and untrusted, so the correction cannot become an
// injection point: the only edit is swapping the literal "Windows 10" for "Windows 11" in a
// string the caller already bounded, which is the same length, and nothing the guest sent is
// copied in. A build number that is absent, not a number, or out of range leaves the guest's
// own words alone: reporting what the guest said beats guessing what it meant.
func correctWindowsName(name, family, build string) string {
	if family != "mswindows" || !strings.Contains(name, "Windows 10") {
		return name
	}
	n, err := strconv.Atoi(strings.TrimSpace(build))
	if err != nil || n < windowsBuild11 {
		return name
	}
	return strings.Replace(name, "Windows 10", "Windows 11", 1)
}

// guestFacts asks an attached guest agent for OS, hostname and addresses, filling g.
// A guest that does not answer stays "unresponsive"; it is reported, not guessed.
func guestFacts(ctx context.Context, virsh func(context.Context, ...string) ([]byte, error), id string, g *protocol.GuestInfo) {
	if g.Agent != "unresponsive" || !uuidPattern.MatchString(id) {
		return
	}
	agent := func(command string, out any) bool {
		data, err := virsh(ctx, "qemu-agent-command", "--timeout", guestAgentTimeout, id, `{"execute":"`+command+`"}`)
		return err == nil && json.Unmarshal(data, out) == nil
	}
	var osinfo struct {
		Return struct {
			PrettyName string `json:"pretty-name"`
			ID         string `json:"name"`
			Ver        string `json:"version"`
			// Family is the agent's own OS family ("mswindows"), and Build is the guest
			// kernel/build number: on Windows, the build that tells 11 apart from 10.
			Family string `json:"id"`
			Build  string `json:"kernel-release"`
		} `json:"return"`
	}
	if !agent("guest-get-osinfo", &osinfo) {
		return
	}
	g.Agent = "connected"
	g.OS = bounded(osinfo.Return.PrettyName, guestOSLimit)
	if g.OS == "" {
		g.OS = bounded(osinfo.Return.ID+" "+osinfo.Return.Ver, guestOSLimit)
	}
	g.OS = correctWindowsName(g.OS, osinfo.Return.Family, osinfo.Return.Build)
	var host struct {
		Return struct {
			Name string `json:"host-name"`
		} `json:"return"`
	}
	if agent("guest-get-host-name", &host) {
		g.Hostname = bounded(host.Return.Name, guestHostnameLimit)
	}
	var nics struct {
		Return []struct {
			Name      string `json:"name"`
			Addresses []struct {
				Address string `json:"ip-address"`
				Prefix  int    `json:"prefix"`
			} `json:"ip-addresses"`
		} `json:"return"`
	}
	if agent("guest-network-get-interfaces", &nics) {
		for _, nic := range nics.Return {
			for _, ip := range nic.Addresses {
				a, err := netip.ParseAddr(ip.Address)
				if err != nil || a.IsLoopback() || a.IsLinkLocalUnicast() || ip.Prefix < 0 || ip.Prefix > a.BitLen() || len(g.Addresses) >= guestAddressLimit {
					continue
				}
				g.Addresses = append(g.Addresses, protocol.GuestAddress{Interface: bounded(nic.Name, guestNameLimit), Address: netip.PrefixFrom(a, ip.Prefix).String()})
			}
		}
	}
}

// ObserveGuest reads one running VM's console channels and display on system libvirt.
func ObserveGuest(ctx context.Context, id string) (*protocol.GuestInfo, error) {
	return observeGuest(ctx, managedVirsh, id)
}

// GuestFacts asks the VM's attached guest agent on system libvirt; see guestFacts.
func GuestFacts(ctx context.Context, id string, g *protocol.GuestInfo) {
	guestFacts(ctx, managedVirsh, id, g)
}
