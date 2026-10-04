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
	"strings"
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
	// Refuse a malformed account label here rather than resolve credentials
	// around it: a run that silently ignored it would act as whichever account
	// was logged in last, which is the mistake the label exists to prevent.
	if _, _, err := account(); err != nil {
		return nil, err
	}

	if credentialsFile == "" {
		creds, err := google.FindDefaultCredentials(ctx, scopes...)
		if err != nil {
			// Same root cause as a bare "login", so give the same full
			// procedure rather than a second, shorter riddle. The procedure
			// leads with option A, so the gcloud command is already in it.
			return nil, fmt.Errorf("%s\n\nApplication Default Credentials were tried as a fallback and are not\navailable either (%v)",
				MissingCredentialsHelp(), err)
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

// accountEnv is the one variable that selects a Google account. It names a
// directory segment under the XDG directories, so switching account — three
// organizations, three logins — is a single export in a direnv .envrc, honoured
// by "login" when it writes the token and by every later call when it reads it.
const accountEnv = "SVG2GSLIDE_ACCOUNT"

// adcSentinel, as the value of accountEnv or of -credentials, asks for
// Application Default Credentials by name. Without it a client.json sitting at
// the default path always wins, so an organization that forbids creating an
// OAuth client of your own could never reach the gcloud route.
const adcSentinel = "adc"

// account returns the directory segment that separates one Google account's
// credentials from another's, and whether the ADC route was asked for by name.
// The segment is a label, not a path: it becomes one component under the XDG
// directories, so a separator or a traversal in it is refused rather than
// quietly resolved somewhere else. An unset variable yields "", which leaves
// every path exactly where a single-account install already has it.
func account() (segment string, adc bool, err error) {
	v := strings.TrimSpace(os.Getenv(accountEnv))
	switch {
	case v == "":
		return "", false, nil
	case strings.EqualFold(v, adcSentinel):
		return "", true, nil
	case v != filepath.Base(v), v == ".", v == "..":
		return "", false, fmt.Errorf("$%s is an account name, not a path: %q cannot be a single directory under the credentials directory", accountEnv, v)
	}
	return v, false, nil
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
// regenerable state, not configuration, so it lives under $XDG_STATE_HOME, and
// under a per-account subdirectory when $SVG2GSLIDE_ACCOUNT names one. One file
// for several accounts would hand a new account's OAuth client the previous
// account's refresh token: Google refuses it when it has expired, and when it
// has not, the conversion lands in the wrong Drive without a word.
//
// Creating the directory is left to saveToken, the only writer.
func tokenCachePath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the token cache directory: %w", err)
	}
	segment, _, err := account()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "svg2gslide", segment, "token.json"), nil
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

// ResolveCredentials returns the credentials file to use, from the most
// specific source to the least: the flag value, then $SVG2GSLIDE_CREDENTIALS,
// then the deprecated $SLIDES_CREDENTIALS, then the OAuth client of the account
// named by $SVG2GSLIDE_ACCOUNT, then the client shared by every account. The
// per-account file is optional on purpose: one "Desktop app" client can
// authorize all three accounts, and then only the tokens need separating.
//
// It may return "", in which case the caller falls back to application default
// credentials; the value "adc" asks for those by name.
func ResolveCredentials(flagValue string) string {
	if flagValue != "" {
		if strings.EqualFold(flagValue, adcSentinel) {
			return ""
		}
		return flagValue
	}
	if v := os.Getenv("SVG2GSLIDE_CREDENTIALS"); v != "" {
		if strings.EqualFold(v, adcSentinel) {
			return ""
		}
		return v
	}
	if v := os.Getenv("SLIDES_CREDENTIALS"); v != "" {
		slog.Warn("$SLIDES_CREDENTIALS is deprecated, use $SVG2GSLIDE_CREDENTIALS")
		return v
	}
	// A malformed label stops the search here and is reported by
	// GetOAuthClient and Login, which refuse to run at all.
	segment, adc, err := account()
	if err != nil || adc {
		return ""
	}
	dir, err := configDir()
	if err != nil {
		return ""
	}
	if segment != "" {
		if p := filepath.Join(dir, "svg2gslide", segment, "client.json"); fileExists(p) {
			return p
		}
	}
	if p := filepath.Join(dir, "svg2gslide", "client.json"); fileExists(p) {
		return p
	}
	return ""
}

// fileExists reports whether a candidate credentials path is there to be read.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// LoginResult says which credential route the login took, so the caller can
// report what actually happened instead of a token path that may not exist.
type LoginResult struct {
	// TokenPath is the file the new token was cached in. It is empty on the
	// ADC route, where gcloud holds the credential and svg2gslide writes none.
	TokenPath string
	// ADC reports that Application Default Credentials are in use and no
	// OAuth client of your own is needed.
	ADC bool
	// Account is the Google account the credentials resolve to, when the API
	// named it.
	Account string
	// Label is the value of $SVG2GSLIDE_ACCOUNT the paths were resolved under,
	// empty when it is unset. With three accounts on one machine, reporting it
	// is how you confirm the login landed in the environment you meant.
	Label string
}

// Login authorizes svg2gslide and returns how it did so. With an OAuth client
// in credentialsFile it runs the browser flow, ignoring any cached token, and
// stores the new one. With no client file it verifies Application Default
// Credentials instead, which is a complete setup on its own.
func Login(ctx context.Context, credentialsFile string) (LoginResult, error) {
	label, _, err := account()
	if err != nil {
		return LoginResult{}, err
	}
	if credentialsFile == "" {
		res, err := loginWithADC(ctx)
		if err != nil {
			return LoginResult{}, err
		}
		res.Label = label
		return res, nil
	}
	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return LoginResult{}, fmt.Errorf("unable to read credentials file: %w", err)
	}
	config, err := google.ConfigFromJSON(b, scopes...)
	if err != nil {
		return LoginResult{}, fmt.Errorf("%s is not an OAuth client JSON (login needs a \"Desktop app\" client): %w", credentialsFile, err)
	}
	tok, err := getTokenFromWeb(ctx, config)
	if err != nil {
		return LoginResult{}, err
	}
	path, err := tokenCachePath()
	if err != nil {
		return LoginResult{}, err
	}
	if err := saveToken(path, tok); err != nil {
		return LoginResult{}, fmt.Errorf("unable to save token: %w", err)
	}
	res := LoginResult{TokenPath: path, Label: label}
	// Best effort: the token is already cached and usable, so a Drive that
	// will not name the account is no reason to fail the login.
	src := &persistingSource{src: config.TokenSource(ctx, tok), last: tok, path: path}
	if email, err := driveAccount(ctx, oauth2.NewClient(ctx, src)); err != nil {
		slog.Warn("logged in, but Drive did not name the account", "error", err)
	} else {
		res.Account = email
	}
	return res, nil
}

