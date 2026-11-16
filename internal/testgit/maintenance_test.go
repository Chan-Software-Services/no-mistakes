package testgit

import (
	"os"
	"os/exec"
	"path/filepath"
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
