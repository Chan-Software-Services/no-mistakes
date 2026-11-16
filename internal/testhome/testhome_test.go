package testhome

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// keepEnv restores the variables Setup changes when the test ends.
func keepEnv(t *testing.T) {
	t.Helper()
	for _, name := range append([]string{"NM_HOME", "HOME", "XDG_CONFIG_HOME", BaseEnv}, goVars...) {
		if value, ok := os.LookupEnv(name); ok {
			t.Setenv(name, value)
		} else {
			t.Setenv(name, "")
			_ = os.Unsetenv(name)
		}
	}
}

// writeModuleCacheShape writes what Go's module cache leaves: a directory at
// 0555 holding a file at 0444, under a directory at 0555.
func writeModuleCacheShape(t *testing.T, home string) {
	t.Helper()
	mod := filepath.Join(home, "go", "pkg", "mod")
	dir := filepath.Join(mod, "example.com", "m@v1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{dir, filepath.Join(mod, "example.com")} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoveAllRemovesAReadOnlyTree(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	writeModuleCacheShape(t, home)
	if runtime.GOOS != "windows" {
		if err := os.RemoveAll(home); err == nil {
			t.Fatal("os.RemoveAll removed the read-only tree, so this test proves nothing")
		}
	}

	if err := RemoveAll(home); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		t.Fatalf("home still exists after RemoveAll: %v", err)
	}
}

func TestRemoveAllNeverChangesALinkedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symbolic link needs a privilege on Windows")
	}
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.WriteFile(outside, []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}

	if err := RemoveAll(home); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	info, err := os.Stat(outside)
	if err != nil {
		t.Fatalf("the link's target is gone: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o444 {
		t.Fatalf("the link's target mode = %o, want 0444", got)
	}
}

