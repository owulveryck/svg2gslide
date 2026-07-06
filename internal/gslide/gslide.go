// Package gslide wraps the Google Slides and Drive services used to append
// a slide, fetch its thumbnail and export the presentation as PDF.
package gslide

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/auth"
	"github.com/owulveryck/svg2gslide/internal/retry"
)

// Client bundles the authenticated Slides and Drive services.
type Client struct {
	Slides *slides.Service
	Drive  *drive.Service
}

// NewClient authenticates and builds the Slides and Drive services.
func NewClient(ctx context.Context, credentialsFile string) (*Client, error) {
	httpClient, err := auth.GetOAuthClient(ctx, credentialsFile)
	if err != nil {
		return nil, err
	}
	slidesSrv, err := slides.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Slides service: %w", err)
	}
	driveSrv, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %w", err)
	}
	return &Client{Slides: slidesSrv, Drive: driveSrv}, nil
}

// PageSize returns the presentation page size in EMU.
func (c *Client) PageSize(ctx context.Context, presentationID string) (w, h float64, err error) {
	pres, err := retry.DoWithResult(ctx, "presentations.get", func() (*slides.Presentation, error) {
		return c.Slides.Presentations.Get(presentationID).Fields("pageSize").Context(ctx).Do()
	})
	if err != nil {
		return 0, 0, fmt.Errorf("failed to get presentation %s: %w", presentationID, err)
	}
	if pres.PageSize == nil || pres.PageSize.Width == nil || pres.PageSize.Height == nil {
		return 0, 0, fmt.Errorf("presentation %s has no page size", presentationID)
	}
	return pres.PageSize.Width.Magnitude, pres.PageSize.Height.Magnitude, nil
}

// BatchUpdate executes the requests, chunked to stay within API limits.
func (c *Client) BatchUpdate(ctx context.Context, presentationID string, reqs []*slides.Request) error {
	const chunkSize = 400
	for start := 0; start < len(reqs); start += chunkSize {
		end := min(start+chunkSize, len(reqs))
		_, err := retry.DoWithResult(ctx, "presentations.batchUpdate", func() (*slides.BatchUpdatePresentationResponse, error) {
			return c.Slides.Presentations.BatchUpdate(presentationID, &slides.BatchUpdatePresentationRequest{
				Requests: reqs[start:end],
			}).Context(ctx).Do()
		})
		if err != nil {
			return fmt.Errorf("batchUpdate (requests %d-%d) failed: %w", start, end-1, err)
		}
	}
	return nil
}

// FetchSlideThumbnail downloads a PNG thumbnail of the given slide.
func (c *Client) FetchSlideThumbnail(ctx context.Context, presentationID, slideID, outPath string) error {
	thumb, err := retry.DoWithResult(ctx, "pages.getThumbnail", func() (*slides.Thumbnail, error) {
		return c.Slides.Presentations.Pages.GetThumbnail(presentationID, slideID).
			ThumbnailPropertiesThumbnailSize("LARGE").
			ThumbnailPropertiesMimeType("PNG").
			Context(ctx).Do()
	})
	if err != nil {
		return fmt.Errorf("failed to get thumbnail: %w", err)
	}
	resp, err := http.Get(thumb.ContentUrl)
	if err != nil {
		return fmt.Errorf("failed to download thumbnail: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("thumbnail download returned %s", resp.Status)
	}
	return writeFile(outPath, resp.Body)
}

// ExportPDF exports the whole presentation as a PDF file.
func (c *Client) ExportPDF(ctx context.Context, presentationID, outPath string) error {
	resp, err := retry.DoWithResult(ctx, "files.export", func() (*http.Response, error) {
		return c.Drive.Files.Export(presentationID, "application/pdf").Context(ctx).Download()
	})
	if err != nil {
		return fmt.Errorf("failed to export PDF: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return writeFile(outPath, resp.Body)
}

func writeFile(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}
