package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallPreservesExistingHooksAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hooks.json")
	if err := os.WriteFile(path, []byte(`{"description":"keep","hooks":{"Stop":[{"hooks":[{"type":"command","command":"existing"}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := InstallHookAt(path, "/tmp/floo"); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Description string
		Hooks       map[string][]any
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if root.Description != "keep" || len(root.Hooks["Stop"]) != 1 || len(root.Hooks["SessionStart"]) != 1 || len(root.Hooks["SessionEnd"]) != 1 {
		t.Fatalf("unexpected hooks: %s", raw)
	}
	var timeouts struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Timeout int `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &timeouts); err != nil {
		t.Fatal(err)
	}
	if got := timeouts.Hooks["SessionEnd"][0].Hooks[0].Timeout; got != 3 {
		t.Fatalf("SessionEnd timeout = %d, want 3", got)
	}
}
