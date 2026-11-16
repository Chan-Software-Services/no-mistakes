package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRenderBox_HasRoundedCorners(t *testing.T) {
	out := renderBox("Title", "content", 40)
	if !strings.Contains(out, "╭") || !strings.Contains(out, "╮") {
		t.Error("expected rounded top corners ╭ and ╮")
	}
	if !strings.Contains(out, "╰") || !strings.Contains(out, "╯") {
		t.Error("expected rounded bottom corners ╰ and ╯")
	}
}

func TestRenderBox_TitleInTopBorder(t *testing.T) {
	out := stripANSI(renderBox("Pipeline", "step content", 40))
	lines := strings.Split(out, "\n")
	if len(lines) == 0 {
		t.Fatal("expected non-empty output")
	}
	if !strings.Contains(lines[0], "Pipeline") {
		t.Errorf("expected title 'Pipeline' in top border line, got %q", lines[0])
	}
}

func TestRenderBox_ContentInsideBorders(t *testing.T) {
	out := stripANSI(renderBox("Test", "hello world", 40))
	lines := strings.Split(out, "\n")
	// Find content line (between top and bottom border).
	foundContent := false
	for _, line := range lines[1:] {
		if strings.Contains(line, "hello world") {
			foundContent = true
			// Content lines should start with │.
			if !strings.HasPrefix(strings.TrimSpace(line), "│") {
				t.Errorf("expected content line to start with │, got %q", line)
			}
			break
		}
	}
	if !foundContent {
		t.Error("expected 'hello world' inside box")
	}
}

func TestRenderBox_HorizontalPadding(t *testing.T) {
	out := stripANSI(renderBox("Test", "X", 20))
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if strings.Contains(line, "X") {
			// Content should have at least 1 space padding from border.
			if strings.Contains(line, "│X") || strings.Contains(line, "X│") {
				t.Errorf("expected horizontal padding between content and border, got %q", line)
			}
			break
		}
	}
}

func TestRenderBox_FillsWidth(t *testing.T) {
	out := stripANSI(renderBox("Title", "content", 50))
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		w := lipgloss.Width(line)
		if w != 50 {
			t.Errorf("expected line width 50, got %d for line %q", w, line)
		}
	}
}

func TestRenderBox_MultilineContent(t *testing.T) {
	out := stripANSI(renderBox("Test", "line1\nline2\nline3", 40))
	if !strings.Contains(out, "line1") || !strings.Contains(out, "line2") || !strings.Contains(out, "line3") {
		t.Error("expected all content lines in output")
	}
}

func TestRenderErrorBox_WrapsLongErrorsInsteadOfCuttingThem(t *testing.T) {
	msg := "cannot approve test: the run worktree holds work no Test turn validated and approval would publish it; use fix to validate it, or abort"
	out := stripANSI(renderErrorBox(errors.New(msg), 60))
	var words []string
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w != 60 {
			t.Errorf("line width = %d, want 60: %q", w, line)
		}
		words = append(words, strings.Fields(strings.Trim(line, "│╭╮╰╯─ "))...)
	}
	if got := strings.Join(words, " "); !strings.Contains(got, msg) {
		t.Fatalf("error box = %q, want the whole message %q", out, msg)
	}
}

// TestRenderBox_TitleThatCannotFitIsCut renders a title at the exact width
// that holds it, where the top border is drawn whole and the title in full,
// and one column narrower, where the title is cut so the top border stays the
// box's width instead of overflowing it.
func TestRenderBox_TitleThatCannotFitIsCut(t *testing.T) {
	const title = "Confirm local branch sync"
	exact := len(title) + 6
	for name, render := range map[string]func(int) string{
		"renderBox":           func(width int) string { return renderBox(title, "content", width) },
		"renderBoxWithFooter": func(width int) string { return renderBoxWithFooter(title, "content", width, "esc cancel") },
	} {
		top := strings.Split(stripANSI(render(exact)), "\n")[0]
		if want := "╭─ " + title + " ─╮"; top != want {
			t.Errorf("%s at the exact fit: top border = %q, want %q", name, top, want)
		}

		top = strings.Split(stripANSI(render(exact-1)), "\n")[0]
		if want := "╭─ " + title[:len(title)-1] + " ─╮"; top != want {
			t.Errorf("%s one column below the fit: top border = %q, want %q", name, top, want)
		}
		if got := lipgloss.Width(top); got != exact-1 {
			t.Errorf("%s one column below the fit: top border is %d columns wide, want %d", name, got, exact-1)
		}
	}
}
