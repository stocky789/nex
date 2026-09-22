// SPDX-License-Identifier: AGPL-3.0-only
package agent

// A fence device that was recorded once and never tried again is a promise, not a
// protection: a controller loses its LAN address, an account is disabled, a motherboard is
// swapped and the pool learns about it at the worst possible moment. So every ready host
// reads every other host's fence device periodically and tells the master what it found.
//
// The rules that make this safe to run unattended on every host in a pool:
//
//   - Nothing here powers anything off. The whole check is vmhost.CheckPoolFenceTarget,
//     which asks the controller which machine it manages and reads the power state, and
//     there is no reachable call that changes it.
//   - A host never checks itself. Fence readiness is about another host being able to shut
//     this one out, so a host's own opinion of its own controller is not evidence.
//   - The results are observations. They live in the master's memory, are overlaid on the
//     membership the master reports, and never touch the durable membership record — so a
//     settled pool does not re-sign and re-push its whole authority because a check ran.
//   - A result the master has not had confirmed recently is stale, and a stale check is
//     never readiness. Freshness is the master's own clock at the moment a report arrives,
//     so it never depends on two hosts agreeing about the time.
//   - A master that restarts reports "not checked yet" until the next pass, honestly,
//     rather than keeping a durable store of something one interval re-establishes.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolFenceCheckPath = "/api/v1/pool/fence-checks"

// The cadence. Minutes, not seconds: a management controller's session table is small and
// this runs from every host against every other one. The jitter keeps a pool's hosts from
// arriving at the same controller together after a simultaneous restart.
var poolFenceCheckInterval = 5 * time.Minute

// poolFenceCheckStaleAfter is how long the master vouches for a result. Three intervals
// tolerates one missed pass and one slow one; beyond that the check is shown as stale.
func poolFenceCheckStaleAfter() time.Duration { return 3 * poolFenceCheckInterval }

// One target at a time, each bounded, and a bound on the whole pass: a host that has gone
// away must not consume the pass and leave the rest of the pool unchecked for ever.
var poolFenceCheckTargetTimeout = 20 * time.Second
var poolFenceCheckPassTimeout = 2 * time.Minute

// The one read this file performs, as a package variable so a lab can stand in for other
// hosts' management controllers the way labs already stand in for this host's own. It is
// read-only by construction: see vmhost.CheckPoolFenceTarget.
var checkPoolFenceTarget = vmhost.CheckPoolFenceTarget

var poolFenceCheckMu sync.Mutex

// poolFenceCheckDue is the next pass for this host, kept per state directory as the rest of
// this package's in-memory pool state is. poolFenceChecks is what a master has been told:
// the host that checked, the host it checked, and the result.
var poolFenceCheckDue = map[string]time.Time{}
var poolFenceChecks = map[string]map[string]map[string]protocol.PoolFenceCheck{}

// RunPoolFenceChecks is one checking pass over every other host in this pool. It returns
// without contacting anything until the interval has elapsed, so the caller can run it on
// the same reconcile cadence as everything else instead of owning a second timer.
func RunPoolFenceChecks(ctx context.Context, s *Store) error {
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil {
		return err
	}
	self, ok := poolMemberByID(state.Membership, s.State.HostID)
	if !ok || self.Status != "ready" {
		// A host that is not a ready member is not evidence about anybody.
		return nil
	}
	// The fence devices, identities and sealed credentials a member checks against are in
	// the signed copy of the pool's records it holds, which is the same place a promotion
	// reads them from. The master's own membership already is that record, and its own
	// credential store already holds the material.
	membership, replica := state.Membership, (*protocol.PoolAuthorityReplica)(nil)
	if membership.MasterHostID != s.State.HostID {
		stored, e := loadPoolReplica(s)
		if e != nil || stored == nil || !stored.Verified {
			return nil // Nothing verified to check against yet.
		}
		membership, replica = stored.Replica.Membership, &stored.Replica
	}
	if !poolFenceCheckDueNow(s.dir) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, poolFenceCheckPassTimeout)
	defer cancel()
	var checks []protocol.PoolFenceCheck
	for _, member := range membership.Members {
		if member.HostID == s.State.HostID || member.Status != "ready" {
			continue
		}
		if member.Fence == nil || member.Fence.Kind == protocol.FenceVirsh {
			// No device to read, or a lab device reached through somebody else's
			// libvirt. The master already knows both from the membership it owns.
			continue
		}
		if ctx.Err() != nil {
			// The pass ran out. What was not checked keeps its previous result and
			// goes stale if this keeps happening, which is the truth.
			break
		}
		target, done := context.WithTimeout(ctx, poolFenceCheckTargetTimeout)
		secret, secretErr := poolFenceCheckSecret(s, state, replica, member)
		result, detail, power := protocol.PoolFenceCheckUnusable, "", ""
		if secretErr != nil {
			detail = secretErr.Error()
		} else {
			result, detail, power = checkPoolFenceTarget(target, s.dir, member.Fence, member.SystemUUID, secret)
		}
		done()
		checks = append(checks, protocol.PoolFenceCheck{HostID: member.HostID, Result: result,
			Detail: detail, Power: power, Profile: protocol.PoolFenceProfileStamp(member)})
	}
	if len(checks) == 0 {
		return nil
	}
	// A host always keeps its own pass, master or not. On the master it is the pool's
	// record of it; on a member it is what the promotion verdict warns from when the
	// master is gone and there is nothing left to ask.
	recordPoolFenceChecks(s.dir, s.State.HostID, checks)
	if state.Membership.MasterHostID == s.State.HostID {
		return nil
	}
	return postPoolFenceChecks(ctx, s, checks)
}

