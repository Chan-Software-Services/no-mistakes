package steps

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// projectToolRule is the one instruction every agent that may run a
// formatter or linter receives (the lint step's own pass, the combined
// document+lint pass, and the document-only pass). A tool run from the
// repository root does not find a configuration that sits in a subdirectory
// and checks or rewrites files with the tool's defaults, which then fails the
// project's own check.
const projectToolRule = `- Run a formatter or linter only the way the project's own scripts and CI run it: find the command in its package scripts, Makefile, task runner, or CI workflow, and run it from the directory it runs from, with the configuration it passes. A tool's configuration may sit in a subdirectory (for example front-end/.prettierrc.json) and still cover files elsewhere in the repository, which the tool does not find when run from the repository root. Never run a formatter or linter with its default settings when the project configures it; when you cannot find how the project runs a configured tool, pass that configuration explicitly.`

// lintScopeRule bounds the agent-driven lint duty to the change. The step
// enforces the reporting half itself (scopeLintFindingsToChange); the prompt
// carries it so the agent neither rewrites nor reports untouched files.
const lintScopeRule = `- Check, fix, and report only the files changed between the base commit and the target commit. Do not reformat or edit any other file.
- A problem in a file this change does not touch predates the change: report it at most as an "info" finding with action "no-op", never as an error or a warning.`

// lintOutOfChangePrefix marks a finding scopeLintFindingsToChange demoted.
const lintOutOfChangePrefix = "In a file outside this change, so not a gate on it: "

// lintChangedPaths returns the complete set of files changed between baseSHA
// and the worktree's HEAD, which includes any fix commits the lint duty made.
func lintChangedPaths(ctx context.Context, workDir, baseSHA string) ([]string, error) {
	out, err := git.Run(ctx, workDir, "diff", "--name-only", "-z", "--no-renames", baseSHA, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("get changed files for lint scope: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	return changedPathList(out), nil
}

// scopeLintFindingsToChange demotes each blocking lint finding whose file is
// outside the change to an informational no-op finding, so a problem the
// change did not introduce is kept on the record but never parks the run.
// A finding with no file, or on a directory that holds a changed file, stays
// as reported: the step cannot tell it is outside the change, so it fails
// toward parking. It returns how many findings it demoted.
func scopeLintFindingsToChange(findings Findings, changed []string, workDir string) (Findings, int) {
	inChange := make(map[string]bool, len(changed))
	for _, file := range changed {
		if p := normalizeReviewedPath(filepath.ToSlash(file)); p != "" {
			inChange[p] = true
		}
	}
	demoted := 0
	items := make([]Finding, len(findings.Items))
	for i, f := range findings.Items {
		items[i] = f
		if f.Severity != "error" && f.Severity != "warning" {
			continue
		}
		file, known := lintFindingPath(f.File, workDir)
		if !known || lintPathTouchesChange(file, inChange) {
			continue
		}
		items[i].Severity = "info"
		items[i].Action = types.ActionNoOp
		items[i].Description = lintOutOfChangePrefix + f.Description
		demoted++
	}
	findings.Items = items
	return findings, demoted
}

// lintFindingPath normalizes a finding's file to a repository-relative slash
// path. It reports false when the finding names no file or names one the step
// cannot place inside the worktree.
func lintFindingPath(file, workDir string) (string, bool) {
	file = strings.TrimSpace(file)
	if file == "" {
		return "", false
	}
	if filepath.IsAbs(file) {
		rel, err := filepath.Rel(workDir, file)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", false
		}
		file = rel
	}
	p := normalizeReviewedPath(filepath.ToSlash(file))
	if p == "" {
		return "", false
	}
	return p, true
}

func lintPathTouchesChange(file string, inChange map[string]bool) bool {
	if inChange[file] {
		return true
	}
	dir := file + "/"
	for changed := range inChange {
		if strings.HasPrefix(changed, dir) {
			return true
		}
	}
	return false
}

// scopeLintOutcomeFindings applies scopeLintFindingsToChange to a lint
// result against the change between baseSHA and the worktree's HEAD, logging
// what it demoted. It returns how many findings it demoted.
func scopeLintOutcomeFindings(sctx *pipeline.StepContext, baseSHA string, findings Findings) (Findings, int, error) {
	changed, err := lintChangedPaths(sctx.Ctx, sctx.WorkDir, baseSHA)
	if err != nil {
		return findings, 0, err
	}
	scoped, demoted := scopeLintFindingsToChange(findings, changed, sctx.WorkDir)
	if demoted > 0 {
		sctx.Log(fmt.Sprintf("lint: %d finding(s) on files outside this change kept as information only", demoted))
	}
	return scoped, demoted, nil
}
