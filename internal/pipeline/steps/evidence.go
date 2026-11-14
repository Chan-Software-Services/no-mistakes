package steps

import (
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/evidence"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

// testEvidenceDir is where the test step writes a run's evidence artifacts.
//
// Evidence is always collected OUTSIDE the worktree, keyed by run ID, so a
// pipeline run can never commit artifacts into the branch it is validating.
// Publishing them (opt-in, see config.Evidence) copies the directory onto the
// repository's orphan evidence branch instead, which is what keeps screenshots
// and logs out of the default branch's history while still linking them from
// the PR.
//
// The path itself is resolved once by the executor (see
// pipeline.StepContext.EvidenceDir) and read from the step context here. Steps
// must never rebuild it from os.TempDir(): on Linux the daemon's TMPDIR is
// unset, so that resolved to the shared /tmp - a fixed name nobody reaped, on
// a filesystem current Ubuntu backs with RAM.
func testEvidenceDir(sctx *pipeline.StepContext) string {
	if sctx == nil {
		return ""
	}
	return sctx.EvidenceDir
}

// testScratchDir is this run's test scratch directory
// (evidence.ScratchDirName), or "" when the run has no evidence directory.
func testScratchDir(sctx *pipeline.StepContext) string {
	dir := testEvidenceDir(sctx)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, evidence.ScratchDirName)
}

// testRunToken is the short, lowercase token the Test prompt asks the agent
// to put in every disposable resource's name, so two runs at once never share
// a container, compose project, or database by construction rather than by
// the agent's choice. It is the last eight letters and digits of the run ID:
// a run ID is a ULID, whose leading characters are the start time that two
// runs started together share, while its tail is random (incremented within
// one millisecond, so successive IDs still differ there).
func testRunToken(sctx *pipeline.StepContext) string {
	if sctx == nil || sctx.Run == nil {
		return ""
	}
	var token []rune
	for _, r := range strings.ToLower(sctx.Run.ID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			token = append(token, r)
		}
	}
	if len(token) > 8 {
		token = token[len(token)-8:]
	}
	return string(token)
}

// evidenceBranchSlug turns a branch name into readable, filesystem-safe path
// segments used as the run's directory on the evidence branch. Branch
// separators are preserved as nested directories; unsafe characters are
// replaced with dashes and traversal segments are dropped.
func evidenceBranchSlug(branch string) []string {
	var segments []string
	for _, raw := range strings.Split(branch, "/") {
		seg := sanitizeEvidenceSegment(raw)
		if seg == "" || seg == "." || seg == ".." {
			continue
		}
		segments = append(segments, seg)
	}
	return segments
}

// sanitizeEvidenceSegment keeps alphanumerics, dash, underscore, and dot,
// replacing every other rune with a dash, then collapses dash runs and trims
// leading/trailing dashes.
func sanitizeEvidenceSegment(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-")
}
