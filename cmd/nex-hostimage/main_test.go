// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// etc-audit compares the overlay upper with the image's /etc; without that /etc it would print nothing and look clean.
func TestEtcAuditNeedsTheImageEtc(t *testing.T) {
	upper := t.TempDir()
	if err := run([]string{"etc-audit", "--upper", upper}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "--lower") {
		t.Errorf("etc-audit ran without --lower: %v", err)
	}
	if err := run([]string{"etc-audit", "--lower", filepath.Join(t.TempDir(), "missing"), "--upper", upper}, &bytes.Buffer{}); err == nil {
		t.Error("etc-audit accepted a --lower that does not exist")
	}
	lower := t.TempDir()
	os.WriteFile(filepath.Join(lower, "passwd"), []byte("image\n"), 0o644)
	os.WriteFile(filepath.Join(upper, "passwd"), []byte("local\n"), 0o644)
	var out bytes.Buffer
	if err := run([]string{"etc-audit", "--lower", lower, "--upper", upper}, &out); err != nil || out.String() != "/etc/passwd: shadows image file\n" {
		t.Errorf("audit output %q: %v", out.String(), err)
	}
}
