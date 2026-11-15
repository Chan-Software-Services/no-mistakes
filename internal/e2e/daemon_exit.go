//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/e2edaemon"
)

// launchedDaemonLine matches the line the CLI and the daemon each log when a
// daemon process starts.
var launchedDaemonLine = regexp.MustCompile(`msg="daemon process launched" pid=(\d+)`)

// launchedDaemonPIDs returns the pid of each daemon launch a log records, in
// order and once each.
func launchedDaemonPIDs(log string) []int {
	var pids []int
	seen := map[int]bool{}
	for _, m := range launchedDaemonLine.FindAllStringSubmatch(log, -1) {
		pid, err := strconv.Atoi(m[1])
		if err != nil || seen[pid] {
			continue
		}
		seen[pid] = true
		pids = append(pids, pid)
	}
	return pids
}

// daemonsOutlivingTest returns the daemons this harness's own logs say it
// launched that running still reports once grace has passed. It looks at
// those pids and no other process.
func (h *Harness) daemonsOutlivingTest(running func(pid int) bool, grace time.Duration) []int {
	var logs string
	for _, name := range []string{"cli.log", "daemon.log"} {
		data, err := os.ReadFile(filepath.Join(h.NMHome, "logs", name))
		if err == nil {
			logs += string(data) + "\n"
		}
	}
	pids := launchedDaemonPIDs(logs)
	deadline := time.Now().Add(grace)
	for {
		var left []int
		for _, pid := range pids {
			if running(pid) {
				left = append(left, pid)
			}
		}
		if len(left) == 0 || !time.Now().Before(deadline) {
			return left
		}
		pids = left
		time.Sleep(50 * time.Millisecond)
	}
}

// daemonStillRuns reports whether pid is alive and still the daemon of this
// harness's NM_HOME, so a pid the system gave to another process since does
// not count.
func (h *Harness) daemonStillRuns(pid int) bool {
	alive, err := e2edaemon.ProcessAlive(pid)
	return err == nil && alive && e2edaemon.MatchesDaemonRoot(pid, h.NMHome)
}

// checkLaunchedDaemonsExited fails the test when a daemon it launched still
// runs after shutdown stopped the daemon. A daemon that outlives its test
// keeps the space of the binary it runs, though TestMain deleted the file,
// so each such test would eat the temp filesystem a little more. It is a
// test cleanup, registered before shutdown so it runs after it.
func (h *Harness) checkLaunchedDaemonsExited() {
	for _, pid := range h.daemonsOutlivingTest(h.daemonStillRuns, 5*time.Second) {
		h.t.Errorf("daemon pid %d, which this test launched for %s, still runs after the test stopped its daemon", pid, h.NMHome)
	}
}
