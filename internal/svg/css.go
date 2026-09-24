package svg

import (
	"regexp"
	"strconv"
	"strings"
)

// The stylesheet evaluator supports exactly the CSS subset used by the
// phase-driven SDLC diagrams: flat rules whose last selector component is a
// class, an id, a tag, or a tag[attr] test, optionally guarded by
// [data-active-phase="N"] conditions on an ancestor (possibly inside :is()).
// Only the `opacity` declaration is interpreted; @keyframes, @supports and
// animation/transition rules are ignored.

type matcherKind int

const (
	matchClass matcherKind = iota
	matchID
	matchTag
	matchTagAttr
)

type matcher struct {
	kind matcherKind
	name string // class name, id, or tag
	attr string // attribute name for matchTagAttr
}

func (m matcher) matches(e *Element) bool {
	switch m.kind {
	case matchClass:
		return e.HasClass(m.name)
	case matchID:
		return e.Attrs["id"] == m.name
	case matchTag:
		return e.Tag == m.name
	case matchTagAttr:
		if m.name != "" && e.Tag != m.name {
			return false
		}
		_, ok := e.Attrs[m.attr]
		return ok
	}
	return false
}

type rule struct {
	phases   map[string]bool // nil = applies in every phase
	matchers []matcher
	opacity  float64
}

// styleRule is a flat, unconditional rule (single compound selector) whose
// presentation declarations are cascaded onto matching elements.
type styleRule struct {
	matchers    []matcher
	specificity int
	order       int
	decls       [][2]string
}

// Stylesheet holds the opacity rules extracted from a <style> block, plus
// the presentation declarations of simple selectors.
type Stylesheet struct {
	rules  []rule
	styles []styleRule
}

// cascadedProps lists the declarations promoted from stylesheet rules into
// element attributes (opacity is handled separately by Opacity).
var cascadedProps = map[string]bool{
	"fill": true, "fill-opacity": true, "stroke": true, "stroke-width": true,
	"stroke-dasharray": true, "stroke-opacity": true, "font-family": true,
	"font-size": true, "font-weight": true, "font-style": true,
	"text-anchor": true, "letter-spacing": true, "text-transform": true,
	"visibility": true, "display": true, "dominant-baseline": true,
	"stop-color": true, "stop-opacity": true, "text-decoration": true,
}

var (
	commentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
	phaseRe   = regexp.MustCompile(`\[data-active-phase="([^"]+)"\]`)
	opacityRe = regexp.MustCompile(`(?:^|;)\s*opacity\s*:\s*([0-9.]+)`)
)

// ParseStylesheet extracts opacity rules from raw CSS text.
func ParseStylesheet(css string) *Stylesheet {
	css = commentRe.ReplaceAllString(css, "")
	s := &Stylesheet{}
	i := 0
	for i < len(css) {
		// Skip whitespace.
		for i < len(css) && (css[i] == ' ' || css[i] == '\n' || css[i] == '\t' || css[i] == '\r') {
			i++
		}
		if i >= len(css) {
			break
		}
		if css[i] == '@' {
			// Skip the whole at-rule block (@keyframes, @supports, ...).
			j := strings.IndexByte(css[i:], '{')
			if j < 0 {
				break
			}
			i += j
			i = skipBlock(css, i)
			continue
		}
		open := strings.IndexByte(css[i:], '{')
		if open < 0 {
			break
		}
		selector := strings.TrimSpace(css[i : i+open])
		i += open
		end := skipBlock(css, i)
		body := css[i+1 : end-1]
		i = end
		s.addRule(selector, body)
	}
	return s
}

// skipBlock advances past a balanced {...} block starting at css[i] == '{'.
func skipBlock(css string, i int) int {
	depth := 0
	for ; i < len(css); i++ {
		switch css[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return i
}

func (s *Stylesheet) addRule(selector, body string) {
	s.addStyleRule(selector, body)
	m := opacityRe.FindStringSubmatch(body)
	if m == nil {
		return
	}
	op, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return
	}
	for _, sel := range splitTop(selector, ',') {
		sel = strings.TrimSpace(sel)
		if sel == "" {
			continue
		}
		var phases map[string]bool
		for _, pm := range phaseRe.FindAllStringSubmatch(sel, -1) {
			if phases == nil {
				phases = make(map[string]bool)
			}
			phases[pm[1]] = true
		}
		tokens := splitTopSpace(sel)
		if len(tokens) == 0 {
			continue
		}
		target := tokens[len(tokens)-1]
		matchers := parseTarget(target)
		if len(matchers) == 0 {
			continue
		}
		s.rules = append(s.rules, rule{phases: phases, matchers: matchers, opacity: op})
	}
}

// addStyleRule records the cascadable declarations of each simple selector
// (a single compound: .class, #id, tag, tag.class) of the rule. Selectors
// with combinators or phase guards are left to the opacity evaluator.
func (s *Stylesheet) addStyleRule(selector, body string) {
	var decls [][2]string
	for decl := range strings.SplitSeq(body, ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "!important"))
		if cascadedProps[k] && v != "" {
			decls = append(decls, [2]string{k, v})
		}
	}
	if len(decls) == 0 {
		return
	}
	for _, sel := range splitTop(selector, ',') {
		sel = strings.TrimSpace(sel)
		if sel == "" || len(splitTopSpace(sel)) != 1 || strings.ContainsAny(sel, ":>+~[") {
			continue
		}
		ms, spec, ok := parseCompound(sel)
		if !ok {
			continue
		}
		s.styles = append(s.styles, styleRule{matchers: ms, specificity: spec, order: len(s.styles), decls: decls})
	}
}

