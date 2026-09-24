package mapper

import (
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// Google Slides text layout metrics (Arial), measured on the baselines of
// an exported PDF (see cmd/presctl -dump and the calibration notes in the
// README):
//   - a TEXT_BOX has a fixed 0.1" inset on every side;
//   - a line of size s at line spacing p occupies slidesLineHeight·s·p;
//     between two lines the pitch is slidesPitchDescent·prev +
//     slidesPitchAscent·cur at single spacing; extra spacing (p>1) is added
//     below the previous line, reduced spacing (p<1) is removed 80% above
//     the current line and 20% below the previous one;
//   - the first baseline sits slidesAscent·s − 0.4pt below the top inset,
//     minus the 80% share of the reduction when p<1;
//   - spaceAbove also applies to the first paragraph.
const (
	slidesLineHeight   = 1.2
	slidesPitchAscent  = 0.97
	slidesPitchDescent = slidesLineHeight - slidesPitchAscent
	slidesAscent       = 0.955
	slidesAscentOffset = 0.4 * emuPerPt
	slidesReduceAbove  = 0.8
	slidesDescent      = 0.25
	// slidesMiddleDescent is the part of the last line below its baseline
	// counted in the block height when it is vertically centred.
	slidesMiddleDescent = 0.195
	// Glyph origins land 0.45pt left of the 0.1" inset (the effective
	// horizontal inset is 6.75pt, i.e. 9px at 96dpi).
	textOriginShiftEMU = 0.45 * emuPerPt
	// maxLineSpacing caps the paragraph line spacing (Slides' default).
	maxLineSpacing = 1.15
)

// firstBaselineOffset is the distance (EMU) from the top text inset to the
// first baseline for a first line of size s (EMU) at line spacing p.
func firstBaselineOffset(s, p float64) float64 {
	off := slidesAscent*s - slidesAscentOffset
	if p < 1 {
		off -= slidesReduceAbove * slidesLineHeight * (1 - p) * s
	}
	return off
}

// textRun is a piece of text sharing one style source element.
type textRun struct {
	text string
	el   *svgpkg.Element // innermost <text>/<tspan>, for inherited style
}

// textLine is one paragraph: an explicit SVG line (tspan with dy/y).
type textLine struct {
	runs     []textRun
	x        float64
	baseline float64
}

func (l *textLine) text() string {
	var b strings.Builder
	for _, r := range l.runs {
		b.WriteString(r.text)
	}
	return b.String()
}

// parseTextLines splits a <text> element into lines. A tspan carrying `y`
// or a non-zero `dy` starts a new line; inline tspans only change style.
func parseTextLines(t *svgpkg.Element) []*textLine {
	x0, _ := firstNum(t.Attr("x"), t.InheritedFloat("font-size", 16))
	y0, _ := firstNum(t.Attr("y"), t.InheritedFloat("font-size", 16))
	cur := &textLine{x: x0, baseline: y0}
	lines := []*textLine{cur}

	var visit func(e *svgpkg.Element)
	visit = func(e *svgpkg.Element) {
		for _, item := range e.Content() {
			switch v := item.(type) {
			case string:
				cur.runs = append(cur.runs, textRun{text: v, el: e})
			case *svgpkg.Element:
				if v.Tag != "tspan" && v.Tag != "a" {
					continue
				}
				fs := v.InheritedFloat("font-size", 16)
				y, hasY := firstNum(v.Attr("y"), fs)
				dy, hasDY := firstNum(v.Attr("dy"), fs)
				x, hasX := firstNum(v.Attr("x"), fs)
				if hasY || (hasDY && dy != 0) {
					base := cur.baseline
					if hasY {
						base = y
					}
					base += dy
					if strings.TrimSpace(cur.text()) != "" {
						cur = &textLine{x: cur.x, baseline: base}
						lines = append(lines, cur)
					} else {
						cur.baseline = base
					}
				}
				if hasX && strings.TrimSpace(cur.text()) == "" {
					cur.x = x
				}
				visit(v)
			}
		}
	}
	visit(t)

	var out []*textLine
	for _, l := range lines {
		normalizeRuns(l)
		if len(l.runs) > 0 {
			out = append(out, l)
		}
	}
	return out
}

// normalizeRuns collapses whitespace across run boundaries (SVG default
// xml:space handling) and trims the line.
func normalizeRuns(l *textLine) {
	var out []textRun
	lastSpace := true // drops leading whitespace
	for _, r := range l.runs {
		var b strings.Builder
		for _, c := range applyTextTransform(r.text, r.el.Inherited("text-transform"), lastSpace) {
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' { // XML whitespace only
				if !lastSpace {
					b.WriteByte(' ')
				}
				lastSpace = true
				continue
			}
			b.WriteRune(c)
			lastSpace = false
		}
		if b.Len() > 0 {
			out = append(out, textRun{text: b.String(), el: r.el})
		}
	}
	// Trim trailing whitespace.
	for len(out) > 0 {
		last := &out[len(out)-1]
		last.text = strings.TrimRight(last.text, " ")
		if last.text != "" {
			break
		}
		out = out[:len(out)-1]
	}
	l.runs = out
}

