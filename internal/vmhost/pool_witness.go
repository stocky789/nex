// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"nex.local/nex/internal/protocol"
)

const poolWitnessDropInPath = "/etc/systemd/system/corosync-qdevice.service.d/10-nex-witness.conf"

// PoolWitnessNSSPath is the durable, generation-scoped NSS database path supplied to the
// native qdevice daemon. A new generation is staged alongside the active database.
func PoolWitnessNSSPath(stateDir string, generation uint64) string {
	return filepath.Join(stateDir, "pool-witness", strconv.FormatUint(generation, 10), "nssdb")
}

func PoolWitnessBundle(stateDir string, generation uint64) ([]byte, error) {
	return os.ReadFile(filepath.Join(filepath.Dir(PoolWitnessNSSPath(stateDir, generation)), "qdevice-net-node.p12"))
}

func PoolWitnessCredentialsReady(ctx context.Context, stateDir, poolID string, witness protocol.PoolWitnessRecord) bool {
	if witness.Valid(poolID) != nil {
		return false
	}
	dir := PoolWitnessNSSPath(stateDir, witness.FencingGeneration)
	for _, name := range []string{"cert9.db", "key4.db"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	ca, err := clusterRun(ctx, "certutil", "-L", "-r", "-d", "sql:"+dir, "-n", "QNet CA")
	if err != nil {
		return false
	}
	cert, err := x509.ParseCertificate(ca)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(cert.Raw)
	if hex.EncodeToString(digest[:]) != witness.CASHA256 {
		return false
	}
	if _, err = clusterRun(ctx, "certutil", "-V", "-u", "V", "-d", "sql:"+dir, "-n", "Cluster Cert"); err != nil {
		return false
	}
	return true
}

func PoolWitnessCSR(ctx context.Context, stateDir, poolID string, witness protocol.PoolWitnessRecord) ([]byte, error) {
	if err := witness.Valid(poolID); err != nil {
		return nil, err
	}
	root := filepath.Dir(PoolWitnessNSSPath(stateDir, witness.FencingGeneration))
	nss := PoolWitnessNSSPath(stateDir, witness.FencingGeneration)
	csrPath := filepath.Join(root, "qdevice-net-node.crq")
	if csr, err := os.ReadFile(csrPath); err == nil {
		return canonicalPoolWitnessCSR(csr)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if _, err := os.Stat(nss); err == nil {
		return nil, errors.New("staged qdevice NSS database exists without its CSR")
	}
	if err := os.MkdirAll(nss, 0700); err != nil {
		return nil, err
	}
	pwd := filepath.Join(nss, "pwdfile.txt")
	noise := filepath.Join(nss, "noise.txt")
	ca := filepath.Join(root, "qnetd-ca.pem")
	for path, contents := range map[string][]byte{pwd: {}, noise: []byte("nex qdevice certificate request noise\n"), ca: []byte(witness.CAPEM)} {
		if err := atomicWrite(path, contents); err != nil {
			return nil, err
		}
	}
	if _, err := clusterRun(ctx, "certutil", "-N", "-d", "sql:"+nss, "-f", pwd); err != nil {
		return nil, err
	}
	if _, err := clusterRun(ctx, "certutil", "-A", "-d", "sql:"+nss, "-n", "QNet CA", "-t", "CT,c,c", "-i", ca, "-f", pwd); err != nil {
		return nil, err
	}
	if _, err := clusterRun(ctx, "certutil", "-R", "-a", "-d", "sql:"+nss, "-s", "CN="+witness.ClusterName, "-o", csrPath, "-f", pwd, "-z", noise); err != nil {
		return nil, err
	}
	csr, err := os.ReadFile(csrPath)
	if err != nil || len(csr) == 0 {
		return nil, errors.New("native qdevice CSR was not created")
	}
	return canonicalPoolWitnessCSR(csr)
}

func canonicalPoolWitnessCSR(csr []byte) ([]byte, error) {
	block, _ := pem.Decode(csr)
	if block == nil || (block.Type != "NEW CERTIFICATE REQUEST" && block.Type != "CERTIFICATE REQUEST") {
		return nil, errors.New("native qdevice CSR is invalid")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: block.Bytes}), nil
}

// PoolWitnessImportMaster installs the qnetd-signed certificate into the staged native NSS
// database and returns its matching PKCS#12 key bundle for sealed delivery to members.
func PoolWitnessImportMaster(ctx context.Context, stateDir, poolID string, witness protocol.PoolWitnessRecord, csrPEM, certificatePEM string) ([]byte, error) {
	if err := witness.Valid(poolID); err != nil {
		return nil, err
	}
	csrBlock, _ := pem.Decode([]byte(csrPEM))
	certBlock, _ := pem.Decode([]byte(certificatePEM))
	if csrBlock == nil || certBlock == nil || csrBlock.Type != "CERTIFICATE REQUEST" || certBlock.Type != "CERTIFICATE" {
		return nil, errors.New("qdevice request or signed certificate is invalid")
	}
	csr, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return nil, errors.New("qdevice certificate request signature is invalid")
	}
	parsedCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || parsedCert.Subject.CommonName != witness.ClusterName || !publicKeysEqual(csr.PublicKey, parsedCert.PublicKey) {
		return nil, errors.New("qdevice certificate does not match the pool request")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(witness.CAPEM)) {
		return nil, errors.New("qnetd CA certificate is invalid")
	}
	if _, err = parsedCert.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, errors.New("qdevice certificate is not signed by the pinned qnetd CA")
	}
	root := filepath.Dir(PoolWitnessNSSPath(stateDir, witness.FencingGeneration))
	nss := PoolWitnessNSSPath(stateDir, witness.FencingGeneration)
	certPath := filepath.Join(root, "cluster-certificate.pem")
	p12 := filepath.Join(root, "qdevice-net-node.p12")
	if bundle, readErr := os.ReadFile(p12); readErr == nil {
		if PoolWitnessCredentialsReady(ctx, stateDir, poolID, witness) {
			return bundle, nil
		}
		return nil, errors.New("staged qdevice database is incomplete")
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	if err := atomicWrite(certPath, []byte(certificatePEM)); err != nil {
		return nil, err
	}
	if _, err := clusterRun(ctx, "certutil", "-A", "-d", "sql:"+nss, "-n", "Cluster Cert", "-t", "u,u,u", "-i", certPath); err != nil {
		return nil, err
	}
	if _, err := clusterRun(ctx, "pk12util", "-o", p12, "-d", "sql:"+nss, "-W", "", "-n", "Cluster Cert"); err != nil {
		return nil, err
	}
	bundle, err := os.ReadFile(p12)
	if err != nil || len(bundle) == 0 {
		return nil, errors.New("native qdevice PKCS#12 bundle was not created")
	}
	return bundle, nil
}

