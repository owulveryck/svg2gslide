package mapper

import (
	"math"
	"testing"

	"google.golang.org/api/slides/v1"
)

func insertedTexts(reqs []*slides.Request) []string {
	var out []string
	for _, r := range reqs {
		if r.InsertText != nil {
			out = append(out, r.InsertText.Text)
		}
	}
	return out
}

func TestLinesGroupedIntoOneBox(t *testing.T) {
	// Hand-written diagrams: one <text> per line, regular pitch.
	reqs, _ := mapSVG(t, `<svg viewBox="0 0 1600 900">
  <rect x="60" y="196" width="440" height="110"/>
  <text x="75" y="220" font-size="13.5" font-weight="700">Model Garden</text>
  <text x="75" y="240" font-size="11">• Fiches modèles</text>
  <text x="75" y="258" font-size="11">• Playground</text>
  <text x="75" y="276" font-size="11">• Statuts</text>
  <text x="75" y="330" font-size="11">far below: new box</text>
</svg>`)
	got := insertedTexts(reqs)
	want := []string{"Model Garden\n• Fiches modèles\n• Playground\n• Statuts", "far below: new box"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("texts = %q, want %q", got, want)
	}
}

func TestWordsJoinedIntoCentredLines(t *testing.T) {
	// PlantUML: one <text> per word, NBSP separators, centred lines with
	// measured textLength, a blank spacer line.
	reqs, _ := mapSVG(t, `<svg viewBox="0 0 1600 900">
  <text font-size="16" font-weight="700" textLength="31.5547" x="394.3907" y="386.287">Dev</text>
  <text font-size="16" font-weight="700" textLength="5.2734" x="425.9454" y="386.287">&#160;</text>
  <text font-size="16" font-weight="700" textLength="48.3984" x="431.2188" y="386.287">Portal</text>
  <text font-size="14" textLength="4.4297" x="434.7891" y="403.1972">&#160;</text>
  <text font-size="14" textLength="28.1846" x="370.5484" y="419.6855">P&#244;le</text>
  <text font-size="14" textLength="4.4297" x="398.7329" y="419.6855">&#160;</text>
  <text font-size="14" textLength="32.5117" x="403.1626" y="419.6855">Tech</text>
  <text font-size="14" textLength="4.4297" x="435.6744" y="419.6855">&#160;</text>
  <text font-size="14" textLength="63.3555" x="440.104" y="419.6855">(Existant)</text>
</svg>`)
	got := insertedTexts(reqs)
	if len(got) != 1 || got[0] != "Dev Portal\nPôle Tech (Existant)" {
		t.Fatalf("texts = %q, want one centred two-line box", got)
	}
	for _, r := range reqs {
		if p := r.UpdateParagraphStyle; p != nil && p.Style.Alignment != "" && p.Style.Alignment != "CENTER" {
			t.Errorf("alignment = %q, want CENTER", p.Style.Alignment)
		}
	}
	// The blank spacer line must reappear as extra space above line 2.
	found := false
	for _, r := range reqs {
		if p := r.UpdateParagraphStyle; p != nil && p.Style.SpaceAbove != nil && p.Style.SpaceAbove.Magnitude > 0 {
			found = true
		}
	}
	if !found {
		t.Error("no spaceAbove for the gap left by the blank line")
	}
}

func TestSeparateColumnsNotMerged(t *testing.T) {
	// Table cells on the same row, and rows too far apart, stay separate.
	reqs, _ := mapSVG(t, `<svg viewBox="0 0 1600 900">
  <text x="52" y="123" font-size="11">SLA, haute disponibilité</text>
  <text x="590" y="123" font-size="11" text-anchor="middle">A / R</text>
  <text x="52" y="153" font-size="11">Latence, TTFT</text>
  <text x="590" y="153" font-size="11" text-anchor="middle">A / R</text>
</svg>`)
	if got := insertedTexts(reqs); len(got) != 4 {
		t.Errorf("texts = %q, want 4 separate boxes", got)
	}
}

func TestPitchModelInverse(t *testing.T) {
	for _, c := range []struct{ d, prev, cur float64 }{{20, 13.5, 11}, {18, 11, 11}, {9, 11, 11}, {33, 16, 14}} {
		p := spacingFor(c.d, c.prev, c.cur)
		if got := naturalPitch(p, c.prev, c.cur); math.Abs(got-c.d) > 1e-9 {
			t.Errorf("naturalPitch(spacingFor(%v)) = %v", c, got)
		}
	}
	// Same size: pitch = 1.2·size·p (calibrated on exported PDFs).
	if got := naturalPitch(1.5, 20, 20); math.Abs(got-36) > 1e-9 {
		t.Errorf("pitch(150%%, 20pt) = %v, want 36", got)
	}
}

func TestFontFallbackList(t *testing.T) {
	reqs, _ := mapSVG(t, `<svg viewBox="0 0 1600 900">
  <text x="10" y="20" font-family='"Helvetica Neue", Helvetica, Arial, -apple-system, sans-serif'>a</text>
  <text x="10" y="200" font-family="-apple-system, BlinkMacSystemFont, sans-serif">b</text>
  <text x="10" y="400" font-family="Outfit, sans-serif">c</text>
</svg>`)
	var fonts []string
	for _, r := range reqs {
		if u := r.UpdateTextStyle; u != nil && u.Style.FontFamily != "" {
			fonts = append(fonts, u.Style.FontFamily)
		}
	}
	want := []string{"Arial", "Arial", "Outfit"}
	if len(fonts) != 3 || fonts[0] != want[0] || fonts[1] != want[1] || fonts[2] != want[2] {
		t.Errorf("fonts = %v, want %v", fonts, want)
	}
}
