// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// A BMC answers at an address, and an address is not an identity: a rebuilt rack, a reused
// DHCP lease, a re-issued DNS record or a typed digit can point a fence at a machine that is
// running somebody else's workload. Two things bind a fence to one machine here.
//
// The certificate. A management controller ships a self-signed certificate with no usable
// name, so a chain proves nothing and skipping verification would send the password to
// whoever answers. Core therefore captures the exact certificate when the profile is
// recorded, keeps its SHA-256 in the profile on the pool master, and refuses every later
// connection that presents anything else. That is also what binds the identity read and the
// power-off to one endpoint: both go through the same pinned connection, so a name that
// resolves elsewhere between them cannot separate them.
//
// The machine. Over that connection Core asks the controller which system it manages — the
// Redfish ComputerSystem.UUID, or IPMI Get System GUID — and compares it with the SMBIOS
// system UUID the target host itself reported while it was alive. Nothing is powered off
// unless the two agree, and an unreadable answer is a refusal, never a pass.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

const bmcTimeout = 30 * time.Second

var ipmiSystemGUID = regexp.MustCompile(`(?im)^\s*System GUID\s*:\s*([0-9a-fA-F-]+)\s*$`)

// bmcCertificateChanged is its own sentence because it is not a network fault: the endpoint
// answered, and it is not the one this profile was recorded against.
var errBMCCertificateChanged = errors.New("this management controller presented a different certificate than the one recorded for it; record its access again after confirming the new certificate on the controller itself")

// placeholderSystemUUIDs are identities firmware writes when it has none to write. They are
// not machine identities: whole production batches ship reporting the same one, and two hosts
// that report the same UUID each satisfy the other's identity check.
var placeholderSystemUUIDs = map[string]bool{
	"00000000000000000000000000000000": true,
	"ffffffffffffffffffffffffffffffff": true,
	"03000200040005000006000700080009": true,
}

// LocalSystemUUID is this host's own SMBIOS system UUID, as the kernel exposes it. A host
// reports it to its pool master, which keeps it in the membership record.
func LocalSystemUUID() (string, error) {
	b, err := os.ReadFile("/sys/class/dmi/id/product_uuid")
	if err != nil {
		return "", errors.New("this host does not expose an SMBIOS system UUID")
	}
	uuid := NormalizedSystemUUID(string(b))
	if uuid == "" {
		return "", errors.New("this host does not expose an SMBIOS system UUID")
	}
	return uuid, nil
}

// NormalizedSystemUUID returns the 32 lower-case hex digits of a UUID, or "" if the input is
// not one or is a known firmware placeholder. It is the one spelling every record, report and
// comparison uses.
func NormalizedSystemUUID(raw string) string {
	digits := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), "-", ""))
	if len(digits) != 32 || placeholderSystemUUIDs[digits] {
		return ""
	}
	if _, err := hex.DecodeString(digits); err != nil {
		return ""
	}
	return digits
}

