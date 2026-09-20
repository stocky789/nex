// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolNetworkPath = "/api/v1/pool/networks/"

var poolNetworkMu sync.Mutex
var poolReadyMembership = Membership                                                  // test seam for callers that must not take a second membership snapshot
var poolNetworkAccess = map[string]map[string]map[string]protocol.PoolNetworkAccess{} // dir -> network -> host -> access

// applyPoolNetwork materializes one master-owned VLAN on this member. Tests stub it.
var applyPoolNetwork = applyPoolNetworkNative

func poolNetworkAccessFor(s *Store) map[string]map[string]protocol.PoolNetworkAccess {
	if poolNetworkAccess[s.dir] == nil {
		poolNetworkAccess[s.dir] = map[string]map[string]protocol.PoolNetworkAccess{}
	}
	return poolNetworkAccess[s.dir]
}

// readyPoolMembers lists the other ready hosts this pool master is responsible for, or nil
// when this host is not the master. Only the master reports member access: a member has no
// members of its own, and rows for a host no longer in the pool must stop being reported.
func readyPoolMembers(s *Store) []string {
	membership, err := poolReadyMembership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return nil
	}
	peers := poolConfirmationPeers(membership, s.State.HostID)
	out := make([]string, len(peers))
	for i, member := range peers {
		out[i] = member.HostID
	}
	return out
}

// PoolNetworkMemberAccess overlays process-local member observations onto the master's
// durable VLAN registrations. Access is never rehydrated from disk after restart.
func PoolNetworkMemberAccess(s *Store, networks []protocol.NetworkRegistration) []protocol.NetworkRegistration {
	poolNetworkMu.Lock()
	defer poolNetworkMu.Unlock()
	accesses := poolNetworkAccessFor(s)
	members := readyPoolMembers(s)
	out := append([]protocol.NetworkRegistration(nil), networks...)
	for i := range out {
		out[i].MemberAccess = nil
		if !protocol.IsPoolSharedNetwork(out[i]) {
			continue
		}
		generation := protocol.NetworkGeneration(out[i])
		for _, hostID := range members {
			if access, ok := accesses[out[i].ID][hostID]; ok && access.HostID != "" {
				out[i].MemberAccess = append(out[i].MemberAccess, access)
				continue
			}
			out[i].MemberAccess = append(out[i].MemberAccess, protocol.PoolNetworkAccess{
				NetworkID: out[i].ID, HostID: hostID, Generation: generation,
				Error: "awaiting member reconciliation",
			})
		}
	}
	return out
}

// PoolNetworkHandler serves master-owned VLAN desired state and member access reports.
func PoolNetworkHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		member, err := AuthenticatePoolPeer(s, r)
		if err != nil {
			http.Error(w, "peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/access") {
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, poolNetworkPath), "/access")
			var access protocol.PoolNetworkAccess
			if id == "" || json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&access) != nil || access.HostID != member.HostID || access.NetworkID != id {
				http.Error(w, "invalid access report", http.StatusBadRequest)
				return
			}
			networks, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedNetworks()
			if err != nil {
				http.Error(w, poolJournalIncomplete, http.StatusServiceUnavailable)
				return
			}
			found := false
			for _, n := range networks {
				if n.ID == id && protocol.IsPoolSharedNetwork(n) && protocol.NetworkGeneration(n) == access.Generation {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "network generation unavailable", http.StatusConflict)
				return
			}
			poolNetworkMu.Lock()
			accesses := poolNetworkAccessFor(s)
			if accesses[id] == nil {
				accesses[id] = map[string]protocol.PoolNetworkAccess{}
			}
			accesses[id][member.HostID] = access
			poolNetworkMu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// A member removes what an authoritative list omits, so an incomplete journal must
		// never be served as a short list: it answers 503 and the member removes nothing.
		applied, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedNetworks()
		if err != nil {
			http.Error(w, poolJournalIncomplete, http.StatusServiceUnavailable)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == strings.TrimSuffix(poolNetworkPath, "/") {
			out := []protocol.NetworkRegistration{}
			for _, n := range applied {
				// The peer list is live-only, like the repository list: a member infers a
				// removal from absence in an authoritative list, never from a wire tombstone.
				if protocol.IsPoolSharedNetwork(n) && !n.Tombstone {
					n.MemberAccess = nil
					n.HostID = member.HostID
					if n.Generation == 0 {
						n.Generation = 1
					}
					out = append(out, n)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
			return
		}
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, poolNetworkPath) {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, poolNetworkPath)
		if id == "" || strings.ContainsAny(id, "/\\") {
			http.NotFound(w, r)
			return
		}
		for _, n := range applied {
			if n.ID == id && protocol.IsPoolSharedNetwork(n) && !n.Tombstone {
				n.MemberAccess = nil
				n.HostID = member.HostID
				if n.Generation == 0 {
					n.Generation = 1
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(n)
				return
			}
		}
		http.NotFound(w, r)
	})
}

