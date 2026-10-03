// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/vmhost"
)

// layerTrustRig is a host tree whose layer manager authenticates the stored release manifest with the
// engine's trust chain (VerifyReleaseManifest), the way HostLayers wires it.
type layerTrustRig struct {
	t        *testing.T
	cfg      Config
	layers   *vmhost.Layers
	rootPub  ed25519.PublicKey
	rootKey  ed25519.PrivateKey
	relPub   ed25519.PublicKey
	relKey   ed25519.PrivateKey
	now      time.Time
	trustDir string
}

func newLayerTrustRig(t *testing.T) *layerTrustRig {
	t.Helper()
	dir := t.TempDir()
	r := &layerTrustRig{t: t, now: t0, trustDir: filepath.Join(dir, "update")}
	r.rootPub, r.rootKey = keyPair(t)
	r.relPub, r.relKey = keyPair(t)
	r.cfg = Config{Dir: filepath.Join(dir, "image"), TrustDir: r.trustDir, PinnedTrust: filepath.Join(dir, "trust.json"), Now: func() time.Time { return r.now }}
	r.pin(TrustDoc{Format: 1, Version: 1, Keys: []TrustKey{
		NewTrustKey("root-1", "root", r.rootPub, t0.Add(365*24*time.Hour)),
		NewTrustKey("rel-1", "release", r.relPub, t0.Add(30*24*time.Hour)),
	}})

	const build, name = "base-7", "nex-zfs"
	layer := []byte("layer bytes")
	sum := sha256.Sum256(layer)
	manifest, err := json.Marshal(Manifest{Format: 1, Sequence: 7, Label: "base", Architecture: Architecture, Created: t0,
		Components: []Component{{ID: name, Kind: "layer", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(layer)), URL: "layers/" + name, RequiresBase: build}}})
	if err != nil {
		t.Fatal(err)
	}
	lcfg := vmhost.LayerConfig{
		Dir: filepath.Join(dir, "layers"), OSRelease: filepath.Join(dir, "os-release"), ReportPath: filepath.Join(dir, "status.json"),
		LayerInfoDir: filepath.Join(dir, "info"), ExtensionsDir: filepath.Join(dir, "extensions"), SysModule: filepath.Join(dir, "sys"),
		Verify: func(manifest, sig []byte) error { return VerifyReleaseManifest(r.cfg, manifest, sig) },
	}
	r.write(lcfg.OSRelease, []byte("BUILD_ID="+build+"\nSYSEXT_LEVEL="+build+"\n"))
	r.write(filepath.Join(lcfg.Dir, "releases", build, "manifest.json"), manifest)
	r.write(filepath.Join(lcfg.Dir, "releases", build, "manifest.sig"), ed25519.Sign(r.relKey, manifest))
	r.write(filepath.Join(lcfg.Dir, "store", "sha256-"+hex.EncodeToString(sum[:])+".raw"), layer)
	r.layers = vmhost.NewLayers(lcfg)
	return r
}

func (r *layerTrustRig) write(path string, b []byte) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *layerTrustRig) pin(d TrustDoc) {
	raw, err := json.Marshal(d)
	if err != nil {
		r.t.Fatal(err)
	}
	r.write(r.cfg.PinnedTrust, raw)
}

// rotate persists the next trust document, signed by the root key, the way the engine adopts one.
func (r *layerTrustRig) rotate(version int, keys ...TrustKey) {
	raw, err := json.Marshal(TrustDoc{Format: 1, Version: version, Keys: keys})
	if err != nil {
		r.t.Fatal(err)
	}
	base := filepath.Join(r.trustDir, fmt.Sprintf("trust.%d", version))
	r.write(base+".json", raw)
	r.write(base+".sig", ed25519.Sign(r.rootKey, raw))
}

// offered reports whether the signed manifest vouches for the layer: Inventory lists only layers of a manifest
// that verified, and Ensure names why one that did not was refused.
func (r *layerTrustRig) offered() (bool, string) {
	r.t.Helper()
	list, err := r.layers.Inventory(r.t.Context())
	if err != nil {
		r.t.Fatal(err)
	}
	if len(list) == 1 && list[0].Name == "nex-zfs" && list[0].Available {
		return true, ""
	}
	var le *vmhost.LayerError
	if err := r.layers.Ensure(r.t.Context(), "nex-zfs"); !errors.As(err, &le) {
		r.t.Fatalf("Ensure = %v, want a layer refusal", err)
	}
	return false, le.Reason
}

func TestLayerAcceptedWhenReleaseKeyIsInTheTrustInForce(t *testing.T) {
	r := newLayerTrustRig(t)
	if ok, reason := r.offered(); !ok {
		t.Fatalf("layer refused (%s) although its release is signed by a trusted release key", reason)
	}
}

func TestLayerRefusedWhenReleaseKeyIsExpiredRevokedOrRotatedOut(t *testing.T) {
	for name, mutate := range map[string]func(r *layerTrustRig){
		"release key past not_after": func(r *layerTrustRig) { r.now = t0.Add(31 * 24 * time.Hour) },
		"release key revoked in the pinned trust": func(r *layerTrustRig) {
			rel := NewTrustKey("rel-1", "release", r.relPub, t0.Add(30*24*time.Hour))
			rel.Revoked = true
			r.pin(TrustDoc{Format: 1, Version: 1, Keys: []TrustKey{NewTrustKey("root-1", "root", r.rootPub, t0.Add(365*24*time.Hour)), rel}})
		},
		"release key replaced by a persisted rotation": func(r *layerTrustRig) {
			newPub, _ := keyPair(t)
			r.rotate(2, NewTrustKey("root-1", "root", r.rootPub, t0.Add(365*24*time.Hour)), NewTrustKey("rel-2", "release", newPub, t0.Add(365*24*time.Hour)))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLayerTrustRig(t)
			mutate(r)
			if ok, reason := r.offered(); ok || reason != vmhost.LayerSignatureFailed {
				t.Fatalf("available = %v, reason = %q; want a refusal with %s", ok, reason, vmhost.LayerSignatureFailed)
			}
		})
	}
}

func TestLayerRefusedWhenThePersistedTrustChainIsBroken(t *testing.T) {
	r := newLayerTrustRig(t)
	forged, forgedKey := keyPair(t)
	raw, _ := json.Marshal(TrustDoc{Format: 1, Version: 2, Keys: []TrustKey{
		NewTrustKey("root-x", "root", forged, t0.Add(365*24*time.Hour)), NewTrustKey("rel-x", "release", r.relPub, t0.Add(365*24*time.Hour))}})
	r.write(filepath.Join(r.trustDir, "trust.2.json"), raw)
	r.write(filepath.Join(r.trustDir, "trust.2.sig"), ed25519.Sign(forgedKey, raw))
	if ok, reason := r.offered(); ok || reason != vmhost.LayerSignatureFailed {
		t.Fatalf("available = %v, reason = %q; a trust document not signed by a trusted root must fail closed", ok, reason)
	}
}
