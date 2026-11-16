package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/testhome"
)

// testHomeChildEnv gates TestTestHomeChildWritesAReadOnlyTree, which only a
// child run of this package's test binary started by
// TestTheTestHomeIsRemovedWithAReadOnlyTreeInIt may run.
const testHomeChildEnv = "NM_CLI_TESTHOME_CHILD"

// The CLI tests used to leave a 373 MB /tmp/nm-cli-home-* behind on every
// run: a `go build` under the throwaway HOME extracted Go's module cache into
// it, the cache's directories are read-only, and the closing os.RemoveAll of
// the HOME failed. This runs the package's own TestMain in a child, with its
// directories under a base of this test's own, has the child leave the module
// cache's shape (a directory at 0555 holding a file at 0444) in its HOME, and
// asserts the base is empty once the child has exited.
func TestTheTestHomeIsRemovedWithAReadOnlyTreeInIt(t *testing.T) {
	if os.Getenv(testHomeChildEnv) == "1" {
		t.Skip("running as the child")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	// Runs before the temp dir's own removal, so a leak fails the assertion
	// below rather than the removal.
	t.Cleanup(func() { _ = testhome.RemoveAll(base) })

	cmd := exec.Command(binary, "-test.run=^TestTestHomeChildWritesAReadOnlyTree$", "-test.count=1", "-test.v")
	cmd.Env = append(os.Environ(), testhome.BaseEnv+"="+base, testHomeChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child run: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "child HOME is inside the base") {
		t.Fatalf("the child did not report its HOME inside %s:\n%s", base, out)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("the child's TestMain left %s in its base", entry.Name())
	}
}

func TestTestHomeChildWritesAReadOnlyTree(t *testing.T) {
	if os.Getenv(testHomeChildEnv) != "1" {
		t.Skip("only a child run of TestTheTestHomeIsRemovedWithAReadOnlyTreeInIt writes the tree")
	}
	base := os.Getenv(testhome.BaseEnv)
	home := os.Getenv("HOME")
	rel, err := filepath.Rel(base, home)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		// Writing the tree anywhere else would leave it where no test can
		// remove it.
		t.Fatalf("HOME %s is not inside %s %s", home, testhome.BaseEnv, base)
	}
	t.Log("child HOME is inside the base")
	dir := filepath.Join(home, "go", "pkg", "mod", "example.com", "m@v1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
}
