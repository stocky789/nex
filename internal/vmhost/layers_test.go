// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type layerFixture struct {
	t     *testing.T
	cfg   LayerConfig
	priv  ed25519.PrivateKey
	body  []byte
	build string
	calls []string
}

// newLayerFixture builds a host tree with one signed layer "nex-zfs" in the store. layersRun and
// nativeRun are stubbed: a sysext merge is simulated by exposing the layer's metadata file, which is
// what the real merge makes visible under /usr/lib/nex/layers.
func newLayerFixture(t *testing.T) *layerFixture {
	t.Helper()
	root := t.TempDir()
	f := &layerFixture{t: t, build: "20261001.1"}
	f.cfg = LayerConfig{
		Dir: filepath.Join(root, "layers"), OSRelease: filepath.Join(root, "os-release"),
		LayerInfoDir: filepath.Join(root, "usr-lib-nex-layers"), ExtensionsDir: filepath.Join(root, "run-extensions"),
		ReportPath: filepath.Join(root, "run", "status.json"), TrustedKey: filepath.Join(root, "release.pub"),
		SysModule: filepath.Join(root, "sys-module"),
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.priv = priv
	f.write(f.cfg.TrustedKey, pub)
	f.write(f.cfg.OSRelease, []byte("ID=nex-core\nBUILD_ID="+f.build+"\nSYSEXT_LEVEL="+f.build+"\n"))
	layer := []byte("layer-bytes-of-nex-zfs")
	f.publish(map[string]any{"id": "nex-zfs", "kind": "layer", "bytes": len(layer), "requires_base": f.build}, layer)

	oldLayers, oldNative := layersRun, nativeRun
	t.Cleanup(func() { layersRun, nativeRun = oldLayers, oldNative })
	layersRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.calls = append(f.calls, name+" "+strings.Join(args, " "))
		if name == "systemd-sysext" {
			meta, _ := json.Marshal(layerMeta{Name: "nex-zfs", RequiresBase: f.build, UnitsEnable: []string{"zfs.target"}, Modules: []string{"zfs"}})
			f.write(filepath.Join(f.cfg.LayerInfoDir, "nex-zfs.json"), meta)
		}
		return nil, nil
	}
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.calls = append(f.calls, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	return f
}

func (f *layerFixture) write(path string, b []byte) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		f.t.Fatal(err)
	}
}

// publish writes the signed manifest for the running build and puts layer into the store under its digest.
func (f *layerFixture) publish(component map[string]any, layer []byte) {
	sum := sha256.Sum256(layer)
	component["sha256"] = hex.EncodeToString(sum[:])
	body, err := json.Marshal(map[string]any{"components": []any{component}})
	if err != nil {
		f.t.Fatal(err)
	}
	dir := filepath.Join(f.cfg.Dir, "releases", f.build)
	f.write(filepath.Join(dir, "manifest.json"), body)
	f.write(filepath.Join(dir, "manifest.sig"), ed25519.Sign(f.priv, body))
	f.write(filepath.Join(f.cfg.Dir, "store", "sha256-"+component["sha256"].(string)+".raw"), layer)
	f.body = body
}

func (f *layerFixture) layers() *Layers {
	l := NewLayers(f.cfg)
	l.InUse = func(context.Context, layerMeta) (bool, string) { return false, "" }
	return l
}

func (f *layerFixture) state() layerState {
	f.t.Helper()
	s, err := f.layers().readState()
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	var le *LayerError
	if !errors.As(err, &le) || le.Reason != reason || !errors.Is(err, ErrLayerUnavailable) {
		t.Fatalf("error = %v, want layer-unavailable/%s", err, reason)
	}
}

