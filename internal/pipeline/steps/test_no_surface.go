package steps

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// The Test step decides a "no-surface" verdict itself only when two
// independent signals agree: the evidence agent found no live-validatable
// surface, and every file the run could publish matches the repository's
// trusted test.no_surface_paths. Either signal alone parks as before, so a
// glob written too broadly cannot skip a real surface and an agent wrongly
// claiming no surface on code still gets a human look.

// noSurfacePathListLimit bounds how many paths a finding names before it
// summarises the rest as " and <k> more".
const noSurfacePathListLimit = 5

// noSurfaceDecision is the outcome of matching a run's changed files against
// the trusted test.no_surface_paths. The zero value means the rules are not
// configured (or the verdict is not no-surface), which leaves today's park
// byte-for-byte unchanged.
type noSurfaceDecision struct {
	// configured says rules were set and the decision was computed.
	configured bool
	// skip says every changed file is covered, so the gate records
	// test-no-surface-skip instead of parking.
	skip bool
	// covered and uncovered partition the changed files, in first-seen order.
	covered   []string
	uncovered []string
	// unconfirmed says the changed files could not be read or the set was
	// empty: never read as "every file is covered".
	unconfirmed bool
}

// noSurfaceChangedPaths lists every file the run could publish: the union,
// deduplicated in first-seen order, of the tracked changes against baseSHA in
// the working tree (every commit past the base and every uncommitted edit,
// with both paths of a rename and the path of a deletion) and every untracked,
// non-ignored file. It is the complete set, never the ignore_patterns-filtered
// one, since ignore_patterns is a pushed-branch field and must not decide a
// trusted rule.
func noSurfaceChangedPaths(ctx context.Context, workDir, baseSHA string) ([]string, error) {
	if strings.TrimSpace(baseSHA) == "" {
		return nil, errors.New("list changed files: no branch base")
	}
	tracked, err := git.RunRaw(ctx, workDir, "diff", "--name-only", "-z", "--no-renames", baseSHA, "--")
	if err != nil {
		return nil, fmt.Errorf("list changed files against %s: %w", baseSHA, err)
	}
	untracked, err := git.RunRaw(ctx, workDir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("list untracked files: %w", err)
	}
	var paths []string
	seen := map[string]bool{}
	for _, name := range strings.Split(string(tracked)+string(untracked), "\x00") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		paths = append(paths, name)
	}
	return paths, nil
}

// decideNoSurface matches changed against rules with the ignore_patterns
// matcher. A read error or an empty set is unconfirmed, so the step parks.
func decideNoSurface(changed []string, readErr error, rules []string) noSurfaceDecision {
	if len(rules) == 0 {
		return noSurfaceDecision{}
	}
	decision := noSurfaceDecision{configured: true}
	if readErr != nil || len(changed) == 0 {
		decision.unconfirmed = true
		return decision
	}
	for _, file := range changed {
		if noSurfacePathCovered(file, rules) {
			decision.covered = append(decision.covered, file)
		} else {
			decision.uncovered = append(decision.uncovered, file)
		}
	}
	decision.skip = len(decision.uncovered) == 0
	return decision
}

func noSurfacePathCovered(file string, rules []string) bool {
	for _, rule := range rules {
		if matchIgnorePattern(file, rule) {
			return true
		}
	}
	return false
}

// noSurfaceSkipFinding is what the gate records in place of the no-surface
// park: an info finding carrying the matching files and the agent's own
// reason for each untested scenario.
func noSurfaceSkipFinding(decision noSurfaceDecision, coverage string, scenarios []types.TestScenario) Finding {
	return Finding{
		ID:       types.FindingIDTestNoSurfaceSkip,
		Severity: types.FindingSeverityInfo,
		Action:   types.ActionNoOp,
		Description: fmt.Sprintf(
			"no live surface, skipped: every changed file matches the trusted test.no_surface_paths (%d file(s): %s) and the evidence agent found no live-validatable surface (%s)%s",
			len(decision.covered), boundedPathList(decision.covered), coverage, untestedScenarioReasonSuffix(scenarios),
		),
	}
}

// noSurfaceSkipLogLine is the step log's one line for a gate-recorded skip.
func noSurfaceSkipLogLine(decision noSurfaceDecision) string {
	return fmt.Sprintf("no live surface, skipped: every changed file matches the trusted test.no_surface_paths (%d file(s))", len(decision.covered))
}

// noSurfaceParkSuffix ends the no-surface park when rules are configured, so
// the operator sees why the gate did not decide it: the uncovered files, or
// that the changed files could not be confirmed. With rules unset it is
// empty, leaving today's park text unchanged.
func noSurfaceParkSuffix(decision noSurfaceDecision) string {
	switch {
	case !decision.configured || decision.skip:
		return ""
	case decision.unconfirmed:
		return "; the gate could not confirm every changed file is on the trusted test.no_surface_paths, so it did not decide this itself"
	default:
		return "; not on the trusted test.no_surface_paths: " + boundedPathList(decision.uncovered)
	}
}

// boundedPathList names the first noSurfacePathListLimit paths and
// summarises the rest.
func boundedPathList(paths []string) string {
	if len(paths) <= noSurfacePathListLimit {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:noSurfacePathListLimit], ", "), len(paths)-noSurfacePathListLimit)
}
