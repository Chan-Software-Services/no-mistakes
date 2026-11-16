package branchsync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/custody"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/git"
)

// ReplacePublishedOperatorNote is the sentence inspection adds at the
// published-head dead end. It names the replacement in words only: no
// next_action ever carries it, because replacing published commits is a
// person's decision and an agent that follows status must stop and ask.
const ReplacePublishedOperatorNote = "Replacing the published head with this local history is the operator's decision: a person runs `no-mistakes sync --replace-published`. Do not run it as an agent; stop and ask."

// maxReplacedCommitsListed bounds the confirmation's list of published commits
// the local history lacks; the rest is reported as a count.
const maxReplacedCommitsListed = 10

// ReplacedCommit is one published commit that the local history lacks.
type ReplacedCommit struct {
	ShortSHA string
	Subject  string
}

// ReplacePublishedPlan is what a person reads before confirming the
// replacement: the published commits that leave the branch.
type ReplacePublishedPlan struct {
	Branch        string
	PublishedHead string
	LocalHead     string
	// LocallyAvailable is false when the published head is not in the local
	// object store, so the lacking commits cannot be listed from here.
	LocallyAvailable bool
	Commits          []ReplacedCommit
	MoreCommits      int
}

// PlanReplacePublished runs the local proofs of ReplacePublished and, when they
// hold, lists the published commits the local history lacks. It reads only:
// no ref, file, or database row changes, and the remote is not contacted, so
// a refusal here is the same refusal ReplacePublished would give before its
// network check.
func (s *Service) PlanReplacePublished(ctx context.Context) (ReplacePublishedPlan, State, bool) {
	if refusal, blocked := s.gateContextRefusal(ctx); blocked {
		return ReplacePublishedPlan{}, refusal, false
	}
	state, run, _ := s.inspect(ctx)
	if blocked, refused := s.replacePublishedLocalProofs(ctx, state, run); refused {
		return ReplacePublishedPlan{}, blocked, false
	}
	published := ptr(run.LastPushedSHA)
	plan := ReplacePublishedPlan{Branch: state.Local.Branch, PublishedHead: published, LocalHead: state.Local.Head}
	if !objectExists(ctx, s.workDir(), published) {
		return plan, state, true
	}
	plan.LocallyAvailable = true
	lacking, err := revList(ctx, s.workDir(), "rev-list", published, "^"+state.Local.Head)
	if err != nil {
		plan.LocallyAvailable = false
		return plan, state, true
	}
	for i, sha := range lacking {
		if i == maxReplacedCommitsListed {
			plan.MoreCommits = len(lacking) - maxReplacedCommitsListed
			break
		}
		line, err := git.Run(ctx, s.workDir(), "show", "-s", "--format=%h %s", sha)
		if err != nil {
			plan.Commits = append(plan.Commits, ReplacedCommit{ShortSHA: shortCommit(sha)})
			continue
		}
		short, subject, _ := strings.Cut(line, " ")
		plan.Commits = append(plan.Commits, ReplacedCommit{ShortSHA: short, Subject: subject})
	}
	return plan, state, true
}

