// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// vncSocket returns the running domain's local VNC socket from its live definition.
// Nex defines guests with a UNIX-socket VNC listener only; a domain exposing a
// network display or no display is refused rather than proxied.
func vncSocket(ctx context.Context, virsh func(context.Context, ...string) ([]byte, error), id string) (string, error) {
	if !uuidPattern.MatchString(id) {
		return "", errors.New("invalid VM ID")
	}
	state, err := virsh(ctx, "domstate", id)
	if err != nil {
		return "", errors.New("cannot establish VM execution state")
	}
	if observed := strings.TrimSpace(string(state)); observed == domainAsleep {
		return "", errors.New("VM is asleep; use Start to wake it before opening its console")
	} else if observed != "running" {
		return "", errors.New("VM is not running; start it before opening its console")
	}
	b, err := virsh(ctx, "dumpxml", id)
	if err != nil {
		return "", errors.New("cannot read the running VM definition")
	}
	var d domainXML
	if err = xml.Unmarshal(b, &d); err != nil || len(d.Devices.Graphics) != 1 {
		return "", errors.New("VM has no single console display")
	}
	g := d.Devices.Graphics[0]
	path := g.Listen.Socket
	if path == "" {
		path = g.Socket
	}
	if g.Type != "vnc" || g.Listen.Type != "socket" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("VM display is not a local VNC socket")
	}
	if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSocket == 0 {
		return "", errors.New("VM console socket is unavailable")
	}
	return path, nil
}

// ConsoleSocket locates the local VNC socket of a running VM on this host's libvirt.
func (e *Engine) ConsoleSocket(ctx context.Context, id string) (string, error) {
	return vncSocket(ctx, e.virsh, id)
}

// ConsoleSocket locates the local VNC socket of a running managed VM on system libvirt.
func ConsoleSocket(ctx context.Context, id string) (string, error) {
	return vncSocket(ctx, managedVirsh, id)
}
