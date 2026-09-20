// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// heldPoolHost is a host whose local enrollment endpoint relays the Controller's own
// "pool identity is held" refusal, which is what happens when a member is re-added to a
// replacement Controller before its pool master has reported it as a ready member.
type heldPoolHost struct {
	password string
	csrf     string
	detail   string
}

func (h *heldPoolHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/local/v1/session":
		var v struct{ Username, Password string }
		json.NewDecoder(r.Body).Decode(&v)
		if v.Username != "root" || v.Password != h.password {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid credentials"})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "local_session", Value: "session-token", Path: "/"})
		json.NewEncoder(w).Encode(map[string]string{"username": "root", "csrf_token": h.csrf})
	case r.Method == "DELETE" && r.URL.Path == "/api/local/v1/session":
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	case r.Method == "POST" && r.URL.Path == "/api/local/v1/enrollment":
		w.WriteHeader(502)
		json.NewEncoder(w).Encode(map[string]string{"error": h.detail})
	default:
		w.WriteHeader(404)
	}
}

// TestReaddingAMemberBeforeItsMasterIsARetryableConflict: connecting a pool member to a
// replacement Controller before its master has reported it is the documented order, not a
// broken host. It must not read as a transport failure.
func TestReaddingAMemberBeforeItsMasterIsARetryableConflict(t *testing.T) {
	a := newClient(t)
	controllerTLSServer(t, a.c).Close() // configures the served certificate the push sends as trust
	site := a.site()
	detail := "controller rejected request (HTTP 409): " + poolIdentityHeldRefusal
	fake := &heldPoolHost{password: "correct horse battery staple", csrf: "fake-host-csrf", detail: detail}
	host := httptest.NewTLSServer(fake)
	defer host.Close()
	var probe probeResponse
	read(t, a.call("POST", "/api/v1/hosts/probe", map[string]string{"address": addr(host)}, nil, 200), &probe)

	var body map[string]any
	read(t, a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: probe.Fingerprint, RootPassword: fake.password, ControllerURL: "https://controller.test",
	}, nil, http.StatusConflict), &body)
	message, _ := body["error"].(string)
	if !strings.Contains(message, "connect the pool master first") {
		t.Fatalf("a normal, documented wait was not explained: %+v", body)
	}
	if body["retryable"] != true {
		t.Fatalf("the wait was not marked retryable: %+v", body)
	}
	if detailed, _ := body["host_error"].(string); !strings.Contains(detailed, poolIdentityHeldRefusal) {
		t.Fatalf("the host's own error was lost: %+v", body)
	}

	// Any other host-side enrollment failure stays a 502: only this one refusal is special.
	fake.detail = "controller rejected request (HTTP 500): enrollment storage failed"
	a.call("POST", "/api/v1/sites/"+site+"/hosts", addHostRequest{
		Address: addr(host), Fingerprint: probe.Fingerprint, RootPassword: fake.password, ControllerURL: "https://controller.test",
	}, nil, http.StatusBadGateway)
}
