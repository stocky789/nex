// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/vmhost"
)

// ---- layers travel with the base ----

func (p *publisher) layerComponent(id string, data []byte, requiresBase string, onDemand bool) Component {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	p.write("blobs/sha256-"+digest, data)
	return Component{ID: id, Kind: "layer", SHA256: digest, Bytes: int64(len(data)), URL: "blobs/sha256-" + digest,
		Required: !onDemand, OnDemand: onDemand, RequiresBase: requiresBase}
}

func (p *publisher) addComponents(c ...Component) {
	p.manifest = func(m *Manifest) { m.Components = append(m.Components, c...) }
}

// layerRig is a rig whose host has a layer store with nex-zfs enabled for the running base (base-1).
type layerRig struct {
	*rig
	dir      string
	oldLayer []byte
	oldSHA   string
}

func hexOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newLayerRig(t *testing.T) *layerRig {
	t.Helper()
	r := &layerRig{rig: newRig(t), dir: filepath.Join(t.TempDir(), "layers"), oldLayer: []byte("zfs-for-base-1")}
	r.oldSHA = hexOf(r.oldLayer)
	r.write("store/sha256-"+r.oldSHA+".raw", r.oldLayer)
	old, _ := json.Marshal(map[string]any{"components": []any{map[string]any{"id": "nex-zfs", "kind": "layer", "sha256": r.oldSHA, "bytes": len(r.oldLayer), "requires_base": "base-1"}}})
	r.write("releases/base-1/manifest.json", old)
	enabled, _ := json.Marshal(layerStateFile{Format: 1, Layers: map[string]layerEntryFile{"nex-zfs": {Enabled: true, SHA256: r.oldSHA, Base: "base-1", Changed: "2026-10-01T00:00:00Z"}}})
	r.write("enabled.json", enabled)
	osRelease := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(osRelease, []byte("BUILD_ID=base-1\nSYSEXT_LEVEL=base-1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.write("releases/base-1/manifest.sig", []byte("sig"))
	r.cfg.Layers = vmhost.NewLayers(vmhost.LayerConfig{Dir: r.dir, OSRelease: osRelease, Verify: func(_, _ []byte) error { return nil }})
	r.restart()
	return r
}

// The on-disk enabled.json, as the layer manager writes it.
type layerEntryFile struct {
	Enabled bool   `json:"enabled"`
	SHA256  string `json:"sha256,omitempty"`
	Base    string `json:"base_build_id,omitempty"`
	Changed string `json:"changed"`
}
type layerStateFile struct {
	Format int                       `json:"format"`
	Layers map[string]layerEntryFile `json:"layers"`
}

func (r *layerRig) write(rel string, b []byte) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *layerRig) enabled() layerEntryFile {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, "enabled.json"))
	if err != nil {
		r.t.Fatal(err)
	}
	var s layerStateFile
	if err = json.Unmarshal(b, &s); err != nil {
		r.t.Fatal(err)
	}
	return s.Layers["nex-zfs"]
}

func (r *layerRig) stored(sha string) bool {
	_, err := os.Stat(filepath.Join(r.dir, "store", "sha256-"+sha+".raw"))
	return err == nil
}

func TestLayersStageWithTheBaseAndSwitchOnlyAtActivate(t *testing.T) {
	r := newLayerRig(t)
	newLayer := []byte("zfs-for-v2-2")
	r.pub.addComponents(r.pub.layerComponent("nex-zfs", newLayer, "v2-2", false))
	b2 := r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)

	if !r.stored(hexOf(newLayer)) || !r.stored(r.oldSHA) {
		t.Fatal("a staged release must put its layers in the store beside the running base's")
	}
	got, err := os.ReadFile(filepath.Join(r.dir, "releases", "v2-2", "manifest.json"))
	want, _ := os.ReadFile(filepath.Join(r.pub.dir, "releases", "2", "manifest.json"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("the signed manifest of the new base is not kept byte for byte: %v", err)
	}
	if e := r.enabled(); e.SHA256 != r.oldSHA || e.Base != "base-1" {
		t.Fatalf("stage must not touch enabled.json: %+v", e)
	}
	if _, err = os.Stat(filepath.Join(r.cfg.Dir, "staging", "2", hexOf(newLayer))); err == nil {
		t.Error("the downloaded layer file is kept after it was placed in the store")
	}

	if _, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err != nil {
		t.Fatal(err)
	}
	if e := r.enabled(); e.SHA256 != hexOf(newLayer) || e.Base != "v2-2" || !e.Enabled {
		t.Fatalf("activate must record the new layer for the new base: %+v", e)
	}
	r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, true)
	if st := r.status(); st.Phase != PhaseCommitted {
		t.Fatalf("%+v", st)
	}
	if !r.stored(r.oldSHA) {
		t.Fatal("rollback is open until the next stage: the previous base's layer must stay")
	}

	// The next stage ends the rollback window: the layer of the base before the running one goes.
	r.pub.serial = 2
	r.pub.addComponents(r.pub.layerComponent("nex-zfs", []byte("zfs-for-v3-3"), "v3-3", false))
	r.pub.release(3, "v3", true)
	r.mustApply("req-apply-0002", nil)
	if r.stored(r.oldSHA) {
		t.Error("the layer of a base that can no longer be booted is kept")
	}
	if _, err = os.Stat(filepath.Join(r.dir, "releases", "base-1")); err == nil {
		t.Error("the release record of a base that can no longer be booted is kept")
	}
	if !r.stored(hexOf(newLayer)) || !r.stored(hexOf([]byte("zfs-for-v3-3"))) {
		t.Error("the running base's and the staged base's layers must both be kept")
	}
}

