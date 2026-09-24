package mapper

import (
	"strings"
	"testing"

	"google.golang.org/api/slides/v1"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

func mapSVGCfg(t *testing.T, doc string, connect bool) []*slides.Request {
	t.Helper()
	root, err := svgpkg.Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	m := New(Config{SlideID: "s", Scale: testScale, ViewBox: svgpkg.ViewBox{W: 1600, H: 900}, ConnectCurves: connect}, svgpkg.ParseStylesheet(""))
	reqs, _ := m.Map(root)
	return reqs
}

func shapeIDs(reqs []*slides.Request, typ string) []string {
	var ids []string
	for _, r := range reqs {
		if r.CreateShape != nil && r.CreateShape.ShapeType == typ {
			ids = append(ids, r.CreateShape.ObjectId)
		}
	}
	return ids
}

func TestTextWrittenIntoItsBox(t *testing.T) {
	// Generous padding: the block goes inside the rectangle.
	reqs := mapSVGCfg(t, `<svg viewBox="0 0 1600 900">
  <rect x="100" y="100" width="400" height="200" fill="#EEE"/>
  <text x="200" y="190" font-size="20">Inside</text>
  <text x="200" y="215" font-size="14">second line</text>
</svg>`, false)
	if n := len(shapeIDs(reqs, "TEXT_BOX")); n != 0 {
		t.Errorf("%d text boxes, want the text inside the rectangle", n)
	}
	rect := shapeIDs(reqs, "RECTANGLE")[0]
	var inserted, middle bool
	for _, r := range reqs {
		if r.InsertText != nil && r.InsertText.ObjectId == rect && r.InsertText.Text == "Inside\nsecond line" {
			inserted = true
		}
		if u := r.UpdateShapeProperties; u != nil && u.ObjectId == rect && u.ShapeProperties.ContentAlignment == "MIDDLE" {
			middle = true
		}
	}
	if !inserted || !middle {
		t.Errorf("inserted=%v middle=%v, want the block in the rectangle, vertically centred", inserted, middle)
	}
}

func TestTightBoxGroupedWithTexts(t *testing.T) {
	// Padding narrower than Slides' inset and two blocks: the box is
	// grouped with its texts and its badge.
	reqs := mapSVGCfg(t, `<svg viewBox="0 0 1600 900">
  <rect x="100" y="100" width="400" height="60" fill="#FFF" stroke="#999"/>
  <rect x="104" y="104" width="60" height="16" fill="#123"/>
  <text x="170" y="116" font-size="11">Title next to the badge</text>
  <text x="102" y="150" font-size="11">A description line that runs along the whole box width ok</text>
</svg>`, false)
	var groups [][]string
	for _, r := range reqs {
		if r.GroupObjects != nil {
			groups = append(groups, r.GroupObjects.ChildrenObjectIds)
		}
	}
	if len(groups) != 1 || len(groups[0]) != 4 {
		t.Fatalf("groups = %v, want one group: box, badge, two texts", groups)
	}
}

func TestContainerNotGroupedOverItsContent(t *testing.T) {
	// A big frame with a title and boxes drawn on top: grouping it would
	// move the frame above the boxes.
	reqs := mapSVGCfg(t, `<svg viewBox="0 0 1600 900">
  <rect x="100" y="100" width="800" height="600" fill="#F4F5F7"/>
  <text x="102" y="112" font-size="11">Frame title too close to the edge</text>
  <rect x="200" y="200" width="100" height="50" fill="#FFF"/>
  <rect x="400" y="200" width="100" height="50" fill="#FFF"/>
</svg>`, false)
	for _, r := range reqs {
		if r.GroupObjects != nil {
			t.Errorf("unexpected group %v", r.GroupObjects.ChildrenObjectIds)
		}
	}
}

func connections(reqs []*slides.Request) map[string][2]string {
	out := map[string][2]string{}
	for _, r := range reqs {
		if u := r.UpdateLineProperties; u != nil && (u.LineProperties.StartConnection != nil || u.LineProperties.EndConnection != nil) {
			c := out[u.ObjectId]
			if s := u.LineProperties.StartConnection; s != nil {
				c[0] = s.ConnectedObjectId + "#" + string(rune('0'+s.ConnectionSiteIndex))
			}
			if e := u.LineProperties.EndConnection; e != nil {
				c[1] = e.ConnectedObjectId + "#" + string(rune('0'+e.ConnectionSiteIndex))
			}
			out[u.ObjectId] = c
		}
	}
	return out
}

func TestLineAttachedWhenOnConnectionSites(t *testing.T) {
	reqs := mapSVGCfg(t, `<svg viewBox="0 0 1600 900">
  <line x1="510" y1="320" x2="510" y2="355" stroke="#0CC" stroke-width="3" marker-end="url(#a)"/>
  <rect x="40" y="95" width="940" height="225" fill="#EFF"/>
  <rect x="40" y="360" width="940" height="230" fill="#EEE"/>
  <line x1="600" y1="500" x2="700" y2="540" stroke="#000"/>
</svg>`, false)
	rects := shapeIDs(reqs, "RECTANGLE")
	conns := connections(reqs)
	if len(conns) != 1 {
		t.Fatalf("connections = %v, want only the first line attached", conns)
	}
	for _, c := range conns {
		if c[0] != rects[0]+"#2" || c[1] != rects[1]+"#0" {
			t.Errorf("connection = %v, want bottom of %s → top of %s", c, rects[0], rects[1])
		}
	}
}

func TestConnectCurvesPlantUMLLink(t *testing.T) {
	doc := `<svg viewBox="0 0 1600 900">
  <g class="entity" id="ent1"><rect x="100" y="100" width="200" height="100" fill="#16B"/></g>
  <g class="entity" id="ent2"><rect x="500" y="400" width="200" height="100" fill="#16B"/></g>
  <g class="link" data-entity-1="ent1" data-entity-2="ent2" id="lnk1">
    <path d="M300,150 C400,150 450,300 600,396" fill="none" style="stroke:#666;stroke-width:1"/>
    <polygon fill="#666" points="600,400,596,390,604,390,600,400"/>
    <text x="420" y="260" font-size="12">label</text>
  </g>
</svg>`
	reqs := mapSVGCfg(t, doc, true)
	var lines []*slides.CreateLineRequest
	for _, r := range reqs {
		if r.CreateLine != nil {
			lines = append(lines, r.CreateLine)
		}
	}
	if len(lines) != 1 || lines[0].Category != "CURVED" {
		t.Fatalf("lines = %d, want one CURVED connector", len(lines))
	}
	if n := len(shapeIDs(reqs, "TRIANGLE")); n != 0 {
		t.Errorf("arrowhead polygon kept (%d triangles), want it on the connector", n)
	}
	rects := shapeIDs(reqs, "RECTANGLE")
	c := connections(reqs)[lines[0].ObjectId]
	if c[0] != rects[0]+"#3" || c[1] != rects[1]+"#0" {
		t.Errorf("connection = %v, want right of ent1 → top of ent2", c)
	}
	arrow := false
	for _, r := range reqs {
		if u := r.UpdateLineProperties; u != nil && u.ObjectId == lines[0].ObjectId && u.LineProperties.EndArrow == "FILL_ARROW" {
			arrow = true
		}
	}
	if !arrow {
		t.Error("no end arrow on the connector")
	}
	// Without the option the path is drawn as before.
	if n := len(connections(mapSVGCfg(t, doc, false))); n != 0 {
		t.Errorf("%d connections without -connect-curves, want 0", n)
	}
}
