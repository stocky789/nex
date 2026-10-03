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

// layersToStage picks the layers of the manifest that are staged with the release: every one but an
// on_demand layer the operator has not enabled on this host. carries reports whether the release has
// layers at all. stageLayers downloads exactly these and checkSpace counts exactly these.
func (e *Engine) layersToStage(ctx context.Context, m Manifest) (stage []Component, carries bool, err error) {
	var comps []Component
	for _, c := range m.Components {
		if c.Kind == "layer" {
			comps = append(comps, c)
		}
	}
	if len(comps) == 0 {
		return nil, false, nil
	}
	if e.cfg.Layers == nil {
		return nil, true, refuse(409, "layers_unavailable", "the release carries layers and this host has no layer store")
	}
	enabled, err := e.cfg.Layers.EnabledNames(ctx)
	if err != nil {
		return nil, true, refuse(500, "layers_unavailable", "the layer record of this host cannot be read")
	}
	for _, c := range comps {
		if !c.OnDemand || contains(enabled, c.ID) {
			stage = append(stage, c)
		}
	}
	return stage, true, nil
}

// stageLayers downloads the layers of the verified release (layersToStage) and places them in the layer
// store. A layer that is not for exactly this release's base never gets here (VerifyManifest refuses it)
// and the store refuses it again. The slot is untouched until this returns.
func (e *Engine) stageLayers(ctx context.Context, id string, v *verified, dir string) ([]LayerRef, error) {
	comps, carries, err := e.layersToStage(ctx, v.manifest)
	if err != nil || !carries {
		return nil, err
	}
	var refs []LayerRef
	for _, c := range comps {
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

// recordRunningLayers writes the layer files the running base's signed manifest lists on its record, unless
// the record has them already (a release this engine staged does; the base an image was installed with, or
// one adopted from the disk, does not). It runs before the first switch away from that base: rolling back
// switches enabled.json back from these refs. A base whose manifest the layer store cannot read has none to
// record; the layers it enabled are then judged by the layer manager at boot.
func (e *Engine) recordRunningLayers(ctx context.Context) {
	cur := e.st.Current
	if e.cfg.Layers == nil || cur == nil || len(cur.Layers) > 0 || cur.Label == "" || cur.Sequence == 0 {
		return
	}
	infos, err := e.cfg.Layers.Inventory(ctx)
	if err != nil {
		return
	}
	for _, i := range infos {
		if i.SHA256 != "" && i.RequiresBase == buildOf(cur) {
			// The inventory does not carry sizes; a recorded base is never downloaded again, so none is kept.
			cur.Layers = append(cur.Layers, LayerRef{ID: i.Name, SHA256: i.SHA256, RequiresBase: i.RequiresBase})
		}
	}
}

// restoreLayers records the running release's layers as the enabled versions again after an arm or rollback
// that switched them and then failed. The failure being reported is the arm's: a failed restore adds nothing
// to it, and the layer manager refuses a layer that does not match the base at boot.
func (e *Engine) restoreLayers(ctx context.Context, running *ReleaseRecord) {
	if running != nil {
		_ = e.switchLayers(context.WithoutCancel(ctx), *running)
	}
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
