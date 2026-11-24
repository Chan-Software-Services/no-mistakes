package main

import (
	"os"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/testgit"
)

// TestMain drops the variables a parent git hands a hook to tie git to its
// repository (testgit.DropParentRepoEnv), since its Makefile tests run make,
// whose VERSION and COMMIT lines run git describe and git rev-parse in a
// temporary workspace: under a hook that inherited them, those gits would act
// on the contributor's checkout.
func TestMain(m *testing.M) {
	testgit.DropParentRepoEnv()
	os.Exit(m.Run())
}
