package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// worktreeRemoveFixture is a bare gate repository holding one commit on main,
// a working repository that pushes to it, and one linked worktree of the gate.
type worktreeRemoveFixture struct {
	ctx   context.Context
	work  string
	bare  string
	sha   string
	wt    string
	admin string
}

func newWorktreeRemoveFixture(t *testing.T) *worktreeRemoveFixture {
	t.Helper()
	f := &worktreeRemoveFixture{ctx: context.Background()}
	f.work = initTestRepo(t)
	f.bare = filepath.Join(t.TempDir(), "gate.git")
	if err := InitBare(f.ctx, f.bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	run(t, f.work, "git", "push", f.bare, "HEAD:refs/heads/main")
	f.sha = run(t, f.work, "git", "rev-parse", "HEAD")
	f.wt = filepath.Join(t.TempDir(), "run-1")
	if err := WorktreeAdd(f.ctx, f.bare, f.wt, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	f.admin = filepath.Join(f.bare, "worktrees", "run-1")
	if _, err := os.Stat(filepath.Join(f.admin, "gitdir")); err != nil {
		t.Fatalf("fixture: admin directory of the worktree: %v", err)
	}
	return f
}

func (f *worktreeRemoveFixture) listsWorktree(t *testing.T) bool {
	t.Helper()
	out, err := Run(f.ctx, f.bare, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	return strings.Contains(out, "run-1")
}

func TestWorktreeRemoveDeletesTheDirectoryAndUnlistsTheWorktree(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	if !f.listsWorktree(t) {
		t.Fatal("fixture: git does not list the worktree before removal")
	}

	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	if _, err := os.Lstat(f.wt); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists after removal (lstat error %v)", err)
	}
	if f.listsWorktree(t) {
		t.Fatal("git still lists the worktree after removal")
	}
	// The repository stays fully usable with the leftover admin entry in it.
	run(t, f.work, "git", "commit", "--allow-empty", "-m", "next")
	run(t, f.work, "git", "push", f.bare, "HEAD:refs/heads/next")
	if _, err := Run(f.ctx, f.bare, "rev-list", "--all"); err != nil {
		t.Fatalf("rev-list --all after removal: %v", err)
	}
}

// A git process that enumerated the gate's worktrees just before the removal
// goes on to read worktrees/<id>/commondir, and dies with "failed to read
// worktrees/<id>/commondir" when the file vanished after it checked that it
// exists. The removal therefore leaves commondir (and HEAD) in place for such a
// reader and only unlists the entry.
func TestWorktreeRemoveKeepsWhatAReaderAlreadyListingTheWorktreeStillReads(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	gitdirContent, err := os.ReadFile(filepath.Join(f.admin, "gitdir"))
	if err != nil {
		t.Fatal(err)
	}

	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	if _, err := os.Lstat(filepath.Join(f.admin, "gitdir")); !os.IsNotExist(err) {
		t.Fatalf("admin gitdir file should be gone so git stops listing the worktree (lstat error %v)", err)
	}
	for _, name := range []string{"commondir", "HEAD"} {
		if _, err := os.ReadFile(filepath.Join(f.admin, name)); err != nil {
			t.Fatalf("admin %s must stay readable for a reader that already listed the worktree: %v", name, err)
		}
	}
	entries, err := os.ReadDir(f.admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("removed worktree's admin directory holds %v, want only commondir and HEAD", names)
	}

	// Put back the one file such a reader had already read: git must then get
	// through every worktree-walking command the gate serves without dying.
	if err := os.WriteFile(filepath.Join(f.admin, "gitdir"), gitdirContent, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(f.ctx, f.bare, "worktree", "list", "--porcelain"); err != nil {
		t.Fatalf("worktree list as an in-flight reader: %v", err)
	}
	if _, err := Run(f.ctx, f.bare, "rev-list", "--all"); err != nil {
		t.Fatalf("rev-list --all as an in-flight reader: %v", err)
	}
	run(t, f.work, "git", "commit", "--allow-empty", "-m", "next")
	run(t, f.work, "git", "push", f.bare, "HEAD:refs/heads/next")
}

// The entry must be unlisted before anything a reader depends on is deleted:
// when deleting the working directory fails, the worktree is already hidden
// and its admin directory is otherwise untouched.
func TestWorktreeRemoveUnlistsBeforeItDeletesAnything(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not block deletion on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	f := newWorktreeRemoveFixture(t)
	locked := filepath.Join(f.wt, "locked-dir")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err == nil {
		t.Fatal("WorktreeRemove succeeded although the working directory cannot be deleted")
	}

	if f.listsWorktree(t) {
		t.Fatal("git still lists the worktree after a removal that failed while deleting it")
	}
	for _, name := range []string{"commondir", "HEAD", "index"} {
		if _, err := os.Lstat(filepath.Join(f.admin, name)); err != nil {
			t.Fatalf("admin %s was deleted before the working directory was gone: %v", name, err)
		}
	}

	// The failed removal is retried once the obstacle is gone.
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
		t.Fatalf("WorktreeRemove retry: %v", err)
	}
	if _, err := os.Lstat(f.wt); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists after the retry (lstat error %v)", err)
	}
}

func TestWorktreeRemoveSweepsLeftoverAdminEntriesOnlyAfterTheGracePeriod(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	second := filepath.Join(t.TempDir(), "run-2")
	if err := WorktreeAdd(f.ctx, f.bare, second, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if err := WorktreeRemove(f.ctx, f.bare, second); err != nil {
		t.Fatalf("WorktreeRemove second: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.admin, "commondir")); err != nil {
		t.Fatalf("a leftover younger than the grace period was swept: %v", err)
	}

	old := time.Now().Add(-2 * removedWorktreeGrace)
	if err := os.Chtimes(f.admin, old, old); err != nil {
		t.Fatal(err)
	}
	third := filepath.Join(t.TempDir(), "run-3")
	if err := WorktreeAdd(f.ctx, f.bare, third, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	// A live worktree's admin directory is never swept, however old.
	liveAdmin := filepath.Join(f.bare, "worktrees", "run-3")
	if err := os.Chtimes(liveAdmin, old, old); err != nil {
		t.Fatal(err)
	}
	fourth := filepath.Join(t.TempDir(), "run-4")
	if err := WorktreeAdd(f.ctx, f.bare, fourth, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if err := WorktreeRemove(f.ctx, f.bare, fourth); err != nil {
		t.Fatalf("WorktreeRemove fourth: %v", err)
	}

	if _, err := os.Lstat(f.admin); !os.IsNotExist(err) {
		t.Fatalf("a leftover older than the grace period was not swept (lstat error %v)", err)
	}
	if _, err := os.Stat(filepath.Join(f.bare, "worktrees", "run-2", "commondir")); err != nil {
		t.Fatalf("the younger leftover was swept: %v", err)
	}
	if got, err := Run(f.ctx, third, "rev-parse", "HEAD"); err != nil || got != f.sha {
		t.Fatalf("live worktree after the sweep: rev-parse = %q, %v", got, err)
	}
}

func TestWorktreeRemoveRefusesADirectoryThatIsNotThisRepositorysWorktree(t *testing.T) {
	f := newWorktreeRemoveFixture(t)

	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plain, "keep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WorktreeRemove(f.ctx, f.bare, plain); !errors.Is(err, ErrNotLinkedWorktree) {
		t.Fatalf("WorktreeRemove of a directory that is no worktree = %v, want ErrNotLinkedWorktree", err)
	}
	if _, err := os.Stat(filepath.Join(plain, "keep.txt")); err != nil {
		t.Fatalf("a directory that is no worktree was touched: %v", err)
	}

	// A worktree of another repository is refused too.
	other := filepath.Join(t.TempDir(), "other.git")
	if err := InitBare(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := WorktreeRemove(f.ctx, other, f.wt); !errors.Is(err, ErrNotLinkedWorktree) {
		t.Fatalf("WorktreeRemove of another repository's worktree = %v, want ErrNotLinkedWorktree", err)
	}
	if !f.listsWorktree(t) {
		t.Fatal("another repository's worktree was unlisted")
	}
	if _, err := os.Stat(filepath.Join(f.wt, ".git")); err != nil {
		t.Fatalf("another repository's worktree was touched: %v", err)
	}
}

func TestWorktreeRemoveFromANonBareRepository(t *testing.T) {
	ctx := context.Background()
	work := initTestRepo(t)
	sha := run(t, work, "git", "rev-parse", "HEAD")
	wt := filepath.Join(t.TempDir(), "linked")
	if err := WorktreeAdd(ctx, work, wt, sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	if err := WorktreeRemove(ctx, work, wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}
	if _, err := os.Lstat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists (lstat error %v)", err)
	}
	out, err := Run(ctx, work, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "linked") {
		t.Fatalf("git still lists the worktree:\n%s", out)
	}
}
