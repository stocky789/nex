// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSaveClassifiesPostRenameFailureAndRetainsPublishedFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.State.Controller = "https://old.example"
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	s.State.Controller = "https://replacement.example"
	s.afterPublish = func() error { return errors.New("injected directory sync failure") }
	err = s.Save()
	if !stateWasPublished(err) {
		t.Fatalf("post-rename failure was not classified as published: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.State.Controller != "https://replacement.example" {
		t.Fatal("post-rename failure did not retain the published state file")
	}
}
