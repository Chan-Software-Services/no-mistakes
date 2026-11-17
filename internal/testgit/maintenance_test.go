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
	emptyConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(emptyConfig, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(trace string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		// A contributor's own maintenance.auto=false must not make the
		// plain commit look safe.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL="+emptyConfig,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_COUNT=0",
			"GIT_TRACE2="+trace,
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
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

// isolatedGit returns the real git with the contributor's own global and
// system config out of the way, so their maintenance.auto=false cannot make
// a check pass.
func isolatedGit(t *testing.T) string {
	t.Helper()
	gitBin, err := RealGit()
	if err != nil {
		t.Fatal(err)
	}
	IgnoreUserGitConfig(t)
	t.Setenv("GIT_CONFIG_COUNT", "0")
	for _, kv := range [][2]string{
		{"GIT_AUTHOR_NAME", "test"}, {"GIT_AUTHOR_EMAIL", "test@test.com"},
		{"GIT_COMMITTER_NAME", "test"}, {"GIT_COMMITTER_EMAIL", "test@test.com"},
	} {
		t.Setenv(kv[0], kv[1])
	}
	return gitBin
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
