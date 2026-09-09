// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"nex.local/nex/internal/controller"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	var cfg controller.Config
	var listen, cert, key, passwordFile string
	flag.StringVar(&cfg.DataDir, "data-dir", "/var/lib/nex-controller", "private controller state directory")
	flag.StringVar(&listen, "listen", ":8443", "HTTPS listen address")
	flag.StringVar(&cert, "tls-cert", "", "server TLS certificate PEM")
	flag.StringVar(&key, "tls-key", "", "server TLS private key PEM")
	flag.StringVar(&cfg.Username, "admin-username", "admin", "first administrator username")
	flag.StringVar(&passwordFile, "admin-password-file", "", "first administrator password file (nonempty; maximum 1024 UTF-8 bytes; no NUL or line breaks)")
	flag.StringVar(&cfg.UIPath, "ui-dir", "web/dist", "built web UI directory")
	flag.StringVar(&cfg.DocsPath, "docs-dir", "docs/api", "generated API reference directory")
	flag.StringVar(&cfg.SourcePath, "source-archive", "dist/nex-source.tar.gz", "matching source archive served at /source")
	flag.DurationVar(&cfg.StaleAfter, "stale-after", 30*time.Second, "management connection freshness window")
	flag.DurationVar(&cfg.AlertDebounce, "alert-debounce", 5*time.Second, "duration a health transition must persist before audit alert")
	flag.Parse()
	if cert == "" || key == "" {
		log.Fatal("-tls-cert and -tls-key required")
	}
	if _, err := os.Stat(cfg.SourcePath); err != nil {
		log.Fatal("matching -source-archive required: ", err)
	}
	if b, err := os.ReadFile(cert); err != nil {
		log.Fatal("cannot read -tls-cert: ", err)
	} else {
		cfg.ServerCertPEM = string(b)
	}
	if passwordFile != "" {
		b, err := os.ReadFile(passwordFile)
		if err != nil {
			log.Fatal(err)
		}
		cfg.Password = strings.TrimRight(string(b), "\r\n")
	}
	c, err := controller.Open(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()
	server := &http.Server{Addr: listen, Handler: c.Handler(), TLSConfig: c.TLSConfig(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	log.Printf("Nex Controller listening on %s", listen)
	if err = server.ListenAndServeTLS(cert, key); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
