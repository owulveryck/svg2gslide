package mapper

import "math"

// Bezier curves have no exact native counterpart in Slides: the CURVED
// connector always draws an S (horizontal tangents at both ends) and the
// ARC shape a quarter ellipse. Each cubic is therefore mapped to
//   - a STRAIGHT line when it is (nearly) straight;
//   - a native ARC when it is a quarter ellipse (axis-aligned tangents);
//   - a CURVED connector when it is a horizontal-tangent S;
//   - otherwise a polyline flattened within curveTolerance, whose segments
//     the caller groups so the edge stays one editable object.

// curveTolerance is the maximum deviation (SVG user units) allowed when a
// curve is flattened into straight segments.
const curveTolerance = 0.6

type pt = [2]float64

func sub(a, b pt) pt               { return pt{a[0] - b[0], a[1] - b[1]} }
func lerpPt(a, b pt, t float64) pt { return pt{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t} }
func norm2(a pt) float64           { return math.Hypot(a[0], a[1]) }

// distToLine is the distance from p to the infinite line (a,b).
func distToLine(p, a, b pt) float64 {
	d := sub(b, a)
	l := norm2(d)
	if l < 1e-9 {
		return norm2(sub(p, a))
	}
	return math.Abs((p[0]-a[0])*d[1]-(p[1]-a[1])*d[0]) / l
}

// quadToCubic elevates a quadratic bezier to a cubic.
func quadToCubic(p0, c, p1 pt) (pt, pt) {
	return lerpPt(p0, c, 2.0/3), lerpPt(p1, c, 2.0/3)
}

// cubicPieces maps one cubic bezier onto native pieces (see above).
func cubicPieces(p0, c1, c2, p1 pt) []pathPiece {
	chord := norm2(sub(p1, p0))
	flat := math.Max(distToLine(c1, p0, p1), distToLine(c2, p0, p1))
	if flat <= math.Max(curveTolerance, 0.01*chord) {
		return []pathPiece{{category: "STRAIGHT", p0: p0, p1: p1}}
	}
	if p, ok := quarterEllipse(p0, c1, c2, p1); ok {
		return []pathPiece{p}
	}
	if isHorizontalS(p0, c1, c2, p1) {
		return []pathPiece{{category: "CURVED", p0: p0, p1: p1}}
	}
	var out []pathPiece
	flattenCubic(p0, c1, c2, p1, 0, &out)
	return out
}

// quadPieces maps one quadratic bezier.
func quadPieces(p0, ctrl, p1 pt) []pathPiece {
	c1, c2 := quadToCubic(p0, ctrl, p1)
	return cubicPieces(p0, c1, c2, p1)
}

// flattenCubic subdivides the curve until each piece is flat within
// curveTolerance, emitting STRAIGHT pieces.
func flattenCubic(p0, c1, c2, p1 pt, depth int, out *[]pathPiece) {
	flat := math.Max(distToLine(c1, p0, p1), distToLine(c2, p0, p1))
	if flat <= curveTolerance || depth >= 6 {
		*out = append(*out, pathPiece{category: "STRAIGHT", p0: p0, p1: p1})
		return
	}
	a, b, c := lerpPt(p0, c1, 0.5), lerpPt(c1, c2, 0.5), lerpPt(c2, p1, 0.5)
	d, e := lerpPt(a, b, 0.5), lerpPt(b, c, 0.5)
	m := lerpPt(d, e, 0.5)
	flattenCubic(p0, a, d, m, depth+1, out)
	flattenCubic(m, e, c, p1, depth+1, out)
}

// tangents returns the start and end tangent vectors of a cubic.
func tangents(p0, c1, c2, p1 pt) (pt, pt) {
	t0 := sub(c1, p0)
	if norm2(t0) < 1e-9 {
		t0 = sub(c2, p0)
	}
	t1 := sub(p1, c2)
	if norm2(t1) < 1e-9 {
		t1 = sub(p1, c1)
	}
	return t0, t1
}

const axisTol = 0.2 // tan(~11°)

func isHoriz(t pt) bool { return math.Abs(t[1]) <= axisTol*math.Abs(t[0]) }
func isVert(t pt) bool  { return math.Abs(t[0]) <= axisTol*math.Abs(t[1]) }

func sameSign(a, b float64) bool { return a*b > 0 }

// quarterEllipse recognizes a cubic drawing a quarter of an axis-aligned
// ellipse: one end with a horizontal tangent (top/bottom of the ellipse),
// the other with a vertical one (left/right), control arms ≈ 0.55 radius.
func quarterEllipse(p0, c1, c2, p1 pt) (pathPiece, bool) {
	t0, t1 := tangents(p0, c1, c2, p1)
	dx, dy := p1[0]-p0[0], p1[1]-p0[1]
	var h, v pt // point with horizontal tangent, point with vertical tangent
	var armH, armV float64
	switch {
	case isHoriz(t0) && isVert(t1) && sameSign(t0[0], dx) && sameSign(t1[1], dy):
		h, v = p0, p1
		armH, armV = norm2(t0), norm2(t1)
	case isVert(t0) && isHoriz(t1) && sameSign(t0[1], dy) && sameSign(t1[0], dx):
		h, v = p1, p0
		armH, armV = norm2(t1), norm2(t0)
	default:
		return pathPiece{}, false
	}
	cx, cy := h[0], v[1]
	rx, ry := math.Abs(v[0]-cx), math.Abs(h[1]-cy)
	if rx < 1e-6 || ry < 1e-6 {
		return pathPiece{}, false
	}
	// A true elliptical quarter has arms of 0.5523·radius; accept a range
	// wide enough for hand-tuned curves.
	if r := armH / rx; r < 0.3 || r > 0.8 {
		return pathPiece{}, false
	}
	if r := armV / ry; r < 0.3 || r > 0.8 {
		return pathPiece{}, false
	}
	return pathPiece{
		arc: true, cx: cx, cy: cy, r: math.Min(rx, ry), rx: rx, ry: ry,
		flipX: v[0] < cx,
		flipY: h[1] > cy,
	}, true
}

// isHorizontalS recognizes the geometry of the native CURVED connector: a
// symmetric S with horizontal tangents at both ends, pointing along dx.
func isHorizontalS(p0, c1, c2, p1 pt) bool {
	t0, t1 := tangents(p0, c1, c2, p1)
	dx := p1[0] - p0[0]
	if !isHoriz(t0) || !isHoriz(t1) || !sameSign(t0[0], dx) || !sameSign(t1[0], dx) {
		return false
	}
	// The connector's inflection is at the chord midpoint.
	mid := pt{(p0[0] + 3*c1[0] + 3*c2[0] + p1[0]) / 8, (p0[1] + 3*c1[1] + 3*c2[1] + p1[1]) / 8}
	chordMid := lerpPt(p0, p1, 0.5)
	return norm2(sub(mid, chordMid)) <= math.Max(1, 0.05*norm2(sub(p1, p0)))
}
