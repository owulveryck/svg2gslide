// Package htmlsvg extracts the inline <svg> elements of an HTML document
// (typically an HTML slide deck, one SVG per slide) as standalone SVG
// documents.
package htmlsvg

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// SVG is one inline SVG, re-serialized as a well-formed XML document.
type SVG struct {
	Index int    // 1-based position among the returned SVGs
	Title string // title of the enclosing slide (heading or .title element), may be ""
	Data  []byte
}

// IsHTML reports whether data looks like an HTML document rather than a
// bare SVG.
func IsHTML(data []byte) bool {
	head := bytes.TrimPrefix(data[:min(len(data), 512)], []byte("\xef\xbb\xbf")) // UTF-8 BOM
	s := strings.ToLower(string(bytes.TrimSpace(head)))
	return strings.HasPrefix(s, "<!doctype html") || strings.HasPrefix(s, "<html")
}

// Extract returns the outermost <svg> elements of the document, in order.
// When some of them sit inside a slide container (<section>, or an element
// with class "slide"), only those are kept: the others are UI chrome
// (button icons, logos).
func Extract(r io.Reader) ([]SVG, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("html parse error: %w", err)
	}
	type found struct {
		node  *html.Node
		slide *html.Node
	}
	var all []found
	var walk func(n, slide *html.Node, inButton bool)
	walk = func(n, slide *html.Node, inButton bool) {
		if n.Type == html.ElementNode {
			if n.Namespace == "svg" && n.Data == "svg" {
				if !inButton {
					all = append(all, found{n, slide})
				}
				return // nested <svg> belong to their outermost one
			}
			if n.Namespace == "" {
				if n.DataAtom == atom.Section || hasClass(n, "slide") {
					slide = n
				}
				inButton = inButton || n.DataAtom == atom.Button
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, slide, inButton)
		}
	}
	walk(doc, nil, false)

	inSlides := all[:0:0]
	for _, f := range all {
		if f.slide != nil {
			inSlides = append(inSlides, f)
		}
	}
	if len(inSlides) > 0 {
		all = inSlides
	}

	out := make([]SVG, 0, len(all))
	for i, f := range all {
		data, err := render(f.node)
		if err != nil {
			return nil, err
		}
		out = append(out, SVG{Index: i + 1, Title: slideTitle(f.slide), Data: data})
	}
	return out, nil
}

// render serializes an <svg> subtree. html.Render escapes all text in
// foreign content (including <style>), restores SVG's camelCase names and
// writes empty elements as open/close pairs, so the output is valid XML.
func render(n *html.Node) ([]byte, error) {
	root := *n
	root.Parent, root.PrevSibling, root.NextSibling = nil, nil, nil
	root.Attr = append([]html.Attribute(nil), n.Attr...)
	if !hasAttr(&root, "", "xmlns") {
		root.Attr = append(root.Attr, html.Attribute{Key: "xmlns", Val: "http://www.w3.org/2000/svg"})
	}
	if !hasAttr(&root, "xmlns", "xlink") {
		root.Attr = append(root.Attr, html.Attribute{Namespace: "xmlns", Key: "xlink", Val: "http://www.w3.org/1999/xlink"})
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, &root); err != nil {
		return nil, fmt.Errorf("rendering inline svg: %w", err)
	}
	return buf.Bytes(), nil
}

// slideTitle returns the text of the first heading or .title element of the
// slide container, outside any SVG.
func slideTitle(slide *html.Node) string {
	if slide == nil {
		return ""
	}
	var title string
	var walk func(n *html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Namespace == "" {
			switch n.DataAtom {
			case atom.H1, atom.H2, atom.H3:
				title = textContent(n)
			default:
				if hasClass(n, "title") && n != slide {
					title = textContent(n)
				}
			}
			if title != "" {
				return true
			}
		}
		if n.Namespace == "svg" {
			return false
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(slide)
	return title
}

func textContent(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func hasClass(n *html.Node, class string) bool {
	for _, a := range n.Attr {
		if a.Namespace == "" && a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == class {
					return true
				}
			}
		}
	}
	return false
}

func hasAttr(n *html.Node, ns, key string) bool {
	for _, a := range n.Attr {
		if a.Namespace == ns && a.Key == key {
			return true
		}
	}
	return false
}