// The base the host was installed with has no layer refs on its record (the engine never staged it); the
// first update records them from the running base's signed manifest, so rolling back switches them back.
func TestRollbackSwitchesTheLayersBack(t *testing.T) {
	r := newLayerRig(t)
	newLayer := []byte("zfs-for-v2-2")
	r.pub.addComponents(r.pub.layerComponent("nex-zfs", newLayer, "v2-2", false))
	b2 := r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err != nil {
		t.Fatal(err)
	}
	if e := r.enabled(); e.SHA256 != hexOf(newLayer) || e.Base != "v2-2" {
		t.Fatalf("activate must switch to the new base's layer: %+v", e)
	}
	r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, true)
	if st := r.status(); st.Phase != PhaseCommitted {
		t.Fatalf("%+v", st)
	}
	if _, _, err := r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-01"}); err != nil {
		t.Fatal(err)
	}
	if e := r.enabled(); e.SHA256 != r.oldSHA || e.Base != "base-1" || !e.Enabled {
		t.Fatalf("rolling back must record the previous base's layer: %+v", e)
	}
}

// An arm that fails after the layers were switched switches them back: the host keeps running the old base.
func TestFailedArmSwitchesTheLayersBack(t *testing.T) {
	r := newLayerRig(t)
	r.pub.addComponents(r.pub.layerComponent("nex-zfs", []byte("zfs-for-v2-2"), "v2-2", false))
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	r.plat.armErr = errors.New("esp write failed")
	if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err == nil {
		t.Fatal("activate did not fail")
	}
	if e := r.enabled(); e.SHA256 != r.oldSHA || e.Base != "base-1" || !e.Enabled {
		t.Fatalf("a failed arm left the layers of the base that is not running: %+v", e)
	}
}

func TestLayerBuiltForAnotherBaseIsRefused(t *testing.T) {
	r := newLayerRig(t)
	r.pub.addComponents(r.pub.layerComponent("nex-zfs", []byte("zfs-for-the-wrong-base"), "v1-1", false))
	r.pub.release(2, "v2", true)
	_, err := r.eng.Check(context.Background())
	wantCode(t, err, "layer_base_mismatch")
	if _, _, err = r.apply("req-apply-0001", nil); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "layer_base_mismatch" || len(r.plat.staged) != 0 {
		t.Fatalf("%+v staged=%v", op, r.plat.staged)
	}

	// The store refuses it too, before writing a byte, even if the engine were handed such a layer.
	l := vmhost.NewLayers(vmhost.LayerConfig{Dir: filepath.Join(t.TempDir(), "layers")})
	file := filepath.Join(t.TempDir(), "zfs")
	os.WriteFile(file, []byte("x"), 0o600)
	err = l.StageRelease(context.Background(), "v2-2", []byte("{}"), []byte("sig"), []vmhost.StagedLayer{{Name: "nex-zfs", SHA256: hexOf([]byte("x")), Bytes: 1, RequiresBase: "v1-1", Path: file}})
	if err == nil || !strings.Contains(err.Error(), vmhost.LayerBaseMismatch) {
		t.Fatalf("%v", err)
	}
}

