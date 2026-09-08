// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/agent"
)

type HandoffConfig struct{ ControllerURL, ServerName, TrustPEM, AdminPassword, SiteName, PoolName, VMID, Name, RequestID, AgentStateDir string }

type handoffHTTP struct {
	client       *http.Client
	origin, csrf string
}

func newHandoffHTTP(origin, trust string, serverName ...string) (*handoffHTTP, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("controller must be an HTTPS origin")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(trust)) {
		return nil, errors.New("controller trust contains no certificates")
	}
	name := ""
	if len(serverName) > 0 {
		name = serverName[0]
	}
	jar, _ := cookiejar.New(nil)
	return &handoffHTTP{origin: strings.TrimRight(origin, "/"), client: &http.Client{Timeout: 15 * time.Second, Jar: jar, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: name}}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("controller redirects refused") }}}, nil
}
func (h *handoffHTTP) call(ctx context.Context, method, path string, in, out any) (int, error) {
	data, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, h.origin+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", h.origin)
	req.Header.Set("X-CSRF-Token", h.csrf)
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("controller connection failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("controller rejected %s (HTTP %d)", path, resp.StatusCode)
	}
	if out == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
		return resp.StatusCode, err
	}
	return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}

func (h *handoffHTTP) waitReady(ctx context.Context) error {
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		status, err := h.call(wait, "GET", "/api/v1/session", nil, nil)
		if status == http.StatusUnauthorized || status == http.StatusOK {
			return nil
		}
		if status >= 400 && status < 500 {
			return fmt.Errorf("Controller readiness returned HTTP %d", status)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-wait.Done():
			timer.Stop()
			return fmt.Errorf("Controller HTTPS did not become ready: %v: %w", err, wait.Err())
		case <-timer.C:
		}
	}
}

// Guest first boot publishes the marker only after its UI check and bootstrap
// secret cleanup. API login/adoption alone cannot establish appliance readiness.
func (h *handoffHTTP) waitApplianceReady(ctx context.Context) error {
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		var marker struct {
			Version int    `json:"bootstrap_version"`
			Status  string `json:"status"`
		}
		status, err := h.call(wait, "GET", "/appliance-ready.json", nil, &marker)
		detail := fmt.Sprintf("readiness marker HTTP %d (version %d, status %q): %v", status, marker.Version, marker.Status, err)
		if err == nil && status == http.StatusOK && marker.Version == 1 && marker.Status == "ready" {
			status, err = h.call(wait, "GET", "/", nil, nil)
			if err == nil && status == http.StatusOK {
				return nil
			}
			detail = fmt.Sprintf("Controller GUI HTTP %d: %v", status, err)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-wait.Done():
			timer.Stop()
			return fmt.Errorf("appliance first boot is not ready (%s): %w", detail, wait.Err())
		case <-timer.C:
		}
	}
}

type runningAgent struct {
	store                     *agent.Store
	hostID, controller, trust string
	started                   bool
	done                      chan struct{}
}

// ponytail: serialize bootstrap across local state directories; use per-directory locks if hosts ever manage multiple identities.
var handoffAgents = struct {
	sync.Mutex
	entries map[string]*runningAgent
}{entries: map[string]*runningAgent{}}

func getHandoffAgent(dir string) (*runningAgent, error) {
	key, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if a := handoffAgents.entries[key]; a != nil {
		return a, nil
	}
	s, err := agent.OpenStore(key)
	if err != nil {
		return nil, err
	}
	a := &runningAgent{store: s, hostID: s.State.HostID, controller: s.State.Controller, trust: s.State.TrustPEM, done: make(chan struct{})}
	handoffAgents.entries[key] = a
	return a, nil
}
func startHandoffAgent(ctx context.Context, dir string, a *runningAgent) {
	if a.started {
		return
	}
	a.started = true
	key, _ := filepath.Abs(dir)
	go func() {
		if a.hostID != "" {
			err := agent.Run(ctx, a.store, agent.RunConfig{Heartbeat: time.Second, ProbeInterval: 5 * time.Second, ProbeTimeout: 3 * time.Second})
			if err != nil {
				log.Printf("local agent stopped: %v", err)
			}
		} else {
			<-ctx.Done()
		}
		close(a.done)
		handoffAgents.Lock()
		defer handoffAgents.Unlock()
		a.store.Close()
		delete(handoffAgents.entries, key)
	}()
}

