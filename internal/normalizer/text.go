// Package normalizer turns raw source fields into clean, comparable values:
// plain-text descriptions, normalized titles and seniority, places, experience,
// salary, remote type and employment type. Every function is pure (no I/O), so
// the same rules run at ingestion time and when re-deriving stored jobs.
package normalizer

import (
	"html"
	"regexp"
	"strings"
)

var (
	blockTags = regexp.MustCompile(`(?i)</?(p|div|br|li|ul|ol|h[1-6]|tr|table|section)\b[^>]*>`)
	liOpen    = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	anyTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	spaces    = regexp.MustCompile(`[ \t\f\v\x{00a0}]+`)
	blankRuns = regexp.MustCompile(`\n{3,}`)
	multiWS   = regexp.MustCompile(`\s+`)
)

// HTMLToText converts a job description's HTML to readable plain text.
func HTMLToText(s string) string {
	s = liOpen.ReplaceAllString(s, "\n- ")
	s = blockTags.ReplaceAllString(s, "\n")
	s = anyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = spaces.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	s = blankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
