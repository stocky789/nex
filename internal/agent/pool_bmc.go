// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Every host configures its own management controller. Nothing here asks an operator for a
// BMC address or a password: the host reads both from its own hardware over /dev/ipmi0,
// creates one account for fencing, and offers the profile and its password to the pool
// master, which proves the controller really is this machine's before it records anything.
//
// The rules that make this safe to run unattended on every reconcile pass:
//
//   - An account Nex Core did not create is never touched. The evidence that it did is this
//     host's own durable record, or the master's membership record for this host, and
//     nothing else — so a reinstalled host that rejoins its pool still recognises its own
//     account, and a host that finds somebody else's reports a conflict and changes nothing.
//   - A profile an operator entered by hand always wins. Automatic provisioning stops at
//     the report and never replaces it.
//   - An account created but not recorded is not a fence device. It is reported as pending
//     and offered to the master again, with a fresh password, on the next pass — so an
//     interrupted provisioning leaves no profile that looks usable and no second account.
//   - Removing the account is an operator decision that sticks: this host does not provision
//     again until the operator asks it to.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolFenceProvisionPath = "/api/v1/pool/fence-provision"

// The in-band operations, as package variables so a lab can stand in for a machine's own
// management controller the way localSystemUUID stands in for its SMBIOS identity.
var planLocalBMCAccount = vmhost.PlanLocalBMCAccount
var configureLocalBMCAccount = vmhost.ConfigureLocalBMCAccount
var removeLocalBMCAccount = vmhost.RemoveLocalBMCAccount

// poolLocalBMCRecord is this host's own durable memory of what it did to its own management
// controller. It holds no password: the material this host wrote lives in its own
// fence-credential store, under the reference Ref names, with exactly the protection the
// master gives it.
type poolLocalBMCRecord struct {
	// PoolID is the pool this record was made in. A host that leaves keeps the account it
	// created on its own controller, so the record must outlive the pool — otherwise the
	// leftover account becomes a permanent name conflict — but nothing it says about what
	// a pool recorded survives with it. See EnsureLocalBMCFence.
	PoolID        string
	Channel, Slot uint8
	Created       bool // Nex Core owns the account in this slot, so it is Nex's to finish or replace.
	// Pending is the profile this host offered and the master has not recorded yet. It is
	// kept so a later pass offers the same account and the same password again: writing a
	// new password to the controller's flash on every ten-second pass, for as long as the
	// master is away, is what this field exists to prevent.
	Pending  *protocol.StorageClusterFence
	Recorded bool // The pool master accepted the profile; there is nothing left to do.
	// Ref names the password script this host wrote for that account. It is saved before
	// the script exists and outlives Pending, so every way out of provisioning — recorded,
	// withdrawn, left the pool, or interrupted between the two writes — still has a name
	// for the file and can retire it. Without it an interrupted pass leaves a root-only
	// file holding a live-looking secret that nothing names any more.
	Ref string
	// RetryAfter is set when the management controller refused a write. Provisioning
	// starts over by setting the account's name and a new password, so retrying on every
	// ten-second reconcile pass would rewrite a controller that will not take it to its
	// flash for ever.
	RetryAfter time.Time
}

// poolFenceProvision is what a member offers its master: the profile it wants recorded, and
// the password of the account it just created. It travels only over the pool's own mTLS peer
// connection, is never logged, and is never part of inventory or of any Controller request.
type poolFenceProvision struct {
	Fence    protocol.StorageClusterFence `json:"fence"`
	Password string                       `json:"password"`
}

