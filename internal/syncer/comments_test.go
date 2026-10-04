package syncer

import (
	"strings"
	"testing"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/slides/v1"
)

func thread(id, anchorID, status, quote, author, text string) *slides.CommentThread {
	return &slides.CommentThread{
		CommentId:      id,
		AnchorId:       anchorID,
		Status:         status,
		PlainTextQuote: quote,
		HeadPost: &slides.Post{
			Author:     &slides.PostAuthor{DisplayName: author},
			CreateTime: "2026-10-02T09:12:00Z",
			Content:    text,
		},
	}
}

func TestFromPresentationResolvesToElementAndRange(t *testing.T) {
	page := slide("svg2gslide_aaa", "Validation")
	page.CommentAnchors = []*slides.CommentAnchor{{
		AnchorId: "anchor1",
		ObjectAnchors: []*slides.ObjectAnchor{{
			ObjectId:         "svg2gslide_aaa_e1",
			ShapeTextAnchors: &slides.ShapeTextAnchors{Ranges: []*slides.TextRange{{StartIndex: 0, EndIndex: 10}}},
		}},
	}}
	pres := presentation(page)
	pres.Comments = []*slides.CommentThread{
		thread("c1", "anchor1", "OPEN", "Validation", "a teammate", "il manque la flèche de retour"),
	}

	got := FromPresentation(pres)
	if got.Source != SourceSlides {
		t.Errorf("source = %q, want %q", got.Source, SourceSlides)
	}
	if len(got.Items) != 1 {
		t.Fatalf("got %d comments, want 1", len(got.Items))
	}
	c := got.Items[0]
	if c.SlideID != "svg2gslide_aaa" || c.ObjectID != "svg2gslide_aaa_e1" {
		t.Errorf("resolved to slide %q element %q", c.SlideID, c.ObjectID)
	}
	if c.Confidence != ConfidenceExact {
		t.Errorf("confidence = %q, want %q", c.Confidence, ConfidenceExact)
	}
	if c.Range == nil || c.Range.Start != 0 || c.Range.End != 10 {
		t.Errorf("range = %+v, want 0-10", c.Range)
	}
	if !c.Open {
		t.Error("an OPEN thread should read as open")
	}
	if c.Author != "a teammate" || len(c.Thread) != 1 {
		t.Errorf("author = %q, thread = %+v", c.Author, c.Thread)
	}
}

func TestFromPresentationStatusAndReplies(t *testing.T) {
	page := slide("svg2gslide_aaa", "x")
	pres := presentation(page)
	resolved := thread("c1", "none", "RESOLVED", "x", "someone", "head")
	resolved.Replies = []*slides.Post{{
		Author:     &slides.PostAuthor{Me: true},
		CreateTime: "2026-10-03T10:00:00Z",
		Content:    "done",
	}}
	page.Comments = []*slides.CommentThread{resolved}

	got := FromPresentation(pres)
	c := got.Items[0]
	if c.Open {
		t.Error("a RESOLVED thread must not read as open")
	}
	if len(c.Thread) != 2 {
		t.Fatalf("thread = %+v, want the head post and its reply", c.Thread)
	}
	if c.Thread[1].Author != "me" || c.Thread[1].Text != "done" {
		t.Errorf("reply = %+v", c.Thread[1])
	}
	// Found on the page, so the slide is known even without a matching anchor.
	if c.SlideID != "svg2gslide_aaa" {
		t.Errorf("slideID = %q, want the page it was returned on", c.SlideID)
	}
}

func TestFromPresentationDeduplicatesAcrossPageAndPresentation(t *testing.T) {
	page := slide("svg2gslide_aaa", "x")
	th := thread("c1", "anchor1", "OPEN", "x", "someone", "hello")
	page.Comments = []*slides.CommentThread{th}
	pres := presentation(page)
	pres.Comments = []*slides.CommentThread{th} // the API may return it in both places

	if got := FromPresentation(pres); len(got.Items) != 1 {
		t.Errorf("got %d comments, want the same thread counted once", len(got.Items))
	}
}

func TestOpenCounts(t *testing.T) {
	c := &Comments{Items: []Comment{
		{SlideID: "s1", Open: true},
		{SlideID: "s1", Open: true},
		{SlideID: "s1", Open: false},
		{SlideID: "s2", Open: true},
		{SlideID: "", Open: true}, // unattributed: cannot be charged to a slide
	}}
	got := c.OpenCounts()
	if got["s1"] != 2 || got["s2"] != 1 {
		t.Errorf("counts = %v, want s1=2 s2=1", got)
	}
	if len(got) != 2 {
		t.Errorf("counts = %v, want unattributed comments left out", got)
	}
}

func TestNilCommentsAreSafe(t *testing.T) {
	var c *Comments
	if len(c.OpenCounts()) != 0 || c.For("s1") != nil || c.Unattributed() != nil {
		t.Error("a nil Comments must behave as empty")
	}
}

// --- the Drive fallback ----------------------------------------------------

func driveComment(id, quote, content string, resolved bool) *drive.Comment {
	return &drive.Comment{
		Id:                id,
		Content:           content,
		Resolved:          resolved,
		CreatedTime:       "2026-10-02T09:12:00Z",
		Author:            &drive.User{DisplayName: "a teammate"},
		QuotedFileContent: &drive.CommentQuotedFileContent{Value: quote},
	}
}

