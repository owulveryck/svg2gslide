package mapper

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
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
	// Parts names the nodes whose text was merged into this object, each with
	// the range it occupies in it. A shape that swallowed the labels drawn on
	// it holds the text of nodes it is not, and so does a group of lines: the
	// object alone answers "which box", the parts answer "which word".
	// Empty when the object's text comes from the node it is.
	Parts []TextPart
}

// TextPart is one source node's contribution to an object's text.
type TextPart struct {
	// Start and End delimit the contribution in the object's text, counted in
	// UTF-16 code units the way the Slides API counts them — the same units a
	// comment anchor's range uses.
	Start, End int
	Locator    string
	SVGID      string
	Tag        string
	Text       string
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

// addTextParts records where the text written into an object came from.
//
// Nothing is recorded when every part is the object's own node: there the
// object already is the answer, and parts would only repeat it.
func (m *Mapper) addTextParts(objectID string, parts []TextPart) {
	for i := range m.origins {
		o := &m.origins[i]
		if o.ObjectID != objectID {
			continue
		}
		if slices.ContainsFunc(parts, func(p TextPart) bool { return p.Locator != o.Locator }) {
			o.Parts = parts
		}
		return
	}
}

// partOf builds the part a run of text contributes, from the node that carries
// it.
func partOf(e *svgpkg.Element, start, end int, text string) TextPart {
	return TextPart{
		Start:   start,
		End:     end,
		Locator: svgpkg.Locator(e),
		SVGID:   e.ID(),
		Tag:     e.Tag,
		Text:    text,
	}
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
