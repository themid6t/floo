package codex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstallHook adds Floo to the user's Codex lifecycle hooks without replacing
// other hook groups. The caller should ensure exe is a stable installed path.
func InstallHook(exe string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return InstallHookAt(filepath.Join(home, ".codex", "hooks.json"), exe)
}

func InstallHookAt(path, exe string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	root := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &root); err != nil {
			return "", fmt.Errorf("existing hooks file is invalid: %w", err)
		}
	}
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	quoted := "'" + strings.ReplaceAll(exe, "'", "'\\''") + "'"
	command := quoted + " codex-hook"
	for _, event := range []string{"SessionStart", "SessionEnd"} {
		groups, _ := hooks[event].([]any)
		found := false
		for _, g := range groups {
			m, _ := g.(map[string]any)
			handlers, _ := m["hooks"].([]any)
			for _, h := range handlers {
				hm, _ := h.(map[string]any)
				if hm["command"] == command {
					found = true
				}
			}
		}
		if !found {
			groups = append(groups, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 5}}})
			hooks[event] = groups
		}
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".floo-hooks-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return "", err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
