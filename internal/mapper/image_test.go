package mapper

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// personIcon draws a white "person" glyph (head disc + rounded body) on a
// transparent 52×52 canvas, like the PlantUML C4 actor icon.
func personIcon(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 52, 52))
	for y := 0; y < 52; y++ {
		for x := 0; x < 52; x++ {
			dx, dy := float64(x)-25.5, float64(y)-12.5
			head := dx*dx+dy*dy <= 11*11
			body := inRoundRect(float64(x), float64(y), 6, 29, 45, 48, 9)
			if head || body {
				img.Set(x, y, color.NRGBA{255, 255, 255, 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestEmbeddedIconRedrawnNatively(t *testing.T) {
	reqs, _ := mapSVG(t, `<svg viewBox="0 0 1600 900" xmlns:xlink="http://www.w3.org/1999/xlink">
  <image x="100" y="100" width="52" height="52" xlink:href="`+personIcon(t)+`"/>
</svg>`)
	shapes := map[string]int{}
	groups := 0
	for _, r := range reqs {
		if r.CreateShape != nil {
			shapes[r.CreateShape.ShapeType]++
		}
		if r.CreateImage != nil {
			t.Errorf("icon inserted as an image, want native shapes")
		}
		if r.GroupObjects != nil {
			groups++
		}
	}
	if shapes["ELLIPSE"] != 1 || shapes["ROUND_RECTANGLE"] != 1 || groups != 1 {
		t.Errorf("shapes = %v, groups = %d; want head ELLIPSE + body ROUND_RECTANGLE grouped", shapes, groups)
	}
}

func TestPhotoImageIsEmbedded(t *testing.T) {
	// A multicolour image is not a silhouette: it stays an image with a
	// placeholder URL for the caller to host.
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 37)
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	reqs, _ := mapSVG(t, `<svg viewBox="0 0 1600 900"><image x="0" y="0" width="80" height="80" href="`+uri+`"/></svg>`)
	var urls []string
	for _, r := range reqs {
		if r.CreateImage != nil {
			urls = append(urls, r.CreateImage.Url)
		}
	}
	if len(urls) != 1 || urls[0] != ImagePlaceholderPrefix+"0" {
		t.Errorf("CreateImage urls = %v, want one placeholder", urls)
	}
}

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := min(max(x, x0+r), x1-r)
	cy := min(max(y, y0+r), y1-r)
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}
