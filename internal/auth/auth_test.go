package auth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

func TestTokenCachePath(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

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

func TestResolveCredentials(t *testing.T) {
	cfg := t.TempDir()
	xdgClient := filepath.Join(cfg, "svg2gslide", "client.json")

	tests := []struct {
		name      string
		flag      string
		newEnv    string
		oldEnv    string
		writeFile bool
		want      string
	}{
		{name: "flag wins", flag: "/flag.json", newEnv: "/new.json", oldEnv: "/old.json", writeFile: true, want: "/flag.json"},
		{name: "new env over old", newEnv: "/new.json", oldEnv: "/old.json", writeFile: true, want: "/new.json"},
		{name: "deprecated env over XDG", oldEnv: "/old.json", writeFile: true, want: "/old.json"},
		{name: "XDG client when it exists", writeFile: true, want: xdgClient},
		{name: "empty when nothing is found", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", cfg)
			t.Setenv("SVG2GSLIDE_CREDENTIALS", tt.newEnv)
			t.Setenv("SLIDES_CREDENTIALS", tt.oldEnv)
			// ResolveCredentials stats the file, so it has to be real.
			if err := os.RemoveAll(filepath.Dir(xdgClient)); err != nil {
				t.Fatal(err)
			}
			if tt.writeFile {
				if err := os.MkdirAll(filepath.Dir(xdgClient), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(xdgClient, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if got := ResolveCredentials(tt.flag); got != tt.want {
				t.Errorf("ResolveCredentials(%q) = %q, want %q", tt.flag, got, tt.want)
			}
		})
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
