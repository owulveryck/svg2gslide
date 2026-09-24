// Command svg2gslide converts an SVG file into a new slide made of native,
// editable Google Slides objects (shapes, lines, text boxes), appended to an
// existing presentation.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/convert"
	"github.com/owulveryck/svg2gslide/internal/gslide"
	"github.com/owulveryck/svg2gslide/internal/mapper"
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
		textTf       = flag.Bool("text-transform", false, "apply CSS text-transform (uppercase…) as browsers do; off by default like librsvg/resvg")
		dryRun       = flag.Bool("dry-run", false, "convert offline (16:9 page) and print element statistics, without calling the API")
	)
	flag.Parse()

	if *dryRun {
		if err := dry(*svgPath, *phase, *verbose, *textTf); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
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
	if err := run(context.Background(), *svgPath, *presentation, *credentials, *phase, *outThumbnail, *exportPDF, *verbose, *textTf); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, svgPath, presentationID, credentials, phase, outThumbnail, exportPDF string, verbose, textTransform bool) error {
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

	res, err := convert.Convert(convert.Input{
		SVG:     r,
		Label:   label,
		PageW:   pageW,
		PageH:   pageH,
		Phase:   phase,
		Verbose: verbose,

		TextTransform: textTransform,
	})
	if err != nil {
		return err
	}
	if verbose {
		for _, w := range res.Warnings {
			fmt.Fprintln(os.Stderr, "  [approx]", w)
		}
	}

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
	fmt.Printf("slide %s created (%d requests, phase %q)\n", res.SlideID, len(res.Requests), res.Phase)
	fmt.Printf("https://docs.google.com/presentation/d/%s/edit#slide=id.%s\n", presentationID, res.SlideID)

	if outThumbnail != "" {
		if err := client.FetchSlideThumbnail(ctx, presentationID, res.SlideID, outThumbnail); err != nil {
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

// dry converts the SVG against a default 16:9 page and prints what would be
// created, one line per text box.
func dry(svgPath, phase string, verbose, textTransform bool) error {
	f, err := os.Open(svgPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	res, err := convert.Convert(convert.Input{SVG: f, Label: svgPath, PageW: 9144000, PageH: 5143500, Phase: phase, Verbose: verbose, TextTransform: textTransform})
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, r := range res.Requests {
		switch {
		case r.CreateShape != nil:
			counts[r.CreateShape.ShapeType]++
		case r.CreateLine != nil:
			counts["LINE"]++
		case r.InsertText != nil && verbose:
			fmt.Printf("  text %q\n", r.InsertText.Text)
		}
	}
	fmt.Printf("%s: %d requests, textboxes=%d, shapes/lines=%v\n", svgPath, len(res.Requests), counts["TEXT_BOX"], counts)
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
