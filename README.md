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
| `-text-transform` | apply CSS `text-transform` (uppercase…) like browsers; off by default, like librsvg/resvg |
| `-dry-run` | convert offline (16:9 page) and print statistics, no API call |
| `-v` | log skipped/approximated elements |

Maintenance utility:

```sh
go run ./cmd/presctl -presentation <ID> -list                      # slides + element counts
go run ./cmd/presctl -presentation <ID> -dump <SLIDE_ID> [-json]   # boxes and text of a slide
go run ./cmd/presctl -presentation <ID> -delete-slide <ID1>,<ID2> -export-pdf /tmp/deck.pdf
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
- Auth uses the Google Identity Services token flow with the
  `https://www.googleapis.com/auth/presentations` scope, plus
  `userinfo.email` to display the signed-in account. Tokens last ~1 h;
  the page silently re-requests one on expiry.
- Thumbnail/PDF export are CLI-only.
- Embedded raster images that are not simple icons are skipped in the
  browser (hosting them needs the Drive scope).

## How it works

1. SVG parsing (`internal/svg`) and the CSS **cascade**: declarations of
   simple selectors (`.class`, `#id`, `tag`, `tag.class`) are applied with
   CSS precedence (presentation attribute < stylesheet rule by specificity
   < inline `style`). The phase-driven visibility CSS
   (`[data-active-phase="N"]`) is evaluated statically — animations
   (`@keyframes`, dots, highlights) are ignored.
2. Mapping to `batchUpdate` requests (`internal/mapper`):
   - presentation attributes are **inherited** from ancestor groups, and
     group `opacity` is composed into fill/stroke alpha;
   - colours: hex (`#rgb`, `#rrggbb`, `#rrggbbaa`), `rgb()`/`rgba()`, common
     names; **gradients** (`url(#id)`) are reduced to their average colour
     (the Slides API has no gradient fills);
   - a full-page background rectangle becomes the **page background**;
   - `rect` → RECTANGLE / ROUND_RECTANGLE (square corners when the native
     rounding would be much larger than `rx`), pills → exact stadium
     (rectangle + two discs, grouped) or FLOW_CHART_TERMINATOR when
     stroked, `circle`/`ellipse` → ELLIPSE;
   - `line` → STRAIGHT connector (arrow if `marker-end`);
   - paths: absolute and relative commands, `S`/`T`; circular arcs are split
     into native quarter **ARC** shapes; each Bézier becomes a straight line
     when nearly straight, a native ARC when it is a quarter ellipse, a
     CURVED connector when it is a horizontal S, and otherwise a polyline
     flattened within 0.6 unit — the pieces of one path are **grouped**;
     closed straight paths are treated as polygons; a closed half-disc →
     FLOW_CHART_DELAY;
   - polygons: 3 points → rotated TRIANGLE, axis-aligned rhombus → DIAMOND,
     arrowhead "darts" → TRIANGLE, "3D box" groups → CUBE;
   - `<image>`: http(s) URLs are inserted as is; embedded (`data:`) small
     monochrome icons (e.g. PlantUML actors) are **redrawn** with native
     shapes (one ellipse / rounded rectangle per blob, grouped); other
     embedded images are hosted temporarily on Drive (CLI only, and only
     when the Workspace policy allows link sharing), else skipped;
   - text: **one multi-paragraph TEXT_BOX per logical block**. A grouping
     pass rebuilds the structure lost by generators emitting one `<text>`
     per line or per word: consecutive `<text>` siblings on the same
     baseline whose extents touch are joined into a line (a space is
     restored between PlantUML words measured with `textLength`); stacked
     lines with a regular pitch (≤ 2.3 em) sharing their start, centre or
     end become paragraphs of one box (blank spacer lines are absorbed).
     Inside a `<text>`, each `<tspan>` with `dy`/`y` starts a paragraph and
     inline tspans become styled runs (fill, weight, style, size).
     `font-family` lists are resolved to the first family Slides has
     (Helvetica / Helvetica Neue / system UI fonts → Arial). Baselines land
     on the SVG baselines (see the calibrated metrics below), rotation is
     carried by the box transform, gradient text is coloured per
     character, and the box is wide enough (Arial advance widths) for lines
     never to wrap.
3. A single BLANK slide is created and then filled in one `batchUpdate`
   (chunked past 400 requests).

## Known approximations

- `letter-spacing` is not supported by the Slides API (ignored; letter-spaced
  titles render narrower).
- `text-transform` is ignored unless `-text-transform` is given (the
  reference rasterizers ignore it too).
- Gradients → average solid colour; radial glows → uniform tint.
- Text opacity is flattened against the page background colour.
- Large `rx` on big shapes cannot be reproduced (Slides fixes the corner
  radius); `clipPath`, `mask`, `filter` are ignored.
- Closed curved paths other than half-discs: bounding box (warning under `-v`).
- Highlighter halo around tspans: rendered as plain bold.
- The SVG's font (e.g. `Outfit`) must exist in Google Fonts; text widths
  are estimated with Arial metrics.
- Glyphs missing from Arial (e.g. `▶`) fall back to another font in Slides.

## Text metrics calibration

Measured on text-matrix baselines of an exported PDF (PyPDF2), Arial:

- the TEXT_BOX inset is 0.1" vertically; glyph origins land 0.45 pt left
  of the horizontal inset;
- at line spacing p, same-size baselines are 1.2·size·p apart; between
  sizes `prev` and `cur` the single-spacing pitch is
  0.23·prev + 0.97·cur, extra spacing (p > 1) being added below the
  previous line and reduced spacing (p < 1) removed 80 % above / 20 %
  below;
- the first baseline sits 0.955·size − 0.4 pt below the inset (minus the
  80 % share of the reduction when p < 1); `spaceAbove` also applies to
  the first paragraph;
- line spacing is capped at 115 %; looser pitches become `spaceAbove`.

Residual baseline errors are within the renderer's 0.75 pt pixel snapping.

## Visual validation

`rsvg-convert` renders these SVGs **blank** (librsvg doesn't apply the
phase CSS). For a faithful reference, use headless Chrome:

```sh
"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" --headless \
  --screenshot=/tmp/ref.png --window-size=2000,1680 \
  "file://$PWD/testdata/sdlc-phase-8.svg"
```

then compare with the thumbnail produced by `-out-thumbnail`.