// EnsureLocalBMCFence is one reconcile pass over this host's own management controller. It
// runs whether or not anything is wrong, and it is a no-op once the profile it created is
// recorded in the pool.
func EnsureLocalBMCFence(ctx context.Context, s *Store) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		return err
	}
	self, ok := poolMemberByID(state.Membership, s.State.HostID)
	if !ok {
		return nil
	}
	record := state.LocalBMC
	if record != nil && record.PoolID != state.Membership.PoolID {
		// This record was made in a pool this host has left. The account on the
		// controller is still this host's own, so the ownership marker stays — without it
		// the leftover account would be a name conflict for ever. Everything the old pool
		// decided goes: the new pool has recorded nothing, and an offer made to a master
		// that is no longer this host's master is not an offer.
		_ = vmhost.RetirePoolFenceCredential(s.dir, record.Ref)
		record = &poolLocalBMCRecord{Channel: record.Channel, Slot: record.Slot, Created: record.Created}
		if err = saveLocalBMCRecord(s, record); err != nil {
			return err
		}
	}
	// A manual profile is the operator's decision and wins, whatever else is recorded
	// against this host: reporting a withdrawal beside a configured device would be untrue.
	manual := self.Fence != nil && !self.Fence.ProvisionedByCore
	switch {
	case self.BMCProvisioningDisabled:
		// The operator withdrew this host's fence device. The account goes with it: the
		// pool master already cleared the profile, and an account nothing fences through
		// has no business existing on the controller.
		return removeLocalBMCFence(ctx, s, record, self.BMC, manual)
	case manual:
		return reportLocalBMC(ctx, s, protocol.PoolHostBMC{State: protocol.PoolBMCManualProfile,
			Detail: "this host's fence device was set by an operator, so Nex Core leaves the management controller alone"}, self.BMC)
	case record != nil && record.Recorded:
		// Done: this host made the account and the master accepted the profile. The copy
		// of the membership a member holds is a replica that can lag, so this host's own
		// record is what decides — otherwise every pass before the next replica arrived
		// would offer a new password for an account that already has one.
		return nil
	case record != nil && record.Pending != nil:
		// The account exists and the master has not recorded it. Offer the same profile
		// and the same password again rather than writing a new one to the controller's
		// flash on every pass for as long as the master is away.
		return offerPendingFence(ctx, s, state, *record, self.BMC)
	case record != nil && time.Now().Before(record.RetryAfter):
		// The controller refused a write recently. Its last report stands until the
		// wait is over: a controller that refuses this refuses it again, and the
		// reconcile pass runs far too often to keep asking.
		return nil
	}

	known := vmhost.LocalBMCAccount{Created: record != nil && record.Created}
	if record != nil {
		known.Slot = record.Slot
	}
	// The pool's own record of this host's profile is the other evidence that the account
	// under the Nex name is this host's own: a reinstalled host has lost its local record,
	// but the pool it rejoins has not.
	if self.Fence != nil && self.Fence.ProvisionedByCore && self.Fence.Username == vmhost.BMCFenceAccount {
		known.Created = true
	}
	plan := planLocalBMCAccount(ctx, known)
	report := protocol.PoolHostBMC{State: plan.State, Detail: plan.Detail, Address: plan.Address,
		Channel: plan.Channel, Account: plan.Account}
	if plan.State != "" {
		return reportLocalBMC(ctx, s, report, self.BMC)
	}
	// From here the controller is about to be changed, so the slot and the name of the
	// password script are recorded first. A controller that refuses halfway through then
	// leaves an account this host still knows is its own: the next pass finishes it,
	// instead of refusing for ever to touch a `nexfence` account it made itself.
	ref := ""
	if record != nil {
		ref = record.Ref
	}
	if ref == "" {
		if ref, err = privateReference(); err != nil {
			return err
		}
	}
	saved := poolLocalBMCRecord{Channel: plan.Channel, Slot: plan.Slot, Created: true, Ref: ref}
	if err = saveLocalBMCRecord(s, &saved); err != nil {
		return err
	}
	password, err := configureLocalBMCAccount(ctx, plan)
	if err != nil {
		saved.RetryAfter = time.Now().Add(poolFenceCheckInterval)
		if saveErr := saveLocalBMCRecord(s, &saved); saveErr != nil {
			return saveErr
		}
		report.State, report.Detail = protocol.PoolBMCUserRefused, err.Error()
		return reportLocalBMC(ctx, s, report, self.BMC)
	}
	fence := protocol.StorageClusterFence{
		Kind: protocol.FenceIPMI, Endpoint: plan.Address, TargetID: "1", Username: plan.Account,
		Cipher: plan.Cipher, ProvisionedByCore: true, CredentialRef: ref,
	}
	// The password is kept where the master keeps it — an executable script in the private
	// fence-credential store — so an offer the master has not taken yet can be repeated
	// after a reboot without touching the controller again. Any script this host wrote
	// under the same name is dead the moment the controller took a new password, and the
	// write refuses to replace material it finds there.
	_ = vmhost.RetirePoolFenceCredential(s.dir, ref)
	if err = vmhost.WritePoolFenceCredential(s.dir, fence, map[string][]byte{"": vmhost.FencePasswordScript(password)}); err != nil {
		return err
	}
	pending := poolLocalBMCRecord{Channel: plan.Channel, Slot: plan.Slot, Created: true, Ref: ref, Pending: &fence}
	if err = saveLocalBMCRecord(s, &pending); err != nil {
		return err
	}
	return offerPendingFence(ctx, s, state, pending, self.BMC)
}

