// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"nex.local/nex/internal/protocol"
	"testing"
	"time"
)

func TestISODownloadProgressIsHostObservedAndClears(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.ISODownloadCapability)
	l.seq++
	supported := l.a.report(l.cert, protocol.ReportRequest{Version: protocol.Version, Sequence: l.seq, Capabilities: []string{protocol.ISODownloadProgressCapability}})
	if supported.ISODownloadProgress == nil || !*supported.ISODownloadProgress {
		t.Fatal("report did not acknowledge ISO progress support")
	}
	l.report()
	var queued protocol.Media
	read(t, l.a.call("POST", "/api/v1/media/download", mediaDownloadRequest{
		RequestID: "progress-iso", StorageID: l.storage, Name: "installer.iso", URL: "https://example.com/installer.iso",
	}, nil, 201), &queued)
	if cmds := l.report().Commands; len(cmds) != 1 || cmds[0].OperationID != queued.OperationID {
		t.Fatalf("URL download was not dispatched: %+v", cmds)
	}
	report := func(progress []protocol.ISODownloadProgress, want int, results ...protocol.Result) {
		t.Helper()
		l.seq++
		state := hostDefaults(l.host.HostID)
		state.ISODownloads = progress
		l.a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: l.seq,
			Capabilities: l.caps, Inventory: &protocol.Inventory{Hostname: "core", Managed: state}, Results: results}, l.cert, want)
	}
	current := protocol.ISODownloadProgress{OperationID: queued.OperationID, ReceivedBytes: 4096, TotalBytes: 8192}
	for _, invalid := range []protocol.ISODownloadProgress{
		{OperationID: queued.OperationID, ReceivedBytes: -1},
		{OperationID: queued.OperationID, ReceivedBytes: 8193, TotalBytes: 8192},
	} {
		report([]protocol.ISODownloadProgress{invalid}, 400)
	}
	report([]protocol.ISODownloadProgress{current, current}, 400)
	other := l.a.enroll(l.a.token(l.a.site(), ""), csr(t))
	l.a.call("POST", "/api/v1/agent/report", protocol.ReportRequest{Version: 1, Sequence: 1,
		Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{ISODownloads: []protocol.ISODownloadProgress{current}}}},
		certificate(t, other.CertificatePEM), 400)
	unknown := protocol.ISODownloadProgress{OperationID: "former-controller-import", ReceivedBytes: 4096, TotalBytes: 8192}
	report([]protocol.ISODownloadProgress{unknown, current}, 200)
	getHost := func() protocol.Host {
		t.Helper()
		for _, host := range l.a.hosts() {
			if host.ID == l.host.HostID {
				return host
			}
		}
		t.Fatal("host disappeared")
		return protocol.Host{}
	}
	host := getHost()
	if host.Inventory == nil || host.Inventory.Managed == nil || len(host.Inventory.Managed.ISODownloads) != 1 || host.Inventory.Managed.ISODownloads[0] != current {
		t.Fatalf("progress was not exposed through host inventory: %+v", host)
	}
	l.a.c.mu.Lock()
	l.a.c.seen[l.host.HostID] = time.Now().Add(-2 * l.a.c.cfg.StaleAfter)
	l.a.c.mu.Unlock()
	host = getHost()
	if host.Connection != "stale" || len(host.Inventory.Managed.ISODownloads) != 1 {
		t.Fatalf("stale progress was presented as current: %+v", host)
	}
	report(nil, 200)
	host = getHost()
	if host.Inventory.Managed != nil && len(host.Inventory.Managed.ISODownloads) != 0 {
		t.Fatalf("omitted progress remained visible: %+v", host)
	}
	report([]protocol.ISODownloadProgress{current}, 200)
	terminal := protocol.Result{OperationID: queued.OperationID, Status: "failed", Error: "download interrupted"}
	report([]protocol.ISODownloadProgress{current}, 200, terminal)
	host = getHost()
	if host.Inventory.Managed != nil && len(host.Inventory.Managed.ISODownloads) != 0 {
		t.Fatalf("terminal result retained its earlier progress snapshot: %+v", host)
	}
	report([]protocol.ISODownloadProgress{current}, 200, terminal)
	report([]protocol.ISODownloadProgress{current}, 400)
	report([]protocol.ISODownloadProgress{unknown}, 200)
	host = getHost()
	if host.Inventory.Managed != nil && len(host.Inventory.Managed.ISODownloads) != 0 {
		t.Fatalf("unknown operation progress was mirrored: %+v", host)
	}
	unknown.ReceivedBytes = -1
	report([]protocol.ISODownloadProgress{unknown}, 400)
}
