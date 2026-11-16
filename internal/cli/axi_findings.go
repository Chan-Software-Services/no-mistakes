package cli

import (
	"fmt"
	"strconv"
	"strings"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/types"
	"github.com/spf13/cobra"
)

// fullFindingRow is one recorded finding as `axi findings` prints it: every
// field the gate shows plus the line, and the description never cut.
type fullFindingRow struct {
	ID          string `toon:"id"`
	Severity    string `toon:"severity"`
	File        string `toon:"file"`
	Line        string `toon:"line"`
	Action      string `toon:"action"`
	Description string `toon:"description"`
}

// scenarioRow is one recorded Test scenario, nothing cut.
type scenarioRow struct {
	Name     string `toon:"name"`
	Result   string `toon:"result"`
	Live     bool   `toon:"live"`
	Evidence string `toon:"evidence"`
	Reason   string `toon:"reason"`
}

func newAxiFindingsCmd() *cobra.Command {
	var step, runID string
	cmd := &cobra.Command{
		Use:           "findings",
		Short:         "Show one pipeline step's recorded findings whole",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAxiFindings(cmd, step, runID)
		},
	}
	cmd.Flags().StringVar(&step, "step", "", "step name: intent, rebase, review, test, document, lint, push, pr, ci, or a repository gate step name (required)")
	cmd.Flags().StringVar(&runID, "run", "", "run ID (default: current branch's active or most recent)")
	return cmd
}

