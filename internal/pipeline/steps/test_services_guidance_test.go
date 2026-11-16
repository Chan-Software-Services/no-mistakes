package steps

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/dockerreap"
)

// A Test agent that needs a database improvised `docker run -d mongo:7` and
// later `docker rm -f`, and every such run left mongo's /data/db and
// /data/configdb behind as anonymous volumes nothing could attribute. The
// emitted prompts of both Test turns are the interface that stops it: they
// send the agent to the repository's own supported command first, and
// otherwise give it the exact label the daemon's end-of-run reap
// (internal/dockerreap) removes by, with the run's own ID in it.
func TestTestStep_PromptsGiveTheRunsDockerLabelAndCleanup(t *testing.T) {
	t.Parallel()
	for _, fixing := range []bool{false, true} {
		dir, baseSHA, headSHA := setupGitRepo(t)
		gitCmd(t, dir, "checkout", "--detach", headSHA)
		ag := &mockAgent{
			name: "test",
			runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
				return &agent.Result{Output: json.RawMessage(`{"summary":"fix tests","findings":[],"tested":["go test ./..."],"testing_summary":"re-verified the behaviour","artifacts":[],"scenarios":[{"name":"the behaviour works for a user","result":"pass","live":true,"evidence":"the repaired behaviour passed its focused test","reason":""}],"verdict":"go"}`)}, nil
			},
		}
		sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
		sctx.Fixing = fixing
		if _, err := (&TestStep{}).Execute(sctx); err != nil {
			t.Fatal(err)
		}
		turns := 1
		if fixing {
			turns = 2
		}
		if len(ag.calls) != turns {
			t.Fatalf("fixing=%v: %d agent turns, want %d", fixing, len(ag.calls), turns)
		}
		label := dockerreap.Label(sctx.Run.ID)
		for i, call := range ag.calls {
			prompt := strings.Join(strings.Fields(call.Prompt), " ")
			for _, want := range []string{
				"use the repository's own supported command for it",
				"--label " + label,
				"docker run --rm",
				"--tmpfs <path>",
				"`docker image inspect -f '{{json .Config.Volumes}}' <image>`",
				"never an anonymous volume",
				"`docker rm -f -v <container>`",
				"no-mistakes removes every container, network, and volume labelled " + label + " when this run ends",
				"an unlabelled one stays on the operator's machine",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("fixing=%v turn %d: prompt missing %q:\n%s", fixing, i, want, call.Prompt)
				}
			}
		}
	}
}
