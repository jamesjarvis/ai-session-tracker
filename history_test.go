package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMergeHistoryEntries_sorted(t *testing.T) {
	t1 := time.Now().Add(-10 * time.Minute)
	t2 := time.Now().Add(-5 * time.Minute)
	a := []HistoryEntry{
		{SessionID: "a", PID: 1, Status: StatusProcessing, parsedTime: t1},
	}
	b := []HistoryEntry{
		{SessionID: "b", PID: 2, Status: StatusWaitingForInput, parsedTime: t2},
	}
	out := mergeHistoryEntries(a, b)
	if len(out) != 2 {
		t.Fatalf("len %d", len(out))
	}
	if !out[0].parsedTime.Before(out[1].parsedTime) {
		t.Fatalf("order wrong: %v then %v", out[0].parsedTime, out[1].parsedTime)
	}
}

func TestReadHistory_mergesClaudeAndCursor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	claudeDir := filepath.Join(home, ".claude", "session-states")
	curDir := filepath.Join(home, ".cursor", "session-tracker")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(curDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ts1 := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339Nano)
	ts2 := time.Now().Add(-20 * time.Minute).UTC().Format(time.RFC3339Nano)
	line1 := `{"timestamp":"` + ts1 + `","session_id":"s1","pid":1,"cwd":"/a","status":"processing","last_event":"e1","current_tool":null}` + "\n"
	line2 := `{"timestamp":"` + ts2 + `","session_id":"s2","pid":-1,"cwd":"/b","status":"waiting_for_input","last_event":"e2","current_tool":null}` + "\n"

	if err := os.WriteFile(filepath.Join(claudeDir, "history.jsonl"), []byte(line1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(curDir, "history.jsonl"), []byte(line2), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadHistory(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries", len(entries))
	}
	if entries[0].SessionID != "s1" || entries[1].SessionID != "s2" {
		t.Fatalf("order or ids: %+v, %+v", entries[0], entries[1])
	}
}
