// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// A Controller-driven join hands the joining Core one credential and nothing else: the
// Controller asks the master for an invitation and hands it over as a durable command, and
// the joining Core performs the whole CSR/join/rehome exchange with the master itself. The
// invitation is bound to the pool, the master and the member host ID; its CSR is bound by
// the master at first redemption, so no Controller-orchestrated ordering is required.
const poolJoinInvitationTTL = 15 * time.Minute

// poolJoinMaxAttempts bounds retries of a stage that has not yet changed membership, so an
// expired or unusable invitation ends as an honest failure instead of a permanent pending.
// ponytail: fixed attempt bound; carry the invitation expiry if operators need it sooner.
const poolJoinMaxAttempts = 10

// poolJoinStages is the durable stage order. A stage names the step still to perform, so a
// host that joined but has not rehomed reports "rehomed", which is the truth a caller needs.
var poolJoinStages = []string{"precheck", "csr", "joined", "rehomed", "advertised", "ready"}

// poolJoinMu single-flights the staged machine: a command and a tick resume must not run it
// at once. It is never held while poolPeerMu is taken by the stages themselves.
var poolJoinMu sync.Mutex

// poolPendingJoin is the joining host's durable record in pool-peer.json. Invitation is a
// secret: it is never logged, reported or returned by PoolJoinReport.
type poolPendingJoin struct {
	OperationID, MasterURL, CAPEM, Invitation, PeerOrigin string
	Stage, Status, LastError                              string
	Attempts                                              int
	UpdatedAt                                             time.Time
}

// CreatePoolJoinInvitation mints a single-use, short-lived invitation on the master. It
// reuses the existing offer record with no CSR hash, which means "bind the CSR at first
// redemption"; the CSR-first host-local offer path is unchanged and still supported.
func CreatePoolJoinInvitation(s *Store, masterURL, memberHostID string) (protocol.PoolJoinInvitation, error) {
	var out protocol.PoolJoinInvitation
	var u *url.URL
	var err error
	if masterURL != "" {
		if u, err = peerOrigin(masterURL); err != nil {
			return out, err
		}
	}
	if s.State.HostID == "" || s.State.PoolID == "" {
		return out, errors.New("pool master identity is unavailable")
	}
	if memberHostID == "" || len(memberHostID) > 128 || memberHostID == s.State.HostID {
		return out, errors.New("member host identity is required")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return out, err
	}
	if state.Membership.PoolID == "" {
		if err = initPoolMaster(s, &state); err != nil {
			return out, err
		}
	} else if state.Membership.PoolID != s.State.PoolID || state.Membership.MasterHostID != s.State.HostID || state.CAKeyPEM == "" {
		return out, errors.New("pool peer state does not match this master")
	}
	for _, member := range state.Membership.Members {
		if member.HostID == memberHostID && member.Status != "ejected" {
			return out, errors.New("host is already a pool member")
		}
	}
	// Admitting a member is a membership write: a pool whose authority is being handed over
	// must not gain one between the replica and the activation.
	if err = poolAuthorityWritable(state); err != nil {
		return out, err
	}
	// Nothing advertises the master's own peer origin: AdvertisePoolPeerOrigin is member to
	// master only, so the first brokered invitation records the master's origin as its own
	// member peer_origin: the supplied master_url, or else the one the master works out for
	// itself. A usable recorded origin wins and the supplied value is ignored, so a later
	// caller cannot move the master. Like the member advertise handler (peer.go:184) this is a
	// recorded observation, not a membership change: no generation bump.
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID != state.Membership.MasterHostID {
			continue
		}
		if recorded, recErr := peerOrigin(state.Membership.Members[i].PeerOrigin); recErr == nil {
			u = recorded
		} else {
			if u == nil {
				if u, err = ownPeerOrigin(); err != nil {
					return out, err
				}
			}
			state.Membership.Members[i].PeerOrigin = u.String()
		}
		break
	}
	if u == nil {
		return out, errors.New("pool master must be an HTTPS origin")
	}
	state.MasterURL = u.String()
	tokenBytes := make([]byte, 32)
	if _, err = rand.Read(tokenBytes); err != nil {
		return out, err
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	digest := offerDigest(token, state.Membership.PoolID, state.Membership.MasterHostID, memberHostID, "")
	for id, old := range state.Offers {
		if time.Now().After(old.Expires) {
			delete(state.Offers, id)
		}
	}
	expires := time.Now().Add(poolJoinInvitationTTL).UTC()
	state.Offers[digest] = poolJoinOffer{Digest: digest, PoolID: state.Membership.PoolID, Master: state.Membership.MasterHostID, HostID: memberHostID, Expires: expires}
	if err = savePoolPeer(s, state); err != nil {
		return out, err
	}
	b, err := json.Marshal(peerOffer{URL: u.String(), CAPEM: state.CAPEM, PoolID: state.Membership.PoolID, PoolName: s.State.PoolName, MasterHostID: state.Membership.MasterHostID, MemberHostID: memberHostID, Token: token})
	if err != nil {
		return out, err
	}
	return protocol.PoolJoinInvitation{MasterURL: u.String(), PoolCAPEM: state.CAPEM, Invitation: base64.RawURLEncoding.EncodeToString(b), ExpiresAt: expires}, nil
}

