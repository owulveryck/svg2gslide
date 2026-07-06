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

// ParsePathD parses a path "d" attribute. Only absolute commands
// (M, L, H, V, Q, C, A, Z) are supported, which covers the target diagrams.
func ParsePathD(d string) ([]PathSeg, error) {
	var segs []PathSeg
	i := 0
	for i < len(d) {
		c := d[i]
		if c == ' ' || c == ',' || c == '\n' || c == '\t' || c == '\r' {
			i++
			continue
		}
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return nil, fmt.Errorf("unexpected character %q in path data at %d", c, i)
		}
		if c >= 'a' && c <= 'z' && c != 'z' {
			return nil, fmt.Errorf("relative path command %q not supported", c)
		}
		i++
		// Collect the numeric arguments up to the next command letter.
		j := i
		for j < len(d) && (d[j] < 'A' || d[j] > 'Z') && (d[j] < 'a' || d[j] > 'z') {
			j++
		}
		args := parseFloats(d[i:j])
		i = j
		op := c
		if op == 'z' {
			op = 'Z'
		}
		segs = append(segs, PathSeg{Op: op, Args: args})
	}
	return segs, nil
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
