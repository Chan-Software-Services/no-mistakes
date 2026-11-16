//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// documentPromptMarker opens the Document step's prompt when lint has its own
// configured command, so the step runs its documentation pass alone.
const documentPromptMarker = "Keep the project documentation accurate for this change."

// toolTurnScenarioName is the one scenario the catch-all Test report carries.
const toolTurnScenarioName = "a user reads the feature value"

// toolTurnNoOutputStream is a Claude turn that edits a file and then ends
// with a success result carrying no structured output, the shape a session
// woken after its last tool call ends in.
func toolTurnNoOutputStream(t *testing.T) []string {
	t.Helper()
	const session = "d0c0de00-0000-4000-8000-000000000001"
	usage := map[string]int{"input_tokens": 10, "output_tokens": 5}
	return []string{
		claudeStreamLine(t, map[string]any{"type": "system", "subtype": "init", "session_id": session}),
		claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"model": "claude-opus-4-7", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_edit", "name": "Edit", "input": map[string]any{
				"file_path": "feature.txt", "old_string": "value", "new_string": "value",
			}}},
		}}),
		claudeStreamLine(t, map[string]any{"type": "user", "session_id": session, "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_edit", "content": "The file feature.txt has been updated."}},
		}}),
		claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"model": "claude-opus-4-7", "usage": usage, "content": []any{map[string]any{"type": "text", "text": "Done."}},
		}}),
		claudeStreamLine(t, map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session, "result": "Done.", "usage": usage}),
	}
}

// toolTurnScenario writes a scenario whose turn matching marker is the
// tool turn without output, and whose every other turn returns a report that
// satisfies every step's schema.
func toolTurnScenario(t *testing.T, marker string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("actions:\n  - match: \"" + marker + "\"\n    claude_stream:\n")
	for _, line := range toolTurnNoOutputStream(t) {
		quoted, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("      - " + string(quoted) + "\n")
	}
	b.WriteString(`  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks detected in the diff"
      risk_scope: source-or-external
      tested: ["go test ./..."]
      testing_summary: "Read the feature value back after the change; it was as written."
      scenarios:
        - name: "` + toolTurnScenarioName + `"
          result: pass
          live: true
          evidence: "the feature value was printed as written"
          reason: ""
      verdict: go
`)
	path := filepath.Join(t.TempDir(), "tool-turn-scenario.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

// runToolTurn drives a run whose turn matching marker is the tool turn
// without output, and returns how many such turns the agent saw, the step's
// log, and the finished run.
func runToolTurn(t *testing.T, marker string, step types.StepName) (int, string, ipc.StepResultInfo, types.RunStatus) {
	t.Helper()
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: toolTurnScenario(t, marker)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	branch := "feature/tool-turn"
	h.CommitChange(branch, ".no-mistakes.yaml", "commands:\n  lint: 'exit 0'\n", "configure lint")
	h.CommitChange(branch, "feature.txt", "value\n", "add feature")

	runOut, _ := h.Run("axi", "run", "--intent", "show the feature value", "--skip", "pr,ci")
	run := h.WaitForRun(branch, 90*time.Second)
	t.Logf("stock axi run surface:\n%s", runOut)

	turns := 0
	for _, inv := range h.AgentInvocations() {
		if strings.Contains(inv.Prompt, marker) {
			turns++
		}
	}
	logOut, err := h.Run("axi", "logs", "--step", string(step), "--full")
	if err != nil {
		t.Fatalf("axi logs: %v\n%s", err, logOut)
	}
	result, ok := findStep(run.Steps, step)
	if !ok {
		t.Fatalf("run has no %s step: %+v", step, run.Steps)
	}
	return turns, logOut, result, run.Status
}

// TestToolTurnWithoutOutputIsReplayedOutsideTheTestStepJourney: the rule
// that a turn which ran tools is never replayed belongs to the Test step's
// analyzer alone, so a Claude Document turn that edited a file and ended with
// no structured output is still replayed, as before that rule.
func TestToolTurnWithoutOutputIsReplayedOutsideTheTestStepJourney(t *testing.T) {
	turns, logOut, _, _ := runToolTurn(t, documentPromptMarker, types.StepDocument)
	if turns != 4 || !strings.Contains(logOut, `missing structured output`) {
		t.Fatalf("Document turns = %d, want the turn replayed to the adapter's limit (4); step log:\n%s", turns, logOut)
	}
	if strings.Contains(logOut, "not replaying the turn") {
		t.Fatalf("the Document step log says the turn was not replayed:\n%s", logOut)
	}
}

// TestTestEvidenceToolTurnWithoutOutputIsNotReplayedJourney: a Test evidence
// turn that ran tools and ended with no structured output is not replayed,
// since a replay drives every scenario again inside the same budget; its
// missing report goes to the correction turn, whose report is recorded.
func TestTestEvidenceToolTurnWithoutOutputIsNotReplayedJourney(t *testing.T) {
	turns, logOut, step, status := runToolTurn(t, testEvidencePromptMarker, types.StepTest)
	if turns != 1 || strings.Contains(logOut, "retrying after transient error") {
		t.Fatalf("Test evidence turns = %d, want exactly one; step log:\n%s", turns, logOut)
	}
	for _, want := range []string{
		"claude returned no structured output after running tools; not replaying the turn",
		"asking agent to correct and resubmit",
	} {
		if !strings.Contains(logOut, want) {
			t.Fatalf("Test step log missing %q:\n%s", want, logOut)
		}
	}
	if status != types.RunCompleted || step.Status != types.StepStatusCompleted || step.FindingsJSON == nil {
		t.Fatalf("run status %s, test step %+v, want completed with the correction's report", status, step)
	}
	findings, err := types.ParseFindingsJSON(*step.FindingsJSON)
	if err != nil {
		t.Fatalf("parse Test findings: %v", err)
	}
	if findings.Verdict != types.TestVerdictGo || len(findings.Scenarios) != 1 || findings.Scenarios[0].Name != toolTurnScenarioName {
		t.Fatalf("recorded Test report = verdict %q scenarios %+v, want the correction's go", findings.Verdict, findings.Scenarios)
	}
}
