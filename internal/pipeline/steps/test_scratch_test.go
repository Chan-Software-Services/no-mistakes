package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/evidence"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// The evidence turn writes driver scripts, fixtures and throwaway configs as
// well as evidence. With no place named for them, a real run wrote them to
// fixed paths under /tmp, which every concurrent run on the machine shares.
// Each run's Test step now names its own scratch directory, inside the run's
// evidence directory, so it is reaped with the run and never published.

func TestTestStep_EvidencePromptNamesThisRunsOwnScratchDirectory(t *testing.T) {
	t.Parallel()
	prompts, tokens := map[string]string{}, map[string]string{}
	for _, runEvidence := range []string{"run-a", "run-b"} {
		dir, baseSHA, headSHA := setupGitRepo(t)
		ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: json.RawMessage(passingScenarioFindingsJSON)}, nil
		}}
		sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
		sctx.EvidenceDir = filepath.Join(t.TempDir(), "evidence", runEvidence)
		if _, err := (&TestStep{}).Execute(sctx); err != nil {
			t.Fatal(err)
		}
		scratch := filepath.Join(sctx.EvidenceDir, evidence.ScratchDirName)
		if info, err := os.Stat(scratch); err != nil || !info.IsDir() {
			t.Fatalf("the run's scratch directory was not created before the turn: %v", err)
		}
		prompt := ag.calls[0].Prompt
		token := testRunToken(sctx)
		if token == "" || !strings.Contains(strings.ToLower(sctx.Run.ID), token) {
			t.Fatalf("the run token %q does not come from the run ID %q", token, sctx.Run.ID)
		}
		for _, want := range []string{
			"Write every scratch file this testing needs (driver scripts, throwaway fixtures, configs, data directories, logs) into this run's own scratch directory, never into the worktree or a fixed path such as /tmp/<name> that another run could share: " + scratch,
			"Give every disposable resource you start (a container, a compose project, a database) a name that carries this run's token, " + token + " (for example demo-" + token + "), and start every server on a free port chosen when it starts, never a fixed one",
			"Never name the scratch directory, its files, this run's token, or those resource names in scenario evidence, artifacts, the \"tested\" array, or the testing summary: a pull request reader cannot see them.",
			"In \"tested\", record such a check in words, or as its command with the scratch path, the token, and the resource names left out.",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s: evidence prompt lacks %q\nprompt:\n%s", runEvidence, want, prompt)
			}
		}
		prompts[runEvidence] = scratch
		tokens[runEvidence] = token
	}
	if prompts["run-a"] == prompts["run-b"] {
		t.Fatalf("two runs share one scratch directory: %s", prompts["run-a"])
	}
	if tokens["run-a"] == tokens["run-b"] {
		t.Fatalf("two runs share one resource token: %s", tokens["run-a"])
	}
}

// Run IDs are ULIDs: the leading characters are the start time, so two runs
// started together share them, and the trailing ones are random (and
// incremented within one millisecond), so the token is taken from the end.
func TestTestRunTokenComesFromTheRunIDsRandomTail(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]string{
		"01M7BP002H3MKTQPP4XE8HP1SQ": "xe8hp1sq",
		"01M7BP002H3MKTQPP4XE8HP1SR": "xe8hp1sr",
		"run-1":                      "run1",
		"":                           "",
	} {
		sctx := &pipeline.StepContext{Run: &db.Run{ID: id}}
		if got := testRunToken(sctx); got != want {
			t.Errorf("testRunToken(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestPublishRunEvidence_NeverPublishesTheTestScratchDirectory(t *testing.T) {
	sctx, remote := newEvidencePublishContext(t, "feature/add-login")
	writeRunEvidence(t, sctx, map[string]string{
		"cli-run.txt":                          "it works\n",
		evidence.ScratchDirName + "/driver.py": "print('drive the product')\n",
	})

	if links := publishRunEvidence(sctx); links == nil {
		t.Fatal("the real test evidence beside the scratch directory must still publish")
	}
	tree := gitCmd(t, remote, "ls-tree", "-r", "--name-only", gitCmd(t, remote, "rev-parse", "refs/heads/no-mistakes/evidence"))
	if !strings.Contains(tree, "cli-run.txt") {
		t.Fatalf("the real test evidence did not publish:\n%s", tree)
	}
	if strings.Contains(tree, evidence.ScratchDirName+"/") || strings.Contains(tree, "driver.py") {
		t.Fatalf("the test scratch directory reached the evidence branch:\n%s", tree)
	}
}

// The scratch directory sits inside the run's evidence directory, and a Test
// path artifact anywhere under that directory is pasted into the PR body as
// text. A scratch file is never evidence, so naming one as an artifact must
// neither embed it nor link it: the driver script holds the run's resource
// names, which the PR must never show.
func TestBuildTestingSummaryForPR_NeverEmbedsATestScratchFile(t *testing.T) {
	evidenceRoot := filepath.Join(t.TempDir(), "evidence", "run-123")
	driver := writeTempEvidenceFile(t, filepath.Join(evidenceRoot, evidence.ScratchDirName), "driver.py", []byte("compose -p demo-stack up\n"))
	findings := fmt.Sprintf(`{"findings":[],"summary":"","testing_summary":"Evidence was collected.","artifacts":[{"kind":"log","label":"Driver run","path":%q}]}`, driver)
	steps := []*db.StepResult{
		{ID: "s1", StepName: types.StepTest, Status: types.StepStatusCompleted, FindingsJSON: &findings},
	}
	rounds := map[string][]*db.StepRound{
		"s1": {{Round: 1, Trigger: "initial", FindingsJSON: &findings, DurationMS: 300}},
	}

	md := buildPRTestingSummary(steps, rounds, "git@github.com:example/widgets.git", "abc123", t.TempDir(), evidenceRoot, nil, scm.ProviderGitHub)
	for _, leaked := range []string{"demo-stack", "driver.py", evidence.ScratchDirName + "/"} {
		if strings.Contains(md, leaked) {
			t.Fatalf("the PR testing summary showed the scratch file's %q:\n%s", leaked, md)
		}
	}
}