func TestLayerTamperNeverReachesTheStoreOrTheSlot(t *testing.T) {
	r := newLayerRig(t)
	layer := []byte("zfs-for-v2-2")
	comp := r.pub.layerComponent("nex-zfs", layer, "v2-2", false)
	r.pub.addComponents(comp)
	r.pub.release(2, "v2", true)
	// The mirror serves different bytes under the layer's digest.
	r.pub.write("blobs/sha256-"+comp.SHA256, []byte("zfs-tampered!"))
	if _, _, err := r.apply("req-apply-0001", nil); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "blob_mismatch" {
		t.Fatalf("%+v", op)
	}
	if len(r.plat.staged) != 0 || r.stored(comp.SHA256) {
		t.Fatalf("a tampered layer must stage nothing: staged=%v", r.plat.staged)
	}
	if e := r.enabled(); e.SHA256 != r.oldSHA {
		t.Fatalf("%+v", e)
	}
}

func TestOnDemandLayerIsStagedOnlyWhenEnabled(t *testing.T) {
	r := newLayerRig(t)
	cluster := r.pub.layerComponent("nex-cluster", []byte("cluster-v2"), "v2-2", true)
	zfs := r.pub.layerComponent("nex-zfs", []byte("zfs-v2"), "v2-2", true)
	r.pub.addComponents(cluster, zfs)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	if r.stored(cluster.SHA256) {
		t.Error("an on_demand layer the operator never enabled must not be downloaded")
	}
	if !r.stored(zfs.SHA256) {
		t.Error("an on_demand layer that is enabled follows its base")
	}
}

func TestInterruptedLayerDownloadResumesAndNothingIsHalfStaged(t *testing.T) {
	r := newLayerRig(t)
	layer := []byte(strings.Repeat("zfs-layer-bytes-", 4096))
	comp := r.pub.layerComponent("nex-zfs", layer, "v2-2", false)
	r.pub.addComponents(comp)
	r.pub.release(2, "v2", true)
	cut := &cutOnce{Source: r.src, contains: comp.SHA256, after: int64(len(layer) / 2)}
	r.cfg.Source = func(string) Source { return cut }
	r.restart()
	if _, _, err := r.apply("req-apply-0001", nil); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "source_unreachable" {
		t.Fatalf("%+v", op)
	}
	if len(r.plat.staged) != 0 || r.stored(comp.SHA256) {
		t.Fatal("an interrupted layer download must not stage the slot or fill the store")
	}
	if st := r.status(); st.Phase == PhaseStaged || st.Phase == PhaseArmed {
		t.Fatalf("%+v", st)
	}
	r.mustApply("req-apply-0002", nil)
	if !r.stored(comp.SHA256) || len(r.plat.staged) != 1 {
		t.Fatal("the retry must finish the transaction")
	}
	if cut.resumedAt != int64(len(layer)/2) {
		t.Fatalf("the layer download restarted from %d instead of continuing at %d", cut.resumedAt, len(layer)/2)
	}
}

// cutOnce fails the first read of a blob whose name contains a string after some bytes, then records the
// offset of the next request for it.
type cutOnce struct {
	Source
	contains  string
	after     int64
	cut       bool
	resumedAt int64
}

func (c *cutOnce) Blob(ctx context.Context, name string, offset int64, v string) (io.ReadCloser, bool, string, error) {
	body, ranged, etag, err := c.Source.Blob(ctx, name, offset, v)
	if err != nil || !strings.Contains(name, c.contains) {
		return body, ranged, etag, err
	}
	if c.cut {
		c.resumedAt = offset
		return body, ranged, etag, nil
	}
	c.cut = true
	return &cutReader{ReadCloser: body, left: c.after}, ranged, etag, nil
}

// ---- settings ----

func TestValidateSettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Settings
		code string
	}{
		{"https", Settings{"stable", "https://updates.example.test/base/"}, ""},
		{"explicit http is allowed", Settings{"dev", "http://mirror.example.test:8080"}, ""},
		{"empty base clears", Settings{"stable", ""}, ""},
		{"credentials", Settings{"stable", "https://user:pw@updates.example.test"}, "invalid_base_url"},
		{"query", Settings{"stable", "https://updates.example.test/?token=1"}, "invalid_base_url"},
		{"fragment", Settings{"stable", "https://updates.example.test/#x"}, "invalid_base_url"},
		{"ftp", Settings{"stable", "ftp://updates.example.test"}, "invalid_base_url"},
		{"no host", Settings{"stable", "https:///path"}, "invalid_base_url"},
		{"space", Settings{"stable", "https://updates.example.test/a b"}, "invalid_base_url"},
		{"channel upper case", Settings{"Stable", "https://updates.example.test"}, "invalid_channel"},
		{"channel empty", Settings{"", "https://updates.example.test"}, "invalid_channel"},
		{"channel path", Settings{"../x", "https://updates.example.test"}, "invalid_channel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateSettings(tc.in)
			if tc.code == "" {
				if err != nil || strings.HasSuffix(got.BaseURL, "/") {
					t.Fatalf("%+v %v", got, err)
				}
				return
			}
			wantCode(t, err, tc.code)
		})
	}
}

