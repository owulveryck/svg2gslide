package mapper

import (
	"strconv"
	"strings"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// styleShape applies fill and outline from the (inherited) SVG presentation
// attributes, with gradients reduced to a solid colour and group opacity
// folded into the alpha channels.
func (m *Mapper) styleShape(id string, e *svgpkg.Element, mat svgpkg.Matrix) {
	if m.elemShape != nil && m.elemShape[e] == "" {
		m.elemShape[e] = id
	}
	props := &slides.ShapeProperties{}
	var fields []string
	op := m.opacity(e)

	c, ok := m.fillPaint(e)
	alpha := c.a * e.InheritedFloat("fill-opacity", 1) * op
	if ok && alpha > 0.005 {
		props.ShapeBackgroundFill = &slides.ShapeBackgroundFill{
			SolidFill: &slides.SolidFill{Color: c.opaque(), Alpha: alpha},
		}
		fields = append(fields, "shapeBackgroundFill.solidFill")
	} else {
		props.ShapeBackgroundFill = &slides.ShapeBackgroundFill{PropertyState: "NOT_RENDERED"}
		fields = append(fields, "shapeBackgroundFill.propertyState")
	}

	if sc, salpha, ok := m.strokePaint(e); ok {
		props.Outline = &slides.Outline{
			OutlineFill: &slides.OutlineFill{SolidFill: &slides.SolidFill{Color: sc.opaque(), Alpha: salpha}},
			Weight:      &slides.Dimension{Magnitude: m.lenEMU(e.InheritedFloat("stroke-width", 1) * avgScale(mat)), Unit: "EMU"},
		}
		fields = append(fields, "outline.outlineFill.solidFill", "outline.weight")
		if dash := e.Inherited("stroke-dasharray"); dash != "" && dash != "none" {
			props.Outline.DashStyle = dashStyle(dash)
			fields = append(fields, "outline.dashStyle")
		}
	} else {
		props.Outline = &slides.Outline{PropertyState: "NOT_RENDERED"}
		fields = append(fields, "outline.propertyState")
	}

	m.reqs = append(m.reqs, &slides.Request{UpdateShapeProperties: &slides.UpdateShapePropertiesRequest{
		ObjectId:        id,
		ShapeProperties: props,
		Fields:          strings.Join(fields, ","),
	}})
}

// strokePaint resolves the effective stroke colour and alpha of e.
func (m *Mapper) strokePaint(e *svgpkg.Element) (rgba, float64, bool) {
	s := e.Inherited("stroke")
	if s == "" || s == "none" || e.InheritedFloat("stroke-width", 1) <= 0 {
		return rgba{}, 0, false
	}
	c, ok := m.paint(s)
	if !ok {
		return rgba{}, 0, false
	}
	alpha := c.a * e.InheritedFloat("stroke-opacity", 1) * m.opacity(e)
	if alpha <= 0.005 {
		return rgba{}, 0, false
	}
	return c, alpha, true
}

// isFilled reports whether the element paints its interior.
func (m *Mapper) isFilled(e *svgpkg.Element) bool {
	c, ok := m.fillPaint(e)
	return ok && c.a*e.InheritedFloat("fill-opacity", 1) > 0.01
}

// dashStyle maps a dasharray onto the closest Slides dash preset.
func dashStyle(dash string) string {
	f := strings.FieldsFunc(dash, func(r rune) bool { return r == ',' || r == ' ' })
	if len(f) >= 2 {
		a, _ := strconv.ParseFloat(f[0], 64)
		b, _ := strconv.ParseFloat(f[1], 64)
		if a > 0 && a <= 2.5 && b > 0 {
			return "DOT"
		}
	}
	return "DASH"
}

// absorbBackground composites a full-page rectangle into the page
// background colour.
func (m *Mapper) absorbBackground(e *svgpkg.Element) {
	c, ok := m.fillPaint(e)
	if !ok {
		return
	}
	a := c.a * e.InheritedFloat("fill-opacity", 1) * m.opacity(e)
	if g := m.gradientRef(e.Inherited("fill")); g != nil && g.radial {
		m.warnf("dégradé radial pleine page approximé par une teinte uniforme")
	}
	m.pageBG = c.over(m.pageBG, a)
	m.bgSet = true
}

// textColor resolves the text fill as an opaque colour: Slides text has no
// alpha, so transparency is flattened against the page background.
func (m *Mapper) textColor(e *svgpkg.Element) rgba {
	c, ok := m.fillPaint(e)
	if !ok {
		return m.pageBG
	}
	a := c.a * e.InheritedFloat("fill-opacity", 1) * m.opacity(e)
	return c.over(m.pageBG, a)
}
