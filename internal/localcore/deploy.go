// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"time"

	"nex.local/nex/internal/vmhost"
)

// dhcpControllerReadyWait bounds how long DHCP resume keeps verifying the
// rediscovered Controller address. Guest-agent DHCP often succeeds before
// nex-controller is listening on :8443.
var dhcpControllerReadyWait = 5 * time.Minute

func deploymentTLS(v DeployRequest) (string, string, error) {
	if v.NetworkMode == "dhcp" {
		name := v.Hostname
		if name == "" {
			name = v.Name
		}
		if name == "" {
			name = "nex-controller"
		}
		return instanceTLS(name)
	}
	if _, err := netip.ParsePrefix(v.AddressCIDR); err != nil {
		return "", "", err
	}
	return instanceTLS(v.AddressCIDR)
}

func instanceTLS(cidr string) (string, string, error) {
	p, _ := netip.ParsePrefix(cidr)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Nex Controller"}, NotBefore: time.Now().Add(-5 * time.Minute), NotAfter: time.Now().AddDate(2, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	if p.IsValid() {
		template.IPAddresses = []net.IP{net.ParseIP(p.Addr().String())}
	} else {
		template.DNSNames = []string{cidr}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	pk, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})), nil
}

// Caller holds s.mu. Persisted request identity is retained across failures and restarts.
func (s *Service) start(id string) {
	if s.active[id] || (s.operations[id].Operation.Status == "ready" && s.operations[id].Request.NetworkMode != "dhcp") {
		return
	}
	s.active[id] = true
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { s.mu.Lock(); delete(s.active, id); s.mu.Unlock() }()
		s.runDeployment(id)
	}()
}
func (s *Service) update(id string, op vmhost.Operation) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.operations[id]
	d.Operation = op
	s.operations[id] = d
	return s.persist() == nil
}
func (s *Service) runDeployment(id string) {
	s.mu.Lock()
	d := s.operations[id]
	s.mu.Unlock()
	v := d.Request
	if v.NetworkMode == "dhcp" && d.Operation.ControllerCertSHA256 != "" {
		s.refreshDHCPController(id, d)
		return
	}
	if err := s.validate(v); err != nil {
		op := d.Operation
		op.Status = "needs_attention"
		op.Error = "Host bridge or storage configuration changed; restore the selected deployment configuration before retry"
		s.update(id, op)
		return
	}
	spec := specFor(v, d.Certificate, d.Key)
	// Poll the engine's durable journal while its synchronous import/boot runs.
	done := make(chan struct{})
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if op, err := s.engine.Operation(s.ctx, id); err == nil {
					s.update(id, op)
				}
			}
		}
	}()
	op, err := s.engine.Deploy(s.ctx, spec)
	close(done)
	<-pollDone
	if err != nil {
		op.ID = id
		op.Kind = "deploy"
		op.Status = "needs_attention"
		op.Error = "VM deployment needs recovery: " + err.Error()
		s.update(id, op)
		return
	}
	if op.Status != "booted" {
		s.update(id, op)
		return
	}
	if v.NetworkMode == "dhcp" {
		op.Stage = "waiting_for_network"
		op.Status = "running"
		op.ControllerURL = ""
		if !s.update(id, op) {
			return
		}
		op.ControllerURL, err = s.engine.WaitControllerURL(s.ctx, id)
		if err != nil {
			op.Status = "needs_attention"
			op.Error = err.Error()
			s.update(id, op)
			return
		}
	}
	op.Stage = "initializing"
	op.Status = "running"
	op.Progress = 92
	if !s.update(id, op) {
		return
	}
	err = Handoff(s.ctx, HandoffConfig{ControllerURL: op.ControllerURL, ServerName: controllerServerName(v), TrustPEM: d.Certificate, AdminPassword: v.AdminPassword, SiteName: v.SiteName, VMID: op.VMID, Name: v.Name, RequestID: id, AgentStateDir: s.cfg.AgentStateDir})
	if err != nil {
		op.Status = "needs_attention"
		op.Error = "Controller setup needs recovery: " + err.Error()
		s.update(id, op)
		return
	}
	op.Stage = "cleanup"
	op.Progress = 98
	if !s.update(id, op) {
		return
	}
	if err = s.engine.RemoveSeed(s.ctx, op.VMID); err != nil {
		op.Status = "needs_attention"
		op.Stage = "cleanup"
		op.Error = "Controller is running; bootstrap seed cleanup requires recovery"
		s.update(id, op)
		return
	}
	s.mu.Lock()
	d = s.operations[id]
	previous := d
	if block, _ := pem.Decode([]byte(d.Certificate)); block != nil {
		sum := sha256.Sum256(block.Bytes)
		op.ControllerCertSHA256 = hex.EncodeToString(sum[:])
	}
	op.Status = "ready"
	op.Stage = "ready"
	op.Progress = 100
	op.Error = ""
	d.Operation = op
	d.Key = ""
	d.Request.AdminPassword = ""
	s.operations[id] = d
	if err := s.persist(); err != nil {
		previous.Operation.Status = "needs_attention"
		previous.Operation.Stage = "cleanup"
		previous.Operation.Error = "Controller is running but readiness could not be persisted; recover this operation"
		s.operations[id] = previous
	}
	s.mu.Unlock()
}
func (s *Service) Resume() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	hasDHCP := false
	for _, d := range s.operations {
		if d.Request.NetworkMode == "dhcp" {
			hasDHCP = true
		}
	}
	// Rediscover the DHCP deployment before resuming its agent, otherwise
	// the old lease address would keep the store busy and prevent verified recovery.
	if !hasDHCP {
		if err := ResumeAgent(s.ctx, s.cfg.AgentStateDir); err != nil {
			return err
		}
	}
	for id := range s.operations {
		s.start(id)
	}
	return nil
}

