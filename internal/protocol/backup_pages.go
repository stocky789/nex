// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "time"

// BackupInventoryPagesCapability means Core serves bounded, read-only backup
// history pages over its enrolled Controller channel.
const BackupInventoryPagesCapability = "backup-inventory-pages-v1"

type BackupRunPage struct {
	Items          []BackupRun       `json:"items"`
	NextCursor     string            `json:"next_cursor,omitempty"`
	Generation     string            `json:"generation"`
	Total          int               `json:"total"`
	AttentionCount int               `json:"attention_count"`
	LastBackupRun  *BackupRunSummary `json:"last_backup_run,omitempty"`
}

type BackupRunSummary struct {
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BackupCatalogPage struct {
	Items           []BackupRecoveryPoint `json:"items"`
	NextCursor      string                `json:"next_cursor,omitempty"`
	Generation      string                `json:"generation"`
	Total           int                   `json:"total"`
	UntestedCount   int                   `json:"untested_count"`
	LastRestoreTest *BackupTestSummary    `json:"last_restore_test,omitempty"`
	BackupDays14    []string              `json:"backup_days_14"`
}

type BackupTestSummary struct {
	Result   string    `json:"result"`
	TestedAt time.Time `json:"tested_at"`
}
