// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import "syscall"

func syscallMkfifo(p string) error { return syscall.Mkfifo(p, 0o644) }
