package steps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

// disclosureIntents are intents shaped the way a driving agent writes a
// Disclosure section: rules for what public text may say, which name the very
// words they bar. The PR body must publish the rest of the intent and none of
// the Disclosure section, on every path that renders the Intent section.
var disclosureIntents = []struct {
	name       string
	intent     string
	published  []string
	disclosure []string
}{
	{
		name: "heading section",
		intent: "Organizer sign-ins are kept on the server so they can be ended.\n\n" +
			"## Disclosure\n\n" +
			"This is a public repository: describe the change as hardening only, and never say \"stolen\" or \"exploit\".\n\n" +
			"## Notes\n\n" +
			"The vendor's remember cookie stays as it is.",
		published:  []string{"Organizer sign-ins are kept on the server", "The vendor's remember cookie stays as it is."},
		disclosure: []string{"Disclosure", "hardening only", "stolen", "exploit"},
	},
	{
		name: "labelled paragraph",
		intent: "Sign-in secrets are stored hashed.\n\n" +
			"Disclosure: commit messages and the PR say what the code now does, never \"attack\" or \"bypass\", and never cite a report.\n\n" +
			"A follow-up removes the old fields after one release.",
		published:  []string{"Sign-in secrets are stored hashed.", "A follow-up removes the old fields after one release."},
		disclosure: []string{"Disclosure", "attack", "bypass", "cite a report"},
	},
	{
		name: "heading with more words",
		intent: "Reset links now expire after one use.\n\n" +
			"## Disclosure rules\n\n" +
			"Say the links are single-use, never \"replay\" or \"takeover\".\n\n" +
			"## Notes\n\n" +
			"Old links stop working at deploy.",
		published:  []string{"Reset links now expire after one use.", "Old links stop working at deploy."},
		disclosure: []string{"Disclosure", "single-use", "replay", "takeover"},
	},
}

func TestPRBodyNeverPublishesTheIntentsDisclosureSection(t *testing.T) {
	t.Parallel()
	for _, tc := range disclosureIntents {
		for _, path := range []string{"ordinary", "ordinary with a body limit", "fallback", "repository template"} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				var sctx *pipeline.StepContext
				limit := 0
				switch path {
				case "repository template":
					sctx, _, _ = templateTestContext(t)
				default:
					dir, base, head := setupGitRepo(t)
					ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
						if path == "fallback" {
							return nil, errors.New("draft unavailable")
						}
						return &agent.Result{Output: json.RawMessage(`{"title":"feat: sign-ins","body":"## What Changed\n\n- Sign-ins are kept on the server."}`)}, nil
					}}
					sctx = newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
					if path == "ordinary with a body limit" {
						limit = 4000
					}
				}
				sctx.UserIntent = tc.intent
				got, err := (&PRStep{}).buildPRContent(sctx, "feature", "main", sctx.Run.BaseSHA, scm.ProviderGitHub, limit)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(got.Body, "## Intent") {
					t.Fatalf("the intent's public part lost its section:\n%s", got.Body)
				}
				for _, want := range tc.published {
					if !strings.Contains(got.Body, want) {
						t.Fatalf("PR body lost the intent's public text %q:\n%s", want, got.Body)
					}
				}
				for _, barred := range tc.disclosure {
					if strings.Contains(got.Body, barred) {
						t.Fatalf("PR body published the Disclosure section's %q:\n%s", barred, got.Body)
					}
				}
			})
		}
	}
}

func TestPRBodyHasNoIntentSectionWhenTheIntentIsOnlyADisclosure(t *testing.T) {
	t.Parallel()
	dir, base, head := setupGitRepo(t)
	ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
		return &agent.Result{Output: json.RawMessage(`{"title":"feat: sign-ins","body":"## What Changed\n\n- Sign-ins are kept on the server."}`)}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})
	sctx.UserIntent = "## Disclosure\n\nNever say \"stolen\" in public text."
	got, err := (&PRStep{}).buildPRContent(sctx, "feature", "main", base, scm.ProviderGitHub, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Body, "## Intent") || strings.Contains(got.Body, "stolen") {
		t.Fatalf("a Disclosure-only intent reached the PR body:\n%s", got.Body)
	}
}

