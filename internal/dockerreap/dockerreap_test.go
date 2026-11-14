package dockerreap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fakeDocker is an in-memory Docker that answers the CLI calls Reap makes the
// way the real CLI does: `ps -a`, `volume ls` and `network ls` with a label
// key filter and a tab-separated format, and `rm -f -v`, `volume rm` and
// `network rm`. Removing a container with -v takes its anonymous volumes with
// it, as Docker does, so a test can assert on what is left rather than on the
// calls made.
type fakeDocker struct {
	containers map[string]fakeObject
	volumes    map[string]fakeObject
	networks   map[string]fakeObject
	// refuse names objects whose removal fails, the way Docker refuses a
	// volume another container still mounts.
	refuse map[string]bool
	calls  [][]string
}

type fakeObject struct {
	labels    map[string]string
	anonymous []string // a container's anonymous volumes
	inUse     bool     // a volume or network another container holds
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		containers: map[string]fakeObject{},
		volumes:    map[string]fakeObject{},
		networks:   map[string]fakeObject{},
		refuse:     map[string]bool{},
	}
}

func runLabel(runID string) map[string]string { return map[string]string{LabelKey: runID} }

// addContainer adds a container and the anonymous volumes its image declares.
func (f *fakeDocker) addContainer(id string, labels map[string]string, anonymous ...string) {
	f.containers[id] = fakeObject{labels: labels, anonymous: anonymous}
	for _, v := range anonymous {
		f.volumes[v] = fakeObject{labels: map[string]string{"com.docker.volume.anonymous": ""}}
	}
}

func (f *fakeDocker) run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	switch {
	case len(args) >= 2 && args[0] == "ps" && args[1] == "-a":
		return f.list(f.containers, args[2:])
	case len(args) >= 2 && args[0] == "volume" && args[1] == "ls":
		return f.list(f.volumes, args[2:])
	case len(args) >= 2 && args[0] == "network" && args[1] == "ls":
		return f.list(f.networks, args[2:])
	case len(args) >= 3 && args[0] == "rm" && args[1] == "-f" && args[2] == "-v":
		return f.remove(f.containers, args[3:], func(c fakeObject) {
			for _, v := range c.anonymous {
				delete(f.volumes, v)
			}
		})
	case len(args) >= 2 && args[0] == "volume" && args[1] == "rm":
		return f.remove(f.volumes, args[2:], nil)
	case len(args) >= 2 && args[0] == "network" && args[1] == "rm":
		return f.remove(f.networks, args[2:], nil)
	}
	return nil, fmt.Errorf("fake docker: unexpected call %q", args)
}

func (f *fakeDocker) list(objects map[string]fakeObject, args []string) ([]byte, error) {
	var key, format string
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--filter":
			k, ok := strings.CutPrefix(args[i+1], "label=")
			if !ok || strings.Contains(k, "=") {
				return nil, fmt.Errorf("fake docker: unsupported filter %q", args[i+1])
			}
			key = k
		case "--format":
			format = args[i+1]
		default:
			return nil, fmt.Errorf("fake docker: unexpected list flag %q", args[i])
		}
	}
	if key == "" || (format != listFormat(".ID") && format != listFormat(".Name")) {
		return nil, fmt.Errorf("fake docker: list without the label key filter and format: %q", args)
	}
	ids := make([]string, 0, len(objects))
	for id := range objects {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var out strings.Builder
	for _, id := range ids {
		value, ok := objects[id].labels[key]
		if !ok {
			continue
		}
		fmt.Fprintf(&out, "%s\t%s\n", id, value)
	}
	return []byte(out.String()), nil
}

func (f *fakeDocker) remove(objects map[string]fakeObject, ids []string, after func(fakeObject)) ([]byte, error) {
	var errs []error
	for _, id := range ids {
		obj, ok := objects[id]
		switch {
		case !ok:
			errs = append(errs, fmt.Errorf("Error response from daemon: No such object: %s", id))
		case f.refuse[id] || obj.inUse:
			errs = append(errs, fmt.Errorf("Error response from daemon: %s is in use", id))
		default:
			delete(objects, id)
			if after != nil {
				after(obj)
			}
		}
	}
	return nil, errors.Join(errs...)
}

func keys(m map[string]fakeObject) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestReapRemovesTheRunsContainersVolumesAndNetworks(t *testing.T) {
	docker := newFakeDocker()
	// The leak this package exists for: a mongo container whose image declares
	// /data/db and /data/configdb, started with the run's label.
	docker.addContainer("mongo-run", runLabel("RUN1"), "anon-db", "anon-configdb")
	docker.volumes["named-run"] = fakeObject{labels: runLabel("RUN1")}
	docker.networks["net-run"] = fakeObject{labels: runLabel("RUN1")}

	removed, err := Reap(context.Background(), docker.run, []string{"RUN1"})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(docker.containers) != 0 || len(docker.volumes) != 0 || len(docker.networks) != 0 {
		t.Fatalf("left behind: containers %v, volumes %v, networks %v", keys(docker.containers), keys(docker.volumes), keys(docker.networks))
	}
	want := Removed{Containers: []string{"mongo-run"}, Networks: []string{"net-run"}, Volumes: []string{"named-run"}}
	if !slices.Equal(removed.Containers, want.Containers) || !slices.Equal(removed.Networks, want.Networks) || !slices.Equal(removed.Volumes, want.Volumes) {
		t.Fatalf("removed = %+v, want %+v", removed, want)
	}
}

