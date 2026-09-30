// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"nex.local/nex/internal/protocol"
	"strings"
	"sync"
	"time"
)

// statsChannelIdle closes a channel the Controller stopped using; the Controller closes its
// side after five minutes, so this only catches a Controller that vanished without a FIN.
const statsChannelIdle = 6 * time.Minute

// serveStatsChannel dials the Controller over the enrolled mTLS identity, upgrades the
// connection, then answers the Controller's HTTP/1.1 stats queries on it until either side
// closes it. No listener is opened on the host.
func serveStatsChannel(ctx context.Context, s State, req protocol.StatsChannelRequest, history http.Handler) error {
	if req.Token == "" || len(req.Token) > 128 || strings.ContainsAny(req.Token, "/\\?#% \r\n") {
		return errors.New("invalid stats channel token")
	}
	config, err := tlsConfig(s, true)
	if err != nil {
		return err
	}
	config.NextProtos = []string{"http/1.1"}
	u, _ := url.Parse(s.Controller)
	port := u.Port()
	if port == "" {
		port = "443"
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: config}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return fmt.Errorf("controller stats connection failed: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	request := "POST /api/v1/agent/stats/" + url.PathEscape(req.Token) + " HTTP/1.1\r\nHost: " + u.Host + "\r\nConnection: Upgrade\r\nUpgrade: nex-stats\r\nContent-Length: 0\r\n\r\n"
	if _, err = conn.Write([]byte(request)); err != nil {
		return err
	}
	r := bufio.NewReader(conn)
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		return fmt.Errorf("controller stats response: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("controller refused stats channel (HTTP %d)", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	serveStatsConn(ctx, bufferedConn{conn, r}, history)
	return nil
}

// statsChannelHandler answers the Controller's read-only queries on a stats channel: the
// host's usage history, and its hardware, which is observed on request and kept nowhere.
func statsChannelHandler(history http.Handler, store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/stats", history)
	mux.HandleFunc("GET /backup-runs", func(w http.ResponseWriter, r *http.Request) { serveBackupRunPage(w, r, store) })
	mux.HandleFunc("GET /backup-orphans", func(w http.ResponseWriter, r *http.Request) { serveBackupOrphans(w, r, store) })
	mux.HandleFunc("GET /backup-catalog", func(w http.ResponseWriter, r *http.Request) { serveBackupCatalogPage(w, r, store) })
	mux.HandleFunc("GET /hardware", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(readHardware(r.Context(), "/"))
	})
	return mux
}

// bufferedConn keeps bytes the upgrade response reader already buffered.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// serveStatsConn serves HTTP/1.1 on one already-open connection until it closes or ctx ends.
func serveStatsConn(ctx context.Context, conn net.Conn, handler http.Handler) {
	l := &oneConnListener{conn: conn, closed: make(chan struct{})}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       statsChannelIdle,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed || state == http.StateHijacked {
				l.Close()
			}
		},
	}
	stop := context.AfterFunc(ctx, func() { server.Close() })
	defer stop()
	_ = server.Serve(l) // returns once the connection has closed
}

// oneConnListener hands out one connection, then blocks until that connection is closed.
type oneConnListener struct {
	conn   net.Conn
	once   sync.Once
	close  sync.Once
	closed chan struct{}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = l.conn })
	if conn != nil {
		return conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error {
	l.close.Do(func() { close(l.closed) })
	return nil
}

func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }
