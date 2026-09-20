// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"nex.local/nex/internal/protocol"
)

// A promoted master holds ISO records it adopted while it was a member. Removal never
// unlinks an adopted file, so the pool must be able to tell the two apart before it offers
// an operator a removal that would drop the record and leave the file behind.
func TestMediaAdoptedHereDistinguishesAnAdoptionFromAnImport(t *testing.T) {
	e, media, _ := sharedMediaFixture(t, "media-adopted")
	if e.MediaAdoptedHere(media.ID) {
		t.Fatal("an ISO that is not recorded at all was reported as adopted")
	}
	if err := e.AdoptSharedMedia(context.Background(), media); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if !e.MediaAdoptedHere(media.ID) {
		t.Fatal("an adopted ISO was reported as this host's own import")
	}

	// The same record, journalled by an import this host performed itself.
	imported := media
	imported.ID, imported.OperationID, imported.Status = "media-imported", "import-1", "succeeded"
	entry := managedJournal{
		Command: protocol.Command{OperationID: "import-1", Kind: "iso-import", Media: &imported},
		Result:  protocol.Result{OperationID: "import-1", Status: "succeeded"},
	}
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(e.stateDir, "import-1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if e.MediaAdoptedHere(imported.ID) {
		t.Fatal("an ISO this host imported was reported as adopted")
	}
}
