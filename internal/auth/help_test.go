package auth

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestClientPathFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/cfg")
	t.Setenv(accountEnv, "")
	got, err := ClientPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/custom/cfg", "svg2gslide", "client.json"); got != want {
		t.Errorf("ClientPath() = %q, want %q", got, want)
	}
}

// The help tells the reader to install the client at this path, so with an
// account named it has to be that account's path, not the shared one.
func TestClientPathFollowsTheAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/cfg")
	t.Setenv(accountEnv, "orgA")
	got, err := ClientPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/custom/cfg", "svg2gslide", "orgA", "client.json"); got != want {
		t.Errorf("ClientPath() = %q, want %q", got, want)
	}

	t.Setenv(accountEnv, "../evil")
	if got, err := ClientPath(); err == nil {
		t.Errorf("ClientPath() accepted a traversing account, returning %q", got)
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
	t.Setenv(accountEnv, "")
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
		// The console procedure is the long way round; a reader with gcloud
		// installed needs to see that they can skip all of it.
		{"the gcloud route", "gcloud auth application-default login"},
		{"both routes, labelled", "Option B"},
		{"what the gcloud route needs", "GOOGLE_CLOUD_QUOTA_PROJECT"},
		// Someone reading this help on a machine with three Google accounts
		// has to learn that one variable separates them.
		{"how to separate accounts", "SVG2GSLIDE_ACCOUNT"},
	}
	for _, tt := range tests {
		t.Run(tt.what, func(t *testing.T) {
			if !strings.Contains(got, tt.want) {
				t.Errorf("the help never mentions %q\n--- got ---\n%s", tt.want, got)
			}
		})
	}

	t.Run("offers the gcloud command exactly once", func(t *testing.T) {
		// GetOAuthClient appends its own "ADC were tried" paragraph to this
		// text, so a second copy here would read as two separate orders.
		if n := strings.Count(got, "gcloud auth application-default login"); n != 1 {
			t.Errorf("the gcloud command appears %d times, want 1\n%s", n, got)
		}
	})

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

// Inside an account, the paths the help prints have to be that account's, and
// it has to say that one shared client would serve them all — otherwise the
// reader installs a second copy of a client they already have.
func TestMissingCredentialsHelpFollowsTheAccount(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/cfg")
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	t.Setenv(accountEnv, "orgA")
	got := MissingCredentialsHelp()

	for _, want := range []string{
		"/custom/cfg/svg2gslide/orgA/client.json",
		"/custom/state/svg2gslide/orgA/token.json",
		"/custom/cfg/svg2gslide/client.json",
		"SVG2GSLIDE_ACCOUNT=orgA",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the help never mentions %q\n--- got ---\n%s", want, got)
		}
	}
}

// The printed command is only useful if it grants exactly the scopes the API
// clients are built with, so it is derived from that slice and checked against
// it rather than against a literal.
func TestADCLoginCommandCarriesEveryScope(t *testing.T) {
	got := adcLoginCommand()
	if !strings.Contains(got, "gcloud auth application-default login") {
		t.Errorf("adcLoginCommand() = %q, want the gcloud ADC login command", got)
	}
	for _, s := range scopes {
		if !strings.Contains(got, s) {
			t.Errorf("adcLoginCommand() = %q, missing scope %q", got, s)
		}
	}
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