func TestUpdateSettingsPersistsAndStartsANewPointerHistory(t *testing.T) {
	r := newRig(t)
	conf := filepath.Join(t.TempDir(), "etc", "updates.conf")
	old := UpdatesConfig
	UpdatesConfig = conf
	t.Cleanup(func() { UpdatesConfig = old })
	t.Setenv("NEX_UPDATE_BASE_URL", "")
	t.Setenv("NEX_UPDATE_CHANNEL", "")
	os.MkdirAll(filepath.Dir(conf), 0o755)
	os.WriteFile(conf, []byte("# kept\nother=1\nchannel=stable\nbase_url=https://old.example.test\n"), 0o644)
	r.cfg.Settings = ImageSettings
	r.cfg.Source = func(string) Source { return r.src }
	r.restart()

	r.pub.serial = 5
	r.pub.release(2, "v2", true)
	if _, err := r.eng.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := r.status(); st.ChannelSerial != 5 {
		t.Fatalf("%+v", st)
	}
	got, err := r.eng.UpdateSettings(context.Background(), Settings{Channel: "dev", BaseURL: "http://mirror.example.test:8080/"})
	if err != nil || got.BaseURL != "http://mirror.example.test:8080" {
		t.Fatalf("%+v %v", got, err)
	}
	b, _ := os.ReadFile(conf)
	if string(b) != "# kept\nother=1\nchannel=dev\nbase_url=http://mirror.example.test:8080\n" {
		t.Fatalf("updates.conf = %q", b)
	}
	st := r.status()
	if st.Channel != "dev" || st.BaseURL != "http://mirror.example.test:8080" || st.ChannelSerial != 0 || st.Available != nil {
		t.Fatalf("the status must report the new source and start a new pointer history: %+v", st)
	}
	if st.RollbackFloor != 0 {
		t.Fatalf("%+v", st)
	}
	r.restart()
	if st = r.status(); st.Channel != "dev" {
		t.Fatalf("settings did not survive a restart: %+v", st)
	}

	// Clearing the base URL returns the host to the project's update server.
	if _, err = r.eng.UpdateSettings(context.Background(), Settings{Channel: "dev"}); err != nil {
		t.Fatal(err)
	}
	if base, _ := ImageSettings(); base != DefaultBaseURL {
		t.Fatalf("a cleared base URL must fall back to the default: %q", base)
	}
}

func TestUpdateSettingsRefusals(t *testing.T) {
	r := newRig(t)
	old := UpdatesConfig
	UpdatesConfig = filepath.Join(t.TempDir(), "updates.conf")
	t.Cleanup(func() { UpdatesConfig = old })
	_, err := r.eng.UpdateSettings(context.Background(), Settings{Channel: "dev", BaseURL: "https://u:p@x.example.test"})
	wantCode(t, err, "invalid_base_url")
	if _, statErr := os.Stat(UpdatesConfig); statErr == nil {
		t.Fatal("an invalid setting must not be written")
	}

	t.Setenv("NEX_UPDATE_BASE_URL", "https://pinned.example.test")
	_, err = r.eng.UpdateSettings(context.Background(), Settings{Channel: "dev", BaseURL: "https://other.example.test"})
	wantCode(t, err, "settings_overridden")
	t.Setenv("NEX_UPDATE_BASE_URL", "")

	b2 := r.pub.release(2, "v2", true)
	r.src.gate = make(chan struct{})
	if _, _, err = r.apply("req-apply-0001", nil); err != nil {
		t.Fatal(err)
	}
	_, err = r.eng.UpdateSettings(context.Background(), Settings{Channel: "dev", BaseURL: "https://other.example.test"})
	wantCode(t, err, "update_in_progress")
	close(r.src.gate)
	r.wait()
	_ = b2
}

// settingsRig is a rig whose source is a variable and whose settings file is a recording stub.
func settingsRig(t *testing.T) (r *rig, base *string, saved *[]Settings) {
	r = newRig(t)
	b := "https://old.example.test"
	var s []Settings
	r.cfg.Settings = func() (string, string) { return b, "stable" }
	r.cfg.SaveSettings = func(n Settings) error { s = append(s, n); b = n.BaseURL; return nil }
	r.restart()
	return r, &b, &s
}

