// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPoolWitnessRegistrationRetryRotationAndReplicaRetry(t *testing.T) {
	s := testStore(t)
	ca1, pin1 := witnessTestCA(t, "qnetd one")
	ca2, pin2 := witnessTestCA(t, "qnetd two")
	var candidateCA, candidatePin = ca1, pin1
	registerCalls := 0
	registrations := []protocol.PoolWitnessRegistrationRequest{}
	wrongResponsePin := false
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case poolWitnessCandidatesPath:
			json.NewEncoder(w).Encode(protocol.PoolWitnessCandidates{Candidates: []protocol.PoolWitnessCandidate{
				{Kind: "pool-master", PoolID: "pool-other", HostID: "other-master", Address: "192.0.2.10:5403"},
				{Kind: "controller", CAPEM: candidateCA, CASHA256: candidatePin},
			}})
		case poolWitnessRegisterPath:
			var request protocol.PoolWitnessRegistrationRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			registerCalls++
			registrations = append(registrations, request)
			if (request.FencingGeneration == 3 && registerCalls == 1) || request.FencingGeneration == 4 && registerCalls == 3 {
				w.WriteHeader(http.StatusConflict)
				return
			}
			responseCA, responsePin := candidateCA, candidatePin
			if wrongResponsePin {
				responseCA, responsePin = ca2, pin2
			}
			json.NewEncoder(w).Encode(protocol.PoolWitnessRegistration{CertificatePEM: "signed-cert", CAPEM: responseCA, CASHA256: responsePin})
		default:
			http.NotFound(w, r)
		}
	}))
	server.StartTLS()
	defer server.Close()
	_, _, clientCert, clientKey, err := newPoolCA("master")
	if err != nil {
		t.Fatal(err)
	}
	s.State = State{Controller: server.URL, TrustPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})),
		HostID: "master", PoolID: "pool-1", CertificatePEM: clientCert, KeyPEM: clientKey}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	membership := protocol.PoolMembership{PoolID: s.State.PoolID, MasterHostID: s.State.HostID, Generation: 3,
		Members: []protocol.PoolMember{{HostID: "master", CertificateSHA256: strings.Repeat("a", 64), Generation: 3, Status: "ready"},
			{HostID: "member", CertificateSHA256: strings.Repeat("b", 64), Generation: 3, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}

	previousCSR, previousReady, previousBundle := poolWitnessCSR, poolWitnessCredentialsReady, poolWitnessBundle
	previousImport, previousPush := poolWitnessImportMaster, pushPoolWitnessAuthority
	t.Cleanup(func() {
		poolWitnessCSR, poolWitnessCredentialsReady, poolWitnessBundle = previousCSR, previousReady, previousBundle
		poolWitnessImportMaster, pushPoolWitnessAuthority = previousImport, previousPush
	})
	csrs := map[uint64]string{}
	poolWitnessCSR = func(_ context.Context, _, _ string, witness protocol.PoolWitnessRecord) ([]byte, error) {
		if csrs[witness.FencingGeneration] == "" {
			csrs[witness.FencingGeneration] = "native-pem-csr-generation-" + big.NewInt(int64(witness.FencingGeneration)).String()
		}
		return []byte(csrs[witness.FencingGeneration]), nil
	}
	imported := map[uint64]bool{}
	poolWitnessCredentialsReady = func(_ context.Context, _, _ string, w protocol.PoolWitnessRecord) bool {
		return imported[w.FencingGeneration]
	}
	poolWitnessBundle = func(_ string, generation uint64) ([]byte, error) {
		if !imported[generation] {
			return nil, os.ErrNotExist
		}
		return []byte("native-pkcs12-" + big.NewInt(int64(generation)).String()), nil
	}
	poolWitnessImportMaster = func(_ context.Context, _, _ string, w protocol.PoolWitnessRecord, csr, cert string) ([]byte, error) {
		if csr != csrs[w.FencingGeneration] || cert == "" {
			t.Fatalf("import did not use the persisted CSR: gen=%d csr=%q cert=%q", w.FencingGeneration, csr, cert)
		}
		imported[w.FencingGeneration] = true
		return []byte("native-pkcs12"), nil
	}
	pushes := 0
	poolWitnessPushErr := true
	pushPoolWitnessAuthority = func(context.Context, *Store, time.Duration) error {
		pushes++
		if poolWitnessPushErr {
			poolWitnessPushErr = false
			return errors.New("member unavailable")
		}
		return nil
	}

	if err = ReconcilePoolWitness(context.Background(), s); err == nil {
		t.Fatal("Controller's stale reported membership was accepted")
	}
	pending, err := Membership(s)
	if err != nil || pending.Witness == nil || pending.Witness.CredentialsReady || pending.Generation != 3 {
		t.Fatalf("pending registration was not durably recorded at the reported generation: %#v (%v)", pending, err)
	}
	if err = ReconcilePoolWitness(context.Background(), s); err == nil || err.Error() != "member unavailable" {
		t.Fatalf("registration retry/import delivery result = %v", err)
	}
	ready, err := Membership(s)
	if err != nil || ready.Witness == nil || !ready.Witness.CredentialsReady || ready.Generation != 3 {
		t.Fatalf("native credentials were not durably marked ready at stable membership generation: %#v (%v)", ready, err)
	}
	if ready.Witness.Address != "127.0.0.1:5403" {
		t.Fatalf("witness address was not derived from the pinned Controller URL: %q", ready.Witness.Address)
	}
	if len(registrations) != 2 || registrations[0].CSRPEM != registrations[1].CSRPEM || registrations[0].FencingGeneration != registrations[1].FencingGeneration {
		t.Fatalf("Controller retry changed the CSR or generation: %#v", registrations)
	}
	if err = ReconcilePoolWitness(context.Background(), s); err != nil {
		t.Fatalf("replica delivery did not resume after the ready-marker/push interruption: %v", err)
	}
	if pushes != 2 || registerCalls != 2 {
		t.Fatalf("retry duplicated Controller registration or missed replica delivery: pushes=%d registrations=%d", pushes, registerCalls)
	}

	// A replacement CA advances the membership generation and clears readiness before
	// registering. The 409 models Controller report lag; the subsequent retry is at gen 4.
	candidateCA, candidatePin = ca2, pin2
	if err = ReconcilePoolWitness(context.Background(), s); err == nil {
		t.Fatal("CA replacement registration ignored Controller report lag")
	}
	pending, err = Membership(s)
	if err != nil || pending.Generation != 4 || pending.Witness == nil || pending.Witness.FencingGeneration != 4 || pending.Witness.CredentialsReady {
		t.Fatalf("CA rotation did not invalidate old credentials at a new generation: %#v (%v)", pending, err)
	}
	for _, member := range pending.Members {
		if member.Generation != 4 {
			t.Fatalf("CA rotation left member %s at stale generation %d", member.HostID, member.Generation)
		}
	}
	if err = ReconcilePoolWitness(context.Background(), s); err != nil {
		t.Fatalf("replacement CA registration retry: %v", err)
	}
	ready, err = Membership(s)
	if err != nil || ready.Generation != 4 || ready.Witness == nil || !ready.Witness.CredentialsReady {
		t.Fatalf("replacement credentials were not ready: %#v (%v)", ready, err)
	}
	if registrations[2].CSRPEM != registrations[3].CSRPEM || registrations[2].FencingGeneration != 4 || registrations[3].FencingGeneration != 4 {
		t.Fatalf("replacement retry changed CSR/generation: %#v", registrations[2:])
	}

	// A response that attempts to substitute a different CA cannot be imported or marked
	// ready, even though the request itself used authenticated Controller TLS.
	candidateCA, candidatePin = ca1, pin1
	wrongResponsePin = true
	if err = ReconcilePoolWitness(context.Background(), s); err == nil || !strings.Contains(err.Error(), "does not match the selected CA pin") {
		t.Fatalf("Controller response with another CA was accepted: %v", err)
	}
	pending, err = Membership(s)
	if err != nil || pending.Generation != 5 || pending.Witness == nil || pending.Witness.CredentialsReady || pending.Witness.CASHA256 != pin1 {
		t.Fatalf("wrong-pin refusal changed the pending selected witness: %#v (%v)", pending, err)
	}
}

func witnessTestCA(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	digest := sha256.Sum256(der)
	return encoded, hex.EncodeToString(digest[:])
}

func TestPoolWitnessPendingDoesNotArmTwoHostWatchdogs(t *testing.T) {
	s := testStore(t)
	s.State.HostID, s.State.PoolID = "host-a", "pool-1"
	ca, pin := witnessTestCA(t, "qnetd")
	membership := protocol.PoolMembership{PoolID: s.State.PoolID, MasterHostID: s.State.HostID, Generation: 1,
		Witness: &protocol.PoolWitnessRecord{Kind: "controller", Address: "192.0.2.20:5403", CAPEM: ca, CASHA256: pin,
			FencingGeneration: 1, ClusterName: protocol.PoolWitnessClusterName(s.State.PoolID)},
		Members: []protocol.PoolMember{{HostID: "host-a", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready", PeerOrigin: "https://192.0.2.11:9444"},
			{HostID: "host-b", CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready", PeerOrigin: "https://192.0.2.12:9444"}}}
	nodes, want, detail := poolClusterIntent(s, membership)
	if !want || len(nodes) != 2 {
		t.Fatalf("pending registered qdevice should allow the cluster to form without SBD: nodes=%#v want=%v detail=%s", nodes, want, detail)
	}
	armed, _, why := poolWatchdogArmingIntent(context.Background(), s, membership, protocol.PoolHostWatchdog{Kind: protocol.PoolWatchdogEmulated}, false)
	if armed || !strings.Contains(why, "two-host watchdog fencing is withheld") {
		t.Fatalf("pending witness allowed SBD arming: armed=%v reason=%q", armed, why)
	}
}

func TestPoolWitnessCredentialIsSealedImportedBeforeMemberAck(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	serveMemberPeer(t, master, member)
	ca, pin := witnessTestCA(t, "qnetd")
	witness := protocol.PoolWitnessRecord{Kind: "controller", Address: "192.0.2.20:5403", CAPEM: ca, CASHA256: pin,
		FencingGeneration: 1, ClusterName: protocol.PoolWitnessClusterName("pool-1"), CredentialsReady: true}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.Witness = &witness
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	bundle := []byte("private-pkcs12-fixture")
	root := filepath.Dir(vmhost.PoolWitnessNSSPath(master.dir, witness.FencingGeneration))
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "qdevice-net-node.p12"), bundle, 0600); err != nil {
		t.Fatal(err)
	}
	previous := poolWitnessImportMember
	t.Cleanup(func() { poolWitnessImportMember = previous })
	imported := false
	poolWitnessImportMember = func(_ context.Context, stateDir, poolID string, received protocol.PoolWitnessRecord, wrapped []byte) error {
		if stateDir != member.dir || poolID != witnessPoolID || received.CASHA256 != pin || string(wrapped) != string(bundle) {
			t.Fatalf("member received wrong witness data: dir=%s pool=%s record=%#v bundle=%q", stateDir, poolID, received, wrapped)
		}
		imported = true
		return nil
	}
	if err = PushPoolAuthorityReplicas(context.Background(), master, 10*time.Second); err != nil {
		t.Fatalf("replica push: %v", err)
	}
	if !imported {
		t.Fatal("member acknowledged a witness replica without importing its PKCS#12 bundle")
	}
	record := storedReplica(t, member)
	if record == nil || !record.Verified || record.Replica.Membership.Witness == nil || !record.Replica.Membership.Witness.CredentialsReady {
		t.Fatalf("member did not retain a verified witness replica: %#v", record)
	}
	var addressed *protocol.PoolWrappedWitnessCredential
	for i := range record.Replica.WitnessCredentials {
		if record.Replica.WitnessCredentials[i].HostID == member.State.HostID {
			addressed = &record.Replica.WitnessCredentials[i]
		}
	}
	if addressed == nil || addressed.Wrapped == nil || addressed.Wrapped.Ciphertext == "" || strings.Contains(addressed.Wrapped.Ciphertext, string(bundle)) {
		t.Fatalf("member replica did not carry a sealed, addressed credential: %#v", addressed)
	}
}

const witnessPoolID = "pool-1"

func TestPoolWitnessPolicyUsesActualRenderedNodeCount(t *testing.T) {
	ca, pin := witnessTestCA(t, "qnetd policy")
	membership := protocol.PoolMembership{PoolID: witnessPoolID, Witness: &protocol.PoolWitnessRecord{
		Kind: "controller", Address: "192.0.2.20:5403", CAPEM: ca, CASHA256: pin,
		FencingGeneration: 1, ClusterName: protocol.PoolWitnessClusterName(witnessPoolID),
	}}
	two := []vmhost.PoolClusterNode{{HostID: "host-a"}, {HostID: "host-b"}}
	three := append(append([]vmhost.PoolClusterNode(nil), two...), vmhost.PoolClusterNode{HostID: "drained-host-still-voting"})
	if !poolWitnessPolicyApplies(two, membership) {
		t.Fatal("two-node qdevice cluster did not select the freeze policy")
	}
	if poolWitnessPolicyApplies(three, membership) {
		t.Fatal("third host retained in rendered nodelist selected two-host freeze policy")
	}
	membership.Witness = nil
	if poolWitnessPolicyApplies(two, membership) {
		t.Fatal("two-node cluster without a witness selected freeze policy")
	}
}
