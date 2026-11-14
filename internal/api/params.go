package api

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"jobscraper/internal/store"
)

// paramError is a client mistake: it becomes a 400 with the message.
type paramError struct{ msg string }

func (e *paramError) Error() string { return e.msg }

func badParam(format string, a ...any) error { return &paramError{fmt.Sprintf(format, a...)} }

var (
	remoteValues     = set("remote", "hybrid", "onsite")
	seniorityValues  = set("intern", "junior", "mid", "senior", "lead", "staff", "principal", "director", "executive")
	employmentValues = set("full_time", "part_time", "contract", "intern", "temporary")
	slugRe           = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
	countryRe        = regexp.MustCompile(`^[A-Za-z]{2}$`)
)

func set(vals ...string) map[string]bool {
	m := make(map[string]bool, len(vals))
	for _, v := range vals {
		m[v] = true
	}
	return m
}

const (
	defaultLimit = 20
	maxLimit     = 100
	maxOffset    = 5000
	maxQueryLen  = 200
	maxCityLen   = 60
)

// parseSearch validates the query string of /api/jobs. Unknown parameters are
// ignored; known ones are strictly checked.
func parseSearch(v url.Values) (store.SearchParams, error) {
	p := store.SearchParams{Limit: defaultLimit, Sort: store.SortNewest}

	p.Q = strings.TrimSpace(v.Get("q"))
	if utf8.RuneCountInString(p.Q) > maxQueryLen {
		return p, badParam("q must be at most %d characters", maxQueryLen)
	}

	p.City = strings.TrimSpace(v.Get("city"))
	if utf8.RuneCountInString(p.City) > maxCityLen {
		return p, badParam("city must be at most %d characters", maxCityLen)
	}

	if c := strings.TrimSpace(v.Get("country")); c != "" {
		if !countryRe.MatchString(c) {
			return p, badParam("country must be a two-letter code such as IN")
		}
		p.Country = strings.ToUpper(c)
	}

	var err error
	if p.Remote, err = oneOf(v, "remote", remoteValues); err != nil {
		return p, err
	}
	if p.Seniority, err = oneOf(v, "seniority", seniorityValues); err != nil {
		return p, err
	}
	if p.Employment, err = oneOf(v, "employment", employmentValues); err != nil {
		return p, err
	}

	if c := strings.TrimSpace(v.Get("company")); c != "" {
		if !slugRe.MatchString(c) {
			return p, badParam("company must be a company slug such as okta")
		}
		p.Company = c
	}

	if s := strings.TrimSpace(v.Get("exp")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > 50 {
			return p, badParam("exp must be a whole number of years between 0 and 50")
		}
		p.Exp = &n
	}
	if p.HideUnknownExp, err = boolParam(v, "hide_unknown_exp"); err != nil {
		return p, err
	}
	if p.HasSalary, err = boolParam(v, "has_salary"); err != nil {
		return p, err
	}
	if p.EntryLevel, err = boolParam(v, "entry_level"); err != nil {
		return p, err
	}

	if s := strings.TrimSpace(v.Get("posted_within")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 365 {
			return p, badParam("posted_within must be a number of days between 1 and 365")
		}
		p.PostedWithinDays = n
	}

	switch s := strings.TrimSpace(v.Get("sort")); s {
	case "":
	case store.SortNewest, store.SortRelevance:
		p.Sort = s
	default:
		return p, badParam("sort must be newest or relevance")
	}
	if p.Sort == store.SortNewest && p.Q != "" && strings.TrimSpace(v.Get("sort")) == "" {
		p.Sort = store.SortRelevance // a text search ranks by relevance unless told otherwise
	}

	if s := strings.TrimSpace(v.Get("limit")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			return p, badParam("limit must be between 1 and %d", maxLimit)
		}
		p.Limit = n
	}
	if s := strings.TrimSpace(v.Get("offset")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 || n > maxOffset {
			return p, badParam("offset must be between 0 and %d", maxOffset)
		}
		p.Offset = n
	}
	return p, nil
}

// oneOf returns the lower-cased value of key if it is in allowed, "" if absent.
func oneOf(v url.Values, key string, allowed map[string]bool) (string, error) {
	s := strings.ToLower(strings.TrimSpace(v.Get(key)))
	if s == "" {
		return "", nil
	}
	if !allowed[s] {
		return "", badParam("%s is not a valid value for %s", s, key)
	}
	return s, nil
}

func boolParam(v url.Values, key string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v.Get(key))) {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	}
	return false, badParam("%s must be 1 or 0", key)
}
