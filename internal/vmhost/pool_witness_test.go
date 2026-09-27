// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestPoolWitnessNativeCredentialLifecycle(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "QNet CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	hash := sha256.Sum256(der)
	witness := protocol.PoolWitnessRecord{Kind: "controller", Address: "controller.example.test:5403", CAPEM: ca, CASHA256: hex.EncodeToString(hash[:]), FencingGeneration: 4, ClusterName: protocol.PoolWitnessClusterName("pool-a")}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: witness.ClusterName}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	parsedCSR, _ := x509.ParseCertificateRequest(csrDER)
	clientTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: witness.ClusterName}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTemplate, tmpl, parsedCSR.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	clientCertPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: clientDER}))
	stateDir := t.TempDir()
	memberDir := filepath.Join(stateDir, "member")
	previous := clusterRun
	failMemberImport := true
	memberImported := false
	clusterRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "pk12util" && hasWitnessTestArg(args, "-i") {
			if failMemberImport {
				failMemberImport = false
				return nil, errors.New("interrupted member import")
			}
			memberImported = true
		}
		if name == "certutil" && hasWitnessTestArg(args, "-V") && strings.Contains(strings.Join(args, " "), memberDir) && !memberImported {
			return nil, errors.New("member certificate missing")
		}
		if name == "certutil" && hasWitnessTestArg(args, "-L") {
			return der, nil
		}
		if name == "certutil" && hasWitnessTestArg(args, "-N") {
			for i, arg := range args {
				if arg == "-d" && i+1 < len(args) {
					dir := strings.TrimPrefix(args[i+1], "sql:")
					if err := os.WriteFile(filepath.Join(dir, "cert9.db"), []byte("db"), 0600); err != nil {
						return nil, err
					}
					if err := os.WriteFile(filepath.Join(dir, "key4.db"), []byte("key"), 0600); err != nil {
						return nil, err
					}
				}
			}
		}
		for i, arg := range args {
			if arg == "-o" && i+1 < len(args) {
				contents := []byte("native output")
				if hasWitnessTestArg(args, "-R") {
					contents = []byte("Certificate request generated\n" + strings.Replace(csrPEM, "CERTIFICATE REQUEST", "NEW CERTIFICATE REQUEST", 2))
				}
				return nil, os.WriteFile(args[i+1], contents, 0600)
			}
		}
		return nil, nil
	}
	t.Cleanup(func() { clusterRun = previous })
	csrBytes, err := PoolWitnessCSR(context.Background(), stateDir, "pool-a", witness)
	if err != nil || string(csrBytes) != csrPEM {
		t.Fatalf("CSR = %q, %v", csrBytes, err)
	}
	if _, err = PoolWitnessCSR(context.Background(), stateDir, "pool-a", witness); err != nil {
		t.Fatalf("retry CSR = %v", err)
	}
	bundle, err := PoolWitnessImportMaster(context.Background(), stateDir, "pool-a", witness, csrPEM, clientCertPEM)
	if err != nil || string(bundle) != "native output" {
		t.Fatalf("PKCS#12 = %q, %v", bundle, err)
	}
	if err = PoolWitnessImportMember(context.Background(), memberDir, "pool-a", witness, bundle); err == nil {
		t.Fatal("member import did not exercise interrupted native import")
	}
	if PoolWitnessCredentialsReady(context.Background(), memberDir, "pool-a", witness) {
		t.Fatal("interrupted member import was reported ready")
	}
	if err = PoolWitnessImportMember(context.Background(), memberDir, "pool-a", witness, bundle); err != nil {
		t.Fatal(err)
	}
	activeDropIn := filepath.Join(stateDir, "active-qdevice.conf")
	if err = os.WriteFile(activeDropIn, []byte("net_nss_db_dir="+PoolWitnessNSSPath(memberDir, witness.FencingGeneration)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(PoolWitnessNSSPath(memberDir, witness.FencingGeneration), "active-sentinel")
	if err = os.WriteFile(sentinel, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	memberImported = false // Simulate a transient native validation failure on an active generation.
	if err = poolWitnessImportMember(context.Background(), memberDir, "pool-a", witness, bundle, activeDropIn); err == nil {
		t.Fatal("active qdevice database was rebuilt after a transient readiness failure")
	}
	if contents, readErr := os.ReadFile(sentinel); readErr != nil || string(contents) != "unchanged" {
		t.Fatalf("active qdevice database was changed: %q, %v", contents, readErr)
	}
}

func TestPoolWitnessRejectsWrongPoolBeforeNativeCommands(t *testing.T) {
	witness := protocol.PoolWitnessRecord{Kind: "controller", Address: "controller.example.test:5403", CAPEM: "bad", CASHA256: "bad", FencingGeneration: 4, ClusterName: protocol.PoolWitnessClusterName("pool-a")}
	if _, err := PoolWitnessCSR(context.Background(), t.TempDir(), "pool-b", witness); err == nil {
		t.Fatal("accepted a witness registration for another pool")
	}
}

func TestDeactivatePoolWitnessWithoutOwnedServiceDoesNotCallSystemctl(t *testing.T) {
	previous := clusterRun
	clusterRun = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("systemctl called without a Nex-owned qdevice drop-in")
		return nil, nil
	}
	t.Cleanup(func() { clusterRun = previous })
	if err := deactivatePoolWitness(context.Background(), filepath.Join(t.TempDir(), "missing.conf")); err != nil {
		t.Fatal(err)
	}
}

func TestPoolWitnessStatusRequiresTLSAndCastVote(t *testing.T) {
	connected := []byte("State: Connected\nTLS active: Yes (client certificate sent)\nPoll timer running: Yes (cast vote)\nCluster name: cluster-a\nQNetd host: controller.example.test:5403\n")
	status := parsePoolWitnessStatus(connected)
	if !status.Observed || !status.Connected || !status.TLSActive || !status.VoteCast {
		t.Fatalf("connected voting witness rejected: %#v", status)
	}
	for _, output := range [][]byte{
		[]byte("State: Connected\nTLS active: Yes\nPoll timer running: Yes (no vote)\n"),
		[]byte("State: Connected\nTLS active: No\nPoll timer running: Yes (cast vote)\n"),
		[]byte("State: Disconnected\nTLS active: No\nPoll timer running: No\n"),
	} {
		if got := parsePoolWitnessStatus(output); got.Observed || got.Error == "" {
			t.Fatalf("unsafe qdevice status accepted: %#v", got)
		}
	}
}

func TestReadPoolWitnessStatusRequestsVerboseNativeOutput(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "QNet CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(der)
	witness := protocol.PoolWitnessRecord{Kind: "controller", Address: "controller.example.test:5403", CAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), CASHA256: hex.EncodeToString(hash[:]), FencingGeneration: 2, ClusterName: protocol.PoolWitnessClusterName("pool-a")}
	nativeClusterName, nativeAddress, nativeCA := witness.ClusterName, witness.Address, der
	previous := clusterRun
	clusterRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "certutil" {
			return nativeCA, nil
		}
		if name != "corosync-qdevice-tool" || !hasWitnessTestArg(args, "-s") {
			t.Fatalf("unexpected native command: %s %v", name, args)
		}
		// Debian 13 only includes TLS and vote fields in verbose status.
		status := "Cluster name:\t" + nativeClusterName + "\nQNetd host:\t" + nativeAddress + "\nState:\tConnected\n"
		if hasWitnessTestArg(args, "-v") {
			status += "Poll timer running:\tYes (cast vote)\nTLS active:\tYes (client certificate sent)\n"
		}
		return []byte(status), nil
	}
	t.Cleanup(func() { clusterRun = previous })
	if status := ReadPoolWitnessStatus(context.Background(), t.TempDir(), "pool-a", witness); !status.Observed || status.Error != "" {
		t.Fatalf("connected native qdevice was reported unready: %#v", status)
	}
	nativeClusterName = "wrong-cluster"
	if status := ReadPoolWitnessStatus(context.Background(), t.TempDir(), "pool-a", witness); status.Observed || status.Error == "" {
		t.Fatalf("wrong cluster was reported ready: %#v", status)
	}
	nativeClusterName, nativeAddress = witness.ClusterName, "other.example.test:5403"
	if status := ReadPoolWitnessStatus(context.Background(), t.TempDir(), "pool-a", witness); status.Observed || status.Error == "" {
		t.Fatalf("wrong witness address was reported ready: %#v", status)
	}
	nativeAddress = witness.Address
	otherCA := *ca
	otherCA.SerialNumber = big.NewInt(2)
	nativeCA, err = x509.CreateCertificate(rand.Reader, &otherCA, &otherCA, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if status := ReadPoolWitnessStatus(context.Background(), t.TempDir(), "pool-a", witness); status.Observed || status.Error == "" {
		t.Fatalf("wrong witness CA was reported ready: %#v", status)
	}
}

func hasWitnessTestArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
