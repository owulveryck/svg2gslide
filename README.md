# svg2gslide

Converts an SVG file into a **native** Google Slides slide (editable shapes,
lines and text boxes — not an image), appended to the end of an existing
presentation.

## Usage

```sh
go run . -svg testdata/sdlc-phase-8.svg \
  -presentation <PRESENTATION_ID> \
  -out-thumbnail /tmp/slide.png \
  -v
```

The SVG can also be piped on stdin:

```sh
cat testdata/sdlc-phase-8.svg | go run . -presentation <PRESENTATION_ID>
```

Flags:

| Flag | Description |
|---|---|
| `-svg` | input SVG file (default: stdin) |
| `-presentation` | target presentation ID (required) |
| `-credentials` | OAuth client or service account JSON (default: `$SLIDES_CREDENTIALS`, then `~/.config/gcloud/slideappscripter-client.json`) |
| `-phase` | force the active phase (default: the SVG's `data-active-phase` attribute) |
| `-out-thumbnail` | download the new slide's PNG thumbnail to this path |
| `-export-pdf` | export the whole presentation as PDF |
| `-v` | log skipped/approximated elements |

Maintenance utility:

```sh
go run ./cmd/presctl -presentation <ID> -delete-slide <SLIDE_OBJECT_ID> -export-pdf /tmp/deck.pdf
```

## Web frontend (WebAssembly)

The same conversion pipeline runs entirely in the browser: sign in with
Google, pick an SVG, paste the presentation URL, convert. No server-side
component — the page talks directly to the Slides REST API.

### One-time Google Cloud setup

1. In a Google Cloud project with the **Google Slides API** enabled, open
   *APIs & Services → Credentials → Create credentials → OAuth client ID →
   **Web application***.
2. Add `http://localhost:8000` to **Authorized JavaScript origins** (plus
   your production origin if you host the page). No redirect URI is needed.
3. If the OAuth consent screen is in *Testing* status, add your Google
   account as a test user.
4. Copy the client ID (`….apps.googleusercontent.com`) — you'll paste it in
   the page (it is remembered in `localStorage`).

The desktop-client JSON used by the CLI cannot be reused: the browser flow
requires a *Web application* client type.

### Build & run

```sh
make serve   # builds web/main.wasm + copies wasm_exec.js, serves on :8000
```

then open <http://localhost:8000>. `make wasm` builds the assets only; any
static file server works (the page must be served over http(s), not
`file://`, or Google sign-in refuses to issue a token).

Notes:

- `web/main.wasm` is ~28 MB raw (~6 MB gzipped) — serve compressed in
  production.
- Auth uses the Google Identity Services token flow, scope
  `https://www.googleapis.com/auth/presentations` only. Tokens last ~1 h;
  the page silently re-requests one on expiry.
- Thumbnail/PDF export are CLI-only.

## How it works

1. SVG parsing (`internal/svg`) and **static** evaluation of the phase-driven
   visibility CSS (`[data-active-phase="N"]`) — animations (`@keyframes`,
   dots, highlights) are ignored.
2. Mapping to `batchUpdate` requests (`internal/mapper`):
   - `rect` → RECTANGLE / ROUND_RECTANGLE, `circle` → ELLIPSE;
   - `line` → STRAIGHT connector (arrow if `marker-end`);
   - axis-aligned quarter arcs (`A`) → native **ARC** shape, oriented per
     quadrant via scaleX/scaleY flips;
   - quadratic curves (`Q`) → CURVED connector, split at the midpoint when
     the curve is deep;
   - 3-point polygons (chevrons) → TRIANGLE with rotation;
   - "3D box" groups (≥3 polygons) → CUBE shape;
   - `text` → centered TEXT_BOX (font, size, bold/italic, color).
3. A single BLANK slide is created and then filled in one `batchUpdate`
   (chunked past 400 requests).

## Known approximations

- Closed cubic-curve paths (clouds, etc.): ignored (warning under `-v`).
- Highlighter halo around tspans: rendered as plain bold.
- The SVG's font (e.g. `Outfit`) must exist in Google Fonts.

## Visual validation

`rsvg-convert` renders these SVGs **blank** (librsvg doesn't apply the
phase CSS). For a faithful reference, use headless Chrome:

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless \
  --screenshot=/tmp/ref.png --window-size=2000,1680 \
  "file://$PWD/testdata/sdlc-phase-8.svg"
```

then compare with the thumbnail produced by `-out-thumbnail`.
