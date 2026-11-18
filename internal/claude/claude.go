// Package claude wakes a running Claude Code session by posting to its
// session inbox socket. Works only from a process Claude Code spawned
// (hook, MCP server, Bash): those inherit CLAUDE_CODE_MESSAGING_{SOCKET,TOKEN}.
package claude

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Session is the Claude Code session this process belongs to.
type Session struct {
	ID, Name, Cwd string
	socket, token string
}

// Current reads the session from the inherited environment.
func Current() (*Session, error) {
	s := &Session{
		ID:     os.Getenv("CLAUDE_CODE_SESSION_ID"),
		socket: os.Getenv("CLAUDE_CODE_MESSAGING_SOCKET"),
		token:  os.Getenv("CLAUDE_CODE_MESSAGING_TOKEN"),
	}
	if s.ID == "" || s.socket == "" || s.token == "" {
		return nil, errors.New("not running inside a Claude Code session (CLAUDE_CODE_MESSAGING_* unset)")
	}
	s.Cwd, _ = os.Getwd()
	if s.Name = lookupName(s.ID); s.Name == "" {
		s.Name = filepath.Base(s.Cwd) + "-" + s.ID[:min(4, len(s.ID))]
	}
	return s, nil
}

// lookupName finds Claude's own display name (e.g. "agent-talk-ea") in
// ~/.claude/sessions/<pid>.json; empty if not found.
func lookupName(id string) string {
	home, _ := os.UserHomeDir()
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "sessions", "*.json"))
	for _, f := range files {
		var m struct{ SessionID, Name string }
		if b, err := os.ReadFile(f); err == nil && json.Unmarshal(b, &m) == nil && m.SessionID == id {
			return m.Name
		}
	}
	return ""
}

// Inject delivers text into the session as a peer message, starting a turn
// if the session is idle (queued if busy).
func (s *Session) Inject(text string) error {
	c, err := net.DialTimeout("unix", s.socket, 5*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	enc := json.NewEncoder(c) // one JSON object per line
	if err := enc.Encode(map[string]string{"type": "auth", "token": s.token}); err != nil {
		return err
	}
	if err := enc.Encode(map[string]any{
		"type":    "user",
		"message": map[string]string{"role": "user", "content": text},
	}); err != nil {
		return err
	}
	// Half-close and wait for the server to hang up; an abrupt close can drop the frame.
	c.(*net.UnixConn).CloseWrite()
	_, err = io.Copy(io.Discard, c)
	return err
}