// ReconcilePoolNetworksFromMaster pulls master-owned VLAN networks onto this member.
func ReconcilePoolNetworksFromMaster(ctx context.Context, s *Store) error {
	return reconcilePoolNetworksFromMaster(ctx, s, 3*time.Second)
}

func reconcilePoolNetworksFromMaster(ctx context.Context, s *Store, perNetworkTimeout time.Duration) error {
	if perNetworkTimeout <= 0 {
		return errors.New("pool network reconciliation timeout is required")
	}
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		return nil
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	listCtx, cancelList := context.WithTimeout(ctx, perNetworkTimeout)
	defer cancelList()
	req, err := http.NewRequestWithContext(listCtx, http.MethodGet, strings.TrimRight(master, "/")+strings.TrimSuffix(poolNetworkPath, "/"), nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("pool master network list unavailable")
	}
	var networks []protocol.NetworkRegistration
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&networks) != nil {
		return errors.New("invalid pool master network list")
	}
	// Authoritative-list gate. Absence means "removed" only in a list that arrived whole,
	// in time, and entirely from this pool; every other shape returns here, before the
	// orphan loop, so a failed list can never tear a member's guest bridge down.
	if err := listCtx.Err(); err != nil {
		return err
	}
	cancelList()
	for _, entry := range networks {
		if entry.PoolID != s.State.PoolID || !protocol.IsPoolSharedNetwork(entry) || entry.Tombstone {
			return errors.New("pool master returned an ineligible network")
		}
	}
	desired := map[string]bool{}
	var last error
	for _, entry := range networks {
		if err := ctx.Err(); err != nil {
			return err
		}
		desired[entry.ID] = true
		work, cancel := context.WithTimeout(ctx, perNetworkTimeout)
		err := reconcilePoolNetwork(work, s, c, master, entry.ID)
		cancel()
		if err != nil {
			last = err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Removal runs after the listed networks were applied, so a network the master still
	// lists is never removed and re-created within one pass, and one the master dropped is
	// never handed to EnsureAppliedNetwork, which would rebuild its bridge.
	if err := removeOrphanPoolNetworks(ctx, s, desired, perNetworkTimeout); err != nil {
		return err
	}
	return last
}

// removeOrphanPoolNetworks tears down the pool VLANs this member still holds that the
// master's authoritative list no longer contains. Only a network of the pool this host is
// currently a member of is eligible: a leftover VLAN from a former pool is not this
// master's to remove. One refused or failed network never stops the others.
func removeOrphanPoolNetworks(ctx context.Context, s *Store, desired map[string]bool, timeout time.Duration) error {
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	applied, err := engine.AppliedNetworks()
	if err != nil {
		return err
	}
	var last error
	for _, entry := range applied {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Tombstone || desired[entry.ID] || !protocol.IsPoolSharedNetwork(entry) || entry.PoolID != s.State.PoolID {
			continue
		}
		// Precheck before Execute, so a persistent refusal writes no journal entry at all:
		// a network a local VM still uses, a bridge Nex cannot prove it created or one with
		// a port attached out of band would otherwise mint one on every probe tick, and a
		// host that never converges would reach the retained-entry cap in about a day.
		if err := engine.CheckNetworkRemoval(ctx, entry.ID); err != nil {
			last = poolConvergeRefusalf("pool network %s cannot be removed on this host: %s", entry.ID, err)
			continue
		}
		generation := protocol.NetworkGeneration(entry)
		work, cancel := context.WithTimeout(ctx, timeout)
		// An unexpected native failure still journals, but the operation ID is deterministic
		// within the UTC hour, so a stuck teardown replays that one entry's stored failure
		// instead of writing a new file every probe tick, and retries for real next hour.
		err := executePoolResourceLifecycle(work, engine, protocol.Command{
			OperationID: poolConvergeOperation("network-remove", entry.ID, generation),
			Kind:        "network-remove",
			ResourceLifecycle: &protocol.ResourceLifecycleRequest{
				Resource: "network", Action: "remove", ResourceID: entry.ID,
				PoolID: s.State.PoolID, HostID: s.State.HostID, ExpectedGeneration: generation,
			},
		})
		cancel()
		if err != nil {
			last = errors.New("pool network removal did not converge")
		}
	}
	return last
}

// executePoolResourceLifecycle runs one member-side removal or rename against the local
// Core engine. The member applies the master's decision itself; it is not a Controller
// command, so it is deliberately not routed through the master-only command fence.
func executePoolResourceLifecycle(ctx context.Context, engine *vmhost.ManagedEngine, cmd protocol.Command) error {
	result, err := engine.Execute(ctx, cmd, nil)
	if err != nil {
		return err
	}
	if result.Status != "succeeded" {
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return errors.New(cmd.Kind + " did not succeed")
	}
	return nil
}

func reconcilePoolNetwork(ctx context.Context, s *Store, c *http.Client, masterURL, networkID string) error {
	if networkID == "" || strings.ContainsAny(networkID, "/\\") {
		return errors.New("invalid pool network ID")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(masterURL, "/")+poolNetworkPath+networkID, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("pool master refused network reconciliation")
	}
	var network protocol.NetworkRegistration
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&network) != nil {
		return errors.New("invalid pool network response")
	}
	if network.ID != networkID || network.PoolID != s.State.PoolID || !protocol.IsPoolSharedNetwork(network) {
		return errors.New("pool network response does not match membership")
	}
	network.HostID = s.State.HostID
	network.Generation = protocol.NetworkGeneration(network)
	network.MemberAccess, network.OperationID, network.Status, network.Error = nil, "", "", ""
	access := protocol.PoolNetworkAccess{
		NetworkID: network.ID, HostID: s.State.HostID, Generation: network.Generation, ObservedAt: time.Now().UTC(),
	}
	if err := applyPoolNetwork(ctx, s, network); err != nil {
		// A replayed journal result is not an observation: this pass did not evaluate the
		// condition, so stamping it with this pass's clock would show the master a failure
		// nobody checked as freshly seen. Leave the member's last real observation standing.
		var replayed poolReplayedFailure
		if errors.As(err, &replayed) {
			return err
		}
		access.Error = err.Error()
		_ = postPoolNetworkAccess(ctx, c, masterURL, network.ID, access)
		return err
	}
	// applyPoolNetwork only returns nil once the guest bridge and its port were observed
	// present on this host, so the reported uplink is an observation, not a journal claim.
	access.Bridge = network.Source
	access.UplinkPresent = true
	return postPoolNetworkAccess(ctx, c, masterURL, network.ID, access)
}

