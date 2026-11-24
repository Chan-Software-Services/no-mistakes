package testgit

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// parentRepoVars are the variables that tie git to one repository, which a
// parent git can hand a hook it runs: a go test started from a hook under a
// git commit or push inherits them, and a git the test starts in its own
// temporary directory then acts on the contributor's checkout instead (with
// GIT_DIR set, `git init` reinitializes that repository and a commit lands in
// it). They are the ones `git rev-parse --local-env-vars` lists, which git
// itself clears before it works in another repository, less the config ones
// (GIT_CONFIG, GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT): those name no
// repository, and the test helpers set or clear them on purpose (see
// TurnOffAutoMaintenance and IgnoreUserGitConfig).
var parentRepoVars = []string{
	"GIT_DIR",
	"GIT_WORK_TREE",
	"GIT_IMPLICIT_WORK_TREE",
	"GIT_INDEX_FILE",
	"GIT_COMMON_DIR",
	"GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_GRAFT_FILE",
	"GIT_NO_REPLACE_OBJECTS",
	"GIT_REPLACE_REF_BASE",
	"GIT_PREFIX",
	"GIT_SHALLOW_FILE",
}

// Environ returns the process environment without the variables a parent
// git hands a hook to tie git to its repository (GIT_DIR, GIT_WORK_TREE,
// GIT_INDEX_FILE and the rest of parentRepoVars), for a test that starts git
// with its own cmd.Env.
func Environ() []string {
	return WithoutParentRepo(os.Environ())
}

// WithoutParentRepo returns env without the variables a parent git hands a
// hook to tie git to its repository; see Environ.
func WithoutParentRepo(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !isParentRepoVar(name) {
			out = append(out, kv)
		}
	}
	return out
}

func isParentRepoVar(name string) bool {
	for _, v := range parentRepoVars {
		// Windows environment names are case-insensitive.
		if name == v || (runtime.GOOS == "windows" && strings.EqualFold(name, v)) {
			return true
		}
	}
	return false
}

// DropParentRepoEnv unsets, in the test process, the variables a parent git
// hands a hook to tie git to its repository (see Environ). A go test started
// from a hook under a git commit or push inherits them, and then every git
// the package's tests start in a temporary directory, directly or through the
// code under test, which runs git on the process environment, acts on the
// contributor's checkout instead. Call it first in TestMain, before m.Run, in
// every package whose tests start git; a test that sets one of them with
// t.Setenv afterwards keeps it. It panics if a variable cannot be unset.
func DropParentRepoEnv() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !isParentRepoVar(name) {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			panic(fmt.Sprintf("testgit: unset %s: %v", name, err))
		}
	}
}
