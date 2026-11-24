package testgit

import (
	"os"
	"testing"
)

// TestMain drops the variables a parent git hands a hook to tie git to its
// repository (DropParentRepoEnv), since these tests build repositories with
// git: under a hook that inherited them, those gits would act on the
// contributor's checkout.
func TestMain(m *testing.M) {
	DropParentRepoEnv()
	os.Exit(m.Run())
}