// applyTextTransform applies the CSS text-transform property. atWordStart
// tells whether s begins a new word (for capitalize).
func applyTextTransform(s, tt string, atWordStart bool) string {
	switch strings.TrimSpace(tt) {
	case "uppercase":
		return strings.ToUpper(s)
	case "lowercase":
		return strings.ToLower(s)
	case "capitalize":
		rs := []rune(s)
		start := atWordStart
		for i, c := range rs {
			if unicode.IsSpace(c) {
				start = true
				continue
			}
			if start {
				rs[i] = unicode.ToUpper(c)
			}
			start = false
		}
		return string(rs)
	}
	return s
}

// firstNum parses the first value of a (possibly list-valued) length
// attribute; "em" is resolved against fontSize.
func firstNum(s string, fontSize float64) (float64, bool) {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	if len(f) == 0 {
		return 0, false
	}
	v := f[0]
	mul := 1.0
	switch {
	case strings.HasSuffix(v, "em"):
		v, mul = strings.TrimSuffix(v, "em"), fontSize
	case strings.HasSuffix(v, "px"):
		v = strings.TrimSuffix(v, "px")
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, false
	}
	return n * mul, true
}

// runStyle is the resolved style of a run.
type runStyle struct {
	font         string
	size         float64 // SVG user units (local)
	bold, italic bool
	color        rgba
	letter       float64 // letter-spacing, user units
}

func (m *Mapper) styleOf(e *svgpkg.Element) runStyle {
	return runStyle{
		font:   m.resolveFont(e),
		size:   e.InheritedFloat("font-size", 16),
		bold:   isBold(e.Inherited("font-weight")),
		italic: strings.Contains(e.Inherited("font-style"), "italic") || strings.Contains(e.Inherited("font-style"), "oblique"),
		color:  m.textColor(e),
		letter: e.InheritedFloat("letter-spacing", 0),
	}
}

func isBold(w string) bool {
	switch strings.TrimSpace(w) {
	case "bold", "bolder":
		return true
	}
	n, err := strconv.Atoi(strings.TrimSpace(w))
	return err == nil && n >= 600
}

// mapText converts one <text> element into a single multi-paragraph
// TEXT_BOX whose baselines land on the SVG baselines.
func (m *Mapper) mapText(e *svgpkg.Element, mat svgpkg.Matrix) {
	lines := parseTextLines(e)
	if len(lines) == 0 {
		return
	}
	var minW []float64
	if tl := e.FloatAttr("textLength", 0); tl > 0 && len(lines) == 1 {
		minW = []float64{tl}
	}
	m.layoutText(e, lines, e.Inherited("text-anchor"), minW, mat)
}

// naturalPitch models the distance between the baselines of two
// consecutive Slides paragraphs of font sizes prev and cur (same unit as
// the result) at line spacing p (fraction of single).
func naturalPitch(p, prev, cur float64) float64 {
	base := slidesPitchDescent*prev + slidesPitchAscent*cur
	if p >= 1 {
		return base + (p-1)*slidesLineHeight*prev
	}
	return base - (1-p)*slidesLineHeight*((1-slidesReduceAbove)*prev+slidesReduceAbove*cur)
}

// spacingFor inverts naturalPitch: the line spacing making the pitch
// between sizes prev and cur equal to d.
func spacingFor(d, prev, cur float64) float64 {
	base := slidesPitchDescent*prev + slidesPitchAscent*cur
	if d >= base {
		return 1 + (d-base)/(slidesLineHeight*prev)
	}
	return 1 - (base-d)/(slidesLineHeight*((1-slidesReduceAbove)*prev+slidesReduceAbove*cur))
}

