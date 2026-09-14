// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/vmhost"
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
		return fmt.Errorf("usage: nex-core-agent enroll|run|rotate|status|storage-guard|cluster-storage-ready|cluster-iscsi-session [flags]")
	}
	mode := os.Args[1]
	if mode == "cluster-iscsi-session" {
		return runClusterISCSISession(os.Args[2:], currentEUID())
	}
	f := flag.NewFlagSet(mode, flag.ContinueOnError)
	dir := f.String("state-dir", "/var/lib/nex-core-agent", "private local identity and journal directory")
	var controller, caFile, tokenFile, replace *string
	var heartbeat, interval, timeout *time.Duration
	var peerListen *string
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
		peerListen = f.String("pool-peer-listen", ":9444", "Core-to-Core pool peer HTTPS listen address")
		f.StringVar(&agent.ApplianceJournalDir, "appliance-journal-dir", agent.ApplianceJournalDir, "local appliance engine deploy journal, reported as this host's Controller appliances")
	case "rotate", "status", "storage-guard", "cluster-storage-ready":
	default:
		return fmt.Errorf("unknown command %q", mode)
	}
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	if mode == "storage-guard" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return vmhost.StorageGuard(ctx, *dir, os.Stdin)
	}
	if mode == "cluster-storage-ready" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		// The long-running agent owns the Store lock. The boot gate uses its
		// own verified, read-only durable snapshot and must never contend for it.
		return agent.ClusterStorageBootReadyFromDir(ctx, *dir)
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
		peer := &http.Server{Addr: *peerListen, Handler: agent.PoolPeerHandler(s), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return agent.PoolPeerTLSConfig(s) }}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
		go func() {
			if err := peer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				log.Printf("pool peer listener: %v", err)
			}
		}()
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = peer.Shutdown(shutdown)
		}()
		return agent.Run(ctx, s, agent.RunConfig{Heartbeat: *heartbeat, ProbeInterval: *interval, ProbeTimeout: *timeout})
	}
	return nil
}

var (
	storageISCSISessionParameters = vmhost.StorageISCSISessionParameters
	validateStorageISCSISession   = vmhost.ValidateStorageISCSISession
	currentEUID                   = os.Geteuid
)

// runClusterISCSISession is the root-only, no-mutation OCF probe entry point.
func runClusterISCSISession(args []string, euid int) error {
	flags := flag.NewFlagSet("cluster-iscsi-session", flag.ContinueOnError)
	portal := flags.String("portal", "", "canonical iSCSI portal")
	target := flags.String("target", "", "exact iSCSI target IQN")
	sessionID := flags.String("session-id", "", "Nex iSCSI session monitor identity")
	validateOnly := flags.Bool("validate-only", false, "validate static monitor parameters without querying iSCSI")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("cluster-iscsi-session accepts no positional arguments")
	}
	if euid != 0 {
		return fmt.Errorf("cluster-iscsi-session requires root")
	}
	canonicalPortal, wantID, err := storageISCSISessionParameters(*portal, *target)
	if err != nil {
		return fmt.Errorf("invalid iSCSI session parameters: %w", err)
	}
	if *portal != canonicalPortal {
		return fmt.Errorf("iSCSI portal is not canonical")
	}
	if *sessionID != wantID {
		return fmt.Errorf("iSCSI session identity does not match target and portal")
	}
	if *validateOnly {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return validateStorageISCSISession(ctx, *portal, *target, *sessionID)
}
