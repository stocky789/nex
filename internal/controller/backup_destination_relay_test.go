// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestBackupDestinationRelayFailsClosedWithoutCoreSupport(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: hostDefaults(master.HostID)},
	})
	path := "/api/v1/pools/" + pool + "/backup-repositories"
	nfs := map[string]any{
		"request_id": "repo-register-1", "id": "repo-1", "name": "Primary", "kind": "nfs",
		"source": map[string]string{"server": "nfs.example.test", "export": "/export/backups"},
	}
	a.call("POST", path, nfs, nil, 202)
	a.call("POST", path, nfs, nil, 200)
	nfs["name"] = "Changed"
	a.call("POST", path, nfs, nil, 409)
	nfs["name"] = "Primary"
	nfs["credentials"] = map[string]string{"secret": "do-not-store-this"}
	a.call("POST", path, nfs, nil, 400)
	delete(nfs, "credentials")
	nfs["source"] = map[string]string{"server": "nfs.example.test", "export": "/export/backups", "secret": "do-not-store-this"}
	a.call("POST", path, nfs, nil, 400)
	nfs["source"] = map[string]string{"server": "nfs.example.test", "export": "/export/backups"}
	a.call("POST", "/api/v1/pools/other/backup-repositories", nfs, nil, 409)
	cookie := a.cookie
	a.cookie = nil
	a.call("POST", path, nfs, nil, 401)
	a.cookie = cookie

	a.call("POST", path, map[string]any{
		"request_id": "repo-register-2", "id": "repo-2", "name": "Remote", "kind": "s3",
		"credential_ref": "opaque-ref", "credential_request_id": "credential-2",
		"s3_source": map[string]string{"endpoint": "https://objects.example.test", "bucket": "backups"},
	}, nil, 409)
	a.call("POST", path, map[string]any{
		"request_id": "repo-register-3", "id": "repo-3", "name": "Unknown", "kind": "unknown",
	}, nil, 400)

	var payload, command string
	if err := a.c.db.QueryRow("SELECT payload,command FROM managed_resources WHERE request_id=?", "repo-register-1").Scan(&payload, &command); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload+command, "do-not-store-this") {
		t.Fatal("Controller persisted rejected credential material")
	}
	var count int
	if err := a.c.db.QueryRow("SELECT count(*) FROM managed_resources WHERE request_id IN ('repo-register-2','repo-register-3')").Scan(&count); err != nil || count != 0 {
		t.Fatalf("unsupported destination was queued: %d, %v", count, err)
	}
}

