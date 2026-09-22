// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Fencing a pool host for a promotion is the same deliberately destructive native action
// clustered storage already performs, with the same discipline: only a profile that can
// independently prove the target's identity may be used, and only a separate observation of
// the target powered off makes a proof. A fence agent's exit status, a missed heartbeat, a
// timeout and an operator assurance are not evidence and cannot produce this record.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"nex.local/nex/internal/protocol"
)

// VerifyPoolHostFence excludes one pool host and returns the proof, bound to the promotion
// its caller names. The binding fields are the ones StorageClusterFenceStatus already
// carries: RequestID is the promotion's operation ID, ConfigGeneration the authority
// generation being promoted, and ConfigDigest the caller's promotion digest.
//
// Unlike a clustered-storage fence this does not require the target to be observed powered
// on first: a pool master that is being promoted away is usually already down, and refusing
// to fence a host that is already off would make an honest proof impossible in exactly the
// case this exists for. Target identity is still proved independently, which is what the
// power-on observation contributes there.
func VerifyPoolHostFence(ctx context.Context, stateDir string, f *protocol.StorageClusterFence, systemUUID, hostID, operationID, digest string, generation uint64) (protocol.StorageClusterFenceStatus, error) {
	proof := protocol.StorageClusterFenceStatus{
		RequestID: operationID, HostID: hostID, Status: "unverified",
		ConfigGeneration: generation, ConfigDigest: digest, ObservedAt: storageFenceNow().UTC(),
	}
	if hostID == "" || operationID == "" || digest == "" || generation == 0 {
		proof.Status, proof.Error = "failed", "a pool fence proof must be bound to a host, an operation and a generation"
		return proof, errors.New(proof.Error)
	}
	if f == nil {
		proof.Status, proof.Error = "failed", "this host has no recorded fence profile; record one before promoting"
		return proof, errors.New(proof.Error)
	}
	err := verifyFence(ctx, stateDir, *f, systemUUID, &proof, false)
	if proof.ObservedAt.IsZero() {
		proof.ObservedAt = storageFenceNow().UTC()
	}
	return proof, err
}

// PoolFenceUsable reports whether this build could turn a recorded profile into a proof at
// all. It is the same refusal verifyFence applies, decided without running anything, so one
// predicate answers both "may this profile be recorded" and "could this host have been
// fence-promoted while it was down". systemUUID is the target host's own recorded SMBIOS
// system UUID: a BMC profile without one could never be proved to address the right machine,
// so it is refused here rather than at the moment somebody is about to power a host off.
func PoolFenceUsable(f *protocol.StorageClusterFence, systemUUID string) error {
	if f == nil {
		return errors.New("no fence profile is recorded for this host")
	}
	if err := f.Valid(); err != nil {
		return errors.New("configured fence target was refused")
	}
	switch f.Kind {
	case protocol.FenceSCSIPR:
		return errors.New("SCSI persistent-reservation fencing is unsupported")
	case protocol.FenceRedfish, protocol.FenceIPMI:
		if f.Username == "" {
			return errors.New("a management controller fence device needs the account Nex Core signs in with")
		}
		// Empty, a firmware placeholder, and an identity another host in this pool also
		// reports all arrive here as "" — none of them names one machine.
		if NormalizedSystemUUID(systemUUID) == "" {
			return errors.New("this host does not report a unique hardware identity, so a management controller cannot be confirmed to be its own")
		}
		return nil
	case protocol.FenceVirsh:
		if !uuidPattern.MatchString(f.TargetID) {
			return errors.New("virsh fencing needs an immutable domain UUID target")
		}
		return nil
	}
	return errors.New("unsupported fencing profile")
}

