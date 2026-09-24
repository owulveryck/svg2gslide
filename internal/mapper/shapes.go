package mapper

import (
	"math"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// slidesRoundRatio is the corner radius of a native ROUND_RECTANGLE as a
// fraction of its shorter side (the API cannot change it).
const slidesRoundRatio = 0.1667

// roundedRectShape picks the native shape closest to a w×h rectangle with
// corner radius r (all in the same unit). rotate reports a vertical pill,
// drawn as a terminator turned 90°.
func roundedRectShape(w, h, r float64) (shape string, rotate bool) {
	mn := math.Min(w, h)
	r = math.Min(r, mn/2)
	switch {
	case r <= 0:
		return "RECTANGLE", false
	case r >= 0.45*mn && math.Abs(w-h) < 0.1*mn:
		return "ELLIPSE", false
	case r >= 0.45*mn:
		// Pill: the flowchart terminator has fully rounded ends.
		return "FLOW_CHART_TERMINATOR", h > w
	case r < 0.4*slidesRoundRatio*mn:
		// A native rounded rectangle would be far rounder than the SVG:
		// square corners are the closer rendering.
		return "RECTANGLE", false
	}
	return "ROUND_RECTANGLE", false
}

// createShapeRotated creates a w×h shape centred on (cx,cy), rotated by
// theta radians (all EMU).
func (m *Mapper) createShapeRotated(id, shapeType string, cx, cy, w, h, theta float64) {
	cos, sin := math.Cos(theta), math.Sin(theta)
	m.reqs = append(m.reqs, &slides.Request{CreateShape: &slides.CreateShapeRequest{
		ObjectId:  id,
		ShapeType: shapeType,
		ElementProperties: &slides.PageElementProperties{
			PageObjectId: m.cfg.SlideID,
			Size:         sizeEMU(w, h),
			Transform: &slides.AffineTransform{
				ScaleX: cos, ShearX: -sin,
				ShearY: sin, ScaleY: cos,
				TranslateX:      cx - (cos*w/2 - sin*h/2),
				TranslateY:      cy - (sin*w/2 + cos*h/2),
				Unit:            "EMU",
				ForceSendFields: []string{"ScaleX", "ScaleY", "ShearX", "ShearY", "TranslateX", "TranslateY"},
			},
		},
	}})
}

// isDiamond reports whether a quadrilateral has its vertices on the
// midpoints of its bounding-box edges (a rhombus aligned with the axes).
func isDiamond(pts [][2]float64) bool {
	if len(pts) != 4 {
		return false
	}
	minX, minY, w, h := bbox(pts)
	if w <= 0 || h <= 0 {
		return false
	}
	targets := [][2]float64{{minX + w/2, minY}, {minX + w, minY + h/2}, {minX + w/2, minY + h}, {minX, minY + h/2}}
	tol := 0.06 * math.Max(w, h)
	for _, t := range targets {
		found := false
		for _, p := range pts {
			if math.Hypot(p[0]-t[0], p[1]-t[1]) <= tol {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// straightPolygon returns the vertices of a single closed subpath made only
// of straight segments.
func straightPolygon(segs []svgpkg.PathSeg) ([][2]float64, bool) {
	var pts [][2]float64
	var cur [2]float64
	moves := 0
	for _, s := range segs {
		switch s.Op {
		case 'M':
			moves++
			if moves > 1 && len(pts) > 0 {
				return nil, false
			}
			cur = [2]float64{s.Args[0], s.Args[1]}
			pts = append(pts, cur)
		case 'L':
			cur = [2]float64{s.Args[0], s.Args[1]}
			pts = append(pts, cur)
		case 'H':
			cur[0] = s.Args[0]
			pts = append(pts, cur)
		case 'V':
			cur[1] = s.Args[0]
			pts = append(pts, cur)
		case 'Z':
		default:
			return nil, false
		}
	}
	pts = dropClosingPoint(pts)
	return pts, len(pts) >= 3
}

// arcGeom is the centre parameterisation of a circular SVG arc.
type arcGeom struct {
	cx, cy, r     float64
	theta, dtheta float64 // start angle and signed sweep (radians, y down)
}

// circularArc converts an endpoint-parameterised arc to centre form (SVG
// spec F.6.5), for circles only (rx≈ry, no rotation needed).
func circularArc(p0, p1 [2]float64, rx, ry float64, large, sweep bool) (arcGeom, bool) {
	if rx <= 0 || math.Abs(rx-ry) > 0.01*rx {
		return arcGeom{}, false
	}
	r := rx
	dx, dy := (p0[0]-p1[0])/2, (p0[1]-p1[1])/2
	d2 := dx*dx + dy*dy
	if d2 == 0 {
		return arcGeom{}, false
	}
	if d2 > r*r { // radius too small: scale up per spec
		r = math.Sqrt(d2)
	}
	coef := math.Sqrt(math.Max(0, (r*r-d2)/d2))
	if large == sweep {
		coef = -coef
	}
	// Centre = midpoint + coef * perpendicular of (dx,dy).
	cx := (p0[0]+p1[0])/2 + coef*dy
	cy := (p0[1]+p1[1])/2 - coef*dx
	t0 := math.Atan2(p0[1]-cy, p0[0]-cx)
	t1 := math.Atan2(p1[1]-cy, p1[0]-cx)
	dt := t1 - t0
	if sweep && dt < 0 {
		dt += 2 * math.Pi
	} else if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	}
	return arcGeom{cx, cy, r, t0, dt}, true
}

func (a arcGeom) at(t float64) [2]float64 {
	return [2]float64{a.cx + a.r*math.Cos(t), a.cy + a.r*math.Sin(t)}
}

// arcPieces splits a circular arc at quadrant boundaries: full quadrants
// become native ARC shapes, remainders CURVED connectors. Elliptical arcs
// fall back to a single CURVED connector.
func arcPieces(p0, p1 [2]float64, rx, ry float64, large, sweep bool) []pathPiece {
	a, ok := circularArc(p0, p1, rx, ry, large, sweep)
	if !ok {
		return []pathPiece{{category: "CURVED", p0: p0, p1: p1}}
	}
	const q = math.Pi / 2
	const eps = 0.02 // radians
	dir := 1.0
	if a.dtheta < 0 {
		dir = -1
	}
	end := a.theta + a.dtheta
	var out []pathPiece
	t := a.theta
	for dir*(end-t) > eps {
		// Next quadrant boundary in the sweep direction.
		var next float64
		if dir > 0 {
			next = math.Floor(t/q+eps)*q + q
		} else {
			next = math.Ceil(t/q-eps)*q - q
		}
		if dir*(next-end) > 0 {
			next = end
		}
		s, e := a.at(t), a.at(next)
		onStart := math.Abs(t/q-math.Round(t/q)) < eps
		onEnd := math.Abs(next/q-math.Round(next/q)) < eps
		if onStart && onEnd && math.Abs(math.Abs(next-t)-q) < eps {
			out = append(out, pathPiece{
				arc: true, cx: a.cx, cy: a.cy, r: a.r,
				flipX: (s[0]-a.cx)+(e[0]-a.cx) < 0,
				flipY: (s[1]-a.cy)+(e[1]-a.cy) > 0,
			})
		} else if math.Abs(next-t) > eps {
			out = append(out, pathPiece{category: "CURVED", p0: s, p1: e})
		}
		t = next
	}
	if len(out) == 0 {
		return []pathPiece{{category: "CURVED", p0: p0, p1: p1}}
	}
	return out
}

// mapSemicircle maps a closed half-disc (one 180° circular arc closed by
// its diameter) onto a FLOW_CHART_DELAY shape oriented toward the bulge.
func (m *Mapper) mapSemicircle(e *svgpkg.Element, segs []svgpkg.PathSeg, mat svgpkg.Matrix) bool {
	var ops []byte
	for _, s := range segs {
		ops = append(ops, s.Op)
	}
	if string(ops) != "MAZ" && string(ops) != "MA" {
		return false
	}
	p0 := [2]float64{segs[0].Args[0], segs[0].Args[1]}
	A := segs[1].Args
	a, ok := circularArc(p0, [2]float64{A[5], A[6]}, A[0], A[1], A[3] != 0, A[4] != 0)
	if !ok || math.Abs(math.Abs(a.dtheta)-math.Pi) > 0.05 {
		return false
	}
	mid := a.theta + a.dtheta/2
	c := matApply(mat, [2]float64{a.cx, a.cy})
	bulge := matApply(mat, a.at(mid))
	phi := math.Atan2(bulge[1]-c[1], bulge[0]-c[0])
	sx, _ := mat.ScaleFactors()
	r := a.r * sx
	// Box centre sits half a radius from the diameter toward the bulge.
	bx, by := c[0]+math.Cos(phi)*r/2, c[1]+math.Sin(phi)*r/2
	ex, ey := m.toEMU(bx, by)
	id := m.nextID()
	m.createShapeRotated(id, "FLOW_CHART_DELAY", ex, ey, m.lenEMU(r), m.lenEMU(2*r), phi)
	m.styleShape(id, e, mat)
	return true
}

// emitPill draws an exact stadium (rectangle with fully rounded ends) as a
// group of a rectangle and two end discs. Only valid for stroke-less,
// opaque fills, where the overlaps are invisible.
func (m *Mapper) emitPill(e *svgpkg.Element, x, y, w, h float64, mat svgpkg.Matrix) {
	var ids []string
	add := func(shape string, x, y, w, h float64) {
		id := m.nextID()
		ex, ey := m.toEMU(x, y)
		m.createShape(id, shape, ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
		m.styleShape(id, e, mat)
		ids = append(ids, id)
	}
	if w >= h {
		r := h / 2
		add("RECTANGLE", x+r, y, w-h, h)
		add("ELLIPSE", x, y, h, h)
		add("ELLIPSE", x+w-h, y, h, h)
	} else {
		r := w / 2
		add("RECTANGLE", x, y+r, w, h-w)
		add("ELLIPSE", x, y, w, w)
		add("ELLIPSE", x, y+h-w, w, w)
	}
	m.reqs = append(m.reqs, &slides.Request{GroupObjects: &slides.GroupObjectsRequest{
		GroupObjectId:     m.nextID(),
		ChildrenObjectIds: ids,
	}})
}

// opaqueNoStroke reports whether e paints an opaque fill and no stroke.
func (m *Mapper) opaqueNoStroke(e *svgpkg.Element) bool {
	if _, _, ok := m.strokePaint(e); ok {
		return false
	}
	c, ok := m.fillPaint(e)
	return ok && c.a*e.InheritedFloat("fill-opacity", 1)*m.opacity(e) >= 0.999
}
