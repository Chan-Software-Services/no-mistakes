package steps

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
)

// These tests model the last Test evidence turn of run
// 01M6MYHR17WEVPM11RFY2J3CTC: after two refused reports the agent sent
// a stub of one-letter placeholders, which the CLI and the gate's validator
// both accepted, so the run recorded go with 1 of 1 scenarios live.

// recordedStubReport is the stub that run recorded, byte for byte.
const recordedStubReport = `{"findings": [], "summary": "Test", "scenarios": [{"name": "x", "result": "pass", "live": true, "evidence": "e", "reason": ""}], "verdict": "go", "tested": ["t"], "testing_summary": "t", "artifacts": []}`

// testReport builds a Test report whose fields all clear the placeholder
// floor, with the given fields replaced.
func testReport(t *testing.T, overrides map[string]any, scenario map[string]any) string {
	t.Helper()
	s := map[string]any{
		"name":     "a shopper pays and sees the receipt",
		"result":   "pass",
		"live":     true,
		"evidence": "the receipt screen showed the order number",
		"reason":   "",
	}
	for k, v := range scenario {
		s[k] = v
	}
	report := map[string]any{
		"findings":        []any{},
		"summary":         "",
		"tested":          []string{"npm run e2e -- checkout"},
		"testing_summary": "Drove checkout live; the receipt appeared.",
		"artifacts":       []any{},
		"scenarios":       []any{s},
		"verdict":         "go",
	}
	for k, v := range overrides {
		report[k] = v
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func validateTestReport(raw string) error {
	var findings Findings
	return unmarshalRequiredTestFindings([]byte(raw), &findings)
}

func TestUnmarshalRequiredTestFindings_RefusesTheRecordedStub(t *testing.T) {
	t.Parallel()
	err := validateTestReport(recordedStubReport)
	if err == nil {
		t.Fatal("the recorded stub was accepted, want it refused as placeholders")
	}
	got := strings.Split(err.Error(), "\n")
	want := []string{
		`tested array holds only placeholders - record the commands, tests or checks that were run`,
		`testing summary "t" is a placeholder - describe what was exercised and the overall result in a sentence`,
		`scenario 1: name "x" is a placeholder - name what an end user does and the observable result, as a full phrase`,
		`scenario 1: evidence "e" is a placeholder - say what was observed that shows this result`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("refusal =\n%s\nwant\n%s", err, strings.Join(want, "\n"))
	}
}

func TestUnmarshalRequiredTestFindings_RefusesEachPlaceholderFieldAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		overrides map[string]any
		scenario  map[string]any
		want      string
	}{
		{
			name:     "scenario name",
			scenario: map[string]any{"name": "Test"},
			want:     `scenario 1: name "Test" is a placeholder - name what an end user does and the observable result, as a full phrase`,
		},
		{
			name:     "pass evidence",
			scenario: map[string]any{"evidence": "it works"},
			want:     `scenario 1: evidence "it works" is a placeholder - say what was observed that shows this result`,
		},
		{
			name:      "fail evidence",
			overrides: map[string]any{"verdict": "no-go"},
			scenario:  map[string]any{"result": "fail", "evidence": "x"},
			want:      `scenario 1: evidence "x" is a placeholder - say what was observed that shows this result`,
		},
		{
			name:      "untested reason",
			overrides: map[string]any{"verdict": "inconclusive"},
			scenario:  map[string]any{"result": "untested", "live": false, "evidence": "", "reason": "n/a"},
			want:      `scenario 1: reason "n/a" is a placeholder - state what was tried to drive it live and why that cannot work`,
		},
		{
			name:      "no-surface reason",
			overrides: map[string]any{"verdict": "no-surface"},
			scenario:  map[string]any{"result": "untested", "live": false, "evidence": "", "reason": "docs"},
			want:      `scenario 1: reason "docs" is a placeholder - state what was tried to drive it live and why that cannot work`,
		},
		{
			name:      "testing summary",
			overrides: map[string]any{"testing_summary": "ok"},
			want:      `testing summary "ok" is a placeholder - describe what was exercised and the overall result in a sentence`,
		},
		{
			name:      "tested entries",
			overrides: map[string]any{"tested": []string{"t", "ok", " - "}},
			want:      `tested array holds only placeholders - record the commands, tests or checks that were run`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateTestReport(testReport(t, tc.overrides, tc.scenario))
			if err == nil || err.Error() != tc.want {
				t.Fatalf("refusal = %v, want exactly %q", err, tc.want)
			}
		})
	}
}

