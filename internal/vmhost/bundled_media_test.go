// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// bundledMediaFixture stages a miniature virtio-win ISO and a temporary default storage tree.
func bundledMediaFixture(t *testing.T) (storage string) {
	t.Helper()
	root := t.TempDir()
	storage = filepath.Join(root, "images")
	if err := os.MkdirAll(storage, 0755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "virtio-win.iso")
	mini := make([]byte, 40000)
	copy(mini[32769:], "CD001")
	sum := sha256.Sum256(mini)
	if err := os.WriteFile(src, mini, 0444); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEX_BUNDLED_VIRTIO_WIN", src)
	oldSize, oldHash := BundledVirtIOWinSize, BundledVirtIOWinSHA256
	BundledVirtIOWinSize, BundledVirtIOWinSHA256 = int64(len(mini)), hex.EncodeToString(sum[:])
	t.Cleanup(func() { BundledVirtIOWinSize, BundledVirtIOWinSHA256 = oldSize, oldHash })
	return storage
}

func storageRegisterJournal(op, id, path string) managedJournal {
	return managedJournal{
		Command: protocol.Command{OperationID: op, Kind: "storage-register", Storage: &protocol.StorageRegistration{
			ID: id, PoolID: "pool-1", HostID: "host-1", Name: DefaultStorageName, Path: path,
		}},
		Result: protocol.Result{OperationID: op, Status: "succeeded"},
		Stage:  "verified",
	}
}

