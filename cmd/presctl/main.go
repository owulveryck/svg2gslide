// Command presctl offers small maintenance operations on a presentation:
// listing/dumping slides, deleting slides created by svg2gslide, fetching
// thumbnails and exporting the deck as PDF.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/gslide"
)

func main() {
	var (
		presentation = flag.String("presentation", "", "presentation ID (required)")
		credentials  = flag.String("credentials", os.Getenv("SLIDES_CREDENTIALS"), "OAuth client or service account JSON")
		deleteSlide  = flag.String("delete-slide", "", "comma-separated object IDs of slides to delete")
		exportPDF    = flag.String("export-pdf", "", "export the presentation as PDF to this path")
		list         = flag.Bool("list", false, "list slides with element counts")
		dump         = flag.String("dump", "", "dump the structure of this slide (object ID) as text")
		dumpJSON     = flag.Bool("json", false, "with -dump: raw JSON of the page")
		thumbnail    = flag.String("thumbnail", "", "with -dump: also download the slide thumbnail to this path")
	)
	flag.Parse()
	if *presentation == "" || (*deleteSlide == "" && *exportPDF == "" && !*list && *dump == "") {
		flag.Usage()
		os.Exit(2)
	}
	ctx := context.Background()
	client, err := gslide.NewClient(ctx, *credentials)
	if err != nil {
		fail(err)
	}
	if *list {
		pres, err := client.Slides.Presentations.Get(*presentation).Context(ctx).Do()
		if err != nil {
			fail(err)
		}
		for i, s := range pres.Slides {
			c := count(s.PageElements)
			fmt.Printf("%2d %-24s elements=%d textboxes=%d shapes=%d lines=%d images=%d groups=%d\n",
				i+1, s.ObjectId, c.total, c.textBoxes, c.shapes, c.lines, c.images, c.groups)
		}
	}
	if *dump != "" {
		page, err := client.Slides.Presentations.Pages.Get(*presentation, *dump).Context(ctx).Do()
		if err != nil {
			fail(err)
		}
		if *dumpJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", " ")
			_ = enc.Encode(page)
		} else {
			c := count(page.PageElements)
			fmt.Printf("slide %s: elements=%d textboxes=%d shapes=%d lines=%d images=%d groups=%d\n",
				*dump, c.total, c.textBoxes, c.shapes, c.lines, c.images, c.groups)
			dumpElements(page.PageElements, "")
		}
		if *thumbnail != "" {
			if err := client.FetchSlideThumbnail(ctx, *presentation, *dump, *thumbnail); err != nil {
				fail(err)
			}
		}
	}
	if *deleteSlide != "" {
		var reqs []*slides.Request
		for _, id := range strings.Split(*deleteSlide, ",") {
			if id = strings.TrimSpace(id); id != "" {
				reqs = append(reqs, &slides.Request{DeleteObject: &slides.DeleteObjectRequest{ObjectId: id}})
			}
		}
		if err := client.BatchUpdate(ctx, *presentation, reqs); err != nil {
			fail(err)
		}
		fmt.Println("deleted slides", *deleteSlide)
	}
	if *exportPDF != "" {
		if err := client.ExportPDF(ctx, *presentation, *exportPDF); err != nil {
			fail(err)
		}
		fmt.Println("pdf:", *exportPDF)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

type counts struct{ total, textBoxes, shapes, lines, images, groups int }

func count(els []*slides.PageElement) counts {
	var c counts
	for _, e := range els {
		c.total++
		switch {
		case e.Shape != nil && e.Shape.ShapeType == "TEXT_BOX":
			c.textBoxes++
		case e.Shape != nil:
			c.shapes++
		case e.Line != nil:
			c.lines++
		case e.Image != nil:
			c.images++
		case e.ElementGroup != nil:
			c.groups++
			sub := count(e.ElementGroup.Children)
			c.total += sub.total
			c.textBoxes += sub.textBoxes
			c.shapes += sub.shapes
			c.lines += sub.lines
			c.images += sub.images
			c.groups += sub.groups
		}
	}
	return c
}

const emuPerPt = 12700.0

// dumpElements prints one line per element: kind, box in points, and the
// text content (paragraphs joined with " ⏎ ").
func dumpElements(els []*slides.PageElement, indent string) {
	for _, e := range els {
		x, y, w, h := box(e)
		kind := "?"
		switch {
		case e.Shape != nil:
			kind = e.Shape.ShapeType
		case e.Line != nil:
			kind = "LINE/" + e.Line.LineCategory
		case e.Image != nil:
			kind = "IMAGE"
		case e.ElementGroup != nil:
			kind = "GROUP"
		}
		txt := ""
		if e.Shape != nil && e.Shape.Text != nil {
			txt = textOf(e.Shape.Text)
		}
		fmt.Printf("%s%-22s x=%6.1f y=%6.1f w=%6.1f h=%6.1f", indent, kind, x, y, w, h)
		if txt != "" {
			fmt.Printf("  %q", txt)
		}
		if e.Line != nil && e.Line.LineProperties != nil {
			lp := e.Line.LineProperties
			if c := lp.StartConnection; c != nil {
				fmt.Printf("  start=%s#%d", c.ConnectedObjectId, c.ConnectionSiteIndex)
			}
			if c := lp.EndConnection; c != nil {
				fmt.Printf("  end=%s#%d", c.ConnectedObjectId, c.ConnectionSiteIndex)
			}
		}
		fmt.Printf("  [%s]\n", e.ObjectId)
		if e.ElementGroup != nil {
			dumpElements(e.ElementGroup.Children, indent+"  ")
		}
	}
}

func box(e *slides.PageElement) (x, y, w, h float64) {
	sx, sy := 1.0, 1.0
	if t := e.Transform; t != nil {
		x, y = t.TranslateX/emuPerPt, t.TranslateY/emuPerPt
		if t.ScaleX != 0 {
			sx = t.ScaleX
		}
		if t.ScaleY != 0 {
			sy = t.ScaleY
		}
	}
	if s := e.Size; s != nil {
		if s.Width != nil {
			w = s.Width.Magnitude * sx / emuPerPt
		}
		if s.Height != nil {
			h = s.Height.Magnitude * sy / emuPerPt
		}
	}
	return
}

func textOf(t *slides.TextContent) string {
	var b strings.Builder
	for _, el := range t.TextElements {
		if el.TextRun != nil {
			b.WriteString(el.TextRun.Content)
		}
	}
	s := strings.TrimSuffix(b.String(), "\n")
	return strings.ReplaceAll(s, "\n", " ⏎ ")
}
