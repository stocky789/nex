// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/localcore"
	"nex.local/nex/internal/vmhost"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "storage-guard" {
		flags := flag.NewFlagSet("storage-guard", flag.ExitOnError)
		dir := flags.String("state-dir", "/var/lib/nex-core-agent", "private agent identity and journal directory")
		flags.Parse(os.Args[2:])
		if flags.NArg() != 0 {
			log.Fatal("storage-guard accepts no positional arguments")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := vmhost.StorageGuard(ctx, *dir, os.Stdin); err != nil {
			log.Fatal(err)
		}
		return
	}
	var cfg localcore.Config
	var listen, cert, key, recoveryFile string
	flag.StringVar(&cfg.DataDir, "data-dir", "/var/lib/nex/core-local", "private local service state")
	flag.StringVar(&cfg.AgentStateDir, "agent-state-dir", "/var/lib/nex-core-agent", "private agent identity; stop standalone agent service")
	flag.StringVar(&listen, "listen", ":9443", "HTTPS listen address")
	flag.StringVar(&cfg.PoolPeerListen, "pool-peer-listen", ":9444", "Core-to-Core pool peer HTTPS listen address")
	flag.StringVar(&cert, "tls-cert", "", "TLS certificate file")
	flag.StringVar(&key, "tls-key", "", "TLS key file")
	flag.StringVar(&cfg.DocsPath, "docs-dir", "docs/api", "generated API reference directory")
	flag.StringVar(&cfg.UIPath, "ui-dir", "web/local-dist", "host local web assets")
	flag.StringVar(&cfg.SourcePath, "source-archive", "dist/nex-source.tar.gz", "matching AGPL source archive")
	flag.StringVar(&cfg.Engine.StorageDir, "storage-dir", "", "existing selected local VM storage")
	flag.StringVar(&cfg.Engine.Bridge, "bridge", "", "existing selected guest bridge")
	flag.StringVar(&cfg.Engine.ImagePath, "image", "", "preprovisioned signed appliance image")
	flag.StringVar(&cfg.Engine.ManifestPath, "manifest", "", "appliance manifest")
	flag.StringVar(&cfg.Engine.SignaturePath, "signature", "", "manifest detached signature")
	flag.StringVar(&cfg.Engine.PublicKeyPath, "trusted-key", "", "independently trusted appliance signing key")
	flag.StringVar(&cfg.Engine.VirshURI, "libvirt-uri", "qemu:///system", "local libvirt connection")
	flag.StringVar(&recoveryFile, "prepare-recovery-file", "", "root-only private JSON request: prepare empty replacement without enrollment")
	flag.Parse()
	cfg.Engine.StateDir = cfg.DataDir + "/vmhost"
	if recoveryFile != "" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		op, err := localcore.PrepareRecovery(ctx, cfg, recoveryFile)
		if err != nil {
			log.Fatal(err)
		}
		if err = json.NewEncoder(os.Stdout).Encode(op); err != nil {
			log.Fatal(err)
		}
		return
	}
	if cert == "" || key == "" {
		log.Fatal("TLS certificate and key required")
	}
	if _, err := os.Stat(cfg.SourcePath); err != nil {
		log.Fatal("matching source archive required")
	}
	cfg.Engine.StateDir = cfg.DataDir + "/vmhost"
	// The embedded agent reports the appliance engine's deployments from this journal.
	agent.ApplianceJournalDir = cfg.Engine.StateDir
	s, err := localcore.Open(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if err = s.Resume(); err != nil {
		log.Fatal("cannot resume agent identity: ", err)
	}
	server := &http.Server{Addr: listen, Handler: s.Handler(), TLSConfig: s.TLSConfig(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	peerServer := &http.Server{Addr: cfg.PoolPeerListen, Handler: s.PoolPeerHandler(), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return s.PoolPeerTLSConfig() }}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = peerServer.Shutdown(ctx)
	}()
	go func() {
		if err := peerServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("Nex Core pool peer: %v", err)
		}
	}()
	log.Printf("Nex Core local UI listening on %s", listen)
	if err = server.ListenAndServeTLS(cert, key); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