// reversedSystemUUID is the same 16 bytes the other way round. IPMI *Get System GUID* is
// documented least-significant-byte first while SMBIOS records it most-significant-byte
// first, and implementations differ over which they send, so the whole-word reversal of an
// IPMI GUID is the same machine. Redfish reports an RFC 4122 string with one spelling and is
// deliberately compared exactly.
func reversedSystemUUID(uuid string) string {
	b, err := hex.DecodeString(uuid)
	if err != nil {
		return ""
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return hex.EncodeToString(b)
}

// VerifyBMCTargetIdentity is one identity read for the one caller outside this package: the
// pool master, proving a management controller before it records it.
func VerifyBMCTargetIdentity(ctx context.Context, stateDir string, f protocol.StorageClusterFence, recorded string) error {
	b, err := newBMCSession(stateDir, f)
	if err != nil {
		return err
	}
	defer b.close()
	return b.targetIdentity(ctx, recorded)
}

// FencePasswordScript is the one format Nex Core keeps a fence account's password in, on disk
// and sealed inside a member's authority replica. It is a shell script because fence_ipmilan
// and fence_redfish take a password no other way that keeps it out of argv, and the fence
// agent is the only thing that ever executes it: Nex Core itself parses it, below.
func FencePasswordScript(secret string) []byte {
	return append([]byte(fencePasswordPrefix+strings.ReplaceAll(secret, "'", `'"'"'`)), '\'', '\n')
}

const fencePasswordPrefix = "#!/bin/sh\nprintf '%s\\n' '"

// FenceCredentialSecretFromScript reads that format back. Parsing rather than running it is
// what makes it safe to open another host's credential on every host every few minutes: a
// member opens the copy sealed inside its own authority replica, and executing those bytes
// would give whoever wrote them a root shell on every member of the pool. Round-tripping is
// the whole validation — anything that does not re-encode byte for byte is not something
// FencePasswordScript wrote, and is refused rather than interpreted.
func FenceCredentialSecretFromScript(script []byte) (string, error) {
	body, ok := strings.CutPrefix(string(script), fencePasswordPrefix)
	secret := strings.ReplaceAll(strings.TrimSuffix(body, "'\n"), `'"'"'`, "'")
	if !ok || secret == "" || !bytes.Equal(script, FencePasswordScript(secret)) {
		return "", errors.New("this host holds no fence credential for that management controller")
	}
	return secret, nil
}

// FenceSecretUsable refuses a fence account password the fence agent would not send as Nex
// Core recorded it. fence_ipmilan runs the script above and strips the whitespace around what
// it printed, so a password with a leading or trailing space authenticates every check Core
// makes itself and fails the one power-off that matters — the host reads "checked from 2
// hosts" and cannot actually be fenced. Core refuses it while the operator is still there to
// correct it.
func FenceSecretUsable(secret string) error {
	if secret == "" || strings.TrimSpace(secret) != secret {
		return errors.New("a fence account password may not begin or end with a space, a tab or a line break: the fence agent strips them and would sign in with a different password")
	}
	return nil
}

// bmcSession is one fence operation's whole conversation with one management controller: the
// password read from the credential store once, and — for Redfish — a single pinned transport
// that the identity read, the power-off and the settle poll all share instead of opening and
// abandoning a TLS connection per call.
type bmcSession struct {
	fence  protocol.StorageClusterFence
	secret string
	client *http.Client // Redfish only: IPMI has no certificate and authenticates its own session.
}

func newBMCSession(stateDir string, f protocol.StorageClusterFence) (*bmcSession, error) {
	secret, err := fenceCredentialSecret(stateDir, f.CredentialRef)
	if err != nil {
		return nil, err
	}
	return openBMCSession(f, secret)
}

// openBMCSession is the same session for a caller that already holds the password: a
// continuous check on a member, which opens the sealed copy in its replica in memory.
func openBMCSession(f protocol.StorageClusterFence, secret string) (*bmcSession, error) {
	b := &bmcSession{fence: f, secret: secret}
	if f.Kind == protocol.FenceRedfish {
		var err error
		if b.client, err = bmcClient(f); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *bmcSession) close() {
	if b.client != nil {
		b.client.CloseIdleConnections()
	}
}

// targetIdentity refuses unless the controller named by the profile — proved by its pinned
// certificate — names the exact machine the master recorded. Its refusals are the operator's
// own sentences: they are reported on a proof and shown in the UI.
func (b *bmcSession) targetIdentity(ctx context.Context, recorded string) error {
	recorded = NormalizedSystemUUID(recorded)
	if recorded == "" {
		return errors.New("this host does not report a unique hardware identity, so a management controller cannot be confirmed to be its own")
	}
	if b.fence.Kind == protocol.FenceRedfish {
		reported, _, err := b.system(ctx)
		if err != nil {
			return err
		}
		if reported != recorded {
			return errBMCWrongMachine
		}
		return nil
	}
	reported, err := b.ipmiSystemUUID(ctx)
	if err != nil {
		return err
	}
	if reported != recorded && reversedSystemUUID(reported) != recorded {
		return errBMCWrongMachine
	}
	return nil
}

var errBMCWrongMachine = errors.New("the management controller at this address reports a different machine than the host it is recorded for; nothing was powered off")

// ReadFenceCredentialSecret is fenceCredentialSecret for the one caller outside this package:
// a host offering the pool master an account it created, with the password it set, again.
func ReadFenceCredentialSecret(stateDir, ref string) (string, error) {
	return fenceCredentialSecret(stateDir, ref)
}

// fenceCredentialSecret reads the private password file the fence-credential store holds for
// this profile. The value is used in this process only: it is never logged, never put in a
// command argument and never returned to an API.
func fenceCredentialSecret(stateDir, ref string) (string, error) {
	path, err := fenceCredentialScript(stateDir, ref)
	if err != nil {
		return "", errors.New("this host holds no fence credential for that management controller")
	}
	script, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("this host holds no fence credential for that management controller")
	}
	return FenceCredentialSecretFromScript(script)
}

// bmcClient talks to exactly one certificate. Chain and name verification are off because a
// factory controller certificate satisfies neither; the pin below replaces both, and it is
// stricter: any certificate but that one is refused, including a well-formed one from a
// public authority. Redirects are never followed — a controller that sends the identity read
// somewhere else would have Core read one machine's UUID and power another off.
func bmcClient(f protocol.StorageClusterFence) (*http.Client, error) {
	want, err := hex.DecodeString(f.TLSFingerprint)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("no certificate is recorded for this management controller; record its access again")
	}
	return &http.Client{
		Timeout:       bmcTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 — replaced by the exact-certificate pin below.
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errBMCCertificateChanged
				}
				got := sha256.Sum256(raw[0])
				if !bytes.Equal(got[:], want) {
					return errBMCCertificateChanged
				}
				return nil
			},
		}},
	}, nil
}

