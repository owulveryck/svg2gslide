package report

import (
	"fmt"
	"slices"
	"time"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/state"
	"github.com/owulveryck/svg2gslide/internal/syncer"
)

// Input is everything needed to build a report.
type Input struct {
	Plan     *syncer.Plan
	State    *state.State
	Live     *slides.Presentation
	Comments *syncer.Comments
	// DryRun records whether the plan was applied.
	DryRun bool
	// BackupURL is the snapshot taken before writing, if any.
	BackupURL string
	// Now is injectable so the rendering is testable.
	Now time.Time
}

// Build turns a reconciliation into the report.
func Build(in Input) *Report {
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	st := in.State
	if st == nil {
		st = &state.State{}
	}
	commentSource := string(syncer.SourceUnavailable)
	if in.Comments != nil {
		commentSource = string(in.Comments.Source)
	}

	r := &Report{
		SchemaVersion:   SchemaVersion,
		PresentationID:  in.Plan.PresentationID,
		PresentationURL: presentationURL(in.Plan.PresentationID),
		GeneratedAt:     now,
		DryRun:          in.DryRun,
		CommentSource:   commentSource,
		BackupURL:       in.BackupURL,
	}

	livePages := map[string]*slides.Page{}
	if in.Live != nil {
		for _, p := range in.Live.Slides {
			livePages[p.ObjectId] = p
		}
	}

	r.Summary.Declared = len(in.Plan.Slides)
	r.Summary.Moved = len(in.Plan.Moves)

	for _, sp := range in.Plan.Slides {
		s := Slide{
			Source:        sp.Entry.Source,
			Label:         sp.Entry.Label,
			SourceHash:    state.SourceHash(sp.Entry.Data),
			SVGID:         sp.Entry.SVGID,
			HTMLIndex:     sp.Entry.HTMLIndex,
			SlideID:       sp.SlideID,
			SlideIndex:    sp.TargetIndex,
			PreviousIndex: sp.CurrentIndex,
			Status:        string(sp.Status),
			Action:        string(sp.Action),
			Reasons:       sp.Reasons,
			Remediation:   remediation(sp),
		}
		if sp.CurrentIndex >= 0 || sp.Action == syncer.ActionCreate {
			s.SlideURL = slideURL(in.Plan.PresentationID, sp.SlideID)
		}

		var origins []state.Origin
		if sp.Previous != nil {
			origins = sp.Previous.Origins
		}
		s.Divergences = divergences(origins, livePages[sp.SlideID], sp)
		s.Comments = comments(in.Comments, sp.SlideID, origins)

		switch sp.Status {
		case syncer.StatusUnchanged:
			r.Summary.Unchanged++
		case syncer.StatusConflict, syncer.StatusAdopt, syncer.StatusDrifted, syncer.StatusMissing:
			r.Summary.Conflicts++
		}
		switch sp.Action {
		case syncer.ActionCreate:
			r.Summary.Created++
		case syncer.ActionReplace:
			r.Summary.Replaced++
		case syncer.ActionSkip:
			r.Summary.Skipped++
		}
		r.Slides = append(r.Slides, s)
	}

	for _, o := range in.Plan.Orphans {
		// An orphan has no state record, so there are no origins to resolve
		// its anchors against: the comments are reported against their object
		// IDs rather than source nodes, which is still better than dropping
		// them.
		threads := comments(in.Comments, o.SlideID, nil)
		r.Orphans = append(r.Orphans, Orphan{
			Kind:       o.Kind,
			SlideID:    o.SlideID,
			SlideIndex: o.Index,
			Title:      o.Title,
			Source:     o.Source,
			Deleted:    o.Delete,
			Note:       orphanNote(o, len(threads)),
			Comments:   threads,
		})
	}
	r.Summary.Orphans = len(r.Orphans)
	r.Summary.Deleted = len(in.Plan.Deletions)

	for _, c := range in.Comments.Unattributed() {
		r.UnattributedComments = append(r.UnattributedComments, comment(c, nil))
	}
	return r
}

// divergences compares what we pushed against what the slide holds now, and
// ties each difference back to a node of the source SVG.
//
// Matching is by object ID, which is reliable because the IDs derive from the
// source nodes: an element the live slide has but the push did not create was
// added by a human, and one the push created but the slide has lost was
// deleted.
func divergences(origins []state.Origin, live *slides.Page, sp syncer.SlidePlan) []Divergence {
	if live == nil || len(origins) == 0 {
		return nil
	}
	byID := map[string]state.Origin{}
	for _, o := range origins {
		byID[o.ObjectID] = o
	}

	var out []Divergence
	seen := map[string]bool{}
	for _, el := range syncer.Elements(live) {
		seen[el.ObjectId] = true
		text := syncer.TextOf(el)
		o, known := byID[el.ObjectId]
		if !known {
			if text == "" {
				// A shape with no text and no counterpart: real, but there is
				// nothing useful to say about it beyond its existence.
				continue
			}
			out = append(out, Divergence{
				Kind:     KindElementAdded,
				ObjectID: el.ObjectId,
				Current:  text,
				Note:     "added in Google Slides; it has no counterpart in the source SVG",
			})
			continue
		}
		if o.Text != "" && text != "" && o.Text != text {
			out = append(out, Divergence{
				Kind:     KindTextChanged,
				ObjectID: el.ObjectId,
				SVG:      node(o),
				Pushed:   o.Text,
				Current:  text,
			})
		}
	}
	for _, o := range origins {
		if seen[o.ObjectID] || o.Text == "" {
			continue
		}
		out = append(out, Divergence{
			Kind:     KindElementRemoved,
			ObjectID: o.ObjectID,
			SVG:      node(o),
			Pushed:   o.Text,
			Note:     "deleted in Google Slides",
		})
	}
	// A geometric difference is reported only when the fingerprint says so;
	// the texts above already cover the common case.
	if sp.Previous != nil && sp.Previous.Pushed.Geometry != "" {
		if fp := syncer.Fingerprint(live, true); fp.Geometry != sp.Previous.Pushed.Geometry {
			out = append(out, Divergence{
				Kind: KindGeometry,
				Note: "element positions or sizes differ from what was pushed",
			})
		}
	}
	return out
}

