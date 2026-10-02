// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Optional feature layers (nex-zfs, nex-cluster): signed systemd-sysext images that Core verifies,
// links into /run/extensions and merges. This host's Core is the only actor: it ensures a layer as the
// first step of an operation that needs it, refuses to disable one that is in use, and re-verifies every
// enabled layer at boot. The Controller is not involved and stores nothing.
//
// State (host-local, owned by Core): the layer store, /var/lib/nex/layers/enabled.json (the journal of
// what the operator wants) and one signed release manifest per base build under releases/<BUILD_ID>/.
// A layer is bound to exactly one base build by SYSEXT_LEVEL=BUILD_ID; a layer for another base is
// refused, so a rolled-back base never merges the new base's layer.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// LayerConfig names every path the layer manager touches so tests can run on a fixture tree.
type LayerConfig struct {
	Dir           string // /var/lib/nex/layers: store/, enabled.json, releases/<BUILD_ID>/manifest.json{,.sig}
	OSRelease     string // /usr/lib/os-release of the running base
	LayerInfoDir  string // /usr/lib/nex/layers: <name>.json, present while the layer is merged
	ExtensionsDir string // /run/extensions: stock systemd-sysext merges what is linked here
	ReportPath    string // /run/nex/layers/status.json: the last boot verification, volatile
	// Verify authenticates a release manifest (the bytes and their detached signature). The host wires in the
	// update engine's own trust check, so layers and updates have one trust anchor; nil refuses every manifest.
	Verify    func(manifest, sig []byte) error
	SysModule string // /sys/module: module reference counts
}

// DefaultLayerConfig is the production layout.
func DefaultLayerConfig() LayerConfig {
	return LayerConfig{
		Dir: "/var/lib/nex/layers", OSRelease: "/usr/lib/os-release", LayerInfoDir: "/usr/lib/nex/layers",
		ExtensionsDir: "/run/extensions", ReportPath: "/run/nex/layers/status.json",
		SysModule: "/sys/module",
	}
}

// Layer failure reasons. Every refusal reaches the caller as ErrLayerUnavailable so existing ZFS and
// cluster operations report one stable code; the reason says what to fix.
const (
	LayerNotInStore      = "not_in_store"      // the layer file has not been placed on this host
	LayerBaseMismatch    = "base_mismatch"     // the layer was built for another base build
	LayerDigestMismatch  = "digest_mismatch"   // the store file is not the signed bytes
	LayerSignatureFailed = "signature_invalid" // the release manifest is missing or not signed by the release key
	LayerUnknown         = "unknown_layer"     // the signed manifest has no such layer
	LayerMergeFailed     = "merge_failed"      // systemd-sysext refused or the layer did not appear
	LayerRebootRequired  = "reboot_required"   // enabled and verified, but a live refresh was not possible
	LayerInUse           = "in_use"            // disable refused while a unit or module still uses it
)

// ErrLayerUnavailable is the stable failure code ("layer-unavailable") of operations that need a layer.
var ErrLayerUnavailable = errors.New("layer-unavailable")

// LayerError carries the reason; errors.Is(err, ErrLayerUnavailable) holds for all of them.
type LayerError struct {
	Layer, Reason, Detail string
}

func (e *LayerError) Error() string {
	s := "layer-unavailable: " + e.Layer + ": " + e.Reason
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}
func (e *LayerError) Is(target error) bool { return target == ErrLayerUnavailable }

