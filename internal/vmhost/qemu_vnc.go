// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"os"
	"path/filepath"
)

// qemuVNCLossyPath is the emulator wrapper that appends lossy=on to QEMU's -vnc
// option. QEMU ignores Tight JPEG quality levels unless lossy is enabled, so
// without this wrapper the console quality presets cannot change the picture.
// Overridable in tests.
var qemuVNCLossyPath = "/usr/lib/nex/qemu-vnc-lossy"

// Wrapper content: bash is required for the argv rebuild. Debian Nex Core always
// has bash; the script is a no-op pass-through when -vnc is absent.
const qemuVNCLossyScript = `#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Enable QEMU VNC lossy encodings so noVNC quality/compression levels take effect.
set -euo pipefail
qemu=/usr/bin/qemu-system-x86_64
args=()
vnc_next=0
for a in "$@"; do
  if ((vnc_next)); then
    [[ "$a" == *lossy=* ]] || a+=",lossy=on"
    vnc_next=0
  elif [[ "$a" == -vnc ]]; then
    vnc_next=1
  fi
  args+=("$a")
done
exec "$qemu" "${args[@]}"
`

// installQEMUVNCLossy writes the emulator wrapper when missing or stale.
func installQEMUVNCLossy(path string) error {
	if path == "" {
		return nil
	}
	if b, err := os.ReadFile(path); err == nil && string(b) == qemuVNCLossyScript {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "qemu-vnc-lossy.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.WriteString(qemuVNCLossyScript); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(0755); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// qemuEmulator returns the lossy VNC wrapper path when it can be installed, or
// empty so libvirt keeps its default emulator.
func qemuEmulator() string {
	if err := installQEMUVNCLossy(qemuVNCLossyPath); err != nil {
		return ""
	}
	if _, err := os.Stat(qemuVNCLossyPath); err != nil {
		return ""
	}
	return qemuVNCLossyPath
}
