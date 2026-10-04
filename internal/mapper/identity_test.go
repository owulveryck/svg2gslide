package mapper

import (
	"regexp"
	"strings"
	"testing"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// mapDoc maps an SVG document and returns the object ID of every created
// page element, keyed by the source node identity it came from.
func mapDoc(t *testing.T, doc string) (byKey map[string][]string, ids []string) {
	t.Helper()
	root, err := svgpkg.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sheet := svgpkg.ParseStylesheet("")
	svgpkg.ApplyStylesheet(root, sheet)
	m := New(Config{SlideID: "svg2gslide_0123456789", Scale: 1, ViewBox: svgpkg.ViewBox{W: 100, H: 100}}, sheet)
	reqs, _ := m.Map(root)
	byKey = map[string][]string{}
	for _, o := range m.Origins() {
		byKey[o.Key] = append(byKey[o.Key], o.ObjectID)
	}
	for _, r := range reqs {
		switch {
		case r.CreateShape != nil:
			ids = append(ids, r.CreateShape.ObjectId)
		case r.CreateLine != nil:
			ids = append(ids, r.CreateLine.ObjectId)
		case r.CreateImage != nil:
			ids = append(ids, r.CreateImage.ObjectId)
		case r.GroupObjects != nil:
			ids = append(ids, r.GroupObjects.GroupObjectId)
		}
	}
	return byKey, ids
}

// TestObjectIDsStableUnderUpstreamEdit is the regression that protects comment
// anchors across syncs. A comment left on a Slides object must still resolve
// to the same source node after the SVG is edited elsewhere, so inserting a
// shape earlier in the document must not renumber unrelated objects — which is
// exactly what the previous sequential nextID() counter did.
func TestObjectIDsStableUnderUpstreamEdit(t *testing.T) {
	const before = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
	  <rect id="first" x="1" y="1" width="10" height="10" fill="#f00"/>
	  <circle id="second" cx="50" cy="50" r="5" fill="#0f0"/>
	</svg>`
	// A rect is inserted at the very top of the document; "first" and
	// "second" are untouched.
	const after = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
	  <rect id="inserted" x="80" y="80" width="5" height="5" fill="#00f"/>
	  <rect id="first" x="1" y="1" width="10" height="10" fill="#f00"/>
	  <circle id="second" cx="50" cy="50" r="5" fill="#0f0"/>
	</svg>`

	beforeByKey, beforeIDs := mapDoc(t, before)
	afterByKey, afterIDs := mapDoc(t, after)

	if len(beforeIDs) == 0 {
		t.Fatal("nothing was mapped; the fixture is wrong")
	}
	if len(afterIDs) != len(beforeIDs)+1 {
		t.Fatalf("got %d objects after the insertion, want %d", len(afterIDs), len(beforeIDs)+1)
	}

	for _, key := range []string{"#first", "#second"} {
		got, want := afterByKey[key], beforeByKey[key]
		if len(want) == 0 {
			t.Fatalf("key %q produced no object; the fixture is wrong", key)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("key %q: object IDs moved after an upstream insertion: %v -> %v", key, want, got)
		}
	}
}

func TestObjectIDsUniqueAndValid(t *testing.T) {
	// Several same-tag siblings without ids, a repeated shape and a path that
	// splits into pieces: all the ways one document can crowd the ID space.
	const doc = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
	  <rect x="1" y="1" width="10" height="10" fill="#f00"/>
	  <rect x="20" y="1" width="10" height="10" fill="#f00"/>
	  <rect x="40" y="1" width="10" height="10" fill="#f00"/>
	  <g><circle cx="10" cy="50" r="4" fill="#0f0"/><circle cx="30" cy="50" r="4" fill="#0f0"/></g>
	  <path d="M 5 80 L 30 80 L 30 95" stroke="#000" fill="none"/>
	  <text x="5" y="70" font-size="6">hello</text>
	</svg>`

	_, ids := mapDoc(t, doc)
	if len(ids) < 6 {
		t.Fatalf("only %d objects mapped; the fixture is too thin to test ID crowding", len(ids))
	}

	// Rules from CreateSlideRequest.objectId: first character [a-zA-Z0-9_],
	// the rest may add hyphen and colon, total length 5-50.
	valid := regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_\-:]*$`)
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Errorf("duplicate object ID %q", id)
		}
		seen[id] = true
		if n := len(id); n < 5 || n > 50 {
			t.Errorf("object ID %q has length %d, outside the API's 5-50", id, n)
		}
		if !valid.MatchString(id) {
			t.Errorf("object ID %q contains forbidden characters", id)
		}
	}
}

