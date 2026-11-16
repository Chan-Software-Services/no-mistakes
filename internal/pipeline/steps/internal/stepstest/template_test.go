package stepstest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/testgit"
)

// The git template SetupGitRepo copies from is made once per test binary in
// the temp dir. The cleanup Init returns is what each TestMain runs at the
// end, so it must take the template with it, or every passing run leaves a
// git-template-* directory behind.
func TestInitCleanupRemovesTheGitRepoTemplate(t *testing.T) {
	cleanup, err := Init()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cleanup() })

	SetupGitRepo(t)
	template, _, _ := GitRepoTemplate(t)
	if _, err := os.Stat(template); err != nil {
		t.Fatalf("template repo after SetupGitRepo: %v", err)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(template); !os.IsNotExist(err) {
		t.Fatalf("template repo %s after cleanup: stat error = %v, want not exist", template, err)
	}

	// A later SetupGitRepo in the same process makes the template again.
	dir, _, headSHA := SetupGitRepo(t)
	if got := GitCmd(t, dir, "rev-parse", "HEAD"); got != headSHA {
		t.Fatalf("HEAD after the template was made again = %q, want %q", got, headSHA)
	}
	if err := cleanup(); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

// The template's commits must start no auto-maintenance: a detached
// `git maintenance run --auto` takes .git/objects/maintenance.lock after the
// commit has returned, and a SetupGitRepo copying the template then can list
// the lock and lstat it after it is gone.
func TestGitRepoTemplateStartsNoAutoMaintenance(t *testing.T) {
	if err := removeGitRepoTemplate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = removeGitRepoTemplate() })
	// A contributor's own maintenance.auto=false must not make the template
	// look safe.
	emptyConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(emptyConfig, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", emptyConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE2", trace)

	GitRepoTemplate(t)

	starts, err := testgit.AutoMaintenanceStarts(trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(starts) != 0 {
		t.Fatalf("making the template started auto-maintenance: %q", starts)
	}
}
