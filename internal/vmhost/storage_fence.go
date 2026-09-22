// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Native fencing proof is intentionally separate from Pacemaker configuration:
// a CIB resource or an agent exit code is not evidence that a target was excluded.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

var storageFenceRun = runFenceAgent
var storageFenceEnvRun = runFenceAgentEnv
var storageFenceNow = time.Now
var virshDOMInfoUUID = regexp.MustCompile(`(?im)^\s*UUID\s*:\s*([0-9a-f-]+)\s*$`)

const fenceVirshPromptOption = `-t '/bin/bash -c "PS1=\\[EXPECT\\]# /bin/bash --noprofile --norc"'`

// runFenceAgent preserves a fence agent's status text even when that agent uses
// a non-zero status exit code. Its caller parses only the power state and never
// returns output or stderr to an API, journal, or error.
func runFenceAgent(ctx context.Context, name string, args ...string) ([]byte, error) {
	return runFenceAgentEnv(ctx, nil, "", name, args...)
}

// runFenceAgentEnv is the same run with extra environment and standard input. Only IPMI
// needs either: the environment hands ipmitool a session password no process list can read,
// and standard input answers its password prompt when it sets one on the controller, which
// is the only way to give it a password that never appears in a command argument.
func runFenceAgentEnv(ctx context.Context, env []string, stdin, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(append(os.Environ(), "LC_ALL=C"), env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.WaitDelay = time.Second
	var out, stderr cappedOutput
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	return out.Bytes(), err
}

// VerifyStorageClusterFence carries out one deliberately destructive native
// fence test for the exact configured node. It never accepts a caller-supplied
// result. A success means a separate observation found the target powered off.
// The pool master must persist the returned proof before storage activation.
func VerifyStorageClusterFence(ctx context.Context, stateDir string, c protocol.StorageClusterConfig, hostID string) (protocol.StorageClusterFenceStatus, error) {
	proof := protocol.StorageClusterFenceStatus{HostID: hostID, Status: "unverified", ObservedAt: storageFenceNow().UTC()}
	node, err := configuredFenceNode(c, hostID)
	if err != nil {
		proof.Status, proof.Error = "failed", "configured fence target was refused"
		return proof, err
	}
	proof.ConfigGeneration = c.Generation
	proof.ConfigDigest = protocol.StorageClusterFenceConfigDigest(c)
	// A configured storage cluster node must be found powered on first: that is what
	// distinguishes a fence device that actually controls this node from one reporting
	// "off" for a target that was never there.
	//
	// A clustered-storage node carries no recorded system UUID, so a BMC profile is refused
	// here: identity comes from the pool master's membership record, which this
	// configuration is not.
	err = verifyFence(ctx, stateDir, node.Fence, "", &proof, true)
	return proof, err
}

// verifyFence is the one native fencing discipline, shared by clustered storage and by a
// pool promotion. It refuses every profile that cannot independently prove target identity
// and exclusion, performs the power-off, and then confirms the power-off by a separate
// observation. It never accepts a caller-supplied result, and its caller must persist the
// proof it fills in before acting on it.
func verifyFence(ctx context.Context, stateDir string, f protocol.StorageClusterFence, systemUUID string, proof *protocol.StorageClusterFenceStatus, requirePoweredOn bool) error {
	proof.TargetID = f.TargetID
	// One predicate decides which profiles this build can ever prove anything with, so a
	// host cannot record a profile that would silently never work, and a restarted master
	// can ask the same question about itself without running a fence.
	if err := PoolFenceUsable(&f, systemUUID); err != nil {
		proof.Status, proof.Error = "failed", err.Error()
		return err
	}
	// One management controller, one conversation: the identity read, the power-off and
	// the settle poll share this session's password and its pinned connection.
	var bmc *bmcSession
	if f.Kind == protocol.FenceVirsh {
		if !virshTargetIdentity(ctx, stateDir, f) {
			proof.Status, proof.Error = "failed", "virsh target identity was not independently confirmed"
			return errors.New(proof.Error)
		}
	} else {
		session, err := newBMCSession(stateDir, f)
		if err != nil {
			proof.Status, proof.Error = "failed", err.Error()
			return err
		}
		defer session.close()
		bmc = session
		if err = bmc.targetIdentity(ctx, systemUUID); err != nil {
			proof.Status, proof.Error = "failed", err.Error()
			return err
		}
	}
	if requirePoweredOn {
		if err := fenceTargetOn(ctx, stateDir, f); err != nil {
			proof.Status, proof.Error = "failed", "configured target was not confirmed powered on"
			return errors.New(proof.Error)
		}
	}
	if err := fenceTargetOff(ctx, stateDir, f, bmc); err != nil {
		proof.Status, proof.Error = "failed", "native fence action did not complete"
		return errors.New(proof.Error)
	}
	if err := fenceTargetExcluded(ctx, stateDir, f, bmc); err != nil {
		proof.Status, proof.Error = "uncertain", "target exclusion was not independently confirmed"
		return errors.New(proof.Error)
	}
	proof.Status, proof.ExclusionVerified, proof.ObservedAt = "passed", true, storageFenceNow().UTC()
	proof.Error = ""
	return nil
}

func fenceTargetOn(ctx context.Context, stateDir string, f protocol.StorageClusterFence) error {
	name, args, err := fenceAgentArgs(stateDir, f, "status")
	if err != nil {
		return err
	}
	out, _ := storageFenceRun(ctx, name, args...)
	if !fencePowerOnOutput(out) {
		return errors.New("fence device did not observe target powered on")
	}
	return nil
}

func configuredFenceNode(c protocol.StorageClusterConfig, hostID string) (protocol.StorageClusterNode, error) {
	if hostID == "" || c.Generation == 0 || len(c.Nodes) < 3 || len(c.Nodes) > 5 {
		return protocol.StorageClusterNode{}, errors.New("fence target and generation are required")
	}
	var found protocol.StorageClusterNode
	seen := map[string]bool{}
	for _, node := range c.Nodes {
		if node.HostID == "" || seen[node.HostID] || node.Fence.Valid() != nil {
			return protocol.StorageClusterNode{}, errors.New("invalid configured fence target")
		}
		seen[node.HostID] = true
		if node.HostID != hostID {
			continue
		}
		if found.HostID != "" {
			return protocol.StorageClusterNode{}, errors.New("ambiguous fence target")
		}
		found = node
	}
	if found.HostID == "" {
		return protocol.StorageClusterNode{}, errors.New("invalid configured fence target")
	}
	return found, nil
}

func fenceTargetOff(ctx context.Context, stateDir string, f protocol.StorageClusterFence, bmc *bmcSession) error {
	// Redfish runs through this host's own pinned connection rather than fence_redfish: the
	// agent cannot be bound to the certificate Core recorded, and the only way to make it
	// talk to a factory controller at all would be to disable verification, which sends the
	// password to whoever answers. Everything else is the shipped agent.
	if f.Kind == protocol.FenceRedfish {
		return bmc.forceOff(ctx)
	}
	name, args, err := fenceAgentArgs(stateDir, f, "off")
	if err != nil {
		return err
	}
	_, err = storageFenceRun(ctx, name, args...)
	if err != nil {
		return errors.New("fence agent failed") // agent output can contain device details.
	}
	return nil
}

func fenceTargetExcluded(ctx context.Context, stateDir string, f protocol.StorageClusterFence, bmc *bmcSession) error {
	if f.Kind == protocol.FenceRedfish {
		// The same pinned resource the identity came from, read again until the machine
		// is off: this is the separate observation, not the power-off's own answer about
		// itself.
		return bmc.settled(ctx)
	}
	switch f.Kind {
	case protocol.FenceVirsh, protocol.FenceIPMI:
		name, args, err := fenceAgentArgs(stateDir, f, "status")
		if err != nil {
			return err
		}
		out, _ := storageFenceRun(ctx, name, args...)
		if !fencePowerOffOutput(out) {
			return errors.New("fence device did not observe target powered off")
		}
		return nil
	default:
		return errors.New("unsupported fencing profile")
	}
}

func fenceAgentArgs(stateDir string, f protocol.StorageClusterFence, action string) (string, []string, error) {
	if action != "off" && action != "status" {
		return "", nil, errors.New("invalid fence action")
	}
	// A profile recorded before BMC fencing carried an account has no username; it can
	// never produce a proof, and leaving the option out keeps its Pacemaker rendering the
	// same rather than emitting an empty one.
	bmcUsername := []string{}
	if f.Username != "" {
		bmcUsername = []string{"--username=" + f.Username}
	}
	switch f.Kind {
	case protocol.FenceRedfish:
		secret, err := fenceCredentialScript(stateDir, f.CredentialRef)
		return "fence_redfish", append([]string{"--ipaddr=" + f.Endpoint, "--system-id=" + f.TargetID}, append(bmcUsername, "--password-script="+secret, "--action="+action)...), err
	case protocol.FenceIPMI:
		secret, err := fenceCredentialScript(stateDir, f.CredentialRef)
		// RMCP+ (IPMI 2.0), the session every identity read and readiness check Core
		// makes already uses: fence_ipmilan defaults to IPMI 1.5, which a controller
		// with 2.0 in use often refuses, and the cipher suite Core selected from the
		// controller's own LAN channel only applies to lanplus.
		cipher := []string{"--lanplus"}
		if f.Cipher != 0 {
			cipher = append(cipher, "--cipher="+strconv.Itoa(int(f.Cipher)))
		}
		return "fence_ipmilan", append([]string{"--ipaddr=" + f.Endpoint, "--plug=" + f.TargetID}, append(append(bmcUsername, cipher...), "--password-script="+secret, "--action="+action)...), err
	case protocol.FenceVirsh:
		identity, err := fenceIdentityFile(stateDir, f.CredentialRef)
		knownHosts, hostErr := fenceKnownHostsFile(stateDir, f.SSHHostKeyRef)
		if err == nil {
			err = hostErr
		}
		sshOptions := "-oUserKnownHostsFile=" + knownHosts + " -oGlobalKnownHostsFile=/dev/null -oStrictHostKeyChecking=yes " + fenceVirshPromptOption
		return "fence_virsh", []string{"--ip=" + f.Endpoint, "--username=" + f.SSHLogin, "--ipport=" + strconv.Itoa(int(f.SSHPort)), "--plug=" + f.TargetID, "--ssh", "--identity-file=" + identity, "--ssh-options=" + sshOptions, "--action=" + action}, err
	default:
		return "", nil, errors.New("unsupported fencing profile")
	}
}

func fenceIdentityFile(stateDir, ref string) (string, error) {
	return fenceCredentialFile(stateDir, ref, 0600)
}

func fenceKnownHostsFile(stateDir, ref string) (string, error) {
	return fenceCredentialFile(stateDir, ref+".known_hosts", 0600)
}

func fenceCredentialScript(stateDir, ref string) (string, error) {
	return fenceCredentialFile(stateDir, ref, 0700)
}

func fenceCredentialFile(stateDir, ref string, mode os.FileMode) (string, error) {
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir || strings.ContainsAny(stateDir, " \t\n") || ref == "" {
		return "", errors.New("invalid fence credential root")
	}
	p := filepath.Join(stateDir, "storage-cluster", "fence-credentials", ref)
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != mode {
		return "", errors.New("fence credential script is unavailable")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("fence credential script is not Core-owned")
	}
	return p, nil
}

func virshTargetIdentity(ctx context.Context, stateDir string, f protocol.StorageClusterFence) bool {
	identity, err := fenceIdentityFile(stateDir, f.CredentialRef)
	if err != nil {
		return false
	}
	knownHosts, err := fenceKnownHostsFile(stateDir, f.SSHHostKeyRef)
	if err != nil {
		return false
	}
	options := []string{"-oUserKnownHostsFile=" + knownHosts, "-oGlobalKnownHostsFile=/dev/null", "-oStrictHostKeyChecking=yes"}
	args := append(options, "-i", identity, "-p", strconv.Itoa(int(f.SSHPort)), "--", f.SSHLogin+"@"+f.Endpoint, "virsh", "--connect", "qemu:///system", "dominfo", f.TargetID)
	out, err := storageFenceRun(ctx, "ssh", args...)
	return err == nil && virshDOMInfoMatchesUUID(out, f.TargetID)
}

func virshDOMInfoMatchesUUID(out []byte, want string) bool {
	matches := virshDOMInfoUUID.FindAllStringSubmatch(string(out), -1)
	return len(matches) == 1 && strings.EqualFold(matches[0][1], want)
}

func fencePowerOffOutput(out []byte) bool {
	return fencePowerState(out) == "off"
}

func fencePowerOnOutput(out []byte) bool {
	return fencePowerState(out) == "on"
}

func fencePowerState(out []byte) string {
	state := ""
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		current := ""
		if line == "status: on" || line == "power: on" || line == "power status: on" {
			current = "on"
		}
		if line == "status: off" || line == "power: off" || line == "power status: off" {
			current = "off"
		}
		if current != "" && state != "" && state != current {
			return ""
		}
		if current != "" {
			state = current
		}
	}
	return state
}