// poolFenceCheckSecret opens the fence account's password for one target, for this pass only.
// On the pool master it is already in this host's own credential store. On a member it is not,
// and must not be: the material is sealed inside that member's own authority replica, and the
// alternative — writing every other host's fence password onto every member so a read-only
// check can run — would spread the secret across the pool for no gain. The unsealed value is
// the same fixed format the store holds, so it is parsed the same way and never touches disk.
func poolFenceCheckSecret(s *Store, state poolPeerState, replica *protocol.PoolAuthorityReplica, member protocol.PoolMember) (string, error) {
	if replica == nil {
		return vmhost.ReadFenceCredentialSecret(s.dir, member.Fence.CredentialRef)
	}
	sealed := poolFenceCredentialFor(*replica, member.HostID)
	if sealed == nil {
		return "", errors.New("this host holds no fence credential for that management controller")
	}
	files, err := unwrapPoolFenceCredential(state.KeyPEM, *replica, *sealed)
	if err != nil {
		return "", err
	}
	return vmhost.FenceCredentialSecretFromScript(files[""])
}

// poolFenceCheckWarning is one host's own last look at another host's fence device, when it
// was failing or stale. It is advisory: nothing decides anything from it.
func poolFenceCheckWarning(dir, checker, target string) string {
	poolFenceCheckMu.Lock()
	check, ok := poolFenceChecks[dir][checker][target]
	poolFenceCheckMu.Unlock()
	if !ok {
		return ""
	}
	if time.Since(check.ObservedAt) > poolFenceCheckStaleAfter() {
		return "this host's last check of that fence device is stale; it was last confirmed at " + check.ObservedAt.UTC().Format(time.RFC3339)
	}
	if check.Result != protocol.PoolFenceCheckOK {
		return "this host's last check of that fence device failed: " + check.Detail
	}
	return ""
}

// poolFenceCheckDueNow answers once per interval and books the next one at the same time, so
// two passes can never overlap on one host.
func poolFenceCheckDueNow(dir string) bool {
	poolFenceCheckMu.Lock()
	defer poolFenceCheckMu.Unlock()
	now := time.Now()
	if due, ok := poolFenceCheckDue[dir]; ok && now.Before(due) {
		return false
	}
	// Up to a fifth of the interval of jitter, so a pool's hosts spread out instead of
	// arriving at the same controllers together.
	poolFenceCheckDue[dir] = now.Add(poolFenceCheckInterval + rand.N(poolFenceCheckInterval/5+1))
	return true
}

// postPoolFenceChecks sends one pass's results to the master over the pool's own mTLS peer
// connection. A master too old to serve the route answers 404 and there is nothing to do
// about it: its membership carries no readiness either, and the Controller says so.
func postPoolFenceChecks(ctx context.Context, s *Store, checks []protocol.PoolFenceCheck) error {
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	b, _ := json.Marshal(protocol.PoolFenceCheckRequest{Checks: checks})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolFenceCheckPath, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("the pool master did not accept this host's fence checks")
	}
	return nil
}

