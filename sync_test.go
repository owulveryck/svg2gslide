package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/api/slides/v1"

	"github.com/owulveryck/svg2gslide/internal/deck"
	"github.com/owulveryck/svg2gslide/internal/state"
	"github.com/owulveryck/svg2gslide/internal/syncer"
)

const fixtureSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`

func TestParseForce(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"one source", "flow.svg", []string{"flow.svg"}},
		{"several, spaces trimmed", "a.svg, b.svg ,c.svg", []string{"a.svg", "b.svg", "c.svg"}},
		// "all" is handled by ForceAll, so it must not land in the set as if
		// a file were literally named "all".
		{"all is not a source name", "all", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseForce(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("parseForce(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for _, w := range tt.want {
				if !got[w] {
					t.Errorf("parseForce(%q) is missing %q", tt.in, w)
				}
			}
		})
	}
}

func TestResolveDeck(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.svg", fixtureSVG)
	write("b.svg", fixtureSVG)
	write("deck.txt", "# ordered\nb.svg\na.svg\n")

	t.Run("manifest order is kept", func(t *testing.T) {
		entries, deckDir, err := resolveDeck(syncFlags{deckFile: filepath.Join(dir, "deck.txt")})
		if err != nil {
			t.Fatal(err)
		}
		if deckDir != dir {
			t.Errorf("deckDir = %q, want the manifest's directory %q", deckDir, dir)
		}
		if len(entries) != 2 || entries[0].Source != "b.svg" || entries[1].Source != "a.svg" {
			t.Errorf("entries = %v, want b.svg then a.svg", sources(entries))
		}
	})

	t.Run("positional sources are appended after the manifest", func(t *testing.T) {
		entries, _, err := resolveDeck(syncFlags{
			deckFile: filepath.Join(dir, "deck.txt"),
			sources:  []string{filepath.Join(dir, "..", filepath.Base(dir), "a.svg")},
		})
		// a.svg is already in the manifest, so this must be refused as a
		// duplicate rather than silently producing two slides for one source.
		if err == nil {
			t.Fatalf("want a duplicate error, got %v", sources(entries))
		}
		if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("error = %v, want it to name the duplicate", err)
		}
	})

	t.Run("positional sources alone", func(t *testing.T) {
		entries, deckDir, err := resolveDeck(syncFlags{sources: []string{filepath.Join(dir, "a.svg")}})
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("entries = %v, want 1", sources(entries))
		}
		if deckDir != dir {
			t.Errorf("deckDir = %q, want %q so the state sits beside the sources", deckDir, dir)
		}
	})

	t.Run("stdin is refused", func(t *testing.T) {
		// A slide read from stdin has no path, so it cannot be identified on
		// the next run: sync would create a duplicate every time.
		_, _, err := resolveDeck(syncFlags{sources: []string{""}})
		if err == nil {
			t.Fatal("want an error for a stdin source")
		}
		if !strings.Contains(err.Error(), "stdin") {
			t.Errorf("error = %v, want it to explain why stdin cannot be synced", err)
		}
	})
}

func TestSyncCommandRejectsBadFlags(t *testing.T) {
	dir := t.TempDir()
	svg := filepath.Join(dir, "a.svg")
	if err := os.WriteFile(svg, []byte(fixtureSVG), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"unknown report format", []string{"-report", "yaml", "-dry-run", svg}, "text or json"},
		{"no source at all", []string{"-dry-run"}, "no source"},
		{"presentation required to write", []string{svg}, "-presentation is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := syncCommand(tt.args)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// TestSyncDryRunNeedsNoNetwork locks in that a dry run without a presentation
// reconciles and reports entirely offline.
func TestSyncDryRunNeedsNoNetwork(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.svg", "b.svg"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(fixtureSVG), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(dir, "report.json")
	err := syncCommand([]string{
		"-dry-run", "-report", "json", "-report-out", out,
		filepath.Join(dir, "a.svg"), filepath.Join(dir, "b.svg"),
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"dryRun": true`, `"declared": 2`, `"status": "create"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("report missing %q\n%s", want, data)
		}
	}
	// A dry run must not write a state file, under either name.
	written, err := filepath.Glob(filepath.Join(dir, ".svg2gslide*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(written) > 0 {
		t.Errorf("a dry run wrote state: %v", written)
	}
}

// TestProgramName locks in that a hint names something runnable: under
// "go run" the executable is a temporary build in the toolchain's cache, and
// printing its name gives a line that only looks like a command.
func TestProgramName(t *testing.T) {
	tests := []struct {
		name, exe, arg0, want string
	}{
		{
			name: "go run builds into the toolchain cache",
			exe:  "/tmp/go-build1234/b001/exe/svg2gslide",
			arg0: "/tmp/go-build1234/b001/exe/svg2gslide",
			want: "go run .",
		},
		{
			name: "so does go test",
			exe:  "/tmp/go-build99/b001/svg2gslide.test",
			arg0: "/tmp/go-build99/b001/svg2gslide.test",
			want: "go run .",
		},
		{
			name: "an installed binary is named as it was called",
			exe:  "/home/u/go/bin/svg2gslide",
			arg0: "svg2gslide",
			want: "svg2gslide",
		},
		{
			// A build sitting in the working directory is run as ./svg2gslide,
			// which is how it was called.
			name: "a local build keeps its spelling",
			exe:  "/home/u/src/svg2gslide/svg2gslide",
			arg0: "./svg2gslide",
			want: "svg2gslide",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := programName(tt.exe, tt.arg0); got != tt.want {
				t.Errorf("programName(%q, %q) = %q, want %q", tt.exe, tt.arg0, got, tt.want)
			}
		})
	}
}

// TestSyncHint locks in that a freshly created presentation tells you how to
// run the sync again, which is the alternative to reading its ID out of a URL
// by hand.
func TestSyncHint(t *testing.T) {
	const id = "1QcAfAtrbqEJZbLkFwD4QoPdtRQdMz7DmUnmEZBIr9_c"
	bin := invocation()

	tests := []struct {
		name         string
		presentation string
		deckFile     string
		slideSel     string
		sources      []string
		want         string
	}{
		{
			// The state file beside the deck names the presentation, so the
			// hint does not have to.
			name:    "the deck alone",
			sources: []string{"slides/flow.svg"},
			want:    bin + " sync slides/flow.svg",
		},
		{
			name:         "a presentation to name",
			presentation: id,
			sources:      []string{"slides/flow.svg"},
			want:         bin + " sync -presentation " + id + " slides/flow.svg",
		},
		{
			name:     "a manifest is the deck",
			deckFile: "deck.txt",
			want:     bin + " sync -deck deck.txt",
		},
		{
			// Which inline SVGs are taken is part of what the deck is.
			name:     "an HTML selection is carried over",
			slideSel: "1-3,7",
			sources:  []string{"talk.html"},
			want:     bin + " sync -slides 1-3,7 talk.html",
		},
		{
			name:    "a path a shell would split is quoted",
			sources: []string{"my slides/flow.svg"},
			want:    bin + " sync 'my slides/flow.svg'",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := syncHint(tt.presentation, tt.deckFile, tt.slideSel, tt.sources)
			if got != tt.want {
				t.Errorf("syncHint() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestResolveTarget covers how the presentation is named, which is also how
// the state file is found: the state is named after the presentation it
// records.
func TestResolveTarget(t *testing.T) {
	const idA = "1QcAfAtrbqEJZbLkFwD4QoPdtRQdMz7DmUnmEZBIr9_c"
	const idB = "1FCml6BnI5WlOKbuMjS-PtpN5dHItaNFViMxwPsxX4Fc"

	tracking := func(t *testing.T, ids ...string) string {
		t.Helper()
		dir := t.TempDir()
		for _, id := range ids {
			if err := (&state.State{PresentationID: id}).Save(state.DefaultPath(dir, id)); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}

	t.Run("new is left for the API to create", func(t *testing.T) {
		got, err := resolveTarget(syncFlags{presentation: "new"}, tracking(t, idA))
		if err != nil {
			t.Fatal(err)
		}
		if got != "new" {
			t.Errorf("target = %q, want %q even with a deck already tracked there", got, "new")
		}
	})

	t.Run("a URL is reduced to its ID", func(t *testing.T) {
		url := "https://docs.google.com/presentation/d/" + idA + "/edit#slide=id.svg2gslide_3ce50fb177"
		got, err := resolveTarget(syncFlags{presentation: url}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if got != idA {
			t.Errorf("target = %q, want %q", got, idA)
		}
	})

	t.Run("an unusable presentation is refused", func(t *testing.T) {
		// The ID becomes part of the state file's name, so it is never passed
		// through unchecked.
		_, err := resolveTarget(syncFlags{presentation: "../../etc"}, t.TempDir())
		if err == nil {
			t.Fatal("want an error for a string that is no presentation")
		}
	})

	t.Run("one tracked deck needs no flag", func(t *testing.T) {
		got, err := resolveTarget(syncFlags{}, tracking(t, idA))
		if err != nil {
			t.Fatal(err)
		}
		if got != idA {
			t.Errorf("target = %q, want the tracked %q", got, idA)
		}
	})

	t.Run("several tracked decks ask which one", func(t *testing.T) {
		_, err := resolveTarget(syncFlags{}, tracking(t, idA, idB))
		if err == nil {
			t.Fatal("want an error: nothing says which deck is meant")
		}
		for _, want := range []string{idA, idB, "-presentation"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %v, want it to mention %q", err, want)
			}
		}
	})

	t.Run("an explicit state file names its presentation", func(t *testing.T) {
		dir := tracking(t, idA)
		path := state.DefaultPath(dir, idA)
		got, err := resolveTarget(syncFlags{statePath: path}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if got != idA {
			t.Errorf("target = %q, want %q", got, idA)
		}
	})

	t.Run("nothing at all is an offline dry run", func(t *testing.T) {
		got, err := resolveTarget(syncFlags{dryRun: true}, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if got != "" {
			t.Errorf("target = %q, want none so the dry run stays offline", got)
		}
	})

	t.Run("nothing at all cannot be written to", func(t *testing.T) {
		_, err := resolveTarget(syncFlags{}, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "-presentation is required") {
			t.Fatalf("error = %v, want it to require -presentation", err)
		}
	})
}

// TestDefaultSlideIsNotPartOfTheDeck locks in what a fresh presentation must
// look like: one source means one slide. The blank slide the API adds on its
// own is hidden from the reconciliation, so it is neither reported as an
// orphan nor counted in the slide positions — the first push deletes it.
func TestDefaultSlideIsNotPartOfTheDeck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.svg")
	if err := os.WriteFile(path, []byte(fixtureSVG), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, _, err := resolveDeck(syncFlags{sources: []string{path}})
	if err != nil {
		t.Fatal(err)
	}

	defaultSlides := []string{"p"}
	live := &slides.Presentation{
		PresentationId: "1AbC",
		Slides:         []*slides.Page{{ObjectId: "p"}},
	}
	live.Slides = withoutSlides(live.Slides, defaultSlides)

	plan := syncer.Reconcile(entries, &state.State{}, live, nil, syncer.Options{})
	if len(plan.Orphans) != 0 {
		t.Errorf("orphans = %+v, want none: the blank slide is not a slide anyone added", plan.Orphans)
	}
	if len(plan.Slides) != 1 || plan.Slides[0].Action != syncer.ActionCreate {
		t.Fatalf("slides = %+v, want one create", plan.Slides)
	}
	if got := plan.Slides[0].TargetIndex; got != 0 {
		t.Errorf("target index = %d, want 0: the deck holds nothing else", got)
	}
	if len(plan.FinalOrder) != 1 {
		t.Errorf("final order = %v, want just the created slide", plan.FinalOrder)
	}
	// The deletion rides along with the first push rather than the plan's own
	// destructive call, so the presentation is never left with no slide.
	if len(plan.Deletions) != 0 {
		t.Errorf("deletions = %v, want the blank slide dropped by the push instead", plan.Deletions)
	}
}

func TestWithoutSlides(t *testing.T) {
	pages := []*slides.Page{{ObjectId: "p"}, {ObjectId: "a"}, {ObjectId: "b"}}

	t.Run("nothing to drop keeps the list", func(t *testing.T) {
		if got := withoutSlides(pages, nil); len(got) != 3 {
			t.Errorf("got %d pages, want all 3", len(got))
		}
	})

	t.Run("order of the rest is kept", func(t *testing.T) {
		got := withoutSlides(pages, []string{"a"})
		if len(got) != 2 || got[0].ObjectId != "p" || got[1].ObjectId != "b" {
			t.Errorf("got %v, want p then b", ids(got))
		}
	})
}

func ids(pages []*slides.Page) []string {
	var out []string
	for _, p := range pages {
		out = append(out, p.ObjectId)
	}
	return out
}

func sources(entries []deck.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Source)
	}
	return out
}
