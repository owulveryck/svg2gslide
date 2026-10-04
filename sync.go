package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/auth"
	"github.com/owulveryck/svg2gslide/internal/convert"
	"github.com/owulveryck/svg2gslide/internal/deck"
	"github.com/owulveryck/svg2gslide/internal/gslide"
	"github.com/owulveryck/svg2gslide/internal/mapper"
	"github.com/owulveryck/svg2gslide/internal/report"
	"github.com/owulveryck/svg2gslide/internal/state"
	"github.com/owulveryck/svg2gslide/internal/syncer"
)

// syncFlags holds everything the sync subcommand accepts.
type syncFlags struct {
	presentation string
	credentials  string
	deckFile     string
	statePath    string
	force        string
	prune        bool
	backup       bool
	backupTitle  string
	reportFormat string
	reportOut    string
	geometry     bool
	dryRun       bool
	slideSel     string
	phase        string
	verbose      bool
	textTf       bool
	connect      bool
	sources      []string
}

func syncUsage(fs *flag.FlagSet) func() {
	return func() {
		fmt.Fprintf(os.Stderr, `Usage:
  %s sync -presentation <id|url|new> [flags] [file.svg ...]

Reconciles a presentation with an ordered list of SVG sources: the list is the
deck. A changed source replaces its slide; unchanged slides are left alone.
A slide edited or commented on in Google Slides is reported, never silently
overwritten.

Flags:
`, filepath.Base(os.Args[0]))
		fs.PrintDefaults()
	}
}

func syncCommand(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	fs.Usage = syncUsage(fs)
	var f syncFlags
	fs.StringVar(&f.presentation, "presentation", "", `target presentation ID or URL, or "new" to create one (required)`)
	fs.StringVar(&f.credentials, "credentials", "", "OAuth client or service account JSON (default: $SVG2GSLIDE_CREDENTIALS)")
	fs.StringVar(&f.deckFile, "deck", "", "manifest listing the sources in order, one path per line (# comments)")
	fs.StringVar(&f.statePath, "state", "", "sync state file (default: "+state.FileName+" beside the deck)")
	fs.StringVar(&f.force, "force", "", `overwrite the slides of these sources despite a conflict: comma-separated, or "all"`)
	fs.BoolVar(&f.prune, "prune", false, "delete the slides svg2gslide created that the deck no longer declares")
	fs.BoolVar(&f.backup, "backup", false, "copy the presentation before writing (there is no named-version API; a copy is the only snapshot)")
	fs.StringVar(&f.backupTitle, "backup-title", "", "title of the -backup copy (default: the presentation's title plus a timestamp)")
	fs.StringVar(&f.reportFormat, "report", "text", "report format: text or json (json carries the source node of every divergence and comment)")
	fs.StringVar(&f.reportOut, "report-out", "", "write the report to this file instead of stdout")
	fs.BoolVar(&f.geometry, "geometry", false, "also compare element positions when detecting drift (noisier: the Slides round-trip moves things)")
	fs.BoolVar(&f.dryRun, "dry-run", false, "reconcile and report without writing anything")
	fs.StringVar(&f.slideSel, "slides", "", `HTML inputs only: which inline SVGs to take, 1-based, e.g. "1-3,7,10-"`)
	fs.StringVar(&f.phase, "phase", "", "active phase (default: the SVG's data-active-phase attribute)")
	fs.BoolVar(&f.verbose, "v", false, "log skipped and approximated elements")
	fs.BoolVar(&f.textTf, "text-transform", false, "apply CSS text-transform as browsers do")
	fs.BoolVar(&f.connect, "connect-curves", false, "replace edges between shapes by attached connectors")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f.sources = fs.Args()

	if f.reportFormat != "text" && f.reportFormat != "json" {
		return fmt.Errorf("-report must be text or json, got %q", f.reportFormat)
	}
	if f.deckFile == "" && len(f.sources) == 0 {
		fs.Usage()
		return fmt.Errorf("no source given: pass SVG files, or a manifest with -deck")
	}
	if !f.dryRun && f.presentation == "" {
		return fmt.Errorf(`-presentation is required (an ID, a URL, or "new")`)
	}
	return runSync(context.Background(), f)
}

