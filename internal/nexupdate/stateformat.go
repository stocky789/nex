// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RequiredStateFormat is the lowest state_format an image must have to read this host's retained
// state, ported from core_storage_format in the retired storage-downgrade-guard.py. A durable ZFS
// record means 3; managed VM storage records mean 2; nothing retained means 0. An unreadable record
// counts as present: running an older image over a live Nex-created pool is the unrecoverable outcome.
// root is "/" on a host.
func RequiredStateFormat(root string) int {
	agent := filepath.Join(root, "var/lib/nex-core-agent")
	for _, name := range []string{"zfs-pool.json", "zfs-lifecycle.json"} {
		if _, err := os.Lstat(filepath.Join(agent, name)); err == nil || !errors.Is(err, fs.ErrNotExist) {
			return 3
		}
	}
	receipts := filepath.Join(agent, "zfs-provision")
	if st, err := os.Lstat(receipts); err == nil {
		if st.Mode()&fs.ModeSymlink != 0 {
			return 3
		}
		entries, err := os.ReadDir(receipts)
		if err != nil || len(entries) > 0 {
			return 3
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 3
	}
	if hasCoreV2State(agent) {
		return 2
	}
	return 0
}

// v2Markers are the keys that only managed-storage records of format 2 carry.
var v2Markers = []string{`"applied_storage":`, `"storage_lifecycle":`, `"credential_receipt":`, `"credential_request_id":`, `"tombstone":`}

func hasCoreV2State(agent string) bool {
	dir := filepath.Join(agent, "managed-vms")
	if _, err := os.Stat(dir); err == nil {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		for _, e := range entries {
			if filepath.Ext(e.Name()) != ".json" || e.Type()&fs.ModeSymlink != 0 {
				continue
			}
			if recordIsV2(filepath.Join(dir, e.Name())) {
				return true
			}
		}
	}
	credentials := filepath.Join(agent, "storage-credentials")
	if _, err := os.Stat(credentials); err != nil {
		return false
	}
	entries, err := os.ReadDir(credentials)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if e.Name() != "receipt-key" {
			return true
		}
	}
	return false
}

// recordIsV2 is true for a managed VM record that uses format-2 storage fields, and for one that
// cannot be read or understood.
func recordIsV2(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var data map[string]any
	if json.Unmarshal(b, &data) != nil || data == nil {
		return true
	}
	compact, err := json.Marshal(data)
	if err != nil {
		return true
	}
	text := string(compact)
	for _, k := range v2Markers {
		if strings.Contains(text, k) {
			return true
		}
	}
	command, _ := data["command"].(map[string]any)
	storage, _ := command["storage"].(map[string]any)
	for _, k := range []string{"kind", "generation", "source", "identity"} {
		if truthy(storage[k]) {
			return true
		}
	}
	return false
}

// truthy is Python's truth value for decoded JSON: absent, null, false, 0, "" and empty containers are false.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}
