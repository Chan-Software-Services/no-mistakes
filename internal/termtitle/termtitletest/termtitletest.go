// Package termtitletest runs a bubbletea command in a test and reports the
// terminal titles it sets.
package termtitletest

import (
	"fmt"
	"reflect"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kunchenguid/no-mistakes/internal/termtitle"
)

// Result is what a command did with the terminal title.
type Result struct {
	// Titles are the titles it set, in the order bubbletea handles them.
	Titles []string
	// Confirmations counts the termtitle.SetMsg confirmations it sent.
	Confirmations int
	// Quit reports whether it quits the program.
	Quit bool
}

// Run runs cmd as bubbletea does, a batch's commands each and a sequence's
// in order, and returns what it did with the title. Any other message is
// dropped, so cmd must not block.
func Run(cmd tea.Cmd) Result {
	var r Result
	r.run(cmd)
	return r
}

func (r *Result) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch msg := msg.(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			r.run(c)
		}
		return
	case termtitle.SetMsg:
		r.Confirmations++
		return
	case tea.QuitMsg:
		r.Quit = true
		return
	}
	// bubbletea keeps the sequence and title message types unexported.
	switch fmt.Sprintf("%T", msg) {
	case "tea.sequenceMsg":
		seq := reflect.ValueOf(msg)
		for i := 0; i < seq.Len(); i++ {
			if c, ok := seq.Index(i).Interface().(tea.Cmd); ok {
				r.run(c)
			}
		}
	case "tea.setWindowTitleMsg":
		r.Titles = append(r.Titles, fmt.Sprint(msg))
	}
}
