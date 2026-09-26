// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"nex.local/nex/internal/protocol"
)

type poolVMStartPermit struct {
	PoolID string `json:"pool_id"`
	HostID string `json:"host_id"`
	VMID   string `json:"vm_id"`
	Permit string `json:"permit"`
	Epoch  uint64 `json:"epoch"`
	BootID string `json:"boot_id"`
}

func poolVMStartPermitPath(stateDir, vmID string) string {
	return filepath.Join(stateDir, "pool-vm-permits", vmID+".json")
}
func poolVMAdmissionPath(stateDir, vmID string) string {
	return filepath.Join(stateDir, "pool-vm-admissions", vmID+".json")
}

// WritePoolVMStartPermit is called only after the pool master durably admits this
// host's start.  The hook consumes it once, so a direct libvirt/autostart attempt
// cannot reuse an old admission.
func WritePoolVMStartPermit(stateDir, vmID, permit string) error {
	return WritePoolVMStartPermitAdmission(stateDir, "local", "local", vmID, 1, permit)
}

// WritePoolVMStartPermitAdmission persists the exact master-issued execution
// epoch alongside the one-use permit.  It is private Core state, not a
// libvirt or client supplied token.
func WritePoolVMStartPermitAdmission(stateDir, poolID, hostID, vmID string, epoch uint64, permit string) error {
	if poolID == "" || hostID == "" || !uuidPattern.MatchString(vmID) || epoch == 0 || len(permit) != 64 {
		return errors.New("invalid pool VM start permit")
	}
	dir := filepath.Join(stateDir, "pool-vm-permits")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	bootID, err := readKernelBootID()
	if err != nil {
		return err
	}
	b, err := json.Marshal(poolVMStartPermit{PoolID: poolID, HostID: hostID, VMID: vmID, Permit: permit, Epoch: epoch, BootID: bootID})
	if err != nil {
		return err
	}
	admissionDir := filepath.Join(stateDir, "pool-vm-admissions")
	if err := os.MkdirAll(admissionDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(admissionDir, 0700); err != nil {
		return err
	}
	// Write the durable current admission before making its consumable permit
	// visible.  A crash between these writes fails closed: an older permit cannot
	// match the newer epoch record.
	if err := atomicWrite(poolVMAdmissionPath(stateDir, vmID), b); err != nil {
		return err
	}
	return atomicWrite(poolVMStartPermitPath(stateDir, vmID), b)
}

var poolHostBootID = readKernelBootID

func RemovePoolVMStartPermit(stateDir, vmID string) error {
	if !uuidPattern.MatchString(vmID) {
		return errors.New("invalid pool VM identity")
	}
	err := os.Remove(poolVMStartPermitPath(stateDir, vmID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err = os.Remove(poolVMAdmissionPath(stateDir, vmID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func consumePoolVMStartPermit(stateDir, vmID string) error {
	path := poolVMStartPermitPath(stateDir, vmID)
	claimed := path + ".claimed"
	// rename is atomic: concurrent libvirt prepare hooks cannot both consume one permit.
	if err := os.Rename(path, claimed); err != nil {
		return errors.New("managed shared VM startup lacks a Core execution permit")
	}
	defer os.Remove(claimed)
	b, err := os.ReadFile(claimed)
	if err != nil {
		return errors.New("managed shared VM startup lacks a Core execution permit")
	}
	var p poolVMStartPermit
	bootID, bootErr := readKernelBootID()
	if json.Unmarshal(b, &p) != nil || p.PoolID == "" || p.HostID == "" || p.VMID != vmID || p.Epoch == 0 || len(p.Permit) != 64 || bootErr != nil || p.BootID != bootID {
		return errors.New("managed shared VM execution permit is invalid")
	}
	current, err := os.ReadFile(poolVMAdmissionPath(stateDir, vmID))
	if err != nil {
		return errors.New("managed shared VM execution admission is unavailable")
	}
	var admission poolVMStartPermit
	if json.Unmarshal(current, &admission) != nil || admission.PoolID != p.PoolID || admission.HostID != p.HostID || admission.VMID != p.VMID || admission.Epoch != p.Epoch || admission.BootID != p.BootID {
		return errors.New("managed shared VM execution permit is stale")
	}
	return nil
}

const poolStateJSONLimit = 1 << 22
const poolReplicaFileName = "pool-replica.json"

type poolReplicaRecord struct {
	Replica  protocol.PoolAuthorityReplica `json:"replica"`
	Verified bool                          `json:"verified"`
}

func readPoolStateFile(stateDir, name string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, os.ErrNotExist
	}
	if err != nil || len(b) > poolStateJSONLimit {
		return nil, errors.New("unreadable")
	}
	return b, nil
}

// poolHostRestricted reads this Core's own durable pool authority state and refuses a
// managed VM start while this host is restricted. It is deliberately a read of the
// one record that decides it, not a copy kept for this hook: a mirror could be stale in
// exactly the direction that starts a second writer. A record that cannot be read at all
// fails closed; a host that is in no pool has no record and is unaffected.
func poolHostRestricted(stateDir string) error {
	unreadable := errors.New("managed VM startup cannot read this host's pool authority record")
	b, err := readPoolStateFile(stateDir, "pool-peer.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return unreadable
	}
	var state struct {
		Membership    protocol.PoolMembership
		Restricted    *struct{ Reason string }
		SelfFence     *struct{}
		BootAuthority *struct {
			PoolID string
			HostID string
			BootID string
		}
	}
	if json.Unmarshal(b, &state) != nil {
		return unreadable
	}
	if state.Restricted != nil {
		reason := strings.TrimSpace(state.Restricted.Reason)
		if len(reason) > 200 || strings.ContainsAny(reason, "\r\n\x00") {
			reason = ""
		}
		if reason == "" {
			reason = "this host cannot establish its pool's current authority"
		}
		return errors.New("managed VM startup is refused while this host is restricted: " + reason)
	}
	// A host with no durable pool membership is outside pool authority. A current member
	// is not its master's authority writer, so existing member and single-host behavior is
	// unchanged. The only boot race is a self-master another member could have fenced.
	if state.Membership.PoolID == "" {
		return nil
	}
	if state.Membership.Valid() != nil {
		return unreadable
	}
	var identity struct {
		HostID string `json:"host_id"`
	}
	identityBytes, identityErr := readPoolStateFile(stateDir, "state.json")
	if identityErr != nil || json.Unmarshal(identityBytes, &identity) != nil || identity.HostID == "" {
		return unreadable
	}
	if state.Membership.MasterHostID != identity.HostID {
		return nil
	}
	readyPeer := false
	var self *protocol.PoolMember
	for i := range state.Membership.Members {
		member := &state.Membership.Members[i]
		if member.HostID == identity.HostID {
			self = member
		} else if member.Status == "ready" {
			readyPeer = true
		}
	}
	if !readyPeer || (state.SelfFence == nil && (self == nil || PoolFenceUsable(self.Fence, self.SystemUUID) != nil)) {
		return nil
	}
	bootID, bootErr := poolHostBootID()
	if bootErr != nil || state.BootAuthority == nil || state.BootAuthority.PoolID != state.Membership.PoolID || state.BootAuthority.HostID != identity.HostID || state.BootAuthority.BootID != bootID {
		return errors.New("managed VM startup is refused until this boot confirms its pool authority")
	}
	return nil
}

// poolHostDrainMembership answers with the freshest membership this host can read for a
// maintenance check. On the master that is its own pool-peer.json record. On a member it is
// the verified authority replica when one is present: the member's own membership copy is
// written at join and never again, so the drain flag in it is stale.
func poolHostDrainMembership(stateDir, hostID string, own protocol.PoolMembership) protocol.PoolMembership {
	if own.MasterHostID == hostID {
		return own
	}
	b, err := readPoolStateFile(stateDir, poolReplicaFileName)
	if errors.Is(err, os.ErrNotExist) || err != nil {
		return own
	}
	var record poolReplicaRecord
	if json.Unmarshal(b, &record) != nil || !record.Verified {
		return own
	}
	replica := record.Replica.Membership
	if replica.PoolID != own.PoolID || replica.MasterHostID != own.MasterHostID || replica.Valid() != nil {
		return own
	}
	return replica
}

// poolHostDrained refuses a managed guest's start while this host's pool record has it in
// maintenance. It is the last gate a libvirt autostart or a direct virsh start passes, and it
// is the only one that runs when libvirtd starts a domain before Core's agent does.
//
// It reads this host's identity and membership from pool-peer.json. On the pool master —
// including every single-host pool — that membership is authoritative. On a member it also
// reads pool-replica.json when a verified replica matches the member's pool identity, because
// the member's own membership copy is written at join and never again. A host that is in no
// pool is unaffected.
func poolHostDrained(stateDir string) error {
	unreadable := errors.New("managed VM startup cannot read this host's pool membership record")
	var identity struct {
		HostID string `json:"host_id"`
	}
	b, err := readPoolStateFile(stateDir, "state.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || json.Unmarshal(b, &identity) != nil {
		return unreadable
	}
	if identity.HostID == "" {
		return nil
	}
	b, err = readPoolStateFile(stateDir, "pool-peer.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return unreadable
	}
	var state struct {
		Membership protocol.PoolMembership
	}
	if json.Unmarshal(b, &state) != nil || state.Membership.Valid() != nil {
		return unreadable
	}
	membership := poolHostDrainMembership(stateDir, identity.HostID, state.Membership)
	for _, member := range membership.Members {
		if member.HostID == identity.HostID && member.Maintenance {
			return errors.New("managed VM startup is refused while this host is in maintenance mode; take it out of maintenance before starting VMs on it")
		}
	}
	return nil
}

// StorageGuard checks libvirt's prepare/begin XML without calling libvirt or taking
// the managed mutation lock: libvirt invokes it while the initiating operation
// already holds that lock. It never mounts, logs in, or repairs a repository.
func StorageGuard(ctx context.Context, stateDir string, input io.Reader) error {
	b, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("storage startup XML is unavailable or exceeds 1 MiB")
	}
	var domain domainXML
	if err := xml.Unmarshal(b, &domain); err != nil || domain.XMLName.Local != "domain" {
		return errors.New("invalid storage startup domain XML")
	}
	// A drained or restricted host starts no managed guest, whatever asked for it. Both
	// gates run before repository work: local-storage guests never reach the shared-storage
	// permit gate below, and they are exactly the ones libvirt brings back after a reboot.
	if hasManagedDisk(domain) {
		if err := poolHostDrained(stateDir); err != nil {
			return err
		}
		if err := poolHostRestricted(stateDir); err != nil {
			return err
		}
	}
	js, err := NewManaged(filepath.Join(stateDir, "managed-vms")).journals()
	if err != nil {
		if os.IsNotExist(err) && !hasManagedDisk(domain) {
			return nil // A fresh host can still boot its independent Controller appliance.
		}
		return errors.New("storage startup cannot read the Core repository journal")
	}
	shared := false
	err = checkStorageStartup(ctx, domain, js, func(ctx context.Context, s protocol.StorageRegistration) error {
		shared = shared || protocol.IsPoolSharedStorage(s)
		a, err := storageInspect(ctx, s)
		if err != nil {
			return err
		}
		return storageVerify(ctx, s, a)
	})
	if err != nil {
		return err
	}
	if shared && hasManagedDisk(domain) {
		// Shared storage also needs a one-use execution permit after its repository
		// checks have passed. Local managed guests do not consume a shared permit.
		if err := consumePoolVMStartPermit(stateDir, domain.UUID); err != nil {
			return err
		}
	}
	if hasManagedDisk(domain) {
		for _, disk := range domain.Devices.Disks {
			if disk.Source.File != "" {
				if err := startupDiskFile(disk.Source.File); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func startupDiskFile(path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return errors.New("managed VM startup disk is missing or replaced by a symlink")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("managed VM startup requires a regular disk file")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return errors.New("managed VM startup disk has hardlink aliases")
	}
	return nil
}

func hasManagedDisk(d domainXML) bool {
	for _, disk := range d.Devices.Disks {
		if strings.Contains(disk.Source.File, "/nex-vms/") {
			return true
		}
	}
	return false
}

func checkStorageStartup(ctx context.Context, d domainXML, js []managedJournal, verify func(context.Context, protocol.StorageRegistration) error) error {
	managed := hasManagedDisk(d)
	for _, j := range js {
		if j.Command.VM != nil && j.Command.VM.ID == d.UUID {
			managed = true
		}
	}
	if managed && !uuidPattern.MatchString(d.UUID) {
		return errors.New("managed VM startup requires a valid VM identity")
	}
	if managed {
		if err := recoveryVMReservationRefusal(js, d.UUID, ""); err != nil {
			return err
		}
	}
	repositories := storageProjection(js)
	checked := map[string]bool{}
	for _, disk := range d.Devices.Disks {
		if disk.Device != "disk" && disk.Device != "cdrom" {
			continue
		}
		path := disk.Source.File
		if path == "" && disk.Device == "cdrom" {
			continue
		}
		if !managed {
			continue // This hook does not change ownership of unrelated guests.
		}
		if disk.Type != "file" || !filepath.IsAbs(path) || filepath.Clean(path) != path || disk.Source.Dev != "" || disk.Source.Protocol != "" {
			return errors.New("managed VM startup requires a registered filesystem disk")
		}
		s, err := storageStartupRepository(path, repositories)
		if err != nil {
			return err
		}
		root := storageRegistrationPath(s)
		if disk.Device == "disk" && !strings.HasPrefix(path, filepath.Join(root, "nex-vms", d.UUID)+string(filepath.Separator)) {
			return errors.New("managed VM startup disk does not belong to this VM")
		}
		if !checked[s.ID] {
			if err := verify(ctx, s); err != nil {
				return errors.New("managed VM startup blocked: repository is unavailable or its identity changed")
			}
			checked[s.ID] = true
		}
	}
	return nil
}

// storageStartupRepository assigns a path to its most specific registered root.
// Nested local registrations are valid, so a broad default registration must not
// claim a VM disk stored in a nested repository.
func storageStartupRepository(path string, repositories map[string]protocol.StorageRegistration) (protocol.StorageRegistration, error) {
	var selected protocol.StorageRegistration
	selectedRoot := ""
	ambiguous := false
	for _, s := range repositories {
		root := storageRegistrationPath(s)
		if !strings.HasPrefix(path, root+string(filepath.Separator)) {
			continue
		}
		if selectedRoot == "" || len(root) > len(selectedRoot) {
			selected, selectedRoot, ambiguous = s, root, false
			continue
		}
		if len(root) == len(selectedRoot) && s.ID != selected.ID {
			ambiguous = true
		}
	}
	if selectedRoot == "" {
		return protocol.StorageRegistration{}, errors.New("managed VM startup disk repository is missing or detached")
	}
	if ambiguous {
		return protocol.StorageRegistration{}, errors.New("managed VM startup disk repository ownership is ambiguous")
	}
	return selected, nil
}
