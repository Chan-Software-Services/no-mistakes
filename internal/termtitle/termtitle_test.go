package termtitle_test

import (
	"reflect"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kunchenguid/no-mistakes/internal/termtitle"
	"github.com/kunchenguid/no-mistakes/internal/termtitle/termtitletest"
)

func TestInitSendsTheFirstTitleWithItsConfirmation(t *testing.T) {
	got := termtitletest.Run(termtitle.New("first").Init())
	want := termtitletest.Result{Titles: []string{"first"}, Confirmations: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Init = %+v, want %+v", got, want)
	}
}

func TestUpdateSendsOneTitleAtATimeAndTheLatestOnConfirmation(t *testing.T) {
	title := termtitle.New("first")

	// Until the first title is confirmed, a new one waits.
	title, cmd := title.Update(tea.WindowSizeMsg{}, "second")
	if cmd != nil {
		t.Fatalf("a title was sent while the first was in flight: %+v", termtitletest.Run(cmd))
	}
	title, cmd = title.Update(tea.WindowSizeMsg{}, "third")
	if cmd != nil {
		t.Fatalf("a title was sent while the first was in flight: %+v", termtitletest.Run(cmd))
	}
	if title.Settled("first") || title.Settled("third") {
		t.Fatal("Settled with a title in flight")
	}

	// The confirmation sends the latest title the model calls for.
	title, cmd = title.Update(termtitle.SetMsg{}, "third")
	got := termtitletest.Run(cmd)
	if want := (termtitletest.Result{Titles: []string{"third"}, Confirmations: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("confirmation sent %+v, want %+v", got, want)
	}

	// Its own confirmation leaves the terminal settled on it.
	title, cmd = title.Update(termtitle.SetMsg{}, "third")
	if cmd != nil {
		t.Fatalf("an unchanged title was sent again: %+v", termtitletest.Run(cmd))
	}
	if !title.Settled("third") {
		t.Fatal("not settled once the latest title was confirmed")
	}

	// An unchanged title is never sent again.
	if _, cmd = title.Update(tea.WindowSizeMsg{}, "third"); cmd != nil {
		t.Fatalf("an unchanged title was sent again: %+v", termtitletest.Run(cmd))
	}
}

func TestZeroTitleSendsTheFirstChange(t *testing.T) {
	var title termtitle.Title
	if !title.Settled("") {
		t.Fatal("the zero Title is not settled on the empty title")
	}
	title, cmd := title.Update(tea.WindowSizeMsg{}, "first")
	got := termtitletest.Run(cmd)
	if want := (termtitletest.Result{Titles: []string{"first"}, Confirmations: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("first change sent %+v, want %+v", got, want)
	}
	if title.Settled("first") {
		t.Fatal("settled before the confirmation")
	}
}
