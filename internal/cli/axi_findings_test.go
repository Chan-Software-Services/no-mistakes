package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/gatecontext"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// findingsDoc is what a worker parses out of `axi findings`.
type findingsDoc struct {
	Step           string   `toon:"step"`
	Run            string   `toon:"run"`
	Status         string   `toon:"status"`
	Summary        string   `toon:"summary"`
	Risk           string   `toon:"risk"`
	RiskRationale  string   `toon:"risk_rationale"`
	Verdict        string   `toon:"verdict"`
	TestingSummary string   `toon:"testing_summary"`
	Tested         []string `toon:"tested"`
	Scenarios      []struct {
		Name     string `toon:"name"`
		Result   string `toon:"result"`
		Live     bool   `toon:"live"`
		Evidence string `toon:"evidence"`
		Reason   string `toon:"reason"`
	} `toon:"scenarios"`
}

// findingRowsDoc is the findings table of `axi findings`; a step without
// findings prints a line under the same key instead.
type findingRowsDoc struct {
	Findings []struct {
		ID          string `toon:"id"`
		Severity    string `toon:"severity"`
		File        string `toon:"file"`
		Line        string `toon:"line"`
		Action      string `toon:"action"`
		Description string `toon:"description"`
	} `toon:"findings"`
}

type findingsLineDoc struct {
	Run      string `toon:"run"`
	Status   string `toon:"status"`
	Findings string `toon:"findings"`
}

type errorDoc struct {
	Error string   `toon:"error"`
	Help  []string `toon:"help"`
}

func decodeInto(t *testing.T, out string, v any) {
	t.Helper()
	if err := toon.UnmarshalString(out, v); err != nil {
		t.Fatalf("decode axi findings TOON into %T: %v\n%s", v, err, out)
	}
}

func decodeFindingRows(t *testing.T, out string) findingRowsDoc {
	t.Helper()
	var doc findingRowsDoc
	decodeInto(t, out, &doc)
	return doc
}

func axiFindingsOutput(t *testing.T, step, runID string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	err := runAxiFindings(cmd, step, runID)
	return out.String(), err
}

func decodeFindingsDoc(t *testing.T, out string) findingsDoc {
	t.Helper()
	var doc findingsDoc
	decodeInto(t, out, &doc)
	return doc
}

func exitCodeOf(err error) int {
	var exit *exitError
	if errors.As(err, &exit) {
		return exit.code
	}
	return -1
}

// insertStepWithFindings records a step of run in status with findings.
func insertStepWithFindings(t *testing.T, database *db.DB, runID string, step types.StepName, status types.StepStatus, findings *types.Findings) *db.StepResult {
	t.Helper()
	sr, err := database.InsertStepResult(runID, step)
	if err != nil {
		t.Fatalf("insert %s step: %v", step, err)
	}
	if err := database.UpdateStepStatus(sr.ID, status); err != nil {
		t.Fatalf("set %s step status: %v", step, err)
	}
	if findings != nil {
		raw, err := types.MarshalFindingsJSON(*findings)
		if err != nil {
			t.Fatalf("marshal findings: %v", err)
		}
		if err := database.SetStepFindings(sr.ID, raw); err != nil {
			t.Fatalf("set %s findings: %v", step, err)
		}
	}
	return sr
}

func insertBranchRun(t *testing.T, database *db.DB, repo *db.Repo, branch string, status types.RunStatus) *db.Run {
	t.Helper()
	r, err := database.InsertRun(repo.ID, branch, "head-"+branch, "base")
	if err != nil {
		t.Fatalf("insert %s run: %v", branch, err)
	}
	if err := database.UpdateRunStatus(r.ID, status); err != nil {
		t.Fatalf("set %s run status: %v", branch, err)
	}
	return r
}

// longFindingDescription is as long as a recorded finding a worker could not
// read whole (669 characters), with the commas and quotes a real description
// carries.
func longFindingDescription(t *testing.T) string {
	t.Helper()
	const sentence = "The loader reads \"flags.enabled\" before the config file is parsed, so a missing file leaves it nil; "
	desc := strings.Repeat(sentence, 7)[:668] + "."
	if got := len([]rune(desc)); got != 669 {
		t.Fatalf("description is %d characters, want 669", got)
	}
	return desc
}