func runSync(ctx context.Context, f syncFlags) error {
	entries, deckDir, err := resolveDeck(f)
	if err != nil {
		return err
	}
	statePath := f.statePath
	if statePath == "" {
		statePath = state.DefaultPath(deckDir)
	}
	st, err := state.Load(statePath)
	if err != nil {
		return err
	}

	opt := syncer.Options{
		Force:        parseForce(f.force),
		ForceAll:     strings.TrimSpace(f.force) == "all",
		Prune:        f.prune,
		WithGeometry: f.geometry,
	}

	// A dry run with no presentation reconciles against an empty deck, which
	// is enough to show what a first sync would create — and costs no API
	// call at all.
	if f.dryRun && f.presentation == "" {
		plan := syncer.Reconcile(entries, st, &slides.Presentation{}, nil, opt)
		return emitReport(f, report.Build(report.Input{
			Plan: plan, State: st, Comments: &syncer.Comments{Source: syncer.SourceUnavailable}, DryRun: true,
		}))
	}

	client, err := gslide.NewClient(ctx, auth.ResolveCredentials(f.credentials))
	if err != nil {
		return err
	}

	presentationID, err := resolvePresentation(ctx, client, f, st, entries)
	if err != nil {
		return err
	}
	if st.PresentationID != "" && st.PresentationID != presentationID {
		return fmt.Errorf("%s records presentation %s, but %s was given; use a different -state for a different deck",
			statePath, st.PresentationID, presentationID)
	}

	live, withComments, err := client.GetPresentationWithComments(ctx, presentationID)
	if err != nil {
		return err
	}
	comments := readComments(ctx, client, presentationID, live, withComments)

	plan := syncer.Reconcile(entries, st, live, comments.OpenCounts(), opt)

	pageW, pageH, err := pageSize(live)
	if err != nil {
		return err
	}

	// Convert only what will be pushed: an unchanged deck does no work.
	conversions := map[string]*convert.Result{}
	for _, sp := range plan.Slides {
		if !sp.Writes() {
			continue
		}
		res, err := convertEntry(sp.Entry, pageW, pageH, f)
		if err != nil {
			return fmt.Errorf("%s: %w", sp.Entry.Label, err)
		}
		if f.verbose {
			for _, w := range res.Warnings {
				fmt.Fprintln(os.Stderr, "  [approx]", w)
			}
		}
		conversions[sp.Entry.Key] = res
	}

	in := report.Input{Plan: plan, State: st, Live: live, Comments: comments, DryRun: f.dryRun, Now: time.Now().UTC()}
	if f.dryRun || !plan.Writes() {
		return emitReport(f, report.Build(in))
	}

	if f.backup {
		url, err := backup(ctx, client, presentationID, live, f.backupTitle)
		if err != nil {
			return err
		}
		in.BackupURL = url
		fmt.Fprintln(os.Stderr, "backup:", url)
	}

	if err := apply(ctx, client, presentationID, live.RevisionId, plan, conversions); err != nil {
		return err
	}

	// One read gives every touched slide's fingerprint, compared like with
	// like against future reads.
	after, err := client.GetPresentation(ctx, presentationID)
	if err != nil {
		return err
	}
	updateState(st, presentationID, after, plan, conversions, f.geometry)
	if err := st.Save(statePath); err != nil {
		return err
	}

	in.Live = after
	return emitReport(f, report.Build(in))
}

// resolveDeck turns the flags into the ordered deck, and says which directory
// the state file belongs in.
func resolveDeck(f syncFlags) (entries []deck.Entry, deckDir string, err error) {
	var written, readFrom []string
	if f.deckFile != "" {
		written, readFrom, err = deck.ParseManifest(f.deckFile)
		if err != nil {
			return nil, "", err
		}
		deckDir = filepath.Dir(f.deckFile)
	}
	for _, s := range f.sources {
		if s == "" {
			return nil, "", fmt.Errorf("sync reads no stdin: a slide needs a path to be identified across runs")
		}
		written = append(written, s)
		readFrom = append(readFrom, s)
	}
	if deckDir == "" && len(f.sources) > 0 {
		deckDir = filepath.Dir(f.sources[0])
	}
	entries, err = deck.Resolve(written, readFrom, deck.Options{Selection: f.slideSel})
	return entries, deckDir, err
}

func parseForce(s string) map[string]bool {
	out := map[string]bool{}
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" && part != "all" {
			out[part] = true
		}
	}
	return out
}

