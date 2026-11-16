package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/custody"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
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
			shape := setupReplacedPublishedShape(t, p, d, branch, tc.pin, tc.custody, tc.keepPublished)
			repo, base, published, local := shape.repo, shape.base, shape.published, shape.local

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

// replacedPublishedShape is what a branch looks like right after
// `no-mistakes sync --replace-published`, or short of it by the options.
type replacedPublishedShape struct {
	repo      *db.Repo
	base      string // the prior run's base
	published string // the prior run's published head
	local     string // the operator's head, which the gate branch follows
}

// setupReplacedPublishedShape publishes a head with a gate commit on branch,
// then leaves what the replacement leaves: the published head pinned at the
// prior run's recovery ref (pin), the gate branch at the operator's own head,
// which lacks the published head unless keepPublished, and custody returned
// (custodyReturned).
func setupReplacedPublishedShape(t *testing.T, p *paths.Paths, d *db.DB, branch string, pin, custodyReturned, keepPublished bool) replacedPublishedShape {
	t.Helper()
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
	if pin {
		gitCmd(t, work, "push", "gate", published+":"+custody.RecoveryRef(prior.ID))
	}
	if !keepPublished {
		gitCmd(t, work, "reset", "--hard", submitted)
	}
	gitCmd(t, work, "commit", "--allow-empty", "-m", "operator follow-up")
	local := gitOutput(t, work, "rev-parse", "HEAD")
	gitCmd(t, work, "push", "--force", "gate", local+":refs/heads/"+branch)
	if custodyReturned {
		if err := d.SetRunCustodyReturned(prior.ID); err != nil {
			t.Fatal(err)
		}
	}
	return replacedPublishedShape{repo: repo, base: base, published: published, local: local}
}

// With the replacement's evidence present, an ancestry check that cannot
// answer (here a head the gate does not hold) refuses the run start rather
// than fall back to the natural base, under which the Rebase step would merge
// the replaced published head back into the operator's history.
func TestReplacedPublishedBaseFailsClosedWhenTheAncestryCheckCannotAnswer(t *testing.T) {
	const branch = "feature/replaced"
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step { return []pipeline.Step{&mockPassStep{name: types.StepReview}} })
	shape := setupReplacedPublishedShape(t, p, d, branch, true, true, false)
	m := &RunManager{db: d, paths: p}

	const absent = "0123456789abcdef0123456789abcdef01234567"
	got, err := m.replacedPublishedBase(context.Background(), shape.repo.ID, branch, absent, shape.base)
	if err == nil {
		t.Fatalf("base = %s with no error, want the run start refused", got)
	}
	if !strings.Contains(err.Error(), shape.published) {
		t.Fatalf("err = %v, want it to name the replaced published head %s", err, shape.published)
	}

	// The same shape with a head the gate holds still answers.
	got, err = m.replacedPublishedBase(context.Background(), shape.repo.ID, branch, shape.local, shape.base)
	if err != nil || got != shape.published {
		t.Fatalf("base = %s, err %v, want the published head %s", got, err, shape.published)
	}
}
