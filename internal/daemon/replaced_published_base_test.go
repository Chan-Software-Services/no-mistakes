package daemon

import (
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/custody"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// TestRunAfterAReplacedPublishedHeadIsBasedOnTheReplacedHead covers the run
// `axi run` starts after `no-mistakes sync --replace-published`: the gate
// branch already follows the operator's head, so the start is a rerun, and its
// base must be the replaced published head. Otherwise the Rebase step reads an
// ordinary push and merges the replaced head straight back into the
// operator's history (TestOperatorReplacesAPublishedHeadJourney).
func TestRunAfterAReplacedPublishedHeadIsBasedOnTheReplacedHead(t *testing.T) {
	const branch = "feature/replaced"
	for _, tc := range []struct {
		name          string
		pin           bool
		custody       bool
		keepPublished bool
		wantPublished bool
	}{
		{name: "replaced", pin: true, custody: true, wantPublished: true},
		{name: "head contains the published head", pin: true, custody: true, keepPublished: true},
		{name: "published head not pinned", custody: true},
		{name: "custody not returned", pin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, d := startTestDaemonWithSteps(t, func() []pipeline.Step { return []pipeline.Step{&mockPassStep{name: types.StepReview}} })
			repo, base := setupTestGitRepo(t, p, d, "replaced-published-repo")
			work := repo.WorkingPath

			gitCmd(t, work, "checkout", "-b", branch)
			gitCmd(t, work, "commit", "--allow-empty", "-m", "operator feature")
			submitted := gitOutput(t, work, "rev-parse", "HEAD")
			gitCmd(t, work, "commit", "--allow-empty", "-m", "no-mistakes(review): fix")
			published := gitOutput(t, work, "rev-parse", "HEAD")
			gitCmd(t, work, "push", "gate", published+":refs/heads/"+branch)
			prior, err := d.InsertRun(repo.ID, branch, submitted, base)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.UpdateRunHeadSHA(prior.ID, published); err != nil {
				t.Fatal(err)
			}
			if err := d.UpdateRunPushBinding(prior.ID, db.PushBinding{HeadSHA: published, TargetKind: "upstream", TargetFingerprint: "fingerprint", Ref: "refs/heads/" + branch}); err != nil {
				t.Fatal(err)
			}
			if err := d.UpdateRunStatus(prior.ID, types.RunCompleted); err != nil {
				t.Fatal(err)
			}

			// What the replacement leaves: the published head pinned, the gate
			// branch at the operator's own head, custody returned.
			if tc.pin {
				gitCmd(t, work, "push", "gate", published+":"+custody.RecoveryRef(prior.ID))
			}
			if !tc.keepPublished {
				gitCmd(t, work, "reset", "--hard", submitted)
			}
			gitCmd(t, work, "commit", "--allow-empty", "-m", "operator follow-up")
			local := gitOutput(t, work, "rev-parse", "HEAD")
			gitCmd(t, work, "push", "--force", "gate", local+":refs/heads/"+branch)
			if tc.custody {
				if err := d.SetRunCustodyReturned(prior.ID); err != nil {
					t.Fatal(err)
				}
			}

			client, err := ipc.Dial(p.Socket())
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var result ipc.RerunResult
			if err := client.Call(ipc.MethodRerun, &ipc.RerunParams{RepoID: repo.ID, Branch: branch, CallerHeadSHA: local}, &result); err != nil {
				t.Fatal(err)
			}
			run := waitForRunTerminalState(t, d, result.RunID)
			want := base
			if tc.wantPublished {
				want = published
			}
			if run.BaseSHA != want {
				t.Fatalf("run base = %s, want %s (published %s, prior base %s)", run.BaseSHA, want, published, base)
			}
		})
	}
}
