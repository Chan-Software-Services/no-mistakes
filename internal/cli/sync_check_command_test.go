package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/kunchenguid/no-mistakes/internal/branchsync"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// The live synchronization check as each reader runs it: an agent surface
// names the axi form, whose output is TOON, and a person's surface names the
// human form.
const (
	agentCheckSyncCommand = "no-mistakes axi sync --check"
	humanCheckSyncCommand = "no-mistakes sync --check"
)

// leaveSubmittedHead amends the operator's commit, so the local head has left
// the submitted head while the pipeline-pushed head is not in the local
// object store: the cached state is ambiguous_context at
// blocked_relation_unknown, whose next action is check_sync.
func leaveSubmittedHead(t *testing.T, f cliSyncFixture) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.local, "file.txt"), []byte("feature, rewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cliGit(t, f.local, "commit", "-a", "--amend", "-m", "feature, rewritten")
}

// assertAgentNextAction checks that an agent surface's branch_sync names the
// next action code with the axi check command, and never the human one.
func assertAgentNextAction(t *testing.T, surface, out, code string) {
	t.Helper()
	for _, want := range []string{"next_action:", "code: " + code, "command: " + agentCheckSyncCommand} {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing %q:\n%s", surface, want, out)
		}
	}
	if strings.Contains(out, humanCheckSyncCommand) {
		t.Errorf("%s names the human command %q, whose output is not TOON:\n%s", surface, humanCheckSyncCommand, out)
	}
}

func TestAgentSurfacesNameAxiCheckAtRelationUnknown(t *testing.T) {
	f := newCLISyncFixture(t)
	leaveSubmittedHead(t, f)

	out, err := executeCmd("axi", "status")
	if err != nil {
		t.Fatalf("axi status: %v\n%s", err, out)
	}
	assertAgentNextAction(t, "axi status", out, "check_sync")

	out, err = executeCmd("axi")
	if err != nil {
		t.Fatalf("axi home: %v\n%s", err, out)
	}
	assertAgentNextAction(t, "axi home", out, "check_sync")

	var drive bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&drive)
	cmd.SetContext(context.Background())
	run := &ipc.RunInfo{ID: f.runID, Branch: "feature/sync", Status: types.RunCompleted, HeadSHA: f.pushed}
	if err := renderDriveResult(cmd, run, false); err != nil {
		t.Fatalf("drive result: %v\n%s", err, drive.String())
	}
	assertAgentNextAction(t, "drive result", drive.String(), "check_sync")
}