// layoutText emits one TEXT_BOX holding the given lines (paragraphs),
// positioned so every baseline lands on its SVG baseline. anchor is the
// SVG text-anchor shared by the lines (each line x is its anchor point);
// minW optionally gives a known minimum width per line (textLength).
// e provides the base style and the opacity context.
func (m *Mapper) layoutText(e *svgpkg.Element, lines []*textLine, anchor string, minW []float64, mat svgpkg.Matrix) {
	base := m.styleOf(e)
	_, s := mat.ScaleFactors() // local → SVG user units (vertical)
	if s <= 0 {
		return
	}
	k := m.cfg.Scale * s // EMU per local unit

	// Per-line metrics in local units.
	lineSize := make([]float64, len(lines))
	lineW := make([]float64, len(lines))
	styles := make([][]runStyle, len(lines))
	for i, l := range lines {
		for _, r := range l.runs {
			st := m.styleOf(r.el)
			styles[i] = append(styles[i], st)
			lineSize[i] = math.Max(lineSize[i], st.size)
			lineW[i] += textWidth(r.text, st)
		}
		if i < len(minW) && minW[i] > lineW[i] {
			lineW[i] = minW[i]
		}
	}
	maxW := 0.0
	for _, w := range lineW {
		maxW = math.Max(maxW, w)
	}

	// Vertical layout (EMU). A single line spacing p is chosen as the
	// tightest pitch; larger pitches are made up with spaceAbove.
	f := lineSize[0] * k
	p := 1.0
	var pitches []float64 // EMU between consecutive baselines
	if len(lines) > 1 {
		p = math.Inf(1)
		for i := 1; i < len(lines); i++ {
			d := (lines[i].baseline - lines[i-1].baseline) * k
			pitches = append(pitches, d)
			p = math.Min(p, spacingFor(d, lineSize[i-1]*k, lineSize[i]*k))
		}
		if p <= 0.05 || math.IsInf(p, 0) {
			p = 1
		}
		// Loose pitches are rendered as spaceAbove rather than a huge
		// line spacing, so that text typed later keeps a sane spacing.
		p = math.Min(p, maxLineSpacing)
		p = math.Round(p*1000) / 1000
	}
	extras := make([]float64, len(lines)) // spaceAbove per line, EMU
	for i := 1; i < len(lines); i++ {
		extras[i] = math.Max(0, pitches[i-1]-naturalPitch(p, lineSize[i-1]*k, lineSize[i]*k))
	}
	firstBaseline := textInsetEMU + firstBaselineOffset(f, p)
	total := 0.0
	for _, d := range pitches {
		total += d
	}
	fl := lineSize[len(lines)-1] * k
	hEMU := firstBaseline + total + slidesDescent*fl*math.Max(1, p) + textInsetEMU

	// Horizontal layout (EMU). Generous slack: SVG lines never wrap, and
	// the paragraph alignment keeps the anchored edge exact.
	xMin := lines[0].x
	for _, l := range lines {
		xMin = math.Min(xMin, l.x)
	}
	contentW := maxW*k*1.06 + 0.6*f
	wEMU := contentW + 2*textInsetEMU
	var bx float64 // box left relative to the anchor point, local-unrotated EMU
	alignment := "START"
	ax := lines[0].x
	switch anchor {
	case "middle":
		alignment = "CENTER"
		bx = -wEMU / 2
	case "end":
		alignment = "END"
		bx = -wEMU + textInsetEMU + textOriginShiftEMU
	default:
		ax = xMin
		bx = -textInsetEMU + textOriginShiftEMU
	}
	by := -firstBaseline

	// Page geometry of the block, used to pick a host shape.
	px, py := mat.Apply(ax, lines[0].baseline)
	ex, ey := m.toEMU(px, py)
	geom := textGeom{
		rotated: math.Abs(math.Atan2(mat.B, mat.A)) > 1e-6 || mat.A < 0 || mat.D < 0,
		ax:      ex, base0: ey, align: alignment, f: f, p: p,
		fl: fl, total: total,
	}
	for i, l := range lines {
		w := lineW[i] * k
		var x0 float64
		switch alignment {
		case "CENTER":
			x0 = ex + (l.x-ax)*k - w/2
		case "END":
			x0 = ex + (l.x-ax)*k - w
		default:
			x0 = ex + (l.x-xMin)*k
		}
		geom.lineX0 = append(geom.lineX0, x0)
		geom.lineW = append(geom.lineW, w)
	}
	geom.top = ey - 0.9*f
	geom.bottom = ey + total + slidesDescent*fl
	geom.left, geom.right = math.Inf(1), math.Inf(-1)
	for i := range geom.lineX0 {
		geom.left = math.Min(geom.left, geom.lineX0[i])
		geom.right = math.Max(geom.right, geom.lineX0[i]+geom.lineW[i])
	}

	emit := func(h *hostPlace) string {
		var id string
		iS, iE, sa0, sbN := 0.0, 0.0, 0.0, 0.0
		alignment, vAlign := alignment, "TOP"
		if h == nil {
			id = m.nextID()
			m.createRotatedBox(id, mat, ax, lines[0].baseline, bx, by, wEMU, hEMU)
		} else {
			id, iS, iE, sa0, sbN = h.id, h.indentStart, h.indentEnd, h.spaceAbove, h.spaceBelow
			alignment, vAlign = h.align, "MIDDLE"
		}

		// Content and runs (indices are UTF-16 code units).
		var content strings.Builder
		type span struct {
			start, end int
			st         runStyle
			text       string
			line       int
		}
		var spans []span
		pos := 0
		for i, l := range lines {
			if i > 0 {
				content.WriteByte('\n')
				pos++
			}
			for j, r := range l.runs {
				n := len(utf16.Encode([]rune(r.text)))
				spans = append(spans, span{pos, pos + n, styles[i][j], r.text, i})
				content.WriteString(r.text)
				pos += n
			}
		}
		m.reqs = append(m.reqs, &slides.Request{InsertText: &slides.InsertTextRequest{ObjectId: id, Text: content.String()}})
		m.reqs = append(m.reqs, &slides.Request{UpdateTextStyle: &slides.UpdateTextStyleRequest{
			ObjectId:  id,
			Style:     m.slidesTextStyle(base, k),
			TextRange: &slides.Range{Type: "ALL"},
			Fields:    "fontFamily,fontSize,bold,italic,foregroundColor",
		}})
		for _, sp := range spans {
			if sp.st == base {
				continue
			}
			m.reqs = append(m.reqs, &slides.Request{UpdateTextStyle: &slides.UpdateTextStyleRequest{
				ObjectId:  id,
				Style:     m.slidesTextStyle(sp.st, k),
				TextRange: fixedRange(sp.start, sp.end),
				Fields:    "fontFamily,fontSize,bold,italic,foregroundColor",
			}})
		}
		m.gradientText(id, e, lines, lineW, styles, anchor, k)

		// Paragraphs: alignment, uniform line spacing, extra pitch as spaceAbove,
		// per-line indent for start-anchored lines with different x.
		m.reqs = append(m.reqs, &slides.Request{UpdateParagraphStyle: &slides.UpdateParagraphStyleRequest{
			ObjectId: id,
			Style: &slides.ParagraphStyle{
				Alignment:       alignment,
				LineSpacing:     math.Round(p*1000) / 10, // p already rounded to 1/1000
				SpaceAbove:      &slides.Dimension{Magnitude: 0, Unit: "PT", ForceSendFields: []string{"Magnitude"}},
				SpaceBelow:      &slides.Dimension{Magnitude: 0, Unit: "PT", ForceSendFields: []string{"Magnitude"}},
				IndentStart:     ptDim(iS),
				IndentFirstLine: ptDim(iS),
				IndentEnd:       ptDim(iE),
			},
			TextRange: &slides.Range{Type: "ALL"},
			Fields:    "alignment,lineSpacing,spaceAbove,spaceBelow,indentStart,indentFirstLine,indentEnd",
		}})
		lineStart := 0
		for i, l := range lines {
			n := len(utf16.Encode([]rune(l.text())))
			ps := &slides.ParagraphStyle{}
			var fields []string
			extra := extras[i]
			if i == 0 {
				extra += sa0
			}
			if extra > 1000 {
				ps.SpaceAbove = &slides.Dimension{Magnitude: math.Round(extra/emuPerPt*100) / 100, Unit: "PT"}
				fields = append(fields, "spaceAbove")
			}
			if alignment == "START" {
				if ind := (l.x - xMin) * k; ind > 1000 {
					ps.IndentStart = &slides.Dimension{Magnitude: (iS + ind) / emuPerPt, Unit: "PT"}
					ps.IndentFirstLine = ps.IndentStart
					fields = append(fields, "indentStart", "indentFirstLine")
				}
			}
			if i == len(lines)-1 && sbN > 1000 {
				ps.SpaceBelow = ptDim(sbN)
				fields = append(fields, "spaceBelow")
			}
			if len(fields) > 0 {
				m.reqs = append(m.reqs, &slides.Request{UpdateParagraphStyle: &slides.UpdateParagraphStyleRequest{
					ObjectId:  id,
					Style:     ps,
					TextRange: fixedRange(lineStart, lineStart+n),
					Fields:    strings.Join(fields, ","),
				}})
			}
			lineStart += n + 1
		}
		m.reqs = append(m.reqs, &slides.Request{UpdateShapeProperties: &slides.UpdateShapePropertiesRequest{
			ObjectId: id,
			ShapeProperties: &slides.ShapeProperties{
				ContentAlignment: vAlign,
				Autofit:          &slides.Autofit{AutofitType: "NONE"},
			},
			Fields: "contentAlignment,autofit.autofitType",
		}})
		return id
	}
	m.deferText(e, geom, emit)
}

