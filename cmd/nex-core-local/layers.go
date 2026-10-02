// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"nex.local/nex/internal/nexupdate"
)

// hostLayers builds the layer manager of this host; tests replace it with one over a fixture tree.
var hostLayers = nexupdate.HostLayers

// runLayers is the on-host entry for optional feature layers (nex-layers.service and the operator).
// activate re-verifies every enabled layer and links the good ones for systemd-sysext; it exits 0 even
// when a layer is refused, because a bad layer must never hold boot (the refusal is in the report).
func runLayers(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: layers activate|list|ensure NAME|disable NAME")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	l := hostLayers()
	switch args[0] {
	case "activate":
		report := l.Activate(ctx)
		names := make([]string, 0, len(report))
		for n := range report {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if report[n] == "" {
				fmt.Fprintf(out, "layer %s: verified\n", n)
			} else {
				fmt.Fprintf(out, "layer %s: refused (%s); left unmerged\n", n, report[n])
			}
		}
		return nil
	case "list":
		info, err := l.Inventory(ctx)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	case "ensure", "disable":
		if len(args) != 2 {
			return fmt.Errorf("layers %s needs exactly one layer name", args[0])
		}
		if args[0] == "ensure" {
			return l.Ensure(ctx, args[1])
		}
		return l.Disable(ctx, args[1])
	}
	return fmt.Errorf("unknown layers command %q", args[0])
}
