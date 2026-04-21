package main

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// HistoryEntry is a single state transition from the JSONL log.
type HistoryEntry struct {
	Timestamp string  `json:"timestamp"`
	SessionID string  `json:"session_id"`
	PID       int     `json:"pid"`
	Status    Status  `json:"status"`
	CWD       string  `json:"cwd"`
	LastEvent string  `json:"last_event"`
	Tool      *string `json:"current_tool"`

	parsedTime time.Time
}

// TimeBucket aggregates session counts for a time window.
type TimeBucket struct {
	Time     time.Time
	Active   int // processing, running_tool, planning, compacting
	Waiting  int // waiting_for_input
	Approval int // waiting_for_approval
}

// Total returns the total session count in this bucket.
func (b TimeBucket) Total() int {
	return b.Active + b.Waiting + b.Approval
}

// StatusCategory classifies a status into a timeline category.
type StatusCategory int

const (
	CategoryActive   StatusCategory = iota // processing, running_tool, planning, compacting
	CategoryWaiting                        // waiting_for_input
	CategoryApproval                       // waiting_for_approval
	CategoryIgnored                        // ended, dead, unknown
)

func categoriseStatus(s Status) StatusCategory {
	switch s {
	case StatusProcessing, StatusRunningTool, StatusPlanning, StatusCompacting:
		return CategoryActive
	case StatusWaitingForInput:
		return CategoryWaiting
	case StatusWaitingForApproval:
		return CategoryApproval
	default:
		return CategoryIgnored
	}
}

// ReadHistory reads the JSONL history file and returns entries within the given duration.
func ReadHistory(window time.Duration) ([]HistoryEntry, error) {
	home, _ := os.UserHomeDir()
	historyPath := filepath.Join(home, ".claude", "session-states", "history.jsonl")

	f, err := os.Open(historyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	cutoff := time.Now().Add(-window)
	var entries []HistoryEntry

	scanner := bufio.NewScanner(f)
	// Increase buffer size for long lines
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)

	for scanner.Scan() {
		var e HistoryEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		t := parseTimestamp(e.Timestamp)
		if t.IsZero() || t.Before(cutoff) {
			continue
		}
		e.parsedTime = t
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].parsedTime.Before(entries[j].parsedTime)
	})

	return entries, nil
}

// staleTimeout caps how long a session can sit in the replay without a fresh
// transition before we stop counting it. Sessions that end without a SessionEnd
// hook firing (SIGKILL, crash, closed terminal) never get a terminal entry in
// history.jsonl, so their last-known status would otherwise carry forward
// forever and inflate counts on wide zoom levels.
const staleTimeout = 24 * time.Hour

// ComputeBuckets aggregates history entries into fixed-width time buckets.
// It reconstructs the state of each session at each bucket boundary by
// replaying transitions forward.
func ComputeBuckets(entries []HistoryEntry, window time.Duration, numBuckets int) []TimeBucket {
	if len(entries) == 0 || numBuckets <= 0 {
		return nil
	}

	now := time.Now()
	start := now.Add(-window)
	bucketWidth := window / time.Duration(numBuckets)

	buckets := make([]TimeBucket, numBuckets)
	for i := range buckets {
		buckets[i].Time = start.Add(time.Duration(i) * bucketWidth)
	}

	// Build a sorted list of all transitions
	// For each bucket, we compute the latest status of each session at that time
	type sessionKey struct {
		pid       int
		sessionID string
	}

	// Track latest status + last-transition timestamp for each session.
	// Walk buckets left-to-right, maintaining session state.
	sessionStates := make(map[sessionKey]Status)
	lastTransition := make(map[sessionKey]time.Time)
	entryIdx := 0

	for i := range buckets {
		bucketEnd := buckets[i].Time.Add(bucketWidth)

		// Replay all transitions that fall within this bucket
		for entryIdx < len(entries) && entries[entryIdx].parsedTime.Before(bucketEnd) {
			e := entries[entryIdx]
			key := sessionKey{pid: e.PID, sessionID: e.SessionID}
			sessionStates[key] = e.Status
			lastTransition[key] = e.parsedTime
			entryIdx++
		}

		// Count sessions by category, skipping any that have gone stale
		// (no transitions within staleTimeout of this bucket).
		for key, status := range sessionStates {
			if buckets[i].Time.Sub(lastTransition[key]) > staleTimeout {
				continue
			}
			cat := categoriseStatus(status)
			switch cat {
			case CategoryActive:
				buckets[i].Active++
			case CategoryWaiting:
				buckets[i].Waiting++
			case CategoryApproval:
				buckets[i].Approval++
			}
			// CategoryIgnored (ended/dead) not counted
		}
	}

	return buckets
}

// MaxBucketTotal returns the maximum total across all buckets.
func MaxBucketTotal(buckets []TimeBucket) int {
	m := 0
	for _, b := range buckets {
		if t := b.Total(); t > m {
			m = t
		}
	}
	return m
}

// parseTimestamp tries multiple ISO 8601 formats that Python's isoformat() produces.
func parseTimestamp(s string) time.Time {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999+00:00",
		"2006-01-02T15:04:05.999999-07:00",
		"2006-01-02T15:04:05+00:00",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// FormatBucketTime formats a bucket timestamp for the X-axis label.
// The format adapts based on the window duration.
func FormatBucketTime(t time.Time, window time.Duration) string {
	local := t.Local()
	switch {
	case window <= 8*time.Hour:
		return local.Format("15:04")
	case window <= 3*24*time.Hour:
		return local.Format("Mon 15h")
	default:
		return local.Format("Jan 02")
	}
}

// ScaleHeight scales a value to fit within maxRows, rounding up from 1.
func ScaleHeight(value, maxValue, maxRows int) int {
	if maxValue == 0 || value == 0 {
		return 0
	}
	return int(math.Ceil(float64(value) * float64(maxRows) / float64(maxValue)))
}
