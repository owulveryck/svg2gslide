// Package report renders what a sync found, in two forms from one model: a
// JSON document complete enough to hand to an LLM so it can fix the source
// SVGs without reading the deck, and a text rendering of the same data.
package report

import "time"

// SchemaVersion is the version of the JSON document this package emits.
const SchemaVersion = 1

// Report is the whole outcome of a sync.
type Report struct {
	SchemaVersion   int       `json:"schemaVersion"`
	PresentationID  string    `json:"presentationId"`
	PresentationURL string    `json:"presentationUrl"`
	GeneratedAt     time.Time `json:"generatedAt"`
	// DryRun says whether anything was actually written.
	DryRun bool `json:"dryRun"`
	// CommentSource says how comments were read, and therefore how precisely
	// they could be attributed. "unavailable" means they could not be read at
	// all — which is not the same as there being none.
	CommentSource string `json:"commentSource"`
	// BackupURL is the snapshot copy taken before writing, when one was asked
	// for. There is no named-version API, so a copy is the only snapshot.
	BackupURL string   `json:"backupUrl,omitempty"`
	Summary   Summary  `json:"summary"`
	Slides    []Slide  `json:"slides"`
	Orphans   []Orphan `json:"orphans,omitempty"`
	// UnattributedComments holds the comments no slide could be tied to.
	// They are listed rather than dropped: a comment nobody sees is worse
	// than one in the wrong place.
	UnattributedComments []Comment `json:"unattributedComments,omitempty"`
}

// Summary counts the outcomes.
type Summary struct {
	Declared  int `json:"declared"`
	Created   int `json:"created"`
	Replaced  int `json:"replaced"`
	Unchanged int `json:"unchanged"`
	Conflicts int `json:"conflicts"`
	Skipped   int `json:"skipped"`
	Moved     int `json:"moved"`
	Deleted   int `json:"deleted"`
	Orphans   int `json:"orphans"`
}

// Slide is the outcome for one declared source.
type Slide struct {
	Source string `json:"source"`
	// Label names the slide the way a human reads it: the source path, plus
	// which inline <svg> of it and that slide's title for an HTML deck.
	Label      string `json:"label,omitempty"`
	SourceHash string `json:"sourceHash,omitempty"`
	SVGID      string `json:"svgId,omitempty"`
	HTMLIndex  int    `json:"htmlIndex,omitempty"`
	SlideID    string `json:"slideId"`
	SlideURL   string `json:"slideUrl,omitempty"`
	// SlideIndex is the 0-based position the slide holds once the sync is
	// done, or -1 if it has none.
	SlideIndex int `json:"slideIndex"`
	// PreviousIndex is where it sat before, -1 when it did not exist.
	PreviousIndex int    `json:"previousIndex"`
	Status        string `json:"status"`
	Action        string `json:"action"`
	// Reasons are stable tokens explaining the status.
	Reasons []string `json:"reasons,omitempty"`
	// Remediation spells out what to do, in prose, so the report can be acted
	// on without knowing the tool's flags.
	Remediation string `json:"remediation,omitempty"`
	// Divergences are the differences between what we pushed and what the
	// slide holds now, each tied back to a node of the source SVG.
	Divergences []Divergence `json:"divergences,omitempty"`
	Comments    []Comment    `json:"comments,omitempty"`
}

// Kinds of divergence.
const (
	KindTextChanged    = "text-changed"
	KindElementAdded   = "element-added"
	KindElementRemoved = "element-removed"
	KindGeometry       = "geometry-changed"
)

// Divergence is one difference between the push and the live slide.
type Divergence struct {
	Kind     string `json:"kind"`
	ObjectID string `json:"objectId,omitempty"`
	// SVG locates the source node to edit. Nil when the element has no
	// counterpart in the source, which is itself the finding.
	SVG *Node `json:"svg,omitempty"`
	// Pushed is what we wrote, Current what the slide holds now.
	Pushed  string `json:"pushed,omitempty"`
	Current string `json:"current,omitempty"`
	Note    string `json:"note,omitempty"`
}

// Node locates a node in the source SVG precisely enough to edit it.
type Node struct {
	Locator string `json:"locator"`
	ID      string `json:"id,omitempty"`
	Tag     string `json:"tag,omitempty"`
}

// Comment is one comment thread, with where it points in the source.
type Comment struct {
	CommentID  string  `json:"commentId"`
	Status     string  `json:"status"`
	Author     string  `json:"author,omitempty"`
	CreatedAt  string  `json:"createdAt,omitempty"`
	QuotedText string  `json:"quotedText,omitempty"`
	Anchor     *Anchor `json:"anchor,omitempty"`
	Thread     []Post  `json:"thread,omitempty"`
	// Candidates lists the slides the quoted text matched when attribution
	// was ambiguous.
	Candidates []string `json:"candidates,omitempty"`
}

// Anchor says what a comment is attached to, and how sure that is.
type Anchor struct {
	ObjectID  string `json:"objectId,omitempty"`
	SVG       *Node  `json:"svg,omitempty"`
	TextRange *Range `json:"textRange,omitempty"`
	// Confidence is "exact", "quoted-text-match", "ambiguous" or
	// "unresolved". Anything but "exact" means the anchor was inferred.
	Confidence string `json:"confidence"`
}

// Range is a character range inside an element's text.
type Range struct {
	StartIndex int64 `json:"startIndex"`
	EndIndex   int64 `json:"endIndex"`
}

// Post is one message of a comment thread.
type Post struct {
	Author string `json:"author,omitempty"`
	At     string `json:"at,omitempty"`
	Text   string `json:"text"`
}

// Orphan is a slide the deck does not account for.
type Orphan struct {
	Kind       string `json:"kind"`
	SlideID    string `json:"slideId"`
	SlideIndex int    `json:"slideIndex"`
	Title      string `json:"title,omitempty"`
	Source     string `json:"source,omitempty"`
	Deleted    bool   `json:"deleted"`
	Note       string `json:"note,omitempty"`
	// Comments are the threads anchored in this slide. An orphan is left
	// untouched, so they are not a conflict — but a comment the report stayed
	// silent about is one nobody reads, and -prune would destroy it.
	Comments []Comment `json:"comments,omitempty"`
}
