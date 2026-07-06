// Package mapper converts a parsed SVG tree into native Google Slides
// batchUpdate requests (shapes, lines and text boxes).
package mapper

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

const (
	emuPerPt = 12700.0
	// Extra width added to text boxes so the estimated text width never
	// wraps; the surplus is symmetric because paragraphs are centered.
	textBoxSlackEMU = 250000.0
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
}

// New creates a Mapper.
func New(cfg Config, sheet *svgpkg.Stylesheet) *Mapper {
	if cfg.FontFamily == "" {
		cfg.FontFamily = "Arial"
	}
	return &Mapper{cfg: cfg, sheet: sheet}
}

// Map converts the SVG root into Slides requests targeting cfg.SlideID.
func (m *Mapper) Map(root *svgpkg.Element) ([]*slides.Request, []string) {
	for _, c := range root.Children {
		m.walk(c, svgpkg.Identity())
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
	w := e.FloatAttr("width", 0)
	h := e.FloatAttr("height", 0)
	if w <= 0 || h <= 0 {
		return
	}
	// The full-canvas background rectangle would only get in the way of
	// editing; the slide background is already white.
	vb := m.cfg.ViewBox
	if e.Attr("class") == "" && math.Abs(x-vb.X) < 1 && math.Abs(y-vb.Y) < 1 &&
		w >= vb.W*0.95 && h >= vb.H*0.95 && e.Attr("stroke") == "" {
		m.warnf("rectangle de fond pleine page ignoré")
		return
	}
	shapeType := "RECTANGLE"
	if e.FloatAttr("rx", 0) > 0 || e.FloatAttr("ry", 0) > 0 {
		shapeType = "ROUND_RECTANGLE"
	}
	id := m.nextID()
	ex, ey := m.toEMU(x, y)
	m.createShape(id, shapeType, ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
	m.styleShape(id, e)
}

func (m *Mapper) mapCircle(e *svgpkg.Element, mat svgpkg.Matrix) {
	cx, cy := mat.Apply(e.FloatAttr("cx", 0), e.FloatAttr("cy", 0))
	r := e.FloatAttr("r", 0)
	if r <= 0 {
		return
	}
	id := m.nextID()
	ex, ey := m.toEMU(cx-r, cy-r)
	m.createShape(id, "ELLIPSE", ex, ey, m.lenEMU(2*r), m.lenEMU(2*r), 0)
	m.styleShape(id, e)
}

func (m *Mapper) mapEllipse(e *svgpkg.Element, mat svgpkg.Matrix) {
	cx, cy := mat.Apply(e.FloatAttr("cx", 0), e.FloatAttr("cy", 0))
	rx := e.FloatAttr("rx", 0)
	ry := e.FloatAttr("ry", 0)
	if rx <= 0 || ry <= 0 {
		return
	}
	id := m.nextID()
	ex, ey := m.toEMU(cx-rx, cy-ry)
	m.createShape(id, "ELLIPSE", ex, ey, m.lenEMU(2*rx), m.lenEMU(2*ry), 0)
	m.styleShape(id, e)
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
	m.styleShape(id, faces[0])
	m.warnf("groupe %q approximé par une forme CUBE", e.Attr("class"))

	for _, c := range e.Children {
		if c.Tag == "polygon" || c.Tag == "line" {
			continue // absorbed by the cube
		}
		m.walk(c, mat)
	}
	return true
}

func (m *Mapper) mapPolygon(e *svgpkg.Element, mat svgpkg.Matrix) {
	pts := svgpkg.ParsePoints(e.Attr("points"))
	switch {
	case len(pts) == 3:
		m.mapTrianglePolygon(e, pts, mat)
	case len(pts) == 4 && isAxisAlignedRect(pts):
		minX, minY, w, h := bbox(pts)
		x, y := mat.Apply(minX, minY)
		id := m.nextID()
		ex, ey := m.toEMU(x, y)
		m.createShape(id, "RECTANGLE", ex, ey, m.lenEMU(w), m.lenEMU(h), 0)
		m.styleShape(id, e)
	default:
		m.warnf("polygone à %d points ignoré (classe %q)", len(pts), e.Attr("class"))
	}
}

// mapTrianglePolygon maps a 3-point polygon (arrowhead chevrons) onto the
// native TRIANGLE shape, rotated to match the pointing direction.
func (m *Mapper) mapTrianglePolygon(e *svgpkg.Element, pts [][2]float64, mat svgpkg.Matrix) {
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
	pointing := math.Atan2(apex[1]-cy, apex[0]-cx) // radians, local coords
	// The Slides TRIANGLE points up (-y): rotate by pointing - (-90°).
	extra := pointing + math.Pi/2
	rot := mat.Rotation()*math.Pi/180 + extra

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
	tcx, tcy := mat.Apply(cx, cy)
	ecx, ecy := m.toEMU(tcx, tcy)
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
	m.styleShape(id, e)
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
	m.createLine(e, "STRAIGHT", x1, y1, x2, y2)
}

func (m *Mapper) mapPath(e *svgpkg.Element, mat svgpkg.Matrix) {
	if e.Attr("fill") == "none" && (e.Attr("stroke") == "none" || e.Attr("stroke") == "") {
		return // invisible motion-path rail
	}
	segs, err := svgpkg.ParsePathD(e.Attr("d"))
	if err != nil {
		m.warnf("path %q ignoré: %v", e.Attr("class"), err)
		return
	}
	var closed bool
	hasCubic := false
	for _, s := range segs {
		switch s.Op {
		case 'Z':
			closed = true
		case 'C':
			hasCubic = true
		}
	}
	if closed || hasCubic {
		m.warnf("path %q (forme libre fermée ou cubique) ignoré", e.Attr("class"))
		return
	}

	// Walk segments, emitting one connector (or native ARC shape) per
	// L/Q/A command. The marker-end arrowhead goes on the last connector.
	var pieces []pathPiece
	var cur [2]float64
	for _, s := range segs {
		switch s.Op {
		case 'M':
			if len(s.Args) >= 2 {
				cur = [2]float64{s.Args[0], s.Args[1]}
			}
		case 'L':
			for i := 0; i+1 < len(s.Args); i += 2 {
				pieces = append(pieces, pathPiece{category: "STRAIGHT", p0: cur, p1: [2]float64{s.Args[i], s.Args[i+1]}})
				cur = [2]float64{s.Args[i], s.Args[i+1]}
			}
		case 'Q':
			for i := 0; i+3 < len(s.Args); i += 4 {
				ctrl := [2]float64{s.Args[i], s.Args[i+1]}
				end := [2]float64{s.Args[i+2], s.Args[i+3]}
				pieces = append(pieces, quadPieces(cur, ctrl, end)...)
				cur = end
			}
		case 'A':
			for i := 0; i+6 < len(s.Args); i += 7 {
				end := [2]float64{s.Args[i+5], s.Args[i+6]}
				if p, ok := quarterArcPiece(cur, end, s.Args[i], s.Args[i+1], s.Args[i+4] != 0); ok {
					pieces = append(pieces, p)
				} else {
					pieces = append(pieces, pathPiece{category: "CURVED", p0: cur, p1: end})
				}
				cur = end
			}
		case 'H':
			for _, x := range s.Args {
				pieces = append(pieces, pathPiece{category: "STRAIGHT", p0: cur, p1: [2]float64{x, cur[1]}})
				cur[0] = x
			}
		case 'V':
			for _, y := range s.Args {
				pieces = append(pieces, pathPiece{category: "STRAIGHT", p0: cur, p1: [2]float64{cur[0], y}})
				cur[1] = y
			}
		}
	}
	if len(pieces) == 0 {
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
		m.createLinePiece(e, p.category, x1, y1, x2, y2, withMarker)
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

// quarterArcPiece recognizes a circular 90° arc whose endpoints are axis
// aligned with the circle center, and returns it as an ARC-shape piece.
func quarterArcPiece(p0, p1 [2]float64, rx, ry float64, sweep bool) (pathPiece, bool) {
	if rx <= 0 || math.Abs(rx-ry) > 0.01*rx {
		return pathPiece{}, false
	}
	r := rx
	chord := math.Hypot(p1[0]-p0[0], p1[1]-p0[1])
	if chord <= 0 || chord > 2*r {
		return pathPiece{}, false
	}
	h := math.Sqrt(math.Max(r*r-(chord/2)*(chord/2), 0))
	ux, uy := (p1[0]-p0[0])/chord, (p1[1]-p0[1])/chord
	// The (minor-arc) center sits perpendicular to the chord: on the
	// +90°-rotated side for sweep=1, the -90° side for sweep=0.
	px, py := uy, -ux
	if sweep {
		px, py = -uy, ux
	}
	cx := (p0[0]+p1[0])/2 + px*h
	cy := (p0[1]+p1[1])/2 + py*h

	axis := func(dx, dy float64) int {
		switch {
		case math.Abs(dy) < r*0.05 && math.Abs(dx) > r*0.95:
			if dx > 0 {
				return 0 // +x
			}
			return 2 // -x
		case math.Abs(dx) < r*0.05 && math.Abs(dy) > r*0.95:
			if dy > 0 {
				return 1 // +y
			}
			return 3 // -y
		}
		return -1
	}
	a0 := axis(p0[0]-cx, p0[1]-cy)
	a1 := axis(p1[0]-cx, p1[1]-cy)
	if a0 < 0 || a1 < 0 || (a0+a1)%2 == 0 {
		return pathPiece{}, false
	}
	return pathPiece{
		arc: true,
		cx:  cx, cy: cy, r: r,
		flipX: (p0[0]-cx)+(p1[0]-cx) < 0,
		flipY: (p0[1]-cy)+(p1[1]-cy) > 0,
	}, true
}

// emitArc creates a native ARC shape covering one quadrant of the circle.
func (m *Mapper) emitArc(e *svgpkg.Element, p pathPiece, mat svgpkg.Matrix) {
	cx, cy := mat.Apply(p.cx, p.cy)
	w := m.lenEMU(2 * p.r)
	scaleX, scaleY := 1.0, 1.0
	tx, ty := m.toEMU(cx-p.r, cy-p.r)
	if p.flipX {
		scaleX = -1
		tx, _ = m.toEMU(cx+p.r, 0)
	}
	if p.flipY {
		scaleY = -1
		_, ty = m.toEMU(0, cy+p.r)
	}
	id := m.nextID()
	m.reqs = append(m.reqs, &slides.Request{CreateShape: &slides.CreateShapeRequest{
		ObjectId:  id,
		ShapeType: "ARC",
		ElementProperties: &slides.PageElementProperties{
			PageObjectId: m.cfg.SlideID,
			Size:         sizeEMU(w, w),
			Transform: &slides.AffineTransform{
				ScaleX: scaleX, ScaleY: scaleY,
				TranslateX: tx, TranslateY: ty,
				Unit:            "EMU",
				ForceSendFields: []string{"ScaleX", "ScaleY", "TranslateX", "TranslateY"},
			},
		},
	}})
	m.styleShape(id, e)
}

func (m *Mapper) createLine(e *svgpkg.Element, category string, x1, y1, x2, y2 float64) {
	m.createLinePiece(e, category, x1, y1, x2, y2, true)
}

func (m *Mapper) createLinePiece(e *svgpkg.Element, category string, x1, y1, x2, y2 float64, withMarker bool) {
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
	stroke := e.Attr("stroke")
	if c, ok := parseColor(stroke); ok {
		props.LineFill = &slides.LineFill{SolidFill: &slides.SolidFill{Color: c, Alpha: strokeAlpha(e)}}
		fields = append(fields, "lineFill.solidFill")
	}
	if sw := e.FloatAttr("stroke-width", 1); sw > 0 {
		props.Weight = &slides.Dimension{Magnitude: m.lenEMU(sw), Unit: "EMU"}
		fields = append(fields, "weight")
	}
	if e.Attr("stroke-dasharray") != "" {
		props.DashStyle = "DASH"
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
// Text

func (m *Mapper) mapText(e *svgpkg.Element, mat svgpkg.Matrix) {
	content := e.TextContent()
	if content == "" {
		return
	}
	x, y := mat.Apply(e.FloatAttr("x", 0), e.FloatAttr("y", 0))
	fontSize := e.FloatAttr("font-size", 10)
	anchor := e.Attr("text-anchor")
	bold := e.Attr("font-weight") == "bold"
	italic := e.Attr("font-style") == "italic"

	// Rough width estimate to size the box; centered paragraphs make the
	// slack symmetric so the estimate only needs to avoid wrapping.
	est := estimateTextWidth(content, fontSize)
	var centerX float64
	switch anchor {
	case "middle":
		centerX = x
	case "end":
		centerX = x - est/2
	default:
		centerX = x + est/2
	}
	// The SVG y is the baseline; the visual center of a line of text sits
	// roughly 0.35em above it.
	centerY := y - 0.35*fontSize

	wEMU := m.lenEMU(est) + textBoxSlackEMU
	hEMU := m.lenEMU(fontSize*1.9) + 100000
	ecx, ecy := m.toEMU(centerX, centerY)

	id := m.nextID()
	m.createShape(id, "TEXT_BOX", ecx-wEMU/2, ecy-hEMU/2, wEMU, hEMU, 0)
	m.reqs = append(m.reqs, &slides.Request{InsertText: &slides.InsertTextRequest{
		ObjectId: id,
		Text:     content,
	}})

	style := &slides.TextStyle{
		FontFamily:      m.cfg.FontFamily,
		FontSize:        &slides.Dimension{Magnitude: ptSize(fontSize, m.cfg.Scale), Unit: "PT"},
		Bold:            bold,
		Italic:          italic,
		ForceSendFields: []string{"Bold", "Italic"},
	}
	fields := []string{"fontFamily", "fontSize", "bold", "italic"}
	if c, ok := parseColor(e.Attr("fill")); ok {
		style.ForegroundColor = &slides.OptionalColor{OpaqueColor: c}
		fields = append(fields, "foregroundColor")
	}
	m.reqs = append(m.reqs, &slides.Request{UpdateTextStyle: &slides.UpdateTextStyleRequest{
		ObjectId:  id,
		Style:     style,
		TextRange: &slides.Range{Type: "ALL"},
		Fields:    strings.Join(fields, ","),
	}})
	m.reqs = append(m.reqs, &slides.Request{UpdateParagraphStyle: &slides.UpdateParagraphStyleRequest{
		ObjectId:  id,
		Style:     &slides.ParagraphStyle{Alignment: "CENTER"},
		TextRange: &slides.Range{Type: "ALL"},
		Fields:    "alignment",
	}})
	m.reqs = append(m.reqs, &slides.Request{UpdateShapeProperties: &slides.UpdateShapePropertiesRequest{
		ObjectId:        id,
		ShapeProperties: &slides.ShapeProperties{ContentAlignment: "MIDDLE"},
		Fields:          "contentAlignment",
	}})
}

// ptSize converts an SVG font size (user units) to Slides points, given the
// page scale in EMU per user unit.
func ptSize(svgSize, scale float64) float64 {
	pt := svgSize * scale / emuPerPt
	if pt < 1 {
		pt = 1
	}
	return math.Round(pt*10) / 10
}

func estimateTextWidth(s string, fontSize float64) float64 {
	n := 0.0
	for _, r := range s {
		switch {
		case r > 0x2000: // emoji and symbols are roughly square
			n += 1.1
		case r >= 'A' && r <= 'Z':
			n += 0.66
		default:
			n += 0.52
		}
	}
	return n * fontSize
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

// styleShape applies fill and outline from the SVG presentation attributes.
func (m *Mapper) styleShape(id string, e *svgpkg.Element) {
	props := &slides.ShapeProperties{}
	var fields []string

	fill := e.Attr("fill")
	if fill == "none" {
		props.ShapeBackgroundFill = &slides.ShapeBackgroundFill{PropertyState: "NOT_RENDERED"}
		fields = append(fields, "shapeBackgroundFill.propertyState")
	} else if c, ok := parseColor(fill); ok {
		alpha := e.FloatAttr("fill-opacity", 1)
		props.ShapeBackgroundFill = &slides.ShapeBackgroundFill{
			SolidFill: &slides.SolidFill{Color: c, Alpha: alpha},
		}
		fields = append(fields, "shapeBackgroundFill.solidFill")
	}

	stroke := e.Attr("stroke")
	if stroke == "" || stroke == "none" {
		props.Outline = &slides.Outline{PropertyState: "NOT_RENDERED"}
		fields = append(fields, "outline.propertyState")
	} else if c, ok := parseColor(stroke); ok {
		props.Outline = &slides.Outline{
			OutlineFill: &slides.OutlineFill{SolidFill: &slides.SolidFill{Color: c, Alpha: strokeAlpha(e)}},
			Weight:      &slides.Dimension{Magnitude: m.lenEMU(e.FloatAttr("stroke-width", 1)), Unit: "EMU"},
		}
		fields = append(fields, "outline.outlineFill.solidFill", "outline.weight")
		if e.Attr("stroke-dasharray") != "" {
			props.Outline.DashStyle = "DASH"
			fields = append(fields, "outline.dashStyle")
		}
	}

	if len(fields) == 0 {
		return
	}
	m.reqs = append(m.reqs, &slides.Request{UpdateShapeProperties: &slides.UpdateShapePropertiesRequest{
		ObjectId:        id,
		ShapeProperties: props,
		Fields:          strings.Join(fields, ","),
	}})
}

func strokeAlpha(e *svgpkg.Element) float64 {
	return e.FloatAttr("stroke-opacity", 1)
}

var namedColors = map[string]string{
	"white": "#ffffff",
	"black": "#000000",
	"red":   "#ff0000",
	"green": "#008000",
	"blue":  "#0000ff",
}

func parseColor(s string) (*slides.OpaqueColor, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "none" || strings.HasPrefix(s, "url(") {
		return nil, false
	}
	if hex, ok := namedColors[s]; ok {
		s = hex
	}
	if !strings.HasPrefix(s, "#") {
		return nil, false
	}
	hex := s[1:]
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	if len(hex) != 6 {
		return nil, false
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return nil, false
	}
	return &slides.OpaqueColor{RgbColor: &slides.RgbColor{
		Red:             float64(v>>16&0xff) / 255,
		Green:           float64(v>>8&0xff) / 255,
		Blue:            float64(v&0xff) / 255,
		ForceSendFields: []string{"Red", "Green", "Blue"},
	}}, true
}
