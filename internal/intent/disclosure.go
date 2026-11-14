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
//     "Disclosure - ..."), or a short phrase holding the word before a colon
//     or a spaced dash ("Disclosure rules:"), up to the end of that block (a
//     list item ends at its next sibling item, a quote takes all of itself);
//     a label with nothing after it also takes the one block that follows it.
//
// Fenced code is never read as a heading or a label. Every section found is
// returned, joined by a blank line; rest keeps everything else in order, with
// line endings normalized to "\n". With no section, rest is the normalized
// input and disclosure is "".
//
// Recognition errs toward removal: a label that starts a line inside a
// paragraph still starts a section, because a line kept out of the PR body by
// mistake costs nothing while a published rule cannot be taken back.
func SplitDisclosure(text string) (rest, disclosure string) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	fenced := fencedLines(lines)

	var kept []string
	var sections []string
	for i := 0; i < len(lines); {
		end := disclosureEnd(lines, fenced, i)
		if end <= i {
			kept = append(kept, lines[i])
			i++
			continue
		}
		sections = append(sections, strings.Trim(strings.Join(lines[i:end], "\n"), "\n"))
		for end < len(lines) && isBlank(lines[end]) {
			end++
		}
		i = end
	}
	if len(sections) == 0 {
		return text, ""
	}
	return strings.Trim(strings.Join(kept, "\n"), "\n"), strings.Join(sections, "\n\n")
}

var (
	atxHeadingRe     = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	atxClosingRe     = regexp.MustCompile(`[ \t]+#+$`)
	setextUnderRe    = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
	fenceRe          = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})(.*)$")
	listItemRe       = regexp.MustCompile(`^[ \t]*(?:[-*+]|\d{1,9}[.)])(?:[ \t]+|$)`)
	disclosureWordRe = regexp.MustCompile(`(?i)\bdisclosure\b`)
	disclosureLbRe   = regexp.MustCompile(`(?i)^(?:\*\*|__)?disclosure(?:\*\*|__)?(?:[ \t]*\([^)]*\))?[ \t]*(?::[ \t]*(?:\*\*|__)?|[ \t]+[-\x{2013}\x{2014}][ \t]+|$)`)
	// phraseLbRe is a label of a few words ("Disclosure rules:"), which
	// counts only when the words hold disclosure (disclosureWordRe) and a
	// colon or a spaced dash ends them, so prose that merely starts with the
	// word is never a label.
	phraseLbRe = regexp.MustCompile(`^(?:\*\*|__)?([^:.!?()*_\x{2013}\x{2014}]{1,60}?)(?:\*\*|__)?(?:[ \t]*\([^)]*\))?(?:[ \t]*:[ \t]*(?:\*\*|__)?|[ \t]+[-\x{2013}\x{2014}][ \t]+)`)
	quoteRe    = regexp.MustCompile(`^ {0,3}>`)
	quoteMkRe  = regexp.MustCompile(`^(?:[ \t]*>[ \t]?)+`)
)

// disclosureEnd returns the end (exclusive) of the Disclosure section that
// starts at line i, or i when no section starts there.
func disclosureEnd(lines []string, fenced []fence, i int) int {
	if fenced[i] != fenceNone {
		return i
	}
	if level, title, ok := headingAt(lines, fenced, i); ok {
		if !disclosureWordRe.MatchString(headingTitle(title)) {
			return i
		}
		j := i + 1
		if !atxHeadingRe.MatchString(lines[i]) {
			j = i + 2 // a setext heading's underline
		}
		for ; j < len(lines); j++ {
			if next, _, ok := headingAt(lines, fenced, j); ok && next <= level {
				break
			}
		}
		return j
	}
	return labelEnd(lines, fenced, i)
}

// labelEnd returns the end of a labelled Disclosure paragraph or list item
// starting at line i, or i when line i carries no label.
func labelEnd(lines []string, fenced []fence, i int) int {
	line := lines[i]
	quoted := quoteRe.MatchString(line)
	if quoted {
		line = line[len(quoteMkRe.FindString(line)):]
	}
	item := listItemRe.FindString(line)
	content := strings.TrimSpace(line[len(item):])
	match := disclosureLabel(content)
	if match == "" {
		return i
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
			return j
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
	return j
}

// disclosureLabel returns the Disclosure label that starts content, or "":
// the word itself ("Disclosure:", "**Disclosure**", "Disclosure (...)"),
// alone or before a colon or a spaced dash, or a short phrase holding the
// word before one ("Disclosure rules:").
func disclosureLabel(content string) string {
	if match := disclosureLbRe.FindString(content); match != "" {
		return match
	}
	if m := phraseLbRe.FindStringSubmatch(content); m != nil && disclosureWordRe.MatchString(m[1]) {
		return m[0]
	}
	return ""
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
