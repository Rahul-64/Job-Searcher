package normalizer

import (
	"regexp"
	"strconv"
	"strings"
)

// Years of experience, e.g. "5+ years", "3-5 yrs", "3 to 5 years", "at least 4 years".
var yearsRe = regexp.MustCompile(`(?i)(\d{1,2})\s*(\+)?\s*(?:(?:-|–|—|to)\s*(\d{1,2})\s*\+?)?\s*(?:years?|yrs?)\b`)

// The phrase must be about the candidate's experience, not e.g. "founded 15 years ago".
var expContextRe = regexp.MustCompile(`(?i)\b(experience|exp|industry|professional|hands-on|working|background|relevant|proven|track record)\b`)

// Words right before the number that make it a time span, not a requirement.
var notRequirementBefore = regexp.MustCompile(`(?i)\b(within|over|next|last|past|every|each|for the|in the)\s+$`)

// Words right after the number that make it an age or a history.
var notRequirementAfter = regexp.MustCompile(`(?i)^\s*(ago|old|of age|in business|of history|and counting|of operation)`)

const maxPlausibleYears = 30

// ParseExperience extracts the experience a job asks for from its description.
// It returns min (and max, when a range such as "3-5 years" is given), or nils
// when the text states nothing usable.
//
// When several requirements appear ("8+ years overall, 2+ years leading teams")
// the largest minimum is taken: that is the overall requirement, the others are
// skill-specific. Fresher / entry-level wording counts as 0.
func ParseExperience(text string) (min, max *int) {
	best := -1
	var bestMax *int

	for _, m := range yearsRe.FindAllStringSubmatchIndex(text, -1) {
		lo, err := strconv.Atoi(text[m[2]:m[3]])
		if err != nil || lo > maxPlausibleYears {
			continue
		}
		var hi *int
		if m[6] >= 0 {
			h, err := strconv.Atoi(text[m[6]:m[7]])
			if err != nil || h < lo || h > maxPlausibleYears {
				continue // "10-5 years" or "2020-2024 years" is not a range
			}
			hi = &h
		}

		start, end := m[0]-70, m[1]+80
		if start < 0 {
			start = 0
		}
		if end > len(text) {
			end = len(text)
		}
		before := text[start:m[0]]
		if !expContextRe.MatchString(text[start:end]) ||
			notRequirementBefore.MatchString(before) ||
			notRequirementAfter.MatchString(text[m[1]:min1(end, len(text))]) {
			continue
		}
		if lo > best {
			best, bestMax = lo, hi
		}
	}
	if best >= 0 {
		return &best, bestMax
	}

	if freshersRe.MatchString(text) {
		zero := 0
		return &zero, nil
	}
	return nil, nil
}

var freshersRe = regexp.MustCompile(`(?i)\b(freshers?|fresh graduates?|entry[- ]level|no (prior )?experience (is )?(required|needed)|0\s*(?:-|–|to)\s*[12]\s*years?)\b`)

func min1(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// formatYears is only used by tests and logs.
func formatYears(min, max *int) string {
	if min == nil {
		return "-"
	}
	s := strconv.Itoa(*min)
	if max != nil {
		s += "-" + strconv.Itoa(*max)
	} else {
		s += "+"
	}
	return strings.TrimSpace(s)
}
