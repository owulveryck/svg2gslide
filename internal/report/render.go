package report

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// WriteJSON emits the report as indented JSON. This is the form meant to be
// handed to an LLM: every divergence and comment carries the source node to
// edit, so the deck never has to be read.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText emits the same content for a human reader.
func (r *Report) WriteText(w io.Writer) error {
	var b strings.Builder

	for _, s := range r.Slides {
		name := s.Label
		if name == "" {
			name = s.Source
		}
		fmt.Fprintf(&b, "%s  %s  %s\n", position(s.SlideIndex), name, headline(s))

		for _, d := range s.Divergences {
			writeDivergence(&b, d)
		}
		for _, c := range s.Comments {
			writeComment(&b, c)
		}
		// Show the advice whenever there is something to act on, including a
		// slide identified only by its position, which is a warning the user
		// would otherwise never see.
		actionable := len(s.Divergences) > 0 || len(s.Comments) > 0 ||
			s.Action == "skipped" || slices.Contains(s.Reasons, "positional-identity")
		if s.Remediation != "" && actionable {
			fmt.Fprintf(&b, "    -> %s\n", s.Remediation)
		}
	}

	for _, o := range r.Orphans {
		label := o.Title
		if label == "" {
			label = o.Source
		}
		if label == "" {
			label = o.SlideID
		}
		fmt.Fprintf(&b, "%s  %-24s %s\n", position(o.SlideIndex), label, orphanHeadline(o))
		for _, c := range o.Comments {
			writeComment(&b, c)
		}
		if o.Note != "" && len(o.Comments) > 0 {
			fmt.Fprintf(&b, "    -> %s\n", o.Note)
		}
	}

	if len(r.UnattributedComments) > 0 {
		fmt.Fprintf(&b, "\n%d comment(s) could not be tied to a slide:\n", len(r.UnattributedComments))
		for _, c := range r.UnattributedComments {
			writeComment(&b, c)
		}
	}

	fmt.Fprintf(&b, "\n%s\n", summaryLine(r))
	// With no presentation there was nothing to read comments from, so
	// saying they are unavailable would be a false alarm.
	if r.CommentSource == "unavailable" && r.PresentationID != "" {
		b.WriteString("comments could not be read, so this report says nothing about them\n")
	} else if r.CommentSource == "drive-fallback" && r.hasComments() {
		// Said only when there is an attribution to qualify: a deck with no
		// comments reaches Drive on every sync, and the caveat would then be a
		// standing line about nothing.
		b.WriteString("comments read through Drive, whose anchors are opaque: they are tied to slides by quoted text\n")
	}
	if r.BackupURL != "" {
		fmt.Fprintf(&b, "backup: %s\n", r.BackupURL)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// hasComments reports whether any thread made it into the report, wherever it
// landed.
func (r *Report) hasComments() bool {
	if len(r.UnattributedComments) > 0 {
		return true
	}
	for _, s := range r.Slides {
		if len(s.Comments) > 0 {
			return true
		}
	}
	for _, o := range r.Orphans {
		if len(o.Comments) > 0 {
			return true
		}
	}
	return false
}

func position(i int) string {
	if i < 0 {
		return "slide   -"
	}
	return fmt.Sprintf("slide %3d", i)
}

// headline is the one-line verdict for a slide.
func headline(s Slide) string {
	switch s.Status {
	case "unchanged":
		return "unchanged"
	case "create":
		return "created"
	case "replace":
		return "source changed -> replaced"
	case "drifted":
		return "DRIFTED  (edited in Slides; the source did not change)"
	case "adopt":
		if s.Action == "replace" {
			return "adopted -> replaced"
		}
		return "ADOPTION  (no record of a previous sync)"
	case "missing":
		if s.Action == "create" {
			return "was deleted -> recreated"
		}
		return "MISSING  (deleted in Slides)"
	case "conflict":
		verdict := "CONFLICT"
		if s.Action == "replace" {
			verdict = "CONFLICT -> overwritten (forced)"
		}
		if len(s.Reasons) > 0 {
			return verdict + "  (" + strings.Join(humanReasons(s.Reasons), ", ") + ")"
		}
		return verdict
	}
	return s.Status
}

func humanReasons(reasons []string) []string {
	var out []string
	for _, r := range reasons {
		switch r {
		case "source-changed":
			out = append(out, "source changed")
		case "slide-edited":
			out = append(out, "slide edited")
		case "open-comments":
			out = append(out, "open comments")
		case "no-baseline":
			out = append(out, "no sync record")
		case "slide-deleted":
			out = append(out, "slide deleted")
		case "forced":
			out = append(out, "forced")
		case "positional-identity":
			out = append(out, "positional identity")
		}
	}
	return out
}

func writeDivergence(b *strings.Builder, d Divergence) {
	switch d.Kind {
	case KindTextChanged:
		fmt.Fprintf(b, "    . text edited %s\n", where(d.SVG))
		fmt.Fprintf(b, "      pushed  : %q\n", d.Pushed)
		fmt.Fprintf(b, "      current : %q\n", d.Current)
	case KindElementAdded:
		fmt.Fprintf(b, "    . element added in Slides: %q (nothing in the source produced it)\n", d.Current)
	case KindElementRemoved:
		fmt.Fprintf(b, "    . element deleted in Slides %s, held %q\n", where(d.SVG), d.Pushed)
	case KindGeometry:
		fmt.Fprintf(b, "    . %s\n", d.Note)
	}
}

func writeComment(b *strings.Builder, c Comment) {
	who := c.Author
	if who == "" {
		who = "someone"
	}
	fmt.Fprintf(b, "    . comment by %s", who)
	if c.CreatedAt != "" {
		fmt.Fprintf(b, ", %s", c.CreatedAt)
	}
	fmt.Fprintf(b, ", %s\n", c.Status)

	if c.Anchor != nil {
		if c.Anchor.SVG != nil {
			fmt.Fprintf(b, "      anchored %s", where(c.Anchor.SVG))
		} else if c.Anchor.ObjectID != "" {
			fmt.Fprintf(b, "      anchored on object %s", c.Anchor.ObjectID)
		} else {
			fmt.Fprintf(b, "      anchor unresolved")
		}
		if c.QuotedText != "" {
			fmt.Fprintf(b, ", quoting %q", c.QuotedText)
		}
		if c.Anchor.Confidence != "" && c.Anchor.Confidence != "exact" {
			fmt.Fprintf(b, " [%s]", c.Anchor.Confidence)
		}
		b.WriteByte('\n')
	}
	if len(c.Candidates) > 0 {
		fmt.Fprintf(b, "      could be any of: %s\n", strings.Join(c.Candidates, ", "))
	}
	for _, p := range c.Thread {
		fmt.Fprintf(b, "      %q\n", p.Text)
	}
}

// where renders a source node the way a human would go find it.
func where(n *Node) string {
	if n == nil {
		return "(source node unknown)"
	}
	if n.ID != "" {
		return fmt.Sprintf("on %s (id=%q)", n.Locator, n.ID)
	}
	return "on " + n.Locator
}

func orphanHeadline(o Orphan) string {
	switch {
	case o.Kind == "slide-vanished":
		return "stale record (slide gone); will be cleared from the state"
	case o.Deleted:
		return "orphan, deleted"
	case len(o.Comments) > 0:
		return fmt.Sprintf("orphan, left untouched (%d comment thread(s))", len(o.Comments))
	default:
		return "orphan, left untouched"
	}
}

func summaryLine(r *Report) string {
	var parts []string
	add := func(n int, label string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, label))
		}
	}
	add(r.Summary.Created, "created")
	add(r.Summary.Replaced, "replaced")
	add(r.Summary.Moved, "moved")
	add(r.Summary.Deleted, "deleted")
	add(r.Summary.Unchanged, "unchanged")
	add(r.Summary.Conflicts, "needing attention")
	add(r.Summary.Orphans, "orphan")
	if len(parts) == 0 {
		parts = append(parts, "nothing to do")
	}
	prefix := ""
	if r.DryRun {
		prefix = "dry run: "
	}
	return prefix + strings.Join(parts, ", ")
}
