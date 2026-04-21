package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Styles
var (
	styleProcessing  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleRunningTool = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styleWaiting     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleApproval    = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	stylePlanning    = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	styleCompacting  = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	styleDead        = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Faint(true)
	styleEnded       = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleUnknown     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))

	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	styleSelected = lipgloss.NewStyle().Background(lipgloss.Color("237"))
	styleHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

func statusStyle(s Status) lipgloss.Style {
	switch s {
	case StatusProcessing:
		return styleProcessing
	case StatusRunningTool:
		return styleRunningTool
	case StatusWaitingForInput:
		return styleWaiting
	case StatusWaitingForApproval:
		return styleApproval
	case StatusPlanning:
		return stylePlanning
	case StatusCompacting:
		return styleCompacting
	case StatusDead:
		return styleDead
	case StatusEnded:
		return styleEnded
	default:
		return styleUnknown
	}
}

func statusLabel(s Status) string {
	switch s {
	case StatusProcessing:
		return "Processing"
	case StatusRunningTool:
		return "Running Tool"
	case StatusWaitingForInput:
		return "Waiting"
	case StatusWaitingForApproval:
		return "NEEDS APPROVAL"
	case StatusPlanning:
		return "Planning"
	case StatusCompacting:
		return "Compacting"
	case StatusDead:
		return "Dead"
	case StatusEnded:
		return "Ended"
	default:
		return "Unknown"
	}
}

// View mode
type viewMode int

const (
	viewTable viewMode = iota
	viewTimeline
)

// Messages
type tickMsg time.Time
type sessionsMsg []Session
type cleanedMsg int
type historyMsg []TimeBucket

// Model is the bubbletea model for the dashboard.
type Model struct {
	sessions []Session
	reader   *StateReader
	width    int
	height   int
	cursor   int
	message  string // transient status message
	quitting bool

	view    viewMode
	buckets []TimeBucket
	zoomIdx int
}

// NewModel creates a new dashboard model.
func NewModel() Model {
	return Model{
		reader:  NewStateReader(),
		zoomIdx: defaultZoomIdx,
	}
}

// Timeline constants
const chartHeight = 15

// Zoom levels from most zoomed-in to most zoomed-out.
var zoomLevels = []struct {
	window time.Duration
	label  string
}{
	{5 * time.Minute, "5m"},
	{15 * time.Minute, "15m"},
	{30 * time.Minute, "30m"},
	{1 * time.Hour, "1h"},
	{2 * time.Hour, "2h"},
	{4 * time.Hour, "4h"},
	{8 * time.Hour, "8h"},
	{24 * time.Hour, "1d"},
	{3 * 24 * time.Hour, "3d"},
	{7 * 24 * time.Hour, "1w"},
	{30 * 24 * time.Hour, "1mo"},
	{90 * 24 * time.Hour, "3mo"},
}

const defaultZoomIdx = 4 // 2h

func (m Model) zoomWindow() time.Duration {
	return zoomLevels[m.zoomIdx].window
}

func (m Model) zoomLabel() string {
	return zoomLevels[m.zoomIdx].label
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.fetchSessions,
		m.fetchHistory,
		tickEvery(5*time.Second),
	)
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) fetchSessions() tea.Msg {
	sessions, _ := m.reader.ReadAll()
	// Self-heal: synthetic "ended" when Claude PID is gone but state is non-terminal,
	// or when a Cursor session is stale (24h) without a terminal state—so timeline
	// counts do not stay inflated.
	m.reader.SealDeadSessions(sessions)
	return sessionsMsg(sessions)
}

func (m Model) cleanStale() tea.Msg {
	n := m.reader.CleanStale(m.sessions)
	return cleanedMsg(n)
}