// PoolFenceCheckHandler serves a member's report of what it found on the other hosts' fence
// devices. The peer certificate says which host is calling, and the report is recorded
// against that host: a member cannot report on somebody else's behalf, and a member's claim
// about its own row is dropped, because a host does not get to say whether it can be shut
// out.
func PoolFenceCheckHandler(s *Store) http.Handler {
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
		var v protocol.PoolFenceCheckRequest
		if json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&v) != nil || v.Valid() != nil {
			http.Error(w, "invalid fence check report", http.StatusBadRequest)
			return
		}
		poolPeerMu.Lock()
		state, e := loadPoolPeer(s)
		poolPeerMu.Unlock()
		if e != nil || state.Membership.Valid() != nil || state.Membership.MasterHostID != s.State.HostID {
			http.Error(w, "only the pool master keeps fence checks", http.StatusConflict)
			return
		}
		recordPoolFenceChecks(s.dir, member.HostID, v.Checks)
		w.WriteHeader(http.StatusNoContent)
	})
}

// recordPoolFenceChecks keeps what one host's pass reached, stamped with this master's own
// clock. A pass the pass timeout cut short carries only the targets it got to, so the results
// it did not refresh are kept and left to go stale rather than being forgotten — a target a
// truncated pass never reached has not become unchecked. Nothing durable is written: these
// are observations, and re-establishing them costs one interval.
func recordPoolFenceChecks(dir, checker string, checks []protocol.PoolFenceCheck) {
	now := time.Now()
	poolFenceCheckMu.Lock()
	defer poolFenceCheckMu.Unlock()
	if poolFenceChecks[dir] == nil {
		poolFenceChecks[dir] = map[string]map[string]protocol.PoolFenceCheck{}
	}
	pass := maps.Clone(poolFenceChecks[dir][checker])
	if pass == nil {
		pass = map[string]protocol.PoolFenceCheck{}
	}
	for _, check := range checks {
		// A host's claim about its own row is dropped here: it does not get to say
		// whether it can be shut out.
		if check.HostID == "" || check.HostID == checker {
			continue
		}
		target := check.HostID
		check.ObservedAt, check.Stale = now, false
		pass[target] = check
	}
	poolFenceChecks[dir][checker] = pass
}

// ClearPoolFenceChecksForTest drops what a master remembers, which is what a restart does.
func ClearPoolFenceChecksForTest(s *Store) {
	poolFenceCheckMu.Lock()
	defer poolFenceCheckMu.Unlock()
	delete(poolFenceChecks, s.dir)
	delete(poolFenceCheckDue, s.dir)
}

// PoolFenceCheckMembership overlays each host's fence readiness onto the membership copy the
// master reports. The durable membership record is never written with it, exactly as
// PoolReplicaMembership never writes an acknowledgement back.
func PoolFenceCheckMembership(s *Store, membership *protocol.PoolMembership) *protocol.PoolMembership {
	if membership == nil || membership.MasterHostID != s.State.HostID {
		return membership
	}
	// Only a ready member of this pool is evidence about another host. A host that has
	// been ejected since it reported is not, and its checks are dropped rather than aged.
	ready := map[string]bool{}
	for _, member := range membership.Members {
		ready[member.HostID] = member.Status == "ready"
	}
	// The fence device each result was taken against, as this master records it now. A
	// result stamped with anything else was taken against a profile that has since been
	// replaced, and is dropped rather than inherited by its replacement.
	stamp := map[string]string{}
	for _, member := range membership.Members {
		stamp[member.HostID] = protocol.PoolFenceProfileStamp(member)
	}
	now := time.Now()
	poolFenceCheckMu.Lock()
	byTarget := map[string][]protocol.PoolFenceCheck{}
	for checker, pass := range poolFenceChecks[s.dir] {
		if !ready[checker] {
			continue
		}
		for target, check := range pass {
			if check.Profile != stamp[target] {
				continue
			}
			check.HostID = checker
			check.Stale = now.Sub(check.ObservedAt) > poolFenceCheckStaleAfter()
			byTarget[target] = append(byTarget[target], check)
		}
	}
	poolFenceCheckMu.Unlock()
	// A host with nobody else to be checked from is not waiting for anything, and a pool
	// of one host has nothing to promote over either.
	peers := 0
	for _, member := range membership.Members {
		if member.Status == "ready" {
			peers++
		}
	}
	quorum := poolLastQuorum(s.dir)
	out := *membership
	out.Members = append([]protocol.PoolMember(nil), membership.Members...)
	summary := protocol.PoolFenceSummary{HostFencing: !membership.HostFencingDisabled}
	for i := range out.Members {
		readiness := poolFenceReadiness(s.dir, out.Members[i], byTarget[out.Members[i].HostID], peers, !membership.HostFencingDisabled, quorum)
		out.Members[i].FenceReadiness = &readiness
		// The count is the pool's own, and it counts the pool: a host that has been
		// ejected, or has not finished joining, is not one of this pool's hosts.
		if out.Members[i].Status != "ready" {
			continue
		}
		summary.Total++
		if readiness.Ready {
			summary.Ready++
		} else {
			summary.Unready = append(summary.Unready, out.Members[i].HostID)
		}
	}
	summary.Prerequisites = poolFencePrerequisites(out.Members, peers, quorum)
	summary.Cluster = PoolFenceClusterFor(s, *membership, quorum)
	out.FenceSummary = &summary
	return &out
}