func TestIdForIsDeterministic(t *testing.T) {
	const doc = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
	  <rect id="a" x="1" y="1" width="10" height="10" fill="#f00"/>
	  <rect x="20" y="1" width="10" height="10" fill="#0f0"/>
	</svg>`
	_, first := mapDoc(t, doc)
	_, second := mapDoc(t, doc)
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("two runs over the same document gave different IDs:\n%v\n%v", first, second)
	}
}

func TestOriginsRecordTheSourceNode(t *testing.T) {
	const doc = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
	  <g><rect id="named" x="1" y="1" width="10" height="10" fill="#f00"/></g>
	  <rect x="20" y="1" width="10" height="10" fill="#0f0"/>
	</svg>`
	byKey, _ := mapDoc(t, doc)

	t.Run("a node with an id is keyed by it", func(t *testing.T) {
		if _, ok := byKey["#named"]; !ok {
			t.Errorf("no origin keyed #named; got keys %v", keysOf(byKey))
		}
	})
	t.Run("a node without an id falls back to its locator", func(t *testing.T) {
		// No index: this is the only <rect> that is a direct child of <svg>,
		// the other one being nested in the <g>. Ranks count same-tag
		// siblings, not every same-tag node in the document.
		if _, ok := byKey["/svg/rect"]; !ok {
			t.Errorf("no origin keyed by a positional locator; got keys %v", keysOf(byKey))
		}
	})
}

// originsOfDoc maps a document at a realistic scale and returns the full
// provenance, parts included.
func originsOfDoc(t *testing.T, doc string) []ElementOrigin {
	t.Helper()
	root, err := svgpkg.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sheet := svgpkg.ParseStylesheet("")
	svgpkg.ApplyStylesheet(root, sheet)
	m := New(Config{SlideID: "s", Scale: testScale, ViewBox: svgpkg.ViewBox{W: 1600, H: 900}}, sheet)
	m.Map(root)
	return m.Origins()
}

func originFor(t *testing.T, origins []ElementOrigin, locator string) ElementOrigin {
	t.Helper()
	for _, o := range origins {
		if o.Locator == locator {
			return o
		}
	}
	t.Fatalf("no origin for %s", locator)
	return ElementOrigin{}
}

// TestTextPartsNameTheNodeBehindEachWord is what makes a comment actionable: a
// label drawn on a box becomes text *of* that box in Slides, so the object a
// comment is anchored to is the shape, and only the parts can say which of the
// labels around it carries the commented word.
func TestTextPartsNameTheNodeBehindEachWord(t *testing.T) {
	// A captioned box as hand-drawn diagrams write one: a rect with two
	// <text> lines sitting on it.
	origins := originsOfDoc(t, `<svg viewBox="0 0 1600 900">
  <g>
    <rect x="600" y="400" width="300" height="120" rx="12" fill="#DAF6F9"/>
    <text x="750" y="450" text-anchor="middle" font-size="28" font-weight="bold">Capture</text>
    <text x="750" y="490" text-anchor="middle" font-size="22">the intent</text>
  </g>
</svg>`)

	box := originFor(t, origins, "/svg/g/rect")
	if len(box.Parts) != 2 {
		t.Fatalf("parts = %+v, want one per <text> swallowed by the box", box.Parts)
	}
	for i, want := range []struct{ locator, text string }{
		{"/svg/g/text[1]", "Capture"},
		{"/svg/g/text[2]", "the intent"},
	} {
		if got := box.Parts[i]; got.Locator != want.locator || got.Text != want.text {
			t.Errorf("part %d = %s %q, want %s %q", i, got.Locator, got.Text, want.locator, want.text)
		}
	}

	t.Run("the ranges are the ones a comment anchor speaks", func(t *testing.T) {
		// UTF-16 code units over the text actually inserted, newline included:
		// "Capture\nthe intent".
		if p := box.Parts[0]; p.Start != 0 || p.End != 7 {
			t.Errorf("first part spans [%d,%d), want [0,7)", p.Start, p.End)
		}
		if p := box.Parts[1]; p.Start != 8 || p.End != 18 {
			t.Errorf("second part spans [%d,%d), want [8,18)", p.Start, p.End)
		}
	})
}

func TestTextPartsLeftOutWhenTheObjectIsTheNode(t *testing.T) {
	// A lone label is its own object: naming a part would only repeat the
	// origin, so the report keeps pointing at the node it already had.
	origins := originsOfDoc(t, `<svg viewBox="0 0 1600 900">
  <text x="100" y="100" font-size="28">Self-correcting</text>
</svg>`)

	if o := originFor(t, origins, "/svg/text"); len(o.Parts) != 0 {
		t.Errorf("parts = %+v, want none for a label that is its own object", o.Parts)
	}
}

func TestGroupIDForDerivesFromMembers(t *testing.T) {
	m := New(Config{SlideID: "svg2gslide_0123456789"}, svgpkg.ParseStylesheet(""))
	a := m.groupIDFor([]string{"x", "y"})
	b := m.groupIDFor([]string{"x", "y"})
	c := m.groupIDFor([]string{"y", "x"})

	if a == b {
		t.Error("the same members twice must still give two distinct object IDs")
	}
	if !strings.HasPrefix(b, a) {
		t.Errorf("the second ID %q should be the first %q plus a dedup suffix", b, a)
	}
	if strings.TrimSuffix(c, "-1") == strings.TrimSuffix(a, "-1") {
		t.Error("a different member order should give a different ID")
	}
	for _, id := range []string{a, b, c} {
		if n := len(id); n < 5 || n > 50 {
			t.Errorf("group ID %q has length %d, outside the API's 5-50", id, n)
		}
	}
}

func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
