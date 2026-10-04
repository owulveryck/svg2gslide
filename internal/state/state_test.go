package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	st, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing state file must start a first sync, got %v", err)
	}
	if len(st.Entries) != 0 || st.SchemaVersion != SchemaVersion {
		t.Errorf("got %d entries, schema %d", len(st.Entries), st.SchemaVersion)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := DefaultPath(t.TempDir(), "1AbC")
	want := &State{
		PresentationID: "1AbC",
		SyncedAt:       time.Date(2026, 10, 4, 12, 4, 0, 0, time.UTC),
		RevisionID:     "rev-1",
		Entries: []Entry{{
			Source:     "slides/flow.svg",
			Key:        "slides/flow.svg",
			SlideID:    "svg2gslide_9f3a1c2b4d",
			SourceHash: SourceHash([]byte("<svg/>")),
			Pushed:     Fingerprint{Texts: []string{"Service A", "Validation"}, Elements: 7},
			Origins: []Origin{{
				ObjectID: "svg2gslide_9f3a1c2b4d_a1b2c3d4",
				Key:      "#auth-label",
				Locator:  "/svg/g[2]/text[1]",
				SVGID:    "auth-label",
				Tag:      "text",
			}},
		}},
	}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.PresentationID != want.PresentationID || !got.SyncedAt.Equal(want.SyncedAt) {
		t.Errorf("header = %q/%v, want %q/%v", got.PresentationID, got.SyncedAt, want.PresentationID, want.SyncedAt)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(got.Entries))
	}
	e := got.Entries[0]
	if e.Key != "slides/flow.svg" || e.SlideID != "svg2gslide_9f3a1c2b4d" {
		t.Errorf("entry = %q/%q", e.Key, e.SlideID)
	}
	if !e.Pushed.Equal(want.Entries[0].Pushed) {
		t.Errorf("fingerprint did not survive the round trip: %+v", e.Pushed)
	}
	if len(e.Origins) != 1 || e.Origins[0].Locator != "/svg/g[2]/text[1]" {
		t.Errorf("origins = %+v", e.Origins)
	}
}

func TestSaveIsReadableAndSortedForDiffs(t *testing.T) {
	path := DefaultPath(t.TempDir(), "1AbC")
	st := &State{Entries: []Entry{
		{Key: "c.svg", SlideID: "c"},
		{Key: "a.svg", SlideID: "a"},
		{Key: "b.svg", SlideID: "b"},
	}}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "\n  ") {
		t.Error("state should be indented so its diffs are readable")
	}
	if !strings.HasSuffix(text, "\n") {
		t.Error("state should end with a newline")
	}
	ia, ib, ic := strings.Index(text, `"a.svg"`), strings.Index(text, `"b.svg"`), strings.Index(text, `"c.svg"`)
	if !(ia < ib && ib < ic) {
		t.Errorf("entries should be sorted by key to keep diffs minimal; offsets %d,%d,%d", ia, ib, ic)
	}
}

func TestLoadRefusesANewerSchema(t *testing.T) {
	path := DefaultPath(t.TempDir(), "1AbC")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":99,"entries":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("want an error for a state written by a newer build")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Errorf("error = %v, want it to explain the version mismatch", err)
	}
}

func TestLoadAcceptsAnOlderSchemaAndUpgradesIt(t *testing.T) {
	path := DefaultPath(t.TempDir(), "1AbC")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":0,"entries":[{"key":"a.svg","slideId":"a"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.SchemaVersion != SchemaVersion {
		t.Errorf("schema = %d, want it upgraded to %d", st.SchemaVersion, SchemaVersion)
	}
	if _, ok := st.Find("a.svg"); !ok {
		t.Error("the older file's entry was lost")
	}
}

func TestDefaultPathNamesThePresentation(t *testing.T) {
	a := DefaultPath("slides", "1AbC")
	b := DefaultPath("slides", "2DeF")
	if a == b {
		t.Fatal("two presentations must not share a state file: that is the whole point of the name")
	}
	if !strings.Contains(a, "1AbC") {
		t.Errorf("path %q should name the presentation it records", a)
	}
	if filepath.Dir(a) != "slides" {
		t.Errorf("path %q should sit beside the deck", a)
	}
	if got := filepath.Dir(DefaultPath("", "1AbC")); got != "." {
		t.Errorf("an unnamed deck directory should be the working one, got %q", got)
	}
}

