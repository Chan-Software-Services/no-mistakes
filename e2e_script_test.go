package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeGoForE2EScript is a `go` that records each invocation's arguments, one
// line each, and answers `go test -list` with FAKE_GO_TESTS the way the real
// one does (names, then the package's ok line). A `go test -run` exits
// FAKE_GO_RUN_EXIT and a go test of the steps packages FAKE_GO_STEPS_EXIT.
const fakeGoForE2EScript = `#!/bin/sh
printf '%s\n' "$*" >>"$FAKE_GO_LOG"
case " $* " in
*" -list "*)
	for name in $FAKE_GO_TESTS; do printf '%s\n' "$name"; done
	printf 'ok  \tgithub.com/kunchenguid/no-mistakes/internal/e2e\t0.01s\n'
	exit 0
	;;
*" -run "*) exit "${FAKE_GO_RUN_EXIT:-0}" ;;
*" ./internal/pipeline/steps/... "*) exit "${FAKE_GO_STEPS_EXIT:-0}" ;;
esac
exit 0
`

// e2eScriptTests is the fake package's test list, out of order on purpose.
var e2eScriptTests = []string{
	"TestUserJourneyGrok", "TestAlpha", "TestUserJourneyClaude", "TestZulu",
	"TestMike", "TestUserJourneyCodex", "TestBravo",
}

// runE2EScript runs scripts/e2e.sh with the fake go first on PATH and returns
// its exit code, its output, and the `go test` invocations it made, the
// inventory reaper's `go run` left out.
func runE2EScript(t *testing.T, env map[string]string, args ...string) (int, string, []string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/e2e.sh is a bash script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGoForE2EScript), 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "go.log")

	cmd := exec.Command("bash", append([]string{filepath.Join("scripts", "e2e.sh")}, args...)...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NM_E2E_") && !strings.HasPrefix(kv, "PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_GO_LOG="+logPath,
		"FAKE_GO_TESTS="+strings.Join(e2eScriptTests, " "),
		// A caller-owned inventory, so the script makes none under /tmp.
		"NM_E2E_DAEMON_INVENTORY="+t.TempDir(),
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run scripts/e2e.sh: %v", err)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var tests []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(line, "test ") {
			tests = append(tests, line)
		}
	}
	return code, string(out), tests
}

// runPattern returns the test names a `go test` line's `-run '^(A|B)$'` selects.
func runPattern(t *testing.T, line string) []string {
	t.Helper()
	fields := strings.Fields(line)
	i := slices.Index(fields, "-run")
	if i < 0 || i+1 >= len(fields) {
		t.Fatalf("go test line has no -run: %q", line)
	}
	pattern := fields[i+1]
	if !strings.HasPrefix(pattern, "^(") || !strings.HasSuffix(pattern, ")$") {
		t.Fatalf("-run %q is not an anchored list of names", pattern)
	}
	return strings.Split(strings.TrimSuffix(strings.TrimPrefix(pattern, "^("), ")$"), "|")
}

// With no arguments the script runs the whole suite in one `go test` run, as local
// `make e2e` and the project's local checks do, under a cap half again over
// CI's slowest whole-package run.
func TestE2EScriptRunsTheWholeSuiteByDefault(t *testing.T) {
	t.Parallel()

	code, out, tests := runE2EScript(t, nil)
	if code != 0 {
		t.Fatalf("scripts/e2e.sh exited %d:\n%s", code, out)
	}
	want := []string{"test -tags=e2e -count=1 -timeout 720s ./internal/e2e/... ./internal/pipeline/steps/..."}
	if !slices.Equal(tests, want) {
		t.Fatalf("go test invocations = %q, want %q", tests, want)
	}
}

