package htmlsvg

import (
	"strings"
	"testing"

	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

const deck = `<!DOCTYPE html>
<html><head><style>.slide > svg { width: 100vw }</style></head>
<body>
<section class="slide image" data-n="1"><div class="title">Intro</div>
<svg viewBox="0 0 1920 1080">
  <defs><linearGradient id="g"><stop offset="0" stop-color="#000"/></linearGradient></defs>
  <style>.a > .b { fill: red }</style>
  <rect width="1920" height="1080" fill="url(#g)"/>
  <text x="10" y="20">Caf&eacute;&nbsp;&amp; co <tspan>&#8594;</tspan></text>
  <image xlink:href="data:image/png;base64,AAAA" width="1" height="1"/>
</svg>
</section>
<section class="slide"><h1>Second</h1><svg viewBox="0 0 10 10"><circle r="1"></circle></svg></section>
<div id="bar"><button><svg viewBox="0 0 24 24"><path d="M0 0"/></svg></button></div>
<svg viewBox="0 0 24 24"><path d="M1 1"/></svg>
</body></html>`

func TestExtract(t *testing.T) {
	if !IsHTML([]byte("\n  " + deck)) {
		t.Fatal("IsHTML(deck) = false")
	}
	if IsHTML([]byte(`<?xml version="1.0"?><svg/>`)) {
		t.Fatal("IsHTML(svg) = true")
	}

	svgs, err := Extract(strings.NewReader(deck))
	if err != nil {
		t.Fatal(err)
	}
	if len(svgs) != 2 {
		t.Fatalf("got %d svgs, want 2 (only those inside slides)", len(svgs))
	}
	if svgs[0].Title != "Intro" || svgs[1].Title != "Second" || svgs[1].Index != 2 {
		t.Fatalf("titles/indexes = %q,%q,%d", svgs[0].Title, svgs[1].Title, svgs[1].Index)
	}

	root, err := svgpkg.Parse(strings.NewReader(string(svgs[0].Data)))
	if err != nil {
		t.Fatalf("re-parsing extracted svg: %v\n%s", err, svgs[0].Data)
	}
	if root.Attr("viewBox") != "0 0 1920 1080" {
		t.Fatalf("viewBox = %q", root.Attr("viewBox"))
	}
	var gradient, style, text, href string
	root.Walk(func(e *svgpkg.Element) {
		switch e.Tag {
		case "linearGradient":
			gradient = e.Attr("id")
		case "style":
			style = e.RawTextContent()
		case "text":
			text = e.RawTextContent()
		case "image":
			href = e.Attr("href")
		}
	})
	if gradient != "g" {
		t.Errorf("linearGradient not preserved (camelCase lost?)")
	}
	if !strings.Contains(style, ".a > .b") {
		t.Errorf("style = %q", style)
	}
	if text != "Café & co →" {
		t.Errorf("text = %q", text)
	}
	if !strings.HasPrefix(href, "data:image/png") {
		t.Errorf("xlink:href = %q", href)
	}
}

func TestExtractWithoutSlides(t *testing.T) {
	svgs, err := Extract(strings.NewReader(`<html><body><p>x</p><svg viewBox="0 0 1 1"><svg><rect/></svg></svg><button><svg/></button></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(svgs) != 1 {
		t.Fatalf("got %d svgs, want 1 (outermost, outside buttons)", len(svgs))
	}
}