// LayerInfo is one layer as the host reports it.
type LayerInfo struct {
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	Merged       bool     `json:"merged"`
	Available    bool     `json:"available"` // the signed manifest lists it and the store file is present at the signed size
	Verified     bool     `json:"verified"`  // the last boot verification re-hashed it successfully
	SHA256       string   `json:"sha256,omitempty"`
	RequiresBase string   `json:"requires_base,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Provides     []string `json:"provides,omitempty"`
}

// layerMeta is /usr/lib/nex/layers/<name>.json inside a layer.
type layerMeta struct {
	Name             string   `json:"name"`
	RequiresBase     string   `json:"requires_base"`
	UnitsEnable      []string `json:"units_enable"`
	UnitsNeverEnable []string `json:"units_never_enable"`
	Provides         []string `json:"provides"`
	Modules          []string `json:"modules"`    // kernel modules whose reference count marks the layer in use
	BusyUnits        []string `json:"busy_units"` // units whose active state marks the layer in use
	// BusyCommands each list something the layer serves (for example imported pools); non-empty output
	// from any of them marks the layer in use.
	BusyCommands [][]string `json:"busy_commands"`
}

type layerEntry struct {
	Enabled bool   `json:"enabled"`
	SHA256  string `json:"sha256,omitempty"`
	Base    string `json:"base_build_id,omitempty"`
	Changed string `json:"changed"`
}

type layerState struct {
	Format int                   `json:"format"`
	Layers map[string]layerEntry `json:"layers"`
}

type layerReport struct {
	Build    string            `json:"base_build_id"`
	Verified map[string]string `json:"verified"` // name -> "" when good, else the failure reason
}

type layerComponent struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
	RequiresBase string `json:"requires_base"`
}

type layerManifest struct {
	Components []layerComponent `json:"components"`
}

// layersRun executes in a transient system-manager service for the same reason zfsProvisionRun does:
// the agent's own mount namespace must never own a sysext merge that libvirt and every other service
// has to see.
var layersRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := []string{"--quiet", "--wait", "--collect", "--pipe", "--service-type=exec", "--", name}
	return nativeRun(ctx, "systemd-run", append(command, args...)...)
}

var layerName = regexp.MustCompile(`^nex-[a-z0-9]+(-[a-z0-9]+)*$`)
var layerHex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// layerBuild is a base BUILD_ID, <label>-<sequence>; it names a directory, so nothing else is accepted.
var layerBuild = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~-]{0,127}$`)

// Layers is the per-host layer manager.
type Layers struct {
	cfg LayerConfig
	mu  sync.Mutex
	// InUse reports whether a merged layer is serving something. nil uses the layer's own metadata.
	InUse func(ctx context.Context, meta layerMeta) (bool, string)
}

func NewLayers(cfg LayerConfig) *Layers { return &Layers{cfg: cfg} }

func (l *Layers) storePath(sum string) string {
	return filepath.Join(l.cfg.Dir, "store", "sha256-"+sum+".raw")
}
func (l *Layers) statePath() string { return filepath.Join(l.cfg.Dir, "enabled.json") }

