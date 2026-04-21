# ai-session-tracker

A terminal dashboard for monitoring Claude Code sessions in real-time. See what every parallel session is doing — processing, running tools, waiting for input, or stuck on a permission prompt — at a glance.

## How it works

```
Claude Code hooks (Python)          Go TUI
┌──────────────────────┐       ┌──────────────────┐
│  SessionStart        │       │                  │
│  UserPromptSubmit    │──────>│  Sessions table  │
│  PreToolUse          │ JSON  │  Timeline chart  │
│  Stop / SessionEnd   │ files │                  │
│  ...                 │       │  Refreshes / 5s  │
└──────────────────────┘       └──────────────────┘
~/.claude/session-states/       reads + PID liveness
```

A Python hook fires on every Claude Code lifecycle event and writes:
- **Current state** per session to `~/.claude/session-states/{pid}.json` (atomic overwrite)
- **Transition history** to `~/.claude/session-states/history.jsonl` (append-only log)

The Go TUI reads these files every 5 seconds, cross-references with Claude's session registry (`~/.claude/sessions/`), checks PID liveness, and renders the dashboard. If a session's PID is gone but its last recorded status isn't terminal (e.g. it was `kill -9`'d or the terminal was closed), the TUI appends a synthetic `ended` transition to the history log so the session no longer counts as active in the timeline replay.

## Setup

### 1. Install the hook

Copy the hook script into your Claude config:

```sh
cp hooks/session-state-tracker.py ~/.claude/hooks/
mkdir -p ~/.claude/session-states
```

### 2. Wire up `~/.claude/settings.json`

Add the hook to every event type in your `settings.json` hooks section. Each event needs an entry like:

```json
{
  "hooks": {
    "SessionStart": [{ "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }],
    "SessionEnd":   [{ "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }],
    "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }],
    "Stop":         [{ "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }],
    "SubagentStop": [{ "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }],
    "PreToolUse":   [{ "matcher": "*", "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }],
    "PostToolUse":  [{ "matcher": "*", "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }],
    "Notification": [{ "matcher": "*", "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }],
    "PermissionRequest": [{ "matcher": "*", "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py", "timeout": 5 }] }],
    "PreCompact": [
      { "matcher": "auto", "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] },
      { "matcher": "manual", "hooks": [{ "type": "command", "command": "python3 ~/.claude/hooks/session-state-tracker.py" }] }
    ]
  }
}
```

### 3. Build and run

```sh
go build -o ai-session-tracker .
./ai-session-tracker
```

## Views

### Sessions table (`1` or `tab`)

Shows every registered session with its current state:

| Column | Description |
|---|---|
| SESSION | Session name (from `--resume` name) or project directory |
| PROJECT | Working directory basename |
| STATUS | Colour-coded current state |
| TOOL | Which tool is running (if any) |
| DURATION | Time since session started |
| LAST ACTIVE | Time since last state change |

### Timeline chart (`2` or `tab`)

Stacked bar chart showing session activity over time. Each column is a time bucket, coloured by category:

- **Yellow** — active (processing, running tools, planning, compacting)
- **Green** — waiting for user input
- **Red** — waiting for permission approval

Use `-`/`+` to zoom between 12 levels:

`5m` · `15m` · `30m` · `1h` · **`2h`** (default) · `4h` · `8h` · `1d` · `3d` · `1w` · `1mo` · `3mo`

X-axis labels adapt automatically — `HH:MM` for short windows, `Mon HHh` for multi-day, `Jan 02` for weeks/months.

## Session states

| State | Colour | Trigger |
|---|---|---|
| Processing | Yellow | Claude is thinking |
| Running Tool | Blue | Executing a tool (Read, Bash, etc.) |
| Waiting | Green | Idle, waiting for user input |
| NEEDS APPROVAL | Red | Permission prompt shown |
| Planning | Magenta | Plan mode active |
| Compacting | Cyan | Context window compaction |
| Dead | Red (dim) | PID no longer alive |
| Ended | Grey | Session exited cleanly (or was sealed after a non-clean exit) |

Timeline replay also applies a 24h staleness cap: any session that hasn't transitioned within 24h of a given bucket is dropped from that bucket's count, as a belt-and-braces safeguard against orphan history entries from before self-healing was introduced.

## Key bindings

| Key | Action |
|---|---|
| `tab` | Toggle between Sessions and Timeline views |
| `1` / `2` | Jump to Sessions / Timeline |
| `j` / `k` | Navigate session list |
| `-` / `[` | Zoom out (timeline) |
| `+` / `]` | Zoom in (timeline) |
| `r` | Force refresh |
| `c` | Clean up stale state files (dead PIDs) |
| `q` | Quit |

## Project structure

```
main.go      Entry point
state.go     Session metadata + state file reader, PID liveness checks
history.go   JSONL history reader, time-bucket aggregation for timeline
ui.go        Bubbletea TUI model, table + timeline rendering, lipgloss styles
hooks/
  session-state-tracker.py   Claude Code hook (install to ~/.claude/hooks/)
```
