// Package syncer reconciles a declared deck of SVG sources with a live Google
// Slides presentation.
//
// Reconcile is pure: it takes the declared deck, the state of the last sync
// and the presentation as read, and returns what should happen. Nothing here
// talks to Google, which is what makes the decision table testable.
package syncer

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/deck"
	"github.com/owulveryck/svg2gslide/internal/state"
)

// Status classifies what the reconciliation found for one declared source.
type Status string

const (
	// StatusUnchanged: the source and the slide both match the last sync.
	StatusUnchanged Status = "unchanged"
	// StatusCreate: the source has no slide yet.
	StatusCreate Status = "create"
	// StatusReplace: the source changed and the slide is untouched.
	StatusReplace Status = "replace"
	// StatusConflict: the source changed and the slide holds human work —
	// an edit, or comments that deleting it would destroy.
	StatusConflict Status = "conflict"
	// StatusDrifted: the slide was edited but the source did not change, so
	// there is nothing to push; the edit is reported for back-porting.
	StatusDrifted Status = "drifted"
	// StatusAdopt: a slide with this identity exists but the state holds no
	// reference, so a human edit cannot be told from our own last push.
	StatusAdopt Status = "adopt"
	// StatusMissing: the state knows this slide but it is gone from the deck.
	StatusMissing Status = "missing"
)

// Action is what the sync will actually do about a source.
type Action string

const (
	ActionNone    Action = "none"
	ActionCreate  Action = "create"
	ActionReplace Action = "replace"
	ActionSkip    Action = "skipped"
)

// Reasons given for a status, as stable machine-readable tokens.
const (
	ReasonSourceChanged   = "source-changed"
	ReasonSlideEdited     = "slide-edited"
	ReasonOpenComments    = "open-comments"
	ReasonNoSlide         = "no-slide"
	ReasonNoBaseline      = "no-baseline"
	ReasonSlideDeleted    = "slide-deleted"
	ReasonForced          = "forced"
	ReasonPositionalIdent = "positional-identity"
)

// Options tunes the reconciliation.
type Options struct {
	// Force names the sources (as written in the deck) whose conflicts should
	// be resolved by overwriting the slide.
	Force map[string]bool
	// ForceAll overrides every conflict.
	ForceAll bool
	// Prune authorizes deleting slides the deck no longer declares. Without
	// it, nothing is ever removed.
	Prune bool
	// WithGeometry compares element positions too.
	WithGeometry bool
}

func (o Options) forced(source string) bool {
	return o.ForceAll || o.Force[source]
}

// SlidePlan is the outcome for one declared source.
type SlidePlan struct {
	Entry  deck.Entry
	Status Status
	Action Action
	// Reasons explains the status, as stable tokens.
	Reasons []string
	// SlideID is the slide this source maps to, present or future.
	SlideID string
	// CurrentIndex is the slide's 0-based position in the deck as read, or -1
	// when it is not there.
	CurrentIndex int
	// TargetIndex is its 0-based position once the sync is done.
	TargetIndex int
	// Live is the slide as read, nil when absent.
	Live *slides.Page
	// Previous is the last-sync record, nil when there is none.
	Previous *state.Entry
	// OpenComments counts the unresolved comment threads anchored in the
	// slide. Replacing a slide's contents destroys the ones anchored to its
	// elements, which is why they turn a safe replace into a conflict.
	OpenComments int
	// ClearElements lists the page elements to delete before repopulating,
	// set on a replace. A replace empties the slide and rebuilds it in place
	// rather than deleting the slide: the page keeps its object ID, its
	// position, its speaker notes, and any comment anchored to the page
	// itself.
	ClearElements []string
}

// Writes reports whether this plan changes the presentation.
func (p SlidePlan) Writes() bool {
	return p.Action == ActionCreate || p.Action == ActionReplace
}

