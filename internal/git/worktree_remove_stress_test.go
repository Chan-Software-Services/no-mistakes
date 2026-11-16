package git

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWorktreeRemoveStressAgainstConcurrentPushes is the reproduction loop for
// the races around a gate repository's run worktrees: pushes to a bare gate
// while its linked worktrees are added and removed, as the daemon does around
// every run. A push must never fail, whatever an add or a removal is doing,
// and neither must the add or the removal itself, which walk the worktrees too.
//
// Adds overlap pushes, removals and each other. Each remover adds a worktree,
// pauses for up to 100ms, so the removal lands at no particular point of a
// push, and removes it. Every failure fails the loop, and the summary counts
// each by what failed and what it died on:
//
//   - a pruned add, one that "could not open" a file of its own new entry: the
//     `git maintenance run --auto` git runs after a push pruned the entry
//     before the add had locked it (with `maintenance.auto=false` in
//     NM_WORKTREE_REMOVE_STRESS_GATE_CONFIG it does not happen);
//   - a reader of the worktrees (a push, an add, a removal) that died reading
//     the entry of a worktree being added: `git worktree add` lists the entry
//     before it has written its commondir, and a reader that reads the file
//     empty dies with "failed to read worktrees/<id>/commondir: <errno>",
//     where <errno> is whatever error the reader had left behind ("Success",
//     "Numerical result out of range", "No such file or directory"), so the
//     text alone does not tell this window apart;
//   - a reader that died reading the entry of a worktree being removed, which
//     `git worktree remove` deletes file by file.
//
// A failure is put in the second or third kind when the worktree it names was
// being added or removed while the failed command ran, and its errno text is
// kept beside the kind. The failure is logged with what its remover was doing
// to that worktree and when.
//
// The windows are a few microseconds wide per push, so the loop only means
// something at volume. It is skipped unless NM_WORKTREE_REMOVE_STRESS names
// how long to run (a Go duration, e.g. 2m). Its other settings:
//
//   - NM_WORKTREE_REMOVE_STRESS_PUSHERS: concurrent pushers (default 4).
//   - NM_WORKTREE_REMOVE_STRESS_REMOVERS: worktrees added and removed at once
//     (default 1; each live worktree is one more read per push).
//   - NM_WORKTREE_REMOVE_STRESS_GIT_ADD: when set, add with
//     `git worktree add --detach`, the creation WorktreeAdd replaced, to see
//     the loop fail.
//   - NM_WORKTREE_REMOVE_STRESS_GIT_REMOVE: when set, remove with
//     `git worktree remove --force`, the removal WorktreeRemove replaced, to
//     see the loop fail.
//   - NM_WORKTREE_REMOVE_STRESS_GATE_CONFIG: space-separated key=value git
//     settings for the gate repository.
//
// Starve it to make the windows easier to hit, e.g.
// `taskset -c 0 env GOMAXPROCS=1 ...`, with TMPDIR on the filesystem to test.
func TestWorktreeRemoveStressAgainstConcurrentPushes(t *testing.T) {
	spec := os.Getenv("NM_WORKTREE_REMOVE_STRESS")
	if spec == "" {
		t.Skip("set NM_WORKTREE_REMOVE_STRESS to a duration to run the reproduction loop")
	}
	duration, err := time.ParseDuration(spec)
	if err != nil {
		t.Fatalf("NM_WORKTREE_REMOVE_STRESS: %v", err)
	}
	pushers := stressCount(t, "NM_WORKTREE_REMOVE_STRESS_PUSHERS", 4)
	removers := stressCount(t, "NM_WORKTREE_REMOVE_STRESS_REMOVERS", 1)
	add := WorktreeAdd
	if os.Getenv("NM_WORKTREE_REMOVE_STRESS_GIT_ADD") != "" {
		add = func(ctx context.Context, repoDir, wtPath, rev string) error {
			_, err := Run(ctx, repoDir, "worktree", "add", "--detach", wtPath, rev)
			return err
		}
	}
	remove := WorktreeRemove
	if os.Getenv("NM_WORKTREE_REMOVE_STRESS_GIT_REMOVE") != "" {
		remove = func(ctx context.Context, repoDir, wtPath string) error {
			_, err := Run(ctx, repoDir, "worktree", "remove", "--force", wtPath)
			return err
		}
	}

	ctx := context.Background()
	work := initTestRepo(t)
	first := run(t, work, "git", "rev-parse", "HEAD")
	run(t, work, "git", "commit", "--allow-empty", "-m", "second")
	second := run(t, work, "git", "rev-parse", "HEAD")
	bare := filepath.Join(t.TempDir(), "gate.git")
	if err := InitBare(ctx, bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	run(t, work, "git", "push", bare, "HEAD:refs/heads/main")
	for _, setting := range strings.Fields(os.Getenv("NM_WORKTREE_REMOVE_STRESS_GATE_CONFIG")) {
		key, value, _ := strings.Cut(setting, "=")
		run(t, bare, "git", "config", key, value)
	}
	wtRoot := t.TempDir()

	deadline := time.Now().Add(duration)
	var pushes, adds, removals atomic.Int64
	start := time.Now()
	now := func() int64 { return time.Since(start).Microseconds() }
	// What the removers did to each worktree and when, so a failure that
	// names a worktree can be laid beside it.
	var historyMu sync.Mutex
	history := map[string]*stressHistory{}
	mark := func(at *int64) {
		historyMu.Lock()
		*at = now()
		historyMu.Unlock()
	}
	var failureMu sync.Mutex
	var failed []stressFailure
	fail := func(f stressFailure) {
		failureMu.Lock()
		failed = append(failed, f)
		failureMu.Unlock()
	}
	var wg sync.WaitGroup

	for r := 0; r < removers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				id := fmt.Sprintf("run-%d-%d", r, i)
				wt := filepath.Join(wtRoot, id)
				h := &stressHistory{addEnd: -1, removeStart: -1, removeEnd: -1}
				historyMu.Lock()
				history[id] = h
				historyMu.Unlock()
				mark(&h.addStart)
				err := add(ctx, bare, wt, first)
				mark(&h.addEnd)
				if err != nil {
					fail(stressFailure{what: "worktree add", self: id, began: h.addStart, ended: h.addEnd, out: err.Error()})
					continue
				}
				adds.Add(1)
				time.Sleep(time.Duration(rand.Int64N(int64(100 * time.Millisecond))))
				mark(&h.removeStart)
				err = remove(ctx, bare, wt)
				mark(&h.removeEnd)
				if err != nil {
					fail(stressFailure{what: "worktree remove", self: id, began: h.removeStart, ended: h.removeEnd, out: err.Error()})
					continue
				}
				removals.Add(1)
			}
		}(r)
	}
	for p := 0; p < pushers; p++ {
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			for n := 0; time.Now().Before(deadline); n++ {
				// Alternate the pushed commit so every push updates the ref.
				sha := first
				if n%2 == 1 {
					sha = second
				}
				cmd := exec.Command("git", "push", "--force", bare, fmt.Sprintf("%s:refs/heads/pusher-%d", sha, p))
				cmd.Dir = work
				began := now()
				out, err := cmd.CombinedOutput()
				ended := now()
				pushes.Add(1)
				if err != nil {
					fail(stressFailure{what: "push", began: began, ended: ended, out: string(out)})
				}
			}
		}(p)
	}
	wg.Wait()

	kinds := map[string]int{}
	var failedPushes, prunedAdds, otherFailures int
	for _, f := range failed {
		kind, named := f.kind(history)
		kinds[f.what+": "+kind]++
		switch {
		case f.what == "push":
			failedPushes++
		case kind == stressPrunedAdd:
			prunedAdds++
		default:
			otherFailures++
		}
		t.Logf("%s %dus-%dus: %s", f.what, f.began, f.ended, lastLine(f.out))
		if h := history[named]; h != nil {
			t.Logf("    %s: %s", named, h)
		}
	}
	t.Logf("%d pushes against %d worktree adds and %d removals in %v: %d pushes failed, %d worktree adds were pruned while being created, %d other worktree adds or removals failed; by kind: %v",
		pushes.Load(), adds.Load(), removals.Load(), duration, failedPushes, prunedAdds, otherFailures, kinds)
	if len(failed) > 0 {
		t.Fatalf("%d of %d pushes failed, %d worktree adds were pruned and %d other worktree adds or removals failed while worktrees were being added and removed",
			failedPushes, pushes.Load(), prunedAdds, otherFailures)
	}
}

