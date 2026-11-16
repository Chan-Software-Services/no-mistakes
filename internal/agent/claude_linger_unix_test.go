//go:build unix

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestClaudeAgent_LingeringSessionIsEndedAfterItsAcceptance models the
// stall cut of run 01M6MYHR17WEVPM11RFY2J3CTC: the CLI accepted the
// report and the session then stayed alive on a background task with nothing
// to say. With a shortened grace the adapter ends the whole process group,
// the background task included, and keeps the accepted report.
func TestClaudeAgent_LingeringSessionIsEndedAfterItsAcceptance(t *testing.T) {
	prev := claudeAcceptedOutputExitGrace
	claudeAcceptedOutputExitGrace = 300 * time.Millisecond
	t.Cleanup(func() { claudeAcceptedOutputExitGrace = prev })

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("NM_CLAUDE_HELPER_GRANDCHILD_PID", pidFile)
	a := setupClaudeStreamHelper(t, claudeStreamRun{
		lines: []string{
			claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
			claudeToolResultLine("toolu_1", false, claudeAcceptedLine),
			`{"type":"result","subtype":"success","is_error":false,"structured_output":` + claudeTestReport + `}`,
		},
		lingerMS: 120000,
	})
	var log claudeRunLog
	started := time.Now()
	res, err := a.Run(context.Background(), log.opts(claudeTestSchema, t.TempDir()))
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Run: %v\nstreamed:\n%s", err, log.streamed())
	}
	if string(res.Output) != claudeTestReport {
		t.Fatalf("Output = %s, want the accepted report", res.Output)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("Run took %s, want it ended shortly after the %s grace", elapsed, claudeAcceptedOutputExitGrace)
	}
	if log.attempts != 1 {
		t.Fatalf("attempts = %d, want one", log.attempts)
	}
	want := "claude: structured output was accepted but the session did not exit within 300ms; ended it"
	if !strings.Contains(log.streamed(), want) {
		t.Fatalf("streamed:\n%s\nwant %q", log.streamed(), want)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("background task %d of the ended session is still alive, want the whole group gone", pid)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
