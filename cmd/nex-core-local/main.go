// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"nex.local/nex/internal/localcore"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var cfg localcore.Config
	var listen, cert, key, recoveryFile string
	flag.StringVar(&cfg.DataDir, "data-dir", "/var/lib/nex/core-local", "private local service state")
	flag.StringVar(&cfg.AgentStateDir, "agent-state-dir", "/var/lib/nex-core-agent", "private agent identity; stop standalone agent service")
	flag.StringVar(&listen, "listen", ":9443", "HTTPS listen address")
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
	s, err := localcore.Open(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if err = s.Resume(); err != nil {
		log.Fatal("cannot resume agent identity: ", err)
	}
	server := &http.Server{Addr: listen, Handler: s.Handler(), TLSConfig: s.TLSConfig(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("Nex Core local UI listening on %s", listen)
	if err = server.ListenAndServeTLS(cert, key); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
