package stepstest

import (
	"os"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/testgit"
)

func TestMain(m *testing.M) {
	// A parent git's repository variables are dropped first, since these tests
	// build repositories with git: under a hook that inherited them, those
	// gits would act on the contributor's checkout
	// (testgit.DropParentRepoEnv).
	testgit.DropParentRepoEnv()
	// Agent harnesses inject git config (e.g. safe.bareRepository=explicit)
	// via GIT_CONFIG_COUNT/KEY_n/VALUE_n; tests that need it re-set it with
	// t.Setenv (issue #362).
	os.Unsetenv("GIT_CONFIG_COUNT")
	os.Exit(m.Run())
}
