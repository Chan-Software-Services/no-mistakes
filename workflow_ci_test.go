package main

import (
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
	On   map[string]any           `yaml:"on"`
	Jobs map[string]ciWorkflowJob `yaml:"jobs"`
}

type ciWorkflowJob struct {
	Name     string         `yaml:"name"`
	RunsOn   any            `yaml:"runs-on"`
	Strategy map[string]any `yaml:"strategy"`
	Steps    []wfStep       `yaml:"steps"`
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
// and e2e checks, each on a single ubuntu-latest runner with no matrix that
// could fan a job back out to macOS or Windows.
func TestCIWorkflowRunsOnLinuxOnly(t *testing.T) {
	t.Parallel()

	wf := loadCIWorkflow(t)
	var checks []string
	for id, job := range wf.Jobs {
		name := job.Name
		if name == "" {
			name = id
		}
		checks = append(checks, name)
		if job.Strategy != nil {
			t.Errorf("job %q has a strategy %v; CI jobs run on one Linux runner, never a matrix", id, job.Strategy)
		}
		if runsOn, ok := job.RunsOn.(string); !ok || runsOn != "ubuntu-latest" {
			t.Errorf("job %q runs-on = %v, want ubuntu-latest", id, job.RunsOn)
		}
	}
	slices.Sort(checks)
	want := []string{"check", "e2e", "test (ubuntu-latest)"}
	if !slices.Equal(checks, want) {
		t.Fatalf("CI checks = %v, want %v", checks, want)
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
