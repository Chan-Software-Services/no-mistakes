package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/termtitle"
	"github.com/kunchenguid/no-mistakes/internal/termtitle/termtitletest"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestPipelineConnectors_NotSuppressedDuringCI(t *testing.T) {
	// When CI is active in responsive layout (wide terminal), the pipeline
	// height should not be capped, so connector lines between steps are preserved.
	// Previously, the cap applied regardless of layout mode, which suppressed
	// connectors even when the CI view was in the right column and didn't
	// compete for vertical space.
	configureTUIColors()
	run := testRunWithCI()
	for i := range run.Steps {
		run.Steps[i].Status = types.StepStatusCompleted
		dur := int64(1000)
		run.Steps[i].DurationMS = &dur
	}
	run.Steps[len(run.Steps)-1].Status = types.StepStatusRunning
	run.Steps[len(run.Steps)-1].DurationMS = nil

	m := NewModel("", nil, run)
	m.width = 120 // wide enough for responsive layout
	m.height = 50

	view := m.View()
	plain := stripANSI(view)

	// Render pipeline directly with height=50 as a baseline.
	leftWidth, _ := responsiveColumnWidths(m.width)
	baseline := stripANSI(renderPipelineView(run, run.Steps, leftWidth, 0, 50))
	baselineConnectors := 0
	for _, line := range strings.Split(baseline, "\n") {
		if strings.Count(line, "│") >= 3 {
			baselineConnectors++
		}
	}
	if baselineConnectors == 0 {
		t.Fatalf("baseline pipeline with height=50 should show connectors:\n%s", baseline)
	}

	// The full view should also contain connector lines.
	// In responsive layout, the pipeline (left column) renders with the real
	// terminal height, not a capped value.
	if !strings.Contains(plain, baseline) {
		// The pipeline should be identical to the baseline (uncapped).
		// Check for connectors by verifying step lines are not adjacent.
		stepLabels := []string{"Review", "Test", "Lint", "Push", "PR"}
		lines := strings.Split(plain, "\n")
		adjacentSteps := 0
		for i := 0; i < len(lines)-1; i++ {
			hasLabel := false
			nextHasLabel := false
			for _, label := range stepLabels {
				if strings.Contains(lines[i], label) {
					hasLabel = true
				}
				if strings.Contains(lines[i+1], label) {
					nextHasLabel = true
				}
			}
			if hasLabel && nextHasLabel {
				adjacentSteps++
			}
		}
		if adjacentSteps > 0 {
			t.Errorf("expected connector lines between steps in responsive layout during CI, but %d step pairs are adjacent.\nview:\n%s", adjacentSteps, plain)
		}
	}
}

func TestPipelineConnectors_SuppressedDuringCIInStackedLayout(t *testing.T) {
	// When CI is active in stacked layout, the pipeline height should be
	// capped so the CI panel still has room below it.
	configureTUIColors()
	run := testRunWithCI()
	for i := range run.Steps {
		run.Steps[i].Status = types.StepStatusCompleted
		dur := int64(1000)
		run.Steps[i].DurationMS = &dur
	}
	run.Steps[len(run.Steps)-1].Status = types.StepStatusRunning
	run.Steps[len(run.Steps)-1].DurationMS = nil

	m := NewModel("", nil, run)
	m.width = 80 // narrow enough to force stacked layout
	m.height = 50

	view := stripANSI(m.View())
	expectedPipeline := stripANSI(renderPipelineView(run, m.stepsWithRunningElapsed(), m.width, 0, cappedPipelineHeight))
	uncappedPipeline := stripANSI(renderPipelineView(run, m.stepsWithRunningElapsed(), m.width, 0, m.height))

	if !strings.Contains(view, expectedPipeline) {
		t.Fatalf("expected stacked CI layout to use capped pipeline height %d\nview:\n%s\nexpected pipeline:\n%s", cappedPipelineHeight, view, expectedPipeline)
	}
	if strings.Contains(view, uncappedPipeline) {
		t.Fatalf("expected stacked CI layout to avoid uncapped pipeline height %d", m.height)
	}
}

