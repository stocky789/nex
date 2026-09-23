// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// sharedMediaFixture publishes a pool-shared ISO on registered storage and returns the
// engine, the media record a member would adopt, and the ISO's path.
func sharedMediaFixture(t *testing.T, mediaID string) (*ManagedEngine, protocol.Media, string) {
	t.Helper()
	state, repo := t.TempDir(), t.TempDir()
	e := NewManaged(state)
	storage := managedJournal{
		Command: protocol.Command{OperationID: "st", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "storage-1", PoolID: "pool1", HostID: "host1", Name: "shared", Path: repo}},
		Result:  protocol.Result{OperationID: "st", Status: "succeeded"},
	}
	b, err := json.Marshal(storage)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, "st.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 40<<10)
	copy(body[32769:], "CD001")
	if err = os.MkdirAll(filepath.Join(repo, "nex-media"), 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "nex-media", mediaID+".iso")
	if err = os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return e, protocol.Media{
		ID: mediaID, PoolID: "pool1", HostID: "host1", StorageID: "storage-1", Name: "shared.iso",
		SizeBytes: int64(len(body)), SHA256: hex.EncodeToString(sum[:]),
	}, path
}

// A Controller media ID is 64 characters, the whole identifier budget, so an operation ID
// built by prefixing it names a journal file the host can never read back. That poisons
// every later managed operation on the member until the file is deleted by hand.
func TestAdoptSharedMediaKeepsTheJournalReadableForALongMediaID(t *testing.T) {
	mediaID := strings.Repeat("ab", 32)
	e, media, _ := sharedMediaFixture(t, mediaID)
	if err := e.AdoptSharedMedia(context.Background(), media); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	js, err := e.journals()
	if err != nil {
		t.Fatalf("journal unreadable after adoption: %v", err)
	}
	if len(js) != 2 {
		t.Fatalf("journal entries = %d", len(js))
	}
	applied := appliedMedia(t, e)
	if len(applied) != 1 || applied[0].ID != mediaID || applied[0].StorageID != "storage-1" {
		t.Fatalf("applied media = %#v", applied)
	}
	// Execute reads the whole journal before doing anything; an unreadable entry fails
	// every managed operation on this host, not just media.
	result, err := e.Execute(context.Background(), protocol.Command{
		OperationID: "later-op", Kind: "network-register",
		Network: &protocol.NetworkRegistration{ID: "net-1", PoolID: "pool1", HostID: "host1", Name: "guests", Kind: "bridge", Source: "br-absent"},
	}, nil)
	if err != nil {
		t.Fatalf("managed execute blocked after adoption: %v", err)
	}
	if result.Status != "failed" || result.Error != "selected bridge does not exist; configure networking separately" {
		t.Fatalf("later execute = %+v", result)
	}
}

// Converge runs every probe interval. Re-hashing every shared ISO each pass reads the whole
// library off remote storage forever; an already-adopted ISO is confirmed by its published
// size instead, and its digest is checked on first adoption and whenever a VM attaches it.
func TestAdoptSharedMediaDoesNotRehashAnAdoptedISO(t *testing.T) {
	e, media, path := sharedMediaFixture(t, strings.Repeat("cd", 32))
	if err := e.AdoptSharedMedia(context.Background(), media); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body[0] ^= 0xff
	if err = os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	if err = e.AdoptSharedMedia(context.Background(), media); err != nil {
		t.Fatalf("adopted ISO re-hashed on a later pass: %v", err)
	}
	truncated := append([]byte(nil), body[:len(body)-1]...)
	if err = os.WriteFile(path, truncated, 0644); err != nil {
		t.Fatal(err)
	}
	if e.AdoptSharedMedia(context.Background(), media) == nil {
		t.Fatal("a shared ISO that changed size was still reported adopted")
	}
}

// Converge gives each ISO one probe timeout (3s), too short to hash a few GiB off remote
// storage, so first adoption never finished (BUG-0541). Adoption checks presence and size;
// every attach verifies the digest.
func TestAdoptSharedMediaDoesNotHashOnFirstAdoption(t *testing.T) {
	e, media, path := sharedMediaFixture(t, strings.Repeat("ef", 32))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, body[:len(body)-1], 0644); err != nil {
		t.Fatal(err)
	}
	if e.AdoptSharedMedia(context.Background(), media) == nil || len(appliedMedia(t, e)) != 0 {
		t.Fatal("a shared ISO of the wrong size was adopted")
	}
	body[0] ^= 0xff
	if err = os.WriteFile(path, body, 0644); err != nil {
		t.Fatal(err)
	}
	if err = e.AdoptSharedMedia(context.Background(), media); err != nil {
		t.Fatalf("first adoption hashed the ISO: %v", err)
	}
	if len(appliedMedia(t, e)) != 1 {
		t.Fatal("adopted ISO was not journalled")
	}
}
