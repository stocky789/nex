// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"nex.local/nex/internal/hostimage"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func keyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// publisher writes an update tree in the online layout, signing like the release job would.
type publisher struct {
	t        *testing.T
	dir      string
	rootPub  ed25519.PublicKey
	rootKey  ed25519.PrivateKey
	relPub   ed25519.PublicKey
	relKey   ed25519.PrivateKey
	trust    TrustDoc
	pinned   string // path of the pinned trust.json
	channel  string
	serial   int
	minSeq   int
	expires  time.Time
	manifest func(*Manifest)
}

func newPublisher(t *testing.T) *publisher {
	t.Helper()
	p := &publisher{t: t, dir: t.TempDir(), channel: "stable", serial: 1, expires: t0.Add(14 * 24 * time.Hour)}
	p.rootPub, p.rootKey = keyPair(t)
	p.relPub, p.relKey = keyPair(t)
	p.trust = TrustDoc{Format: 1, Version: 1, Keys: []TrustKey{
		NewTrustKey("root-1", "root", p.rootPub, t0.Add(365*24*time.Hour)),
		NewTrustKey("rel-1", "release", p.relPub, t0.Add(180*24*time.Hour)),
	}}
	raw, _ := json.Marshal(p.trust)
	p.pinned = filepath.Join(t.TempDir(), "trust.json")
	os.WriteFile(p.pinned, raw, 0o644)
	return p
}

func NewTrustKey(id, role string, pub ed25519.PublicKey, notAfter time.Time) TrustKey {
	return TrustKey{ID: id, Role: role, PublicKey: b64(pub), NotAfter: notAfter}
}

func (p *publisher) write(name string, b []byte) {
	p.t.Helper()
	full := filepath.Join(p.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(full, b, 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *publisher) signedFile(name string, v any, key ed25519.PrivateKey, sigName string) []byte {
	p.t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	p.write(name, raw)
	p.write(sigName, ed25519.Sign(key, raw))
	return raw
}

func (p *publisher) publishTrust() {
	p.signedFile("trust.json", p.trust, p.rootKey, "trust.sig")
}

type built struct {
	blob     []byte
	rootHash string
	sha      string
}

// buildRelease makes the slot-image blob of one release: a zstd-compressed tar of a valid release directory.
func buildRelease(t *testing.T, label string, seq int, seed int64) built {
	t.Helper()
	dir := t.TempDir()
	if err := hostimage.SyntheticRelease(dir, label, seq, seed, 16); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		if err = tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if err != nil || tw.Close() != nil {
		t.Fatal(err)
	}
	rh, _ := os.ReadFile(filepath.Join(dir, "roothash"))
	blob := zstdBytes(t, buf.Bytes())
	sum := sha256.Sum256(blob)
	return built{blob: blob, rootHash: strings.TrimSpace(string(rh)), sha: hex.EncodeToString(sum[:])}
}

// zstdBytes compresses one slot-image tar the way the publisher does (a single zstd frame with a checksum).
func zstdBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw, err := zstd.NewWriter(&out, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = zw.Write(raw); err != nil || zw.Close() != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// release publishes a release and (when point) makes the channel point at it.
func (p *publisher) release(seq int, label string, point bool) built {
	p.t.Helper()
	b := buildRelease(p.t, label, seq, int64(seq))
	p.write("blobs/sha256-"+b.sha, b.blob)
	m := Manifest{Format: 1, Sequence: seq, Label: label, Architecture: Architecture, Created: t0, MinFromSequence: 0,
		Compat: Compat{UpdateEngine: 1, StateFormat: 2, MinStateFormat: 0},
		Components: []Component{
			{ID: "base", Kind: "slot-image", SHA256: b.sha, Bytes: int64(len(b.blob)), URL: "blobs/sha256-" + b.sha, Required: true, VerityRootHash: b.rootHash, Encoding: SlotEncoding},
			{ID: "src", Kind: "source", SHA256: strings.Repeat("a", 64), Bytes: 1, Required: false},
		}}
	if p.manifest != nil {
		p.manifest(&m)
	}
	raw := p.signedFile("releases/"+itoa(seq)+"/manifest.json", m, p.relKey, "releases/"+itoa(seq)+"/manifest.sig")
	if point {
		p.point(seq, raw)
	}
	return b
}

func (p *publisher) point(seq int, manifestRaw []byte) {
	sum := sha256.Sum256(manifestRaw)
	c := Channel{Format: 1, Channel: p.channel, ChannelSerial: p.serial, Sequence: seq, ManifestSHA256: hex.EncodeToString(sum[:]),
		Expires: p.expires, MinSequence: p.minSeq}
	p.signedFile(p.channel+"/channel.json", c, p.relKey, p.channel+"/channel.sig")
}

// repoint re-signs channel.json for an already published release.
func (p *publisher) repoint(seq int) {
	raw, err := os.ReadFile(filepath.Join(p.dir, "releases", itoa(seq), "manifest.json"))
	if err != nil {
		p.t.Fatal(err)
	}
	p.point(seq, raw)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func b64(pub ed25519.PublicKey) string {
	b, _ := json.Marshal([]byte(pub)) // base64 string in quotes
	return strings.Trim(string(b), `"`)
}

// testSource is a Source over a directory with faults: a mirror that cuts the connection, ignores
// Range, flips bytes, blocks, or is down.
type testSource struct {
	inner   DirSource
	mu      sync.Mutex
	offsets []int64
	cutAt   int64 // deliver this many bytes of the next blob read, then fail (0 = never)
	noRange bool
	flip    bool
	down    bool
	gate    chan struct{}
	onFetch func(name string) // runs before a metadata file is served
}

func (s *testSource) Fetch(ctx context.Context, name string, limit int64) ([]byte, error) {
	s.mu.Lock()
	down, hook := s.down, s.onFetch
	s.mu.Unlock()
	if down {
		return nil, unreachable(nil)
	}
	if hook != nil {
		hook(name)
	}
	return s.inner.Fetch(ctx, name, limit)
}

type cutReader struct {
	io.ReadCloser
	left int64
	flip bool
	done bool
}

func (c *cutReader) Read(b []byte) (int, error) {
	if c.left == 0 {
		return 0, errors.New("connection reset")
	}
	if c.left > 0 && int64(len(b)) > c.left {
		b = b[:c.left]
	}
	n, err := c.ReadCloser.Read(b)
	if c.flip && n > 0 && !c.done {
		b[0] ^= 0xff
		c.done = true
	}
	if c.left > 0 {
		c.left -= int64(n)
	}
	return n, err
}

func (s *testSource) Blob(ctx context.Context, name string, offset int64, v string) (io.ReadCloser, bool, string, error) {
	s.mu.Lock()
	cut, noRange, flip, gate := s.cutAt, s.noRange, s.flip, s.gate
	s.cutAt = 0
	s.offsets = append(s.offsets, offset)
	s.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, false, "", ctx.Err()
		}
	}
	if noRange {
		offset = 0
	}
	body, ranged, etag, err := s.inner.Blob(ctx, name, offset, v)
	if err != nil {
		return nil, false, "", err
	}
	left := int64(-1)
	if cut > 0 {
		left = cut
	}
	return &cutReader{ReadCloser: body, left: left, flip: flip}, ranged, etag, nil
}

// fakePlatform is a Platform without disks. Verify is real: it authenticates the extracted release.
type fakePlatform struct {
	mu                        sync.Mutex
	booted                    Booted
	entries                   []hostimage.UKIEntry
	staged, armed, disarmed   []int
	rolled                    [][2]int
	stageErr, armErr, rollErr error
	disarmErr                 error
	stageGate                 chan struct{}
}

func (f *fakePlatform) Booted(context.Context) (Booted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.booted, nil
}
func (f *fakePlatform) Verify(dir string) (*hostimage.Release, error) {
	return hostimage.ReadRelease(dir)
}
func (f *fakePlatform) Stage(ctx context.Context, rel *hostimage.Release, b Booted) (string, error) {
	if f.stageGate != nil {
		<-f.stageGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stageErr != nil {
		return "", f.stageErr
	}
	f.staged = append(f.staged, rel.Manifest.Sequence)
	return "b", nil
}
func (f *fakePlatform) Arm(_ context.Context, rel *hostimage.Release) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.armErr != nil {
		return f.armErr
	}
	f.armed = append(f.armed, rel.Manifest.Sequence)
	f.entries = append(f.entries, hostimage.UKIEntry{Sequence: rel.Manifest.Sequence, Counted: true, Tries: 3})
	return nil
}
func (f *fakePlatform) Disarm(_ context.Context, rel ReleaseRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.disarmErr != nil {
		return f.disarmErr
	}
	f.disarmed = append(f.disarmed, rel.Sequence)
	kept := f.entries[:0]
	for _, u := range f.entries {
		if u.Sequence != rel.Sequence {
			kept = append(kept, u)
		}
	}
	f.entries = kept
	return nil
}
func (f *fakePlatform) Rollback(_ context.Context, from, to ReleaseRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rollErr != nil {
		return f.rollErr
	}
	f.rolled = append(f.rolled, [2]int{from.Sequence, to.Sequence})
	return nil
}
func (f *fakePlatform) Entries(context.Context) ([]hostimage.UKIEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]hostimage.UKIEntry(nil), f.entries...), nil
}

