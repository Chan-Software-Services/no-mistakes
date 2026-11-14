package dockerreap

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestReapLiveMongoLeavesNoVolume drives the real Docker the way the Test
// agent leaked: a detached mongo:7 with no --rm, whose image declares
// /data/db and /data/configdb, so Docker gives it two anonymous volumes. It
// runs only with NM_LIVE_DOCKER=1 and mongo:7 already pulled, since it starts a
// container on the machine's Docker.
func TestReapLiveMongoLeavesNoVolume(t *testing.T) {
	if os.Getenv("NM_LIVE_DOCKER") != "1" {
		t.Skip("set NM_LIVE_DOCKER=1 to run against the real Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	docker := func(args ...string) string {
		t.Helper()
		out, err := DockerCLI(ctx, args...)
		if err != nil {
			t.Fatalf("%v", err)
		}
		return strings.TrimSpace(string(out))
	}
	if _, err := DockerCLI(ctx, "image", "inspect", "mongo:7"); err != nil {
		t.Skipf("mongo:7 is not pulled: %v", err)
	}
	runID := "live-test-" + time.Now().UTC().Format("20060102T150405.000000000")
	other := runID + "-other"

	container := docker("run", "-d", "--label", Label(runID), "mongo:7")
	t.Cleanup(func() { _, _ = DockerCLI(context.Background(), "rm", "-f", "-v", container) })
	var anonymous []string
	for _, line := range strings.Split(docker("inspect", "-f", `{{range .Mounts}}{{.Name}}{{"\n"}}{{end}}`, container), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			anonymous = append(anonymous, line)
		}
	}
	if len(anonymous) != 2 {
		t.Fatalf("mongo:7 mounted %v, want its two anonymous volumes", anonymous)
	}
	named := docker("volume", "create", "--label", Label(runID))
	network := docker("network", "create", "--label", Label(runID), runID)
	kept := docker("volume", "create", "--label", Label(other))
	t.Cleanup(func() { _, _ = DockerCLI(context.Background(), "volume", "rm", kept) })

	removed, err := Reap(ctx, DockerCLI, []string{runID})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(removed.Containers) != 1 || len(removed.Networks) != 1 || len(removed.Volumes) != 1 {
		t.Fatalf("removed = %+v, want the container, the network and the named volume", removed)
	}
	for _, obj := range append([]string{container, named, network}, anonymous...) {
		if _, err := DockerCLI(ctx, "inspect", obj); err == nil {
			t.Errorf("%s is still there after Reap", obj)
		}
	}
	if _, err := DockerCLI(ctx, "volume", "inspect", kept); err != nil {
		t.Errorf("another run's volume %s was removed: %v", kept, err)
	}
}
