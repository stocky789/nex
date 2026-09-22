// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"nex.local/nex/internal/protocol"
)

// zpoolStatusOutput is the part of `zpool status --json` (OpenZFS 2.3) Nex reports.
// Counts and sizes are formatted strings there, so they are carried through verbatim.
type zpoolStatusOutput struct {
	Pools map[string]struct {
		Name      string `json:"name"`
		State     string `json:"state"`
		Status    string `json:"status"`
		Action    string `json:"action"`
		ScanStats *struct {
			Function  string `json:"function"`
			State     string `json:"state"`
			StartTime string `json:"start_time"`
			EndTime   string `json:"end_time"`
			ToExamine string `json:"to_examine"`
			Examined  string `json:"examined"`
			Issued    string `json:"issued"`
			Errors    string `json:"errors"`
		} `json:"scan_stats"`
		VDevs map[string]zpoolVDev `json:"vdevs"`
	} `json:"pools"`
}

type zpoolVDev struct {
	Name           string               `json:"name"`
	Path           string               `json:"path"`
	State          string               `json:"state"`
	VDevType       string               `json:"vdev_type"`
	ReadErrors     string               `json:"read_errors"`
	WriteErrors    string               `json:"write_errors"`
	ChecksumErrors string               `json:"checksum_errors"`
	VDevs          map[string]zpoolVDev `json:"vdevs"`
}

// Class containers sit beside the root in the pool's vdevs map; they are not data vdevs.
var zfsVDevClassContainers = map[string]bool{
	"spares": true, "logs": true, "l2cache": true, "dedup": true, "special": true,
}

// zfsPoolStatus is observation only: it never imports a pool or changes a property, and a
// pool whose status cannot be read reports no detail rather than failing the repository.
func zfsPoolStatus(ctx context.Context, pool string) *protocol.ZFSPoolStatus {
	out, err := nativeRun(ctx, "zpool", "status", "--json", pool)
	if err != nil {
		return nil
	}
	return decodeZFSPoolStatus(out, pool)
}

func decodeZFSPoolStatus(out []byte, pool string) *protocol.ZFSPoolStatus {
	var raw zpoolStatusOutput
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	p, ok := raw.Pools[pool]
	if !ok {
		return nil
	}
	status := &protocol.ZFSPoolStatus{Name: p.Name, State: p.State, Status: zfsSentence(p.Status), Action: zfsSentence(p.Action)}
	if s := p.ScanStats; s != nil {
		status.Scan = &protocol.ZFSPoolScan{Function: s.Function, State: s.State, StartTime: s.StartTime, EndTime: s.EndTime, ToExamine: s.ToExamine, Examined: s.Examined, Issued: s.Issued, Errors: s.Errors}
	}
	status.Devices = zfsPoolRootLeaves(p.VDevs, pool)
	return status
}

// zfsPoolRootLeaves walks only the pool's root vdev, ignoring class containers (spares,
// logs, l2cache, …) that OpenZFS places beside the root in the same map.
func zfsPoolRootLeaves(vdevs map[string]zpoolVDev, pool string) []protocol.ZFSPoolDevice {
	if root, ok := vdevs[pool]; ok {
		return zfsPoolLeaves(root.VDevs, "", nil)
	}
	for _, name := range slices.Sorted(maps.Keys(vdevs)) {
		if zfsVDevClassContainers[name] {
			continue
		}
		v := vdevs[name]
		if v.VDevType == "root" {
			return zfsPoolLeaves(v.VDevs, "", nil)
		}
	}
	out := []protocol.ZFSPoolDevice{}
	for _, name := range slices.Sorted(maps.Keys(vdevs)) {
		if zfsVDevClassContainers[name] {
			continue
		}
		out = zfsPoolLeaves(map[string]zpoolVDev{name: vdevs[name]}, "", out)
	}
	return out
}

// zfsPoolLeaves flattens the vdev tree to the devices an operator can replace. ZFS
// reports the tree as JSON objects, so the children are ordered by name for a stable report.
// Leaves under a replacing or spare vdev keep that parent type so a second replace is refused.
func zfsPoolLeaves(vdevs map[string]zpoolVDev, parent string, out []protocol.ZFSPoolDevice) []protocol.ZFSPoolDevice {
	for _, name := range slices.Sorted(maps.Keys(vdevs)) {
		v := vdevs[name]
		marker := parent
		if t := zfsReplaceParentType(v); t != "" {
			marker = t
		}
		if len(v.VDevs) > 0 {
			out = zfsPoolLeaves(v.VDevs, marker, out)
			continue
		}
		if v.Name == "" || v.State == "" {
			continue
		}
		out = append(out, protocol.ZFSPoolDevice{
			Name: v.Name, Path: v.Path, State: v.State, Parent: marker,
			ReadErrors: v.ReadErrors, WriteErrors: v.WriteErrors, ChecksumErrors: v.ChecksumErrors,
		})
	}
	return out
}

func zfsReplaceParentType(v zpoolVDev) string {
	switch v.VDevType {
	case "replacing", "spare":
		return v.VDevType
	}
	if strings.HasPrefix(v.Name, "replacing-") {
		return "replacing"
	}
	if strings.HasPrefix(v.Name, "spare-") {
		return "spare"
	}
	return ""
}

// zfsSentence joins the tab-wrapped advice ZFS prints into one line for the report.
func zfsSentence(text string) string { return strings.Join(strings.Fields(text), " ") }
