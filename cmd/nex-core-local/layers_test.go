// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/vmhost"
)

const layerTestBuild = "20261001.1"

// layerTestHost is a host tree with two enabled, signed layers in the store, wired in as the layer manager
// runLayers uses. Nothing here runs a command: the paths exercised are verification and reporting.
type layerTestHost struct {
	cfg   vmhost.LayerConfig
	store map[string]string // layer name -> its store file
}

func newLayerTestHost(t *testing.T) *layerTestHost {
	t.Helper()
	root := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &layerTestHost{store: map[string]string{}}
	h.cfg = vmhost.LayerConfig{
		Dir: filepath.Join(root, "layers"), OSRelease: filepath.Join(root, "os-release"),
		LayerInfoDir: filepath.Join(root, "usr-lib-nex-layers"), ExtensionsDir: filepath.Join(root, "run-extensions"),
		ReportPath: filepath.Join(root, "run", "status.json"), SysModule: filepath.Join(root, "sys-module"),
		Verify: func(manifest, sig []byte) error {
			if !ed25519.Verify(pub, manifest, sig) {
				return errors.New("bad signature")
			}
			return nil
		},
	}
	write := func(path string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0644); err != nil {
			t.Fatal(err)
		}
	}
	marshal := func(v any) []byte {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	write(h.cfg.OSRelease, []byte("BUILD_ID="+layerTestBuild+"\nSYSEXT_LEVEL="+layerTestBuild+"\n"))
	components, enabled := []any{}, map[string]any{}
	for _, name := range []string{"nex-cluster", "nex-zfs"} {
		data := []byte("layer bytes of " + name)
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		components = append(components, map[string]any{"id": name, "kind": "layer", "sha256": digest, "bytes": len(data), "requires_base": layerTestBuild})
		h.store[name] = filepath.Join(h.cfg.Dir, "store", "sha256-"+digest+".raw")
		write(h.store[name], data)
		enabled[name] = map[string]any{"enabled": true, "sha256": digest, "base_build_id": layerTestBuild, "changed": "2026-10-01T00:00:00Z"}
	}
	body := marshal(map[string]any{"components": components})
	release := filepath.Join(h.cfg.Dir, "releases", layerTestBuild)
	write(filepath.Join(release, "manifest.json"), body)
	write(filepath.Join(release, "manifest.sig"), ed25519.Sign(priv, body))
	write(filepath.Join(h.cfg.Dir, "enabled.json"), marshal(map[string]any{"format": 1, "layers": enabled}))
	old := hostLayers
	hostLayers = func() *vmhost.Layers { return vmhost.NewLayers(h.cfg) }
	t.Cleanup(func() { hostLayers = old })
	return h
}

func (h *layerTestHost) tamper(t *testing.T, name string) {
	t.Helper()
	b, err := os.ReadFile(h.store[name])
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 1
	if err = os.WriteFile(h.store[name], b, 0644); err != nil {
		t.Fatal(err)
	}
}

// A tampered layer must never hold boot: activate exits 0, says which layer it refused and why, links only
// the layer that still verifies, and the next list reports both truthfully.
func TestLayersActivateExitsZeroAndReportsTheRefusedLayer(t *testing.T) {
	h := newLayerTestHost(t)
	h.tamper(t, "nex-cluster")
	var out bytes.Buffer
	if err := runLayers([]string{"activate"}, &out); err != nil {
		t.Fatalf("activate failed, which would hold boot: %v", err)
	}
	want := "layer nex-cluster: refused (digest_mismatch); left unmerged\nlayer nex-zfs: verified\n"
	if out.String() != want {
		t.Fatalf("activate printed %q, want %q", out.String(), want)
	}
	if _, err := os.Lstat(filepath.Join(h.cfg.ExtensionsDir, "nex-cluster.raw")); !os.IsNotExist(err) {
		t.Fatalf("the refused layer was linked for the merge: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(h.cfg.ExtensionsDir, "nex-zfs.raw")); err != nil {
		t.Fatalf("the verified layer was not linked: %v", err)
	}

	out.Reset()
	if err := runLayers([]string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	var info []vmhost.LayerInfo
	if err := json.Unmarshal(out.Bytes(), &info); err != nil || len(info) != 2 {
		t.Fatalf("list = %q, %v", out.String(), err)
	}
	if bad, good := info[0], info[1]; bad.Name != "nex-cluster" || bad.Verified || bad.Reason != vmhost.LayerDigestMismatch ||
		good.Name != "nex-zfs" || !good.Verified || !good.Enabled {
		t.Fatalf("list = %+v", info)
	}
}

// ensure reports a refused layer as the stable layer-unavailable error; it takes exactly one name.
func TestLayersEnsureAndDisableNeedOneNameAndReportLayerUnavailable(t *testing.T) {
	h := newLayerTestHost(t)
	h.tamper(t, "nex-cluster")
	var out bytes.Buffer
	err := runLayers([]string{"ensure", "nex-cluster"}, &out)
	var le *vmhost.LayerError
	if !errors.Is(err, vmhost.ErrLayerUnavailable) || !errors.As(err, &le) || le.Reason != vmhost.LayerDigestMismatch {
		t.Fatalf("ensure of a tampered layer = %v", err)
	}
	if err = runLayers([]string{"disable", "Not A Layer"}, &out); !errors.Is(err, vmhost.ErrLayerUnavailable) {
		t.Fatalf("disable of a malformed name = %v", err)
	}
	for _, args := range [][]string{{"ensure"}, {"disable"}, {"ensure", "nex-zfs", "nex-cluster"}} {
		if err = runLayers(args, &out); err == nil || !strings.Contains(err.Error(), "exactly one layer name") {
			t.Fatalf("%v = %v, want a refusal naming the argument rule", args, err)
		}
	}
	for _, args := range [][]string{nil, {"merge"}} {
		if err = runLayers(args, &out); err == nil {
			t.Fatalf("%v was accepted", args)
		}
	}
	if out.Len() != 0 {
		t.Fatalf("refusals printed %q", out.String())
	}
}
