// Package mapper converts a parsed SVG tree into native Google Slides
// batchUpdate requests (shapes, lines and text boxes).
package mapper

import (
	"fmt"
	"math"
	"strings"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

const (
	emuPerPt = 12700.0
	// Default text box inset (0.1") on each side, compensated so anchored
	// text starts exactly on the SVG anchor point.
	textInsetEMU = 91440.0
)

// Config parametrizes a conversion run.
type Config struct {
	SlideID string
	Phase   string
	// Scale is the EMU-per-SVG-unit factor; OffX/OffY center the drawing
	// on the page (already in EMU).
	Scale      float64
	OffX, OffY float64
	ViewBox    svgpkg.ViewBox
	FontFamily string
	Verbose    bool
}

// Mapper walks the SVG tree and accumulates Slides requests.
type Mapper struct {
	cfg      Config
	sheet    *svgpkg.Stylesheet
	reqs     []*slides.Request
	warnings []string
	n        int

	grads  map[string]*gradient
	pageBG rgba // current page background (composited full-page layers)
	bgSet  bool
}

// New creates a Mapper.
func New(cfg Config, sheet *svgpkg.Stylesheet) *Mapper {
	if cfg.FontFamily == "" {
		cfg.FontFamily = "Arial"
	}
	return &Mapper{cfg: cfg, sheet: sheet, pageBG: white, grads: map[string]*gradient{}}
}

// Map converts the SVG root into Slides requests targeting cfg.SlideID.
func (m *Mapper) Map(root *svgpkg.Element) ([]*slides.Request, []string) {
	m.grads = collectGradients(root)
	for _, c := range root.Children {
		m.walk(c, svgpkg.Identity())
	}
	if m.bgSet {
		m.reqs = append(m.reqs, &slides.Request{UpdatePageProperties: &slides.UpdatePagePropertiesRequest{
			ObjectId: m.cfg.SlideID,
			PageProperties: &slides.PageProperties{PageBackgroundFill: &slides.PageBackgroundFill{
				SolidFill: &slides.SolidFill{Color: m.pageBG.opaque(), Alpha: 1},
			}},
			Fields: "pageBackgroundFill.solidFill",
		}})
	}
	return m.reqs, m.warnings
}

var skippedTags = map[string]bool{
	"defs": true, "style": true, "mask": true, "clipPath": true,
	"marker": true, "filter": true, "linearGradient": true,
	"radialGradient": true, "title": true, "desc": true, "metadata": true,
}

func (m *Mapper) walk(e *svgpkg.Element, parent svgpkg.Matrix) {
	if skippedTags[e.Tag] {
		return
	}
	if m.sheet.Opacity(e, m.cfg.Phase) < 0.01 {
		return
	}
	mat := parent
	if t := e.Attr("transform"); t != "" {
		mat = parent.Mul(svgpkg.ParseTransform(t))
	}

	switch e.Tag {
	case "g":
		if m.map3DBox(e, mat) {
			return
		}
		if m.mapCylinder(e, mat) {
			return
		}
		for _, c := range e.Children {
			m.walk(c, mat)
		}
	case "rect":
		m.mapRect(e, mat)
	case "circle":
		m.mapCircle(e, mat)
	case "ellipse":
		m.mapEllipse(e, mat)
	case "line":
		m.mapLine(e, mat)
	case "path":
		m.mapPath(e, mat)
	case "polygon":
		m.mapPolygon(e, mat)
	case "text":
		m.mapText(e, mat)
	case "svg":
		// A nested <svg> establishes a new coordinate system.
		vb, err := svgpkg.ParseViewBox(e.Attr("viewBox"))
		nested := svgpkg.NestedSVGMatrix(
			e.FloatAttr("x", 0), e.FloatAttr("y", 0),
			e.FloatAttr("width", 0), e.FloatAttr("height", 0),
			vb, err == nil, e.Attr("preserveAspectRatio"))
		mat = mat.Mul(nested)
		for _, c := range e.Children {
			m.walk(c, mat)
		}
	default:
		m.warnf("élément <%s> ignoré (non supporté)", e.Tag)
	}
}

func (m *Mapper) warnf(format string, args ...any) {
	m.warnings = append(m.warnings, fmt.Sprintf(format, args...))
}

func (m *Mapper) nextID() string {
	m.n++
	return fmt.Sprintf("%s_e%03d", m.cfg.SlideID, m.n)
}

// toEMU converts SVG user coordinates to page EMU.
func (m *Mapper) toEMU(x, y float64) (float64, float64) {
	return (x-m.cfg.ViewBox.X)*m.cfg.Scale + m.cfg.OffX,
		(y-m.cfg.ViewBox.Y)*m.cfg.Scale + m.cfg.OffY
}

func (m *Mapper) lenEMU(v float64) float64 { return v * m.cfg.Scale }

// ---------------------------------------------------------------------------
// Shapes

func (m *Mapper) mapRect(e *svgpkg.Element, mat svgpkg.Matrix) {
	x, y := mat.Apply(e.FloatAttr("x", 0), e.FloatAttr("y", 0))
	sx, sy := mat.ScaleFactors()
	w := e.FloatAttr("width", 0) * sx
	h := e.FloatAttr("height", 0) * sy
	if w <= 0 || h <= 0 {
		return
	}
	// Fully transparent, stroke-less rectangles are hitbox/spacer helpers.
	stroke := e.Inherited("stroke")
	noStroke := stroke == "" || stroke == "none"
	if noStroke && e.InheritedFloat("fill-opacity", 1) <= 0.01 {
		return
	}
	// A full-canvas background rectangle becomes the page background:
	// it would only get in the way of editing as a shape.
	vb := m.cfg.ViewBox
	if e.Attr("class") == "" && math.Abs(x-vb.X) < 1 && math.Abs(y-vb.Y) < 1 &&
		w >= vb.W*0.95 && h >= vb.H*0.95 && noStroke {
		m.absorbBackground(e)
		return
	}
	rx := e.FloatAttr("rx", -1)
	ry := e.FloatAttr("ry", -1)
	if rx < 0 {
		rx = ry
	}
	if ry < 0 {
		ry = rx
	}
	r := math.Min(math.Max(rx, 0)*sx, math.Max(ry, 0)*sy)
	shapeType, rot := roundedRectShape(w, h, r)
	if shapeType == "FLOW_CHART_TERMINATOR" && m.opaqueNoStroke(e) {
		m.emitPill(e, x, y, w, h, mat)
		return
	}
	id := m.nextID()
	ex, ey := m.toEMU(x, y)
	if rot {
		// Vertical pill: a horizontal terminator turned 90°.
		m.createShapeRotated(id, shapeType, ex+m.lenEMU(w)/2, ey+m.lenEMU(h)/2, m.lenEMU(h), m.lenEMU(w), math.Pi/2)
	} else {
		m.createShape(id, shapeType, ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
	}
	m.styleShape(id, e, mat)
}

func (m *Mapper) mapCircle(e *svgpkg.Element, mat svgpkg.Matrix) {
	cx, cy := mat.Apply(e.FloatAttr("cx", 0), e.FloatAttr("cy", 0))
	r := e.FloatAttr("r", 0)
	if r <= 0 {
		return
	}
	sx, sy := mat.ScaleFactors()
	id := m.nextID()
	ex, ey := m.toEMU(cx-r*sx, cy-r*sy)
	m.createShape(id, "ELLIPSE", ex, ey, m.lenEMU(2*r*sx), m.lenEMU(2*r*sy), 0)
	m.styleShape(id, e, mat)
}

func (m *Mapper) mapEllipse(e *svgpkg.Element, mat svgpkg.Matrix) {
	cx, cy := mat.Apply(e.FloatAttr("cx", 0), e.FloatAttr("cy", 0))
	sx, sy := mat.ScaleFactors()
	rx := e.FloatAttr("rx", 0) * sx
	ry := e.FloatAttr("ry", 0) * sy
	if rx <= 0 || ry <= 0 {
		return
	}
	id := m.nextID()
	ex, ey := m.toEMU(cx-rx, cy-ry)
	m.createShape(id, "ELLIPSE", ex, ey, m.lenEMU(2*rx), m.lenEMU(2*ry), 0)
	m.styleShape(id, e, mat)
}

// map3DBox recognizes a group drawn as an isometric box (several >=4-point
// polygons) and replaces the polygons and interior lines by a single native
// CUBE shape. Returns false when the group doesn't match, in which case the
// caller recurses normally.
func (m *Mapper) map3DBox(e *svgpkg.Element, mat svgpkg.Matrix) bool {
	var faces []*svgpkg.Element
	for _, c := range e.Children {
		if c.Tag == "polygon" && len(svgpkg.ParsePoints(c.Attr("points"))) >= 4 {
			faces = append(faces, c)
		}
	}
	if len(faces) < 3 {
		return false
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, f := range faces {
		for _, p := range svgpkg.ParsePoints(f.Attr("points")) {
			x, y := mat.Apply(p[0], p[1])
			minX, minY = math.Min(minX, x), math.Min(minY, y)
			maxX, maxY = math.Max(maxX, x), math.Max(maxY, y)
		}
	}
	id := m.nextID()
	ex, ey := m.toEMU(minX, minY)
	m.createShape(id, "CUBE", ex, ey, m.lenEMU(maxX-minX), m.lenEMU(maxY-minY), 0)
	m.styleShape(id, faces[0], mat)
	m.warnf("groupe %q approximé par une forme CUBE", e.Attr("class"))

	for _, c := range e.Children {
		if c.Tag == "polygon" || c.Tag == "line" {
			continue // absorbed by the cube
		}
		m.walk(c, mat)
	}
	return true
}

// mapCylinder recognizes a group drawn as a cylinder — a filled closed path
// with cubic sides plus a stroke-only cubic path for the lid seam (PlantUML
// database and queue participants) — and replaces both paths by a native CAN
// shape, rotated when the cylinder lies on its side. Returns false when the
// group doesn't match.
func (m *Mapper) mapCylinder(e *svgpkg.Element, mat svgpkg.Matrix) bool {
	var body, lid *svgpkg.Element
	for _, c := range e.Children {
		if c.Tag != "path" {
			continue
		}
		segs, err := svgpkg.ParsePathD(c.Attr("d"))
		if err != nil {
			return false
		}
		cubics := 0
		for _, s := range segs {
			if s.Op == 'C' {
				cubics += len(s.Args) / 6
			}
		}
		switch {
		case cubics >= 2 && m.isFilled(c) && body == nil:
			body = c
		case cubics >= 1 && !m.isFilled(c) && lid == nil:
			lid = c
		default:
			return false
		}
	}
	if body == nil || lid == nil {
		return false
	}

	bodyBox := applyAll(mat, pathEndpoints(body))
	lidBox := applyAll(mat, pathEndpoints(lid))
	minX, minY, w, h := bbox(bodyBox)
	if w <= 0 || h <= 0 {
		return false
	}
	_, _, lw, lh := bbox(lidBox)

	id := m.nextID()
	if lw >= lh {
		// Flat lid: upright cylinder.
		ex, ey := m.toEMU(minX, minY)
		m.createShape(id, "CAN", ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
	} else {
		// Tall lid on the side: cylinder lying down, rotate the CAN 90°
		// clockwise so its top faces right (same transform layout as
		// mapTrianglePolygon).
		ecx, ecy := m.toEMU(minX+w/2, minY+h/2)
		wEMU, hEMU := m.lenEMU(h), m.lenEMU(w)
		cos, sin := 0.0, 1.0
		tx := ecx - (cos*wEMU/2 - sin*hEMU/2)
		ty := ecy - (sin*wEMU/2 + cos*hEMU/2)
		m.reqs = append(m.reqs, &slides.Request{CreateShape: &slides.CreateShapeRequest{
			ObjectId:  id,
			ShapeType: "CAN",
			ElementProperties: &slides.PageElementProperties{
				PageObjectId: m.cfg.SlideID,
				Size:         sizeEMU(wEMU, hEMU),
				Transform: &slides.AffineTransform{
					ScaleX: cos, ShearX: -sin,
					ShearY: sin, ScaleY: cos,
					TranslateX: tx, TranslateY: ty,
					Unit:            "EMU",
					ForceSendFields: []string{"ScaleX", "ScaleY", "ShearX", "ShearY", "TranslateX", "TranslateY"},
				},
			},
		}})
	}
	m.styleShape(id, body, mat)
	m.warnf("groupe %q approximé par une forme CAN", e.Attr("class"))

	for _, c := range e.Children {
		if c.Tag == "path" {
			continue // absorbed by the cylinder
		}
		m.walk(c, mat)
	}
	return true
}

// pathEndpoints returns the on-curve points of a path (control points are
// excluded, which is what a footprint bbox wants).
func pathEndpoints(e *svgpkg.Element) [][2]float64 {
	segs, err := svgpkg.ParsePathD(e.Attr("d"))
	if err != nil {
		return nil
	}
	var pts [][2]float64
	var cur [2]float64
	add := func(x, y float64) {
		cur = [2]float64{x, y}
		pts = append(pts, cur)
	}
	for _, s := range segs {
		switch s.Op {
		case 'M', 'L':
			for i := 0; i+1 < len(s.Args); i += 2 {
				add(s.Args[i], s.Args[i+1])
			}
		case 'Q':
			for i := 0; i+3 < len(s.Args); i += 4 {
				add(s.Args[i+2], s.Args[i+3])
			}
		case 'C':
			for i := 0; i+5 < len(s.Args); i += 6 {
				add(s.Args[i+4], s.Args[i+5])
			}
		case 'A':
			for i := 0; i+6 < len(s.Args); i += 7 {
				add(s.Args[i+5], s.Args[i+6])
			}
		case 'H':
			for _, x := range s.Args {
				add(x, cur[1])
			}
		case 'V':
			for _, y := range s.Args {
				add(cur[0], y)
			}
		}
	}
	return pts
}

func (m *Mapper) mapPolygon(e *svgpkg.Element, mat svgpkg.Matrix) {
	m.mapPolygonPts(e, dropClosingPoint(svgpkg.ParsePoints(e.Attr("points"))), mat)
}

func (m *Mapper) mapPolygonPts(e *svgpkg.Element, pts [][2]float64, mat svgpkg.Matrix) {
	switch {
	case len(pts) == 3:
		m.mapTrianglePolygon(e, pts, mat)
	case len(pts) == 4 && isAxisAlignedRect(pts):
		tpts := applyAll(mat, pts)
		minX, minY, w, h := bbox(tpts)
		id := m.nextID()
		ex, ey := m.toEMU(minX, minY)
		m.createShape(id, "RECTANGLE", ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
		m.styleShape(id, e, mat)
	case len(pts) == 4:
		if tri, apex, ok := dartApexTriangle(pts); ok {
			m.emitTriangle(e, applyAll(mat, tri), matApply(mat, apex), mat)
			return
		}
		tpts := applyAll(mat, pts)
		if isDiamond(tpts) {
			minX, minY, w, h := bbox(tpts)
			id := m.nextID()
			ex, ey := m.toEMU(minX, minY)
			m.createShape(id, "DIAMOND", ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
			m.styleShape(id, e, mat)
			return
		}
		minX, minY, w, h := bbox(tpts)
		id := m.nextID()
		ex, ey := m.toEMU(minX, minY)
		m.createShape(id, "RECTANGLE", ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
		m.styleShape(id, e, mat)
		m.warnf("quadrilatère %q approximé par un rectangle", e.Attr("class"))
	default:
		m.warnf("polygone à %d points ignoré (classe %q)", len(pts), e.Attr("class"))
	}
}

// dropClosingPoint removes a trailing point that duplicates the first one
// (polygons are implicitly closed, some generators repeat the start point).
func dropClosingPoint(pts [][2]float64) [][2]float64 {
	if n := len(pts); n >= 2 &&
		math.Abs(pts[0][0]-pts[n-1][0]) < 0.01 && math.Abs(pts[0][1]-pts[n-1][1]) < 0.01 {
		return pts[:n-1]
	}
	return pts
}

// dartApexTriangle reduces a concave quadrilateral (arrowhead "dart") to the
// triangle spanned by its convex vertices. The apex is the vertex opposite
// the single reflex vertex, which is where such an arrowhead points.
func dartApexTriangle(pts [][2]float64) (tri [][2]float64, apex [2]float64, ok bool) {
	if len(pts) != 4 {
		return nil, apex, false
	}
	area := 0.0
	for i := range pts {
		p, q := pts[i], pts[(i+1)%4]
		area += p[0]*q[1] - q[0]*p[1]
	}
	reflex := -1
	for i := range pts {
		prev, cur, next := pts[(i+3)%4], pts[i], pts[(i+1)%4]
		cross := (cur[0]-prev[0])*(next[1]-cur[1]) - (cur[1]-prev[1])*(next[0]-cur[0])
		if cross*area < 0 {
			if reflex >= 0 {
				return nil, apex, false // self-intersecting, not a dart
			}
			reflex = i
		}
	}
	if reflex < 0 {
		return nil, apex, false // convex quadrilateral
	}
	for i := range pts {
		if i != reflex {
			tri = append(tri, pts[i])
		}
	}
	return tri, pts[(reflex+2)%4], true
}

func matApply(mat svgpkg.Matrix, p [2]float64) [2]float64 {
	x, y := mat.Apply(p[0], p[1])
	return [2]float64{x, y}
}

// mapTrianglePolygon maps a 3-point polygon (arrowhead chevrons) onto the
// native TRIANGLE shape, rotated to match the pointing direction.
func (m *Mapper) mapTrianglePolygon(e *svgpkg.Element, pts [][2]float64, mat svgpkg.Matrix) {
	// Work in transformed space so scale and rotation are baked in.
	pts = applyAll(mat, pts)
	minX, minY, w, h := bbox(pts)
	cx, cy := minX+w/2, minY+h/2
	// The vertex farthest from the bbox center is the apex.
	apex := pts[0]
	best := -1.0
	for _, p := range pts {
		d := (p[0]-cx)*(p[0]-cx) + (p[1]-cy)*(p[1]-cy)
		if d > best {
			best = d
			apex = p
		}
	}
	m.emitTriangle(e, pts, apex, mat)
}

// emitTriangle creates a native TRIANGLE covering pts (already transformed),
// rotated so it points toward apex.
func (m *Mapper) emitTriangle(e *svgpkg.Element, pts [][2]float64, apex [2]float64, mat svgpkg.Matrix) {
	minX, minY, w, h := bbox(pts)
	cx, cy := minX+w/2, minY+h/2
	pointing := math.Atan2(apex[1]-cy, apex[0]-cx)
	// The Slides TRIANGLE points up (-y): rotate by pointing - (-90°).
	rot := pointing + math.Pi/2

	// Extent along the pointing axis becomes the triangle height.
	cosP, sinP := math.Cos(pointing), math.Sin(pointing)
	along, perp := 0.0, 0.0
	for _, p := range pts {
		for _, q := range pts {
			dx, dy := p[0]-q[0], p[1]-q[1]
			along = math.Max(along, math.Abs(dx*cosP+dy*sinP))
			perp = math.Max(perp, math.Abs(-dx*sinP+dy*cosP))
		}
	}
	ecx, ecy := m.toEMU(cx, cy)
	wEMU, hEMU := m.lenEMU(perp), m.lenEMU(along)

	id := m.nextID()
	cos, sin := math.Cos(rot), math.Sin(rot)
	tx := ecx - (cos*wEMU/2 - sin*hEMU/2)
	ty := ecy - (sin*wEMU/2 + cos*hEMU/2)
	m.reqs = append(m.reqs, &slides.Request{CreateShape: &slides.CreateShapeRequest{
		ObjectId:  id,
		ShapeType: "TRIANGLE",
		ElementProperties: &slides.PageElementProperties{
			PageObjectId: m.cfg.SlideID,
			Size:         sizeEMU(wEMU, hEMU),
			Transform: &slides.AffineTransform{
				ScaleX: cos, ShearX: -sin,
				ShearY: sin, ScaleY: cos,
				TranslateX: tx, TranslateY: ty,
				Unit:            "EMU",
				ForceSendFields: []string{"ScaleX", "ScaleY", "ShearX", "ShearY", "TranslateX", "TranslateY"},
			},
		},
	}})
	m.styleShape(id, e, mat)
}

func applyAll(mat svgpkg.Matrix, pts [][2]float64) [][2]float64 {
	out := make([][2]float64, len(pts))
	for i, p := range pts {
		x, y := mat.Apply(p[0], p[1])
		out[i] = [2]float64{x, y}
	}
	return out
}

func isAxisAlignedRect(pts [][2]float64) bool {
	for i := range pts {
		p, q := pts[i], pts[(i+1)%len(pts)]
		if math.Abs(p[0]-q[0]) > 0.01 && math.Abs(p[1]-q[1]) > 0.01 {
			return false
		}
	}
	return true
}

func bbox(pts [][2]float64) (minX, minY, w, h float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, p := range pts {
		minX, minY = math.Min(minX, p[0]), math.Min(minY, p[1])
		maxX, maxY = math.Max(maxX, p[0]), math.Max(maxY, p[1])
	}
	return minX, minY, maxX - minX, maxY - minY
}

// ---------------------------------------------------------------------------
// Lines and paths

func (m *Mapper) mapLine(e *svgpkg.Element, mat svgpkg.Matrix) {
	x1, y1 := mat.Apply(e.FloatAttr("x1", 0), e.FloatAttr("y1", 0))
	x2, y2 := mat.Apply(e.FloatAttr("x2", 0), e.FloatAttr("y2", 0))
	m.createLine(e, "STRAIGHT", x1, y1, x2, y2, mat)
}

func (m *Mapper) mapPath(e *svgpkg.Element, mat svgpkg.Matrix) {
	if f := e.Inherited("fill"); f == "none" && (e.Inherited("stroke") == "none" || e.Inherited("stroke") == "") {
		return // invisible motion-path rail
	}
	segs, err := svgpkg.ParsePathD(e.Attr("d"))
	if err != nil {
		m.warnf("path %q ignoré: %v", e.Attr("class"), err)
		return
	}
	// Walk segments, emitting one connector (or native ARC shape) per
	// L/Q/C/A command. The marker-end arrowhead goes on the last connector.
	var pieces []pathPiece
	var cur, start [2]float64
	var outline [][2]float64
	closed, hasStart := false, false
	for _, s := range segs {
		switch s.Op {
		case 'Z':
			closed = true
			if cur != start {
				pieces = append(pieces, pathPiece{category: "STRAIGHT", p0: cur, p1: start})
				cur = start
			}
		case 'M':
			if len(s.Args) >= 2 {
				cur = [2]float64{s.Args[0], s.Args[1]}
				if !hasStart {
					start, hasStart = cur, true
				}
				outline = append(outline, cur)
			}
		case 'L':
			for i := 0; i+1 < len(s.Args); i += 2 {
				pieces = append(pieces, pathPiece{category: "STRAIGHT", p0: cur, p1: [2]float64{s.Args[i], s.Args[i+1]}})
				cur = [2]float64{s.Args[i], s.Args[i+1]}
				outline = append(outline, cur)
			}
		case 'Q':
			for i := 0; i+3 < len(s.Args); i += 4 {
				ctrl := [2]float64{s.Args[i], s.Args[i+1]}
				end := [2]float64{s.Args[i+2], s.Args[i+3]}
				pieces = append(pieces, quadPieces(cur, ctrl, end)...)
				cur = end
				outline = append(outline, cur)
			}
		case 'C':
			for i := 0; i+5 < len(s.Args); i += 6 {
				c1 := [2]float64{s.Args[i], s.Args[i+1]}
				c2 := [2]float64{s.Args[i+2], s.Args[i+3]}
				end := [2]float64{s.Args[i+4], s.Args[i+5]}
				pieces = append(pieces, cubicPieces(cur, c1, c2, end)...)
				cur = end
				outline = append(outline, cur)
			}
		case 'A':
			for i := 0; i+6 < len(s.Args); i += 7 {
				end := [2]float64{s.Args[i+5], s.Args[i+6]}
				pieces = append(pieces, arcPieces(cur, end, s.Args[i], s.Args[i+1], s.Args[i+3] != 0, s.Args[i+4] != 0)...)
				cur = end
				outline = append(outline, cur)
			}
		case 'H':
			for _, x := range s.Args {
				pieces = append(pieces, pathPiece{category: "STRAIGHT", p0: cur, p1: [2]float64{x, cur[1]}})
				cur[0] = x
				outline = append(outline, cur)
			}
		case 'V':
			for _, y := range s.Args {
				pieces = append(pieces, pathPiece{category: "STRAIGHT", p0: cur, p1: [2]float64{cur[0], y}})
				cur[1] = y
				outline = append(outline, cur)
			}
		}
	}
	if len(pieces) == 0 {
		return
	}
	// Some generators close a subpath by drawing back to its start instead
	// of using Z (PlantUML cylinders do this).
	if hasStart && math.Hypot(cur[0]-start[0], cur[1]-start[1]) < 0.5 {
		closed = true
	}
	// A closed, filled path is a free-form solid: keep its footprint as a
	// rounded rectangle rather than exploding it into stray connectors.
	if closed && m.isFilled(e) {
		if poly, ok := straightPolygon(segs); ok {
			m.mapPolygonPts(e, poly, mat)
			return
		}
		if m.mapSemicircle(e, segs, mat) {
			return
		}
		tpts := applyAll(mat, outline)
		minX, minY, w, h := bbox(tpts)
		id := m.nextID()
		ex, ey := m.toEMU(minX, minY)
		m.createShape(id, "ROUND_RECTANGLE", ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
		m.styleShape(id, e, mat)
		m.warnf("path fermé %q approximé par sa boîte englobante", pathLabel(e))
		return
	}
	if len(pieces) > 1 {
		m.warnf("path %q approximé par %d éléments", pathLabel(e), len(pieces))
	}
	for i, p := range pieces {
		if p.arc {
			m.emitArc(e, p, mat)
			continue
		}
		x1, y1 := mat.Apply(p.p0[0], p.p0[1])
		x2, y2 := mat.Apply(p.p1[0], p.p1[1])
		withMarker := i == len(pieces)-1
		m.createLinePiece(e, p.category, x1, y1, x2, y2, withMarker, mat)
	}
}

// pathPiece is one drawable fragment of a path: either a connector between
// two points, or an axis-aligned quarter-circle mapped to the native ARC
// shape (whose default geometry is exactly a quarter arc, orientable to any
// quadrant with scale flips).
type pathPiece struct {
	category     string
	p0, p1       [2]float64
	arc          bool
	cx, cy       float64
	r            float64
	flipX, flipY bool
}

func pathLabel(e *svgpkg.Element) string {
	if c := e.Attr("class"); c != "" {
		return c
	}
	if e.Parent != nil {
		return e.Parent.Attr("class")
	}
	return ""
}

// quadPieces approximates one quadratic bezier. A shallow curve becomes a
// single CURVED connector; a deep one (control point far from the chord) is
// split at the true curve midpoint so the bow is preserved.
func quadPieces(p0, ctrl, p1 [2]float64) []pathPiece {
	mid := [2]float64{(p0[0] + 2*ctrl[0] + p1[0]) / 4, (p0[1] + 2*ctrl[1] + p1[1]) / 4}
	chordMid := [2]float64{(p0[0] + p1[0]) / 2, (p0[1] + p1[1]) / 2}
	dev := math.Hypot(mid[0]-chordMid[0], mid[1]-chordMid[1])
	chord := math.Hypot(p1[0]-p0[0], p1[1]-p0[1])
	if chord > 0 && dev > 0.2*chord {
		return []pathPiece{
			{category: "CURVED", p0: p0, p1: mid},
			{category: "CURVED", p0: mid, p1: p1},
		}
	}
	return []pathPiece{{category: "CURVED", p0: p0, p1: p1}}
}

// cubicPieces approximates one cubic bezier with the same strategy as
// quadPieces, splitting at the true curve midpoint when the bow is deep.
func cubicPieces(p0, c1, c2, p1 [2]float64) []pathPiece {
	mid := [2]float64{
		(p0[0] + 3*c1[0] + 3*c2[0] + p1[0]) / 8,
		(p0[1] + 3*c1[1] + 3*c2[1] + p1[1]) / 8,
	}
	chordMid := [2]float64{(p0[0] + p1[0]) / 2, (p0[1] + p1[1]) / 2}
	dev := math.Hypot(mid[0]-chordMid[0], mid[1]-chordMid[1])
	chord := math.Hypot(p1[0]-p0[0], p1[1]-p0[1])
	if chord > 0 && dev > 0.2*chord {
		return []pathPiece{
			{category: "CURVED", p0: p0, p1: mid},
			{category: "CURVED", p0: mid, p1: p1},
		}
	}
	return []pathPiece{{category: "CURVED", p0: p0, p1: p1}}
}

// emitArc creates a native ARC shape covering one quadrant of the circle.
func (m *Mapper) emitArc(e *svgpkg.Element, p pathPiece, mat svgpkg.Matrix) {
	cx, cy := mat.Apply(p.cx, p.cy)
	sx, sy := mat.ScaleFactors()
	rx, ry := p.r*sx, p.r*sy
	scaleX, scaleY := 1.0, 1.0
	tx, ty := m.toEMU(cx-rx, cy-ry)
	if p.flipX {
		scaleX = -1
		tx, _ = m.toEMU(cx+rx, 0)
	}
	if p.flipY {
		scaleY = -1
		_, ty = m.toEMU(0, cy+ry)
	}
	id := m.nextID()
	m.reqs = append(m.reqs, &slides.Request{CreateShape: &slides.CreateShapeRequest{
		ObjectId:  id,
		ShapeType: "ARC",
		ElementProperties: &slides.PageElementProperties{
			PageObjectId: m.cfg.SlideID,
			Size:         sizeEMU(m.lenEMU(2*rx), m.lenEMU(2*ry)),
			Transform: &slides.AffineTransform{
				ScaleX: scaleX, ScaleY: scaleY,
				TranslateX: tx, TranslateY: ty,
				Unit:            "EMU",
				ForceSendFields: []string{"ScaleX", "ScaleY", "TranslateX", "TranslateY"},
			},
		},
	}})
	m.styleShape(id, e, mat)
}

func (m *Mapper) createLine(e *svgpkg.Element, category string, x1, y1, x2, y2 float64, mat svgpkg.Matrix) {
	m.createLinePiece(e, category, x1, y1, x2, y2, true, mat)
}

func (m *Mapper) createLinePiece(e *svgpkg.Element, category string, x1, y1, x2, y2 float64, withMarker bool, mat svgpkg.Matrix) {
	ex1, ey1 := m.toEMU(x1, y1)
	ex2, ey2 := m.toEMU(x2, y2)
	w := math.Abs(ex2 - ex1)
	h := math.Abs(ey2 - ey1)
	scaleX, scaleY := 1.0, 1.0
	if ex2 < ex1 {
		scaleX = -1
	}
	if ey2 < ey1 {
		scaleY = -1
	}
	id := m.nextID()
	m.reqs = append(m.reqs, &slides.Request{CreateLine: &slides.CreateLineRequest{
		ObjectId: id,
		Category: category,
		ElementProperties: &slides.PageElementProperties{
			PageObjectId: m.cfg.SlideID,
			Size:         sizeEMU(math.Max(w, 1), math.Max(h, 1)),
			Transform: &slides.AffineTransform{
				ScaleX: scaleX, ScaleY: scaleY,
				TranslateX: ex1, TranslateY: ey1,
				Unit:            "EMU",
				ForceSendFields: []string{"ScaleX", "ScaleY", "TranslateX", "TranslateY"},
			},
		},
	}})

	props := &slides.LineProperties{}
	var fields []string
	if c, alpha, ok := m.strokePaint(e); ok {
		props.LineFill = &slides.LineFill{SolidFill: &slides.SolidFill{Color: c.opaque(), Alpha: alpha}}
		fields = append(fields, "lineFill.solidFill")
	}
	if sw := e.InheritedFloat("stroke-width", 1); sw > 0 {
		props.Weight = &slides.Dimension{Magnitude: m.lenEMU(sw * avgScale(mat)), Unit: "EMU"}
		fields = append(fields, "weight")
	}
	if dash := e.Inherited("stroke-dasharray"); dash != "" && dash != "none" {
		props.DashStyle = dashStyle(dash)
		fields = append(fields, "dashStyle")
	}
	if withMarker && e.Attr("marker-end") != "" {
		props.EndArrow = "FILL_ARROW"
		fields = append(fields, "endArrow")
	}
	if withMarker && e.Attr("marker-start") != "" {
		props.StartArrow = "FILL_ARROW"
		fields = append(fields, "startArrow")
	}
	if len(fields) == 0 {
		return
	}
	m.reqs = append(m.reqs, &slides.Request{UpdateLineProperties: &slides.UpdateLinePropertiesRequest{
		ObjectId:       id,
		LineProperties: props,
		Fields:         strings.Join(fields, ","),
	}})
}

// ---------------------------------------------------------------------------
// Common helpers

func sizeEMU(w, h float64) *slides.Size {
	return &slides.Size{
		Width:  &slides.Dimension{Magnitude: math.Max(w, 1), Unit: "EMU", ForceSendFields: []string{"Magnitude"}},
		Height: &slides.Dimension{Magnitude: math.Max(h, 1), Unit: "EMU", ForceSendFields: []string{"Magnitude"}},
	}
}

func (m *Mapper) createShape(id, shapeType string, x, y, w, h, rotDeg float64) {
	m.reqs = append(m.reqs, &slides.Request{CreateShape: &slides.CreateShapeRequest{
		ObjectId:  id,
		ShapeType: shapeType,
		ElementProperties: &slides.PageElementProperties{
			PageObjectId: m.cfg.SlideID,
			Size:         sizeEMU(w, h),
			Transform: &slides.AffineTransform{
				ScaleX: 1, ScaleY: 1,
				TranslateX: x, TranslateY: y,
				Unit:            "EMU",
				ForceSendFields: []string{"ScaleX", "ScaleY", "TranslateX", "TranslateY"},
			},
		},
	}})
}

// avgScale is the pragmatic single factor used for stroke widths under a
// possibly non-uniform scale (exact when the scale is uniform).
func avgScale(mat svgpkg.Matrix) float64 {
	sx, sy := mat.ScaleFactors()
	return (sx + sy) / 2
}
