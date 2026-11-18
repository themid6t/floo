package claude

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
)

// Inject must send exactly: auth line, then one user frame, then half-close.
func TestInjectFrames(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []map[string]any, 1)
	go func() {
		c, _ := ln.Accept()
		defer c.Close()
		var lines []map[string]any
		sc := bufio.NewScanner(c) // ends at the client's half-close
		for sc.Scan() {
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			lines = append(lines, m)
		}
		got <- lines
	}()

	s := &Session{ID: "x", socket: sock, token: "tok"}
	if err := s.Inject("hi"); err != nil {
		t.Fatal(err)
	}
	lines := <-got
	if len(lines) != 2 || lines[0]["type"] != "auth" || lines[0]["token"] != "tok" ||
		lines[1]["type"] != "user" || lines[1]["message"].(map[string]any)["content"] != "hi" {
		t.Fatalf("bad frames: %v", lines)
	}
}
