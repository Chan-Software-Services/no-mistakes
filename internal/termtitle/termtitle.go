// Package termtitle sets a bubbletea program's terminal title with the
// framework's window-title command, one title at a time.
//
// A model must not write the title's OSC sequence into its frame: bubbletea's
// renderer measures each frame line with x/ansi and cuts a line it measures
// wider than the terminal, and x/ansi v0.11.6 counts some title sequences as
// visible columns (a title that starts with a check or cross mark), so the
// line carrying the title lost its right end.
//
// bubbletea runs each command in a goroutine of its own, so two title
// commands could reach the terminal in either order. Title keeps one in
// flight and sends the latest title when that one is confirmed.
package termtitle

import tea "github.com/charmbracelet/bubbletea"

// SetMsg confirms that the title sent last has been set. A model passes it,
// like every other message, to Title.Update.
type SetMsg struct{}

// Title is the terminal title a model has sent. Its zero value has sent
// nothing and has nothing in flight.
type Title struct {
	sent    string
	pending bool
}

// New returns a Title whose first title is title, which Init sends; until it
// is confirmed, Update sends no other.
func New(title string) Title {
	return Title{sent: title, pending: true}
}

// Init sends the first title, for the model's Init.
func (t Title) Init() tea.Cmd {
	return setCmd(t.sent)
}

// Update takes msg and the title the model's state calls for now, and returns
// the command that sends it when it differs from the last title sent and no
// title is in flight, or nil.
func (t Title) Update(msg tea.Msg, want string) (Title, tea.Cmd) {
	if _, ok := msg.(SetMsg); ok {
		t.pending = false
	}
	if t.pending || want == t.sent {
		return t, nil
	}
	t.sent = want
	t.pending = true
	return t, setCmd(want)
}

// Settled reports whether title is the title the terminal has, with nothing
// in flight.
func (t Title) Settled(title string) bool {
	return !t.pending && t.sent == title
}

// setCmd sets the terminal title, then confirms it with SetMsg. One sequence
// sends both, so bubbletea handles the title before its confirmation.
func setCmd(title string) tea.Cmd {
	return tea.Sequence(tea.SetWindowTitle(title), func() tea.Msg { return SetMsg{} })
}
