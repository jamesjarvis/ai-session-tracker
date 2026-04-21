package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Status represents the current state of an agent session.
type Status string

const (
	StatusProcessing         Status = "processing"
	StatusRunningTool        Status = "running_tool"
	StatusWaitingForInput    Status = "waiting_for_input"
	StatusWaitingForApproval Status = "waiting_for_approval"
	StatusPlanning           Status = "planning"
	StatusCompacting         Status = "compacting"
	StatusEnded              Status = "ended"
	StatusDead               Status = "dead"
	StatusUnknown            Status = "unknown"
)

// Session kind values stored in SessionMeta.Kind (registry JSON).
const (
	SessionKindClaude = "claude"
	SessionKindCursor = "cursor"
)

// cursorSessionStaleAfter is how long without a state update before a Cursor
// session is treated as inactive (no syscall liveness for Cursor).
const cursorSessionStaleAfter = 24 * time.Hour

// SessionMeta is read from ~/.claude/sessions/{pid}.json or
// ~/.cursor/session-tracker/sessions/{slug}.json.
type SessionMeta struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	StartedAt  int64  `json:"startedAt"` // Unix millis
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
}

// SessionState is read from session-states or cursor session-tracker states.
type SessionState struct {
	SessionID   string  `json:"session_id"`
	PID         int     `json:"pid"`
	CWD         string  `json:"cwd"`
	Status      Status  `json:"status"`
	CurrentTool *string `json:"current_tool"`
	LastEvent   string  `json:"last_event"`
	Timestamp   string  `json:"timestamp"` // ISO 8601
	TTY         *string `json:"tty"`
}

// Session is the merged view used by the TUI.
type Session struct {
	Meta       SessionMeta
	State      *SessionState
	Alive      bool
	StartedAt  time.Time
	CursorSlug string // basename without .json under ~/.cursor/session-tracker/sessions; empty for Claude
}

// IsCursor reports whether this session comes from Cursor hooks / registry.
func (s Session) IsCursor() bool {
	return strings.EqualFold(s.Meta.Kind, SessionKindCursor)
}

// CursorSessionSlug returns a filesystem-safe id for a Cursor conversation_id
// (SHA-256 hex, 64 characters). Must match hooks/cursor-session-state-tracker.py.
func CursorSessionSlug(conversationID string) string {
	sum := sha256.Sum256([]byte(conversationID))
	return hex.EncodeToString(sum[:])
}

// EffectiveStatus computes the display status considering PID liveness (Claude)
// or staleness / ended (Cursor).
func (s Session) EffectiveStatus() Status {
	if s.State == nil {
		if !s.Alive {
			return StatusDead
		}
		return StatusUnknown
	}
	if s.State.Status == StatusEnded {
		return StatusEnded
	}
	if !s.Alive {
		return StatusDead
	}
	return s.State.Status
}

// DisplayName returns the session name or the basename of CWD.
func (s Session) DisplayName() string {
	if s.Meta.Name != "" {
		return s.Meta.Name
	}
	if s.Meta.CWD != "" {
		return filepath.Base(s.Meta.CWD)
	}
	return s.Meta.SessionID
}

// Project returns the basename of CWD.
func (s Session) Project() string {
	if s.Meta.CWD == "" {
		return "-"
	}
	return filepath.Base(s.Meta.CWD)
}

// Duration returns how long the session has been running.
func (s Session) Duration() time.Duration {
	return time.Since(s.StartedAt)
}

// LastActivity returns the parsed timestamp from state, or startedAt as fallback.
func (s Session) LastActivity() time.Time {
	if s.State != nil && s.State.Timestamp != "" {
		t, err := time.Parse(time.RFC3339Nano, s.State.Timestamp)
		if err == nil {
			return t
		}
		// Try parsing Python's isoformat (includes +00:00 instead of Z)
		t, err = time.Parse("2006-01-02T15:04:05.999999-07:00", s.State.Timestamp)
		if err == nil {
			return t
		}
		t, err = time.Parse("2006-01-02T15:04:05.999999+00:00", s.State.Timestamp)
		if err == nil {
			return t
		}
	}
	return s.StartedAt
}

func sessionAlive(meta SessionMeta, state *SessionState) bool {
	if strings.EqualFold(meta.Kind, SessionKindCursor) {
		if state != nil && state.Status == StatusEnded {
			return false
		}
		last := lastActivityTime(meta, state)
		if time.Since(last) > cursorSessionStaleAfter {
			return false
		}
		return true
	}
	if meta.PID <= 0 {
		return false
	}
	return isProcessAlive(meta.PID)
}

func lastActivityTime(meta SessionMeta, state *SessionState) time.Time {
	if state != nil && state.Timestamp != "" {
		t, err := time.Parse(time.RFC3339Nano, state.Timestamp)
		if err == nil {
			return t
		}
		t, err = time.Parse("2006-01-02T15:04:05.999999-07:00", state.Timestamp)
		if err == nil {
			return t
		}
		t, err = time.Parse("2006-01-02T15:04:05.999999+00:00", state.Timestamp)
		if err == nil {
			return t
		}
	}
	return time.UnixMilli(meta.StartedAt)
}

// StateReader reads session data from Claude Code and Cursor hook directories.
type StateReader struct {
	claudeSessionsDir string
	claudeStatesDir   string
	cursorSessionsDir string
	cursorStatesDir   string
	cursorHistoryPath string
}

// NewStateReader creates a reader pointing at the standard Claude and Cursor paths.
func NewStateReader() *StateReader {
	home, _ := os.UserHomeDir()
	cursorBase := filepath.Join(home, ".cursor", "session-tracker")
	return &StateReader{
		claudeSessionsDir: filepath.Join(home, ".claude", "sessions"),
		claudeStatesDir:   filepath.Join(home, ".claude", "session-states"),
		cursorSessionsDir: filepath.Join(cursorBase, "sessions"),
		cursorStatesDir:   filepath.Join(cursorBase, "states"),
		cursorHistoryPath: filepath.Join(cursorBase, "history.jsonl"),
	}
}