// driveAccount names the Google account a client is authorized as. With
// several accounts on one machine the email address is the only way to tell
// that a login landed on the intended one.
func driveAccount(ctx context.Context, client *http.Client) (string, error) {
	srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return "", fmt.Errorf("unable to reach Drive: %w", err)
	}
	about, err := srv.About.Get().Fields("user/emailAddress").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	if about.User == nil {
		return "", nil
	}
	return about.User.EmailAddress, nil
}

// loginWithADC confirms Application Default Credentials rather than running a
// browser flow: gcloud owns the refresh token, so there is nothing for
// svg2gslide to obtain or cache. The Drive call is what makes this worth
// running — discovery succeeds on a credential that was granted the wrong
// scopes or names no quota project, and both only fail at the first real API
// call. Better here than halfway through a conversion.
func loginWithADC(ctx context.Context) (LoginResult, error) {
	client, err := GetOAuthClient(ctx, "")
	if err != nil {
		return LoginResult{}, err
	}
	email, err := driveAccount(ctx, client)
	if err != nil {
		return LoginResult{}, fmt.Errorf("Application Default Credentials were found, but Google refused them: %w\n\n"+
			"Usually they were granted without the scopes svg2gslide needs, or their\n"+
			"quota project does not have the Slides and Drive APIs enabled. Granting\n"+
			"the scopes again:\n\n  %s\n\n"+
			"Or set up an OAuth client of your own instead — \"svg2gslide login\" with\n"+
			"-credentials, or a client.json at the path it reports, prints the steps.",
			err, indent(adcLoginCommand(), "  "))
	}
	return LoginResult{ADC: true, Account: email}, nil
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