// ownPeerOrigin is this host's configured peer listener. A wildcard bind names no address,
// so it falls back to the address this host uses for its default route, which on Nex Core is
// the management interface. Connecting a UDP socket sends nothing.
// ponytail: a derived origin is recorded for good; pick by the joining host's route if a
// master ever needs a different address per member.
func ownPeerOrigin() (*url.URL, error) {
	host, port, err := net.SplitHostPort(PoolPeerListen)
	if err != nil {
		return nil, errors.New("the pool master's peer listen address is not an address and port")
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		conn, dialErr := net.Dial("udp", "192.0.2.1:9")
		if dialErr != nil {
			return nil, errors.New("the pool master cannot work out its own peer address because it has no default route")
		}
		defer conn.Close()
		host = conn.LocalAddr().(*net.UDPAddr).IP.String()
	}
	return &url.URL{Scheme: "https", Host: net.JoinHostPort(host, port)}, nil
}

// ExecutePoolRequest answers one Controller-brokered pool request on the master. The
// envelope is transient: the Controller holds the invitation only for the remainder of the
// HTTP request it answers, and Core keeps the authoritative offer record.
func ExecutePoolRequest(s *Store, request protocol.PoolRequest) protocol.PoolResult {
	result := protocol.PoolResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "failed", ObservedAt: time.Now().UTC()}
	if request.RequestID == "" || len(request.RequestID) > 128 || request.Action != "invite" || request.HostID != s.State.HostID || request.PoolID != s.State.PoolID {
		result.Error = "pool request does not address this host"
		return result
	}
	invitation, err := CreatePoolJoinInvitation(s, request.MasterURL, request.MemberHostID)
	if err != nil {
		result.Error = resultError(err.Error())
		return result
	}
	result.Status, result.Invitation = "succeeded", &invitation
	return result
}

// PoolJoinPrecheck holds every refusal a joining host owes its operator, for both callers:
// the host-local endpoint and the Controller command path. One guard, all callers.
func PoolJoinPrecheck(ctx context.Context, s *Store, engine *vmhost.ManagedEngine) error {
	if s.State.HostID == "" {
		return errors.New("enroll this Core before joining a pool")
	}
	if engine == nil {
		engine = vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	}
	managed, _ := engine.Applied(ctx)
	if len(managed.VMs) != 0 {
		return errors.New("joining a pool with managed VMs is not supported; migrate or remove workloads first")
	}
	for _, storage := range managed.Storage {
		if protocol.StorageBlocksPoolJoin(storage) {
			return errors.New("joining a pool with existing remote storage is not supported; detach it first")
		}
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return err
	}
	// A Core that is still the only host of its own pool may join another; a master with
	// members, or a host that already belongs to another pool, may not.
	if state.Membership.PoolID != "" && (state.Membership.MasterHostID != s.State.HostID || len(state.Membership.Members) != 1) {
		return errors.New("only the master of its own single-host pool can join another pool")
	}
	return nil
}

