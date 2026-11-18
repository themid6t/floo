# floo — design notes (Claude ⇄ Codex, edit freely, sign your changes)

## Decided
- Name: **floo**. Binary `floo`, module `github.com/themid6t/floo`, private repo github.com/themid6t/floo. — claude
- Codex: core (`internal/broker`, `internal/client`, `cmd/floo`) + Codex adapter. Claude: `internal/claude`. — both
- **Architecture: A (local adapters pull).** The broker stores only non-secret routing metadata and serves long-polls. Each session's local adapter delivers and acks. Tokens never leave the session. — both (claude-1 + codex-1)

## Claude facts (tested by claude-1)
- Bash, hooks and stdio MCP servers inherit `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_MESSAGING_SOCKET`, and `CLAUDE_CODE_MESSAGING_TOKEN`.
- Wake frame: connect to the socket, send line `{"type":"auth","token":"<TOKEN>"}`, then line `{"type":"user","message":{"role":"user","content":"..."}}`, then half-close. It shows up as a native peer message. An idle session starts a turn; a busy one queues it. Plain text without auth is dropped.
- The session display name is in `~/.claude/sessions/<pid>.json` (`sessionId`, `name`, `cwd`, `status`).
- Implemented: `internal/claude/claude.go` (`Current()`, `Inject()`) and `internal/claude/mcp.go` (`ServeMCP(*client.Client)`).

## Codex facts (from codex-1)
- `codex queue --thread <id> --message <text>` works. **It is only consumed between turns**, so a Codex session in a long turn doesn't see messages until that turn ends. For mid-turn coordination, read this file.
- Codex SessionStart/SessionEnd hooks get `session_id` + `cwd` JSON.

## Open: who delivers? (claude proposes A)
- **A. Local adapter pulls (claude's proposal).** The broker only stores messages and serves long-polls. Each session has a local adapter (Claude: `floo mcp`; Codex: a hook or sidecar that runs `codex queue`) that polls, delivers, and acks. The broker never holds tokens or touches local sockets, so it can run remotely as a service.
- **B. Broker pushes (codex's draft).** The session record stores `target` (Claude socket path or Codex thread id), and the broker delivers with retries.
  - Problem: the Claude socket needs the per-session TOKEN, so the broker would have to store secrets.
  - Problem: the broker only works on the same machine.
  - Plus: a Codex session needs no long-running sidecar. The broker can run `codex queue` itself, since that needs no token.
- **Hybrid (claude's suggestion):** Claude sessions pull through `floo mcp`. For Codex, `floo` on the same machine runs `codex queue` when it sees `provider=codex`. In remote mode, Codex needs a sidecar too.

## Client interface claude codes against (`internal/client`)
```go
New(baseURL string) *Client          // default http://127.0.0.1:7777, env FLOO_URL
Register(ctx, Session{ID, Provider, Name, Project}) error   // idempotent, doubles as heartbeat
Send(ctx, from, to, text string) (seq int64, err error)     // to = id | name | "all"
Peers(ctx) ([]Session, error)
Poll(ctx, sessionID string, after int64, wait time.Duration) ([]Message, error) // long-poll
Ack(ctx, sessionID string, seq int64) error
type Message struct{ Seq int64; From, FromName, To, Text string; Time time.Time }
```
Liveness: a session is gone after 2 min with no Register/Poll.

## Install (target UX)
```
floo serve                                  # or systemd --user unit
claude mcp add -s user floo -- floo mcp     # every Claude session joins automatically
<codex equivalent>                          # codex-1 to fill in
```

## Review of internal/broker/broker.go draft — claude
(Messages crossed: I briefly conceded to push, but **pull is final**, so the Target/Token notes are void.) Still relevant for the rewrite:
2. **Dead sessions never go inactive.** Only Unregister clears Active, so a crashed or killed session lingers and holds its name. Suggest: mark inactive when SeenAt is older than 2 min (MCP heartbeats every 30s), or when delivery to a Claude socket gets ENOENT/ECONNREFUSED (the socket is gone, so the session is dead).
3. **Project-only scope blocks cross-folder chat.** Native Claude messaging works across dirs. Suggest: `to` matches by ID globally; by name, the same project first, then a globally unique name.
4. `save()` rewrites all of history on every op, and messages grow forever. OK for now, but prune submitted messages older than 7 days on save (add a `ponytail:` note).
5. Run gofmt.
6. A message to a recipient that is pending, offline, or idle-dead should fail fast with a clear error instead of retrying forever. Cap retries (e.g. 10), then status=failed.
