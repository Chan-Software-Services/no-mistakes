package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// setupSubdirFormatterRepo builds a repository laid out like a project whose
// formatter config sits in a subdirectory: front-end/.prettierrc.json and a
// front-end/package.json whose format:check script checks ../docs with that
// config. No formatter runs here (the agent is a mock returning canned
// findings); the fixture stands for that layout and for the tracked paths the
// findings name. The branch touches only front-end/src/App.vue (plus the
// template's feature.txt), so docs/OPERATIONS.md and docs/STARTUP.md are
// tracked files outside the change.
func setupSubdirFormatterRepo(t *testing.T) (dir, baseSHA, headSHA string) {
	t.Helper()
	dir, _, _ = setupGitRepo(t)
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, dir, "checkout", "main")
	write("front-end/.prettierrc.json", `{"semi": true, "singleQuote": true, "printWidth": 100, "trailingComma": "all"}`+"\n")
	write("front-end/package.json", `{"scripts": {"format:check": "prettier --config .prettierrc.json --check . ../docs"}}`+"\n")
	write("docs/OPERATIONS.md", "# Operations\n")
	write("docs/STARTUP.md", "# Startup\n")
	write("front-end/src/App.vue", "<script setup lang=\"ts\">\nconst a = 'x';\n</script>\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "project layout")
	baseSHA = gitCmd(t, dir, "rev-parse", "HEAD")

	gitCmd(t, dir, "checkout", "feature")
	gitCmd(t, dir, "merge", "--no-edit", "main")
	write("front-end/src/App.vue", "<script setup lang=\"ts\">\nconst a = \"x\"\n</script>\n")
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "touch the front end")
	headSHA = gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	return dir, baseSHA, headSHA
}

// untouchedDocsAndTouchedFileFindings is what the agent reported on the run
// that motivated the scope guard - the two untouched docs flagged under
// Prettier's default config - plus a real problem in the touched file.
const untouchedDocsAndTouchedFileFindings = `{"findings":[
	{"id":"lint-1","severity":"warning","file":"docs/OPERATIONS.md","description":"prettier --check fails","action":"ask-user","category":"lint"},
	{"id":"lint-2","severity":"warning","file":"./docs/STARTUP.md","line":3,"description":"prettier --check fails","action":"ask-user","category":"lint"},
	{"id":"lint-3","severity":"warning","file":"front-end/src/App.vue","line":2,"description":"prettier --check fails: quotes and semicolon","action":"ask-user","category":"lint"}
],"summary":"3 lint issues"}`

const untouchedDocsOnlyFindings = `{"findings":[
	{"id":"lint-1","severity":"warning","file":"docs/OPERATIONS.md","description":"prettier --check fails","action":"ask-user","category":"lint"},
	{"id":"lint-2","severity":"error","file":"docs/STARTUP.md","description":"prettier --check fails","action":"auto-fix","category":"lint"}
],"summary":"2 lint issues"}`

func lintFindingsByID(t *testing.T, raw string) map[string]Finding {
	t.Helper()
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		t.Fatalf("parse lint findings: %v\n%s", err, raw)
	}
	byID := map[string]Finding{}
	for _, f := range findings.Items {
		byID[f.ID] = f
	}
	return byID
}

// assertUntouchedDocsDemoted checks that a finding on a file outside the
// change is kept for the record but can no longer park the run.
func assertUntouchedDocsDemoted(t *testing.T, byID map[string]Finding, ids ...string) {
	t.Helper()
	for _, id := range ids {
		f, ok := byID[id]
		if !ok {
			t.Fatalf("finding %s dropped; an out-of-change finding must stay visible as information", id)
		}
		if f.Severity != "info" || f.Action != types.ActionNoOp {
			t.Errorf("finding %s on untouched %s = severity %q action %q, want info/no-op", id, f.File, f.Severity, f.Action)
		}
		if !strings.Contains(f.Description, "outside this change") {
			t.Errorf("finding %s description %q does not say it is outside the change", id, f.Description)
		}
	}
}

