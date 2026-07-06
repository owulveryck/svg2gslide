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

	"github.com/owulveryck/svg2gslide/internal/convert"
	"github.com/owulveryck/svg2gslide/internal/gslide"
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
	})
	if err != nil {
		return err
	}
	if verbose {
		for _, w := range res.Warnings {
			fmt.Fprintln(os.Stderr, "  [approx]", w)
		}
	}

	if err := client.BatchUpdate(ctx, presentationID, res.Requests); err != nil {
		return err
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