func (l *Layers) lock(ctx context.Context) (*os.File, error) {
	if err := os.MkdirAll(l.cfg.Dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(l.cfg.Dir, "layers.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (l *Layers) readState() (layerState, error) {
	s := layerState{Format: 1, Layers: map[string]layerEntry{}}
	b, err := os.ReadFile(l.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &s); err != nil || s.Format != 1 {
		return layerState{}, errors.New("layer state is unreadable; it is not rewritten")
	}
	if s.Layers == nil {
		s.Layers = map[string]layerEntry{}
	}
	return s, nil
}

func (l *Layers) writeState(s layerState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWrite(l.statePath(), append(b, '\n'), 0600)
}

// buildID is the running base's BUILD_ID; SYSEXT_LEVEL must agree because that is what systemd checks.
func (l *Layers) buildID() (string, error) {
	b, err := os.ReadFile(l.cfg.OSRelease)
	if err != nil {
		return "", err
	}
	var build, level string
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		switch k {
		case "BUILD_ID":
			build = v
		case "SYSEXT_LEVEL":
			level = v
		}
	}
	if build == "" || build != level {
		return "", errors.New("base os-release has no matching BUILD_ID and SYSEXT_LEVEL")
	}
	return build, nil
}

// manifest returns the layer components of the signed release manifest of the running base.
func (l *Layers) manifest(build string) (map[string]layerComponent, error) {
	dir := filepath.Join(l.cfg.Dir, "releases", build)
	body, err := readSmall(filepath.Join(dir, "manifest.json"), 1<<20)
	if err != nil {
		return nil, errors.New("signed release manifest unavailable")
	}
	sig, err := readSmall(filepath.Join(dir, "manifest.sig"), 64)
	if err != nil {
		return nil, errors.New("release manifest signature unavailable")
	}
	if l.cfg.Verify == nil {
		return nil, errors.New("no release trust is configured")
	}
	if err := l.cfg.Verify(body, sig); err != nil {
		return nil, fmt.Errorf("release manifest signature verification failed: %w", err)
	}
	var m layerManifest
	if json.Unmarshal(body, &m) != nil {
		return nil, errors.New("release manifest is not valid JSON")
	}
	out := map[string]layerComponent{}
	for _, c := range m.Components {
		if c.Kind == "layer" && layerName.MatchString(c.ID) && layerHex.MatchString(c.SHA256) && c.Bytes > 0 {
			out[c.ID] = c
		}
	}
	return out, nil
}

// resolve proves, for one layer, everything short of the merge: signature, base binding and the
// store file's bytes. hash=false (inventory) only checks the size so a status poll never reads 100 MB.
func (l *Layers) resolve(name string, hash bool) (layerComponent, string, error) {
	if !layerName.MatchString(name) {
		return layerComponent{}, "", &LayerError{Layer: name, Reason: LayerUnknown}
	}
	build, err := l.buildID()
	if err != nil {
		return layerComponent{}, "", &LayerError{Layer: name, Reason: LayerBaseMismatch, Detail: err.Error()}
	}
	comps, err := l.manifest(build)
	if err != nil {
		return layerComponent{}, "", &LayerError{Layer: name, Reason: LayerSignatureFailed, Detail: err.Error()}
	}
	c, ok := comps[name]
	if !ok {
		return c, "", &LayerError{Layer: name, Reason: LayerUnknown}
	}
	if c.RequiresBase != build {
		return c, "", &LayerError{Layer: name, Reason: LayerBaseMismatch, Detail: "built for " + c.RequiresBase + ", running " + build}
	}
	path := l.storePath(c.SHA256)
	f, err := os.Open(path)
	if err != nil {
		return c, path, &LayerError{Layer: name, Reason: LayerNotInStore}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() != c.Bytes {
		return c, path, &LayerError{Layer: name, Reason: LayerDigestMismatch, Detail: "size differs from the signed manifest"}
	}
	if hash {
		h := sha256.New()
		if _, err = io.Copy(h, f); err != nil {
			return c, path, &LayerError{Layer: name, Reason: LayerDigestMismatch, Detail: err.Error()}
		}
		if hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
			return c, path, &LayerError{Layer: name, Reason: LayerDigestMismatch, Detail: "bytes differ from the signed manifest"}
		}
	}
	return c, path, nil
}

func (l *Layers) link(name, target string) error {
	if err := os.MkdirAll(l.cfg.ExtensionsDir, 0755); err != nil {
		return err
	}
	link := filepath.Join(l.cfg.ExtensionsDir, name+".raw")
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func (l *Layers) merged(name string) bool {
	_, err := os.Stat(filepath.Join(l.cfg.LayerInfoDir, name+".json"))
	return err == nil
}

func (l *Layers) meta(name string) (layerMeta, error) {
	var m layerMeta
	b, err := readSmall(filepath.Join(l.cfg.LayerInfoDir, name+".json"), 1<<16)
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(b, &m); err != nil || m.Name != name {
		return m, errors.New("layer metadata does not describe " + name)
	}
	return m, nil
}

func (l *Layers) otherMerged(name string) bool {
	entries, _ := os.ReadDir(l.cfg.LayerInfoDir)
	for _, e := range entries {
		if n := strings.TrimSuffix(e.Name(), ".json"); n != name && n != e.Name() && layerName.MatchString(n) {
			return true
		}
	}
	return false
}

// Ensure makes the layer enabled and merged, idempotently. The order is the journal: the store file is
// proven first, then enabled.json records the wish, then the link and merge. A crash anywhere repeats
// to the same end.
func (l *Layers) Ensure(ctx context.Context, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, err := l.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lf)
	c, path, err := l.resolve(name, true)
	if err != nil {
		return err
	}
	build, _ := l.buildID()
	state, err := l.readState()
	if err != nil {
		return err
	}
	if e := state.Layers[name]; !e.Enabled || e.SHA256 != c.SHA256 || e.Base != build {
		state.Layers[name] = layerEntry{Enabled: true, SHA256: c.SHA256, Base: build, Changed: time.Now().UTC().Format(time.RFC3339)}
		if err = l.writeState(state); err != nil {
			return err
		}
	}
	if err = l.link(name, path); err != nil {
		return err
	}
	if !l.merged(name) {
		verb := "merge"
		if l.otherMerged(name) {
			// Another layer already owns the overlay. Replacing it briefly unmounts /usr, which fails
			// while something holds a file from it; that failure leaves the layer for the next boot.
			verb = "refresh"
		}
		if out, mergeErr := layersRun(ctx, "systemd-sysext", verb); mergeErr != nil {
			return &LayerError{Layer: name, Reason: LayerRebootRequired, Detail: strings.TrimSpace(string(out) + " " + mergeErr.Error())}
		}
		if !l.merged(name) {
			return &LayerError{Layer: name, Reason: LayerMergeFailed, Detail: "systemd-sysext did not expose the layer"}
		}
	}
	meta, err := l.meta(name)
	if err != nil {
		return &LayerError{Layer: name, Reason: LayerMergeFailed, Detail: err.Error()}
	}
	if meta.RequiresBase != build {
		return &LayerError{Layer: name, Reason: LayerBaseMismatch, Detail: "metadata requires " + meta.RequiresBase}
	}
	// A merge happens after systemd's own sysusers/tmpfiles/udev passes at boot; running them again is
	// idempotent and gives a live first enable what boot gives for free.
	for _, step := range [][]string{{"systemd-sysusers"}, {"systemd-tmpfiles", "--create"}, {"udevadm", "control", "--reload"}} {
		if out, runErr := layersRun(ctx, step[0], step[1:]...); runErr != nil {
			return &LayerError{Layer: name, Reason: LayerMergeFailed, Detail: step[0] + ": " + strings.TrimSpace(string(out)+" "+runErr.Error())}
		}
	}
	if _, err = nativeRun(ctx, "systemctl", "daemon-reload"); err != nil {
		return &LayerError{Layer: name, Reason: LayerMergeFailed, Detail: "daemon-reload: " + err.Error()}
	}
	for _, unit := range meta.UnitsEnable {
		if out, runErr := nativeRun(ctx, "systemctl", "enable", "--now", unit); runErr != nil {
			return &LayerError{Layer: name, Reason: LayerMergeFailed, Detail: "enable " + unit + ": " + strings.TrimSpace(string(out)+" "+runErr.Error())}
		}
	}
	return nil
}

func (l *Layers) inUse(ctx context.Context, meta layerMeta) (bool, string) {
	if l.InUse != nil {
		return l.InUse(ctx, meta)
	}
	for _, m := range meta.Modules {
		if b, err := os.ReadFile(filepath.Join(l.cfg.SysModule, m, "refcnt")); err == nil {
			if n, convErr := strconv.Atoi(strings.TrimSpace(string(b))); convErr == nil && n > 0 {
				return true, "kernel module " + m + " is in use"
			}
		}
	}
	for _, unit := range meta.BusyUnits {
		if _, err := nativeRun(ctx, "systemctl", "is-active", "--quiet", unit); err == nil {
			return true, unit + " is active"
		}
	}
	for _, cmd := range meta.BusyCommands {
		if len(cmd) == 0 {
			continue
		}
		if out, err := nativeRun(ctx, cmd[0], cmd[1:]...); err == nil && strings.TrimSpace(string(out)) != "" {
			return true, cmd[0] + " reports " + strings.Join(strings.Fields(string(out)), " ")
		}
	}
	return false, ""
}

// Disable records that the layer is no longer wanted. It is refused while the layer is in use; the
// merge itself ends at the next boot (sysext refresh is not used to drop a layer under running services).
func (l *Layers) Disable(ctx context.Context, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, err := l.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lf)
	if !layerName.MatchString(name) {
		return &LayerError{Layer: name, Reason: LayerUnknown}
	}
	state, err := l.readState()
	if err != nil {
		return err
	}
	e, ok := state.Layers[name]
	if !ok || !e.Enabled {
		return nil
	}
	if meta, metaErr := l.meta(name); metaErr == nil {
		if busy, why := l.inUse(ctx, meta); busy {
			return &LayerError{Layer: name, Reason: LayerInUse, Detail: why}
		}
		for _, unit := range meta.UnitsEnable {
			if out, runErr := nativeRun(ctx, "systemctl", "disable", "--now", unit); runErr != nil {
				return fmt.Errorf("disable %s: %s", unit, strings.TrimSpace(string(out)+" "+runErr.Error()))
			}
		}
	}
	e.Enabled, e.Changed = false, time.Now().UTC().Format(time.RFC3339)
	state.Layers[name] = e
	return l.writeState(state)
}

