package mapper

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// ElementOrigin records which SVG node produced a Slides object. It is what
// makes a later sync actionable: an edit or a comment found on a page element
// can be traced back to the source node to fix, instead of to an opaque
// object ID.
type ElementOrigin struct {
	ObjectID string // Slides page element object ID
	Key      string // identity the ID derives from: "#id" or the positional locator
	Locator  string // positional locator, always set
	SVGID    string // id attribute of the source node, "" when it has none
	Tag      string // SVG tag that produced the object
}

// Origins returns the provenance of every object created so far, in creation
// order.
func (m *Mapper) Origins() []ElementOrigin { return m.origins }

// idFor returns the object ID for one Slides object produced by e.
//
// The ID derives from the element's identity rather than from a counter, so
// editing or inserting a node elsewhere in the document leaves every other
// object's ID untouched — which is what lets comment anchors from a previous
// sync still resolve. An element that yields several objects (a path split
// into shapes, a text turned into a box plus runs) gets a "-N" suffix in
// creation order, which is deterministic for a given document.
func (m *Mapper) idFor(e *svgpkg.Element) string {
	key := svgpkg.Key(e)
	sum := sha256.Sum256([]byte(key))
	base := m.cfg.SlideID + "_" + hex.EncodeToString(sum[:4])
	id := m.dedup(base)
	m.origins = append(m.origins, ElementOrigin{
		ObjectID: id,
		Key:      key,
		Locator:  svgpkg.Locator(e),
		SVGID:    e.ID(),
		Tag:      e.Tag,
	})
	return id
}

// groupIDFor returns the object ID of a group assembled from objects that do
// not all come from one SVG node. It derives from the members, so the group
// keeps its ID as long as its contents do.
func (m *Mapper) groupIDFor(childIDs []string) string {
	sum := sha256.Sum256([]byte(strings.Join(childIDs, "\x00")))
	return m.dedup(m.cfg.SlideID + "_g" + hex.EncodeToString(sum[:3]))
}

// dedup makes base unique within the slide, suffixing "-N" on reuse. It
// absorbs both an element legitimately yielding several objects and the rare
// hash collision between two distinct keys.
func (m *Mapper) dedup(base string) string {
	n := m.idSeq[base]
	m.idSeq[base]++
	if n == 0 {
		return base
	}
	return base + "-" + strconv.Itoa(n)
}
