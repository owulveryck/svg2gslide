package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/deck"
	"github.com/owulveryck/svg2gslide/internal/state"
	"github.com/owulveryck/svg2gslide/internal/syncer"
)

var fixedNow = time.Date(2026, 10, 4, 12, 4, 0, 0, time.UTC)

// textBox builds a live text box.
func textBox(objectID, content string) *slides.PageElement {
	return &slides.PageElement{
		ObjectId: objectID,
		Shape: &slides.Shape{
			ShapeType: "TEXT_BOX",
			Text:      &slides.TextContent{TextElements: []*slides.TextElement{{TextRun: &slides.TextRun{Content: content}}}},
		},
	}
}

// conflictScenario sets up the case the whole feature exists for: the source
// changed, a human retyped a label on the slide, and left an open comment.
func conflictScenario() Input {
	src := deck.Entry{
		Source: "slides/flow.svg", Label: "slides/flow.svg",
		Data: []byte("<svg>v2</svg>"), Key: "slides/flow.svg",
		SlideID: "svg2gslide_9f3a1c2b4d", SVGID: "flow",
	}
	live := &slides.Page{
		ObjectId: src.SlideID,
		PageElements: []*slides.PageElement{
			textBox("svg2gslide_9f3a1c2b4d_a1b2c3d4", "Service Auth"), // retyped
			textBox("svg2gslide_9f3a1c2b4d_7e8f9a0b", "Validation"),
			textBox("humanBox1", "Rate limiter"), // added by hand
		},
		CommentAnchors: []*slides.CommentAnchor{{
			AnchorId: "anchor1",
			ObjectAnchors: []*slides.ObjectAnchor{{
				ObjectId:         "svg2gslide_9f3a1c2b4d_7e8f9a0b",
				ShapeTextAnchors: &slides.ShapeTextAnchors{Ranges: []*slides.TextRange{{StartIndex: 0, EndIndex: 10}}},
			}},
		}},
	}
	pres := &slides.Presentation{
		PresentationId: "1AbC",
		Slides:         []*slides.Page{live},
		Comments: []*slides.CommentThread{{
			CommentId: "AAABc", AnchorId: "anchor1", Status: "OPEN", PlainTextQuote: "Validation",
			HeadPost: &slides.Post{
				Author:     &slides.PostAuthor{DisplayName: "a teammate"},
				CreateTime: "2026-10-02T09:12:00Z",
				Content:    "il manque la flèche de retour",
			},
		}},
	}

	st := &state.State{PresentationID: "1AbC", Entries: []state.Entry{{
		Source: src.Source, Key: src.Key, SlideID: src.SlideID,
		SourceHash: state.SourceHash([]byte("<svg>v1</svg>")), // the source has since changed
		Pushed:     state.Fingerprint{Texts: []string{"Service A", "Validation"}, Elements: 2},
		Origins: []state.Origin{
			{ObjectID: "svg2gslide_9f3a1c2b4d_a1b2c3d4", Key: "#auth-label", Locator: "/svg/g[2]/text[1]", SVGID: "auth-label", Tag: "text", Text: "Service A"},
			{ObjectID: "svg2gslide_9f3a1c2b4d_7e8f9a0b", Key: "#validation", Locator: "/svg/g[3]/rect[1]", SVGID: "validation", Tag: "rect", Text: "Validation"},
		},
	}}}

	comments := syncer.FromPresentation(pres)
	plan := syncer.Reconcile([]deck.Entry{src}, st, pres, comments.OpenCounts(), syncer.Options{})
	return Input{Plan: plan, State: st, Live: pres, Comments: comments, Now: fixedNow, DryRun: true}
}

