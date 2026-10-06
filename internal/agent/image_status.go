// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"os"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
)

// efiSecureBootVar is the firmware's SecureBoot variable: four attribute bytes, then 1 when enforcing.
var efiSecureBootVar = "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"

// imageProbe reports Inventory.image on the host. It only reads: the running release comes
// from the kernel command line and os-release, the rest from the update engine's record, which the engine
// (one per host) alone writes. The record may lag a boot until nex-update-reconcile.service has run.
func imageProbe(ctx context.Context) (any, error) {
	st, err := nexupdate.ReadStatus(nexupdate.HostConfig())
	if err != nil {
		return nil, err
	}
	booted, err := nexupdate.HostPlatform().Booted(ctx)
	if err != nil {
		return nil, err
	}
	return imageInventory(st, booted, readSecureBoot(efiSecureBootVar)), nil
}

func readSecureBoot(path string) *bool {
	b, err := os.ReadFile(path)
	if err != nil || len(b) != 5 {
		return nil
	}
	on := b[4] == 1
	return &on
}

// imageInventory maps the engine's record onto the report. The booted release is what actually runs; an
// armed release is reported as staged, with the armed phase telling the two apart.
func imageInventory(st nexupdate.ImageStatus, booted nexupdate.Booted, secureBoot *bool) protocol.ImageStatus {
	out := protocol.ImageStatus{
		Booted:        protocol.ImageRelease{Label: booted.Label, Sequence: booted.Sequence, RootHash: booted.RootHash},
		Channel:       st.Channel,
		Phase:         st.Phase,
		RollbackFloor: st.RollbackFloor,
		StateFormat:   st.StateFormat,
		SecureBoot:    secureBoot,
		BaseURL:       st.BaseURL,
	}
	for _, m := range st.Media {
		media := protocol.ImageMedia{ID: m.ID, Source: m.Source, Mounted: m.Mounted, Checksums: m.Checksums}
		for _, c := range m.Channels {
			media.Channels = append(media.Channels, protocol.ImageMediaChannel{Name: c.Name, Sequence: c.Sequence})
		}
		out.Media = append(out.Media, media)
	}
	for _, s := range st.Slots {
		rel := protocol.ImageRelease{Label: s.Label, Sequence: s.Sequence, RootHash: s.RootHash, Slot: s.Slot}
		switch s.Role {
		case "booted":
			if s.RootHash == booted.RootHash {
				out.Booted.Slot = s.Slot
			}
		case "previous":
			out.Previous = &rel
		case "staged", "armed":
			out.Staged = &rel
		}
	}
	if st.Available != nil {
		out.Available = &protocol.ImageRelease{Label: st.Available.Label, Sequence: st.Available.Sequence}
	}
	if st.Failure != nil {
		out.Failure = &protocol.ImageError{Code: st.Failure.Code, Message: st.Failure.Message}
	}
	if op := st.Operation; op != nil {
		out.Operation = &protocol.ImageOperation{ID: op.ID, Kind: op.Kind, Phase: op.Phase, Sequence: op.Sequence,
			StartedAt: op.StartedAt, FinishedAt: op.FinishedAt}
		if op.Error != nil {
			out.Operation.Error = &protocol.ImageError{Code: op.Error.Code, Message: op.Error.Message}
		}
	}
	return out
}