// assertTouchedFileKept checks the touched-file finding lint-3: present and
// exactly as the agent reported it when want is true, absent otherwise.
func assertTouchedFileKept(t *testing.T, byID map[string]Finding, want bool) {
	t.Helper()
	touched, ok := byID["lint-3"]
	if ok != want {
		t.Fatalf("touched-file finding present = %v, want %v", ok, want)
	}
	if !want {
		return
	}
	if touched.Severity != types.FindingSeverityWarning || touched.Action != types.ActionAskUser || touched.Description != "prettier --check fails: quotes and semicolon" {
		t.Fatalf("touched-file finding changed: %+v", touched)
	}
}

// TestLintStep_OwnPass_FindingsOutsideTheChangeDoNotPark is the regression for
// a gate that parked on formatter warnings for two docs the branch never
// touched: the lint step's own agent pass demotes them to information, while
// a problem in a touched file still parks as before.
func TestLintStep_OwnPass_FindingsOutsideTheChangeDoNotPark(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		output        string
		needsApproval bool
		wantTouched   bool
	}{
		{name: "only untouched docs", output: untouchedDocsOnlyFindings, needsApproval: false},
		{name: "untouched docs and a touched file", output: untouchedDocsAndTouchedFileFindings, needsApproval: true, wantTouched: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, baseSHA, headSHA := setupSubdirFormatterRepo(t)
			ag := &mockAgent{
				name: "test",
				runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
					return &agent.Result{Output: json.RawMessage(tc.output)}, nil
				},
			}
			sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})

			outcome, err := (&LintStep{}).Execute(sctx)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.NeedsApproval != tc.needsApproval {
				t.Fatalf("NeedsApproval = %v, want %v; findings %s", outcome.NeedsApproval, tc.needsApproval, outcome.Findings)
			}
			byID := lintFindingsByID(t, outcome.Findings)
			assertUntouchedDocsDemoted(t, byID, "lint-1", "lint-2")
			assertTouchedFileKept(t, byID, tc.wantTouched)
		})
	}
}

// TestLintStep_CombinedPass_FindingsOutsideTheChangeDoNotPark drives the path
// the motivating run took: with no commands.lint, the document step's
// combined pass reports the lint findings and the lint step consumes them.
func TestLintStep_CombinedPass_FindingsOutsideTheChangeDoNotPark(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		output        string
		needsApproval bool
		wantTouched   bool
	}{
		{name: "only untouched docs", output: untouchedDocsOnlyFindings, needsApproval: false},
		{name: "untouched docs and a touched file", output: untouchedDocsAndTouchedFileFindings, needsApproval: true, wantTouched: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, baseSHA, headSHA := setupSubdirFormatterRepo(t)
			calls := 0
			ag := &mockAgent{
				name: "test",
				runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
					calls++
					return &agent.Result{Output: json.RawMessage(tc.output)}, nil
				},
			}
			sctx := newHousekeepingContext(t, ag, dir, baseSHA, headSHA, config.Commands{})

			if _, err := (&DocumentStep{}).Execute(sctx); err != nil {
				t.Fatal(err)
			}
			outcome, err := (&LintStep{}).Execute(sctx)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("agent calls = %d, want 1 (the lint step consumes the combined result)", calls)
			}
			if outcome.NeedsApproval != tc.needsApproval {
				t.Fatalf("NeedsApproval = %v, want %v; findings %s", outcome.NeedsApproval, tc.needsApproval, outcome.Findings)
			}
			byID := lintFindingsByID(t, outcome.Findings)
			assertUntouchedDocsDemoted(t, byID, "lint-1", "lint-2")
			assertTouchedFileKept(t, byID, tc.wantTouched)
		})
	}
}