func TestBuildConflict(t *testing.T) {
	r := Build(conflictScenario())

	if r.SchemaVersion != SchemaVersion {
		t.Errorf("schemaVersion = %d, want %d", r.SchemaVersion, SchemaVersion)
	}
	if r.CommentSource != string(syncer.SourceSlides) {
		t.Errorf("commentSource = %q, want %q", r.CommentSource, syncer.SourceSlides)
	}
	if len(r.Slides) != 1 {
		t.Fatalf("got %d slides, want 1", len(r.Slides))
	}
	s := r.Slides[0]
	if s.Status != string(syncer.StatusConflict) {
		t.Fatalf("status = %q, want %q", s.Status, syncer.StatusConflict)
	}
	if s.SlideURL == "" || !strings.Contains(s.SlideURL, s.SlideID) {
		t.Errorf("slideUrl = %q, want a deep link to the slide", s.SlideURL)
	}
	if s.Remediation == "" {
		t.Error("a conflict must say what to do about it")
	}
	if !strings.Contains(s.Remediation, s.Source) {
		t.Errorf("remediation = %q, want it to name the source to fix", s.Remediation)
	}

	t.Run("the retyped label is reported against its source node", func(t *testing.T) {
		var found *Divergence
		for i := range s.Divergences {
			if s.Divergences[i].Kind == KindTextChanged {
				found = &s.Divergences[i]
			}
		}
		if found == nil {
			t.Fatalf("no text-changed divergence; got %+v", s.Divergences)
		}
		if found.Pushed != "Service A" || found.Current != "Service Auth" {
			t.Errorf("pushed/current = %q/%q", found.Pushed, found.Current)
		}
		if found.SVG == nil {
			t.Fatal("a text change must carry the source node to edit")
		}
		if found.SVG.Locator != "/svg/g[2]/text[1]" || found.SVG.ID != "auth-label" {
			t.Errorf("svg = %+v", found.SVG)
		}
	})

	t.Run("a hand-added element is reported with no source node", func(t *testing.T) {
		var found *Divergence
		for i := range s.Divergences {
			if s.Divergences[i].Kind == KindElementAdded {
				found = &s.Divergences[i]
			}
		}
		if found == nil {
			t.Fatalf("no element-added divergence; got %+v", s.Divergences)
		}
		if found.Current != "Rate limiter" {
			t.Errorf("current = %q", found.Current)
		}
		if found.SVG != nil {
			t.Error("an element the source never produced must not claim a source node")
		}
	})

	t.Run("the comment resolves to its source node", func(t *testing.T) {
		if len(s.Comments) != 1 {
			t.Fatalf("got %d comments, want 1", len(s.Comments))
		}
		c := s.Comments[0]
		if c.Status != "open" || c.Author != "a teammate" {
			t.Errorf("comment = %+v", c)
		}
		if c.Anchor == nil || c.Anchor.SVG == nil {
			t.Fatalf("anchor = %+v, want it resolved to a source node", c.Anchor)
		}
		if c.Anchor.SVG.ID != "validation" || c.Anchor.Confidence != string(syncer.ConfidenceExact) {
			t.Errorf("anchor = %+v", c.Anchor)
		}
		if c.Anchor.TextRange == nil || c.Anchor.TextRange.EndIndex != 10 {
			t.Errorf("textRange = %+v", c.Anchor.TextRange)
		}
		if len(c.Thread) != 1 || !strings.Contains(c.Thread[0].Text, "flèche de retour") {
			t.Errorf("thread = %+v", c.Thread)
		}
	})
}

func TestJSONIsSelfContainedForAnLLM(t *testing.T) {
	var buf bytes.Buffer
	if err := Build(conflictScenario()).WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}

	// Round-trips, so a consumer can parse it.
	var back Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatalf("the report must be valid JSON: %v", err)
	}
	if back.SchemaVersion != SchemaVersion || len(back.Slides) != 1 {
		t.Fatalf("round trip lost content: %+v", back)
	}

	// Everything needed to patch the source without reading the deck.
	for _, want := range []string{
		"slides/flow.svg",   // which file to edit
		"/svg/g[2]/text[1]", // which node in it
		"auth-label",        // its id
		"Service A",         // what we wrote
		"Service Auth",      // what it says now
		"flèche de retour",  // the human's actual words
		"/svg/g[3]/rect[1]", // where that comment points
		"\"confidence\"",    // how sure the anchor is
		"\"remediation\"",   // what to do
		"\"schemaVersion\"", // so a consumer can version its parsing
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the JSON report is missing %q, which an LLM needs to fix the source", want)
		}
	}
}