func TestAxiFindingsPrintsALongFindingWhole(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/mine")
	chdir(t, repoDir)

	description := longFindingDescription(t)
	summary := strings.Repeat("a summary longer than the gate shows, ", 40)
	mine := insertBranchRun(t, database, repo, "feature/mine", types.RunRunning)
	insertStepWithFindings(t, database, mine.ID, types.StepReview, types.StepStatusAwaitingApproval, &types.Findings{
		Summary:       summary,
		RiskLevel:     "medium",
		RiskRationale: "the loader can read a nil flag",
		Items: []types.Finding{
			{ID: "long-1", Severity: "warning", File: "internal/config/load.go", Line: 42, Action: types.ActionAskUser, Description: description},
			{ID: "short-2", Severity: "info", Action: types.ActionNoOp, Description: "a note"},
		},
	})

	out, err := axiFindingsOutput(t, "review", "")
	if err != nil {
		t.Fatalf("axi findings: %v\n%s", err, out)
	}
	doc := decodeFindingsDoc(t, out)
	if doc.Step != "review" || doc.Run != mine.ID || doc.Status != string(types.StepStatusAwaitingApproval) {
		t.Errorf("header = step %q run %q status %q:\n%s", doc.Step, doc.Run, doc.Status, out)
	}
	if doc.Summary != summary {
		t.Errorf("summary was not printed whole:\ngot  %q\nwant %q", doc.Summary, summary)
	}
	if doc.Risk != "medium" || doc.RiskRationale != "the loader can read a nil flag" {
		t.Errorf("risk = %q, risk_rationale = %q", doc.Risk, doc.RiskRationale)
	}
	rows := decodeFindingRows(t, out)
	if len(rows.Findings) != 2 {
		t.Fatalf("findings = %+v, want 2 rows:\n%s", rows.Findings, out)
	}
	got := rows.Findings[0]
	if got.ID != "long-1" || got.Severity != "warning" || got.File != "internal/config/load.go" || got.Line != "42" || got.Action != string(types.ActionAskUser) {
		t.Errorf("first row = %+v", got)
	}
	if got.Description != description {
		t.Errorf("description was not printed whole:\ngot  %q\nwant %q", got.Description, description)
	}
	if rows.Findings[1].Line != "" || rows.Findings[1].File != "" {
		t.Errorf("a finding with no file or line printed file %q line %q", rows.Findings[1].File, rows.Findings[1].Line)
	}
	if strings.Contains(out, "truncated") {
		t.Errorf("axi findings cut something:\n%s", out)
	}
	for _, absent := range []string{"verdict", "testing_summary", "tested", "scenarios"} {
		if strings.Contains(out, absent+":") || strings.Contains(out, absent+"[") {
			t.Errorf("a review step printed the Test field %q:\n%s", absent, out)
		}
	}
}

func TestAxiFindingsPrintsTheTestStepsVerdictAndScenarios(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/mine")
	chdir(t, repoDir)

	mine := insertBranchRun(t, database, repo, "feature/mine", types.RunCompleted)
	insertStepWithFindings(t, database, mine.ID, types.StepTest, types.StepStatusCompleted, &types.Findings{
		Summary:        "tests passed",
		Verdict:        "go",
		TestingSummary: "drove the config loader against a fresh file and read the flag back",
		Tested:         []string{"go test ./internal/config/...", "no-mistakes axi run --intent demo"},
		Scenarios: []types.TestScenario{
			{Name: "the loader reads the flag from a fresh file", Result: "pass", Live: true, Evidence: "the loader printed enabled=true, read from the new file"},
			{Name: "the loader on a read-only home", Result: "untested", Reason: "the sandbox has no read-only mount, so the case cannot be driven here"},
		},
	})

	out, err := axiFindingsOutput(t, "test", "")
	if err != nil {
		t.Fatalf("axi findings: %v\n%s", err, out)
	}
	doc := decodeFindingsDoc(t, out)
	if doc.Verdict != "go" || doc.TestingSummary != "drove the config loader against a fresh file and read the flag back" {
		t.Errorf("verdict %q testing_summary %q:\n%s", doc.Verdict, doc.TestingSummary, out)
	}
	if strings.Join(doc.Tested, "|") != "go test ./internal/config/...|no-mistakes axi run --intent demo" {
		t.Errorf("tested = %q:\n%s", doc.Tested, out)
	}
	if len(doc.Scenarios) != 2 {
		t.Fatalf("scenarios = %+v:\n%s", doc.Scenarios, out)
	}
	if s := doc.Scenarios[0]; s.Name != "the loader reads the flag from a fresh file" || s.Result != "pass" || !s.Live || s.Evidence != "the loader printed enabled=true, read from the new file" {
		t.Errorf("first scenario = %+v", s)
	}
	if s := doc.Scenarios[1]; s.Result != "untested" || s.Live || s.Reason != "the sandbox has no read-only mount, so the case cannot be driven here" {
		t.Errorf("second scenario = %+v", s)
	}
	var line findingsLineDoc
	decodeInto(t, out, &line)
	if line.Findings != `no findings recorded for step "test" in this run` {
		t.Errorf("a Test step with no findings lacks the no-findings line:\n%s", out)
	}
}

