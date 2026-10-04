// Package auth provides authentication helpers for Google APIs.
// It supports both OAuth2 user credentials (with interactive browser-based
// authorization and local token caching) and service account credentials for
// accessing Google Slides and Drive services.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

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

	if credentialsFile == "" {
		creds, err := google.FindDefaultCredentials(ctx, scopes...)
		if err != nil {
			return nil, fmt.Errorf("no credentials file provided and ADC not available: %w\n"+
				"Either provide --credentials or SVG2GSLIDE_CREDENTIALS,\n"+
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
		tokenFile, err := tokenCachePath()
		if err != nil {
			return nil, err
		}
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
		src := &persistingSource{src: config.TokenSource(ctx, tok), last: tok, path: tokenFile}
		return oauth2.NewClient(ctx, src), nil
	}

	creds, err := google.CredentialsFromJSONWithParams(ctx, b, google.CredentialsParams{Scopes: scopes}) //nolint:staticcheck // credentials file is local and user-controlled
	if err != nil {
		return nil, fmt.Errorf("unable to parse credentials: %w", err)
	}
	return oauth2.NewClient(ctx, creds.TokenSource), nil
}

// configDir returns the base directory for user configuration. It honours
// $XDG_CONFIG_HOME on every platform — os.UserConfigDir only consults it on
// Unix — and otherwise falls back to the OS convention.
func configDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("path in $XDG_CONFIG_HOME is relative")
		}
		return dir, nil
	}
	return os.UserConfigDir()
}

// stateDir returns the base directory for persistent state such as cached
// tokens. The standard library has no UserStateDir, so $XDG_STATE_HOME is
// resolved by hand with the spec default of ~/.local/state.
func stateDir() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		if !filepath.IsAbs(dir) {
			return "", errors.New("path in $XDG_STATE_HOME is relative")
		}
		return dir, nil
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return os.UserConfigDir()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state"), nil
}

// tokenCachePath returns the file holding the cached OAuth token. The token is
// regenerable state, not configuration, so it lives under $XDG_STATE_HOME.
// Creating the directory is left to saveToken, the only writer.
func tokenCachePath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the token cache directory: %w", err)
	}
	return filepath.Join(dir, "svg2gslide", "token.json"), nil
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

var scopes = []string{drive.DriveScope, slides.PresentationsScope}

// ResolveCredentials returns the credentials file to use: the flag value, then
// $SVG2GSLIDE_CREDENTIALS, then the deprecated $SLIDES_CREDENTIALS, then the
// OAuth client under $XDG_CONFIG_HOME if present. It may return "", in which
// case the caller falls back to application default credentials.
func ResolveCredentials(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("SVG2GSLIDE_CREDENTIALS"); v != "" {
		return v
	}
	if v := os.Getenv("SLIDES_CREDENTIALS"); v != "" {
		slog.Warn("$SLIDES_CREDENTIALS is deprecated, use $SVG2GSLIDE_CREDENTIALS")
		return v
	}
	if dir, err := configDir(); err == nil {
		p := filepath.Join(dir, "svg2gslide", "client.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Login runs the browser authorization flow with the OAuth client in
// credentialsFile, ignoring any cached token, and stores the new token. It
// returns the path of the token file.
func Login(ctx context.Context, credentialsFile string) (string, error) {
	if credentialsFile == "" {
		return "", errors.New("no OAuth client JSON found: use -credentials, $SVG2GSLIDE_CREDENTIALS " +
			"or ~/.config/svg2gslide/client.json (a \"Desktop app\" OAuth client)")
	}
	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return "", fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := google.ConfigFromJSON(b, scopes...)
	if err != nil {
		return "", fmt.Errorf("%s is not an OAuth client JSON (login needs a \"Desktop app\" client): %w", credentialsFile, err)
	}
	tok, err := getTokenFromWeb(ctx, config)
	if err != nil {
		return "", err
	}
	path, err := tokenCachePath()
	if err != nil {
		return "", err
	}
	if err := saveToken(path, tok); err != nil {
		return "", fmt.Errorf("unable to save token: %w", err)
	}
	return path, nil
}

// persistingSource writes refreshed tokens back to the cache file.
type persistingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last *oauth2.Token
	path string
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	if p.last == nil || tok.AccessToken != p.last.AccessToken {
		if err := saveToken(p.path, tok); err != nil {
			slog.Warn("failed to save refreshed token", "error", err)
		}
		p.last = tok
	}
	return tok, nil
}

const loginTimeout = 3 * time.Minute

// getTokenFromWeb runs the authorization-code flow with PKCE: it opens the
// browser on the consent page and receives the code on a loopback listener.
// If the listener cannot be started it falls back to a manual copy-paste.
func getTokenFromWeb(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		slog.Warn("cannot listen on loopback, falling back to manual code entry", "error", err)
		return getTokenManually(ctx, config)
	}
	defer func() { _ = ln.Close() }()

	cfg := *config
	cfg.RedirectURL = fmt.Sprintf("http://%s/callback", ln.Addr().String())

	state, err := randomState()
	if err != nil {
		return nil, err
	}
	verifier := oauth2.GenerateVerifier()
	authURL := cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier))

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", callbackHandler(state, codeCh, errCh))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	fmt.Fprintf(os.Stderr, "Opening your browser to authorize access. If it does not open, visit:\n%v\n", authURL)
	if err := openBrowser(authURL); err != nil {
		slog.Warn("could not open the browser, open the link above manually", "error", err)
	}

	select {
	case code := <-codeCh:
		tok, err := cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
		if err != nil {
			return nil, fmt.Errorf("unable to retrieve token from web: %w", err)
		}
		return tok, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(loginTimeout):
		return nil, errors.New("timed out waiting for the browser authorization")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// callbackHandler validates the state and forwards the authorization code.
func callbackHandler(state string, codeCh chan<- string, errCh chan<- error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "invalid state", http.StatusBadRequest)
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "authorization failed: "+html.EscapeString(e), http.StatusBadRequest)
			select {
			case errCh <- fmt.Errorf("authorization denied: %s", e):
			default:
			}
			return
		}
		code := q.Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><meta charset=utf-8><title>svg2gslide</title>"+
			"<p>Login successful. You can close this tab and return to the terminal.</p>")
		select {
		case codeCh <- code:
		default:
		}
	}
}

func getTokenManually(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
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

func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("unable to generate state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func saveToken(path string, token *oauth2.Token) error {
	slog.Info("saving credential file", "path", path)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return json.NewEncoder(f).Encode(token)
}