func TestWriteTextIsReadable(t *testing.T) {
	var buf bytes.Buffer
	if err := Build(conflictScenario()).WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"slides/flow.svg",
		"CONFLICT",
		"source changed",
		"slide edited",
		"open comments",
		"pushed  : \"Service A\"",
		"current : \"Service Auth\"",
		"/svg/g[2]/text[1]",
		"a teammate",
		"flèche de retour",
		"-force slides/flow.svg",
		"dry run:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestUnchangedDeckReportsNothingToDo(t *testing.T) {
	src := deck.Entry{Source: "a.svg", Data: []byte("<svg/>"), Key: "a.svg", SlideID: "svg2gslide_aaa"}
	live := &slides.Page{ObjectId: src.SlideID, PageElements: []*slides.PageElement{textBox("svg2gslide_aaa_e1", "A")}}
	pres := &slides.Presentation{PresentationId: "1AbC", Slides: []*slides.Page{live}}
	st := &state.State{Entries: []state.Entry{{
		Source: src.Source, Key: src.Key, SlideID: src.SlideID,
		SourceHash: state.SourceHash(src.Data),
		Pushed:     syncer.Fingerprint(live, false),
	}}}
	plan := syncer.Reconcile([]deck.Entry{src}, st, pres, nil, syncer.Options{})

	r := Build(Input{Plan: plan, State: st, Live: pres, Now: fixedNow})
	if r.Summary.Unchanged != 1 || r.Summary.Conflicts != 0 {
		t.Errorf("summary = %+v, want 1 unchanged and no conflict", r.Summary)
	}
	if len(r.Slides[0].Divergences) != 0 {
		t.Errorf("divergences = %+v, want none", r.Slides[0].Divergences)
	}

	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "1 unchanged") {
		t.Errorf("text = %q, want it to say the deck is up to date", buf.String())
	}
}

func TestUnavailableCommentsAreStated(t *testing.T) {
	src := deck.Entry{Source: "a.svg", Data: []byte("<svg/>"), Key: "a.svg", SlideID: "svg2gslide_aaa"}
	pres := &slides.Presentation{PresentationId: "1AbC"}
	plan := syncer.Reconcile([]deck.Entry{src}, &state.State{}, pres, nil, syncer.Options{})

	r := Build(Input{Plan: plan, Live: pres, Now: fixedNow,
		Comments: &syncer.Comments{Source: syncer.SourceUnavailable}})
	if r.CommentSource != "unavailable" {
		t.Errorf("commentSource = %q", r.CommentSource)
	}

	var buf bytes.Buffer
	_ = r.WriteText(&buf)
	// Silence would read as "no comments", which is a different claim.
	if !strings.Contains(buf.String(), "could not be read") {
		t.Errorf("text = %q, want it to say comments could not be read", buf.String())
	}
}

func TestOrphanNotes(t *testing.T) {
	src := deck.Entry{Source: "a.svg", Data: []byte("<svg/>"), Key: "a.svg", SlideID: "svg2gslide_aaa"}
	ours := &slides.Page{ObjectId: src.SlideID}
	handMade := &slides.Page{ObjectId: "humanSlide1", PageElements: []*slides.PageElement{textBox("t", "Annexe")}}
	pres := &slides.Presentation{PresentationId: "1AbC", Slides: []*slides.Page{ours, handMade}}

	plan := syncer.Reconcile([]deck.Entry{src}, &state.State{}, pres, nil, syncer.Options{Prune: true})
	r := Build(Input{Plan: plan, Live: pres, Now: fixedNow})

	if len(r.Orphans) != 1 {
		t.Fatalf("orphans = %+v, want the hand-made slide", r.Orphans)
	}
	o := r.Orphans[0]
	if o.Deleted {
		t.Error("a hand-made slide must not be reported as deleted")
	}
	if o.Title != "Annexe" || o.Note == "" {
		t.Errorf("orphan = %+v, want it recognizable and explained", o)
	}
}

