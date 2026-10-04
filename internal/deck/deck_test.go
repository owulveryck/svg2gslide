package deck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const bareSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`

const htmlDeck = `<!DOCTYPE html>
<html><body>
<section class="slide" id="intro"><svg viewBox="0 0 10 10"><rect/></svg></section>
<section class="slide"><svg viewBox="0 0 10 10"><circle r="1"/></svg></section>
</body></html>`

func TestSlideID(t *testing.T) {
	valid := regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_\-:]*$`)

	t.Run("deterministic", func(t *testing.T) {
		if a, b := SlideID("slides/a.svg"), SlideID("slides/a.svg"); a != b {
			t.Errorf("same key gave %q and %q", a, b)
		}
	})
	t.Run("distinct keys differ", func(t *testing.T) {
		if a, b := SlideID("slides/a.svg"), SlideID("slides/b.svg"); a == b {
			t.Errorf("distinct keys both gave %q", a)
		}
	})
	t.Run("satisfies the API object-ID rules", func(t *testing.T) {
		for _, key := range []string{"", "a.svg", "deck.html#intro", "deck.html#@12", strings.Repeat("x/", 200)} {
			id := SlideID(key)
			if n := len(id); n < 5 || n > 50 {
				t.Errorf("key %q: length %d outside 5-50", key, n)
			}
			if !valid.MatchString(id) {
				t.Errorf("key %q: id %q has forbidden characters", key, id)
			}
			if !strings.HasPrefix(id, SlideIDPrefix) {
				t.Errorf("key %q: id %q lacks the prefix", key, id)
			}
		}
	})
}

