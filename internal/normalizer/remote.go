package normalizer

import (
	"regexp"
	"strings"
)

// Remote types.
const (
	Remote = "remote"
	Hybrid = "hybrid"
	Onsite = "onsite"
)

var (
	descRemoteRe = regexp.MustCompile(`(?i)\b(fully|100%|entirely|completely)\s+remote\b|\bremote[- ]first\b|\bthis is a remote (role|position|job|opportunity)\b|\bwork (fully )?remotely\b`)
	descHybridRe = regexp.MustCompile(`(?i)\bhybrid (work|working|model|role|position|setup|schedule|environment|policy|arrangement)\b|\bhybrid\b.{0,40}\b(office|days)\b|\b\d days? (a|per) week in (the )?office\b`)
	descOnsiteRe = regexp.MustCompile(`(?i)\b(on-?site|in[- ]office) (role|position|only|required|presence)\b|\bwork from (the |our )?office\b|\bthis is an? (on-?site|in-office)\b`)
	titleRemote  = regexp.MustCompile(`(?i)\bremote\b`)
	titleHybrid  = regexp.MustCompile(`(?i)\bhybrid\b`)
)

// NormalizeRemote maps a source's workplace wording ("Remote", "OnSite",
// "on_site", "hybrid", "unspecified") to remote / hybrid / onsite, or "".
func NormalizeRemote(s string) string {
	l := strings.ToLower(strings.NewReplacer("-", "", "_", "", " ", "").Replace(s))
	switch l {
	case "remote":
		return Remote
	case "hybrid":
		return Hybrid
	case "onsite", "inoffice", "office":
		return Onsite
	}
	return ""
}

// DetectRemote decides the work arrangement. Order of trust: what the source
// states, then the location text, then the title, then explicit description
// phrases. It returns "" rather than guessing "onsite" when nothing says so.
func DetectRemote(sourceHint string, loc LocationResult, title, description string) string {
	if r := NormalizeRemote(sourceHint); r != "" {
		return r
	}
	switch {
	case loc.Hybrid:
		return Hybrid
	case loc.Remote:
		return Remote
	case titleHybrid.MatchString(title):
		return Hybrid
	case titleRemote.MatchString(title):
		return Remote
	case descRemoteRe.MatchString(description):
		return Remote
	case descHybridRe.MatchString(description):
		return Hybrid
	case descOnsiteRe.MatchString(description):
		return Onsite
	}
	return ""
}
