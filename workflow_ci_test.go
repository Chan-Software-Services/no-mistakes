package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ciWorkflow is a minimal typed view of .github/workflows/ci.yml, capturing only
// what the Linux-only CI contract pins.
type ciWorkflow struct {
	On          map[string]any           `yaml:"on"`
	Permissions any                      `yaml:"permissions"`
	Jobs        map[string]ciWorkflowJob `yaml:"jobs"`
}

type ciWorkflowJob struct {
	Name        string         `yaml:"name"`
	Needs       any            `yaml:"needs"`
	If          string         `yaml:"if"`
	Permissions any            `yaml:"permissions"`
	RunsOn      any            `yaml:"runs-on"`
	Strategy    map[string]any `yaml:"strategy"`
	Steps       []wfStep       `yaml:"steps"`
}

// e2eShardJob is the one job allowed a matrix: it fans the e2e suite out over
// shards of one Linux runner each, never over platforms.
const e2eShardJob = "e2e-shard"

// e2eShards returns the shard job's matrix values, failing unless the matrix
// is exactly `shard: [1, 2, ..., n]`.
func e2eShards(t *testing.T, job ciWorkflowJob) []int {
	t.Helper()
	matrix, ok := job.Strategy["matrix"].(map[string]any)
	if !ok || len(matrix) != 1 {
		t.Fatalf("%s matrix = %v, want exactly one key, shard", e2eShardJob, job.Strategy["matrix"])
	}
	raw, ok := matrix["shard"].([]any)
	if !ok || len(raw) == 0 {
		t.Fatalf("%s matrix = %v, want shard: [1, ..., n]", e2eShardJob, matrix)
	}
	shards := make([]int, len(raw))
	for i, v := range raw {
		n, ok := v.(int)
		if !ok || n != i+1 {
			t.Fatalf("%s matrix shard = %v, want 1 to n in order", e2eShardJob, raw)
		}
		shards[i] = n
	}
	return shards
}

func loadCIWorkflow(t *testing.T) ciWorkflow {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read CI workflow: %v", err)
	}
	var wf ciWorkflow
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	return wf
}

// The CI workflow runs on Linux only: exactly the check, test (ubuntu-latest)
// and e2e checks, plus the e2e shards the e2e check rolls up, each on a single
// ubuntu-latest runner. The only matrix is the shard job's, over shard alone,
// so none can fan a job back out to macOS or Windows.
func TestCIWorkflowRunsOnLinuxOnly(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflow(t)
	var checks []string
	for id, job := range wf.Jobs {
		name := job.Name
		if name == "" {
			name = id
		}
		if id == e2eShardJob {
			for _, shard := range e2eShards(t, job) {
				shards := len(e2eShards(t, job))
				checks = append(checks, strings.NewReplacer(
					"${{ matrix.shard }}", fmt.Sprint(shard),
					"${{ strategy.job-total }}", fmt.Sprint(shards),
				).Replace(name))
			}
		} else {
			checks = append(checks, name)
			if job.Strategy != nil {
				t.Errorf("job %q has a strategy %v; only %s fans out, over shards", id, job.Strategy, e2eShardJob)
			}
		}
		if runsOn, ok := job.RunsOn.(string); !ok || runsOn != "ubuntu-latest" {
			t.Errorf("job %q runs-on = %v, want ubuntu-latest", id, job.RunsOn)
		}
	}
	slices.Sort(checks)
	want := []string{"check", "e2e", "e2e shard 1/3", "e2e shard 2/3", "e2e shard 3/3", "test (ubuntu-latest)"}
	if !slices.Equal(checks, want) {
		t.Fatalf("CI checks = %v, want %v", checks, want)
	}
}

