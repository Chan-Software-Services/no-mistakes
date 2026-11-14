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
// formatter config sits in a subdirectory: front-end/.prettierrc.json, a
// front-end/package.json whose format:check script checks ../docs with that
// config, and docs that pass that config but not Prettier's defaults. The
// branch touches only front-end/src/App.vue (plus the template's
// feature.txt), so the docs are outside the change.
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
	write("docs/OPERATIONS.md", "# Operations\n\nA line that is longer than eighty columns but shorter than one hundred columns, as configured.\n")
	write("docs/STARTUP.md", "# Startup\n\nAnother line that is longer than eighty columns but shorter than one hundred columns.\n")
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
		demoted       []string
	}{
		{name: "only untouched docs", output: untouchedDocsOnlyFindings, needsApproval: false, demoted: []string{"lint-1", "lint-2"}},
		{name: "untouched docs and a touched file", output: untouchedDocsAndTouchedFileFindings, needsApproval: true, demoted: []string{"lint-1", "lint-2"}},
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
			assertUntouchedDocsDemoted(t, byID, tc.demoted...)
			if touched, ok := byID["lint-3"]; ok {
				if touched.Severity != "warning" || touched.Action != types.ActionAskUser || touched.Description != "prettier --check fails: quotes and semicolon" {
					t.Fatalf("touched-file finding changed: %+v", touched)
				}
			}
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
	}{
		{name: "only untouched docs", output: untouchedDocsOnlyFindings, needsApproval: false},
		{name: "untouched docs and a touched file", output: untouchedDocsAndTouchedFileFindings, needsApproval: true},
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
			if touched, ok := byID["lint-3"]; ok && (touched.Severity != "warning" || touched.Action != types.ActionAskUser) {
				t.Fatalf("touched-file finding changed: %+v", touched)
			}
		})
	}
}

// TestScopeLintFindingsToChange pins how a finding's file is matched against
// the change: path spellings of a changed file stay in scope, a finding with
// no file or on a directory holding a changed file fails toward parking, and
// only a file outside the change is demoted.
func TestScopeLintFindingsToChange(t *testing.T) {
	t.Parallel()
	workDir := filepath.Join(string(filepath.Separator), "work", "tree")
	changed := []string{"front-end/src/App.vue", "feature.txt"}
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
		{file: ".", demoted: false},
		{file: "docs/OPERATIONS.md", demoted: true},
		{file: "./docs/STARTUP.md", demoted: true},
		{file: filepath.Join(workDir, "docs", "OPERATIONS.md"), demoted: true},
		{file: "front-end/src/App.vue.bak", demoted: true},
		{file: "front", demoted: true},
	} {
		in := Findings{Items: []Finding{{ID: "lint-1", Severity: "warning", File: tc.file, Description: "fails", Action: types.ActionAskUser}}}
		out, demoted := scopeLintFindingsToChange(in, changed, workDir)
		got := out.Items[0]
		if (demoted > 0) != tc.demoted || (got.Severity == "info") != tc.demoted {
			t.Errorf("file %q: demoted=%v severity=%q, want demoted=%v", tc.file, demoted, got.Severity, tc.demoted)
		}
		if !tc.demoted && got != in.Items[0] {
			t.Errorf("file %q: in-scope finding changed to %+v", tc.file, got)
		}
	}

	info := Findings{Items: []Finding{{ID: "lint-1", Severity: "info", File: "docs/OPERATIONS.md", Description: "note", Action: types.ActionNoOp}}}
	if out, demoted := scopeLintFindingsToChange(info, changed, workDir); demoted > 0 || out.Items[0] != info.Items[0] {
		t.Errorf("an informational finding outside the change must pass through untouched, got %+v", out.Items[0])
	}
}

// TestLintPrompts_RunToolsAsTheProjectDoes pins the instruction every agent
// that lints or formats files receives: run the tool the way the project's
// own scripts and CI run it, never with the tool's defaults, and - for the
// lint duty - touch and report only the change's files.
func TestLintPrompts_RunToolsAsTheProjectDoes(t *testing.T) {
	t.Parallel()
	toolRule := []string{
		"the way the project's own scripts and CI run it",
		"from the directory it runs from, with the configuration it passes",
		"front-end/.prettierrc.json",
		"Never run a formatter or linter with its default settings when the project configures it",
	}
	scopeRule := []string{
		"Check, fix, and report only the files changed between the base commit and the target commit",
		"Do not reformat or edit any other file",
		`report it at most as an "info" finding with action "no-op"`,
	}

	prompts := map[string]struct {
		prompt string
		want   []string
	}{}

	dir, baseSHA, headSHA := setupGitRepo(t)
	capture := func(cmds config.Commands, shared bool, run func(*pipeline.StepContext)) string {
		var prompt string
		ag := &mockAgent{
			name: "test",
			runFn: func(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
				prompt = opts.Prompt
				return &agent.Result{Output: json.RawMessage(`{"findings":[],"summary":"clean"}`)}, nil
			},
		}
		var sctx *pipeline.StepContext
		if shared {
			sctx = newHousekeepingContext(t, ag, dir, baseSHA, headSHA, cmds)
		} else {
			sctx = newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, cmds)
		}
		run(sctx)
		return prompt
	}
	prompts["lint own pass"] = struct {
		prompt string
		want   []string
	}{capture(config.Commands{}, false, func(sctx *pipeline.StepContext) { (&LintStep{}).Execute(sctx) }), append(append([]string{}, toolRule...), scopeRule...)}
	prompts["combined document+lint pass"] = struct {
		prompt string
		want   []string
	}{capture(config.Commands{}, true, func(sctx *pipeline.StepContext) { (&DocumentStep{}).Execute(sctx) }), append(append([]string{}, toolRule...), scopeRule...)}
	prompts["document-only pass"] = struct {
		prompt string
		want   []string
	}{capture(config.Commands{Lint: "true"}, true, func(sctx *pipeline.StepContext) { (&DocumentStep{}).Execute(sctx) }), toolRule}

	for name, p := range prompts {
		if p.prompt == "" {
			t.Fatalf("%s: no prompt captured", name)
		}
		for _, want := range p.want {
			if !strings.Contains(p.prompt, want) {
				t.Errorf("%s prompt missing %q", name, want)
			}
		}
		if strings.Contains(p.prompt, "when possible") {
			t.Errorf("%s prompt still leaves the changed-files scope optional", name)
		}
	}
}
