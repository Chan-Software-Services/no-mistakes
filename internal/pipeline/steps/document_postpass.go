package steps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/git"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

// verifiedDateLayout is the one date shape the guard reads; any other value
// is not a date it judges.
const verifiedDateLayout = "2006-01-02"

// emDash is U+2014, which the step never leaves on a Markdown line it wrote.
const emDash = "\u2014"

// markdownEdit is one Markdown file the document step changed: its content at
// the step's starting head (absent when the file did not exist there) and in
// the worktree now.
type markdownEdit struct {
	path   string
	before []byte
	absent bool
	after  []byte
}

// documentPostPass is the deterministic pass the document step runs on its
// own edits after the agent returns and before they are committed, so every
// pass that commits document agent edits (the document pass, the combined
// housekeeping pass, and a manual fix round) goes through it. output is the
// agent's structured output, trusted only when it validates as the step's
// findings.
func documentPostPass(sctx *pipeline.StepContext, startHead string, output []byte) error {
	edits, err := changedMarkdownFiles(sctx, startHead)
	if err != nil {
		return fmt.Errorf("document post-pass: %w", err)
	}
	if len(edits) == 0 {
		return nil
	}
	readSet := documentReadSet(output, sctx.WorkDir)
	today := time.Now().Format(verifiedDateLayout)
	for _, edit := range edits {
		written, err := linesWrittenSince(sctx, startHead, edit)
		if err != nil {
			return fmt.Errorf("document post-pass: %w", err)
		}
		original := edit.after
		edit.after = rewriteEmDashOnWrittenLines(sctx, edit, written)
		content := guardVerifiedDate(sctx, edit, readSet, today)
		if bytes.Equal(content, original) {
			continue
		}
		if err := os.WriteFile(filepath.Join(sctx.WorkDir, filepath.FromSlash(edit.path)), content, 0o644); err != nil {
			return fmt.Errorf("document post-pass: write %s: %w", edit.path, err)
		}
	}
	return nil
}

// changedMarkdownFiles lists the .md and .mdx files whose worktree content
// differs from startHead, tracked or new, with both versions. A file the step
// deleted has nothing left to guard.
func changedMarkdownFiles(sctx *pipeline.StepContext, startHead string) ([]markdownEdit, error) {
	tracked, err := git.RunRaw(sctx.Ctx, sctx.WorkDir, "diff", "--name-only", "--no-renames", "-z", startHead)
	if err != nil {
		return nil, fmt.Errorf("list changed files: %w", err)
	}
	untracked, err := git.RunRaw(sctx.Ctx, sctx.WorkDir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("list new files: %w", err)
	}
	var edits []markdownEdit
	seen := map[string]bool{}
	for _, name := range strings.Split(string(tracked)+string(untracked), "\x00") {
		if name == "" || seen[name] || !isMarkdownPath(name) {
			continue
		}
		seen[name] = true
		after, err := os.ReadFile(filepath.Join(sctx.WorkDir, filepath.FromSlash(name)))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		edit := markdownEdit{path: name, after: after}
		listed, err := git.RunRaw(sctx.Ctx, sctx.WorkDir, "ls-tree", "-z", "--name-only", startHead, "--", name)
		if err != nil {
			return nil, fmt.Errorf("look up %s at %s: %w", name, startHead, err)
		}
		if len(listed) == 0 {
			edit.absent = true
		} else if edit.before, err = git.RunRaw(sctx.Ctx, sctx.WorkDir, "show", startHead+":"+name); err != nil {
			return nil, fmt.Errorf("read %s at %s: %w", name, startHead, err)
		}
		edits = append(edits, edit)
	}
	return edits, nil
}

func isMarkdownPath(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".md" || ext == ".mdx"
}

