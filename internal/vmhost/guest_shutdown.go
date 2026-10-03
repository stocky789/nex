// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"strings"
	"time"
)

// RunningGuests lists the UUIDs of every libvirt domain that is running, managed or not.
func RunningGuests(ctx context.Context) ([]string, error) {
	b, err := managedVirsh(ctx, "list", "--state-running", "--uuid")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, id := range strings.Fields(string(b)) {
		if uuidPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// ShutdownGuests asks each guest to shut down through ACPI, never forcing it, and waits until
// they are off or wait elapses. It returns the guests still running, so the caller decides
// what an incomplete shutdown means; nothing is destroyed here.
func ShutdownGuests(ctx context.Context, ids []string, wait time.Duration) []string {
	for _, id := range ids {
		// A guest that is already off, or refuses the request, is judged by its state below.
		_, _ = managedVirsh(ctx, "shutdown", id)
	}
	deadline := time.Now().Add(wait)
	for {
		var running []string
		for _, id := range ids {
			if state, err := ManagedVMState(ctx, id); err != nil || state != "shut off" {
				running = append(running, id)
			}
		}
		if len(running) == 0 || !time.Now().Before(deadline) || ctx.Err() != nil {
			return running
		}
		select {
		case <-ctx.Done():
			return running
		case <-time.After(2 * time.Second):
		}
	}
}

// StartGuest starts a domain libvirt knows by UUID. It is for restoring a guest this host
// itself shut down; managed pool guests start only through pool admission instead.
func StartGuest(ctx context.Context, id string) error {
	_, err := managedVirsh(ctx, "start", id)
	return err
}
