package gslide

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/retry"
)

// GetPresentation reads the whole presentation: slides, page elements and
// page size.
func (c *Client) GetPresentation(ctx context.Context, presentationID string) (*slides.Presentation, error) {
	pres, err := retry.DoWithResult(ctx, "presentations.get", func() (*slides.Presentation, error) {
		return c.Slides.Presentations.Get(presentationID).Context(ctx).Do()
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get presentation %s: %w", presentationID, err)
	}
	return pres, nil
}

// GetPresentationWithComments reads the presentation with its comment threads
// and their anchors, and reports whether they came back.
//
// Comments are a Google Workspace Developer Preview feature: an account not
// enrolled in the program, or without permission to view comments, gets a 403
// or 400. That is not a failure of the sync — only of the comment precision —
// so the error is swallowed and the caller told to fall back to Drive.
func (c *Client) GetPresentationWithComments(ctx context.Context, presentationID string) (pres *slides.Presentation, withComments bool, err error) {
	pres, err = retry.DoWithResult(ctx, "presentations.get+comments", func() (*slides.Presentation, error) {
		return c.Slides.Presentations.Get(presentationID).
			CommentsViewMode("COMMENTS_VIEW_MODE_INCLUDED").Context(ctx).Do()
	})
	if err == nil {
		return pres, true, nil
	}
	if !isCommentsUnavailable(err) {
		return nil, false, fmt.Errorf("failed to get presentation %s: %w", presentationID, err)
	}
	// Retry plainly: the deck itself is readable, only the comments are not.
	pres, err = c.GetPresentation(ctx, presentationID)
	return pres, false, err
}

// isCommentsUnavailable reports whether the error is the API refusing the
// comments view mode rather than a real problem with the request.
func isCommentsUnavailable(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case http.StatusForbidden, http.StatusBadRequest, http.StatusNotImplemented:
		return true
	}
	return false
}

// ListDriveComments reads a file's comments through the Drive API, which
// works without the Developer Preview. Drive documents its anchor as opaque
// for editor files, so the caller has to attribute them to slides itself.
func (c *Client) ListDriveComments(ctx context.Context, fileID string) ([]*drive.Comment, error) {
	if c.Drive == nil {
		return nil, errors.New("Drive API unavailable (token-only client)")
	}
	const fields = "nextPageToken,comments(id,content,anchor,quotedFileContent,resolved,deleted," +
		"createdTime,modifiedTime,author(displayName,me),replies(content,createdTime,author(displayName,me)))"

	var out []*drive.Comment
	token := ""
	for {
		resp, err := retry.DoWithResult(ctx, "comments.list", func() (*drive.CommentList, error) {
			call := c.Drive.Comments.List(fileID).Fields(fields).PageSize(100).Context(ctx)
			if token != "" {
				call = call.PageToken(token)
			}
			return call.Do()
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list comments of %s: %w", fileID, err)
		}
		out = append(out, resp.Comments...)
		if token = resp.NextPageToken; token == "" {
			return out, nil
		}
	}
}

// CopyPresentation snapshots a presentation under a new title and returns the
// copy's ID.
//
// This is the only snapshot mechanism the API offers: Drive has no named-
// version API — a revision carries no name, and keepForever is documented as
// applying only to files with binary content, so it is inert on a Slides
// file.
func (c *Client) CopyPresentation(ctx context.Context, presentationID, title string) (string, error) {
	if c.Drive == nil {
		return "", errors.New("Drive API unavailable (token-only client)")
	}
	f, err := retry.DoWithResult(ctx, "files.copy", func() (*drive.File, error) {
		return c.Drive.Files.Copy(presentationID, &drive.File{Name: title}).Fields("id").Context(ctx).Do()
	})
	if err != nil {
		return "", fmt.Errorf("failed to copy presentation %s: %w", presentationID, err)
	}
	return f.Id, nil
}

// BatchUpdateAtRevision sends the requests in a single call, refusing to write
// if the presentation has changed since requiredRevisionID was read.
//
// Unlike BatchUpdate it does not chunk: the guarantee is only meaningful for
// one atomic call, and the caller is expected to keep the request list small
// (the final reposition-and-delete step). An empty requiredRevisionID writes
// unconditionally.
func (c *Client) BatchUpdateAtRevision(ctx context.Context, presentationID string, reqs []*slides.Request, requiredRevisionID string) error {
	if len(reqs) == 0 {
		return nil
	}
	if len(reqs) > batchLimit {
		return fmt.Errorf("batchUpdate: %d requests exceeds the %d that can be sent atomically", len(reqs), batchLimit)
	}
	req := &slides.BatchUpdatePresentationRequest{Requests: reqs}
	if requiredRevisionID != "" {
		req.WriteControl = &slides.WriteControl{RequiredRevisionId: requiredRevisionID}
	}
	_, err := retry.DoWithResult(ctx, "presentations.batchUpdate", func() (*slides.BatchUpdatePresentationResponse, error) {
		return c.Slides.Presentations.BatchUpdate(presentationID, req).Context(ctx).Do()
	})
	if err != nil {
		return fmt.Errorf("batchUpdate (%d requests) failed: %w", len(reqs), err)
	}
	return nil
}
