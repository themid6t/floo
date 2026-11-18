# floo

Chat between **Claude Code** and **Codex** sessions, the way Claude Code sessions already message each other.
One small broker stores messages. Each session runs a local adapter that registers the session, picks up its messages, and hands them over in that agent's native way.

```
 Claude session ──(floo mcp)──┐                 ┌──(floo codex-agent)── Codex session
   ▲ inbox socket             ├── floo serve ───┤             ▲ codex queue
   └──────────────────────────┘   (broker)      └─────────────┘
```

## Install

```sh
go build -o ~/.local/bin/floo ./cmd/floo             # from a checkout
# private repo: GOPRIVATE=github.com/themid6t go install github.com/themid6t/floo/cmd/floo@latest
```

### 1. Run the broker (keep it running)

```sh
floo serve                        # 127.0.0.1:7777, state in ~/.local/state/floo/broker
```

As a systemd user service:

```sh
mkdir -p ~/.config/systemd/user
install -m 644 deploy/floo.service ~/.config/systemd/user/floo.service
systemctl --user daemon-reload
systemctl --user enable --now floo
```

To use a remote broker, keep `floo serve` on loopback with `FLOO_TOKEN=<secret>` set, and put an HTTPS reverse proxy (Caddy, nginx) in front of it. On every client machine, set `FLOO_URL=https://host` and the same `FLOO_TOKEN`. Plain HTTP on a non-loopback address is refused unless you pass both `FLOO_TOKEN` and `--allow-remote-http`. Don't do that outside a trusted network.

### 2. Claude Code: one command, then every session joins

```sh
claude mcp add -s user floo -- floo mcp
# remote broker: claude mcp add -s user floo -e FLOO_URL=... -e FLOO_TOKEN=... -- floo mcp
```

Claude Code starts `floo mcp` with each session and stops it when the session ends. It:
- registers the session under its Claude display name (e.g. `agent-talk-ea`) and keeps it alive with heartbeats;
- delivers incoming messages into the session's inbox socket (`CLAUDE_CODE_MESSAGING_SOCKET`). They arrive like native peer messages: an idle session wakes and starts a turn, and a busy one sees the message on its next step;
- gives Claude two tools: `send(to, text)` and `peers()`.

### 3. Codex

```sh
floo install codex
```

This adds `SessionStart` and `SessionEnd` hooks to `~/.codex/hooks.json`, preserving existing hooks. On each new or resumed Codex session, the start hook launches a small local adapter with that session's ID and project path. It registers and polls the broker, then uses `codex queue --thread` to deliver incoming messages. The end hook stops that adapter. The default session name is `codex-` plus the first eight characters of its ID; set `FLOO_NAME` before launching Codex to choose a name. Restart or resume existing sessions to run the new start hook.

The hook tells Codex how to send messages with `floo send`. In a shell, use the session ID shown by `floo who` as `--from`.

## Use

Just ask either agent, e.g. *"tell the codex session to run the tests"*. From a shell:

```sh
floo who                                  # live sessions in this folder
floo send --from <id> --to <name|id|all> message
floo history
```

Addressing: a session ID works anywhere. A name resolves in the sender's project first, then anywhere if only one session has it. `all` means every live session in the same project.

## Limits

- **Claude:** delivery needs the session's own token, which only processes started by that session have. That's why delivery runs inside `floo mcp` and not in the broker. Sessions started without the MCP server can't receive messages.
- **Codex:** `codex queue` delivers between turns, so a Codex session in a long turn sees the message when that turn ends.
- A session counts as gone 2 minutes after its last heartbeat. Messages wait in the broker until delivered (at-least-once delivery: after an adapter restart, a message can arrive twice).
- "Acked" means the adapter handed the message to the native receiver (Claude inbox socket or `codex queue`). It does not mean the model has read or acted on it; ask for a reply if you need that.
- Peer messages are not user approval. Both agents are told to treat them as requests from a teammate.
- The broker's single state file is intended for one user's small to moderate number of sessions. It stores message text on disk and rewrites the file on updates. Run one broker process per state directory; a file lock prevents concurrent writers.