// boot simulates a reboot into a version: its entry is blessed or not.
func (f *fakePlatform) boot(b Booted, bless bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.booted = b
	for i := range f.entries {
		if f.entries[i].Sequence == b.Sequence && bless {
			f.entries[i].Counted = false
		}
	}
}

// exhaust simulates three failed boots of a version.
func (f *fakePlatform) exhaust(seq int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.entries {
		if f.entries[i].Sequence == seq {
			f.entries[i].Tries, f.entries[i].Done = 0, 3
		}
	}
}

type rig struct {
	t      *testing.T
	pub    *publisher
	src    *testSource
	plat   *fakePlatform
	eng    *Engine
	cfg    Config
	now    time.Time
	nowMu  sync.Mutex
	reboot chan struct{}
	root   string
}

func (r *rig) clock() time.Time {
	r.nowMu.Lock()
	defer r.nowMu.Unlock()
	return r.now
}

func (r *rig) advance(d time.Duration) {
	r.nowMu.Lock()
	r.now = r.now.Add(d)
	r.nowMu.Unlock()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	pub := newPublisher(t)
	pub.publishTrust()
	r := &rig{t: t, pub: pub, now: t0, reboot: make(chan struct{}, 8), root: t.TempDir()}
	r.src = &testSource{inner: DirSource{Root: pub.dir}}
	r.plat = &fakePlatform{booted: Booted{RootHash: strings.Repeat("0", 64), Label: "base", Sequence: 1}}
	dir := t.TempDir()
	r.cfg = Config{
		Dir: filepath.Join(dir, "image"), TrustDir: filepath.Join(dir, "update"), PinnedTrust: pub.pinned, Root: r.root,
		Settings:    func() (string, string) { return "https://updates.example.test", "stable" },
		Source:      func(string) Source { return r.src },
		Platform:    r.plat,
		Now:         r.clock,
		Reboot:      func(context.Context) error { r.reboot <- struct{}{}; return nil },
		RebootDelay: time.Millisecond,
	}
	r.eng = NewEngine(r.cfg)
	t.Cleanup(func() { r.eng.Close() })
	return r
}

