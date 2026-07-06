package mapper

import (
	"math"
	"strings"
	"testing"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

const testScale = 5715.0 // 9144000 EMU / 1600 units, OffX/OffY = 0

func mapSVG(t *testing.T, doc string) ([]*slides.Request, []string) {
	t.Helper()
	root, err := svgpkg.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{
		SlideID: "s",
		Scale:   testScale,
		ViewBox: svgpkg.ViewBox{W: 1600, H: 900},
	}, svgpkg.ParseStylesheet(""))
	return m.Map(root)
}

func createShapes(reqs []*slides.Request) []*slides.CreateShapeRequest {
	var out []*slides.CreateShapeRequest
	for _, r := range reqs {
		if r.CreateShape != nil {
			out = append(out, r.CreateShape)
		}
	}
	return out
}

func nearEMU(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1000 { // 1000 EMU ≈ 0.03mm
		t.Errorf("%s = %.0f EMU, want %.0f EMU", name, got, want)
	}
}

func TestNestedSVGMapped(t *testing.T) {
	reqs, warnings := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <svg x="80" y="165" width="1440" height="680" viewBox="0 0 860 380">
    <rect x="10" y="20" width="100" height="50" fill="#ff0000"/>
    <circle cx="430" cy="190" r="20" fill="#0000ff"/>
    <text x="430" y="50" font-size="20" text-anchor="middle">Hello</text>
  </svg>
</svg>`)

	for _, w := range warnings {
		if strings.Contains(w, "<svg>") {
			t.Errorf("nested <svg> was dropped: %q", w)
		}
	}
	shapes := createShapes(reqs)
	if len(shapes) != 3 {
		t.Fatalf("got %d CreateShape requests, want 3", len(shapes))
	}
	if shapes[0].ShapeType != "RECTANGLE" || shapes[1].ShapeType != "ELLIPSE" || shapes[2].ShapeType != "TEXT_BOX" {
		t.Fatalf("shape types = %s, %s, %s", shapes[0].ShapeType, shapes[1].ShapeType, shapes[2].ShapeType)
	}

	// xMidYMid meet: uniform scale, vertical letterbox.
	s := math.Min(1440.0/860, 680.0/380)
	letterY := 165 + (680-380*s)/2

	rect := shapes[0]
	nearEMU(t, "rect x", rect.ElementProperties.Transform.TranslateX, (80+10*s)*testScale)
	nearEMU(t, "rect y", rect.ElementProperties.Transform.TranslateY, (letterY+20*s)*testScale)
	nearEMU(t, "rect w", rect.ElementProperties.Size.Width.Magnitude, 100*s*testScale)
	nearEMU(t, "rect h", rect.ElementProperties.Size.Height.Magnitude, 50*s*testScale)

	circ := shapes[1]
	nearEMU(t, "circle w", circ.ElementProperties.Size.Width.Magnitude, 40*s*testScale)
	nearEMU(t, "circle h", circ.ElementProperties.Size.Height.Magnitude, 40*s*testScale)
	nearEMU(t, "circle x", circ.ElementProperties.Transform.TranslateX, (80+(430-20)*s)*testScale)

	var fontPt float64
	for _, r := range reqs {
		if r.UpdateTextStyle != nil && r.UpdateTextStyle.Style.FontSize != nil {
			fontPt = r.UpdateTextStyle.Style.FontSize.Magnitude
		}
	}
	if want := ptSize(20*s, testScale); fontPt != want {
		t.Errorf("font size = %g pt, want %g pt", fontPt, want)
	}
}

func TestRootElementsUnchanged(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <rect width="1600" height="900" fill="#ffffff"/>
  <rect x="0" y="0" width="1600" height="10" fill="#00d2dd"/>
</svg>`)

	shapes := createShapes(reqs)
	// The full-page background rect is still dropped; the strip is kept
	// with identity sizing.
	if len(shapes) != 1 {
		t.Fatalf("got %d CreateShape requests, want 1", len(shapes))
	}
	nearEMU(t, "strip w", shapes[0].ElementProperties.Size.Width.Magnitude, 1600*testScale)
	nearEMU(t, "strip h", shapes[0].ElementProperties.Size.Height.Magnitude, 10*testScale)
	nearEMU(t, "strip x", shapes[0].ElementProperties.Transform.TranslateX, 0)
}
