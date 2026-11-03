package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/contract/eventwire"
)

// A turn another client started with a picture and nothing else is still a
// turn, and the live stream is the only account of it this screen gets. The
// kernel names the stored image in Attachments, so the live row must say what
// it carried in the same words a restored one does.
func TestLiveTurnStartKeepsATurnThatWasOnlyAnImage(t *testing.T) {
	tr := fold(eventwire.Event{
		Kind: "turn_started", AuthoredTurn: new(1), MsgIndex: new(1),
		Attachments: []eventwire.Attachment{{Path: ".reasonix/attachments/shot.png"}},
	})
	if len(tr.Items) != 1 || tr.Items[0].Kind != ItemUser {
		t.Fatalf("a live image-only turn became %d rows (%+v)", len(tr.Items), tr.Items)
	}
	row := tr.Items[0]
	if row.Text != "" || row.Images != 1 || row.MsgIndex != 1 {
		t.Fatalf("live row = %+v, want the turn's index and one image", row)
	}
	if got := ansi.Strip(renderItem(&row, 80, 0, false)); !strings.Contains(got, "1 image(s) attached") {
		t.Fatalf("the live row says nothing about the image: %q", got)
	}
}

// A text turn is drawn exactly as before: the person's line, nothing added.
func TestLiveTurnStartOfTextIsUnchanged(t *testing.T) {
	tr := fold(eventwire.Event{Kind: "turn_started", AuthoredTurn: new(1), MsgIndex: new(1), Text: "fix the build"})
	if len(tr.Items) != 1 || tr.Items[0].Kind != ItemUser || tr.Items[0].Text != "fix the build" || tr.Items[0].Images != 0 {
		t.Fatalf("live text turn = %+v", tr.Items)
	}
	if got := ansi.Strip(renderItem(&tr.Items[0], 80, 0, false)); !strings.Contains(got, "fix the build") || strings.Contains(got, "image(s) attached") {
		t.Fatalf("a live text turn gained or lost a line: %q", got)
	}
}

// A turn that carried words and pictures keeps both facts live, just as a
// restored one does.
func TestLiveTurnStartKeepsTheWordsAndTheImages(t *testing.T) {
	tr := fold(eventwire.Event{
		Kind: "turn_started", AuthoredTurn: new(2), MsgIndex: new(2), Text: "what is this?",
		Attachments: []eventwire.Attachment{{Path: ".reasonix/attachments/a.png"}, {Path: ".reasonix/attachments/b.png"}},
	})
	if len(tr.Items) != 1 {
		t.Fatalf("live text+image turn = %d rows (%+v)", len(tr.Items), tr.Items)
	}
	row := tr.Items[0]
	if row.Text != "what is this?" || row.Images != 2 {
		t.Fatalf("live row = %+v, want its words and two images", row)
	}
	got := ansi.Strip(renderItem(&row, 80, 0, false))
	if !strings.Contains(got, "what is this?") || !strings.Contains(got, "2 image(s) attached") {
		t.Fatalf("the live row does not show both facts: %q", got)
	}
}

// One turn is one row: the live announcement is drawn once even if it arrives
// twice, and a reload that restores the same turn does not add a second.
func TestALiveImageTurnIsDrawnOnceLiveAndAfterRestore(t *testing.T) {
	started := eventwire.Event{
		Kind: "turn_started", AuthoredTurn: new(1), MsgIndex: new(1),
		Attachments: []eventwire.Attachment{{Path: ".reasonix/attachments/shot.png"}},
	}
	tr := fold(started, started)
	users := 0
	for _, it := range tr.Items {
		if it.Kind == ItemUser {
			users++
		}
	}
	if users != 1 {
		t.Fatalf("one live image turn drawn %d times: %+v", users, tr.Items)
	}
	tr.Restore([]HistoryMessage{
		{Role: "user", MsgIndex: 1, Images: 1},
		{Role: "assistant", Content: "a picture"},
	})
	users = 0
	for _, it := range tr.Items {
		if it.Kind == ItemUser {
			users++
			if it.Images != 1 || !strings.Contains(ansi.Strip(renderItem(&it, 80, 0, false)), "1 image(s) attached") {
				t.Fatalf("restored row after the live one = %+v", it)
			}
		}
	}
	if users != 1 {
		t.Fatalf("the turn appears %d times after restore: %+v", users, tr.Items)
	}
}

// A user-role frame with neither words nor a picture is host chrome and is
// still not a row.
func TestLiveTurnStartWithoutWordsOrImagesIsStillNotARow(t *testing.T) {
	tr := fold(eventwire.Event{Kind: "turn_started", AuthoredTurn: new(1), MsgIndex: new(1)})
	if len(tr.Items) != 0 {
		t.Fatalf("a wordless, imageless turn made a row: %+v", tr.Items)
	}
}
