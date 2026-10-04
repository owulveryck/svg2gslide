package syncer

import (
	"slices"
	"strings"
	"testing"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/deck"
	"github.com/owulveryck/svg2gslide/internal/state"
)

// --- fixtures ---------------------------------------------------------------

// entry builds a declared source whose content is the given text.
func entry(source, content string) deck.Entry {
	key := source
	return deck.Entry{
		Source:  source,
		Label:   source,
		Data:    []byte(content),
		Key:     key,
		SlideID: deck.SlideID(key),
	}
}

// slide builds a live slide holding one text box.
func slide(objectID string, texts ...string) *slides.Page {
	var els []*slides.PageElement
	for i, txt := range texts {
		els = append(els, &slides.PageElement{
			ObjectId: objectID + "_e" + string(rune('a'+i)),
			Shape: &slides.Shape{
				ShapeType: "TEXT_BOX",
				Text:      &slides.TextContent{TextElements: []*slides.TextElement{{TextRun: &slides.TextRun{Content: txt}}}},
			},
		})
	}
	return &slides.Page{ObjectId: objectID, PageElements: els}
}

func presentation(pages ...*slides.Page) *slides.Presentation {
	return &slides.Presentation{PresentationId: "pres1", Slides: pages}
}

// syncedState records a past push of e that produced the given live slide.
func syncedState(e deck.Entry, live *slides.Page) *state.State {
	return &state.State{
		PresentationID: "pres1",
		Entries: []state.Entry{{
			Source:     e.Source,
			Key:        e.Key,
			SlideID:    e.SlideID,
			SourceHash: state.SourceHash(e.Data),
			Pushed:     Fingerprint(live, false),
		}},
	}
}

func planFor(t *testing.T, e deck.Entry, st *state.State, live *slides.Presentation, comments map[string]int, opt Options) SlidePlan {
	t.Helper()
	p := Reconcile([]deck.Entry{e}, st, live, comments, opt)
	if len(p.Slides) != 1 {
		t.Fatalf("got %d slide plans, want 1", len(p.Slides))
	}
	return p.Slides[0]
}

// --- the decision table ----------------------------------------------------

