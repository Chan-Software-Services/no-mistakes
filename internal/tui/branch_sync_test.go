package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kunchenguid/no-mistakes/internal/branchsync"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestBranchSyncActionRefreshesBeforeConfirmationAndAppliesThroughSharedPath(t *testing.T) {
	run := &ipc.RunInfo{ID: "run-1", Branch: "feature", Status: types.RunRunning}
	m := NewModel("socket", nil, run)
	cached := branchsync.State{
		State: branchsync.StateBehind, Relation: branchsync.RelationBehind, Safety: "refresh_required",
		Local:      branchsync.LocalState{Branch: "feature", Head: strings.Repeat("a", 40), Clean: true},
		Pipeline:   branchsync.PipelineState{RunID: "run-1", PushedHead: strings.Repeat("b", 40)},
		Target:     branchsync.TargetState{Kind: "fork", Remote: "fork", Ref: "refs/heads/feature"},
		NextAction: &branchsync.NextAction{Code: "sync", Command: "no-mistakes axi sync"},
	}
	m.branchSync = &cached
	refreshCalls := 0
	applyCalls := 0
	m.syncRefresh = func() branchsync.State {
		refreshCalls++
		fresh := cached
		fresh.Safety = "safe_fast_forward"
		fresh.Remote.Freshness = "live"
		return fresh
	}
	m.syncApply = func() branchsync.State {
		applyCalls++
		applied := cached
		applied.State = branchsync.StateSynchronized
		applied.Safety = "already_synchronized"
		applied.Relation = branchsync.RelationEqual
		applied.Changed = true
		applied.Local.Head = applied.Pipeline.PushedHead
		return applied
	}

	nextModel, cmd := m.handleKey(keyMsg("u"))
	m = nextModel.(Model)
	if cmd == nil || !m.syncRefreshing || m.syncConfirm || refreshCalls != 0 {
		t.Fatalf("refresh was not scheduled explicitly: %#v", m)
	}
	msg := cmd()
	next, _ := m.Update(msg)
	m = next.(Model)
	if refreshCalls != 1 || !m.syncConfirm || m.branchSync.Remote.Freshness != "live" || applyCalls != 0 {
		t.Fatalf("fresh confirmation state = %#v, refresh=%d apply=%d", m.branchSync, refreshCalls, applyCalls)
	}
	plain := stripANSI(m.View())
	for _, want := range []string{strings.Repeat("a", 40), strings.Repeat("b", 40), "refs/heads/feature", "strict fast-forward", "u/enter apply"} {
		if !strings.Contains(plain, want) {
			t.Errorf("confirmation missing %q:\n%s", want, plain)
		}
	}
	confirmation := boxView(t, renderSyncConfirmation(*m.branchSync, 80))
	for _, want := range []string{
		"Local HEAD:   " + strings.Repeat("a", 40),
		"No reset, stash, merge commit, rebase, force push, branch switch, or remote update can occur.",
	} {
		if !strings.Contains(confirmation, want) {
			t.Errorf("80-column confirmation missing %q:\n%s", want, confirmation)
		}
	}

	nextModel, cmd = m.handleKey(keyMsg("enter"))
	m = nextModel.(Model)
	if cmd == nil || applyCalls != 0 {
		t.Fatal("apply did not wait for async command")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if applyCalls != 1 || m.syncConfirm || m.branchSync.State != branchsync.StateSynchronized || !m.branchSync.Changed {
		t.Fatalf("apply result = %#v", m.branchSync)
	}
}

