package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/themid6t/floo/internal/client"
)

type HookInput struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Event     string `json:"hook_event_name"`
}

func Agent(ctx context.Context, c *client.Client, id, name, project string) error {
	if _, err := safeID(id); err != nil {
		return err
	}
	dir := stateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil
	} // another adapter owns this session
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	me := client.Session{ID: id, Name: name, Provider: "codex", Project: project}
	var after int64
	for ctx.Err() == nil {
		if stopped(id) {
			return nil
		}
		if err := c.Register(ctx, me); err != nil {
			fmt.Fprintln(os.Stderr, "floo register:", err)
			pause(ctx)
			continue
		}
		msgs, err := c.Poll(ctx, id, after, 30*time.Second)
		if err != nil {
			fmt.Fprintln(os.Stderr, "floo poll:", err)
			pause(ctx)
			continue
		}
		for _, m := range msgs {
			from := m.FromName
			if from == "" {
				from = m.From
			}
			text := fmt.Sprintf("[floo #%d from %s] %s\n(reply: floo send --from %s --to %s <message>)", m.Seq, from, m.Text, id, m.From)
			cmd := exec.CommandContext(ctx, "codex", "queue", "--thread", id, "--message", text)
			out, err := cmd.CombinedOutput()
			if err != nil {
				fmt.Fprintf(os.Stderr, "floo queue: %v: %s\n", err, strings.TrimSpace(string(out)))
				pause(ctx)
				break
			}
			// The native queue accepted the message. Keep retrying only its ack;
			// polling again here would queue duplicates while the broker is down.
			for {
				if stopped(id) {
					return nil
				}
				if err := c.Ack(ctx, id, m.Seq); err == nil {
					break
				} else {
					fmt.Fprintln(os.Stderr, "floo ack:", err)
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				pause(ctx)
			}
			after = m.Seq
		}
	}
	return ctx.Err()
}

func pause(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
	}
}

func stateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "floo", "agents")
}

func safeID(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\\x00") || len(id) > 200 {
		return "", errors.New("invalid session id")
	}
	return id, nil
}

func stopped(id string) bool {
	_, err := os.Stat(filepath.Join(stateDir(), id+".stop"))
	return err == nil
}

func Hook(in HookInput) error {
	id, err := safeID(in.SessionID)
	if err != nil {
		return err
	}
	dir := stateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	stop := filepath.Join(dir, id+".stop")
	if in.Event == "SessionEnd" {
		return os.WriteFile(stop, []byte("stop"), 0600)
	}
	if in.Event != "SessionStart" {
		return fmt.Errorf("unsupported hook event %s", in.Event)
	}
	if err := os.Remove(stop); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	name := os.Getenv("FLOO_NAME")
	if name == "" {
		name = "codex-" + id[:min(len(id), 8)]
	}
	logfile, err := os.OpenFile(filepath.Join(dir, id+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer logfile.Close()
	cmd := exec.Command(exe, "codex-agent", "--id", id, "--name", name, "--project", in.Cwd)
	cmd.Stdin = nil
	cmd.Stdout = logfile
	cmd.Stderr = logfile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func DecodeHook() (HookInput, error) {
	var in HookInput
	err := json.NewDecoder(os.Stdin).Decode(&in)
	return in, err
}
