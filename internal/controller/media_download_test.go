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

// A failed import must not be a permanent ISO library row. Clearing one removes only this
// Controller's request record: the host keeps its journal outcome, the task history stays,
// and an ISO the host actually holds is Core's record, so removing it here is refused.
func TestFailedMediaImportIsRemovableAndRetryable(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.ISODownloadCapability)
	v := mediaDownloadRequest{RequestID: "failed-iso", StorageID: l.storage, Name: "virtio-win.iso", URL: "https://example.com/virtio-win.iso"}
	var queued protocol.Media
	read(t, l.a.call("POST", "/api/v1/media/download", v, nil, 201), &queued)
	path := "/api/v1/media/" + queued.ID
	list := func() []protocol.Media {
		t.Helper()
		var out []protocol.Media
		read(t, l.a.call("GET", "/api/v1/media", nil, nil, 200), &out)
		return out
	}
	// A queued or dispatched import is the host's to finish; only its task can end it.
	l.a.call("DELETE", path, nil, nil, 409)
	if cmds := l.report().Commands; len(cmds) != 1 {
		t.Fatalf("import not dispatched: %+v", cmds)
	}
	l.a.call("DELETE", path, nil, nil, 409)
	l.report(protocol.Result{OperationID: queued.OperationID, Status: "failed", Error: "ISO download refused this server's redirect from HTTPS to plain HTTP; use a direct HTTPS link to the ISO"})
	if all := list(); len(all) != 1 || all[0].Status != "failed" || all[0].SizeBytes != 0 {
		t.Fatalf("failed import row: %+v", all)
	}
	// Authentication and CSRF are required exactly as on the other admin media routes.
	cookie, csrf := l.a.cookie, l.a.csrf
	l.a.cookie = nil
	l.a.call("DELETE", path, nil, nil, 401)
	l.a.cookie = cookie
	l.a.csrf = ""
	l.a.call("DELETE", path, nil, nil, 403)
	l.a.csrf = csrf
	// The target is explicit: an unknown or malformed identity removes nothing.
	l.a.call("DELETE", "/api/v1/media/"+strings.Repeat("a", 64), nil, nil, 404)
	l.a.call("DELETE", "/api/v1/media/"+strings.Repeat("a", 65), nil, nil, 404)
	if len(list()) != 1 {
		t.Fatal("a rejected target removed a library entry")
	}
	l.a.call("DELETE", path, nil, nil, 200)
	l.a.call("DELETE", path, nil, nil, 404)
	if all := list(); len(all) != 0 {
		t.Fatalf("cleared import still listed: %+v", all)
	}
	// What happened is history, not library content: the failed task stays.
	kept := false
	for _, op := range l.operations() {
		kept = kept || (op.ID == queued.OperationID && op.Status == "failed" && op.Kind == "iso-import")
	}
	if !kept {
		t.Fatal("clearing the entry erased its task history")
	}
	// The host reports that failed outcome on every heartbeat; it must not restore the row.
	inv := &protocol.Inventory{Hostname: "core", Managed: hostDefaults(l.host.HostID)}
	inv.Managed.Outcomes = []protocol.ManagedOutcome{{OperationID: queued.OperationID, Kind: "iso-import", ID: queued.ID, Name: v.Name, Status: "failed", Error: "download refused"}}
	l.seq++
	l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv})
	if all := list(); len(all) != 0 {
		t.Fatalf("host outcome restored a cleared import: %+v", all)
	}
	// Retrying is the existing download call under a new request_id; nothing else is needed.
	retry := v
	retry.RequestID = "failed-iso-retry"
	var again protocol.Media
	read(t, l.a.call("POST", "/api/v1/media/download", retry, nil, 201), &again)
	if again.ID == queued.ID || again.OperationID == queued.OperationID {
		t.Fatal("retry reused the failed import identity")
	}
	if cmds := l.report().Commands; len(cmds) != 1 || cmds[0].Media == nil || cmds[0].Media.URL != v.URL {
		t.Fatalf("retry not dispatched: %+v", cmds)
	}
	// An interrupted import with an uncertain outcome is clearable: it published nothing the
	// host reports, and adoption restores the entry from Core's record if it did after all.
	l.report(protocol.Result{OperationID: again.OperationID, Status: "needs_attention", Error: "interrupted"})
	l.a.call("DELETE", "/api/v1/media/"+again.ID, nil, nil, 200)
	if all := list(); len(all) != 0 {
		t.Fatalf("interrupted import not cleared: %+v", all)
	}
	// An ISO the host holds belongs to the host: the library cannot drop Core's record.
	third := v
	third.RequestID = "stored-iso"
	var stored protocol.Media
	read(t, l.a.call("POST", "/api/v1/media/download", third, nil, 201), &stored)
	if cmds := l.report().Commands; len(cmds) != 1 {
		t.Fatalf("third import not dispatched: %+v", cmds)
	}
	applied := protocol.Media{ID: stored.ID, PoolID: l.pool, HostID: l.host.HostID, StorageID: l.storage, Name: v.Name, SizeBytes: 40000, SHA256: strings.Repeat("c", 64)}
	inv = &protocol.Inventory{Hostname: "core", Managed: hostDefaults(l.host.HostID)}
	inv.Managed.Media = []protocol.Media{applied}
	l.seq++
	l.a.report(l.cert, protocol.ReportRequest{Version: 1, Sequence: l.seq, Capabilities: l.caps, Inventory: inv, Results: []protocol.Result{{OperationID: stored.OperationID, Status: "succeeded"}}})
	l.a.call("DELETE", "/api/v1/media/"+stored.ID, nil, nil, 409)
	if all := list(); len(all) != 1 || all[0].ID != stored.ID || all[0].Status != "succeeded" {
		t.Fatalf("stored ISO changed: %+v", all)
	}
}

// Staged upload bytes of an import that never reached its host are Controller scratch
// space; clearing the entry releases them instead of holding staging capacity until restart.
func TestClearingAFailedUploadReleasesStagedBytes(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	iso := make([]byte, 32774)
	copy(iso[32769:], "CD001")
	var queued protocol.Media
	read(t, l.a.call("POST", "/api/v1/media?storage_id="+l.storage+"&name=guest.iso&request_id=upload-iso", string(iso), nil, 201), &queued)
	staged := filepath.Join(l.a.c.cfg.DataDir, "media", queued.ID)
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("upload was not staged: %v", err)
	}
	if cmds := l.report().Commands; len(cmds) != 1 {
		t.Fatalf("upload not dispatched: %+v", cmds)
	}
	l.report(protocol.Result{OperationID: queued.OperationID, Status: "failed", Error: "host transfer failed"})
	l.a.call("DELETE", "/api/v1/media/"+queued.ID, nil, nil, 200)
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged bytes kept after the entry was cleared: %v", err)
	}
}

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