// Activate runs at boot before systemd-sysext. It links every enabled layer that still verifies and
// removes links for the rest, so the merge that follows leaves them out; a layer already merged stays merged until the
// next boot. A layer that fails is reported; boot never waits on it.
func (l *Layers) Activate(ctx context.Context) map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	report := layerReport{Verified: map[string]string{}}
	report.Build, _ = l.buildID()
	if lf, err := l.lock(ctx); err == nil {
		defer unlock(lf)
	}
	state, err := l.readState()
	if err != nil {
		report.Verified["state"] = err.Error()
	}
	names := make([]string, 0, len(state.Layers))
	for n := range state.Layers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !state.Layers[n].Enabled {
			_ = os.Remove(filepath.Join(l.cfg.ExtensionsDir, n+".raw"))
			continue
		}
		_, path, rerr := l.resolve(n, true)
		if rerr == nil {
			rerr = l.link(n, path)
		}
		if rerr != nil {
			_ = os.Remove(filepath.Join(l.cfg.ExtensionsDir, n+".raw"))
			var le *LayerError
			if errors.As(rerr, &le) {
				report.Verified[n] = le.Reason
			} else {
				report.Verified[n] = rerr.Error()
			}
			continue
		}
		report.Verified[n] = ""
	}
	if b, mErr := json.Marshal(report); mErr == nil && os.MkdirAll(filepath.Dir(l.cfg.ReportPath), 0755) == nil {
		_ = AtomicWrite(l.cfg.ReportPath, append(b, '\n'), 0644)
	}
	return report.Verified
}

