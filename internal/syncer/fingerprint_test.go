package syncer

import (
	"testing"

	"google.golang.org/api/slides/v1"
)

// text builds a shape element holding the given paragraphs.
func text(shapeType string, paragraphs ...string) *slides.PageElement {
	var els []*slides.TextElement
	for _, p := range paragraphs {
		els = append(els, &slides.TextElement{TextRun: &slides.TextRun{Content: p}})
	}
	return &slides.PageElement{
		ObjectId: "o" + shapeType + paragraphs[0],
		Shape:    &slides.Shape{ShapeType: shapeType, Text: &slides.TextContent{TextElements: els}},
	}
}

func shape(id, shapeType string) *slides.PageElement {
	return &slides.PageElement{ObjectId: id, Shape: &slides.Shape{ShapeType: shapeType}}
}

func TestFingerprintCollectsTextInOrder(t *testing.T) {
	page := &slides.Page{PageElements: []*slides.PageElement{
		text("TEXT_BOX", "Service A"),
		shape("r1", "RECTANGLE"),
		text("TEXT_BOX", "Validation"),
	}}
	fp := Fingerprint(page, false)

	if got, want := len(fp.Texts), 2; got != want {
		t.Fatalf("got %d texts, want %d: %v", got, want, fp.Texts)
	}
	if fp.Texts[0] != "Service A" || fp.Texts[1] != "Validation" {
		t.Errorf("texts = %v, want document order", fp.Texts)
	}
	if fp.Elements != 3 {
		t.Errorf("elements = %d, want 3", fp.Elements)
	}
	if fp.Geometry != "" {
		t.Errorf("geometry = %q, want it omitted unless asked for", fp.Geometry)
	}
}

func TestFingerprintCountsGroupChildren(t *testing.T) {
	page := &slides.Page{PageElements: []*slides.PageElement{
		{
			ObjectId: "g1",
			ElementGroup: &slides.Group{Children: []*slides.PageElement{
				shape("c1", "RECTANGLE"),
				text("TEXT_BOX", "inside"),
			}},
		},
	}}
	fp := Fingerprint(page, false)

	if fp.Elements != 3 {
		t.Errorf("elements = %d, want 3 (the group and its two children)", fp.Elements)
	}
	if len(fp.Texts) != 1 || fp.Texts[0] != "inside" {
		t.Errorf("texts = %v, want the grouped text to be collected", fp.Texts)
	}
}

// Slides stores a trailing newline on every text box and may re-wrap or
// re-pad text. None of that is a human edit, so the fingerprint must be blind
// to it or every sync would report a conflict.
func TestFingerprintIgnoresSlidesTextNormalization(t *testing.T) {
	pushed := &slides.Page{PageElements: []*slides.PageElement{text("TEXT_BOX", "Service A")}}
	readBack := &slides.Page{PageElements: []*slides.PageElement{
		text("TEXT_BOX", "  Service   A  \n"),
	}}
	if !Fingerprint(pushed, false).Equal(Fingerprint(readBack, false)) {
		t.Errorf("whitespace and the trailing newline must not read as an edit:\n%v\n%v",
			Fingerprint(pushed, false).Texts, Fingerprint(readBack, false).Texts)
	}
}

func TestFingerprintDetectsARealEdit(t *testing.T) {
	pushed := &slides.Page{PageElements: []*slides.PageElement{text("TEXT_BOX", "Service A")}}
	edited := &slides.Page{PageElements: []*slides.PageElement{text("TEXT_BOX", "Service Auth")}}
	if Fingerprint(pushed, false).Equal(Fingerprint(edited, false)) {
		t.Error("a retyped label must be detected")
	}
}

func TestFingerprintGeometry(t *testing.T) {
	at := func(x, y, w, h float64) *slides.PageElement {
		return &slides.PageElement{
			ObjectId:  "o",
			Shape:     &slides.Shape{ShapeType: "RECTANGLE"},
			Transform: &slides.AffineTransform{TranslateX: x, TranslateY: y, ScaleX: 1, ScaleY: 1},
			Size: &slides.Size{
				Width:  &slides.Dimension{Magnitude: w},
				Height: &slides.Dimension{Magnitude: h},
			},
		}
	}
	base := &slides.Page{PageElements: []*slides.PageElement{at(0, 0, emuPerPt*10, emuPerPt*10)}}
	moved := &slides.Page{PageElements: []*slides.PageElement{at(emuPerPt*50, 0, emuPerPt*10, emuPerPt*10)}}
	// Sub-point jitter is what the Slides round-trip introduces; it must not
	// register as a move.
	jittered := &slides.Page{PageElements: []*slides.PageElement{at(100, 0, emuPerPt*10, emuPerPt*10)}}

	t.Run("filled only when requested", func(t *testing.T) {
		if Fingerprint(base, true).Geometry == "" {
			t.Error("geometry should be set when asked for")
		}
	})
	t.Run("a real move is detected", func(t *testing.T) {
		if Fingerprint(base, true).Geometry == Fingerprint(moved, true).Geometry {
			t.Error("a 50pt move should change the geometry hash")
		}
	})
	t.Run("sub-point jitter is not", func(t *testing.T) {
		if Fingerprint(base, true).Geometry != Fingerprint(jittered, true).Geometry {
			t.Error("rounding to the point should absorb sub-point jitter")
		}
	})
}

func TestElementsFlattensGroups(t *testing.T) {
	page := &slides.Page{PageElements: []*slides.PageElement{
		shape("a", "RECTANGLE"),
		{ObjectId: "g", ElementGroup: &slides.Group{Children: []*slides.PageElement{
			shape("b", "RECTANGLE"),
			{ObjectId: "g2", ElementGroup: &slides.Group{Children: []*slides.PageElement{shape("c", "RECTANGLE")}}},
		}}},
	}}
	var ids []string
	for _, e := range Elements(page) {
		ids = append(ids, e.ObjectId)
	}
	want := []string{"a", "g", "b", "g2", "c"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
}

func TestTextOf(t *testing.T) {
	if got := TextOf(shape("a", "RECTANGLE")); got != "" {
		t.Errorf("a shape without text gave %q", got)
	}
	if got := TextOf(text("TEXT_BOX", "hello\n")); got != "hello" {
		t.Errorf("TextOf = %q, want %q", got, "hello")
	}
}
