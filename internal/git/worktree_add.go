package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// newWorktreeStaging is the directory, beside the repository's worktrees
// directory, in which WorktreeAdd builds a new worktree's admin entry before
// renaming it into worktrees/.
const newWorktreeStaging = "no-mistakes-new-worktrees"

// creatingWorktreeLockReason is the content of the lock file a worktree's admin
// entry carries while WorktreeAdd builds it, which tells it from a worktree
// somebody else locked.
const creatingWorktreeLockReason = "being created by no-mistakes; a later worktree removal deletes this entry if the creation died\n"

// worktreeAdminGitdirPending is the file of an admin entry being created that
// holds what its gitdir file will hold once the worktree is complete.
const worktreeAdminGitdirPending = "gitdir.pending"

// maxWorktreeEntrySuffix bounds the numbers WorktreeAdd appends to an admin
// entry's name that is already taken.
const maxWorktreeEntrySuffix = 999

// WorktreeAdd creates a worktree of the repository at repoDir at wtPath, its
// HEAD detached at the commit rev names (a SHA, a branch name or a full ref).
//
// On a repository whose refs are in the files format it does not run
// `git worktree add`, which builds the admin entry worktrees/<id> in place,
// one file at a time, while other git processes may be walking the
// repository's worktrees. Git writes the entry's gitdir file, which makes every
// reader look at the entry, before its commondir, and a reader in between - a
// push's connectivity check among them - dies with "failed to read
// worktrees/<id>/commondir". And git writes the entry's lock file only after
// creating its directory, so the prune that the maintenance after a push runs
// can delete the entry in between, failing the add.
//
// The creation here follows the rules it shares with WorktreeRemove, its
// mirror:
//
//   - An admin entry has a gitdir file (is listed) only while it is complete.
//     Removal unlists first; creation lists last.
//   - An admin entry without a gitdir file is locked for as long as it sits in
//     worktrees/, so git never prunes it file by file. Removal writes the lock
//     before it unlists; creation brings the lock with it.
//   - An entry without a gitdir file enters and leaves worktrees/ in one
//     rename. Creation renames a fully staged entry in; the sweep renames a
//     leftover out.
//
// So the entry is staged under newWorktreeStaging, holding the lock file, its
// commondir, its HEAD and its future gitdir file (worktreeAdminGitdirPending),
// renamed into worktrees/, checked out through its named git directory, then
// listed by renaming gitdir.pending to gitdir, and unlocked.
//
// A wtPath that exists and is not an empty directory is refused untouched. A
// creation that fails part way, a cancelled context included, undoes what it
// did and returns the original error. What a creation the process did not live
// to finish left is taken by a later WorktreeRemove on the same repository: of
// its working directory, which also takes the unlisted entry, or by that
// removal's sweep once it is older than creatingWorktreeGrace.
//
// On a repository in another ref format (extensions.refstorage), whose HEAD
// cannot be written as a file, it runs `git worktree add --detach`, and the
// two windows above stay open there.
func WorktreeAdd(ctx context.Context, repoDir, wtPath, rev string) error {
	format, err := Run(ctx, repoDir, "config", "--get", "extensions.refstorage")
	if err != nil && !isConfigKeyUnset(err) {
		return fmt.Errorf("create worktree %s: read extensions.refstorage: %w", wtPath, err)
	}
	if err == nil && format != "files" {
		_, err := Run(ctx, repoDir, "worktree", "add", "--detach", wtPath, rev)
		return err
	}
	c := &worktreeCreation{repoDir: repoDir, wtPath: wtPath}
	if err := c.create(ctx, rev); err != nil {
		c.undo()
		return fmt.Errorf("create worktree %s: %w", wtPath, err)
	}
	return nil
}

