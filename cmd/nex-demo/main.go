// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nex.local/nex/internal/demo"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", "127.0.0.1:8090", "Loopback development TCP address (ignored when -socket is set)")
	socket := flag.String("socket", "", "UNIX socket path; refuses any existing filesystem entry")
	ui := flag.String("ui-dir", "/app/web", "Built demo web assets directory")
	docs := flag.String("docs-dir", "docs/api", "Authoritative API reference assets directory")
	source := flag.String("source-archive", "/app/source.tar.gz", "Exact corresponding source archive")
	origin := flag.String("public-origin", "https://demo.example.com", "Fixed browser origin; HTTPS required in production")
	insecure := flag.Bool("insecure-cookie", false, "Development only: allow non-Secure cookies with a loopback HTTP origin")
	healthcheck := flag.Bool("healthcheck", false, "Check the existing demo server's /healthz and exit")
	flag.Parse()
	if *healthcheck {
		return checkHealth(*listen, *socket)
	}
	if *socket != "" && *insecure {
		return errors.New("UNIX production mode does not allow insecure cookies")
	}
	server, err := demo.New(demo.Config{UIDir: *ui, DocsDir: *docs, SourceArchive: *source, PublicOrigin: *origin, InsecureCookie: *insecure})
	if err != nil {
		return err
	}
	defer server.Close()
	listener, err := openListener(*listen, *socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- httpServer.Serve(listener) }()
	log.Printf("Read-only simulated Nex demo listening on %s", listener.Addr())
	select {
	case err := <-finished:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdown)
	}
}

func openListener(address, socket string) (net.Listener, error) {
	if socket == "" {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("TCP demo listener must use a numeric loopback address; production uses -socket")
		}
		return net.Listen("tcp", address)
	}
	if info, err := os.Lstat(socket); err == nil {
		// A crashed container (restart policy) leaves its socket behind; remove
		// only a socket nobody is listening on, never a file or a live server.
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("socket path already exists; refusing to replace it")
		}
		if conn, dialErr := net.DialTimeout("unix", socket, time.Second); dialErr == nil {
			conn.Close()
			return nil, errors.New("socket path is served by a live process; refusing to replace it")
		}
		if err := os.Remove(socket); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Bind itself must create a private socket, not briefly expose one before chmod.
	mask := syscall.Umask(0077)
	listener, err := net.Listen("unix", socket)
	syscall.Umask(mask)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(socket, 0600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

// healthTimeout bounds one health probe; tests on a saturated runner raise it.
var healthTimeout = 3 * time.Second

func checkHealth(address, socket string) error {
	transport := &http.Transport{Proxy: nil}
	if socket != "" {
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: healthTimeout}
	endpoint := "http://" + address + "/healthz"
	if socket != "" {
		endpoint = "http://localhost/healthz"
	}
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health returned HTTP %d", response.StatusCode)
	}
	var health struct {
		Status    string `json:"status"`
		Simulated bool   `json:"simulated"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1024))
	if err := decoder.Decode(&health); err != nil {
		return err
	}
	if health.Status != "ok" || !health.Simulated {
		return errors.New("unexpected demo health response")
	}
	return nil
}
