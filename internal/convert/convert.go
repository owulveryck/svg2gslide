// Package convert holds the pure SVG → Slides-requests pipeline shared by
// the CLI and the wasm frontend. It performs no I/O beyond reading the
// provided SVG stream.
package convert

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/mapper"
	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

// Input describes one conversion.
type Input struct {
	SVG          io.Reader // SVG document
	Label        string    // used in error messages ("<stdin>", filename, "upload")
	PageW, PageH float64   // presentation page size in EMU
	Phase        string    // "" → use the SVG's data-active-phase attribute
	Verbose      bool      // passed to mapper.Config
}

// Result is the outcome of a conversion, ready for a batchUpdate call.
type Result struct {
	SlideID  string            // "svg2gslide_" + random suffix
	Phase    string            // effective phase actually used
	Requests []*slides.Request // CreateSlide prepended; len ≥ 1
	Warnings []string
}

// Convert parses the SVG and maps it to Slides requests, fit-centered on a
// new blank slide of the given page size.
func Convert(in Input) (*Result, error) {
	root, err := svgpkg.Parse(in.SVG)
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", in.Label, err)
	}
	if root.Tag != "svg" {
		return nil, fmt.Errorf("%s: root element is <%s>, expected <svg>", in.Label, root.Tag)
	}

	vb, err := svgpkg.ParseViewBox(root.Attr("viewBox"))
	if err != nil {
		// Fall back to width/height attributes.
		w, h := root.FloatAttr("width", 0), root.FloatAttr("height", 0)
		if w <= 0 || h <= 0 {
			return nil, fmt.Errorf("%s: no usable viewBox or width/height", in.Label)
		}
		vb = svgpkg.ViewBox{W: w, H: h}
	}

	phase := in.Phase
	if phase == "" {
		phase = root.Attr("data-active-phase")
	}
	var sheet *svgpkg.Stylesheet
	if styleEl := root.Find("style"); styleEl != nil {
		sheet = svgpkg.ParseStylesheet(styleEl.RawTextContent())
	} else {
		sheet = svgpkg.ParseStylesheet("")
	}

	scale := min(in.PageW/vb.W, in.PageH/vb.H)
	offX := (in.PageW - vb.W*scale) / 2
	offY := (in.PageH - vb.H*scale) / 2

	slideID := "svg2gslide_" + randomSuffix()
	m := mapper.New(mapper.Config{
		SlideID:    slideID,
		Phase:      phase,
		Scale:      scale,
		OffX:       offX,
		OffY:       offY,
		ViewBox:    vb,
		FontFamily: fontFamily(root),
		Verbose:    in.Verbose,
	}, sheet)
	reqs, warnings := m.Map(root)
	if len(reqs) == 0 {
		return nil, fmt.Errorf("nothing visible to convert (phase %q)", phase)
	}

	all := append([]*slides.Request{{CreateSlide: &slides.CreateSlideRequest{
		ObjectId:             slideID,
		SlideLayoutReference: &slides.LayoutReference{PredefinedLayout: "BLANK"},
	}}}, reqs...)

	return &Result{SlideID: slideID, Phase: phase, Requests: all, Warnings: warnings}, nil
}

// fontFamily extracts the first font of the root font-family attribute.
func fontFamily(root *svgpkg.Element) string {
	ff := root.Attr("font-family")
	if ff == "" {
		return "Arial"
	}
	first := strings.SplitN(ff, ",", 2)[0]
	return strings.Trim(strings.TrimSpace(first), `'"`)
}

func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