// --shard i/n deals the e2e package's tests out round-robin in name order, so
// every test runs in exactly one shard, and the steps packages run once, in
// shard 1.
func TestE2EScriptShardsDealEveryTestOnce(t *testing.T) {
	t.Parallel()

	const shards = 3
	seen := map[string]int{}
	var firstShard []string
	for i := 1; i <= shards; i++ {
		shard := fmt.Sprintf("%d/%d", i, shards)
		code, out, tests := runE2EScript(t, nil, "--shard", shard)
		if code != 0 {
			t.Fatalf("shard %s exited %d:\n%s", shard, code, out)
		}
		if len(tests) == 0 || !strings.Contains(tests[0], " -list ") {
			t.Fatalf("shard %s did not list the package's tests first: %q", shard, tests)
		}
		var e2eRuns, stepsRuns []string
		for _, line := range tests[1:] {
			if !strings.HasPrefix(line, "test -tags=e2e -count=1 -timeout 480s ") {
				t.Errorf("shard %s ran %q, want -tags=e2e -count=1 -timeout 480s", shard, line)
			}
			switch {
			case strings.HasSuffix(line, " ./internal/e2e/..."):
				e2eRuns = append(e2eRuns, line)
			case strings.HasSuffix(line, " ./internal/pipeline/steps/..."):
				stepsRuns = append(stepsRuns, line)
				if strings.Contains(line, " -run ") {
					t.Errorf("shard %s filtered the steps packages: %q", shard, line)
				}
			default:
				t.Errorf("shard %s ran an unexpected go test: %q", shard, line)
			}
		}
		if len(e2eRuns) != 1 {
			t.Fatalf("shard %s e2e runs = %q, want one", shard, e2eRuns)
		}
		wantSteps := 0
		if i == 1 {
			wantSteps = 1
		}
		if len(stepsRuns) != wantSteps {
			t.Errorf("shard %s steps runs = %q, want %d", shard, stepsRuns, wantSteps)
		}
		names := runPattern(t, e2eRuns[0])
		if i == 1 {
			firstShard = names
		}
		if !slices.IsSorted(names) {
			t.Errorf("shard %s names %q are not in name order", shard, names)
		}
		for _, name := range names {
			seen[name]++
		}
	}
	for _, name := range e2eScriptTests {
		if seen[name] != 1 {
			t.Errorf("%s ran in %d shards, want exactly 1", name, seen[name])
		}
	}
	if len(seen) != len(e2eScriptTests) {
		t.Errorf("shards ran %v, want exactly %v", seen, e2eScriptTests)
	}

	// Round-robin in name order: the three UserJourney tests sort together
	// and so land in three different shards.
	if !slices.Equal(firstShard, []string{"TestAlpha", "TestUserJourneyClaude", "TestZulu"}) {
		t.Errorf("shard 1/3 = %q, want every third name from the first", firstShard)
	}
}

// A failing e2e share still lets shard 1 run the steps packages, so one run
// reports both, and the shard exits non-zero.
func TestE2EScriptShardFailsWhenItsTestsFail(t *testing.T) {
	t.Parallel()

	code, out, tests := runE2EScript(t, map[string]string{"FAKE_GO_RUN_EXIT": "1"}, "--shard", "1/2")
	if code == 0 {
		t.Fatalf("shard exited 0 though its e2e tests failed:\n%s", out)
	}
	if len(tests) != 3 || !strings.HasSuffix(tests[2], " ./internal/pipeline/steps/...") {
		t.Fatalf("go test invocations = %q, want the list, the e2e share, then the steps packages", tests)
	}
}

// A failure in the steps packages alone fails shard 1 too.
func TestE2EScriptShardFailsWhenTheStepsPackagesFail(t *testing.T) {
	t.Parallel()

	code, out, _ := runE2EScript(t, map[string]string{"FAKE_GO_STEPS_EXIT": "3"}, "--shard", "1/2")
	if code != 3 {
		t.Fatalf("shard exited %d, want the steps packages' 3:\n%s", code, out)
	}
	if code, out, _ := runE2EScript(t, map[string]string{"FAKE_GO_STEPS_EXIT": "3"}, "--shard", "2/2"); code != 0 {
		t.Fatalf("shard 2/2, which runs no steps packages, exited %d:\n%s", code, out)
	}
}

func TestE2EScriptRefusesAMalformedShard(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"--shard"},
		{"--shard", "0/3"},
		{"--shard", "4/3"},
		{"--shard", "1/0"},
		{"--shard", "a/b"},
		{"--shard", "1/3", "-v"},
	} {
		code, out, tests := runE2EScript(t, nil, args...)
		if code != 2 {
			t.Errorf("scripts/e2e.sh %q exited %d, want 2:\n%s", args, code, out)
		}
		if !strings.Contains(out, "usage:") {
			t.Errorf("scripts/e2e.sh %q printed no usage:\n%s", args, out)
		}
		if len(tests) != 0 {
			t.Errorf("scripts/e2e.sh %q ran go test %q before refusing", args, tests)
		}
	}
}