// parseCompound parses "tag.class1.class2#id" into matchers that must all
// match, with its CSS specificity (id=100, class=10, tag=1).
func parseCompound(sel string) ([]matcher, int, bool) {
	var ms []matcher
	spec := 0
	i := 0
	for i < len(sel) {
		j := i + 1
		for j < len(sel) && sel[j] != '.' && sel[j] != '#' {
			j++
		}
		part := sel[i:j]
		switch {
		case part[0] == '.':
			ms = append(ms, matcher{kind: matchClass, name: part[1:]})
			spec += 10
		case part[0] == '#':
			ms = append(ms, matcher{kind: matchID, name: part[1:]})
			spec += 100
		case part == "*":
		default:
			if !isIdent(part) {
				return nil, 0, false
			}
			ms = append(ms, matcher{kind: matchTag, name: part})
			spec++
		}
		i = j
	}
	return ms, spec, len(ms) > 0 || sel == "*"
}

// ApplyStylesheet cascades the stylesheet declarations onto the element
// attributes, per CSS precedence: presentation attribute < stylesheet rule
// (by specificity, then source order) < inline style declaration.
func ApplyStylesheet(root *Element, s *Stylesheet) {
	if s == nil || len(s.styles) == 0 {
		return
	}
	root.Walk(func(e *Element) {
		inline := map[string]bool{}
		for decl := range strings.SplitSeq(e.Attrs["style"], ";") {
			if k, _, ok := strings.Cut(decl, ":"); ok {
				inline[strings.TrimSpace(k)] = true
			}
		}
		var matched []styleRule
		for _, r := range s.styles {
			all := true
			for _, m := range r.matchers {
				if !m.matches(e) {
					all = false
					break
				}
			}
			if all {
				matched = append(matched, r)
			}
		}
		// Stable insertion sort by specificity (source order kept).
		for i := 1; i < len(matched); i++ {
			for j := i; j > 0 && matched[j].specificity < matched[j-1].specificity; j-- {
				matched[j], matched[j-1] = matched[j-1], matched[j]
			}
		}
		for _, r := range matched {
			for _, d := range r.decls {
				if !inline[d[0]] {
					e.Attrs[d[0]] = d[1]
				}
			}
		}
	})
}

func parseTarget(t string) []matcher {
	if strings.HasPrefix(t, ":is(") && strings.HasSuffix(t, ")") {
		var out []matcher
		for _, inner := range splitTop(t[4:len(t)-1], ',') {
			out = append(out, parseTarget(strings.TrimSpace(inner))...)
		}
		return out
	}
	switch {
	case strings.HasPrefix(t, "."):
		return []matcher{{kind: matchClass, name: t[1:]}}
	case strings.HasPrefix(t, "#"):
		return []matcher{{kind: matchID, name: t[1:]}}
	case strings.Contains(t, "["):
		open := strings.IndexByte(t, '[')
		closeIdx := strings.IndexByte(t, ']')
		if closeIdx < open {
			return nil
		}
		attr := t[open+1 : closeIdx]
		if eq := strings.IndexByte(attr, '='); eq >= 0 {
			attr = attr[:eq]
		}
		return []matcher{{kind: matchTagAttr, name: t[:open], attr: attr}}
	default:
		if isIdent(t) {
			return []matcher{{kind: matchTag, name: t}}
		}
	}
	return nil
}

func isIdent(s string) bool {
	for _, r := range s {
		if r != '-' && r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return s != ""
}

// splitTop splits on sep at paren/bracket depth zero.
func splitTop(s string, sep byte) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// splitTopSpace splits on whitespace at paren/bracket depth zero.
func splitTopSpace(s string) []string {
	var out []string
	depth := 0
	start := -1
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		}
		if depth == 0 && (c == ' ' || c == '\t' || c == '\n') {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}

// Opacity computes the element's own static opacity for the given active
// phase: the `opacity` presentation attribute first, then unconditional
// rules, then phase-guarded rules (mirroring their higher CSS specificity).
func (s *Stylesheet) Opacity(e *Element, phase string) float64 {
	op := 1.0
	if a, ok := e.Attrs["opacity"]; ok {
		if f, err := strconv.ParseFloat(strings.TrimSpace(a), 64); err == nil {
			op = f
		}
	}
	for _, r := range s.rules {
		if r.phases != nil {
			continue
		}
		if r.matchesAny(e) {
			op = r.opacity
		}
	}
	for _, r := range s.rules {
		if r.phases == nil || !r.phases[phase] {
			continue
		}
		if r.matchesAny(e) {
			op = r.opacity
		}
	}
	return op
}

func (r rule) matchesAny(e *Element) bool {
	for _, m := range r.matchers {
		if m.matches(e) {
			return true
		}
	}
	return false
}

// Visible reports whether the element is statically visible in the given
// phase, taking ancestor opacity into account (opacity 0 on a group hides
// its whole subtree).
func (s *Stylesheet) Visible(e *Element, phase string) bool {
	for cur := e; cur != nil; cur = cur.Parent {
		if s.Opacity(cur, phase) < 0.01 {
			return false
		}
	}
	return true
}
