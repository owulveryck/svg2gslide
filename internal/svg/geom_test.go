package svg

import (
	"math"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %g, want %g (±%g)", name, got, want, tol)
	}
}

func TestNestedSVGMatrixMeet(t *testing.T) {
	// slide-01 numbers: non-uniform box ratio, default preserveAspectRatio
	// (xMidYMid meet) => uniform min scale, centered vertically.
	vb := ViewBox{X: 0, Y: 0, W: 860, H: 380}
	m := NestedSVGMatrix(80, 165, 1440, 680, vb, true, "")

	s := math.Min(1440.0/860, 680.0/380) // 1.674419...
	sx, sy := m.ScaleFactors()
	near(t, "sx", sx, s, 1e-9)
	near(t, "sy", sy, s, 1e-9)

	x, y := m.Apply(0, 0)
	near(t, "origin x", x, 80, 1e-9)
	near(t, "origin y", y, 165+(680-380*s)/2, 1e-9)

	// The horizontal axis fills the box exactly (it is the min ratio).
	x, _ = m.Apply(860, 0)
	near(t, "right edge", x, 80+1440, 1e-9)
}

func TestNestedSVGMatrixNone(t *testing.T) {
	vb := ViewBox{X: 0, Y: 0, W: 860, H: 380}
	m := NestedSVGMatrix(80, 165, 1440, 680, vb, true, "none")
	sx, sy := m.ScaleFactors()
	near(t, "sx", sx, 1440.0/860, 1e-9)
	near(t, "sy", sy, 680.0/380, 1e-9)
	x, y := m.Apply(860, 380)
	near(t, "corner x", x, 80+1440, 1e-9)
	near(t, "corner y", y, 165+680, 1e-9)
}

func TestNestedSVGMatrixNoViewBox(t *testing.T) {
	m := NestedSVGMatrix(10, 20, 0, 0, ViewBox{}, false, "")
	x, y := m.Apply(5, 5)
	near(t, "x", x, 15, 1e-9)
	near(t, "y", y, 25, 1e-9)
}

func TestScaleFactorsWithRotation(t *testing.T) {
	m := ParseTransform("rotate(30) scale(2,3)")
	sx, sy := m.ScaleFactors()
	near(t, "sx", sx, 2, 1e-9)
	near(t, "sy", sy, 3, 1e-9)
}
