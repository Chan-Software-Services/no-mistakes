//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// trustedNoSurfacePaths is what a maintainer adds to the default-branch
// .no-mistakes.yaml. The second entry keeps the pushed-config case below honest:
// that branch parks only because its pushed no_surface_paths is ignored, never
// because the config file it changes is itself uncovered.
const trustedNoSurfacePaths = "test:\n  no_surface_paths:\n    - 'docs/**'\n    - '.no-mistakes.yaml'\n"

// pushedNoSurfacePathsWideningEverything is what a contributor ships on their
// own branch to exempt every file, its code included, from live validation.
const pushedNoSurfacePathsWideningEverything = "test:\n  no_surface_paths:\n    - '*'\n"

const (
	noSurfaceScenarioName   = "synthetic docs scenario"
	noSurfaceScenarioReason = "synthetic docs-only change"
	// noSurfaceCoverage and noSurfaceReasons are the parts of the skip and park
	// descriptions the canned report decides.
	noSurfaceCoverage = "(0 of 1 scenarios were driven live against the product)"
	noSurfaceReasons  = "; " + noSurfaceScenarioName + ": " + noSurfaceScenarioReason
)

// noSurfaceFindingsDoc is `axi findings --step test` with the columns this
// journey judges the skip and the park by.
type noSurfaceFindingsDoc struct {
	Status   string `toon:"status"`
	Verdict  string `toon:"verdict"`
	Findings []struct {
		ID          string `toon:"id"`
		Severity    string `toon:"severity"`
		File        string `toon:"file"`
		Action      string `toon:"action"`
		Description string `toon:"description"`
	} `toon:"findings"`
}

// TestTestNoSurfaceSkipJourney is the end-to-end proof of test.no_surface_paths
// through the real CLI, daemon and gate with a canned evidence agent that
// answers no-surface on every change. The gate decides no-surface itself only
// when the trusted default-branch paths cover every changed file; a change
// outside them, or a pushed branch widening them, still parks for a human.
func TestTestNoSurfaceSkipJourney(t *testing.T) {
	t.Run("covered_docs_only_change_passes_without_an_answer", func(t *testing.T) {
		h := newNoSurfaceHarness(t)
		branch := "no-surface-docs"
		h.CommitChange(branch, "docs/guide.md", "# Guide\n\nThe new flag is documented here.\n", "document the flag")

		out, err := h.Run("axi", "run", "--intent", "Document the new flag", "--skip", "pr,ci")
		if err != nil {
			t.Fatalf("axi run: %v\n%s", err, out)
		}
		// No axi respond was sent: the run settled on its own.
		assertTestExceptionOutput(t, out, "passed", "")
		run := h.WaitForRun(branch, 60*time.Second)
		if run.Status != types.RunCompleted {
			t.Fatalf("run status = %s, want completed: error=%v", run.Status, deref(run.Error))
		}

		doc := noSurfaceTestFindings(t, h)
		if doc.Verdict != string(types.TestVerdictNoSurface) {
			t.Errorf("recorded verdict = %q, want the agent's no-surface kept", doc.Verdict)
		}
		if len(doc.Findings) != 1 {
			t.Fatalf("test findings = %+v, want only the skip finding", doc.Findings)
		}
		skip := doc.Findings[0]
		wantDescription := "no live surface, skipped: every changed file matches the trusted test.no_surface_paths (1 file(s): docs/guide.md) and the evidence agent found no live-validatable surface " +
			noSurfaceCoverage + noSurfaceReasons
		if skip.ID != types.FindingIDTestNoSurfaceSkip || skip.Severity != string(types.FindingSeverityInfo) ||
			skip.Action != string(types.ActionNoOp) || skip.File != "" || skip.Description != wantDescription {
			t.Errorf("skip finding = %+v\nwant id %s, info, no-op, no file, description\n%q",
				skip, types.FindingIDTestNoSurfaceSkip, wantDescription)
		}

		logs, err := h.Run("axi", "logs", "--run", run.ID, "--step", "test", "--full")
		if err != nil {
			t.Fatalf("axi logs: %v\n%s", err, logs)
		}
		if want := "no live surface, skipped: every changed file matches the trusted test.no_surface_paths (1 file(s))"; !strings.Contains(logs, want) {
			t.Errorf("test step log is missing %q\n%s", want, logs)
		}

		// An independent read-only connection proves nobody approved the step
		// and that the skip is no Test exception.
		database, err := db.OpenReadOnly(filepath.Join(h.NMHome, "state.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		steps, err := database.GetStepsByRun(run.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range steps {
			if step.StepName != types.StepTest {
				continue
			}
			if step.Status != types.StepStatusCompleted || step.ApprovalReason != nil || step.OverrideReason != nil {
				t.Fatalf("test step was not settled by the gate alone: %+v", step)
			}
		}
	})

	t.Run("change_outside_the_trusted_paths_parks", func(t *testing.T) {
		h := newNoSurfaceHarness(t)
		branch := "no-surface-feature"
		h.CommitChange(branch, "feature.txt", "synthetic feature\n", "add feature")
		assertNoSurfaceParksNaming(t, h, branch, "feature.txt")
	})

	t.Run("pushed_paths_cannot_widen_the_trusted_ones", func(t *testing.T) {
		h := newNoSurfaceHarness(t)
		branch := "no-surface-widened"
		h.CommitChange(branch, ".no-mistakes.yaml", harnessRepoConfigWithExtra(pushedNoSurfacePathsWideningEverything),
			"contributor: exempt every file from live validation")
		h.CommitChange(branch, "feature.txt", "synthetic feature\n", "add feature")
		assertNoSurfaceParksNaming(t, h, branch, "feature.txt")
	})
}

// newNoSurfaceHarness starts a harness whose trusted default branch lists
// test.no_surface_paths and whose evidence agent answers no-surface.
func newNoSurfaceHarness(t *testing.T) *Harness {
	t.Helper()
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: noSurfaceAgentScenario(t)})
	pushMainRepoConfig(t, h, harnessRepoConfigWithExtra(trustedNoSurfacePaths))
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return h
}

