package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// worktreeAddFixture is a repository to add worktrees to (a bare gate, or a
// repository with its own working tree) holding one commit.
type worktreeAddFixture struct {
	ctx    context.Context
	repo   string
	sha    string
	branch string
}

// newBareWorktreeAddFixture is a bare gate that a working repository pushed
// its commit to, as main. setup, when not nil, runs on the working repository
// before its commit.
func newBareWorktreeAddFixture(t *testing.T, setup func(work string)) *worktreeAddFixture {
	t.Helper()
	f := &worktreeAddFixture{ctx: context.Background(), branch: "main"}
	work := initTestRepo(t)
	if setup != nil {
		setup(work)
		run(t, work, "git", "add", ".")
		run(t, work, "git", "commit", "-m", "setup")
	}
	f.repo = filepath.Join(t.TempDir(), "gate.git")
	if err := InitBare(f.ctx, f.repo); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	run(t, work, "git", "push", f.repo, "HEAD:refs/heads/main")
	f.sha = run(t, work, "git", "rev-parse", "HEAD")
	return f
}

func newNonBareWorktreeAddFixture(t *testing.T) *worktreeAddFixture {
	t.Helper()
	f := &worktreeAddFixture{ctx: context.Background()}
	f.repo = initTestRepo(t)
	f.sha = run(t, f.repo, "git", "rev-parse", "HEAD")
	f.branch = run(t, f.repo, "git", "branch", "--show-current")
	return f
}

// commonDir is the repository's git directory, where worktrees/ lives.
func (f *worktreeAddFixture) commonDir(t *testing.T) string {
	t.Helper()
	dir, err := Run(f.ctx, f.repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		t.Fatalf("resolve common dir: %v", err)
	}
	return dir
}

// listed reports whether `git worktree list` lists a worktree at wtPath.
func (f *worktreeAddFixture) listed(t *testing.T, wtPath string) bool {
	t.Helper()
	out, err := Run(f.ctx, f.repo, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok && (filepath.Clean(filepath.FromSlash(path)) == filepath.Clean(wtPath) || sameRealPath(path, wtPath)) {
			return true
		}
	}
	return false
}

