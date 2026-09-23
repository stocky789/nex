// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PoolMember is a Core-master-owned membership record.  The Controller mirrors it from
// inventory; it must not infer membership from which hosts happen to be connected.
type PoolMember struct {
	HostID            string    `json:"host_id"`
	CertificateSHA256 string    `json:"certificate_sha256"`
	Generation        uint64    `json:"generation"`
	Status            string    `json:"status"`
	ObservedAt        time.Time `json:"observed_at"`
	Error             string    `json:"error,omitempty"`
	PeerOrigin        string    `json:"peer_origin,omitempty"`
	// Maintenance is the master's durable drain intent for this host: it refuses new
	// placements and new starts there and changes nothing that is already running.  It is
	// deliberately outside the generation equality rule below, so draining a host never
	// invalidates an in-flight placement, a published inventory or a peer generation check.
	// MaintenanceSince is always present and is the zero time when Maintenance is false,
	// in the manner of ObservedAt above: a time.Time cannot be omitted when it is zero,
	// and a member that reports a zero time is one that is not drained.
	Maintenance      bool      `json:"maintenance,omitempty"`
	MaintenanceSince time.Time `json:"maintenance_since"`
	// ReplicaGeneration, ReplicaDigest and ReplicaObservedAt are the master's observation
	// of what this member acknowledged holding of the signed authority replica.  They are
	// memory-only observations overlaid on the reported copy of this record, never part of
	// the durable membership and never an input to the generation equality rule below: a
	// member that has not acknowledged anything is reported at generation 0, honestly.
	ReplicaGeneration uint64    `json:"replica_generation,omitempty"`
	ReplicaDigest     string    `json:"replica_digest,omitempty"`
	ReplicaError      string    `json:"replica_error,omitempty"`
	ReplicaObservedAt time.Time `json:"replica_observed_at"`
	// Fence is this host's power-fence profile, the same closed typed profile clustered
	// storage already uses, kept in the master's membership and replicated with it.  It is
	// what lets a surviving member prove the old master is excluded before it promotes
	// itself.  Credentials are never in it: CredentialRef names a Core-private secret
	// delivered by the existing fence-credential handoff.  Like Maintenance above it is
	// outside the generation equality rule, so recording a fence profile never invalidates
	// an in-flight placement or a published inventory.
	Fence *StorageClusterFence `json:"fence,omitempty"`
	// SystemUUID is this host's own SMBIOS system UUID, reported by the host itself while
	// it was alive and kept here so another host can prove, before it powers anything off,
	// that the BMC it is talking to belongs to this machine and not to some other one that
	// answers at the same address.  Like the fields above it is outside the generation
	// equality rule: learning a host's UUID is an observation, not a membership change.  A
	// member too old to report one stays empty and simply cannot get a BMC fence proof.
	SystemUUID string `json:"system_uuid,omitempty"`
	// SystemUUIDConflictHostID names the member that already reported the identity this
	// host reports, when the master refused this host's report because of it.  Without it
	// the refusal is a dead end: an operator is told their host has no unique identity and
	// has nothing to act on.  With it they can see which host claims the identity and
	// eject it if it is not theirs.  It is cleared the moment the identity is accepted.
	SystemUUIDConflictHostID string `json:"system_uuid_conflict_host_id,omitempty"`
	// BMC is what this host's own Core found and did about its own management controller,
	// in-band, and reported to the master.  It is an observation and a result, never a
	// credential: the account's password goes to the master's fence-credential store and
	// nowhere else.  A member too old to report one stays empty.
	BMC *PoolHostBMC `json:"bmc,omitempty"`
	// BMCProvisioningDisabled is the operator's own decision, recorded by the master, that
	// this host must not configure its management controller for fencing.  Clearing a fence
	// device Nex Core provisioned sets it — one operator action with one meaning — and the
	// host removes the account it created when it sees it.  It is a decision, not an
	// observation, so it belongs with the fence profile in the replicated membership and a
	// host cannot set or clear it by reporting about itself.
	BMCProvisioningDisabled bool `json:"bmc_provisioning_disabled,omitempty"`
	// Watchdog is this host's own report of the watchdog device it has, read from sysfs
	// while it was alive and kept here because the watchdog method has to be answered for
	// a host that is gone.  Like SystemUUID it is a slow-changing capability rather than a
	// running observation: it is written only when the finding changes, and it carries no
	// timestamp because nothing decides anything from how long ago a host last said it.
	// A member too old to report one stays nil, which is "not reported" and never counts.
	Watchdog *PoolHostWatchdog `json:"watchdog,omitempty"`
	// WatchdogArmed is this host's own report that it has armed that device, kept durably
	// and replicated because it is the first of the three conditions of a watchdog
	// exclusion proof, and the host that needs to read it is a candidate whose master has
	// gone.  It is separate from the capability above because it has different rules: it
	// carries the pool's fencing generation, and a report from an older one is not
	// evidence about the pool as it is now.  A member too old to report one stays nil.
	WatchdogArmed *PoolWatchdogArmed `json:"watchdog_armed,omitempty"`
	// FenceReadiness is the master's own answer to "could this host be shut out right
	// now", per method, with the reason when it could not.  It is derived from the
	// membership above and from the continuous non-destructive checks other hosts report,
	// so like ReplicaGeneration it is a memory-only observation overlaid on the reported
	// copy of this record and never part of the durable membership: a master that has just
	// restarted reports "not checked yet" until the next pass, honestly, rather than
	// keeping a durable store of something it can re-establish in one interval.  A master
	// too old to report one leaves it nil, which is "not reported" and never ready.
	FenceReadiness *PoolFenceReadiness `json:"fence_readiness,omitempty"`
	// FenceCredentialVersion is the master's counter for the private material behind that
	// profile.  The material itself is never here, never in inventory and never in a
	// Controller: only the master's own private digest of it decides when this number
	// moves.  It is inside the replicated membership so that rotating a credential changes
	// the replicated authority's content digest and therefore advances the authority
	// generation, which is what makes every member receive the new material.  Like the two
	// fields above it is outside the generation equality rule.
	FenceCredentialVersion uint64 `json:"fence_credential_version,omitempty"`
}

// PoolMembership is retained by the master and copied to a member only after the member
// proves the master's certificate-bound identity.  Generation prevents an old peer request
// from resurrecting an ejected member.
type PoolMembership struct {
	PoolID       string       `json:"pool_id"`
	MasterHostID string       `json:"master_host_id"`
	Generation   uint64       `json:"generation"`
	Members      []PoolMember `json:"members"`
	// HostFencingDisabled is the pool's Host fencing setting, recorded the way it is
	// decided: fencing is on for a pool that has never been told otherwise, so an absent
	// field is "on" and a record written before the setting existed keeps the default
	// without migrating anything.  It is a decision, so unlike the observations below it
	// is durable, replicated and only ever written by the master.
	HostFencingDisabled bool `json:"host_fencing_disabled,omitempty"`
	// FenceSummary is the master's own count, the setting above and Core's prerequisite
	// checklist, computed over this membership when it is reported.  Like the readiness on
	// each member row it is a memory-only observation overlaid on the reported copy and is
	// never part of the durable record: a master that has just restarted reports "not
	// checked yet" until its next pass rather than keeping a store of it.
	FenceSummary *PoolFenceSummary `json:"fence_summary,omitempty"`
}

// PoolJoinCapability gates the Controller-driven join: the brokered invitation envelope
// below, the pool-join command and the reported join status.  A Core without it can
// neither issue an invitation nor be told to join, and the Controller says so instead of
// falling back to a partial exchange.
const PoolJoinCapability = "pool-join-v1"

// DefaultPoolPeerPort is the port a Core's pool-peer listener binds unless configured
// otherwise; a Controller assumes it only for a host that does not report its peer origin.
const DefaultPoolPeerPort = 9444

// PoolMaintenanceCapability gates the drain flag: the maintenance command and the two
// PoolMember fields above.  A Controller talking to a master without it offers no toggle,
// because the refusal that makes the flag mean anything lives in the master.
const PoolMaintenanceCapability = "pool-maintenance-v1"

// PoolAuthorityReplicaCapability gates the master's signed authority replica: the peer
// push/ack below, the member's stored copy and the reported replica generation.  A master
// with it and a member without it records that member at replica generation 0 with an
// upgrade-required reason; the master never degrades to an unsigned or partial copy.
const PoolAuthorityReplicaCapability = "pool-authority-replica-v1"

// PoolAuthorityTransferCapability gates a planned master change and a member eject: the
// transfer command, the two peer endpoints below and the reported transfer status.  It is
// required on the master, on the target and on every ready member, because a member that
// cannot re-point would keep following a host that is no longer the pool master.
const PoolAuthorityTransferCapability = "pool-authority-transfer-v1"