func applyPoolNetworkNative(ctx context.Context, s *Store, network protocol.NetworkRegistration) error {
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	applied, err := engine.AppliedNetworks()
	if err != nil {
		return err
	}
	for _, current := range applied {
		if current.ID != network.ID || current.Tombstone {
			continue
		}
		steps, err := poolNetworkRenameSteps(current, network)
		if err != nil {
			return err
		}
		for ; steps > 0; steps-- {
			generation := protocol.NetworkGeneration(current)
			if err = executePoolResourceLifecycle(ctx, engine, protocol.Command{
				OperationID: poolConvergeOperation("network-update", current.ID, generation),
				Kind:        "network-update",
				ResourceLifecycle: &protocol.ResourceLifecycleRequest{
					Resource: "network", Action: "update", ResourceID: current.ID,
					PoolID: s.State.PoolID, HostID: s.State.HostID,
					ExpectedGeneration: generation, Name: network.Name,
				},
			}); err != nil {
				return err
			}
			current.Name, current.Generation = network.Name, generation+1
		}
		// A journal match is not an observation. Confirm the bridge is still there
		// and rebuild it when it was removed out of band; Execute cannot be reused
		// because the network's resource ID stays reserved by this entry.
		return engine.EnsureAppliedNetwork(ctx, current.ID)
	}
	// A removal on this host is permanent — Execute reserves the resource ID for good — so a
	// master that lists the ID again (restored from an older backup, say) must not be answered
	// with a register that can only fail. Refuse before Execute: no journal entry, and the
	// operator is told what to do about it.
	removed, err := engine.NetworkRemoved(network.ID)
	if err != nil {
		return err
	}
	if removed {
		membership, membershipErr := Membership(s)
		if membershipErr != nil {
			return membershipErr
		}
		command := protocol.Command{OperationID: poolConvergeOperation("network-apply", network.ID, network.Generation), Kind: "network-register", Network: &network}
		allowed, grantErr := engine.EjectCleanupReapplyAvailable(command, s.State.PoolID, s.State.HostID, membership.Generation)
		if grantErr != nil {
			return poolConvergeRefusalf("pool network %s cannot be reapplied: %s", network.ID, grantErr)
		}
		if !allowed {
			return poolConvergeRefusalf("pool network %s was removed on this host and is listed again by the master; remove it on the master and add it again", network.ID)
		}
		if err := poolNetworkApplyRefusal(network); err != nil {
			return err
		}
		result, executeErr := engine.ExecuteEjectCleanupReapply(ctx, command, s.State.PoolID, s.State.HostID, membership.Generation)
		if executeErr != nil {
			return executeErr
		}
		if result.Status != "succeeded" {
			return errors.New(result.Error)
		}
		return nil
	}
	// Precheck before Execute, as the removal path does. A host precondition the native apply
	// refuses — no such uplink yet, the wrong kind of uplink — is re-evaluated on every probe
	// tick and journals nothing, so the operator's fix converges on the next tick instead of
	// waiting for the hour-pinned operation ID below to change.
	if err := poolNetworkApplyRefusal(network); err != nil {
		return err
	}
	started := time.Now().UTC()
	result, err := engine.Execute(ctx, protocol.Command{OperationID: poolConvergeOperation("network-apply", network.ID, network.Generation), Kind: "network-register", Network: &network}, nil)
	if err != nil {
		return err
	}
	if result.Status != "succeeded" {
		detail := result.Error
		if detail == "" {
			detail = "pool network apply did not succeed"
		}
		// An older result means Execute replayed this hour's entry rather than attempting
		// the apply again; the caller must not report it as a new observation.
		if result.ObservedAt.Before(started) {
			return poolReplayedFailure{at: result.ObservedAt, detail: detail}
		}
		return errors.New(detail)
	}
	return nil
}