// TestScopeLintFindingsToChange pins how a finding's file is matched against
// the change: only a path naming a file tracked at HEAD and outside the
// change is demoted; a changed file in any spelling the step can place, a
// finding with no file, and any path that is not a tracked file as written
// (a directory, a subdirectory-relative path, a bare name, file:line, a path
// escaping the worktree) all fail toward parking.
func TestScopeLintFindingsToChange(t *testing.T) {
	t.Parallel()
	workDir := filepath.Join(string(filepath.Separator), "work", "tree")
	changed := []string{"front-end/src/App.vue", "feature.txt"}
	tracked := []string{"front-end/src/App.vue", "feature.txt", "front-end/package.json", "docs/OPERATIONS.md", "docs/STARTUP.md", "base.txt"}
	for _, tc := range []struct {
		file    string
		demoted bool
	}{
		{file: "", demoted: false},
		{file: "front-end/src/App.vue", demoted: false},
		{file: "./front-end/src/App.vue", demoted: false},
		{file: filepath.Join(workDir, "front-end", "src", "App.vue"), demoted: false},
		{file: "front-end", demoted: false},
		{file: "front-end/src/", demoted: false},
		{file: "docs", demoted: false},
		{file: ".", demoted: false},
		{file: "src/App.vue", demoted: false},
		{file: "App.vue", demoted: false},
		{file: "front-end/src/App.vue:2", demoted: false},
		{file: "../front-end/src/App.vue", demoted: false},
		{file: "../docs/OPERATIONS.md", demoted: false},
		{file: "front-end/src/App.vue.bak", demoted: false},
		{file: filepath.Join(string(filepath.Separator), "elsewhere", "docs", "OPERATIONS.md"), demoted: false},
		{file: "docs/OPERATIONS.md", demoted: true},
		{file: "./docs/STARTUP.md", demoted: true},
		{file: "front-end/../docs/STARTUP.md", demoted: true},
		{file: filepath.Join(workDir, "docs", "OPERATIONS.md"), demoted: true},
		{file: "front-end/package.json", demoted: true},
	} {
		in := Findings{Items: []Finding{{ID: "lint-1", Severity: types.FindingSeverityWarning, File: tc.file, Description: "fails", Action: types.ActionAskUser}}}
		out, demoted := scopeLintFindingsToChange(in, changed, tracked, workDir)
		got := out.Items[0]
		if (demoted > 0) != tc.demoted || (got.Severity == types.FindingSeverityInfo) != tc.demoted {
			t.Errorf("file %q: demoted=%v severity=%q, want demoted=%v", tc.file, demoted, got.Severity, tc.demoted)
		}
		if !tc.demoted && got != in.Items[0] {
			t.Errorf("file %q: kept finding changed to %+v", tc.file, got)
		}
	}

	info := Findings{Items: []Finding{{ID: "lint-1", Severity: types.FindingSeverityInfo, File: "docs/OPERATIONS.md", Description: "note", Action: types.ActionNoOp}}}
	if out, demoted := scopeLintFindingsToChange(info, changed, tracked, workDir); demoted > 0 || out.Items[0] != info.Items[0] {
		t.Errorf("an informational finding outside the change must pass through untouched, got %+v", out.Items[0])
	}
}