// isConfigKeyUnset reports whether a `git config --get` failure is the "key
// not set" case (exit 1).
func isConfigKeyUnset(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

// worktreeCreation is one WorktreeAdd on a files-format repository, and what
// it has done so far, so a failure can undo it.
type worktreeCreation struct {
	repoDir string
	wtPath  string

	commonDir string
	wtAbs     string

	staging      string // the staged admin entry while it is still staged
	adminDir     string // the admin entry once it is renamed into worktrees/
	listed       bool   // the admin entry has its gitdir file
	createdWt    bool   // this creation made wtAbs
	populatedWt  bool   // this creation wrote into wtAbs
	wtExistedDir bool   // wtAbs was an empty directory before
}

func (c *worktreeCreation) create(ctx context.Context, rev string) error {
	commonDir, err := Run(ctx, c.repoDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve git directory of %s: %w", c.repoDir, err)
	}
	if c.commonDir, err = filepath.EvalSymlinks(commonDir); err != nil {
		return fmt.Errorf("resolve git directory of %s: %w", c.repoDir, err)
	}
	commit, err := Run(ctx, c.repoDir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return fmt.Errorf("resolve %s to a commit: %w", rev, err)
	}
	if c.wtAbs, err = filepath.Abs(c.wtPath); err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}
	if err := c.checkPathFree(); err != nil {
		return err
	}
	worktreeConfig, err := c.worktreeConfigToCopy(ctx)
	if err != nil {
		return err
	}
	// The real path of the working directory, as git writes it, though the
	// directory itself does not exist yet.
	wtReal, err := realPathOfMissing(c.wtAbs)
	if err != nil {
		return fmt.Errorf("resolve worktree path: %w", err)
	}

	// Stage the admin entry where git does not look.
	stagingRoot := filepath.Join(c.commonDir, newWorktreeStaging)
	worktreesDir := filepath.Join(c.commonDir, "worktrees")
	for _, dir := range []string{stagingRoot, worktreesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if c.staging, err = os.MkdirTemp(stagingRoot, "wt-"); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	// MkdirTemp makes it private; git's own entries are not.
	if err := os.Chmod(c.staging, 0o755); err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	files := []struct{ name, content string }{
		{worktreeAdminLocked, creatingWorktreeLockReason},
		{worktreeAdminCommondir, "../..\n"},
		{"HEAD", commit + "\n"},
		{worktreeAdminGitdirPending, filepath.ToSlash(filepath.Join(wtReal, ".git")) + "\n"},
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(c.staging, file.name), []byte(file.content), 0o644); err != nil {
			return fmt.Errorf("stage admin entry: %w", err)
		}
	}
	if worktreeConfig != "" {
		if err := c.stageWorktreeConfig(ctx, worktreeConfig); err != nil {
			return err
		}
	}

	// Rename it in, complete, locked and unlisted.
	if err := c.renameIn(worktreesDir); err != nil {
		return err
	}

	// The working directory, and its checkout through the named git directory.
	if err := c.makeWorkingDirectory(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(c.wtAbs, ".git"), []byte("gitdir: "+filepath.ToSlash(c.adminDir)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write .git file: %w", err)
	}
	if _, err := Run(ctx, c.wtAbs, "--git-dir="+c.adminDir, "--work-tree="+c.wtAbs, "reset", "--hard", "--no-recurse-submodules", "--quiet"); err != nil {
		return fmt.Errorf("check out %s: %w", commit, err)
	}

	// List it, then unlock it.
	if err := os.Rename(filepath.Join(c.adminDir, worktreeAdminGitdirPending), filepath.Join(c.adminDir, worktreeAdminGitdir)); err != nil {
		return fmt.Errorf("list worktree: %w", err)
	}
	c.listed = true
	if err := os.Remove(filepath.Join(c.adminDir, worktreeAdminLocked)); err != nil {
		return fmt.Errorf("unlock worktree: %w", err)
	}
	return nil
}

// checkPathFree refuses a wtPath that exists and is not an empty directory, as
// git does, and notes whether it exists.
func (c *worktreeCreation) checkPathFree() error {
	info, err := os.Lstat(c.wtAbs)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect worktree path: %w", err)
	}
	if info.IsDir() {
		entries, err := os.ReadDir(c.wtAbs)
		if err != nil {
			return fmt.Errorf("inspect worktree path: %w", err)
		}
		if len(entries) == 0 {
			c.wtExistedDir = true
			return nil
		}
	}
	return fmt.Errorf("worktree path %s already exists and is not empty", c.wtPath)
}

// worktreeConfigToCopy returns the repository's own config.worktree when
// extensions.worktreeConfig is on and the file exists, and "" otherwise. Git's
// own add copies it into every new worktree.
func (c *worktreeCreation) worktreeConfigToCopy(ctx context.Context) (string, error) {
	enabled, err := Run(ctx, c.repoDir, "config", "--type=bool", "--get", "extensions.worktreeConfig")
	if err != nil {
		if isConfigKeyUnset(err) {
			return "", nil
		}
		return "", fmt.Errorf("read extensions.worktreeConfig: %w", err)
	}
	if enabled != "true" {
		return "", nil
	}
	path := filepath.Join(c.commonDir, "config.worktree")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("inspect %s: %w", path, err)
	}
	return path, nil
}