func TestLocalBranchStatusIsCompactAndOnlyOffersEligibleAction(t *testing.T) {
	state := branchsync.State{State: branchsync.StateBehind, Safety: "refresh_required"}
	view := localBranchView(t, &state)
	if !strings.Contains(view, "Safe fast-forward available after refresh") || !strings.Contains(view, "u sync branch") {
		t.Fatalf("behind view:\n%s", view)
	}
	state.State = branchsync.StateDiverged
	view = localBranchView(t, &state)
	if !strings.Contains(view, "diverged") || strings.Contains(view, "u sync branch") {
		t.Fatalf("diverged view:\n%s", view)
	}
	state.NextAction = &branchsync.NextAction{Code: "sync", Command: "no-mistakes axi sync"}
	view = localBranchView(t, &state)
	if !strings.Contains(view, "equivalent work") || !strings.Contains(view, "u sync branch") {
		t.Fatalf("equivalent candidate view:\n%s", view)
	}
	state.Safety = branchsync.SafetySafeEquivalentAdvance
	view = localBranchView(t, &state)
	if !strings.Contains(view, "represented in the live pipeline head") || strings.Contains(view, "No automatic reconciliation") {
		t.Fatalf("equivalent live view:\n%s", view)
	}
}

func TestBranchSyncActionRefreshesEquivalentDivergedBeforeConfirmation(t *testing.T) {
	m := NewModel("socket", nil, &ipc.RunInfo{ID: "run-1", Branch: "feature", Status: types.RunRunning})
	cached := branchsync.State{
		State: branchsync.StateDiverged, Relation: branchsync.RelationDiverged, Safety: "refresh_required",
		Local:      branchsync.LocalState{Branch: "feature", Head: strings.Repeat("a", 40), Clean: true},
		Pipeline:   branchsync.PipelineState{RunID: "run-1", PushedHead: strings.Repeat("b", 40)},
		Target:     branchsync.TargetState{Kind: "fork", Remote: "fork", Ref: "refs/heads/feature"},
		NextAction: &branchsync.NextAction{Code: "sync", Command: "no-mistakes axi sync"},
	}
	m.branchSync = &cached
	refreshCalls := 0
	m.syncRefresh = func() branchsync.State {
		refreshCalls++
		fresh := cached
		fresh.Safety = branchsync.SafetySafeEquivalentAdvance
		fresh.Remote.Freshness = "live"
		return fresh
	}

	nextModel, cmd := m.handleKey(keyMsg("u"))
	m = nextModel.(Model)
	if cmd == nil || !m.syncRefreshing || m.syncConfirm {
		t.Fatalf("refresh was not scheduled: %#v", m)
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	if refreshCalls != 1 || !m.syncConfirm || m.branchSync.Safety != branchsync.SafetySafeEquivalentAdvance {
		t.Fatalf("fresh equivalent confirmation state = %#v, refresh=%d", m.branchSync, refreshCalls)
	}
	if plain := stripANSI(m.View()); !strings.Contains(plain, "Confirm local branch sync") {
		t.Fatalf("view does not show the confirmation:\n%s", plain)
	}
	confirmation := boxView(t, renderSyncConfirmation(*m.branchSync, 80))
	for _, want := range []string{
		"equivalent live pipeline head",
		"anchored before the branch moves",
		"No stash, merge commit, rebase, force push, branch switch, or remote update can occur.",
		"u/enter apply",
	} {
		if !strings.Contains(confirmation, want) {
			t.Errorf("confirmation missing %q:\n%s", want, confirmation)
		}
	}
}

func TestFreshAttachUsesPersistedCIReadyAndCompletedSubscriptionCloseIsNotError(t *testing.T) {
	ciRun := &ipc.RunInfo{ID: "run-ci", Branch: "feature", Status: types.RunRunning, CIReady: true, Steps: []ipc.StepResultInfo{{RunID: "run-ci", StepName: types.StepCI, Status: types.StepStatusRunning}}}
	m := NewModel("socket", nil, ciRun)
	view := stripANSI(renderCIViewWithSelection(ciRun, m.steps, "", nil, 80, 20, 0, nil))
	if !strings.Contains(view, "Checks passed") || strings.Contains(view, "Monitoring CI checks") {
		t.Fatalf("fresh CI view ignored persisted readiness:\n%s", view)
	}

	completed := NewModel("socket", nil, &ipc.RunInfo{ID: "done", Status: types.RunCompleted})
	closed := make(chan ipc.Event)
	close(closed)
	next, cmd := completed.Update(connectedMsg{events: closed, cancelSub: func() {}, subscriptionID: completed.subscriptionID})
	completed = next.(Model)
	if cmd != nil || completed.err != nil {
		t.Fatalf("completed attach scheduled closed-stream error: cmd=%v err=%v", cmd != nil, completed.err)
	}
}

func TestSyncConfirmationEscapeNeverApplies(t *testing.T) {
	m := NewModel("socket", nil, &ipc.RunInfo{ID: "run", Status: types.RunRunning})
	m.syncConfirm = true
	calls := 0
	m.syncApply = func() branchsync.State { calls++; return branchsync.State{} }
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(Model)
	if cmd != nil || m.syncConfirm || calls != 0 {
		t.Fatalf("escape applied sync: confirm=%v calls=%d", m.syncConfirm, calls)
	}
}

func TestMissingPreservedHeadStatusShowsKeepLocalRecoveryCommand(t *testing.T) {
	state := branchsync.State{
		State:  branchsync.StatePipelineOwned,
		Safety: "blocked_recover_preserved_head_missing",
		NextAction: &branchsync.NextAction{
			Code:    "recover_custody",
			Command: "no-mistakes axi sync --recover --keep-local",
		},
	}

	// The TUI is a person's surface, so it names the human command, whose
	// output is human text, never the agent's axi form from the next action.
	view := localBranchView(t, &state)
	if !strings.Contains(view, "`no-mistakes sync --recover --keep-local`") {
		t.Errorf("missing-head status did not show the human recovery command:\n%s", view)
	}
	if strings.Contains(view, "axi sync") {
		t.Errorf("missing-head status names an axi command:\n%s", view)
	}
}

// TestRecoverableCustodyActionFlowsThroughConfirmationAndRecoverService covers
// the TUI half of the guarded custody recovery: a terminal pre-push
// pipeline_owned state renders the recovery offer, `u` opens an explicit
// confirmation instead of acting, and `enter` routes through the shared
// branchsync recovery service exactly once.
func TestRecoverableCustodyActionFlowsThroughConfirmationAndRecoverService(t *testing.T) {
	run := &ipc.RunInfo{ID: "run-1", Branch: "feature", Status: types.RunCancelled}
	m := NewModel("socket", nil, run)
	stranded := branchsync.State{
		State: branchsync.StatePipelineOwned, Relation: branchsync.RelationUnknown, Safety: "blocked_pipeline_owned_recoverable",
		Local:    branchsync.LocalState{Branch: "feature", Head: strings.Repeat("a", 40), Clean: true},
		Pipeline: branchsync.PipelineState{RunID: "run-1", Status: "cancelled", Phase: "pre_push", CurrentHead: strings.Repeat("c", 40)},
	}
	m.branchSync = &stranded

	view := localBranchView(t, m.branchSync)
	for _, want := range []string{"preserved in the local gate", "Recover custody", "u recover custody"} {
		if !strings.Contains(view, want) {
			t.Errorf("recoverable status missing %q:\n%s", want, view)
		}
	}

	recoverCalls := 0
	m.syncRecover = func(keepLocal bool) branchsync.State {
		if keepLocal {
			t.Fatal("ordinary recovery unexpectedly selected keep-local")
		}
		recoverCalls++
		recovered := stranded
		recovered.State = branchsync.StateCustodyReturned
		recovered.Safety = "custody_returned"
		recovered.Relation = branchsync.RelationEqual
		recovered.Recovered = true
		recovered.Changed = true
		recovered.Local.Head = recovered.Pipeline.CurrentHead
		return recovered
	}

	nextModel, cmd := m.handleKey(keyMsg("u"))
	m = nextModel.(Model)
	if cmd != nil || !m.recoverConfirm || recoverCalls != 0 {
		t.Fatalf("u must open confirmation without acting: confirm=%v calls=%d", m.recoverConfirm, recoverCalls)
	}
	plain := stripANSI(m.View())
	for _, want := range []string{"custody", strings.Repeat("a", 40), strings.Repeat("c", 40), "u/enter recover", "--keep-local"} {
		if !strings.Contains(plain, want) {
			t.Errorf("recover confirmation missing %q:\n%s", want, plain)
		}
	}
	confirmation := boxView(t, renderRecoverConfirmation(stranded, 80))
	for _, want := range []string{
		"Preserved HEAD: " + strings.Repeat("c", 40),
		"Dirty worktrees and divergence that cannot be proven contained refuse without changes; `no-mistakes sync --recover --keep-local` keeps the current head instead.",
	} {
		if !strings.Contains(confirmation, want) {
			t.Errorf("80-column recover confirmation missing %q:\n%s", want, confirmation)
		}
	}

	nextModel, cmd = m.handleKey(keyMsg("enter"))
	m = nextModel.(Model)
	if cmd == nil || recoverCalls != 0 {
		t.Fatal("recover did not wait for async command")
	}
	next, _ := m.Update(cmd())
	m = next.(Model)
	if recoverCalls != 1 || m.recoverConfirm || m.branchSync.State != branchsync.StateCustodyReturned || !m.branchSync.Recovered {
		t.Fatalf("recover result = %#v", m.branchSync)
	}
	if m.err != nil {
		t.Fatalf("successful recovery left an error: %v", m.err)
	}

	returned := localBranchView(t, m.branchSync)
	if !strings.Contains(returned, "Custody returned") {
		t.Fatalf("custody_returned status:\n%s", returned)
	}
}

func TestArchiveRecoveryConfirmationUsesOnlyGuardedKeepLocalAction(t *testing.T) {
	run := &ipc.RunInfo{ID: "run-archive", Branch: "feature", Status: types.RunCancelled}
	m := NewModel("socket", nil, run)
	stranded := branchsync.State{
		State: branchsync.StatePipelineOwned, Relation: branchsync.RelationDiverged, Safety: "blocked_pipeline_owned_recoverable",
		Local:    branchsync.LocalState{Branch: "feature", Head: strings.Repeat("a", 40), Clean: true},
		Pipeline: branchsync.PipelineState{RunID: run.ID, Status: "cancelled", Phase: "pre_push", CurrentHead: strings.Repeat("c", 40)},
		Recovery: &branchsync.RecoveryEvidence{
			Source: "bound_archive", RepositoryID: "repo-1", RunID: run.ID, Branch: "feature",
			RequiredHead: strings.Repeat("a", 40), PreservedHead: strings.Repeat("c", 40),
			ArchiveRef: "refs/heads/archive/run-archive", KeepLocal: true, Proof: "verified",
		},
		NextAction: &branchsync.NextAction{Code: "recover_custody", Command: "no-mistakes axi sync --recover --keep-local"},
	}
	m.branchSync = &stranded
	called := false
	m.syncRecover = func(keepLocal bool) branchsync.State {
		called = true
		if !keepLocal {
			t.Fatal("archive recovery did not use keep-local")
		}
		recovered := stranded
		recovered.State = branchsync.StateSynchronized
		recovered.Safety = "already_synchronized"
		recovered.Recovered = true
		recovered.Changed = false
		return recovered
	}

	view := localBranchView(t, m.branchSync)
	for _, want := range []string{"verified archive", "exact required local head", "u recover custody"} {
		if !strings.Contains(view, want) {
			t.Errorf("archive status missing %q:\n%s", want, view)
		}
	}
	next, cmd := m.handleKey(keyMsg("u"))
	m = next.(Model)
	if cmd != nil || !m.recoverConfirm {
		t.Fatal("archive recovery did not open confirmation")
	}
	confirmation := stripANSI(m.View())
	for _, want := range []string{"never selects or replays the archive", stranded.Recovery.ArchiveRef, stranded.Recovery.RequiredHead} {
		if !strings.Contains(confirmation, want) {
			t.Errorf("archive confirmation missing %q:\n%s", want, confirmation)
		}
	}
	confirmation = boxView(t, renderRecoverConfirmation(stranded, 80))
	for _, want := range []string{
		"Archive ref:    " + stranded.Recovery.ArchiveRef,
		"Any changed archive, head, branch, run, repository, or gate evidence makes recovery refuse without selecting the divergent head.",
	} {
		if !strings.Contains(confirmation, want) {
			t.Errorf("80-column archive confirmation missing %q:\n%s", want, confirmation)
		}
	}
	next, cmd = m.handleKey(keyMsg("enter"))
	m = next.(Model)
	if cmd == nil || called {
		t.Fatal("archive recovery did not wait for its command")
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if !called || !m.branchSync.Recovered || m.branchSync.Changed {
		t.Fatalf("archive recovery result = %#v", m.branchSync)
	}
}

// TestActivePipelineOwnedStateOffersNoRecoveryAction pins that the recovery
// affordance never appears while the owning run is still active.
func TestActivePipelineOwnedStateOffersNoRecoveryAction(t *testing.T) {
	run := &ipc.RunInfo{ID: "run-1", Branch: "feature", Status: types.RunRunning}
	m := NewModel("socket", nil, run)
	m.branchSync = &branchsync.State{
		State: branchsync.StatePipelineOwned, Safety: "blocked_pipeline_owned",
		Local:    branchsync.LocalState{Branch: "feature", Head: strings.Repeat("a", 40), Clean: true},
		Pipeline: branchsync.PipelineState{RunID: "run-1", Status: "running", Phase: "pre_push"},
	}
	m.syncRecover = func(bool) branchsync.State {
		t.Fatal("recover service must not be reachable for an active run")
		return branchsync.State{}
	}
	view := localBranchView(t, m.branchSync)
	if strings.Contains(view, "recover") || !strings.Contains(view, "Do not make follow-up commits") {
		t.Fatalf("active pipeline_owned view:\n%s", view)
	}
	nextModel, cmd := m.handleKey(keyMsg("u"))
	m = nextModel.(Model)
	if cmd != nil || m.recoverConfirm || m.syncConfirm {
		t.Fatalf("u acted on an active pipeline_owned state: %#v", m)
	}
}

// localBranchView renders the Local branch box at 80 columns through
// boxView.
func localBranchView(t *testing.T, state *branchsync.State) string {
	t.Helper()
	return boxView(t, renderLocalBranchStatus(state, false, 80))
}

// boxView takes a box rendered at 80 columns, fails the test when any line
// does not fit the box exactly, and returns the box with its content lines
// trimmed and joined onto one line, between the top and bottom borders, so a
// test can match a phrase wherever the wrap fell.
func boxView(t *testing.T, rendered string) string {
	t.Helper()
	const width = 80
	view := stripANSI(rendered)
	lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
	for _, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Errorf("box line is %d columns wide, want %d:\n%s", got, width, view)
		}
	}
	if len(lines) < 3 {
		t.Fatalf("box has no content line:\n%s", view)
	}
	content := make([]string, 0, len(lines)-2)
	for _, line := range lines[1 : len(lines)-1] {
		if text := strings.TrimSpace(strings.Trim(line, "│")); text != "" {
			content = append(content, text)
		}
	}
	return lines[0] + "\n" + strings.Join(content, " ") + "\n" + lines[len(lines)-1]
}

