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

func TestLifelineDashedFromStyle(t *testing.T) {
	// PlantUML lifeline: everything is in the inline style attribute.
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <line style="stroke:#181818;stroke-width:0.5;stroke-dasharray:5,5;" x1="48" x2="48" y1="81" y2="306"/>
</svg>`)

	var props *slides.LineProperties
	for _, r := range reqs {
		if r.UpdateLineProperties != nil {
			props = r.UpdateLineProperties.LineProperties
		}
	}
	if props == nil {
		t.Fatal("no UpdateLineProperties request emitted")
	}
	if props.DashStyle != "DASH" {
		t.Errorf("dash style = %q, want DASH from style attribute", props.DashStyle)
	}
	if props.LineFill == nil || props.LineFill.SolidFill == nil {
		t.Error("line color from style attribute not applied")
	}
	if props.Weight == nil {
		t.Error("stroke-width from style attribute not applied")
	} else {
		nearEMU(t, "weight", props.Weight.Magnitude, 0.5*testScale)
	}
}

func TestHitboxRectSkipped(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <rect fill="#000000" fill-opacity="0.00000" height="225" width="8" x="44" y="81"/>
</svg>`)
	if shapes := createShapes(reqs); len(shapes) != 0 {
		t.Fatalf("got %d shapes, want the transparent hitbox rect skipped", len(shapes))
	}
}

func TestMessageArrowheadDart(t *testing.T) {
	// Real PlantUML arrowhead: concave quadrilateral pointing right (+x).
	reqs, warnings := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <polygon fill="#181818" points="112.4072,108.7988,122.4072,112.7988,112.4072,116.7988,116.4072,112.7988" style="stroke:#181818;stroke-width:1;"/>
</svg>`)

	for _, w := range warnings {
		if strings.Contains(w, "ignoré") {
			t.Fatalf("arrowhead dart dropped: %q", w)
		}
	}
	shapes := createShapes(reqs)
	if len(shapes) != 1 || shapes[0].ShapeType != "TRIANGLE" {
		t.Fatalf("got %d shapes (first type %s), want one TRIANGLE", len(shapes), shapes[0].ShapeType)
	}
	tr := shapes[0].ElementProperties.Transform
	// Pointing +x means a 90° rotation of the up-pointing TRIANGLE.
	if math.Abs(tr.ShearY-1) > 0.01 || math.Abs(tr.ScaleX) > 0.01 {
		t.Errorf("transform = scaleX %.2f shearY %.2f, want 90° rotation (0, 1)", tr.ScaleX, tr.ShearY)
	}
}

func TestControlDartWithClosingPoint(t *testing.T) {
	// The PlantUML control participant repeats the start point: 5 points
	// that reduce to a dart pointing left.
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <polygon fill="#181818" points="259.5127,37,265.5127,32,263.5127,37,265.5127,42,259.5127,37"/>
</svg>`)
	shapes := createShapes(reqs)
	if len(shapes) != 1 || shapes[0].ShapeType != "TRIANGLE" {
		t.Fatalf("got %d shapes, want one TRIANGLE for the 5-point dart", len(shapes))
	}
	tr := shapes[0].ElementProperties.Transform
	// Pointing -x means a -90° rotation.
	if math.Abs(tr.ShearY+1) > 0.01 || math.Abs(tr.ScaleX) > 0.01 {
		t.Errorf("transform = scaleX %.2f shearY %.2f, want -90° rotation (0, -1)", tr.ScaleX, tr.ShearY)
	}
}

func TestCubicPathNotDropped(t *testing.T) {
	// Open cubic path (database lid seam) must yield connectors.
	reqs, warnings := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <path d="M372.2485,29 C372.2485,39 390.2485,39 390.2485,39 C390.2485,39 408.2485,39 408.2485,29" fill="none" style="stroke:#181818;stroke-width:0.5;"/>
</svg>`)
	for _, w := range warnings {
		if strings.Contains(w, "ignoré") {
			t.Fatalf("cubic path dropped: %q", w)
		}
	}
	lines := 0
	for _, r := range reqs {
		if r.CreateLine != nil {
			lines++
		}
	}
	if lines == 0 {
		t.Fatal("no connectors emitted for the open cubic path")
	}
}

func TestClosedFilledCubicBbox(t *testing.T) {
	// A filled cylinder body alone (closed by drawing back to its start,
	// no Z) becomes its bounding box, not stray connectors.
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <path d="M372.2485,29 C372.2485,19 390.2485,19 390.2485,19 C390.2485,19 408.2485,19 408.2485,29 L408.2485,55 C408.2485,65 390.2485,65 390.2485,65 C390.2485,65 372.2485,65 372.2485,55 L372.2485,29" fill="#E2E2F0" style="stroke:#181818;stroke-width:0.5;"/>
</svg>`)
	shapes := createShapes(reqs)
	if len(shapes) != 1 || shapes[0].ShapeType != "ROUND_RECTANGLE" {
		t.Fatalf("got %d shapes, want one ROUND_RECTANGLE bbox", len(shapes))
	}
	nearEMU(t, "bbox w", shapes[0].ElementProperties.Size.Width.Magnitude, 36*testScale)
	nearEMU(t, "bbox h", shapes[0].ElementProperties.Size.Height.Magnitude, 46*testScale)
}

