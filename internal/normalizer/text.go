// Package normalizer turns raw source fields into clean, comparable values.
// Phase 1 covers only text basics; location, experience and salary parsing
// arrive in Phase 3.
package normalizer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

// NormalizeTitle lower-cases and collapses whitespace. Abbreviation expansion
// ("sr" -> "senior") is added in Phase 3.
func NormalizeTitle(s string) string {
	return strings.ToLower(multiWS.ReplaceAllString(strings.TrimSpace(s), " "))
}

// CountryHint returns an ISO country code only when the location text makes it
// obvious. Anything else stays nil until the Phase 3 location normalizer.
func CountryHint(location string) *string {
	if strings.Contains(strings.ToLower(location), "india") {
		c := "IN"
		return &c
	}
	return nil
}

// Fingerprint identifies "the same opening" for dedup. Phase 3 refines it
// (normalized city instead of raw location).
func Fingerprint(companyID int64, titleNormalized, location string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", companyID, titleNormalized, NormalizeTitle(location))))
	return hex.EncodeToString(sum[:])
}