// documentReadSet is the set of docs the agent reported reading in full
// against the code in this pass. An output that does not validate as the
// step's findings, or whose read_against_code is not a list of strings, gives
// an empty set, so the guard fails toward restoring.
func documentReadSet(output []byte, workDir string) map[string]bool {
	readSet := map[string]bool{}
	var findings Findings
	if output == nil || unmarshalRequiredFindings(output, &findings, true) != nil {
		return readSet
	}
	var payload struct {
		ReadAgainstCode []string `json:"read_against_code"`
	}
	if json.Unmarshal(output, &payload) != nil {
		return readSet
	}
	for _, reported := range payload.ReadAgainstCode {
		if name := repoRelativePath(reported, workDir); name != "" {
			readSet[name] = true
		}
	}
	return readSet
}

// repoRelativePath normalizes an agent-reported path to the slash-separated,
// repository-relative form git lists, or "" when it is outside the worktree.
func repoRelativePath(reported, workDir string) string {
	reported = strings.TrimSpace(reported)
	if reported == "" {
		return ""
	}
	if filepath.IsAbs(reported) {
		rel, err := filepath.Rel(workDir, reported)
		if err != nil {
			return ""
		}
		reported = rel
	}
	cleaned := path.Clean(filepath.ToSlash(reported))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

// linesWrittenSince marks each line of the edit's worktree content that the
// step added or changed relative to startHead: every line of a new file, and
// otherwise the added lines of git's diff, read from the hunk bodies so a
// configured inter-hunk context cannot widen them.
func linesWrittenSince(sctx *pipeline.StepContext, startHead string, edit markdownEdit) ([]bool, error) {
	written := make([]bool, len(splitLinesKeepEnds(string(edit.after))))
	if edit.absent {
		for i := range written {
			written[i] = true
		}
		return written, nil
	}
	diff, err := git.RunRaw(sctx.Ctx, sctx.WorkDir, "--literal-pathspecs", "diff", "--no-color", "--no-ext-diff",
		"--no-textconv", "--text", "--no-renames", "--unified=0", "--inter-hunk-context=0", startHead, "--", edit.path)
	if err != nil {
		return nil, fmt.Errorf("diff %s against %s: %w", edit.path, startHead, err)
	}
	next, inHunk := 0, false
	for _, line := range strings.Split(string(diff), "\n") {
		if strings.HasPrefix(line, "@@ ") {
			start, ok := hunkNewStart(line)
			if !ok {
				return nil, fmt.Errorf("diff %s against %s: unreadable hunk header %q", edit.path, startHead, line)
			}
			next, inHunk = start, true
			continue
		}
		if !inHunk || line == "" {
			continue
		}
		switch line[0] {
		case '+':
			if next >= 1 && next <= len(written) {
				written[next-1] = true
			}
			next++
		case ' ':
			next++
		}
	}
	return written, nil
}

// hunkNewStart reads the first new-side line number of a unified diff hunk
// header ("@@ -a,b +c,d @@").
func hunkNewStart(header string) (int, bool) {
	fields := strings.Fields(header)
	if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
		return 0, false
	}
	start, _, _ := strings.Cut(fields[2][1:], ",")
	n, err := strconv.Atoi(start)
	if err != nil {
		return 0, false
	}
	// A hunk that adds nothing names the line before it; it has no '+' or
	// context lines, so its start is never used.
	return n, true
}

// rewriteEmDashOnWrittenLines returns the edit's content with every em dash
// on a line the step wrote replaced by a plain dash; lines it did not write,
// such as a quoted rule, are left as they were, and so is an em dash in a
// fenced code block, an indented code block of a .md file, or an inline code
// span, whose meaning is the character. An .mdx file has no indented code
// blocks, so its indented lines are text.
func rewriteEmDashOnWrittenLines(sctx *pipeline.StepContext, edit markdownEdit, written []bool) []byte {
	content := string(edit.after)
	code := markdownCodeIn(content, strings.ToLower(path.Ext(edit.path)) == ".md")
	lines := splitLinesKeepEnds(content)
	total, offset := 0, 0
	for i, line := range lines {
		start := offset
		offset += len(line)
		if i >= len(written) || !written[i] {
			continue
		}
		rewritten, n := rewriteEmDash(line, code[start:offset])
		lines[i] = rewritten
		total += n
	}
	if total == 0 {
		return edit.after
	}
	sctx.Log(fmt.Sprintf("rewrote %d em dash(es) in %s", total, edit.path))
	return []byte(strings.Join(lines, ""))
}