// TestLintPrompts_RunToolsAsTheProjectDoes pins the instruction every agent
// that lints or formats files receives: run the tool the way the project's
// own scripts and CI run it, never with the tool's defaults or on files the
// project does not check with it, and - for the lint duty - touch and report
// only the change's files (in the combined pass, plus the files the
// documentation duty edits), with repository-relative paths.
func TestLintPrompts_RunToolsAsTheProjectDoes(t *testing.T) {
	t.Parallel()
	toolRule := []string{
		"the way the project's own scripts and CI run it",
		"from the directory it runs from, with the configuration it passes",
		"front-end/.prettierrc.json",
		"Never run a formatter or linter with its default settings when the project configures it",
		"never run it on a file the project's own command for it does not check",
		"only on files under the configuration's directory",
	}
	reportRule := []string{
		`report it at most as an "info" finding with action "no-op"`,
		`Give each finding's "file" as a path relative to the repository root`,
	}
	ownScope := []string{
		"Check, fix, and report only the files changed between the base commit and the target commit",
		"Do not reformat or edit any other file",
	}
	combinedScope := []string{
		"plus every file this pass edits for the documentation duty",
		"Lint fixes must not reformat or edit any other file",
	}
	join := func(parts ...[]string) []string {
		var all []string
		for _, p := range parts {
			all = append(all, p...)
		}
		return all
	}

	dir, baseSHA, headSHA := setupGitRepo(t)
	for _, tc := range []struct {
		name   string
		cmds   config.Commands
		step   pipeline.Step
		want   []string
		absent []string
	}{
		{name: "lint own pass", cmds: config.Commands{}, step: &LintStep{}, want: join(toolRule, reportRule, ownScope), absent: combinedScope},
		{name: "combined document+lint pass", cmds: config.Commands{}, step: &DocumentStep{}, want: join(toolRule, reportRule, combinedScope), absent: ownScope},
		{name: "document-only pass", cmds: config.Commands{Lint: "true"}, step: &DocumentStep{}, want: toolRule, absent: join(ownScope, combinedScope)},
	} {
		var prompt string
		ag := &mockAgent{
			name: "test",
			runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
				prompt = opts.Prompt
				return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"clean"}`)}, nil
			},
		}
		sctx := newHousekeepingContext(t, ag, dir, baseSHA, headSHA, tc.cmds)
		if _, err := tc.step.Execute(sctx); err != nil {
			t.Fatalf("%s: Execute: %v", tc.name, err)
		}
		if prompt == "" {
			t.Fatalf("%s: no prompt captured", tc.name)
		}
		for _, want := range tc.want {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s prompt missing %q", tc.name, want)
			}
		}
		for _, absent := range tc.absent {
			if strings.Contains(prompt, absent) {
				t.Errorf("%s prompt carries another pass's scope %q", tc.name, absent)
			}
		}
		if strings.Contains(prompt, "when possible") {
			t.Errorf("%s prompt still leaves the changed-files scope optional", tc.name)
		}
	}
}

// TestLintStep_OwnPass_UnmatchedSpellingOfATouchedFileStillParks is the
// review's test plan for the guard failing open: an agent that ran Prettier
// from front-end/ reports the touched front-end/src/App.vue as src/App.vue.
// Any path the step cannot match to a file at HEAD must keep its severity.
func TestLintStep_OwnPass_UnmatchedSpellingOfATouchedFileStillParks(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"src/App.vue", "App.vue", "front-end/src/App.vue:2", "../front-end/src/App.vue", "../docs/OPERATIONS.md"} {
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			dir, baseSHA, headSHA := setupSubdirFormatterRepo(t)
			output, err := json.Marshal(Findings{Items: []Finding{{ID: "lint-1", Severity: types.FindingSeverityWarning, File: file, Description: "prettier --check fails", Action: types.ActionAskUser}}, Summary: "1 lint issue"})
			if err != nil {
				t.Fatal(err)
			}
			ag := &mockAgent{
				name: "test",
				runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
					return &agent.Result{Output: json.RawMessage(output)}, nil
				},
			}
			sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
			outcome, err := (&LintStep{}).Execute(sctx)
			if err != nil {
				t.Fatal(err)
			}
			if !outcome.NeedsApproval {
				t.Fatalf("NeedsApproval = false, want true; findings %s", outcome.Findings)
			}
			if got := lintFindingsByID(t, outcome.Findings)["lint-1"]; got.Severity != types.FindingSeverityWarning || got.Action != types.ActionAskUser {
				t.Fatalf("finding = %+v, want it kept as warning/ask-user", got)
			}
		})
	}
}