// Orphan is a slide the deck does not account for.
type Orphan struct {
	Kind    string // OrphanNotDeclared or OrphanVanished
	SlideID string
	Index   int    // 0-based, -1 when the slide is gone
	Title   string // first text found on the slide, to make it recognizable
	Source  string // the declared source, for OrphanVanished
	// Delete is true when -prune authorizes removing it.
	Delete bool
}

// Kinds of orphan.
const (
	// OrphanNotDeclared: a slide in the deck that no source declares, most
	// likely added by hand.
	OrphanNotDeclared = "slide-not-declared"
	// OrphanVanished: a source whose slide the state knows but the deck no
	// longer has.
	OrphanVanished = "slide-vanished"
)

// Move is one repositioning step. The Slides API requires slideObjectIds to
// be in existing presentation order, so a set cannot be permuted in one call:
// reordering is a sequence of single-slide moves.
type Move struct {
	SlideID string
	// InsertionIndex is interpreted against the arrangement *before* this
	// move, as the API documents.
	InsertionIndex int
}

// Plan is the whole reconciliation outcome.
type Plan struct {
	PresentationID string
	Slides         []SlidePlan
	Orphans        []Orphan
	// Moves repositions slides so the deck reads in declared order. Apply
	// them in order, after creations and before deletions.
	Moves []Move
	// Deletions lists slide object IDs to remove. Only pruned orphans ever
	// appear here: a replace rebuilds its slide in place, so no slide the
	// deck still declares is ever deleted.
	Deletions []string
	// FinalOrder is the deck's slide object IDs once the plan is applied.
	FinalOrder []string
}

// Conflicts counts the sources held back for human attention.
func (p *Plan) Conflicts() int {
	n := 0
	for _, s := range p.Slides {
		if s.Status == StatusConflict || s.Status == StatusAdopt || s.Status == StatusDrifted {
			n++
		}
	}
	return n
}

// Writes reports whether applying the plan would change anything.
func (p *Plan) Writes() bool {
	if len(p.Moves) > 0 || len(p.Deletions) > 0 {
		return true
	}
	for _, s := range p.Slides {
		if s.Writes() {
			return true
		}
	}
	return false
}

// Reconcile decides what the sync should do.
//
// entries is the declared deck in order; st the last sync's record; live the
// presentation as read; openComments the number of unresolved comment threads
// per slide object ID.
func Reconcile(entries []deck.Entry, st *state.State, live *slides.Presentation, openComments map[string]int, opt Options) *Plan {
	if st == nil {
		st = &state.State{}
	}
	plan := &Plan{PresentationID: live.PresentationId}

	index := map[string]int{} // slide object ID → current 0-based index
	page := map[string]*slides.Page{}
	for i, s := range live.Slides {
		index[s.ObjectId] = i
		page[s.ObjectId] = s
	}

	declared := map[string]bool{} // slide object IDs the deck accounts for
	for _, e := range entries {
		declared[e.SlideID] = true
	}

	for _, e := range entries {
		sp := decide(e, st, page[e.SlideID], openComments[e.SlideID], opt)
		sp.CurrentIndex = -1
		if i, ok := index[e.SlideID]; ok {
			sp.CurrentIndex = i
		}
		plan.Slides = append(plan.Slides, sp)
	}

	// A replace empties its slide and rebuilds it in place, so collect the
	// elements to clear. The slide itself is never deleted: that keeps its
	// object ID (which the source derives), its index, its speaker notes,
	// and any page-level comment.
	for i := range plan.Slides {
		sp := &plan.Slides[i]
		if sp.Action != ActionReplace || sp.Live == nil {
			continue
		}
		for _, el := range sp.Live.PageElements {
			// Only top-level elements: deleting a group takes its children.
			sp.ClearElements = append(sp.ClearElements, el.ObjectId)
		}
	}

	plan.Orphans = findOrphans(entries, st, live, declared, opt)
	for _, o := range plan.Orphans {
		if o.Delete {
			plan.Deletions = append(plan.Deletions, o.SlideID)
		}
	}

	plan.Moves, plan.FinalOrder = planMoves(plan, live)
	for i := range plan.Slides {
		plan.Slides[i].TargetIndex = slices.Index(plan.FinalOrder, plan.Slides[i].SlideID)
	}
	return plan
}

