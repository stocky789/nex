// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const (
	poolWitnessCandidatesPath = "/api/v1/agent/witness/candidates"
	poolWitnessRegisterPath   = "/api/v1/agent/witness/register"
)

var (
	poolWitnessCSR              = vmhost.PoolWitnessCSR
	poolWitnessCredentialsReady = vmhost.PoolWitnessCredentialsReady
	poolWitnessBundle           = vmhost.PoolWitnessBundle
	poolWitnessImportMaster     = vmhost.PoolWitnessImportMaster
	poolWitnessImportMember     = vmhost.PoolWitnessImportMember
	readPoolWitnessStatus       = vmhost.ReadPoolWitnessStatus
	pushPoolWitnessAuthority    = PushPoolAuthorityReplicas
)

// ReconcilePoolWitness performs registration on the pool master. Selection and the CA
// pin live in pool-peer.json; the Controller only signs the native qdevice CSR. A failed
// report, registration or import leaves the durable record pending for the next pass.
func ReconcilePoolWitness(ctx context.Context, s *Store) error {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return err
	}
	if readyPoolMemberCount(membership) != 2 || membership.StorageLease != nil || membership.HostFencingDisabled {
		return nil
	}
	c, err := client(s.State, true)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	var candidates protocol.PoolWitnessCandidates
	if err = get(ctx, c, s.State.Controller, poolWitnessCandidatesPath, &candidates); err != nil {
		return err
	}
	var selected *protocol.PoolWitnessCandidate
	for i := range candidates.Candidates {
		candidate := &candidates.Candidates[i]
		if candidate.Valid() != nil || candidate.Kind != "controller" || candidate.Address != "" {
			continue
		}
		selected = candidate
		break
	}
	if selected == nil {
		return errors.New("Controller did not provide a usable qdevice witness candidate")
	}
	address, err := controllerWitnessAddress(s.State.Controller)
	if err != nil {
		return err
	}
	current := membership.Witness
	if current != nil && current.FencingGeneration != membership.Generation {
		poolPeerMu.Lock()
		state, loadErr := loadPoolPeer(s)
		if loadErr == nil && state.Membership.MasterHostID == s.State.HostID && state.Membership.Generation == membership.Generation &&
			state.Membership.Witness != nil && state.Membership.Witness.FencingGeneration == current.FencingGeneration {
			state.Membership.Witness.FencingGeneration = state.Membership.Generation
			state.Membership.Witness.CredentialsReady = false
			loadErr = savePoolPeer(s, state)
		}
		poolPeerMu.Unlock()
		if loadErr != nil {
			return loadErr
		}
		membership, err = Membership(s)
		if err != nil {
			return err
		}
		current = membership.Witness
	}
	changedCA := current != nil && (current.CASHA256 != selected.CASHA256 || current.Address != address)
	if current != nil && !changedCA && current.CredentialsReady && poolWitnessCredentialsReady(ctx, s.dir, membership.PoolID, *current) {
		if _, err = poolWitnessBundle(s.dir, current.FencingGeneration); err == nil {
			return pushPoolWitnessAuthority(ctx, s, time.Minute)
		}
		if err = rotatePoolWitnessGeneration(s, membership, *current); err != nil {
			return err
		}
		membership, err = Membership(s)
		if err != nil {
			return err
		}
		current = membership.Witness
	}
	if current != nil && current.CredentialsReady && !poolWitnessCredentialsReady(ctx, s.dir, membership.PoolID, *current) {
		if err = rotatePoolWitnessGeneration(s, membership, *current); err != nil {
			return err
		}
		return nil
	}
	if current == nil || changedCA {
		poolPeerMu.Lock()
		state, loadErr := loadPoolPeer(s)
		if loadErr == nil && state.Membership.MasterHostID == s.State.HostID {
			next := state.Membership
			generation := next.Generation
			if changedCA {
				next = nextPoolMembership(next, s.State.HostID, "")
				generation = next.Generation
			}
			next.Witness = &protocol.PoolWitnessRecord{Kind: selected.Kind, Address: address, CAPEM: selected.CAPEM,
				CASHA256: selected.CASHA256, FencingGeneration: generation,
				ClusterName: protocol.PoolWitnessClusterName(next.PoolID)}
			state.Membership = next
			loadErr = savePoolPeer(s, state)
		}
		poolPeerMu.Unlock()
		if loadErr != nil {
			return loadErr
		}
		membership, err = Membership(s)
		if err != nil {
			return err
		}
		current = membership.Witness
	}
	if current == nil || current.Valid(membership.PoolID) != nil || current.FencingGeneration != membership.Generation {
		return errors.New("pool witness selection does not match the current fencing generation")
	}
	if poolWitnessCredentialsReady(ctx, s.dir, membership.PoolID, *current) {
		if _, err = poolWitnessBundle(s.dir, current.FencingGeneration); err == nil {
			if err = markPoolWitnessReady(s, *current); err != nil {
				return err
			}
			return pushPoolWitnessAuthority(ctx, s, time.Minute)
		}
		if err = rotatePoolWitnessGeneration(s, membership, *current); err != nil {
			return err
		}
		return nil
	}
	csr, err := poolWitnessCSR(ctx, s.dir, membership.PoolID, *current)
	if err != nil {
		if strings.Contains(err.Error(), "staged qdevice NSS database exists without its CSR") {
			return rotatePoolWitnessGeneration(s, membership, *current)
		}
		return err
	}
	request := protocol.PoolWitnessRegistrationRequest{PoolID: membership.PoolID, ClusterName: current.ClusterName,
		FencingGeneration: current.FencingGeneration, CSRPEM: string(csr)}
	var result protocol.PoolWitnessRegistration
	if err = post(ctx, c, s.State.Controller, poolWitnessRegisterPath, request, &result); err != nil {
		return err
	}
	if result.Address != "" || result.CASHA256 != current.CASHA256 || result.CAPEM != current.CAPEM {
		return errors.New("Controller witness registration does not match the selected CA pin")
	}
	bundle, err := poolWitnessImportMaster(ctx, s.dir, membership.PoolID, *current, string(csr), result.CertificatePEM)
	if err != nil {
		if strings.Contains(err.Error(), "staged qdevice database is incomplete") {
			return rotatePoolWitnessGeneration(s, membership, *current)
		}
		return err
	}
	if !poolWitnessCredentialsReady(ctx, s.dir, membership.PoolID, *current) {
		return errors.New("native qdevice credentials failed validation after import")
	}
	if len(bundle) == 0 {
		return errors.New("native qdevice import produced an empty member credential")
	}
	if err = markPoolWitnessReady(s, *current); err != nil {
		return err
	}
	return pushPoolWitnessAuthority(ctx, s, time.Minute)
}

