// SPDX-License-Identifier: AGPL-3.0-only

// nex-update drives the image update engine from a shell on the host: the same engine nex-core-local
// serves over the host-local API, for recovery and for the QEMU lab proof. It prints the engine's JSON
// and exits non-zero with the refusal code when the engine refuses.
//
//	nex-update status
//	nex-update check
//	nex-update apply [-sequence N] [-activate stage_only|reboot] -request-id ID
//	nex-update activate|rollback|cancel -request-id ID
//	nex-update state-guard
//
// state-guard is nex-state-guard.service: it fails when the host's retained state needs a newer
// state_format than this image reads, which keeps libvirtd and nex-core-local from starting over it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"nex.local/nex/internal/nexupdate"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: nex-update status|check|apply|activate|rollback|cancel|state-guard")
	}
	cmd, args := os.Args[1], os.Args[2:]
	if cmd == "state-guard" {
		stateGuard()
		return
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	requestID := fs.String("request-id", "", "operation ID (idempotency key)")
	sequence := fs.Int("sequence", 0, "release sequence (apply; default: the channel's)")
	activate := fs.String("activate", "stage_only", "apply: stage_only or reboot")
	vms := fs.String("running-vms", "refuse", "refuse or shutdown")
	wait := fs.Duration("wait", 20*time.Minute, "apply: how long to wait for the staging to finish")
	_ = fs.Parse(args)

	cfg := nexupdate.HostConfig()
	cfg.Platform = nexupdate.HostPlatform()
	cfg.Reboot = nexupdate.SystemReboot
	cfg.RebootDelay = time.Second
	e := nexupdate.NewEngine(cfg)
	defer e.Close()
	ctx := context.Background()

	switch cmd {
	case "status":
		print(e.Status(ctx))
	case "check":
		print(e.Check(ctx))
	case "apply":
		op, _, err := e.Apply(ctx, nexupdate.ApplyRequest{RequestID: *requestID, Sequence: *sequence, Activate: *activate, RunningVMs: *vms})
		if err != nil {
			refused(err)
		}
		deadline := time.Now().Add(*wait)
		for time.Now().Before(deadline) {
			st, err := e.Status(ctx)
			if err != nil {
				refused(err)
			}
			if !st.Running {
				break
			}
			time.Sleep(time.Second)
		}
		fmt.Fprintln(os.Stderr, "apply operation", op.ID)
		if *activate == "reboot" {
			// The engine reboots a moment after arming; keep the process alive until it does.
			time.Sleep(30 * time.Second)
		}
		print(e.Status(ctx))
	case "activate", "rollback", "cancel":
		call := map[string]func(context.Context, nexupdate.ActionRequest) (nexupdate.Operation, bool, error){
			"activate": e.Activate, "rollback": e.Rollback, "cancel": e.Cancel}[cmd]
		if _, _, err := call(ctx, nexupdate.ActionRequest{RequestID: *requestID, RunningVMs: *vms}); err != nil {
			refused(err)
		}
		if cmd != "cancel" {
			time.Sleep(30 * time.Second)
		}
		print(e.Status(ctx))
	default:
		fail("unknown command " + cmd)
	}
}

// stateGuard exits 1 when RequiredStateFormat exceeds the format this image reads, which os-release
// records as NEX_STATE_FORMAT. An image without the key reads nothing and passes (a classic host).
func stateGuard() {
	var supported int
	if _, err := fmt.Sscan(nexupdate.OSReleaseValue("NEX_STATE_FORMAT"), &supported); err != nil {
		return
	}
	if required := nexupdate.RequiredStateFormat("/"); required > supported {
		fail(fmt.Sprintf("nex-state-guard: the retained state needs state_format %d but this image reads %d; boot the previous image", required, supported))
	}
}

func print(v any, err error) {
	if err != nil {
		refused(err)
	}
	b, _ := json.MarshalIndent(v, "", " ")
	fmt.Println(string(b))
}

func refused(err error) {
	var re *nexupdate.Error
	if errors.As(err, &re) {
		fail(re.Code + ": " + re.Message)
	}
	fail(err.Error())
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}
