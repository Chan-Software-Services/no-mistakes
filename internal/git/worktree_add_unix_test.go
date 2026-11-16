//go:build unix

package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A git process walking the gate's worktrees while a run starts must never see
// the new worktree's admin entry half-built, and a prune must never take it.
// The smudge filter runs in the middle of the add's checkout and looks at the
// gate from outside, as a concurrent push or prune would: git's own add lists
// the entry there already.
func TestWorktreeAddListsTheWorktreeOnlyOnceItIsComplete(t *testing.T) {
	f := routeThroughFilter(t, "probe")
	out := t.TempDir()
	entry := filepath.Join(f.repo, "worktrees", "run-1")
	script := filepath.Join(t.TempDir(), "probe.sh")
	writeFile(t, script, `#!/bin/sh
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_PREFIX
out='`+out+`'
gate='`+f.repo+`'
entry='`+entry+`'
ls -A "$entry" > "$out/entry" 2>&1
cat "$entry/commondir" > "$out/commondir" 2>&1
git --git-dir="$gate" worktree list --porcelain > "$out/list" 2>&1
git --git-dir="$gate" worktree prune --expire now > "$out/prune" 2>&1
ls -A "$entry" > "$out/after-prune" 2>&1
cat
`)
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, f.repo, "git", "--git-dir="+f.repo, "config", "filter.probe.smudge", script)

	wt := filepath.Join(t.TempDir(), "run-1")
	if err := WorktreeAdd(f.ctx, f.repo, wt, f.sha); err != nil {
		t.Fatalf("WorktreeAdd: %v", err)
	}

	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("the filter did not record %s: %v", name, err)
		}
		return string(data)
	}
	during := strings.Fields(read("entry"))
	if !containsName(during, "locked") || !containsName(during, "commondir") {
		t.Errorf("during the checkout the entry holds %v, want locked and commondir", during)
	}
	if containsName(during, "gitdir") {
		t.Errorf("during the checkout the entry holds a gitdir file (%v): it is listed before it is complete", during)
	}
	if got := strings.TrimSpace(read("commondir")); got == "" {
		t.Error("during the checkout the entry's commondir is empty")
	}
	if list := read("list"); strings.Contains(list, "run-1") {
		t.Errorf("during the checkout the gate lists the worktree:\n%s", list)
	}
	if after := strings.Fields(read("after-prune")); !containsName(after, "commondir") || !containsName(after, "locked") {
		t.Errorf("a prune during the checkout left the entry holding %v (prune said %q)", after, read("prune"))
	}

	if !f.listed(t, wt) {
		t.Fatal("git does not list the worktree after the add")
	}
	for _, name := range []string{"locked", "gitdir.pending"} {
		if _, err := os.Lstat(filepath.Join(entry, name)); !os.IsNotExist(err) {
			t.Errorf("the entry still holds %s after the add (lstat error %v)", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(wt, "probe.txt")); err != nil || string(data) != "probed\n" {
		t.Fatalf("probe.txt in the worktree = %q, %v; want it checked out through the filter", data, err)
	}
	assertUsableDetachedWorktree(t, f, wt)
}