func TestLayerEnsureVerifiesRecordsAndMerges(t *testing.T) {
	f := newLayerFixture(t)
	if err := f.layers().Ensure(context.Background(), "nex-zfs"); err != nil {
		t.Fatal(err)
	}
	if e := f.state().Layers["nex-zfs"]; !e.Enabled || e.Base != f.build {
		t.Fatalf("journal entry = %#v", e)
	}
	target, err := os.Readlink(filepath.Join(f.cfg.ExtensionsDir, "nex-zfs.raw"))
	if err != nil || !strings.HasPrefix(filepath.Base(target), "sha256-") {
		t.Fatalf("extension link = %q, %v", target, err)
	}
	if !strings.Contains(strings.Join(f.calls, "\n"), "systemctl enable --now zfs.target") {
		t.Fatalf("layer units were not enabled: %v", f.calls)
	}
	// Idempotent: the layer is already merged, so a second call must not merge again.
	f.calls = nil
	if err := f.layers().Ensure(context.Background(), "nex-zfs"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "systemd-sysext") {
			t.Fatalf("second Ensure merged again: %v", f.calls)
		}
	}
}

func TestLayerEnsureRefusesBadLayersWithoutJournalling(t *testing.T) {
	ctx := context.Background()
	t.Run("flipped byte in the store", func(t *testing.T) {
		f := newLayerFixture(t)
		var store string
		entries, _ := os.ReadDir(filepath.Join(f.cfg.Dir, "store"))
		store = filepath.Join(f.cfg.Dir, "store", entries[0].Name())
		b, _ := os.ReadFile(store)
		b[3] ^= 0xff
		if err := os.WriteFile(store, b, 0644); err != nil {
			t.Fatal(err)
		}
		wantReason(t, f.layers().Ensure(ctx, "nex-zfs"), LayerDigestMismatch)
		if _, err := os.Stat(f.layers().statePath()); !os.IsNotExist(err) {
			t.Fatal("a refused layer was journalled as enabled")
		}
	})
	t.Run("layer of another base build", func(t *testing.T) {
		f := newLayerFixture(t)
		f.publish(map[string]any{"id": "nex-zfs", "kind": "layer", "bytes": 5, "requires_base": "20260101.1"}, []byte("other"))
		wantReason(t, f.layers().Ensure(ctx, "nex-zfs"), LayerBaseMismatch)
		for _, c := range f.calls {
			if strings.HasPrefix(c, "systemd-sysext") {
				t.Fatalf("a layer for another base was merged: %v", f.calls)
			}
		}
	})
	t.Run("manifest not signed by the release key", func(t *testing.T) {
		f := newLayerFixture(t)
		dir := filepath.Join(f.cfg.Dir, "releases", f.build)
		f.write(filepath.Join(dir, "manifest.json"), append(f.body, ' '))
		wantReason(t, f.layers().Ensure(ctx, "nex-zfs"), LayerSignatureFailed)
	})
	t.Run("layer missing from the store", func(t *testing.T) {
		f := newLayerFixture(t)
		entries, _ := os.ReadDir(filepath.Join(f.cfg.Dir, "store"))
		os.Remove(filepath.Join(f.cfg.Dir, "store", entries[0].Name()))
		wantReason(t, f.layers().Ensure(ctx, "nex-zfs"), LayerNotInStore)
	})
	t.Run("name the manifest does not offer", func(t *testing.T) {
		f := newLayerFixture(t)
		wantReason(t, f.layers().Ensure(ctx, "nex-cluster"), LayerUnknown)
		wantReason(t, f.layers().Ensure(ctx, "../etc"), LayerUnknown)
	})
}

func TestLayerDisableRefusedWhileInUseThenTakesEffect(t *testing.T) {
	f := newLayerFixture(t)
	ctx := context.Background()
	if err := f.layers().Ensure(ctx, "nex-zfs"); err != nil {
		t.Fatal(err)
	}
	busy := f.layers()
	busy.InUse = func(context.Context, layerMeta) (bool, string) { return true, "kernel module zfs is in use" }
	wantReason(t, busy.Disable(ctx, "nex-zfs"), LayerInUse)
	if !f.state().Layers["nex-zfs"].Enabled {
		t.Fatal("a refused disable cleared the journal")
	}
	if err := f.layers().Disable(ctx, "nex-zfs"); err != nil {
		t.Fatal(err)
	}
	if f.state().Layers["nex-zfs"].Enabled {
		t.Fatal("disable did not record the wish")
	}
	// The merge ends at the next boot, where Activate no longer links the layer.
	f.layers().Activate(ctx)
	if _, err := os.Lstat(filepath.Join(f.cfg.ExtensionsDir, "nex-zfs.raw")); !os.IsNotExist(err) {
		t.Fatalf("disabled layer still linked at boot: %v", err)
	}
}