// TestOrphanCommentsAreReported covers the case a slide appended by the
// one-shot path used to produce: the deck does not declare the slide, so it is
// an orphan — but a comment on it must still reach the reader, since -prune
// would destroy it and the report is the only place it surfaces.
func TestOrphanCommentsAreReported(t *testing.T) {
	src := deck.Entry{Source: "a.svg", Data: []byte("<svg/>"), Key: "a.svg", SlideID: "svg2gslide_aaa"}
	orphan := &slides.Page{
		ObjectId:     "svg2gslide_0e1c0e9b",
		PageElements: []*slides.PageElement{textBox("svg2gslide_0e1c0e9b_t", "enriches the solution")},
		CommentAnchors: []*slides.CommentAnchor{{
			AnchorId:      "anchor1",
			ObjectAnchors: []*slides.ObjectAnchor{{ObjectId: "svg2gslide_0e1c0e9b_t"}},
		}},
	}
	pres := &slides.Presentation{
		PresentationId: "1AbC",
		Slides:         []*slides.Page{orphan},
		Comments: []*slides.CommentThread{{
			CommentId: "AAABc", AnchorId: "anchor1", Status: "OPEN",
			HeadPost: &slides.Post{
				Author:  &slides.PostAuthor{DisplayName: "the author"},
				Content: "cette phase mérite un exemple",
			},
		}},
	}

	comments := syncer.FromPresentation(pres)
	plan := syncer.Reconcile([]deck.Entry{src}, &state.State{}, pres, comments.OpenCounts(), syncer.Options{})
	r := Build(Input{Plan: plan, Live: pres, Comments: comments, Now: fixedNow})

	if len(r.Orphans) != 1 {
		t.Fatalf("orphans = %+v, want the undeclared slide", r.Orphans)
	}
	if got := len(r.Orphans[0].Comments); got != 1 {
		t.Fatalf("orphan comments = %d, want the thread anchored in it", got)
	}
	if !strings.Contains(r.Orphans[0].Note, "-prune would destroy") {
		t.Errorf("note = %q, want it to say the comment is at risk", r.Orphans[0].Note)
	}

	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"1 comment thread(s)", "cette phase mérite un exemple", "the author"} {
		if !strings.Contains(out, want) {
			t.Errorf("text report is missing %q\n--- got ---\n%s", want, out)
		}
	}
}

func TestPositionalIdentityWarningReachesTheReader(t *testing.T) {
	// An HTML slide with no id is identified by its position. That is a
	// warning the user would otherwise never see, since such a slide is
	// usually a plain "created" with nothing else to report.
	src := deck.Entry{
		Source: "deck.html", Label: "deck.html#2 (Second)",
		Data: []byte("<svg/>"), Key: "deck.html#@2",
		SlideID: "svg2gslide_bbb", HTMLIndex: 2, Positional: true,
	}
	pres := &slides.Presentation{PresentationId: "1AbC"}
	plan := syncer.Reconcile([]deck.Entry{src}, &state.State{}, pres, nil, syncer.Options{})

	var buf bytes.Buffer
	if err := Build(Input{Plan: plan, Live: pres, Now: fixedNow}).WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "identified by its position") {
		t.Errorf("text report must warn about positional identity\n--- got ---\n%s", out)
	}
	// And the slide must be nameable: two slides of one HTML file would
	// otherwise print the same path twice.
	if !strings.Contains(out, "deck.html#2 (Second)") {
		t.Errorf("text report should use the label, not just the path\n--- got ---\n%s", out)
	}
}