func TestDecisionTable(t *testing.T) {
	// The source as it was pushed, and the slide that push produced.
	pushed := entry("flow.svg", "<svg>v1</svg>")
	pushedSlide := slide(pushed.SlideID, "Service A")
	// The same source, edited.
	changed := entry("flow.svg", "<svg>v2</svg>")
	// The slide as a human left it.
	editedSlide := slide(pushed.SlideID, "Service Auth")

	tests := []struct {
		name        string
		entry       deck.Entry
		state       *state.State
		live        *slides.Presentation
		comments    map[string]int
		opt         Options
		wantStatus  Status
		wantAction  Action
		wantReasons []string
	}{
		{
			name:       "source and slide both match the last sync",
			entry:      pushed,
			state:      syncedState(pushed, pushedSlide),
			live:       presentation(pushedSlide),
			wantStatus: StatusUnchanged,
			wantAction: ActionNone,
		},
		{
			name:        "slide edited, source untouched: nothing to push",
			entry:       pushed,
			state:       syncedState(pushed, pushedSlide),
			live:        presentation(editedSlide),
			wantStatus:  StatusDrifted,
			wantAction:  ActionNone,
			wantReasons: []string{ReasonSlideEdited},
		},
		{
			name:        "source changed, slide untouched: safe replace",
			entry:       changed,
			state:       syncedState(pushed, pushedSlide),
			live:        presentation(pushedSlide),
			wantStatus:  StatusReplace,
			wantAction:  ActionReplace,
			wantReasons: []string{ReasonSourceChanged},
		},
		{
			name:        "source changed, open comments: conflict, they would be destroyed",
			entry:       changed,
			state:       syncedState(pushed, pushedSlide),
			live:        presentation(pushedSlide),
			comments:    map[string]int{pushed.SlideID: 1},
			wantStatus:  StatusConflict,
			wantAction:  ActionSkip,
			wantReasons: []string{ReasonSourceChanged, ReasonOpenComments},
		},
		{
			name:        "source changed and slide edited: conflict",
			entry:       changed,
			state:       syncedState(pushed, pushedSlide),
			live:        presentation(editedSlide),
			wantStatus:  StatusConflict,
			wantAction:  ActionSkip,
			wantReasons: []string{ReasonSourceChanged, ReasonSlideEdited},
		},
		{
			name:        "conflict forced by source name",
			entry:       changed,
			state:       syncedState(pushed, pushedSlide),
			live:        presentation(editedSlide),
			opt:         Options{Force: map[string]bool{"flow.svg": true}},
			wantStatus:  StatusConflict,
			wantAction:  ActionReplace,
			wantReasons: []string{ReasonForced},
		},
		{
			name:       "conflict forced by -force all",
			entry:      changed,
			state:      syncedState(pushed, pushedSlide),
			live:       presentation(editedSlide),
			opt:        Options{ForceAll: true},
			wantStatus: StatusConflict,
			wantAction: ActionReplace,
		},
		{
			name:        "no state, no slide: create",
			entry:       pushed,
			state:       &state.State{},
			live:        presentation(),
			wantStatus:  StatusCreate,
			wantAction:  ActionCreate,
			wantReasons: []string{ReasonNoSlide},
		},
		{
			name:        "no state but the slide exists: adoption needs a decision",
			entry:       pushed,
			state:       &state.State{},
			live:        presentation(pushedSlide),
			wantStatus:  StatusAdopt,
			wantAction:  ActionSkip,
			wantReasons: []string{ReasonNoBaseline},
		},
		{
			name:       "adoption forced",
			entry:      pushed,
			state:      &state.State{},
			live:       presentation(pushedSlide),
			opt:        Options{ForceAll: true},
			wantStatus: StatusAdopt,
			wantAction: ActionReplace,
		},
		{
			name:        "state knows the slide but it was deleted by hand",
			entry:       pushed,
			state:       syncedState(pushed, pushedSlide),
			live:        presentation(),
			wantStatus:  StatusMissing,
			wantAction:  ActionSkip,
			wantReasons: []string{ReasonSlideDeleted},
		},
		{
			name:       "deleted slide recreated only when forced",
			entry:      pushed,
			state:      syncedState(pushed, pushedSlide),
			live:       presentation(),
			opt:        Options{ForceAll: true},
			wantStatus: StatusMissing,
			wantAction: ActionCreate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planFor(t, tt.entry, tt.state, tt.live, tt.comments, tt.opt)
			if got.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Action != tt.wantAction {
				t.Errorf("action = %q, want %q", got.Action, tt.wantAction)
			}
			for _, r := range tt.wantReasons {
				if !slices.Contains(got.Reasons, r) {
					t.Errorf("reasons = %v, want it to contain %q", got.Reasons, r)
				}
			}
		})
	}
}

func TestReplaceClearsInPlaceRatherThanDeletingTheSlide(t *testing.T) {
	pushed := entry("flow.svg", "<svg>v1</svg>")
	live := slide(pushed.SlideID, "Service A")
	changed := entry("flow.svg", "<svg>v2</svg>")

	p := Reconcile([]deck.Entry{changed}, syncedState(pushed, live), presentation(live), nil, Options{})
	sp := p.Slides[0]

	if sp.Action != ActionReplace {
		t.Fatalf("action = %q, want %q", sp.Action, ActionReplace)
	}
	// Deleting the slide would lose its object ID (which the source derives),
	// its position, its notes and its page-level comments.
	if slices.Contains(p.Deletions, sp.SlideID) {
		t.Error("a replace must not delete its slide")
	}
	if len(sp.ClearElements) != len(live.PageElements) {
		t.Errorf("ClearElements = %v, want the slide's %d top-level elements", sp.ClearElements, len(live.PageElements))
	}
	if len(p.Moves) != 0 {
		t.Errorf("a replace keeps its position, so it needs no move; got %v", p.Moves)
	}
}

func TestPositionalIdentityIsFlagged(t *testing.T) {
	e := entry("deck.html", "<svg/>")
	e.Positional, e.HTMLIndex = true, 2
	sp := planFor(t, e, &state.State{}, presentation(), nil, Options{})
	if !slices.Contains(sp.Reasons, ReasonPositionalIdent) {
		t.Errorf("reasons = %v, want %q so the user knows the slide drifts if the HTML is reordered",
			sp.Reasons, ReasonPositionalIdent)
	}
}