// offerPendingFence offers the master the profile this host already configured, with the
// password it already set. Until the master records it there is no fence device, and the host
// says so rather than leaving a profile that looks usable.
func offerPendingFence(ctx context.Context, s *Store, state poolPeerState, record poolLocalBMCRecord, reported *protocol.PoolHostBMC) error {
	fence := *record.Pending
	report := protocol.PoolHostBMC{State: protocol.PoolBMCProvisioned, Address: fence.Endpoint,
		Channel: record.Channel, Account: fence.Username}
	password, err := vmhost.ReadFenceCredentialSecret(s.dir, fence.CredentialRef)
	if err == nil {
		err = offerProvisionedFence(ctx, s, state, fence, password)
	}
	if err != nil {
		report.State = protocol.PoolBMCPendingMaster
		report.Detail = "this host created its fence account, but the pool master has not recorded it yet: " + err.Error() + ". This host offers the same account again on its next pass."
		return reportLocalBMC(ctx, s, report, reported)
	}
	done := record
	done.Pending, done.Recorded = nil, true
	if err = saveLocalBMCRecord(s, &done); err != nil {
		return err
	}
	return reportLocalBMC(ctx, s, report, reported)
}

// removeLocalBMCFence takes the account off this host's own controller once an operator has
// withdrawn its fence device. A controller that will not take it off is reported honestly and
// tried again; the operator's decision itself is the pool master's and has already been made.
func removeLocalBMCFence(ctx context.Context, s *Store, record *poolLocalBMCRecord, reported *protocol.PoolHostBMC, manual bool) error {
	report := protocol.PoolHostBMC{State: protocol.PoolBMCRemoved,
		Detail: "an operator withdrew this host's fence device, so Nex Core removed the account it had created on the management controller and will not create another until automatic provisioning is turned back on for this host"}
	if manual {
		// The withdrawal still removes the account, but the host has a fence device
		// again and must not report that it has none.
		report = protocol.PoolHostBMC{State: protocol.PoolBMCManualProfile,
			Detail: "this host's fence device was set by an operator, so Nex Core leaves the management controller alone"}
	}
	if record == nil || !record.Created {
		return reportLocalBMC(ctx, s, report, reported)
	}
	if err := removeLocalBMCAccount(ctx, record.Channel, record.Slot); err != nil {
		report.Detail = "an operator withdrew this host's fence device, but the account Nex Core created is still on the management controller: " + err.Error() + ". This host tries again on its next pass; remove it by hand if the controller does not recover."
		pending := *record
		pending.Pending, pending.Recorded = nil, false
		if saveErr := saveLocalBMCRecord(s, &pending); saveErr != nil {
			return saveErr
		}
		return reportLocalBMC(ctx, s, report, reported)
	}
	// The account is gone, so the password of it has nothing left to open. Leaving the
	// script behind would leave a root-only file holding a live-looking secret that nothing
	// names any more — whether or not the master ever recorded the profile.
	_ = vmhost.RetirePoolFenceCredential(s.dir, record.Ref)
	if err := saveLocalBMCRecord(s, nil); err != nil {
		return err
	}
	return reportLocalBMC(ctx, s, report, reported)
}