func TestUnmarshalRequiredTestFindings_PlaceholderFloorBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		overrides map[string]any
		scenario  map[string]any
		refused   bool
	}{
		{name: "seven letters in the name", scenario: map[string]any{"name": "pay now"}, refused: true},
		{name: "eight letters in the name", scenario: map[string]any{"name": "pay later"}},
		{name: "punctuation is not a letter", scenario: map[string]any{"name": "x.x.x.x.x.x.x"}, refused: true},
		{name: "digits count", scenario: map[string]any{"evidence": "12345678"}},
		{name: "seven digits", scenario: map[string]any{"evidence": "HTTP 201"}, refused: true},
		{name: "a sentence without spaces", scenario: map[string]any{"evidence": "用户付款看到收据"}},
		{name: "seven letters without spaces", scenario: map[string]any{"evidence": "用户付款看收据"}, refused: true},
		{name: "testing summary at eight", overrides: map[string]any{"testing_summary": "it passed"}},
		{name: "a tested entry of three", overrides: map[string]any{"tested": []string{"t", "npm"}}},
		{name: "tested entries of two", overrides: map[string]any{"tested": []string{"go", "ls"}}, refused: true},
		{name: "untested evidence is not floored", overrides: map[string]any{"verdict": "inconclusive"},
			scenario: map[string]any{"result": "untested", "live": false, "evidence": "", "reason": "no browser installed here"}},
		{name: "a pass's reason is not floored", scenario: map[string]any{"reason": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateTestReport(testReport(t, tc.overrides, tc.scenario))
			if tc.refused && (err == nil || !strings.Contains(err.Error(), "placeholder")) {
				t.Fatalf("err = %v, want a placeholder refusal", err)
			}
			if !tc.refused && err != nil {
				t.Fatalf("err = %v, want it accepted", err)
			}
		})
	}
}

func TestUnmarshalRequiredTestFindings_PlaceholderQuoteIsCut(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("-", 60) + "x"
	err := validateTestReport(testReport(t, nil, map[string]any{"name": long}))
	want := `scenario 1: name "` + strings.Repeat("-", 40) + `" is a placeholder`
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("refusal = %v, want the value cut to 40 characters: %s", err, want)
	}
}

func TestTestStep_PlaceholderReportIsCorrectedNotRecorded(t *testing.T) {
	t.Parallel()
	ag := scriptedTestAgent(reply(recordedStubReport), reply(passingScenarioFindingsJSON))
	findings, err := executeScriptedTestStep(t, ag)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(ag.calls) != 2 {
		t.Fatalf("agent calls = %d, want the evidence turn and one correction", len(ag.calls))
	}
	correction := ag.calls[1].Prompt
	for _, want := range []string{
		`scenario 1: name "x" is a placeholder`,
		"A placeholder (a single letter, or a word such as test or x) is not evidence and is never expanded into a claim.",
	} {
		if !strings.Contains(correction, want) {
			t.Fatalf("correction prompt missing %q:\n%s", want, correction)
		}
	}
	if findings.TestingSummary == "t" || len(findings.Scenarios) != 1 || findings.Scenarios[0].Name == "x" {
		t.Fatalf("recorded report = %+v, want the correction's", findings)
	}
}

func TestTestStep_CorrectionPromptNeverExpandsAPlaceholder(t *testing.T) {
	t.Parallel()
	const rule = "A placeholder (a single letter, or a word such as test or x) is not evidence and is never expanded into a claim."
	for name, refused := range map[string]bool{"without refused submissions": false, "with refused submissions": true} {
		var submissions []agent.RefusedOutput
		if refused {
			submissions = []agent.RefusedOutput{{Payload: refusedFullReport, Reason: refusedArtifactsReason}}
		}
		prompt := testAnalyzerCorrectionPrompt(errors.New(`scenario 1: name "x" is a placeholder`), []byte(recordedStubReport), submissions)
		if strings.Count(prompt, rule) != 1 {
			t.Fatalf("%s: correction prompt holds the placeholder rule %d times, want once:\n%s", name, strings.Count(prompt, rule), prompt)
		}
	}
}