func resolvePresentation(ctx context.Context, client *gslide.Client, f syncFlags, st *state.State, entries []deck.Entry) (string, error) {
	if f.presentation == "new" {
		if st.PresentationID != "" {
			return "", fmt.Errorf(`-presentation new, but the state already tracks %s; drop the state file or name the presentation`, st.PresentationID)
		}
		title := "svg2gslide"
		if len(entries) > 0 {
			title = strings.TrimSuffix(filepath.Base(entries[0].Source), filepath.Ext(entries[0].Source))
		}
		id, err := client.CreatePresentation(ctx, title)
		if err != nil {
			return "", err
		}
		fmt.Printf("presentation created: https://docs.google.com/presentation/d/%s/edit\n", id)
		return id, nil
	}
	if id, err := convert.ExtractPresentationID(f.presentation); err == nil {
		return id, nil
	}
	return f.presentation, nil
}

// readComments gets the comments the best way available, and never fails the
// sync over them: the report states which source was used.
func readComments(ctx context.Context, client *gslide.Client, presentationID string, live *slides.Presentation, withComments bool) *syncer.Comments {
	if withComments {
		return syncer.FromPresentation(live)
	}
	items, err := client.ListDriveComments(ctx, presentationID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "warning: comments could not be read:", err)
		return &syncer.Comments{Source: syncer.SourceUnavailable}
	}
	return syncer.FromDrive(items, live)
}

func pageSize(pres *slides.Presentation) (w, h float64, err error) {
	if pres.PageSize == nil || pres.PageSize.Width == nil || pres.PageSize.Height == nil {
		return 0, 0, fmt.Errorf("presentation %s has no page size", pres.PresentationId)
	}
	return pres.PageSize.Width.Magnitude, pres.PageSize.Height.Magnitude, nil
}

func convertEntry(e deck.Entry, pageW, pageH float64, f syncFlags) (*convert.Result, error) {
	return convert.Convert(convert.Input{
		SVG:           bytes.NewReader(e.Data),
		Label:         e.Label,
		PageW:         pageW,
		PageH:         pageH,
		SlideID:       e.SlideID,
		Phase:         f.phase,
		Verbose:       f.verbose,
		TextTransform: f.textTf,
		ConnectCurves: f.connect,
	})
}

func backup(ctx context.Context, client *gslide.Client, presentationID string, live *slides.Presentation, title string) (string, error) {
	if title == "" {
		base := live.Title
		if base == "" {
			base = "presentation"
		}
		title = fmt.Sprintf("%s — svg2gslide %s", base, time.Now().UTC().Format(time.RFC3339))
	}
	id, err := client.CopyPresentation(ctx, presentationID, title)
	if err != nil {
		return "", err
	}
	return "https://docs.google.com/presentation/d/" + id + "/edit", nil
}

// apply writes the plan.
//
// The destructive step comes first, alone, in one atomic call guarded by the
// revision read at the start: if anyone edited the deck in the meantime it
// fails and nothing is lost. Everything after it only adds.
//
// A replace empties its slide and refills it, so a failure between the two
// leaves that slide blank. Re-running the sync fixes it, and -backup is the
// belt for the cases where that is not good enough.
func apply(ctx context.Context, client *gslide.Client, presentationID, revisionID string, plan *syncer.Plan, conversions map[string]*convert.Result) error {
	var destructive []*slides.Request
	for _, sp := range plan.Slides {
		for _, id := range sp.ClearElements {
			destructive = append(destructive, &slides.Request{DeleteObject: &slides.DeleteObjectRequest{ObjectId: id}})
		}
	}
	for _, id := range plan.Deletions {
		destructive = append(destructive, &slides.Request{DeleteObject: &slides.DeleteObjectRequest{ObjectId: id}})
	}
	if err := client.BatchUpdateAtRevision(ctx, presentationID, destructive, revisionID); err != nil {
		return fmt.Errorf("clearing what the sync replaces: %w", err)
	}

	for _, sp := range plan.Slides {
		res := conversions[sp.Entry.Key]
		if res == nil {
			continue
		}
		reqs := res.Requests
		if sp.Action == syncer.ActionReplace {
			// The slide already exists and has just been emptied: creating it
			// again would collide with its own object ID.
			reqs = withoutCreateSlide(reqs)
		}
		if err := pushSlide(ctx, client, presentationID, res, reqs); err != nil {
			return fmt.Errorf("%s: %w", sp.Entry.Label, err)
		}
		fmt.Printf("%s: slide %s %s\n", sp.Entry.Label, sp.SlideID, sp.Action)
	}

	var moves []*slides.Request
	for _, m := range plan.Moves {
		moves = append(moves, &slides.Request{UpdateSlidesPosition: &slides.UpdateSlidesPositionRequest{
			SlideObjectIds: []string{m.SlideID},
			InsertionIndex: int64(m.InsertionIndex),
		}})
	}
	if err := client.BatchUpdateAtRevision(ctx, presentationID, moves, ""); err != nil {
		return fmt.Errorf("putting the deck in declared order: %w", err)
	}
	return nil
}

