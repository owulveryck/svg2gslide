package mapper

import (
	"math"
	"strconv"
	"strings"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// rgba is a straight (non-premultiplied) colour, components in [0,1].
type rgba struct{ r, g, b, a float64 }

var black = rgba{0, 0, 0, 1}
var white = rgba{1, 1, 1, 1}

func (c rgba) opaque() *slides.OpaqueColor {
	return &slides.OpaqueColor{RgbColor: &slides.RgbColor{
		Red: c.r, Green: c.g, Blue: c.b,
		ForceSendFields: []string{"Red", "Green", "Blue"},
	}}
}

// over composites c (with alpha a) over the opaque colour bg.
func (c rgba) over(bg rgba, a float64) rgba {
	return rgba{
		r: bg.r + (c.r-bg.r)*a,
		g: bg.g + (c.g-bg.g)*a,
		b: bg.b + (c.b-bg.b)*a,
		a: 1,
	}
}

func lerp(a, b rgba, t float64) rgba {
	return rgba{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t, a.a + (b.a-a.a)*t}
}

type gradStop struct {
	off float64
	c   rgba
}

// gradient is a linear or radial gradient reduced to what Slides can use:
// its stops (for an average solid colour, or per-character sampling on
// text) and the linear direction vector.
type gradient struct {
	radial         bool
	stops          []gradStop
	x1, y1, x2, y2 float64
}

// at samples the gradient at t ∈ [0,1] (pad spread).
func (g *gradient) at(t float64) rgba {
	s := g.stops
	if len(s) == 0 {
		return black
	}
	if t <= s[0].off {
		return s[0].c
	}
	for i := 1; i < len(s); i++ {
		if t <= s[i].off {
			span := s[i].off - s[i-1].off
			if span <= 0 {
				return s[i].c
			}
			return lerp(s[i-1].c, s[i].c, (t-s[i-1].off)/span)
		}
	}
	return s[len(s)-1].c
}

// average integrates the gradient over [0,1]. Radial gradients weight each
// ring by its area (∝ t dt), so the outer stops dominate as they do on
// screen.
func (g *gradient) average() rgba {
	const n = 64
	var acc rgba
	wsum := 0.0
	for i := 0; i < n; i++ {
		t := (float64(i) + 0.5) / n
		w := 1.0
		if g.radial {
			w = t
		}
		c := g.at(t)
		// Average premultiplied so transparent stops don't tint the result.
		acc.r += c.r * c.a * w
		acc.g += c.g * c.a * w
		acc.b += c.b * c.a * w
		acc.a += c.a * w
		wsum += w
	}
	if acc.a <= 0 {
		return rgba{0, 0, 0, 0}
	}
	return rgba{acc.r / acc.a, acc.g / acc.a, acc.b / acc.a, acc.a / wsum}
}

// collectGradients indexes every gradient in the document by id, resolving
// href stop inheritance.
func collectGradients(root *svgpkg.Element) map[string]*gradient {
	raw := map[string]*svgpkg.Element{}
	root.Walk(func(e *svgpkg.Element) {
		if (e.Tag == "linearGradient" || e.Tag == "radialGradient") && e.ID() != "" {
			raw[e.ID()] = e
		}
	})
	out := map[string]*gradient{}
	for id, e := range raw {
		g := &gradient{
			radial: e.Tag == "radialGradient",
			x1:     pct(e.Attr("x1"), 0), y1: pct(e.Attr("y1"), 0),
			x2: pct(e.Attr("x2"), 1), y2: pct(e.Attr("y2"), 0),
		}
		src := e
		for depth := 0; depth < 5 && src != nil; depth++ {
			if len(stopsOf(src)) > 0 {
				break
			}
			src = raw[strings.TrimPrefix(src.Attr("href"), "#")]
		}
		if src != nil {
			g.stops = stopsOf(src)
		}
		out[id] = g
	}
	return out
}

func stopsOf(e *svgpkg.Element) []gradStop {
	var out []gradStop
	last := 0.0
	for _, s := range e.Children {
		if s.Tag != "stop" {
			continue
		}
		c, ok := parseRGBA(s.Attr("stop-color"))
		if !ok {
			c = black
		}
		c.a *= s.FloatAttr("stop-opacity", 1)
		off := math.Max(last, math.Min(1, pct(s.Attr("offset"), 0)))
		last = off
		out = append(out, gradStop{off: off, c: c})
	}
	return out
}

// pct parses "50%" or "0.5" into a fraction.
func pct(s string, def float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if strings.HasSuffix(s, "%") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err != nil {
			return def
		}
		return f / 100
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return f
}

// gradientRef returns the gradient referenced by a url(#id) paint.
func (m *Mapper) gradientRef(v string) *gradient {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "url(") {
		return nil
	}
	id := strings.TrimSuffix(strings.TrimPrefix(v, "url("), ")")
	id = strings.Trim(strings.TrimSpace(id), `'"`)
	return m.grads[strings.TrimPrefix(id, "#")]
}

