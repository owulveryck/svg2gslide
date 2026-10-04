// Command svg2gslide converts an SVG file into a new slide made of native,
// editable Google Slides objects (shapes, lines, text boxes), appended to an
// existing presentation. Given an HTML page instead, it converts each inline
// <svg> (one per slide of an HTML deck) into its own slide, in order.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/auth"
	"github.com/owulveryck/svg2gslide/internal/convert"
	"github.com/owulveryck/svg2gslide/internal/gslide"
	"github.com/owulveryck/svg2gslide/internal/htmlsvg"
	"github.com/owulveryck/svg2gslide/internal/mapper"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "login" {
		if err := login(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	flag.Usage = usage
	var (
		svgPath      = flag.String("svg", "", "input SVG file, or HTML page with inline SVGs (one slide each) (default: stdin)")
		slideSel     = flag.String("slides", "", "HTML input only: SVGs to convert, 1-based, e.g. \"1-3,7,10-\" (default: all)")
		presentation = flag.String("presentation", "", "target Google Slides presentation ID or URL, or \"new\" to create one (required)")
		credentials  = flag.String("credentials", "", "OAuth client or service account JSON (default: $SVG2GSLIDE_CREDENTIALS)")
		phase        = flag.String("phase", "", "active phase (default: the SVG's data-active-phase attribute)")
		outThumbnail = flag.String("out-thumbnail", "", "download the new slide's PNG thumbnail to this path")
		exportPDF    = flag.String("export-pdf", "", "export the whole presentation as PDF to this path")
		verbose      = flag.Bool("v", false, "log skipped and approximated elements")
		textTf       = flag.Bool("text-transform", false, "apply CSS text-transform (uppercase…) as browsers do; off by default like librsvg/resvg")
		connect      = flag.Bool("connect-curves", false, "replace edges between shapes (PlantUML links, curved paths) by connectors attached to both shapes")
		dryRun       = flag.Bool("dry-run", false, "convert offline (16:9 page) and print element statistics, without calling the API")
	)
	flag.Parse()

	if *dryRun {
		if err := dry(*svgPath, *slideSel, *phase, *verbose, *textTf, *connect); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if *presentation == "" {
		fmt.Fprintln(os.Stderr, "error: -presentation is required (an ID, a URL, or \"new\" to create a presentation)")
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
	if err := run(context.Background(), *svgPath, *slideSel, *presentation, *credentials, *phase, *outThumbnail, *exportPDF, *verbose, *textTf, *connect); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, svgPath, slideSel, presentationID, credentials, phase, outThumbnail, exportPDF string, verbose, textTransform, connect bool) error {
	sources, err := loadSources(svgPath, slideSel)
	if err != nil {
		return err
	}

	credentials = auth.ResolveCredentials(credentials)

	client, err := gslide.NewClient(ctx, credentials)
	if err != nil {
		return err
	}

	if presentationID == "new" {
		title := strings.TrimSuffix(filepath.Base(svgPath), filepath.Ext(svgPath))
		if svgPath == "" {
			title = "svg2gslide"
		}
		presentationID, err = client.CreatePresentation(ctx, title)
		if err != nil {
			return err
		}
		fmt.Printf("presentation created: https://docs.google.com/presentation/d/%s/edit\n", presentationID)
	} else if id, err := convert.ExtractPresentationID(presentationID); err == nil {
		presentationID = id
	}

	pageW, pageH, err := client.PageSize(ctx, presentationID)
	if err != nil {
		return err
	}

	for _, src := range sources {
		res, err := convert.Convert(convert.Input{
			SVG:     bytes.NewReader(src.data),
			Label:   src.label,
			PageW:   pageW,
			PageH:   pageH,
			Phase:   phase,
			Verbose: verbose,

			TextTransform: textTransform,
			ConnectCurves: connect,
		})
		if err != nil {
			if len(sources) == 1 {
				return err
			}
			// One unconvertible SVG must not abort the rest of the deck.
			fmt.Fprintln(os.Stderr, "warning: skipped:", err)
			continue
		}
		if verbose {
			for _, w := range res.Warnings {
				fmt.Fprintln(os.Stderr, "  [approx]", w)
			}
		}
		if err := appendSlide(ctx, client, presentationID, res); err != nil {
			return fmt.Errorf("%s: %w", src.label, err)
		}
		fmt.Printf("%s: slide %s created (%d requests, phase %q)\n", src.label, res.SlideID, len(res.Requests), res.Phase)
		fmt.Printf("https://docs.google.com/presentation/d/%s/edit#slide=id.%s\n", presentationID, res.SlideID)

		if outThumbnail != "" {
			path := outThumbnail
			if len(sources) > 1 {
				ext := filepath.Ext(path)
				path = fmt.Sprintf("%s-%02d%s", strings.TrimSuffix(path, ext), src.index, ext)
			}
			if err := client.FetchSlideThumbnail(ctx, presentationID, res.SlideID, path); err != nil {
				return err
			}
			fmt.Println("thumbnail:", path)
		}
	}
	if exportPDF != "" {
		if err := client.ExportPDF(ctx, presentationID, exportPDF); err != nil {
			return err
		}
		fmt.Println("pdf:", exportPDF)
	}
	return nil
}

// appendSlide sends the conversion result to the presentation.
func appendSlide(ctx context.Context, client *gslide.Client, presentationID string, res *convert.Result) error {
	// Embedded images are inserted in a second pass: they need hosting,
	// and a failure there must not lose the slide.
	var imageReqs []*slides.Request
	if len(res.Images) > 0 {
		main := res.Requests[:0:0]
		for _, q := range res.Requests {
			if q.CreateImage != nil && strings.HasPrefix(q.CreateImage.Url, mapper.ImagePlaceholderPrefix) {
				imageReqs = append(imageReqs, q)
				continue
			}
			main = append(main, q)
		}
		res.Requests = main
	}
	if err := client.BatchUpdate(ctx, presentationID, res.Requests); err != nil {
		return err
	}
	if len(imageReqs) > 0 {
		if err := insertImages(ctx, client, presentationID, res, imageReqs); err != nil {
			fmt.Fprintln(os.Stderr, "warning: embedded images skipped:", err)
		}
	}
	return nil
}

// source is one SVG document to turn into a slide.
type source struct {
	index int // 1-based position in an HTML input, 0 for a bare SVG
	label string
	data  []byte
}

// loadSources reads the input (file or stdin): a bare SVG gives one source,
// an HTML page one source per selected inline SVG.
func loadSources(path, slideSel string) ([]source, error) {
	var (
		data  []byte
		err   error
		label = path
	)
	if path == "" {
		data, err = io.ReadAll(os.Stdin)
		label = "<stdin>"
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	if !htmlsvg.IsHTML(data) {
		if slideSel != "" {
			return nil, fmt.Errorf("-slides requires an HTML input")
		}
		return []source{{label: label, data: data}}, nil
	}

	svgs, err := htmlsvg.Extract(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	keep, err := parseSelection(slideSel)
	if err != nil {
		return nil, err
	}
	var out []source
	for _, s := range svgs {
		if !keep(s.Index) {
			continue
		}
		l := fmt.Sprintf("%s#%d", label, s.Index)
		if s.Title != "" {
			l += " (" + s.Title + ")"
		}
		out = append(out, source{index: s.Index, label: l, data: s.Data})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no inline <svg> selected (%d found)", label, len(svgs))
	}
	return out, nil
}

// parseSelection parses a list of 1-based indexes and ranges ("1-3,7,10-").
func parseSelection(sel string) (func(int) bool, error) {
	if strings.TrimSpace(sel) == "" {
		return func(int) bool { return true }, nil
	}
	type span struct{ lo, hi int }
	var spans []span
	for part := range strings.SplitSeq(sel, ",") {
		part = strings.TrimSpace(part)
		loS, hiS, isRange := strings.Cut(part, "-")
		lo, err := strconv.Atoi(loS)
		if err != nil || lo < 1 {
			return nil, fmt.Errorf("-slides: invalid item %q", part)
		}
		hi := lo
		if isRange {
			if hiS == "" {
				hi = int(^uint(0) >> 1)
			} else if hi, err = strconv.Atoi(hiS); err != nil || hi < lo {
				return nil, fmt.Errorf("-slides: invalid range %q", part)
			}
		}
		spans = append(spans, span{lo, hi})
	}
	return func(i int) bool {
		for _, s := range spans {
			if i >= s.lo && i <= s.hi {
				return true
			}
		}
		return false
	}, nil
}

// dry converts each SVG against a default 16:9 page and prints what would
// be created.
func dry(svgPath, slideSel, phase string, verbose, textTransform, connect bool) error {
	sources, err := loadSources(svgPath, slideSel)
	if err != nil {
		return err
	}
	for _, src := range sources {
		if err := dryOne(src, phase, verbose, textTransform, connect); err != nil {
			if len(sources) == 1 {
				return err
			}
			fmt.Fprintln(os.Stderr, "warning: skipped:", err)
		}
	}
	return nil
}

func dryOne(src source, phase string, verbose, textTransform, connect bool) error {
	res, err := convert.Convert(convert.Input{SVG: bytes.NewReader(src.data), Label: src.label, PageW: 9144000, PageH: 5143500, Phase: phase, Verbose: verbose, TextTransform: textTransform, ConnectCurves: connect})
	if err != nil {
		return err
	}
	counts := map[string]int{}
	boxes := map[string]bool{}
	inShape, conns, groups := 0, 0, 0
	for _, r := range res.Requests {
		switch {
		case r.CreateShape != nil:
			counts[r.CreateShape.ShapeType]++
			if r.CreateShape.ShapeType == "TEXT_BOX" {
				boxes[r.CreateShape.ObjectId] = true
			}
		case r.CreateLine != nil:
			counts["LINE"]++
		case r.InsertText != nil:
			if !boxes[r.InsertText.ObjectId] {
				inShape++
			}
			if verbose {
				fmt.Printf("  text %q\n", r.InsertText.Text)
			}
		case r.UpdateLineProperties != nil:
			lp := r.UpdateLineProperties.LineProperties
			if lp.StartConnection != nil {
				conns++
			}
			if lp.EndConnection != nil {
				conns++
			}
		case r.GroupObjects != nil:
			groups++
		}
	}
	fmt.Printf("%s: %d requests, textboxes=%d, text-in-shapes=%d, connections=%d, groups=%d, shapes/lines=%v\n",
		src.label, len(res.Requests), counts["TEXT_BOX"], inShape, conns, groups, counts)
	if verbose {
		for _, w := range res.Warnings {
			fmt.Fprintln(os.Stderr, "  [approx]", w)
		}
	}
	return nil
}

// insertImages hosts the embedded images on Drive and inserts them.
func insertImages(ctx context.Context, client *gslide.Client, presentationID string, res *convert.Result, reqs []*slides.Request) error {
	urls := map[string]string{}
	for i, img := range res.Images {
		u, cleanup, err := client.HostImage(ctx, fmt.Sprintf("svg2gslide-%s-%d", res.SlideID, i), img.MIME, img.Data)
		if err != nil {
			return err
		}
		defer cleanup()
		urls[img.Placeholder] = u
	}
	for _, q := range reqs {
		q.CreateImage.Url = urls[q.CreateImage.Url]
	}
	return client.BatchUpdate(ctx, presentationID, reqs)
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage:\n  %[1]s login [-credentials file]\n  %[1]s [flags] -presentation <id|url|new> [-svg file]\n\nFlags:\n", filepath.Base(os.Args[0]))
	flag.PrintDefaults()
}

// login runs the browser authorization flow and caches the token.
func login(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	credentials := fs.String("credentials", "", "OAuth client JSON (default: $SVG2GSLIDE_CREDENTIALS)")
	_ = fs.Parse(args)
	path, err := auth.Login(context.Background(), auth.ResolveCredentials(*credentials))
	if err != nil {
		return err
	}
	fmt.Println("logged in; token saved to", path)
	return nil
}
