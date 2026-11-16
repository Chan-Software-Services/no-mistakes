//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// acceptedReportScenarioName is the one scenario the scripted Test report
// carries, so the journey can tell the submitted report from any other.
const acceptedReportScenarioName = "an applicant submits an application and sees it in the list"

// claudeStreamLine marshals one stream-json event for a claude_stream script.
func claudeStreamLine(t *testing.T, event map[string]any) string {
	t.Helper()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal stream event: %v", err)
	}
	return string(data)
}

// acceptedThenNotifiedScenario scripts the Test evidence turn of run
// 01M6PVYEV5SAMQKVK1P6Z3V7DE (T223): the agent starts a background watch,
// submits its whole report through the StructuredOutput tool, which the CLI
// accepts, and then the watch's notifications wake the session for more
// turns, each ending without a new submission. The stream's last result
// event therefore carries no structured_output.
func acceptedThenNotifiedScenario(t *testing.T) string {
	t.Helper()
	report := map[string]any{
		"findings":        []any{},
		"summary":         "every scenario driven live passed",
		"tested":          []string{"npx playwright test e2e/applications.spec.ts"},
		"testing_summary": "Drove the application flow live against an isolated stack; it passed.",
		"scenarios": []any{map[string]any{
			"name":     acceptedReportScenarioName,
			"result":   "pass",
			"live":     true,
			"evidence": "the submitted application appeared at the top of the list",
			"reason":   "",
		}},
		"verdict":   "go",
		"artifacts": []any{},
	}
	const session = "a3f0c0de-0000-4000-8000-000000000223"
	usage := map[string]int{"input_tokens": 10, "output_tokens": 5}
	stream := []string{
		claudeStreamLine(t, map[string]any{"type": "system", "subtype": "init", "session_id": session}),
		claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"model": "claude-opus-4-7", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_watch", "name": "Bash", "input": map[string]any{
				"command": "npx playwright test e2e/applications.spec.ts", "run_in_background": true,
			}}},
		}}),
		claudeStreamLine(t, map[string]any{"type": "user", "session_id": session, "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_watch", "content": "Command running in background with ID: bg1"}},
		}}),
		claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"model": "claude-opus-4-7", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_report", "name": "StructuredOutput", "input": report}},
		}}),
		claudeStreamLine(t, map[string]any{"type": "user", "session_id": session, "message": map[string]any{
			"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_report", "content": "Structured output provided successfully"}},
		}}),
		claudeStreamLine(t, map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session, "result_index": 0, "structured_output": report, "usage": usage}),
	}
	for i, text := range []string{
		"The structured result has been submitted, and this is another stale monitor expiry.",
		"The result is already on record. I submitted it successfully, so I won't send it again.",
	} {
		stream = append(stream,
			claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
				"model": "claude-opus-4-7", "usage": usage, "content": []any{map[string]any{"type": "text", "text": text}},
			}}),
			claudeStreamLine(t, map[string]any{
				"type": "result", "subtype": "success", "is_error": false, "session_id": session, "result_index": i + 1,
				"origin": map[string]any{"kind": "task-notification", "producer": "session-task"}, "result": text, "usage": usage,
			}),
		)
	}

	var b strings.Builder
	b.WriteString("actions:\n  - match: \"" + testEvidencePromptMarker + "\"\n    claude_stream:\n")
	for _, line := range stream {
		quoted, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("      - " + string(quoted) + "\n")
	}
	b.WriteString(`  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks detected in the diff"
      risk_scope: source-or-external
`)
	path := filepath.Join(t.TempDir(), "accepted-then-notified-scenario.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

// TestAcceptedTestReportSurvivesLaterTurnsJourney reproduces run
// 01M6PVYEV5SAMQKVK1P6Z3V7DE (T223) through the stock `no-mistakes axi`
// surface. The Test agent's report was accepted by the CLI, but the session's
// last result event carried no structured output, so the adapter called the
// turn "claude returned no structured output" and replayed the whole live
// validation until the budget ran out. The accepted report is now the turn's
// result: the step records its verdict after one evidence turn, and the step
// log says what the adapter ignored.
func TestAcceptedTestReportSurvivesLaterTurnsJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: acceptedThenNotifiedScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	branch := "feature/accepted-test-report"
	h.CommitChange(branch, ".no-mistakes.yaml", "commands:\n  lint: 'exit 0'\n", "configure lint")
	h.CommitChange(branch, "feature.txt", "value\n", "add feature")

	runOut, runErr := h.Run("axi", "run", "--intent", "validate the feature whose test agent keeps getting woken", "--skip", "pr,ci")
	run := h.WaitForRun(branch, 90*time.Second)
	t.Logf("stock axi run surface:\n%s", runOut)

	evidenceTurns := 0
	for _, inv := range h.AgentInvocations() {
		if strings.Contains(inv.Prompt, testEvidencePromptMarker) {
			evidenceTurns++
		}
	}
	logOut, err := h.Run("axi", "logs", "--step", "test", "--full")
	if err != nil {
		t.Fatalf("axi logs: %v\n%s", err, logOut)
	}
	if evidenceTurns != 1 || strings.Contains(logOut, "retrying after transient error") {
		t.Fatalf("Test evidence invocations = %d, want exactly one: an accepted report is never replayed; step log:\n%s", evidenceTurns, logOut)
	}
	if !strings.Contains(logOut, "claude: structured output was accepted; ignored what followed") {
		t.Fatalf("Test step log does not say what the adapter ignored:\n%s", logOut)
	}
	if runErr != nil || run.Status != types.RunCompleted {
		t.Fatalf("run status = %s (axi run error %v, run error %q), want completed", run.Status, runErr, deref(run.Error))
	}

	step, ok := findStep(run.Steps, types.StepTest)
	if !ok || step.Status != types.StepStatusCompleted || step.FindingsJSON == nil {
		t.Fatalf("test step = %+v, want completed with recorded findings", step)
	}
	findings, err := types.ParseFindingsJSON(*step.FindingsJSON)
	if err != nil {
		t.Fatalf("parse Test findings: %v", err)
	}
	if findings.Verdict != types.TestVerdictGo || len(findings.Scenarios) != 1 || findings.Scenarios[0].Name != acceptedReportScenarioName {
		t.Fatalf("recorded Test report = verdict %q scenarios %+v, want the submitted report's go and its one scenario", findings.Verdict, findings.Scenarios)
	}
}

