//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// longReviewFindingDescription is a review finding as long as T224's (669
// characters), past the gate's 600-character cut, with the commas and quotes
// a real description carries so the TOON row has to quote it.
func longReviewFindingDescription(t *testing.T) string {
	t.Helper()
	const sentence = "The loader reads \"flags.enabled\" before the config file is parsed, so a missing file leaves it nil; "
	desc := strings.Repeat(sentence, 7)
	desc = desc[:668] + "."
	if got := len([]rune(desc)); got != 669 {
		t.Fatalf("long finding description is %d characters, want 669", got)
	}
	return desc
}

// axiFindingsScenario parks the review step on one ask-user finding whose
// description is longer than the gate shows, and lets every other step pass
// with a recorded Test verdict and scenario.
func axiFindingsScenario(t *testing.T, description string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "axi-findings-scenario.yaml")
	content := `actions:
  - match: "Review the code changes and return structured findings"
    text: "review found a long warning"
    structured:
      findings:
        - id: "long-1"
          severity: warning
          file: "feature.txt"
          line: 3
          description: ` + strconv.Quote(description) + `
          action: ask-user
      summary: "found 1 issue"
      risk_level: medium
      risk_rationale: "the warning needs a human decision"
      risk_scope: source-or-external
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks detected in the diff"
      risk_scope: source-or-external
      tested:
        - "fakeagent: simulated test run"
      testing_summary: "simulated tests passed against the feature flag loader"
      scenarios:
        - name: "the config loader reads the feature flag from a fresh file"
          result: pass
          live: true
          evidence: "fakeagent: the loader printed the flag value it read"
          reason: ""
      verdict: go
      artifacts: []
      title: "feat: fakeagent change"
      body: "## Summary\nfakeagent canned PR body"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write axi findings scenario: %v", err)
	}
	return path
}

type axiFindingsDoc struct {
	Step           string   `toon:"step"`
	Run            string   `toon:"run"`
	Status         string   `toon:"status"`
	Summary        string   `toon:"summary"`
	Verdict        string   `toon:"verdict"`
	TestingSummary string   `toon:"testing_summary"`
	Tested         []string `toon:"tested"`
	Scenarios      []struct {
		Name     string `toon:"name"`
		Result   string `toon:"result"`
		Live     bool   `toon:"live"`
		Evidence string `toon:"evidence"`
	} `toon:"scenarios"`
}

// axiFindingRowsDoc is the findings table; a step with no findings prints a
// line under the same key instead.
type axiFindingRowsDoc struct {
	Findings []struct {
		ID          string `toon:"id"`
		File        string `toon:"file"`
		Description string `toon:"description"`
	} `toon:"findings"`
}

// TestAxiFindingsPrintsALongFindingWholeJourney models T224: a worker who may
// not read the gate's directory was shown a 669-character finding cut at 600
// characters, and no gate command printed it whole. The run parks on such a
// finding, the gate points at `axi findings`, and that command reads the
// finding back byte for byte, then the Test step's recorded verdict.
func TestAxiFindingsPrintsALongFindingWholeJourney(t *testing.T) {
	description := longReviewFindingDescription(t)
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: axiFindingsScenario(t, description)})

	h.CommitChange("init-findings", "seed.txt", "seed\n", "seed for axi findings")
	initWorktree := h.AddWorktree("init-findings")
	if out, err := h.RunInDir(initWorktree, "init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	h.CommitChange("feature/findings", "feature.txt", "one\ntwo\nthree\n", "add feature change")
	fw := h.AddWorktree("feature/findings")

	gateOut, err := h.RunInDir(fw, "axi", "run", "--intent", axiIntent)
	if err != nil {
		t.Fatalf("axi run (expected to stop at gate, exit 0): %v\n%s", err, gateOut)
	}
	if strings.Contains(gateOut, description) {
		t.Fatalf("the gate printed the long finding whole; this journey needs it cut:\n%s", gateOut)
	}
	for _, want := range []string{"(truncated, 669 chars total)", "Run `no-mistakes axi findings --step review` to read every finding whole"} {
		if !strings.Contains(gateOut, want) {
			t.Errorf("axi run gate output missing %q in:\n%s", want, gateOut)
		}
	}
	gated := waitForStepStatus(t, h, "feature/findings", types.StepReview, types.StepStatusAwaitingApproval, 60*time.Second)
	if gated == nil {
		t.Fatal("expected feature/findings run to be awaiting approval")
	}

	out, err := h.RunInDir(fw, "axi", "findings", "--step", "review")
	if err != nil {
		t.Fatalf("axi findings --step review: %v\n%s", err, out)
	}
	var doc axiFindingsDoc
	if err := toon.UnmarshalString(out, &doc); err != nil {
		t.Fatalf("decode axi findings: %v\n%s", err, out)
	}
	if doc.Step != "review" || doc.Run != gated.ID || doc.Status != string(types.StepStatusAwaitingApproval) {
		t.Errorf("axi findings header = step %q run %q status %q, want review %s awaiting_approval:\n%s", doc.Step, doc.Run, doc.Status, gated.ID, out)
	}
	var rows axiFindingRowsDoc
	if err := toon.UnmarshalString(out, &rows); err != nil {
		t.Fatalf("decode axi findings rows: %v\n%s", err, out)
	}
	if len(rows.Findings) != 1 || rows.Findings[0].ID != "long-1" || rows.Findings[0].File != "feature.txt" {
		t.Fatalf("axi findings rows = %+v, want the one long-1 finding:\n%s", rows.Findings, out)
	}
	if rows.Findings[0].Description != description {
		t.Fatalf("axi findings did not print the description whole:\ngot  %q\nwant %q", rows.Findings[0].Description, description)
	}
	if strings.Contains(out, "truncated") {
		t.Errorf("axi findings cut something:\n%s", out)
	}

	if out, err := h.RunInDir(fw, "axi", "respond", "--action", "approve"); err != nil {
		t.Fatalf("axi respond approve: %v\n%s", err, out)
	}
	if completed := h.WaitForRun("feature/findings", 60*time.Second); completed.Status != types.RunCompleted {
		t.Fatalf("feature/findings run status = %s, want completed", completed.Status)
	}

	testOut, err := h.RunInDir(fw, "axi", "findings", "--step", "test")
	if err != nil {
		t.Fatalf("axi findings --step test: %v\n%s", err, testOut)
	}
	var testDoc axiFindingsDoc
	if err := toon.UnmarshalString(testOut, &testDoc); err != nil {
		t.Fatalf("decode axi findings --step test: %v\n%s", err, testOut)
	}
	if testDoc.Verdict != "go" || testDoc.TestingSummary != "simulated tests passed against the feature flag loader" {
		t.Errorf("axi findings --step test verdict %q testing summary %q:\n%s", testDoc.Verdict, testDoc.TestingSummary, testOut)
	}
	if len(testDoc.Tested) != 1 || testDoc.Tested[0] != "fakeagent: simulated test run" {
		t.Errorf("axi findings --step test tested = %q:\n%s", testDoc.Tested, testOut)
	}
	if len(testDoc.Scenarios) != 1 || testDoc.Scenarios[0].Name != "the config loader reads the feature flag from a fresh file" ||
		testDoc.Scenarios[0].Result != "pass" || !testDoc.Scenarios[0].Live {
		t.Errorf("axi findings --step test scenarios = %+v:\n%s", testDoc.Scenarios, testOut)
	}
}
