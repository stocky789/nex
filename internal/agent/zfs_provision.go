// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"crypto/sha256"
	"encoding/hex"
)

// zfsRepositoryID is deterministic because the master command journal is the
// durable allocation record. A redelivery therefore cannot create another pool.
func zfsRepositoryID(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return "zfs-" + hex.EncodeToString(sum[:16])
}

func zfsRegistrationOperationID(repositoryID string) string {
	sum := sha256.Sum256([]byte(repositoryID))
	return "zfsreg-" + hex.EncodeToString(sum[:16])
}
