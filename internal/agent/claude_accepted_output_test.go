package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The streams below are modelled on run 01M6PVYEV5SAMQKVK1P6Z3V7DE:
// the CLI accepts a StructuredOutput call, then background tasks wake the
// session for more turns, each ending without a new submission.

const (
	claudeTestReport      = `{"verdict":"go","summary":"accepted report"}`
	claudeTestLaterReport = `{"verdict":"go","summary":"corrected report"}`
)

func claudeToolUseLine(id, name, input string) string {
	return `{"type":"assistant","session_id":"stream-session","message":{"model":"stream-model","usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
}

func claudeToolResultLine(id string, isError bool, content string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","is_error":` + strconv.FormatBool(isError) + `,"content":` + content + `}]}}`
}

func claudeTextLine(text string) string {
	return `{"type":"assistant","session_id":"stream-session","message":{"usage":{"input_tokens":1,"output_tokens":1},"content":[{"type":"text","text":"` + text + `"}]}}`
}

const (
	claudeAcceptedLine = `"Structured output provided successfully"`
	claudeResultNoOut  = `{"type":"result","subtype":"success","is_error":false,"session_id":"stream-session","origin":{"kind":"task-notification"}}`
)

func parseClaudeStreamLines(t *testing.T, lines ...string) claudeStream {
	t.Helper()
	var usage TokenUsage
	var stream claudeStream
	if err := parseClaudeEvents(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), nil, &usage, &stream); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return stream
}

func TestParseClaudeEvents_LatestAcceptanceWins(t *testing.T) {
	stream := parseClaudeStreamLines(t,
		claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
		claudeToolResultLine("toolu_1", false, claudeAcceptedLine),
		claudeTextLine("let me correct that"),
		claudeToolUseLine("toolu_2", "StructuredOutput", claudeTestLaterReport),
		claudeToolResultLine("toolu_2", false, claudeAcceptedLine),
		claudeTextLine("already submitted"),
		claudeResultNoOut,
	)
	if string(stream.accepted) != claudeTestLaterReport {
		t.Fatalf("accepted = %s, want the latest accepted call %s", stream.accepted, claudeTestLaterReport)
	}
	if stream.ranTools {
		t.Fatal("ranTools = true, want false: only StructuredOutput was called")
	}
	if stream.result == nil || claudeHasPayload(stream.result.StructuredOutput) {
		t.Fatalf("last result = %+v, want the recorded result without output", stream.result)
	}
}

func TestParseClaudeEvents_RefusedCallIsNotAnAcceptance(t *testing.T) {
	refused := `"Output does not match required schema: root: must have required property 'scenarios'"`
	stream := parseClaudeStreamLines(t,
		claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
		claudeToolResultLine("toolu_1", false, claudeAcceptedLine),
		claudeToolUseLine("toolu_2", "StructuredOutput", claudeTestLaterReport),
		claudeToolResultLine("toolu_2", true, refused),
		claudeResultNoOut,
	)
	if string(stream.accepted) != claudeTestReport {
		t.Fatalf("accepted = %s, want the earlier accepted call %s: a refused call is no acceptance", stream.accepted, claudeTestReport)
	}

	onlyRefused := parseClaudeStreamLines(t,
		claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
		claudeToolResultLine("toolu_1", true, refused),
		claudeResultNoOut,
	)
	if onlyRefused.accepted != nil {
		t.Fatalf("accepted = %s, want none when every call was refused", onlyRefused.accepted)
	}
}

func TestParseClaudeEvents_ToolResultWithListContent(t *testing.T) {
	list := `[{"type":"text","text":"Structured output provided successfully"}]`
	stream := parseClaudeStreamLines(t,
		claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
		claudeToolResultLine("toolu_1", false, list),
		claudeResultNoOut,
	)
	if string(stream.accepted) != claudeTestReport {
		t.Fatalf("accepted = %s, want the call whose tool_result content is a list", stream.accepted)
	}
	refused := parseClaudeStreamLines(t,
		claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
		claudeToolResultLine("toolu_1", true, `[{"type":"text","text":"Output does not match required schema"}]`),
		claudeResultNoOut,
	)
	if refused.accepted != nil {
		t.Fatalf("accepted = %s, want none for a refused call with list content", refused.accepted)
	}
}

