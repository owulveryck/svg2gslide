package auth

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ClientPath returns the file svg2gslide reads its OAuth client from by
// default, resolved through $XDG_CONFIG_HOME / os.UserConfigDir, and under the
// account named by $SVG2GSLIDE_ACCOUNT when there is one — that is where the
// help tells the reader to install the client, so it has to name the account
// they are currently in. It is not hardcoded to ~/.config because that is only
// right on Linux: macOS resolves to ~/Library/Application Support and Windows
// to %AppData%.
func ClientPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	segment, _, err := account()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "svg2gslide", segment, "client.json"), nil
}

// sharedClientPath returns the account-less client file, the one every account
// falls back to. It is only named when an account is in play, where the
// difference between the two paths is worth a line.
func sharedClientPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "svg2gslide", "client.json"), nil
}

// adcLoginCommand is the gcloud invocation that grants svg2gslide's scopes to
// Application Default Credentials. The scopes come from the same slice the API
// clients are built with, so the printed command cannot drift away from what
// the tool actually asks for.
func adcLoginCommand() string {
	return "gcloud auth application-default login \\\n  --scopes=" + strings.Join(scopes, ",")
}

// adcHelp describes the credential route that needs no Google Cloud console
// visit, for the messages that offer both.
func adcHelp() string {
	return fmt.Sprintf(`Option A — reuse the Google Cloud SDK's own client (fastest, needs gcloud):

  %s

That signs you in with gcloud's OAuth client, so there is nothing to create and
no file to install. Two things it does need:
  - the scopes above, granted at that login: svg2gslide cannot add scopes to a
    credential that was created without them.
  - a quota project with the Slides and Drive APIs enabled. gcloud normally
    records one; otherwise set $GOOGLE_CLOUD_QUOTA_PROJECT.`, indent(adcLoginCommand(), "  "))
}

// indent prefixes every line but the first, which the caller has already
// placed, so a multi-line command keeps its shape inside a bulleted block.
func indent(s, prefix string) string {
	return strings.ReplaceAll(s, "\n", "\n"+prefix)
}

// MissingCredentialsHelp is the whole procedure for obtaining credentials,
// printed when none were found: the gcloud route, then installing an OAuth
// client of your own.
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
	// An unusable label is reported by the callers that need a path; the help
	// only has to avoid claiming an account it could not resolve.
	label, _, _ := account()

	var b strings.Builder
	b.WriteString("no credentials found: svg2gslide needs Google credentials to sign you in.\nThere are two ways to get them.\n\n")
	b.WriteString(adcHelp())
	b.WriteString(`

Option B — your own "Desktop app" OAuth client (no gcloud needed).

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
		if label != "" {
			if shared, err := sharedClientPath(); err == nil {
				fmt.Fprintf(&b, "     That path is this account's, named by $%s=%s. One\n"+
					"     client can authorize several accounts, and installed at the shared\n"+
					"     path instead it serves them all, only the tokens staying separate:\n"+
					"       %s\n", accountEnv, label, shared)
			}
		}
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

Several Google accounts on this machine? Name the one you are working in and
svg2gslide keeps its client and its token apart from the others':
  export SVG2GSLIDE_ACCOUNT=orgname    one directory per account, login included
  export SVG2GSLIDE_ACCOUNT=adc        this account goes through gcloud instead

For unattended use a service account key works instead of an OAuth client,
but it has no access to your personal Drive: share the target presentation
with the service account's email address first.`)

	return b.String()
}
