package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/state"
)

const emuPerPt = 12700.0

// Fingerprint projects a slide as the server returns it into a comparable
// shape: the text it holds, in document order, and how many elements it has.
//
// Geometry is deliberately left out unless asked for. Slides normalizes
// positions and autofits text, so a pushed shape rarely reads back at exactly
// the coordinates it was sent, and comparing them by default would report
// edits nobody made.
func Fingerprint(page *slides.Page, withGeometry bool) state.Fingerprint {
	var (
		fp   state.Fingerprint
		geom strings.Builder
	)
	var walk func(els []*slides.PageElement)
	walk = func(els []*slides.PageElement) {
		for _, e := range els {
			fp.Elements++
			if withGeometry {
				x, y, w, h := box(e)
				fmt.Fprintf(&geom, "%s:%.0f,%.0f,%.0f,%.0f\n", kindOf(e), x, y, w, h)
			}
			if e.Shape != nil && e.Shape.Text != nil {
				if t := NormalizeText(e.Shape.Text); t != "" {
					fp.Texts = append(fp.Texts, t)
				}
			}
			if e.ElementGroup != nil {
				walk(e.ElementGroup.Children)
			}
		}
	}
	walk(page.PageElements)

	if withGeometry {
		sum := sha256.Sum256([]byte(geom.String()))
		fp.Geometry = hex.EncodeToString(sum[:8])
	}
	return fp
}

// NormalizeText renders a shape's text the way the fingerprint compares it:
// runs joined, whitespace collapsed, ends trimmed. Slides stores a trailing
// newline on every text box and may re-wrap paragraphs, neither of which is
// an edit.
func NormalizeText(t *slides.TextContent) string {
	var b strings.Builder
	for _, el := range t.TextElements {
		if el.TextRun != nil {
			b.WriteString(el.TextRun.Content)
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// TextOf returns the text of a page element, or "" when it holds none.
func TextOf(e *slides.PageElement) string {
	if e.Shape == nil || e.Shape.Text == nil {
		return ""
	}
	return NormalizeText(e.Shape.Text)
}

// Elements flattens a page's elements, groups included, in document order.
func Elements(page *slides.Page) []*slides.PageElement {
	var out []*slides.PageElement
	var walk func(els []*slides.PageElement)
	walk = func(els []*slides.PageElement) {
		for _, e := range els {
			out = append(out, e)
			if e.ElementGroup != nil {
				walk(e.ElementGroup.Children)
			}
		}
	}
	walk(page.PageElements)
	return out
}

func kindOf(e *slides.PageElement) string {
	switch {
	case e.Shape != nil:
		return e.Shape.ShapeType
	case e.Line != nil:
		return "LINE"
	case e.Image != nil:
		return "IMAGE"
	case e.ElementGroup != nil:
		return "GROUP"
	}
	return "?"
}

// box returns the element's position and size in points, folding the
// transform's scale into the size the way the element actually renders.
func box(e *slides.PageElement) (x, y, w, h float64) {
	sx, sy := 1.0, 1.0
	if t := e.Transform; t != nil {
		x, y = t.TranslateX/emuPerPt, t.TranslateY/emuPerPt
		if t.ScaleX != 0 {
			sx = t.ScaleX
		}
		if t.ScaleY != 0 {
			sy = t.ScaleY
		}
	}
	if s := e.Size; s != nil {
		if s.Width != nil {
			w = s.Width.Magnitude * sx / emuPerPt
		}
		if s.Height != nil {
			h = s.Height.Magnitude * sy / emuPerPt
		}
	}
	return
}