// TestLocalBranchBoxDrawsInsideTheResponsiveLeftColumn renders the model on a
// 100- to 119-column terminal with sidebar content, where the responsive
// layout gives the left column under 40 columns, and checks that the Local
// branch box is drawn at that column's width with whole borders, and that no
// line of the view is wider than the terminal.
func TestLocalBranchBoxDrawsInsideTheResponsiveLeftColumn(t *testing.T) {
	run := &ipc.RunInfo{ID: "run-1", Branch: "feature", Status: types.RunCancelled}
	for width := responsiveLayoutMinWidth; width < 120; width++ {
		m := NewModel("socket", nil, run)
		m.width = width
		m.height = 50
		m.logs = []string{"log line"}
		m.branchSync = &branchsync.State{
			State: branchsync.StatePipelineOwned, Relation: branchsync.RelationUnknown, Safety: "blocked_pipeline_owned_recoverable",
			Local:    branchsync.LocalState{Branch: "feature", Head: strings.Repeat("a", 40), Clean: true},
			Pipeline: branchsync.PipelineState{RunID: "run-1", Status: "cancelled", Phase: "pre_push", CurrentHead: strings.Repeat("c", 40)},
		}
		if !shouldUseResponsiveLayout(width, hasResponsiveSidebarContent(m)) {
			t.Fatalf("fixture: width %d does not use the responsive layout", width)
		}
		leftWidth, _ := responsiveColumnWidths(width)

		// The view opens with the terminal title's OSC sequence, which
		// takes no columns on the terminal.
		rendered := m.View()
		if end := strings.IndexByte(rendered, '\a'); strings.HasPrefix(rendered, "\x1b]2;") && end >= 0 {
			rendered = rendered[end+1:]
		}
		view := stripANSI(rendered)
		lines := strings.Split(view, "\n")
		for _, line := range lines {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d: view line is %d columns wide:\n%s", width, got, line)
			}
		}

		top := -1
		for i, line := range lines {
			if strings.HasPrefix(line, "╭─ Local branch ") {
				top = i
				break
			}
		}
		if top < 0 {
			t.Fatalf("width %d: no Local branch box in the view:\n%s", width, view)
		}
		bottom := -1
		for i := top; i < len(lines); i++ {
			left := []rune(lines[i])
			if len(left) < leftWidth {
				t.Fatalf("width %d: box line %d is shorter than the %d-column left column:\n%s", width, i, leftWidth, view)
			}
			left = left[:leftWidth]
			first, last := string(left[0]), string(left[leftWidth-1])
			switch {
			case i == top:
				if last != "╮" {
					t.Errorf("width %d: top border does not close at column %d: %q", width, leftWidth, string(left))
				}
			case first == "╰":
				if last != "╯" {
					t.Errorf("width %d: bottom border does not close at column %d: %q", width, leftWidth, string(left))
				}
				bottom = i
			case first != "│" || last != "│":
				t.Errorf("width %d: box line is not whole within the %d-column left column: %q", width, leftWidth, string(left))
			}
			if bottom >= 0 {
				break
			}
		}
		if bottom < 0 {
			t.Fatalf("width %d: the Local branch box has no bottom border:\n%s", width, view)
		}
		box := strings.Builder{}
		for _, line := range lines[top : bottom+1] {
			box.WriteString(string([]rune(line)[:leftWidth]))
			box.WriteString("\n")
		}
		for _, want := range []string{"Recover", "u recover custody"} {
			if !strings.Contains(box.String(), want) {
				t.Errorf("width %d: Local branch box missing %q:\n%s", width, want, box.String())
			}
		}
	}
}

