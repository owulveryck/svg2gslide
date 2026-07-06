package convert

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	urlIDRe  = regexp.MustCompile(`/presentation/(?:u/\d+/)?d/([a-zA-Z0-9_-]+)`)
	bareIDRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{20,}$`)
)

// ExtractPresentationID accepts a docs.google.com presentation URL or a bare
// presentation ID and returns the ID.
func ExtractPresentationID(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("empty presentation URL or ID")
	}
	if m := urlIDRe.FindStringSubmatch(s); m != nil {
		return m[1], nil
	}
	if bareIDRe.MatchString(s) {
		return s, nil
	}
	return "", fmt.Errorf("cannot extract a presentation ID from %q", s)
}