func TestSettingsWaitForACheckInFlight(t *testing.T) {
	r, _, saved := settingsRig(t)
	r.pub.serial = 5
	r.pub.release(2, "v2", true)
	var during error
	r.src.onFetch = func(name string) {
		if name == "stable/channel.json" && during == nil {
			// The check has released the engine's mutex to read the source: a settings change now would
			// let it publish the old source's serial and release into the new source's record.
			_, during = r.eng.UpdateSettings(context.Background(), Settings{Channel: "stable", BaseURL: "https://new.example.test"})
		}
	}
	if _, err := r.eng.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantCode(t, during, "update_in_progress")
	if len(*saved) != 0 {
		t.Fatalf("settings were written during a check: %+v", *saved)
	}
	if _, err := r.eng.UpdateSettings(context.Background(), Settings{Channel: "stable", BaseURL: "https://new.example.test"}); err != nil || len(*saved) != 1 {
		t.Fatalf("settings after the check: %v %+v", err, *saved)
	}
}

func TestEachSourceKeepsItsOwnPointerHistory(t *testing.T) {
	r, base, _ := settingsRig(t)
	r.pub.serial = 9
	r.pub.release(2, "v2", true)
	if _, err := r.eng.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A crash between writing updates.conf and resetting the record leaves a new source with the old
	// source's history; a pointer with a lower serial from the new source must still be accepted.
	*base = "https://new.example.test"
	r.advance(2 * time.Minute)
	r.pub.serial = 1
	r.pub.repoint(2)
	if _, err := r.eng.Check(context.Background()); err != nil {
		t.Fatalf("the new source was judged against the old source's history: %v", err)
	}
	// Going back to the old source, its own history still refuses a replay.
	*base = "https://old.example.test"
	r.advance(2 * time.Minute)
	_, err := r.eng.Check(context.Background())
	wantCode(t, err, "channel_replayed")
}

func TestBundleChannelsKeepTheirOwnPointerHistory(t *testing.T) {
	r := newRig(t)
	r.pub.serial = 9
	r.pub.release(2, "v2", true)
	if _, err := r.eng.Check(context.Background()); err != nil { // the host follows stable and has accepted serial 9
		t.Fatal(err)
	}
	r.pub.channel, r.pub.serial = "beta", 1
	r.pub.release(3, "v3", true)
	r.bundle("spring", t0.Add(180*24*time.Hour))
	if _, _, err := r.importFrom("req-import-0001", "inbox/spring", func(q *ImportRequest) { q.Channel = "beta" }); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error != nil || op.Sequence != 3 {
		t.Fatalf("the beta channel was judged against stable's serial: %+v", op)
	}
}

