package citest

import (
	"fmt"
	"os"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/pipeline/steps/internal/stepstest"
	"github.com/kunchenguid/no-mistakes/internal/testgit"
)

func TestMain(m *testing.M) {
	// A parent git's repository variables are dropped first, since these tests
	// build repositories with git and the steps run it: under a hook that
	// inherited them, those gits would act on the contributor's checkout
	// (testgit.DropParentRepoEnv).
	testgit.DropParentRepoEnv()
	os.Unsetenv("GIT_CONFIG_COUNT")
	cleanup, err := stepstest.Init()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init fake CLI helper: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	if err := cleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup fake CLI helper: %v\n", err)
		code = 1
	}
	os.Exit(code)
}