// poolNetworkApplyRefusal reports the host preconditions of a pool VLAN apply that can be
// evaluated without touching anything, so a member that cannot apply yet journals nothing and
// retries the moment the operator fixes the cause. The conditions and their wording are the
// native apply's own (vmhost.CheckNetworkApply, which ensureGuestBridge runs first), so the
// precheck and the apply can never disagree. It only ever refuses, and never approves an
// apply — a condition it cannot see from here (an uplink the host itself is addressed on,
// unreadable host network configuration) is still refused natively, under the hour-pinned
// operation ID.
func poolNetworkApplyRefusal(n protocol.NetworkRegistration) error {
	if err := protocol.ValidateVLAN(n); err != nil {
		return poolConvergeRefusalf("pool network %s is not a valid VLAN registration: %s", n.ID, err)
	}
	if err := vmhost.CheckNetworkApply(n); err != nil {
		return poolConvergeRefusalf("%s", err)
	}
	return nil
}

// poolNetworkRenameSteps says how many network-update operations this member owes for a
// pool VLAN it already holds. A rename moves only the name, so the member reaches the
// master's generation and reports that generation as its access; Core advances a generation
// by exactly one per update, so a member several renames behind steps through them instead
// of claiming the master's generation it never applied. A changed uplink, VLAN or bridge is
// refused: a bridge that running VMs are attached to is never re-plumbed underneath them.
func poolNetworkRenameSteps(current, desired protocol.NetworkRegistration) (uint64, error) {
	if current.Kind != desired.Kind || current.Source != desired.Source || current.Uplink != desired.Uplink || current.VLAN != desired.VLAN {
		return 0, fmt.Errorf("pool network %s changed uplink or VLAN; remove and re-add it", desired.ID)
	}
	local, master := protocol.NetworkGeneration(current), protocol.NetworkGeneration(desired)
	if master <= local {
		return 0, nil
	}
	return master - local, nil
}

