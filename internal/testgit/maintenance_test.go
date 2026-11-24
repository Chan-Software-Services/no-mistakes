package testgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A plain commit starts git's auto-maintenance in the background, which is
// what races a test helper that copies or walks the repository next; the
// NoAutoMaintenance options keep the same commit from starting it.
func TestNoAutoMaintenanceKeepsACommitFromStartingMaintenance(t *testing.T) {
	t.Parallel()
	gitBin, err := RealGit()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	emptyConfig := emptyGitConfig(t)
	git := func(trace string, args ...string) {
		t.Helper()
		runGitIn(t, gitBin, dir, parallelGitEnv(emptyConfig, trace), args...)
	}
	traceDir := t.TempDir()
	git(filepath.Join(traceDir, "init"), "init")

	plain := filepath.Join(traceDir, "plain")
	git(plain, "commit", "--allow-empty", "-m", "plain")
	if starts := autoMaintenanceStarts(t, plain); len(starts) == 0 {
		t.Fatalf("a plain commit started no auto-maintenance; trace:\n%s", readFile(t, plain))
	}

	quiet := filepath.Join(traceDir, "quiet")
	git(quiet, append(NoAutoMaintenance(), "commit", "--allow-empty", "-m", "quiet")...)
	if starts := autoMaintenanceStarts(t, quiet); len(starts) != 0 {
		t.Fatalf("a commit with NoAutoMaintenance started auto-maintenance: %q", starts)
	}
}

// A go test started from a hook under `git -c maintenance.auto=false commit`
// inherits that option through GIT_CONFIG_PARAMETERS. The parallel test above
// cannot set it, so this one does, and runs the same plain commit in the
// environment that test gives git: the commit must still start
// auto-maintenance, or the parallel test fails under such a parent git.
func TestParallelGitEnvShutsOutAParentGitsOptions(t *testing.T) {
	gitBin, err := RealGit()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_PARAMETERS", "'maintenance.auto'='false'")
	dir := t.TempDir()
	emptyConfig := emptyGitConfig(t)
	traceDir := t.TempDir()
	runGitIn(t, gitBin, dir, parallelGitEnv(emptyConfig, filepath.Join(traceDir, "init")), "init")

	plain := filepath.Join(traceDir, "plain")
	runGitIn(t, gitBin, dir, parallelGitEnv(emptyConfig, plain), "commit", "--allow-empty", "-m", "plain")
	if starts := autoMaintenanceStarts(t, plain); len(starts) == 0 {
		t.Fatalf("a plain commit started no auto-maintenance; trace:\n%s", readFile(t, plain))
	}
}

// A go test started from a hook under a git commit or push can inherit
// GIT_DIR from its parent git. The parallel test above cannot set it, so this
// one does, pointed at a second repository standing in for the contributor's
// checkout, and runs the same init and plain commit in the environment that
// test gives git: both must act on the test's own directory.
func TestParallelGitEnvShutsOutAParentGitsRepository(t *testing.T) {
	gitBin, err := RealGit()
	if err != nil {
		t.Fatal(err)
	}
	emptyConfig := emptyGitConfig(t)
	traceDir := t.TempDir()
	parent := t.TempDir()
	runGitIn(t, gitBin, parent, parallelGitEnv(emptyConfig, filepath.Join(traceDir, "parent-init")), "init")
	t.Setenv("GIT_DIR", filepath.Join(parent, ".git"))

	dir := t.TempDir()
	runGitIn(t, gitBin, dir, parallelGitEnv(emptyConfig, filepath.Join(traceDir, "init")), "init")
	runGitIn(t, gitBin, dir, parallelGitEnv(emptyConfig, filepath.Join(traceDir, "plain")), "commit", "--allow-empty", "-m", "plain")

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("git init made no repository in the test's directory: %v", err)
	}
	if subjects := logSubjects(t, gitBin, filepath.Join(parent, ".git")); subjects != "" {
		t.Errorf("the plain commit landed in the repository GIT_DIR names: its log is %q", subjects)
	}
}

// logSubjects returns the subjects of every commit the repository at gitDir
// holds, "" when it has none. It names the repository on the command line, so
// it reads that one whatever GIT_DIR the test process has.
func logSubjects(t *testing.T, gitBin, gitDir string) string {
	t.Helper()
	out, err := exec.Command(gitBin, "--git-dir="+gitDir, "log", "--all", "--format=%s").Output()
	if err != nil {
		t.Fatalf("git log in %s: %v", gitDir, err)
	}
	return strings.TrimSpace(string(out))
}

