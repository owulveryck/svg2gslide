package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestCallbackHandler(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		wantCode int
		wantSent bool
	}{
		{"ok", "?state=s&code=abc", http.StatusOK, true},
		{"bad state", "?state=x&code=abc", http.StatusBadRequest, false},
		{"missing code", "?state=s", http.StatusBadRequest, false},
		{"denied", "?state=s&error=access_denied", http.StatusBadRequest, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codeCh := make(chan string, 1)
			errCh := make(chan error, 1)
			rec := httptest.NewRecorder()
			callbackHandler("s", codeCh, errCh)(rec, httptest.NewRequest("GET", "/callback"+tt.query, nil))
			if rec.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantCode)
			}
			select {
			case code := <-codeCh:
				if !tt.wantSent || code != "abc" {
					t.Errorf("unexpected code %q", code)
				}
			default:
				if tt.wantSent {
					t.Error("code not forwarded")
				}
			}
		})
	}
}

// homeDefault is the directory a resolver must fall back to when its XDG
// variable is unset, for a home of base.
func homeDefault(base string, xdgUnsetSuffix ...string) string {
	if runtime.GOOS == "darwin" {
		return filepath.Join(base, "Library", "Application Support")
	}
	return filepath.Join(append([]string{base}, xdgUnsetSuffix...)...)
}

func TestConfigDir(t *testing.T) {
	t.Run("absolute is honoured", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/somewhere/cfg")
		got, err := configDir()
		if err != nil || got != "/somewhere/cfg" {
			t.Errorf("configDir() = %q, %v; want /somewhere/cfg", got, err)
		}
	})
	t.Run("relative is rejected", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "relative/cfg")
		if _, err := configDir(); err == nil {
			t.Error("configDir() accepted a relative path")
		}
	})
	t.Run("unset falls back to the OS default", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("fallback is %AppData%, not derived from the home directory")
		}
		home := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", home)
		want := homeDefault(home, ".config")
		if got, err := configDir(); err != nil || got != want {
			t.Errorf("configDir() = %q, %v; want %q", got, err, want)
		}
	})
}

func TestStateDir(t *testing.T) {
	t.Run("absolute is honoured", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "/somewhere/state")
		got, err := stateDir()
		if err != nil || got != "/somewhere/state" {
			t.Errorf("stateDir() = %q, %v; want /somewhere/state", got, err)
		}
	})
	t.Run("relative is rejected", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "relative/state")
		if _, err := stateDir(); err == nil {
			t.Error("stateDir() accepted a relative path")
		}
	})
	t.Run("unset falls back to the OS default", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("fallback is %AppData%, not derived from the home directory")
		}
		home := t.TempDir()
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", home)
		want := homeDefault(home, ".local", "state")
		if got, err := stateDir(); err != nil || got != want {
			t.Errorf("stateDir() = %q, %v; want %q", got, err, want)
		}
	})
}

func TestAccount(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		want    string
		wantADC bool
		wantErr bool
	}{
		{name: "unset keeps the single-account paths", env: "", want: ""},
		{name: "a name becomes the segment", env: "orgA", want: "orgA"},
		{name: "surrounding space is dropped", env: "  orgA  ", want: "orgA"},
		{name: "adc asks for ADC by name", env: "adc", wantADC: true},
		{name: "adc is case-insensitive", env: "ADC", wantADC: true},
		{name: "a traversal is refused", env: "../evil", wantErr: true},
		{name: "a separator is refused", env: "org/a", wantErr: true},
		{name: "an absolute path is refused", env: "/org", wantErr: true},
		{name: "dot is refused", env: ".", wantErr: true},
		{name: "dotdot is refused", env: "..", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(accountEnv, tt.env)
			segment, adc, err := account()
			if (err != nil) != tt.wantErr {
				t.Fatalf("account() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				// The reader has to see which variable to fix.
				if !strings.Contains(err.Error(), accountEnv) {
					t.Errorf("account() error does not name $%s: %v", accountEnv, err)
				}
				return
			}
			if segment != tt.want || adc != tt.wantADC {
				t.Errorf("account() = %q, %v; want %q, %v", segment, adc, tt.want, tt.wantADC)
			}
		})
	}
}

func TestTokenCachePath(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)
	t.Setenv(accountEnv, "")

	got, err := tokenCachePath()
	if err != nil {
		t.Fatalf("tokenCachePath() error: %v", err)
	}
	if want := filepath.Join(base, "svg2gslide", "token.json"); got != want {
		t.Errorf("tokenCachePath() = %q, want %q", got, want)
	}
	// Resolving a path must not touch the filesystem; saveToken creates the
	// directory when it actually writes.
	if _, err := os.Stat(filepath.Join(base, "svg2gslide")); !os.IsNotExist(err) {
		t.Errorf("tokenCachePath() created the directory (stat error: %v)", err)
	}
}

// Two accounts have to end up in two files: one token for both would hand the
// second account's OAuth client the first account's refresh token.
func TestTokenCachePathSeparatesAccounts(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	paths := map[string]string{}
	for _, label := range []string{"", "orgA", "orgB"} {
		t.Setenv(accountEnv, label)
		got, err := tokenCachePath()
		if err != nil {
			t.Fatalf("tokenCachePath() with %s=%q: %v", accountEnv, label, err)
		}
		if want := filepath.Join(base, "svg2gslide", label, "token.json"); got != want {
			t.Errorf("tokenCachePath() with %s=%q = %q, want %q", accountEnv, label, got, want)
		}
		if other, seen := paths[got]; seen {
			t.Errorf("accounts %q and %q share the token file %q", other, label, got)
		}
		paths[got] = label
	}

	t.Setenv(accountEnv, "../evil")
	if got, err := tokenCachePath(); err == nil {
		t.Errorf("tokenCachePath() accepted a traversing account, returning %q", got)
	}
}

