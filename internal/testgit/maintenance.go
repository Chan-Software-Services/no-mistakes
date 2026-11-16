package testgit

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"
)

// NoAutoMaintenance returns the git options that keep a command from
// starting git's auto-maintenance. A test helper that builds a repository and
// then copies or walks it runs the git calls that can start it with them: a
// commit, merge or fetch otherwise starts a detached
// `git maintenance run --auto`, which can take .git/objects/maintenance.lock
// after the command has returned, so the copy lists the lock and then finds
// it gone. maintenance.auto=false stops
// the start; gc.auto=0 keeps a git too old to have it from running
// `gc --auto` instead.
func NoAutoMaintenance() []string {
	return []string{"-c", "maintenance.auto=false", "-c", "gc.auto=0"}
}

// AutoMaintenanceStarts reads a GIT_TRACE2 trace file and returns the
// command line of each child that started git's auto-maintenance
// (`git maintenance run --auto` or `git gc --auto`). A missing or empty trace
// is an error, since then git never traced and nothing was checked.
func AutoMaintenanceStarts(trace string) ([]string, error) {
	data, err := os.ReadFile(trace)
	if err != nil {
		return nil, fmt.Errorf("read git trace: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("git trace %s is empty", trace)
	}
	var starts []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		_, after, ok := strings.Cut(scanner.Text(), "child_start[")
		if !ok {
			continue
		}
		_, command, ok := strings.Cut(after, "] ")
		if !ok {
			continue
		}
		fields := strings.Fields(command)
		if len(fields) < 3 || !hasArg(fields, "--auto") {
			continue
		}
		if (fields[1] == "maintenance" && fields[2] == "run") || fields[1] == "gc" {
			starts = append(starts, command)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read git trace: %w", err)
	}
	return starts, nil
}

func hasArg(fields []string, arg string) bool {
	for _, field := range fields {
		if field == arg {
			return true
		}
	}
	return false
}
