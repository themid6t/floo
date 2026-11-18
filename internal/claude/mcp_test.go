package claude

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/themid6t/floo/internal/broker"
	"github.com/themid6t/floo/internal/client"
	"github.com/themid6t/floo/internal/server"
)

func TestDeliverRetriesAckWithoutReinjecting(t *testing.T) {
	b, err := broker.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	base := (&server.Server{Broker: b}).Handler()
	var ackAttempts atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/acks" && ackAttempts.Add(1) == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		base.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	c := client.New(httpServer.URL)
	sock := filepath.Join(t.TempDir(), "claude.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var injections atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				sc := bufio.NewScanner(conn)
				for sc.Scan() {
				}
				injections.Add(1)
			}()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	me := client.Session{ID: "claude-test", Name: "claude-test", Provider: "claude", Project: "/project"}
	go deliver(ctx, c, &Session{ID: me.ID, socket: sock, token: "test"}, me)
	deadline := time.Now().Add(8 * time.Second)
	for {
		peers, _ := c.Peers(context.Background())
		if len(peers) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Claude adapter did not register")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := c.Register(context.Background(), client.Session{ID: "codex-test", Name: "codex-test", Provider: "codex", Project: "/project"}); err != nil {
		t.Fatal(err)
	}
	seq, err := c.Send(context.Background(), "codex-test", me.ID, "ping")
	if err != nil {
		t.Fatal(err)
	}
	for {
		ms, _ := c.History(context.Background(), me.ID)
		if len(ms) == 1 && ms[0].Seq == seq && ms[0].Acked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Claude adapter did not ack")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := injections.Load(); n != 1 {
		t.Fatalf("injected %d times after ack retry", n)
	}
	if n := ackAttempts.Load(); n < 2 {
		t.Fatalf("ack was not retried: %d attempts", n)
	}
}
