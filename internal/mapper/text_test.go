package mapper

import (
	"math"
	"strings"
	"testing"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

func parseText(t *testing.T, doc string) []*textLine {
	t.Helper()
	root, err := svgpkg.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	return parseTextLines(root.Find("text"))
}

func TestParseTextLinesKeepsInlineOrder(t *testing.T) {
	lines := parseText(t, `<svg><text x="10" y="20"><tspan>Designs</tspan> the <tspan font-weight="bold">resolution</tspan> of a problem</text></svg>`)
	if len(lines) != 1 || lines[0].text() != "Designs the resolution of a problem" {
		t.Fatalf("got %d lines: %q", len(lines), lines[0].text())
	}
	if len(lines[0].runs) != 4 {
		t.Errorf("got %d runs, want 4", len(lines[0].runs))
	}
}

func TestParseTextLinesDy(t *testing.T) {
	lines := parseText(t, `<svg><text x="100" y="385" font-size="22">
      <tspan x="100" dy="0">Quelques minutes</tspan>
      <tspan x="100" dy="30">l&#8217;intention</tspan>
      <tspan x="120" dy="1.5em">fin&#8195;·</tspan>
    </text></svg>`)
	want := []struct {
		text    string
		x, base float64
	}{{"Quelques minutes", 100, 385}, {"l\u2019intention", 100, 415}, {"fin\u2003·", 120, 448}}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d", len(lines), len(want))
	}
	for i, w := range want {
		l := lines[i]
		if l.text() != w.text || l.x != w.x || math.Abs(l.baseline-w.base) > 1e-9 {
			t.Errorf("line %d = %q x=%v base=%v, want %+v", i, l.text(), l.x, l.baseline, w)
		}
	}
}

func TestMultilineTextSingleBox(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <text x="100" y="200" font-size="20" fill="#000"><tspan x="100" dy="0">one</tspan><tspan x="100" dy="30">two</tspan><tspan x="100" dy="30">three</tspan></text>
</svg>`)
	shapes := createShapes(reqs)
	if len(shapes) != 1 {
		t.Fatalf("got %d shapes, want 1 text box", len(shapes))
	}
	var text string
	var lineSpacing float64
	var top string
	var above []float64
	for _, r := range reqs {
		if r.InsertText != nil {
			text = r.InsertText.Text
		}
		if p := r.UpdateParagraphStyle; p != nil {
			if p.Style.LineSpacing > 0 {
				lineSpacing = p.Style.LineSpacing
			}
			if p.TextRange.Type == "FIXED_RANGE" && p.Style.SpaceAbove != nil {
				above = append(above, p.Style.SpaceAbove.Magnitude)
			}
		}
		if u := r.UpdateShapeProperties; u != nil {
			top = u.ShapeProperties.ContentAlignment
		}
	}
	if text != "one\ntwo\nthree" {
		t.Errorf("text = %q", text)
	}
	// 30 units pitch for 20 units font = 1.5em: line spacing capped at
	// 115%, the rest as spaceAbove on lines 2 and 3.
	if lineSpacing != 115 {
		t.Errorf("lineSpacing = %v, want 115", lineSpacing)
	}
	wantAbove := (30 - naturalPitch(1.15, 20, 20)) * testScale / emuPerPt
	if len(above) != 2 || math.Abs(above[0]-wantAbove) > 0.02 || math.Abs(above[1]-wantAbove) > 0.02 {
		t.Errorf("spaceAbove = %v pt, want 2× %.2f", above, wantAbove)
	}
	if top != "TOP" {
		t.Errorf("contentAlignment = %q, want TOP", top)
	}
	// First baseline must land on y=200.
	box := shapes[0].ElementProperties
	f := 20 * testScale
	wantTop := 200*testScale - textInsetEMU - firstBaselineOffset(f, 1.15)
	nearEMU(t, "box y", box.Transform.TranslateY, wantTop)
}

func TestRotatedText(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <text transform="translate(800,450) rotate(-90)" text-anchor="middle" font-size="20">UP</text>
</svg>`)
	tr := createShapes(reqs)[0].ElementProperties.Transform
	if math.Abs(tr.ScaleX) > 1e-9 || math.Abs(tr.ShearY+1) > 1e-9 || math.Abs(tr.ShearX-1) > 1e-9 {
		t.Errorf("transform = %+v, want a -90° rotation", tr)
	}
}

func TestIsBold(t *testing.T) {
	for w, want := range map[string]bool{"bold": true, "700": true, "600": true, "500": false, "normal": false, "": false} {
		if isBold(w) != want {
			t.Errorf("isBold(%q) = %v", w, !want)
		}
	}
}