// decide applies the decision table to one declared source.
func decide(e deck.Entry, st *state.State, live *slides.Page, openComments int, opt Options) SlidePlan {
	sp := SlidePlan{Entry: e, SlideID: e.SlideID, Live: live, OpenComments: openComments}
	if e.Positional {
		// Not a conflict on its own, but the user should know this slide is
		// identified by its position in the HTML and will drift if the deck
		// is reordered.
		sp.Reasons = append(sp.Reasons, ReasonPositionalIdent)
	}
	prev, hasPrev := st.Find(e.Key)
	if hasPrev {
		sp.Previous = &prev
	}

	if live == nil {
		if hasPrev {
			// We synced this slide once and it is gone: someone deleted it.
			// Recreating it silently would undo that decision.
			sp.Status, sp.Reasons = StatusMissing, append(sp.Reasons, ReasonSlideDeleted)
			if opt.forced(e.Source) {
				sp.Action, sp.Reasons = ActionCreate, append(sp.Reasons, ReasonForced)
			} else {
				sp.Action = ActionSkip
			}
			return sp
		}
		sp.Status, sp.Action = StatusCreate, ActionCreate
		sp.Reasons = append(sp.Reasons, ReasonNoSlide)
		return sp
	}

	if !hasPrev {
		// The slide exists but nothing records what we last pushed, so our
		// own output cannot be told from a human's edits.
		sp.Status, sp.Reasons = StatusAdopt, append(sp.Reasons, ReasonNoBaseline)
		if opt.forced(e.Source) {
			sp.Action, sp.Reasons = ActionReplace, append(sp.Reasons, ReasonForced)
		} else {
			sp.Action = ActionSkip
		}
		return sp
	}

	sourceChanged := prev.SourceHash != state.SourceHash(e.Data)
	edited := !prev.Pushed.Equal(Fingerprint(live, opt.WithGeometry))

	switch {
	case !sourceChanged && !edited:
		sp.Status, sp.Action = StatusUnchanged, ActionNone
		return sp

	case !sourceChanged && edited:
		// Nothing to push: the source is what it was. Report the edit so it
		// can be back-ported into the SVG.
		sp.Status, sp.Action = StatusDrifted, ActionNone
		sp.Reasons = append(sp.Reasons, ReasonSlideEdited)
		return sp

	case sourceChanged && !edited && openComments == 0:
		sp.Status, sp.Action = StatusReplace, ActionReplace
		sp.Reasons = append(sp.Reasons, ReasonSourceChanged)
		return sp

	default:
		sp.Status = StatusConflict
		sp.Reasons = append(sp.Reasons, ReasonSourceChanged)
		if edited {
			sp.Reasons = append(sp.Reasons, ReasonSlideEdited)
		}
		if openComments > 0 {
			// Deleting the slide destroys its comment threads, and there is
			// no API to move them.
			sp.Reasons = append(sp.Reasons, ReasonOpenComments)
		}
		if opt.forced(e.Source) {
			sp.Action, sp.Reasons = ActionReplace, append(sp.Reasons, ReasonForced)
		} else {
			sp.Action = ActionSkip
		}
		return sp
	}
}

