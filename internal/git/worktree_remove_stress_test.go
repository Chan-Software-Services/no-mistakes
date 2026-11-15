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
// the race WorktreeRemove's ordering closes: pushes to a bare gate repository
// while its linked worktrees are added and removed, as the daemon does around
// every run. A push must never fail, whatever the removal is doing, and
// neither must the daemon's own git commands, which walk the worktrees too.
//
// Only the removals overlap the pushes. `git worktree add` has a window of its
// own, which nothing here closes: it creates the new entry's commondir empty
// and then writes it, and a reader in between dies with "failed to read
// worktrees/<id>/commondir: Success". Left in, that window fails this loop
// whatever the removal does, so an add waits for the pushes in flight and
// holds new ones back, and adds do not overlap each other. A removal follows
// its add after a random pause of up to 100ms.
//
// One failure of an add is counted and not failed on, for the same reason:
// "could not open" a file of its own new entry for writing. The maintenance
// git runs after a push prunes worktrees, and a prune that finds the entry
// `git worktree add` has just made, before the add locked it, deletes it. With
// `maintenance.auto=false` in NM_WORKTREE_REMOVE_STRESS_GATE_CONFIG it does
// not happen.
//
// The window is a few microseconds wide per push, so the loop only means
// something at volume. It is skipped unless NM_WORKTREE_REMOVE_STRESS names how
// long to run (a Go duration, e.g. 2m). Its other settings:
//
//   - NM_WORKTREE_REMOVE_STRESS_PUSHERS: concurrent pushers (default 4).
//   - NM_WORKTREE_REMOVE_STRESS_REMOVERS: worktrees added and removed at once
//     (default 1; each live worktree is one more read per push).
//   - NM_WORKTREE_REMOVE_STRESS_GIT_REMOVE: when set, remove with
//     `git worktree remove --force`, the removal WorktreeRemove replaced, to
//     see the loop fail.
//   - NM_WORKTREE_REMOVE_STRESS_GATE_CONFIG: space-separated key=value git
//     settings for the gate repository.
//
// Starve it to make the window easier to hit, e.g.
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
	var pushes, removals, failures, otherReaders, prunedAdds atomic.Int64
	var failureMu sync.Mutex
	failureKinds := map[string]int{}
	// What the removers were doing to each worktree and when, so a failure
	// that names a worktree can be laid beside it.
	start := time.Now()
	var history sync.Map
	mark := func(id, event string) {
		prev, _ := history.Load(id)
		line, _ := prev.(string)
		history.Store(id, line+fmt.Sprintf(" %s@%dus", event, time.Since(start).Microseconds()))
	}
	var details []string
	// Held shared by a push and exclusively by a worktree add.
	var adding sync.RWMutex
	var wg sync.WaitGroup

	for r := 0; r < removers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				id := fmt.Sprintf("run-%d-%d", r, i)
				wt := filepath.Join(wtRoot, id)
				mark(id, "add")
				adding.Lock()
				err := WorktreeAdd(ctx, bare, wt, first)
				adding.Unlock()
				mark(id, "added")
				if err != nil {
					if strings.Contains(err.Error(), "could not open") && strings.Contains(err.Error(), "/worktrees/"+id+"/") {
						prunedAdds.Add(1)
					} else {
						otherReaders.Add(1)
					}
					failureMu.Lock()
					details = append(details, fmt.Sprintf("worktree add %s at %dus: %v", id, time.Since(start).Microseconds(), lastLine(err.Error())))
					failureMu.Unlock()
					continue
				}
				// Let the pushes the add held back get going, so the removal
				// lands at no particular point of a push.
				time.Sleep(time.Duration(rand.Int64N(int64(100 * time.Millisecond))))
				mark(id, "remove")
				err = remove(ctx, bare, wt)
				mark(id, "removed")
				if err != nil {
					otherReaders.Add(1)
					failureMu.Lock()
					details = append(details, fmt.Sprintf("worktree remove %s at %dus: %v", id, time.Since(start).Microseconds(), lastLine(err.Error())))
					failureMu.Unlock()
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
				adding.RLock()
				began := time.Since(start).Microseconds()
				out, err := cmd.CombinedOutput()
				adding.RUnlock()
				pushes.Add(1)
				if err != nil {
					failures.Add(1)
					kind := "other"
					switch {
					case strings.Contains(string(out), "failed to read worktrees/"):
						kind = "failed to read worktrees/<id>/commondir"
					case strings.Contains(string(out), "worktrees/"):
						kind = "died on a worktree's admin directory"
					}
					failureMu.Lock()
					failureKinds[kind]++
					details = append(details, fmt.Sprintf("push %dus-%dus: %s", began, time.Since(start).Microseconds(), lastLine(string(out))))
					failureMu.Unlock()
				}
			}
		}(p)
	}
	wg.Wait()

	named := regexp.MustCompile(`worktrees/(run-\d+-\d+)`)
	for _, detail := range details {
		t.Log(detail)
		if m := named.FindStringSubmatch(detail); m != nil {
			line, _ := history.Load(m[1])
			t.Logf("    %s:%v", m[1], line)
		}
	}
	t.Logf("%d pushes against %d worktree removals in %v: %d pushes failed %v, %d worktree adds or removals failed, %d worktree adds were pruned while being created",
		pushes.Load(), removals.Load(), duration, failures.Load(), failureKinds, otherReaders.Load(), prunedAdds.Load())
	if failures.Load() > 0 || otherReaders.Load() > 0 {
		t.Fatalf("%d of %d pushes and %d worktree adds or removals failed while worktrees were being removed",
			failures.Load(), pushes.Load(), otherReaders.Load())
	}
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
