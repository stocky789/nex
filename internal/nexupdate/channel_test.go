// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func classicConf(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	old, oldStamp := UpdatesConfig, IndexStampPath
	UpdatesConfig, IndexStampPath = filepath.Join(dir, "updates.conf"), filepath.Join(dir, "refresh.stamp")
	t.Cleanup(func() { UpdatesConfig, IndexStampPath = old, oldStamp })
	t.Setenv("NEX_UPDATE_REPO", "")
	if content != "" {
		if err := os.WriteFile(UpdatesConfig, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return UpdatesConfig
}

func TestRepoChannelFollowsTheArchiveSubtree(t *testing.T) {
	for url, want := range map[string]string{
		"https://example.test/apt":      "stable",
		"https://example.test/apt/":     "stable",
		"https://example.test/apt/dev":  "dev",
		"https://example.test/apt/dev/": "dev",
		"https://example.test/devices":  "stable",
	} {
		if got := RepoChannel(url); got != want {
			t.Errorf("%s: %s, want %s", url, got, want)
		}
	}
	if got := ChannelRepoURL("https://example.test/apt/dev/", "stable"); got != "https://example.test/apt" {
		t.Errorf("stable moves back to the archive root, got %s", got)
	}
	if got := ChannelRepoURL("https://example.test/apt/dev", "dev"); got != "https://example.test/apt/dev" {
		t.Errorf("dev is idempotent, got %s", got)
	}
}

func TestSavingTheChannelChangesTheRepoTheNextCheckReads(t *testing.T) {
	conf := classicConf(t, "repo_url=https://mirror.example.test/nex\nsomething=kept\n")
	if err := os.WriteFile(IndexStampPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveClassicChannel("dev"); err != nil {
		t.Fatal(err)
	}
	if got := DefaultRepoURL(); got != "https://mirror.example.test/nex/dev" {
		t.Fatalf("a mirror keeps its own root: %s", got)
	}
	if b, _ := os.ReadFile(conf); string(b) != "repo_url=https://mirror.example.test/nex/dev\nsomething=kept\n" {
		t.Fatalf("other lines are kept: %q", b)
	}
	if _, err := os.Stat(IndexStampPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the package index of the old archive must not satisfy the next check")
	}
	if err := SaveClassicChannel("stable"); err != nil {
		t.Fatal(err)
	}
	if got := DefaultRepoURL(); got != "https://mirror.example.test/nex" {
		t.Fatalf("stable restores the root: %s", got)
	}
}

func TestSavingTheChannelWithoutAConfigStartsFromTheDefaultArchive(t *testing.T) {
	classicConf(t, "")
	if err := SaveClassicChannel("dev"); err != nil {
		t.Fatal(err)
	}
	if got := DefaultRepoURL(); got != defaultRepoURL+"/dev" {
		t.Fatalf("got %s", got)
	}
}

func TestSavingARefusedChannelChangesNothing(t *testing.T) {
	conf := classicConf(t, "repo_url=https://mirror.example.test/nex\n")
	for _, bad := range []string{"", "testing", "Dev", "../dev"} {
		var refusal *Error
		if err := SaveClassicChannel(bad); !errors.As(err, &refusal) || refusal.Code != "invalid_channel" || refusal.Status != 400 {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// An environment override would silently win over the file, so the change is refused instead.
	t.Setenv("NEX_UPDATE_REPO", "https://env.example.test/apt")
	var refusal *Error
	if err := SaveClassicChannel("dev"); !errors.As(err, &refusal) || refusal.Code != "settings_overridden" || refusal.Status != 409 {
		t.Errorf("override: %v", err)
	}
	if b, _ := os.ReadFile(conf); string(b) != "repo_url=https://mirror.example.test/nex\n" {
		t.Fatalf("a refusal must not touch the file: %q", b)
	}
}