// poolFencePrerequisites is what must hold before Nex Core arms any host's watchdog. It is
// computed here, on the master, from the membership it owns, and reported with it: an operator
// sees the same checklist whether or not they ever open the Host fencing setting, and a fresh
// Controller reads it from the master like everything else.
func poolFencePrerequisites(members []protocol.PoolMember, peers int, q vmhost.PoolQuorum) []protocol.PoolFencePrerequisite {
	hosts := protocol.PoolFencePrerequisite{Name: protocol.PoolFencePrerequisiteHosts,
		State:  protocol.PoolFencePrerequisiteMet,
		Detail: "this pool has " + strconv.Itoa(peers) + " hosts, so a partition can have a majority side"}
	if peers < 3 {
		hosts.State = protocol.PoolFencePrerequisiteUnmet
		hosts.Detail = "a watchdog needs three or more hosts, so that a partition has a majority side; this pool has " +
			strconv.Itoa(peers) + ". Below that, hosts are shut out through their management controllers instead"
	}
	watchdogs := protocol.PoolFencePrerequisite{Name: protocol.PoolFencePrerequisiteWatchdogs,
		State: protocol.PoolFencePrerequisiteMet, Detail: "every host reports a hardware watchdog"}
	bmc := protocol.PoolFencePrerequisite{Name: protocol.PoolFencePrerequisiteBMC,
		State: protocol.PoolFencePrerequisiteMet, Detail: "every host has a fence device another host has confirmed"}
	var without, unfenced []string
	for _, member := range members {
		if member.Status != "ready" {
			continue
		}
		if member.Watchdog == nil || member.Watchdog.Kind != protocol.PoolWatchdogHardware {
			without = append(without, member.HostID)
		}
		// Specifically the fence device, not "any method is ready": a watchdog must never
		// satisfy the prerequisite for arming watchdogs.
		if !poolFenceDeviceReady(member) {
			unfenced = append(unfenced, member.HostID)
		}
	}
	if len(without) > 0 {
		watchdogs.State = protocol.PoolFencePrerequisiteUnmet
		watchdogs.Detail = "these hosts report no hardware watchdog, so they could not shut themselves out"
		watchdogs.Hosts = without
	}
	if len(unfenced) > 0 {
		bmc.State = protocol.PoolFencePrerequisiteUnmet
		bmc.Detail = "these hosts have no confirmed fence device"
		bmc.Hosts = unfenced
	}
	return []protocol.PoolFencePrerequisite{hosts, watchdogs, bmc, poolQuorumPrerequisite(q, members)}
}