func TestHumanSurfacesKeepHumanCheckAtRelationUnknown(t *testing.T) {
	f := newCLISyncFixture(t)
	leaveSubmittedHead(t, f)

	out, err := executeCmd("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	want := "check it live with `" + humanCheckSyncCommand + "`"
	if !strings.Contains(out, want) {
		t.Errorf("human status missing %q:\n%s", want, out)
	}
	if strings.Contains(out, "axi sync") {
		t.Errorf("human status names an axi command:\n%s", out)
	}
}

func TestAxiAbortNamesAxiCheckAtRelationUnknown(t *testing.T) {
	f := newCLISyncFixtureAtHome(t, makeSocketSafeTempDir(t))
	leaveSubmittedHead(t, f)
	serveFakeAbortDaemon(t, fakeAbortDaemon{
		active: &ipc.RunInfo{ID: f.runID, Branch: "feature/sync", Status: types.RunRunning},
		getRun: func(context.Context, int) (*ipc.RunInfo, error) {
			return &ipc.RunInfo{ID: f.runID, Branch: "feature/sync", Status: types.RunCancelled}, nil
		},
	})

	out, err := executeCmd("axi", "abort")
	if err != nil {
		t.Fatalf("axi abort: %v\n%s", err, out)
	}
	if !strings.Contains(out, "aborted: true") {
		t.Fatalf("fixture: abort was not confirmed:\n%s", out)
	}
	assertAgentNextAction(t, "axi abort", out, "check_sync")
	if want := "Run `" + agentCheckSyncCommand + "`"; !strings.Contains(out, want) {
		t.Errorf("axi abort help missing %q:\n%s", want, out)
	}
}

func TestAxiSyncCheckNamesAxiCheckWhenOffline(t *testing.T) {
	f := newCLISyncFixture(t)
	// The configured push target cannot be reached: its ls-remote fails, so a
	// live refresh reports blocked_offline with a retry next action.
	if err := os.Rename(f.remote, f.remote+".unreachable"); err != nil {
		t.Fatal(err)
	}

	out, _ := executeCmd("axi", "sync", "--check")
	if !strings.Contains(out, "state: "+branchsync.StateOffline) || !strings.Contains(out, "safety: blocked_offline") {
		t.Fatalf("fixture: axi sync --check did not report blocked_offline:\n%s", out)
	}
	assertAgentNextAction(t, "axi sync --check", out, "retry")
	if want := "Run `" + agentCheckSyncCommand + "`"; !strings.Contains(out, want) {
		t.Errorf("axi sync --check help missing %q:\n%s", want, out)
	}

	out, _ = executeCmd("axi", "sync")
	if !strings.Contains(out, "safety: blocked_offline") {
		t.Fatalf("fixture: axi sync did not report blocked_offline:\n%s", out)
	}
	assertAgentNextAction(t, "axi sync", out, "retry")
	if want := "Run `" + agentCheckSyncCommand + "`"; !strings.Contains(out, want) {
		t.Errorf("axi sync help missing %q:\n%s", want, out)
	}

	// At blocked_offline the human output names no command at all, so the
	// human side holds only as never naming the axi form, once the check has
	// reached the offline state.
	out, err := executeCmd("sync", "--check")
	if err == nil || !strings.Contains(out, "could not refresh the configured push target") {
		t.Fatalf("fixture: human sync --check did not report the offline refusal (err %v):\n%s", err, out)
	}
	if strings.Contains(out, "axi sync") {
		t.Errorf("human sync --check names an axi command:\n%s", out)
	}
}

func TestAxiSyncCheckNamesAxiCheckWhenFetchFails(t *testing.T) {
	f := newCLISyncFixture(t)
	// The configured push target answers its ls-remote, but the fetch into the
	// run's private sync ref fails: a ref nested under that name leaves no
	// room for it, so git refuses the update and the refresh reports
	// blocked_offline at the fetch.
	cliGit(t, f.local, "update-ref", "refs/no-mistakes/sync/"+f.runID+"/blocker", f.old)

	out, _ := executeCmd("axi", "sync", "--check")
	if !strings.Contains(out, "state: "+branchsync.StateOffline) || !strings.Contains(out, "safety: blocked_offline") ||
		!strings.Contains(out, "could not fetch the configured push target") {
		t.Fatalf("fixture: axi sync --check did not report the fetch failure:\n%s", out)
	}
	assertAgentNextAction(t, "axi sync --check", out, "retry")
	if want := "Run `" + agentCheckSyncCommand + "`"; !strings.Contains(out, want) {
		t.Errorf("axi sync --check help missing %q:\n%s", want, out)
	}

	out, _ = executeCmd("axi", "sync")
	if !strings.Contains(out, "could not fetch the configured push target") {
		t.Fatalf("fixture: axi sync did not report the fetch failure:\n%s", out)
	}
	assertAgentNextAction(t, "axi sync", out, "retry")
	if want := "Run `" + agentCheckSyncCommand + "`"; !strings.Contains(out, want) {
		t.Errorf("axi sync help missing %q:\n%s", want, out)
	}

	out, err := executeCmd("sync", "--check")
	if err == nil || !strings.Contains(out, "could not fetch the configured push target") {
		t.Fatalf("fixture: human sync --check did not report the fetch failure (err %v):\n%s", err, out)
	}
	if strings.Contains(out, "axi sync") {
		t.Errorf("human sync --check names an axi command:\n%s", out)
	}
}

func TestAxiSyncCheckNamesAxiCheckWhenTheRemoteChangesDuringTheRefresh(t *testing.T) {
	f := newCLISyncFixture(t)
	// The remote branch moves between the refresh's ls-remote and its fetch:
	// a reference-transaction hook rewrites the run's private sync ref to
	// another commit as soon as the fetch commits it, so the fetched head no
	// longer equals the one ls-remote saw.
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\n" +
		"[ \"$1\" = committed ] || exit 0\n" +
		"[ -z \"$NM_TEST_REF_MOVED\" ] || exit 0\n" +
		"while read -r old new ref; do\n" +
		"  case \"$ref\" in refs/no-mistakes/sync/*)\n" +
		"    [ \"$new\" = 0000000000000000000000000000000000000000 ] || NM_TEST_REF_MOVED=1 git update-ref \"$ref\" " + f.old + " ;;\n" +
		"  esac\n" +
		"done\n"
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	cliGit(t, f.local, "config", "core.hooksPath", hooks)

	out, _ := executeCmd("axi", "sync", "--check")
	if !strings.Contains(out, "safety: blocked_remote_changed_during_refresh") {
		t.Fatalf("fixture: axi sync --check did not report the remote changing during the refresh:\n%s", out)
	}
	assertAgentNextAction(t, "axi sync --check", out, "retry")
	if want := "Run `" + agentCheckSyncCommand + "`"; !strings.Contains(out, want) {
		t.Errorf("axi sync --check help missing %q:\n%s", want, out)
	}
	if strings.Contains(out, "command: no-mistakes axi sync\n") {
		t.Errorf("axi sync --check still offers the cached sync command:\n%s", out)
	}

	out, _ = executeCmd("axi", "sync")
	if !strings.Contains(out, "safety: blocked_remote_changed_during_refresh") {
		t.Fatalf("fixture: axi sync did not report the remote changing during the refresh:\n%s", out)
	}
	assertAgentNextAction(t, "axi sync", out, "retry")
	if want := "Run `" + agentCheckSyncCommand + "`"; !strings.Contains(out, want) {
		t.Errorf("axi sync help missing %q:\n%s", want, out)
	}
	if got := cliGit(t, f.local, "rev-parse", "HEAD"); got != f.old {
		t.Fatalf("axi sync moved HEAD to %s on a remote that changed during the refresh", got)
	}
}
