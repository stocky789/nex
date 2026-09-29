// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolBackupPoliciesFile = "pool-backup-policies.json"

var ErrBackupPolicyNotFound = errors.New("backup policy not found")

type poolBackupPoliciesState struct {
	Policies map[string]protocol.BackupPolicy `json:"policies"`
}

func poolBackupPoliciesPath(s *Store) string {
	return filepath.Join(poolBackupStoreDir(s), poolBackupPoliciesFile)
}

func loadPoolBackupPolicies(s *Store) (poolBackupPoliciesState, error) {
	state := poolBackupPoliciesState{Policies: map[string]protocol.BackupPolicy{}}
	b, err := os.ReadFile(poolBackupPoliciesPath(s))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return poolBackupPoliciesState{}, err
	}
	if state.Policies == nil {
		state.Policies = map[string]protocol.BackupPolicy{}
	}
	return state, nil
}

func savePoolBackupPolicies(s *Store, state poolBackupPoliciesState) error {
	if state.Policies == nil {
		state.Policies = map[string]protocol.BackupPolicy{}
	}
	if err := checkPoolBackupAuthorityCapacity(s, func(auth *protocol.PoolBackupAuthority) {
		auth.Definitions = auth.Definitions[:0]
		for _, policy := range state.Policies {
			auth.Definitions = append(auth.Definitions, policy)
		}
	}); err != nil {
		return err
	}
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(poolBackupPoliciesPath(s), b, 0600)
}

func masterBackupPool(s *Store, poolID string) error {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return ErrBackupUnavailable
	}
	if poolID != "" && membership.PoolID != poolID {
		return ErrBackupUnavailable
	}
	return nil
}

// UpsertPoolBackupPolicy creates or updates a durable policy/schedule on the master.
func UpsertPoolBackupPolicy(s *Store, policy protocol.BackupPolicy) (protocol.BackupPolicy, error) {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if policy.PoolID == "" {
		policy.PoolID = s.State.PoolID
	}
	if policy.Revision == 0 {
		policy.Revision = 1
	}
	if err := policy.Valid(); err != nil || policy.PoolID == "" {
		return protocol.BackupPolicy{}, ErrBackupInvalid
	}
	if err := masterBackupPool(s, policy.PoolID); err != nil {
		return protocol.BackupPolicy{}, err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return protocol.BackupPolicy{}, err
	}
	if _, err := GetPoolBackupRepository(s, policy.RepositoryID); err != nil {
		return protocol.BackupPolicy{}, err
	}
	membership, err := Membership(s)
	if err != nil {
		return protocol.BackupPolicy{}, ErrBackupUnavailable
	}
	for _, vmID := range policy.VMIDs {
		vm, err := lookupPoolVMSpec(s, vmID)
		if err != nil || vm.ID != vmID || vm.PoolID != policy.PoolID {
			return protocol.BackupPolicy{}, ErrBackupInvalid
		}
		member, ok := poolMemberByID(membership, vm.HostID)
		if !ok || member.Status != "ready" {
			return protocol.BackupPolicy{}, ErrBackupInvalid
		}
	}
	state, err := loadPoolBackupPolicies(s)
	if err != nil {
		return protocol.BackupPolicy{}, err
	}
	now := time.Now().UTC()
	if existing, ok := state.Policies[policy.ID]; ok {
		if policy.ExpectedRevision == nil || *policy.ExpectedRevision != existing.Revision {
			return protocol.BackupPolicy{}, ErrBackupConflict
		}
		if existing.ScheduleID != policy.ScheduleID {
			return protocol.BackupPolicy{}, ErrBackupConflict
		}
		policy.CreatedAt = existing.CreatedAt
		policy.Revision = existing.Revision + 1
		// Preserve next/last when only pause toggles and schedule fields are unchanged.
		if policy.NextDispatchAt.IsZero() {
			policy.NextDispatchAt = existing.NextDispatchAt
		}
		if policy.LastDispatchAt.IsZero() {
			policy.LastDispatchAt = existing.LastDispatchAt
		}
	} else {
		if policy.ExpectedRevision != nil {
			return protocol.BackupPolicy{}, ErrBackupConflict
		}
		if policy.Revision == 0 {
			policy.Revision = 1
		}
		policy.CreatedAt = now
	}
	policy.ExpectedRevision = nil
	if policy.Enabled && !policy.Paused {
		next, err := protocol.NextBackupOccurrence(policy.Rule, policy.TimeZone, now)
		if err != nil {
			return protocol.BackupPolicy{}, err
		}
		policy.NextDispatchAt = next
	}
	policy.UpdatedAt = now
	if err := policy.Valid(); err != nil {
		return protocol.BackupPolicy{}, ErrBackupInvalid
	}
	state.Policies[policy.ID] = policy
	if err := savePoolBackupPolicies(s, state); err != nil {
		return protocol.BackupPolicy{}, err
	}
	return policy, nil
}

