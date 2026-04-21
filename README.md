# ai-session-tracker

A terminal dashboard for monitoring **Claude Code** and **Cursor** agent sessions in real time: processing, running tools, waiting for input, compacting, or idle—at a glance in one place.

## How it works

Hooks run inside each product and write small JSON files. The Go TUI polls about every **5 seconds**, merges **both** sources, and renders a session table plus a timeline.

```
┌─────────────────────────┐     ┌─────────────────────────┐
│  Claude Code (hooks)    │     │  Cursor (hooks)         │
│  settings.json → py    │     │  hooks.json → py        │
└───────────┬─────────────┘     └───────────┬─────────────┘
            │                               │
            ▼                               ▼
   ~/.claude/sessions/              ~/.cursor/session-tracker/
   ~/.claude/session-states/        ├── sessions/{slug}.json   (registry)
                                    ├── states/{slug}.json     (live state)
                                    └── history.jsonl        (append-only)

            └───────────────┬─────────────────┘
                            ▼
                   Go TUI (bubbletea)
                   Sessions + timeline
```

### Claude Code data layout

- **Registry:** `~/.claude/sessions/{pid}.json` (one file per OS process ID).
- **Live state:** `~/.claude/session-states/{pid}.json` (atomic overwrite).
- **History:** `~/.claude/session-states/history.jsonl` (one JSON object per line).

The TUI checks **PID liveness** with `kill(pid, 0)`. If a process is gone but the last state was not terminal (crash, `kill -9`, closed terminal), the app **seals** the session: it appends a synthetic `ended` line to history and rewrites the state file so the timeline does not count it as active forever.

### Cursor data layout

- **Registry:** `~/.cursor/session-tracker/sessions/{slug}.json`
- **Live state:** `~/.cursor/session-tracker/states/{slug}.json`
- **History:** `~/.cursor/session-tracker/history.jsonl`

Here **`slug`** is the **SHA-256 hex** (64 characters) of the Cursor `conversation_id`, so paths stay safe on disk and match the hook in this repo (`hooks/cursor-session-state-tracker.py` and `state.go`).

There is **no real OS PID** for Cursor sessions. The TUI treats a Cursor row as **inactive** when the state is **`ended`** or when there has been **no state update for 24 hours** (staleness). In the latter case it **seals** the session the same way as Claude (synthetic `ended` + state rewrite).

### Timeline

The chart merges **both** `history.jsonl` files, sorted by timestamp. A **24h staleness** rule also applies when *replaying* buckets so old orphan transitions do not inflate counts at wide zoom levels.

---

## Setup: Cursor

### 1. Install the hook script

This repository ships the Cursor tracker:

```sh
mkdir -p ~/.cursor/hooks
cp hooks/cursor-session-state-tracker.py ~/.cursor/hooks/
chmod +x ~/.cursor/hooks/cursor-session-state-tracker.py
```

The script is **stdlib-only** (Python 3). It writes under `~/.cursor/session-tracker/` and always returns JSON that keeps the agent unblocked (for example `{"permission":"allow"}` on `preToolUse`).

### 2. Wire up Cursor hooks

Use **user** hooks (global) or **project** hooks (repo-only). Paths are resolved relative to the hook config location:

| Config | File | Hook command path example |
|--------|------|---------------------------|
| User | `~/.cursor/hooks.json` | `python3 ./hooks/cursor-session-state-tracker.py` (cwd is `~/.cursor/`) |
| Project | `<repo>/.cursor/hooks.json` | `python3 .cursor/hooks/cursor-session-state-tracker.py` (cwd is project root) |

Minimal **`~/.cursor/hooks.json`** (user):

```json
{
  "version": 1,
  "hooks": {
    "sessionStart": [{ "command": "python3 ./hooks/cursor-session-state-tracker.py" }],
    "sessionEnd": [{ "command": "python3 ./hooks/cursor-session-state-tracker.py" }],
    "beforeSubmitPrompt": [{ "command": "python3 ./hooks/cursor-session-state-tracker.py" }],
    "preToolUse": [{ "command": "python3 ./hooks/cursor-session-state-tracker.py" }],
    "postToolUse": [{ "command": "python3 ./hooks/cursor-session-state-tracker.py" }],
    "postToolUseFailure": [{ "command": "python3 ./hooks/cursor-session-state-tracker.py" }],
    "preCompact": [{ "command": "python3 ./hooks/cursor-session-state-tracker.py" }],
    "stop": [{ "command": "python3 ./hooks/cursor-session-state-tracker.py" }]
  }
}
```

If you already have a `hooks.json`, **merge** these entries with your existing hooks instead of replacing the file. Cursor reloads hook config on save; restart the app if something does not pick up.