// ReplacePublished makes the gate follow the clean local head in place of a
// finished run's published head that the local history does not contain, so
// the next run publishes that history over the remote branch through the
// existing guarded force push. It is a person's explicit decision, never an
// agent's: only the human `sync` command calls it, and no next_action names it.
//
// It applies only to a terminal run with an exact push binding to the
// configured target and a pull request that is neither merged nor closed,
// whose published head the clean local head genuinely does not contain
// (blocked_diverged without equivalent content, or blocked_relation_unknown
// when the published head is not in the local object store).
//
// Before any change it proves, each failing closed with no Git ref or database
// row changed: the worktree is clean; the local gate is there; the gate's
// branch is exactly the published head; the configured push target's branch,
// read live under its own branch_sync_remote_timeout budget, is still exactly
// that head under the same target fingerprint; and, re-read after the network
// call, the branch, the local head, and the run are what they were.
//
// Then, in this order: the published head is pinned at the run's recovery ref
// in the gate (refusing a conflicting ref), the local head is staged into the
// gate by a gate-side fetch from the worktree (never a push, which would start
// a run), the gate's branch moves from the published head to the local head by
// compare-and-swap, and the run's custody is stamped returned; a failed stamp
// moves the gate's branch back by compare-and-swap. The worktree, the local
// branch, and the remote are never changed.
func (s *Service) ReplacePublished(ctx context.Context) State {
	if refusal, blocked := s.gateContextRefusal(ctx); blocked {
		return refusal
	}
	state, run, _ := s.inspect(ctx)
	if blocked, refused := s.replacePublishedLocalProofs(ctx, state, run); refused {
		return blocked
	}
	branchRef := "refs/heads/" + state.Local.Branch
	published := ptr(run.LastPushedSHA)

	repo, err := s.DB.GetRepo(s.Repo.ID)
	if err != nil || repo == nil {
		return blockedPlan(state, state.State, "blocked_replace_published_offline", "the configured push target could not be read; no files or gate refs were changed")
	}
	fingerprint := TargetFingerprint(repo.PushURL())
	if fingerprint != ptr(run.PushTargetFingerprint) {
		return blockedPlan(state, StateTargetChanged, "blocked_replace_published_target_changed", "the configured push target changed after the run published; no files or gate refs were changed")
	}
	pushURL := s.resolvedPushURL(ctx, repo)
	if strings.TrimSpace(pushURL) == "" {
		return blockedPlan(state, state.State, "blocked_replace_published_offline", "the configured push target is unavailable; no files or gate refs were changed")
	}
	liveCtx, cancel := context.WithTimeout(ctx, s.remoteTimeout())
	live, err := s.runLsRemote(liveCtx, s.workDir(), pushURL, branchRef)
	cancel()
	if err != nil {
		return blockedPlan(state, StateOffline, "blocked_replace_published_offline", "could not read the configured push target's branch; no files or gate refs were changed")
	}
	if live != published {
		return blockedPlan(state, StateRemoteRewritten, "blocked_replace_published_target_changed", fmt.Sprintf("the configured push target's branch is at %s, not the published head %s; no files or gate refs were changed", displayHead(live), published))
	}
	currentRepo, err := s.DB.GetRepo(s.Repo.ID)
	if err != nil || currentRepo == nil || TargetFingerprint(currentRepo.PushURL()) != fingerprint {
		return blockedPlan(state, StateTargetChanged, "blocked_replace_published_target_changed", "the configured push target changed while it was being read; no files or gate refs were changed")
	}

	// The first inspection is a snapshot taken before a network call of up to
	// branch_sync_remote_timeout: nothing may change the gate on its word.
	recheck, recheckRun, _ := s.inspect(ctx)
	if recheckRun == nil || recheckRun.ID != run.ID || !sameReplacePublishedRun(run, recheckRun) ||
		recheck.Local.Branch != state.Local.Branch || recheck.Local.Head != state.Local.Head || !recheck.Local.Clean {
		return blockedPlan(recheck, StateAmbiguousContext, "blocked_replace_published_assumptions_changed", "the branch, the local head, or the run changed while the push target was being read; no files or gate refs were changed")
	}
	if s.beforeReplacePublishedChange != nil {
		s.beforeReplacePublishedChange()
	}

	if err := custody.PreserveRecoveryHead(ctx, s.GateDir, run.ID, published); err != nil {
		return blockedPlan(state, state.State, "blocked_replace_published_preserve_failed", fmt.Sprintf("the published head could not be pinned at %s in the gate: %v; no files or gate refs were changed", custody.RecoveryRef(run.ID), err))
	}
	local := state.Local.Head
	stagingRef := "refs/no-mistakes/replace-published/" + run.ID
	if !objectExists(ctx, s.GateDir, local) {
		// The fetch source must be absolute: the command runs inside the gate
		// directory, where a relative worktree path would name the gate itself.
		source, err := filepath.Abs(s.workDir())
		if err != nil {
			return blockedPlan(state, state.State, "blocked_replace_published_assumptions_changed", "the invoking worktree path could not be resolved; the published head stays pinned and the gate branch was not changed")
		}
		if _, err := git.Run(ctx, s.GateDir, "fetch", "--no-tags", "--no-write-fetch-head", source, "+"+branchRef+":"+stagingRef); err != nil {
			return blockedPlan(state, state.State, "blocked_replace_published_assumptions_changed", "the local head could not be staged into the gate; the published head stays pinned and the gate branch was not changed")
		}
		staged, err := git.Run(ctx, s.GateDir, "rev-parse", stagingRef+"^{commit}")
		_, _ = git.Run(context.WithoutCancel(ctx), s.GateDir, "update-ref", "-d", stagingRef)
		if err != nil || staged != local {
			return blockedPlan(state, state.State, "blocked_replace_published_assumptions_changed", "the local branch moved while its head was being staged into the gate; the published head stays pinned and the gate branch was not changed")
		}
	}
	if _, err := git.Run(ctx, s.GateDir, "update-ref", branchRef, local, published); err != nil {
		return blockedPlan(state, state.State, "blocked_replace_published_gate_race", "the gate branch changed while it was being moved; the published head stays pinned and the gate branch was not replaced")
	}
	if err := s.stampCustodyReturned(run.ID); err != nil {
		message := "the custody return could not be recorded"
		if _, rollbackErr := git.Run(context.WithoutCancel(ctx), s.GateDir, "update-ref", branchRef, published, local); rollbackErr != nil {
			message += fmt.Sprintf("; the gate branch could not be moved back from %s to the published head %s", local, published)
		} else {
			message += "; the gate branch was moved back to the published head"
		}
		fresh, _, _ := s.inspect(ctx)
		return blockedPlan(fresh, fresh.State, "blocked_replace_published_custody_failed", message)
	}
	replaced, _, _ := s.inspect(ctx)
	replaced.Changed = true
	return replaced
}