func TestLayerModuleRefcountMarksInUse(t *testing.T) {
	f := newLayerFixture(t)
	f.write(filepath.Join(f.cfg.SysModule, "zfs", "refcnt"), []byte("2\n"))
	busy, why := NewLayers(f.cfg).inUse(context.Background(), layerMeta{Modules: []string{"zfs"}})
	if !busy || !strings.Contains(why, "zfs") {
		t.Fatalf("inUse = %v %q", busy, why)
	}
}

func TestLayerBusyCommandOutputMarksInUse(t *testing.T) {
	f := newLayerFixture(t)
	pools := ""
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "zpool" {
			return []byte(pools), nil
		}
		return nil, nil
	}
	meta := layerMeta{BusyCommands: [][]string{{"zpool", "list", "-H", "-o", "name"}}}
	l := NewLayers(f.cfg)
	if busy, _ := l.inUse(context.Background(), meta); busy {
		t.Fatal("a host without pools reported the layer in use")
	}
	pools = "tank\n"
	if busy, why := l.inUse(context.Background(), meta); !busy || !strings.Contains(why, "tank") {
		t.Fatalf("inUse = %v %q with an imported pool", busy, why)
	}
}

func TestLayerActivateConvergesAfterCrashAndRefusesTamper(t *testing.T) {
	f := newLayerFixture(t)
	ctx := context.Background()
	// A crash after the journal write and before the link: the wish is recorded, nothing is linked.
	if err := f.layers().Ensure(ctx, "nex-zfs"); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.cfg.ExtensionsDir, "nex-zfs.raw")
	os.Remove(link)
	if got := f.layers().Activate(ctx); got["nex-zfs"] != "" {
		t.Fatalf("verification = %v", got)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("boot did not relink the enabled layer: %v", err)
	}
	// A layer whose store bytes changed is left unmerged and reported; boot is not blocked.
	entries, _ := os.ReadDir(filepath.Join(f.cfg.Dir, "store"))
	store := filepath.Join(f.cfg.Dir, "store", entries[0].Name())
	b, _ := os.ReadFile(store)
	b[0] ^= 1
	os.WriteFile(store, b, 0644)
	if got := f.layers().Activate(ctx); got["nex-zfs"] != LayerDigestMismatch {
		t.Fatalf("verification = %v", got)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("a layer that failed verification stayed linked: %v", err)
	}
	inv, err := f.layers().Inventory(ctx)
	if err != nil || len(inv) != 1 || inv[0].Verified || inv[0].Reason != LayerDigestMismatch || !inv[0].Enabled {
		t.Fatalf("inventory = %#v, %v", inv, err)
	}
}

func TestLayerInventoryOffersSignedLayersWithoutHashing(t *testing.T) {
	f := newLayerFixture(t)
	inv, err := f.layers().Inventory(context.Background())
	if err != nil || len(inv) != 1 {
		t.Fatalf("inventory = %#v, %v", inv, err)
	}
	if got := inv[0]; got.Name != "nex-zfs" || got.Enabled || got.Merged || !got.Available || got.RequiresBase != f.build {
		t.Fatalf("layer = %#v", got)
	}
}

func TestLayerActivateReportsUnreadableJournalWithoutLinking(t *testing.T) {
	f := newLayerFixture(t)
	f.write(filepath.Join(f.cfg.Dir, "enabled.json"), []byte("{not json"))
	got := f.layers().Activate(context.Background())
	if got["state"] == "" {
		t.Fatalf("verification = %v", got)
	}
	if err := f.layers().Ensure(context.Background(), "nex-zfs"); err == nil {
		t.Fatal("Ensure overwrote an unreadable journal")
	}
}
