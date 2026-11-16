package branchsync

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/custody"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const replaceBranch = "feature/replace"

// replaceFixture is the shape of a gate commit replaced on the branch: a
// completed run published a head carrying a gate fix commit to the configured target, the gate branch still
// names that published head, and the operator's local branch has been
// rewritten so it lacks the fix commit.
type replaceFixture struct {
	t         *testing.T
	ctx       context.Context
	db        *db.DB
	repo      *db.Repo
	run       *db.Run
	service   *Service
	root      string
	local     string
	gate      string
	remote    string
	pipeline  string
	base      string
	submitted string
	published string
}

// newReplaceFixture builds the fixture. With fetched, the published head is in
// the local object store (blocked_diverged); without it, it is not
// (blocked_relation_unknown).
func newReplaceFixture(t *testing.T, fetched bool) *replaceFixture {
	t.Helper()
	f := newReplaceFixtureAtSubmitted(t)
	if fetched {
		mustRun(t, f.local, "fetch", f.remote, replaceBranch)
	}
	mustWrite(t, filepath.Join(f.local, "file.txt"), "feature, validated by the operator\n")
	mustRun(t, f.local, "commit", "-a", "--amend", "-m", "feature, rewritten")
	return f
}

// newReplaceFixtureAtSubmitted builds everything but the operator's rewrite:
// the local branch is still at the submitted head.
func newReplaceFixtureAtSubmitted(t *testing.T) *replaceFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	remote := filepath.Join(root, "upstream.git")
	mustRun(t, root, "init", "--bare", remote)
	local := filepath.Join(root, "operator")
	mustRun(t, root, "init", "-b", "main", local)
	configureIdentity(t, local)
	mustWrite(t, filepath.Join(local, "file.txt"), "base\n")
	mustRun(t, local, "add", "file.txt")
	mustRun(t, local, "commit", "-m", "base")
	base := mustRun(t, local, "rev-parse", "HEAD")
	mustRun(t, local, "push", remote, "main:refs/heads/main")
	mustRun(t, local, "checkout", "-b", replaceBranch)
	mustWrite(t, filepath.Join(local, "file.txt"), "feature\n")
	mustRun(t, local, "commit", "-am", "feature")
	submitted := mustRun(t, local, "rev-parse", "HEAD")

	gate := filepath.Join(root, "gate.git")
	mustRun(t, root, "init", "--bare", gate)
	mustRun(t, local, "push", gate, "refs/heads/"+replaceBranch+":refs/heads/"+replaceBranch)
	pipeline := filepath.Join(root, "pipeline")
	mustRun(t, root, "-c", "core.autocrlf=false", "clone", gate, pipeline)
	configureIdentity(t, pipeline)
	mustRun(t, pipeline, "checkout", replaceBranch)
	mustWrite(t, filepath.Join(pipeline, "fix.txt"), "pipeline fix\n")
	mustRun(t, pipeline, "add", "fix.txt")
	mustRun(t, pipeline, "commit", "-m", "no-mistakes(review): add the guard")
	published := mustRun(t, pipeline, "rev-parse", "HEAD")
	mustRun(t, pipeline, "push", "origin", "HEAD:refs/heads/"+replaceBranch)
	mustRun(t, pipeline, "push", remote, "HEAD:refs/heads/"+replaceBranch)

	database, err := db.Open(filepath.Join(root, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	repo, err := database.InsertRepo(local, remote, "main")
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.InsertRun(repo.ID, replaceBranch, submitted, base)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunHeadSHA(run.ID, published); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunPushBinding(run.ID, db.PushBinding{
		HeadSHA: published, TargetKind: "upstream", TargetFingerprint: TargetFingerprint(remote), Ref: "refs/heads/" + replaceBranch,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateRunStatus(run.ID, types.RunCompleted); err != nil {
		t.Fatal(err)
	}
	run, _ = database.GetRun(run.ID)
	return &replaceFixture{
		t: t, ctx: ctx, db: database, repo: repo, run: run,
		service: &Service{DB: database, Repo: repo, WorkDir: local, GateDir: gate},
		root:    root, local: local, gate: gate, remote: remote, pipeline: pipeline,
		base: base, submitted: submitted, published: published,
	}
}

func (f *replaceFixture) localHead() string {
	return mustRun(f.t, f.local, "rev-parse", "HEAD")
}

func (f *replaceFixture) gateBranch() string {
	return mustRun(f.t, f.gate, "rev-parse", "refs/heads/"+replaceBranch)
}

// snapshot records every ref in the worktree, the gate, and the remote, the
// worktree's status, and the run's database row, so a refusal can be shown
// to have changed none of them.
type replaceSnapshot struct {
	localRefs, gateRefs, remoteRefs, status string
	run                                     db.Run
}

func (f *replaceFixture) snapshot() replaceSnapshot {
	f.t.Helper()
	run, err := f.db.GetRun(f.run.ID)
	if err != nil || run == nil {
		f.t.Fatalf("read run: %v", err)
	}
	return replaceSnapshot{
		localRefs:  mustRun(f.t, f.local, "for-each-ref", "--format=%(refname) %(objectname)"),
		gateRefs:   mustRun(f.t, f.gate, "for-each-ref", "--format=%(refname) %(objectname)"),
		remoteRefs: mustRun(f.t, f.remote, "for-each-ref", "--format=%(refname) %(objectname)"),
		status:     mustRun(f.t, f.local, "status", "--porcelain=v2", "--branch", "--untracked-files=all"),
		run:        *run,
	}
}

func (f *replaceFixture) assertUnchanged(before replaceSnapshot) {
	f.t.Helper()
	after := f.snapshot()
	if !reflect.DeepEqual(before, after) {
		f.t.Fatalf("a refusal changed something:\nbefore %+v\nafter  %+v", before, after)
	}
}

func (f *replaceFixture) custodyReturned() bool {
	f.t.Helper()
	run, err := f.db.GetRun(f.run.ID)
	if err != nil || run == nil {
		f.t.Fatalf("read run: %v", err)
	}
	return run.CustodyReturnedAt != nil
}

func assertReplaced(t *testing.T, f *replaceFixture, state State, local string) {
	t.Helper()
	if !state.Changed || state.State != StateCustodyReturned || state.Safety != "gate_ready" ||
		state.NextAction == nil || state.NextAction.Code != "run_pipeline" {
		t.Fatalf("replacement state = %#v", state)
	}
	if got := f.gateBranch(); got != local {
		t.Fatalf("gate branch = %s, want the local head %s", got, local)
	}
	if got := mustRun(t, f.gate, "rev-parse", custody.RecoveryRef(f.run.ID)); got != f.published {
		t.Fatalf("recovery ref = %s, want the published head %s pinned", got, f.published)
	}
	if !f.custodyReturned() {
		t.Fatal("custody was not stamped")
	}
	if got := f.localHead(); got != local {
		t.Fatalf("local head moved to %s", got)
	}
	if got := mustRun(t, f.remote, "rev-parse", "refs/heads/"+replaceBranch); got != f.published {
		t.Fatalf("remote branch changed to %s", got)
	}
}

func TestReplacePublishedPinsThePublishedHeadAndMovesTheGate(t *testing.T) {
	t.Parallel()
	f := newReplaceFixture(t, true)
	local := f.localHead()
	before := f.snapshot()

	cached := f.service.InspectCached(f.ctx)
	if cached.Safety != "blocked_diverged" || !strings.Contains(cached.Error, ReplacePublishedOperatorNote) {
		t.Fatalf("dead end = %#v, want blocked_diverged with the operator's sentence", cached)
	}
	if cached.NextAction == nil || cached.NextAction.Code != "inspect_and_reconcile_manually" || strings.Contains(cached.NextAction.Command, "replace-published") {
		t.Fatalf("dead-end next action = %#v, want the unchanged manual reconciliation", cached.NextAction)
	}
	plan, _, ok := f.service.PlanReplacePublished(f.ctx)
	// The published history the rewrite lacks is the gate's fix and the
	// operator's own original commit, which the amend replaced; newest first.
	if !ok || !plan.LocallyAvailable || len(plan.Commits) != 2 || plan.MoreCommits != 0 ||
		plan.Commits[0].Subject != "no-mistakes(review): add the guard" || plan.Commits[1].Subject != "feature" {
		t.Fatalf("plan = %#v, ok %v", plan, ok)
	}
	if plan.PublishedHead != f.published || plan.LocalHead != local || plan.Branch != replaceBranch {
		t.Fatalf("plan heads = %#v", plan)
	}
	f.assertUnchanged(before)

	localRefs := before.localRefs
	state := f.service.ReplacePublished(f.ctx)
	assertReplaced(t, f, state, local)
	if got := mustRun(t, f.local, "for-each-ref", "--format=%(refname) %(objectname)"); got != localRefs {
		t.Fatalf("the replacement changed a worktree ref:\nbefore %s\nafter  %s", localRefs, got)
	}
	if staged := mustRun(t, f.gate, "for-each-ref", "refs/no-mistakes/replace-published/"); staged != "" {
		t.Fatalf("staging ref left in the gate: %s", staged)
	}

	// Inspection keeps reporting the replacement, and a follow-up commit on
	// top of the head the gate follows is still ready to run.
	if again := f.service.InspectCached(f.ctx); again.State != StateCustodyReturned || again.Safety != "gate_ready" {
		t.Fatalf("inspection after the replacement = %#v", again)
	}
	mustWrite(t, filepath.Join(f.local, "followup.txt"), "follow-up\n")
	mustRun(t, f.local, "add", "followup.txt")
	mustRun(t, f.local, "commit", "-m", "follow-up")
	if followed := f.service.InspectCached(f.ctx); followed.State != StateCustodyReturned || followed.Safety != "gate_ready" ||
		followed.NextAction == nil || followed.NextAction.Code != "run_pipeline" {
		t.Fatalf("inspection after a follow-up commit = %#v", followed)
	}
	// A second replacement finds nothing to replace.
	if second := f.service.ReplacePublished(f.ctx); second.Changed || second.Safety != "blocked_replace_published_not_applicable" {
		t.Fatalf("second replacement = %#v", second)
	}
}

func TestReplacePublishedWithThePublishedHeadAbsentLocally(t *testing.T) {
	t.Parallel()
	f := newReplaceFixture(t, false)
	local := f.localHead()
	if objectExists(f.ctx, f.local, f.published) {
		t.Fatal("fixture: the published head is in the local object store")
	}
	cached := f.service.InspectCached(f.ctx)
	if cached.Safety != "blocked_relation_unknown" || !strings.Contains(cached.Error, ReplacePublishedOperatorNote) ||
		cached.NextAction == nil || cached.NextAction.Code != "check_sync" {
		t.Fatalf("dead end = %#v, want blocked_relation_unknown with the operator's sentence and its own next action", cached)
	}
	plan, _, ok := f.service.PlanReplacePublished(f.ctx)
	if !ok || plan.LocallyAvailable || len(plan.Commits) != 0 {
		t.Fatalf("plan = %#v, ok %v, want the commits reported as not available locally", plan, ok)
	}
	if objectExists(f.ctx, f.gate, local) {
		t.Fatal("fixture: the local head is already in the gate")
	}

	state := f.service.ReplacePublished(f.ctx)
	assertReplaced(t, f, state, local)
	if objectExists(f.ctx, f.local, f.published) {
		t.Fatal("the replacement fetched the published head into the worktree")
	}
}

// TestReplacePublishedLegacyRebaseThenHandMerge is the legacy rebase shape: an
// old binary's Rebase step rebased the branch onto a newer main and published that
// rebase with a fix commit on top, and the operator then merged the same main
// into their own branch by hand and went on working. The local history lacks
// the published commits and holds work the published head lacks, so it is not
// the equivalent divergence sync advances, and nothing but the replacement
// applies.
func TestReplacePublishedLegacyRebaseThenHandMerge(t *testing.T) {
	t.Parallel()
	f := newReplaceFixtureAtSubmitted(t)
	mustRun(t, f.local, "checkout", "main")
	mustWrite(t, filepath.Join(f.local, "upstream.txt"), "newer main\n")
	mustRun(t, f.local, "add", "upstream.txt")
	mustRun(t, f.local, "commit", "-m", "advance main")
	mustRun(t, f.local, "push", f.remote, "main:refs/heads/main")
	mustRun(t, f.local, "checkout", replaceBranch)

	mustRun(t, f.pipeline, "fetch", f.remote, "main")
	mustRun(t, f.pipeline, "checkout", "-B", replaceBranch, f.submitted)
	mustRun(t, f.pipeline, "rebase", "FETCH_HEAD")
	mustWrite(t, filepath.Join(f.pipeline, "fix.txt"), "pipeline fix\n")
	mustRun(t, f.pipeline, "add", "fix.txt")
	mustRun(t, f.pipeline, "commit", "-m", "no-mistakes(review): add the guard")
	rebased := mustRun(t, f.pipeline, "rev-parse", "HEAD")
	mustRun(t, f.pipeline, "push", "--force", "origin", "HEAD:refs/heads/"+replaceBranch)
	mustRun(t, f.pipeline, "push", "--force", f.remote, "HEAD:refs/heads/"+replaceBranch)
	if err := f.db.UpdateRunHeadSHA(f.run.ID, rebased); err != nil {
		t.Fatal(err)
	}
	if err := f.db.UpdateRunPushBinding(f.run.ID, db.PushBinding{
		HeadSHA: rebased, TargetKind: "upstream", TargetFingerprint: TargetFingerprint(f.remote), Ref: "refs/heads/" + replaceBranch,
	}); err != nil {
		t.Fatal(err)
	}
	f.published = rebased

	mustRun(t, f.local, "fetch", f.remote, replaceBranch)
	mustRun(t, f.local, "merge", "--no-ff", "--no-edit", "-m", "merge main by hand", "main")
	mustWrite(t, filepath.Join(f.local, "followup.txt"), "work after the hand merge\n")
	mustRun(t, f.local, "add", "followup.txt")
	mustRun(t, f.local, "commit", "-m", "work after the hand merge")
	local := f.localHead()

	if cached := f.service.InspectCached(f.ctx); cached.Safety != "blocked_diverged" || !strings.Contains(cached.Error, ReplacePublishedOperatorNote) {
		t.Fatalf("legacy dead end = %#v", cached)
	}
	plan, _, ok := f.service.PlanReplacePublished(f.ctx)
	if !ok || len(plan.Commits) != 2 {
		t.Fatalf("plan = %#v, ok %v, want the rebased feature commit and the fix", plan, ok)
	}
	state := f.service.ReplacePublished(f.ctx)
	assertReplaced(t, f, state, local)
}

func TestReplacePublishedPlanListsTenCommitsAndCountsTheRest(t *testing.T) {
	t.Parallel()
	f := newReplaceFixtureAtSubmitted(t)
	for i := 0; i < 12; i++ {
		mustWrite(t, filepath.Join(f.pipeline, "fix.txt"), fmt.Sprintf("pipeline fix %d\n", i))
		mustRun(t, f.pipeline, "commit", "-am", fmt.Sprintf("no-mistakes(review): fix %d", i))
	}
	published := mustRun(t, f.pipeline, "rev-parse", "HEAD")
	mustRun(t, f.pipeline, "push", "origin", "HEAD:refs/heads/"+replaceBranch)
	mustRun(t, f.pipeline, "push", f.remote, "HEAD:refs/heads/"+replaceBranch)
	if err := f.db.UpdateRunHeadSHA(f.run.ID, published); err != nil {
		t.Fatal(err)
	}
	if err := f.db.UpdateRunPushBinding(f.run.ID, db.PushBinding{
		HeadSHA: published, TargetKind: "upstream", TargetFingerprint: TargetFingerprint(f.remote), Ref: "refs/heads/" + replaceBranch,
	}); err != nil {
		t.Fatal(err)
	}
	mustRun(t, f.local, "fetch", f.remote, replaceBranch)
	mustWrite(t, filepath.Join(f.local, "file.txt"), "rewritten\n")
	mustRun(t, f.local, "commit", "-a", "--amend", "-m", "feature, rewritten")

	plan, _, ok := f.service.PlanReplacePublished(f.ctx)
	// Twelve fixes, the fixture's own fix, and the replaced original commit.
	if !ok || len(plan.Commits) != maxReplacedCommitsListed || plan.MoreCommits != 4 {
		t.Fatalf("plan lists %d commits and %d more (ok %v), want 10 and 4", len(plan.Commits), plan.MoreCommits, ok)
	}
	if plan.Commits[0].Subject != "no-mistakes(review): fix 11" || len(plan.Commits[0].ShortSHA) < 7 {
		t.Fatalf("first listed commit = %#v, want the newest", plan.Commits[0])
	}
}

func TestReplacePublishedNotApplicableChangesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) *replaceFixture
	}{
		{name: "local head is the published head", setup: func(t *testing.T) *replaceFixture {
			f := newReplaceFixtureAtSubmitted(t)
			mustRun(t, f.local, "fetch", f.remote, replaceBranch)
			mustRun(t, f.local, "merge", "--ff-only", "FETCH_HEAD")
			return f
		}},
		{name: "local head is behind the published head", setup: func(t *testing.T) *replaceFixture {
			return newReplaceFixtureAtSubmitted(t)
		}},
		{name: "local head contains the published head", setup: func(t *testing.T) *replaceFixture {
			f := newReplaceFixtureAtSubmitted(t)
			mustRun(t, f.local, "fetch", f.remote, replaceBranch)
			mustRun(t, f.local, "merge", "--ff-only", "FETCH_HEAD")
			mustRun(t, f.local, "commit", "--allow-empty", "-m", "follow-up")
			return f
		}},
		{name: "divergence sync already advances", setup: func(t *testing.T) *replaceFixture {
			f := newReplaceFixtureAtSubmitted(t)
			mustRun(t, f.local, "fetch", f.remote, replaceBranch)
			mustRun(t, f.local, "commit", "--allow-empty", "--amend", "-m", "feature, same content")
			return f
		}},
		{name: "run still active", setup: func(t *testing.T) *replaceFixture {
			f := newReplaceFixture(t, true)
			if err := f.db.UpdateRunStatus(f.run.ID, types.RunRunning); err != nil {
				t.Fatal(err)
			}
			return f
		}},
		{name: "pull request merged", setup: func(t *testing.T) *replaceFixture {
			f := newReplaceFixture(t, true)
			if err := f.db.UpdateRunPRState(f.run.ID, "merged"); err != nil {
				t.Fatal(err)
			}
			return f
		}},
		{name: "pull request closed", setup: func(t *testing.T) *replaceFixture {
			f := newReplaceFixture(t, true)
			if err := f.db.UpdateRunPRState(f.run.ID, "closed"); err != nil {
				t.Fatal(err)
			}
			return f
		}},
		{name: "branch checked out twice", setup: func(t *testing.T) *replaceFixture {
			f := newReplaceFixture(t, true)
			mustRun(t, f.local, "worktree", "add", "--force", filepath.Join(f.root, "second"), replaceBranch)
			return f
		}},
		{name: "no push binding", setup: func(t *testing.T) *replaceFixture {
			f := newReplaceFixture(t, true)
			run, err := f.db.InsertRun(f.repo.ID, replaceBranch, f.localHead(), f.base)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.db.UpdateRunStatusWithVerifiedHead(run.ID, types.RunCancelled, f.localHead()); err != nil {
				t.Fatal(err)
			}
			return f
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := tc.setup(t)
			before := f.snapshot()
			if _, planned, ok := f.service.PlanReplacePublished(f.ctx); ok || planned.Safety != "blocked_replace_published_not_applicable" {
				t.Fatalf("plan = %#v, ok %v", planned, ok)
			}
			state := f.service.ReplacePublished(f.ctx)
			if state.Changed || state.Safety != "blocked_replace_published_not_applicable" || state.NextAction != nil {
				t.Fatalf("state = %#v", state)
			}
			if cached := f.service.InspectCached(f.ctx); strings.Contains(cached.Error, ReplacePublishedOperatorNote) {
				t.Fatalf("status names the replacement where it does not apply: %#v", cached)
			}
			f.assertUnchanged(before)
		})
	}
}

