// Package deck resolves the ordered list of SVG sources that makes up a
// presentation and gives each one a stable identity, so that a later sync can
// tell which slide came from which source.
package deck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/owulveryck/svg2gslide/internal/htmlsvg"
)

// SlideIDPrefix starts every slide object ID this tool creates. It is what
// distinguishes our slides from those a human added by hand.
const SlideIDPrefix = "svg2gslide_"

// Entry is one slide of the declared deck, in order.
type Entry struct {
	Source string // path as written (manifest line or command-line argument), "" for stdin
	Label  string // human-readable origin, used in messages
	Data   []byte // the SVG document

	// Key is the canonical identity of this slide within the deck, and the
	// only input to SlideID.
	Key string
	// SlideID is the deterministic Google Slides page object ID derived from
	// Key, so a slide can be found again without consulting any stored state.
	SlideID string

	HTMLIndex int    // 1-based index of the inline <svg>, 0 for a bare SVG file
	SVGID     string // id of the <svg> or of its slide container, "" when neither has one
	// Positional reports that Key falls back to HTMLIndex because the inline
	// <svg> carries no id. Such a slide loses its identity if the HTML deck is
	// reordered, which callers should surface to the user.
	Positional bool
}

// Options tunes how sources are resolved.
type Options struct {
	// Selection restricts an HTML input to some of its inline SVGs, 1-based,
	// e.g. "1-3,7,10-". Empty means all. It is an error on a bare SVG.
	Selection string
	// Stdin is read when a source path is empty. Defaults to os.Stdin.
	Stdin io.Reader
}

// ParseManifest reads a deck manifest: one source path per line, "#" starting
// a comment, blank lines ignored. Relative paths are interpreted against the
// manifest's own directory so the manifest works from any working directory,
// but the returned paths keep the spelling written in the file, which is what
// gives a slide a cwd-independent identity.
func ParseManifest(path string) (written, resolved []string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	dir := filepath.Dir(path)
	for i, line := range strings.Split(string(data), "\n") {
		if idx := strings.IndexByte(line, '#'); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if filepath.IsAbs(line) {
			return nil, nil, fmt.Errorf("%s:%d: absolute path %q; use a path relative to the manifest so the deck stays portable", path, i+1, line)
		}
		written = append(written, line)
		resolved = append(resolved, filepath.Join(dir, line))
	}
	if len(written) == 0 {
		return nil, nil, fmt.Errorf("%s: no source listed", path)
	}
	return written, resolved, nil
}

// Resolve reads each source and expands it into entries: a bare SVG yields one,
// an HTML page one per selected inline <svg>.
//
// written and readFrom are parallel: written holds the paths as the user spelled
// them (they feed each entry's identity) and readFrom the paths to actually
// open. Pass the same slice twice when there is no manifest indirection.
func Resolve(written, readFrom []string, opt Options) ([]Entry, error) {
	if len(written) != len(readFrom) {
		return nil, fmt.Errorf("resolve: %d written paths for %d read paths", len(written), len(readFrom))
	}
	keep, err := parseSelection(opt.Selection)
	if err != nil {
		return nil, err
	}

	var out []Entry
	var (
		seenKey  = map[string]string{} // identity key  -> label that claimed it
		seenFile = map[string]string{} // physical file -> label that claimed it
	)
	for i, path := range readFrom {
		entries, err := resolveOne(written[i], path, opt, keep)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if prev, dup := seenKey[e.Key]; dup {
				return nil, fmt.Errorf("duplicate source %q: already declared as %s", e.Label, prev)
			}
			seenKey[e.Key] = e.Label
			// Also catch the same file declared under two spellings (a
			// relative path in the manifest and an absolute one on the
			// command line): the keys differ, so it would otherwise quietly
			// become two slides of the same drawing.
			if phys := physical(path, e.HTMLIndex); phys != "" {
				if prev, dup := seenFile[phys]; dup {
					return nil, fmt.Errorf("duplicate source %q: the same file is already declared as %s", e.Label, prev)
				}
				seenFile[phys] = e.Label
			}
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no source to convert")
	}
	return out, nil
}

