package normalizer

import (
	"regexp"
	"strconv"
	"strings"
)

// Salary is a pay range. Period is "year", "month" or "hour".
type Salary struct {
	Min, Max float64
	Currency string // ISO 4217
	Period   string
}

const currencyPat = `(?:\$|₹|£|€|\b(?:usd|inr|gbp|eur|cad|aud|sgd|rs\.?))`
const suffixPat = `(?:k|m|l|lpa|lakhs?|lacs?|cr|crores?)`

// A range such as "$88,000—$103,000", "₹12L - ₹18L", "12-18 LPA" or "€48.000—€56.000".
var salaryRe = regexp.MustCompile(`(?i)(` + currencyPat + `)?\s?(\d[\d.,]*)\s?(` + suffixPat + `\b)?\s*(?:-|–|—|to)\s*(` + currencyPat + `)?\s?(\d[\d.,]*)\s?(` + suffixPat + `\b)?`)

var (
	payAnchorRe  = regexp.MustCompile(`(?i)\b(salary|compensation|pay|paid|base|annual|annually|range|ctc|wage|wages|per (year|annum|hour|month)|hourly|monthly|lpa|ote|on-target)\b`)
	hourRe       = regexp.MustCompile(`(?i)(per hour|/\s?hr\b|/\s?hour\b|hourly|an hour|per hr\b)`)
	monthRe      = regexp.MustCompile(`(?i)(per month|monthly|/\s?month\b|a month|per mo\b)`)
	yearRe       = regexp.MustCompile(`(?i)(per year|per annum|annual|annually|yearly|/\s?yr\b|/\s?year\b|p\.a\.|\blpa\b|a year)`)
	bonusAfterRe = regexp.MustCompile(`(?i)^\W{0,3}(?:in\s+)?(?:sign|bonus|equity|stock|rsu|relocation|stipend|allowance|referral)`)
	bonusBefore  = regexp.MustCompile(`(?i)(bonus|equity|stipend|allowance|relocation|referral)\b[^.]{0,20}$`)
	codeRe       = regexp.MustCompile(`(?i)\b(cad|aud|sgd|nzd)\b`)
)

// ParseSalary finds a stated pay range in a job description. It favours
// precision over recall: a range is accepted only when it sits next to pay
// vocabulary, has a currency, has a sensible period and magnitude, and is not
// a bonus or equity figure. Returns nil when nothing qualifies.
func ParseSalary(text string) *Salary {
	for _, m := range salaryRe.FindAllStringSubmatchIndex(text, -1) {
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return text[m[2*i]:m[2*i+1]]
		}
		c1, n1, s1, c2, n2, s2 := g(1), g(2), g(3), g(4), g(5), g(6)

		start, end := m[0]-140, m[1]+140
		if start < 0 {
			start = 0
		}
		if end > len(text) {
			end = len(text)
		}
		window := text[start:end]
		after := text[m[1]:min1(m[1]+60, len(text))]
		before := text[max0(m[0]-50):m[0]]

		if !payAnchorRe.MatchString(window) {
			continue
		}
		if bonusAfterRe.MatchString(text[m[1]:min1(m[1]+25, len(text))]) || bonusBefore.MatchString(before) {
			continue
		}

		cur := currencyOf(firstNonEmpty2(c1, c2), window)
		lakh := isLakhSuffix(s1) || isLakhSuffix(s2)
		if cur == "" && lakh {
			cur = "INR" // "12-18 LPA" carries no symbol
		}
		if cur == "" {
			continue // "2020-2024", "10-15 people"
		}

		lo, ok1 := parseAmount(n1, firstNonEmpty2(s1, s2))
		hi, ok2 := parseAmount(n2, firstNonEmpty2(s2, s1))
		if !ok1 || !ok2 || lo <= 0 || hi < lo {
			continue
		}

		period, explicit := detectPeriod(after, before, lakh)
		if lo == hi && !explicit {
			continue // "$40—$40" is a template artefact, not a range
		}
		if period == "" {
			period = guessPeriod(cur, hi)
		}
		if period == "" || !plausible(lo, hi, period) {
			continue
		}
		return &Salary{Min: lo, Max: hi, Currency: cur, Period: period}
	}
	return nil
}

func firstNonEmpty2(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

func isLakhSuffix(s string) bool {
	switch strings.ToLower(s) {
	case "l", "lpa", "lakh", "lakhs", "lac", "lacs":
		return true
	}
	return false
}

func currencyOf(sym, window string) string {
	switch strings.ToLower(strings.TrimSpace(sym)) {
	case "₹", "inr", "rs", "rs.":
		return "INR"
	case "£", "gbp":
		return "GBP"
	case "€", "eur":
		return "EUR"
	case "usd":
		return "USD"
	case "cad":
		return "CAD"
	case "aud":
		return "AUD"
	case "sgd":
		return "SGD"
	case "$":
		// "$" is ambiguous; an explicit nearby code settles it.
		if m := codeRe.FindString(window); m != "" {
			return strings.ToUpper(m)
		}
		return "USD"
	}
	return ""
}

// parseAmount handles "88,000", "48.000" (European thousands), "120.5", "12" with
// a k / m / L / LPA / cr suffix.
func parseAmount(s, suffix string) (float64, bool) {
	s = strings.Trim(s, ".,")
	var clean string
	switch {
	case regexp.MustCompile(`^\d{1,3}(,\d{3})+(\.\d+)?$`).MatchString(s):
		clean = strings.ReplaceAll(s, ",", "")
	case regexp.MustCompile(`^\d{1,3}(\.\d{3})+(,\d+)?$`).MatchString(s):
		clean = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	case regexp.MustCompile(`^\d+([.,]\d+)?$`).MatchString(s):
		clean = strings.ReplaceAll(s, ",", ".")
	default:
		return 0, false
	}
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(suffix) {
	case "k":
		v *= 1e3
	case "m":
		v *= 1e6
	case "l", "lpa", "lakh", "lakhs", "lac", "lacs":
		v *= 1e5
	case "cr", "crore", "crores":
		v *= 1e7
	}
	return v, true
}

func detectPeriod(after, before string, lakh bool) (period string, explicit bool) {
	switch {
	case hourRe.MatchString(after) || hourRe.MatchString(before):
		return "hour", true
	case monthRe.MatchString(after) || monthRe.MatchString(before):
		return "month", true
	case lakh || yearRe.MatchString(after) || yearRe.MatchString(before):
		return "year", true
	}
	return "", false
}

// guessPeriod covers ranges with a pay anchor but no period word. Large amounts
// are annual; small ones are ambiguous (hourly? monthly?) so they are rejected.
func guessPeriod(currency string, max float64) string {
	threshold := 20000.0
	if currency == "INR" {
		threshold = 300000
	}
	if max >= threshold {
		return "year"
	}
	return ""
}

func plausible(lo, hi float64, period string) bool {
	if hi/lo > 6 {
		return false
	}
	switch period {
	case "year":
		return lo >= 5000 && hi <= 5e8
	case "month":
		return lo >= 500 && hi <= 1e7
	case "hour":
		return lo >= 3 && hi <= 1000
	}
	return false
}
