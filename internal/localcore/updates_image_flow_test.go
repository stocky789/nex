// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/hostimage"
	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// signedTree is a release tree in the online layout, signed the way the release job signs it.
type signedTree struct {
	dir, pinned string
}

func signedRelease(t *testing.T, label string, seq int) signedTree {
	t.Helper()
	root, rel := t.TempDir(), t.TempDir()
	rootPub, rootKey, _ := ed25519.GenerateKey(rand.Reader)
	relPub, relKey, _ := ed25519.GenerateKey(rand.Reader)
	key := func(pub ed25519.PublicKey) string {
		b, _ := json.Marshal([]byte(pub))
		return strings.Trim(string(b), `"`)
	}
	now := time.Now()
	trust := nexupdate.TrustDoc{Format: 1, Version: 1, Keys: []nexupdate.TrustKey{
		{ID: "root-1", Role: "root", PublicKey: key(rootPub), NotAfter: now.Add(365 * 24 * time.Hour)},
		{ID: "rel-1", Role: "release", PublicKey: key(relPub), NotAfter: now.Add(180 * 24 * time.Hour)},
	}}
	write := func(name string, b []byte) {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	signed := func(name, sig string, v any, k ed25519.PrivateKey) []byte {
		raw, _ := json.Marshal(v)
		write(name, raw)
		write(sig, ed25519.Sign(k, raw))
		return raw
	}
	trustRaw := signed("trust.json", "trust.sig", trust, rootKey)
	pinned := filepath.Join(t.TempDir(), "trust.json")
	if err := os.WriteFile(pinned, trustRaw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := hostimage.SyntheticRelease(rel, label, seq, int64(seq), 16); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	err := filepath.Walk(rel, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		name, _ := filepath.Rel(rel, p)
		b, _ := os.ReadFile(p)
		if err = tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(name), Mode: 0o644, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if err != nil || tw.Close() != nil {
		t.Fatal(err)
	}
	rootHash, _ := os.ReadFile(filepath.Join(rel, "roothash"))
	var blob bytes.Buffer
	zw, zerr := zstd.NewWriter(&blob, zstd.WithEncoderConcurrency(1))
	if zerr != nil {
		t.Fatal(zerr)
	}
	if _, zerr = zw.Write(buf.Bytes()); zerr != nil || zw.Close() != nil {
		t.Fatal(zerr)
	}
	sum := sha256.Sum256(blob.Bytes())
	sha := hex.EncodeToString(sum[:])
	write("blobs/sha256-"+sha, blob.Bytes())
	n := strconv.Itoa(seq)
	m := nexupdate.Manifest{Format: 1, Sequence: seq, Label: label, Architecture: nexupdate.Architecture, Created: now,
		Compat: nexupdate.Compat{UpdateEngine: 1, StateFormat: 2},
		Components: []nexupdate.Component{{ID: "base", Kind: "slot-image", SHA256: sha, Bytes: int64(blob.Len()),
			Encoding: nexupdate.SlotEncoding,
			URL:      "blobs/sha256-" + sha, Required: true, VerityRootHash: strings.TrimSpace(string(rootHash))}}}
	mraw := signed("releases/"+n+"/manifest.json", "releases/"+n+"/manifest.sig", m, relKey)
	msum := sha256.Sum256(mraw)
	signed("stable/channel.json", "stable/channel.sig", nexupdate.Channel{Format: 1, Channel: "stable", ChannelSerial: 1, Sequence: seq,
		ManifestSHA256: hex.EncodeToString(msum[:]), Expires: now.Add(14 * 24 * time.Hour)}, relKey)
	return signedTree{dir: root, pinned: pinned}
}

// flowPlatform is a host with no disks: Verify is real, staging and arming are recorded.
type flowPlatform struct {
	mu      sync.Mutex
	armed   []int
	entries []hostimage.UKIEntry
}

func (*flowPlatform) Booted(context.Context) (nexupdate.Booted, error) {
	return nexupdate.Booted{RootHash: strings.Repeat("0", 64), Label: "2026.10.1", Sequence: 1}, nil
}
func (*flowPlatform) Verify(dir string) (*hostimage.Release, error) {
	return hostimage.ReadRelease(dir)
}
func (*flowPlatform) Stage(context.Context, *hostimage.Release, nexupdate.Booted) (string, error) {
	return "b", nil
}
func (p *flowPlatform) Arm(_ context.Context, rel *hostimage.Release) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed = append(p.armed, rel.Manifest.Sequence)
	p.entries = append(p.entries, hostimage.UKIEntry{Sequence: rel.Manifest.Sequence, Counted: true, Tries: 3})
	return nil
}
func (*flowPlatform) Rollback(context.Context, nexupdate.ReleaseRecord, nexupdate.ReleaseRecord) error {
	return nil
}
func (p *flowPlatform) Entries(context.Context) ([]hostimage.UKIEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]hostimage.UKIEntry(nil), p.entries...), nil
}

