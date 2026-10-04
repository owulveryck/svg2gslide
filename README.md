# svg2gslide

Converts an SVG file into a **native** Google Slides slide (editable shapes,
lines and text boxes — not an image), appended to the end of an existing
presentation.

To iterate on a whole deck rather than append once, see
[Sync](#sync-keep-a-deck-in-step-with-its-sources): an ordered list of SVGs
becomes the deck, and only what changed is rewritten.

## Login

Put your "Desktop app" OAuth client JSON where the tool looks for it, then log in:

```sh
mkdir -p ~/.config/svg2gslide && cp ~/Downloads/client_secret_*.json ~/.config/svg2gslide/client.json
go run . login
```

Opens your browser for Google consent, receives the callback on `127.0.0.1` and caches the
token in `~/.local/state/svg2gslide/token.json` (mode `0600`). Without a cached token, any
conversion triggers the same flow. Use `-presentation new` to create a fresh presentation
instead of passing an ID.

Both locations follow the [XDG Base Directory Specification][xdg] — `$XDG_CONFIG_HOME` for the
client you provide, `$XDG_STATE_HOME` for the token the tool writes:

| File | Default | Overridden by |
|---|---|---|
| OAuth client | `~/.config/svg2gslide/client.json` | `-credentials`, `$SVG2GSLIDE_CREDENTIALS`, `$XDG_CONFIG_HOME` |
| Cached token | `~/.local/state/svg2gslide/token.json` | `$XDG_STATE_HOME` |

[xdg]: https://specifications.freedesktop.org/basedir-spec/latest/

> **Upgrading from an earlier version:** the token used to be cached in
> `~/.credentials/slideappscripter-token.json` and the client read from
> `~/.config/gcloud/slideappscripter-client.json`. Neither is consulted any more — move your
> client JSON to the path above and run `login` once. `$SLIDES_CREDENTIALS` still works but
> is deprecated in favour of `$SVG2GSLIDE_CREDENTIALS`.

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

An HTML page with inline SVGs (e.g. an HTML slide deck) is detected
automatically: each `<svg>` becomes one slide, appended in document order.
When some SVGs sit inside a slide container (`<section>` or `class="slide"`),
only those are converted — UI icons (buttons, toolbars) are ignored.

```sh
go run . -svg deck.html -presentation <PRESENTATION_ID>              # all slides
go run . -svg deck.html -presentation <PRESENTATION_ID> -slides 1-3,7  # a selection
go run . -svg deck.html -dry-run                                     # one stats line per slide
```

Flags:

| Flag | Description |
|---|---|
| `-svg` | input SVG file, or HTML page with inline SVGs (default: stdin) |
| `-slides` | HTML input only: SVGs to convert, 1-based (`1-3,7,10-`; default: all) |
| `-presentation` | target presentation ID or URL (required); `new` creates a presentation named after the SVG and prints its URL |
| `-credentials` | OAuth client or service account JSON (default: `$SVG2GSLIDE_CREDENTIALS`, then `~/.config/svg2gslide/client.json`) |
| `-phase` | force the active phase (default: the SVG's `data-active-phase` attribute) |
| `-out-thumbnail` | download the new slide's PNG thumbnail to this path (`name-NN.png` per slide for an HTML input) |
| `-export-pdf` | export the whole presentation as PDF |
| `-text-transform` | apply CSS `text-transform` (uppercase…) like browsers; off by default, like librsvg/resvg |
| `-connect-curves` | replace edges between two shapes (PlantUML links, open curved paths) by one connector attached to both shapes (routed by Slides) |
| `-dry-run` | convert offline (16:9 page) and print statistics, no API call |
| `-v` | log skipped/approximated elements |

Maintenance utility:

```sh
go run ./cmd/presctl -presentation <ID> -list                      # slides + element counts
go run ./cmd/presctl -presentation <ID> -dump <SLIDE_ID> [-json]   # boxes and text of a slide
go run ./cmd/presctl -presentation <ID> -delete-slide <ID1>,<ID2> -export-pdf /tmp/deck.pdf
```

## Sync: keep a deck in step with its sources

`svg2gslide <file>` appends — handy once, awkward to iterate on. `svg2gslide
sync` is the declarative form: **an ordered list of SVGs *is* the deck**, and
sync reconciles the presentation with it. A changed source replaces its slide;
unchanged slides are not touched; syncing twice does nothing the second time.

```sh
# the deck is the argument list; the shell's glob gives the order
go run . sync -presentation <ID> slides/*.svg

# or a manifest, which is what you commit to git
cat deck.txt
# the talk, in order
intro.svg
architecture.svg
roadmap.svg
go run . sync -presentation <ID> -deck deck.txt

# see what would happen, with no API call at all
go run . sync -dry-run -deck deck.txt
```

Sync writes a state file (`.svg2gslide.json`, next to the deck by default)
recording what it pushed. That record is what lets it tell *"the source
changed"* from *"someone edited the slide"*. Commit it alongside the sources;
its diffs are meant to be read.

### Nothing human is overwritten by accident

A presentation is a shared document. Before writing, sync compares each slide
to what it last pushed, and reads the comment threads. It refuses to overwrite
a slide that carries human work:

```
slide   3  architecture.svg  source changed -> replaced
slide   4  flow.svg  CONFLICT  (source changed, slide edited, open comments)
    . text edited on /svg/g[2]/text[1] (id="auth-label")
      pushed  : "Service A"
      current : "Service Auth"
    . comment by a teammate, 2026-10-02T09:12:00Z, open
      anchored on /svg/g[3]/rect[1] (id="validation"), quoting "Validation"
      "il manque la flèche de retour"
    -> Port the divergences and comments below into flow.svg, then sync again.
       To discard the work in the slide instead: -force flow.svg
```

Every finding names the **node of the source SVG** to edit, not just a Slides
object ID. `-report json` emits the same thing in a form complete enough to
hand to an LLM so it can patch the SVG without ever reading the deck.

Resolve a conflict either way: port the feedback into the SVG and sync again,
or `-force <source>` (or `-force all`) to let the source win.

### Flags worth knowing

| Flag | What it does |
|---|---|
| `-dry-run` | reconcile and report, write nothing |
| `-report json` | the machine-readable report, with source locators |
| `-force <sources>` | overwrite these slides despite a conflict; `all` for every one |
| `-prune` | delete slides the deck no longer declares (see below) |
| `-backup` | copy the presentation before writing |
| `-geometry` | also compare element positions when detecting drift |
| `-state <path>` | where the state file lives |

**Nothing is ever deleted without `-prune`**, and even then only slides
svg2gslide created: a slide added by hand is reported as an orphan and left
alone. `-backup` takes a Drive copy first — there is no named-version API
(a Drive revision carries no name, and `keepForever` is documented as applying
only to files with binary content, so it is inert on a Slides file), so a copy
is the only snapshot available.

### Identity, and why ids matter

Each slide's object ID is derived from its source path, and each shape's from
the SVG node that produced it. That is what makes a comment left on a shape
still resolve to the right source node after you edit the SVG elsewhere.

For an HTML deck, a slide is identified by the `id` of its `<svg>` (or of its
`<section>`). Without one it falls back to the inline SVG's position, and sync
says so — such a slide loses its identity if you reorder the HTML. Giving your
`<svg>` elements ids is worth the trouble.

### Comments

Reading comments anchored to a precise shape uses a Slides API feature that is
in [Developer Preview](https://developers.google.com/workspace/preview). Without
enrollment, sync falls back to the Drive comments API, whose anchor is opaque
for editor files; comments are then tied to slides by matching their quoted
text, and each one is marked `quoted-text-match` or `ambiguous`. The report
always states which source was used, and says so explicitly when comments could
not be read at all — silence would read as "no comments", which is a different
claim.

### What a failure leaves behind

Sync clears the slides it is about to rebuild in one atomic, revision-guarded
call: if anyone edited the deck since it was read, that call fails and nothing
is lost. Everything after it only adds. If the run dies between the clear and
the refill, the replaced slides are left empty — running sync again fixes them,
and `-backup` is the belt for when that is not good enough.

## Web frontend (WebAssembly)

The same conversion pipeline runs entirely in the browser: sign in with
Google, pick an SVG (or an HTML page with inline SVGs, one slide each), paste the presentation URL, convert. No server-side
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
3. Editing structure, resolved once everything is mapped (`connect.go`):
   - **text in shapes**: a block whose topmost underlying object is a
     rectangle / rounded rectangle / ellipse / terminator containing it
     (and hosting no other block) is written into that shape, vertically
     centred (`MIDDLE`) with `spaceAbove`/`spaceBelow` and indents placing
     the baselines on the SVG ones — the text moves with its box;
   - otherwise (Slides' fixed 0.1" inset is often wider than the SVG
     padding, and there is no API to change it) the box is **grouped** with
     its texts and the small objects drawn on it (badges, icons), unless
     it is a mere frame (texts covering < 10 % of it) or grouping would
     change the stacking order;
   - **connections**: straight connector ends that already lie on a
     connection site of a shape (edge midpoints, 8 ellipse points) are
     attached to it — the drawing is unchanged. With `-connect-curves`,
     PlantUML links (`data-entity-1/2`) and open paths whose ends touch
     two shapes become one STRAIGHT/CURVED connector attached at both
     ends, the arrowhead polygon becoming the connector arrow.
4. A single BLANK slide is created and then filled in one `batchUpdate`
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
- Link labels don't follow connectors (Slides has no connector labels).
- With `-connect-curves`, Slides routes the connectors itself: the curves
  and the attachment points (edge midpoints) differ from the SVG.
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
- line spacing is capped at 115 %; looser pitches become `spaceAbove`;
- text inside a shape uses the same metrics within the shape's text
  rectangle (ECMA-376 presets: rounded rectangle inset 0.293·r with
  r = min(w,h)/6, ellipse 14.6 %, terminator 1018/21600 · 3163/21600);
  vertically centred, the block height is spaceAbove + first ascent +
  pitches + 0.195·size of the last line (+ its extra spacing), and
  overflow is centred too.

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