// TestConfirmationProseWrapsToItsBox renders the custody recovery and local
// branch sync confirmations at the 65 columns the responsive layout's right
// column has on a 105-column terminal, and checks that their opening prose
// fills each line before breaking it: no line ends while the next line's first
// word would still have fit on it.
func TestConfirmationProseWrapsToItsBox(t *testing.T) {
	ordinary := branchsync.State{
		State: branchsync.StatePipelineOwned, Safety: "blocked_pipeline_owned_recoverable",
		Local:    branchsync.LocalState{Branch: "feature", Head: strings.Repeat("a", 40), Clean: true},
		Pipeline: branchsync.PipelineState{RunID: "run-1", Status: "cancelled", CurrentHead: strings.Repeat("c", 40)},
	}
	archive := ordinary
	archive.Recovery = &branchsync.RecoveryEvidence{
		Source: "bound_archive", RequiredHead: strings.Repeat("a", 40), ArchiveRef: "refs/heads/archive/run-1", KeepLocal: true, Proof: "verified",
	}
	fastForward := ordinary
	fastForward.State = branchsync.StateBehind
	fastForward.Safety = branchsync.SafetySafeFastForward
	equivalent := fastForward
	equivalent.State = branchsync.StateDiverged
	equivalent.Safety = branchsync.SafetySafeEquivalentAdvance
	const width = 65
	for name, rendered := range map[string]string{
		"ordinary recovery":       renderRecoverConfirmation(ordinary, width),
		"archive recovery":        renderRecoverConfirmation(archive, width),
		"fast-forward sync":       renderSyncConfirmation(fastForward, width),
		"equivalent-advance sync": renderSyncConfirmation(equivalent, width),
	} {
		lines := strings.Split(stripANSI(rendered), "\n")
		var prose []string
		for _, line := range lines[1 : len(lines)-1] {
			text := strings.TrimSpace(strings.Trim(line, "│"))
			if strings.HasPrefix(text, "Local branch:") {
				break
			}
			prose = append(prose, text)
		}
		for i := 0; i+1 < len(prose); i++ {
			if prose[i] == "" || prose[i+1] == "" {
				continue
			}
			next := strings.Fields(prose[i+1])[0]
			if lipgloss.Width(prose[i])+1+lipgloss.Width(next) <= width-4 {
				t.Errorf("%s confirmation breaks %q before %q, which fits on it:\n%s", name, prose[i], next, strings.Join(lines, "\n"))
			}
		}
	}
}