// PoolTransferLimit bounds one transfer body.  A notice carries one membership record and
// nothing else, so it is far smaller than a replica.
const PoolTransferLimit = 256 << 10

// PoolAuthorityNotice is the pool-CA-signed statement that pool authority moved.  It is
// used twice in one transfer, and both uses are verified against the pool CA every host
// already holds, so no host ever takes another host's word for who the master is:
//
//   - the hand-off, signed by the outgoing master, naming the exact authority generation
//     it froze and the host it handed that generation to.  Membership is empty; the target
//     activates only on the replica it already holds at exactly that generation.
//   - the re-point, signed by the new master with the pool CA key it now holds, carrying
//     the new membership at a strictly higher membership generation.  Every other host —
//     including the outgoing master — adopts it only because it is signed and higher.
type PoolAuthorityNotice struct {
	PoolID      string `json:"pool_id"`
	OperationID string `json:"operation_id"`
	Action      string `json:"action"` // promote or eject
	// FromHostID issued this notice; ToHostID is the host it addresses.  TargetHostID is
	// the transfer's subject: the promoted host, or the ejected one.
	FromHostID          string         `json:"from_host_id"`
	ToHostID            string         `json:"to_host_id"`
	TargetHostID        string         `json:"target_host_id"`
	AuthorityGeneration uint64         `json:"authority_generation"`
	Membership          PoolMembership `json:"membership"`
	IssuedAt            time.Time      `json:"issued_at"`
	Signature           string         `json:"signature,omitempty"`
}

// SigningBytes is the whole notice but its signature.
func (n PoolAuthorityNotice) SigningBytes() ([]byte, error) {
	return signingBytesClearSignature(n, func(v *PoolAuthorityNotice) { v.Signature = "" })
}

func (n PoolAuthorityNotice) Valid() error {
	if n.PoolID == "" || len(n.PoolID) > 128 || n.OperationID == "" || len(n.OperationID) > 128 {
		return errors.New("pool authority notice requires a pool and an operation")
	}
	if n.Action != "promote" && n.Action != "eject" {
		return errors.New("pool authority notice action is not a master change or an eject")
	}
	for _, host := range []string{n.FromHostID, n.ToHostID, n.TargetHostID} {
		if host == "" || len(host) > 128 {
			return errors.New("pool authority notice requires issuing, addressed and target hosts")
		}
	}
	if n.AuthorityGeneration == 0 {
		return errors.New("pool authority notice requires an authority generation")
	}
	if n.Signature == "" || len(n.Signature) > 1024 {
		return errors.New("pool authority notice is not signed")
	}
	return nil
}

// PoolTransferRequest is one step of a transfer, addressed to one peer.  Every action is
// idempotent by operation ID: the same request repeated after a lost reply returns the same
// answer and performs the step at most once.
type PoolTransferRequest struct {
	OperationID string              `json:"operation_id"`
	Action      string              `json:"action"` // activate, abort, repoint or leave
	Notice      PoolAuthorityNotice `json:"notice"`
}

func (r PoolTransferRequest) Valid() error {
	if r.OperationID == "" || len(r.OperationID) > 128 {
		return errors.New("pool transfer requires an operation")
	}
	switch r.Action {
	case "activate", "repoint", "leave":
		if err := r.Notice.Valid(); err != nil {
			return err
		}
		if r.Notice.OperationID != r.OperationID {
			return errors.New("pool transfer notice names another operation")
		}
		return nil
	case "abort":
		return nil
	}
	return errors.New("invalid pool transfer action")
}

// PoolTransferReply is the addressed host's own answer.  Activated is the fact an abort
// needs: whether this host had already adopted the authority, decided once under its own
// lock, so an abort and an activation can never both succeed.
type PoolTransferReply struct {
	HostID               string `json:"host_id"`
	OperationID          string `json:"operation_id"`
	Action               string `json:"action"`
	Accepted             bool   `json:"accepted"`
	Activated            bool   `json:"activated,omitempty"`
	AuthorityGeneration  uint64 `json:"authority_generation,omitempty"`
	MembershipGeneration uint64 `json:"membership_generation,omitempty"`
	// Notice is the new master's signed re-point notice, returned with an accepted
	// activation so the outgoing master demotes itself on signed evidence, not on its own
	// assumption that the activation worked.
	Notice *PoolAuthorityNotice `json:"notice,omitempty"`
	Error  string               `json:"error,omitempty"`
}