// CheckPoolFenceTarget reads one host's fence device and changes nothing. It is the whole of
// what a continuous check does: sign in to the management controller with the recorded
// account, ask it which machine it manages, and read that machine's power state. There is no
// branch from here to a power-off: the only two requests it can make are bmcSession.system,
// an HTTP GET, and bmcSession.powerState, a GET or `ipmitool … chassis power status`, and no
// fence agent is reachable from here at all — so running this against every host in a pool
// every few minutes cannot switch anything off.
//
// It returns one of the closed protocol results and Core's own sentence for it, so an
// operator sees "BMC not reachable from host-b" rather than a host that is merely not ready.
//
// secret is the fence account's password, which the caller has already opened: the pool
// master reads it from its own credential store, and a member unseals the copy inside its own
// authority replica in memory. It is deliberately a parameter rather than a read from this
// host's disk, because the alternative is writing every other host's fence password onto every
// member for a read-only check.
func CheckPoolFenceTarget(ctx context.Context, stateDir string, f *protocol.StorageClusterFence, systemUUID, secret string) (result, detail, power string) {
	if err := PoolFenceUsable(f, systemUUID); err != nil {
		if f == nil {
			return protocol.PoolFenceCheckNoProfile, err.Error(), ""
		}
		return protocol.PoolFenceCheckUnusable, err.Error(), ""
	}
	if f.Kind != protocol.FenceRedfish && f.Kind != protocol.FenceIPMI {
		// A virsh device is a disposable lab's, reached over somebody else's libvirt.
		// It is not checked continuously and its caller does not ask.
		return protocol.PoolFenceCheckUnusable, "only a management controller is checked continuously", ""
	}
	if secret == "" {
		return protocol.PoolFenceCheckUnusable, "this host holds no fence credential for that management controller", ""
	}
	b, err := openBMCSession(*f, secret)
	if err != nil {
		return poolFenceCheckResult(err)
	}
	defer b.close()
	if err = b.targetIdentity(ctx, systemUUID); err != nil {
		return poolFenceCheckResult(err)
	}
	if power, err = b.powerState(ctx); err != nil {
		return poolFenceCheckResult(err)
	}
	return protocol.PoolFenceCheckOK, "", power
}

// poolFenceCheckResult classifies a refusal the same way the fence itself does, so the row
// says what an operator has to act on: a wrong machine and a changed certificate are not
// network faults and must never read as one.
func poolFenceCheckResult(err error) (string, string, string) {
	switch {
	case errors.Is(err, errBMCWrongMachine):
		return protocol.PoolFenceCheckIdentity, err.Error(), ""
	case errors.Is(err, errBMCCertificateChanged):
		return protocol.PoolFenceCheckCertificate, err.Error(), ""
	case errors.Is(err, errBMCRejected):
		return protocol.PoolFenceCheckRejected, err.Error(), ""
	case errors.Is(err, errNoIPMITool):
		return protocol.PoolFenceCheckToolMissing, err.Error(), ""
	case errors.Is(err, errBMCNoAnswer), errors.Is(err, errBMCNoReport):
		return protocol.PoolFenceCheckUnreachable, err.Error(), ""
	}
	return protocol.PoolFenceCheckUnusable, err.Error(), ""
}

// Fence proof freshness (ObservedAt within the promotion window) is enforced in protocol
// beside the promotion stages that consume StorageClusterFenceStatus.

// PoolFenceCredentialLimit bounds one host's fence credential material. A fence_ssh profile
// is an SSH private key and a known_hosts line — a couple of kilobytes — so this is generous
// and still keeps the replica far below its own 1 MiB limit: a wrapped credential is the
// sealed plaintext base64-encoded, about 1.4x, so about 22 KiB per profiled host.
const PoolFenceCredentialLimit = 16 << 10

// poolFenceCredentialNames are the files one recorded profile owns, relative to its
// reference: the credential itself and, for an SSH profile, its pinned host keys.
var poolFenceCredentialNames = []string{"", ".known_hosts"}

// poolFenceCredentialMode is the mode the store keeps one profile's own credential file at.
// A BMC password is a script the fence agent executes, so it is 0700; an SSH identity is
// only ever read. The pinned host keys beside it are always 0600.
func poolFenceCredentialMode(f protocol.StorageClusterFence, suffix string) os.FileMode {
	if suffix == "" && f.Kind != protocol.FenceVirsh {
		return 0700
	}
	return 0600
}