// restart is a process restart: a new engine over the same state on disk.
func (r *rig) restart() {
	r.eng.Close()
	r.eng = NewEngine(r.cfg)
	r.t.Cleanup(func() { r.eng.Close() })
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var re *Error
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func (r *rig) status() ImageStatus {
	r.t.Helper()
	st, err := r.eng.Status(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return st
}

// wait blocks until the last operation finished and returns it.
func (r *rig) wait() Operation {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if op := r.status().Operation; op != nil && op.FinishedAt != nil && !r.status().Running {
			return *op
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.t.Fatal("operation did not finish")
	return Operation{}
}

func (r *rig) apply(id string, mod func(*ApplyRequest)) (Operation, bool, error) {
	req := ApplyRequest{RequestID: id}
	if mod != nil {
		mod(&req)
	}
	return r.eng.Apply(context.Background(), req)
}

func (r *rig) mustApply(id string, mod func(*ApplyRequest)) Operation {
	r.t.Helper()
	if _, _, err := r.apply(id, mod); err != nil {
		r.t.Fatal(err)
	}
	op := r.wait()
	if op.Error != nil {
		r.t.Fatalf("apply failed: %+v", op.Error)
	}
	return op
}

// ---- metadata verification ----

func TestCheckVerifiesAndRefusesBadMetadata(t *testing.T) {
	type tc struct {
		name  string
		setup func(r *rig)
		code  string
	}
	cases := []tc{
		{"manifest bytes changed after the channel pointed at it", func(r *rig) {
			p := filepath.Join(r.pub.dir, "releases/2/manifest.json")
			b, _ := os.ReadFile(p)
			os.WriteFile(p, bytes.Replace(b, []byte(`"label":"v2"`), []byte(`"label":"v9"`), 1), 0o644)
		}, "manifest_mismatch"},
		{"manifest signed by an unknown key", func(r *rig) {
			_, other := keyPair(t)
			p := filepath.Join(r.pub.dir, "releases/2")
			b, _ := os.ReadFile(filepath.Join(p, "manifest.json"))
			os.WriteFile(filepath.Join(p, "manifest.sig"), ed25519.Sign(other, b), 0o644)
		}, "bad_signature"},
		{"channel signed by an unknown key", func(r *rig) {
			_, other := keyPair(t)
			b, _ := os.ReadFile(filepath.Join(r.pub.dir, "stable/channel.json"))
			os.WriteFile(filepath.Join(r.pub.dir, "stable/channel.sig"), ed25519.Sign(other, b), 0o644)
		}, "bad_signature"},
		{"channel signature is a truncated file", func(r *rig) {
			os.WriteFile(filepath.Join(r.pub.dir, "stable/channel.sig"), []byte("short"), 0o644)
		}, "bad_signature"},
		{"channel expired", func(r *rig) { r.advance(15 * 24 * time.Hour) }, "channel_expired"},
		{"release key past not_after", func(r *rig) {
			r.advance(181 * 24 * time.Hour)
			r.pub.expires = r.clock().Add(time.Hour)
			r.pub.repoint(2)
		}, "bad_signature"},
		{"release key revoked in the pinned trust", func(r *rig) {
			d := r.pub.trust
			d.Keys = append([]TrustKey(nil), d.Keys...)
			d.Keys[1].Revoked = true
			raw, _ := json.Marshal(d)
			os.WriteFile(r.pub.pinned, raw, 0o644)
		}, "bad_signature"},
		{"channel below its own min_sequence", func(r *rig) { r.pub.minSeq = 3; r.pub.serial = 2; r.pub.repoint(2) }, "below_rollback_floor"},
		{"wrong architecture", func(r *rig) {
			r.pub.manifest = func(m *Manifest) { m.Architecture = "arm64" }
			r.pub.serial = 2
			r.pub.release(2, "v2", true)
		}, "wrong_architecture"},
		{"release needs a newer engine", func(r *rig) {
			r.pub.manifest = func(m *Manifest) { m.Compat.UpdateEngine = EngineVersion + 1 }
			r.pub.serial = 2
			r.pub.release(2, "v2", true)
		}, "engine_too_old"},
		{"release needs a bridge", func(r *rig) {
			r.pub.manifest = func(m *Manifest) { m.MinFromSequence = 5 }
			r.pub.serial = 2
			r.pub.release(2, "v2", true)
		}, "bridge_required"},
		{"required component this engine cannot install", func(r *rig) {
			r.pub.manifest = func(m *Manifest) {
				m.Components = append(m.Components, Component{ID: "controller", Kind: "vm-image", SHA256: strings.Repeat("b", 64), Bytes: 1, Required: true})
			}
			r.pub.serial = 2
			r.pub.release(2, "v2", true)
		}, "unsupported_component"},
		{"no source configured", func(r *rig) {
			r.eng.cfg.Settings = func() (string, string) { return "", "stable" }
		}, "source_unconfigured"},
		{"source down", func(r *rig) { r.src.down = true }, "source_unreachable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t)
			r.pub.release(2, "v2", true)
			c.setup(r)
			before := r.status()
			_, err := r.eng.Check(context.Background())
			wantCode(t, err, c.code)
			after := r.status()
			if after.Available != nil || after.ChannelSerial != before.ChannelSerial || after.RollbackFloor != before.RollbackFloor {
				t.Errorf("a refused check changed the record: %+v -> %+v", before, after)
			}
		})
	}
}

func TestCheckReportsAvailableAndThrottles(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	st, err := r.eng.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Available == nil || st.Available.Sequence != 2 || st.Available.Label != "v2" || st.ChannelSerial != 1 {
		t.Fatalf("%+v", st)
	}
	if st.Phase != PhaseIdle || len(st.Slots) != 1 || st.Slots[0].Role != "booted" || st.Slots[0].Sequence != 1 {
		t.Fatalf("a check must not change the boot state: %+v", st)
	}
	_, err = r.eng.Check(context.Background())
	wantCode(t, err, "check_throttled")
	r.advance(61 * time.Second)
	if _, err = r.eng.Check(context.Background()); err != nil {
		t.Fatalf("after the throttle: %v", err)
	}
}

func TestChannelReplayAndRollbackFloor(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true) // serial 1
	r.pub.serial = 5
	r.pub.release(3, "v3", true)
	if _, err := r.eng.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A mirror (or attacker) replays the old pointer: older serial.
	r.pub.serial = 4
	r.pub.repoint(2)
	r.advance(2 * time.Minute)
	_, err := r.eng.Check(context.Background())
	wantCode(t, err, "channel_replayed")
	// Same serial, different release.
	r.pub.serial = 5
	r.pub.repoint(2)
	r.advance(2 * time.Minute)
	_, err = r.eng.Check(context.Background())
	wantCode(t, err, "channel_replayed")
	// The replay protection survives a restart.
	r.restart()
	r.pub.serial = 4
	r.advance(2 * time.Minute)
	_, err = r.eng.Check(context.Background())
	wantCode(t, err, "channel_replayed")
	// A newer pointer raises the floor; a later pointer to a release below it is refused.
	r.pub.serial, r.pub.minSeq = 6, 3
	r.pub.repoint(3)
	r.advance(2 * time.Minute)
	if st, err := r.eng.Check(context.Background()); err != nil || st.RollbackFloor != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	r.pub.serial, r.pub.minSeq = 7, 0
	r.pub.repoint(2)
	r.advance(2 * time.Minute)
	_, err = r.eng.Check(context.Background())
	wantCode(t, err, "below_rollback_floor")
	// Applying an older release by sequence is below the floor too.
	if _, _, err = r.apply("req-floor-0001", func(a *ApplyRequest) { a.Sequence = 2 }); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "below_rollback_floor" {
		t.Fatalf("%+v", op)
	}
}

func TestTrustRotation(t *testing.T) {
	r := newRig(t)
	pub2, key2 := keyPair(t)
	r.pub.release(2, "v2", true)
	// Rotate: version 2 adds a second release key, signed by the root key; the channel is signed by it.
	r.pub.trust = TrustDoc{Format: 1, Version: 2, Keys: append(append([]TrustKey(nil), r.pub.trust.Keys...), NewTrustKey("rel-2", "release", pub2, t0.Add(300*24*time.Hour)))}
	r.pub.publishTrust()
	r.pub.relKey = key2
	r.pub.serial = 2
	r.pub.release(3, "v3", true)
	st, err := r.eng.Check(context.Background())
	if err != nil || st.Available == nil || st.Available.Sequence != 3 {
		t.Fatalf("%+v %v", st, err)
	}
	// The rotation persisted: a restart with the update source's trust.json gone still verifies.
	r.restart()
	os.Remove(filepath.Join(r.pub.dir, "trust.json"))
	r.pub.write("trust.json", func() []byte {
		b, _ := json.Marshal(TrustDoc{Format: 1, Version: 1, Keys: r.pub.trust.Keys[:2]})
		return b
	}())
	r.advance(2 * time.Minute)
	if _, err = r.eng.Check(context.Background()); err != nil {
		t.Fatalf("persisted trust lost, and an older trust.json must be ignored: %v", err)
	}
}

func TestTrustRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate func(r *rig)
		code   string
	}{
		"newer trust signed by a release key, not a root key": {func(r *rig) {
			r.pub.trust.Version = 2
			raw, _ := json.Marshal(r.pub.trust)
			r.pub.write("trust.json", raw)
			r.pub.write("trust.sig", ed25519.Sign(r.pub.relKey, raw))
		}, "bad_signature"},
		"newer trust that skips a version the source does not carry": {func(r *rig) {
			r.pub.trust.Version = 3
			r.pub.publishTrust()
		}, "trust_gap"},
		"newer trust without a release key": {func(r *rig) {
			r.pub.trust = TrustDoc{Format: 1, Version: 2, Keys: r.pub.trust.Keys[:1]}
			r.pub.publishTrust()
		}, "metadata_invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.pub.release(2, "v2", true)
			tc.mutate(r)
			_, err := r.eng.Check(context.Background())
			wantCode(t, err, tc.code)
		})
	}
}