func TestRemoveAllOfAMissingDirectoryIsNotAnError(t *testing.T) {
	if err := RemoveAll(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
}

func TestSetupKeepsGoOutOfTheHomeAndCloseRemovesIt(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	keepEnv(t)
	base := t.TempDir()
	t.Setenv(BaseEnv, base)

	dirs, err := Setup("nm-x", filepath.Join(t.TempDir(), "unused-default"))
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if got := os.Getenv("NM_HOME"); got != dirs.Root {
		t.Errorf("NM_HOME = %q, want %q", got, dirs.Root)
	}
	if got := os.Getenv("HOME"); got != dirs.Home {
		t.Errorf("HOME = %q, want %q", got, dirs.Home)
	}
	for dir, prefix := range map[string]string{dirs.Root: "nm-x-test-", dirs.Home: "nm-x-home-"} {
		if filepath.Dir(dir) != base || !strings.HasPrefix(filepath.Base(dir), prefix) {
			t.Errorf("directory %s is not a %s* directory in %s %s", dir, prefix, BaseEnv, base)
		}
	}

	// What the toolchain resolves now, with HOME moved, is what a test's
	// `go build` would use.
	out, err := exec.Command("go", append([]string{"env"}, goVars...)...).Output()
	if err != nil {
		t.Fatalf("go env: %v", err)
	}
	values := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	if len(values) != len(goVars) {
		t.Fatalf("go env printed %d lines, want %d: %q", len(values), len(goVars), out)
	}
	for i, name := range goVars {
		value := strings.TrimSpace(values[i])
		if value == "" {
			t.Errorf("go env %s is empty", name)
		} else if isInside(value, dirs.Home) {
			t.Errorf("go env %s = %s is inside the throwaway HOME %s", name, value, dirs.Home)
		}
		if got := os.Getenv(name); got != value {
			t.Errorf("%s = %q, not exported as the toolchain's %q", name, got, value)
		}
	}

	writeModuleCacheShape(t, dirs.Home)
	if code := dirs.Close(0); code != 0 {
		t.Errorf("Close(0) = %d, want 0", code)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Errorf("Close left %s in the base", entry.Name())
	}
}

// A go command run under a fresh HOME writes telemetry counters into it and
// starts a detached upload sidecar that can recreate the HOME after Close.
func TestAGoCommandUnderTheHomeWritesNoTelemetry(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not on PATH")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Go's telemetry directory is under %AppData%, not HOME, on Windows")
	}
	keepEnv(t)
	_ = os.Unsetenv("XDG_CONFIG_HOME")
	t.Setenv(BaseEnv, t.TempDir())

	dirs, err := Setup("nm-x", "")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer dirs.Close(0)
	if out, err := exec.Command("go", "env", "GOCACHE").CombinedOutput(); err != nil {
		t.Fatalf("go env: %v: %s", err, out)
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	mode := filepath.Join(cfg, "go", "telemetry", "mode")
	err = filepath.WalkDir(dirs.Home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && path != mode {
			t.Errorf("a go command left %s in the throwaway HOME", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSetupUsesTheDefaultBaseWhenNoneIsSet(t *testing.T) {
	keepEnv(t)
	_ = os.Unsetenv(BaseEnv)
	base := t.TempDir()

	dirs, err := Setup("nm-x", base)
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer dirs.Close(0)
	for _, dir := range []string{dirs.Root, dirs.Home} {
		if filepath.Dir(dir) != base {
			t.Errorf("directory %s is not in the default base %s", dir, base)
		}
	}
}

func TestSetupWithoutGoOnPathSetsNoGoVariable(t *testing.T) {
	keepEnv(t)
	t.Setenv("PATH", t.TempDir())
	for _, name := range goVars {
		_ = os.Unsetenv(name)
	}
	t.Setenv(BaseEnv, t.TempDir())

	dirs, err := Setup("nm-x", "")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer dirs.Close(0)
	for _, name := range goVars {
		if value, ok := os.LookupEnv(name); ok {
			t.Errorf("%s = %q, want it left unset with no go on PATH", name, value)
		}
	}
}

func TestCloseNamesADirectoryItCannotRemove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only parent does not stop a removal on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root removes a directory from a read-only parent")
	}
	parent := filepath.Join(t.TempDir(), "parent")
	home := filepath.Join(parent, "home")
	root := filepath.Join(t.TempDir(), "root")
	for _, dir := range []string{home, root} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// RemoveAll makes only what is under the directory writable, so a
	// read-only parent is a removal that still fails.
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
	var buf bytes.Buffer
	stderr = &buf
	t.Cleanup(func() { stderr = os.Stderr })

	dirs := &Dirs{Root: root, Home: home}
	if code := dirs.Close(0); code != 1 {
		t.Errorf("Close(0) = %d, want 1 for a directory left behind", code)
	}
	want := "testhome: could not remove " + home + ": "
	if !strings.HasPrefix(buf.String(), want) || strings.Count(buf.String(), "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q", buf.String(), want)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("the removable NM_HOME was kept: %v", err)
	}

	buf.Reset()
	if code := dirs.Close(3); code != 3 {
		t.Errorf("Close(3) = %d, want the run's own code 3", code)
	}
}

func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		code    int
		failed  bool
		goos    string
		want    int
		comment string
	}{
		{0, false, "linux", 0, "a clean removal keeps a pass"},
		{0, true, "linux", 1, "a leak fails a passing run"},
		{0, true, "darwin", 1, "a leak fails a passing run"},
		{2, true, "linux", 2, "a failing run keeps its own code"},
		{0, true, "windows", 0, "a file a Windows daemon still holds never turns a pass red"},
		{2, true, "windows", 2, "a failing run keeps its own code"},
	} {
		if got := exitCode(tc.code, tc.failed, tc.goos); got != tc.want {
			t.Errorf("exitCode(%d, %v, %s) = %d, want %d: %s", tc.code, tc.failed, tc.goos, got, tc.want, tc.comment)
		}
	}
}