// PoolAuthorityStatus is the read-only authority probe every host answers to any holder of
// a pool certificate.  It is how a returning or restarted host discovers that it has been
// superseded or ejected, and how a host that missed a re-point finds the current master
// without trusting a stale one: the notice below is signed, and a probe answer alone
// changes nothing.
type PoolAuthorityStatus struct {
	HostID               string `json:"host_id"`
	PoolID               string `json:"pool_id"`
	Role                 string `json:"role"` // master or member
	MasterHostID         string `json:"master_host_id"`
	MasterPeerOrigin     string `json:"master_peer_origin,omitempty"`
	AuthorityGeneration  uint64 `json:"authority_generation"`
	MembershipGeneration uint64 `json:"membership_generation"`
	ReplicaGeneration    uint64 `json:"replica_generation"`
	ReplicaVerified      bool   `json:"replica_verified"`
	Frozen               bool   `json:"frozen"`
	HandedOff            bool   `json:"handed_off"`
	// CallerStatus is the membership status this host holds for the calling certificate:
	// ready, pending, failed, ejected, or empty when it names no member at all.
	CallerStatus string               `json:"caller_status,omitempty"`
	Transfer     *PoolTransferStatus  `json:"transfer,omitempty"`
	Notice       *PoolAuthorityNotice `json:"notice,omitempty"`
	// Restricted is this host's own refusal to act on pool authority: it is set while the
	// host cannot prove it holds the pool's current authority, and every start path,
	// placement admission and authority write on it is refused meanwhile.
	Restricted *PoolRestrictedStatus `json:"restricted,omitempty"`
	// VMs is this host's own view of the VM definitions it owns, answered only when the
	// caller asks for it (`?vms=1`).  It is the accepted-ownership evidence a promotion
	// candidate needs from a reachable owner, read from that host's own managed journal.
	VMs []PoolVMOwnership `json:"vms,omitempty"`
	// VMsReported separates "this host defines no VM" from "this host could not read its
	// own record": an empty list means the first only when this is true.
	VMsReported bool      `json:"vms_reported,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

// PoolVMOwnership is one host's own statement about one VM it defines.  Epoch is
// deliberately absent: the execution epoch is the master's number and a member never holds
// it, so a member could only repeat it back rather than confirm it.
type PoolVMOwnership struct {
	VMID     string `json:"vm_id"`
	HostID   string `json:"host_id"`
	Revision int    `json:"revision"`
	PoolID   string `json:"pool_id,omitempty"`
}

// PoolRestrictedStatus is a Core-enforced state, not a Controller flag: a host that cannot
// prove it holds the pool's current authority refuses every start path, placement admission
// and authority write until it has reconciled, whether or not a Controller is connected.
// Stop, power-off, local console and recovery access and reporting keep working.
type PoolRestrictedStatus struct {
	// Kind is the structural form of Reason, so no caller has to match on the sentence:
	// "unconfirmed" is a master that has not re-proved its authority since it restarted,
	// "superseded" is a host that found a higher authority elsewhere.
	Kind   string    `json:"kind,omitempty"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
	// Conflicts names the VMs this host still defines that the current pool master records
	// as running elsewhere.  A conflict is never resolved by guessing: the host stays
	// restricted and reports it.
	Conflicts  []string  `json:"conflicts,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// PoolPromotionCheck is one named check of the promotion proof.  Status is passed, blocked
// or unknown; unknown is an honest "this could not be established", which blocks a
// promotion exactly as a refusal does and is reported differently so an operator can tell
// a refusal from an outage.
type PoolPromotionCheck struct {
	Name   string   `json:"name"` // replica-integrity, replica-supremacy, accepted-ownership, old-master-exclusion
	Status string   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Hosts  []string `json:"hosts,omitempty"`
	VMs    []string `json:"vms,omitempty"`
}

// PoolPromotionHost is one probed host of the replica's membership, as the candidate found
// it.  An unreachable host is reported unreachable, never assumed stopped.
type PoolPromotionHost struct {
	HostID               string `json:"host_id"`
	Reachable            bool   `json:"reachable"`
	Role                 string `json:"role,omitempty"`
	AuthorityGeneration  uint64 `json:"authority_generation,omitempty"`
	ReplicaGeneration    uint64 `json:"replica_generation,omitempty"`
	MembershipGeneration uint64 `json:"membership_generation,omitempty"`
	// Blocks is this host's own answer to "does this host stop the promotion as things
	// stand": an unreachable member that could have been promoted itself, or a reachable one
	// that disagrees. A blocking host that can be fenced appears in the verdict's
	// FenceHostIDs; one that cannot is a refusal an operator must resolve another way.
	Blocks bool `json:"blocks"`
	// FenceRecorded, FenceUsable and FenceUnusableReason are the same structural answer the
	// verdict carries for the old master, per host, so a caller can show exactly which hosts
	// a promotion would power off and which of them it cannot.
	FenceRecorded       bool   `json:"fence_recorded"`
	FenceUsable         bool   `json:"fence_usable"`
	FenceUnusableReason string `json:"fence_unusable_reason,omitempty"`
	// FenceKind names the method this promotion would shut this host out with — the
	// recorded profile's kind — so the operator sees what is about to happen rather than
	// only that something will.
	FenceKind string `json:"fence_kind,omitempty"`
	// FenceCheckWarning is advisory and nothing else: what this candidate's own last
	// non-destructive check of that fence device found, when it was failing or stale. The
	// promotion's proof is unchanged and still decides; this exists so an operator is not
	// surprised by a fence that was already known to be unreachable.
	FenceCheckWarning string `json:"fence_check_warning,omitempty"`
	Error             string `json:"error,omitempty"`
}

// PoolPromotionVM is one VM record of the replica, classified: confirmed by its reachable
// owner, fenced with its owner, or blocked.  A blocked VM never blocks the pool's
// promotion; it stays visible and unstartable until it is reconciled.
type PoolPromotionVM struct {
	VMID   string `json:"vm_id"`
	HostID string `json:"host_id"`
	State  string `json:"state"` // confirmed, fenced or blocked
	Reason string `json:"reason,omitempty"`
}

// PoolPromotionVerdict is the candidate Core's whole structured answer, computed on Core
// from its own replica and its own live probes.  A preflight returns it without changing
// anything, so an operator sees exactly what blocks before confirming a destructive fence.
type PoolPromotionVerdict struct {
	PoolID              string `json:"pool_id"`
	CandidateHostID     string `json:"candidate_host_id"`
	OldMasterHostID     string `json:"old_master_host_id,omitempty"`
	OperationID         string `json:"operation_id,omitempty"`
	AuthorityGeneration uint64 `json:"authority_generation"`
	// CanPromote is true only when the whole proof holds right now, fence included. In the
	// normal ready state it is false, because the old master has not been fenced yet: that
	// is an honest "not proved", not a refusal.
	CanPromote bool `json:"can_promote"`
	// ReadyToFence is the state a caller offers the operator the promotion in: every check
	// but the exclusion has passed, the exclusion is unknown only because no fence has been
	// attempted, and this host holds both a usable fence profile and a fence credential it
	// can open for the old master. Confirming now proceeds to the fence rather than to a
	// refusal. It is deliberately separate from CanPromote, which never guesses.
	ReadyToFence  bool `json:"ready_to_fence"`
	FenceRequired bool `json:"fence_required"`
	// FenceRecorded, FenceUsable and FenceUnusableReason are the structural form of the
	// exclusion check's reason: whether the old master has a fence profile at all, whether
	// this host could turn it into a proof (profile kind and a credential it can open), and
	// Core's own short sentence when it could not.
	FenceRecorded       bool   `json:"fence_recorded"`
	FenceUsable         bool   `json:"fence_usable"`
	FenceUnusableReason string `json:"fence_unusable_reason,omitempty"`
	// FenceHostIDs is every host this promotion would power off, sorted: the old master, and
	// every other host that blocks the promotion and can be excluded by a fence.  It is what
	// the operator confirms and what the promotion command must carry back verbatim, so a
	// host is never powered off without having been named to the operator first.
	FenceHostIDs []string                   `json:"fence_host_ids,omitempty"`
	Checks       []PoolPromotionCheck       `json:"checks,omitempty"`
	Hosts        []PoolPromotionHost        `json:"hosts,omitempty"`
	VMs          []PoolPromotionVM          `json:"vms,omitempty"`
	Fence        *StorageClusterFenceStatus `json:"fence,omitempty"`
	Blocking     []string                   `json:"blocking,omitempty"`
	ObservedAt   time.Time                  `json:"observed_at"`
}

// PoolPromotionStatus is the candidate's durable promotion, for inventory.  Verdict is the
// last computed one; it is recomputed rather than stored, because a verdict is an
// observation with a lifetime, not a record.
type PoolPromotionStatus struct {
	OperationID string                `json:"operation_id,omitempty"`
	Stage       string                `json:"stage,omitempty"`
	Status      string                `json:"status,omitempty"` // pending, succeeded, failed or needs_attention
	Error       string                `json:"error,omitempty"`
	Verdict     *PoolPromotionVerdict `json:"verdict,omitempty"`
	// Cancellable is the same Core-computed answer PoolTransferStatus carries.  A promotion
	// fences the old master, so it is never true; it is reported rather than inferred so a
	// caller reads one field for every kind of transfer.
	Cancellable bool      `json:"cancellable"`
	ObservedAt  time.Time `json:"observed_at"`
}

// PoolPromotionFenceWindow is how long an observed power-off stays proof.  Beyond it the
// old master could have been powered on again by anything, so a retry re-fences.
const PoolPromotionFenceWindow = 15 * time.Minute

// PoolPromotionFenceProofFresh reports whether observedAt is inside the promotion fence
// proof window as measured from now.  Fresh when age <= PoolPromotionFenceWindow: a proof
// observed exactly at the window limit remains valid until the window elapses.
func PoolPromotionFenceProofFresh(observedAt, now time.Time) bool {
	if observedAt.IsZero() || observedAt.After(now.Add(time.Minute)) {
		return false
	}
	return now.Sub(observedAt) <= PoolPromotionFenceWindow
}

// PoolPromotionFenceDigest binds a fence proof to exactly one promotion: this pool, this
// excluded host, this operation and the authority generation being promoted.  A proof for
// another operation, another host or another generation therefore cannot be presented here,
// in the same way StorageClusterConfig.Digest binds a clustered-storage proof.
func PoolPromotionFenceDigest(poolID, oldMasterHostID, operationID string, generation uint64) string {
	sum := sha256.Sum256([]byte("nex pool promotion fence v1\x00" + poolID + "\x00" + oldMasterHostID + "\x00" + operationID + "\x00" + strconv.FormatUint(generation, 10)))
	return hex.EncodeToString(sum[:])
}

// PoolPromotionFenceVerified is the only way a stored proof becomes evidence.  It demands an
// observed exclusion, the exact binding above, the exact fence target of the recorded
// profile, and an observation inside the freshness window.  A fence request that returned
// success, a timeout, a missed heartbeat and an operator assurance all fail here, because
// none of them can produce this record.
func PoolPromotionFenceVerified(proof *StorageClusterFenceStatus, member PoolMember, poolID, oldMasterHostID, operationID string, generation uint64, now time.Time) error {
	if proof == nil {
		return errors.New("this promotion has no fence proof for the old pool master")
	}
	fence := member.Fence
	if proof.Method == PoolFenceMethodWatchdogProof {
		if err := poolWatchdogProofVerified(proof, member); err != nil {
			return err
		}
	} else if fence == nil || fence.Valid() != nil {
		return errors.New("the old pool master has no usable fence profile")
	}
	if proof.Status != "passed" || !proof.ExclusionVerified {
		reason := proof.Error
		if reason == "" {
			reason = "the fence did not independently observe the old pool master powered off"
		}
		return errors.New(reason)
	}
	if proof.HostID != oldMasterHostID || proof.Method != PoolFenceMethodWatchdogProof && proof.TargetID != fence.TargetID {
		return errors.New("this fence proof excludes another host")
	}
	if proof.RequestID != operationID || proof.ConfigGeneration != generation {
		return errors.New("this fence proof belongs to another promotion")
	}
	if proof.ConfigDigest != PoolPromotionFenceDigest(poolID, oldMasterHostID, operationID, generation) {
		return errors.New("this fence proof is not bound to this promotion")
	}
	if !PoolPromotionFenceProofFresh(proof.ObservedAt, now) {
		return errors.New("this fence proof is older than " + PoolPromotionFenceWindow.String() + "; fence the old pool master again")
	}
	return nil
}

// poolWatchdogProofVerified re-checks the three conditions of the rule change against the
// record the proof carries, at the moment the proof is used rather than only when it was made.
// A proof is a claim about what a host observed; this is what makes it evidence.
func poolWatchdogProofVerified(proof *StorageClusterFenceStatus, member PoolMember) error {
	// The generation is read off the member record rather than taken from a caller:
	// PoolMembership.Valid pins every member's Generation equal to the membership's, so
	// this cannot be handed the authority generation by mistake.  It was, and readiness and
	// the promotion path silently disagreed.
	generation := member.Generation
	w := proof.Watchdog
	if w == nil {
		return errors.New("this watchdog exclusion proof carries no evidence")
	}
	// (a) the target's own armed report, in the durable record, at this generation.
	armed := member.WatchdogArmed
	if armed == nil || !armed.Armed {
		return errors.New("pool host " + proof.HostID + " never reported that it armed a watchdog, so it cannot be shut out by one")
	}
	if !PoolWatchdogArmable(armed.Kind) || !PoolWatchdogArmable(w.Kind) || armed.Kind != w.Kind {
		return errors.New("pool host " + proof.HostID + " reports no watchdog that could reset it independently of its kernel")
	}
	if armed.FencingGeneration != generation || w.ArmedGeneration != generation {
		return errors.New("pool host " + proof.HostID + " armed its watchdog under an earlier record of this pool, so that report is not evidence about the pool as it is now")
	}
	if armed.TimeoutSeconds == 0 || w.TimeoutSeconds != armed.TimeoutSeconds {
		return errors.New("this watchdog exclusion proof was measured against a different timeout than " + proof.HostID + " reports")
	}
	// (c) the bound, which must be at least twice the target's own timeout, and must have
	// been held. (b), the quorate partition excluding the target, is what the proving host
	// measured; the partition it recorded must not name the target.
	bound := int(PoolWatchdogBound(armed.TimeoutSeconds) / time.Second)
	if w.BoundSeconds < bound || w.HeldSeconds < w.BoundSeconds {
		return errors.New("this pool has not been quorate without " + proof.HostID + " for the " +
			PoolWatchdogBound(armed.TimeoutSeconds).String() + " a watchdog exclusion needs")
	}
	if len(w.Partition) < 2 {
		return errors.New("a watchdog exclusion needs a majority partition, which needs three or more hosts")
	}
	for _, host := range w.Partition {
		if host == proof.HostID {
			return errors.New("this watchdog exclusion proof names " + proof.HostID + " in the partition it claims excludes it")
		}
	}
	return nil
}

// PoolTransferStatus is every participant's own report of the transfer it is part of.  The
// Controller assembles one honest picture from several hosts rather than keeping a job.
type PoolTransferStatus struct {
	OperationID         string `json:"operation_id"`
	Action              string `json:"action"`
	Role                string `json:"role"` // source, target or ejected
	TargetHostID        string `json:"target_host_id"`
	Stage               string `json:"stage"`
	Status              string `json:"status"` // pending, succeeded, failed or needs_attention
	Error               string `json:"error,omitempty"`
	AuthorityGeneration uint64 `json:"authority_generation,omitempty"`
	Frozen              bool   `json:"frozen,omitempty"`
	// Cancellable is Core's own answer to "may this still be cancelled?", so no caller has
	// to infer it from stage names.  It is true only before the transfer's first
	// irreversible durable step: the signed hand-off of a master change, the membership
	// written without the member of an eject, and never for a promotion, which fences.
	Cancellable bool `json:"cancellable"`
	// HandedOff is the source's own durable answer to "have I signed this pool's authority
	// away?".  It is reported, like Cancellable, so a caller never infers the irreversible
	// step from stage names or from a refusal sentence.  Only the outgoing master records
	// it, so it is set only on the source's own report; it is always present, so an older
	// Core that omits it is distinguishable from one that reports false.
	HandedOff  bool      `json:"handed_off"`
	ObservedAt time.Time `json:"observed_at"`
}

// PoolReplicaLimit bounds one replica body on the wire and one stored replica file.  A
// pool whose authority no longer fits is a refusal on the master, never a short replica: a
// truncated copy is exactly what the section digests below exist to make impossible.
const PoolReplicaLimit = 1 << 20

// PoolReplicaSections are the sections every replica declares, in this order.  A member
// recomputes each digest and refuses a replica that declares a different set, so a section
// cannot be dropped, reordered or replaced without invalidating the whole copy.
var PoolReplicaSections = []string{"membership", "vms", "storage", "networks", "media", "storage_clusters"}

type PoolReplicaSection struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// PoolWrappedKey is the pool CA private key sealed for exactly one member: ECDH between a
// fresh ephemeral P-256 key and that member's certified peer public key, HKDF-SHA256 bound
// to pool, member and generation, then AES-256-GCM with the same binding as its additional
// data.  A member stores it sealed; only a promotion it is told to perform opens it.
type PoolWrappedKey struct {
	EphemeralPublicKey string `json:"ephemeral_public_key"`
	Nonce              string `json:"nonce"`
	Ciphertext         string `json:"ciphertext"`
}

// PoolWrappedFenceCredential carries one host's fence credential material to one member,
// sealed the same way and for the same reason as the pool CA key above: the host that
// actually fences during a promotion is a surviving member, and it cannot be given the
// material after the master it must fence is gone.  A member stores it sealed inside its
// 0600 replica and opens it only when it is performing a promotion.  Version is the master's
// counter, so a member can tell a re-wrap of the same material from new material.
type PoolWrappedFenceCredential struct {
	HostID        string          `json:"host_id"`
	CredentialRef string          `json:"credential_ref"`
	Version       uint64          `json:"version,omitempty"`
	Wrapped       *PoolWrappedKey `json:"wrapped"`
}

// PoolAuthorityReplica is the pool master's signed, read-only copy of its own durable pool
// authority.  It is stored by every ready member and acted on by none of them: it is the
// recoverable record a promotion reads, never an independent authority and never health
// evidence.  The signature is made with the pool CA key, so a member verifies it against
// the pool CA it already holds long after the master that signed it is gone.
type PoolAuthorityReplica struct {
	PoolID       string    `json:"pool_id"`
	MasterHostID string    `json:"master_host_id"`
	Generation   uint64    `json:"generation"`
	MemberHostID string    `json:"member_host_id"`
	CreatedAt    time.Time `json:"created_at"`
	// VMs is the master's pool-vms.json owner projection kept as received bytes, so
	// ownership, epochs and pending updates survive replication unchanged.
	Membership      PoolMembership         `json:"membership"`
	VMs             json.RawMessage        `json:"vms"`
	Storage         []StorageRegistration  `json:"storage"`
	Networks        []NetworkRegistration  `json:"networks"`
	Media           []Media                `json:"media"`
	StorageClusters []StorageClusterConfig `json:"storage_clusters"`
	CAKeyWrapped    *PoolWrappedKey        `json:"ca_key_wrapped,omitempty"`
	// FenceCredentials are the pool's fence credential material, sealed for this member
	// alone.  Like CAKeyWrapped they are covered by the signature and excluded from
	// ContentDigest, so the digest stays the same for every member of one generation; what
	// makes new material advance the generation is the version in the membership above.
	FenceCredentials []PoolWrappedFenceCredential `json:"fence_credentials,omitempty"`
	Sections         []PoolReplicaSection         `json:"sections"`
	Signature        string                       `json:"signature,omitempty"`
}

// SigningBytes is everything but the signature: the per-member fields are deliberately
// inside it, so a replica wrapped for one member cannot be presented to another.
func (r PoolAuthorityReplica) SigningBytes() ([]byte, error) {
	return signingBytesClearSignature(r, func(v *PoolAuthorityReplica) { v.Signature = "" })
}

// ContentDigest identifies the replicated authority itself.  The per-member address, the
// per-push wrap, the creation time, the signature and the generation are excluded, so a
// master can number an authority by what changed in it, every member of one generation
// reports the same digest, and a re-push of the same generation is recognizable as
// identical rather than as a conflicting second copy.  The signature still covers all of
// them, so nothing excluded here can be altered in transit.
func (r PoolAuthorityReplica) ContentDigest() (string, error) {
	r.MemberHostID, r.CreatedAt, r.CAKeyWrapped, r.Signature, r.Generation = "", time.Time{}, nil, "", 0
	r.FenceCredentials = nil
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// SectionDigests recomputes the per-section digests from the replica's own fields.
func (r PoolAuthorityReplica) SectionDigests() ([]PoolReplicaSection, error) {
	values := []any{r.Membership, r.VMs, r.Storage, r.Networks, r.Media, r.StorageClusters}
	out := make([]PoolReplicaSection, 0, len(PoolReplicaSections))
	for i, name := range PoolReplicaSections {
		b, err := json.Marshal(values[i])
		if err != nil {
			return nil, err
		}
		out = append(out, PoolReplicaSection{Name: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(b))})
	}
	return out, nil
}

// VerifySections proves the stored sections are the ones the signature covers and that
// none of them is missing.  A replica that declares fewer sections is refused, not used.
func (r PoolAuthorityReplica) VerifySections() error {
	want, err := r.SectionDigests()
	if err != nil {
		return err
	}
	if len(r.Sections) != len(want) {
		return errors.New("pool authority replica does not declare every section")
	}
	for i, section := range r.Sections {
		if section.Name != want[i].Name || section.SHA256 != want[i].SHA256 {
			return errors.New("pool authority replica section digest does not match")
		}
	}
	return nil
}

func (r PoolAuthorityReplica) Valid() error {
	if r.PoolID == "" || len(r.PoolID) > 128 || r.MasterHostID == "" || len(r.MasterHostID) > 128 || r.MemberHostID == "" || len(r.MemberHostID) > 128 {
		return errors.New("pool authority replica requires pool, master and member identities")
	}
	if r.Generation == 0 {
		return errors.New("pool authority replica requires a generation")
	}
	if err := r.Membership.Valid(); err != nil {
		return err
	}
	if r.Membership.PoolID != r.PoolID || r.Membership.MasterHostID != r.MasterHostID {
		return errors.New("pool authority replica membership names another pool or master")
	}
	if len(r.VMs) == 0 || !json.Valid(r.VMs) {
		return errors.New("pool authority replica VM ownership record is invalid")
	}
	return r.VerifySections()
}

// PoolAuthorityRecord is the pool master's durable authority number and the digest of the
// content it numbered.  It is reported by the master alone; a Controller mirrors it and
// compares each member's acknowledged replica generation against it, and stores neither.
type PoolAuthorityRecord struct {
	Generation uint64    `json:"generation"`
	Digest     string    `json:"digest,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// PoolReplicaAck is the member's answer to one push.  Re-pushing the same generation
// returns the same acknowledgement; a refusal carries a bounded reason, never a drop.
type PoolReplicaAck struct {
	HostID     string `json:"host_id"`
	Generation uint64 `json:"generation"`
	Digest     string `json:"digest,omitempty"`
	Verified   bool   `json:"verified"`
	Error      string `json:"error,omitempty"`
}

// PoolReplicaStatus is observed state on both sides: what a host holds and how fresh that
// observation is.  The Controller mirrors it for display and stores none of it.
type PoolReplicaStatus struct {
	Generation   uint64    `json:"generation"`
	Digest       string    `json:"digest,omitempty"`
	MasterHostID string    `json:"master_host_id,omitempty"`
	SignedAt     time.Time `json:"signed_at"`
	ReceivedAt   time.Time `json:"received_at"`
	Verified     bool      `json:"verified"`
	Error        string    `json:"error,omitempty"`
}

// PoolCommand is sent to the pool master, except for the join action, which is sent to the
// joining host.  The Controller is a transport for this request and never persists
// membership intent as authoritative state.
type PoolCommand struct {
	RequestID       string         `json:"request_id"`
	Action          string         `json:"action"`
	Membership      PoolMembership `json:"membership"`
	MemberEndpoint  string         `json:"member_endpoint,omitempty"`
	RepositoryID    string         `json:"repository_id,omitempty"`
	ExpectedVersion uint64         `json:"expected_version,omitempty"`
	// MemberHostID names the joining host; for action "join" it is the command's target.
	// For action "maintenance" it names the host being drained, which is not the target:
	// that command goes to the master, addressed by Membership.MasterHostID.
	MemberHostID string `json:"member_host_id,omitempty"`
	// Enabled is the desired maintenance state for MemberHostID, and for action
	// "host-fencing" the desired Host fencing state for the whole pool.  It is a desired
	// state, not a transition, so re-sending it is a success and never a conflict.
	Enabled bool `json:"enabled,omitempty"`
	// MasterURL, PoolCAPEM and Invitation are the master-issued join credential the
	// joining Core redeems by itself.  The invitation is single-use and short-lived; the
	// Controller relays it and never stores, logs or audits it.  PeerOrigin is the bare
	// HTTPS origin this host will advertise to its new master.
	MasterURL  string `json:"master_url,omitempty"`
	PoolCAPEM  string `json:"pool_ca_pem,omitempty"`
	Invitation string `json:"invitation,omitempty"`
	PeerOrigin string `json:"peer_origin,omitempty"`
	// Fence is the power-fence profile recorded for MemberHostID by action
	// "fence-profile"; a nil profile clears it.  Credentials never travel in it.
	Fence *StorageClusterFence `json:"fence,omitempty"`
	// FenceConfirmed is the operator's separate, explicit acknowledgement that the old
	// pool master will be powered off.  It is a precondition for action "promote" and
	// nothing more: Core still produces and verifies the fence proof itself, and an
	// acknowledgement is never evidence of anything.
	FenceConfirmed bool `json:"fence_confirmed,omitempty"`
	// FenceHostIDs is the exact set of hosts the operator agreed to power off, as Core's own
	// preflight named them. Core re-computes that set when the promotion runs and refuses if
	// it differs, so an operator can never power off a host they were not shown.
	FenceHostIDs []string `json:"fence_host_ids,omitempty"`
}

func (c PoolCommand) Valid() error {
	if c.RequestID == "" || c.Action != "member-join" && c.Action != "storage-reconcile" && c.Action != "join" && c.Action != "maintenance" && c.Action != "master-change" && c.Action != "eject" && c.Action != "transfer-cancel" && c.Action != "promote" && c.Action != "fence-profile" && c.Action != "bmc-provisioning" && c.Action != "host-fencing" {
		return errors.New("invalid pool command action")
	}
	// Host fencing is one pool-wide setting, so unlike every other action here it names no
	// member: only the master, which keeps it.  Enabled is a desired state, so re-sending
	// it is a success and never a conflict.
	if c.Action == "host-fencing" {
		return requireNonEmptyBoundedID(c.Membership.MasterHostID, "a host fencing setting requires the pool master")
	}
	// Whether a host may configure its own management controller is a master-owned
	// membership field, so this one routes to the master and names the host it is about.
	// It says one thing — allow it again — and re-sending it is a success.
	if c.Action == "bmc-provisioning" {
		return requirePoolCommandTargets(c.Membership.MasterHostID, c.MemberHostID, "a management controller request requires the pool master", "a management controller request requires a member host")
	}
	// A promotion is the one transfer action addressed to a host that is not the master:
	// the old master is gone, so the operator's chosen candidate performs it.  No
	// membership is on the wire, because the candidate reads the replica it already holds.
	if c.Action == "promote" {
		return requireNonEmptyBoundedID(c.MemberHostID, "pool promotion requires the candidate host")
	}
	// A fence profile is a master-owned membership field, so the command routes to the
	// master and names the host the profile belongs to.
	if c.Action == "fence-profile" {
		if err := requirePoolCommandTargets(c.Membership.MasterHostID, c.MemberHostID, "a pool fence profile requires the pool master", "a pool fence profile requires a member host"); err != nil {
			return err
		}
		if c.Fence != nil {
			return c.Fence.Valid()
		}
		return nil
	}
	// A master change, an eject and a cancel carry no membership for the same reason
	// maintenance does not: the master reads its own record, and a membership composed in
	// the Controller could already be stale.  Only the routing target and the subject host
	// are on the wire; ExpectedVersion, when set, is checked against the master's own
	// membership generation.
	if c.Action == "master-change" || c.Action == "eject" || c.Action == "transfer-cancel" {
		return requirePoolCommandTargets(c.Membership.MasterHostID, c.MemberHostID, "pool authority transfer requires the pool master", "pool authority transfer requires a target host")
	}
	// Maintenance carries no membership either: the master reads its own, and composing one
	// in the Controller would make a drain depend on a mirror that may already be stale.
	// Only the routing target and the drained host are on the wire.
	if c.Action == "maintenance" {
		return requirePoolCommandTargets(c.Membership.MasterHostID, c.MemberHostID, "pool maintenance requires the pool master", "pool maintenance requires a member host")
	}
	// A join carries the master's invitation instead of a membership record: the target is
	// not a member yet, and the Controller never composes a membership for it.
	if c.Action == "join" {
		if c.MemberHostID == "" || len(c.MemberHostID) > 128 || c.MasterURL == "" || len(c.MasterURL) > 512 {
			return errors.New("pool join requires a member host and master URL")
		}
		if c.PoolCAPEM == "" || len(c.PoolCAPEM) > 16384 || c.Invitation == "" || len(c.Invitation) > 8192 {
			return errors.New("pool join requires a pool CA and invitation")
		}
		if len(c.PeerOrigin) > 512 {
			return errors.New("pool join peer origin is too long")
		}
		return nil
	}
	if err := c.Membership.Valid(); err != nil {
		return err
	}
	if c.ExpectedVersion != 0 && c.ExpectedVersion != c.Membership.Generation {
		return errors.New("pool command membership generation changed")
	}
	if c.Action == "storage-reconcile" && c.RepositoryID == "" {
		return errors.New("pool storage reconciliation requires a repository")
	}
	return nil
}

// PoolHostBMC is one host's own report about its own management controller: what Nex Core
// found over the local IPMI interface and what it was able to do there. It is a Core
// observation kept in the master's membership record, so a fresh Controller recovers it with
// the rest of the membership and never derives or remembers it itself.
type PoolHostBMC struct {
	// State is the closed set the UI switches on; Detail is Core's own sentence for it,
	// shown verbatim.  Every state but "provisioned" means this host has no automatically
	// provisioned fence device, and the host row offers Set BMC access… instead.
	State   string `json:"state"`
	Detail  string `json:"detail,omitempty"`
	Address string `json:"address,omitempty"` // The controller's own LAN address, as it reports it.
	Channel uint8  `json:"channel,omitempty"`
	Account string `json:"account,omitempty"` // Not a secret: the password never leaves Core.
	// ObservedAt is when this finding last *changed*, not when it was last confirmed: an
	// unchanged finding is deliberately not re-reported, because the membership is signed
	// and replicated and a moving timestamp would re-sign and re-push the pool's whole
	// authority every reconcile pass.  It therefore says nothing about whether the host is
	// still reporting — liveness comes from that host's own connection, and the freshness
	// of a fence device comes from PoolFenceCheck below, which the master timestamps on
	// its own clock every time it is confirmed.
	ObservedAt time.Time `json:"observed_at"`
}

// PoolHostWatchdog is one host's own report of the watchdog device it has.  A watchdog is how
// a host shuts *itself* out when it loses the pool's quorum, so unlike a management controller
// it is read on the host it is about — from sysfs only.  Nex Core never opens /dev/watchdog to
// find out: on most drivers opening the device starts the timer, and a host that has not been
// told to arm one must not be reset by having been inspected.
type PoolHostWatchdog struct {
	// Kind is the closed classification the pool acts on.  Only "hardware" can ever count:
	// the kernel's software watchdog is a timer inside the kernel that is supposed to
	// reset the machine, and a hung kernel does not run it.  A driver Nex Core does not
	// recognise is "unknown" and counts as nothing until it is classified.
	Kind string `json:"kind"`
	// Device is the sysfs name, watchdog0 and so on — not a path under /dev, because
	// nothing here opens one.  Driver is the kernel module behind it, reported so an
	// operator can see why a device was classified as it was.
	Device string `json:"device,omitempty"`
	Driver string `json:"driver,omitempty"`
	// TimeoutSeconds is the device's configured timeout, which is the bound a watchdog
	// fencing proof is measured against.  Zero means the kernel did not say.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// NoWayOut is the kernel's own setting that closing the device does not stop the
	// timer, and InUse that something already holds the device open.  Both are read from
	// sysfs; a device already in use is reported, not taken.  NoWayOut decides whether
	// Nex Core can disarm this host without rebooting it.
	NoWayOut bool `json:"nowayout,omitempty"`
	InUse    bool `json:"in_use,omitempty"`
}

// The watchdog classifications.  Closed: the Controller and the UI switch on them.
const (
	PoolWatchdogHardware = "hardware"
	PoolWatchdogSoftware = "software"
	PoolWatchdogUnknown  = "unknown"
	PoolWatchdogNone     = "none"
	// PoolWatchdogEmulated is a device a hypervisor presents to a host that is itself a
	// virtual machine.  It really does reset that machine, so Nex Core arms it and a
	// nested lab can exercise the whole path; it is its own kind because a reset a
	// hypervisor performs is not evidence about physical hardware, and every proof, row
	// and guide sentence that rests on one says so.  A physical host never reports it.
	PoolWatchdogEmulated = "emulated"
)

// PoolWatchdogArmable is whether a reported device is one Nex Core will feed.  Only a device
// that resets the machine independently of the kernel counts, which is hardware and the
// emulated device a hypervisor gives a virtual host.
func PoolWatchdogArmable(kind string) bool {
	return kind == PoolWatchdogHardware || kind == PoolWatchdogEmulated
}

// Valid bounds what a host may report about its own watchdog.
func (w PoolHostWatchdog) Valid() error {
	switch w.Kind {
	case PoolWatchdogHardware, PoolWatchdogEmulated, PoolWatchdogSoftware, PoolWatchdogUnknown, PoolWatchdogNone:
	default:
		return errors.New("unsupported watchdog kind")
	}
	if len(w.Device) > 64 || len(w.Driver) > 128 ||
		strings.ContainsAny(w.Device+w.Driver, "\x00\r\n") ||
		w.TimeoutSeconds < 0 || w.TimeoutSeconds > 86400 {
		return errors.New("invalid watchdog report")
	}
	return nil
}

// PoolWatchdogArmed is a host's own report that its watchdog is armed: something outside the
// kernel is now being fed and will reset this machine if it stops.  It is deliberately a
// separate record from the capability above, with different rules.
//
// A capability changes when hardware changes.  This changes when the pool's decision changes,
// and a proof rests on it, so it carries its own freshness — the master's clock when the
// report arrived — and the pool's fencing generation at the moment the host armed.  A report
// from an earlier generation is not evidence about the pool as it is now.
type PoolWatchdogArmed struct {
	// Armed is the host's answer.  A host that is not armed reports why in Detail, and a
	// host that could not be disarmed cleanly says so there too.
	Armed bool `json:"armed"`
	// Kind is the device class this host armed: hardware, or emulated where the host is
	// itself a virtual machine.  It is repeated here rather than read from the capability
	// because a proof records what was armed, not what the host has.
	Kind           string `json:"kind,omitempty"`
	Device         string `json:"device,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	// FencingGeneration is the pool *membership* generation this host armed at — the
	// counter that moves when the set of hosts that makes a majority changes, which is
	// exactly what invalidates an armed report.  It is deliberately not the authority
	// generation, which moves on every content change and would expire a perfectly good
	// report every few seconds.  A proof is refused unless this equals the generation of
	// the member record it is read from, so a host that armed under an older record of
	// this pool cannot be shut out on the strength of it.
	FencingGeneration uint64 `json:"fencing_generation,omitempty"`
	Detail            string `json:"detail,omitempty"`
	// Feeding is whether this host's watchdog device is still being fed, which is not the
	// same question as Armed and is why it is a field of its own.  Armed is what the pool
	// may rest a proof on, and it goes false the moment the pool decides this host should
	// stop; the device itself is only released when that host's cluster next stops, which
	// can be a pass or two later.  Every other host needs to know the difference: a host
	// that leaves the pool's cluster while a peer is still feeding can take that peer below
	// quorum, and the peer resets with nothing wrong.
	Feeding bool `json:"feeding,omitempty"`
	// ArmedAt is the host's own clock when it *started* feeding this device, carried over
	// unchanged while it goes on feeding the same one.  Nothing compares it with another
	// host's clock.  It exists so that a host which stops and starts its watchdog — a
	// reboot, a cluster restart — produces a record that is *different* from the one
	// before, because an identical record is never written, never replicated, and would
	// leave a survivor unable to tell "still armed since before" from "armed again since".
	ArmedAt time.Time `json:"armed_at"`
	// ObservedAt is when this finding last *changed*, in the manner of PoolHostBMC — not
	// when it was last confirmed.  Nothing decides anything from it: a proof's staleness
	// rule is the fencing generation above, which is durable, replicated and therefore
	// still readable by a candidate whose master is gone.  A wall clock could not be.
	ObservedAt time.Time `json:"observed_at"`
}

// Valid bounds what a host may report about its own arming.
func (a PoolWatchdogArmed) Valid() error {
	if a.Armed && !PoolWatchdogArmable(a.Kind) {
		return errors.New("only a hardware or emulated watchdog can be armed")
	}
	if len(a.Device) > 64 || len(a.Detail) > 200 || strings.ContainsAny(a.Device+a.Detail, "\x00\r\n") ||
		a.TimeoutSeconds < 0 || a.TimeoutSeconds > 86400 || a.Armed && a.TimeoutSeconds == 0 {
		return errors.New("invalid watchdog arming report")
	}
	// ArmedAt is a host's own clock and nothing compares it with another host's, but it is
	// still an untrusted field that the master records and replicates, so it is bounded like
	// every field beside it: a host cannot claim to have armed in the future.
	if a.ArmedAt.After(time.Now().Add(time.Hour)) {
		return errors.New("invalid watchdog arming report")
	}
	return nil
}

// PoolWatchdogBound is how long the quorate side must have held a partition without the target
// before that target counts as reset. It is at least twice the target's own watchdog timeout,
// which is the rule change's third condition, and never less than a minute.
//
// The floor is the number that matters and it comes from a measurement, not from arithmetic on
// the watchdog timeout. In a nested lab a partitioned host left the quorate partition 8.6 s
// after its cluster link went down and had reset and rebooted by 15.7 s; twice a 5 s watchdog
// timeout is 10 s, which is *shorter* than that, so the doubling alone would promise a reset
// that had not happened. A minute is roughly four times the measured worst case, which leaves
// room for a loaded physical host that Corosync takes longer to declare lost — and it is still
// only nested-lab evidence, so the margin is deliberately generous.
func PoolWatchdogBound(timeoutSeconds int) time.Duration {
	bound := 2 * time.Duration(timeoutSeconds) * time.Second
	if bound < time.Minute {
		return time.Minute
	}
	return bound
}

// PoolHostFencingCapability gates the pool's Host fencing setting: the command that changes
// it, the watchdog each host reports about itself and the prerequisite checklist the master
// reports with its membership.  A Controller talking to a master without it offers no switch,
// because the setting that means anything is the master's.
const PoolHostFencingCapability = "pool-host-fencing-v1"

// PoolFenceCheck is one host's non-destructive read of another host's fence device: the
// management controller's own report of which machine it manages and whether that machine is
// powered on, over the recorded profile and the replicated credential.  Nothing on this path
// can power anything off.
//
// It is an observation, so it never enters the durable membership and never moves the pool's
// authority generation.  The master keeps the checks it has been told about in memory and
// stamps each one with its own clock when it arrives, which is why freshness here does not
// depend on two hosts agreeing about the time.
type PoolFenceCheck struct {
	// HostID is the other host of the pair.  A member reports it as the host it checked;
	// the master reports it back as the host that did the checking, because the checks are
	// then listed on the row of the host they are about.
	HostID string `json:"host_id"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
	Power  string `json:"power,omitempty"` // on, off or empty when the controller did not say.
	// Profile binds this result to the fence device it was taken against, so a profile
	// that was replaced between a check and a reading of it cannot inherit that check's
	// success.  The master drops every result whose stamp is not the one its membership
	// records now, which reads as "not checked yet" until the next pass.
	Profile string `json:"profile,omitempty"`
	// ObservedAt is when the master last had this result confirmed, on the master's own
	// clock, and Stale says that is longer ago than the master is willing to vouch for.
	// A stale check is never readiness.
	ObservedAt time.Time `json:"observed_at"`
	Stale      bool      `json:"stale,omitempty"`
}

// The results of one non-destructive check.  The set is closed: the Controller and the UI
// switch on it, and Detail is Core's own sentence for it.
const (
	PoolFenceCheckOK          = "ok"
	PoolFenceCheckUnreachable = "unreachable"
	PoolFenceCheckRejected    = "credentials_rejected"
	PoolFenceCheckIdentity    = "identity_mismatch"
	PoolFenceCheckCertificate = "certificate_changed"
	PoolFenceCheckToolMissing = "tool_missing"
	PoolFenceCheckNoProfile   = "no_profile"
	PoolFenceCheckUnusable    = "unusable"
)

// PoolFenceCheckRequest is what a member sends its master after one checking pass: one
// result per host it looked at.  The checks a member sends are about other hosts, never
// about itself — a host does not get to say whether it can be shut out — and the master
// drops any entry naming the caller.
type PoolFenceCheckRequest struct {
	Checks []PoolFenceCheck `json:"checks"`
}

// PoolFenceCheckLimit bounds one report.  A pool is a handful of hosts; this is generous.
const PoolFenceCheckLimit = 64

// PoolFenceProfileStamp names the exact fence device a check was taken against: everything a
// check actually exercises, plus the identity it compares against.  It is not a secret and
// not a signature — it exists so a replaced profile immediately invalidates the results taken
// against the old one, rather than looking ready for another check interval.
func PoolFenceProfileStamp(member PoolMember) string {
	if member.Fence == nil {
		return ""
	}
	f := member.Fence
	// The credential is in the stamp because rotating only the password leaves every other
	// field identical: without it a check taken with the previous password would go on
	// reading as ready for a credential no host has used yet.
	sum := sha256.Sum256([]byte(strings.Join([]string{f.Kind, f.Endpoint, f.TargetID, f.Username,
		f.TLSFingerprint, strconv.Itoa(int(f.Cipher)), member.SystemUUID,
		f.CredentialRef, strconv.FormatUint(member.FenceCredentialVersion, 10)}, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func (r PoolFenceCheckRequest) Valid() error {
	if len(r.Checks) > PoolFenceCheckLimit {
		return errors.New("too many fence checks in one report")
	}
	for _, check := range r.Checks {
		if check.HostID == "" || len(check.HostID) > 128 {
			return errors.New("invalid fence check target")
		}
		switch check.Result {
		case PoolFenceCheckOK, PoolFenceCheckUnreachable, PoolFenceCheckRejected, PoolFenceCheckIdentity,
			PoolFenceCheckCertificate, PoolFenceCheckToolMissing, PoolFenceCheckNoProfile, PoolFenceCheckUnusable:
		default:
			return errors.New("unsupported fence check result")
		}
		if len(check.Detail) > 1024 || strings.ContainsAny(check.Detail, "\x00\r\n") ||
			(check.Power != "" && check.Power != "on" && check.Power != "off") {
			return errors.New("invalid fence check")
		}
		// An unstamped result is accepted and then dropped by the master, which keeps no
		// result whose stamp is not the profile it records now.  Refusing the request
		// instead would make one member running an older Core lose its whole pass — and
		// with it every other host's result in it — rather than only the field it omits.
		if check.Profile == "" {
			continue
		}
		if profile, err := hex.DecodeString(check.Profile); err != nil || len(profile) != 8 {
			return errors.New("invalid fence check profile")
		}
	}
	return nil
}

// PoolFenceReadiness is the master's per-host answer to "could this host be shut out right
// now", one entry per fencing method.  Today there is one method, the recorded fence device;
// watchdog self-fencing becomes a second entry beside it without changing this shape.
type PoolFenceReadiness struct {
	Ready   bool              `json:"ready"`
	Methods []PoolFenceMethod `json:"methods"`
}

// PoolFenceSummary is the pool's own fencing answer: the master's count of how many of this
// pool's ready hosts another host could shut out, which ones it could not, the pool's Host
// fencing setting and Core's prerequisite checklist for arming watchdogs.  A client displays
// it; it does not add up rows of its own and does not decide what the prerequisites are.
type PoolFenceSummary struct {
	Ready   int      `json:"ready"`
	Total   int      `json:"total"`
	Unready []string `json:"unready,omitempty"`
	// HostFencing is the pool's durable setting, reported so a client shows the switch in
	// the state the master records rather than the state it last sent.
	HostFencing bool `json:"host_fencing"`
	// Prerequisites is what must hold before Nex Core arms anything, computed by the
	// master.  The names are a closed set and the sentences are Core's own.
	Prerequisites []PoolFencePrerequisite `json:"prerequisites,omitempty"`
	// Cluster is what the pool's own Corosync/Pacemaker cluster is doing: whether Nex Core
	// forms one for this pool at all, and how far through arming it is.  Like the counts
	// above it is the master's memory-only observation over the membership it owns, never
	// durable: the durable facts are the membership and each host's own armed report, and
	// this is derived from them on every report.
	Cluster *PoolFenceCluster `json:"cluster,omitempty"`
}

// PoolFenceCluster is the pool's own watchdog cluster, as the master sees it.  Nex Core forms
// one Corosync/Pacemaker cluster per pool, with no resources on it, purely so that diskless sbd
// has a quorum to lose; clustered storage owns that cluster instead where a pool has one, and
// such a pool is not armed at all in this release.
type PoolFenceCluster struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	// Armed and Total are how many of this pool's ready hosts are feeding a watchdog, and
	// how many should be.  ArmingHostID is the host whose turn it is: arming means a
	// controlled cluster restart, and exactly one host at a time may take one.
	Armed        int    `json:"armed"`
	Total        int    `json:"total"`
	ArmingHostID string `json:"arming_host_id,omitempty"`
}

// The states a pool's watchdog cluster can be in.  Closed: the Controller and the UI switch on
// them.  "off", "not_available" and "degraded" are all "nothing is armed", kept apart because
// an operator acts on them differently: off is their own setting, not_available is this pool
// (clustered storage, too few hosts, too many hosts, no watchdog), degraded is a pool that was
// armed and is not now.
const (
	PoolClusterOff          = "off"
	PoolClusterNotAvailable = "not_available"
	PoolClusterForming      = "forming"
	PoolClusterArming       = "arming"
	PoolClusterArmed        = "armed"
	PoolClusterDegraded     = "degraded"
)

// PoolWatchdogClusterCapability gates the pool-owned Corosync/Pacemaker cluster: Core forming
// it, the arming rollout across its hosts and the cluster state reported above.  A master
// without it forms no cluster, so a Controller shows the setting without the cluster line
// rather than inventing one.
const PoolWatchdogClusterCapability = "pool-watchdog-cluster-v1"

// PoolWatchdogClusterHosts is the largest pool Nex Core forms a watchdog cluster for.  It is
// not a votequorum limit — votequorum scales far past this — but the largest cluster this code
// has been exercised on.  A pool above it is reported and left unarmed; no host is ever
// silently left out of a cluster the rest of the pool makes quorum decisions with.
const PoolWatchdogClusterHosts = 5

// PoolFencePrerequisite is one line of that checklist.
type PoolFencePrerequisite struct {
	Name   string `json:"name"` // hosts, watchdogs, bmc or quorum.
	State  string `json:"state"`
	Detail string `json:"detail"`
	// Hosts is which hosts the sentence above is about, as host IDs, when it is about
	// particular ones.  Core ends Detail before naming them and puts them here, because a
	// host ID is an opaque durable identifier and every other line on the page shows the
	// host's display name; only a client knows the names, so only a client can write them.
	Hosts []string `json:"hosts,omitempty"`
}

// The names of the prerequisite lines, and the states one can be in.  "unknown" is Nex Core
// saying it cannot observe this yet, and is never read as met.
const (
	PoolFencePrerequisiteHosts     = "hosts"
	PoolFencePrerequisiteWatchdogs = "watchdogs"
	PoolFencePrerequisiteBMC       = "bmc"
	PoolFencePrerequisiteQuorum    = "quorum"

	PoolFencePrerequisiteMet     = "met"
	PoolFencePrerequisiteUnmet   = "unmet"
	PoolFencePrerequisiteUnknown = "unknown"
)

// PoolFenceMethod is one way this host could be shut out, and whether it would work.  State
// is ready, not_ready or unknown; unknown is "Nex Core could not observe this", which stops a
// promotion exactly as not_ready does and is never read as fine.
type PoolFenceMethod struct {
	Kind   string `json:"kind"` // The fence profile's kind: ipmilan, redfish or virsh.
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
	// Checks is every other host's last non-destructive read of this method against this
	// host, so an operator sees which host could not reach it rather than only that
	// something could not.
	Checks []PoolFenceCheck `json:"checks,omitempty"`
}

// The states one fencing method can be in.
const (
	PoolFenceMethodReady    = "ready"
	PoolFenceMethodNotReady = "not_ready"
	PoolFenceMethodUnknown  = "unknown"
	// PoolFenceMethodNoPeer is its own state because it is not a wait: a pool with one
	// ready host has nobody to be checked from, ever, and telling an operator to wait for
	// a check that cannot happen would be untrue.  A single-host pool also has nothing to
	// promote over, so it needs no fencing at all.
	PoolFenceMethodNoPeer = "no_peer"
	// PoolFenceMethodNotApplicable is a method this pool cannot use at all, as a watchdog
	// cannot be used below three hosts: a two-host pool has no majority, so no host may
	// ever conclude from losing contact that it is the side that should keep running.  It
	// is not a wait either, and a pool that will never have a third host must not be shown
	// one more thing to fix.
	PoolFenceMethodNotApplicable = "not_applicable"
)

// PoolFenceWatchdog is the method kind a host's own watchdog is reported under, beside the
// fence profile's kind.
const PoolFenceWatchdog = "watchdog"

// Valid bounds what a member may advertise about itself. The state is closed, because the
// Controller and the UI act on it; the sentence is Core's own and is only bounded in length.
func (b PoolHostBMC) Valid() error {
	switch b.State {
	case PoolBMCProvisioned, PoolBMCNoInterface, PoolBMCUserRefused, PoolBMCLANDisabled,
		PoolBMCNameConflict, PoolBMCManualProfile, PoolBMCRemoved, PoolBMCPendingMaster:
	default:
		return errors.New("unsupported management controller state")
	}
	if len(b.Detail) > 1024 || strings.ContainsAny(b.Detail, "\x00\r\n") ||
		(b.Address != "" && !storageServer(b.Address)) || b.Channel > 15 ||
		(b.Account != "" && !storageOpaqueID.MatchString(b.Account)) {
		return errors.New("invalid management controller report")
	}
	return nil
}

// The states a host reports about its own management controller.
const (
	PoolBMCProvisioned   = "provisioned"
	PoolBMCNoInterface   = "no_local_interface"
	PoolBMCUserRefused   = "user_management_refused"
	PoolBMCLANDisabled   = "lan_disabled"
	PoolBMCNameConflict  = "name_conflict"
	PoolBMCManualProfile = "manual_profile"
	PoolBMCRemoved       = "removed"
	PoolBMCPendingMaster = "pending_master"
)

func (m PoolMembership) Valid() error {
	if m.PoolID == "" || m.MasterHostID == "" || m.Generation == 0 {
		return errors.New("pool membership requires pool, master and generation")
	}
	seen, master := map[string]bool{}, false
	for _, member := range m.Members {
		_, hexErr := hex.DecodeString(member.CertificateSHA256)
		if member.HostID == "" || seen[member.HostID] || len(member.CertificateSHA256) != 64 || hexErr != nil || member.Generation != m.Generation || (member.Status != "ready" && member.Status != "pending" && member.Status != "failed" && member.Status != "ejected") {
			return errors.New("invalid pool member record")
		}
		seen[member.HostID] = true
		master = master || member.HostID == m.MasterHostID && member.Status == "ready"
	}
	if !master {
		return errors.New("pool membership omits ready master")
	}
	return nil
}

// PoolRequest and PoolResult are a transient Controller/Core envelope carried by one
// report round trip, in the shape StorageClusterRequest/StorageClusterResult already use.
// The master owns the operation; the Controller keeps nothing after the exchange.
type PoolRequest struct {
	RequestID    string `json:"request_id"`
	PoolID       string `json:"pool_id"`
	HostID       string `json:"host_id"`
	Action       string `json:"action"` // invite or promotion-preflight
	MemberHostID string `json:"member_host_id,omitempty"`
	MasterURL    string `json:"master_url,omitempty"`
}

// PoolJoinInvitation is the master-issued, single-use credential a joining Core redeems
// against the master by itself.  It exists in Controller memory only for the remainder of
// the HTTP request that relays it: it is never stored, logged or audited.
type PoolJoinInvitation struct {
	MasterURL  string    `json:"master_url"`
	PoolCAPEM  string    `json:"pool_ca_pem"`
	Invitation string    `json:"invitation"`
	ExpiresAt  time.Time `json:"expires_at"`
}

type PoolResult struct {
	RequestID  string              `json:"request_id"`
	PoolID     string              `json:"pool_id"`
	HostID     string              `json:"host_id"`
	Status     string              `json:"status"` // succeeded or failed
	Error      string              `json:"error,omitempty"`
	ObservedAt time.Time           `json:"observed_at"`
	Invitation *PoolJoinInvitation `json:"invitation,omitempty"`
	// Promotion is the candidate's full structured verdict, answered to a preflight.  It
	// is computed on Core for that one round trip and stored by no one.
	Promotion *PoolPromotionVerdict `json:"promotion,omitempty"`
}

// PoolJoinStatus is the joining host's own report of its durable staged join.  Stage is the
// step it must still perform (precheck, csr, joined, rehomed, advertised) or "ready"; a host
// that is already a member but has not finished reports needs_attention, never success.
type PoolJoinStatus struct {
	OperationID string    `json:"operation_id"`
	Stage       string    `json:"stage"`
	Status      string    `json:"status"` // pending, succeeded, failed or needs_attention
	Error       string    `json:"error,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

// PoolStorageAccess is a member's observed access to a master-owned repository.  It is not
// a placement grant: Core checks current access again before every VM write or start.
type PoolStorageAccess struct {
	RepositoryID string    `json:"repository_id"`
	HostID       string    `json:"host_id"`
	Generation   uint64    `json:"generation"`
	Identity     string    `json:"identity"`
	Readable     bool      `json:"readable"`
	Writable     bool      `json:"writable"`
	QEMUWritable bool      `json:"qemu_writable"`
	Locking      bool      `json:"locking"`
	ObservedAt   time.Time `json:"observed_at"`
	Error        string    `json:"error,omitempty"`
}

// PoolNetworkAccess is a member's observed apply of a master-owned VLAN network. It is not
// a placement grant: Core checks the bridge again before attaching a guest NIC.
type PoolNetworkAccess struct {
	NetworkID     string    `json:"network_id"`
	HostID        string    `json:"host_id"`
	Generation    uint64    `json:"generation"`
	Bridge        string    `json:"bridge,omitempty"`
	UplinkPresent bool      `json:"uplink_present"`
	ObservedAt    time.Time `json:"observed_at"`
	Error         string    `json:"error,omitempty"`
}

// PoolMediaAccess is a member's verification that a master-owned ISO on shared storage is
// present with the recorded digest. Bytes ride the shared repository; this is metadata only.
type PoolMediaAccess struct {
	MediaID    string    `json:"media_id"`
	HostID     string    `json:"host_id"`
	StorageID  string    `json:"storage_id"`
	SHA256     string    `json:"sha256,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	Error      string    `json:"error,omitempty"`
}
