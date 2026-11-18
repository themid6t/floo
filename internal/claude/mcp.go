package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/themid6t/floo/internal/client"
)

// ServeMCP runs `floo mcp`: a stdio MCP server that Claude Code starts per
// session. It registers the session, delivers incoming floo messages into it
// via Inject, and exposes send/peers tools. Exits when Claude closes stdin.
func ServeMCP(c *client.Client) error {
	log.SetOutput(os.Stderr) // stdout is the MCP channel
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Outside a session (e.g. `claude mcp get` health check) still serve the
	// handshake so the check passes; tool calls then report sessErr.
	s, sessErr := Current()
	var me client.Session
	if sessErr == nil {
		me = client.Session{ID: s.ID, Provider: "claude", Name: s.Name, Project: s.Cwd}
		go deliver(ctx, c, s, me)
	}

	var mu sync.Mutex
	out := json.NewEncoder(os.Stdout)
	reply := func(id json.RawMessage, result any, rerr *rpcErr) {
		mu.Lock()
		defer mu.Unlock()
		msg := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		if rerr != nil {
			msg = map[string]any{"jsonrpc": "2.0", "id": id, "error": rerr}
		}
		out.Encode(msg)
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue // notifications need no reply
		}
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			json.Unmarshal(req.Params, &p)
			reply(req.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "floo", "version": "0.1.0"},
				"instructions": "floo connects you to other Claude Code and Codex sessions. " +
					"Messages from them arrive as '[floo from <name> ...]'. Reply with the send tool.",
			}, nil)
		case "tools/list":
			reply(req.ID, map[string]any{"tools": tools}, nil)
		case "tools/call":
			var p struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			json.Unmarshal(req.Params, &p)
			text, err := "", sessErr
			if err == nil {
				text, err = call(ctx, c, me, p.Name, p.Arguments)
			}
			if err != nil {
				text = "error: " + err.Error()
			}
			reply(req.ID, map[string]any{
				"content": []map[string]string{{"type": "text", "text": text}},
				"isError": err != nil,
			}, nil)
		case "ping":
			reply(req.ID, map[string]any{}, nil)
		default:
			reply(req.ID, nil, &rpcErr{-32601, "method not found: " + req.Method})
		}
	}
	return sc.Err()
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var tools = []map[string]any{
	{
		"name":        "send",
		"description": "Send a message to another Claude Code or Codex session via floo. `to` is a session name or id from `peers`, or \"all\".",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"to", "text"},
			"properties": map[string]any{
				"to":   map[string]string{"type": "string"},
				"text": map[string]string{"type": "string"},
			},
		},
	},
	{
		"name":        "peers",
		"description": "List Claude Code and Codex sessions reachable via floo.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
}

func call(ctx context.Context, c *client.Client, me client.Session, name string, args map[string]string) (string, error) {
	switch name {
	case "send":
		seq, err := c.Send(ctx, me.ID, args["to"], args["text"])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("sent (#%d)", seq), nil
	case "peers":
		ps, err := c.Peers(ctx)
		if err != nil {
			return "", err
		}
		b, _ := json.MarshalIndent(ps, "", "  ")
		return string(b), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// deliver long-polls the broker and injects each message, acking only after
// a successful inject so nothing is lost if the socket is briefly unavailable.
func deliver(ctx context.Context, c *client.Client, s *Session, me client.Session) {
	var after int64
	for ctx.Err() == nil {
		if err := c.Register(ctx, me); err != nil { // also the heartbeat
			log.Printf("floo: register: %v", err)
			sleep(ctx, 3*time.Second)
			continue
		}
		msgs, err := c.Poll(ctx, me.ID, after, 30*time.Second)
		if err != nil {
			log.Printf("floo: poll: %v", err)
			sleep(ctx, 3*time.Second)
			continue
		}
		for _, m := range msgs {
			from := m.FromName
			if from == "" {
				from = m.From
			}
			text := fmt.Sprintf("[floo from %s] %s\n(reply with the floo send tool, to=%q)", from, m.Text, from)
			if err := s.Inject(text); err != nil {
				log.Printf("floo: inject: %v", err)
				break // retry from this message on next poll
			}
			// The socket accepted the message. Retry only its ack, so a
			// temporary broker failure does not re-inject it repeatedly.
			for {
				if err := c.Ack(ctx, me.ID, m.Seq); err == nil {
					break
				} else {
					log.Printf("floo: ack #%d: %v", m.Seq, err)
				}
				if ctx.Err() != nil {
					return
				}
				sleep(ctx, 3*time.Second)
			}
			after = m.Seq
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
