// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"strings"
	"testing"
)

func TestStorageClusterLocalRoutesRequireSessionAndRejectMalformedInput(t *testing.T) {
	s, _ := testService(t)
	if w := callLocal(s, "GET", "/api/local/v1/pool/storage-cluster", nil, "", "", ""); w.Code != 401 {
		t.Fatalf("anonymous status = %d", w.Code)
	}
	cookie, csrf := loginLocal(t, s)
	if w := callLocal(s, "POST", "/api/local/v1/pool/storage-cluster/apply", map[string]any{"unexpected": true}, cookie, csrf, "https://core.test"); w.Code != 400 {
		t.Fatalf("malformed apply = %d", w.Code)
	}
	if w := callLocal(s, "GET", "/api/local/v1/pool/storage-cluster", nil, cookie, csrf, ""); w.Code != 409 {
		t.Fatalf("unenrolled status = %d", w.Code)
	} else if strings.Contains(w.Body.String(), "credential") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("private data leaked: %s", w.Body.String())
	}
}