// TestBranchSyncBoxesFitANarrowStackedTerminal renders the Local branch box
// and both confirmations at 30 columns, the width the stacked layout gives
// them on a 30-column terminal, and checks that every line fits and each
// box's borders are whole at that width.
func TestBranchSyncBoxesFitANarrowStackedTerminal(t *testing.T) {
	const width = 30
	stranded := branchsync.State{
		State: branchsync.StatePipelineOwned, Safety: "blocked_pipeline_owned_recoverable",
		Local:    branchsync.LocalState{Branch: "feature", Head: strings.Repeat("a", 40), Clean: true},
		Pipeline: branchsync.PipelineState{RunID: "run-1", Status: "cancelled", CurrentHead: strings.Repeat("c", 40)},
	}
	behind := stranded
	behind.State = branchsync.StateBehind
	behind.Safety = branchsync.SafetySafeFastForward
	behind.Pipeline.PushedHead = strings.Repeat("c", 40)
	behind.Target = branchsync.TargetState{Kind: "upstream", Remote: "origin", Ref: "refs/heads/feature"}
	for name, rendered := range map[string]string{
		"local branch":          renderLocalBranchStatus(&stranded, false, width),
		"recovery confirmation": renderRecoverConfirmation(stranded, width),
		"sync confirmation":     renderSyncConfirmation(behind, width),
	} {
		lines := strings.Split(stripANSI(rendered), "\n")
		for i, line := range lines {
			if got := lipgloss.Width(line); got != width {
				t.Errorf("%s: line %d is %d columns wide, want %d: %q", name, i, got, width, line)
			}
			runes := []rune(line)
			first, last := string(runes[0]), string(runes[len(runes)-1])
			switch {
			case i == 0:
				if first != "╭" || last != "╮" {
					t.Errorf("%s: top border is not whole: %q", name, line)
				}
			case i == len(lines)-1:
				if first != "╰" || last != "╯" {
					t.Errorf("%s: bottom border is not whole: %q", name, line)
				}
			case first != "│" || last != "│":
				t.Errorf("%s: line %d is not inside the box: %q", name, i, line)
			}
		}
	}
}
