package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// every run. A push must never fail, whatever the removal is doing.
//
// The window is a few microseconds wide per push, so the loop only means
// something at volume. It is skipped unless NM_WORKTREE_REMOVE_STRESS names how
// long to run (a Go duration, e.g. 5m). NM_WORKTREE_REMOVE_STRESS_PUSHERS sets
// the number of concurrent pushers (default 4) and
// NM_WORKTREE_REMOVE_STRESS_REMOVERS the number of worktrees added and removed
// at once (default 1; each live worktree is one more read per push). Starve it
// to make the window easier to hit, e.g. `taskset -c 0 env GOMAXPROCS=1 ...`,
// with TMPDIR on the filesystem to test.
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
	wtRoot := t.TempDir()

	deadline := time.Now().Add(duration)
	var pushes, removals, failures, otherReaders atomic.Int64
	var failureMu sync.Mutex
	failureKinds := map[string]int{}
	var wg sync.WaitGroup

	for r := 0; r < removers; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; time.Now().Before(deadline); i++ {
				wt := filepath.Join(wtRoot, fmt.Sprintf("run-%d-%d", r, i))
				if err := WorktreeAdd(ctx, bare, wt, first); err != nil {
					// The daemon's own git commands walk the worktrees too.
					if !strings.Contains(err.Error(), "failed to read") {
						t.Errorf("worktree add %d-%d: %v", r, i, err)
						return
					}
					otherReaders.Add(1)
					continue
				}
				if err := WorktreeRemove(ctx, bare, wt); err != nil {
					t.Errorf("worktree remove %d-%d: %v", r, i, err)
					return
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
				out, err := cmd.CombinedOutput()
				pushes.Add(1)
				if err != nil {
					failures.Add(1)
					kind := "other: " + strings.TrimSpace(string(out))
					if strings.Contains(string(out), "failed to read worktrees/") {
						kind = "failed to read worktrees/<id>/commondir"
					}
					failureMu.Lock()
					failureKinds[kind]++
					failureMu.Unlock()
				}
			}
		}(p)
	}
	wg.Wait()

	t.Logf("%d pushes against %d worktree removals in %v: %d pushes failed %v, %d worktree adds died reading a worktree",
		pushes.Load(), removals.Load(), duration, failures.Load(), failureKinds, otherReaders.Load())
	if failures.Load() > 0 || otherReaders.Load() > 0 {
		t.Fatalf("%d of %d pushes and %d worktree adds failed while worktrees were being removed",
			failures.Load(), pushes.Load(), otherReaders.Load())
	}
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
