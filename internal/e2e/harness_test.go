//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestFixtureRootFromRepoRoot(t *testing.T) {
	root, err := fixtureRootFromRepoRoot(t.TempDir())
	if err == nil {
		t.Fatalf("fixtureRootFromRepoRoot succeeded with %q, want error", root)
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}

	root, err = fixtureRootFromRepoRoot(repoRoot)
	if err != nil {
		t.Fatalf("fixtureRootFromRepoRoot: %v", err)
	}
	want := filepath.Join(repoRoot, "internal", "e2e", "fixtures")
	if root != want {
		t.Fatalf("fixture root = %q, want %q", root, want)
	}
}

func TestDaemonStartTimeoutLeavesRoomForLoginShellProbe(t *testing.T) {
	timeout, err := time.ParseDuration(e2eDaemonStartTimeout)
	if err != nil {
		t.Fatalf("parse e2eDaemonStartTimeout: %v", err)
	}
	if timeout <= 30*time.Second {
		t.Fatalf("e2eDaemonStartTimeout = %v, want more than the 30s login-shell probe budget", timeout)
	}
}

func TestCommitChangeCreatesMissingBranchFromMain(t *testing.T) {
	workDir := t.TempDir()
	h := &Harness{t: t, WorkDir: workDir}
	ctx := context.Background()
	mustGit := func(args ...string) string {
		t.Helper()
		out, err := h.runGit(ctx, workDir, args...)
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	mustGit("init", "--initial-branch=main")
	mustGit("config", "user.email", "e2e@example.com")
	mustGit("config", "user.name", "E2E Test")
	mustGit("config", "commit.gpgsign", "false")

	readme := filepath.Join(workDir, "README.md")
	if err := os.WriteFile(readme, []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	mustGit("add", "README.md")
	mustGit("commit", "-m", "initial commit")
	mainSHA := mustGit("rev-parse", "HEAD")

	mustGit("checkout", "-b", "feature/existing")
	featureOnly := filepath.Join(workDir, "feature-only.txt")
	if err := os.WriteFile(featureOnly, []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature-only.txt: %v", err)
	}
	mustGit("add", "feature-only.txt")
	mustGit("commit", "-m", "feature commit")

	h.CommitChange("feature/new", "hello.txt", "hello\n", "new branch commit")

	mergeBase := mustGit("merge-base", "feature/new", "main")
	if mergeBase != mainSHA {
		t.Fatalf("merge-base(feature/new, main) = %s, want %s", mergeBase, mainSHA)
	}
	if _, err := os.Stat(filepath.Join(workDir, "feature-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("feature-only.txt present on new branch, want branch rooted at main")
	}
	show := mustGit("show", "feature/new:hello.txt")
	if show != "hello" {
		t.Fatalf("hello.txt contents = %q, want %q", show, "hello")
	}
}

func TestDaemonStopDirFallsBackWhenWorkDirMoved(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "missing-work")
	homeDir := t.TempDir()
	h := &Harness{WorkDir: workDir, HomeDir: homeDir}

	if got := h.daemonStopDir(); got != homeDir {
		t.Fatalf("daemonStopDir() = %q, want home dir %q", got, homeDir)
	}
}

func TestWaitForRunPrefersNewestRunOnBranch(t *testing.T) {
	nmHome, err := os.MkdirTemp("/tmp", "nm-e2e-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(nmHome) })
	workDir := t.TempDir()
	p := paths.WithRoot(nmHome)
	if err := os.MkdirAll(nmHome, 0o755); err != nil {
		t.Fatalf("mkdir nm home: %v", err)
	}

	server := ipc.NewServer()
	var calls atomic.Int32
	server.Handle(ipc.MethodGetRuns, func(_ context.Context, _ json.RawMessage) (interface{}, error) {
		if calls.Add(1) == 1 {
			return ipc.GetRunsResult{Runs: []ipc.RunInfo{
				{ID: "run-new", RepoID: "ignored", Branch: "feature/e2e", Status: types.RunRunning, CreatedAt: 20, UpdatedAt: 20},
				{ID: "run-old", RepoID: "ignored", Branch: "feature/e2e", Status: types.RunCompleted, CreatedAt: 10, UpdatedAt: 10},
			}}, nil
		}
		return ipc.GetRunsResult{Runs: []ipc.RunInfo{
			{ID: "run-new", RepoID: "ignored", Branch: "feature/e2e", Status: types.RunCompleted, CreatedAt: 20, UpdatedAt: 30},
			{ID: "run-old", RepoID: "ignored", Branch: "feature/e2e", Status: types.RunCompleted, CreatedAt: 10, UpdatedAt: 10},
		}}, nil
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(p.Socket())
	}()
	t.Cleanup(func() {
		server.Close()
		if err := <-errCh; err != nil {
			t.Errorf("ipc server: %v", err)
		}
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client, err := ipc.Dial(p.Socket())
		if err == nil {
			client.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	h := &Harness{t: t, NMHome: nmHome, WorkDir: workDir}
	run := h.WaitForRun("feature/e2e", 2*time.Second)
	if run.ID != "run-new" {
		t.Fatalf("WaitForRun returned %q, want newest run", run.ID)
	}
	if run.Status != types.RunCompleted {
		t.Fatalf("WaitForRun status = %s, want %s", run.Status, types.RunCompleted)
	}
	if calls.Load() < 2 {
		t.Fatalf("GetRuns calls = %d, want at least 2 polls", calls.Load())
	}
}

// A wait that times out must say where the run stopped, so a hang is read
// from the failure line and not dug out of the dumped logs.
func TestRunProgressNamesTheStepTheRunStoppedAt(t *testing.T) {
	step := func(order int, name types.StepName, status types.StepStatus) ipc.StepResultInfo {
		return ipc.StepResultInfo{StepName: name, StepOrder: order, Status: status}
	}
	for _, tc := range []struct {
		name string
		run  *ipc.RunInfo
		want string
	}{
		{
			name: "a running step after finished ones, whatever the slice order",
			run: &ipc.RunInfo{Status: types.RunRunning, Steps: []ipc.StepResultInfo{
				step(3, types.StepDocument, types.StepStatusPending),
				step(2, types.StepTest, types.StepStatusRunning),
				step(0, types.StepRebase, types.StepStatusSkipped),
				step(1, types.StepReview, types.StepStatusCompleted),
			}},
			want: "run status=running, stopped at step test status=running",
		},
		{
			name: "a parked step",
			run: &ipc.RunInfo{Status: types.RunRunning, Steps: []ipc.StepResultInfo{
				step(0, types.StepReview, types.StepStatusAwaitingApproval),
				step(1, types.StepTest, types.StepStatusPending),
			}},
			want: "run status=running, stopped at step review status=awaiting_approval",
		},
		{
			name: "every step finished",
			run: &ipc.RunInfo{Status: types.RunCompleted, Steps: []ipc.StepResultInfo{
				step(0, types.StepReview, types.StepStatusCompleted),
				step(1, types.StepTest, types.StepStatusSkipped),
			}},
			want: "run status=completed, every step finished",
		},
		{
			name: "no steps recorded yet",
			run:  &ipc.RunInfo{Status: types.RunPending},
			want: "run status=pending, no steps recorded",
		},
		{name: "no run", run: nil, want: "no run seen"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runProgress(tc.run); got != tc.want {
				t.Fatalf("runProgress = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemoveBuiltBinariesDeletesTheBuildDir(t *testing.T) {
	saved := builtDir
	t.Cleanup(func() { builtDir = saved })

	dir := filepath.Join(t.TempDir(), "nm-e2e-bin-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "no-mistakes"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	builtDir = dir
	removeBuiltBinaries()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("build dir still exists after removeBuiltBinaries: %v", err)
	}
	if builtDir != "" {
		t.Fatalf("builtDir = %q after removal, want empty", builtDir)
	}

	// Nothing was built: nothing to remove, and nothing else is touched.
	removeBuiltBinaries()
}
