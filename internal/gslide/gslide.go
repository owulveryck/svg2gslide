// Package gslide wraps the Google Slides and Drive services used to append
// a slide, fetch its thumbnail and export the presentation as PDF.
package gslide

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
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

// CreatePresentation creates a presentation and returns its ID together with
// the object IDs of the slides the API put in it unasked.
//
// A new presentation always comes with one blank slide, which is not part of
// anybody's deck. Callers delete it in the same batchUpdate that creates their
// first real slide: that way the deck never keeps a slide nobody asked for,
// and never momentarily holds none — which is what deleting it in a call of
// its own would do, on a presentation whose last page the API may well refuse
// to remove.
func (c *Client) CreatePresentation(ctx context.Context, title string) (id string, defaultSlides []string, err error) {
	pres, err := retry.DoWithResult(ctx, "presentations.create", func() (*slides.Presentation, error) {
		return c.Slides.Presentations.Create(&slides.Presentation{Title: title}).Context(ctx).Do()
	})
	if err != nil {
		return "", nil, fmt.Errorf("failed to create presentation: %w", err)
	}
	for _, s := range pres.Slides {
		defaultSlides = append(defaultSlides, s.ObjectId)
	}
	return pres.PresentationId, defaultSlides, nil
}

// DeleteRequests builds the requests that remove these objects.
func DeleteRequests(ids []string) []*slides.Request {
	out := make([]*slides.Request, 0, len(ids))
	for _, id := range ids {
		out = append(out, &slides.Request{DeleteObject: &slides.DeleteObjectRequest{ObjectId: id}})
	}
	return out
}

// PageSize returns the presentation page size in EMU.
func (c *Client) PageSize(ctx context.Context, presentationID string) (w, h float64, err error) {
	w, h, _, err = c.outline(ctx, presentationID, "pageSize")
	return w, h, err
}

// Outline returns the page size in EMU together with the object IDs of the
// slides the presentation already holds, in one call. The append path needs
// both: the size to fit the drawing, and the IDs to know whether the slide ID
// it derives from the source is still free.
func (c *Client) Outline(ctx context.Context, presentationID string) (w, h float64, slideIDs map[string]bool, err error) {
	return c.outline(ctx, presentationID, "pageSize,slides.objectId")
}

func (c *Client) outline(ctx context.Context, presentationID, fields string) (w, h float64, slideIDs map[string]bool, err error) {
	pres, err := retry.DoWithResult(ctx, "presentations.get", func() (*slides.Presentation, error) {
		return c.Slides.Presentations.Get(presentationID).Fields(googleapi.Field(fields)).Context(ctx).Do()
	})
	if err != nil {
		return 0, 0, nil, fmt.Errorf("failed to get presentation %s: %w", presentationID, err)
	}
	if pres.PageSize == nil || pres.PageSize.Width == nil || pres.PageSize.Height == nil {
		return 0, 0, nil, fmt.Errorf("presentation %s has no page size", presentationID)
	}
	slideIDs = make(map[string]bool, len(pres.Slides))
	for _, s := range pres.Slides {
		slideIDs[s.ObjectId] = true
	}
	return pres.PageSize.Width.Magnitude, pres.PageSize.Height.Magnitude, slideIDs, nil
}

// batchLimit is the most requests one batchUpdate call carries. Beyond it the
// work is split across calls, which also means it stops being atomic.
const batchLimit = 400

// BatchUpdate executes the requests, chunked to stay within API limits.
//
// Note that a request list longer than batchLimit is sent as several calls, so
// it is not applied atomically. Use BatchUpdateAtRevision for the steps where
// that matters.
func (c *Client) BatchUpdate(ctx context.Context, presentationID string, reqs []*slides.Request) error {
	for start := 0; start < len(reqs); start += batchLimit {
		end := min(start+batchLimit, len(reqs))
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

// HostImage uploads an image to Drive, readable by anyone with the link
// when the domain policy allows it, and returns a URL the Slides API can
// fetch plus a cleanup function that deletes the file (Slides copies the
// image at insertion time).
func (c *Client) HostImage(ctx context.Context, name, mime string, data []byte) (string, func(), error) {
	f, err := retry.DoWithResult(ctx, "files.create", func() (*drive.File, error) {
		return c.Drive.Files.Create(&drive.File{Name: name, MimeType: mime}).
			Media(bytes.NewReader(data)).Fields("id").Context(ctx).Do()
	})
	if err != nil {
		return "", nil, fmt.Errorf("failed to upload image: %w", err)
	}
	cleanup := func() { _ = c.Drive.Files.Delete(f.Id).Context(context.Background()).Do() }
	// Public sharing may be forbidden by the Workspace policy
	// (publishOutNotPermitted): the file then stays private and Slides
	// fetches it with the caller's credentials when it can.
	_, _ = c.Drive.Permissions.Create(f.Id, &drive.Permission{Type: "anyone", Role: "reader"}).Context(ctx).Do()
	return "https://drive.google.com/uc?export=download&id=" + f.Id, cleanup, nil
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