// paint resolves an SVG paint value into a solid colour. Gradients are
// reduced to their average colour (Slides has no gradient fills through the
// API).
func (m *Mapper) paint(v string) (rgba, bool) {
	if g := m.gradientRef(v); g != nil {
		return g.average(), true
	}
	return parseRGBA(v)
}

// fillPaint resolves the effective fill of e (inherited, SVG default black).
// ok=false means no fill (none / transparent / unresolvable url).
func (m *Mapper) fillPaint(e *svgpkg.Element) (rgba, bool) {
	f := strings.TrimSpace(e.Inherited("fill"))
	switch f {
	case "":
		return black, true
	case "none", "transparent":
		return rgba{}, false
	}
	return m.paint(f)
}

// opacity is the composed group opacity of e: the product of its own and
// every ancestor's opacity (attribute and stylesheet, for the active phase).
func (m *Mapper) opacity(e *svgpkg.Element) float64 {
	op := 1.0
	for cur := e; cur != nil; cur = cur.Parent {
		op *= m.sheet.Opacity(cur, m.cfg.Phase)
	}
	return op
}

var namedColors = map[string]string{
	"white": "#ffffff", "black": "#000000", "red": "#ff0000",
	"green": "#008000", "blue": "#0000ff", "gray": "#808080",
	"grey": "#808080", "silver": "#c0c0c0", "yellow": "#ffff00",
	"orange": "#ffa500", "purple": "#800080", "navy": "#000080",
	"teal": "#008080", "lime": "#00ff00", "aqua": "#00ffff",
	"cyan": "#00ffff", "magenta": "#ff00ff", "fuchsia": "#ff00ff",
	"maroon": "#800000", "olive": "#808000", "lightgray": "#d3d3d3",
	"lightgrey": "#d3d3d3", "darkgray": "#a9a9a9", "darkgrey": "#a9a9a9",
}

// parseRGBA parses #rgb, #rrggbb, #rrggbbaa, rgb(), rgba() and a few named
// colours.
func parseRGBA(s string) (rgba, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "none" || s == "transparent" || strings.HasPrefix(s, "url(") {
		return rgba{}, false
	}
	if hex, ok := namedColors[s]; ok {
		s = hex
	}
	if strings.HasPrefix(s, "rgb") {
		open, close := strings.IndexByte(s, '('), strings.IndexByte(s, ')')
		if open < 0 || close < open {
			return rgba{}, false
		}
		parts := strings.FieldsFunc(s[open+1:close], func(r rune) bool { return r == ',' || r == ' ' || r == '/' })
		if len(parts) < 3 {
			return rgba{}, false
		}
		var v [4]float64
		v[3] = 1
		for i := 0; i < len(parts) && i < 4; i++ {
			p := parts[i]
			f, err := strconv.ParseFloat(strings.TrimSuffix(p, "%"), 64)
			if err != nil {
				return rgba{}, false
			}
			switch {
			case strings.HasSuffix(p, "%"):
				f /= 100
			case i < 3:
				f /= 255
			}
			v[i] = math.Max(0, math.Min(1, f))
		}
		return rgba{v[0], v[1], v[2], v[3]}, true
	}
	if !strings.HasPrefix(s, "#") {
		return rgba{}, false
	}
	hex := s[1:]
	switch len(hex) {
	case 3, 4:
		b := make([]byte, 0, 8)
		for i := 0; i < len(hex); i++ {
			b = append(b, hex[i], hex[i])
		}
		hex = string(b)
	}
	if len(hex) == 6 {
		hex += "ff"
	}
	if len(hex) != 8 {
		return rgba{}, false
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return rgba{}, false
	}
	return rgba{
		r: float64(v>>24&0xff) / 255,
		g: float64(v>>16&0xff) / 255,
		b: float64(v>>8&0xff) / 255,
		a: float64(v&0xff) / 255,
	}, true
}