// TestParseClaudeEvents_ResultOnlyStreamIsUnchanged covers the stream every
// turn produced before the tool call was read: the fake agent's default
// events (no tool call at all) and the recorded fixture, whose tool call and
// result event carry the same payload.
func TestParseClaudeEvents_ResultOnlyStreamIsUnchanged(t *testing.T) {
	resultOnly := parseClaudeStreamLines(t,
		claudeTextLine("done"),
		`{"type":"result","subtype":"success","is_error":false,"structured_output":`+claudeTestReport+`}`,
	)
	if string(resultOnly.accepted) != claudeTestReport || resultOnly.acceptedText != "done" {
		t.Fatalf("result-only stream: accepted %s text %q, want the result's output and the turn's text", resultOnly.accepted, resultOnly.acceptedText)
	}

	fixture, err := os.ReadFile(filepath.Join("..", "e2e", "fixtures", "claude", "structured.jsonl"))
	if err != nil {
		t.Fatalf("read recorded fixture: %v", err)
	}
	var usage TokenUsage
	var recorded claudeStream
	if err := parseClaudeEvents(context.Background(), strings.NewReader(string(fixture)), nil, &usage, &recorded); err != nil {
		t.Fatalf("parse recorded fixture: %v", err)
	}
	if recorded.result == nil || recorded.accepted == nil {
		t.Fatalf("recorded fixture: result %+v accepted %s, want both", recorded.result, recorded.accepted)
	}
	if string(recorded.accepted) != string(recorded.result.StructuredOutput) {
		t.Fatalf("recorded fixture: accepted %s, want the last result event's output %s", recorded.accepted, recorded.result.StructuredOutput)
	}
	if recorded.ignoredAfterAcceptance(nil, nil) != "" {
		t.Fatalf("recorded fixture: ignored %q, want nothing after a normal turn", recorded.ignoredAfterAcceptance(nil, nil))
	}
}

func TestParseClaudeEvents_NullStructuredOutputIsNoAcceptance(t *testing.T) {
	stream := parseClaudeStreamLines(t,
		`{"type":"result","subtype":"success","is_error":false,"structured_output":null}`,
	)
	if stream.accepted != nil {
		t.Fatalf("accepted = %s, want none for a null structured_output", stream.accepted)
	}
}

// claudeStreamRun is one scripted invocation of the stream helper.
type claudeStreamRun struct {
	lines    []string
	lingerMS int
	exitCode int
	schema   json.RawMessage
}

// runClaudeStreamHelper writes the scripted stream, optionally spawns a
// grandchild in its process group, lingers with stdout open, and exits.
func runClaudeStreamHelper() {
	data, err := os.ReadFile(os.Getenv("NM_CLAUDE_HELPER_STREAM"))
	if err != nil {
		os.Exit(6)
	}
	_, _ = os.Stdout.Write(data)
	if pidFile := os.Getenv("NM_CLAUDE_HELPER_GRANDCHILD_PID"); pidFile != "" {
		child := exec.Command(os.Args[0], "-test.run=^TestClaudeStdinHelper$")
		child.Env = append(os.Environ(), "NM_CLAUDE_STDIN_HELPER=grandchild")
		if err := child.Start(); err != nil {
			os.Exit(7)
		}
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o644)
	}
	if ms, _ := strconv.Atoi(os.Getenv("NM_CLAUDE_HELPER_LINGER_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	code, _ := strconv.Atoi(os.Getenv("NM_CLAUDE_HELPER_EXIT"))
	os.Exit(code)
}

// setupClaudeStreamHelper scripts the helper for every attempt of one run.
func setupClaudeStreamHelper(t *testing.T, run claudeStreamRun) *claudeAgent {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(run.lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NM_CLAUDE_STDIN_HELPER", "stream")
	t.Setenv("NM_CLAUDE_HELPER_STREAM", path)
	t.Setenv("NM_CLAUDE_HELPER_LINGER_MS", strconv.Itoa(run.lingerMS))
	t.Setenv("NM_CLAUDE_HELPER_EXIT", strconv.Itoa(run.exitCode))
	return newClaudeStdinHelperAgent(t)
}

// claudeRunLog collects what a run streamed and how many attempts it made.
type claudeRunLog struct {
	mu       sync.Mutex
	chunks   []string
	attempts int
}

func (l *claudeRunLog) opts(schema json.RawMessage, cwd string) RunOpts {
	return RunOpts{
		Prompt:     "validate",
		CWD:        cwd,
		JSONSchema: schema,
		OnChunk: func(s string) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.chunks = append(l.chunks, s)
		},
		OnAttempt: func(Attempt) {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.attempts++
		},
	}
}

func (l *claudeRunLog) streamed() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.chunks, "\n")
}