func TestFromDriveAttributesByQuotedText(t *testing.T) {
	pres := presentation(
		slide("s1", "Validation", "Service A"),
		slide("s2", "Deploy"),
	)

	tests := []struct {
		name           string
		quote          string
		wantSlide      string
		wantConfidence Confidence
	}{
		{"unique quote resolves", "Validation", "s1", ConfidenceQuoted},
		{"quote on the other slide", "Deploy", "s2", ConfidenceQuoted},
		{"whitespace is normalized", "  Service   A ", "s1", ConfidenceQuoted},
		{"unknown quote stays unresolved", "Nowhere", "", ConfidenceUnresolved},
		{"empty quote stays unresolved", "", "", ConfidenceUnresolved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromDrive([]*drive.Comment{driveComment("c1", tt.quote, "note", false)}, pres)
			if got.Source != SourceDrive {
				t.Errorf("source = %q, want %q", got.Source, SourceDrive)
			}
			c := got.Items[0]
			if c.SlideID != tt.wantSlide {
				t.Errorf("slideID = %q, want %q", c.SlideID, tt.wantSlide)
			}
			if c.Confidence != tt.wantConfidence {
				t.Errorf("confidence = %q, want %q", c.Confidence, tt.wantConfidence)
			}
		})
	}
}

func TestFromDriveResolvesAQuotedFragment(t *testing.T) {
	// Drive quotes the words the commenter selected, not the whole label, so a
	// comment on one word of a text box must still find its element.
	pres := presentation(
		slide("s1", "👁 Capture the intent", "📋 Plan the steps"),
		slide("s2", "Deploy"),
	)
	got := FromDrive([]*drive.Comment{driveComment("c1", "Capture", "note", false)}, pres)

	c := got.Items[0]
	if c.SlideID != "s1" || c.ObjectID != "s1_ea" {
		t.Errorf("resolved to slide %q element %q, want s1/s1_ea", c.SlideID, c.ObjectID)
	}
	if c.Confidence != ConfidenceQuoted {
		t.Errorf("confidence = %q, want %q", c.Confidence, ConfidenceQuoted)
	}
}

func TestFromDrivePrefersAWholeElementOverAFragment(t *testing.T) {
	// "Deploy" is a label of its own on s2 and a word of a sentence on s1:
	// the element the commenter can have selected whole is the better guess.
	pres := presentation(slide("s1", "Deploy the thing"), slide("s2", "Deploy"))
	got := FromDrive([]*drive.Comment{driveComment("c1", "Deploy", "note", false)}, pres)

	if c := got.Items[0]; c.SlideID != "s2" {
		t.Errorf("resolved to %q, want the slide holding it as a whole label", c.SlideID)
	}
}

func TestFromDriveReportsAnAmbiguousFragment(t *testing.T) {
	pres := presentation(slide("s1", "Plan the steps"), slide("s2", "Plan the release"))
	got := FromDrive([]*drive.Comment{driveComment("c1", "Plan", "note", false)}, pres)

	c := got.Items[0]
	if c.Confidence != ConfidenceAmbiguous || c.SlideID != "" {
		t.Errorf("confidence = %q, slideID = %q, want an ambiguous fragment left unattributed", c.Confidence, c.SlideID)
	}
	if strings.Join(c.Candidates, ",") != "s1,s2" {
		t.Errorf("candidates = %v, want both slides listed", c.Candidates)
	}
}

func TestFromDriveReportsAmbiguityRatherThanGuessing(t *testing.T) {
	// The same label on two slides: Drive's anchor is opaque, so there is no
	// way to tell which one. Saying so beats picking one.
	pres := presentation(slide("s1", "Validation"), slide("s2", "Validation"))
	got := FromDrive([]*drive.Comment{driveComment("c1", "Validation", "note", false)}, pres)

	c := got.Items[0]
	if c.Confidence != ConfidenceAmbiguous {
		t.Errorf("confidence = %q, want %q", c.Confidence, ConfidenceAmbiguous)
	}
	if c.SlideID != "" {
		t.Errorf("slideID = %q, want it left empty when ambiguous", c.SlideID)
	}
	if strings.Join(c.Candidates, ",") != "s1,s2" {
		t.Errorf("candidates = %v, want both slides listed", c.Candidates)
	}
	if len(got.Unattributed()) != 1 {
		t.Error("an ambiguous comment must still be surfaced as unattributed")
	}
}

func TestFromDriveSkipsDeletedAndCarriesReplies(t *testing.T) {
	pres := presentation(slide("s1", "Validation"))

	t.Run("deleted comments are skipped", func(t *testing.T) {
		dc := driveComment("c1", "Validation", "note", false)
		dc.Deleted = true
		if got := FromDrive([]*drive.Comment{dc}, pres); len(got.Items) != 0 {
			t.Errorf("got %d comments, want deleted ones dropped", len(got.Items))
		}
	})

	t.Run("replies are carried", func(t *testing.T) {
		dc := driveComment("c1", "Validation", "head", false)
		dc.Replies = []*drive.Reply{{
			Content:     "agreed",
			CreatedTime: "2026-10-03T10:00:00Z",
			Author:      &drive.User{DisplayName: "someone else"},
		}}
		got := FromDrive([]*drive.Comment{dc}, pres)
		c := got.Items[0]
		if len(c.Thread) != 2 {
			t.Fatalf("thread = %+v, want the comment and its reply", c.Thread)
		}
		if c.Thread[1].Author != "someone else" || c.Thread[1].Text != "agreed" {
			t.Errorf("reply = %+v", c.Thread[1])
		}
	})

	t.Run("resolved state is carried", func(t *testing.T) {
		got := FromDrive([]*drive.Comment{driveComment("c1", "Validation", "note", true)}, pres)
		if got.Items[0].Open {
			t.Error("a resolved Drive comment must not read as open")
		}
	})
}