// TestReplacePublishedProofsFailClosed covers each proof before the first
// change: every refusal has its own safety code and leaves every ref, the
// worktree, and the run row exactly as they were.
func TestReplacePublishedProofsFailClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		safety string
		setup  func(t *testing.T, f *replaceFixture)
	}{
		{name: "dirty worktree", safety: "blocked_replace_published_dirty", setup: func(t *testing.T, f *replaceFixture) {
			mustWrite(t, filepath.Join(f.local, "file.txt"), "uncommitted\n")
		}},
		{name: "untracked file", safety: "blocked_replace_published_dirty", setup: func(t *testing.T, f *replaceFixture) {
			mustWrite(t, filepath.Join(f.local, "scratch.txt"), "untracked\n")
		}},
		{name: "gate missing", safety: "blocked_replace_published_gate_unavailable", setup: func(t *testing.T, f *replaceFixture) {
			f.service.GateDir = filepath.Join(f.root, "missing.git")
		}},
		{name: "no gate configured", safety: "blocked_replace_published_gate_unavailable", setup: func(t *testing.T, f *replaceFixture) {
			f.service.GateDir = ""
		}},
		{name: "gate branch moved", safety: "blocked_replace_published_gate_changed", setup: func(t *testing.T, f *replaceFixture) {
			mustRun(t, f.gate, "update-ref", "refs/heads/"+replaceBranch, f.submitted)
		}},
		{name: "gate branch deleted", safety: "blocked_replace_published_gate_changed", setup: func(t *testing.T, f *replaceFixture) {
			mustRun(t, f.gate, "update-ref", "-d", "refs/heads/"+replaceBranch)
		}},
		{name: "push target unreachable", safety: "blocked_replace_published_offline", setup: func(t *testing.T, f *replaceFixture) {
			f.service.lsRemote = func(context.Context, string, string, string) (string, error) {
				return "", errors.New("network down")
			}
		}},
		{name: "remote branch advanced", safety: "blocked_replace_published_target_changed", setup: func(t *testing.T, f *replaceFixture) {
			writer := cloneRemoteBranchNamed(t, f.remote, replaceBranch)
			mustRun(t, writer, "commit", "--allow-empty", "-m", "out-of-band commit")
			mustRun(t, writer, "push", "origin", "HEAD:refs/heads/"+replaceBranch)
		}},
		{name: "remote branch deleted", safety: "blocked_replace_published_target_changed", setup: func(t *testing.T, f *replaceFixture) {
			mustRun(t, f.remote, "update-ref", "-d", "refs/heads/"+replaceBranch)
		}},
		{name: "push target replaced", safety: "blocked_replace_published_target_changed", setup: func(t *testing.T, f *replaceFixture) {
			replacement := filepath.Join(f.root, "replacement.git")
			mustRun(t, f.root, "init", "--bare", replacement)
			if _, err := f.db.ReplaceRepoURLs(f.repo.ID, replacement, ""); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "push target replaced while it was read", safety: "blocked_replace_published_target_changed", setup: func(t *testing.T, f *replaceFixture) {
			replacement := filepath.Join(f.root, "replacement.git")
			mustRun(t, f.root, "init", "--bare", replacement)
			f.service.lsRemote = func(ctx context.Context, dir, remote, ref string) (string, error) {
				if _, err := f.db.ReplaceRepoURLs(f.repo.ID, replacement, ""); err != nil {
					t.Fatal(err)
				}
				return f.published, nil
			}
		}},
		{name: "local head moved while the target was read", safety: "blocked_replace_published_assumptions_changed", setup: func(t *testing.T, f *replaceFixture) {
			f.service.lsRemote = func(ctx context.Context, dir, remote, ref string) (string, error) {
				mustRun(t, f.local, "commit", "--allow-empty", "-m", "concurrent commit")
				return f.published, nil
			}
		}},
		{name: "run changed while the target was read", safety: "blocked_replace_published_assumptions_changed", setup: func(t *testing.T, f *replaceFixture) {
			f.service.lsRemote = func(ctx context.Context, dir, remote, ref string) (string, error) {
				if err := f.db.UpdateRunStatus(f.run.ID, types.RunRunning); err != nil {
					t.Fatal(err)
				}
				return f.published, nil
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newReplaceFixture(t, true)
			tc.setup(t, f)
			// The mid-call races change state on purpose; compare against
			// what each leaves once it has run.
			before := f.snapshot()
			state := f.service.ReplacePublished(f.ctx)
			if state.Changed || state.Safety != tc.safety || state.NextAction != nil {
				t.Fatalf("state = %#v, want %s", state, tc.safety)
			}
			if !strings.Contains(state.Error, "no files or gate refs were changed") {
				t.Fatalf("refusal %q does not say nothing was changed", state.Error)
			}
			if strings.HasSuffix(tc.name, "while the target was read") {
				before = f.snapshotAfterRace(before)
			}
			f.assertUnchanged(before)
		})
	}
}

// snapshotAfterRace takes the snapshot again once a race hook has run, keeping
// the gate's and the remote's refs from before: the race moved only what it
// names, and the replacement itself must have moved nothing.
func (f *replaceFixture) snapshotAfterRace(before replaceSnapshot) replaceSnapshot {
	f.t.Helper()
	after := f.snapshot()
	if after.gateRefs != before.gateRefs || after.remoteRefs != before.remoteRefs {
		f.t.Fatalf("a refused replacement changed the gate or the remote:\nbefore %+v\nafter  %+v", before, after)
	}
	if after.run.CustodyReturnedAt != nil {
		f.t.Fatal("a refused replacement stamped custody")
	}
	return after
}

func TestReplacePublishedRefusesAConflictingRecoveryRef(t *testing.T) {
	t.Parallel()
	f := newReplaceFixture(t, true)
	mustRun(t, f.gate, "update-ref", custody.RecoveryRef(f.run.ID), f.submitted)
	before := f.snapshot()

	state := f.service.ReplacePublished(f.ctx)
	if state.Changed || state.Safety != "blocked_replace_published_preserve_failed" {
		t.Fatalf("state = %#v", state)
	}
	f.assertUnchanged(before)
}

func TestReplacePublishedLosesAGateRaceWithoutReplacing(t *testing.T) {
	t.Parallel()
	f := newReplaceFixture(t, true)
	var racer string
	f.service.beforeReplacePublishedChange = func() {
		mustRun(t, f.pipeline, "commit", "--allow-empty", "-m", "concurrent gate push")
		racer = mustRun(t, f.pipeline, "rev-parse", "HEAD")
		mustRun(t, f.pipeline, "push", "origin", "HEAD:refs/heads/"+replaceBranch)
	}

	state := f.service.ReplacePublished(f.ctx)
	if state.Changed || state.Safety != "blocked_replace_published_gate_race" {
		t.Fatalf("state = %#v", state)
	}
	if got := f.gateBranch(); got != racer {
		t.Fatalf("gate branch = %s, want the concurrent push %s kept", got, racer)
	}
	if got := mustRun(t, f.gate, "rev-parse", custody.RecoveryRef(f.run.ID)); got != f.published {
		t.Fatalf("recovery ref = %s, want the published head %s still pinned", got, f.published)
	}
	if f.custodyReturned() {
		t.Fatal("a lost race stamped custody")
	}
}

func TestReplacePublishedFailedStampMovesTheGateBack(t *testing.T) {
	t.Parallel()
	f := newReplaceFixture(t, true)
	stamped := ""
	f.service.setCustodyReturned = func(runID string) error {
		stamped = runID
		if got := f.gateBranch(); got != f.localHead() {
			t.Fatalf("gate branch at the stamp = %s, want the local head", got)
		}
		return errors.New("database is locked")
	}

	state := f.service.ReplacePublished(f.ctx)
	if state.Changed || state.Safety != "blocked_replace_published_custody_failed" || stamped != f.run.ID {
		t.Fatalf("state = %#v (stamped %q)", state, stamped)
	}
	if !strings.Contains(state.Error, "moved back to the published head") {
		t.Fatalf("refusal %q does not report the rollback", state.Error)
	}
	if got := f.gateBranch(); got != f.published {
		t.Fatalf("gate branch = %s, want it moved back to the published head %s", got, f.published)
	}
	if f.custodyReturned() {
		t.Fatal("custody was stamped")
	}
	if cached := f.service.InspectCached(f.ctx); cached.Safety != "blocked_diverged" {
		t.Fatalf("inspection after the rollback = %#v, want the dead end back", cached)
	}
}

// TestReplacedPublishedHeadNeedsThePin keeps the post-replacement
// classification to what the replacement leaves: a pushed run whose custody
// was returned with its gate branch moved off the published head is not ready
// to run unless the published head is pinned at the run's recovery ref.
func TestReplacedPublishedHeadNeedsThePin(t *testing.T) {
	t.Parallel()
	f := newReplaceFixture(t, true)
	if state := f.service.ReplacePublished(f.ctx); !state.Changed {
		t.Fatalf("replacement = %#v", state)
	}
	mustRun(t, f.gate, "update-ref", "-d", custody.RecoveryRef(f.run.ID))
	if cached := f.service.InspectCached(f.ctx); cached.State == StateCustodyReturned || cached.Safety == "gate_ready" {
		t.Fatalf("inspection without the pin = %#v, want the ordinary classification", cached)
	}
}

func cloneRemoteBranchNamed(t *testing.T, remote, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "writer")
	mustRun(t, filepath.Dir(dir), "-c", "core.autocrlf=false", "clone", remote, dir)
	configureIdentity(t, dir)
	mustRun(t, dir, "checkout", branch)
	return dir
}

// The operator's sentence and the replacement read one predicate for the run
// and its pull request, so a dead end the replacement would refuse as not
// applicable never tells the reader a person runs it.
func TestReplacePublishedNoteSkipsAMergedOrClosedPullRequest(t *testing.T) {
	t.Parallel()
	for _, prState := range []string{"merged", "closed"} {
		t.Run(prState, func(t *testing.T) {
			t.Parallel()
			f := newReplaceFixture(t, true)
			cached := f.service.InspectCached(f.ctx)
			if cached.Safety != "blocked_diverged" || !strings.Contains(cached.Error, ReplacePublishedOperatorNote) {
				t.Fatalf("dead end = %#v, want blocked_diverged with the operator's sentence", cached)
			}
			run, err := f.db.GetRun(f.run.ID)
			if err != nil {
				t.Fatal(err)
			}
			state := cached
			state.Error = strings.TrimSuffix(strings.TrimSuffix(cached.Error, ReplacePublishedOperatorNote), " ")
			state.PRState = prState
			f.service.noteReplacePublished(f.ctx, &state, run)
			if strings.Contains(state.Error, ReplacePublishedOperatorNote) {
				t.Fatalf("a %s pull request's dead end names the replacement: %q", prState, state.Error)
			}
		})
	}
}