var claudeTestSchema = json.RawMessage(`{"type":"object"}`)

func TestClaudeAgent_AcceptedOutputSurvivesWhatFollows(t *testing.T) {
	defer withFastBackoff(t)()
	accepted := []string{
		claudeToolUseLine("toolu_watch", "Bash", `{"command":"npx playwright test","run_in_background":true}`),
		claudeToolResultLine("toolu_watch", false, `"Command running in background"`),
		claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
		claudeToolResultLine("toolu_1", false, claudeAcceptedLine),
		claudeTextLine("another stale monitor expiry"),
	}
	for _, tc := range []struct {
		name     string
		tail     []string
		exitCode int
		ignored  string
	}{
		{
			name:    "a later result without output",
			tail:    []string{claudeResultNoOut},
			ignored: "a later result event without structured output",
		},
		{
			name:     "a non-success result and a non-zero exit",
			tail:     []string{`{"type":"result","subtype":"error_during_execution","is_error":true}`},
			exitCode: 1,
			ignored:  "a result event with subtype=error_during_execution; the process exited: exit status 1",
		},
		{
			name:    "no result event at all",
			ignored: "no result event after it",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := setupClaudeStreamHelper(t, claudeStreamRun{lines: append(append([]string(nil), accepted...), tc.tail...), exitCode: tc.exitCode})
			var log claudeRunLog
			res, err := a.Run(context.Background(), log.opts(claudeTestSchema, t.TempDir()))
			if err != nil {
				t.Fatalf("Run: %v\nstreamed:\n%s", err, log.streamed())
			}
			if string(res.Output) != claudeTestReport {
				t.Fatalf("Output = %s, want the accepted report", res.Output)
			}
			if res.SessionID != "stream-session" || res.Model != "stream-model" {
				t.Fatalf("session %q model %q, want the stream's", res.SessionID, res.Model)
			}
			if log.attempts != 1 {
				t.Fatalf("attempts = %d, want one: an accepted report is never replayed", log.attempts)
			}
			want := "claude: structured output was accepted; ignored what followed (" + tc.ignored + ")"
			if !strings.Contains(log.streamed(), want) {
				t.Fatalf("streamed:\n%s\nwant the line %q", log.streamed(), want)
			}
		})
	}
}

// claudeToolTurnNoOutput is a turn that ran a tool and ended with a success
// result carrying no structured output.
var claudeToolTurnNoOutput = []string{
	claudeToolUseLine("toolu_watch", "Bash", `{"command":"npx playwright test"}`),
	claudeToolResultLine("toolu_watch", false, `"174 passed"`),
	claudeTextLine("the verdict is go"),
	claudeResultNoOut,
}

func TestClaudeAgent_NoOutputAfterAToolCallIsNotReplayed(t *testing.T) {
	defer withFastBackoff(t)()
	a := setupClaudeStreamHelper(t, claudeStreamRun{lines: claudeToolTurnNoOutput})
	var log claudeRunLog
	opts := log.opts(claudeTestSchema, t.TempDir())
	opts.NoReplayAfterTools = true
	_, err := a.Run(context.Background(), opts)
	if err == nil || err.Error() != "claude returned no structured output" {
		t.Fatalf("err = %v, want the no-structured-output rejection", err)
	}
	if !IsStructuredOutputRejected(err) {
		t.Fatalf("err = %v, want a structured-output rejection the step can correct", err)
	}
	if log.attempts != 1 {
		t.Fatalf("attempts = %d, want one: a turn that ran tools is never replayed", log.attempts)
	}
	streamed := log.streamed()
	for _, want := range []string{
		"claude returned no structured output after running tools; not replaying the turn",
		"structured output missing: subtype=success",
		"raw result event: ",
	} {
		if !strings.Contains(streamed, want) {
			t.Fatalf("streamed:\n%s\nwant %q", streamed, want)
		}
	}
}

