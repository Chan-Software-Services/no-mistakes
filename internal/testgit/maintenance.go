package testgit

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// autoMaintenanceOff is the git configuration that keeps a command from
// starting git's auto-maintenance: maintenance.auto=false stops the start, and
// gc.auto=0 keeps a git too old to have it from running `gc --auto` instead.
var autoMaintenanceOff = [][2]string{{"maintenance.auto", "false"}, {"gc.auto", "0"}}

// NoAutoMaintenance returns the git options that keep a command from
// starting git's auto-maintenance. A test helper that builds a repository and
// then copies or walks it runs the git calls that can start it with them: a
// commit, merge or fetch otherwise starts a detached
// `git maintenance run --auto`, which can take .git/objects/maintenance.lock
// after the command has returned, so the copy lists the lock and then finds
// it gone. A test whose code under test runs the git calls uses
// TurnOffAutoMaintenance instead.
func NoAutoMaintenance() []string {
	var args []string
	for _, kv := range autoMaintenanceOff {
		args = append(args, "-c", kv[0]+"="+kv[1])
	}
	return args
}

// TurnOffAutoMaintenance gives every git the test process starts from here
// on, the code under test's own calls included, the configuration
// NoAutoMaintenance passes as options, through GIT_CONFIG_COUNT and its
// GIT_CONFIG_KEY_n and GIT_CONFIG_VALUE_n pairs, after any pairs already set.
// It is for a test that walks a repository in which the code under test ran
// a commit, merge or fetch, which a test cannot hand options to.
//
// Git drops GIT_CONFIG_COUNT for the receive-pack a local push starts, so a
// repository a test pushes into needs the same keys in its own config (see
// TurnOffRepoAutoMaintenance).
func TurnOffAutoMaintenance(t testing.TB) {
	t.Helper()
	count := 0
	if value := os.Getenv("GIT_CONFIG_COUNT"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			t.Fatalf("GIT_CONFIG_COUNT = %q, want a count", value)
		}
		count = n
	}
	for _, kv := range autoMaintenanceOff {
		t.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", count), kv[0])
		t.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", count), kv[1])
		count++
	}
	t.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(count))
}

// TurnOffRepoAutoMaintenance writes the configuration NoAutoMaintenance
// passes as options into the repository at gitDir (a bare repository or a
// .git directory). It is for a repository a test pushes into over a local
// path: the receive-pack that push starts reads its own repository's config
// but not GIT_CONFIG_COUNT, and starts auto-maintenance after the push has
// taken its refs.
func TurnOffRepoAutoMaintenance(t testing.TB, gitDir string) {
	t.Helper()
	for _, kv := range autoMaintenanceOff {
		cmd := exec.Command("git", "--git-dir="+gitDir, "config", kv[0], kv[1])
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git config %s %s in %s: %v: %s", kv[0], kv[1], gitDir, err, out)
		}
	}
}

// TraceGit points GIT_TRACE2 at a new file for the rest of the test and
// returns its path, for RequireNoAutoMaintenance to read.
func TraceGit(t testing.TB) string {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "git-trace2")
	t.Setenv("GIT_TRACE2", trace)
	return trace
}

// RequireNoAutoMaintenance fails the test if any git that wrote to trace
// started auto-maintenance (see AutoMaintenanceStarts).
func RequireNoAutoMaintenance(t testing.TB, trace string) {
	t.Helper()
	starts, err := AutoMaintenanceStarts(trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) > 0 {
		t.Fatalf("git started auto-maintenance %d times: %q", len(starts), starts)
	}
}

// AutoMaintenanceStarts reads a GIT_TRACE2 trace file and returns the
// command line of each child that started git's auto-maintenance
// (`git maintenance run --auto` or `git gc --auto`). A missing or empty trace
// is an error, since then git never traced and nothing was checked. A check
// built on it assumes git 2.29 or later: an older git has no
// maintenance.auto, so a commit there still starts `git gc --auto`, which
// gc.auto=0 makes do nothing but which this still counts as a start.
func AutoMaintenanceStarts(trace string) ([]string, error) {
	data, err := os.ReadFile(trace)
	if err != nil {
		return nil, fmt.Errorf("read git trace: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("git trace %s is empty", trace)
	}
	var starts []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		_, after, ok := strings.Cut(scanner.Text(), "child_start[")
		if !ok {
			continue
		}
		_, command, ok := strings.Cut(after, "] ")
		if !ok {
			continue
		}
		fields := strings.Fields(command)
		if len(fields) < 3 || !hasArg(fields, "--auto") {
			continue
		}
		if (fields[1] == "maintenance" && fields[2] == "run") || fields[1] == "gc" {
			starts = append(starts, command)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read git trace: %w", err)
	}
	return starts, nil
}

func hasArg(fields []string, arg string) bool {
	for _, field := range fields {
		if field == arg {
			return true
		}
	}
	return false
}
