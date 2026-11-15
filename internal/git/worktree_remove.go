package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// removedWorktreeGrace is how long the leftover admin entry of a removed
// worktree (see WorktreeRemove) stays before a later removal sweeps it. The
// only process the leftover exists for is one that read the entry's gitdir
// file before it was unlinked and has not yet read its commondir, two steps
// git takes back to back, so a minute is far beyond any real gap.
const removedWorktreeGrace = time.Minute

// The files of a linked worktree's admin directory (<common dir>/worktrees/<id>)
// that this file handles by name.
const (
	worktreeAdminGitdir    = "gitdir"
	worktreeAdminCommondir = "commondir"
	worktreeAdminLocked    = "locked"
)

// removedWorktreeLockReason is the content of the lock file WorktreeRemove
// leaves in a removed worktree's admin entry. It is also how the sweep tells
// such a leftover from a worktree somebody locked.
const removedWorktreeLockReason = "removed by no-mistakes; this leftover entry is deleted by a later removal\n"

// removedWorktreeTrash is the directory, beside the repository's worktrees
// directory, that the sweep moves a leftover entry into to delete it.
const removedWorktreeTrash = "no-mistakes-removed-worktrees"

// ErrNotLinkedWorktree is what WorktreeRemove returns, wrapped, for a path that
// is not a linked worktree of the repository it was asked to remove it from.
var ErrNotLinkedWorktree = errors.New("not a linked worktree of the repository")

// WorktreeRemove removes the linked worktree at wtPath from the repository at
// repoDir: the working directory, and the worktree's registration with git.
//
// It does not run `git worktree remove`, which deletes the admin directory
// worktrees/<id> file by file while other git processes may be walking it.
// Every git command that enumerates worktrees - the connectivity check of a
// push among them - reads worktrees/<id>/gitdir, then checks that
// worktrees/<id>/commondir exists and reads it, and dies with "failed to read
// worktrees/<id>/commondir" when the file is deleted between that check and
// the read. A push arriving while a finished run's worktree was being removed
// failed that way.
//
// The removal here is ordered so no reader can hit that:
//
//  1. Write the admin lock file, then unlink the admin gitdir file. Git skips
//     an admin entry without a gitdir file, so from here on no new reader looks
//     at the entry at all.
//  2. Delete the working directory, its .git file last.
//  3. Delete the rest of the admin directory except commondir, which a reader
//     that got past step 1 in time still reads, and the lock file.
//
// The lock file is what keeps git's own hands off the leftover: an entry
// without a gitdir file is prunable, the maintenance git runs after every push
// prunes as soon as one entry is, and that prune deletes file by file again -
// and takes an entry a concurrent `git worktree add` has only just created
// with it. A locked entry is never pruned.
//
// The leftover (two small files) is deleted by a later WorktreeRemove on the
// same repository once it is older than removedWorktreeGrace.
//
// Unlike `git worktree remove --force`, this also removes a locked worktree
// and one that holds submodules. A wtPath that is not a linked worktree of
// repoDir is refused untouched with ErrNotLinkedWorktree. A removal that fails
// part way can be retried.
func WorktreeRemove(ctx context.Context, repoDir, wtPath string) error {
	adminDir, err := worktreeAdminDir(ctx, repoDir, wtPath)
	if err != nil {
		return err
	}
	sweepRemovedWorktreeAdmins(filepath.Dir(adminDir), adminDir, time.Now())

	if err := os.WriteFile(filepath.Join(adminDir, worktreeAdminLocked), []byte(removedWorktreeLockReason), 0o644); err != nil {
		return fmt.Errorf("lock worktree admin directory %s: %w", adminDir, err)
	}
	if err := os.Remove(filepath.Join(adminDir, worktreeAdminGitdir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("unlist worktree %s: %w", wtPath, err)
	}
	if err := removeWorktreeDirectory(wtPath); err != nil {
		return fmt.Errorf("remove worktree directory %s: %w", wtPath, err)
	}
	if err := removeAllExcept(adminDir, worktreeAdminCommondir, worktreeAdminLocked); err != nil {
		return fmt.Errorf("clear worktree admin directory %s: %w", adminDir, err)
	}
	return nil
}

// worktreeAdminDir returns the admin directory (<common dir>/worktrees/<id>) of
// the linked worktree at wtPath, read from the worktree's own .git file, and
// fails unless it is an entry of repoDir's worktrees directory.
func worktreeAdminDir(ctx context.Context, repoDir, wtPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(wtPath, ".git"))
	if err != nil {
		return "", fmt.Errorf("%s: %w: %v", wtPath, ErrNotLinkedWorktree, err)
	}
	pointer, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return "", fmt.Errorf("%s: %w: its .git file names no gitdir", wtPath, ErrNotLinkedWorktree)
	}
	adminDir := strings.TrimSpace(pointer)
	if !filepath.IsAbs(adminDir) {
		adminDir = filepath.Join(wtPath, adminDir)
	}
	adminDir, err = filepath.EvalSymlinks(adminDir)
	if err != nil {
		return "", fmt.Errorf("%s: %w: resolve admin directory: %v", wtPath, ErrNotLinkedWorktree, err)
	}

	commonDir, err := Run(ctx, repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git directory of %s: %w", repoDir, err)
	}
	worktreesDir, err := filepath.EvalSymlinks(filepath.Join(commonDir, "worktrees"))
	if err != nil {
		return "", fmt.Errorf("%s: %w %s: %v", wtPath, ErrNotLinkedWorktree, repoDir, err)
	}
	if filepath.Dir(adminDir) != worktreesDir {
		return "", fmt.Errorf("%s: %w %s: its admin directory is %s", wtPath, ErrNotLinkedWorktree, repoDir, adminDir)
	}
	return adminDir, nil
}

