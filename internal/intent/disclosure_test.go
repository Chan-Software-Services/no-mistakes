package intent

import "testing"

func TestSplitDisclosure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		in         string
		rest       string
		disclosure string
	}{
		{
			name:       "no disclosure",
			in:         "Store sign-in secrets hashed.\n\nKeep the vendor cookie.",
			rest:       "Store sign-in secrets hashed.\n\nKeep the vendor cookie.",
			disclosure: "",
		},
		{
			name:       "heading runs to the next heading of its level",
			in:         "Goal.\n\n## Disclosure\n\nNever say \"stolen\".\n\n### Words\n\n- \"attack\"\n\n## Notes\n\nKeep it.",
			rest:       "Goal.\n\n## Notes\n\nKeep it.",
			disclosure: "## Disclosure\n\nNever say \"stolen\".\n\n### Words\n\n- \"attack\"",
		},
		{
			name:       "heading runs to a higher heading",
			in:         "# Plan\n\n### Disclosure\n\nNever say \"stolen\".\n\n# Next\n\nKeep it.",
			rest:       "# Plan\n\n# Next\n\nKeep it.",
			disclosure: "### Disclosure\n\nNever say \"stolen\".",
		},
		{
			name:       "heading runs to the end",
			in:         "Goal.\n\n## Disclosure:\n\nNever say \"stolen\".\n\nNever cite the report.",
			rest:       "Goal.",
			disclosure: "## Disclosure:\n\nNever say \"stolen\".\n\nNever cite the report.",
		},
		{
			name:       "emphasised heading and any case",
			in:         "Goal.\n\n## **DISCLOSURE** ##\n\nNever say \"stolen\".",
			rest:       "Goal.",
			disclosure: "## **DISCLOSURE** ##\n\nNever say \"stolen\".",
		},
		{
			name:       "setext heading",
			in:         "Goal.\n\nDisclosure\n----------\n\nNever say \"stolen\".\n\nNotes\n-----\n\nKeep it.",
			rest:       "Goal.\n\nNotes\n-----\n\nKeep it.",
			disclosure: "Disclosure\n----------\n\nNever say \"stolen\".",
		},
		{
			name:       "labelled paragraph",
			in:         "Goal.\n\nDisclosure: never say \"stolen\",\nnor \"attack\".\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "Disclosure: never say \"stolen\",\nnor \"attack\".",
		},
		{
			name:       "bold label",
			in:         "Goal.\n\n**Disclosure:** never say \"stolen\".\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "**Disclosure:** never say \"stolen\".",
		},
		{
			name:       "label with a parenthetical",
			in:         "Goal.\n\n**Disclosure** (public repository): never say \"stolen\".\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "**Disclosure** (public repository): never say \"stolen\".",
		},
		{
			name:       "empty label takes the block after it",
			in:         "Goal.\n\n**Disclosure**\n\n- never say \"stolen\"\n- never cite the report\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "**Disclosure**\n\n- never say \"stolen\"\n- never cite the report",
		},
		{
			name:       "empty label with its list directly below",
			in:         "Goal.\n\nDisclosure:\n- never say \"stolen\"\n- never cite the report\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "Disclosure:\n- never say \"stolen\"\n- never cite the report",
		},
		{
			name:       "list item stops at its sibling",
			in:         "Goal:\n- Hash the secrets.\n- Disclosure: never say \"stolen\".\n  Never cite the report.\n- Keep the vendor cookie.",
			rest:       "Goal:\n- Hash the secrets.\n- Keep the vendor cookie.",
			disclosure: "- Disclosure: never say \"stolen\".\n  Never cite the report.",
		},
		{
			name:       "list item keeps its nested items",
			in:         "- Hash the secrets.\n- Disclosure:\n  - never say \"stolen\"\n  - never cite the report\n- Keep it.",
			rest:       "- Hash the secrets.\n- Keep it.",
			disclosure: "- Disclosure:\n  - never say \"stolen\"\n  - never cite the report",
		},
		{
			name:       "every disclosure section",
			in:         "Disclosure: never say \"stolen\".\n\nGoal.\n\n## Disclosure\n\nNever say \"attack\".",
			rest:       "Goal.",
			disclosure: "Disclosure: never say \"stolen\".\n\n## Disclosure\n\nNever say \"attack\".",
		},
		{
			name:       "fenced code is never a heading",
			in:         "Goal.\n\n```md\n## Disclosure\nDisclosure: example\n```\n\nKeep it.",
			rest:       "Goal.\n\n```md\n## Disclosure\nDisclosure: example\n```\n\nKeep it.",
			disclosure: "",
		},
		{
			name:       "a fence inside the section stays in it",
			in:         "## Disclosure\n\n```\n# not a heading\n```\n\nNever say \"stolen\".\n\n## Notes\n\nKeep it.",
			rest:       "## Notes\n\nKeep it.",
			disclosure: "## Disclosure\n\n```\n# not a heading\n```\n\nNever say \"stolen\".",
		},
		{
			name:       "a fence after the labelled paragraph is its own block",
			in:         "Disclosure: never say \"stolen\".\n\n```sh\nmake test\n```",
			rest:       "```sh\nmake test\n```",
			disclosure: "Disclosure: never say \"stolen\".",
		},
		{
			name:       "an empty label takes a fence that follows it",
			in:         "Disclosure:\n\n```text\nstolen\n```\n\n```text\nkept\n```",
			rest:       "```text\nkept\n```",
			disclosure: "Disclosure:\n\n```text\nstolen\n```",
		},
		{
			name:       "the word in prose is not a section",
			in:         "Disclosure of a token is now impossible.\n\nThe Disclosure section below is empty.\n\n## Disclosures and notices\n\nKeep it.",
			rest:       "Disclosure of a token is now impossible.\n\nThe Disclosure section below is empty.\n\n## Disclosures and notices\n\nKeep it.",
			disclosure: "",
		},
		{
			name:       "heading with more words",
			in:         "Goal.\n\n## Disclosure rules\n\nNever say \"stolen\".\n\n## Notes\n\nKeep it.",
			rest:       "Goal.\n\n## Notes\n\nKeep it.",
			disclosure: "## Disclosure rules\n\nNever say \"stolen\".",
		},
		{
			name:       "heading with the word last",
			in:         "Goal.\n\n## Public disclosure\n\nNever say \"stolen\".\n\n## Notes\n\nKeep it.",
			rest:       "Goal.\n\n## Notes\n\nKeep it.",
			disclosure: "## Public disclosure\n\nNever say \"stolen\".",
		},
		{
			name:       "policy heading",
			in:         "Goal.\n\n## Disclosure policy\n\nNever say \"stolen\".\n\n## Notes\n\nKeep it.",
			rest:       "Goal.\n\n## Notes\n\nKeep it.",
			disclosure: "## Disclosure policy\n\nNever say \"stolen\".",
		},
		{
			name:       "numbered heading",
			in:         "Goal.\n\n## 3. Disclosure\n\nNever say \"stolen\".\n\n## 4. Notes\n\nKeep it.",
			rest:       "Goal.\n\n## 4. Notes\n\nKeep it.",
			disclosure: "## 3. Disclosure\n\nNever say \"stolen\".",
		},
		{
			name:       "label with more words",
			in:         "Goal.\n\nDisclosure rules: never say \"stolen\".\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "Disclosure rules: never say \"stolen\".",
		},
		{
			name:       "label followed by an em dash",
			in:         "Goal.\n\nDisclosure — never say \"stolen\".\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "Disclosure — never say \"stolen\".",
		},
		{
			name:       "block-quoted label",
			in:         "Goal.\n\n> Disclosure: never say \"stolen\".\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "> Disclosure: never say \"stolen\".",
		},
		{
			name:       "block-quoted label runs to the end of the quote",
			in:         "Goal.\n\n> Disclosure: never say \"stolen\",\n> nor \"attack\".\nlazily continued.\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "> Disclosure: never say \"stolen\",\n> nor \"attack\".\nlazily continued.",
		},
		{
			name:       "an empty block-quoted label takes the block after it",
			in:         "Goal.\n\n> **Disclosure**\n\n- never say \"stolen\"\n- never cite the report\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "> **Disclosure**\n\n- never say \"stolen\"\n- never cite the report",
		},
		{
			name:       "bold label with more words",
			in:         "Goal.\n\n- **Public disclosure rules:** never say \"stolen\".\n- Keep it.",
			rest:       "Goal.\n\n- Keep it.",
			disclosure: "- **Public disclosure rules:** never say \"stolen\".",
		},
		{
			name:       "the word inside a sentence is not a label",
			in:         "Prevent the disclosure of tokens in logs.\n\nGoal: stop token disclosure.\n\nKeep it.",
			rest:       "Prevent the disclosure of tokens in logs.\n\nGoal: stop token disclosure.\n\nKeep it.",
			disclosure: "",
		},
		{
			name:       "bold label of more words alone on its line",
			in:         "Goal.\n\n**Disclosure rules**\n\n- never say \"stolen\"\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "**Disclosure rules**\n\n- never say \"stolen\"",
		},
		{
			name:       "plain label of more words alone on its line",
			in:         "Goal.\n\nPublic disclosure rules\n- never say \"stolen\"\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "Public disclosure rules\n- never say \"stolen\"",
		},
		{
			name:       "label followed by an unspaced em dash",
			in:         "Goal.\n\nDisclosure\u2014never say \"stolen\".\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "Disclosure\u2014never say \"stolen\".",
		},
		{
			name:       "phrase label followed by an unspaced en dash",
			in:         "Goal.\n\nDisclosure rules\u2013never say \"stolen\".\n\nKeep it.",
			rest:       "Goal.\n\nKeep it.",
			disclosure: "Disclosure rules\u2013never say \"stolen\".",
		},
		{
			name:       "a vulnerability heading is still removed from the body",
			in:         "Goal.\n\n## Information disclosure fix\n\nThe error page printed X.\n\n## Notes\n\nKeep it.",
			rest:       "Goal.\n\n## Notes\n\nKeep it.",
			disclosure: "## Information disclosure fix\n\nThe error page printed X.",
		},
		{
			name:       "an ordinary quote is kept",
			in:         "Goal.\n\n> The disclosure of a token is now impossible.\n\nKeep it.",
			rest:       "Goal.\n\n> The disclosure of a token is now impossible.\n\nKeep it.",
			disclosure: "",
		},
		{
			name:       "carriage returns",
			in:         "Goal.\r\n\r\n## Disclosure\r\n\r\nNever say \"stolen\".\r\n\r\n## Notes\r\nKeep it.",
			rest:       "Goal.\n\n## Notes\nKeep it.",
			disclosure: "## Disclosure\n\nNever say \"stolen\".",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rest, disclosure := SplitDisclosure(tc.in)
			if rest != tc.rest {
				t.Errorf("rest:\n got %q\nwant %q", rest, tc.rest)
			}
			if disclosure != tc.disclosure {
				t.Errorf("disclosure:\n got %q\nwant %q", disclosure, tc.disclosure)
			}
		})
	}
}

