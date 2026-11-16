//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// TestTUIYoloFixesAGateParkedBeforeAttach is the operator who attaches to a
// run already parked at Review and presses yolo as soon as the run is drawn.
// The TUI's first authoritative read is usually still in flight at that
// keypress, so yolo cannot act on it then; it must act once the read lands,
// since a gate parked on a human sends no later event. It used to stay on,
// "end yolo" in the action bar, with the gate never resolved. Whether the read
// has landed by the keypress is a race here, so this journey can pass without
// that fix on a run where the read lands first;
// TestModel_Yolo_ActsWhenTheReconcileInFlightAtTheKeypressLands pins the
// order.
func TestTUIYoloFixesAGateParkedBeforeAttach(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the TUI driver uses pty.fork(), which is Unix-only")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed; it drives the TUI through a pty")
	}

	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: axiScenario(t)})
	h.CommitChange("init-tui-yolo", "seed.txt", "seed\n", "seed for the tui yolo journey")
	initWorktree := h.AddWorktree("init-tui-yolo")
	if out, err := h.RunInDir(initWorktree, "init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	branch := "feature/tui-yolo"
	h.CommitChange(branch, "feature.txt", "flag = true\n", "add the feature flag")
	fw := h.AddWorktree(branch)
	if out, err := h.RunInDir(fw, "axi", "run", "--intent", "wire the feature flag into the config loader"); err != nil {
		t.Fatalf("axi run: %v\n%s", err, out)
	}
	if gated := waitForStepStatus(t, h, branch, types.StepReview, types.StepStatusAwaitingApproval, 90*time.Second); gated == nil {
		t.Fatal("review step never parked")
	}

	// The fix only has to be sent before the TUI quits; the wait below
	// covers the daemon acting on it.
	screen := pressYoloInTheTUI(t, h, fw, branch, 3*time.Second)
	if !strings.Contains(screen, "end yolo") {
		t.Fatalf("yolo mode never engaged in the TUI:\n%s", screen)
	}

	// Yolo's fix sends the finding to the fixer, and the rereview parks the
	// step again as a fix review. Yolo is still on for the window above and may
	// approve that park too, and with the fast fake agent the run then finishes
	// in a fraction of a second, too soon for a poll to see the park. The TUI
	// has quit, so nothing resolves a park from here: the run rests at the fix
	// review or has finished, and either is yolo's fix done.
	run := waitForYoloFixOutcome(t, h, branch, screen, 90*time.Second)
	if review, ok := findStep(run.Steps, types.StepReview); ok {
		t.Logf("after yolo's fix: run %s, review step %s", run.Status, review.Status)
	}
	if run.Status.Terminal() && run.Status != types.RunCompleted {
		t.Fatalf("run ended %s after yolo's fix, want completed; TUI screen:\n%s", run.Status, lastScreenFrame(screen))
	}
	fixer := -1
	rereviewed := false
	for i, inv := range h.AgentInvocations() {
		switch {
		case fixer < 0 && strings.Contains(inv.Prompt, "axi-1") && strings.Contains(inv.Prompt, "Investigate previous review findings"):
			fixer = i
		case fixer >= 0 && strings.Contains(inv.Prompt, "Review the code changes and return structured findings"):
			rereviewed = true
		}
	}
	if fixer < 0 {
		t.Fatal("no fix turn was handed the parked finding axi-1")
	}
	if !rereviewed {
		t.Fatal("no review ran after the fix turn")
	}
}

// waitForYoloFixOutcome polls until branch's run has finished or its review
// step is parked as a fix review, and returns that run. A run still parked at
// the first gate when the timeout ends fails the test, with the last frame of
// screen, the TUI's drawing: yolo never acted.
func waitForYoloFixOutcome(t *testing.T, h *Harness, branch, screen string, timeout time.Duration) *ipc.RunInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastRun *ipc.RunInfo
	var pollErr error
	for time.Now().Before(deadline) {
		var runs []ipc.RunInfo
		runs, pollErr = h.runs()
		for i := range runs {
			run := runs[i]
			if run.Branch != branch {
				continue
			}
			lastRun = &run
			if run.Status.Terminal() {
				return &run
			}
			if step, ok := findStep(run.Steps, types.StepReview); ok && step.Status == types.StepStatusFixReview {
				return &run
			}
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	h.dumpDebugState()
	t.Fatalf("yolo never fixed the parked review gate on %s in %v (%s); TUI screen:\n%s", branch, timeout, polledRunProgress(lastRun, pollErr), lastScreenFrame(screen))
	return nil
}

// tuiReadyDeadline bounds how long pressYoloInTheTUI waits for the TUI to
// draw the run before it gives up.
const tuiReadyDeadline = 60 * time.Second

// yoloStandAsideWindow is how long a test that requires yolo to stand aside
// keeps the TUI open after the keypress: the window in which a wrongly sent
// response would land.
const yoloStandAsideWindow = 8 * time.Second

// pressYoloInTheTUI attaches the real TUI to worktree through a pty
// (testdata/tui_yolo_driver.py), presses y once the screen shows ready,
// keeps the TUI open for after, quits it, and returns everything it drew. The
// caller skips first where the driver cannot run (Windows, no python3). A
// driver failure fails the test with the driver's own report.
func pressYoloInTheTUI(t *testing.T, h *Harness, worktree, ready string, after time.Duration) string {
	t.Helper()
	driver, err := filepath.Abs(filepath.Join("testdata", "tui_yolo_driver.py"))
	if err != nil {
		t.Fatalf("resolve tui driver: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), tuiReadyDeadline+after+30*time.Second)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "python3", driver, h.NMBin, worktree, "y", ready,
		strconv.FormatFloat(tuiReadyDeadline.Seconds(), 'f', -1, 64),
		strconv.FormatFloat(after.Seconds(), 'f', -1, 64))
	cmd.Env = os.Environ()
	cmd.Stderr = &stderr
	screen, err := cmd.Output()
	if err != nil {
		t.Fatalf("drive the TUI: %v\n%s\nscreen:\n%s", err, strings.TrimSpace(stderr.String()), screen)
	}
	return string(screen)
}
