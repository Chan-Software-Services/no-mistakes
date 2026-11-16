//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// replacePublishedOperatorSentence is what status adds at the dead end, so an
// agent that follows status stops and asks instead of trying recoveries that do
// not apply.
const replacePublishedOperatorSentence = "Replacing the published head with this local history is the operator's decision: a person runs `no-mistakes sync --replace-published`. Do not run it as an agent; stop and ask."

func replacePublishedScenario(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "replace-published-scenario.yaml")
	content := `actions:
  - match: "Investigate previous review findings"
    text: "added a guard helper"
    edits:
      - path: "guard.txt"
        new: "guard helper\n"
    structured:
      summary: "add a guard helper alongside the feature"
  - match: "Review the code changes and return structured findings"
    text: "review found a warning"
    structured:
      findings:
        - id: "replace-1"
          severity: warning
          file: "feature.txt"
          line: 1
          description: "the feature needs a guard helper"
          action: auto-fix
      summary: "found one issue"
      risk_level: medium
      risk_rationale: "the feature needs a guard"
      risk_scope: source-or-external
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no remaining risk"
      risk_scope: source-or-external
      tested: ["fakeagent: focused verification"]
      testing_summary: "simulated tests passed"
      scenarios:
        - name: "fakeagent: simulated end-to-end scenario"
          result: pass
          live: true
          evidence: "fakeagent: simulated test run"
          reason: ""
      verdict: go
      artifacts: []
      title: "feat: replace a published head"
      body: "replace published head journey"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write replace-published scenario: %v", err)
	}
	return path
}

// TestOperatorReplacesAPublishedHeadJourney is T200's PR 273 (2026-11-04) and
// T117's rewritten branch: a run publishes a head carrying a gate commit (a
// review fix), and the operator then rewrites the branch so it lacks that
// commit. Before `sync --replace-published` existed this was a dead end:
// status said blocked_diverged and offered only `git log`, `axi run` was
// refused by mirror reconciliation, and neither `--recover --keep-local` nor
// `--adopt-published` applied, so it took a one-off script. The journey shows
// an agent is still told to stop and ask, while a person's confirmed command
// makes the gate follow the local history with the published head pinned, and
// the next `axi run` publishes that history through the existing guarded push.
func TestOperatorReplacesAPublishedHeadJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: replacePublishedScenario(t)})
	initFromOwnWorktree(t, h, "init-replace-published")

	const branch = "feature/replace-published"
	submitted := h.CommitChange(branch, "feature.txt", "unsafe\n", "add unsafe feature")
	operator := h.AddWorktree(branch)
	gateOut, err := h.RunInDir(operator, "axi", "run", "--intent", "guard the feature")
	if err != nil || !strings.Contains(gateOut, "replace-1") {
		t.Fatalf("initial review gate: %v\n%s", err, gateOut)
	}
	if fixOut, err := h.RunInDir(operator, "axi", "respond", "--action", "fix", "--findings", "replace-1"); err != nil {
		t.Fatalf("review fix: %v\n%s", err, fixOut)
	}
	if doneOut, err := h.RunInDir(operator, "axi", "respond", "--action", "approve"); err != nil || !strings.Contains(doneOut, "outcome: passed") {
		t.Fatalf("approve the fix review: %v\n%s", err, doneOut)
	}
	first := h.WaitForRun(branch, 30*time.Second)
	if first.Status != types.RunCompleted {
		t.Fatalf("first run status = %s, want completed", first.Status)
	}
	published := h.UpstreamBranchSHA(branch)
	if published == submitted || published != first.HeadSHA {
		t.Fatalf("published head = %s, want the run's fix head %s on top of %s", published, first.HeadSHA, submitted)
	}

	// The operator rewrites the branch: they fetch the published head, then
	// amend their own commit, so the local history lacks the gate's fix commit.
	gitIn(t, h, operator, "fetch", "origin", branch)
	if err := os.WriteFile(filepath.Join(operator, "feature.txt"), []byte("validated by the operator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, h, operator, "commit", "-a", "--amend", "-m", "add validated feature")
	local := gitIn(t, h, operator, "rev-parse", "HEAD")
	if isAncestorIn(t, h, operator, published, local) || isAncestorIn(t, h, operator, local, published) {
		t.Fatalf("the rewritten head %s does not diverge from the published head %s", local, published)
	}
	gateDir := filepath.Join(h.NMHome, "repos", h.repoID()+".git")

	// The dead end, as an agent meets it: status keeps its safety and next
	// action, names the operator's command in words, and offers no command
	// that replaces anything.
	statusOut, err := h.RunInDir(operator, "axi", "status")
	if err != nil {
		t.Fatalf("axi status at the dead end: %v\n%s", err, statusOut)
	}
	for _, want := range []string{"safety: blocked_diverged", "code: inspect_and_reconcile_manually", replacePublishedOperatorSentence} {
		if !strings.Contains(statusOut, want) {
			t.Fatalf("axi status at the dead end lacks %q:\n%s", want, statusOut)
		}
	}
	assertNoCommandNamesReplacePublished(t, "axi status at the dead end", statusOut)

	runOut, err := h.RunInDir(operator, "axi", "run", "--intent", "publish the operator's rewritten history")
	if err == nil || !strings.Contains(runOut, "at-risk commit") {
		t.Fatalf("axi run at the dead end was not refused by mirror reconciliation: %v\n%s", err, runOut)
	}
	assertNoCommandNamesReplacePublished(t, "the refused axi run", runOut)

	agentOut, err := h.RunInDir(operator, "axi", "sync", "--replace-published")
	t.Logf("axi sync --replace-published:\n%s", agentOut)
	assertExitCode(t, err, 2)
	if want := "--replace-published is the operator's decision: a person runs `no-mistakes sync --replace-published`; no files or gate refs were changed"; !strings.Contains(agentOut, want) {
		t.Fatalf("axi sync --replace-published refusal lacks %q:\n%s", want, agentOut)
	}
	if got := gitIn(t, h, gateDir, "rev-parse", "refs/heads/"+branch); got != published {
		t.Fatalf("the refused agent command moved the gate branch to %s", got)
	}

	// The person's confirmed command.
	replaceOut, err := h.RunInDir(operator, "sync", "--replace-published", "--yes")
	if err != nil {
		t.Fatalf("sync --replace-published --yes: %v\n%s", err, replaceOut)
	}
	if want := gitIn(t, h, operator, "log", "-1", "--format=%s", published); !strings.Contains(replaceOut, want) {
		t.Fatalf("the command did not list the published fix commit the local history lacks (%q):\n%s", want, replaceOut)
	}
	if got := gitIn(t, h, gateDir, "rev-parse", "refs/no-mistakes/recover/"+first.ID); got != published {
		t.Fatalf("recovery ref = %s, want the replaced published head %s pinned", got, published)
	}
	if got := gitIn(t, h, gateDir, "rev-parse", "refs/heads/"+branch); got != local {
		t.Fatalf("gate branch = %s, want the local head %s", got, local)
	}
	if got := gitIn(t, h, operator, "rev-parse", "HEAD"); got != local {
		t.Fatalf("the command moved the local branch to %s", got)
	}
	if got := h.UpstreamBranchSHA(branch); got != published {
		t.Fatalf("the command changed the remote branch to %s", got)
	}

	readyOut, err := h.RunInDir(operator, "axi", "status")
	if err != nil {
		t.Fatalf("axi status after the replacement: %v\n%s", err, readyOut)
	}
	for _, want := range []string{"state: custody_returned", "safety: gate_ready", "code: run_pipeline"} {
		if !strings.Contains(readyOut, want) {
			t.Fatalf("axi status after the replacement lacks %q:\n%s", want, readyOut)
		}
	}

	nextOut, err := h.RunInDir(operator, "axi", "run", "--intent", "publish the operator's rewritten history")
	if err != nil || !strings.Contains(nextOut, "replace-1") {
		t.Fatalf("axi run after the replacement did not start and reach review: %v\n%s", err, nextOut)
	}
	next := h.ActiveRun(branch)
	if next == nil || next.ID == first.ID || next.SubmittedHeadSHA == nil || *next.SubmittedHeadSHA != local {
		t.Fatalf("the next run = %+v, want a new run submitted at the local head %s", next, local)
	}
	if doneOut, err := h.RunInDir(operator, "axi", "respond", "--action", "approve"); err != nil || !strings.Contains(doneOut, "outcome: passed") {
		t.Fatalf("drive the next run to its push: %v\n%s", err, doneOut)
	}
	if got := h.UpstreamBranchSHA(branch); got != local {
		t.Fatalf("remote branch after the next run = %s, want the operator's history %s published", got, local)
	}
	// The run treats the replacement as the operator's force push: the
	// replaced published head is not merged back into their history.
	rebaseLog, err := h.RunInDir(operator, "axi", "logs", "--run", next.ID, "--step", "rebase", "--full")
	if err != nil || !strings.Contains(rebaseLog, "force push detected") {
		t.Fatalf("the next run's Rebase step did not read the replacement as a force push: %v\n%s", err, rebaseLog)
	}
	if gitIn(t, h, operator, "ls-tree", "--name-only", local, "guard.txt") != "" {
		t.Fatal("the published history carries the replaced fix commit's file")
	}
	if got := gitIn(t, h, gateDir, "rev-parse", "refs/no-mistakes/recover/"+first.ID); got != published {
		t.Fatalf("recovery ref after the next run = %s, want the replaced head %s still pinned", got, published)
	}
}

// assertNoCommandNamesReplacePublished fails when any machine-followable
// command line in out names the replacement, which only a person may run.
func assertNoCommandNamesReplacePublished(t *testing.T, what, out string) {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "command:") && strings.Contains(line, "replace-published") {
			t.Fatalf("%s offers the replacement as a next_action command: %q\n%s", what, line, out)
		}
	}
}