func TestAxiFindingsStepWithNoFindings(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/mine")
	chdir(t, repoDir)

	mine := insertBranchRun(t, database, repo, "feature/mine", types.RunRunning)
	insertStepWithFindings(t, database, mine.ID, types.StepLint, types.StepStatusPending, nil)

	// lint has a row with nothing recorded; document has no row in this run.
	for _, tc := range []struct{ step, status string }{{"lint", "pending"}, {"document", "not recorded"}} {
		out, err := axiFindingsOutput(t, tc.step, "")
		if err != nil {
			t.Fatalf("axi findings --step %s: %v\n%s", tc.step, err, out)
		}
		var doc findingsLineDoc
		decodeInto(t, out, &doc)
		want := `no findings recorded for step "` + tc.step + `" in this run`
		if doc.Findings != want || doc.Run != mine.ID || doc.Status != tc.status {
			t.Errorf("axi findings --step %s = %+v, want run %s status %q findings %q:\n%s", tc.step, doc, mine.ID, tc.status, want, out)
		}
	}
}

func TestAxiFindingsRefusesAMissingOrUnknownStepLikeLogs(t *testing.T) {
	repoDir, _, _, _ := setupAxiQueryRepo(t)
	chdir(t, repoDir)

	for _, tc := range []struct {
		step, want string
	}{
		{"", "--step is required"},
		{"  ", "--step is required"},
		{"bogus", `unknown step "bogus"`},
		{"gate.lint.x/../../escape", `unknown step "gate.lint.x/../../escape"`},
	} {
		out, err := axiFindingsOutput(t, tc.step, "")
		if code := exitCodeOf(err); code != 2 {
			t.Errorf("--step %q exit code = %d, want 2:\n%s", tc.step, code, out)
		}
		var doc errorDoc
		decodeInto(t, out, &doc)
		if doc.Error != tc.want || len(doc.Help) != 1 || doc.Help[0] != validLogStepsHelp {
			t.Errorf("--step %q = %+v, want error %q with the valid-steps help", tc.step, doc, tc.want)
		}
	}
}

func TestAxiFindingsDoesNotReadAnotherBranchesRun(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/mine")
	chdir(t, repoDir)

	other := insertBranchRun(t, database, repo, "feature/other", types.RunRunning)
	insertStepWithFindings(t, database, other.ID, types.StepReview, types.StepStatusAwaitingApproval, &types.Findings{
		Items: []types.Finding{{ID: "other-1", Severity: "warning", Action: types.ActionAskUser, Description: "another branch's finding"}},
	})

	out, err := axiFindingsOutput(t, "review", "")
	if code := exitCodeOf(err); code != 1 {
		t.Fatalf("exit code = %d, want 1:\n%s", code, out)
	}
	if strings.Contains(out, other.ID) || strings.Contains(out, "another branch's finding") {
		t.Fatalf("axi findings read another branch's run:\n%s", out)
	}
	for _, want := range []string{"no run found for this branch to read findings from", "no-mistakes axi findings --run <id> --step <step>"} {
		if !strings.Contains(out, want) {
			t.Errorf("no-run output missing %q:\n%s", want, out)
		}
	}

	// --run reads it deliberately, observation only.
	out, err = axiFindingsOutput(t, "review", other.ID)
	if err != nil {
		t.Fatalf("axi findings --run: %v\n%s", err, out)
	}
	doc := decodeFindingsDoc(t, out)
	if rows := decodeFindingRows(t, out); doc.Run != other.ID || len(rows.Findings) != 1 || rows.Findings[0].Description != "another branch's finding" {
		t.Fatalf("explicit --run did not read the selected run:\n%s", out)
	}
	if strings.Contains(out, "axi respond") {
		t.Errorf("an explicitly selected run offered a mutation command:\n%s", out)
	}

	out, err = axiFindingsOutput(t, "review", "missing-run")
	var missing errorDoc
	decodeInto(t, out, &missing)
	if code := exitCodeOf(err); code != 1 || missing.Error != `run "missing-run" not found` {
		t.Errorf("unknown --run: exit %d:\n%s", code, out)
	}
}

