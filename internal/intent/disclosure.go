package intent

import (
	"regexp"
	"strings"
)

// SplitDisclosure separates an intent's Disclosure section from the rest of it.
//
// A Disclosure section is the driving agent's rules for what public text may
// say about the change (a security change in a public repository is described
// as hardening, and named words and references are barred). The rules name
// the very words they bar, so the section itself must never be published: the
// PR step publishes rest, never disclosure, while every step prompt keeps the
// whole intent because the rules govern what each step writes.
//
// A section is one of:
//
//   - an ATX or setext heading whose title holds the word "Disclosure"
//     ("Disclosure", "Disclosure rules", "3. Public disclosure"; any level,
//     any case, optionally emphasised), up to the next heading of its level
//     or higher;
//   - a paragraph, list item or block quote that starts with a label: the
//     word itself ("Disclosure:", "**Disclosure**", "Disclosure (...)",
//     "Disclosure - ...", "Disclosure<em dash>..."), or a short phrase holding
//     the word, alone on its line or before a colon or a dash ("Disclosure
//     rules:", "**Disclosure rules**"), up to the end of that block (a list
//     item ends at its next sibling item, a quote takes all of itself); a
//     label with nothing after it also takes the one block that follows it.
//
// Fenced code is never read as a heading or a label. Every section found is
// returned, joined by a blank line; rest keeps everything else in order, with
// line endings normalized to "\n". With no section, rest is the normalized
// input and disclosure is "".
//
// DisclosureRules reads the same sections the other way, for the one outlet
// where too much is the leak.
//
// Recognition errs toward removal: a label that starts a line inside a
// paragraph still starts a section, because a line kept out of the PR body by
// mistake costs nothing while a published rule cannot be taken back.
func SplitDisclosure(text string) (rest, disclosure string) {
	lines, fenced := disclosureLines(text)
	var kept []string
	var sections []string
	next := 0
	for _, sec := range disclosureSections(lines, fenced, 0, len(lines)) {
		kept = append(kept, lines[next:sec.start]...)
		sections = append(sections, sec.text(lines))
		next = sec.next
	}
	if len(sections) == 0 {
		return strings.Join(lines, "\n"), ""
	}
	kept = append(kept, lines[next:]...)
	return strings.Trim(strings.Join(kept, "\n"), "\n"), strings.Join(sections, "\n\n")
}

// DisclosureRules returns the Disclosure sections that read as rules, joined
// by a blank line, or "". It is the text the PR-drafting turns get under the
// caller-side omission, which withholds the rest of the intent, so it errs
// the other way from SplitDisclosure: a heading or label counts only when it
// is the word alone or one of the named rules shapes ("Disclosure",
// "Disclosure rules", "Disclosure policy", "Public disclosure"; numbered,
// emphasised, with a parenthetical or a colon), never a phrase that merely
// holds the word, such as a vulnerability class ("Information disclosure in
// error pages"), which describes the very change the omission withholds.
// SplitDisclosure still removes such a section from the PR body; a rules
// section inside one is still found here.
func DisclosureRules(text string) string {
	lines, fenced := disclosureLines(text)
	return strings.Join(disclosureRuleTexts(lines, fenced, 0, len(lines)), "\n\n")
}

func disclosureRuleTexts(lines []string, fenced []fence, from, to int) []string {
	var rules []string
	for _, sec := range disclosureSections(lines, fenced, from, to) {
		if sec.rules {
			rules = append(rules, sec.text(lines))
			continue
		}
		rules = append(rules, disclosureRuleTexts(lines, fenced, sec.bodyStart, sec.end)...)
	}
	return rules
}

// section is one Disclosure section: lines[start:end], with next past the
// blank lines after it and bodyStart past its heading or label line; rules
// says whether its heading or label reads as rules (DisclosureRules).
type section struct {
	start, bodyStart, end, next int
	rules                       bool
}

func (s section) text(lines []string) string {
	return strings.Trim(strings.Join(lines[s.start:s.end], "\n"), "\n")
}

func disclosureLines(text string) ([]string, []fence) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	return lines, fencedLines(lines)
}

// disclosureSections finds every Disclosure section that starts in
// lines[from:to], each cut off at to.
func disclosureSections(lines []string, fenced []fence, from, to int) []section {
	var sections []section
	for i := from; i < to; {
		end, bodyStart, rules := disclosureEnd(lines, fenced, i)
		if end <= i {
			i++
			continue
		}
		end = min(end, to)
		next := end
		for next < to && isBlank(lines[next]) {
			next++
		}
		sections = append(sections, section{start: i, bodyStart: min(bodyStart, end), end: end, next: next, rules: rules})
		i = next
	}
	return sections
}

