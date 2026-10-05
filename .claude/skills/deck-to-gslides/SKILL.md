---
name: deck-to-gslides
description: Write a slide deck from a prompt with mdslides, publish it to Google Slides as native editable shapes with svg2gslide, then bring the reviewers' comments back and apply them to the markdown source. Use whenever asked to build or update a deck, to push a deck or SVG slides to Google Slides, or to apply review comments from a presentation.
---

# From a prompt to a reviewed deck

Two tools, one loop. [mdslides](https://github.com/owulveryck/mdslides) turns a
markdown deck into one SVG per slide; [svg2gslide](https://github.com/owulveryck/svg2gslide)
pushes those SVGs into Google Slides as **native editable objects** and brings the
comments left on them back as JSON. You write the markdown, humans comment in Slides,
you apply the comments to the markdown. The generated SVGs are never the source.

## Once per machine

```sh
go install github.com/owulveryck/mdslides@latest
go install github.com/owulveryck/svg2gslide@latest
svg2gslide login          # prints the whole credential procedure if none is found
```

Inside either checkout, `go run .` replaces the binary.

## 1. Write the deck

`mdslides guide` is the authoring contract — read it before writing a slide. It is in
French and it is binding. `mdslides layouts` lists the layouts, `mdslides layouts
<nom>` prints a paste-ready block.

```sh
mdslides new deck.md               # -template minimal for a short deck
mdslides guide                     # all of it; mdslides guide "La charte" for one section
```

A one-line prompt is enough to start — *"a 5-slide deck arguing we move our CI to
managed runners"*. From it: one slide per idea, **the title is the assertion the slide
proves**, and **never invent a figure, a date or a source** — write
`"[à compléter : …]"` instead, in quotes, and list what is left to the author.

If the deck's repo also has the `mdslides` skill installed, follow it for the
authoring details.

## 2. Render

```sh
mdslides -format lecture deck.md   # -> deck/gen/lecture/NN_<layout>.svg   <- publish these
mdslides -format salle   deck.md   # the talk itself: dark, stepped
```

Fix every error, and fix or justify every warning, before publishing. To fit `salle`,
remove text or reserve it for `lecture` — never shrink a size, never force a colour.

**Publish the `lecture` SVGs, never `salle`.** `lecture` renders final states only, so
it emits exactly one file per slide; `salle` splits a stepped slide into
`NN_<layout>.stepK.svg` and its numbering shifts.

## 3. Publish

```sh
svg2gslide sync -dry-run deck/gen/lecture/*.svg                    # no API call at all
svg2gslide sync -presentation new deck/gen/lecture/*.svg
```

The glob order is the deck order. Sync prints the presentation URL and writes
`.svg2gslide-<presentationID>.json` next to the sources — **commit it**: that record is
what later tells *"the source changed"* from *"a human edited the slide"*. Once a deck
is synced the target is implicit, so `-presentation` is only needed to create one.

## 4. Bring the review back

```sh
svg2gslide sync -dry-run -report json -report-out review.json deck/gen/lecture/*.svg
```

Read, per slide: `status`, `reasons[]`, `remediation`, `divergences[]` (what a human
changed in the slide) and `comments[]` (`quotedText`, `anchor.svg.locator`, `thread[]`).

A top-level `commentSource: "unavailable"` means the comments could **not be read** —
that is not "no comments". Say so rather than report a clean review.

## 5. Apply the review in the markdown

The filename is the map: in `lecture`, `NN` is the slide's position in `deck.md`,
counting the `#` divider and every `##`. So `04_quad.svg` is the 4th slide. A slide
with no ```` ```slide ```` block renders no SVG, leaves a gap in the numbering, and
never reaches Google Slides.

Edit `deck.md`, then go round again:

```sh
mdslides -format lecture deck.md && svg2gslide sync deck/gen/lecture/*.svg
```

**Never edit anything under `deck/gen/`** — the next compile overwrites it and
deletes the orphans.

A slide reported `conflict` means the source and the slide both moved. Port the
divergence into `deck.md` first, recompile, and only then let the source win:

```sh
svg2gslide sync -force deck/gen/lecture/04_quad.svg deck/gen/lecture/*.svg
```

`-force` matches the source exactly as the report's `source` field spells it.
`-force all` discards every human edit — don't reach for it to clear a conflict.
`-prune` deletes the slides the deck no longer declares and destroys their comment
threads: ask before running it.

## Done when

- zero errors in both formats, warnings justified in the speaker notes;
- every open comment applied, or answered to its author in the presentation;
- remaining `[à compléter]` listed to the author;
- the state file committed next to the deck.

## Install this skill next to the deck

The deck usually lives in neither tool's repo. Copy this directory into the
`.claude/skills/` of the repo where `deck.md` lives, alongside mdslides' own
`mdslides` skill.