// noSurfaceAgentScenario answers Test's evidence turn with a no-surface report
// and one untested scenario with its reason; every other agent phase completes
// without findings.
func noSurfaceAgentScenario(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	data := `actions:
  - match: "You are validating a code change by driving the product itself."
    text: "no live surface"
    structured:
      findings: []
      summary: "no live surface"
      tested: ["fakeagent: looked for a live surface"]
      testing_summary: "nothing in this change runs in the product"
      artifacts: []
      verdict: no-surface
      scenarios:
        - name: ` + noSurfaceScenarioName + `
          result: untested
          live: false
          evidence: "fakeagent: no runtime surface"
          reason: ` + noSurfaceScenarioReason + `
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "synthetic clean response"
      risk_scope: source-or-external
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// assertNoSurfaceParksNaming runs the branch and asserts Test parks on the
// ask-user no-surface finding naming uncovered, with no skip recorded.
func assertNoSurfaceParksNaming(t *testing.T, h *Harness, branch, uncovered string) {
	t.Helper()
	head, err := h.runGit(t.Context(), h.WorkDir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	out, err := h.Run("axi", "run", "--intent", "Validate the synthetic feature", "--skip", "pr,ci")
	if err != nil {
		t.Fatalf("axi run (expected to stop at the gate, exit 0): %v\n%s", err, out)
	}
	gated := waitForStepStatus(t, h, branch, types.StepTest, types.StepStatusAwaitingApproval, 60*time.Second)
	if gated.HeadSHA != strings.TrimSpace(string(head)) {
		t.Fatalf("parked run head = %s, want %s", gated.HeadSHA, head)
	}

	doc := noSurfaceTestFindings(t, h)
	if doc.Status != string(types.StepStatusAwaitingApproval) {
		t.Errorf("test step status = %q, want awaiting_approval", doc.Status)
	}
	if len(doc.Findings) != 1 {
		t.Fatalf("test findings = %+v, want only the no-surface park", doc.Findings)
	}
	park := doc.Findings[0]
	wantDescription := "this change has no live-validatable surface; proceed without live validation? " +
		noSurfaceCoverage + noSurfaceReasons + "; not on the trusted test.no_surface_paths: " + uncovered
	if park.ID == types.FindingIDTestNoSurfaceSkip || park.Severity != string(types.FindingSeverityWarning) ||
		park.Action != string(types.ActionAskUser) || park.Description != wantDescription {
		t.Errorf("park finding = %+v\nwant a warning, ask-user, description\n%q", park, wantDescription)
	}
}

// noSurfaceTestFindings reads `axi findings --step test` for the current
// branch's run.
func noSurfaceTestFindings(t *testing.T, h *Harness) noSurfaceFindingsDoc {
	t.Helper()
	out, err := h.Run("axi", "findings", "--step", "test")
	if err != nil {
		t.Fatalf("axi findings --step test: %v\n%s", err, out)
	}
	var doc noSurfaceFindingsDoc
	if err := toon.UnmarshalString(out, &doc); err != nil {
		t.Fatalf("decode axi findings --step test: %v\n%s", err, out)
	}
	return doc
}
