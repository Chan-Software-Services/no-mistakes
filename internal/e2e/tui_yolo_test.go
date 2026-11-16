//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// TestTUIYoloFixesAGateParkedBeforeAttach is the operator who attaches to a
// run already parked at Review and presses yolo as soon as the run is drawn.
// The TUI's first authoritative read is still in flight at that keypress, so
// yolo cannot act on it then; it must act once the read lands, since a gate
// parked on a human sends no later event. It used to stay on, "end yolo" in
// the action bar, with the gate never resolved.
func TestTUIYoloFixesAGateParkedBeforeAttach(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the TUI driver uses pty.fork(), which is Unix-only")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed; it drives the TUI through a pty")
	}
	driver, err := filepath.Abs(filepath.Join("testdata", "tui_yolo_driver.py"))
	if err != nil {
		t.Fatalf("resolve tui driver: %v", err)
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

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", driver, h.NMBin, fw, "y", branch, "60", "3")
	cmd.Env = os.Environ()
	screen, err := cmd.Output()
	if err != nil {
		t.Fatalf("drive the TUI: %v\n%s", err, screen)
	}
	if !strings.Contains(string(screen), "end yolo") {
		t.Fatalf("yolo mode never engaged in the TUI:\n%s", screen)
	}

	// Yolo's fix sends the finding to the fixer, and the rereview parks the
	// step again as a fix review.
	if fixed := waitForStepStatus(t, h, branch, types.StepReview, types.StepStatusFixReview, 90*time.Second); fixed == nil {
		t.Fatalf("yolo never fixed the parked review gate; TUI screen:\n%s", lastScreenFrame(string(screen)))
	}
	sawFixer := false
	for _, inv := range h.AgentInvocations() {
		if strings.Contains(inv.Prompt, "axi-1") && strings.Contains(inv.Prompt, "Investigate previous review findings") {
			sawFixer = true
		}
	}
	if !sawFixer {
		t.Fatal("no fix turn was handed the parked finding axi-1")
	}
}