// publishTrustLink publishes one version of the trust under its own name (trust.<n>.json and .sig) as the
// publisher does, signed by key; the latest one is also published as trust.json.
func (p *publisher) publishTrustLink(doc TrustDoc, key ed25519.PrivateKey, latest bool) {
	p.t.Helper()
	name := "trust." + itoa(doc.Version)
	p.signedFile(name+".json", doc, key, name+".sig")
	if latest {
		p.signedFile("trust.json", doc, key, "trust.sig")
	}
}

// A host that missed trust versions catches up link by link: every document in between is fetched and
// must be signed by a root key of the one before it, so a root key that only the middle document names
// is the one that vouches for the last.
func TestTrustCatchesUpThroughTheIntermediateDocuments(t *testing.T) {
	day := 24 * time.Hour
	rootPub2, rootKey2 := keyPair(t)
	build := func(r *rig) (v2, v3 TrustDoc) {
		v2 = TrustDoc{Format: 1, Version: 2, Keys: []TrustKey{
			NewTrustKey("root-1", "root", r.pub.rootPub, t0.Add(365*day)),
			NewTrustKey("root-2", "root", rootPub2, t0.Add(900*day)),
			NewTrustKey("rel-1", "release", r.pub.relPub, t0.Add(180*day)),
		}}
		// Version 3 drops the original root key: only root-2, named by version 2, can have signed it.
		v3 = TrustDoc{Format: 1, Version: 3, Keys: []TrustKey{
			NewTrustKey("root-2", "root", rootPub2, t0.Add(900*day)),
			NewTrustKey("rel-1", "release", r.pub.relPub, t0.Add(180*day)),
		}}
		return v2, v3
	}
	t.Run("the chain is walked and persisted", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		v2, v3 := build(r)
		r.pub.publishTrustLink(v2, r.pub.rootKey, false)
		r.pub.publishTrustLink(v3, rootKey2, true)
		if st, err := r.eng.Check(context.Background()); err != nil || st.Available == nil {
			t.Fatalf("%+v %v", st, err)
		}
		if got := readUpdate(t, r.cfg.Dir).TrustVersion; got != 3 {
			t.Fatalf("adopted trust version %d, want 3", got)
		}
		r.restart() // the persisted chain loads in order and still verifies
		r.advance(2 * time.Minute)
		if _, err := r.eng.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("an intermediate the old root did not sign is refused", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		v2, v3 := build(r)
		_, strangerKey := keyPair(t)
		r.pub.publishTrustLink(v2, strangerKey, false)
		r.pub.publishTrustLink(v3, rootKey2, true)
		_, err := r.eng.Check(context.Background())
		wantCode(t, err, "bad_signature")
		if got := readUpdate(t, r.cfg.Dir).TrustVersion; got != 0 {
			t.Fatalf("a refused chain was adopted: version %d", got)
		}
		if _, err := os.Stat(filepath.Join(r.cfg.TrustDir, "trust.2.json")); err == nil {
			t.Fatal("a refused chain was persisted")
		}
	})
	t.Run("a missing intermediate is a gap", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		_, v3 := build(r)
		r.pub.publishTrustLink(v3, rootKey2, true)
		_, err := r.eng.Check(context.Background())
		wantCode(t, err, "trust_gap")
	})
}

// ---- apply, activate, boot, rollback ----

