package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCursorSessionSlug_deterministic(t *testing.T) {
	id := "conv-test-123"
	a := CursorSessionSlug(id)
	b := CursorSessionSlug(id)
	if a != b {
		t.Fatalf("slug not deterministic: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("expected 64-char hex slug, got len %d: %q", len(a), a)
	}
	for _, c := range a {
		if c < '0' || c > 'f' || (c > '9' && c < 'a') {
			t.Fatalf("non-hex in slug: %q", a)
		}
	}
}

func TestSessionAlive_cursorStale(t *testing.T) {
	meta := SessionMeta{
		Kind:      SessionKindCursor,
		SessionID: "c1",
		StartedAt: time.Now().Add(-48 * time.Hour).UnixMilli(),
		CWD:       "/tmp/proj",
	}
	oldTS := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	state := &SessionState{
		SessionID: "c1",
		Status:    StatusProcessing,
		Timestamp: oldTS,
	}
	if sessionAlive(meta, state) {
		t.Fatal("expected stale cursor session to be not alive")
	}
}

func TestSessionAlive_cursorEnded(t *testing.T) {
	meta := SessionMeta{Kind: SessionKindCursor, SessionID: "c1", StartedAt: time.Now().UnixMilli()}
	state := &SessionState{SessionID: "c1", Status: StatusEnded, Timestamp: time.Now().UTC().Format(time.RFC3339Nano)}
	if sessionAlive(meta, state) {
		t.Fatal("ended cursor session should not be alive")
	}
}

func TestSessionAlive_cursorRecent(t *testing.T) {
	meta := SessionMeta{Kind: SessionKindCursor, SessionID: "c1", StartedAt: time.Now().UnixMilli(), CWD: "/x"}
	state := &SessionState{
		SessionID: "c1",
		Status:    StatusProcessing,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if !sessionAlive(meta, state) {
		t.Fatal("expected recent cursor session to be alive")
	}
}

func TestSessionAlive_claudeNonPositivePID(t *testing.T) {
	meta := SessionMeta{Kind: "", PID: 0, SessionID: "x"}
	if sessionAlive(meta, nil) {
		t.Fatal("claude with pid 0 should not be alive")
	}
}

func TestReadAll_cursorRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	slug := CursorSessionSlug("cursor-conv-xyz")
	base := filepath.Join(home, ".cursor", "session-tracker")
	regDir := filepath.Join(base, "sessions")
	stDir := filepath.Join(base, "states")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stDir, 0o755); err != nil {
		t.Fatal(err)
	}

	reg := []byte(`{"pid":-42,"sessionId":"cursor-conv-xyz","cwd":"/tmp/ws","startedAt":1700000000000,"kind":"cursor","entrypoint":"agent","name":""}`)
	if err := os.WriteFile(filepath.Join(regDir, slug+".json"), reg, 0o644); err != nil {
		t.Fatal(err)
	}
	st := []byte(`{"session_id":"cursor-conv-xyz","pid":-42,"cwd":"/tmp/ws","status":"processing","current_tool":null,"last_event":"x","timestamp":"` +
		time.Now().UTC().Format(time.RFC3339Nano) + `","tty":null}`)
	if err := os.WriteFile(filepath.Join(stDir, slug+".json"), st, 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewStateReader()
	sessions, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range sessions {
		if s.Meta.SessionID == "cursor-conv-xyz" && s.IsCursor() {
			found = true
			if s.CursorSlug != slug {
				t.Fatalf("CursorSlug %q want %q", s.CursorSlug, slug)
			}
			if s.State == nil || s.State.Status != StatusProcessing {
				t.Fatalf("state: %+v", s.State)
			}
		}
	}
	if !found {
		t.Fatal("cursor session not merged into ReadAll")
	}
}
