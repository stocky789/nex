// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"nex.local/nex/internal/agent"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: nex-core-agent enroll|run|rotate|status [flags]")
	}
	mode := os.Args[1]
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	dir := f.String("state-dir", "/var/lib/nex-core-agent", "private local identity and journal directory")
	var controller, caFile, tokenFile, replace *string
	var heartbeat, interval, timeout *time.Duration
	switch mode {
	case "enroll":
		controller = f.String("controller", "", "verified HTTPS controller origin")
		caFile = f.String("ca-file", "", "trusted controller CA PEM file")
		tokenFile = f.String("token-file", "", "enrollment token file, or - for stdin")
		replace = f.String("replace-host", "", "exact current host ID for revoked-host re-enrollment")
	case "run":
		heartbeat = f.Duration("heartbeat", 10*time.Second, "report interval")
		interval = f.Duration("probe-interval", 10*time.Second, "probe interval")
		timeout = f.Duration("probe-timeout", 3*time.Second, "independent probe deadline")
		f.StringVar(&agent.ApplianceJournalDir, "appliance-journal-dir", agent.ApplianceJournalDir, "local appliance engine deploy journal, reported as this host's Controller appliances")
	case "rotate", "status":
	default:
		return fmt.Errorf("unknown command %q", mode)
	}
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	s, err := agent.OpenStore(*dir)
	if err != nil {
		return err
	}
	defer s.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	switch mode {
	case "enroll":
		if *controller == "" || *caFile == "" || *tokenFile == "" {
			return fmt.Errorf("enroll requires --controller, --ca-file and --token-file")
		}
		ca, err := os.ReadFile(*caFile)
		if err != nil {
			return err
		}
		var token []byte
		if *tokenFile == "-" {
			token, err = io.ReadAll(io.LimitReader(os.Stdin, 4097))
		} else {
			token, err = os.ReadFile(*tokenFile)
		}
		if err != nil {
			return err
		}
		if len(token) > 4096 {
			return fmt.Errorf("token exceeds 4096 bytes")
		}
		if err = agent.Enroll(ctx, s, strings.TrimRight(*controller, "/"), string(ca), string(token), *replace); err != nil {
			return err
		}
		fmt.Println("Enrolled host", s.State.HostID)
	case "rotate":
		if err = agent.Rotate(ctx, s); err != nil {
			return err
		}
		fmt.Println("Rotated host certificate", s.State.HostID)
	case "status":
		expiry, err := agent.CertificateExpiry(s.State)
		if err != nil {
			return err
		}
		fmt.Printf("Host: %s\nController: %s\nCertificate expiry: %s\nReport sequence: %d\nJournal entries: %d\n", s.State.HostID, s.State.Controller, expiry.UTC().Format(time.RFC3339), s.State.Sequence, len(s.State.Journal))
	case "run":
		return agent.Run(ctx, s, agent.RunConfig{Heartbeat: *heartbeat, ProbeInterval: *interval, ProbeTimeout: *timeout})
	}
	return nil
}