func resolveOne(written, path string, opt Options, keep func(int) bool) ([]Entry, error) {
	var (
		data  []byte
		err   error
		label = written
	)
	if path == "" {
		stdin := opt.Stdin
		if stdin == nil {
			stdin = os.Stdin
		}
		data, err = io.ReadAll(stdin)
		label = "<stdin>"
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}

	if !htmlsvg.IsHTML(data) {
		if opt.Selection != "" {
			return nil, fmt.Errorf("%s: a slide selection requires an HTML input", label)
		}
		key := canonical(written)
		return []Entry{{
			Source:  written,
			Label:   label,
			Data:    data,
			Key:     key,
			SlideID: SlideID(key),
		}}, nil
	}

	svgs, err := htmlsvg.Extract(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	var out []Entry
	for _, s := range svgs {
		if !keep(s.Index) {
			continue
		}
		l := fmt.Sprintf("%s#%d", label, s.Index)
		if s.Title != "" {
			l += " (" + s.Title + ")"
		}
		key, positional := htmlKey(written, s.ID, s.Index)
		out = append(out, Entry{
			Source:     written,
			Label:      l,
			Data:       s.Data,
			Key:        key,
			SlideID:    SlideID(key),
			HTMLIndex:  s.Index,
			SVGID:      s.ID,
			Positional: positional,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no inline <svg> selected (%d found)", label, len(svgs))
	}
	return out, nil
}

// physical identifies the file on disk a source reads from, so the same file
// reached by two different spellings can be spotted. It returns "" for stdin,
// and for a path that cannot be resolved — in which case opening it will fail
// with a better message anyway.
func physical(path string, htmlIndex int) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs + "#" + strconv.Itoa(htmlIndex)
}

// canonical normalizes a source path so the same file spelled differently
// yields the same identity.
func canonical(path string) string {
	if path == "" {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(path))
}

// htmlKey builds the identity of one inline <svg>. It prefers the SVG's id,
// which survives the deck being reordered; the 1-based index is only a
// fallback, and the returned flag says so.
func htmlKey(path, svgID string, index int) (key string, positional bool) {
	if svgID != "" {
		return canonical(path) + "#" + svgID, false
	}
	return canonical(path) + "#@" + strconv.Itoa(index), true
}

// SlideID derives the Google Slides page object ID from a canonical key.
//
// The result is 21 characters, within the API's 5-50 range, starts with a
// letter and uses only [a-zA-Z0-9_], so it satisfies the object-ID rules of
// CreateSlideRequest.
func SlideID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return SlideIDPrefix + hex.EncodeToString(sum[:5])
}

// parseSelection compiles a list of 1-based indexes and ranges ("1-3,7,10-")
// into a predicate. An empty selection keeps everything.
func parseSelection(sel string) (func(int) bool, error) {
	if strings.TrimSpace(sel) == "" {
		return func(int) bool { return true }, nil
	}
	type span struct{ lo, hi int }
	var spans []span
	for part := range strings.SplitSeq(sel, ",") {
		part = strings.TrimSpace(part)
		loS, hiS, isRange := strings.Cut(part, "-")
		lo, err := strconv.Atoi(loS)
		if err != nil || lo < 1 {
			return nil, fmt.Errorf("slide selection: invalid item %q", part)
		}
		hi := lo
		if isRange {
			if hiS == "" {
				hi = int(^uint(0) >> 1)
			} else if hi, err = strconv.Atoi(hiS); err != nil || hi < lo {
				return nil, fmt.Errorf("slide selection: invalid range %q", part)
			}
		}
		spans = append(spans, span{lo, hi})
	}
	return func(i int) bool {
		for _, s := range spans {
			if i >= s.lo && i <= s.hi {
				return true
			}
		}
		return false
	}, nil
}
