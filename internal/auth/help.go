package auth

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ClientPath returns the file svg2gslide reads its OAuth client from by
// default, resolved through $XDG_CONFIG_HOME / os.UserConfigDir. It is not
// hardcoded to ~/.config because that is only right on Linux: macOS resolves
// to ~/Library/Application Support and Windows to %AppData%.
func ClientPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "svg2gslide", "client.json"), nil
}

// MissingCredentialsHelp is the whole procedure for obtaining and installing
// an OAuth client, printed when none was found.
//
// It is deliberately long. The failure happens before anyone has a working
// setup, so a one-line "not found" leaves the reader to guess which of
// Google's several credential types they need and where its screens moved to.
func MissingCredentialsHelp() string {
	client, err := ClientPath()
	if err != nil {
		// Without a config directory the path cannot be named; the flag and
		// the environment variable still work.
		client = ""
	}
	token, err := tokenCachePath()
	if err != nil {
		token = ""
	}

	var b strings.Builder
	b.WriteString(`no OAuth client found: svg2gslide needs a Google "Desktop app" OAuth client to sign you in.

This is a one-time setup. In the Google Cloud console:

  1. Create or pick a project
       https://console.cloud.google.com/projectcreate

  2. Enable the two APIs this tool calls
       https://console.cloud.google.com/apis/library/slides.googleapis.com
       https://console.cloud.google.com/apis/library/drive.googleapis.com

  3. Configure consent, under "Google Auth Platform"
       Branding  https://console.cloud.google.com/auth/branding
                 an app name and a support email are enough
       Audience  https://console.cloud.google.com/auth/audience
                 "Internal" if your Google Workspace organization allows it,
                 otherwise "External" — and then add your own Google account
                 under "Test users", or consent will be refused

  4. Create the client
       https://console.cloud.google.com/auth/clients
       "Create client" > Application type: "Desktop app" > Create,
       then "Download JSON"
`)

	b.WriteString("\n  5. Install it where svg2gslide looks\n")
	if client != "" {
		fmt.Fprintf(&b, "       mkdir -p %s\n", filepath.Dir(client))
		fmt.Fprintf(&b, "       mv ~/Downloads/client_secret_*.json %s\n", client)
	} else {
		b.WriteString("       pass it with -credentials, or set $SVG2GSLIDE_CREDENTIALS\n")
	}

	b.WriteString(`
Then run "svg2gslide login" again: it opens your browser for consent and
caches the token`)
	if token != "" {
		fmt.Fprintf(&b, " in %s (mode 0600)", token)
	}
	b.WriteString(`.

Notes worth knowing before you hit them:
  - An "External" app left in "Testing" gives its test users a consent that
    expires after 7 days, so login has to be re-run weekly until you publish
    the app ("Audience" > "Publish app").
  - The client JSON is yours and stays on this machine; it is only read to
    start the consent flow. It is not a secret you need to share with anyone.
  - A client of any type other than "Desktop app" will fail: the flow needs a
    loopback redirect on 127.0.0.1, which only that type permits.

Other ways to point at the file:
  -credentials /path/to/client.json
  export SVG2GSLIDE_CREDENTIALS=/path/to/client.json

For unattended use a service account key works instead of an OAuth client,
but it has no access to your personal Drive: share the target presentation
with the service account's email address first.`)

	return b.String()
}
