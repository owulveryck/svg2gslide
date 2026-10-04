package syncer

import (
	"strings"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/slides/v1"
)

// Source says where comments were read from, and therefore how precisely they
// could be attributed.
type Source string

const (
	// SourceSlides: the Slides API returned anchors resolving to a page and a
	// page element. Requires the Google Workspace Developer Preview.
	SourceSlides Source = "slides-preview"
	// SourceDrive: read through the Drive API, whose anchor is documented as
	// opaque for editor files, so slides are attributed by matching the
	// quoted text.
	SourceDrive Source = "drive-fallback"
	// SourceUnavailable: comments could not be read at all. Reported as such
	// rather than as an absence of comments.
	SourceUnavailable Source = "unavailable"
)

// Confidence qualifies how a comment was tied to a slide and a node.
type Confidence string

const (
	// ConfidenceExact: resolved through a Slides anchor and our provenance.
	ConfidenceExact Confidence = "exact"
	// ConfidenceQuoted: resolved by matching the quoted text against the one
	// slide that holds it.
	ConfidenceQuoted Confidence = "quoted-text-match"
	// ConfidenceAmbiguous: the quoted text appears on several slides.
	ConfidenceAmbiguous Confidence = "ambiguous"
	// ConfidenceUnresolved: no slide could be identified.
	ConfidenceUnresolved Confidence = "unresolved"
)

// Comment is one comment thread, attributed to a slide as precisely as the
// available API allowed.
type Comment struct {
	ID         string
	Open       bool
	Author     string
	CreatedAt  string
	QuotedText string

	SlideID    string     // "" when unresolved
	ObjectID   string     // page element the comment is anchored to, "" when unknown
	Range      *TextRange // text range within the element, when anchored to text
	Confidence Confidence
	// Candidates lists the slides the quoted text matched when attribution
	// was ambiguous.
	Candidates []string

	Thread []Post
}

// TextRange is the character range a comment covers inside an element.
type TextRange struct {
	Start, End int64
}

// Post is one message of a thread.
type Post struct {
	Author string
	At     string
	Text   string
}

// Comments is everything that could be read about a presentation's comments.
type Comments struct {
	Source Source
	Items  []Comment
}

// OpenCounts returns, per slide object ID, how many unresolved threads it
// holds. This is what makes a replace a conflict: rebuilding a slide destroys
// the comments anchored to its elements.
func (c *Comments) OpenCounts() map[string]int {
	out := map[string]int{}
	if c == nil {
		return out
	}
	for _, it := range c.Items {
		if it.Open && it.SlideID != "" {
			out[it.SlideID]++
		}
	}
	return out
}

// For returns the comments attributed to a slide.
func (c *Comments) For(slideID string) []Comment {
	if c == nil {
		return nil
	}
	var out []Comment
	for _, it := range c.Items {
		if it.SlideID == slideID {
			out = append(out, it)
		}
	}
	return out
}

// Unattributed returns the comments no slide could be tied to, which the
// report must still surface rather than drop.
func (c *Comments) Unattributed() []Comment {
	if c == nil {
		return nil
	}
	var out []Comment
	for _, it := range c.Items {
		if it.SlideID == "" {
			out = append(out, it)
		}
	}
	return out
}

// FromPresentation reads the comments a Slides presentation returned, with
// their anchors. Pure: it only reads the response.
func FromPresentation(pres *slides.Presentation) *Comments {
	out := &Comments{Source: SourceSlides}

	// An anchor ID identifies a location; the page that declares it is the
	// slide the comment belongs to.
	type loc struct {
		slideID  string
		objectID string
		rng      *TextRange
	}
	anchors := map[string]loc{}
	for _, page := range pres.Slides {
		for _, a := range page.CommentAnchors {
			l := loc{slideID: page.ObjectId}
			for _, oa := range a.ObjectAnchors {
				if oa.ObjectId != "" {
					l.objectID = oa.ObjectId
				}
				if oa.ShapeTextAnchors != nil && len(oa.ShapeTextAnchors.Ranges) > 0 {
					r := oa.ShapeTextAnchors.Ranges[0]
					l.rng = &TextRange{Start: r.StartIndex, End: r.EndIndex}
				}
			}
			anchors[a.AnchorId] = l
		}
	}

	// Threads come back on the presentation or on the page, depending on the
	// view mode; take both and de-duplicate by comment ID.
	seen := map[string]bool{}
	add := func(th *slides.CommentThread, pageID string) {
		if th == nil || seen[th.CommentId] {
			return
		}
		seen[th.CommentId] = true
		c := convertThread(th)
		if l, ok := anchors[th.AnchorId]; ok {
			c.SlideID, c.ObjectID, c.Range = l.slideID, l.objectID, l.rng
			c.Confidence = ConfidenceExact
		} else if pageID != "" {
			// Found on the page but with no matching anchor: the slide is
			// still known for certain.
			c.SlideID = pageID
			c.Confidence = ConfidenceExact
		} else {
			c.Confidence = ConfidenceUnresolved
		}
		out.Items = append(out.Items, c)
	}
	for _, page := range pres.Slides {
		for _, th := range page.Comments {
			add(th, page.ObjectId)
		}
	}
	for _, th := range pres.Comments {
		add(th, "")
	}
	return out
}

