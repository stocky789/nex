// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestUnixSocketRefusesConflictAndUsesPrivateMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.sock")
	if err := os.WriteFile(path, []byte("existing operator-owned file"), 0600); err != nil {
		t.Fatal(err)
	}
	if listener, err := openListener("", path); err == nil {
		listener.Close()
		t.Fatal("replaced existing path")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "existing operator-owned file" {
		t.Fatalf("conflicting file changed: %q %v", body, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	listener, err := openListener("", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket not private: %v", info.Mode())
	}
	if second, err := openListener("", path); err == nil {
		second.Close()
		t.Fatal("replaced live socket")
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("unexpected health path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","simulated":true}`))
	})}
	defer server.Close()
	go server.Serve(listener)
	if err := checkHealth("", path); err != nil {
		t.Fatalf("UNIX health mode failed: %v", err)
	}
}

func TestTCPListenerCannotExposePublicInterface(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.1:0", ":0"} {
		if listener, err := openListener(address, ""); err == nil {
			listener.Close()
			t.Fatalf("allowed public TCP bind %s", address)
		}
	}
	listener, err := openListener("127.0.0.1:0", "")
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
}

func TestUnixSocketReplacesOnlyStaleSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.sock")
	crashed, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := openListener("", path); err == nil {
		second.Close()
		t.Fatal("replaced the socket of a live server")
	}
	// Closing without unlinking models a killed process leaving its socket behind.
	crashed.(*net.UnixListener).SetUnlinkOnClose(false)
	crashed.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("stale socket missing: %v", err)
	}
	listener, err := openListener("", path)
	if err != nil {
		t.Fatalf("restart after crash must replace a stale socket: %v", err)
	}
	listener.Close()
}
