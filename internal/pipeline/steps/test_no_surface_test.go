package steps

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const noSurfaceDocsFindingsJSON = `{
  "findings": [],
  "summary": "",
  "tested": ["read docs/guide.md"],
  "testing_summary": "docs-only change has no running product to drive",
  "artifacts": [],
  "scenarios": [{"name":"guide explains the new flag","result":"untested","live":false,"evidence":"","reason":"a Markdown page has no running product no-mistakes can drive"}],
  "verdict": "no-surface"
}`

// commitFeatureFiles resets the feature branch to baseSHA and commits files
// (path -> content) on it, so the run's changed set is exactly those paths.
func commitFeatureFiles(t *testing.T, dir, baseSHA string, files map[string]string) string {
	t.Helper()
	gitCmd(t, dir, "checkout", "-B", "feature", baseSHA)
	for name, content := range files {
		writeWorktreeFile(t, dir, name, content)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "change files")
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

func writeWorktreeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func cannedTestAgent(output string) *mockAgent {
	return &mockAgent{
		name: "test",
		runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: json.RawMessage(output)}, nil
		},
	}
}

func parsedTestOutcome(t *testing.T, raw string) types.Findings {
	t.Helper()
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

func findingWithID(findings types.Findings, id string) *types.Finding {
	for i := range findings.Items {
		if findings.Items[i].ID == id {
			return &findings.Items[i]
		}
	}
	return nil
}

func noSurfaceParkFinding(findings types.Findings) *types.Finding {
	for i := range findings.Items {
		if strings.Contains(findings.Items[i].Description, "this change has no live-validatable surface; proceed without live validation?") {
			return &findings.Items[i]
		}
	}
	return nil
}

var docsNoSurfaceRules = []string{"docs/**", "*.md"}

// TestTestStep_NoSurfaceCoveredDocsOnlySkips is the reproduction: a
// docs-only change the trusted test.no_surface_paths covers, with the
// evidence agent's own no-surface verdict, completes without parking.
func TestTestStep_NoSurfaceCoveredDocsOnlySkips(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n"})

	ag := cannedTestAgent(noSurfaceDocsFindingsJSON)
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Config.Test.NoSurfacePaths = docsNoSurfaceRules

	outcome, err := (&TestStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("agent calls = %d, want the evidence turn to run once", len(ag.calls))
	}
	if outcome.NeedsApproval {
		t.Fatalf("covered docs-only no-surface must not park, findings: %s", outcome.Findings)
	}
	findings := parsedTestOutcome(t, outcome.Findings)
	if findings.Verdict != types.TestVerdictNoSurface {
		t.Fatalf("verdict = %q, want no-surface kept", findings.Verdict)
	}
	if findings.TestedHeadSHA != headSHA {
		t.Fatalf("TestedHeadSHA = %q, want %q", findings.TestedHeadSHA, headSHA)
	}
	if len(findings.Scenarios) != 1 || findings.Scenarios[0].Result != types.ScenarioResultUntested {
		t.Fatalf("scenarios = %+v, want the agent's untested scenario kept", findings.Scenarios)
	}
	if park := noSurfaceParkFinding(findings); park != nil {
		t.Fatalf("covered change still carries the park finding: %+v", park)
	}
	skip := findingWithID(findings, types.FindingIDTestNoSurfaceSkip)
	if skip == nil {
		t.Fatalf("missing test-no-surface-skip finding in %s", outcome.Findings)
	}
	if skip.Severity != types.FindingSeverityInfo || skip.Action != types.ActionNoOp || skip.File != "" {
		t.Fatalf("skip finding = %+v, want severity info, action no-op, no file", skip)
	}
	want := "no live surface, skipped: every changed file matches the trusted test.no_surface_paths (1 file(s): docs/guide.md) and the evidence agent found no live-validatable surface (0 of 1 scenarios were driven live against the product); guide explains the new flag: a Markdown page has no running product no-mistakes can drive"
	if skip.Description != want {
		t.Fatalf("skip description =\n%q\nwant\n%q", skip.Description, want)
	}
}

// runNoSurfaceStep runs the Test step with the trusted rules set and returns
// the outcome, its parsed findings and the step log.
func runNoSurfaceStep(t *testing.T, ag *mockAgent, dir, baseSHA, headSHA string, cmds config.Commands, rules []string) (*pipeline.StepOutcome, types.Findings, []string) {
	t.Helper()
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, cmds)
	sctx.Config.Test.NoSurfacePaths = rules
	var logs []string
	sctx.Log = func(s string) {
		t.Log(s)
		logs = append(logs, s)
	}
	outcome, err := (&TestStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	return outcome, parsedTestOutcome(t, outcome.Findings), logs
}

func requireNoSurfacePark(t *testing.T, outcome *pipeline.StepOutcome, findings types.Findings, wantSuffix string) {
	t.Helper()
	if !outcome.NeedsApproval {
		t.Fatalf("NeedsApproval = false, want the no-surface park (findings: %s)", outcome.Findings)
	}
	if skip := findingWithID(findings, types.FindingIDTestNoSurfaceSkip); skip != nil {
		t.Fatalf("parked run also recorded the skip: %+v", skip)
	}
	park := noSurfaceParkFinding(findings)
	if park == nil {
		t.Fatalf("missing no-surface park finding in %s", outcome.Findings)
	}
	if park.Severity != types.FindingSeverityWarning || park.Action != types.ActionAskUser {
		t.Fatalf("park = %+v, want warning ask-user", park)
	}
	if !strings.HasSuffix(park.Description, wantSuffix) {
		t.Fatalf("park description = %q, want suffix %q", park.Description, wantSuffix)
	}
	if len(types.AutoFixableFindings(findings).Items) != 0 {
		t.Fatalf("no-surface park must not be auto-fixable, got %s", outcome.Findings)
	}
}

func TestTestStep_NoSurfaceCoveredDocsOnlyLogsTheSkip(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n", "README.md": "# Readme\n"})

	outcome, findings, logs := runNoSurfaceStep(t, cannedTestAgent(noSurfaceDocsFindingsJSON), dir, baseSHA, headSHA, config.Commands{}, docsNoSurfaceRules)
	if outcome.NeedsApproval {
		t.Fatalf("covered change parked: %s", outcome.Findings)
	}
	if findingWithID(findings, types.FindingIDTestNoSurfaceSkip) == nil {
		t.Fatalf("missing skip finding in %s", outcome.Findings)
	}
	want := "no live surface, skipped: every changed file matches the trusted test.no_surface_paths (2 file(s))"
	for _, line := range logs {
		if line == want {
			return
		}
	}
	t.Fatalf("step log %q is missing %q", logs, want)
}

func TestTestStep_NoSurfaceCoveredCIWorkflowOnlySkips(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitCIWorkflowOnlyChange(t, dir, baseSHA)

	outcome, findings, _ := runNoSurfaceStep(t, cannedTestAgent(noSurfaceCIWorkflowFindingsJSON), dir, baseSHA, headSHA, config.Commands{}, []string{".github/workflows/**"})
	if outcome.NeedsApproval {
		t.Fatalf("covered CI-workflow-only no-surface must not park, findings: %s", outcome.Findings)
	}
	skip := findingWithID(findings, types.FindingIDTestNoSurfaceSkip)
	if skip == nil || !strings.Contains(skip.Description, "(1 file(s): .github/workflows/ci.yml)") {
		t.Fatalf("skip finding = %+v, want it to name .github/workflows/ci.yml", skip)
	}
}

func TestTestStep_NoSurfaceMixedDocsAndCodeParks(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n", "main.go": "package main\n"})

	outcome, findings, _ := runNoSurfaceStep(t, cannedTestAgent(noSurfaceDocsFindingsJSON), dir, baseSHA, headSHA, config.Commands{}, docsNoSurfaceRules)
	requireNoSurfacePark(t, outcome, findings, "; not on the trusted test.no_surface_paths: main.go")
}

// TestTestStep_NoSurfaceUntrackedProductFileParks covers a file the agent
// left in the worktree: a later step would commit it, so it counts.
func TestTestStep_NoSurfaceUntrackedProductFileParks(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n"})
	ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
		writeWorktreeFile(t, dir, "main.go", "package main\n")
		return &agent.Result{Output: json.RawMessage(noSurfaceDocsFindingsJSON)}, nil
	}}

	outcome, findings, _ := runNoSurfaceStep(t, ag, dir, baseSHA, headSHA, config.Commands{}, docsNoSurfaceRules)
	requireNoSurfacePark(t, outcome, findings, "; not on the trusted test.no_surface_paths: main.go")
}

