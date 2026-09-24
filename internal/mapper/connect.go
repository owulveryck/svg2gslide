package mapper

import (
	"math"
	"strings"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// Editing-oriented structure, resolved once the whole drawing is mapped:
//
//   - text in shapes: a text block whose topmost underlying object is a
//     shape containing it (and which hosts no other block) is written into
//     that shape instead of a separate TEXT_BOX, so it moves with it;
//   - connections: connector ends that already sit on a connection site of
//     a shape are attached to it (the drawing is unchanged); with
//     Config.ConnectCurves, edges between two shapes (PlantUML links, open
//     curved paths) become a single connector attached at both ends;
//   - a connected shape hosting several text blocks is grouped with them
//     and the small objects drawn on it, so the box moves as a whole.
//
// Text blocks and edges are emitted as nil placeholders in m.reqs and
// expanded by finalize, which keeps the z-order.

// textGeom is the page geometry (EMU) of a text block.
type textGeom struct {
	rotated                  bool
	ax, base0                float64 // anchor x (alignment edge/centre), first baseline
	align                    string  // START, CENTER, END
	f, p                     float64 // first line size (EMU), line spacing
	fl, total                float64 // last line size, first→last baseline (EMU)
	lineX0, lineW            []float64
	left, right, top, bottom float64
}

// hostPlace positions a text block inside a host shape.
// The text is vertically centred (MIDDLE) in the shape: Slides centres
// the block even when it overflows, which makes the fixed 0.1" vertical
// inset irrelevant; spaceAbove on the first paragraph (or spaceBelow on
// the last one) moves the baselines onto the SVG ones.
type hostPlace struct {
	id                                 string
	align                              string  // paragraph alignment
	indentStart, indentEnd, spaceAbove float64 // EMU
	spaceBelow                         float64 // EMU, last paragraph
}

type pendingText struct {
	slot int
	el   *svgpkg.Element
	geom textGeom
	emit func(*hostPlace) string
	id   string // resulting object (host or text box)
}

// pendingEdge is an open path that may become one attached connector.
type pendingEdge struct {
	slot       int
	el         *svgpkg.Element // stroke style source
	fallback   []*slides.Request
	p0, p1     pt // endpoints, page EMU
	straight   bool
	startArrow bool
	endArrow   bool
	ent0, ent1 *svgpkg.Element // explicit end shapes (PlantUML), or nil
}

// objRec is an object of the page, rebuilt from the requests.
type objRec struct {
	id, typ    string
	x, y, w, h float64 // axis-aligned bounding box, EMU
	seq        int     // position in m.reqs
	axis       bool    // unrotated, unflipped
	line       bool
}

func (o *objRec) area() float64 { return o.w * o.h }

func (o *objRec) contains(l, t, r, b, tol float64) bool {
	return l >= o.x-tol && r <= o.x+o.w+tol && t >= o.y-tol && b <= o.y+o.h+tol
}

func (o *objRec) overlaps(l, t, r, b float64) bool {
	return l < o.x+o.w && r > o.x && t < o.y+o.h && b > o.y
}

// textRect returns the insets (EMU) of the text area of a shape type, as
// measured in Slides (ECMA-376 preset text rectangles), or ok=false when
// the shape can't host text faithfully.
func textRect(typ string, w, h float64) (l, t, r, b float64, ok bool) {
	switch typ {
	case "RECTANGLE":
		return 0, 0, 0, 0, true
	case "ROUND_RECTANGLE":
		d := 0.29289 * math.Min(w, h) / 6
		return d, d, d, d, true
	case "ELLIPSE":
		return 0.14645 * w, 0.14645 * h, 0.14645 * w, 0.14645 * h, true
	case "FLOW_CHART_TERMINATOR":
		return w * 1018 / 21600, h * 3163 / 21600, w * 1018 / 21600, h * 3163 / 21600, true
	}
	return 0, 0, 0, 0, false
}

// connectionSites returns the connection sites (EMU) of a shape, indexed
// as in Slides, or nil when unknown.
func connectionSites(typ string, x, y, w, h float64) []pt {
	cx, cy := x+w/2, y+h/2
	switch typ {
	case "RECTANGLE", "ROUND_RECTANGLE", "FLOW_CHART_TERMINATOR", "FLOW_CHART_PROCESS":
		return []pt{{cx, y}, {x, cy}, {cx, y + h}, {x + w, cy}}
	case "ELLIPSE":
		var out []pt
		for i := 0; i < 8; i++ {
			a := math.Pi/2 + float64(i)*math.Pi/4 // from the top, counterclockwise
			out = append(out, pt{cx + w/2*math.Cos(a), cy - h/2*math.Sin(a)})
		}
		return out
	case "CAN":
		return []pt{{cx, y + math.Min(w, h)/4}, {cx, y}, {x, cy}, {cx, y + h}, {x + w, cy}}
	}
	return nil
}

func (m *Mapper) deferText(e *svgpkg.Element, g textGeom, emit func(*hostPlace) string) {
	m.pendingTexts = append(m.pendingTexts, &pendingText{slot: len(m.reqs), el: e, geom: g, emit: emit})
	m.reqs = append(m.reqs, nil)
}

// captureReqs runs fn and returns the requests it appended (removed from
// m.reqs).
func (m *Mapper) captureReqs(fn func()) []*slides.Request {
	start := len(m.reqs)
	fn()
	out := append([]*slides.Request(nil), m.reqs[start:]...)
	m.reqs = m.reqs[:start]
	return out
}

// buildRegistry lists the shapes and lines created so far.
func (m *Mapper) buildRegistry() []*objRec { return registryOf(m.reqs, false) }

// registryOf lists the objects created by reqs (text boxes included when
// withText is set), seq being the request position.
func registryOf(reqs []*slides.Request, withText bool) []*objRec {
	var out []*objRec
	for i, r := range reqs {
		var id, typ string
		var props *slides.PageElementProperties
		line := false
		switch {
		case r == nil:
			continue
		case r.CreateShape != nil:
			id, typ, props = r.CreateShape.ObjectId, r.CreateShape.ShapeType, r.CreateShape.ElementProperties
		case r.CreateLine != nil:
			id, typ, props, line = r.CreateLine.ObjectId, "LINE", r.CreateLine.ElementProperties, true
		default:
			continue
		}
		if (typ == "TEXT_BOX" && !withText) || props == nil || props.Size == nil || props.Transform == nil {
			continue
		}
		t := props.Transform
		w, h := props.Size.Width.Magnitude, props.Size.Height.Magnitude
		var xs, ys []float64
		for _, c := range []pt{{0, 0}, {w, 0}, {0, h}, {w, h}} {
			xs = append(xs, t.ScaleX*c[0]+t.ShearX*c[1]+t.TranslateX)
			ys = append(ys, t.ShearY*c[0]+t.ScaleY*c[1]+t.TranslateY)
		}
		o := &objRec{id: id, typ: typ, seq: i, line: line,
			axis: t.ShearX == 0 && t.ShearY == 0 && t.ScaleX > 0 && t.ScaleY > 0}
		o.x, o.w = minMax(xs)
		o.y, o.h = minMax(ys)
		out = append(out, o)
	}
	return out
}

func minMax(v []float64) (lo, span float64) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, x := range v {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	return lo, hi - lo
}

// fitInHost computes how a block sits in a host shape, or nil when it
// wouldn't render at the same place without wrapping.
func fitInHost(o *objRec, g *textGeom) *hostPlace {
	il, it, ir, ib, ok := textRect(o.typ, o.w, o.h)
	if !ok || !o.axis || g.rotated {
		return nil
	}
	L := o.x + il + textInsetEMU
	R := o.x + o.w - ir - textInsetEMU
	T := o.y + it + textInsetEMU
	A := o.h - it - ib - 2*textInsetEMU // may be negative: centred overflow
	tol := 0.5 * emuPerPt
	hp := &hostPlace{id: o.id, align: g.align}

	// Vertical: block height Hb = spaceAbove + first ascent + pitches +
	// last descent; its top is centred: T + (A − Hb)/2.
	off := firstBaselineOffset(g.f, g.p)
	d := slidesMiddleDescent * g.fl
	if g.p >= 1 {
		d += (g.p - 1) * slidesLineHeight * g.fl
	} else {
		d -= (1 - slidesReduceAbove) * slidesLineHeight * (1 - g.p) * g.fl
	}
	sa := 2*(g.base0-T-A/2) - off + g.total + d
	if sa >= 0 {
		hp.spaceAbove = sa
	} else {
		hp.spaceBelow = -sa
	}

	// Horizontal. A single start-anchored line centred in the shape is
	// centred in Slides too (the SVG padding is often narrower than the
	// fixed inset).
	align := g.align
	ax := g.ax
	if align == "START" && len(g.lineX0) == 1 {
		c := g.lineX0[0] + g.lineW[0]/2
		if math.Abs(c-(o.x+o.w/2)) < 0.3*g.f {
			align, ax, hp.align = "CENTER", c, "CENTER"
		}
	}
	slack := func(w float64) float64 { return w*1.04 + 0.1*g.f }
	switch align {
	case "START":
		hp.indentStart = ax - (L - textOriginShiftEMU)
		if hp.indentStart < -tol {
			return nil
		}
		hp.indentStart = math.Max(0, hp.indentStart)
		for i := range g.lineX0 {
			if g.lineX0[i]+slack(g.lineW[i]) > R {
				return nil
			}
		}
	case "END":
		hp.indentEnd = R - textOriginShiftEMU - ax
		if hp.indentEnd < -tol {
			return nil
		}
		hp.indentEnd = math.Max(0, hp.indentEnd)
		for i := range g.lineX0 {
			if ax-slack(g.lineW[i]) < L {
				return nil
			}
		}
	default: // CENTER
		dx := 2*ax - L - R
		hp.indentStart, hp.indentEnd = math.Max(0, dx), math.Max(0, -dx)
		avail := R - L - math.Abs(dx)
		for i := range g.lineW {
			if slack(g.lineW[i]) > avail {
				return nil
			}
		}
	}
	return hp
}

// finalize resolves the deferred texts and edges, then appends the
// connection and grouping requests.
func (m *Mapper) finalize() {
	reg := m.buildRegistry()
	byID := map[string]*objRec{}
	for _, o := range reg {
		byID[o.id] = o
	}

	// Text hosts: the topmost object below the block must contain it.
	hostOf := map[*pendingText]*objRec{}
	hostTexts := map[string][]*pendingText{}
	for _, t := range m.pendingTexts {
		g := &t.geom
		// Shrink the estimated extent a little: estimated widths and
		// descents overshoot, and touching a neighbour doesn't count.
		sh := 0.15 * g.f
		l, r, top, b := g.left+sh, g.right-sh, g.top+sh, g.bottom-sh
		var top1 *objRec
		for _, o := range reg {
			if o.seq > t.slot {
				break
			}
			if o.overlaps(l, top, r, b) {
				top1 = o
			}
		}
		if top1 == nil || top1.line || !top1.contains(g.left, g.top, g.right, g.bottom, 0.3*g.f) {
			continue
		}
		hostOf[t] = top1
		hostTexts[top1.id] = append(hostTexts[top1.id], t)
	}

	// Connections.
	var connReqs []*slides.Request
	connected := map[string]bool{}
	attach := func(lineID string, start bool, shape string, site int) {
		c := &slides.LineConnection{ConnectedObjectId: shape, ConnectionSiteIndex: int64(site), ForceSendFields: []string{"ConnectionSiteIndex"}}
		lp := &slides.LineProperties{}
		f := "endConnection"
		if start {
			lp.StartConnection, f = c, "startConnection"
		} else {
			lp.EndConnection = c
		}
		connReqs = append(connReqs, &slides.Request{UpdateLineProperties: &slides.UpdateLinePropertiesRequest{
			ObjectId: lineID, LineProperties: lp, Fields: f}})
		connected[shape] = true
	}

	edgeReqs := map[int][]*slides.Request{}
	for _, pe := range m.pendingEdges {
		edgeReqs[pe.slot] = m.resolveEdge(pe, reg, attach)
	}

	// Default attachment: ends already on a connection site.
	u := m.cfg.Scale
	for _, al := range m.attachable {
		dir := sub(al.p1, al.p0)
		n := norm2(dir)
		if n == 0 {
			continue
		}
		dir = pt{dir[0] / n, dir[1] / n}
		for end, p := range []pt{al.p0, al.p1} {
			out := dir // direction pointing beyond the end
			if end == 0 {
				out = pt{-dir[0], -dir[1]}
			}
			var best *objRec
			bestSite := -1
			for _, o := range reg {
				if o.line || !o.axis {
					continue
				}
				for si, s := range connectionSites(o.typ, o.x, o.y, o.w, o.h) {
					d := sub(s, p)
					along := d[0]*out[0] + d[1]*out[1]
					perp := math.Abs(d[0]*out[1] - d[1]*out[0])
					if perp <= 2*u && along >= -2*u && along <= 8*u && (best == nil || o.area() < best.area()) {
						best, bestSite = o, si
					}
				}
			}
			if best != nil {
				attach(al.id, end == 0, best.id, bestSite)
			}
		}
	}

	// Expand placeholders.
	textAt := map[int]*pendingText{}
	for _, t := range m.pendingTexts {
		textAt[t.slot] = t
	}
	var out []*slides.Request
	var groupReqs []*slides.Request
	multi := map[string]bool{} // hosts left with several separate blocks
	for i, r := range m.reqs {
		if r != nil {
			out = append(out, r)
			continue
		}
		if reqs, ok := edgeReqs[i]; ok {
			out = append(out, reqs...)
			continue
		}
		t := textAt[i]
		if t == nil {
			continue
		}
		var hp *hostPlace
		if o := hostOf[t]; o != nil && len(hostTexts[o.id]) == 1 {
			hp = fitInHost(o, &t.geom)
		}
		if o := hostOf[t]; o != nil && hp == nil {
			multi[o.id] = true
		}
		t.id = m.captureAppend(&out, func() string { return t.emit(hp) })
	}

	// Boxes whose text couldn't go inside (Slides' fixed 0.1" inset is
	// often larger than the SVG padding) are grouped with their texts and
	// the small objects drawn on them, so the box moves as a whole.
	groupReqs = m.groupBoxes(reg, multi, hostTexts, out)
	// Attach only lines that exist (fallback drawings replaced by a
	// connector are dropped).
	created := map[string]bool{}
	for _, o := range registryOf(out, false) {
		created[o.id] = true
	}
	for _, r := range connReqs {
		if created[r.UpdateLineProperties.ObjectId] {
			out = append(out, r)
		}
	}
	out = append(out, groupReqs...)
	m.reqs = out
}

// captureAppend runs fn with m.reqs temporarily empty and appends what it
// produced to out.
func (m *Mapper) captureAppend(out *[]*slides.Request, fn func() string) string {
	saved := m.reqs
	m.reqs = nil
	id := fn()
	*out = append(*out, m.reqs...)
	m.reqs = saved
	return id
}

// groupedIDs returns the objects already in a group.
func groupedIDs(reqs []*slides.Request) map[string]bool {
	g := map[string]bool{}
	for _, r := range reqs {
		if r != nil && r.GroupObjects != nil {
			for _, c := range r.GroupObjects.ChildrenObjectIds {
				g[c] = true
			}
		}
	}
	return g
}

// resolveEdge turns an edge between two shapes into one attached
// connector, or returns its fallback drawing.
func (m *Mapper) resolveEdge(pe *pendingEdge, reg []*objRec, attach func(string, bool, string, int)) []*slides.Request {
	find := func(ent *svgpkg.Element, p pt) *objRec {
		if ent != nil {
			// The entity group's first mapped shape (its box).
			var id string
			ent.Walk(func(c *svgpkg.Element) {
				if id == "" {
					id = m.elemShape[c]
				}
			})
			for _, o := range reg {
				if id != "" && o.id == id && o.axis && connectionSites(o.typ, o.x, o.y, o.w, o.h) != nil {
					return o
				}
			}
			return nil
		}
		// Nearest boundary within ~7 units, smallest shape first.
		tol := 7 * m.cfg.Scale
		var best *objRec
		for _, o := range reg {
			if o.line || !o.axis || connectionSites(o.typ, o.x, o.y, o.w, o.h) == nil {
				continue
			}
			if p[0] < o.x-tol || p[0] > o.x+o.w+tol || p[1] < o.y-tol || p[1] > o.y+o.h+tol {
				continue
			}
			inX, inY := p[0] > o.x+tol && p[0] < o.x+o.w-tol, p[1] > o.y+tol && p[1] < o.y+o.h-tol
			if inX && inY {
				continue // deep inside: not an end on its outline
			}
			if best == nil || o.area() < best.area() {
				best = o
			}
		}
		return best
	}
	a, b := find(pe.ent0, pe.p0), find(pe.ent1, pe.p1)
	if a == nil || b == nil || a == b {
		return pe.fallback
	}
	nearest := func(o *objRec, p pt) (int, pt) {
		best, bi := math.Inf(1), 0
		sites := connectionSites(o.typ, o.x, o.y, o.w, o.h)
		for i, s := range sites {
			if o.typ == "CAN" && i == 0 {
				continue // lid centre, inside the shape
			}
			if d := norm2(sub(s, p)); d < best {
				best, bi = d, i
			}
		}
		return bi, sites[bi]
	}
	si, sp := nearest(a, pe.p0)
	ei, ep := nearest(b, pe.p1)
	cat := "CURVED"
	if pe.straight {
		cat = "STRAIGHT"
	}
	var id string
	reqs := m.captureReqs(func() {
		x1, y1 := m.fromEMU(sp)
		x2, y2 := m.fromEMU(ep)
		id = m.createLinePieceMarkers(pe.el, cat, x1, y1, x2, y2, false, false, svgpkg.Identity())
		var f []string
		lp := &slides.LineProperties{}
		if pe.startArrow {
			lp.StartArrow, f = "FILL_ARROW", append(f, "startArrow")
		}
		if pe.endArrow {
			lp.EndArrow, f = "FILL_ARROW", append(f, "endArrow")
		}
		if len(f) > 0 {
			m.reqs = append(m.reqs, &slides.Request{UpdateLineProperties: &slides.UpdateLinePropertiesRequest{
				ObjectId: id, LineProperties: lp, Fields: strings.Join(f, ",")}})
		}
	})
	attach(id, true, a.id, si)
	attach(id, false, b.id, ei)
	return reqs
}

// fromEMU converts page EMU back to SVG user coordinates.
func (m *Mapper) fromEMU(p pt) (float64, float64) {
	return (p[0]-m.cfg.OffX)/m.cfg.Scale + m.cfg.ViewBox.X, (p[1]-m.cfg.OffY)/m.cfg.Scale + m.cfg.ViewBox.Y
}

// mapLinkGroup handles a PlantUML link group (data-entity-1/2): its path
// and arrowhead polygon become one pending edge between the two entities;
// the labels are mapped normally.
func (m *Mapper) mapLinkGroup(e *svgpkg.Element, mat svgpkg.Matrix) {
	var path *svgpkg.Element
	var polys []*svgpkg.Element
	for _, c := range e.Children {
		switch c.Tag {
		case "path":
			if path == nil {
				path = c
			}
		case "polygon":
			polys = append(polys, c)
		}
	}
	ent0, ent1 := m.byID[e.Attr("data-entity-1")], m.byID[e.Attr("data-entity-2")]
	pts := pathEndpoints(path)
	if path == nil || ent0 == nil || ent1 == nil || len(pts) < 2 {
		m.inLink = true
		for _, c := range e.Children {
			m.walk(c, mat)
		}
		m.inLink = false
		return
	}
	m.inLink = true
	fb := m.captureReqs(func() {
		for _, c := range e.Children {
			if c.Tag != "text" {
				m.walk(c, mat)
			}
		}
	})
	m.inLink = false

	p0, p1 := matApply(mat, pts[0]), matApply(mat, pts[len(pts)-1])
	pe := &pendingEdge{slot: len(m.reqs), el: path, fallback: fb, ent0: ent0, ent1: ent1, straight: pathIsStraight(path)}
	ex0, ey0 := m.toEMU(p0[0], p0[1])
	ex1, ey1 := m.toEMU(p1[0], p1[1])
	pe.p0, pe.p1 = pt{ex0, ey0}, pt{ex1, ey1}
	// Arrowheads are separate polygons: put the arrow on the nearest end.
	for _, pg := range polys {
		c := centroid(applyAll(mat, svgpkg.ParsePoints(pg.Attr("points"))))
		if norm2(sub(c, p0)) < norm2(sub(c, p1)) {
			pe.startArrow = true
		} else {
			pe.endArrow = true
		}
	}
	m.pendingEdges = append(m.pendingEdges, pe)
	m.reqs = append(m.reqs, nil)
	for _, c := range e.Children {
		if c.Tag == "text" {
			m.walk(c, mat)
		}
	}
}

func centroid(ps [][2]float64) pt {
	var c pt
	for _, p := range ps {
		c[0] += p[0] / float64(len(ps))
		c[1] += p[1] / float64(len(ps))
	}
	return c
}

// pathIsStraight reports whether every point of the path (control points
// included) lies within one unit of the chord.
func pathIsStraight(e *svgpkg.Element) bool {
	segs, err := svgpkg.ParsePathD(e.Attr("d"))
	if err != nil {
		return false
	}
	var ps []pt
	for _, s := range segs {
		switch s.Op {
		case 'M', 'L', 'C', 'Q', 'S', 'T':
			for i := 0; i+1 < len(s.Args); i += 2 {
				ps = append(ps, pt{s.Args[i], s.Args[i+1]})
			}
		case 'A', 'H', 'V':
			return false
		}
	}
	if len(ps) < 2 {
		return false
	}
	for _, p := range ps {
		if distToLine(p, ps[0], ps[len(ps)-1]) > 1 {
			return false
		}
	}
	return true
}

// groupBoxes groups each host shape left with separate text boxes with
// those texts and the small objects on it (badges, icons, with their own
// texts), smallest boxes first; an object joins at most one group.
func (m *Mapper) groupBoxes(reg []*objRec, multi map[string]bool, hostTexts map[string][]*pendingText, out []*slides.Request) []*slides.Request {
	claimed := groupedIDs(out)
	// Existing groups (pills, icons, polylines) are members as a whole.
	children := map[string][]string{}
	for _, r := range out {
		if r != nil && r.GroupObjects != nil {
			children[r.GroupObjects.GroupObjectId] = r.GroupObjects.ChildrenObjectIds
		}
	}
	byID := map[string]*objRec{}
	for _, o := range reg {
		byID[o.id] = o
	}
	final := registryOf(out, true)
	var hosts []*objRec
	for id := range multi {
		hosts = append(hosts, byID[id])
	}
	for i := 1; i < len(hosts); i++ { // by area, then drawing order
		for j := i; j > 0 && (hosts[j].area() < hosts[j-1].area() ||
			hosts[j].area() == hosts[j-1].area() && hosts[j].seq < hosts[j-1].seq); j-- {
			hosts[j], hosts[j-1] = hosts[j-1], hosts[j]
		}
	}
	var reqs []*slides.Request
	for _, h := range hosts {
		if claimed[h.id] {
			continue
		}
		// A frame with just a title is a container, not a box: grouping
		// it would capture the boxes drawn on it.
		covered := 0.0
		for _, t := range hostTexts[h.id] {
			covered += (t.geom.right - t.geom.left) * (t.geom.bottom - t.geom.top)
		}
		if covered < 0.1*h.area() {
			continue
		}
		members := []string{h.id}
		taken := map[string]bool{}
		add := func(id string) {
			if !claimed[id] && !taken[id] && id != h.id {
				taken[id] = true
				members = append(members, id)
			}
		}
		small := func(o *objRec) bool {
			return o.seq > h.seq && !o.line && o.area() < 0.25*h.area() && h.contains(o.x, o.y, o.x+o.w, o.y+o.h, 0)
		}
		for _, t := range hostTexts[h.id] {
			add(t.id)
		}
		for _, o := range reg {
			if small(o) && !claimed[o.id] && !taken[o.id] {
				add(o.id)
				for _, t := range hostTexts[o.id] {
					add(t.id)
				}
			}
		}
		for gid, kids := range children {
			all := !claimed[gid] && !taken[gid]
			for _, k := range kids {
				o := byID[k]
				all = all && o != nil && small(o)
			}
			if all {
				add(gid)
			}
		}
		if len(members) > 1 && !zInterleaved(members, children, final, h) {
			claimed[h.id] = true
			for id := range taken {
				claimed[id] = true
			}
			reqs = append(reqs, &slides.Request{GroupObjects: &slides.GroupObjectsRequest{
				GroupObjectId: m.nextID(), ChildrenObjectIds: members}})
		}
	}
	return reqs
}

// zInterleaved reports whether an object outside the group, drawn between
// its members, overlaps the host: grouping would move it below them.
func zInterleaved(members []string, children map[string][]string, final []*objRec, h *objRec) bool {
	in := map[string]bool{}
	for _, id := range members {
		in[id] = true
		for _, k := range children[id] {
			in[k] = true
		}
	}
	lo, hi := math.MaxInt, -1
	for _, o := range final {
		if in[o.id] {
			lo, hi = min(lo, o.seq), max(hi, o.seq)
		}
	}
	for _, o := range final {
		if o.seq > lo && o.seq < hi && !in[o.id] && h.overlaps(o.x, o.y, o.x+o.w, o.y+o.h) {
			return true
		}
	}
	return false
}