### 3. Cursor → state mapping (what the hook writes)

| Cursor hook | Tracked status / notes |
|-------------|-------------------------|
| `sessionStart` | `waiting_for_input` |
| `beforeSubmitPrompt` | `processing` (user just submitted) |
| `preToolUse` | `running_tool` + tool name |
| `postToolUse` | `processing` |
| `postToolUseFailure` | `processing` (with failure hint in `last_event`) |
| `preCompact` | `compacting` |
| `stop` / `sessionEnd` | `ended` |

Cursor does not expose the same permission/notification hooks as Claude Code, so **NEEDS APPROVAL** is uncommon for Cursor-backed rows unless you extend the hook yourself.

---

## Setup: Claude Code

Claude Code expects a **command hook** that reads JSON on stdin and updates `~/.claude/session-states/` (and history). That hook is **not** vendored in this repository; use your own script or another project’s `session-state-tracker.py` if you have one. The on-disk **shape** the TUI expects matches the sections above (`SessionMeta` / `SessionState` fields used in `state.go`).

### 1. Directories

```sh
mkdir -p ~/.claude/hooks ~/.claude/session-states
```

Install your Claude hook under `~/.claude/hooks/` and reference it from **`~/.claude/settings.json`**.

### 2. Example `settings.json` hook wiring

Each event needs an entry similar to:

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

Adjust the `python3 …` path to match where your Claude hook actually lives.

---

## Build and run

```sh
go build -o ai-session-tracker .
./ai-session-tracker
```

```sh
go test ./...
```

---

## Views

### Sessions table (`1` or `tab`)

Shows every registered session (Claude + Cursor) with its current state:

| Column | Description |
|--------|-------------|
| SESSION | Claude: resume name or CWD basename. Cursor: name field or CWD / conversation id. |
| PROJECT | Working directory basename |
| STATUS | Colour-coded current state |
| TOOL | Which tool is running (if any) |
| DURATION | Time since session started |
| LAST ACTIVE | Time since last state change |

### Timeline chart (`2` or `tab`)

Stacked bar chart of activity over time. Each column is a time bucket:

- **Yellow** — active (processing, running tools, planning, compacting)
- **Green** — waiting for user input
- **Red** — waiting for permission approval (mainly Claude when your hook reports it)

Use `-` / `+` to zoom across **12** levels:

`5m` · `15m` · `30m` · `1h` · **`2h`** (default) · `4h` · `8h` · `1d` · `3d` · `1w` · `1mo` · `3mo`

X-axis labels adapt: `HH:MM` for short windows, `Mon HHh` for multi-day, `Jan 02` for weeks/months.

---

## Session states

| State | Colour | Meaning |
|-------|--------|---------|
| Processing | Yellow | Model working after input or between tools |
| Running Tool | Blue | A tool call is in flight |
| Waiting | Green | Waiting for user input |
| NEEDS APPROVAL | Red | Permission prompt (Claude when hooked; rare on Cursor) |
| Planning | Magenta | Plan mode (when the hook reports it) |
| Compacting | Cyan | Context compaction |
| Dead | Red (dim) | Claude: PID gone. Cursor: stale (no update in 24h) until sealed |
| Ended | Grey | Session ended cleanly, or sealed after a non-clean stop |

---

## Key bindings

| Key | Action |
|-----|--------|
| `tab` | Toggle Sessions / Timeline |
| `1` / `2` | Jump to Sessions / Timeline |
| `j` / `k` | Move selection (sessions table) |
| `-` / `[` | Zoom out (timeline) |
| `+` / `]` | Zoom in (timeline) |
| `r` | Force refresh |
| `c` | Remove stale **state** files: Claude dead PIDs; Cursor inactive (`ended` or stale) sessions |
| `q` | Quit |

---

## Project structure

```
main.go       Entry point
state.go      Merged reader: Claude + Cursor paths, liveness, seal/clean
history.go    Merged JSONL history, bucket aggregation
ui.go         Bubble Tea TUI: table + timeline, lipgloss styles
hooks/
  cursor-session-state-tracker.py   Cursor hooks → ~/.cursor/session-tracker/
```

---

## Troubleshooting

- **No Cursor sessions:** Confirm `~/.cursor/hooks.json` paths match where you installed the script, that `python3` runs it, and that the Hooks output channel in Cursor shows no errors.
- **Hooks block the agent:** The shipped Cursor script only observes and allows tool use; if you customize it, keep returning valid JSON (including `permission` on gated hooks).
- **PermissionRequest / Notification on Cursor:** Not applicable in Cursor the same way as Claude; the dashboard still supports those **states** when driven by Claude hooks.
