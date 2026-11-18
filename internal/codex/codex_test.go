package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themid6t/floo/internal/broker"
	"github.com/themid6t/floo/internal/client"
	"github.com/themid6t/floo/internal/server"
)

func TestAgentQueuesThenAcknowledges(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	bin := filepath.Join(state, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(state, "queue-args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + output + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	b, err := broker.Open(filepath.Join(state, "broker"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	base := (&server.Server{Broker: b}).Handler()
	var rejectFirstAck atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/acks" && !rejectFirstAck.Swap(true) {
			http.Error(w, `{"error":"temporary failure"}`, http.StatusServiceUnavailable)
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := client.New(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Agent(ctx, c, "test-thread", "codex-test", "/project") }()
	deadline := time.Now().Add(8 * time.Second)
	for {
		ps, _ := c.Peers(context.Background())
		if len(ps) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent did not register")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := c.Register(context.Background(), client.Session{ID: "claude-test", Name: "claude-test", Provider: "claude", Project: "/project"}); err != nil {
		t.Fatal(err)
	}
	seq, err := c.Send(context.Background(), "claude-test", "codex-test", "ping")
	if err != nil {
		t.Fatal(err)
	}
	for {
		raw, err := os.ReadFile(output)
		if err == nil && strings.Contains(string(raw), "--thread test-thread") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("codex queue not invoked: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for {
		ms, _ := c.History(context.Background(), "test-thread")
		if len(ms) == 1 && ms[0].Seq == seq && ms[0].Acked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued message not acknowledged")
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "--thread test-thread"); n != 1 {
		t.Fatalf("queued %d times after ack retry: %s", n, raw)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("agent did not stop")
	}
}

func TestAgentStopsWhenOwnerDies(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	owner := exec.Command("sleep", "30")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { owner.Process.Kill(); owner.Wait() }()
	_, start, err := procInfo(owner.Process.Pid)
	if err != nil {
		t.Skipf("/proc unavailable: %v", err)
	}
	b, err := broker.Open(filepath.Join(state, "broker"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	srv := httptest.NewServer((&server.Server{Broker: b}).Handler())
	defer srv.Close()
	c := client.New(srv.URL)
	done := make(chan error, 1)
	go func() {
		done <- AgentWithOwner(context.Background(), c, "owner-test", "owner-test", "/project", owner.Process.Pid, start)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		peers, _ := c.Peers(context.Background())
		if len(peers) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent did not register")
		}
		time.Sleep(20 * time.Millisecond)
	}
	owner.Process.Kill()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("agent stayed alive after owner died")
	}
}
