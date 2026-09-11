// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMediaDownloadAdmissionReplayAndHostMetadata(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	v := mediaDownloadRequest{RequestID: "url-iso", StorageID: l.storage, Name: "installer.iso", URL: "https://example.com/installer.iso"}
	call := func(want int) *httptest.ResponseRecorder {
		return l.a.call("POST", "/api/v1/media/download", v, nil, want)
	}
	cookie := l.a.cookie
	l.a.cookie = nil
	call(401)
	l.a.cookie = cookie
	csrf := l.a.csrf
	l.a.csrf = ""
	call(403)
	l.a.csrf = csrf
	l.a.call("POST", "/api/v1/media/download", `{"unknown":true}`, nil, 400)
	call(409)
	l.caps = append(l.caps, protocol.ISODownloadCapability)
	l.report()
	original := v
	for _, u := range []string{"file:///image.iso", "http://127.0.0.1/a.iso", "https://user:pass@example.com/a.iso", "invalid"} {
		v.URL = u
		call(400)
	}
	v = original
	v.Name = "../installer.iso"
	call(400)
	v = original
	v.StorageID = "missing"
	call(409)
	v = original
	var media protocol.Media
	read(t, call(201), &media)
	if media.URL != "" || media.SizeBytes != 0 || media.SHA256 != "" {
		t.Fatalf("unexpected queued metadata: %+v", media)
	}
	entries, err := os.ReadDir(filepath.Join(l.a.c.cfg.DataDir, "media"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("URL import staged Controller bytes: %v %v", entries, err)
	}
	var replay protocol.Media
	read(t, call(200), &replay)
	if replay.ID != media.ID {
		t.Fatal("retry changed identity")
	}
	v.URL = "https://example.com/other.iso"
	call(409)
	v = original
	l.caps = []string{protocol.ManagedVMCapability}
	if cmds := l.report().Commands; len(cmds) != 0 {
		t.Fatal("URL dispatched to old Core")
	}
	call(200)
	l.caps = append(l.caps, protocol.ISODownloadCapability)
	cmds := l.report().Commands
	if len(cmds) != 1 || cmds[0].Media.URL != v.URL {
		t.Fatalf("wrong URL command: %+v", cmds)
	}
	done := *cmds[0].Media
	done.URL = ""
	done.SizeBytes = 40000
	done.SHA256 = strings.Repeat("a", 64)
	inv := &protocol.Inventory{Hostname: "core", Managed: hostDefaults(l.host.HostID)}
	inv.Managed.Media = []protocol.Media{done}
	l.seq++
	l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv, Results: []protocol.Result{{OperationID: media.OperationID, Status: "succeeded"}}})
	read(t, call(200), &replay)
	if replay.SizeBytes != done.SizeBytes || replay.SHA256 != done.SHA256 || replay.Status != "succeeded" || replay.URL != "" {
		t.Fatalf("host metadata not mirrored: %+v", replay)
	}
	var all []protocol.Media
	read(t, l.a.call("GET", "/api/v1/media", nil, nil, 200), &all)
	if len(all) != 1 || all[0].SHA256 != done.SHA256 {
		t.Fatalf("media list %+v", all)
	}

	// A different authenticated host cannot alter this import's observed metadata.
	other := l.a.enroll(l.a.token(l.a.site(), ""), csr(t))
	forged := done
	forged.SHA256 = strings.Repeat("b", 64)
	l.a.report(certificate(t, other.CertificatePEM), protocol.ReportRequest{Version: 1, Sequence: 1, Capabilities: l.caps, Inventory: &protocol.Inventory{Hostname: "other-core", Managed: &protocol.ManagedState{Media: []protocol.Media{forged}}}})
	read(t, call(200), &replay)
	if replay.SHA256 != done.SHA256 {
		t.Fatal("another host changed ISO metadata")
	}
	// Simulate a fresh Controller resource mirror: only Core's applied inventory remains.
	if _, err := l.a.c.db.Exec("DELETE FROM managed_resources WHERE id=?", done.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.a.c.db.Exec("DELETE FROM operations WHERE id=?", done.OperationID); err != nil {
		t.Fatal(err)
	}
	l.seq++
	out := l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv})
	if len(out.Commands) != 0 {
		t.Fatal("adoption redispatched completed ISO")
	}
	read(t, l.a.call("GET", "/api/v1/media", nil, nil, 200), &all)
	if len(all) != 1 || all[0].ID != done.ID || all[0].Name != done.Name || all[0].StorageID != done.StorageID || all[0].SizeBytes != done.SizeBytes || all[0].SHA256 != done.SHA256 || all[0].URL != "" || all[0].Status != "succeeded" {
		t.Fatalf("Core ISO not recovered: %+v", all)
	}
}