// Every step prompt carries the whole intent, Disclosure included, because
// the section governs what each step writes. When it is present the prompt
// says so; without one the section is exactly what it was.
func TestUserIntentPromptSectionBindsPublishedTextToTheDisclosure(t *testing.T) {
	t.Parallel()
	for _, tc := range disclosureIntents {
		t.Run(tc.name, func(t *testing.T) {
			sctx := &pipeline.StepContext{UserIntent: tc.intent, IntentSource: "agent"}
			got := userIntentPromptSection(sctx)
			if !strings.Contains(got, cleanedUserIntent(sctx)) {
				t.Fatalf("the prompt lost the full intent:\n%s", got)
			}
			if !strings.Contains(got, disclosurePromptClause) {
				t.Fatalf("the prompt does not bind published text to the Disclosure section:\n%s", got)
			}
		})
	}
	plain := &pipeline.StepContext{UserIntent: "Sign-in secrets are stored hashed.", IntentSource: "agent"}
	if strings.Contains(userIntentPromptSection(plain), disclosurePromptClause) {
		t.Fatal("an intent with no Disclosure section got the Disclosure clause")
	}
}

// The caller-side omission withholds the intent from the PR-drafting turns,
// but those turns write the PR title and body, the most public text a run
// produces, from a diff and commit messages that may hold the very words a
// Disclosure section bars. They still get the Disclosure section, alone, with
// the clause that binds what they write to it; the rest stays withheld and the
// body still has no Intent section.
func TestPRDraftingUnderOmissionStillFollowsTheDisclosure(t *testing.T) {
	t.Parallel()
	const withheld = "Organizer sign-ins are kept on the server so they can be ended."
	const rule = "describe the change as hardening only, and never say \"stolen\""
	intentText := withheld + "\n\n## Disclosure\n\n" + rule + ".\n\n## Notes\n\nThe vendor cookie stays."
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (*pipeline.StepContext, *mockAgent)
	}{
		{name: "ordinary narrative", setup: func(t *testing.T) (*pipeline.StepContext, *mockAgent) {
			dir, base, head := setupGitRepo(t)
			ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
				return &agent.Result{Output: json.RawMessage(`{"title":"feat: sign-ins","body":"## What Changed\n\n- Sign-ins are kept on the server."}`)}, nil
			}}
			return newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{}), ag
		}},
		{name: "repository template narrative", setup: func(t *testing.T) (*pipeline.StepContext, *mockAgent) {
			sctx, ag, _ := templateTestContext(t)
			return sctx, ag
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sctx, ag := tc.setup(t)
			sctx.UserIntent = intentText
			sctx.IntentSource = "agent"
			sctx.Run.OmitIntent = true
			got, err := (&PRStep{}).buildPRContent(sctx, "feature", "main", sctx.Run.BaseSHA, scm.ProviderGitHub, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(ag.calls) == 0 {
				t.Fatal("no drafting turn ran")
			}
			for i, call := range ag.calls {
				if !strings.Contains(call.Prompt, rule) {
					t.Errorf("drafting turn %d lost the Disclosure rules:\n%s", i, call.Prompt)
				}
				if !strings.Contains(call.Prompt, omittedIntentDisclosureClause) {
					t.Errorf("drafting turn %d lost the clause binding its text to the Disclosure:\n%s", i, call.Prompt)
				}
				for _, rest := range []string{withheld, "The vendor cookie stays.", "USER INTENT"} {
					if strings.Contains(call.Prompt, rest) {
						t.Errorf("drafting turn %d received withheld intent text %q:\n%s", i, rest, call.Prompt)
					}
				}
			}
			if strings.Contains(got.Body, "## Intent") || strings.Contains(got.Body, "stolen") || strings.Contains(got.Body, withheld) {
				t.Fatalf("PR body published withheld intent:\n%s", got.Body)
			}
		})
	}
}