// parallelGitEnv returns the environment a parallel test, which cannot call
// IgnoreUserGitConfig since that uses t.Setenv, gives a git it starts, so a
// maintenance.auto=false from outside the test's repository cannot make a
// plain commit look safe:
//   - GIT_CONFIG_GLOBAL points at emptyConfig, an empty file, so neither
//     ~/.gitconfig nor $XDG_CONFIG_HOME/git/config is read;
//   - GIT_CONFIG_NOSYSTEM keeps the system config out;
//   - GIT_CONFIG_PARAMETERS is left out of what the test process has: a git
//     run as `git -c <key>=<value> <cmd>` hands its options to every child
//     through it, so a go test started from a hook under such a commit or
//     push would otherwise pass them on;
//   - GIT_CONFIG_COUNT is 0, so no GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n
//     pairs an agent harness set apply.
//
// It builds on Environ, so a GIT_DIR, GIT_WORK_TREE or GIT_INDEX_FILE such a
// parent git hands its hook cannot point the git at the contributor's
// checkout instead of the test's directory. It also writes the git's trace to trace and gives it an identity to commit
// with.
func parallelGitEnv(emptyConfig, trace string) []string {
	var env []string
	for _, kv := range Environ() {
		if !strings.HasPrefix(kv, "GIT_CONFIG_PARAMETERS=") {
			env = append(env, kv)
		}
	}
	return append(env,
		"GIT_CONFIG_GLOBAL="+emptyConfig,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_COUNT=0",
		"GIT_TRACE2="+trace,
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
	)
}

func emptyGitConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runGitIn(t *testing.T, gitBin, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command(gitBin, args...)
	cmd.Dir = dir
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestAutoMaintenanceStartsReadsChildStartLines(t *testing.T) {
	t.Parallel()
	trace := filepath.Join(t.TempDir(), "trace")
	lines := "" +
		"04:24:40.468341 git.c:507  cmd_name commit (commit)\n" +
		"04:24:40.468341 run-command.c:740  child_start[0] git maintenance run --auto --quiet --detach\n" +
		"04:24:40.470952 git.c:507  cmd_name maintenance (commit/maintenance)\n" +
		"04:24:41.000000 run-command.c:740  child_start[1] git gc --auto --quiet\n" +
		"04:24:42.000000 run-command.c:740  child_start[2] git maintenance run --task=gc\n" +
		"04:24:43.000000 run-command.c:740  child_start[3] git rev-parse HEAD\n"
	if err := os.WriteFile(trace, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"git maintenance run --auto --quiet --detach", "git gc --auto --quiet"}
	got := autoMaintenanceStarts(t, trace)
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("AutoMaintenanceStarts = %q, want %q", got, want)
	}
}

func TestAutoMaintenanceStartsRefusesATraceGitNeverWrote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := AutoMaintenanceStarts(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("AutoMaintenanceStarts accepted a missing trace")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := AutoMaintenanceStarts(empty); err == nil {
		t.Fatal("AutoMaintenanceStarts accepted an empty trace")
	}
}

