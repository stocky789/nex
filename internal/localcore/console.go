// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"net"
	"net/http"
	"nex.local/nex/internal/wsock"
	"time"
)

// console bridges the signed-in local administrator's WebSocket to a running VM's
// local VNC socket. Root on this host already owns every local guest, so any
// running domain with a local VNC socket may be viewed; nothing listens on the network.
func (s *Service) console(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "https://"+r.Host {
		fail(w, 403, "same-origin request required")
		return
	}
	socket, err := s.engine.ConsoleSocket(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, 409, "console unavailable: "+err.Error())
		return
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	vnc, err := dialer.DialContext(r.Context(), "unix", socket)
	if err != nil {
		fail(w, 503, "VM console socket refused the connection")
		return
	}
	ws, err := wsock.Accept(w, r)
	if err != nil {
		vnc.Close()
		fail(w, 400, err.Error())
		return
	}
	s.wg.Add(1)
	defer s.wg.Done()
	stop := context.AfterFunc(s.ctx, func() { ws.Close() })
	defer stop()
	wsock.Pipe(ws, vnc)
}