// stressHistory is when the stress loop's remover added and removed one
// worktree, in microseconds since the loop started; -1 is not yet.
type stressHistory struct {
	addStart, addEnd, removeStart, removeEnd int64
}

func (h *stressHistory) String() string {
	return fmt.Sprintf("add@%dus added@%dus remove@%dus removed@%dus", h.addStart, h.addEnd, h.removeStart, h.removeEnd)
}

// stressFailure is one failed git command of the stress loop: what it was,
// the worktree it was adding or removing (none for a push), when it ran, and
// its output.
type stressFailure struct {
	what, self   string
	began, ended int64
	out          string
}

const stressPrunedAdd = "could not open a file of its own new entry (pruned while being created)"

var (
	stressNamedWorktree = regexp.MustCompile(`worktrees/(run-\d+-\d+)/`)
	stressCommondirErr  = regexp.MustCompile(`/commondir: ([^\n]*)`)
)

// kind names what the failure died on, from its output and from what the
// worktree it names was going through while it ran, and returns that
// worktree's name.
func (f stressFailure) kind(history map[string]*stressHistory) (string, string) {
	m := stressNamedWorktree.FindStringSubmatch(f.out)
	if m == nil {
		return "other", ""
	}
	named := m[1]
	if f.what == "worktree add" && named == f.self && strings.Contains(f.out, "could not open") {
		return stressPrunedAdd, named
	}
	reading := "died on a worktree's entry"
	if c := stressCommondirErr.FindStringSubmatch(f.out); c != nil {
		reading = "failed to read worktrees/<id>/commondir (" + c[1] + ")"
	}
	h := history[named]
	switch {
	case h == nil:
	case overlaps(f.began, f.ended, h.addStart, h.addEnd):
		return reading + " of a worktree being added", named
	case overlaps(f.began, f.ended, h.removeStart, h.removeEnd):
		return reading + " of a worktree being removed", named
	}
	return reading + " of a worktree neither being added nor removed", named
}

// overlaps reports whether [began, ended] meets [from, to], where a from of -1
// has not begun and a to of -1 has not ended.
func overlaps(began, ended, from, to int64) bool {
	return from >= 0 && from <= ended && (to < 0 || began <= to)
}

// lastLine returns the line of a command's output that names a worktree it
// could not read, else its last non-empty line, cut short.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	for _, candidate := range lines {
		if strings.Contains(candidate, "worktrees/") {
			line = strings.TrimSpace(candidate)
			break
		}
	}
	if len(line) > 300 {
		line = line[:300]
	}
	return line
}

func stressCount(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("%s = %q, want a positive integer", name, raw)
	}
	return n
}