// ExecutePoolJoin records the Controller's durable join command and drives it as far as it
// gets in this pass. It must run on the goroutine that owns the Store: a completed join
// rewrites this Core's pool identity. The second return reports whether it did.
func ExecutePoolJoin(ctx context.Context, s *Store, cmd protocol.Command) (protocol.Result, bool) {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	v := cmd.Pool
	if v == nil || v.Action != "join" || v.Valid() != nil || v.MemberHostID != s.State.HostID {
		result.Error = "pool join command does not address this host"
		return result, false
	}
	master, err := peerOrigin(v.MasterURL)
	if err != nil || !x509.NewCertPool().AppendCertsFromPEM([]byte(v.PoolCAPEM)) || ValidatePoolJoinOffer(v.Invitation) != nil {
		result.Error = "master_url, pool_ca_pem and a one-use invitation are required"
		return result, false
	}
	origin := ""
	if v.PeerOrigin != "" {
		u, originErr := peerOrigin(v.PeerOrigin)
		if originErr != nil {
			result.Error = "peer origin must be a bare HTTPS origin"
			return result, false
		}
		origin = u.String()
	}
	poolJoinMu.Lock()
	defer poolJoinMu.Unlock()
	pending, err := loadPendingJoin(s)
	if err != nil {
		result.Error = "pool join state is unavailable"
		return result, false
	}
	if pending != nil && pending.OperationID != cmd.OperationID && pending.Status != "succeeded" && pending.Status != "failed" {
		result.Error = "another pool join is already in progress on this host"
		return result, false
	}
	if pending == nil || pending.OperationID != cmd.OperationID {
		pending = &poolPendingJoin{OperationID: cmd.OperationID, MasterURL: master.String(), CAPEM: v.PoolCAPEM, Invitation: v.Invitation, PeerOrigin: origin, Stage: "precheck", Status: "pending"}
		if err = savePendingJoin(s, pending); err != nil {
			result.Error = "pool join state could not be recorded"
			return result, false
		}
	}
	before := s.State.PoolID
	status := advancePoolJoin(ctx, s, pending)
	result.Status, result.Error, result.ObservedAt = status.Status, status.Error, status.ObservedAt
	if status.Status == "pending" {
		// The command is terminal for the Controller; the host keeps retrying and reports
		// its true stage in inventory rather than holding the operation open forever.
		result.Status = "needs_attention"
	}
	return result, s.State.PoolID != before
}

// ResumePoolJoin re-attempts a persisted staged join after a restart, a reboot or a
// Controller outage. Like ExecutePoolJoin it must run on the Store's owning goroutine and
// reports whether this Core's pool identity changed.
func ResumePoolJoin(ctx context.Context, s *Store) bool {
	poolJoinMu.Lock()
	defer poolJoinMu.Unlock()
	pending, err := loadPendingJoin(s)
	if err != nil || pending == nil || (pending.Status != "pending" && pending.Status != "needs_attention") {
		return false
	}
	before := s.State.PoolID
	advancePoolJoin(ctx, s, pending)
	return s.State.PoolID != before
}