func (m Model) fetchHistory() tea.Msg {
	// Use available width for bucket count, fall back to 80
	numBuckets := m.width - 10
	if numBuckets < 40 {
		numBuckets = 80
	}
	w := m.zoomWindow()
	entries, _ := ReadHistory(w)
	buckets := ComputeBuckets(entries, w, numBuckets)
	return historyMsg(buckets)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		m.message = ""
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "tab":
			if m.view == viewTable {
				m.view = viewTimeline
			} else {
				m.view = viewTable
			}
		case "1":
			m.view = viewTable
		case "2":
			m.view = viewTimeline
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}
		case "c":
			return m, m.cleanStale
		case "r":
			return m, tea.Batch(m.fetchSessions, m.fetchHistory)
		case "-", "[":
			// Zoom out
			if m.zoomIdx < len(zoomLevels)-1 {
				m.zoomIdx++
				return m, m.fetchHistory
			}
		case "+", "=", "]":
			// Zoom in
			if m.zoomIdx > 0 {
				m.zoomIdx--
				return m, m.fetchHistory
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case sessionsMsg:
		m.sessions = []Session(msg)
		if m.cursor >= len(m.sessions) {
			m.cursor = max(0, len(m.sessions)-1)
		}

	case historyMsg:
		m.buckets = []TimeBucket(msg)

	case cleanedMsg:
		n := int(msg)
		if n > 0 {
			m.message = fmt.Sprintf("Cleaned %d stale session(s)", n)
		} else {
			m.message = "No stale sessions to clean"
		}
		return m, m.fetchSessions

	case tickMsg:
		return m, tea.Batch(
			m.fetchSessions,
			m.fetchHistory,
			tickEvery(5*time.Second),
		)
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Title
	aliveCount := 0
	for _, s := range m.sessions {
		if s.Alive {
			aliveCount++
		}
	}
	title := styleTitle.Render("AI Session Dashboard")
	count := styleDim.Render(fmt.Sprintf(" %d sessions, %d alive", len(m.sessions), aliveCount))
	b.WriteString(title + count)

	// Tab bar
	tableTab := " [1] Sessions "
	timelineTab := " [2] Timeline "
	if m.view == viewTable {
		tableTab = styleTitle.Render(tableTab)
		timelineTab = styleDim.Render(timelineTab)
	} else {
		tableTab = styleDim.Render(tableTab)
		timelineTab = styleTitle.Render(timelineTab)
	}
	b.WriteString("    " + tableTab + " " + timelineTab + "\n\n")

	switch m.view {
	case viewTable:
		m.renderTable(&b)
	case viewTimeline:
		m.renderTimeline(&b)
	}

	// Status message
	if m.message != "" {
		b.WriteString("\n  " + m.message + "\n")
	}

	// Help footer
	b.WriteString("\n")
	help := "  q: quit  r: refresh  tab: switch view  "
	if m.view == viewTable {
		help += "c: clean stale  j/k: navigate"
	} else {
		help += "-/+: zoom  " +
			"active=" + styleProcessing.Render("█") +
			"  waiting=" + styleWaiting.Render("█") +
			"  approval=" + styleApproval.Render("█")
	}
	b.WriteString(styleHelp.Render(help))
	b.WriteString("\n")

	return b.String()
}

func (m Model) renderTable(b *strings.Builder) {
	if len(m.sessions) == 0 {
		b.WriteString(styleDim.Render("  No sessions found. Start a Claude Code or Cursor agent session (with hooks) to see it here.\n"))
		return
	}

	// Column widths
	const (
		colSession = 24
		colProject = 20
		colStatus  = 16
		colTool    = 20
		colDur     = 10
		colLast    = 12
	)

	// Header
	header := fmt.Sprintf("  %-*s %-*s %-*s %-*s %-*s %-*s",
		colSession, "SESSION",
		colProject, "PROJECT",
		colStatus, "STATUS",
		colTool, "TOOL",
		colDur, "DURATION",
		colLast, "LAST ACTIVE",
	)
	b.WriteString(styleHeader.Render(header) + "\n")

	// Rows
	for i, s := range m.sessions {
		status := s.EffectiveStatus()
		stStyle := statusStyle(status)

		name := truncateStr(s.DisplayName(), colSession)
		project := truncateStr(s.Project(), colProject)
		statusText := stStyle.Render(padRight(statusLabel(status), colStatus))

		tool := ""
		if s.State != nil && s.State.CurrentTool != nil {
			tool = *s.State.CurrentTool
		}
		tool = truncateStr(tool, colTool)

		dur := formatDuration(s.Duration())
		last := formatRelativeTime(s.LastActivity())

		row := fmt.Sprintf("  %-*s %-*s %s %-*s %-*s %-*s",
			colSession, name,
			colProject, project,
			statusText,
			colTool, tool,
			colDur, dur,
			colLast, last,
		)

		if i == m.cursor {
			row = styleSelected.Render(row)
		}

		b.WriteString(row + "\n")
	}
}