// --- orphans ---------------------------------------------------------------

func TestOrphans(t *testing.T) {
	declared := entry("a.svg", "<svg/>")
	ours := slide(declared.SlideID, "A")
	foreignOurs := slide(deck.SlideID("removed.svg"), "Old")
	handMade := slide("humanSlide1", "Annexe")

	t.Run("a hand-made slide is reported and never deleted", func(t *testing.T) {
		p := Reconcile([]deck.Entry{declared}, &state.State{}, presentation(ours, handMade), nil, Options{Prune: true})
		var found *Orphan
		for i := range p.Orphans {
			if p.Orphans[i].SlideID == "humanSlide1" {
				found = &p.Orphans[i]
			}
		}
		if found == nil {
			t.Fatalf("hand-made slide not reported; orphans = %+v", p.Orphans)
		}
		if found.Delete {
			t.Error("a slide svg2gslide did not create is not ours to delete, even with -prune")
		}
		if slices.Contains(p.Deletions, "humanSlide1") {
			t.Error("hand-made slide queued for deletion")
		}
		if found.Title != "Annexe" {
			t.Errorf("title = %q, want the slide's first text so it is recognizable", found.Title)
		}
	})

	t.Run("one of ours no longer declared is kept without -prune", func(t *testing.T) {
		p := Reconcile([]deck.Entry{declared}, &state.State{}, presentation(ours, foreignOurs), nil, Options{})
		if len(p.Deletions) != 0 {
			t.Errorf("deletions = %v, want none without -prune", p.Deletions)
		}
		if p.Orphans[0].Kind != OrphanNotDeclared {
			t.Errorf("kind = %q, want %q", p.Orphans[0].Kind, OrphanNotDeclared)
		}
	})

	t.Run("one of ours no longer declared is pruned on request", func(t *testing.T) {
		p := Reconcile([]deck.Entry{declared}, &state.State{}, presentation(ours, foreignOurs), nil, Options{Prune: true})
		if !slices.Contains(p.Deletions, foreignOurs.ObjectId) {
			t.Errorf("deletions = %v, want it to contain %q", p.Deletions, foreignOurs.ObjectId)
		}
	})

	t.Run("a stale state record is reported, not acted on", func(t *testing.T) {
		gone := entry("gone.svg", "<svg/>")
		st := syncedState(gone, slide(gone.SlideID, "Gone"))
		p := Reconcile([]deck.Entry{declared}, st, presentation(ours), nil, Options{Prune: true})
		var found bool
		for _, o := range p.Orphans {
			if o.Kind == OrphanVanished && o.Source == "gone.svg" {
				found = true
				if o.Delete {
					t.Error("a vanished slide cannot be deleted; there is nothing there")
				}
			}
		}
		if !found {
			t.Errorf("stale state record not reported; orphans = %+v", p.Orphans)
		}
	})
}

// --- ordering --------------------------------------------------------------

func TestApplyMoveMatchesTheAPISemantics(t *testing.T) {
	// insertionIndex is interpreted against the arrangement before the move,
	// which is the subtle part: moving forward lands one short of the index.
	tests := []struct {
		name  string
		order []string
		from  int
		index int
		want  []string
	}{
		{"move backward to the front", []string{"a", "b", "c"}, 2, 0, []string{"c", "a", "b"}},
		{"move forward", []string{"a", "b", "c"}, 0, 2, []string{"b", "a", "c"}},
		{"move forward to the end", []string{"a", "b", "c"}, 0, 3, []string{"b", "c", "a"}},
		{"no-op", []string{"a", "b", "c"}, 1, 1, []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := applyMove(tt.order, tt.from, tt.index)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("applyMove(%v, %d, %d) = %v, want %v", tt.order, tt.from, tt.index, got, tt.want)
			}
		})
	}
}