func specFor(v DeployRequest, cert, key string) vmhost.DeploySpec {
	return vmhost.DeploySpec{RequestID: v.RequestID, Name: v.Name, Hostname: v.Hostname, CPUs: v.CPUs, MemoryMiB: v.MemoryMiB, DiskGiB: v.DiskGiB, NetworkMode: v.NetworkMode, AddressCIDR: v.AddressCIDR, Gateway: v.Gateway, DNS: v.DNS, AdminPassword: v.AdminPassword, TLSCertificate: cert, TLSPrivateKey: key}
}

func controllerServerName(v DeployRequest) string {
	if v.NetworkMode != "dhcp" {
		return ""
	}
	if v.Hostname != "" {
		return v.Hostname
	}
	if v.Name != "" {
		return v.Name
	}
	return "nex-controller"
}

// Completed DHCP deployments retain their certificate and VM identity, not their
// bootstrap password. Refresh reconnects that identity without redeploying.
func (s *Service) refreshDHCPController(id string, d deployment) {
	op := d.Operation
	op.Status, op.Stage, op.Error = "running", "waiting_for_network", ""
	op.ControllerURL = ""
	if !s.update(id, op) {
		return
	}
	endpoint, err := s.engine.WaitControllerURL(s.ctx, id)
	if err == nil {
		err = refreshDHCPAgentReady(s.ctx, s.cfg.AgentStateDir, endpoint, d.Certificate, controllerServerName(d.Request))
	}
	if err != nil {
		// Keep the enrolled agent reporting on its last known endpoint so a Controller
		// that is still booting does not leave the host permanently stale.
		if resumeErr := ResumeAgent(s.ctx, s.cfg.AgentStateDir); resumeErr != nil {
			err = fmt.Errorf("%w; also could not resume agent: %v", err, resumeErr)
		}
		op.Status = "needs_attention"
		op.Error = err.Error()
	} else {
		op.Status, op.Stage, op.ControllerURL = "ready", "ready", endpoint
	}
	s.update(id, op)
}

// refreshDHCPAgentReady retries TLS verification of a rediscovered DHCP Controller
// address until the appliance accepts connections or the deadline expires.
func refreshDHCPAgentReady(ctx context.Context, dir, endpoint, trust, serverName string) error {
	wait, cancel := context.WithTimeout(ctx, dhcpControllerReadyWait)
	defer cancel()
	var err error
	for {
		err = refreshDHCPAgent(wait, dir, endpoint, trust, serverName)
		if err == nil {
			return nil
		}
		select {
		case <-wait.Done():
			if wait.Err() == context.DeadlineExceeded {
				return fmt.Errorf("Controller is not ready: %v; check the appliance and retry", err)
			}
			return err
		case <-time.After(time.Second):
		}
	}
}