// BMCCertificateFingerprint is the SHA-256 of the certificate this controller presents now.
// It is read once, when a profile is recorded, and pinned from then on. This is the one
// moment the endpoint is not yet proved, which is why the fingerprint is reported to the
// operator: they can compare it with what the controller's own page shows.
func BMCCertificateFingerprint(ctx context.Context, endpoint string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, bmcTimeout)
	defer cancel()
	dialer := tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} // #nosec G402 — this call exists to read the certificate, not to trust it.
	conn, err := dialer.DialContext(ctx, "tcp", bmcAddress(endpoint))
	if err != nil {
		return "", errBMCNoAnswer
	}
	defer conn.Close()
	state := conn.(*tls.Conn).ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return "", errBMCNoAnswer
	}
	sum := sha256.Sum256(state.PeerCertificates[0].Raw)
	return hex.EncodeToString(sum[:]), nil
}

// powerState reads the machine's power state and asks for no change to it. Redfish reads the
// same ComputerSystem resource the identity came from, over the connection already pinned to
// that controller's certificate; IPMI asks its own chassis command over the session the
// identity was proved on, with the password in the environment exactly as that read does.
func (b *bmcSession) powerState(ctx context.Context) (string, error) {
	if b.fence.Kind == protocol.FenceRedfish {
		_, power, err := b.system(ctx)
		return power, err
	}
	f := b.fence
	out, err := storageFenceEnvRun(ctx, []string{"IPMI_PASSWORD=" + b.secret}, "", "ipmitool",
		append(ipmiCipherArgs(f), "-I", "lanplus", "-H", f.Endpoint, "-U", f.Username, "-E", "chassis", "power", "status")...)
	if err != nil {
		return "", errBMCNoAnswer
	}
	// "Chassis Power is on" is ipmitool's own wording; the fence agents' wording is read by
	// fencePowerState, which this deliberately does not reuse for a different tool.
	switch answer := strings.ToLower(strings.TrimSpace(string(out))); {
	case strings.HasSuffix(answer, "is on"):
		return "on", nil
	case strings.HasSuffix(answer, "is off"):
		return "off", nil
	}
	return "", nil
}

var errNoIPMITool = errors.New("this Nex Core has no ipmitool, so it cannot ask an IPMI controller which machine it manages; use a Redfish fence device or upgrade this host")
var errBMCNoAnswer = errors.New("this management controller did not answer, so the machine behind it could not be identified")
var errBMCNoReport = errors.New("this management controller did not report the machine it manages")
var errBMCRejected = errors.New("this management controller rejected the recorded account, so the machine behind it could not be identified")

// ipmiCipherArgs names the RMCP+ cipher suite of a recorded profile. Suite 0 asks a
// controller for no authentication, no integrity and no confidentiality, so it is never
// named here and never accepted into a profile; an unrecorded suite leaves the choice to
// ipmitool's own default, which is what a profile recorded before Core selected one has.
func ipmiCipherArgs(f protocol.StorageClusterFence) []string {
	if f.Cipher == 0 {
		return nil
	}
	return []string{"-C", strconv.Itoa(int(f.Cipher))}
}

// bmcAddress defaults an endpoint with no port to HTTPS, which is what every Redfish service
// listens on and what the certificate read must reach.
func bmcAddress(endpoint string) string {
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		return net.JoinHostPort(endpoint, "443")
	}
	return endpoint
}

