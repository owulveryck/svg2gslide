// Command svg2gslide converts an SVG file into a new slide made of native,
// editable Google Slides objects (shapes, lines, text boxes), appended to an
// existing presentation.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/gslide"
	"github.com/owulveryck/svg2gslide/internal/mapper"
	svgpkg "github.com/owulveryck/svg2gslide/internal/svg"
)

func main() {
	var (
		svgPath      = flag.String("svg", "", "input SVG file (default: stdin)")
		presentation = flag.String("presentation", "", "target Google Slides presentation ID (required)")
		credentials  = flag.String("credentials", "", "OAuth client or service account JSON (default: $SLIDES_CREDENTIALS)")
		phase        = flag.String("phase", "", "active phase (default: the SVG's data-active-phase attribute)")
		outThumbnail = flag.String("out-thumbnail", "", "download the new slide's PNG thumbnail to this path")
		exportPDF    = flag.String("export-pdf", "", "export the whole presentation as PDF to this path")
		verbose      = flag.Bool("v", false, "log skipped and approximated elements")
	)
	flag.Parse()

	if *presentation == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *svgPath == "" {
		info, err := os.Stdin.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice != 0 {
			flag.Usage()
			os.Exit(2)
		}
	}
	if err := run(context.Background(), *svgPath, *presentation, *credentials, *phase, *outThumbnail, *exportPDF, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, svgPath, presentationID, credentials, phase, outThumbnail, exportPDF string, verbose bool) error {
	var (
		r     io.Reader
		label = svgPath
	)
	if svgPath == "" {
		r = os.Stdin
		label = "<stdin>"
	} else {
		f, err := os.Open(svgPath)
		if err != nil {
			return err
		}
		defer func() {
			_ = f.Close()
		}()
		r = f
	}
	root, err := svgpkg.Parse(r)
	if err != nil {
		return fmt.Errorf("parsing %s: %w", label, err)
	}
	if root.Tag != "svg" {
		return fmt.Errorf("%s: root element is <%s>, expected <svg>", label, root.Tag)
	}

	vb, err := svgpkg.ParseViewBox(root.Attr("viewBox"))
	if err != nil {
		// Fall back to width/height attributes.
		w, h := root.FloatAttr("width", 0), root.FloatAttr("height", 0)
		if w <= 0 || h <= 0 {
			return fmt.Errorf("%s: no usable viewBox or width/height", label)
		}
		vb = svgpkg.ViewBox{W: w, H: h}
	}

	if phase == "" {
		phase = root.Attr("data-active-phase")
	}
	var sheet *svgpkg.Stylesheet
	if styleEl := root.Find("style"); styleEl != nil {
		sheet = svgpkg.ParseStylesheet(styleEl.RawTextContent())
	} else {
		sheet = svgpkg.ParseStylesheet("")
	}

	if credentials == "" {
		credentials = os.Getenv("SLIDES_CREDENTIALS")
	}
	if credentials == "" {
		// Reuse the agentigslide OAuth client if present, so the cached
		// token works without a new interactive flow.
		if home, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(home, ".config", "gcloud", "slideappscripter-client.json")
			if _, err := os.Stat(p); err == nil {
				credentials = p
			}
		}
	}

	client, err := gslide.NewClient(ctx, credentials)
	if err != nil {
		return err
	}

	pageW, pageH, err := client.PageSize(ctx, presentationID)
	if err != nil {
		return err
	}
	scale := min(pageW/vb.W, pageH/vb.H)
	offX := (pageW - vb.W*scale) / 2
	offY := (pageH - vb.H*scale) / 2

	slideID := "svg2gslide_" + randomSuffix()
	m := mapper.New(mapper.Config{
		SlideID:    slideID,
		Phase:      phase,
		Scale:      scale,
		OffX:       offX,
		OffY:       offY,
		ViewBox:    vb,
		FontFamily: fontFamily(root),
		Verbose:    verbose,
	}, sheet)
	reqs, warnings := m.Map(root)
	if verbose {
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, "  [approx]", w)
		}
	}
	if len(reqs) == 0 {
		return fmt.Errorf("nothing visible to convert (phase %q)", phase)
	}

	all := append([]*slides.Request{{CreateSlide: &slides.CreateSlideRequest{
		ObjectId:             slideID,
		SlideLayoutReference: &slides.LayoutReference{PredefinedLayout: "BLANK"},
	}}}, reqs...)

	if err := client.BatchUpdate(ctx, presentationID, all); err != nil {
		return err
	}
	fmt.Printf("slide %s created (%d requests, phase %q)\n", slideID, len(all), phase)
	fmt.Printf("https://docs.google.com/presentation/d/%s/edit#slide=id.%s\n", presentationID, slideID)

	if outThumbnail != "" {
		if err := client.FetchSlideThumbnail(ctx, presentationID, slideID, outThumbnail); err != nil {
			return err
		}
		fmt.Println("thumbnail:", outThumbnail)
	}
	if exportPDF != "" {
		if err := client.ExportPDF(ctx, presentationID, exportPDF); err != nil {
			return err
		}
		fmt.Println("pdf:", exportPDF)
	}
	return nil
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
