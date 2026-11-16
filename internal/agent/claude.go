package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/shellenv"
)

// claudeMaxRetries is the number of additional attempts past the initial
// invocation. With 3 retries the agent makes up to 4 total attempts before
// surfacing a transient API error to the pipeline.
const claudeMaxRetries = 3

// errNoStructuredOutput is returned when Claude succeeds but omits structured output.
var errNoStructuredOutput = errors.New("claude returned no structured output")

// claudeTurnRanToolsError marks a no-structured-output rejection whose turn
// ran a tool other than StructuredOutput, so claudeRetryClassifier never
// replays it. Its message is the rejection's own.
type claudeTurnRanToolsError struct{ err error }

func (e *claudeTurnRanToolsError) Error() string { return e.err.Error() }
func (e *claudeTurnRanToolsError) Unwrap() error { return e.err }

// claudeAcceptedOutputExitGrace is how long a session may stay alive after
// the CLI first accepted its structured output before the adapter ends its
// process group and keeps the accepted payload. A normal turn exits within a
// second or two of its acceptance; the recorded sessions that lingered (kept
// awake by their own background watches) did so for 5 to 10 minutes, long
// enough to be cut as a stall. A package variable so tests can shorten it.
var claudeAcceptedOutputExitGrace = 60 * time.Second

// claudeStructuredOutputTool is the tool Claude Code gives the model for its
// --json-schema output.
const claudeStructuredOutputTool = "StructuredOutput"

// claudeStructuredOutputRetriesExhausted is the result subtype the CLI ends a
// turn with when it refused every StructuredOutput call up to its own limit.
const claudeStructuredOutputRetriesExhausted = "error_max_structured_output_retries"

const claudeScannerMaxTokenSize = 256 * 1024 * 1024

// claudeAgent spawns the claude CLI for each invocation.
type claudeAgent struct {
	bin       string
	extraArgs []string
	subprocessContext
	// disableProjectSettings is the resolved, trusted-only opt-out. When true,
	// buildArgs suppresses claude's project-level settings/memory surface.
	disableProjectSettings bool
}

func (a *claudeAgent) Name() string { return "claude" }

// SupportsSessionResume reports claude's native durable-session capability:
// every stream-json event carries a session_id, and `claude -p --resume <id>`
// continues that session in print mode with the same identity.
func (a *claudeAgent) SupportsSessionResume() bool { return true }

func (a *claudeAgent) ReportsAgentAttempts() bool { return true }

// NeutralizesGateInstructions reports whether claude is currently launched with
// the target repo's project-level settings/memory suppressed. It is meaningful
// only under the opt-out (disableProjectSettings): the gate only consults it
// when the repo opted out. It is honest about the EFFECTIVE setting sources -
// claude's project surface (project CLAUDE.md/AGENTS.md, .claude/settings.json,
// and .claude/settings.local.json) is dropped iff the effective
// --setting-sources excludes both `project` and `local`. buildArgs appends
// `--setting-sources user` when the operator did not pin their own; an operator
// override that re-adds `project`/`local` defeats neutralization, so this
// returns false and the gate fails closed. Verified empirically: with project
// memory loaded claude adopts the firstmate identity; with --setting-sources
// user it does not.
func (a *claudeAgent) NeutralizesGateInstructions() bool {
	return a.disableProjectSettings && claudeEffectiveSettingSourcesNeutral(a.extraArgs)
}

func (a *claudeAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	return runWithRetry(ctx, "claude", opts, claudeMaxRetries, claudeRetryClassifier, nil, func() (*Result, error) {
		return a.runOnce(ctx, opts)
	})
}