func publicKeysEqual(a, b any) bool {
	left, err := x509.MarshalPKIXPublicKey(a)
	if err != nil {
		return false
	}
	right, err := x509.MarshalPKIXPublicKey(b)
	return err == nil && string(left) == string(right)
}

func PoolWitnessImportMember(ctx context.Context, stateDir, poolID string, witness protocol.PoolWitnessRecord, bundle []byte) error {
	return poolWitnessImportMember(ctx, stateDir, poolID, witness, bundle, poolWitnessDropInPath)
}

func poolWitnessImportMember(ctx context.Context, stateDir, poolID string, witness protocol.PoolWitnessRecord, bundle []byte, dropInPath string) error {
	if err := witness.Valid(poolID); err != nil {
		return err
	}
	root := filepath.Dir(PoolWitnessNSSPath(stateDir, witness.FencingGeneration))
	nss := PoolWitnessNSSPath(stateDir, witness.FencingGeneration)
	if _, err := os.Stat(nss); err == nil {
		if PoolWitnessCredentialsReady(ctx, stateDir, poolID, witness) {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		dropIn, err := os.ReadFile(dropInPath)
		if err == nil && strings.Contains(string(dropIn), "net_nss_db_dir="+nss+"\n") {
			return errors.New("active qdevice database failed credential validation")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.RemoveAll(nss); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(nss, 0700); err != nil {
		return err
	}
	pwd := filepath.Join(nss, "pwdfile.txt")
	ca := filepath.Join(root, "qnetd-ca.pem")
	p12 := filepath.Join(root, "qdevice-net-node.p12")
	if err := atomicWrite(pwd, nil); err != nil {
		return err
	}
	if err := atomicWrite(ca, []byte(witness.CAPEM)); err != nil {
		return err
	}
	if err := atomicWrite(p12, bundle); err != nil {
		return err
	}
	if _, err := clusterRun(ctx, "certutil", "-N", "-d", "sql:"+nss, "-f", pwd); err != nil {
		return err
	}
	if _, err := clusterRun(ctx, "certutil", "-A", "-d", "sql:"+nss, "-n", "QNet CA", "-t", "CT,c,c", "-i", ca, "-f", pwd); err != nil {
		return err
	}
	if _, err := clusterRun(ctx, "pk12util", "-i", p12, "-d", "sql:"+nss, "-W", ""); err != nil {
		return err
	}
	if !PoolWitnessCredentialsReady(ctx, stateDir, poolID, witness) {
		return errors.New("imported qdevice certificate does not match the pinned witness CA")
	}
	return nil
}

func ActivatePoolWitness(ctx context.Context, stateDir, poolID string, witness protocol.PoolWitnessRecord) error {
	if err := witness.Valid(poolID); err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(witness.Address); err != nil {
		return errors.New("pool witness address is invalid")
	}
	if !PoolWitnessCredentialsReady(ctx, stateDir, poolID, witness) {
		return errors.New("staged qdevice keys are unavailable")
	}
	unitDir := "/etc/systemd/system/corosync-qdevice.service.d"
	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return err
	}
	dropIn := fmt.Sprintf("[Service]\nExecStart=\nExecStart=/usr/sbin/corosync-qdevice -f -S net_nss_db_dir=%s\n", PoolWitnessNSSPath(stateDir, witness.FencingGeneration))
	path := poolWitnessDropInPath
	old, readErr := os.ReadFile(path)
	changed := readErr != nil || string(old) != dropIn
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if changed {
		if err := atomicWrite(path, []byte(dropIn)); err != nil {
			return err
		}
		if _, err := clusterRun(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
		_, err := clusterRun(ctx, "systemctl", "restart", "corosync-qdevice.service")
		return err
	}
	_, err := clusterRun(ctx, "systemctl", "start", "corosync-qdevice.service")
	return err
}

// DeactivatePoolWitness stops only a Nex-configured qdevice before membership or credential
// rotation can leave it voting with a certificate the authority no longer records.
func DeactivatePoolWitness(ctx context.Context) error {
	return deactivatePoolWitness(ctx, poolWitnessDropInPath)
}

func deactivatePoolWitness(ctx context.Context, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := clusterRun(ctx, "systemctl", "stop", "corosync-qdevice.service"); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err == nil {
		_, err = clusterRun(ctx, "systemctl", "daemon-reload")
		return err
	}
	return nil
}

type PoolWitnessStatus struct {
	Observed    bool
	Connected   bool
	TLSActive   bool
	VoteCast    bool
	ClusterName string
	QNetdHost   string
	Error       string
}

func ReadPoolWitnessStatus(ctx context.Context, stateDir, poolID string, witness protocol.PoolWitnessRecord) PoolWitnessStatus {
	if err := witness.Valid(poolID); err != nil {
		return PoolWitnessStatus{Error: "pool witness record is invalid"}
	}
	out, err := clusterRun(ctx, "corosync-qdevice-tool", "-s", "-v")
	if len(out) == 0 {
		return PoolWitnessStatus{Error: "qdevice status is unavailable"}
	}
	status := parsePoolWitnessStatus(out)
	if err != nil || !status.Connected || !status.TLSActive || !status.VoteCast || status.ClusterName != witness.ClusterName || status.QNetdHost != witness.Address {
		status.Observed = false
		status.Error = "qdevice is not connected with its pinned witness vote"
		return status
	}
	ca, err := clusterRun(ctx, "certutil", "-L", "-r", "-d", "sql:"+PoolWitnessNSSPath(stateDir, witness.FencingGeneration), "-n", "QNet CA")
	if err != nil {
		status.Observed = false
		status.Error = "qdevice CA pin could not be verified"
		return status
	}
	cert, err := x509.ParseCertificate(ca)
	if err != nil {
		status.Observed = false
		status.Error = "qdevice CA pin could not be verified"
		return status
	}
	digest := sha256.Sum256(cert.Raw)
	if hex.EncodeToString(digest[:]) != witness.CASHA256 {
		status.Observed = false
		status.Error = "qdevice is using a different witness CA"
		return status
	}
	status.Observed = true
	return status
}

func parsePoolWitnessStatus(out []byte) PoolWitnessStatus {
	status := PoolWitnessStatus{}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "State":
			status.Connected = strings.EqualFold(strings.TrimSpace(value), "Connected")
		case "TLS active":
			status.TLSActive = strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), "yes")
		case "Poll timer running":
			status.VoteCast = strings.Contains(strings.ToLower(value), "yes") && strings.Contains(strings.ToLower(value), "cast vote")
		case "Cluster name":
			status.ClusterName = strings.TrimSpace(value)
		case "QNetd host":
			status.QNetdHost = strings.TrimSpace(value)
		}
	}
	if status.Connected && status.TLSActive && status.VoteCast {
		status.Observed = true
	} else {
		status.Error = "qdevice is not connected with its pinned witness vote"
	}
	return status
}