func TestAxiFindingsPrefersTheBranchsActiveRunThenItsMostRecent(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/mine")
	chdir(t, repoDir)

	done := insertBranchRun(t, database, repo, "feature/mine", types.RunCompleted)
	insertStepWithFindings(t, database, done.ID, types.StepReview, types.StepStatusCompleted, &types.Findings{
		Items: []types.Finding{{ID: "old", Severity: "info", Action: types.ActionNoOp, Description: "from the finished run"}},
	})
	out, err := axiFindingsOutput(t, "review", "")
	if err != nil || decodeFindingsDoc(t, out).Run != done.ID {
		t.Fatalf("with no active run, axi findings did not read the branch's most recent run: %v\n%s", err, out)
	}

	active := insertBranchRun(t, database, repo, "feature/mine", types.RunRunning)
	insertStepWithFindings(t, database, active.ID, types.StepReview, types.StepStatusAwaitingApproval, &types.Findings{
		Items: []types.Finding{{ID: "new", Severity: "warning", Action: types.ActionAskUser, Description: "from the active run"}},
	})
	out, err = axiFindingsOutput(t, "review", "")
	if err != nil || decodeFindingsDoc(t, out).Run != active.ID {
		t.Fatalf("axi findings did not read the branch's active run: %v\n%s", err, out)
	}
}