func (a *claudeAgent) runOnce(ctx context.Context, opts RunOpts) (*Result, error) {
	resumeID := ""
	if opts.Session != nil {
		resumeID = opts.Session.ID
	}
	args := a.buildArgs(opts.JSONSchema, resumeID)
	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = opts.CWD
	// Claude Code print mode documents text stdin as its non-interactive
	// prompt transport. Giving os/exec an in-memory reader keeps user prompt
	// bytes out of argv and lets Cmd own the bounded concurrent copy, including
	// EOF, early-child-exit, cancellation, and WaitDelay cleanup paths.
	cmd.Stdin = strings.NewReader(opts.Prompt)
	cmd.Env = a.gitSafeEnv(opts.CWD, opts.Env)
	shellenv.ConfigureShellCommand(cmd)

	var stderrBuf []byte
	var stderrWG sync.WaitGroup
	started, err := startNativeAgentCommand(cmd, nativeAgentActivityObserver(opts, "claude"))
	if err != nil {
		return nil, fmt.Errorf("claude start: %w", err)
	}
	defer started.closePipes()
	pid := started.pid()
	emitAgentStarted(opts, "claude", pid)

	stderrWG.Add(1)
	go func() {
		defer stderrWG.Done()
		stderrBuf, _ = io.ReadAll(started.stderr)
	}()

	var usage TokenUsage
	stream := &claudeStream{}
	var lingered atomic.Bool
	var lingerTimer *time.Timer
	if len(opts.JSONSchema) > 0 {
		// The parser calls this on the goroutine that later stops the timer,
		// so lingerTimer needs no lock. The clock starts at the first
		// acceptance and is never restarted by a later one.
		stream.onFirstAccept = func() {
			lingerTimer = time.AfterFunc(claudeAcceptedOutputExitGrace, func() {
				lingered.Store(true)
				started.terminate()
			})
		}
	}
	parseErr := parseClaudeEvents(ctx, started.stdout, opts.OnChunk, &usage, stream)
	var waitErr error
	if parseErr != nil {
		parseErr = started.waitAfterParseError(parseErr)
	} else {
		waitErr = started.wait()
	}
	if lingerTimer != nil {
		lingerTimer.Stop()
	}
	stderrWG.Wait()

	// The report the CLI accepted is the turn's result, whatever the session
	// did after it: a later turn's result event without output, a non-success
	// subtype, a failed read of the stream, or a non-zero exit. Only the
	// caller's own context ending outranks it, and then the paths below
	// return what they returned before.
	if len(opts.JSONSchema) > 0 && stream.accepted != nil && ctx.Err() == nil {
		if opts.OnChunk != nil {
			if lingered.Load() {
				opts.OnChunk(fmt.Sprintf("claude: structured output was accepted but the session did not exit within %s; ended it", claudeAcceptedOutputExitGrace))
			} else if ignored := stream.ignoredAfterAcceptance(parseErr, waitErr); ignored != "" {
				opts.OnChunk("claude: structured output was accepted; ignored what followed (" + ignored + ")")
			}
		}
		res := &Result{
			Output:                stream.accepted,
			Text:                  stream.acceptedText,
			Usage:                 usage,
			UsageReported:         usage.Reported,
			CacheCreationReported: usage.CacheCreationReported,
		}
		a.stampClaudeResult(res, stream.sessionID, stream.model, resumeID)
		emitAgentExited(opts, "claude", pid, nil)
		return stream.withRefused(res), nil
	}

	if parseErr != nil {
		retErr := fmt.Errorf("claude parse events: %w", parseErr)
		emitAgentExited(opts, "claude", pid, retErr)
		return stream.withRefused(resultFromUsage(usage)), retErr
	}
	if waitErr != nil {
		var retErr error = fmt.Errorf("claude exited: %w: %s", waitErr, string(stderrBuf))
		if r := stream.result; r != nil && r.Subtype == claudeStructuredOutputRetriesExhausted {
			// The CLI exits non-zero on an error result. Name the subtype
			// and keep the mark whatever the exit code, so a caller that can
			// use the refused submissions still tells this case apart.
			retErr = &structuredOutputRetriesExhausted{err: fmt.Errorf("claude error: subtype=%s; %w", r.Subtype, retErr)}
		}
		emitAgentExited(opts, "claude", pid, retErr)
		return stream.withRefused(resultFromUsage(usage)), retErr
	}

	result := stream.result
	if result == nil {
		retErr := fmt.Errorf("claude returned no result event")
		emitAgentExited(opts, "claude", pid, retErr)
		return stream.withRefused(resultFromUsage(usage)), retErr
	}

	res, err := finalizeClaudeResult(result, opts.JSONSchema, usage)
	if res != nil {
		a.stampClaudeResult(res, result.sessionID, result.model, resumeID)
	}
	if errors.Is(err, errNoStructuredOutput) && opts.OnChunk != nil {
		opts.OnChunk(fmt.Sprintf("structured output missing: subtype=%s, text_len=%d, input_tokens=%d, output_tokens=%d",
			result.Subtype, len(result.text), usage.InputTokens, usage.OutputTokens))
		opts.OnChunk(fmt.Sprintf("raw result event: %s", string(result.rawEvent)))
	}
	if errors.Is(err, errNoStructuredOutput) && stream.ranTools {
		// Replaying a turn that ran tools runs them again in a fresh session:
		// for a Test turn that is a second full live validation, with its own
		// side effects, inside the same budget. Only a pure prompt-to-JSON
		// turn keeps its cheap retry (claudeRetryClassifier).
		if opts.OnChunk != nil {
			opts.OnChunk("claude returned no structured output after running tools; not replaying the turn")
		}
		err = &claudeTurnRanToolsError{err: err}
	}
	emitAgentExited(opts, "claude", pid, err)
	return stream.withRefused(res), err
}

