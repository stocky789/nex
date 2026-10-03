//go:build lab

// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"os"
	"time"
)

// A lab build (go build -tags lab ./cmd/nex-core-local) lets NEX_BACKUP_RUN_LIMIT (a Go
// duration) shorten the stalled-run deadline, so the lab can exercise it in seconds. Production
// builds do not read the variable.
func init() {
	if limit, err := time.ParseDuration(os.Getenv("NEX_BACKUP_RUN_LIMIT")); err == nil && limit > 0 {
		stalledRunLimit = limit
	}
}