func TestApplyStagesWithoutArmingThenActivateCommitsAndRollsBack(t *testing.T) {
	r := newRig(t)
	b2 := r.pub.release(2, "v2", true)
	op, replayed, err := r.apply("req-apply-0001", nil)
	if err != nil || replayed || op.Kind != "apply" {
		t.Fatalf("%+v %v %v", op, replayed, err)
	}
	if done := r.wait(); done.Phase != PhaseStaged || done.Error != nil || done.Sequence != 2 {
		t.Fatalf("%+v", done)
	}
	st := r.status()
	if st.Phase != PhaseStaged || len(r.plat.armed) != 0 || len(r.plat.staged) != 1 {
		t.Fatalf("stage must not arm: %+v armed=%v", st, r.plat.armed)
	}
	if _, err = os.Stat(filepath.Join(r.cfg.Dir, "staging", "2", b2.sha)); err == nil {
		t.Error("the downloaded blob is kept after it was extracted and verified")
	}
	// A restart after staging changes nothing: next boot is still the old version.
	r.restart()
	if st = r.status(); st.Phase != PhaseStaged || len(r.plat.armed) != 0 {
		t.Fatalf("%+v", st)
	}

	if _, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.reboot:
	case <-time.After(time.Minute):
		t.Fatal("no reboot after activate")
	}
	if st = r.status(); st.Phase != PhaseArmed || len(r.plat.armed) != 1 || r.plat.armed[0] != 2 {
		t.Fatalf("%+v", st)
	}
	// Armed, not rebooted yet: the record stays armed.
	r.restart()
	if st = r.status(); st.Phase != PhaseArmed {
		t.Fatalf("%+v", st)
	}
	// Rebooted into the new version, not blessed yet.
	r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, false)
	if st = r.status(); st.Phase != PhaseBooted {
		t.Fatalf("%+v", st)
	}
	// Blessed: committed, and the old version is the retained previous.
	r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, true)
	st = r.status()
	if st.Phase != PhaseCommitted || st.Available != nil {
		t.Fatalf("%+v", st)
	}
	roles := map[string]int{}
	for _, s := range st.Slots {
		roles[s.Role] = s.Sequence
	}
	if roles["booted"] != 2 || roles["previous"] != 1 || len(roles) != 2 {
		t.Fatalf("slots %+v", st.Slots)
	}
	if _, err = os.Stat(filepath.Join(r.cfg.Dir, "staging")); err == nil {
		t.Error("staging is kept after commit")
	}

	// Operator rollback arms the retained previous version.
	if _, _, err = r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-01"}); err != nil {
		t.Fatal(err)
	}
	if len(r.plat.rolled) != 1 || r.plat.rolled[0] != [2]int{2, 1} {
		t.Fatalf("%v", r.plat.rolled)
	}
	r.plat.boot(Booted{RootHash: strings.Repeat("0", 64), Label: "base", Sequence: 1}, true)
	r.plat.mu.Lock()
	r.plat.entries = append(r.plat.entries, hostimage.UKIEntry{Sequence: 1}) // the previous entry, blessed again
	r.plat.mu.Unlock()
	st = r.status()
	roles = map[string]int{}
	for _, s := range st.Slots {
		roles[s.Role] = s.Sequence
	}
	if st.Phase != PhaseCommitted || roles["booted"] != 1 || roles["previous"] != 2 {
		t.Fatalf("%+v", st)
	}
}

func TestFailedBootFallsBackAndIsReported(t *testing.T) {
	r := newRig(t)
	b2 := r.pub.release(2, "v2", true)
	r.mustApply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" })
	select {
	case <-r.reboot:
	case <-time.After(time.Minute):
		t.Fatal("no reboot")
	}
	_ = b2
	// Three failed tries: the old version is running again and the new entry is exhausted.
	r.plat.exhaust(2)
	st := r.status()
	if st.Phase != PhaseRolledBack || st.Failure == nil || st.Failure.Code != "boot_failed" {
		t.Fatalf("%+v", st)
	}
	for _, s := range st.Slots {
		if s.Sequence == 2 {
			t.Errorf("the failed version is still listed: %+v", st.Slots)
		}
	}
	if st.Slots[0].Sequence != 1 {
		t.Fatalf("%+v", st.Slots)
	}
	// The host can try again.
	r.pub.serial = 2
	r.pub.repoint(2)
	r.plat.mu.Lock()
	r.plat.entries = nil
	r.plat.mu.Unlock()
	r.mustApply("req-apply-0002", nil)
	if st = r.status(); st.Phase != PhaseStaged || st.Failure != nil {
		t.Fatalf("%+v", st)
	}
}

func TestStateFormatAndMDGuards(t *testing.T) {
	t.Run("target cannot read retained ZFS state", func(t *testing.T) {
		r := newRig(t)
		agent := filepath.Join(r.root, "var/lib/nex-core-agent")
		os.MkdirAll(agent, 0o755)
		os.WriteFile(filepath.Join(agent, "zfs-pool.json"), []byte("{}"), 0o600)
		r.pub.release(2, "v2", true) // state_format 2
		_, _, err := r.apply("req-apply-0001", nil)
		if err != nil {
			t.Fatal(err)
		}
		op := r.wait()
		if op.Error == nil || op.Error.Code != "state_format_downgrade" || len(r.plat.staged) != 0 {
			t.Fatalf("%+v staged=%v", op, r.plat.staged)
		}
		if r.status().StateFormat != 3 {
			t.Error("status does not report the required state format")
		}
	})
	t.Run("rollback to an image that cannot read the state", func(t *testing.T) {
		r := newRig(t)
		r.pub.manifest = func(m *Manifest) { m.Compat.StateFormat = 3 }
		b2 := r.pub.release(2, "v2", true)
		r.mustApply("req-apply-0001", nil)
		if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err != nil {
			t.Fatal(err)
		}
		r.plat.boot(Booted{RootHash: b2.rootHash, Label: "v2", Sequence: 2}, true)
		r.status()
		// The previous record has state_format 0 (unknown, from the booted identity); ZFS state appeared since.
		agent := filepath.Join(r.root, "var/lib/nex-core-agent")
		os.MkdirAll(agent, 0o755)
		os.WriteFile(filepath.Join(agent, "zfs-lifecycle.json"), []byte("{}"), 0o600)
		_, _, err := r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-rollback-01"})
		wantCode(t, err, "state_format_downgrade")
		if len(r.plat.rolled) != 0 {
			t.Fatal("rolled back despite the guard")
		}
	})
	t.Run("md generation", func(t *testing.T) {
		r := newRig(t)
		r.pub.manifest = func(m *Manifest) { m.Compat.MDGeneration = 1 }
		r.pub.release(2, "v2", true)
		_, _, _ = r.apply("req-apply-0001", nil)
		if op := r.wait(); op.Error == nil || op.Error.Code != "md_generation_unsafe" || len(r.plat.staged) != 0 {
			t.Fatalf("%+v", op)
		}
		r.pub.manifest = func(m *Manifest) { m.Compat.MDGeneration, m.Compat.MDFallbackSafe = 1, true }
		r.pub.serial = 2
		r.pub.release(3, "v3", true)
		r.advance(time.Minute)
		r.mustApply("req-apply-0002", nil)
		if len(r.plat.staged) != 1 {
			t.Fatalf("a tested md bump was refused: %v", r.plat.staged)
		}
	})
}

