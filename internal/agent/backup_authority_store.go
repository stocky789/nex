// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"path/filepath"
	"strings"
)

// The active backup tree is selected by the same durable peer record that grants
// master authority. An interrupted promotion therefore exposes either the old
// complete tree or the new complete tree, never a mixture of their stores.
func poolBackupStoreDir(s *Store) string {
	state, err := loadPoolPeer(s)
	if err != nil {
		return filepath.Join("/dev/null", "unavailable")
	}
	root := state.BackupActiveRoot
	if root == "" {
		return s.dir
	}
	if !strings.HasPrefix(root, "pool-backup-authority/") || filepath.Clean(root) != root ||
		strings.Contains(root[len("pool-backup-authority/"):], "/") {
		return filepath.Join("/dev/null", "unavailable")
	}
	return filepath.Join(s.dir, root)
}
