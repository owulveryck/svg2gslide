package main

import (
	"testing"

	"github.com/owulveryck/svg2gslide/internal/deck"
)

// TestAppendSlideIDMatchesSync is the invariant that keeps the one-shot append
// and sync talking about the same slide: appending a file must give it the
// object ID sync derives from that same path, or the next sync creates a second
// slide beside it and reports the first as an orphan.
func TestAppendSlideIDMatchesSync(t *testing.T) {
	entries, err := loadSources("testdata/sdlc-phase-8.svg", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 for a bare SVG", len(entries))
	}

	taken := map[string]bool{}
	got := appendSlideID(entries[0], taken)
	if want := deck.SlideID("testdata/sdlc-phase-8.svg"); got != want {
		t.Errorf("appendSlideID = %q, want sync's %q", got, want)
	}
}

func TestAppendSlideIDFallsBackToRandom(t *testing.T) {
	e := deck.Entry{Source: "a.svg", Key: "a.svg", SlideID: "svg2gslide_aaa"}

	t.Run("an ID already in the presentation", func(t *testing.T) {
		// Appending the same source twice must not fail on a duplicate object
		// ID: the second slide gets a random one.
		taken := map[string]bool{"svg2gslide_aaa": true}
		if got := appendSlideID(e, taken); got != "" {
			t.Errorf("appendSlideID = %q, want \"\" so the converter mints a random ID", got)
		}
	})

	t.Run("twice in one run", func(t *testing.T) {
		taken := map[string]bool{}
		if got := appendSlideID(e, taken); got != e.SlideID {
			t.Fatalf("first call = %q, want %q", got, e.SlideID)
		}
		if got := appendSlideID(e, taken); got != "" {
			t.Errorf("second call = %q, want \"\": the ID was just handed out", got)
		}
	})

	t.Run("stdin", func(t *testing.T) {
		// A stdin source has no path, so its key identifies nothing a later
		// sync could find again — and every stdin append would otherwise claim
		// the same derived ID.
		stdin := deck.Entry{Source: "", SlideID: deck.SlideID("")}
		if got := appendSlideID(stdin, map[string]bool{}); got != "" {
			t.Errorf("appendSlideID = %q, want \"\" for stdin", got)
		}
	})
}
