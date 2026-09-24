package svg

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ViewBox is the root svg viewBox.
type ViewBox struct {
	X, Y, W, H float64
}

// ParseViewBox parses a "minX minY width height" viewBox attribute.
func ParseViewBox(s string) (ViewBox, error) {
	f := strings.FieldsFunc(s, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' })
	if len(f) != 4 {
		return ViewBox{}, fmt.Errorf("invalid viewBox %q", s)
	}
	var v [4]float64
	for i, x := range f {
		p, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return ViewBox{}, fmt.Errorf("invalid viewBox %q: %w", s, err)
		}
		v[i] = p
	}
	return ViewBox{X: v[0], Y: v[1], W: v[2], H: v[3]}, nil
}

// Matrix is a 2D affine transform using SVG conventions:
// x' = A*x + C*y + E ; y' = B*x + D*y + F.
type Matrix struct {
	A, B, C, D, E, F float64
}

// Identity returns the identity matrix.
func Identity() Matrix { return Matrix{A: 1, D: 1} }

// Mul returns m * n (n applied first).
func (m Matrix) Mul(n Matrix) Matrix {
	return Matrix{
		A: m.A*n.A + m.C*n.B,
		B: m.B*n.A + m.D*n.B,
		C: m.A*n.C + m.C*n.D,
		D: m.B*n.C + m.D*n.D,
		E: m.A*n.E + m.C*n.F + m.E,
		F: m.B*n.E + m.D*n.F + m.F,
	}
}

// Apply transforms a point.
func (m Matrix) Apply(x, y float64) (float64, float64) {
	return m.A*x + m.C*y + m.E, m.B*x + m.D*y + m.F
}

// Rotation returns the rotation component in degrees (assumes no skew and
// uniform positive scale, which holds for translate+rotate transforms).
func (m Matrix) Rotation() float64 {
	return math.Atan2(m.B, m.A) * 180 / math.Pi
}

// ScaleFactors returns the length scaling applied by the matrix along the
// local x and y axes (robust to rotation; ignores skew).
func (m Matrix) ScaleFactors() (sx, sy float64) {
	return math.Hypot(m.A, m.B), math.Hypot(m.C, m.D)
}

// NestedSVGMatrix returns the coordinate transform established by a nested
// <svg x y width height viewBox> element. Without a viewBox it is a pure
// translation. A preserveAspectRatio other than "none" is treated as the SVG
// default xMidYMid meet (uniform min scale, centered); "none" stretches
// non-uniformly.
func NestedSVGMatrix(x, y, w, h float64, vb ViewBox, hasViewBox bool, preserveAspectRatio string) Matrix {
	m := Matrix{A: 1, D: 1, E: x, F: y}
	if !hasViewBox || vb.W <= 0 || vb.H <= 0 {
		return m
	}
	sx, sy := 1.0, 1.0
	if w > 0 && h > 0 {
		sx, sy = w/vb.W, h/vb.H
		if strings.TrimSpace(preserveAspectRatio) != "none" { // spec default: xMidYMid meet
			s := math.Min(sx, sy)
			m = m.Mul(Matrix{A: 1, D: 1, E: (w - vb.W*s) / 2, F: (h - vb.H*s) / 2})
			sx, sy = s, s
		}
	}
	return m.Mul(Matrix{A: sx, D: sy}).Mul(Matrix{A: 1, D: 1, E: -vb.X, F: -vb.Y})
}

var transformRe = regexp.MustCompile(`(translate|rotate|scale|matrix)\s*\(([^)]*)\)`)