// ResumeAgent keeps an enrolled host reporting without waiting for its Controller to be reachable.
// Its context must live as long as the local service. An external agent cannot share this directory.
func ResumeAgent(ctx context.Context, dir string) error {
	handoffAgents.Lock()
	defer handoffAgents.Unlock()
	a, err := getHandoffAgent(dir)
	if err != nil {
		return err
	}
	if a.hostID != "" {
		startHandoffAgent(ctx, dir, a)
	} else {
		a.store.Close()
		key, _ := filepath.Abs(dir)
		delete(handoffAgents.entries, key)
	}
	return nil
}

type handoffState struct {
	RequestID, ControllerURL, TrustPEM, SiteName, PoolName, VMID, Name, Token, PoolID string
	Completed                                                                         bool
	ReadyVersion                                                                      int
}

func saveHandoffState(dir string, s handoffState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".handoff-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "handoff.json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func loadHandoffState(c HandoffConfig) (handoffState, error) {
	want := handoffState{RequestID: c.RequestID, ControllerURL: c.ControllerURL, TrustPEM: c.TrustPEM, SiteName: c.SiteName, PoolName: c.PoolName, VMID: c.VMID, Name: c.Name}
	path := filepath.Join(c.AgentStateDir, "handoff.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		var token [32]byte
		if _, err = rand.Read(token[:]); err != nil {
			return want, err
		}
		want.Token = hex.EncodeToString(token[:])
		return want, saveHandoffState(c.AgentStateDir, want)
	}
	if err != nil {
		return want, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return want, errors.New("handoff state must be a regular private file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return want, err
	}
	var saved handoffState
	if err = json.Unmarshal(data, &saved); err != nil {
		return want, err
	}
	compare := saved
	compare.Token = ""
	compare.PoolID = ""
	compare.Completed = false
	compare.ReadyVersion = 0
	if compare.ControllerURL != want.ControllerURL && c.ServerName != "" {
		// Handoff verifies the newly observed endpoint with the original certificate
		// before loading this state. All other deployment identity must still match.
		compare.ControllerURL = want.ControllerURL
	}
	if compare != want || (!saved.Completed && len(saved.Token) != 64) {
		return want, errors.New("handoff differs from persisted deployment; original identity must be reconciled")
	}
	if saved.ControllerURL != want.ControllerURL {
		saved.ControllerURL = want.ControllerURL
		if err := saveHandoffState(c.AgentStateDir, saved); err != nil {
			return want, err
		}
	}
	return saved, nil
}

// Handoff authenticates once, bootstraps idempotently, enrolls with durable keys,
// starts the embedded agent and waits for Controller-verified libvirt observation before adoption.
// The supplied context is the service lifetime, not an HTTP request context.
func Handoff(ctx context.Context, c HandoffConfig) error {
	if c.RequestID == "" || c.AgentStateDir == "" {
		return errors.New("handoff requires a request ID and agent state directory")
	}
	h, err := newHandoffHTTP(c.ControllerURL, c.TrustPEM, c.ServerName)
	if err != nil {
		return err
	}
	defer h.client.CloseIdleConnections()
	handoffAgents.Lock()
	defer handoffAgents.Unlock()
	a, err := getHandoffAgent(c.AgentStateDir)
	if err != nil {
		return err
	}
	defer func() {
		if !a.started {
			a.store.Close()
			key, _ := filepath.Abs(c.AgentStateDir)
			delete(handoffAgents.entries, key)
		}
	}()
	if a.controller != "" && a.controller != c.ControllerURL && a.trust == c.TrustPEM && c.ServerName != "" && a.store.State.ControllerServerName == c.ServerName {
		if a.started {
			return errors.New("Controller DHCP address changed while the agent is active; reserve the lease and restart the local service before retry")
		}
		if err = agent.UpdateControllerURL(ctx, a.store, c.ControllerURL); err != nil {
			return err
		}
		a.controller = c.ControllerURL
	}
	if a.controller != "" && (a.controller != c.ControllerURL || a.trust != c.TrustPEM) {
		return errors.New("agent belongs to a different Controller or trust")
	}
	if c.ServerName != "" {
		if err = h.waitReady(ctx); err != nil {
			return err
		}
	}
	state, err := loadHandoffState(c)
	if err != nil {
		return err
	}
	if state.Completed {
		if a.hostID == "" {
			return errors.New("completed handoff has no enrolled identity")
		}
		startHandoffAgent(ctx, c.AgentStateDir, a)
		if state.ReadyVersion != 1 {
			if err = h.waitApplianceReady(ctx); err != nil {
				return err
			}
			state.ReadyVersion = 1
			return saveHandoffState(c.AgentStateDir, state)
		}
		return nil
	}
	if err = h.waitReady(ctx); err != nil {
		return err
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if _, err = h.call(ctx, "POST", "/api/v1/session", map[string]string{"username": "admin", "password": c.AdminPassword}, &session); err != nil {
		return err
	}
	h.csrf = session.CSRF
	if h.csrf == "" {
		return errors.New("controller returned no CSRF token")
	}
	defer func() {
		logout, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		h.call(logout, "DELETE", "/api/v1/session", nil, nil)
	}()
	var bootstrap struct {
		PoolID string `json:"pool_id"`
	}
	if _, err = h.call(ctx, "POST", "/api/v1/appliances/bootstrap", map[string]string{"request_id": c.RequestID, "site_name": c.SiteName, "pool_name": c.PoolName, "enrollment_token": state.Token}, &bootstrap); err != nil {
		return err
	}
	if bootstrap.PoolID == "" || (state.PoolID != "" && state.PoolID != bootstrap.PoolID) {
		return errors.New("bootstrap returned inconsistent pool identity")
	}
	state.PoolID = bootstrap.PoolID
	if err = saveHandoffState(c.AgentStateDir, state); err != nil {
		return err
	}
	if a.hostID == "" {
		a.store.State.ControllerServerName = c.ServerName
		if err = agent.Enroll(ctx, a.store, c.ControllerURL, c.TrustPEM, state.Token, ""); err != nil {
			return err
		}
		a.hostID = a.store.State.HostID
		a.controller = a.store.State.Controller
		a.trust = a.store.State.TrustPEM
	}
	startHandoffAgent(ctx, c.AgentStateDir, a)
	wait, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		var adopted struct {
			ID     string `json:"id"`
			PoolID string `json:"pool_id"`
			HostID string `json:"host_id"`
		}
		status, e := h.call(wait, "POST", "/api/v1/appliances/adopt", map[string]string{"request_id": c.RequestID, "pool_id": state.PoolID, "host_id": a.hostID, "vm_id": c.VMID, "name": c.Name}, &adopted)
		if e == nil {
			if adopted.ID != c.VMID || adopted.PoolID != state.PoolID || adopted.HostID != a.hostID {
				return errors.New("Controller adoption returned inconsistent ownership")
			}
			if err = h.waitApplianceReady(ctx); err != nil {
				return err
			}
			state.Token = ""
			state.Completed = true
			state.ReadyVersion = 1
			return saveHandoffState(c.AgentStateDir, state)
		}
		if status != 409 {
			return e
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-a.done:
			timer.Stop()
			return errors.New("embedded agent stopped before appliance adoption")
		case <-wait.Done():
			timer.Stop()
			return fmt.Errorf("Controller has not confirmed appliance observation: %w", wait.Err())
		case <-timer.C:
		}
	}
}

// Caller must rediscover the address from the owned VM, then verify the saved
// Controller trust before changing the persisted agent endpoint.
func refreshDHCPAgent(ctx context.Context, dir, endpoint, trust, serverName string) error {
	handoffAgents.Lock()
	defer handoffAgents.Unlock()
	a, err := getHandoffAgent(dir)
	if err != nil {
		return err
	}
	defer func() {
		if !a.started {
			a.store.Close()
			key, _ := filepath.Abs(dir)
			delete(handoffAgents.entries, key)
		}
	}()
	if a.hostID == "" || a.trust != trust || a.store.State.ControllerServerName != serverName {
		return errors.New("DHCP reconnect does not match enrolled Controller identity")
	}
	if a.started && a.controller != endpoint {
		return errors.New("Controller DHCP address changed while the agent is active; restart the local service to reconnect")
	}
	if !a.started {
		if err = agent.UpdateControllerURL(ctx, a.store, endpoint); err != nil {
			return err
		}
		a.controller = endpoint
	}
	startHandoffAgent(ctx, dir, a)
	return nil
}