// withoutCreateSlide drops the CreateSlide request convert prepends.
func withoutCreateSlide(reqs []*slides.Request) []*slides.Request {
	out := make([]*slides.Request, 0, len(reqs))
	for _, r := range reqs {
		if r.CreateSlide == nil {
			out = append(out, r)
		}
	}
	return out
}

// pushSlide sends a slide's requests, hosting embedded images in a second
// pass as the one-shot path does.
func pushSlide(ctx context.Context, client *gslide.Client, presentationID string, res *convert.Result, reqs []*slides.Request) error {
	var imageReqs []*slides.Request
	if len(res.Images) > 0 {
		main := reqs[:0:0]
		for _, q := range reqs {
			if q.CreateImage != nil && strings.HasPrefix(q.CreateImage.Url, mapper.ImagePlaceholderPrefix) {
				imageReqs = append(imageReqs, q)
				continue
			}
			main = append(main, q)
		}
		reqs = main
	}
	if err := client.BatchUpdate(ctx, presentationID, reqs); err != nil {
		return err
	}
	if len(imageReqs) > 0 {
		if err := insertImages(ctx, client, presentationID, res, imageReqs); err != nil {
			fmt.Fprintln(os.Stderr, "warning: embedded images skipped:", err)
		}
	}
	return nil
}

// updateState records what was pushed, so the next sync can tell a changed
// source from an edited slide.
func updateState(st *state.State, presentationID string, after *slides.Presentation, plan *syncer.Plan, conversions map[string]*convert.Result, withGeometry bool) {
	st.PresentationID = presentationID
	st.SyncedAt = time.Now().UTC()
	st.RevisionID = after.RevisionId

	pages := map[string]*slides.Page{}
	for _, p := range after.Slides {
		pages[p.ObjectId] = p
	}

	for _, sp := range plan.Slides {
		res := conversions[sp.Entry.Key]
		if res == nil || !sp.Writes() {
			continue
		}
		page := pages[sp.SlideID]
		if page == nil {
			// The slide is not in the deck we just read: something went wrong
			// upstream, and recording a fingerprint for it would be a lie.
			continue
		}
		st.Put(state.Entry{
			Source:     sp.Entry.Source,
			Key:        sp.Entry.Key,
			SlideID:    sp.SlideID,
			SVGID:      sp.Entry.SVGID,
			HTMLIndex:  sp.Entry.HTMLIndex,
			SourceHash: state.SourceHash(sp.Entry.Data),
			Pushed:     syncer.Fingerprint(page, withGeometry),
			Origins:    originsOf(res),
		})
	}

	// Clear records whose slide is neither declared nor present any more.
	for _, o := range plan.Orphans {
		if o.Kind == syncer.OrphanVanished || o.Delete {
			for _, e := range slices.Clone(st.Entries) {
				if e.SlideID == o.SlideID {
					st.Remove(e.Key)
				}
			}
		}
	}
}

// originsOf pairs the mapper's provenance with the text actually written into
// each object, which the InsertText requests carry.
func originsOf(res *convert.Result) []state.Origin {
	texts := map[string]string{}
	for _, r := range res.Requests {
		if r.InsertText != nil {
			texts[r.InsertText.ObjectId] += r.InsertText.Text
		}
	}
	out := make([]state.Origin, 0, len(res.Origins))
	for _, o := range res.Origins {
		out = append(out, state.Origin{
			ObjectID: o.ObjectID,
			Key:      o.Key,
			Locator:  o.Locator,
			SVGID:    o.SVGID,
			Tag:      o.Tag,
			Text:     strings.Join(strings.Fields(texts[o.ObjectID]), " "),
		})
	}
	return out
}

func emitReport(f syncFlags, r *report.Report) error {
	out := os.Stdout
	if f.reportOut != "" {
		fh, err := os.Create(f.reportOut)
		if err != nil {
			return err
		}
		defer func() { _ = fh.Close() }()
		out = fh
	}
	if f.reportFormat == "json" {
		return r.WriteJSON(out)
	}
	return r.WriteText(out)
}
