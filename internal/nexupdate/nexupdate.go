// SPDX-License-Identifier: AGPL-3.0-only

// Package nexupdate updates Nex Core. It is the Core's own engine (image.go, trust.go, source.go,
// offline.go, platform.go): it authenticates signed release metadata, downloads and stages a verity
// image into the inactive A/B slot of every disk, arms the boot entry on activate, and rolls back.
package nexupdate

import (
	"context"
	"os/exec"
)

// Command runs a host tool; tests replace it.
var Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}
