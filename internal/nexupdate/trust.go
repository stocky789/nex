// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// Metadata formats and the engine generation this build implements. A manifest whose
// compat.update_engine is higher needs a newer Core first.
const (
	MetadataFormat = 1
	EngineVersion  = 1
	Architecture   = "amd64"
)

// SlotEncoding is the Component.Encoding of the slot-image: a tar of the release directory, zstd-compressed.
const SlotEncoding = "tar+zstd"

// Error is a refusal with a stable code, one literal message and the HTTP status the host API uses.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func refuse(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// TrustKey is one key of a trust document. PublicKey is the base64 of the raw 32-byte Ed25519 key.
type TrustKey struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"` // "root" signs trust documents, "release" signs channels and manifests
	PublicKey string    `json:"public_key"`
	NotAfter  time.Time `json:"not_after"`
	Revoked   bool      `json:"revoked,omitempty"`
}

// TrustDoc is trust.json: the keys in force. Version only grows.
type TrustDoc struct {
	Format  int        `json:"format"`
	Version int        `json:"version"`
	Keys    []TrustKey `json:"keys"`
}

// keys returns the usable keys of a role at now: not revoked, not past not_after. A zero now ignores
// not_after (the historical check of a persisted trust chain).
func (d TrustDoc) keys(role string, now time.Time) []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range d.Keys {
		if k.Role != role || k.Revoked || (!now.IsZero() && now.After(k.NotAfter)) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(k.PublicKey)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// ParseTrust decodes trust.json without verifying it (the pinned anchor in the verity root is trusted
// by being there).
func ParseTrust(raw []byte) (TrustDoc, error) {
	var d TrustDoc
	if err := json.Unmarshal(raw, &d); err != nil || d.Format != MetadataFormat || d.Version < 1 {
		return TrustDoc{}, refuse(502, "metadata_invalid", "trust.json is malformed")
	}
	return d, nil
}

func verifyAny(keys []ed25519.PublicKey, msg, sig []byte) bool {
	if len(sig) != ed25519.SignatureSize {
		return false
	}
	for _, k := range keys {
		if ed25519.Verify(k, msg, sig) {
			return true
		}
	}
	return false
}

// VerifyTrust accepts a new trust document signed by a root key of the one in force. Its version must
// exceed the current one (a replayed older document is refused) and it must still name a usable root
// key, so a rotation cannot strand the host.
func VerifyTrust(current TrustDoc, raw, sig []byte, now time.Time) (TrustDoc, error) {
	if !verifyAny(current.keys("root", now), raw, sig) {
		return TrustDoc{}, refuse(502, "bad_signature", "trust.json is not signed by a trusted root key")
	}
	d, err := ParseTrust(raw)
	if err != nil {
		return TrustDoc{}, err
	}
	if d.Version <= current.Version {
		return TrustDoc{}, refuse(502, "trust_replayed", "trust.json is not newer than the trust in force")
	}
	if len(d.keys("root", now)) == 0 || len(d.keys("release", now)) == 0 {
		return TrustDoc{}, refuse(502, "metadata_invalid", "trust.json names no usable root or release key")
	}
	return d, nil
}

// Channel is channel.json, signed by a release key.
type Channel struct {
	Format         int       `json:"format"`
	Channel        string    `json:"channel"`
	ChannelSerial  int       `json:"channel_serial"`
	Sequence       int       `json:"sequence"`
	ManifestSHA256 string    `json:"manifest_sha256"`
	Expires        time.Time `json:"expires"`
	MinSequence    int       `json:"min_sequence"`
}

// VerifyChannel authenticates channel.json under the trust in force and checks its expiry and name.
// Replay and floor checks need stored state and are the caller's.
func VerifyChannel(trust TrustDoc, raw, sig []byte, name string, now time.Time) (Channel, error) {
	if !verifyAny(trust.keys("release", now), raw, sig) {
		return Channel{}, refuse(502, "bad_signature", "channel.json is not signed by a trusted release key")
	}
	var c Channel
	if err := json.Unmarshal(raw, &c); err != nil || c.Format != MetadataFormat || c.Sequence < 1 || c.ChannelSerial < 1 || len(c.ManifestSHA256) != 64 {
		return Channel{}, refuse(502, "metadata_invalid", "channel.json is malformed")
	}
	if c.Channel != name {
		return Channel{}, refuse(502, "metadata_invalid", "channel.json is for another channel")
	}
	if !now.Before(c.Expires) {
		return Channel{}, refuse(502, "channel_expired", "channel.json has expired")
	}
	return c, nil
}

// Compat is the manifest's compatibility block. MDGeneration and MDFallbackSafe are additive to the
// plan's list: the md RAID1 superblock generation of the image's kernel (see checkMD).
type Compat struct {
	UpdateEngine          int  `json:"update_engine"`
	StateFormat           int  `json:"state_format"`
	MinStateFormat        int  `json:"min_state_format"`
	APIEpoch              int  `json:"api_epoch"`
	MinControllerAPIEpoch int  `json:"min_controller_api_epoch"`
	MixedPool             bool `json:"mixed_pool"`
	ControllerFirst       bool `json:"controller_first"`
	MDGeneration          int  `json:"md_generation,omitempty"`
	MDFallbackSafe        bool `json:"md_fallback_safe,omitempty"`
}

// Component is one downloadable part of a release.
type Component struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"` // slot-image, layer, vm-image, iso, source, compliance
	SHA256         string `json:"sha256"`
	Bytes          int64  `json:"bytes"`
	URL            string `json:"url"`
	Required       bool   `json:"required"`
	OnDemand       bool   `json:"on_demand,omitempty"`
	RequiresBase   string `json:"requires_base,omitempty"`
	VerityRootHash string `json:"verity_root_hash,omitempty"`
	// Encoding of the slot-image blob: SlotEncoding, a tar compressed with zstd. The SHA-256 and size
	// are those of the compressed bytes that are downloaded.
	Encoding string `json:"encoding,omitempty"`
	// Name is the file name of an on_demand payload under /var/lib/nex/payload/<sha256>/.
	Name string `json:"name,omitempty"`
}

