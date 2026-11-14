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