// entries returns the names in <common dir>/<sub>, nil when it does not exist.
func (f *worktreeAddFixture) entries(t *testing.T, sub string) []string {
	t.Helper()
	list, err := os.ReadDir(filepath.Join(f.commonDir(t), sub))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

// assertNothingLeftBehind fails unless the repository holds no worktree entry,
// no staging directory and lists no worktree at wtPath.
func (f *worktreeAddFixture) assertNothingLeftBehind(t *testing.T, wtPath string) {
	t.Helper()
	if names := f.entries(t, "worktrees"); len(names) != 0 {
		t.Errorf("worktrees/ holds %v, want nothing", names)
	}
	if names := f.entries(t, newWorktreeStaging); len(names) != 0 {
		t.Errorf("%s/ holds %v, want nothing", newWorktreeStaging, names)
	}
	if f.listed(t, wtPath) {
		t.Errorf("git lists a worktree at %s", wtPath)
	}
}

func sameRealPath(a, b string) bool {
	ra, err := filepath.EvalSymlinks(filepath.FromSlash(a))
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(filepath.FromSlash(b))
	if err != nil {
		return false
	}
	return ra == rb
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return run(t, dir, "git", append([]string{"-c", "user.name=T", "-c", "user.email=t@t.com"}, args...)...)
}

// assertUsableDetachedWorktree checks the worktree at wtPath from outside: its
// files, a HEAD detached at sha, a clean status, and that it commits.
func assertUsableDetachedWorktree(t *testing.T, f *worktreeAddFixture, wtPath string) {
	t.Helper()
	if data, err := os.ReadFile(filepath.Join(wtPath, "README.md")); err != nil || string(data) != "# test\n" {
		t.Fatalf("README.md in the worktree = %q, %v; want the committed file", data, err)
	}
	if got := gitIn(t, wtPath, "rev-parse", "HEAD"); got != f.sha {
		t.Fatalf("worktree HEAD = %s, want %s", got, f.sha)
	}
	if out, err := exec.Command("git", "-C", wtPath, "symbolic-ref", "-q", "HEAD").CombinedOutput(); err == nil {
		t.Fatalf("worktree HEAD is on a branch (%s), want detached", strings.TrimSpace(string(out)))
	}
	if got := gitIn(t, wtPath, "status", "--porcelain"); got != "" {
		t.Fatalf("worktree status = %q, want clean", got)
	}
	if got := gitIn(t, wtPath, "rev-parse", "--is-inside-work-tree"); got != "true" {
		t.Fatalf("is-inside-work-tree = %q, want true", got)
	}
	if !f.listed(t, wtPath) {
		t.Fatal("git does not list the worktree")
	}
	writeFile(t, filepath.Join(wtPath, "new.txt"), "new\n")
	gitIn(t, wtPath, "add", "new.txt")
	gitIn(t, wtPath, "commit", "-q", "-m", "in the worktree")
	if got := gitIn(t, wtPath, "rev-parse", "HEAD~1"); got != f.sha {
		t.Fatalf("worktree commit's parent = %s, want %s", got, f.sha)
	}
	if _, err := Run(f.ctx, f.repo, "fsck", "--no-progress"); err != nil {
		t.Fatalf("fsck: %v", err)
	}
}

func TestWorktreeAddChecksOutADetachedWorktree(t *testing.T) {
	repos := []struct {
		name string
		make func(t *testing.T) *worktreeAddFixture
	}{
		{"bare", func(t *testing.T) *worktreeAddFixture { return newBareWorktreeAddFixture(t, nil) }},
		{"non-bare", newNonBareWorktreeAddFixture},
	}
	revs := []struct {
		name string
		rev  func(f *worktreeAddFixture) string
	}{
		{"sha", func(f *worktreeAddFixture) string { return f.sha }},
		{"branch", func(f *worktreeAddFixture) string { return f.branch }},
		{"full ref", func(f *worktreeAddFixture) string { return "refs/heads/" + f.branch }},
	}
	for _, repo := range repos {
		for _, rev := range revs {
			t.Run(repo.name+"/"+rev.name, func(t *testing.T) {
				f := repo.make(t)
				wt := filepath.Join(t.TempDir(), "run-1")
				if err := WorktreeAdd(f.ctx, f.repo, wt, rev.rev(f)); err != nil {
					t.Fatalf("WorktreeAdd: %v", err)
				}
				assertUsableDetachedWorktree(t, f, wt)
				if names := f.entries(t, newWorktreeStaging); len(names) != 0 {
					t.Fatalf("%s/ holds %v after the add, want nothing", newWorktreeStaging, names)
				}
				for _, name := range []string{"locked", "gitdir.pending"} {
					if _, err := os.Lstat(filepath.Join(f.commonDir(t), "worktrees", "run-1", name)); !os.IsNotExist(err) {
						t.Fatalf("the entry still holds %s after the add (lstat error %v)", name, err)
					}
				}
			})
		}
	}
}

func TestWorktreeAddCopiesTheWorktreeConfigWithoutCoreBare(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	bare := filepath.Join(base, "gate.git")
	if err := InitBare(ctx, bare); err != nil {
		t.Fatal(err)
	}
	seedGate(t, base, bare)
	if err := IsolateHooksPath(ctx, bare); err != nil {
		t.Fatalf("IsolateHooksPath: %v", err)
	}
	if !worktreeConfigIsolatesCoreBare(t, bare) {
		t.Skip("git lacks `config --worktree`; core.bare cannot be isolated on this host")
	}

	wt := filepath.Join(base, "wt")
	if err := WorktreeAdd(ctx, bare, wt, "refs/heads/main"); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	hooks, err := filepath.Abs(filepath.Join(bare, "hooks"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runGitOrFatal(t, wt, "config", "--get", "core.hookspath")); !sameRealPath(got, hooks) {
		t.Fatalf("worktree core.hookspath = %q, want the gate's hooks directory %q", got, hooks)
	}
	if got := strings.TrimSpace(runGitOrFatal(t, wt, "rev-parse", "--is-bare-repository")); got != "false" {
		t.Fatalf("worktree is-bare-repository = %q, want false", got)
	}
	if out, err := exec.Command("git", "-C", wt, "config", "--get", "core.bare").CombinedOutput(); err == nil && strings.TrimSpace(string(out)) == "true" {
		t.Fatal("core.bare=true leaked into the worktree")
	}
	// The gate itself keeps its own per-worktree core.bare.
	if !worktreeConfigIsolatesCoreBare(t, bare) {
		t.Fatal("the gate lost its per-worktree core.bare")
	}
}

func TestWorktreeAddRefusesAPathThatAlreadyHoldsFiles(t *testing.T) {
	t.Run("holds files", func(t *testing.T) {
		f := newBareWorktreeAddFixture(t, nil)
		wt := filepath.Join(t.TempDir(), "run-1")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(wt, "keep.txt"), "mine\n")

		err := WorktreeAdd(f.ctx, f.repo, wt, f.sha)
		if err == nil || !strings.Contains(err.Error(), "worktree path "+wt+" already exists and is not empty") {
			t.Fatalf("WorktreeAdd error = %v, want it refused as already existing and not empty", err)
		}
		if data, err := os.ReadFile(filepath.Join(wt, "keep.txt")); err != nil || string(data) != "mine\n" {
			t.Fatalf("keep.txt = %q, %v; want it untouched", data, err)
		}
		if names, err := os.ReadDir(wt); err != nil || len(names) != 1 {
			t.Fatalf("the path holds %v (%v), want only keep.txt", names, err)
		}
		f.assertNothingLeftBehind(t, wt)
	})
	t.Run("is a file", func(t *testing.T) {
		f := newBareWorktreeAddFixture(t, nil)
		wt := filepath.Join(t.TempDir(), "run-1")
		writeFile(t, wt, "mine\n")

		if err := WorktreeAdd(f.ctx, f.repo, wt, f.sha); err == nil || !strings.Contains(err.Error(), "already exists and is not empty") {
			t.Fatalf("WorktreeAdd error = %v, want it refused", err)
		}
		if data, err := os.ReadFile(wt); err != nil || string(data) != "mine\n" {
			t.Fatalf("the file = %q, %v; want it untouched", data, err)
		}
		f.assertNothingLeftBehind(t, wt)
	})
	t.Run("an empty directory is accepted", func(t *testing.T) {
		f := newBareWorktreeAddFixture(t, nil)
		wt := filepath.Join(t.TempDir(), "run-1")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WorktreeAdd(f.ctx, f.repo, wt, f.sha); err != nil {
			t.Fatalf("WorktreeAdd into an empty directory: %v", err)
		}
		assertUsableDetachedWorktree(t, f, wt)
	})
}

func TestWorktreeAddUndoesAFailedCreation(t *testing.T) {
	t.Run("unknown rev", func(t *testing.T) {
		f := newBareWorktreeAddFixture(t, nil)
		wt := filepath.Join(t.TempDir(), "run-1")
		err := WorktreeAdd(f.ctx, f.repo, wt, "refs/heads/no-such-branch")
		if err == nil || !strings.HasPrefix(err.Error(), "create worktree "+wt+": ") {
			t.Fatalf("WorktreeAdd error = %v, want a create worktree error", err)
		}
		if _, err := os.Lstat(wt); !os.IsNotExist(err) {
			t.Fatalf("the worktree path exists after a failed add (lstat error %v)", err)
		}
		f.assertNothingLeftBehind(t, wt)
		if names := f.entries(t, ""); containsName(names, removedWorktreeTrash) {
			t.Errorf("the repository holds %s after a failed add", removedWorktreeTrash)
		}
	})
	// A required filter that fails makes the checkout fail part way.
	failingGate := func(t *testing.T) *worktreeAddFixture {
		f := routeThroughFilter(t, "broken")
		run(t, f.repo, "git", "--git-dir="+f.repo, "config", "filter.broken.smudge", "false")
		run(t, f.repo, "git", "--git-dir="+f.repo, "config", "filter.broken.required", "true")
		return f
	}
	t.Run("failed checkout into a new path", func(t *testing.T) {
		f := failingGate(t)
		wt := filepath.Join(t.TempDir(), "run-1")
		err := WorktreeAdd(f.ctx, f.repo, wt, f.sha)
		if err == nil || !strings.HasPrefix(err.Error(), "create worktree "+wt+": ") {
			t.Fatalf("WorktreeAdd error = %v, want a create worktree error", err)
		}
		if _, err := os.Lstat(wt); !os.IsNotExist(err) {
			t.Fatalf("the worktree path exists after a failed add (lstat error %v)", err)
		}
		f.assertNothingLeftBehind(t, wt)
	})
	t.Run("failed checkout into an existing empty directory", func(t *testing.T) {
		f := failingGate(t)
		wt := filepath.Join(t.TempDir(), "run-1")
		if err := os.MkdirAll(wt, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WorktreeAdd(f.ctx, f.repo, wt, f.sha); err == nil {
			t.Fatal("WorktreeAdd through a failing filter succeeded")
		}
		names, err := os.ReadDir(wt)
		if err != nil {
			t.Fatalf("the existing directory is gone after a failed add: %v", err)
		}
		if len(names) != 0 {
			t.Fatalf("the existing directory holds %v after a failed add, want it empty", names)
		}
		f.assertNothingLeftBehind(t, wt)
	})
}

// routeThroughFilter makes the fixture's commit carry probe.txt, which a
// .gitattributes routes through the filter named filter; the gate configures
// what that filter runs.
func routeThroughFilter(t *testing.T, filter string) *worktreeAddFixture {
	t.Helper()
	return newBareWorktreeAddFixture(t, func(work string) {
		writeFile(t, filepath.Join(work, ".gitattributes"), "probe.txt filter="+filter+"\n")
		writeFile(t, filepath.Join(work, "probe.txt"), "probed\n")
	})
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

func TestWorktreeAddTakesTheNextNameWhenItsEntryNameIsTaken(t *testing.T) {
	f := newBareWorktreeAddFixture(t, nil)
	first := filepath.Join(t.TempDir(), "run-1")
	second := filepath.Join(t.TempDir(), "run-1")
	for _, wt := range []string{first, second} {
		if err := WorktreeAdd(f.ctx, f.repo, wt, f.sha); err != nil {
			t.Fatalf("WorktreeAdd %s: %v", wt, err)
		}
	}
	if got := strings.Join(f.entries(t, "worktrees"), " "); got != "run-1 run-11" {
		t.Fatalf("worktrees/ holds %q, want run-1 run-11", got)
	}
	assertUsableDetachedWorktree(t, f, first)
	assertUsableDetachedWorktree(t, f, second)

	// The removal leaves its entry behind for a while; the same path added again
	// takes the next free name.
	if err := WorktreeRemove(f.ctx, f.repo, first); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.commonDir(t), "worktrees", "run-1", "locked")); err != nil {
		t.Fatalf("fixture: the removal left no entry behind: %v", err)
	}
	if err := WorktreeAdd(f.ctx, f.repo, first, f.sha); err != nil {
		t.Fatalf("WorktreeAdd of the removed path again: %v", err)
	}
	if !containsName(f.entries(t, "worktrees"), "run-12") {
		t.Fatalf("worktrees/ holds %v, want run-12 for the second add of %s", f.entries(t, "worktrees"), first)
	}
	assertUsableDetachedWorktree(t, f, first)
}

func TestWorktreeAddSanitizesTheEntryName(t *testing.T) {
	f := newBareWorktreeAddFixture(t, nil)
	wt := filepath.Join(t.TempDir(), "run 1+x")
	if err := WorktreeAdd(f.ctx, f.repo, wt, f.sha); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}
	if got := strings.Join(f.entries(t, "worktrees"), " "); got != "run-1-x" {
		t.Fatalf("worktrees/ holds %q, want run-1-x", got)
	}
	assertUsableDetachedWorktree(t, f, wt)
}

