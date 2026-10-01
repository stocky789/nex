// SPDX-License-Identifier: AGPL-3.0-only
package rootfs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// rootHost is a fake host: findmnt reports the root's current options and the remounts
// change them, so the sequence of states a window leaves behind can be observed.
type rootHost struct {
	mu      sync.Mutex
	options string
	calls   []string
	failRW  bool
	failFnd bool
}

func (h *rootHost) run(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	line := strings.Join(append([]string{name}, args...), " ")
	h.calls = append(h.calls, line)
	switch line {
	case "findmnt -nro OPTIONS -T /":
		if h.failFnd {
			return []byte("findmnt: not found"), errors.New("exit status 127")
		}
		return []byte(h.options + "\n"), nil
	case "mount -o remount,rw /":
		if h.failRW {
			return []byte("mount: permission denied"), errors.New("exit status 32")
		}
		h.options = strings.Replace(h.options, "ro,", "rw,", 1)
	case "mount -o remount,ro /":
		h.options = strings.Replace(h.options, "rw,", "ro,", 1)
	}
	return nil, nil
}

func (h *rootHost) state() (string, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.options, strings.Join(h.calls, "|")
}

func TestOpenWritableRemountsOnlyAReadOnlyRoot(t *testing.T) {
	cases := []struct{ options, afterOpen, afterRestore string }{
		{"ro,relatime", "rw,relatime", "ro,relatime"},
		{"rw,relatime", "rw,relatime", "rw,relatime"},
		{"rw,noro", "rw,noro", "rw,noro"},
	}
	for _, c := range cases {
		host := &rootHost{options: c.options}
		restore, err := OpenWritable(context.Background(), host.run)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := host.state(); got != c.afterOpen {
			t.Fatalf("%s: after open %q, want %q", c.options, got, c.afterOpen)
		}
		restore()
		if got, _ := host.state(); got != c.afterRestore {
			t.Fatalf("%s: after restore %q, want %q", c.options, got, c.afterRestore)
		}
	}
}

// The root is read where the writer runs, through the runner: a caller whose own view of /
// is read-only for sandbox reasons must not turn a writable host read-only, which reading
// its own mount table would have done.
func TestOpenWritableTrustsTheRunnerNotTheCallersNamespace(t *testing.T) {
	host := &rootHost{options: "rw,relatime"}
	restore, err := OpenWritable(context.Background(), host.run)
	if err != nil {
		t.Fatal(err)
	}
	restore()
	if _, calls := host.state(); strings.Contains(calls, "remount") {
		t.Fatalf("remounted a root the runner reported writable: %s", calls)
	}
}

func TestOpenWritableReportsAFailedRemountAndReleasesTheWindow(t *testing.T) {
	host := &rootHost{options: "ro,relatime", failRW: true}
	if restore, err := OpenWritable(context.Background(), host.run); err == nil || restore != nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("%v", err)
	}
	host.failRW = false
	restore, err := OpenWritable(context.Background(), host.run)
	if err != nil {
		t.Fatalf("window stayed locked after a failed open: %v", err)
	}
	restore()
}

// A root whose state cannot be read is not assumed writable: nothing is installed.
func TestOpenWritableRefusesWhenTheRootStateIsUnreadable(t *testing.T) {
	host := &rootHost{options: "ro,relatime", failFnd: true}
	if restore, err := OpenWritable(context.Background(), host.run); err == nil || restore != nil {
		t.Fatalf("opened a window on an unreadable root: %v", err)
	}
	host.failFnd = false
	restore, err := OpenWritable(context.Background(), host.run)
	if err != nil {
		t.Fatalf("window stayed locked after an unreadable root: %v", err)
	}
	restore()
}

// Two writers never overlap: the second reads the root only after the first has restored it,
// so it opens its own window instead of mistaking the first one's writable root for the
// normal state and being closed underneath itself.
func TestOpenWritableSerializesWindows(t *testing.T) {
	host := &rootHost{options: "ro,relatime"}
	first, err := OpenWritable(context.Background(), host.run)
	if err != nil {
		t.Fatal(err)
	}
	secondOpened := make(chan func())
	go func() {
		restore, err := OpenWritable(context.Background(), host.run)
		if err != nil {
			t.Error(err)
		}
		secondOpened <- restore
	}()
	select {
	case <-secondOpened:
		t.Fatal("a second window opened while the first was still open")
	case <-time.After(100 * time.Millisecond):
	}
	first()
	var second func()
	select {
	case second = <-secondOpened:
	case <-time.After(5 * time.Second):
		t.Fatal("the second window never opened after the first closed")
	}
	if options, _ := host.state(); options != "rw,relatime" {
		t.Fatalf("second window did not open its own: %q", options)
	}
	second()
	if options, _ := host.state(); options != "ro,relatime" {
		t.Fatalf("root not read-only after both windows: %q", options)
	}
}

func TestOpenWritableGivesUpWhenCancelledWhileWaiting(t *testing.T) {
	host := &rootHost{options: "ro,relatime"}
	first, err := OpenWritable(context.Background(), host.run)
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if restore, err := OpenWritable(ctx, host.run); !errors.Is(err, context.DeadlineExceeded) || restore != nil {
		t.Fatalf("%v", err)
	}
}
