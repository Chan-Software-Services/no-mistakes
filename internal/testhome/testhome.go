// Package testhome gives a test package's TestMain a throwaway NM_HOME and
// HOME, so a full test run never touches a real ~/.no-mistakes, and removes
// them again when the run ends.
//
// It exists because a throwaway HOME leaked: Go's module and build caches
// default to paths under HOME, so a test's `go build` extracted a fresh
// module cache (hundreds of MB) into it, the module cache's directories are
// read-only, and the closing os.RemoveAll failed without a word. Enough of
// those filled /tmp's quota and failed unrelated packages with "disk quota
// exceeded". Setup therefore pins Go's caches before HOME moves and turns Go's
// telemetry off inside the new HOME, and Close
// makes the trees writable before it removes them and says so when it still
// cannot.
package testhome

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// BaseEnv names the variable that, when set, overrides the directory Setup
// creates its two directories in. A test points a child run of its own test
// binary at a directory it can inspect with it.
const BaseEnv = "NM_TEST_TMP_BASE"

// goVars are the Go settings that default to paths under HOME. Setup exports
// the values the toolchain resolves before HOME moves, so a test's `go build`
// reuses the contributor's caches and writes nothing under the throwaway HOME.
// This is chosen over GOFLAGS=-modcacherw, which makes the cache removable but
// still downloads every module on every run.
var goVars = []string{"GOMODCACHE", "GOCACHE", "GOPATH"}

// stderr is where Close reports a directory it could not remove.
var stderr io.Writer = os.Stderr

// Dirs are the two directories Setup made: Root is NM_HOME and Home is HOME.
type Dirs struct {
	Root string
	Home string
}

// Setup pins Go's caches, creates <prefix>-test-* (NM_HOME) and
// <prefix>-home-* (HOME) in the base directory, points NM_HOME and HOME at
// them, and turns Go's telemetry off inside the new HOME. The base is BaseEnv when it is set, otherwise defaultBase, and the
// system temporary directory when that is empty too. The caller sets its other
// variables and stops its own daemon, then ends with os.Exit(dirs.Close(code)).
func Setup(prefix, defaultBase string) (*Dirs, error) {
	base := os.Getenv(BaseEnv)
	if base == "" {
		base = defaultBase
	}
	if base == "" {
		base = os.TempDir()
	}
	if err := pinGoVars(); err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp(base, prefix+"-test-")
	if err != nil {
		return nil, fmt.Errorf("testhome: create test NM_HOME: %w", err)
	}
	home, err := os.MkdirTemp(base, prefix+"-home-")
	if err != nil {
		_ = RemoveAll(root)
		return nil, fmt.Errorf("testhome: create test HOME: %w", err)
	}
	_ = os.Setenv("NM_HOME", root)
	_ = os.Setenv("HOME", home)
	if err := turnGoTelemetryOff(home); err != nil {
		_ = RemoveAll(root)
		_ = RemoveAll(home)
		return nil, err
	}
	return &Dirs{Root: root, Home: home}, nil
}

// turnGoTelemetryOff writes Go's telemetry mode file as "off" when Go's
// telemetry directory (under os.UserConfigDir) now lies inside home. Every go
// command a test runs would otherwise write counters there, and about once a
// day start a detached upload sidecar that can outlive the run and recreate
// the HOME after Close removed it. With the mode off the go command writes
// nothing there and starts no sidecar.
func turnGoTelemetryOff(home string) error {
	cfg, err := os.UserConfigDir()
	if err != nil || !isInside(cfg, home) {
		return nil
	}
	dir := filepath.Join(cfg, "go", "telemetry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("testhome: turn Go telemetry off: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("off"), 0o644); err != nil {
		return fmt.Errorf("testhome: turn Go telemetry off: %w", err)
	}
	return nil
}

// isInside reports whether path is dir or lies under it.
func isInside(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// pinGoVars exports goVars at the values the toolchain resolves now, before
// HOME moves. With no go on PATH it sets nothing: nothing in the run can
// build then, so nothing can fill the HOME.
func pinGoVars() error {
	if _, err := exec.LookPath("go"); err != nil {
		return nil
	}
	args := append([]string{"env"}, goVars...)
	out, err := exec.Command("go", args...).Output()
	if err != nil {
		return fmt.Errorf("testhome: go %s: %w", strings.Join(args, " "), err)
	}
	values := strings.Split(strings.TrimRight(string(out), "\r\n"), "\n")
	if len(values) != len(goVars) {
		return fmt.Errorf("testhome: go %s printed %d lines, want %d: %q", strings.Join(args, " "), len(values), len(goVars), out)
	}
	for i, name := range goVars {
		value := strings.TrimSpace(values[i])
		if value == "" {
			return fmt.Errorf("testhome: go env %s is empty", name)
		}
		_ = os.Setenv(name, value)
	}
	return nil
}

// Close removes both directories and returns the exit code the run should
// end with. A directory it cannot remove is named on stderr as
// "testhome: could not remove <dir>: <error>".
func (d *Dirs) Close(code int) int {
	failed := false
	for _, dir := range []string{d.Root, d.Home} {
		if err := RemoveAll(dir); err != nil {
			fmt.Fprintf(stderr, "testhome: could not remove %s: %v\n", dir, err)
			failed = true
		}
	}
	return exitCode(code, failed, runtime.GOOS)
}

// exitCode turns a passing run red when a removal failed, except on Windows.
// Once RemoveAll has made the trees writable, a removal that still fails off
// Windows is a real leak, which stays in the temp dir on every run until its
// quota is gone, so it fails the package by name. On Windows a daemon that is
// still exiting can hold a file open for a moment, and that must not turn a
// green run red, so there the line on stderr is the whole report.
func exitCode(code int, removalFailed bool, goos string) int {
	if removalFailed && code == 0 && goos != "windows" {
		return 1
	}
	return code
}

// RemoveAll removes dir and everything in it, read-only trees included: it
// first makes every directory under dir writable (a module cache's are 0555)
// and every regular file writable (Windows refuses to delete a read-only
// file), then removes them. It never follows a symbolic link. A dir that does
// not exist is not an error.
func RemoveAll(dir string) error {
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable entry is left to os.RemoveAll, whose error names it.
			return nil
		}
		switch {
		case entry.IsDir():
			_ = os.Chmod(path, 0o700)
		case entry.Type().IsRegular():
			_ = os.Chmod(path, 0o600)
		}
		return nil
	})
	return os.RemoveAll(dir)
}
