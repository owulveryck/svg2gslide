// Package state persists what svg2gslide pushed to a presentation, so a later
// sync can tell a changed source from a slide a human has edited.
//
// The file lives next to the deck sources, which are already versioned: the
// state belongs in the same repository and its diffs are meant to be read.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SchemaVersion is the version this build writes. A file from a newer version
// is refused rather than misread.
const SchemaVersion = 1

// FileName is the default state file name.
const FileName = ".svg2gslide.json"

// State is the record of one presentation's last sync.
type State struct {
	SchemaVersion  int       `json:"schemaVersion"`
	PresentationID string    `json:"presentationId"`
	SyncedAt       time.Time `json:"syncedAt"`
	// RevisionID is informational: the Slides API documents it as opaque and
	// only valid for 24 hours, so it is never used as a drift reference.
	RevisionID string  `json:"revisionId,omitempty"`
	Entries    []Entry `json:"entries"`
}

// Entry records one synced slide.
type Entry struct {
	Source     string `json:"source"`  // path as written in the manifest
	Key        string `json:"key"`     // canonical identity, the primary key here
	SlideID    string `json:"slideId"` // Slides page object ID
	SVGID      string `json:"svgId,omitempty"`
	HTMLIndex  int    `json:"htmlIndex,omitempty"`
	SourceHash string `json:"sourceHash"` // sha256 of the SVG bytes converted

	// Pushed is the fingerprint of the slide as the server returned it right
	// after the push. Comparing it to a fresh read is what detects a human
	// edit.
	Pushed Fingerprint `json:"pushed"`
	// Origins ties each page element back to its SVG node, so a comment
	// anchored to an object from this push can still be reported against the
	// source.
	Origins []Origin `json:"origins,omitempty"`
}

// Fingerprint is the comparable projection of a slide.
type Fingerprint struct {
	Texts    []string `json:"texts"`
	Elements int      `json:"elements"`
	// Geometry is only filled when the user asked for geometric drift
	// detection: the Slides round-trip normalizes positions, so comparing it
	// by default would report edits nobody made.
	Geometry string `json:"geometry,omitempty"`
}

// Equal reports whether two fingerprints describe the same slide. An empty
// Geometry on either side is ignored, so a state written without geometry
// still compares cleanly against a geometric read.
func (f Fingerprint) Equal(other Fingerprint) bool {
	if f.Elements != other.Elements || !slices.Equal(f.Texts, other.Texts) {
		return false
	}
	if f.Geometry == "" || other.Geometry == "" {
		return true
	}
	return f.Geometry == other.Geometry
}

// Origin ties a Slides object to the SVG node that produced it.
type Origin struct {
	ObjectID string `json:"objectId"`
	Key      string `json:"key"`
	Locator  string `json:"locator"`
	SVGID    string `json:"svgId,omitempty"`
	Tag      string `json:"tag,omitempty"`
	// Text is what we wrote into the object, normalized. Comparing it to what
	// the object holds now is what turns a drift into a reportable "this
	// label was retyped" rather than an opaque fingerprint mismatch.
	Text string `json:"text,omitempty"`
}

// DefaultPath returns the state file path for a deck rooted at dir.
func DefaultPath(dir string) string {
	if dir == "" {
		dir = "."
	}
	return filepath.Join(dir, FileName)
}

// Load reads the state file. A missing file is not an error: it yields an
// empty state, which is how a first sync starts.
func Load(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &State{SchemaVersion: SchemaVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%s: written by a newer svg2gslide (schema %d, this build understands %d); upgrade rather than risk misreading it",
			path, st.SchemaVersion, SchemaVersion)
	}
	st.SchemaVersion = SchemaVersion
	return &st, nil
}

// Save writes the state atomically: a temporary file in the same directory,
// then a rename, so an interrupted run cannot leave a half-written state that
// the next sync would misread.
func (s *State) Save(path string) error {
	s.SchemaVersion = SchemaVersion
	// Sort by key so the file's diff shows what actually changed rather than
	// how the deck happened to be ordered.
	slices.SortFunc(s.Entries, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, FileName+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// Find returns the entry for a canonical key.
func (s *State) Find(key string) (Entry, bool) {
	for _, e := range s.Entries {
		if e.Key == key {
			return e, true
		}
	}
	return Entry{}, false
}

// Put inserts or replaces the entry with the same key.
func (s *State) Put(e Entry) {
	for i := range s.Entries {
		if s.Entries[i].Key == e.Key {
			s.Entries[i] = e
			return
		}
	}
	s.Entries = append(s.Entries, e)
}

// Remove drops the entry with that key, reporting whether one was there.
func (s *State) Remove(key string) bool {
	for i, e := range s.Entries {
		if e.Key == key {
			s.Entries = slices.Delete(s.Entries, i, i+1)
			return true
		}
	}
	return false
}

// SourceHash fingerprints the bytes of a source document.
func SourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