// replacePublishedLocalProofs runs the checks that need neither the network nor
// a mutation, in the order the refusals are documented, and returns the
// refusal when one fails.
func (s *Service) replacePublishedLocalProofs(ctx context.Context, state State, run *db.Run) (State, bool) {
	if !s.replacePublishedApplies(ctx, state, run) {
		return blockedPlan(state, state.State, "blocked_replace_published_not_applicable", "replacing a published head requires the branch's finished run to have an exact push binding and an open pull request, and the local history not to contain its published head; no files or gate refs were changed"), true
	}
	if !state.Local.Clean {
		return blockedPlan(state, StateDirty, "blocked_replace_published_dirty", "the invoking worktree is not completely clean; no files or gate refs were changed"), true
	}
	if strings.TrimSpace(s.GateDir) == "" {
		return blockedPlan(state, StateAmbiguousContext, "blocked_replace_published_gate_unavailable", "no local gate is configured for this repository; no files or gate refs were changed"), true
	}
	if _, err := os.Stat(s.GateDir); err != nil {
		return blockedPlan(state, StateAmbiguousContext, "blocked_replace_published_gate_unavailable", "the local gate is unavailable; no files or gate refs were changed"), true
	}
	gateHead, exists, err := git.ExactRefTarget(ctx, s.GateDir, "refs/heads/"+state.Local.Branch)
	if err != nil {
		return blockedPlan(state, StateAmbiguousContext, "blocked_replace_published_gate_unavailable", "the local gate branch could not be read; no files or gate refs were changed"), true
	}
	if !exists || gateHead != ptr(run.LastPushedSHA) {
		return blockedPlan(state, state.State, "blocked_replace_published_gate_changed", "the gate branch is not exactly the run's published head; no files or gate refs were changed"), true
	}
	return State{}, false
}