// Manifest is releases/<sequence>/manifest.json.
type Manifest struct {
	Format          int         `json:"format"`
	Sequence        int         `json:"sequence"`
	Label           string      `json:"label"`
	Architecture    string      `json:"architecture"`
	Created         time.Time   `json:"created"`
	MinFromSequence int         `json:"min_from_sequence"`
	Compat          Compat      `json:"compat"`
	Components      []Component `json:"components"`
}

// SlotImage is the one component that carries the OS image.
func (m Manifest) SlotImage() (Component, error) {
	var found []Component
	for _, c := range m.Components {
		if c.Kind == "slot-image" {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		return Component{}, refuse(502, "metadata_invalid", "the manifest must carry exactly one slot-image component")
	}
	c := found[0]
	if !isHex64(c.SHA256) || !isHex64(c.VerityRootHash) || c.Bytes <= 0 || c.URL == "" {
		return Component{}, refuse(502, "metadata_invalid", "the slot-image component is incomplete")
	}
	if c.Encoding != SlotEncoding {
		return Component{}, refuse(502, "metadata_invalid", "the slot-image component is not a zstd-compressed tar")
	}
	return c, nil
}

var hex64RE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func isHex64(s string) bool { return hex64RE.MatchString(s) }

// VerifyManifest authenticates a manifest. When channelHash is not empty (the manifest the channel
// points to) the bytes must hash to it; any other release is accepted on its signature alone, since
// releases are immutable.
func VerifyManifest(trust TrustDoc, raw, sig []byte, channelHash string, now time.Time) (Manifest, error) {
	if channelHash != "" {
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != channelHash {
			return Manifest{}, refuse(502, "manifest_mismatch", "the manifest does not match the hash in channel.json")
		}
	}
	if !verifyAny(trust.keys("release", now), raw, sig) {
		return Manifest{}, refuse(502, "bad_signature", "manifest.json is not signed by a trusted release key")
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Format != MetadataFormat || m.Sequence < 1 || m.Label == "" {
		return Manifest{}, refuse(502, "metadata_invalid", "manifest.json is malformed")
	}
	if m.Architecture != Architecture {
		return Manifest{}, refuse(409, "wrong_architecture", "the release is for another architecture")
	}
	seen := map[string]bool{}
	for _, c := range m.Components {
		if c.ID == "" || seen[c.ID] || !isHex64(c.SHA256) || c.Bytes < 0 {
			return Manifest{}, refuse(502, "metadata_invalid", "manifest.json has an invalid component")
		}
		seen[c.ID] = true
		if err := checkComponent(m, c); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

// VerifyReleaseManifest authenticates a stored release manifest the way the engine does: under the trust in
// force (the pinned anchor plus every adopted rotation, never going back) with VerifyManifest, so a revoked
// or expired release key no longer vouches for anything. The layer manager calls it at every boot and
// ensure, which makes trust.json the one trust anchor of updates and layers. cfg is the engine Config
// (zero fields take the host defaults).
func VerifyReleaseManifest(cfg Config, raw, sig []byte) error {
	e := NewEngine(cfg)
	defer e.Close()
	e.mu.Lock()
	err := e.load()
	e.mu.Unlock()
	if err != nil {
		return err
	}
	trust, err := e.loadTrust()
	if err != nil {
		return err
	}
	_, err = VerifyManifest(trust, raw, sig, "", e.cfg.Now())
	return err
}

var layerIDRE = regexp.MustCompile(`^nex-[a-z0-9]+(-[a-z0-9]+)*$`)
var payloadNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// BuildID is the BUILD_ID the image of this release states about itself; a layer is built for exactly one.
func (m Manifest) BuildID() string { return fmt.Sprintf("%s-%d", m.Label, m.Sequence) }

// checkComponent holds the per-kind rules that need no stored state. A layer must name the base it was
// built for and that base must be this release (a layer of another build is refused, so a release can
// never carry a layer that systemd-sysext would reject); an on_demand payload needs a safe file name.
func checkComponent(m Manifest, c Component) error {
	switch {
	case c.Kind == "layer":
		if !layerIDRE.MatchString(c.ID) || c.Bytes <= 0 || c.URL == "" || (c.VerityRootHash != "" && !isHex64(c.VerityRootHash)) {
			return refuse(502, "metadata_invalid", "manifest.json has an invalid layer component")
		}
		if c.RequiresBase != m.BuildID() {
			return refuse(502, "layer_base_mismatch", "a layer in the release was built for another base")
		}
	case c.OnDemand && c.Kind != "source" && c.Kind != "compliance":
		if !payloadNameRE.MatchString(c.Name) || c.Bytes <= 0 {
			return refuse(502, "metadata_invalid", "manifest.json has an on_demand payload without a valid file name")
		}
	}
	return nil
}