// Inventory lists every layer the signed manifest offers plus any recorded one. It re-hashes nothing.
func (l *Layers) Inventory(ctx context.Context) ([]LayerInfo, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, err := l.readState()
	if err != nil {
		return nil, err
	}
	build, _ := l.buildID()
	comps, _ := l.manifest(build)
	var report layerReport
	if b, rerr := readSmall(l.cfg.ReportPath, 1<<16); rerr == nil {
		_ = json.Unmarshal(b, &report)
	}
	names := map[string]bool{}
	for n := range comps {
		names[n] = true
	}
	for n := range state.Layers {
		names[n] = true
	}
	out := make([]LayerInfo, 0, len(names))
	for n := range names {
		info := LayerInfo{Name: n, Enabled: state.Layers[n].Enabled, Merged: l.merged(n), RequiresBase: comps[n].RequiresBase, SHA256: comps[n].SHA256}
		if _, _, rerr := l.resolve(n, false); rerr == nil {
			info.Available = true
		} else {
			var le *LayerError
			if errors.As(rerr, &le) {
				info.Reason = le.Reason
			}
		}
		if reason, ok := report.Verified[n]; ok && report.Build == build {
			info.Verified = reason == ""
			if reason != "" {
				info.Reason = reason
			}
		}
		if meta, merr := l.meta(n); merr == nil {
			info.Provides = meta.Provides
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// StagedLayer is one layer file the update engine has downloaded and authenticated against the signed
// release manifest, waiting in the engine's staging directory.
type StagedLayer struct {
	Name, SHA256, RequiresBase string
	Bytes                      int64
	Path                       string
}

// EnabledNames lists the layers the operator wants merged: the ones an update has to carry along.
func (l *Layers) EnabledNames(ctx context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, err := l.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock(lf)
	state, err := l.readState()
	if err != nil {
		return nil, err
	}
	var names []string
	for n, e := range state.Layers {
		if e.Enabled {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// StageRelease puts the layers of a release, built for base build, into the store and keeps the signed
// manifest under releases/<build> so that base can verify them when it boots. It never touches
// enabled.json (SwitchRelease does, at activation) and never removes anything: the running base's layers
// stay until PruneReleases. A layer built for another base is refused before any byte is written.
func (l *Layers) StageRelease(ctx context.Context, build string, manifest, sig []byte, layers []StagedLayer) error {
	if !layerBuild.MatchString(build) {
		return &LayerError{Layer: build, Reason: LayerBaseMismatch, Detail: "not a base build id"}
	}
	for _, s := range layers {
		if !layerName.MatchString(s.Name) {
			return &LayerError{Layer: s.Name, Reason: LayerUnknown}
		}
		if s.RequiresBase != build {
			return &LayerError{Layer: s.Name, Reason: LayerBaseMismatch, Detail: "built for " + s.RequiresBase + ", release is " + build}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, err := l.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lf)
	for _, s := range layers {
		if err = l.place(s); err != nil {
			return err
		}
	}
	dir := filepath.Join(l.cfg.Dir, "releases", build)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err = AtomicWrite(filepath.Join(dir, "manifest.json"), manifest, 0600); err != nil {
		return err
	}
	return AtomicWrite(filepath.Join(dir, "manifest.sig"), sig, 0600)
}

// place copies one staged file into the store under its digest, hashing as it goes: a file that is not the
// signed bytes never gets the digest's name. A store file that is already right is left alone.
func (l *Layers) place(s StagedLayer) error {
	dst := l.storePath(s.SHA256)
	if sum, err := fileDigest(dst); err == nil && sum == s.SHA256 {
		return nil
	}
	src, err := os.Open(s.Path)
	if err != nil {
		return err
	}
	defer src.Close()
	if err = os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".stage-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), src)
	if err == nil && (n != s.Bytes || hex.EncodeToString(h.Sum(nil)) != s.SHA256) {
		err = &LayerError{Layer: s.Name, Reason: LayerDigestMismatch, Detail: "staged bytes differ from the signed manifest"}
	}
	if err == nil {
		err = tmp.Chmod(0600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), dst); err != nil {
		return err
	}
	if d, derr := os.Open(filepath.Dir(dst)); derr == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SwitchRelease is the activation step: every layer the operator has enabled that the release carries is
// recorded in enabled.json as the release's version, built for the release's base. It runs when the
// release is armed, never at stage, so a staged release changes nothing about the running host.
func (l *Layers) SwitchRelease(ctx context.Context, build string, layers []StagedLayer) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, err := l.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lf)
	state, err := l.readState()
	if err != nil {
		return err
	}
	changed := false
	for _, s := range layers {
		if e, ok := state.Layers[s.Name]; ok && e.Enabled && (e.SHA256 != s.SHA256 || e.Base != build) {
			state.Layers[s.Name] = layerEntry{Enabled: true, SHA256: s.SHA256, Base: build, Changed: time.Now().UTC().Format(time.RFC3339)}
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return l.writeState(state)
}

// PruneReleases removes every release record and store file that no base in keep still uses, which is how
// old layer versions leave once their base can no longer be booted again. Nothing is removed when a kept
// manifest cannot be read: a store file is only dropped on proof that nothing needs it.
func (l *Layers) PruneReleases(ctx context.Context, keep []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	lf, err := l.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lf)
	needed := map[string]bool{}
	kept := map[string]bool{}
	for _, build := range keep {
		kept[build] = true
		body, rerr := readSmall(filepath.Join(l.cfg.Dir, "releases", build, "manifest.json"), 1<<20)
		if errors.Is(rerr, os.ErrNotExist) {
			continue // this base was installed without layers
		}
		var m layerManifest
		if rerr != nil || json.Unmarshal(body, &m) != nil {
			return errors.New("a retained release manifest is unreadable; no layer was removed")
		}
		for _, c := range m.Components {
			if c.Kind == "layer" {
				needed[c.SHA256] = true
			}
		}
	}
	releases, _ := os.ReadDir(filepath.Join(l.cfg.Dir, "releases"))
	for _, r := range releases {
		if !kept[r.Name()] {
			_ = os.RemoveAll(filepath.Join(l.cfg.Dir, "releases", r.Name()))
		}
	}
	files, _ := os.ReadDir(filepath.Join(l.cfg.Dir, "store"))
	for _, f := range files {
		name := f.Name()
		sum := strings.TrimSuffix(strings.TrimPrefix(name, "sha256-"), ".raw")
		if !strings.HasPrefix(name, "sha256-") || !layerHex.MatchString(sum) || !needed[sum] {
			_ = os.Remove(filepath.Join(l.cfg.Dir, "store", name))
		}
	}
	return nil
}