// refusedReportScenarioName is the one scenario the refused Test reports
// carry. The fake agent answers a correction prompt that holds it with the
// report rebuilt from them, and any other correction prompt with what a
// correction can rebuild from the probe alone.
const refusedReportScenarioName = "an organizer adds an option and sees every file answer offered"

// refusedThenProbedScenario scripts the Test evidence turn of run
// 01M6KC8W8JRWQJ0RHZ5S25M04W (T213): the agent submits its whole report
// three times and the CLI refuses each for the missing artifacts array, then
// it sends a probe with no scenarios, which the CLI accepts and which becomes
// the turn's output. The gate's validator refuses the probe's empty scenarios
// and asks for a correction. A correction that sees the refused reports
// rebuilds the report from them; one that sees only the probe can say no more
// than that nothing was driven live, which is how the recorded run parked
// "inconclusive: 0 of 1 scenarios driven live" after a full live run.
func refusedThenProbedScenario(t *testing.T) string {
	t.Helper()
	scenario := map[string]any{
		"name":     refusedReportScenarioName,
		"result":   "pass",
		"live":     true,
		"evidence": "the option picker listed every answer from the imported file that no other option held",
		"reason":   "",
	}
	refusedReport := map[string]any{
		"findings":        []any{},
		"summary":         "every scenario driven live passed",
		"tested":          []string{"npx playwright test e2e/options.spec.ts"},
		"testing_summary": "Drove the option picker live against an isolated stack; it passed.",
		"scenarios":       []any{scenario},
		"verdict":         "go",
	}
	probe := map[string]any{
		"findings":        []any{},
		"summary":         "Test submission to check the schema accepts the artifacts field.",
		"tested":          []string{"Baseline (already run): ./scripts/test.sh"},
		"testing_summary": "Schema check only.",
		"artifacts":       []any{},
		"scenarios":       []any{},
		"verdict":         "go",
	}
	const session = "a3f0c0de-0000-4000-8000-000000000213"
	usage := map[string]int{"input_tokens": 10, "output_tokens": 5}
	call := func(id string, input map[string]any) string {
		return claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"model": "claude-opus-4-7", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": "StructuredOutput", "input": input}},
		}})
	}
	outcome := func(id, content string, isError bool) string {
		block := map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}
		if isError {
			block["is_error"] = true
		}
		return claudeStreamLine(t, map[string]any{"type": "user", "session_id": session, "message": map[string]any{
			"role": "user", "content": []any{block},
		}})
	}
	stream := []string{
		claudeStreamLine(t, map[string]any{"type": "system", "subtype": "init", "session_id": session}),
		claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"model": "claude-opus-4-7", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_drive", "name": "Bash", "input": map[string]any{
				"command": "npx playwright test e2e/options.spec.ts",
			}}},
		}}),
		outcome("toolu_drive", "1 passed", false),
	}
	for i := 1; i <= 3; i++ {
		id := "toolu_report" + string(rune('0'+i))
		stream = append(stream,
			call(id, refusedReport),
			outcome(id, "Output does not match required schema: root: must have required property 'artifacts'", true),
		)
	}
	stream = append(stream,
		call("toolu_probe", probe),
		outcome("toolu_probe", "Structured output provided successfully", false),
		claudeStreamLine(t, map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session, "structured_output": probe, "usage": usage}),
	)

	var b strings.Builder
	b.WriteString("actions:\n  - match: \"" + testEvidencePromptMarker + "\"\n    claude_stream:\n")
	for _, line := range stream {
		quoted, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("      - " + string(quoted) + "\n")
	}
	b.WriteString(`  - match: "` + refusedReportScenarioName + `"
    text: "rebuilt the report from the refused submission"
    structured:
      findings: []
      summary: "every scenario driven live passed"
      tested: ["npx playwright test e2e/options.spec.ts"]
      testing_summary: "Drove the option picker live against an isolated stack; it passed."
      artifacts: []
      scenarios:
        - name: "` + refusedReportScenarioName + `"
          result: pass
          live: true
          evidence: "the option picker listed every answer from the imported file that no other option held"
          reason: ""
      verdict: go
  - match: "` + testCorrectionPromptMarker + `"
    text: "the probe records nothing driven live"
    structured:
      findings: []
      summary: "No live validation was recorded in the prior submission."
      tested: ["Baseline (already run): ./scripts/test.sh"]
      testing_summary: "The prior submission recorded no live scenario."
      artifacts: []
      scenarios:
        - name: "the change works for an end user"
          result: untested
          live: false
          evidence: ""
          reason: "the prior payload did not establish a live result"
      verdict: inconclusive
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks detected in the diff"
      risk_scope: source-or-external
`)
	path := filepath.Join(t.TempDir(), "refused-then-probed-scenario.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

// testCorrectionPromptMarker opens the Test step's correction prompt.
const testCorrectionPromptMarker = "Your previous structured findings were REJECTED"

// TestRefusedTestReportKeepsItsEvidenceJourney reproduces run
// 01M6KC8W8JRWQJ0RHZ5S25M04W (T213) through the stock `no-mistakes axi`
// surface. The CLI refused the Test agent's whole report three times for a
// missing artifacts array and then accepted a probe with no scenarios; the
// gate's correction turn saw only the probe, and the step parked inconclusive
// with 0 of 1 scenarios driven live after a full live run. The correction
// turn now receives the refused reports, so the recorded verdict and
// scenarios are theirs, and a note says the report was rebuilt.
func TestRefusedTestReportKeepsItsEvidenceJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: refusedThenProbedScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	branch := "feature/refused-test-report"
	h.CommitChange(branch, ".no-mistakes.yaml", "commands:\n  lint: 'exit 0'\n", "configure lint")
	h.CommitChange(branch, "feature.txt", "value\n", "add feature")

	runOut, runErr := h.Run("axi", "run", "--intent", "validate the feature whose test report the CLI refused", "--skip", "pr,ci")
	t.Logf("stock axi run surface:\n%s", runOut)
	// A Test step that parks is the failure this journey reproduces, so it
	// is reported at once with what it parked on rather than waited out.
	if active := h.ActiveRun(branch); active != nil {
		if step, ok := findStep(h.RunInfo(active.ID).Steps, types.StepTest); ok && step.Status == types.StepStatusAwaitingApproval {
			t.Fatalf("Test step parked, want the refused report's go recorded; findings: %s", deref(step.FindingsJSON))
		}
	}
	run := h.WaitForRun(branch, 90*time.Second)

	var corrections []string
	for _, inv := range h.AgentInvocations() {
		if strings.Contains(inv.Prompt, testCorrectionPromptMarker) {
			corrections = append(corrections, inv.Prompt)
		}
	}
	if len(corrections) != 1 || !strings.Contains(corrections[0], refusedReportScenarioName) {
		t.Fatalf("Test correction prompts = %d, want one that holds the refused report's scenario %q:\n%s",
			len(corrections), refusedReportScenarioName, strings.Join(corrections, "\n---\n"))
	}
	if runErr != nil || run.Status != types.RunCompleted {
		t.Fatalf("run status = %s (axi run error %v, run error %q), want completed with the refused report's go\n%s", run.Status, runErr, deref(run.Error), runOut)
	}

	step, ok := findStep(run.Steps, types.StepTest)
	if !ok || step.Status != types.StepStatusCompleted || step.FindingsJSON == nil {
		t.Fatalf("test step = %+v, want completed with recorded findings", step)
	}
	findings, err := types.ParseFindingsJSON(*step.FindingsJSON)
	if err != nil {
		t.Fatalf("parse Test findings: %v", err)
	}
	if findings.Verdict != types.TestVerdictGo || len(findings.Scenarios) != 1 || findings.Scenarios[0].Name != refusedReportScenarioName || !findings.Scenarios[0].Live {
		t.Fatalf("recorded Test report = verdict %q scenarios %+v, want the refused report's go and its one live scenario", findings.Verdict, findings.Scenarios)
	}
	note := ""
	for _, item := range findings.Items {
		if strings.HasPrefix(item.Description, "Test report rebuilt by a correction turn: ") {
			note = item.Description
			if item.Severity != types.FindingSeverityInfo || item.Action != types.ActionNoOp {
				t.Fatalf("rebuilt-report note = %+v, want severity info and action no-op", item)
			}
		}
	}
	if !strings.Contains(note, "the evidence turn's report was refused (") {
		t.Fatalf("Test findings carry no rebuilt-report note: %+v", findings.Items)
	}
}

