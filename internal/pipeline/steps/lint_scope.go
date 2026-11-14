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
// project's own check. Its fallback stays inside what that configuration
// covers, so it never raises a finding the project's own command would not.
const projectToolRule = `- Run a formatter or linter only the way the project's own scripts and CI run it: find the command in its package scripts, Makefile, task runner, or CI workflow, and run it from the directory it runs from, with the configuration it passes. A tool's configuration may sit in a subdirectory (for example front-end/.prettierrc.json) and still cover files elsewhere in the repository, which the tool does not find when run from the repository root. Never run a formatter or linter with its default settings when the project configures it, and never run it on a file the project's own command for it does not check. When you cannot find how the project runs a configured tool, pass that configuration explicitly, and only on files under the configuration's directory.`

// lintFindingPathRule asks for the one path spelling scopeLintFindingsToChange
// can match against the change.
const lintFindingPathRule = `- Give each finding's "file" as a path relative to the repository root, even when you ran the tool from a subdirectory.`

// lintUntouchedFileRule is the reporting half of the lint duty's scope. The
// step enforces it itself (scopeLintFindingsToChange); the prompt carries it
// so the agent does not report untouched files as gates.
const lintUntouchedFileRule = `- A problem in a file this change does not touch predates the change: report it at most as an "info" finding with action "no-op", never as an error or a warning.`

// lintScopeRule bounds the lint step's own agent pass to the change.
const lintScopeRule = `- Check, fix, and report only the files changed between the base commit and the target commit. Do not reformat or edit any other file.
` + lintUntouchedFileRule + `
` + lintFindingPathRule

// housekeepingLintScopeRule bounds the combined pass's lint duty. The same
// turn's document duty edits docs outside base..target, so the lint duty
// checks those too, and only its own actions are kept off other files.
const housekeepingLintScopeRule = `- Check and fix the files changed between the base commit and the target commit, plus every file this pass edits for the documentation duty. Lint fixes must not reformat or edit any other file.
` + lintUntouchedFileRule + `
` + lintFindingPathRule

// lintOutOfChangePrefix marks a finding scopeLintFindingsToChange demoted.
const lintOutOfChangePrefix = "In a file outside this change, so not a gate on it: "

// lintScopePaths returns the complete set of files changed between baseSHA
// and the worktree's HEAD, which includes any fix commits the lint duty made,
// and the files tracked at HEAD.
func lintScopePaths(ctx context.Context, workDir, baseSHA string) (changed, tracked []string, err error) {
	out, err := git.Run(ctx, workDir, "diff", "--name-only", "-z", "--no-renames", baseSHA, "HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("get changed files for lint scope: %w", err)
	}
	if out != "" {
		changed = changedPathList(out)
	}
	out, err = git.Run(ctx, workDir, "ls-tree", "-r", "-z", "--name-only", "--full-tree", "HEAD")
	if err != nil {
		return nil, nil, fmt.Errorf("list tracked files for lint scope: %w", err)
	}
	if out != "" {
		tracked = changedPathList(out)
	}
	return changed, tracked, nil
}

// scopeLintFindingsToChange demotes each blocking lint finding that names a
// file tracked at HEAD and outside the change to an informational no-op
// finding, so a problem the change did not introduce is kept on the record
// but never parks the run. Every other finding stays as reported: with no
// file, or a path that is not a tracked file as written (a directory, a path
// relative to a subdirectory, a bare file name, a file:line spelling), the
// step cannot tell it is outside the change, so it fails toward parking. It
// returns how many findings it demoted.
func scopeLintFindingsToChange(findings Findings, changed, tracked []string, workDir string) (Findings, int) {
	inChange := normalizedPathSet(changed)
	atHead := normalizedPathSet(tracked)
	demoted := 0
	items := make([]Finding, len(findings.Items))
	for i, f := range findings.Items {
		items[i] = f
		if !isBlockingFinding(f) {
			continue
		}
		file, known := lintFindingPath(f.File, workDir)
		if !known || !atHead[file] || inChange[file] {
			continue
		}
		items[i].Severity = types.FindingSeverityInfo
		items[i].Action = types.ActionNoOp
		items[i].Description = lintOutOfChangePrefix + f.Description
		demoted++
	}
	findings.Items = items
	return findings, demoted
}

func normalizedPathSet(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, file := range paths {
		if p := normalizeReviewedPath(filepath.ToSlash(file)); p != "" {
			set[p] = true
		}
	}
	return set
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
		if err != nil {
			return "", false
		}
		file = rel
	}
	p := normalizeReviewedPath(filepath.ToSlash(file))
	if p == "" || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// scopeLintOutcomeFindings applies scopeLintFindingsToChange to a lint
// result against the change between baseSHA and the worktree's HEAD, logging
// what it demoted. It returns how many findings it demoted.
func scopeLintOutcomeFindings(sctx *pipeline.StepContext, baseSHA string, findings Findings) (Findings, int, error) {
	changed, tracked, err := lintScopePaths(sctx.Ctx, sctx.WorkDir, baseSHA)
	if err != nil {
		return findings, 0, err
	}
	scoped, demoted := scopeLintFindingsToChange(findings, changed, tracked, sctx.WorkDir)
	if demoted > 0 {
		sctx.Log(fmt.Sprintf("lint: %d finding(s) on files outside this change kept as information only", demoted))
	}
	return scoped, demoted, nil
}
