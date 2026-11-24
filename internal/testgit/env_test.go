package testgit

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// WithoutParentRepo drops each variable the installed git lists as one it
// clears before it works in another repository, keeps the config ones the
// test helpers manage, and keeps the rest of the environment as it is.
func TestWithoutParentRepoDropsTheRepositoryVariablesGitLists(t *testing.T) {
	t.Parallel()
	gitBin, err := RealGit()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(gitBin, "rev-parse", "--local-env-vars").Output()
	if err != nil {
		t.Fatalf("git rev-parse --local-env-vars: %v", err)
	}
	config := []string{"GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"}
	var env, kept []string
	for _, name := range strings.Fields(string(out)) {
		env = append(env, name+"=/parent")
		if slices.Contains(config, name) {
			kept = append(kept, name+"=/parent")
		}
	}
	env = append(env, "HOME=/home/test", "GIT_AUTHOR_NAME=test", "GIT_CONFIG_KEY_0=user.name")
	kept = append(kept, "HOME=/home/test", "GIT_AUTHOR_NAME=test", "GIT_CONFIG_KEY_0=user.name")

	if got := WithoutParentRepo(env); !slices.Equal(got, kept) {
		t.Fatalf("WithoutParentRepo kept %q, want %q", got, kept)
	}
}

// DropParentRepoEnv unsets, in the test process, a GIT_DIR that a parent
// git handed the go test, here pointed at a second temporary repository.
func TestDropParentRepoEnvUnsetsAParentGitsRepositoryInTheProcess(t *testing.T) {
	gitBin, err := RealGit()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	if out, err := exec.Command(gitBin, "init", parent).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", parent, err, out)
	}
	t.Setenv("GIT_DIR", filepath.Join(parent, ".git"))

	DropParentRepoEnv()

	if value, ok := os.LookupEnv("GIT_DIR"); ok {
		t.Fatalf("GIT_DIR = %q after DropParentRepoEnv, want it unset", value)
	}
}
