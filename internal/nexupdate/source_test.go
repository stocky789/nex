// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFullLengthCorruptPartGetsOneCleanDownload(t *testing.T) {
	for _, corruptSource := range []bool{false, true} {
		name := "valid source"
		if corruptSource {
			name = "corrupt source"
		}
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			blob := r.pub.release(2, "v2", true)
			dir := filepath.Join(r.cfg.Dir, "staging", "2")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, blob.sha+".part"), bytes.Repeat([]byte{0x42}, len(blob.blob)), 0o600); err != nil {
				t.Fatal(err)
			}
			r.src.flip = corruptSource
			if _, _, err := r.apply("req-full-part-0001", nil); err != nil {
				t.Fatal(err)
			}
			op := r.wait()
			if len(r.src.offsets) != 1 || r.src.offsets[0] != 0 {
				t.Fatalf("download offsets %v, want one clean download", r.src.offsets)
			}
			if corruptSource {
				if op.Error == nil || op.Error.Code != "blob_mismatch" || len(r.plat.staged) != 0 {
					t.Fatalf("corrupt source reached a slot: operation=%+v staged=%v", op, r.plat.staged)
				}
			} else if op.Error != nil || op.Phase != PhaseStaged || len(r.plat.staged) != 1 {
				t.Fatalf("clean retry did not stage: operation=%+v staged=%v", op, r.plat.staged)
			}
		})
	}
}