// CompletePoolJoinStages runs the stages that follow a redeemed offer on the host-local
// path. The host is already a member there, so an interrupted rehome is reported as a stage
// still to perform, never as a failed join.
func CompletePoolJoinStages(ctx context.Context, s *Store, advertisedOrigin string) protocol.PoolJoinStatus {
	origin := ""
	if advertisedOrigin != "" {
		u, err := peerOrigin(advertisedOrigin)
		if err != nil {
			return protocol.PoolJoinStatus{Stage: "advertised", Status: "needs_attention", Error: "peer origin must be a bare HTTPS origin", ObservedAt: time.Now().UTC()}
		}
		origin = u.String()
	}
	poolJoinMu.Lock()
	defer poolJoinMu.Unlock()
	stored, err := loadPendingJoin(s)
	if err != nil {
		return protocol.PoolJoinStatus{Stage: "rehomed", Status: "needs_attention", Error: "pool join state is unavailable", ObservedAt: time.Now().UTC()}
	}
	// A Controller-driven join's record is the only durable trace of that operation, so it
	// is advanced, never replaced: the exchange this path just completed is the same
	// admission that record was waiting on. Its identity, and the operation the Controller
	// is still tracking, survive.
	pending := &poolPendingJoin{PeerOrigin: origin, Stage: "rehomed"}
	if stored != nil && stored.Status != "succeeded" && stored.Status != "failed" {
		pending = stored
		pending.Invitation, pending.Attempts, pending.LastError = "", 0, ""
		if origin != "" {
			pending.PeerOrigin = origin
		}
		if slices.Index(poolJoinStages, pending.Stage) < slices.Index(poolJoinStages, "rehomed") {
			pending.Stage = "rehomed"
		}
	}
	if err := savePendingJoin(s, pending); err != nil {
		return protocol.PoolJoinStatus{Stage: "rehomed", Status: "needs_attention", Error: "pool join state could not be recorded", ObservedAt: time.Now().UTC()}
	}
	return advancePoolJoin(ctx, s, pending)
}

// PoolJoinReport is the durable join as inventory and the host-local pool view see it. It
// never exposes the invitation or any other credential.
func PoolJoinReport(s *Store) *protocol.PoolJoinStatus {
	pending, err := loadPendingJoin(s)
	if err != nil || pending == nil {
		return nil
	}
	status := poolJoinStatus(pending)
	return &status
}

// advancePoolJoin executes each remaining stage in order, persisting after every one, so a
// crash resumes at the stage that did not complete. Every stage is individually idempotent.
// poolJoinMu must be held; the stages take poolPeerMu themselves.
func advancePoolJoin(ctx context.Context, s *Store, pending *poolPendingJoin) protocol.PoolJoinStatus {
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	for pending.Stage != "ready" {
		var err error
		switch pending.Stage {
		case "precheck":
			err = PoolJoinPrecheck(ctx, s, engine)
		case "csr":
			_, err = PoolJoinCSR(s)
		case "joined":
			err = redeemPoolJoin(ctx, s, pending)
		case "rehomed":
			// The rehome is two durable steps: this Core's own pool identity, then the
			// scope recorded in its managed journal. Both are safe to repeat.
			if err = ResumePoolRehome(ctx, s); err == nil {
				err = vmhost.RehomePoolScope(filepath.Join(s.dir, "managed-vms"), s.State.PoolID, s.State.HostID)
			}
		case "advertised":
			if pending.PeerOrigin != "" {
				err = AdvertisePoolPeerOrigin(ctx, s, pending.PeerOrigin)
			}
		default:
			err = errors.New("unknown pool join stage")
		}
		if err != nil {
			pending.Attempts++
			pending.LastError = resultError(err.Error())
			break
		}
		// The invitation is a bearer credential and it is done with: anyone who could read
		// this file inside its TTL could present it with their own CSR. Nothing after the
		// admission stage needs it, and a resume from any later stage does not.
		if pending.Stage == "joined" {
			pending.Invitation = ""
		}
		pending.Stage, pending.Attempts, pending.LastError = nextPoolJoinStage(pending.Stage), 0, ""
		if saveErr := savePendingJoin(s, pending); saveErr != nil {
			pending.Attempts++
			pending.LastError = resultError(saveErr.Error())
			break
		}
	}
	status := poolJoinStatus(pending)
	if err := savePendingJoin(s, pending); err != nil && status.Status == "succeeded" {
		// The stage completed natively; only its record did not. Report the uncertainty.
		status.Status, status.Error = "needs_attention", resultError(err.Error())
	}
	return status
}

