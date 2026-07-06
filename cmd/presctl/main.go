// Command presctl offers small maintenance operations on a presentation:
// deleting a slide created by svg2gslide and exporting the deck as PDF.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/gslide"
)

func main() {
	var (
		presentation = flag.String("presentation", "", "presentation ID (required)")
		credentials  = flag.String("credentials", os.Getenv("SLIDES_CREDENTIALS"), "OAuth client or service account JSON")
		deleteSlide  = flag.String("delete-slide", "", "object ID of a slide to delete")
		exportPDF    = flag.String("export-pdf", "", "export the presentation as PDF to this path")
	)
	flag.Parse()
	if *presentation == "" || (*deleteSlide == "" && *exportPDF == "") {
		flag.Usage()
		os.Exit(2)
	}
	ctx := context.Background()
	client, err := gslide.NewClient(ctx, *credentials)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if *deleteSlide != "" {
		err := client.BatchUpdate(ctx, *presentation, []*slides.Request{
			{DeleteObject: &slides.DeleteObjectRequest{ObjectId: *deleteSlide}},
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println("deleted slide", *deleteSlide)
	}
	if *exportPDF != "" {
		if err := client.ExportPDF(ctx, *presentation, *exportPDF); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println("pdf:", *exportPDF)
	}
}
