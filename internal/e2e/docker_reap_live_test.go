//go:build e2e

package e2e

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/dockerreap"
)

// TestGateRunLeavesNoLabelledDatabaseBehind drives a real gate run whose Test
// step starts a MongoDB the way the Test agent did, detached and never removed,
// but carrying the run's label as the Test prompts now ask. The run ends with
// the container still running, so only the daemon's run-end reap can remove it
// and the two anonymous volumes mongo:7 declares (/data/db, /data/configdb).
// It runs only with NM_LIVE_DOCKER=1 and mongo:7 already pulled, since it
// starts a container on the machine's Docker.
func TestGateRunLeavesNoLabelledDatabaseBehind(t *testing.T) {
	if os.Getenv("NM_LIVE_DOCKER") != "1" {
		t.Skip("set NM_LIVE_DOCKER=1 to run against the real Docker")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker is not on PATH")
	}
	inspect := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, docker, args...).Run()
	}
	if err := inspect("image", "inspect", "mongo:7"); err != nil {
		t.Skipf("mongo:7 is not pulled: %v", err)
	}

	h := NewHarness(t, SetupOpts{Agent: "claude"})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	out := t.TempDir()
	// The run's worktree is <worktrees>/<repo>/<run id>, so the command reads
	// the run's ID from its own working directory.
	script := `#!/bin/sh
set -e
run_id=$(basename "$PWD")
id=$(` + docker + ` run -d --label ` + dockerreap.LabelKey + `="$run_id" mongo:7)
printf '%s\n' "$id" > ` + filepath.Join(out, "container") + `
` + docker + ` inspect -f '{{range .Mounts}}{{.Name}}{{"\n"}}{{end}}' "$id" > ` + filepath.Join(out, "mounts") + `
`
	command := filepath.Join(h.BinDir, "nm-start-mongo")
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	const branch = "feature/docker-reap"
	h.CommitChange(branch, ".no-mistakes.yaml", "allow_repo_commands: true\ncommands:\n  test: nm-start-mongo\n  lint: 'exit 0'\n", "configure a test command that starts a database")
	h.CommitChange(branch, "feature.txt", "value\n", "add feature")
	h.PushToGate(branch)
	run := waitForCompletedRun(t, h, branch)

	read := func(name string) []string {
		data, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("the test command did not record its %s: %v", name, err)
		}
		return strings.Fields(string(data))
	}
	containers := read("container")
	t.Cleanup(func() { _ = inspect(append([]string{"rm", "-f", "-v"}, containers...)...) })
	volumes := read("mounts")
	if len(containers) != 1 || len(volumes) != 2 {
		t.Fatalf("the test command started %v with volumes %v, want one container and its two anonymous volumes", containers, volumes)
	}
	t.Logf("run %s's test command started container %s with anonymous volumes %v", run.ID, containers[0], volumes)
	// The run's status turns terminal before its cleanup runs, so wait for the
	// container, bounded by the reap's own timeout and some margin.
	deadline := time.Now().Add(dockerreap.DefaultTimeout + 20*time.Second)
	for inspect("container", "inspect", containers[0]) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("run %s ended and its container %s is still there", run.ID, containers[0])
		}
		time.Sleep(300 * time.Millisecond)
	}
	for _, volume := range volumes {
		if err := inspect("volume", "inspect", volume); err == nil {
			t.Errorf("run %s ended and its container's anonymous volume %s is still there", run.ID, volume)
		}
	}
	t.Logf("after run %s ended: container %s and volumes %v are gone", run.ID, containers[0], volumes)
}
