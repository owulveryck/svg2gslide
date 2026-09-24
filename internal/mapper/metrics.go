package mapper

import "golang.org/x/text/unicode/norm"

// Arial advance widths (1/1000 em) for U+0020..U+007E, regular and bold.
// Arial is metric-compatible with Helvetica; italic uses regular widths.
var arialRegular = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

var arialBold = [95]int{
	278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
	975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
	333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
	611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584,
}

// arialExtra covers the typographic characters common in French slides.
var arialExtra = map[rune][2]int{
	'\u00a0': {278, 278},   // nbsp
	'\u202f': {200, 200},   // narrow nbsp
	'\u2009': {200, 200},   // thin space
	'\u2002': {500, 500},   // en space
	'\u2003': {1000, 1000}, // em space
	'\u2014': {1000, 1000}, // em dash
	'\u2013': {556, 556},   // en dash
	'\u2019': {222, 278}, '\u2018': {222, 278},
	'\u201c': {333, 500}, '\u201d': {333, 500},
	'\u00ab': {556, 556}, '\u00bb': {556, 556},
	'\u2022': {350, 350}, '\u00b7': {278, 278},
	'\u2026': {1000, 1000}, '\u00d7': {584, 584},
	'\u2192': {1000, 1000}, '\u2190': {1000, 1000},
	'\u2191': {500, 500}, '\u2193': {500, 500},
	'\u2264': {549, 549}, '\u2265': {549, 549},
	'\u20ac': {556, 556}, '\u00b0': {400, 400},
	'\u2212': {584, 584}, '\u00e6': {889, 889}, '\u0153': {944, 944},
	'\u00c6': {1000, 1000}, '\u0152': {1000, 1000}, '\u00df': {611, 611},
}

// runeWidth returns the Arial advance of c in em.
func runeWidth(c rune, bold bool) float64 {
	b := 0
	if bold {
		b = 1
	}
	if c >= 0x20 && c <= 0x7e {
		if bold {
			return float64(arialBold[c-0x20]) / 1000
		}
		return float64(arialRegular[c-0x20]) / 1000
	}
	if w, ok := arialExtra[c]; ok {
		return float64(w[b]) / 1000
	}
	// Accented Latin letters: width of the base letter.
	if d := []rune(norm.NFD.String(string(c))); len(d) > 1 && d[0] >= 0x20 && d[0] <= 0x7e {
		return runeWidth(d[0], bold)
	}
	if c >= 0x2000 {
		return 1.0 // symbols, dingbats, emoji: roughly square
	}
	return 0.6
}