func (m Model) renderTimeline(b *strings.Builder) {
	zoomInfo := fmt.Sprintf("  Window: %s", styleTitle.Render(m.zoomLabel()))
	b.WriteString(zoomInfo + styleDim.Render("  (- zoom out, + zoom in)") + "\n\n")

	if len(m.buckets) == 0 {
		b.WriteString(styleDim.Render("  No history data yet. Use Claude Code or Cursor for a bit and check back.\n"))
		return
	}

	maxTotal := MaxBucketTotal(m.buckets)
	if maxTotal == 0 {
		b.WriteString(styleDim.Render(fmt.Sprintf("  No active sessions in the last %s (Claude + Cursor).\n", m.zoomLabel())))
		return
	}

	// Chart dimensions
	height := chartHeight
	if maxTotal < height {
		height = maxTotal
	}

	// Available width for the chart area (subtract Y-axis label width + padding)
	chartWidth := m.width - 8
	if chartWidth < 20 {
		chartWidth = 80
	}

	// Downsample buckets if we have more than chartWidth
	buckets := m.buckets
	if len(buckets) > chartWidth {
		buckets = downsampleBuckets(buckets, chartWidth)
	}

	// Y-axis label width
	yLabelWidth := len(fmt.Sprintf("%d", maxTotal))
	if yLabelWidth < 2 {
		yLabelWidth = 2
	}

	// Render chart rows top-to-bottom
	for row := height; row >= 1; row-- {
		// Y-axis label — show on top, middle, and bottom rows

		label := strings.Repeat(" ", yLabelWidth)
		if row == height {
			label = fmt.Sprintf("%*d", yLabelWidth, maxTotal)
		} else if row == 1 {
			label = fmt.Sprintf("%*d", yLabelWidth, 0)
		} else if row == height/2+1 {
			label = fmt.Sprintf("%*d", yLabelWidth, maxTotal/2)
		}

		b.WriteString("  " + styleDim.Render(label) + " " + styleDim.Render("│"))

		for _, bucket := range buckets {
			totalScaled := ScaleHeight(bucket.Total(), maxTotal, height)

			if totalScaled < row {
				b.WriteString(" ")
				continue
			}

			// Determine which category this row falls in (stacked bottom-up)
			activeScaled := ScaleHeight(bucket.Active, maxTotal, height)
			waitingScaled := ScaleHeight(bucket.Active+bucket.Waiting, maxTotal, height)

			if row <= activeScaled {
				b.WriteString(styleProcessing.Render("█"))
			} else if row <= waitingScaled {
				b.WriteString(styleWaiting.Render("█"))
			} else {
				b.WriteString(styleApproval.Render("█"))
			}
		}
		b.WriteString("\n")
	}

	// X-axis line
	b.WriteString("  " + strings.Repeat(" ", yLabelWidth) + " " + styleDim.Render("└"+strings.Repeat("─", len(buckets))) + "\n")

	// X-axis time labels
	b.WriteString("  " + strings.Repeat(" ", yLabelWidth) + "  ")
	labelInterval := len(buckets) / 6
	if labelInterval < 1 {
		labelInterval = 1
	}
	skip := 0
	for i, bucket := range buckets {
		if skip > 0 {
			skip--
			continue
		}
		if i%labelInterval == 0 {
			label := FormatBucketTime(bucket.Time, m.zoomWindow())
			b.WriteString(label)
			skip = len(label) - 1
		} else {
			b.WriteString(" ")
		}
	}
	b.WriteString("\n")
}

// downsampleBuckets reduces the number of buckets by averaging adjacent ones.
func downsampleBuckets(buckets []TimeBucket, target int) []TimeBucket {
	if len(buckets) <= target {
		return buckets
	}

	result := make([]TimeBucket, target)
	ratio := float64(len(buckets)) / float64(target)

	for i := range result {
		startIdx := int(float64(i) * ratio)
		endIdx := int(float64(i+1) * ratio)
		if endIdx > len(buckets) {
			endIdx = len(buckets)
		}

		result[i].Time = buckets[startIdx].Time
		// Use max values in the range (not average) to preserve peaks
		for j := startIdx; j < endIdx; j++ {
			if buckets[j].Active > result[i].Active {
				result[i].Active = buckets[j].Active
			}
			if buckets[j].Waiting > result[i].Waiting {
				result[i].Waiting = buckets[j].Waiting
			}
			if buckets[j].Approval > result[i].Approval {
				result[i].Approval = buckets[j].Approval
			}
		}
	}

	return result
}

// Helper functions

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func padRight(s string, width int) string {
	if len(s) >= width {
		return s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60

	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