// ReadAll reads all session metadata and state from Claude and Cursor, merges, and returns sorted sessions.
func (r *StateReader) ReadAll() ([]Session, error) {
	var sessions []Session

	claudeFiles, err := filepath.Glob(filepath.Join(r.claudeSessionsDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("globbing claude sessions: %w", err)
	}

	for _, f := range claudeFiles {
		base := filepath.Base(f)
		pidStr := strings.TrimSuffix(base, ".json")
		if _, err := strconv.Atoi(pidStr); err != nil {
			continue
		}

		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		var state *SessionState
		stateFile := filepath.Join(r.claudeStatesDir, pidStr+".json")
		if stateData, err := os.ReadFile(stateFile); err == nil {
			var s SessionState
			if json.Unmarshal(stateData, &s) == nil && s.SessionID == meta.SessionID {
				state = &s
			}
		}

		sessions = append(sessions, Session{
			Meta:       meta,
			State:      state,
			Alive:      sessionAlive(meta, state),
			StartedAt:  time.UnixMilli(meta.StartedAt),
			CursorSlug: "",
		})
	}

	_ = os.MkdirAll(r.cursorSessionsDir, 0o755)
	_ = os.MkdirAll(r.cursorStatesDir, 0o755)

	cursorFiles, err := filepath.Glob(filepath.Join(r.cursorSessionsDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("globbing cursor sessions: %w", err)
	}

	for _, f := range cursorFiles {
		slug := strings.TrimSuffix(filepath.Base(f), ".json")
		if slug == "" || slug == "history" {
			continue
		}

		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var meta SessionMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}
		if !strings.EqualFold(meta.Kind, SessionKindCursor) {
			continue
		}

		var state *SessionState
		stateFile := filepath.Join(r.cursorStatesDir, slug+".json")
		if stateData, err := os.ReadFile(stateFile); err == nil {
			var s SessionState
			if json.Unmarshal(stateData, &s) == nil && s.SessionID == meta.SessionID {
				state = &s
			}
		}

		sessions = append(sessions, Session{
			Meta:       meta,
			State:      state,
			Alive:      sessionAlive(meta, state),
			StartedAt:  time.UnixMilli(meta.StartedAt),
			CursorSlug: slug,
		})
	}

	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Alive != sessions[j].Alive {
			return sessions[i].Alive
		}
		return sessions[i].StartedAt.After(sessions[j].StartedAt)
	})

	return sessions, nil
}

// CleanStale removes state files for inactive sessions (dead Claude PIDs or
// ended/stale Cursor sessions).
func (r *StateReader) CleanStale(sessions []Session) int {
	cleaned := 0
	for _, s := range sessions {
		if s.Alive {
			continue
		}
		if s.IsCursor() {
			if s.CursorSlug == "" {
				continue
			}
			reg := filepath.Join(r.cursorSessionsDir, s.CursorSlug+".json")
			st := filepath.Join(r.cursorStatesDir, s.CursorSlug+".json")
			if err := os.Remove(st); err == nil {
				cleaned++
			}
			_ = os.Remove(reg)
			continue
		}
		stateFile := filepath.Join(r.claudeStatesDir, fmt.Sprintf("%d.json", s.Meta.PID))
		if err := os.Remove(stateFile); err == nil {
			cleaned++
		}
	}
	return cleaned
}

// SealDeadSessions appends synthetic "ended" transitions where needed:
// Claude — PID is dead and last status is not terminal;
// Cursor — session is stale (no activity within cursorSessionStaleAfter) and not terminal.
func (r *StateReader) SealDeadSessions(sessions []Session) int {
	sealed := 0

	for _, s := range sessions {
		if s.Alive {
			continue
		}
		if s.State == nil {
			continue
		}
		if s.State.Status == StatusEnded {
			continue
		}

		nowISO := time.Now().UTC().Format(time.RFC3339Nano)
		historyPath := filepath.Join(r.claudeStatesDir, "history.jsonl")
		stateFile := filepath.Join(r.claudeStatesDir, fmt.Sprintf("%d.json", s.Meta.PID))
		if s.IsCursor() {
			if s.CursorSlug == "" {
				continue
			}
			historyPath = r.cursorHistoryPath
			stateFile = filepath.Join(r.cursorStatesDir, s.CursorSlug+".json")
		}

		sealed++

		entry := map[string]any{
			"session_id":   s.State.SessionID,
			"pid":          s.State.PID,
			"cwd":          s.State.CWD,
			"status":       string(StatusEnded),
			"current_tool": nil,
			"last_event":   "SyntheticEnded",
			"timestamp":    nowISO,
			"tty":          s.State.TTY,
		}
		line, err := json.Marshal(entry)
		if err != nil {
			sealed--
			continue
		}
		line = append(line, '\n')

		if err := appendHistoryLine(historyPath, line); err != nil {
			sealed--
			continue
		}

		updated := *s.State
		updated.Status = StatusEnded
		updated.CurrentTool = nil
		updated.LastEvent = "SyntheticEnded"
		updated.Timestamp = nowISO
		tmpDir := r.claudeStatesDir
		if s.IsCursor() {
			tmpDir = r.cursorStatesDir
		}
		if err := atomicWriteJSON(stateFile, tmpDir, updated); err != nil {
			sealed--
		}
	}
	return sealed
}

func appendHistoryLine(path string, line []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

func atomicWriteJSON(target, tmpDir string, v any) error {
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, "*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	if err := enc.Encode(v); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, target)
}

func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