func TestCheckMD(t *testing.T) {
	cases := []struct {
		name        string
		target, cur ReleaseRecord
		wantCode    string
	}{
		{"same generation", ReleaseRecord{}, ReleaseRecord{}, ""},
		{"raise without a tested fallback", ReleaseRecord{MDGeneration: 1}, ReleaseRecord{}, "md_generation_unsafe"},
		{"raise with a tested fallback", ReleaseRecord{MDGeneration: 1, MDFallbackSafe: true}, ReleaseRecord{}, ""},
		{"roll back across an untested bump", ReleaseRecord{MDGeneration: 0}, ReleaseRecord{MDGeneration: 1}, "md_generation_downgrade"},
		{"roll back across a tested bump", ReleaseRecord{MDGeneration: 0}, ReleaseRecord{MDGeneration: 1, MDFallbackSafe: true}, ""},
	}
	for _, c := range cases {
		err := checkMD(c.target, &c.cur)
		if c.wantCode == "" && err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if c.wantCode != "" {
			var re *Error
			if !errors.As(err, &re) || re.Code != c.wantCode {
				t.Errorf("%s: %v, want %s", c.name, err, c.wantCode)
			}
		}
	}
}

// ---- idempotency, resume, tamper, crash ----

func TestRequestIDIdempotency(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	gate := make(chan struct{})
	r.src.gate = gate
	op, replayed, err := r.apply("req-apply-0001", nil)
	if err != nil || replayed {
		t.Fatal(err)
	}
	// Exact replay while running: the original operation, 200.
	op2, replayed, err := r.apply("req-apply-0001", nil)
	if err != nil || !replayed || op2.ID != op.ID {
		t.Fatalf("%+v %v %v", op2, replayed, err)
	}
	// Same id, different body.
	_, _, err = r.apply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" })
	wantCode(t, err, "request_conflict")
	// A different id while one is active.
	_, _, err = r.apply("req-apply-0002", nil)
	wantCode(t, err, "update_in_progress")
	_, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	wantCode(t, err, "update_in_progress")
	if !r.status().Running {
		t.Error("status does not say an operation is running")
	}
	close(gate)
	r.wait()
	// Replay after completion returns the finished operation without redoing the work.
	op3, replayed, err := r.apply("req-apply-0001", nil)
	if err != nil || !replayed || op3.FinishedAt == nil || len(r.plat.staged) != 1 {
		t.Fatalf("%+v %v %v staged=%v", op3, replayed, err, r.plat.staged)
	}
	// Bad ids are refused before anything is recorded.
	_, _, err = r.apply("short", nil)
	wantCode(t, err, "invalid_request")
	_, _, err = r.apply("req-apply-0009", func(a *ApplyRequest) { a.Scope = "controller" })
	wantCode(t, err, "scope_unavailable")
	// Activate replay.
	if _, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err != nil {
		t.Fatal(err)
	}
	if _, replayed, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err != nil || !replayed || len(r.plat.armed) != 1 {
		t.Fatalf("replayed=%v err=%v armed=%v", replayed, err, r.plat.armed)
	}
	_, _, err = r.eng.Rollback(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	wantCode(t, err, "request_conflict")
}

func TestDownloadResumesFromPartAndSurvivesRestart(t *testing.T) {
	r := newRig(t)
	b2 := r.pub.release(2, "v2", true)
	r.src.cutAt = int64(len(b2.blob) / 3) // the connection dies a third of the way in
	_, _, err := r.apply("req-apply-0001", nil)
	if err != nil {
		t.Fatal(err)
	}
	op := r.wait()
	if op.Error == nil || op.Error.Code != "source_unreachable" {
		t.Fatalf("%+v", op)
	}
	part := filepath.Join(r.cfg.Dir, "staging", "2", b2.sha+".part")
	if st, err := os.Stat(part); err != nil || st.Size() < int64(len(b2.blob)/3) {
		t.Fatalf("no usable .part after the cut: %v", err)
	}
	// Retrying with a fresh request id continues at the part's length.
	r.src.mu.Lock()
	r.src.offsets = nil
	r.src.mu.Unlock()
	r.mustApply("req-apply-0002", nil)
	if len(r.src.offsets) != 1 || r.src.offsets[0] != int64(len(b2.blob)/3) {
		t.Fatalf("resume offsets %v, want one request at %d", r.src.offsets, len(b2.blob)/3)
	}
	if len(r.plat.staged) != 1 {
		t.Fatal("not staged after the resume")
	}
}

func TestDownloadWithoutRangeSupportRestartsAndCorruptPrefixRecovers(t *testing.T) {
	r := newRig(t)
	b2 := r.pub.release(2, "v2", true)
	r.src.cutAt = int64(len(b2.blob) / 2)
	r.apply("req-apply-0001", nil)
	r.wait()
	r.src.noRange = true
	r.mustApply("req-apply-0002", nil)
	if len(r.plat.staged) != 1 {
		t.Fatal("a source without Range support did not finish")
	}

	// A corrupted .part (bad prefix) is detected by the final hash and retried once from zero.
	r2 := newRig(t)
	b := r2.pub.release(2, "v2", true)
	dir := filepath.Join(r2.cfg.Dir, "staging", "2")
	os.MkdirAll(dir, 0o700)
	junk := bytes.Repeat([]byte{0x42}, len(b.blob)/2)
	os.WriteFile(filepath.Join(dir, b.sha+".part"), junk, 0o600)
	r2.mustApply("req-apply-0001", nil)
	if len(r2.plat.staged) != 1 {
		t.Fatal("did not recover from a corrupt part")
	}
}

func TestTamperedBlobIsNeverStaged(t *testing.T) {
	r := newRig(t)
	r.pub.release(2, "v2", true)
	r.src.flip = true
	_, _, err := r.apply("req-apply-0001", nil)
	if err != nil {
		t.Fatal(err)
	}
	op := r.wait()
	if op.Error == nil || op.Error.Code != "blob_mismatch" {
		t.Fatalf("%+v", op)
	}
	st := r.status()
	if len(r.plat.staged) != 0 || st.Phase == PhaseStaged || len(st.Slots) != 1 {
		t.Fatalf("tampered image reached the slots: %+v", st)
	}
	// The blob served from a mirror whose bytes were swapped for a valid-looking other release.
	r2 := newRig(t)
	r2.pub.release(2, "v2", true)
	other := buildRelease(t, "v2", 2, 99)
	m, _ := os.ReadFile(filepath.Join(r2.pub.dir, "releases/2/manifest.json"))
	var man Manifest
	json.Unmarshal(m, &man)
	swapped := make([]byte, man.Components[0].Bytes) // same length, other bytes: only the hash can tell
	copy(swapped, other.blob)
	os.WriteFile(filepath.Join(r2.pub.dir, "blobs/sha256-"+man.Components[0].SHA256), swapped, 0o644)
	r2.apply("req-apply-0001", nil)
	if op = r2.wait(); op.Error == nil || op.Error.Code != "blob_mismatch" || len(r2.plat.staged) != 0 {
		t.Fatalf("%+v", op)
	}
}

func TestImageThatDoesNotMatchTheManifestIsRefused(t *testing.T) {
	r := newRig(t)
	r.pub.manifest = func(m *Manifest) { m.Components[0].VerityRootHash = strings.Repeat("c", 64) }
	r.pub.release(2, "v2", true)
	r.apply("req-apply-0001", nil)
	if op := r.wait(); op.Error == nil || op.Error.Code != "roothash_mismatch" || len(r.plat.staged) != 0 {
		t.Fatalf("%+v", op)
	}
}

func TestFailuresAtEachStepLeaveTheOldVersionAndAreRetryable(t *testing.T) {
	t.Run("slot write fails", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		r.plat.stageErr = errors.New("disk 2 gone")
		r.apply("req-apply-0001", nil)
		op := r.wait()
		st := r.status()
		if op.Error == nil || st.Phase != PhaseIdle || st.Running || len(r.plat.armed) != 0 {
			t.Fatalf("%+v %+v", op, st)
		}
		if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err == nil {
			t.Fatal("activated a release that was never staged")
		}
		// Repaired: a retry stages.
		r.plat.stageErr = nil
		r.mustApply("req-apply-0002", nil)
		if r.status().Phase != PhaseStaged {
			t.Fatal("retry did not stage")
		}
	})
	t.Run("arm fails and is retried", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		r.mustApply("req-apply-0001", nil)
		r.plat.armErr = errors.New("esp read-only")
		_, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
		if err == nil {
			t.Fatal("arm failure not reported")
		}
		select {
		case <-r.reboot:
			t.Fatal("rebooted although arming failed")
		case <-time.After(50 * time.Millisecond):
		}
		if st := r.status(); st.Phase != PhaseStaged {
			t.Fatalf("%+v", st)
		}
		r.plat.armErr = nil
		if _, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-02"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("restart in the middle of the download resumes the same request", func(t *testing.T) {
		r := newRig(t)
		b := r.pub.release(2, "v2", true)
		gate := make(chan struct{})
		r.src.gate = gate
		r.apply("req-apply-0001", nil)
		time.Sleep(20 * time.Millisecond)
		r.restart() // power cut: the process is gone, the operation unfinished on disk
		r.src.gate = nil
		if st := r.status(); st.Running || st.Operation == nil || st.Operation.FinishedAt != nil || st.Phase != PhaseDownloading {
			t.Fatalf("%+v", st)
		}
		// Another request is refused until the interrupted one is resumed or cancelled.
		_, _, err := r.apply("req-apply-0002", nil)
		wantCode(t, err, "update_in_progress")
		op, replayed, err := r.apply("req-apply-0001", nil)
		if err != nil || !replayed {
			t.Fatalf("%+v %v %v", op, replayed, err)
		}
		if done := r.wait(); done.Error != nil || done.Phase != PhaseStaged {
			t.Fatalf("%+v", done)
		}
		_ = b
	})
}

