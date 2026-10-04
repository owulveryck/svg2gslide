package svg

import (
	"strconv"
	"strings"
)

// Locator returns a document-order path identifying e, of the form
// "/svg/g[2]/rect[1]". Each index is the element's 1-based rank among its
// siblings carrying the same tag, so inserting or removing an element of a
// different tag — or anything outside e's ancestor chain — leaves the path
// unchanged.
//
// It returns "" for a nil element.
func Locator(e *Element) string {
	if e == nil {
		return ""
	}
	// Collect the segments leaf-to-root, then emit them in document order.
	var segs []string
	for cur := e; cur != nil; cur = cur.Parent {
		segs = append(segs, segment(cur))
	}
	var b strings.Builder
	for i := len(segs) - 1; i >= 0; i-- {
		b.WriteByte('/')
		b.WriteString(segs[i])
	}
	return b.String()
}

// segment renders one path step: the tag, suffixed with a 1-based index when
// the parent holds several children of that tag.
func segment(e *Element) string {
	if e.Parent == nil {
		return e.Tag
	}
	rank, total := 0, 0
	for _, sib := range e.Parent.Children {
		if sib.Tag != e.Tag {
			continue
		}
		total++
		if sib == e {
			rank = total
		}
	}
	if total <= 1 {
		return e.Tag
	}
	if rank == 0 {
		// e is not among its parent's children: a detached or synthesized
		// node. Mark it rather than claim a position it does not hold.
		return e.Tag + "[?]"
	}
	return e.Tag + "[" + strconv.Itoa(rank) + "]"
}

// Key returns the stable identity of e used to derive its Google Slides
// object ID: the id attribute when it has one, otherwise the positional
// Locator.
//
// An id is strongly preferred: it survives the element moving within the
// document, so a comment anchored to the resulting Slides object still
// resolves back to the same source node after the SVG is restructured. A
// positional locator only survives edits that leave the ancestor chain and
// the same-tag sibling ranks intact.
func Key(e *Element) string {
	if e == nil {
		return ""
	}
	if id := e.ID(); id != "" {
		return "#" + id
	}
	return Locator(e)
}
