// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"nex.local/nex/internal/vmhost"
)

// Optional feature layers travel with the base they were built for: a release's layers are downloaded and
// authenticated with the slot image, placed in /var/lib/nex/layers/store before the inactive slot is
// written, and recorded as enabled only when the release is armed. The layer manager (internal/vmhost)
// owns the store and enabled.json; this file only drives it from the update transaction.

// LayerRef is one layer file of a release as the engine records it.
type LayerRef struct {
	ID           string `json:"id"`
	SHA256       string `json:"sha256"`
	Bytes        int64  `json:"bytes"`
	RequiresBase string `json:"requires_base"`
}

func buildOf(r *ReleaseRecord) string { return fmt.Sprintf("%s-%d", r.Label, r.Sequence) }

func stagedLayers(refs []LayerRef, dir string) []vmhost.StagedLayer {
	out := make([]vmhost.StagedLayer, 0, len(refs))
	for _, r := range refs {
		out = append(out, vmhost.StagedLayer{Name: r.ID, SHA256: r.SHA256, Bytes: r.Bytes, RequiresBase: r.RequiresBase, Path: filepath.Join(dir, r.SHA256)})
	}
	return out
}

// stageLayers downloads the layers of the verified release and places them in the layer store. Every
// layer is staged unless it is on_demand and the operator has not enabled it on this host. A layer that
// is not for exactly this release's base never gets here (VerifyManifest refuses it) and the store
// refuses it again. The slot is untouched until this returns.
func (e *Engine) stageLayers(ctx context.Context, id string, v *verified, dir string) ([]LayerRef, error) {
	var comps []Component
	for _, c := range v.manifest.Components {
		if c.Kind == "layer" {
			comps = append(comps, c)
		}
	}
	if len(comps) == 0 {
		return nil, nil
	}
	if e.cfg.Layers == nil {
		return nil, refuse(409, "layers_unavailable", "the release carries layers and this host has no layer store")
	}
	enabled, err := e.cfg.Layers.EnabledNames(ctx)
	if err != nil {
		return nil, refuse(500, "layers_unavailable", "the layer record of this host cannot be read")
	}
	var refs []LayerRef
	for _, c := range comps {
		if c.OnDemand && !contains(enabled, c.ID) {
			continue
		}
		if _, err = fetchBlob(ctx, v.src, blobName(c), c, dir, func(done, total int64) {
			e.setProgress(id, OpProgress{Component: c.ID, BytesDone: done, BytesTotal: total})
		}); err != nil {
			return nil, err
		}
		refs = append(refs, LayerRef{ID: c.ID, SHA256: c.SHA256, Bytes: c.Bytes, RequiresBase: c.RequiresBase})
	}
	if err = e.cfg.Layers.StageRelease(ctx, v.manifest.BuildID(), v.manifestRaw, v.manifestSig, stagedLayers(refs, dir)); err != nil {
		var le *vmhost.LayerError
		if errors.As(err, &le) {
			return nil, refuse(502, "layer_invalid", "a layer of the release failed verification: "+le.Reason)
		}
		return nil, err
	}
	for _, r := range refs {
		_ = os.Remove(filepath.Join(dir, r.SHA256))
	}
	return refs, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// switchLayers records the release's layers as the enabled versions. It runs when the release is armed
// (and when a rollback arms the previous one), never at stage.
func (e *Engine) switchLayers(ctx context.Context, r ReleaseRecord) error {
	if e.cfg.Layers == nil || len(r.Layers) == 0 {
		return nil
	}
	if err := e.cfg.Layers.SwitchRelease(ctx, buildOf(&r), stagedLayers(r.Layers, "")); err != nil {
		return refuse(500, "layers_unavailable", "the layer record of this host cannot be updated")
	}
	return nil
}

// pruneLayers drops the layer files and release manifests of every base that is no longer bootable.
// It is housekeeping after a state change that already succeeded, so a failure leaves extra files, never a
// missing one: the store refuses to remove anything when it cannot prove what is still needed.
func (e *Engine) pruneLayers(ctx context.Context, keep ...*ReleaseRecord) {
	if e.cfg.Layers == nil {
		return
	}
	var builds []string
	for _, r := range keep {
		if r == nil {
			continue
		}
		if r.Label == "" || r.Sequence == 0 {
			return // the running base's BUILD_ID is unknown, so what it needs is too
		}
		builds = append(builds, buildOf(r))
	}
	_ = e.cfg.Layers.PruneReleases(ctx, builds)
}

// opKind is the kind of a recorded operation ("" when unknown). e.mu is held.
func (e *Engine) opKind(id string) string {
	for _, r := range e.st.Operations {
		if r.Op.ID == id {
			return r.Op.Kind
		}
	}
	return ""
}