// placeholderReportScenarioName is the one scenario the refused Test reports
// carry inside their summary string. The fake agent answers a correction
// prompt that holds it with the report rebuilt from them.
const placeholderReportScenarioName = "an importer types a word of a long question and finds its column"

// refusedThenStubbedScenario scripts the last Test evidence turn of run
// 01M6MYHR17WEVPM11RFY2J3CTC (T223): the agent submits its real report twice,
// and each time the tool call has swallowed the scenarios array into the
// summary string, so the CLI refuses it for the missing scenarios. The agent
// then sends the stub it recorded (scenario "x", evidence "e", tested "t",
// testing summary "t", verdict go), which the CLI accepts. Before the
// placeholder floor the gate's validator accepted it too, so the run recorded
// go with 1 of 1 scenarios live.
func refusedThenStubbedScenario(t *testing.T) string {
	t.Helper()
	scenarios, err := json.Marshal([]any{map[string]any{
		"name":     placeholderReportScenarioName,
		"result":   "pass",
		"live":     true,
		"evidence": "typing a word of the question's label narrowed the list to it and named the column that holds it",
		"reason":   "",
	}})
	if err != nil {
		t.Fatal(err)
	}
	refusedReport := map[string]any{
		"findings":        []any{},
		"summary":         "Every scenario driven live passed.</summary>\n<parameter name=\"scenarios\">" + string(scenarios),
		"verdict":         "go",
		"testing_summary": "Drove the import column search live against an isolated stack; it passed.",
		"artifacts":       []any{},
		"tested":          []string{"npx playwright test e2e/choices-at-scale.spec.ts"},
	}
	stub := map[string]any{
		"findings":        []any{},
		"summary":         "Test",
		"scenarios":       []any{map[string]any{"name": "x", "result": "pass", "live": true, "evidence": "e", "reason": ""}},
		"verdict":         "go",
		"tested":          []string{"t"},
		"testing_summary": "t",
		"artifacts":       []any{},
	}
	const session = "a3f0c0de-0000-4000-8000-000000000233"
	usage := map[string]int{"input_tokens": 10, "output_tokens": 5}
	call := func(id string, input map[string]any) string {
		return claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"model": "claude-opus-4-7", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": "StructuredOutput", "input": input}},
		}})
	}
	outcome := func(id, content string, isError bool) string {
		block := map[string]any{"type": "tool_result", "tool_use_id": id, "content": content}
		if isError {
			block["is_error"] = true
		}
		return claudeStreamLine(t, map[string]any{"type": "user", "session_id": session, "message": map[string]any{
			"role": "user", "content": []any{block},
		}})
	}
	stream := []string{
		claudeStreamLine(t, map[string]any{"type": "system", "subtype": "init", "session_id": session}),
		claudeStreamLine(t, map[string]any{"type": "assistant", "session_id": session, "message": map[string]any{
			"model": "claude-opus-4-7", "usage": usage,
			"content": []any{map[string]any{"type": "tool_use", "id": "toolu_drive", "name": "Bash", "input": map[string]any{
				"command": "npx playwright test e2e/choices-at-scale.spec.ts",
			}}},
		}}),
		outcome("toolu_drive", "15 passed", false),
	}
	for i := 1; i <= 2; i++ {
		id := "toolu_report" + string(rune('0'+i))
		stream = append(stream,
			call(id, refusedReport),
			outcome(id, "Output does not match required schema: root: must have required property 'scenarios'", true),
		)
	}
	stream = append(stream,
		call("toolu_stub", stub),
		outcome("toolu_stub", "Structured output provided successfully", false),
		claudeStreamLine(t, map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session, "structured_output": stub, "usage": usage}),
	)

	var b strings.Builder
	b.WriteString("actions:\n  - match: \"" + testEvidencePromptMarker + "\"\n    claude_stream:\n")
	for _, line := range stream {
		quoted, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString("      - " + string(quoted) + "\n")
	}
	b.WriteString(`  - match: "` + placeholderReportScenarioName + `"
    text: "rebuilt the report from the refused submission"
    structured:
      findings: []
      summary: "every scenario driven live passed"
      tested: ["npx playwright test e2e/choices-at-scale.spec.ts"]
      testing_summary: "Drove the import column search live against an isolated stack; it passed."
      artifacts: []
      scenarios:
        - name: "` + placeholderReportScenarioName + `"
          result: pass
          live: true
          evidence: "typing a word of the question's label narrowed the list to it and named the column that holds it"
          reason: ""
      verdict: go
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks detected in the diff"
      risk_scope: source-or-external
`)
	path := filepath.Join(t.TempDir(), "refused-then-stubbed-scenario.yaml")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