func TestAxiFindingsDetachedHEADHelp(t *testing.T) {
	repoDir, _, _, _ := setupAxiQueryRepo(t)
	run(t, repoDir, "git", "checkout", "--detach")
	chdir(t, repoDir)

	out, err := axiFindingsOutput(t, "review", "")
	if err == nil {
		t.Fatalf("detached findings unexpectedly found a run:\n%s", out)
	}
	for _, want := range []string{"no current branch", "axi findings --run <id> --step <step>", "check out a branch"} {
		if !strings.Contains(out, want) {
			t.Errorf("detached findings help missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "axi run --intent") || strings.Contains(out, "axi logs") {
		t.Errorf("detached findings help offered another command:\n%s", out)
	}
}

func TestAxiFindingsIsReachableFromTheCLI(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	run(t, repoDir, "git", "checkout", "-b", "feature/mine")
	chdir(t, repoDir)
	mine := insertBranchRun(t, database, repo, "feature/mine", types.RunRunning)
	insertStepWithFindings(t, database, mine.ID, types.StepReview, types.StepStatusAwaitingApproval, &types.Findings{
		Items: []types.Finding{{ID: "f1", Severity: "warning", Action: types.ActionAskUser, Description: "read me"}},
	})

	out, err := executeCmd("axi", "findings", "--step", "review")
	if err != nil {
		t.Fatalf("no-mistakes axi findings: %v\n%s", err, out)
	}
	if doc := decodeFindingRows(t, out); len(doc.Findings) != 1 || doc.Findings[0].Description != "read me" {
		t.Fatalf("no-mistakes axi findings output:\n%s", out)
	}
}

// The gate names `axi findings` exactly when it cut something, before the
// full-log line, and with --run for an explicitly selected run.
func TestGateHelpNamesAxiFindingsOnlyWhenSomethingWasCut(t *testing.T) {
	long := strings.Repeat("d", maxFindingDesc+1)
	cases := []struct {
		name     string
		findings types.Findings
		cut      bool
	}{
		{"nothing cut", types.Findings{Summary: strings.Repeat("s", maxGateSummary), Items: []types.Finding{{ID: "a", Description: strings.Repeat("d", maxFindingDesc)}}}, false},
		{"description cut", types.Findings{Summary: "short", Items: []types.Finding{{ID: "a", Description: "short"}, {ID: "b", Description: long}}}, true},
		{"summary cut", types.Findings{Summary: strings.Repeat("s", maxGateSummary+1)}, true},
		{"multibyte at the limit", types.Findings{Items: []types.Finding{{ID: "a", Description: strings.Repeat("é", maxFindingDesc)}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := types.MarshalFindingsJSON(tc.findings)
			if err != nil {
				t.Fatal(err)
			}
			gate := stepView{Name: "review", Status: "awaiting_approval", FindingsJSON: raw}
			for _, mode := range []struct {
				name    string
				fields  []toon.Field
				command string
				logLine string
			}{
				{"branch", gateFields(gate), "no-mistakes axi findings --step review", "no-mistakes axi logs --step review --full"},
				{"explicit run", inspectionOnlyGateFields(gate, "run-7"), "no-mistakes axi findings --run run-7 --step review", "no-mistakes axi logs --run run-7 --step review --full"},
			} {
				var doc struct {
					Help []string `toon:"help"`
				}
				out := axiDoc(mode.fields...)
				if err := toon.UnmarshalString(out, &doc); err != nil {
					t.Fatalf("decode gate: %v\n%s", err, out)
				}
				want := "Run `" + mode.command + "` to read every finding whole"
				findingsAt, logAt := -1, -1
				for i, line := range doc.Help {
					if line == want {
						findingsAt = i
					}
					if strings.Contains(line, mode.logLine) {
						logAt = i
					}
					if strings.Contains(line, "axi findings") && line != want {
						t.Errorf("%s: unexpected findings help %q", mode.name, line)
					}
				}
				if !tc.cut {
					if findingsAt >= 0 {
						t.Errorf("%s: nothing was cut but the help names axi findings:\n%s", mode.name, out)
					}
					continue
				}
				if findingsAt < 0 || logAt < 0 || findingsAt+1 != logAt {
					t.Errorf("%s: help line %q at %d, full-log line at %d; want it right before:\n%s", mode.name, want, findingsAt, logAt, out)
				}
			}
		})
	}
}

// The cut itself is unchanged: the gate still shows 600 and 1200 characters
// and the same marker.
func TestGateStillCutsAtTheSameLimitsWithTheSameMarker(t *testing.T) {
	if maxFindingDesc != 600 || maxGateSummary != 1200 {
		t.Fatalf("limits = %d and %d, want 600 and 1200", maxFindingDesc, maxGateSummary)
	}
	if got := truncate(strings.Repeat("x", 601), maxFindingDesc); got != strings.Repeat("x", 600)+"… (truncated, 601 chars total)" {
		t.Fatalf("marker changed: %q", got[590:])
	}
}

// A gate agent inside a step may read findings like status and logs, and the
// nested-context refusal names the command among the allowed ones.
func TestAxiFindingsIsAllowedInANestedGateContext(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"axi", "findings"})
	if err != nil || cmd.CommandPath() != "no-mistakes axi findings" {
		t.Fatalf("find axi findings: %v (path %q)", err, cmd.CommandPath())
	}
	if mutatesPipelineControl(cmd) {
		t.Fatal("axi findings is treated as pipeline control")
	}
	for _, tc := range []struct {
		result gatecontext.Result
		want   string
	}{
		{gatecontext.Result{Nested: true}, "no-mistakes axi findings --step <phase>"},
		{gatecontext.Result{Nested: true, RunID: "run-safe"}, "no-mistakes axi findings --run run-safe --step <phase>"},
	} {
		var out bytes.Buffer
		refused := newAxiRunCmd()
		refused.SetOut(&out)
		_ = emitGateContextRefusal(refused, tc.result)
		var doc struct {
			Allowed []string `toon:"allowed"`
		}
		if err := toon.UnmarshalString(out.String(), &doc); err != nil {
			t.Fatalf("decode refusal: %v\n%s", err, out.String())
		}
		if !contains(doc.Allowed, tc.want) {
			t.Errorf("refusal allowed = %q, want %q among them", doc.Allowed, tc.want)
		}
	}
}
