package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/owulveryck/svg2gslide/internal/deck"
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
	// A dry run must not write the state file.
	if _, err := os.Stat(filepath.Join(dir, ".svg2gslide.json")); err == nil {
		t.Error("a dry run wrote the state file")
	}
}

func sources(entries []deck.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Source)
	}
	return out
}