func TestDatabaseCylinder(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <g class="participant participant-head">
    <text x="355.916" y="78.5352" font-size="14">Database</text>
    <path d="M372.2485,29 C372.2485,19 390.2485,19 390.2485,19 C390.2485,19 408.2485,19 408.2485,29 L408.2485,55 C408.2485,65 390.2485,65 390.2485,65 C390.2485,65 372.2485,65 372.2485,55 L372.2485,29" fill="#E2E2F0" style="stroke:#181818;stroke-width:0.5;"/>
    <path d="M372.2485,29 C372.2485,39 390.2485,39 390.2485,39 C390.2485,39 408.2485,39 408.2485,29" fill="none" style="stroke:#181818;stroke-width:0.5;"/>
  </g>
</svg>`)
	var can *slides.CreateShapeRequest
	for _, s := range createShapes(reqs) {
		if s.ShapeType == "CAN" {
			can = s
		}
	}
	if can == nil {
		t.Fatal("no CAN shape emitted for the database group")
	}
	nearEMU(t, "can w", can.ElementProperties.Size.Width.Magnitude, 36*testScale)
	nearEMU(t, "can h", can.ElementProperties.Size.Height.Magnitude, 46*testScale)
	if can.ElementProperties.Transform.ShearY != 0 {
		t.Errorf("database CAN rotated (shearY %.2f), want upright", can.ElementProperties.Transform.ShearY)
	}
}

func TestQueueCylinderHorizontal(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <g class="participant participant-head">
    <path d="M542.6396,55 L596.4922,55 C601.4922,55 601.4922,68.2441 601.4922,68.2441 C601.4922,68.2441 601.4922,81.4883 596.4922,81.4883 L542.6396,81.4883 C537.6396,81.4883 537.6396,68.2441 537.6396,68.2441 C537.6396,68.2441 537.6396,55 542.6396,55" fill="#E2E2F0" style="stroke:#181818;stroke-width:0.5;"/>
    <path d="M596.4922,55 C591.4922,55 591.4922,68.2441 591.4922,68.2441 C591.4922,81.4883 596.4922,81.4883 596.4922,81.4883" fill="none" style="stroke:#181818;stroke-width:0.5;"/>
    <text x="542.6396" y="73.5352" font-size="14">Queue</text>
  </g>
</svg>`)
	var can *slides.CreateShapeRequest
	for _, s := range createShapes(reqs) {
		if s.ShapeType == "CAN" {
			can = s
		}
	}
	if can == nil {
		t.Fatal("no CAN shape emitted for the queue group")
	}
	tr := can.ElementProperties.Transform
	if math.Abs(tr.ShearY-1) > 0.01 || math.Abs(tr.ScaleX) > 0.01 {
		t.Errorf("transform = scaleX %.2f shearY %.2f, want 90° rotation for a lying cylinder", tr.ScaleX, tr.ShearY)
	}
	// Pre-rotation size: width = visual height, height = visual width.
	nearEMU(t, "can w", can.ElementProperties.Size.Width.Magnitude, 26.4883*testScale)
	nearEMU(t, "can h", can.ElementProperties.Size.Height.Magnitude, 63.8526*testScale)
}

func TestTextAnchorStartUsesTextLength(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <text x="12" y="70.5352" font-family="sans-serif" font-size="14" textLength="72.167">Participant</text>
</svg>`)
	shapes := createShapes(reqs)
	if len(shapes) != 1 || shapes[0].ShapeType != "TEXT_BOX" {
		t.Fatalf("got %d shapes, want one TEXT_BOX", len(shapes))
	}
	box := shapes[0]
	nearEMU(t, "box w", box.ElementProperties.Size.Width.Magnitude, 72.167*testScale+textBoxSlackEMU)
	nearEMU(t, "box x", box.ElementProperties.Transform.TranslateX, 12*testScale-textInsetEMU)

	var alignment, font string
	for _, r := range reqs {
		if r.UpdateParagraphStyle != nil {
			alignment = r.UpdateParagraphStyle.Style.Alignment
		}
		if r.UpdateTextStyle != nil {
			font = r.UpdateTextStyle.Style.FontFamily
		}
	}
	if alignment != "START" {
		t.Errorf("paragraph alignment = %q, want START for un-anchored SVG text", alignment)
	}
	if font != "Arial" {
		t.Errorf("font = %q, want sans-serif mapped to Arial", font)
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
