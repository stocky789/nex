// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ManifestFile is one entry of a release manifest's file list.
type ManifestFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is manifest.json of a release directory (written by image/release.py).
type Manifest struct {
	Label      string                  `json:"label"`
	Sequence   int                     `json:"sequence"`
	BuildID    string                  `json:"build_id"`
	RootHash   string                  `json:"roothash"`
	Files      map[string]ManifestFile `json:"files"`
	Components json.RawMessage         `json:"components,omitempty"`
}

// Release is a verified image release directory.
type Release struct {
	Dir      string
	Manifest Manifest
	UKI      string // file name of the UKI, nex-core_<label>_<sequence>.efi
	bios     biosCache
}

var (
	rootHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ukiNameRE  = regexp.MustCompile(`^nex-core_[A-Za-z0-9.~_-]+_[0-9]+\.efi$`)
)

// Erofs, Verity and UKIPath are the release files by role.
func (r *Release) Erofs() string   { return filepath.Join(r.Dir, "root.erofs") }
func (r *Release) Verity() string  { return filepath.Join(r.Dir, "root.verity") }
func (r *Release) UKIPath() string { return filepath.Join(r.Dir, r.UKI) }

// ReadRelease loads a release directory and verifies every listed file by size and SHA-256, the
// roothash file against the manifest, and root.verity against root.erofs. Nothing in the directory
// outside the manifest is trusted or used.
func ReadRelease(dir string) (*Release, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("release %s: %w", dir, err)
	}
	r := &Release{Dir: dir}
	if err = json.Unmarshal(b, &r.Manifest); err != nil {
		return nil, fmt.Errorf("release %s: manifest.json: %w", dir, err)
	}
	m := r.Manifest
	if !rootHashRE.MatchString(m.RootHash) {
		return nil, fmt.Errorf("release %s: manifest roothash %q is not 64 lowercase hex characters", dir, m.RootHash)
	}
	for name, f := range m.Files {
		if !fs.ValidPath(name) || name == "manifest.json" {
			return nil, fmt.Errorf("release %s: invalid file name %q in the manifest", dir, name)
		}
		if err = checkFile(filepath.Join(dir, filepath.FromSlash(name)), f); err != nil {
			return nil, fmt.Errorf("release %s: %s: %w", dir, name, err)
		}
		if ukiNameRE.MatchString(name) {
			if r.UKI != "" {
				return nil, fmt.Errorf("release %s: more than one UKI (%s, %s)", dir, r.UKI, name)
			}
			r.UKI = name
		}
	}
	for _, need := range []string{"root.erofs", "root.verity", "roothash"} {
		if _, ok := m.Files[need]; !ok {
			return nil, fmt.Errorf("release %s: manifest lists no %s", dir, need)
		}
	}
	if r.UKI == "" {
		return nil, fmt.Errorf("release %s: manifest lists no nex-core_<label>_<sequence>.efi", dir)
	}
	if !strings.HasPrefix(r.UKI, "nex-core_"+m.Label+"_") || !strings.HasSuffix(r.UKI, fmt.Sprintf("_%d.efi", m.Sequence)) {
		return nil, fmt.Errorf("release %s: UKI %s does not match label %q and sequence %d", dir, r.UKI, m.Label, m.Sequence)
	}
	hb, err := os.ReadFile(filepath.Join(dir, "roothash"))
	if err != nil || strings.TrimSpace(string(hb)) != m.RootHash {
		return nil, fmt.Errorf("release %s: roothash file does not match the manifest", dir)
	}
	if err = VerifyVerity(r.Erofs(), r.Verity(), m.RootHash); err != nil {
		return nil, fmt.Errorf("release %s: %w", dir, err)
	}
	return r, nil
}

func checkFile(p string, want ManifestFile) error {
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != want.Size {
		return fmt.Errorf("is %d bytes, the manifest says %d", n, want.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want.SHA256 {
		return fmt.Errorf("sha256 is %s, the manifest says %s", got, want.SHA256)
	}
	return nil
}

// SlotIDs are the partition GUIDs of a slot for a root hash, as the Discoverable Partitions
// Specification derives them: the root partition takes the first 128 bits, the verity
// partition the last 128 bits, each written as a textual UUID.
func SlotIDs(rootHash string) (root, verity GUID, err error) {
	if !rootHashRE.MatchString(rootHash) {
		return root, verity, fmt.Errorf("root hash %q is not 64 lowercase hex characters", rootHash)
	}
	text := func(h string) string { return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:] }
	root, err = ParseGUID(text(rootHash[:32]))
	if err != nil {
		return root, verity, err
	}
	verity, err = ParseGUID(text(rootHash[32:]))
	return root, verity, err
}