func TestReapKeepsWhatIsNotTheRunsOwn(t *testing.T) {
	docker := newFakeDocker()
	docker.addContainer("mine", runLabel("RUN1"), "anon-mine")
	docker.addContainer("other-run", runLabel("RUN2"), "anon-other")
	docker.addContainer("prefix-run", runLabel("RUN10"))
	docker.addContainer("unlabelled", map[string]string{"com.example": "x"}, "anon-unlabelled")
	// A label with no value is nobody's run: it must never match, however the
	// caller's list was built.
	docker.addContainer("empty-value", runLabel(""))
	docker.volumes["user-data"] = fakeObject{labels: map[string]string{}}
	docker.volumes["other-run-vol"] = fakeObject{labels: runLabel("RUN2")}
	docker.networks["other-run-net"] = fakeObject{labels: runLabel("RUN2")}

	if _, err := Reap(context.Background(), docker.run, []string{"RUN1", ""}); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if got, want := keys(docker.containers), []string{"empty-value", "other-run", "prefix-run", "unlabelled"}; !slices.Equal(got, want) {
		t.Fatalf("containers left = %v, want %v", got, want)
	}
	if got, want := keys(docker.volumes), []string{"anon-other", "anon-unlabelled", "other-run-vol", "user-data"}; !slices.Equal(got, want) {
		t.Fatalf("volumes left = %v, want %v", got, want)
	}
	if got, want := keys(docker.networks), []string{"other-run-net"}; !slices.Equal(got, want) {
		t.Fatalf("networks left = %v, want %v", got, want)
	}
}

func TestReapReapsSeveralRunsInOnePass(t *testing.T) {
	docker := newFakeDocker()
	docker.addContainer("a", runLabel("RUN1"), "anon-a")
	docker.addContainer("b", runLabel("RUN2"), "anon-b")
	docker.addContainer("c", runLabel("RUN3"))

	if _, err := Reap(context.Background(), docker.run, []string{"RUN1", "RUN2"}); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if got := keys(docker.containers); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("containers left = %v, want [c]", got)
	}
	// Three listings whatever the number of runs, so a startup pass over many
	// crashed runs costs no more Docker calls than one run's end.
	lists := 0
	for _, call := range docker.calls {
		if slices.Contains(call, "--format") {
			lists++
		}
	}
	if lists > 6 {
		t.Fatalf("Reap listed %d times for two runs, want one listing per kind before and after removal", lists)
	}
}

func TestReapWithNothingLabelledRemovesNothing(t *testing.T) {
	docker := newFakeDocker()
	docker.addContainer("unlabelled", nil, "anon")

	removed, err := Reap(context.Background(), docker.run, []string{"RUN1"})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(removed.Containers)+len(removed.Volumes)+len(removed.Networks) != 0 {
		t.Fatalf("removed = %+v, want nothing", removed)
	}
	for _, call := range docker.calls {
		if !slices.Contains(call, "--format") {
			t.Fatalf("Reap made a removal call with nothing to remove: %q", call)
		}
	}
}

func TestReapReportsWhatItCouldNotRemove(t *testing.T) {
	docker := newFakeDocker()
	docker.addContainer("mine", runLabel("RUN1"))
	docker.volumes["held"] = fakeObject{labels: runLabel("RUN1"), inUse: true}
	docker.networks["net"] = fakeObject{labels: runLabel("RUN1")}

	removed, err := Reap(context.Background(), docker.run, []string{"RUN1"})
	if err == nil {
		t.Fatal("Reap returned no error with a volume still there")
	}
	if !strings.Contains(err.Error(), "held") {
		t.Fatalf("error %q does not name the volume left behind", err)
	}
	// One refusal does not stop the rest of the run's objects going.
	if len(docker.containers) != 0 || len(docker.networks) != 0 {
		t.Fatalf("left behind: containers %v, networks %v", keys(docker.containers), keys(docker.networks))
	}
	if slices.Contains(removed.Volumes, "held") {
		t.Fatalf("removed = %+v reports the refused volume as removed", removed)
	}
}

func TestReapTreatsAnObjectGoneMeanwhileAsRemoved(t *testing.T) {
	// A container started with --rm can remove itself between the listing and
	// the removal; Docker then answers "No such container", which is not a
	// leftover.
	docker := newFakeDocker()
	docker.addContainer("racer", runLabel("RUN1"))
	run := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "rm" {
			delete(docker.containers, "racer")
		}
		return docker.run(ctx, args...)
	}
	if _, err := Reap(context.Background(), run, []string{"RUN1"}); err != nil {
		t.Fatalf("Reap: %v", err)
	}
}

func TestReapWithoutDockerIsANoOp(t *testing.T) {
	run := func(context.Context, ...string) ([]byte, error) { return nil, ErrNoDocker }
	removed, err := Reap(context.Background(), run, []string{"RUN1"})
	if err != nil {
		t.Fatalf("Reap without Docker: %v", err)
	}
	if len(removed.Containers)+len(removed.Volumes)+len(removed.Networks) != 0 {
		t.Fatalf("removed = %+v, want nothing", removed)
	}
}

func TestReapWithDockerNotAnsweringFails(t *testing.T) {
	run := func(context.Context, ...string) ([]byte, error) {
		return nil, errors.New("Cannot connect to the Docker daemon")
	}
	if _, err := Reap(context.Background(), run, []string{"RUN1"}); err == nil {
		t.Fatal("Reap returned no error when Docker could not list anything")
	}
}

func TestReapWithNoRunsCallsNothing(t *testing.T) {
	docker := newFakeDocker()
	if _, err := Reap(context.Background(), docker.run, []string{"", ""}); err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(docker.calls) != 0 {
		t.Fatalf("Reap called Docker with no run to reap: %q", docker.calls)
	}
}

func TestLabel(t *testing.T) {
	if got, want := Label("01ABC"), "no-mistakes.run=01ABC"; got != want {
		t.Fatalf("Label = %q, want %q", got, want)
	}
}