// ptDim is an explicit (zero included) length in points from EMU.
func ptDim(emu float64) *slides.Dimension {
	return &slides.Dimension{Magnitude: math.Round(emu/emuPerPt*100) / 100, Unit: "PT", ForceSendFields: []string{"Magnitude"}}
}

func fixedRange(start, end int) *slides.Range {
	s, e := int64(start), int64(end)
	return &slides.Range{Type: "FIXED_RANGE", StartIndex: &s, EndIndex: &e}
}

func (m *Mapper) slidesTextStyle(st runStyle, k float64) *slides.TextStyle {
	pt := math.Max(1, math.Round(st.size*k/emuPerPt*10)/10)
	return &slides.TextStyle{
		FontFamily:      st.font,
		FontSize:        &slides.Dimension{Magnitude: pt, Unit: "PT"},
		Bold:            st.bold,
		Italic:          st.italic,
		ForegroundColor: &slides.OptionalColor{OpaqueColor: st.color.opaque()},
		ForceSendFields: []string{"Bold", "Italic"},
	}
}

// createRotatedBox creates a TEXT_BOX of size w×h (EMU) whose top-left
// corner is offset by (bx,by) EMU from the anchor (ax,ay) in the local text
// frame, carrying the rotation of mat.
func (m *Mapper) createRotatedBox(id string, mat svgpkg.Matrix, ax, ay, bx, by, w, h float64) {
	px, py := mat.Apply(ax, ay)
	ex, ey := m.toEMU(px, py)
	theta := math.Atan2(mat.B, mat.A)
	if math.Abs(theta) < 1e-6 {
		m.createShape(id, "TEXT_BOX", ex+bx, ey+by, w, h, 0)
		return
	}
	cos, sin := math.Cos(theta), math.Sin(theta)
	m.reqs = append(m.reqs, &slides.Request{CreateShape: &slides.CreateShapeRequest{
		ObjectId:  id,
		ShapeType: "TEXT_BOX",
		ElementProperties: &slides.PageElementProperties{
			PageObjectId: m.cfg.SlideID,
			Size:         sizeEMU(w, h),
			Transform: &slides.AffineTransform{
				ScaleX: cos, ShearX: -sin,
				ShearY: sin, ScaleY: cos,
				TranslateX:      ex + cos*bx - sin*by,
				TranslateY:      ey + sin*bx + cos*by,
				Unit:            "EMU",
				ForceSendFields: []string{"ScaleX", "ScaleY", "ShearX", "ShearY", "TranslateX", "TranslateY"},
			},
		},
	}})
}