func TestMediaThatIsNotRegularFilesIsRefusedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.json")
	os.WriteFile(outside, []byte(`{}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "stable"), 0o755)
	os.MkdirAll(filepath.Join(dir, "beta"), 0o755)
	os.MkdirAll(filepath.Join(dir, "dev"), 0o755)
	if err := syscall.Mkfifo(filepath.Join(dir, "stable", "channel.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Symlink(outside, filepath.Join(dir, "beta", "channel.json")) // a link leaving the bundle
	os.WriteFile(filepath.Join(dir, "dev", "channel.json"), []byte(`{"sequence":4}`), 0o644)

	done := make(chan struct{})
	var got []MediaChannel
	var fetchErr error
	go func() {
		got = bundleChannels(dir)
		_, fetchErr = DirSource{Root: dir}.Fetch(context.Background(), "stable/channel.json", 1024)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Minute):
		t.Fatal("reading a FIFO on the media blocked")
	}
	if len(got) != 1 || got[0].Name != "dev" || got[0].Sequence != 4 {
		t.Fatalf("only the regular file inside the bundle is a channel: %+v", got)
	}
	wantCode(t, fetchErr, "metadata_invalid")
	_, err := DirSource{Root: dir}.Fetch(context.Background(), "beta/channel.json", 1024)
	wantCode(t, err, "metadata_invalid")
}

// ---- offline bundles ----

// bundle copies the publisher's online layout into the host's inbox as a bundle whose channel pointer has its
// own expiry, the way scripts/update-bundle.py writes one.
func (r *rig) bundle(name string, expires time.Time) string {
	r.t.Helper()
	inbox := r.cfg.InboxDir
	if inbox == "" {
		inbox = filepath.Join(r.t.TempDir(), "inbox")
		r.cfg.InboxDir = inbox
		r.restart()
	}
	dst := filepath.Join(inbox, name)
	if err := os.CopyFS(dst, os.DirFS(r.pub.dir)); err != nil {
		r.t.Fatal(err)
	}
	var c Channel
	b, _ := os.ReadFile(filepath.Join(dst, "stable", "channel.json"))
	if err := json.Unmarshal(b, &c); err != nil {
		r.t.Fatal(err)
	}
	c.Expires = expires
	raw, _ := json.Marshal(c)
	os.WriteFile(filepath.Join(dst, "stable", "channel.json"), raw, 0o644)
	os.WriteFile(filepath.Join(dst, "stable", "channel.sig"), ed25519.Sign(r.pub.relKey, raw), 0o644)
	return dst
}

func (r *rig) importFrom(id, media string, mod func(*ImportRequest)) (Operation, bool, error) {
	req := ImportRequest{RequestID: id, Media: media}
	if mod != nil {
		mod(&req)
	}
	return r.eng.Import(context.Background(), req)
}

func TestMediaListsBundlesWithoutChangingThem(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	dst := r.bundle("spring", t0.Add(180*24*time.Hour))
	noMedia := MediaLabelDevice
	MediaLabelDevice = filepath.Join(t.TempDir(), "no-such-device")
	t.Cleanup(func() { MediaLabelDevice = noMedia })
	media, err := r.eng.Media(context.Background())
	if err != nil || len(media) != 1 || media[0].ID != "inbox/spring" || media[0].Channels[0].Sequence != 2 {
		t.Fatalf("%+v %v", media, err)
	}
	if st := r.status(); len(st.Media) != 1 || st.Media[0].ID != "inbox/spring" {
		t.Fatalf("the scan must be reported in the status: %+v", st.Media)
	}
	if _, err = os.Stat(filepath.Join(dst, "stable", "channel.json")); err != nil {
		t.Fatal(err)
	}
}

func TestImportStagesThroughTheSamePipeline(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.bundle("spring", t0.Add(180*24*time.Hour))
	r.src.down = true // the update server is unreachable: the bundle is the only source
	op, replayed, err := r.importFrom("req-import-0001", "inbox/spring", nil)
	if err != nil || replayed || op.Kind != "import" {
		t.Fatalf("%+v %v %v", op, replayed, err)
	}
	if done := r.wait(); done.Phase != PhaseStaged || done.Error != nil || done.Sequence != 2 {
		t.Fatalf("%+v", done)
	}
	if st := r.status(); st.Phase != PhaseStaged || len(r.plat.armed) != 0 || len(r.plat.staged) != 1 {
		t.Fatalf("import must stage and never arm: %+v", st)
	}
	// An exact replay returns the original operation; a different bundle under the same id is a conflict.
	if _, replayed, err = r.importFrom("req-import-0001", "inbox/spring", nil); err != nil || !replayed {
		t.Fatalf("%v %v", replayed, err)
	}
	_, _, err = r.importFrom("req-import-0001", "inbox", nil)
	wantCode(t, err, "request_conflict")
}

func TestImportRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		prep func(r *rig)
		code string
	}{
		{"unknown media", func(r *rig) {}, "unknown_media"},
		{"bundle channel expired", func(r *rig) {
			r.pub.release(2, "v2", true)
			r.bundle("spring", t0.Add(-time.Hour))
		}, "channel_expired"},
		{"clock earlier than the release was created", func(r *rig) {
			r.pub.release(2, "v2", true)
			r.bundle("spring", t0.Add(180*24*time.Hour))
			r.advance(-time.Hour)
		}, "clock_behind"},
		{"tampered blob", func(r *rig) {
			b := r.pub.release(2, "v2", true)
			dst := r.bundle("spring", t0.Add(180*24*time.Hour))
			raw, _ := os.ReadFile(filepath.Join(dst, "blobs", "sha256-"+b.sha))
			raw[len(raw)/2] ^= 0xff
			os.WriteFile(filepath.Join(dst, "blobs", "sha256-"+b.sha), raw, 0o644)
		}, "blob_mismatch"},
		{"tampered manifest", func(r *rig) {
			r.pub.release(2, "v2", true)
			dst := r.bundle("spring", t0.Add(180*24*time.Hour))
			raw, _ := os.ReadFile(filepath.Join(dst, "releases", "2", "manifest.json"))
			os.WriteFile(filepath.Join(dst, "releases", "2", "manifest.json"), []byte(strings.Replace(string(raw), `"v2"`, `"v9"`, 1)), 0o644)
		}, "manifest_mismatch"},
		{"unsigned pointer", func(r *rig) {
			r.pub.release(2, "v2", true)
			dst := r.bundle("spring", t0.Add(180*24*time.Hour))
			os.WriteFile(filepath.Join(dst, "stable", "channel.sig"), make([]byte, 64), 0o644)
		}, "bad_signature"},
		{"replayed older bundle", func(r *rig) {
			r.pub.serial = 7
			r.pub.release(3, "v3", true)
			if _, err := r.eng.Check(context.Background()); err != nil { // the host has accepted serial 7
				t.Fatal(err)
			}
			r.pub.serial = 2
			r.pub.release(2, "v2", true)
			r.bundle("old", t0.Add(180*24*time.Hour))
		}, "channel_replayed"},
		{"bundle below the rollback floor", func(r *rig) {
			r.pub.minSeq = 9
			r.pub.serial = 3
			r.pub.release(3, "v3", true)
			r.bundle("floor", t0.Add(180*24*time.Hour))
		}, "below_rollback_floor"},
		{"layer for another base", func(r *rig) {
			r.pub.addComponents(r.pub.layerComponent("nex-zfs", []byte("zfs"), "v1-1", false))
			r.pub.release(2, "v2", true)
			r.bundle("spring", t0.Add(180*24*time.Hour))
		}, "layer_base_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			tc.prep(r)
			media := "inbox/spring"
			if tc.name == "replayed older bundle" {
				media = "inbox/old"
			}
			if tc.name == "bundle below the rollback floor" {
				media = "inbox/floor"
			}
			if _, _, err := r.importFrom("req-import-0001", media, nil); err != nil {
				wantCode(t, err, tc.code)
				return
			}
			op := r.wait()
			if op.Error == nil || op.Error.Code != tc.code {
				t.Fatalf("%+v, want %s", op, tc.code)
			}
			if len(r.plat.staged) != 0 || r.status().Phase == PhaseStaged {
				t.Fatal("a refused bundle must leave the host as it was")
			}
		})
	}
}

func TestInterruptedImportResumesFromTheSameRequest(t *testing.T) {
	r := newRig(t)
	b := r.pub.release(2, "v2", true)
	r.bundle("spring", t0.Add(180*24*time.Hour))
	if _, _, err := r.importFrom("req-import-0001", "inbox/spring", nil); err != nil {
		t.Fatal(err)
	}
	r.wait()
	// A crash while the bundle was being read: the host restarts and the same request goes on.
	r.eng.mu.Lock()
	r.eng.st.Staged, r.eng.st.Phase = nil, PhaseIdle
	for i := range r.eng.st.Operations {
		r.eng.st.Operations[i].Op.FinishedAt, r.eng.st.Operations[i].Op.Phase = nil, PhaseDownloading
	}
	_ = r.eng.save()
	r.eng.mu.Unlock()
	r.plat.staged = nil
	r.restart()
	// A different request is refused until the interrupted one is resumed or cancelled.
	_, _, err := r.importFrom("req-import-0002", "inbox/spring", nil)
	wantCode(t, err, "update_in_progress")
	if _, replayed, err := r.importFrom("req-import-0001", "inbox/spring", nil); err != nil || !replayed {
		t.Fatalf("%v %v", replayed, err)
	}
	if op := r.wait(); op.Error != nil || op.Phase != PhaseStaged {
		t.Fatalf("%+v", op)
	}
	if len(r.plat.staged) != 1 || r.status().Slots[len(r.status().Slots)-1].RootHash != b.rootHash {
		t.Fatalf("%v", r.plat.staged)
	}
}

func TestImportNeedsAChannelWhenTheBundleHasSeveral(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	dst := r.bundle("spring", t0.Add(180*24*time.Hour))
	if err := os.CopyFS(filepath.Join(dst, "testing"), os.DirFS(filepath.Join(dst, "stable"))); err != nil {
		t.Fatal(err)
	}
	r.cfg.Settings = func() (string, string) { return "https://updates.example.test", "dev" }
	r.restart()
	if _, _, err := r.importFrom("req-import-0001", "inbox/spring", nil); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "channel_ambiguous" {
		t.Fatalf("%+v", op)
	}
	if _, _, err := r.importFrom("req-import-0002", "inbox/spring", func(q *ImportRequest) { q.Channel = "stable" }); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error != nil {
		t.Fatalf("%+v", op)
	}
}

// ---- on_demand payloads ----

func payloadRig(t *testing.T) (*rig, Component, []byte) {
	t.Helper()
	r := newRig(t)
	r.cfg.PayloadDir = filepath.Join(t.TempDir(), "payload")
	r.restart()
	data := []byte(strings.Repeat("controller-appliance-", 1000))
	digest := hexOf(data)
	r.pub.write("blobs/sha256-"+digest, data)
	comp := Component{ID: "controller", Kind: "vm-image", SHA256: digest, Bytes: int64(len(data)), URL: "blobs/sha256-" + digest, OnDemand: true, Name: "controller.qcow2"}
	r.pub.addComponents(comp)
	return r, comp, data
}

func (r *rig) fetch(id, component string, mod func(*FetchRequest)) (Operation, bool, error) {
	req := FetchRequest{RequestID: id}
	if mod != nil {
		mod(&req)
	}
	return r.eng.FetchComponent(context.Background(), component, req)
}

func TestFetchPlacesAnOnDemandPayloadByDigestWithoutDisturbingAStagedRelease(t *testing.T) {
	r, comp, data := payloadRig(t)
	r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", nil)
	if _, _, err := r.fetch("req-fetch-0001", "controller", nil); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Kind != "fetch" || op.Phase != "fetched" || op.Error != nil {
		t.Fatalf("%+v", op)
	}
	got, err := os.ReadFile(filepath.Join(r.cfg.PayloadDir, comp.SHA256, "controller.qcow2"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("payload not placed: %v", err)
	}
	if st := r.status(); st.Phase != PhaseStaged || len(st.Slots) < 2 {
		t.Fatalf("a payload fetch must leave the staged release alone: %+v", st)
	}
	// Replay returns the original operation; the next fetch finds the file and does not download again.
	if _, replayed, err := r.fetch("req-fetch-0001", "controller", nil); err != nil || !replayed {
		t.Fatalf("%v %v", replayed, err)
	}
	r.src.mu.Lock()
	r.src.offsets = nil
	r.src.mu.Unlock()
	if _, _, err = r.fetch("req-fetch-0002", "controller", nil); err != nil {
		t.Fatal(err)
	}
	r.wait()
	if len(r.src.offsets) != 0 {
		t.Fatalf("a payload that is already in place was downloaded again: %v", r.src.offsets)
	}
}

func TestFetchRefusals(t *testing.T) {
	r, comp, _ := payloadRig(t)
	layer := r.pub.layerComponent("nex-zfs", []byte("zfs-v2"), "v2-2", true)
	r.pub.manifest = func(m *Manifest) { m.Components = append(m.Components, comp, layer) }
	r.pub.release(2, "v2", true)
	for _, tc := range []struct{ component, code string }{
		{"nope", "unknown_component"},
		{"base", "not_on_demand"},
		{"nex-zfs", "not_on_demand"},
	} {
		if _, _, err := r.fetch("req-fetch-"+tc.component, tc.component, nil); err != nil {
			t.Fatal(err)
		}
		if op := r.wait(); op.Error == nil || op.Error.Code != tc.code {
			t.Fatalf("%s: %+v", tc.component, op)
		}
	}
	_, _, err := r.fetch("req-fetch-bad1", "../etc", nil)
	wantCode(t, err, "invalid_request")

	// A tampered payload is never placed.
	r.pub.write("blobs/sha256-"+comp.SHA256, make([]byte, comp.Bytes))
	if _, _, err = r.fetch("req-fetch-tamper", "controller", nil); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "blob_mismatch" {
		t.Fatalf("%+v", op)
	}
	if _, err = os.Stat(filepath.Join(r.cfg.PayloadDir, comp.SHA256)); err == nil {
		t.Fatal("a payload that failed its hash was placed")
	}
}

func TestFetchFromABundle(t *testing.T) {
	r, comp, _ := payloadRig(t)
	r.pub.release(2, "v2", true)
	r.bundle("full", t0.Add(180*24*time.Hour))
	r.src.down = true
	if _, _, err := r.fetch("req-fetch-0001", "controller", func(q *FetchRequest) { q.Media = "inbox/full" }); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error != nil {
		t.Fatalf("%+v", op)
	}
	if _, err := os.Stat(filepath.Join(r.cfg.PayloadDir, comp.SHA256, "controller.qcow2")); err != nil {
		t.Fatal(err)
	}
}