func TestBackupCredentialHandoffAndRegistrationKeepSecretOutOfDatabase(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1,
		Capabilities: []string{protocol.ManagedVMCapability, "backup-destination-s3-v1"},
		Inventory:    &protocol.Inventory{Hostname: "core", Managed: hostDefaults(master.HostID)},
	})
	const registrationID = "repo-reg-remote"
	const credentialID = "cred-remote-1"
	const repositoryID = "repo-remote"
	const secret = "private-test-secret"
	binding := "backup-credential:s3:" + registrationID
	handoff := storageCredentialRequest{RequestID: credentialID, RegistrationRequestID: binding, PoolID: pool, HostID: master.HostID, RepositoryID: repositoryID,
		Credentials: protocol.StorageCredentials{Username: "backup-user", Secret: secret}}
	// A destination the registration would refuse is rejected before any secret is offered.
	for _, repository := range []*backupCredentialRepository{nil, {Name: "Remote", S3Source: &protocol.BackupS3Source{Endpoint: "https://objects.example.test", Bucket: "a/b"}}} {
		handoff.Repository = repository
		a.call("POST", "/api/v1/storage/credentials", handoff, nil, 400)
	}
	if offers := a.c.credentials.offers(master.HostID); len(offers) != 0 {
		t.Fatalf("invalid destination offered its credential: %#v", offers)
	}
	a.call("POST", "/api/v1/storage/credentials", storageCredentialRequest{RequestID: "cred-unknown-pool", RegistrationRequestID: binding, PoolID: "no-such-pool",
		HostID: master.HostID, RepositoryID: repositoryID, Credentials: handoff.Credentials, Repository: &backupCredentialRepository{Name: "Remote",
			S3Source: &protocol.BackupS3Source{Endpoint: "https://objects.example.test", Bucket: "backups"}}}, nil, 404)
	handoff.Repository = &backupCredentialRepository{Name: "Remote", S3Source: &protocol.BackupS3Source{Endpoint: "https://objects.example.test", Bucket: "backups"}}
	raw, err := json.Marshal(handoff)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "https://nex.test/api/v1/storage/credentials", strings.NewReader(string(raw)))
		req.AddCookie(a.cookie)
		req.Header.Set("X-CSRF-Token", a.csrf)
		w := httptest.NewRecorder()
		a.c.Handler().ServeHTTP(w, req)
		result <- w.Code
	}()
	var offer protocol.StorageCredentialOffer
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if offers := a.c.credentials.offers(master.HostID); len(offers) == 1 {
			offer = offers[0]
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if offer.Handle == "" {
		t.Fatal("Core credential offer missing")
	}
	receipt := protocol.StorageCredentialReceipt{Receipt: strings.Repeat("a", 48), RequestID: credentialID, RegistrationRequestID: binding,
		PoolID: pool, HostID: master.HostID, RepositoryID: repositoryID, Status: "ready"}
	if err := a.c.credentials.acknowledge(offer.Handle, master.HostID, receipt); err != nil {
		t.Fatal(err)
	}
	if code := <-result; code != http.StatusCreated {
		t.Fatalf("credential handoff status %d", code)
	}
	path := "/api/v1/pools/" + pool + "/backup-repositories"
	body := map[string]any{"request_id": registrationID, "id": repositoryID, "name": "Remote", "kind": "s3",
		"credential_ref": receipt.Receipt, "credential_request_id": credentialID,
		"s3_source": map[string]string{"endpoint": "https://objects.example.test", "bucket": "backups"}}
	delete(body, "id")
	a.call("POST", path, body, nil, 400)
	body["id"] = repositoryID
	a.call("POST", path, body, nil, 202)
	a.call("POST", path, body, nil, 200)
	body["request_id"] = "repo-reg-other"
	a.call("POST", path, body, nil, 409)
	body["request_id"] = registrationID
	body["credential_ref"] = strings.Repeat("b", 48)
	a.call("POST", path, body, nil, 409)
	handoff.HostID = "other-host"
	a.call("POST", "/api/v1/storage/credentials", handoff, nil, 409)
	handoff.RegistrationRequestID = "backup-credential:unknown:" + registrationID
	a.call("POST", "/api/v1/storage/credentials", handoff, nil, 400)
	cookie := a.cookie
	a.cookie = nil
	a.call("POST", "/api/v1/storage/credentials", handoff, nil, 401)
	a.cookie = cookie
	var payload, command string
	if err := a.c.db.QueryRow("SELECT payload,command FROM managed_resources WHERE request_id=?", registrationID).Scan(&payload, &command); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload+command, secret) || strings.Contains(payload+command, "backup-user") || !strings.Contains(command, receipt.Receipt) {
		t.Fatal("Controller persisted credential material or lost its opaque receipt")
	}
}

func TestBackupCredentialReplayBindsSFTPPrivateKey(t *testing.T) {
	broker := newCredentialBroker()
	request := storageCredentialRequest{RequestID: "cred-sftp", RegistrationRequestID: "backup-credential:sftp:repo-reg",
		PoolID: "pool-1", HostID: "host-1", RepositoryID: "repo-1",
		Credentials: protocol.StorageCredentials{Username: "backup-user", SSHPrivateKey: "first-private-key"}}
	offer, err := broker.offer(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := broker.acknowledge(offer.Handle, request.HostID, protocol.StorageCredentialReceipt{
		Receipt: "receipt-1", RequestID: request.RequestID, RegistrationRequestID: request.RegistrationRequestID,
		PoolID: request.PoolID, HostID: request.HostID, RepositoryID: request.RepositoryID, Status: "ready",
	}); err != nil {
		t.Fatal(err)
	}
	request.Credentials.SSHPrivateKey = "different-private-key"
	if _, err := broker.offer(request); err == nil {
		t.Fatal("SFTP key change reused an acknowledged credential request")
	}
}

func TestBackupRepositoryListMarksStaleHealthUnknown(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	repo := protocol.BackupRepository{
		ID: "repo-1", PoolID: pool, Name: "Primary", Kind: protocol.BackupRepoKindNFS,
		Source: protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export/backups"},
		Health: protocol.BackupRepositoryHealth{Status: "online", ObservedAt: time.Now().UTC()},
	}
	managed := hostDefaults(master.HostID)
	managed.Membership = &protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
	}}
	managed.BackupRepositories = []protocol.BackupRepository{repo}
	managed.BackupRepositoriesReported = true
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: managed},
	})
	path := "/api/v1/pools/" + pool + "/backup-repositories"
	var rows []protocol.BackupRepository
	read(t, a.call("GET", path, nil, nil, 200), &rows)
	if len(rows) != 1 || rows[0].Health.Status != "online" {
		t.Fatalf("fresh health %#v", rows)
	}
	a.c.mu.Lock()
	a.c.seen[master.HostID] = time.Now().Add(-2 * a.c.cfg.StaleAfter)
	a.c.mu.Unlock()
	read(t, a.call("GET", path, nil, nil, 200), &rows)
	if len(rows) != 1 || rows[0].Health.Status != "unknown" {
		t.Fatalf("stale master health %#v", rows)
	}
	// Core ages health on its own clock; a master clock ahead of the Controller keeps it.
	repo.Health.ObservedAt = time.Now().Add(3 * time.Minute)
	managed.BackupRepositories = []protocol.BackupRepository{repo}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 2, Capabilities: []string{protocol.ManagedVMCapability},
		Inventory: &protocol.Inventory{Hostname: "core", Managed: managed},
	})
	read(t, a.call("GET", path, nil, nil, 200), &rows)
	if len(rows) != 1 || rows[0].Health.Status != "online" {
		t.Fatalf("skewed Core health %#v", rows)
	}
}