// gradientText colours each character of a gradient-filled text by
// sampling the gradient at the character position (objectBoundingBox
// units), approximating the SVG rendering.
func (m *Mapper) gradientText(id string, e *svgpkg.Element, lines []*textLine, lineW []float64, styles [][]runStyle, anchor string, k float64) {
	type ch struct {
		idx  int
		x, y float64
		g    *gradient
	}
	var chars []ch
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	pos := 0
	any := false
	for i, l := range lines {
		if i > 0 {
			pos++
		}
		start := l.x
		switch anchor {
		case "middle":
			start -= lineW[i] / 2
		case "end":
			start -= lineW[i]
		}
		x := start
		for j, r := range l.runs {
			st := styles[i][j]
			g := m.gradientRef(r.el.Inherited("fill"))
			for _, c := range r.text {
				w := runeWidth(c, st.bold)*st.size + st.letter
				if g != nil && !g.radial {
					chars = append(chars, ch{pos, x + w/2, l.baseline - 0.35*st.size, g})
					any = true
				}
				x += w
				pos += len(utf16.Encode([]rune{c}))
			}
			minY = math.Min(minY, l.baseline-0.9*st.size)
			maxY = math.Max(maxY, l.baseline+0.2*st.size)
		}
		minX, maxX = math.Min(minX, start), math.Max(maxX, x)
	}
	if !any || len(chars) > 400 {
		return
	}
	bw, bh := math.Max(maxX-minX, 1e-6), math.Max(maxY-minY, 1e-6)
	for _, c := range chars {
		g := c.g
		u, v := (c.x-minX)/bw, (c.y-minY)/bh
		dx, dy := g.x2-g.x1, g.y2-g.y1
		t := 0.0
		if d := dx*dx + dy*dy; d > 0 {
			t = ((u-g.x1)*dx + (v-g.y1)*dy) / d
		}
		col := g.at(math.Max(0, math.Min(1, t)))
		col = col.over(m.pageBG, col.a*m.opacity(e))
		m.reqs = append(m.reqs, &slides.Request{UpdateTextStyle: &slides.UpdateTextStyleRequest{
			ObjectId:  id,
			Style:     &slides.TextStyle{ForegroundColor: &slides.OptionalColor{OpaqueColor: col.opaque()}},
			TextRange: fixedRange(c.idx, c.idx+1),
			Fields:    "foregroundColor",
		}})
	}
}