// stampClaudeResult records the session, model and cache-reporting facts the
// stream carried on a Result.
func (a *claudeAgent) stampClaudeResult(res *Result, sessionID, model, resumeID string) {
	res.SessionID = sessionID
	res.Resumed = resumeID != ""
	res.Model = model
	// Claude reports cache-creation cost per message, so the accumulated
	// value is meaningful (recorded as a real number, not unknown). Its
	// stream-json usage is per-invocation, not cumulative across --resume,
	// so SessionUsageCumulative stays false and per-round deltas equal the
	// raw counters.
	res.CacheCreationReported = res.UsageReported
	if model != "" {
		res.ModelProvider = "anthropic"
	}
}

func (a *claudeAgent) Close() error { return nil }

func finalizeClaudeResult(result *claudeResult, schema json.RawMessage, usage TokenUsage) (*Result, error) {
	if result.IsError || result.Subtype != "success" {
		err := fmt.Errorf("claude error: subtype=%s", result.Subtype)
		if result.Subtype == claudeStructuredOutputRetriesExhausted {
			err = &structuredOutputRetriesExhausted{err: err}
		}
		return resultFromUsage(usage), err
	}
	if len(schema) > 0 && !claudeHasPayload(result.StructuredOutput) {
		return resultFromUsage(usage), rejectStructuredOutput(errNoStructuredOutput)
	}

	return &Result{
		Output:                result.StructuredOutput,
		Text:                  result.text,
		Usage:                 usage,
		UsageReported:         usage.Reported,
		CacheCreationReported: usage.CacheCreationReported,
	}, nil
}