func postPoolNetworkAccess(ctx context.Context, c *http.Client, masterURL, networkID string, access protocol.PoolNetworkAccess) error {
	b, err := json.Marshal(access)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(masterURL, "/")+poolNetworkPath+networkID+"/access", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("pool master refused network access report")
	}
	return nil
}

// poolConvergeStatus records the outcome of the last pool network and media converge pass
// per store. Without it a member's converge errors are discarded, so a host whose VLAN or
// shared ISO never materialized looks healthy. It mirrors pool storage reconciliation.
var poolConvergeStatus = struct {
	sync.Mutex
	byDir map[string]map[string]poolReconcileObservation
}{byDir: map[string]map[string]poolReconcileObservation{}}

const (
	poolNetworkConvergeCheck = "pool-network-reconciliation"
	poolMediaConvergeCheck   = "pool-media-reconciliation"
)

// poolConvergeRefusal is a converge failure this member decided for itself. Its detail is
// built here from this pool's own resource and VM identifiers and is bounded, so it can be
// reported in the host check; remote text never becomes one.
type poolConvergeRefusal struct{ detail string }

func (e poolConvergeRefusal) Error() string { return e.detail }

func poolConvergeRefusalf(format string, args ...any) error {
	detail := fmt.Sprintf(format, args...)
	if len(detail) > 200 {
		detail = detail[:200]
	}
	return poolConvergeRefusal{detail: detail}
}

// poolReplayedFailure is a failure the managed journal replayed for an operation ID this pass
// did not attempt again. It is a stored result, not an observation, so the member reports the
// converge outcome from it but never re-stamps the master's member row with this pass's clock.
type poolReplayedFailure struct {
	at     time.Time
	detail string
}

func (e poolReplayedFailure) Error() string { return e.detail }

// poolJournalIncomplete is what a peer list answers with when this host cannot read its own
// journal whole. It is a fixed string: it names no file and carries nothing from the journal.
const poolJournalIncomplete = "pool resource list unavailable: managed journal is incomplete"

// poolConvergeNow is the clock the operation ID below is pinned to. Tests simulate a day.
var poolConvergeNow = time.Now