// findOrphans lists what the deck does not account for: slides nobody
// declares, and declared slides the state knew but the deck has lost.
func findOrphans(entries []deck.Entry, st *state.State, live *slides.Presentation, declared map[string]bool, opt Options) []Orphan {
	var out []Orphan
	for i, s := range live.Slides {
		if declared[s.ObjectId] {
			continue
		}
		out = append(out, Orphan{
			Kind:    OrphanNotDeclared,
			SlideID: s.ObjectId,
			Index:   i,
			Title:   slideTitle(s),
			// Only our own slides are ever pruned: a slide a human created
			// is not ours to delete, whatever the flags say.
			Delete: opt.Prune && strings.HasPrefix(s.ObjectId, deck.SlideIDPrefix),
		})
	}

	live2 := map[string]bool{}
	for _, s := range live.Slides {
		live2[s.ObjectId] = true
	}
	declaredKeys := map[string]bool{}
	for _, e := range entries {
		declaredKeys[e.Key] = true
	}
	for _, e := range st.Entries {
		if declaredKeys[e.Key] || live2[e.SlideID] {
			continue
		}
		// The state names a slide that is neither declared any more nor
		// present: a stale record to clear, not a deletion to perform.
		out = append(out, Orphan{
			Kind:    OrphanVanished,
			SlideID: e.SlideID,
			Index:   -1,
			Source:  e.Source,
		})
	}
	return out
}

// slideTitle returns the first text on a slide, to make it recognizable in a
// report.
func slideTitle(s *slides.Page) string {
	for _, e := range Elements(s) {
		if t := TextOf(e); t != "" {
			if len(t) > 60 {
				return t[:57] + "..."
			}
			return t
		}
	}
	return ""
}

// planMoves computes the repositioning sequence and the resulting deck order.
//
// Target arrangement: the slides the deck declares form a contiguous block in
// declared order, anchored at the lowest index they already occupy (or at the
// end of the deck when none exists yet). Slides the deck does not declare are
// never deleted and keep their relative order, but one sitting between two
// declared slides ends up after the block.
//
// The Slides API requires slideObjectIds to be in existing presentation
// order, so a set cannot be permuted in a single call: the result is a
// sequence of single-slide moves, simulated here so the plan knows exactly
// where everything lands.
func planMoves(plan *Plan, live *slides.Presentation) (moves []Move, order []string) {
	deleting := map[string]bool{}
	for _, id := range plan.Deletions {
		deleting[id] = true
	}
	// The deck as it stands when the moves run: pruned slides gone, newly
	// created ones appended at the end by the build step. A replace keeps its
	// slide where it is.
	for _, s := range live.Slides {
		if !deleting[s.ObjectId] {
			order = append(order, s.ObjectId)
		}
	}
	present := map[string]bool{}
	for _, id := range order {
		present[id] = true
	}

	var want []string // declared slides that will exist, in declared order
	for _, sp := range plan.Slides {
		switch {
		case present[sp.SlideID]:
			want = append(want, sp.SlideID)
		case sp.Action == ActionCreate:
			order = append(order, sp.SlideID)
			want = append(want, sp.SlideID)
		}
	}
	if len(want) == 0 {
		return nil, order
	}

	anchor := len(order)
	for i, id := range order {
		if slices.Contains(want, id) {
			anchor = i
			break
		}
	}
	if anchor+len(want) > len(order) {
		anchor = len(order) - len(want)
	}

	for i, id := range want {
		target := anchor + i
		j := slices.Index(order, id)
		if j == target {
			continue
		}
		// insertionIndex is read against the arrangement before the move, so
		// landing past the current position needs one more.
		k := target
		if j < target {
			k = target + 1
		}
		moves = append(moves, Move{SlideID: id, InsertionIndex: k})
		order = applyMove(order, j, k)
	}
	return moves, order
}

// applyMove reproduces UpdateSlidesPosition on a single slide: the element at
// j is removed, then inserted at insertionIndex interpreted against the
// arrangement before the move.
func applyMove(order []string, j, insertionIndex int) []string {
	id := order[j]
	rest := slices.Delete(slices.Clone(order), j, j+1)
	at := insertionIndex
	if insertionIndex > j {
		at = insertionIndex - 1
	}
	at = max(0, min(at, len(rest)))
	return slices.Insert(rest, at, id)
}

// String renders a move for diagnostics.
func (m Move) String() string {
	return fmt.Sprintf("%s -> @%d", m.SlideID, m.InsertionIndex)
}
