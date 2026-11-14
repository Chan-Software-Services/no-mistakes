package daemon

import (
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// dockerReapRecorder stands in for dockerreap.ReapAndLog in this package's
// tests (TestMain installs it): it records each reap's run IDs and reason.
type dockerReapRecorder struct {
	mu    sync.Mutex
	calls []dockerReapCall
}

type dockerReapCall struct {
	runIDs []string
	reason string
}

var dockerReaps = &dockerReapRecorder{}

func (r *dockerReapRecorder) record(runIDs []string, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, dockerReapCall{runIDs: slices.Clone(runIDs), reason: reason})
}

// reasonsFor returns the reason of every reap that named runID.
func (r *dockerReapRecorder) reasonsFor(runID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var reasons []string
	for _, call := range r.calls {
		if slices.Contains(call.runIDs, runID) {
			reasons = append(reasons, call.reason)
		}
	}
	return reasons
}

type outcomeStep struct{ err error }

func (s *outcomeStep) Name() types.StepName { return types.StepReview }
func (s *outcomeStep) Execute(*pipeline.StepContext) (*pipeline.StepOutcome, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &pipeline.StepOutcome{}, nil
}

// A run's agents label the Docker objects they start with the run's ID, and
// the run's end is where they go: a completed run and a failed one alike,
// since a Test agent that dies mid-scenario is exactly the one whose own
// teardown never ran.
func TestRunEndReapsTheRunsDockerObjects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status types.RunStatus
	}{
		{name: "completed", status: types.RunCompleted},
		{name: "failed", err: fmt.Errorf("step blew up"), status: types.RunFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
				return []pipeline.Step{&outcomeStep{err: tc.err}}
			})
			runID := pushTestRun(t, p, d, "docker-reap-"+tc.name)
			if run := waitForRunTerminalState(t, d, runID); run.Status != tc.status {
				t.Fatalf("run status = %q, want %q", run.Status, tc.status)
			}
			shutdownTestDaemonAndWaitForCleanup(t, p)
			if got := dockerReaps.reasonsFor(runID); !slices.Equal(got, []string{"run_finished"}) {
				t.Fatalf("reaps naming the run = %q, want one at run_finished", got)
			}
		})
	}
}

func pushTestRun(t *testing.T, p *paths.Paths, d *db.DB, repoID string) string {
	t.Helper()
	repo, headSHA := setupTestGitRepo(t, p, d, repoID)
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir(repo.ID),
		Ref:  "refs/heads/main",
		Old:  "0000000000000000000000000000000000000000",
		New:  headSHA,
	}, &result); err != nil {
		t.Fatal(err)
	}
	return result.RunID
}

// A run the previous daemon was executing when it died never reached its run
// end, so startup cleanup reaps its Docker objects with its worktree, in one
// pass for every such run, and never a run that is still executing.
func TestCleanupOrphanWorktreesReapsTheCrashedRunsDockerObjects(t *testing.T) {
	p := paths.WithRoot(t.TempDir())
	if err := p.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(p.DB())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	repo, err := d.InsertRepoWithID("reap-repo", t.TempDir(), "https://example.com/owner/reap-repo", "main")
	if err != nil {
		t.Fatal(err)
	}
	defaultTree := filepath.Join(p.WorktreesDir(), repo.ID)
	crashed := placeCleanupRun(t, d, repo.ID, defaultTree, types.RunFailed)
	finished := placeCleanupRun(t, d, repo.ID, defaultTree, types.RunCompleted)
	active := placeCleanupRun(t, d, repo.ID, defaultTree, types.RunRunning)
	runID := filepath.Base

	cleanupOrphanWorktrees(d, p, leftoverRecordedRunWorktrees(d, p))

	for _, dir := range []string{crashed, finished} {
		if got := dockerReaps.reasonsFor(runID(dir)); !slices.Equal(got, []string{"worktree_cleanup"}) {
			t.Errorf("reaps naming run %s = %q, want one at worktree_cleanup", runID(dir), got)
		}
	}
	if got := dockerReaps.reasonsFor(runID(active)); len(got) != 0 {
		t.Errorf("startup cleanup reaped the Docker objects of a run still executing: %q", got)
	}
	dockerReaps.mu.Lock()
	defer dockerReaps.mu.Unlock()
	passes := 0
	for _, call := range dockerReaps.calls {
		if slices.Contains(call.runIDs, runID(crashed)) {
			passes++
			if !slices.Contains(call.runIDs, runID(finished)) {
				t.Errorf("startup cleanup reaped the two runs in separate passes: %q", call.runIDs)
			}
		}
	}
	if passes != 1 {
		t.Errorf("startup cleanup reaped the crashed run in %d passes, want 1", passes)
	}
}