// SetPoolBackupPolicyPaused toggles pause without rewriting coverage.
func SetPoolBackupPolicyPaused(s *Store, policyID string, paused bool) (protocol.BackupPolicy, error) {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	state, err := loadPoolBackupPolicies(s)
	if err != nil {
		return protocol.BackupPolicy{}, err
	}
	if err := masterBackupPool(s, ""); err != nil {
		return protocol.BackupPolicy{}, err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return protocol.BackupPolicy{}, err
	}
	policy, ok := state.Policies[policyID]
	if !ok {
		return protocol.BackupPolicy{}, ErrBackupPolicyNotFound
	}
	policy.Paused = paused
	policy.Revision++
	policy.UpdatedAt = time.Now().UTC()
	if policy.Enabled && !policy.Paused {
		next, err := protocol.NextBackupOccurrence(policy.Rule, policy.TimeZone, time.Now().UTC())
		if err != nil {
			return protocol.BackupPolicy{}, err
		}
		policy.NextDispatchAt = next
	}
	state.Policies[policyID] = policy
	if err := savePoolBackupPolicies(s, state); err != nil {
		return protocol.BackupPolicy{}, err
	}
	return policy, nil
}

// ListPoolBackupPolicies returns the master's durable policies.
func ListPoolBackupPolicies(s *Store) ([]protocol.BackupPolicy, error) {
	if err := masterBackupPool(s, ""); err != nil {
		return nil, err
	}
	state, err := loadPoolBackupPolicies(s)
	if err != nil {
		return nil, err
	}
	out := make([]protocol.BackupPolicy, 0, len(state.Policies))
	for _, p := range state.Policies {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetPoolBackupPolicy returns one policy by ID.
func GetPoolBackupPolicy(s *Store, id string) (protocol.BackupPolicy, error) {
	policies, err := ListPoolBackupPolicies(s)
	if err != nil {
		return protocol.BackupPolicy{}, err
	}
	for _, p := range policies {
		if p.ID == id {
			return p, nil
		}
	}
	return protocol.BackupPolicy{}, ErrBackupPolicyNotFound
}

// PoolBackupAuthorityRefs builds the durable backup authority projection for the replica.
// Must not call Membership: snapshot reservation holds poolPeerMu while sizing the
// authority replica via buildPoolAuthorityReplicaForMembership, which calls here.
// The caller already verified this host is the pool master. Full NFS payloads stay out;
// catalog entries are index-only.
func PoolBackupAuthorityRefs(s *Store) (*protocol.PoolBackupAuthority, error) {
	state, err := loadPoolBackupPolicies(s)
	if err != nil {
		return nil, err
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return nil, err
	}
	reposState, err := loadPoolBackupRepositories(s)
	if err != nil {
		return nil, err
	}
	catalog, err := loadPoolBackupCatalog(s)
	if err != nil {
		return nil, err
	}
	auth := &protocol.PoolBackupAuthority{}
	repos := make([]protocol.BackupRepository, 0, len(reposState.Repositories))
	for _, repo := range reposState.Repositories {
		repo.Access = protocol.StorageAccess{}
		repo.MemberAccess = nil
		repo.Health = protocol.BackupRepositoryHealth{}
		repos = append(repos, repo)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].ID < repos[j].ID })
	auth.Repositories = repos

	policies := make([]protocol.BackupPolicy, 0, len(state.Policies))
	for _, p := range state.Policies {
		policies = append(policies, p)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID < policies[j].ID })
	auth.Definitions = policies
	for _, p := range policies {
		auth.Policies = append(auth.Policies, protocol.BackupPolicyRef{
			ID: p.ID, ScheduleID: p.ScheduleID, Revision: p.Revision,
			Enabled: p.Enabled, Paused: p.Paused, Name: p.Name, UpdatedAt: p.UpdatedAt,
		})
	}

	// Transfer every retained record. The peer body limit refuses an oversized
	// transfer rather than silently losing history on the new master.
	var unresolved, terminal []protocol.BackupRun
	for _, r := range runs {
		if r.Status == "pending" || r.Status == "needs_attention" {
			unresolved = append(unresolved, r)
		} else {
			terminal = append(terminal, r)
		}
	}
	appendRun := func(r protocol.BackupRun) {
		auth.History = append(auth.History, r)
		auth.Runs = append(auth.Runs, protocol.BackupRunRef{
			OperationID: r.OperationID, OccurrenceID: r.OccurrenceID, Kind: r.Kind,
			Status: r.Status, PolicyID: r.PolicyID, VMID: r.VMID, UpdatedAt: r.UpdatedAt,
		})
	}
	for _, r := range unresolved {
		appendRun(r)
	}
	for _, r := range terminal {
		appendRun(r)
	}

	auth.Catalog = catalog
	pins, err := loadPoolBackupPins(s)
	if err != nil {
		return nil, err
	}
	for _, pin := range pins.Pins {
		auth.Pins = append(auth.Pins, pin)
	}
	sort.Slice(auth.Pins, func(i, j int) bool {
		if auth.Pins[i].RecoveryPointID != auth.Pins[j].RecoveryPointID {
			return auth.Pins[i].RecoveryPointID < auth.Pins[j].RecoveryPointID
		}
		return auth.Pins[i].OperationID < auth.Pins[j].OperationID
	})
	receipts, err := listPoolBackupMetadataReceipts(s)
	if err != nil {
		return nil, err
	}
	auth.MetadataReceipts = receipts
	sort.Slice(auth.MetadataReceipts, func(i, j int) bool {
		return auth.MetadataReceipts[i].OperationID < auth.MetadataReceipts[j].OperationID
	})

	if err := auth.Valid(); err != nil {
		return nil, err
	}
	if len(auth.Repositories) == 0 && len(auth.Definitions) == 0 && len(auth.History) == 0 &&
		len(auth.Catalog) == 0 && len(auth.Pins) == 0 && len(auth.Policies) == 0 && len(auth.Runs) == 0 && len(auth.MetadataReceipts) == 0 {
		return nil, nil
	}
	return auth, nil
}

