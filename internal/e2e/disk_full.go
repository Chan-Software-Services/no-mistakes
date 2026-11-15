//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// diskFullSigns are what a write says when its filesystem has no room left:
// ENOSPC, EDQUOT (a user quota, as a tmpfs /tmp can carry), SQLite's
// SQLITE_FULL, and Windows' ERROR_DISK_FULL. Compared in lower case.
var diskFullSigns = []string{
	"no space left on device",
	"disk quota exceeded",
	"database or disk is full",
	"not enough space on the disk",
}

// diskFullLine returns the first line of text that carries a full-disk sign,
// or "" when none does.
func diskFullLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		lowerLine := strings.ToLower(line)
		for _, sign := range diskFullSigns {
			if strings.Contains(lowerLine, sign) {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

// noteDiskFull keeps the first full-disk sign a command the harness ran
// printed, with source saying which command. Later signs are dropped: once
// the disk is full every write fails, and the first one is the cause.
func (h *Harness) noteDiskFull(source, output string) {
	line := diskFullLine(output)
	if line == "" {
		return
	}
	h.diskFullMu.Lock()
	defer h.diskFullMu.Unlock()
	if h.diskFullSign == "" {
		h.diskFullSource, h.diskFullSign = source, line
	}
}

// diskFullReport says that the test's temp filesystem ran out of space, when
// a command the harness ran or a log under NMHome shows it, and "" otherwise.
//
// A full disk fails a journey in many shapes that do not name it: a git
// command exiting 128, a run that failed before any step, and a run left
// `running` until the wait times out, because the daemon could not write the
// failure to its database either. Two copies of the suite fail at the same
// moment this way, since they share the filesystem.
func (h *Harness) diskFullReport() string {
	h.diskFullMu.Lock()
	source, sign := h.diskFullSource, h.diskFullSign
	h.diskFullMu.Unlock()
	if sign == "" {
		source, sign = h.diskFullSignInLogs()
	}
	if sign == "" {
		return ""
	}
	return fmt.Sprintf("the filesystem holding this test's temp directory (under %s) ran out of space while the test ran, so this failure is most likely the full disk or quota and not the product: free space there, or point TMPDIR at a roomier filesystem, and run the test again.\nfirst sign, from %s:\n%s", os.TempDir(), source, sign)
}

// diskFullSignInLogs looks for a full-disk sign in the daemon's log first,
// then in every other log under NMHome, in path order.
func (h *Harness) diskFullSignInLogs() (source, sign string) {
	logsDir := filepath.Join(h.NMHome, "logs")
	daemonLog := filepath.Join(logsDir, "daemon.log")
	read := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		if line := diskFullLine(string(data)); line != "" {
			source, sign = path, line
			return true
		}
		return false
	}
	if read(daemonLog) {
		return source, sign
	}
	_ = filepath.Walk(logsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || path == daemonLog {
			return nil
		}
		if read(path) {
			return filepath.SkipAll
		}
		return nil
	})
	return source, sign
}

// reportDiskFull adds the full-disk report to a test that already failed.
// It is a test cleanup, registered last so it reads the logs before the
// harness's temp state is removed.
func (h *Harness) reportDiskFull() {
	if !h.t.Failed() {
		return
	}
	if report := h.diskFullReport(); report != "" {
		h.t.Errorf("DISK FULL: %s", report)
	}
}
