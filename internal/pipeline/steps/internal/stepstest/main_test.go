package stepstest

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Agent harnesses inject git config (e.g. safe.bareRepository=explicit)
	// via GIT_CONFIG_COUNT/KEY_n/VALUE_n; tests that need it re-set it with
	// t.Setenv (issue #362).
	os.Unsetenv("GIT_CONFIG_COUNT")
	os.Exit(m.Run())
}
