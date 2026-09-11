// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestMediaRejectsRepositoryWithoutCoreISOWriteAccess(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.ISODownloadCapability)
	var raw string
	if err := l.a.c.db.QueryRow("SELECT command FROM managed_resources WHERE id=?", l.storage).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cmd protocol.Command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		t.Fatal(err)
	}
	cmd.Storage.Kind = "local"
	cmd.Storage.Capabilities = protocol.StorageCapabilities{VMDisk: true, ISO: true}
	cmd.Storage.Access = protocol.StorageAccess{Readable: true, Writable: true}
	iso := make([]byte, 32774)
	copy(iso[32769:], "CD001")
	upload := "/api/v1/media?storage_id=" + l.storage + "&name=guest.iso&request_id=storage-iso-upload"
	download := mediaDownloadRequest{RequestID: "storage-iso-download", StorageID: l.storage, Name: "guest.iso", URL: "https://example.com/guest.iso"}
	for _, field := range []string{"readable", "writable", "iso", "detached"} {
		s := *cmd.Storage
		switch field {
		case "readable":
			s.Access.Readable = false
		case "writable":
			s.Access.Writable = false
		case "iso":
			s.Capabilities.ISO = false
		case "detached":
			s.Tombstone = true
		}
		changed := cmd
		changed.Storage = &s
		b, err := json.Marshal(changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.a.c.db.Exec("UPDATE managed_resources SET command=? WHERE id=?", string(b), l.storage); err != nil {
			t.Fatal(err)
		}
		l.a.call("POST", upload, string(iso), nil, 409)
		l.a.call("POST", "/api/v1/media/download", download, nil, 409)
	}
	b, _ := json.Marshal(cmd)
	if _, err := l.a.c.db.Exec("UPDATE managed_resources SET command=? WHERE id=?", string(b), l.storage); err != nil {
		t.Fatal(err)
	}
	l.a.call("POST", upload, string(iso), nil, 201)
	l.a.call("POST", "/api/v1/media/download", download, nil, 201)
}
