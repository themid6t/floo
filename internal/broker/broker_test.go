package broker

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPendingMessageSurvivesRestartAndAcknowledgement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	b, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []Session{
		{ID: "c1", Name: "codex", Provider: "codex", Project: "/p"},
		{ID: "a1", Name: "claude", Provider: "claude", Project: "/p"},
	} {
		if err := b.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	seq, err := b.Send("c1", "claude", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	msgs, _, err := b.Poll("a1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Seq != seq || msgs[0].Text != "hello" {
		t.Fatalf("after restart: %+v", msgs)
	}
	if err := b.Ack("a1", seq); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	msgs, _, err = b.Poll("a1", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if len(msgs) != 0 {
		t.Fatalf("acked message replayed: %+v", msgs)
	}
}

func TestRoutingAndExpiredSession(t *testing.T) {
	b, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []Session{
		{ID: "c1", Name: "worker", Provider: "codex", Project: "/p"},
		{ID: "a1", Name: "worker", Provider: "claude", Project: "/q"},
		{ID: "x1", Name: "sender", Provider: "codex", Project: "/p"},
	} {
		if err := b.Register(s); err != nil {
			t.Fatal(err)
		}
	}
	seq, err := b.Send("x1", "worker", "same project")
	if err != nil {
		t.Fatal(err)
	}
	if b.data.Messages[seq].To != "c1" {
		t.Fatal("name did not prefer same project")
	}
	seq, err = b.Send("x1", "a1", "by id")
	if err != nil {
		t.Fatal(err)
	}
	if b.data.Messages[seq].To != "a1" {
		t.Fatal("global ID did not route")
	}
	s := b.data.Sessions["a1"]
	s.SeenAt = time.Now().Add(-3 * time.Minute)
	b.data.Sessions["a1"] = s
	if _, err = b.Send("x1", "a1", "gone"); err == nil {
		t.Fatal("expired target accepted")
	}
}