// TestTestStep_NoSurfaceRenameOutOfProductCodeParks moves the base's product
// file into docs/: --no-renames lists the old path, which is uncovered.
func TestTestStep_NoSurfaceRenameOutOfProductCodeParks(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "-B", "feature", baseSHA)
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "mv", "base.txt", "docs/base.txt")
	gitCmd(t, dir, "commit", "-m", "move base into docs")
	headSHA := gitCmd(t, dir, "rev-parse", "HEAD")

	outcome, findings, _ := runNoSurfaceStep(t, cannedTestAgent(noSurfaceDocsFindingsJSON), dir, baseSHA, headSHA, config.Commands{}, docsNoSurfaceRules)
	requireNoSurfacePark(t, outcome, findings, "; not on the trusted test.no_surface_paths: base.txt")
}

// TestTestStep_NoSurfaceSkipKeepsFailingBaselinePark proves the skip
// replaces only the no-surface park: a failing commands.test still parks.
func TestTestStep_NoSurfaceSkipKeepsFailingBaselinePark(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n"})

	outcome, findings, _ := runNoSurfaceStep(t, cannedTestAgent(noSurfaceDocsFindingsJSON), dir, baseSHA, headSHA, config.Commands{Test: "exit 3"}, docsNoSurfaceRules)
	if !outcome.NeedsApproval {
		t.Fatalf("failing baseline must still park, findings: %s", outcome.Findings)
	}
	if outcome.ExitCode != 3 {
		t.Fatalf("ExitCode = %d, want the baseline's 3", outcome.ExitCode)
	}
	if findingWithID(findings, types.FindingIDTestNoSurfaceSkip) == nil {
		t.Fatalf("covered no-surface must still record the skip beside the baseline park: %s", outcome.Findings)
	}
	if park := noSurfaceParkFinding(findings); park != nil {
		t.Fatalf("covered change still carries the no-surface park: %+v", park)
	}
	var baseline bool
	for _, item := range findings.Items {
		if item.Category == types.FindingCategoryTestCommand && item.Severity == types.FindingSeverityError {
			baseline = true
		}
	}
	if !baseline {
		t.Fatalf("missing the configured-command finding: %s", outcome.Findings)
	}
}