// removeWorktreeDirectory deletes the working directory at wtPath, its .git
// file last: that file is what names the worktree's admin directory, so a
// removal that failed on some other entry can still be retried.
func removeWorktreeDirectory(wtPath string) error {
	if err := removeAllExcept(wtPath, ".git"); err != nil {
		return err
	}
	return os.RemoveAll(wtPath)
}

// removeAllExcept deletes every entry of dir but the named ones. A dir that
// does not exist has nothing to delete.
func removeAllExcept(dir string, keep ...string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var errs []error
entries:
	for _, entry := range entries {
		for _, name := range keep {
			if entry.Name() == name {
				continue entries
			}
		}
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sweepRemovedWorktreeAdmins deletes, best effort, the admin entries under
// worktreesDir that an earlier WorktreeRemove left behind - no gitdir file, and
// this package's lock file older than removedWorktreeGrace. skip is the entry
// the caller is removing right now.
//
// An entry is renamed out of worktreesDir before it is deleted, so it leaves
// in one step: deleted in place it would, once its lock file is gone, be a
// prunable entry until its directory went, and one prunable entry is all it
// takes for the maintenance after a push to prune (see WorktreeRemove). The
// rename leaves one such moment: git also counts as prunable an entry it
// listed and then finds gone.
func sweepRemovedWorktreeAdmins(worktreesDir, skip string, now time.Time) {
	entries, err := os.ReadDir(worktreesDir)
	if err != nil {
		return
	}
	trash := filepath.Join(filepath.Dir(worktreesDir), removedWorktreeTrash)
	swept := false
	for _, entry := range entries {
		dir := filepath.Join(worktreesDir, entry.Name())
		if !entry.IsDir() || dir == skip {
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, worktreeAdminGitdir)); !os.IsNotExist(err) {
			continue
		}
		lock := filepath.Join(dir, worktreeAdminLocked)
		info, err := os.Lstat(lock)
		if err != nil || now.Sub(info.ModTime()) < removedWorktreeGrace {
			continue
		}
		if reason, err := os.ReadFile(lock); err != nil || string(reason) != removedWorktreeLockReason {
			continue
		}
		if err := os.MkdirAll(trash, 0o755); err != nil {
			return
		}
		target := filepath.Join(trash, entry.Name())
		_ = os.RemoveAll(target)
		if err := os.Rename(dir, target); err == nil {
			swept = true
		}
	}
	if swept {
		// Whatever is in the trash is already out of git's sight, an entry an
		// interrupted sweep left there included.
		_ = removeAllExcept(trash)
	}
}
