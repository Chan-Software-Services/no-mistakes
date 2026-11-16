package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// The streams below are modelled on run 01M6KC8W8JRWQJ0RHZ5S25M04W (T213):
// the CLI refuses the agent's whole report for a missing artifacts array,
// three times, and then accepts a probe with no scenarios.

const (
	claudeRefusalArtifacts = "Output does not match required schema: root: must have required property 'artifacts'"
	claudeProbeReport      = `{"verdict":"go","summary":"Test submission to check the schema accepts the artifacts field.","scenarios":[]}`
)

func claudeRefusedReport(n int) string {
	return `{"verdict":"go","summary":"full report ` + string(rune('0'+n)) + `","scenarios":[{"name":"an organizer adds an option"}]}`
}

func TestParseClaudeEvents_RefusedCallsAreKeptWithTheirReason(t *testing.T) {
	stream := parseClaudeStreamLines(t,
		claudeToolUseLine("toolu_1", "StructuredOutput", claudeRefusedReport(1)),
		claudeToolResultLine("toolu_1", true, `"first refusal"`),
		claudeToolUseLine("toolu_2", "StructuredOutput", claudeRefusedReport(2)),
		claudeToolResultLine("toolu_2", true, `"`+claudeRefusalArtifacts+`"`),
		claudeToolUseLine("toolu_3", "StructuredOutput", claudeRefusedReport(3)),
		claudeToolResultLine("toolu_3", true, `[{"type":"text","text":"`+claudeRefusalArtifacts+`"}]`),
		claudeToolUseLine("toolu_4", "StructuredOutput", claudeRefusedReport(4)),
		claudeToolResultLine("toolu_4", true, `"`+claudeRefusalArtifacts+`"`),
		claudeToolUseLine("toolu_5", "StructuredOutput", claudeProbeReport),
		claudeToolResultLine("toolu_5", false, claudeAcceptedLine),
		`{"type":"result","subtype":"success","is_error":false,"structured_output":`+claudeProbeReport+`}`,
	)
	if string(stream.accepted) != claudeProbeReport {
		t.Fatalf("accepted = %s, want the accepted probe", stream.accepted)
	}
	want := []RefusedOutput{
		{Payload: claudeRefusedReport(2), Reason: claudeRefusalArtifacts},
		{Payload: claudeRefusedReport(3), Reason: claudeRefusalArtifacts},
		{Payload: claudeRefusedReport(4), Reason: claudeRefusalArtifacts},
	}
	if len(stream.refused) != len(want) {
		t.Fatalf("refused = %+v, want the three most recent refusals %+v", stream.refused, want)
	}
	for i := range want {
		if stream.refused[i] != want[i] {
			t.Fatalf("refused[%d] = %+v, want %+v", i, stream.refused[i], want[i])
		}
	}
}

func TestParseClaudeEvents_RefusedPayloadIsCutToValidUTF8(t *testing.T) {
	// Each "é" is two bytes, so an odd byte offset cuts one in half unless the
	// cut steps back to a rune boundary.
	big := `{"summary":"x` + strings.Repeat("é", maxRefusedOutputBytes) + `"}`
	stream := parseClaudeStreamLines(t,
		claudeToolUseLine("toolu_1", "StructuredOutput", big),
		claudeToolResultLine("toolu_1", true, `"too big"`),
		claudeResultNoOut,
	)
	if len(stream.refused) != 1 {
		t.Fatalf("refused = %d entries, want one", len(stream.refused))
	}
	got := stream.refused[0].Payload
	if len(got) > maxRefusedOutputBytes || len(got) < maxRefusedOutputBytes-utf8.UTFMax {
		t.Fatalf("payload length = %d, want it cut to just under %d bytes", len(got), maxRefusedOutputBytes)
	}
	if !utf8.ValidString(got) || !strings.HasPrefix(big, got) {
		t.Fatal("cut payload is not a valid UTF-8 prefix of the submission")
	}
	if maxRefusedOutputBytes != 64*1024 {
		t.Fatalf("maxRefusedOutputBytes = %d, want 64 KiB", maxRefusedOutputBytes)
	}
}