// TestTestStep_NoSurfaceSkipKeepsAgentWarningPark proves the skip removes
// exactly one finding: an agent ask-user warning still parks.
func TestTestStep_NoSurfaceSkipKeepsAgentWarningPark(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n"})
	report := strings.Replace(noSurfaceDocsFindingsJSON, `"findings": []`,
		`"findings": [{"severity":"warning","action":"ask-user","description":"the rendered docs site could not be built to show the page"}]`, 1)

	outcome, findings, _ := runNoSurfaceStep(t, cannedTestAgent(report), dir, baseSHA, headSHA, config.Commands{}, docsNoSurfaceRules)
	if !outcome.NeedsApproval {
		t.Fatalf("an agent ask-user warning must still park, findings: %s", outcome.Findings)
	}
	if findingWithID(findings, types.FindingIDTestNoSurfaceSkip) == nil {
		t.Fatalf("missing skip finding: %s", outcome.Findings)
	}
	if park := noSurfaceParkFinding(findings); park != nil {
		t.Fatalf("covered change still carries the no-surface park: %+v", park)
	}
	var warning bool
	for _, item := range findings.Items {
		if strings.Contains(item.Description, "the rendered docs site could not be built") {
			warning = true
		}
	}
	if !warning {
		t.Fatalf("missing the agent's own warning: %s", outcome.Findings)
	}
}

