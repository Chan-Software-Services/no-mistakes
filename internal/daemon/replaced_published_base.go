package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

	"github.com/kunchenguid/no-mistakes/internal/custody"
	"github.com/kunchenguid/no-mistakes/internal/git"
)

// replacedPublishedBase returns the base for a run that starts after an
// operator replaced the branch's published head with the local history
// (`no-mistakes sync --replace-published`). That command moves the gate branch
// to the local head, so the next submission is a no-op push or a fast-forward
// of the gate and its natural base would be the operator's own head. But the
// run rewrites the published head, and the Rebase step reads a base that the
// head does not contain as the operator's force push: the pushed commit is
// authoritative and the remote branch it replaces is not merged back in.
// So the base is the replaced published head, exactly as a reconciled
// branch's archived previous head is (HandlePushReceived).
//
// The evidence is the gate's own and the database's, never the request's: the
// branch's latest run has returned custody while its head is its own push
// binding, which only the replacement produces, and the gate pins that
// published head at the run's recovery ref. A head that already contains the
// published head keeps its base. The push step's force-push decision is
// unchanged and still refuses a remote that moved after the published head.
//
// It fails closed, as every custody decision does: when the runs cannot be
// read, or the replacement's evidence is present and the ancestry check cannot
// answer, it returns an error and the run does not start, because the natural
// base would have the Rebase step merge the replaced published head back in.
func (m *RunManager) replacedPublishedBase(ctx context.Context, repoID, branch, headSHA, baseSHA string) (string, error) {
	runs, err := m.db.GetRunsByRepo(repoID)
	if err != nil {
		return "", fmt.Errorf("read runs to check for a replaced published head: %w", err)
	}
	for _, run := range runs {
		if run.Branch != branch {
			continue
		}
		if run.CustodyReturnedAt == nil || run.LastPushedSHA == nil || run.HeadSHA != *run.LastPushedSHA {
			return baseSHA, nil
		}
		published := *run.LastPushedSHA
		gateDir := m.paths.RepoDir(repoID)
		if !custody.PublishedHeadPinned(ctx, gateDir, run.ID, published) {
			return baseSHA, nil
		}
		_, err := git.Run(ctx, gateDir, "merge-base", "--is-ancestor", published, headSHA)
		if err == nil {
			return baseSHA, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return published, nil
		}
		return "", fmt.Errorf("check whether head %s contains the replaced published head %s: %w", headSHA, published, err)
	}
	return baseSHA, nil
}