// Walk one durable record at a time for v2 replica construction. Directory
// entries are sorted by os.ReadDir, so unchanged host state has stable bytes.
func walkPoolBackupAuthorityRecords(s *Store, emit func(byte, any) error) (bool, error) {
	nonempty := false
	visit := func(kind byte, record any) error {
		nonempty = true
		return emit(kind, record)
	}
	repos, err := loadPoolBackupRepositories(s)
	if err != nil {
		return false, err
	}
	repoIDs := make([]string, 0, len(repos.Repositories))
	for id := range repos.Repositories {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(repoIDs)
	for _, id := range repoIDs {
		repo := repos.Repositories[id]
		repo.Access, repo.MemberAccess, repo.Health = protocol.StorageAccess{}, nil, protocol.BackupRepositoryHealth{}
		if err := visit(1, repo); err != nil {
			return false, err
		}
	}
	policies, err := loadPoolBackupPolicies(s)
	if err != nil {
		return false, err
	}
	policyIDs := make([]string, 0, len(policies.Policies))
	for id := range policies.Policies {
		policyIDs = append(policyIDs, id)
	}
	sort.Strings(policyIDs)
	for _, id := range policyIDs {
		if err := visit(2, policies.Policies[id]); err != nil {
			return false, err
		}
	}
	for _, group := range []struct {
		kind byte
		path string
	}{
		{3, poolBackupRunsPath(s)},
		{4, poolBackupCatalogPath(s)},
		{6, poolBackupReceiptsPath(s)},
	} {
		entries, err := os.ReadDir(group.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(group.path, entry.Name()))
			if err != nil {
				return false, err
			}
			switch group.kind {
			case 3:
				var run protocol.BackupRun
				if json.Unmarshal(raw, &run) != nil || run.Valid() != nil || run.OperationID+".json" != entry.Name() {
					return false, errors.New("backup run record is corrupt")
				}
				err = visit(3, run)
			case 4:
				var point protocol.BackupRecoveryPoint
				if json.Unmarshal(raw, &point) != nil || point.Valid() != nil || point.ID+".json" != entry.Name() {
					return false, errors.New("backup catalog entry is corrupt")
				}
				err = visit(4, point)
			case 6:
				var receipt protocol.BackupMetadataReceipt
				if json.Unmarshal(raw, &receipt) != nil || receipt.Valid() != nil || receipt.OperationID+".json" != entry.Name() {
					return false, errors.New("backup metadata receipt is corrupt")
				}
				err = visit(6, receipt)
			}
			if err != nil {
				return false, err
			}
		}
	}
	pins, err := loadPoolBackupPins(s)
	if err != nil {
		return false, err
	}
	pinIDs := make([]string, 0, len(pins.Pins))
	for id := range pins.Pins {
		pinIDs = append(pinIDs, id)
	}
	sort.Strings(pinIDs)
	for _, id := range pinIDs {
		if err := visit(5, pins.Pins[id]); err != nil {
			return false, err
		}
	}
	return nonempty, nil
}
