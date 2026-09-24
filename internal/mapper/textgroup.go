package mapper

import (
	"math"
	"strings"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// Many generators emit one <text> per visual line (hand-written diagrams)
// or even one per word (PlantUML, with explicit x/textLength). Mapped
// naively this yields hundreds of text boxes. The grouping pass below
// rebuilds the logical structure before mapping:
//
//   - words → line: consecutive start-anchored texts on the same baseline
//     whose extents touch (gap below ~0.8em) are joined, a space being
//     inserted where the gap shows one;
//   - lines → block: consecutive lines with a regular pitch (≤ ~2.3em)
//     sharing their start, centre or end are stacked in one box.
//
// Only consecutive <text> siblings are considered, so the drawing order
// (z-order) with respect to shapes is preserved.

// textBlock is a group of <text> elements rendered as one text box.
type textBlock struct {
	lines  []*textLine
	minW   []float64
	anchor string
}

// gLine is a visual line under construction.
type gLine struct {
	els    []*svgpkg.Element
	runs   []textRun
	x0, x1 float64 // horizontal extent, user units
	y, fs  float64 // baseline and largest font size
	anchor string  // anchor of the source text(s)
	hasTL  bool    // extent comes from textLength
}

func (l *gLine) blank() bool {
	for _, r := range l.runs {
		if strings.TrimSpace(strings.ReplaceAll(r.text, "\u00a0", " ")) != "" {
			return false
		}
	}
	return true
}

// planTextGroups scans the tree and records, for each group leader, the
// block to render, and the member elements to skip.
func (m *Mapper) planTextGroups(root *svgpkg.Element) {
	m.textBlocks = map[*svgpkg.Element]*textBlock{}
	m.textSkip = map[*svgpkg.Element]bool{}
	root.Walk(func(e *svgpkg.Element) {
		var run []*svgpkg.Element
		flush := func() {
			if len(run) > 0 {
				m.groupTextRun(run)
			}
			run = nil
		}
		for _, c := range e.Children {
			if c.Tag == "text" && c.Attr("transform") == "" && c.Attr("rotate") == "" &&
				m.sheet.Opacity(c, m.cfg.Phase) >= 0.01 {
				run = append(run, c)
				continue
			}
			flush()
		}
		flush()
	})
}

// groupTextRun groups a run of consecutive <text> siblings.
func (m *Mapper) groupTextRun(run []*svgpkg.Element) {
	// 1. Words → lines.
	var lines []*gLine
	var cur *gLine
	for _, el := range run {
		tl := parseTextLines(el)
		if len(tl) == 0 {
			m.textSkip[el] = true // empty text
			cur = nil
			continue
		}
		if len(tl) != 1 {
			cur = nil // multi-line <text> of its own: map it as is
			continue
		}
		l := tl[0]
		anchor := strings.TrimSpace(el.Inherited("text-anchor"))
		if anchor == "" {
			anchor = "start"
		}
		fs, w := 0.0, 0.0
		for _, r := range l.runs {
			st := m.styleOf(r.el)
			fs = math.Max(fs, st.size)
			w += textWidth(r.text, st)
		}
		hasTL := false
		if v := el.FloatAttr("textLength", 0); v > 0 {
			w, hasTL = v, true
		}
		x0 := l.x
		switch anchor {
		case "middle":
			x0 -= w / 2
		case "end":
			x0 -= w
		}
		g := &gLine{els: []*svgpkg.Element{el}, runs: l.runs, x0: x0, x1: x0 + w, y: l.baseline, fs: fs, anchor: anchor, hasTL: hasTL}

		if cur != nil && cur.anchor == "start" && anchor == "start" &&
			math.Abs(g.y-cur.y) < 0.05*math.Max(fs, cur.fs)+0.01 {
			gap := g.x0 - cur.x1
			em := math.Max(fs, cur.fs)
			// With measured extents (textLength) the gap is reliable and a
			// word space can be restored; with estimated widths only
			// touching pieces are joined, explicit x gaps being layout.
			joinable := gap > -0.3*em && gap < 0.8*em
			if !(cur.hasTL && hasTL) {
				joinable = math.Abs(gap) < 0.1*em
			}
			if joinable {
				endsSpace := strings.HasSuffix(strings.ReplaceAll(cur.runs[len(cur.runs)-1].text, "\u00a0", " "), " ") || cur.blank()
				startsSpace := strings.HasPrefix(strings.ReplaceAll(g.runs[0].text, "\u00a0", " "), " ") || g.blank()
				if gap > 0.15*em && !endsSpace && !startsSpace {
					cur.runs = append(cur.runs, textRun{text: " ", el: g.runs[0].el})
				}
				cur.els = append(cur.els, el)
				cur.runs = append(cur.runs, g.runs...)
				cur.x1 = g.x1
				cur.fs = em
				cur.hasTL = cur.hasTL && hasTL
				continue
			}
		}
		cur = g
		lines = append(lines, g)
	}

	// Joined lines: PlantUML separates words with standalone NBSP texts,
	// which are plain spaces once joined.
	for _, l := range lines {
		if len(l.els) > 1 {
			for i := range l.runs {
				l.runs[i].text = strings.ReplaceAll(l.runs[i].text, "\u00a0", " ")
			}
			tmp := &textLine{runs: l.runs}
			normalizeRuns(tmp)
			l.runs = tmp.runs
		}
	}

	// 2. Lines → blocks.
	var block []*gLine
	align := ""
	emit := func() {
		m.emitBlock(block, align)
		block, align = nil, ""
	}
	for _, l := range lines {
		if l.blank() {
			// Blank spacer lines are absorbed (the pitch keeps the gap).
			for _, el := range l.els {
				m.textSkip[el] = true
			}
			continue
		}
		if len(block) == 0 {
			block = []*gLine{l}
			continue
		}
		prev := block[len(block)-1]
		d := l.y - prev.y
		em := math.Max(l.fs, prev.fs)
		a := ""
		if d > 0.8*math.Min(l.fs, prev.fs) && d <= 2.3*em {
			a = blockAlignment(append(block[:len(block):len(block)], l))
		}
		if a == "" {
			emit()
			block = []*gLine{l}
			continue
		}
		align = a
		block = append(block, l)
	}
	emit()
}

// blockAlignment returns how the stacked lines are aligned ("start",
// "middle" or "end"), or "" when no alignment holds for every pair.
func blockAlignment(lines []*gLine) string {
	first := lines[0]
	measured := true
	for _, l := range lines {
		measured = measured && l.hasTL
	}
	order := []string{first.anchor, "start", "middle", "end"}
	if measured {
		// Generators emitting measured runs (PlantUML) centre their
		// blocks; a start match is then usually a coincidence.
		order = []string{"middle", "start", "end"}
	}
	for _, a := range order {
		ok := true
		for i := 1; i < len(lines) && ok; i++ {
			ok = aligned(lines[i-1], lines[i], a)
		}
		if ok {
			return a
		}
	}
	return ""
}

// aligned reports whether two stacked lines share the given alignment.
// Estimated extents are imprecise: centre/end alignment needs either
// explicit anchors or measured (textLength) extents.
func aligned(a, b *gLine, align string) bool {
	em := math.Max(a.fs, b.fs)
	tolStart := math.Max(1, 0.1*em)
	tolMid := math.Max(1.5, 0.2*em)
	measured := a.hasTL && b.hasTL
	switch align {
	case "start":
		return a.anchor == "start" && b.anchor == "start" && math.Abs(a.x0-b.x0) < tolStart
	case "middle":
		same := math.Abs((a.x0+a.x1)/2-(b.x0+b.x1)/2) < tolMid
		return same && a.anchor == b.anchor && (a.anchor == "middle" || measured)
	case "end":
		same := math.Abs(a.x1-b.x1) < tolMid
		return same && a.anchor == b.anchor && (a.anchor == "end" || measured)
	}
	return false
}

// emitBlock registers a block of lines under its first element.
func (m *Mapper) emitBlock(block []*gLine, align string) {
	if len(block) == 0 {
		return
	}
	// A single line made of a single element maps as usual.
	if len(block) == 1 && len(block[0].els) == 1 {
		return
	}
	if align == "" {
		align = block[0].anchor
	}
	tb := &textBlock{anchor: align}
	for _, l := range block {
		x := l.x0
		switch align {
		case "middle":
			x = (l.x0 + l.x1) / 2
		case "end":
			x = l.x1
		}
		tb.lines = append(tb.lines, &textLine{runs: l.runs, x: x, baseline: l.y})
		w := 0.0
		if l.hasTL {
			w = l.x1 - l.x0
		}
		tb.minW = append(tb.minW, w)
		for _, el := range l.els {
			m.textSkip[el] = true
		}
	}
	leader := block[0].els[0]
	delete(m.textSkip, leader)
	m.textBlocks[leader] = tb
}

// mapTextBlock renders a grouped block led by e.
func (m *Mapper) mapTextBlock(e *svgpkg.Element, tb *textBlock, mat svgpkg.Matrix) {
	anchor := tb.anchor
	if anchor == "start" {
		anchor = ""
	}
	m.layoutText(e, tb.lines, anchor, tb.minW, mat)
}