func TestTestStep_NoSurfaceRulesLeaveNoGoAndInconclusive(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		report     string
		wantPrefix string
		wantAction string
	}{
		{
			name: "no-go",
			report: `{"findings":[],"summary":"","tested":["opened the docs site"],"testing_summary":"the guide page 404s","artifacts":[],
				"scenarios":[{"name":"guide page renders","result":"fail","live":true,"evidence":"the docs site returns 404 for the guide page","reason":""}],
				"verdict":"no-go"}`,
			wantPrefix: "live validation verdict: no-go",
			wantAction: types.ActionAutoFix,
		},
		{
			name: "inconclusive",
			report: `{"findings":[],"summary":"","tested":["read the diff"],"testing_summary":"the docs site could not start","artifacts":[],
				"scenarios":[{"name":"guide page renders","result":"untested","live":false,"evidence":"","reason":"the docs site needs a Node version this machine lacks"}],
				"verdict":"inconclusive"}`,
			wantPrefix: "live validation verdict: inconclusive",
			wantAction: types.ActionAskUser,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, baseSHA, _ := setupGitRepo(t)
			headSHA := commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n"})

			outcome, findings, _ := runNoSurfaceStep(t, cannedTestAgent(tc.report), dir, baseSHA, headSHA, config.Commands{}, docsNoSurfaceRules)
			if !outcome.NeedsApproval {
				t.Fatalf("%s must park on a covered change, findings: %s", tc.name, outcome.Findings)
			}
			if skip := findingWithID(findings, types.FindingIDTestNoSurfaceSkip); skip != nil {
				t.Fatalf("%s recorded the no-surface skip: %+v", tc.name, skip)
			}
			var matched *types.Finding
			for i, item := range findings.Items {
				if strings.HasPrefix(item.Description, tc.wantPrefix) {
					matched = &findings.Items[i]
				}
			}
			if matched == nil || matched.Action != tc.wantAction {
				t.Fatalf("verdict finding = %+v, want %q with action %q", matched, tc.wantPrefix, tc.wantAction)
			}
			if strings.Contains(matched.Description, "test.no_surface_paths") {
				t.Fatalf("%s finding mentions the no-surface rule: %q", tc.name, matched.Description)
			}
		})
	}
}

// TestTestStep_NoSurfaceCorrectionRebuiltReportSkips covers the common
// docs-only shape: the first report called static checks passes and the
// correction turn rebuilt it as no-surface.
func TestTestStep_NoSurfaceCorrectionRebuiltReportSkips(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n"})
	calls := 0
	ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
		calls++
		if calls == 1 {
			return &agent.Result{Output: json.RawMessage(passNotLiveFindingsJSON)}, nil
		}
		return &agent.Result{Output: json.RawMessage(noSurfaceDocsFindingsJSON)}, nil
	}}

	outcome, findings, _ := runNoSurfaceStep(t, ag, dir, baseSHA, headSHA, config.Commands{}, docsNoSurfaceRules)
	if calls != 2 {
		t.Fatalf("agent calls = %d, want the evidence turn plus one correction", calls)
	}
	if outcome.NeedsApproval {
		t.Fatalf("a rebuilt no-surface report on a covered change must not park, findings: %s", outcome.Findings)
	}
	if findingWithID(findings, types.FindingIDTestNoSurfaceSkip) == nil {
		t.Fatalf("missing skip finding: %s", outcome.Findings)
	}
	if _, ok := rebuiltReportNote(&findings); !ok {
		t.Fatalf("missing the rebuilt-report note: %s", outcome.Findings)
	}
}

func TestVerdictFindings_NoSurfaceRulesUnsetKeepsTodaysPark(t *testing.T) {
	t.Parallel()
	findings := parsedTestOutcome(t, noSurfaceDocsFindingsJSON)
	got := verdictFindings(findings, false, noSurfaceDecision{})
	want := "this change has no live-validatable surface; proceed without live validation? (0 of 1 scenarios were driven live against the product); guide explains the new flag: a Markdown page has no running product no-mistakes can drive"
	if len(got) != 1 || got[0].Description != want || got[0].ID != "" || got[0].Severity != types.FindingSeverityWarning || got[0].Action != types.ActionAskUser {
		t.Fatalf("verdictFindings() = %+v, want today's park %q", got, want)
	}
}

func TestVerdictFindings_NoSurfaceUnconfirmedParkSaysSo(t *testing.T) {
	t.Parallel()
	findings := parsedTestOutcome(t, noSurfaceDocsFindingsJSON)
	for _, decision := range []noSurfaceDecision{
		decideNoSurface(nil, errors.New("git diff: bad revision"), docsNoSurfaceRules),
		decideNoSurface(nil, nil, docsNoSurfaceRules),
	} {
		got := verdictFindings(findings, false, decision)
		want := "; the gate could not confirm every changed file is on the trusted test.no_surface_paths, so it did not decide this itself"
		if len(got) != 1 || !strings.HasSuffix(got[0].Description, want) || got[0].Action != types.ActionAskUser {
			t.Fatalf("verdictFindings() = %+v, want the park ending %q", got, want)
		}
	}
}