func readyPoolMemberCount(membership protocol.PoolMembership) int {
	count := 0
	for _, member := range membership.Members {
		if member.Status == "ready" {
			count++
		}
	}
	return count
}

func rotatePoolWitnessGeneration(s *Store, membership protocol.PoolMembership, witness protocol.PoolWitnessRecord) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	if state.Membership.MasterHostID != s.State.HostID || state.Membership.Generation != membership.Generation ||
		state.Membership.Witness == nil || state.Membership.Witness.FencingGeneration != witness.FencingGeneration {
		return errors.New("pool witness changed while credentials were being repaired")
	}
	next := nextPoolMembership(state.Membership, s.State.HostID, "")
	next.Witness.FencingGeneration = next.Generation
	next.Witness.CredentialsReady = false
	state.Membership = next
	return savePoolPeer(s, state)
}

func markPoolWitnessReady(s *Store, witness protocol.PoolWitnessRecord) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	if state.Membership.MasterHostID != s.State.HostID || state.Membership.Generation != witness.FencingGeneration || state.Membership.Witness == nil ||
		state.Membership.Witness.CASHA256 != witness.CASHA256 {
		return errors.New("pool witness changed while registration was in progress")
	}
	if state.Membership.Witness.CredentialsReady {
		return nil
	}
	state.Membership.Witness.CredentialsReady = true
	return savePoolPeer(s, state)
}

func controllerWitnessAddress(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("qdevice witness requires the pinned Controller HTTPS origin")
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "\r\n") {
		return "", errors.New("Controller witness host is invalid")
	}
	return net.JoinHostPort(host, "5403"), nil
}

func get(ctx context.Context, c *http.Client, origin, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(origin, "/")+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("Controller witness query failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&failure)
		return httpStatusError{Status: resp.StatusCode, Detail: strings.ToValidUTF8(failure.Error, "�")}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(out)
}