// poolQuorumPrerequisite is the pool's Corosync quorum as this master reads it from the engine
// that keeps it. A master that cannot read one says so — "unknown" is Nex Core saying it
// cannot observe this, and is never read as met.
func poolQuorumPrerequisite(q vmhost.PoolQuorum, members []protocol.PoolMember) protocol.PoolFencePrerequisite {
	line := protocol.PoolFencePrerequisite{Name: protocol.PoolFencePrerequisiteQuorum}
	if !q.Observed {
		line.State = protocol.PoolFencePrerequisiteUnknown
		line.Detail = "this host is not running a Corosync cluster for this pool, so Nex Core arms no watchdog and no host resets itself"
		return line
	}
	if !q.Quorate {
		line.State = protocol.PoolFencePrerequisiteUnmet
		line.Detail = "this pool's Corosync cluster is not quorate"
		return line
	}
	inPartition := map[string]bool{}
	for _, host := range q.Hosts {
		inPartition[host] = true
	}
	var outside []string
	for _, member := range members {
		if member.Status == "ready" && !inPartition[member.HostID] {
			outside = append(outside, member.HostID)
		}
	}
	if len(outside) > 0 {
		line.State = protocol.PoolFencePrerequisiteUnmet
		line.Detail = "these hosts are not in this pool's quorate Corosync partition"
		line.Hosts = outside
		return line
	}
	line.State = protocol.PoolFencePrerequisiteMet
	line.Detail = "this pool's Corosync cluster is quorate and every host is in it"
	return line
}

// poolFenceDeviceReady is whether another host could power this one off through its recorded
// fence device. It reads the fence-device method specifically: "any method is ready" would
// let an armed watchdog satisfy the prerequisite for arming watchdogs.
func poolFenceDeviceReady(member protocol.PoolMember) bool {
	if member.FenceReadiness == nil {
		return false
	}
	for _, method := range member.FenceReadiness.Methods {
		if method.Kind != protocol.PoolFenceWatchdog && method.State == protocol.PoolFenceMethodReady {
			return true
		}
	}
	return false
}

// poolFenceReadiness is the master's answer for one host: which methods could shut it out,
// and why each one could not. It reads the membership the master owns and the checks other
// hosts reported, and runs nothing.
func poolFenceReadiness(dir string, member protocol.PoolMember, checks []protocol.PoolFenceCheck, peers int, hostFencing bool, q vmhost.PoolQuorum) protocol.PoolFenceReadiness {
	methods := []protocol.PoolFenceMethod{poolFenceDeviceMethod(member, checks, peers > 1),
		poolWatchdogMethod(dir, member, peers, hostFencing, q)}
	ready := false
	for _, method := range methods {
		ready = ready || method.State == protocol.PoolFenceMethodReady
	}
	return protocol.PoolFenceReadiness{Ready: ready, Methods: methods}
}

// poolWatchdogMethod is whether this host could shut *itself* out: it has armed a device that
// resets the machine independently of its kernel, it said so itself at this pool's current
// record, and the pool has a quorum to lose. Anything less is reported with the reason, and
// every branch but the armed one stays not-ready for ever.
func poolWatchdogMethod(dir string, member protocol.PoolMember, peers int, hostFencing bool, q vmhost.PoolQuorum) protocol.PoolFenceMethod {
	method := protocol.PoolFenceMethod{Kind: protocol.PoolFenceWatchdog, State: protocol.PoolFenceMethodNotReady}
	armed := member.WatchdogArmed
	switch {
	case peers < 3:
		// Two hosts have no majority, so neither side of a partition may conclude that it
		// is the one that should keep running. Those pools are fenced through their
		// management controllers, and this is not something an operator can fix. A pool of
		// one host has nothing to promote over either, so it is not told about a fence
		// device it does not need.
		method.State = protocol.PoolFenceMethodNotApplicable
		method.Reason = "a watchdog needs three or more hosts to have a majority side"
		if peers > 1 {
			method.Reason += "; this pool is fenced through its management controllers"
		}
	case !hostFencing:
		method.Reason = "Host fencing is turned off for this pool, so Nex Core arms no watchdog"
	case member.Watchdog == nil:
		method.State = protocol.PoolFenceMethodUnknown
		method.Reason = "this host does not report whether it has a watchdog; upgrade it"
	case member.Watchdog.Kind == protocol.PoolWatchdogSoftware:
		method.Reason = "this host has only the kernel's software watchdog, which a hung kernel cannot run, so it does not count"
	case member.Watchdog.Kind == protocol.PoolWatchdogUnknown:
		method.Reason = "Nex Core does not recognise this host's watchdog driver " + member.Watchdog.Driver +
			", so it does not count as hardware"
	case !protocol.PoolWatchdogArmable(member.Watchdog.Kind):
		method.Reason = "this host reports no watchdog device"
	case armed == nil:
		method.State = protocol.PoolFenceMethodUnknown
		method.Reason = "this host has not said whether it armed its watchdog; upgrade it"
	case !armed.Armed:
		method.Reason = armed.Detail
		if method.Reason == "" {
			method.Reason = "this host's watchdog is not armed"
		}
	case armed.FencingGeneration != member.Generation:
		method.Reason = "this host armed its watchdog under an earlier record of this pool, so that report is not evidence about the pool as it is now"
	case PoolWatchdogRejoined(dir, member.HostID):
		// The same condition the proof applies. A row that reads ready while a promotion
		// would refuse is the disagreement slice 4b's review found once already.
		method.Reason = poolWatchdogRejoinedReason(member.HostID)
	case !q.Observed:
		method.State = protocol.PoolFenceMethodUnknown
		method.Reason = "this pool master cannot read this pool's Corosync quorum, so it cannot say whether losing it would reset this host"
	case !q.Quorate:
		method.Reason = "this pool's Corosync cluster is not quorate"
	case !slices.Contains(q.Hosts, member.HostID):
		// The host is armed and outside the quorate partition: it is resetting itself
		// right now, which is not the same as being ready to.
		method.Reason = "this host is not in this pool's quorate Corosync partition"
	default:
		method.State, method.Reason = protocol.PoolFenceMethodReady, ""
	}
	return method
}