func convertThread(th *slides.CommentThread) Comment {
	c := Comment{
		ID:         th.CommentId,
		Open:       !strings.EqualFold(th.Status, "RESOLVED"),
		QuotedText: th.PlainTextQuote,
	}
	if th.HeadPost != nil {
		c.Author = authorOf(th.HeadPost.Author)
		c.CreatedAt = th.HeadPost.CreateTime
		c.Thread = append(c.Thread, postOf(th.HeadPost))
	}
	for _, r := range th.Replies {
		c.Thread = append(c.Thread, postOf(r))
	}
	return c
}

func postOf(p *slides.Post) Post {
	return Post{Author: authorOf(p.Author), At: p.CreateTime, Text: p.Content}
}

func authorOf(a *slides.PostAuthor) string {
	switch {
	case a == nil:
		return ""
	case a.DisplayName != "":
		return a.DisplayName
	case a.Me:
		return "me"
	case a.Anonymous:
		return "anonymous"
	}
	return ""
}

// FromDrive attributes Drive comments to slides by matching their quoted text
// against the text each slide holds.
//
// Drive documents its anchor as opaque for editor files — it "can't resolve
// internal document regions, cell coordinates, or slide elements" — so this
// is the best attribution available without the Developer Preview, and every
// result is marked with how sure it is.
func FromDrive(items []*drive.Comment, pres *slides.Presentation) *Comments {
	out := &Comments{Source: SourceDrive}

	// Every piece of text the deck holds, in reading order, so a quote is
	// matched against the elements deterministically.
	var hits []textHit
	for _, page := range pres.Slides {
		for _, el := range Elements(page) {
			if t := TextOf(el); t != "" {
				hits = append(hits, textHit{page.ObjectId, el.ObjectId, t})
			}
		}
	}

	for _, dc := range items {
		if dc.Deleted {
			continue
		}
		c := Comment{
			ID:        dc.Id,
			Open:      !dc.Resolved,
			CreatedAt: dc.CreatedTime,
		}
		if dc.Author != nil {
			c.Author = dc.Author.DisplayName
		}
		if dc.QuotedFileContent != nil {
			c.QuotedText = dc.QuotedFileContent.Value
		}
		c.Thread = append(c.Thread, Post{Author: c.Author, At: dc.CreatedTime, Text: dc.Content})
		for _, r := range dc.Replies {
			p := Post{At: r.CreatedTime, Text: r.Content}
			if r.Author != nil {
				p.Author = r.Author.DisplayName
			}
			c.Thread = append(c.Thread, p)
		}

		quote := strings.Join(strings.Fields(c.QuotedText), " ")
		matches := matchQuote(hits, quote)
		switch slideIDs := uniqueSlides(matches); {
		case len(slideIDs) == 0:
			c.Confidence = ConfidenceUnresolved
		case len(slideIDs) == 1:
			c.SlideID = slideIDs[0]
			c.ObjectID = matches[0].objectID
			c.Confidence = ConfidenceQuoted
		default:
			// The same label on several slides: say so rather than guess.
			c.Confidence = ConfidenceAmbiguous
			c.Candidates = slideIDs
		}
		out.Items = append(out.Items, c)
	}
	return out
}

// textHit records where a piece of text was found.
type textHit struct{ slideID, objectID, text string }

// matchQuote finds the elements a Drive quote could have come from.
//
// Drive quotes what the commenter selected, which is usually a fragment of the
// element holding it — commenting on one word of a label quotes that word. An
// element holding the quote is therefore the match to fall back on; without it
// anything but a comment on a whole label would be attributed to nothing.
// A whole-element match still wins, so the common case keeps its precision.
func matchQuote(hits []textHit, quote string) []textHit {
	if quote == "" {
		return nil
	}
	var exact, within []textHit
	for _, h := range hits {
		switch {
		case h.text == quote:
			exact = append(exact, h)
		case strings.Contains(h.text, quote):
			within = append(within, h)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return within
}

// uniqueSlides lists the distinct slides among the hits, in encounter order.
func uniqueSlides(hits []textHit) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range hits {
		if !seen[h.slideID] {
			seen[h.slideID] = true
			out = append(out, h.slideID)
		}
	}
	return out
}
