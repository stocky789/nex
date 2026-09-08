// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRootLoginUsesCurrentHostCredential(t *testing.T) {
	s, cfg := testService(t)
	// An old independent credential file cannot restore the former login.
	if err := os.WriteFile(filepath.Join(cfg.DataDir, "credentials.json"), []byte(`{"salt":"old","hash":"old"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var err error
	s, err = Open(cfg)
	if err != nil {
		t.Fatal("legacy credential file affected startup", err)
	}
	t.Cleanup(func() { s.Close() })
	current := "initial root password"
	calls := 0
	s.cfg.Authenticate = func(_ context.Context, password string) error {
		calls++
		if password != current {
			return errors.New("private PAM failure detail")
		}
		return nil
	}
	login := func(username, password string, want int) string {
		t.Helper()
		w := callLocal(s, "POST", "/api/local/v1/session", map[string]string{"username": username, "password": password}, "", "", "https://core.test")
		if w.Code != want {
			t.Fatalf("login status %d want %d: %s", w.Code, want, w.Body)
		}
		if strings.Contains(w.Body.String(), "private PAM") {
			t.Fatal("helper detail leaked")
		}
		if want != 200 && len(w.Result().Cookies()) != 0 {
			t.Fatal("failed login created cookie")
		}
		if want == 200 {
			if !strings.Contains(w.Body.String(), `"username":"root"`) {
				t.Fatal("incorrect session identity")
			}
			return w.Result().Cookies()[0].Value
		}
		return ""
	}
	login("admin", current, 401)
	login("root", current+"\x00ignored", 400)
	if calls != 0 {
		t.Fatal("invalid identity/input reached PAM")
	}
	login("root", "wrong", 401)
	cookie := login("root", current, 200)
	current = "changed host password"
	login("root", "initial root password", 401)
	login("root", current, 200)
	if w := callLocal(s, "GET", "/api/local/v1/session", nil, cookie, "", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"username":"root"`) {
		t.Fatal("existing session changed unexpectedly", w.Code, w.Body)
	}
}

func TestRootAuthHelperFailureAndTimeout(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	// The fixture checks raw stdin (including trailing newline), no password argv, and exit status.
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n[ \"$#\" -eq 0 ] || exit 2\nvalue=$(cat; printf x)\n[ \"$value\" = ' secret\nx' ]\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := runRootAuth(context.Background(), helper, " secret\n"); err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"wrong", "", " secret\n\x00", strings.Repeat("x", 1025)} {
		if runRootAuth(context.Background(), helper, password) == nil {
			t.Fatal("invalid password accepted")
		}
	}
	if runRootAuth(context.Background(), filepath.Join(dir, "missing"), "secret") == nil {
		t.Fatal("missing helper accepted")
	}
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if runRootAuth(ctx, helper, "secret") == nil || time.Since(start) > 2*time.Second {
		t.Fatal("helper timeout failed")
	}
}

func TestRootAuthFailureDoesNotIssueSession(t *testing.T) {
	for _, err := range []error{errors.New("helper unavailable"), context.DeadlineExceeded} {
		s, _ := testService(t)
		s.cfg.Authenticate = func(context.Context, string) error { return err }
		w := callLocal(s, "POST", "/api/local/v1/session", map[string]string{"username": "root", "password": "secret"}, "", "", "https://core.test")
		if w.Code != 401 || len(w.Result().Cookies()) != 0 || len(s.sessions) != 0 || strings.Contains(w.Body.String(), err.Error()) {
			t.Fatal("helper failed open or leaked details", w.Code, w.Body)
		}
	}
}