// TestImageEditionHostServesTheWholeUpdateFlow drives check, apply, activate and the operation lookup
// through nex-core-local's HTTP routes on an image-edition host with a signed fake source, and runs the
// Controller's relayed command through the embedded agent into the same engine.
func TestImageEditionHostServesTheWholeUpdateFlow(t *testing.T) {
	tree := signedRelease(t, "2026.10.2", 2)
	platform := &flowPlatform{}
	rebooted := make(chan struct{}, 1)
	dir := t.TempDir()
	eng := nexupdate.NewEngine(nexupdate.Config{
		Dir: filepath.Join(dir, "image"), TrustDir: filepath.Join(dir, "update"), PinnedTrust: tree.pinned, Root: t.TempDir(),
		Platform: platform, Settings: func() (string, string) { return "https://updates.example.test", "stable" },
		Source:      func(string) nexupdate.Source { return nexupdate.DirSource{Root: tree.dir} },
		Reboot:      func(context.Context) error { rebooted <- struct{}{}; return nil },
		RebootDelay: time.Millisecond,
	})
	cfg := Config{DataDir: filepath.Join(dir, "state"), AgentStateDir: filepath.Join(dir, "agent"), Authenticate: testRootAuth,
		Engine: vmhost.Config{StateDir: filepath.Join(dir, "vmstate"), StorageDir: filepath.Join(dir, "storage"), Bridge: "br0"}, ImageUpdate: eng}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cookie, csrf := loginLocal(t, s)

	code, body := post(s, "/api/local/v1/updates/check", map[string]any{}, cookie, csrf)
	avail, _ := body["available"].(map[string]any)
	if code != 200 || avail["sequence"] != float64(2) || avail["label"] != "2026.10.2" {
		t.Fatalf("check: %d %v", code, body)
	}
	waitIdle := func(id string) map[string]any {
		deadline := time.Now().Add(10 * time.Second)
		for {
			w := callLocal(s, "GET", "/api/local/v1/updates/operations/"+id, nil, cookie, csrf, "https://core.test")
			var op map[string]any
			json.Unmarshal(w.Body.Bytes(), &op)
			if w.Code == 200 && op["finished_at"] != nil {
				return op
			}
			if time.Now().After(deadline) {
				t.Fatalf("operation %s never finished: %d %s", id, w.Code, w.Body)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if w := callLocal(s, "GET", "/api/local/v1/updates/operations/nope-nope-1", nil, cookie, csrf, "https://core.test"); w.Code != 404 {
		t.Fatalf("unknown operation: %d %s", w.Code, w.Body)
	}
	if w := callLocal(s, "GET", "/api/local/v1/updates/operations/nope-nope-1", nil, "", "", "https://core.test"); w.Code != 401 {
		t.Fatalf("operations without a session: %d", w.Code)
	}

	code, body = post(s, "/api/local/v1/updates/apply", map[string]any{"request_id": "req-apply-0001", "activate": "stage_only"}, cookie, csrf)
	if code != 202 {
		t.Fatalf("apply: %d %v", code, body)
	}
	if op := waitIdle("req-apply-0001"); op["error"] != nil || op["phase"] != "staged" {
		t.Fatalf("apply finished %v", op)
	}
	if len(platform.armed) != 0 {
		t.Fatalf("stage armed the next boot: %v", platform.armed)
	}

	// The Controller's relayed activate arrives through the embedded agent, into the same engine.
	result := agent.ExecuteImageUpdate(context.Background(), protocol.Command{OperationID: "op-1", Kind: protocol.ImageUpdateCommandKind,
		ImageUpdate: &protocol.ImageUpdateRequest{HostID: "host-1", Action: "activate", RequestID: "req-activate-01"}})
	if result.Status != "succeeded" {
		t.Fatalf("relayed activate: %+v", result)
	}
	select {
	case <-rebooted:
	case <-time.After(5 * time.Second):
		t.Fatal("activate did not reboot the host")
	}
	if len(platform.armed) != 1 || platform.armed[0] != 2 {
		t.Fatalf("armed %v", platform.armed)
	}
	// A refusal is the engine's own stable code and message in a failed result.
	refused := agent.ExecuteImageUpdate(context.Background(), protocol.Command{OperationID: "op-2", Kind: protocol.ImageUpdateCommandKind,
		ImageUpdate: &protocol.ImageUpdateRequest{HostID: "host-1", Action: "cancel", RequestID: "req-cancel-0001"}})
	if refused.Status != "failed" || !strings.HasPrefix(refused.Error, "already_armed: ") {
		t.Fatalf("cancel after arming: %+v", refused)
	}
	w := callLocal(s, "GET", "/api/local/v1/updates", nil, cookie, csrf, "https://core.test")
	var st nexupdate.ImageStatus
	if json.Unmarshal(w.Body.Bytes(), &st) != nil || st.Phase != "armed" || st.Operation == nil || st.Operation.ID != "req-activate-01" {
		t.Fatalf("status after activate: %d %s", w.Code, w.Body)
	}
}