func TestResolveCredentials(t *testing.T) {
	cfg := t.TempDir()
	xdgClient := filepath.Join(cfg, "svg2gslide", "client.json")
	ownClient := filepath.Join(cfg, "svg2gslide", "orgA", "client.json")

	tests := []struct {
		name      string
		flag      string
		newEnv    string
		oldEnv    string
		account   string
		writeFile bool
		writeOwn  bool
		want      string
	}{
		{name: "flag wins", flag: "/flag.json", newEnv: "/new.json", oldEnv: "/old.json", writeFile: true, want: "/flag.json"},
		{name: "new env over old", newEnv: "/new.json", oldEnv: "/old.json", writeFile: true, want: "/new.json"},
		{name: "deprecated env over XDG", oldEnv: "/old.json", writeFile: true, want: "/old.json"},
		{name: "XDG client when it exists", writeFile: true, want: xdgClient},
		{name: "empty when nothing is found", want: ""},
		// One OAuth client can authorize several accounts, so a client of the
		// account's own is preferred but not required.
		{name: "the account's own client wins", account: "orgA", writeFile: true, writeOwn: true, want: ownClient},
		{name: "an account falls back to the shared client", account: "orgA", writeFile: true, want: xdgClient},
		{name: "the credentials variable outranks the account", account: "orgA", newEnv: "/new.json", writeOwn: true, want: "/new.json"},
		// Without the sentinel, an installed client.json would make the gcloud
		// route unreachable for an organization that forbids creating one.
		{name: "adc as the account skips every client file", account: "adc", writeFile: true, writeOwn: true, want: ""},
		{name: "adc as the flag skips every client file", flag: "ADC", writeFile: true, want: ""},
		{name: "adc in the credentials variable skips every client file", newEnv: "adc", writeFile: true, want: ""},
		// The label is reported by GetOAuthClient and Login; discovery just
		// stops rather than guessing which account was meant.
		{name: "a malformed account finds nothing", account: "../evil", writeFile: true, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", cfg)
			t.Setenv("SVG2GSLIDE_CREDENTIALS", tt.newEnv)
			t.Setenv("SLIDES_CREDENTIALS", tt.oldEnv)
			t.Setenv(accountEnv, tt.account)
			// ResolveCredentials stats the file, so it has to be real.
			if err := os.RemoveAll(filepath.Dir(xdgClient)); err != nil {
				t.Fatal(err)
			}
			for path, write := range map[string]bool{xdgClient: tt.writeFile, ownClient: tt.writeOwn} {
				if !write {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := ResolveCredentials(tt.flag); got != tt.want {
				t.Errorf("ResolveCredentials(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
	}
}

// noADC points the credential search at a file that is not there, so a machine
// with real Application Default Credentials cannot make these tests reach the
// network: GOOGLE_APPLICATION_CREDENTIALS is consulted first and its failure
// is returned straight away, before the well-known file or GCE metadata.
func noADC(t *testing.T) {
	t.Helper()
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "absent.json"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SVG2GSLIDE_CREDENTIALS", "")
	t.Setenv("SLIDES_CREDENTIALS", "")
	t.Setenv(accountEnv, "")
}

func TestGetOAuthClientUsesADC(t *testing.T) {
	// A credential discovery fixture: well-formed, never exchanged, so no
	// request leaves the test.
	adc := filepath.Join(t.TempDir(), "application_default_credentials.json")
	const authorizedUser = `{"type":"authorized_user","client_id":"id.apps.googleusercontent.com","client_secret":"secret","refresh_token":"refresh"}`
	if err := os.WriteFile(adc, []byte(authorizedUser), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)
	t.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", "")
	t.Setenv("VERTEX_PROJECT_ID", "")
	t.Setenv(accountEnv, "")

	client, err := GetOAuthClient(context.Background(), "")
	if err != nil {
		t.Fatalf("GetOAuthClient(ctx, \"\") error: %v", err)
	}
	if client == nil {
		t.Error("GetOAuthClient(ctx, \"\") returned a nil client")
	}
}

func TestLoginWithoutAnyCredentials(t *testing.T) {
	noADC(t)

	res, err := Login(context.Background(), ResolveCredentials(""))
	if err == nil {
		t.Fatalf("Login() with no credentials succeeded, returning %+v", res)
	}
	// Login used to answer an absent client file with the OAuth-client
	// procedure alone; it now has to offer the gcloud route as well.
	for _, want := range []string{"Option A", "Option B"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Login() error is missing %q:\n%v", want, err)
		}
	}
}

func TestSaveToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "svg2gslide", "token.json")
	want := &oauth2.Token{AccessToken: "at", RefreshToken: "rt", Expiry: time.Now().Add(time.Hour).Round(time.Second)}

	if err := saveToken(path, want); err != nil {
		t.Fatalf("saveToken() error: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0600 {
		t.Errorf("token file mode = %o, want 600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("token directory not created: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0700 {
		t.Errorf("token directory mode = %o, want 700", perm)
	}

	got, err := tokenFromFile(path)
	if err != nil {
		t.Fatalf("tokenFromFile() error: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || !got.Expiry.Equal(want.Expiry) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}
