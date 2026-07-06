// Package svg parses SVG documents into a lightweight element tree and
// evaluates the subset of CSS used to drive per-phase visibility.
package svg

import (
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Element is a node of the SVG document tree.
type Element struct {
	Tag      string
	Attrs    map[string]string
	Children []*Element
	Parent   *Element

	// seq holds character data (string) and child elements (*Element) in
	// document order, so mixed content like "<tspan>a</tspan> b" keeps its
	// original text ordering.
	seq []any
}

// Parse reads an SVG document and returns its root element.
func Parse(r io.Reader) (*Element, error) {
	dec := xml.NewDecoder(r)
	var root, cur *Element
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml parse error: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := &Element{Tag: t.Name.Local, Attrs: make(map[string]string, len(t.Attr)), Parent: cur}
			for _, a := range t.Attr {
				el.Attrs[a.Name.Local] = a.Value
			}
			if cur == nil {
				root = el
			} else {
				cur.Children = append(cur.Children, el)
				cur.seq = append(cur.seq, el)
			}
			cur = el
		case xml.EndElement:
			if cur != nil {
				cur = cur.Parent
			}
		case xml.CharData:
			if cur != nil {
				s := string(t)
				if s != "" {
					cur.seq = append(cur.seq, s)
				}
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("no root element found")
	}
	return root, nil
}

// Attr returns the attribute value or "" if absent.
func (e *Element) Attr(name string) string { return e.Attrs[name] }

// FloatAttr returns the attribute parsed as float64, or def if absent/invalid.
func (e *Element) FloatAttr(name string, def float64) float64 {
	v, ok := e.Attrs[name]
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return def
	}
	return f
}

// Classes returns the space-separated class list of the element.
func (e *Element) Classes() []string { return strings.Fields(e.Attrs["class"]) }

// HasClass reports whether the element carries the given class.
func (e *Element) HasClass(c string) bool {
	for _, x := range e.Classes() {
		if x == c {
			return true
		}
	}
	return false
}

// TextContent returns the concatenated character data of the element and its
// descendants, in document order, with surrounding whitespace trimmed and
// internal whitespace collapsed.
func (e *Element) TextContent() string {
	var b strings.Builder
	e.appendText(&b)
	return strings.Join(strings.Fields(b.String()), " ")
}

// RawTextContent returns the concatenated character data without whitespace
// normalization (used for <style> blocks).
func (e *Element) RawTextContent() string {
	var b strings.Builder
	e.appendText(&b)
	return b.String()
}

func (e *Element) appendText(b *strings.Builder) {
	for _, item := range e.seq {
		switch v := item.(type) {
		case string:
			b.WriteString(v)
		case *Element:
			v.appendText(b)
		}
	}
}

// Find returns the first descendant (depth-first) with the given tag.
func (e *Element) Find(tag string) *Element {
	for _, c := range e.Children {
		if c.Tag == tag {
			return c
		}
		if f := c.Find(tag); f != nil {
			return f
		}
	}
	return nil
}
