// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"
)

type JournalEntry struct {
	AfterOperationID string            `json:"after_operation_id,omitempty"`
	Order            uint64            `json:"order,omitempty"`
	Command          *protocol.Command `json:"command,omitempty"`
	Status           string            `json:"status"`
	Error            string            `json:"error,omitempty"`
	RequestedAt      time.Time         `json:"requested_at"`
	ObservedAt       time.Time         `json:"observed_at"`
	Acknowledged     bool              `json:"acknowledged"`
}
type State struct {
	Controller           string `json:"controller"`
	ControllerServerName string `json:"controller_server_name,omitempty"`
	TrustPEM             string `json:"trust_pem"`
	// Identity is host-owned. HostID is set once a Controller has accepted it and is the
	// enrolled marker; PendingHostID holds the UUID this host generated before its first
	// enrollment so retries present the same one. PoolID and PoolName are generated with it
	// (this host is its pool's master) and presented unchanged to every Controller, including
	// a replacement one. Hosts enrolled before host-owned identity keep their Controller-issued
	// HostID and adopt the pool identity the Controller reports.
	HostID         string `json:"host_id"`
	PendingHostID  string `json:"pending_host_id,omitempty"`
	PoolID         string `json:"pool_id,omitempty"`
	PoolName       string `json:"pool_name,omitempty"`
	KeyPEM         string `json:"key_pem"`
	CSRPEM         string `json:"csr_pem"`
	CertificatePEM string `json:"certificate_pem"`
	PendingKeyPEM  string `json:"pending_key_pem,omitempty"`
	PendingCSRPEM  string `json:"pending_csr_pem,omitempty"`
	Sequence       uint64 `json:"sequence"`
	// StorageProtocolReady is set only after this Core has received a positive,
	// authenticated Controller v2 preflight. It prevents a package upgrade from
	// sending new storage fields to a strict v1 Controller.
	StorageProtocolReady bool `json:"storage_protocol_ready,omitempty"`
	// ZFSProvisionProtocolReady is the narrower Controller acknowledgement for
	// ZFS-only inventory and commands. It resets on Controller replacement.
	ZFSProvisionProtocolReady bool `json:"zfs_provision_protocol_ready,omitempty"`
	// ZFSReplaceProtocolReady is the narrower acknowledgement for hot-spare /
	// widened-layout selections and the additive zfs_pool / pool observation
	// fields. It resets on Controller replacement and clears when a Controller
	// only acknowledges storage-zfs-provision-v1.
	ZFSReplaceProtocolReady bool `json:"zfs_replace_protocol_ready,omitempty"`
	// StorageClusterProtocolReady is Controller-connection scoped. It is set
	// only by the explicit clustered-storage v2 acknowledgement and reset on
	// reconnect/replacement before a report can carry clustered-storage data.
	StorageClusterProtocolReady bool `json:"storage_cluster_protocol_ready,omitempty"`
	// StorageClusterSessionMonitorProtocolReady is a narrower,
	// Controller-connection-scoped acknowledgement for the additive member
	// session-monitor report fields. It never represents native readiness.
	StorageClusterSessionMonitorProtocolReady bool `json:"storage_cluster_session_monitor_protocol_ready,omitempty"`
	// StorageProtocolLegacy records an explicit v2 rejection by a Controller only
	// while Core has no v2 storage state. It preserves existing local management.
	StorageProtocolLegacy bool                    `json:"storage_protocol_legacy,omitempty"`
	Journal               map[string]JournalEntry `json:"journal"`
}

type Store struct {
	cpuBaseline       poolCPUCache // transient observation, never authoritative pool state
	dir               string
	lock              *os.File
	poolStorageAccess map[string]map[string]protocol.PoolStorageAccess
	// storageMonitor is Run's live access observer, shared so a peer reply is as fresh as a report.
	storageMonitor atomic.Pointer[vmhost.StorageMonitor]
	beforePublish  func() error // test-only deterministic pre-rename failure hook
	afterPublish   func() error // test-only deterministic post-rename failure hook
	State          State
}

// publishedStateError means state.json was atomically renamed before the
// durability confirmation failed. The current process must not revert its
// in-memory State to the old value: the new file is already visible, while a
// crash can still require retrying from the previously persisted pending state.
type publishedStateError struct{ err error }

func (e *publishedStateError) Error() string {
	return "state was published but durability is uncertain; recheck host status: " + e.err.Error()
}
func (e *publishedStateError) Unwrap() error { return e.err }

func stateWasPublished(err error) bool {
	var published *publishedStateError
	return errors.As(err, &published)
}

func OpenStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("state directory must be a real directory with permissions 0700")
	}
	fd, err := syscall.Open(filepath.Join(dir, "lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "lock")
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another agent process owns the state directory")
	}
	s := &Store{dir: dir, lock: lock, poolStorageAccess: map[string]map[string]protocol.PoolStorageAccess{}, State: State{Journal: map[string]JournalEntry{}}}
	path := filepath.Join(dir, "state.json")
	if info, err = os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			s.Close()
			return nil, errors.New("state.json must be a regular private file")
		}
		data, e := os.ReadFile(path)
		if e != nil {
			s.Close()
			return nil, e
		}
		if e = json.Unmarshal(data, &s.State); e != nil {
			s.Close()
			return nil, fmt.Errorf("invalid agent state: %w", e)
		}
	} else if !os.IsNotExist(err) {
		s.Close()
		return nil, err
	}
	if s.State.Journal == nil {
		s.State.Journal = map[string]JournalEntry{}
	}
	return s, nil
}
func (s *Store) Close() error { return s.lock.Close() }
func (s *Store) Save() error {
	data, err := json.MarshalIndent(s.State, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".state-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if s.beforePublish != nil {
		if err = s.beforePublish(); err != nil {
			return err
		}
	}
	if err = os.Rename(name, filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	if s.afterPublish != nil {
		if err = s.afterPublish(); err != nil {
			return &publishedStateError{err: err}
		}
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return &publishedStateError{err: err}
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return &publishedStateError{err: err}
	}
	return nil
}

// newUUID returns a random version 4 UUID in canonical lowercase form.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func newKey() (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{}}, key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr})), nil
}
