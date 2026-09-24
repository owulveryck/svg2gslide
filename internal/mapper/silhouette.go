package mapper

import (
	"bytes"
	"image"
	_ "image/gif" // decoders for embedded images
	_ "image/jpeg"
	_ "image/png"
	"math"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// Small monochrome icons (PlantUML actor/person glyphs, pictograms) are
// rebuilt from native shapes instead of being inserted as images, which
// would require hosting them on a public URL: each connected blob of
// opaque pixels becomes an ELLIPSE, a ROUND_RECTANGLE or a RECTANGLE
// according to how much of its bounding box it fills.

const (
	silhouetteMaxSide = 160 // px: larger images are real pictures
	silhouetteMaxBlob = 12  // more blobs than this is not a simple icon
)

type blob struct {
	minX, minY, maxX, maxY int
	area                   int
}

// mapSilhouette tries to draw the image data as native shapes in the box
// (x,y,w,h) (SVG user units, before mat). Returns false when the image is
// not a simple monochrome silhouette.
func (m *Mapper) mapSilhouette(data []byte, x, y, w, h float64, mat svgpkg.Matrix) bool {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return false
	}
	b := img.Bounds()
	W, H := b.Dx(), b.Dy()
	if W == 0 || H == 0 || W > silhouetteMaxSide || H > silhouetteMaxSide {
		return false
	}
	// Opaque mask and mean colour; reject multicolour images.
	mask := make([]bool, W*H)
	var sr, sg, sb, n float64
	area := 0
	var sr2, sg2, sb2 float64
	for j := 0; j < H; j++ {
		for i := 0; i < W; i++ {
			r, g, bb, a := img.At(b.Min.X+i, b.Min.Y+j).RGBA()
			if a < 0x8000 {
				continue
			}
			mask[j*W+i] = true
			area++
			if a < 0xe000 {
				continue // antialiased edges blend in the background colour
			}
			// Un-premultiply.
			fr, fg, fb := float64(r)/float64(a), float64(g)/float64(a), float64(bb)/float64(a)
			sr, sg, sb, n = sr+fr, sg+fg, sb+fb, n+1
			sr2, sg2, sb2 = sr2+fr*fr, sg2+fg*fg, sb2+fb*fb
		}
	}
	if n == 0 || float64(area) > 0.9*float64(W*H) {
		return false // empty, or an opaque picture
	}
	variance := (sr2/n - (sr/n)*(sr/n)) + (sg2/n - (sg/n)*(sg/n)) + (sb2/n - (sb/n)*(sb/n))
	if variance > 0.02 {
		return false
	}
	col := rgba{r: sr / n, g: sg / n, b: sb / n, a: 1}

	blobs := findBlobs(mask, W, H)
	if len(blobs) == 0 || len(blobs) > silhouetteMaxBlob {
		return false
	}
	sx, sy := w/float64(W), h/float64(H)
	var ids []string
	for _, bl := range blobs {
		bw, bh := bl.maxX-bl.minX+1, bl.maxY-bl.minY+1
		if bl.area < 4 {
			continue // antialiasing specks
		}
		fill := float64(bl.area) / float64(bw*bh)
		shape := "ROUND_RECTANGLE"
		switch {
		case fill > 0.95:
			shape = "RECTANGLE"
		case fill < 0.83 && math.Abs(float64(bw-bh)) <= 0.3*float64(max(bw, bh)):
			shape = "ELLIPSE"
		}
		px, py := mat.Apply(x+float64(bl.minX)*sx, y+float64(bl.minY)*sy)
		msx, msy := mat.ScaleFactors()
		ex, ey := m.toEMU(px, py)
		id := m.nextID()
		m.createShape(id, shape, ex, ey, m.lenEMU(float64(bw)*sx*msx), m.lenEMU(float64(bh)*sy*msy), 0)
		m.reqs = append(m.reqs, &slides.Request{UpdateShapeProperties: &slides.UpdateShapePropertiesRequest{
			ObjectId: id,
			ShapeProperties: &slides.ShapeProperties{
				ShapeBackgroundFill: &slides.ShapeBackgroundFill{SolidFill: &slides.SolidFill{Color: col.opaque(), Alpha: 1}},
				Outline:             &slides.Outline{PropertyState: "NOT_RENDERED"},
			},
			Fields: "shapeBackgroundFill.solidFill,outline.propertyState",
		}})
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return false
	}
	if len(ids) > 1 {
		m.reqs = append(m.reqs, &slides.Request{GroupObjects: &slides.GroupObjectsRequest{
			GroupObjectId: m.nextID(), ChildrenObjectIds: ids,
		}})
	}
	return true
}

// findBlobs labels the 4-connected components of the mask.
func findBlobs(mask []bool, W, H int) []blob {
	seen := make([]bool, len(mask))
	var out []blob
	var stack []int
	for start := range mask {
		if !mask[start] || seen[start] {
			continue
		}
		bl := blob{minX: W, minY: H, maxX: -1, maxY: -1}
		stack = append(stack[:0], start)
		seen[start] = true
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			i, j := p%W, p/W
			bl.area++
			bl.minX, bl.maxX = min(bl.minX, i), max(bl.maxX, i)
			bl.minY, bl.maxY = min(bl.minY, j), max(bl.maxY, j)
			for _, q := range [4][2]int{{i - 1, j}, {i + 1, j}, {i, j - 1}, {i, j + 1}} {
				if q[0] < 0 || q[1] < 0 || q[0] >= W || q[1] >= H {
					continue
				}
				k := q[1]*W + q[0]
				if mask[k] && !seen[k] {
					seen[k] = true
					stack = append(stack, k)
				}
			}
		}
		out = append(out, bl)
	}
	return out
}
