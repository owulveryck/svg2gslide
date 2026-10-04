package auth

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClientPathFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/cfg")
	got, err := ClientPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/custom/cfg", "svg2gslide", "client.json"); got != want {
		t.Errorf("ClientPath() = %q, want %q", got, want)
	}
}

func TestClientPathRejectsARelativeXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative/cfg")
	if _, err := ClientPath(); err == nil {
		t.Fatal("want an error for a relative $XDG_CONFIG_HOME")
	}
}

// The help is what the user sees before anything works, so it has to carry
// the whole procedure rather than a hint.
func TestMissingCredentialsHelpIsComplete(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/cfg")
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	got := MissingCredentialsHelp()

	tests := []struct {
		what string
		want string
	}{
		{"the credential type needed", `"Desktop app"`},
		{"where to create a project", "console.cloud.google.com/projectcreate"},
		{"the Slides API to enable", "apis/library/slides.googleapis.com"},
		{"the Drive API to enable", "apis/library/drive.googleapis.com"},
		// The consent screens moved to Google Auth Platform; the old
		// APIs & Services > Credentials path no longer holds them.
		{"where consent is configured", "console.cloud.google.com/auth/branding"},
		{"where the audience is set", "console.cloud.google.com/auth/audience"},
		{"where clients are created", "console.cloud.google.com/auth/clients"},
		{"the test-user requirement", "Test users"},
		{"the 7-day expiry of a testing app", "7 days"},
		{"why only a desktop client works", "127.0.0.1"},
		{"the flag alternative", "-credentials"},
		{"the environment variable", "SVG2GSLIDE_CREDENTIALS"},
		{"the service-account caveat", "share the target presentation"},
		{"what to run next", `"svg2gslide login" again`},
	}
	for _, tt := range tests {
		t.Run(tt.what, func(t *testing.T) {
			if !strings.Contains(got, tt.want) {
				t.Errorf("the help never mentions %q\n--- got ---\n%s", tt.want, got)
			}
		})
	}

	t.Run("names the resolved paths, not a hardcoded ~/.config", func(t *testing.T) {
		// ~/.config is only right on Linux: macOS resolves to
		// ~/Library/Application Support and Windows to %AppData%.
		if !strings.Contains(got, "/custom/cfg/svg2gslide/client.json") {
			t.Errorf("the help does not name the resolved client path\n%s", got)
		}
		if !strings.Contains(got, "/custom/state/svg2gslide/token.json") {
			t.Errorf("the help does not name the resolved token path\n%s", got)
		}
	})
}

func TestMissingCredentialsHelpSurvivesAnUnresolvableConfigDir(t *testing.T) {
	// A relative XDG path makes the default location unnameable; the help
	// must still explain the flag and the variable instead of failing.
	t.Setenv("XDG_CONFIG_HOME", "relative/cfg")
	t.Setenv("XDG_STATE_HOME", "relative/state")
	got := MissingCredentialsHelp()
	if strings.Contains(got, "mkdir -p") {
		t.Error("the help should not print an install command it cannot resolve")
	}
	for _, want := range []string{"-credentials", "SVG2GSLIDE_CREDENTIALS", `"Desktop app"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the fallback help is missing %q\n%s", want, got)
		}
	}
}