func node(o state.Origin) *Node {
	return &Node{Locator: o.Locator, ID: o.SVGID, Tag: o.Tag}
}

// comments renders the threads attributed to a slide, resolving each anchor
// to a source node when the provenance knows it.
func comments(all *syncer.Comments, slideID string, origins []state.Origin) []Comment {
	byID := map[string]state.Origin{}
	for _, o := range origins {
		byID[o.ObjectID] = o
	}
	var out []Comment
	for _, c := range all.For(slideID) {
		var n *Node
		if o, ok := byID[c.ObjectID]; ok {
			n = node(o)
		}
		out = append(out, comment(c, n))
	}
	return out
}

func comment(c syncer.Comment, n *Node) Comment {
	out := Comment{
		CommentID:  c.ID,
		Status:     statusOf(c.Open),
		Author:     c.Author,
		CreatedAt:  c.CreatedAt,
		QuotedText: c.QuotedText,
		Candidates: c.Candidates,
		Anchor: &Anchor{
			ObjectID:   c.ObjectID,
			SVG:        n,
			Confidence: string(c.Confidence),
		},
	}
	if c.Range != nil {
		out.Anchor.TextRange = &Range{StartIndex: c.Range.Start, EndIndex: c.Range.End}
	}
	for _, p := range c.Thread {
		out.Thread = append(out.Thread, Post{Author: p.Author, At: p.At, Text: p.Text})
	}
	return out
}

func statusOf(open bool) string {
	if open {
		return "open"
	}
	return "resolved"
}

// remediation says what to do about a slide, in prose rather than flags.
func remediation(sp syncer.SlidePlan) string {
	switch sp.Status {
	case syncer.StatusConflict:
		if sp.Action == syncer.ActionReplace {
			return fmt.Sprintf("Overwritten on request (-force %s): the edits and comments listed here are gone from the slide, so keep this report.", sp.Entry.Source)
		}
		return fmt.Sprintf("Port the divergences and comments below into %s, then sync again. To discard the work in the slide instead: -force %s", sp.Entry.Source, sp.Entry.Source)
	case syncer.StatusDrifted:
		return fmt.Sprintf("The slide was edited but %s did not change, so there is nothing to push. Port the edits below into the source to keep them.", sp.Entry.Source)
	case syncer.StatusAdopt:
		if sp.Action == syncer.ActionReplace {
			return "Adopted on request: the slide was rebuilt from the source."
		}
		return fmt.Sprintf("No record of a previous sync for this slide, so its contents cannot be told from a human's edits. Check it, then adopt it with -force %s", sp.Entry.Source)
	case syncer.StatusMissing:
		if sp.Action == syncer.ActionCreate {
			return "Recreated on request."
		}
		return fmt.Sprintf("The slide was deleted in Google Slides. Remove %s from the deck, or recreate the slide with -force %s", sp.Entry.Source, sp.Entry.Source)
	}
	if slices.Contains(sp.Reasons, syncer.ReasonPositionalIdent) {
		return "This slide is identified by its position in the HTML because its <svg> has no id; give it one so it keeps its identity if the deck is reordered."
	}
	return ""
}

func orphanNote(o syncer.Orphan, comments int) string {
	switch {
	case o.Kind == syncer.OrphanVanished:
		return "The last sync recorded this slide but it is neither declared nor present; its record will be cleared."
	case o.Delete && comments > 0:
		return fmt.Sprintf("Deleted: no longer declared, and -prune was given. Its %d comment thread(s), listed here, went with it.", comments)
	case o.Delete:
		return "Deleted: no longer declared, and -prune was given."
	case comments > 0:
		return fmt.Sprintf("Not declared by the deck; left untouched, and it carries %d comment thread(s) that -prune would destroy. If this slide is one svg2gslide appended, declare its source in the deck so a sync keeps it in step.", comments)
	default:
		return "Not declared by the deck; left untouched. Use -prune to delete the slides svg2gslide created."
	}
}

func presentationURL(id string) string {
	if id == "" {
		return ""
	}
	return "https://docs.google.com/presentation/d/" + id + "/edit"
}

func slideURL(presentationID, slideID string) string {
	if presentationID == "" || slideID == "" {
		return ""
	}
	return presentationURL(presentationID) + "#slide=id." + slideID
}
