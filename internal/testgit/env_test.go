package testgit

import (
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Environ drops each variable git itself clears before it works in another
// repository, except the config ones the test helpers manage, and keeps the
// rest of the environment as it is.
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

// TestMain calls DropParentRepoEnv so that no git the package's tests start,
// through the code under test included, inherits a parent git's repository.
func TestDropParentRepoEnvUnsetsAParentGitsRepositoryInTheProcess(t *testing.T) {
	parent := t.TempDir()
	for _, name := range parentRepoVars {
		t.Setenv(name, parent)
	}
	t.Setenv("GIT_CONFIG_COUNT", "0")

	DropParentRepoEnv()

	for _, name := range parentRepoVars {
		if value, ok := os.LookupEnv(name); ok {
			t.Errorf("%s = %q after DropParentRepoEnv, want it unset", name, value)
		}
	}
	if got := os.Getenv("GIT_CONFIG_COUNT"); got != "0" {
		t.Errorf("GIT_CONFIG_COUNT = %q after DropParentRepoEnv, want it kept", got)
	}
}