// buildArgs constructs the claude CLI arguments. User-supplied extraArgs
// (from agent_args_override in the global config) are inserted ahead of the
// managed flags, so user choices win over no-mistakes' defaults. If the user
// supplied their own permission mode, the default --dangerously-skip-permissions
// is not added. A non-empty resumeID continues that session via --resume
// (never --fork-session: the session identity must stay stable so later
// turns keep resuming the same conversation).
func (a *claudeAgent) buildArgs(schema json.RawMessage, resumeID string) []string {
	args := make([]string, 0, len(a.extraArgs)+11)
	args = append(args, a.extraArgs...)
	args = append(args,
		"-p",
		"--verbose",
		"--output-format", "stream-json",
	)
	// Project-settings opt-out (trusted-only; see config.DisableProjectSettings):
	// load only user-level settings and memory, never the target repo's
	// project/local CLAUDE.md/AGENTS.md, .claude/settings.json, or
	// .claude/settings.local.json. In an agent-orchestration target (firstmate)
	// the project memory otherwise installs a fleet-captain identity on the gate
	// agent; `--setting-sources user` drops the project and local sources (the
	// full project surface) while preserving the operator's own user-level config
	// and auth. Suppressed only when the operator did not pin their own
	// --setting-sources. When the repo did not opt out, nothing is added and
	// claude loads its project memory exactly as before (backward-compat).
	if a.disableProjectSettings && !claudeUserSetSettingSources(a.extraArgs) {
		args = append(args, "--setting-sources", "user")
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	if len(schema) > 0 {
		args = append(args, "--json-schema", string(schema))
	}
	if !claudeUserSetPermissionMode(a.extraArgs) {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}

// claudeUserSetSettingSources reports whether extraArgs pin --setting-sources at
// all, in which case buildArgs does not add its own.
func claudeUserSetSettingSources(extraArgs []string) bool {
	_, pinned := claudeUserSettingSources(extraArgs)
	return pinned
}

// claudeUserSettingSources returns the operator-pinned --setting-sources value
// (last occurrence wins) and whether it was pinned. Handles `--setting-sources
// <v>` and `--setting-sources=<v>`.
func claudeUserSettingSources(extraArgs []string) (string, bool) {
	value := ""
	pinned := false
	for i, arg := range extraArgs {
		if arg == "--setting-sources" && i+1 < len(extraArgs) {
			value = extraArgs[i+1]
			pinned = true
		} else if strings.HasPrefix(arg, "--setting-sources=") {
			value = strings.TrimPrefix(arg, "--setting-sources=")
			pinned = true
		}
	}
	return value, pinned
}

// claudeEffectiveSettingSourcesNeutral reports whether the EFFECTIVE claude
// setting sources drop the target repo's project and local surface: true when
// the operator did not pin --setting-sources (buildArgs appends `user`) or
// pinned a value that contains neither `project` nor `local`, and false when the
// operator's value re-adds `project`/`local`.
func claudeEffectiveSettingSourcesNeutral(extraArgs []string) bool {
	value, pinned := claudeUserSettingSources(extraArgs)
	if !pinned {
		return true // buildArgs appends --setting-sources user
	}
	for _, src := range strings.Split(value, ",") {
		switch strings.ToLower(strings.TrimSpace(src)) {
		case "project", "local":
			return false
		}
	}
	return true
}

// claudeUserSetPermissionMode reports whether extraArgs already declare a
// permission flag, in which case buildArgs skips its default.
func claudeUserSetPermissionMode(extraArgs []string) bool {
	for _, arg := range extraArgs {
		if arg == "--dangerously-skip-permissions" ||
			arg == "--permission-mode" ||
			strings.HasPrefix(arg, "--permission-mode=") {
			return true
		}
	}
	return false
}

// claudeEvent is the top-level JSONL event from claude CLI.
type claudeEvent struct {
	Type      string          `json:"type"`
	Message   json.RawMessage `json:"message,omitempty"`
	SessionID string          `json:"session_id,omitempty"`

	// result fields
	Subtype          string          `json:"subtype,omitempty"`
	IsError          bool            `json:"is_error,omitempty"`
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	Usage            *claudeUsage    `json:"usage,omitempty"`
}

// claudeResult captures the parsed result event.
type claudeResult struct {
	Subtype          string
	IsError          bool
	StructuredOutput json.RawMessage
	text             string // accumulated text from assistant events
	rawEvent         json.RawMessage
	sessionID        string // durable session identity from the event stream
	model            string // model reported by assistant events
	seq              int    // position of the event in the stream
}

// claudeStream is what one invocation's stream-json said, read in stream
// order. A payload counts as accepted when the CLI accepted a StructuredOutput
// tool call (its tool_result is not an error) or when a result event carries
// structured_output; the latest acceptance from either source is the
// invocation's output. Both sources are read because a session may go on
// after its report: background tasks wake it for more turns, and the last
// turn's result event then carries no structured_output.
type claudeStream struct {
	result       *claudeResult   // the last result event
	accepted     json.RawMessage // the latest accepted payload
	acceptedText string          // assistant text up to that acceptance
	acceptedSeq  int
	ranTools     bool // a tool other than StructuredOutput was called
	sessionID    string
	model        string
	// refused holds the most recent StructuredOutput calls the CLI refused,
	// oldest first, for Result.RefusedOutputs.
	refused []RefusedOutput

	// structuredCalls holds each StructuredOutput call's input until its
	// tool_result says whether the CLI accepted it.
	structuredCalls map[string]json.RawMessage
	// onFirstAccept, when set, is called once, at the first acceptance.
	onFirstAccept func()
}

func (s *claudeStream) accept(payload json.RawMessage, text string, seq int) {
	first := s.accepted == nil
	s.accepted = append(json.RawMessage(nil), payload...)
	s.acceptedText = text
	s.acceptedSeq = seq
	if first && s.onFirstAccept != nil {
		s.onFirstAccept()
	}
}

// refuse records a StructuredOutput call the CLI refused, keeping only the
// most recent maxRefusedOutputs, each cut to maxRefusedOutputBytes.
func (s *claudeStream) refuse(payload json.RawMessage, reason string) {
	s.refused = append(s.refused, RefusedOutput{
		Payload: cutValidUTF8(string(payload), maxRefusedOutputBytes),
		Reason:  cutValidUTF8(reason, maxRefusedOutputBytes),
	})
	if extra := len(s.refused) - maxRefusedOutputs; extra > 0 {
		s.refused = append([]RefusedOutput(nil), s.refused[extra:]...)
	}
}

// withRefused sets the stream's refused submissions on res, creating a Result
// when the path had none, so they reach the caller beside an error too.
func (s *claudeStream) withRefused(res *Result) *Result {
	if len(s.refused) == 0 {
		return res
	}
	if res == nil {
		res = &Result{}
	}
	res.RefusedOutputs = append([]RefusedOutput(nil), s.refused...)
	return res
}

// ignoredAfterAcceptance names what the stream and the process did after the
// latest acceptance that would otherwise have failed the invocation, or ""
// when nothing did.
func (s *claudeStream) ignoredAfterAcceptance(parseErr, waitErr error) string {
	var parts []string
	switch r := s.result; {
	case r == nil || r.seq < s.acceptedSeq:
		parts = append(parts, "no result event after it")
	case r.IsError || r.Subtype != "success":
		parts = append(parts, "a result event with subtype="+r.Subtype)
	case r.seq > s.acceptedSeq:
		parts = append(parts, "a later result event without structured output")
	}
	if parseErr != nil {
		parts = append(parts, "reading the stream failed: "+parseErr.Error())
	}
	if waitErr != nil {
		parts = append(parts, "the process exited: "+waitErr.Error())
	}
	return strings.Join(parts, "; ")
}

// claudeHasPayload reports whether a structured-output slot holds a payload:
// absent and JSON null both mean none.
func claudeHasPayload(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type claudeMessage struct {
	Model   string          `json:"model"`
	Usage   claudeUsage     `json:"usage"`
	Content []claudeContent `json:"content"`
}

// claudeContent is one content block of an assistant or user message: text,
// a tool_use (ID, Name, Input) or a tool_result (ToolUseID, IsError, Content).
type claudeContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// claudeToolResultText reads a tool_result's content, which is either a
// string or a list of text blocks.
func claudeToolResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// parseClaudeEvents reads JSONL from the reader and dispatches events.
// It accumulates token usage and records into stream (when non-nil) the
// result events, the StructuredOutput calls the CLI accepted, and whether
// any other tool ran.
func parseClaudeEvents(ctx context.Context, r io.Reader, onChunk func(string), usage *TokenUsage, stream *claudeStream) error {
	if stream == nil {
		stream = &claudeStream{}
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), claudeScannerMaxTokenSize)
	var textBuf string
	seq := 0

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var event claudeEvent
		if err := json.Unmarshal(line, &event); err != nil {
			continue // skip malformed lines
		}
		seq++
		if event.SessionID != "" {
			stream.sessionID = event.SessionID
		}

		switch event.Type {
		case "assistant":
			var msg claudeMessage
			if err := json.Unmarshal(event.Message, &msg); err != nil {
				continue
			}
			if msg.Model != "" {
				stream.model = msg.Model
			}
			usage.Add(TokenUsage{
				InputTokens:           msg.Usage.InputTokens,
				OutputTokens:          msg.Usage.OutputTokens,
				CacheReadTokens:       msg.Usage.CacheReadInputTokens,
				CacheCreationTokens:   msg.Usage.CacheCreationInputTokens,
				Reported:              true,
				CacheCreationReported: true,
			})
			for _, c := range msg.Content {
				switch c.Type {
				case "text":
					if c.Text != "" {
						textBuf += c.Text
						if onChunk != nil {
							onChunk(c.Text)
						}
					}
				case "tool_use":
					if c.Name != claudeStructuredOutputTool {
						stream.ranTools = true
						continue
					}
					if c.ID != "" && claudeHasPayload(c.Input) {
						if stream.structuredCalls == nil {
							stream.structuredCalls = map[string]json.RawMessage{}
						}
						stream.structuredCalls[c.ID] = append(json.RawMessage(nil), c.Input...)
					}
				}
			}

		case "user":
			// A user message's content may be a plain string (a prompt) rather
			// than blocks; only tool_result blocks matter here, so a message
			// that does not decode as blocks is skipped.
			var msg claudeMessage
			if err := json.Unmarshal(event.Message, &msg); err != nil {
				continue
			}
			for _, c := range msg.Content {
				if c.Type != "tool_result" {
					continue
				}
				input, ok := stream.structuredCalls[c.ToolUseID]
				if !ok {
					continue
				}
				delete(stream.structuredCalls, c.ToolUseID)
				if c.IsError {
					stream.refuse(input, claudeToolResultText(c.Content))
				} else {
					stream.accept(input, textBuf, seq)
				}
			}

		case "result":
			raw := make(json.RawMessage, len(line))
			copy(raw, line)
			stream.result = &claudeResult{
				Subtype:          event.Subtype,
				IsError:          event.IsError,
				StructuredOutput: event.StructuredOutput,
				text:             textBuf,
				rawEvent:         raw,
				sessionID:        stream.sessionID,
				model:            stream.model,
				seq:              seq,
			}
			if claudeHasPayload(event.StructuredOutput) {
				stream.accept(event.StructuredOutput, textBuf, seq)
			}
		}
	}

	return scanner.Err()
}