func TestDiscoverListsEveryDeckTrackedBesideTheSources(t *testing.T) {
	t.Run("missing directory records nothing", func(t *testing.T) {
		ids, err := Discover(filepath.Join(t.TempDir(), "never-synced"))
		if err != nil {
			t.Fatalf("a deck that was never synced is not an error, got %v", err)
		}
		if len(ids) != 0 {
			t.Errorf("got %v, want no presentation", ids)
		}
	})

	t.Run("one file per presentation, sorted", func(t *testing.T) {
		dir := t.TempDir()
		for _, id := range []string{"2DeF", "1AbC"} {
			if err := (&State{PresentationID: id}).Save(DefaultPath(dir, id)); err != nil {
				t.Fatal(err)
			}
		}
		// Neither a source nor a leftover temp file is a state file.
		if err := os.WriteFile(filepath.Join(dir, "a.svg"), []byte("<svg/>"), 0o644); err != nil {
			t.Fatal(err)
		}
		ids, err := Discover(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 2 || ids[0] != "1AbC" || ids[1] != "2DeF" {
			t.Errorf("got %v, want [1AbC 2DeF]", ids)
		}
	})

	t.Run("a legacy file is found by what it records", func(t *testing.T) {
		dir := t.TempDir()
		if err := (&State{PresentationID: "1AbC"}).Save(filepath.Join(dir, LegacyFileName)); err != nil {
			t.Fatal(err)
		}
		ids, err := Discover(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != "1AbC" {
			t.Errorf("got %v, want the legacy file's presentation [1AbC]", ids)
		}
	})
}

func TestLoadForMigratesTheLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyFileName)
	old := &State{PresentationID: "1AbC", Entries: []Entry{{
		Key: "a.svg", SlideID: "svg2gslide_a", SourceHash: SourceHash([]byte("<svg/>")),
		Pushed: Fingerprint{Texts: []string{"Service A"}, Elements: 1},
	}}}
	if err := old.Save(legacy); err != nil {
		t.Fatal(err)
	}

	st, path, err := LoadFor(dir, "1AbC")
	if err != nil {
		t.Fatal(err)
	}
	if path != DefaultPath(dir, "1AbC") {
		t.Errorf("save path = %q, want the per-presentation name", path)
	}
	// Losing the baseline would cost a whole deck's fingerprints and report
	// every slide as one to adopt.
	if _, ok := st.Find("a.svg"); !ok {
		t.Fatal("the legacy baseline was not carried over")
	}

	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Error("the legacy file should be gone once the deck is migrated")
	}
	again, _, err := LoadFor(dir, "1AbC")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Find("a.svg"); !ok {
		t.Error("the migrated file lost the baseline")
	}
}

func TestLoadForIgnoresALegacyFileForAnotherPresentation(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyFileName)
	if err := (&State{PresentationID: "1AbC", Entries: []Entry{{Key: "a.svg"}}}).Save(legacy); err != nil {
		t.Fatal(err)
	}

	st, path, err := LoadFor(dir, "2DeF")
	if err != nil {
		t.Fatal(err)
	}
	if st.PresentationID != "" || len(st.Entries) != 0 {
		t.Errorf("another deck's state leaked in: %+v", st)
	}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("another deck's legacy file must be left alone, got %v", err)
	}
}

func TestFindPutRemove(t *testing.T) {
	st := &State{}

	t.Run("find on empty", func(t *testing.T) {
		if _, ok := st.Find("a.svg"); ok {
			t.Error("found an entry in an empty state")
		}
	})
	t.Run("put inserts", func(t *testing.T) {
		st.Put(Entry{Key: "a.svg", SlideID: "first"})
		got, ok := st.Find("a.svg")
		if !ok || got.SlideID != "first" {
			t.Errorf("got %+v, ok=%v", got, ok)
		}
	})
	t.Run("put replaces in place", func(t *testing.T) {
		st.Put(Entry{Key: "a.svg", SlideID: "second"})
		if len(st.Entries) != 1 {
			t.Fatalf("got %d entries, want the first one replaced", len(st.Entries))
		}
		got, _ := st.Find("a.svg")
		if got.SlideID != "second" {
			t.Errorf("slideID = %q, want %q", got.SlideID, "second")
		}
	})
	t.Run("remove", func(t *testing.T) {
		if !st.Remove("a.svg") {
			t.Error("Remove reported nothing removed")
		}
		if st.Remove("a.svg") {
			t.Error("Remove reported a second removal")
		}
		if len(st.Entries) != 0 {
			t.Errorf("got %d entries after removal", len(st.Entries))
		}
	})
}

func TestFingerprintEqual(t *testing.T) {
	base := Fingerprint{Texts: []string{"a", "b"}, Elements: 2}

	tests := []struct {
		name  string
		a, b  Fingerprint
		equal bool
	}{
		{"identical", base, base, true},
		{"text changed", base, Fingerprint{Texts: []string{"a", "c"}, Elements: 2}, false},
		{"text order matters", base, Fingerprint{Texts: []string{"b", "a"}, Elements: 2}, false},
		{"element added", base, Fingerprint{Texts: []string{"a", "b"}, Elements: 3}, false},
		{
			name:  "geometry absent on one side is ignored",
			a:     base,
			b:     Fingerprint{Texts: []string{"a", "b"}, Elements: 2, Geometry: "g1"},
			equal: true,
		},
		{
			name:  "geometry compared when both sides have it",
			a:     Fingerprint{Texts: []string{"a", "b"}, Elements: 2, Geometry: "g1"},
			b:     Fingerprint{Texts: []string{"a", "b"}, Elements: 2, Geometry: "g2"},
			equal: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Equal(tt.b); got != tt.equal {
				t.Errorf("Equal() = %v, want %v", got, tt.equal)
			}
		})
	}
}

func TestSourceHash(t *testing.T) {
	a, b := SourceHash([]byte("<svg/>")), SourceHash([]byte("<svg/>"))
	if a != b {
		t.Error("same bytes gave different hashes")
	}
	if a == SourceHash([]byte("<svg />")) {
		t.Error("different bytes gave the same hash")
	}
	if !strings.HasPrefix(a, "sha256:") {
		t.Errorf("hash %q should name its algorithm", a)
	}
}
