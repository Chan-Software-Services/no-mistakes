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
	failure := "worktree add: exit status 128"
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
		{
			name: "a failed run carries its error",
			run:  &ipc.RunInfo{Status: types.RunFailed, Error: &failure},
			want: `run status=failed error="worktree add: exit status 128", no steps recorded`,
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

func TestDropPollLinesKeepsWhatTheDaemonDid(t *testing.T) {
	log := "level=INFO msg=\"run started\" run=r1\n" +
		"level=DEBUG msg=\"ipc request\" method=get_runs\n" +
		"level=DEBUG msg=\"ipc request\" method=get_run\n" +
		"level=INFO msg=\"ipc request\" method=respond\n" +
		"level=WARN msg=\"run failed\" run=r1"
	want := "level=INFO msg=\"run started\" run=r1\n" +
		"level=INFO msg=\"ipc request\" method=respond\n" +
		"level=WARN msg=\"run failed\" run=r1"
	if got := string(dropPollLines([]byte(log))); got != want {
		t.Fatalf("dropPollLines =\n%s\nwant\n%s", got, want)
	}
}

func TestDiskFullLineFindsWhatAFullDiskSays(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"git on a full filesystem": {
			text: "remote: fatal: unable to write loose object file: No space left on device        \nerror: remote unpack failed",
			want: "remote: fatal: unable to write loose object file: No space left on device",
		},
		"a user quota": {
			text: "ok\nfatal: cannot create directory at 'a': Disk quota exceeded\n",
			want: "fatal: cannot create directory at 'a': Disk quota exceeded",
		},
		"sqlite": {
			text: `level=ERROR msg="pipeline failed" error="start step ci: start step: database or disk is full (13)"`,
			want: `level=ERROR msg="pipeline failed" error="start step ci: start step: database or disk is full (13)"`,
		},
		"windows": {
			text: "write x: There is not enough space on the disk.",
			want: "write x: There is not enough space on the disk.",
		},
		"an ordinary failure": {
			text: "fatal: couldn't find remote ref refs/heads/agent-edits\nexit status 128",
			want: "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := diskFullLine(tc.text); got != tc.want {
				t.Fatalf("diskFullLine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDiskFullReportNamesTheFullDiskFromTheDaemonLog(t *testing.T) {
	home := t.TempDir()
	logs := filepath.Join(home, "logs")
	if err := os.MkdirAll(filepath.Join(logs, "run1"), 0o755); err != nil {
		t.Fatal(err)
	}
	daemonLog := filepath.Join(logs, "daemon.log")
	line := `level=ERROR msg="failed to update run error status" error="update run error with verified head: database or disk is full (13)"`
	if err := os.WriteFile(daemonLog, []byte("level=INFO msg=\"daemon ready\"\n"+line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stepLog := filepath.Join(logs, "run1", "document.log")
	if err := os.WriteFile(stepLog, []byte("git add -A: No space left on device\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Harness{NMHome: home}
	report := h.diskFullReport()
	for _, want := range []string{"ran out of space", os.TempDir(), daemonLog, line} {
		if !strings.Contains(report, want) {
			t.Errorf("report misses %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, stepLog) {
		t.Errorf("report should carry one sign, the daemon log's, got:\n%s", report)
	}
}

func TestDiskFullReportReadsAStepLogWhenTheDaemonLogIsSilent(t *testing.T) {
	home := t.TempDir()
	stepLog := filepath.Join(home, "logs", "run1", "document.log")
	if err := os.MkdirAll(filepath.Dir(stepLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stepLog, []byte("git add -A: exit status 128: fatal: unable to write loose object file: No space left on device\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Harness{NMHome: home}
	if report := h.diskFullReport(); !strings.Contains(report, stepLog) {
		t.Fatalf("report should name the step log %s, got %q", stepLog, report)
	}
}

func TestDiskFullReportKeepsTheFirstSignACommandPrinted(t *testing.T) {
	h := &Harness{NMHome: t.TempDir()}
	h.noteDiskFull("git checkout feature", "error: unable to write new index file\nfatal: sha1 file '.git/index.lock' write error. Out of diskspace: No space left on device\n")
	h.noteDiskFull("git push", "remote: fatal: Disk quota exceeded")
	h.noteDiskFull("no-mistakes status", "all good")

	report := h.diskFullReport()
	for _, want := range []string{"git checkout feature", "Out of diskspace: No space left on device"} {
		if !strings.Contains(report, want) {
			t.Errorf("report misses %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "Disk quota exceeded") {
		t.Errorf("report should keep the first sign only, got:\n%s", report)
	}
}

func TestDiskFullReportIsEmptyWithoutASign(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "logs", "daemon.log"), []byte("level=ERROR msg=\"pipeline failed\" error=\"step review failed\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Harness{NMHome: home}
	h.noteDiskFull("git fetch", "fatal: couldn't find remote ref refs/heads/x")
	if report := h.diskFullReport(); report != "" {
		t.Fatalf("report = %q, want none", report)
	}
}

func TestLaunchedDaemonPIDsReadsEachLaunchOnce(t *testing.T) {
	log := `2026-11-15T18:55:55+08:00 lifecycle command=daemon.stop force=false pid=1586662 ppid=1584906
time=2026-11-15T18:55:55.263+08:00 level=INFO msg="daemon process launched" pid=1586853 log=/tmp/x/daemon.log
time=2026-11-15T18:55:55.364+08:00 level=INFO msg="daemon ready" pid=1586853
time=2026-11-15T18:55:55.591+08:00 level=INFO msg="daemon process launched" pid=1587221 log=/tmp/x/daemon.log
time=2026-11-15T18:55:55.634+08:00 level=INFO msg="daemon process launched" pid=1587221
`
	got := launchedDaemonPIDs(log)
	if len(got) != 2 || got[0] != 1586853 || got[1] != 1587221 {
		t.Fatalf("launchedDaemonPIDs = %v, want [1586853 1587221]", got)
	}
}

func TestDaemonsOutlivingTestNamesTheLaunchedDaemonThatStillRuns(t *testing.T) {
	home := t.TempDir()
	logs := filepath.Join(home, "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "cli.log"), []byte("level=INFO msg=\"daemon process launched\" pid=101 log=x\nlevel=INFO msg=\"daemon process launched\" pid=102 log=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "daemon.log"), []byte("level=INFO msg=\"daemon process launched\" pid=103\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Harness{NMHome: home}

	// 101 has exited, 102 is gone at the third ask, 103 never goes.
	polls := map[int]int{}
	running := func(pid int) bool {
		polls[pid]++
		switch pid {
		case 102:
			return polls[pid] < 3
		case 103:
			return true
		}
		return false
	}
	got := h.daemonsOutlivingTest(running, 5, 0)
	if len(got) != 1 || got[0] != 103 {
		t.Fatalf("daemonsOutlivingTest = %v, want [103]", got)
	}
	if polls[103] != 5 {
		t.Fatalf("asked about the daemon that never exits %d times, want all 5", polls[103])
	}
}

func TestDaemonsOutlivingTestIsEmptyWhenNoDaemonWasLaunched(t *testing.T) {
	h := &Harness{NMHome: t.TempDir()}
	if got := h.daemonsOutlivingTest(func(int) bool { return true }, 5, 0); len(got) != 0 {
		t.Fatalf("daemonsOutlivingTest = %v, want none", got)
	}
}

// A wait that times out because the daemon is gone must still fail with its
// own message, so the run's progress degrades to the lookup error.
func TestRunProgressByIDReportsAnUnreachableDaemon(t *testing.T) {
	h := &Harness{t: t, NMHome: t.TempDir()}

	got := h.runProgressByID("run-1")
	if !strings.HasPrefix(got, "run not readable: dial daemon: ") {
		t.Fatalf("runProgressByID() = %q, want the dial error in place of the run's progress", got)
	}
}