func poolFenceCredentialPath(stateDir, ref, suffix string) (string, error) {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return "", errors.New("invalid fence credential root")
	}
	if ref == "" || len(ref) > 128 || strings.ContainsAny(ref, "/\\\x00") || strings.HasPrefix(ref, ".") {
		return "", errors.New("invalid fence credential reference")
	}
	return filepath.Join(stateDir, "storage-cluster", "fence-credentials", ref+suffix), nil
}

// ReadPoolFenceCredential returns the private material of one recorded fence profile, so the
// pool master can seal it for the members that may one day have to fence with it. The bytes
// are returned to Core only; no caller logs, reports or returns them over an API.
func ReadPoolFenceCredential(stateDir string, f protocol.StorageClusterFence) (map[string][]byte, error) {
	out := map[string][]byte{}
	total := 0
	for _, suffix := range poolFenceCredentialNames {
		path, err := poolFenceCredentialPath(stateDir, f.CredentialRef, suffix)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			if suffix == "" {
				return nil, errors.New("fence credential is unavailable")
			}
			continue
		}
		if info.Mode().Perm() != poolFenceCredentialMode(f, suffix) {
			return nil, errors.New("fence credential is not private")
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
			return nil, errors.New("fence credential is not Core-owned")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, errors.New("fence credential is unavailable")
		}
		total += len(b)
		if total > PoolFenceCredentialLimit {
			return nil, errors.New("fence credential material is too large to replicate")
		}
		out[suffix] = b
	}
	return out, nil
}

// PoolFenceCredentialDigest identifies the material without exposing it. It is kept in the
// master's own private state and never replicated, reported or logged; what the pool sees is
// the version number the master bumps when this digest changes.
func PoolFenceCredentialDigest(files map[string][]byte) string {
	sum := sha256.New()
	for _, suffix := range poolFenceCredentialNames {
		sum.Write([]byte(suffix + "\x00"))
		sum.Write(files[suffix])
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// WritePoolFenceCredential materializes the material on this host at the same private paths
// the credential handoff uses. It refuses to replace material that is already there and
// different: a promotion must never quietly overwrite a credential this host already owns.
func WritePoolFenceCredential(stateDir string, f protocol.StorageClusterFence, files map[string][]byte) error {
	dir, err := poolFenceCredentialPath(stateDir, f.CredentialRef, "")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	if err = os.Chmod(filepath.Dir(dir), 0700); err != nil {
		return err
	}
	for _, suffix := range poolFenceCredentialNames {
		data, ok := files[suffix]
		if !ok {
			continue
		}
		path, pathErr := poolFenceCredentialPath(stateDir, f.CredentialRef, suffix)
		if pathErr != nil {
			return pathErr
		}
		// The file this writes is mode 0700 and a fence agent and Pacemaker both run it as
		// root, so only bytes the one producer wrote may reach it. The material arrives
		// here unsealed from another host's replica; parsing it is the same round-trip
		// check every read applies, applied where Core writes instead.
		if suffix == "" && f.Kind != protocol.FenceVirsh {
			if _, err = FenceCredentialSecretFromScript(data); err != nil {
				return errors.New("this fence credential is not in the format Nex Core writes")
			}
		}
		if existing, readErr := os.ReadFile(path); readErr == nil {
			if string(existing) == string(data) {
				continue
			}
			return errors.New("this host already holds different material under that fence credential reference")
		}
		if err = AtomicWrite(path, data, poolFenceCredentialMode(f, suffix)); err != nil {
			return err
		}
	}
	return nil
}

// RetirePoolFenceCredential removes the material of a profile that was cleared or replaced.
// It is the whole lifecycle: material exists on the host that holds the profile, and stops
// existing when that host stops holding it.
func RetirePoolFenceCredential(stateDir, ref string) error {
	for _, suffix := range poolFenceCredentialNames {
		path, err := poolFenceCredentialPath(stateDir, ref, suffix)
		if err != nil {
			return err
		}
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