var (
	atxHeadingRe     = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	atxClosingRe     = regexp.MustCompile(`[ \t]+#+$`)
	setextUnderRe    = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
	fenceRe          = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	listItemRe       = regexp.MustCompile(`^[ \t]*(?:[-*+]|\d{1,9}[.)])(?:[ \t]+|$)`)
	disclosureWordRe = regexp.MustCompile(`(?i)\bdisclosure\b`)
	disclosureLbRe   = regexp.MustCompile(`(?i)^(?:\*\*|__)?disclosure(?:\*\*|__)?(?:[ \t]*\([^)]*\))?` + labelEndRe)
	// phraseLbRe is a label of a few words ("Disclosure rules:"), which
	// counts only when the words hold disclosure (disclosureWordRe) and a
	// colon, a dash, or the end of the line ends them, so prose that merely
	// starts with the word is never a label.
	phraseLbRe = regexp.MustCompile(`^(?:\*\*|__)?([^:.!?()*_\x{2013}\x{2014}]{1,60}?)(?:\*\*|__)?(?:[ \t]*\([^)]*\))?` + labelEndRe)
	// rulesTitleRe is a heading title or label that reads as rules
	// (DisclosureRules), once normalized by rulesTitle.
	rulesTitleRe   = regexp.MustCompile(`^(?:public )?disclosure(?: rules| policy)?$`)
	numberPrefixRe = regexp.MustCompile(`^\d{1,9}[.)][ \t]*`)
	parentheticRe  = regexp.MustCompile(`[ \t]*\([^)]*\)`)
	quoteRe        = regexp.MustCompile(`^ {0,3}>`)
	quoteMkRe      = regexp.MustCompile(`^(?:[ \t]*>[ \t]?)+`)
)

// labelEndRe ends a label: a colon (and the emphasis that closes it), a
// hyphen between spaces, an en or em dash with or without spaces, or the end
// of the line, which makes the label one with nothing after it.
const labelEndRe = `(?:[ \t]*:[ \t]*(?:\*\*|__)?|[ \t]+-[ \t]+|[ \t]*[\x{2013}\x{2014}][ \t]*|[ \t]*$)`

// rulesTitle normalizes a heading title or label for rulesTitleRe: no
// emphasis, numbering, parenthetical, or trailing colon, lower case, single
// spaces.
func rulesTitle(title string) string {
	title = headingTitle(title)
	title = numberPrefixRe.ReplaceAllString(title, "")
	title = parentheticRe.ReplaceAllString(title, "")
	title = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(title), ":"))
	return strings.ToLower(strings.Join(strings.Fields(title), " "))
}

// disclosureEnd returns the end (exclusive) of the Disclosure section that
// starts at line i, or i when no section starts there, with the first line of
// its body and whether its heading or label reads as rules.
func disclosureEnd(lines []string, fenced []fence, i int) (end, bodyStart int, rules bool) {
	if fenced[i] != fenceNone {
		return i, i, false
	}
	if level, title, ok := headingAt(lines, fenced, i); ok {
		if !disclosureWordRe.MatchString(headingTitle(title)) {
			return i, i, false
		}
		j := i + 1
		if !atxHeadingRe.MatchString(lines[i]) {
			j = i + 2 // a setext heading's underline
		}
		bodyStart = j
		for ; j < len(lines); j++ {
			if next, _, ok := headingAt(lines, fenced, j); ok && next <= level {
				break
			}
		}
		return j, bodyStart, rulesTitleRe.MatchString(rulesTitle(title))
	}
	return labelEnd(lines, fenced, i)
}

