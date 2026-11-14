// Package dockerreap removes the Docker containers, networks and volumes a
// pipeline run's agents started, once that run has ended.
//
// Why it is needed. The Test step asks its agent to stand the product up
// against disposable services it builds itself, and a database is the common
// one. An agent that improvises `docker run -d mongo:7` and later
// `docker rm -f` leaves the image's declared volumes (/data/db and
// /data/configdb for mongo) behind as anonymous volumes: `docker rm` keeps a
// container's anonymous volumes unless it is given -v, and nothing says whose
// they were. Each gate run left another pair.
//
// The association. A Docker object outlives every process of the run, so the
// cwd match internal/procreap uses cannot name it. The run's label can: the
// Test prompts tell the agent to put Label(runID) on every container, network
// and volume it starts, and Reap removes every object whose LabelKey value is
// one of the ended runs' IDs. An object without the label is never touched,
// whatever it looks like, because nothing proves it is the run's: an operator's
// own database and a sibling run's are indistinguishable from it by shape.
//
// What it removes. Containers with -v, so their anonymous volumes go with
// them; then labelled networks, which a container may have held; then labelled
// volumes, which only an unremoved container can still hold. What is still
// there afterwards is reported, so a refusal is visible in the daemon log
// rather than silently left on the operator's disk.
package dockerreap

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/winproc"
)

// LabelKey is the Docker label that names the pipeline run an object belongs
// to. Its value is the run ID.
const LabelKey = "no-mistakes.run"

// Label returns the label, as `docker run --label` takes it, that marks an
// object as the given run's.
func Label(runID string) string { return LabelKey + "=" + runID }

// ErrNoDocker is what a Runner returns when there is no Docker CLI to run.
// Reap treats it as nothing to reap: a machine without Docker has no
// container a run could have left.
var ErrNoDocker = errors.New("docker is not on PATH")

// Runner runs one Docker CLI call and returns its standard output.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// Removed lists what Reap removed, by container ID, network ID and volume
// name.
type Removed struct {
	Containers []string
	Networks   []string
	Volumes    []string
}

// Empty reports whether nothing was removed.
func (r Removed) Empty() bool {
	return len(r.Containers) == 0 && len(r.Networks) == 0 && len(r.Volumes) == 0
}

// listFormat prints one object per line: its identity, which is a volume's
// name (docker volume ls has no {{.ID}}) and every other kind's ID, and the
// value of its LabelKey label.
func listFormat(identity string) string {
	return `{{` + identity + `}}` + "\t" + `{{.Label "` + LabelKey + `"}}`
}

type kind struct {
	name     string
	list     []string
	identity string
	remove   []string
}

// kinds is the removal order: a container first, since it holds networks and
// volumes, and removing it with -v takes its anonymous volumes too.
var kinds = []kind{
	{name: "container", list: []string{"ps", "-a"}, identity: ".ID", remove: []string{"rm", "-f", "-v"}},
	{name: "network", list: []string{"network", "ls"}, identity: ".ID", remove: []string{"network", "rm"}},
	{name: "volume", list: []string{"volume", "ls"}, identity: ".Name", remove: []string{"volume", "rm"}},
}

// Reap removes every container, network and volume labelled as one of
// runIDs' and reports what it removed. An empty run ID is ignored, so a label
// with no value never matches. Docker being absent is not an error; Docker not
// answering, and anything still there after removal, is.
//
// Each kind is listed once for all the runs, so reaping many runs costs the
// same number of listings as reaping one.
func Reap(ctx context.Context, run Runner, runIDs []string) (Removed, error) {
	wanted := make(map[string]bool, len(runIDs))
	for _, id := range runIDs {
		if id != "" {
			wanted[id] = true
		}
	}
	var removed Removed
	if len(wanted) == 0 {
		return removed, nil
	}
	var errs []error
	for _, k := range kinds {
		ids, err := listLabelled(ctx, run, k, wanted)
		if errors.Is(err, ErrNoDocker) {
			return Removed{}, nil
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if len(ids) == 0 {
			continue
		}
		// The removal's own error is not the verdict: an object that went
		// meanwhile (a --rm container exiting) fails it harmlessly. What is
		// still listed afterwards is.
		_, removeErr := run(ctx, append(slices.Clone(k.remove), ids...)...)
		left, err := listLabelled(ctx, run, k, wanted)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, id := range ids {
			if !slices.Contains(left, id) {
				switch k.name {
				case "container":
					removed.Containers = append(removed.Containers, id)
				case "network":
					removed.Networks = append(removed.Networks, id)
				case "volume":
					removed.Volumes = append(removed.Volumes, id)
				}
			}
		}
		if len(left) > 0 {
			err := fmt.Errorf("%s %s still there after removal", k.name, strings.Join(left, ", "))
			if removeErr != nil {
				err = fmt.Errorf("%w: %v", err, removeErr)
			}
			errs = append(errs, err)
		}
	}
	return removed, errors.Join(errs...)
}

// listLabelled returns the objects of one kind labelled as one of the wanted
// runs'. It filters on the key alone and matches values here, so one listing
// serves every run, and a value is compared whole: run RUN1 never takes
// RUN10's objects.
func listLabelled(ctx context.Context, run Runner, k kind, wanted map[string]bool) ([]string, error) {
	args := append(slices.Clone(k.list), "--filter", "label="+LabelKey, "--format", listFormat(k.identity))
	out, err := run(ctx, args...)
	if err != nil {
		if errors.Is(err, ErrNoDocker) {
			return nil, err
		}
		return nil, fmt.Errorf("list %ss labelled %s: %w", k.name, LabelKey, err)
	}
	var ids []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		id, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "\t")
		if ok && id != "" && wanted[value] {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// DockerCLI runs the docker binary on PATH, or returns ErrNoDocker when there
// is none. A failed call's error carries its standard error, which is where
// Docker says why.
func DockerCLI(ctx context.Context, args ...string) ([]byte, error) {
	path, err := exec.LookPath("docker")
	if err != nil {
		return nil, ErrNoDocker
	}
	cmd := exec.CommandContext(ctx, path, args...)
	// The daemon runs console-less on Windows, where an unhardened console
	// child flashes a window.
	winproc.Harden(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return out, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, msg)
		}
		return out, fmt.Errorf("docker %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// DefaultTimeout bounds one ReapAndLog pass, so a Docker that does not answer
// cannot hold a run's cleanup, or the daemon's startup, whose readiness budget
// it shares with the rest of recovery, for long. A pass that answers takes well
// under a second: three listings, and removals only of what they found.
const DefaultTimeout = 10 * time.Second

// ReapAndLog reaps the given runs' Docker objects with the docker CLI, bounded
// by DefaultTimeout, and logs what it removed and what it could not. It is
// best effort by design: a run's result must never fail over its cleanup.
// reason says which cleanup route called it.
func ReapAndLog(runIDs []string, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	removed, err := Reap(ctx, DockerCLI, runIDs)
	if !removed.Empty() {
		slog.Info("removed run docker objects", "reason", reason, "run_ids", runIDs,
			"containers", removed.Containers, "networks", removed.Networks, "volumes", removed.Volumes)
	}
	if err != nil {
		slog.Warn("failed to remove run docker objects", "reason", reason, "run_ids", runIDs, "error", err)
	}
}