// DisclosureRules is what the PR-drafting turns get under the caller-side
// omission, so it must err the other way from SplitDisclosure: a section
// whose heading or label only names a vulnerability class ("Information
// disclosure") is removed from the PR body but is never handed over as the
// rules, since it describes the change the omission withholds. A real
// Disclosure section inside such a section is still found.
func TestDisclosureRules(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		in    string
		rules string
	}{
		{name: "heading", in: "Goal.\n\n## Disclosure\n\nSay hardening only.\n\n## Notes\n\nKeep it.", rules: "## Disclosure\n\nSay hardening only."},
		{name: "rules heading", in: "## Disclosure rules\n\nSay hardening only.", rules: "## Disclosure rules\n\nSay hardening only."},
		{name: "policy heading", in: "## Disclosure policy\n\nSay hardening only.", rules: "## Disclosure policy\n\nSay hardening only."},
		{name: "qualified heading", in: "## Public disclosure\n\nSay hardening only.", rules: "## Public disclosure\n\nSay hardening only."},
		{name: "numbered heading", in: "## 3. Disclosure\n\nSay hardening only.", rules: "## 3. Disclosure\n\nSay hardening only."},
		{name: "label", in: "Goal.\n\nDisclosure: say hardening only.", rules: "Disclosure: say hardening only."},
		{name: "phrase label", in: "Disclosure rules: say hardening only.", rules: "Disclosure rules: say hardening only."},
		{name: "bold label with a parenthetical", in: "**Disclosure** (public repository): say hardening only.", rules: "**Disclosure** (public repository): say hardening only."},
		{name: "label alone on its line", in: "**Disclosure rules**\n\n- say hardening only", rules: "**Disclosure rules**\n\n- say hardening only"},
		{name: "list item", in: "- Goal.\n- Disclosure: say hardening only.", rules: "- Disclosure: say hardening only."},
		{name: "quoted label", in: "> Disclosure: say hardening only.", rules: "> Disclosure: say hardening only."},
		{name: "unspaced em dash", in: "Disclosure\u2014say hardening only.", rules: "Disclosure\u2014say hardening only."},
		{name: "vulnerability heading", in: "Goal.\n\n## Information disclosure fix\n\nThe error page printed X.\n\n## Notes\n\nKeep it.", rules: ""},
		{name: "vulnerability title", in: "# Stop token disclosure in logs\n\nTokens were logged.", rules: ""},
		{name: "vulnerability label", in: "Information disclosure: the error page printed the stack trace.", rules: ""},
		{name: "vulnerability line alone", in: "Information disclosure fix\n\nThe error page printed X.", rules: ""},
		{
			name:  "a real section inside a vulnerability heading's section",
			in:    "# Stop information disclosure in error pages\n\nThe error page printed the stack trace.\n\n## Disclosure\n\nSay hardening only.",
			rules: "## Disclosure\n\nSay hardening only.",
		},
		{
			name:  "a real label beside a vulnerability label",
			in:    "Information disclosure: the error page printed the stack trace.\n\nDisclosure: say hardening only.",
			rules: "Disclosure: say hardening only.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisclosureRules(tc.in); got != tc.rules {
				t.Errorf("rules:\n got %q\nwant %q", got, tc.rules)
			}
			if _, disclosure := SplitDisclosure(tc.in); disclosure == "" {
				t.Errorf("SplitDisclosure no longer removes %q from the PR body", tc.in)
			}
		})
	}
}