// poolConvergeOperation names a member-side converge operation deterministically within the
// current UTC hour. A converge pass runs every probe tick, so a salted ID would write one
// journal entry per tick for an operation that keeps failing and reach the engine's
// retained-entry cap within a day, after which every managed operation on the host fails.
// Replaying the hour's ID returns the stored failure without repeating any effect; the next
// hour's ID retries for real. It is the backstop for a failure no precheck saw coming, and
// bounds one persistently failing resource to 24 journal entries a day; a cause a precheck
// can evaluate is refused before Execute, journals nothing and retries on the next tick.
func poolConvergeOperation(action, id string, generation uint64) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(action+"\x00"+id+"\x00"+strconv.FormatUint(generation, 10)+"\x00"+poolConvergeNow().UTC().Format("2006-01-02T15"))))
}

func recordPoolConverge(s *Store, name string, err error) {
	poolConvergeStatus.Lock()
	defer poolConvergeStatus.Unlock()
	if poolConvergeStatus.byDir[s.dir] == nil {
		poolConvergeStatus.byDir[s.dir] = map[string]poolReconcileObservation{}
	}
	poolConvergeStatus.byDir[s.dir][name] = poolReconcileObservation{at: time.Now().UTC(), err: err}
}

// PoolConvergeChecks reports this member's last pool network and media converge passes.
// Only a member converges, and the reported error is an outcome word, never remote text.
func PoolConvergeChecks(s *Store) []protocol.Check {
	membership, err := Membership(s)
	if err != nil {
		return nil
	}
	if membership.MasterHostID == s.State.HostID {
		now := time.Now().UTC()
		return []protocol.Check{{Name: poolNetworkConvergeCheck, Status: "healthy", Freshness: "fresh", ObservedAt: now}, {Name: poolMediaConvergeCheck, Status: "healthy", Freshness: "fresh", ObservedAt: now}}
	}
	poolConvergeStatus.Lock()
	observed := map[string]poolReconcileObservation{}
	for name, o := range poolConvergeStatus.byDir[s.dir] {
		observed[name] = o
	}
	poolConvergeStatus.Unlock()
	out := []protocol.Check{}
	for _, name := range []string{poolNetworkConvergeCheck, poolMediaConvergeCheck} {
		o, ok := observed[name]
		switch {
		case !ok:
			out = append(out, protocol.Check{Name: name, Status: "unknown", Freshness: "unknown", ObservedAt: time.Now().UTC(), Error: "awaiting first member reconciliation"})
		case time.Since(o.at) > 2*time.Minute:
			out = append(out, protocol.Check{Name: name, Status: "unknown", Freshness: "stale", ObservedAt: o.at, Error: name + " observation is stale"})
		case o.err != nil:
			outcome := "failed"
			if errors.Is(o.err, context.DeadlineExceeded) {
				outcome = "deadline"
			}
			if errors.Is(o.err, context.Canceled) {
				outcome = "cancelled"
			}
			message := name + " " + outcome
			// A removal this member refused is its own bounded decision, and the operator
			// cannot act on it without the resource and VM it names.
			var refusal poolConvergeRefusal
			if errors.As(o.err, &refusal) {
				message += ": " + refusal.detail
			}
			out = append(out, protocol.Check{Name: name, Status: "failed", Freshness: "fresh", ObservedAt: o.at, Error: message})
		default:
			out = append(out, protocol.Check{Name: name, Status: "healthy", Freshness: "fresh", ObservedAt: o.at})
		}
	}
	return out
}

// ClearPoolConvergeStatusForTest resets process-local converge observations.
func ClearPoolConvergeStatusForTest(s *Store) {
	poolConvergeStatus.Lock()
	defer poolConvergeStatus.Unlock()
	delete(poolConvergeStatus.byDir, s.dir)
}

// ClearPoolNetworkAccessForTest resets process-local network access observations.
func ClearPoolNetworkAccessForTest(s *Store) {
	poolNetworkMu.Lock()
	defer poolNetworkMu.Unlock()
	delete(poolNetworkAccess, s.dir)
}
