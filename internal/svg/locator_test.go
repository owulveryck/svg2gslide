package svg

import (
	"strings"
	"testing"
)

func TestLocator(t *testing.T) {
	const doc = `<svg xmlns="http://www.w3.org/2000/svg">
	  <defs><marker id="arrow"/></defs>
	  <g class="layer">
	    <rect x="0"/>
	    <rect x="1"/>
	    <text>hello</text>
	  </g>
	  <g id="second"><circle/></g>
	</svg>`

	root, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tests := []struct {
		name string
		find func() *Element
		want string
	}{
		{"root", func() *Element { return root }, "/svg"},
		{"only child of its tag", func() *Element { return root.Children[0] }, "/svg/defs"},
		{"nested unique tag", func() *Element { return root.Children[0].Children[0] }, "/svg/defs/marker"},
		{"first of two same-tag siblings", func() *Element { return root.Children[1] }, "/svg/g[1]"},
		{"second of two same-tag siblings", func() *Element { return root.Children[2] }, "/svg/g[2]"},
		{"indexed within indexed parent", func() *Element { return root.Children[1].Children[1] }, "/svg/g[1]/rect[2]"},
		{"unique tag among indexed siblings", func() *Element { return root.Children[1].Children[2] }, "/svg/g[1]/text"},
		{"leaf under second group", func() *Element { return root.Children[2].Children[0] }, "/svg/g[2]/circle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Locator(tt.find()); got != tt.want {
				t.Errorf("Locator() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("nil", func(t *testing.T) {
		if got := Locator(nil); got != "" {
			t.Errorf("Locator(nil) = %q, want %q", got, "")
		}
	})

	t.Run("detached element", func(t *testing.T) {
		orphan := &Element{Tag: "rect", Attrs: map[string]string{}, Parent: root.Children[1]}
		if got := Locator(orphan); got != "/svg/g[1]/rect[?]" {
			t.Errorf("Locator(detached) = %q, want %q", got, "/svg/g[1]/rect[?]")
		}
	})
}

// TestLocatorStableUnderUpstreamInsertion is the regression that matters most:
// a Slides object ID derives from the locator, and a comment left on that
// object must still resolve to the same source node after the SVG is edited
// elsewhere. Inserting an element earlier in the document must therefore not
// disturb the locators of unrelated nodes.
func TestLocatorStableUnderUpstreamInsertion(t *testing.T) {
	const before = `<svg xmlns="http://www.w3.org/2000/svg">
	  <g class="a"><rect id="keep"/></g>
	  <g class="b"><text id="label">x</text></g>
	</svg>`
	// A <defs> block and a <circle> appear upstream; "keep" and "label" are
	// untouched.
	const after = `<svg xmlns="http://www.w3.org/2000/svg">
	  <defs><marker id="arrow"/></defs>
	  <g class="a"><circle/><rect id="keep"/></g>
	  <g class="b"><text id="label">x</text></g>
	</svg>`

	locate := func(doc, id string) (locator, key string) {
		t.Helper()
		root, err := Parse(strings.NewReader(doc))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		var found *Element
		root.Walk(func(e *Element) {
			if e.ID() == id {
				found = e
			}
		})
		if found == nil {
			t.Fatalf("element id=%q not found", id)
		}
		return Locator(found), Key(found)
	}

	t.Run("positional locator survives a different-tag insertion", func(t *testing.T) {
		// Ranks count same-tag siblings only, so neither the <defs> added
		// before the groups nor the <circle> added before the <rect> moves
		// the locator of "keep".
		gotBefore, _ := locate(before, "keep")
		gotAfter, _ := locate(after, "keep")
		if gotBefore != gotAfter {
			t.Errorf("locator moved: %q -> %q", gotBefore, gotAfter)
		}
	})

	t.Run("Key is identical for an element carrying an id", func(t *testing.T) {
		for _, id := range []string{"keep", "label"} {
			_, keyBefore := locate(before, id)
			_, keyAfter := locate(after, id)
			if keyBefore != keyAfter {
				t.Errorf("id=%s: Key moved: %q -> %q", id, keyBefore, keyAfter)
			}
			if keyBefore != "#"+id {
				t.Errorf("id=%s: Key() = %q, want %q", id, keyBefore, "#"+id)
			}
		}
	})
}

func TestKey(t *testing.T) {
	const doc = `<svg xmlns="http://www.w3.org/2000/svg">
	  <g><rect id="named"/><rect/></g>
	</svg>`
	root, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	g := root.Children[0]

	tests := []struct {
		name string
		el   *Element
		want string
	}{
		{"id attribute wins", g.Children[0], "#named"},
		{"falls back to the locator", g.Children[1], "/svg/g/rect[2]"},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Key(tt.el); got != tt.want {
				t.Errorf("Key() = %q, want %q", got, tt.want)
			}
		})
	}
}