// request performs one authenticated call over the pinned connection.
func (b *bmcSession) request(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	f := b.fence
	target := &url.URL{Scheme: "https", Host: f.Endpoint, Path: path}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, errors.New("this management controller address is not usable")
	}
	req.SetBasicAuth(f.Username, b.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		if errors.Is(err, errBMCCertificateChanged) {
			return nil, errBMCCertificateChanged
		}
		return nil, errBMCNoAnswer
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		return nil, errBMCRejected
	}
	return resp, nil
}

// system reads the one resource that answers both questions this fence asks: which machine
// the controller manages, and whether that machine is powered on.
func (b *bmcSession) system(ctx context.Context) (uuid, power string, err error) {
	resp, err := b.request(ctx, http.MethodGet, "/redfish/v1/Systems/"+b.fence.TargetID, nil)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", errBMCNoReport
	}
	var system struct {
		UUID       string `json:"UUID"`
		PowerState string `json:"PowerState"`
	}
	// A Redfish ComputerSystem is a few kilobytes; the cap stops a hostile endpoint from
	// streaming forever into this decode.
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&system) != nil {
		return "", "", errBMCNoReport
	}
	uuid = NormalizedSystemUUID(system.UUID)
	if uuid == "" {
		return "", "", errBMCNoReport
	}
	return uuid, strings.ToLower(system.PowerState), nil
}

// forceOff cuts the power. ForceOff is the only reset type that excludes a host: a graceful
// shutdown is a request the guest operating system can ignore.
//
// A refused Reset is deliberately not a failure. Several controllers answer 409 or 400 to
// ForceOff for a machine that is already off, and a machine that is already off is exactly
// the case a pool fence exists for. What decides is settled() below: Core's own reads of the
// controller's PowerState, never the POST's answer about itself.
func (b *bmcSession) forceOff(ctx context.Context) error {
	resp, err := b.request(ctx, http.MethodPost,
		"/redfish/v1/Systems/"+b.fence.TargetID+"/Actions/ComputerSystem.Reset", []byte(`{"ResetType":"ForceOff"}`))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// bmcSettleInterval and bmcSettleTimeout bound the wait for a machine to actually go off.
// ComputerSystem.Reset is asynchronous — a controller answers immediately and goes on
// reporting PowerState "On" for seconds — so a single read straight after the POST would
// report a working fence as uncertain and leave a promotion stuck. The interval is a package
// variables so a test can run the same loop without the wait.
var bmcSettleInterval = 2 * time.Second
var bmcSettleTimeout = 30 * time.Second

// settled is the separate observation that makes a proof: it reads the controller's own
// PowerState over the pinned connection until the machine is off or the bound expires.
func (b *bmcSession) settled(ctx context.Context) error {
	deadline := time.Now().Add(bmcSettleTimeout) // Monotonic: a clock step cannot shorten it.
	for {
		_, power, err := b.system(ctx)
		if err != nil {
			return err
		}
		if power == "off" {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("fence device did not observe target powered off")
		}
		select {
		case <-ctx.Done():
			return errors.New("fence device did not observe target powered off")
		case <-time.After(bmcSettleInterval):
		}
	}
}

// ipmiSystemUUID asks for the IPMI *Get System GUID*. IPMI has no TLS: RMCP+ authenticates
// the session itself, so there is no certificate to pin here. The password goes through the
// environment because an argument would be readable by every process on this host.
func (b *bmcSession) ipmiSystemUUID(ctx context.Context) (string, error) {
	f := b.fence
	if _, err := exec.LookPath("ipmitool"); err != nil {
		return "", errNoIPMITool
	}
	out, err := storageFenceEnvRun(ctx, []string{"IPMI_PASSWORD=" + b.secret}, "", "ipmitool",
		append(ipmiCipherArgs(f), "-I", "lanplus", "-H", f.Endpoint, "-U", f.Username, "-E", "mc", "guid")...)
	if err != nil {
		return "", errBMCNoAnswer
	}
	match := ipmiSystemGUID.FindAllStringSubmatch(string(out), -1)
	if len(match) != 1 {
		return "", errBMCNoReport
	}
	uuid := NormalizedSystemUUID(match[0][1])
	if uuid == "" {
		return "", errBMCNoReport
	}
	return uuid, nil
}
