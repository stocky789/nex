// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/netip"
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
	for _, v := range d.Devices.Video {
		if v.Model.Type == "virtio" {
			g.Resize = true
		}
	}
	// The live tray, not the persistent definition: a guest that ejects its own disc
	// disconnects the source here while Nex's durable media_id stays as it was. Reporting
	// the difference is what lets the console offer the recorded ISO again; nothing about
	// this observation changes the VM definition.
	g.Media = protocol.MediaNoDrive
	for _, disk := range d.Devices.Disks {
		if disk.Device != "cdrom" {
			continue
		}
		g.Media = protocol.MediaEmpty
		if disk.Target.Tray != "open" && disk.Source != nil && (disk.Source.File != "" || disk.Source.Dev != "") {
			g.Media = protocol.MediaLoaded
		}
		break
	}
	return g, nil
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