// Only a caller that sets NoReplayAfterTools (the Test step's analyzer turns)
// loses the replay; every other step keeps it for a turn that ran tools.
func TestClaudeAgent_NoOutputAfterAToolCallIsReplayedWithoutNoReplayAfterTools(t *testing.T) {
	defer withFastBackoff(t)()
	a := setupClaudeStreamHelper(t, claudeStreamRun{lines: claudeToolTurnNoOutput})
	var log claudeRunLog
	_, err := a.Run(context.Background(), log.opts(claudeTestSchema, t.TempDir()))
	if !errors.Is(err, errNoStructuredOutput) {
		t.Fatalf("err = %v, want the no-structured-output rejection", err)
	}
	if log.attempts != claudeMaxRetries+1 {
		t.Fatalf("attempts = %d, want %d: without NoReplayAfterTools a turn that ran tools keeps its retry", log.attempts, claudeMaxRetries+1)
	}
	if strings.Contains(log.streamed(), "not replaying the turn") {
		t.Fatalf("streamed:\n%s\nwant no not-replaying line without NoReplayAfterTools", log.streamed())
	}
}

func TestClaudeAgent_NoOutputWithoutAToolCallIsStillRetried(t *testing.T) {
	defer withFastBackoff(t)()
	a := setupClaudeStreamHelper(t, claudeStreamRun{lines: []string{
		claudeTextLine("here is my answer in prose"),
		claudeResultNoOut,
	}})
	var log claudeRunLog
	_, err := a.Run(context.Background(), log.opts(claudeTestSchema, t.TempDir()))
	if !errors.Is(err, errNoStructuredOutput) {
		t.Fatalf("err = %v, want the no-structured-output rejection", err)
	}
	if log.attempts != claudeMaxRetries+1 {
		t.Fatalf("attempts = %d, want %d: a pure prompt-to-JSON turn keeps its retry", log.attempts, claudeMaxRetries+1)
	}
	if strings.Contains(log.streamed(), "not replaying the turn") {
		t.Fatalf("streamed:\n%s\na turn with no tool call must not claim it ran tools", log.streamed())
	}
}

func TestClaudeAgent_CancelledContextOutranksAnAcceptedOutput(t *testing.T) {
	a := setupClaudeStreamHelper(t, claudeStreamRun{
		lines: []string{
			claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
			claudeToolResultLine("toolu_1", false, claudeAcceptedLine),
			claudeTextLine("accepted-and-lingering"),
		},
		lingerMS: 30000,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts := RunOpts{Prompt: "validate", CWD: t.TempDir(), JSONSchema: claudeTestSchema, OnChunk: func(s string) {
		if s == "accepted-and-lingering" {
			cancel()
		}
	}}
	done := make(chan error, 1)
	go func() {
		_, err := a.runOnce(ctx, opts)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("runOnce succeeded after its caller's context was cancelled, want the cancellation to win")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runOnce did not return after cancellation")
	}
}

func TestClaudeAgent_NoSchemaBehavesAsBefore(t *testing.T) {
	defer withFastBackoff(t)()
	a := setupClaudeStreamHelper(t, claudeStreamRun{lines: []string{
		claudeTextLine("plain answer"),
		`{"type":"result","subtype":"success","is_error":false}`,
	}})
	var log claudeRunLog
	res, err := a.Run(context.Background(), log.opts(nil, t.TempDir()))
	if err != nil {
		t.Fatalf("Run without a schema: %v", err)
	}
	if res.Output != nil || res.Text != "plain answer" {
		t.Fatalf("Output %s Text %q, want no output and the turn's text", res.Output, res.Text)
	}

	failing := setupClaudeStreamHelper(t, claudeStreamRun{
		lines: []string{
			claudeToolUseLine("toolu_1", "StructuredOutput", claudeTestReport),
			claudeToolResultLine("toolu_1", false, claudeAcceptedLine),
		},
		exitCode: 1,
	})
	if _, err := failing.Run(context.Background(), (&claudeRunLog{}).opts(nil, t.TempDir())); err == nil || !strings.Contains(err.Error(), "claude exited") {
		t.Fatalf("err = %v, want a failed exit to fail a run without a schema as before", err)
	}
}