func TestTerminalTitle_AllPending(t *testing.T) {
	m := NewModel("/tmp/sock", nil, testRun())
	title := m.terminalTitle()
	if title != "○ Pending - feature/foo" {
		t.Errorf("expected '○ Pending - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_RunningStep(t *testing.T) {
	run := testRun()
	run.Steps[0].Status = types.StepStatusRunning
	m := NewModel("/tmp/sock", nil, run)
	title := m.terminalTitle()
	// Should use the current spinner frame and include the step label and branch.
	if title != "⠋ Review - feature/foo" {
		t.Errorf("expected '⠋ Review - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_CIChecksPassed(t *testing.T) {
	run := testRunWithCI()
	run.Steps[5].Status = types.StepStatusRunning
	run.CIReady = true
	m := NewModel("/tmp/sock", nil, run)
	m.steps = run.Steps
	m.logs = []string{
		"monitoring CI for PR #42 (timeout: 4h)...",
		"all CI checks passed - still monitoring until merged or closed",
	}
	title := m.terminalTitle()
	if title != "✓ Checks passed - feature/foo" {
		t.Errorf("expected '✓ Checks passed - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_CIMonitoringNotReady(t *testing.T) {
	run := testRunWithCI()
	run.Steps[5].Status = types.StepStatusRunning
	m := NewModel("/tmp/sock", nil, run)
	m.steps = run.Steps
	m.logs = []string{"monitoring CI for PR #42 (timeout: 4h)..."}
	title := m.terminalTitle()
	if strings.Contains(title, "Checks passed") {
		t.Errorf("expected non-passed CI title while monitoring, got %q", title)
	}
}

func TestTerminalTitle_RunningStepSpinnerAdvances(t *testing.T) {
	run := testRun()
	run.Steps[0].Status = types.StepStatusRunning
	m := NewModel("/tmp/sock", nil, run)
	m.spinnerFrame = 3
	title := m.terminalTitle()
	// Frame 3 is "⠸".
	if title != "⠸ Review - feature/foo" {
		t.Errorf("expected '⠸ Review - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_AwaitingApproval(t *testing.T) {
	run := testRun()
	run.Steps[0].Status = types.StepStatusAwaitingApproval
	m := NewModel("/tmp/sock", nil, run)
	title := m.terminalTitle()
	if title != "⏸ Review - feature/foo" {
		t.Errorf("expected '⏸ Review - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_Completed(t *testing.T) {
	run := testRun()
	run.Status = types.RunCompleted
	m := NewModel("/tmp/sock", nil, run)
	m.done = true
	title := m.terminalTitle()
	if title != "✓ Completed - feature/foo" {
		t.Errorf("expected '✓ Completed - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_ReattachCompletedRun(t *testing.T) {
	run := testRun()
	run.Status = types.RunCompleted
	m := NewModel("/tmp/sock", nil, run)
	title := m.terminalTitle()
	if title != "✓ Completed - feature/foo" {
		t.Errorf("expected '✓ Completed - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_Failed(t *testing.T) {
	run := testRun()
	run.Status = types.RunFailed
	m := NewModel("/tmp/sock", nil, run)
	m.done = true
	title := m.terminalTitle()
	if title != "✗ Failed - feature/foo" {
		t.Errorf("expected '✗ Failed - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_Cancelled(t *testing.T) {
	run := testRun()
	run.Status = types.RunCancelled
	m := NewModel("/tmp/sock", nil, run)
	m.done = true
	title := m.terminalTitle()
	if title != "✗ Cancelled - feature/foo" {
		t.Errorf("expected '✗ Cancelled - feature/foo', got %q", title)
	}
}

func TestTerminalTitle_CIMonitorInterrupted(t *testing.T) {
	run := testRun()
	run.Status = types.RunCIMonitorInterrupted
	m := NewModel("/tmp/sock", nil, run)
	title := m.terminalTitle()
	if title != "CI monitor interrupted - feature/foo" {
		t.Errorf("expected 'CI monitor interrupted - feature/foo', got %q", title)
	}
}

func TestNewModel_DoneRoutesThroughTerminal(t *testing.T) {
	// NewModel must derive the done field from RunStatus.Terminal() rather than
	// a hand-maintained status list, so a newly added terminal status can never
	// leave the TUI spinning forever on a finished run. Issue #361 added
	// RunCIMonitorInterrupted as the fourth terminal status; this pins every
	// status to the shared classifier so the two can never drift apart.
	cases := []struct {
		status types.RunStatus
		want   bool
	}{
		{types.RunPending, false},
		{types.RunRunning, false},
		{types.RunCompleted, true},
		{types.RunFailed, true},
		{types.RunCancelled, true},
		{types.RunCIMonitorInterrupted, true},
	}
	for _, tc := range cases {
		run := testRun()
		run.Status = tc.status
		m := NewModel("/tmp/sock", nil, run)
		if m.done != tc.want {
			t.Errorf("NewModel done for status %q = %v, want %v", tc.status, m.done, tc.want)
		}
		if m.done != tc.status.Terminal() {
			t.Errorf("NewModel done for status %q = %v must equal RunStatus.Terminal() = %v", tc.status, m.done, tc.status.Terminal())
		}
	}
}

func TestTerminalTitle_FixingStep(t *testing.T) {
	run := testRun()
	run.Steps[0].Status = types.StepStatusCompleted
	run.Steps[1].Status = types.StepStatusFixing
	m := NewModel("/tmp/sock", nil, run)
	title := m.terminalTitle()
	if title != "⠋ Test - feature/foo" {
		t.Errorf("expected '⠋ Test - feature/foo', got %q", title)
	}
}

// titleStates is one model per kind of terminal title, so a test that runs
// over it covers every title the TUI can set.
func titleStates() map[string]func() Model {
	return map[string]func() Model{
		"pending": func() Model { return NewModel("", nil, testRun()) },
		"running": func() Model {
			run := testRun()
			run.Steps[0].Status = types.StepStatusRunning
			return NewModel("", nil, run)
		},
		"awaiting approval": func() Model {
			run := testRun()
			run.Steps[0].Status = types.StepStatusAwaitingApproval
			return NewModel("", nil, run)
		},
		"checks passed": func() Model {
			run := testRunWithCI()
			run.Steps[5].Status = types.StepStatusRunning
			run.CIReady = true
			return NewModel("", nil, run)
		},
		"completed": func() Model {
			run := testRun()
			run.Status = types.RunCompleted
			return NewModel("", nil, run)
		},
		"failed": func() Model {
			run := testRun()
			run.Status = types.RunFailed
			return NewModel("", nil, run)
		},
		"cancelled": func() Model {
			run := testRun()
			run.Status = types.RunCancelled
			return NewModel("", nil, run)
		},
		"CI monitor interrupted": func() Model {
			run := testRun()
			run.Status = types.RunCIMonitorInterrupted
			return NewModel("", nil, run)
		},
	}
}

// TestView_FirstLineKeepsTheTerminalWidthInEveryTitleState measures each
// frame's lines as bubbletea's renderer does, escape sequences and all, in the
// stacked layout and in the wide layout at 105 and 119 columns. The renderer
// cuts a line it measures wider than the terminal, and x/ansi counts a title
// OSC sequence that starts with a check or cross mark as visible columns, so a
// title written into the frame cut its first line short.
func TestView_FirstLineKeepsTheTerminalWidthInEveryTitleState(t *testing.T) {
	configureTUIColors()
	for name, model := range titleStates() {
		for _, width := range []int{80, 105, 119} {
			m := model()
			m.width = width
			m.height = 40
			m.logs = []string{"log line"}
			view := m.View()
			if strings.Contains(view, "\x1b]") {
				t.Errorf("%s at %d columns: the frame carries an OSC sequence: %q", name, width, view)
			}
			lines := strings.Split(view, "\n")
			for i, line := range lines {
				got := ansi.StringWidth(line)
				if got > width || (i == 0 && got != width) {
					t.Errorf("%s at %d columns: line %d measures %d columns: %q", name, width, i, got, line)
				}
			}
		}
	}
}

// startedModel runs Init's title command and delivers its confirmation, as
// bubbletea does when the program starts, and returns the title Init set.
func startedModel(t *testing.T, m Model) (Model, string) {
	t.Helper()
	got := termtitletest.Run(m.Init())
	if len(got.Titles) != 1 || got.Confirmations != 1 {
		t.Fatalf("Init did %+v, want one title and its confirmation", got)
	}
	next, cmd := m.Update(termtitle.SetMsg{})
	if titles := termtitletest.Run(cmd).Titles; len(titles) != 0 {
		t.Fatalf("the confirmation of an unchanged title set titles %q", titles)
	}
	return next.(Model), got.Titles[0]
}

func TestInit_SetsTheTitleOfEveryState(t *testing.T) {
	for name, model := range titleStates() {
		m := model()
		_, title := startedModel(t, m)
		if want := m.terminalTitle(); title != want {
			t.Errorf("%s: Init set the title %q, want %q", name, title, want)
		}
	}
}

func TestUpdate_SetsTheTitleWhenItChanges(t *testing.T) {
	run := testRun()
	run.Steps[0].Status = types.StepStatusRunning
	m, title := startedModel(t, NewModel("", nil, run))
	if title != "⠋ Review - feature/foo" {
		t.Fatalf("Init set the title %q", title)
	}

	// A message that leaves the title as it is sets no title.
	next, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if titles := termtitletest.Run(cmd).Titles; len(titles) != 0 {
		t.Fatalf("an unchanged title was set again: %q", titles)
	}
	m = next.(Model)

	// The spinner's next frame changes the title.
	next, cmd = m.Update(spinnerTickMsg{})
	if got := termtitletest.Run(cmd); !reflect.DeepEqual(got.Titles, []string{"⠙ Review - feature/foo"}) || got.Confirmations != 1 {
		t.Fatalf("spinner tick did %+v, want the next frame's title and its confirmation", got)
	}
	m = next.(Model)

	// Until that title is confirmed, a later change waits.
	next, cmd = m.Update(eventMsg{subscriptionID: m.subscriptionID, event: ipc.Event{
		Type: ipc.EventRunCompleted, RunID: run.ID, Status: ptr(string(types.RunCompleted)), StateRev: 2,
	}})
	if titles := termtitletest.Run(cmd).Titles; len(titles) != 0 {
		t.Fatalf("a title was set while another was in flight: %q", titles)
	}
	m = next.(Model)

	// The confirmation sends the latest title.
	next, cmd = m.Update(termtitle.SetMsg{})
	if got := termtitletest.Run(cmd); !reflect.DeepEqual(got.Titles, []string{"✓ Completed - feature/foo"}) || got.Confirmations != 1 {
		t.Fatalf("the confirmation did %+v, want the completed title and its confirmation", got)
	}
	m = next.(Model)
	next, cmd = m.Update(termtitle.SetMsg{})
	if titles := termtitletest.Run(cmd).Titles; len(titles) != 0 {
		t.Fatalf("the confirmation of the latest title set titles %q", titles)
	}
}

func TestQuit_ClearsTheTitleBeforeItQuits(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c"} {
		run := testRun()
		run.Steps[0].Status = types.StepStatusRunning
		m, _ := startedModel(t, NewModel("", nil, run))
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		if key == "ctrl+c" {
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		}
		next, cmd := m.Update(msg)
		if got := termtitletest.Run(cmd); !reflect.DeepEqual(got.Titles, []string{""}) || got.Confirmations != 1 || got.Quit {
			t.Fatalf("%s: did %+v; want the title cleared and no quit before it lands", key, got)
		}
		if view := next.(Model).View(); view != "" {
			t.Errorf("%s: quitting view = %q, want empty", key, view)
		}
		_, cmd = next.(Model).Update(termtitle.SetMsg{})
		if got := termtitletest.Run(cmd); len(got.Titles) != 0 || !got.Quit {
			t.Fatalf("%s: once the blank title lands: %+v; want a quit and no title", key, got)
		}
	}
}

func TestQuit_WaitsForATitleInFlight(t *testing.T) {
	run := testRun()
	run.Steps[0].Status = types.StepStatusRunning
	m, _ := startedModel(t, NewModel("", nil, run))
	next, _ := m.Update(spinnerTickMsg{}) // a title is now in flight
	next, cmd := next.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if got := termtitletest.Run(cmd); len(got.Titles) != 0 || got.Quit {
		t.Fatalf("quit with a title in flight did %+v; want it to wait", got)
	}
	next, cmd = next.(Model).Update(termtitle.SetMsg{})
	if got := termtitletest.Run(cmd); !reflect.DeepEqual(got.Titles, []string{""}) || got.Confirmations != 1 || got.Quit {
		t.Fatalf("after the in-flight title: %+v; want the blank title", got)
	}
	_, cmd = next.(Model).Update(termtitle.SetMsg{})
	if !termtitletest.Run(cmd).Quit {
		t.Fatal("no quit once the blank title landed")
	}
}