// stageWorktreeConfig copies the repository's config.worktree into the staged
// entry without core.bare and core.worktree, as `git worktree add` does. On a
// gate that carries its pinned core.hookspath into the worktree and keeps the
// gate's core.bare=true out of it.
func (c *worktreeCreation) stageWorktreeConfig(ctx context.Context, source string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read %s: %w", source, err)
	}
	copyPath := filepath.Join(c.staging, "config.worktree")
	if err := os.WriteFile(copyPath, data, 0o644); err != nil {
		return fmt.Errorf("stage config.worktree: %w", err)
	}
	for _, key := range []string{"core.bare", "core.worktree"} {
		if _, err := Run(ctx, c.repoDir, "config", "--file", copyPath, "--unset-all", key); err != nil && !isConfigKeyMissing(err) {
			return fmt.Errorf("remove %s from the worktree's config: %w", key, err)
		}
	}
	return nil
}

// renameIn renames the staged entry to worktrees/<id>, taking <id>1, <id>2 and
// so on while the name is taken.
func (c *worktreeCreation) renameIn(worktreesDir string) error {
	id := worktreeEntryName(c.wtAbs)
	for n := 0; n <= maxWorktreeEntrySuffix; n++ {
		name := id
		if n > 0 {
			name += strconv.Itoa(n)
		}
		target := filepath.Join(worktreesDir, name)
		// A rename replaces an empty directory on some systems; never take the
		// place of anything already there.
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			continue
		}
		err := os.Rename(c.staging, target)
		if err == nil {
			c.staging = ""
			c.adminDir = target
			return nil
		}
		if _, statErr := os.Lstat(target); statErr == nil {
			continue
		}
		return fmt.Errorf("move admin entry into %s: %w", worktreesDir, err)
	}
	return fmt.Errorf("move admin entry into %s: every name from %s to %s%d is taken", worktreesDir, id, id, maxWorktreeEntrySuffix)
}

// worktreeEntryName is the admin entry name for a worktree at wtPath: its base
// name, with every character outside [A-Za-z0-9._-] replaced by "-".
func worktreeEntryName(wtPath string) string {
	name := []byte(filepath.Base(wtPath))
	for i, ch := range name {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '.', ch == '_', ch == '-':
		default:
			name[i] = '-'
		}
	}
	if len(name) == 0 || string(name) == "." || string(name) == ".." || string(name) == string(filepath.Separator) {
		return "worktree"
	}
	return string(name)
}

func (c *worktreeCreation) makeWorkingDirectory() error {
	if c.wtExistedDir {
		c.populatedWt = true
		return nil
	}
	parent := filepath.Dir(c.wtAbs)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", parent, err)
	}
	if err := os.Mkdir(c.wtAbs, 0o755); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("worktree path %s already exists and is not empty", c.wtPath)
		}
		return fmt.Errorf("create worktree directory: %w", err)
	}
	c.createdWt = true
	c.populatedWt = true
	return nil
}

// undo takes back, best effort, what a failed create did. It does no git work
// that a cancelled context could stop, except the removal of an entry that was
// already listed.
func (c *worktreeCreation) undo() {
	if c.listed {
		_ = WorktreeRemove(context.Background(), c.repoDir, c.wtAbs)
		return
	}
	if c.populatedWt {
		if c.createdWt {
			_ = os.RemoveAll(c.wtAbs)
		} else {
			_ = removeAllExcept(c.wtAbs)
		}
	}
	if c.adminDir != "" {
		// Out of worktrees/ in one step: deleted in place the entry would lose
		// its lock before its directory, and be prunable in between.
		trash := filepath.Join(c.commonDir, removedWorktreeTrash)
		if err := os.MkdirAll(trash, 0o755); err == nil {
			if holder, err := os.MkdirTemp(trash, "undo-"); err == nil {
				if os.Rename(c.adminDir, filepath.Join(holder, filepath.Base(c.adminDir))) == nil {
					c.adminDir = ""
				}
				_ = os.RemoveAll(holder)
			}
		}
	}
	if c.staging != "" {
		_ = os.RemoveAll(c.staging)
	}
}

// realPathOfMissing resolves the symlinks of the longest leading part of path
// that exists, and appends the rest.
func realPathOfMissing(path string) (string, error) {
	rest := ""
	for dir := path; ; {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if rest == "" {
				return real, nil
			}
			return filepath.Join(real, rest), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return path, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}