func TestCanonicalKeyIgnoresPathSpelling(t *testing.T) {
	for _, p := range []string{"slides/a.svg", "./slides/a.svg", "slides/../slides/a.svg"} {
		if got, want := canonical(p), "slides/a.svg"; got != want {
			t.Errorf("canonical(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestHTMLKey(t *testing.T) {
	tests := []struct {
		name           string
		svgID          string
		index          int
		wantKey        string
		wantPositional bool
	}{
		{"id wins and is stable", "intro", 1, "deck.html#intro", false},
		{"index is a flagged fallback", "", 3, "deck.html#@3", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, positional := htmlKey("deck.html", tt.svgID, tt.index)
			if key != tt.wantKey || positional != tt.wantPositional {
				t.Errorf("htmlKey() = %q,%v; want %q,%v", key, positional, tt.wantKey, tt.wantPositional)
			}
		})
	}
}

func TestParseManifest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("comments and blank lines", func(t *testing.T) {
		p := write("deck.txt", "# the deck\n\na.svg\n  b.svg  # trailing comment\n\nsub/c.svg\n")
		written, resolved, err := ParseManifest(p)
		if err != nil {
			t.Fatal(err)
		}
		wantWritten := []string{"a.svg", "b.svg", "sub/c.svg"}
		if strings.Join(written, ",") != strings.Join(wantWritten, ",") {
			t.Errorf("written = %v, want %v", written, wantWritten)
		}
		// Resolved paths are relative to the manifest, so the deck works
		// from any working directory.
		if resolved[2] != filepath.Join(dir, "sub/c.svg") {
			t.Errorf("resolved[2] = %q, want %q", resolved[2], filepath.Join(dir, "sub/c.svg"))
		}
	})

	t.Run("rejects absolute paths", func(t *testing.T) {
		p := write("abs.txt", "/etc/a.svg\n")
		if _, _, err := ParseManifest(p); err == nil {
			t.Fatal("want an error for an absolute path")
		}
	})

	t.Run("rejects an empty manifest", func(t *testing.T) {
		p := write("empty.txt", "# nothing but a comment\n")
		if _, _, err := ParseManifest(p); err == nil {
			t.Fatal("want an error for a manifest with no source")
		}
	})
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	svgPath := filepath.Join(dir, "a.svg")
	htmlPath := filepath.Join(dir, "deck.html")
	for p, c := range map[string]string{svgPath: bareSVG, htmlPath: htmlDeck} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("bare SVG gives one entry", func(t *testing.T) {
		got, err := Resolve([]string{"a.svg"}, []string{svgPath}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d entries, want 1", len(got))
		}
		e := got[0]
		if e.Key != "a.svg" || e.SlideID != SlideID("a.svg") {
			t.Errorf("key/id = %q/%q", e.Key, e.SlideID)
		}
		if e.HTMLIndex != 0 || e.Positional {
			t.Errorf("a bare SVG should not be positional: index=%d positional=%v", e.HTMLIndex, e.Positional)
		}
	})

	t.Run("HTML gives one entry per inline svg", func(t *testing.T) {
		got, err := Resolve([]string{"deck.html"}, []string{htmlPath}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d entries, want 2", len(got))
		}
		if got[0].Key != "deck.html#intro" || got[0].Positional {
			t.Errorf("entry 0: key=%q positional=%v, want keyed by id", got[0].Key, got[0].Positional)
		}
		if got[1].Key != "deck.html#@2" || !got[1].Positional {
			t.Errorf("entry 1: key=%q positional=%v, want a flagged positional fallback", got[1].Key, got[1].Positional)
		}
	})

	t.Run("selection filters an HTML input", func(t *testing.T) {
		got, err := Resolve([]string{"deck.html"}, []string{htmlPath}, Options{Selection: "2"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].HTMLIndex != 2 {
			t.Fatalf("got %d entries (first index %d), want only index 2", len(got), got[0].HTMLIndex)
		}
	})

	t.Run("selection on a bare SVG is an error", func(t *testing.T) {
		if _, err := Resolve([]string{"a.svg"}, []string{svgPath}, Options{Selection: "1"}); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("duplicate source is an error", func(t *testing.T) {
		_, err := Resolve([]string{"a.svg", "./a.svg"}, []string{svgPath, svgPath}, Options{})
		if err == nil {
			t.Fatal("want an error for the same source declared twice")
		}
		if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("error = %v, want it to mention a duplicate", err)
		}
	})

	t.Run("the same file under two spellings is an error", func(t *testing.T) {
		// Relative in a manifest, absolute on the command line: the identity
		// keys differ, so only a check on the file itself catches it. Without
		// that, one drawing would quietly become two slides.
		_, err := Resolve([]string{"a.svg", svgPath}, []string{svgPath, svgPath}, Options{})
		if err == nil {
			t.Fatal("want an error for the same file declared twice")
		}
		if !strings.Contains(err.Error(), "same file") {
			t.Errorf("error = %v, want it to explain that the file is already declared", err)
		}
	})

	t.Run("stdin", func(t *testing.T) {
		got, err := Resolve([]string{""}, []string{""}, Options{Stdin: strings.NewReader(bareSVG)})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Label != "<stdin>" {
			t.Fatalf("got %d entries, label %q", len(got), got[0].Label)
		}
	})

	t.Run("order follows the declaration", func(t *testing.T) {
		got, err := Resolve([]string{"deck.html", "a.svg"}, []string{htmlPath, svgPath}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, e := range got {
			keys = append(keys, e.Key)
		}
		want := "deck.html#intro,deck.html#@2,a.svg"
		if strings.Join(keys, ",") != want {
			t.Errorf("keys = %v, want %s", keys, want)
		}
	})
}

func TestParseSelection(t *testing.T) {
	tests := []struct {
		name    string
		sel     string
		keep    []int
		drop    []int
		wantErr bool
	}{
		{name: "empty keeps all", sel: "", keep: []int{1, 2, 99}},
		{name: "single", sel: "3", keep: []int{3}, drop: []int{2, 4}},
		{name: "range", sel: "2-4", keep: []int{2, 3, 4}, drop: []int{1, 5}},
		{name: "open range", sel: "10-", keep: []int{10, 1000}, drop: []int{9}},
		{name: "mixed", sel: "1-3,7,10-", keep: []int{1, 3, 7, 10}, drop: []int{4, 8}},
		{name: "zero is invalid", sel: "0", wantErr: true},
		{name: "reversed range", sel: "5-2", wantErr: true},
		{name: "not a number", sel: "a", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keep, err := parseSelection(tt.sel)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, i := range tt.keep {
				if !keep(i) {
					t.Errorf("index %d should be kept", i)
				}
			}
			for _, i := range tt.drop {
				if keep(i) {
					t.Errorf("index %d should be dropped", i)
				}
			}
		})
	}
}
