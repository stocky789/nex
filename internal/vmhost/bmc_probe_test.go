// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// labRedfishService is a controller that answers its service root to anyone and lists one
// system, under the Id one vendor gives it, to the lab account only.
func labRedfishService(t *testing.T, uuid string) string {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/redfish/v1" {
			_, _ = w.Write([]byte(`{"RedfishVersion":"1.11.0"}`))
			return
		}
		if user, password, ok := r.BasicAuth(); !ok || user != "nex-fence" || password != "lab-secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/redfish/v1/Systems":
			_, _ = w.Write([]byte(`{"Members":[{"@odata.id":"/redfish/v1/Systems/System.Embedded.1"}]}`))
		case "/redfish/v1/Systems/System.Embedded.1":
			_, _ = w.Write([]byte(`{"Id":"System.Embedded.1","UUID":"` + uuid + `","PowerState":"On"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint.Host
}

// labSilentAddress is an address nothing answers on.
func labSilentAddress(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.NotFoundHandler())
	endpoint, _ := url.Parse(server.URL)
	server.Close()
	return endpoint.Host
}

// labIPMI stands up an ipmitool that answers power status, or fails every session, and
// records the cipher suites it was asked for.
func labIPMI(t *testing.T, answers bool) *[]string {
	t.Helper()
	fakeCommand(t, "ipmitool", "exit 0")
	ciphers := []string{}
	previous := storageFenceEnvRun
	storageFenceEnvRun = func(_ context.Context, _ []string, _, _ string, a ...string) ([]byte, error) {
		ciphers = append(ciphers, a[1])
		if !answers {
			return nil, errors.New("timeout")
		}
		return []byte("Chassis Power is on\n"), nil
	}
	t.Cleanup(func() { storageFenceEnvRun = previous })
	return &ciphers
}

func TestBMCProbeRecommendsTheRedfishSystemThatIsThisHost(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	endpoint := labRedfishService(t, labSystemUUID)
	result, err := ProbeBMC(context.Background(), endpoint, "nex-fence", "lab-secret", labSystemUUID)
	if err != nil {
		t.Fatal(err)
	}
	if err = result.Valid(); err != nil {
		t.Fatalf("result = %#v: %v", result, err)
	}
	if c := result.Recommended; c == nil || c.Kind != "redfish" || c.TargetID != "System.Embedded.1" || !strings.Contains(c.Reason, "hardware identity") {
		t.Fatalf("recommended = %#v", result.Recommended)
	}
	if redfish := result.Methods[0]; !redfish.Reachable || len(redfish.TargetIDs) != 1 || redfish.TargetIDs[0] != "System.Embedded.1" {
		t.Fatalf("redfish = %#v", redfish)
	}
	if ipmi := result.Methods[1]; ipmi.Reachable || !strings.Contains(ipmi.Detail, "no ipmitool") {
		t.Fatalf("ipmi = %#v", ipmi)
	}
}

// A single system that reports another machine is not offered: Set BMC access would refuse
// it, and a recommendation the operator cannot record is worse than none.
func TestBMCProbeDoesNotOfferASystemThatIsAnotherMachine(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	endpoint := labRedfishService(t, "ffffffff-1111-4111-8111-111111111111")
	result, err := ProbeBMC(context.Background(), endpoint, "nex-fence", "lab-secret", labSystemUUID)
	if err == nil || result.Recommended != nil || !strings.Contains(err.Error(), "different machine") {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestBMCProbeWithoutAnAccountAsksForOne(t *testing.T) {
	endpoint := labRedfishService(t, labSystemUUID)
	result, err := ProbeBMC(context.Background(), endpoint, "", "", labSystemUUID)
	if err == nil || result.Recommended != nil || !strings.Contains(err.Error(), "enter its account and password") {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if !result.Methods[0].Reachable || len(result.Methods[0].TargetIDs) != 0 {
		t.Fatalf("redfish = %#v", result.Methods[0])
	}
}

func TestBMCProbeWithNothingAnsweringIsOneSentence(t *testing.T) {
	labIPMI(t, false)
	result, err := ProbeBMC(context.Background(), labSilentAddress(t), "nex-fence", "lab-secret", labSystemUUID)
	if err == nil || result.Recommended != nil {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	if !strings.HasPrefix(err.Error(), "the management controller did not answer Redfish or IPMI") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("error = %q", err)
	}
	for _, method := range result.Methods {
		if method.Reachable {
			t.Fatalf("method = %#v", method)
		}
	}
}

func TestBMCProbeRecommendsIPMIPlugOneWhenOnlyIPMIAnswers(t *testing.T) {
	ciphers := labIPMI(t, true)
	result, err := ProbeBMC(context.Background(), labSilentAddress(t), "nex-fence", "lab-secret", labSystemUUID)
	if err != nil {
		t.Fatal(err)
	}
	if c := result.Recommended; c == nil || c.Kind != "ipmilan" || c.TargetID != "1" {
		t.Fatalf("recommended = %#v", result.Recommended)
	}
	if strings.Join(*ciphers, ",") != "17" {
		t.Fatalf("cipher suites tried = %v", *ciphers)
	}
}

func TestBMCProbeTriesCipherThreeAfterSeventeen(t *testing.T) {
	ciphers := labIPMI(t, false)
	_, _ = ProbeBMC(context.Background(), labSilentAddress(t), "nex-fence", "lab-secret", labSystemUUID)
	if strings.Join(*ciphers, ",") != "17,3" {
		t.Fatalf("cipher suites tried = %v", *ciphers)
	}
}

func TestBMCProbePrefersRedfishWhenBothAnswer(t *testing.T) {
	labIPMI(t, true)
	endpoint := labRedfishService(t, labSystemUUID)
	result, err := ProbeBMC(context.Background(), endpoint, "nex-fence", "lab-secret", labSystemUUID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Methods[0].Reachable || !result.Methods[1].Reachable {
		t.Fatalf("methods = %#v", result.Methods)
	}
	if c := result.Recommended; c == nil || c.Kind != "redfish" || c.TargetID != "System.Embedded.1" {
		t.Fatalf("recommended = %#v", result.Recommended)
	}
}