func TestCancel(t *testing.T) {
	t.Run("while downloading", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		r.src.gate = make(chan struct{})
		r.apply("req-apply-0001", nil)
		time.Sleep(20 * time.Millisecond)
		op, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"})
		if err != nil || op.Phase != "cancelled" {
			t.Fatalf("%+v %v", op, err)
		}
		st := r.status()
		if st.Running || st.Phase != PhaseIdle || len(r.plat.staged) != 0 {
			t.Fatalf("%+v", st)
		}
		// The cancelled apply stays cancelled on replay.
		if o, replayed, err := r.apply("req-apply-0001", nil); err != nil || !replayed || o.Phase != "cancelled" {
			t.Fatalf("%+v %v %v", o, replayed, err)
		}
	})
	t.Run("a staged release is discarded", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		r.mustApply("req-apply-0001", nil)
		if _, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"}); err != nil {
			t.Fatal(err)
		}
		if st := r.status(); st.Phase != PhaseIdle {
			t.Fatalf("%+v", st)
		}
		if _, _, err := r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"}); err == nil {
			t.Fatal("activated a cancelled stage")
		}
	})
	t.Run("refused once armed", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		r.mustApply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" })
		_, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"})
		wantCode(t, err, "already_armed")
		_, _, err = r.apply("req-apply-0002", nil)
		wantCode(t, err, "already_armed")
	})
	t.Run("nothing to cancel", func(t *testing.T) {
		r := newRig(t)
		_, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"})
		wantCode(t, err, "nothing_to_cancel")
	})
	t.Run("not while the slots are written", func(t *testing.T) {
		r := newRig(t)
		r.pub.release(2, "v2", true)
		r.plat.stageGate = make(chan struct{})
		r.apply("req-apply-0001", nil)
		deadline := time.Now().Add(time.Minute)
		for r.status().Phase != PhaseWriting && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		_, _, err := r.eng.Cancel(context.Background(), ActionRequest{RequestID: "req-cancel-001"})
		wantCode(t, err, "write_in_progress")
		close(r.plat.stageGate)
		r.wait()
	})
}