// rewriteEmDash replaces each em dash in line that code does not mark with
// "-" and counts them. That one replacement turns " — " into " - " and any
// other em dash into "-". code holds one entry per byte of line.
func rewriteEmDash(line string, code []bool) (string, int) {
	var b strings.Builder
	n := 0
	for i := 0; i < len(line); {
		if strings.HasPrefix(line[i:], emDash) && !code[i] {
			b.WriteByte('-')
			i += len(emDash)
			n++
			continue
		}
		b.WriteByte(line[i])
		i++
	}
	if n == 0 {
		return line, 0
	}
	return b.String(), n
}

// markdownCode marks the bytes of content that are Markdown code, reading it
// as a .md file does: markdownCodeIn with indented code blocks.
func markdownCode(content string) []bool {
	return markdownCodeIn(content, true)
}

// markdownCodeIn marks the bytes of content that are Markdown code: every line
// of a fenced code block, its fences included (an unclosed fence runs to the
// end), every inline code span, matched across the lines of a paragraph and
// within an ATX heading the way CommonMark matches them, and, when
// indentedCode, every line of an indented code block. Fences are found after
// any indentation, blockquote markers or list marker, so a fence nested in a
// list or a quote is still code.
//
// Outside a paragraph, an indented code line is a line indented at least four
// columns past the content column of the innermost open list item that does
// not start past it (column 0 outside a list), or a list item whose text
// starts five or more columns after its marker. A paragraph runs from its
// first other line to a blank line, a fence or an ATX heading, and every later
// line in it is a lazy continuation, never code, however far it is indented. An
// indented code block in a blockquote is not recognized, a setext heading's
// text reads as a paragraph, and list structure is read no further than this.
func markdownCodeIn(content string, indentedCode bool) []bool {
	code := make([]bool, len(content))
	fence := ""     // the open fence's marker run, "" outside a fence
	paragraph := -1 // byte offset of the open paragraph's first line
	var items []int // content columns of the open list items, outermost first
	closeParagraph := func(end int) {
		if paragraph >= 0 {
			markCodeSpans(content[paragraph:end], code[paragraph:end])
			paragraph = -1
		}
	}
	offset := 0
	for _, line := range splitLinesKeepEnds(content) {
		start := offset
		offset += len(line)
		body := fenceCandidate(line)
		switch {
		case fence != "":
			markBytes(code[start:offset])
			if closesFence(body, fence) {
				fence = ""
			}
			continue
		case openingFence(body) != "":
			closeParagraph(start)
			fence = openingFence(body)
			markBytes(code[start:offset])
			continue
		case strings.TrimSpace(line) == "":
			closeParagraph(start)
			continue
		}
		col, rest := indentColumns(line, 0)
		if paragraph < 0 {
			for len(items) > 0 && items[len(items)-1] > col {
				items = items[:len(items)-1]
			}
			base := 0
			if len(items) > 0 {
				base = items[len(items)-1]
			}
			if indentedCode && col >= base+4 {
				markBytes(code[start:offset])
				continue
			}
		}
		if width, after := listMarker(rest); width > 0 {
			if paragraph >= 0 {
				for len(items) > 0 && items[len(items)-1] > col {
					items = items[:len(items)-1]
				}
			}
			gap, _ := indentColumns(after, col+width)
			if gap < 1 || gap > 4 || strings.TrimSpace(after) == "" {
				gap = 1
			}
			items = append(items, col+width+gap)
			if textCol, _ := indentColumns(after, col+width); indentedCode && paragraph < 0 && textCol-gap >= 4 {
				markBytes(code[start:offset])
				continue
			}
		}
		if atxHeading(rest) && col < 4 {
			closeParagraph(start)
			markCodeSpans(content[start:offset], code[start:offset])
			continue
		}
		if paragraph < 0 {
			paragraph = start
		}
	}
	closeParagraph(len(content))
	return code
}