func TestSeedBundledMediaPublishesOnce(t *testing.T) {
	ctx := context.Background()
	storage := bundledMediaFixture(t)
	e := startEngine(t)
	storageID := "storage-1"
	if err := e.save(storageRegisterJournal("storage-op", storageID, storage)); err != nil {
		t.Fatal(err)
	}
	if err := e.seedBundledMedia(ctx, "pool-1", "host-1", storage); err != nil {
		t.Fatal(err)
	}
	js := journalsOf(t, e)
	var media int
	for _, j := range js {
		if j.Command.Kind == "iso-import" {
			media++
			m := journalMedia(j)
			if m.Name != BundledVirtIOWinName || m.SHA256 != BundledVirtIOWinSHA256 || m.StorageID != storageID || j.Result.Status != "succeeded" {
				t.Fatalf("seeded media %+v status %s", m, j.Result.Status)
			}
			if err := verifyISO(j.MediaPath, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	if media != 1 {
		t.Fatalf("media entries %d", media)
	}
	if err := e.seedBundledMedia(ctx, "pool-1", "host-1", storage); err != nil {
		t.Fatal(err)
	}
	if got := journalsOf(t, e); len(got) != 2 {
		t.Fatalf("second seed added entries: %+v", got)
	}
}

// BUG-0534: the service runs with UMask=0077, and the media directory the seed creates
// first must stay traversable by QEMU or every later ISO import to that storage fails.
func TestSeedBundledMediaCreatesTraversableDirectoryUnderServiceUmask(t *testing.T) {
	storage := bundledMediaFixture(t)
	e := startEngine(t)
	if err := e.save(storageRegisterJournal("storage-op", "storage-1", storage)); err != nil {
		t.Fatal(err)
	}
	old := syscall.Umask(0077)
	defer syscall.Umask(old)
	if err := e.seedBundledMedia(context.Background(), "pool-1", "host-1", storage); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(storage, "nex-media"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0711 {
		t.Fatalf("nex-media mode %v, want 0711", info.Mode().Perm())
	}
}

func TestSeedBundledMediaRetriesFailedImport(t *testing.T) {
	storage := bundledMediaFixture(t)
	e := startEngine(t)
	if err := e.save(storageRegisterJournal("storage-op", "storage-1", storage)); err != nil {
		t.Fatal(err)
	}
	failed := protocol.Media{ID: "failed", PoolID: "pool-1", HostID: "host-1", StorageID: "storage-1", Name: BundledVirtIOWinName, SizeBytes: BundledVirtIOWinSize, SHA256: BundledVirtIOWinSHA256}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "failed-import", Kind: "iso-import", Media: &failed}, Result: protocol.Result{OperationID: "failed-import", Status: "failed"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.seedBundledMedia(context.Background(), "pool-1", "host-1", storage); err != nil {
		t.Fatal(err)
	}
	for _, j := range journalsOf(t, e) {
		if j.Command.Kind == "iso-import" && j.Result.Status == "succeeded" {
			return
		}
	}
	t.Fatal("failed import blocked bundled media seeding")
}

func TestSeedBundledMediaDoesNotTreatSameNamedISOAsPinnedBundle(t *testing.T) {
	storage := bundledMediaFixture(t)
	e := startEngine(t)
	if err := e.save(storageRegisterJournal("storage-op", "storage-1", storage)); err != nil {
		t.Fatal(err)
	}
	other := protocol.Media{ID: "other", PoolID: "pool-1", HostID: "host-1", StorageID: "storage-1", Name: BundledVirtIOWinName, SizeBytes: BundledVirtIOWinSize, SHA256: strings.Repeat("a", 64)}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: "other-import", Kind: "iso-import", Media: &other}, Result: protocol.Result{OperationID: "other-import", Status: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	if err := e.seedBundledMedia(context.Background(), "pool-1", "host-1", storage); err != nil {
		t.Fatal(err)
	}
	for _, j := range journalsOf(t, e) {
		if j.Command.Media != nil && j.Command.Media.SHA256 == BundledVirtIOWinSHA256 && j.Result.Status == "succeeded" {
			return
		}
	}
	t.Fatal("same-named ISO blocked the pinned bundled media seed")
}

// A detached default-storage registration must not receive the bundled ISO: media on a
// storage ID the projection no longer holds is adopted as needing attention, not usable.
func TestSeedBundledMediaUsesLiveDefaultStorageAfterDetach(t *testing.T) {
	storage := bundledMediaFixture(t)
	e := startEngine(t)
	for _, j := range []managedJournal{
		// Journal names sort the detached registration first.
		storageRegisterJournal("a-register", "old-storage", storage),
		{Command: protocol.Command{OperationID: "b-detach", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "old-storage", PoolID: "pool-1", HostID: "host-1", ExpectedGeneration: 1}}, Result: protocol.Result{OperationID: "b-detach", Status: "succeeded"}, Stage: "verified"},
		storageRegisterJournal("c-register", "new-storage", storage),
	} {
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.seedBundledMedia(context.Background(), "pool-1", "host-1", storage); err != nil {
		t.Fatal(err)
	}
	js := journalsOf(t, e)
	for _, j := range js {
		if j.Command.Kind == "iso-import" {
			m := journalMedia(j)
			if m.StorageID != "new-storage" {
				t.Fatalf("bundled media published to %q, not the live default storage", m.StorageID)
			}
			if _, _, err := publishedMediaPath(js, m.ID); err != nil {
				t.Fatalf("seeded media is not usable: %v", err)
			}
			return
		}
	}
	t.Fatal("bundled media was not seeded")
}

func TestSeedBundledMediaSkipsDetachedOnlyDefaultStorage(t *testing.T) {
	storage := bundledMediaFixture(t)
	e := startEngine(t)
	for _, j := range []managedJournal{
		storageRegisterJournal("a-register", "old-storage", storage),
		{Command: protocol.Command{OperationID: "b-detach", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "old-storage", PoolID: "pool-1", HostID: "host-1", ExpectedGeneration: 1}}, Result: protocol.Result{OperationID: "b-detach", Status: "succeeded"}, Stage: "verified"},
	} {
		if err := e.save(j); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.seedBundledMedia(context.Background(), "pool-1", "host-1", storage); err != nil {
		t.Fatal(err)
	}
	if js := journalsOf(t, e); len(js) != 2 {
		t.Fatalf("seeded media onto detached storage: %+v", js)
	}
}

func TestSeedBundledMediaNoopsWithoutPackageFile(t *testing.T) {
	t.Setenv("NEX_BUNDLED_VIRTIO_WIN", filepath.Join(t.TempDir(), "missing.iso"))
	e := startEngine(t)
	if err := e.SeedBundledMedia(context.Background(), "pool-1", "host-1"); err != nil {
		t.Fatal(err)
	}
	if js := journalsOf(t, e); len(js) != 0 {
		t.Fatalf("unexpected journal %+v", js)
	}
}

// Review R-3: the seed copies unlocked, but a storage detach that lands before it journals must
// win; the seed decides under the operations lock and discards the published copy.
func TestSeedBundledMediaDiscardsCopyWhenStorageDetachedMeanwhile(t *testing.T) {
	storage := bundledMediaFixture(t)
	e := startEngine(t)
	if err := e.save(storageRegisterJournal("a-register", "storage-1", storage)); err != nil {
		t.Fatal(err)
	}
	held, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.seedBundledMedia(context.Background(), "pool-1", "host-1", storage) }()
	// Wait until the copy is published (or the seed finished without waiting for the lock).
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if isos, _ := filepath.Glob(filepath.Join(storage, "nex-media", "*.iso")); len(isos) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bundled copy was never published")
		}
	}
	time.Sleep(50 * time.Millisecond)
	detach := managedJournal{Command: protocol.Command{OperationID: "b-detach", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "storage-1", PoolID: "pool-1", HostID: "host-1", ExpectedGeneration: 1}}, Result: protocol.Result{OperationID: "b-detach", Status: "succeeded"}, Stage: "verified"}
	if err = e.save(detach); err != nil {
		t.Fatal(err)
	}
	unlock(held)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	for _, j := range journalsOf(t, e) {
		if j.Command.Kind == "iso-import" {
			t.Fatalf("media journaled on detached storage: %+v", journalMedia(j))
		}
	}
	if isos, _ := filepath.Glob(filepath.Join(storage, "nex-media", "*.iso")); len(isos) != 0 {
		t.Fatalf("discarded copy left behind: %v", isos)
	}
}