// poolFenceDeviceMethod is whether another host could power this one off through the fence
// device the master records for it.
func poolFenceDeviceMethod(member protocol.PoolMember, checks []protocol.PoolFenceCheck, hasPeers bool) protocol.PoolFenceMethod {
	method := protocol.PoolFenceMethod{Kind: "bmc", State: protocol.PoolFenceMethodNotReady}
	switch {
	case member.Fence != nil:
		method.Kind = member.Fence.Kind
	case member.BMCProvisioningDisabled:
		// A deliberate withdrawal, not a gap to nag about.
		method.Reason = "an operator withdrew this host's fence device, so no host can shut it out"
		return method
	case member.SystemUUIDConflictHostID != "":
		method.Reason = "this host reports the same hardware identity as " + member.SystemUUIDConflictHostID +
			", so a management controller cannot be confirmed to be its own"
		return method
	case member.BMC != nil && member.BMC.State != protocol.PoolBMCProvisioned:
		method.Reason = member.BMC.Detail
		if method.Reason == "" {
			method.Reason = "Nex Core could not configure this host's management controller"
		}
		return method
	default:
		method.Reason = "no fence device is recorded for this host"
		return method
	}
	if err := vmhost.PoolFenceUsable(member.Fence, member.SystemUUID); err != nil {
		method.Reason = err.Error()
		return method
	}
	if member.Fence.Kind == protocol.FenceVirsh {
		// A disposable lab's device. It is usable, and Nex Core says plainly that it is
		// not the same evidence as a controller another host reads every few minutes.
		method.State = protocol.PoolFenceMethodReady
		method.Reason = "this is a lab virsh device, which Nex Core does not check continuously"
		return method
	}
	method.Checks = checks
	fresh := 0
	var failures []string
	for _, check := range checks {
		if check.Stale {
			failures = append(failures, "the last check from "+check.HostID+" is stale")
			continue
		}
		if check.Result == protocol.PoolFenceCheckOK {
			fresh++
			continue
		}
		failures = append(failures, check.Detail+" (from "+check.HostID+")")
	}
	switch {
	case fresh > 0:
		method.State, method.Reason = protocol.PoolFenceMethodReady, ""
	case len(checks) == 0 && !hasPeers:
		method.State = protocol.PoolFenceMethodNoPeer
		method.Reason = "this pool has no other host to check this host's management controller from, and no other host to promote in its place"
	case len(checks) == 0:
		method.State = protocol.PoolFenceMethodUnknown
		method.Reason = "no other host in this pool has checked this host's management controller yet"
	default:
		method.Reason = strings.Join(failures, "; ")
	}
	return method
}
