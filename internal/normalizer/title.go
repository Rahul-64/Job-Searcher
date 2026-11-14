package normalizer

import (
	"regexp"
	"strings"
)

// abbreviations are expanded token by token, so "Sr. SWE" becomes "senior software engineer".
var abbreviations = map[string]string{
	"sr": "senior", "snr": "senior", "jr": "junior", "jnr": "junior",
	"swe": "software engineer", "sde": "software development engineer",
	"sdet": "software development engineer in test", "sre": "site reliability engineer",
	"eng": "engineer", "engr": "engineer", "engg": "engineer",
	"mgr": "manager", "mngr": "manager", "dir": "director",
	"dev": "developer", "asst": "assistant", "assoc": "associate",
	"vp": "vice president", "svp": "senior vice president", "evp": "executive vice president",
	"mktg": "marketing", "admin": "administrator",
}

var titleSplitRe = regexp.MustCompile(`[^a-z0-9+#.]+`)

// NormalizeTitle lower-cases a title, drops punctuation and expands common
// abbreviations. Level markers (II, III, Senior, Staff) are kept: they
// distinguish different jobs, so they must survive into the fingerprint.
func NormalizeTitle(s string) string {
	s = strings.ToLower(strings.ReplaceAll(s, "&", " and "))
	var out []string
	for _, tok := range titleSplitRe.Split(s, -1) {
		tok = strings.Trim(tok, ".") // "sr." -> "sr", but keep "c++", ".net", "node.js"
		if tok == "" {
			continue
		}
		if exp, ok := abbreviations[tok]; ok {
			tok = exp
		}
		out = append(out, tok)
	}
	return strings.Join(out, " ")
}

// Seniority levels, lowest to highest.
const (
	Intern    = "intern"
	Junior    = "junior"
	Mid       = "mid"
	Senior    = "senior"
	Lead      = "lead"
	Staff     = "staff"
	Principal = "principal"
	Director  = "director"
	Executive = "executive"
)

var (
	internRe    = regexp.MustCompile(`\bintern(ship)?s?\b`)
	executiveRe = regexp.MustCompile(`\b(vice president|chief|president|c[etfmopis]o|ciso|cpo)\b`)
	directorRe  = regexp.MustCompile(`\b(director|head of|managing director)\b`)
	principalRe = regexp.MustCompile(`\bprincipal\b`)
	staffRe     = regexp.MustCompile(`\bstaff\b`)
	notStaffRe  = regexp.MustCompile(`\bstaff (accountant|attorney|counsel|nurse|writer|auditor|augmentation|physician)\b`)
	leadRe      = regexp.MustCompile(`\b(lead|tech lead|team lead)\b`)
	notLeadRe   = regexp.MustCompile(`\blead (gen|generation|qualification|capture|nurturing|scoring|development|management)\b`)
	seniorRe    = regexp.MustCompile(`\bsenior\b`)
	juniorRe    = regexp.MustCompile(`\b(junior|graduate|entry level|trainee|apprentice|associate)\b`)
)

// Seniority infers a level from a title that has already been through
// NormalizeTitle. It returns "" when the title carries no level signal, rather
// than guessing "mid". The most senior signal wins ("Associate Director" is a
// director, "Senior Lead Engineer" is a lead).
func Seniority(normTitle string) string {
	t := normTitle
	words := strings.Fields(t)
	last := ""
	if len(words) > 0 {
		last = words[len(words)-1]
	}
	switch {
	case internRe.MatchString(t):
		return Intern
	case executiveRe.MatchString(t):
		return Executive
	case directorRe.MatchString(t):
		return Director
	case principalRe.MatchString(t):
		return Principal
	case staffRe.MatchString(t) && !notStaffRe.MatchString(t):
		return Staff
	case leadRe.MatchString(t) && !notLeadRe.MatchString(t):
		return Lead
	case seniorRe.MatchString(t):
		return Senior
	case hasWord(words, "iii"):
		return Senior
	case hasWord(words, "ii"):
		return Mid
	case juniorRe.MatchString(t), last == "i":
		return Junior
	}
	return ""
}

func hasWord(words []string, w string) bool {
	for _, x := range words {
		if x == w {
			return true
		}
	}
	return false
}

// Employment types.
const (
	FullTime  = "full_time"
	PartTime  = "part_time"
	Contract  = "contract"
	InternEmp = "intern"
	Temporary = "temporary"
)

var (
	contractRe  = regexp.MustCompile(`\b(contract|contractor|freelance)\b`)
	partTimeRe  = regexp.MustCompile(`\bpart[- ]time\b`)
	temporaryRe = regexp.MustCompile(`\b(temporary|temp)\b`)
)

// EmploymentFromTitle infers an employment type from explicit title words only.
func EmploymentFromTitle(normTitle string) string {
	switch {
	case internRe.MatchString(normTitle):
		return InternEmp
	case contractRe.MatchString(normTitle):
		return Contract
	case partTimeRe.MatchString(normTitle):
		return PartTime
	case temporaryRe.MatchString(normTitle):
		return Temporary
	}
	return ""
}

// NormalizeEmployment maps a source's own wording ("Full Time Employee",
// "FullTime", "Contractor") to our values, or "" when unrecognised.
func NormalizeEmployment(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "intern"):
		return InternEmp
	case strings.Contains(l, "part"):
		return PartTime
	case strings.Contains(l, "contract") || strings.Contains(l, "freelance"):
		return Contract
	case strings.Contains(l, "temp"):
		return Temporary
	case strings.Contains(l, "full"):
		return FullTime
	}
	return ""
}
