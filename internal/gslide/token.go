package gslide

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"
	"google.golang.org/api/slides/v1"
)

// NewClientWithToken builds a Slides-only client from a ready-made OAuth2
// access token (e.g. obtained in the browser via Google Identity Services).
// Client.Drive is nil, so PDF export is unavailable.
func NewClientWithToken(ctx context.Context, accessToken string) (*Client, error) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: accessToken})
	httpClient := oauth2.NewClient(ctx, ts)
	slidesSrv, err := slides.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Slides service: %w", err)
	}
	return &Client{Slides: slidesSrv}, nil
}