// indentColumns returns the width in columns of the leading spaces and tabs of
// text, a tab advancing to the next multiple of four from column at, and the
// text after them.
func indentColumns(text string, at int) (int, string) {
	col := at
	i := 0
	for ; i < len(text); i++ {
		if text[i] == ' ' {
			col++
		} else if text[i] == '\t' {
			col += 4 - col%4
		} else {
			break
		}
	}
	return col - at, text[i:]
}

// listMarker returns the width of the list marker text starts with (a bullet,
// or up to nine digits and a "." or ")") when a space, a tab or the line end
// follows it, and the text after the marker; 0 when text starts with none.
func listMarker(text string) (int, string) {
	width := 0
	switch {
	case text == "":
		return 0, text
	case text[0] == '-' || text[0] == '*' || text[0] == '+':
		width = 1
	default:
		digits := len(text) - len(strings.TrimLeft(text, "0123456789"))
		if digits == 0 || digits > 9 || digits >= len(text) || (text[digits] != '.' && text[digits] != ')') {
			return 0, text
		}
		width = digits + 1
	}
	after := text[width:]
	if strings.TrimRight(after, "\r\n") != "" && after[0] != ' ' && after[0] != '\t' {
		return 0, text
	}
	return width, after
}

// atxHeading reports whether text, a line after its indentation, is an ATX
// heading: one to six "#" and then a space, a tab or the line end.
func atxHeading(text string) bool {
	run := len(text) - len(strings.TrimLeft(text, "#"))
	if run < 1 || run > 6 {
		return false
	}
	after := strings.TrimRight(text[run:], "\r\n")
	return after == "" || after[0] == ' ' || after[0] == '\t'
}

func markBytes(code []bool) {
	for i := range code {
		code[i] = true
	}
}

// fenceCandidate strips what may precede a fence on its line: indentation,
// blockquote markers, and one list marker.
func fenceCandidate(line string) string {
	body := strings.TrimLeft(line, " \t>")
	if rest, ok := strings.CutPrefix(body, "- "); ok {
		return strings.TrimLeft(rest, " \t")
	}
	for _, marker := range []string{"* ", "+ "} {
		if rest, ok := strings.CutPrefix(body, marker); ok {
			return strings.TrimLeft(rest, " \t")
		}
	}
	digits := len(body) - len(strings.TrimLeft(body, "0123456789"))
	if digits > 0 && len(body) > digits+1 && (body[digits] == '.' || body[digits] == ')') && body[digits+1] == ' ' {
		return strings.TrimLeft(body[digits+2:], " \t")
	}
	return body
}

// openingFence returns the marker run of a code fence opener (three or more
// backticks or tildes; a backtick fence's info string holds no backtick), or
// "" when body does not open a fence.
func openingFence(body string) string {
	if body == "" || (body[0] != '`' && body[0] != '~') {
		return ""
	}
	run := len(body) - len(strings.TrimLeft(body, body[:1]))
	if run < 3 || (body[0] == '`' && strings.Contains(body[run:], "`")) {
		return ""
	}
	return body[:run]
}

// closesFence reports whether body closes the fence opened with marker: a run
// of the same character at least as long, with nothing after it.
func closesFence(body, marker string) bool {
	body = strings.TrimRight(body, " \t\r\n")
	return len(body) >= len(marker) && strings.Trim(body, marker[:1]) == ""
}

// markCodeSpans marks the inline code spans of one paragraph or heading: a
// backtick run opens a span that the next run of exactly the same length
// closes, a run with no closer is literal, and a backslash-escaped backtick
// opens nothing.
func markCodeSpans(text string, code []bool) {
	for i := 0; i < len(text); {
		switch text[i] {
		case '\\':
			i += 2
			continue
		case '`':
		default:
			i++
			continue
		}
		open := backtickRun(text, i)
		end := -1
		for j := i + open; j < len(text); {
			if text[j] != '`' {
				j++
				continue
			}
			run := backtickRun(text, j)
			if run == open {
				end = j + run
				break
			}
			j += run
		}
		if end < 0 {
			i += open
			continue
		}
		markBytes(code[i:end])
		i = end
	}
}