// resolveFont picks the Slides font for a text element: its (inherited)
// font-family, generic CSS families mapped to stock fonts, else the
// document default.
func (m *Mapper) resolveFont(e *svgpkg.Element) string {
	list := strings.TrimSpace(e.Inherited("font-family"))
	if list == "" {
		return mapFontName(m.cfg.FontFamily)
	}
	fallback := ""
	for f := range strings.SplitSeq(list, ",") {
		f = strings.Trim(strings.TrimSpace(f), `'"`)
		if f == "" {
			continue
		}
		if sub, ok := platformFonts[strings.ToLower(f)]; ok {
			// Not available in Slides: remember its substitute and keep
			// looking for a real family further down the list.
			if fallback == "" {
				fallback = sub
			}
			continue
		}
		// Real family (a Google Font Slides can load) or a CSS generic.
		return mapFontName(f)
	}
	if fallback == "" {
		return mapFontName(m.cfg.FontFamily)
	}
	return fallback
}

// platformFonts are OS/UI fonts that Google Slides doesn't have, with the
// metric-compatible substitute used when nothing better follows them in the
// font-family list.
var platformFonts = map[string]string{
	"helvetica neue": "Arial", "helvetica": "Arial",
	"-apple-system": "Arial", "blinkmacsystemfont": "Arial", "system-ui": "Arial",
	"segoe ui": "Arial", "ui-sans-serif": "Arial", "sf pro display": "Arial",
	"sf pro text": "Arial", ".sfnstext": "Arial",
	"menlo": "Roboto Mono", "consolas": "Roboto Mono", "sf mono": "Roboto Mono",
	"monaco": "Roboto Mono", "ui-monospace": "Roboto Mono",
}

// genericFonts maps CSS generic families to stock Slides fonts.
var genericFonts = map[string]string{
	"sans-serif": "Arial", "serif": "Times New Roman", "monospace": "Courier New",
	"cursive": "Comic Sans MS", "fantasy": "Impact", "times": "Times New Roman",
	"courier": "Courier New",
}

func mapFontName(f string) string {
	l := strings.ToLower(strings.TrimSpace(f))
	if name, ok := genericFonts[l]; ok {
		return name
	}
	if name, ok := platformFonts[l]; ok {
		return name
	}
	return f
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

// textWidth estimates the advance width of s (user units) with Arial
// metrics, including letter-spacing.
func textWidth(s string, st runStyle) float64 {
	w := 0.0
	for _, c := range s {
		w += runeWidth(c, st.bold)*st.size + st.letter
	}
	return w
}