func TestClaudeAgent_RefusedOutputsRideASuccessfulResult(t *testing.T) {
	defer withFastBackoff(t)()
	a := setupClaudeStreamHelper(t, claudeStreamRun{lines: []string{
		claudeToolUseLine("toolu_drive", "Bash", `{"command":"npx playwright test"}`),
		claudeToolResultLine("toolu_drive", false, `"1 passed"`),
		claudeToolUseLine("toolu_1", "StructuredOutput", claudeRefusedReport(1)),
		claudeToolResultLine("toolu_1", true, `"`+claudeRefusalArtifacts+`"`),
		claudeToolUseLine("toolu_2", "StructuredOutput", claudeProbeReport),
		claudeToolResultLine("toolu_2", false, claudeAcceptedLine),
		`{"type":"result","subtype":"success","is_error":false,"structured_output":` + claudeProbeReport + `}`,
	}})
	var log claudeRunLog
	res, err := a.Run(context.Background(), log.opts(claudeTestSchema, t.TempDir()))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(res.Output) != claudeProbeReport {
		t.Fatalf("Output = %s, want the accepted probe", res.Output)
	}
	if len(res.RefusedOutputs) != 1 || res.RefusedOutputs[0].Payload != claudeRefusedReport(1) || res.RefusedOutputs[0].Reason != claudeRefusalArtifacts {
		t.Fatalf("RefusedOutputs = %+v, want the one refused report with its refusal", res.RefusedOutputs)
	}
}

func TestClaudeAgent_RefusedOutputsRideAnExhaustedRetriesError(t *testing.T) {
	defer withFastBackoff(t)()
	a := setupClaudeStreamHelper(t, claudeStreamRun{
		lines: []string{
			claudeToolUseLine("toolu_drive", "Bash", `{"command":"npx playwright test"}`),
			claudeToolResultLine("toolu_drive", false, `"1 passed"`),
			claudeToolUseLine("toolu_1", "StructuredOutput", claudeRefusedReport(1)),
			claudeToolResultLine("toolu_1", true, `"`+claudeRefusalArtifacts+`"`),
			claudeToolUseLine("toolu_2", "StructuredOutput", claudeRefusedReport(2)),
			claudeToolResultLine("toolu_2", true, `"`+claudeRefusalArtifacts+`"`),
			`{"type":"result","subtype":"error_max_structured_output_retries","is_error":true}`,
		},
		exitCode: 1,
	})
	var log claudeRunLog
	res, err := a.Run(context.Background(), log.opts(claudeTestSchema, t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "claude error: subtype=error_max_structured_output_retries") {
		t.Fatalf("err = %v, want the exhausted-retries error", err)
	}
	if !IsStructuredOutputRetriesExhausted(err) {
		t.Fatalf("err = %v, want it marked as exhausted structured-output retries", err)
	}
	if IsStructuredOutputRejected(err) {
		t.Fatalf("err = %v must not be a structured-output rejection: Review and the fix turns keep failing on it", err)
	}
	if log.attempts != 1 {
		t.Fatalf("attempts = %d, want one", log.attempts)
	}
	if res == nil || len(res.RefusedOutputs) != 2 || res.RefusedOutputs[1].Payload != claudeRefusedReport(2) {
		t.Fatalf("result beside the error = %+v, want both refused reports", res)
	}
}

func TestFinalizeClaudeResult_ExhaustedRetriesIsMarked(t *testing.T) {
	_, err := finalizeClaudeResult(&claudeResult{Subtype: "error_max_structured_output_retries", IsError: true}, json.RawMessage(`{"type":"object"}`), TokenUsage{})
	if !IsStructuredOutputRetriesExhausted(err) {
		t.Fatalf("err = %v, want it marked as exhausted structured-output retries", err)
	}
	_, other := finalizeClaudeResult(&claudeResult{Subtype: "error_during_execution", IsError: true}, json.RawMessage(`{"type":"object"}`), TokenUsage{})
	if other == nil || IsStructuredOutputRetriesExhausted(other) {
		t.Fatalf("err = %v, want another error subtype left unmarked", other)
	}
}
