// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestPoolWitnessClusterNameAndRecordValidation(t *testing.T) {
	nameDigest := sha256.Sum256([]byte("nex-pool-cluster\x00pool-a"))
	if got := PoolWitnessClusterName("pool-a"); got != "nexpool"+hex.EncodeToString(nameDigest[:16]) {
		t.Fatalf("cluster name = %q", got)
	}
	// These UUIDs have the same first 32 digest bits under the old name scheme.
	first := "00000000-0000-4000-8000-000000003774"
	second := "00000000-0000-4000-8000-000000007845"
	firstDigest := sha256.Sum256([]byte("nex-pool-cluster\x00" + first))
	secondDigest := sha256.Sum256([]byte("nex-pool-cluster\x00" + second))
	if hex.EncodeToString(firstDigest[:4]) != hex.EncodeToString(secondDigest[:4]) {
		t.Fatal("test UUIDs no longer reproduce the old 32-bit collision")
	}
	if PoolWitnessClusterName(first) == PoolWitnessClusterName(second) {
		t.Fatal("distinct pools share a witness cluster name")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	caHash := sha256.Sum256(der)
	record := PoolWitnessRecord{Kind: "controller", Address: "controller.example.test:5403", CAPEM: caPEM, CASHA256: hex.EncodeToString(caHash[:]), FencingGeneration: 3, ClusterName: PoolWitnessClusterName("pool-a")}
	if err = record.Valid("pool-a"); err != nil {
		t.Fatal(err)
	}
	record.CASHA256 = "bad"
	if err = record.Valid("pool-a"); err == nil {
		t.Fatal("accepted a CA pin that does not match the certificate")
	}
}

func TestPoolWitnessCandidateAllowsUnusablePoolMaster(t *testing.T) {
	if err := (PoolWitnessCandidate{Kind: "pool-master", PoolID: "pool-b", HostID: "host-b", Address: "core.example.test:5403"}).Valid(); err != nil {
		t.Fatal(err)
	}
	if err := (PoolWitnessCandidate{Kind: "pool_master", PoolID: "pool-b", HostID: "host-b", Address: "core.example.test:5403"}).Valid(); err == nil {
		t.Fatal("accepted inconsistent pool-master candidate kind")
	}
}