// runAxiFindings prints a step's recorded findings with nothing cut, for a
// worker that may not read the gate's directory: the gate's own output cuts a
// summary at maxGateSummary and a description at maxFindingDesc. It resolves
// its run exactly as `axi logs` does and reads the step's current
// findings_json from the database, as status does. It is a read-only query:
// it does not emit telemetry, and it is not pipeline control.
func runAxiFindings(cmd *cobra.Command, step, runID string) error {
	step = strings.TrimSpace(step)
	if step == "" {
		return emitError(cmd, 2, "--step is required", validLogStepsHelp)
	}
	if !validReadableStep(types.StepName(step)) {
		return emitError(cmd, 2, fmt.Sprintf("unknown step %q", step), validLogStepsHelp)
	}

	env, err := openAxiQueryEnv(runID)
	if err != nil {
		return emitError(cmd, 1, err.Error(), repoInitHelp(err)...)
	}
	defer env.close()

	branch, branchErr := currentBranchForRunResolve(cmd.Context())
	if branchErr != nil && runID == "" {
		return emitError(cmd, 1, branchErr.Error())
	}
	run, _, err := resolveRun(env, runID, branch)
	if err != nil {
		return emitError(cmd, 1, err.Error())
	}
	if run == nil {
		if runID != "" {
			return emitError(cmd, 1, fmt.Sprintf("run %q not found", runID))
		}
		help := []string{
			startRunHelp(),
			"To read another branch's run, name it: `no-mistakes axi findings --run <id> --step <step>`",
		}
		if branch == "" {
			help = []string{"This worktree has no current branch (detached HEAD), so no run can be attributed to it; inspect a specific run with `no-mistakes axi findings --run <id> --step <step>`, or check out a branch first"}
		}
		return emitError(cmd, 1, "no run found for this branch to read findings from", help...)
	}
	stepResults, err := env.d.GetStepsByRun(run.ID)
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("load steps: %v", err))
	}

	fields := []toon.Field{
		{Key: "step", Value: step},
		{Key: "run", Value: run.ID},
	}
	noFindings := toon.Field{Key: "findings", Value: fmt.Sprintf("no findings recorded for step %q in this run", step)}
	var findingsJSON string
	recorded := false
	for _, sr := range stepResults {
		if string(sr.StepName) != step {
			continue
		}
		recorded = true
		fields = append(fields, toon.Field{Key: "status", Value: string(sr.Status)})
		if sr.FindingsJSON != nil {
			findingsJSON = *sr.FindingsJSON
		}
		break
	}
	if !recorded {
		// The run has no row for this step (a gate step this run does not
		// have, or a run that never reached it): there is no status to report.
		fields = append(fields, toon.Field{Key: "status", Value: "not recorded"}, noFindings)
		emitDoc(cmd, fields...)
		return nil
	}
	if strings.TrimSpace(findingsJSON) == "" {
		emitDoc(cmd, append(fields, noFindings)...)
		return nil
	}
	parsed, err := types.ParseFindingsJSON(findingsJSON)
	if err != nil {
		return emitError(cmd, 1, fmt.Sprintf("read recorded findings: %v", err))
	}

	if parsed.Summary != "" {
		fields = append(fields, toon.Field{Key: "summary", Value: parsed.Summary})
	}
	if parsed.RiskLevel != "" {
		fields = append(fields, toon.Field{Key: "risk", Value: parsed.RiskLevel})
	}
	if parsed.RiskRationale != "" {
		fields = append(fields, toon.Field{Key: "risk_rationale", Value: parsed.RiskRationale})
	}
	if parsed.Verdict != "" {
		fields = append(fields, toon.Field{Key: "verdict", Value: parsed.Verdict})
	}
	if parsed.TestingSummary != "" {
		fields = append(fields, toon.Field{Key: "testing_summary", Value: parsed.TestingSummary})
	}
	if len(parsed.Tested) > 0 {
		fields = append(fields, toon.Field{Key: "tested", Value: parsed.Tested})
	}
	if len(parsed.Scenarios) > 0 {
		rows := make([]scenarioRow, 0, len(parsed.Scenarios))
		for _, s := range parsed.Scenarios {
			rows = append(rows, scenarioRow{Name: s.Name, Result: s.Result, Live: s.Live, Evidence: s.Evidence, Reason: s.Reason})
		}
		fields = append(fields, toon.Field{Key: "scenarios", Value: rows})
	}
	if len(parsed.Items) == 0 {
		emitDoc(cmd, append(fields, noFindings)...)
		return nil
	}
	rows := make([]fullFindingRow, 0, len(parsed.Items))
	for _, f := range parsed.Items {
		line := ""
		if f.Line > 0 {
			line = strconv.Itoa(f.Line)
		}
		rows = append(rows, fullFindingRow{
			ID:          f.ID,
			Severity:    f.Severity,
			File:        f.File,
			Line:        line,
			Action:      f.Action,
			Description: f.Description,
		})
	}
	emitDoc(cmd, append(fields, toon.Field{Key: "findings", Value: rows})...)
	return nil
}

// axiFindingsCommand is the `axi findings` call for step, scoped to runID when
// the caller selected a run explicitly.
func axiFindingsCommand(step, runID string) string {
	if runID != "" {
		return fmt.Sprintf("no-mistakes axi findings --run %s --step %s", runID, step)
	}
	return fmt.Sprintf("no-mistakes axi findings --step %s", step)
}

// gateCutSomething reports whether the gate's rendering of findingsJSON cut
// its summary or any finding description.
func gateCutSomething(findingsJSON string) bool {
	parsed, err := types.ParseFindingsJSON(findingsJSON)
	if err != nil {
		return false
	}
	if len([]rune(parsed.Summary)) > maxGateSummary {
		return true
	}
	for _, f := range parsed.Items {
		if len([]rune(f.Description)) > maxFindingDesc {
			return true
		}
	}
	return false
}

// gateLogHelp is the gate help's closing pointers: the `axi findings` line
// when the gate cut something, then the full-log line.
func gateLogHelp(gate stepView, runID string) []string {
	var help []string
	if gateCutSomething(gate.FindingsJSON) {
		help = append(help, fmt.Sprintf("Run `%s` to read every finding whole", axiFindingsCommand(gate.Name, runID)))
	}
	return append(help, fmt.Sprintf("Run `%s` to read the full step log", axiLogsFullCommand(gate.Name, runID)))
}
