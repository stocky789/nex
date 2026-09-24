// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
)

// SanlockLockspacePath returns the stable lockspace file for one applied repository.
// The caller must first verify that repoPath is the matching, active shared NFS mount.
func SanlockLockspacePath(repoPath string) (string, error) {
	if !filepath.IsAbs(repoPath) || filepath.Clean(repoPath) != repoPath || repoPath == "/" {
		return "", errors.New("invalid sanlock repository path")
	}
	return filepath.Join(repoPath, ".nex-sanlock", "lockspace"), nil
}

func SanlockAuthorityResourcePath(repoPath, poolID string) (string, error) {
	if !storageID.MatchString(poolID) {
		return "", errors.New("invalid sanlock pool identity")
	}
	lockspace, err := SanlockLockspacePath(repoPath)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("nex-pool-authority-lease-v1\x00" + poolID))
	return filepath.Join(filepath.Dir(lockspace), "authority-"+hex.EncodeToString(digest[:16])), nil
}

func SanlockAuthorityResourceKey(poolID string) (string, error) {
	if !storageID.MatchString(poolID) {
		return "", errors.New("invalid sanlock pool identity")
	}
	digest := sha256.Sum256([]byte("nex-pool-authority-lease-v1\x00" + poolID))
	return "nex-authority-" + hex.EncodeToString(digest[:16]), nil
}

func SanlockAuthorityLease(poolID, lockspace, repoPath string) (SanlockResource, error) {
	path, err := SanlockAuthorityResourcePath(repoPath, poolID)
	if err != nil {
		return SanlockResource{}, err
	}
	key, err := SanlockAuthorityResourceKey(poolID)
	if err != nil || !sanlockName(lockspace) {
		return SanlockResource{}, errors.New("invalid sanlock authority lockspace")
	}
	return SanlockResource{Lockspace: lockspace, Key: key, Path: path}, nil
}

// SanlockVMResourcePath is stable across pool members and unique to the VM/repository pair.
func SanlockVMResourcePath(repoPath, vmID, repositoryID string) (string, error) {
	if !uuidPattern.MatchString(vmID) || !storageID.MatchString(repositoryID) {
		return "", errors.New("invalid sanlock VM resource identity")
	}
	lockspace, err := SanlockLockspacePath(repoPath)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("nex-vm-repo-lease-v1\x00" + vmID + "\x00" + repositoryID))
	return filepath.Join(filepath.Dir(lockspace), "vm-"+hex.EncodeToString(digest[:16])), nil
}