// replacePublishedApplies reports the case the replacement exists for: a
// finished run with an exact push binding and an open pull request, whose
// published head the local head does not contain. Divergence that sync already
// advances (equivalent content) and a local head behind the published head are
// not this case.
func (s *Service) replacePublishedApplies(ctx context.Context, state State, run *db.Run) bool {
	if run == nil || !terminalRunStatus(run.Status) || run.CustodyReturnedAt != nil || !exactPushedBinding(s.Repo, run, state.Local.Branch) {
		return false
	}
	if state.PRState == "merged" || state.PRState == "closed" {
		return false
	}
	switch state.State {
	case StateDiverged, StateDirty:
	case StateAmbiguousContext:
		if state.Safety != "blocked_relation_unknown" {
			return false
		}
	default:
		return false
	}
	// A dirty worktree stops inspection before the relation is classified, so
	// classify it here on a copy; the dirty refusal comes next.
	probe := state
	s.classifyRelation(ctx, &probe, ptr(run.LastPushedSHA), run.BaseSHA, false)
	return probe.Safety == "blocked_diverged" || probe.Safety == "blocked_relation_unknown"
}

// noteReplacePublished adds the operator's sentence to a dead-end state whose
// locally checkable conditions for the replacement hold. It changes neither
// the safety code nor the next action.
func (s *Service) noteReplacePublished(ctx context.Context, state *State, run *db.Run) {
	if state.Safety != "blocked_diverged" && state.Safety != "blocked_relation_unknown" {
		return
	}
	if run == nil || !terminalRunStatus(run.Status) || run.CustodyReturnedAt != nil || !state.Local.Clean || !exactPushedBinding(s.Repo, run, state.Local.Branch) {
		return
	}
	if strings.TrimSpace(s.GateDir) == "" {
		return
	}
	gateHead, exists, err := git.ExactRefTarget(ctx, s.GateDir, "refs/heads/"+state.Local.Branch)
	if err != nil || !exists || gateHead != ptr(run.LastPushedSHA) {
		return
	}
	state.Error = strings.TrimSuffix(state.Error, ".") + ". " + ReplacePublishedOperatorNote
}

// replacedPublishedHead reports a run whose published head an operator
// replaced: custody returned on a run that was itself published (its head is
// its push binding, which no other recovery stamps), the published head pinned
// at the run's recovery ref in the gate, and the gate branch moved off it to a
// commit the local head contains.
func (s *Service) replacedPublishedHead(ctx context.Context, run *db.Run, local string) bool {
	if run == nil || run.CustodyReturnedAt == nil || run.LastPushedSHA == nil || run.HeadSHA != ptr(run.LastPushedSHA) || strings.TrimSpace(s.GateDir) == "" {
		return false
	}
	published := ptr(run.LastPushedSHA)
	if !custody.PublishedHeadPinned(ctx, s.GateDir, run.ID, published) {
		return false
	}
	gateHead, exists, err := git.ExactRefTarget(ctx, s.GateDir, "refs/heads/"+run.Branch)
	if err != nil || !exists || gateHead == published {
		return false
	}
	return gateHead == local || isAncestor(ctx, s.workDir(), gateHead, local)
}

func sameReplacePublishedRun(a, b *db.Run) bool {
	return a.Status == b.Status && a.HeadSHA == b.HeadSHA && ptr(a.LastPushedSHA) == ptr(b.LastPushedSHA) &&
		value(a.PushGeneration) == value(b.PushGeneration) && ptr(a.PushTargetFingerprint) == ptr(b.PushTargetFingerprint) &&
		ptr(a.PushRef) == ptr(b.PushRef) && a.CustodyReturnedAt == nil && b.CustodyReturnedAt == nil && !b.PushActive
}

func (s *Service) stampCustodyReturned(runID string) error {
	if s.setCustodyReturned != nil {
		return s.setCustodyReturned(runID)
	}
	return s.DB.SetRunCustodyReturned(runID)
}

func displayHead(sha string) string {
	if sha == "" {
		return "nothing (the branch does not exist)"
	}
	return sha
}

func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