// labelEnd returns the end of a labelled Disclosure paragraph, list item or
// block quote starting at line i, or i when line i carries no label, with the
// line after the label's and whether the label reads as rules.
func labelEnd(lines []string, fenced []fence, i int) (int, int, bool) {
	line := lines[i]
	quoted := quoteRe.MatchString(line)
	if quoted {
		line = line[len(quoteMkRe.FindString(line)):]
	}
	item := listItemRe.FindString(line)
	content := strings.TrimSpace(line[len(item):])
	match, rules := disclosureLabel(content)
	if match == "" {
		return i, i, false
	}
	indent := indentWidth(line)
	needBlock := strings.TrimSpace(content[len(match):]) == ""
	sawBlank, sawContent := false, false
	j := i + 1
	if quoted {
		// A block-quoted label takes the whole quote, lazy continuation
		// lines included, and an empty one also the block after it.
		for ; j < len(lines) && !isBlank(lines[j]); j++ {
			if quoteRe.MatchString(lines[j]) {
				if !isBlank(lines[j][len(quoteMkRe.FindString(lines[j])):]) {
					sawContent = true
				}
				continue
			}
			if _, _, ok := headingAt(lines, fenced, j); ok || fenced[j] != fenceNone {
				break
			}
			sawContent = true
		}
		if !needBlock || sawContent || j >= len(lines) || !isBlank(lines[j]) {
			return j, i + 1, rules
		}
		indent, item = 0, ""
	}
	for ; j < len(lines); j++ {
		if fenced[j] == fenceBody {
			continue // inside a fence this block already took
		}
		if isBlank(lines[j]) {
			sawBlank = true
			continue
		}
		if _, _, ok := headingAt(lines, fenced, j); ok {
			break
		}
		if indentWidth(lines[j]) > indent {
			sawBlank, sawContent = false, true
			continue
		}
		if item != "" && listItemRe.MatchString(lines[j]) {
			break
		}
		if sawBlank && (!needBlock || sawContent) {
			break
		}
		sawBlank, sawContent = false, true
	}
	return j, i + 1, rules
}

// disclosureLabel returns the Disclosure label that starts content, or "",
// and whether it reads as rules (rulesTitleRe): the word itself
// ("Disclosure:", "**Disclosure**", "Disclosure (...)", "Disclosure - ..."),
// alone or before a colon or a dash, or a short phrase holding the word,
// alone on its line or before one ("Disclosure rules:", "**Disclosure
// rules**").
func disclosureLabel(content string) (string, bool) {
	if match := disclosureLbRe.FindString(content); match != "" {
		return match, true
	}
	if m := phraseLbRe.FindStringSubmatch(content); m != nil && disclosureWordRe.MatchString(m[1]) {
		return m[0], rulesTitleRe.MatchString(rulesTitle(m[1]))
	}
	return "", false
}

// headingAt reports whether line i opens an ATX heading or is the text line
// of a setext heading, with its level and raw title.
func headingAt(lines []string, fenced []fence, i int) (int, string, bool) {
	if fenced[i] != fenceNone {
		return 0, "", false
	}
	if m := atxHeadingRe.FindStringSubmatch(lines[i]); m != nil {
		return len(m[1]), atxClosingRe.ReplaceAllString(m[2], ""), true
	}
	if i+1 >= len(lines) || fenced[i+1] != fenceNone || isBlank(lines[i]) || listItemRe.MatchString(lines[i]) {
		return 0, "", false
	}
	m := setextUnderRe.FindStringSubmatch(lines[i+1])
	if m == nil {
		return 0, "", false
	}
	level := 2
	if m[1][0] == '=' {
		level = 1
	}
	return level, lines[i], true
}

// headingTitle strips emphasis and a trailing colon from a heading's title.
func headingTitle(title string) string {
	title = strings.TrimSpace(strings.NewReplacer("**", "", "__", "", "*", "", "_", "").Replace(title))
	return strings.TrimSpace(strings.TrimSuffix(title, ":"))
}

// fence is a line's place in fenced code: outside it, its opening line, or
// a line after the opening one (its body or closing line).
type fence uint8

const (
	fenceNone fence = iota
	fenceOpen
	fenceBody
)

// fencedLines marks every line that opens, closes, or sits inside a fenced
// code block. An unclosed fence runs to the end, as Markdown renders it.
func fencedLines(lines []string) []fence {
	fenced := make([]fence, len(lines))
	marker := ""
	for i, line := range lines {
		if marker != "" {
			fenced[i] = fenceBody
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, marker) && strings.Trim(trimmed, marker[:1]) == "" {
				marker = ""
			}
			continue
		}
		if m := fenceRe.FindStringSubmatch(line); m != nil {
			if m[1][0] == '`' && strings.Contains(m[2], "`") {
				continue // an inline code span, not a fence
			}
			fenced[i] = fenceOpen
			marker = m[1]
		}
	}
	return fenced
}

func indentWidth(line string) int {
	width := 0
	for _, r := range line {
		switch r {
		case ' ':
			width++
		case '\t':
			width += 4
		default:
			return width
		}
	}
	return width
}

func isBlank(line string) bool { return strings.TrimSpace(line) == "" }
