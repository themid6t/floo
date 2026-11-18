package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The broker stores messages only. Per-session adapters own local delivery
// credentials and sockets, which lets the broker run on another machine.
type Session struct {
	ID       string    `json:"id"`
	Provider string    `json:"provider"`
	Name     string    `json:"name"`
	Project  string    `json:"project"`
	SeenAt   time.Time `json:"seen_at"`
}

type Message struct {
	Seq      int64     `json:"seq"`
	From     string    `json:"from"`
	FromName string    `json:"from_name"`
	To       string    `json:"to"`
	Text     string    `json:"text"`
	Time     time.Time `json:"time"`
	Acked    bool      `json:"acked"`
	AckedAt  time.Time `json:"acked_at,omitempty"`
}

type state struct {
	NextSeq  int64              `json:"next_seq"`
	Sessions map[string]Session `json:"sessions"`
	Messages map[int64]Message  `json:"messages"`
}

type Broker struct {
	mu     sync.Mutex
	dir    string
	data   state
	wakeup chan struct{}
	lock   *os.File
}

func Open(dir string) (*Broker, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("state directory already owned by another broker: %w", err)
	}
	b := &Broker{dir: dir, data: state{Sessions: map[string]Session{}, Messages: map[int64]Message{}}, wakeup: make(chan struct{}), lock: lock}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		b.Close()
		return nil, err
	}
	if err := json.Unmarshal(raw, &b.data); err != nil {
		b.Close()
		return nil, fmt.Errorf("load state: %w", err)
	}
	if b.data.Sessions == nil {
		b.data.Sessions = map[string]Session{}
	}
	if b.data.Messages == nil {
		b.data.Messages = map[int64]Message{}
	}
	return b, nil
}

func (b *Broker) Close() error {
	if b.lock == nil {
		return nil
	}
	err := syscall.Flock(int(b.lock.Fd()), syscall.LOCK_UN)
	closeErr := b.lock.Close()
	b.lock = nil
	if err != nil {
		return err
	}
	return closeErr
}

const acknowledgedRetention = 7 * 24 * time.Hour

// save must be called with mu held. Old acknowledged messages are removed
// from the saved snapshot; pending messages are retained until delivery.
func (b *Broker) save() error {
	snapshot := b.data
	snapshot.Messages = make(map[int64]Message, len(b.data.Messages))
	cutoff := time.Now().Add(-acknowledgedRetention)
	for seq, m := range b.data.Messages {
		ackedAt := m.AckedAt
		if ackedAt.IsZero() {
			ackedAt = m.Time
		} // state written before AckedAt existed
		if !m.Acked || !ackedAt.Before(cutoff) {
			snapshot.Messages[seq] = m
		}
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(b.dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(b.dir, "state.json")); err != nil {
		return err
	}
	d, err := os.Open(b.dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return err
	}
	b.data.Messages = snapshot.Messages
	return nil
}

func (b *Broker) signal() { close(b.wakeup); b.wakeup = make(chan struct{}) }

func (b *Broker) Register(s Session) error {
	if s.ID == "" || s.Name == "" || s.Project == "" {
		return errors.New("id, name, and project are required")
	}
	if s.Provider != "codex" && s.Provider != "claude" {
		return errors.New("provider must be codex or claude")
	}
	if len(s.ID) > 200 || len(s.Name) > 100 || len(s.Project) > 4096 {
		return errors.New("session field too long")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, other := range b.data.Sessions {
		if other.ID != s.ID && other.Name == s.Name && other.Project == s.Project && recent(other.SeenAt) {
			return fmt.Errorf("name %q already used in project", s.Name)
		}
	}
	s.SeenAt = time.Now().UTC()
	previous, existed := b.data.Sessions[s.ID]
	b.data.Sessions[s.ID] = s
	if err := b.save(); err != nil {
		if existed {
			b.data.Sessions[s.ID] = previous
		} else {
			delete(b.data.Sessions, s.ID)
		}
		return err
	}
	return nil
}

func recent(t time.Time) bool { return time.Since(t) < 2*time.Minute }

func (b *Broker) Peers(project string) []Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Session{}
	for _, s := range b.data.Sessions {
		if recent(s.SeenAt) && (project == "" || s.Project == project) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (b *Broker) Send(from, to, body string) (int64, error) {
	if strings.TrimSpace(body) == "" || len(body) > 64*1024 {
		return 0, errors.New("message must contain 1 to 65536 bytes")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	sender, ok := b.data.Sessions[from]
	if !ok || !recent(sender.SeenAt) {
		return 0, fmt.Errorf("sender %q is not registered or has expired", from)
	}
	var recipients []Session
	if to == "all" {
		for _, s := range b.data.Sessions {
			if recent(s.SeenAt) && s.Project == sender.Project && s.ID != from {
				recipients = append(recipients, s)
			}
		}
	} else {
		// IDs are global. A name resolves in the sender's project first;
		// outside it, the name must identify exactly one live session.
		for _, s := range b.data.Sessions {
			if recent(s.SeenAt) && s.ID == to && s.ID != from {
				recipients = append(recipients, s)
				break
			}
		}
		if len(recipients) == 0 {
			for _, s := range b.data.Sessions {
				if recent(s.SeenAt) && s.Name == to && s.Project == sender.Project && s.ID != from {
					recipients = append(recipients, s)
				}
			}
		}
		if len(recipients) == 0 {
			for _, s := range b.data.Sessions {
				if recent(s.SeenAt) && s.Name == to && s.ID != from {
					recipients = append(recipients, s)
				}
			}
			if len(recipients) > 1 {
				return 0, fmt.Errorf("name %q is ambiguous across projects; use session ID", to)
			}
		}
	}
	if len(recipients) == 0 {
		return 0, fmt.Errorf("recipient %q not found in project", to)
	}
	oldSeq := b.data.NextSeq
	for _, s := range recipients {
		b.data.NextSeq++
		m := Message{Seq: b.data.NextSeq, From: from, FromName: sender.Name, To: s.ID, Text: body, Time: time.Now().UTC()}
		b.data.Messages[m.Seq] = m
	}
	if err := b.save(); err != nil {
		for seq := oldSeq + 1; seq <= b.data.NextSeq; seq++ {
			delete(b.data.Messages, seq)
		}
		b.data.NextSeq = oldSeq
		return 0, err
	}
	b.signal()
	return oldSeq + 1, nil
}

func (b *Broker) Poll(session string, after int64) ([]Message, <-chan struct{}, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.data.Sessions[session]
	if !ok || !recent(s.SeenAt) {
		return nil, nil, fmt.Errorf("session %q is not registered or has expired", session)
	}
	out := []Message{}
	for _, m := range b.data.Messages {
		if m.To == session && m.Seq > after && !m.Acked {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, b.wakeup, nil
}

func (b *Broker) Ack(session string, seq int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.data.Messages[seq]
	if !ok || m.To != session {
		return os.ErrNotExist
	}
	if m.Acked {
		return nil
	}
	m.Acked = true
	m.AckedAt = time.Now().UTC()
	b.data.Messages[seq] = m
	if err := b.save(); err != nil {
		m.Acked = false
		m.AckedAt = time.Time{}
		b.data.Messages[seq] = m
		return err
	}
	return nil
}

func (b *Broker) History(session string) []Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Message{}
	for _, m := range b.data.Messages {
		if session == "" || m.From == session || m.To == session {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}