// offerProvisionedFence hands the profile and its password to whoever keeps the pool's
// durable record. The master records its own; a member sends one peer request over the
// pool's mTLS and lets the master prove the controller for itself.
func offerProvisionedFence(ctx context.Context, s *Store, state poolPeerState, fence protocol.StorageClusterFence, password string) error {
	if state.Membership.MasterHostID == s.State.HostID {
		return AcceptProvisionedFence(s, s.State.HostID, fence, password)
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return errors.New("the pool master is not reachable from this host")
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		return errors.New("the pool master is not reachable from this host")
	}
	defer c.CloseIdleConnections()
	b, _ := json.Marshal(poolFenceProvision{Fence: fence, Password: password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolFenceProvisionPath, bytes.NewReader(b))
	if err != nil {
		return errors.New("the pool master is not reachable from this host")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return errors.New("the pool master is not reachable from this host")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		// An older master serves no such route. Saying so names the upgrade rather than
		// leaving an operator with an unexplained pending host.
		return errors.New("this pool master is too old to record a fence device a host configured itself; upgrade it")
	}
	// The master's own refusal sentence, which is what an operator has to act on.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if refusal := strings.TrimSpace(string(body)); refusal != "" {
		return errors.New(refusal)
	}
	return errors.New("the pool master refused this host's fence device")
}

// AcceptProvisionedFence records a profile a host's own Core created on its own management
// controller. It writes the password into this master's fence-credential store first,
// because the identity check that follows signs in with it — and then goes through exactly
// the recording path an operator-entered profile goes through, including proving that the
// controller really manages that host's machine before anything is recorded.
func AcceptProvisionedFence(s *Store, hostID string, fence protocol.StorageClusterFence, password string) error {
	if !fence.ProvisionedByCore || fence.Kind != protocol.FenceIPMI || fence.Username != vmhost.BMCFenceAccount ||
		fence.Valid() != nil || len(password) > 64 || strings.ContainsAny(password, "\x00\r\n") ||
		vmhost.FenceSecretUsable(password) != nil {
		return errors.New("this is not a fence device a host configured for itself")
	}
	if err := vmhost.WritePoolFenceCredential(s.dir, fence, map[string][]byte{"": vmhost.FencePasswordScript(password)}); err != nil {
		return errors.New("this pool master could not keep the fence account's password")
	}
	requestID, err := privateReference()
	if err != nil {
		return err
	}
	result := ExecutePoolFenceProfile(s, protocol.Command{OperationID: requestID, Kind: "pool-fence-profile",
		Pool: &protocol.PoolCommand{RequestID: requestID, Action: "fence-profile", MemberHostID: hostID,
			Membership: protocol.PoolMembership{MasterHostID: s.State.HostID}, Fence: &fence}})
	if result.Status != "succeeded" {
		return errors.New(result.Error)
	}
	return nil
}

