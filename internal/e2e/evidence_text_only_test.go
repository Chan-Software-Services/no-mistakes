//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

// trustedRepoConfigWithEvidenceBranch opts the repository into copying the
// run's evidence onto its evidence branch.
const trustedRepoConfigWithEvidenceBranch = `allow_repo_commands: true
test:
  evidence:
    store_in_repo: true
`

// textOnlyEvidenceInlineImage is how a report carries a screenshot without a
// file of its own.
const textOnlyEvidenceInlineImage = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk"

// textOnlyEvidenceScenario has the test step write one plain log beside the
// files a browser test run leaves behind (a screenshot, a trace archive, an
// HTML report with an inline image, a Markdown note embedding the screenshot)
// and report every one of them as an artifact by its full path, the labels
// naming the files as agents routinely do.
func textOnlyEvidenceScenario(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "text-only-evidence.yaml")
	content := `actions:
  - match: "You are validating a code change by driving the product itself."
    text: "captured the evidence"
    write_evidence:
      - path: "server.log"
        content: "POST /checkout 200 in 12ms\n"
      - path: "checkout.png"
        content: "PNG screenshot bytes"
      - path: "trace.zip"
        content: "PK archive bytes"
      - path: "report.html"
        content: "<html><body><h1>1 passed</h1><img src=\"` + textOnlyEvidenceInlineImage + `\"></body></html>\n"
      - path: "notes.md"
        content: "# Checkout\n\n![checkout page](checkout.png)\n"
    structured:
      findings: []
      summary: "all tests passed"
      tested:
        - "drove the checkout page in a browser"
      testing_summary: "The checkout page loads and the order goes through."
      scenarios:
        - name: "a shopper checks out"
          result: pass
          live: true
          evidence: "The server answered POST /checkout with 200."
          reason: ""
      verdict: go
      artifacts:
        - kind: log
          label: "Server log"
          path: "{{evidence_dir}}/server.log"
        - kind: screenshot
          label: "checkout.png"
          path: "{{evidence_dir}}/checkout.png"
          content: "The checkout page shows the order total and a Pay button."
        - kind: trace
          label: "trace.zip"
          path: "{{evidence_dir}}/trace.zip"
        - kind: report
          label: "Browser report"
          path: "{{evidence_dir}}/report.html"
        - kind: log
          label: "notes.md"
          path: "{{evidence_dir}}/notes.md"
  - text: "no issues found"
    structured:
      findings: []
      summary: "no issues found"
      risk_level: low
      risk_rationale: "no risks detected in the diff"
      risk_scope: source-or-external
      title: "feat: checkout total"
      body: "## What Changed\n\n- the checkout page shows the order total"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write text-only evidence scenario: %v", err)
	}
	return path
}

var (
	e2eMediaLinkTarget = regexp.MustCompile(`(?i)(?:\]\(\s*<?|\b(?:href|src|srcset|poster)\s*=\s*["']?|\b(?:https?://|www\.))[^\s<>"')\]]*\.(?:png|jpe?g|gif|webp|svg|mp4|webm|mov)\b`)
	e2eMediaForbidden  = []string{"![", "<img", "<video", "<picture", "<source", "user-attachments", "data:image", "ivborw0kggo"}
)

// assertPRBodyCarriesNoMedia fails when body embeds or links an image or a
// video, or carries one inline.
func assertPRBodyCarriesNoMedia(t *testing.T, what, body string) {
	t.Helper()
	lower := strings.ToLower(body)
	for _, needle := range e2eMediaForbidden {
		if strings.Contains(lower, needle) {
			t.Errorf("%s contains %q:\n%s", what, needle, body)
		}
	}
	if match := e2eMediaLinkTarget.FindString(body); match != "" {
		t.Errorf("%s links to media (%q):\n%s", what, match, body)
	}
}

// TestGateRunPublishesOnlyTextEvidence drives a gate run whose test step left a
// log, a screenshot, a trace archive, an HTML report with an inline image and a
// Markdown note embedding the screenshot in the evidence directory. Only the
// log reaches the evidence branch, and the PR body sent to the forge carries
// no image, no image link and no local path or file name.
//
// A second run on the same branch then meets the PR already open, with a body
// an earlier version wrote that still embeds an image: no revision of the body
// the second run writes carries it.
func TestGateRunPublishesOnlyTextEvidence(t *testing.T) {
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: textOnlyEvidenceScenario(t)})

	// A GitHub-shaped origin rewritten to the local bare repo: evidence links
	// are only derivable for GitHub, and the rewrite keeps every push local.
	const originURL = "https://github.com/owner/no-mistakes.git"
	configureGitURLRewrite(t, h, originURL, h.UpstreamDir)
	if out, err := h.runGit(t.Context(), h.WorkDir, "remote", "set-url", "origin", originURL); err != nil {
		t.Fatalf("set github-shaped origin: %v\n%s", err, out)
	}
	ghLog := filepath.Join(filepath.Dir(h.AgentLog), "gh-text-only-evidence.log")
	prStore := filepath.Join(filepath.Dir(h.AgentLog), "gh-text-only-evidence-pr.json")
	t.Setenv("FAKEAGENT_GH_MODE", "fork-pr")
	t.Setenv("FAKEAGENT_GH_LOG", ghLog)
	t.Setenv("FAKEAGENT_GH_PARENT", "owner/no-mistakes")
	t.Setenv("FAKEAGENT_GH_PR_STORE", prStore)

	pushMainRepoConfig(t, h, trustedRepoConfigWithEvidenceBranch)
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}

	const branch = "feature/checkout-total"
	h.CommitChange(branch, "checkout.txt", "total = 42.00\n", "show the order total")
	h.PushToGate(branch)
	run := h.WaitForRun(branch, 180*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want completed (error=%v)", run.Status, deref(run.Error))
	}

	// The evidence branch, read out of the repository the pipeline pushed to.
	tree, err := h.runGit(t.Context(), h.UpstreamDir, "ls-tree", "-r", "--name-only", "no-mistakes/evidence")
	if err != nil {
		t.Fatalf("evidence branch was not published: %v\n%s", err, tree)
	}
	published := string(tree)
	t.Logf("published evidence branch tree:\n%s", published)
	if !strings.Contains(published, "server.log") {
		t.Fatalf("the evidence branch carries no text evidence, so the rest proves nothing:\n%s", published)
	}
	for _, kept := range []string{"checkout.png", "trace.zip", "report.html", "notes.md"} {
		if strings.Contains(published, kept) {
			t.Errorf("the evidence branch carries %q, which is not plain text evidence:\n%s", kept, published)
		}
	}
	if blobs, err := h.runGit(t.Context(), h.UpstreamDir, "grep", "-l", "-e", "iVBORw0KGgo", "-e", "!\\[", "no-mistakes/evidence"); err == nil {
		t.Errorf("a file on the evidence branch carries an image:\n%s", blobs)
	}

	// The PR body the pipeline sent to the forge.
	body := createdPRBody(t, readGHStubInvocations(t, ghLog))
	t.Logf("PR body sent to the forge:\n%s", body)
	assertPRBodyCarriesNoMedia(t, "the PR body", body)
	evidenceDir := filepath.Join(h.NMHome, "evidence", run.ID)
	for _, unwanted := range []string{"checkout.png", "trace.zip", "report.html", "notes.md", evidenceDir, filepath.ToSlash(evidenceDir), "1 passed"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the PR body carries %q:\n%s", unwanted, body)
		}
	}
	for _, want := range []string{
		"POST /checkout 200 in 12ms",
		"/blob/",
		"- Screenshot: The checkout page shows the order total and a Pay button.",
		"- Evidence: Artifact (kept with the run, not published)",
		"- Evidence: Browser report (kept with the run, not published)",
		"- Evidence: Log (kept with the run, not published)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the PR body is missing %q:\n%s", want, body)
		}
	}

	// The PR is open; its body is now one an earlier version wrote, with an
	// embed and an image link in it.
	var stored map[string]string
	data, err := os.ReadFile(prStore)
	if err != nil {
		t.Fatalf("read the stored pull request: %v", err)
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("parse the stored pull request: %v\n%s", err, data)
	}
	stored["body"] = "![earlier shot](https://github.com/user-attachments/assets/0f8fad5b-d9cb-469f-a165-70867728950e)\n\n<img src=\"https://example.com/earlier.png\">\n\n" + stored["body"]
	data, _ = json.Marshal(stored)
	if err := os.WriteFile(prStore, data, 0o644); err != nil {
		t.Fatalf("rewrite the stored pull request: %v", err)
	}
	callsBefore := len(readGHStubInvocations(t, ghLog))

	secondHead := h.CommitChange(branch, "checkout.txt", "total = 42.00\ncurrency = EUR\n", "show the currency")
	h.PushToGate(branch)
	second := waitForRunAtHead(t, h, branch, secondHead, 180*time.Second)
	if second.Status != types.RunCompleted {
		t.Fatalf("second run status = %s, want completed (error=%v)", second.Status, deref(second.Error))
	}

	edits := 0
	for _, inv := range readGHStubInvocations(t, ghLog)[callsBefore:] {
		if len(inv.Args) < 2 || inv.Args[0] != "pr" || inv.Body == "" {
			continue
		}
		if inv.Args[1] == "create" {
			t.Fatalf("the second run opened a new pull request instead of updating the open one: %v", inv.Args)
		}
		edits++
		t.Logf("PR body revision %d written by the second run:\n%s", edits, inv.Body)
		assertPRBodyCarriesNoMedia(t, "a PR body revision written by the second run", inv.Body)
	}
	// One revision attests the new head before the push, one is the PR step's.
	if edits < 2 {
		t.Fatalf("the second run wrote %d PR body revision(s), want the pre-push attestation rewrite and the PR step's update", edits)
	}
}

// waitForRunAtHead polls until the branch's run for head reaches a terminal
// status, which tells a second run on a branch from the first.
func waitForRunAtHead(t *testing.T, h *Harness, branch, head string, timeout time.Duration) *ipc.RunInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, run := range h.Runs() {
			if strings.TrimPrefix(run.Branch, "refs/heads/") == branch && run.HeadSHA == head && run.Status.Terminal() {
				return &run
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no run for %s at %s finished within %s; runs: %+v", branch, head, timeout, h.Runs())
	return nil
}