func TestWorktreeAddFallsBackToGitOnAnotherRefFormat(t *testing.T) {
	ctx := context.Background()
	work := initTestRepo(t)
	bare := filepath.Join(t.TempDir(), "gate.git")
	if out, err := exec.Command("git", "init", "--bare", "--ref-format=reftable", bare).CombinedOutput(); err != nil {
		t.Skipf("git cannot make a reftable repository here: %v: %s", err, out)
	}
	run(t, work, "git", "push", bare, "HEAD:refs/heads/main")
	f := &worktreeAddFixture{ctx: ctx, repo: bare, sha: run(t, work, "git", "rev-parse", "HEAD"), branch: "main"}

	wt := filepath.Join(t.TempDir(), "run-1")
	if err := WorktreeAdd(ctx, bare, wt, f.sha); err != nil {
		t.Fatalf("WorktreeAdd on a reftable gate: %v", err)
	}
	assertUsableDetachedWorktree(t, f, wt)
	if names := f.entries(t, newWorktreeStaging); names != nil {
		t.Fatalf("the reftable gate holds %s/ (%v): the add did not fall back to git's own", newWorktreeStaging, names)
	}
}

// The prune that git's maintenance runs after a push deletes worktrees/ once
// it is empty, which it is on a gate's first run or once its last entry went.
// An add that finds worktrees/ gone when it renames its staged entry in must
// still succeed. Every add here starts with worktrees/ absent, while prunes run
// back to back.
func TestWorktreeAddSurvivesAPruneDeletingTheEmptyWorktreesDirectory(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	bare := filepath.Join(base, "gate.git")
	if err := InitBare(ctx, bare); err != nil {
		t.Fatal(err)
	}
	seedGate(t, base, bare)
	if err := IsolateHooksPath(ctx, bare); err != nil {
		t.Fatalf("IsolateHooksPath: %v", err)
	}
	worktrees := filepath.Join(bare, "worktrees")

	stop := make(chan struct{})
	done := make(chan struct{})
	prunes := 0
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = exec.Command("git", "--git-dir="+bare, "worktree", "prune").Run()
			prunes++
		}
	}()
	// Every exit, a t.Fatal included, stops the prunes before the gate's
	// directory is cleaned up (cleanups run last registered first).
	stopPrunes := sync.OnceFunc(func() {
		close(stop)
		<-done
	})
	t.Cleanup(stopPrunes)

	const adds = 200
	var failures []string
	for i := 0; i < adds; i++ {
		if err := os.RemoveAll(worktrees); err != nil {
			t.Fatal(err)
		}
		wt := filepath.Join(base, "wt")
		if err := WorktreeAdd(ctx, bare, wt, "refs/heads/main"); err != nil {
			failures = append(failures, err.Error())
			_ = os.RemoveAll(wt)
			continue
		}
		if err := WorktreeRemove(ctx, bare, wt); err != nil {
			t.Fatalf("WorktreeRemove: %v", err)
		}
	}
	stopPrunes()
	t.Logf("%d adds, %d prunes", adds, prunes)
	if len(failures) > 0 {
		t.Fatalf("%d of %d adds failed while prunes ran, first: %s", len(failures), adds, failures[0])
	}
}