func backtickRun(text string, i int) int {
	n := 0
	for i+n < len(text) && text[i+n] == '`' {
		n++
	}
	return n
}

// guardVerifiedDate returns the edit's content with its frontmatter
// `verified` date restored to the starting value (or removed when it was
// absent) when the step moved or introduced a date it may not set: on a doc
// outside the read set, earlier than the starting date, or later than today.
// The read set is only the docs the agent reported reading in full: editing a
// doc for another reason does not earn its date. A file without a dated
// top-level key is untouched.
func guardVerifiedDate(sctx *pipeline.StepContext, edit markdownEdit, readSet map[string]bool, today string) []byte {
	afterLines := splitLinesKeepEnds(string(edit.after))
	afterIdx, afterValue := frontmatterVerified(afterLines)
	if afterIdx < 0 || !isVerifiedDate(afterValue) {
		return edit.after
	}
	beforeLines := splitLinesKeepEnds(string(edit.before))
	beforeIdx, beforeValue := frontmatterVerified(beforeLines)
	if beforeIdx >= 0 && beforeValue == afterValue {
		return edit.after
	}

	reason := ""
	switch {
	case !readSet[edit.path]:
		reason = "not reported in read_against_code"
	case beforeIdx >= 0 && isVerifiedDate(beforeValue) && afterValue < beforeValue:
		reason = "earlier than the date it replaced"
	case afterValue > today:
		reason = "later than the run's day " + today
	default:
		return edit.after
	}

	restored := append([]string(nil), afterLines...)
	oldValue := "(absent)"
	if beforeIdx >= 0 {
		oldValue = beforeValue
		restored[afterIdx] = strings.TrimRight(beforeLines[beforeIdx], "\r\n") + lineEnding(afterLines[afterIdx])
	} else {
		restored = append(restored[:afterIdx], restored[afterIdx+1:]...)
	}
	sctx.Log(fmt.Sprintf("restored verified date in %s: %s -> %s (%s)", edit.path, afterValue, oldValue, reason))
	return []byte(strings.Join(restored, ""))
}

// frontmatterVerified finds the top-level `verified:` key in a leading YAML
// frontmatter block (a first line of "---" up to the next "---" or "...")
// and returns its line index and unquoted value, or -1 when there is none.
func frontmatterVerified(lines []string) (int, string) {
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t\r\n") != "---" {
		return -1, ""
	}
	idx, value := -1, ""
	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r\n")
		if trimmed := strings.TrimRight(line, " \t"); trimmed == "---" || trimmed == "..." {
			return idx, value
		}
		rest, ok := strings.CutPrefix(line, "verified:")
		if !ok || idx >= 0 {
			continue
		}
		if comment := strings.Index(rest, " #"); comment >= 0 {
			rest = rest[:comment]
		}
		rest = strings.TrimSpace(rest)
		if len(rest) >= 2 && (rest[0] == '"' || rest[0] == '\'') && rest[len(rest)-1] == rest[0] {
			rest = rest[1 : len(rest)-1]
		}
		idx, value = i, rest
	}
	// An unclosed block is not frontmatter.
	return -1, ""
}

func isVerifiedDate(value string) bool {
	_, err := time.Parse(verifiedDateLayout, value)
	return err == nil
}

func splitLinesKeepEnds(content string) []string {
	if content == "" {
		return nil
	}
	return strings.SplitAfter(content, "\n")
}

func lineEnding(line string) string {
	switch {
	case strings.HasSuffix(line, "\r\n"):
		return "\r\n"
	case strings.HasSuffix(line, "\n"):
		return "\n"
	}
	return ""
}
