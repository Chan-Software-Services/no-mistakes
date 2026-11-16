package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

// adminEntryNames returns the sorted names in a worktree's admin directory.
func adminEntryNames(t *testing.T, admin string) []string {
	t.Helper()
	entries, err := os.ReadDir(admin)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A git process that enumerated the gate's worktrees just before the removal
// goes on to read worktrees/<id>/commondir, and dies with "failed to read
// worktrees/<id>/commondir" when the file vanished after it checked that it
// exists. The removal therefore leaves commondir in place for such a reader
// and only unlists the entry.
func TestWorktreeRemoveKeepsWhatAReaderAlreadyListingTheWorktreeStillReads(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	gitdirContent, err := os.ReadFile(filepath.Join(f.admin, "gitdir"))
	if err != nil {
		t.Fatal(err)
	}

	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	if names := adminEntryNames(t, f.admin); !slices.Equal(names, []string{"commondir", "locked"}) {
		t.Fatalf("removed worktree's admin directory holds %v, want only commondir and locked", names)
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

// The leftover has no gitdir file, which makes it prunable to git, and git
// prunes on its own: the maintenance it runs after every push prunes worktrees
// as soon as one entry is prunable. That prune deletes the leftover file by
// file, the very thing the removal avoids, so the leftover carries a lock file
// that keeps git from pruning it.
func TestWorktreeRemoveLeftoverSurvivesGitsOwnPruning(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	if _, err := Run(f.ctx, f.bare, "worktree", "prune", "--expire=now"); err != nil {
		t.Fatalf("worktree prune: %v", err)
	}
	if _, err := Run(f.ctx, f.bare, "maintenance", "run", "--auto"); err != nil {
		t.Fatalf("maintenance run --auto: %v", err)
	}
	run(t, f.work, "git", "commit", "--allow-empty", "-m", "next")
	run(t, f.work, "git", "push", f.bare, "HEAD:refs/heads/next")

	if names := adminEntryNames(t, f.admin); !slices.Equal(names, []string{"commondir", "locked"}) {
		t.Fatalf("after git's own pruning the leftover holds %v, want commondir and locked", names)
	}
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
	addAndRemove := func(name string) {
		t.Helper()
		wt := filepath.Join(t.TempDir(), name)
		if err := WorktreeAdd(f.ctx, f.bare, wt, f.sha); err != nil {
			t.Fatalf("worktree add %s: %v", name, err)
		}
		if err := WorktreeRemove(f.ctx, f.bare, wt); err != nil {
			t.Fatalf("WorktreeRemove %s: %v", name, err)
		}
	}
	old := time.Now().Add(-2 * removedWorktreeGrace)
	age := func(path string) {
		t.Helper()
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	addAndRemove("run-2")
	if _, err := os.Stat(filepath.Join(f.admin, "commondir")); err != nil {
		t.Fatalf("a leftover younger than the grace period was swept: %v", err)
	}

	// The leftover's age is its lock file's.
	age(filepath.Join(f.admin, "locked"))

	// A live worktree is never swept, however old and even when locked.
	live := filepath.Join(t.TempDir(), "run-3")
	if err := WorktreeAdd(f.ctx, f.bare, live, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	liveAdmin := filepath.Join(f.bare, "worktrees", "run-3")
	if err := os.WriteFile(filepath.Join(liveAdmin, "locked"), []byte(removedWorktreeLockReason), 0o644); err != nil {
		t.Fatal(err)
	}
	age(filepath.Join(liveAdmin, "locked"))

	// Nor is an entry without a gitdir file that somebody else locked.
	foreign := filepath.Join(t.TempDir(), "run-4")
	if err := WorktreeAdd(f.ctx, f.bare, foreign, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	foreignAdmin := filepath.Join(f.bare, "worktrees", "run-4")
	if err := os.WriteFile(filepath.Join(foreignAdmin, "locked"), []byte("on a portable drive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	age(filepath.Join(foreignAdmin, "locked"))
	if err := os.Remove(filepath.Join(foreignAdmin, "gitdir")); err != nil {
		t.Fatal(err)
	}

	addAndRemove("run-5")

	if _, err := os.Lstat(f.admin); !os.IsNotExist(err) {
		t.Fatalf("a leftover older than the grace period was not swept (lstat error %v)", err)
	}
	if names := adminEntryNames(t, filepath.Join(f.bare, removedWorktreeTrash)); len(names) != 0 {
		t.Fatalf("the sweep left %v in its trash directory", names)
	}
	if _, err := os.Stat(filepath.Join(f.bare, "worktrees", "run-2", "commondir")); err != nil {
		t.Fatalf("the younger leftover was swept: %v", err)
	}
	if got, err := Run(f.ctx, live, "rev-parse", "HEAD"); err != nil || got != f.sha {
		t.Fatalf("live worktree after the sweep: rev-parse = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(foreignAdmin, "HEAD")); err != nil {
		t.Fatalf("an entry somebody else locked was swept: %v", err)
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

// The daemon owns its run worktrees outright, so a lock somebody put on one
// does not keep it: `git worktree remove --force` refuses a locked worktree.
func TestWorktreeRemoveRemovesALockedWorktree(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	if _, err := Run(f.ctx, f.bare, "worktree", "lock", "--reason", "kept", f.wt); err != nil {
		t.Fatalf("worktree lock: %v", err)
	}

	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	if _, err := os.Lstat(f.wt); !os.IsNotExist(err) {
		t.Fatalf("locked worktree's directory still exists (lstat error %v)", err)
	}
	if f.listsWorktree(t) {
		t.Fatal("git still lists the locked worktree after removal")
	}
}

// The admin entry comes from the working directory's own .git file, which a
// process in the run can rewrite. An entry that does not point back at the
// working directory belongs to another worktree and is left alone.
func TestWorktreeRemoveRefusesAnAdminEntryThatPointsAtAnotherWorktree(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	other := filepath.Join(t.TempDir(), "run-2")
	if err := WorktreeAdd(f.ctx, f.bare, other, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	otherAdmin := filepath.Join(f.bare, "worktrees", "run-2")
	if err := os.WriteFile(filepath.Join(f.wt, ".git"), []byte("gitdir: "+otherAdmin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WorktreeRemove(f.ctx, f.bare, f.wt); !errors.Is(err, ErrNotLinkedWorktree) {
		t.Fatalf("WorktreeRemove through a .git file naming another worktree's entry = %v, want ErrNotLinkedWorktree", err)
	}

	if got, err := Run(f.ctx, other, "rev-parse", "HEAD"); err != nil || got != f.sha {
		t.Fatalf("the other worktree after the refusal: rev-parse = %q, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(otherAdmin, "locked")); !os.IsNotExist(err) {
		t.Fatalf("the other worktree's entry was locked (lstat error %v)", err)
	}
	if entries, err := os.ReadDir(f.wt); err != nil || len(entries) < 2 {
		t.Fatalf("the refused working directory was emptied: %d entries, %v", len(entries), err)
	}

	// An entry with no gitdir file is taken only when this code unlisted it.
	if err := os.Remove(filepath.Join(otherAdmin, "gitdir")); err != nil {
		t.Fatal(err)
	}
	if err := WorktreeRemove(f.ctx, f.bare, f.wt); !errors.Is(err, ErrNotLinkedWorktree) {
		t.Fatalf("WorktreeRemove of an entry somebody else unlisted = %v, want ErrNotLinkedWorktree", err)
	}
	if _, err := os.Stat(filepath.Join(otherAdmin, "HEAD")); err != nil {
		t.Fatalf("an entry somebody else unlisted was cleared: %v", err)
	}
}

// A worktree linked with relative paths names its admin entry, and is named by
// it, relative to where each file lives.
func TestWorktreeRemoveRemovesAWorktreeLinkedWithRelativePaths(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	wt := filepath.Join(t.TempDir(), "relative")
	if _, err := Run(f.ctx, f.bare, "worktree", "add", "--relative-paths", "--detach", wt, f.sha); err != nil {
		t.Skipf("git worktree add --relative-paths: %v", err)
	}

	if err := WorktreeRemove(f.ctx, f.bare, wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	if _, err := os.Lstat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists (lstat error %v)", err)
	}
	if names := adminEntryNames(t, filepath.Join(f.bare, "worktrees", "relative")); !slices.Equal(names, []string{"commondir", "locked"}) {
		t.Fatalf("removed worktree's admin directory holds %v, want only commondir and locked", names)
	}
}

// killCreation turns the finished worktree whose admin entry is admin back into
// what WorktreeAdd leaves when it dies after renaming the entry in and before
// listing it: no gitdir file, the creation lock, and gitdir.pending holding
// what gitdir held.
func killCreation(t *testing.T, admin string) {
	t.Helper()
	if err := os.Rename(filepath.Join(admin, worktreeAdminGitdir), filepath.Join(admin, worktreeAdminGitdirPending)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, worktreeAdminLocked), []byte(creatingWorktreeLockReason), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A creation the daemon did not live to finish leaves an unlisted entry under
// the creation lock, its future gitdir file in gitdir.pending. The run's end
// removes its working directory as usual, and that removal takes the entry
// too - but only when gitdir.pending names that working directory, since the
// entry is found through the working directory's own .git file.
func TestWorktreeRemoveRemovesAWorktreeWhoseCreationDied(t *testing.T) {
	t.Run("gitdir.pending names the working directory", func(t *testing.T) {
		f := newWorktreeRemoveFixture(t)
		killCreation(t, f.admin)
		if f.listsWorktree(t) {
			t.Fatal("fixture: git lists a worktree whose creation died")
		}

		if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
			t.Fatalf("WorktreeRemove: %v", err)
		}

		if _, err := os.Lstat(f.wt); !os.IsNotExist(err) {
			t.Fatalf("working directory still exists (lstat error %v)", err)
		}
		if names := adminEntryNames(t, f.admin); !slices.Equal(names, []string{"commondir", "locked"}) {
			t.Fatalf("admin directory holds %v, want only commondir and locked", names)
		}
		if reason, err := os.ReadFile(filepath.Join(f.admin, worktreeAdminLocked)); err != nil || string(reason) != removedWorktreeLockReason {
			t.Fatalf("lock file = %q, %v; want the removal's lock", reason, err)
		}
		if f.listsWorktree(t) {
			t.Fatal("git lists the removed worktree")
		}
	})

	refused := func(t *testing.T, f *worktreeRemoveFixture) {
		t.Helper()
		before := adminEntryNames(t, f.admin)
		if err := WorktreeRemove(f.ctx, f.bare, f.wt); !errors.Is(err, ErrNotLinkedWorktree) {
			t.Fatalf("WorktreeRemove = %v, want ErrNotLinkedWorktree", err)
		}
		if after := adminEntryNames(t, f.admin); !slices.Equal(after, before) {
			t.Fatalf("the refused entry changed from %v to %v", before, after)
		}
		if reason, err := os.ReadFile(filepath.Join(f.admin, worktreeAdminLocked)); err != nil || string(reason) != creatingWorktreeLockReason {
			t.Fatalf("the refused entry's lock = %q, %v; want the creation's lock", reason, err)
		}
		if entries, err := os.ReadDir(f.wt); err != nil || len(entries) < 2 {
			t.Fatalf("the refused working directory was emptied: %d entries, %v", len(entries), err)
		}
	}

	t.Run("gitdir.pending names another path", func(t *testing.T) {
		f := newWorktreeRemoveFixture(t)
		killCreation(t, f.admin)
		elsewhere := filepath.Join(t.TempDir(), "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.admin, worktreeAdminGitdirPending), []byte(filepath.ToSlash(filepath.Join(elsewhere, ".git"))+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		refused(t, f)
	})

	t.Run("gitdir.pending is missing", func(t *testing.T) {
		f := newWorktreeRemoveFixture(t)
		killCreation(t, f.admin)
		if err := os.Remove(filepath.Join(f.admin, worktreeAdminGitdirPending)); err != nil {
			t.Fatal(err)
		}
		refused(t, f)
	})
}

// A dead creation whose working directory nobody removes is left to the sweep,
// and so is a staging directory a creation died in before renaming it in. A
// creation in flight must never be swept, so the grace is far beyond any
// checkout, and far beyond the grace of a removal's leftover.
func TestWorktreeRemoveSweepsADeadCreationOnlyAfterItsGracePeriod(t *testing.T) {
	f := newWorktreeRemoveFixture(t)
	setAge := func(path string, age time.Duration) {
		t.Helper()
		then := time.Now().Add(-age)
		if err := os.Chtimes(path, then, then); err != nil {
			t.Fatal(err)
		}
	}
	old := creatingWorktreeGrace + time.Hour
	young := 2 * removedWorktreeGrace

	// Dead creations, the working directory gone with the daemon's run.
	deadCreation := func(name string, age time.Duration) string {
		t.Helper()
		wt := filepath.Join(t.TempDir(), name)
		if err := WorktreeAdd(f.ctx, f.bare, wt, f.sha); err != nil {
			t.Fatalf("worktree add %s: %v", name, err)
		}
		admin := filepath.Join(f.bare, "worktrees", name)
		killCreation(t, admin)
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		setAge(filepath.Join(admin, worktreeAdminLocked), age)
		return admin
	}
	oldCreation := deadCreation("run-2", old)
	youngCreation := deadCreation("run-3", young)

	// Staging directories of creations that died before the rename.
	staging := func(age time.Duration) string {
		t.Helper()
		root := filepath.Join(f.bare, newWorktreeStaging)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		dir, err := os.MkdirTemp(root, "wt-")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, worktreeAdminLocked), []byte(creatingWorktreeLockReason), 0o644); err != nil {
			t.Fatal(err)
		}
		setAge(dir, age)
		return dir
	}
	oldStaging := staging(old)
	youngStaging := staging(young)

	// A worktree that died between being listed and being unlocked works, and
	// is never swept.
	listed := filepath.Join(t.TempDir(), "run-4")
	if err := WorktreeAdd(f.ctx, f.bare, listed, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	listedLock := filepath.Join(f.bare, "worktrees", "run-4", worktreeAdminLocked)
	if err := os.WriteFile(listedLock, []byte(creatingWorktreeLockReason), 0o644); err != nil {
		t.Fatal(err)
	}
	setAge(listedLock, old)

	// Nor is an unlisted entry somebody else locked.
	foreign := filepath.Join(t.TempDir(), "run-5")
	if err := WorktreeAdd(f.ctx, f.bare, foreign, f.sha); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	foreignAdmin := filepath.Join(f.bare, "worktrees", "run-5")
	if err := os.WriteFile(filepath.Join(foreignAdmin, worktreeAdminLocked), []byte("on a portable drive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(foreignAdmin, worktreeAdminGitdir)); err != nil {
		t.Fatal(err)
	}
	setAge(filepath.Join(foreignAdmin, worktreeAdminLocked), old)

	// The sweep runs in a removal.
	if err := WorktreeRemove(f.ctx, f.bare, f.wt); err != nil {
		t.Fatalf("WorktreeRemove: %v", err)
	}

	if _, err := os.Lstat(oldCreation); !os.IsNotExist(err) {
		t.Fatalf("a dead creation older than the grace period was not swept (lstat error %v)", err)
	}
	if names := adminEntryNames(t, filepath.Join(f.bare, removedWorktreeTrash)); len(names) != 0 {
		t.Fatalf("the sweep left %v in its trash directory", names)
	}
	if _, err := os.Lstat(oldStaging); !os.IsNotExist(err) {
		t.Fatalf("a staging directory older than the grace period was not swept (lstat error %v)", err)
	}
	if _, err := os.Stat(filepath.Join(youngCreation, worktreeAdminGitdirPending)); err != nil {
		t.Fatalf("a dead creation younger than the grace period was swept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(youngStaging, worktreeAdminLocked)); err != nil {
		t.Fatalf("a staging directory younger than the grace period was swept: %v", err)
	}
	if got, err := Run(f.ctx, listed, "rev-parse", "HEAD"); err != nil || got != f.sha {
		t.Fatalf("listed worktree after the sweep: rev-parse = %q, %v", got, err)
	}
	if _, err := os.Stat(listedLock); err != nil {
		t.Fatalf("a listed worktree under the creation lock was swept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(foreignAdmin, "HEAD")); err != nil {
		t.Fatalf("an entry somebody else locked was swept: %v", err)
	}
}