func TestReorderReachesTheDeclaredOrder(t *testing.T) {
	a, b, c := entry("a.svg", "<svg>a</svg>"), entry("b.svg", "<svg>b</svg>"), entry("c.svg", "<svg>c</svg>")
	sa, sb, sc := slide(a.SlideID, "A"), slide(b.SlideID, "B"), slide(c.SlideID, "C")

	st := &state.State{PresentationID: "pres1"}
	for _, pair := range []struct {
		e deck.Entry
		s *slides.Page
	}{{a, sa}, {b, sb}, {c, sc}} {
		st.Put(state.Entry{
			Source: pair.e.Source, Key: pair.e.Key, SlideID: pair.e.SlideID,
			SourceHash: state.SourceHash(pair.e.Data), Pushed: Fingerprint(pair.s, false),
		})
	}

	// Live deck is C, A, B; the manifest declares A, B, C.
	live := presentation(sc, sa, sb)
	p := Reconcile([]deck.Entry{a, b, c}, st, live, nil, Options{})

	want := []string{a.SlideID, b.SlideID, c.SlideID}
	if strings.Join(p.FinalOrder, ",") != strings.Join(want, ",") {
		t.Fatalf("final order = %v, want the declared order %v (moves: %v)", p.FinalOrder, want, p.Moves)
	}
	for i, sp := range p.Slides {
		if sp.TargetIndex != i {
			t.Errorf("%s: target index = %d, want %d", sp.Entry.Source, sp.TargetIndex, i)
		}
	}
	if len(p.Moves) == 0 {
		t.Error("a reordered deck needs at least one move")
	}
}

func TestNoMovesWhenAlreadyInOrder(t *testing.T) {
	a, b := entry("a.svg", "<svg>a</svg>"), entry("b.svg", "<svg>b</svg>")
	sa, sb := slide(a.SlideID, "A"), slide(b.SlideID, "B")

	st := &state.State{}
	st.Put(state.Entry{Source: a.Source, Key: a.Key, SlideID: a.SlideID, SourceHash: state.SourceHash(a.Data), Pushed: Fingerprint(sa, false)})
	st.Put(state.Entry{Source: b.Source, Key: b.Key, SlideID: b.SlideID, SourceHash: state.SourceHash(b.Data), Pushed: Fingerprint(sb, false)})

	p := Reconcile([]deck.Entry{a, b}, st, presentation(sa, sb), nil, Options{})
	if len(p.Moves) != 0 {
		t.Errorf("moves = %v, want none", p.Moves)
	}
	if p.Writes() {
		t.Error("an up-to-date deck must produce no write at all")
	}
}

func TestCreationsLandInDeclaredOrderAfterForeignSlides(t *testing.T) {
	handMade := slide("humanSlide1", "Annexe")
	a, b := entry("a.svg", "<svg>a</svg>"), entry("b.svg", "<svg>b</svg>")

	p := Reconcile([]deck.Entry{a, b}, &state.State{}, presentation(handMade), nil, Options{})
	if len(p.FinalOrder) != 3 {
		t.Fatalf("final order = %v, want 3 slides", p.FinalOrder)
	}
	ia := slices.Index(p.FinalOrder, a.SlideID)
	ib := slices.Index(p.FinalOrder, b.SlideID)
	if ia < 0 || ib < 0 || ia > ib {
		t.Errorf("final order = %v, want a.svg before b.svg", p.FinalOrder)
	}
	if !slices.Contains(p.FinalOrder, "humanSlide1") {
		t.Error("the hand-made slide disappeared from the final order")
	}
}

func TestIdempotence(t *testing.T) {
	// The property the whole declarative model rests on: syncing an
	// up-to-date deck twice must be a no-op the second time too.
	a := entry("a.svg", "<svg>a</svg>")
	sa := slide(a.SlideID, "A")
	st := &state.State{}
	st.Put(state.Entry{Source: a.Source, Key: a.Key, SlideID: a.SlideID, SourceHash: state.SourceHash(a.Data), Pushed: Fingerprint(sa, false)})

	for i := range 2 {
		p := Reconcile([]deck.Entry{a}, st, presentation(sa), nil, Options{})
		if p.Writes() {
			t.Fatalf("run %d would write: slides=%+v moves=%v deletions=%v", i+1, p.Slides, p.Moves, p.Deletions)
		}
		if p.Slides[0].Status != StatusUnchanged {
			t.Fatalf("run %d: status = %q, want %q", i+1, p.Slides[0].Status, StatusUnchanged)
		}
	}
}
