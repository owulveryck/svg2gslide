package mapper

import "testing"

func TestCubicPieces(t *testing.T) {
	// PlantUML "straight" edge written as a cubic.
	if p := cubicPieces(pt{437, 220}, pt{437, 265}, pt{437, 311}, pt{437, 352}); len(p) != 1 || p[0].category != "STRAIGHT" {
		t.Errorf("straight cubic = %+v, want one STRAIGHT", p)
	}
	// Quarter circle of radius 50 (arm 0.5523·r): native ARC, top-right.
	k := 0.5523 * 50
	p := cubicPieces(pt{0, 0}, pt{k, 0}, pt{50, 50 - k}, pt{50, 50})
	// Centre (0,50): from the top of the circle to its right = the
	// native top-right quadrant, no flip.
	if len(p) != 1 || !p[0].arc || p[0].cx != 0 || p[0].cy != 50 || p[0].rx != 50 || p[0].flipX || p[0].flipY {
		t.Errorf("quarter = %+v, want one unflipped ARC centred on (0,50)", p)
	}
	// Mirrored: from the top heading left down to the left point.
	p = cubicPieces(pt{0, 0}, pt{-k, 0}, pt{-50, 50 - k}, pt{-50, 50})
	if len(p) != 1 || !p[0].arc || !p[0].flipX || p[0].flipY {
		t.Errorf("mirrored quarter = %+v, want ARC with flipX", p)
	}
	// Horizontal S: the native CURVED connector.
	if p := cubicPieces(pt{0, 0}, pt{50, 0}, pt{50, 80}, pt{100, 80}); len(p) != 1 || p[0].category != "CURVED" {
		t.Errorf("S = %+v, want one CURVED", p)
	}
	// Generic spline: flattened, every point within tolerance.
	p = cubicPieces(pt{329, 677}, pt{235, 691}, pt{110, 715}, pt{80, 755})
	if len(p) < 3 {
		t.Errorf("spline flattened in %d pieces, want several", len(p))
	}
	for i := 1; i < len(p); i++ {
		if p[i].p0 != p[i-1].p1 {
			t.Errorf("piece %d not contiguous", i)
		}
	}
}