// ParseTransform parses an SVG transform attribute (translate, rotate, scale,
// matrix), composing the operations left to right.
func ParseTransform(s string) Matrix {
	m := Identity()
	for _, g := range transformRe.FindAllStringSubmatch(s, -1) {
		args := parseFloats(g[2])
		switch g[1] {
		case "translate":
			tx, ty := 0.0, 0.0
			if len(args) > 0 {
				tx = args[0]
			}
			if len(args) > 1 {
				ty = args[1]
			}
			m = m.Mul(Matrix{A: 1, D: 1, E: tx, F: ty})
		case "rotate":
			if len(args) == 0 {
				continue
			}
			rad := args[0] * math.Pi / 180
			cos, sin := math.Cos(rad), math.Sin(rad)
			rot := Matrix{A: cos, B: sin, C: -sin, D: cos}
			if len(args) >= 3 {
				cx, cy := args[1], args[2]
				m = m.Mul(Matrix{A: 1, D: 1, E: cx, F: cy}).Mul(rot).Mul(Matrix{A: 1, D: 1, E: -cx, F: -cy})
			} else {
				m = m.Mul(rot)
			}
		case "scale":
			sx := 1.0
			if len(args) > 0 {
				sx = args[0]
			}
			sy := sx
			if len(args) > 1 {
				sy = args[1]
			}
			m = m.Mul(Matrix{A: sx, D: sy})
		case "matrix":
			if len(args) == 6 {
				m = m.Mul(Matrix{A: args[0], B: args[1], C: args[2], D: args[3], E: args[4], F: args[5]})
			}
		}
	}
	return m
}

var numRe = regexp.MustCompile(`[-+]?[0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?`)

func parseFloats(s string) []float64 {
	var out []float64
	for _, n := range numRe.FindAllString(s, -1) {
		f, err := strconv.ParseFloat(n, 64)
		if err == nil {
			out = append(out, f)
		}
	}
	return out
}

// PathSeg is one command of an SVG path.
type PathSeg struct {
	Op   byte
	Args []float64
}