// TestPlaceholderTestReportIsRefusedJourney reproduces the last Test evidence
// turn of run 01M6MYHR17WEVPM11RFY2J3CTC (T223) through the stock
// `no-mistakes axi` surface. The CLI refused the agent's real report twice
// and then accepted a stub of one-letter placeholders, which the gate's
// validator accepted too, so the PR body and the attestation said 1 of 1
// scenarios live with the testing summary "t". The validator now refuses the
// stub by name, the correction turn rebuilds the report from the refused
// ones, and the recorded verdict is the correction's.
func TestPlaceholderTestReportIsRefusedJourney(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: refusedThenStubbedScenario(t)})
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	branch := "feature/placeholder-test-report"
	h.CommitChange(branch, ".no-mistakes.yaml", "commands:\n  lint: 'exit 0'\n", "configure lint")
	h.CommitChange(branch, "feature.txt", "value\n", "add feature")

	runOut, runErr := h.Run("axi", "run", "--intent", "validate the feature whose test agent submitted a stub", "--skip", "pr,ci")
	t.Logf("stock axi run surface:\n%s", runOut)
	run := h.WaitForRun(branch, 90*time.Second)

	step, ok := findStep(run.Steps, types.StepTest)
	if !ok || step.FindingsJSON == nil {
		t.Fatalf("test step = %+v, want recorded findings", step)
	}
	findings, err := types.ParseFindingsJSON(*step.FindingsJSON)
	if err != nil {
		t.Fatalf("parse Test findings: %v", err)
	}
	if findings.TestingSummary == "t" || len(findings.Scenarios) != 1 || findings.Scenarios[0].Name != placeholderReportScenarioName {
		t.Fatalf("recorded Test report = verdict %q testing summary %q scenarios %+v, want the correction's rebuild of the refused report, not the placeholder stub",
			findings.Verdict, findings.TestingSummary, findings.Scenarios)
	}

	logOut, err := h.Run("axi", "logs", "--step", "test", "--full")
	if err != nil {
		t.Fatalf("axi logs: %v\n%s", err, logOut)
	}
	for _, want := range []string{
		`scenario 1: name "x" is a placeholder - name what an end user does and the observable result, as a full phrase`,
		`scenario 1: evidence "e" is a placeholder - say what was observed that shows this result`,
		`testing summary "t" is a placeholder - describe what was exercised and the overall result in a sentence`,
		`tested array holds only placeholders - record the commands, tests or checks that were run`,
	} {
		// axi prints a log line holding quotes as a quoted TOON string.
		if !strings.Contains(logOut, want) && !strings.Contains(logOut, strings.ReplaceAll(want, `"`, `\"`)) {
			t.Fatalf("Test step log does not refuse the stub with %q:\n%s", want, logOut)
		}
	}

	var corrections []string
	for _, inv := range h.AgentInvocations() {
		if strings.Contains(inv.Prompt, testCorrectionPromptMarker) {
			corrections = append(corrections, inv.Prompt)
		}
	}
	if len(corrections) != 1 || !strings.Contains(corrections[0], placeholderReportScenarioName) {
		t.Fatalf("Test correction prompts = %d, want one that holds the refused reports' scenario %q:\n%s",
			len(corrections), placeholderReportScenarioName, strings.Join(corrections, "\n---\n"))
	}
	if runErr != nil || run.Status != types.RunCompleted || step.Status != types.StepStatusCompleted {
		t.Fatalf("run status = %s, test step %s (axi run error %v, run error %q), want completed with the correction's go\n%s",
			run.Status, step.Status, runErr, deref(run.Error), runOut)
	}
	if findings.Verdict != types.TestVerdictGo || !findings.Scenarios[0].Live {
		t.Fatalf("recorded Test verdict = %q scenarios %+v, want the correction's go with its live scenario", findings.Verdict, findings.Scenarios)
	}
}
