// SPDX-License-Identifier: AGPL-3.0-only

// Package rootfs opens the short, explicit writable window that the few operations
// which change the operating system (package installs, bootloader writes) need on a
// host whose root file system is mounted read-only. Everything else Nex Core writes
// lives under /etc, /var and /run.
package rootfs

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
)

// Run executes one command and returns its output. It must run commands in the mount
// namespace where the window's writer will run: the Core process itself for a package
// install, a transient system-manager unit for a bootloader write. That is where the root's
// state is read and changed, because a sandboxed service sees its own read-only view of /.
type Run func(ctx context.Context, name string, args ...string) ([]byte, error)

// window is the one lock every window holds from before the root's state is read until it
// has been restored. Without it, a second caller would see the first one's writable root as
// the normal state, skip its own remount, and be made read-only mid-write when the first
// one closes.
var window = make(chan struct{}, 1)

// OpenWritable makes a read-only / writable and returns the function that makes it
// read-only again and releases the window. A writable root (an older host, the Controller
// appliance) needs no remount, but still holds the window so a concurrent writer cannot
// close it. If the final remount fails the root stays writable until the next boot, when
// nex-root-ro.service mounts it read-only again.
func OpenWritable(ctx context.Context, run Run) (func(), error) {
	select {
	case window <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	release := func() { once.Do(func() { <-window }) }
	out, err := run(ctx, "findmnt", "-nro", "OPTIONS", "-T", "/")
	if err != nil {
		release()
		return nil, errors.New("could not read the root file system's mount options: " + strings.TrimSpace(string(out)+" "+err.Error()))
	}
	lines := strings.Fields(string(out))
	if len(lines) == 0 || !slices.Contains(strings.Split(lines[len(lines)-1], ","), "ro") {
		return release, nil
	}
	if out, err := run(ctx, "mount", "-o", "remount,rw", "/"); err != nil {
		release()
		return nil, errors.New("could not make the root file system writable: " + strings.TrimSpace(string(out)+" "+err.Error()))
	}
	return func() {
		defer release()
		_, _ = run(context.Background(), "mount", "-o", "remount,ro", "/")
	}, nil
}
