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
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type JournalEntry struct {
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	RequestedAt  time.Time `json:"requested_at"`
	ObservedAt   time.Time `json:"observed_at"`
	Acknowledged bool      `json:"acknowledged"`
}
type State struct {
	Controller           string                  `json:"controller"`
	ControllerServerName string                  `json:"controller_server_name,omitempty"`
	TrustPEM             string                  `json:"trust_pem"`
	HostID               string                  `json:"host_id"`
	KeyPEM               string                  `json:"key_pem"`
	CSRPEM               string                  `json:"csr_pem"`
	CertificatePEM       string                  `json:"certificate_pem"`
	PendingKeyPEM        string                  `json:"pending_key_pem,omitempty"`
	PendingCSRPEM        string                  `json:"pending_csr_pem,omitempty"`
	Sequence             uint64                  `json:"sequence"`
	Journal              map[string]JournalEntry `json:"journal"`
}

type Store struct {
	dir   string
	lock  *os.File
	State State
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
	s := &Store{dir: dir, lock: lock, State: State{Journal: map[string]JournalEntry{}}}
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
	if err = os.Rename(name, filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	d, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
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