// PoolFenceProvisionHandler serves a member's offer of the fence device its own Core created.
// The peer certificate says which host is calling, and a host may only offer its own: the
// profile is recorded for the caller, never for a host it names.
func PoolFenceProvisionHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		member, err := AuthenticatePoolPeer(s, r)
		if err != nil {
			http.Error(w, "peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var v poolFenceProvision
		if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&v) != nil {
			http.Error(w, "invalid fence device", http.StatusBadRequest)
			return
		}
		if err = AcceptProvisionedFence(s, member.HostID, v.Fence, v.Password); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// ExecutePoolBMCProvisioning lets a host configure its own management controller again after
// an operator withdrew its fence device. It is the master's record, like every other pool
// decision: the host reads it from the membership it already holds and acts on its own
// controller on its next pass. Withdrawing is the other direction and is not a second
// operation — clearing the fence device is what withdraws it, through the one path that
// clears a profile.
func ExecutePoolBMCProvisioning(s *Store, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	v := cmd.Pool
	if v == nil || v.Action != "bmc-provisioning" {
		result.Error = "this command carries no management controller request"
		return result
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil || state.Membership.Valid() != nil {
		result.Error = "pool membership is unavailable"
		return result
	}
	if state.Membership.MasterHostID != s.State.HostID {
		result.Error = "only the pool master records whether a host configures its own management controller"
		return result
	}
	if err = poolAuthorityWritable(state); err != nil {
		result.Error = err.Error()
		return result
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID != v.MemberHostID {
			continue
		}
		if !state.Membership.Members[i].BMCProvisioningDisabled {
			result.Status = "succeeded" // A desired state, not a transition.
			return result
		}
		state.Membership.Members[i].BMCProvisioningDisabled = false
		// The host's own next pass creates the account; reporting one now would claim a
		// fence device that does not exist yet.
		state.Membership.Members[i].BMC = nil
		if err = savePoolPeer(s, state); err != nil {
			result.Status, result.Error = "needs_attention", "this decision could not be recorded durably"
			return result
		}
		result.Status = "succeeded"
		return result
	}
	result.Error = "no such member of this pool"
	return result
}

// reportLocalBMC puts this host's own finding in the master's membership record, where a
// fresh Controller reads it with the rest of the membership. The master writes its own row;
// a member advertises, exactly as it does its machine identity.
//
// A finding that has not changed is not reported at all. The membership is signed and
// replicated, so re-reporting an unchanged state — which is every state a settled pool sits
// in — would move the pool's authority generation and push a fresh replica to every member on
// every ten-second pass, for ever, because of a timestamp.
func reportLocalBMC(ctx context.Context, s *Store, report protocol.PoolHostBMC, reported *protocol.PoolHostBMC) error {
	if samePoolBMCReport(reported, report) {
		return nil
	}
	if report.ObservedAt.IsZero() {
		report.ObservedAt = time.Now().UTC()
	}
	if err := report.Valid(); err != nil {
		return err
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	master := ""
	if err == nil && state.Membership.Valid() == nil {
		master = state.Membership.MasterHostID
		if master == s.State.HostID {
			err = poolAuthorityWritable(state)
			if err == nil {
				err = applyPoolBMCReport(s, &state, s.State.HostID, report)
			}
		}
	}
	poolPeerMu.Unlock()
	if err != nil || master == "" || master == s.State.HostID {
		return err
	}
	return postPoolAdvertise(ctx, s, map[string]any{"bmc": report})
}

// applyPoolBMCReport writes one host's report into the master's membership. It is an
// observation and nothing more: a host says what it found on its own controller, and cannot
// change what the pool decided about it. Withdrawing a fence device is the operator's
// decision, authenticated at the Controller and recorded by the master, exactly as recording
// one is — a host cannot delete its own fence device by describing itself.
//
// A report that has not changed is not written, so the pool's signed authority does not move
// because a timestamp did.
func applyPoolBMCReport(s *Store, state *poolPeerState, hostID string, report protocol.PoolHostBMC) error {
	for i := range state.Membership.Members {
		member := &state.Membership.Members[i]
		if member.HostID != hostID {
			continue
		}
		if samePoolBMCReport(member.BMC, report) {
			return nil
		}
		member.BMC = &report
		return savePoolPeer(s, *state)
	}
	return errors.New("no such member of this pool")
}

// samePoolBMCReport compares two findings by what they say, not by when they were observed.
func samePoolBMCReport(recorded *protocol.PoolHostBMC, report protocol.PoolHostBMC) bool {
	if recorded == nil {
		return false
	}
	report.ObservedAt = recorded.ObservedAt
	return *recorded == report
}

// saveLocalBMCRecord keeps this host's own memory of its own controller. A nil record is how
// an operator turning automatic provisioning back on is recorded: nothing is known again.
func saveLocalBMCRecord(s *Store, record *poolLocalBMCRecord) error {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	if record != nil {
		// Stamped here rather than at each call site, so a record can never be saved
		// without the pool it belongs to.
		record.PoolID = state.Membership.PoolID
	}
	state.LocalBMC = record
	return savePoolPeer(s, state)
}