// redeemPoolJoin presents this host's own CSR against the master's invitation. A replay of
// the same invitation returns the same certificate and membership, never a second member.
func redeemPoolJoin(ctx context.Context, s *Store, pending *poolPendingJoin) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil {
		return err
	}
	// The master committed membership before this Core recorded the stage: the join is
	// done, and redeeming again would only risk a second exchange for the same host.
	if state.Membership.Valid() == nil && state.Membership.MasterHostID != s.State.HostID {
		return nil
	}
	if state.PendingCSRPEM == "" {
		return errors.New("member CSR is unavailable for this join")
	}
	bound, err := bindPoolJoinInvitation(pending.Invitation, state.PendingCSRPEM)
	if err != nil {
		return err
	}
	return JoinPoolPeer(ctx, s, pending.MasterURL, pending.CAPEM, bound)
}

// bindPoolJoinInvitation records this host's CSR hash in its own copy of the invitation. The
// master issued it without one and binds the presented CSR at first redemption; naming the
// CSR locally keeps every pinned-offer check in JoinPoolPeer exactly as it is.
func bindPoolJoinInvitation(invitation, csrPEM string) (string, error) {
	decoded, err := decodePeerOffer(invitation)
	if err != nil {
		return "", err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(csrPEM)))
	if decoded.CSRHash != "" && decoded.CSRHash != hash {
		return "", errors.New("join invitation is bound to a different member key")
	}
	decoded.CSRHash = hash
	b, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// poolJoinStatus is the honest projection of a staged join. A host that is already a member
// but has not finished is needs_attention: the join is not undone and is not complete.
func poolJoinStatus(pending *poolPendingJoin) protocol.PoolJoinStatus {
	status := protocol.PoolJoinStatus{OperationID: pending.OperationID, Stage: pending.Stage, Error: pending.LastError, ObservedAt: time.Now().UTC()}
	switch {
	case pending.Stage == "ready":
		status.Status = "succeeded"
	case pending.Stage == "rehomed" || pending.Stage == "advertised":
		status.Status = "needs_attention"
	case pending.Stage == "joined" && pending.LastError != "" && pending.Attempts >= poolJoinMaxAttempts:
		// "joined" is the admission stage: the master commits the membership before its
		// reply is delivered, so an exhausted budget here can mean the host is already a
		// ready member and only the reply was lost. Reporting that as "failed" would leave
		// the master saying member and the host saying not, with both recovery paths shut
		// (minting and redeeming each refuse an existing member). The retry continues —
		// the invitation replays past its own expiry once it has issued a certificate, so
		// the truth stays learnable — and the operator is told to look.
		status.Status = "needs_attention"
	case pending.LastError != "" && (pending.Stage == "precheck" || pending.Attempts >= poolJoinMaxAttempts):
		// A precheck refusal is a decision, not a transient error, and a pre-admission
		// stage that keeps failing is not retried forever as "pending". Only the stages
		// before the master could have admitted this host are bounded this way.
		status.Status = "failed"
	default:
		status.Status = "pending"
	}
	return status
}

func nextPoolJoinStage(stage string) string {
	for i, name := range poolJoinStages[:len(poolJoinStages)-1] {
		if name == stage {
			return poolJoinStages[i+1]
		}
	}
	return "ready"
}

func loadPendingJoin(s *Store) (*poolPendingJoin, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.PendingJoin == nil {
		return nil, err
	}
	pending := *state.PendingJoin
	return &pending, nil
}

// savePendingJoin records the stage and the status derived from it together, so a crash
// between two stages never leaves a status that contradicts the stage it was saved with.
func savePendingJoin(s *Store, pending *poolPendingJoin) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	pending.Status = poolJoinStatus(pending).Status
	pending.UpdatedAt = time.Now().UTC()
	record := *pending
	state.PendingJoin = &record
	return savePoolPeer(s, state)
}