var submitOnceContract = []string{
	"- Submit the report once, when everything else is finished. The first report the output tool accepts ends your turn and is recorded as your verdict; never submit a trial, partial or placeholder report to see whether it is accepted.",
	"- If the output tool refuses the report, its message names the missing or wrong field. Resubmit the same complete report with only that corrected: its top-level fields are findings, summary, tested, testing_summary, scenarios and verdict, plus artifacts when you have any, and each belongs at the top level, never inside another field's text.",
	"- Before you submit, stop every background command, watch or monitor you started: nothing you started may still be running when you submit.",
}

func TestTestStep_EvidencePromptStatesTheSubmitOnceContract(t *testing.T) {
	t.Parallel()
	ag := scriptedTestAgent(reply(passingScenarioFindingsJSON))
	if _, err := executeScriptedTestStep(t, ag); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	prompt := ag.calls[0].Prompt
	_, rules, ok := strings.Cut(prompt, "\nRules:\n")
	if !ok {
		t.Fatalf("evidence prompt has no Rules block:\n%s", prompt)
	}
	last := -1
	for _, line := range submitOnceContract {
		at := strings.Index(rules, line+"\n")
		if at < 0 {
			t.Fatalf("Rules block missing %q:\n%s", line, rules)
		}
		if at < last {
			t.Fatalf("Rules block holds %q out of order:\n%s", line, rules)
		}
		last = at
	}
	for _, appended := range []string{"-----BEGIN USER INTENT-----", "Agent memory files (AGENTS.md and CLAUDE.md)"} {
		if at := strings.Index(rules, appended); at < last {
			t.Fatalf("the contract must come before the appended %q section:\n%s", appended, rules)
		}
	}
}

func TestTestFindingsSchema_DescribesWhatEachFieldWants(t *testing.T) {
	t.Parallel()
	var parsed struct {
		Properties map[string]map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(testFindingsSchema, &parsed); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]string{
		"tested":          "the exact commands, tests and checks that were run; at least one real entry, never a placeholder",
		"testing_summary": "one or more sentences on what was exercised and the overall result, never a placeholder",
		"artifacts":       "optional: reviewer-visible evidence artifacts; empty or absent when there are none",
	} {
		var got string
		if err := json.Unmarshal(parsed.Properties[field]["description"], &got); err != nil || got != want {
			t.Fatalf("%s description = %q (%v), want %q", field, got, err, want)
		}
	}
	for field, keys := range map[string][]string{
		"tested":          {"type", "items", "description"},
		"testing_summary": {"type", "description"},
		"artifacts":       {"type", "items", "description"},
	} {
		if len(parsed.Properties[field]) != len(keys) {
			t.Fatalf("%s keywords = %v, want only %v", field, parsed.Properties[field], keys)
		}
		for _, key := range keys {
			if _, ok := parsed.Properties[field][key]; !ok {
				t.Fatalf("%s keywords = %v, want only %v", field, parsed.Properties[field], keys)
			}
		}
	}
}

// A report whose scenarios array or an artifact's label is missing used to
// return that one refusal and drop the placeholder issues gathered before it,
// so the correction turn spent an attempt finding the stub summary.
func TestUnmarshalRequiredTestFindings_AnEarlyRefusalStillNamesThePlaceholders(t *testing.T) {
	t.Parallel()
	const summaryIssue = `testing summary "x" is a placeholder - describe what was exercised and the overall result in a sentence`
	for _, tc := range []struct {
		name      string
		overrides map[string]any
		last      string
	}{
		{"missing scenarios", map[string]any{"scenarios": nil}, "missing scenarios array"},
		{"empty scenarios", map[string]any{"scenarios": []any{}}, "empty scenarios array - report at least one named scenario; if nothing could be driven, return it as untested with a reason"},
		{"artifact without a label", map[string]any{"artifacts": []any{map[string]any{"kind": "log", "content": "the receipt log"}}}, "artifact 0 missing label"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			overrides := map[string]any{"testing_summary": "x"}
			for k, v := range tc.overrides {
				overrides[k] = v
			}
			err := validateTestReport(testReport(t, overrides, nil))
			if err == nil {
				t.Fatal("the report was accepted, want it refused")
			}
			if want := summaryIssue + "\n" + tc.last; err.Error() != want {
				t.Fatalf("refusal =\n%s\nwant\n%s", err, want)
			}
		})
	}
}
