// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"testing"
	"time"

	"nex.local/nex/internal/nexupdate"
)

// A script that runs `nex-update apply` must be able to trust the exit status: only an operation that
// finished without an error is a success.
func TestOutcomeOnlyCallsAFinishedOperationASuccess(t *testing.T) {
	now := time.Now()
	for name, tc := range map[string]struct {
		op      nexupdate.Operation
		running bool
		ok      bool
	}{
		"finished":                 {nexupdate.Operation{Phase: "staged", FinishedAt: &now}, false, true},
		"failed with a code":       {nexupdate.Operation{Phase: "failed", FinishedAt: &now, Error: &nexupdate.Error{Code: "bad_signature", Message: "x"}}, false, false},
		"cancelled":                {nexupdate.Operation{Phase: "cancelled", FinishedAt: &now}, false, false},
		"interrupted":              {nexupdate.Operation{Phase: "downloading"}, false, false},
		"still running after wait": {nexupdate.Operation{Phase: "downloading"}, true, false},
	} {
		if got := outcome(tc.op, tc.running); (got == "") != tc.ok {
			t.Errorf("%s: outcome = %q", name, got)
		}
	}
	if got := outcome(nexupdate.Operation{FinishedAt: &now, Error: &nexupdate.Error{Code: "blob_mismatch", Message: "m"}}, false); got != "blob_mismatch: m" {
		t.Errorf("the refusal code must lead the message: %q", got)
	}
}