func TestRunningVMsPolicy(t *testing.T) {
	r := newRig(t)
	r.cfg.RunningVMs = func(context.Context) ([]string, error) { return []string{"vm-1"}, nil }
	r.eng.cfg.RunningVMs = r.cfg.RunningVMs
	r.pub.release(2, "v2", true)
	_, _, err := r.apply("req-apply-0001", func(a *ApplyRequest) { a.Activate = "reboot" })
	wantCode(t, err, "running_vms")
	if st := r.status(); st.Operation != nil || st.Running {
		t.Fatalf("a refused request left an operation: %+v", st)
	}
	r.mustApply("req-apply-0002", nil) // stage_only never reboots
	_, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01"})
	wantCode(t, err, "running_vms")
	if len(r.plat.armed) != 0 {
		t.Fatal("armed despite a running VM")
	}
	if _, _, err = r.eng.Activate(context.Background(), ActionRequest{RequestID: "req-activate-01", RunningVMs: "shutdown"}); err != nil {
		t.Fatalf("the same id may be retried after a refusal: %v", err)
	}
}

// ---- state format port ----

func TestRequiredStateFormat(t *testing.T) {
	write := func(root, rel, content string) {
		p := filepath.Join(root, "var/lib/nex-core-agent", rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name string
		do   func(root string)
		want int
	}{
		{"nothing retained", func(string) {}, 0},
		{"zfs pool record", func(r string) { write(r, "zfs-pool.json", "{}") }, 3},
		{"zfs lifecycle record", func(r string) { write(r, "zfs-lifecycle.json", "") }, 3},
		{"zfs receipt", func(r string) { write(r, "zfs-provision/r1.json", "{}") }, 3},
		{"empty receipt dir", func(r string) { os.MkdirAll(filepath.Join(r, "var/lib/nex-core-agent/zfs-provision"), 0o755) }, 0},
		{"managed VM with a plain record", func(r string) { write(r, "managed-vms/a.json", `{"name":"a"}`) }, 0},
		{"managed VM with applied_storage", func(r string) { write(r, "managed-vms/a.json", `{"applied_storage":{}}`) }, 2},
		{"managed VM with a tombstone", func(r string) { write(r, "managed-vms/a.json", `{"x":{"tombstone":true}}`) }, 2},
		{"managed VM command storage kind", func(r string) { write(r, "managed-vms/a.json", `{"command":{"storage":{"kind":"nfs"}}}`) }, 2},
		{"managed VM empty storage kind", func(r string) { write(r, "managed-vms/a.json", `{"command":{"storage":{"kind":""}}}`) }, 0},
		{"unreadable managed VM record", func(r string) { write(r, "managed-vms/a.json", `{not json`) }, 2},
		{"non-json file ignored", func(r string) { write(r, "managed-vms/a.txt", `{not json`) }, 0},
		{"only the receipt key", func(r string) { write(r, "storage-credentials/receipt-key", "k") }, 0},
		{"a stored credential", func(r string) { write(r, "storage-credentials/c1", "k") }, 2},
		{"zfs beats managed VM", func(r string) {
			write(r, "managed-vms/a.json", `{"applied_storage":1}`)
			write(r, "zfs-pool.json", "{}")
		}, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			c.do(root)
			if got := RequiredStateFormat(root); got != c.want {
				t.Fatalf("got %d, want %d", got, c.want)
			}
		})
	}
}

// ---- platform helpers ----

func TestExtractReleaseRefusesUnsafeArchives(t *testing.T) {
	mk := func(h tar.Header, body string) string {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		h.Size = int64(len(body))
		tw.WriteHeader(&h)
		tw.Write([]byte(body))
		tw.Close()
		p := filepath.Join(t.TempDir(), "blob")
		os.WriteFile(p, zstdBytes(t, buf.Bytes()), 0o600)
		return p
	}
	for name, h := range map[string]tar.Header{
		"parent traversal": {Name: "../escape", Typeflag: tar.TypeReg},
		"absolute path":    {Name: "/etc/passwd", Typeflag: tar.TypeReg},
		"symlink":          {Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc"},
		"device":           {Name: "dev", Typeflag: tar.TypeChar},
	} {
		dir := filepath.Join(t.TempDir(), "release")
		err := extractRelease(mk(h, "x"), dir)
		wantCode(t, err, "image_invalid")
		if _, serr := os.Stat(dir); serr == nil {
			t.Errorf("%s: a release directory exists after the refusal", name)
		}
		if _, serr := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); serr == nil {
			t.Errorf("%s: wrote outside the release directory", name)
		}
	}
}

func TestEditionAndSettings(t *testing.T) {
	dir := t.TempDir()
	prevOS, prevConf := OSReleasePath, UpdatesConfig
	t.Cleanup(func() { OSReleasePath, UpdatesConfig = prevOS, prevConf })
	OSReleasePath, UpdatesConfig = filepath.Join(dir, "os-release"), filepath.Join(dir, "updates.conf")
	if Edition() != "classic" {
		t.Fatal("a host without the image os-release is classic")
	}
	os.WriteFile(OSReleasePath, []byte("NAME=\"Nex Core\"\nIMAGE_ID=nex-core\nBUILD_ID=\"dev-7\"\n"), 0o644)
	if Edition() != "image" {
		t.Fatal("IMAGE_ID=nex-core is the image edition")
	}
	t.Setenv("NEX_UPDATE_BASE_URL", "")
	t.Setenv("NEX_UPDATE_CHANNEL", "")
	if base, ch := ImageSettings(); base != "" || ch != "stable" {
		t.Fatalf("there is no built-in base URL: %q %q", base, ch)
	}
	os.WriteFile(UpdatesConfig, []byte("repo_url=https://old.example.test/apt\nbase_url=http://mirror.example.test/nex\nchannel=testing\n"), 0o644)
	if base, ch := ImageSettings(); base != "http://mirror.example.test/nex" || ch != "testing" {
		t.Fatalf("%q %q", base, ch)
	}
	t.Setenv("NEX_UPDATE_BASE_URL", "https://env.example.test")
	if base, _ := ImageSettings(); base != "https://env.example.test" {
		t.Fatalf("environment must win: %q", base)
	}
}
