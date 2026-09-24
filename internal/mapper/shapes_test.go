package mapper

import (
	"math"
	"strings"
	"testing"
)

func TestRoundedRectShape(t *testing.T) {
	for _, c := range []struct {
		w, h, r float64
		want    string
		rot     bool
	}{
		{430, 700, 16, "RECTANGLE", false},            // big card, subtle radius
		{172, 60, 10, "ROUND_RECTANGLE", false},       // node
		{380, 58, 29, "FLOW_CHART_TERMINATOR", false}, // pill
		{58, 380, 29, "FLOW_CHART_TERMINATOR", true},
		{30, 30, 15, "ELLIPSE", false},
		{100, 50, 0, "RECTANGLE", false},
	} {
		got, rot := roundedRectShape(c.w, c.h, c.r)
		if got != c.want || rot != c.rot {
			t.Errorf("roundedRectShape(%v,%v,%v) = %s,%v want %s,%v", c.w, c.h, c.r, got, rot, c.want, c.rot)
		}
	}
}

func TestRelativeDiamondPath(t *testing.T) {
	reqs, warnings := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <path d="M 510 374 l 11 11 l -11 11 l -11 -11 z" fill="#0D1128" stroke="#2DD4BF" stroke-width="2.4"/>
</svg>`)
	shapes := createShapes(reqs)
	if len(shapes) != 1 || shapes[0].ShapeType != "DIAMOND" {
		t.Fatalf("got %+v (warnings %v), want one DIAMOND", shapes, warnings)
	}
	nearEMU(t, "x", shapes[0].ElementProperties.Transform.TranslateX, 499*testScale)
	nearEMU(t, "w", shapes[0].ElementProperties.Size.Width.Magnitude, 22*testScale)
}

func TestSemicircle(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <path d="M 885 791 A 12 12 0 0 0 885 815 Z" fill="#C9A6FF"/>
</svg>`)
	shapes := createShapes(reqs)
	if len(shapes) != 1 || shapes[0].ShapeType != "FLOW_CHART_DELAY" {
		t.Fatalf("got %+v, want one FLOW_CHART_DELAY", shapes)
	}
	tr := shapes[0].ElementProperties.Transform
	// Left half-disc: bulge toward -x, i.e. rotated 180°.
	if math.Abs(tr.ScaleX+1) > 1e-6 {
		t.Errorf("transform = %+v, want 180° rotation", tr)
	}
	// Visual bbox must be x ∈ [873,885].
	w := shapes[0].ElementProperties.Size.Width.Magnitude
	nearEMU(t, "left", tr.TranslateX-w, 873*testScale)
}

func TestArcSplitIntoQuadrants(t *testing.T) {
	// 270° spinner arc: three native quarter ARC shapes.
	pieces := arcPieces([2]float64{-9, 0}, [2]float64{0, 9}, 9, 9, true, true)
	arcs := 0
	for _, p := range pieces {
		if p.arc {
			arcs++
		}
	}
	if arcs != 3 || len(pieces) != 3 {
		t.Errorf("got %d pieces (%d arcs), want 3 arcs: %+v", len(pieces), arcs, pieces)
	}
}

func TestOpaquePillIsExactStadium(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <rect x="100" y="100" width="300" height="60" rx="30" fill="#ff0000"/>
  <rect x="100" y="300" width="300" height="60" rx="30" fill="#ff0000" stroke="#000"/>
</svg>`)
	var types []string
	groups := 0
	for _, r := range reqs {
		if r.CreateShape != nil {
			types = append(types, r.CreateShape.ShapeType)
		}
		if r.GroupObjects != nil {
			groups++
		}
	}
	want := "RECTANGLE,ELLIPSE,ELLIPSE,FLOW_CHART_TERMINATOR"
	if got := strings.Join(types, ","); got != want || groups != 1 {
		t.Errorf("shapes = %s (groups %d), want %s (1 group)", got, groups, want)
	}
}