// Domain accounts work for SMB3 backup credentials: a valid domain reaches the Core offer,
// while separators, control characters and non-SMB kinds are refused before any offer.
func TestBackupCredentialHandoffDomainValidation(t *testing.T) {
	a := newClient(t)
	master := a.enroll(a.token(a.site(), ""), csr(t))
	pool := a.poolOf(master.HostID)
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{
		Version: 1, Sequence: 1,
		Capabilities: []string{protocol.ManagedVMCapability, "backup-destination-smb3-v1"},
		Inventory:    &protocol.Inventory{Hostname: "core", Managed: hostDefaults(master.HostID)},
	})
	smb := &backupCredentialRepository{Name: "Share", SMBSource: &protocol.SMBStorageSource{Server: "smb.example.test", Share: "backups"}}
	request := func(kind, domain string, repository *backupCredentialRepository) storageCredentialRequest {
		return storageCredentialRequest{RequestID: "cred-domain", RegistrationRequestID: "backup-credential:" + kind + ":repo-reg-domain", PoolID: pool, HostID: master.HostID,
			RepositoryID: "repo-domain", Repository: repository, Credentials: protocol.StorageCredentials{Username: "user", Secret: "private-test-secret", Domain: domain}}
	}
	s3 := &backupCredentialRepository{Name: "Objects", S3Source: &protocol.BackupS3Source{Endpoint: "https://objects.example.test", Bucket: "backups"}}
	for _, bad := range []storageCredentialRequest{
		request("smb3", "EX,AMPLE", smb), request("smb3", "EX=AMPLE", smb), request("smb3", "EX\nAMPLE", smb), request("smb3", `EX\AMPLE`, smb), request("s3", "EXAMPLE", s3),
	} {
		a.call("POST", "/api/v1/storage/credentials", bad, nil, 400)
	}
	if offers := a.c.credentials.offers(master.HostID); len(offers) != 0 {
		t.Fatalf("invalid domain offered its credential: %#v", offers)
	}
	raw, err := json.Marshal(request("smb3", "corp.example.test", smb))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "https://nex.test/api/v1/storage/credentials", strings.NewReader(string(raw)))
		req.AddCookie(a.cookie)
		req.Header.Set("X-CSRF-Token", a.csrf)
		w := httptest.NewRecorder()
		a.c.Handler().ServeHTTP(w, req)
		result <- w.Code
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if offers := a.c.credentials.offers(master.HostID); len(offers) == 1 {
			receipt := protocol.StorageCredentialReceipt{Receipt: strings.Repeat("a", 48), RequestID: "cred-domain", RegistrationRequestID: "backup-credential:smb3:repo-reg-domain",
				PoolID: pool, HostID: master.HostID, RepositoryID: "repo-domain", Status: "ready"}
			if err := a.c.credentials.acknowledge(offers[0].Handle, master.HostID, receipt); err != nil {
				t.Fatal(err)
			}
			if code := <-result; code != http.StatusCreated {
				t.Fatalf("domain handoff status %d", code)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case code := <-result:
		t.Fatalf("valid SMB domain refused with HTTP %d", code)
	default:
		t.Fatal("valid SMB domain never reached a Core offer")
	}
}