func autoMaintenanceStarts(t *testing.T, trace string) []string {
	t.Helper()
	starts, err := AutoMaintenanceStarts(trace)
	if err != nil {
		t.Fatal(err)
	}
	return starts
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TurnOffAutoMaintenance reaches a git the test process starts without being
// handed any option, which is how the code under test runs git, and keeps any
// configuration pairs already in the environment.
func TestTurnOffAutoMaintenanceKeepsThePlainCommitOfTheProcessFromStartingMaintenance(t *testing.T) {
	// A contributor's own maintenance.auto=false must not reach the commit,
	// or the check below would pass without TurnOffAutoMaintenance.
	userConfig := filepath.Join(t.TempDir(), "user-gitconfig")
	if err := os.WriteFile(userConfig, []byte("[maintenance]\n\tauto = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", userConfig)
	gitBin := isolatedGit(t)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "kept")
	dir := t.TempDir()
	run(t, gitBin, dir, "init")
	if out, err := exec.Command(gitBin, "-C", dir, "config", "--get", "maintenance.auto").Output(); err == nil {
		t.Fatalf("maintenance.auto = %q from the contributor's global config, want it shut out", strings.TrimSpace(string(out)))
	}
	trace := TraceGit(t)
	TurnOffAutoMaintenance(t)

	run(t, gitBin, dir, "commit", "--allow-empty", "-m", "quiet")
	RequireNoAutoMaintenance(t, trace)
	if got := run(t, gitBin, dir, "config", "user.name"); got != "kept" {
		t.Fatalf("user.name = %q, want the pair already in the environment kept", got)
	}
}

// A local push's receive-pack does not see GIT_CONFIG_COUNT, so the
// environment alone leaves it starting auto-maintenance in the repository
// pushed into; that repository's own config stops it.
func TestTurnOffRepoAutoMaintenanceKeepsALocalPushFromStartingMaintenance(t *testing.T) {
	gitBin := isolatedGit(t)
	TurnOffAutoMaintenance(t)
	work := t.TempDir()
	run(t, gitBin, work, "init")
	run(t, gitBin, work, "commit", "--allow-empty", "-m", "base")
	plainGate := filepath.Join(t.TempDir(), "plain.git")
	quietGate := filepath.Join(t.TempDir(), "quiet.git")
	run(t, gitBin, work, "init", "--bare", plainGate)
	run(t, gitBin, work, "init", "--bare", quietGate)
	TurnOffRepoAutoMaintenance(t, quietGate)

	plain := TraceGit(t)
	run(t, gitBin, work, "push", plainGate, "HEAD:refs/heads/main")
	if starts := autoMaintenanceStarts(t, plain); len(starts) == 0 {
		t.Fatalf("a local push into a gate without the config started no auto-maintenance; trace:\n%s", readFile(t, plain))
	}

	quiet := TraceGit(t)
	run(t, gitBin, work, "push", quietGate, "HEAD:refs/heads/main")
	RequireNoAutoMaintenance(t, quiet)
}

// A go test started from a hook under `git -c maintenance.auto=false commit`
// inherits that option through GIT_CONFIG_PARAMETERS, and an agent harness can
// hand the same keys through GIT_CONFIG_COUNT pairs. IgnoreUserGitConfig must
// shut out both, or a check that calls it passes with the test's own switch
// removed.
func TestIgnoreUserGitConfigShutsOutConfigFromTheEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  [][2]string
	}{
		{"a parent git's options", [][2]string{
			{"GIT_CONFIG_PARAMETERS", "'maintenance.auto'='false' 'gc.auto'='0'"},
		}},
		{"configuration pairs", [][2]string{
			{"GIT_CONFIG_COUNT", "2"},
			{"GIT_CONFIG_KEY_0", "maintenance.auto"}, {"GIT_CONFIG_VALUE_0", "false"},
			{"GIT_CONFIG_KEY_1", "gc.auto"}, {"GIT_CONFIG_VALUE_1", "0"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gitBin, err := RealGit()
			if err != nil {
				t.Fatal(err)
			}
			for _, kv := range tc.env {
				t.Setenv(kv[0], kv[1])
			}
			IgnoreUserGitConfig(t)
			setGitIdentity(t)
			dir := t.TempDir()
			run(t, gitBin, dir, "init")
			if out, err := exec.Command(gitBin, "-C", dir, "config", "--get", "maintenance.auto").Output(); err == nil {
				t.Errorf("maintenance.auto = %q from %s, want it shut out", strings.TrimSpace(string(out)), tc.name)
			}

			trace := TraceGit(t)
			run(t, gitBin, dir, "commit", "--allow-empty", "-m", "plain")
			if starts := autoMaintenanceStarts(t, trace); len(starts) == 0 {
				t.Fatalf("a plain commit started no auto-maintenance with %s set; trace:\n%s", tc.name, readFile(t, trace))
			}
		})
	}
}

// isolatedGit returns the real git with the contributor's own config and
// any config in the environment out of the way, so a maintenance.auto=false
// from either cannot make a check pass.
func isolatedGit(t *testing.T) string {
	t.Helper()
	gitBin, err := RealGit()
	if err != nil {
		t.Fatal(err)
	}
	IgnoreUserGitConfig(t)
	setGitIdentity(t)
	return gitBin
}

func setGitIdentity(t *testing.T) {
	t.Helper()
	for _, kv := range [][2]string{
		{"GIT_AUTHOR_NAME", "test"}, {"GIT_AUTHOR_EMAIL", "test@test.com"},
		{"GIT_COMMITTER_NAME", "test"}, {"GIT_COMMITTER_EMAIL", "test@test.com"},
	} {
		t.Setenv(kv[0], kv[1])
	}
}

func run(t *testing.T, gitBin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitBin, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}