// Each e2e shard runs its share through scripts/e2e.sh with the matrix's own
// length as the count (strategy.job-total, so the count is written once), every shard reports even when another fails, and the e2e check runs
// after them all, whatever they concluded, and passes only when every one
// passed: a skipped roll-up, or one that read a single shard, would let a red
// shard through.
func TestCIWorkflowE2EShardsRollUpIntoTheE2ECheck(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflow(t)
	shardJob, ok := wf.Jobs[e2eShardJob]
	if !ok {
		t.Fatalf("CI workflow has no %s job", e2eShardJob)
	}
	e2eShards(t, shardJob)
	if want := "e2e shard ${{ matrix.shard }}/${{ strategy.job-total }}"; shardJob.Name != want {
		t.Errorf("%s name = %q, want %q", e2eShardJob, shardJob.Name, want)
	}
	if ff, ok := shardJob.Strategy["fail-fast"].(bool); !ok || ff {
		t.Errorf("%s fail-fast = %v, want false so every shard reports", e2eShardJob, shardJob.Strategy["fail-fast"])
	}
	var runs []string
	for _, step := range shardJob.Steps {
		if run := strings.TrimSpace(step.Run); run != "" {
			runs = append(runs, run)
		}
	}
	if want := []string{"bash scripts/e2e.sh --shard ${{ matrix.shard }}/${{ strategy.job-total }}"}; !slices.Equal(runs, want) {
		t.Errorf("%s commands = %q, want %q", e2eShardJob, runs, want)
	}

	rollup, ok := wf.Jobs["e2e"]
	if !ok {
		t.Fatal("CI workflow has no e2e job")
	}
	if rollup.Needs != e2eShardJob {
		t.Errorf("e2e needs = %v, want %s", rollup.Needs, e2eShardJob)
	}
	if rollup.If != "always()" {
		t.Errorf("e2e if = %q, want always() so a failed or cancelled shard still fails the check", rollup.If)
	}
	if len(rollup.Steps) != 1 || rollup.Steps[0].Env["SHARDS"] != "${{ needs.e2e-shard.result }}" || strings.TrimSpace(rollup.Steps[0].Run) != `test "$SHARDS" = success` {
		t.Errorf("e2e steps = %+v, want one step testing needs.e2e-shard.result is success", rollup.Steps)
	}
}

// CI can be started by hand, beside its pull_request trigger.
func TestCIWorkflowCanBeStartedByHand(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflow(t)
	if _, ok := wf.On["workflow_dispatch"]; !ok {
		t.Fatalf("CI workflow triggers = %v, want workflow_dispatch so it can be started by hand", wf.On)
	}
	if _, ok := wf.On["pull_request"]; !ok {
		t.Fatalf("CI workflow triggers = %v, want pull_request kept", wf.On)
	}
}

// The test job runs the whole race-enabled suite unconditionally, then builds
// the binary.
func TestCIWorkflowTestJobRunsTheRaceSuiteAndBuilds(t *testing.T) {
	t.Parallel()

	job, ok := loadCIWorkflow(t).Jobs["test"]
	if !ok {
		t.Fatal("CI workflow has no test job")
	}
	var runs []string
	for _, step := range job.Steps {
		if step.If != "" {
			t.Errorf("test job step %q is conditional (%q); every step runs on the one Linux runner", step.Name, step.If)
		}
		if run := strings.TrimSpace(step.Run); run != "" {
			runs = append(runs, run)
		}
	}
	want := []string{"go test -race ./...", "go build ./cmd/no-mistakes"}
	if !slices.Equal(runs, want) {
		t.Fatalf("test job commands = %q, want %q", runs, want)
	}
}

// Every CI run gets a read-only token: the workflow grants contents: read and
// nothing else, and no job sets its own permissions block, since no job writes
// to GitHub.
func TestCIWorkflowTokenIsReadOnly(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflow(t)
	perms, ok := wf.Permissions.(map[string]any)
	if !ok || len(perms) != 1 || perms["contents"] != "read" {
		t.Fatalf("CI workflow permissions = %v, want exactly contents: read", wf.Permissions)
	}
	for id, job := range wf.Jobs {
		if job.Permissions != nil {
			t.Errorf("job %q sets permissions %v; no CI job writes to GitHub, so no job sets its own block beside the workflow's read-only token", id, job.Permissions)
		}
	}
}
