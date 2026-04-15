package main

import (
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

// Status represents the current state of a Claude Code session.
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

// SessionMeta is read from ~/.claude/sessions/{pid}.json.
type SessionMeta struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	StartedAt  int64  `json:"startedAt"` // Unix millis
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
}

// SessionState is read from ~/.claude/session-states/{pid}.json.
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
	Meta      SessionMeta
	State     *SessionState
	Alive     bool
	StartedAt time.Time
}

// EffectiveStatus computes the display status considering PID liveness.
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
	return filepath.Base(s.Meta.CWD)
}

// Project returns the basename of CWD.
func (s Session) Project() string {
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

// StateReader reads session data from the filesystem.
type StateReader struct {
	sessionsDir string
	statesDir   string
}

// NewStateReader creates a reader pointing at the standard Claude directories.
func NewStateReader() *StateReader {
	home, _ := os.UserHomeDir()
	return &StateReader{
		sessionsDir: filepath.Join(home, ".claude", "sessions"),
		statesDir:   filepath.Join(home, ".claude", "session-states"),
	}
}

// ReadAll reads all session metadata and state, merges them, and returns sorted sessions.
func (r *StateReader) ReadAll() ([]Session, error) {
	metaFiles, err := filepath.Glob(filepath.Join(r.sessionsDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("globbing sessions: %w", err)
	}

	var sessions []Session
	for _, f := range metaFiles {
		base := filepath.Base(f)
		pidStr := strings.TrimSuffix(base, ".json")
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
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

		// Read state file (may not exist yet)
		var state *SessionState
		stateFile := filepath.Join(r.statesDir, pidStr+".json")
		if stateData, err := os.ReadFile(stateFile); err == nil {
			var s SessionState
			if json.Unmarshal(stateData, &s) == nil {
				// Verify this state belongs to the same session (PID reuse guard)
				if s.SessionID == meta.SessionID {
					state = &s
				}
			}
		}

		alive := isProcessAlive(pid)

		sessions = append(sessions, Session{
			Meta:      meta,
			State:     state,
			Alive:     alive,
			StartedAt: time.UnixMilli(meta.StartedAt),
		})
	}

	// Sort: alive first, then by startedAt descending
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].Alive != sessions[j].Alive {
			return sessions[i].Alive
		}
		return sessions[i].StartedAt.After(sessions[j].StartedAt)
	})

	return sessions, nil
}

// CleanStale removes state files for sessions where the PID is dead.
func (r *StateReader) CleanStale(sessions []Session) int {
	cleaned := 0
	for _, s := range sessions {
		if s.Alive {
			continue
		}
		stateFile := filepath.Join(r.statesDir, fmt.Sprintf("%d.json", s.Meta.PID))
		if err := os.Remove(stateFile); err == nil {
			cleaned++
		}
	}
	return cleaned
}

func isProcessAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
