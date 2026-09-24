package mapper

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestGradientAverageAndBackground(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <defs>
    <linearGradient id="bg"><stop offset="0%" stop-color="#000000"/><stop offset="100%" stop-color="#ffffff"/></linearGradient>
    <linearGradient id="card" x2="0%" y2="100%"><stop offset="0" stop-color="#ff0000"/><stop offset="1" stop-color="#ff0000"/></linearGradient>
  </defs>
  <rect x="0" y="0" width="1600" height="900" fill="url(#bg)"/>
  <rect x="10" y="10" width="100" height="100" fill="url(#card)"/>
</svg>`)
	var page, card bool
	for _, r := range reqs {
		if u := r.UpdatePageProperties; u != nil {
			c := u.PageProperties.PageBackgroundFill.SolidFill.Color.RgbColor
			if !near(c.Red, 0.5) || !near(c.Green, 0.5) {
				t.Errorf("page background = %+v, want mid gray", c)
			}
			page = true
		}
		if u := r.UpdateShapeProperties; u != nil && u.ShapeProperties.ShapeBackgroundFill.SolidFill != nil {
			c := u.ShapeProperties.ShapeBackgroundFill.SolidFill.Color.RgbColor
			if !near(c.Red, 1) || !near(c.Green, 0) {
				t.Errorf("card fill = %+v, want red", c)
			}
			card = true
		}
	}
	if !page || !card {
		t.Fatalf("page background set=%v, card filled=%v", page, card)
	}
	if n := len(createShapes(reqs)); n != 1 {
		t.Errorf("got %d shapes, want 1 (background absorbed)", n)
	}
}

func TestInheritedFillAndGroupOpacity(t *testing.T) {
	reqs, _ := mapSVG(t, `
<svg viewBox="0 0 1600 900">
  <g fill="#00ff00" opacity="0.5"><rect x="10" y="10" width="100" height="100" fill-opacity="0.5"/></g>
  <text x="10" y="200" fill="#ffffff" opacity="0.25">x</text>
</svg>`)
	for _, r := range reqs {
		if u := r.UpdateShapeProperties; u != nil && u.ShapeProperties.ShapeBackgroundFill != nil {
			sf := u.ShapeProperties.ShapeBackgroundFill.SolidFill
			if sf == nil || !near(sf.Color.RgbColor.Green, 1) || !near(sf.Alpha, 0.25) {
				t.Errorf("rect fill = %+v, want green alpha 0.25", sf)
			}
		}
		if u := r.UpdateTextStyle; u != nil && u.Style.ForegroundColor != nil {
			// White at 25% over the default white page stays white.
			if c := u.Style.ForegroundColor.OpaqueColor.RgbColor; !near(c.Red, 1) {
				t.Errorf("text colour = %+v", c)
			}
		}
	}
}

func TestParseRGBA(t *testing.T) {
	for in, want := range map[string]rgba{
		"#f00":             {1, 0, 0, 1},
		"rgb(0, 128, 255)": {0, 128.0 / 255, 1, 1},
		"rgba(0,0,0,0.5)":  {0, 0, 0, 0.5},
		"#00000080":        {0, 0, 0, 128.0 / 255},
		"grey":             {128.0 / 255, 128.0 / 255, 128.0 / 255, 1},
	} {
		got, ok := parseRGBA(in)
		if !ok || !near(got.r, want.r) || !near(got.g, want.g) || !near(got.b, want.b) || !near(got.a, want.a) {
			t.Errorf("parseRGBA(%q) = %+v,%v want %+v", in, got, ok, want)
		}
	}
}