func TestDecideNoSurface(t *testing.T) {
	t.Parallel()
	many := []string{"docs/a.md", "docs/b.md", "docs/c.md", "docs/d.md", "docs/e.md", "docs/f.md", "docs/g.md"}
	for _, tc := range []struct {
		name    string
		changed []string
		readErr error
		rules   []string
		want    noSurfaceDecision
	}{
		{name: "rules unset", changed: []string{"docs/a.md"}, want: noSurfaceDecision{}},
		{name: "empty set", rules: docsNoSurfaceRules, want: noSurfaceDecision{configured: true, unconfirmed: true}},
		{name: "read error", changed: []string{"docs/a.md"}, readErr: errors.New("boom"), rules: docsNoSurfaceRules, want: noSurfaceDecision{configured: true, unconfirmed: true}},
		{
			name: "all covered", changed: []string{"docs/a.md", "pkg/README.md"}, rules: docsNoSurfaceRules,
			want: noSurfaceDecision{configured: true, skip: true, covered: []string{"docs/a.md", "pkg/README.md"}},
		},
		{
			name: "one uncovered", changed: []string{"docs/a.md", "main.go"}, rules: docsNoSurfaceRules,
			want: noSurfaceDecision{configured: true, covered: []string{"docs/a.md"}, uncovered: []string{"main.go"}},
		},
		{
			name: "full-path glob does not cross a slash", changed: []string{"docs/sub/a.md"}, rules: []string{"docs/*.md"},
			want: noSurfaceDecision{configured: true, uncovered: []string{"docs/sub/a.md"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := decideNoSurface(tc.changed, tc.readErr, tc.rules); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("decideNoSurface() = %+v, want %+v", got, tc.want)
			}
		})
	}

	covered := decideNoSurface(many, nil, docsNoSurfaceRules)
	skip := noSurfaceSkipFinding(covered, "0 of 1 scenarios were driven live against the product", nil)
	if !strings.Contains(skip.Description, "(7 file(s): docs/a.md, docs/b.md, docs/c.md, docs/d.md, docs/e.md and 2 more)") {
		t.Fatalf("skip description does not bound its list: %q", skip.Description)
	}
	mixed := decideNoSurface([]string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"}, nil, docsNoSurfaceRules)
	if got, want := noSurfaceParkSuffix(mixed), "; not on the trusted test.no_surface_paths: a.go, b.go, c.go, d.go, e.go and 1 more"; got != want {
		t.Fatalf("park suffix = %q, want %q", got, want)
	}
	exact := decideNoSurface(many[:5], nil, docsNoSurfaceRules)
	if got := boundedPathList(exact.covered); got != "docs/a.md, docs/b.md, docs/c.md, docs/d.md, docs/e.md" {
		t.Fatalf("five paths must be listed whole, got %q", got)
	}
}

func TestNoSurfaceChangedPaths(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	commitFeatureFiles(t, dir, baseSHA, map[string]string{"docs/guide.md": "# Guide\n"})
	writeWorktreeFile(t, dir, "base.txt", "edited but not committed\n")
	writeWorktreeFile(t, dir, "notes/todo.txt", "untracked\n")
	writeWorktreeFile(t, dir, ".gitignore", "ignored.log\n")
	writeWorktreeFile(t, dir, "ignored.log", "ignored\n")

	got, err := noSurfaceChangedPaths(context.Background(), dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"base.txt", "docs/guide.md", ".gitignore", "notes/todo.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("noSurfaceChangedPaths() = %q, want %q", got, want)
	}

	if _, err := noSurfaceChangedPaths(context.Background(), dir, "0123456789abcdef0123456789abcdef01234567"); err == nil {
		t.Fatal("an unknown base SHA must return an error")
	}
	if _, err := noSurfaceChangedPaths(context.Background(), dir, ""); err == nil {
		t.Fatal("an empty base must return an error")
	}
}
