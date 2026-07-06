// Package auth provides authentication helpers for Google APIs.
// It supports both OAuth2 user credentials (with interactive browser-based
// authorization and local token caching) and service account credentials for
// accessing Google Slides and Drive services.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
	"google.golang.org/api/slides/v1"
	htransport "google.golang.org/api/transport/http"
)

// GetOAuthClient returns an authenticated HTTP client for Google Slides and
// Drive APIs. It reads the credentials file and attempts OAuth2 user
// authorization with token caching. If the credentials file contains a service
// account key, it falls back to service account authentication.
func GetOAuthClient(ctx context.Context, credentialsFile string) (*http.Client, error) {
	scopes := []string{drive.DriveScope, slides.PresentationsScope}

	if credentialsFile == "" {
		creds, err := google.FindDefaultCredentials(ctx, scopes...)
		if err != nil {
			return nil, fmt.Errorf("no credentials file provided and ADC not available: %w\n"+
				"Either provide --credentials or SLIDES_CREDENTIALS,\n"+
				"or run: gcloud auth application-default login "+
				"--scopes=https://www.googleapis.com/auth/drive,"+
				"https://www.googleapis.com/auth/presentations,"+
				"https://www.googleapis.com/auth/cloud-platform", err)
		}
		opts := []option.ClientOption{option.WithCredentials(creds)}
		quotaProject := creds.ProjectID
		if quotaProject == "" {
			quotaProject = os.Getenv("GOOGLE_CLOUD_QUOTA_PROJECT")
		}
		if quotaProject == "" {
			quotaProject = os.Getenv("VERTEX_PROJECT_ID")
		}
		if quotaProject != "" {
			opts = append(opts, option.WithQuotaProject(quotaProject))
		}
		client, _, err := htransport.NewClient(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP client: %w", err)
		}
		return client, nil
	}

	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("unable to read credentials file: %w", err)
	}

	config, err := google.ConfigFromJSON(b, scopes...)
	if err == nil {
		tokenFile := tokenCachePath()
		tok, err := tokenFromFile(tokenFile)
		if err != nil {
			tok, err = getTokenFromWeb(ctx, config)
			if err != nil {
				return nil, err
			}
			if err := saveToken(tokenFile, tok); err != nil {
				slog.Warn("failed to save token", "error", err)
			}
		}
		return config.Client(ctx, tok), nil
	}

	creds, err := google.CredentialsFromJSONWithParams(ctx, b, google.CredentialsParams{Scopes: scopes}) //nolint:staticcheck // credentials file is local and user-controlled
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}
	return oauth2.NewClient(ctx, creds.TokenSource), nil
}

// tokenCachePath reuses the same cache file as agentigslide so an already
// authorized token is picked up without a new interactive flow.
func tokenCachePath() string {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".credentials")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "slideappscripter-token.json")
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func getTokenFromWeb(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Fprintf(os.Stderr, "Go to the following link in your browser then type the authorization code:\n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		return nil, fmt.Errorf("unable to read authorization code: %w", err)
	}

	tok, err := config.Exchange(ctx, authCode)
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
	}
	return tok, nil
}

func saveToken(path string, token *oauth2.Token) error {
	slog.Info("saving credential file", "path", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(token)
}
