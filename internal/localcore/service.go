// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/vmhost"
	"nex.local/nex/internal/websecurity"
)

type Config struct {
	DataDir, AgentStateDir, UIPath, DocsPath, SourcePath, PoolPeerListen string
	Authenticate                                                         func(context.Context, string) error
	Engine                                                               vmhost.Config
}

// PoolPeerHandler serves the isolated Core-to-Core join endpoint; it never accepts an
// operator session or Controller command as membership authority.
func (s *Service) PoolPeerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handoffAgents.Lock()
		a, err := getHandoffAgent(s.cfg.AgentStateDir)
		handoffAgents.Unlock()
		if err != nil {
			http.Error(w, "pool peer unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/api/v1/pool/storage" || strings.HasPrefix(r.URL.Path, "/api/v1/pool/storage/") {
			agent.PoolStorageHandler(a.store).ServeHTTP(w, r)
			return
		}
		agent.PoolPeerHandler(a.store).ServeHTTP(w, r)
	})
}

func (s *Service) PoolPeerTLSConfig() (*tls.Config, error) {
	handoffAgents.Lock()
	a, err := getHandoffAgent(s.cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil {
		return nil, err
	}
	return agent.PoolPeerTLSConfig(a.store)
}

type Service struct {
	cfg        Config
	engine     *vmhost.Engine
	mu         sync.Mutex
	sessions   map[string]session
	loginGate  websecurity.LoginGate
	operations map[string]deployment
	active     map[string]bool
	ctx        context.Context
	cancel     context.CancelFunc
	lock       *os.File
	wg         sync.WaitGroup
	enrollMu   sync.Mutex
}
type DeployRequest struct {
	RequestID     string   `json:"request_id"`
	Name          string   `json:"name"`
	Hostname      string   `json:"hostname"`
	CPUs          int      `json:"cpus"`
	MemoryMiB     int      `json:"memory_mib"`
	DiskGiB       int      `json:"disk_gib"`
	NetworkMode   string   `json:"network_mode,omitempty"`
	AddressCIDR   string   `json:"address_cidr"`
	Gateway       string   `json:"gateway"`
	DNS           []string `json:"dns"`
	AdminPassword string   `json:"admin_password"`
	SiteName      string   `json:"site_name"`
	Bridge        string   `json:"bridge"`
	StorageDir    string   `json:"storage_dir"`
}
type deployment struct {
	Operation   vmhost.Operation `json:"operation"`
	Request     DeployRequest    `json:"request"`
	Digest      string           `json:"digest"`
	Certificate string           `json:"certificate"`
	Key         string           `json:"key,omitempty"`
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{7,63}$`)

func Open(cfg Config) (*Service, error) {
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("local state directory must be private 0700")
	}
	fd, err := syscall.Open(filepath.Join(cfg.DataDir, "lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("local service already running")
	}
	s := &Service{cfg: cfg, lock: lock, sessions: map[string]session{}, operations: map[string]deployment{}, active: map[string]bool{}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	ok := false
	defer func() {
		if !ok {
			s.cancel()
			lock.Close()
		}
	}()
	if s.cfg.Authenticate == nil {
		s.cfg.Authenticate = authenticateRoot
	}
	err = readPrivate(filepath.Join(cfg.DataDir, "deployments.json"), &s.operations)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if s.operations == nil {
		s.operations = map[string]deployment{}
	}
	s.engine, err = vmhost.New(cfg.Engine)
	if err != nil {
		return nil, err
	}
	ok = true
	return s, nil
}
func (s *Service) Close() error {
	s.cancel()
	s.wg.Wait()
	// The embedded enrollment agent is not on s.wg; wait for it so callers (and
	// testing.T.TempDir cleanup) can remove AgentStateDir without racing Save().
	releaseHandoffAgent(s.cfg.AgentStateDir)
	// A background update apply is not on s.wg either, and it keeps writing
	// progress into DataDir; abandoning it would leave dpkg running and write
	// into a directory the caller is about to remove.
	nexupdate.WaitApply()
	return s.lock.Close()
}

// HTTP/1.1 is preferred so the console WebSocket can upgrade; HTTP/2 cannot be hijacked.
func (s *Service) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
}
func (s *Service) persist() error {
	return savePrivate(filepath.Join(s.cfg.DataDir, "deployments.json"), s.operations)
}
func (s *Service) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/local/v1/session", s.login)
	m.HandleFunc("GET /api/local/v1/session", s.auth(s.getSession))
	m.HandleFunc("DELETE /api/local/v1/session", s.auth(s.logout))
	m.HandleFunc("GET /api/local/v1/host", s.auth(s.host))
	m.HandleFunc("POST /api/local/v1/enrollment", s.auth(s.enrollment))
	m.HandleFunc("GET /api/local/v1/pool", s.auth(s.pool))
	m.HandleFunc("POST /api/local/v1/pool/join", s.auth(s.joinPool))
	m.HandleFunc("POST /api/local/v1/pool/join-csr", s.auth(s.poolJoinCSR))
	m.HandleFunc("POST /api/local/v1/pool/join-offer", s.auth(s.poolJoinOffer))
	m.HandleFunc("POST /api/local/v1/pool/storage-cluster/apply", s.auth(s.storageClusterApply))
	m.HandleFunc("GET /api/local/v1/pool/storage-cluster", s.auth(s.storageClusterStatus))
	m.HandleFunc("GET /api/local/v1/vms", s.auth(s.vms))
	m.HandleFunc("POST /api/local/v1/vms/{id}/actions", s.auth(s.power))
	m.HandleFunc("GET /api/local/v1/vms/{id}/console", s.auth(s.console))
	m.HandleFunc("POST /api/local/v1/deployments", s.auth(s.deploy))
	m.HandleFunc("GET /api/local/v1/deployments", s.auth(s.deployments))
	m.HandleFunc("GET /api/local/v1/deployments/{id}", s.auth(s.operation))
	m.HandleFunc("POST /api/local/v1/deployments/{id}/retry", s.auth(s.retry))
	m.HandleFunc("GET /api/local/v1/updates", s.auth(s.updates))
	m.HandleFunc("POST /api/local/v1/updates/apply", s.auth(s.applyUpdate))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"status": "ok"}) })
	m.Handle("GET /api/docs/", http.StripPrefix("/api/docs/", http.FileServer(http.Dir(s.cfg.DocsPath))))
	m.HandleFunc("GET /source", func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, s.cfg.SourcePath) })
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, filepath.Join(s.cfg.UIPath, "local.html"))
			return
		}
		http.FileServer(http.Dir(s.cfg.UIPath)).ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		websecurity.Headers(w)
		if r.TLS == nil {
			fail(w, 400, "HTTPS required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if r.Header.Get("Origin") != "https://"+r.Host {
				fail(w, 403, "same-origin request required")
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
func (s *Service) host(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	pre, err := s.engine.Preflight(r.Context(), vmhost.DeploySpec{CPUs: 2, MemoryMiB: 4096, DiskGiB: 32})
	if err != nil {
		fail(w, 503, "host preflight unavailable")
		return
	}
	hostID, controllerURL := s.enrollmentState()
	reply(w, 200, map[string]any{"hostname": hostname, "bridge": s.cfg.Engine.Bridge, "storage_dir": s.cfg.Engine.StorageDir, "defaults": map[string]int{"cpus": 2, "memory_mib": 4096, "disk_gib": 32}, "preflight": pre, "host_id": hostID, "controller": controllerURL})
}
func (s *Service) vms(w http.ResponseWriter, r *http.Request) {
	v, err := s.engine.List(r.Context())
	if err != nil {
		fail(w, 503, "VM inventory unavailable")
		return
	}
	reply(w, 200, v)
}
func (s *Service) power(w http.ResponseWriter, r *http.Request) {
	var v struct {
		RequestID    string `json:"request_id"`
		Action       string `json:"action"`
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &v) {
		return
	}
	if !validID.MatchString(v.RequestID) || (v.Action != "start" && v.Action != "shutdown" && v.Action != "force-stop") {
		fail(w, 400, "invalid request_id or action")
		return
	}
	op, err := s.engine.Power(r.Context(), v.RequestID, r.PathValue("id"), v.Action)
	if err != nil {
		fail(w, 409, "VM action refused: "+err.Error())
		return
	}
	reply(w, 202, op)
}
func (s *Service) deployments(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []vmhost.Operation{}
	for _, v := range s.operations {
		out = append(out, v.Operation)
	}
	reply(w, 200, out)
}
func (s *Service) operation(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	v, ok := s.operations[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		fail(w, 404, "deployment not found")
		return
	}
	reply(w, 200, v.Operation)
}
func (s *Service) deploy(w http.ResponseWriter, r *http.Request) {
	var v DeployRequest
	if !decode(w, r, &v) {
		return
	}
	if v.CPUs == 0 {
		v.CPUs = 2
	}
	if v.MemoryMiB == 0 {
		v.MemoryMiB = 4096
	}
	if v.DiskGiB == 0 {
		v.DiskGiB = 32
	}
	if err := s.validate(v); err != nil {
		fail(w, 400, err.Error())
		return
	}
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.operations[v.RequestID]; ok {
		if old.Digest != digest {
			fail(w, 409, "request_id already used with different deployment settings")
			return
		}
		s.start(v.RequestID)
		reply(w, 202, old.Operation)
		return
	}
	if _, err := os.Lstat(filepath.Join(s.cfg.DataDir, "recovery-preparation.json")); !os.IsNotExist(err) {
		fail(w, 409, "this host has a manual recovery preparation; complete recovery of that appliance")
		return
	}
	if hasAgentState(s.cfg.AgentStateDir) {
		fail(w, 409, "this host already has agent identity or pending enrollment; retain its existing Controller ownership")
		return
	}
	if len(s.operations) > 0 {
		fail(w, 409, "this host already has a Controller deployment; recover its existing operation")
		return
	}
	cert, key, err := deploymentTLS(v)
	if err != nil {
		fail(w, 400, "invalid network settings or TLS generation failure")
		return
	}
	if err = vmhost.Validate(specFor(v, cert, key)); err != nil {
		fail(w, 400, err.Error())
		return
	}
	op := vmhost.Operation{ID: v.RequestID, Kind: "deploy", Status: "running", Stage: "queued", Progress: 0}
	s.operations[v.RequestID] = deployment{Operation: op, Request: v, Digest: digest, Certificate: cert, Key: key}
	if err = s.persist(); err != nil {
		delete(s.operations, v.RequestID)
		fail(w, 500, "cannot persist deployment intent")
		return
	}
	s.start(v.RequestID)
	reply(w, 202, op)
}
func (s *Service) validate(v DeployRequest) error {
	if !validID.MatchString(v.RequestID) {
		return errors.New("request_id must contain 8–64 letters, digits, underscores or hyphens")
	}
	if v.Bridge != s.cfg.Engine.Bridge || v.StorageDir != s.cfg.Engine.StorageDir || v.Bridge == "" || v.StorageDir == "" {
		return errors.New("explicitly select configured bridge and storage")
	}
	if strings.TrimSpace(v.SiteName) == "" || len(v.SiteName) > 200 {
		return errors.New("Site name is required (maximum 200 bytes)")
	}
	return nil
}

func (s *Service) retry(w http.ResponseWriter, r *http.Request) {
	var v struct{}
	if !decode(w, r, &v) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	d, ok := s.operations[id]
	if !ok {
		fail(w, 404, "deployment not found")
		return
	}
	s.start(id)
	reply(w, 202, d.Operation)
}