// ParsePathD parses a path "d" attribute into absolute commands. Relative
// commands are resolved, S/T are expanded into C/Q with reflected control
// points, and implicit command repetition is handled. The returned ops are
// M, L, H, V, Q, C, A and Z; each segment carries one command's worth of
// arguments (a repeated command yields several segments).
func ParsePathD(d string) ([]PathSeg, error) {
	toks, err := tokenizePath(d)
	if err != nil {
		return nil, err
	}
	arity := map[byte]int{'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'T': 2, 'A': 7, 'Z': 0}
	var segs []PathSeg
	var cx, cy, sx, sy float64 // current point, subpath start
	var lcx, lcy float64       // last control point (for S/T)
	var lastOp byte
	i := 0
	var cmd byte
	for i < len(toks) {
		if toks[i].cmd != 0 {
			cmd = toks[i].cmd
			i++
		} else if cmd == 0 {
			return nil, fmt.Errorf("path data must start with a command")
		}
		up := cmd &^ 0x20 // upper-case
		rel := cmd != up
		n := arity[up]
		if up == 'Z' {
			segs = append(segs, PathSeg{Op: 'Z'})
			cx, cy = sx, sy
			lastOp = 'Z'
			continue
		}
		if i+n > len(toks) {
			return nil, fmt.Errorf("path command %q: missing arguments", cmd)
		}
		a := make([]float64, n)
		for k := 0; k < n; k++ {
			if toks[i+k].cmd != 0 {
				return nil, fmt.Errorf("path command %q: missing arguments", cmd)
			}
			a[k] = toks[i+k].num
		}
		i += n
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		switch up {
		case 'M':
			cx, cy = a[0]+ox, a[1]+oy
			sx, sy = cx, cy
			segs = append(segs, PathSeg{Op: 'M', Args: []float64{cx, cy}})
			// Subsequent pairs are implicit lineto.
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
		case 'L':
			cx, cy = a[0]+ox, a[1]+oy
			segs = append(segs, PathSeg{Op: 'L', Args: []float64{cx, cy}})
		case 'H':
			cx = a[0] + ox
			segs = append(segs, PathSeg{Op: 'H', Args: []float64{cx}})
		case 'V':
			cy = a[0] + oy
			segs = append(segs, PathSeg{Op: 'V', Args: []float64{cy}})
		case 'C':
			c := []float64{a[0] + ox, a[1] + oy, a[2] + ox, a[3] + oy, a[4] + ox, a[5] + oy}
			segs = append(segs, PathSeg{Op: 'C', Args: c})
			lcx, lcy, cx, cy = c[2], c[3], c[4], c[5]
		case 'S':
			x1, y1 := cx, cy
			if lastOp == 'C' {
				x1, y1 = 2*cx-lcx, 2*cy-lcy
			}
			c := []float64{x1, y1, a[0] + ox, a[1] + oy, a[2] + ox, a[3] + oy}
			segs = append(segs, PathSeg{Op: 'C', Args: c})
			lcx, lcy, cx, cy = c[2], c[3], c[4], c[5]
			up = 'C'
		case 'Q':
			c := []float64{a[0] + ox, a[1] + oy, a[2] + ox, a[3] + oy}
			segs = append(segs, PathSeg{Op: 'Q', Args: c})
			lcx, lcy, cx, cy = c[0], c[1], c[2], c[3]
		case 'T':
			x1, y1 := cx, cy
			if lastOp == 'Q' {
				x1, y1 = 2*cx-lcx, 2*cy-lcy
			}
			c := []float64{x1, y1, a[0] + ox, a[1] + oy}
			segs = append(segs, PathSeg{Op: 'Q', Args: c})
			lcx, lcy, cx, cy = x1, y1, c[2], c[3]
			up = 'Q'
		case 'A':
			c := []float64{a[0], a[1], a[2], a[3], a[4], a[5] + ox, a[6] + oy}
			segs = append(segs, PathSeg{Op: 'A', Args: c})
			cx, cy = c[5], c[6]
		}
		lastOp = up
	}
	return segs, nil
}

type pathTok struct {
	cmd byte
	num float64
}

// tokenizePath splits path data into command letters and numbers
// (handling "1-2", ".5.5", exponents and compact arc flags).
func tokenizePath(d string) ([]pathTok, error) {
	var toks []pathTok
	i := 0
	var lastCmd byte
	argIdx := 0
	for i < len(d) {
		c := d[i]
		switch {
		case c == ' ' || c == ',' || c == '\n' || c == '\t' || c == '\r':
			i++
		case strings.IndexByte("MmLlHhVvCcSsQqTtAaZz", c) >= 0:
			toks = append(toks, pathTok{cmd: c})
			lastCmd = c &^ 0x20
			argIdx = 0
			i++
		default:
			// Arc flags (4th and 5th args) may be written without separators.
			if lastCmd == 'A' && (argIdx%7 == 3 || argIdx%7 == 4) && (c == '0' || c == '1') {
				toks = append(toks, pathTok{num: float64(c - '0')})
				argIdx++
				i++
				continue
			}
			j := i
			if j < len(d) && (d[j] == '+' || d[j] == '-') {
				j++
			}
			dot := false
			for j < len(d) && ((d[j] >= '0' && d[j] <= '9') || (d[j] == '.' && !dot)) {
				if d[j] == '.' {
					dot = true
				}
				j++
			}
			if j < len(d) && (d[j] == 'e' || d[j] == 'E') {
				k := j + 1
				if k < len(d) && (d[k] == '+' || d[k] == '-') {
					k++
				}
				if k < len(d) && d[k] >= '0' && d[k] <= '9' {
					j = k
					for j < len(d) && d[j] >= '0' && d[j] <= '9' {
						j++
					}
				}
			}
			if j == i {
				return nil, fmt.Errorf("unexpected character %q in path data at %d", c, i)
			}
			f, err := strconv.ParseFloat(d[i:j], 64)
			if err != nil {
				return nil, fmt.Errorf("invalid number %q in path data", d[i:j])
			}
			toks = append(toks, pathTok{num: f})
			argIdx++
			i = j
		}
	}
	return toks, nil
}

// ParsePoints parses a polygon/polyline points attribute.
func ParsePoints(s string) [][2]float64 {
	f := parseFloats(s)
	var pts [][2]float64
	for i := 0; i+1 < len(f); i += 2 {
		pts = append(pts, [2]float64{f[i], f[i+1]})
	}
	return pts
}
