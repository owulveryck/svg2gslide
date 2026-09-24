package svg

import (
	"os"
	"strings"
	"testing"
)

// loadTestSVG parses testdata/sdlc-phase-8.svg and returns the root and its
// stylesheet.
func loadTestSVG(t *testing.T) (*Element, *Stylesheet) {
	t.Helper()
	f, err := os.Open("../../testdata/sdlc-phase-8.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	root, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	styleEl := root.Find("style")
	if styleEl == nil {
		t.Fatal("no <style> element found")
	}
	return root, ParseStylesheet(styleEl.RawTextContent())
}

func TestMergeInlineStyle(t *testing.T) {
	attrs := map[string]string{
		"style":  "stroke:#181818;stroke-width:0.5;stroke-dasharray:5,5;width:608px;background:#FFFFFF",
		"stroke": "#ff0000",
		"width":  "8",
	}
	mergeInlineStyle(attrs)
	if attrs["stroke"] != "#181818" {
		t.Errorf("stroke = %q, want inline style to win over the presentation attribute", attrs["stroke"])
	}
	if attrs["stroke-width"] != "0.5" || attrs["stroke-dasharray"] != "5,5" {
		t.Errorf("stroke-width/dasharray = %q/%q, want promoted from style", attrs["stroke-width"], attrs["stroke-dasharray"])
	}
	if attrs["width"] != "8" {
		t.Errorf("width = %q, want geometry attributes untouched by style", attrs["width"])
	}
	if _, ok := attrs["background"]; ok {
		t.Error("background promoted from style, want it ignored (not whitelisted)")
	}
}

// findByClass returns the first element carrying the class.
func findByClass(e *Element, class string) *Element {
	if e.HasClass(class) {
		return e
	}
	for _, c := range e.Children {
		if f := findByClass(c, class); f != nil {
			return f
		}
	}
	return nil
}

func findByID(e *Element, id string) *Element {
	if e.Attrs["id"] == id {
		return e
	}
	for _, c := range e.Children {
		if f := findByID(c, id); f != nil {
			return f
		}
	}
	return nil
}

func TestPhase8Visibility(t *testing.T) {
	root, sheet := loadTestSVG(t)

	visible := []string{
		"sa-circle-core", "box-problematique", "arrow-prob-conception",
		"box-conception", "box-specs", "box-expression",
		"sa-fond", "sa-enrichit",
		"box-resultat", "arrow-resultat-livraison", "box-livraison",
		"conn-expression-capter", "agent-circle", "agent-title",
		"agent-subtitle", "label-autocorrectif",
		"box-capter", "box-planifier", "box-agir", "box-observer",
		"loop-correction", "arrow-observer-resultat",
		"capter-label",
	}
	hidden := []string{
		"sa-band", "capter-label-alt", "livraison-robot", "st-hl",
		"arrow-dev-resultat", "plat-top", "plat-front", "plat-label",
		"cyl-standards", "roller-left", "belt-right", "cloud-cloud",
		"xaas", "enabling-frame", "enabling-collab",
		"arrow-ctx-technique", "arrow-parametre",
	}

	for _, class := range visible {
		el := findByClass(root, class)
		if el == nil {
			t.Errorf("class %q: no element found", class)
			continue
		}
		if !sheet.Visible(el, "8") {
			t.Errorf("class %q: expected visible in phase 8", class)
		}
	}
	for _, class := range hidden {
		el := findByClass(root, class)
		if el == nil {
			t.Errorf("class %q: no element found", class)
			continue
		}
		if sheet.Visible(el, "8") {
			t.Errorf("class %q: expected hidden in phase 8", class)
		}
	}

	// The spin circles and animated dots must be statically hidden.
	for _, id := range []string{"spin-conception", "spin-agent", "spin-llm"} {
		el := findByID(root, id)
		if el == nil {
			t.Errorf("id %q: no element found", id)
			continue
		}
		if sheet.Visible(el, "8") {
			t.Errorf("id %q: expected hidden", id)
		}
	}
	dot := findByClass(root, "tt-dot-p8")
	if dot == nil {
		t.Fatal("tt-dot-p8 not found")
	}
	if sheet.Visible(dot, "8") {
		t.Error("animated dot tt-dot-p8: expected hidden (circle[data-follow])")
	}
}

func TestPhaseSwitching(t *testing.T) {
	root, sheet := loadTestSVG(t)

	saBand := findByClass(root, "sa-band")
	if saBand == nil {
		t.Fatal("sa-band not found")
	}
	if !sheet.Visible(saBand, "8.1") {
		t.Error("sa-band should be visible in phase 8.1")
	}
	platTop := findByClass(root, "plat-top")
	if platTop == nil {
		t.Fatal("plat-top not found")
	}
	if !sheet.Visible(platTop, "13") {
		t.Error("plat-top should be visible in phase 13")
	}
	if sheet.Visible(platTop, "8") {
		t.Error("plat-top should be hidden in phase 8")
	}
}

func TestApplyStylesheetCascade(t *testing.T) {
	root, err := Parse(strings.NewReader(`<svg>
  <style>
    .item { font-size: 11px; fill: #3E4F78; font-weight: 700 }
    text.item { fill: #111111 }
    .card { fill: #FFFFFF; stroke: #CFD3DD; }
    #special { fill: #FF0000 }
  </style>
  <text class="item" fill="#CFD3DD">stylesheet beats attribute</text>
  <text class="item" style="fill:#00FF00">inline beats stylesheet</text>
  <rect class="card"/>
  <rect class="card" id="special"/>
</svg>`))
	if err != nil {
		t.Fatal(err)
	}
	ApplyStylesheet(root, ParseStylesheet(root.Find("style").RawTextContent()))
	var texts, rects []*Element
	root.Walk(func(e *Element) {
		switch e.Tag {
		case "text":
			texts = append(texts, e)
		case "rect":
			rects = append(rects, e)
		}
	})
	if got := texts[0].Attr("fill"); got != "#111111" {
		t.Errorf("fill = %q, want the more specific text.item rule over the attribute", got)
	}
	if texts[0].Attr("font-size") != "11px" || texts[0].Attr("font-weight") != "700" {
		t.Errorf("font = %q/%q, want class declarations applied", texts[0].Attr("font-size"), texts[0].Attr("font-weight"))
	}
	if got := texts[1].Attr("fill"); got != "#00FF00" {
		t.Errorf("fill = %q, want the inline style to win", got)
	}
	if rects[0].Attr("fill") != "#FFFFFF" || rects[0].Attr("stroke") != "#CFD3DD" {
		t.Errorf("card = %q/%q, want the class fill and stroke", rects[0].Attr("fill"), rects[0].Attr("stroke"))
	}
	if got := rects[1].Attr("fill"); got != "#FF0000" {
		t.Errorf("fill = %q, want the #id rule to win over the class", got)
	}
}
