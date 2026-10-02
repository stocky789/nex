// SPDX-License-Identifier: AGPL-3.0-only

package nexupdate

// The image-edition update engine: authenticated metadata, resumable downloads, slot staging on every
// disk, arming of the boot entry, rollback. Core owns all of it and keeps its record in the "update"
// subtree of /var/lib/nex/image/state.json; the disks (slot GUIDs and UKI names on every ESP) remain
// the truth, so a lost cache is rebuilt by Reconcile. See docs/plans/2026-10-01-immutable-nex-core.md,
// "Update pipeline, API and pool-safe rollout".

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/hostimage"
	"nex.local/nex/internal/vmhost"
)

// Lifecycle phases of the update subtree (the plan's host pipeline table) plus the two resting ones.
const (
	PhaseIdle        = "idle"
	PhaseChecking    = "checking"
	PhaseDownloading = "downloading"
	PhaseWriting     = "writing"
	PhaseStaged      = "staged"
	PhaseArmed       = "armed"
	PhaseBooted      = "booted"
	PhaseCommitted   = "committed"
	PhaseRolledBack  = "rolled_back"
	PhaseSwapping    = "swapping" // a Controller appliance swap (scope controller); never a host lifecycle phase
)

const (
	checkThrottle  = 60 * time.Second
	keepOperations = 20
	maxTrustFile   = 64 << 10
	maxExtract     = 16 << 30
	maxZstdWindow  = 64 << 20
	maxZstdMemory  = 1 << 30
)

// lockWait is how long a mutating call retries a busy update lock before update_in_progress (a variable
// so tests need not wait it out).
var lockWait = 2 * time.Second

var requestIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{7,63}$`)

// Booted is the running image.
type Booted struct {
	RootHash string
	Label    string
	Sequence int
	// The facts the running image states about itself (os-release): what a release record needs
	// when the engine has to recover it from the disk instead of a signed manifest.
	StateFormat    int
	MinStateFormat int
	MDGeneration   int
	MDFallbackSafe bool
}

func recordOfBooted(b Booted) ReleaseRecord {
	return ReleaseRecord{Label: b.Label, Sequence: b.Sequence, RootHash: b.RootHash, StateFormat: b.StateFormat,
		MinStateFormat: b.MinStateFormat, MDGeneration: b.MDGeneration, MDFallbackSafe: b.MDFallbackSafe}
}

// Platform is what the engine needs from the host: the running image, the slot writer and the boot
// entries. HostPlatform is the real one; tests substitute fakes and file-backed disks.
type Platform interface {
	Booted(ctx context.Context) (Booted, error)
	// Verify authenticates an extracted release directory (hashes, roothash, hash tree).
	Verify(dir string) (*hostimage.Release, error)
	// Stage writes the release into the inactive slot of every disk and returns the slot letter.
	Stage(ctx context.Context, rel *hostimage.Release, booted Booted) (string, error)
	Arm(ctx context.Context, rel *hostimage.Release) error
	Rollback(ctx context.Context, from, to ReleaseRecord) error
	Entries(ctx context.Context) ([]hostimage.UKIEntry, error)
}

// Config is everything an Engine needs; zero fields get the host defaults of HostConfig.
type Config struct {
	Dir         string // /var/lib/nex/image: state.json and staging/
	TrustDir    string // /var/lib/nex/update: rotated trust documents
	PinnedTrust string // /usr/share/nex/update/trust.json in the verity root
	Root        string // "/", where RequiredStateFormat looks for retained state
	// Settings returns the update base URL and channel; the base URL is configuration only.
	Settings func() (baseURL, channel string)
	// Source builds the metadata and blob source for a base URL.
	Source     func(baseURL string) Source
	Platform   Platform
	RunningVMs func(ctx context.Context) ([]string, error)
	Reboot     func(ctx context.Context) error
	Now        func() time.Time
	// Layers is the host's layer store: the layers of a release are placed beside the base in the same
	// transaction. nil on a host (or test) that has none; a release that needs one is then refused.
	Layers *vmhost.Layers
	// Appliance replaces the Controller appliance's system image (the host's vmhost engine). nil on a
	// host (or test) without one: scope "controller" is then refused scope_unavailable.
	Appliance Appliance
	// ControllerProbe answers whether the Controller's readiness path responds at its management URL.
	ControllerProbe func(ctx context.Context, url string) error
	// PayloadDir receives on_demand components, one directory per SHA-256 (/var/lib/nex/payload).
	PayloadDir string
	// InboxDir holds offline bundles in the online layout (/var/lib/nex/image/inbox).
	InboxDir string
	// Mount makes labelled update media readable and returns its directory with a release function; the
	// engine never writes to media.
	Mount func(ctx context.Context, device string) (dir string, done func(), err error)
	// SaveSettings persists the update settings (/etc/nex/updates.conf).
	SaveSettings func(Settings) error
	// RebootDelay lets the HTTP response out before the host goes down.
	RebootDelay time.Duration
}

// ReleaseRecord is what the engine remembers about one release, copied from its manifest.
type ReleaseRecord struct {
	Label          string `json:"label"`
	Sequence       int    `json:"sequence"`
	RootHash       string `json:"root_hash"`
	Slot           string `json:"slot,omitempty"`
	StateFormat    int    `json:"state_format"`
	MinStateFormat int    `json:"min_state_format"`
	MDGeneration   int    `json:"md_generation,omitempty"`
	MDFallbackSafe bool   `json:"md_fallback_safe,omitempty"`
	Rollback       bool   `json:"rollback,omitempty"` // the armed record is a revert
	// Layers are the layer files that came with this release; they are switched in when it is armed.
	Layers []LayerRef `json:"layers,omitempty"`
}

// Available is a verified newer release.
type Available struct {
	Label      string          `json:"label"`
	Sequence   int             `json:"sequence"`
	Components []ComponentInfo `json:"components"`
	Compat     Compat          `json:"compat"`
}

// ComponentInfo is a component as the API reports it.
type ComponentInfo struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Bytes    int64  `json:"bytes"`
	Required bool   `json:"required"`
	OnDemand bool   `json:"on_demand,omitempty"`
}

// OpProgress is the progress of the current component.
type OpProgress struct {
	Component  string `json:"component,omitempty"`
	BytesDone  int64  `json:"bytes_done"`
	BytesTotal int64  `json:"bytes_total"`
}

// Operation is UpdateOperation: its ID is the request_id that created it.
type Operation struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"` // apply, activate, rollback, cancel
	Phase      string      `json:"phase"`
	Progress   *OpProgress `json:"progress,omitempty"`
	Sequence   int         `json:"sequence,omitempty"`
	Error      *Error      `json:"error,omitempty"`
	StartedAt  time.Time   `json:"started_at"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
}

type opRecord struct {
	Op     Operation `json:"op"`
	Digest string    `json:"digest"`
}

type updateState struct {
	Channel string `json:"channel,omitempty"`
	// Replay is the newest channel pointer accepted from each source (see replayKey). A channel_serial
	// counts one publisher's channel, so the history belongs to the source and never mixes.
	Replay        map[string]replayPoint `json:"replay,omitempty"`
	RollbackFloor int                    `json:"rollback_floor"`
	TrustVersion  int                    `json:"trust_version,omitempty"` // newest trust document adopted; never goes back
	CheckedAt     time.Time              `json:"checked_at,omitempty"`
	Available     *Available             `json:"available,omitempty"`
	Phase         string                 `json:"phase"`
	Current       *ReleaseRecord         `json:"current,omitempty"`
	Previous      *ReleaseRecord         `json:"previous,omitempty"`
	Staged        *ReleaseRecord         `json:"staged,omitempty"`
	Armed         *ReleaseRecord         `json:"armed,omitempty"`
	Failure       *Error                 `json:"failure,omitempty"`
	Operations    []opRecord             `json:"operations,omitempty"`
	Media         []Media                `json:"media,omitempty"` // the last scan of offline bundles
}

// SlotStatus is one slot as the engine knows it.
type SlotStatus struct {
	Slot     string `json:"slot,omitempty"`
	Role     string `json:"role"` // booted, previous, staged, armed
	Label    string `json:"label"`
	Sequence int    `json:"sequence"`
	RootHash string `json:"root_hash"`
}

// ImageStatus is UpdateStatus.
type ImageStatus struct {
	Running       bool         `json:"running"`
	Slots         []SlotStatus `json:"slots"`
	Channel       string       `json:"channel"`
	Available     *Available   `json:"available,omitempty"`
	Phase         string       `json:"phase"`
	RollbackFloor int          `json:"rollback_floor"`
	StateFormat   int          `json:"state_format"`
	CheckedAt     *time.Time   `json:"checked_at,omitempty"`
	Operation     *Operation   `json:"operation,omitempty"`
	Failure       *Error       `json:"failure,omitempty"`
	// ChannelSerial is the newest pointer accepted from the configured source and channel; it only grows.
	ChannelSerial int     `json:"channel_serial"`
	BaseURL       string  `json:"base_url,omitempty"`
	Media         []Media `json:"media,omitempty"`
}

// ApplyRequest is POST /updates/apply.
type ApplyRequest struct {
	RequestID  string `json:"request_id"`
	Sequence   int    `json:"sequence,omitempty"`
	Scope      string `json:"scope,omitempty"`    // host (default) or controller
	Activate   string `json:"activate,omitempty"` // stage_only (default) or reboot
	RunningVMs string `json:"running_vms,omitempty"`
	// kind is apply, import or fetch; media, channel and component carry what Import and FetchComponent
	// ask for. They are not part of the apply request body.
	kind, media, channel, component string
}

// ActionRequest is the body of activate, rollback and cancel.
type ActionRequest struct {
	RequestID  string `json:"request_id"`
	RunningVMs string `json:"running_vms,omitempty"` // refuse (default) or shutdown
}

// Engine is the image-edition update engine of one host.
type Engine struct {
	cfg Config

	mu        sync.Mutex
	raw       map[string]json.RawMessage
	st        updateState
	loaded    bool
	active    *activeOp
	lastCheck time.Time
	// checks counts Checks in flight: a Check releases mu while it reads the source, so a settings change
	// must wait for it.
	checks int
	// rest is the lifecycle phase to return to when an apply fails (in memory: after a restart it is idle).
	rest   string
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// lockMu guards the cross-process update lock (see enter). Lock order: mu, then lockMu.
	lockMu sync.Mutex
	hold   int      // enters not yet left
	flock  *os.File // the update.lock descriptor while hold > 0
	stale  bool     // the lock was just taken: reload the record before using the cache
}

type activeOp struct {
	id     string
	cancel context.CancelFunc
	done   chan struct{}
	phase  string
	owns   bool // the operation holds one enter of the update lock
}

// NewEngine builds an engine; Close stops background work.
func NewEngine(cfg Config) *Engine {
	d := HostConfig()
	if cfg.Dir == "" {
		cfg.Dir = d.Dir
	}
	if cfg.TrustDir == "" {
		cfg.TrustDir = d.TrustDir
	}
	if cfg.PinnedTrust == "" {
		cfg.PinnedTrust = d.PinnedTrust
	}
	if cfg.Root == "" {
		cfg.Root = "/"
	}
	if cfg.PayloadDir == "" {
		cfg.PayloadDir = d.PayloadDir
	}
	if cfg.Settings == nil {
		cfg.Settings = ImageSettings
	}
	if cfg.Source == nil {
		cfg.Source = func(base string) Source { return HTTPSource{Base: base} }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RebootDelay == 0 {
		cfg.RebootDelay = 3 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{cfg: cfg, ctx: ctx, cancel: cancel}
}

// Close cancels background work and waits for it.
func (e *Engine) Close() {
	e.cancel()
	e.wg.Wait()
}

// ---- state file ----

func (e *Engine) statePath() string { return filepath.Join(e.cfg.Dir, "state.json") }

// load reads the update subtree once; the rest of state.json belongs to other writers and is kept.
func (e *Engine) load() error {
	if e.loaded && e.cacheValid() {
		return nil
	}
	e.st = updateState{}
	e.raw = map[string]json.RawMessage{}
	b, err := os.ReadFile(e.statePath())
	if err == nil {
		if err = json.Unmarshal(b, &e.raw); err != nil {
			return fmt.Errorf("%s: %w", e.statePath(), err)
		}
		if u, ok := e.raw["update"]; ok {
			if err = json.Unmarshal(u, &e.st); err != nil {
				return fmt.Errorf("%s: update: %w", e.statePath(), err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if e.st.Phase == "" {
		e.st.Phase = PhaseIdle
	}
	e.loaded = true
	return nil
}

func (e *Engine) save() error {
	u, err := json.Marshal(e.st)
	if err != nil {
		return err
	}
	e.raw["update"] = u
	b, err := json.MarshalIndent(e.raw, "", " ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(e.cfg.Dir, 0o700); err != nil {
		return err
	}
	return writeFileAtomic(e.statePath(), append(b, '\n'), 0o600)
}

func writeFileAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	if d, derr := os.Open(filepath.Dir(path)); derr == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

func (e *Engine) stagingDir(sequence int) string {
	return filepath.Join(e.cfg.Dir, "staging", fmt.Sprint(sequence))
}

// ---- update lock ----

// enter takes the host-wide update lock, an flock on update.lock beside state.json, unless this process
// already holds it. The state file has one writer at a time across processes (the service and
// nex-update); wait bounds how long a busy lock is retried before update_in_progress. Each enter is
// paired with one leave; an Apply that starts hands its enter to the running operation.
func (e *Engine) enter(wait time.Duration) error {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.hold == 0 {
		if err := os.MkdirAll(e.cfg.Dir, 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(e.cfg.Dir, "update.lock"), os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return err
		}
		deadline := time.Now().Add(wait)
		for {
			if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
				break
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) {
				f.Close()
				return err
			}
			if !time.Now().Before(deadline) {
				f.Close()
				return refuse(409, "update_in_progress", "another process is updating this host")
			}
			time.Sleep(50 * time.Millisecond)
		}
		e.flock = f
		e.stale = true // whatever this engine cached may have been rewritten by the previous holder
	}
	e.hold++
	return nil
}

func (e *Engine) leave() {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.hold--; e.hold == 0 && e.flock != nil {
		e.flock.Close() // closing the descriptor drops the flock
		e.flock = nil
	}
}

func isBusy(err error) bool {
	var re *Error
	return errors.As(err, &re) && re.Code == "update_in_progress"
}

// cacheValid says whether the cached record can be trusted: only while this engine holds the update lock
// and nobody else could have written since it took it.
func (e *Engine) cacheValid() bool {
	e.lockMu.Lock()
	defer e.lockMu.Unlock()
	if e.hold == 0 || e.stale {
		e.stale = false
		return false
	}
	return true
}

// ---- trust ----

// loadTrust is the trust in force: the pinned anchor in the verity root, then every persisted
// rotation in version order, each verified by the one before it.
func (e *Engine) loadTrust() (TrustDoc, error) {
	invalid := func() (TrustDoc, error) {
		return TrustDoc{}, refuse(500, "trust_invalid", "the persisted update trust chain is invalid; reinstall the trust documents or the image")
	}
	b, err := readFileLimit(e.cfg.PinnedTrust, maxTrustFile)
	if err != nil {
		return TrustDoc{}, refuse(500, "trust_unavailable", "the pinned update trust is missing")
	}
	cur, err := ParseTrust(b)
	if err != nil {
		return TrustDoc{}, refuse(500, "trust_unavailable", "the pinned update trust is unreadable")
	}
	for {
		next := filepath.Join(e.cfg.TrustDir, fmt.Sprintf("trust.%d", cur.Version+1))
		raw, err := readFileLimit(next+".json", maxTrustFile)
		if errors.Is(err, os.ErrNotExist) {
			break // the end of the chain (a crash between the .sig and the .json leaves only the .sig)
		}
		if err != nil {
			return invalid()
		}
		sig, err := readFileLimit(next+".sig", 128)
		if err != nil {
			return invalid()
		}
		// Each link was signed by the one before it when it was adopted; whether that key is still
		// inside its validity today does not matter, so the check ignores not_after (zero time).
		d, err := VerifyTrust(cur, raw, sig, time.Time{})
		if err != nil || d.Version != cur.Version+1 {
			return invalid()
		}
		cur = d
	}
	e.mu.Lock()
	adopted := e.st.TrustVersion
	e.mu.Unlock()
	if cur.Version < adopted {
		// A trust version once adopted is never given up: documents that vanished do not bring back the
		// keys they revoked.
		return invalid()
	}
	return cur, nil
}

// refreshTrust fetches trust.json and adopts a newer one signed by the trust in force. An equal or
// older document is ignored (a stale mirror); a newer one that does not verify is a refusal.
func (e *Engine) refreshTrust(ctx context.Context, src Source, cur TrustDoc) (TrustDoc, error) {
	raw, err := src.Fetch(ctx, "trust.json", maxTrustFile)
	if err != nil {
		return cur, err
	}
	d, err := ParseTrust(raw)
	if err != nil {
		return cur, err
	}
	if d.Version <= cur.Version {
		return cur, nil
	}
	sig, err := src.Fetch(ctx, "trust.sig", 128)
	if err != nil {
		return cur, err
	}
	next, err := VerifyTrust(cur, raw, sig, e.cfg.Now())
	if err != nil {
		return cur, err
	}
	if err = os.MkdirAll(e.cfg.TrustDir, 0o700); err != nil {
		return cur, err
	}
	base := filepath.Join(e.cfg.TrustDir, fmt.Sprintf("trust.%d", next.Version))
	// A skipped version is stored under every number in between so the chain loads in order.
	if next.Version != cur.Version+1 {
		return cur, refuse(502, "trust_gap", "trust.json skips a version; fetch the intermediate trust documents first")
	}
	if err = writeFileAtomic(base+".sig", sig, 0o600); err != nil {
		return cur, err
	}
	if err = writeFileAtomic(base+".json", raw, 0o600); err != nil {
		return cur, err
	}
	return next, nil
}

func readFileLimit(p string, limit int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f, limit)
}

// readFileIn reads a small file of untrusted media through openRegular.
func readFileIn(root *os.Root, name string, limit int64) ([]byte, error) {
	f, err := openRegular(root, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f, limit)
}

func readLimited(f *os.File, limit int64) ([]byte, error) {
	b := make([]byte, limit+1)
	n, err := f.Read(b)
	if err != nil && n == 0 {
		return nil, err
	}
	if int64(n) > limit {
		return nil, errors.New("file too large")
	}
	return b[:n], nil
}

// ---- metadata ----

type verified struct {
	manifest Manifest
	src      Source
	// manifestRaw and manifestSig are the exact signed bytes: the layer store keeps them so the new base
	// can verify its layers when it boots.
	manifestRaw, manifestSig []byte
}

// fetchMetadata runs the plan's host checks in order and, only when every one passes, ratchets
// channel_serial and rollback_floor. target 0 means the release the channel points to.
func (e *Engine) fetchMetadata(ctx context.Context, target int) (*verified, error) {
	base, channel := e.cfg.Settings()
	if strings.TrimSpace(base) == "" {
		return nil, refuse(503, "source_unconfigured", "no update base URL is configured")
	}
	if channel == "" {
		channel = "stable"
	}
	return e.fetchMetadataFrom(ctx, e.cfg.Source(base), channel, target)
}

// fetchMetadataFrom is the one verification path of every source: the update server, the pool master's
// cache and an offline bundle in the online layout.
func (e *Engine) fetchMetadataFrom(ctx context.Context, src Source, channel string, target int) (*verified, error) {
	base, _ := e.cfg.Settings()
	key := replayKey(base, channel)
	trust, err := e.loadTrust()
	if err != nil {
		return nil, err
	}
	if trust, err = e.refreshTrust(ctx, src, trust); err != nil {
		return nil, err
	}
	// The adopted trust version only grows: once recorded, loadTrust refuses anything older.
	e.mu.Lock()
	if trust.Version > e.st.TrustVersion {
		e.st.TrustVersion = trust.Version
		err = e.save()
	}
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	now := e.cfg.Now()
	craw, err := src.Fetch(ctx, channel+"/channel.json", maxMetadata)
	if err != nil {
		return nil, err
	}
	csig, err := src.Fetch(ctx, channel+"/channel.sig", 128)
	if err != nil {
		return nil, err
	}
	ch, err := VerifyChannel(trust, craw, csig, channel, now)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	floor, err := e.admitLocked(key, ch, ch.Sequence)
	e.mu.Unlock()
	if err != nil {
		return nil, err
	}
	seq, channelHash := ch.Sequence, ch.ManifestSHA256
	if target != 0 && target != ch.Sequence {
		if target < floor {
			return nil, refuse(409, "below_rollback_floor", "the release is below the rollback floor")
		}
		if target > ch.Sequence {
			return nil, refuse(409, "unknown_sequence", "the channel does not offer that sequence")
		}
		seq, channelHash = target, ""
	}
	dir := fmt.Sprintf("releases/%d/", seq)
	mraw, err := src.Fetch(ctx, dir+"manifest.json", maxMetadata)
	if err != nil {
		return nil, err
	}
	msig, err := src.Fetch(ctx, dir+"manifest.sig", 128)
	if err != nil {
		return nil, err
	}
	m, err := VerifyManifest(trust, mraw, msig, channelHash, now)
	if err != nil {
		return nil, err
	}
	if m.Sequence != seq {
		return nil, refuse(502, "metadata_invalid", "the manifest is for another sequence")
	}
	if m.Created.After(now) {
		return nil, refuse(409, "clock_behind", "the host clock is earlier than the release was created; set the clock first")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	// The record may have moved while the network was busy (another operation in this process): judge
	// the metadata against what it says now, and only ever raise the serial and the floor.
	newFloor, err := e.admitLocked(key, ch, seq)
	if err != nil {
		return nil, err
	}
	if err = e.checkCompat(m, e.st.Current); err != nil {
		return nil, err
	}
	e.recordPointLocked(key, ch)
	e.st.RollbackFloor = newFloor
	e.st.Channel = channel
	e.st.CheckedAt = now
	if err = e.save(); err != nil {
		return nil, err
	}
	return &verified{manifest: m, src: src, manifestRaw: mraw, manifestSig: msig}, nil
}

// replayPoint is the newest channel pointer accepted from one source.
type replayPoint struct {
	Serial int    `json:"serial"`
	SHA    string `json:"manifest_sha256,omitempty"`
}

// replayKey names the pointer history of a source: the configured update server and the channel read from
// it, or from a bundle carrying that channel. Changing the server therefore needs no reset, and a bundle's
// other channels keep their own history instead of being judged against the configured one.
func replayKey(base, channel string) string { return base + "\n" + channel }

func (e *Engine) recordPointLocked(key string, ch Channel) {
	if e.st.Replay == nil {
		e.st.Replay = map[string]replayPoint{}
	}
	e.st.Replay[key] = replayPoint{Serial: max(e.st.Replay[key].Serial, ch.ChannelSerial), SHA: ch.ManifestSHA256}
}

// admitLocked judges a verified channel pointer for sequence seq against the source's accepted record: not
// older than the newest pointer accepted, not below the rollback floor (which is about releases and spans
// sources). It returns the floor to keep, the larger of the one on record and the pointer's min_sequence.
// e.mu is held.
func (e *Engine) admitLocked(key string, ch Channel, seq int) (int, error) {
	seen := e.st.Replay[key]
	if ch.ChannelSerial < seen.Serial || (ch.ChannelSerial == seen.Serial && seen.SHA != "" && ch.ManifestSHA256 != seen.SHA) {
		return 0, refuse(502, "channel_replayed", "channel.json is older than the one already accepted")
	}
	floor := max(e.st.RollbackFloor, ch.MinSequence)
	if ch.Sequence < floor || seq < floor {
		return 0, refuse(409, "below_rollback_floor", "the release is below the rollback floor")
	}
	return floor, nil
}

func availableOf(m Manifest) *Available {
	a := &Available{Label: m.Label, Sequence: m.Sequence, Compat: m.Compat}
	for _, c := range m.Components {
		a.Components = append(a.Components, ComponentInfo{ID: c.ID, Kind: c.Kind, Bytes: c.Bytes, Required: c.Required, OnDemand: c.OnDemand})
	}
	return a
}

func recordOf(m Manifest, rootHash string) ReleaseRecord {
	return ReleaseRecord{Label: m.Label, Sequence: m.Sequence, RootHash: rootHash, StateFormat: m.Compat.StateFormat,
		MinStateFormat: m.Compat.MinStateFormat, MDGeneration: m.Compat.MDGeneration, MDFallbackSafe: m.Compat.MDFallbackSafe}
}

// ---- guards ----

// guardTarget is the compatibility gate run at stage, activate and rollback.
func (e *Engine) guardTarget(target ReleaseRecord, current *ReleaseRecord) error {
	required := RequiredStateFormat(e.cfg.Root)
	if target.StateFormat < required {
		return refuse(409, "state_format_downgrade", "the target image cannot read this host's retained state")
	}
	if required < target.MinStateFormat {
		return refuse(409, "state_format_too_old", "this host's retained state is older than the target image can migrate")
	}
	return checkMD(target, current)
}

// checkMD is the md superblock gate. A kernel generation that stamps a field into the md v1.2
// superblock (observed: logical_block_size at offset 224, written by a 7.x kernel and refused by
// 6.12.107, docs/qa/image/phase2-build.md defect 4) leaves nex-data unassemblable for an older
// generation. Two rules keep both directions safe:
//
//   - an image that raises the generation must be published with md_fallback_safe, the publisher's
//     assertion that the older kernel still assembles the array (CI tests the rollback across it);
//   - an image of a lower generation than the running one, which is not md_fallback_safe, is never
//     staged or rolled back to.
//
// Boot-count fallback bypasses userspace, so the first rule is the one that protects it.
func checkMD(target ReleaseRecord, current *ReleaseRecord) error {
	cur := 0
	safe := false
	if current != nil {
		cur, safe = current.MDGeneration, current.MDFallbackSafe
	}
	if target.MDGeneration > cur && !target.MDFallbackSafe {
		return refuse(409, "md_generation_unsafe", "the image raises the md superblock generation without a tested fallback")
	}
	if target.MDGeneration < cur && !safe {
		return refuse(409, "md_generation_downgrade", "the target image's kernel cannot assemble the data array the running kernel generation touched")
	}
	return nil
}

func (e *Engine) checkCompat(m Manifest, current *ReleaseRecord) error {
	if m.Compat.UpdateEngine > EngineVersion {
		return refuse(409, "engine_too_old", "the release needs a newer update engine; update Nex Core first")
	}
	if current != nil && m.MinFromSequence > current.Sequence {
		return refuse(409, "bridge_required", "the release cannot be installed from this version; install the bridge release first")
	}
	for _, c := range m.Components {
		if c.Required && !c.OnDemand && c.Kind != "slot-image" && c.Kind != "source" && c.Kind != "compliance" && c.Kind != "layer" {
			return refuse(409, "unsupported_component", "the release has a required component this engine cannot install yet")
		}
	}
	return nil
}

func (e *Engine) checkVMs(ctx context.Context, policy string) error {
	if policy == "shutdown" || e.cfg.RunningVMs == nil {
		return nil
	}
	names, err := e.cfg.RunningVMs(ctx)
	if err != nil {
		return refuse(503, "vm_inventory_unavailable", "the running virtual machines cannot be listed")
	}
	if len(names) > 0 {
		return refuse(409, "running_vms", "running virtual machines block the reboot; send running_vms shutdown to accept their downtime")
	}
	return nil
}

// ---- reconcile ----

// reconcile folds what the disks say into the record: whether an armed release booted, was blessed or
// fell back, and whether the running image is the one the record thinks. e.mu must be held.
func (e *Engine) reconcile(ctx context.Context) error {
	if e.cfg.Platform == nil {
		return nil
	}
	b, err := e.cfg.Platform.Booted(ctx)
	if err != nil {
		return err
	}
	st := &e.st
	changed := false
	if st.Current == nil || st.Current.RootHash == "" {
		rec := recordOfBooted(b)
		st.Current, changed = &rec, true
	}
	if st.Armed != nil {
		entries, err := e.cfg.Platform.Entries(ctx)
		if err != nil {
			return err
		}
		var total, blessed, bad int
		for _, u := range entries {
			if u.Sequence == st.Armed.Sequence {
				total++
				if u.Blessed() {
					blessed++
				}
				if u.Bad() {
					bad++
				}
			}
		}
		switch {
		case b.RootHash == st.Armed.RootHash && blessed > 0:
			e.commit(ctx)
			changed = true
		case b.RootHash == st.Armed.RootHash:
			if st.Phase != PhaseBooted {
				st.Phase, changed = PhaseBooted, true
			}
		case total > 0 && bad == total:
			st.Phase = PhaseRolledBack
			st.Failure = refuse(0, "boot_failed", "the new version did not pass its boot assessment and the previous version is running")
			st.Armed, st.Staged = nil, nil
			e.removeStaging()
			changed = true
		case total == 0:
			// The armed entry is gone from every ESP: the record is stale.
			st.Armed = nil
			st.Phase = PhaseIdle
			changed = true
		}
	} else if st.Current.RootHash != b.RootHash {
		// Booted something the record did not expect: the operator picked the previous entry in the
		// boot menu, or the record was lost.
		if st.Previous != nil && st.Previous.RootHash == b.RootHash {
			st.Current, st.Previous = st.Previous, st.Current
		} else {
			rec := recordOfBooted(b)
			st.Current = &rec
		}
		changed = true
	}
	if st.Available != nil && st.Current != nil && st.Available.Sequence <= st.Current.Sequence {
		st.Available, changed = nil, true
	}
	if changed {
		return e.save()
	}
	return nil
}

func (e *Engine) commit(ctx context.Context) {
	st := &e.st
	armed := *st.Armed
	armed.Rollback = false
	prev := st.Current
	st.Previous, st.Current = prev, &armed
	st.Armed, st.Staged = nil, nil
	st.Phase = PhaseCommitted
	st.Failure = nil
	e.removeStaging()
	// The base before this one stays bootable (rollback is open until the next stage), so its layers
	// stay; anything older goes.
	e.pruneLayers(ctx, st.Current, st.Previous)
}

func (e *Engine) removeStaging() { _ = os.RemoveAll(filepath.Join(e.cfg.Dir, "staging")) }

// ---- status ----

func (e *Engine) statusLocked() ImageStatus {
	st := e.st
	s := ImageStatus{Running: e.active != nil, Channel: st.Channel, Available: st.Available, Phase: st.Phase,
		RollbackFloor: st.RollbackFloor, Failure: st.Failure,
		StateFormat: RequiredStateFormat(e.cfg.Root), Slots: []SlotStatus{}}
	base, channel := e.cfg.Settings()
	s.BaseURL, s.Media = base, st.Media
	s.ChannelSerial = st.Replay[replayKey(base, channel)].Serial
	if s.Channel == "" {
		s.Channel = channel
	}
	if !st.CheckedAt.IsZero() {
		t := st.CheckedAt
		s.CheckedAt = &t
	}
	add := func(role string, r *ReleaseRecord) {
		if r != nil {
			s.Slots = append(s.Slots, SlotStatus{Slot: r.Slot, Role: role, Label: r.Label, Sequence: r.Sequence, RootHash: r.RootHash})
		}
	}
	add("booted", st.Current)
	add("previous", st.Previous)
	add("staged", st.Staged)
	add("armed", st.Armed)
	if n := len(st.Operations); n > 0 {
		op := st.Operations[n-1].Op
		s.Operation = &op
	}
	return s
}

// Status is GET /updates: no side effects beyond folding the boot state into the record.
func (e *Engine) Status(ctx context.Context) (ImageStatus, error) {
	if err := e.enter(0); err != nil {
		if !isBusy(err) {
			return ImageStatus{}, err
		}
		// Another process is mid-update: report its record as it is on disk and fold nothing in.
		st, rerr := ReadStatus(e.cfg)
		st.Running = true
		return st, rerr
	}
	defer e.leave()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.load(); err != nil {
		return ImageStatus{}, err
	}
	if e.active == nil {
		if err := e.reconcile(ctx); err != nil {
			return ImageStatus{}, err
		}
	}
	return e.statusLocked(), nil
}

// Operation is GET /updates/operations/{id}: one recorded operation (the last 20 are kept).
func (e *Engine) Operation(id string) (Operation, bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.load(); err != nil {
		return Operation{}, false, err
	}
	for _, r := range e.st.Operations {
		if r.Op.ID == id {
			return r.Op, true, nil
		}
	}
	return Operation{}, false, nil
}

// ReadStatus is the recorded status read from state.json alone, for inventory probes in other processes.
// It reads no disk and writes nothing: the Engine, one per host, owns every write and the reconcile that
// folds a boot into the record (nex-update-reconcile.service runs it after each boot).
func ReadStatus(cfg Config) (ImageStatus, error) {
	e := NewEngine(cfg)
	defer e.Close()
	if err := e.load(); err != nil {
		return ImageStatus{}, err
	}
	return e.statusLocked(), nil
}

// Check is POST /updates/check: refresh and verify the metadata. Within 60 s of the last check it is
// refused with check_throttled (429).
func (e *Engine) Check(ctx context.Context) (ImageStatus, error) {
	if err := e.enter(lockWait); err != nil {
		return ImageStatus{}, err
	}
	defer e.leave()
	e.mu.Lock()
	if err := e.load(); err != nil {
		e.mu.Unlock()
		return ImageStatus{}, err
	}
	if !e.lastCheck.IsZero() && e.cfg.Now().Sub(e.lastCheck) < checkThrottle {
		e.mu.Unlock()
		return ImageStatus{}, refuse(429, "check_throttled", "the update source was checked less than a minute ago")
	}
	e.checks++
	defer func() {
		e.mu.Lock()
		e.checks--
		e.mu.Unlock()
	}()
	if err := e.reconcile(ctx); err != nil {
		e.mu.Unlock()
		return ImageStatus{}, err
	}
	e.mu.Unlock()
	v, err := e.fetchMetadata(ctx, 0)
	if err != nil {
		return ImageStatus{}, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastCheck = e.cfg.Now()
	// The record may have moved while the network was busy: judge against what it says now.
	if cur := e.st.Current; cur != nil && v.manifest.Sequence > cur.Sequence && v.manifest.Sequence >= e.st.RollbackFloor {
		e.st.Available = availableOf(v.manifest)
	} else {
		e.st.Available = nil
	}
	if err = e.save(); err != nil {
		return ImageStatus{}, err
	}
	return e.statusLocked(), nil
}

// ---- operations ----

func digestOf(kind string, req any) string {
	b, _ := json.Marshal(req)
	h := sha256.Sum256(append([]byte(kind+"\x00"), b...))
	return hex.EncodeToString(h[:])
}

// begin records a new operation, or returns the recorded one for a replayed request_id. replay is true
// for an exact replay; resume is true when that operation was interrupted and is not running.
func (e *Engine) begin(id, kind, digest string, sequence int) (op Operation, replay, resume bool, err error) {
	if !requestIDRE.MatchString(id) {
		return Operation{}, false, false, refuse(400, "invalid_request", "request_id must be 8 to 64 letters, digits, - or _")
	}
	for _, r := range e.st.Operations {
		if r.Op.ID != id {
			continue
		}
		if r.Digest != digest || r.Op.Kind != kind {
			return Operation{}, false, false, refuse(409, "request_conflict", "request_id was used for a different request")
		}
		if r.Op.FinishedAt == nil && (e.active == nil || e.active.id != id) {
			if e.active != nil {
				return Operation{}, false, false, refuse(409, "update_in_progress", "another update operation is running")
			}
			return r.Op, true, true, nil
		}
		return r.Op, true, false, nil
	}
	if e.active != nil {
		return Operation{}, false, false, refuse(409, "update_in_progress", "another update operation is running")
	}
	now := e.cfg.Now()
	for i := range e.st.Operations {
		o := &e.st.Operations[i].Op
		if o.FinishedAt == nil {
			if o.Kind == "apply" || o.Kind == "import" || o.Kind == "controller" {
				// An interrupted apply is resumed by repeating its request_id or cancelled, never abandoned silently.
				return Operation{}, false, false, refuse(409, "update_in_progress", "an interrupted update must be resumed with its request_id or cancelled first")
			}
			o.FinishedAt, o.Phase, o.Error = &now, "failed", &Error{Code: "interrupted", Message: "the operation was interrupted"}
		}
	}
	op = Operation{ID: id, Kind: kind, Phase: PhaseChecking, Sequence: sequence, StartedAt: now}
	e.st.Operations = append(e.st.Operations, opRecord{Op: op, Digest: digest})
	if n := len(e.st.Operations); n > keepOperations {
		e.st.Operations = e.st.Operations[n-keepOperations:]
	}
	return op, false, false, e.save()
}

// reserve marks the engine busy for an operation whose VM policy is evaluated with the lock released.
func (e *Engine) reserve(id string, cancel context.CancelFunc) {
	e.active = &activeOp{id: id, cancel: cancel, done: make(chan struct{}), phase: PhaseChecking}
}

func (e *Engine) release(id string) {
	if e.active != nil && e.active.id == id {
		e.clearActive()
	}
}

// clearActive ends the running operation and, when it owns the update lock, gives the lock back.
func (e *Engine) clearActive() {
	a := e.active
	if a == nil {
		return
	}
	close(a.done)
	e.active = nil
	if a.owns {
		e.leave()
	}
}

func (e *Engine) updateOp(id string, fn func(*Operation)) {
	for i := range e.st.Operations {
		if e.st.Operations[i].Op.ID == id {
			fn(&e.st.Operations[i].Op)
		}
	}
}

func (e *Engine) finish(id string, phase string, opErr error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	fetch := sideJob(e.opKind(id)) // a payload fetch or Controller swap never moves the lifecycle phase of a staged release
	e.updateOp(id, func(o *Operation) {
		o.FinishedAt = &now
		if phase != "" {
			o.Phase = phase
		}
		o.Progress = nil
		if opErr != nil {
			var re *Error
			if !errors.As(opErr, &re) {
				re = refuse(500, "internal", "the update operation failed")
			}
			o.Error = &Error{Code: re.Code, Message: re.Message}
			o.Phase = "failed"
		}
	})
	if opErr != nil && !fetch {
		switch {
		case e.st.Staged != nil:
			e.st.Phase = PhaseStaged
		case e.rest != "" && e.rest != PhaseStaged:
			e.st.Phase = e.rest
		default:
			e.st.Phase = PhaseIdle
		}
	}
	// Write the record while the update lock is still held: clearActive gives the lock to the next process.
	_ = e.save()
	e.clearActive()
}

func (e *Engine) setPhase(id, phase string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.active != nil && e.active.id == id {
		e.active.phase = phase
	}
	fetch := sideJob(e.opKind(id))
	e.updateOp(id, func(o *Operation) { o.Phase = phase })
	if !fetch && (phase == PhaseChecking || phase == PhaseDownloading || phase == PhaseWriting) {
		e.st.Phase = phase
	}
	_ = e.save()
}

func (e *Engine) setProgress(id string, p OpProgress) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.updateOp(id, func(o *Operation) { o.Progress = &p })
}

func normalizeApply(r ApplyRequest) (ApplyRequest, error) {
	if r.Scope == "" {
		r.Scope = "host"
	}
	if r.Activate == "" {
		r.Activate = "stage_only"
	}
	if r.RunningVMs == "" {
		r.RunningVMs = "refuse"
	}
	if r.Sequence < 0 || (r.Scope != "host" && r.Scope != "controller") || (r.Activate != "stage_only" && r.Activate != "reboot") ||
		(r.RunningVMs != "refuse" && r.RunningVMs != "shutdown") || (r.Scope == "controller" && r.Activate == "reboot") {
		return r, refuse(400, "invalid_request", "scope, activate or running_vms has an unknown value")
	}
	return r, nil
}

func normalizeAction(r ActionRequest) (ActionRequest, error) {
	if r.RunningVMs == "" {
		r.RunningVMs = "refuse"
	}
	if r.RunningVMs != "refuse" && r.RunningVMs != "shutdown" {
		return r, refuse(400, "invalid_request", "running_vms has an unknown value")
	}
	return r, nil
}

// Apply is POST /updates/apply: verify, download, stage and optionally arm and reboot, in the
// background. replayed is true when request_id repeats an earlier identical request (200, not 202).
func (e *Engine) Apply(ctx context.Context, req ApplyRequest) (op Operation, replayed bool, err error) {
	return e.startJob(ctx, "apply", req)
}

// startJob runs apply, import and fetch: one background operation under the update lock, from a source
// (the update server, or an offline bundle for import and fetch), recorded and resumable by request_id.
func (e *Engine) startJob(ctx context.Context, kind string, req ApplyRequest) (op Operation, replayed bool, err error) {
	if req, err = normalizeApply(req); err != nil {
		return Operation{}, false, err
	}
	if req.Scope == "controller" {
		if e.cfg.Appliance == nil || e.cfg.ControllerProbe == nil {
			return Operation{}, false, refuse(409, "scope_unavailable", "this host has no Controller appliance engine")
		}
		kind = "controller"
	}
	req.kind = kind
	digest := digestOf(kind, struct {
		S int
		A string
		V string
		M string `json:",omitempty"`
		C string `json:",omitempty"`
		K string `json:",omitempty"`
	}{req.Sequence, req.Activate, req.RunningVMs, req.media, req.channel, req.component})
	if err = e.enter(lockWait); err != nil {
		return Operation{}, false, err
	}
	owned := false // a started operation owns the update lock until it ends (clearActive)
	defer func() {
		if !owned {
			e.leave()
		}
	}()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err = e.load(); err != nil {
		return Operation{}, false, err
	}
	if e.cfg.Platform == nil {
		return Operation{}, false, refuse(409, "unsupported_edition", "this host does not run the image edition")
	}
	if e.active == nil {
		if err = e.reconcile(ctx); err != nil {
			return Operation{}, false, err
		}
	}
	op, replay, resume, err := e.begin(req.RequestID, kind, digest, req.Sequence)
	if err != nil {
		return Operation{}, false, err
	}
	if replay && !resume {
		return op, true, nil
	}
	if !replay && !sideJob(kind) {
		if st := e.st.Phase; st == PhaseArmed || st == PhaseBooted {
			e.dropOp(req.RequestID)
			return Operation{}, false, refuse(409, "already_armed", "a release is armed; reboot into it or roll back before staging another")
		}
	}
	if !replay && !sideJob(kind) {
		e.rest = ""
		switch e.st.Phase {
		case PhaseCommitted, PhaseRolledBack, PhaseStaged:
			e.rest = e.st.Phase
		}
	}
	opctx, cancel := context.WithCancel(e.ctx)
	e.reserve(op.ID, cancel)
	e.active.owns, owned = true, true
	if !replay && req.Activate == "reboot" {
		e.mu.Unlock()
		err = e.checkVMs(ctx, req.RunningVMs)
		e.mu.Lock()
		if err != nil {
			cancel()
			e.dropOp(req.RequestID) // the refused request leaves no trace: forget it before the lock goes to the next process
			e.release(op.ID)
			return Operation{}, false, err
		}
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.runApply(opctx, op.ID, req)
	}()
	return op, replay, nil
}

// dropOp removes a just-recorded operation whose request was refused before any work started.
func (e *Engine) dropOp(id string) {
	ops := e.st.Operations[:0]
	for _, r := range e.st.Operations {
		if r.Op.ID != id {
			ops = append(ops, r)
		}
	}
	e.st.Operations = ops
	_ = e.save()
}

func (e *Engine) runApply(ctx context.Context, id string, req ApplyRequest) {
	var phase string
	var err error
	switch req.kind {
	case "fetch":
		phase, err = e.fetchPipeline(ctx, id, req)
	case "controller":
		phase, err = e.controllerPipeline(ctx, id, req)
	default:
		phase, err = e.pipeline(ctx, id, req)
	}
	if ctx.Err() != nil {
		// Cancelled or shutting down. The operation stays unfinished here: Cancel records its end, and
		// after a shutdown a repeat of the request_id resumes it.
		e.mu.Lock()
		e.release(id)
		e.mu.Unlock()
		return
	}
	e.finish(id, phase, err)
}

func (e *Engine) pipeline(ctx context.Context, id string, req ApplyRequest) (string, error) {
	e.setPhase(id, PhaseChecking)
	src, channel, done, err := e.sourceFor(ctx, req)
	if err != nil {
		return "", err
	}
	defer done()
	v, err := e.fetchMetadataFrom(ctx, src, channel, req.Sequence)
	if err != nil {
		return "", err
	}
	m := v.manifest
	e.mu.Lock()
	cur := e.st.Current
	e.mu.Unlock()
	if cur != nil && m.Sequence <= cur.Sequence {
		return "", refuse(409, "not_newer", "the release is not newer than the running version; use rollback to go back")
	}
	comp, err := m.SlotImage()
	if err != nil {
		return "", err
	}
	target := recordOf(m, comp.VerityRootHash)
	if err = e.guardTarget(target, cur); err != nil {
		return "", err
	}
	e.mu.Lock()
	e.updateOp(id, func(o *Operation) { o.Sequence = m.Sequence })
	staged, phase := e.st.Staged, e.st.Phase
	e.mu.Unlock()
	if staged != nil && staged.Sequence == m.Sequence && staged.RootHash == target.RootHash && phase == PhaseStaged {
		// Already staged by an earlier run of this request.
		return e.afterStage(ctx, id, req, m)
	}

	// Staging starts: anything of an older stage is stale, and the retained previous version is
	// about to lose its slot.
	e.mu.Lock()
	e.st.Staged, e.st.Failure = nil, nil
	e.mu.Unlock()
	dir := e.stagingDir(m.Sequence)
	e.setPhase(id, PhaseDownloading)
	blob, err := fetchBlob(ctx, v.src, blobName(comp), comp, dir, func(done, total int64) {
		e.setProgress(id, OpProgress{Component: comp.ID, BytesDone: done, BytesTotal: total})
	})
	if err != nil {
		return "", err
	}
	releaseDir := filepath.Join(dir, "release")
	if err = extractRelease(blob, releaseDir); err != nil {
		return "", err
	}
	rel, err := e.cfg.Platform.Verify(releaseDir)
	if err != nil {
		_ = os.RemoveAll(releaseDir)
		return "", refuse(502, "image_invalid", "the downloaded image failed verification")
	}
	if rel.Manifest.RootHash != comp.VerityRootHash || rel.Manifest.Sequence != m.Sequence || rel.Manifest.Label != m.Label {
		_ = os.RemoveAll(releaseDir)
		return "", refuse(502, "roothash_mismatch", "the image does not match the release manifest")
	}
	_ = os.Remove(blob)
	if err = ctx.Err(); err != nil {
		return "", err
	}
	layers, err := e.stageLayers(ctx, id, v, dir)
	if err != nil {
		return "", err
	}
	target.Layers = layers
	e.setPhase(id, PhaseWriting)
	// The slot about to be written holds the retained previous version: it stops being one now.
	e.mu.Lock()
	e.st.Previous = nil
	err = e.save()
	e.mu.Unlock()
	if err != nil {
		return "", err
	}
	b, err := e.cfg.Platform.Booted(ctx)
	if err != nil {
		return "", err
	}
	slot, err := e.cfg.Platform.Stage(ctx, rel, b)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	target.Slot = slot
	if e.st.Current != nil && e.st.Current.Slot == "" {
		e.st.Current.Slot = otherSlot(slot)
	}
	e.st.Staged = &target
	e.st.Phase = PhaseStaged
	err = e.save()
	e.mu.Unlock()
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	current := e.st.Current
	e.mu.Unlock()
	e.pruneLayers(ctx, current, &target)
	return e.afterStage(ctx, id, req, m)
}

func otherSlot(s string) string {
	if s == "a" {
		return "b"
	}
	return "a"
}

func blobName(c Component) string {
	if c.URL != "" {
		return c.URL
	}
	return "blobs/sha256-" + c.SHA256
}

func (e *Engine) afterStage(ctx context.Context, id string, req ApplyRequest, m Manifest) (string, error) {
	if req.Activate != "reboot" {
		return PhaseStaged, nil
	}
	// Staging takes minutes: guests may have started since Apply accepted the request. Look again
	// right before arming; a refusal leaves the release staged for a later activate.
	if err := e.checkVMs(ctx, req.RunningVMs); err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.armLocked(ctx); err != nil {
		return "", err
	}
	e.scheduleReboot()
	return PhaseArmed, nil
}

// armLocked arms the staged release. e.mu is held; guards and the VM policy are re-evaluated here
// because the host may have changed since staging.
func (e *Engine) armLocked(ctx context.Context) error {
	st := &e.st
	if st.Staged == nil || st.Phase != PhaseStaged {
		return refuse(409, "nothing_staged", "no release is staged")
	}
	staged := *st.Staged
	if staged.Sequence < st.RollbackFloor {
		return refuse(409, "below_rollback_floor", "the staged release is below the rollback floor")
	}
	if err := e.guardTarget(staged, st.Current); err != nil {
		return err
	}
	rel, err := e.cfg.Platform.Verify(filepath.Join(e.stagingDir(staged.Sequence), "release"))
	if err != nil {
		return refuse(409, "image_invalid", "the staged image no longer verifies; stage it again")
	}
	if rel.Manifest.RootHash != staged.RootHash {
		return refuse(409, "roothash_mismatch", "the staged image does not match the record; stage it again")
	}
	if err = e.switchLayers(ctx, staged); err != nil {
		return err
	}
	if err = e.cfg.Platform.Arm(ctx, rel); err != nil {
		return err
	}
	st.Armed = &staged
	st.Phase = PhaseArmed
	return e.save()
}

func (e *Engine) scheduleReboot() {
	if e.cfg.Reboot == nil {
		return
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		select {
		case <-time.After(e.cfg.RebootDelay):
			_ = e.cfg.Reboot(e.ctx)
		case <-e.ctx.Done():
		}
	}()
}

// Activate is POST /updates/activate: arm the staged release and reboot. Stage never changes the
// next boot; this is the only arming point.
func (e *Engine) Activate(ctx context.Context, req ActionRequest) (Operation, bool, error) {
	return e.action(ctx, req, "activate", func(ctx context.Context) (string, error) {
		if err := e.armLocked(ctx); err != nil {
			return "", err
		}
		return PhaseArmed, nil
	})
}

// Rollback is POST /updates/rollback: arm the retained previous version and reboot. During the trial boot of an
// update (phase booted) the running release is not yet committed; the release it replaced is the one to return to,
// as the Controller offers it and the guide describes.
func (e *Engine) Rollback(ctx context.Context, req ActionRequest) (Operation, bool, error) {
	return e.action(ctx, req, "rollback", func(ctx context.Context) (string, error) {
		st := &e.st
		current, previous := st.Current, st.Previous
		if st.Armed != nil {
			if st.Phase != PhaseBooted {
				return "", refuse(409, "already_armed", "a release is armed; reboot or wait for the boot assessment")
			}
			running := *st.Armed
			running.Rollback = false
			current, previous = &running, st.Current
		}
		if previous == nil || current == nil {
			return "", refuse(409, "nothing_to_roll_back", "no previous version is retained")
		}
		prev := *previous
		if prev.Sequence < st.RollbackFloor {
			return "", refuse(409, "below_rollback_floor", "the previous version is below the rollback floor")
		}
		if err := e.guardTarget(prev, current); err != nil {
			return "", err
		}
		if err := e.switchLayers(ctx, prev); err != nil {
			return "", err
		}
		if err := e.cfg.Platform.Rollback(ctx, *current, prev); err != nil {
			return "", refuse(409, "previous_unavailable", "the previous version's slot or boot entry is not intact on every disk")
		}
		st.Current, st.Previous = current, previous
		prev.Rollback = true
		st.Armed = &prev
		st.Staged = nil
		st.Phase = PhaseArmed
		st.Available = nil
		return PhaseArmed, e.save()
	})
}

// action runs one synchronous mutation (arming is a few renames) under the operation rules and then
// schedules the reboot.
func (e *Engine) action(ctx context.Context, req ActionRequest, kind string, fn func(context.Context) (string, error)) (Operation, bool, error) {
	req, err := normalizeAction(req)
	if err != nil {
		return Operation{}, false, err
	}
	if err = e.enter(lockWait); err != nil {
		return Operation{}, false, err
	}
	defer e.leave()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err = e.load(); err != nil {
		return Operation{}, false, err
	}
	if e.cfg.Platform == nil {
		return Operation{}, false, refuse(409, "unsupported_edition", "this host does not run the image edition")
	}
	if e.active == nil {
		if err = e.reconcile(ctx); err != nil {
			return Operation{}, false, err
		}
	}
	op, replay, resume, err := e.begin(req.RequestID, kind, digestOf(kind, req.RunningVMs), 0)
	if err != nil {
		return Operation{}, false, err
	}
	if replay && !resume {
		return op, true, nil
	}
	e.reserve(op.ID, func() {})
	e.mu.Unlock()
	err = e.checkVMs(ctx, req.RunningVMs)
	e.mu.Lock()
	e.release(op.ID)
	if err == nil {
		var phase string
		if phase, err = fn(ctx); err == nil {
			e.finishLocked(op.ID, phase, nil)
			e.scheduleReboot()
			return e.recorded(op.ID), replay, nil
		}
	}
	e.finishLocked(op.ID, "", err)
	e.dropFailedOp(op.ID, err)
	return Operation{}, false, err
}

// dropFailedOp forgets an operation that was refused before changing anything, so the request_id can
// be used again once the cause is fixed; the refusal itself is returned to the caller.
func (e *Engine) dropFailedOp(id string, err error) {
	var re *Error
	if errors.As(err, &re) && re.Status >= 400 && re.Status < 500 {
		e.dropOp(id)
	}
}

func (e *Engine) recorded(id string) Operation {
	for _, r := range e.st.Operations {
		if r.Op.ID == id {
			return r.Op
		}
	}
	return Operation{ID: id}
}

func (e *Engine) finishLocked(id, phase string, opErr error) {
	now := e.cfg.Now()
	e.updateOp(id, func(o *Operation) {
		o.FinishedAt = &now
		if phase != "" {
			o.Phase = phase
		}
		if opErr != nil {
			var re *Error
			if !errors.As(opErr, &re) {
				re = refuse(500, "internal", "the update operation failed")
			}
			o.Error = &Error{Code: re.Code, Message: re.Message}
			o.Phase = "failed"
		}
	})
	_ = e.save()
}

// Cancel is POST /updates/cancel: stop a running apply, or discard a staged release. It is refused
// once a release is armed (already_armed) and while the slot is being written (write_in_progress).
func (e *Engine) Cancel(ctx context.Context, req ActionRequest) (Operation, bool, error) {
	req, err := normalizeAction(req)
	if err != nil {
		return Operation{}, false, err
	}
	if err = e.enter(lockWait); err != nil {
		return Operation{}, false, err
	}
	defer e.leave()
	e.mu.Lock()
	if err = e.load(); err != nil {
		e.mu.Unlock()
		return Operation{}, false, err
	}
	digest := digestOf("cancel", req.RunningVMs)
	for _, r := range e.st.Operations {
		if r.Op.ID == req.RequestID {
			e.mu.Unlock()
			if r.Digest != digest || r.Op.Kind != "cancel" {
				return Operation{}, false, refuse(409, "request_conflict", "request_id was used for a different request")
			}
			return r.Op, true, nil
		}
	}
	if !requestIDRE.MatchString(req.RequestID) {
		e.mu.Unlock()
		return Operation{}, false, refuse(400, "invalid_request", "request_id must be 8 to 64 letters, digits, - or _")
	}
	if e.st.Phase == PhaseArmed || e.st.Phase == PhaseBooted {
		e.mu.Unlock()
		return Operation{}, false, refuse(409, "already_armed", "a release is armed and can no longer be cancelled; use rollback")
	}
	record := func() (Operation, bool, error) {
		now := e.cfg.Now()
		op := Operation{ID: req.RequestID, Kind: "cancel", Phase: "cancelled", StartedAt: now, FinishedAt: &now}
		e.st.Operations = append(e.st.Operations, opRecord{Op: op, Digest: digest})
		if n := len(e.st.Operations); n > keepOperations {
			e.st.Operations = e.st.Operations[n-keepOperations:]
		}
		return op, false, e.save()
	}
	if a := e.active; a != nil {
		if a.phase == PhaseSwapping {
			e.mu.Unlock()
			return Operation{}, false, refuse(409, "swap_in_progress", "the Controller swap cannot be cancelled; it restores the previous image itself if the new one fails")
		}
		if a.phase == PhaseWriting {
			e.mu.Unlock()
			return Operation{}, false, refuse(409, "write_in_progress", "the image is being written to the slots and cannot be cancelled")
		}
		id, done := a.id, a.done
		a.cancel()
		e.mu.Unlock()
		<-done
		e.mu.Lock()
		defer e.mu.Unlock()
		now := e.cfg.Now()
		e.updateOp(id, func(o *Operation) { o.Phase = "cancelled"; o.FinishedAt = &now; o.Progress = nil })
		if !sideJob(e.opKind(id)) {
			e.st.Phase = PhaseIdle
		}
		return record()
	}
	defer e.mu.Unlock()
	if e.st.Phase == PhaseStaged {
		e.st.Staged, e.st.Phase = nil, PhaseIdle
		e.pruneLayers(ctx, e.st.Current, e.st.Previous)
		e.removeStaging()
		return record()
	}
	for _, r := range e.st.Operations {
		if r.Op.FinishedAt == nil && r.Op.Kind == "controller" {
			return Operation{}, false, refuse(409, "swap_in_progress", "an interrupted Controller swap is resumed by repeating its request_id; it cannot be cancelled")
		}
		if r.Op.FinishedAt == nil && !sideJob(r.Op.Kind) {
			// An interrupted apply: forget its partial downloads.
			now := e.cfg.Now()
			e.updateOp(r.Op.ID, func(o *Operation) { o.Phase = "cancelled"; o.FinishedAt = &now })
			e.st.Phase = PhaseIdle
			e.removeStaging()
			return record()
		}
	}
	return Operation{}, false, refuse(409, "nothing_to_cancel", "no update operation is running or staged")
}
